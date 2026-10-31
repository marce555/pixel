import sys
import os
import json
import subprocess
import re

def main():
    try:
        # L'argument passé par Pixel
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni. Utiliser: python run.py <query>")
            return

        query = sys.argv[1]
        
        # Vérification de base pour éviter les attaques d'injection de commande
        if query.startswith("check_gmail_emails"):
            # Simulation d'une connexion sécurisée (dans un environnement réel, utiliser OAuth2/API)
            # Pour cet exemple, on simule une réponse car l'accès direct au Gmail n'est pas possible sans credentials.
            print("Récupération des emails Gmail en cours...")
            print("Aucun email trouvé ou erreur de connexion (simulation).")
            print("Pour une utilisation réelle, configurez les credentials OAuth2 dans l'environnement sécurisé.")
        else:
            print(f"Commande inconnue pour Gmail: {query}")
            
    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()