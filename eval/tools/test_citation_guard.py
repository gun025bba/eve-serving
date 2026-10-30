import json, unittest
from pathlib import Path
from citation_guard import sanitize_citations

CASES = [json.loads(l) for l in open(Path(__file__).with_name("citation_cases.jsonl"), encoding="utf-8") if l.strip()]


class TestCitationGuard(unittest.TestCase):
    pass


def _make(c):
    def t(self):
        out, n = sanitize_citations(c["text"], c["valid"])
        self.assertEqual(out, c["expected"], c["desc"])
        self.assertEqual(n, c["removed"], c["desc"])
    return t


for _c in CASES:
    setattr(TestCitationGuard, f"test_{_c['id']}", _make(_c))


class TestExtra(unittest.TestCase):
    def test_idempotent(self):
        for c in CASES:
            once, _ = sanitize_citations(c["text"], c["valid"])
            twice, n = sanitize_citations(once, c["valid"])
            self.assertEqual((twice, n), (once, 0), c["id"])

    def test_valid_accepts_strings(self):
        self.assertEqual(sanitize_citations("A [1]", ["1"]), ("A [1]", 0))


if __name__ == "__main__":
    unittest.main()
