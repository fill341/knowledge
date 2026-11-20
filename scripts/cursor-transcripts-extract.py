#!/usr/bin/env python3
"""Extract knowledge candidates from Cursor agent transcripts (compaction summaries).

Reads ~/.cursor/projects/*/agent-transcripts/*/compaction/segment_*.md
and emits JSONL lines suitable for `kn save` (one object per candidate).

Does not dump raw chat logs — only curated "## Summary" blocks from compaction.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
from dataclasses import dataclass, asdict
from datetime import datetime, timezone
from pathlib import Path

CURSOR_ROOT = Path.home() / ".cursor" / "projects"

# Cursor project folder (~/.cursor/projects/<folder>, e.g. "Users-me-Code-billing") → kn project.
# Kept out of git: JSON {"<folder>": "<space>/<slug>"} from --map, scripts/cursor-projects.local.json
# or ~/.config/kn/cursor-projects.json. Unmapped folders go to the "personal" space without a project.
DEFAULT_MAPS = (
    Path(__file__).resolve().parent / "cursor-projects.local.json",
    Path.home() / ".config" / "kn" / "cursor-projects.json",
)
PROJECT_BY_FOLDER: dict[str, str] = {}


def load_project_map(path: Path | None) -> dict[str, str]:
    for p in ([path] if path else DEFAULT_MAPS):
        if p.is_file():
            return json.loads(p.read_text(encoding="utf-8"))
    if path:
        raise SystemExit(f"project map not found: {path}")
    return {}

SECRET_PATTERNS = [
    re.compile(r"(?i)(api[_-]?key|token|password|secret|bearer)\s*[:=]\s*\S+"),
    re.compile(r"sk-[a-zA-Z0-9]{20,}"),
]

SUMMARY_START = "## Summary (curated by compaction step)"
SUMMARY_END_MARKERS = ("## Verbatim turns", "## Segment metadata")


@dataclass
class Candidate:
    kind: str
    title: str
    body: str
    summary: str
    project: str
    source_ref: str
    tags: list[str]
    dedupe_key: str


def project_for_transcript(path: Path) -> str:
    parts = path.parts
    try:
        idx = parts.index("projects")
        folder = parts[idx + 1]
    except (ValueError, IndexError):
        return "personal"
    return PROJECT_BY_FOLDER.get(folder, "personal")


def scrub(text: str) -> str:
    out = text
    for pat in SECRET_PATTERNS:
        out = pat.sub("[redacted]", out)
    return out.strip()


def extract_summary_block(text: str) -> str | None:
    start = text.find(SUMMARY_START)
    if start < 0:
        return None
    start += len(SUMMARY_START)
    end = len(text)
    for marker in SUMMARY_END_MARKERS:
        pos = text.find(marker, start)
        if pos >= 0:
            end = min(end, pos)
    block = text[start:end].strip()
    # Drop leading "Summary:" duplicate line
    block = re.sub(r"^Summary:\s*", "", block, count=1)
    return block if block else None


def parse_summary_sections(block: str) -> dict[str, str]:
    """Parse numbered sections like '4. Errors and Fixes: ...'"""
    sections: dict[str, str] = {}
    # Split on lines that look like "N. Title:" at start or after newline
    pattern = re.compile(
        r"(?:^|\n)(\d+)\.\s+([^:\n]+):\s*",
        re.MULTILINE,
    )
    matches = list(pattern.finditer(block))
    if not matches:
        sections["overview"] = block
        return sections
    for i, m in enumerate(matches):
        key = m.group(2).strip().lower()
        start = m.end()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(block)
        sections[key] = block[start:end].strip()
    return sections


def section_to_candidates(
    section_key: str,
    content: str,
    *,
    project: str,
    source_ref: str,
    conv_id: str,
    seg: str,
) -> list[Candidate]:
    content = scrub(content)
    if not content or len(content) < 40:
        return []
    out: list[Candidate] = []
    base_tags = ["cursor-transcript", f"conv-{conv_id[:8]}"]

    if "errors and fixes" in section_key or "problem solving" in section_key:
        kind = "lesson"
        title_seed = content.split(".")[0][:120]
        title = f"Из Cursor ({conv_id[:8]}): {title_seed}"
        summary = content[:240] + ("…" if len(content) > 240 else "")
        dk = hashlib.sha256(f"lesson|{conv_id}|{seg}|{section_key}|{content[:500]}".encode()).hexdigest()[:16]
        out.append(
            Candidate(kind, title, content, summary, project, source_ref, base_tags + ["lesson"], dk)
        )
        return out

    if "key technical concepts" in section_key:
        title_line = content.split(".")[0][:120].strip()
        if len(title_line) < 20:
            title_line = f"Техконтекст чата {conv_id[:8]} ({seg})"
        title = title_line
        summary = content[:280] + ("…" if len(content) > 280 else "")
        dk = hashlib.sha256(f"fact|{conv_id}|{seg}|concepts".encode()).hexdigest()[:16]
        body = (
            f"{content}\n\n"
            f"Источник: compaction `{seg}`, чат `{conv_id}`.\n"
            f"Полный transcript: `{source_ref}`."
        )
        out.append(
            Candidate(
                "fact",
                title,
                body,
                summary,
                project,
                source_ref,
                base_tags + ["compaction-fact"],
                dk,
            )
        )
        return out

    if "primary request and intent" in section_key:
        kind = "note"
        first_sentence = content.split(".")[0][:150]
        title = f"Сессия Cursor {conv_id[:8]}: {first_sentence}"
        summary = content[:220] + ("…" if len(content) > 220 else "")
        # One intent note per conversation (last segment wins when deduping by key)
        dk = hashlib.sha256(f"note|{conv_id}|intent".encode()).hexdigest()[:16]
        body = f"{content}\n\n_Краткая выжимка intent из compaction; подробности — в transcript по source_ref._"
        out.append(Candidate(kind, title, body, summary, project, source_ref, base_tags + ["session-intent"], dk))
        return out

    return []


def inventory_fact() -> Candidate:
    root = CURSOR_ROOT
    counts: dict[str, int] = {}
    total = 0
    mtimes = []
    for dirpath, _, fnames in os.walk(root):
        if "/agent-transcripts/" not in dirpath:
            continue
        for f in fnames:
            if f.endswith(".jsonl"):
                total += 1
                p = Path(dirpath) / f
                try:
                    parts = p.parts
                    idx = parts.index("projects")
                    folder = parts[idx + 1]
                    counts[folder] = counts.get(folder, 0) + 1
                    mtimes.append(p.stat().st_mtime)
                except (ValueError, OSError):
                    pass
    oldest = datetime.fromtimestamp(min(mtimes), tz=timezone.utc).date() if mtimes else None
    newest = datetime.fromtimestamp(max(mtimes), tz=timezone.utc).date() if mtimes else None
    body = (
        "Локальное хранилище сессий Cursor на этой машине (без сырого импорта в kn).\n\n"
        f"- **Транскрипты агента:** `{root}/*/agent-transcripts/*.jsonl` — **{total}** файлов"
    )
    if oldest and newest:
        body += f", период **{oldest}** … **{newest}**"
    body += ".\n"
    body += "- **Архив Composer:** `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb` "
    body += "(заголовки чатов `composerHeaders`, пузыри `bubbleId`, состояние агента `agentKv`).\n"
    body += "- **Compaction:** у длинных чатов каталог `compaction/` с curated Summary — приоритетный источник для kn.\n"
    body += "- **Планы:** `~/.cursor/plans/`. **AgentStores:** файлы между сессиями агента.\n\n"
    body += "По проектам (папки `~/.cursor/projects/*`):\n\n"
    for folder, n in sorted(counts.items(), key=lambda x: -x[1]):
        kn = PROJECT_BY_FOLDER.get(folder, "personal")
        body += f"- `{folder}` → **{kn}**: {n} jsonl\n"
    summary = f"{total} jsonl-транскриптов Cursor; импорт в kn — только выжимки (compaction), не дамп."
    return Candidate(
        "fact",
        "Инвентарь локальных сессий Cursor (agent-transcripts)",
        body,
        summary,
        "personal/knowledge",
        str(root),
        ["cursor-transcript", "inventory"],
        "cursor-inventory-v1",
    )


def rule_entry() -> Candidate:
    body = """При переносе знаний из Cursor в kn:

