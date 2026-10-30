"""A small pty runner: starts a program on a pseudo-terminal of a given size, plays the part of the
terminal emulator (answers the queries a program may send), feeds it steps and collects every byte it prints.

The same runner starts the bare shell and the shell under the recorder, so any difference between the
two runs comes from the recorder and not from the harness.
"""
import errno
import fcntl
import os
import re
import select
import shutil
import signal
import struct
import subprocess
import sys
import termios
import time

# The harness reads /proc where it can and asks ps and lsof where it cannot (macOS), and finds the shells and the GNU tools the
# sessions use (md5sum, sha256sum, setsid) on the machine it runs on.
IS_LINUX = sys.platform.startswith("linux")
LOCALE = "C.UTF-8" if IS_LINUX else "en_US.UTF-8"
_HERE = os.path.dirname(os.path.abspath(__file__))


def find_shell(name):
    """The shell to test: $TRECS_FUZZ_BASH (ZSH, FISH) if set, else the first on PATH, else /usr/bin. macOS ships bash 3.2, which cannot
    mark its prompts: put a newer one first (brew install bash) or name it in TRECS_FUZZ_BASH."""
    return os.environ.get("TRECS_FUZZ_" + name.upper()) or shutil.which(name) or "/usr/bin/" + name


SHELLS = {n: find_shell(n) for n in ("bash", "zsh", "fish")}


def preflight(shells=None, binaries=()):
    """Exit once, with one message, if a program the harness runs is missing, instead of failing in every session. shells: the names
    in use (default all); binaries: paths that must be executable (the recorder). ps and lsof are needed only where /proc cannot be
    read (everywhere but Linux); mount and umount are optional (without them the disk-full test uses a file size limit instead)."""
    missing = []
    for n in shells or SHELLS:
        if n not in SHELLS:
            missing.append(f"{n}: not a shell the harness knows (bash, zsh, fish)")
        elif not (os.path.isfile(SHELLS[n]) and os.access(SHELLS[n], os.X_OK)):
            missing.append(f"{n}: {SHELLS[n]} is not an executable (set TRECS_FUZZ_{n.upper()})")
    for b in binaries:
        if not (os.path.isfile(b) and os.access(b, os.X_OK)):
            missing.append(f"{b} is not an executable")
    for n in ("python3",) if IS_LINUX else ("python3", "ps", "lsof"):
        if shutil.which(n) is None:
            missing.append(f"{n}: not on PATH" + (" (install it: pkg install lsof, apt install lsof, ...)" if n == "lsof" else ""))
    if missing:
        sys.exit("The harness cannot run on this system:\n  " + "\n  ".join(missing))


def shim_dir():
    """A directory of stand-ins for the commands the sessions type that this system does not have: md5sum, sha256sum, setsid.
    Created on first use; "" if nothing is missing (Linux)."""
    need = {n: shutil.which(n) is None for n in ("md5sum", "sha256sum", "setsid")}
    if not any(need.values()):
        return ""
    d = os.path.join(_HERE, "shims")
    os.makedirs(d, exist_ok=True)
    body = {
        "md5sum": "#!/bin/sh\nexec md5 -r \"$@\"\n",
        "sha256sum": "#!/bin/sh\nexec shasum -a 256 \"$@\"\n",
        "setsid": "#!/usr/bin/env python3\nimport os, sys\nos.setsid() if os.getpid() != os.getpgid(0) else None\nos.execvp(sys.argv[1], sys.argv[1:])\n",
    }
    for n, missing in need.items():
        if missing:
            f = os.path.join(d, n)
            with open(f, "w") as fh:
                fh.write(body[n])
            os.chmod(f, 0o755)
    return d


def tool_path():
    """PATH for the sessions: the shells' directories and python3's first (they are not in /usr/bin on macOS), then the usual."""
    dirs = [shim_dir()]
    for tool in [*SHELLS.values(), shutil.which("python3") or ""]:
        if tool:
            dirs.append(os.path.dirname(tool))
    for extra in ("/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"):
        if os.path.isdir(extra):
            dirs.append(extra)
    seen = []
    for d in dirs:
        if d and d not in seen:
            seen.append(d)
    return ":".join(seen)


def is_zombie(pid):
    """True if the process is gone or has ended and not been waited for."""
    try:
        os.kill(pid, 0)
    except OSError:
        return True
    if IS_LINUX:
        try:
            with open(f"/proc/{pid}/stat") as f:
                return f.read().split(")")[-1].split()[0] == "Z"
        except OSError:
            return True
    r = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], capture_output=True, text=True)
    return r.returncode != 0 or r.stdout.strip().startswith("Z") or not r.stdout.strip()

