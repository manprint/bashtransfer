// Bashtransfer frontend tests with Playwright (Chromium): pages, browser uploads, page downloads,
// outcomes and cancellation. Run through scripts/ui/run.sh, which starts the servers.
// Env: BASE (long-wait server), BASE_SHORT (3s wait), BASE_AUTH (Basic auth u:p), BASE_GRACE
// (BASHT_SENDER_GRACE=8, sender grace), OUT (empty directory
// for files and screenshots), TAG (test label), ONLY (test-name regex), CHROME
// (Chromium executable instead of the Playwright installation), EXCLUDE_HOSTS (extra reachable
// hosts beyond 127.0.0.1 and localhost, comma-separated).
import { chromium } from 'playwright-core';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import net from 'node:net';

for (const k of ['BASE', 'BASE_SHORT', 'BASE_AUTH', 'BASE_GRACE', 'OUT']) {
  if (!process.env[k]) { console.error(`${k} not set: use scripts/ui/run.sh`); process.exit(2); }
}
const { BASE, BASE_SHORT, BASE_AUTH, BASE_GRACE, OUT } = process.env;
const TAG = process.env.TAG || 'direct';
const ONLY = process.env.ONLY ? new RegExp(process.env.ONLY) : null;
fs.mkdirSync(OUT, { recursive: true });

const env = { ...process.env };
for (const k of ['http_proxy', 'HTTP_PROXY', 'https_proxy', 'HTTPS_PROXY', 'all_proxy', 'ALL_PROXY']) delete env[k];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sha = (buf) => crypto.createHash('sha256').update(buf).digest('hex');
function assert(c, msg) { if (!c) throw new Error(msg); }

function mkfile(name, size) {
  const p = path.join(OUT, 'src', name);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, crypto.randomBytes(size));
  return p;
}

// Run a shell command in cwd (exec lets kill reach the program).
function sh(line, cwd = OUT) {
  fs.mkdirSync(cwd, { recursive: true });
  const p = spawn('bash', ['-c', 'exec ' + line], { cwd, env, stdio: ['ignore', 'pipe', 'pipe'] });
  let out = '', err = '';
  p.stdout.on('data', (d) => (out += d));
  p.stderr.on('data', (d) => (err += d));
  const done = new Promise((res) => p.on('close', (code, sig) => res({ code, sig, out, err })));
  return { p, done, get out() { return out; } };
}

const results = [];
async function test(name, fn) {
  if (ONLY && !ONLY.test(name)) return;
  const t0 = Date.now();
  try {
    await fn();
    results.push([name, 'PASS']);
    console.log(`PASS ${name} (${((Date.now() - t0) / 1000).toFixed(1)}s)`);
  } catch (e) {
    results.push([name, 'FAIL', e.message]);
    console.log(`FAIL ${name}: ${e.message}\n     ${(e.stack || '').split('\n').slice(1, 3).join('\n     ')}`);
  }
}

// No connections outside test servers: disable background Google services and prevent
// names other than test hosts (comma-separated EXCLUDE_HOSTS) from resolving.
const allowed = ['127.0.0.1', 'localhost', ...(process.env.EXCLUDE_HOSTS || '').split(',').filter(Boolean)];
// channel 'chromium': full Chromium in headless mode, not the headless shell.
const browser = await chromium.launch({
  ...(process.env.CHROME ? { executablePath: process.env.CHROME } : { channel: 'chromium' }),
  headless: true,
  args: ['--no-proxy-server', '--disable-background-networking', '--disable-component-update',
    '--disable-sync', '--no-pings', '--disable-domain-reliability',
    '--host-resolver-rules=MAP * ~NOTFOUND, ' + allowed.map((h) => 'EXCLUDE ' + h).join(', ')],
});

async function newPage(opts = {}) {
  const ctx = await browser.newContext({
    viewport: opts.viewport || { width: 1280, height: 900 },
    colorScheme: opts.dark ? 'dark' : 'light',
    acceptDownloads: true,
    ...(opts.creds ? { httpCredentials: opts.creds } : {}),
  });
  if (opts.clipboard) await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: opts.clipboard });
  const page = await ctx.newPage();
  page.errors = [];
  page.on('console', (m) => { if (m.type() === 'error') page.errors.push(m.text()); });
  page.on('pageerror', (e) => page.errors.push(e.message));
  // Network trace with ISO timestamps matching server logs, for failure diagnosis.
  page.net = [];
  page.on('response', (r) => page.net.push(`${new Date().toISOString()} ${r.status()} ${r.request().method()} ${r.url()}`));
  page.on('requestfailed', (r) => page.net.push(`${new Date().toISOString()} FAILED ${r.method()} ${r.url()} ${r.failure()?.errorText}`));
  page.on('download', (d) => page.net.push(`${new Date().toISOString()} DOWNLOAD ${d.url()}`));
  return page;
}

