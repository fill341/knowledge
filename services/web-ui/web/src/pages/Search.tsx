import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, STATUSES, type Entry, type SearchItem, type Status } from "../api";
import { ErrorBox, fmtAgo, Highlight, KindBadge, StatusBadge, statusLabel, Tags, useAsync } from "../components/ui";
import { useT } from "../i18n";

type Row = (SearchItem | Entry) & { highlight?: string; score?: number };
const DEFAULT_STATUSES: Status[] = ["active", "proposed", "deprecated"];

export function Search() {
  const { t } = useT();
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const space = params.get("space") ?? "";
  const project = params.get("project") ?? "";
  const kinds = params.get("kind")?.split(",").filter(Boolean) ?? [];
  const statuses = (params.get("status")?.split(",").filter(Boolean) as Status[] | undefined) ?? DEFAULT_STATUSES;
  const tag = params.get("tag") ?? "";
  const pinned = params.get("pinned") === "true";

  const [draft, setDraft] = useState(q);
  useEffect(() => setDraft(q), [q]);

  const spaces = useAsync(() => api.spaces(), []);
  const projects = useAsync(() => api.projects(), []);
  const kindList = useAsync(() => api.kinds(), []);

  const [rows, setRows] = useState<Row[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  const set = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v === null || v === "") next.delete(k);
      else next.set(k, v);
    }
    setParams(next, { replace: true });
  };

  const load = async (more = false) => {
    setLoading(true);
    setError(null);
    try {
      const page = q.trim()
        ? await api.search({
            q, spaces: space ? [space] : undefined, project: project || undefined, project_only: !!project,
            kinds, status: statuses, tags: tag ? [tag] : undefined, limit: 30, cursor: more ? cursor : undefined,
          })
        : await api.listEntries({
            space: space || undefined, project: project || undefined, kind: kinds, status: statuses,
            tag: tag ? [tag] : undefined, pinned: pinned || undefined, limit: 30, cursor: more ? cursor : undefined,
          });
      setRows((r) => (more ? [...r, ...(page.items as Row[])] : (page.items as Row[])));
      setCursor(page.next_cursor || undefined);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  };

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => void load(false), [params.toString()]);

  const toggle = (list: string[], value: string) =>
    list.includes(value) ? list.filter((x) => x !== value) : [...list, value];

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.entries")}</h1>
          <p>{t("search.subtitle")}</p>
        </div>
        <Link to="/entries/new" className="btn primary">{t("common.newEntry")}</Link>
      </div>

      <div className="card stack">
        <form className="row" onSubmit={(e) => { e.preventDefault(); set({ q: draft }); }}>
          <input className="search-input grow" style={{ flex: 1 }} placeholder={t("search.placeholder")}
            value={draft} onChange={(e) => setDraft(e.target.value)} autoFocus />
          <button className="btn primary">{t("search.submit")}</button>
          {q && <button type="button" className="btn ghost" onClick={() => set({ q: null })}>{t("search.reset")}</button>}
        </form>
        <div className="form-grid">
          <label className="field">
            <span>{t("common.space")}</span>
            <select value={space} onChange={(e) => set({ space: e.target.value, project: null })}>
              <option value="">{t("search.allSpaces")}</option>
              {spaces.data?.map((s) => <option key={s.slug} value={s.slug}>{s.slug}</option>)}
            </select>
          </label>
          <label className="field">
            <span>{t("common.project")}</span>
            <select value={project} onChange={(e) => set({ project: e.target.value })}>
              <option value="">{t("search.anyProject")}</option>
              {projects.data?.filter((p) => !space || p.space === space).map((p) => (
                <option key={p.ref} value={p.ref}>{p.ref}</option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>{t("search.tag")}</span>
            <input value={tag} placeholder={t("search.tagPh")} onChange={(e) => set({ tag: e.target.value.trim().toLowerCase() })} />
          </label>
        </div>
        <div className="row small">
          <span className="muted">{t("search.kinds")}</span>
          {kindList.data?.map((k) => (
            <span key={k.kind} className={`chip ${kinds.includes(k.kind) ? "on" : ""}`}
              onClick={() => set({ kind: toggle(kinds, k.kind).join(",") })}>{k.kind}</span>
          ))}
        </div>
        <div className="row small">
          <span className="muted">{t("search.statuses")}</span>
          {STATUSES.map((st) => (
            <span key={st} className={`chip ${statuses.includes(st) ? "on" : ""}`}
              onClick={() => set({ status: toggle(statuses, st).join(",") || STATUSES.join(",") })}>{statusLabel(st, t)}</span>
          ))}
          {!q && (
            <label className="check right">
              <input type="checkbox" checked={pinned} onChange={(e) => set({ pinned: e.target.checked ? "true" : null })} />
              {t("search.onlyPinned")}
            </label>
          )}
        </div>
      </div>

      <ErrorBox error={error} />
      <div className="card" style={{ padding: 0, marginTop: 12 }}>
        {!loading && rows.length === 0 && <div className="empty">{q ? t("search.notFound", { q }) : t("search.noEntries")}</div>}
        {rows.map((r) => (
          <Link key={r.id} to={`/entries/${r.id}`} className="list-item">
            <div className="row">
              <span className="list-title">{r.title}</span>
              {"pinned" in r && r.pinned && <span className="badge pin">{t("common.pinned")}</span>}
            </div>
            {r.highlight ? (
              <div className="snippet"><Highlight text={r.highlight} /></div>
            ) : (
              r.summary && <div className="snippet">{r.summary}</div>
            )}
            <div className="row small muted" style={{ marginTop: 6 }}>
              <KindBadge kind={r.kind} />
              <StatusBadge status={r.status} />
              <span>{r.project ?? r.space}</span>
              <Tags tags={r.tags} />
              <span className="right">
                {"updated_by" in r ? `${r.updated_by} · ` : ""}{fmtAgo(r.updated_at)}
                {r.score !== undefined && ` · ${t("search.relevance", { score: r.score.toFixed(3) })}`}
              </span>
            </div>
          </Link>
        ))}
      </div>
      <div className="row" style={{ justifyContent: "center", marginTop: 12 }}>
        {loading && <span className="muted">{t("common.loading")}</span>}
        {!loading && cursor && <button className="btn" onClick={() => void load(true)}>{t("common.showMore")}</button>}
      </div>
    </>
  );
}
