// Test frontend di bashtransfer con Playwright (Chromium): pagine, invio dal browser, download dalla
// pagina, esiti e annullamento. Si lancia con scripts/ui/run.sh, che avvia i server.
// Env: BASE (server, attesa lunga), BASE_SHORT (attesa 3s), BASE_AUTH (Basic auth u:p), BASE_GRACE
// (BASHT_SENDER_GRACE=8, tolleranza del mittente), OUT (cartella
// vuota per file e screenshot), TAG (nome della prova), ONLY (regex dei nomi dei test), CHROME
// (eseguibile Chromium al posto di quello installato da Playwright), EXCLUDE_HOSTS (host raggiungibili
// oltre a 127.0.0.1 e localhost, separati da virgola).
import { chromium } from 'playwright-core';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import net from 'node:net';

for (const k of ['BASE', 'BASE_SHORT', 'BASE_AUTH', 'BASE_GRACE', 'OUT']) {
  if (!process.env[k]) { console.error(`${k} non impostata: usare scripts/ui/run.sh`); process.exit(2); }
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

// Esegue una riga di shell (exec: kill raggiunge il programma) in cwd.
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

// Nessuna connessione fuori dai server di test: niente servizi Google in background, e ogni nome
// diverso dagli host di test (EXCLUDE_HOSTS, separati da virgola) non si risolve.
const allowed = ['127.0.0.1', 'localhost', ...(process.env.EXCLUDE_HOSTS || '').split(',').filter(Boolean)];
// channel 'chromium': il Chromium completo in modalità headless, non la headless shell.
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
  // Traccia di rete con orario ISO (come i log dei server): serve a diagnosticare i fallimenti.
  page.net = [];
  page.on('response', (r) => page.net.push(`${new Date().toISOString()} ${r.status()} ${r.request().method()} ${r.url()}`));
  page.on('requestfailed', (r) => page.net.push(`${new Date().toISOString()} FAILED ${r.method()} ${r.url()} ${r.failure()?.errorText}`));
  page.on('download', (d) => page.net.push(`${new Date().toISOString()} DOWNLOAD ${d.url()}`));
  return page;
}

// Stampa e salva in OUT lo stato della pagina e del mittente su un fallimento, per il runner (il
// file <name>-fail.json e lo screenshot finiscono nell'artifact ui).
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
// Proxy TCP davanti a target che taglia le connessioni su comando, come un telefono che perde la
// rete: sever() chiude tutte le connessioni aperte, block impedisce di aprirne di nuove.
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

// allow: errori attesi, per esempio il caricamento di una pagina 404/410 o il PUT interrotto.
function noErrors(page, what, allow) {
  const errs = page.errors.filter((e) => !(allow && allow.test(e)));
  assert(errs.length === 0, `${what}: errori console ${JSON.stringify(errs)}`);
}
const LOAD_4XX = /^Failed to load resource: the server responded with a status of 4(04|10) /;
const PUT_FAILED = /^Failed to load resource: net::ERR_|^Failed to load resource: the server responded with a status of 404 /;

