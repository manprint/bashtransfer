#!/usr/bin/env python3
"""Invariante di bashtransfer: la sorgente non viene mai alterata.

Esegue i comandi stampati al mittente, le loro varianti sbagliate (flag omessi o aggiunti, file di
configurazione dell'utente) e i comandi di invio, con la sorgente (file, cartella, link simbolico)
proprio dove il ricevente salva: la sua cartella è quella del mittente. Per ogni caso confronta
un'istantanea della sorgente (contenuto, dimensione, permessi, mtime, ctime, inode) prima e dopo.

Ogni caso deve anche provare di aver esercitato lo stato dichiarato: un link valido risulta
consumato dalla variante, oppure ancora valido e scaricato subito dopo con il comando stampato;
gli stati di errore sono controllati prima e dopo. Un caso che non prova nulla è un errore.

uso: invariant.py BINARIO
    BINARIO             bashtransfer compilato (go build ./cmd/bashtransfer)
    BASHT_INV_REQUIRE   strumenti obbligatori, es. "curl,wget,aria2c,busybox": se uno manca il
                        test fallisce invece di saltarlo (CI)
    BASHT_INV_JOBS      casi in parallelo (default 8)
Esce con 1 se un caso altera la sorgente o non prova nulla. I limiti noti (README, "La sorgente
non viene mai alterata") sono eseguiti e riportati, senza esito.
"""

import concurrent.futures
import hashlib
import itertools
import json
import os
import pty
import re
import shlex
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

BIN = os.path.abspath(sys.argv[1])
TMP = tempfile.mkdtemp(prefix="bt-inv-")
JOBS = int(os.environ.get("BASHT_INV_JOBS", "8"))
REQUIRE = [t for t in os.environ.get("BASHT_INV_REQUIRE", "").split(",") if t]
SIZE = 1 << 20
NAME = "perché è.bin"
SHORT_WAIT = 2  # BASHT_WAIT_TIMEOUT del server per i link scaduti, in secondi

# Nessun proxy e nessuna configurazione dell'utente che lancia il test.
ENV = {k: v for k, v in os.environ.items()
       if not k.lower().endswith("_proxy") and k not in ("CURL_HOME", "WGETRC", "XDG_CONFIG_HOME")}
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
PROCS = []
PRINTED = {}  # "rows": tabella stampata per un link, modello dei comandi negli stati di errore
TABLE_ROW = re.compile(r"^\| (curl|wget|aria2c) +\| (.*?) +\|$", re.M)


def fail(msg):
    print("INVARIANT FAIL: " + msg, file=sys.stderr)
    sys.exit(1)


def status(url, headers=None, limit=None):
    """Status di un GET; con limit legge al più limit byte del body e chiude."""
    try:
        with OPENER.open(urllib.request.Request(url, headers=headers or {}), timeout=15) as r:
            r.read(limit) if limit else r.read()
            return r.status
    except urllib.error.HTTPError as e:
        return e.code


def probe(url):
    """Stato del link senza reclamarlo: 400 valido, 410 usato o scaduto, 404 sconosciuto."""
    return status(url, {"Range": "bytes=1-"})


def start_server(name, **env):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    log = open(os.path.join(TMP, name + ".log"), "wb")
    PROCS.append(subprocess.Popen([BIN], env=dict(ENV, BASHT_LISTEN=f"127.0.0.1:{port}", **env),
                                  stdout=log, stderr=subprocess.STDOUT))
    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        try:
            if status(base + "/") == 200:
                return base
        except OSError:
            time.sleep(0.1)
    fail(f"il server {name} non risponde")


def make_home(name, files):
    home = os.path.join(TMP, "home-" + name)
    for rel, content in files.items():
        os.makedirs(os.path.dirname(os.path.join(home, rel)), exist_ok=True)
        with open(os.path.join(home, rel), "w") as f:
            f.write(content)
    os.makedirs(home, exist_ok=True)
    return home


def tool_env(home):
    return dict(ENV, HOME=home, XDG_CONFIG_HOME=os.path.join(home, ".config"))


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 16), b""):
            h.update(chunk)
    return h.hexdigest()


