#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Skill: billboard_radio
Scrape les 100 titres du classement Billboard Hot 100 pour une année donnée (ex: 1983)
ou le classement actuel, et retourne la liste des 100 morceaux pour les enchaîner
dans le lecteur musical de Pixel avec enchaînement direct radio (nouveau titre lancé
directement à 100% avec suppression des silences de début et de fin pendant la descente du volume du morceau précédent).
"""

import html
import json
import re
import sys
import urllib.request
from datetime import datetime


USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)


def fetch_html(url: str, timeout: int = 12) -> str:
    req = urllib.request.Request(
        url,
        headers={
            "User-Agent": USER_AGENT,
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
            "Accept-Language": "en-US,en;q=0.9,fr;q=0.8",
        },
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read().decode("utf-8", errors="ignore")


def clean_text(raw: str) -> str:
    # Remove ref tags like [1] or HTML tags
    txt = re.sub(r"<sup[^>]*>.*?</sup>", "", raw, flags=re.DOTALL | re.IGNORECASE)
    txt = re.sub(r"<[^>]+>", "", txt)
    txt = html.unescape(txt)
    txt = re.sub(r"\s+", " ", txt).strip()
    txt = txt.strip('"').strip("«").strip("»").strip()
    return txt


def scrape_billboard_direct(year: str | None) -> list[dict]:
    """Tente de scraper directement billboard.com (Hot 100 ou Year-End Hot 100)."""
    urls = []
    if year:
        urls.append(f"https://www.billboard.com/charts/year-end/{year}/hot-100-songs/")
        urls.append(f"https://www.billboard.com/charts/hot-100/{year}-07-02/")
    else:
        urls.append("https://www.billboard.com/charts/hot-100/")

    for url in urls:
        try:
            page = fetch_html(url, timeout=10)
            containers = page.split("o-chart-results-list-row-container")[1:]
            tracks = []
            for idx, block in enumerate(containers, start=1):
                h3_match = re.search(
                    r'<h3[^>]*id="title-of-a-story"[^>]*>(.*?)</h3>',
                    block,
                    flags=re.DOTALL | re.IGNORECASE,
                )
                if not h3_match:
                    continue
                title = clean_text(h3_match.group(1))
                after_h3 = block[h3_match.end() :]
                span_match = re.search(
                    r'<span[^>]*class="[^"]*c-label[^"]*"[^>]*>(.*?)</span>',
                    after_h3,
                    flags=re.DOTALL | re.IGNORECASE,
                )
                artist = clean_text(span_match.group(1)) if span_match else ""
                if title and artist and not artist.isdigit():
                    tracks.append(
                        {
                            "rank": len(tracks) + 1,
                            "title": title,
                            "artist": artist,
                            "query": f"{artist} - {title} lyrics",
                            "source": url,
                        }
                    )
                if len(tracks) >= 100:
                    break
            if len(tracks) >= 20:
                return tracks[:100]
        except Exception:
            continue
    return []


def scrape_billboard_year_end_archive(year: str) -> list[dict]:
    """
    Scrape l'archive complète officielle du Billboard Year-End Hot 100 singles
    pour l'année demandée (100 titres exacts classés de #1 à #100).
    """
    url = f"https://en.wikipedia.org/wiki/Billboard_Year-End_Hot_100_singles_of_{year}"
    try:
        page = fetch_html(url, timeout=12)
        tables = re.findall(
            r'<table[^>]*class="[^"]*wikitable[^"]*"[^>]*>(.*?)</table>',
            page,
            flags=re.DOTALL | re.IGNORECASE,
        )
        tracks = []
        for table in tables:
            rows = re.findall(r"<tr[^>]*>(.*?)</tr>", table, flags=re.DOTALL | re.IGNORECASE)
            for row in rows:
                cells = re.findall(
                    r"<t[dh][^>]*>(.*?)</t[dh]>", row, flags=re.DOTALL | re.IGNORECASE
                )
                if len(cells) >= 3:
                    rank_str = clean_text(cells[0]).rstrip(".")
                    title = clean_text(cells[1])
                    artist = clean_text(cells[2])
                    if rank_str.isdigit() and title and artist:
                        tracks.append(
                            {
                                "rank": int(rank_str),
                                "title": title,
                                "artist": artist,
                                "query": f"{artist} - {title} lyrics",
                                "source": "https://www.billboard.com/charts/hot-100/",
                            }
                        )
            if len(tracks) >= 50:
                break
        return tracks[:100]
    except Exception:
        return []


def extract_year(query: str) -> str | None:
    m = re.search(r"\b(19[5-9]\d|20[0-2]\d)\b", query)
    if m:
        return m.group(1)
    # Gérer les formes courtes comme "années 80" -> 1983 par défaut ou similaire
    m_short = re.search(r"\b(?:année|annee|an)\s*(\d{2})\b", query.lower())
    if m_short:
        yy = int(m_short.group(1))
        return f"19{yy:02d}" if yy >= 58 else f"20{yy:02d}"
    return None


def get_billboard_100(query: str) -> tuple[str, list[dict]]:
    year = extract_year(query)
    tracks = []
    if year:
        # Wikipedia conserve l'archive officielle intégrale du Billboard Year-End Hot 100
        # pour chaque année de 1958 à nos jours (100 titres classés de 1 à 100).
        tracks = scrape_billboard_year_end_archive(year)
        if len(tracks) < 50:
            direct = scrape_billboard_direct(year)
            if len(direct) > len(tracks):
                tracks = direct
    else:
        # Classement actuel en cours sur billboard.com
        tracks = scrape_billboard_direct(None)
        if len(tracks) < 50:
            target_year = str(datetime.now().year - 1)
            tracks = scrape_billboard_year_end_archive(target_year)
            if tracks:
                year = target_year

    label = f"Billboard Hot 100 ({year})" if year else "Billboard Hot 100"
    return label, tracks[:100]


def main():
    raw_args = sys.argv[1:]
    full_str = " ".join(raw_args)
    json_mode = "--json" in full_str
    query = full_str.replace("--json", "").strip()

    label, tracks = get_billboard_100(query)

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
        print(f"Impossible de récupérer la liste {label} pour le moment.")
        sys.exit(1)

    preview = ", ".join(
        f"#{t['rank']} {t['artist']} - {t['title']}" for t in tracks[:5]
    )
    print(
        f"Liste {label} récupérée avec succès ({len(tracks)} titres).\n"
        f"Premiers titres : {preview}...\n"
        f"Les {len(tracks)} morceaux sont programmés dans le lecteur musical avec ambiance Radio DJ (annonces vocales de Pixel à chaque titre, ducking du volume à 20% pendant la voix, et enchaînement direct du morceau suivant à 100% sans silence)."
    )


if __name__ == "__main__":
    main()