ANSWERS = [  # what a terminal answers to what a program asks it
    (re.compile(rb"\x1b\[6n"), b"\x1b[5;10R"),              # cursor position
    (re.compile(rb"\x1b\[c"), b"\x1b[?62;c"),               # primary device attributes
    (re.compile(rb"\x1b\[0c"), b"\x1b[?62;c"),
    (re.compile(rb"\x1b\[>c"), b"\x1b[>1;10;0c"),           # secondary device attributes
    (re.compile(rb"\x1b\[>0c"), b"\x1b[>1;10;0c"),
    (re.compile(rb"\x1b\]11;\?(?:\x07|\x1b\\)"), b"\x1b]11;rgb:0000/0000/0000\x1b\\"),
    (re.compile(rb"\x1b\]10;\?(?:\x07|\x1b\\)"), b"\x1b]10;rgb:ffff/ffff/ffff\x1b\\"),
    (re.compile(rb"\x1b\[5n"), b"\x1b[0n"),                 # device status
]


def set_winsize(fd, rows, cols):
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


class Run:
    def __init__(self, argv, env, cwd, rows=30, cols=100, answer=True, preexec=None, tty=None):
        self.argv, self.env, self.cwd = argv, env, cwd
        self.master, slave = os.openpty()
        os.set_blocking(self.master, False)
        self.deadline = time.time() + 240      # a whole session may not take longer than this
        set_winsize(slave, rows, cols)
        if tty:
            attrs = termios.tcgetattr(slave)
            tty(attrs)
            termios.tcsetattr(slave, termios.TCSANOW, attrs)
        self.pristine = termios.tcgetattr(slave)

        def child():
            # a harness started in the background inherits SIGINT/SIGQUIT/SIGHUP as ignored; the shell under test must start with
            # the dispositions of a person's terminal session, in the bare run and in the recorded one alike
            for sg in (signal.SIGINT, signal.SIGQUIT, signal.SIGHUP, signal.SIGTERM, signal.SIGTSTP, signal.SIGPIPE):
                signal.signal(sg, signal.SIG_DFL)
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)
            if preexec:
                preexec()

        self.p = subprocess.Popen(argv, env=env, cwd=cwd, stdin=slave, stdout=slave, stderr=slave,
                                  close_fds=True, preexec_fn=child)
        os.close(slave)
        self.buf = bytearray()
        self.answered = 0          # how far in buf the queries have been looked at
        self.answer = answer
        self.eof = False
        self.paused = False
        self.hold = False          # while set, the answers to queries are kept, not sent (see release)
        self.held = []

    # -- reading
    def pump(self, timeout=0.05):
        """Read what is there; answer terminal queries. Returns the number of bytes read."""
        if time.time() > self.deadline:
            raise TimeoutError("session deadline")
        if self.eof or self.paused:
            time.sleep(timeout)
            return 0
        n = 0
        while True:
            r, _, _ = select.select([self.master], [], [], timeout)
            if not r:
                break
            try:
                d = os.read(self.master, 65536)
            except BlockingIOError:
                break
            except OSError as e:
                if e.errno == errno.EIO:
                    self.eof = True
                    break
                raise
            if not d:
                self.eof = True
                break
            self.buf += d
            n += len(d)
            timeout = 0
        if self.answer and n:
            self._answer()
        return n

    def _answer(self):
        if b'\x1b' not in self.buf[max(0, self.answered - 16):]:
            self.answered = len(self.buf)
            return
        tail = bytes(self.buf[max(0, self.answered - 16):])
        base = max(0, self.answered - 16)
        for rx, reply in ANSWERS:
            for m in rx.finditer(tail):
                if base + m.end() > self.answered:
                    if self.hold:
                        self.held.append(reply)
                        continue
                    try:
                        os.write(self.master, reply)
                    except OSError:
                        pass
        self.answered = len(self.buf)

    def release(self):
        """Stop holding the answers, and send the ones kept. A terminal answers a query when it is asked, and the shell that asked reads
        the answer; one that comes late, with a program running that reads its input, is typed into that program."""
        self.hold = False
        held, self.held = self.held, []
        for reply in held:
            try:
                os.write(self.master, reply)
            except OSError:
                pass

    def expect(self, rx, start=0, timeout=10.0):
        """Wait until rx (bytes regex) matches in buf[start:]. Returns the match or None. Looks only at what is new
        (plus a little before it, as a match may begin there): a flood of output must not be searched again at every read."""
        rx = re.compile(rx) if isinstance(rx, bytes) else rx
        end = time.time() + timeout
        seen = start
        while True:
            m = rx.search(self.buf, max(start, seen - 512))
            seen = len(self.buf)
            if m:
                return m
            if self.eof or time.time() > end:
                return None
            self.pump(0.05)

    def settle(self, quiet=0.15, limit=3.0):
        end = time.time() + limit
        last = time.time()
        while time.time() < end and time.time() - last < quiet:
            if self.pump(0.03):
                last = time.time()
            if self.eof:
                break

    # -- writing
    def send(self, data, chunk=None, gap=0.0):
        if chunk is None:
            self._write(data)
            return
        for i in range(0, len(data), chunk):
            self._write(data[i:i + chunk])
            if gap:
                time.sleep(gap)
            self.pump(0)

    def _write(self, data, stall=30.0):
        view = memoryview(data)
        last = time.time()
        while len(view):
            if time.time() - last > stall or time.time() > self.deadline:
                raise TimeoutError("input stalled: the program is not reading")
            try:
                _, w, _ = select.select([], [self.master], [], 0.05)
                if not w:
                    self.pump(0)       # the program is not reading: keep emptying its output
                    continue
                k = os.write(self.master, view[:4096])
                view = view[k:]
                last = time.time()
            except BlockingIOError:
                self.pump(0)
            except OSError as e:
                if e.errno in (errno.EIO, errno.EPIPE):
                    return
                raise

    def resize(self, rows, cols):
        set_winsize(self.master, rows, cols)

    def termios(self):
        try:
            return termios.tcgetattr(self.master)
        except Exception as e:
            return repr(e)

    # -- ending
    def wait(self, timeout=8.0):
        end = time.time() + timeout
        while time.time() < end:
            self.pump(0.05)
            if self.p.poll() is not None:
                break
        else:
            return None
        self.settle(0.1, 1.0)
        return self.p.returncode

    def kill(self):
        try:
            os.killpg(self.p.pid, signal.SIGKILL)
        except Exception:
            pass
        try:
            self.p.kill()
        except Exception:
            pass
        try:
            self.p.wait(2)
        except Exception:
            pass

    def close(self):
        self.eof = True        # nothing more can be read: a later wait() only looks at the process
        try:
            os.close(self.master)
        except OSError:
            pass


