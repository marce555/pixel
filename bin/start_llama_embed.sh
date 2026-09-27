#!/bin/bash
set -e

BACKEND_DIR="/home/marceloc/.lmstudio/extensions/backends"
LLAMA_SERVER="${BACKEND_DIR}/llama.cpp-linux-x86_64-nvidia-cuda12-avx2-2.22.0/llama-server"
CUDA_VENDOR="${BACKEND_DIR}/vendor/linux-llama-cuda12-vendor-v1"

MODEL_PATH="/home/marceloc/.lmstudio/.internal/bundled-models/nomic-ai/nomic-embed-text-v1.5-GGUF/nomic-embed-text-v1.5.Q4_K_M.gguf"
PORT="52626"
HOST="127.0.0.1"

export LD_LIBRARY_PATH="${CUDA_VENDOR}:${LD_LIBRARY_PATH}"
export CUDA_VISIBLE_DEVICES=0

echo "[CUDA Embeddings] Starting nomic-embed-text on http://${HOST}:${PORT}..."
exec "${LLAMA_SERVER}" \
    -m "${MODEL_PATH}" \
    --host "${HOST}" \
    --port "${PORT}" \
    --embeddings \
    -ngl 99 \
    -c 2048 \
    -b 512 \
    -ub 512 \
    -np 1 \
    -a "embed-gemma:300m,nomic-embed-text,nomic-embed-text-v1.5,default"
