import argparse
import json
import shutil
import subprocess
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=Path.home() / "tmp/arbiter-work/yamnaya-mutants")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    args.output.mkdir(parents=True, exist_ok=True)
    variants = [
        ("stale-judgment", "internal/work/store.go", "if e.Expected != x.Revision {", "if false {", "TestConflictingContextEdits"),
        ("lost-feedback", "internal/work/event.go", "r.Feedback, r.Judgment = old.Feedback, old.Judgment", 'r.Feedback, r.Judgment = "", old.Judgment', "TestHumanReview"),
        ("old-source-ready", "internal/work/event.go", 'r.State, r.PreparedVersion = s.PreparationState(), ""\n\t\tr.Updated = e.At', "r.State, r.PreparedVersion = old.State, old.PreparedVersion\n\t\tr.Updated = e.At", "TestStaleJudgments"),
        ("agent-can-decide", "internal/work/event.go", 'if e.Actor != "owner" {', "if false {", "TestStaleJudgmentsAndAuthority"),
        ("future-input-checkpoint", "internal/work/agent_store.go", "if revision > current.Revision {", "if false {", "TestManagement"),
        ("empty-management-plan", "internal/work/agent_store.go", 'if strings.TrimSpace(c.Next) == "" && strings.TrimSpace(c.WaitFor) == "" {', "if false {", "TestManagement"),
        ("obsolete-result-verification", "internal/work/event.go", 's.ResultContext != s.Context.Revision || ', '', "TestDelegator"),
    ]
    results = []
    for name, filename, old, new, test in variants:
        target = args.output / name
        if target.exists():
            shutil.rmtree(target)
        shutil.copytree(root, target, ignore=shutil.ignore_patterns(".git", "ym", "__pycache__"))
        path = target / filename
        source = path.read_text()
        if source.count(old) != 1:
            raise RuntimeError(f"{name}: mutation does not identify exactly one operation")
        path.write_text(source.replace(old, new))
        run = subprocess.run(["go", "test", "./internal/work", "-run", test, "-count=1"], cwd=target, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        (target / "result.log").write_text(run.stdout)
        caught = run.returncode != 0 and "--- FAIL:" in run.stdout and "[build failed]" not in run.stdout
        results.append({"mutation": name, "caught_by_behavior": caught, "test": test})
        print(f"{name}: {'caught' if caught else 'NOT CAUGHT'}", flush=True)
    (args.output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    if not all(row["caught_by_behavior"] for row in results):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
