import sys
import os
import subprocess
import re

def get_sensor_data():
    """Récupère les données de température depuis lm-sensors ou /sys."""
    temps = {}
    
    # Tenter d'utiliser lm-sensors si disponible
    try:
        result = subprocess.run(['lm-sensors'], capture_output=True, text=True, timeout=5)
        if result.returncode == 0:
            content = result.stdout
            # Recherche des lignes contenant "CPU" ou "CPU core"
            cpu_matches = re.findall(r'CPU\s+.*?\s*:\s*([0-9]+)°C', content, re.IGNORECASE)
            if cpu_matches:
                temps['cpu'] = cpu_matches[0]
            
            # Recherche des lignes contenant "Case" ou "Case temp"
            case_matches = re.findall(r'Case\s+.*?\s*:\s*([0-9]+)°C', content, re.IGNORECASE)
            if case_matches:
                temps['case'] = case_matches[0]
    except Exception as e:
        # Si lm-sensors échoue ou n'est pas installé, essayer de lire /sys/class/thermal
        try:
            # Tenter de lire les zones thermiques
            zones = os.listdir('/sys/class/thermal/')
            for zone in zones:
                path = f'/sys/class/thermal/{zone}'
                if os.path.exists(path):
                    try:
                        with open(f'{path}/temp', 'r') as f:
                            temp_str = f.read().strip()
                            # Convertir en degrés Celsius (le fichier est souvent en Kelvin ou mV)
                            # Pour simplifier, on essaie de convertir en Kelvin puis Celsius
                            if 'mV' in temp_str:
                                # Conversion approximative pour un exemple (nécessite le voltage)
                                # On suppose un voltage standard pour l'exemple ou on retourne la valeur brute
                                temps[zone] = temp_str
                            else:
                                temps[zone] = temp_str
                    except Exception:
                        continue
        except Exception:
            pass
    except Exception:
        pass
    
    return temps

def main():
    try:
        # L'argument passé par Pixel
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni.")
            return

        query = sys.argv[1].lower()
        
        temps = get_sensor_data()
        
        if not temps:
            print("Impossible de lire les températures. Vérifiez si lm-sensors est installé et actif.")
            return

        result_parts = []
        
        if 'cpu' in temps:
            result_parts.append(f"Température CPU : {temps['cpu']}°C")
        
        if 'case' in temps:
            result_parts.append(f"Température Boîtier : {temps['case']}°C")
        
        # Si on a lu des zones thermiques génériques
        if temps and 'cpu' not in temps and 'case' not in temps:
            for key, val in temps.items():
                result_parts.append(f"Température {key.capitalize()} : {val}")
        
        print(" ".join(result_parts))
        
    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()