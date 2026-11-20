import { useState } from "react";
import type { Kind, Project, SpaceAccess } from "../api";
import { translate, useT } from "../i18n";
import { Markdown } from "./content";

export interface EntryFormValue {
  space: string;
  project: string;
  kind: string;
  title: string;
  summary: string;
  body: string;
  tags: string;
  attributes: string; // JSON text
  pinned: boolean;
  confidence: string;
  review_after: string;
  source_ref: string;
}

export const emptyForm = (): EntryFormValue => ({
  space: "", project: "", kind: "note", title: "", summary: "", body: "", tags: "",
  attributes: "{}", pinned: false, confidence: "", review_after: "", source_ref: "",
});

// attributesTemplate pre-fills attribute keys from a kind's JSON Schema.
export function attributesTemplate(kind?: Kind): string {
  const props = (kind?.attributes_schema?.properties ?? {}) as Record<string, { type?: string }>;
  const tpl: Record<string, unknown> = {};
  for (const [k, p] of Object.entries(props)) tpl[k] = p.type === "array" ? [] : "";
  return JSON.stringify(tpl, null, 2);
}

// toPayload converts the form to API fields, dropping empty attribute values.
export function toPayload(v: EntryFormValue): { payload: Record<string, unknown>; error?: string } {
  let attrs: Record<string, unknown> = {};
  try {
    attrs = v.attributes.trim() ? JSON.parse(v.attributes) : {};
    if (typeof attrs !== "object" || Array.isArray(attrs) || attrs === null) return { payload: {}, error: translate("form.attrsNotObject") };
  } catch (e) {
    return { payload: {}, error: translate("form.attrsInvalid", { error: (e as Error).message }) };
  }
  for (const [k, val] of Object.entries(attrs)) {
    if (val === "" || (Array.isArray(val) && val.length === 0)) delete attrs[k];
  }
  const payload: Record<string, unknown> = {
    title: v.title.trim(),
    summary: v.summary.trim(),
    body: v.body,
    tags: v.tags.split(/[,\s]+/).map((t) => t.trim()).filter(Boolean),
    attributes: attrs,
    pinned: v.pinned,
    review_after: v.review_after,
    source_ref: v.source_ref.trim(),
  };
  if (v.confidence !== "") payload.confidence = Number(v.confidence);
  return { payload };
}

export function EntryForm({ value, onChange, kinds, spaces, projects, isNew, kindInfo }: {
  value: EntryFormValue;
  onChange: (v: EntryFormValue) => void;
  kinds: Kind[];
  spaces: SpaceAccess[];
  projects: Project[];
  isNew: boolean;
  kindInfo?: Kind;
}) {
  const { t } = useT();
  const [preview, setPreview] = useState(false);
  const set = (patch: Partial<EntryFormValue>) => onChange({ ...value, ...patch });
  const writable = spaces.filter((s) => ["write", "review", "admin"].includes(s.level));

  return (
    <div className="stack">
      <div className="form-grid">
        {isNew && (
          <label className="field">
            <span>{t("common.space")}</span>
            <select value={value.space} onChange={(e) => set({ space: e.target.value, project: "" })}>
              <option value="">{t("common.choose")}</option>
              {writable.map((s) => <option key={s.slug} value={s.slug}>{s.slug}</option>)}
            </select>
          </label>
        )}
        <label className="field">
          <span>{t("common.project")}</span>
          <select value={value.project} onChange={(e) => set({ project: e.target.value })}>
            <option value="">{t("common.noProject")}</option>
            {projects.filter((p) => !value.space || p.space === value.space).map((p) => (
              <option key={p.ref} value={p.ref}>{p.ref}</option>
            ))}
          </select>
        </label>
        {isNew && (
          <label className="field">
            <span>{t("common.type")}</span>
            <select value={value.kind} onChange={(e) => {
              const k = kinds.find((x) => x.kind === e.target.value);
              set({ kind: e.target.value, attributes: attributesTemplate(k) });
            }}>
              {kinds.map((k) => <option key={k.kind} value={k.kind}>{k.kind} — {k.description}</option>)}
            </select>
          </label>
        )}
      </div>

      <label className="field">
        <span>{t("form.title")}</span>
        <input value={value.title} onChange={(e) => set({ title: e.target.value })} placeholder={t("form.titlePh")} />
      </label>
      <label className="field">
        <span>{t("form.summary")}</span>
        <input value={value.summary} onChange={(e) => set({ summary: e.target.value })} placeholder={t("form.summaryPh")} />
      </label>

      <div className="field">
        <div className="row between">
          <span className="small muted" style={{ fontWeight: 500 }}>{t("form.body")}</span>
          <span className="row">
            <span className={`chip ${!preview ? "on" : ""}`} onClick={() => setPreview(false)}>{t("form.editor")}</span>
            <span className={`chip ${preview ? "on" : ""}`} onClick={() => setPreview(true)}>{t("form.preview")}</span>
          </span>
        </div>
        {preview ? (
          <div className="card" style={{ minHeight: 200 }}><Markdown text={value.body} /></div>
        ) : (
          <textarea rows={14} value={value.body} onChange={(e) => set({ body: e.target.value })} />
        )}
      </div>

      <label className="field">
        <span>
          {t("form.attributes", { schema: kindInfo ? t("form.attributesSchema", { kind: kindInfo.kind }) : "" })}
        </span>
        <textarea rows={6} value={value.attributes} onChange={(e) => set({ attributes: e.target.value })} />
      </label>

      <div className="form-grid">
        <label className="field">
          <span>{t("form.tags")}</span>
          <input value={value.tags} onChange={(e) => set({ tags: e.target.value })} placeholder="webhooks, reliability" />
        </label>
        <label className="field">
          <span>{t("form.source")}</span>
          <input value={value.source_ref} onChange={(e) => set({ source_ref: e.target.value })} />
        </label>
        <label className="field">
          <span>{t("form.confidence")}</span>
          <input type="number" min={0} max={1} step={0.05} value={value.confidence} onChange={(e) => set({ confidence: e.target.value })} />
        </label>
        <label className="field">
          <span>{t("form.reviewAfter")}</span>
          <input type="date" value={value.review_after} onChange={(e) => set({ review_after: e.target.value })} />
        </label>
      </div>
      <label className="check">
        <input type="checkbox" checked={value.pinned} onChange={(e) => set({ pinned: e.target.checked })} />
        {t("form.pin")}
      </label>
    </div>
  );
}
