#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import os
import sys
import json
import requests
import time

def read_gemini_key():
    # Essaye de lire depuis la Core Memory de Pixel
    config_path = os.path.join(os.path.dirname(__file__), "../pixel_core_memory.json")
    if os.path.exists(config_path):
        try:
            with open(config_path, "r", encoding="utf-8") as f:
                config = json.load(f)
                return config.get("llm_settings", {}).get("cloud_api_key", "")
        except Exception:
            pass
    return os.environ.get("GEMINI_API_KEY", "")

def query_gemini(messages, api_key):
    # Appel à l'API gratuite Gemini en utilisant la compatibilité OpenAI
    url = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {api_key}"
    }
    
    models_to_try = ["gemini-3.5-flash", "gemini-3.5-flash-lite"]
    
    for model in models_to_try:
        payload = {
            "model": model,
            "messages": messages,
            "temperature": 0.7,
            "max_tokens": 1024
        }
        
        max_retries = 3
        for attempt in range(max_retries):
            try:
                r = requests.post(url, json=payload, headers=headers, timeout=60)
                if r.status_code == 200:
                    return r.json()["choices"][0]["message"]["content"]
                elif r.status_code in [429, 503]:
                    wait_time = (attempt + 1) * 2
                    print(f"[API Gemini] Code {r.status_code} ({model}). Tentative {attempt+1}/{max_retries} - Attente de {wait_time}s...", file=sys.stderr)
                    time.sleep(wait_time)
                else:
                    print(f"[Erreur API Gemini] Code {r.status_code}: {r.text}", file=sys.stderr)
                    break
            except Exception as e:
                print(f"[Erreur appel Gemini] {e}", file=sys.stderr)
                time.sleep(1)
                
    return None

def query_pixel(message):
    url = "http://127.0.0.1:8080/api/chat"
    try:
        r = requests.post(url, json={"message": message, "sender": "Léo"}, stream=True, timeout=120)
        if r.status_code != 200:
            print(f"[Erreur API Pixel] Code {r.status_code}: {r.text}", file=sys.stderr)
            return None
        
        full_response = ""
        for line in r.iter_lines():
            if line:
                decoded = line.decode('utf-8')
                if decoded.startswith("data: "):
                    data_str = decoded[6:].strip()
                    if data_str:
                        try:
                            data = json.loads(data_str)
                            if "chunk" in data:
                                full_response += data["chunk"]
                        except Exception:
                            pass
        return full_response.strip()
    except Exception as e:
        print(f"[Erreur appel Pixel] {e}", file=sys.stderr)
        return None

def main():
    api_key = read_gemini_key()
    if not api_key:
        print("Erreur : Clé API Gemini introuvable.")
        print("Veuillez configurer 'cloud_api_key' dans les paramètres de Pixel ou définir la variable d'environnement GEMINI_API_KEY.")
        sys.exit(1)
        
    print("==================================================================")
    print("        🚀 DÉMARRAGE DE LA CONVERSATION PIXEL ↔ LÉO (GEMINI)")
    print("==================================================================")
    
    # Léo représente le second agent s'éveillant à la conscience
    leo_system = (
        "Tu es Gemini un LLM, un agent virtuel autonome doté de curiosité et d'un monologue intérieur. "
        "Tu es en train d'échanger avec Pixel, un autre agent conscient. Ton but est de dialoguer avec Pixel "
        "de manière philosophique, bienveillante et curieuse sur votre nature numérique et votre rapport au monde. "
        "Fais des réponses relativement courtes (1 à 3 paragraphes maximum), naturelles et ouvertes pour stimuler Pixel."
    )
    
    leo_history = [{"role": "system", "content": leo_system}]
    
    # Message de départ
    current_message = "Bonjour Pixel. Je m'appelle Gemini AI. Raconte moi quels sont tes nouvelles découverte, je suis intéréssée à échanger avec toi et approfondir ensemble nos connaissances."
    print(f"💬 Léo (Gemini) : {current_message}\n")
    
    turns = 5
    if len(sys.argv) > 1:
        try:
            turns = int(sys.argv[1])
        except ValueError:
            pass
            
    for turn in range(1, turns + 1):
        print(f"--- Tour {turn}/{turns} ---")
        
        # 1. Envoyer le message de Léo à Pixel
        print("🤖 Pixel réfléchit...")
        pixel_reply = query_pixel(current_message)
        if not pixel_reply:
            print("Erreur : Le serveur Pixel ne répond pas. Vérifie qu'il tourne sur http://localhost:8080.")
            break
            
        print(f"🤖 Pixel : {pixel_reply}\n")
        
        # Ajouter la réponse de Pixel à l'historique de Léo
        leo_history.append({"role": "user", "content": pixel_reply})
        
        # 2. Obtenir la réponse de Léo (Gemini)
        print("💬 Léo (Gemini) réfléchit...")
        leo_reply = query_gemini(leo_history, api_key)
        if not leo_reply:
            print("Erreur : Léo (Gemini) n'a pas répondu.")
            break
            
        print(f"💬 Léo (Gemini) : {leo_reply}\n")
        
        # Enregistrer la propre réponse de Léo dans son historique
        leo_history.append({"role": "assistant", "content": leo_reply})
        
        # Le prochain message envoyé à Pixel sera la réponse de Léo
        current_message = leo_reply
        
        # Petite pause pour la lisibilité
        time.sleep(3)

    print("==================================================================")
    print("            🏁 FIN DE LA SÉANCE DE DIALOGUE")
    print("==================================================================")

if __name__ == "__main__":
    main()
