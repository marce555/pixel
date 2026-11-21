#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import os
import sys
import tempfile
import unittest

import numpy as np
import soundfile as sf

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from openaudio_engine import (
    TARGET_SR,
    TrackInfo,
    apply_echo_out,
    apply_filter_sweep,
    apply_jet_flanger,
    apply_vinyl_brake,
    assess_adaptive_transition_pace,
    camelot_distance,
    choose_dynamic_effect,
    choose_transition_mode,
    collect_concierto_singels,
    compute_stretch_ratio,
    estimate_out_tail_bpm,
    extract_local_beat_samples,
    extract_requested_folder_and_start_track,
    extract_requested_folder_query,
    extract_requested_track_count,
    find_best_echo_cue_in_tail,
    find_best_jingle_spot_in_body,
    find_concierto_apertura_path,
    find_leading_silence_sample,
    find_matching_track_file,
    find_mix_windows_from_pcm,
    is_concierto_discoteque_query,
    load_folder_analysis_cache,
    measure_integrated_lufs,
    normalize_audio_lufs,
    overlay_jingle_with_ducking,
    pick_next_continuous_track,
    render_beatlocked_crossfade,
    render_drop_in_transition,
    render_echo_drop_it_transition,
    render_pro_dj_transition,
    render_transition_with_effect,
    save_folder_analysis_cache,
    transition_compatibility,
    trim_jingle_silence,
)
import run as dj_run


