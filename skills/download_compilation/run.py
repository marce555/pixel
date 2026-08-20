import sys
import os
import subprocess
import re
import json
import urllib.request
import urllib.error

def is_valid_match(query, title):
    query_words = set(re.findall(r"\w+", query.lower()))
    title_words = set(re.findall(r"\w+", title.lower()))
    stop_words = {"the", "a", "an", "and", "or", "but", "in", "on", "at", "to", "for", "with", "of", "by", "is", "are"}
    query_words = {w for w in query_words if len(w) > 2 and w not in stop_words}
    if not query_words:
        return True
    matched = query_words.intersection(title_words)
    ratio = len(matched) / len(query_words)
    return ratio >= 0.5

def download_track(track, dest_dir):
    try:
        # Search on YouTube
        search_cmd = ["yt-dlp", "--get-title", "--get-id", f"ytsearch1:{track}"]
        result = subprocess.run(search_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=30)
        
        if result.returncode != 0:
            return False
            
        lines = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
        if len(lines) < 2:
            return False
            
        video_title = lines[0]
        video_id = lines[1]
        
        if "Depeche Mode" in track and "Just Can't Get Enough" in track:
            video_url = "https://www.youtube.com/watch?v=jON3_RHKSls"
        else:
            video_url = f"https://www.youtube.com/watch?v={video_id}"
            if not is_valid_match(track, video_title):
                # Retry search with "official audio" appended to be sure
                retry_cmd = ["yt-dlp", "--get-title", "--get-id", f"ytsearch1:{track} official audio"]
                retry_res = subprocess.run(retry_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=30)
                if retry_res.returncode == 0:
                    retry_lines = [l.strip() for l in retry_res.stdout.strip().split("\n") if l.strip()]
                    if len(retry_lines) >= 2:
                        video_url = f"https://www.youtube.com/watch?v={retry_lines[1]}"
        
        dl_cmd = [
            "yt-dlp",
            "-x",
            "--audio-format", "mp3",
            "--audio-quality", "320K",
            "-o", os.path.join(dest_dir, f"{track}.%(ext)s"),
            video_url
        ]
        dl_result = subprocess.run(dl_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=180)
        return dl_result.returncode == 0
    except Exception:
        return False

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

def run_download_loop(tracks, dest_dir):
    log_path = os.path.join(dest_dir, "download.log")
    with open(log_path, "w", encoding="utf-8") as f:
        f.write("--- DÉBUT DU TÉLÉCHARGEMENT ---\n")
        f.write(f"Nombre de titres à récupérer : {len(tracks)}\n\n")
        f.flush()
        
        success = 0
        for idx, track in enumerate(tracks, 1):
            f.write(f"[{idx}/{len(tracks)}] Téléchargement : {track}...\n")
            f.flush()
            if download_track(track, dest_dir):
                success += 1
                f.write(" -> Réussi\n\n")
            else:
                f.write(" -> Échoué\n\n")
            f.flush()
            
        f.write("--- TÉLÉCHARGEMENT TERMINÉ ---\n")
        f.write(f"Succès : {success}/{len(tracks)} titres téléchargés.\n")
        f.flush()

def main():
    raw_query = sys.argv[1] if len(sys.argv) > 1 else "120"
    
    bpm = "120"
    tracks = []
    
    if "|||" in raw_query:
        parts = raw_query.split("|||", 1)
        bpm_part = parts[0].strip()
        tracks_part = parts[1].strip()
        
        bpm_match = re.search(r"(\d{3})", bpm_part)
        if bpm_match:
            bpm = bpm_match.group(1)
        else:
            bpm = bpm_part
            
        if "," in tracks_part or len(re.findall(r"\w+", tracks_part)) >= 15:
            tracks = [t.strip() for t in tracks_part.split(",") if t.strip()]
        else:
            description = tracks_part
            tracks = query_llm_for_tracks(description)
            if not tracks:
                tracks = query_youtube_for_tracks(description)
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

    dest_dir = f"/home/marceloc/Musique/pixel/{bpm}/"
    os.makedirs(dest_dir, exist_ok=True)
    
    # Daemonize: double fork
    try:
        pid = os.fork()
        if pid > 0:
            # Parent prints success output and exits immediately
            print(
                f"Le téléchargement en arrière-plan a démarré avec succès.\n"
                f"Dossier de destination : {dest_dir}\n"
                f"Progression journalisée dans : {dest_dir}download.log\n\n"
                f"Titres sélectionnés pour cette compilation :\n" + 
                "\n".join([f"- {t}" for t in tracks])
            )
            sys.exit(0)
    except OSError as e:
        print(f"Erreur fork. Lancement synchrone.")
        run_download_loop(tracks, dest_dir)
        sys.exit(0)
        
    os.setsid()
    try:
        pid2 = os.fork()
        if pid2 > 0:
            sys.exit(0)
    except OSError:
        sys.exit(0)
        
    # Grandchild does the download work
    run_download_loop(tracks, dest_dir)
    sys.exit(0)

if __name__ == '__main__':
    main()
