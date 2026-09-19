#!/usr/bin/env python3
import sys
import os
import shlex
import subprocess
import time
import select

def main():
    try:
        query = ""
        if len(sys.argv) > 1:
            query = sys.argv[1].strip()
        
        args = []
        if query:
            try:
                args = shlex.split(query)
            except Exception as e:
                # If shlex parsing fails due to quotes, split by spaces
                args = query.split()

        is_follow = False
        clean_args = []
        for arg in args:
            if arg in ["-f", "--follow"]:
                is_follow = True
            else:
                clean_args.append(arg)

        # Base journalctl command
        cmd = ["journalctl", "--no-pager"]

        if is_follow:
            # Real-time capture mode: run journalctl -f for 5 seconds
            cmd.append("-f")
            cmd.extend(clean_args)
            if not any(a in clean_args for a in ["-n", "--lines"]):
                cmd.extend(["-n", "20"])

            try:
                proc = subprocess.Popen(
                    cmd,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True
                )
                
                output_lines = []
                start_time = time.time()

                # Read output continuously for 5 seconds using non-blocking select
                while True:
                    elapsed = time.time() - start_time
                    if elapsed >= 5.0 or proc.poll() is not None:
                        break

                    remaining_time = max(0.1, 5.0 - elapsed)
                    rlist, _, _ = select.select([proc.stdout], [], [], min(0.5, remaining_time))
                    if proc.stdout in rlist:
                        line = proc.stdout.readline()
                        if line:
                            output_lines.append(line)
                        else:
                            break

                # Terminate process after real-time capture window
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=1.5)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                
                # Drain remaining output if any
                remaining, _ = proc.communicate(timeout=1.0)
                if remaining:
                    output_lines.extend(remaining.splitlines(keepends=True))

                result_text = "".join(output_lines).strip()
                if result_text:
                    print(f"=== CAPTURE LOGS EN TEMPS RÉEL (CACHYOS HÔTE - 5 SECONDES) ===\n{result_text}")
                else:
                    print("Aucun nouveau log capturé pendant la période d'observation en temps réel.")

            except Exception as e:
                print(f"Erreur lors de la capture en temps réel : {e}", file=sys.stderr)
        else:
            # Static query mode
            if not any(a in clean_args for a in ["-n", "--lines"]):
                clean_args.extend(["-n", "50"])
            
            cmd.extend(clean_args)

            try:
                result = subprocess.run(
                    cmd,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    timeout=15
                )

                if result.returncode == 0:
                    output = result.stdout.strip()
                    if output:
                        print(f"=== LOGS SYSTÈME HÔTE CACHYOS (journalctl) ===\n{output}")
                    else:
                        print("Aucun log ne correspond aux critères spécifiés.")
                else:
                    print(f"Erreur journalctl ({result.returncode}) : {result.stderr.strip()}", file=sys.stderr)

            except subprocess.TimeoutExpired:
                print("Erreur : La commande journalctl a dépassé le délai d'attente (15s).", file=sys.stderr)

    except Exception as e:
        print(f"Erreur interne de la brique cachyos_host_logs : {e}", file=sys.stderr)

if __name__ == '__main__':
    main()
