#!/bin/bash
set -e

# Change directory to the script location
cd "$(dirname "$0")"

echo "=========================================="
echo "  🛠️  COMPILATION DU BINAIRE PIXEL GUI"
echo "=========================================="

echo "[Build] Création de l'environnement virtuel temporaire (venv)..."
python3 -m venv venv

echo "[Build] Installation des dépendances (PyInstaller, PyQt6, requests)..."
./venv/bin/pip install --quiet --upgrade pip
./venv/bin/pip install --quiet pyinstaller PyQt6 requests

echo "[Build] Compilation de pixel_gui.py en binaire autonome..."
./venv/bin/pyinstaller --onefile --windowed --name pixel_gui pixel_gui.py

echo "[Build] Copie du binaire autonome vers ./bin/..."
mkdir -p bin
rm -f bin/pixel_gui
cp dist/pixel_gui bin/pixel_gui

echo "[Build] Nettoyage des fichiers temporaires..."
rm -rf build dist pixel_gui.spec venv

echo "=========================================="
echo "  ✅  SUCCÈS : Binaire disponible dans :"
echo "      ./bin/pixel_gui"
echo "=========================================="
