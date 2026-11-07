import sys
import os
import subprocess
import re
import json
import urllib.request
import urllib.error
from concurrent.futures import ThreadPoolExecutor, as_completed

USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

def is_valid_match(query, title):
    query_words = set(re.findall(r"\w+", query.lower()))
    title_words = set(re.findall(r"\w+", title.lower()))
    stop_words = {"the", "a", "an", "and", "or", "but", "in", "on", "at", "to", "for", "with", "of", "by", "is", "are", "mix", "remix", "version", "edit", "extended", "radio", "club", "original"}
    important_words = {w for w in query_words if len(w) > 2 and w not in stop_words and not w.isdigit()}
    if not important_words:
        important_words = {w for w in query_words if len(w) > 1}
    if not important_words:
        return True
    matched = important_words.intersection(title_words)
    ratio = len(matched) / len(important_words)
    return ratio >= 0.4

def parse_duration_seconds(length_str):
    if not length_str:
        return 0
    parts = [int(p) for p in re.findall(r"\d+", length_str)]
    if len(parts) == 2:
        return parts[0] * 60 + parts[1]
    if len(parts) == 3:
        return parts[0] * 3600 + parts[1] * 60 + parts[2]
    return 0

def search_youtube_innertube(query, timeout=8):
    payload = {
        "context": {
            "client": {
                "clientName": "WEB",
                "clientVersion": "2.20261021.01.00",
                "hl": "fr",
                "gl": "FR",
            }
        },
        "query": query,
    }
    req = urllib.request.Request(
        "https://www.youtube.com/youtubei/v1/search?prettyPrint=false",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "User-Agent": USER_AGENT,
            "Accept-Language": "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7",
        },
        method="POST",
    )
    candidates = []
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read().decode("utf-8", errors="ignore"))

        def walk(obj):
            if isinstance(obj, dict):
                if "videoRenderer" in obj:
                    vr = obj["videoRenderer"]
                    vid = vr.get("videoId", "")
                    runs = vr.get("title", {}).get("runs", [])
                    title = "".join(r.get("text", "") for r in runs).strip()
                    length_txt = vr.get("lengthText", {}).get("simpleText", "")
                    if vid and title:
                        candidates.append((vid, title, parse_duration_seconds(length_txt)))
                else:
                    for v in obj.values():
                        walk(v)
            elif isinstance(obj, list):
                for item in obj:
                    walk(item)

        walk(data)
    except Exception:
        pass

    # Filter reasonable track lengths (60s to 900s) first
    for vid, title, dur in candidates:
        if (dur == 0 or 60 <= dur <= 900) and is_valid_match(query, title):
            return f"https://www.youtube.com/watch?v={vid}", title
    if candidates:
        return f"https://www.youtube.com/watch?v={candidates[0][0]}", candidates[0][1]
    return None, None

def sanitize_filename(name):
    cleaned = re.sub(r'[\\/*?:"<>|]', "_", name).strip()
    return re.sub(r"\s+", " ", cleaned)

