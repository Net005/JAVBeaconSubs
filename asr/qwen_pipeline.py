#!/usr/bin/env python3
"""Qwen-first Japanese ASR pipeline for JAVBeaconSubs.

The worker deliberately loads only one large model family at a time.  Qwen ASR
runs first, ReazonSpeech is loaded only for fallback candidates, whisper.cpp is
invoked only to break meaningful disagreement, and the Qwen aligner is loaded
after transcription models have been released.
"""

from __future__ import annotations

import argparse
import difflib
import gc
import json
import math
import os
import re
import subprocess
import sys
import tempfile
import time
import unicodedata
from dataclasses import dataclass, field
from typing import Any, Iterable

import numpy as np


PIPELINE_VERSION = "qwen-first-v2"
JAPANESE = "Japanese"
PROFILE_CONTEXT = {
    "standard": "日本語",
    "jav": "日本語・成人向け映像",
    "giga": "日本語・特撮・ヒロインアクション",
}
PROFILE_ALIASES = {"tokusatsu": "giga", "akiba": "giga"}
LEGACY_CONTEXTS = (
    "Japanese dialogue. Preserve names, short replies, incomplete speech, and vocal reactions.",
    "Japanese adult-video dialogue. Preserve names, explicit vocabulary, short replies, whispers, and vocal reactions without inventing sentences.",
    "Japanese tokusatsu dialogue. Preserve character, organization, attack, and transformation names; also retain shouts and incomplete speech.",
    "Japanese dialogue from noisy or compressed recordings. Preserve names, short replies, and incomplete speech; do not guess through noise.",
)
LEAK_FRAGMENTS = (
    "without inventing",
    "inventing",
    "preserve names",
    "incomplete speech",
    "vocal reactions",
    "explicit vocabulary",
    "transformation names",
    "do not guess through noise",
    "japanese dialogue",
)
PUNCTUATION = set("。！？!?…")
SHORT_REACTION = re.compile(r"^[あいうえおんっぁぃぅぇぉー～〜はぁ]+[。！？!?…]*$")


@dataclass
class Region:
    start: int
    end: int
    speech_probability: float
    classification: str


@dataclass
class Candidate:
    engine: str
    model: str
    text: str
    suspicion: list[str] = field(default_factory=list)
    leak_similarity: float = 0.0
    matched_context_fragment: str = ""
    retry_without_context: bool = False


@dataclass
class Decision:
    region: Region
    selected: Candidate
    candidates: dict[str, Candidate]
    comparison_score: float
    confidence: float
    quality_state: str
    alignment: str = "pending"
    aligned_items: list[dict[str, Any]] = field(default_factory=list)


def normalize_text(value: str) -> str:
    return " ".join(str(value or "").split()).strip()


def comparison_text(value: str) -> str:
    value = unicodedata.normalize("NFKC", value)
    return "".join(ch for ch in value if not ch.isspace() and not unicodedata.category(ch).startswith("P"))


def normalize_profile(value: str) -> str:
    value = str(value or "").strip().lower()
    return PROFILE_ALIASES.get(value, value)


def leakage_text(value: str) -> str:
    return comparison_text(value).casefold()


def detect_prompt_leakage(text: str, active_context: str) -> tuple[bool, float, str]:
    """Detect prompt echoes without flagging ordinary short Japanese overlap."""
    output = leakage_text(text)
    if not output:
        return False, 0.0, ""
    contexts = [active_context, *LEGACY_CONTEXTS]
    best_score, best_fragment = 0.0, ""
    for context in contexts:
        prompt = leakage_text(context)
        if len(prompt) < 6:
            continue
        score = similarity(output, prompt)
        if prompt in output or output in prompt and len(output) >= 10:
            score = max(score, min(len(output), len(prompt)) / max(len(output), len(prompt)))
        if score > best_score:
            best_score, best_fragment = score, context
        if prompt in output or (len(output) >= 12 and output in prompt) or score >= 0.72:
            return True, round(score, 3), context
    for fragment in LEAK_FRAGMENTS:
        normalized = leakage_text(fragment)
        if normalized in output:
            return True, 1.0, fragment
    latin = [ch for ch in text if ch.isascii() and ch.isalpha()]
    visible = [ch for ch in text if not ch.isspace() and not unicodedata.category(ch).startswith("P")]
    if len(latin) >= 12 and len(latin) / max(1, len(visible)) >= 0.65:
        return True, round(len(latin) / len(visible), 3), "suspicious_latin_output"
    return False, round(best_score, 3), best_fragment


