#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
OpenAudio Pro DJ Mix Skill
--------------------------
Usage:
  python3 run.py "lance un mix avec discoteque-80s"
  python3 run.py "lance un mix"
  python3 run.py "--json lance un mix avec Hits-Remix"
  python3 run.py "--json --android lance un mix"
"""

import json
import os
import secrets
import shutil
import subprocess
import sys
import time
import uuid
import warnings

warnings.filterwarnings("ignore")

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
VENV_PYTHON = os.path.join(SCRIPT_DIR, "venv", "bin", "python")

# Transparently re-exec inside the skill's dedicated OpenAudio venv if needed
if os.path.exists(VENV_PYTHON) and os.path.abspath(sys.executable) != os.path.abspath(VENV_PYTHON):
    try:
        import librosa  # noqa: F401
    except ImportError:
        os.execv(VENV_PYTHON, [VENV_PYTHON] + sys.argv)

sys.path.insert(0, SCRIPT_DIR)

import numpy as np  # noqa: E402

from openaudio_engine import (  # noqa: E402
    TARGET_SR,
    decode_audio_clip_normalized,
    decode_full_track_stretched,
    ensure_track_mix_windows,
    extract_requested_folder_and_start_track,
    find_concierto_apertura_path,
    find_leading_silence_sample,
    is_concierto_discoteque_query,
    list_available_music_folders,
    overlay_jingle_with_ducking,
    render_transition_between_files,
    resolve_music_folder,
    select_pro_dj_set,
    snap_beat_sample,
    write_audio_chunk,
)

MIX_BASE_TMP = "/tmp/pixel_openaudio_mix"
DIRECT_IPC_SOCKET = "/tmp/mpvsocket_openaudio_mix"


def cleanup_old_sessions(max_age_sec: float = 3600.0 * 4) -> None:
    if not os.path.isdir(MIX_BASE_TMP):
        return
    now = time.time()
    for entry in os.listdir(MIX_BASE_TMP):
        full = os.path.join(MIX_BASE_TMP, entry)
        try:
            if os.path.isdir(full) and (now - os.path.getmtime(full)) > max_age_sec:
                shutil.rmtree(full, ignore_errors=True)
        except Exception:
            pass


def format_announcement(mode: str, folder_label: str, anchor_bpm: float, tracks: list) -> str:
    if not tracks:
        return "Mix OpenAudio prêt !"
    t1 = tracks[0]
    title1 = t1.title or os.path.basename(t1.path)
    artist1 = f" de {t1.artist}" if t1.artist else ""
    bpm_int = int(round(anchor_bpm))
    if mode == "folder":
        return (
            f"Mix OpenAudio activé sur {folder_label}, calé autour de {bpm_int} BPM. "
            f"On démarre avec {title1}{artist1} !"
        )
    return (
        f"Mix OpenAudio aléatoire lancé au rythme de {bpm_int} BPM ! "
        f"J'enchaîne {len(tracks)} pistes harmoniques en commençant par {title1}{artist1}."
    )


def build_and_start_mix(
    raw_query: str,
    json_mode: bool = False,
    android_mode: bool = False,
    transition_type: str = "auto",
    effect: str = "auto",
    mix_in_sec: float = None,
    mix_out_sec: float = None,
    explicit_folder: str = "",
    explicit_start_track: str = "",
) -> dict:
    cleanup_old_sessions()

    concierto_mode = is_concierto_discoteque_query(raw_query)
    folder_hint, start_track_hint = extract_requested_folder_and_start_track(raw_query)
    if explicit_folder:
        folder_hint = explicit_folder
    if explicit_start_track:
        start_track_hint = explicit_start_track
    folder_path = None
    if folder_hint:
        folder_path = resolve_music_folder(folder_hint)
        if not folder_path:
            available = list_available_music_folders()
            preview = ", ".join(available[:15])
            raise RuntimeError(
                f"Répertoire '{folder_hint}' introuvable dans ~/Musique. "
                f"Dossiers disponibles : {preview}"
            )

    mode, folder_label, ordered_tracks, anchor_bpm = select_pro_dj_set(
        folder_path,
        max_tracks=10,
        start_track_hint=start_track_hint,
    )

    apertura_audio = None
    if concierto_mode:
        apertura_path = find_concierto_apertura_path()
        if apertura_path:
            try:
                apertura_audio = decode_audio_clip_normalized(apertura_path, target_lufs=-13.0)
            except Exception:
                apertura_audio = None

    session_id = uuid.uuid4().hex[:12]
    session_dir = os.path.join(MIX_BASE_TMP, session_id)
    os.makedirs(session_dir, exist_ok=True)

    audio_format = "mp3" if android_mode else "flac"
    ext = "mp3" if android_mode else "flac"

    # Generate an unguessable per-session token if Android streams via /api/media/mix
    mix_token = secrets.token_urlsafe(24)
    with open(os.path.join(session_dir, "mix_token.txt"), "w", encoding="utf-8") as f:
        f.write(mix_token)

    # Render Track 0 solo body immediately (~0.5s) after refining drum/no-vocal mix points
    t0 = ensure_track_mix_windows(ordered_tracks[0])
    audio0 = decode_full_track_stretched(
        t0.path,
        tempo_ratio=1.0,
        target_rms_db=-14.0,
        current_rms_db=t0.rms_db,
    )
    in_sample = find_leading_silence_sample(audio0)
    # 20ms smooth fade-in at entry
    fade_in_n = min(int(0.02 * TARGET_SR), len(audio0) - in_sample)
    if fade_in_n > 0:
        audio0[in_sample : in_sample + fade_in_n] *= np.linspace(0.0, 1.0, fade_in_n, dtype=np.float32)[:, None]

    if len(ordered_tracks) > 1:
        approx_out0 = float(mix_out_sec) if mix_out_sec is not None else t0.outro_start
        out_sample = snap_beat_sample(audio0, approx_out0)
        min_end = in_sample + TARGET_SR * 18
        if apertura_audio is not None:
            min_end = max(min_end, in_sample + int(6.5 * TARGET_SR) + len(apertura_audio))
        tail_floor = 20 if len(audio0) >= TARGET_SR * 180 else 13
        max_end = max(in_sample + TARGET_SR * 18, len(audio0) - TARGET_SR * tail_floor)
        min_end = min(min_end, max_end)
        out_sample = max(min_end, min(max_end, out_sample))

        tail0 = audio0[out_sample:]
        if len(tail0) > TARGET_SR * 4:
            np.save(os.path.join(session_dir, "tail_000.npy"), tail0)
            part0_audio = audio0[in_sample:out_sample]
        else:
            part0_audio = audio0[in_sample:]
    else:
        part0_audio = audio0[in_sample:]

    # In Concierto Discoteque mode: after 5s of track 0, mix Apertura at 100% over 100% music bed (no ducking)
    if concierto_mode and apertura_audio is not None and len(part0_audio) > int(7.0 * TARGET_SR):
        part0_audio = overlay_jingle_with_ducking(
            part0_audio,
            apertura_audio,
            start_sample=int(5.0 * TARGET_SR),
            music_duck_gain=1.00,
            jingle_gain=1.00,
        )

    first_chunk_path = os.path.join(session_dir, f"part_000.{ext}")
    write_audio_chunk(part0_audio, first_chunk_path, audio_format=audio_format)
    with open(first_chunk_path + ".ready", "w", encoding="utf-8") as rf:
        rf.write("ok")

    tracks_meta = [
        {
            "index": i + 1,
            "path": tr.path,
            "title": tr.title,
            "artist": tr.artist,
            "duration": tr.duration,
            "bpm": tr.bpm,
            "camelot": tr.camelot,
            "key": tr.key_name,
            "energy": tr.energy,
            "rms_db": tr.rms_db,
            "intro_trim": tr.intro_trim,
            "outro_start": tr.outro_start,
        }
        for i, tr in enumerate(ordered_tracks)
    ]

    out_mode = "concierto_discoteque" if concierto_mode else mode
    plan = {
        "session_id": session_id,
        "session_dir": session_dir,
        "mode": out_mode,
        "concierto_discoteque": concierto_mode,
        "folder_label": folder_label,
        "anchor_bpm": anchor_bpm,
        "audio_format": audio_format,
        "transition_type": transition_type or "auto",
        "effect": effect or "auto",
        "mix_in_sec": mix_in_sec,
        "mix_out_sec": mix_out_sec,
        "tracks": tracks_meta,
    }
    with open(os.path.join(session_dir, "plan.json"), "w", encoding="utf-8") as f:
        json.dump(plan, f, ensure_ascii=False, indent=2)

    # Launch detached background worker to render beatmatched transitions ahead of playback
    worker_script = os.path.join(SCRIPT_DIR, "openaudio_mix_worker.py")
    if len(ordered_tracks) > 1:
        worker_args = [VENV_PYTHON if os.path.exists(VENV_PYTHON) else sys.executable, worker_script, session_dir]
        if android_mode:
            worker_args.append("--batch")
        worker_log_path = os.path.join(session_dir, "worker.log")
        wlog = open(worker_log_path, "a", encoding="utf-8")
        subprocess.Popen(
            worker_args,
            cwd=SCRIPT_DIR,
            stdin=subprocess.DEVNULL,
            stdout=wlog,
            stderr=wlog,
            close_fds=True,
            start_new_session=True,
        )
        wlog.close()

    announcement = "" if concierto_mode else format_announcement(mode, folder_label, anchor_bpm, ordered_tracks)

    return {
        "status": "ok",
        "mode": out_mode,
        "folder_label": folder_label,
        "session_id": session_id,
        "session_dir": session_dir,
        "mix_token": mix_token,
        "first_chunk": first_chunk_path,
        "anchor_bpm": anchor_bpm,
        "track_count": len(ordered_tracks),
        "tracks": tracks_meta,
        "announcement": announcement,
    }


def launch_direct_mpv(result: dict) -> None:
    """Starts mpv directly in standalone CLI mode and links ipc_socket.txt to the worker."""
    try:
        subprocess.run(["pkill", "-f", f"input-ipc-server={DIRECT_IPC_SOCKET}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        pass
    if os.path.exists(DIRECT_IPC_SOCKET):
        try:
            os.remove(DIRECT_IPC_SOCKET)
        except OSError:
            pass

    cmd = [
        "mpv",
        "--no-video",
        "--no-terminal",
        "--ao=pipewire",
        "--gapless-audio=yes",
        f"--input-ipc-server={DIRECT_IPC_SOCKET}",
        result["first_chunk"],
    ]
    subprocess.Popen(
        cmd,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        close_fds=True,
        start_new_session=True,
    )

    # Wait briefly for mpv socket creation, then notify worker
    for _ in range(30):
        if os.path.exists(DIRECT_IPC_SOCKET):
            break
        time.sleep(0.1)
    with open(os.path.join(result["session_dir"], "ipc_socket.txt"), "w", encoding="utf-8") as f:
        f.write(DIRECT_IPC_SOCKET)


def _parse_cli_tokens(args: list) -> dict:
    """Parses Go's flat CLI arg string, structured flags, or JSON payload."""
    # Go's ExecuteSkill passes a single string; split safely if flags/JSON are present
    tokens = []
    if len(args) == 1 and args[0].lstrip().startswith("{"):
        tokens = [args[0].strip()]
    elif len(args) == 1 and "--" in args[0]:
        import shlex

        try:
            tokens = shlex.split(args[0])
        except ValueError:
            tokens = args[0].split()
    else:
        tokens = list(args)

    cfg = {
        "json_mode": False,
        "android_mode": False,
        "transition_type": "auto",
        "effect": "auto",
        "mix_in_sec": None,
        "mix_out_sec": None,
        "explicit_folder": "",
        "explicit_start_track": "",
        "render_test": None,
        "out_path": "/tmp/openaudio_transition_test.flac",
        "query": "",
    }

    if len(tokens) == 1 and tokens[0].startswith("{"):
        try:
            data = json.loads(tokens[0])
            if isinstance(data, dict):
                cfg["json_mode"] = bool(data.get("json", True))
                cfg["android_mode"] = bool(data.get("android", False))
                cfg["transition_type"] = str(data.get("transition_type", "auto"))
                cfg["effect"] = str(data.get("effect", "auto"))
                cfg["mix_in_sec"] = data.get("mix_in_sec")
                cfg["mix_out_sec"] = data.get("mix_out_sec")
                cfg["explicit_folder"] = str(data.get("folder", "") or "")
                cfg["explicit_start_track"] = str(data.get("start_track", "") or "")
                cfg["query"] = str(data.get("query", "") or "")
                return cfg
        except Exception:
            pass

    rest = []
    i = 0
    while i < len(tokens):
        tok = tokens[i]
        if tok == "--json":
            cfg["json_mode"] = True
        elif tok == "--android":
            cfg["android_mode"] = True
        elif tok == "--transition-type" and i + 1 < len(tokens):
            cfg["transition_type"] = tokens[i + 1]
            i += 1
        elif tok == "--effect" and i + 1 < len(tokens):
            cfg["effect"] = tokens[i + 1]
            i += 1
        elif tok == "--mix-in-sec" and i + 1 < len(tokens):
            cfg["mix_in_sec"] = float(tokens[i + 1])
            i += 1
        elif tok == "--mix-out-sec" and i + 1 < len(tokens):
            cfg["mix_out_sec"] = float(tokens[i + 1])
            i += 1
        elif tok == "--out" and i + 1 < len(tokens):
            cfg["out_path"] = tokens[i + 1]
            i += 1
        elif tok == "--render-transition-test" and i + 2 < len(tokens):
            cfg["render_test"] = (tokens[i + 1], tokens[i + 2])
            i += 2
        else:
            rest.append(tok)
        i += 1

    cfg["query"] = " ".join(rest).strip()
    return cfg


