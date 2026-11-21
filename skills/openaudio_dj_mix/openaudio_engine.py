#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
OpenAudio Pro DJ Engine
-----------------------
Open-source audio analysis & beatmatched DJ mixing engine combining:
- librosa (beat tracking, onset/phrase detection, CQT chroma harmonic key)
- Mixxx SQLite database (~/.mixxx/mixxxdb.sqlite) fast lookup + persistent JSON cache
- FFmpeg + Rubberband (Master Tempo time-stretching without pitch shift)
- SciPy + NumPy (sample-accurate beat-aligned Bass-Swap EQ & equal-power crossfades)
"""

from __future__ import annotations

import concurrent.futures
import json
import math
import os
import random
import re
import sqlite3
import subprocess
import unicodedata
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Dict, List, Optional, Tuple

import numpy as np
import soundfile as sf
from scipy.signal import butter, sosfilt

AUDIO_EXTENSIONS = {
    ".mp3", ".m4a", ".wav", ".flac", ".ogg", ".opus", ".aac", ".wma", ".alac", ".aiff", ".mka"
}
BASE_MUSIC_DIR = os.path.expanduser("~/Musique")
MIXXX_DB_PATH = os.path.expanduser("~/.mixxx/mixxxdb.sqlite")
CACHE_DIR = os.path.expanduser("~/.cache/pixel_openaudio_dj")
ANALYSIS_CACHE_FILE = os.path.join(CACHE_DIR, "analysis_cache.json")

# Folders in ~/Musique that contain spoken jingles / non-musical announcements rather than songs
EXCLUDED_RANDOM_FOLDERS = {
    "annonces-radio-concierto",
    "cuñas y frases radiales pack concierto discotheque (g.s)  por dj kapo",
    "cunas y frases radiales pack concierto discotheque (g.s)  por dj kapo",
    "karakoe",
    "marcas",
    "mixxx",
    "pixel",
}

MARCAS_DIR = os.path.join(BASE_MUSIC_DIR, "Marcas")
MARCAS_SINGELS_DIR = os.path.join(MARCAS_DIR, "singels")
APERTURA_FILENAMES = (
    "Apertura-Concierto-Discoteque.mp3",
    "Apertura-Concierto-Discotheque.mp3",
)

TARGET_SR = 44100
MIN_TRACK_DURATION_SEC = 55.0
MAX_TRACK_DURATION_SEC = 900.0  # Exclude 40-70min pre-mixed megamixes from single-track DJ mixing

# Standard musical key -> Camelot wheel notation
KEY_TO_CAMELOT: Dict[str, str] = {
    "Abm": "1A", "G#m": "1A", "B": "1B",
    "Ebm": "2A", "D#m": "2A", "F#": "2B", "Gb": "2B",
    "Bbm": "3A", "A#m": "3A", "Db": "3B", "C#": "3B",
    "Fm": "4A", "Ab": "4B", "G#": "4B",
    "Cm": "5A", "Eb": "5B", "D#": "5B",
    "Gm": "6A", "Bb": "6B", "A#": "6B",
    "Dm": "7A", "F": "7B",
    "Am": "8A", "C": "8B",
    "Em": "9A", "G": "9B",
    "Bm": "10A", "D": "10B",
    "F#m": "11A", "Gbm": "11A", "A": "11B",
    "C#m": "12A", "Dbm": "12A", "E": "12B",
}

PITCH_CLASS_NAMES = ["C", "C#", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"]

# Krumhansl-Schmuckler tonal profiles
MAJOR_PROFILE = np.array([6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88], dtype=np.float64)
MINOR_PROFILE = np.array([6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17], dtype=np.float64)


def strip_accents(text: str) -> str:
    nfkd = unicodedata.normalize("NFKD", text)
    return "".join(c for c in nfkd if not unicodedata.combining(c))


def normalize_token(text: str) -> str:
    clean = strip_accents(text.lower()).strip()
    clean = re.sub(r"[^a-z0-9]+", " ", clean)
    return re.sub(r"\s+", " ", clean).strip()


def normalize_bpm(bpm: float) -> float:
    """Normalizes half-time/double-time BPM estimates into a club/dance-friendly 82-164 BPM range."""
    if bpm <= 0:
        return 120.0
    while bpm < 82.0:
        bpm *= 2.0
    while bpm > 164.0:
        bpm /= 2.0
    return round(float(bpm), 2)


def camelot_distance(cam_a: str, cam_b: str) -> int:
    """Returns harmonic compatibility bonus (higher = smoother mix)."""
    if not cam_a or not cam_b:
        return 6
    m_a = re.match(r"^(\d{1,2})([AB])$", cam_a.upper())
    m_b = re.match(r"^(\d{1,2})([AB])$", cam_b.upper())
    if not m_a or not m_b:
        return 6
    num_a, mode_a = int(m_a.group(1)), m_a.group(2)
    num_b, mode_b = int(m_b.group(1)), m_b.group(2)

    diff = min((num_a - num_b) % 12, (num_b - num_a) % 12)
    if num_a == num_b and mode_a == mode_b:
        return 30  # Perfect match
    if diff == 1 and mode_a == mode_b:
        return 25  # Adjacent fifth/fourth on Camelot wheel
    if num_a == num_b and mode_a != mode_b:
        return 22  # Relative major/minor
    if diff == 2 and mode_a == mode_b:
        return 14  # Energy boost (+2)
    if diff == 1 and mode_a != mode_b:
        return 10
    return 0


def parse_track_title_artist(path: str) -> Tuple[str, str]:
    stem = Path(path).stem
    # Strip leading year prefix like "1981 - "
    cleaned = re.sub(r"^\d{4}\s*[-_]\s*", "", stem).strip()
    if " - " in cleaned:
        parts = cleaned.split(" - ", 1)
        return parts[0].strip(), parts[1].strip()
    return "", cleaned


@dataclass
class TrackInfo:
    path: str
    title: str
    artist: str
    duration: float
    bpm: float
    camelot: str
    key_name: str
    energy: float
    rms_db: float
    intro_trim: float
    outro_start: float
    mtime: float
    size: int
    hpss_done: bool = False
    hpss_ver: int = 0
    vocal_ratio: float = 0.5


FOLDER_CACHE_DIR = os.path.join(CACHE_DIR, "folders")


def _folder_cache_path(folder_path: str) -> str:
    norm = re.sub(r"[^a-zA-Z0-9_-]+", "_", os.path.basename(os.path.normpath(folder_path))).strip("_") or "root"
    import hashlib

    digest = hashlib.md5(os.path.realpath(folder_path).encode("utf-8")).hexdigest()[:8]
    return os.path.join(FOLDER_CACHE_DIR, f"{norm}_{digest}.json")


def load_folder_analysis_cache(folder_path: str) -> Dict[str, dict]:
    """Loads per-directory cached DJ metadata (BPM, Camelot, mix_in, mix_out, energy, vocal_ratio)."""
    if not folder_path:
        return {}
    cpath = _folder_cache_path(folder_path)
    if not os.path.exists(cpath):
        return {}
    try:
        with open(cpath, "r", encoding="utf-8") as f:
            data = json.load(f)
        if isinstance(data, dict) and isinstance(data.get("tracks"), dict):
            return data["tracks"]
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def save_folder_analysis_cache(folder_path: str, folder_tracks: Dict[str, dict]) -> None:
    """Persists per-directory JSON metadata cache so subsequent DJ launches skip redundant analysis."""
    if not folder_path:
        return
    try:
        os.makedirs(FOLDER_CACHE_DIR, exist_ok=True)
        cpath = _folder_cache_path(folder_path)
        payload = {
            "folder": os.path.realpath(folder_path),
            "version": 3,
            "tracks": {
                p: {
                    "title": m.get("title", ""),
                    "artist": m.get("artist", ""),
                    "duration": m.get("duration", 0.0),
                    "bpm": m.get("bpm", 0.0),
                    "camelot": m.get("camelot", ""),
                    "key_name": m.get("key_name", ""),
                    "energy": m.get("energy", 0.5),
                    "rms_db": m.get("rms_db", -14.0),
                    "mix_in_sec": m.get("intro_trim", 0.0),
                    "mix_out_sec": m.get("outro_start", 0.0),
                    "intro_trim": m.get("intro_trim", 0.0),
                    "outro_start": m.get("outro_start", 0.0),
                    "vocal_ratio": m.get("vocal_ratio", 0.5),
                    "mtime": m.get("mtime", 0.0),
                    "size": m.get("size", 0),
                    "hpss_done": m.get("hpss_done", False),
                    "hpss_ver": m.get("hpss_ver", 0),
                }
                for p, m in folder_tracks.items()
            },
        }
        tmp = cpath + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False, indent=2)
        os.replace(tmp, cpath)
    except Exception:
        pass


def load_mixxx_index() -> Dict[str, dict]:
    """Reads pre-analyzed BPM, key, duration, and replaygain from Mixxx's SQLite DB if available."""
    index: Dict[str, dict] = {}
    if not os.path.exists(MIXXX_DB_PATH):
        return index
    try:
        conn = sqlite3.connect(f"file:{MIXXX_DB_PATH}?mode=ro", uri=True, timeout=2.0)
        cur = conn.cursor()
        rows = cur.execute(
            """
            SELECT tl.location, l.artist, l.title, l.bpm, l.key, l.duration, l.replaygain
            FROM library l
            JOIN track_locations tl ON l.location = tl.id
            WHERE COALESCE(l.mixxx_deleted, 0) = 0
            """
        ).fetchall()
        conn.close()
        for loc, artist, title, bpm, key, duration, replaygain in rows:
            if not loc:
                continue
            real_loc = os.path.realpath(loc)
            key_str = (key or "").strip()
            camelot = KEY_TO_CAMELOT.get(key_str, "")
            if not camelot and re.match(r"^\d{1,2}[AB]$", key_str.upper()):
                camelot = key_str.upper()
            entry = {
                "artist": (artist or "").strip(),
                "title": (title or "").strip(),
                "bpm": normalize_bpm(float(bpm)) if bpm and float(bpm) > 0 else 0.0,
                "key_name": key_str,
                "camelot": camelot,
                "duration": float(duration or 0.0),
                "replaygain": float(replaygain or 1.0),
            }
            index[loc] = entry
            index[real_loc] = entry
    except Exception:
        pass
    return index


def load_analysis_cache() -> Dict[str, dict]:
    if not os.path.exists(ANALYSIS_CACHE_FILE):
        return {}
    try:
        with open(ANALYSIS_CACHE_FILE, "r", encoding="utf-8") as f:
            data = json.load(f)
            return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def save_analysis_cache(cache: Dict[str, dict]) -> None:
    try:
        os.makedirs(CACHE_DIR, exist_ok=True)
        tmp = ANALYSIS_CACHE_FILE + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(cache, f, ensure_ascii=False)
        os.replace(tmp, ANALYSIS_CACHE_FILE)
    except Exception:
        pass


def ffprobe_duration(path: str) -> float:
    try:
        out = subprocess.check_output(
            [
                "ffprobe", "-v", "error", "-show_entries", "format=duration",
                "-of", "default=noprint_wrappers=1:nokey=1", path
            ],
            stderr=subprocess.DEVNULL,
            timeout=8,
        ).decode("utf-8", errors="ignore").strip()
        val = float(out)
        return val if math.isfinite(val) and val > 0 else 0.0
    except Exception:
        return 0.0


def detect_camelot_from_chroma(chroma: np.ndarray) -> Tuple[str, str]:
    mean_chroma = np.mean(chroma, axis=1)
    norm = np.linalg.norm(mean_chroma)
    if norm <= 1e-9:
        return "8A", "Am"
    mean_chroma = mean_chroma / norm

    best_score = -1e9
    best_name = "Am"
    for shift in range(12):
        maj_rot = np.roll(MAJOR_PROFILE, shift)
        maj_rot = maj_rot / np.linalg.norm(maj_rot)
        score_maj = float(np.dot(mean_chroma, maj_rot))
        if score_maj > best_score:
            best_score = score_maj
            best_name = PITCH_CLASS_NAMES[shift]

        min_rot = np.roll(MINOR_PROFILE, shift)
        min_rot = min_rot / np.linalg.norm(min_rot)
        score_min = float(np.dot(mean_chroma, min_rot))
        if score_min > best_score:
            best_score = score_min
            best_name = PITCH_CLASS_NAMES[shift] + "m"

    return KEY_TO_CAMELOT.get(best_name, "8A"), best_name


