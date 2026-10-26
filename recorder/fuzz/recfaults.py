#!/usr/bin/env python3
"""Fault injection and stress scenarios for the trecs recorder.

Where recfuzz.py asks "is a random session the same with and without the recorder?", this asks the
questions that need a hand-built situation: what happens to the shell and to the person's terminal when the recorder
is signalled, killed, cannot write, cannot find a temporary directory, runs out of file descriptors, is
fed faster than the terminal reads, is nested, or has the terminal closed under it.

Each scenario prints PASS / FAIL / NOTE with what was seen. Bare-shell behaviour is the reference
wherever there is one.
"""
import os
import re
import shutil
import signal
import statistics
import subprocess
import sys
import time
import hashlib

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ptyrun import Run, processes_in  # noqa: E402
import recfuzz as rf  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.join(HERE, "faultwork")
RESULTS = []


def result(status, name, detail=""):
    RESULTS.append((status, name, detail))
    print(f"{status:5} {name}" + (f"\n        {detail}" if detail else ""), flush=True)


def sandbox(tag, shell="bash", rc=None):
    root = os.path.join(WORK, tag)
    shutil.rmtree(root, ignore_errors=True)
    for d in ("home", "tmp", "obs", "cwd", "out"):
        os.makedirs(f"{root}/{d}")
    rf.write_profile(shell, f"{root}/home", rc or [])
    return root, rf.env_for(shell, root)


def start(tag, mode="rec", shell="bash", env_tweak=None, rec_args=(), preexec=None, rc=None, rows=30, cols=100, expect_prompt=True, argv_override=None):
    root, env = sandbox(tag, shell, rc)
    if env_tweak:
        env_tweak(env, root)
    sh = rf.SHELLS[shell]
    if argv_override:
        argv = argv_override(root, sh)
    elif mode == "bare":
        argv = [sh]
    else:
        argv = [rf.TRECS, "record", "-output", f"{root}/out", "-name", "s", "-cmd", sh, "-no-upload", "-overwrite", *rec_args]
    run = Run(argv, env, f"{root}/cwd", rows=rows, cols=cols, preexec=preexec)
    run.root = root
    run.root = root
    if expect_prompt:
        ok = run.expect(rb"P> ", 0, 10)
        run.settle(0.3, 2)
        run.ok = bool(ok)
    return run


def alive(run, tag="x"):
    """Is the shell answering? (types a sum and looks for its result)"""
    pos = len(run.buf)
    run.send(b"printf 'ALIVE%d\\n' 21\n")
    return bool(run.expect(rb"ALIVE\d+\r?\n", pos, 4))


def shell_pid(run):
    pos = len(run.buf)
    run.send(b"echo PID=$$=\n")
    m = run.expect(rb"PID=(\d+)=", pos, 4)
    return int(m.group(1)) if m else None


def gone(pid, wait=3.0):
    end = time.time() + wait
    while time.time() < end:
        try:
            os.kill(pid, 0)
            with open(f"/proc/{pid}/stat") as f:
                if f.read().split(")")[-1].split()[0] == "Z":
                    return True
        except (OSError, FileNotFoundError):
            return True
        time.sleep(0.05)
    return False


def finish(run):
    run.kill()
    time.sleep(0.2)
    left = processes_in(run.root)
    for pid, _ in left:
        try:
            os.kill(pid, 9)
        except OSError:
            pass
    run.close()
    return left


def raw_state(run):
    t = run.termios()
    if not isinstance(t, list):
        return None
    lflag = t[3]
    import termios as T
    return {"icanon": bool(lflag & T.ICANON), "echo": bool(lflag & T.ECHO), "isig": bool(lflag & T.ISIG)}


# -------------------------------------------------------------------------------------------------------

def test_signals_to_recorder():
    """A signal sent to the recorder (a window manager's HUP, a kill, a stray USR1) must not leave the person's terminal
    in raw mode or the shell running on without a terminal."""
    sigs = [signal.SIGHUP, signal.SIGTERM, signal.SIGINT, signal.SIGQUIT, signal.SIGUSR1, signal.SIGUSR2, signal.SIGALRM,
            signal.SIGPIPE, signal.SIGXCPU, signal.SIGXFSZ, signal.SIGVTALRM, signal.SIGPROF, signal.SIGWINCH, signal.SIGCHLD,
            signal.SIGCONT, signal.SIGURG]
    for sg in sigs:
        run = start(f"sig-{sg.name}")
        sp = shell_pid(run)
        rec = run.p.pid
        os.kill(rec, sg)
        time.sleep(0.8)
        run.pump(0.1)
        rec_alive = run.p.poll() is None
        sh_alive = sp is not None and not gone(sp, 0.1)
        usable = rec_alive and alive(run)
        st = raw_state(run)
        raw = st and not st["icanon"] and not st["echo"] if st else None
        # the terminal is the outer one: raw-looking means it was not restored. (while recording it is raw by design)
        left = None
        if not rec_alive:
            time.sleep(0.5)
            left = [c for _, c in processes_in(run.root)]
            fate = f"recorder died ({run.p.returncode})"
            problems = []
            if raw:
                problems.append("TERMINAL LEFT IN RAW MODE")
            if sh_alive and not gone(sp, 3):
                problems.append("shell still running without a terminal")
            tmpl = [t for t in os.listdir(f"{run.root}/tmp") if t.startswith("trecs")]
            if tmpl:
                problems.append(f"temp dir left {tmpl}")
            if problems:
                result("FAIL", f"signal {sg.name} to recorder", fate + "; " + "; ".join(problems))
            else:
                result("PASS", f"signal {sg.name} to recorder", fate + "; terminal restored, shell gone, temp cleaned")
        else:
            if usable:
                result("PASS", f"signal {sg.name} to recorder", "ignored: recorder and shell carry on")
            else:
                result("FAIL", f"signal {sg.name} to recorder", "recorder alive but the shell stopped answering")
        finish(run)


