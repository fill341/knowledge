#!/usr/bin/env python3
"""Export a project's approved knowledge from kn into the repository.

Reads a `kn export --format jsonl` dump and writes:
  - a readable Markdown snapshot (decisions, rules, facts, lessons), and
  - an importable JSONL seed (`kn import`) with the project, the entries and the links between them.

Only active decision/rule/fact/lesson entries of the project are exported; session logs, documents,
drafts and superseded entries stay in the base. Text replacements from --scrub (TSV: old<TAB>new,
kept out of git) hide names of other private projects; --exclude drops entries by id.

  bin/kn export --format jsonl --space personal -o /tmp/personal.jsonl
  scripts/export-project-knowledge.py /tmp/personal.jsonl --project personal/knowledge \
      --scrub seed/scrub.local.tsv --exclude-file seed/exclude.local.txt \
      --md docs/project-knowledge.md --jsonl seed/knowledge-project.jsonl
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

KINDS = ("decision", "rule", "fact", "lesson")
KIND_TITLES = {
    "decision": "Решения",
    "rule": "Правила",
    "fact": "Факты",
    "lesson": "Уроки (грабли)",
}


def load_scrub(path: str | None) -> list[tuple[str, str]]:
    if not path:
        return []
    pairs = []
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        old, _, new = line.partition("\t")
        pairs.append((old, new))
    # longest first, so "personal/my-app" wins over "my-app"
    return sorted(pairs, key=lambda p: -len(p[0]))


def scrub(value, pairs):
    if isinstance(value, str):
        for old, new in pairs:
            value = value.replace(old, new)
        return value
    if isinstance(value, list):
        return [scrub(v, pairs) for v in value]
    if isinstance(value, dict):
        return {k: scrub(v, pairs) for k, v in value.items()}
    return value


def demote_headings(body: str) -> str:
    """Entry bodies use ## headings; nest them under the entry's ### title."""
    lines, fence = [], False
    for line in body.splitlines():
        if line.startswith("```"):
            fence = not fence
        if not fence and line.startswith("#"):
            line = "###" + line
        lines.append(line)
    return "\n".join(lines)


def render_md(project: dict, entries: list[dict]) -> str:
    out = [
        f"# Знания проекта: {project['name']}",
        "",
        f"Снимок одобренных записей проекта `{project['space']}/{project['slug']}` из базы знаний kn —",
        "решения, правила, факты и уроки, накопленные агентами и владельцем при разработке kn.",
        "Генерируется `scripts/export-project-knowledge.py`; тот же набор для импорта в свою базу —",
        "[`seed/knowledge-project.jsonl`](../seed/knowledge-project.jsonl) (см. README, «Знания этого проекта»).",
        "Источник правды — база: правьте записи там и перегенерируйте снимок.",
        "",
    ]
    by_kind = {k: [e for e in entries if e["kind"] == k] for k in KINDS}
    out.append("## Содержание")
    out.append("")
    for k in KINDS:
        if by_kind[k]:
            out.append(f"- **{KIND_TITLES[k]}** ({len(by_kind[k])})")
            for e in by_kind[k]:
                out.append(f"  - [{e['title']}](#{e['id']})")
    out.append("")
    for k in KINDS:
        if not by_kind[k]:
            continue
        out.append(f"## {KIND_TITLES[k]}")
        out.append("")
        for e in by_kind[k]:
            out.append(f'<a id="{e["id"]}"></a>')
            out.append(f"### {e['title']}")
            out.append("")
            meta = [f"`{e['id']}`", f"обновлено {e['updated_at'][:10]}"]
            if e.get("tags"):
                meta.append("теги: " + ", ".join(e["tags"]))
            out.append("<sub>" + " · ".join(meta) + "</sub>")
            out.append("")
            if e.get("summary"):
                out.append(f"> {e['summary']}")
                out.append("")
            body = (e.get("body") or "").strip()
            if body:
                out.append(demote_headings(body))
                out.append("")
    return "\n".join(out).rstrip() + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("dump", help="kn export --format jsonl file")
    ap.add_argument("--project", required=True, help="<space>/<slug>")
    ap.add_argument("--scrub", help="TSV with old<TAB>new replacements")
    ap.add_argument("--exclude", action="append", default=[], help="entry id to skip (repeatable)")
    ap.add_argument("--exclude-file", help="file with entry ids to skip, one per line")
    ap.add_argument("--md", required=True, help="Markdown output")
    ap.add_argument("--jsonl", required=True, help="JSONL seed output")
    args = ap.parse_args()

    space, _, slug = args.project.partition("/")
    excluded = set(args.exclude)
    if args.exclude_file:
        excluded |= {l.split("#")[0].strip() for l in Path(args.exclude_file).read_text().splitlines()} - {""}
    pairs = load_scrub(args.scrub)

    project, entries, links = None, [], []
    with open(args.dump, encoding="utf-8") as f:
        for line in f:
            rec = json.loads(line)
            t = rec.get("type")
            if t == "project" and rec["space"] == space and rec["slug"] == slug:
                project = rec
            elif (t == "entry" and rec.get("project") == args.project and rec["status"] == "active"
                  and rec["kind"] in KINDS and rec["id"] not in excluded):
                entries.append(rec)
            elif t == "link":
                links.append(rec)
    if project is None:
        print(f"project {args.project} not found in dump", file=sys.stderr)
        return 1

    kind_order = {k: i for i, k in enumerate(KINDS)}
    entries.sort(key=lambda e: (kind_order[e["kind"]], e["created_at"]))
    entries = [scrub(e, pairs) for e in entries]
    ids = {e["id"] for e in entries}
    links = [l for l in links if l["from_id"] in ids and l["to_id"] in ids]
    project = scrub({**project, "repo_urls": []}, pairs)  # repos are attached locally: kn project link

    Path(args.md).parent.mkdir(parents=True, exist_ok=True)
    Path(args.md).write_text(render_md(project, entries), encoding="utf-8")

    Path(args.jsonl).parent.mkdir(parents=True, exist_ok=True)
    with open(args.jsonl, "w", encoding="utf-8") as f:
        f.write(json.dumps(project, ensure_ascii=False) + "\n")
        for e in entries:
            # sessions and authors belong to the source base; the importer becomes the author
            e = {**e, "session_id": None, "superseded_by": None}
            f.write(json.dumps(e, ensure_ascii=False) + "\n")
        for l in links:
            f.write(json.dumps(l, ensure_ascii=False) + "\n")

    print(f"{len(entries)} entries, {len(links)} links → {args.md}, {args.jsonl}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
