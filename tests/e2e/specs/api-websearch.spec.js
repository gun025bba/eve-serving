// Web search via the chat tool path (docs/web-search.md): body flag web_search:"force" → gateway pre-searches (SearXNG)
// and streams chat.completion.tool_call / tool_result / sources events before [DONE]. One short request only.
const { test, expect } = require('@playwright/test');
const { auth, requireKey, body, parseSSE } = require('./helpers');
test('chat web_search=force streams tool_call + sources', async ({ request }) => {
  requireKey();
  const r = await request.post('/v1/chat/completions', { headers: auth(),
    data: body('What is the capital of France? One word. /no_think', { stream: true, web_search: 'force', max_tokens: 64 }) });
  if (r.status() === 400) {
    const t = await r.text();
    // web.enabled=false on the instance → 400 web_search_disabled (documented). Skip, don't fail.
    test.skip(t.includes('web_search_disabled'), 'web search disabled on this instance (GW_WEB_ENABLED=false)');
  }
  expect(r.status()).toBe(200);
  const ev = parseSSE(await r.text());
  const objs = ev.map(e => { try { return JSON.parse(e.data); } catch { return {}; } });
  const kinds = new Set(objs.map(o => o.object));
  console.log('[websearch] events:', [...kinds].join(','));
  for (const o of objs.filter(o => o.object && o.object.startsWith('chat.completion.') && o.object !== 'chat.completion.chunk'))
    console.log('[websearch]', o.object, JSON.stringify(o.args || ''), (o.sources || []).map(s => `${s.n}:${s.domain}`).join(' '), o.status || '');
  expect(kinds.has('chat.completion.tool_call') || kinds.has('chat.completion.tool_result') || kinds.has('chat.completion.sources'),
    `no web-search events; got ${[...kinds].join(',')}`).toBe(true);
  const src = objs.find(o => o.object === 'chat.completion.sources' || o.object === 'chat.completion.tool_result');
  if (src) for (const s of src.sources || []) { expect(s).toHaveProperty('url'); expect(s).toHaveProperty('n'); }
  expect(ev[ev.length - 1].data).toBe('[DONE]');
});
