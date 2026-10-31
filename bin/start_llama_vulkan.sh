#!/bin/bash
set -e

BACKEND_DIR="/home/marceloc/.lmstudio/extensions/backends/llama.cpp-linux-x86_64-vulkan-avx2-2.20.1"
LLAMA_SERVER="${BACKEND_DIR}/llama-server"

MODEL_PATH="/home/marceloc/.cache/huggingface/hub/models--unsloth--Qwen3-4B-GGUF/snapshots/22c9fc8a8c7700b76a1789366280a6a1789366280a6a5a1ad1120/Qwen3-4B-Q4_0.gguf"
if [ ! -f "${MODEL_PATH}" ]; then
    MODEL_PATH="/home/marceloc/.cache/huggingface/hub/models--unsloth--Qwen3-4B-GGUF/snapshots/22c9fc8a8c7700b76a1789366280a6a5a1ad1120/Qwen3-4B-Q4_0.gguf"
fi
PORT="18081"
HOST="127.0.0.1"

export LD_LIBRARY_PATH="${BACKEND_DIR}:${LD_LIBRARY_PATH}"
export CUDA_VISIBLE_DEVICES=""
export VK_ICD_FILENAMES="/usr/share/vulkan/icd.d/radeon_icd.json"

echo "[Vulkan iGPU LLM] Starting llama-server on http://${HOST}:${PORT} on AMD Radeon 890M (Vulkan0)..."
exec "${LLAMA_SERVER}" \
    -m "${MODEL_PATH}" \
    --device Vulkan0 \
    --host "${HOST}" \
    --port "${PORT}" \
    -ngl 99 \
    -c 8192 \
    -b 2048 \
    -ub 512 \
    -np 1 \
    --flash-attn on \
    --reasoning off \
    -a "qwen3.5:9b,qwen3:4b,qwen3.5-4b,qwen3.5,default"
