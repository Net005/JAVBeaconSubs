#!/usr/bin/env python3
"""Qwen-first Japanese ASR pipeline for JAVBeaconSubs.

The worker deliberately loads only one large model family at a time. Qwen ASR
runs first, suspicious lexical output receives one Qwen retry, whisper.cpp is
then loaded once for unresolved candidates, and the Qwen aligner is loaded only
after transcription models have been released. Reazon remains available as a
standalone/compatibility backend but is not part of normal Balanced jobs.
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
from typing import Any

import numpy as np


PIPELINE_VERSION = "qwen-first-v2.5"
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
VOCALIZATION_TOKENS = {
    "あ", "あっ", "ああ", "い", "う", "うっ", "うん", "え", "えっ", "お", "おっ", "ん", "んっ",
    "はい", "はぁ", "はあ", "はは", "ふふ", "へへ", "やっ", "いや", "わっ", "きゃ", "きゃっ",
}
VOCALIZATION_CHARACTERS = set("あいうえおんっぁぃぅぇぉゃゅょー～〜はふへわきゃ")
# Tuning for the prompt-leak tiny-vocalization suspicion score below. A
# "local repetition" window: how close (by segment position or by time)
# two prompt-leak retries that both resolved to the same tiny generic
# vocalization need to be before that repetition counts as evidence of a
# phantom pattern rather than two independent genuine utterances.
PROMPT_LEAK_VOCALIZATION_REPETITION_WINDOW_SEGMENTS = 3
PROMPT_LEAK_VOCALIZATION_REPETITION_WINDOW_SECONDS = 20.0
# Suspicion-score threshold at or above which a prompt-leak retry that
# resolved to a tiny generic vocalization is withheld from automatic
# recovery (see prompt_leak_vocalization_suspicion_score).
PROMPT_LEAK_VOCALIZATION_REJECTION_THRESHOLD = 2
# Conservative Chinese-specific forms observed in malformed Japanese ASR. This
# is intentionally tiny; uncommon Japanese kanji are not errors by default.
ATYPICAL_JAPANESE_CJK = set("啥这吗们说没给让从还")


@dataclass
class Region:
    start: int
    end: int
    speech_probability: float
    classification: str
    parent_vad_region_id: int = 0
    split_index: int = 0
    split_count: int = 1
    split_strategy: str = "natural"


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
    timing_quality: str = "timing_suspicious"
    qwen_retry_attempted: bool = False
    qwen_retry_selected: bool = False
    qwen_retry_reason: str = ""
    qwen_retry_similarity: float = 0.0
    qwen_retry_recovered: bool = False
    qwen_retry_ambiguous_vocalization: bool = False
    # "" (n/a), "rejected_repetition", "rejected_weak_evidence", or
    # "preserved_strong_evidence" -- see PRIORITY 8's structured-reason note.
    qwen_retry_vocalization_outcome: str = ""
    whisper_eligible: bool = False
    whisper_reason: str = ""
    whisper_candidate_valid: bool = False
    whisper_rejection_reason: str = "not_attempted"
    fallback_audio: dict[str, Any] = field(default_factory=dict)
    whisper_execution: dict[str, Any] = field(default_factory=dict)
    aligned_items: list[dict[str, Any]] = field(default_factory=list)


@dataclass
class WhisperBatchResult:
    texts: dict[int, str] = field(default_factory=dict)
    candidate_errors: dict[int, str] = field(default_factory=dict)
    candidate_details: dict[int, dict[str, Any]] = field(default_factory=dict)
    exit_code: int | None = None
    success: bool = False
    stderr: str = ""
    stdout_summary: str = ""
    duration_seconds: float = 0.0
    failure_reason: str = "not_attempted"
    cuda_attempted: bool = False
    cuda_failed: bool = False
    cuda_failure_reason: str = ""
    cuda_stderr: str = ""
    cpu_fallback_attempted: bool = False
    cpu_fallback_succeeded: bool = False
    cpu_fallback_seconds: float = 0.0
    cpu_fallback_timeout: int = 0
    cpu_fallback_candidate_count: int = 0
    execution_device: str = "not_attempted"
    cuda_preflight_skipped: bool = False
    cuda_preflight_free_mb: int | None = None
    cuda_safe_minimum_mb: int = 0


def normalize_text(value: str) -> str:
    return " ".join(str(value or "").split()).strip()


def comparison_text(value: str) -> str:
    value = unicodedata.normalize("NFKC", value)
    return "".join(ch for ch in value if not ch.isspace() and not unicodedata.category(ch).startswith("P"))


def meaningful_characters(value: str) -> str:
    """Return only actual letters and numbers, excluding punctuation/formatting."""
    value = unicodedata.normalize("NFKC", str(value or ""))
    return "".join(ch for ch in value if unicodedata.category(ch)[:1] in {"L", "N"})


def has_meaningful_transcript(value: str) -> bool:
    return bool(meaningful_characters(value))


def transcript_features(value: str) -> dict[str, Any]:
    meaningful = meaningful_characters(value)
    compact = comparison_text(value)
    if not meaningful:
        return {"meaningful_char_count": 0, "vocalization_ratio": 0.0, "lexical_ratio": 0.0, "vocalization_heavy": False}
    token = meaningful.casefold()
    repeated_token = any(token == item * repeats for item in VOCALIZATION_TOKENS for repeats in range(1, 8))
    vocal_chars = sum(ch in VOCALIZATION_CHARACTERS for ch in token)
    vocalization_ratio = vocal_chars / len(meaningful)
    vocalization_heavy = token in VOCALIZATION_TOKENS or repeated_token or (len(meaningful) <= 12 and vocalization_ratio >= 0.8)
    return {
        "meaningful_char_count": len(meaningful),
        "vocalization_ratio": round(vocalization_ratio, 3),
        "lexical_ratio": round(1.0 - vocalization_ratio, 3),
        "vocalization_heavy": vocalization_heavy,
        "compact": compact,
    }


def script_anomaly(value: str) -> bool:
    meaningful = meaningful_characters(value)
    return any(ch in ATYPICAL_JAPANESE_CJK for ch in meaningful)


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
    if len(value) < 8 or transcript_features(value)["vocalization_heavy"]:
        return False
    for width in range(1, min(12, len(value) // 3) + 1):
        unit = value[:width]
        if len(unit) and value.count(unit) >= 4 and len(unit) * value.count(unit) >= len(value) * 0.75:
            return True
    return False


def malformed_lexical_repetition(text: str) -> bool:
    features = transcript_features(text)
    if features["vocalization_heavy"] or features["meaningful_char_count"] < 6:
        return False
    normalized = unicodedata.normalize("NFKC", text)
    return re.search(r"([ぁ-んァ-ヶ]{2,4})[\s、,。…]*\1", normalized) is not None


def suspicion_reasons(text: str, duration: float, speech_probability: float, context: str = "") -> list[str]:
    value = meaningful_characters(text)
    features = transcript_features(text)
    reasons: list[str] = []
    if not value:
        reasons.append("empty_transcript")
    if duration > 0 and len(value) / duration > 16:
        reasons.append("text_duration_ratio")
    if repeated_phrase(value):
        reasons.append("pathological_repetition")
    if malformed_lexical_repetition(text):
        reasons.append("malformed_lexical_repetition")
    if speech_probability < 0.28 and len(value) >= 8:
        reasons.append("weak_speech_conflict")
    if script_anomaly(text):
        reasons.append("script_anomaly")
    latin = sum(ch.isascii() and ch.isalpha() for ch in value)
    japanese = sum(
        "HIRAGANA" in unicodedata.name(ch, "") or "KATAKANA" in unicodedata.name(ch, "") or "CJK UNIFIED" in unicodedata.name(ch, "")
        for ch in value
    )
    if latin >= 4 and japanese >= 2 and latin / len(value) >= 0.35:
        reasons.append("mixed_script_lexical")
    leaked, _, _ = detect_prompt_leakage(text, context)
    if leaked:
        reasons.append("prompt_leakage")
    return reasons


LEXICAL_REASONS = {
    "script_anomaly",
    "mixed_script_lexical",
    "pathological_repetition",
    "text_duration_ratio",
    "weak_speech_conflict",
    "identical_neighbors",
    "lexical_low_confidence",
    "malformed_lexical_repetition",
    "suspicious_mixed_script",
    "implausible_lexical_output",
}


def lexical_fallback_eligibility(
    text: str, reasons: list[str], classification: str, speech_probability: float
) -> tuple[bool, str]:
    """Return whether a transcript has genuine lexical uncertainty."""
    if not has_meaningful_transcript(text):
        return False, "empty_transcript"
    features = transcript_features(text)
    if "prompt_leakage" in reasons:
        return True, "prompt_leakage"
    lexical = [reason for reason in reasons if reason in LEXICAL_REASONS]
    if features["vocalization_heavy"]:
        return False, "vocalization"
    if classification == "ambiguous_vocalization" and features["meaningful_char_count"] <= 6:
        return False, "ambiguous_vocalization"
    if lexical:
        return True, lexical[0]
    if speech_probability < 0.25 and features["meaningful_char_count"] >= 8:
        return True, "lexical_low_confidence"
    return False, "not_lexical"


def should_use_reazon_fallback(text: str, reasons: list[str], classification: str, speech_probability: float) -> tuple[bool, str]:
    """Compatibility alias for historical tests/diagnostic tooling."""
    return lexical_fallback_eligibility(text, reasons, classification, speech_probability)


def should_retry_qwen(text: str, reasons: list[str], classification: str, speech_probability: float) -> tuple[bool, str]:
    return lexical_fallback_eligibility(text, reasons, classification, speech_probability)


def suspicion_category(text: str, reasons: list[str], timing_quality: str = "timing_good") -> str:
    if "prompt_leakage" in reasons:
        return "META"
    if any(reason in LEXICAL_REASONS for reason in reasons) and not transcript_features(text)["vocalization_heavy"]:
        return "LEXICAL"
    if transcript_features(text)["vocalization_heavy"]:
        return "VOCALIZATION"
    if timing_quality != "timing_good":
        return "TIMING"
    return "NONE"


def frame_rms(waveform: np.ndarray, frame_samples: int) -> np.ndarray:
    count = max(1, math.ceil(len(waveform) / frame_samples))
    padded = np.pad(waveform, (0, count * frame_samples - len(waveform)))
    frames = padded.reshape(count, frame_samples)
    return np.sqrt(np.mean(np.square(frames, dtype=np.float64), axis=1))


def split_oversized_region(
    waveform: np.ndarray, samplerate: int, start: int, end: int, maximum: int
) -> list[tuple[int, int, str]]:
    """Split long VAD regions near quiet valleys, with a bounded hard fallback."""
    if end <= start:
        return []
    if end - start <= maximum:
        return [(start, end, "natural")]
    frame_samples = max(1, round(samplerate * 0.02))
    minimum_child = max(frame_samples, round(samplerate * 3.0))
    parts: list[tuple[int, int, str]] = []
    cursor = start
    while end - cursor > maximum:
        target = cursor + maximum
        search_radius = min(round(maximum * 0.22), round(samplerate * 6.0))
        search_start = max(cursor + minimum_child, target - search_radius)
        # The safety limit is a hard upper bound, not merely a preferred
        # split point. Only consider valleys at or before it; selecting a
        # quiet point after ``target`` used to create 30-36 second regions
        # that the Go orchestrator correctly rejected.
        search_end = min(end - minimum_child, target + frame_samples)
        boundary, strategy = target, "hard_max"
        if search_end > search_start:
            local = waveform[search_start:search_end]
            rms = frame_rms(local, frame_samples)
            if len(rms) >= 3:
                smooth = np.convolve(rms, np.ones(3, dtype=np.float64) / 3.0, mode="same")
                interior = smooth[1:-1] if len(smooth) > 2 else smooth
                valley_index = int(np.argmin(interior)) + (1 if len(smooth) > 2 else 0)
                valley = float(smooth[valley_index])
                reference = max(float(np.percentile(smooth, 65)), 1e-8)
                if valley <= reference * 0.55:
                    candidate = search_start + valley_index * frame_samples
                    if cursor + minimum_child <= candidate <= min(target, end - minimum_child):
                        boundary, strategy = candidate, "energy_valley"
        boundary = max(cursor + frame_samples, min(boundary, target, end))
        parts.append((cursor, boundary, strategy))
        cursor = boundary
    if end > cursor:
        parts.append((cursor, end, "natural" if not parts else parts[-1][2]))
    return parts


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
    parent_region_id = 0
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
        parts = split_oversized_region(waveform, samplerate, start, end, maximum)
        for split_index, (part_start, part_end, strategy) in enumerate(parts):
            regions.append(
                Region(
                    part_start,
                    part_end,
                    probability,
                    classification,
                    parent_vad_region_id=parent_region_id,
                    split_index=split_index,
                    split_count=len(parts),
                    split_strategy=strategy,
                )
            )
        parent_region_id += 1
    return regions


def is_tiny_transcript_long_region(text: str, duration: float, threshold_seconds: float = 8.0) -> bool:
    count = transcript_features(text)["meaningful_char_count"]
    return 0 < count <= 3 and duration > threshold_seconds


def recover_tiny_timing(
    waveform: np.ndarray,
    samplerate: int,
    speech_probability: float,
    classification: str,
) -> tuple[float, float, str]:
    """Bound a tiny unaligned utterance around the strongest local activity."""
    duration = len(waveform) / max(1, samplerate)
    if duration <= 2.5:
        return 0.0, max(0.4, duration), "timing_vad_fallback"
    frame_seconds = 0.02
    frame_samples = max(1, round(samplerate * frame_seconds))
    rms = frame_rms(np.asarray(waveform, dtype=np.float32), frame_samples)
    peak_index = int(np.argmax(rms)) if len(rms) else 0
    peak = float(rms[peak_index]) if len(rms) else 0.0
    floor = float(np.percentile(rms, 20)) if len(rms) else 0.0
    cutoff = max(floor * 1.8, peak * 0.22, 1e-5)
    active = rms >= cutoff
    # Bridge only very short gaps inside a voiced burst.
    gap_frames = max(1, round(0.12 / frame_seconds))
    index = 0
    while index < len(active):
        if active[index]:
            index += 1
            continue
        begin = index
        while index < len(active) and not active[index]:
            index += 1
        if begin > 0 and index < len(active) and index - begin <= gap_frames:
            active[begin:index] = True
    runs: list[tuple[int, int, float]] = []
    index = 0
    while index < len(active):
        if not active[index]:
            index += 1
            continue
        begin = index
        while index < len(active) and active[index]:
            index += 1
        runs.append((begin, index, float(np.sum(rms[begin:index]))))
    if runs:
        begin, end, _ = max(runs, key=lambda item: item[2])
        start_seconds = begin * frame_seconds
        end_seconds = min(duration, end * frame_seconds)
        run_duration = end_seconds - start_seconds
        evidence_cap = 8.0 if classification == "speech" and speech_probability >= 0.65 and run_duration >= 2.0 else 2.5
        if run_duration > evidence_cap:
            center = (start_seconds + end_seconds) / 2
            start_seconds, end_seconds = center - evidence_cap / 2, center + evidence_cap / 2
        start_seconds = max(0.0, start_seconds - 0.15)
        end_seconds = min(duration, end_seconds + 0.2)
        if end_seconds - start_seconds < 0.4:
            center = (start_seconds + end_seconds) / 2
            start_seconds = max(0.0, center - 0.2)
            end_seconds = min(duration, start_seconds + 0.4)
        return start_seconds, end_seconds, "timing_energy_recovered"
    center = min(duration, peak_index * frame_seconds + frame_seconds / 2)
    target = 1.2 if classification == "ambiguous_vocalization" or speech_probability < 0.45 else 2.0
    start_seconds = max(0.0, center - target / 2)
    end_seconds = min(duration, start_seconds + target)
    start_seconds = max(0.0, end_seconds - target)
    return start_seconds, end_seconds, "timing_bounded"


def release_cuda() -> dict[str, Any]:
    actions: dict[str, Any] = {
        "qwen_gc_collected": gc.collect(),
        "cuda_cache_cleared": False,
        "cuda_ipc_collected": False,
    }
    try:
        import torch

        if torch.cuda.is_available():
            torch.cuda.synchronize()
            torch.cuda.empty_cache()
            actions["cuda_cache_cleared"] = True
            try:
                torch.cuda.ipc_collect()
                actions["cuda_ipc_collected"] = True
            except Exception:
                pass
    except Exception:
        pass
    return actions


def dispose_qwen(model: Any) -> dict[str, Any]:
    """Break Qwen's owned model/processor references before clearing CUDA."""
    actions: dict[str, Any] = {
        "qwen_model_moved_to_cpu": False,
        "qwen_model_reference_cleared": False,
        "qwen_processor_reference_cleared": False,
        "qwen_generation_state_cleared": False,
    }
    inner_model = getattr(model, "model", None)
    if inner_model is not None:
        try:
            inner_model.to("cpu")
            actions["qwen_model_moved_to_cpu"] = True
        except Exception:
            pass
    for attribute, metric in (
        ("forced_aligner", "qwen_generation_state_cleared"),
        ("sampling_params", "qwen_generation_state_cleared"),
        ("processor", "qwen_processor_reference_cleared"),
        ("model", "qwen_model_reference_cleared"),
    ):
        if hasattr(model, attribute):
            try:
                setattr(model, attribute, None)
                actions[metric] = True
            except Exception:
                pass
    del inner_model
    actions.update(release_cuda())
    return actions


