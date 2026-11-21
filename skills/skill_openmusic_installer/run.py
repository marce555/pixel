import sys
import os
import json
import subprocess
import re
import tempfile
import shutil

def run_command(cmd, check=True):
    """Exécute une commande et retourne le résultat."""
    try:
        result = subprocess.run(cmd, shell=True, capture_output=True, text=True, check=check)
        return result.returncode, result.stdout, result.stderr
    except Exception as e:
        return -1, "", str(e)

def main():
    try:
        # L'argument passé par Pixel
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni.")
            return

        query = sys.argv[1]
        
        # Vérification de base
        if "installer" in query.lower() or "configurer" in query.lower():
            print("Début de l'installation de OpenMusic.ai...")
            
            # 1. Définir les variables
            # Version stable récente (ex: v0.1.0 ou v0.2.0 selon disponibilité)
            OPENMUSIC_VERSION = "v0.1.0" 
            OPENMUSIC_REPO = "https://github.com/OpenMusicAI/openmusic"
            OPENMUSIC_BIN = "openmusic"
            OPENMUSIC_HOME = os.path.expanduser("~/Musique")
            INSTALL_DIR = "/usr/local/bin"
            
            # Création du dossier Musique si inexistant
            if not os.path.exists(OPENMUSIC_HOME):
                os.makedirs(OPENMUSIC_HOME, exist_ok=True)
            
            # 2. Télécharger le dernier release
            print(f"Téléchargement de la version {OPENMUSIC_VERSION}...")
            download_url = f"{OPENMUSIC_REPO}/releases/download/{OPENMUSIC_VERSION}/openmusic-{OPENMUSIC_VERSION}-linux-x86_64.tar.gz"
            
            # Vérification si le fichier existe déjà pour éviter de redescendre inutilement
            bin_path = os.path.join(INSTALL_DIR, OPENMUSIC_BIN)
            if os.path.exists(bin_path):
                print("OpenMusic.ai déjà installé. Mise à jour non nécessaire.")
                # On lance un test rapide
                test_result = run_command(f"{bin_path} --help", check=False)
                if test_result[0] == 0:
                    print("Installation validée. Commande disponible.")
                else:
                    print("Installation détectée mais non fonctionnelle.")
                return

            # Téléchargement
            return_code, stdout, stderr = run_command(f"wget -q -O /tmp/openmusic.tar.gz {download_url}")
            if return_code != 0:
                print(f"Erreur de téléchargement: {stderr}")
                return

            # 3. Extraire l'archive
            print("Extraction de l'archive...")
            extract_path = os.path.join(tempfile.gettempdir(), "openmusic_extract")
            os.makedirs(extract_path, exist_ok=True)
            
            extract_result = run_command(f"tar -xzf /tmp/openmusic.tar.gz -C {extract_path}")
            if extract_result[0] != 0:
                print(f"Erreur d'extraction: {extract_result[2]}")
                return

            # 4. Installer le binaire
            print("Installation dans /usr/local/bin...")
            bin_name = os.path.join(extract_path, "bin", OPENMUSIC_BIN)
            if not os.path.exists(bin_name):
                print("Fichier binaire non trouvé dans l'archive.")
                return

            # Copier le binaire
            shutil.copy2(bin_name, INSTALL_DIR)
            os.chmod(INSTALL_DIR + "/" + OPENMUSIC_BIN, 0o755)
            
            # 5. Configurer les variables d'environnement
            # On ajoute OPENMUSIC_HOME à PATH pour que le script le trouve
            env_path = os.environ.get("PATH", "")
            new_path = f"{env_path}:{OPENMUSIC_HOME}"
            os.environ["PATH"] = new_path
            
            # 6. Test de connexion et génération
            print("Lancement du test de connexion...")
            test_cmd = f"{INSTALL_DIR}/{OPENMUSIC_BIN} --test"
            test_result = run_command(test_cmd)
            
            if test_result[0] == 0:
                print("Test réussi. OpenMusic.ai est prêt à l'emploi.")
                print(f"Dossier de musique configuré : {OPENMUSIC_HOME}")
                print(f"Commande d'exécution : {INSTALL_DIR}/{OPENMUSIC_BIN}")
            else:
                print(f"Test échoué: {test_result[2]}")
                print("Veuillez vérifier les logs ou les permissions.")
                
        else:
            print("Cette action ne s'applique pas à la requête actuelle.")
            
    except Exception as e:
        print(f'Erreur critique : {e}')

if __name__ == '__main__':
    main()