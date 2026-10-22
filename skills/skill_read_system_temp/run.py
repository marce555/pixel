import sys
import os
import json
import subprocess
import re

def run_command(cmd):
    try:
        result = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=10)
        return result.stdout.strip()
    except Exception as e:
        return None

def get_cpu_temp():
    # Utilise k10temp pour obtenir la température CPU réelle
    output = run_command("k10temp --sensor 1")
    if output:
        # k10temp retourne souvent 'CPU 1: 47.5C'
        match = re.search(r'(\d+\.?\d*)', output)
        if match:
            return float(match.group(1))
    return None

def get_case_temp():
    # Utilise lm-sensors (hwmon) pour obtenir la température du boîtier
    # On cherche généralement hwmon1 ou hwmon2, souvent 'temp1' ou 'temp2'
    # On évite les capteurs 'acpi_fan' et 'asus_custom_fan_curve' qui sont des ventilateurs ou des courbes
    sensors = run_command("lm-sensors")
    if not sensors:
        return None
    
    # On cherche les lignes contenant 'temp' et 'hwmon' pour identifier les capteurs de température
    # On ignore les lignes qui mentionnent 'fan' ou 'acpi' ou 'asus_custom'
    temp_lines = []
    for line in sensors.split('\n'):
        if 'temp' in line.lower() and 'hwmon' in line.lower():
            if 'fan' not in line.lower() and 'acpi' not in line.lower() and 'asus_custom' not in line.lower():
                temp_lines.append(line)
    
    if not temp_lines:
        return None
    
    # On prend la première ligne de température valide trouvée
    # Format typique: 'hwmon1: temp1=47.5 C'
    match = re.search(r'temp\d*=(\d+\.?\d*)', temp_lines[0])
    if match:
        return float(match.group(1))
    
    return None

def main():
    try:
        # L'argument passé par Pixel
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni.")
            return

        query = sys.argv[1]
        
        # Extraction des mots-clés pour déterminer ce que l'utilisateur veut
        query_lower = query.lower()
        
        cpu_temp = get_cpu_temp()
        case_temp = get_case_temp()
        
        if "cpu" in query_lower or "processeur" in query_lower:
            if cpu_temp is not None:
                print(f"Température CPU : {cpu_temp}°C")
            else:
                print("Impossible de lire la température CPU.")
        elif "case" in query_lower or "boitier" in query_lower or "coque" in query_lower:
            if case_temp is not None:
                print(f"Température Boîtier : {case_temp}°C")
            else:
                print("Impossible de lire la température du boîtier.")
        elif "température" in query_lower:
            # Si la requête est générale, on affiche les deux si disponibles
            if cpu_temp is not None:
                print(f"Température CPU : {cpu_temp}°C")
            if case_temp is not None:
                print(f"Température Boîtier : {case_temp}°C")
        else:
            # Si la requête ne concerne pas explicitement la température, on peut répondre poliment
            print("Je ne suis pas sûr de comprendre votre demande concernant la température.")
            
    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()