def torch_memory_snapshot(initialize: bool = False) -> dict[str, Any]:
    snapshot: dict[str, Any] = {
        "available": False, "allocated_mb": 0.0, "reserved_mb": 0.0,
        "max_allocated_mb": 0.0, "max_reserved_mb": 0.0,
    }
    try:
        import torch

        if not torch.cuda.is_available():
            return snapshot
        if initialize:
            torch.cuda.init()
        divisor = 1024 * 1024
        snapshot.update({
            "available": True,
            "allocated_mb": round(torch.cuda.memory_allocated() / divisor, 2),
            "reserved_mb": round(torch.cuda.memory_reserved() / divisor, 2),
            "max_allocated_mb": round(torch.cuda.max_memory_allocated() / divisor, 2),
            "max_reserved_mb": round(torch.cuda.max_memory_reserved() / divisor, 2),
        })
    except Exception as error:
        snapshot["error"] = normalize_text(str(error))[:1024]
    return snapshot


def surviving_cuda_tensors(limit: int = 64) -> dict[str, Any]:
    result: dict[str, Any] = {"count": 0, "total_bytes": 0, "tensors": []}
    try:
        import torch

        for item in gc.get_objects():
            try:
                if not isinstance(item, torch.Tensor) or not item.is_cuda:
                    continue
                size = int(item.nelement() * item.element_size())
                result["count"] += 1
                result["total_bytes"] += size
                if len(result["tensors"]) < limit:
                    result["tensors"].append({
                        "shape": list(item.shape), "dtype": str(item.dtype),
                        "bytes": size, "type": type(item).__name__,
                    })
            except Exception:
                continue
    except Exception as error:
        result["error"] = normalize_text(str(error))[:1024]
    return result


def gpu_snapshot() -> dict[str, Any]:
    """Return plain-data NVIDIA memory/process diagnostics without retaining tensors."""
    snapshot: dict[str, Any] = {"available": False, "total_mb": None, "used_mb": None, "free_mb": None, "processes": []}
    try:
        memory = subprocess.run(
            [
                "nvidia-smi", "--query-gpu=memory.total,memory.used,memory.free",
                "--format=csv,noheader,nounits", "--id=0",
            ],
            check=False, capture_output=True, text=True, timeout=10,
        )
        if memory.returncode != 0:
            snapshot["error"] = normalize_text(memory.stderr)[:1024]
            return snapshot
        values = [int(float(item.strip())) for item in memory.stdout.splitlines()[0].split(",")]
        snapshot.update({"available": True, "total_mb": values[0], "used_mb": values[1], "free_mb": values[2]})
        processes = subprocess.run(
            [
                "nvidia-smi", "--query-compute-apps=pid,process_name,used_memory",
                "--format=csv,noheader,nounits", "--id=0",
            ],
            check=False, capture_output=True, text=True, timeout=10,
        )
        if processes.returncode == 0:
            for line in processes.stdout.splitlines()[:32]:
                parts = [item.strip() for item in line.split(",", 2)]
                if len(parts) == 3:
                    snapshot["processes"].append({"pid": parts[0], "name": os.path.basename(parts[1]), "used_mb": parts[2]})
    except (OSError, ValueError, IndexError, subprocess.TimeoutExpired) as error:
        snapshot["error"] = normalize_text(str(error))[:1024]
    return snapshot


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
            del results
        except Exception:
            # Isolate a malformed/problematic segment instead of losing the movie.
            for clip in batch:
                try:
                    result = model.transcribe(audio=clip, context=context, language=JAPANESE)[0]
                    out.append(normalize_text(result.text))
                    del result
                except Exception:
                    out.append("")
        finally:
            del batch
    return out


def qwen_worker_transcribe(
    python: str,
    worker_script: str,
    input_path: str,
    regions: list[Region],
    indexes: list[int],
    context: str,
    model: str,
    revision: str,
    device: str,
    batch_size: int,
    debug: bool = False,
    mode: str = "balanced",
) -> tuple[dict[int, str], dict[str, Any]]:
    """Run Qwen primary/retry in one child so its CUDA context dies on exit."""
    if not indexes:
        return {}, {}
    with tempfile.TemporaryDirectory(prefix="javbeaconsubs-qwen-phase-") as directory:
        manifest = os.path.join(directory, "regions.json")
        output = os.path.join(directory, "results.json")
        with open(manifest + ".tmp", "w", encoding="utf-8") as handle:
            json.dump(
                [
                    {
                        "index": index,
                        "start": regions[index].start,
                        "end": regions[index].end,
                        "speech_probability": regions[index].speech_probability,
                        "classification": regions[index].classification,
                    }
                    for index in indexes
                ],
                handle,
                separators=(",", ":"),
            )
        os.replace(manifest + ".tmp", manifest)
        command = [
            python, worker_script,
            "--input", input_path,
            "--regions", manifest,
            "--output", output,
            "--context", context,
            "--model", model,
            "--revision", revision,
            "--device", device,
            "--batch-size", str(max(1, batch_size)),
            "--mode", mode,
        ]
        if debug:
            command.append("--debug")
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=None)
        with open(output, encoding="utf-8") as handle:
            payload = json.load(handle)
        return (
            {int(item["index"]): normalize_text(item.get("text", "")) for item in payload.get("results", [])},
            payload.get("diagnostics", {}),
        )


