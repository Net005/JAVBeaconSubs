import importlib.util
import json
import os
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock

import numpy as np


MODULE_PATH = pathlib.Path(__file__).with_name("qwen_pipeline.py")
SPEC = importlib.util.spec_from_file_location("qwen_pipeline", MODULE_PATH)
pipeline = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = pipeline
SPEC.loader.exec_module(pipeline)


class PipelineUtilitiesTest(unittest.TestCase):
    def run_synthetic_pipeline(
        self, transcript, align_side_effect=None, mode="balanced", retry_transcript=None,
        speech_probability=0.55, classification="speech",
    ):
        samplerate = 1000
        audio = np.zeros(samplerate * 30, dtype=np.float32)
        audio[samplerate * 12 : samplerate * 13] = 0.04
        with tempfile.TemporaryDirectory() as directory:
            input_path = pathlib.Path(directory) / "input.wav"
            output_path = pathlib.Path(directory) / "output.json"
            fake_soundfile = types.SimpleNamespace(read=lambda *_args, **_kwargs: (audio, samplerate))
            aligner = mock.Mock()
            aligner.align.side_effect = align_side_effect
            reasons = pipeline.suspicion_reasons(transcript, 30.0, speech_probability, pipeline.PROFILE_CONTEXT["jav"])
            eligible, retry_reason = pipeline.should_retry_qwen(transcript, reasons, classification, speech_probability)
            wants_retry = "prompt_leakage" in reasons or (mode != "fast" and eligible)
            retry_value = retry_transcript if retry_transcript is not None else transcript
            arguments = [
                "qwen_pipeline.py", "--input", str(input_path), "--output", str(output_path),
                "--device", "cpu", "--mode", mode, "--disable-reazon", "--disable-whisper", "--debug",
            ]
            with (
                mock.patch.object(sys, "argv", arguments),
                mock.patch.object(
                    pipeline, "detect_regions",
                    return_value=[pipeline.Region(0, len(audio), speech_probability, classification)],
                ),
                mock.patch.object(
                    pipeline,
                    "qwen_worker_transcribe",
                    return_value=({0: transcript}, {
                        "retry_results": (
                            [{"index": 0, "text": retry_value}] if wants_retry else []
                        ),
                        "retry_reasons": ({
                            "0": "prompt_leakage_unresolved" if "prompt_leakage" in reasons else retry_reason
                        } if wants_retry else {}),
                    }),
                ),
                mock.patch.object(pipeline, "load_aligner", return_value=aligner),
                mock.patch.object(
                    pipeline, "release_cuda",
                    return_value={"qwen_gc_collected": 0, "cuda_cache_cleared": False, "cuda_ipc_collected": False},
                ),
                mock.patch.object(pipeline, "gpu_snapshot", return_value={"available": False}),
                mock.patch.object(pipeline, "progress"),
                mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}),
            ):
                self.assertEqual(pipeline.main(), 0)
            return json.loads(output_path.read_text(encoding="utf-8")), aligner

    def test_meaningful_transcript_rejects_only_formatting(self):
        for value in ["。", "、。", "...", "「」", "　", "。\n"]:
            self.assertFalse(pipeline.has_meaningful_transcript(value), value)
        for value in ["あ", "はい", "A", "NHK", "123"]:
            self.assertTrue(pipeline.has_meaningful_transcript(value), value)

    def test_punctuation_only_never_aligns_or_emits(self):
        result, aligner = self.run_synthetic_pipeline("。")
        self.assertEqual(result["segments"], [])
        self.assertEqual(result["metrics"]["punctuation_only_segments_suppressed"], 1)
        aligner.align.assert_not_called()

    def test_single_n_alignment_failure_gets_bounded_energy_timing(self):
        result, _ = self.run_synthetic_pipeline("ん", RuntimeError("alignment failed"))
        self.assertEqual(len(result["segments"]), 1)
        segment = result["segments"][0]
        self.assertEqual(segment["text"], "ん")
        self.assertEqual(segment["timing_quality_state"], "timing_energy_recovered")
        self.assertLessEqual(segment["end_ms"] - segment["start_ms"], 2500)
        self.assertEqual(result["metrics"]["tiny_transcript_rows_over_10_seconds"], 0)

    def test_recall_vad_finds_quiet_padded_dialogue(self):
        samplerate = 16000
        audio = np.zeros(samplerate * 3, dtype=np.float32)
        t = np.arange(samplerate) / samplerate
        audio[samplerate : samplerate * 2] = 0.006 * np.sin(2 * np.pi * 220 * t)
        regions = pipeline.detect_regions(audio, samplerate, 1.45, 90, 500, 350, 600, 30)
        self.assertEqual(len(regions), 1)
        self.assertLessEqual(regions[0].start, samplerate)
        self.assertGreaterEqual(regions[0].end, samplerate * 2)

    def test_vad_splits_continuous_audio_at_maximum(self):
        samplerate = 1000
        audio = np.ones(samplerate * 65, dtype=np.float32) * 0.02
        regions = pipeline.detect_regions(audio, samplerate, 1.1, 90, 500, 0, 0, 30)
        self.assertEqual([item.end - item.start for item in regions], [30000, 30000, 5000])
        self.assertEqual([item.split_strategy for item in regions[:2]], ["hard_max", "hard_max"])

    def test_oversized_region_prefers_internal_energy_valley(self):
        samplerate = 1000
        audio = np.ones(samplerate * 50, dtype=np.float32) * 0.02
        audio[samplerate * 26 : samplerate * 28] = 0.0001
        parts = pipeline.split_oversized_region(audio, samplerate, 0, len(audio), samplerate * 30)
        self.assertEqual(parts[0][2], "energy_valley")
        self.assertGreaterEqual(parts[0][1], samplerate * 25)
        self.assertLessEqual(parts[0][1], samplerate * 29)
        self.assertEqual(parts[-1][1], len(audio))

    def test_energy_valley_never_extends_past_hard_maximum(self):
        samplerate = 1000
        maximum = samplerate * 30
        audio = np.ones(samplerate * 65, dtype=np.float32) * 0.02
        # This is the deepest nearby valley, but it is beyond the first hard
        # boundary and therefore must not produce an oversized ASR region.
        audio[samplerate * 34 : samplerate * 35] = 0.00001
        parts = pipeline.split_oversized_region(audio, samplerate, 0, len(audio), maximum)
        self.assertTrue(parts)
        self.assertTrue(all(end - start <= maximum for start, end, _ in parts))
        self.assertEqual(parts[0][1], maximum)

    def test_tiny_transcript_long_region_uses_local_energy(self):
        samplerate = 1000
        audio = np.zeros(samplerate * 30, dtype=np.float32)
        audio[samplerate * 12 : samplerate * 13] = 0.04
        start, end, state = pipeline.recover_tiny_timing(audio, samplerate, 0.55, "speech")
        self.assertEqual(state, "timing_energy_recovered")
        self.assertLess(end - start, 2.5)
        self.assertGreaterEqual(start, 11.5)
        self.assertLessEqual(end, 13.5)
        self.assertTrue(pipeline.is_tiny_transcript_long_region("ん", 30))
        self.assertFalse(pipeline.is_tiny_transcript_long_region("今日は仕事です", 30))

    def test_suspicion_detects_hallucination_shapes(self):
        reasons = pipeline.suspicion_reasons("やめて" * 8, 0.7, 0.2)
        self.assertIn("text_duration_ratio", reasons)
        self.assertIn("pathological_repetition", reasons)
        self.assertIn("weak_speech_conflict", reasons)

    def test_balanced_fallback_is_lexical_aware(self):
        for value in ["はい", "うん", "あ", "ん", "ははは", "うん。うん。うん。うん。うん。"]:
            reasons = pipeline.suspicion_reasons(value, 30, 0.2)
            eligible, _ = pipeline.should_retry_qwen(value, reasons + ["identical_neighbors"], "ambiguous_vocalization", 0.2)
            self.assertFalse(eligible, value)
        reasons = pipeline.suspicion_reasons("あ、啥。", 0.35, 0.8)
        self.assertIn("script_anomaly", reasons)
        self.assertTrue(pipeline.should_retry_qwen("あ、啥。", reasons, "speech", 0.8)[0])
        malformed = "言ってんだ。あず、あずして..."
        reasons = pipeline.suspicion_reasons(malformed, 3.0, 0.7)
        self.assertIn("malformed_lexical_repetition", reasons)
        self.assertTrue(pipeline.should_retry_qwen(malformed, reasons, "speech", 0.7)[0])

    def test_adn803_recorded_regressions_classify_as_expected(self):
        fixture = pathlib.Path(__file__).with_name("testdata") / "adn803_balanced_regressions.json"
        cases = json.loads(fixture.read_text(encoding="utf-8"))["cases"]
        for case in cases:
            features = pipeline.transcript_features(case["text"])
            self.assertEqual(pipeline.has_meaningful_transcript(case["text"]), case["meaningful"], case)
            self.assertEqual(features["vocalization_heavy"], case["vocalization_heavy"], case)
            reasons = pipeline.suspicion_reasons(case["text"], case["duration_seconds"], 0.5)
            eligible, _ = pipeline.should_retry_qwen(case["text"], reasons, "speech", 0.5)
            self.assertEqual(eligible, case["reazon_eligible"], case)

    def test_comparison_ignores_only_superficial_formatting(self):
        self.assertEqual(pipeline.similarity("もう、ダメ！", "もうダメ"), 1.0)
        self.assertLess(pipeline.similarity("もうダメ", "気持ちいい"), 0.5)

    def test_majority_candidate_wins_tie_break(self):
        candidates = {
            "qwen3": pipeline.Candidate("qwen3", "q", "もうダメ"),
            "reazon": pipeline.Candidate("reazon", "r", "気持ちいい"),
            "whisper": pipeline.Candidate("whisper", "w", "もう、ダメ"),
        }
        chosen, score = pipeline.choose_candidate(candidates)
        self.assertIn(chosen.engine, {"qwen3", "whisper"})
        self.assertGreater(score, 0.4)

    def test_alignment_segmentation_respects_punctuation(self):
        items = [
            {"text": "もう", "start_time": 0.1, "end_time": 0.5},
            {"text": "ダメ。", "start_time": 0.5, "end_time": 1.0},
            {"text": "やめて", "start_time": 1.2, "end_time": 1.8},
        ]
        groups = pipeline.split_alignment(items)
        self.assertEqual(len(groups), 2)
        self.assertEqual("".join(x["text"] for x in groups[0]), "もうダメ。")

    def test_normalization_anchors_come_only_from_successful_alignment(self):
        items = [
            {"text": "もう", "start_time": 0.1, "end_time": 0.5},
            {"text": "ダメ", "start_time": 0.5, "end_time": 1.0},
            {"text": "。", "start_time": 1.0, "end_time": 1.2},
        ]
        self.assertEqual(pipeline.trusted_timing_anchors(items, 10.0, "aligned"), [10500, 11000])
        self.assertEqual(pipeline.trusted_timing_anchors(items, 10.0, "timing_only"), [])
        self.assertEqual(pipeline.trusted_timing_anchors(items[:1], 10.0, "aligned"), [])

    def test_reazon_fallback_runs_in_external_interpreter(self):
        regions = [pipeline.Region(10, 20, 0.8, "speech"), pipeline.Region(30, 45, 0.7, "speech")]

        def complete_worker(command, **_kwargs):
            output = command[command.index("--output") + 1]
            with open(output, "w", encoding="utf-8") as handle:
                pipeline.json.dump({"results": [{"index": 1, "text": "  もう   ダメ  "}]}, handle)

        with mock.patch.object(pipeline.subprocess, "run", side_effect=complete_worker) as run:
            result = pipeline.reazon_batch_transcribe("/opt/reazon/bin/python", "/app/asr/reazon_batch_worker.py", "movie.wav", regions, [1], "model", "cuda")

        self.assertEqual(result, {1: "もう ダメ"})
        self.assertEqual(run.call_args.args[0][:2], ["/opt/reazon/bin/python", "/app/asr/reazon_batch_worker.py"])

    def test_qwen_phase_runs_in_external_process_and_returns_diagnostics(self):
        regions = [pipeline.Region(10, 20, 0.8, "speech")]

        def complete_worker(command, **_kwargs):
            manifest = command[command.index("--regions") + 1]
            output = command[command.index("--output") + 1]
            entries = json.loads(pathlib.Path(manifest).read_text(encoding="utf-8"))
            pathlib.Path(output).write_text(json.dumps({
                "results": [{"index": entries[0]["index"], "text": "  もう   ダメ  "}],
                "diagnostics": {"gpu_after_asr": {"used_mb": 4000}},
            }), encoding="utf-8")
            return types.SimpleNamespace(returncode=0)

        with mock.patch.object(pipeline.subprocess, "run", side_effect=complete_worker) as run:
            result, diagnostics = pipeline.qwen_worker_transcribe(
                "/opt/qwen-asr/bin/python", "/app/asr/qwen_batch_worker.py", "movie.wav",
                regions, [0], "文脈", "Qwen/Qwen3-ASR-1.7B", "revision", "cuda", 4, True,
            )

        self.assertEqual(result, {0: "もう ダメ"})
        self.assertEqual(diagnostics["gpu_after_asr"]["used_mb"], 4000)
        command = run.call_args.args[0]
        self.assertEqual(command[:2], ["/opt/qwen-asr/bin/python", "/app/asr/qwen_batch_worker.py"])
        self.assertIn("--debug", command)

    def test_profiles_are_minimal_japanese_and_aliases_are_canonical(self):
        self.assertEqual(pipeline.normalize_profile("tokusatsu"), "giga")
        self.assertEqual(pipeline.normalize_profile("AKIBA"), "giga")
        self.assertEqual(set(pipeline.PROFILE_CONTEXT), {"standard", "jav", "giga"})
        self.assertTrue(all(not any(ch.isascii() and ch.isalpha() for ch in value) for value in pipeline.PROFILE_CONTEXT.values()))

    def test_prompt_leakage_catches_old_context_and_partial_fragment(self):
        old = "Japanese tokusatsu dialogue. Preserve character, organization, attack, and transformation names; also retain shouts and incomplete speech."
        self.assertTrue(pipeline.detect_prompt_leakage(old, pipeline.PROFILE_CONTEXT["giga"])[0])
        self.assertTrue(pipeline.detect_prompt_leakage("あっ without inventing sentences", pipeline.PROFILE_CONTEXT["jav"])[0])
        self.assertTrue(pipeline.detect_prompt_leakage("inventing", pipeline.PROFILE_CONTEXT["jav"])[0])
        self.assertFalse(pipeline.detect_prompt_leakage("日本語が上手ですね", pipeline.PROFILE_CONTEXT["standard"])[0])

    def test_whisper_requires_meaningful_unresolved_lexical_suspicion(self):
        suspicious = pipeline.Candidate("qwen3", "q", "今日はもう本当にダメです", ["weak_speech_conflict"])
        clean = pipeline.Candidate("qwen3", "q", "もうダメ")
        vocalization = pipeline.Candidate("qwen3", "q", "うん", ["identical_neighbors"])
        self.assertFalse(pipeline.should_use_whisper(suspicious, "speech", 0.8, "balanced")[0])
        self.assertTrue(pipeline.should_use_whisper(suspicious, "speech", 0.5, "high_accuracy")[0])
        self.assertFalse(pipeline.should_use_whisper(clean, "speech", 0.8)[0])
        self.assertFalse(pipeline.should_use_whisper(vocalization, "ambiguous_vocalization", 0.2)[0])
        self.assertFalse(pipeline.mode_allows_whisper("fast"))
        self.assertTrue(pipeline.mode_allows_whisper("balanced"))
        self.assertTrue(pipeline.mode_allows_whisper("high_accuracy"))

    def test_qwen_retry_resolves_script_anomaly_before_whisper(self):
        original = pipeline.Candidate("qwen3", "q", "あ、啥。", ["script_anomaly"])
        retry = pipeline.Candidate("qwen_retry", "q", "あ、そう。", [])
        self.assertTrue(pipeline.qwen_retry_improves(original, retry, 1.2))
        self.assertFalse(pipeline.should_use_whisper(retry, "speech", 0.8)[0])

    def test_prompt_leak_retry_can_be_much_shorter_and_avoids_whisper(self):
        original_text = "日本語・成人向け映像。"
        retry_text = "エアコンの温度を一十五度に。"
        original_reasons = pipeline.suspicion_reasons(original_text, 2.0, 0.8, pipeline.PROFILE_CONTEXT["jav"])
        retry_reasons = pipeline.suspicion_reasons(retry_text, 2.0, 0.8, "")
        original = pipeline.Candidate("qwen3", "q", original_text, original_reasons)
        retry = pipeline.Candidate("qwen_retry", "q", retry_text, retry_reasons)
        self.assertIn("prompt_leakage", original.suspicion)
        self.assertNotIn("prompt_leakage", retry.suspicion)
        self.assertTrue(pipeline.qwen_retry_improves(original, retry, 2.0))
        self.assertNotEqual(pipeline.suspicion_category(retry.text, retry.suspicion), "META")
        leaked = pipeline.Candidate("qwen3", "q", original.text, ["prompt_leakage"])
        self.assertFalse(pipeline.should_use_whisper(leaked, "speech", 0.8, "balanced")[0])

    def test_prompt_leak_retry_state_is_clean_in_pipeline_output(self):
        result, _ = self.run_synthetic_pipeline(
            "日本語・成人向け映像。",
            RuntimeError("alignment failed"),
            mode="balanced",
            retry_transcript="エアコンの温度を一十五度に。",
        )
        self.assertEqual(result["metrics"]["prompt_leakage_detected"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_attempted"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_clean"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_selected"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_escalated_to_whisper"], 0)
        self.assertEqual(result["segments"][0]["selected_engine"], "qwen_retry")
        self.assertNotEqual(result["segments"][0]["suspicion_category"], "META")
        self.assertTrue(result["metrics"]["qwen_model_deleted"])
        self.assertIn("gpu_used_mb_after_qwen_cleanup", result["metrics"])
        self.assertIn("gpu_used_mb_before_aligner", result["metrics"])

    def test_retry_is_low_evidence_vocalization_requires_tiny_token_and_weak_evidence(self):
        tiny_tokens = ["はい", "うん", "あ", "え", "ん"]
        for token in tiny_tokens:
            retry = pipeline.Candidate("qwen_retry", "q", token, [])
            with self.subTest(token=token, evidence="ambiguous_classification"):
                self.assertTrue(pipeline.retry_is_low_evidence_vocalization(retry, 0.8, "ambiguous_vocalization"))
            with self.subTest(token=token, evidence="weak_speech_probability"):
                self.assertTrue(pipeline.retry_is_low_evidence_vocalization(retry, 0.1, "speech"))
            with self.subTest(token=token, evidence="strong"):
                # Real timing/audio confidence (clear speech classification,
                # high VAD speech probability) must let the vocalization be
                # preserved rather than treated as ambiguous.
                self.assertFalse(pipeline.retry_is_low_evidence_vocalization(retry, 0.8, "speech"))
        # A genuine short utterance recovered from a leaked prompt is not a
        # tiny generic vocalization and must never be classified this way,
        # regardless of how weak the surrounding evidence looks.
        substantive = pipeline.Candidate("qwen_retry", "q", "エアコンの温度を一十五度に。", [])
        self.assertFalse(pipeline.retry_is_low_evidence_vocalization(substantive, 0.1, "ambiguous_vocalization"))

    def test_qwen_retry_improves_withholds_recovery_for_weak_evidence_vocalization(self):
        original_text = "日本語・成人向け映像。"
        original_reasons = pipeline.suspicion_reasons(original_text, 2.0, 0.8, pipeline.PROFILE_CONTEXT["jav"])
        original = pipeline.Candidate("qwen3", "q", original_text, original_reasons)
        self.assertIn("prompt_leakage", original.suspicion)

        retry_reasons = pipeline.suspicion_reasons("あ", 2.0, 0.1, "")
        retry = pipeline.Candidate("qwen_retry", "q", "あ", retry_reasons)
        self.assertNotIn("prompt_leakage", retry.suspicion)

        # Weak/ambiguous evidence: do not automatically accept the retry as
        # recovered.
        self.assertFalse(
            pipeline.qwen_retry_improves(
                original, retry, 2.0, speech_probability=0.1, classification="ambiguous_vocalization"
            )
        )
        # The same tiny vocalization with strong timing/audio confidence
        # behind it (clear speech classification, high speech probability)
        # is still preserved as a recovered retry.
        self.assertTrue(
            pipeline.qwen_retry_improves(
                original, retry, 2.0, speech_probability=0.9, classification="speech"
            )
        )
        # Calling with no speech_probability/classification at all (the
        # original call shape) keeps prior behavior: recovery is accepted.
        self.assertTrue(pipeline.qwen_retry_improves(original, retry, 2.0))

    def run_synthetic_pipeline_sequence(self, entries, mode="balanced", region_seconds=3.0):
        """Like run_synthetic_pipeline, but lays out several sequential VAD
        regions (one per entry) instead of a single region, so local
        repetition across nearby prompt-leak retries can be exercised
        end-to-end. Each entry is a dict with keys: transcript,
        retry_transcript (optional), speech_probability, classification
        (optional, defaults to "speech")."""
        samplerate = 1000
        region_samples = int(region_seconds * samplerate)
        audio = np.zeros(region_samples * len(entries), dtype=np.float32)
        context = pipeline.PROFILE_CONTEXT["jav"]
        with tempfile.TemporaryDirectory() as directory:
            input_path = pathlib.Path(directory) / "input.wav"
            output_path = pathlib.Path(directory) / "output.json"
            fake_soundfile = types.SimpleNamespace(read=lambda *_args, **_kwargs: (audio, samplerate))
            aligner = mock.Mock()
            aligner.align.side_effect = RuntimeError("alignment failed")
            regions = []
            primary_texts = {}
            retry_results = []
            retry_reasons = {}
            for index, entry in enumerate(entries):
                start = index * region_samples
                end = start + region_samples
                classification = entry.get("classification", "speech")
                regions.append(pipeline.Region(start, end, entry["speech_probability"], classification))
                primary_texts[index] = entry["transcript"]
                reasons = pipeline.suspicion_reasons(
                    entry["transcript"], region_seconds, entry["speech_probability"], context
                )
                eligible, retry_reason = pipeline.should_retry_qwen(
                    entry["transcript"], reasons, classification, entry["speech_probability"]
                )
                wants_retry = "prompt_leakage" in reasons or (mode != "fast" and eligible)
                if wants_retry:
                    retry_value = entry.get("retry_transcript", entry["transcript"])
                    retry_results.append({"index": index, "text": retry_value})
                    retry_reasons[str(index)] = (
                        "prompt_leakage_unresolved" if "prompt_leakage" in reasons else retry_reason
                    )
            arguments = [
                "qwen_pipeline.py", "--input", str(input_path), "--output", str(output_path),
                "--device", "cpu", "--mode", mode, "--disable-reazon", "--disable-whisper", "--debug",
            ]
            with (
                mock.patch.object(sys, "argv", arguments),
                mock.patch.object(pipeline, "detect_regions", return_value=regions),
                mock.patch.object(
                    pipeline, "qwen_worker_transcribe",
                    return_value=(primary_texts, {"retry_results": retry_results, "retry_reasons": retry_reasons}),
                ),
                mock.patch.object(pipeline, "load_aligner", return_value=aligner),
                mock.patch.object(
                    pipeline, "release_cuda",
                    return_value={"qwen_gc_collected": 0, "cuda_cache_cleared": False, "cuda_ipc_collected": False},
                ),
                mock.patch.object(pipeline, "gpu_snapshot", return_value={"available": False}),
                mock.patch.object(pipeline, "progress"),
                mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}),
            ):
                self.assertEqual(pipeline.main(), 0)
            return json.loads(output_path.read_text(encoding="utf-8")), aligner

    def test_case_d_direct_vocalization_without_prompt_leakage_is_unaffected(self):
        # PRIORITY 6/9 CASE D: a direct Qwen recognition of a tiny
        # vocalization with no prompt leakage must never enter this
        # suppression path.
        result, _ = self.run_synthetic_pipeline(
            "はい。", RuntimeError("alignment failed"), mode="balanced",
            speech_probability=0.65, classification="speech",
        )
        self.assertEqual(result["metrics"]["qwen_retry_attempted"], 0)
        self.assertEqual(result["segments"][0]["text"], "はい。")
        self.assertEqual(result["segments"][0]["selected_engine"], "qwen3")

    def test_ambiguous_vocalization_cases_a_through_h(self):
        leaked_original = pipeline.Candidate("qwen3", "q", "日本語・成人向け映像。", ["prompt_leakage"])
        leaked_original_with_neighbors = pipeline.Candidate(
            "qwen3", "q", "日本語・成人向け映像。", ["prompt_leakage", "identical_neighbors"]
        )

        # CASE A: weak speech probability plus a weak/ambiguous VAD
        # classification (the closest pre-alignment proxy available for
        # "poor timing evidence" -- real alignment state does not exist yet
        # at retry-decision time) -> reject.
        retry_a = pipeline.Candidate("qwen_retry", "q", "はい。", [])
        self.assertTrue(pipeline.retry_is_low_evidence_vocalization(
            retry_a, 0.37, "ambiguous_vocalization", leaked_original,
            region_duration=3.0, local_repetition_count=0,
        ))

        # CASE B: moderate speech probability with a clean "speech"
        # classification, but the same token repeats nearby -> reject.
        retry_b = pipeline.Candidate("qwen_retry", "q", "はい。", [])
        self.assertTrue(pipeline.retry_is_low_evidence_vocalization(
            retry_b, 0.42, "speech", leaked_original,
            region_duration=3.0, local_repetition_count=2,
        ))

        # CASE C: strong speech probability, clean speech classification,
        # isolated occurrence -> preserve.
        retry_c = pipeline.Candidate("qwen_retry", "q", "はい。", [])
        self.assertFalse(pipeline.retry_is_low_evidence_vocalization(
            retry_c, 0.82, "speech", leaked_original,
            region_duration=3.0, local_repetition_count=0,
        ))

        # CASE E: substantive prompt-leak retry text is not a tiny
        # vocalization at all -- this suppression path never applies, no
        # matter how weak the surrounding evidence looks, and normal
        # recovery still proceeds.
        substantive = pipeline.Candidate("qwen_retry", "q", "エアコンの温度を十五度に。", [])
        self.assertFalse(pipeline.retry_is_low_evidence_vocalization(
            substantive, 0.1, "ambiguous_vocalization", leaked_original,
            region_duration=3.0, local_repetition_count=5,
        ))
        self.assertTrue(pipeline.qwen_retry_improves(leaked_original, substantive, 2.0))

        # CASE F: うん, weak/ambiguous evidence plus repetition -> reject.
        retry_f = pipeline.Candidate("qwen_retry", "q", "うん", [])
        self.assertTrue(pipeline.retry_is_low_evidence_vocalization(
            retry_f, 0.40, "speech", leaked_original,
            region_duration=3.0, local_repetition_count=3,
        ))

        # CASE G: あ, strong speech evidence, isolated -> preserve.
        retry_g = pipeline.Candidate("qwen_retry", "q", "あ", [])
        self.assertFalse(pipeline.retry_is_low_evidence_vocalization(
            retry_g, 0.9, "speech", leaked_original,
            region_duration=3.0, local_repetition_count=0,
        ))

        # CASE H: the retry token is not in the generic vocalization set --
        # this suppression path must not apply regardless of how weak the
        # evidence looks.
        retry_h = pipeline.Candidate("qwen_retry", "q", "多分そう思う", [])
        self.assertFalse(pipeline.retry_is_low_evidence_vocalization(
            retry_h, 0.1, "ambiguous_vocalization", leaked_original,
            region_duration=3.0, local_repetition_count=5,
        ))

        # identical_neighbors on the original is one more signal that can
        # combine with a moderate probability to cross the threshold.
        retry_neighbors = pipeline.Candidate("qwen_retry", "q", "ん", [])
        self.assertTrue(pipeline.retry_is_low_evidence_vocalization(
            retry_neighbors, 0.40, "speech", leaked_original_with_neighbors,
            region_duration=3.0, local_repetition_count=1,
        ))

    def test_local_prompt_leak_vocalization_repetition_counts(self):
        # Three "はい" entries close together (by position and by time)
        # should see each other; a lone "うん" and a distant "はい" should not.
        candidates = [
            (0, "はい", 0.0, 1.0),
            (1, "はい", 3.0, 4.0),
            (2, "うん", 6.0, 7.0),
            (3, "はい", 9.0, 10.0),
            (10, "はい", 200.0, 201.0),
        ]
        counts = pipeline.local_prompt_leak_vocalization_repetition_counts(
            candidates, window_segments=3, window_seconds=20.0
        )
        self.assertEqual(counts[0], 2)
        self.assertEqual(counts[1], 2)
        self.assertEqual(counts[2], 0)
        self.assertEqual(counts[3], 2)
        self.assertEqual(counts[10], 0)

    def test_prompt_leak_vocalization_repetition_regression_sequence(self):
        # PRIORITY 10: a synthetic sequence resembling the ADN-803 pattern --
        # several prompt-leak retries in a row all resolving to the same
        # weak-evidence "はい", interleaved with real lexical speech and one
        # clearly strong, isolated "はい".
        weak = {"transcript": "日本語・成人向け映像。", "retry_transcript": "はい。"}
        entries = [
            {**weak, "speech_probability": 0.40},
            {**weak, "speech_probability": 0.38},
            {**weak, "speech_probability": 0.42},
            {**weak, "speech_probability": 0.36},
            {
                "transcript": "今日は少し遅くなりました",
                "speech_probability": 0.75,
            },
            {**weak, "speech_probability": 0.44},
            {**weak, "speech_probability": 0.39},
            {
                "transcript": "日本語・成人向け映像。",
                "retry_transcript": "はい。",
                "speech_probability": 0.88,
            },
        ]
        result, _ = self.run_synthetic_pipeline_sequence(entries, mode="balanced")

        texts = [segment["text"] for segment in result["segments"]]
        self.assertIn("今日は少し遅くなりました", texts)
        self.assertEqual(texts.count("はい。"), 1, texts)

        metrics = result["metrics"]
        self.assertGreaterEqual(metrics["prompt_leakage_retry_vocalization_repetition_rejected"], 5)
        self.assertGreaterEqual(metrics["prompt_leakage_retry_vocalization_strong_evidence_preserved"], 1)
        self.assertEqual(metrics["prompt_leakage_unresolved"], 0)
        # PRIORITY 7: none of this ever escalates to Whisper in Balanced.
        self.assertEqual(metrics["prompt_leakage_escalated_to_whisper"], 0)
        self.assertEqual(metrics["whisper_candidates"], 0)

    def test_ambiguous_vocalization_prompt_leak_retry_prefers_no_subtitle(self):
        result, _ = self.run_synthetic_pipeline(
            "日本語・成人向け映像。",
            RuntimeError("alignment failed"),
            mode="balanced",
            retry_transcript="あ",
            speech_probability=0.1,
            classification="ambiguous_vocalization",
        )
        self.assertEqual(result["metrics"]["prompt_leakage_detected"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_attempted"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_selected"], 0)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_ambiguous_vocalization"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_escalated_to_whisper"], 0)
        # No usable evidence either way: prefer silence over surfacing the
        # leaked prompt text or an unsupported guess.
        self.assertEqual(result["segments"], [])

    def test_strong_evidence_vocalization_prompt_leak_retry_is_still_preserved(self):
        result, _ = self.run_synthetic_pipeline(
            "日本語・成人向け映像。",
            RuntimeError("alignment failed"),
            mode="balanced",
            retry_transcript="あ",
            speech_probability=0.9,
            classification="speech",
        )
        self.assertEqual(result["metrics"]["prompt_leakage_retry_selected"], 1)
        self.assertEqual(result["metrics"]["prompt_leakage_retry_ambiguous_vocalization"], 0)
        self.assertEqual(result["segments"][0]["selected_engine"], "qwen_retry")
        self.assertEqual(result["segments"][0]["text"], "あ")

    def test_malformed_retry_remains_balanced_whisper_eligible(self):
        retry = pipeline.Candidate(
            "qwen_retry", "q", "まあそうだ...までまで俺には...", ["malformed_lexical_repetition"]
        )
        eligible, reason = pipeline.should_use_whisper(retry, "speech", 0.7, "balanced", 3.0)
        self.assertTrue(eligible)
        self.assertEqual(reason, "malformed_lexical_repetition")

    def test_high_accuracy_is_broader_but_vocalizations_stay_excluded(self):
        lexical = pipeline.Candidate("qwen3", "q", "今日は少し遅くなりました", [])
        self.assertFalse(pipeline.should_use_whisper(lexical, "speech", 0.45, "balanced", 4.0)[0])
        eligible, reason = pipeline.should_use_whisper(lexical, "speech", 0.45, "high_accuracy", 4.0)
        self.assertTrue(eligible)
        self.assertEqual(reason, "medium_confidence_verification")
        vocal = pipeline.Candidate("qwen3", "q", "あっ", ["weak_speech_conflict"])
        self.assertFalse(pipeline.should_use_whisper(vocal, "speech", 0.45, "high_accuracy", 12.0)[0])

    def test_fast_never_runs_normal_retry_or_fallback_for_script_anomaly(self):
        result, _ = self.run_synthetic_pipeline("あ、啥。", RuntimeError("alignment failed"), mode="fast")
        self.assertEqual(result["metrics"]["qwen_retry_attempted"], 0)
        self.assertEqual(result["metrics"]["whisper_candidates"], 0)
        self.assertNotIn("reazon_candidates", result["metrics"])

    def test_balanced_attempts_targeted_qwen_retry_for_script_anomaly(self):
        result, _ = self.run_synthetic_pipeline("あ、啥。", RuntimeError("alignment failed"), mode="balanced")
        self.assertEqual(result["metrics"]["qwen_retry_attempted"], 1)
        self.assertEqual(result["metrics"]["qwen_retry_unresolved"], 1)
        self.assertNotIn("reazon_candidates", result["metrics"])

    def test_whisper_only_wins_when_it_clearly_improves_original_issue(self):
        original = pipeline.Candidate("qwen3", "q", "言ってんだ。あず、あずして。", ["malformed_lexical_repetition"])
        whisper = pipeline.Candidate("whisper", "w", "言ってるんだ。外して。", [])
        chosen, _, corrected, rejection = pipeline.choose_balanced_candidate(original, original, whisper, 3.0)
        self.assertEqual(chosen.engine, "whisper")
        self.assertTrue(corrected)
        self.assertEqual(rejection, "")

    def test_vocabulary_support_scores_but_never_replaces_text(self):
        supported = pipeline.Candidate("whisper", "w", "ムーンエンジェルが来た", [])
        other = pipeline.Candidate("qwen3", "q", "誰かが来た", [])
        terms = {"ムーンエンジェル", "スパンデクサー", "テラリウム鉱石", "コントラ"}
        self.assertGreater(pipeline.candidate_score(supported, 2.0, terms), pipeline.candidate_score(other, 2.0, terms))
        self.assertEqual(supported.text, "ムーンエンジェルが来た")

    def test_whisper_splits_188780ms_into_one_multi_file_model_invocation(self):
        samplerate = 1000
        audio = np.ones(188780, dtype=np.float32) * 0.02
        written_lengths = []

        def write_audio(path, child, _rate, subtype=None):
            self.assertEqual(subtype, "PCM_16")
            written_lengths.append(len(child))
            pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128)

        def audio_info(path):
            child_length = written_lengths[int(pathlib.Path(path).stem.split("-")[-1])]
            return types.SimpleNamespace(
                frames=child_length, channels=1, samplerate=samplerate, subtype="PCM_16",
                duration=child_length / samplerate
            )

        def complete_whisper(command, **_kwargs):
            prefixes = [command[index + 1] for index, value in enumerate(command) if value == "-of"]
            for prefix in prefixes:
                pathlib.Path(prefix + ".json").write_text(
                    json.dumps({"transcription": [{"text": "テスト"}]}), encoding="utf-8"
                )
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")

        fake_soundfile = types.SimpleNamespace(write=write_audio, info=audio_info)
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            with (
                mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}),
                mock.patch.object(pipeline, "gpu_snapshot", return_value={"free_mb": 5000}),
                mock.patch.object(pipeline.subprocess, "run", side_effect=complete_whisper) as run,
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), [(audio, samplerate)], [0], "ja", "cuda", 30,
                    threads=12, beam_size=3, best_of=2,
                )
        self.assertTrue(written_lengths)
        self.assertTrue(all(length <= 30000 for length in written_lengths))
        self.assertGreater(len(written_lengths), 1)
        self.assertEqual(run.call_count, 1)
        self.assertNotIn("-ng", run.call_args.args[0])
        self.assertIn("-ojf", run.call_args.args[0])
        self.assertEqual(run.call_args.args[0][run.call_args.args[0].index("-l") + 1], "ja")
        self.assertNotIn("-tr", run.call_args.args[0])
        self.assertEqual(run.call_args.args[0][run.call_args.args[0].index("-t") + 1], "12")
        self.assertEqual(run.call_args.args[0][run.call_args.args[0].index("-bs") + 1], "3")
        self.assertEqual(run.call_args.args[0][run.call_args.args[0].index("-bo") + 1], "2")
        self.assertEqual(run.call_args.args[0].count("-f"), len(written_lengths))
        self.assertEqual(run.call_args.args[0].count("-of"), len(written_lengths))
        self.assertTrue(result.success)
        self.assertEqual(result.exit_code, 0)
        self.assertEqual(result.texts[0], "テスト" * len(written_lengths))

    def test_whisper_missing_model_is_explicit_without_launch(self):
        with mock.patch.object(pipeline.subprocess, "run") as run:
            result = pipeline.whisper_batch_transcribe(
                "whisper-cli", "/missing/ggml-large-v3.bin", [(np.ones(1000, dtype=np.float32), 1000)], [0], "ja"
            )
        self.assertFalse(result.success)
        self.assertEqual(result.failure_reason, "whisper_model_missing")
        self.assertEqual(result.candidate_errors[0], "whisper_model_missing")
        run.assert_not_called()

    def test_whisper_nonzero_exit_preserves_bounded_diagnostics(self):
        audio = np.ones(1000, dtype=np.float32) * 0.02
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            fake_soundfile = types.SimpleNamespace(
                write=lambda path, *_args, **_kwargs: pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128),
                info=lambda _path: types.SimpleNamespace(
                    frames=1000, channels=1, samplerate=1000, subtype="PCM_16", duration=1.0
                ),
            )
            completed = types.SimpleNamespace(returncode=3, stdout="", stderr="CUDA initialization failure " * 1000)
            with mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}), mock.patch.object(
                pipeline.subprocess, "run", return_value=completed
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), [(audio, 1000)], [0], "ja", "cuda", 30, False
                )
        self.assertFalse(result.success)
        self.assertEqual(result.exit_code, 3)
        self.assertEqual(result.failure_reason, "whisper_cuda_initialization_failed")
        self.assertEqual(result.candidate_errors[0], "whisper_batch_failed")
        self.assertLessEqual(len(result.stderr), 8192)

    def test_whisper_cuda_oom_retries_whole_batch_once_on_cpu(self):
        audio = np.ones(1000, dtype=np.float32) * 0.02
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            fake_soundfile = types.SimpleNamespace(
                write=lambda path, *_args, **_kwargs: pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128),
                info=lambda _path: types.SimpleNamespace(
                    frames=1000, channels=1, samplerate=1000, subtype="PCM_16", duration=1.0
                ),
            )

            def launch(command, **_kwargs):
                if "-ng" not in command:
                    return types.SimpleNamespace(
                        returncode=-6, stdout="", stderr="cudaMalloc failed: out of memory"
                    )
                prefix = command[command.index("-of") + 1]
                pathlib.Path(prefix + ".json").write_text(
                    json.dumps({"transcription": [{"text": "これはテストです"}]}), encoding="utf-8"
                )
                return types.SimpleNamespace(returncode=0, stdout="", stderr="")

            with (
                mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}),
                mock.patch.object(pipeline, "gpu_snapshot", return_value={"free_mb": 5000}),
                mock.patch.object(pipeline.subprocess, "run", side_effect=launch) as run,
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), [(audio, 1000)], [0], "ja", "cuda", 30, True, 900
                )
        self.assertEqual(run.call_count, 2)
        self.assertTrue(result.success)
        self.assertTrue(result.cuda_attempted)
        self.assertTrue(result.cuda_failed)
        self.assertEqual(result.cuda_failure_reason, "whisper_cuda_oom")
        self.assertTrue(result.cpu_fallback_attempted)
        self.assertTrue(result.cpu_fallback_succeeded)
        self.assertEqual(result.execution_device, "cpu")
        self.assertEqual(result.texts[0], "これはテストです")
        self.assertNotIn(0, result.candidate_errors)

    def test_whisper_low_vram_preflight_skips_known_oom_and_uses_cpu(self):
        audio = np.ones(1000, dtype=np.float32) * 0.02
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            fake_soundfile = types.SimpleNamespace(
                write=lambda path, *_args, **_kwargs: pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128),
                info=lambda _path: types.SimpleNamespace(
                    frames=1000, channels=1, samplerate=1000, subtype="PCM_16", duration=1.0
                ),
            )

            def complete_cpu(command, **_kwargs):
                self.assertIn("-ng", command)
                prefix = command[command.index("-of") + 1]
                pathlib.Path(prefix + ".json").write_text(
                    json.dumps({"transcription": [{"text": "これはテストです"}]}), encoding="utf-8"
                )
                return types.SimpleNamespace(returncode=0, stdout="", stderr="")

            with (
                mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}),
                mock.patch.object(pipeline, "gpu_snapshot", return_value={"free_mb": 2700}),
                mock.patch.object(pipeline.subprocess, "run", side_effect=complete_cpu) as run,
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), [(audio, 1000)], [0], "ja", "cuda", 30, True, 900,
                    cuda_safe_minimum_mb=4096,
                )

        self.assertEqual(run.call_count, 1)
        self.assertTrue(result.success)
        self.assertTrue(result.cuda_preflight_skipped)
        self.assertEqual(result.cuda_preflight_free_mb, 2700)
        self.assertFalse(result.cuda_attempted)
        self.assertTrue(result.cpu_fallback_succeeded)
        self.assertEqual(result.execution_device, "cpu")

    def test_whisper_does_not_cpu_retry_non_cuda_failure(self):
        audio = np.ones(1000, dtype=np.float32) * 0.02
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            fake_soundfile = types.SimpleNamespace(
                write=lambda path, *_args, **_kwargs: pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128),
                info=lambda _path: types.SimpleNamespace(
                    frames=1000, channels=1, samplerate=1000, subtype="PCM_16", duration=1.0
                ),
            )
            completed = types.SimpleNamespace(returncode=2, stdout="", stderr="unknown command-line option")
            with (
                mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}),
                mock.patch.object(pipeline, "gpu_snapshot", return_value={"free_mb": 5000}),
                mock.patch.object(pipeline.subprocess, "run", return_value=completed) as run,
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), [(audio, 1000)], [0], "ja", "cuda", 30, True, 900
                )
        self.assertEqual(run.call_count, 1)
        self.assertFalse(result.cpu_fallback_attempted)
        self.assertEqual(result.failure_reason, "whisper_execution_failed")
        self.assertEqual(result.candidate_errors[0], "whisper_batch_failed")

    def test_invalid_whisper_wav_rejects_only_its_candidate(self):
        clips = [(np.zeros(1000, dtype=np.float32), 1000), (np.ones(1000, dtype=np.float32) * 0.02, 1000)]
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            fake_soundfile = types.SimpleNamespace(
                write=lambda path, *_args, **_kwargs: pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128),
                info=lambda _path: types.SimpleNamespace(
                    frames=1000, channels=1, samplerate=1000, subtype="PCM_16", duration=1.0
                ),
            )

            def complete_whisper(command, **_kwargs):
                prefix = command[command.index("-of") + 1]
                pathlib.Path(prefix + ".json").write_text(
                    json.dumps({"transcription": [{"text": "これはテストです"}]}), encoding="utf-8"
                )
                return types.SimpleNamespace(returncode=0, stdout="", stderr="")

            with mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}), mock.patch.object(
                pipeline.subprocess, "run", side_effect=complete_whisper
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), clips, [0, 1], "ja", "cpu"
                )
        self.assertTrue(result.success)
        self.assertEqual(result.candidate_errors[0], "whisper_invalid_audio")
        self.assertNotIn(1, result.candidate_errors)
        self.assertEqual(result.texts[1], "これはテストです")

    def test_successful_process_with_missing_json_is_parse_failure_not_empty(self):
        audio = np.ones(1000, dtype=np.float32) * 0.02
        with tempfile.TemporaryDirectory() as directory:
            model = pathlib.Path(directory) / "large-v3.bin"
            model.write_bytes(b"model")
            fake_soundfile = types.SimpleNamespace(
                write=lambda path, *_args, **_kwargs: pathlib.Path(path).write_bytes(b"RIFF" + b"0" * 128),
                info=lambda _path: types.SimpleNamespace(
                    frames=1000, channels=1, samplerate=1000, subtype="PCM_16", duration=1.0
                ),
            )
            completed = types.SimpleNamespace(returncode=0, stdout="", stderr="")
            with mock.patch.dict(sys.modules, {"soundfile": fake_soundfile}), mock.patch.object(
                pipeline.subprocess, "run", return_value=completed
            ):
                result = pipeline.whisper_batch_transcribe(
                    "whisper-cli", str(model), [(audio, 1000)], [0], "ja"
                )
        self.assertTrue(result.success)
        self.assertEqual(result.candidate_errors[0], "whisper_output_parse_failed")
        self.assertEqual(result.texts[0], "")

    @unittest.skipUnless(
        os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_WAV") and os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_MODEL"),
        "set Whisper smoke WAV/model paths to run the real wrapper integration test",
    )
    def test_real_whisper_wrapper_smoke(self):
        import soundfile as sf

        paths = [os.environ["JAVBEACONSUBS_WHISPER_SMOKE_WAV"]]
        if os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_WAV_2"):
            paths.append(os.environ["JAVBEACONSUBS_WHISPER_SMOKE_WAV_2"])
        clips = []
        for wav in paths:
            waveform, samplerate = sf.read(wav, dtype="float32", always_2d=False)
            if waveform.ndim > 1:
                waveform = waveform.mean(axis=1)
            clips.append((waveform, samplerate))
        result = pipeline.whisper_batch_transcribe(
            os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_BINARY", "whisper-cli"),
            os.environ["JAVBEACONSUBS_WHISPER_SMOKE_MODEL"],
            clips,
            list(range(len(clips))),
            "ja",
            os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_DEVICE", "cuda"),
        )
        print(json.dumps({
            "whisper_process_success": result.success,
            "whisper_execution_device": result.execution_device,
            "whisper_cuda_failure": result.cuda_failure_reason,
            "whisper_process_duration_seconds": result.duration_seconds,
        }))
        self.assertTrue(result.success, result.stderr)
        self.assertEqual(result.exit_code, 0)
        for index, (waveform, samplerate) in enumerate(clips):
            self.assertTrue(pipeline.has_meaningful_transcript(result.texts.get(index, "")))
            candidate = pipeline.Candidate("whisper", "large-v3", result.texts[index])
            self.assertTrue(
                pipeline.validate_whisper_candidate(candidate, candidate, len(waveform) / samplerate)[0]
            )

    @unittest.skipUnless(
        os.getenv("JAVBEACONSUBS_ISOLATED_QWEN_SMOKE") == "1"
        and os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_WAV")
        and os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_MODEL"),
        "set isolated-Qwen smoke flag plus Whisper WAV/model paths to run the process-boundary test",
    )
    def test_isolated_qwen_then_whisper_smoke(self):
        import soundfile as sf

        source = os.environ["JAVBEACONSUBS_WHISPER_SMOKE_WAV"]
        waveform, samplerate = sf.read(source, dtype="float32", always_2d=False)
        if waveform.ndim > 1:
            waveform = waveform.mean(axis=1)
        sample_count = min(len(waveform), samplerate * 30)
        region = pipeline.Region(0, sample_count, 0.8, "speech")
        before = pipeline.gpu_snapshot()
        texts, worker = pipeline.qwen_worker_transcribe(
            sys.executable, str(MODULE_PATH.with_name("qwen_batch_worker.py")), source,
            [region], [0], pipeline.PROFILE_CONTEXT["jav"], "Qwen/Qwen3-ASR-1.7B",
            "7278e1e70fe206f11671096ffdd38061171dd6e5", "cuda", 1, True,
        )
        after_worker_exit = pipeline.gpu_snapshot()
        result = pipeline.whisper_batch_transcribe(
            os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_BINARY", "whisper-cli"),
            os.environ["JAVBEACONSUBS_WHISPER_SMOKE_MODEL"],
            [(waveform[:sample_count], samplerate)], [0], "ja", "cuda", 30, True, 7200,
        )
        print(json.dumps({
            "gpu_before_qwen": before,
            "gpu_after_qwen_worker_exit": after_worker_exit,
            "qwen_worker": worker,
            "qwen_nonempty": pipeline.has_meaningful_transcript(texts.get(0, "")),
            "whisper_cuda_failure": result.cuda_failure_reason,
            "whisper_execution_device": result.execution_device,
            "whisper_seconds": result.duration_seconds,
        }))
        self.assertTrue(result.success, result.stderr or result.cuda_stderr)
        self.assertEqual(result.execution_device, "cuda")

    @unittest.skipUnless(
        os.getenv("JAVBEACONSUBS_POST_QWEN_SMOKE") == "1"
        and os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_WAV")
        and os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_MODEL"),
        "set post-Qwen smoke flag plus Whisper WAV/model paths to run the GPU lifecycle test",
    )
    def test_post_qwen_cleanup_whisper_smoke(self):
        import soundfile as sf

        waveform, samplerate = sf.read(
            os.environ["JAVBEACONSUBS_WHISPER_SMOKE_WAV"], dtype="float32", always_2d=False
        )
        if waveform.ndim > 1:
            waveform = waveform.mean(axis=1)
        clip = (waveform[: samplerate * 30], samplerate)
        before = pipeline.gpu_snapshot()
        qwen = pipeline.load_qwen(
            "Qwen/Qwen3-ASR-1.7B", "7278e1e70fe206f11671096ffdd38061171dd6e5", "cuda", 1
        )
        pipeline.qwen_transcribe(qwen, [clip], pipeline.PROFILE_CONTEXT["jav"], 1)
        cleanup = pipeline.dispose_qwen(qwen)
        del qwen
        after_cleanup = pipeline.gpu_snapshot()
        result = pipeline.whisper_batch_transcribe(
            os.getenv("JAVBEACONSUBS_WHISPER_SMOKE_BINARY", "whisper-cli"),
            os.environ["JAVBEACONSUBS_WHISPER_SMOKE_MODEL"],
            [clip], [0], "ja", "cuda", 30, True, 7200,
        )
        print(json.dumps({
            "gpu_used_mb_before_qwen": before.get("used_mb"),
            "gpu_used_mb_after_qwen_cleanup": after_cleanup.get("used_mb"),
            "cleanup": cleanup,
            "whisper_cuda_failure": result.cuda_failure_reason,
            "whisper_execution_device": result.execution_device,
        }))
        self.assertTrue(result.success, result.stderr or result.cuda_stderr)
        self.assertTrue(pipeline.has_meaningful_transcript(result.texts.get(0, "")))

    def test_fallback_audio_stats_preserve_sample_offsets(self):
        audio = np.array([0.0, 0.25, -0.5, 0.0], dtype=np.float32)
        region = pipeline.Region(16000, 48000, 0.8, "speech")
        stats = pipeline.fallback_audio_stats(audio, 16000, region)
        self.assertEqual(stats["fallback_audio_source_start_ms"], 1000)
        self.assertEqual(stats["fallback_audio_source_end_ms"], 3000)
        self.assertEqual(stats["fallback_audio_nonzero_percentage"], 50.0)
        self.assertEqual(stats["fallback_audio_peak"], 0.5)

    def test_alignment_integrity_preserves_real_regression_cases(self):
        damaged = [
            ("できてるといいな", "できてるとな"),
            ("もう一年も経ってるし", "もう一年も経っし"),
            ("またダメだったそっか", "ダメたそっか"),
            ("おやすみなさいおやすみ", "やすみなさいおやすみ"),
        ]
        for canonical, aligned in damaged:
            self.assertFalse(pipeline.alignment_integrity(canonical, aligned)["valid"], (canonical, aligned))
        self.assertTrue(pipeline.alignment_integrity("もう、ダメ！", "もうダメ")["valid"])


if __name__ == "__main__":
    unittest.main()
