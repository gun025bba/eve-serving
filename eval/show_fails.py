import json, sys
from pathlib import Path
items={}
for f in ['ko_eval_v1','regression_v1']:
    for l in Path(f'eval/data/{f}.jsonl').read_text().splitlines():
        if l.strip(): x=json.loads(l); items[x['id']]=x
def runs(path):
    d=json.loads(Path(path).read_text())
    return d['runs'] if 'runs' in d else [d]
for r in runs(sys.argv[1]):
    if len(sys.argv)>2 and r['suite']!=sys.argv[2]: continue
    print('##', r['suite'])
    for x in r['results']:
        if not x['pass']:
            it=items[x['id']]
            print(x['id'], '| Q:', it['prompt'][:90].replace('\n',' '), '| checks:', json.dumps(it['checks'],ensure_ascii=False)[:150])
            print('   A:', (x.get('content_preview') or '')[:200].replace('\n',' / '))
