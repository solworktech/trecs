import sys, os, re, termios
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import recfuzz as rf
from ptyrun import Run
import shutil

def custom(a):
    a[0] &= ~(termios.IXON | termios.IXOFF)          # stty -ixon -ixoff  (free Ctrl-S / Ctrl-Q)
    a[0] &= ~termios.IUTF8                           # not a UTF-8 terminal
    a[3] &= ~termios.ECHOCTL                         # stty -echoctl
    a[3] &= ~termios.ECHOE | 0
    a[6][termios.VERASE] = b"\x08"                   # erase = ^H
    a[6][termios.VINTR] = b"\x14"                    # intr = ^T
    a[6][termios.VWERASE] = b"\x1f"
    a[1] &= ~termios.OPOST                           # raw output processing off (no \n -> \r\n)

res = {}
for mode in ("bare", "rec"):
    root = os.path.join(rf.HERE, "adhocwork", "tty-" + mode); shutil.rmtree(root, ignore_errors=True)
    for d in ("home", "tmp", "obs", "cwd", "out"): os.makedirs(f"{root}/{d}")
    rf.write_profile("bash", f"{root}/home", [])
    env = rf.env_for("bash", root)
    argv = ["/usr/bin/bash"] if mode == "bare" else [rf.TRECS, "record", "-output", f"{root}/out", "-name", "s", "-cmd", "/usr/bin/bash", "-no-upload", "-overwrite"]
    r = Run(argv, env, f"{root}/cwd", tty=custom)
    r.expect(rb"P> ", 0, 8); r.settle(0.4, 2)
    pos = len(r.buf)
    r.send(b"echo STTY-$(stty -a | tr '\\n' ' ' | md5sum | cut -c1-8)-END; stty -a | tr -s ' \\n' ' ' | grep -o -E '(intr|erase|werase) = [^;]*;|-?ixon|-?ixoff|-?iutf8|-?echoctl|-?opost' | tr '\\n' ' '; echo; echo DONE$((1+1))\n")
    r.expect(rb"DONE2", pos, 5); r.settle(0.2, 1)
    out = rf.OSC133.sub(b"", bytes(r.buf[pos:])).decode("latin1")
    res[mode] = out
    r.send(b"exit\n"); r.wait(4); r.kill(); r.close()
for m in res: print("---", m, "\n", res[m].replace("\r", ""))
