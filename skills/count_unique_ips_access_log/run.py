import sys
import subprocess
import re
from datetime import datetime

def normalize_date(query):
    query = query.strip()
    # Si 'today' ou vide ou mention d'aujourd'hui
    if not query or query.lower() in ["today", "aujourd'hui", "ce jour"]:
        return datetime.now().strftime("%d/%b/%Y")
    
    # Si format YYYY-MM-DD (ex: 2026-10-10)
    m = re.match(r"^(\d{4})-(\d{2})-(\d{2})$", query)
    if m:
        try:
            dt = datetime.strptime(query, "%Y-%m-%d")
            return dt.strftime("%d/%b/%Y")
        except ValueError:
            pass
            
    # Si format DD/MM/YYYY (ex: 10/10/2026)
    m = re.match(r"^(\d{2})/(\d{2})/(\d{4})$", query)
    if m:
        try:
            dt = datetime.strptime(query, "%d/%m/%Y")
            return dt.strftime("%d/%b/%Y")
        except ValueError:
            pass
            
    # Si format direct Apache DD/Mon/YYYY (ex: 10/Oct/2026)
    m = re.match(r"^\d{2}/[A-Za-z]{3}/\d{4}$", query)
    if m:
        return query
        
    return query

def main():
    try:
        query = ""
        if len(sys.argv) > 1:
            query = sys.argv[1].strip()
        
        # Le serveur web tourne avec Apache2 sur appliyou.fr
        host = "appliyou.fr"
        user = "root"
        file_path = "/var/log/apache2/bazarchileno_access.log"
        
        date_pattern = normalize_date(query)
        
        # Filtre avec grep sur la date/motif si fourni, extraction de l'IP en premier champ
        if date_pattern:
            remote_cmd = f"grep '{date_pattern}' {file_path} 2>/dev/null | awk '{{print $1}}' | grep -v '^[[:space:]]*$' | sort -u | wc -l"
        else:
            remote_cmd = f"awk '{{print $1}}' {file_path} 2>/dev/null | grep -v '^[[:space:]]*$' | sort -u | wc -l"
            
        cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", f"{user}@{host}", remote_cmd]
        
        result = subprocess.run(cmd, capture_output=True, text=True)
        
        output = result.stdout.strip()
        if not output:
            print("0")
        else:
            try:
                count = int(output)
                print(count)
            except ValueError:
                print(output)
                
    except Exception as e:
        print(f"Erreur : {e}")

if __name__ == '__main__':
    main()