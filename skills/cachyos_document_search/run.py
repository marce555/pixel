#!/usr/bin/env python3
import sys
import os
import re
import subprocess
from datetime import datetime

def format_size(size_bytes):
    if size_bytes < 1024:
        return f"{size_bytes} B"
    elif size_bytes < 1024 * 1024:
        return f"{size_bytes / 1024:.1f} KB"
    elif size_bytes < 1024 * 1024 * 1024:
        return f"{size_bytes / (1024 * 1024):.1f} MB"
    else:
        return f"{size_bytes / (1024 * 1024 * 1024):.1f} GB"

def search_files(query, search_dir):
    matched_files = set()
    file_details = []

    # 1. Search filenames using fd if available
    fd_cmd = ["fd", "-H", "-I", "--max-results", "30"]
    
    # Check if user specified extension (e.g. pdf, md, txt, py, docx)
    ext_match = re.search(r'\b(pdf|md|txt|docx?|xlsx?|py|go|json|yaml|yml|conf|log|png|jpg|jpeg|mp3|mp4|wav|m4a)\b', query.lower())
    clean_query = query
    if ext_match:
        ext = ext_match.group(1)
        fd_cmd.extend(["-e", ext])
        clean_query = re.sub(r'\b' + ext + r'\b', '', query, flags=re.IGNORECASE).strip()

    pattern = clean_query if clean_query else "."
    fd_cmd.extend([pattern, search_dir])

    try:
        res = subprocess.run(fd_cmd, capture_output=True, text=True, timeout=8)
        if res.returncode == 0:
            for path in res.stdout.strip().split("\n"):
                if path.strip() and os.path.exists(path.strip()):
                    matched_files.add(os.path.abspath(path.strip()))
    except Exception:
        pass

    # 2. Content search using ripgrep (rg) for text queries
    if clean_query and len(clean_query) >= 2:
        rg_cmd = ["rg", "-i", "-l", "--max-count", "1", "--max-filesize", "10M", clean_query, search_dir]
        try:
            res_rg = subprocess.run(rg_cmd, capture_output=True, text=True, timeout=8)
            if res_rg.returncode == 0:
                for path in res_rg.stdout.strip().split("\n"):
                    if path.strip() and os.path.exists(path.strip()):
                        matched_files.add(os.path.abspath(path.strip()))
        except Exception:
            pass

    # Fallback to python os.walk if fd/rg returned nothing or failed
    if not matched_files:
        count = 0
        q_lower = clean_query.lower()
        for root, dirs, files in os.walk(search_dir):
            dirs[:] = [d for d in dirs if not d.startswith('.')]
            for f in files:
                if not q_lower or q_lower in f.lower():
                    matched_files.add(os.path.abspath(os.path.join(root, f)))
                    count += 1
                    if count >= 30:
                        break
            if count >= 30:
                break

    # 3. Gather stats and previews
    previews = {}
    for filepath in list(matched_files)[:25]:
        try:
            st = os.stat(filepath)
            mtime = datetime.fromtimestamp(st.st_mtime).strftime("%d/%m/%Y %H:%M")
            size_str = format_size(st.st_size)
            ext = os.path.splitext(filepath)[1].lower() or "fichier"
            
            file_details.append({
                "path": filepath,
                "name": os.path.basename(filepath),
                "size": size_str,
                "mtime": mtime,
                "ext": ext
            })

            # Get text preview snippet for readable files
            if ext in [".txt", ".md", ".py", ".go", ".json", ".yaml", ".yml", ".conf", ".sh", ".html", ".css", ".js", ".csv"]:
                try:
                    with open(filepath, "r", encoding="utf-8", errors="ignore") as f:
                        content_sample = f.read(1000)
                        if clean_query and clean_query.lower() in content_sample.lower():
                            lines = content_sample.splitlines()
                            matching_lines = [l.strip() for l in lines if clean_query.lower() in l.lower()]
                            if matching_lines:
                                previews[filepath] = matching_lines[0][:150]
                        elif content_sample.strip():
                            previews[filepath] = content_sample.strip().splitlines()[0][:150]
                except Exception:
                    pass

        except Exception:
            continue

    return file_details, previews

def resolve_target_dir(home_dir, query):
    q_lower = query.lower()
    dirs_map = [
        (["download", "téléchargement", "telechargement", "téléchargements", "telechargements"], ["Téléchargements", "Downloads"]),
        (["document", "documents"], ["Documents"]),
        (["bureau", "desktop"], ["Bureau", "Desktop"]),
        (["musique", "music"], ["Musique", "Music"]),
        (["image", "images", "picture", "pictures"], ["Images", "Pictures"]),
        (["vidéo", "videos", "vidéos"], ["Vidéos", "Videos"]),
    ]

    for keywords, candidates in dirs_map:
        if any(kw in q_lower for kw in keywords):
            for cand in candidates:
                path = os.path.join(home_dir, cand)
                if os.path.exists(path):
                    return path

    for fallback in ["Documents", "Téléchargements", "Downloads", "Bureau", "Desktop"]:
        path = os.path.join(home_dir, fallback)
        if os.path.exists(path):
            return path

    return home_dir

def main():
    query = ""
    if len(sys.argv) > 1:
        query = " ".join(sys.argv[1:]).strip()

    if not query:
        query = "documents récents"

    home_dir = os.path.expanduser("~")
    target_dir = resolve_target_dir(home_dir, query)

    # If the query is just a directory keyword, list all files in that directory
    clean_search_query = query
    dir_keywords = ["téléchargement", "téléchargements", "telechargement", "telechargements", "downloads", "download", "documents", "document", "desktop", "bureau"]
    if clean_search_query.strip().lower() in dir_keywords:
        clean_search_query = ""

    files, previews = search_files(clean_search_query, target_dir)

    print(f"### 📁 Recherche de Documents CachyOS")
    print(f"**Recherche :** `{query}` | **Dossier exploré :** `{target_dir}`")
    print(f"**Documents identifiés :** {len(files)}\n")

    if not files:
        print("Aucun document correspondant n'a été trouvé dans le répertoire spécifié.")
        return

    print("| Nom du Fichier | Extension | Taille | Modification | Chemin Absolu |")
    print("|---|---|---|---|---|")
    for f in files[:25]:
        print(f"| **{f['name']}** | `{f['ext']}` | {f['size']} | {f['mtime']} | `{f['path']}` |")

    if previews:
        print("\n### 📝 Aperçus de Contenu")
        for path, snippet in list(previews.items())[:5]:
            filename = os.path.basename(path)
            print(f"- **{filename}** : *\"{snippet}\"*")

if __name__ == "__main__":
    main()
