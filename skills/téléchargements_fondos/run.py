import sys
import os

def main():
    try:
        downloads_dir = "/home/marceloc/Téléchargements"
        if not os.path.exists(downloads_dir):
            downloads_dir = os.path.expanduser("~/Downloads")

        if not os.path.exists(downloads_dir):
            print("❌ Dossier Téléchargements introuvable.")
            return

        files = []
        for filename in os.listdir(downloads_dir):
            filepath = os.path.join(downloads_dir, filename)
            if os.path.isfile(filepath):
                try:
                    size = os.path.getsize(filepath)
                    files.append((filename, size, filepath))
                except Exception:
                    pass

        files.sort(key=lambda x: x[1], reverse=True)

        print(f"### 📂 Fichiers du dossier Téléchargements ({len(files)} fichiers)\n")
        print("| Nom du Fichier | Taille | Chemin |")
        print("|---|---|---|")
        for name, size, path in files[:20]:
            size_mb = size / (1024 * 1024)
            print(f"| **{name}** | {size_mb:.2f} MB | `{path}` |")

    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()