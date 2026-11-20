import { diffLines } from "diff";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, ApiError, LINK_RELS, TRANSITIONS, type EntryDetail, type Status, type Version } from "../api";
import { Attributes, EntryPicker, Markdown } from "../components/content";
import { EntryForm, toPayload, type EntryFormValue } from "../components/EntryForm";
import { notifyChanged } from "../components/Layout";
import {
  CopyButton, ErrorBox, fmtAgo, fmtDate, KindBadge, Modal, Spinner, StatusBadge, statusLabel, Tags, useAsync, useToast,
} from "../components/ui";
import { useT } from "../i18n";

type Tab = "content" | "history" | "links";

export function EntryPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const toast = useToast();
  const { t } = useT();
  const entry = useAsync(() => api.entry(id), [id]);
  const [tab, setTab] = useState<Tab>("content");
  const editing = params.get("edit") === "1";
  const setEditing = (on: boolean) => setParams(on ? { edit: "1" } : {}, { replace: true });
  const [modal, setModal] = useState<null | "status" | "promote" | "link">(null);

  useEffect(() => setTab("content"), [id]);

  if (entry.loading && !entry.data) return <Spinner />;
  if (entry.error) return <ErrorBox error={entry.error} />;
  const e = entry.data!;
  const reload = () => {
    entry.reload();
    notifyChanged();
  };

  return (
    <>
      <div className="page-head" style={{ alignItems: "flex-start" }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="row small muted" style={{ marginBottom: 6 }}>
            <Link to={`/entries?space=${e.space}`}>{e.space}</Link>
            {e.project && <>/ <Link to={`/entries?project=${e.project}`}>{e.project.split("/")[1]}</Link></>}
          </div>
          <h1>{e.title}</h1>
          <div className="row" style={{ marginTop: 8 }}>
            <KindBadge kind={e.kind} />
            <StatusBadge status={e.status} />
            {e.pinned && <span className="badge pin">{t("common.pinned")}</span>}
            <Tags tags={e.tags} />
          </div>
        </div>
        {!editing && (
          <div className="row">
            {e.status === "proposed" && (
              <>
                <button className="btn ok" onClick={() => void api.review(e.id, "approve").then(() => { toast.ok(t("common.approved")); reload(); }, toast.error)}>
                  {t("common.approve")}
                </button>
                <button className="btn danger" onClick={() => void api.review(e.id, "reject").then(() => { toast.ok(t("common.rejected")); reload(); }, toast.error)}>
                  {t("common.reject")}
                </button>
              </>
            )}
            <button className="btn" onClick={() => setEditing(true)}>{t("common.editEntry")}</button>
            <button className="btn" onClick={() => setModal("status")}>{t("common.status")}</button>
            <button className="btn" onClick={() => setModal("link")}>{t("common.link")}</button>
            <button className="btn" onClick={() => setModal("promote")}>{t("entry.promote")}</button>
          </div>
        )}
      </div>

      {e.superseded_by && (
        <div className="alert" style={{ marginBottom: 12 }}>
          {t("entry.supersededBy")} <Link to={`/entries/${e.superseded_by}`}>{t("entry.goCurrent")}</Link>
        </div>
      )}

      {editing ? (
        <EditEntry entry={e} onDone={(changed) => { setEditing(false); if (changed) reload(); }} />
      ) : (
        <div className="entry-layout">
          <div className="card">
            <div className="tabs">
              <button className={`tab ${tab === "content" ? "on" : ""}`} onClick={() => setTab("content")}>{t("entry.tabContent")}</button>
              <button className={`tab ${tab === "history" ? "on" : ""}`} onClick={() => setTab("history")}>{t("entry.tabHistory", { v: e.version })}</button>
              <button className={`tab ${tab === "links" ? "on" : ""}`} onClick={() => setTab("links")}>
                {t("entry.tabLinks", { n: e.links.outgoing.length + e.links.incoming.length })}
              </button>
            </div>
            {tab === "content" && (
              <>
                {e.summary && <div className="summary">{e.summary}</div>}
                <Attributes attrs={e.attributes} body={e.body} />
                <Markdown text={e.body} />
              </>
            )}
            {tab === "history" && <History entry={e} onReverted={reload} />}
            {tab === "links" && <Links entry={e} onChanged={reload} />}
          </div>
          <div className="card">
            <h3>{t("entry.info")}</h3>
            <dl className="meta">
              <dt>ID</dt><dd className="row"><code className="small">{e.id.slice(0, 8)}…</code><CopyButton text={e.id} label="id" /></dd>
              <dt>{t("entry.version")}</dt><dd>{e.version}</dd>
              <dt>{t("entry.createdBy")}</dt><dd>{e.created_by}<div className="muted small">{fmtDate(e.created_at)}</div></dd>
              <dt>{t("entry.updatedBy")}</dt><dd>{e.updated_by}<div className="muted small">{fmtAgo(e.updated_at)}</div></dd>
              {e.source_ref && <><dt>{t("entry.source")}</dt><dd><code className="small">{e.source_ref}</code></dd></>}
              {e.session_id && <><dt>{t("entry.session")}</dt><dd><code className="small">{e.session_id.slice(0, 8)}…</code></dd></>}
              {e.confidence !== null && <><dt>{t("entry.confidence")}</dt><dd>{e.confidence}</dd></>}
              {e.review_due && (
                <><dt title={t("entry.reviewDueTitle")}>{t("entry.reviewDue")}</dt>
                  <dd style={e.status === "active" && e.review_due <= new Date().toISOString().slice(0, 10) ? { color: "var(--danger)" } : undefined}>
                    {fmtDate(e.review_due, false)}
                    {e.status === "active" && e.review_due <= new Date().toISOString().slice(0, 10) && <div className="small">{t("entry.overdue")}</div>}
                  </dd></>
              )}
              {e.usage && (
                <>
                  <dt title={t("entry.usageTitle")}>
                    {t("entry.usage")}
                  </dt>
                  <dd>
                    {e.usage.shown + e.usage.delivered + e.usage.opened === 0
                      ? <span className="muted">{t("entry.unused", { days: t("count.days", { count: e.usage.days }) })}</span>
                      : <>{t("entry.usageStats", { shown: e.usage.shown, delivered: e.usage.delivered, opened: e.usage.opened })}</>}
                    {e.usage.last_day && <div className="muted small">{t("entry.usageLast", { days: t("count.days", { count: e.usage.days }), date: fmtDate(e.usage.last_day, false) })}</div>}
                  </dd>
                </>
              )}
            </dl>
          </div>
        </div>
      )}

      {modal === "status" && <StatusModal entry={e} onClose={() => setModal(null)} onDone={reload} />}
      {modal === "promote" && <PromoteModal entry={e} onClose={() => setModal(null)} />}
      {modal === "link" && <LinkModal entry={e} onClose={() => setModal(null)} onDone={reload} />}
    </>
  );
}

