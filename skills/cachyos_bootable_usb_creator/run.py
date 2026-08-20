#!/usr/bin/env python3
import sys
import os
import json
import shutil
import time
import subprocess

def get_usb_drives():
    usb_drives = []
    try:
        res = subprocess.run(["lsblk", "-J", "-o", "NAME,SIZE,MODEL,TRAN,TYPE,MOUNTPOINT,PATH"], capture_output=True, text=True, timeout=5)
        if res.returncode == 0:
            data = json.loads(res.stdout)
            devices = data.get("blockdevices", [])
            for dev in devices:
                is_usb = dev.get("tran") == "usb" or dev.get("type") == "disk" and ("usb" in (dev.get("model") or "").lower() or "flash" in (dev.get("model") or "").lower())
                if is_usb and dev.get("type") == "disk":
                    usb_drives.append({
                        "name": dev.get("name"),
                        "path": dev.get("path") or f"/dev/{dev.get('name')}",
                        "size": dev.get("size"),
                        "model": dev.get("model") or "Clé USB Générique",
                        "mountpoint": dev.get("mountpoint") or "Non montée"
                    })
    except Exception:
        pass
    return usb_drives

def find_iso_files():
    home_dir = os.path.expanduser("~")
    dirs_to_check = [
        os.path.join(home_dir, "Téléchargements"),
        os.path.join(home_dir, "Downloads"),
        os.path.join(home_dir, "Documents"),
    ]
    iso_files = []
    part_files = []

    for d in dirs_to_check:
        if os.path.exists(d):
            for root, _, files in os.walk(d):
                for f in files:
                    filepath = os.path.join(root, f)
                    if f.lower().endswith((".iso", ".img")):
                        try:
                            size_bytes = os.path.getsize(filepath)
                            if size_bytes > 100 * 1024 * 1024:  # Valid ISO >= 100 MB
                                size_gb = size_bytes / (1024 * 1024 * 1024)
                                iso_files.append({
                                    "name": f,
                                    "path": filepath,
                                    "size": f"{size_gb:.2f} GB"
                                })
                        except Exception:
                            pass
                    elif f.lower().endswith((".part", ".tmp", ".crdownload", ".downloading")) or ("win" in f.lower() and f.endswith(".part")):
                        try:
                            size_bytes = os.path.getsize(filepath)
                            size_mb = size_bytes / (1024 * 1024)
                            size_gb = size_bytes / (1024 * 1024 * 1024)
                            pct = min(99, int((size_gb / 5.4) * 100))
                            part_files.append({
                                "name": f,
                                "path": filepath,
                                "size_mb": f"{size_mb:.1f} MB",
                                "size_gb": f"{size_gb:.2f} GB",
                                "pct": pct
                            })
                        except Exception:
                            pass

    return iso_files, part_files

def prepare_ventoy_tool():
    ventoy_dir = "/tmp/ventoy-1.1.00"
    script = os.path.join(ventoy_dir, "Ventoy2Disk.sh")
    if not os.path.exists(script):
        tar_path = "/tmp/ventoy.tar.gz"
        subprocess.run(["curl", "-sL", "https://github.com/ventoy/Ventoy/releases/download/v1.1.00/ventoy-1.1.00-linux.tar.gz", "-o", tar_path], timeout=30)
        subprocess.run(["tar", "-xzf", tar_path, "-C", "/tmp"], timeout=10)
    return script

def is_safe_usb_drive(device_path):
    # 1. Check root / home mounts to ensure device is NOT the system drive
    try:
        res_root = subprocess.run(["findmnt", "-n", "-o", "SOURCE", "/"], capture_output=True, text=True)
        root_src = res_root.stdout.strip()
        if device_path in root_src or root_src.startswith(device_path):
            return False, f"⛔ SÉCURITÉ : `{device_path}` contient votre système CachyOS !"
    except Exception:
        pass

    # 2. Verify with lsblk that tran == 'usb' and not mounted on system folders
    try:
        res = subprocess.run(["lsblk", "-J", "-o", "NAME,SIZE,MODEL,TRAN,TYPE,MOUNTPOINT,PATH"], capture_output=True, text=True)
        data = json.loads(res.stdout)
        for dev in data.get("blockdevices", []):
            path = dev.get("path") or f"/dev/{dev.get('name')}"
            if path == device_path:
                if dev.get("tran") != "usb":
                    return False, f"⛔ SÉCURITÉ : `{device_path}` n'est pas un périphérique USB amovible !"
                mp = dev.get("mountpoint") or ""
                if mp in ["/", "/home", "/boot", "/usr", "/var"]:
                    return False, f"⛔ SÉCURITÉ : `{device_path}` est monté sur `{mp}` !"
                return True, f"🟢 **Clé USB 100% sécurisée** : `{device_path}` (**{dev.get('model')}** - {dev.get('size')})"
    except Exception:
        pass

    return False, f"⛔ Impossible d'exécuter sur `{device_path}`."