def processes_in(path):
    """pids of the processes whose working directory or command line lies under path (leftovers of a run)."""
    out = []
    me = os.getpid()
    if not IS_LINUX:
        cwds = {}
        r = subprocess.run(["lsof", "-a", "-d", "cwd", "-Fpn"], capture_output=True, text=True)
        pid = None
        for line in r.stdout.splitlines():
            if line.startswith("p"):
                pid = int(line[1:])
            elif line.startswith("n") and pid is not None:
                cwds[pid] = line[1:]
        r = subprocess.run(["ps", "-axo", "pid=,command="], capture_output=True, text=True)
        for line in r.stdout.splitlines():
            parts = line.strip().split(None, 1)
            if len(parts) != 2 or not parts[0].isdigit() or int(parts[0]) == me:
                continue
            pid, cmd = int(parts[0]), parts[1]
            if cwds.get(pid, "").startswith(path) or path in cmd:
                out.append((pid, cmd.strip()[:80]))
        return out
    for d in os.listdir("/proc"):
        if not d.isdigit() or int(d) == me:
            continue
        try:
            cwd = os.readlink(f"/proc/{d}/cwd")
            cmd = open(f"/proc/{d}/cmdline", "rb").read().replace(b"\0", b" ").decode("latin1")
        except OSError:
            continue
        if cwd.startswith(path) or path in cmd:
            out.append((int(d), cmd.strip()[:80]))
    return out


_CR_DOUBLING = None


def pty_cr_doubling():
    """How many times a bare pty, with no recorder anywhere, turned "\\n" into "\\r\\r\\n" in a few floods read slowly. 0 on Linux. On BSD
    kernels (macOS) the line discipline can write the "\\r" of an NL -> CRNL translation, find its queue full, and write the "\\r" again
    when the "\\n" is retried: a stream that is read slower than it is written then has a doubled CR now and then. That is the OS, and a
    comparison between a bare shell and a recorded one has to know it is there (the recorded shell has two ptys, and is more exposed).
    Measured once per process, only where it can happen."""
    global _CR_DOUBLING
    if _CR_DOUBLING is not None:
        return _CR_DOUBLING
    if IS_LINUX:
        _CR_DOUBLING = 0
        return 0
    import pty
    total = 0
    for _ in range(3):
        pid, fd = pty.fork()
        if pid == 0:
            os.execv("/bin/sh", ["sh", "-c", "seq 1 400000"])
        buf = bytearray()
        end = time.time() + 20
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.5)
            if not r:
                break
            try:
                data = os.read(fd, 700)
            except OSError:
                break
            if not data:
                break
            buf += data
            time.sleep(0.0004)  # slower than the flood: the pty's queue fills
        # The master is closed FIRST: a process that is killed while its output is waiting in the pty does not finish exiting until
        # the pty is drained or closed (BSD waits for it as the slave closes), so a wait with the master still open never ends.
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
        give_up = time.time() + 5
        while time.time() < give_up:
            try:
                done, _ = os.waitpid(pid, os.WNOHANG)
            except OSError:
                break
            if done:
                break
            time.sleep(0.05)
        total += bytes(buf).count(b"\r\r\n")
    _CR_DOUBLING = total
    return total


def undouble_cr(b):
    """b with the CRs that the OS doubled taken out, if this OS is seen to double them (see pty_cr_doubling)."""
    return b.replace(b"\r\r\n", b"\r\n") if pty_cr_doubling() > 0 else b