def test_sigkill_recorder():
    run = start("sigkill")
    sp = shell_pid(run)
    run.send(b"sleep 300 &\n")
    run.settle(0.3, 1)
    os.kill(run.p.pid, signal.SIGKILL)
    time.sleep(1.5)
    st = raw_state(run)
    problems = []
    if not gone(sp, 4):
        problems.append("shell survived the recorder's death")
    left = [c for _, c in processes_in(run.root) if "sleep" in c]
    if left:
        problems.append(f"background job survived: {left}")
    note = []
    if st and not st["icanon"]:
        note.append("terminal left raw (unavoidable on SIGKILL: `reset` fixes it)")
    tmpl = [t for t in os.listdir(f"{run.root}/tmp") if t.startswith("trecs")]
    if tmpl:
        note.append("temp dir left behind (unavoidable on SIGKILL)")
    result("FAIL" if problems else "NOTE", "SIGKILL to recorder", "; ".join(problems + note) or "clean")
    finish(run)


def test_terminal_closed():
    """The terminal window is closed with a foreground job, a background job, and a nohup job."""
    for mode in ("bare", "rec"):
        run = start(f"hup-{mode}", mode)
        run.send(b"nohup sleep 200 >/dev/null 2>&1 &\nsleep 201 &\n")
        run.settle(0.3, 1)
        run.send(b"sleep 202\n")
        time.sleep(0.5)
        run.close()
        time.sleep(2.5)
        left = sorted(set(c.split()[0] + " " + c.split()[-1] for _, c in processes_in(run.root)))
        run.p.poll()
        setattr(test_terminal_closed, mode, left)
        end = run.p.poll()
        setattr(test_terminal_closed, mode + "_end", end)
        for pid, _ in processes_in(run.root):
            try:
                os.kill(pid, 9)
            except OSError:
                pass
        run.kill()
    b, r = test_terminal_closed.bare, test_terminal_closed.rec
    if b == r and test_terminal_closed.rec_end is not None:
        result("PASS", "terminal closed under a foreground job, bg job and nohup job", f"same survivors as bare: {r}; recorder ended")
    else:
        result("FAIL", "terminal closed under jobs", f"bare left {b}; recorder left {r}; recorder ended={test_terminal_closed.rec_end}")


def can_mount_tmpfs():
    d = os.path.join(WORK, "mnt")
    os.makedirs(d, exist_ok=True)
    r = subprocess.run(["mount", "-t", "tmpfs", "-o", "size=64k", "tmpfs", d], capture_output=True)
    if r.returncode == 0:
        subprocess.run(["umount", d])
        return d
    return None


def test_disk_full():
    """The recording cannot be written (ENOSPC / EFBIG): the shell must carry on as if nothing happened."""
    mnt = can_mount_tmpfs()
    how = "tmpfs of 64 KB" if mnt else "RLIMIT_FSIZE 32 KB, SIGXFSZ ignored"
    root, env = sandbox("diskfull")
    pre = None
    out = f"{root}/out"
    if mnt:
        subprocess.run(["mount", "-t", "tmpfs", "-o", "size=64k", "tmpfs", mnt], check=True)
        out = mnt
    else:
        import resource

        def pre():
            signal.signal(signal.SIGXFSZ, signal.SIG_IGN)
            resource.setrlimit(resource.RLIMIT_FSIZE, (32 * 1024, 32 * 1024))
    try:
        argv = [rf.TRECS, "record", "-output", out, "-name", "s", "-cmd", "/usr/bin/bash", "-no-upload", "-overwrite"]
        run = Run(argv, env, f"{root}/cwd", preexec=pre)
        run.root = root
        run.root = root
        run.expect(rb"P> ", 0, 10)
        run.settle(0.3, 2)
        pos = len(run.buf)
        run.send(b"echo S-$((1+1))-S; seq 1 40000; echo E-$((1+1))-E\n")
        run.expect(rb"E-2-E", pos, 30)
        seg = bytes(run.buf[pos:])
        body = re.search(rb"S-2-S\r?\n(.*)E-2-E", rf.OSC133.sub(b"", seg), re.S)
        want = b"".join(b"%d\r\n" % i for i in range(1, 40001))
        ok_out = bool(body) and body.group(1) == want
        run.settle(0.3, 2)
        notices = len(re.findall(rb"recording can no longer be written", bytes(run.buf)))
        still = alive(run)
        run.send(b"touch still-works; exit\n")
        run.wait(6)
        wrote = os.path.exists(f"{root}/cwd/still-works")
        detail = f"({how}) shell output intact={ok_out}, shell alive after={still}, command after failure ran={wrote}, notices printed={notices}"
        result("PASS" if (ok_out and still and wrote and notices == 1) else "FAIL", "recording file cannot be written mid-session", detail)
        finish(run)
    finally:
        if mnt:
            subprocess.run(["umount", mnt], capture_output=True)


