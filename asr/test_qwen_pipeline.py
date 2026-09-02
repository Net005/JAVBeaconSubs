import importlib.util
import json
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
    def run_synthetic_pipeline(self, transcript, align_side_effect=None):
        samplerate = 1000
        audio = np.zeros(samplerate * 30, dtype=np.float32)
        audio[samplerate * 12 : samplerate * 13] = 0.04
        with tempfile.TemporaryDirectory() as directory:
            input_path = pathlib.Path(directory) / "input.wav"
            output_path = pathlib.Path(directory) / "output.json"
            fake_soundfile = types.SimpleNamespace(read=lambda *_args, **_kwargs: (audio, samplerate))
            aligner = mock.Mock()
            aligner.align.side_effect = align_side_effect
            arguments = [
                "qwen_pipeline.py", "--input", str(input_path), "--output", str(output_path),
                "--device", "cpu", "--disable-reazon", "--disable-whisper", "--debug",
            ]
            with (
                mock.patch.object(sys, "argv", arguments),
                mock.patch.object(pipeline, "detect_regions", return_value=[pipeline.Region(0, len(audio), 0.55, "speech")]),
                mock.patch.object(pipeline, "load_qwen", return_value=object()),
                mock.patch.object(pipeline, "qwen_transcribe", return_value=[transcript]),
                mock.patch.object(pipeline, "load_aligner", return_value=aligner),
                mock.patch.object(pipeline, "release_cuda"),
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
            eligible, _ = pipeline.should_use_reazon_fallback(value, reasons + ["identical_neighbors"], "ambiguous_vocalization", 0.2)
            self.assertFalse(eligible, value)
        reasons = pipeline.suspicion_reasons("あ、啥。", 0.35, 0.8)
        self.assertIn("script_anomaly", reasons)
        self.assertTrue(pipeline.should_use_reazon_fallback("あ、啥。", reasons, "speech", 0.8)[0])
        malformed = "言ってんだ。あず、あずして..."
        reasons = pipeline.suspicion_reasons(malformed, 3.0, 0.7)
        self.assertIn("malformed_lexical_repetition", reasons)
        self.assertTrue(pipeline.should_use_reazon_fallback(malformed, reasons, "speech", 0.7)[0])

    def test_adn803_recorded_regressions_classify_as_expected(self):
        fixture = pathlib.Path(__file__).with_name("testdata") / "adn803_balanced_regressions.json"
        cases = json.loads(fixture.read_text(encoding="utf-8"))["cases"]
        for case in cases:
            features = pipeline.transcript_features(case["text"])
            self.assertEqual(pipeline.has_meaningful_transcript(case["text"]), case["meaningful"], case)
            self.assertEqual(features["vocalization_heavy"], case["vocalization_heavy"], case)
            reasons = pipeline.suspicion_reasons(case["text"], case["duration_seconds"], 0.5)
            eligible, _ = pipeline.should_use_reazon_fallback(case["text"], reasons, "speech", 0.5)
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

    def test_whisper_requires_meaningful_unresolved_competition(self):
        suspicious = pipeline.Candidate("qwen3", "q", "もうダメ", ["weak_speech_conflict"])
        clean = pipeline.Candidate("qwen3", "q", "もうダメ")
        empty = pipeline.Candidate("reazon", "r", "", ["empty_transcript"])
        different = pipeline.Candidate("reazon", "r", "気持ちいい")
        self.assertFalse(pipeline.should_use_whisper(suspicious, empty))
        self.assertFalse(pipeline.should_use_whisper(clean, different))
        self.assertTrue(pipeline.should_use_whisper(suspicious, different))

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
