import unittest

from reazon_worker import window_ranges


class WindowRangesTest(unittest.TestCase):
    def test_overlapping_windows_cover_full_recording(self):
        windows = list(window_ranges(125.0, 45.0, 2.0))
        self.assertEqual(len(windows), 3)
        self.assertEqual(windows[0][2:], (0.0, 45.0, 0.0, 47.0))
        self.assertEqual(windows[1][2:], (45.0, 90.0, 43.0, 92.0))
        self.assertEqual(windows[2][2:], (90.0, 125.0, 88.0, 125.0))


if __name__ == "__main__":
    unittest.main()