def main():
    cfg = _parse_cli_tokens(sys.argv[1:])
    json_mode = cfg["json_mode"]

    try:
        if cfg["render_test"] is not None:
            tr_a, tr_b = cfg["render_test"]
            res = render_transition_between_files(
                tr_a,
                tr_b,
                out_path=cfg["out_path"],
                transition_type=cfg["transition_type"] if cfg["transition_type"] != "auto" else "drop_in",
                effect=cfg["effect"] if cfg["effect"] != "auto" else "jet_flanger",
                mix_out_sec=cfg["mix_out_sec"],
                mix_in_sec=cfg["mix_in_sec"],
            )
            print(json.dumps(res, ensure_ascii=False, indent=2))
            return

        result = build_and_start_mix(
            cfg["query"],
            json_mode=json_mode,
            android_mode=cfg["android_mode"],
            transition_type=cfg["transition_type"],
            effect=cfg["effect"],
            mix_in_sec=cfg["mix_in_sec"],
            mix_out_sec=cfg["mix_out_sec"],
            explicit_folder=cfg["explicit_folder"],
            explicit_start_track=cfg["explicit_start_track"],
        )
        if json_mode:
            print(json.dumps(result, ensure_ascii=False))
            return

        launch_direct_mpv(result)
        lines = [
            f"🎧 **Mix OpenAudio Pro DJ lancé** ({result['folder_label']} — ~{int(round(result['anchor_bpm']))} BPM)",
            "",
        ]
        for tr in result["tracks"]:
            artist_part = f"{tr['artist']} - " if tr["artist"] else ""
            lines.append(f"  {tr['index']}. {artist_part}{tr['title']}  *({tr['bpm']:.1f} BPM • {tr['camelot']})*")
        lines.append("")
        lines.append("✨ Enchaînements beatmatchés avec **Rubberband Master Tempo** et **Bass-Swap EQ** en cours via `mpv` (gapless).")
        print("\n".join(lines))
    except Exception as e:
        if json_mode:
            print(json.dumps({"status": "error", "error": str(e)}, ensure_ascii=False))
        else:
            print(f"❌ Erreur Mix OpenAudio : {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
