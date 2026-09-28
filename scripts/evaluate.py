#!/usr/bin/env python3
"""Compare committed regression tests against pinned baseline and HEAD, offline."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tarfile
import tempfile

BASELINE = "23e702926408c789ce0129ce25dc6df93613d173"
GROUPS = (
    ("./pkg/adapter/scim", "TestApplyRejectsIncompleteOrAmbiguousLookup",
     "pkg/adapter/scim/scim_test.go", "unsafe lookup"),
    ("./pkg/reconcile", "TestConvergeDistinguishesMissingAndEmptyAttributes",
     "pkg/reconcile/equivalence_test.go", "got action=unchanged writes=0; want action=updated writes=1"),
    ("./pkg/reconcile", "TestCanonicalizeRejectsAmbiguousAttributeNames",
     "pkg/reconcile/collision_test.go", "got error <nil>; want"),
    ("./cmd/reconcile", "TestReadEventsPreservesLargeNumericIdentifiers",
     "cmd/reconcile/main_test.go", 'employeeId = "9007199254740992", want "9007199254740993"'),
)


def git(repo, *args):
    return subprocess.check_output(["git", "-C", str(repo), *args])


def snapshot(repo, revision, destination):
    archive = git(repo, "archive", "--format=tar", revision)
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as source:
        for member in source:
            name = PurePosixPath(member.name)
            if name.is_absolute() or ".." in name.parts:
                raise ValueError("unsafe archive path: " + member.name)
            target = destination.joinpath(*name.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                with source.extractfile(member) as content:
                    target.write_bytes(content.read())
                target.chmod(member.mode & 0o777)
            else:
                raise ValueError("unsupported archive entry: " + member.name)


def run_group(go, source, package, name, marker, expected, environment):
    command = [go, "test", "-json", "-count=1", "-timeout=60s", package,
               "-run", "^" + name + "$"]
    process = subprocess.run(command, cwd=source, env=environment,
                             capture_output=True, text=True, timeout=120)
    events = [json.loads(line) for line in process.stdout.splitlines() if line]
    outcomes = {event["Test"]: event["Action"] for event in events
                if "Test" in event and event["Action"] in ("pass", "fail", "skip")}
    package_outcomes = [event["Action"] for event in events
                        if "Test" not in event and event["Action"] in ("pass", "fail")]
    output = "".join(event.get("Output", "") for event in events)
    assertion_seen = any(marker in event.get("Output", "")
                         and event.get("Test", "").split("/")[0] == name
                         for event in events)
    build_error = any(event["Action"] in ("build-fail", "build-output") for event in events)
    unrelated = [test for test in outcomes if test.split("/")[0] != name]
    valid = (outcomes.get(name) == expected and package_outcomes == [expected]
             and process.returncode == (0 if expected == "pass" else 1)
             and not build_error and not unrelated
             and "panic:" not in output and "[build failed]" not in output)
    if expected == "fail":
        valid = valid and assertion_seen
    else:
        valid = valid and all(value == "pass" for value in outcomes.values())
    return {"test": name, "package": package, "command": command,
            "expected": expected, "observed": outcomes.get(name),
            "returncode": process.returncode, "verified": bool(valid),
            "expected_failure_assertion_seen": assertion_seen,
            "test_outcomes": outcomes, "output": output, "stderr": process.stderr}


def evaluate(candidate_ref):
    repo = Path(__file__).resolve().parent.parent
    candidate = git(repo, "rev-parse", "--verify", candidate_ref + "^{commit}").decode().strip()
    go = shutil.which("go")
    if go is None:
        raise RuntimeError("Go is required on PATH")
    environment = os.environ.copy()
    # Never fetch dependencies or a different Go toolchain during evaluation.
    environment.update(GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local",
                       GOWORK="off", GOFLAGS="")
    report = {"schema_version": 1, "baseline": BASELINE, "candidate": candidate,
              "runner_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              "go_version": subprocess.check_output([go, "version"], env=environment, text=True).strip(),
              "network_policy": "GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local; SCIM tests use loopback fixtures",
              "test_sources": [], "sources": {}}
    with tempfile.TemporaryDirectory(prefix="reconciliation-evaluation-") as temporary:
        root = Path(temporary)
        for label, revision, expected in (("baseline", BASELINE, "fail"), ("candidate", candidate, "pass")):
            destination = root / label
            destination.mkdir()
            snapshot(repo, revision, destination)
            for _, _, path, _ in GROUPS:
                content = git(repo, "show", candidate + ":" + path)
                target = destination / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(content)
                if label == "candidate":
                    report["test_sources"].append({"path": path, "commit": candidate,
                        "git_blob": git(repo, "rev-parse", candidate + ":" + path).decode().strip(),
                        "sha256": hashlib.sha256(content).hexdigest()})
            report["sources"][label] = {"commit": revision,
                "tree": git(repo, "rev-parse", revision + "^{tree}").decode().strip(),
                "test_overlay_commit": candidate,
                "groups": [run_group(go, destination, package, name, marker, expected, environment)
                           for package, name, _, marker in GROUPS]}
    report["verified"] = all(group["verified"] for source in report["sources"].values()
                             for group in source["groups"])
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", default="HEAD", help="committed source and test ref (default: HEAD)")
    arguments = parser.parse_args()
    try:
        result = evaluate(arguments.candidate)
    except Exception as error:
        result = {"verified": False, "error": str(error), "error_type": type(error).__name__}
    print(json.dumps(result, indent=2, sort_keys=True))
    sys.exit(0 if result["verified"] else 1)