def active_recognition_vocabulary(path: str, profile: str, title: str) -> tuple[set[str], set[str]]:
    """Load profile/title terms for scoring only; never feed them to ASR."""
    if not path:
        return set(), set()
    try:
        with open(path, encoding="utf-8") as handle:
            payload = json.load(handle)
        scoped: set[str] = set()
        for scope in ("global", profile) if profile in {"jav", "giga"} else ("global",):
            scoped.update(normalize_text(item.get("term", "")) for item in payload.get("scopes", {}).get(scope, []))
        titled = {
            normalize_text(item.get("term", ""))
            for item in payload.get("title_or_series_overrides", {}).get(title, [])
        } if title else set()
        return {item for item in scoped if item}, {item for item in titled if item}
    except (OSError, ValueError, TypeError):
        return set(), set()


def candidate_score(
    candidate: Candidate,
    duration: float,
    profile_terms: set[str] | None = None,
    title_terms: set[str] | None = None,
) -> float:
    """Conservative deterministic lexical plausibility score."""
    features = transcript_features(candidate.text)
    if not has_meaningful_transcript(candidate.text):
        return -10.0
    meaningful = meaningful_characters(candidate.text)
    japanese = sum(
        "HIRAGANA" in unicodedata.name(ch, "")
        or "KATAKANA" in unicodedata.name(ch, "")
        or "CJK UNIFIED" in unicodedata.name(ch, "")
        for ch in meaningful
    )
    score = 0.35 + 0.25 * japanese / max(1, len(meaningful)) + 0.15 * features["lexical_ratio"]
    density = len(meaningful) / max(0.25, duration)
    if 0.4 <= density <= 12:
        score += 0.1
    score -= min(0.6, 0.14 * len(set(candidate.suspicion)))
    if features["vocalization_heavy"]:
        score -= 0.18
    if title_terms and any(term in candidate.text for term in title_terms):
        score += 0.18
    if profile_terms and any(term in candidate.text for term in profile_terms):
        score += 0.08
    return round(score, 4)


def is_tiny_generic_vocalization_candidate(retry_text: str) -> bool:
    """The narrow "could this be a phantom filler-word guess" gate: a tiny
    generic vocalization token (e.g. はい/うん/あ/え/ん), with almost no
    lexical content. This gate alone says nothing about whether the token
    should be trusted -- see prompt_leak_vocalization_suspicion_score for
    that -- it only decides whether this suppression path applies at all.
    A genuine short utterance or a token outside VOCALIZATION_TOKENS never
    enters this path (Priority 6/8: direct vocalization recognition without
    prompt leakage, and substantive retry text, are untouched)."""
    features = transcript_features(retry_text)
    token = meaningful_characters(retry_text).casefold()
    return (
        token in VOCALIZATION_TOKENS
        and features["meaningful_char_count"] <= 2
        and features["lexical_ratio"] <= 0.05
    )


def local_prompt_leak_vocalization_repetition_counts(
    candidates: list[tuple[int, str, float, float]],
    window_segments: int = PROMPT_LEAK_VOCALIZATION_REPETITION_WINDOW_SEGMENTS,
    window_seconds: float = PROMPT_LEAK_VOCALIZATION_REPETITION_WINDOW_SECONDS,
) -> dict[int, int]:
    """The strongest real-world evidence that a prompt-leak "tiny
    vocalization" recovery is a phantom guess is not any single occurrence,
    it's the same token being produced by nearby prompt-leak retries over
    and over (see PRIORITY 3). `candidates` holds one (segment_index, token,
    start_seconds, end_seconds) entry per segment where the original leaked
    its prompt AND the context-free retry resolved to a tiny generic
    vocalization, in segment order. Returns, per segment_index, how many
    OTHER nearby candidates (within window_segments positions, or within
    window_seconds of start time) share the identical token. This is a
    plain deterministic count over already-computed data -- no new model,
    no randomness -- scoped only to this specific prompt-leak retry path,
    never to ordinary dialogue."""
    counts: dict[int, int] = {}
    for index, token, start, _end in candidates:
        nearby = 0
        for other_index, other_token, other_start, _other_end in candidates:
            if other_index == index or other_token != token:
                continue
            # Compare actual segment index distance, not position within
            # this already-filtered candidate list: unrelated dialogue can
            # sit between two prompt-leak retries, and that should not make
            # them look artificially "adjacent".
            if abs(other_index - index) <= window_segments or abs(other_start - start) <= window_seconds:
                nearby += 1
        counts[index] = nearby
    return counts


def prompt_leak_vocalization_suspicion_score(
    retry: Candidate,
    original: Candidate,
    speech_probability: float,
    classification: str,
    region_duration: float,
    local_repetition_count: int = 0,
) -> int:
    """Deterministic multi-signal suspicion score for a prompt-leak retry
    that resolved to nothing but a tiny generic vocalization. No single weak
    signal decides alone (PRIORITY 19); several must combine before the
    recovery is withheld, and strong direct evidence overrides the rest
    (PRIORITY 2).

    Real post-alignment timing state (aligned vs. vad_fallback vs.
    energy-recovered) is not available yet at this point in the pipeline --
    retry acceptance is decided before forced alignment runs -- so "weak
    timing/audio evidence" is approximated with the VAD-level signals that
    ARE already available this early: speech_probability, region
    classification, and how the tiny transcript compares to the region's
    own duration (PRIORITY 4)."""
    features = transcript_features(retry.text)
    score = 0
    if speech_probability < 0.28:
        score += 2
    elif speech_probability < 0.45:
        score += 1
    if classification == "ambiguous_vocalization":
        score += 2
    if is_tiny_transcript_long_region(retry.text, region_duration):
        score += 1
    if features["vocalization_heavy"] and features["meaningful_char_count"] <= 12 and region_duration > 8.0:
        score += 1
    if "identical_neighbors" in original.suspicion:
        score += 1
    if local_repetition_count >= 1:
        score += 2
    # Strong, direct evidence of real speech overrides everything above: a
    # clearly audible utterance must not be suppressed just because it also
    # happens to be short and generic-sounding (PRIORITY 2/18).
    if classification == "speech" and speech_probability >= 0.70:
        score -= 2
    return score


def retry_is_low_evidence_vocalization(
    retry: Candidate,
    speech_probability: float,
    classification: str,
    original: Candidate | None = None,
    region_duration: float = 0.0,
    local_repetition_count: int = 0,
) -> bool:
    """A prompt-leak retry that comes back as nothing more than a tiny
    generic vocalization (e.g. はい/うん/あ/え/ん) is not, by itself, strong
    enough evidence of real speech to accept automatically -- especially
    when several weak signals line up (weak/moderate speech probability, an
    ambiguous VAD classification, a tiny transcript on a long region, or the
    same token recurring nearby). Those tokens are exactly the kind of thing
    background noise or a stray breath gets misheard as. Distinguish that
    case from a genuine short utterance recovered from a leaked prompt, or
    one backed by strong isolated evidence, which should still be kept."""
    if not is_tiny_generic_vocalization_candidate(retry.text):
        return False
    if original is None:
        # No decision context available (e.g. a direct call without the
        # original candidate): fall back to the two strongest individual
        # signals only, each of which was already sufficient on its own.
        return classification == "ambiguous_vocalization" or speech_probability < 0.28
    score = prompt_leak_vocalization_suspicion_score(
        retry, original, speech_probability, classification, region_duration, local_repetition_count
    )
    return score >= PROMPT_LEAK_VOCALIZATION_REJECTION_THRESHOLD


def qwen_retry_improves(
    original: Candidate,
    retry: Candidate,
    duration: float,
    profile_terms: set[str] | None = None,
    title_terms: set[str] | None = None,
    speech_probability: float = 1.0,
    classification: str = "speech",
    region_duration: float = 0.0,
    local_repetition_count: int = 0,
) -> bool:
    if not meaningful_candidate(retry):
        return False
    original_critical = set(original.suspicion) & (LEXICAL_REASONS | {"prompt_leakage"})
    retry_critical = set(retry.suspicion) & (LEXICAL_REASONS | {"prompt_leakage"})
    # Prompt echoes can be much longer than the real utterance. Resolve that
    # defect before applying the normal content-loss guard, otherwise clean
    # short retries are rejected merely because they removed leaked prompt.
    if "prompt_leakage" in original_critical and "prompt_leakage" not in retry_critical:
        if retry_is_low_evidence_vocalization(
            retry, speech_probability, classification, original, region_duration, local_repetition_count
        ):
            # Do not automatically mark this prompt leak as recovered. The
            # caller is expected to classify it as an ambiguous vocalization
            # and prefer no subtitle instead of surfacing either the leaked
            # text or an unsupported guess.
            return False
        return True
    original_count = transcript_features(original.text)["meaningful_char_count"]
    retry_count = transcript_features(retry.text)["meaningful_char_count"]
    if original_count >= 2 and retry_count < original_count * 0.55:
        return False
    if not retry_critical and original_critical:
        return True
    return candidate_score(retry, duration, profile_terms, title_terms) >= candidate_score(
        original, duration, profile_terms, title_terms
    ) + 0.12


def fallback_audio_stats(waveform: np.ndarray, samplerate: int, region: Region) -> dict[str, Any]:
    audio = np.asarray(waveform, dtype=np.float32)
    absolute = np.abs(audio)
    return {
        "fallback_audio_duration_ms": round(len(audio) * 1000 / max(1, samplerate)),
        "fallback_audio_rms": round(float(np.sqrt(np.mean(np.square(audio, dtype=np.float64)))) if len(audio) else 0.0, 7),
        "fallback_audio_peak": round(float(np.max(absolute)) if len(audio) else 0.0, 7),
        "fallback_audio_nonzero_percentage": round(float(np.mean(absolute > 1e-6) * 100) if len(audio) else 0.0, 3),
        "fallback_audio_source_start_ms": round(region.start * 1000 / max(1, samplerate)),
        "fallback_audio_source_end_ms": round(region.end * 1000 / max(1, samplerate)),
    }


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


def whisper_cuda_oom(stderr: str) -> bool:
    value = normalize_text(stderr).casefold()
    return any(fragment in value for fragment in (
        "cudamalloc failed: out of memory",
        "cuda out of memory",
        "failed to allocate cuda buffer",
        "ggml cuda allocation failed",
        "cuda error 2",
    ))


def whisper_cuda_failure(stderr: str) -> bool:
    value = normalize_text(stderr).casefold()
    return whisper_cuda_oom(value) or any(fragment in value for fragment in (
        "cuda initialization failure",
        "failed to initialize cuda",
        "no cuda-capable device",
        "cuda driver version is insufficient",
    ))


