import sys, os, re, shutil
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import recfuzz as rf
from ptyrun import Run
out = {}
for mode in ("bare", "rec"):
    root = os.path.join(rf.HERE, "adhocwork", "fishdef-" + mode); shutil.rmtree(root, ignore_errors=True)
    for d in ("home", "tmp", "obs", "cwd", "out"): os.makedirs(f"{root}/{d}")
    os.makedirs(f"{root}/home/.config/fish")
    open(f"{root}/home/.config/fish/config.fish", "w").write("set -g fish_greeting\n")   # nothing else: fish's own prompt
    env = rf.env_for("fish", root)
    argv = ["/usr/bin/fish"] if mode == "bare" else [rf.TRECS, "record", "-output", f"{root}/out", "-name", "s", "-cmd", "/usr/bin/fish", "-no-upload", "-overwrite"]
    r = Run(argv, env, f"{root}/cwd")
    r.settle(1.5, 4)
    r.send(b"echo hi\n"); r.settle(0.5, 2)
    out[mode] = bytes(r.buf)
    r.send(b"exit\n"); r.wait(5); r.kill(); r.close()
for m in out:
    s = rf.OSC133.sub(b"", out[m]); s = rf.BANNER.sub(b"", s)
    print("---", m, "marks:", len(rf.OSC133.findall(out[m])))
    print(re.sub(rb"\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b\(B", b"", s)[:300].decode("latin1").replace("\r", "\\r"))
