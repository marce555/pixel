#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Background progressive mixer worker for OpenAudio Pro DJ Mix.
Renders beatmatched, Rubberband time-stretched, Bass-Swap EQ transitions
and appends each seamless FLAC chunk to the live mpv IPC socket.
"""

import json
import os
import random
import shutil
import socket
import sys
import time

import numpy as np

from openaudio_engine import (
    TARGET_SR,
    TrackInfo,
    assess_adaptive_transition_pace,
    assess_rhythmic_mix_suitability,
    choose_dynamic_effect,
    choose_transition_mode,
    collect_concierto_singels,
    compute_stretch_ratio,
    decode_audio_clip_normalized,
    decode_full_track_stretched,
    ensure_track_mix_windows,
    estimate_out_tail_bpm,
    find_best_jingle_spot_in_body,
    overlay_jingle_with_ducking,
    pick_next_continuous_track,
    render_echo_drop_it_transition,
    render_transition_with_effect,
    snap_beat_sample,
    unclamped_bpm_ratio,
    write_audio_chunk,
)


def mpv_ipc_call(sock_path: str, cmd_obj: dict):
    client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.settimeout(2.0)
    client.connect(sock_path)
    client.sendall((json.dumps(cmd_obj) + "\n").encode("utf-8"))
    raw = client.recv(4096).decode("utf-8", errors="ignore")
    client.close()
    first_line = raw.strip().split("\n")[0] if raw.strip() else "{}"
    return json.loads(first_line)


def wait_for_ipc_socket(session_dir: str, timeout_sec: float = 45.0) -> str:
    ipc_file = os.path.join(session_dir, "ipc_socket.txt")
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        if os.path.exists(ipc_file):
            with open(ipc_file, "r", encoding="utf-8") as f:
                sock_path = f.read().strip()
            if sock_path and os.path.exists(sock_path):
                return sock_path
        time.sleep(0.25)
    return ""


def main():
    if len(sys.argv) < 2:
        return
    session_dir = sys.argv[1]
    batch_mode = "--batch" in sys.argv[2:]
    plan_path = os.path.join(session_dir, "plan.json")
    if not os.path.exists(plan_path):
        return

    with open(plan_path, "r", encoding="utf-8") as f:
        plan = json.load(f)

    tracks = plan.get("tracks", [])
    anchor_bpm = float(plan.get("anchor_bpm", 120.0))
    audio_format = plan.get("audio_format", "flac")
    ext = "mp3" if audio_format == "mp3" else "flac"
    concierto_mode = bool(plan.get("concierto_discoteque", False))
    continuous = bool(plan.get("continuous", False)) and (not batch_mode)
    folder_path = str(plan.get("folder_path", "") or "")
    transition_pref = str(plan.get("transition_type", "auto") or "auto").lower()
    effect_pref = str(plan.get("effect", "auto") or "auto").lower()
    mix_in_override = plan.get("mix_in_sec")
    mix_out_override = plan.get("mix_out_sec")
    singels = collect_concierto_singels() if concierto_mode else []
    last_singel = None
    prev_energy = float(tracks[0].get("energy", 0.5)) if tracks else 0.5

    if len(tracks) <= 1 and not continuous:
        return

    sock_path = ""
    if not batch_mode:
        sock_path = wait_for_ipc_socket(session_dir, timeout_sec=60.0)
        if not sock_path:
            return
        # Give mpv 0.4s to finish opening part_000
        time.sleep(0.4)

    last_mode = None
    current_bpm = float(tracks[0].get("bpm", anchor_bpm))
    current_anchor = current_bpm
    played_paths = [str(tracks[0].get("path", ""))] if tracks and tracks[0].get("path") else []
    cleanup_idx = 0
    idx = 1
    mpv_alive = True

    while True:
        if batch_mode and idx >= min(len(tracks), 6):
            break
        if not continuous and idx >= len(tracks):
            break

        if not batch_mode:
            if not os.path.exists(sock_path):
                mpv_alive = False
                break

            # Backpressure: stay at most 2 tracks ahead of playback, and clean up old played chunks
            while os.path.exists(sock_path):
                try:
                    res = mpv_ipc_call(sock_path, {"command": ["get_property", "playlist-pos"]})
                    pos = res.get("data", 0)
                    if isinstance(pos, int):
                        while cleanup_idx <= pos - 2:
                            old_chunk = os.path.join(session_dir, f"part_{cleanup_idx:03d}.{ext}")
                            for p_rm in (old_chunk, old_chunk + ".ready"):
                                if os.path.exists(p_rm):
                                    try:
                                        os.remove(p_rm)
                                    except OSError:
                                        pass
                            cleanup_idx += 1
                        if (idx - pos) <= 2:
                            break
                except Exception:
                    mpv_alive = False
                    break
                time.sleep(2.0)
            if not mpv_alive:
                break

        if idx >= len(tracks):
            if not continuous:
                break
            prev_meta = tracks[-1] if tracks else {}
            next_tr = pick_next_continuous_track(
                curr_meta=prev_meta,
                folder_path=folder_path,
                played_paths=played_paths,
                anchor_bpm=current_anchor,
                max_candidates=12,
            )
            if not next_tr:
                print("[openaudio-worker] No further eligible track found; ending continuous mix.", flush=True)
                break
            next_tr["index"] = len(tracks) + 1
            tracks.append(next_tr)
            print(
                f"[openaudio-worker] Continuous queue +{next_tr['index']}: {next_tr.get('artist','')} - {next_tr.get('title','')} ({next_tr.get('bpm',0):.1f} BPM, {next_tr.get('camelot','')})",
                flush=True,
            )

        tail_path = os.path.join(session_dir, f"tail_{idx - 1:03d}.npy")
        if not os.path.exists(tail_path):
            break

        try:
            out_tail = np.load(tail_path)
            os.remove(tail_path)
        except Exception:
            break

        tr = tracks[idx]
        if tr.get("path"):
            played_paths.append(str(tr["path"]))
        try:
            st = os.stat(tr["path"])
            refined = ensure_track_mix_windows(TrackInfo(
                path=tr["path"],
                title=tr.get("title", ""),
                artist=tr.get("artist", ""),
                duration=float(tr.get("duration", 0.0)),
                bpm=float(tr.get("bpm", anchor_bpm)),
                camelot=tr.get("camelot", ""),
                key_name=tr.get("key", ""),
                energy=float(tr.get("energy", 0.5)),
                rms_db=float(tr.get("rms_db", -14.0)),
                intro_trim=float(tr.get("intro_trim", 0.0)),
                outro_start=float(tr.get("outro_start", 180.0)),
                mtime=float(st.st_mtime),
                size=int(st.st_size),
                hpss_done=False,
            ))
            tr["intro_trim"] = refined.intro_trim
            tr["outro_start"] = refined.outro_start
        except Exception:
            pass
        tr_bpm = float(tr.get("bpm", current_bpm))
        est_out = estimate_out_tail_bpm(out_tail, current_bpm)
        # Keep beatmatch target closely anchored around current_anchor to avoid cumulative BPM runaway
        target_bpm = round(max(current_anchor * 0.975, min(current_anchor * 1.025, est_out)), 2)
        raw_ratio = unclamped_bpm_ratio(tr_bpm, target_bpm)
        too_wide_for_beatmatch = abs(raw_ratio - 1.0) > 0.08
        if too_wide_for_beatmatch:
            # Avoid forcing extreme stretching: keep Track 2 at natural tempo and hand off via Echo Drop-It
            ratio = 1.0
            playing_bpm = tr_bpm
        else:
            ratio = compute_stretch_ratio(tr_bpm, target_bpm)
            playing_bpm = target_bpm
        try:
            audio_i = decode_full_track_stretched(
                tr["path"],
                tempo_ratio=ratio,
                target_rms_db=-14.0,
                current_rms_db=float(tr.get("rms_db", -14.0)),
            )
        except Exception:
            np.save(os.path.join(session_dir, f"tail_{idx:03d}.npy"), out_tail)
            idx += 1
            continue

        if len(audio_i) < TARGET_SR * 20:
            np.save(os.path.join(session_dir, f"tail_{idx:03d}.npy"), out_tail)
            idx += 1
            continue

        raw_intro = float(mix_in_override) if mix_in_override is not None else float(tr.get("intro_trim", 0.0))
        intro_sec = raw_intro / max(ratio, 0.5)
        in_sample = snap_beat_sample(audio_i, intro_sec, bpm_hint=playing_bpm)

        # Verify actual decoded intro BPM against target_bpm so stale BPM tags are corrected full-track
        if not too_wide_for_beatmatch and len(audio_i) - in_sample >= TARGET_SR * 12:
            actual_in_bpm = estimate_out_tail_bpm(
                audio_i[in_sample : min(len(audio_i), in_sample + TARGET_SR * 24)],
                playing_bpm,
            )
            if abs(actual_in_bpm - playing_bpm) / max(playing_bpm, 1.0) > 0.018:
                corrected_ratio = ratio * (playing_bpm / max(actual_in_bpm, 60.0))
                try:
                    if abs(corrected_ratio - 1.0) <= 0.08:
                        ratio = corrected_ratio
                        audio_i = decode_full_track_stretched(
                            tr["path"],
                            tempo_ratio=ratio,
                            target_rms_db=-14.0,
                            current_rms_db=float(tr.get("rms_db", -14.0)),
                        )
                        intro_sec = raw_intro / max(ratio, 0.5)
                        in_sample = snap_beat_sample(audio_i, intro_sec, bpm_hint=playing_bpm)
                    else:
                        too_wide_for_beatmatch = True
                        prev_ratio = ratio
                        ratio = 1.0
                        playing_bpm = round(actual_in_bpm / max(prev_ratio, 0.5), 2)
                        audio_i = decode_full_track_stretched(
                            tr["path"],
                            tempo_ratio=1.0,
                            target_rms_db=-14.0,
                            current_rms_db=float(tr.get("rms_db", -14.0)),
                        )
                        in_sample = snap_beat_sample(audio_i, raw_intro, bpm_hint=playing_bpm)
                except Exception:
                    pass

        # Choose transition type ('drop_in', 'crossfade', or 'echo_drop_in') & DSP effect
        if len(out_tail) >= TARGET_SR * 2 and (len(audio_i) - in_sample) > TARGET_SR * 15:
            if too_wide_for_beatmatch:
                mode = "echo_drop_in"
                mode_reason = "wide_bpm_echo_drop_it"
                out_beats = np.array([], dtype=np.int64)
                in_beats = np.array([], dtype=np.int64)
            elif transition_pref in ("drop_in", "crossfade"):
                rhythmic_ok, r_reason, out_beats, in_beats = assess_rhythmic_mix_suitability(
                    out_tail, audio_i, in_sample, anchor_bpm=target_bpm, min_drop_in_sec=5.0
                )
                if not rhythmic_ok and r_reason.startswith("out_") and transition_pref != "crossfade" and len(out_tail) >= int(4.5 * TARGET_SR):
                    mode = "echo_drop_in"
                    mode_reason = "out_low_percussion_echo_drop_it"
                else:
                    mode = transition_pref if (rhythmic_ok or transition_pref == "crossfade") else "crossfade"
                    mode_reason = r_reason if not rhythmic_ok else f"explicit_{transition_pref}"
            else:
                mode, mode_reason, out_beats, in_beats = choose_transition_mode(
                    out_tail,
                    audio_i,
                    in_sample,
                    anchor_bpm=target_bpm,
                    transition_index=idx,
                    last_mode=last_mode,
                    tempo_ratio=ratio,
                    min_drop_in_sec=5.0,
                )

            pace, target_sync_sec, hold_sec, pace_reason = assess_adaptive_transition_pace(
                out_tail,
                audio_i,
                in_sample,
                anchor_bpm=target_bpm,
                tempo_ratio=ratio,
                out_beats=out_beats,
                in_beats=in_beats,
            )

            if mode == "echo_drop_in":
                trans_seg, body_start, eff_note = render_echo_drop_it_transition(
                    out_tail,
                    audio_i,
                    in_sample,
                    anchor_bpm=target_bpm,
                    precomputed_in_beats=in_beats,
                )
            else:
                cur_energy = float(tr.get("energy", 0.5))
                if effect_pref == "auto":
                    eff = choose_dynamic_effect(mode, idx, energy_jump=(cur_energy - prev_energy))
                else:
                    eff = effect_pref
                prev_energy = cur_energy

                trans_seg, body_start, eff_note = render_transition_with_effect(
                    out_tail,
                    audio_i,
                    in_sample,
                    anchor_bpm=target_bpm,
                    transition_mode=mode,
                    effect=eff,
                    precomputed_out_beats=out_beats,
                    precomputed_in_beats=in_beats,
                    min_drop_in_sec=hold_sec,
                    target_sync_sec=target_sync_sec,
                )
            last_mode = "drop_in" if mode in ("drop_in", "echo_drop_in") else mode
            current_bpm = round(playing_bpm, 2)
            if too_wide_for_beatmatch:
                current_anchor = current_bpm
            print(
                f"[openaudio-worker] track {idx} ({tr.get('title','')[:28]}) bpm={tr_bpm:.1f}->target={target_bpm:.1f} ratio={ratio:.4f} mix_in={in_sample/TARGET_SR:.1f}s mix_out={tr.get('outro_start',0):.1f}s transition={eff_note} pace={pace} ({pace_reason}, {mode_reason})",
                flush=True,
            )
            body_start = min(body_start, max(in_sample, len(audio_i) - TARGET_SR * 15))
        else:
            trans_seg = out_tail
            body_start = in_sample
            current_bpm = round(playing_bpm, 2)

        max_idx = min(len(tracks), 6) if batch_mode else len(tracks)
        has_more = continuous or (idx < max_idx - 1)
        next_tail = np.zeros((0, 2), dtype=np.float32)
        if has_more:
            if mix_out_override is not None:
                approx_out_sec = float(mix_out_override) / max(ratio, 0.5)
            else:
                approx_out_sec = float(tr.get("outro_start", len(audio_i) / TARGET_SR - 16.0)) / max(ratio, 0.5)
            out_sample = snap_beat_sample(audio_i, approx_out_sec, bpm_hint=playing_bpm)
            min_body_end = body_start + TARGET_SR * 15
            tail_floor = 20 if len(audio_i) >= TARGET_SR * 180 else 13
            max_body_end = max(min_body_end, len(audio_i) - TARGET_SR * tail_floor)
            out_sample = max(min_body_end, min(max_body_end, out_sample))

            next_tail = audio_i[out_sample:]
            if len(next_tail) > TARGET_SR * 4:
                np.save(os.path.join(session_dir, f"tail_{idx:03d}.npy"), next_tail)
                body_seg = audio_i[body_start:out_sample]
            else:
                body_seg = audio_i[body_start:]
        else:
            body_seg = audio_i[body_start:]
            # Smooth 2s fade out on the final track of the set
            fade_n = min(len(body_seg), TARGET_SR * 2)
            if fade_n > 0:
                fade = np.linspace(1.0, 0.0, fade_n, dtype=np.float32)[:, None]
                body_seg[-fade_n:] = body_seg[-fade_n:] * fade

        # Concierto Discoteque radio show: place random brand singel on vocal-free intro window after 4s
        if concierto_mode and singels and len(body_seg) > TARGET_SR * 7:
            cands = [s for s in singels if s != last_singel] or singels
            random.shuffle(cands)
            chosen_singel = cands[0]
            try:
                singel_audio = decode_audio_clip_normalized(chosen_singel, target_lufs=-13.0)
                if len(singel_audio) > len(body_seg) - TARGET_SR * 4 and len(cands) > 1:
                    for alt in cands[1:]:
                        alt_audio = decode_audio_clip_normalized(alt, target_lufs=-13.0)
                        if len(alt_audio) <= len(body_seg) - TARGET_SR * 4:
                            chosen_singel, singel_audio = alt, alt_audio
                            break
                if len(body_seg) > len(singel_audio) + TARGET_SR * 2:
                    last_singel = chosen_singel
                    j_start = find_best_jingle_spot_in_body(body_seg, len(singel_audio), min_offset_sec=4.0)
                    body_seg = overlay_jingle_with_ducking(
                        body_seg,
                        singel_audio,
                        start_sample=j_start,
                        music_duck_gain=1.00,
                        jingle_gain=1.00,
                    )
                    print(f"[openaudio-worker] Concierto brand singel injected: {os.path.basename(chosen_singel)}", flush=True)
            except Exception:
                pass

        chunk_audio = np.vstack([trans_seg, body_seg]) if len(trans_seg) > 0 else body_seg
        chunk_path = os.path.join(session_dir, f"part_{idx:03d}.{ext}")
        try:
            write_audio_chunk(chunk_audio, chunk_path, audio_format=audio_format)
            with open(chunk_path + ".ready", "w", encoding="utf-8") as rf:
                rf.write("ok")
        except Exception:
            if has_more and not os.path.exists(os.path.join(session_dir, f"tail_{idx:03d}.npy")):
                np.save(os.path.join(session_dir, f"tail_{idx:03d}.npy"), out_tail)
            idx += 1
            continue

        if not batch_mode:
            if not os.path.exists(sock_path):
                mpv_alive = False
                break
            try:
                mpv_ipc_call(sock_path, {"command": ["loadfile", chunk_path, "append"]})
            except Exception:
                mpv_alive = False
                break

        idx += 1

    if batch_mode:
        return

    # Wait while mpv is still active, then preserve worker.log and clean up temporary mix files
    while mpv_alive and os.path.exists(sock_path):
        try:
            mpv_ipc_call(sock_path, {"command": ["get_property", "pid"]})
            time.sleep(5.0)
        except Exception:
            break

    try:
        wlog_path = os.path.join(session_dir, "worker.log")
        if os.path.exists(wlog_path):
            shutil.copy2(wlog_path, "/tmp/pixel_openaudio_last_worker.log")
    except Exception:
        pass

    try:
        shutil.rmtree(session_dir, ignore_errors=True)
    except Exception:
        pass


if __name__ == "__main__":
    main()