def whisper_batch_transcribe(
    binary: str,
    model: str,
    clips: list[tuple[np.ndarray, int]],
    indexes: list[int],
    language: str,
    device: str = "cpu",
    max_segment_seconds: float = 30.0,
    cpu_fallback: bool = True,
    cpu_timeout_seconds: int = 7200,
    threads: int = 12,
    beam_size: int = 5,
    best_of: int = 5,
    cuda_safe_minimum_mb: int = 4096,
) -> WhisperBatchResult:
    """Run whisper.cpp once for safely split fallback files.

    The pinned CLI accepts multiple ``-f``/``-of`` arguments and loads its model
    before processing them, preserving one heavyweight lifecycle phase without
    concatenating unrelated regions or admitting an oversized input.
    """
    result = WhisperBatchResult()
    if not indexes:
        return result
    started = time.monotonic()

    def fail(
        reason: str,
        stderr: str = "",
        exit_code: int | None = None,
        candidate_reason: str | None = None,
    ) -> WhisperBatchResult:
        result.failure_reason = reason
        result.stderr = normalize_text(stderr)[:8192]
        result.exit_code = exit_code
        result.duration_seconds = round(time.monotonic() - started, 3)
        for candidate_index in indexes:
            result.candidate_errors.setdefault(candidate_index, candidate_reason or reason)
        return result

    try:
        model_stat = os.stat(model)
        if not os.path.isfile(model) or model_stat.st_size <= 0 or not os.access(model, os.R_OK):
            return fail("whisper_model_missing", f"Whisper model is not a readable non-empty file: {model}")
    except OSError as error:
        return fail("whisper_model_missing", str(error))

    import soundfile as sf

    samplerate = clips[indexes[0]][1]
    maximum = max(1, round(max_segment_seconds * samplerate))
    with tempfile.TemporaryDirectory(prefix="javbeaconsubs-whisper-") as directory:
        inputs: list[tuple[int, str, str]] = []
        for index in indexes:
            waveform, rate = clips[index]
            if rate != samplerate:
                raise ValueError("fallback clips have inconsistent sample rates")
            waveform = np.asarray(waveform, dtype=np.float32)
            # whisper-cli accepts multiple files while loading the model once.
            # Split every logical candidate first so even a legacy 188780 ms
            # region can never reach inference as one oversized input.
            for child_start, child_end, _ in split_oversized_region(
                waveform, samplerate, 0, len(waveform), maximum
            ):
                child = waveform[child_start:child_end]
                if not len(child) or len(child) > maximum:
                    continue
                sequence = len(inputs)
                audio_path = os.path.join(directory, f"fallback-{sequence:05d}.wav")
                prefix = os.path.join(directory, f"result-{sequence:05d}")
                try:
                    sf.write(audio_path, child, samplerate, subtype="PCM_16")
                    info = sf.info(audio_path)
                    valid = (
                        os.path.getsize(audio_path) > 44
                        and info.frames > 0
                        and info.channels == 1
                        and info.samplerate == samplerate
                        and str(info.subtype).startswith("PCM")
                        and info.duration <= max_segment_seconds + 0.05
                        and float(np.max(np.abs(child))) > 1e-6
                    )
                except (OSError, ValueError, RuntimeError) as error:
                    valid = False
                    result.candidate_details.setdefault(index, {})["wav_error"] = normalize_text(str(error))[:1024]
                if not valid:
                    result.candidate_errors[index] = "whisper_invalid_audio"
                    continue
                inputs.append((index, audio_path, prefix))
        if not inputs:
            return fail("whisper_invalid_audio")
        valid_indexes = {item[0] for item in inputs}
        for candidate_index in indexes:
            detail = result.candidate_details.setdefault(candidate_index, {})
            candidate_inputs = [item for item in inputs if item[0] == candidate_index]
            detail["fallback_candidate_id"] = f"fallback-{candidate_index}"
            detail["source_segment_index"] = candidate_index
            detail["temp_wav_ids"] = [os.path.basename(item[1]) for item in candidate_inputs]
            detail["whisper_result_indexes"] = [inputs.index(item) for item in candidate_inputs]
            detail["input_count"] = sum(item[0] == candidate_index for item in inputs)
            detail["input_valid"] = candidate_index in valid_indexes

        def command_for(execution_device: str) -> list[str]:
            command = [
                binary, "-m", model, "-l", language, "-ojf", "-nt",
                "-t", str(max(1, threads)), "-bs", str(max(1, beam_size)),
                "-bo", str(max(1, best_of)),
            ]
            for _, audio_path, _ in inputs:
                command.extend(["-f", audio_path])
            for _, _, prefix in inputs:
                command.extend(["-of", prefix])
            if execution_device != "cuda":
                command.append("-ng")
            return command

        def launch(execution_device: str, timeout: float):
            result.execution_device = execution_device
            return subprocess.run(
                command_for(execution_device),
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                timeout=timeout,
            )

        requested_device = "cuda" if device in {"auto", "cuda"} else "cpu"
        result.cuda_safe_minimum_mb = max(0, cuda_safe_minimum_mb)
        if requested_device == "cuda" and cpu_fallback and result.cuda_safe_minimum_mb:
            free_mb = gpu_snapshot().get("free_mb")
            result.cuda_preflight_free_mb = free_mb if isinstance(free_mb, int) else None
            if isinstance(free_mb, int) and free_mb < result.cuda_safe_minimum_mb:
                result.cuda_preflight_skipped = True
                result.cpu_fallback_attempted = True
                result.cpu_fallback_candidate_count = len(valid_indexes)
                result.cpu_fallback_timeout = max(300, cpu_timeout_seconds)
                requested_device = "cpu"
        result.cuda_attempted = requested_device == "cuda"
        launch_started = time.monotonic()
        try:
            launch_timeout = (
                result.cpu_fallback_timeout if result.cuda_preflight_skipped
                else max(300.0, min(1800.0, len(inputs) * 20.0))
            )
            completed = launch(requested_device, launch_timeout)
        except subprocess.TimeoutExpired as error:
            return fail("whisper_timeout", str(error), candidate_reason="whisper_batch_failed")
        except OSError as error:
            return fail("whisper_execution_failed", str(error), candidate_reason="whisper_batch_failed")
        if result.cuda_preflight_skipped:
            result.cpu_fallback_seconds = round(time.monotonic() - launch_started, 3)
            result.cpu_fallback_succeeded = completed.returncode == 0
        if requested_device == "cuda" and completed.returncode != 0:
            cuda_stderr = normalize_text(completed.stderr)[:8192]
            result.cuda_failed = True
            result.cuda_stderr = cuda_stderr
            result.cuda_failure_reason = (
                "whisper_cuda_oom" if whisper_cuda_oom(cuda_stderr) else
                "whisper_cuda_initialization_failed" if whisper_cuda_failure(cuda_stderr) else
                "whisper_execution_failed"
            )
            if cpu_fallback and result.cuda_failure_reason in {"whisper_cuda_oom", "whisper_cuda_initialization_failed"}:
                result.cpu_fallback_attempted = True
                result.cpu_fallback_candidate_count = len(valid_indexes)
                result.cpu_fallback_timeout = max(300, cpu_timeout_seconds)
                for _, _, prefix in inputs:
                    try:
                        os.remove(prefix + ".json")
                    except FileNotFoundError:
                        pass
                cpu_started = time.monotonic()
                try:
                    completed = launch("cpu", result.cpu_fallback_timeout)
                except subprocess.TimeoutExpired as error:
                    result.cpu_fallback_seconds = round(time.monotonic() - cpu_started, 3)
                    return fail("whisper_timeout", str(error), candidate_reason="whisper_batch_failed")
                except OSError as error:
                    result.cpu_fallback_seconds = round(time.monotonic() - cpu_started, 3)
                    return fail("whisper_execution_failed", str(error), candidate_reason="whisper_batch_failed")
                result.cpu_fallback_seconds = round(time.monotonic() - cpu_started, 3)
                result.cpu_fallback_succeeded = completed.returncode == 0
        result.exit_code = completed.returncode
        result.stderr = normalize_text(completed.stderr)[:8192]
        # whisper-cli can print recognized dialogue to stdout, so retain only a
        # bounded structural summary rather than sensitive transcript content.
        stdout = completed.stdout or ""
        result.stdout_summary = f"{len(stdout.encode('utf-8'))} bytes captured"
        if completed.returncode != 0:
            reason = (
                result.cuda_failure_reason if result.execution_device == "cuda" else
                "whisper_cpu_fallback_failed" if result.cpu_fallback_attempted else
                "whisper_execution_failed"
            )
            return fail(reason, completed.stderr, completed.returncode, "whisper_batch_failed")
        result.success = True
        result.failure_reason = ""
        results: dict[int, list[str]] = {index: [] for index in indexes}
        for index, _, prefix in inputs:
            try:
                with open(prefix + ".json", encoding="utf-8") as handle:
                    doc = json.load(handle)
                transcription = doc.get("transcription")
                if not isinstance(transcription, list):
                    raise ValueError("missing transcription array")
            except (OSError, ValueError, TypeError) as error:
                result.candidate_errors[index] = "whisper_output_parse_failed"
                result.candidate_details.setdefault(index, {})["output_error"] = normalize_text(str(error))[:1024]
                continue
            results[index].extend(str(item.get("text", "")) for item in transcription if isinstance(item, dict))
        result.texts = {index: normalize_text("".join(parts)) for index, parts in results.items()}
        result.duration_seconds = round(time.monotonic() - started, 3)
        return result


def meaningful_candidate(candidate: Candidate | None) -> bool:
    return candidate is not None and has_meaningful_transcript(candidate.text) and "prompt_leakage" not in candidate.suspicion


def should_use_whisper(
    qwen: Candidate,
    classification: str,
    speech_probability: float,
    mode: str = "balanced",
    duration: float = 0.0,
    vocabulary_terms: set[str] | None = None,
) -> tuple[bool, str]:
    """Select controlled lexical fallback; prompt leakage alone is not enough."""
    if not has_meaningful_transcript(qwen.text):
        return False, "empty_transcript"
    features = transcript_features(qwen.text)
    if features["vocalization_heavy"]:
        return False, "vocalization"
    if classification == "ambiguous_vocalization" and features["meaningful_char_count"] <= 6:
        return False, "ambiguous_vocalization"
    strong = [reason for reason in qwen.suspicion if reason in {
        "script_anomaly", "mixed_script_lexical", "pathological_repetition",
        "malformed_lexical_repetition", "suspicious_mixed_script", "implausible_lexical_output",
    }]
    if strong:
        return True, strong[0]
    # A leaked prompt gets exactly one no-context Qwen retry. Do not turn a
    # failed retry into a movie-wide Whisper flood unless another lexical
    # defect independently justifies it.
    other_lexical = [
        reason for reason in qwen.suspicion
        if reason in LEXICAL_REASONS and reason != "weak_speech_conflict"
    ]
    if "prompt_leakage" in qwen.suspicion and not other_lexical:
        return False, "prompt_leakage_retry_unresolved"
    if mode == "high_accuracy":
        if duration >= 10.0 and speech_probability < 0.7 and features["meaningful_char_count"] >= 12:
            return True, "long_lexical_verification"
        if 0.28 <= speech_probability < 0.58 and features["meaningful_char_count"] >= 8:
            return True, "medium_confidence_verification"
        if vocabulary_terms and any(term in qwen.text for term in vocabulary_terms) and qwen.suspicion:
            return True, "vocabulary_uncertainty"
    return False, "not_lexical"


def mode_allows_whisper(mode: str) -> bool:
    return mode in {"balanced", "high_accuracy"}


