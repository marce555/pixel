import sys
import os
import re

def main():
    try:
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni.")
            return

        query = sys.argv[1]

        # Détection des mots-clés
        keywords = ['openmusic', 'remix', 'mix local']
        is_target = False

        for kw in keywords:
            if kw in query.lower():
                is_target = True
                break

        if is_target:
            # Simulation du lancement de la session (car l'outil externe n'est pas accessible ici)
            # Dans un environnement réel, on appellerait l'API ou le script externe.
            result = f"Session de {query} lancée automatiquement."
            print(result)
        else:
            print("Aucun mot-clé de lancement détecté.")

    except Exception as e:
        print(f"Erreur : {e}")

if __name__ == '__main__':
    main()