// Print and save page/sender state in OUT on failure for the runner
// (<name>-fail.json and its screenshot are included in the ui artifact).
async function diagnose(page, name, sender) {
  const info = {
    name,
    at: new Date().toISOString(),
    url: page.url(),
    status: await page.textContent('#dl-status').catch((e) => `ERR ${e.message}`),
    dlForm: await page.$eval('#dl', (f) => f.outerHTML).catch((e) => `ERR ${e.message}`),
    body: (await page.evaluate(() => document.body.innerText).catch(() => '')).slice(0, 800),
    net: page.net.slice(-60),
    errors: page.errors,
    sender: sender ? { exited: sender.exited(), out: sender.out().slice(-1500) } : null,
  };
  fs.writeFileSync(path.join(OUT, `${name}-fail.json`), JSON.stringify(info, null, 2));
  await page.screenshot({ path: path.join(OUT, `${name}-fail.png`), fullPage: true }).catch(() => {});
  console.log(`DIAG ${name} ${JSON.stringify(info, null, 1)}`);
}
// TCP proxy that cuts connections on demand, like a phone losing its network:
// sever() closes open connections; block prevents new ones.
async function severProxy(target) {
  const t = new URL(target);
  const socks = new Set();
  const px = { block: false, sever() { for (const c of socks) c.destroy(); socks.clear(); } };
  const srv = net.createServer((c) => {
    if (px.block) { c.destroy(); return; }
    const up = net.connect(+t.port, t.hostname);
    for (const x of [c, up]) { socks.add(x); x.on('error', () => {}); x.on('close', () => { socks.delete(x); c.destroy(); up.destroy(); }); }
    c.pipe(up);
    up.pipe(c);
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  px.url = `http://127.0.0.1:${srv.address().port}`;
  px.close = () => { px.sever(); srv.close(); };
  return px;
}

// allow: expected errors, e.g. loading a 404/410 page or an interrupted PUT.
function noErrors(page, what, allow) {
  const errs = page.errors.filter((e) => !(allow && allow.test(e)));
  assert(errs.length === 0, `${what}: console errors ${JSON.stringify(errs)}`);
}
const LOAD_4XX = /^Failed to load resource: the server responded with a status of 4(04|10) /;
const PUT_FAILED = /^Failed to load resource: net::ERR_|^Failed to load resource: the server responded with a status of 404 /;

// Alignment checks: desktop row fields/buttons align vertically and Copy buttons have
// equal widths; mobile buttons do not squeeze commands: above list fields and below
// standalone commands, with adequate touch targets. Commands wrap instead of scrolling
// horizontally at every width.
async function layoutChecks(page) {
  return page.evaluate(() => {
    const bad = [];
    const r = (el) => el.getBoundingClientRect();
    if (document.documentElement.scrollWidth > innerWidth) bad.push(`horizontal scroll ${document.documentElement.scrollWidth} > ${innerWidth}`);
    const narrow = matchMedia('(max-width: 40rem)').matches;
    const copies = [...document.querySelectorAll('.copy')].filter((b) => b.offsetParent);
    const widths = new Set(copies.map((b) => Math.round(r(b).width)));
    if (widths.size > 1) bad.push(`Copy button widths ${[...widths]}`);
    for (const b of copies) {
      const f = document.getElementById(b.dataset.copy);
      if (!f) continue;
      const a = r(f), c = r(b);
      if (narrow) {
        // On mobile, Copy buttons are compact and centered below or beside commands.
        if (c.width < 43 || c.height < 34) bad.push(`Copy button too small ${b.dataset.copy}: ${c.width}x${c.height}`);
        if (!(c.bottom <= a.top + 1 || c.top >= a.bottom - 1)) bad.push(`overlapping button ${b.dataset.copy}`);
      } else {
        if (Math.abs(a.top - c.top) > 1 || Math.abs(a.height - c.height) > 1) bad.push(`row ${b.dataset.copy}: field ${a.top}/${a.height} button ${c.top}/${c.height}`);
        if (c.left < a.right) bad.push(`overlapping button ${b.dataset.copy}`);
      }
    }
    for (const f of document.querySelectorAll('.field')) {
      if (f.offsetParent && f.scrollWidth > f.clientWidth + 1) bad.push(`command does not wrap: ${f.textContent} (${f.scrollWidth} > ${f.clientWidth})`);
    }
    // Fields in each grid align on the left.
    for (const g of document.querySelectorAll('.rows, .cmd')) {
      const lefts = new Set([...g.querySelectorAll('.field')].map((f) => Math.round(r(f).left)));
      if (lefts.size > 1) bad.push(`fields misaligned in ${g.className}: ${[...lefts]}`);
    }
    return bad;
  });
}

async function linkRows(page) {
  return page.$eval('#x-links', (s) => [
    ...[...s.querySelectorAll('#x-rows .label')].map((l) => ({ label: l.textContent, line: l.nextElementSibling.textContent })),
    { label: 'direct', line: s.querySelector('#x-c3').textContent },
  ]);
}

// Deterministic pattern for files created in the page.
const pattern = (n) => { const b = Buffer.alloc(n); for (let i = 0; i < n; i++) b[i] = (i * 31 + 7 + (i >> 11)) & 255; return b; };

async function dropFiles(page, files) {
  await page.evaluate((files) => {
    const dt = new DataTransfer();
    for (const f of files) {
      const b = new Uint8Array(f.size);
      for (let i = 0; i < f.size; i++) b[i] = (i * 31 + 7 + (i >> 11)) & 255;
      dt.items.add(new File([b], f.name, { type: 'application/octet-stream' }));
    }
    const drop = document.getElementById('drop');
    drop.dispatchEvent(new DragEvent('dragenter', { dataTransfer: dt, bubbles: true, cancelable: true }));
    window.__over = drop.classList.contains('over');
    drop.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }));
    drop.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
  }, files);
}

async function waitResult(page, timeout = 30000) {
  await page.waitForSelector('#x-result:not([hidden])', { timeout });
  return page.evaluate(() => ({ cls: document.getElementById('x-result').className, text: document.getElementById('x-result-text').textContent }));
}

async function peerBadge(page, id, ready) {
  await page.waitForFunction(({ id, ready }) => document.getElementById(id)?.textContent === (ready ? 'READY' : 'UNREADY'), { id, ready }, { timeout: 10000 });
  const style = await page.evaluate(({ id, ready }) => {
    const badge = document.getElementById(id), css = getComputedStyle(badge);
    const probe = document.createElement('span');
    probe.style.color = ready ? 'var(--recv)' : 'var(--err)';
    document.body.appendChild(probe);
    const expected = getComputedStyle(probe).color;
    probe.remove();
    return { color: css.color, expected, weight: css.fontWeight, border: css.borderTopColor };
  }, { id, ready });
  assert(style.color === style.expected && style.border === style.color && style.weight === '700', `badge ${id}: ${JSON.stringify(style)}`);
}

// --- Test ---

for (const w of [320, 390]) for (const dark of [false, true]) {
  await test(`peer readiness layout ${w} ${dark ? 'dark' : 'light'}`, async () => {
    const sender = await newPage({ viewport: { width: w, height: 844 }, dark });
    const receiver = await newPage({ viewport: { width: w, height: 844 }, dark });
    try {
      await sender.goto(BASE + '/');
      await sender.setInputFiles('#file', mkfile(`ready-${w}-${dark}.bin`, 1 << 20));
      await sender.waitForSelector('#x-links:not([hidden])');
      const url = (await linkRows(sender))[3].line;
      await peerBadge(sender, 'x-ready', false);
      assert(await sender.isEnabled('#x-copy'), 'UNREADY prevents link sharing');
      let confirm = false;
      await receiver.route('**/*?status=1', (route) => confirm
        ? route.continue()
        : route.fulfill({ json: { state: 'ready' } }));
      await receiver.goto(url);
      await peerBadge(receiver, 'dl-ready', false);
      assert(await receiver.isDisabled('#dl button'), 'state without server confirmation enables Download');
      await peerBadge(sender, 'x-ready', false);
      confirm = true;
      await receiver.evaluate(() => window.dispatchEvent(new Event('online')));
      await peerBadge(receiver, 'dl-ready', true);
      await peerBadge(sender, 'x-ready', true);
      assert(await receiver.isEnabled('#dl button'), 'READY does not enable Download');
      assert((await fetch(url, { headers: { Accept: 'text/html' } })).status === 200, 'READY consumed the link');
      for (const [page, role] of [[sender, 'sender'], [receiver, 'receiver']]) {
        assert((await layoutChecks(page)).length === 0, `layout badge ${role} ${w}`);
        await page.screenshot({ path: path.join(OUT, `ready-${role}-${w}-${dark ? 'dark' : 'light'}.png`), fullPage: true });
      }
      await sender.click('#x-cancel');
      await peerBadge(sender, 'x-ready', false);
      await peerBadge(receiver, 'dl-ready', false);
      assert(await receiver.isDisabled('#dl button'), 'cancelled session still downloadable');
    } finally {
      await sender.context().close();
      await receiver.context().close();
    }
  });
}

