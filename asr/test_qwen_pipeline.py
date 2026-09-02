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
    def run_synthetic_pipeline(self, transcript, align_side_effect=None, mode="balanced", retry_transcript=None):
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
                "--device", "cpu", "--mode", mode, "--disable-reazon", "--disable-whisper", "--debug",
            ]
            with (
                mock.patch.object(sys, "argv", arguments),
                mock.patch.object(pipeline, "detect_regions", return_value=[pipeline.Region(0, len(audio), 0.55, "speech")]),
                mock.patch.object(pipeline, "load_qwen", return_value=object()),
                mock.patch.object(
                    pipeline,
                    "qwen_transcribe",
                    side_effect=([[transcript], [retry_transcript]] if retry_transcript is not None else None),
                    return_value=[transcript],
                ),
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
                mock.patch.object(pipeline.subprocess, "run", side_effect=complete_whisper) as run,
            ):
                result = pipeline.whisper_batch_transcribe("whisper-cli", str(model), [(audio, samplerate)], [0], "ja", "cuda", 30)
        self.assertTrue(written_lengths)
        self.assertTrue(all(length <= 30000 for length in written_lengths))
        self.assertGreater(len(written_lengths), 1)
        self.assertEqual(run.call_count, 1)
        self.assertNotIn("-ng", run.call_args.args[0])
        self.assertIn("-ojf", run.call_args.args[0])
        self.assertEqual(run.call_args.args[0][run.call_args.args[0].index("-l") + 1], "ja")
        self.assertNotIn("-tr", run.call_args.args[0])
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
                result = pipeline.whisper_batch_transcribe("whisper-cli", str(model), [(audio, 1000)], [0], "ja")
        self.assertFalse(result.success)
        self.assertEqual(result.exit_code, 3)
        self.assertEqual(result.failure_reason, "whisper_execution_failed")
        self.assertEqual(result.candidate_errors[0], "whisper_execution_failed")
        self.assertLessEqual(len(result.stderr), 8192)

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
        self.assertTrue(result.success, result.stderr)
        self.assertEqual(result.exit_code, 0)
        for index, (waveform, samplerate) in enumerate(clips):
            self.assertTrue(pipeline.has_meaningful_transcript(result.texts.get(index, "")))
            candidate = pipeline.Candidate("whisper", "large-v3", result.texts[index])
            self.assertTrue(
                pipeline.validate_whisper_candidate(candidate, candidate, len(waveform) / samplerate)[0]
            )

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