def test_output_dir_problems():
    cases = {
        "missing parent": lambda root: f"{root}/nope/deeper/out",
        "a file in the way": lambda root: (open(f"{root}/afile", "w").write("x"), f"{root}/afile")[1],
        "read-only dir": lambda root: (os.makedirs(f"{root}/ro", exist_ok=True), os.chmod(f"{root}/ro", 0o500), f"{root}/ro")[2],
        "/dev/full": lambda root: "/dev/full",
        "empty string": lambda root: "",
    }
    isroot = os.geteuid() == 0
    for nm, mk in cases.items():
        root, env = sandbox("outdir")
        outd = mk(root)
        argv = [rf.TRECS, "record", "-output", outd, "-name", "s", "-cmd", "/usr/bin/bash", "-no-upload", "-overwrite"]
        run = Run(argv, env, f"{root}/cwd")
        run.root = root
        run.expect(rb"P> ", 0, 4)
        run.settle(0.4, 2)
        started = b"P> " in bytes(run.buf)
        if started:
            run.send(b"exit\n")
        rc = run.wait(5)
        st = raw_state(run) if rc is None else None
        started_s = "the shell was started" if started else "no shell started"
        if nm == "read-only dir" and isroot:
            result("NOTE", f"output dir: {nm}", "skipped meaningfully: running as root ignores permissions")
        elif started:
            # a recording that cannot be made while the shell runs normally is acceptable only if the person is told
            told = re.search(rb"recorder:|Error|rror:|cannot|failed", bytes(run.buf))
            result("NOTE", f"output dir: {nm}", f"{started_s}; the person was {'told' if told else 'NOT told'}; exit={rc}")
        else:
            clean = rc is not None and rc != 0
            left = processes_in(root)
            result("PASS" if clean and not left else "FAIL", f"output dir: {nm}",
                   f"{started_s}; recorder exit={rc}; message: {bytes(run.buf).strip()[-120:]!r}; leftover={len(left)}")
        finish(run)
        os.chmod(f"{root}/ro", 0o700) if os.path.exists(f"{root}/ro") else None


def test_tmpdir_problems():
    for nm, tw in {
        "TMPDIR missing": lambda env, root: env.update(TMPDIR=f"{root}/nonexistent"),
        "TMPDIR is a file": lambda env, root: (open(f"{root}/tmpfile", "w").write("x"), env.update(TMPDIR=f"{root}/tmpfile")),
        "TMPDIR unset": lambda env, root: env.pop("TMPDIR"),
    }.items():
        for shell in ("bash", "zsh", "fish"):
            run = start(f"tmp-{shell}", shell=shell, env_tweak=tw)
            ok = getattr(run, "ok", False)
            if ok:
                ok = alive(run)
            note = re.findall(rb"integration unavailable[^\n]*", bytes(run.buf))
            result("PASS" if ok else "FAIL", f"{nm} ({shell})", f"shell works={ok}; notice={note[:1]}")
            run.send(b"exit\n")
            run.wait(4)
            finish(run)