def decode_pcm_segment(path: str, start_sec: float = 0.0, duration_sec: Optional[float] = None, sr: int = 22050, channels: int = 1) -> np.ndarray:
    cmd = ["ffmpeg", "-v", "error"]
    if start_sec > 0:
        cmd += ["-ss", f"{start_sec:.3f}"]
    cmd += ["-i", path]
    if duration_sec is not None and duration_sec > 0:
        cmd += ["-t", f"{duration_sec:.3f}"]
    cmd += ["-f", "f32le", "-ar", str(sr), "-ac", str(channels), "pipe:1"]
    raw = subprocess.check_output(cmd, stderr=subprocess.DEVNULL, timeout=30)
    arr = np.frombuffer(raw, dtype=np.float32)
    if channels > 1:
        usable = (len(arr) // channels) * channels
        return arr[:usable].reshape(-1, channels)
    return arr


def analyze_track_openaudio(
    path: str,
    mixxx_index: Dict[str, dict],
    cache: Dict[str, dict],
) -> Optional[TrackInfo]:
    """Analyzes BPM, key, loudness, and intro/outro cue points using Mixxx DB + librosa OpenAudio DSP."""
    try:
        st = os.stat(path)
    except OSError:
        return None

    mtime = float(st.st_mtime)
    size = int(st.st_size)
    if size < 100_000:
        return None

    cached = cache.get(path)
    if cached and abs(cached.get("mtime", 0.0) - mtime) < 1e-3 and cached.get("size") == size:
        valid_fields = TrackInfo.__dataclass_fields__
        kwargs = {k: v for k, v in cached.items() if k in valid_fields}
        kwargs.setdefault("path", path)
        return TrackInfo(**kwargs)

    mixxx_entry = mixxx_index.get(path) or mixxx_index.get(os.path.realpath(path))
    duration = (mixxx_entry or {}).get("duration", 0.0)
    if duration <= 0:
        duration = ffprobe_duration(path)
    if duration < MIN_TRACK_DURATION_SEC or duration > MAX_TRACK_DURATION_SEC:
        return None

    parsed_artist, parsed_title = parse_track_title_artist(path)
    artist = (mixxx_entry or {}).get("artist") or parsed_artist
    title = (mixxx_entry or {}).get("title") or parsed_title

    bpm = (mixxx_entry or {}).get("bpm", 0.0)
    camelot = (mixxx_entry or {}).get("camelot", "")
    key_name = (mixxx_entry or {}).get("key_name", "")

    import librosa  # Loaded inside venv

    # Analyze core body (offset ~28s, 36s window) for tempo, key, and RMS energy
    sample_offset = min(28.0, max(0.0, duration * 0.22))
    sample_dur = min(36.0, max(12.0, duration - sample_offset - 5.0))
    try:
        y = decode_pcm_segment(path, start_sec=sample_offset, duration_sec=sample_dur, sr=22050, channels=1)
    except Exception:
        return None

    if len(y) < 22050 * 6:
        return None

    rms_val = float(np.sqrt(np.mean(np.square(y)) + 1e-12))
    rms_db = float(20.0 * math.log10(max(rms_val, 1e-6)))
    energy = float(max(0.05, min(1.0, (rms_val - 0.03) / 0.22)))

    if bpm <= 0:
        tempo, _ = librosa.beat.beat_track(y=y, sr=22050)
        raw_bpm = float(np.atleast_1d(tempo)[0])
        bpm = normalize_bpm(raw_bpm)

    if not camelot:
        chroma = librosa.feature.chroma_cqt(y=y, sr=22050)
        camelot, key_name = detect_camelot_from_chroma(chroma)

    # Fast intro silence / dead-air trim check (first 14 seconds)
    intro_trim = 0.0
    try:
        y_in = decode_pcm_segment(path, start_sec=0.0, duration_sec=min(14.0, duration * 0.2), sr=22050, channels=1)
        if len(y_in) > 2048:
            hop = 512
            frame_rms = librosa.feature.rms(y=y_in, frame_length=2048, hop_length=hop)[0]
            peak = float(np.max(frame_rms))
            thresh = max(peak * 0.08, 0.008)
            active = np.where(frame_rms >= thresh)[0]
            if len(active) > 0:
                intro_trim = max(0.0, float(active[0] * hop / 22050.0) - 0.08)
    except Exception:
        intro_trim = 0.0

    # Fast outro energy tail detection (last 32 seconds) so transitions happen BEFORE dead fade-out
    outro_win = min(32.0, duration * 0.3)
    outro_base = max(intro_trim + 25.0, duration - outro_win)
    outro_start = max(intro_trim + 25.0, duration - 16.0)
    try:
        y_out = decode_pcm_segment(path, start_sec=outro_base, duration_sec=outro_win, sr=22050, channels=1)
        if len(y_out) > 4096:
            hop = 512
            frame_rms = librosa.feature.rms(y=y_out, frame_length=2048, hop_length=hop)[0]
            # Find last point where track still carries >= 25% of its body energy
            energy_thresh = max(rms_val * 0.28, 0.015)
            active_out = np.where(frame_rms >= energy_thresh)[0]
            if len(active_out) > 0:
                last_active_sec = outro_base + float(active_out[-1] * hop / 22050.0)
                # Start mixing out before active energy ends (~10-14s window for 5s Drop-In / Crossfade)
                mix_len = max(10.0, min(14.0, 20.0 * 60.0 / max(bpm, 80.0)))
                outro_start = max(intro_trim + 25.0, min(duration - 13.0, last_active_sec - mix_len))
    except Exception:
        pass

    info = TrackInfo(
        path=path,
        title=title,
        artist=artist,
        duration=round(duration, 2),
        bpm=round(bpm, 2),
        camelot=camelot,
        key_name=key_name,
        energy=round(energy, 3),
        rms_db=round(rms_db, 2),
        intro_trim=round(intro_trim, 3),
        outro_start=round(outro_start, 3),
        mtime=mtime,
        size=size,
    )
    cache[path] = asdict(info)
    return info


def find_mix_windows_from_pcm(
    y: np.ndarray,
    sr: int,
    bpm: float,
    fallback_in: float = 0.0,
    fallback_out: Optional[float] = None,
) -> Tuple[float, float]:
    """
    Uses Harmonic-Percussive Source Separation (HPSS) to detect the cleanest DJ Mix-In
    and Mix-Out windows anywhere in the track where drums/percussion are strong and
    vocal harmonic energy (300-3200 Hz) is minimal.
    """
    import librosa

    dur = len(y) / float(sr)
    if fallback_out is None:
        fallback_out = max(fallback_in + 20.0, dur - 16.0)
    if dur < 30.0:
        return fallback_in, fallback_out

    n_fft = 1024
    hop = 512
    S = np.abs(librosa.stft(y, n_fft=n_fft, hop_length=hop))
    if S.shape[1] < 20:
        return fallback_in, fallback_out

    H, P = librosa.decompose.hpss(S, margin=(1.0, 2.0))
    freqs = librosa.fft_frequencies(sr=sr, n_fft=n_fft)
    vocal_mask = (freqs >= 300.0) & (freqs <= 3200.0)
    drum_mask = (freqs >= 40.0) & (freqs <= 200.0)

    vocal_harm = np.sqrt(np.mean(H[vocal_mask, :] ** 2, axis=0))
    perc_energy = np.sqrt(np.mean(P ** 2, axis=0))
    kick_energy = np.sqrt(np.mean(P[drum_mask, :] ** 2, axis=0))
    total_rms = np.sqrt(np.mean(S ** 2, axis=0)) + 1e-8

    onset_env = librosa.onset.onset_strength(
        S=librosa.amplitude_to_db(P, ref=np.max), sr=sr, hop_length=hop
    )
    fps = float(sr) / hop
    med_total = float(np.median(total_rms))
    med_vocal = float(np.median(vocal_harm)) + 1e-8
    med_perc = float(np.median(perc_energy)) + 1e-8
    med_kick = float(np.median(kick_energy)) + 1e-8

    # 1. Detect Mix-In window strictly in the opening intro [0 .. min(36s, 22% dur)]
    in_end = min(int(36.0 * fps), int(0.22 * S.shape[1]))
    in_win = max(int(6.0 * fps), 1)
    head_win = max(int(2.0 * fps), 1)
    best_in_t = fallback_in
    best_in_score = -1e9
    for start in range(0, max(1, in_end - in_win), max(1, int(0.5 * fps))):
        sl = slice(start, start + in_win)
        sl_head = slice(start, min(start + head_win, start + in_win))
        r = float(np.mean(total_rms[sl]))
        if r < med_total * 0.40:
            continue
        # Ensure drums & kick rhythm are solidly present right at entry (avoid quiet/beatless dips)
        if float(np.mean(perc_energy[sl_head])) < med_perc * 0.50:
            continue
        if float(np.mean(kick_energy[sl_head])) < med_kick * 0.45:
            continue
        drum_ratio = float(np.mean(perc_energy[sl]) + np.mean(kick_energy[sl])) / (
            float(np.mean(vocal_harm[sl])) + 1e-6
        )
        voc_pen = float(np.mean(vocal_harm[sl])) / med_vocal
        voc_extra = 2.0 if voc_pen > 0.80 else 0.0
        sc = drum_ratio * 2.0 - voc_pen * 2.2 - voc_extra - (start / fps) * 0.025
        if sc > best_in_score:
            best_in_score = sc
            best_in_t = start / fps

    # 2. Detect Mix-Out window in the genuine final outro zone (so full tracks are never cut in half!)
    # Picks the cleanest vocal-free rhythmic outro window without chopping mid-song breaks.
    if dur >= 180.0:
        mix_len_sec = max(14.0, min(20.0, 32.0 * 60.0 / max(bpm, 80.0)))
        end_margin_sec = 20.0
        out_start_sec = max(dur * 0.65, dur - 85.0)
    else:
        mix_len_sec = max(10.0, min(14.0, 20.0 * 60.0 / max(bpm, 80.0)))
        end_margin_sec = 12.0
        out_start_sec = max(dur * 0.62, dur - 55.0)
    out_win = max(int(mix_len_sec * fps), 1)
    min_play_sec = best_in_t + (80.0 if dur >= 160.0 else max(25.0, dur * 0.45))
    out_start_f = max(int(min_play_sec * fps), int(out_start_sec * fps))
    out_end_f = max(out_start_f + 1, S.shape[1] - int(end_margin_sec * fps))

    best_out_t = fallback_out
    best_out_score = -1e9
    for strict_rhythm in (True, False):
        if best_out_score > -1e8:
            break
        for start in range(out_start_f, max(out_start_f + 1, out_end_f - out_win), max(1, int(1.0 * fps))):
            sl = slice(start, start + out_win)
            r = float(np.mean(total_rms[sl]))
            if r < med_total * (0.40 if strict_rhythm else 0.25):
                continue
            perc_mean = float(np.mean(perc_energy[sl]))
            kick_mean = float(np.mean(kick_energy[sl]))
            if strict_rhythm and (perc_mean < med_perc * 0.55 or kick_mean < med_kick * 0.45):
                continue
            drum_ratio = (perc_mean + 0.8 * kick_mean) / (float(np.mean(vocal_harm[sl])) + 1e-6)
            voc_ratio = float(np.mean(vocal_harm[sl])) / med_vocal
            pulse = float(np.std(onset_env[sl]))
            pos_frac = start / float(S.shape[1])
            voc_penalty = voc_ratio * 3.5 + (3.0 if voc_ratio > 0.80 else 0.0)
            sc = drum_ratio * 2.5 + pulse * 0.3 - voc_penalty + pos_frac * 0.6
            if sc > best_out_score:
                best_out_score = sc
                best_out_t = start / fps

    best_out_t = max(best_in_t + 25.0, min(dur - (20.0 if dur >= 180.0 else 13.0), best_out_t))
    return round(best_in_t, 3), round(best_out_t, 3)


def ensure_track_mix_windows(track: TrackInfo) -> TrackInfo:
    """Refines a track's intro_trim (mix_in) and outro_start (mix_out) using HPSS drum/no-vocal detection."""
    if track.hpss_done and track.hpss_ver >= 6:
        return track
    try:
        y = decode_pcm_segment(track.path, start_sec=0.0, duration_sec=None, sr=11025, channels=1)
        if len(y) > 11025 * 30:
            mix_in, mix_out = find_mix_windows_from_pcm(
                y, 11025, track.bpm, fallback_in=track.intro_trim, fallback_out=track.outro_start
            )
            track.intro_trim = mix_in
            track.outro_start = mix_out
            track.hpss_done = True
            track.hpss_ver = 6
            cache = load_analysis_cache()
            cache[track.path] = asdict(track)
            save_analysis_cache(cache)
    except Exception:
        pass
    return track


def is_concierto_discoteque_query(raw_query: str) -> bool:
    """Returns True when the user invokes the 'Concierto Discoteque' radio-show variant."""
    norm = normalize_token(raw_query or "")
    return bool(re.search(r"\bconcierto\s+dis(?:c|k)oth?[eèé]?que\b", norm))


def _strip_trailing_device_hints(text: str) -> str:
    cleaned = (text or "").strip().strip("\"'«».,!?")
    cleaned = re.sub(
        r"\b(?:sur\s+(?:mon|le)\s+(?:pc|ordi|t[eé]l(?:[eé]phone)?|mobile|portable|enceintes?)|s[' ]?il\s+te\s+pla[iî]t|stp)\b.*$",
        "",
        cleaned,
        flags=re.IGNORECASE,
    ).strip().strip("\"'«».,!?")
    return cleaned


_TRACK_COUNT_PATTERN = re.compile(
    r"(?:^|\s)(?:(?:pour|de|d'|avec|sur|limite\s+[àa])\s+)?(\d{1,3})\s+(?:titres?|morceaux?|pistes?|chansons?|sons?|tracks?)\b",
    flags=re.IGNORECASE,
)


def extract_requested_track_count(raw_query: str) -> Optional[int]:
    """
    Extracts an explicit track count if the user specified one (e.g. '5 titres',
    '12 morceaux', '8 tracks'). Returns None when no count is requested, allowing
    the mix to run continuously. Never matches folder numbers like '125 BPM' or '80s'.
    """
    m = _TRACK_COUNT_PATTERN.search(raw_query or "")
    if not m:
        return None
    val = int(m.group(1))
    return val if val >= 1 else None


def _strip_track_count_clause(text: str) -> str:
    cleaned = _TRACK_COUNT_PATTERN.sub(" ", text or "")
    cleaned = re.sub(r"\s{2,}", " ", cleaned).strip()
    return cleaned.strip("\"'«».,!?")


def extract_requested_folder_and_start_track(raw_query: str) -> Tuple[str, str]:
    """
    Extracts (folder_hint, start_track_hint) from natural queries such as:
    - "lance Concierto Discoteque Hits-Remix en commençant par Celebration"
    - "lance un mix avec discoteque-80s et commence par Don't Go"
    - "lance Concierto Discoteque 125 BPM best 80's"
    """
    q = _strip_track_count_clause(_strip_trailing_device_hints(raw_query or ""))
    if not q:
        return "", ""

    start_track = ""
    start_patterns = [
        r"(?:,?\s*(?:et\s+)?)(?:en\s+)?(?:commen[cç]ant|d[eé]marrant|partant|d[eé]butant)\s+(?:(?:par|avec|sur|de)\s+)?(.+)$",
        r"(?:,?\s*(?:et\s+)?)(?:tu\s+)?(?:commences?|d[eé]marres?|d[eé]butes?|pars)\s+(?:(?:par|avec|sur|de)\s+)?(.+)$",
        r"(?:,?\s*)(?:avec\s+)?(?:comme\s+)?(?:premier\s+(?:titre|morceau|son|track)|titre\s+de\s+d[eé]part)\s*[:=]?\s*(.+)$",
    ]
    for pat in start_patterns:
        m_st = re.search(pat, q, flags=re.IGNORECASE)
        if m_st:
            cand_st = _strip_trailing_device_hints(m_st.group(1))
            cand_st = re.sub(
                r"^(?:le\s+(?:titre|morceau|son)|la\s+(?:chanson|piste)|l['’]?\s*(?:audio|ouverture))\s+",
                "",
                cand_st,
                flags=re.IGNORECASE,
            ).strip().strip("\"'«».,!?")
            if cand_st:
                start_track = cand_st
                q = _strip_track_count_clause(_strip_trailing_device_hints(q[: m_st.start()]))
                break

    if not q:
        return "", start_track

    # Variant pattern: "lance Concierto Discoteque [avec/dans/du/de] <folder>"
    m_cd = re.search(
        r"\bconcierto\s+dis(?:c|k)oth?[eèé]?que\b\s*(?:(?:avec|dans|du|de|sur)\s+)?(?:le\s+(?:r[eé]pertoire|dossier)\s+)?(.*)$",
        q,
        flags=re.IGNORECASE,
    )
    if m_cd:
        folder = _strip_trailing_device_hints(m_cd.group(1))
        if normalize_token(folder) in {"", "pc", "mon pc", "telephone", "mon telephone", "mobile"}:
            return "", start_track
        return folder, start_track

    # Explicit pattern: "... mix avec/dans/du/de <folder>"
    m = re.search(
        r"\bmix\s+(?:avec|dans|du|de|sur)\s+(?:le\s+(?:r[eé]pertoire|dossier)\s+)?(.+)$",
        q,
        flags=re.IGNORECASE,
    )
    if m:
        cand_folder = _strip_trailing_device_hints(m.group(1))
        if normalize_token(cand_folder) in {"", "pc", "mon pc", "telephone", "mon telephone", "mobile"}:
            return "", start_track
        return cand_folder, start_track

    # Check if user wrote "dans le répertoire X" / "du dossier X"
    m2 = re.search(r"\b(?:dans|du|de)\s+(?:le\s+)?(?:r[eé]pertoire|dossier)\s+(.+)$", q, flags=re.IGNORECASE)
    if m2:
        return _strip_trailing_device_hints(m2.group(1)), start_track

    # If the query isn't a full natural sentence (e.g. direct folder name passed to CLI), check directly
    lower_norm = normalize_token(q)
    generic_triggers = {
        "lance un mix", "lance moi un mix", "mets un mix", "mets moi un mix",
        "fais un mix", "fais moi un mix", "genere un mix", "cree un mix",
        "joue un mix", "mix", "mix dj", "openaudio", "mix openaudio",
        "lance un mix dj", "lance un mix openaudio", "un mix",
        "lance un mix continu", "fais un mix continu", "mix continu",
    }
    if lower_norm in generic_triggers:
        return "", start_track

    # If query mentions generic mix words without naming a folder
    if re.match(r"^(?:pixel\s+)?(?:lance|lances|mets|met|fais|fai|genere|cree|joue)(?:\s+moi)?\s+un\s+mix(?:\s+(?:dj|openaudio|continu|aleatoire|au\s+hasard|sur\s+mon\s+pc|sur\s+mon\s+tel.*))?$", lower_norm):
        return "", start_track

    return q, start_track


def extract_requested_folder_query(raw_query: str) -> str:
    """Extracts the folder name after 'lance un mix avec ...' or 'lance Concierto Discoteque ...', or ''."""
    folder, _ = extract_requested_folder_and_start_track(raw_query)
    return folder


def resolve_music_folder(folder_hint: str) -> Optional[str]:
    """Resolves a requested folder name inside ~/Musique (exact, fuzzy, accent-insensitive)."""
    if not folder_hint or not os.path.isdir(BASE_MUSIC_DIR):
        return None

    expanded = os.path.expanduser(folder_hint)
    if os.path.isdir(expanded):
        return expanded

    direct = os.path.join(BASE_MUSIC_DIR, folder_hint)
    if os.path.isdir(direct):
        return direct

    hint_norm = normalize_token(folder_hint)
    if not hint_norm:
        return None

    candidates: List[Tuple[int, str]] = []
    for entry in os.listdir(BASE_MUSIC_DIR):
        full = os.path.join(BASE_MUSIC_DIR, entry)
        if not os.path.isdir(full):
            continue
        entry_norm = normalize_token(entry)
        if entry_norm == hint_norm:
            return full
        if hint_norm in entry_norm or entry_norm in hint_norm:
            score = 100 - abs(len(entry_norm) - len(hint_norm))
            candidates.append((score, full))
        else:
            # Word overlap score
            hint_words = set(hint_norm.split())
            entry_words = set(entry_norm.split())
            overlap = len(hint_words & entry_words)
            if overlap > 0:
                score = overlap * 30 - len(hint_words ^ entry_words)
                candidates.append((score, full))

    if candidates:
        candidates.sort(key=lambda x: x[0], reverse=True)
        return candidates[0][1]
    return None


def list_available_music_folders() -> List[str]:
    if not os.path.isdir(BASE_MUSIC_DIR):
        return []
    folders = []
    for entry in sorted(os.listdir(BASE_MUSIC_DIR)):
        full = os.path.join(BASE_MUSIC_DIR, entry)
        if os.path.isdir(full) and entry.lower() not in EXCLUDED_RANDOM_FOLDERS:
            folders.append(entry)
    return folders


def collect_audio_files(directory: str, recursive: bool = True) -> List[str]:
    files: List[str] = []
    if recursive:
        for root, _, filenames in os.walk(directory, followlinks=True):
            for fn in filenames:
                if os.path.splitext(fn)[1].lower() in AUDIO_EXTENSIONS:
                    files.append(os.path.join(root, fn))
    else:
        for fn in os.listdir(directory):
            full = os.path.join(directory, fn)
            if os.path.isfile(full) and os.path.splitext(fn)[1].lower() in AUDIO_EXTENSIONS:
                files.append(full)
    return files


def collect_random_library_files() -> List[str]:
    """Collects musical tracks across ~/Musique while skipping announcement/karaoke folders."""
    files: List[str] = []
    if not os.path.isdir(BASE_MUSIC_DIR):
        return files
    for entry in os.listdir(BASE_MUSIC_DIR):
        if entry.lower() in EXCLUDED_RANDOM_FOLDERS:
            continue
        full = os.path.join(BASE_MUSIC_DIR, entry)
        if os.path.isdir(full):
            for f in collect_audio_files(full, recursive=True):
                low_name = os.path.basename(f).lower()
                if "karaoke" in low_name or "karaoké" in low_name:
                    continue
                files.append(f)
    return files


def transition_compatibility(curr: TrackInfo, nxt: TrackInfo, target_bpm: float) -> Tuple[float, float]:
    """
    Computes (score, stretch_ratio_for_next) where higher score = smoother Pro DJ transition.
    Checks natural BPM as well as double/half-time octave equivalence.
    """
    if nxt.bpm <= 0 or curr.bpm <= 0:
        return 0.0, 1.0

    best_ratio = target_bpm / nxt.bpm
    best_diff = abs(best_ratio - 1.0)
    for mult in (0.5, 2.0):
        cand_ratio = target_bpm / (nxt.bpm * mult)
        cand_diff = abs(cand_ratio - 1.0)
        if cand_diff < best_diff:
            best_diff = cand_diff
            best_ratio = cand_ratio

    # Comfortable DJ stretch is within +/- 6%, acceptable up to +/- 8%; beyond 8% penalize strongly
    if best_diff <= 0.06:
        bpm_score = 70.0 - best_diff * 400.0
    elif best_diff <= 0.08:
        bpm_score = 44.0 - (best_diff - 0.06) * 500.0
    else:
        bpm_score = -350.0 - best_diff * 300.0

    harm_score = float(camelot_distance(curr.camelot, nxt.camelot))
    energy_diff = abs(curr.energy - nxt.energy)
    energy_score = 15.0 * (1.0 - min(1.0, energy_diff * 2.0))

    total = bpm_score + harm_score + energy_score
    return total, best_ratio


def find_matching_track_file(
    files: List[str],
    track_hint: str,
    cache: Optional[Dict[str, dict]] = None,
    mixxx_index: Optional[Dict[str, dict]] = None,
) -> Optional[str]:
    """Finds the best-matching track file for a requested starting track title/artist."""
    raw_hint = (track_hint or "").strip()
    raw_hint = re.sub(r"\.(?:mp3|m4a|wav|flac|ogg|opus|aac|wma|alac|aiff|mka)$", "", raw_hint, flags=re.IGNORECASE)
    q = normalize_token(raw_hint)
    if not q or not files:
        return None
    q_tokens = [w for w in q.split() if len(w) >= 2]
    if not q_tokens:
        q_tokens = q.split()

    best_path: Optional[str] = None
    best_score = 0.0

    for f in files:
        stem = Path(f).stem
        parsed_artist, parsed_title = parse_track_title_artist(f)
        meta = (cache or {}).get(f) or (mixxx_index or {}).get(f) or (mixxx_index or {}).get(os.path.realpath(f)) or {}
        meta_title = str(meta.get("title") or "")
        meta_artist = str(meta.get("artist") or "")

        candidates_str = [
            normalize_token(stem),
            normalize_token(parsed_title),
            normalize_token(f"{parsed_artist} {parsed_title}"),
            normalize_token(meta_title),
            normalize_token(f"{meta_artist} {meta_title}"),
        ]

        score = 0.0
        q_compact = q.replace(" ", "")
        for cs in candidates_str:
            if not cs:
                continue
            cs_compact = cs.replace(" ", "")
            if cs == q or (q_compact and cs_compact == q_compact):
                score = max(score, 100.0)
            elif q in cs or (q_compact and len(q_compact) >= 4 and q_compact in cs_compact):
                # Contiguous substring match; favor tight matches over extra remix tags
                score = max(score, 85.0 - min(20.0, abs(len(cs_compact) - len(q_compact)) * 0.25))
            else:
                cs_words = set(cs.split())
                matched = sum(1 for tok in q_tokens if tok in cs_words or any(tok in w for w in cs_words))
                if matched > 0:
                    recall = matched / float(len(q_tokens))
                    if recall >= 0.5:
                        score = max(score, 55.0 * recall)

        if score > best_score:
            best_score = score
            best_path = f

    return best_path if best_score >= 45.0 else None


def select_pro_dj_set(
    folder_path: Optional[str],
    max_tracks: int = 10,
    start_track_hint: str = "",
) -> Tuple[str, str, List[TrackInfo], float]:
    """
    Analyzes candidate tracks and sequences a Pro DJ set:
    - If folder_path is provided: analyzes tracks in that folder and chains them by rhythm & Camelot key.
    - If folder_path is None: randomly picks a rhythmic anchor across ~/Musique and builds a cohesive set
      from the most rhythm-compatible random tracks across the library.
    - If start_track_hint is provided: prioritizes that specific track as Track 0 (anchor).
    Returns: (mode, folder_label, ordered_tracks, anchor_bpm)
    """
    mixxx_index = load_mixxx_index()
    cache = load_analysis_cache()
    if folder_path:
        folder_cache = load_folder_analysis_cache(folder_path)
        for p, meta in folder_cache.items():
            if p not in cache:
                cache[p] = meta

    matched_start_file: Optional[str] = None

    if folder_path:
        mode = "folder"
        folder_label = os.path.basename(os.path.normpath(folder_path))
        all_files = collect_audio_files(folder_path, recursive=True)
        if not all_files:
            raise RuntimeError(f"Aucune piste audio valide trouvée dans '{folder_path}'.")
        if start_track_hint.strip():
            matched_start_file = find_matching_track_file(all_files, start_track_hint, cache, mixxx_index)
            if not matched_start_file:
                raise RuntimeError(
                    f"Titre '{start_track_hint}' introuvable dans le répertoire '{folder_label}'."
                )
        # Prioritize up to 16 randomized tracks (keeping matched_start_file at candidates[0])
        rest_files = [f for f in all_files if f != matched_start_file]
        random.shuffle(rest_files)
        limit_rest = 15 if matched_start_file else 16
        if len(rest_files) > limit_rest:
            known = [f for f in rest_files if f in cache or f in mixxx_index]
            unknown = [f for f in rest_files if f not in cache and f not in mixxx_index]
            picked = known[:11] + unknown[: max(4, limit_rest - len(known[:11]))]
        else:
            picked = rest_files
        candidates = ([matched_start_file] + picked) if matched_start_file else picked
    else:
        mode = "random_rhythm"
        folder_label = "Sélection Rythme Aléatoire (~/Musique)"
        all_files = collect_random_library_files()
        if not all_files:
            raise RuntimeError("Aucune piste audio trouvée dans ~/Musique.")
        if start_track_hint.strip():
            matched_start_file = find_matching_track_file(all_files, start_track_hint, cache, mixxx_index)
            if not matched_start_file:
                raise RuntimeError(f"Titre '{start_track_hint}' introuvable dans ~/Musique.")

        # Prefer rhythmic tracks with known Mixxx/cached BPM in 98-136 BPM for anchor selection,
        # mixed with fresh random library tracks so the set stays diverse.
        dance_pool = []
        for f in all_files:
            if f == matched_start_file:
                continue
            meta = cache.get(f) or mixxx_index.get(f) or mixxx_index.get(os.path.realpath(f))
            if meta and 96.0 <= float(meta.get("bpm", 0.0)) <= 138.0:
                dur = float(meta.get("duration", 0.0))
                if dur == 0.0 or (MIN_TRACK_DURATION_SEC <= dur <= MAX_TRACK_DURATION_SEC):
                    dance_pool.append(f)

        random.shuffle(dance_pool)
        random.shuffle(all_files)
        if matched_start_file or dance_pool:
            seed_file = matched_start_file or dance_pool[0]
            seed_meta = cache.get(seed_file) or mixxx_index.get(seed_file) or mixxx_index.get(os.path.realpath(seed_file))
            seed_bpm = float((seed_meta or {}).get("bpm", 122.0))
            # Pick tracks close to seed_bpm first + a few fresh unknown tracks
            rhythm_matches = []
            for f in dance_pool:
                if f == seed_file:
                    continue
                meta = cache.get(f) or mixxx_index.get(f) or mixxx_index.get(os.path.realpath(f))
                fbpm = float((meta or {}).get("bpm", 0.0))
                if abs(fbpm - seed_bpm) <= 8.0 or abs(fbpm * 2.0 - seed_bpm) <= 8.0:
                    rhythm_matches.append(f)
                if len(rhythm_matches) >= 13:
                    break
            extra_fresh = [f for f in all_files if f not in rhythm_matches and f != seed_file][:2]
            candidates = [seed_file] + rhythm_matches + extra_fresh
        else:
            candidates = all_files[:16]

    # Analyze candidates concurrently (Librosa Numba warmup on 1st file if needed)
    analyzed: List[TrackInfo] = []
    if candidates:
        first_info = analyze_track_openaudio(candidates[0], mixxx_index, cache)
        if first_info:
            analyzed.append(first_info)
        if len(candidates) > 1:
            with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
                for res in pool.map(lambda p: analyze_track_openaudio(p, mixxx_index, cache), candidates[1:]):
                    if res is not None:
                        analyzed.append(res)

    save_analysis_cache(cache)
    if folder_path:
        save_folder_analysis_cache(folder_path, {t.path: asdict(t) for t in analyzed})

    if not analyzed:
        raise RuntimeError(f"Impossible d'analyser les pistes audio dans '{folder_label}'.")

    # Choose rhythmic anchor track & target set BPM (Storytelling curve when unconstrained)
    if matched_start_file:
        explicit = [t for t in analyzed if t.path == matched_start_file]
        if explicit:
            anchor = explicit[0]
        else:
            raise RuntimeError(
                f"La piste '{start_track_hint}' ne correspond pas aux critères de durée (55s-15min) ou de décodage."
            )
    elif mode == "random_rhythm":
        anchor = analyzed[0]
    else:
        # Storytelling opener: favor a solid mid-tempo track (~108-118 BPM, moderate energy)
        # while staying within reach of the folder median BPM so Rubberband stays natural.
        bpms = sorted(t.bpm for t in analyzed)
        median_bpm = bpms[len(bpms) // 2]
        mid_openers = [
            t for t in analyzed
            if 106.0 <= t.bpm <= min(118.0, median_bpm + 2.0) and abs(t.bpm - median_bpm) <= 8.0
        ]
        if mid_openers:
            anchor = min(mid_openers, key=lambda t: (t.energy, abs(t.bpm - 113.0)))
        else:
            near_median = [t for t in analyzed if abs(t.bpm - median_bpm) <= 7.0]
            anchor = min(near_median if near_median else analyzed, key=lambda t: t.energy)

    anchor_bpm = anchor.bpm
    peak_target_bpm = max(anchor_bpm, min(128.0, max(t.bpm for t in analyzed)))
    ordered: List[TrackInfo] = [anchor]
    remaining = [t for t in analyzed if t.path != anchor.path]

    while remaining and len(ordered) < max_tracks:
        curr = ordered[-1]
        progress = len(ordered) / float(max(2, min(max_tracks, len(analyzed)) - 1))
        # Storytelling target arc: 0..0.72 ramp up energy & BPM toward peak (~122-128), 0.72..1.0 breathe down slightly
        if progress <= 0.72:
            phase = progress / 0.72
            target_step_bpm = anchor_bpm + (peak_target_bpm - anchor_bpm) * phase
            desired_energy = min(1.0, curr.energy + 0.04)
        else:
            cool_phase = (progress - 0.72) / 0.28
            target_step_bpm = peak_target_bpm - (peak_target_bpm - anchor_bpm) * 0.35 * cool_phase
            desired_energy = max(0.35, curr.energy - 0.08)

        scored = []
        for cand in remaining:
            sc, _ = transition_compatibility(curr, cand, anchor_bpm)
            # Storytelling curve bonus/penalty for smooth BPM & energy trajectory
            bpm_arc_dist = abs(cand.bpm - target_step_bpm)
            sc += max(-10.0, 10.0 - bpm_arc_dist * 0.9)
            sc += max(-8.0, 8.0 - abs(cand.energy - desired_energy) * 18.0)
            # Slight penalty for same artist back-to-back
            if curr.artist and cand.artist and curr.artist.lower() == cand.artist.lower():
                sc -= 12.0
            scored.append((sc, cand))
        scored.sort(key=lambda x: x[0], reverse=True)

        # Pick randomly among the top 2-3 compatible tracks for natural DJ spontaneity
        top_pool = [item[1] for item in scored[:3] if item[0] >= scored[0][0] - 10.0]
        chosen = random.choice(top_pool) if top_pool else scored[0][1]
        ordered.append(chosen)
        remaining.remove(chosen)

    return mode, folder_label, ordered, anchor_bpm


def pick_next_continuous_track(
    curr_meta: dict,
    folder_path: str,
    played_paths: List[str],
    anchor_bpm: float,
    max_candidates: int = 12,
) -> Optional[dict]:
    """
    Dynamically selects, analyzes (using cache when fresh), and scores the next compatible
    track for endless continuous DJ mixing once the initial planned batch runs through.
    Respects an anti-repeat cooldown window so songs don't repeat prematurely.
    """
    if folder_path:
        pool = collect_audio_files(folder_path, recursive=True)
    else:
        pool = collect_random_library_files()

    if len(pool) < 2:
        return None

    # Deduplicate preserving order
    unique_pool = list(dict.fromkeys(pool))
    if len(unique_pool) < 2:
        return None

    cooldown = min(len(unique_pool) - 1, max(12, int(len(unique_pool) * 0.75)))
    blocked = set(played_paths[-cooldown:]) if cooldown > 0 else set()
    avail = [p for p in unique_pool if p not in blocked]
    if not avail:
        # Should rarely trigger thanks to cooldown < len(unique_pool), but safe fallback
        last_one = played_paths[-1] if played_paths else ""
        avail = [p for p in unique_pool if p != last_one] or unique_pool

    mixxx_index = load_mixxx_index()
    cache = load_analysis_cache()
    if folder_path:
        folder_cache = load_folder_analysis_cache(folder_path)
        for p, meta in folder_cache.items():
            if p not in cache:
                cache[p] = meta

    random.shuffle(avail)
    if not folder_path:
        # In library-wide continuous mode, prefer candidates with known rhythmic proximity + a couple fresh picks
        close_bpm = []
        fresh = []
        for p in avail:
            meta = cache.get(p) or mixxx_index.get(p) or mixxx_index.get(os.path.realpath(p))
            if meta:
                fbpm = float(meta.get("bpm", 0.0))
                if 96.0 <= fbpm <= 138.0 and (
                    abs(fbpm - anchor_bpm) <= 10.0 or abs(fbpm * 2.0 - anchor_bpm) <= 10.0
                ):
                    close_bpm.append(p)
                    continue
            else:
                fresh.append(p)
        sample_paths = close_bpm[: max(1, max_candidates - 2)] + fresh[:2]
        if len(sample_paths) < max_candidates:
            seen_s = set(sample_paths)
            for p in avail:
                if p not in seen_s:
                    sample_paths.append(p)
                    seen_s.add(p)
                if len(sample_paths) >= max_candidates:
                    break
    else:
        known = [p for p in avail if p in cache or p in mixxx_index]
        unknown = [p for p in avail if p not in cache and p not in mixxx_index]
        n_known = max(0, max_candidates - min(4, len(unknown)))
        sample_paths = known[:n_known] + unknown[: max(0, max_candidates - len(known[:n_known]))]
        if len(sample_paths) < max_candidates:
            rest = [p for p in avail if p not in set(sample_paths)]
            sample_paths.extend(rest[: max_candidates - len(sample_paths)])

    analyzed: List[TrackInfo] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool_ex:
        for res in pool_ex.map(lambda p: analyze_track_openaudio(p, mixxx_index, cache), sample_paths):
            if res is not None:
                analyzed.append(res)

    if not analyzed:
        return None

    save_analysis_cache(cache)
    if folder_path:
        merged_fc = load_folder_analysis_cache(folder_path)
        for t in analyzed:
            merged_fc[t.path] = asdict(t)
        save_folder_analysis_cache(folder_path, merged_fc)

    curr_track = TrackInfo(
        path=str(curr_meta.get("path", "")),
        title=str(curr_meta.get("title", "")),
        artist=str(curr_meta.get("artist", "")),
        duration=float(curr_meta.get("duration", 0.0)),
        bpm=float(curr_meta.get("bpm", anchor_bpm)),
        camelot=str(curr_meta.get("camelot", "8A")),
        key_name=str(curr_meta.get("key", "")),
        energy=float(curr_meta.get("energy", 0.5)),
        rms_db=float(curr_meta.get("rms_db", -14.0)),
        intro_trim=float(curr_meta.get("intro_trim", 0.0)),
        outro_start=float(curr_meta.get("outro_start", 180.0)),
        mtime=float(curr_meta.get("mtime", 0.0)),
        size=int(curr_meta.get("size", 0)),
    )

    scored = []
    for cand in analyzed:
        sc, _ = transition_compatibility(curr_track, cand, anchor_bpm)
        if curr_track.artist and cand.artist and curr_track.artist.lower() == cand.artist.lower():
            sc -= 12.0
        scored.append((sc, cand))
    scored.sort(key=lambda x: x[0], reverse=True)

    top_pool = [item[1] for item in scored[:3] if item[0] >= scored[0][0] - 10.0]
    chosen = random.choice(top_pool) if top_pool else scored[0][1]
    return {
        "path": chosen.path,
        "title": chosen.title,
        "artist": chosen.artist,
        "duration": chosen.duration,
        "bpm": chosen.bpm,
        "camelot": chosen.camelot,
        "key": chosen.key_name,
        "energy": chosen.energy,
        "rms_db": chosen.rms_db,
        "intro_trim": chosen.intro_trim,
        "outro_start": chosen.outro_start,
    }


def unclamped_bpm_ratio(track_bpm: float, target_bpm: float) -> float:
    """Returns closest-octave BPM multiplier (target / track) without clamping."""
    if track_bpm <= 0 or target_bpm <= 0:
        return 1.0
    best_ratio = target_bpm / track_bpm
    best_diff = abs(best_ratio - 1.0)
    for mult in (0.5, 2.0):
        r = target_bpm / (track_bpm * mult)
        if abs(r - 1.0) < best_diff:
            best_diff = abs(r - 1.0)
            best_ratio = r
    return best_ratio


def compute_stretch_ratio(track_bpm: float, target_bpm: float) -> float:
    best_ratio = unclamped_bpm_ratio(track_bpm, target_bpm)
    # Safe natural DJ pitch/tempo range (+/- 8%) so full tracks adapt cleanly without strain
    return max(0.92, min(1.08, best_ratio))


def _k_weighting_sos(sr: int = TARGET_SR) -> np.ndarray:
    """Builds exact ITU-R BS.1770-4 K-weighting 2-stage SOS filter for sample rate sr."""
    # Stage 1: pre-filter high-shelf (+4 dB)
    f0 = 1681.974450955533
    G = 3.999843853973347
    Q = 0.7071752369554196
    K = math.tan(math.pi * f0 / sr)
    Vh = 10.0 ** (G / 20.0)
    Vb = Vh ** 0.4996667741545416
    a0 = 1.0 + K / Q + K * K
    sos1 = [
        (Vh + Vb * K / Q + K * K) / a0,
        2.0 * (K * K - Vh) / a0,
        (Vh - Vb * K / Q + K * K) / a0,
        1.0,
        2.0 * (K * K - 1.0) / a0,
        (1.0 - K / Q + K * K) / a0,
    ]

    # Stage 2: RLB high-pass (~38 Hz)
    f0_hp = 38.13547087602444
    Q_hp = 0.5003270373238773
    K_hp = math.tan(math.pi * f0_hp / sr)
    a0_hp = 1.0 + K_hp / Q_hp + K_hp * K_hp
    sos2 = [
        1.0 / a0_hp,
        -2.0 / a0_hp,
        1.0 / a0_hp,
        1.0,
        2.0 * (K_hp * K_hp - 1.0) / a0_hp,
        (1.0 - K_hp / Q_hp + K_hp * K_hp) / a0_hp,
    ]
    return np.array([sos1, sos2], dtype=np.float64)


def measure_integrated_lufs(audio: np.ndarray, sr: int = TARGET_SR) -> float:
    """
    Measures perceptual integrated loudness (ITU-R BS.1770-4 / EBU R128 LUFS)
    over a full mono or stereo audio array using K-weighting + dual gating.
    """
    if len(audio) == 0:
        return -70.0
    arr = audio[:, None] if audio.ndim == 1 else audio
    sos = _k_weighting_sos(sr)
    filtered = sosfilt(sos, arr.astype(np.float64), axis=0)

    # 100ms sub-blocks -> 400ms overlapping gated blocks (75% overlap per BS.1770-4)
    step = max(1, int(0.10 * sr))
    n_sub = len(filtered) // step
    if n_sub < 4:
        mean_sq = float(np.sum(np.mean(filtered ** 2, axis=0)))
        return -0.691 + 10.0 * math.log10(max(mean_sq, 1e-12))

    trimmed = filtered[: n_sub * step]
    # Per-100ms mean square summed across stereo channels (L + R weights = 1.0)
    sub_ms = np.sum(np.mean(trimmed.reshape(n_sub, step, arr.shape[1]) ** 2, axis=1), axis=1)
    # 400ms block energy = average of 4 consecutive 100ms sub-blocks
    block_ms = (sub_ms[:-3] + sub_ms[1:-2] + sub_ms[2:-1] + sub_ms[3:]) * 0.25
    block_lufs = -0.691 + 10.0 * np.log10(np.maximum(block_ms, 1e-12))

    # Absolute gate (-70 LUFS)
    abs_mask = block_lufs >= -70.0
    if not np.any(abs_mask):
        return -70.0
    abs_mean_ms = float(np.mean(block_ms[abs_mask]))
    gamma_r = -0.691 + 10.0 * math.log10(max(abs_mean_ms, 1e-12)) - 10.0

    # Relative gate (-10 LU below ungated mean)
    rel_mask = block_lufs >= gamma_r
    if not np.any(rel_mask):
        return -0.691 + 10.0 * math.log10(max(abs_mean_ms, 1e-12))
    gated_mean_ms = float(np.mean(block_ms[rel_mask]))
    return -0.691 + 10.0 * math.log10(max(gated_mean_ms, 1e-12))


def _apply_soft_peak_ceiling(audio: np.ndarray, knee_start: float = 0.74, ceiling: float = 0.94) -> np.ndarray:
    """Smooth soft-knee peak limiter leaving samples <= knee_start completely untouched."""
    mag = np.abs(audio)
    if float(np.max(mag)) <= ceiling:
        return audio
    knee = max(0.05, ceiling - knee_start)
    out = audio.copy()
    over = mag > knee_start
    if np.any(over):
        excess = (mag[over] - knee_start) / knee
        out[over] = np.sign(out[over]) * (knee_start + knee * np.tanh(excess))
    return out


def normalize_audio_lufs(
    audio: np.ndarray,
    target_lufs: float = -14.0,
    max_gain_db: float = 18.0,
    min_gain_db: float = -18.0,
    sr: int = TARGET_SR,
) -> np.ndarray:
    """
    Normalizes an entire decoded track to exact perceptual target_lufs (-14.0 LUFS)
    with a transparent soft-knee peak limiter so quiet and loud tracks sit evenly.
    """
    if len(audio) == 0:
        return audio.astype(np.float32)
    cur_lufs = measure_integrated_lufs(audio, sr=sr)
    if cur_lufs <= -65.0:
        return audio.astype(np.float32)

    gain_db = max(min_gain_db, min(max_gain_db, target_lufs - cur_lufs))
    gain_lin = 10.0 ** (gain_db / 20.0)
    out = _apply_soft_peak_ceiling((audio.astype(np.float32) * np.float32(gain_lin)), knee_start=0.74, ceiling=0.94)

    # One fast refinement pass in case soft peak limiting trimmed dense chorus energy
    post_lufs = measure_integrated_lufs(out, sr=sr)
    delta_db = target_lufs - post_lufs
    if abs(delta_db) > 0.35 and post_lufs > -65.0:
        trim_db = max(-3.5, min(3.5, delta_db))
        trim_lin = 10.0 ** (trim_db / 20.0)
        out = _apply_soft_peak_ceiling((out * np.float32(trim_lin)), knee_start=0.74, ceiling=0.94)

    return out.astype(np.float32)


def decode_full_track_stretched(path: str, tempo_ratio: float, target_rms_db: float = -14.0, current_rms_db: float = -14.0) -> np.ndarray:
    """Decodes an entire track to stereo 44.1kHz float32, time-stretched with crisp phase-locked Rubberband and EBU R128 LUFS normalized."""
    filters = []
    if abs(tempo_ratio - 1.0) > 0.002:
        filters.append(
            f"rubberband=tempo={tempo_ratio:.6f}:pitch=1.0"
            ":transients=crisp:detector=compound:phase=laminar:formant=preserved:pitchq=quality:channels=together"
        )

    cmd = ["ffmpeg", "-v", "error", "-i", path]
    if filters:
        cmd += ["-af", ",".join(filters)]
    cmd += ["-f", "f32le", "-ar", str(TARGET_SR), "-ac", "2", "pipe:1"]

    raw = subprocess.check_output(cmd, stderr=subprocess.DEVNULL, timeout=90)
    arr = np.frombuffer(raw, dtype=np.float32)
    usable = (len(arr) // 2) * 2
    stereo = arr[:usable].reshape(-1, 2).copy()
    return normalize_audio_lufs(stereo, target_lufs=target_rms_db, sr=TARGET_SR)


def find_leading_silence_sample(audio: np.ndarray, sr: int = TARGET_SR) -> int:
    """
    Returns sample index right after pure dead silence at track start (<= 5s),
    keeping the entire musical intro intact so Track 0 plays from the very beginning.
    """
    if len(audio) < sr:
        return 0
    scan_len = min(len(audio), 6 * sr)
    mono = np.mean(audio[:scan_len], axis=1) if audio.ndim > 1 else audio[:scan_len]
    hop = max(1, int(0.010 * sr))
    n_hops = len(mono) // hop
    if n_hops <= 0:
        return 0
    hop_rms = np.sqrt(np.mean(mono[: n_hops * hop].reshape(n_hops, hop) ** 2, axis=1))
    peak = float(np.max(hop_rms))
    thresh = max(0.004, peak * 0.025)
    active = np.where(hop_rms >= thresh)[0]
    if len(active) == 0:
        return 0
    idx = max(0, int(active[0]) * hop - int(0.025 * sr))
    return min(idx, 5 * sr)


def _compute_kick_envelope(audio_seg: np.ndarray, sr: int = TARGET_SR) -> Tuple[np.ndarray, np.ndarray]:
    """
    Computes (<180 Hz) low-frequency kick drum envelope and attack-weighted transient curve.
    Returns (kick_env, score_env).
    """
    mono = np.mean(audio_seg, axis=1).astype(np.float32) if audio_seg.ndim > 1 else audio_seg.astype(np.float32)
    sos_lp = butter(2, 180.0, btype="lowpass", fs=sr, output="sos")
    low = np.abs(sosfilt(sos_lp, mono))
    win_ms = max(1, int(0.008 * sr))
    kick_env = np.convolve(low, np.ones(win_ms, dtype=np.float32) / win_ms, mode="same")
    shift = max(1, int(0.015 * sr))
    prev_env = np.empty_like(kick_env)
    prev_env[:shift] = 0.0
    prev_env[shift:] = kick_env[:-shift]
    kick_atk = np.maximum(0.0, kick_env - prev_env)
    score_env = (0.40 * kick_env + 0.60 * kick_atk).astype(np.float32)
    return kick_env, score_env


def snap_beat_sample(
    audio: np.ndarray,
    approx_sec: float,
    sr: int = TARGET_SR,
    bpm_hint: float = 0.0,
) -> int:
    """
    Finds the nearest physical kick downbeat sample around approx_sec,
    snapping librosa beat frames onto the peak <180 Hz kick attack (+-45ms).
    """
    center = int(approx_sec * sr)
    win_start = max(0, center - 4 * sr)
    win_end = min(len(audio), center + 4 * sr)
    if win_end - win_start < sr * 2:
        return max(0, min(len(audio) - 1, center))

    try:
        import librosa

        seg = audio[win_start:win_end]
        mono = np.mean(seg, axis=1) if seg.ndim > 1 else seg
        mono_ds = mono[::2]
        sr_ds = sr // 2
        hint = float(bpm_hint) if 60.0 <= float(bpm_hint) <= 200.0 else None
        _, beats = librosa.beat.beat_track(y=mono_ds, sr=sr_ds, bpm=hint)
        if len(beats) > 0:
            beat_samples = win_start + librosa.frames_to_samples(beats) * 2
            nearest = int(beat_samples[np.argmin(np.abs(beat_samples - center))])
            _, score_env = _compute_kick_envelope(seg, sr=sr)
            tol = int(0.045 * sr)
            rel = nearest - win_start
            lo = max(0, rel - tol)
            hi = min(len(score_env), rel + tol + 1)
            if hi > lo:
                refined = win_start + lo + int(np.argmax(score_env[lo:hi]))
                return max(0, min(len(audio) - 1, refined))
            return max(0, min(len(audio) - 1, nearest))
    except Exception:
        pass
    return max(0, min(len(audio) - 1, center))


def apply_highpass_transition(audio: np.ndarray, cutoff_hz: float = 220.0, sr: int = TARGET_SR) -> np.ndarray:
    sos = butter(2, cutoff_hz, btype="highpass", fs=sr, output="sos")
    return sosfilt(sos, audio, axis=0).astype(np.float32)


def render_pro_dj_transition(
    out_tail: np.ndarray,
    in_head: np.ndarray,
    overlap_samples: int,
) -> np.ndarray:
    """
    Renders a Pro DJ Bass-Swap + Equal-Power Crossfade between out_tail and in_head:
    - First half (0..50%): incoming track enters with 220Hz highpass (no bass clash!)
    - Midpoint downbeat (50%): smooth bass swap — outgoing bass rolls off via HPF while incoming bass drops in
    - Equal-power cos/sin gain curve + soft tanh headroom limiter
    """
    n = min(overlap_samples, len(out_tail), len(in_head))
    if n <= TARGET_SR:
        n = min(len(out_tail), len(in_head))
    if n <= 0:
        return in_head

    out_seg = out_tail[:n].astype(np.float32)
    in_seg = in_head[:n].astype(np.float32)

    out_hp = apply_highpass_transition(out_seg, cutoff_hz=230.0)
    in_hp = apply_highpass_transition(in_seg, cutoff_hz=230.0)

    t = np.linspace(0.0, 1.0, n, dtype=np.float32)[:, None]

    # Equal-power fade curves
    out_gain = np.cos(t * (math.pi * 0.5))
    in_gain = np.sin(t * (math.pi * 0.5))

    # Bass-swap envelope peaking smoothly around midpoint (0.42 .. 0.58)
    swap = np.clip((t - 0.40) / 0.18, 0.0, 1.0)
    # Outgoing transitions from full -> HPF after midpoint; incoming transitions from HPF -> full at midpoint
    out_blended = ((1.0 - swap) * out_seg + swap * out_hp) * out_gain
    in_blended = ((1.0 - swap) * in_hp + swap * in_seg) * in_gain

    mixed = out_blended + in_blended
    # Soft saturation ceiling to guarantee 0 clipping during dense drops
    peak = float(np.max(np.abs(mixed)))
    if peak > 0.95:
        mixed = np.tanh(mixed / 0.95) * 0.95
    return mixed.astype(np.float32)


def _refine_kick_phase_offset(
    out_sync: np.ndarray,
    in_sync: np.ndarray,
    sr: int = TARGET_SR,
    max_shift_ms: float = 28.0,
) -> Tuple[np.ndarray, int]:
    """
    Fine-tunes kick drum phase alignment between out_sync and in_sync within +-max_shift_ms
    using <180 Hz kick attack cross-correlation so kicks hit at the exact same sample.
    Returns (aligned_in_sync, applied_shift_samples).
    """
    n = min(len(out_sync), len(in_sync))
    if n < sr * 2:
        return in_sync, 0

    scan_n = min(n, int(6.0 * sr))
    _, ko = _compute_kick_envelope(out_sync[:scan_n], sr=sr)
    _, ki = _compute_kick_envelope(in_sync[:scan_n], sr=sr)

    norm_o = float(np.sqrt(np.sum(ko * ko)))
    norm_i = float(np.sqrt(np.sum(ki * ki)))
    if norm_o < 1e-6 or norm_i < 1e-6:
        return in_sync, 0

    max_lag = int(round((max_shift_ms * 0.001) * sr))
    margin = max_lag + 16
    if scan_n <= 2 * margin + sr:
        return in_sync, 0

    ko_mid = ko[margin : scan_n - margin]
    zero_corr = float(np.dot(ko_mid, ki[margin : scan_n - margin]))
    best_lag = 0
    best_corr = zero_corr

    coarse_step = max(1, int(round(0.001 * sr)))
    for lag in range(-max_lag, max_lag + 1, coarse_step):
        c = float(np.dot(ko_mid, ki[margin - lag : scan_n - margin - lag]))
        if c > best_corr:
            best_corr = c
            best_lag = lag

    # Fine 4-sample refinement around coarse best_lag
    f_lo = max(-max_lag, best_lag - coarse_step)
    f_hi = min(max_lag, best_lag + coarse_step)
    for lag in range(f_lo, f_hi + 1, 4):
        c = float(np.dot(ko_mid, ki[margin - lag : scan_n - margin - lag]))
        if c > best_corr:
            best_corr = c
            best_lag = lag

    if best_lag == 0 or best_corr < zero_corr * 1.04:
        return in_sync, 0

    aligned = np.zeros_like(in_sync, dtype=np.float32)
    if best_lag > 0:
        aligned[best_lag:] = in_sync[: len(in_sync) - best_lag]
    else:
        shift = -best_lag
        aligned[: len(in_sync) - shift] = in_sync[shift:]
        aligned[len(in_sync) - shift :] = in_sync[len(in_sync) - 1 :]
    return aligned, best_lag


def _stretch_sync_by_beats(
    out_sync: np.ndarray,
    audio_in: np.ndarray,
    in_beat_0: int,
    out_beat_0: int,
    out_beats: np.ndarray,
    in_beats: np.ndarray,
    sync_beats: int,
    sync_samples: int,
    src_sync_len: int,
    sr: int = TARGET_SR,
) -> Tuple[np.ndarray, int]:
    """
    Locks Track 2's constant full-track tempo cleanly onto Track 1 without forcing
    audible bar-by-bar speedups/decelerations: applies at most a single gentle uniform
    micro-trim when local span ratio is within +-4%, then refines pure kick phase (<=28ms).
    Returns (aligned_in_sync, effective_resume_sample).
    """
    local_ratio = src_sync_len / max(1, sync_samples)
    if 0.96 <= local_ratio <= 1.04 and abs(src_sync_len - sync_samples) > 4:
        in_raw = audio_in[in_beat_0 : in_beat_0 + src_sync_len].astype(np.float32)
        warped = _stretch_sync_segment(in_raw, sync_samples)
        eff_src_len = src_sync_len
    else:
        avail = max(0, len(audio_in) - in_beat_0)
        take = min(sync_samples, avail)
        warped = audio_in[in_beat_0 : in_beat_0 + take].astype(np.float32)
        if take < sync_samples and take > 0:
            pad = np.repeat(warped[-1:], sync_samples - take, axis=0)
            warped = np.concatenate([warped, pad], axis=0)
        eff_src_len = take

    aligned, applied_shift = _refine_kick_phase_offset(out_sync, warped, sr=sr, max_shift_ms=28.0)
    resume = max(in_beat_0 + 1, min(len(audio_in) - 1, in_beat_0 + eff_src_len - applied_shift))
    return aligned, resume


def estimate_out_tail_bpm(
    out_tail: np.ndarray,
    fallback_bpm: float,
    sr: int = TARGET_SR,
) -> float:
    """
    Measures true local outro tempo of out_tail around fallback_bpm so Track 2's
    stretched BPM matches Track 1's actual outgoing tempo seamlessly.
    """
    fb = max(65.0, min(190.0, float(fallback_bpm)))
    if len(out_tail) < sr * 4:
        return fb
    scan = out_tail[: min(len(out_tail), 22 * sr)]
    beats = extract_local_beat_samples(scan, fb, sr=sr, allow_extrapolate=False)
    if len(beats) < 6:
        return fb
    ibis = np.diff(beats).astype(np.float64) / float(sr)
    cv = float(np.std(ibis) / max(float(np.mean(ibis)), 1e-6))
    if cv > 0.09:
        return fb
    med_ibi = float(np.median(ibis))
    if med_ibi <= 0.2:
        return fb
    est = 60.0 / med_ibi
    if (0.91 * fb) <= est <= (1.09 * fb):
        return round(est, 2)
    return fb


def render_beatlocked_crossfade(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    precomputed_out_beats: Optional[np.ndarray] = None,
    precomputed_in_beats: Optional[np.ndarray] = None,
    target_sync_sec: Optional[float] = None,
) -> Tuple[np.ndarray, int]:
    """
    Adaptive phase-locked Bass-Swap Crossfade (quick ~5s, medium ~10-12s, or long 80s-style 12-20+s)
    using bar-by-bar beatgrid lock + kick phase alignment across the entire crossfade span.
    Returns (transition_audio, resume_sample_in_audio_in).
    """
    in_start = max(0, min(max(0, len(audio_in) - TARGET_SR), int(in_start_sample)))
    bpm = max(float(anchor_bpm), 80.0)
    beat_sec = 60.0 / bpm
    goal_sec = float(target_sync_sec) if target_sync_sec is not None else 10.0
    sync_beats = 44
    for cand_beats in (8, 12, 16, 20, 24, 28, 32, 36, 40, 44):
        if cand_beats * beat_sec >= goal_sec:
            sync_beats = cand_beats
            break
    nominal_sync = min(len(out_tail), int(round(max(goal_sec, sync_beats * beat_sec) * TARGET_SR)))

    scan_limit = max(15, int(math.ceil(goal_sec)) + 4) * TARGET_SR
    out_beats = (
        precomputed_out_beats
        if precomputed_out_beats is not None and len(precomputed_out_beats) >= 2
        else extract_local_beat_samples(out_tail[: min(len(out_tail), scan_limit)], bpm, sr=TARGET_SR)
    )
    if precomputed_in_beats is not None and len(precomputed_in_beats) >= 2:
        in_beats = precomputed_in_beats
    else:
        in_beats = extract_local_beat_samples(
            audio_in[in_start : min(len(audio_in), in_start + scan_limit)], bpm, sr=TARGET_SR
        ) + in_start

    out_beat_0 = int(out_beats[0]) if len(out_beats) > 0 and int(out_beats[0]) < int(1.8 * TARGET_SR) else 0
    sync_samples = min(nominal_sync, len(out_tail) - out_beat_0)
    if len(out_beats) > sync_beats:
        cand_len = int(out_beats[sync_beats]) - out_beat_0
        if cand_len > TARGET_SR * 3 and (out_beat_0 + cand_len) <= len(out_tail):
            sync_samples = cand_len

    in_beat_0 = int(in_beats[0]) if len(in_beats) > 0 and abs(int(in_beats[0]) - in_start) < int(2.5 * TARGET_SR) else in_start
    if in_beat_0 >= len(audio_in) - max(sync_samples, TARGET_SR * 4):
        in_beat_0 = in_start

    src_sync_len = sync_samples
    if len(in_beats) > sync_beats:
        cand_src = int(in_beats[sync_beats]) - in_beat_0
        local_ratio = cand_src / max(1, sync_samples)
        if 0.96 <= local_ratio <= 1.04 and (in_beat_0 + cand_src) < (len(audio_in) - TARGET_SR * 4):
            src_sync_len = cand_src

    max_avail_in = max(1, len(audio_in) - in_beat_0 - TARGET_SR * 2)
    if src_sync_len > max_avail_in:
        src_sync_len = min(sync_samples, len(audio_in) - in_beat_0)
        sync_samples = min(sync_samples, src_sync_len)

    if sync_samples <= TARGET_SR * 2 or src_sync_len <= TARGET_SR * 2:
        return out_tail.astype(np.float32), in_beat_0

    prefix = out_tail[:out_beat_0].astype(np.float32) if out_beat_0 > 0 else np.zeros((0, 2), dtype=np.float32)
    out_sync = out_tail[out_beat_0 : out_beat_0 + sync_samples].astype(np.float32)
    in_sync, resume_sample = _stretch_sync_by_beats(
        out_sync,
        audio_in,
        in_beat_0=in_beat_0,
        out_beat_0=out_beat_0,
        out_beats=out_beats,
        in_beats=in_beats,
        sync_beats=sync_beats,
        sync_samples=sync_samples,
        src_sync_len=src_sync_len,
        sr=TARGET_SR,
    )
    xf = render_pro_dj_transition(out_sync, in_sync, sync_samples)
    trans = np.concatenate([prefix, xf], axis=0) if len(prefix) > 0 else xf
    return trans, resume_sample


def _regularize_kick_beatgrid(
    score_env: np.ndarray,
    raw_beats: np.ndarray,
    bpm_hint: float,
    sr: int,
    n: int,
) -> np.ndarray:
    """
    Regularizes detected beats onto a periodic <180 Hz kick grid (eliminating spurious double
    hits and offbeat snare snaps via folded modulo periodogram + smooth peak tracking).
    """
    eff_bpm = max(65.0, min(190.0, float(bpm_hint))) if bpm_hint > 0 else 120.0
    T_nom = (60.0 / eff_bpm) * sr

    # Filter consecutive raw beats to true beat-scale intervals
    if len(raw_beats) >= 3:
        clean_raw = [int(raw_beats[0])]
        for b in raw_beats[1:]:
            if (int(b) - clean_raw[-1]) >= int(0.72 * T_nom):
                clean_raw.append(int(b))
        ibis = np.diff(np.array(clean_raw, dtype=np.float64))
        valid_ibis = ibis[(ibis >= 0.78 * T_nom) & (ibis <= 1.24 * T_nom)]
        T_beat = float(np.median(valid_ibis)) if len(valid_ibis) >= 2 else T_nom
    else:
        T_beat = T_nom

    period_i = max(int(0.28 * sr), int(round(T_beat)))
    fold_n = min(n, int(10 * T_beat))
    hist = np.zeros(period_i, dtype=np.float64)
    k_folds = fold_n // period_i
    if k_folds >= 2:
        reshaped = score_env[: k_folds * period_i].reshape(k_folds, period_i)
        hist = np.sum(reshaped, axis=0)
        anchor = int(np.argmax(hist))
    elif len(raw_beats) > 0:
        anchor = int(raw_beats[0]) % period_i
    else:
        anchor = int(np.argmax(score_env[: min(n, period_i)]))

    grid: List[int] = []
    cur_period = float(T_beat)
    tol = max(int(0.025 * sr), int(round(0.14 * cur_period)))

    lo0 = max(0, anchor - tol)
    hi0 = min(n, anchor + tol + 1)
    first_b = (lo0 + int(np.argmax(score_env[lo0:hi0]))) if hi0 > lo0 else min(n - 1, anchor)
    grid.append(first_b)

    while True:
        pred = int(round(grid[-1] + cur_period))
        if pred >= n - int(0.04 * sr):
            break
        lo = max(grid[-1] + int(0.68 * T_beat), pred - tol)
        hi = min(n, pred + tol + 1)
        if hi <= lo:
            break
        win = score_env[lo:hi]
        peak_val = float(np.max(win))
        med_val = float(np.median(win))
        snapped = lo + int(np.argmax(win))
        if med_val > 1e-6 and peak_val >= 1.25 * med_val:
            step = float(snapped - grid[-1])
            cur_period = max(0.84 * T_beat, min(1.18 * T_beat, 0.85 * cur_period + 0.15 * step))
            grid.append(snapped)
        else:
            grid.append(pred)

    return np.array(grid, dtype=np.int64)


def extract_local_beat_samples(
    audio_seg: np.ndarray,
    bpm_hint: float,
    sr: int = TARGET_SR,
    allow_extrapolate: bool = True,
) -> np.ndarray:
    """
    Detects local beat timestamps in audio_seg and locks each beat onto
    the exact low-frequency (<180 Hz) kick drum transient energy.
    """
    n = len(audio_seg)
    if n < sr:
        return np.array([], dtype=np.int64)

    _, score_env = _compute_kick_envelope(audio_seg, sr=sr)
    mono = np.mean(audio_seg, axis=1).astype(np.float32) if audio_seg.ndim > 1 else audio_seg.astype(np.float32)

    tol = int(0.045 * sr)
    min_sep = int(0.28 * sr)
    beats_samples: List[int] = []
    try:
        import librosa

        mono_ds = mono[::2]
        sr_ds = sr // 2
        hint = float(bpm_hint) if 60.0 <= bpm_hint <= 200.0 else None
        _, beat_frames = librosa.beat.beat_track(y=mono_ds, sr=sr_ds, bpm=hint)
        if len(beat_frames) > 0:
            raw = librosa.frames_to_samples(beat_frames) * 2
            for b in raw:
                b_int = int(b)
                if 0 <= b_int < n:
                    lo = max(0, b_int - tol)
                    hi = min(n, b_int + tol + 1)
                    snapped = (lo + int(np.argmax(score_env[lo:hi]))) if hi > lo else b_int
                    if not beats_samples or (snapped - beats_samples[-1]) >= min_sep:
                        beats_samples.append(snapped)
    except Exception:
        pass

    raw_arr = np.array(beats_samples, dtype=np.int64)
    if len(raw_arr) >= 4:
        ibis = np.diff(raw_arr).astype(np.float64) / float(sr)
        raw_cv = float(np.std(ibis) / max(float(np.mean(ibis)), 1e-6))
        if raw_cv <= 0.22:
            return _regularize_kick_beatgrid(score_env, raw_arr, bpm_hint, sr, n)
        return raw_arr

    if allow_extrapolate and bpm_hint > 0:
        return _regularize_kick_beatgrid(score_env, raw_arr, bpm_hint, sr, n)

    return raw_arr


def _segment_pulse_contrast(audio_seg: np.ndarray, sr: int = TARGET_SR) -> float:
    """Measures percussive/kick pulse contrast (p95 / p50 of smoothed envelope)."""
    if len(audio_seg) < sr:
        return 1.0
    mono = np.mean(audio_seg, axis=1).astype(np.float32) if audio_seg.ndim > 1 else audio_seg.astype(np.float32)
    # Decimate ~10x for fast envelope measurement
    mono_ds = mono[::10]
    sr_ds = max(100, sr // 10)
    sos_lp = butter(2, 220.0, btype="lowpass", fs=sr_ds, output="sos")
    low = np.abs(sosfilt(sos_lp, mono_ds))
    step = max(1, int(0.025 * sr_ds))
    n_hops = len(low) // step
    if n_hops < 8:
        return 1.0
    low_rms = np.sqrt(np.mean(low[: n_hops * step].reshape(n_hops, step) ** 2, axis=1))
    full_rms = np.sqrt(np.mean(mono_ds[: n_hops * step].reshape(n_hops, step) ** 2, axis=1))
    c_low = float(np.percentile(low_rms, 95) / max(float(np.percentile(low_rms, 50)), 1e-6))
    c_full = float(np.percentile(full_rms, 95) / max(float(np.percentile(full_rms, 50)), 1e-6))
    return max(c_low, c_full)


def _assess_segment_percussion(
    seg: np.ndarray,
    bpm: float,
    sr: int = TARGET_SR,
) -> Tuple[bool, str, np.ndarray]:
    """Evaluates whether an audio window contains steady percussion sequences."""
    beats = extract_local_beat_samples(seg, bpm, sr=sr, allow_extrapolate=False)
    if len(beats) < 5:
        return False, "insufficient_beats", beats
    if _segment_pulse_contrast(seg, sr=sr) < 1.35:
        return False, "weak_rhythm_pulse", beats
    ibi = np.diff(beats).astype(np.float64) / float(sr)
    cv = float(np.std(ibi) / max(float(np.mean(ibi)), 1e-6))
    if cv > 0.16:
        return False, "unstable_beatgrid", beats
    return True, "steady_rhythm", beats


def assess_rhythmic_mix_suitability(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    min_drop_in_sec: float = 5.0,
) -> Tuple[bool, str, np.ndarray, np.ndarray]:
    """
    Checks whether both tracks have sufficient duration and steady beat pulses
    for a tight beat-locked Drop-In transition, returning cached local beat grids.
    """
    in_start = max(0, min(max(0, len(audio_in) - TARGET_SR), int(in_start_sample)))
    if len(out_tail) < int((min_drop_in_sec + 2.0) * TARGET_SR) or (len(audio_in) - in_start) < int(10.0 * TARGET_SR):
        return False, "short_tail_or_head", np.array([], dtype=np.int64), np.array([], dtype=np.int64)

    bpm = max(float(anchor_bpm), 80.0)
    out_scan = out_tail[: min(len(out_tail), 26 * TARGET_SR)]
    in_scan = audio_in[in_start : min(len(audio_in), in_start + 26 * TARGET_SR)]

    out_ok, out_reason, out_beats = _assess_segment_percussion(out_scan, bpm, sr=TARGET_SR)
    in_ok, in_reason, in_beats_rel = _assess_segment_percussion(in_scan, bpm, sr=TARGET_SR)
    in_beats = in_beats_rel + in_start

    if not out_ok:
        return False, f"out_{out_reason}", out_beats, in_beats
    if not in_ok:
        return False, in_reason, out_beats, in_beats

    return True, "steady_rhythm", out_beats, in_beats


def choose_transition_mode(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    transition_index: int,
    last_mode: Optional[str] = None,
    tempo_ratio: float = 1.0,
    min_drop_in_sec: float = 5.0,
) -> Tuple[str, str, np.ndarray, np.ndarray]:
    """
    Dynamically chooses between 'drop_in', 'crossfade', and 'echo_drop_in':
    - Selects 'echo_drop_in' when Track 1 lacks sufficient percussion sequences
      to beatmatch smoothly: picks the best melodic/vocal crest in Track 1's tail,
      fires a BPM-synced echo-out, and drops Track 2 cleanly in Drop-It mode.
    - Forces 'crossfade' when only Track 2 lacks rhythm, tempo stretch is wide
      (> 6.5%), or remaining tail/head audio is too short.
    - Otherwise alternates between 'drop_in' and 'crossfade' across the set.
    Returns (mode, reason, out_beats, in_beats).
    """
    rhythmic_ok, reason, out_beats, in_beats = assess_rhythmic_mix_suitability(
        out_tail, audio_in, in_start_sample, anchor_bpm, min_drop_in_sec=min_drop_in_sec
    )
    if not rhythmic_ok:
        in_start = max(0, min(max(0, len(audio_in) - TARGET_SR), int(in_start_sample)))
        has_room_for_echo_drop = (
            len(out_tail) >= int(4.5 * TARGET_SR)
            and (len(audio_in) - in_start) >= int(10.0 * TARGET_SR)
        )
        if reason.startswith("out_") and has_room_for_echo_drop:
            return "echo_drop_in", "out_low_percussion_echo_drop_it", out_beats, in_beats
        if reason == "short_tail_or_head" and has_room_for_echo_drop:
            bpm = max(float(anchor_bpm), 80.0)
            out_ok, _, checked_out_beats = _assess_segment_percussion(out_tail, bpm, sr=TARGET_SR)
            if not out_ok:
                return "echo_drop_in", "out_low_percussion_echo_drop_it", checked_out_beats, in_beats
        return "crossfade", reason, out_beats, in_beats

    if abs(float(tempo_ratio) - 1.0) > 0.085:
        return "echo_drop_in", "wide_bpm_echo_drop_it", out_beats, in_beats

    if len(out_beats) >= 4 and len(in_beats) >= 4:
        med_out = float(np.median(np.diff(out_beats)))
        med_in = float(np.median(np.diff(in_beats)))
        if abs((med_in / max(med_out, 1.0)) - 1.0) > 0.04:
            return "echo_drop_in", "wide_bpm_echo_drop_it", out_beats, in_beats

    # Dynamic alternation across the evening: drop_in -> crossfade -> drop_in -> ...
    if last_mode == "drop_in":
        return "crossfade", "dynamic_alternation", out_beats, in_beats
    if last_mode is None and transition_index > 1 and (transition_index % 2 == 0):
        return "crossfade", "dynamic_alternation", out_beats, in_beats
    return "drop_in", "beatlocked_drop_in", out_beats, in_beats


def _measure_rhythm_vocal_runway_sec(
    seg: np.ndarray,
    max_sec: float = 26.0,
    sr: int = TARGET_SR,
) -> Tuple[float, float]:
    """
    Estimates (rhythm_runway_sec, low_vocal_runway_sec) starting at seg[0]:
    - rhythm_runway_sec: how long steady drum/low-band groove holds without fading dead.
    - low_vocal_runway_sec: how long the segment stays percussive/instrumental before
      dense lead vocals spike above initial instrumental balance.
    """
    n = min(len(seg), int(max_sec * sr))
    if n < sr * 2:
        dur_s = len(seg) / float(sr)
        return dur_s, dur_s

    mono_ds = (np.mean(seg[:n], axis=1) if seg.ndim > 1 else seg[:n])[::4].astype(np.float32)
    sr_ds = max(1000, sr // 4)
    sos_drum = butter(2, [40.0, 220.0], btype="bandpass", fs=sr_ds, output="sos")
    sos_voc = butter(2, [400.0, 2800.0], btype="bandpass", fs=sr_ds, output="sos")
    d_sig = sosfilt(sos_drum, mono_ds)
    v_sig = sosfilt(sos_voc, mono_ds)

    step = max(1, int(0.5 * sr_ds))
    n_blk = len(mono_ds) // step
    if n_blk < 4:
        dur_s = n / float(sr)
        return dur_s, dur_s

    d_blk = np.sqrt(np.mean(d_sig[: n_blk * step].reshape(n_blk, step) ** 2, axis=1))
    v_blk = np.sqrt(np.mean(v_sig[: n_blk * step].reshape(n_blk, step) ** 2, axis=1))

    ref_drum = float(np.median(d_blk[: min(len(d_blk), 24)]))
    drum_floor = max(0.010, ref_drum * 0.35)

    rhythm_blks = 0
    gap = 0
    for idx_b, val in enumerate(d_blk):
        if float(val) >= drum_floor:
            rhythm_blks += 1
            gap = 0
        elif idx_b < 2:
            # Ignore a brief soft pickup before first kick drops in
            rhythm_blks += 1
        elif gap == 0 and rhythm_blks >= 4:
            rhythm_blks += 1
            gap = 1
        else:
            break

    ratios = v_blk / (d_blk + 1e-4)
    init_ratio = max(0.35, float(np.median(ratios[: min(6, len(ratios))])))
    vocal_spike_thresh = max(1.85, init_ratio * 2.25)

    clean_blks = 0
    consec_voc = 0
    for i, r in enumerate(ratios):
        if i >= 4 and float(r) > vocal_spike_thresh and float(v_blk[i]) > 0.035:
            consec_voc += 1
            if consec_voc >= 2:
                clean_blks = max(2, i - 1)
                break
        else:
            consec_voc = 0
        clean_blks = i + 1

    return round(rhythm_blks * 0.5, 2), round(clean_blks * 0.5, 2)


def assess_adaptive_transition_pace(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    tempo_ratio: float = 1.0,
    out_beats: Optional[np.ndarray] = None,
    in_beats: Optional[np.ndarray] = None,
    sr: int = TARGET_SR,
) -> Tuple[str, float, float, str]:
    """
    Decides dynamically whether the DJ transition should be:
    - 'long'   (12-21s / 24-40+ beats): classic 80s Concierto extended ride when both tracks
               hold steady rhythmic pulse & low vocal clash over a long runway.
    - 'medium' (8.2-11.2s / 16-20 beats): balanced default groove transition.
    - 'quick'  (4.2-6.2s / 8-12 beats): crisp tight mix when vocals arrive early, tail is short,
               or local rhythm has higher jitter / wide tempo stretch.
    Returns (pace, target_sync_sec, hold_sec, reason).
    """
    in_start = max(0, min(max(0, len(audio_in) - sr), int(in_start_sample)))
    out_dur_s = len(out_tail) / float(sr)
    in_rem_s = (len(audio_in) - in_start) / float(sr)

    if out_dur_s < 6.5 or in_rem_s < 12.0:
        return "quick", min(5.5, max(3.5, out_dur_s * 0.80)), min(2.5, max(1.8, out_dur_s * 0.42)), "short_segment"

    out_rhythm_s, _ = _measure_rhythm_vocal_runway_sec(out_tail, max_sec=26.0, sr=sr)
    in_rhythm_s, in_clean_s = _measure_rhythm_vocal_runway_sec(audio_in[in_start:], max_sec=26.0, sr=sr)

    out_avail = min(out_dur_s, out_rhythm_s)
    in_avail = min(max(0.0, in_rem_s - 4.0), in_rhythm_s, in_clean_s)
    shared_runway = min(out_avail, in_avail)

    bpm = max(float(anchor_bpm), 80.0)
    ob = out_beats if out_beats is not None and len(out_beats) >= 2 else extract_local_beat_samples(
        out_tail[: min(len(out_tail), 26 * sr)], bpm, sr=sr, allow_extrapolate=False
    )
    ib_raw = in_beats if in_beats is not None and len(in_beats) >= 2 else extract_local_beat_samples(
        audio_in[in_start : min(len(audio_in), in_start + 26 * sr)], bpm, sr=sr, allow_extrapolate=False
    ) + in_start

    out_cv = 0.0
    in_cv = 0.0
    if len(ob) >= 4:
        ibi = np.diff(ob).astype(np.float64) / sr
        out_cv = float(np.std(ibi) / max(float(np.mean(ibi)), 1e-6))
    else:
        out_cv = 0.25
    if len(ib_raw) >= 4:
        ibi = np.diff(ib_raw).astype(np.float64) / sr
        in_cv = float(np.std(ibi) / max(float(np.mean(ibi)), 1e-6))
    else:
        in_cv = 0.25

    max_cv = max(out_cv, in_cv)
    stretch_diff = abs(float(tempo_ratio) - 1.0)

    if shared_runway < 7.2 or out_dur_s < 8.0 or max_cv > 0.135 or stretch_diff > 0.07:
        t_sec = min(6.2, max(4.2, shared_runway * 0.85))
        h_sec = min(3.0, max(2.0, t_sec * 0.50))
        return "quick", round(t_sec, 2), round(h_sec, 2), f"quick(runway={shared_runway:.1f}s,cv={max_cv:.2f})"

    if (
        shared_runway >= 12.5
        and out_dur_s >= 14.0
        and max_cv <= 0.115
        and stretch_diff <= 0.055
        and min(len(ob), len(ib_raw)) >= 18
    ):
        t_sec = min(21.0, max(12.0, shared_runway * 0.85))
        h_sec = min(10.0, max(6.0, t_sec * 0.52))
        return "long", round(t_sec, 2), round(h_sec, 2), f"long_80s_ride(runway={shared_runway:.1f}s,cv={max_cv:.2f})"

    t_sec = min(11.2, max(8.2, shared_runway * 0.88))
    h_sec = min(5.0, max(3.5, t_sec * 0.52))
    return "medium", round(t_sec, 2), round(h_sec, 2), f"medium(runway={shared_runway:.1f}s,cv={max_cv:.2f})"


def _stretch_sync_segment(seg: np.ndarray, target_len: int) -> np.ndarray:
    """Micro-stretches a stereo segment cleanly to exact target_len samples preserving kick transients."""
    if target_len <= 0:
        return np.zeros((0, seg.shape[1] if seg.ndim > 1 else 2), dtype=np.float32)
    if len(seg) == target_len:
        return seg.astype(np.float32)
    if len(seg) <= 1:
        return np.repeat(seg[:1].astype(np.float32), target_len, axis=0)

    try:
        import soxr

        out = soxr.resample(seg.astype(np.float32), float(len(seg)), float(target_len), quality="HQ")
        if len(out) > target_len:
            return out[:target_len].astype(np.float32)
        if len(out) < target_len:
            pad = np.repeat(out[-1:], target_len - len(out), axis=0)
            return np.concatenate([out, pad], axis=0).astype(np.float32)
        return out.astype(np.float32)
    except Exception:
        x_old = np.linspace(0.0, 1.0, len(seg), dtype=np.float64)
        x_new = np.linspace(0.0, 1.0, target_len, dtype=np.float64)
        chans = [np.interp(x_new, x_old, seg[:, c]) for c in range(seg.shape[1])]
        return np.stack(chans, axis=1).astype(np.float32)


def render_drop_in_transition(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    min_sync_sec: float = 5.0,
    precomputed_out_beats: Optional[np.ndarray] = None,
    precomputed_in_beats: Optional[np.ndarray] = None,
    target_sync_sec: Optional[float] = None,
) -> Tuple[np.ndarray, int]:
    """
    Pro DJ Drop-In transition with local beatgrid phase locking & gradual fade-out:
    - Adapts phrase length cleanly from quick (~4.5-6s / 8-12 beats) and medium (~8-11s)
      up to extended 80s long rides (~12-21s / 24-40 beats) when target_sync_sec requests it.
    - Incoming track drops in cleanly on Beat 0 at 80% volume (15ms micro-attack + 200Hz HPF)
      and increases gradually up to 100% volume.
    - Outgoing track stays at 100% volume for at least min_sync_sec (e.g. 3s quick, 5s medium,
      6-10s long ride), then lowers gradually down to silence.
    Returns (transition_audio, resume_sample_in_audio_in).
    """
    in_start = max(0, min(max(0, len(audio_in) - TARGET_SR), int(in_start_sample)))
    if len(out_tail) < TARGET_SR or (len(audio_in) - in_start) < int(min_sync_sec * TARGET_SR):
        return out_tail.astype(np.float32), in_start

    bpm = max(float(anchor_bpm), 80.0)
    beat_sec = 60.0 / bpm
    goal_sec = float(target_sync_sec) if target_sync_sec is not None else max(8.2, min_sync_sec + 3.2)
    sync_beats = 40
    for cand_beats in (8, 12, 16, 20, 24, 28, 32, 36, 40):
        if cand_beats * beat_sec >= goal_sec:
            sync_beats = cand_beats
            break
    nominal_sync = int(round(max(min_sync_sec + 1.5, sync_beats * beat_sec) * TARGET_SR))
    min_sync_samples = int(math.ceil(min_sync_sec * TARGET_SR))

    scan_len = max(15, int(math.ceil(goal_sec)) + 4) * TARGET_SR
    out_scan_len = min(len(out_tail), scan_len)
    if precomputed_out_beats is not None and len(precomputed_out_beats) >= 2:
        out_beats = precomputed_out_beats
    else:
        out_beats = extract_local_beat_samples(out_tail[:out_scan_len], bpm, sr=TARGET_SR)

    in_scan_end = min(len(audio_in), in_start + scan_len)
    if precomputed_in_beats is not None and len(precomputed_in_beats) >= 2:
        in_beats = precomputed_in_beats
    else:
        in_beats_rel = extract_local_beat_samples(audio_in[in_start:in_scan_end], bpm, sr=TARGET_SR)
        in_beats = in_beats_rel + in_start

    # Choose out_beat_0 near start of out_tail that leaves enough room for 3s hold + gradual fadeout
    out_beat_0 = 0
    sync_samples = min(len(out_tail), nominal_sync)
    for i, b in enumerate(out_beats):
        b_int = int(b)
        if i + sync_beats < len(out_beats):
            cand_len = int(out_beats[i + sync_beats]) - b_int
            if cand_len >= min_sync_samples and (b_int + cand_len) <= len(out_tail):
                out_beat_0 = b_int
                sync_samples = cand_len
                break
        if (len(out_tail) - b_int) >= nominal_sync:
            out_beat_0 = b_int
            sync_samples = nominal_sync
            break

    if sync_samples < min_sync_samples and len(out_tail) >= min_sync_samples:
        out_beat_0 = 0
        sync_samples = min(len(out_tail), nominal_sync)

    # Lock incoming beat 0 -> beat K onto [out_beat_0 .. out_beat_0 + sync_samples]
    in_beat_0 = int(in_beats[0]) if len(in_beats) > 0 else in_start
    if in_beat_0 >= len(audio_in) - max(sync_samples, TARGET_SR * 4):
        in_beat_0 = in_start

    src_sync_len = sync_samples
    if len(in_beats) > sync_beats:
        cand_in_k = int(in_beats[sync_beats])
        cand_src_len = cand_in_k - in_beat_0
        local_ratio = cand_src_len / max(1, sync_samples)
        if 0.96 <= local_ratio <= 1.04 and (in_beat_0 + cand_src_len) < (len(audio_in) - TARGET_SR * 2):
            src_sync_len = cand_src_len

    # Ensure source slice stays valid inside audio_in
    max_avail_in = max(1, len(audio_in) - in_beat_0 - TARGET_SR)
    if src_sync_len > max_avail_in:
        src_sync_len = min(sync_samples, len(audio_in) - in_beat_0)
        sync_samples = min(sync_samples, src_sync_len)

    if sync_samples <= 0 or src_sync_len <= 0:
        return out_tail.astype(np.float32), in_beat_0

    prefix = out_tail[:out_beat_0].astype(np.float32) if out_beat_0 > 0 else np.zeros((0, 2), dtype=np.float32)
    out_sync = out_tail[out_beat_0 : out_beat_0 + sync_samples].astype(np.float32)
    in_sync, resume_sample = _stretch_sync_by_beats(
        out_sync,
        audio_in,
        in_beat_0=in_beat_0,
        out_beat_0=out_beat_0,
        out_beats=out_beats,
        in_beats=in_beats,
        sync_beats=sync_beats,
        sync_samples=sync_samples,
        src_sync_len=src_sync_len,
        sr=TARGET_SR,
    )

    in_hp = apply_highpass_transition(in_sync, cutoff_hz=200.0)
    out_hp = apply_highpass_transition(out_sync, cutoff_hz=220.0)

    # Outgoing holds full punch for >= min_sync_sec (3.0s), then descends gradually to 0
    if sync_samples > min_sync_samples + int(0.8 * TARGET_SR):
        hold_samples = min_sync_samples
    else:
        hold_samples = int(0.50 * sync_samples)
    hold_frac = min(0.75, hold_samples / float(sync_samples))

    t = np.linspace(0.0, 1.0, sync_samples, dtype=np.float32)[:, None]

    # Incoming track: enters in Drop-In at 80% volume (0.80) on Beat 0 and ramps smoothly up to 100% (1.00)
    attack_len = max(1, int(0.015 * TARGET_SR))
    in_attack = np.ones((sync_samples, 1), dtype=np.float32)
    n_att = min(attack_len, sync_samples)
    in_attack[:n_att, 0] = np.linspace(0.0, 1.0, n_att, dtype=np.float32)

    in_vol_u = np.clip(t / 0.85, 0.0, 1.0)
    in_gain = (0.80 + 0.20 * 0.5 * (1.0 - np.cos(math.pi * in_vol_u))) * in_attack

    open_start = max(0.20, hold_frac - 0.06)
    open_span = max(0.15, min(0.28, 0.90 - open_start))
    in_open = np.clip((t - open_start) / open_span, 0.0, 1.0)
    in_blended = ((1.0 - in_open) * in_hp + in_open * in_sync) * in_gain

    # Outgoing track: stays at 1.0 for hold_frac (>= 3.0s), then gradually lowers volume via raised-cosine curve + gentle HPF
    fade_u = np.clip((t - hold_frac) / max(1e-4, 1.0 - hold_frac), 0.0, 1.0)
    out_gain = 0.5 * (1.0 + np.cos(math.pi * fade_u))
    out_hpf_mix = np.clip(fade_u / 0.45, 0.0, 1.0)
    out_blended = ((1.0 - out_hpf_mix) * out_sync + out_hpf_mix * out_hp) * out_gain

    mixed_sync = out_blended + in_blended
    peak = float(np.max(np.abs(mixed_sync)))
    if peak > 0.95:
        mixed_sync = np.tanh(mixed_sync / 0.95) * 0.95

    transition = np.concatenate([prefix, mixed_sync.astype(np.float32)], axis=0) if len(prefix) > 0 else mixed_sync.astype(np.float32)
    return transition, resume_sample


SUPPORTED_DJ_EFFECTS = ("none", "jet_flanger", "echo_out", "filter_sweep", "vinyl_brake")


def apply_jet_flanger(
    audio: np.ndarray,
    bpm: float = 120.0,
    sr: int = TARGET_SR,
    feedback: float = 0.65,
    depth_ms: float = 3.8,
    wet: float = 0.62,
) -> np.ndarray:
    """
    80s DJ Jet/Flanger DSP effect:
    LFO-modulated short delay (0.5ms .. 4.3ms) with ~65% feedback resonance,
    ideal on instrumental builds or just before a Drop-In.
    """
    n = len(audio)
    if n < sr // 4:
        return audio.astype(np.float32)

    arr = audio.astype(np.float32)
    idx = np.arange(n, dtype=np.float64)
    lfo_hz = max(0.22, min(0.85, float(bpm) / 480.0))
    t_sec = idx / float(sr)
    min_d = 0.0005 * sr
    max_d = (0.0005 + max(0.001, depth_ms * 0.001)) * sr
    delay = min_d + 0.5 * (max_d - min_d) * (1.0 - np.cos(2.0 * math.pi * lfo_hz * t_sec))

    fb = max(0.0, min(0.78, float(feedback)))
    chans_out = []
    for c in range(arr.shape[1]):
        x = arr[:, c].astype(np.float64)
        comb = np.zeros(n, dtype=np.float64)
        # Vectorized Neumann feedback series: fb + fb^2 D + ... (8 passes -> <-30dB error)
        cur = x
        weight = fb
        for _ in range(8):
            pos = np.clip(idx - delay, 0.0, float(n - 1))
            cur = np.interp(pos, idx, cur)
            comb += weight * cur
            weight *= fb
        y = x * (1.0 - 0.35 * wet) + wet * comb
        chans_out.append(y)

    out = np.stack(chans_out, axis=1).astype(np.float32)
    return _apply_soft_peak_ceiling(out, knee_start=0.76, ceiling=0.95)


def apply_echo_out(
    audio: np.ndarray,
    bpm: float = 120.0,
    tail_pad_samples: int = 0,
    beats_window: float = 3.0,
    subdiv: float = 0.5,
    feedback: float = 0.52,
    sr: int = TARGET_SR,
) -> Tuple[np.ndarray, float]:
    """
    BPM-synchronized Echo-Out DSP effect:
    - Computes exact tempo delay: delay_ms = (60000 / bpm) * subdiv
      (subdiv=1.0 -> quarter note, subdiv=0.5 -> eighth note 'croche' = 30000 / bpm).
    - Captures the last 2..4 beats before cutoff and lets decaying warm low-passed
      echoes ring across tail_pad_samples.
    Returns (processed_audio_of_len_N_plus_pad, delay_ms).
    """
    eff_bpm = max(60.0, min(200.0, float(bpm)))
    delay_ms = (60000.0 / eff_bpm) * float(subdiv)
    delay_samp = max(1, int(round((delay_ms / 1000.0) * sr)))
    n = len(audio)
    if n < sr // 2:
        padded = np.vstack([audio.astype(np.float32), np.zeros((max(0, tail_pad_samples), audio.shape[1]), dtype=np.float32)])
        return padded, delay_ms

    beat_sec = 60.0 / eff_bpm
    win_samp = min(n, max(delay_samp, int(round(max(2.0, min(4.0, beats_window)) * beat_sec * sr))))
    pad = max(0, int(tail_pad_samples))
    out = np.zeros((n + pad, audio.shape[1]), dtype=np.float32)
    out[:n] = audio.astype(np.float32)

    # Smoothly fade dry audio over last 0.45 * win_samp down to cutoff at n
    dry_fade = min(win_samp, max(int(0.15 * sr), int(win_samp * 0.45)))
    out[n - dry_fade : n] *= np.linspace(1.0, 0.0, dry_fade, dtype=np.float32)[:, None]

    src_echo = audio[n - win_samp : n].astype(np.float32).copy()
    att = min(int(0.020 * sr), len(src_echo) // 4)
    if att > 0:
        src_echo[:att] *= np.linspace(0.0, 1.0, att, dtype=np.float32)[:, None]
        src_echo[-att:] *= np.linspace(1.0, 0.0, att, dtype=np.float32)[:, None]

    # Classic analog 80s BBD low-pass warmth (~3.2 kHz) per tap
    sos_lp = butter(2, 3200.0, btype="lowpass", fs=sr, output="sos")
    tap = src_echo
    fb = max(0.25, min(0.75, float(feedback)))
    gain = fb
    start_anchor = n - win_samp
    for k in range(1, 6):
        tap = sosfilt(sos_lp, tap, axis=0).astype(np.float32)
        pos = start_anchor + k * delay_samp
        if pos >= len(out):
            break
        copy_len = min(len(tap), len(out) - pos)
        if copy_len > 0:
            out[pos : pos + copy_len] += tap[:copy_len] * np.float32(gain)
        gain *= fb

    out = _apply_soft_peak_ceiling(out, knee_start=0.78, ceiling=0.95)
    return out, delay_ms


def apply_filter_sweep(
    audio: np.ndarray,
    direction: str = "highpass",
    start_hz: float = 70.0,
    end_hz: float = 2200.0,
    sr: int = TARGET_SR,
) -> np.ndarray:
    """
    Progressive frequency filter sweep (highpass or lowpass) using overlapping
    crossfaded Butterworth SOS stages so cutoff sweeps smoothly with zero clicks.
    """
    n = len(audio)
    if n < sr // 4:
        return audio.astype(np.float32)

    btype = "lowpass" if str(direction).lower() == "lowpass" else "highpass"
    n_bands = 14
    s_hz = max(30.0, min(sr * 0.42, float(start_hz)))
    e_hz = max(30.0, min(sr * 0.42, float(end_hz)))
    cutoffs = np.geomspace(s_hz, e_hz, n_bands)

    arr = audio.astype(np.float32)
    step = n / float(n_bands)
    half_win = max(int(step), int(0.06 * sr))
    out = np.zeros_like(arr, dtype=np.float32)
    weight_sum = np.zeros((n, 1), dtype=np.float32)

    for b_idx, fc in enumerate(cutoffs):
        sos = butter(2, float(fc), btype=btype, fs=sr, output="sos")
        # Run filter slightly ahead of window so transient settles cleanly
        center = int((b_idx + 0.5) * step)
        lo = max(0, center - half_win)
        hi = min(n, center + half_win)
        pre = max(0, lo - int(0.05 * sr))
        filt_full = sosfilt(sos, arr[pre:hi], axis=0).astype(np.float32)
        seg = filt_full[lo - pre :]

        w_len = hi - lo
        if b_idx == 0:
            w = np.linspace(1.0, 0.0, w_len, dtype=np.float32)[:, None] if n_bands > 1 else np.ones((w_len, 1), dtype=np.float32)
            # Keep full weight across left half of first window
            mid = w_len // 2
            w[:mid] = 1.0
            if w_len - mid > 1:
                u = np.linspace(0.0, 1.0, w_len - mid, dtype=np.float32)[:, None]
                w[mid:] = 0.5 * (1.0 + np.cos(math.pi * u))
        elif b_idx == n_bands - 1:
            w = np.ones((w_len, 1), dtype=np.float32)
            mid = w_len // 2
            if mid > 1:
                u = np.linspace(0.0, 1.0, mid, dtype=np.float32)[:, None]
                w[:mid] = 0.5 * (1.0 - np.cos(math.pi * u))
        else:
            u = np.linspace(0.0, 1.0, w_len, dtype=np.float32)[:, None]
            w = 0.5 * (1.0 - np.cos(2.0 * math.pi * u))

        out[lo:hi] += seg * w
        weight_sum[lo:hi] += w

    out = out / np.maximum(weight_sum, 1e-5)
    return _apply_soft_peak_ceiling(out, knee_start=0.78, ceiling=0.95)


def apply_vinyl_brake(
    audio: np.ndarray,
    brake_sec: float = 0.65,
    sr: int = TARGET_SR,
) -> np.ndarray:
    """
    Turntable vinyl-brake DSP effect:
    Rapidly slows down pitch & tempo on the final ~0.5-0.8s right before cutoff
    to create an authentic Technics SL-1200 power-off stop before a hard Drop-In.
    """
    n = len(audio)
    brake_len = min(n, max(int(0.25 * sr), int(round(max(0.45, min(0.95, float(brake_sec))) * sr))))
    if brake_len < int(0.2 * sr):
        return audio.astype(np.float32)

    out = audio.astype(np.float32).copy()
    start = n - brake_len
    tail = out[start:].astype(np.float64)

    u = np.linspace(0.0, 1.0, brake_len, dtype=np.float64)
    # Slowdown curve: rate starts near 1.0 and drops smoothly to ~0.07 at stop
    rate = np.maximum(0.07, 1.0 - 0.93 * (u ** 1.35))
    src_pos = np.cumsum(rate)
    src_pos = np.clip(src_pos - src_pos[0], 0.0, float(brake_len - 1))

    base_idx = np.arange(brake_len, dtype=np.float64)
    braked = np.stack([np.interp(src_pos, base_idx, tail[:, c]) for c in range(out.shape[1])], axis=1).astype(np.float32)

    fade_n = min(int(0.055 * sr), brake_len // 4)
    if fade_n > 0:
        braked[-fade_n:] *= np.linspace(1.0, 0.0, fade_n, dtype=np.float32)[:, None]

    out[start:] = braked
    return out


def choose_dynamic_effect(
    transition_mode: str,
    transition_index: int,
    energy_jump: float = 0.0,
) -> str:
    """Selects a musical 80s DJ DSP effect dynamically when effect='auto'."""
    if transition_mode == "drop_in":
        cycle = ("jet_flanger", "filter_sweep", "vinyl_brake", "jet_flanger")
        if energy_jump >= 0.18 and transition_index > 1:
            return "vinyl_brake"
        return cycle[(max(1, transition_index) - 1) % len(cycle)]
    cycle_xf = ("filter_sweep", "echo_out", "none", "filter_sweep")
    return cycle_xf[(max(1, transition_index) - 1) % len(cycle_xf)]


def find_best_jingle_spot_in_body(
    body_seg: np.ndarray,
    jingle_len: int,
    min_offset_sec: float = 4.0,
    sr: int = TARGET_SR,
) -> int:
    """
    Finds the lowest vocal-presence instrumental intro window in body_seg
    (between min_offset_sec and ~14s) for clean radio singel placement.
    """
    max_start = len(body_seg) - jingle_len - int(2.0 * sr)
    min_start = min(max_start, int(min_offset_sec * sr))
    if max_start <= min_start:
        return max(TARGET_SR, min(max_start, int(min_offset_sec * sr)))

    search_hi = min(max_start, int(14.0 * sr))
    step = int(0.5 * sr)
    mono = np.mean(body_seg[: search_hi + min(jingle_len, 8 * sr)], axis=1).astype(np.float32)
    sos_voc = butter(2, [350.0, 3000.0], btype="bandpass", fs=sr, output="sos")
    sos_low = butter(2, 220.0, btype="lowpass", fs=sr, output="sos")
    voc_env = np.abs(sosfilt(sos_voc, mono))
    low_env = np.abs(sosfilt(sos_low, mono))

    eval_len = min(jingle_len, int(6.0 * sr))
    best_start = min_start
    best_score = 1e9
    for pos in range(min_start, search_hi + 1, step):
        v = float(np.mean(voc_env[pos : pos + eval_len]))
        l = float(np.mean(low_env[pos : pos + eval_len])) + 1e-6
        score = v / l + (pos / float(sr)) * 0.015
        if score < best_score:
            best_score = score
            best_start = pos
    return best_start


def find_best_echo_cue_in_tail(
    out_tail: np.ndarray,
    min_lead_sec: float = 1.5,
    max_search_sec: float = 8.0,
    sr: int = TARGET_SR,
) -> int:
    """
    Finds the best musical phrase crest in a low-percussion Track 1 outro window
    to trigger a BPM-synced Echo-Out right before a natural breathing dip.
    Returns sample index inside out_tail.
    """
    n = len(out_tail)
    if n <= int(1.6 * sr):
        return max(1, int(round(0.75 * n)))

    search_end = min(n, int(round(max_search_sec * sr)))
    hop = max(1, int(round(0.05 * sr)))
    n_hops = search_end // hop
    if n_hops < 8:
        return min(n, max(int(0.8 * sr), int(round(0.60 * n))))

    mono = (
        np.mean(out_tail[: n_hops * hop], axis=1)
        if out_tail.ndim > 1
        else out_tail[: n_hops * hop]
    ).astype(np.float32)
    frames = mono.reshape(n_hops, hop)
    env = np.sqrt(np.mean(frames * frames, axis=1) + 1e-12)
    kernel = np.ones(5, dtype=np.float32) / 5.0
    env_smooth = np.convolve(env, kernel, mode="same")

    lo_k = max(1, min(n_hops - 3, int(round(min_lead_sec / 0.05))))
    hi_k = max(lo_k, min(n_hops - 2, n_hops - int(round(0.45 / 0.05))))

    best_k = lo_k
    best_score = -1.0
    for k in range(lo_k, hi_k + 1):
        crest = float(env_smooth[k])
        f_lo = min(n_hops - 1, k + 2)
        f_hi = min(n_hops, k + 14)
        following = float(np.mean(env_smooth[f_lo:f_hi])) if f_hi > f_lo else crest
        dip_ratio = 1.0 - min(1.0, following / max(crest, 1e-6))
        t_sec = k * 0.05
        timing_bonus = 1.0 - 0.03 * abs(t_sec - 4.0)
        score = crest * (1.0 + 0.35 * dip_ratio) * max(0.80, timing_bonus)
        if score > best_score:
            best_score = score
            best_k = k

    cue_sample = int(best_k * hop)
    return max(min(n, int(0.8 * sr)), min(n, cue_sample))


def render_echo_drop_it_transition(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    precomputed_in_beats: Optional[np.ndarray] = None,
) -> Tuple[np.ndarray, int, str]:
    """
    When Track 1 lacks percussion sequences for beatmatched blending:
    - Finds the best phrase crest in Track 1's outro,
    - Fires a BPM-synced warm analog echo-out tail from that cue point,
    - Drops Track 2 in Drop-It style (zero-click 15ms micro-attack + 0.85->1.00 punch)
      right on the cue while the Track 1 echoes decay smoothly overhead.
    Returns (transition_audio, resume_sample, note).
    """
    bpm = max(float(anchor_bpm), 80.0)
    beat_sec = 60.0 / bpm
    cue_sample = find_best_echo_cue_in_tail(out_tail, sr=TARGET_SR)
    echo_tail_sec = max(2.5, min(3.6, 6.0 * beat_sec))
    echo_tail_samples = int(round(echo_tail_sec * TARGET_SR))

    out_prefix = out_tail[:cue_sample].astype(np.float32)
    if out_prefix.ndim == 1:
        out_prefix = np.stack([out_prefix, out_prefix], axis=1)
    echo_out_tail, delay_ms = apply_echo_out(
        out_prefix,
        bpm=bpm,
        tail_pad_samples=echo_tail_samples,
        beats_window=2.5,
        subdiv=0.5,
        feedback=0.54,
        sr=TARGET_SR,
    )

    in_start = max(0, min(max(0, len(audio_in) - TARGET_SR), int(in_start_sample)))
    in_beat_0 = in_start
    if precomputed_in_beats is not None and len(precomputed_in_beats) > 0:
        cand_beat = int(precomputed_in_beats[0])
        if in_start <= cand_beat <= in_start + 3 * TARGET_SR and (cand_beat + echo_tail_samples + TARGET_SR) <= len(audio_in):
            in_beat_0 = cand_beat
    else:
        cand_beat = snap_beat_sample(audio_in, in_start / float(TARGET_SR))
        if (cand_beat + echo_tail_samples + TARGET_SR) <= len(audio_in):
            in_beat_0 = cand_beat

    avail_in = max(0, len(audio_in) - in_beat_0)
    overlap_len = min(echo_tail_samples, max(0, len(echo_out_tail) - cue_sample), avail_in)
    if overlap_len <= 0:
        return echo_out_tail.astype(np.float32), in_beat_0, f"echo_drop_in+echo_out({delay_ms:.0f}ms)"

    in_seg = audio_in[in_beat_0 : in_beat_0 + overlap_len].astype(np.float32).copy()
    if in_seg.ndim == 1:
        in_seg = np.stack([in_seg, in_seg], axis=1)

    gain = np.ones((overlap_len, 1), dtype=np.float32)
    att_n = min(overlap_len, max(1, int(0.015 * TARGET_SR)))
    punch_n = min(overlap_len, max(att_n + 1, int(0.60 * TARGET_SR)))
    gain[:att_n, 0] = np.linspace(0.0, 0.85, att_n, dtype=np.float32)
    if punch_n > att_n:
        gain[att_n:punch_n, 0] = np.linspace(0.85, 1.0, punch_n - att_n, dtype=np.float32)

    trans = echo_out_tail.astype(np.float32).copy()
    trans[cue_sample : cue_sample + overlap_len] += in_seg * gain
    trans = _apply_soft_peak_ceiling(trans, knee_start=0.76, ceiling=0.95)
    resume_sample = in_beat_0 + overlap_len
    note = f"echo_drop_in+echo_out({delay_ms:.0f}ms)@{cue_sample / float(TARGET_SR):.2f}s"
    return trans, resume_sample, note


def render_vinyl_brake_echo_transition(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    transition_mode: str = "drop_in",
    precomputed_out_beats: Optional[np.ndarray] = None,
    precomputed_in_beats: Optional[np.ndarray] = None,
) -> Tuple[np.ndarray, int, str]:
    """
    Technics-style Vinyl Brake + Immediate Echo-Only Handoff:
    1. Plays outgoing out_tail up to a clean beat stop cue and fires the 0.62s turntable slowdown.
    2. Cuts dry Track 1 completely at the stop (zero beat clash!), applying BPM-synced
       analog echoes of the vinyl brake sweep itself starting right on the stop.
    3. Drops Track 2 cleanly on Beat 0 at the exact stop point and lets Track 2 continue
       at steady tempo while ONLY the vinyl-stop echo decays smoothly overhead.
    """
    bpm = max(float(anchor_bpm), 80.0)
    beat_sec = 60.0 / bpm
    sr = TARGET_SR

    # Choose beat-aligned vinyl brake stop cue inside [2.0s .. 4.5s] of out_tail
    cue_sample = min(len(out_tail), max(int(1.8 * sr), int(round(4.0 * beat_sec * sr))))
    if precomputed_out_beats is not None and len(precomputed_out_beats) >= 3:
        for b in precomputed_out_beats:
            b_i = int(b)
            if int(1.8 * sr) <= b_i <= min(len(out_tail), int(4.5 * sr)):
                cue_sample = b_i
                break

    brake_sec = 0.62
    out_pre = out_tail[:cue_sample].astype(np.float32)
    if out_pre.ndim == 1:
        out_pre = np.stack([out_pre, out_pre], axis=1)
    braked_pre = apply_vinyl_brake(out_pre, brake_sec=brake_sec, sr=sr)

    # Extract ONLY the braked slowdown segment itself for echoing (no dry beat carryover!)
    brake_len = min(len(braked_pre), int(round(brake_sec * sr)))
    echo_src = braked_pre[len(braked_pre) - brake_len :].copy()
    att = min(int(0.020 * sr), len(echo_src) // 4)
    if att > 0:
        echo_src[:att] *= np.linspace(0.0, 1.0, att, dtype=np.float32)[:, None]

    delay_samp = max(int(0.12 * sr), min(int(0.85 * sr), int(round(0.5 * beat_sec * sr))))
    delay_ms = (delay_samp * 1000.0) / float(sr)
    echo_span = max(int(2.6 * sr), 5 * delay_samp + brake_len)

    # Incoming Track 2 starts cleanly at cue_sample on Beat 0 and plays uninterrupted
    in_start = max(0, min(max(0, len(audio_in) - sr), int(in_start_sample)))
    in_beat_0 = in_start
    if precomputed_in_beats is not None and len(precomputed_in_beats) > 0:
        cand = int(precomputed_in_beats[0])
        if in_start <= cand <= in_start + 3 * sr:
            in_beat_0 = cand
    else:
        cand = snap_beat_sample(audio_in, in_start / float(sr), bpm_hint=bpm)
        if in_start <= cand <= in_start + 3 * sr:
            in_beat_0 = cand

    avail_in = max(0, len(audio_in) - in_beat_0)
    play_in_len = min(avail_in, max(echo_span, int(5.4 * sr)))
    if play_in_len <= 0:
        return braked_pre, in_beat_0, f"{transition_mode}+vinyl_brake+echo({delay_ms:.0f}ms)"

    total_len = cue_sample + play_in_len
    trans = np.zeros((total_len, 2), dtype=np.float32)
    trans[:cue_sample] = braked_pre

    # Place low-passed BPM echoes of the vinyl stop starting right at cue_sample
    sos_lp = butter(2, 3000.0, btype="lowpass", fs=sr, output="sos")
    tap = echo_src
    gain = 0.56
    for k in range(5):
        tap = sosfilt(sos_lp, tap, axis=0).astype(np.float32)
        pos = cue_sample + k * delay_samp
        if pos >= total_len:
            break
        copy_n = min(len(tap), total_len - pos)
        if copy_n > 0:
            trans[pos : pos + copy_n] += tap[:copy_n] * np.float32(gain)
        gain *= 0.56

    # Drop Track 2 cleanly on Beat 0 right after vinyl stop & let it continue steadily
    in_seg = audio_in[in_beat_0 : in_beat_0 + play_in_len].astype(np.float32).copy()
    if in_seg.ndim == 1:
        in_seg = np.stack([in_seg, in_seg], axis=1)
    att_n = min(len(in_seg), max(1, int(0.012 * sr)))
    in_seg[:att_n] *= np.linspace(0.0, 1.0, att_n, dtype=np.float32)[:, None]

    trans[cue_sample : cue_sample + play_in_len] += in_seg
    trans = _apply_soft_peak_ceiling(trans, knee_start=0.76, ceiling=0.95)
    resume_sample = in_beat_0 + play_in_len
    note = f"{transition_mode}+vinyl_brake+echo({delay_ms:.0f}ms)@{cue_sample / float(sr):.2f}s"
    return trans, resume_sample, note


def render_transition_with_effect(
    out_tail: np.ndarray,
    audio_in: np.ndarray,
    in_start_sample: int,
    anchor_bpm: float,
    transition_mode: str = "drop_in",
    effect: str = "none",
    precomputed_out_beats: Optional[np.ndarray] = None,
    precomputed_in_beats: Optional[np.ndarray] = None,
    min_drop_in_sec: float = 5.0,
    target_sync_sec: Optional[float] = None,
) -> Tuple[np.ndarray, int, str]:
    """
    Renders 'drop_in', 'crossfade', or 'echo_drop_in' with optional 80s DJ DSP effect
    ('jet_flanger', 'echo_out', 'filter_sweep', 'vinyl_brake', or 'none').
    Returns (transition_audio, resume_sample, effect_metadata_note).
    """
    eff = (effect or "none").strip().lower()
    if eff not in SUPPORTED_DJ_EFFECTS:
        eff = "none"

    worked_tail = out_tail.astype(np.float32).copy()
    note = f"{transition_mode}+{eff}"

    if transition_mode == "echo_drop_in":
        return render_echo_drop_it_transition(
            worked_tail,
            audio_in,
            in_start_sample,
            anchor_bpm=anchor_bpm,
            precomputed_in_beats=precomputed_in_beats,
        )

    if eff == "vinyl_brake" and len(worked_tail) >= int(2.5 * TARGET_SR):
        return render_vinyl_brake_echo_transition(
            worked_tail,
            audio_in,
            in_start_sample,
            anchor_bpm=anchor_bpm,
            transition_mode=transition_mode,
            precomputed_out_beats=precomputed_out_beats,
            precomputed_in_beats=precomputed_in_beats,
        )

    if transition_mode == "drop_in":
        trans, resume = render_drop_in_transition(
            worked_tail,
            audio_in,
            in_start_sample,
            anchor_bpm=anchor_bpm,
            min_sync_sec=min_drop_in_sec,
            precomputed_out_beats=precomputed_out_beats,
            precomputed_in_beats=precomputed_in_beats,
            target_sync_sec=target_sync_sec,
        )
        # Apply outgoing DSP flourish carefully to transition head / build
        hold_n = min(len(trans), int(min_drop_in_sec * TARGET_SR))
        if eff == "jet_flanger" and hold_n > TARGET_SR:
            # Jet flanger build across the first ~3.5s of the sync hold into the fade
            fl_len = min(len(trans), int(4.0 * TARGET_SR))
            trans[:fl_len] = apply_jet_flanger(trans[:fl_len], bpm=anchor_bpm, feedback=0.65, wet=0.58)
        elif eff == "filter_sweep" and hold_n > TARGET_SR * 2:
            sw_len = min(len(trans) - TARGET_SR, int(4.5 * TARGET_SR))
            if sw_len > TARGET_SR:
                swept = apply_filter_sweep(trans[TARGET_SR : TARGET_SR + sw_len], direction="highpass", start_hz=60.0, end_hz=1200.0)
                trans[TARGET_SR : TARGET_SR + sw_len] = 0.45 * trans[TARGET_SR : TARGET_SR + sw_len] + 0.55 * swept
        elif eff == "echo_out":
            echo_pad, delay_ms = apply_echo_out(worked_tail[: min(len(worked_tail), int(4.0 * TARGET_SR))], bpm=anchor_bpm, tail_pad_samples=int(2.5 * TARGET_SR), beats_window=2.5, subdiv=0.5)
            note = f"drop_in+echo_out({delay_ms:.0f}ms)"
            blend_n = min(len(trans), len(echo_pad))
            if blend_n > TARGET_SR:
                trans[:blend_n] = _apply_soft_peak_ceiling(0.75 * trans[:blend_n] + 0.35 * echo_pad[:blend_n], ceiling=0.95)
        return trans, resume, note

    # Crossfade mode with DSP effect applied onto outgoing tail prior to crossfade blend
    eff_dur = max(12.0, float(target_sync_sec or 12.0))
    tail_eff_len = min(len(worked_tail), int(eff_dur * TARGET_SR))
    if eff == "jet_flanger" and tail_eff_len > TARGET_SR:
        worked_tail[:tail_eff_len] = apply_jet_flanger(worked_tail[:tail_eff_len], bpm=anchor_bpm, feedback=0.65, wet=0.60)
    elif eff == "filter_sweep" and tail_eff_len > TARGET_SR:
        worked_tail[:tail_eff_len] = apply_filter_sweep(worked_tail[:tail_eff_len], direction="highpass", start_hz=70.0, end_hz=2200.0)
    elif eff == "echo_out" and tail_eff_len > TARGET_SR:
        mid_cut = max(int(3.0 * TARGET_SR), tail_eff_len // 2)
        echo_seg, delay_ms = apply_echo_out(worked_tail[:mid_cut], bpm=anchor_bpm, tail_pad_samples=tail_eff_len - mid_cut, beats_window=3.0, subdiv=0.5)
        paste_len = min(len(worked_tail), len(echo_seg))
        worked_tail[:paste_len] = echo_seg[:paste_len]
        note = f"crossfade+echo_out({delay_ms:.0f}ms)"

    trans, resume = render_beatlocked_crossfade(
        worked_tail,
        audio_in,
        in_start_sample,
        anchor_bpm=anchor_bpm,
        precomputed_out_beats=precomputed_out_beats,
        precomputed_in_beats=precomputed_in_beats,
        target_sync_sec=target_sync_sec,
    )
    return trans, resume, note


def render_transition_between_files(
    track_a_path: str,
    track_b_path: str,
    out_path: str,
    transition_type: str = "drop_in",
    effect: str = "jet_flanger",
    mix_out_sec: Optional[float] = None,
    mix_in_sec: Optional[float] = None,
) -> dict:
    """
    Standalone deterministic CLI transition renderer between two local audio files.
    Outputs a beatmatched, LUFS-normalized snippet (5s pre-roll + transition + 6s incoming body).
    """
    mixxx_idx = load_mixxx_index()
    cache = load_analysis_cache()
    ta = ensure_track_mix_windows(analyze_track_openaudio(track_a_path, mixxx_idx, cache))
    tb = ensure_track_mix_windows(analyze_track_openaudio(track_b_path, mixxx_idx, cache))
    if not ta or not tb:
        raise RuntimeError("Impossible d'analyser les deux pistes audio fournies.")

    anchor_bpm = ta.bpm
    ratio = compute_stretch_ratio(tb.bpm, anchor_bpm)
    audio_a = decode_full_track_stretched(ta.path, tempo_ratio=1.0, target_rms_db=-14.0)
    audio_b = decode_full_track_stretched(tb.path, tempo_ratio=ratio, target_rms_db=-14.0)

    approx_out = float(mix_out_sec) if mix_out_sec is not None else ta.outro_start
    approx_in = float(mix_in_sec) if mix_in_sec is not None else (tb.intro_trim / max(ratio, 0.5))

    out_sample = snap_beat_sample(audio_a, max(8.0, min(len(audio_a) / TARGET_SR - 14.0, approx_out)))
    in_sample = snap_beat_sample(audio_b, max(0.0, min(len(audio_b) / TARGET_SR - 18.0, approx_in)))

    tail_a = audio_a[out_sample:]
    mode = transition_type if transition_type in ("drop_in", "crossfade") else "drop_in"
    trans, resume, note = render_transition_with_effect(
        tail_a,
        audio_b,
        in_sample,
        anchor_bpm=anchor_bpm,
        transition_mode=mode,
        effect=effect,
        min_drop_in_sec=5.0,
    )

    pre = audio_a[max(0, out_sample - 5 * TARGET_SR) : out_sample]
    post = audio_b[resume : min(len(audio_b), resume + 6 * TARGET_SR)]
    snippet = np.vstack([pre, trans, post]).astype(np.float32)

    fmt = "wav" if out_path.lower().endswith(".wav") else ("mp3" if out_path.lower().endswith(".mp3") else "flac")
    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    if fmt == "wav":
        sf.write(out_path, snippet, TARGET_SR, subtype="PCM_16")
    else:
        write_audio_chunk(snippet, out_path, audio_format=fmt)

    return {
        "status": "ok",
        "track_a": ta.title,
        "track_b": tb.title,
        "anchor_bpm": anchor_bpm,
        "tempo_ratio": round(ratio, 4),
        "camelot_a": ta.camelot,
        "camelot_b": tb.camelot,
        "transition_type": mode,
        "effect": effect,
        "effect_detail": note,
        "transition_sec": round(len(trans) / TARGET_SR, 3),
        "total_snippet_sec": round(len(snippet) / TARGET_SR, 3),
        "peak": round(float(np.max(np.abs(snippet))), 4),
        "out_path": out_path,
    }


def find_concierto_apertura_path(marcas_dir: str = MARCAS_DIR) -> Optional[str]:
    """Finds Apertura-Concierto-Discoteque/Discotheque audio file in ~/Musique/Marcas."""
    if not os.path.isdir(marcas_dir):
        return None
    for name in APERTURA_FILENAMES:
        cand = os.path.join(marcas_dir, name)
        if os.path.isfile(cand):
            return cand
    for entry in sorted(os.listdir(marcas_dir)):
        full = os.path.join(marcas_dir, entry)
        if os.path.isfile(full) and Path(entry).suffix.lower() in AUDIO_EXTENSIONS:
            norm = normalize_token(entry)
            if "apertura" in norm and "concierto" in norm:
                return full
    return None


def collect_concierto_singels(
    singels_dir: str = MARCAS_SINGELS_DIR,
    marcas_dir: str = MARCAS_DIR,
) -> List[str]:
    """
    Collects brand jingles ('singels') from ~/Musique/Marcas/singels/.
    Falls back to non-Apertura jingles in ~/Musique/Marcas/ if singels/ is empty.
    """
    results: List[str] = []
    if os.path.isdir(singels_dir):
        for root, _, files in os.walk(singels_dir):
            for fn in sorted(files):
                if Path(fn).suffix.lower() in AUDIO_EXTENSIONS:
                    results.append(os.path.join(root, fn))
    if results:
        return results

    if os.path.isdir(marcas_dir):
        for fn in sorted(os.listdir(marcas_dir)):
            full = os.path.join(marcas_dir, fn)
            if os.path.isfile(full) and Path(fn).suffix.lower() in AUDIO_EXTENSIONS:
                if "apertura" not in normalize_token(fn):
                    results.append(full)
    return results


def trim_jingle_silence(jingle: np.ndarray, sr: int = TARGET_SR) -> np.ndarray:
    """
    Removes dead leading & trailing silence from radio jingles ('Apertura' / 'singels')
    so background music never stays ducked during trailing silent frames.
    """
    if len(jingle) < int(0.4 * sr):
        return jingle.astype(np.float32)

    mono = np.mean(jingle, axis=1) if jingle.ndim > 1 else jingle
    hop = max(1, int(0.020 * sr))
    n_hops = len(mono) // hop
    if n_hops < 4:
        return jingle.astype(np.float32)

    hop_rms = np.sqrt(np.mean(mono[: n_hops * hop].reshape(n_hops, hop) ** 2, axis=1))
    pk = float(np.max(hop_rms))
    if pk <= 1e-5:
        return jingle.astype(np.float32)

    lead_thresh = max(0.008, pk * 0.025)
    trail_thresh = max(0.015, pk * 0.045)
    lead_idx = np.where(hop_rms >= lead_thresh)[0]
    trail_idx = np.where(hop_rms >= trail_thresh)[0]
    if len(lead_idx) == 0 or len(trail_idx) == 0:
        return jingle.astype(np.float32)

    start_samp = max(0, int(lead_idx[0]) * hop - int(0.025 * sr))
    end_samp = min(len(jingle), (int(trail_idx[-1]) + 1) * hop + int(0.060 * sr))
    if end_samp - start_samp < int(0.25 * sr):
        return jingle.astype(np.float32)
    return jingle[start_samp:end_samp].astype(np.float32)


def decode_audio_clip_normalized(path: str, target_lufs: float = -13.0) -> np.ndarray:
    """Decodes an audio jingle/clip cleanly to stereo 44.1kHz float32, trims dead silence, and normalizes to target_lufs."""
    raw = decode_full_track_stretched(path, tempo_ratio=1.0, target_rms_db=target_lufs)
    return trim_jingle_silence(raw, sr=TARGET_SR)


def overlay_jingle_with_ducking(
    music: np.ndarray,
    jingle: np.ndarray,
    start_sample: int,
    music_duck_gain: float = 1.00,
    jingle_gain: float = 1.00,
) -> np.ndarray:
    """
    Mixes a radio/brand jingle cleanly over a background music bed:
    - Trims any trailing silence from the jingle so no empty air is mixed.
    - By default keeps music at 100% (music_duck_gain=1.00, no volume dip); if a lower
      music_duck_gain (< 1.0) is requested, smoothly dips and restores ~0.55s before jingle end.
    - Jingle plays at jingle_gain (100% volume) with clean micro-fades and peak safety ceiling.
    """
    trimmed_jingle = trim_jingle_silence(jingle, sr=TARGET_SR)
    if len(music) < TARGET_SR or len(trimmed_jingle) < int(0.25 * TARGET_SR):
        return music.astype(np.float32)

    start = max(0, min(max(0, len(music) - TARGET_SR // 2), int(start_sample)))
    n = min(len(trimmed_jingle), len(music) - start)
    if n < int(0.25 * TARGET_SR):
        return music.astype(np.float32)

    out = music.astype(np.float32).copy()
    music_seg = out[start : start + n]
    jingle_seg = trimmed_jingle[:n].astype(np.float32)

    duck_g = float(music_duck_gain)
    if duck_g >= 0.999:
        music_env = np.ones((n, 1), dtype=np.float32)
    else:
        music_env = np.full((n, 1), duck_g, dtype=np.float32)
        ramp_in = min(int(0.25 * TARGET_SR), max(1, n // 5))
        # Start bringing music volume back up ~0.55s before jingle tail finishes so it hits 100% cleanly on time
        ramp_out = min(int(0.55 * TARGET_SR), max(1, n // 3))

        u_in = np.linspace(0.0, 1.0, ramp_in, dtype=np.float32)[:, None]
        music_env[:ramp_in] = 1.0 - (1.0 - duck_g) * 0.5 * (1.0 - np.cos(math.pi * u_in))

        u_out = np.linspace(0.0, 1.0, ramp_out, dtype=np.float32)[:, None]
        music_env[-ramp_out:] = duck_g + (1.0 - duck_g) * 0.5 * (1.0 - np.cos(math.pi * u_out))

    jingle_env = np.full((n, 1), float(jingle_gain), dtype=np.float32)
    att = min(int(0.015 * TARGET_SR), max(1, n // 10))
    rel = min(int(0.045 * TARGET_SR), max(1, n // 10))
    jingle_env[:att, 0] *= np.linspace(0.0, 1.0, att, dtype=np.float32)
    jingle_env[-rel:, 0] *= np.linspace(1.0, 0.0, rel, dtype=np.float32)

    blended = music_seg * music_env + jingle_seg * jingle_env
    blended = _apply_soft_peak_ceiling(blended, knee_start=0.78, ceiling=0.95)
    out[start : start + n] = blended.astype(np.float32)
    return out


def write_audio_chunk(audio: np.ndarray, out_path: str, audio_format: str = "flac") -> str:
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    if audio_format == "mp3":
        wav_tmp = out_path + ".tmp.wav"
        sf.write(wav_tmp, audio, TARGET_SR, subtype="PCM_16")
        subprocess.run(
            ["ffmpeg", "-v", "error", "-y", "-i", wav_tmp, "-codec:a", "libmp3lame", "-b:a", "256k", out_path],
            check=True,
            timeout=60,
        )
        try:
            os.remove(wav_tmp)
        except OSError:
            pass
    else:
        sf.write(out_path, audio, TARGET_SR, subtype="PCM_16")
    return out_path