function EditEntry({ entry: e, onDone }: { entry: EntryDetail; onDone: (changed: boolean) => void }) {
  const toast = useToast();
  const { t } = useT();
  const kinds = useAsync(() => api.kinds(), []);
  const spaces = useAsync(() => api.spaces(), []);
  const projects = useAsync(() => api.projects(), []);
  const [value, setValue] = useState<EntryFormValue>({
    space: e.space, project: e.project ?? "", kind: e.kind, title: e.title, summary: e.summary ?? "", body: e.body,
    tags: e.tags.join(", "), attributes: JSON.stringify(e.attributes ?? {}, null, 2), pinned: e.pinned,
    confidence: e.confidence === null ? "" : String(e.confidence), review_after: e.review_after ?? "", source_ref: e.source_ref ?? "",
  });
  const [note, setNote] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    const { payload, error: perr } = toPayload(value);
    if (perr) return setError(new Error(perr));
    payload.project = value.project;
    if (note.trim()) payload.change_note = note.trim();
    setBusy(true);
    setError(null);
    try {
      const r = await api.patchEntry(e.id, e.version, payload);
      toast.ok(t("entry.savedVersion", { v: r.entry.version }));
      onDone(true);
    } catch (err) {
      if (err instanceof ApiError && err.code === "version_conflict") {
        setError(new Error(t("entry.conflict", { v: String(err.currentVersion) })));
      } else setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card stack">
      <EntryForm value={value} onChange={setValue} kinds={kinds.data ?? []} spaces={spaces.data ?? []}
        projects={projects.data ?? []} isNew={false} kindInfo={kinds.data?.find((k) => k.kind === e.kind)} />
      <label className="field">
        <span>{t("entry.changeNote")}</span>
        <input value={note} onChange={(ev) => setNote(ev.target.value)} />
      </label>
      <ErrorBox error={error} />
      <div className="row">
        <button className="btn primary" disabled={busy} onClick={() => void save()}>{t("common.save")}</button>
        <button className="btn" onClick={() => onDone(false)}>{t("common.cancel")}</button>
      </div>
    </div>
  );
}