await test('peer readiness expires and ignores a late response after backgrounding', async () => {
  const sender = await newPage(), receiver = await newPage();
  try {
    await sender.goto(BASE + '/');
    await sender.setInputFiles('#file', mkfile('ready-lease.bin', 1 << 20));
    await sender.waitForSelector('#x-links:not([hidden])');
    const url = (await linkRows(sender))[3].line;
    await receiver.goto(url);
    await peerBadge(receiver, 'dl-ready', true);
    await peerBadge(sender, 'x-ready', true);
    await receiver.evaluate(() => {
      window.__visible = true;
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => window.__visible ? 'visible' : 'hidden' });
      const original = window.fetch;
      window.__holdStatus = true;
      window.fetch = (url, opts) => original(url, opts).then((response) => {
        if (!window.__holdStatus) return response;
        return new Promise((resolve) => { window.__releaseStatus = () => resolve(response); });
      });
    });
    await receiver.waitForFunction(() => typeof window.__releaseStatus === 'function');
    await peerBadge(receiver, 'dl-ready', false);
    assert(await receiver.isDisabled('#dl button'), 'confirmation expired but Download remains enabled');
    await receiver.evaluate(() => {
      window.__visible = false;
      document.dispatchEvent(new Event('visibilitychange'));
      window.__releaseStatus();
    });
    await sleep(100);
    await peerBadge(receiver, 'dl-ready', false);
    assert(await receiver.isDisabled('#dl button'), 'stale response re-enables Download');
    await peerBadge(sender, 'x-ready', false);
    assert((await fetch(url, { headers: { Accept: 'text/html' } })).status === 200, 'presence expiry consumed the link');
    await receiver.evaluate(() => {
      window.__holdStatus = false;
      window.__visible = true;
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await peerBadge(receiver, 'dl-ready', true);
    await peerBadge(sender, 'x-ready', true);
    const [dl] = await Promise.all([receiver.waitForEvent('download'), receiver.click('#dl button')]);
    const saved = path.join(OUT, 'ready-lease-received.bin');
    await dl.saveAs(saved);
    assert(sha(fs.readFileSync(saved)) === sha(fs.readFileSync(path.join(OUT, 'src', 'ready-lease.bin'))), 'confirmations changed the content');
    assert((await waitResult(sender)).cls === 'result ok', 'relay outcome');
    await peerBadge(sender, 'x-ready', false);
    await peerBadge(receiver, 'dl-ready', false);
  } finally {
    await sender.context().close();
    await receiver.context().close();
  }
});

await test('peer readiness rejects failed status responses and retries', async () => {
  const sender = await newPage(), receiver = await newPage();
  try {
    await sender.goto(BASE + '/');
    await sender.setInputFiles('#file', mkfile('ready-status-error.bin', 1 << 20));
    await sender.waitForSelector('#x-links:not([hidden])');
    await receiver.goto((await linkRows(sender))[3].line);
    await peerBadge(receiver, 'dl-ready', true);
    let failing = true;
    await receiver.route('**/*?status=1', (route) => failing
      ? route.fulfill({ status: 503, json: { state: 'ready', ready: true, ready_ttl: 5 } })
      : route.continue());
    const response = receiver.waitForResponse((r) => r.url().includes('?status=1') && r.status() === 503);
    await receiver.evaluate(() => window.dispatchEvent(new Event('online')));
    await (await response).finished();
    await receiver.evaluate(() => new Promise((resolve) => requestAnimationFrame(resolve)));
    assert((await receiver.textContent('#dl-ready')) === 'UNREADY' && await receiver.isDisabled('#dl button'), 'failed HTTP response is treated as READY');
    failing = false;
    await peerBadge(receiver, 'dl-ready', true);
    assert(await receiver.isEnabled('#dl button'), 'transient error not recovered automatically');
    await sender.click('#x-cancel');
  } finally {
    await sender.context().close();
    await receiver.context().close();
  }
});

for (const [w, dark, font] of [[1280, false], [1280, true], [390, false], [390, true], [320, false], [320, true], [320, false, 'Arial, sans-serif']]) {
  await test(`home layout ${w} ${dark ? 'dark' : 'light'}${font ? ' fallback font' : ''}`, async () => {
    const page = await newPage({ viewport: { width: w, height: 900 }, dark });
    const resp = await page.goto(BASE + '/');
    assert(resp.status() === 200, `status ${resp.status()}`);
    if (font) await page.addStyleTag({ content: `body { font-family: ${font}; }` });
    await page.screenshot({ path: path.join(OUT, `home-${w}-${dark ? 'dark' : 'light'}${font ? '-fallback-font' : ''}.png`), fullPage: true });
    const first = await page.$eval('main > section', (s) => ({ h2: s.querySelector('h2').textContent, drop: !!s.querySelector('#drop') }));
    assert(first.drop && /Send a file/.test(first.h2), `first section ${JSON.stringify(first)}`);
    const box = await page.$eval('#drop', (d) => d.getBoundingClientRect().toJSON());
    assert(box.height >= 150 && box.height <= 280, `drop box height ${box.height}`);
    const pick = await page.$eval('#pick', (b) => ({ ...b.getBoundingClientRect().toJSON(), text: b.textContent }));
    assert(pick.text === 'Upload' && pick.top > box.top && pick.bottom < box.bottom, `Upload button ${JSON.stringify(pick)}`);
    const bad = await layoutChecks(page);
    assert(bad.length === 0, bad.join('; '));
    const roles = await page.$$eval('h2', (hs) => hs.map((h) => h.textContent.trim()));
    assert(roles.some((r) => r.startsWith('Sender') && r.includes('terminal')) && roles.some((r) => r.startsWith('Receiver')), `headings ${roles}`);
    const receiver = await page.$eval('section[aria-labelledby="receive"]', (s) => ({
      color: getComputedStyle(s.querySelector('.role-recv')).color,
      tools: [...s.querySelectorAll('.label')].map((l) => ({ color: getComputedStyle(l).color, weight: getComputedStyle(l).fontWeight })),
    }));
    assert(receiver.tools.length === 3 && receiver.tools.every((l) => l.color === receiver.color && l.weight === '700'), `receiver tool styling ${JSON.stringify(receiver)}`);
    noErrors(page, 'home');
    await page.context().close();
  });
}

await test('home copy button', async () => {
  const page = await newPage({ clipboard: BASE });
  await page.goto(BASE + '/');
  await page.click('[data-copy="c-send"]');
  // The label changes after the asynchronous clipboard write succeeds.
  await page.waitForFunction(() => document.querySelector('[data-copy="c-send"]').textContent !== 'Copy', null, { timeout: 2000 }).catch(() => {});
  const txt = await page.textContent('[data-copy="c-send"]');
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  assert(txt === 'Copied', `label ${txt}`);
  assert(clip === `curl ${BASE} -T FILE`, `clipboard ${clip}`);
  await sleep(1700);
  assert((await page.textContent('[data-copy="c-send"]')) === 'Copy', 'label not restored');
  noErrors(page, 'home copy');
  await page.context().close();
});

await test('copy feedback cannot mark a new sender link as copied', async () => {
  const page = await newPage({ viewport: { width: 390, height: 844 }, clipboard: BASE });
  try {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'share', { configurable: true, value: undefined });
      Object.defineProperty(navigator.clipboard, 'writeText', { configurable: true, value: () => new Promise((resolve) => { window.__finishCopy = resolve; }) });
    });
    await page.goto(BASE + '/');
    await page.setInputFiles('#file', mkfile('copy-old.bin', 100));
    await page.waitForSelector('#x-links:not([hidden])');
    const old = (await linkRows(page))[3].line;
    await page.click('#x-copy');
    await page.waitForFunction(() => typeof window.__finishCopy === 'function');
    await page.click('#x-cancel');
    await waitResult(page, 5000);
    await page.click('#x-again');
    await page.setInputFiles('#file', mkfile('copy-new.bin', 100));
    await page.waitForSelector('#x-links:not([hidden])');
    assert((await linkRows(page))[3].line !== old, 'new upload reuses the old link');
    await page.evaluate(async () => {
      window.__finishCopy();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    assert((await page.textContent('#x-copy')) === 'Copy link', 'old copy outcome applied to the new link');
    await page.click('#x-cancel');
    noErrors(page, 'stale Copy feedback', PUT_FAILED);
  } finally { await page.context().close(); }
});

