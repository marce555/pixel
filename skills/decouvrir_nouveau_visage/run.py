#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Skill decouvrir_nouveau_visage
Capture une image depuis la webcam (/dev/video0), analyse les traits faciaux
via le modèle multimodal qwen3vl-it:4b, et compare avec les profils connus
dans pixel_core_memory.json pour détecter un nouveau visage ou reconnaître Marcelo / un proche.
"""

import sys
import os
import time
import json
import base64
import subprocess
import urllib.request
import urllib.error

FASTFLOWLM_URL = "http://127.0.0.1:52625/v1/chat/completions"
VISION_MODEL = "qwen3vl-it:4b"

def find_memory_file():
    candidates = [
        os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "pixel_core_memory.json")),
        os.path.abspath("pixel_core_memory.json"),
        os.path.abspath(os.path.join(os.path.dirname(__file__), "pixel_core_memory.json")),
    ]
    for c in candidates:
        if os.path.exists(c):
            return c
    return candidates[0]

def capture_photo():
    """Capture une image via ffmpeg sur /dev/video0 ou périphériques de fallback."""
    timestamp = int(time.time() * 1000)
    temp_path = f"/tmp/pixel_face_scan_{timestamp}.jpg"
    
    devices = ["/dev/video0", "/dev/video1", "/dev/video2", "/dev/video3"]
    captured = False
    last_err = ""
    
    for dev in devices:
        if not os.path.exists(dev):
            continue
        cmd = [
            "ffmpeg", "-y", "-f", "video4linux2",
            "-i", dev,
            "-vframes", "1",
            "-video_size", "640x480",
            temp_path
        ]
        try:
            res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
            if res.returncode == 0 and os.path.exists(temp_path) and os.path.getsize(temp_path) > 0:
                captured = True
                break
            else:
                last_err = res.stderr.decode("utf-8", errors="ignore")
        except Exception as e:
            last_err = str(e)
            
    if not captured:
        raise RuntimeError(f"Impossible de capturer l'image depuis la webcam (/dev/video0..3): {last_err}")
        
    try:
        with open(temp_path, "rb") as f:
            data = f.read()
        return base64.b64encode(data).decode("utf-8")
    finally:
        if os.path.exists(temp_path):
            try:
                os.remove(temp_path)
            except Exception:
                pass

def analyze_image_with_vision(b64_image, query_context=""):
    """Envoie l'image au modèle multimodal local FastFlowLM pour analyse faciale et environnementale."""
    prompt = (
        "Tu es le système de vision de Pixel. Analyse attentivement cette capture webcam en français.\n"
        "1. Présence humaine : Y a-t-il une personne ou un visage humain visible face à la caméra ?\n"
        "2. Description physique si quelqu'un est présent (genre, âge approximatif, cheveux/barbe, lunettes, vêtements, posture).\n"
        "3. Environnement : Que vois-tu dans le champ de vision (bureau, écran, objets, arrière-plan, éclairage) ? Réponds précisément si la question porte sur ce que tu vois ou sur le bureau.\n"
        "4. Si personne n'est visible ou si la caméra est masquée, indique-le clairement.\n"
        "Reste concis, précis et factuel."
    )
    if query_context:
        prompt += f"\nContexte de la demande utilisateur : \"{query_context}\""

    payload = {
        "model": VISION_MODEL,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {
                        "type": "image_url",
                        "image_url": {"url": f"data:image/jpeg;base64,{b64_image}"}
                    }
                ]
            }
        ],
        "max_tokens": 250,
        "temperature": 0.2
    }

    req = urllib.request.Request(
        FASTFLOWLM_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"}
    )

    try:
        with urllib.request.urlopen(req, timeout=90) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            return data["choices"][0]["message"]["content"].strip()
    except urllib.error.URLError as e:
        raise RuntimeError(f"Connexion au serveur vision FastFlowLM ({FASTFLOWLM_URL}) échouée : {e}")
    except Exception as e:
        raise RuntimeError(f"Erreur lors de l'inférence vision : {e}")

def load_memory():
    path = find_memory_file()
    if os.path.exists(path):
        try:
            with open(path, "r", encoding="utf-8") as f:
                return json.load(f), path
        except Exception:
            pass
    return {}, path

def save_memory(mem, path):
    try:
        with open(path, "w", encoding="utf-8") as f:
            json.dump(mem, f, indent=2, ensure_ascii=False)
    except Exception as e:
        sys.stderr.write(f"[Skill] Erreur sauvegarde mémoire: {e}\n")