def test_stdout_gone():
    """The recorder's standard output goes nowhere (a redirect to /dev/null, a closed pipe): the shell, which has the pty,
    must be unaffected."""
    root, env = sandbox("stdoutgone")
    argv = ["/bin/sh", "-c", f"exec {rf.TRECS} record -output {root}/out -name s -cmd /usr/bin/bash -no-upload -overwrite >/dev/null"]
    run = Run(argv, env, f"{root}/cwd")
    run.root = root
    time.sleep(1.5)
    run.send(b"touch alive-after-redirect; echo done > done.txt; exit\n")
    rc = run.wait(6)
    ok = os.path.exists(f"{root}/cwd/alive-after-redirect") and rc is not None
    result("PASS" if ok else "FAIL", "recorder stdout to /dev/null", f"commands ran={ok}, recorder ended={rc is not None}")
    finish(run)
    # closed pipe: stdout is a pipe nobody reads
    root, env = sandbox("stdoutpipe")
    argv = ["/bin/sh", "-c", f"{rf.TRECS} record -output {root}/out -name s -cmd /usr/bin/bash -no-upload -overwrite | (sleep 0.7; exit 0)"]
    run = Run(argv, env, f"{root}/cwd")
    run.root = root
    time.sleep(2.2)
    run.send(b"touch alive-after-pipe; for i in 1 2 3; do echo line $i; done; exit\n")
    rc = run.wait(6)
    ok = os.path.exists(f"{root}/cwd/alive-after-pipe")
    result("PASS" if ok else "FAIL", "recorder stdout is a pipe whose reader is gone", f"the shell kept working={ok} (SIGPIPE on the recorder would end the session)")
    finish(run)


def test_fd_limit():
    import resource
    for lim in (6, 10, 16, 32):
        def pre(lim=lim):
            resource.setrlimit(resource.RLIMIT_NOFILE, (lim, lim))
        res = {}
        for mode in ("bare", "rec"):
            run = start(f"fdlim-{mode}", mode, preexec=pre, expect_prompt=False)
            ok = run.expect(rb"P> ", 0, 6)
            run.settle(0.3, 1.5)
            works = bool(ok) and alive(run)
            res[mode] = (works, bytes(run.buf)[-160:], run.p.poll())
            run.send(b"exit\n")
            run.wait(3)
            finish(run)
        if res["rec"][0]:
            result("PASS", f"RLIMIT_NOFILE={lim}", f"bare works={res['bare'][0]}, recorded works={res['rec'][0]}")
        else:
            msg = re.sub(rb"\x1b\[[0-9;?]*[a-zA-Z]", b"", res["rec"][1]).strip()[-100:]
            clean = res["rec"][2] is not None
            result("PASS" if (not res["bare"][0] or clean) else "NOTE", f"RLIMIT_NOFILE={lim}",
                   f"bare works={res['bare'][0]}; recorder refused/ended cleanly={clean}: {msg!r}")


def test_environments():
    variants = {
        "TERM unset": lambda e, r: e.pop("TERM"),
        "TERM=dumb": lambda e, r: e.update(TERM="dumb"),
        "TERM=linux": lambda e, r: e.update(TERM="linux"),
        "TERM=xterm-kitty": lambda e, r: e.update(TERM="xterm-kitty"),
        "HOME unset": lambda e, r: e.pop("HOME"),
        "LANG unset": lambda e, r: (e.pop("LANG"), e.pop("LC_ALL")),
        "LANG=C": lambda e, r: e.update(LANG="C", LC_ALL="C"),
        "SHELL unset": lambda e, r: e.pop("SHELL"),
        "PATH minimal": lambda e, r: e.update(PATH="/usr/bin:/bin"),
        "huge environment (1.5 MB)": lambda e, r: [e.update({f"BIG{i}": "x" * 30000}) for i in range(50)],
        "env var with newline and utf8": lambda e, r: e.update(WEIRD="a\nbé─", **{"X-Y": "1"}),
        "PS1 exported": lambda e, r: e.update(PS1="E> "),
        "PROMPT_COMMAND exported": lambda e, r: e.update(PROMPT_COMMAND="echo exported-pc"),
        "BASH_ENV set": lambda e, r: e.update(BASH_ENV=f"{r}/home/benv"),
        "ENV set": lambda e, r: e.update(ENV=f"{r}/home/benv"),
        "POSIXLY_CORRECT": lambda e, r: e.update(POSIXLY_CORRECT="1"),
    }
    for nm, tw in variants.items():
        outs = {}
        for mode in ("bare", "rec"):
            def tw2(env, root, tw=tw):
                open(f"{root}/home/benv", "w").write("echo from-benv\n")
                tw(env, root)
            run = start(f"envv-{mode}", mode, env_tweak=tw2, expect_prompt=False)
            run.expect(rb"\$ |> ", 0, 8)
            run.settle(0.4, 2)
            pos = len(run.buf)
            run.send(b"echo ENDMARK$((1+1))\n")
            m = run.expect(rb"ENDMARK2\r?\n", pos, 6)
            seg = bytes(run.buf[pos:])
            envs = run.root
            run.send(("env -0 | tr '\\0' '\\n' | grep -a -v -E '^(_|SHLVL|PWD|OLDPWD|PS1|PROMPT_COMMAND|TRECS_RECORDING|ZDOTDIR|TRECS_USER_ZDOTDIR|OBS|TMPDIR|HOME|SHELL)=' | sed 's#" + envs + "#@R@#g' | md5sum | cut -c1-12 | sed 's/^/ENVSUM-/'; echo ENDMARK$((2+1))\n").encode())
            m2 = run.expect(rb"ENDMARK3\r?\n", pos, 8)
            outs[mode] = (bool(m), re.findall(rb"ENVSUM-([0-9a-f]{12})", bytes(run.buf[pos:])), bytes(run.buf)[:200])
            run.send(b"exit\n")
            run.wait(4)
            finish(run)
        same = outs["bare"][1] and outs["bare"][1] == outs["rec"][1]
        if nm in ("TERM=dumb", "TERM unset"):
            result("NOTE", f"environment: {nm}", f"shell works={outs['rec'][0]}; env same as bare={same} (TERM is replaced by xterm-256color here, by design)")
        else:
            result("PASS" if outs["rec"][0] and same else "FAIL", f"environment: {nm}",
                   f"shell started={outs['rec'][0]}; environment inside identical to bare (apart from known)={same}")


