#!/usr/bin/env python3
"""Transcribe selected WAV regions with ReazonSpeech in its isolated venv."""

import argparse
import json
import os
import sys


def normalize_text(value: str) -> str:
    return " ".join(str(value or "").split()).strip()


def write_json(path: str, payload) -> None:
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, separators=(",", ":"))
    os.replace(temporary, path)


def main() -> int:
    import numpy as np
    import soundfile as sf
    import torch
    from nemo.collections.asr.models import EncDecRNNTBPEModel
    from reazonspeech.nemo.asr import TranscribeConfig, audio_from_numpy, transcribe

    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--regions", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--model", default="reazon-research/reazonspeech-nemo-v2")
    parser.add_argument("--device", choices=("cuda", "cpu"), default="cuda")
    args = parser.parse_args()

    with open(args.regions, encoding="utf-8") as handle:
        regions = json.load(handle)
    waveform, samplerate = sf.read(args.input, dtype="float32", always_2d=False)
    if waveform.ndim > 1:
        waveform = waveform.mean(axis=1)

    model = EncDecRNNTBPEModel.from_pretrained(args.model, map_location=args.device)
    model.eval()
    results = []
    for position, region in enumerate(regions):
        index = int(region["index"])
        start = max(0, int(region["start"]))
        end = min(len(waveform), int(region["end"]))
        text, error = "", ""
        try:
            audio = audio_from_numpy(np.asarray(waveform[start:end], dtype=np.float32), samplerate)
            result = transcribe(model, audio, TranscribeConfig(verbose=False))
            text = normalize_text("".join(segment.text for segment in result.segments))
        except Exception as exc:  # Keep one failed region from aborting the movie.
            error = str(exc)
            if args.device == "cuda":
                torch.cuda.empty_cache()
        results.append({"index": index, "text": text, "error": error})
        print(f"progress = {round((position + 1) * 100 / max(1, len(regions)))}%", file=sys.stderr, flush=True)
    write_json(args.output, {"results": results})
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"ReazonSpeech batch worker failed: {error}", file=sys.stderr)
        raise SystemExit(1)