// Controlli di allineamento: su desktop campo e pulsante della stessa riga alla stessa altezza,
// pulsanti di copia della stessa larghezza; su telefono il pulsante non schiaccia il comando: sta
// sopra al campo negli elenchi e sotto nei comandi singoli, con un'area di tocco adeguata. I
// comandi vanno a capo invece di scorrere in orizzontale, a ogni larghezza.
async function layoutChecks(page) {
  return page.evaluate(() => {
    const bad = [];
    const r = (el) => el.getBoundingClientRect();
    if (document.documentElement.scrollWidth > innerWidth) bad.push(`scroll orizzontale ${document.documentElement.scrollWidth} > ${innerWidth}`);
    const narrow = matchMedia('(max-width: 40rem)').matches;
    const copies = [...document.querySelectorAll('.copy')].filter((b) => b.offsetParent);
    const widths = new Set(copies.map((b) => Math.round(r(b).width)));
    if (widths.size > 1) bad.push(`larghezze pulsanti copia ${[...widths]}`);
    for (const b of copies) {
      const f = document.getElementById(b.dataset.copy);
      if (!f) continue;
      const a = r(f), c = r(b);
      if (narrow) {
        // Su telefono i Copy sono compatti e centrati sotto o accanto al comando.
        if (c.width < 43 || c.height < 34) bad.push(`pulsante copia troppo piccolo ${b.dataset.copy}: ${c.width}x${c.height}`);
        if (!(c.bottom <= a.top + 1 || c.top >= a.bottom - 1)) bad.push(`pulsante sovrapposto ${b.dataset.copy}`);
      } else {
        if (Math.abs(a.top - c.top) > 1 || Math.abs(a.height - c.height) > 1) bad.push(`riga ${b.dataset.copy}: campo ${a.top}/${a.height} pulsante ${c.top}/${c.height}`);
        if (c.left < a.right) bad.push(`pulsante sovrapposto ${b.dataset.copy}`);
      }
    }
    for (const f of document.querySelectorAll('.field')) {
      if (f.offsetParent && f.scrollWidth > f.clientWidth + 1) bad.push(`comando non va a capo: ${f.textContent} (${f.scrollWidth} > ${f.clientWidth})`);
    }
    // Campi di ogni griglia allineati a sinistra.
    for (const g of document.querySelectorAll('.rows, .cmd')) {
      const lefts = new Set([...g.querySelectorAll('.field')].map((f) => Math.round(r(f).left)));
      if (lefts.size > 1) bad.push(`campi non allineati in ${g.className}: ${[...lefts]}`);
    }
    return bad;
  });
}

async function linkRows(page) {
  return page.$$eval('#x-rows .label', (ls) => ls.map((l) => ({ label: l.textContent, line: l.nextElementSibling.textContent })));
}

// Pattern deterministico per i file creati nella pagina.
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

// --- Test ---

for (const [w, dark, font] of [[1280, false], [1280, true], [390, false], [390, true], [320, false], [320, false, 'Arial, sans-serif']]) {
  await test(`home layout ${w} ${dark ? 'dark' : 'light'}${font ? ' fallback font' : ''}`, async () => {
    const page = await newPage({ viewport: { width: w, height: 900 }, dark });
    const resp = await page.goto(BASE + '/');
    assert(resp.status() === 200, `stato ${resp.status()}`);
    if (font) await page.addStyleTag({ content: `body { font-family: ${font}; }` });
    await page.screenshot({ path: path.join(OUT, `home-${w}-${dark ? 'dark' : 'light'}${font ? '-fallback-font' : ''}.png`), fullPage: true });
    const first = await page.$eval('main > section', (s) => ({ h2: s.querySelector('h2').textContent, drop: !!s.querySelector('#drop') }));
    assert(first.drop && /Send a file/.test(first.h2), `prima sezione ${JSON.stringify(first)}`);
    const box = await page.$eval('#drop', (d) => d.getBoundingClientRect().toJSON());
    assert(box.height >= 150 && box.height <= 280, `altezza riquadro ${box.height}`);
    const pick = await page.$eval('#pick', (b) => ({ ...b.getBoundingClientRect().toJSON(), text: b.textContent }));
    assert(pick.text === 'Upload' && pick.top > box.top && pick.bottom < box.bottom, `pulsante Upload ${JSON.stringify(pick)}`);
    const bad = await layoutChecks(page);
    assert(bad.length === 0, bad.join('; '));
    const roles = await page.$$eval('h2', (hs) => hs.map((h) => h.textContent.trim()));
    assert(roles.some((r) => r.startsWith('Sender') && r.includes('terminal')) && roles.some((r) => r.startsWith('Receiver')), `titoli ${roles}`);
    noErrors(page, 'home');
    await page.context().close();
  });
}

await test('home copy button', async () => {
  const page = await newPage({ clipboard: BASE });
  await page.goto(BASE + '/');
  await page.click('[data-copy="c-send"]');
  // L'etichetta cambia quando la scrittura negli appunti (asincrona) è riuscita.
  await page.waitForFunction(() => document.querySelector('[data-copy="c-send"]').textContent !== 'Copy', null, { timeout: 2000 }).catch(() => {});
  const txt = await page.textContent('[data-copy="c-send"]');
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  assert(txt === 'Copied', `etichetta ${txt}`);
  assert(clip === `curl ${BASE} -T FILE`, `appunti ${clip}`);
  await sleep(1700);
  assert((await page.textContent('[data-copy="c-send"]')) === 'Copy', 'etichetta non ripristinata');
  noErrors(page, 'home copy');
  await page.context().close();
});