def download_track(track, dest_dir):
    safe_track = sanitize_filename(track)
    final_mp3 = os.path.join(dest_dir, f"{safe_track}.mp3")
    if os.path.exists(final_mp3) and os.path.getsize(final_mp3) > 10000:
        return True, final_mp3

    try:
        if "Depeche Mode" in track and "Just Can't Get Enough" in track:
            video_url = "https://www.youtube.com/watch?v=jON3_RHKSls"
        else:
            video_url, _ = search_youtube_innertube(track)
            if not video_url:
                # Fallback to yt-dlp search
                search_cmd = ["yt-dlp", "--get-title", "--get-id", f"ytsearch1:{track}"]
                result = subprocess.run(search_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=30)
                if result.returncode != 0:
                    return False, "Recherche échouée"
                lines = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
                if len(lines) < 2:
                    return False, "Aucun résultat"
                video_title, video_id = lines[0], lines[1]
                video_url = f"https://www.youtube.com/watch?v={video_id}"
                if not is_valid_match(track, video_title):
                    retry_cmd = ["yt-dlp", "--get-title", "--get-id", f"ytsearch1:{track} audio"]
                    retry_res = subprocess.run(retry_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=30)
                    if retry_res.returncode == 0:
                        retry_lines = [l.strip() for l in retry_res.stdout.strip().split("\n") if l.strip()]
                        if len(retry_lines) >= 2:
                            video_url = f"https://www.youtube.com/watch?v={retry_lines[1]}"

        dl_cmd = [
            "yt-dlp",
            "--no-playlist",
            "-x",
            "--audio-format", "mp3",
            "--audio-quality", "320K",
            "-o", os.path.join(dest_dir, f"{safe_track}.%(ext)s"),
            video_url
        ]
        dl_result = subprocess.run(dl_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=180)
        if dl_result.returncode == 0 and os.path.exists(final_mp3):
            return True, final_mp3

        # Fallback direct ytsearch1 download if video_url failed (e.g. age-restricted/unavailable)
        fallback_cmd = [
            "yt-dlp",
            "--no-playlist",
            "-x",
            "--audio-format", "mp3",
            "--audio-quality", "320K",
            "-o", os.path.join(dest_dir, f"{safe_track}.%(ext)s"),
            f"ytsearch1:{track} audio"
        ]
        fb_result = subprocess.run(fallback_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=180)
        return fb_result.returncode == 0 and os.path.exists(final_mp3), final_mp3
    except Exception as e:
        return False, str(e)

def query_llm_for_tracks(description):
    try:
        core_mem_path = "/home/marceloc/Documents/Pixel/pixel_core_memory.json"
        with open(core_mem_path, "r", encoding="utf-8") as f:
            data = json.load(f)
        settings = data.get("llm_settings", {})
        base_url = settings.get("local_base_url", "http://127.0.0.1:52625/v1")
        model = settings.get("local_model", "qwen3.5:9b")
    except Exception:
        base_url = "http://127.0.0.1:52625/v1"
        model = "qwen3.5:9b"

    url = f"{base_url.rstrip('/')}/chat/completions"

    prompt = (
        f"Donne-moi une liste d'au moins 20 titres de chansons correspondant à cette demande : '{description}'. "
        "Chaque titre doit être au format 'Artiste - Titre'. Réponds uniquement par la liste des titres séparés par des virgules, "
        "sans introduction, sans numéros, sans conclusion, et sans bloc de code markdown. "
        "Exemple: Gloria Gaynor - I Will Survive, Chic - Le Freak, Bee Gees - Stayin' Alive"
    )

    headers = {"Content-Type": "application/json"}
    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": "Tu es un expert en musique. Tu réponds uniquement par des listes séparées par des virgules."},
            {"role": "user", "content": prompt}
        ],
        "temperature": 0.2
    }

    try:
        req = urllib.request.Request(url, data=json.dumps(body).encode("utf-8"), headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=30) as response:
            resp_data = json.loads(response.read().decode("utf-8"))
            content = resp_data["choices"][0]["message"]["content"].strip()
            tracks = [t.strip() for t in content.split(",") if t.strip()]
            if len(tracks) >= 5:
                return tracks
    except Exception:
        pass
    return []

def query_youtube_for_tracks(description):
    try:
        search_cmd = ["yt-dlp", "--get-title", f"ytsearch25:{description}"]
        result = subprocess.run(search_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=40)
        if result.returncode == 0:
            lines = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
            tracks = []
            for line in lines:
                if any(word in line.lower() for word in ["nonstop", "non-stop", "2h", "1h", "compilation", "full album", "dj mix", "playlist", "mix"]):
                    continue
                tracks.append(line)
            if tracks:
                return tracks[:20]
    except Exception:
        pass
    return []