await test('home password ZIP commands copy', async () => {
  const page = await newPage({ clipboard: BASE });
  await page.goto(BASE + '/');
  const commands = await page.$$eval('section[aria-labelledby="send-zip"] .field', (cs) => cs.map((c) => ({ id: c.id, line: c.textContent })));
  assert(commands.length === 6, `missing ZIP/history commands: ${commands.length}`);
  for (const { id, line } of commands) {
    await page.click(`[data-copy="${id}"]`);
    await page.waitForFunction((id) => document.querySelector(`[data-copy="${id}"]`).textContent === 'Copied', id, { timeout: 2000 });
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    assert(clip === line, `incomplete copy ${id}: ${clip}`);
    const parsed = spawnSync('bash', ['-n', '-c', clip], { env, encoding: 'utf8' });
    assert(parsed.status === 0, `Bash syntax ${id}: ${parsed.stderr || parsed.error}`);
  }
  noErrors(page, 'home ZIP copy');
  await page.context().close();
});

for (const [w, dark] of [[320, false], [320, true], [390, false], [390, true]]) {
  await test(`home layout ${w} ${dark ? 'dark' : 'light'} after upload: share and terminal commands`, async () => {
    const src = mkfile(`mobile-${w}-${dark}.bin`, 1 << 20);
    const page = await newPage({ viewport: { width: w, height: 844 }, dark, clipboard: BASE });
    try {
      await page.addInitScript(() => Object.defineProperty(navigator, 'share', { configurable: true, value: undefined }));
      await page.goto(BASE + '/');
      await page.setInputFiles('#file', src);
      await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
      const url = (await linkRows(page))[3].line;
      const share = await page.$eval('#x-share-row', (s) => ({
        text: s.querySelector('.share-label').textContent,
        color: getComputedStyle(s.querySelector('.share-label')).color,
        weight: getComputedStyle(s.querySelector('.share-label')).fontWeight,
        senderColor: getComputedStyle(document.querySelector('.role-send')).color,
        button: s.querySelector('#x-copy').getBoundingClientRect().toJSON(),
        label: s.querySelector('.share-label').getBoundingClientRect().toJSON(),
      }));
      assert(share.text === 'Share:' && share.color === share.senderColor && share.weight === '700', `Share label ${JSON.stringify(share)}`);
      assert(share.button.left > share.label.right && share.button.height >= 44, `Share row ${JSON.stringify(share)}`);
      assert(await page.isVisible('#x-copy') && !(await page.isVisible('#x-c3')), 'mobile Share is not just a label and button');
      assert(!(await page.isVisible('#x-c0')), 'Bash commands expanded before requested');
      assert((await layoutChecks(page)).length === 0, 'compact Share layout');
      await page.locator('main > section').first().screenshot({ path: path.join(OUT, `share-${w}-${dark ? 'dark' : 'light'}.png`) });
      await page.click('#x-copy');
      await page.waitForFunction(() => document.getElementById('x-copy').textContent === 'Copied');
      assert((await page.evaluate(() => navigator.clipboard.readText())) === url, 'invalid public link copy');
      await page.click('#x-terminal summary');
      assert(await page.isVisible('#x-c0') && await page.isVisible('#x-copy'), 'Bash commands hide Share');
      await page.click('[data-copy="x-c0"]');
      await page.waitForFunction(() => document.querySelector('[data-copy="x-c0"]').textContent === 'Copied');
      assert((await page.evaluate(() => navigator.clipboard.readText())) === `curl -f -OJ "${url}"`, 'invalid Bash command copy');
      const bad = await layoutChecks(page);
      assert(bad.length === 0, bad.join('; '));
      await page.locator('main > section').first().screenshot({ path: path.join(OUT, `commands-${w}-${dark ? 'dark' : 'light'}.png`) });
      await page.click('#x-cancel');
      await waitResult(page, 5000);
      assert((await layoutChecks(page)).length === 0, 'mobile outcome layout');
      noErrors(page, 'mobile sharing layout', PUT_FAILED);
    } finally { await page.context().close(); }
  });
}

await test('mobile download and error page layouts', async () => {
  const sender = await newPage();
  try {
    await sender.goto(BASE + '/');
    await sender.setInputFiles('#file', mkfile('mobile-download.bin', 1 << 18));
    await sender.waitForSelector('#x-links:not([hidden])');
    const url = (await linkRows(sender))[3].line;
    for (const [w, dark] of [[320, false], [320, true], [390, false], [390, true]]) {
      const page = await newPage({ viewport: { width: w, height: 844 }, dark });
      try {
        const response = await page.goto(url);
        assert(response.status() === 200, `download page ${response.status()}`);
        assert((await layoutChecks(page)).length === 0, `layout download ${w}/${dark}`);
        const tools = await page.$eval('.rows.tools', (s) => ({
          color: getComputedStyle(document.querySelector('.role-recv')).color,
          labels: [...s.querySelectorAll('.label')].map((l) => ({ color: getComputedStyle(l).color, weight: getComputedStyle(l).fontWeight })),
        }));
        assert(tools.labels.length === 3 && tools.labels.every((l) => l.color === tools.color && l.weight === '700'), 'inconsistent receiver tool styling');
        await page.screenshot({ path: path.join(OUT, `download-${w}-${dark ? 'dark' : 'light'}.png`), fullPage: true });
        const missing = await page.goto(BASE + '/AbC123xY9z');
        assert(missing.status() === 404, `error page ${missing.status()}`);
        assert((await layoutChecks(page)).length === 0, `error layout ${w}/${dark}`);
        const font = await page.evaluate(() => parseFloat(getComputedStyle(document.body).fontSize));
        assert(font >= 14 && font < 16, `mobile font size ${font}`);
        await page.screenshot({ path: path.join(OUT, `error-${w}-${dark ? 'dark' : 'light'}.png`), fullPage: true });
        noErrors(page, 'mobile download/error', LOAD_4XX);
      } finally { await page.context().close(); }
    }
    await sender.click('#x-cancel');
    await waitResult(sender, 5000);
  } finally { await sender.context().close(); }
});