await test('home layout 390 after upload: link commands wrap', async () => {
  const src = mkfile('mobile.bin', 1 << 20);
  const page = await newPage({ viewport: { width: 390, height: 844 } });
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
  const bad = await layoutChecks(page);
  assert(bad.length === 0, bad.join('; '));
  await page.screenshot({ path: path.join(OUT, 'upload-390.png'), fullPage: true });
  await page.click('#x-cancel');
  await waitResult(page, 5000);
  noErrors(page, 'upload 390', PUT_FAILED);
  await page.context().close();
});

await test('upload button + curl receiver from the table', async () => {
  const size = 24 << 20;
  const src = mkfile('upload.bin', size);
  const page = await newPage({ clipboard: BASE });
  await page.goto(BASE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
  const rows = await linkRows(page);
  assert(rows.map((r) => r.label).join(',') === 'curl,wget,aria2c,direct', `etichette ${JSON.stringify(rows)}`);
  const url = rows[3].line;
  assert(url.startsWith(BASE + '/') && url.length > BASE.length + 5, `direct ${url}`);
  assert(rows[0].line === `curl -f -OJ "${url}"`, `riga curl ${rows[0].line}`);
  assert(rows[1].line === `wget --content-disposition "${url}"`, `riga wget ${rows[1].line}`);
  assert(rows[2].line === `aria2c -x1 -s1 --allow-overwrite=false "${url}"`, `riga aria2c ${rows[2].line}`);
  assert((await page.textContent('#x-name')) === 'upload.bin' && (await page.textContent('#x-size')) === '24.0 MiB', 'nome/dimensione');
  // Su un telefono passare a un'altra app ferma l'invio: l'avviso lo dice accanto ai link.
  const hint = await page.textContent('#x-links');
  assert(/Keep this page open and in the foreground until the end/.test(hint) && /split screen/.test(hint), `avviso primo piano: ${hint}`);
  // Copia del link diretto.
  await page.click('[data-copy="x-c3"]');
  await page.waitForFunction(() => document.querySelector('[data-copy="x-c3"]').textContent === 'Copied', null, { timeout: 2000 });
  assert((await page.evaluate(() => navigator.clipboard.readText())) === url, 'copia link diretto');
  await sleep(2600);
  const waitStatus = await page.textContent('#x-status');
  assert(waitStatus === 'Waiting for the receiver to open the link…', `stato in attesa: ${waitStatus}`);
  // Prima del ricevente nessun avanzamento: i byte nei buffer di rete non sono consegnati.
  const waitPct = await page.textContent('#x-pct');
  assert(waitPct === '0%', `avanzamento senza ricevente: ${waitPct}`);
  const bad = await layoutChecks(page);
  assert(bad.length === 0, bad.join('; '));
  await page.screenshot({ path: path.join(OUT, 'upload-waiting.png'), fullPage: true });
  // Ricevente: il comando curl della tabella, rallentato per osservare il progresso.
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
  console.log('     campioni:', samples.map((s) => `${s.pct} ${s.rate} [${s.amount}] ${s.status}`).join(' | '));
  const mid = samples.filter((s) => s.now > 0 && s.now < 100);
  assert(mid.length > 0, 'nessun campione intermedio');
  assert(mid.some((s) => /^\d+\.\d (KiB|MiB)\/s$/.test(s.rate)), 'velocità assente');
  assert(mid.some((s) => s.status === 'Sending to the receiver…'), 'stato invio assente');
  assert(mid.some((s) => /left$/.test(s.amount)), 'tempo residuo assente');
  for (let i = 1; i < samples.length; i++) assert(samples[i].now >= samples[i - 1].now, 'progresso non monotono');
  const r = await rcv.done;
  assert(r.code === 0, `curl ricevente: ${r.code} ${r.err}`);
  const res = await waitResult(page);
  assert(res.cls === 'result ok' && /^Done: the receiver got the whole file \(24\.0 MiB in \d/.test(res.text), `esito ${JSON.stringify(res)}`);
  assert(await page.$eval('#x-links', (e) => e.hidden), 'link ancora visibili');
  assert((await page.getAttribute('#x-bar', 'aria-valuenow')) === '100', 'barra non al 100');
  const got = fs.readFileSync(path.join(recvDir, 'upload.bin'));
  assert(sha(got) === sha(fs.readFileSync(src)), 'contenuto ricevuto diverso');
  await page.screenshot({ path: path.join(OUT, 'upload-done.png'), fullPage: true });
  await page.click('#x-again');
  assert(await page.isVisible('#drop') && !(await page.isVisible('#xfer')), 'Send another file non ripristina');
  noErrors(page, 'upload');
  await page.context().close();
});

await test('drag and drop + browser receiver on the download page', async () => {
  const size = 3 << 20;
  const name = 'relazione finale ü.bin';
  const page = await newPage();
  await page.goto(BASE + '/');
  await dropFiles(page, [{ name, size }]);
  assert(await page.evaluate(() => window.__over), 'classe over assente su dragenter');
  await page.waitForSelector('#x-links:not([hidden])', { timeout: 10000 });
  assert(!(await page.$eval('#drop', (d) => d.classList.contains('over'))), 'classe over rimasta');
  const url = (await linkRows(page))[3].line;
  // Anteprima in scuro su telefono: non consuma il link.
  const peek = await newPage({ viewport: { width: 390, height: 844 }, dark: true });
  await peek.goto(url);
  await peek.screenshot({ path: path.join(OUT, 'download-390-dark.png'), fullPage: true });
  assert((await layoutChecks(peek)).length === 0, 'layout download 390');
  noErrors(peek, 'download 390');
  await peek.context().close();
  const rp = await newPage();
  const resp = await rp.goto(url);
  assert(resp.status() === 200, `pagina download ${resp.status()}`);
  const info = await rp.$$eval('.file dd', (d) => d.map((x) => x.textContent));
  assert(info[0] === name && info[1] === '3.0 MiB (3,145,728 bytes)', `dati file ${info}`);
  const tools = await rp.$$eval('.rows.tools .label', (l) => l.map((x) => x.textContent));
  assert(tools.join(',') === 'curl,wget,aria2c', `comandi pagina download ${tools}`);
  const bad = await layoutChecks(rp);
  assert(bad.length === 0, bad.join('; '));
  await rp.screenshot({ path: path.join(OUT, 'download-1280.png'), fullPage: true });
  const [dl] = await Promise.all([rp.waitForEvent('download', { timeout: 15000 }), rp.click('#dl button')]);
  assert(dl.suggestedFilename() === name, `nome suggerito ${dl.suggestedFilename()}`);
  const saved = path.join(OUT, 'recv-browser.bin');
  await dl.saveAs(saved);
  assert(sha(fs.readFileSync(saved)) === sha(pattern(size)), 'contenuto scaricato diverso');
  const btn = await rp.$eval('#dl button', (b) => ({ dis: b.disabled, t: b.textContent }));
  assert(btn.dis && btn.t === 'Download requested', `pulsante dopo il click ${JSON.stringify(btn)}`);
  const res = await waitResult(page);
  assert(res.cls === 'result ok', `esito mittente ${JSON.stringify(res)}`);
  noErrors(rp, 'download');
  noErrors(page, 'upload drop');
  const again = await rp.goto(url);
  assert(again.status() === 410 && /Link already used or expired/.test(await rp.textContent('h2')), `link usato ${again.status()}`);
  await rp.screenshot({ path: path.join(OUT, 'message-410.png'), fullPage: true });
  noErrors(rp, 'pagina 410', LOAD_4XX);
  await page.context().close();
  await rp.context().close();
});

await test('drop of several files is refused', async () => {
  const page = await newPage();
  await page.goto(BASE + '/');
  await dropFiles(page, [{ name: 'a.txt', size: 10 }, { name: 'b.txt', size: 10 }]);
  await sleep(300);
  assert((await page.textContent('#drop-error')) === 'Drop one file at a time.' && (await page.isVisible('#drop-error')), 'messaggio errore');
  assert(!(await page.isVisible('#xfer')), 'upload partito');
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
  assert(res.cls === 'result err' && res.text === 'Cancelled: the link no longer works.', `esito ${JSON.stringify(res)}`);
  const ms = await waitGone(url, 1000);
  assert(ms >= 0, 'link ancora valido 1 s dopo Cancel');
  console.log(`     link 410 ${ms} ms dopo Cancel`);
  // Il ricevente che apre il link dopo Cancel non vede "già usato": nessuno ha scaricato nulla.
  const rp = await newPage();
  const rr = await rp.goto(url);
  assert(rr.status() === 410 && (await rp.textContent('h2')) === 'Sender stopped', `ricevente dopo Cancel ${rr.status()} ${await rp.textContent('h2')}`);
  await rp.context().close();
  await page.screenshot({ path: path.join(OUT, 'upload-cancelled.png'), fullPage: true });
  noErrors(page, 'cancel', PUT_FAILED);
  await page.context().close();
});

// Stato del link senza consumarlo: la pagina di download (Accept: text/html) è 200 o 410.
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
  // Come l'utente: chiude la scheda e conferma l'avviso di beforeunload.
  let asked = false;
  page.on('dialog', (d) => { asked = d.type() === 'beforeunload'; d.accept(); });
  await Promise.all([page.waitForEvent('close'), page.close({ runBeforeUnload: true })]);
  assert(asked, 'nessun avviso beforeunload durante un invio');
  const ms = await waitGone(url, 3000);
  assert(ms >= 0, 'link ancora valido 3 s dopo la chiusura della pagina');
  console.log(`     link 410 ${ms} ms dopo la chiusura`);
  await page.context().close();
});

