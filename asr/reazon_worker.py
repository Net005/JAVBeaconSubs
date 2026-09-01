#!/usr/bin/env python3
"""Bounded-memory ReazonSpeech transcription worker for JAVBeaconSubs."""

import argparse
import json
import math
import os
import sys

def load_model(model_name: str, device: str):
    from nemo.collections.asr.models import EncDecRNNTBPEModel

    model = EncDecRNNTBPEModel.from_pretrained(model_name, map_location=device)
    model.eval()
    return model


def write_ready_marker(path: str, model_name: str) -> None:
    if not path:
        return
    directory = os.path.dirname(path)
    if directory:
        os.makedirs(directory, exist_ok=True)
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        handle.write(model_name + "\n")
    os.replace(temporary, path)


def normalize_text(value: str) -> str:
    return " ".join(value.split())


def transcribe_window(model, waveform, samplerate, device, attempts=2):
    import numpy as np
    import torch
    from reazonspeech.nemo.asr import TranscribeConfig, audio_from_numpy, transcribe

    last_error = None
    for _ in range(attempts):
        try:
            audio = audio_from_numpy(np.asarray(waveform, dtype=np.float32), samplerate)
            return transcribe(model, audio, TranscribeConfig(verbose=False)).segments
        except Exception as error:  # NeMo raises several backend-specific error types.
            last_error = error
            if device == "cuda":
                torch.cuda.empty_cache()
    raise last_error


def window_ranges(total_seconds: float, chunk_seconds: float, overlap_seconds: float):
    chunk_count = max(1, math.ceil(total_seconds / chunk_seconds))
    for index in range(chunk_count):
        core_start = index * chunk_seconds
        core_end = min(total_seconds, core_start + chunk_seconds)
        yield (
            index,
            chunk_count,
            core_start,
            core_end,
            max(0.0, core_start - overlap_seconds),
            min(total_seconds, core_end + overlap_seconds),
        )


def main() -> int:
    import soundfile as sf

    parser = argparse.ArgumentParser()
    parser.add_argument("--input")
    parser.add_argument("--output")
    parser.add_argument("--model", default="reazon-research/reazonspeech-nemo-v2")
    parser.add_argument("--device", choices=("cuda", "cpu"), default="cuda")
    parser.add_argument("--chunk-seconds", type=float, default=45.0)
    parser.add_argument("--overlap-seconds", type=float, default=2.0)
    parser.add_argument("--max-segment-seconds", type=float, default=60.0)
    parser.add_argument("--download-only", action="store_true")
    parser.add_argument("--ready-marker", default="")
    args = parser.parse_args()

    if args.chunk_seconds < 10 or args.overlap_seconds < 0:
        parser.error("chunk-seconds must be >= 10 and overlap-seconds must be >= 0")
    if args.overlap_seconds * 2 >= args.chunk_seconds:
        parser.error("overlap must be less than half the chunk duration")

    model = load_model(args.model, args.device)
    if args.download_only:
        write_ready_marker(args.ready_marker, args.model)
        return 0
    if not args.input or not args.output:
        parser.error("--input and --output are required for transcription")

    output_segments = []
    with sf.SoundFile(args.input) as audio:
        samplerate = audio.samplerate
        total_frames = len(audio)
        total_seconds = total_frames / samplerate
        for index, chunk_count, core_start, core_end, window_start, window_end in window_ranges(
            total_seconds, args.chunk_seconds, args.overlap_seconds
        ):
            first_frame = int(round(window_start * samplerate))
            frame_count = max(0, int(round(window_end * samplerate)) - first_frame)
            audio.seek(first_frame)
            waveform = audio.read(frame_count, dtype="float32", always_2d=True)
            if waveform.shape[1] > 1:
                waveform = waveform.mean(axis=1)
            else:
                waveform = waveform[:, 0]

            segments = transcribe_window(model, waveform, samplerate, args.device)
            for segment in segments:
                start = window_start + float(segment.start_seconds)
                end = window_start + float(segment.end_seconds)
                midpoint = (start + end) / 2
                in_core = midpoint >= core_start and (midpoint < core_end or index == chunk_count - 1)
                text = normalize_text(segment.text)
                if not in_core or not text:
                    continue
                if start < 0 or end <= start or end > total_seconds + 1:
                    raise RuntimeError(f"invalid ReazonSpeech timestamps {start:.3f}-{end:.3f}")
                if end - start > args.max_segment_seconds:
                    raise RuntimeError(f"ReazonSpeech segment exceeds {args.max_segment_seconds:.0f}s")
                output_segments.append({
                    "start_ms": round(start * 1000),
                    "end_ms": round(min(end, total_seconds) * 1000),
                    "text": text,
                })

            progress = round((index + 1) * 100 / chunk_count)
            print(f"progress = {progress}%", file=sys.stderr, flush=True)

    output_segments.sort(key=lambda item: (item["start_ms"], item["end_ms"]))
    payload = {
        "duration_ms": round(total_seconds * 1000),
        "processed_ms": round(total_seconds * 1000),
        "segments": output_segments,
    }
    temporary = args.output + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False)
    os.replace(temporary, args.output)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"ReazonSpeech worker failed: {error}", file=sys.stderr)
        raise SystemExit(1)
