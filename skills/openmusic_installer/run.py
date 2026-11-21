import sys
import os
import subprocess
import re

def main():
    try:
        # L'argument passé par Pixel
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni.")
            return

        query = sys.argv[1]
        
        # Logique de détection simple pour OpenMusic
        # On cherche si la requête contient "openmusic" ou "installer openmusic"
        if re.search(r'openmusic', query, re.IGNORECASE):
            # Tentative d'installation via pip (hypothèse : c'est un package pip)
            # Ou via apt-get si c'est un package système. On essaie pip d'abord.
            print("Tentative d'installation d'OpenMusic via pip...")
            try:
                result = subprocess.run(['pip', 'install', 'openmusic'], capture_output=True, text=True, timeout=30)
                if result.returncode == 0:
                    print("OpenMusic installé avec succès.")
                else:
                    print(f"Erreur d'installation pip: {result.stderr}")
            except FileNotFoundError:
                print("pip n'est pas installé. Essayons apt-get...")
                try:
                    result = subprocess.run(['sudo', 'apt-get', 'install', '-y', 'openmusic'], capture_output=True, text=True, timeout=30)
                    if result.returncode == 0:
                        print("OpenMusic installé avec succès via apt-get.")
                    else:
                        print(f"Erreur d'installation apt-get: {result.stderr}")
                except Exception as e:
                    print(f"Erreur lors de l'installation système: {e}")
            return
        
        else:
            print("La requête ne concerne pas l'installation d'OpenMusic.")

    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()