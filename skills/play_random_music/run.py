#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import sys
import os
import re
import json
import socket
import random
import time
import subprocess
from pathlib import Path

DEFAULT_MUSIC_DIR = os.path.expanduser("~/Musique/MIX-EXTENDED")
BASE_MUSIC_DIR = os.path.expanduser("~/Musique")
IPC_SOCKET = "/tmp/mpvsocket_random_music"
PLAYLIST_FILE = "/tmp/pixel_random_music_playlist.m3u"
AUDIO_EXTENSIONS = {".mp3", ".m4a", ".wav", ".flac", ".ogg", ".wma", ".aac", ".opus", ".alac", ".aiff", ".mka"}

def send_mpv_ipc(command_dict):
    """Envoie une commande JSON-IPC au lecteur mpv."""
    if not os.path.exists(IPC_SOCKET):
        return None, "Lecteur mpv non actif (socket introuvable)."
    try:
        client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        client.settimeout(2.0)
        client.connect(IPC_SOCKET)
        msg = json.dumps(command_dict) + "\n"
        client.sendall(msg.encode("utf-8"))
        data = client.recv(4096).decode("utf-8")
        client.close()
        return json.loads(data.strip().split("\n")[0]), None
    except Exception as e:
        return None, f"Erreur IPC : {e}"