def parse_tracks_list(text):
    lines = [l.strip() for l in text.splitlines() if l.strip()]
    if len(lines) > 1 or re.match(r"^\d+[\.\)\-]\s*", text.strip()):
        # Split by newline or numbered items
        items = re.split(r"(?:\r?\n)+|(?=\b\d{1,2}[\.\)]\s*[A-Za-z0-9])", text)
        tracks = []
        for item in items:
            cleaned = re.sub(r"^\s*\d{1,3}[\.\)\-]\s*", "", item.strip()).strip()
            cleaned = re.sub(r"[\s\-]+$", "", cleaned).strip()
            if cleaned and "-" in cleaned:
                tracks.append(cleaned)
        if tracks:
            return tracks
    if "," in text or len(re.findall(r"\w+", text)) >= 15:
        return [re.sub(r"^\s*\d{1,3}[\.\)\-]\s*", "", t.strip()).strip() for t in text.split(",") if t.strip()]
    return []

def run_download_loop(tracks, dest_dir, parallel_workers=6):
    log_path = os.path.join(dest_dir, "download.log")
    with open(log_path, "w", encoding="utf-8") as f:
        f.write("--- DÉBUT DU TÉLÉCHARGEMENT ---\n")
        f.write(f"Dossier : {dest_dir}\n")
        f.write(f"Nombre de titres à récupérer : {len(tracks)}\n\n")
        f.flush()

        success = 0
        failed_tracks = []
        with ThreadPoolExecutor(max_workers=parallel_workers) as executor:
            future_to_track = {
                executor.submit(download_track, track, dest_dir): (idx, track)
                for idx, track in enumerate(tracks, 1)
            }
            for future in as_completed(future_to_track):
                idx, track = future_to_track[future]
                ok, info = future.result()
                if ok:
                    success += 1
                    msg = f"[{idx}/{len(tracks)}] Téléchargement : {track} -> Réussi\n"
                else:
                    failed_tracks.append(track)
                    msg = f"[{idx}/{len(tracks)}] Téléchargement : {track} -> Échoué ({info})\n"
                f.write(msg)
                f.flush()

        f.write("\n--- TÉLÉCHARGEMENT TERMINÉ ---\n")
        f.write(f"Succès : {success}/{len(tracks)} titres téléchargés.\n")
        if failed_tracks:
            f.write("Titres échoués :\n" + "\n".join(f"- {t}" for t in failed_tracks) + "\n")
        f.flush()
    return success, failed_tracks

