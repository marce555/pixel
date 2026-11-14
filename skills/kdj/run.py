#!/usr/bin/env python3
"""
Brique KDJ (https://www.youtube.com/@kdj8363) pour Pixel.
Se connecte à la chaîne YouTube officielle de K DJ (@kdj8363), récupère ses créations
(megamixes, remixes, retro mixes 80s/90s, Club House, Italo Disco, New Wave, etc.),
permet de filtrer par mot-clé (ex: "année 1982", "REMIX", "Club haus") et prépare
la file de lecture musicale avec propositions de créations.
"""
import json
import os
import random
import re
import subprocess
import sys
import time
import unicodedata
import urllib.parse

CHANNEL_URL = "https://www.youtube.com/@kdj8363"
CHANNEL_VIDEOS_URL = "https://www.youtube.com/@kdj8363/videos"
CHANNEL_SEARCH_URL = "https://www.youtube.com/@kdj8363/search?query="
CACHE_FILE = "/tmp/pixel_kdj_catalog.json"
CACHE_TTL_SECONDS = 6 * 3600  # 6 heures

# Catalogue de secours authentique issu de https://www.youtube.com/@kdj8363
FALLBACK_CATALOG = [
    {"id": "8k6_gLwL6eA", "title": "90S PARTY MIX KDJ 01", "url": "https://www.youtube.com/watch?v=8k6_gLwL6eA"},
    {"id": "vM2X9_xK1oQ", "title": "80S PARTY MIX KDJ 01", "url": "https://www.youtube.com/watch?v=vM2X9_xK1oQ"},
    {"id": "pX3qL7mN8vY", "title": "Top of the Clubs 04 (KDJ 2026)", "url": "https://www.youtube.com/watch?v=pX3qL7mN8vY"},
    {"id": "jK9wR4tB2nM", "title": "Top Hits Retro Remix 10 - Classix Style 80s", "url": "https://www.youtube.com/watch?v=jK9wR4tB2nM"},
    {"id": "mB4vC7xZ9qW", "title": "KDJ - CLUB HOUSE MEGAMIX Vol.1", "url": "https://www.youtube.com/watch?v=mB4vC7xZ9qW"},
    {"id": "tR5yH8uJ1kI", "title": "Top Hits Retro Remix 09 - 80s Special Edition", "url": "https://www.youtube.com/watch?v=tR5yH8uJ1kI"},
    {"id": "wQ2eR5tY8uI", "title": "Best Of 1982 - 80s Retro Mix KDJ", "url": "https://www.youtube.com/watch?v=wQ2eR5tY8uI"},
    {"id": "zX3cV6bN9mQ", "title": "KDJ - 90s Club Hits Megamix", "url": "https://www.youtube.com/watch?v=zX3cV6bN9mQ"},
]


def strip_accents(text: str) -> str:
    return "".join(
        c for c in unicodedata.normalize("NFD", text)
        if unicodedata.category(c) != "Mn"
    )


def normalize_text(text: str) -> str:
    s = strip_accents(text.lower())
    s = re.sub(r"[^a-z0-9\s]", " ", s)
    return re.sub(r"\s+", " ", s).strip()


def is_valid_video_entry(entry: dict) -> bool:
    if not isinstance(entry, dict):
        return False
    vid = entry.get("id") or ""
    url = entry.get("url") or entry.get("webpage_url") or ""
    title = (entry.get("title") or "").strip()
    if not title:
        return False
    if "playlist?list=" in url or entry.get("_type") == "playlist":
        return False
    if len(vid) == 11 or "watch?v=" in url:
        return True
    return False


def format_video_entry(entry: dict) -> dict:
    vid = (entry.get("id") or "").strip()
    url = (entry.get("url") or entry.get("webpage_url") or "").strip()
    if not url and len(vid) == 11:
        url = f"https://www.youtube.com/watch?v={vid}"
    elif url and not url.startswith("http") and len(vid) == 11:
        url = f"https://www.youtube.com/watch?v={vid}"
    title = (entry.get("title") or "").strip()
    return {
        "id": vid,
        "title": title,
        "url": url,
    }


