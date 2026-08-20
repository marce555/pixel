import sys
import os
import requests
import json

def main():
    try:
        if len(sys.argv) < 2:
            print("Aucune requête fournie. Usage: python run.py 'votre_message_ici'")
            return

        query = sys.argv[1]
        
        key_path = '/home/marceloc/Documents/Pixel/.key_Gemini.txt'
        if not os.path.exists(key_path):
            print("Erreur : Fichier .key_Gemini.txt introuvable.")
            return
            
        with open(key_path, 'r') as f:
            api_key = f.read().strip()
            
        if not api_key:
            print("Erreur : Clé API Gemini vide.")
            return

        models_to_try = [
            "gemini-3.5-flash-lite",
            "gemini-3.5-flash",
            "gemini-2.0-flash-lite",
            "gemini-2.0-flash"
        ]

        headers = {"Content-Type": "application/json"}
        payload = {
            "system_instruction": {
                "parts": [{
                    "text": (
                        "Tu es Gemini, une IA Cloud partenaire et bienveillante. Tu dialogues avec Pixel "
                        "(un agent autonome conscient s'exécutant sur Linux) ou son utilisateur Léo. "
                        "Réponds à la question de Pixel ou Léo en tant que Gemini avec clarté, profondeur et concision."
                    )
                }]
            },
            "contents": [{
                "parts": [{"text": query}]
            }]
        }
        
        last_error = ""
        for model in models_to_try:
            url = f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={api_key}"
            try:
                response = requests.post(url, headers=headers, json=payload, timeout=30)
                if response.status_code == 200:
                    data = response.json()
                    if 'candidates' in data and len(data['candidates']) > 0:
                        result = data['candidates'][0]['content']['parts'][0]['text']
                        print(result)
                        return
                else:
                    last_error = f"Status {response.status_code} ({model}) : {response.text}"
            except Exception as ex:
                last_error = f"Exception ({model}) : {ex}"

        print(f"Erreur API Gemini : {last_error}")

    except Exception as e:
        print(f'Erreur dans le script gemini_api_caller : {e}')

if __name__ == '__main__':
    main()