await test('upload button + curl receiver from the table', async () => {
  const size = 24 << 20;
  const src = mkfile('upload.bin', size);
  const page = await newPage({ clipboard: BASE });
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
  const rows = await linkRows(page);
  assert(rows.map((r) => r.label).join(',') === 'curl,wget,aria2c,direct', `labels ${JSON.stringify(rows)}`);
  const url = rows[3].line;
  assert(url.startsWith(BASE + '/') && url.length > BASE.length + 5, `direct ${url}`);
  assert(rows[0].line === `curl -f -OJ "${url}"`, `curl row ${rows[0].line}`);
  assert(rows[1].line === `wget --content-disposition "${url}"`, `wget row ${rows[1].line}`);
  assert(rows[2].line === `aria2c -x1 -s1 --allow-overwrite=false "${url}"`, `aria2c row ${rows[2].line}`);
  assert((await page.textContent('#x-name')) === 'upload.bin' && (await page.textContent('#x-size')) === '24.0 MiB', 'filename/size');
  // Switching apps on a phone stops uploading: show the warning beside the links.
  const hint = await page.textContent('#x-links');
  assert(/Keep this page open and in the foreground until the end/.test(hint) && /split screen/.test(hint), `foreground warning: ${hint}`);
  // Copy the direct link.
  await page.click('[data-copy="x-c3"]');
  await page.waitForFunction(() => document.querySelector('[data-copy="x-c3"]').textContent === 'Copied', null, { timeout: 2000 });
  assert((await page.evaluate(() => navigator.clipboard.readText())) === url, 'direct link copy');
  await sleep(2600);
  const waitStatus = await page.textContent('#x-status');
  assert(waitStatus === 'Waiting for the receiver to start the download…', `waiting status: ${waitStatus}`);
  // No progress before the receiver: bytes in network buffers are not delivered.
  const waitPct = await page.textContent('#x-pct');
  assert(waitPct === '0%', `progress without a receiver: ${waitPct}`);
  const bad = await layoutChecks(page);
  assert(bad.length === 0, bad.join('; '));
  await page.screenshot({ path: path.join(OUT, 'upload-waiting.png'), fullPage: true });
  // Receiver: the table's curl command, slowed down to observe progress.
  const recvDir = path.join(OUT, 'recv-curl');
  const rcv = sh(rows[0].line.replace(/^curl /, 'curl --limit-rate 6M '), recvDir);
  const samples = [];
  for (let i = 0; i < 8; i++) {
    await sleep(400);
    samples.push(await page.evaluate(() => ({
      now: +document.getElementById('x-bar').getAttribute('aria-valuenow'),
      pct: document.getElementById('x-pct').textContent,
      rate: document.getElementById('x-rate').textContent,
      amount: document.getElementById('x-amount').textContent,
      status: document.getElementById('x-status').textContent,
    })));
    if (i === 4) await page.screenshot({ path: path.join(OUT, 'upload-progress.png'), fullPage: true });
  }
  console.log('     samples:', samples.map((s) => `${s.pct} ${s.rate} [${s.amount}] ${s.status}`).join(' | '));
  const mid = samples.filter((s) => s.now > 0 && s.now < 100);
  assert(mid.length > 0, 'no intermediate samples');
  assert(mid.some((s) => /^\d+\.\d (KiB|MiB)\/s$/.test(s.rate)), 'missing transfer rate');
  assert(mid.some((s) => s.status === 'Sending to the receiver…'), 'missing sending status');
  assert(mid.some((s) => /left$/.test(s.amount)), 'missing remaining time');
  for (let i = 1; i < samples.length; i++) assert(samples[i].now >= samples[i - 1].now, 'non-monotonic progress');
  const r = await rcv.done;
  assert(r.code === 0, `curl receiver: ${r.code} ${r.err}`);
  const res = await waitResult(page);
  assert(res.cls === 'result ok' && /^Done: the receiver got the whole file \(24\.0 MiB in \d/.test(res.text), `outcome ${JSON.stringify(res)}`);
  assert(await page.$eval('#x-links', (e) => e.hidden), 'links still visible');
  assert((await page.getAttribute('#x-bar', 'aria-valuenow')) === '100', 'progress bar not at 100');
  const got = fs.readFileSync(path.join(recvDir, 'upload.bin'));
  assert(sha(got) === sha(fs.readFileSync(src)), 'received content mismatch');
  await page.screenshot({ path: path.join(OUT, 'upload-done.png'), fullPage: true });
  await page.click('#x-again');
  assert(await page.isVisible('#drop') && !(await page.isVisible('#xfer')), 'Send another file does not reset');
  noErrors(page, 'upload');
  await page.context().close();
});

await test('drag and drop + browser receiver on the download page', async () => {
  const size = 3 << 20;
  const name = 'relazione finale ü.bin';
  const page = await newPage();
  await page.goto(BASE + '/');
  await dropFiles(page, [{ name, size }]);
  assert(await page.evaluate(() => window.__over), 'over class missing on dragenter');
  await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
  assert(!(await page.$eval('#drop', (d) => d.classList.contains('over'))), 'over class remains');
  const url = (await linkRows(page))[3].line;
  // Dark mobile preview: does not consume the link.
  const peek = await newPage({ viewport: { width: 390, height: 844 }, dark: true });
  await peek.goto(url);
  await peek.screenshot({ path: path.join(OUT, 'download-390-dark.png'), fullPage: true });
  assert((await layoutChecks(peek)).length === 0, 'layout download 390');
  noErrors(peek, 'download 390');
  await peek.context().close();
  const rp = await newPage();
  const resp = await rp.goto(url);
  assert(resp.status() === 200, `download page ${resp.status()}`);
  const info = await rp.$$eval('.file dd', (d) => d.map((x) => x.textContent));
  assert(info[0] === name && info[1] === '3.0 MiB (3,145,728 bytes)', `file details ${info}`);
  const tools = await rp.$$eval('.rows.tools .label', (l) => l.map((x) => x.textContent));
  assert(tools.join(',') === 'curl,wget,aria2c', `download page commands ${tools}`);
  const bad = await layoutChecks(rp);
  assert(bad.length === 0, bad.join('; '));
  await rp.screenshot({ path: path.join(OUT, 'download-1280.png'), fullPage: true });
  const [dl] = await Promise.all([rp.waitForEvent('download', { timeout: 15000 }), rp.click('#dl button')]);
  assert(dl.suggestedFilename() === name, `suggested filename ${dl.suggestedFilename()}`);
  const saved = path.join(OUT, 'recv-browser.bin');
  await dl.saveAs(saved);
  assert(sha(fs.readFileSync(saved)) === sha(pattern(size)), 'downloaded content mismatch');
  const btn = await rp.$eval('#dl button', (b) => ({ dis: b.disabled, t: b.textContent }));
  assert(btn.dis && btn.t === 'Download requested', `button after click ${JSON.stringify(btn)}`);
  const res = await waitResult(page);
  assert(res.cls === 'result ok', `sender outcome ${JSON.stringify(res)}`);
  noErrors(rp, 'download');
  noErrors(page, 'upload drop');
  const again = await rp.goto(url);
  assert(again.status() === 410 && /Link already used or expired/.test(await rp.textContent('h2')), `used link ${again.status()}`);
  await rp.screenshot({ path: path.join(OUT, 'message-410.png'), fullPage: true });
  noErrors(rp, '410 page', LOAD_4XX);
  await page.context().close();
  await rp.context().close();
});

await test('drop of several files is refused', async () => {
  const page = await newPage();
  await page.goto(BASE + '/');
  await dropFiles(page, [{ name: 'a.txt', size: 10 }, { name: 'b.txt', size: 10 }]);
  await sleep(300);
  assert((await page.textContent('#drop-error')) === 'Drop one file at a time.' && (await page.isVisible('#drop-error')), 'error message');
  assert(!(await page.isVisible('#xfer')), 'upload started');
  await page.screenshot({ path: path.join(OUT, 'drop-error.png') });
  noErrors(page, 'drop multipli');
  await page.context().close();
});

await test('cancel closes the link', async () => {
  const src = mkfile('cancel.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  await sleep(1500);
  await page.click('#x-cancel');
  const res = await waitResult(page, 5000);
  assert(res.cls === 'result err' && res.text === 'Cancelled: the link no longer works.', `outcome ${JSON.stringify(res)}`);
  const ms = await waitGone(url, 1000);
  assert(ms >= 0, 'link still valid 1 s after Cancel');
  console.log(`     link 410 ${ms} ms after Cancel`);
  // A receiver opening the link after Cancel must not see "already used": nothing was downloaded.
  const rp = await newPage();
  const rr = await rp.goto(url);
  assert(rr.status() === 410 && (await rp.textContent('h2')) === 'Sender stopped', `receiver after Cancel ${rr.status()} ${await rp.textContent('h2')}`);
  await rp.context().close();
  await page.screenshot({ path: path.join(OUT, 'upload-cancelled.png'), fullPage: true });
  noErrors(page, 'cancel', PUT_FAILED);
  await page.context().close();
});

// Non-consuming link state: download page (Accept: text/html) returns 200 or 410.
async function linkStatus(url) {
  const r = await fetch(url, { headers: { Accept: 'text/html' } });
  await r.arrayBuffer();
  return r.status;
}
async function waitGone(url, ms) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) {
    if ((await linkStatus(url)) === 410) return Date.now() - t0;
    await sleep(100);
  }
  return -1;
}

