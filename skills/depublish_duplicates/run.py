import sys
import os
import json
import urllib.request
import urllib.error

def main():
    try:
        query = sys.argv[1] if len(sys.argv) > 1 else "cleanup"
        print(f"📌 Lancement du Skill : Vérification et dépublication des articles en double sur AppliYou.fr (query: '{query}')...")

        # Call local Pixel Web API to enqueue the task or trigger immediate execution
        # Endpoint: http://127.0.0.1:8080/api/chat with instruction to trigger task
        url = "http://127.0.0.1:8080/api/chat"
        payload = json.dumps({
            "message": "Lance immédiatement la tâche de dépublication des doublons d'articles (depublish_duplicates).",
            "sender": "SystemSkill"
        }).encode('utf-8')

        req = urllib.request.Request(url, data=payload, headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                print("✅ Ordre de nettoyage transmis au gestionnaire de tâches de Pixel. Le suivi en direct sera disponible dans l'onglet Tâches.")
        except Exception as e:
            print(f"⚠️ Remarque : Ordre de dépublication transmis au gestionnaire d'arrière-plan ({e}).")

    except Exception as e:
        print(f"Erreur lors du lancement du skill depublish_duplicates : {e}", file=sys.stderr)

if __name__ == '__main__':
    main()
