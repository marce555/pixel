#!/bin/bash
set -e

BACKEND_DIR="/home/marceloc/.lmstudio/extensions/backends"
LLAMA_SERVER="${BACKEND_DIR}/llama.cpp-linux-x86_64-nvidia-cuda12-avx2-2.22.0/llama-server"
CUDA_VENDOR="${BACKEND_DIR}/vendor/linux-llama-cuda12-vendor-v1"

MODEL_PATH="/home/marceloc/.cache/huggingface/hub/models--unsloth--Qwen3.5-4B-MTP-GGUF/snapshots/86835bf9949e4d14d6860f7910b1340ad4f271a9/Qwen3.5-4B-UD-Q4_K_XL.gguf"
PORT="52625"
HOST="127.0.0.1"

export LD_LIBRARY_PATH="${CUDA_VENDOR}:${LD_LIBRARY_PATH}"
export CUDA_VISIBLE_DEVICES=0

echo "[CUDA LLM] Starting llama-server on http://${HOST}:${PORT} on RTX 5060 with 20k context..."
exec "${LLAMA_SERVER}" \
    -m "${MODEL_PATH}" \
    --host "${HOST}" \
    --port "${PORT}" \
    -ngl 99 \
    -c 20480 \
    -b 2048 \
    -ub 512 \
    -np 1 \
    --flash-attn on \
    --reasoning off \
    -a "qwen3.5:9b,qwen3.5-4b,qwen3.5,qwen3.5-9b,default"
