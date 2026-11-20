import { lazy, Suspense, useState } from "react";
import { Link } from "react-router-dom";
import { api, canReview, isAdmin } from "../api";
import { useAuth } from "../auth";
import { ErrorBox, fmtAgo, KindBadge, Spinner, StatusBadge, statusLabel, useAsync } from "../components/ui";
import { useT } from "../i18n";

// three.js is heavy: both views are separate chunks loaded only on this page.
const KnowledgeGraph = lazy(() => import("../components/KnowledgeGraph"));
const Galaxy = lazy(() => import("../components/Galaxy"));

type View = "galaxy" | "graph";
const VIEW_KEY = "kn.overview.view";

function KnowledgeSpace() {
  const { t } = useT();
  const [view, setView] = useState<View>(() => {
    try {
      return (localStorage.getItem(VIEW_KEY) as View) || "galaxy";
    } catch {
      return "galaxy";
    }
  });
  const choose = (v: View) => {
    setView(v);
    try {
      localStorage.setItem(VIEW_KEY, v);
    } catch {
      /* private mode: the choice just is not remembered */
    }
  };
  return (
    <>
      <div className="row between" style={{ marginTop: 20 }}>
        <h2 style={{ margin: 0 }}>{t("dash.map")}</h2>
        <div className="segmented">
          <button className={view === "galaxy" ? "on" : ""} onClick={() => choose("galaxy")}>{t("dash.galaxy")}</button>
          <button className={view === "graph" ? "on" : ""} onClick={() => choose("graph")}>{t("dash.graph")}</button>
        </div>
      </div>
      <Suspense fallback={<div className="graph-wrap"><div className="graph-overlay">{t("dash.loading3d")}</div></div>}>
        {view === "galaxy" ? <Galaxy key="galaxy" /> : <KnowledgeGraph key="graph" />}
      </Suspense>
    </>
  );
}

const STATUS_COLOR: Record<string, string> = {
  active: "var(--ok)",
  proposed: "var(--warn)",
  draft: "var(--muted)",
  deprecated: "var(--danger)",
  superseded: "var(--danger)",
  archived: "var(--border)",
};

export function Dashboard() {
  const { me } = useAuth();
  const { t, rich } = useT();
  const stats = useAsync(() => api.stats(), []);
  const recent = useAsync(() => api.listEntries({ limit: 8 }), []);
  const audit = useAsync(() => (isAdmin(me) ? api.audit({ limit: 8 }) : Promise.resolve([])), [me]);

  if (stats.loading && !stats.data) return <Spinner />;
  const s = stats.data;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.overview")}</h1>
          <p>{rich("dash.subtitle", { name: <strong>{me?.principal.name}</strong> })}</p>
        </div>
        <Link to="/entries/new" className="btn primary">{t("common.newEntry")}</Link>
      </div>
      <ErrorBox error={stats.error} />
      {s && (
        <div className="grid grid-4">
          {canReview(me) && (
            <Link to="/review" className="card" style={{ color: "inherit", textDecoration: "none" }}>
              <div className="stat-value" style={{ color: s.review_queue ? "var(--warn)" : undefined }}>{s.review_queue}</div>
              <div className="stat-label">{t("dash.waitingReview")}</div>
            </Link>
          )}
          <div className="card">
            <div className="stat-value">{s.total}</div>
            <div className="stat-label">{t("dash.total")}</div>
          </div>
          <Link to="/projects" className="card" style={{ color: "inherit", textDecoration: "none" }}>
            <div className="stat-value">{s.projects}</div>
            <div className="stat-label">{t("dash.projects")}</div>
          </Link>
          <div className="card">
            <div className="stat-value">{Object.keys(s.by_kind).length}</div>
            <div className="stat-label">{t("dash.kindsUsed")}</div>
          </div>
        </div>
      )}

      <KnowledgeSpace />

      <div className="grid grid-2" style={{ marginTop: 16 }}>
        <div className="card">
          <h2>{t("dash.bySpace")}</h2>
          {s && Object.keys(s.by_space).length === 0 && <div className="muted">{t("dash.noEntries")}</div>}
          {s &&
            Object.entries(s.by_space).map(([space, byStatus]) => {
              const total = Object.values(byStatus).reduce((a, b) => a + b, 0);
              return (
                <div key={space} style={{ marginBottom: 14 }}>
                  <div className="row between small">
                    <Link to={`/entries?space=${space}`}><strong>{space}</strong></Link>
                    <span className="muted">{total}</span>
                  </div>
                  <div className="bar" style={{ margin: "6px 0" }}>
                    {Object.entries(byStatus).map(([st, n]) => (
                      <span key={st} title={`${statusLabel(st, t)}: ${n}`}
                        style={{ width: `${(n / total) * 100}%`, background: STATUS_COLOR[st] }} />
                    ))}
                  </div>
                  <div className="row small muted">
                    {Object.entries(byStatus).map(([st, n]) => (
                      <span key={st}>{statusLabel(st, t)}: {n}</span>
                    ))}
                  </div>
                </div>
              );
            })}
          {s && Object.keys(s.by_kind).length > 0 && (
            <>
              <h3 style={{ marginTop: 18 }}>{t("dash.byKind")}</h3>
              <div className="row">
                {Object.entries(s.by_kind).sort((a, b) => b[1] - a[1]).map(([k, n]) => (
                  <Link key={k} to={`/entries?kind=${k}`} className="badge kind">{k} · {n}</Link>
                ))}
              </div>
            </>
          )}
        </div>

        <div className="card" style={{ padding: 0 }}>
          <div style={{ padding: "16px 16px 4px" }}><h2>{t("dash.recent")}</h2></div>
          {recent.data?.items.length === 0 && <div className="empty">{t("dash.empty")}</div>}
          {recent.data?.items.map((e) => (
            <Link key={e.id} to={`/entries/${e.id}`} className="list-item">
              <div className="row">
                <span className="list-title">{e.title}</span>
              </div>
              <div className="row small muted" style={{ marginTop: 4 }}>
                <KindBadge kind={e.kind} />
                <StatusBadge status={e.status} />
                <span>{e.project ?? e.space}</span>
                <span className="right">{e.updated_by} · {fmtAgo(e.updated_at)}</span>
              </div>
            </Link>
          ))}
        </div>
      </div>

      {isAdmin(me) && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="row between"><h2>{t("dash.events")}</h2><Link to="/audit" className="small">{t("dash.allAudit")}</Link></div>
          <table>
            <tbody>
              {audit.data?.map((a) => (
                <tr key={a.id}>
                  <td className="nowrap muted small">{fmtAgo(a.at)}</td>
                  <td>{a.principal ?? "—"}</td>
                  <td><code>{a.action}</code></td>
                  <td className="small muted">
                    {a.target_type === "entry" && a.target_id ? <Link to={`/entries/${a.target_id}`}>{t("common.entryWord")}</Link> : a.target_type}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
