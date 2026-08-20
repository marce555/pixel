import sys
import shlex
import subprocess

def main():
    try:
        if len(sys.argv) < 2:
            print("Erreur : Veuillez fournir la requête (ex: root@192.168.1.10 -u nginx).")
            return
            
        query = sys.argv[1].strip()
        args = shlex.split(query)
        if not args:
            print("Erreur : Requête invalide.")
            return
            
        server = args[0]

        # Validate that the server argument contains a '@' and a host part
        if '@' not in server:
            print(f"Erreur : L'argument '{server}' n'est pas un identifiant SSH valide. Format attendu : utilisateur@adresse_ip (ex: root@192.168.1.10).")
            return

        user_part, host_part = server.rsplit('@', 1)
        if not host_part or host_part.strip() == '':
            print(f"Erreur : Aucune adresse IP ou nom d'hôte trouvé dans '{server}'. Format attendu : utilisateur@adresse_ip.")
            return

        journal_args = args[1:]
        
        # Build journalctl command
        if not journal_args:
            # Default to last 50 lines of system log if no args provided
            journal_cmd = "journalctl -n 50 --no-pager"
        else:
            # If arguments are provided, use them but ensure no-pager is set
            journal_args_str = " ".join(shlex.quote(arg) for arg in journal_args)
            journal_cmd = f"journalctl --no-pager {journal_args_str}"
            
        cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", server, journal_cmd]
        
        result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=25)
        
        if result.returncode == 0:
            # Tronquer la sortie si elle est trop massive pour ne pas noyer le terminal/LLM
            out = result.stdout
            if len(out) > 5000:
                print(f"{out[:5000]}\n... [SORTIE TRONQUÉE CAR TROP LONGUE]")
            else:
                print(out)
        else:
            # Print errors to stdout so the agent can see them
            error_msg = result.stderr.strip()
            print(f"Erreur SSH/Journalctl (code {result.returncode}) lors de la connexion à {server} : {error_msg}")
            if "Permission denied" in result.stderr:
                print(f"\nNote: La clé SSH de cette machine n'est pas autorisée sur le serveur {server}.")
            elif "Connection refused" in result.stderr:
                print(f"\nNote: Le serveur {server} refuse la connexion SSH.")
            elif "Connection timed out" in result.stderr:
                print(f"\nNote: Le serveur {server} n'a pas répondu dans le délai imparti.")

    except subprocess.TimeoutExpired:
        print("Erreur : Timeout de la connexion ou lecture trop longue.")
    except Exception as e:
        print(f"Erreur interne de la brique : {e}")

if __name__ == '__main__':
    main()
