import sys
import os

def main():
    try:
        # L'argument passé par Pixel (l'article à sauvegarder)
        if len(sys.argv) < 2:
            print("Erreur : Aucune requête fournie. Veuillez fournir le texte de l'article.")
            return
        
        query = sys.argv[1]

        # Définition du répertoire et du nom de fichier pour la sauvegarde
        save_directory = os.path.expanduser("~/.pixel_drafts")
        
        # Création du dossier si n'existe pas (optionnel, mais robuste)
        if not os.path.exists(save_directory):
            try:
                os.makedirs(save_directory)
            except OSError as e:
                print(f"Impossible de créer le répertoire de sauvegarde : {e}")

        # Génération d'un nom de fichier unique basé sur l'heure ou un hash simple du contenu pourrait être utilisé, 
        # mais ici on prendra simplement une extension .txt. Pour éviter les conflits immédiats, on ajoute un timestamp.
        import time
        file_name = f"article_{int(time.time())}.txt"
        file_path = os.path.join(save_directory, file_name)

        try:
            with open(file_path, 'w', encoding='utf-8') as f:
                f.write(query)
            
            print(f"Succès : Article sauvegardé dans {file_path}")
        
        except IOError as e:
            print(f"Erreur lors de l'écriture du fichier : {e}")

    except Exception as e:
        print(f'Erreur inattendue : {e}')

if __name__ == '__main__':
    main()