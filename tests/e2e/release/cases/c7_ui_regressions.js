'use strict';
// Case 7: fixed iPhone UI regressions, required in every run. Read-only against the server (static GETs only; the backend is mocked in the page).
// - computed backdrop-filter (and -webkit-) is 'none' on .navbar, .navbar.collapsed (after scrolling the list) and #topbar, plus a page-wide sweep
// - composer textarea is not inside a <form>, autocomplete/autocorrect/autocapitalize/spellcheck/name as expected
// Emulation limits (stay REAL-DEVICE items): Playwright WebKit cannot emulate CSS @media (display-mode: standalone), the notch safe-area inset,
// or the iOS contact-suggestion bar. We emulate the JS side (matchMedia + navigator.standalone -> html.standalone) and, in Chromium, the CSS media feature via CDP.
const { uiBase, openPage } = require('../ui');
const EXPECT_V = process.env.EVE_EXPECT_UI_VERSION || '1.9.0';
const EXPECT_AC = process.env.EVE_EXPECT_COMPOSER_AUTOCOMPLETE || 'eve-composer';

const THREADS = Array.from({ length: 30 }, (_, i) => ({ id: `qa-t${i}`, title: `QA 대화 ${i + 1}`, created_at: '2026-10-28T00:00:00Z', updated_at: `2026-10-28T00:${String(59 - i).padStart(2, '0')}:00Z`, archived: false }));
const SWEEP = `(() => { const bad = []; for (const n of document.querySelectorAll('*')) { const s = getComputedStyle(n); const v = s.backdropFilter || 'none', w = s.webkitBackdropFilter || 'none'; if (v !== 'none' || w !== 'none') bad.push((n.id ? '#' + n.id : '') + '.' + (n.className && n.className.baseVal === undefined ? n.className : n.tagName) + ' ' + v + '/' + w); } return bad; })()`;
const BF = sel => `(() => { const n = document.querySelector(${JSON.stringify(sel)}); if (!n) return null; const s = getComputedStyle(n); return { bf: s.backdropFilter || 'none', wbf: s.webkitBackdropFilter || 'none', vis: n.offsetParent !== null }; })()`;

async function scrollList(page) {   // scroll whichever element in #listView actually scrolls
  return page.evaluate(() => { const lv = document.getElementById('listView'); const els = [lv, ...lv.querySelectorAll('*')].filter(e => e.scrollHeight > e.clientHeight + 40 && /auto|scroll/.test(getComputedStyle(e).overflowY));
    const s = els[0] || document.scrollingElement; s.scrollTop = 400; s.dispatchEvent(new Event('scroll')); return !!els[0]; });
}