def validate_whisper_candidate(whisper: Candidate, qwen: Candidate, duration: float) -> tuple[bool, str]:
    if not has_meaningful_transcript(whisper.text):
        return False, "whisper_empty_output"
    features = transcript_features(whisper.text)
    if features["vocalization_heavy"]:
        return False, "vocalization_for_lexical_candidate"
    if "prompt_leakage" in whisper.suspicion:
        return False, "prompt_leakage"
    if "pathological_repetition" in whisper.suspicion:
        return False, "whisper_pathological_repetition"
    if "script_anomaly" in whisper.suspicion:
        return False, "script_anomaly"
    qwen_count = transcript_features(qwen.text)["meaningful_char_count"]
    whisper_count = features["meaningful_char_count"]
    if qwen_count >= 6 and whisper_count < qwen_count * 0.5:
        return False, "substantial_content_loss"
    if duration > 0 and whisper_count / duration > 16:
        return False, "duration_density"
    meaningful = meaningful_characters(whisper.text)
    japanese = sum(
        "HIRAGANA" in unicodedata.name(ch, "")
        or "KATAKANA" in unicodedata.name(ch, "")
        or "CJK UNIFIED" in unicodedata.name(ch, "")
        for ch in meaningful
    )
    if japanese / max(1, len(meaningful)) < 0.5:
        return False, "whisper_wrong_language"
    return True, ""


def choose_balanced_candidate(
    original: Candidate,
    current: Candidate,
    whisper: Candidate | None,
    duration: float,
    profile_terms: set[str] | None = None,
    title_terms: set[str] | None = None,
    verification_reason: str = "",
) -> tuple[Candidate, float, bool, str]:
    baseline_score = candidate_score(current, duration, profile_terms, title_terms)
    if whisper is None:
        return current, 0.0, False, "not_attempted"
    valid, rejection = validate_whisper_candidate(whisper, current, duration)
    if not valid:
        return current, similarity(current.text, whisper.text), False, rejection
    whisper_score = candidate_score(whisper, duration, profile_terms, title_terms)
    original_issue = set(original.suspicion) & (LEXICAL_REASONS | {"prompt_leakage"})
    whisper_issue = set(whisper.suspicion) & (LEXICAL_REASONS | {"prompt_leakage"})
    improved = bool(original_issue - whisper_issue)
    materially_different = similarity(original.text, whisper.text) < 0.92
    high_accuracy_verification = verification_reason in {
        "long_lexical_verification", "medium_confidence_verification",
        "vocabulary_uncertainty", "proper_name_verification",
    }
    if high_accuracy_verification and materially_different and not whisper_issue and whisper_score >= baseline_score + 0.12:
        return whisper, similarity(current.text, whisper.text), True, ""
    if improved and materially_different and whisper_score >= baseline_score + 0.1:
        return whisper, similarity(current.text, whisper.text), True, ""
    return current, similarity(current.text, whisper.text), False, "no_clear_improvement"


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
    usable = [item for item in candidates.values() if has_meaningful_transcript(item.text)]
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


def confidence_for(decision: Decision) -> float:
    """Estimate ASR confidence independently from alignment/timing quality."""
    score = 0.55 + decision.region.speech_probability * 0.25 + decision.comparison_score * 0.2
    asr_reasons = [reason for reason in decision.selected.suspicion if reason in LEXICAL_REASONS or reason == "prompt_leakage"]
    score -= min(0.4, 0.1 * len(asr_reasons))
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


def trusted_timing_anchors(group: list[dict[str, Any]], base_seconds: float, alignment: str) -> list[int]:
    """Return only real internal boundaries from a successful forced alignment."""
    if alignment != "aligned" or len(group) < 2:
        return []
    return [round((base_seconds + item["end_time"]) * 1000) for item in group[:-1]]


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


def whisper_model_metadata(path: str) -> dict[str, Any]:
    name = os.path.basename(path).casefold()
    quantization = "full"
    for label in ("q2", "q3", "q4", "q5", "q6", "q8"):
        if label in name:
            quantization = label
            break
    try:
        size_mb = round(os.path.getsize(path) / (1024 * 1024), 2)
    except OSError:
        size_mb = None
    return {"path": path, "size_mb": size_mb, "quantization": quantization}


