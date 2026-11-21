#!/usr/bin/env python3
"""
Persistent local CPU microservice for speaker embedding extraction via sherpa-onnx.
Exposes GET /health and POST /embed {"wav_path": "...", "max_seconds": 15.0} over localhost HTTP.
"""

import argparse
import json
import signal
import sys
import threading
import time
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import sherpa_onnx


def load_wav_mono_float32(wav_path: str, max_seconds: float = 15.0):
    """Read a PCM WAV file using stdlib wave and convert to mono float32 in [-1, 1]."""
    with wave.open(wav_path, "rb") as wf:
        channels = wf.getnchannels()
        sample_width = wf.getsampwidth()
        sample_rate = wf.getframerate()
        total_frames = wf.getnframes()
        if sample_rate <= 0 or total_frames <= 0:
            return np.zeros(0, dtype=np.float32), sample_rate, 0.0

        duration_ms = (float(total_frames) / float(sample_rate)) * 1000.0
        max_frames = int(sample_rate * max_seconds) if max_seconds > 0 else total_frames
        frames_to_read = min(total_frames, max_frames)
        raw = wf.readframes(frames_to_read)

    if sample_width == 2:
        pcm = np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0
    elif sample_width == 4:
        pcm = np.frombuffer(raw, dtype=np.int32).astype(np.float32) / 2147483648.0
    elif sample_width == 1:
        pcm = (np.frombuffer(raw, dtype=np.uint8).astype(np.float32) - 128.0) / 128.0
    else:
        raise ValueError(f"Unsupported sample width: {sample_width}")

    if channels > 1:
        pcm = pcm.reshape(-1, channels).mean(axis=1)

    return pcm, sample_rate, duration_ms


def make_handler(extractor: sherpa_onnx.SpeakerEmbeddingExtractor, lock: threading.Lock):
    class SpeakerHandler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format, *args):
            # Keep stdout/stderr quiet except startup and errors.
            return

        def _send_json(self, status: int, payload: dict):
            body = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path == "/health":
                self._send_json(200, {"ok": True, "dim": extractor.dim})
                return
            self._send_json(404, {"error": "not_found"})

        def do_POST(self):
            if self.path != "/embed":
                self._send_json(404, {"error": "not_found"})
                return

            try:
                content_len = int(self.headers.get("Content-Length", "0"))
                raw_body = self.rfile.read(content_len)
                req = json.loads(raw_body.decode("utf-8"))
                wav_path = str(req.get("wav_path", "")).strip()
                max_seconds = float(req.get("max_seconds", 15.0))
            except Exception as exc:
                self._send_json(400, {"error": f"bad_request: {exc}"})
                return

            if not wav_path:
                self._send_json(400, {"error": "wav_path_required"})
                return

            try:
                samples, sample_rate, duration_ms = load_wav_mono_float32(wav_path, max_seconds=max_seconds)
            except FileNotFoundError:
                self._send_json(404, {"error": "missing_file"})
                return
            except Exception as exc:
                self._send_json(400, {"error": f"invalid_wav: {exc}"})
                return

            if len(samples) == 0:
                self._send_json(422, {"error": "audio_too_short", "duration_ms": duration_ms})
                return

            t0 = time.monotonic()
            with lock:
                stream = extractor.create_stream()
                stream.accept_waveform(sample_rate=sample_rate, waveform=samples)
                stream.input_finished()
                if not extractor.is_ready(stream):
                    self._send_json(422, {"error": "audio_too_short", "duration_ms": duration_ms})
                    return
                emb = np.array(extractor.compute(stream), dtype=np.float32)

            norm = float(np.linalg.norm(emb))
            if norm <= 1e-8 or not np.isfinite(norm):
                self._send_json(422, {"error": "degenerate_embedding", "duration_ms": duration_ms})
                return

            emb = emb / norm
            infer_ms = (time.monotonic() - t0) * 1000.0
            self._send_json(
                200,
                {
                    "embedding": emb.tolist(),
                    "dim": int(emb.shape[0]),
                    "duration_ms": round(duration_ms, 1),
                    "infer_ms": round(infer_ms, 1),
                },
            )

    return SpeakerHandler


def main():
    parser = argparse.ArgumentParser(description="Pixel Speaker Embedding Worker")
    parser.add_argument("--model", required=True, help="Path to ONNX speaker model")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=8090, help="Bind port")
    parser.add_argument("--threads", type=int, default=2, help="ONNX CPU threads")
    args = parser.parse_args()

    cfg = sherpa_onnx.SpeakerEmbeddingExtractorConfig(
        model=args.model,
        num_threads=args.threads,
        provider="cpu",
    )
    extractor = sherpa_onnx.SpeakerEmbeddingExtractor(cfg)
    lock = threading.Lock()

    server = ThreadingHTTPServer((args.host, args.port), make_handler(extractor, lock))

    def shutdown_handler(_signum, _frame):
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown_handler)
    signal.signal(signal.SIGINT, shutdown_handler)

    print(f"[SpeakerWorker] Ready on http://{args.host}:{args.port} (dim={extractor.dim})", flush=True)
    try:
        server.serve_forever()
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