def main():
    args = sys.argv[1:]
    sync_mode = False
    custom_dest = None
    filtered_args = []

    i = 0
    while i < len(args):
        if args[i] == "--sync":
            sync_mode = True
            i += 1
        elif args[i] == "--dest" and i + 1 < len(args):
            custom_dest = os.path.expanduser(args[i + 1])
            i += 2
        else:
            filtered_args.append(args[i])
            i += 1

    raw_query = " ".join(filtered_args) if filtered_args else "120"

    bpm = "120"
    tracks = []
    explicit_list = False

    if "|||" in raw_query:
        parts = raw_query.split("|||", 1)
        bpm_part = parts[0].strip()
        tracks_part = parts[1].strip()

        if bpm_part.startswith("/") or bpm_part.startswith("~"):
            custom_dest = os.path.expanduser(bpm_part)
        else:
            bpm_match = re.search(r"(\d{3})", bpm_part)
            if bpm_match:
                bpm = bpm_match.group(1)
            elif bpm_part:
                bpm = bpm_part

        parsed = parse_tracks_list(tracks_part)
        if parsed:
            tracks = parsed
            explicit_list = True
        else:
            description = tracks_part
            tracks = query_llm_for_tracks(description)
            if not tracks:
                tracks = query_youtube_for_tracks(description)
    else:
        parsed = parse_tracks_list(raw_query)
        if parsed:
            tracks = parsed
            explicit_list = True
        else:
            bpm_match = re.search(r"(\d{3})", raw_query)
            if bpm_match:
                bpm = bpm_match.group(1)

            tracks = [
                "Whitney Houston - I Wanna Dance with Somebody",
                "Madonna - Material Girl",
                "Madonna - Into the Groove",
                "Cyndi Lauper - Girls Just Want to Have Fun",
                "Kool & The Gang - Celebration",
                "Lipps Inc. - Funkytown",
                "Michael Jackson - Wanna Be Startin' Somethin'",
                "Gloria Estefan - Conga",
                "Paula Abdul - Cold Hearted",
                "Jermaine Stewart - We Don't Have to Take Our Clothes Off",
                "Rockwell - Somebody's Watching Me",
                "Eurythmics - Sweet Dreams (Are Made of This)",
                "Cutting Crew - (I Just) Died in Your Arms",
                "Bananarama - Venus",
                "Prince - When Doves Cry",
                "Dead or Alive - You Spin Me Round (Like a Record)",
                "Duran Duran - Hungry Like the Wolf",
                "Wang Chung - Everybody Have Fun Tonight",
                "New Order - Blue Monday",
                "Laura Branigan - Gloria",
                "Tiffany - I Think We're Alone Now"
            ]

    if not tracks:
        tracks = [
            "Whitney Houston - I Wanna Dance with Somebody",
            "Madonna - Material Girl",
            "Madonna - Into the Groove",
            "Cyndi Lauper - Girls Just Want to Have Fun",
            "Kool & The Gang - Celebration"
        ]

    # Deduplicate tracks while preserving order
    seen = set()
    unique_tracks = []
    for t in tracks:
        clean_t = t.strip()
        if clean_t and clean_t.lower() not in seen:
            seen.add(clean_t.lower())
            unique_tracks.append(clean_t)

    tracks = unique_tracks if explicit_list else unique_tracks[:10]

    dest_dir = custom_dest if custom_dest else f"/home/marceloc/Musique/pixel/{bpm}/"
    os.makedirs(dest_dir, exist_ok=True)

    if sync_mode:
        print(f"Téléchargement synchrone de {len(tracks)} titres vers {dest_dir}...")
        sys.stdout.flush()
        success, failed = run_download_loop(tracks, dest_dir)
        print(f"Terminé : {success}/{len(tracks)} titres téléchargés dans {dest_dir}.")
        if failed:
            print("Échecs :", ", ".join(failed))
        sys.exit(0 if success > 0 else 1)

    # Daemonize: double fork
    try:
        pid = os.fork()
        if pid > 0:
            print(
                f"Le téléchargement en arrière-plan a démarré avec succès.\n"
                f"Dossier de destination : {dest_dir}\n"
                f"Progression journalisée dans : {os.path.join(dest_dir, 'download.log')}\n\n"
                f"Titres sélectionnés ({len(tracks)}) :\n" +
                "\n".join([f"- {t}" for t in tracks])
            )
            sys.stdout.flush()
            sys.exit(0)
    except OSError as e:
        print(f"Erreur fork : {e}. Lancement synchrone.")
        run_download_loop(tracks, dest_dir)
        sys.exit(0)

    os.setsid()
    try:
        pid2 = os.fork()
        if pid2 > 0:
            sys.exit(0)
    except OSError:
        sys.exit(0)

    sys.stdout.flush()
    sys.stderr.flush()
    try:
        devnull = os.open(os.devnull, os.O_RDWR)
        log_fd = os.open(os.path.join(dest_dir, "download.log"), os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o644)
        os.dup2(devnull, 0)
        os.dup2(log_fd, 1)
        os.dup2(log_fd, 2)
        os.close(devnull)
        os.close(log_fd)
    except Exception:
        pass

    run_download_loop(tracks, dest_dir)
    sys.exit(0)

if __name__ == '__main__':
    main()
