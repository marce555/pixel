import sys
import os
import json
import subprocess
import re

def main():
    try:
        # L'argument passé par Pixel
        query = sys.argv[1] 
        
        # === IMPLÉMENTE LA LOGIQUE DE LA BRIQUE ICI ===
        # Rappel : Ton but est de résoudre l'objectif demandé.
        # Utilise print() pour renvoyer le résultat final à Pixel.
        # Utilise subprocess pour exécuter les commandes de ligne
        # Utilise re pour trouver les mots qui font référence aux processus de consommation du RAM
        # Utilise os to utiliser os.getpid et os.getppid pour obtenir l'identifiant du processus
        # Utilise os to utiliser os.getpid et os.getppid pour obtenir l'identifiant du processus
        process = subprocess.Popen(['free', '-p', query], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        result = process.stdout.read().decode()
        for word in re.findall(r'\d{1,3}(\.\d+)?(?:\s*([a-zA-Z]+)\s*,?\s*)?', result):
            print(f"{word}: {int(word.split('.')[0])}")

    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()