1. **Не импортировать** сырые `.jsonl`, `state.vscdb`, `agentKv` и verbatim compaction.
2. **Приоритет:** секции `## Summary (curated by compaction step)` → lesson / fact / note.
3. **Короткие чаты** без compaction — только вручную или `kn_log_session` в конце сессии.
4. **source_ref** — путь к transcript или uuid чата; тег `cursor-transcript`.
5. Секреты и токены не сохранять (скрипт `cursor-transcripts-extract.py` маскирует очевидные паттерны).
"""
    return Candidate(
        "rule",
        "Импорт знаний из Cursor: только compaction summary, не сырые логи",
        body,
        "Выжимки из compaction → kn; jsonl и vscdb остаются локальным архивом.",
        "personal/knowledge",
        "scripts/cursor-transcripts-extract.py",
        ["cursor-transcript", "kn"],
        "cursor-import-rule-v1",
    )


def collect_compaction(root: Path) -> list[Candidate]:
    out: list[Candidate] = []
    for seg_path in sorted(root.glob("**/agent-transcripts/*/compaction/segment_*.md")):
        conv_id = seg_path.parts[seg_path.parts.index("agent-transcripts") + 1]
        project = project_for_transcript(seg_path)
        source_ref = str(
            seg_path.parent.parent / f"{conv_id}.jsonl"
        )
        try:
            text = seg_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        block = extract_summary_block(text)
        if not block:
            continue
        seg = seg_path.stem
        for key, content in parse_summary_sections(block).items():
            out.extend(
                section_to_candidates(
                    key, content, project=project, source_ref=source_ref, conv_id=conv_id, seg=seg
                )
            )
    return out


def dedupe(candidates: list[Candidate]) -> list[Candidate]:
    """Last occurrence wins (compaction segments are processed in path order)."""
    by_key: dict[str, Candidate] = {}
    for c in candidates:
        by_key[c.dedupe_key] = c
    return list(by_key.values())


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", "-o", type=Path, default=Path("-"), help="JSONL output (default stdout)")
    ap.add_argument("--no-meta", action="store_true", help="Skip inventory fact and rule")
    ap.add_argument("--map", type=Path, help="JSON {cursor-project-folder: space/slug}")
    args = ap.parse_args()
    PROJECT_BY_FOLDER.update(load_project_map(args.map))

    candidates = collect_compaction(CURSOR_ROOT)
    if not args.no_meta:
        candidates = [inventory_fact(), rule_entry()] + candidates
    candidates = dedupe(candidates)

    out_f = sys.stdout if str(args.out) == "-" else args.out.open("w", encoding="utf-8")
    try:
        for c in candidates:
            out_f.write(json.dumps(asdict(c), ensure_ascii=False) + "\n")
    finally:
        if out_f is not sys.stdout:
            out_f.close()

    print(f"# candidates: {len(candidates)}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())