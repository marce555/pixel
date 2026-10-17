import sys
import shlex
import subprocess

def main():
    try:
        if len(sys.argv) < 2:
            print("Erreur : Veuillez fournir l'identifiant du serveur (ex: root@192.168.1.10).")
            return
            
        query = sys.argv[1].strip()
        args = shlex.split(query)
        if not args:
            print("Erreur : Aucun serveur spécifié.")
            return
            
        server = args[0]

        if '@' not in server:
            server = f"root@{server}"
        
        user_part, host_part = server.rsplit('@', 1)
        if not host_part or host_part.strip() == '':
            print(f"Erreur : Aucune adresse IP ou nom d'hôte valide trouvé dans '{server}'.")
            return

        # Audit rapide de santé et sécurité
        audit_cmd = (
            "echo '=== UPTIME ===' && uptime && "
            "echo '=== MEMORY ===' && free -m && "
            "echo '=== DISK ===' && df -h / | tail -n 1 && "
            "echo '=== SERVICES ===' && (systemctl is-active apache2 mariadb docker fail2ban 2>/dev/null || echo 'N/A') && "
            "echo '=== FAILED ===' && (systemctl --failed --no-legend 2>/dev/null | grep -v 'proc-fs-nfsd' | head -n 5 || true)"
        )
        
        cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", server, audit_cmd]
        result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=20)
        
        if result.returncode != 0:
            error_msg = result.stderr.strip() or "Connexion impossible"
            print(f"❌ Échec connexion SSH à {server} : {error_msg}")
            return

        lines = result.stdout.splitlines()
        section = None
        disk_str = ""
        mem_str = ""
        uptime_str = ""
        failed_services = []

        for line in lines:
            if line.startswith("=== UPTIME ==="):
                section = "uptime"
                continue
            elif line.startswith("=== MEMORY ==="):
                section = "mem"
                continue
            elif line.startswith("=== DISK ==="):
                section = "disk"
                continue
            elif line.startswith("=== SERVICES ==="):
                section = "services"
                continue
            elif line.startswith("=== FAILED ==="):
                section = "failed"
                continue

            if section == "uptime" and line.strip():
                uptime_str = line.strip()
            elif section == "disk" and line.strip():
                disk_str = line.strip()
            elif section == "failed" and line.strip():
                clean_line = line.strip().lstrip("●").strip()
                unit_name = clean_line.split()[0] if clean_line else ""
                if unit_name and not any(ign in unit_name for ign in ["proc-fs-nfsd", "run-rpc_pipefs"]):
                    failed_services.append(unit_name)

        # Évaluation
        critical_svcs = ["apache2", "nginx", "docker", "mariadb", "mysql", "postgresql", "ssh", "sshd", "fail2ban"]
        real_critical = [s for s in failed_services if any(c in s.lower() for c in critical_svcs)]
        secondary = [s for s in failed_services if s not in real_critical]

        issues = []
        if real_critical:
            issues.append(f"Services critiques en panne : {', '.join(real_critical)}")

        disk_critical = False
        disk_used = ""
        disk_free = ""
        if disk_str:
            cols = disk_str.split()
            if len(cols) >= 5:
                disk_used = cols[4]
                disk_free = cols[3]
                use_pct = cols[4].replace("%", "")
                try:
                    if int(use_pct) >= 90:
                        issues.append(f"Espace disque critique ({use_pct}%)")
                        disk_critical = True
                except ValueError:
                    pass

        if not issues:
            print(f"DIAGNOSTIC SERVEUR ({server}) : NOMINAL / SAIN")
            print(f"Le serveur est stable et en bon état de fonctionnement.")
            print(f"- Charge / Uptime : {uptime_str}")
            print(f"- Disque : {disk_used if disk_used else 'OK'} utilisé ({disk_free} libres)")
            if secondary:
                print(f"- Note d'administration : {len(secondary)} service(s) secondaire(s) inactif(s) répertorié(s) ({', '.join(secondary)}). Aucun impact sur les services de base.")
            else:
                print(f"- Tous les services de base sont opérationnels.")
        else:
            print(f"DIAGNOSTIC SERVEUR ({server}) : ATTENTION REQUISE")
            for iss in issues:
                print(f"- ⚠️ {iss}")
            if secondary:
                print(f"- Services secondaires inactifs : {', '.join(secondary)}")
            print(f"- Charge / Uptime : {uptime_str}")
            print(f"- Disque : {disk_used} utilisé ({disk_free} libres)")

    except subprocess.TimeoutExpired:
        print(f"❌ Erreur : Délai d'attente dépassé (timeout 20s) vers {server}.")
    except Exception as e:
        print(f"Erreur interne : {e}")

if __name__ == '__main__':
    main()