def test_resize_storm():
    for mode in ("bare", "rec"):
        run = start(f"rs-{mode}", mode, rc=["trap 'echo -n W' WINCH"])
        run.send(b"sleep 3 & wait; echo\n")
        time.sleep(0.4)
        for i in range(300):
            run.resize(20 + i % 30, 60 + i % 90)
            if i % 20 == 0:
                run.pump(0)
        run.resize(33, 111)
        time.sleep(1.0)
        run.send(b"\x03")
        run.settle(0.3, 2)
        pos = len(run.buf)
        run.send(b"echo SZ-$(stty size)-DONE\n")
        run.expect(rb"SZ-\d+ \d+-DONE", pos, 5)
        seg = bytes(run.buf[pos:])
        sz = re.search(rb"SZ-(\d+ \d+)-DONE", seg)
        setattr(test_resize_storm, mode, sz.group(1) if sz else None)
        finish(run)
    b, r = test_resize_storm.bare, test_resize_storm.rec
    result("PASS" if r == b"33 111" else "FAIL", "300 resizes in a burst, then `stty size`", f"bare {b}, recorded {r}")


def test_throughput_and_latency():
    # flood: time to push N MB through to a reader that does nothing but read (a slow reader would measure itself)
    import select as _sel
    n = 40_000_000
    rates = {}
    for mode in ("bare", "rec"):
        for rep in range(3):
            run = start(f"perf-{mode}", mode)
            run.answer = False
            run.send(f"head -c {n} /dev/zero | tr '\\0' 'a'; echo; echo FLOOD$((1+1))END\n".encode())
            tail, total, t0 = b"", 0, time.time()
            while time.time() - t0 < 120:
                x, _, _ = _sel.select([run.master], [], [], 0.5)
                if not x:
                    continue
                try:
                    d = os.read(run.master, 1 << 20)
                except OSError:
                    break
                total += len(d)
                tail = (tail + d)[-64:]
                if b"FLOOD2END" in tail:
                    break
            rates.setdefault(mode, []).append(total / 1e6 / (time.time() - t0))
            finish(run)
    b, r = max(rates["bare"]), max(rates["rec"])
    result("PASS" if r > b / 4 else "FAIL", "throughput: 40 MB of output", f"bare {b:.0f} MB/s, recorded {r:.0f} MB/s (x{b / r:.2f} slower)")
    # echo latency at the prompt
    lat = {}
    for mode in ("bare", "rec"):
        run = start(f"lat-{mode}", mode)
        run.send(b"cat\n")
        run.settle(0.3, 1)
        xs = []
        for i in range(300):
            ch = bytes([65 + i % 26])
            pos = len(run.buf)
            t0 = time.perf_counter()
            run.send(ch)
            while ch not in run.buf[pos:] and time.perf_counter() - t0 < 1:
                run.pump(0.001)
            xs.append((time.perf_counter() - t0) * 1000)
            time.sleep(0.003)
        lat[mode] = (statistics.median(xs), sorted(xs)[int(len(xs) * 0.99)])
        finish(run)
    (bm, bp), (rm, rp) = lat["bare"], lat["rec"]
    result("PASS" if rm - bm < 5 and rp - bp < 20 else "NOTE", "keystroke echo latency (300 keys)", f"bare p50 {bm:.2f}ms p99 {bp:.2f}ms; recorded p50 {rm:.2f}ms p99 {rp:.2f}ms")


