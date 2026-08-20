import re

filepath = "/var/lib/lemonade/.cache/lemonade/bin/vllm/rocm/lib/python3.12/site-packages/vllm/platforms/__init__.py"

with open(filepath, "r") as f:
    content = f.read()

new_func = (
    "def cuda_platform_plugin() -> str | None:\n"
    "    # PATCH lemonade: libnvidia-ml.so.1 est present sur ce systeme (drivers NVIDIA),\n"
    "    # ce qui cause une double detection cuda+rocm. CUDA desactive dans ce venv ROCm.\n"
    '    logger.debug("CUDA platform disabled in ROCm venv (lemonade patch).")\n'
    "    return None"
)

pattern = re.compile(
    r"def cuda_platform_plugin\(\) -> str \| None:.*?return \"vllm\.platforms\.cuda\.CudaPlatform\" if is_cuda else None",
    re.DOTALL,
)

if pattern.search(content):
    content = pattern.sub(new_func, content)
    with open(filepath, "w") as f:
        f.write(content)
    print("PATCH APPLIQUE avec succes !")
else:
    print("ERREUR: fonction cible introuvable (deja patchee ?)")
    # Afficher la fonction actuelle pour debug
    match = re.search(r"def cuda_platform_plugin.*?(?=\ndef )", content, re.DOTALL)
    if match:
        print("Fonction actuelle:")
        print(match.group(0)[:300])