def main() -> int:
    import soundfile as sf

    parser = argparse.ArgumentParser()
    parser.add_argument("--input")
    parser.add_argument("--output")
    parser.add_argument("--device", choices=("cuda", "cpu"), default="cuda")
    parser.add_argument("--mode", choices=("fast", "balanced", "high_accuracy"), default="balanced")
    parser.add_argument("--profile", choices=tuple(PROFILE_CONTEXT) + tuple(PROFILE_ALIASES), default="jav")
    parser.add_argument("--context", default="")
    parser.add_argument("--recognition-vocabulary", default="")
    parser.add_argument("--title", default="")
    parser.add_argument("--qwen-model", default="Qwen/Qwen3-ASR-1.7B")
    parser.add_argument("--qwen-revision", default="7278e1e70fe206f11671096ffdd38061171dd6e5")
    parser.add_argument(
        "--qwen-worker-script",
        default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "qwen_batch_worker.py"),
    )
    parser.add_argument("--aligner-model", default="Qwen/Qwen3-ForcedAligner-0.6B")
    parser.add_argument("--aligner-revision", default="c7cbfc2048c462b0d63a45797104fc9db3ad62b7")
    parser.add_argument("--reazon-model", default="reazon-research/reazonspeech-nemo-v2")
    parser.add_argument("--reazon-python", default="python3")
    parser.add_argument("--reazon-script", default="reazon_batch_worker.py")
    parser.add_argument("--whisper-binary", default="whisper-cli")
    parser.add_argument("--whisper-model", default="")
    parser.add_argument("--whisper-device", choices=("auto", "cuda", "cpu"), default="auto")
    parser.add_argument("--whisper-cpu-timeout", type=int, default=7200)
    parser.add_argument("--whisper-threads", type=int, default=12)
    parser.add_argument("--whisper-beam-size", type=int, default=5)
    parser.add_argument("--whisper-best-of", type=int, default=5)
    parser.add_argument("--whisper-cuda-safe-minimum-mb", type=int, default=4096)
    parser.add_argument("--whisper-runtime-status", default="")
    parser.add_argument("--disable-whisper-cpu-fallback", action="store_true")
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
    gpu_lifecycle: dict[str, Any] = {}
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

    progress(8, "running_qwen_worker")
    qwen_started = time.monotonic()
    gpu_lifecycle["before_qwen"] = gpu_snapshot()
    # Keep ASR conditioning deliberately tiny.  --context remains accepted for
    # backward-compatible command lines, but legacy prose is never sent to the
    # recognizer because it can be echoed into the transcript.
    context = PROFILE_CONTEXT[args.profile]
    profile_terms, title_terms = active_recognition_vocabulary(
        args.recognition_vocabulary, args.profile, args.title
    )
    primary_results, primary_worker = qwen_worker_transcribe(
        sys.executable,
        args.qwen_worker_script,
        args.input,
        regions,
        list(range(len(regions))),
        context,
        args.qwen_model,
        args.qwen_revision,
        args.device,
        max(1, args.batch_size),
        args.debug,
        args.mode,
    )
    qwen_texts = [primary_results.get(index, "") for index in range(len(regions))]
    gpu_lifecycle["after_qwen"] = primary_worker.get("gpu_after_load", {"available": False})
    gpu_lifecycle["after_qwen_asr"] = primary_worker.get("gpu_after_asr", {"available": False})
    qwen_phase_seconds = time.monotonic() - qwen_started
    retry_indexes: list[int] = []
    retry_reasons: dict[int, str] = {}
    for index, (region, text) in enumerate(zip(regions, qwen_texts)):
        seconds = (region.end - region.start) / samplerate
        reasons = suspicion_reasons(text, seconds, region.speech_probability, context)
        eligible, reason = should_retry_qwen(text, reasons, region.classification, region.speech_probability)
        # Preserve Fast's existing no-context prompt-leak recovery without
        # allowing general lexical fallback into the speed-oriented mode.
        if "prompt_leakage" in reasons or (args.mode != "fast" and eligible):
            retry_indexes.append(index)
            retry_reasons[index] = "prompt_leakage_unresolved" if "prompt_leakage" in reasons else reason
    retry_results = {
        int(item["index"]): normalize_text(item.get("text", ""))
        for item in primary_worker.get("retry_results", [])
    }
    worker_retry_reasons = {
        int(index): str(reason) for index, reason in primary_worker.get("retry_reasons", {}).items()
    }
    # Recompute in the parent as a defensive consistency check and retain the
    # worker's reason when available. Recognition thresholds remain unchanged.
    if set(retry_results) != set(retry_indexes):
        raise RuntimeError("isolated Qwen retry selection disagreed with parent selection")
    retry_reasons.update(worker_retry_reasons)
    qwen_retry_seconds = float(primary_worker.get("qwen_retry_seconds", 0.0))
    qwen_primary_seconds = max(0.0, qwen_phase_seconds - qwen_retry_seconds)
    gpu_lifecycle["after_qwen_retries"] = primary_worker.get(
        "gpu_after_retries", primary_worker.get("gpu_after_asr", {"available": False})
    )
    # A child process owns all Qwen tensors and its CUDA context. Its exit is
    # the hard lifecycle boundary; the parent process now has no Qwen model to
    # retain before launching whisper.cpp.
    qwen_cleanup = dict(primary_worker.get("cleanup", {}))
    qwen_cleanup["qwen_worker_process_exited"] = True
    qwen_model_deleted = True
    gpu_lifecycle["after_qwen_cleanup"] = gpu_snapshot()
    # Precompute, for every prompt-leak retry that resolved to a tiny
    # generic vocalization, how often nearby prompt-leak retries resolved to
    # the exact same token (PRIORITY 3/4). This only reads data already
    # produced above (regions, qwen_texts, retry_results) and is a plain
    # deterministic count -- no new model, no lookahead beyond what the
    # pipeline already computed for this file.
    prompt_leak_vocalization_positions: list[tuple[int, str, float, float]] = []
    for index, (region, text) in enumerate(zip(regions, qwen_texts)):
        if index not in retry_results:
            continue
        if "prompt_leakage" not in suspicion_reasons(text, (region.end - region.start) / samplerate, region.speech_probability, context):
            continue
        retry_text = retry_results[index]
        if not is_tiny_generic_vocalization_candidate(retry_text):
            continue
        token = meaningful_characters(retry_text).casefold()
        prompt_leak_vocalization_positions.append(
            (index, token, region.start / samplerate, region.end / samplerate)
        )
    prompt_leak_vocalization_indexes = {item[0] for item in prompt_leak_vocalization_positions}
    local_repetition_counts = local_prompt_leak_vocalization_repetition_counts(prompt_leak_vocalization_positions)
    decisions: list[Decision] = []
    previous: list[str] = []
    for index, (region, text) in enumerate(zip(regions, qwen_texts)):
        seconds = (region.end - region.start) / samplerate
        reasons = suspicion_reasons(text, seconds, region.speech_probability, context)
        features = transcript_features(text)
        if (
            len(previous) >= 3
            and features["meaningful_char_count"] >= 8
            and not features["vocalization_heavy"]
            and all(similarity(text, item) > 0.96 for item in previous[-3:])
        ):
            reasons.append("identical_neighbors")
        leaked, leak_score, fragment = detect_prompt_leakage(text, context)
        original = Candidate("qwen3", args.qwen_model, text, reasons, leak_score, fragment if leaked else "")
        candidates = {"qwen3": original}
        selected = original
        retry_selected = False
        retry_recovered = False
        retry_ambiguous_vocalization = False
        retry_vocalization_outcome = ""
        retry_similarity = 0.0
        eligibility_candidate = selected
        if index in retry_results:
            retry_text = retry_results[index]
            retry_suspicion = suspicion_reasons(retry_text, seconds, region.speech_probability, "")
            retry = Candidate("qwen_retry", args.qwen_model, retry_text, retry_suspicion, retry_without_context=True)
            candidates["qwen_retry"] = retry
            retry_similarity = similarity(text, retry_text)
            is_tiny_vocalization_candidate = index in prompt_leak_vocalization_indexes
            local_repetition_count = local_repetition_counts.get(index, 0)
            if qwen_retry_improves(
                original, retry, seconds, profile_terms, title_terms,
                region.speech_probability, region.classification,
                seconds, local_repetition_count,
            ):
                selected = retry
                eligibility_candidate = retry
                retry_selected = True
                retry_recovered = not bool(set(retry.suspicion) & (LEXICAL_REASONS | {"prompt_leakage"}))
                if is_tiny_vocalization_candidate:
                    retry_vocalization_outcome = "preserved_strong_evidence"
            elif "prompt_leakage" in original.suspicion and retry_is_low_evidence_vocalization(
                retry, region.speech_probability, region.classification,
                original, seconds, local_repetition_count,
            ):
                # The retry only recovered a tiny, low-confidence
                # vocalization from a leaked prompt, with no strong
                # timing/audio evidence of real speech behind it (often
                # confirmed by the same token repeating in nearby prompt-leak
                # retries -- PRIORITY 3). Prefer no subtitle over surfacing
                # either the leaked prompt text or an unsupported guess: an
                # empty selection is treated the same as any other unusable
                # transcript in the alignment pass below
                # (has_meaningful_transcript is False -> "failed", segment
                # dropped).
                selected = Candidate(
                    "ambiguous_vocalization", original.model, "",
                    ["ambiguous_vocalization_low_evidence"],
                )
                eligibility_candidate = selected
                retry_ambiguous_vocalization = True
                retry_vocalization_outcome = (
                    "rejected_repetition" if local_repetition_count >= 1 else "rejected_weak_evidence"
                )
            elif meaningful_candidate(retry):
                # A still-suspicious lexical retry is the best evidence for
                # fallback eligibility, without making it canonical output.
                eligibility_candidate = retry
        whisper_eligible, whisper_reason = should_use_whisper(
            eligibility_candidate,
            region.classification,
            region.speech_probability,
            args.mode,
            seconds,
            profile_terms | title_terms,
        )
        whisper_eligible = bool(
            mode_allows_whisper(args.mode) and whisper_eligible and not args.disable_whisper and args.whisper_model
        )
        decisions.append(
            Decision(
                region,
                selected,
                candidates,
                0.0,
                0.0,
                "fallback_required" if whisper_eligible else "review" if selected.suspicion else "accepted",
                qwen_retry_attempted=index in retry_results,
                qwen_retry_selected=retry_selected,
                qwen_retry_reason=retry_reasons.get(index, ""),
                qwen_retry_similarity=retry_similarity,
                qwen_retry_recovered=retry_recovered,
                qwen_retry_ambiguous_vocalization=retry_ambiguous_vocalization,
                qwen_retry_vocalization_outcome=retry_vocalization_outcome,
                whisper_eligible=whisper_eligible,
                whisper_reason=whisper_reason,
                fallback_audio=fallback_audio_stats(clips[index][0], clips[index][1], region) if whisper_eligible else {},
            )
        )
        previous.append(text)

    whisper_seconds = 0.0
    whisper_indexes = [index for index, decision in enumerate(decisions) if decision.whisper_eligible]
    whisper_result = WhisperBatchResult()
    if whisper_indexes:
        progress(45, "whisper_lexical_fallback")
        gpu_lifecycle["before_whisper"] = gpu_snapshot()
        whisper_started = time.monotonic()
        whisper_result = whisper_batch_transcribe(
            args.whisper_binary,
            args.whisper_model,
            clips,
            whisper_indexes,
            "ja",
            "cpu" if args.device == "cpu" else args.whisper_device,
            args.max_segment_seconds,
            not args.disable_whisper_cpu_fallback,
            args.whisper_cpu_timeout,
            args.whisper_threads,
            args.whisper_beam_size,
            args.whisper_best_of,
            args.whisper_cuda_safe_minimum_mb,
        )
        whisper_seconds = time.monotonic() - whisper_started
        gpu_lifecycle["after_whisper"] = gpu_snapshot()
        print(
            "Whisper fallback process: "
            f"exit={whisper_result.exit_code} success={whisper_result.success} "
            f"duration={whisper_result.duration_seconds:.3f}s candidates={len(whisper_indexes)} "
            f"device={whisper_result.execution_device} failure={whisper_result.failure_reason or 'none'} "
            f"cuda_failure={whisper_result.cuda_failure_reason or 'none'}",
            file=sys.stderr,
            flush=True,
        )
        if not whisper_result.success:
            print(
                "Whisper fallback failed: "
                f"reason={whisper_result.failure_reason} exit={whisper_result.exit_code} "
                f"stderr={whisper_result.stderr[:1024]}",
                file=sys.stderr,
                flush=True,
            )
        release_cuda()

    for index in whisper_indexes:
        decision = decisions[index]
        text = whisper_result.texts.get(index, "")
        seconds = (regions[index].end - regions[index].start) / samplerate
        whisper = Candidate(
            "whisper", args.whisper_model, text, suspicion_reasons(text, seconds, regions[index].speech_probability, "")
        )
        decision.candidates["whisper"] = whisper
        execution_error = whisper_result.candidate_errors.get(index, "")
        if execution_error:
            chosen, agreement, corrected, rejection = decision.selected, 0.0, False, execution_error
        else:
            chosen, agreement, corrected, rejection = choose_balanced_candidate(
                decision.candidates["qwen3"], decision.selected, whisper, seconds, profile_terms, title_terms,
                decision.whisper_reason,
            )
        decision.selected = chosen
        decision.comparison_score = agreement
        decision.whisper_candidate_valid = rejection in {"", "no_clear_improvement"}
        decision.whisper_rejection_reason = rejection
        decision.whisper_execution = {
            "whisper_process_exit_code": whisper_result.exit_code,
            "whisper_process_success": whisper_result.success,
            "whisper_process_stderr": whisper_result.stderr,
            "whisper_process_stdout_summary": whisper_result.stdout_summary,
            "whisper_process_duration_seconds": whisper_result.duration_seconds,
            "whisper_process_failure_reason": whisper_result.failure_reason,
            "whisper_cuda_attempted": whisper_result.cuda_attempted,
            "whisper_cuda_failed": whisper_result.cuda_failed,
            "whisper_cuda_failure_reason": whisper_result.cuda_failure_reason,
            "whisper_cuda_preflight_skipped": whisper_result.cuda_preflight_skipped,
            "whisper_cuda_preflight_free_mb": whisper_result.cuda_preflight_free_mb,
            "whisper_cuda_safe_minimum_mb": whisper_result.cuda_safe_minimum_mb,
            "whisper_cuda_stderr": whisper_result.cuda_stderr,
            "whisper_cpu_fallback_attempted": whisper_result.cpu_fallback_attempted,
            "whisper_cpu_fallback_succeeded": whisper_result.cpu_fallback_succeeded,
            "whisper_execution_device": whisper_result.execution_device,
            "cpu_fallback_seconds": whisper_result.cpu_fallback_seconds,
            "cpu_fallback_timeout": whisper_result.cpu_fallback_timeout,
            "cpu_fallback_candidate_count": whisper_result.cpu_fallback_candidate_count,
            "source_start_ms": decision.fallback_audio.get("fallback_audio_source_start_ms"),
            "source_end_ms": decision.fallback_audio.get("fallback_audio_source_end_ms"),
            **whisper_result.candidate_details.get(index, {}),
        }
        decision.quality_state = "accepted" if corrected and not chosen.suspicion else "review"

    # Fast never escalates prompt leakage to another ASR, but leaked meta-text
    # must also never become accepted Japanese or reach the translator.
    if args.mode == "fast":
        for decision in decisions:
            if "prompt_leakage" in decision.selected.suspicion:
                decision.quality_state = "failed"

    progress(62, "aligning")
    align_started = time.monotonic()
    gpu_lifecycle["before_aligner"] = gpu_snapshot()
    aligner = load_aligner(args.aligner_model, args.aligner_revision, args.device)
    output_segments: list[dict[str, Any]] = []
    alignment_failures = 0
    alignment_integrity_failures = 0
    alignment_timing_only_segments = 0
    alignment_recovered_segments = 0
    total_alignment_loss = 0.0
    for index, (decision, clip) in enumerate(zip(decisions, clips)):
        selected = normalize_text(decision.selected.text)
        features = transcript_features(selected)
        region_duration = (decision.region.end - decision.region.start) / samplerate
        tiny_long = is_tiny_transcript_long_region(selected, region_duration)
        vocalization_long = features["vocalization_heavy"] and features["meaningful_char_count"] <= 12 and region_duration > 8.0
        timing_recovery_needed = tiny_long or vocalization_long
        if not has_meaningful_transcript(selected) or "prompt_leakage" in decision.selected.suspicion:
            decision.quality_state = "failed"
            decision.confidence = 0
            decision.timing_quality = "timing_suspicious"
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
                decision.timing_quality = "timing_good"
                groups = split_alignment(items)
            else:
                alignment_integrity_failures += 1
                alignment_timing_only_segments += 1
                decision.alignment = "timing_only"
                decision.quality_state = "review"
                aligned_duration = items[-1]["end_time"] - items[0]["start_time"]
                if timing_recovery_needed and aligned_duration > 8.0:
                    recovered_start, recovered_end, timing_state = recover_tiny_timing(
                        clip[0], clip[1], decision.region.speech_probability, decision.region.classification
                    )
                    decision.timing_quality = timing_state
                    groups = [[{"text": selected, "start_time": recovered_start, "end_time": recovered_end}]]
                else:
                    decision.timing_quality = "timing_good"
                    groups = [[{"text": selected, "start_time": items[0]["start_time"], "end_time": items[-1]["end_time"]}]]
        except Exception:
            alignment_failures += 1
            decision.alignment = "vad_fallback"
            decision.quality_state = "review"
            clip_seconds = len(clip[0]) / clip[1]
            if timing_recovery_needed:
                recovered_start, recovered_end, timing_state = recover_tiny_timing(
                    clip[0], clip[1], decision.region.speech_probability, decision.region.classification
                )
                decision.timing_quality = timing_state
                groups = [[{"text": selected, "start_time": recovered_start, "end_time": recovered_end}]]
            else:
                decision.timing_quality = "timing_vad_fallback"
                groups = [[{"text": selected, "start_time": 0.0, "end_time": clip_seconds}]]
            integrity = alignment_integrity(selected, selected)
        decision.confidence = confidence_for(decision)
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
            decision.timing_quality = "timing_vad_fallback"
        for group in groups:
            text = "".join(item["text"] for item in group).strip()
            if not text:
                continue
            start_ms = round((base_seconds + group[0]["start_time"]) * 1000)
            end_ms = round((base_seconds + group[-1]["end_time"]) * 1000)
            region_end_ms = round(decision.region.end / samplerate * 1000)
            end_ms = min(region_end_ms, max(end_ms, start_ms + 350))
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
                "timing_quality_state": decision.timing_quality,
                "suspicion_category": suspicion_category(selected, decision.selected.suspicion, decision.timing_quality),
            }
            anchors = trusted_timing_anchors(group, base_seconds, decision.alignment)
            if anchors:
                record["timing_anchors_ms"] = anchors
            if args.debug:
                record["candidates"] = candidate_payload
                record["alignment_items"] = group
                record["vad_speech_probability"] = round(decision.region.speech_probability, 3)
                record["canonical_text"] = selected
                record["aligned_text"] = "".join(item["text"] for item in decision.aligned_items)
                record["alignment_similarity"] = integrity["similarity"]
                record["alignment_coverage"] = integrity["coverage"]
                record["alignment_integrity_state"] = decision.alignment
                record["meaningful_char_count"] = features["meaningful_char_count"]
                record["vocalization_ratio"] = features["vocalization_ratio"]
                record["lexical_ratio"] = features["lexical_ratio"]
                record["region_duration"] = round(region_duration, 3)
                record["tiny_transcript_long_region"] = tiny_long
                record["vocalization_long_region"] = vocalization_long
                retry = decision.candidates.get("qwen_retry")
                whisper = decision.candidates.get("whisper")
                record["qwen_retry_attempted"] = decision.qwen_retry_attempted
                record["qwen_retry_selected"] = decision.qwen_retry_selected
                record["qwen_retry_reason"] = decision.qwen_retry_reason
                record["qwen_retry_original_text"] = decision.candidates["qwen3"].text
                record["qwen_retry_text"] = retry.text if retry else ""
                record["qwen_retry_similarity"] = round(decision.qwen_retry_similarity, 3)
                record["qwen_retry_recovered"] = decision.qwen_retry_recovered
                record["whisper_eligible"] = decision.whisper_eligible
                record["whisper_reason"] = decision.whisper_reason
                record["whisper_candidate_valid"] = decision.whisper_candidate_valid
                record["whisper_rejection_reason"] = decision.whisper_rejection_reason
                record["whisper_text"] = whisper.text if whisper else ""
                record["whisper_meaningful_char_count"] = transcript_features(whisper.text)["meaningful_char_count"] if whisper else 0
                record["whisper_lexical_ratio"] = transcript_features(whisper.text)["lexical_ratio"] if whisper else 0
                record.update(decision.fallback_audio)
                record.update(decision.whisper_execution)
                record["parent_vad_region_id"] = decision.region.parent_vad_region_id
                record["split_index"] = decision.region.split_index
                record["split_count"] = decision.region.split_count
                record["split_strategy"] = decision.region.split_strategy
            output_segments.append(record)
        if index % 8 == 0:
            progress(62 + round(30 * (index + 1) / len(decisions)), "aligning")
    del aligner
    release_cuda()
    gpu_lifecycle["after_aligner"] = gpu_snapshot()
    alignment_seconds = time.monotonic() - align_started

    output_segments.sort(key=lambda item: (item["start_ms"], item["end_ms"]))
    retry_count = sum(item.qwen_retry_attempted for item in decisions)
    retry_selected = sum(item.qwen_retry_selected for item in decisions)
    retry_recovered = sum(item.qwen_retry_recovered for item in decisions)
    retry_reason_counts: dict[str, int] = {}
    for item in decisions:
        if item.qwen_retry_attempted:
            reason = item.qwen_retry_reason or "lexical_suspicion"
            retry_reason_counts[reason] = retry_reason_counts.get(reason, 0) + 1
    whisper_count = len(whisper_indexes)
    whisper_nonempty = sum(
        index not in whisper_result.candidate_errors
        and has_meaningful_transcript(whisper_result.texts.get(index, ""))
        for index in whisper_indexes
    )
    whisper_failed = sum(bool(whisper_result.candidate_errors.get(index)) for index in whisper_indexes)
    whisper_empty = sum(
        not has_meaningful_transcript(whisper_result.texts.get(index, ""))
        for index in whisper_indexes
        if index not in whisper_result.candidate_errors
    )
    whisper_selected = sum(item.selected.engine == "whisper" for item in decisions)
    whisper_rejected = whisper_count - whisper_selected
    whisper_reason_counts: dict[str, int] = {}
    whisper_selected_by_reason: dict[str, int] = {}
    whisper_rejected_by_reason: dict[str, int] = {}
    for index in whisper_indexes:
        reason = decisions[index].whisper_reason or "lexical_suspicion"
        whisper_reason_counts[reason] = whisper_reason_counts.get(reason, 0) + 1
        target = whisper_selected_by_reason if decisions[index].selected.engine == "whisper" else whisper_rejected_by_reason
        target[reason] = target.get(reason, 0) + 1
    whisper_benefit_by_reason = {
        reason: {
            "candidates": count,
            "selected": whisper_selected_by_reason.get(reason, 0),
            "rejected": whisper_rejected_by_reason.get(reason, 0),
            "benefit_percentage": round(whisper_selected_by_reason.get(reason, 0) * 100 / count, 2),
            "average_runtime_seconds_per_candidate": round(whisper_seconds / max(1, whisper_count), 3),
        }
        for reason, count in whisper_reason_counts.items()
    }
    whisper_corrections = []
    for index in whisper_indexes:
        decision = decisions[index]
        if decision.selected.engine != "whisper":
            continue
        retry = decision.candidates.get("qwen_retry")
        whisper = decision.candidates.get("whisper")
        whisper_corrections.append({
            "source_segment_index": index,
            "source_suspicion_reason": decision.whisper_reason,
            "mode": args.mode,
            "original_qwen_text": decision.candidates["qwen3"].text,
            "qwen_retry_text": retry.text if retry else "",
            "whisper_text": whisper.text if whisper else "",
            "comparison_score": round(decision.comparison_score, 3),
            "selected": True,
        })
    review_count = sum(item.quality_state != "accepted" for item in decisions)
    leakage_indexes = [index for index, item in enumerate(decisions) if "prompt_leakage" in item.candidates["qwen3"].suspicion]
    leakage_segments = len(leakage_indexes)
    leakage_recovered = sum("prompt_leakage" not in decisions[index].selected.suspicion for index in leakage_indexes)
    leakage_retry_clean = sum(
        meaningful_candidate(decisions[index].candidates.get("qwen_retry")) for index in leakage_indexes
    )
    leakage_retry_selected = sum(decisions[index].qwen_retry_selected for index in leakage_indexes)
    leakage_retry_recovered = sum(
        decisions[index].qwen_retry_selected
        and "prompt_leakage" not in decisions[index].selected.suspicion
        for index in leakage_indexes
    )
    leakage_escalated = sum(decisions[index].whisper_eligible for index in leakage_indexes)
    leakage_ambiguous_vocalization = sum(
        decisions[index].qwen_retry_ambiguous_vocalization for index in leakage_indexes
    )
    leakage_repetition_rejected = sum(
        decisions[index].qwen_retry_vocalization_outcome == "rejected_repetition" for index in leakage_indexes
    )
    leakage_weak_timing_rejected = sum(
        decisions[index].qwen_retry_vocalization_outcome == "rejected_weak_evidence" for index in leakage_indexes
    )
    leakage_strong_evidence_preserved = sum(
        decisions[index].qwen_retry_vocalization_outcome == "preserved_strong_evidence" for index in leakage_indexes
    )
    suspicion_counts: dict[str, int] = {}
    for decision in decisions:
        for reason in decision.candidates["qwen3"].suspicion:
            suspicion_counts[reason] = suspicion_counts.get(reason, 0) + 1
    total_seconds = time.monotonic() - started
    punctuation_only_segments = sum(bool(normalize_text(item.selected.text)) and not has_meaningful_transcript(item.selected.text) for item in decisions)
    timing_quality_counts: dict[str, int] = {}
    suspicion_category_counts: dict[str, int] = {}
    for item in decisions:
        timing_quality_counts[item.timing_quality] = timing_quality_counts.get(item.timing_quality, 0) + 1
        category = suspicion_category(item.selected.text, item.selected.suspicion, item.timing_quality)
        suspicion_category_counts[category] = suspicion_category_counts.get(category, 0) + 1
    output_durations = [item["end_ms"] - item["start_ms"] for item in output_segments]
    tiny_long_output = sum(
        transcript_features(item["text"])["meaningful_char_count"] <= 3 and item["end_ms"] - item["start_ms"] > 10000
        for item in output_segments
    )
    hard_max_splits = sum(item.region.split_strategy == "hard_max" for item in decisions)
    energy_valley_splits = sum(item.region.split_strategy == "energy_valley" for item in decisions)
    warnings: list[str] = []
    if whisper_count and whisper_result.success and whisper_nonempty == 0:
        warnings.append("whisper_all_candidates_empty")
    if whisper_count >= 20 and whisper_seconds < 3.0:
        warnings.append("whisper_runtime_suspiciously_low")
    if whisper_result.cuda_failure_reason == "whisper_cuda_oom":
        warnings.append("whisper_cuda_oom")
        if not whisper_result.success:
            warnings.append("whisper_batch_failed_cuda_oom")
    if leakage_segments >= 20 and leakage_recovered / max(1, leakage_segments) < 0.2:
        warnings.append("prompt_leak_recovery_regressed")
    high_accuracy_reasons = {
        "long_lexical_verification",
        "medium_confidence_verification",
        "vocabulary_uncertainty",
    }
    if args.mode == "high_accuracy" and not any(item.whisper_reason in high_accuracy_reasons for item in decisions):
        warnings.append("high_accuracy_same_as_balanced")
    before_qwen_used = gpu_lifecycle.get("before_qwen", {}).get("used_mb")
    before_whisper_used = gpu_lifecycle.get("before_whisper", {}).get("used_mb")
    if (
        before_qwen_used is not None and before_whisper_used is not None
        and before_whisper_used > before_qwen_used + 512
    ):
        warnings.append("qwen_vram_not_released_before_whisper")

    def gpu_used(stage: str) -> int | None:
        return gpu_lifecycle.get(stage, {}).get("used_mb")
    primary_torch_after = primary_worker.get("torch_after_asr", {})
    final_worker = primary_worker
    final_torch_cleanup = final_worker.get("torch_after_cleanup", {})
    worker_before = primary_worker.get("gpu_before_load", {}).get("used_mb")
    worker_context = primary_worker.get("gpu_after_context", {}).get("used_mb")
    idle_python_cuda_context_mb = (
        max(0, worker_context - worker_before)
        if isinstance(worker_context, int) and isinstance(worker_before, int) else None
    )
    after_cleanup_used = gpu_used("after_qwen_cleanup")
    qwen_cleanup_excess_gpu_mb = (
        max(0, after_cleanup_used - before_qwen_used)
        if isinstance(after_cleanup_used, int) and isinstance(before_qwen_used, int) else None
    )
    whisper_model = whisper_model_metadata(args.whisper_model)
    payload = {
        "duration_ms": round(duration * 1000),
        "processed_ms": round(duration * 1000),
        "language": "ja",
        "pipeline_version": PIPELINE_VERSION,
        "profile": args.profile,
        "asr_mode": args.mode,
        "fallback_architecture": {
            "asr_primary": args.qwen_model,
            "asr_retry_engine": args.qwen_model,
            "asr_secondary": args.whisper_model if not args.disable_whisper else "disabled",
            "fallback_strategy": "lexical_qwen_retry_then_whisper",
            "reazon_status": "experimental_inactive",
        },
        "model_versions": {
            "asr_primary": args.qwen_model,
            "asr_primary_revision": args.qwen_revision,
            "asr_retry_engine": args.qwen_model,
            "asr_secondary": args.whisper_model if not args.disable_whisper else "disabled",
            "asr_experimental": args.reazon_model if not args.disable_reazon else "disabled",
            "aligner": args.aligner_model,
            "aligner_revision": args.aligner_revision,
        },
        "metrics": {
            "source_duration_seconds": round(duration, 3),
            "vad_seconds": round(vad_seconds, 3),
            "qwen_asr_seconds": round(qwen_primary_seconds, 3),
            "qwen_retry_seconds": round(qwen_retry_seconds, 3),
            "qwen_worker_total_seconds": round(qwen_phase_seconds, 3),
            "whisper_seconds": round(whisper_seconds, 3),
            "alignment_seconds": round(alignment_seconds, 3),
            "total_processing_seconds": round(total_seconds, 3),
            "real_time_factor": round(total_seconds / duration, 4) if duration else 0,
            "vad_regions": len(regions),
            "qwen_segments": len(decisions),
            "qwen_retry_candidates": retry_count,
            "qwen_retry_attempted": retry_count,
            "qwen_retry_selected": retry_selected,
            "qwen_retry_recovered": retry_recovered,
            "qwen_retry_unresolved": retry_count - retry_recovered,
            "qwen_retry_percentage": round(retry_count * 100 / len(decisions), 2),
            "qwen_retry_reason_counts": retry_reason_counts,
            "whisper_candidates": whisper_count,
            "whisper_candidates_attempted": whisper_count,
            "whisper_candidates_succeeded": whisper_count - whisper_failed,
            "whisper_candidates_failed": whisper_failed,
            "whisper_nonempty_candidates": whisper_nonempty,
            "whisper_empty_candidates": whisper_empty,
            "whisper_selected_segments": whisper_selected,
            "whisper_corrected_segments": whisper_selected,
            "whisper_rejected_segments": whisper_rejected,
            "whisper_fallback_segments": whisper_count,
            "whisper_fallback_percentage": round(whisper_count * 100 / len(decisions), 2),
            "whisper_benefit_percentage": round(whisper_selected * 100 / whisper_count, 2) if whisper_count else 0,
            "whisper_reason_counts": whisper_reason_counts,
            "whisper_selected_by_reason": whisper_selected_by_reason,
            "whisper_rejected_by_reason": whisper_rejected_by_reason,
            "whisper_benefit_by_reason": whisper_benefit_by_reason,
            "whisper_process_exit_code": whisper_result.exit_code,
            "whisper_process_success": whisper_result.success,
            "whisper_process_stderr": whisper_result.stderr,
            "whisper_process_stdout_summary": whisper_result.stdout_summary,
            "whisper_process_duration_seconds": whisper_result.duration_seconds,
            "whisper_process_failure_reason": whisper_result.failure_reason,
            "whisper_cuda_attempted": whisper_result.cuda_attempted,
            "whisper_cuda_failed": whisper_result.cuda_failed,
            "whisper_cuda_failure_reason": whisper_result.cuda_failure_reason,
            "whisper_cuda_stderr": whisper_result.cuda_stderr,
            "whisper_cuda_preflight_skipped": whisper_result.cuda_preflight_skipped,
            "whisper_cuda_preflight_free_mb": whisper_result.cuda_preflight_free_mb,
            "whisper_cuda_safe_minimum_mb": whisper_result.cuda_safe_minimum_mb,
            "whisper_cpu_fallback_attempted": whisper_result.cpu_fallback_attempted,
            "whisper_cpu_fallback_succeeded": whisper_result.cpu_fallback_succeeded,
            "whisper_execution_device": whisper_result.execution_device,
            "whisper_effective_threads": max(1, args.whisper_threads),
            "whisper_effective_beam_size": max(1, args.whisper_beam_size),
            "whisper_effective_best_of": max(1, args.whisper_best_of),
            "whisper_model_path": whisper_model["path"],
            "whisper_model_size_mb": whisper_model["size_mb"],
            "whisper_model_quantization": whisper_model["quantization"],
            "cpu_fallback_seconds": whisper_result.cpu_fallback_seconds,
            "cpu_fallback_timeout": whisper_result.cpu_fallback_timeout,
            "cpu_fallback_candidate_count": whisper_result.cpu_fallback_candidate_count,
            "gpu_used_mb_before_qwen": gpu_used("before_qwen"),
            "gpu_used_mb_after_qwen": gpu_used("after_qwen"),
            "gpu_used_mb_after_qwen_asr": gpu_used("after_qwen_asr"),
            "gpu_used_mb_after_qwen_retries": gpu_used("after_qwen_retries"),
            "gpu_used_mb_after_qwen_cleanup": gpu_used("after_qwen_cleanup"),
            "gpu_used_mb_before_whisper": gpu_used("before_whisper"),
            "gpu_used_mb_after_whisper": gpu_used("after_whisper"),
            "gpu_used_mb_before_aligner": gpu_used("before_aligner"),
            "gpu_used_mb_after_aligner": gpu_used("after_aligner"),
            "whisper_gpu_total_mb_before_launch": gpu_lifecycle.get("before_whisper", {}).get("total_mb"),
            "whisper_gpu_used_mb_before_launch": before_whisper_used,
            "whisper_gpu_free_mb_before_launch": gpu_lifecycle.get("before_whisper", {}).get("free_mb"),
            "whisper_gpu_processes_before_launch": gpu_lifecycle.get("before_whisper", {}).get("processes", []),
            "qwen_model_deleted": qwen_model_deleted,
            "qwen_worker_process_exited": True,
            "torch_allocated_mb_after_qwen": primary_torch_after.get("allocated_mb"),
            "torch_reserved_mb_after_qwen": primary_torch_after.get("reserved_mb"),
            "torch_max_allocated_mb_after_qwen": primary_torch_after.get("max_allocated_mb"),
            "torch_max_reserved_mb_after_qwen": primary_torch_after.get("max_reserved_mb"),
            "torch_allocated_mb_after_cleanup": final_torch_cleanup.get("allocated_mb"),
            "torch_reserved_mb_after_cleanup": final_torch_cleanup.get("reserved_mb"),
            "idle_python_cuda_context_mb": idle_python_cuda_context_mb,
            "qwen_cleanup_excess_gpu_mb": qwen_cleanup_excess_gpu_mb,
            "surviving_cuda_tensors_after_cleanup": final_worker.get("surviving_cuda_tensors_after_cleanup", {}),
            **qwen_cleanup,
            "review_segments": review_count,
            "low_confidence_percentage": round(review_count * 100 / len(decisions), 2),
            "alignment_failures": alignment_failures,
            "alignment_integrity_failures": alignment_integrity_failures,
            "alignment_timing_only_segments": alignment_timing_only_segments,
            "alignment_recovered_segments": alignment_recovered_segments,
            "alignment_text_loss_percentage": round(total_alignment_loss * 100 / len(decisions), 2),
            "timing_quality_states": timing_quality_counts,
            "suspicion_categories": suspicion_category_counts,
            "subtitle_rows_over_10_seconds": sum(duration > 10000 for duration in output_durations),
            "subtitle_rows_over_20_seconds": sum(duration > 20000 for duration in output_durations),
            "subtitle_rows_over_30_seconds": sum(duration > 30000 for duration in output_durations),
            "subtitle_rows_at_segment_limit": sum(abs(duration - args.max_segment_seconds * 1000) <= 100 for duration in output_durations),
            "tiny_transcript_rows_over_10_seconds": tiny_long_output,
            "punctuation_only_segments_suppressed": punctuation_only_segments,
            "hard_max_split_regions": hard_max_splits,
            "energy_valley_split_regions": energy_valley_splits,
            "prompt_leakage_segments": leakage_segments,
            "prompt_leakage_detected": leakage_segments,
            "prompt_leakage_retry_attempted": len(leakage_indexes),
            "prompt_leakage_retry_clean": leakage_retry_clean,
            "prompt_leakage_retry_selected": leakage_retry_selected,
            "prompt_leakage_retry_failed": leakage_segments - leakage_retry_clean,
            "prompt_leakage_escalated_to_whisper": leakage_escalated,
            "prompt_leakage_retry_ambiguous_vocalization": leakage_ambiguous_vocalization,
            "prompt_leakage_retry_vocalization_repetition_rejected": leakage_repetition_rejected,
            "prompt_leakage_retry_vocalization_weak_timing_rejected": leakage_weak_timing_rejected,
            "prompt_leakage_retry_vocalization_strong_evidence_preserved": leakage_strong_evidence_preserved,
            "prompt_leakage_retries": len(leakage_indexes),
            "prompt_leakage_recovered": leakage_recovered,
            "prompt_leakage_unresolved": leakage_segments - leakage_recovered,
            "prompt_leak_retry_detected": leakage_segments,
            "prompt_leak_retry_attempted": len(leakage_indexes),
            "prompt_leak_retry_selected": leakage_retry_selected,
            "prompt_leak_retry_recovered": leakage_retry_recovered,
            "prompt_leak_retry_unresolved": leakage_segments - leakage_retry_recovered,
            "prompt_leakage_percentage": round(leakage_segments * 100 / len(decisions), 2),
            "suspicion_reasons": suspicion_counts,
            "runtime_warnings": warnings,
        },
        "whisper_corrections": whisper_corrections,
        "segments": output_segments,
    }
    if args.whisper_runtime_status and whisper_indexes:
        try:
            os.makedirs(os.path.dirname(args.whisper_runtime_status) or ".", exist_ok=True)
            write_json(args.whisper_runtime_status, {
                "updated_unix": round(time.time()),
                "last_load_result": (
                    "cpu_fallback_success" if whisper_result.cpu_fallback_succeeded else
                    "success" if whisper_result.success else whisper_result.failure_reason
                ),
                "last_cuda_failure": whisper_result.cuda_failure_reason,
                "cuda_preflight_skipped": whisper_result.cuda_preflight_skipped,
                "cuda_preflight_free_mb": whisper_result.cuda_preflight_free_mb,
                "execution_device": whisper_result.execution_device,
                "cpu_fallback_available": not args.disable_whisper_cpu_fallback,
            })
        except OSError as error:
            payload["metrics"]["runtime_warnings"].append("whisper_runtime_status_write_failed")
            payload["metrics"]["whisper_runtime_status_error"] = normalize_text(str(error))[:1024]
    write_json(args.output, payload)
    progress(100, "complete")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"Qwen Japanese ASR pipeline failed: {error}", file=sys.stderr)
        raise SystemExit(1)
