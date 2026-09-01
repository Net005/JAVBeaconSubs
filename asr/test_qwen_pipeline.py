import importlib.util
import pathlib
import sys
import unittest

import numpy as np


MODULE_PATH = pathlib.Path(__file__).with_name("qwen_pipeline.py")
SPEC = importlib.util.spec_from_file_location("qwen_pipeline", MODULE_PATH)
pipeline = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = pipeline
SPEC.loader.exec_module(pipeline)


class PipelineUtilitiesTest(unittest.TestCase):
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

    def test_suspicion_detects_hallucination_shapes(self):
        reasons = pipeline.suspicion_reasons("やめて" * 8, 0.7, 0.2)
        self.assertIn("text_duration_ratio", reasons)
        self.assertIn("pathological_repetition", reasons)
        self.assertIn("weak_speech_conflict", reasons)

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


if __name__ == "__main__":
    unittest.main()
