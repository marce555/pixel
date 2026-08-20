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

        # Validate that the server argument contains a '@' and a host part
        if '@' not in server:
            print(f"Erreur : L'argument '{server}' n'est pas un identifiant SSH valide. Format attendu : utilisateur@adresse_ip (ex: root@192.168.1.10).")
            return
        
        user_part, host_part = server.rsplit('@', 1)
        if not host_part or host_part.strip() == '':
            print(f"Erreur : Aucune adresse IP ou nom d'hôte trouvé dans '{server}'. Format attendu : utilisateur@adresse_ip (ex: root@192.168.1.10).")
            return

        # Basic health command: uptime, free memory, disk space
        health_cmd = "echo '--- UPTIME ---' && uptime && echo '\\n--- MEMOIRE (MB) ---' && free -m && echo '\\n--- DISQUE ---' && df -h /"
        
        # We use StrictHostKeyChecking=accept-new to avoid getting stuck on new hosts
        # BatchMode=yes to fail immediately if password is required (we don't want the agent to hang on password prompt)
        cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", server, health_cmd]
        
        result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=20)
        
        if result.returncode == 0:
            print(result.stdout)
        else:
            # Print errors to stdout so the agent can see them and report accurately
            error_msg = result.stderr.strip()
            print(f"Erreur SSH (code {result.returncode}) lors de la connexion à {server} : {error_msg}")
            if "Permission denied" in result.stderr:
                print(f"\nNote: La clé SSH de cette machine n'est pas autorisée sur le serveur {server}. Configurez l'authentification par clé SSH.")
            elif "Connection refused" in result.stderr:
                print(f"\nNote: Le serveur {server} refuse la connexion SSH (port 22 fermé ou SSH non actif).")
            elif "No route to host" in result.stderr or "Network is unreachable" in result.stderr:
                print(f"\nNote: Le serveur {server} est injoignable sur le réseau.")
            elif "Connection timed out" in result.stderr:
                print(f"\nNote: Le serveur {server} n'a pas répondu dans le délai imparti (timeout).")

    except subprocess.TimeoutExpired:
        print(f"Erreur : La connexion SSH vers {server} a dépassé le temps imparti (20s). Le serveur est peut-être injoignable ou surchargé.")
    except Exception as e:
        print(f"Erreur interne de la brique : {e}")

if __name__ == '__main__':
    main()