def snapshot(path):
    """Istantanea di path, di ciò che contiene e, se è un link, di ciò a cui punta."""
    out = []

    def add(p, rel):
        try:
            st = os.lstat(p)
        except FileNotFoundError:
            out.append((rel, "MANCANTE"))
            return
        e = [rel, stat.S_IFMT(st.st_mode), st.st_mode, st.st_size, st.st_mtime_ns, st.st_ctime_ns,
             st.st_ino, st.st_nlink]
        if stat.S_ISREG(st.st_mode):
            e.append(sha256(p))
        elif stat.S_ISLNK(st.st_mode):
            e.append(os.readlink(p))
            add(os.path.realpath(p), rel + " ->")
        elif stat.S_ISDIR(st.st_mode):
            for n in sorted(os.listdir(p)):
                add(os.path.join(p, n), rel + "/" + n)
        out.append(tuple(e))

    add(path, os.path.basename(path))
    return out


_seq = itertools.count()
_seq_lock = threading.Lock()


def case_dir():
    with _seq_lock:
        n = next(_seq)
    d = os.path.join(TMP, f"c{n}")
    os.mkdir(d)
    return d


def make_source(kind, d, name):
    """Crea la sorgente name in d: file, cartella o link simbolico a un file fuori da d."""
    path = os.path.join(d, name)
    if kind == "file":
        with open(path, "wb") as f:
            f.write(os.urandom(SIZE))
    elif kind == "dir":
        os.makedirs(os.path.join(path, "sub"))
        with open(os.path.join(path, "a.bin"), "wb") as f:
            f.write(os.urandom(300000))
        with open(os.path.join(path, "sub", "b.txt"), "w") as f:
            f.write("ciao\n")
    else:
        target = os.path.join(os.path.dirname(d), os.path.basename(d) + "-fuori", "vera.bin")
        os.makedirs(os.path.dirname(target))
        with open(target, "wb") as f:
            f.write(os.urandom(SIZE))
        os.symlink(target, path)
    return path


