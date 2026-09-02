#!/usr/bin/env python3
"""Summarize before/after subtitle diagnostics without printing dialogue."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

from qwen_pipeline import has_meaningful_transcript, transcript_features


def summarize(path: Path) -> dict[str, Any]:
    document = json.loads(path.read_text(encoding="utf-8"))
    diagnostics = document.get("diagnostics") or document
    metrics = diagnostics.get("metrics") or document.get("diagnostic_summary") or {}
    segments = diagnostics.get("segments") or document.get("japanese") or []
    durations = [max(0, int(item.get("end_ms", 0)) - int(item.get("start_ms", 0))) for item in segments]
    punctuation = sum(bool(str(item.get("text", "")).strip()) and not has_meaningful_transcript(item.get("text", "")) for item in segments)
    tiny_long = sum(
        transcript_features(item.get("text", ""))["meaningful_char_count"] <= 3 and duration > 10_000
        for item, duration in zip(segments, durations)
    )
    fields = (
        "vad_regions",
        "qwen_segments",
        "reazon_candidates",
        "reazon_nonempty_candidates",
        "reazon_empty_candidates",
        "reazon_empty_percentage",
        "reazon_selected_segments",
        "reazon_corrected_segments",
        "fallback_benefit_percentage",
        "qwen_retry_candidates",
        "qwen_retry_attempted",
        "qwen_retry_selected",
        "qwen_retry_recovered",
        "qwen_retry_unresolved",
        "qwen_retry_percentage",
        "qwen_retry_seconds",
        "whisper_candidates",
        "whisper_candidates_attempted",
        "whisper_candidates_succeeded",
        "whisper_candidates_failed",
        "whisper_nonempty_candidates",
        "whisper_empty_candidates",
        "whisper_selected_segments",
        "whisper_corrected_segments",
        "whisper_rejected_segments",
        "whisper_fallback_percentage",
        "whisper_benefit_percentage",
        "whisper_seconds",
        "whisper_process_exit_code",
        "whisper_process_success",
        "whisper_process_duration_seconds",
        "whisper_process_failure_reason",
        "whisper_reason_counts",
        "prompt_leakage_detected",
        "prompt_leakage_retry_attempted",
        "prompt_leakage_retry_clean",
        "prompt_leakage_retry_selected",
        "prompt_leakage_retry_failed",
        "prompt_leakage_escalated_to_whisper",
        "runtime_warnings",
        "review_segments",
        "low_confidence_percentage",
        "alignment_failures",
        "alignment_integrity_failures",
        "total_processing_seconds",
        "real_time_factor",
    )
    summary = {field: metrics.get(field) for field in fields}
    if summary["reazon_candidates"] is None:
        summary["reazon_candidates"] = metrics.get("reazon_fallback_segments")
    observed_reazon = [item for item in segments if (item.get("candidates") or {}).get("reazon") is not None]
    observed_nonempty = sum(
        has_meaningful_transcript(item["candidates"]["reazon"].get("text", "")) for item in observed_reazon
    )
    if summary["reazon_nonempty_candidates"] is None and observed_reazon:
        observed_selected = sum(item.get("selected_engine") == "reazon" for item in observed_reazon)
        summary["reazon_nonempty_candidates"] = observed_nonempty
        summary["reazon_empty_candidates"] = len(observed_reazon) - observed_nonempty
        summary["reazon_empty_percentage"] = round((len(observed_reazon) - observed_nonempty) * 100 / len(observed_reazon), 2)
        summary["reazon_selected_segments"] = observed_selected
        summary["reazon_corrected_segments"] = observed_selected
        summary["fallback_benefit_percentage"] = round(observed_selected * 100 / len(observed_reazon), 2)
    summary.update(
        {
            "file": str(path),
            "pipeline_version": diagnostics.get("pipeline_version") or document.get("pipeline_version"),
            "profile": diagnostics.get("profile") or document.get("profile"),
            "asr_mode": diagnostics.get("asr_mode") or document.get("asr_mode"),
            "subtitle_rows": len(segments),
            "subtitle_rows_over_10_seconds": sum(value > 10_000 for value in durations),
            "subtitle_rows_over_20_seconds": sum(value > 20_000 for value in durations),
            "subtitle_rows_over_30_seconds": sum(value > 30_000 for value in durations),
            "near_30_second_rows": sum(29_900 <= value <= 30_100 for value in durations),
            "tiny_transcript_rows_over_10_seconds": tiny_long,
            "punctuation_only_rows": punctuation,
        }
    )
    return summary


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("diagnostics", nargs="+", type=Path)
    args = parser.parse_args()
    print(json.dumps([summarize(path) for path in args.diagnostics], ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
