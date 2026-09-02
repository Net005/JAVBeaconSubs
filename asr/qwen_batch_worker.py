#!/usr/bin/env python3
"""Isolated Qwen ASR phase; exiting releases its complete CUDA context."""

from __future__ import annotations

import argparse
import ctypes
import json
import os
import signal
import sys
import time
from typing import Any

import soundfile as sf

import qwen_pipeline as pipeline


def terminate_if_parent_exits() -> None:
    """Avoid orphaning a CUDA owner when a cancelled job kills its parent."""
    if not sys.platform.startswith("linux"):
        return
    parent = os.getppid()
    libc = ctypes.CDLL(None)
    if libc.prctl(1, signal.SIGTERM) != 0:  # PR_SET_PDEATHSIG
        raise OSError("could not configure parent-death signal")
    if os.getppid() != parent:
        raise SystemExit("parent exited while Qwen worker was starting")


def main() -> int:
    terminate_if_parent_exits()
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--regions", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--context", default="")
    parser.add_argument("--model", required=True)
    parser.add_argument("--revision", default="")
    parser.add_argument("--device", choices=("cuda", "cpu"), required=True)
    parser.add_argument("--batch-size", type=int, default=4)
    parser.add_argument("--mode", choices=("fast", "balanced", "high_accuracy"), default="balanced")
    parser.add_argument("--debug", action="store_true")
    args = parser.parse_args()

    with open(args.regions, encoding="utf-8") as handle:
        entries: list[dict[str, Any]] = json.load(handle)
    waveform, samplerate = sf.read(args.input, dtype="float32", always_2d=False)
    if waveform.ndim > 1:
        waveform = waveform.mean(axis=1)
    clips = [(waveform[int(item["start"]):int(item["end"])], samplerate) for item in entries]

    before_load = pipeline.gpu_snapshot()
    torch_before_load = pipeline.torch_memory_snapshot(initialize=args.device == "cuda")
    after_context = pipeline.gpu_snapshot()
    model = pipeline.load_qwen(args.model, args.revision, args.device, max(1, args.batch_size))
    after_load = pipeline.gpu_snapshot()
    primary_started = time.monotonic()
    texts = pipeline.qwen_transcribe(model, clips, args.context, max(1, args.batch_size))
    qwen_primary_seconds = time.monotonic() - primary_started
    after_asr = pipeline.gpu_snapshot()
    torch_after_asr = pipeline.torch_memory_snapshot()
    retry_positions: list[int] = []
    retry_reasons: dict[str, str] = {}
    for position, (entry, text) in enumerate(zip(entries, texts)):
        seconds = (int(entry["end"]) - int(entry["start"])) / samplerate
        probability = float(entry.get("speech_probability", 0.0))
        classification = str(entry.get("classification", "speech"))
        reasons = pipeline.suspicion_reasons(text, seconds, probability, args.context)
        eligible, reason = pipeline.should_retry_qwen(text, reasons, classification, probability)
        if "prompt_leakage" in reasons or (args.mode != "fast" and eligible):
            retry_positions.append(position)
            retry_reasons[str(int(entry["index"]))] = (
                "prompt_leakage_unresolved" if "prompt_leakage" in reasons else reason
            )
    retry_started = time.monotonic()
    retry_texts = pipeline.qwen_transcribe(
        model, [clips[position] for position in retry_positions], "", max(1, args.batch_size)
    ) if retry_positions else []
    qwen_retry_seconds = time.monotonic() - retry_started if retry_positions else 0.0
    after_retries = pipeline.gpu_snapshot()
    cleanup = pipeline.dispose_qwen(model)
    del model
    torch_after_cleanup = pipeline.torch_memory_snapshot()
    surviving = pipeline.surviving_cuda_tensors() if args.debug else {"count": 0, "total_bytes": 0, "tensors": []}
    after_cleanup = pipeline.gpu_snapshot()

    pipeline.write_json(args.output, {
        "results": [
            {"index": int(item["index"]), "text": text}
            for item, text in zip(entries, texts)
        ],
        "diagnostics": {
            "retry_results": [
                {"index": int(entries[position]["index"]), "text": text}
                for position, text in zip(retry_positions, retry_texts)
            ],
            "retry_reasons": retry_reasons,
            "qwen_primary_seconds": round(qwen_primary_seconds, 3),
            "qwen_retry_seconds": round(qwen_retry_seconds, 3),
            "gpu_before_load": before_load,
            "gpu_after_context": after_context,
            "gpu_after_load": after_load,
            "gpu_after_asr": after_asr,
            "gpu_after_retries": after_retries,
            "gpu_after_cleanup": after_cleanup,
            "torch_before_load": torch_before_load,
            "torch_after_asr": torch_after_asr,
            "torch_after_cleanup": torch_after_cleanup,
            "surviving_cuda_tensors_after_cleanup": surviving,
            "cleanup": cleanup,
        },
    })
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"Isolated Qwen ASR phase failed: {error}", file=sys.stderr)
        raise SystemExit(1)
