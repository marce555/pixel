#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Skill: dj_remix
Recherche et lance sur YouTube des versions "Remix" (versions revisitées, ex:
"She Drives Me Crazy (The Justin Strauss Remix) - Fine Young Cannibals")
en mode Radio DJ dans le lecteur musical de Pixel avec annonces vocales DJ,
ducking du volume à 20% pendant la voix et enchaînement direct à 100%.
"""

import html
import json
import random
import re
import sys
import urllib.parse
import urllib.request

USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

# Catalogue de référence de versions revisitées ("Remix") emblématiques sur YouTube
CURATED_REMIXES = [
    {
        "title": "She Drives Me Crazy (The Justin Strauss Remix)",
        "base_title": "She Drives Me Crazy",
        "artist": "Fine Young Cannibals",
        "remix": "The Justin Strauss Remix",
        "query": "She Drives Me Crazy (The Justin Strauss Remix) - Fine Young Cannibals",
        "tags": ["80s", "new wave", "pop", "rock", "fine young cannibals", "she drives me crazy", "justin strauss"],
    },
    {
        "title": "Enjoy the Silence (Hands and Feet Mix)",
        "base_title": "Enjoy the Silence",
        "artist": "Depeche Mode",
        "remix": "Hands and Feet Mix",
        "query": "Enjoy the Silence (Hands and Feet Mix) - Depeche Mode",
        "tags": ["80s", "90s", "synthpop", "new wave", "electro", "depeche mode", "enjoy the silence"],
    },
    {
        "title": "Blue Monday (Moreno J Remix)",
        "base_title": "Blue Monday",
        "artist": "New Order",
        "remix": "Moreno J Remix",
        "query": "Blue Monday (Moreno J Remix) - New Order",
        "tags": ["80s", "synthpop", "new wave", "electro", "dance", "new order", "blue monday"],
    },
    {
        "title": "West End Girls (Shep Pettibone Mastermix Remix)",
        "base_title": "West End Girls",
        "artist": "Pet Shop Boys",
        "remix": "Shep Pettibone Mastermix Remix",
        "query": "West End Girls (Shep Pettibone Mastermix) - Pet Shop Boys",
        "tags": ["80s", "synthpop", "pop", "dance", "pet shop boys", "west end girls", "shep pettibone"],
    },
    {
        "title": "Need You Tonight (Mendelsohn Extended Mix)",
        "base_title": "Need You Tonight",
        "artist": "INXS",
        "remix": "Mendelsohn Extended Mix",
        "query": "Need You Tonight (Mendelsohn Extended Mix) - INXS",
        "tags": ["80s", "rock", "funk", "pop", "inxs", "need you tonight"],
    },
    {
        "title": "Into the Groove (Shep Pettibone Remix)",
        "base_title": "Into the Groove",
        "artist": "Madonna",
        "remix": "Shep Pettibone Remix",
        "query": "Into the Groove (Shep Pettibone Remix) - Madonna",
        "tags": ["80s", "pop", "dance", "disco", "madonna", "into the groove", "shep pettibone"],
    },
    {
        "title": "Everybody Wants to Rule the World (Moreno J Remix)",
        "base_title": "Everybody Wants to Rule the World",
        "artist": "Tears for Fears",
        "remix": "Moreno J Remix",
        "query": "Everybody Wants to Rule the World (Moreno J Remix) - Tears for Fears",
        "tags": ["80s", "new wave", "pop", "synthpop", "tears for fears", "everybody wants to rule the world"],
    },
    {
        "title": "The Reflex (Dance Mix - Nile Rodgers Remix)",
        "base_title": "The Reflex",
        "artist": "Duran Duran",
        "remix": "Nile Rodgers Dance Mix",
        "query": "The Reflex (Dance Mix) - Duran Duran",
        "tags": ["80s", "new wave", "pop", "funk", "duran duran", "the reflex", "nile rodgers"],
    },
    {
        "title": "Sweet Dreams (Are Made of This) (Hot Remix)",
        "base_title": "Sweet Dreams (Are Made of This)",
        "artist": "Eurythmics",
        "remix": "Hot Remix",
        "query": "Sweet Dreams (Are Made of This) (Hot Remix) - Eurythmics",
        "tags": ["80s", "synthpop", "electro", "dance", "eurythmics", "sweet dreams"],
    },
    {
        "title": "Personal Jesus (Pump Mix - François Kevorkian Remix)",
        "base_title": "Personal Jesus",
        "artist": "Depeche Mode",
        "remix": "François Kevorkian Pump Mix",
        "query": "Personal Jesus (Pump Mix) - Depeche Mode",
        "tags": ["80s", "90s", "synthpop", "rock", "electro", "depeche mode", "personal jesus"],
    },
    {
        "title": "Don't You (Forget About Me) (12\" Extended Remix)",
        "base_title": "Don't You (Forget About Me)",
        "artist": "Simple Minds",
        "remix": "12\" Extended Remix",
        "query": "Don't You (Forget About Me) (Extended Mix) - Simple Minds",
        "tags": ["80s", "new wave", "rock", "pop", "simple minds", "dont you forget about me"],
    },
    {
        "title": "Smalltown Boy (12\" Extended Remix)",
        "base_title": "Smalltown Boy",
        "artist": "Bronski Beat",
        "remix": "12\" Extended Remix",
        "query": "Smalltown Boy (12\" Extended Mix) - Bronski Beat",
        "tags": ["80s", "synthpop", "hi-nrg", "dance", "bronski beat", "smalltown boy"],
    },
    {
        "title": "Tainted Love (Extended Dance Mix)",
        "base_title": "Tainted Love",
        "artist": "Soft Cell",
        "remix": "Extended Dance Mix",
        "query": "Tainted Love (Extended Dance Mix) - Soft Cell",
        "tags": ["80s", "synthpop", "new wave", "soft cell", "tainted love"],
    },
    {
        "title": "Lullaby (Extended Mix)",
        "base_title": "Lullaby",
        "artist": "The Cure",
        "remix": "Extended Mix",
        "query": "Lullaby (Extended Mix) - The Cure",
        "tags": ["80s", "new wave", "rock", "post-punk", "the cure", "lullaby"],
    },
    {
        "title": "Relax (New York Mix)",
        "base_title": "Relax",
        "artist": "Frankie Goes to Hollywood",
        "remix": "New York Mix",
        "query": "Relax (New York Mix) - Frankie Goes to Hollywood",
        "tags": ["80s", "synthpop", "dance", "hi-nrg", "frankie goes to hollywood", "relax"],
    },
    {
        "title": "Situation (François Kevorkian US 12\" Remix)",
        "base_title": "Situation",
        "artist": "Yazoo",
        "remix": "François Kevorkian US 12\" Remix",
        "query": "Situation (US 12\" Mix) - Yazoo",
        "tags": ["80s", "synthpop", "electro", "dance", "yazoo", "situation"],
    },
    {
        "title": "Missing (Todd Terry Club Mix)",
        "base_title": "Missing",
        "artist": "Everything But The Girl",
        "remix": "Todd Terry Club Mix",
        "query": "Missing (Todd Terry Club Mix) - Everything But The Girl",
        "tags": ["90s", "house", "dance", "pop", "everything but the girl", "missing", "todd terry"],
    },
    {
        "title": "Brimful of Asha (Norman Cook Remix)",
        "base_title": "Brimful of Asha",
        "artist": "Cornershop",
        "remix": "Norman Cook Remix",
        "query": "Brimful of Asha (Norman Cook Remix) - Cornershop",
        "tags": ["90s", "indie", "rock", "big beat", "cornershop", "brimful of asha", "norman cook", "fatboy slim"],
    },
    {
        "title": "Sing It Back (Boris Dlugosch Musical Mix)",
        "base_title": "Sing It Back",
        "artist": "Moloko",
        "remix": "Boris Dlugosch Musical Mix",
        "query": "Sing It Back (Boris Dlugosch Mix) - Moloko",
        "tags": ["90s", "house", "disco", "dance", "moloko", "sing it back"],
    },
    {
        "title": "Cosmic Girl (David Morales Classic Mix)",
        "base_title": "Cosmic Girl",
        "artist": "Jamiroquai",
        "remix": "David Morales Classic Mix",
        "query": "Cosmic Girl (Classic Mix) - Jamiroquai",
        "tags": ["90s", "funk", "disco", "house", "jamiroquai", "cosmic girl", "david morales"],
    },
    {
        "title": "Professional Widow (Armand Van Helden's Star Trunk Mix)",
        "base_title": "Professional Widow",
        "artist": "Tori Amos",
        "remix": "Armand Van Helden's Star Trunk Mix",
        "query": "Professional Widow (Armand Van Helden Remix) - Tori Amos",
        "tags": ["90s", "house", "dance", "electro", "tori amos", "professional widow", "armand van helden"],
    },
    {
        "title": "19-2000 (Soulchild Remix)",
        "base_title": "19-2000",
        "artist": "Gorillaz",
        "remix": "Soulchild Remix",
        "query": "19-2000 (Soulchild Remix) - Gorillaz",
        "tags": ["2000s", "pop", "hip hop", "funk", "gorillaz", "19-2000", "soulchild"],
    },
    {
        "title": "I Follow Rivers (The Magician Remix)",
        "base_title": "I Follow Rivers",
        "artist": "Lykke Li",
        "remix": "The Magician Remix",
        "query": "I Follow Rivers (The Magician Remix) - Lykke Li",
        "tags": ["2010s", "pop", "dance", "house", "lykke li", "i follow rivers", "the magician"],
    },
    {
        "title": "A Little Less Conversation (JXL Remix)",
        "base_title": "A Little Less Conversation",
        "artist": "Elvis Presley",
        "remix": "JXL Remix",
        "query": "A Little Less Conversation (JXL Remix) - Elvis Presley",
        "tags": ["2000s", "rock", "funk", "big beat", "elvis presley", "jxl"],
    },
    {
        "title": "Let's Dance (12\" Extended Remix)",
        "base_title": "Let's Dance",
        "artist": "David Bowie",
        "remix": "12\" Extended Remix",
        "query": "Let's Dance (12\" Extended Mix) - David Bowie",
        "tags": ["80s", "pop", "rock", "funk", "dance", "david bowie", "lets dance"],
    },
]

REMIX_KEYWORDS = [
    "remix",
    "rmx",
    "extended",
    "rework",
    "revisited",
    "club mix",
    "dance mix",
    "dub mix",
    "mastermix",
    "12\"",
    "12 inch",
    "12-inch",
    "mix",
    "version",
    "edit",
]

EXCLUDED_COMPILATION_KEYWORDS = [
    "megamix",
    "nonstop",
    "non-stop",
    "marathon",
    "full album",
    "compilation",
    "1 hour",
    "2 hours",
    "3 hours",
    "4 hours",
    "7 hours",
    "karaoke",
    "tutorial",
    "how to",
    "reaction",
    "guitar lesson",
]


def parse_duration_seconds(dur_str: str) -> int:
    if not dur_str:
        return 0
    parts = [p.strip() for p in dur_str.split(":")]
    if not all(p.isdigit() for p in parts):
        return 0
    nums = [int(p) for p in parts]
    if len(nums) == 2:
        return nums[0] * 60 + nums[1]
    if len(nums) == 3:
        return nums[0] * 3600 + nums[1] * 60 + nums[2]
    return 0


def clean_video_noise(text: str) -> str:
    """Supprime les mentions parasites ([Official Video], [HD], (1989), etc.) tout en conservant le nom du Remix."""
    cleaned = html.unescape(text)
    # Normaliser les tirets longs (en-dash, em-dash) en tiret standard
    cleaned = re.sub(r"\s*[–—]\s*", " - ", cleaned)
    # Couper les suffixes promotionnels après une barre verticale '|'
    if " | " in cleaned:
        cleaned = cleaned.split(" | ", 1)[0]
    # Retirer les emojis courants des titres YouTube
    cleaned = re.sub(r"[\U00010000-\U0010ffff]", "", cleaned)
    cleaned = re.sub(
        r"[\(\[]\s*(?:official\s*(?:music\s*)?(?:video|audio)|audio\s*officiel|clip\s*officiel|hd|hq|4k|remaster(?:ed)?(?:\s*\d{4})?|\d{4}|lyrics?|paroles)\s*[\)\]]",
        "",
        cleaned,
        flags=re.IGNORECASE,
    )
    cleaned = re.sub(r"\s+", " ", cleaned).strip(" -|")
    return cleaned


def parse_remix_video_title(raw_title: str, channel: str = "") -> dict:
    """
    Analyse un titre YouTube de remix et extrait (title, base_title, artist, remix, query).
    Gère les deux formats :
    - 'She Drives Me Crazy (The Justin Strauss Remix) - Fine Young Cannibals'
    - 'Fine Young Cannibals - She Drives Me Crazy (The Justin Strauss Remix)'
    """
    cleaned = clean_video_noise(raw_title)

    # Extraire l'indication du remix entre parenthèses ou crochets
    remix_label = "Remix"
    paren_matches = re.findall(r"[\(\[]([^\)\]]+)[\)\]]", cleaned)
    for pm in paren_matches:
        pm_lower = pm.lower()
        if any(k in pm_lower for k in REMIX_KEYWORDS):
            remix_label = pm.strip()
            break

    artist = ""
    song_part = cleaned

    # Chercher un séparateur " - " situé HORS des parenthèses () ou crochets []
    split_idx = -1
    depth = 0
    for i in range(len(cleaned) - 2):
        ch = cleaned[i]
        if ch in "([":
            depth += 1
        elif ch in ")]":
            depth = max(0, depth - 1)
        elif depth == 0 and cleaned[i : i + 3] == " - ":
            split_idx = i
            break

    if split_idx != -1:
        left = cleaned[:split_idx].strip()
        right = cleaned[split_idx + 3 :].strip()
        left_has_remix = any(k in left.lower() for k in ["remix", "rmx", "extended", "mix", "rework"])
        right_has_remix = any(k in right.lower() for k in ["remix", "rmx", "extended", "mix", "rework"])

        if left_has_remix and not right_has_remix:
            # Format: "She Drives Me Crazy (The Justin Strauss Remix) - Fine Young Cannibals"
            song_part = left
            artist = right
        else:
            # Format: "Fine Young Cannibals - She Drives Me Crazy (The Justin Strauss Remix)"
            artist = left
            song_part = right
    elif channel:
        artist = re.sub(r"\s*-\s*Topic$", "", channel, flags=re.IGNORECASE).strip()

    base_title = re.sub(r"\s*[\(\[][^\)\]]*[\)\]]", "", song_part).strip()
    if not base_title:
        base_title = song_part

    # S'assurer que song_part contient la mention du remix entre parenthèses
    if not any(k in song_part.lower() for k in ["remix", "rmx", "extended", "mix", "rework"]):
        title_with_remix = f"{base_title} ({remix_label})"
    else:
        title_with_remix = song_part

    if artist:
        formatted_query = f"{title_with_remix} - {artist}"
    else:
        formatted_query = title_with_remix

    return {
        "title": title_with_remix,
        "base_title": base_title,
        "artist": artist,
        "remix": remix_label,
        "query": formatted_query,
    }


def search_youtube_innertube(search_query: str, timeout: int = 7) -> list[dict]:
    """Interroge l'API YouTube InnerTube et retourne les vidéos de type Remix individuelles."""
    payload = {
        "context": {
            "client": {
                "clientName": "WEB",
                "clientVersion": "2.20261021.01.00",
                "hl": "fr",
                "gl": "FR",
            }
        },
        "query": search_query,
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

    raw_videos = []
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
                    owner_runs = vr.get("ownerText", {}).get("runs", [])
                    channel = "".join(r.get("text", "") for r in owner_runs).strip()
                    if vid and title:
                        raw_videos.append(
                            {
                                "video_id": vid,
                                "raw_title": title,
                                "duration_str": length_txt,
                                "duration_sec": parse_duration_seconds(length_txt),
                                "channel": channel,
                            }
                        )
                else:
                    for v in obj.values():
                        walk(v)
            elif isinstance(obj, list):
                for item in obj:
                    walk(item)

        walk(data)
    except Exception:
        pass

    results = []
    seen_ids = set()
    for v in raw_videos:
        vid = v["video_id"]
        if vid in seen_ids:
            continue
        seen_ids.add(vid)

        t_lower = v["raw_title"].lower()
        dur = v["duration_sec"]

        # Exclure les compilations longues (> 15 min) ou les shorts (< 1m45s)
        if dur > 0 and (dur < 105 or dur > 900):
            continue
        if any(bad in t_lower for bad in EXCLUDED_COMPILATION_KEYWORDS):
            continue

        # Doit obligatoirement être un Remix / Version revisitée
        if not any(rk in t_lower for rk in REMIX_KEYWORDS):
            continue

        parsed = parse_remix_video_title(v["raw_title"], v["channel"])
        score = 0
        if "remix" in t_lower or "rmx" in t_lower:
            score += 150
        elif "extended" in t_lower or "rework" in t_lower or "mix" in t_lower:
            score += 100
        if re.search(r"[\(\[][^\)\]]*(?:remix|rmx|mix|extended|rework)[^\)\]]*[\)\]]", t_lower):
            score += 80
        if 200 <= dur <= 570:
            score += 40

        results.append(
            {
                "title": parsed["title"],
                "base_title": parsed["base_title"],
                "artist": parsed["artist"],
                "remix": parsed["remix"],
                "query": parsed["query"],
                "url": f"https://www.youtube.com/watch?v={vid}&autoplay=1",
                "score": score,
            }
        )

    results.sort(key=lambda x: x["score"], reverse=True)
    return results


def extract_remix_subject(raw_query: str) -> str:
    """
    Extrait l'artiste, le titre ou le style demandé après 'lance moi un remix' (ou retourne '' si générique).
    """
    q = raw_query.strip()
    # Retirer les préfixes de déclenchement courants
    patterns = [
        r"^(?:pixel\s*[,:]?\s*)?(?:s'il\s+te\s+pla[iî]t\s+)?(?:lance|lances|mets|met|joue|joues|fais(?:-|\s+)moi\s+[ée]couter|fais\s+[ée]couter|[ée]coute|balance|diffuse)(?:-|\s+)(?:moi\s+)?(?:un|une|des|le|la|les|quelques)?\s*(?:musiques?\s+en\s+mode\s+dj\s+|mode\s+dj\s+)?(?:versions?\s+revisit[ée]e?s?|remix(?:es|e)?|re-?mix)\b",
        r"^(?:pixel\s*[,:]?\s*)?(?:en\s+)?mode\s+dj\s*(?:remix)?\b",
        r"^remix\b",
    ]
    cleaned = q
    for pat in patterns:
        cleaned = re.sub(pat, "", cleaned, flags=re.IGNORECASE).strip()

    # Retirer les suffixes de politesse ou de mode DJ
    cleaned = re.sub(
        r"\b(?:en\s+mode\s+dj|s'il\s+te\s+pla[iî]t|stp|svp|merci|sur\s+youtube)\b",
        "",
        cleaned,
        flags=re.IGNORECASE,
    ).strip()

    # Retirer les prépositions initiales ("de", "du", "des", "d'", "sur", ":")
    cleaned = re.sub(r"^(?:de\s+la|du|des|de|d'|sur|avec|pour|:|-)\s*", "", cleaned, flags=re.IGNORECASE).strip()
    cleaned = cleaned.strip(" .,!?;:\"'")
    return cleaned


def extract_limit(raw_query: str, default_limit: int = 20) -> int:
    m = re.search(r"\b(\d+)\s*(?:remix|titres?|morceaux?|chansons?|musiques?)\b", raw_query.lower())
    if m:
        val = int(m.group(1))
        if 1 <= val <= 50:
            return val
    return default_limit


def build_dj_remix_playlist(raw_query: str) -> tuple[str, list[dict]]:
    subject = extract_remix_subject(raw_query)
    limit = extract_limit(raw_query, default_limit=20)
    tracks = []
    seen_keys = set()

    def add_track(item: dict):
        key = f"{item.get('base_title', '').lower()}_{item.get('artist', '').lower()}_{item.get('remix', '').lower()}"
        if not key.strip("_"):
            key = item.get("query", "").lower()
        if key in seen_keys:
            return
        seen_keys.add(key)
        tracks.append(
            {
                "rank": len(tracks) + 1,
                "title": item["title"],
                "base_title": item.get("base_title", item["title"]),
                "artist": item.get("artist", ""),
                "remix": item.get("remix", "Remix"),
                "query": item["query"],
                "url": item.get("url", ""),
            }
        )

    if subject:
        subj_lower = subject.lower()

        # 1. Vérifier si un titre du catalogue de référence correspond directement
        for c in CURATED_REMIXES:
            if (
                subj_lower in c["query"].lower()
                or subj_lower in c["base_title"].lower()
                or subj_lower in c["artist"].lower()
                or any(subj_lower == tag for tag in c.get("tags", []))
            ):
                add_track(dict(c))

        # 2. Recherche directe sur YouTube des versions "Remix" (versions revisitées)
        yt_search_str = subject
        if not any(k in subj_lower for k in ["remix", "rmx", "extended", "mix"]):
            yt_search_str = f"{subject} Remix"

        yt_results = search_youtube_innertube(yt_search_str)
        for yr in yt_results:
            matched_existing = False
            for existing in tracks:
                if (
                    existing["base_title"].lower() == yr["base_title"].lower()
                    and existing["remix"].lower() in yr["remix"].lower()
                    and not existing.get("url")
                ):
                    existing["url"] = yr["url"]
                    matched_existing = True
                    break
            if not matched_existing:
                add_track(yr)
            if len(tracks) >= limit:
                break

        # 3. Compléter la sélection DJ Remix avec le catalogue mélangé pour assurer une session continue
        pool = list(CURATED_REMIXES)
        random.shuffle(pool)
        for c in pool:
            if len(tracks) >= limit:
                break
            add_track(dict(c))

        # Pré-résoudre l'URL exacte du 1er titre s'il provient du catalogue et n'a pas encore d'URL
        if tracks and not tracks[0].get("url"):
            first_results = search_youtube_innertube(tracks[0]["query"], timeout=5)
            if first_results:
                tracks[0]["url"] = first_results[0]["url"]

        label = f"DJ Remix ({subject})"
    else:
        # Mode DJ Remix général ("lance moi un remix") :
        # Mélanger le catalogue de versions revisitées emblématiques
        pool = list(CURATED_REMIXES)
        random.shuffle(pool)

        for c in pool:
            if len(tracks) >= limit:
                break
            add_track(dict(c))

        # Pré-résoudre immédiatement l'URL YouTube du 1er titre pour un démarrage instantané dans mpv
        if tracks and not tracks[0].get("url"):
            first_results = search_youtube_innertube(tracks[0]["query"], timeout=5)
            if first_results:
                tracks[0]["url"] = first_results[0]["url"]

        label = "DJ Remix (Versions Revisitées)"

    return label, tracks[:limit]


def main():
    raw_args = sys.argv[1:]
    full_str = " ".join(raw_args)
    json_mode = "--json" in full_str
    query = full_str.replace("--json", "").strip()

    label, tracks = build_dj_remix_playlist(query)

    if json_mode:
        print(
            json.dumps(
                {
                    "label": label,
                    "count": len(tracks),
                    "tracks": tracks,
                },
                ensure_ascii=False,
            )
        )
        return

    if not tracks:
        print(f"Impossible de récupérer une sélection {label} pour le moment.")
        sys.exit(1)

    preview = "\n".join(
        f"  #{t['rank']} {t['query']}" for t in tracks[:5]
    )
    print(
        f"🎧 Sélection {label} prête ({len(tracks)} versions revisitées) !\n"
        f"Premiers titres du mix :\n{preview}\n"
        f"Les morceaux sont programmés en mode DJ avec annonces vocales de Pixel, ducking à 20% et enchaînement direct à 100%."
    )


if __name__ == "__main__":
    main()