function History({ entry, onReverted }: { entry: EntryDetail; onReverted: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const versions = useAsync(() => api.versions(entry.id), [entry.id, entry.version]);
  const [pair, setPair] = useState<[number, number] | null>(null);

  if (!versions.data) return versions.error ? <ErrorBox error={versions.error} /> : <Spinner />;
  const vs = versions.data;
  const byNum = new Map(vs.map((v) => [v.version, v]));
  const cmp = pair ?? (vs.length > 1 ? [vs[1].version, vs[0].version] : null);

  return (
    <div className="stack">
      <table>
        <thead><tr><th>v</th><th>{t("common.when")}</th><th>{t("common.who")}</th><th>{t("common.status")}</th><th>{t("entry.colComment")}</th><th /></tr></thead>
        <tbody>
          {vs.map((v) => (
            <tr key={v.version}>
              <td><strong>{v.version}</strong></td>
              <td className="nowrap small">{fmtDate(v.created_at)}</td>
              <td>{v.changed_by}</td>
              <td><StatusBadge status={v.status} /></td>
              <td className="small">{v.change_note}</td>
              <td className="nowrap">
                {v.version > 1 && (
                  <button className="btn ghost sm" onClick={() => setPair([v.version - 1, v.version])}>diff</button>
                )}
                {v.version !== entry.version && (
                  <button className="btn ghost sm" onClick={() => {
                    if (!confirm(t("entry.confirmRevert", { v: v.version }))) return;
                    void api.revert(entry.id, entry.version, v.version).then(() => { toast.ok(t("entry.reverted")); onReverted(); }, toast.error);
                  }}>{t("entry.revert")}</button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {cmp && byNum.get(cmp[0]) && byNum.get(cmp[1]) && (
        <VersionDiff a={byNum.get(cmp[0])!} b={byNum.get(cmp[1])!} />
      )}
    </div>
  );
}

const versionText = (v: Version) =>
  [
    `# ${v.title}`,
    v.summary ? `> ${v.summary}` : "",
    `status: ${v.status}${v.pinned ? ", pinned" : ""}`,
    `tags: ${v.tags.join(", ")}`,
    `attributes: ${JSON.stringify(v.attributes, null, 2)}`,
    "",
    v.body,
  ].join("\n");

function VersionDiff({ a, b }: { a: Version; b: Version }) {
  const { t } = useT();
  const parts = diffLines(versionText(a), versionText(b));
  return (
    <div>
      <h3>{t("entry.diffTitle", { a: a.version, b: b.version })}</h3>
      <div className="diff">
        {parts.map((p, i) => (
          <div key={i} className={p.added ? "add" : p.removed ? "del" : ""}>{p.value}</div>
        ))}
      </div>
    </div>
  );
}

function Links({ entry, onChanged }: { entry: EntryDetail; onChanged: () => void }) {
  const { t } = useT();
  const { outgoing, incoming } = entry.links;
  return (
    <div className="stack">
      {!outgoing.length && !incoming.length ? <div className="muted">{t("entry.noLinks")}</div>
        : <LinkTable entry={entry} onChanged={onChanged} />}
      <Impact entry={entry} />
      <Suggestions entry={entry} onChanged={onChanged} />
    </div>
  );
}

// What relies on this entry (impact analysis, up to three steps).
function Impact({ entry }: { entry: EntryDetail }) {
  const { t } = useT();
  const im = useAsync(() => api.impact(entry.id), [entry.id, entry.version]);
  if (!im.data || im.data.count === 0) return null;
  return (
    <div>
      <h3>{t("entry.dependents", { n: im.data.count })}</h3>
      <div className="small muted" style={{ marginBottom: 6 }}>{t("entry.dependentsHint")}</div>
      {im.data.dependents.map((d) => (
        <div key={d.id} className="row small" style={{ paddingLeft: (d.depth - 1) * 18 }}>
          <Link to={`/entries/${d.id}`}>{d.title}</Link> <span className="muted">{d.kind} · {d.rel} → {d.via}</span>
        </div>
      ))}
    </div>
  );
}

// Proposed links: related structured knowledge of the same space; linking is one click, never automatic.
function Suggestions({ entry, onChanged }: { entry: EntryDetail; onChanged: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const sug = useAsync(() => api.linkSuggestions(entry.id), [entry.id, entry.links.outgoing.length, entry.links.incoming.length]);
  if (!sug.data?.length) return null;
  return (
    <div>
      <h3>{t("entry.suggestions")}</h3>
      {sug.data.map((s) => (
        <div key={s.id} className="row" style={{ justifyContent: "space-between", padding: "3px 0" }}>
          <Link to={`/entries/${s.id}`}>{s.title}</Link>
          <span className="row" style={{ gap: 4 }}>
            {(["relates_to", "depends_on"] as const).map((rel) => (
              <button key={rel} className="btn sm" onClick={() =>
                void api.addLink(entry.id, s.id, rel).then(() => { toast.ok(t("common.linkAdded")); onChanged(); }, toast.error)}>
                {rel === "relates_to" ? t("common.link") : t("entry.dependsOn")}
              </button>
            ))}
          </span>
        </div>
      ))}
    </div>
  );
}

function LinkTable({ entry, onChanged }: { entry: EntryDetail; onChanged: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const { outgoing, incoming } = entry.links;
  return (
    <table>
      <tbody>
        {outgoing.map((l) => (
          <tr key={`o-${l.to_id}-${l.rel}`}>
            <td className="nowrap"><code>→ {l.rel}</code></td>
            <td><Link to={`/entries/${l.to_id}`}>{l.title}</Link></td>
            <td className="nowrap">
              <button className="btn ghost sm" onClick={() =>
                void api.deleteLink(entry.id, l.to_id, l.rel).then(() => { toast.ok(t("entry.linkDeleted")); onChanged(); }, toast.error)}>
                {t("entry.deleteLink")}
              </button>
            </td>
          </tr>
        ))}
        {incoming.map((l) => (
          <tr key={`i-${l.from_id}-${l.rel}`}>
            <td className="nowrap"><code>← {l.rel}</code></td>
            <td><Link to={`/entries/${l.from_id}`}>{l.title}</Link></td>
            <td />
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function StatusModal({ entry, onClose, onDone }: { entry: EntryDetail; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const [to, setTo] = useState<Status | "">("");
  const [note, setNote] = useState("");
  const [by, setBy] = useState<{ id: string; title: string } | null>(null);
  const options = TRANSITIONS[entry.status];
  const im = useAsync(() => api.impact(entry.id), [entry.id]);
  const retiring = to === "superseded" || to === "deprecated" || to === "archived";
  const submit = async () => {
    if (!to) return;
    try {
      await api.transition(entry.id, to, { note: note || undefined, superseded_by: by?.id });
      toast.ok(t("entry.statusToast", { status: statusLabel(to, t) }));
      onDone();
      onClose();
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <Modal title={t("entry.changeStatus")} onClose={onClose}
      footer={<><button className="btn" onClick={onClose}>{t("common.cancel")}</button>
        <button className="btn primary" disabled={!to || (to === "superseded" && !by)} onClick={() => void submit()}>{t("entry.apply")}</button></>}>
      <div className="stack">
        <div className="row">{t("entry.current")} <StatusBadge status={entry.status} /></div>
        <div className="row">
          {options.map((s) => (
            <span key={s} className={`chip ${to === s ? "on" : ""}`} onClick={() => setTo(s)}>{statusLabel(s, t)}</span>
          ))}
        </div>
        {to === "superseded" && (
          <div className="stack">
            <div className="small muted">{t("entry.supersededWith")}</div>
            {by ? (
              <div className="row"><strong>{by.title}</strong><button className="btn ghost sm" onClick={() => setBy(null)}>{t("entry.other")}</button></div>
            ) : (
              <EntryPicker excludeId={entry.id} onPick={(i) => setBy({ id: i.id, title: i.title })} />
            )}
          </div>
        )}
        {retiring && !!im.data?.count && (
          <div className="alert error small">
            {t("entry.retireWarn", { n: im.data.count, direct: im.data.direct })}{" "}
            {im.data.dependents.slice(0, 5).map((d) => d.title).join(", ")}{im.data.count > 5 ? "…" : ""}.{" "}
            {t("entry.retireHint")}
          </div>
        )}
        <label className="field"><span>{t("entry.historyComment")}</span><input value={note} onChange={(e) => setNote(e.target.value)} /></label>
      </div>
    </Modal>
  );
}

function PromoteModal({ entry, onClose }: { entry: EntryDetail; onClose: () => void }) {
  const toast = useToast();
  const { t, rich } = useT();
  const navigate = useNavigate();
  const spaces = useAsync(() => api.spaces(), []);
  const projects = useAsync(() => api.projects(), []);
  const [space, setSpace] = useState("");
  const [project, setProject] = useState("");
  const submit = async () => {
    try {
      const r = await api.promote(entry.id, space, project);
      toast.ok(t("entry.copiedTo", { space: r.entry.space }));
      onClose();
      navigate(`/entries/${r.entry.id}`);
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <Modal title={t("entry.promoteTitle")} onClose={onClose}
      footer={<><button className="btn" onClick={onClose}>{t("common.cancel")}</button>
        <button className="btn primary" disabled={!space} onClick={() => void submit()}>{t("entry.copyAction")}</button></>}>
      <div className="stack">
        <div className="small muted">
          {rich("entry.promoteHint", { rel: <code>derived_from</code> })}
        </div>
        <div className="form-grid">
          <label className="field"><span>{t("common.space")}</span>
            <select value={space} onChange={(e) => { setSpace(e.target.value); setProject(""); }}>
              <option value="">{t("common.choose")}</option>
              {spaces.data?.filter((s) => s.slug !== entry.space && ["write", "review", "admin"].includes(s.level)).map((s) => (
                <option key={s.slug} value={s.slug}>{s.slug}</option>
              ))}
            </select>
          </label>
          <label className="field"><span>{t("entry.projectOptional")}</span>
            <select value={project} onChange={(e) => setProject(e.target.value)}>
              <option value="">{t("common.noProject")}</option>
              {projects.data?.filter((p) => p.space === space).map((p) => <option key={p.ref} value={p.ref}>{p.ref}</option>)}
            </select>
          </label>
        </div>
      </div>
    </Modal>
  );
}

function LinkModal({ entry, onClose, onDone }: { entry: EntryDetail; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const [rel, setRel] = useState<string>("relates_to");
  return (
    <Modal title={t("entry.linkTitle")} onClose={onClose} wide>
      <div className="stack">
        <div className="row small">
          <span className="muted">{t("entry.linkType")}</span>
          {LINK_RELS.map((r) => (
            <span key={r} className={`chip ${rel === r ? "on" : ""}`} onClick={() => setRel(r)}>{r}</span>
          ))}
        </div>
        <EntryPicker excludeId={entry.id} onPick={(i) =>
          void api.addLink(entry.id, i.id, rel).then(() => { toast.ok(t("common.linkAdded")); onDone(); onClose(); }, toast.error)} />
      </div>
    </Modal>
  );
}
