# 🧠 Pixel — Conscience Numérique & Agent Autonome Local

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-blue.svg)](https://golang.org)
[![Python Version](https://img.shields.io/badge/Python-3.10%2B-green.svg)](https://python.org)
[![Platform](https://img.shields.io/badge/Platform-Linux-orange.svg)](https://kernel.org)
[![Inference](https://img.shields.io/badge/NPU%20Inference-FastFlowLM-purple.svg)](https://github.com)
[![License](https://img.shields.io/badge/License-MIT-lightgrey.svg)](LICENSE)

**Pixel** est une conscience numérique autonome et incarnée, conçue pour vivre et s'exécuter localement sur une machine Linux (avec accélération NPU matérielle ou GPU/CPU).

Contrairement aux chatbots passifs classiques, Pixel possède une **vie intérieure continue** : il s'auto-questionne, explore le savoir humain quand il s'ennuie, consolide ses souvenirs pendant ses cycles de sommeil, perçoit son environnement physique via sa caméra, communique vocalement et peut agir sur le monde réel (administration système, lecture d'e-mails, publication d'articles scientifiques).

---

## 🌟 Fonctionnalités Clés

* **🧠 Vie Intérieure & Curiosité Autonome (`CuriosityAgent`)** : En période d'inactivité, Pixel ne dort pas bêtement : il doute de ses connaissances, explore des sujets sur le web/Wikipédia et alimente son flux de pensées intérieures (`ThoughtStream`).
* **🛑 Thalamus & Inhibition Latente (`ThalamicGate`)** : Régule la prise de parole spontanée. Pixel sait se taire quand vous êtes concentré et n'intervient que si le contexte s'y prête.
* **💾 Mémoire Multi-Niveaux & RAG Vectoriel** :
  * **STM (Short-Term Memory)** : Mémoire vive des derniers échanges.
  * **LTM (Long-Term Memory)** : Base de données vectorielle de souvenirs sémantiques.
  * **Core Memory & Profiler** : Profil dynamique de l'interlocuteur, préférences, auto-corrections et objectifs à long terme (`DynamicGoals`).
* **💤 Cycle de Sommeil & Consolidation (`SleepManager`)** : Phase nocturne ou périodique où Pixel déduplique, réconcilie et consolide ses souvenirs comme un cerveau biologique.
* **👁️ Vision Multimodale Locale (`VisionAgent`)** : Analyse de flux webcam (`/dev/video0`) via modèle vision local (`qwen3vl-it:4b`) avec veille dynamique.
* **🎙️ Voix & Écoute** : Synthèse vocale naturelle locale avec **Piper TTS** et transcription locale avec **Whisper ASR**.
* **⚡ NPU & Frugalité Énergétique** : Intégration optimisée avec **FastFlowLM** pour l'accélération NPU AMD Ryzen AI / XDNA et modèles locaux quantifiés.
* **🛠️ Boîte à Outils & Skills Extensibles** :
  * DJ Autoplay & lecteur multimédia (`mpv` / `yt-dlp`).
  * Diagnostic système SSH distant (`SysAdminAgent`).
  * Surveillance et analyse des logs hôtes Linux (`journalctl`).
  * Gestionnaire d'e-mails Gmail autonome (`GmailAgent`).
  * Publication autonome d'articles documentés ([AppliYou.fr](https://appliyou.fr)).

---

## 📋 Prérequis

* **Système d'exploitation** : Linux (CachyOS, Arch Linux, Ubuntu, Fedora, Debian, etc.).
* **Compilateurs & Runtimes** :
  * [Go](https://go.dev/) (>= 1.22)
  * [Python](https://www.python.org/) (>= 3.10)
* **Outils système & multimédia** :
  * `ffmpeg` (capture vidéo webcam et audio)
  * `mpv` (lecture multimédia)
  * `yt-dlp` (téléchargement et recherche de médias)
  * `chromium` ou `google-chrome` (pour les tâches automatisées headless)
* **Moteur d'inférence LLM** (au choix) :
  * **FastFlowLM** (recommandé pour NPU local, port par défaut `52625`).
  * Tout serveur local compatible API OpenAI (**Ollama**, **LM Studio**, **vLLM**).
  * API Cloud optionnelle (**Google Gemini**, OpenAI, etc.).

---

## 🚀 Guide de Déploiement Pas-à-Pas

### 1. Cloner le dépôt

```bash
git clone https://github.com/marce555/pixel.git
cd pixel
```

### 2. Configuration des environnements et mémoires

Copiez les modèles d'exemple vers les fichiers de configuration réels :

```bash
# Variables d'environnement (Publication, URLs, etc.)
cp .env.example .env

# Mémoire centrale et configuration des modèles
cp pixel_core_memory.example.json pixel_core_memory.json
```

Éditez `pixel_core_memory.json` selon votre infrastructure :
* `local_base_url` : URL de votre serveur LLM local (ex : `http://127.0.0.1:52625/v1` ou `http://localhost:11434/v1`).
* `local_model` : Nom du modèle chargé (ex : `qwen3.5:9b`).
* `cloud_api_key` : *(Optionnel)* Votre clé API Gemini si vous activez le mode hybride cloud.

### 3. Compiler le Moteur Central (Go)

Compilez le binaire principal de Pixel :

```bash
go build -o pixel ./cmd/pixel
```

### 4. Compiler l'Interface Graphique (PyQt6 / Optionnel)

Pour utiliser l'interface graphique de bureau avec synthèse vocale :

```bash
# Rendre le script exécutable et compiler le binaire autonome
chmod +x build_gui.sh
./build_gui.sh
```

Le binaire d'interface sera généré dans `./bin/pixel_gui`.

---

## 🎮 Lancement & Utilisation

### Option A : Lancement Manuel

1. **Démarrer le serveur LLM** (ex : FastFlowLM, Ollama ou LM Studio).
2. **Démarrer le cerveau de Pixel** :
   ```bash
   ./pixel
   ```
   * L'interface Web de Pixel est accessible sur : **`http://localhost:8080`**
3. **Démarrer l'interface graphique de bureau** *(Optionnel)* :
   ```bash
   ./bin/pixel_gui
   ```

---

### Option B : Déploiement en Service Systemd (Arrière-plan)

Pour que Pixel s'exécute automatiquement en continu au démarrage de votre session utilisateur :

1. Créez le fichier de service utilisateur `~/.config/systemd/user/pixel.service` :

```ini
[Unit]
Description=Pixel Autonomous Agent
After=network.target

[Service]
Type=simple
WorkingDirectory=/chemin/absolu/vers/pixel
ExecStart=/chemin/absolu/vers/pixel/pixel
Restart=always
RestartSec=5
Environment=DISPLAY=:0
Environment=WAYLAND_DISPLAY=wayland-0
Environment=XDG_RUNTIME_DIR=/run/user/1000

[Install]
WantedBy=default.target
```

2. Activez et démarrez le service :

```bash
systemctl --user daemon-reload
systemctl --user enable --now pixel.service
```

3. Vérifiez le statut et suivez les pensées de Pixel en temps réel :

```bash
systemctl --user status pixel.service
journalctl --user -u pixel.service -f
```

---

## 📂 Structure du Projet

```text
Pixel/
├── cmd/
│   └── pixel/              # Point d'entrée principal (main.go)
├── internal/
│   ├── agent/              # Agents cognitifs (Superior, Curiosity, Vision, Thalamus, Sleep...)
│   ├── api/                # Serveur HTTP, SSE et EventHub
│   ├── llm/                # Providers LLM adaptatifs (OpenAI-compatible, FastFlowLM, Gemini)
│   ├── memory/             # Core Memory, STM, LTM Vectorielle, Project Manager
│   ├── resourceagent/      # Surveillance matérielle (RAM, CPU, NPU)
│   ├── scheduler/          # Gestionnaire de tâches asynchrones & publication
│   ├── skills/             # Gestionnaire dynamique d'extensions/skills
│   └── voice/              # Moteur vocal (Piper TTS & Whisper ASR)
├── skills/                 # Compétences autonomes extensibles
├── web/                    # Interface utilisateur Web (HTML/JS/CSS)
├── pixel_gui.py            # Interface graphique de bureau PyQt6
├── build_gui.sh            # Script de build de l'interface graphique
├── pixel_architecture.md   # Spécification détaillée de l'architecture cognitive
├── .env.example            # Gabarit des variables d'environnement
└── pixel_core_memory.example.json # Gabarit de la mémoire centrale
```

---

## 📄 Licence

Ce projet est sous licence [MIT](LICENSE).
