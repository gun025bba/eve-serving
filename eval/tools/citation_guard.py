"""Citation-number guard (reference implementation for the gateway).

sanitize_citations(text, valid) removes inline citations [n] whose n is not in `valid`
(the result numbers returned by tools in this request; pass an empty set when no tool
call was made, which removes every [n]).
- Skipped: fenced code blocks (```...```), inline code (`...`), and index forms
  like a[1], x)[2], arr][3] (a run directly after an ASCII letter/digit, '_', ')' or ']' is an index, not a citation;
  Hangul directly before [n] is still a citation: '입니다[1]').
- Consecutive runs such as [1][2] are handled number by number.
- When a whole run is removed, horizontal whitespace right before it is removed too.
Returns (new_text, removed_count)."""
import re

_SEG = re.compile(r"```.*?(?:```|\Z)|`[^`\n]*`", re.S)
_RUN = re.compile(r"[ \t]*((?:\[\d+\])+)")
_ONE = re.compile(r"\[(\d+)\]")


def _fix(chunk, valid, counter):
    def run(m):
        i = m.start(1)
        if i > 0 and re.match(r"[A-Za-z0-9_)\]]", m.string[i - 1]):
            return m.group(0)  # index form like a[1], f(x)[2], m[0][7]
        kept = [c for c in _ONE.findall(m.group(1)) if int(c) in valid]
        counter[0] += len(_ONE.findall(m.group(1))) - len(kept)
        if not kept:
            return ""
        lead = m.group(0)[: len(m.group(0)) - len(m.group(1))]
        return lead + "".join(f"[{c}]" for c in kept)
    return _RUN.sub(run, chunk)


def sanitize_citations(text, valid):
    valid = {int(v) for v in (valid or ())}
    counter = [0]
    out, pos = [], 0
    for m in _SEG.finditer(text or ""):
        out.append(_fix(text[pos:m.start()], valid, counter))
        out.append(m.group(0))
        pos = m.end()
    out.append(_fix((text or "")[pos:], valid, counter))
    return "".join(out), counter[0]
