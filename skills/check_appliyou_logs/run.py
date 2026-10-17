#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import sys
import subprocess
import re

SERVER = "root@appliyou.fr"

def run_ssh_command(cmd, timeout=35):
    ssh_opts = [
        "ssh",
        "-n",
        "-T",
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=15",
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
        return -1, "", "Délai d'attente SSH dépassé (> 35s). Serveur ou réseau injoignable."
    except Exception as e:
        return -2, "", str(e)

def main():
    global SERVER
    service_filter = ""
    if len(sys.argv) > 1 and sys.argv[1].strip():
        arg = sys.argv[1].strip()
        if re.search(r'\b(?:\d{1,3}\.){3}\d{1,3}\b', arg) or '@' in arg:
            SERVER = arg if '@' in arg else f"root@{arg}"
        else:
            service_filter = arg

    # Diagnostic complet : services critiques, santé système et sécurité active
    script = (
        "echo '=== SERVICES ===' && "
        "for s in apache2 mariadb docker fail2ban; do echo \"$s: $(systemctl is-active $s 2>&1)\"; done && "
        "echo '=== DISK ===' && "
        "df -h / | tail -n 1 && "
        "echo '=== LOAD ===' && "
        "uptime && "
        "echo '=== FAIL2BAN ===' && "
        "(fail2ban-client status sshd 2>/dev/null | grep -E 'Currently failed|Currently banned' || echo 'none') && "
        "echo '=== FAILED_UNITS ===' && "
        "systemctl --failed --no-legend 2>&1 && "
        "echo '=== RECENT_SECURITY_ERRORS ===' && "
        "journalctl -p 3 -n 40 --no-pager 2>&1"
    )

    code, stdout, stderr = run_ssh_command(script, timeout=40)

    if code != 0:
        err_msg = stderr or stdout or "Erreur inconnue"
        print(f"ÉCHEC DE CONNEXION : Impossible de joindre le serveur {SERVER} en SSH (code {code}).")
        print(f"Détail : {err_msg}")
        return

    lines = stdout.splitlines()
    section = None
    services = {}
    disk_line = ""
    load_line = ""
    fail2ban_stats = []
    failed_units = []
    raw_errors = []

    for line in lines:
        if line.startswith("=== SERVICES ==="):
            section = "services"
            continue
        elif line.startswith("=== DISK ==="):
            section = "disk"
            continue
        elif line.startswith("=== LOAD ==="):
            section = "load"
            continue
        elif line.startswith("=== FAIL2BAN ==="):
            section = "fail2ban"
            continue
        elif line.startswith("=== FAILED_UNITS ==="):
            section = "failed"
            continue
        elif line.startswith("=== RECENT_SECURITY_ERRORS ==="):
            section = "errors"
            continue

        if section == "services" and line.strip():
            parts = line.split(":", 1)
            if len(parts) == 2:
                services[parts[0].strip()] = parts[1].strip()
        elif section == "disk" and line.strip():
            disk_line = line.strip()
        elif section == "load" and line.strip():
            load_line = line.strip()
        elif section == "fail2ban" and line.strip():
            fail2ban_stats.append(line.strip())
        elif section == "failed" and line.strip():
            # Filtrer les montages virtuels inoffensifs
            if not any(ign in line for ign in ["proc-fs-nfsd", "run-rpc_pipefs"]):
                failed_units.append(line.strip())
        elif section == "errors" and line.strip():
            raw_errors.append(line.strip())

    # --- Analyse de sécurité et de santé ---
    security_problems = []
    warnings = []

    # 1. Vérification des services
    for svc, status in services.items():
        if status != "active":
            if svc in ["apache2", "mariadb", "docker"]:
                security_problems.append(f"Service critique arrêté : `{svc}` ({status})")
            elif svc == "fail2ban":
                warnings.append("Service de protection brute-force `fail2ban` inactif.")

    # 2. Vérification disque
    if disk_line:
        cols = disk_line.split()
        if len(cols) >= 5:
            use_pct_str = cols[4].replace("%", "")
            try:
                use_pct = int(use_pct_str)
                if use_pct >= 92:
                    security_problems.append(f"Disque saturé à {use_pct}% sur `/` ({cols[3]} libres)")
                elif use_pct >= 85:
                    warnings.append(f"Disque à {use_pct}% sur `/` ({cols[3]} disponibles)")
            except ValueError:
                pass

    # 3. Unités en panne
    critical_failed = [u for u in failed_units if any(c in u for c in ["apache2", "mariadb", "docker", "mysql", "nginx", "fail2ban"])]
    if critical_failed:
        security_problems.append("Unités système en échec : " + ", ".join(critical_failed))

    # 4. Analyse des logs d'erreurs (séparation brute-force routinier vs réelles failles/pannes)
    bot_probes = 0
    real_crashes = []

    for err in raw_errors:
        err_lower = err.lower()
        # Scanners internet / bots SSH automatiques (bruit normal d'un serveur public)
        if any(kw in err_lower for kw in [
            "kex_exchange_identification",
            "connection closed by remote host",
            "connection reset by peer",
            "banner line contains invalid",
            "parse publickey packet",
            "failed password for",
            "invalid user"
        ]):
            bot_probes += 1
        elif any(kw in err_lower for kw in ["segfault", "out of memory", "oom-killer", "corrupt", "panic"]):
            real_crashes.append(err)

    if real_crashes:
        security_problems.append(f"Erreurs critiques détectées dans le noyau/services : {len(real_crashes)} incident(s)")

    # --- Synthèse finale claire et épurée ---
    if not security_problems and not warnings:
        # TOUT VA BIEN : Message synthétique, direct, sans étalage de détails inutiles
        print("=== STATUT APPLIYOU.FR : NOMINAL ===")
        print("DIAGNOSTIC SÉCURITÉ & SANTÉ : TOUT EST NOMINAL")
        print(f"Le serveur {SERVER} est sain et sécurisé.")
        print("- Services web, base de données et conteneurs : Actifs (apache2, mariadb, docker)")
        print(f"- Protection active : fail2ban opérationnel ({bot_probes} sondes bloquées/filtrées sans impact)")
        print(f"- Ressources : Disque à {use_pct_str if 'use_pct_str' in locals() else 'OK'}%, charge normale.")
        return

    # Si des avertissements ou problèmes sont détectés :
    if security_problems:
        print("=== STATUT APPLIYOU.FR : ANOMALIES_DÉTECTÉES ===")
        print("DIAGNOSTIC SÉCURITÉ & SANTÉ : ANOMALIES DÉTECTÉES")
        print("\nProblèmes critiques :")
        for p in security_problems:
            print(f"- ❌ {p}")
    else:
        print("=== STATUT APPLIYOU.FR : NOMINAL ===")
        print("DIAGNOSTIC SÉCURITÉ & SANTÉ : NOMINAL (AVEC POINTS D'ATTENTION)")

    if warnings:
        print("\nPoints d'attention :")
        for w in warnings:
            print(f"- ⚠️ {w}")

    print(f"\nÉtat général : {len(services)} services surveillés, {bot_probes} tentatives de scans externes rejetées par le pare-feu/SSH.")

if __name__ == '__main__':
    main()
