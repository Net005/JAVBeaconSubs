"""Synthetic progress tests: no model loading or inference."""
import types
import unittest
from unittest import mock

import qwen_pipeline as pipeline


class RecognitionProgressTest(unittest.TestCase):
    def test_batches_report_bounded_monotonic_progress_and_preserve_text(self):
        model = mock.Mock()
        model.transcribe.side_effect = lambda **kw: [types.SimpleNamespace(text="Example") for _ in kw["audio"]]
        with mock.patch.object(pipeline, "progress") as report:
            texts = pipeline.qwen_transcribe(model, [None] * 5, "", 2, progress_range=(8, 36))
        self.assertEqual(texts, ["Example"] * 5)
        self.assertEqual([call.args[0] for call in report.call_args_list], [19, 30, 36])

    def test_empty_retry_does_not_report_false_progress(self):
        with mock.patch.object(pipeline, "progress") as report:
            self.assertEqual(pipeline.qwen_transcribe(mock.Mock(), [], "", 2, progress_range=(36, 44)), [])
        report.assert_not_called()


if __name__ == "__main__":
    unittest.main()