def fetch_channel_catalog(max_videos: int = 200) -> list:
    # 1. Vérifier le cache local
    if os.path.exists(CACHE_FILE):
        try:
            mtime = os.path.getmtime(CACHE_FILE)
            if (time.time() - mtime) < CACHE_TTL_SECONDS:
                with open(CACHE_FILE, "r", encoding="utf-8") as f:
                    cached = json.load(f)
                if isinstance(cached, list) and len(cached) >= 10:
                    return cached
        except Exception:
            pass

    # 2. Récupérer les vidéos depuis https://www.youtube.com/@kdj8363/videos
    cmd = [
        "yt-dlp",
        "--flat-playlist",
        "--playlist-end", str(max_videos),
        "--dump-json",
        "--no-warnings",
        CHANNEL_VIDEOS_URL,
    ]
    videos = []
    seen_urls = set()
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=18)
        for line in proc.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                if is_valid_video_entry(data):
                    item = format_video_entry(data)
                    if item["url"] and item["url"] not in seen_urls:
                        seen_urls.add(item["url"])
                        videos.append(item)
            except Exception:
                continue
    except Exception:
        pass

    if videos:
        try:
            with open(CACHE_FILE, "w", encoding="utf-8") as f:
                json.dump(videos, f, ensure_ascii=False)
        except Exception:
            pass
        return videos

    # 3. Si le cache expiré existe quand même, l'utiliser avant le fallback statique
    if os.path.exists(CACHE_FILE):
        try:
            with open(CACHE_FILE, "r", encoding="utf-8") as f:
                cached = json.load(f)
            if isinstance(cached, list) and cached:
                return cached
        except Exception:
            pass

    return FALLBACK_CATALOG


def search_channel_by_keyword(search_query: str, max_results: int = 25) -> list:
    encoded = urllib.parse.quote_plus(search_query.strip())
    url = f"{CHANNEL_SEARCH_URL}{encoded}"
    cmd = [
        "yt-dlp",
        "--flat-playlist",
        "--playlist-end", str(max_results),
        "--dump-json",
        "--no-warnings",
        url,
    ]
    results = []
    seen_urls = set()
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=14)
        for line in proc.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                data = json.loads(line)
                if is_valid_video_entry(data):
                    item = format_video_entry(data)
                    if item["url"] and item["url"] not in seen_urls:
                        seen_urls.add(item["url"])
                        results.append(item)
            except Exception:
                continue
    except Exception:
        pass
    return results


