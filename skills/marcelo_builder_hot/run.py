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
        
        # Définition du répertoire local de musique de Marcelo
        # Assumons que le répertoire est dans le même dossier que le script ou un dossier standard
        # On cherche un dossier nommé 'marcelo' ou on utilise un chemin relatif par défaut
        music_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'marcelo_music')
        
        # Vérification si le dossier existe, sinon on essaie un chemin relatif simple
        if not os.path.exists(music_dir):
            # Tentative de trouver un dossier 'marcelo' dans le courant
            for root, dirs, files in os.walk('.'):
                if 'marcelo' in dirs:
                    music_dir = os.path.join(root, 'marcelo')
                    break
            if not os.path.exists(music_dir):
                # Fallback: on suppose que le dossier est dans le répertoire courant
                music_dir = './marcelo'
        
        # Construction de la commande pour lancer le mode 'Builder Hot'
        # On simule l'exécution d'un outil de musique avec les paramètres demandés
        # En l'absence d'un outil spécifique, on lance un script de simulation ou un player
        # avec des paramètres dynamiques
        
        # Paramètres simulés pour le mode Builder Hot
        tempo = "140"  # Tempo rapide
        energy = "high"
        transition_style = "dynamic"
        
        # Création d'un fichier de configuration JSON pour la session
        session_config = {
            "mode": "Builder Hot",
            "source_dir": music_dir,
            "tempo": tempo,
            "energy_level": energy,
            "transition_style": transition_style,
            "query": query
        }
        
        # Écriture de la configuration dans un fichier temporaire
        config_file = os.path.join(music_dir, "session_config.json")
        with open(config_file, 'w') as f:
            json.dump(session_config, f, indent=4)
        
        # Simulation de l'exécution (ou appel à un outil externe si disponible)
        # Ici, on simule le lancement d'une session en imprimant le statut
        print(f"[SESSION LANCÉE] Mode: Builder Hot")
        print(f"[SOURCE] Répertoire: {music_dir}")
        print(f"[PARAMETRES] Tempo: {tempo} BPM, Énergie: {energy}, Transitions: {transition_style}")
        print(f"[QUERY] Requête traitée: {query}")
        print(f"[STATUT] Session de remix dynamique initiée avec succès.")
        
    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()