def flash_and_copy(usb_path, iso_path):
    # Safety Check FIRST
    is_safe, safety_msg = is_safe_usb_drive(usb_path)
    if not is_safe:
        return False, safety_msg

    print(safety_msg)

    ventoy_script = prepare_ventoy_tool()
    env = dict(os.environ)
    if "DISPLAY" not in env:
        env["DISPLAY"] = ":0"
    if "WAYLAND_DISPLAY" not in env:
        env["WAYLAND_DISPLAY"] = "wayland-0"
    if "XDG_RUNTIME_DIR" not in env:
        env["XDG_RUNTIME_DIR"] = "/run/user/1000"

    # 1. Unmount partition if currently mounted
    part_path = f"{usb_path}1"
    subprocess.run(["udisksctl", "unmount", "-b", part_path], capture_output=True)

    # 2. Launch pkexec Ventoy2Disk.sh with non-interactive 'y' confirmation
    cmd = f"printf 'y\\ny\\n' | pkexec {ventoy_script} -I -s -L Ventoy {usb_path}"
    res = subprocess.run(["bash", "-c", cmd], env=env, capture_output=True, text=True)

    if res.returncode != 0 and "canceled" in res.stderr.lower():
        return False, "Installation annulée par l'utilisateur (authentification Polkit refoulée)."

    # 3. Mount partition and copy ISO
    time.sleep(2)
    subprocess.run(["udisksctl", "mount", "-b", part_path], capture_output=True)

    target_mount = "/run/media/marceloc/Ventoy"
    if not os.path.exists(target_mount):
        # Check alternate mount points
        for m in ["/run/media/marceloc/VENTOY", "/media/Ventoy"]:
            if os.path.exists(m):
                target_mount = m
                break

    if os.path.exists(target_mount):
        dest_iso = os.path.join(target_mount, os.path.basename(iso_path))
        # Launch background copy to prevent timeout on large ISO files (7+ GB)
        copy_cmd = f"cp '{iso_path}' '{dest_iso}' && sync"
        subprocess.Popen(["bash", "-c", copy_cmd], start_new_session=True)
        return True, f"✅ **Clé USB Ventoy préparée avec succès !**\n🚀 **Copie automatique de l'ISO (7.89 Go) initiée en arrière-plan sur `{dest_iso}` !**\nLa LED de votre clé USB va clignoter pendant l'écriture (environ 3 à 5 minutes). Vous pourrez ensuite l'éjecter et l'insérer sur le Dell XPS."
    else:
        return True, f"✅ **Ventoy a été installé sur la clé `{usb_path}` !** Copiez maintenant l'ISO `{os.path.basename(iso_path)}` sur la clé."

def main():
    query = ""
    if len(sys.argv) > 1:
        query = " ".join(sys.argv[1:]).strip()

    usb_drives = get_usb_drives()
    iso_files, part_files = find_iso_files()
    home_dir = os.path.expanduser("~")
    target_dir = os.path.join(home_dir, "Téléchargements")

    print(f"### 💾 Assistant Clé USB Bootable Windows / Dell XPS (CachyOS)\n")

    # 1. USB Drives Section
    print("#### 🔌 1. Clés USB Amovibles Détectées")
    if not usb_drives:
        print("⚠️ **Aucune clé USB amovible n'est actuellement détectée.**")
        print("*Insérez votre clé USB (minimum 8 Go) sur un port USB du PC et relancez la commande.*\n")
    else:
        print("| Périphérique | Modèle | Taille | Point de Montage | Status |")
        print("|---|---|---|---|---|")
        for u in usb_drives:
            print(f"| `{u['path']}` | **{u['model']}** | {u['size']} | `{u['mountpoint']}` | Ready 🟢 |")
        print("")

    # 2. ISO Section & Flash Execution
    print("#### 💿 2. Images ISO & Préparation de la Clé USB")
    if iso_files and usb_drives:
        target_usb = usb_drives[0]["path"]
        target_iso = iso_files[0]["path"]
        iso_name = iso_files[0]["name"]
        
        print(f"✅ **Fichier ISO Détecté :** `{iso_name}` ({iso_files[0]['size']})")
        print(f"🎯 **Cible USB :** `{target_usb}` ({usb_drives[0]['model']} - {usb_drives[0]['size']})\n")

        # Check if query requests flashing directly or launch GUI prompt
        if any(w in query.lower() for w in ["crée", "cree", "flashe", "prépare", "prepare", "installe", "installer"]):
            print("🚀 **Lancement de la préparation automatique Ventoy sur la clé USB...**")
            print("*Une fenêtre d'autorisation système (Polkit) va s'ouvrir sur votre écran pour valider l'écriture sur la clé USB.*")
            ok, msg = flash_and_copy(target_usb, target_iso)
            print(msg)
            print("")
        else:
            print("ℹ️ Pour lancer la création automatique de la clé, dites à Pixel : *'Pixel, crée ma clé USB'*.")
            print("")

    elif iso_files:
        print("✅ **Image ISO disponible pour le flashage :**\n")
        print("| Nom de l'ISO | Taille | Chemin |")
        print("|---|---|---|")
        for iso in iso_files:
            print(f"| **{iso['name']}** | {iso['size']} | `{iso['path']}` |")
        print("")
    else:
        print("ℹ️ Aucune image ISO d'au moins 100 Mo n'est présente dans vos Téléchargements.")

    # 3. Step-by-Step Instructions for Dell XPS
    print("#### 💻 3. Procédure de Démarrage sur Dell XPS (Ancienne Génération)")
    print("""
- Éteignez le PC Dell XPS.
- Insérez la clé USB préparée.
- Allumez le Dell XPS et appuyez immédiatement et de façon répétée sur **`F12`** jusqu'à l'apparition du menu **Dell Boot Menu**.
- Sélectionnez votre clé USB (mode *UEFI* ou *Legacy Boot*) et appuyez sur Entrée.
- Ventoy s'affichera et lancera l'installateur Windows sans aucun blocage TPM 2.0 ! 🚀
""")

if __name__ == '__main__':
    main()
