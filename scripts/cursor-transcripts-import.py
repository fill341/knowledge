#!/usr/bin/env python3
"""Import JSONL from cursor-transcripts-extract.py via kn CLI (body-file, safe tags)."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path

TAG_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,31}$")


def sanitize_tag(t: str) -> str | None:
    t = t.lower().replace(":", "-").replace("_", "-")
    t = re.sub(r"[^a-z0-9-]", "", t)
    if not t or not TAG_RE.match(t):
        return None
    return t


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("jsonl", type=Path)
    ap.add_argument("--kn", default=os.environ.get("KN_BIN", ""))
    ap.add_argument("--principal", default="cursor@personal", help="kn --as principal")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument(
        "--sleep",
        type=float,
        default=1.05,
        help="seconds between writes (API limit 60/min)",
    )
    ap.add_argument(
        "--progress",
        type=Path,
        help="file with dedupe_key lines already imported (append on success)",
    )
    args = ap.parse_args()

    kn = args.kn or str(Path(__file__).resolve().parent.parent / "bin" / "kn")
    if not Path(kn).is_file():
        print(f"kn not found: {kn}", file=sys.stderr)
        return 2

    done: set[str] = set()
    if args.progress and args.progress.is_file():
        done = {ln.strip() for ln in args.progress.read_text(encoding="utf-8").splitlines() if ln.strip()}

    ok = fail = skip = 0
    with args.jsonl.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            rec = json.loads(line)
            dedupe = rec.get("dedupe_key", "")
            # Skip meta entries if already imported (inventory + rule)
            if dedupe in ("cursor-inventory-v1", "cursor-import-rule-v1"):
                skip += 1
                continue
            if dedupe and dedupe in done:
                skip += 1
                continue
            with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as tf:
                tf.write(rec["body"])
                body_path = tf.name
            try:
                cmd = [
                    kn,
                    "--as",
                    args.principal,
                    "save",
                    "--kind",
                    rec["kind"],
                    "--title",
                    rec["title"],
                    "--project",
                    rec["project"],
                    "--summary",
                    rec.get("summary", ""),
                    "--source-ref",
                    rec.get("source_ref", ""),
                    "--body-file",
                    body_path,
                ]
                seen_tags: set[str] = set()
                for t in rec.get("tags") or []:
                    st = sanitize_tag(t)
                    if st and st not in seen_tags:
                        seen_tags.add(st)
                        cmd.extend(["--tag", st])
                if args.dry_run:
                    ok += 1
                    continue
                r = subprocess.run(cmd, capture_output=True, text=True)
                if r.returncode == 0:
                    ok += 1
                    if dedupe and args.progress:
                        with args.progress.open("a", encoding="utf-8") as pf:
                            pf.write(dedupe + "\n")
                        done.add(dedupe)
                    if args.sleep > 0:
                        time.sleep(args.sleep)
                else:
                    fail += 1
                    msg = (r.stderr or r.stdout or "").strip().split("\n")[0]
                    print(f"FAIL {rec['kind']}: {rec['title'][:70]} — {msg}", file=sys.stderr)
                    if "rate_limited" in msg and args.sleep > 0:
                        time.sleep(65)
            finally:
                try:
                    os.unlink(body_path)
                except OSError:
                    pass

    print(f"ok={ok} fail={fail} skip={skip}", file=sys.stderr)
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())