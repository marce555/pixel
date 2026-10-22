import sys
import subprocess
import json
import re
import urllib.request
import urllib.error
from datetime import datetime
from collections import Counter

def normalize_date(query):
    query = (query or "").strip()
    if not query or query.lower() in ["today", "aujourd'hui", "ce jour", "logs"]:
        return datetime.now().strftime("%d/%b/%Y")
    
    # Format YYYY-MM-DD
    m = re.match(r"^(\d{4})-(\d{2})-(\d{2})$", query)
    if m:
        try:
            dt = datetime.strptime(query, "%Y-%m-%d")
            return dt.strftime("%d/%b/%Y")
        except ValueError:
            pass

    # Format DD/MM/YYYY
    m = re.match(r"^(\d{2})/(\d{2})/(\d{4})$", query)
    if m:
        try:
            dt = datetime.strptime(query, "%d/%m/%Y")
            return dt.strftime("%d/%b/%Y")
        except ValueError:
            pass

    # Format direct DD/Mon/YYYY
    if re.match(r"^\d{2}/[A-Za-z]{3}/\d{4}$", query):
        return query
        
    return query

def is_private_ip(ip):
    if ip.startswith("127.") or ip == "::1" or ip.startswith("fe80:"):
        return True
    if ip.startswith("10.") or ip.startswith("192.168."):
        return True
    if ip.startswith("172."):
        parts = ip.split(".")
        if len(parts) == 4 and parts[1].isdigit():
            val = int(parts[1])
            if 16 <= val <= 31:
                return True
    return False

def fetch_unique_ips_from_server(pattern):
    host = "appliyou.fr"
    user = "root"
    file_path = "/var/log/apache2/bazarchileno_access.log"

    if pattern:
        remote_cmd = f"grep '{pattern}' {file_path} 2>/dev/null | awk '{{print $1}}' | grep -v '^[[:space:]]*$' | sort -u"
    else:
        remote_cmd = f"tail -n 2000 {file_path} 2>/dev/null | awk '{{print $1}}' | grep -v '^[[:space:]]*$' | sort -u"

    cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", f"{user}@{host}", remote_cmd]
    res = subprocess.run(cmd, capture_output=True, text=True)
    if res.returncode != 0:
        raise RuntimeError(f"Erreur SSH ({res.returncode}): {res.stderr.strip()}")

    ips = [line.strip() for line in res.stdout.splitlines() if line.strip()]
    valid_ips = [ip for ip in ips if not is_private_ip(ip)]
    return valid_ips

def geolocate_batch(ip_list):
    # ip-api.com permet jusqu'à 100 IPs par requête batch
    all_results = []
    chunk_size = 100
    for i in range(0, min(len(ip_list), 300), chunk_size):
        chunk = ip_list[i:i + chunk_size]
        url = "http://ip-api.com/batch"
        payload = json.dumps([
            {"query": ip, "fields": "status,message,country,countryCode,city,regionName,isp,org,as,hosting,proxy,query"}
            for ip in chunk
        ]).encode("utf-8")
        req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                data = json.loads(resp.read().decode("utf-8"))
                all_results.extend(data)
        except Exception as e:
            print(f"Erreur appel géolocalisation: {e}", file=sys.stderr)
            break
    return all_results

def main():
    try:
        raw_query = ""
        if len(sys.argv) > 1:
            raw_query = sys.argv[1].strip()

        date_filter = normalize_date(raw_query)
        ips = fetch_unique_ips_from_server(date_filter)

        if not ips:
            print(f"Aucune adresse IP trouvée dans les logs d'accès pour le filtre : `{date_filter or 'global'}`.")
            return

        geo_data = geolocate_batch(ips)
        if not geo_data:
            print(f"{len(ips)} IP(s) trouvée(s), mais échec de la récupération des données de géolocalisation.")
            return

        countries = Counter()
        datacenter_count = 0
        residential_count = 0
        details = []

        for item in geo_data:
            if item.get("status") == "success":
                country = item.get("country", "Inconnu")
                is_hosting = item.get("hosting", False) or item.get("proxy", False)
                if is_hosting:
                    datacenter_count += 1
                else:
                    residential_count += 1

                countries[country] += 1
                details.append({
                    "ip": item.get("query"),
                    "country": country,
                    "city": item.get("city", "-"),
                    "isp": item.get("isp") or item.get("org") or "-",
                    "type": "Cloud / Datacenter (Bot potentiel)" if is_hosting else "Résidentiel (Visiteur humain potentiel)"
                })

        print(f"### 📊 Analyse de la provenance des visiteurs ({date_filter or 'Tous logs'})")
        print(f"- **Total IP uniques analysées :** {len(details)}")
        print(f"- **Connexions résidentielles / FAI :** {residential_count} ({residential_count * 100 // max(len(details), 1)}%)")
        print(f"- **Serveurs / Datacenters / Cloud :** {datacenter_count} ({datacenter_count * 100 // max(len(details), 1)}%)\n")

        print("#### 🌍 Répartition géographique par pays :")
        for country, count in countries.most_common(10):
            print(f"- **{country}** : {count} visiteur(s)")

        print("\n#### 🔍 Échantillon représentatif des connexions :")
        print("| IP | Pays | Ville | FAI / Fournisseur | Catégorie |")
        print("|---|---|---|---|---|")
        for row in details[:15]:
            print(f"| `{row['ip']}` | {row['country']} | {row['city']} | {row['isp']} | {row['type']} |")

    except Exception as e:
        print(f"Erreur lors de l'analyse des logs : {e}")

if __name__ == '__main__':
    main()
