#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import os
import sys
import time
import json
import struct
import tempfile
import subprocess
import requests
import re
from PyQt6.QtWidgets import (QApplication, QMainWindow, QWidget, QVBoxLayout, 
                             QHBoxLayout, QLabel, QTextBrowser, QLineEdit, 
                             QPushButton, QTabWidget, QTextEdit, QComboBox, 
                             QSlider, QCheckBox, QSystemTrayIcon, QMenu, QSplitter,
                             QScrollArea, QFrame, QSizePolicy, QMessageBox)
from PyQt6.QtCore import QThread, pyqtSignal, QTimer, Qt, QUrl, QFile, QIODevice
from PyQt6.QtGui import QIcon, QAction, QTextCursor, QFont, QColor
from PyQt6.QtMultimedia import QAudioSource, QAudioFormat, QMediaDevices, QMediaPlayer, QAudioOutput

# Constants
API_BASE = "http://127.0.0.1:8080"
SERVER_BIN = "./pixel"

class SSEReaderThread(QThread):
    proactive_received = pyqtSignal(str)
    
    def __init__(self):
        super().__init__()
        self.running = True
        
    def run(self):
        while self.running:
            try:
                # SSE request using stream=True and no timeout (long-lived)
                response = requests.get(f"{API_BASE}/api/events", stream=True, timeout=None)
                for line in response.iter_lines():
                    if not self.running:
                        break
                    if line:
                        decoded = line.decode('utf-8')
                        if decoded.startswith("data: "):
                            try:
                                payload = json.loads(decoded[6:].strip())
                                if "proactive_message" in payload:
                                    self.proactive_received.emit(payload["proactive_message"])
                            except Exception:
                                pass
            except Exception:
                # Silent retry if server is restarting or connection drops
                time.sleep(2)

class ChatStreamThread(QThread):
    token_received = pyqtSignal(str)
    error_received = pyqtSignal(str)
    finished_stream = pyqtSignal()
    
    def __init__(self, message, sender="Marcelo"):
        super().__init__()
        self.message = message
        self.sender = sender
        
    def run(self):
        try:
            response = requests.post(
                f"{API_BASE}/api/chat", 
                json={"message": self.message, "sender": self.sender}, 
                stream=True, 
                timeout=(10, None)
            )
            for line in response.iter_lines():
                if line:
                    decoded = line.decode('utf-8')
                    if decoded.startswith("data: "):
                        try:
                            payload = json.loads(decoded[6:].strip())
                            if "error" in payload:
                                self.error_received.emit(payload["error"])
                            elif "chunk" in payload:
                                self.token_received.emit(payload["chunk"])
                        except Exception:
                            pass
        except Exception as e:
            self.error_received.emit(str(e))
        finally:
            self.finished_stream.emit()

class SleepThread(QThread):
    finished_signal = pyqtSignal()
    
    def run(self):
        try:
            requests.post(f"{API_BASE}/api/force_sleep", timeout=30)
        except Exception:
            pass
        self.finished_signal.emit()

class TranscribeThread(QThread):
    finished_signal = pyqtSignal(str)
    
    def __init__(self, wav_path):
        super().__init__()
        self.wav_path = wav_path
        
    def run(self):
        text = ""
        try:
            with open(self.wav_path, 'rb') as f:
                files = {'file': ('mic.wav', f, 'audio/wav')}
                r = requests.post(f"{API_BASE}/api/voice/transcribe", files=files, timeout=15)
                if r.status_code == 200:
                    data = r.json()
                    text = data.get("text", "").strip()
                    # Filter Whisper hallucinations
                    if len(text) < 3 or re.search(r'^(.{10,50})\1{2,}', text):
                        text = ""
        except Exception as e:
            print(f"[GUI] Erreur transcription : {e}")
        finally:
            try: os.unlink(self.wav_path)
            except Exception: pass
            
        self.finished_signal.emit(text)

class TTSDownloadThread(QThread):
    finished_signal = pyqtSignal(str) # Emits local_wav_path
    
    def __init__(self, sentence, rate, pitch):
        super().__init__()
        self.sentence = sentence
        self.rate = rate
        self.pitch = pitch
        self.temp_file = os.path.join(tempfile.gettempdir(), f"tts_{int(time.time()*1000)}.wav")
        
    def run(self):
        try:
            url = f"{API_BASE}/api/voice/speak?text={requests.utils.quote(self.sentence)}&rate={self.rate}&pitch={self.pitch}"
            r = requests.get(url, timeout=40)
            if r.status_code == 200:
                with open(self.temp_file, 'wb') as f:
                    f.write(r.content)
                self.finished_signal.emit(self.temp_file)
                return
        except Exception as e:
            print(f"[GUI] Erreur téléchargement TTS : {e}")
        self.finished_signal.emit("")

class RecordReaderThread(QThread):
    finished_signal = pyqtSignal(str) # Emits wav_path
    speech_started_signal = pyqtSignal() # Emits when speech begins for barge-in
    
    def __init__(self, is_handsfree=False):
        super().__init__()
        self.is_handsfree = is_handsfree
        self.temp_wav_path = os.path.join(tempfile.gettempdir(), f"mic_{int(time.time()*1000)}.wav")
        self.process = None
        self.should_stop = False
        
    def stop(self):
        self.should_stop = True
        if self.process:
            try:
                self.process.terminate()
            except Exception:
                pass
                
    def run(self):
        import struct
        import math
        
        # Start parec subprocess streaming to stdout with real-time unbuffered default source capture
        try:
            self.process = subprocess.Popen(
                ["parec", "-d", "@DEFAULT_SOURCE@", "--channels=1", "--rate=16000", "--format=s16le", "--latency-msec=50"],
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL
            )
        except Exception as e:
            print(f"[GUI] Erreur lancement parec : {e}")
            self.finished_signal.emit("")
            return
            
        # Dynamic adaptive noise floor VAD with DC offset removal
        audio_data = bytearray()
        noise_floor = 3000.0
        speech_detected = False
        speech_start_time = None
        silence_start_time = None
        start_time = time.time()
        min_speech_ms = 250
        
        while not self.should_stop:
            # Read a small chunk (1024 bytes = 512 samples = ~32ms of audio)
            chunk = self.process.stdout.read(1024)
            if not chunk:
                break
                
            audio_data.extend(chunk)
            
            # Analyze AC volume (with DC offset removal)
            num_samples = len(chunk) // 2
            if num_samples > 0:
                try:
                    samples = struct.unpack(f"{num_samples}h", chunk)
                    mean = sum(samples) / num_samples
                    ac_rms = math.sqrt(sum((s - mean)**2 for s in samples) / num_samples)
                except Exception:
                    ac_rms = 0
                
                now = time.time()
                
                # Adapt background noise floor dynamically during non-speech
                if not speech_detected:
                    noise_floor = noise_floor * 0.94 + ac_rms * 0.06
                
                threshold = max(600.0, noise_floor * 1.4)
                
                # Check for speech detection
                if ac_rms > threshold:
                    if not speech_detected:
                        speech_start_time = now
                        speech_detected = True
                        self.speech_started_signal.emit()
                    silence_start_time = None
                elif speech_detected:
                    if silence_start_time is None:
                        silence_start_time = now
                    else:
                        silence_dur = now - silence_start_time
                        speech_dur = (silence_start_time - speech_start_time) * 1000
                        adaptive_delay = 1.0 if speech_dur > 2000 else (1.3 if self.is_handsfree else 1.0)
                        
                        if silence_dur > adaptive_delay and speech_dur > min_speech_ms:
                            print(f"[GUI] Dynamic VAD : Fin de parole ({int(speech_dur)}ms parole, {int(silence_dur*1000)}ms silence) -> Transcription")
                            break
                            
            # Max duration safety (15s)
            if time.time() - start_time > 15.0:
                print("[GUI] Durée max d'enregistrement atteinte (15s)")
                break
                
        # Terminate subprocess
        if self.process:
            try:
                self.process.terminate()
                self.process.wait(timeout=1)
            except Exception:
                pass
                
        # Write WAV file
        if len(audio_data) > 44:
            if AudioWavWriter.write_wav_header_from_bytes(audio_data, self.temp_wav_path):
                self.finished_signal.emit(self.temp_wav_path)
                return
                
        self.finished_signal.emit("")

