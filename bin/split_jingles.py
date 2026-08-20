import os
import subprocess
import re

INPUT_DIR = "/home/marceloc/Musique/Annonces-Radio-Concierto"
OUTPUT_DIR = "/home/marceloc/Musique/Annonces-Radio-Concierto/Slices"

os.makedirs(OUTPUT_DIR, exist_ok=True)

def split_file(file_path):
    file_name = os.path.basename(file_path)
    print(f"Analyse de {file_name}...")
    
    # Commande pour détecter les silences de plus de 0.8 seconde avec un volume de -30dB
    cmd = [
        "ffmpeg", "-i", file_path,
        "-af", "silencedetect=noise=-30dB:d=0.8",
        "-f", "null", "-"
    ]
    
    result = subprocess.run(cmd, stderr=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    stderr = result.stderr
    
    # Extraire les débuts et fins de silences
    silence_starts = [float(x) for x in re.findall(r"silence_start: (\d+\.?\d*)", stderr)]
    silence_ends = [float(x) for x in re.findall(r"silence_end: (\d+\.?\d*)", stderr)]
    
    if not silence_starts:
        print("Aucun silence détecté. Fichier trop court ou continu.")
        return
        
    intervals = []
    start = 0.0
    for s_start, s_end in zip(silence_starts, silence_ends):
        if s_start > start + 2.0:  # Segment de plus de 2 secondes
            intervals.append((start, s_start))
        start = s_end
    
    intervals.append((start, None))
    
    print(f"Découpage en {len(intervals)} segments...")
    base_name = os.path.splitext(file_name)[0]
    
    for idx, (t_start, t_end) in enumerate(intervals, 1):
        output_file = os.path.join(OUTPUT_DIR, f"{base_name}_slice_{idx:03d}.mp3")
        
        args = ["ffmpeg", "-y", "-ss", str(t_start)]
        if t_end is not None:
            args += ["-to", str(t_end)]
            
        args += ["-i", file_path, "-acodec", "copy", output_file]
        subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        
    print(f"Terminé pour {file_name}.\n")

def main():
    print(f"Dossier source : {INPUT_DIR}")
    print(f"Dossier de sortie : {OUTPUT_DIR}\n")
    
    found = False
    for file in os.listdir(INPUT_DIR):
        file_path = os.path.join(INPUT_DIR, file)
        # On découpe les gros fichiers mp3 (plus de 5 Mo) qui sont des compilations
        if file.endswith(".mp3") and os.path.getsize(file_path) > 5 * 1024 * 1024:
            split_file(file_path)
            found = True
            
    if not found:
        print("Aucune grande compilation MP3 (> 5 Mo) trouvée à découper.")

if __name__ == "__main__":
    main()