module.exports = {
  id: 7, name: 'UI regressions: no backdrop blur on headers, composer outside form + autofill attrs', readonly: true, needsDevice: true,
  async run(c) {
    const ui = await uiBase();
    try {
      c.info('UI source', `${ui.source} (${ui.base})`);
      // static facts
      const html = (await c.lib.req('GET', '/ui/').catch(() => null)) || { text: '' };
      const htmlText = ui.source === 'gateway' ? html.text : await (await fetch(ui.base + '/ui/')).text();
      const css = ui.source === 'gateway' ? (await c.lib.req('GET', '/ui/styles.css?v=' + EXPECT_V)).text : await (await fetch(ui.base + '/ui/styles.css')).text();
      c.check('index.html viewport has viewport-fit=cover', /name="viewport"[^>]*viewport-fit=cover/.test(htmlText));
      c.check('apple-mobile-web-app-status-bar-style = default (no translucent system blur)', /apple-mobile-web-app-status-bar-style" content="default"/.test(htmlText) && !/black-translucent/.test(htmlText));
      c.check('header height/padding use env(safe-area-inset-top)', /\.navbar\{[^}]*env\(safe-area-inset-top\)/.test(css) && /#topbar\{[^}]*env\(safe-area-inset-top\)/.test(css));
      c.check('no backdrop-filter declaration left in styles.css', !/(^|[^-\w])(-webkit-)?backdrop-filter\s*:/.test(css.replace(/\/\*[\s\S]*?\*\//g, '')), 'a backdrop-filter rule exists');
      const sw = ui.source === 'gateway' ? (await c.lib.req('GET', '/ui/sw.js')).text : await (await fetch(ui.base + '/ui/sw.js')).text();
      c.check(`UI version ${EXPECT_V}: sw.js cache name + index.html asset version`, sw.includes(`llmchat-${EXPECT_V}`) && htmlText.includes(`?v=${EXPECT_V}`), `expected ${EXPECT_V}`);

      for (const scheme of ['dark', 'light']) {
        const s = await openPage({ engine: 'webkit', scheme, standalone: true, base: ui.base, mock: { threads: THREADS } });
        try {
          const t = `[webkit standalone-emulated ${scheme}] `;
          c.check(t + 'html.standalone is set (emulation took effect)', await s.page.evaluate(() => document.documentElement.classList.contains('standalone')));
          c.check(t + 'UI reports version ' + EXPECT_V, (await s.page.evaluate('__app.version')) === EXPECT_V);
          const nav = await s.page.evaluate(BF('#listView .navbar'));
          c.check(t + '.navbar computed backdrop-filter is none', nav && nav.vis && nav.bf === 'none' && nav.wbf === 'none', JSON.stringify(nav));
          const scrolled = await scrollList(s.page); await s.page.waitForTimeout(500);
          const col = await s.page.evaluate(`(() => { const n = document.querySelector('#listView .navbar.collapsed'); if (!n) return null; const s = getComputedStyle(n); return { bf: s.backdropFilter || 'none', wbf: s.webkitBackdropFilter || 'none' }; })()`);
          if (!col) c.fail(t + '.navbar.collapsed exists after scrolling the list', `list scrolled=${scrolled}`);
          else c.check(t + '.navbar.collapsed computed backdrop-filter is none', col.bf === 'none' && col.wbf === 'none', JSON.stringify(col));
          c.check(t + 'page-wide sweep (list view): no element with a backdrop-filter', (await s.page.evaluate(SWEEP)).length === 0, (await s.page.evaluate(SWEEP)).join('; '));
          // chat view
          await s.page.click('#newChat'); await s.page.waitForTimeout(600);
          const top = await s.page.evaluate(BF('#topbar'));
          c.check(t + '#topbar computed backdrop-filter is none', top && top.vis && top.bf === 'none' && top.wbf === 'none', JSON.stringify(top));
          c.check(t + 'page-wide sweep (chat view): no element with a backdrop-filter', (await s.page.evaluate(SWEEP)).length === 0, (await s.page.evaluate(SWEEP)).join('; '));
          const comp = await s.page.evaluate(`(() => { const i = document.getElementById('input'); if (!i) return null; return { tag: i.tagName, form: !!i.closest('form'), ac: i.getAttribute('autocomplete'), acr: i.getAttribute('autocorrect'), cap: i.getAttribute('autocapitalize'), sc: i.getAttribute('spellcheck'), spell: i.spellcheck, name: i.getAttribute('name'), type: i.getAttribute('type'), aria: i.getAttribute('aria-autocomplete'), form_attr: i.getAttribute('form') }; })()`);
          if (!comp) c.fail(t + 'composer textarea #input exists');
          else {
            c.check(t + 'composer is a <textarea> not inside any <form> (and has no form= attribute)', comp.tag === 'TEXTAREA' && !comp.form && !comp.form_attr, JSON.stringify(comp));
            c.check(t + `composer autocomplete="${EXPECT_AC}", name="${EXPECT_AC}" (not a contact-ish token)`, comp.ac === EXPECT_AC && comp.name === EXPECT_AC && !/^(name|given-name|family-name|tel|email|username|on)$/.test(comp.ac || ''), JSON.stringify(comp));
            c.check(t + 'composer autocorrect=off, autocapitalize=off, spellcheck=false', comp.acr === 'off' && comp.cap === 'off' && comp.sc === 'false' && comp.spell === false, JSON.stringify(comp));
          }
          c.check(t + 'no page errors', s.errs.length === 0, s.errs.join('; '));
        } finally { await s.close(); }
      }
      // Chromium: emulate the CSS media feature too (display-mode: standalone) via CDP, then re-check the headers
      try {
        const s = await openPage({ engine: 'chromium', scheme: 'dark', standalone: true, base: ui.base, mock: { threads: THREADS } });
        try {
          const cdp = await s.ctx.newCDPSession(s.page);
          await cdp.send('Emulation.setEmulatedMedia', { features: [{ name: 'display-mode', value: 'standalone' }] });
          const dm = await s.page.evaluate("matchMedia('(display-mode: standalone)').matches");
          const nav = await s.page.evaluate(BF('#listView .navbar'));
          await scrollList(s.page); await s.page.waitForTimeout(500);
          const col = await s.page.evaluate(BF('#listView .navbar.collapsed'));
          await s.page.click('#newChat'); await s.page.waitForTimeout(500);
          const top = await s.page.evaluate(BF('#topbar'));
          const ok = [nav, col, top].every(x => x && x.bf === 'none' && x.wbf === 'none');
          c.check(`[chromium CDP display-mode:standalone=${dm}] .navbar, .navbar.collapsed, #topbar backdrop-filter none`, ok, JSON.stringify({ nav, col, top }));
        } finally { await s.close(); }
      } catch (e) { c.skip('chromium CDP standalone media emulation', String(e.message).split('\n')[0]); }
      c.device('iPhone home-screen PWA (force-quit and reopen; settings footer must read v' + EXPECT_V + ' · SW ' + EXPECT_V + ')',
        'top: status-bar area and header are solid, no blur/translucency, also after scrolling the list and a long thread; composer: tap it, NO contact/autofill suggestion bar or key icon above the keyboard; type a name-like word, no suggestions.');
      c.device('iPhone notch safe-area', 'header content starts below the notch with no double padding; Playwright cannot emulate safe-area-inset-top.');
    } finally { ui.close(); }
  },
};