class AudioWavWriter:
    @staticmethod
    def write_wav_header(raw_path, wav_path, sample_rate=16000, channels=1, bits_per_sample=16):
        if not os.path.exists(raw_path):
            return False
        with open(raw_path, 'rb') as raw_file:
            raw_data = raw_file.read()
            
        data_size = len(raw_data)
        # 44-byte WAV header
        header = struct.pack('<4sI4s', b'RIFF', 36 + data_size, b'WAVE')
        header += struct.pack('<4sIHHIIHH', b'fmt ', 16, 1, channels, sample_rate, 
                              sample_rate * channels * (bits_per_sample // 8), 
                              channels * (bits_per_sample // 8), bits_per_sample)
        header += struct.pack('<4sI', b'data', data_size)
        
        with open(wav_path, 'wb') as wav_file:
            wav_file.write(header)
            wav_file.write(raw_data)
        return True

    @staticmethod
    def write_wav_header_from_bytes(raw_bytes, wav_path, sample_rate=16000, channels=1, bits_per_sample=16):
        try:
            data_size = len(raw_bytes)
            # 44-byte WAV header
            header = struct.pack('<4sI4s', b'RIFF', 36 + data_size, b'WAVE')
            header += struct.pack('<4sIHHIIHH', b'fmt ', 16, 1, channels, sample_rate, 
                                  sample_rate * channels * (bits_per_sample // 8), 
                                  channels * (bits_per_sample // 8), bits_per_sample)
            header += struct.pack('<4sI', b'data', data_size)
            
            with open(wav_path, 'wb') as wav_file:
                wav_file.write(header)
                wav_file.write(raw_bytes)
            return True
        except Exception as e:
            print(f"[GUI] Erreur écriture WAV en mémoire : {e}")
            return False

def parse_markdown(text):
    if not text:
        return ""
    
    # Code blocks (```lang ... ```)
    def repl_codeblock(m):
        code = m.group(2).strip()
        code = code.replace("<", "&lt;").replace(">", "&gt;")
        return f'<pre style="background-color: #090d16; border: 1px solid rgba(255, 255, 255, 0.12); border-radius: 8px; padding: 10px 14px; color: #38bdf8; font-family: monospace; font-size: 13px; margin: 8px 0;"><code>{code}</code></pre>'
    
    text = re.sub(r'```([a-zA-Z0-9_+-]*)\n(.*?)```', repl_codeblock, text, flags=re.DOTALL)
    
    # Inline code (`...`)
    text = re.sub(r'`([^`]+)`', r'<code style="background-color: rgba(255, 255, 255, 0.12); color: #f472b6; padding: 2px 6px; border-radius: 4px; font-family: monospace; font-size: 13px;">\1</code>', text)
    
    # Headers
    text = re.sub(r'^#### (.*$)', r'<h4 style="color:#c084fc; margin-top:10px; margin-bottom:4px; font-size: 15px;">\1</h4>', text, flags=re.MULTILINE)
    text = re.sub(r'^### (.*$)', r'<h3 style="color:#c084fc; margin-top:12px; margin-bottom:6px; font-size: 16px;">\1</h3>', text, flags=re.MULTILINE)
    text = re.sub(r'^## (.*$)', r'<h2 style="color:#c084fc; margin-top:14px; margin-bottom:8px; font-size: 18px;">\1</h2>', text, flags=re.MULTILINE)
    text = re.sub(r'^# (.*$)', r'<h1 style="color:#c084fc; margin-top:16px; margin-bottom:10px; font-size: 20px;">\1</h1>', text, flags=re.MULTILINE)
    
    # Bold
    text = re.sub(r'\*\*(.*?)\*\*', r'<strong style="color:#ffffff;">\1</strong>', text)
    
    # Italic
    text = re.sub(r'\*(.*?)\*', r'<em>\1</em>', text)
    
    # Links
    text = re.sub(r'\[([^\]]+)\]\(([^)]+)\)', r'<a href="\2" style="color:#38bdf8; text-decoration:none; font-weight:600;">\1</a>', text)
    
    # Lists
    text = re.sub(r'^\s*-\s+(.*$)', r'<li style="margin-left:15px; margin-bottom:4px;">\1</li>', text, flags=re.MULTILINE)
    
    # Replace newlines with <br>
    text = text.replace('\n', '<br>')
    
    # Clean up duplicate <br> before block tags
    text = re.sub(r'(<\/h[1-4]>|<pre[^>]*>.*?<\/pre>|<li[^>]*>.*<\/li>)<br>', r'\1', text)
    text = re.sub(r'<br>(<h[1-4]>|<pre[^>]*>|<li[^>]*>)', r'\1', text)
    
    return text

def format_response_html(text):
    response_text = text
    thought_text = ""
    
    thought_markers = [
        "**Pensée interne de Pixel :**",
        "**Pensée interne de Pixel:**",
        "Pensée interne de Pixel :",
        "Pensée interne de Pixel:",
        "**Pensée interne :**",
        "**Pensée interne:**",
        "Pensée interne :",
        "Pensée interne:"
    ]
    
    marker_index = -1
    marker_length = 0
    for marker in thought_markers:
        idx = response_text.find(marker)
        if idx != -1:
            marker_index = idx
            marker_length = len(marker)
            break
            
    if marker_index != -1:
        thought_text = response_text[marker_index + marker_length:].strip()
        response_text = response_text[:marker_index].strip()
        thought_text = re.sub(r'^[\s*_~]+|[\s*_~]+$', '', thought_text)
        
    direct_markers = [
        "**Réponse directe :**",
        "**Réponse directe:**",
        "Réponse directe :",
        "Réponse directe:",
        "**Réponse :**",
        "**Réponse:**",
        "Réponse :",
        "Réponse:"
    ]
    
    for marker in direct_markers:
        if response_text.startswith(marker):
            response_text = response_text[len(marker):].strip()
            break
            
    response_text = re.sub(r'^[\s*_~]+|[\s*_~]+$', '', response_text)
    
    formatted_response = parse_markdown(response_text)
    formatted_thought = parse_markdown(thought_text) if thought_text else ""
    
    if thought_text:
        return f"""
        <div class="direct-reply">{formatted_response}</div>
        <div style="background-color: rgba(124, 58, 237, 0.08); border-left: 3px solid #8b5cf6; padding: 10px 14px; margin-top: 10px; border-radius: 8px;">
            <div style="color: #c084fc; font-weight: bold; font-size: 0.85em; margin-bottom: 4px;">
                💡 Pensée interne de Pixel
            </div>
            <div style="color: #cbd5e1; font-style: italic; font-size: 0.9em; line-height: 1.4;">{formatted_thought}</div>
        </div>
        """
        
    return formatted_response

class MainWindow(QMainWindow):
    def __init__(self):
        super().__init__()
        self.server_process = None
        self.sse_thread = None
        self.chat_thread = None
        self.is_recording = False
        self.audio_source = None
        self.temp_raw_file = None
        self.voice_rate = 1.0
        self.voice_pitch = 1.0
        self.continuous_voice = True
        self.voice_enabled = True
        self.chat_failed = False
        
        # Audio Player setup
        self.media_player = QMediaPlayer()
        self.audio_output = QAudioOutput()
        self.media_player.setAudioOutput(self.audio_output)
        self.media_player.mediaStatusChanged.connect(self._on_media_status_changed)
        
        # TTS queue
        self.speech_queue = []
        self.speech_buffer = ""
        self.is_speaking_sentence = False
        self.is_speaking_session = False
        self.is_stream_finished = False
        
        self.setWindowTitle("Pixel - Compagnon Conscient")
        self.resize(950, 750)
        
        # Start Go backend server
        self.start_backend_server()
        
        # Setup UI
        self.setup_ui()
        self.apply_qss()
        self.set_handsfree(self.continuous_voice)
        
        # Timer for polling server info
        self.poll_timer = QTimer(self)
        self.poll_timer.timeout.connect(self.poll_server_data)
        
        # Connection check before loading data
        self.connection_retry_count = 0
        self.init_timer = QTimer(self)
        self.init_timer.timeout.connect(self.check_connection_and_init)
        self.init_timer.start(500)
        
        # Switch audio cards to duplex automatically
        self.ensure_duplex_profile()
        
    def ensure_duplex_profile(self):
        print("[GUI] Configuration de la carte audio en Duplex (Entrée + Sortie)...")
        try:
            output = subprocess.check_output(["pactl", "list", "cards", "short"], stderr=subprocess.DEVNULL).decode('utf-8')
            for line in output.splitlines():
                parts = line.split()
                if len(parts) >= 2:
                    card_name = parts[1]
                    # Try setting to analog duplex or pro-audio
                    subprocess.run(
                        ["pactl", "set-card-profile", card_name, "output:analog-stereo+input:analog-stereo"],
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL
                    )
        except Exception as e:
            print(f"[GUI] Erreur ensure_duplex_profile : {e}")
            
    def start_backend_server(self):
        # Check if server is already running on port 8080
        try:
            requests.get(f"{API_BASE}/api/system_status", timeout=1)
            print("[GUI] Le serveur Pixel est déjà en cours d'exécution.")
            return
        except Exception:
            pass
            
        print("[GUI] Démarrage du serveur backend Pixel...")
        bin_path = SERVER_BIN
        if not os.path.exists(bin_path):
            bin_path = "./bin/pixel"
            
        if os.path.exists(bin_path):
            try:
                self.server_process = subprocess.Popen(
                    [bin_path],
                    cwd=os.getcwd(),
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL
                )
                print(f"[GUI] Serveur Pixel lancé avec succès (PID: {self.server_process.pid})")
            except Exception as e:
                print(f"[GUI] Erreur lors du lancement du serveur : {e}")
        else:
            QMessageBox.critical(
                self, 
                "Erreur", 
                "Impossible de trouver le binaire 'pixel' dans le dossier racine ou dans ./bin."
            )

    def check_connection_and_init(self):
        try:
            requests.get(f"{API_BASE}/api/system_status", timeout=1)
            self.init_timer.stop()
            print("[GUI] Connexion établie avec le serveur backend.")
            self.load_initial_data()
            self.poll_timer.start(10000) # Every 10 seconds for general stats
            
            # Start SSE thread
            self.sse_thread = SSEReaderThread()
            self.sse_thread.proactive_received.connect(self.handle_proactive_message, Qt.ConnectionType.QueuedConnection)
            self.sse_thread.start()
        except Exception:
            self.connection_retry_count += 1
            self.chat_display.setHtml(
                f"<div style='color:#9ca3af; text-align:center; margin-top:20px;'>"
                f"Connexion au serveur Pixel en cours... (tentative {self.connection_retry_count})</div>"
            )
            if self.connection_retry_count > 20:
                self.init_timer.stop()
                QMessageBox.critical(
                    self, 
                    "Erreur de Connexion", 
                    "Le serveur Pixel ne répond pas. Vérifiez que le binaire s'exécute correctement."
                )

    def load_initial_data(self):
        self.fetch_chat_history()
        self.poll_server_data()
        self.fetch_tasks()
        self.fetch_gmail_settings()
        
        # Start a faster timer for Tasks (every 2s)
        self.tasks_timer = QTimer(self)
        self.tasks_timer.timeout.connect(self.fetch_tasks)
        self.tasks_timer.start(2000)

    def setup_ui(self):
        # Central widget is a QHBoxLayout split into Sidebar and Main Area
        main_widget = QWidget()
        self.setCentralWidget(main_widget)
        main_layout = QHBoxLayout(main_widget)
        main_layout.setContentsMargins(0, 0, 0, 0)
        main_layout.setSpacing(0)
        
        # Splitter to allow resizing sidebar
        splitter = QSplitter(Qt.Orientation.Horizontal)
        main_layout.addWidget(splitter)
        
        # 1. Sidebar (Console)
        sidebar = QFrame()
        sidebar.setObjectName("sidebar")
        sidebar.setMinimumWidth(280)
        sidebar_layout = QVBoxLayout(sidebar)
        sidebar_layout.setContentsMargins(15, 15, 15, 15)
        sidebar_layout.setSpacing(15)
        
        # Header
        sb_header = QHBoxLayout()
        sb_title = QLabel("Console Système")
        sb_title.setStyleSheet("font-size: 1.1em; font-weight: bold; letter-spacing: 0.5px;")
        self.status_led = QLabel()
        self.status_led.setFixedSize(10, 10)
        self.status_led.setStyleSheet("background-color: #10b981; border-radius: 5px;")
        sb_header.addWidget(sb_title)
        sb_header.addStretch()
        sb_header.addWidget(self.status_led)
        sidebar_layout.addLayout(sb_header)
        
        # Nervous system block (Survie)
        survie_block = QFrame()
        survie_block.setObjectName("survieBlock")
        survie_layout = QVBoxLayout(survie_block)
        survie_layout.setSpacing(8)
        
        survie_title = QLabel("SYSTÈME NERVEUX (SURVIE)")
        survie_title.setStyleSheet("color: #a78bfa; font-size: 0.8em; font-weight: bold; letter-spacing: 1px;")
        survie_layout.addWidget(survie_title)
        
        self.lbl_internet = QLabel("Internet: En attente...")
        self.lbl_ram = QLabel("RAM: -- %")
        self.lbl_cpu = QLabel("CPU (1m): -- / --")
        
        survie_layout.addWidget(self.lbl_internet)
        survie_layout.addWidget(self.lbl_ram)
        survie_layout.addWidget(self.lbl_cpu)
        sidebar_layout.addWidget(survie_block)
        
        # Live log console
        log_title = QLabel("📋 FLUX EN TEMPS RÉEL")
        log_title.setStyleSheet("color: #60a5fa; font-size: 0.8em; font-weight: bold; letter-spacing: 1px; margin-top: 10px;")
        sidebar_layout.addWidget(log_title)
        
        self.log_console = QTextEdit()
        self.log_console.setObjectName("sysLogs")
        self.log_console.setReadOnly(True)
        sidebar_layout.addWidget(self.log_console)
        
        # Force Sleep button
        self.btn_sleep = QPushButton("Forcer Sommeil (Sauvegarder)")
        self.btn_sleep.setIcon(QIcon.fromTheme("system-suspend"))
        self.btn_sleep.clicked.connect(self.trigger_force_sleep)
        sidebar_layout.addWidget(self.btn_sleep)
        
        splitter.addWidget(sidebar)
        
        # 2. Main Tab widget
        self.tabs = QTabWidget()
        splitter.addWidget(self.tabs)
        
        # Tab 1: Chat Window (Dialogue)
        chat_tab = QWidget()
        chat_tab_layout = QVBoxLayout(chat_tab)
        chat_tab_layout.setContentsMargins(15, 15, 15, 15)
        chat_tab_layout.setSpacing(12)
        
        # Chat header Actions
        chat_header = QHBoxLayout()
        chat_title = QLabel("Pixel - Discussion")
        chat_title.setStyleSheet("font-size: 1.2em; font-weight: bold; color: #a78bfa;")
        chat_header.addWidget(chat_title)
        chat_header.addStretch()
        
        # Speaker toggle button
        self.btn_speaker = QPushButton("🔊 Voix")
        self.btn_speaker.setCheckable(True)
        self.btn_speaker.setChecked(True)
        self.btn_speaker.clicked.connect(self.toggle_voice)
        chat_header.addWidget(self.btn_speaker)
        
        # Hands-free toggle button
        self.btn_handsfree = QPushButton("🙌 Mains Libres")
        self.btn_handsfree.setCheckable(True)
        self.btn_handsfree.setChecked(self.continuous_voice)
        self.btn_handsfree.clicked.connect(lambda checked: self.set_handsfree(checked))
        chat_header.addWidget(self.btn_handsfree)
        
        chat_tab_layout.addLayout(chat_header)
        
        # Text display area
        self.chat_display = QTextBrowser()
        self.chat_display.setOpenExternalLinks(True)
        self.chat_display.setLineWrapMode(QTextBrowser.LineWrapMode.WidgetWidth)
        self.chat_display.setHorizontalScrollBarPolicy(Qt.ScrollBarPolicy.ScrollBarAlwaysOff)
        chat_tab_layout.addWidget(self.chat_display)
        
        # Input block
        input_bar = QHBoxLayout()
        input_bar.setSpacing(10)
        
        self.btn_mic = QPushButton("🎙️")
        self.btn_mic.setToolTip("Parler à Pixel")
        self.btn_mic.setFixedSize(44, 44)
        self.btn_mic.clicked.connect(self.toggle_microphone)
        input_bar.addWidget(self.btn_mic)
        
        self.input_field = QLineEdit()
        self.input_field.setPlaceholderText("Écrivez un message ou parlez...")
        self.input_field.setFixedHeight(44)
        self.input_field.returnPressed.connect(self.send_chat_message)
        input_bar.addWidget(self.input_field)
        
        self.btn_send = QPushButton("⚡")
        self.btn_send.setFixedSize(44, 44)
        self.btn_send.clicked.connect(self.send_chat_message)
        input_bar.addWidget(self.btn_send)
        
        chat_tab_layout.addLayout(input_bar)
        self.tabs.addTab(chat_tab, "💬 Dialogue")
        
        # Tab 2: Cerveau & Persona
        brain_tab = QWidget()
        brain_layout = QVBoxLayout(brain_tab)
        
        self.lbl_persona_title = QLabel("🎭 Identité (Persona)")
        self.lbl_persona_title.setStyleSheet("font-weight: bold; color: #a78bfa; margin-bottom:4px;")
        self.txt_persona = QTextEdit()
        self.txt_persona.setReadOnly(True)
        
        self.lbl_profile_title = QLabel("👤 Profil Utilisateur")
        self.lbl_profile_title.setStyleSheet("font-weight: bold; color: #a78bfa; margin-bottom:4px; margin-top:10px;")
        self.txt_profile = QTextEdit()
        self.txt_profile.setReadOnly(True)
        
        self.lbl_learnings_title = QLabel("💡 Apprentissages & Découvertes Web (LTM)")
        self.lbl_learnings_title.setStyleSheet("font-weight: bold; color: #a78bfa; margin-bottom:4px; margin-top:10px;")
        self.txt_learnings = QTextEdit()
        self.txt_learnings.setReadOnly(True)
        
        brain_layout.addWidget(self.lbl_persona_title)
        brain_layout.addWidget(self.txt_persona)
        brain_layout.addWidget(self.lbl_profile_title)
        brain_layout.addWidget(self.txt_profile)
        brain_layout.addWidget(self.lbl_learnings_title)
        brain_layout.addWidget(self.txt_learnings)
        self.tabs.addTab(brain_tab, "🎭 Cerveau")
        
        # Tab 3: Perceptions & Projet
        perc_tab = QWidget()
        perc_layout = QVBoxLayout(perc_tab)
        
        self.lbl_vision_title = QLabel("👁️ Perception Visuelle Locale (Webcam)")
        self.lbl_vision_title.setStyleSheet("font-weight: bold; color: #a78bfa; margin-bottom:4px;")
        self.txt_vision = QTextEdit()
        self.txt_vision.setReadOnly(True)
        
        self.lbl_proj_title = QLabel("📌 Projet Actif")
        self.lbl_proj_title.setStyleSheet("font-weight: bold; color: #a78bfa; margin-bottom:4px; margin-top:10px;")
        
        self.proj_card = QFrame()
        self.proj_card.setObjectName("survieBlock")
        card_layout = QVBoxLayout(self.proj_card)
        self.lbl_proj_name = QLabel("Projet: --")
        self.lbl_proj_name.setStyleSheet("font-weight: bold; font-size: 1.1em; color: #60a5fa;")
        self.lbl_proj_goal = QLabel("Objectif: --")
        self.lbl_proj_step = QLabel("Étape Actuelle: --")
        self.lbl_proj_step.setStyleSheet("color: #f59e0b; font-weight: bold;")
        self.txt_proj_milestones = QTextEdit()
        self.txt_proj_milestones.setReadOnly(True)
        
        card_layout.addWidget(self.lbl_proj_name)
        card_layout.addWidget(self.lbl_proj_goal)
        card_layout.addWidget(self.lbl_proj_step)
        card_layout.addWidget(QLabel("Jalons du projet :"))
        card_layout.addWidget(self.txt_proj_milestones)
        
        perc_layout.addWidget(self.lbl_vision_title)
        perc_layout.addWidget(self.txt_vision)
        perc_layout.addWidget(self.lbl_proj_title)
        perc_layout.addWidget(self.proj_card)
        self.tabs.addTab(perc_tab, "👁️ Perceptions")
        
        # Tab 4: Tasks Dashboard
        tasks_tab = QWidget()
        tasks_layout = QVBoxLayout(tasks_tab)
        
        # Top bar with clean action
        task_header = QHBoxLayout()
        task_header.addWidget(QLabel("⏳ Gestionnaire de Tâches Asynchrones"))
        task_header.addStretch()
        btn_clear_tasks = QPushButton("Nettoyer l'historique")
        btn_clear_tasks.setStyleSheet("background: rgba(239, 68, 68, 0.2); border: 1px solid #ef4444;")
        btn_clear_tasks.clicked.connect(self.clear_tasks_history)
        task_header.addWidget(btn_clear_tasks)
        tasks_layout.addLayout(task_header)
        
        # Active Task
        tasks_layout.addWidget(QLabel("⚡ Tâche En cours :"))
        self.lbl_active_task = QLabel("Aucune tâche active")
        self.lbl_active_task.setStyleSheet("font-weight: bold; color: #60a5fa;")
        self.txt_task_logs = QTextEdit()
        self.txt_task_logs.setReadOnly(True)
        self.txt_task_logs.setMinimumHeight(220)
        self.txt_task_logs.setStyleSheet("""
            QTextEdit {
                background-color: #090d16;
                border: 1px solid rgba(255, 255, 255, 0.12);
                border-radius: 8px;
                padding: 10px;
                color: #38bdf8;
                font-family: monospace;
                font-size: 13px;
            }
        """)
        self.btn_cancel_task = QPushButton("Passer / Arrêter")
        self.btn_cancel_task.setEnabled(False)
        self.btn_cancel_task.clicked.connect(self.cancel_active_task)
        
        tasks_layout.addWidget(self.lbl_active_task)
        tasks_layout.addWidget(self.txt_task_logs)
        tasks_layout.addWidget(self.btn_cancel_task)
        
        # Pending queue
        tasks_layout.addWidget(QLabel("📋 File d'attente (Planifié) :"))
        self.txt_pending_tasks = QTextEdit()
        self.txt_pending_tasks.setReadOnly(True)
        self.txt_pending_tasks.setMaximumHeight(120)
        tasks_layout.addWidget(self.txt_pending_tasks)
        
        # Completed history
        tasks_layout.addWidget(QLabel("✅ Historique des Tâches :"))
        self.txt_completed_tasks = QTextEdit()
        self.txt_completed_tasks.setReadOnly(True)
        tasks_layout.addWidget(self.txt_completed_tasks)
        
        self.tabs.addTab(tasks_tab, "⏳ Tâches")
        
        # Tab 5: Configuration
        config_tab = QWidget()
        config_layout = QVBoxLayout(config_tab)
        config_layout.setSpacing(12)
        
        # Voice Config Block
        voice_group = QFrame()
        voice_group.setObjectName("survieBlock")
        v_lay = QVBoxLayout(voice_group)
        v_lay.addWidget(QLabel("🗣️ PARAMÈTRES VOCAUX"))
        
        # Rate Slider
        sl_rate_lay = QHBoxLayout()
        sl_rate_lay.addWidget(QLabel("Vitesse :"))
        self.lbl_rate_val = QLabel("1.0x")
        sl_rate_lay.addStretch()
        sl_rate_lay.addWidget(self.lbl_rate_val)
        v_lay.addLayout(sl_rate_lay)
        
        self.slider_rate = QSlider(Qt.Orientation.Horizontal)
        self.slider_rate.setRange(6, 18)
        self.slider_rate.setValue(10)
        self.slider_rate.valueChanged.connect(self.update_voice_rate)
        v_lay.addWidget(self.slider_rate)
        
        # Pitch Slider
        sl_pitch_lay = QHBoxLayout()
        sl_pitch_lay.addWidget(QLabel("Tonalité (Pitch) :"))
        self.lbl_pitch_val = QLabel("1.0")
        sl_pitch_lay.addStretch()
        sl_pitch_lay.addWidget(self.lbl_pitch_val)
        v_lay.addLayout(sl_pitch_lay)
        
        self.slider_pitch = QSlider(Qt.Orientation.Horizontal)
        self.slider_pitch.setRange(6, 14)
        self.slider_pitch.setValue(10)
        self.slider_pitch.valueChanged.connect(self.update_voice_pitch)
        v_lay.addWidget(self.slider_pitch)
        
        # Dialogue Continu Checkbox
        self.chk_handsfree = QCheckBox("Dialogue Continu (Mains Libres)")
        self.chk_handsfree.setChecked(self.continuous_voice)
        self.chk_handsfree.stateChanged.connect(lambda state: self.set_handsfree(state == 2))
        v_lay.addWidget(self.chk_handsfree)
        
        config_layout.addWidget(voice_group)
        
        # LLM Settings Block
        llm_group = QFrame()
        llm_group.setObjectName("survieBlock")
        l_lay = QVBoxLayout(llm_group)
        l_lay.setSpacing(10)
        l_lay.addWidget(QLabel("🧠 PARAMÈTRES LLM"))
        
        # Main Provider
        prov_lay = QHBoxLayout()
        prov_lay.addWidget(QLabel("Cerveau Principal :"))
        self.cb_provider = QComboBox()
        self.cb_provider.addItems([
            "🖥️ Pixel Interne (llama-server)",
            "🖥️ Ollama (port 11434)",
            "🖥️ LM Studio (port 1234)",
            "☁️ Google Gemini",
            "☁️ OpenAI",
            "☁️ Groq",
            "☁️ Anthropic"
        ])
        prov_lay.addWidget(self.cb_provider)
        l_lay.addLayout(prov_lay)
        
        # API Key
        key_lay = QHBoxLayout()
        key_lay.addWidget(QLabel("Clé API Cloud :"))
        self.txt_api_key = QLineEdit()
        self.txt_api_key.setEchoMode(QLineEdit.EchoMode.Password)
        self.txt_api_key.setPlaceholderText("Clé API pour les providers cloud...")
        key_lay.addWidget(self.txt_api_key)
        l_lay.addLayout(key_lay)
        
        btn_save_config = QPushButton("Enregistrer la configuration LLM")
        btn_save_config.clicked.connect(self.save_llm_settings)
        l_lay.addWidget(btn_save_config)
        
        config_layout.addWidget(llm_group)
        
        # Gmail Settings Block
        gmail_group = QFrame()
        gmail_group.setObjectName("survieBlock")
        g_lay = QVBoxLayout(gmail_group)
        g_lay.setSpacing(10)
        g_lay.addWidget(QLabel("📬 PARAMÈTRES GMAIL"))
        
        self.chk_gmail_enabled = QCheckBox("Activer la surveillance des e-mails Gmail")
        g_lay.addWidget(self.chk_gmail_enabled)
        
        email_lay = QHBoxLayout()
        email_lay.addWidget(QLabel("Adresse E-mail :"))
        self.txt_gmail_email = QLineEdit()
        self.txt_gmail_email.setPlaceholderText("votre.adresse@gmail.com")
        email_lay.addWidget(self.txt_gmail_email)
        g_lay.addLayout(email_lay)
        
        pass_lay = QHBoxLayout()
        pass_lay.addWidget(QLabel("Mot de passe d'application :"))
        self.txt_gmail_password = QLineEdit()
        self.txt_gmail_password.setEchoMode(QLineEdit.EchoMode.Password)
        self.txt_gmail_password.setPlaceholderText("Mot de passe à 16 caractères...")
        pass_lay.addWidget(self.txt_gmail_password)
        g_lay.addLayout(pass_lay)
        
        interval_lay = QHBoxLayout()
        interval_lay.addWidget(QLabel("Intervalle de vérification (minutes) :"))
        self.sb_gmail_interval = QComboBox()
        self.sb_gmail_interval.addItems(["1", "2", "5", "10", "15", "30", "60"])
        self.sb_gmail_interval.setCurrentText("2")
        interval_lay.addWidget(self.sb_gmail_interval)
        g_lay.addLayout(interval_lay)
        
        btn_save_gmail = QPushButton("Enregistrer la configuration Gmail")
        btn_save_gmail.clicked.connect(self.save_gmail_settings)
        g_lay.addWidget(btn_save_gmail)
        
        config_layout.addWidget(gmail_group)
        
        config_layout.addStretch()
        self.tabs.addTab(config_tab, "⚙️ Configuration")
        
        # System Tray Icon Setup
        self.setup_tray_icon()
        
        # Set splitter sizes (sidebar 280px, tabs the rest)
        splitter.setSizes([280, 670])
        splitter.setCollapsible(0, False)
        splitter.setCollapsible(1, False)

    def setup_tray_icon(self):
        self.tray_icon = QSystemTrayIcon(self)
        # Use an available standard icon
        icon = QIcon.fromTheme("system-chat", QIcon.fromTheme("preferences-desktop-notification"))
        self.tray_icon.setIcon(icon)
        
        tray_menu = QMenu()
        show_action = QAction("Afficher Pixel", self)
        show_action.triggered.connect(self.showNormal)
        hide_action = QAction("Masquer Pixel", self)
        hide_action.triggered.connect(self.hide)
        exit_action = QAction("Quitter", self)
        exit_action.triggered.connect(self.close)
        
        tray_menu.addAction(show_action)
        tray_menu.addAction(hide_action)
        tray_menu.addSeparator()
        tray_menu.addAction(exit_action)
        
        self.tray_icon.setContextMenu(tray_menu)
        self.tray_icon.activated.connect(self.on_tray_icon_activated)
        self.tray_icon.show()

    def on_tray_icon_activated(self, reason):
        if reason == QSystemTrayIcon.ActivationReason.Trigger:
            if self.isVisible():
                self.hide()
            else:
                self.showNormal()
                self.raise_()
                self.activateWindow()

    def toggle_voice(self, checked):
        self.voice_enabled = checked
        if checked:
            self.btn_speaker.setText("🔊 Voix")
        else:
            self.btn_speaker.setText("🔇 Voix")
            self.stop_speaking()

    def set_handsfree(self, enabled):
        self.continuous_voice = enabled
        
        # Block signals to avoid infinite loops between button and checkbox
        if hasattr(self, "chk_handsfree"):
            self.chk_handsfree.blockSignals(True)
            self.chk_handsfree.setChecked(enabled)
            self.chk_handsfree.blockSignals(False)
            
        if hasattr(self, "btn_handsfree"):
            self.btn_handsfree.blockSignals(True)
            self.btn_handsfree.setChecked(enabled)
            if enabled:
                self.btn_handsfree.setText("🙌 Mains Libres [Actif]")
                self.btn_handsfree.setStyleSheet("background-color: rgba(124, 58, 237, 0.25); border: 1.5px solid #7c3aed; color: #c084fc; font-weight: bold;")
            else:
                self.btn_handsfree.setText("🙌 Mains Libres")
                self.btn_handsfree.setStyleSheet("")
            self.btn_handsfree.blockSignals(False)
            
        # Microphone state transition
        if enabled:
            # Only start recording if initialization is complete
            if not self.is_recording and not self.is_speaking_session and not self.is_speaking_sentence:
                if hasattr(self, "init_timer") and not self.init_timer.isActive():
                    self.start_microphone_recording()
        else:
            if self.is_recording:
                self.stop_microphone_recording()

    def update_voice_rate(self, val):
        self.voice_rate = val / 10.0
        self.lbl_rate_val.setText(f"{self.voice_rate:.1f}x")

    def update_voice_pitch(self, val):
        self.voice_pitch = val / 10.0
        self.lbl_pitch_val.setText(f"{self.voice_pitch:.1f}")

    def apply_qss(self):
        # Apply style matching web CSS
        qss = """
        QMainWindow {
            background-color: #0b0f19;
        }
        QWidget {
            color: #f3f4f6;
            font-family: 'Inter', 'Segoe UI', 'Outfit', sans-serif;
            font-size: 13px;
        }
        QFrame#sidebar {
            background-color: rgba(0, 0, 0, 0.25);
            border-right: 1px solid rgba(255, 255, 255, 0.08);
        }
        QFrame#survieBlock {
            background-color: rgba(0, 0, 0, 0.15);
            border: 1px solid rgba(255, 255, 255, 0.08);
            border-radius: 10px;
            padding: 12px;
        }
        QTextEdit#sysLogs {
            background-color: rgba(0, 0, 0, 0.4);
            border: 1px solid rgba(255, 255, 255, 0.08);
            border-radius: 8px;
            color: #a7f3d0;
            font-family: 'Courier New', monospace;
            font-size: 11px;
        }
        QTabWidget::pane {
            border: 1px solid rgba(255, 255, 255, 0.08);
            background-color: rgba(255, 255, 255, 0.01);
            border-radius: 12px;
        }
        QTabBar::tab {
            background-color: rgba(255, 255, 255, 0.03);
            border: 1px solid rgba(255, 255, 255, 0.06);
            padding: 10px 20px;
            border-top-left-radius: 8px;
            border-top-right-radius: 8px;
            margin-right: 4px;
            color: #9ca3af;
            font-weight: bold;
        }
        QTabBar::tab:selected {
            background-color: #7c3aed;
            color: white;
            border: 1px solid #7c3aed;
        }
        QTabBar::tab:hover {
            background-color: rgba(124, 58, 237, 0.2);
            color: white;
        }
        QPushButton {
            background-color: rgba(124, 58, 237, 0.2);
            border: 1px solid #7c3aed;
            border-radius: 8px;
            color: white;
            padding: 8px 16px;
            font-weight: bold;
        }
        QPushButton:hover {
            background-color: rgba(124, 58, 237, 0.4);
        }
        QPushButton:pressed {
            background-color: rgba(124, 58, 237, 0.6);
        }
        QLineEdit, QComboBox, QPlainTextEdit, QTextBrowser, QTextEdit {
            background-color: rgba(0, 0, 0, 0.35);
            border: 1px solid rgba(255, 255, 255, 0.08);
            border-radius: 8px;
            padding: 8px;
            color: #f3f4f6;
        }
        QLineEdit:focus, QComboBox:focus, QPlainTextEdit:focus, QTextBrowser:focus, QTextEdit:focus {
            border: 1px solid #7c3aed;
        }
        QScrollBar:vertical {
            border: none;
            background: transparent;
            width: 8px;
            margin: 0px;
        }
        QScrollBar::handle:vertical {
            background: rgba(255, 255, 255, 0.12);
            min-height: 20px;
            border-radius: 4px;
        }
        QScrollBar::handle:vertical:hover {
            background: #7c3aed;
        }
        QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
            border: none;
            background: none;
        }
        """
        self.setStyleSheet(qss)

    # ── POLLING DATA FROM SERVER ──
    def poll_server_data(self):
        self.fetch_system_status()
        self.fetch_core_memory()
        self.fetch_ltm_learnings()
        self.fetch_active_project()

    def fetch_system_status(self):
        try:
            r = requests.get(f"{API_BASE}/api/system_status", timeout=2)
            if r.status_code == 200:
                data = r.json()
                
                # Render internet
                if data.get("internet_active"):
                    self.lbl_internet.setText("Internet: Actif")
                    self.lbl_internet.setStyleSheet("color: #10b981; font-weight: bold;")
                else:
                    self.lbl_internet.setText("Internet: Coupé")
                    self.lbl_internet.setStyleSheet("color: #ef4444; font-weight: bold;")
                    
                # Render RAM
                ram = data.get("ram_used_percent", 0.0)
                self.lbl_ram.setText(f"RAM: {ram:.1f} %")
                
                # Render CPU
                cpu = data.get("load_average_1", 0.0)
                cpus = data.get("num_cpus", 1)
                self.lbl_cpu.setText(f"CPU (1m): {cpu:.2f} / {cpus}")
                
                # Set Led color according to health
                health = data.get("health_status", "Excellent")
                if health == "Excellent":
                    self.status_led.setStyleSheet("background-color: #10b981; border-radius: 5px;")
                elif health == "Warning":
                    self.status_led.setStyleSheet("background-color: #f59e0b; border-radius: 5px;")
                else:
                    self.status_led.setStyleSheet("background-color: #ef4444; border-radius: 5px;")
                    
                # Render logs
                logs = data.get("logs", "")
                self.log_console.setPlainText(logs)
                # Auto-scroll logs to bottom
                self.log_console.moveCursor(QTextCursor.MoveOperation.End)
        except Exception as e:
            print(f"[GUI] Erreur fetch system status : {e}")

    def fetch_core_memory(self):
        try:
            r = requests.get(f"{API_BASE}/api/core_memory", timeout=2)
            if r.status_code == 200:
                data = r.json()
                self.txt_persona.setPlainText(data.get("agent_persona", ""))
                
                # User profile formatting
                profile = data.get("user_profile", {})
                static = profile.get("static", {})
                self.user_name = static.get('name', 'Marcelo')
                html = f"<strong>Nom:</strong> {self.user_name}<br>"
                html += f"<strong>Rôle:</strong> {static.get('role', 'Utilisateur')}"
                
                volatile = profile.get("volatile", {})
                if volatile:
                    html += "<br><br><strong>État Actuel:</strong><br>"
                    for k, v in volatile.items():
                        if k == "Dernière vision":
                            self.txt_vision.setPlainText(v)
                            continue
                        html += f"&bull; <em>{k}</em>: {v}<br>"
                
                dynamic_goals = data.get("dynamic_goals", [])
                if dynamic_goals:
                    active_goals = [g for g in dynamic_goals if g.get("status") == "active"]
                    if active_goals:
                        html += "<br><br><strong>🎯 Objectifs cognitifs :</strong><br>"
                        for goal in active_goals:
                            desc = goal.get("description", "")
                            priority = goal.get("priority", 0.0)
                            html += f"&bull; {desc} <em>(Priorité: {priority:.1f})</em><br>"

                self.txt_profile.setHtml(html)
        except Exception as e:
            print(f"[GUI] Erreur fetch core memory : {e}")

    def fetch_ltm_learnings(self):
        try:
            r = requests.get(f"{API_BASE}/api/ltm_learnings", timeout=2)
            if r.status_code == 200:
                memories = r.json()
                if not memories:
                    self.txt_learnings.setHtml("<span style='color:rgba(255,255,255,0.4);'>Aucun apprentissage enregistré.</span>")
                    return
                html = ""
                for m in memories:
                    # Parse timestamp
                    date_str = m.get("timestamp", "")[:16].replace("T", " ")
                    cat = m.get("category", "Technical")
                    badge_color = "#7c3aed"
                    if cat == "Project": badge_color = "#3b82f6"
                    elif cat == "Personal": badge_color = "#ec4899"
                    elif cat == "Decision": badge_color = "#ef4444"
                    
                    html += f"""
                    <div style='margin-bottom: 12px; border-bottom: 1px solid rgba(255,255,255,0.05); padding-bottom: 8px;'>
                        <div style='display: flex; justify-content: space-between;'>
                            <span style='color: rgba(255,255,255,0.4); font-size: 0.85em;'>{date_str}</span>
                            <span style='background: {badge_color}; color: white; padding: 1px 6px; border-radius: 4px; font-weight: 600; font-size: 0.8em;'>{cat}</span>
                        </div>
                        <strong style='color: #60a5fa; display: block; font-size: 0.95em; margin-top: 4px;'>{m.get('title', '')}</strong>
                        <span style='color: rgba(255,255,255,0.85); display:block; margin-top:2px;'>{m.get('action_summary', '')}</span>
                    </div>
                    """
                self.txt_learnings.setHtml(html)
        except Exception as e:
            print(f"[GUI] Erreur fetch learnings : {e}")

    def fetch_active_project(self):
        try:
            r = requests.get(f"{API_BASE}/api/project", timeout=2)
            if r.status_code == 404:
                self.lbl_proj_name.setText("Aucun projet actif")
                self.lbl_proj_goal.setText("")
                self.lbl_proj_step.setText("")
                self.txt_proj_milestones.setPlainText("")
                return
            if r.status_code == 200:
                proj = r.json()
                if not proj or proj.get("status") != "active":
                    self.lbl_proj_name.setText("Aucun projet actif")
                    return
                self.lbl_proj_name.setText(f"Projet: {proj.get('title', '--')}")
                self.lbl_proj_goal.setText(f"Objectif: {proj.get('goal', '--')}")
                
                current_step_desc = "--"
                steps_html = ""
                for s in proj.get("steps", []):
                    symbol = "⬜"
                    style = "color: rgba(255,255,255,0.6);"
                    if s.get("status") == "completed":
                        symbol = "✅"
                        style = "text-decoration: line-through; color: rgba(255,255,255,0.4);"
                    elif s.get("id") == proj.get("current_step_id") or s.get("status") == "active":
                        symbol = "🎯"
                        style = "color: #f59e0b; font-weight: bold;"
                        current_step_desc = s.get("description", "")
                        
                    steps_html += f"<div style='margin-bottom: 4px; {style}'>{symbol} {s.get('description', '')}</div>"
                    
                self.lbl_proj_step.setText(f"Étape Actuelle: {current_step_desc}")
                self.txt_proj_milestones.setHtml(steps_html)
        except Exception as e:
            print(f"[GUI] Erreur fetch project : {e}")

    def fetch_tasks(self):
        try:
            r = requests.get(f"{API_BASE}/api/tasks", timeout=1)
            if r.status_code == 200:
                tasks = r.json()
                
                active_html = "Aucune tâche en cours d'exécution."
                pending_html = "File d'attente vide."
                completed_html = "Aucun historique disponible."
                
                self.btn_cancel_task.setEnabled(False)
                self.active_task_id = None
                
                running_list = []
                pending_list = []
                completed_list = []
                
                for t in tasks:
                    status = t.get("status")
                    if status == "running":
                        running_list.append(t)
                    elif status == "pending":
                        pending_list.append(t)
                    else:
                        completed_list.append(t)
                        
                if running_list:
                    self.btn_cancel_task.setEnabled(True)
                    self.active_task_id = running_list[0].get("id")
                    active_html = ""
                    active_logs = ""
                    for r_task in running_list:
                        active_html += f"⚡ <strong>{r_task.get('name')}</strong> <i>(ID: {r_task.get('id')[:8]})</i><br>"
                        active_logs += f"=== Tâche : {r_task.get('name')} ===\n{r_task.get('log', 'Démarrage...')}\n\n"
                    self.lbl_active_task.setText(active_html)
                    self.txt_task_logs.setPlainText(active_logs.strip())
                    self.txt_task_logs.moveCursor(QTextCursor.MoveOperation.End)
                else:
                    self.btn_cancel_task.setEnabled(False)
                    self.active_task_id = None
                    self.lbl_active_task.setText("Aucune tâche en cours d'exécution.")
                    self.txt_task_logs.setPlainText("")
                    
                if pending_list:
                    p_text = ""
                    for p in pending_list:
                        p_text += f"• {p.get('name')} (Planifié)\n"
                    self.txt_pending_tasks.setPlainText(p_text)
                else:
                    self.txt_pending_tasks.setPlainText(pending_html)
                    
                if completed_list:
                    c_html = ""
                    # Show last 5 completed tasks reversed
                    for c in reversed(completed_list[-5:]):
                        status_color = "#10b981"
                        status_label = "Fini"
                        if c.get("status") == "failed":
                            status_color = "#ef4444"
                            status_label = f"Échoué: {c.get('error', '')}"
                        elif c.get("status") == "cancelled":
                            status_color = "#6b7280"
                            status_label = "Annulé"
                            
                        c_html += f"<div style='margin-bottom: 5px;'><strong>{c.get('name')}</strong> - <span style='color:{status_color};'>{status_label}</span></div>"
                    self.txt_completed_tasks.setHtml(c_html)
                else:
                    self.txt_completed_tasks.setHtml(completed_html)
        except Exception as e:
            print(f"[GUI] Erreur fetch tasks : {e}")

    def fetch_chat_history(self):
        try:
            r = requests.get(f"{API_BASE}/api/history", timeout=2)
            if r.status_code == 200:
                messages = r.json()
                self.chat_history_cache = []
                self.chat_display.clear()
                if not messages:
                    return
                # Render initial chat
                for msg in messages:
                    sender = "pixel" if msg.get("role") == "assistant" else "user"
                    self.append_chat_bubble(msg.get("content", ""), sender)
        except Exception as e:
            print(f"[GUI] Erreur fetch history : {e}")

    # ── ACTIONS ──
    def trigger_force_sleep(self):
        self.btn_sleep.setEnabled(False)
        self.btn_sleep.setText("Consolidation en cours...")
        
        self.sleep_thread = SleepThread()
        self.sleep_thread.finished_signal.connect(self.handle_sleep_finished, Qt.ConnectionType.QueuedConnection)
        self.sleep_thread.start()
        
    def handle_sleep_finished(self):
        self.poll_server_data()
        self.btn_sleep.setEnabled(True)
        self.btn_sleep.setText("Forcer Sommeil (Sauvegarder)")

    def clear_tasks_history(self):
        try:
            requests.post(f"{API_BASE}/api/tasks/clear", timeout=2)
            self.fetch_tasks()
        except Exception as e:
            print(f"[GUI] Erreur clear tasks : {e}")

    def cancel_active_task(self):
        if hasattr(self, "active_task_id") and self.active_task_id:
            try:
                requests.post(f"{API_BASE}/api/tasks/cancel", json={"id": self.active_task_id}, timeout=2)
                self.fetch_tasks()
            except Exception as e:
                print(f"[GUI] Erreur cancel task : {e}")

    def save_llm_settings(self):
        prov = self.cb_provider.currentText()
        # Mapping to match API fields
        mapping = {
            "🖥️ Pixel Interne (llama-server)": "local-pixel",
            "🖥️ Ollama (port 11434)": "local-ollama",
            "🖥️ LM Studio (port 1234)": "local-lm",
            "☁️ Google Gemini": "gemini",
            "☁️ OpenAI": "openai",
            "☁️ Groq": "groq",
            "☁️ Anthropic": "anthropic"
        }
        prov_id = mapping.get(prov, "local-pixel")
        is_cloud = prov_id in ["gemini", "openai", "groq", "anthropic"]
        active_mode = "cloud" if is_cloud else "local"
        
        # Simple configurations depending on providers
        local_url = "http://127.0.0.1:52625/v1"
        if prov_id == "local-ollama":
            local_url = "http://127.0.0.1:11434/v1"
        elif prov_id == "local-lm":
            local_url = "http://127.0.0.1:1234/v1"
            
        payload = {
            "active_mode": active_mode,
            "cloud_api_key": self.txt_api_key.text().strip(),
            "cloud_base_url": "https://generativelanguage.googleapis.com/v1beta/openai" if prov_id == "gemini" else "https://api.openai.com/v1",
            "cloud_model": "gemini-2.5-flash" if prov_id == "gemini" else "gpt-4o",
            "local_base_url": local_url,
            "local_model": "qwen3vl-it:4b" if prov_id == "local-pixel" else "",
            "code_base_url": local_url,
            "code_model": ""
        }
        
        try:
            r = requests.post(f"{API_BASE}/api/llm_settings", json=payload, timeout=5)
            if r.status_code == 200:
                QMessageBox.information(self, "Succès", "Configuration LLM enregistrée !")
                self.poll_server_data()
            else:
                QMessageBox.warning(self, "Erreur", f"Erreur de sauvegarde : {r.text}")
        except Exception as e:
            QMessageBox.critical(self, "Erreur Réseau", str(e))

    def fetch_gmail_settings(self):
        try:
            r = requests.get(f"{API_BASE}/api/gmail_settings", timeout=2)
            if r.status_code == 200:
                data = r.json()
                self.chk_gmail_enabled.setChecked(data.get("enabled", False))
                self.txt_gmail_email.setText(data.get("email", ""))
                self.txt_gmail_password.setText(data.get("app_password", ""))
                self.sb_gmail_interval.setCurrentText(str(data.get("check_interval_mins", 2)))
        except Exception as e:
            print(f"[GUI] Erreur fetch gmail settings : {e}")

    def save_gmail_settings(self):
        try:
            interval = int(self.sb_gmail_interval.currentText())
        except ValueError:
            interval = 2
            
        payload = {
            "enabled": self.chk_gmail_enabled.isChecked(),
            "email": self.txt_gmail_email.text().strip(),
            "app_password": self.txt_gmail_password.text().strip(),
            "check_interval_mins": interval
        }
        try:
            r = requests.post(f"{API_BASE}/api/gmail_settings", json=payload, timeout=5)
            if r.status_code == 200:
                QMessageBox.information(self, "Succès", "Configuration Gmail enregistrée !")
            else:
                QMessageBox.warning(self, "Erreur", f"Erreur de sauvegarde : {r.text}")
        except Exception as e:
            QMessageBox.critical(self, "Erreur Réseau", str(e))

    # ── DISCUSSION (CHAT) HANDLING ──
    def send_chat_message(self):
        msg = self.input_field.text().strip()
        if not msg:
            return
            
        # Cancel any active TTS playback
        self.stop_speaking()
        
        self.chat_failed = False
        self.input_field.clear()
        self.append_chat_bubble(msg, "user")
        
        # Prepare bubble for streaming response
        self.stream_accumulated = ""
        self.chat_display.append(
            f'<table width="100%" border="0" cellspacing="0" cellpadding="0" style="margin-bottom: 14px; margin-top: 6px;">'
            f'<tr><td align="left">'
            f'<table border="0" cellspacing="0" cellpadding="0" style="background-color: #1e293b; border: 1px solid #0284c7; border-left: 4px solid #38bdf8; border-radius: 16px 16px 16px 4px;">'
            f'<tr><td style="padding: 12px 18px;">'
            f'<div style="color: #38bdf8; font-weight: 700; font-size: 0.8em; margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px;">✨ Pixel</div>'
            f'<div style="color: #94a3b8; font-size: 14px;"><i>Pixel réfléchit...</i></div>'
            f'</td></tr></table>'
            f'</td></tr></table>'
        )
        self.chat_display.moveCursor(QTextCursor.MoveOperation.End)
        
        # Start TTS stream session
        self.start_speech_session()
        
        # Disable input while responding
        self.input_field.setEnabled(False)
        self.btn_send.setEnabled(False)
        
        # Start streaming thread
        self.chat_thread = ChatStreamThread(msg, getattr(self, 'user_name', 'Marcelo'))
        self.chat_thread.token_received.connect(self.handle_stream_chunk, Qt.ConnectionType.QueuedConnection)
        self.chat_thread.error_received.connect(self.handle_stream_error, Qt.ConnectionType.QueuedConnection)
        self.chat_thread.finished_stream.connect(self.handle_stream_finished, Qt.ConnectionType.QueuedConnection)
        self.chat_thread.start()

    def handle_stream_chunk(self, chunk):
        self.stream_accumulated += chunk
        
        # Update speaking queue chunk
        self.feed_chunk(chunk)
        
        # Format HTML using regex parser
        formatted_html = format_response_html(self.stream_accumulated)
        
        # Replace the temporary stream bubble content
        self.refresh_chat_display_with_active_stream(formatted_html)

    def handle_stream_error(self, err):
        self.chat_failed = True
        self.chat_display.append(
            f'<table width="100%" border="0" cellspacing="0" cellpadding="0" style="margin-bottom: 14px; margin-top: 6px;">'
            f'<tr><td align="left">'
            f'<table border="0" cellspacing="0" cellpadding="0" style="background-color: #450a0a; border: 1px solid #ef4444; border-left: 4px solid #f87171; border-radius: 16px 16px 16px 4px;">'
            f'<tr><td style="padding: 12px 18px;">'
            f'<div style="color: #f87171; font-weight: 700; font-size: 0.8em; margin-bottom: 6px; text-transform: uppercase;">⚠️ Erreur Système</div>'
            f'<div style="color: #fca5a5; font-size: 14px;">{err}</div>'
            f'</td></tr></table>'
            f'</td></tr></table>'
        )

    def handle_stream_finished(self):
        self.finalize_speech()
        self.input_field.setEnabled(True)
        self.btn_send.setEnabled(True)
        self.input_field.setFocus()
        
        # Save streamed response into chat history cache
        if hasattr(self, "stream_accumulated") and self.stream_accumulated:
            if not hasattr(self, "chat_history_cache"):
                self.chat_history_cache = []
            self.chat_history_cache.append((self.stream_accumulated, "pixel"))
            
        self.poll_server_data()

    def refresh_chat_display_with_active_stream(self, active_html):
        if not hasattr(self, "chat_history_cache"):
            self.chat_history_cache = []
            
        html = ""
        for msg, sender in self.chat_history_cache:
            html += self.build_bubble_html(msg, sender)
            
        # Active stream bubble
        html += f"""
        <table width="100%" border="0" cellspacing="0" cellpadding="0" style="margin-bottom: 14px; margin-top: 6px;">
          <tr>
            <td align="left">
              <table border="0" cellspacing="0" cellpadding="0" style="background-color: #1e293b; border: 1px solid #0284c7; border-left: 4px solid #38bdf8; border-radius: 16px 16px 16px 4px;">
                <tr>
                  <td style="padding: 12px 18px;">
                    <div style="color: #38bdf8; font-weight: 700; font-size: 0.8em; margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px;">✨ Pixel <i style="color: #94a3b8; font-weight: normal; font-size: 0.9em;">(Réponse en cours...)</i></div>
                    <div style="color: #f8fafc; font-size: 14px; line-height: 1.5;">{active_html}</div>
                  </td>
                </tr>
              </table>
            </td>
          </tr>
        </table>
        """
        self.chat_display.setHtml(html)
        self.chat_display.moveCursor(QTextCursor.MoveOperation.End)

    def append_chat_bubble(self, content, sender):
        if not hasattr(self, "chat_history_cache"):
            self.chat_history_cache = []
            
        self.chat_history_cache.append((content, sender))
        html_bubble = self.build_bubble_html(content, sender)
        self.chat_display.append(html_bubble)
        self.chat_display.moveCursor(QTextCursor.MoveOperation.End)

    def build_bubble_html(self, content, sender):
        user_name = getattr(self, 'user_name', 'Marcelo')
        if sender == "user":
            formatted = parse_markdown(content)
            return f"""
            <table width="100%" border="0" cellspacing="0" cellpadding="0" style="margin-bottom: 12px; margin-top: 6px;">
              <tr>
                <td align="right">
                  <table border="0" cellspacing="0" cellpadding="0" style="background-color: #5b21b6; border: 1px solid #8b5cf6; border-radius: 16px 16px 4px 16px;">
                    <tr>
                      <td style="padding: 10px 16px;">
                        <div style="color: #ddd6fe; font-weight: 700; font-size: 0.78em; margin-bottom: 4px; text-align: right; text-transform: uppercase; letter-spacing: 0.5px;">👤 Vous ({user_name})</div>
                        <div style="color: #ffffff; font-size: 14px; line-height: 1.5;">{formatted}</div>
                      </td>
                    </tr>
                  </table>
                </td>
              </tr>
            </table>
            """
        else:
            formatted = format_response_html(content)
            return f"""
            <table width="100%" border="0" cellspacing="0" cellpadding="0" style="margin-bottom: 14px; margin-top: 6px;">
              <tr>
                <td align="left">
                  <table border="0" cellspacing="0" cellpadding="0" style="background-color: #1e293b; border: 1px solid rgba(255, 255, 255, 0.1); border-left: 4px solid #10b981; border-radius: 16px 16px 16px 4px;">
                    <tr>
                      <td style="padding: 12px 18px;">
                        <div style="color: #34d399; font-weight: 700; font-size: 0.8em; margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px;">✨ Pixel</div>
                        <div style="color: #f8fafc; font-size: 14px; line-height: 1.5;">{formatted}</div>
                      </td>
                    </tr>
                  </table>
                </td>
              </tr>
            </table>
            """

    def handle_proactive_message(self, message):
        if message.startswith("VISION:"):
            desc = message[7:]
            self.txt_vision.setPlainText(desc)
        else:
            self.append_chat_bubble(message, "pixel")
            if self.voice_enabled:
                self.start_speech_session()
                self.feed_chunk(message)
                self.finalize_speech()
                
            # Show system tray notification
            self.tray_icon.showMessage(
                "Pixel",
                message,
                QSystemTrayIcon.MessageIcon.Information,
                5000
            )

    # ── MICROPHONE / AUDIO RECORDING (STT) ──
    def toggle_microphone(self):
        if self.is_recording:
            self.stop_microphone_recording()
        else:
            self.start_microphone_recording()

    def start_microphone_recording(self):
        self.stop_speaking()
        
        self.is_recording = True
        self.btn_mic.setText("🔴")
        self.btn_mic.setStyleSheet("background: rgba(239, 68, 68, 0.2); border: 2px solid #ef4444;")
        self.input_field.setPlaceholderText("Pixel vous écoute... Cliquez sur 🔴 ou parlez.")
        
        # Start QThread-based recording with real-time silence detection
        self.record_thread = RecordReaderThread(is_handsfree=self.continuous_voice)
        self.record_thread.finished_signal.connect(self.handle_record_finished, Qt.ConnectionType.QueuedConnection)
        self.record_thread.speech_started_signal.connect(self.stop_speaking, Qt.ConnectionType.QueuedConnection)
        self.record_thread.start()

    def stop_microphone_recording(self):
        if not self.is_recording:
            return
            
        self.is_recording = False
        self.btn_mic.setText("🎙️")
        self.btn_mic.setStyleSheet("")
        self.input_field.setPlaceholderText("Écrivez un message ou parlez...")
        
        if hasattr(self, "record_thread") and self.record_thread:
            self.record_thread.stop()

    def handle_record_finished(self, wav_path):
        self.is_recording = False
        self.btn_mic.setText("🎙️")
        self.btn_mic.setStyleSheet("")
        self.input_field.setPlaceholderText("Écrivez un message ou parlez...")
        
        if wav_path and os.path.exists(wav_path):
            self.send_wav_for_transcription(wav_path)
        else:
            # Re-enable input if recording failed / empty
            self.input_field.setEnabled(True)
            if self.continuous_voice:
                QTimer.singleShot(600, self.start_microphone_recording)

    def send_wav_for_transcription(self, wav_path):
        self.input_field.setText("... (Pixel décode votre voix) ...")
        self.input_field.setEnabled(False)
        
        self.transcribe_thread = TranscribeThread(wav_path)
        self.transcribe_thread.finished_signal.connect(self.handle_transcription_finished, Qt.ConnectionType.QueuedConnection)
        self.transcribe_thread.start()
        
    def handle_transcription_finished(self, text):
        self.input_field.setEnabled(True)
        if text:
            # Check for trailing keywords like "envoi", "envoyer", "envoie"
            has_send_keyword = text.lower().rstrip('.,!?* ').endswith(('envoi', 'envoyer', 'envoie'))
            
            if self.continuous_voice:
                # Si la phrase commence par "Pixel", on retire le mot-clé pour garder uniquement la question
                match = re.match(r'^pixel([\s,:\-\.\!\?]+|$)(.*)', text, re.IGNORECASE)
                if match and match.group(2).strip():
                    cleaned_text = match.group(2).strip()
                else:
                    cleaned_text = text.strip()
                
                # Nettoyer les mots-clés d'envoi éventuels en fin de phrase
                cleaned_text = re.sub(r'[\s,:\-\.\!\?]*(envoi|envoyer|envoie)[\s,:\-\.\!\?]*$', '', cleaned_text, flags=re.IGNORECASE).strip()
                
                if cleaned_text and cleaned_text.lower() != "pixel":
                    self.input_field.setText(cleaned_text)
                    self.send_chat_message()
                else:
                    # Si seul "Pixel" a été dit, inviter à parler
                    self.input_field.setText("")
                    self.input_field.setPlaceholderText("Pixel : Oui ? Je vous écoute...")
                    if not self.is_recording:
                        QTimer.singleShot(600, self.start_microphone_recording)
            else:
                # Manual microphone activation: strip trailing send keyword if present
                cleaned_text = re.sub(r'[\s,:\-\.\!\?]*(envoi|envoyer|envoie)[\s,:\-\.\!\?]*$', '', text, flags=re.IGNORECASE).strip()
                self.input_field.setText(cleaned_text)
                if has_send_keyword:
                    self.send_chat_message()
        else:
            self.input_field.setText("")
            self.input_field.setPlaceholderText("Aucune voix détectée. Réessayez.")
            if self.continuous_voice and not self.is_recording:
                QTimer.singleShot(600, self.start_microphone_recording)

    # ── TTS ENGINE (PIPER/GO BACKEND) ──
    def start_speech_session(self):
        self.stop_speaking()
        self.is_speaking_session = True
        self.speech_queue = []
        self.speech_buffer = ""
        self.is_speaking_sentence = False
        self.is_stream_finished = False
        self.tts_thoughts_stopped = False

    def feed_chunk(self, chunk):
        if not self.voice_enabled or getattr(self, "tts_thoughts_stopped", False):
            return
        self.speech_buffer += chunk
        
        # Check if we hit the thought markers in the accumulated buffer
        thought_markers = [
            "**Pensée interne de Pixel :**",
            "**Pensée interne de Pixel:**",
            "Pensée interne de Pixel :",
            "Pensée interne de Pixel:",
            "**Pensée interne :**",
            "**Pensée interne:**",
            "Pensée interne :",
            "Pensée interne:"
        ]
        for marker in thought_markers:
            idx = self.speech_buffer.find(marker)
            if idx != -1:
                # Keep only text before the marker, and flag to stop further chunks
                self.speech_buffer = self.speech_buffer[:idx]
                self.tts_thoughts_stopped = True
                break
        
        while True:
            boundary = -1
            for i, c in enumerate(self.speech_buffer):
                if c in ['.', '?', '!', '\n']:
                    # Exclude floats (e.g. 1.0)
                    if c == '.' and i + 1 < len(self.speech_buffer) and self.speech_buffer[i+1].isdigit():
                        continue
                    # Exclude repeated punctuation (e.g. ..., !!!, ???) by waiting for the last one
                    if i + 1 < len(self.speech_buffer) and self.speech_buffer[i+1] == c:
                        continue
                    # Exclude mixed trailing punctuation (e.g. ?!, !?) by waiting for the last one
                    if i + 1 < len(self.speech_buffer) and self.speech_buffer[i+1] in ['.', '?', '!', '\n']:
                        continue
                    boundary = i
                    break
                    
            if boundary != -1:
                sentence = self.speech_buffer[:boundary + 1]
                self.speech_buffer = self.speech_buffer[boundary + 1:]
                
                # Check for incomplete <thought> blocks
                if "<thought>" in sentence and "</thought>" not in sentence:
                    # Put it back and wait
                    self.speech_buffer = sentence + self.speech_buffer
                    break
                    
                # Clean HTML/XML and markdown
                sentence = re.sub(r'<thought>.*?</thought>', '', sentence, flags=re.DOTALL)
                sentence = re.sub(r'<[^>]+>', '', sentence)
                sentence = re.sub(r'\*\*|\*|`|###|##|#', '', sentence).strip()
                
                # Verify that it has at least one alphanumeric character
                if len(sentence) > 1 and any(char.isalnum() for char in sentence):
                    self.speech_queue.append(sentence)
                    self._play_next_tts_sentence()
            else:
                break
                
        if getattr(self, "tts_thoughts_stopped", False):
            # Immediately process anything left in the buffer and stop
            self.finalize_speech()

    def finalize_speech(self):
        self.is_stream_finished = True
        if not self.voice_enabled:
            self.is_speaking_session = False
            # Hands-free: start recording if continuous voice is enabled, mic not recording, and chat didn't fail
            if self.continuous_voice and not self.is_recording and not self.chat_failed:
                QTimer.singleShot(1000, self.start_microphone_recording)
            return
            
        remaining = self.speech_buffer.strip()
        # Clean HTML/XML and markdown
        remaining = re.sub(r'<thought>.*?</thought>', '', remaining, flags=re.DOTALL)
        remaining = re.sub(r'<[^>]+>', '', remaining)
        remaining = re.sub(r'\*\*|\*|`|###|##|#', '', remaining).strip()
        
        if len(remaining) > 1 and any(char.isalnum() for char in remaining):
            self.speech_queue.append(remaining)
        self.speech_buffer = ""
        self._play_next_tts_sentence()

    def _play_next_tts_sentence(self):
        if not self.voice_enabled or self.is_speaking_sentence:
            return
            
        if not self.speech_queue:
            if self.is_stream_finished and not self.is_speaking_sentence:
                self.is_speaking_session = False
                if self.continuous_voice and not self.is_recording and not self.chat_failed:
                    QTimer.singleShot(650, self.start_microphone_recording)
            return
            
        self.is_speaking_sentence = True
        sentence = self.speech_queue.pop(0)
        
        # Get speed and pitch settings from UI
        rate_val = self.sld_rate.value() / 100.0 if hasattr(self, 'sld_rate') else 1.0
        pitch_val = self.sld_pitch.value() / 100.0 if hasattr(self, 'sld_pitch') else 1.0
        
        # Download synthesis in background QThread to avoid player buffering glitched starts
        self.tts_thread = TTSDownloadThread(sentence, rate_val, pitch_val)
        self.tts_thread.finished_signal.connect(self.handle_tts_download_finished, Qt.ConnectionType.QueuedConnection)
        self.tts_thread.start()

    def handle_tts_download_finished(self, local_path):
        if local_path and os.path.exists(local_path):
            self.current_tts_file = local_path
            self.media_player.setSource(QUrl.fromLocalFile(local_path))
            self.media_player.play()
        else:
            self.is_speaking_sentence = False
            QTimer.singleShot(80, self._play_next_tts_sentence)

    def _on_media_status_changed(self, status):
        # Detect end of audio file
        if status == QMediaPlayer.MediaStatus.EndOfMedia:
            self.is_speaking_sentence = False
            
            # Clean up the localized wav file
            if hasattr(self, "current_tts_file") and self.current_tts_file:
                try: os.unlink(self.current_tts_file)
                except Exception: pass
                self.current_tts_file = None
                
            QTimer.singleShot(80, self._play_next_tts_sentence)

    def stop_speaking(self):
        self.media_player.stop()
        if hasattr(self, "current_tts_file") and self.current_tts_file:
            try: os.unlink(self.current_tts_file)
            except Exception: pass
            self.current_tts_file = None
        self.speech_queue = []
        self.speech_buffer = ""
        self.is_speaking_sentence = False
        self.is_speaking_session = False

    # ── CLEAN EXIT ──
    def closeEvent(self, event):
        # Terminate backend process on window close
        self.stop_speaking()
        if self.sse_thread:
            self.sse_thread.running = False
            self.sse_thread.wait()
            
        if self.server_process:
            print("[GUI] Arrêt du serveur backend Pixel...")
            self.server_process.terminate()
            self.server_process.wait()
        event.accept()

if __name__ == "__main__":
    app = QApplication(sys.argv)
    window = MainWindow()
    window.show()
    sys.exit(app.exec())