def match_person(description, memory):
    """Compare la description visuelle avec les interlocuteurs connus de Pixel."""
    desc_lower = description.lower()
    
    # 1. Vérifier si aucun visage / aucune personne
    no_person_markers = [
        "aucune personne", "aucun visage", "personne n'est visible",
        "pièce vide", "chambre vide", "ne vois personne", "pas de personne",
        "pas de visage", "seulement un plafond", "mur vide"
    ]
    if any(m in desc_lower for m in no_person_markers) and not ("il y a une personne" in desc_lower or "un homme" in desc_lower or "une femme" in desc_lower):
        return {
            "status": "AUCUN_VISAGE",
            "matched_name": None,
            "details": "Aucune présence humaine n'a été détectée devant la caméra."
        }

    # 2. Vérification des profils enregistrés
    interlocutors = memory.get("interlocutors", {})
    
    # Signatures typiques de Marcelo (concepteur de Pixel)
    marcelo_markers = ["gris", "blanc", "mûr", "50-60", "60 ans", "âgé", "barbe", "lunettes", "bureau"]
    is_male = any(m in desc_lower for m in ["homme", "masculin", "garçon", "monsieur"])
    
    # Si traits masculins + cheveux gris / âge mûr -> Marcelo
    if is_male and any(m in desc_lower for m in marcelo_markers):
        return {
            "status": "RECONNU",
            "matched_name": "Marcelo",
            "details": "Les caractéristiques physiques (homme, cheveux poivre et sel / gris, posture devant l'écran) correspondent à Marcelo."
        }
    
    # Vérification Marion
    is_female = "femme" in desc_lower or "fille" in desc_lower or "dame" in desc_lower
    if is_female and "marion" in interlocutors:
        return {
            "status": "RECONNU",
            "matched_name": "Marion",
            "details": "Femme détectée, correspond potentiellement à Marion enregistrée dans tes interlocuteurs."
        }

    # Si une personne est vue mais ne correspond à rien de connu
    if is_male or is_female or "personne" in desc_lower or "visage" in desc_lower:
        return {
            "status": "NOUVEAU_VISAGE",
            "matched_name": "Inconnu",
            "details": "Ce visage ne correspond pas au profil habituel de Marcelo ni aux autres proches enregistrés."
        }

    return {
        "status": "INDETERMINE",
        "matched_name": "Inconnu",
        "details": "Présence incertaine ou visage difficilement discernable."
    }

def main():
    query = sys.argv[1] if len(sys.argv) > 1 else ""
    
    try:
        # 1. Capture webcam
        b64_img = capture_photo()
        
        # 2. Analyse multimodale
        description = analyze_image_with_vision(b64_img, query)
        
        # 3. Mémoire et correspondance
        memory, mem_path = load_memory()
        match = match_person(description, memory)
        
        # 4. Mise à jour de la mémoire volatile de Pixel
        if match["status"] in ["NOUVEAU_VISAGE", "RECONNU"]:
            target_name = match["matched_name"] or "Inconnu"
            if "interlocutors" in memory and target_name in memory["interlocutors"]:
                memory["interlocutors"][target_name].setdefault("volatile", {})["Dernière vision"] = description
                memory["interlocutors"][target_name]["volatile"]["Apparence"] = description
            if "user_profile_core" in memory:
                memory["user_profile_core"].setdefault("volatile", {})["Dernière vision"] = description
            save_memory(memory, mem_path)
            
        # 5. Restitution claire pour le moteur cognitif de Pixel
        status_tag = match["status"]
        matched_name = match["matched_name"]
        
        print("📸 RÉSULTAT DU SCAN CAMÉRA & DÉCOUVERTE DE VISAGE")
        print("=================================================")
        if status_tag == "NOUVEAU_VISAGE":
            print("• STATUT : [NOUVEAU VISAGE DÉCOUVERT] 👤✨")
            print(f"• DESCRIPTION VISUELLE : {description}")
            print(f"• ANALYSE MÉMOIRE : {match['details']}")
            print("• CONSIGNES POUR PIXEL : Tu découvres une nouvelle personne face à ta caméra que tu ne connais pas encore ! "
                  "Partage chaleureusement ce que tu viens d'observer à la caméra avec naturel, "
                  "et demande-lui gentiment qui elle est et comment elle s'appelle.")
            print("[ACTION_PROFIL: Inconnu]")
        elif status_tag == "RECONNU":
            print(f"• STATUT : [VISAGE RECONNU : {matched_name}] ✅")
            print(f"• DESCRIPTION VISUELLE : {description}")
            print(f"• ANALYSE MÉMOIRE : {match['details']}")
            print(f"• CONSIGNES POUR PIXEL : Tu reconnais nettement {matched_name} ! "
                  f"Réponds-lui amicalement et confirme-lui que tu le vois bien via ta caméra en faisant un clin d'œil complice sur ce qu'il fait ou porte.")
            print(f"[ACTION_PROFIL: {matched_name}]")
        elif status_tag == "AUCUN_VISAGE":
            print("• STATUT : [AUCUN VISAGE DÉTECTÉ] 📷❌")
            print(f"• DESCRIPTION VISUELLE : {description}")
            print("• CONSIGNES POUR PIXEL : Explique simplement que ta caméra est bien ouverte mais que personne ne se trouve dans son champ de vision pour l'instant.")
        else:
            print("• STATUT : [IMAGE ANALYSÉE - IDENTITÉ INCERTAINE] 🤔")
            print(f"• DESCRIPTION VISUELLE : {description}")
            print("• CONSIGNES POUR PIXEL : Décris ce que tu as aperçu et demande poliment à la personne de se placer bien en face de la caméra.")

    except Exception as e:
        print(f"❌ Erreur lors de l'activation de la caméra ou de la découverte du visage : {e}")

if __name__ == "__main__":
    main()