def test_slow_consumer():
    """Output is produced faster than the terminal reads it (a slow ssh link): nothing may be lost or reordered, and ^C must
    still reach the job while the recorder is blocked writing to the terminal."""
    digest = {}
    for mode in ("bare", "rec"):
        run = start(f"slow-{mode}", mode)
        pos = len(run.buf)
        run.send(b"seq 1 600000 | tee /dev/null | md5sum; seq 1 600000; echo SLOWEND\n")
        t0 = time.time()
        run.paused = False
        run.pump(0.2)
        run.paused = True           # stop reading: the recorder's write to us blocks, the pty fills
        time.sleep(2.0)
        run.paused = False
        run.expect(rb"SLOWEND\r?\n", pos, 60)
        data = bytes(run.buf[pos:])
        digest[mode] = hashlib.md5(rf.OSC133.sub(b"", data).split(b"SLOWEND")[0].split(b"\n", 1)[1] if b"\n" in data else data).hexdigest()
        finish(run)
    result("PASS" if digest["bare"] == digest["rec"] else "FAIL", "consumer stops reading for 2 s during a 4 MB flood", f"output digests bare={digest['bare'][:8]} recorded={digest['rec'][:8]}")
    # ^C while blocked
    res = {}
    for mode in ("bare", "rec"):
        run = start(f"slowc-{mode}", mode)
        run.send(b"yes | cat\n")
        time.sleep(0.5)
        run.paused = True
        time.sleep(1.0)
        run.send(b"\x03")
        time.sleep(0.5)
        run.paused = False
        t0 = time.time()
        pos = len(run.buf)
        run.send(b"\necho CTRLC-$((6*7))\n")
        got = run.expect(rb"CTRLC-42\r?\n", pos, 10)
        res[mode] = (bool(got), time.time() - t0)
        finish(run)
    result("PASS" if res["rec"][0] else "FAIL", "^C typed while the consumer is not reading and the output is blocked", f"bare returns={res['bare'][0]} ({res['bare'][1]:.2f}s), recorded returns={res['rec'][0]} ({res['rec'][1]:.2f}s)")


