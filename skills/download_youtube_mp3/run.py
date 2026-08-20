import sys
import os
import subprocess
import re

def is_exact_match(query, title):
    query_clean = query.lower()
    title_clean = title.lower()

    # Découpage de la requête en mots en utilisant des séparateurs courants
    words = re.split(r"[\s\-\'\,\.\/\(\)\[\]\|]", query_clean)
    
    matched_count = 0
    important_words_count = 0
    
    # Mots vides courants à ignorer
    stop_words = {
        "des", "les", "une", "qui", "que", "de", "la", "le", "du", "en", "pour", 
        "avec", "par", "dans", "sur", "a", "au", "aux", "d", "l", "un", "et", "ou"
    }
    
    for w in words:
        w = w.strip()
        if not w:
            continue
        if len(w) <= 2 or w in stop_words:
            continue
        important_words_count += 1
        if w in title_clean:
            matched_count += 1
            
    if important_words_count == 0:
        return True # Si aucun mot important, on autorise par défaut
        
    match_ratio = float(matched_count) / float(important_words_count)
    return match_ratio >= 0.75

def main():
    try:
        if len(sys.argv) < 2:
            print("Erreur : Aucun titre ou artiste fourni.")
            sys.exit(1)
            
        query = sys.argv[1]
        print(f"Recherche de '{query}' sur YouTube...")
        
        # Exécution de la recherche rapide avec yt-dlp
        search_cmd = ["yt-dlp", "--get-title", "--get-id", f"ytsearch1:{query}"]
        result = subprocess.run(search_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=20)
        
        if result.returncode != 0:
            print(f"Erreur lors de la recherche sur YouTube : {result.stderr.strip()}")
            sys.exit(1)
            
        lines = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
        if len(lines) < 2:
            print("Erreur : Aucun résultat trouvé sur YouTube.")
            sys.exit(1)
            
        video_title = lines[0]
        video_id = lines[1]
        video_url = f"https://www.youtube.com/watch?v={video_id}"
        
        print(f"Meilleur résultat trouvé : '{video_title}'")
        
        # Validation de la correspondance
        if not is_exact_match(query, video_title):
            print(f"Le titre trouvé ('{video_title}') ne correspond pas suffisamment à la recherche ('{query}'). Annulation par sécurité.")
            sys.exit(0)
            
        # Création du répertoire de destination dans Musique/Pixel
        dest_dir = "/home/marceloc/Musique/Pixel"
        os.makedirs(dest_dir, exist_ok=True)
        
        print("Match validé. Lancement du téléchargement en MP3 haute qualité (320kbps)...")
        
        # Téléchargement et conversion en MP3 320kbps
        # %(title)s.%(ext)s gérera le nom du fichier basé sur le titre de la vidéo
        dl_cmd = [
            "yt-dlp",
            "-x",
            "--audio-format", "mp3",
            "--audio-quality", "320K",
            "-o", os.path.join(dest_dir, "%(title)s.%(ext)s"),
            video_url
        ]
        
        dl_result = subprocess.run(dl_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=180)
        
        if dl_result.returncode != 0:
            print(f"Erreur lors du téléchargement ou de la conversion : {dl_result.stderr.strip()}")
            sys.exit(1)
            
        print(f"Téléchargement terminé avec succès ! Le fichier MP3 de '{video_title}' est disponible de façon permanente dans : {dest_dir}")
        
    except subprocess.TimeoutExpired:
        print("La commande a dépassé le temps de réponse limite de 3 minutes.")
        sys.exit(1)
    except Exception as e:
        print(f"Erreur inattendue : {e}")
        sys.exit(1)

if __name__ == '__main__':
    main()