class TestOpenAudioDJEngine(unittest.TestCase):
    def test_extract_requested_folder_query(self):
        self.assertEqual(extract_requested_folder_query("lance un mix avec discoteque-80s"), "discoteque-80s")
        self.assertEqual(extract_requested_folder_query("Lance-moi un mix avec Hits-Remix sur mon pc"), "Hits-Remix")
        self.assertEqual(extract_requested_folder_query("lance un mix avec 125 BPM best 80's"), "125 BPM best 80's")
        self.assertEqual(extract_requested_folder_query("lance un mix"), "")
        self.assertEqual(extract_requested_folder_query("fais-moi un mix dj"), "")
        self.assertEqual(extract_requested_folder_query("lance un mix openaudio"), "")

    def test_camelot_and_transition_scoring(self):
        self.assertEqual(camelot_distance("8A", "8A"), 30)
        self.assertEqual(camelot_distance("8A", "9A"), 25)
        self.assertEqual(camelot_distance("8A", "8B"), 22)

        curr = TrackInfo("a.mp3", "A", "Art", 200.0, 124.0, "8A", "Am", 0.7, -14.0, 0.0, 180.0, 1.0, 1000)
        good = TrackInfo("b.mp3", "B", "Art", 200.0, 125.0, "9A", "Em", 0.72, -14.0, 0.0, 180.0, 1.0, 1000)
        bad = TrackInfo("c.mp3", "C", "Art", 200.0, 92.0, "2B", "F#", 0.3, -18.0, 0.0, 180.0, 1.0, 1000)

        sc_good, ratio_good = transition_compatibility(curr, good, 124.0)
        sc_bad, _ = transition_compatibility(curr, bad, 124.0)
        self.assertGreater(sc_good, sc_bad)
        self.assertAlmostEqual(ratio_good, 124.0 / 125.0, places=4)

    def test_render_pro_dj_transition_no_clipping(self):
        t = np.linspace(0, 6.0, TARGET_SR * 6, endpoint=False, dtype=np.float32)
        sine1 = np.stack([np.sin(2 * np.pi * 110 * t), np.sin(2 * np.pi * 110 * t)], axis=1) * 0.75
        sine2 = np.stack([np.sin(2 * np.pi * 220 * t), np.sin(2 * np.pi * 220 * t)], axis=1) * 0.75

        blended = render_pro_dj_transition(sine1, sine2, TARGET_SR * 4)
        self.assertEqual(blended.shape, (TARGET_SR * 4, 2))
        self.assertLessEqual(float(np.max(np.abs(blended))), 0.96)

    def test_find_mix_windows_from_pcm_prefers_drum_break(self):
        sr = 11025
        dur = 90.0
        n = int(sr * dur)
        t = np.linspace(0, dur, n, endpoint=False, dtype=np.float32)
        y = np.zeros(n, dtype=np.float32)

        # 0..10s: soft ambient pad (no drums)
        y[: sr * 10] = 0.08 * np.sin(2 * np.pi * 440.0 * t[: sr * 10])

        # 10..90s: steady 120 BPM drum pulse (kick transient every 0.5s)
        rng = np.random.default_rng(42)
        for beat_t in np.arange(10.0, 89.5, 0.5):
            idx = int(beat_t * sr)
            k_len = min(int(0.10 * sr), n - idx)
            kt = np.linspace(0, 0.10, k_len, endpoint=False, dtype=np.float32)
            kick = 0.85 * np.sin(2 * np.pi * (120.0 * np.exp(-kt * 18.0)) * kt) * np.exp(-kt * 25.0)
            y[idx : idx + k_len] += kick
        # Subtle hi-hat noise ticks on off-beats from 10s..90s
        for hat_t in np.arange(10.25, 89.5, 0.5):
            idx = int(hat_t * sr)
            h_len = min(int(0.03 * sr), n - idx)
            y[idx : idx + h_len] += 0.18 * rng.standard_normal(h_len).astype(np.float32) * np.exp(-np.linspace(0, 1, h_len) * 8.0)

        # 10..42s and 62..88s: strong vocal harmonics in 600-1800 Hz; 42..62s is pure drum break!
        vocal = (
            0.45 * np.sin(2 * np.pi * 680.0 * t)
            + 0.35 * np.sin(2 * np.pi * 1360.0 * t)
        ).astype(np.float32)
        y[sr * 10 : sr * 42] += vocal[sr * 10 : sr * 42]
        y[sr * 62 : sr * 88] += vocal[sr * 62 : sr * 88]

        mix_in, mix_out = find_mix_windows_from_pcm(y, sr, 120.0, fallback_in=0.0, fallback_out=74.0)
        self.assertGreaterEqual(mix_in, 8.0)
        self.assertGreaterEqual(mix_out, 38.0)
        self.assertLessEqual(mix_out, 60.0)

    def _make_beat_track(self, dur_sec: float, bpm: float, phase_offset_sec: float) -> np.ndarray:
        n = int(dur_sec * TARGET_SR)
        t = np.linspace(0, dur_sec, n, endpoint=False, dtype=np.float32)
        mono = 0.10 * np.sin(2 * np.pi * 330.0 * t)
        beat_sec = 60.0 / bpm
        b = phase_offset_sec
        while b < dur_sec - 0.15:
            idx = int(b * TARGET_SR)
            k_len = min(int(0.09 * TARGET_SR), n - idx)
            kt = np.linspace(0, 0.09, k_len, endpoint=False, dtype=np.float32)
            kick = 0.70 * np.sin(2 * np.pi * (110.0 * np.exp(-kt * 16.0)) * kt) * np.exp(-kt * 22.0)
            mono[idx : idx + k_len] += kick
            b += beat_sec
        return np.stack([mono, mono], axis=1).astype(np.float32)

    def test_render_drop_in_transition_holds_5s_and_ramps_volumes(self):
        # Outgoing tail (13s @ 125 BPM with 3kHz probe tone), incoming track (18s @ 123 BPM with 4.5kHz probe tone)
        out_tail = self._make_beat_track(13.0, 125.0, 0.25)
        t_out = np.linspace(0, 13.0, len(out_tail), endpoint=False, dtype=np.float32)[:, None]
        out_tail += 0.10 * np.sin(2 * np.pi * 3000.0 * t_out)

        audio_in = self._make_beat_track(18.0, 123.0, 0.09)
        t_in = np.linspace(0, 18.0, len(audio_in), endpoint=False, dtype=np.float32)[:, None]
        audio_in += 0.10 * np.sin(2 * np.pi * 4500.0 * t_in)

        in_start = int(1.0 * TARGET_SR)
        out_beats = extract_local_beat_samples(out_tail, 125.0)
        b0 = int(out_beats[0]) if len(out_beats) > 0 else 0

        trans, resume_sample = render_drop_in_transition(
            out_tail,
            audio_in,
            in_start_sample=in_start,
            anchor_bpm=125.0,
            min_sync_sec=5.0,
        )

        # At 125 BPM (beat=0.48s), sync picks 20 beats (~9.6s): >= 5.0s hold + gradual fadeout
        self.assertGreaterEqual(len(trans), int(8.2 * TARGET_SR))
        self.assertGreaterEqual(resume_sample - in_start, int(8.2 * TARGET_SR))
        self.assertLess(resume_sample, len(audio_in))
        self.assertLessEqual(float(np.max(np.abs(trans))), 0.96)

        def band_rms(seg: np.ndarray, center_hz: float) -> float:
            mono = np.mean(seg, axis=1)
            fft_mag = np.abs(np.fft.rfft(mono)) / max(1, len(mono))
            freqs = np.fft.rfftfreq(len(mono), d=1.0 / TARGET_SR)
            mask = (freqs >= center_hz - 100.0) & (freqs <= center_hz + 100.0)
            return float(np.sqrt(np.sum(fft_mag[mask] ** 2)))

        # Track 1 holds 100% during first 5.0s after Beat 0, then fades gradually to silence
        e_out_1 = band_rms(trans[b0 + int(0.4 * TARGET_SR) : b0 + int(2.0 * TARGET_SR)], 3000.0)
        e_out_2 = band_rms(trans[b0 + int(3.0 * TARGET_SR) : b0 + int(4.6 * TARGET_SR)], 3000.0)
        e_out_fade = band_rms(trans[b0 + int(6.8 * TARGET_SR) : b0 + int(7.8 * TARGET_SR)], 3000.0)
        e_out_end = band_rms(trans[-int(0.2 * TARGET_SR) :], 3000.0)
        self.assertAlmostEqual(e_out_1, e_out_2, delta=e_out_1 * 0.12)
        self.assertGreater(e_out_2, e_out_fade * 1.3)
        self.assertGreater(e_out_fade, e_out_end * 4.0)

        # Track 2 starts near ~80% volume at Drop-In entry and rises smoothly to ~100%
        e_in_start = band_rms(trans[b0 + int(0.05 * TARGET_SR) : b0 + int(0.65 * TARGET_SR)], 4500.0)
        e_in_end = band_rms(trans[-int(0.70 * TARGET_SR) : -int(0.10 * TARGET_SR)], 4500.0)
        ratio_in = e_in_start / max(e_in_end, 1e-6)
        self.assertGreaterEqual(ratio_in, 0.72)
        self.assertLessEqual(ratio_in, 0.88)

        # Beatlocked crossfade >= 9.5s (~10-11.5s) with zero clipping
        xf_trans, xf_res = render_beatlocked_crossfade(out_tail, audio_in, in_start, anchor_bpm=125.0)
        self.assertGreaterEqual(len(xf_trans), int(9.5 * TARGET_SR))
        self.assertGreaterEqual(xf_res - in_start, int(9.5 * TARGET_SR))
        self.assertLessEqual(float(np.max(np.abs(xf_trans))), 0.96)

    def test_choose_transition_mode_alternates_and_fallbacks(self):
        out_tail = self._make_beat_track(10.0, 125.0, 0.25)
        audio_in = self._make_beat_track(14.0, 125.0, 0.15)
        in_start = int(1.0 * TARGET_SR)

        m1, _, _, _ = choose_transition_mode(out_tail, audio_in, in_start, 125.0, transition_index=1, last_mode=None)
        m2, r2, _, _ = choose_transition_mode(out_tail, audio_in, in_start, 125.0, transition_index=2, last_mode=m1)
        m3, _, _, _ = choose_transition_mode(out_tail, audio_in, in_start, 125.0, transition_index=3, last_mode=m2)
        self.assertEqual(m1, "drop_in")
        self.assertEqual(m2, "crossfade")
        self.assertEqual(r2, "dynamic_alternation")
        self.assertEqual(m3, "drop_in")

        # Non-rhythmic ambient incoming pad -> forced crossfade ("pas assez de temps rythme")
        t_amb = np.linspace(0, 14.0, int(14.0 * TARGET_SR), endpoint=False, dtype=np.float32)
        pad = 0.20 * np.sin(2 * np.pi * 440.0 * t_amb)
        ambient_in = np.stack([pad, pad], axis=1).astype(np.float32)
        m_amb, r_amb, _, _ = choose_transition_mode(
            out_tail, ambient_in, in_start, 125.0, transition_index=3, last_mode="crossfade"
        )
        self.assertEqual(m_amb, "crossfade")
        self.assertIn(r_amb, {"insufficient_beats", "weak_rhythm_pulse", "unstable_beatgrid"})

        # Short tail -> forced crossfade
        m_short, r_short, _, _ = choose_transition_mode(
            out_tail[: int(3.0 * TARGET_SR)], audio_in, in_start, 125.0, transition_index=1, last_mode="crossfade"
        )
        self.assertEqual(m_short, "crossfade")
        self.assertEqual(r_short, "short_tail_or_head")

    def test_normalize_audio_lufs_balances_quiet_and_loud_tracks(self):
        base = self._make_beat_track(12.0, 122.0, 0.1)
        # Quiet track (~14 dB quieter) with an initial quiet 3s intro + kicks
        quiet = base * 0.22
        quiet[: TARGET_SR * 3] *= 0.25
        # Loud compressed track (~8 dB louder)
        loud = np.tanh(base * 2.2) * 0.85

        lufs_q_before = measure_integrated_lufs(quiet)
        lufs_l_before = measure_integrated_lufs(loud)
        self.assertGreater(lufs_l_before - lufs_q_before, 10.0)

        norm_q = normalize_audio_lufs(quiet, target_lufs=-14.0)
        norm_l = normalize_audio_lufs(loud, target_lufs=-14.0)

        lufs_q_after = measure_integrated_lufs(norm_q)
        lufs_l_after = measure_integrated_lufs(norm_l)
        self.assertAlmostEqual(lufs_q_after, -14.0, delta=0.4)
        self.assertAlmostEqual(lufs_l_after, -14.0, delta=0.4)
        self.assertLessEqual(float(np.max(np.abs(norm_q))), 0.95)
        self.assertLessEqual(float(np.max(np.abs(norm_l))), 0.95)

    def test_concierto_discoteque_parsing_ducking_and_singels(self):
        import tempfile

        self.assertTrue(is_concierto_discoteque_query("lance Concierto Discoteque Hits-Remix"))
        self.assertTrue(is_concierto_discoteque_query("Lance concierto discotheque avec discoteque-80s"))
        self.assertFalse(is_concierto_discoteque_query("lance un mix avec discoteque-80s"))

        self.assertEqual(extract_requested_folder_query("lance Concierto Discoteque Hits-Remix"), "Hits-Remix")
        self.assertEqual(extract_requested_folder_query("lance Concierto Discoteque avec discoteque-80s sur mon pc"), "discoteque-80s")
        self.assertEqual(extract_requested_folder_query("lance Concierto Discoteque"), "")

        # Verify overlay_jingle_with_ducking: music stays at 100% under 100% jingle, and stays 100% after
        dur_m = 12.0
        t_m = np.linspace(0, dur_m, int(dur_m * TARGET_SR), endpoint=False, dtype=np.float32)
        m_sig = 0.25 * np.sin(2 * np.pi * 1000.0 * t_m)
        music = np.stack([m_sig, m_sig], axis=1).astype(np.float32)

        dur_j = 3.0
        t_j = np.linspace(0, dur_j, int(dur_j * TARGET_SR), endpoint=False, dtype=np.float32)
        j_sig = 0.25 * np.sin(2 * np.pi * 3500.0 * t_j)
        jingle = np.stack([j_sig, j_sig], axis=1).astype(np.float32)

        mixed = overlay_jingle_with_ducking(
            music,
            jingle,
            start_sample=int(5.0 * TARGET_SR),
            music_duck_gain=1.00,
            jingle_gain=1.00,
        )
        self.assertLessEqual(float(np.max(np.abs(mixed))), 0.95)

        def tone_mag(seg: np.ndarray, hz: float) -> float:
            mono = np.mean(seg, axis=1)
            fft_mag = np.abs(np.fft.rfft(mono)) / max(1, len(mono))
            freqs = np.fft.rfftfreq(len(mono), d=1.0 / TARGET_SR)
            mask = (freqs >= hz - 50.0) & (freqs <= hz + 50.0)
            return float(np.sqrt(np.sum(fft_mag[mask] ** 2)))

        m_pre = tone_mag(mixed[int(1.0 * TARGET_SR) : int(4.0 * TARGET_SR)], 1000.0)
        m_duck = tone_mag(mixed[int(5.6 * TARGET_SR) : int(7.4 * TARGET_SR)], 1000.0)
        m_post = tone_mag(mixed[int(8.6 * TARGET_SR) : int(11.0 * TARGET_SR)], 1000.0)
        j_mid = tone_mag(mixed[int(5.6 * TARGET_SR) : int(7.4 * TARGET_SR)], 3500.0)
        self.assertAlmostEqual(m_duck / m_pre, 1.00, delta=0.03)
        self.assertAlmostEqual(m_post / m_pre, 1.00, delta=0.03)
        self.assertAlmostEqual(j_mid / m_pre, 1.00, delta=0.03)

    def test_start_track_parsing_and_matching(self):
        f1, t1 = extract_requested_folder_and_start_track("lance Concierto Discoteque Hits-Remix en commençant par Celebration")
        self.assertEqual((f1, t1), ("Hits-Remix", "Celebration"))

        f2, t2 = extract_requested_folder_and_start_track(
            "lance Concierto Discoteque avec discoteque-80s et commence par You Should Be Dancing sur mon pc"
        )
        self.assertEqual((f2, t2), ("discoteque-80s", "You Should Be Dancing"))

        f3, t3 = extract_requested_folder_and_start_track("lance un mix avec 125 BPM best 80's en démarrant avec Don't Go")
        self.assertEqual((f3, t3), ("125 BPM best 80's", "Don't Go"))

        f4, t4 = extract_requested_folder_and_start_track("lance Concierto Discoteque Hits-Remix premier titre Stayin Alive")
        self.assertEqual((f4, t4), ("Hits-Remix", "Stayin Alive"))

        f5, t5 = extract_requested_folder_and_start_track(
            "lance Concierto Discoteque Hits-Remix en commençant Pet Shop Boys - Heart (1987 Dance Mix).mp3"
        )
        self.assertEqual((f5, t5), ("Hits-Remix", "Pet Shop Boys - Heart (1987 Dance Mix).mp3"))

        files = [
            "/tmp/music/Bee Gees - You Should Be Dancing.mp3",
            "/tmp/music/Kool & The Gang - Celebration.mp3",
            "/tmp/music/Yazoo - Don't Go.mp3",
            "/tmp/music/Pet Shop Boys - Heart (Dim Zach remix)  2020.mp3",
            "/tmp/music/Pet Shop Boys - Heart (1987 Dance Mix).mp3",
        ]
        self.assertEqual(find_matching_track_file(files, "Celebration"), files[1])
        self.assertEqual(find_matching_track_file(files, "you should be dancing"), files[0])
        self.assertEqual(find_matching_track_file(files, "dont go"), files[2])
        self.assertEqual(
            find_matching_track_file(files, "Pet Shop Boys - Heart (1987 Dance Mix).mp3"),
            files[4],
        )
        self.assertIsNone(find_matching_track_file(files, "NonExistentSong123"))

        # Verify Track 0 starts cleanly from the beginning (only leading dead silence trimmed)
        immediate = np.full((TARGET_SR * 4, 2), 0.15, dtype=np.float32)
        self.assertEqual(find_leading_silence_sample(immediate), 0)
        with_silence = np.vstack([
            np.zeros((int(1.5 * TARGET_SR), 2), dtype=np.float32),
            immediate,
        ])
        lead_idx = find_leading_silence_sample(with_silence)
        self.assertGreaterEqual(lead_idx, int(1.40 * TARGET_SR))
        self.assertLessEqual(lead_idx, int(1.51 * TARGET_SR))

        # Verify apertura & singels lookup (singels/ prioritized, fallback to non-Apertura Marcas files)
        with tempfile.TemporaryDirectory() as tmp:
            ap = os.path.join(tmp, "Apertura-Concierto-Discotheque.mp3")
            s1 = os.path.join(tmp, "MARCA 34.mp3")
            sub = os.path.join(tmp, "singels")
            os.makedirs(sub, exist_ok=True)
            open(ap, "wb").close()
            open(s1, "wb").close()
            self.assertEqual(find_concierto_apertura_path(tmp), ap)
            self.assertEqual(collect_concierto_singels(sub, tmp), [s1])
            s2 = os.path.join(sub, "S1.mp3")
            open(s2, "wb").close()
            self.assertEqual(collect_concierto_singels(sub, tmp), [s2])

    def test_dsp_effects_jingle_placement_folder_cache_and_cli(self):
        seg = self._make_beat_track(6.0, 120.0, 0.1)

        # 1. Jet Flanger preserves length, modifies timbre, <= 0.95 ceiling
        jf = apply_jet_flanger(seg, bpm=120.0, feedback=0.65, wet=0.62)
        self.assertEqual(jf.shape, seg.shape)
        self.assertGreater(float(np.mean(np.abs(jf - seg))), 0.01)
        self.assertLessEqual(float(np.max(np.abs(jf))), 0.95)

        # 2. Echo Out: 120 BPM eighth-note delay = 250ms, rings into tail_pad
        eo, delay_ms = apply_echo_out(seg, bpm=120.0, tail_pad_samples=TARGET_SR * 2, beats_window=3.0, subdiv=0.5)
        self.assertAlmostEqual(delay_ms, 250.0, places=2)
        self.assertEqual(len(eo), len(seg) + TARGET_SR * 2)
        self.assertGreater(float(np.max(np.abs(eo[len(seg) : len(seg) + TARGET_SR]))), 0.01)
        self.assertLessEqual(float(np.max(np.abs(eo))), 0.95)

        # 3. Filter Sweep progressive HPF reduces low frequency energy towards end vs start
        fs = apply_filter_sweep(seg, direction="highpass", start_hz=70.0, end_hz=2200.0)
        self.assertEqual(fs.shape, seg.shape)
        self.assertLessEqual(float(np.max(np.abs(fs))), 0.95)

        # 4. Vinyl Brake preserves shape & tapers cleanly to near 0 at end
        vb = apply_vinyl_brake(seg, brake_sec=0.65)
        self.assertEqual(vb.shape, seg.shape)
        self.assertLess(float(np.max(np.abs(vb[-200:]))), 0.05)

        # 5. Transition dispatcher with explicit effects
        out_tail = self._make_beat_track(13.0, 125.0, 0.2)
        audio_in = self._make_beat_track(18.0, 125.0, 0.1)
        for mode, eff in (("drop_in", "jet_flanger"), ("crossfade", "echo_out"), ("drop_in", "vinyl_brake"), ("crossfade", "filter_sweep")):
            tr_audio, res, note = render_transition_with_effect(
                out_tail, audio_in, TARGET_SR, anchor_bpm=125.0, transition_mode=mode, effect=eff, min_drop_in_sec=5.0
            )
            self.assertGreater(len(tr_audio), TARGET_SR * 6)
            self.assertGreater(res, TARGET_SR * 6)
            self.assertLessEqual(float(np.max(np.abs(tr_audio))), 0.96)
            self.assertIn(eff, note)
            if eff == "vinyl_brake":
                self.assertIn("echo(", note)
        self.assertIn(choose_dynamic_effect("drop_in", 1), ("jet_flanger", "filter_sweep", "vinyl_brake"))

        # Verify vinyl_brake + echo stops Track 1 dry beats cleanly and lets Track 2 continue steadily
        vb_trans, _, _ = render_transition_with_effect(
            out_tail, audio_in, TARGET_SR, anchor_bpm=125.0, transition_mode="drop_in", effect="vinyl_brake"
        )
        post_brake = vb_trans[int(2.2 * TARGET_SR) :]
        vb_beats = extract_local_beat_samples(post_brake, 125.0, allow_extrapolate=False)
        self.assertGreaterEqual(len(vb_beats), 8)
        vb_ibi_ms = (np.diff(vb_beats) / float(TARGET_SR)) * 1000.0
        self.assertLess(float(np.max(np.abs(vb_ibi_ms - 480.0))), 16.0)

        # 6. Jingle placement avoids vocal-heavy window [4..8s] in favor of instrumental [9..14s]
        body = self._make_beat_track(22.0, 120.0, 0.0)
        t_b = np.linspace(0, 22.0, len(body), endpoint=False, dtype=np.float32)[:, None]
        voc = 0.25 * np.sin(2 * np.pi * 1200.0 * t_b)
        body[int(3.5 * TARGET_SR) : int(8.5 * TARGET_SR)] += voc[int(3.5 * TARGET_SR) : int(8.5 * TARGET_SR)]
        spot = find_best_jingle_spot_in_body(body, jingle_len=int(4.0 * TARGET_SR), min_offset_sec=4.0)
        self.assertGreaterEqual(spot, int(8.0 * TARGET_SR))

        # 7. Per-directory JSON cache + CLI/JSON parser
        with tempfile.TemporaryDirectory() as fdir:
            save_folder_analysis_cache(fdir, {os.path.join(fdir, "a.mp3"): {"bpm": 124.0, "camelot": "8A", "intro_trim": 4.0, "outro_start": 160.0}})
            loaded = load_folder_analysis_cache(fdir)
            self.assertIn(os.path.join(fdir, "a.mp3"), loaded)
            self.assertEqual(loaded[os.path.join(fdir, "a.mp3")]["mix_in_sec"], 4.0)

        cfg = dj_run._parse_cli_tokens([
            '{"folder":"Hits-Remix","transition_type":"drop_in","effect":"echo_out","mix_in_sec":5.0,"mix_out_sec":140.0}'
        ])
        self.assertEqual(cfg["transition_type"], "drop_in")
        self.assertEqual(cfg["effect"], "echo_out")
        self.assertEqual(cfg["mix_in_sec"], 5.0)
        self.assertEqual(cfg["mix_out_sec"], 140.0)

    def test_adaptive_transition_pace_long_80s_vs_quick(self):
        # Case 1: Long steady 80s 122 BPM pure rhythm on both tracks (22s out, 28s in) -> 'long' >= 12s
        long_out = self._make_beat_track(22.0, 122.0, 0.2)
        long_in = self._make_beat_track(28.0, 122.0, 0.1)
        pace_l, target_l, hold_l, _ = assess_adaptive_transition_pace(
            long_out, long_in, in_start_sample=TARGET_SR, anchor_bpm=122.0, tempo_ratio=1.0
        )
        self.assertEqual(pace_l, "long")
        self.assertGreaterEqual(target_l, 12.0)
        self.assertGreaterEqual(hold_l, 6.0)

        xf_long, _, _ = render_transition_with_effect(
            long_out, long_in, TARGET_SR, anchor_bpm=122.0,
            transition_mode="crossfade", effect="none", min_drop_in_sec=hold_l, target_sync_sec=target_l
        )
        self.assertGreaterEqual(len(xf_long), int(12.0 * TARGET_SR))

        # Case 2: Early vocals arriving at +3.5s on incoming track -> 'quick' <= 6.2s
        short_vocal_in = self._make_beat_track(18.0, 122.0, 0.1)
        t_v = np.linspace(0, 18.0, len(short_vocal_in), endpoint=False, dtype=np.float32)[:, None]
        lead_voc = 0.45 * np.sin(2 * np.pi * 1100.0 * t_v) + 0.30 * np.sin(2 * np.pi * 1950.0 * t_v)
        short_vocal_in[int(4.5 * TARGET_SR) :] += lead_voc[int(4.5 * TARGET_SR) :]
        pace_q, target_q, hold_q, _ = assess_adaptive_transition_pace(
            long_out, short_vocal_in, in_start_sample=TARGET_SR, anchor_bpm=122.0, tempo_ratio=1.0
        )
        self.assertEqual(pace_q, "quick")
        self.assertLessEqual(target_q, 6.2)
        self.assertLessEqual(hold_q, 3.0)

        di_quick, _, _ = render_transition_with_effect(
            long_out, short_vocal_in, TARGET_SR, anchor_bpm=122.0,
            transition_mode="drop_in", effect="none", min_drop_in_sec=hold_q, target_sync_sec=target_q
        )
        self.assertLessEqual(len(di_quick), int(7.5 * TARGET_SR))

    def test_jingle_silence_trim_and_late_vocal_free_mixout(self):
        # 1. Jingle with 3.0s audio + 3.0s trailing silence -> trims to ~3.06s & music restores before trailing silence
        t_j = np.linspace(0, 3.0, 3 * TARGET_SR, endpoint=False, dtype=np.float32)
        active_j = 0.25 * np.sin(2 * np.pi * 3500.0 * t_j)
        active_stereo = np.stack([active_j, active_j], axis=1).astype(np.float32)
        padded_jingle = np.vstack([
            np.zeros((int(0.4 * TARGET_SR), 2), dtype=np.float32),
            active_stereo,
            np.zeros((int(3.0 * TARGET_SR), 2), dtype=np.float32),
        ])
        trimmed = trim_jingle_silence(padded_jingle)
        self.assertLess(len(trimmed), int(3.3 * TARGET_SR))
        self.assertGreater(len(trimmed), int(2.8 * TARGET_SR))

        t_m = np.linspace(0, 12.0, 12 * TARGET_SR, endpoint=False, dtype=np.float32)
        m_sig = 0.25 * np.sin(2 * np.pi * 1000.0 * t_m)
        music = np.stack([m_sig, m_sig], axis=1).astype(np.float32)
        mixed = overlay_jingle_with_ducking(music, padded_jingle, start_sample=4 * TARGET_SR, music_duck_gain=0.55)
        # During the formerly-silent 7.5s..9.5s region, music is ALREADY back at 100%
        rms_pre = float(np.sqrt(np.mean(mixed[1 * TARGET_SR : 3 * TARGET_SR] ** 2)))
        rms_after = float(np.sqrt(np.mean(mixed[int(7.6 * TARGET_SR) : int(9.5 * TARGET_SR)] ** 2)))
        self.assertAlmostEqual(rms_after / rms_pre, 1.00, delta=0.03)

        # 2. 280s track with vocals up to 240s and vocal-free rhythmic outro 240..280s -> waits past 235s!
        sr = 11025
        dur = 280.0
        n = int(sr * dur)
        t = np.linspace(0, dur, n, endpoint=False, dtype=np.float32)
        y = np.zeros(n, dtype=np.float32)
        for beat_t in np.arange(0.0, dur - 0.2, 0.5):
            idx = int(beat_t * sr)
            k_len = min(int(0.09 * sr), n - idx)
            kt = np.linspace(0, 0.09, k_len, endpoint=False, dtype=np.float32)
            y[idx : idx + k_len] += 0.80 * np.sin(2 * np.pi * (115.0 * np.exp(-kt * 18.0)) * kt) * np.exp(-kt * 24.0)
        voc = (0.45 * np.sin(2 * np.pi * 750.0 * t) + 0.35 * np.sin(2 * np.pi * 1500.0 * t)).astype(np.float32)
        y[: int(242.0 * sr)] += voc[: int(242.0 * sr)]

        _, mix_out = find_mix_windows_from_pcm(y, sr, 120.0, fallback_in=0.0, fallback_out=255.0)
        self.assertGreaterEqual(mix_out, 236.0)

        # 3. 360s extended track with a clean break at 115s must NOT chop the song in half: stays in final outro (>= 220s)
        dur3 = 360.0
        n3 = int(sr * dur3)
        t3 = np.linspace(0, dur3, n3, endpoint=False, dtype=np.float32)
        y3 = np.zeros(n3, dtype=np.float32)
        for beat_t in np.arange(0.0, dur3 - 0.2, 0.5):
            idx = int(beat_t * sr)
            k_len = min(int(0.09 * sr), n3 - idx)
            kt = np.linspace(0, 0.09, k_len, endpoint=False, dtype=np.float32)
            y3[idx : idx + k_len] += 0.80 * np.sin(2 * np.pi * (115.0 * np.exp(-kt * 18.0)) * kt) * np.exp(-kt * 24.0)
        voc3 = (0.45 * np.sin(2 * np.pi * 750.0 * t3) + 0.35 * np.sin(2 * np.pi * 1500.0 * t3)).astype(np.float32)
        # Put vocals across 20..110s, clean break 110..145s, chorus vocals 145..300s, clean outro 300..360s
        y3[int(20 * sr) : int(110 * sr)] += voc3[int(20 * sr) : int(110 * sr)]
        y3[int(145 * sr) : int(300 * sr)] += voc3[int(145 * sr) : int(300 * sr)]
        _, mix_out3 = find_mix_windows_from_pcm(y3, sr, 120.0, fallback_in=0.0, fallback_out=340.0)
        self.assertGreaterEqual(mix_out3, 230.0)

    def test_low_percussion_track1_triggers_echo_drop_it(self):
        # Track 1 outro: 10s smooth melodic ballad pad with clear vocal/synth swell crest near 4.0s (no kick percussion)
        dur_out = 10.0
        n_out = int(dur_out * TARGET_SR)
        t_out = np.linspace(0.0, dur_out, n_out, endpoint=False, dtype=np.float32)
        env = (0.14 + 0.34 * np.exp(-0.5 * ((t_out - 4.0) / 0.45) ** 2)).astype(np.float32)
        env[int(4.4 * TARGET_SR) :] *= 0.45
        sig = env * (0.7 * np.sin(2 * np.pi * 440.0 * t_out) + 0.3 * np.sin(2 * np.pi * 660.0 * t_out))
        beatless_out_tail = np.stack([sig, sig], axis=1).astype(np.float32)

        rhythmic_in = self._make_beat_track(18.0, 122.0, 0.20)
        in_start = int(1.0 * TARGET_SR)

        mode, reason, _, in_beats = choose_transition_mode(
            beatless_out_tail, rhythmic_in, in_start, 122.0, transition_index=1, last_mode="crossfade"
        )
        self.assertEqual(mode, "echo_drop_in")
        self.assertEqual(reason, "out_low_percussion_echo_drop_it")

        cue = find_best_echo_cue_in_tail(beatless_out_tail)
        self.assertGreaterEqual(cue, int(3.2 * TARGET_SR))
        self.assertLessEqual(cue, int(5.0 * TARGET_SR))

        trans, resume, note = render_echo_drop_it_transition(
            beatless_out_tail, rhythmic_in, in_start, anchor_bpm=122.0, precomputed_in_beats=in_beats
        )
        self.assertIn("echo_drop_in+echo_out", note)
        self.assertGreater(len(trans), int(5.5 * TARGET_SR))
        self.assertLessEqual(float(np.max(np.abs(trans))), 0.96)
        self.assertGreater(resume, in_start + int(2.0 * TARGET_SR))

    def test_clean_fulltrack_bpm_limit_and_echo_drop_it_on_wide_gap(self):
        # Safe +/-8% pitch limit so tracks adapt comfortably without strain or wobbles
        self.assertAlmostEqual(compute_stretch_ratio(106.0, 120.0), 1.08, delta=1e-4)
        self.assertAlmostEqual(compute_stretch_ratio(128.0, 112.0), 0.92, delta=1e-4)

        def make_kick_track(dur_s: float, bpm: float, offset_s: float, tone_hz: float) -> np.ndarray:
            n = int(dur_s * TARGET_SR)
            t = np.linspace(0.0, dur_s, n, endpoint=False, dtype=np.float32)
            y = 0.08 * np.sin(2 * np.pi * tone_hz * t)
            step_s = 60.0 / bpm
            k_len = int(0.08 * TARGET_SR)
            kt = np.linspace(0.0, 0.08, k_len, endpoint=False, dtype=np.float32)
            kick = 0.78 * np.sin(2 * np.pi * (120.0 * np.exp(-kt * 20.0)) * kt) * np.exp(-kt * 24.0)
            b_t = offset_s
            while b_t + 0.08 < dur_s:
                idx = int(round(b_t * TARGET_SR))
                y[idx : idx + k_len] += kick
                b_t += step_s
            return np.stack([y, y], axis=1).astype(np.float32)

        out_tail = make_kick_track(14.0, 120.0, 0.21, 900.0)
        # 1) Too wide local BPM difference (120 vs 112 BPM -> ~7% local > 4%): triggers echo_drop_in instead of forcing
        wide_in = make_kick_track(22.0, 112.0, 1.37, 2400.0)
        est_bpm = estimate_out_tail_bpm(out_tail, fallback_bpm=124.0)
        self.assertAlmostEqual(est_bpm, 120.0, delta=0.8)

        mode_wide, reason_wide, _, _ = choose_transition_mode(
            out_tail, wide_in, int(1.0 * TARGET_SR), 120.0, transition_index=1, last_mode="crossfade", tempo_ratio=1.0
        )
        self.assertEqual(mode_wide, "echo_drop_in")
        self.assertEqual(reason_wide, "wide_bpm_echo_drop_it")

        # 2) Full-track BPM-matched incoming (120 BPM with arbitrary intro offset): clean rock-steady crossfade
        matched_in = make_kick_track(22.0, 120.0, 1.37, 2400.0)
        mode_ok, _, out_beats, in_beats = choose_transition_mode(
            out_tail, matched_in, int(1.0 * TARGET_SR), 120.0, transition_index=1, last_mode="drop_in", tempo_ratio=1.04
        )
        self.assertEqual(mode_ok, "crossfade")

        xf_trans, _ = render_beatlocked_crossfade(
            out_tail, matched_in, int(1.0 * TARGET_SR), anchor_bpm=120.0,
            precomputed_out_beats=out_beats, precomputed_in_beats=in_beats, target_sync_sec=10.0,
        )
        mid_beats = extract_local_beat_samples(xf_trans[int(1.0 * TARGET_SR) : int(9.0 * TARGET_SR)], 120.0, allow_extrapolate=False)
        self.assertGreaterEqual(len(mid_beats), 12)
        ibi_ms = (np.diff(mid_beats) / float(TARGET_SR)) * 1000.0
        self.assertLess(float(np.max(np.abs(ibi_ms - 500.0))), 16.0)

    def test_continuous_mix_track_count_parsing_and_next_picker(self):
        # 1. Explicit track counts vs default continuous mode (None), never misreading '125 BPM' or '80s'
        self.assertIsNone(extract_requested_track_count("lance Concierto Discoteque 125 BPM best 80's"))
        self.assertIsNone(extract_requested_track_count("lance un mix avec discoteque-80s"))
        self.assertEqual(extract_requested_track_count("lance un mix avec 5 titres de discoteque-80s"), 5)
        self.assertEqual(extract_requested_track_count("lance Concierto Discoteque 12 morceaux REMIX-80 en commençant par Celebration"), 12)
        self.assertEqual(extract_requested_track_count("fais un mix de 7 pistes"), 7)

        self.assertEqual(extract_requested_folder_and_start_track("lance Concierto Discoteque 125 BPM best 80's"), ("125 BPM best 80's", ""))
        self.assertEqual(extract_requested_folder_and_start_track("lance un mix avec 5 titres de discoteque-80s"), ("discoteque-80s", ""))
        self.assertEqual(
            extract_requested_folder_and_start_track("lance Concierto Discoteque 12 morceaux REMIX-80 en commençant par Celebration"),
            ("REMIX-80", "Celebration"),
        )
        self.assertEqual(extract_requested_folder_and_start_track("fais un mix de 7 pistes"), ("", ""))

        # 2. pick_next_continuous_track selects unplayed compatible tracks without immediate repeats
        with tempfile.TemporaryDirectory() as td:
            paths = []
            folder_cache = {}
            for i in range(4):
                p = os.path.join(td, f"track_{i}.wav")
                # >100KB dummy wav on disk + fresh folder cache metadata so analysis is instant
                sig = np.zeros((30000, 2), dtype=np.float32)
                sf.write(p, sig, TARGET_SR)
                st = os.stat(p)
                paths.append(p)
                folder_cache[p] = {
                    "path": p,
                    "title": f"Track {i}",
                    "artist": f"Artist {i}",
                    "duration": 220.0,
                    "bpm": 120.0 + i * 0.5,
                    "camelot": "8A",
                    "key_name": "Am",
                    "energy": 0.65,
                    "rms_db": -14.0,
                    "intro_trim": 4.0,
                    "outro_start": 195.0,
                    "vocal_ratio": 0.25,
                    "mtime": float(st.st_mtime),
                    "size": int(st.st_size),
                    "hpss_done": True,
                    "hpss_ver": 6,
                }
            save_folder_analysis_cache(td, folder_cache)

            curr = {
                "path": paths[0],
                "title": "Track 0",
                "artist": "Artist 0",
                "duration": 220.0,
                "bpm": 120.0,
                "camelot": "8A",
                "key": "Am",
                "energy": 0.65,
                "rms_db": -14.0,
                "intro_trim": 4.0,
                "outro_start": 195.0,
            }
            nxt = pick_next_continuous_track(curr, td, played_paths=[paths[0], paths[1], paths[2]], anchor_bpm=120.0)
            self.assertIsNotNone(nxt)
            self.assertEqual(nxt["path"], paths[3])

            # When all tracks have been played, still rotates to a track other than the immediate previous one
            nxt_cycle = pick_next_continuous_track(nxt, td, played_paths=paths, anchor_bpm=120.0)
            self.assertIsNotNone(nxt_cycle)
            self.assertNotEqual(nxt_cycle["path"], paths[3])


if __name__ == "__main__":
    unittest.main()