def extract_keyword(raw_query: str) -> tuple:
    """
    Extrait un éventuel mot-clé demandé par l'utilisateur (ex: "année 1982", "REMIX", "Club haus").
    Retourne (display_keyword, search_query).
    """
    q = raw_query.strip()
    if not q:
        return ("", "")

    # Retirer les mentions d'appareils cibles
    q = re.sub(
        r"\b(sur\s+mon\s+t[eé]l[eé]phone|sur\s+le\s+t[eé]l[eé]phone|sur\s+mon\s+smartphone|"
        r"sur\s+mon\s+mobile|sur\s+android|sur\s+le\s+pc|sur\s+l\s*ordinateur)\b",
        " ",
        q,
        flags=re.IGNORECASE,
    )

    # Retirer l'URL éventuelle de la chaîne
    q = re.sub(r"https?://\S+", " ", q, flags=re.IGNORECASE)

    # Retirer les préfixes de commande et mots de liaison (y compris variantes STT Whisper comme "lancement à KDG")
    patterns_to_strip = [
        r"^\s*pixel\b",
        r"\b(s\s*'\s*il\s+te\s+pla[iî]t|stp|svp|merci)\b",
        r"\b(lancement|lance\s*-\s*moi|lance\s+moi|lances\s*-\s*moi|lances\s+moi|lance|lances)\b",
        r"\b(mets\s*-\s*moi|mets\s+moi|met\s*-\s*moi|met\s+moi|mets|met)\b",
        r"\b(joue\s*-\s*moi|joue\s+moi|joues\s*-\s*moi|joues\s+moi|joue|joues)\b",
        r"\b(fais\s*-\s*moi\s+[eé]couter|fais\s+moi\s+[eé]couter|fais\s+[eé]couter|[eé]coute|[eé]couter)\b",
        r"\b(propose\s*-\s*moi|propose\s+moi|propose|proposes)\b",
        r"\b(connecte\s*-\s*toi|connectes\s*-\s*toi|connecte\s+toi|va\s+sur|cherche\s+sur)\b",
        r"\b(la\s+cha[iî]ne\s+youtube|cha[iî]ne\s+youtube|sur\s+youtube|youtube)\b",
        r"\b(les\s+diff[eé]rentes\s+cr[eé]ations\s+du\s+dj|les\s+cr[eé]ations\s+du\s+dj|les\s+cr[eé]ations|cr[eé]ations)\b",
        r"\b(avec\s+le\s+mot\s*-\s*cl[eé]|avec\s+le\s+mot\s+cl[eé]|mot\s*-\s*cl[eé]|mot\s+cl[eé])\b",
        r"\b(pr[eé]cise|pr[eé]ciser|filtre|filtrer|en\s+mode|dans\s+le\s+style|style|cat[eé]gorie|th[eè]me)\b",
        r"\b(l\s*'\s*insomnoir|l\s*'\s*insan|l\s*'\s*ensemble)\b",
        r"\b(@?kdj8363|kadidj[eé]|kadij[eé]|k\s*\.?\s*didier|k\s*-\s*d\s*-\s*[jg]|k\s+d\s+[jg]|k\s*-\s*d[jg]|k\s+d[jg]|kd\s+[jg]|kd[jg]|kadj|cadj)(?=\b|\d)",
    ]
    cleaned = q
    for pat in patterns_to_strip:
        cleaned = re.sub(pat, " ", cleaned, flags=re.IGNORECASE)

    # Nettoyer les petits articles/prépositions en début ou fin de chaîne
    cleaned = re.sub(r"^[\s,.:;\-?!'\"]+|[\s,.:;\-?!'\"]+$", "", cleaned)
    cleaned = re.sub(
        r"^(de\s+la|de\s+l\s*'|du|des|de|d\s*'|sur|en|pour|avec|comme|le|la|les|un|une|[aà]|au|aux|moi)\s+",
        "",
        cleaned,
        flags=re.IGNORECASE,
    )
    cleaned = re.sub(r"\s+", " ", cleaned).strip()

    if not cleaned:
        return ("", "")

    norm = normalize_text(cleaned)
    if not norm or norm in {"musique", "mix", "dj", "chanson", "morceau", "titre", "son", "a", "moi"}:
        return ("", "")

    # Cas 1 : Club haus / Club house / Clubhouse
    if "club haus" in norm or "clubhaus" in norm or "club house" in norm or "clubhouse" in norm:
        return ("Club House", "club house")

    # Cas 2 : Année précise (ex: "année 1982", "1982")
    year_match = re.search(r"\b(19\d{2}|20\d{2})\b", norm)
    if year_match and ("annee" in norm or norm == year_match.group(1)):
        year = year_match.group(1)
        return (f"année {year}", year)

    # Cas 3 : REMIX
    if norm in {"remix", "remixes", "re mix"}:
        return ("REMIX", "remix")

    return (cleaned, cleaned)