await test('without the DELETE the grace period closes the link', async () => {
  const src = mkfile('crashed.bin', 24 << 20);
  const page = await newPage();
  await page.goto(BASE_GRACE + '/');
  await page.setInputFiles('#file', src);
  await page.waitForSelector('#x-links:not([hidden])');
  const url = (await linkRows(page))[3].line;
  // Senza l'annullamento esplicito (crash, DELETE perso): resta la chiusura dello stream e del PUT.
  await page.evaluate(() => {
    const f = window.fetch;
    window.__deletes = 0;
    window.fetch = (u, o) => (o && o.method === 'DELETE' ? (window.__deletes++, Promise.resolve(new Response(null))) : f(u, o));
  });
  await sleep(1500);
  await page.click('#x-cancel');
  assert((await page.evaluate(() => window.__deletes)) === 1, 'Cancel senza DELETE');
  const ms = await waitGone(url, 30000);
  assert(ms >= 0, 'link ancora valido dopo la tolleranza');
  console.log(`     link 410 ${ms} ms dopo la chiusura dello stream, senza DELETE`);
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
  assert(r, 'ricevente non interrotto entro 8 s');
  assert(r.code !== 0, `ricevente uscito con 0: ${r.out}`);
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
  assert(res.cls === 'result err' && res.text === 'No receiver opened the link in time: the link is closed.', `esito ${JSON.stringify(res)}`);
  const rp = await newPage();
  const rr = await rp.goto(url);
  assert(rr.status() === 410 && (await rp.textContent('h2')) === 'Link expired', `ricevente dopo la scadenza ${rr.status()} ${await rp.textContent('h2')}`);
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
  assert(res.cls === 'result err' && res.text === 'The receiver disconnected or stopped downloading.', `esito ${JSON.stringify(res)}`);
  await page.screenshot({ path: path.join(OUT, 'upload-receiver-gone.png'), fullPage: true });
  noErrors(page, 'receiver gone', PUT_FAILED);
  await page.context().close();
});

