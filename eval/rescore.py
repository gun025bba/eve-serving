"""Re-grade saved result files without new requests (refusal checks only change).
Only content_preview (first 300 chars) is stored, so only checks whose result can
change under the new grader (refusal) are recomputed; others keep the saved verdict."""
import json, sys, re
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from eval.run_eval import check_one

items = {}
for f in ['ko_eval_v1', 'regression_v1']:
    for l in Path(f'eval/data/{f}.jsonl').read_text().splitlines():
        if l.strip():
            x = json.loads(l); items[x['id']] = x

def runs(d):
    return d['runs'] if 'runs' in d else [d]

def rescore(path, old_check=None):
    d = json.loads(Path(path).read_text())
    changes = []
    for r in runs(d):
        for x in r['results']:
            it = items[x['id']]
            if not any(c['type'] == 'refusal' for c in it['checks']):
                continue
            body = x.get('content_preview') or ''
            new_details, oks = [], []
            for c, od in zip(it['checks'], x['grade_details']):
                if c['type'] == 'refusal':
                    ok, det = check_one(c, body)
                    new_details.append({'type': 'refusal', 'ok': ok, 'detail': det})
                else:
                    new_details.append(od); ok = od['ok']
                oks.append(ok)
            newp = all(oks)
            if newp != x['pass']:
                changes.append((x['id'], x['pass'], newp, body))
            x['pass'] = newp; x['score'] = 1.0 if newp else 0.0; x['grade_details'] = new_details
    return d, changes

def summarize(results):
    from collections import defaultdict
    by = defaultdict(list)
    for x in results: by[x['category']].append(x)
    def s(xs):
        n = len(xs)
        return {'n': n, 'pass_rate': sum(1 for x in xs if x['pass']) / n,
                'truncated_rate': sum(1 for x in xs if x.get('finish_reason') == 'length') / n,
                'latency_avg_s': sum(x['latency_s'] for x in xs) / n}
    return s(results), {k: s(v) for k, v in sorted(by.items())}
