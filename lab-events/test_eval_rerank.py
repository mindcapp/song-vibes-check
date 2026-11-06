"""Checks that the Python port of the formula matches the Go one (internal/analytics).

    python3 -m unittest test_eval_rerank
"""
import unittest

from eval_rerank import rerank


def cand(i, cosine):
    return {"id": i, "cosine": cosine}


class RerankPortTest(unittest.TestCase):
    def test_hand_calculation_matches_go_test(self):
        # Same numbers as TestRerankHandCalculation in internal/analytics/rerank_test.go.
        stats = {1: (10, 2), 2: (20, 18)}
        got = rerank([cand(1, 0.90), cand(2, 0.85), cand(3, 0.80)], stats)
        self.assertEqual([r["id"] for r in got], [1, 3, 2])
        self.assertAlmostEqual(got[0]["keep"], 10.25 / 15)
        self.assertAlmostEqual(got[0]["score"], 0.835)
        self.assertAlmostEqual(got[1]["keep"], 0.45)
        self.assertAlmostEqual(got[1]["score"], 0.695)
        self.assertAlmostEqual(got[2]["score"], 0.646)

    def test_no_data_keeps_cosine_order(self):
        got = rerank([cand(1, 0.9), cand(2, 0.8)], {})
        self.assertEqual([r["id"] for r in got], [1, 2])
        self.assertAlmostEqual(got[0]["keep"], 0.5)

    def test_smoothing_example_matches_go_test(self):
        stats = {1: (2, 0), 2: (200, 20), 3: (100, 90)}
        cands = [cand(1, 0.8), cand(2, 0.8), cand(3, 0.5)]
        self.assertEqual([r["id"] for r in rerank(cands, stats, 0.3, 5)], [2, 1, 3])
        self.assertEqual([r["id"] for r in rerank(cands, stats, 0.3, 0)], [1, 2, 3])


if __name__ == "__main__":
    unittest.main()