// Tolleranza del mittente (BASHT_SENDER_GRACE=8 su BASE_GRACE). La pagina del mittente passa da un
// proxy che taglia le connessioni, come un telefono che perde la rete in secondo piano.
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
const WAITING = 'Waiting for the receiver to open the link…';

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
    assert(sha(fs.readFileSync(path.join(OUT, 'grace1', 'grace1.bin'))) === sha(fs.readFileSync(s.src)), 'file ricevuto diverso');
    const res = await waitResult(s.page, 20000);
    assert(res.cls === 'result ok' && /^Done: the receiver got the whole file/.test(res.text), `esito ${JSON.stringify(res)}`);
    noErrors(s.page, 'grace reconnect', PUT_FAILED);
  } finally { px.close(); if (s) await s.page.context().close().catch(() => {}); }
});

await test('sender grace: a browser receiver pressing Download while the sender is away', async () => {
  const px = await severProxy(BASE_GRACE);
  let s, rp;
  try {
    s = await graceSender(px, 'grace2.bin', 24 << 20);
    await sleep(1000);
    // Nessun nuovo collegamento: il mittente resta assente finché il ricevente non ha premuto Download.
    px.block = true;
    px.sever();
    rp = await newPage();
    let away = false;
    for (let i = 0; i < 40 && !away; i++) {
      const resp = await rp.goto(s.direct);
      assert(resp.status() === 200, `pagina ${resp.status()}`);
      away = await rp.isVisible('#dl-away');
      if (!away) await sleep(500);
    }
    assert(away, 'la pagina di download non avvisa che il mittente è assente');
    assert(await rp.isDisabled('#dl button'), 'il download è disponibile con il mittente assente');
    assert(/link has not been used/.test(await rp.textContent('#dl-status')), 'lo stato non spiega che il link resta intatto');
    assert((await fetch(s.direct, { headers: { Accept: 'text/html' } })).status === 200, 'l’attesa ha consumato il link');
    px.block = false;
    await rp.waitForFunction(() => !document.querySelector('#dl button').disabled, null, { timeout: 15000 });
    assert(!(await rp.isVisible('#dl-away')), 'lo stato non si aggiorna al ritorno del mittente');
    const [dl] = await Promise.all([rp.waitForEvent('download', { timeout: 30000 }), rp.click('#dl button')]);
    const saved = path.join(OUT, 'recv-grace2.bin');
    await dl.saveAs(saved);
    assert(sha(fs.readFileSync(saved)) === sha(fs.readFileSync(s.src)), 'file ricevuto diverso');
    const res = await waitResult(s.page, 20000);
    assert(res.cls === 'result ok', `esito ${JSON.stringify(res)}`);
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
    assert(await rp.isDisabled('#dl button'), 'il pulsante resta attivo dopo la scadenza');
    assert((await waitGone(s.direct, 30000)) >= 0, 'il server non ha fatto scadere la tolleranza');
    px.block = false;
    const res = await waitResult(s.page, 15000);
    assert(res.cls === 'result err' && /no longer works/.test(res.text), `esito ${JSON.stringify(res)}`);
    let gone = null;
    for (let i = 0; i < 40 && !gone; i++) {
      const resp = await rp.goto(s.direct);
      if (resp.status() === 410) gone = await rp.textContent('h2');
      else await sleep(500);
    }
    assert(gone === 'Sender stopped', `pagina dopo la scadenza: ${gone}`);
  } finally {
    px.close();
    for (const p of [rp, s && s.page]) if (p) await p.context().close().catch(() => {});
  }
});