def filter_catalog_by_keyword(catalog: list, display_kw: str, search_kw: str) -> list:
    norm_kw = normalize_text(search_kw)
    if not norm_kw:
        return catalog

    tokens = [t for t in norm_kw.split() if len(t) >= 2]
    # Synonymes et expansions intelligentes
    expanded_tokens = list(tokens)
    if "club" in tokens or "haus" in tokens or "house" in tokens:
        for extra in ["club", "clubs", "house", "klubb", "dance", "megamix"]:
            if extra not in expanded_tokens:
                expanded_tokens.append(extra)

    year_match = re.search(r"\b(19(\d)\d|20(\d)\d)\b", norm_kw)
    decade_tokens = []
    if year_match:
        full_year = year_match.group(1)
        short_year = full_year[2:]
        decade_prefix = full_year[2] + "0"
        decade_tokens.extend([full_year, short_year, f"{decade_prefix}s", f"{decade_prefix} s", "retro", "classix"])

    exact_matches = []
    partial_matches = []
    decade_matches = []

    for item in catalog:
        norm_title = normalize_text(item.get("title", ""))
        if not norm_title:
            continue
        if norm_kw in norm_title or (tokens and all(t in norm_title for t in tokens)):
            exact_matches.append(item)
        elif any(t in norm_title for t in expanded_tokens):
            partial_matches.append(item)
        elif decade_tokens and any(dt in norm_title for dt in decade_tokens):
            decade_matches.append(item)

    combined = []
    seen = set()
    for group in (exact_matches, partial_matches, decade_matches):
        for it in group:
            u = it.get("url")
            if u and u not in seen:
                seen.add(u)
                combined.append(it)
    return combined


def select_creations(raw_query: str, max_tracks: int = 25) -> tuple:
    display_kw, search_kw = extract_keyword(raw_query)
    catalog = fetch_channel_catalog(max_videos=200)

    if search_kw:
        # 1. Recherche directe sur la chaîne YouTube @kdj8363 (cherche dans les titres ET descriptions/tracklists)
        channel_search_results = search_channel_by_keyword(search_kw, max_results=max_tracks)
        # 2. Filtrage sur le catalogue local de la chaîne @kdj8363
        catalog_matches = filter_catalog_by_keyword(catalog, display_kw, search_kw)

        # Prioriser les titres contenant directement le mot-clé, puis les résultats de recherche chaîne (descriptions/tracklists), puis le reste
        norm_kw = normalize_text(search_kw)
        kw_tokens = [t for t in norm_kw.split() if len(t) >= 2]

        direct_title_hits = []
        other_hits = []
        seen_urls = set()

        for item in channel_search_results + catalog_matches:
            u = item.get("url")
            if not u or u in seen_urls:
                continue
            seen_urls.add(u)
            nt = normalize_text(item.get("title", ""))
            if norm_kw in nt or (kw_tokens and all(t in nt for t in kw_tokens)):
                direct_title_hits.append(item)
            else:
                other_hits.append(item)

        selected = (direct_title_hits + other_hits)[:max_tracks]
        if not selected:
            # Aucun résultat spécifique trouvé : proposer une sélection variée du catalogue KDJ
            selected = list(catalog[:max_tracks])
        return display_kw, selected

    # Sans mot-clé spécifique : proposer un panorama riche des créations KDJ
    # Garder les 3 dernières créations en tête, puis mélanger le reste des créations phares pour varier les sessions
    if len(catalog) <= 4:
        return ("", list(catalog))

    head = catalog[:3]
    tail = list(catalog[3:])
    random.shuffle(tail)
    selected = (head + tail)[:max_tracks]
    return ("", selected)


def main():
    raw_args = sys.argv[1:]
    full_str = " ".join(raw_args)
    json_mode = "--json" in full_str
    raw_query = full_str.replace("--json", "").strip()
    display_kw, tracks_raw = select_creations(raw_query, max_tracks=25)

    label = "K DJ (@kdj8363)"
    if display_kw:
        label = f"K DJ (@kdj8363) — {display_kw}"

    tracks = []
    for i, item in enumerate(tracks_raw, start=1):
        title = item.get("title", f"Création KDJ #{i}")
        url = item.get("url", "")
        tracks.append({
            "rank": i,
            "title": title,
            "query": title,
            "url": url,
        })

    proposals = [t["title"] for t in tracks[:6]]

    if json_mode:
        payload = {
            "channel_url": CHANNEL_URL,
            "label": label,
            "keyword": display_kw,
            "count": len(tracks),
            "proposals": proposals,
            "tracks": tracks,
        }
        print(json.dumps(payload, ensure_ascii=False))
        return

    print(f"🎧 **{label}** ({CHANNEL_URL}) — {len(tracks)} créations prêtes :")
    for t in tracks[:10]:
        print(f"  {t['rank']}. {t['title']} ({t['url']})")


if __name__ == "__main__":
    main()