def similarity(left: str, right: str) -> float:
    a, b = comparison_text(left), comparison_text(right)
    if not a or not b:
        return 1.0 if a == b else 0.0
    return difflib.SequenceMatcher(None, a, b, autojunk=False).ratio()


def repeated_phrase(text: str) -> bool:
    value = comparison_text(text)
    if len(value) < 8:
        return False
    for width in range(1, min(12, len(value) // 3) + 1):
        unit = value[:width]
        if len(unit) and value.count(unit) >= 4 and len(unit) * value.count(unit) >= len(value) * 0.75:
            return True
    return False


def suspicion_reasons(text: str, duration: float, speech_probability: float, context: str = "") -> list[str]:
    value = comparison_text(text)
    reasons: list[str] = []
    if not value:
        reasons.append("empty_transcript")
    if duration > 0 and len(value) / duration > 16:
        reasons.append("text_duration_ratio")
    if repeated_phrase(value):
        reasons.append("pathological_repetition")
    if speech_probability < 0.28 and len(value) >= 8:
        reasons.append("weak_speech_conflict")
    leaked, _, _ = detect_prompt_leakage(text, context)
    if leaked:
        reasons.append("prompt_leakage")
    return reasons


def frame_rms(waveform: np.ndarray, frame_samples: int) -> np.ndarray:
    count = max(1, math.ceil(len(waveform) / frame_samples))
    padded = np.pad(waveform, (0, count * frame_samples - len(waveform)))
    frames = padded.reshape(count, frame_samples)
    return np.sqrt(np.mean(np.square(frames, dtype=np.float64), axis=1))


def split_long_region(start: int, end: int, maximum: int) -> Iterable[tuple[int, int]]:
    while end - start > maximum:
        yield start, start + maximum
        start += maximum
    if end > start:
        yield start, end


def detect_regions(
    waveform: np.ndarray,
    samplerate: int,
    threshold: float,
    min_speech_ms: int,
    merge_silence_ms: int,
    pre_roll_ms: int,
    post_roll_ms: int,
    max_segment_seconds: float,
) -> list[Region]:
    """Recall-biased energy VAD with bounded, padded regions.

    The configurable threshold is a multiplier over an adaptive noise floor.
    Very quiet frames are retained as ambiguous vocalizations rather than being
    promoted to lexical speech with invented text.
    """
    frame_ms = 30
    frame_samples = max(1, round(samplerate * frame_ms / 1000))
    rms = frame_rms(waveform, frame_samples)
    noise = max(float(np.percentile(rms, 20)), 1e-5)
    peak = max(float(np.percentile(rms, 98)), noise)
    if peak / noise < 1.5:
        cutoff = max(0.0008, peak * 0.5)
    else:
        cutoff = max(0.0008, noise * max(1.05, threshold), peak * 0.012)
    active = rms >= cutoff
    bridge = max(0, round(merge_silence_ms / frame_ms))
    if bridge:
        silent = np.flatnonzero(~active)
        del silent  # keeps the implementation vector-friendly without storing audio.
        index = 0
        while index < len(active):
            if active[index]:
                index += 1
                continue
            begin = index
            while index < len(active) and not active[index]:
                index += 1
            if begin > 0 and index < len(active) and index - begin <= bridge:
                active[begin:index] = True
    minimum = max(1, round(min_speech_ms / frame_ms))
    pre = round(pre_roll_ms * samplerate / 1000)
    post = round(post_roll_ms * samplerate / 1000)
    maximum = max(frame_samples, round(max_segment_seconds * samplerate))
    regions: list[Region] = []
    index = 0
    while index < len(active):
        if not active[index]:
            index += 1
            continue
        begin = index
        while index < len(active) and active[index]:
            index += 1
        if index - begin < minimum:
            continue
        raw_start = begin * frame_samples
        raw_end = min(len(waveform), index * frame_samples)
        start, end = max(0, raw_start - pre), min(len(waveform), raw_end + post)
        local = rms[begin:index]
        probability = float(np.clip(np.mean(np.clip(local / max(cutoff * 2.5, 1e-8), 0, 1)), 0, 1))
        classification = "speech" if probability >= 0.35 else "ambiguous_vocalization"
        for part_start, part_end in split_long_region(start, end, maximum):
            regions.append(Region(part_start, part_end, probability, classification))
    return regions


def release_cuda() -> None:
    gc.collect()
    try:
        import torch

        if torch.cuda.is_available():
            torch.cuda.synchronize()
            torch.cuda.empty_cache()
            torch.cuda.ipc_collect()
    except Exception:
        pass


def torch_settings(device: str) -> tuple[Any, str]:
    import torch

    if device == "cuda":
        return torch.bfloat16, "cuda:0"
    return torch.float32, "cpu"


def load_qwen(model_name: str, revision: str, device: str, batch_size: int):
    from qwen_asr import Qwen3ASRModel

    dtype, device_map = torch_settings(device)
    return Qwen3ASRModel.from_pretrained(
        model_name,
        revision=revision or None,
        dtype=dtype,
        device_map=device_map,
        max_inference_batch_size=max(1, batch_size),
        max_new_tokens=256,
    )


def qwen_transcribe(model, clips: list[tuple[np.ndarray, int]], context: str, batch_size: int) -> list[str]:
    out: list[str] = []
    for start in range(0, len(clips), batch_size):
        batch = clips[start : start + batch_size]
        try:
            results = model.transcribe(
                audio=batch,
                context=[context] * len(batch),
                language=[JAPANESE] * len(batch),
                return_time_stamps=False,
            )
            out.extend(normalize_text(item.text) for item in results)
        except Exception:
            # Isolate a malformed/problematic segment instead of losing the movie.
            for clip in batch:
                try:
                    result = model.transcribe(audio=clip, context=context, language=JAPANESE)[0]
                    out.append(normalize_text(result.text))
                except Exception:
                    out.append("")
    return out


def reazon_batch_transcribe(
    python: str,
    script: str,
    input_path: str,
    regions: list[Region],
    indexes: list[int],
    model_name: str,
    device: str,
) -> dict[int, str]:
    with tempfile.TemporaryDirectory(prefix="javbeaconsubs-reazon-") as directory:
        manifest = os.path.join(directory, "regions.json")
        output = os.path.join(directory, "results.json")
        # The worker expects a top-level list to keep its input intentionally simple.
        temporary = manifest + ".list"
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(
                [{"index": index, "start": regions[index].start, "end": regions[index].end} for index in indexes],
                handle,
                separators=(",", ":"),
            )
        os.replace(temporary, manifest)
        command = [
            python,
            script,
            "--input",
            input_path,
            "--regions",
            manifest,
            "--output",
            output,
            "--model",
            model_name,
            "--device",
            device,
        ]
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=None)
        with open(output, encoding="utf-8") as handle:
            payload = json.load(handle)
        return {int(item["index"]): normalize_text(item.get("text", "")) for item in payload.get("results", [])}


def whisper_batch_transcribe(
    binary: str, model: str, clips: list[tuple[np.ndarray, int]], indexes: list[int], language: str
) -> dict[int, str]:
    """Run whisper.cpp once for the complete fallback phase.

    Selected clips are concatenated with a silence delimiter. Whisper timestamps
    are then mapped back to their stable source-region indexes.
    """
    import soundfile as sf

    if not indexes:
        return {}
    samplerate = clips[indexes[0]][1]
    separator = np.zeros(samplerate, dtype=np.float32)
    pieces: list[np.ndarray] = []
    spans: list[tuple[int, float, float]] = []
    cursor = 0
    for index in indexes:
        waveform, rate = clips[index]
        if rate != samplerate:
            raise ValueError("fallback clips have inconsistent sample rates")
        start = cursor / samplerate
        pieces.append(np.asarray(waveform, dtype=np.float32))
        cursor += len(waveform)
        spans.append((index, start, cursor / samplerate))
        pieces.append(separator)
        cursor += len(separator)
    with tempfile.TemporaryDirectory(prefix="javbeaconsubs-whisper-") as directory:
        audio_path = os.path.join(directory, "fallback.wav")
        prefix = os.path.join(directory, "result")
        sf.write(audio_path, np.concatenate(pieces), samplerate, subtype="PCM_16")
        command = [binary, "-m", model, "-f", audio_path, "-l", language, "-ojf", "-of", prefix, "-ng", "-nt"]
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        with open(prefix + ".json", encoding="utf-8") as handle:
            doc = json.load(handle)
        results: dict[int, list[str]] = {index: [] for index in indexes}
        for item in doc.get("transcription", []):
            offsets = item.get("offsets", {})
            start_ms, end_ms = offsets.get("from"), offsets.get("to")
            if not isinstance(start_ms, (int, float)) or not isinstance(end_ms, (int, float)):
                continue
            midpoint = (float(start_ms) + float(end_ms)) / 2000
            for index, start, end in spans:
                if start <= midpoint <= end:
                    results[index].append(str(item.get("text", "")))
                    break
        return {index: normalize_text("".join(parts)) for index, parts in results.items()}


def meaningful_candidate(candidate: Candidate | None) -> bool:
    return candidate is not None and bool(comparison_text(candidate.text)) and "prompt_leakage" not in candidate.suspicion


def should_use_whisper(qwen: Candidate, reazon: Candidate | None, threshold: float = 0.58) -> bool:
    if not meaningful_candidate(qwen) or not meaningful_candidate(reazon):
        return False
    if not qwen.suspicion:
        return False
    return similarity(qwen.text, reazon.text) < threshold


def alignment_integrity(canonical: str, aligned: str) -> dict[str, Any]:
    source, result = comparison_text(canonical), comparison_text(aligned)
    if not source:
        return {"valid": not result, "similarity": 1.0 if not result else 0.0, "coverage": 1.0, "omission_ratio": 0.0}
    score = similarity(source, result)
    matched = sum(block.size for block in difflib.SequenceMatcher(None, source, result, autojunk=False).get_matching_blocks())
    coverage = matched / len(source)
    omission = max(0.0, (len(source) - len(result)) / len(source))
    prefix_loss = bool(source[:2] and not result.startswith(source[:2]))
    suffix_loss = bool(source[-2:] and not result.endswith(source[-2:]))
    valid = score >= 0.86 and coverage >= 0.9 and omission <= 0.1 and not prefix_loss and not suffix_loss
    return {
        "valid": valid,
        "similarity": round(score, 3),
        "coverage": round(coverage, 3),
        "omission_ratio": round(omission, 3),
        "prefix_loss": prefix_loss,
        "suffix_loss": suffix_loss,
    }


def choose_candidate(candidates: dict[str, Candidate]) -> tuple[Candidate, float]:
    usable = [item for item in candidates.values() if comparison_text(item.text)]
    if not usable:
        return next(iter(candidates.values())), 0.0
    if len(usable) == 1:
        return usable[0], 0.0
    scores: dict[str, float] = {}
    for candidate in usable:
        agreements = [similarity(candidate.text, other.text) for other in usable if other.engine != candidate.engine]
        scores[candidate.engine] = (sum(agreements) / len(agreements)) - min(0.35, len(candidate.suspicion) * 0.12)
    chosen = max(usable, key=lambda item: (scores[item.engine], item.engine == "qwen3"))
    return chosen, max(0.0, min(1.0, scores[chosen.engine]))


def confidence_for(decision: Decision, alignment_ok: bool) -> float:
    score = 0.45 + decision.region.speech_probability * 0.25 + decision.comparison_score * 0.2
    score += 0.1 if alignment_ok else -0.18
    score -= min(0.3, 0.1 * len(decision.selected.suspicion))
    return round(max(0.0, min(1.0, score)), 3)


def split_alignment(items: list[dict[str, Any]], max_chars: int = 24, max_duration: float = 5.5) -> list[list[dict[str, Any]]]:
    groups: list[list[dict[str, Any]]] = []
    current: list[dict[str, Any]] = []
    for item in items:
        current.append(item)
        text = "".join(part["text"] for part in current)
        duration = current[-1]["end_time"] - current[0]["start_time"]
        if len(text) >= max_chars or duration >= max_duration or (text and text[-1] in PUNCTUATION):
            groups.append(current)
            current = []
    if current:
        groups.append(current)
    return groups


def load_aligner(model_name: str, revision: str, device: str):
    from qwen_asr import Qwen3ForcedAligner

    dtype, device_map = torch_settings(device)
    return Qwen3ForcedAligner.from_pretrained(model_name, revision=revision or None, dtype=dtype, device_map=device_map)


def progress(value: int, message: str) -> None:
    print(f"progress = {max(0, min(100, value))}% {message}", file=sys.stderr, flush=True)


def write_json(path: str, payload: dict[str, Any]) -> None:
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, separators=(",", ":"))
    os.replace(temporary, path)