class Sender:
    """Comando di invio (bash -c) avviato in d, in un proprio gruppo di processi."""

    def __init__(self, cmd, d, home, out):
        self.d, self.out = d, out
        # Uscita su un terminale, come per chi lo lancia a mano: verso un file curl la bufferizza e
        # la tabella comparirebbe solo alla fine.
        master, slave = pty.openpty()
        self.p = subprocess.Popen(["bash", "-c", cmd], cwd=d, env=tool_env(home), stdin=subprocess.DEVNULL,
                                  stdout=slave, stderr=slave, start_new_session=True)
        os.close(slave)
        self.copier = threading.Thread(target=self._copy, args=(master,), daemon=True)
        self.copier.start()

    def _copy(self, master):
        with open(self.out, "wb") as f:
            while True:
                try:
                    b = os.read(master, 65536)
                except OSError:
                    break
                if not b:
                    break
                f.write(b)
                f.flush()
        os.close(master)

    def texts(self, skip):
        """Uscita del comando e file nuovi in d (curl -OJ scrive la risposta in bashtransfer.txt)."""
        paths = [self.out] + [os.path.join(self.d, n) for n in os.listdir(self.d) if n != skip]
        for p in paths:
            try:
                if os.path.isfile(p) and os.path.getsize(p) < 1 << 20:
                    with open(p, "rb") as f:
                        yield f.read().decode("utf-8", "replace").replace("\r", "")
            except OSError:
                pass

    def table(self, skip, timeout=10):
        """Comandi stampati {strumento: riga}, cercati nell'uscita e nei file nuovi; None se assenti."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            for t in self.texts(skip):
                rows = dict(TABLE_ROW.findall(t))
                if len(rows) == 3:
                    return rows
            if self.p.poll() is not None and time.time() > deadline - timeout + 1:
                return None
            time.sleep(0.05)
        return None

    def said(self, skip, text):
        return any(text in t for t in self.texts(skip))

    def signal(self, sig):
        try:
            os.killpg(self.p.pid, sig)
        except ProcessLookupError:
            pass

    def wait(self, timeout=30):
        try:
            rc = self.p.wait(timeout)
        except subprocess.TimeoutExpired:
            self.signal(signal.SIGKILL)
            self.p.wait()
            rc = "timeout"
        self.copier.join(5)
        return rc


def run(argv, cwd, home, timeout=30):
    try:
        p = subprocess.run(argv, cwd=cwd, env=tool_env(home), stdin=subprocess.DEVNULL,
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=timeout)
        return p.returncode, p.stderr.decode("utf-8", "replace").strip()[-300:]
    except subprocess.TimeoutExpired:
        return "timeout", ""


def url_of(rows):
    return shlex.split(rows["curl"])[-1]


# --- Varianti dei comandi del ricevente -------------------------------------------------------
# Ognuna trasforma i token della riga stampata nel comando eseguito. residuo: limite noto,
# eseguito e riportato senza esito.

def receiver_variants():
    v = []
    # curl -f -OJ URL: ogni sottoinsieme di -f, -O, -J; aggiunte comuni.
    v.append(("curl", "stampato", lambda t: t, None))
    for f, o, j in itertools.product((1, 0), repeat=3):
        flags = ["-f"] * f + ["-O"] * o + ["-J"] * j
        v.append(("curl", " ".join(flags) or "senza flag", lambda t, fl=flags: ["curl", *fl, t[-1]], None))
    for extra in (["-C", "-"], ["-L"], ["-k"], ["--remote-name-all"]):
        v.append(("curl", "stampato " + " ".join(extra), lambda t, e=extra: t[:-1] + e + t[-1:], None))
    v.append(("curl", "stampato --clobber", lambda t: t[:-1] + ["--clobber"] + t[-1:],
              "--clobber chiede esplicitamente di sovrascrivere"))
    # wget --content-disposition URL: con e senza --content-disposition, aggiunte comuni.
    # "stampato" è sempre la riga della tabella così com'è, non la sua ricostruzione.
    for cd in (True, False):
        for extra in ([], ["-c"], ["-N"], ["-nc"], ["-c", "-N"]):
            flags = ["--content-disposition"] * cd + extra
            if flags == ["--content-disposition"]:
                v.append(("wget", "stampato", lambda t: t, None))
            else:
                v.append(("wget", " ".join(flags) or "senza flag", lambda t, fl=flags: ["wget", *fl, t[-1]], None))
    # busybox wget riceve la riga di wget: non conosce --content-disposition (esce con errore
    # senza scrivere) e ignora -nc.
    v.append(("busybox", "stampato", lambda t: ["busybox", *t], None))
    for flags in ([], ["-c"], ["-q"], ["--content-disposition", "-c"]):
        v.append(("busybox", " ".join(flags) or "senza flag", lambda t, fl=flags: ["busybox", "wget", *fl, t[-1]], None))
    # aria2c -x1 -s1 --allow-overwrite=false URL: ogni sottoinsieme dei flag; aggiunte comuni.
    printed = ["-x1", "-s1", "--allow-overwrite=false"]
    for keep in itertools.product((1, 0), repeat=3):
        flags = [f for f, k in zip(printed, keep) if k]
        if all(keep):
            v.append(("aria2c", "stampato", lambda t: t, None))
        else:
            v.append(("aria2c", " ".join(flags) or "senza flag", lambda t, fl=flags: ["aria2c", *fl, t[-1]], None))
    for extra in (["-c"], ["--auto-file-renaming=false"], ["-x4", "-s4"]):
        v.append(("aria2c", "stampato " + " ".join(extra), lambda t, e=extra: t[:-1] + e + t[-1:], None))
    v.append(("aria2c", "-x1 -s1 --allow-overwrite=true", lambda t: ["aria2c", "-x1", "-s1", "--allow-overwrite=true", t[-1]],
              "--allow-overwrite=true chiede esplicitamente di sovrascrivere"))
    return v


# Configurazioni dell'utente ricevente: vuota e due ostili.
CONFIGS = {
    "vuota": {},
    "ostile-1": {".curlrc": "remote-name-all\n", ".wgetrc": "continue = on\ncontent_disposition = on\n",
                 ".aria2/aria2.conf": "allow-overwrite=true\n"},
    "ostile-2": {".curlrc": "remote-header-name\n", ".wgetrc": "timestamping = on\nno_clobber = off\n",
                 ".aria2/aria2.conf": "auto-file-renaming=false\ncontinue=true\n"},
}


def config_residual(tool, argv, cfg):
    if cfg == "ostile-1" and tool == "aria2c" and "--allow-overwrite=false" not in argv:
        return "aria2.conf con allow-overwrite=true e --allow-overwrite=false tolto"
    return None


# Sorgenti nella cartella del ricevente, con il nome che il download userebbe.
KINDS = [("file", NAME), ("file", "bashtransfer.txt"), ("dir", "cartella"), ("link", NAME)]


def sender_cmd(kind, name, base):
    u = shlex.quote(base + "/" + urllib.parse.quote(name))
    if kind == "dir":
        return f"tar -cf - {shlex.quote(name)} | curl -sS -N -T - {u}"
    return f"curl -sS -N -T {shlex.quote(name)} {u}"


def receiver_valid(tool, label, fn, residuo, kind, name, cfg):
    """Link valido: il mittente invia la sorgente dalla cartella in cui il ricevente lancia la variante."""
    c = case_dir()
    d = os.path.join(c, "cartella")
    os.mkdir(d)
    src = make_source(kind, d, name)
    before = snapshot(src)
    snd = Sender(sender_cmd(kind, name, MAIN), d, HOMES["vuota"], os.path.join(c, "tx.out"))
    rows = snd.table(name)
    r = dict(side="ricevente", tool=tool, label=label, kind=kind, name=name, state="valido", cfg=cfg, residuo=residuo)
    if rows is None:
        snd.wait(1)
        return dict(r, error="nessuna tabella dal mittente")
    if probe(url_of(rows)) != 400:
        snd.wait(1)
        return dict(r, error="link non valido prima della variante")
    line = rows["wget" if tool == "busybox" else tool]
    argv = fn(shlex.split(line))
    r["argv"] = argv
    r["residuo"] = residuo or config_residual(tool, argv, cfg)
    r["rc"], r["err"] = run(argv, d, HOMES[cfg])
    # Prova dello stato: la variante ha consumato il link, oppure il link è ancora valido e il
    # comando stampato lo scarica subito dopo (in una cartella vuota) con il contenuto giusto.
    st = probe(url_of(rows))
    if st == 400:
        ctl = os.path.join(c, "controllo")
        os.mkdir(ctl)
        rc, err = run(shlex.split(rows["curl"]), ctl, HOMES["vuota"])
        got = os.listdir(ctl)
        if rc != 0 or got != [name]:
            r["error"] = f"controllo dopo la variante: rc={rc} file={got} {err}"
        elif kind != "dir" and sha256(os.path.join(ctl, name)) != sha256(os.path.realpath(src)):
            r["error"] = "controllo dopo la variante: contenuto diverso"
        r["link"] = "non consumato"
    elif st == 410:
        r["link"] = "consumato"
    else:
        r["error"] = f"link in stato {st} dopo la variante"
    if snd.wait() == "timeout":
        r["error"] = "il mittente non termina"
    r["after_ok"] = snapshot(src) == before
    r["files"] = sorted(os.listdir(d))
    return r


def receiver_error_state(tool, label, fn, residuo, kind, name, cfg, state, url):
    c = case_dir()
    d = os.path.join(c, "cartella")
    os.mkdir(d)
    src = make_source(kind, d, name)
    before = snapshot(src)
    # La riga stampata per un altro link, con l'URL di questo stato.
    rows = PRINTED["rows"]
    line = rows["wget" if tool == "busybox" else tool].replace(url_of(rows), url)
    argv = fn(shlex.split(line))
    rc, err = run(argv, d, HOMES[cfg])
    return dict(side="ricevente", tool=tool, label=label, kind=kind, name=name, state=state, cfg=cfg, argv=argv,
                residuo=residuo or config_residual(tool, argv, cfg), rc=rc, err=err,
                after_ok=snapshot(src) == before, files=sorted(os.listdir(d)))


# --- Comandi del mittente ---------------------------------------------------------------------
# {F} sorgente, {U} URL con il nome, {B} base. residuo come sopra.

SENDERS = [
    ("curl -T F B", "curl -sS -T {F} {B}", None),
    ("curl -T F B/", "curl -sS -T {F} {B}/", None),
    ("curl -T F B/F", "curl -sS -T {F} {U}", None),
    ("curl B/F -T F", "curl -sS {U} -T {F}", None),
    ("curl --upload-file F B/F", "curl -sS --upload-file {F} {U}", None),
    ("curl -N -T F B/F", "curl -sS -N -T {F} {U}", None),
    ("curl -f -T F B/F", "curl -sS -f -T {F} {U}", None),
    ("curl -T F -J B/F", "curl -sS -T {F} -J {U}", None),
    # Con -OJ la tabella va in bashtransfer.txt e senza -N curl la scrive solo alla fine: -N la
    # rende visibile al test, il file aperto è lo stesso.
    ("curl -N -T F -OJ B/F", "curl -sS -N -T {F} -OJ {U}", "ojerr"),
    ("curl -N -T F -OJ B", "curl -sS -N -T {F} -OJ {B}", "ojerr"),
    ("curl -T F -L B/F", "curl -sS -T {F} -L {U}", None),
    ("cat F | curl -T - B/F", "cat {F} | curl -sS -T - {U}", None),
    ("curl -T - B/F < F", "curl -sS -T - {U} < {F}", None),
    ("cat F | curl -N -T - -OJ B/F", "cat {F} | curl -sS -N -T - -OJ {U}", "ojerr"),
    ("curl -T F -O B/F", "curl -sS -T {F} -O {U}", "curl -O scrive la risposta nel file con il nome dell'URL: la sorgente"),
    ("curl -T F -O B/", "curl -sS -T {F} -O {B}/", "con / finale curl aggiunge all'URL il nome della sorgente, e -O lo usa"),
    ("curl -T F -o F B", "curl -sS -T {F} -o {F} {B}", "-o con il nome della sorgente"),
]
DIR_SENDERS = [
    ("tar c D | curl -T - B/D.tar", "tar -cf - {F} | curl -sS -T - {U}.tar", None),
    ("tar cz D | curl -T - B/D.tgz", "tar -czf - {F} | curl -sS -T - {U}.tgz", None),
    ("tar c D | curl -N -T - -OJ B/D", "tar -cf - {F} | curl -sS -N -T - -OJ {U}", "ojerr"),
    ("tar c D | curl -T - -O B/D", "tar -cf - {F} | curl -sS -T - -O {U}", None),
]
OUTCOMES = ["ok", "ricevente-caduto", "scaduto", "ctrl-c", "401", "503"]


def sender_case(label, tmpl, residuo, kind, name, outcome):
    c = case_dir()
    d = os.path.join(c, "cartella")
    os.mkdir(d)
    src = make_source(kind, d, name)
    before = snapshot(src)
    base = {"scaduto": SHORT, "401": AUTH, "503": CAP}.get(outcome, MAIN)
    cmd = tmpl.format(F=shlex.quote(name), U=shlex.quote(base + "/" + urllib.parse.quote(name)), B=shlex.quote(base))
    if residuo == "ojerr":
        residuo = "curl -OJ su una risposta di errore: curl recenti ignorano Content-Disposition" \
            if outcome in ("401", "503") else None
    r = dict(side="mittente", tool="curl", label=label, kind=kind, name=name, state=outcome, cfg="vuota",
             argv=["bash", "-c", cmd], residuo=residuo)
    snd = Sender(cmd, d, HOMES["vuota"], os.path.join(c, "tx.out"))
    evidence = None
    if outcome in ("ok", "ricevente-caduto", "ctrl-c"):
        rows = snd.table(name)
        if rows is not None:
            url = url_of(rows)
            if outcome == "ok":
                ctl = os.path.join(c, "controllo")
                os.mkdir(ctl)
                rc, err = run(["curl", "-sS", "-f", "-o", os.path.join(ctl, "x"), url], ctl, HOMES["vuota"])
                if rc != 0:
                    r["error"] = f"download di controllo: rc={rc} {err}"
                elif kind != "dir" and sha256(os.path.join(ctl, "x")) != sha256(os.path.realpath(src)):
                    r["error"] = "download di controllo: contenuto diverso"
            elif outcome == "ricevente-caduto":
                status(url, limit=65536)
            else:
                snd.signal(signal.SIGINT)
            evidence = "tabella"
    rc = snd.wait()
    r["rc"] = rc
    if outcome == "scaduto" and snd.said(name, "nessun ricevente si è collegato"):
        evidence = "scadenza"
    if outcome in ("401", "503"):
        evidence = "controllato all'avvio"
    if evidence is None and rc == 23:
        # curl -OJ non può creare bashtransfer.txt (esiste già: è la sorgente) e si ferma.
        evidence = "curl rifiuta di scrivere"
    if evidence is None and residuo is None:
        r["error"] = "nessuna prova che il server abbia risposto"
    if rc == "timeout":
        r["error"] = "il mittente non termina"
    r["after_ok"] = snapshot(src) == before
    r["files"] = sorted(os.listdir(d))
    return r


# --- Avvio ------------------------------------------------------------------------------------

def tool_version(tool):
    argv = {"busybox": ["busybox"], "aria2c": ["aria2c", "--version"]}.get(tool, [tool, "--version"])
    try:
        out = subprocess.run(argv, capture_output=True, timeout=60, env=ENV).stdout.decode("utf-8", "replace")
    except (OSError, subprocess.TimeoutExpired):
        return None
    return out.strip().splitlines()[0] if out.strip() else None


def main():
    tools = {}
    for t in ("curl", "wget", "busybox", "aria2c"):
        v = tool_version(t) if shutil.which(t) else None
        if t == "busybox" and v and subprocess.run(["busybox", "wget", "--help"], capture_output=True,
                                                     env=ENV, timeout=60).returncode not in (0, 1):
            v = None
        if v:
            tools[t] = v
        elif t in REQUIRE:
            fail(f"{t} richiesto (BASHT_INV_REQUIRE) ma non disponibile")
    if "curl" not in tools:
        fail("curl non disponibile")
    print("strumenti: " + " | ".join(tools.values()))

    global MAIN, SHORT, AUTH, CAP, HOMES
    MAIN = start_server("main")
    SHORT = start_server("short", BASHT_WAIT_TIMEOUT=f"{SHORT_WAIT}s")
    AUTH = start_server("auth", BASHT_AUTH_USER="alice", BASHT_AUTH_PASSWORD="s3cret")
    CAP = start_server("cap", BASHT_MAX_SESSIONS="1")
    HOMES = {name: make_home(name, files) for name, files in CONFIGS.items()}

    # Il server con capacità 1 resta pieno: ogni PUT risponde 503.
    hold_dir = case_dir()
    with open(os.path.join(hold_dir, "h"), "wb") as f:
        f.write(b"x" * 1000)
    hold = Sender(f"curl -sS -N -T h {CAP}/h", hold_dir, HOMES["vuota"], os.path.join(hold_dir, "out"))
    if hold.table("h") is None:
        fail("sessione di riempimento non avviata")
    for base, want in ((AUTH, 401), (CAP, 503)):
        got = subprocess.run(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-T", os.path.join(hold_dir, "h"),
                              base + "/x"], capture_output=True, env=ENV).stdout.decode()
        if got != str(want):
            fail(f"controllo iniziale: PUT su {base} ha risposto {got}, atteso {want}")

    # Link negli stati di errore, condivisi da tutte le varianti.
    states = {"sconosciuto": (MAIN + "/ABCDEFGHIJ", 404)}
    d = case_dir()
    with open(os.path.join(d, "s"), "wb") as f:
        f.write(os.urandom(1000))
    used = Sender(f"curl -sS -N -T s {MAIN}/s", d, HOMES["vuota"], os.path.join(d, "used.out"))
    rows = used.table("s")
    PRINTED["rows"] = rows
    status(url_of(rows))
    used.wait()
    states["usato"] = (url_of(rows), 410)
    exp = Sender(f"curl -sS -N -T s {SHORT}/s", d, HOMES["vuota"], os.path.join(d, "exp.out"))
    rows = exp.table("s")
    exp.wait()
    states["scaduto"] = (url_of(rows), 410)
    gone = Sender(f"curl -sS -N -T s {MAIN}/s", d, HOMES["vuota"], os.path.join(d, "gone.out"))
    rows = gone.table("s")
    gone.signal(signal.SIGKILL)
    gone.wait()
    for _ in range(100):
        if probe(url_of(rows)) == 410:
            break
        time.sleep(0.2)
    states["mittente-caduto"] = (url_of(rows), 410)
    for st, (url, want) in states.items():
        if probe(url) != want:
            fail(f"link {st}: stato {probe(url)}, atteso {want}")

    variants = [v for v in receiver_variants() if v[0] in tools]
    jobs = []
    for tool, label, fn, residuo in variants:
        for cfg in CONFIGS:
            if tool == "busybox" and cfg != "vuota":
                continue  # busybox wget non legge file di configurazione
            for kind, name in KINDS:
                jobs.append((receiver_valid, (tool, label, fn, residuo, kind, name, cfg)))
                for st, (url, _) in states.items():
                    jobs.append((receiver_error_state, (tool, label, fn, residuo, kind, name, cfg, st, url)))
    for label, tmpl, residuo in SENDERS:
        for name in ("src.bin", "bashtransfer.txt"):
            for outcome in OUTCOMES:
                jobs.append((sender_case, (label, tmpl, residuo, "file", name, outcome)))
    for label, tmpl, residuo in DIR_SENDERS:
        for outcome in OUTCOMES:
            jobs.append((sender_case, (label, tmpl, residuo, "dir", "cartella", outcome)))

    t0 = time.time()
    with concurrent.futures.ThreadPoolExecutor(JOBS) as ex:
        results = list(ex.map(lambda j: j[0](*j[1]), jobs))
    hold.signal(signal.SIGKILL)
    for st, (url, want) in states.items():
        if probe(url) != want:
            fail(f"link {st}: stato cambiato durante il test ({probe(url)}, atteso {want})")

    # Controlli positivi: ogni comando stampato, in una cartella vuota, salva una copia identica
    # con il nome giusto (busybox: senza --content-disposition, con il nome dello slug).
    positive = []
    for tool in tools:
        for r in results:
            if r["side"] == "ricevente" and r["tool"] == tool and r["state"] == "valido" and r["cfg"] == "vuota" \
                    and r["kind"] == "file" and r["name"] == NAME \
                    and r["label"] == ("senza flag" if tool == "busybox" else "stampato"):
                positive.append(r)
    for r in positive:
        c = case_dir()
        d = os.path.join(c, "vuota")
        os.mkdir(d)
        src = make_source("file", c, "orig")
        u = shlex.quote(MAIN + "/" + urllib.parse.quote(NAME))
        snd = Sender(f"curl -sS -N -T orig {u}", c, HOMES["vuota"], os.path.join(c, "tx.out"))
        rows = snd.table("orig")
        if rows is None:
            fail(f"controllo positivo {r['tool']}: nessuna tabella dal mittente")
        argv = r["argv"][:-1] + [url_of(rows)]
        rc, err = run(argv, d, HOMES["vuota"])
        snd.wait()
        got = os.listdir(d)
        want = os.path.basename(urllib.parse.urlsplit(url_of(rows)).path) if r["tool"] == "busybox" else NAME
        if rc != 0 or got != [want] or sha256(os.path.join(d, want)) != sha256(src):
            fail(f"controllo positivo {r['tool']} {argv}: rc={rc} file={got} {err}")

    if os.environ.get("BASHT_INV_DUMP"):
        with open(os.environ["BASHT_INV_DUMP"], "w") as f:
            for r in results:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
    bad = [r for r in results if not r.get("residuo") and (r.get("error") or not r["after_ok"])]
    residual = [r for r in results if r.get("residuo")]
    for side in ("ricevente", "mittente"):
        rs = [r for r in results if r["side"] == side and not r.get("residuo")]
        by = {}
        for r in rs:
            by.setdefault((r["tool"], r["state"]), []).append(r)
        print(f"{side}: {len(rs)} casi, {sum(not r['after_ok'] for r in rs)} sorgenti alterate")
        for (tool, st), group in sorted(by.items()):
            extra = ""
            if st == "valido":
                n = sum(r.get("link") == "consumato" for r in group)
                extra = f" (link consumato dalla variante in {n}, scaricato dopo con il comando stampato negli altri)"
            print(f"  {tool:8} {st:17} {len(group):4} casi{extra}")
    print(f"controlli positivi: {len(positive)} ok ({', '.join(r['tool'] for r in positive)})")
    print("limiti noti (eseguiti, senza esito):")
    seen = set()
    for r in residual:
        key = (r["side"], r["label"], r["cfg"], r["residuo"])
        alt = sum(not x["after_ok"] for x in residual if (x["side"], x["label"], x["cfg"], x["residuo"]) == key)
        tot = sum(1 for x in residual if (x["side"], x["label"], x["cfg"], x["residuo"]) == key)
        if key not in seen:
            seen.add(key)
            print(f"  {r['side']:9} {r['label']:34} cfg={r['cfg']:8} alterata in {alt}/{tot}: {r['residuo']}")
    print(f"durata: {time.time() - t0:.0f} s")
    if bad:
        for r in bad[:40]:
            what = r.get("error") or "SORGENTE ALTERATA"
            print(f"FAIL {what}: {r['side']} {r['tool']} [{r['label']}] {r['kind']} {r['name']!r} stato={r['state']} "
                  f"cfg={r['cfg']} argv={r.get('argv')} rc={r.get('rc')} file={r.get('files')} {r.get('err', '')}",
                  file=sys.stderr)
        fail(f"{len(bad)} casi su {len(results)}")
    print("INVARIANTE OK")


try:
    main()
finally:
    for p in PROCS:
        p.kill()
    shutil.rmtree(TMP, ignore_errors=True)
