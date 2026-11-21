import os
import tempfile
import unittest
from unittest.mock import patch

import run as kdj


class TestKDJSkill(unittest.TestCase):
    def test_extract_year_keywords(self):
        cases = [
            ("KDJ 1983", ("année 1983", "1983")),
            ("lance kdj 1983", ("année 1983", "1983")),
            ("lance kdj année 1983", ("année 1983", "1983")),
            ("kdj hits 1983", ("année 1983", "1983")),
            ("kdj 1983 au hasard", ("année 1983", "1983")),
            ("lance KDJ1985", ("année 1985", "1985")),
            ("1983", ("année 1983", "1983")),
        ]
        for raw, expected in cases:
            self.assertEqual(kdj.extract_keyword(raw), expected, f"failed for {raw!r}")

    def test_year_selection_random_and_non_repeating(self):
        channel_hits = [
            {"id": "v1", "title": "Retro 80s 12 Mix 03 (Kdj)", "url": "https://www.youtube.com/watch?v=v1"},
            {"id": "v2", "title": "John Rocca - I Want It To Be Real", "url": "https://www.youtube.com/watch?v=v2"},
            {"id": "v3", "title": "Michael Sembello - Maniac", "url": "https://www.youtube.com/watch?v=v3"},
            {"id": "v4", "title": "Rockwell - Somebody's Watching Me (1983)", "url": "https://www.youtube.com/watch?v=v4"},
        ]
        # Generic 80s catalog entry that does NOT match 1983 channel search
        catalog = [
            {"id": "gen80", "title": "80s Generic Mix 1987", "url": "https://www.youtube.com/watch?v=gen80"},
        ] + channel_hits

        with tempfile.TemporaryDirectory() as tmpdir:
            hist_path = os.path.join(tmpdir, "history.json")
            with patch.object(kdj, "fetch_channel_catalog", return_value=catalog), \
                 patch.object(kdj, "search_channel_by_keyword", return_value=channel_hits):
                picked_first_ids = []
                for _ in range(len(channel_hits)):
                    kw, tracks = kdj.select_creations("KDJ 1983", max_tracks=10, history_file=hist_path)
                    self.assertEqual(kw, "année 1983")
                    ids = [t["id"] for t in tracks]
                    # Must not pollute 1983 results with non-matching generic catalog item when 1983 pool is non-empty
                    self.assertNotIn("gen80", ids)
                    picked_first_ids.append(tracks[0]["id"])

                # Across N draws on N unique tracks, every draw must yield a distinct first track
                self.assertEqual(len(set(picked_first_ids)), len(channel_hits))

                # On draw N+1 (after exhausting pool), must never immediately repeat the previous pick
                _, tracks_next = kdj.select_creations("KDJ 1983", max_tracks=10, history_file=hist_path)
                self.assertNotEqual(tracks_next[0]["id"], picked_first_ids[-1])


if __name__ == "__main__":
    unittest.main()