def stop_previous_player():
    """Arrête proprement une instance précédente de mpv pour éviter les superpositions audio."""
    if os.path.exists(IPC_SOCKET):
        try:
            send_mpv_ipc({"command": ["quit"]})
            time.sleep(0.3)
        except Exception:
            pass
    
    # Nettoyage supplémentaire des processus mpv liés à ce socket
    try:
        subprocess.run(["pkill", "-f", f"input-ipc-server={IPC_SOCKET}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        pass

    if os.path.exists(IPC_SOCKET):
        try:
            os.remove(IPC_SOCKET)
        except Exception:
            pass

def find_target_directory(query):
    """Détermine le dossier cible à partir de la requête utilisateur."""
    if not query or not query.strip():
        return DEFAULT_MUSIC_DIR

    query_str = query.strip()

    # 1. Vérifier si un chemin explicite commençant par ~ ou / est présent
    path_match = re.search(r'(?:~|/)[^\s\'"]+', query_str)
    if path_match:
        candidate = os.path.expanduser(path_match.group(0))
        if os.path.isdir(candidate):
            return candidate

    # 2. Vérifier si un sous-dossier de ~/Musique est mentionné
    if os.path.isdir(BASE_MUSIC_DIR):
        try:
            available_folders = [d for d in os.listdir(BASE_MUSIC_DIR) if os.path.isdir(os.path.join(BASE_MUSIC_DIR, d))]
            # Recherche exacte ou partielle
            for folder in available_folders:
                if folder.lower() in query_str.lower():
                    return os.path.join(BASE_MUSIC_DIR, folder)
        except Exception:
            pass

    # 3. Si "mix" ou "extended" est mentionné ou par défaut
    if "mix" in query_str.lower() or "extended" in query_str.lower():
        if os.path.isdir(DEFAULT_MUSIC_DIR):
            return DEFAULT_MUSIC_DIR

    # 4. Repli sur le dossier par défaut si existant, sinon ~/Musique
    if os.path.isdir(DEFAULT_MUSIC_DIR):
        return DEFAULT_MUSIC_DIR
    elif os.path.isdir(BASE_MUSIC_DIR):
        return BASE_MUSIC_DIR
    return DEFAULT_MUSIC_DIR

def collect_audio_files(directory):
    """Scanne récursivement le répertoire pour trouver tous les fichiers audio valides."""
    audio_files = []
    for root, _, files in os.walk(directory):
        for f in files:
            ext = os.path.splitext(f)[1].lower()
            if ext in AUDIO_EXTENSIONS:
                audio_files.append(os.path.join(root, f))
    return audio_files

def main():
    try:
        raw_query = sys.argv[1] if len(sys.argv) > 1 else ""
        query = raw_query.lower().strip()

        # Commandes rapides de contrôle du lecteur
        if query in ["stop", "arrete", "arrête", "stop musique", "quitter"]:
            stop_previous_player()
            print("🛑 Lecture de musique arrêtée avec succès.")
            return

        if query in ["pause", "reprendre", "resume", "play_pause"]:
            res, err = send_mpv_ipc({"command": ["cycle", "pause"]})
            if err:
                print(f"⚠️ Impossible de basculer la pause : {err}")
            else:
                print("⏯️ Pause/Lecture basculée avec succès.")
            return

        if query in ["next", "suivant", "chanson suivante"]:
            res, err = send_mpv_ipc({"command": ["playlist-next"]})
            if err:
                print(f"⚠️ Impossible de passer au morceau suivant : {err}")
            else:
                print("⏭️ Morceau suivant lancé.")
            return

        if query in ["status", "statut", "info", "quelle chanson", "current"]:
            res, err = send_mpv_ipc({"command": ["get_property", "media-title"]})
            if err:
                print(f"ℹ️ Aucune musique n'est actuellement en cours de lecture ({err}).")
            else:
                title = res.get("data", "Inconnu")
                print(f"🎵 Titre actuellement en lecture : {title}")
            return

        # Localisation du répertoire cible
        target_dir = find_target_directory(raw_query)
        if not os.path.isdir(target_dir):
            print(f"❌ Erreur : Le répertoire '{target_dir}' n'existe pas sur votre système.")
            if os.path.isdir(BASE_MUSIC_DIR):
                subdirs = [d for d in os.listdir(BASE_MUSIC_DIR) if os.path.isdir(os.path.join(BASE_MUSIC_DIR, d))]
                print(f"💡 Dossiers disponibles dans ~/Musique : {', '.join(subdirs[:10])}")
            sys.exit(1)

        # Collecte des morceaux audio
        audio_files = collect_audio_files(target_dir)
        if not audio_files:
            print(f"❌ Aucun fichier audio valide trouvé dans '{target_dir}'.")
            sys.exit(1)

        # Mélange aléatoire (Shuffle) sans répétition
        # Chaque morceau n'apparaît qu'une seule fois dans la liste ordonnée aléatoirement
        random.seed()
        shuffled_files = list(audio_files)
        random.shuffle(shuffled_files)

        # Écriture de la playlist M3U
        with open(PLAYLIST_FILE, "w", encoding="utf-8") as f:
            f.write("#EXTM3U\n")
            for track in shuffled_files:
                f.write(f"{track}\n")

        # Arrêt d'une éventuelle instance précédente
        stop_previous_player()

        # Démarrage de mpv en arrière-plan détaché (daemon)
        mpv_cmd = [
            "mpv",
            "--no-video",
            "--no-terminal",
            f"--input-ipc-server={IPC_SOCKET}",
            "--loop-playlist=no",
            f"--playlist={PLAYLIST_FILE}"
        ]

        proc = subprocess.Popen(
            mpv_cmd,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True
        )

        # Attente brève pour vérifier l'initialisation du processus
        time.sleep(0.4)
        if proc.poll() is not None:
            print(f"❌ Échec lors du lancement de mpv (code de sortie: {proc.returncode}).")
            sys.exit(1)

        # Symlink optionnel vers /tmp/mpvsocket pour compatibilité maximale avec Pixel
        try:
            if not os.path.exists("/tmp/mpvsocket"):
                os.symlink(IPC_SOCKET, "/tmp/mpvsocket")
        except Exception:
            pass

        # Affichage du rapport complet pour Pixel et l'utilisateur
        folder_display = target_dir.replace(os.path.expanduser("~"), "~")
        total_tracks = len(shuffled_files)

        print(f"🎶 **Musique lancée en aléatoire sans répétition !**")
        print(f"📂 **Répertoire source** : `{folder_display}`")
        print(f"🔢 **Pistes chargées** : {total_tracks} morceaux")
        print(f"🔀 **Mode** : Lecture aléatoire unique (chaque morceau sera joué 1 fois sans répétition)")
        print(f"▶️ **Premiers morceaux de la file d'attente** :")
        for i, track_path in enumerate(shuffled_files[:5], 1):
            track_name = os.path.splitext(os.path.basename(track_path))[0]
            print(f"  {i}. {track_name}")
        if total_tracks > 5:
            print(f"  ... et {total_tracks - 5} autres morceaux en attente.")
        print(f"\n*(Contrôlable en direct via Pixel : 'pause', 'suivant', 'stop musique' ou 'statut')*")

    except Exception as e:
        print(f"❌ Erreur inattendue : {e}")
        sys.exit(1)

if __name__ == '__main__':
    main()
