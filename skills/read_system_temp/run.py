import sys
import os
import subprocess
import re

def main():
    try:
        if len(sys.argv) < 2:
            print("Erreur : Aucun argument fourni. Utilise 'read_system_temp <query>'")
            return

        query = sys.argv[1].lower()

        # Fonction pour exécuter une commande et récupérer le premier nombre trouvé
        def get_sensor_value(cmd):
            try:
                result = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=5)
                if result.returncode != 0:
                    return None
                # Chercher le premier nombre dans la sortie
                match = re.search(r'\d+\.?\d*', result.stdout)
                if match:
                    return float(match.group())
                return None
            except Exception:
                return None

        # Fonction pour lire directement /sys/class/thermal
        def get_thermal_value(path):
            try:
                if os.path.exists(path):
                    with open(path, 'r') as f:
                        content = f.read().strip()
                        # Nettoyer le contenu (souvent contient des unités ou des suffixes)
                        match = re.search(r'(\d+\.?\d*)', content)
                        if match:
                            return float(match.group())
            except Exception:
                pass
            return None

        results = {}

        # Tentative 1: lm-sensors
        if "cpu" in query or "processeur" in query:
            temp = get_sensor_value("sensors | grep -i cpu")
            if temp is not None:
                results["CPU"] = f"{temp:.1f} °C"
        
        # Tentative 2: lm-sensors pour le boîtier
        if "case" in query or "coffre" in query or "boitier" in query:
            temp = get_sensor_value("sensors | grep -i case")
            if temp is not None:
                results["Boîtier"] = f"{temp:.1f} °C"

        # Tentative 3: Lecture directe /sys/class/thermal (fallback)
        if not results and ("temp" in query or "chaleur" in query):
            thermal_zones = [
                "/sys/class/thermal/thermal_zone0/temp",
                "/sys/class/thermal/thermal_zone1/temp",
                "/sys/class/thermal/thermal_zone2/temp",
                "/sys/class/thermal/thermal_zone3/temp",
                "/sys/class/thermal/thermal_zone4/temp"
            ]
            for zone in thermal_zones:
                val = get_thermal_value(zone)
                if val is not None:
                    results["Zone " + zone.split('/')[-1]] = f"{val:.1f} °C"
                    break

        # Si aucune donnée n'a été trouvée
        if not results:
            print("Aucune donnée thermique disponible. Assurez-vous que lm-sensors est installé et que les données thermiques sont activées.")
        else:
            # Affichage formaté
            print("Températures système détectées :")
            for component, value in results.items():
                print(f"  - {component}: {value}")

    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()