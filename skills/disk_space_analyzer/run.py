import sys
import os
import subprocess

def main():
    try:
        # Récupération de la requête utilisateur (le chemin ou commande spécifique)
        if len(sys.argv) < 2:
            # Si aucun argument, on fait un df -h global par défaut comme demandé dans l'objectif principal
            command = "df -h" 
        else:
            query = sys.argv[1]
            
            # Vérification si la requête ressemble à une commande 'df' personnalisée ou juste un chemin.
            # Pour être robuste, on essaie d'exécuter df -h sur le chemin spécifié si c'est valide.
            if os.path.exists(query) and query != "/":
                command = f"du -sh --max-depth=1 {query}" # Alternative utile pour un dossier spécifique
            else:
                command = "df -h"

        # Exécution de la commande shell via subprocess avec timeout et capture de sortie
        try:
            result = subprocess.run(
                command, 
                shell=True, 
                stdout=subprocess.PIPE, 
                stderr=subprocess.PIPE, 
                text=True, 
                timeout=10
            )
            
            if result.returncode == 0:
                # Nettoyage léger de la sortie pour éviter les erreurs d'affichage excessives si nécessaire
                output = result.stdout.strip()
                
                # Optionnel : Formater ou résumer le résultat en JSON si besoin, mais ici on imprime directement.
                print(output)
            else:
                print(f"Erreur exécution commande : {result.stderr}", file=sys.stderr)

        except subprocess.TimeoutExpired:
            print("La commande a dépassé le temps imparti.", file=sys.stderr)
            
    except Exception as e:
        # Gestion globale des exceptions pour éviter les crashes violents sans crasher l'agent IA entier.
        print(f"Erreur critique : {e}", file=sys.stderr)

if __name__ == '__main__':
    main()