import sys
import os
import re
import json
import urllib.request
import urllib.error
import subprocess

def is_private_ip(ip):
    # Filter out local loopback, link-local, RFC 1918 private IPs
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

def extract_ips(text):
    # Extract IPv4 and IPv6 addresses
    ipv4_pattern = r'\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b'
    ipv6_pattern = r'\b(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\b|\b(?:[0-9a-fA-F]{1,4}:){1,7}:[0-9a-fA-F]{0,4}\b'
    
    ips = set()
    for ip in re.findall(ipv4_pattern, text):
        if not is_private_ip(ip):
            ips.add(ip)
    for ip in re.findall(ipv6_pattern, text):
        if not is_private_ip(ip):
            ips.add(ip)
    return list(ips)

def get_active_connection_ips():
    # Execute ss -tun state established to capture active public connection peer IPs
    try:
        res = subprocess.run(["ss", "-tun", "state", "established"], capture_output=True, text=True, timeout=5)
        if res.returncode == 0:
            return extract_ips(res.stdout)
    except Exception:
        pass
    return []

def geolocate_ips(ip_list):
    if not ip_list:
        return []
    
    # Cap at 100 IPs for ip-api.com batch limit
    ip_list = ip_list[:100]
    
    url = "http://ip-api.com/batch"
    payload = json.dumps([{"query": ip, "fields": "status,message,country,countryCode,regionName,city,zip,lat,lon,isp,org,as,mobile,proxy,hosting,query"} for ip in ip_list]).encode('utf-8')
    
    req = urllib.request.Request(url, data=payload, headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read().decode('utf-8'))
            return data
    except Exception as e:
        print(f"Erreur d'accès à ip-api.com: {e}", file=sys.stderr)
        return []

def main():
    try:
        query = ""
        if len(sys.argv) > 1:
            query = sys.argv[1].strip()
        
        # 1. Parse IPs from argument or detect active connections
        ips = extract_ips(query)
        
        if not ips:
            # Try to fetch active connection peer IPs from local machine
            ips = get_active_connection_ips()
        
        if not ips:
            print("Aucune adresse IP publique valide n'a été spécifiée ou détectée dans la connexion active.")
            return

        # 2. Query ip-api.com
        results = geolocate_ips(ips)
        if not results:
            print("Impossible d'obtenir les données de géolocalisation depuis ip-api.com.")
            return

        # 3. Format output
        country_counts = {}
        successful_items = []
        
        for item in results:
            if item.get("status") == "success":
                country = item.get("country", "Inconnu")
                country_counts[country] = country_counts.get(country, 0) + 1
                successful_items.append(item)

        print(f"### 🌐 Rapport de Géolocalisation IP (ip-api.com)")
        print(f"**Nombre d'IPs analysées :** {len(successful_items)}\n")
        
        if country_counts:
            print("**Répartition par Pays :**")
            for cty, count in sorted(country_counts.items(), key=lambda x: x[1], reverse=True):
                print(f"- {cty} : {count} IP(s)")
            print("")

        print("| Adresse IP | Pays | Ville / Région | FAI / Organisation | Coordonnées |")
        print("|---|---|---|---|---|")
        for item in successful_items:
            ip = item.get("query", "")
            country = item.get("country", "-")
            city = item.get("city", "")
            region = item.get("regionName", "")
            loc_str = f"{city}, {region}".strip(", ")
            isp = item.get("isp") or item.get("org") or "-"
            lat = item.get("lat", "")
            lon = item.get("lon", "")
            coords = f"{lat}, {lon}" if lat and lon else "-"
            print(f"| `{ip}` | {country} | {loc_str} | {isp} | {coords} |")

    except Exception as e:
        print(f"Erreur critique lors de l'exécution de la brique ip_geo_analyzer: {e}", file=sys.stderr)

if __name__ == '__main__':
    main()
