import DOMPurify from "dompurify";
import { marked } from "marked";
import { useMemo, useState } from "react";
import { api, type SearchItem } from "../api";
import { hasMsg, useT, type TFunc } from "../i18n";
import { Highlight, KindBadge, StatusBadge } from "./ui";

const escapeHtml = (t: string) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// Agents often write placeholders like <slug> in prose: show raw HTML as text instead of dropping it.
marked.use({ gfm: true, breaks: false, renderer: { html: ({ text }) => escapeHtml(text) } });

// attrsCoveredByBody reports whether every string in attributes already appears in the body —
// true for decisions recorded via MCP, whose body is generated from the attributes.
export function attrsCoveredByBody(attrs: Record<string, unknown>, body: string) {
  const strings: string[] = [];
  const walk = (v: unknown) => {
    if (typeof v === "string") strings.push(v);
    else if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === "object") Object.values(v).forEach(walk);
  };
  walk(attrs);
  const norm = (t: string) => t.replace(/\s+/g, " ").trim();
  const b = norm(body);
  return strings.length > 0 && strings.every((x) => b.includes(norm(x)));
}

// Markdown renders entry bodies. Content written by agents is untrusted, so it is sanitized.
export function Markdown({ text }: { text: string }) {
  const { t } = useT();
  const html = useMemo(() => DOMPurify.sanitize(marked.parse(text || "", { async: false }) as string), [text]);
  if (!text?.trim()) return <div className="muted">{t("md.empty")}</div>;
  return <div className="md" dangerouslySetInnerHTML={{ __html: html }} />;
}

// Attribute names with a dictionary label ("attr.<name>"); others are shown as is.
const attrLabel = (k: string, t: TFunc) => {
  const key = `attr.${k}`;
  return hasMsg(key) ? t(key) : k;
};

export function Attributes({ attrs, body }: { attrs: Record<string, unknown>; body?: string }) {
  const entries = Object.entries(attrs || {});
  if (!entries.length || (body !== undefined && attrsCoveredByBody(attrs, body))) return null;
  return (
    <dl className="attrs">
      {entries.map(([k, v]) => (
        <AttrRow key={k} k={k} v={v} />
      ))}
    </dl>
  );
}

function AttrRow({ k, v }: { k: string; v: unknown }) {
  const { t } = useT();
  let value: React.ReactNode;
  if (Array.isArray(v)) {
    value = (
      <ul style={{ margin: 0, paddingLeft: 18 }}>
        {v.map((x, i) => (
          <li key={i}>{typeof x === "string" ? x : JSON.stringify(x)}</li>
        ))}
      </ul>
    );
  } else if (v && typeof v === "object") {
    value = <code>{JSON.stringify(v)}</code>;
  } else {
    value = String(v);
  }
  return (
    <>
      <dt>{attrLabel(k, t)}</dt>
      <dd>{value}</dd>
    </>
  );
}

// EntryPicker finds an entry by full-text search; used for links and "superseded by".
export function EntryPicker({ onPick, excludeId }: { onPick: (item: SearchItem) => void; excludeId?: string }) {
  const { t } = useT();
  const [q, setQ] = useState("");
  const [items, setItems] = useState<SearchItem[]>([]);
  const [busy, setBusy] = useState(false);
  const run = async () => {
    if (!q.trim()) return;
    setBusy(true);
    try {
      const r = await api.search({ q, limit: 10, status: ["active", "proposed", "deprecated", "draft"] });
      setItems(r.items.filter((i) => i.id !== excludeId));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="stack">
      <div className="row">
        <input
          className="grow"
          style={{ width: "auto", flex: 1 }}
          placeholder={t("picker.placeholder")}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void run()}
          autoFocus
        />
        <button className="btn" onClick={() => void run()} disabled={busy}>{t("picker.find")}</button>
      </div>
      {items.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          {items.map((it) => (
            <button
              key={it.id}
              className="list-item"
              style={{ width: "100%", textAlign: "left", background: "none", border: 0, cursor: "pointer", font: "inherit" }}
              onClick={() => onPick(it)}
            >
              <div className="row">
                <span className="list-title">{it.title}</span>
                <KindBadge kind={it.kind} />
                <StatusBadge status={it.status} />
                <span className="muted small right">{it.project ?? it.space}</span>
              </div>
              {it.highlight && <div className="snippet"><Highlight text={it.highlight} /></div>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