await test('native sharing: return guidance, cancellation and Copy fallback', async () => {
  const page = await newPage({ viewport: { width: 390, height: 844 } });
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
    assert(await page.isVisible('#x-share'), 'pulsante di condivisione nativa assente');
    assert(/After sharing, return to this browser page/.test(await page.textContent('#x-share-hint')), 'istruzioni di ritorno assenti');
    for (const mode of ['abort', 'fail', 'ok']) {
      await page.evaluate((mode) => { window.__shareMode = mode; }, mode);
      await page.click('#x-share');
      await page.waitForFunction(() => !document.getElementById('x-share').disabled);
      assert(await page.isVisible('#x-links'), 'la condivisione ha annullato il link');
      if (mode === 'fail') assert(/Use Copy/.test(await page.textContent('#x-share-status')), 'fallback Copy assente');
    }
    const shares = await page.evaluate(() => window.__shares);
    assert(shares.length === 3 && shares.every((s) => s.url === url && !s.files), `condivisione ${JSON.stringify(shares)}`);
    const recv = path.join(OUT, 'recv-share');
    const r = await sh(`curl -sS -f -OJ "${url}"`, recv).done;
    assert(r.code === 0, `download dopo la condivisione: ${r.err}`);
    assert(sha(fs.readFileSync(path.join(recv, 'share.bin'))) === sha(fs.readFileSync(src)), 'file dopo la condivisione diverso');
    assert((await waitResult(page)).cls === 'result ok', 'invio dopo la condivisione fallito');
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
    assert((await page.textContent('#x-status')) === WAITING, 'la simulazione ha notificato la perdita alla UI');
    const upload = page.waitForRequest((r) => r.method() === 'PUT' && r.url() === url);
    await page.evaluate(() => window.dispatchEvent(new Event('pageshow')));
    await upload;
    const recv = path.join(OUT, 'recv-silent');
    const r = await sh(`curl -sS -f -OJ "${direct}"`, recv).done;
    assert(r.code === 0, `download dopo il ritorno: ${r.err}`);
    assert(sha(fs.readFileSync(path.join(recv, 'silent.bin'))) === sha(fs.readFileSync(src)), 'file dopo il ritorno diverso');
    assert((await waitResult(page)).cls === 'result ok', 'il ritorno in primo piano non ha recuperato la sessione');
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
    assert(!(await page.isVisible('#x-share')), 'condivisione mostrata senza supporto');
    const url = (await linkRows(page))[3].line;
    await page.click('[data-copy="x-c3"]');
    await page.waitForFunction(() => document.querySelector('[data-copy="x-c3"]').textContent === 'Copied');
    assert((await page.evaluate(() => navigator.clipboard.readText())) === url, 'fallback di copia non funzionante');
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
  assert(url, `tabella curl senza riga direct: ${up.out}`);
  const page = await newPage();
  const resp = await page.goto(url);
  assert(resp.status() === 200, `pagina ${resp.status()}`);
  const info = await page.$$eval('.file dd', (d) => d.map((x) => x.textContent));
  assert(info[0] === 'stream.bin' && info[1] === 'unknown (streamed by the sender)', `dati ${info}`);
  let dl;
  try {
    [dl] = await Promise.all([page.waitForEvent('download', { timeout: 30000 }), page.click('#dl button')]);
  } catch (e) {
    await diagnose(page, 'pipe', sender);
    throw e;
  }
  const saved = path.join(OUT, 'recv-stream.bin');
  await dl.saveAs(saved);
  assert(fs.statSync(saved).size === 100000, `dimensione ${fs.statSync(saved).size}`);
  const r = await up.done;
  assert(r.code === 0 && /OK: download completato, 100000 byte/.test(r.out), `mittente ${r.code} ${r.out.slice(-200)}`);
  noErrors(page, 'pipe');
  await page.context().close();
});

