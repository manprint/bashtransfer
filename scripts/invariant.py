#!/usr/bin/env python3
"""Bashtransfer invariant: the source is never altered.

Run the commands printed for the sender, their incorrect variants (omitted/added flags,
user configuration files) and sending commands, with the source (file, directory, symlink)
where the receiver saves: in the sender's directory. Compare a source snapshot (content,
size, permissions, mtime, ctime, inode) before and after each case.

Each case must prove it exercised the declared state: a valid link is consumed by the
variant or remains valid and is immediately downloaded with the printed command;
error states are checked before and after. A case that proves nothing is an error.

usage: invariant.py BINARY
    BINARY              compiled bashtransfer (go build ./cmd/bashtransfer)
    BASHT_INV_REQUIRE   required tools, e.g. "curl,wget,aria2c,busybox": missing tools
                        fail the test instead of being skipped (CI)
    BASHT_INV_JOBS      parallel cases (default 8)
Exit 1 if a case changes the source or proves nothing. Known limitations (README,
"The source is never altered") are exercised and reported without asserting an outcome.
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
SHORT_WAIT = 2  # Server BASHT_WAIT_TIMEOUT for expired links, in seconds

# Ignore proxies and configuration belonging to the user running the test.
ENV = {k: v for k, v in os.environ.items()
       if not k.lower().endswith("_proxy") and k not in ("CURL_HOME", "WGETRC", "XDG_CONFIG_HOME")}
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
PROCS = []
PRINTED = {}  # "rows": printed link table, a command template for error states
TABLE_ROW = re.compile(r"^\| (curl|wget|aria2c) +\| (.*?) +\|$", re.M)


def fail(msg):
    print("INVARIANT FAIL: " + msg, file=sys.stderr)
    sys.exit(1)


def status(url, headers=None, limit=None):
    """GET status; if limit is set, read at most that many body bytes and close."""
    try:
        with OPENER.open(urllib.request.Request(url, headers=headers or {}), timeout=15) as r:
            r.read(limit) if limit else r.read()
            return r.status
    except urllib.error.HTTPError as e:
        return e.code


def probe(url):
    """Non-consuming link state: 400 valid, 410 used/expired, 404 unknown."""
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
    fail(f"server {name} not responding")


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
    """Snapshot a path, its contents and, for a symlink, its target."""
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
    """Create source name in d: a file, directory or symlink to a file outside d."""
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
    """Sending command (bash -c) started in d in its own process group."""

    def __init__(self, cmd, d, home, out):
        self.d, self.out = d, out
        # Use terminal output as in a manual invocation: curl buffers output to a file,
        # which would make the table appear only after completion.
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
        """Command output and new files in d (curl -OJ writes to bashtransfer.txt)."""
        paths = [self.out] + [os.path.join(self.d, n) for n in os.listdir(self.d) if n != skip]
        for p in paths:
            try:
                if os.path.isfile(p) and os.path.getsize(p) < 1 << 20:
                    with open(p, "rb") as f:
                        yield f.read().decode("utf-8", "replace").replace("\r", "")
            except OSError:
                pass

    def table(self, skip, timeout=10):
        """Printed {tool: row} commands from output/new files; None if missing."""
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


# --- Receiver command variants --------------------------------------------------------------
# Each transforms the printed row's tokens into the executed command. residuo: a known
# limitation, exercised and reported without asserting an outcome.

def receiver_variants():
    v = []
    # curl -f -OJ URL: every subset of -f, -O, -J and common additions.
    v.append(("curl", "stampato", lambda t: t, None))
    for f, o, j in itertools.product((1, 0), repeat=3):
        flags = ["-f"] * f + ["-O"] * o + ["-J"] * j
        v.append(("curl", " ".join(flags) or "senza flag", lambda t, fl=flags: ["curl", *fl, t[-1]], None))
    for extra in (["-C", "-"], ["-L"], ["-k"], ["--remote-name-all"]):
        v.append(("curl", "stampato " + " ".join(extra), lambda t, e=extra: t[:-1] + e + t[-1:], None))
    v.append(("curl", "stampato --clobber", lambda t: t[:-1] + ["--clobber"] + t[-1:],
              "--clobber explicitly requests overwriting"))
    # wget --content-disposition URL: with/without --content-disposition and common additions.
    # "stampato" always means the original table row, not a reconstruction.
    for cd in (True, False):
        for extra in ([], ["-c"], ["-N"], ["-nc"], ["-c", "-N"]):
            flags = ["--content-disposition"] * cd + extra
            if flags == ["--content-disposition"]:
                v.append(("wget", "stampato", lambda t: t, None))
            else:
                v.append(("wget", " ".join(flags) or "senza flag", lambda t, fl=flags: ["wget", *fl, t[-1]], None))
    # busybox wget receives the wget row: --content-disposition is unsupported (it exits
    # with an error without writing) and -nc is ignored.
    v.append(("busybox", "stampato", lambda t: ["busybox", *t], None))
    for flags in ([], ["-c"], ["-q"], ["--content-disposition", "-c"]):
        v.append(("busybox", " ".join(flags) or "senza flag", lambda t, fl=flags: ["busybox", "wget", *fl, t[-1]], None))
    # aria2c -x1 -s1 --allow-overwrite=false URL: every subset of flags and common additions.
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
              "--allow-overwrite=true explicitly requests overwriting"))
    return v


# Receiver user configurations: empty and two hostile ones.
CONFIGS = {
    "vuota": {},
    "ostile-1": {".curlrc": "remote-name-all\n", ".wgetrc": "continue = on\ncontent_disposition = on\n",
                 ".aria2/aria2.conf": "allow-overwrite=true\n"},
    "ostile-2": {".curlrc": "remote-header-name\n", ".wgetrc": "timestamping = on\nno_clobber = off\n",
                 ".aria2/aria2.conf": "auto-file-renaming=false\ncontinue=true\n"},
}


def config_residual(tool, argv, cfg):
    if cfg == "ostile-1" and tool == "aria2c" and "--allow-overwrite=false" not in argv:
        return "aria2.conf with allow-overwrite=true and --allow-overwrite=false removed"
    return None


# Sources in the receiver's directory, named as the download would be.
KINDS = [("file", NAME), ("file", "bashtransfer.txt"), ("dir", "cartella"), ("link", NAME)]


def sender_cmd(kind, name, base):
    u = shlex.quote(base + "/" + urllib.parse.quote(name))
    if kind == "dir":
        return f"tar -cf - {shlex.quote(name)} | curl -sS -N -T - {u}"
    return f"curl -sS -N -T {shlex.quote(name)} {u}"


def receiver_valid(tool, label, fn, residuo, kind, name, cfg):
    """Valid link: send the source from the directory where the receiver runs the variant."""
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
        return dict(r, error="no table from sender")
    if probe(url_of(rows)) != 400:
        snd.wait(1)
        return dict(r, error="link not valid before variant")
    line = rows["wget" if tool == "busybox" else tool]
    argv = fn(shlex.split(line))
    r["argv"] = argv
    r["residuo"] = residuo or config_residual(tool, argv, cfg)
    r["rc"], r["err"] = run(argv, d, HOMES[cfg])
    # State evidence: the variant consumed the link, or it is still valid and the printed
    # command downloads it immediately afterward into an empty directory with correct content.
    st = probe(url_of(rows))
    if st == 400:
        ctl = os.path.join(c, "controllo")
        os.mkdir(ctl)
        rc, err = run(shlex.split(rows["curl"]), ctl, HOMES["vuota"])
        got = os.listdir(ctl)
        if rc != 0 or got != [name]:
            r["error"] = f"check after variant: rc={rc} files={got} {err}"
        elif kind != "dir" and sha256(os.path.join(ctl, name)) != sha256(os.path.realpath(src)):
            r["error"] = "check after variant: content mismatch"
        r["link"] = "non consumato"
    elif st == 410:
        r["link"] = "consumato"
    else:
        r["error"] = f"link in state {st} after variant"
    if snd.wait() == "timeout":
        r["error"] = "sender did not exit"
    r["after_ok"] = snapshot(src) == before
    r["files"] = sorted(os.listdir(d))
    return r


def receiver_error_state(tool, label, fn, residuo, kind, name, cfg, state, url):
    c = case_dir()
    d = os.path.join(c, "cartella")
    os.mkdir(d)
    src = make_source(kind, d, name)
    before = snapshot(src)
    # The row printed for another link, using this state's URL.
    rows = PRINTED["rows"]
    line = rows["wget" if tool == "busybox" else tool].replace(url_of(rows), url)
    argv = fn(shlex.split(line))
    rc, err = run(argv, d, HOMES[cfg])
    return dict(side="ricevente", tool=tool, label=label, kind=kind, name=name, state=state, cfg=cfg, argv=argv,
                residuo=residuo or config_residual(tool, argv, cfg), rc=rc, err=err,
                after_ok=snapshot(src) == before, files=sorted(os.listdir(d)))


# --- Sender commands -------------------------------------------------------------------------
# {F} source, {U} URL including filename, {B} base. residuo as above.

SENDERS = [
    ("curl -T F B", "curl -sS -T {F} {B}", None),
    ("curl -T F B/", "curl -sS -T {F} {B}/", None),
    ("curl -T F B/F", "curl -sS -T {F} {U}", None),
    ("curl B/F -T F", "curl -sS {U} -T {F}", None),
    ("curl --upload-file F B/F", "curl -sS --upload-file {F} {U}", None),
    ("curl -N -T F B/F", "curl -sS -N -T {F} {U}", None),
    ("curl -f -T F B/F", "curl -sS -f -T {F} {U}", None),
    ("curl -T F -J B/F", "curl -sS -T {F} -J {U}", None),
    # With -OJ the table goes to bashtransfer.txt; without -N curl writes it only at the end.
    # -N makes it visible to the test while opening the same file.
    ("curl -N -T F -OJ B/F", "curl -sS -N -T {F} -OJ {U}", "ojerr"),
    ("curl -N -T F -OJ B", "curl -sS -N -T {F} -OJ {B}", "ojerr"),
    ("curl -T F -L B/F", "curl -sS -T {F} -L {U}", None),
    ("cat F | curl -T - B/F", "cat {F} | curl -sS -T - {U}", None),
    ("curl -T - B/F < F", "curl -sS -T - {U} < {F}", None),
    ("cat F | curl -N -T - -OJ B/F", "cat {F} | curl -sS -N -T - -OJ {U}", "ojerr"),
    ("curl -T F -O B/F", "curl -sS -T {F} -O {U}", "curl -O writes the response to the URL's filename: the source"),
    ("curl -T F -O B/", "curl -sS -T {F} -O {B}/", "with trailing / curl adds the source filename to the URL and -O uses it"),
    ("curl -T F -o F B", "curl -sS -T {F} -o {F} {B}", "-o with the source filename"),
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
        residuo = "curl -OJ on an error response: recent curl versions ignore Content-Disposition" \
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
                    r["error"] = f"control download: rc={rc} {err}"
                elif kind != "dir" and sha256(os.path.join(ctl, "x")) != sha256(os.path.realpath(src)):
                    r["error"] = "control download: content mismatch"
            elif outcome == "ricevente-caduto":
                status(url, limit=65536)
            else:
                snd.signal(signal.SIGINT)
            evidence = "tabella"
    rc = snd.wait()
    r["rc"] = rc
    if outcome == "scaduto" and snd.said(name, "no receiver connected"):
        evidence = "scadenza"
    if outcome in ("401", "503"):
        evidence = "controllato all'avvio"
    if evidence is None and rc == 23:
        # curl -OJ cannot create bashtransfer.txt (it already exists as the source) and stops.
        evidence = "curl rifiuta di scrivere"
    if evidence is None and residuo is None:
        r["error"] = "no evidence that the server responded"
    if rc == "timeout":
        r["error"] = "sender did not exit"
    r["after_ok"] = snapshot(src) == before
    r["files"] = sorted(os.listdir(d))
    return r


# --- Startup ----------------------------------------------------------------------------------

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
            fail(f"{t} required (BASHT_INV_REQUIRE) but unavailable")
    if "curl" not in tools:
        fail("curl unavailable")
    print("tools: " + " | ".join(tools.values()))

    global MAIN, SHORT, AUTH, CAP, HOMES
    MAIN = start_server("main")
    SHORT = start_server("short", BASHT_WAIT_TIMEOUT=f"{SHORT_WAIT}s")
    AUTH = start_server("auth", BASHT_AUTH_USER="alice", BASHT_AUTH_PASSWORD="s3cret")
    CAP = start_server("cap", BASHT_MAX_SESSIONS="1")
    HOMES = {name: make_home(name, files) for name, files in CONFIGS.items()}

    # Keep the capacity-1 server full: every PUT returns 503.
    hold_dir = case_dir()
    with open(os.path.join(hold_dir, "h"), "wb") as f:
        f.write(b"x" * 1000)
    hold = Sender(f"curl -sS -N -T h {CAP}/h", hold_dir, HOMES["vuota"], os.path.join(hold_dir, "out"))
    if hold.table("h") is None:
        fail("capacity-filling session not started")
    for base, want in ((AUTH, 401), (CAP, 503)):
        got = subprocess.run(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-T", os.path.join(hold_dir, "h"),
                              base + "/x"], capture_output=True, env=ENV).stdout.decode()
        if got != str(want):
            fail(f"initial check: PUT to {base} returned {got}, expected {want}")

    # Links in error states, shared by every variant.
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
            fail(f"link {st}: state {probe(url)}, expected {want}")

    variants = [v for v in receiver_variants() if v[0] in tools]
    jobs = []
    for tool, label, fn, residuo in variants:
        for cfg in CONFIGS:
            if tool == "busybox" and cfg != "vuota":
                continue  # busybox wget does not read configuration files
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
            fail(f"link {st}: state changed during the test ({probe(url)}, expected {want})")

    # Positive controls: every printed command saves an identical copy with the correct
    # filename in an empty directory (busybox: no --content-disposition, named after the slug).
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
            fail(f"positive control {r['tool']}: no table from sender")
        argv = r["argv"][:-1] + [url_of(rows)]
        rc, err = run(argv, d, HOMES["vuota"])
        snd.wait()
        got = os.listdir(d)
        want = os.path.basename(urllib.parse.urlsplit(url_of(rows)).path) if r["tool"] == "busybox" else NAME
        if rc != 0 or got != [want] or sha256(os.path.join(d, want)) != sha256(src):
            fail(f"positive control {r['tool']} {argv}: rc={rc} files={got} {err}")

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
        print(f"{side}: {len(rs)} cases, {sum(not r['after_ok'] for r in rs)} altered sources")
        for (tool, st), group in sorted(by.items()):
            extra = ""
            if st == "valido":
                n = sum(r.get("link") == "consumato" for r in group)
                extra = f" (variant consumed the link in {n}; otherwise downloaded afterward with the printed command)"
            print(f"  {tool:8} {st:17} {len(group):4} cases{extra}")
    print(f"positive controls: {len(positive)} ok ({', '.join(r['tool'] for r in positive)})")
    print("known limitations (exercised without asserting outcomes):")
    seen = set()
    for r in residual:
        key = (r["side"], r["label"], r["cfg"], r["residuo"])
        alt = sum(not x["after_ok"] for x in residual if (x["side"], x["label"], x["cfg"], x["residuo"]) == key)
        tot = sum(1 for x in residual if (x["side"], x["label"], x["cfg"], x["residuo"]) == key)
        if key not in seen:
            seen.add(key)
            print(f"  {r['side']:9} {r['label']:34} cfg={r['cfg']:8} altered in {alt}/{tot}: {r['residuo']}")
    print(f"duration: {time.time() - t0:.0f} s")
    if bad:
        for r in bad[:40]:
            what = r.get("error") or "SOURCE ALTERED"
            print(f"FAIL {what}: {r['side']} {r['tool']} [{r['label']}] {r['kind']} {r['name']!r} state={r['state']} "
                  f"cfg={r['cfg']} argv={r.get('argv')} rc={r.get('rc')} file={r.get('files')} {r.get('err', '')}",
                  file=sys.stderr)
        fail(f"{len(bad)} cases out of {len(results)}")
    print("INVARIANT OK")


try:
    main()
finally:
    for p in PROCS:
        p.kill()
    shutil.rmtree(TMP, ignore_errors=True)