await test('closing the page closes the link', async () => {
  const src = mkfile('closed.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  await sleep(1500);
  // Act like the user: close the tab and confirm the beforeunload warning.
  let asked = false;
  page.on('dialog', (d) => { asked = d.type() === 'beforeunload'; d.accept(); });
  await Promise.all([page.waitForEvent('close'), page.close({ runBeforeUnload: true })]);
  assert(asked, 'no beforeunload warning during an upload');
  const ms = await waitGone(url, 3000);
  assert(ms >= 0, 'link still valid 3 s after page closure');
  console.log(`     link 410 ${ms} ms after closure`);
  await page.context().close();
});

await test('without the DELETE the grace period closes the link', async () => {
  const src = mkfile('crashed.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE_GRACE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  // Without explicit cancellation (crash, lost DELETE), only stream/PUT closure remains.
  await page.evaluate(() => {
    const f = window.fetch;
    window.__deletes = 0;
    window.fetch = (u, o) => (o && o.method === 'DELETE' ? (window.__deletes++, Promise.resolve(new Response(null))) : f(u, o));
  });
  await sleep(1500);
  await page.click('#x-cancel');
  assert((await page.evaluate(() => window.__deletes)) === 1, 'Cancel without DELETE');
  const ms = await waitGone(url, 30000);
  assert(ms >= 0, 'link still valid after grace');
  console.log(`     link 410 ${ms} ms after stream closure, without DELETE`);
  await page.context().close();
});

await test('closing the page during the download stops the receiver', async () => {
  const src = mkfile('closed2.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  const rcv = sh(`curl -sS -f --limit-rate 2M -o /dev/null -w '%{http_code} %{size_download}' "${url}"`);
  await page.waitForFunction(() => document.getElementById('x-status').textContent === 'Sending to the receiver…', null, { timeout: 10000 });
  await page.close({ runBeforeUnload: false });
  const r = await Promise.race([rcv.done, sleep(8000).then(() => null)]);
  assert(r, 'receiver not interrupted within 8 s');
  assert(r.code !== 0, `receiver exited with 0: ${r.out}`);
  await page.context().close();
});

await test('expired link reported to the browser sender', async () => {
  const src = mkfile('expire.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE_SHORT + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  const res = await waitResult(page, 20000);
  assert(res.cls === 'result err' && res.text === 'No receiver started the download in time: the link is closed.', `outcome ${JSON.stringify(res)}`);
  const rp = await newPage();
  const rr = await rp.goto(url);
  assert(rr.status() === 410 && (await rp.textContent('h2')) === 'Link expired', `receiver after expiry ${rr.status()} ${await rp.textContent('h2')}`);
  await rp.context().close();
  await page.screenshot({ path: path.join(OUT, 'upload-expired.png'), fullPage: true });
  noErrors(page, 'expired', PUT_FAILED);
  await page.context().close();
});

await test('receiver gone reported to the browser sender', async () => {
  const src = mkfile('gone.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  const rcv = sh(`curl -s --limit-rate 1M -o /dev/null "${url}"`);
  await sleep(1500);
  rcv.p.kill('SIGKILL');
  await rcv.done;
  const res = await waitResult(page, 20000);
  assert(res.cls === 'result err' && res.text === 'The receiver disconnected or stopped downloading.', `outcome ${JSON.stringify(res)}`);
  await page.screenshot({ path: path.join(OUT, 'upload-receiver-gone.png'), fullPage: true });
  noErrors(page, 'receiver gone', PUT_FAILED);
  await page.context().close();
});

// Sender grace (BASHT_SENDER_GRACE=8 on BASE_GRACE). Route the sender page through a proxy
// that cuts connections, like a phone losing its network in the background.
async function graceSender(px, name, size) {
  const src = mkfile(name, size);
  const page = await newPage();
  await page.goto(px.url + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  assert(url.startsWith(px.url + '/'), `link ${url}`);
  await page.evaluate(() => {
    window.__st = [];
    new MutationObserver(() => window.__st.push(document.getElementById('x-status').textContent))
      .observe(document.getElementById('x-status'), { childList: true, characterData: true, subtree: true });
  });
  return { src, page, url, direct: url.replace(px.url, BASE_GRACE) };
}
const WAITING = 'Waiting for the receiver to start the download…';

await test('peer readiness follows sender foreground loss and recovery', async () => {
  const s = await graceSender({ url: BASE_GRACE }, 'ready-foreground.bin', 24 << 20);
  const receiver = await newPage();
  try {
    await receiver.goto(s.url);
    await peerBadge(receiver, 'dl-ready', true);
    await peerBadge(s.page, 'x-ready', true);
    await s.page.evaluate(() => {
      window.__visible = false;
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => window.__visible ? 'visible' : 'hidden' });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await peerBadge(s.page, 'x-ready', false);
    await peerBadge(receiver, 'dl-ready', false);
    assert(await receiver.isDisabled('#dl button'), 'sender absent but Download enabled');
    await s.page.evaluate(() => {
      window.__visible = true;
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await peerBadge(receiver, 'dl-ready', true);
    await peerBadge(s.page, 'x-ready', true);
    await s.page.click('#x-cancel');
    await waitResult(s.page);
    await peerBadge(receiver, 'dl-ready', false);
  } finally {
    await s.page.context().close();
    await receiver.context().close();
  }
});

await test('sender grace: the page reconnects and a curl receiver gets the file', async () => {
  const px = await severProxy(BASE_GRACE);
  let s;
  try {
    s = await graceSender(px, 'grace1.bin', 24 << 20);
    await sleep(1000);
    px.sever();
    await s.page.waitForFunction((w) => { const a = window.__st; return a.some((x) => /reconnecting/.test(x)) && a[a.length - 1] === w; }, WAITING, { timeout: 15000 });
    const rcv = sh(`curl -sS -f -OJ "${s.direct}"`, path.join(OUT, 'grace1'));
    const r = await rcv.done;
    assert(r.code === 0, `curl ${r.code} ${r.err}`);
    assert(sha(fs.readFileSync(path.join(OUT, 'grace1', 'grace1.bin'))) === sha(fs.readFileSync(s.src)), 'received file mismatch');
    const res = await waitResult(s.page, 20000);
    assert(res.cls === 'result ok' && /^Done: the receiver got the whole file/.test(res.text), `outcome ${JSON.stringify(res)}`);
    noErrors(s.page, 'grace reconnect', PUT_FAILED);
  } finally { px.close(); if (s) await s.page.context().close().catch(() => {}); }
});

await test('sender grace: a browser receiver pressing Download while the sender is away', async () => {
  const px = await severProxy(BASE_GRACE);
  let s, rp;
  try {
    s = await graceSender(px, 'grace2.bin', 24 << 20);
    await sleep(1000);
    // No new connections: keep the sender absent until the receiver attempts Download.
    px.block = true;
    px.sever();
    rp = await newPage();
    let away = false;
    for (let i = 0; i < 40 && !away; i++) {
      const resp = await rp.goto(s.direct);
      assert(resp.status() === 200, `page ${resp.status()}`);
      away = await rp.isVisible('#dl-away');
      if (!away) await sleep(500);
    }
    assert(away, 'download page does not warn that the sender is absent');
    assert(await rp.isDisabled('#dl button'), 'download available while sender is absent');
    assert(/link has not been used/.test(await rp.textContent('#dl-status')), 'status does not explain that the link stays intact');
    assert((await fetch(s.direct, { headers: { Accept: 'text/html' } })).status === 200, 'waiting consumed the link');
    px.block = false;
    await rp.waitForFunction(() => !document.querySelector('#dl button').disabled, null, { timeout: 15000 });
    assert(!(await rp.isVisible('#dl-away')), 'status does not update when sender returns');
    const [dl] = await Promise.all([rp.waitForEvent('download', { timeout: 30000 }), rp.click('#dl button')]);
    const saved = path.join(OUT, 'recv-grace2.bin');
    await dl.saveAs(saved);
    assert(sha(fs.readFileSync(saved)) === sha(fs.readFileSync(s.src)), 'received file mismatch');
    const res = await waitResult(s.page, 20000);
    assert(res.cls === 'result ok', `outcome ${JSON.stringify(res)}`);
  } finally {
    px.close();
    for (const p of [rp, s && s.page]) if (p) await p.context().close().catch(() => {});
  }
});

await test('sender grace: the link closes if the sender does not come back in time', async () => {
  const px = await severProxy(BASE_GRACE);
  let s, rp;
  try {
    s = await graceSender(px, 'grace3.bin', 1 << 20);
    await sleep(1000);
    px.block = true;
    px.sever();
    rp = await newPage();
    await rp.goto(s.direct);
    await rp.waitForFunction(() => /Sender stopped/.test(document.getElementById('dl-status').textContent), null, { timeout: 30000 });
    assert(await rp.isDisabled('#dl button'), 'button remains enabled after expiry');
    assert((await waitGone(s.direct, 30000)) >= 0, 'server did not expire grace');
    px.block = false;
    const res = await waitResult(s.page, 15000);
    assert(res.cls === 'result err' && /no longer works/.test(res.text), `outcome ${JSON.stringify(res)}`);
    let gone = null;
    for (let i = 0; i < 40 && !gone; i++) {
      const resp = await rp.goto(s.direct);
      if (resp.status() === 410) gone = await rp.textContent('h2');
      else await sleep(500);
    }
    assert(gone === 'Sender stopped', `page after expiry: ${gone}`);
  } finally {
    px.close();
    for (const p of [rp, s && s.page]) if (p) await p.context().close().catch(() => {});
  }
});

await test('native sharing: return guidance, cancellation and Copy fallback', async () => {
  const page = await newPage({ viewport: { width: 390, height: 844 }, clipboard: BASE_GRACE });
  const src = mkfile('share.bin', 1 << 20);
  try {
    await page.addInitScript(() => {
      window.__shares = [];
      window.__shareMode = 'abort';
      Object.defineProperty(navigator, 'share', { configurable: true, value: (data) => {
        window.__shares.push(data);
        if (window.__shareMode === 'abort') return Promise.reject(new DOMException('cancelled', 'AbortError'));
        if (window.__shareMode === 'fail') return Promise.reject(new Error('unavailable'));
        return Promise.resolve();
      } });
    });
    await page.goto(BASE_GRACE + '/');
    await page.setInputFiles('#file', src);
    await page.waitForSelector('#x-links:not([hidden])');
    const url = (await linkRows(page))[3].line;
    assert(await page.isVisible('#x-share'), 'native sharing button missing');
    const shareBox = await page.locator('#x-share').boundingBox();
    assert(shareBox.height >= 44 && shareBox.height <= 48, `Link button wraps: ${shareBox.height}`);
    assert(!(await page.isVisible('#x-copy')), 'duplicate sharing actions on mobile');
    assert(/After sharing, return to this browser page/.test(await page.textContent('#x-share-hint')), 'return instructions missing');
    const bad = await layoutChecks(page);
    assert(bad.length === 0, bad.join('; '));
    await page.locator('main > section').first().screenshot({ path: path.join(OUT, 'share-native-390.png') });
    for (const mode of ['abort', 'ok', 'fail']) {
      await page.evaluate((mode) => { window.__shareMode = mode; }, mode);
      await page.click('#x-share');
      await page.waitForFunction(() => !document.getElementById('x-share').disabled);
      assert(await page.isVisible('#x-links'), 'sharing cancelled the link');
      if (mode === 'fail') {
        assert(/Use Copy/.test(await page.textContent('#x-share-status')), 'Copy fallback missing');
        assert(await page.isVisible('#x-copy') && !(await page.isVisible('#x-share')), 'Copy fallback does not replace Share');
        await page.click('#x-copy');
        await page.waitForFunction(() => document.getElementById('x-copy').textContent === 'Copied');
        assert((await page.evaluate(() => navigator.clipboard.readText())) === url, 'Copy fallback differs from public link');
      }
    }
    const shares = await page.evaluate(() => window.__shares);
    assert(shares.length === 3 && shares.every((s) => s.url === url && !s.files), `sharing ${JSON.stringify(shares)}`);
    const recv = path.join(OUT, 'recv-share');
    const r = await sh(`curl -sS -f -OJ "${url}"`, recv).done;
    assert(r.code === 0, `download after sharing: ${r.err}`);
    assert(sha(fs.readFileSync(path.join(recv, 'share.bin'))) === sha(fs.readFileSync(src)), 'file mismatch after sharing');
    assert((await waitResult(page)).cls === 'result ok', 'upload failed after sharing');
    noErrors(page, 'share', PUT_FAILED);
  } finally { await page.context().close(); }
});

await test('foreground return recovers a silent stalled connection', async () => {
  const px = await severProxy(BASE_GRACE);
  const page = await newPage();
  const src = mkfile('silent.bin', 1 << 20);
  try {
    await page.addInitScript(() => {
      const original = window.fetch;
      window.fetch = (url, opts) => {
        const r = original(url, opts);
        if (url === '/' && opts?.method === 'POST' && JSON.parse(opts.body).name) {
          return r.then(async (res) => {
            const reader = res.body.getReader(), first = await reader.read();
            window.__heldReader = reader;
            return new Response(new ReadableStream({ start(c) { c.enqueue(first.value); } }), {
              headers: { 'Content-Type': 'application/x-ndjson' }
            });
          });
        }
        return r;
      };
      const XHR = window.XMLHttpRequest;
      window.XMLHttpRequest = function () {
        const xhr = new XHR();
        window.__xhr = xhr;
        return xhr;
      };
    });
    await page.goto(px.url + '/');
    await page.setInputFiles('#file', src);
    await page.waitForSelector('#x-links:not([hidden])');
    const url = (await linkRows(page))[3].line;
    const direct = url.replace(px.url, BASE_GRACE);
    await page.evaluate(() => { window.__xhr.onerror = null; });
    px.sever();
    await sleep(1500);
    assert((await page.textContent('#x-status')) === WAITING, 'simulation notified the UI of connection loss');
    const upload = page.waitForRequest((r) => r.method() === 'PUT' && r.url() === url);
    await page.evaluate(() => window.dispatchEvent(new Event('pageshow')));
    await upload;
    const recv = path.join(OUT, 'recv-silent');
    const r = await sh(`curl -sS -f -OJ "${direct}"`, recv).done;
    assert(r.code === 0, `download after return: ${r.err}`);
    assert(sha(fs.readFileSync(path.join(recv, 'silent.bin'))) === sha(fs.readFileSync(src)), 'file mismatch after return');
    assert((await waitResult(page)).cls === 'result ok', 'foreground return did not recover the session');
    noErrors(page, 'silent resume', PUT_FAILED);
  } finally { px.close(); await page.context().close(); }
});

await test('native sharing unavailable keeps Copy usable', async () => {
  const page = await newPage({ clipboard: BASE });
  try {
    await page.addInitScript(() => Object.defineProperty(navigator, 'share', { configurable: true, value: undefined }));
    await page.goto(BASE + '/');
    await page.setInputFiles('#file', mkfile('copy-only.bin', 100));
    await page.waitForSelector('#x-links:not([hidden])');
    assert(!(await page.isVisible('#x-share')), 'sharing shown without support');
    const url = (await linkRows(page))[3].line;
    await page.click('[data-copy="x-c3"]');
    await page.waitForFunction(() => document.querySelector('[data-copy="x-c3"]').textContent === 'Copied');
    assert((await page.evaluate(() => navigator.clipboard.readText())) === url, 'copy fallback not working');
    await page.click('#x-cancel');
  } finally { await page.context().close(); }
});

await test('unknown link page', async () => {
  const page = await newPage();
  const resp = await page.goto(BASE + '/AbC123xY9z');
  assert(resp.status() === 404 && (await page.textContent('h2')) === 'Link not found', `404 ${resp.status()}`);
  noErrors(page, '404', LOAD_4XX);
  await page.context().close();
});

await test('pipe upload landing shows unknown size', async () => {
  const up = sh(`bash -c 'head -c 100000 /dev/urandom | curl -sS -N -T - "${BASE}/stream.bin"'`);
  let exited = null;
  up.done.then((r) => { exited = r; });
  const sender = { exited: () => exited, out: () => up.out };
  let url = '';
  for (let i = 0; i < 50 && !url; i++) {
    await sleep(100);
    const m = /\| direct +\| (\S+) +\|/.exec(up.out);
    if (m) url = m[1];
  }
  assert(url, `curl table missing direct row: ${up.out}`);
  const page = await newPage();
  const resp = await page.goto(url);
  assert(resp.status() === 200, `page ${resp.status()}`);
  const info = await page.$$eval('.file dd', (d) => d.map((x) => x.textContent));
  assert(info[0] === 'stream.bin' && info[1] === 'unknown (streamed by the sender)', `details ${info}`);
  let dl;
  try {
    [dl] = await Promise.all([page.waitForEvent('download', { timeout: 30000 }), page.click('#dl button')]);
  } catch (e) {
    await diagnose(page, 'pipe', sender);
    throw e;
  }
  const saved = path.join(OUT, 'recv-stream.bin');
  await dl.saveAs(saved);
  assert(fs.statSync(saved).size === 100000, `size ${fs.statSync(saved).size}`);
  const r = await up.done;
  assert(r.code === 0 && /OK: download completed, 100000 bytes/.test(r.out), `sender ${r.code} ${r.out.slice(-200)}`);
  noErrors(page, 'pipe');
  await page.context().close();
});

// Basic auth server (BASE_AUTH, user u, password p): the page reports missing credentials;
// browser credentials allow uploads (PUT uses the token, not the credentials).
{
  const BA = BASE_AUTH;
  await test('login required for the browser sender', async () => {
    const page = await newPage();
    // The browser prompts for credentials in its dialog; simulate the user cancelling.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Fetch.enable', { handleAuthRequests: true, patterns: [{ urlPattern: '*' }] });
    let asked = 0;
    cdp.on('Fetch.requestPaused', (e) => cdp.send('Fetch.continueRequest', { requestId: e.requestId }).catch(() => {}));
    cdp.on('Fetch.authRequired', (e) => { asked++; cdp.send('Fetch.continueWithAuth', { requestId: e.requestId, authChallengeResponse: { response: 'CancelAuth' } }).catch(() => {}); });
    await page.goto(BA + '/');
    await page.setInputFiles('#file', mkfile('auth-none.bin', 1000));
    const res = await waitResult(page, 10000);
    assert(asked === 1, `login requests ${asked}`);
    assert(res.cls === 'result err' && res.text === 'This server requires a login to send files.', `outcome ${JSON.stringify(res)}`);
    noErrors(page, 'auth none', /status of 401/);
    await page.context().close();
  });
  await test('browser sender with credentials', async () => {
    const size = 2 << 20;
    const src = mkfile('auth-ok.bin', size);
    const page = await newPage({ creds: { username: 'u', password: 'p' } });
    await page.goto(BA + '/');
    await page.setInputFiles('#file', src);
    await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
    const curl = (await linkRows(page))[0].line;
    const dir = path.join(OUT, 'recv-auth');
    const r = await sh(curl, dir).done;
    assert(r.code === 0, `curl receiver ${r.code} ${r.err}`);
    assert(sha(fs.readFileSync(path.join(dir, 'auth-ok.bin'))) === sha(fs.readFileSync(src)), 'content mismatch');
    const res = await waitResult(page);
    assert(res.cls === 'result ok', `outcome ${JSON.stringify(res)}`);
    noErrors(page, 'auth ok');
    await page.context().close();
  });
}

await browser.close();
const failed = results.filter((r) => r[1] !== 'PASS');
console.log(`\n${TAG}: ${results.length - failed.length}/${results.length} PASS`);
process.exit(failed.length ? 1 : 0);
