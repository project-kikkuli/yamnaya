import ast
import builtins
import contextlib
import inspect
import json
import os
import runpy
import sys
import tempfile

source, mode, request_path = sys.argv[1:]
with open(request_path, encoding="utf-8") as handle:
    request = json.load(handle)
module = runpy.run_path(source, run_name="_ym_cs_profile")
required = ("Session", "do_port", "source_turns", "claude_turns", "codex_turns", "write_codex_rollout", "load_codex")
if any(not callable(module.get(name)) for name in required):
    raise SystemExit("installed cs does not expose the inspected profile-port interface")
globals_ = module["do_port"].__globals__
if module["do_port"].__code__.co_argcount != 3:
    raise SystemExit("installed cs has an incompatible do_port interface")
globals_.update(CLAUDE=request["claude_root"], CODEX=request["codex_root"], CACHE=request["cache"], _YM_CODEX_SQLITE=request["sqlite_path"])

def rewrite_database(node, changed):
    if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == "join" and len(node.args) == 2 and isinstance(node.args[0], ast.Name) and node.args[0].id == "CODEX" and isinstance(node.args[1], ast.Constant) and node.args[1].value == "state_5.sqlite":
        changed[0] += 1
        return ast.copy_location(ast.Name(id="_YM_CODEX_SQLITE", ctx=ast.Load()), node)
    for field, value in ast.iter_fields(node):
        if isinstance(value, list):
            setattr(node, field, [rewrite_database(item, changed) if isinstance(item, ast.AST) else item for item in value])
        elif isinstance(value, ast.AST):
            setattr(node, field, rewrite_database(value, changed))
    return node

for name in ("load_codex", "write_codex_rollout"):
    tree = ast.parse(inspect.getsource(module[name]))
    changed = [0]
    tree = rewrite_database(tree, changed)
    if changed[0] != 1:
        raise SystemExit("installed cs database interface changed: " + name)
    exec(compile(ast.fix_missing_locations(tree), source, "exec"), globals_)

parser = globals_[request["harness"] + "_turns"]
turns, _, _ = parser(request["transcript"])
if not turns:
    raise SystemExit("selected conversation has no portable prompts or replies; source is unchanged")
if mode == "check":
    raise SystemExit(0)
if mode != "port":
    raise SystemExit("unknown profile-port operation")

@contextlib.contextmanager
def atomic_transcript(path, mode, args, kwargs):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".ym-port-", dir=os.path.dirname(path))
    os.close(descriptor)
    try:
        with builtins.open(temporary, mode, *args, **kwargs) as handle:
            yield handle
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory = os.open(os.path.dirname(path), os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def profile_open(path, mode="r", *args, **kwargs):
    if mode == "w" and str(path).endswith(".jsonl") and os.path.commonpath((os.path.abspath(path), request["target_root"])) == request["target_root"]:
        return atomic_transcript(path, mode, args, kwargs)
    return builtins.open(path, mode, *args, **kwargs)


globals_["open"] = profile_open
session = globals_["Session"](request["harness"], request["session"], request["cwd"], 0, [], request["transcript"])
print(globals_["do_port"](session, {session.id: session}, request["to"]))
