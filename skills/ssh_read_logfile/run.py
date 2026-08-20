import sys
import shlex
import subprocess

def main():
    try:
        if len(sys.argv) < 2:
            print("Erreur : Veuillez fournir la requête (ex: root@192.168.1.10 /var/log/syslog).")
            return
            
        query = sys.argv[1].strip()
        args = shlex.split(query)
        if len(args) < 2:
            print("Erreur : Requête invalide. Il faut au moins un serveur et un chemin de fichier (ex: root@192.168.1.10 /var/log/syslog).")
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

        filepath = args[1]
        
        # Default to reading the last 100 lines
        lines_arg = "-n 100"
        
        # Extract optional number of lines if provided
        for i, arg in enumerate(args[2:]):
            if arg == "-n" and i + 1 < len(args[2:]):
                lines_arg = f"-n {args[2+i+1]}"
        
        # We use tail to avoid reading gigantic log files entirely into memory
        tail_cmd = f"tail {lines_arg} {shlex.quote(filepath)}"
        
        cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", server, tail_cmd]
        
        result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=25)
        
        if result.returncode == 0:
            out = result.stdout
            if len(out) > 5000:
                print(f"{out[:5000]}\n... [SORTIE TRONQUÉE CAR TROP LONGUE]")
            else:
                print(out)
        else:
            # Print errors to stdout so the agent can see them
            error_msg = result.stderr.strip()
            print(f"Erreur SSH/Tail (code {result.returncode}) lors de la connexion à {server} : {error_msg}")
            if "Permission denied" in result.stderr:
                print(f"\nNote: La clé SSH de cette machine n'est pas autorisée sur le serveur {server}.")
            elif "No such file or directory" in result.stderr:
                print(f"\nNote: Le fichier {filepath} n'existe pas sur le serveur distant.")
            elif "Connection refused" in result.stderr:
                print(f"\nNote: Le serveur {server} refuse la connexion SSH.")

    except subprocess.TimeoutExpired:
        print("Erreur : Timeout de la connexion ou lecture trop longue.")
    except Exception as e:
        print(f"Erreur interne de la brique : {e}")

if __name__ == '__main__':
    main()
