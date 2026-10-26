"""Quick differential experiments: adhoc.py <shell> '<rc line>' 'input1' 'input2' ...   (inputs get \\n appended; \\e,\\x03 etc. escapes understood)"""
import sys, os, re, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import recfuzz as rf
from ptyrun import Run

def session(shell, rc, inputs, mode, binary=None, tag="adhoc"):
    root = os.path.join(rf.HERE, "adhocwork", f"{tag}-{mode}")
    import shutil; shutil.rmtree(root, ignore_errors=True)
    for d in ("home", "tmp", "obs", "cwd", "out"): os.makedirs(f"{root}/{d}")
    rf.write_profile(shell, f"{root}/home", rc)
    env = rf.env_for(shell, root)
    sh = rf.SHELLS[shell]
    argv = [sh] if mode == "bare" else [binary or rf.TRECS, "record", "-output", f"{root}/out", "-name", "s", "-cmd", sh, "-no-upload", "-overwrite"]
    r = Run(argv, env, f"{root}/cwd")
    r.expect(rb"P> ", 0, 8); r.settle(0.4, 2)
    pos = len(r.buf)
    for i in inputs:
        r.send(i.encode().decode("unicode_escape").encode("latin1") + (b"" if i.endswith(("\\x03", "\\x04")) else b"\n"))
        r.settle(0.3, 3)
    alive = None
    r.send(b"printf 'ALIVE%d\\n' 1\n"); alive = bool(r.expect(rb"ALIVE1\r?\n", pos, 3))
    r.settle(0.3, 2)
    out = bytes(r.buf[pos:])
    r.send(b"exit\n"); r.wait(5)
    left = r.p.poll()
    r.kill(); r.close()
    return rf.norm(out, root, mode), alive

if __name__ == "__main__":
    shell = sys.argv[1]; rc = [x for x in sys.argv[2].split(";;") if x]; inputs = sys.argv[3:]
    res = {}
    for mode in ("bare", "rec"):
        res[mode] = session(shell, rc, inputs, mode)
    for mode in ("bare", "rec"):
        print(f"--- {mode}: shell still answering at the end: {res[mode][1]}")
        print(res[mode][0].decode("latin1").replace("\r", ""))
    print("IDENTICAL" if res["bare"] == res["rec"] else "DIFFERENT")
