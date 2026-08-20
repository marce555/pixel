import sys
import os
import re
import urllib.request
import html as html_lib

music_dir = "/home/marceloc/Musique"
audio_extensions = (".mp3", ".m4a", ".wav", ".flac", ".ogg", ".wma")

stop_words = {"the", "a", "an", "and", "or", "of", "in", "on", "at", "to", "for", "with", "by", "is", "are", "feat", "ft", "version", "extended", "mix", "remix", "single", "edit"}

def get_words(text):
    text = text.lower()
    text = re.sub(r'[^a-z0-9\s]', ' ', text)
    words = text.split()
    return [w for w in words if w not in stop_words and len(w) >= 2]

def is_match(bb_title, bb_artist, file_name):
    name_without_ext = os.path.splitext(file_name)[0]
    file_words = set(get_words(name_without_ext))
    
    title_words = get_words(bb_title)
    artist_words = get_words(bb_artist)
    
    if not title_words or not artist_words:
        return False
        
    title_matched = [w for w in title_words if w in file_words]
    title_ratio = len(title_matched) / len(title_words)
    if title_ratio < 0.7:
        return False
        
    artist_matched = [w for w in artist_words if w in file_words]
    if len(artist_words) <= 2:
        if len(artist_matched) < len(artist_words):
            return False
    else:
        if len(artist_matched) < 2 and len(artist_matched) < len(artist_words):
            return False
            
    return True

def scan_library():
    local_files = []
    for root, dirs, files in os.walk(music_dir):
        for file in files:
            if file.lower().endswith(audio_extensions):
                local_files.append((file, root))
    return local_files

def main():
    try:
        if len(sys.argv) < 2:
            query = "scan"
        else:
            query = sys.argv[1].lower().strip()

        print(f"=== PIXEL MUSIC MANAGER ===")
        print(f"Requête : {query}")

        local_files = scan_library()
        print(f"Bibliothèque analysée : {len(local_files)} fichiers audio trouvés.")

        if "scan" in query or "analyse" in query or not query:
            # Print directory statistics
            dirs_count = {}
            for file, root in local_files:
                folder_name = os.path.basename(root)
                dirs_count[folder_name] = dirs_count.get(folder_name, 0) + 1
            
            print("\nRépartition par dossiers principaux :")
            for folder, count in sorted(dirs_count.items(), key=lambda x: x[1], reverse=True)[:10]:
                print(f"- {folder} : {count} fichiers")
            return

        if "manquant" in query or "billboard" in query:
            print("\nRecherche des hits Billboard Hot 100 (1978-1989) manquants...")
            all_songs = []
            # Scrape sample years to keep it fast
            years_to_check = [1980, 1985, 1988]
            for year in years_to_check:
                url = f"https://en.wikipedia.org/wiki/Billboard_Year-End_Hot_100_singles_of_{year}"
                headers = {'User-Agent': 'Mozilla/5.0'}
                req = urllib.request.Request(url, headers=headers)
                try:
                    with urllib.request.urlopen(req, timeout=10) as response:
                        html = response.read().decode('utf-8')
                    table_pattern = re.compile(r'<table[^>]*class="[^"]*wikitable[^"]*"[^>]*>(.*?)</table>', re.DOTALL)
                    tables = table_pattern.findall(html)
                    if tables:
                        tr_pattern = re.compile(r'<tr[^>]*>(.*?)</tr>', re.DOTALL)
                        rows = tr_pattern.findall(tables[0])
                        tag_cleaner = re.compile(r'<[^>]+>')
                        for row in rows:
                            tds = re.compile(r'<td[^>]*>(.*?)</td>', re.DOTALL).findall(row)
                            if len(tds) >= 3:
                                rank = tds[0]
                                title = tag_cleaner.sub('', tds[1]).strip().strip('"')
                                artist = html_lib.unescape(tag_cleaner.sub('', tds[2]).strip())
                                all_songs.append((year, rank, title, artist))
                except Exception as e:
                    pass
            
            # Match
            missing = []
            for y, r, t, a in all_songs:
                found = False
                for lf, _ in local_files:
                    if is_match(t, a, lf):
                        found = True
                        break
                if not found:
                    missing.append((y, r, t, a))
            
            print(f"Analyse sur les années {years_to_check} : {len(missing)} hits manquants trouvés.")
            print("Quelques exemples de hits manquants :")
            for y, r, t, a in missing[:10]:
                print(f"- [{y} #{r}] {a} - {t}")
            return

        # Otherwise, search a specific song
        print(f"\nRecherche de '{query}' dans votre bibliothèque...")
        query_words = get_words(query)
        matches = []
        for file, root in local_files:
            file_lower = file.lower()
            if all(w in file_lower for w in query_words):
                matches.append((file, os.path.basename(root)))
                
        if matches:
            print(f"Trouvé {len(matches)} correspondance(s) :")
            for file, folder in matches[:15]:
                print(f"- {file} (dans le dossier '{folder}')")
        else:
            print("Aucun fichier correspondant trouvé dans votre bibliothèque locale.")

    except Exception as e:
        print(f"Erreur d'exécution : {e}")

if __name__ == '__main__':
    main()