def main() -> int:
    import soundfile as sf

    parser = argparse.ArgumentParser()
    parser.add_argument("--input")
    parser.add_argument("--output")
    parser.add_argument("--device", choices=("cuda", "cpu"), default="cuda")
    parser.add_argument("--mode", choices=("fast", "balanced", "high_accuracy"), default="balanced")
    parser.add_argument("--profile", choices=tuple(PROFILE_CONTEXT) + tuple(PROFILE_ALIASES), default="jav")
    parser.add_argument("--context", default="")
    parser.add_argument("--qwen-model", default="Qwen/Qwen3-ASR-1.7B")
    parser.add_argument("--qwen-revision", default="7278e1e70fe206f11671096ffdd38061171dd6e5")
    parser.add_argument("--aligner-model", default="Qwen/Qwen3-ForcedAligner-0.6B")
    parser.add_argument("--aligner-revision", default="c7cbfc2048c462b0d63a45797104fc9db3ad62b7")
    parser.add_argument("--reazon-model", default="reazon-research/reazonspeech-nemo-v2")
    parser.add_argument("--reazon-python", default="python3")
    parser.add_argument("--reazon-script", default="reazon_batch_worker.py")
    parser.add_argument("--whisper-binary", default="whisper-cli")
    parser.add_argument("--whisper-model", default="")
    parser.add_argument("--disable-reazon", action="store_true")
    parser.add_argument("--disable-whisper", action="store_true")
    parser.add_argument("--batch-size", type=int, default=4)
    parser.add_argument("--vad-threshold", type=float, default=1.45)
    parser.add_argument("--vad-min-speech-ms", type=int, default=100)
    parser.add_argument("--vad-merge-silence-ms", type=int, default=500)
    parser.add_argument("--vad-pre-roll-ms", type=int, default=350)
    parser.add_argument("--vad-post-roll-ms", type=int, default=600)
    parser.add_argument("--max-segment-seconds", type=float, default=30.0)
    parser.add_argument("--debug", action="store_true")
    parser.add_argument("--download-only", action="store_true")
    parser.add_argument("--ready-marker", default="")
    args = parser.parse_args()
    args.profile = normalize_profile(args.profile)

    started = time.monotonic()
    if args.download_only:
        # Hugging Face cache semantics avoid downloading unchanged revisions.
        from huggingface_hub import snapshot_download

        revisions = {}
        for model_name, revision in ((args.qwen_model, args.qwen_revision), (args.aligner_model, args.aligner_revision)):
            path = snapshot_download(model_name, revision=revision or None)
            revisions[model_name] = {"revision": revision, "path": os.path.realpath(path)}
        if args.ready_marker:
            os.makedirs(os.path.dirname(args.ready_marker), exist_ok=True)
            write_json(args.ready_marker, revisions)
        return 0
    if not args.input or not args.output:
        parser.error("--input and --output are required")

    waveform, samplerate = sf.read(args.input, dtype="float32", always_2d=False)
    if waveform.ndim > 1:
        waveform = waveform.mean(axis=1)
    duration = len(waveform) / samplerate
    progress(2, "detecting_speech")
    vad_started = time.monotonic()
    regions = detect_regions(
        waveform,
        samplerate,
        args.vad_threshold,
        args.vad_min_speech_ms,
        args.vad_merge_silence_ms,
        args.vad_pre_roll_ms,
        args.vad_post_roll_ms,
        args.max_segment_seconds,
    )
    if not regions:
        raise RuntimeError("dialogue-aware VAD found no speech or vocalization segments")
    vad_seconds = time.monotonic() - vad_started
    clips = [(waveform[item.start : item.end], samplerate) for item in regions]

    progress(8, "loading_qwen")
    qwen_started = time.monotonic()
    qwen = load_qwen(args.qwen_model, args.qwen_revision, args.device, args.batch_size)
    # Keep ASR conditioning deliberately tiny.  --context remains accepted for
    # backward-compatible command lines, but legacy prose is never sent to the
    # recognizer because it can be echoed into the transcript.
    context = PROFILE_CONTEXT[args.profile]
    qwen_texts = qwen_transcribe(qwen, clips, context, max(1, args.batch_size))
    leakage_indexes = [index for index, text in enumerate(qwen_texts) if detect_prompt_leakage(text, context)[0]]
    leakage_retry_results: dict[int, str] = {}
    if leakage_indexes:
        progress(30, "retrying_prompt_leakage")
        retries = qwen_transcribe(qwen, [clips[index] for index in leakage_indexes], "", max(1, args.batch_size))
        leakage_retry_results = dict(zip(leakage_indexes, retries))
        for index, retry in leakage_retry_results.items():
            if comparison_text(retry) and not detect_prompt_leakage(retry, context)[0]:
                qwen_texts[index] = retry
    del qwen
    release_cuda()
    qwen_seconds = time.monotonic() - qwen_started
    decisions: list[Decision] = []
    previous: list[str] = []
    for index, (region, text) in enumerate(zip(regions, qwen_texts)):
        seconds = (region.end - region.start) / samplerate
        reasons = suspicion_reasons(text, seconds, region.speech_probability, context)
        if len(previous) >= 3 and comparison_text(text) and all(similarity(text, item) > 0.96 for item in previous[-3:]):
            reasons.append("identical_neighbors")
        leaked, leak_score, fragment = detect_prompt_leakage(text, context)
        candidate = Candidate(
            "qwen3", args.qwen_model, text, reasons, leak_score, fragment if leaked else "", index in leakage_retry_results
        )
        decisions.append(Decision(region, candidate, {"qwen3": candidate}, 0.0, 0.0, "fallback_required" if reasons else "accepted"))
        previous.append(text)

    fallback_indexes = list(range(len(decisions))) if args.mode == "high_accuracy" else [i for i, item in enumerate(decisions) if item.selected.suspicion]
    if args.mode == "fast":
        fallback_indexes = []
    reazon_seconds = 0.0
    whisper_seconds = 0.0
    if fallback_indexes and not args.disable_reazon:
        progress(38, "fallback_transcribing")
        reazon_started = time.monotonic()
        try:
            reazon_texts = reazon_batch_transcribe(
                args.reazon_python, args.reazon_script, args.input, regions, fallback_indexes, args.reazon_model, args.device
            )
        except Exception:
            reazon_texts = {}
        for index in fallback_indexes:
            text = reazon_texts.get(index, "")
            seconds = (regions[index].end - regions[index].start) / samplerate
            decisions[index].candidates["reazon"] = Candidate(
                "reazon", args.reazon_model, text, suspicion_reasons(text, seconds, regions[index].speech_probability, "")
            )
        release_cuda()
        reazon_seconds = time.monotonic() - reazon_started

    whisper_indexes: list[int] = []
    for index in fallback_indexes:
        decision = decisions[index]
        qwen_candidate = decision.candidates["qwen3"]
        reazon_candidate = decision.candidates.get("reazon")
        if should_use_whisper(qwen_candidate, reazon_candidate) and not args.disable_whisper and args.whisper_model:
            whisper_indexes.append(index)

    whisper_texts: dict[int, str] = {}
    if whisper_indexes:
        progress(52, "whisper_tie_break")
        whisper_started = time.monotonic()
        try:
            whisper_texts = whisper_batch_transcribe(args.whisper_binary, args.whisper_model, clips, whisper_indexes, "ja")
        except Exception:
            whisper_texts = {}
        whisper_seconds = time.monotonic() - whisper_started

    for index in fallback_indexes:
        decision = decisions[index]
        if index in whisper_indexes:
            text = whisper_texts.get(index, "")
            seconds = (regions[index].end - regions[index].start) / samplerate
            decision.candidates["whisper"] = Candidate(
                "whisper", args.whisper_model, text, suspicion_reasons(text, seconds, regions[index].speech_probability, "")
            )
        chosen, agreement = choose_candidate(decision.candidates)
        decision.selected = chosen
        decision.comparison_score = agreement
        decision.quality_state = "review" if chosen.suspicion or (len(decision.candidates) > 1 and agreement < 0.55) else "accepted"

    # Fast never escalates prompt leakage to another ASR, but leaked meta-text
    # must also never become accepted Japanese or reach the translator.
    if args.mode == "fast":
        for decision in decisions:
            if "prompt_leakage" in decision.selected.suspicion:
                decision.quality_state = "failed"

    progress(62, "aligning")
    align_started = time.monotonic()
    aligner = load_aligner(args.aligner_model, args.aligner_revision, args.device)
    output_segments: list[dict[str, Any]] = []
    alignment_failures = 0
    alignment_integrity_failures = 0
    alignment_timing_only_segments = 0
    alignment_recovered_segments = 0
    total_alignment_loss = 0.0
    for index, (decision, clip) in enumerate(zip(decisions, clips)):
        selected = normalize_text(decision.selected.text)
        if not selected or "prompt_leakage" in decision.selected.suspicion:
            decision.quality_state = "failed"
            decision.confidence = 0
            continue
        try:
            aligned = aligner.align(audio=clip, text=selected, language=JAPANESE)[0]
            items = [
                {"text": normalize_text(item.text), "start_time": float(item.start_time), "end_time": float(item.end_time)}
                for item in aligned
                if normalize_text(item.text) and float(item.end_time) > float(item.start_time)
            ]
            clip_seconds = len(clip[0]) / clip[1]
            reasonable = bool(items) and items[0]["start_time"] >= -0.1 and items[-1]["end_time"] <= clip_seconds + 0.5
            if not reasonable:
                raise RuntimeError("unreasonable alignment")
            decision.aligned_items = items
            integrity = alignment_integrity(selected, "".join(item["text"] for item in items))
            total_alignment_loss += integrity["omission_ratio"]
            if integrity["valid"]:
                decision.alignment = "aligned"
                groups = split_alignment(items)
            else:
                alignment_integrity_failures += 1
                alignment_timing_only_segments += 1
                decision.alignment = "timing_only"
                decision.quality_state = "review"
                groups = [[{"text": selected, "start_time": items[0]["start_time"], "end_time": items[-1]["end_time"]}]]
        except Exception:
            alignment_failures += 1
            decision.alignment = "vad_fallback"
            decision.quality_state = "review"
            clip_seconds = len(clip[0]) / clip[1]
            groups = [[{"text": selected, "start_time": 0.0, "end_time": clip_seconds}]]
            integrity = alignment_integrity(selected, selected)
        decision.confidence = confidence_for(decision, decision.alignment == "aligned")
        if decision.confidence < 0.45:
            decision.quality_state = "review"
        base_seconds = decision.region.start / samplerate
        candidate_payload = {
            name: {
                "model": candidate.model,
                "text": candidate.text,
                "suspicion": candidate.suspicion,
                "leak_similarity": candidate.leak_similarity,
                "matched_context_fragment": candidate.matched_context_fragment,
                "retry_without_context": candidate.retry_without_context,
            }
            for name, candidate in decision.candidates.items()
        }
        if comparison_text("".join("".join(item["text"] for item in group) for group in groups)) != comparison_text(selected):
            alignment_recovered_segments += 1
            decision.alignment = "recovered"
            decision.quality_state = "review"
            clip_seconds = len(clip[0]) / clip[1]
            groups = [[{"text": selected, "start_time": 0.0, "end_time": clip_seconds}]]
        for group in groups:
            text = "".join(item["text"] for item in group).strip()
            if not text:
                continue
            start_ms = round((base_seconds + group[0]["start_time"]) * 1000)
            end_ms = round((base_seconds + group[-1]["end_time"]) * 1000)
            end_ms = max(end_ms, start_ms + 350)
            record: dict[str, Any] = {
                "start_ms": start_ms,
                "end_ms": end_ms,
                "text": text,
                "classification": decision.region.classification,
                "quality_state": decision.quality_state,
                "confidence": decision.confidence,
                "confidence_class": "high" if decision.confidence >= 0.78 else "medium" if decision.confidence >= 0.5 else "low",
                "selected_engine": decision.selected.engine,
                "selected_model": decision.selected.model,
                "comparison_score": round(decision.comparison_score, 3),
                "alignment": decision.alignment,
            }
            if args.debug:
                record["candidates"] = candidate_payload
                record["alignment_items"] = group
                record["vad_speech_probability"] = round(decision.region.speech_probability, 3)
                record["canonical_text"] = selected
                record["aligned_text"] = "".join(item["text"] for item in decision.aligned_items)
                record["alignment_similarity"] = integrity["similarity"]
                record["alignment_coverage"] = integrity["coverage"]
                record["alignment_integrity_state"] = decision.alignment
            output_segments.append(record)
        if index % 8 == 0:
            progress(62 + round(30 * (index + 1) / len(decisions)), "aligning")
    del aligner
    release_cuda()
    alignment_seconds = time.monotonic() - align_started

    output_segments.sort(key=lambda item: (item["start_ms"], item["end_ms"]))
    reazon_count = sum("reazon" in item.candidates for item in decisions)
    whisper_count = sum("whisper" in item.candidates for item in decisions)
    review_count = sum(item.quality_state != "accepted" for item in decisions)
    leakage_segments = len(leakage_indexes)
    leakage_recovered = sum(
        1 for index in leakage_indexes if comparison_text(leakage_retry_results.get(index, "")) and "prompt_leakage" not in decisions[index].selected.suspicion
    )
    suspicion_counts: dict[str, int] = {}
    for decision in decisions:
        for reason in decision.candidates["qwen3"].suspicion:
            suspicion_counts[reason] = suspicion_counts.get(reason, 0) + 1
    total_seconds = time.monotonic() - started
    payload = {
        "duration_ms": round(duration * 1000),
        "processed_ms": round(duration * 1000),
        "language": "ja",
        "pipeline_version": PIPELINE_VERSION,
        "profile": args.profile,
        "asr_mode": args.mode,
        "model_versions": {
            "asr_primary": args.qwen_model,
            "asr_primary_revision": args.qwen_revision,
            "asr_secondary": args.reazon_model if not args.disable_reazon else "disabled",
            "asr_tertiary": args.whisper_model if not args.disable_whisper else "disabled",
            "aligner": args.aligner_model,
            "aligner_revision": args.aligner_revision,
        },
        "metrics": {
            "source_duration_seconds": round(duration, 3),
            "vad_seconds": round(vad_seconds, 3),
            "qwen_asr_seconds": round(qwen_seconds, 3),
            "reazon_seconds": round(reazon_seconds, 3),
            "whisper_seconds": round(whisper_seconds, 3),
            "alignment_seconds": round(alignment_seconds, 3),
            "total_processing_seconds": round(total_seconds, 3),
            "real_time_factor": round(total_seconds / duration, 4) if duration else 0,
            "vad_regions": len(regions),
            "qwen_segments": len(decisions),
            "reazon_fallback_segments": reazon_count,
            "reazon_fallback_percentage": round(reazon_count * 100 / len(decisions), 2),
            "whisper_fallback_segments": whisper_count,
            "whisper_fallback_percentage": round(whisper_count * 100 / len(decisions), 2),
            "review_segments": review_count,
            "low_confidence_percentage": round(review_count * 100 / len(decisions), 2),
            "alignment_failures": alignment_failures,
            "alignment_integrity_failures": alignment_integrity_failures,
            "alignment_timing_only_segments": alignment_timing_only_segments,
            "alignment_recovered_segments": alignment_recovered_segments,
            "alignment_text_loss_percentage": round(total_alignment_loss * 100 / len(decisions), 2),
            "prompt_leakage_segments": leakage_segments,
            "prompt_leakage_retries": len(leakage_indexes),
            "prompt_leakage_recovered": leakage_recovered,
            "prompt_leakage_unresolved": leakage_segments - leakage_recovered,
            "prompt_leakage_percentage": round(leakage_segments * 100 / len(decisions), 2),
            "suspicion_reasons": suspicion_counts,
        },
        "segments": output_segments,
    }
    write_json(args.output, payload)
    progress(100, "complete")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"Qwen Japanese ASR pipeline failed: {error}", file=sys.stderr)
        raise SystemExit(1)