def test_typeahead_and_paste():
    """Input typed while the shell is busy (typeahead) and big pastes arrive as they would without the recorder."""
    sizes = [10, 4000, 70000, 1_000_000]
    for size in sizes:
        data = (os.urandom(size // 2).hex().encode())[:size]
        data = re.sub(rb"(.{80})", rb"\1\n", data)
        got = {}
        for mode in ("bare", "rec"):
            run = start(f"paste-{mode}-{size}", mode)
            run.send(b"sleep 1.5\n")                       # typeahead: the shell is not reading
            time.sleep(0.2)
            run.send(b"cat > pasted.txt\n" + data + b"\n")
            time.sleep(0.2)
            run.send(b"\x04")
            run.settle(0.5, 8)
            pos = len(run.buf)
            run.send(b"echo SHA-$(sha256sum pasted.txt | cut -c1-64)-END\n")
            run.expect(rb"SHA-[0-9a-f]{64}-END", pos, 20)
            h = re.findall(rb"SHA-([0-9a-f]{64})-END", bytes(run.buf[pos:]))
            got[mode] = h[-1][:10] if h else None
            finish(run)
        want = hashlib.sha256(data + b"\n").hexdigest()[:10].encode()
        same = got["bare"] == got["rec"]
        result("PASS" if same else "FAIL", f"typeahead of {size} bytes behind a running command (^D typed later)",
               f"bare stored {got['bare']}, recorded stored {got['rec']}, intact copy would be {want}")


def test_nested_recorder():
    run = start("nested")
    pos = len(run.buf)
    inner = f"{rf.TRECS} record -output {run.root}/out2 -name inner -cmd /usr/bin/bash -no-upload -overwrite"
    run.send((inner + "\n").encode())
    ok = run.expect(rb"recording bash", pos, 8)
    run.settle(0.6, 2)
    pos = len(run.buf)
    run.send(b"echo INNER-$((6*7)); false; echo st=$?\n")
    got = run.expect(rb"(?s)INNER-42.*st=1\r?\n", pos, 5)
    run.send(b"exit\n")
    run.settle(0.8, 4)
    pos = len(run.buf)
    run.send(b"echo OUTER-$((6*7))\n")
    got2 = run.expect(rb"OUTER-42\r?\n", pos, 5)
    result("PASS" if got and got2 else "FAIL", "recorder run inside a recorded shell", f"inner works={bool(got)}, outer back at prompt={bool(got2)}")
    run.send(b"exit\n")
    run.wait(5)
    finish(run)


def test_exec_and_exit_codes():
    for what, send in {
        "exit 7": b"exit 7\n", "exec true": b"exec true\n", "exec false": b"exec false\n", "kill -9 $$": b"kill -9 $$\n",
        "kill -HUP $$": b"kill -HUP $$\n", "exec sleep 0.3": b"exec sleep 0.3\n",
    }.items():
        res = {}
        for mode in ("bare", "rec"):
            run = start(f"ex-{mode}", mode, rc=['trap \'echo "exit-trap:$?" >> $OBS/ex\' EXIT'])
            run.send(send)
            rc = run.wait(6)
            ex = open(f"{run.root}/obs/ex").read().strip() if os.path.exists(f"{run.root}/obs/ex") else "-"
            res[mode] = (rc is not None, ex, rc)
            finish(run)
        same = res["bare"][0] == res["rec"][0] and res["bare"][1] == res["rec"][1]
        result("PASS" if same else "FAIL", f"ending the shell with `{what}`", f"bare ended={res['bare'][0]} trap={res['bare'][1]!r}; recorded ended={res['rec'][0]} trap={res['rec'][1]!r} (recorder exit {res['rec'][2]})")


def test_winsize_and_pixels():
    out = {}
    for mode in ("bare", "rec"):
        run = start(f"ws-{mode}", mode, rows=37, cols=123)
        pos = len(run.buf)
        run.send(b"python3 -c \"import fcntl,termios,struct; print('WS', *struct.unpack('HHHH', fcntl.ioctl(0, termios.TIOCGWINSZ, bytes(8))))\"; stty size\n")
        run.expect(rb"WS \d+ \d+ \d+ \d+\r?\n\d+ \d+", pos, 6)
        m = re.search(rb"WS (\d+ \d+ \d+ \d+)\r?\n(\d+ \d+)", bytes(run.buf[pos:]))
        out[mode] = m.groups() if m else None
        finish(run)
    result("PASS" if out["bare"] == out["rec"] else "FAIL", "window size seen by programs (rows cols xpixel ypixel)", f"bare {out['bare']} recorded {out['rec']}")


def test_job_control_pgrp():
    out = {}
    for mode in ("bare", "rec"):
        run = start(f"pg-{mode}", mode)
        pos = len(run.buf)
        run.send(b"sh -c 'ps -o pid=,pgid=,tpgid=,sid= -p $$' | awk -v me=$PPID '{print ($2==$3), ($4==me), ($2==$1)}'; sleep 5 & ps -o pgid=,tpgid= -p $! | awk '{print ($1==$2)}'; echo PGEND\n")
        run.expect(rb"PGEND\r?\n", pos, 6)
        seg = rf.norm(bytes(run.buf[pos:]), run.root, mode)
        out[mode] = re.findall(rb"\n([01] [01] [01]|[01])\r?\n", seg)
        finish(run)
    result("PASS" if out["bare"] == out["rec"] else "FAIL", "process groups: foreground job owns the terminal, background job does not", f"bare {out['bare']} recorded {out['rec']}")


def test_terminal_state_after_exit():
    for how, send in {"exit": b"exit\n", "ctrl-d": b"\x04", "stty raw then exit": b"stty raw -echo; exit\n", "exit in vim-like raw mode": b"stty -icanon -echo -isig; exit\n"}.items():
        run = start("tstate")
        pristine = run.pristine
        run.send(send)
        run.wait(5)
        t = run.termios()
        d = rf.termios_diff(t, pristine) if isinstance(t, list) else None
        result("PASS" if not d else "FAIL", f"outer terminal settings after the session ends ({how})", f"differences from before: {d}")
        finish(run)


def test_stale_state_between_sessions():
    """Two recorded sessions after each other in one directory: nothing from the first leaks into the second."""
    root, env = sandbox("twice")
    envs = []
    for i in range(2):
        argv = [rf.TRECS, "record", "-output", f"{root}/out", "-name", f"s{i}", "-cmd", "/usr/bin/bash", "-no-upload", "-overwrite"]
        run = Run(argv, env, f"{root}/cwd")
        run.root = root
        run.expect(rb"P> ", 0, 8)
        run.settle(0.3, 1)
        pos = len(run.buf)
        run.send(b"env | grep -c TRECS; echo ${PROMPT_COMMAND}; ls -d /tmp/trecs-shell-* 2>/dev/null | wc -l; ls $TMPDIR | grep -c trecs\n")
        run.settle(0.4, 2)
        envs.append(rf.norm(bytes(run.buf[pos:]), root, "R")[-120:])
        run.send(b"exit\n")
        run.wait(5)
        finish(run)
    result("PASS" if envs[0].split(b"s{")[0] == envs[1].split(b"s{")[0] else "NOTE", "two sessions in a row see the same environment", f"{envs[0][-80:]!r} / {envs[1][-80:]!r}")


def test_shell_flavours():
    """-cmd variants: relative name, a login shell flag in the path, a shell the recorder cannot mark, a missing shell."""
    for nm, cmd in {"bare name 'bash'": "bash", "relative './sh'": None, "unmarked shell 'dash'": "/usr/bin/dash", "missing shell": "/usr/bin/nonexistent-shell", "sh": "/bin/sh"}.items():
        root, env = sandbox("flavour")
        if cmd is None:
            os.symlink("/usr/bin/bash", f"{root}/cwd/sh")
            cmd = "./sh"
        argv = [rf.TRECS, "record", "-output", f"{root}/out", "-name", "s", "-cmd", cmd, "-no-upload", "-overwrite", "-allow-unmarked"]
        run = Run(argv, env, f"{root}/cwd")
        run.root = root
        run.settle(1.0, 3)
        run.send(b"echo FLAV-$((1+1))\n")
        got = run.expect(rb"FLAV-2\r?\n", 0, 3)
        run.send(b"exit\n")
        rc = run.wait(5)
        result("PASS" if got or (nm == "missing shell" and rc not in (None, 0)) else "FAIL", f"-cmd {nm}", f"shell works={bool(got)}, recorder exit={rc}")
        finish(run)


def test_job_control_pgrp():
    """The foreground job owns the terminal (its group is the terminal's foreground group); a background job does not."""
    out = {}
    for mode in ("bare", "rec"):
        run = start(f"pg-{mode}", mode)
        pos = len(run.buf)
        run.send(b"sh -c 'echo FG-$(ps -o pgid= -p $$ | tr -d \" \")-$(ps -o tpgid= -p $$ | tr -d \" \")-$$'; sleep 5 & echo BG-$(ps -o pgid= -p $! | tr -d \" \")-$(ps -o tpgid= -p $! | tr -d \" \")-$!; echo PGEND$((1+1))\n")
        run.expect(rb"PGEND2", pos, 6)
        seg = bytes(run.buf[pos:])
        fg = re.search(rb"FG-(\d+)-(\d+)-(\d+)", seg)
        bg = re.search(rb"BG-(\d+)-(\d+)-(\d+)", seg)
        out[mode] = (bool(fg) and fg.group(1) == fg.group(2) == fg.group(3), bool(bg) and bg.group(1) != bg.group(2) or bool(bg) and bg.group(1) == bg.group(3) and bg.group(1) != bg.group(2))
        finish(run)
    result("PASS" if out["bare"] == out["rec"] and out["rec"][0] else "FAIL", "process groups: the foreground job owns the terminal", f"(fg job is the terminal's foreground group, bg job is not) bare {out['bare']} recorded {out['rec']}")


def test_slow_consumer():
    """Output is produced faster than the terminal reads it (a slow ssh link): nothing may be lost or reordered, and ^C must
    still reach the job while the recorder is blocked writing to the terminal."""
    digest, sizes = {}, {}
    for mode in ("bare", "rec"):
        run = start(f"slow-{mode}", mode)
        pos = len(run.buf)
        run.send(b"echo S-$((1+1))-S; seq 1 600000; echo E-$((1+1))-E\n")
        run.pump(0.2)
        run.paused = True           # stop reading: the recorder's write to us blocks, the pty fills
        time.sleep(2.0)
        run.paused = False
        run.expect(rb"E-2-E", pos, 60)
        data = bytes(run.buf[pos:])
        m = re.search(rb"S-2-S\r?\n(.*)E-2-E", rf.OSC133.sub(b"", data), re.S)
        body = m.group(1) if m else b""
        digest[mode] = hashlib.md5(body).hexdigest()
        sizes[mode] = len(body)
        finish(run)
    result("PASS" if digest["bare"] == digest["rec"] and sizes["bare"] > 3_000_000 else "FAIL", "consumer stops reading for 2 s during a 4 MB flood",
           f"bytes bare={sizes['bare']} recorded={sizes['rec']}; digests bare={digest['bare'][:8]} recorded={digest['rec'][:8]}")
    res = {}
    for mode in ("bare", "rec"):
        run = start(f"slowc-{mode}", mode)
        run.send(b"yes | cat\n")
        time.sleep(0.5)
        run.paused = True
        time.sleep(1.0)
        run.send(b"\x03")
        time.sleep(0.5)
        run.paused = False
        t0 = time.time()
        pos = len(run.buf)
        run.send(b"\necho CTRLC-$((6*7))\n")
        got = run.expect(rb"CTRLC-42\r?\n", pos, 10)
        res[mode] = (bool(got), time.time() - t0)
        finish(run)
    result("PASS" if res["rec"][0] else "FAIL", "^C typed while the consumer is not reading and the output is blocked", f"bare returns={res['bare'][0]} ({res['bare'][1]:.2f}s), recorded returns={res['rec'][0]} ({res['rec'][1]:.2f}s)")


def main():
    os.makedirs(WORK, exist_ok=True)
    only = sys.argv[1:]
    tests = [(n[5:], f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for nm, f in tests:
        if only and nm not in only:
            continue
        print(f"\n## {nm}: {(f.__doc__ or '').strip().splitlines()[0] if f.__doc__ else ''}", flush=True)
        try:
            f()
        except Exception:
            import traceback
            result("ERROR", nm, traceback.format_exc()[-600:])
    print("\n==== summary ====")
    for k in ("PASS", "FAIL", "NOTE", "ERROR"):
        print(k, sum(1 for s, _, _ in RESULTS if s == k))
    for s, n, d in RESULTS:
        if s in ("FAIL", "ERROR"):
            print(f"{s}: {n}\n   {d}")


if __name__ == "__main__":
    main()
