#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import sys
import subprocess
import time

SERVER = "root@appliyou.fr"

def run_ssh_command(cmd, timeout=18):
    ssh_opts = [
        "ssh",
        "-n",
        "-T",
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=10",
        "-o", "ConnectionAttempts=2",
        "-o", "StrictHostKeyChecking=accept-new",
        SERVER,
        cmd
    ]
    try:
        res = subprocess.run(
            ssh_opts,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=timeout
        )
        return res.returncode, res.stdout.strip(), res.stderr.strip()
    except subprocess.TimeoutExpired:
        return -1, "", "Délai d'attente SSH dépassé (Timeout > 18s). Bannière SSH ou réseau injoignable."
    except Exception as e:
        return -2, "", str(e)

def main():
    service_filter = ""
    if len(sys.argv) > 1:
        service_filter = sys.argv[1].strip()

    # 1. Vérification de connectivité et collecte des métriques principales
    script = (
        "echo '=== FAILED_UNITS ===' && "
        "systemctl --failed --no-legend 2>&1 && "
        "echo '=== SERVICES ===' && "
        "for s in apache2 mariadb docker; do echo \"$s: $(systemctl is-active $s 2>&1)\"; done && "
        "echo '=== DISK ===' && "
        "df -h / | tail -n 1 && "
        "echo '=== LOGS_ERR ===' && "
    )
    if service_filter and service_filter not in ["all", "none"]:
        script += f"journalctl -u {service_filter} -p 3 -n 25 --no-pager 2>&1"
    else:
        script += "journalctl -p 3 -n 25 --no-pager 2>&1"

    code, stdout, stderr = run_ssh_command(script, timeout=20)

    if code != 0:
        err_msg = stderr or stdout or "Erreur inconnue"
        print(f"=== STATUT APPLIYOU.FR : ANOMALIE_CRITIQUE ===\n")
        print(f"Échec de la connexion SSH à {SERVER} (code {code}).")
        print(f"Détail : {err_msg}")
        print("\nImpact : Le serveur distant est injoignable par SSH ou le service SSHd est saturé/bloqué.")
        return

    # Analyse des données retournées
    has_incident = False
    incidents = []

    lines = stdout.splitlines()
    section = None
    failed_units = []
    service_status = {}
    disk_line = ""
    error_logs = []

    for line in lines:
        if line.startswith("=== FAILED_UNITS ==="):
            section = "failed"
            continue
        elif line.startswith("=== SERVICES ==="):
            section = "services"
            continue
        elif line.startswith("=== DISK ==="):
            section = "disk"
            continue
        elif line.startswith("=== LOGS_ERR ==="):
            section = "logs"
            continue

        if section == "failed" and line.strip():
            # Filtrer les montages inoffensifs de nfs/rpc
            if not any(ign in line for ign in ["proc-fs-nfsd", "run-rpc_pipefs"]):
                failed_units.append(line.strip())
        elif section == "services" and line.strip():
            parts = line.split(":", 1)
            if len(parts) == 2:
                svc = parts[0].strip()
                status = parts[1].strip()
                service_status[svc] = status
                if status != "active":
                    incidents.append(f"Service critique inactif : {svc} ({status})")
        elif section == "disk" and line.strip():
            disk_line = line.strip()
            cols = disk_line.split()
            if len(cols) >= 5:
                use_pct = cols[4].replace("%", "")
                try:
                    if int(use_pct) >= 90:
                        incidents.append(f"Espace disque saturé à {use_pct}% sur /")
                except ValueError:
                    pass
        elif section == "logs" and line.strip():
            # Ignore routine benign entries
            error_logs.append(line.strip())

    # Vérifier si des unités importantes sont failed
    critical_failed = [u for u in failed_units if any(c in u for c in ["apache2", "mariadb", "docker", "mysql", "nginx", "fail2ban"])]
    if critical_failed:
        incidents.append("Services en état 'failed' détectés : " + ", ".join(critical_failed))

    if incidents:
        print(f"=== STATUT APPLIYOU.FR : ANOMALIE_DETECTEE ===\n")
        print("Anomalies relevées :")
        for inc in incidents:
            print(f"- ⚠️ {inc}")
    else:
        print(f"=== STATUT APPLIYOU.FR : NOMINAL ===\n")
        print("Tous les services critiques (apache2, mariadb, docker) sont actifs et sains.")

    print(f"\nEspace disque : {disk_line}")
    if critical_failed:
        print(f"\nUnités système en échec :\n" + "\n".join(critical_failed))
    if error_logs:
        print(f"\nDerniers logs d'erreur (journalctl -p 3) :\n" + "\n".join(error_logs[-10:]))

if __name__ == '__main__':
    main()
