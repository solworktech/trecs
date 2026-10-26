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
import signal
import struct
import subprocess
import termios
import time

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
                    try:
                        os.write(self.master, reply)
                    except OSError:
                        pass
        self.answered = len(self.buf)

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