// Server con Basic auth (BASE_AUTH, utente u, password p): senza credenziali la pagina lo dice,
// con le credenziali del browser l'invio funziona (il PUT usa il token, non le credenziali).
{
  const BA = BASE_AUTH;
  await test('login required for the browser sender', async () => {
    const page = await newPage();
    // Il browser chiede utente e password con la sua finestra; qui l'utente preme Annulla.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Fetch.enable', { handleAuthRequests: true, patterns: [{ urlPattern: '*' }] });
    let asked = 0;
    cdp.on('Fetch.requestPaused', (e) => cdp.send('Fetch.continueRequest', { requestId: e.requestId }).catch(() => {}));
    cdp.on('Fetch.authRequired', (e) => { asked++; cdp.send('Fetch.continueWithAuth', { requestId: e.requestId, authChallengeResponse: { response: 'CancelAuth' } }).catch(() => {}); });
    await page.goto(BA + '/');
    await page.setInputFiles('#file', mkfile('auth-none.bin', 1000));
    const res = await waitResult(page, 10000);
    assert(asked === 1, `richieste di login ${asked}`);
    assert(res.cls === 'result err' && res.text === 'This server requires a login to send files.', `esito ${JSON.stringify(res)}`);
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
    assert(r.code === 0, `curl ricevente ${r.code} ${r.err}`);
    assert(sha(fs.readFileSync(path.join(dir, 'auth-ok.bin'))) === sha(fs.readFileSync(src)), 'contenuto diverso');
    const res = await waitResult(page);
    assert(res.cls === 'result ok', `esito ${JSON.stringify(res)}`);
    noErrors(page, 'auth ok');
    await page.context().close();
  });
}

await browser.close();
const failed = results.filter((r) => r[1] !== 'PASS');
console.log(`\n${TAG}: ${results.length - failed.length}/${results.length} PASS`);
process.exit(failed.length ? 1 : 0);
