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
            print(f"Erreur : Aucune adresse IP ou nom d'hôte trouvé dans '{server}'. Format attendu : utilisateur@adresse_ip.")
            return

        # Identify top processes by memory and CPU
        ps_cmd = "echo '--- TOP PROCESSUS PAR MEMOIRE ---' && ps aux --sort=-%mem | head -n 10 && echo '\\n--- TOP PROCESSUS PAR CPU ---' && ps aux --sort=-%cpu | head -n 10"
        
        cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", server, ps_cmd]
        
        result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=20)
        
        if result.returncode == 0:
            print(result.stdout)
        else:
            # Print errors to stdout so the agent can see them
            error_msg = result.stderr.strip()
            print(f"Erreur SSH (code {result.returncode}) lors de la connexion à {server} : {error_msg}")
            if "Permission denied" in result.stderr:
                print(f"\nNote: La clé SSH de cette machine n'est pas autorisée sur le serveur {server}.")
            elif "Connection refused" in result.stderr:
                print(f"\nNote: Le serveur {server} refuse la connexion SSH.")
            elif "Connection timed out" in result.stderr:
                print(f"\nNote: Le serveur {server} n'a pas répondu dans le délai imparti.")

    except subprocess.TimeoutExpired:
        print("Erreur : La connexion SSH a dépassé le temps imparti.")
    except Exception as e:
        print(f"Erreur interne de la brique : {e}")

if __name__ == '__main__':
    main()
