import { useState } from "react";
import { Link } from "react-router-dom";
import { api, type HealthCheck, type HealthItem } from "../api";
import { HealthTrend } from "../components/HealthTrend";
import { notifyChanged } from "../components/Layout";
import { ErrorBox, fmtAgo, KindBadge, Spinner, StatusBadge, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

const SEV_CLASS: Record<HealthCheck["severity"], string> = { ok: "s-active", info: "", warn: "s-proposed", critical: "s-deprecated" };

// Knowledge health report (ADR-8). Detectors are read-only; fixes below are explicit human actions
// built from existing operations (transition to superseded, links), each confirmed.
export function Health() {
  const { t, lang } = useT();
  const projects = useAsync(() => api.projects(), []);
  const [project, setProject] = useState("");
  const report = useAsync(() => api.health(project || undefined), [project, lang]);
  // Snapshots aggregate every space, so the API serves them to global admins only; others just see no trend.
  const history = useAsync(() => api.healthHistory(90).catch(() => null), []);
  const r = report.data;

  const problems = r?.checks.filter((c) => c.severity !== "ok") ?? [];
  const fine = r?.checks.filter((c) => c.severity === "ok") ?? [];

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.health")}</h1>
          <p>{t("health.subtitle")}</p>
        </div>
        <div className="row">
          <select value={project} onChange={(e) => setProject(e.target.value)} style={{ width: 260 }}>
            <option value="">{t("health.allSpaces")}</option>
            {projects.data?.map((p) => <option key={p.ref} value={p.ref}>{p.ref}</option>)}
          </select>
          <button className="btn" onClick={report.reload}>{t("health.refresh")}</button>
        </div>
      </div>
      <ErrorBox error={report.error} />
      {report.loading && !r && <Spinner />}
      {r && (
        <>
          <div className="grid grid-4">
            <div className="card">
              <div className="stat-value" style={{ color: r.score >= 80 ? "var(--ok)" : r.score >= 50 ? "var(--warn)" : "var(--danger)" }}>{r.score}</div>
              <div className="stat-label">{t("health.score")}</div>
            </div>
            <div className="card">
              <div className="stat-value">{r.live_entries}</div>
              <div className="stat-label">{t("health.live", { scope: r.project ?? r.spaces.join(", ") })}</div>
            </div>
            <div className="card">
              <div className="stat-value">{problems.length}</div>
              <div className="stat-label">{t("health.problemChecks", { total: r.checks.length })}</div>
            </div>
          </div>
          {!project && history.data && (
            <div className="card" style={{ marginTop: 16 }}>
              <h3>{t("health.trendTitle")}</h3>
              <HealthTrend history={history.data} checks={r.checks} />
            </div>
          )}
          <div className="stack" style={{ marginTop: 16 }}>
            {problems.map((c) => <CheckCard key={c.id} check={c} onFixed={() => { report.reload(); notifyChanged(); }} />)}
          </div>
          {fine.length > 0 && (
            <div className="card" style={{ marginTop: 12 }}>
              <h3>{t("health.ok")}</h3>
              {fine.map((c) => <div key={c.id} className="small"><span className="badge s-active">✓</span> <strong>{c.title}</strong> — <span className="muted">{c.message}</span></div>)}
            </div>
          )}
          <div className="small muted" style={{ marginTop: 10 }}>{t("health.generated", { ago: fmtAgo(r.generated_at) })}</div>
        </>
      )}
    </>
  );
}

function ItemLink({ it }: { it: HealthItem }) {
  const { t } = useT();
  return (
    <span className="row" style={{ gap: 6, display: "inline-flex" }}>
      <Link to={`/entries/${it.id}`}>{it.title}</Link>
      <KindBadge kind={it.kind} />
      {it.status !== "active" && <StatusBadge status={it.status} />}
      <span className="muted small">{it.project ?? it.space}{it.links ? ` · ${t("health.links", { n: it.links })}` : ""}</span>
    </span>
  );
}

function CheckCard({ check: c, onFixed }: { check: HealthCheck; onFixed: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const [busy, setBusy] = useState(false);

  // Keep one entry, mark the others as superseded by it. Nothing is deleted: history stays.
  const keep = async (keeper: HealthItem, group: HealthItem[]) => {
    const others = group.filter((g) => g.id !== keeper.id);
    if (!confirm(t("health.confirmKeep", { title: keeper.title, n: others.length }))) return;
    setBusy(true);
    let done = 0;
    for (const o of others) {
      try {
        await api.transition(o.id, "superseded", { superseded_by: keeper.id, note: t("health.dupNote") });
        done++;
      } catch (e) {
        toast.error(e);
      }
    }
    setBusy(false);
    if (done) {
      toast.ok(t("health.replacedCopies", { n: done }));
      onFixed();
    }
  };

  // Bulk: keep the recommended (first) entry of every shown group.
  const keepAll = async (groups: HealthItem[][]) => {
    const copies = groups.reduce((n, g) => n + g.length - 1, 0);
    if (!confirm(t("health.confirmKeepAll", { groups: groups.length, copies }))) return;
    setBusy(true);
    let done = 0, failed = 0;
    for (const g of groups) {
      for (const o of g.slice(1)) {
        try {
          await api.transition(o.id, "superseded", { superseded_by: g[0].id, note: t("health.dupNote") });
          done++;
        } catch {
          failed++;
        }
      }
    }
    setBusy(false);
    if (failed) toast.error(new Error(t("health.failedCopies", { n: failed })));
    if (done) {
      toast.ok(t("health.replacedCopies", { n: done }));
      onFixed();
    }
  };

  const link = async (a: HealthItem, b: HealthItem) => {
    setBusy(true);
    try {
      await api.addLink(a.id, b.id, "relates_to");
      toast.ok(t("health.linked"));
      onFixed();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const supersede = async (keeper: HealthItem, old: HealthItem) => {
    if (!confirm(t("health.confirmSupersede", { old: old.title, keeper: keeper.title }))) return;
    setBusy(true);
    try {
      await api.transition(old.id, "superseded", { superseded_by: keeper.id, note: t("health.nearDupNote") });
      toast.ok(t("health.superseded"));
      onFixed();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <div className="row between">
        <h2 style={{ margin: 0 }}>{c.title}</h2>
        <span className={`badge ${SEV_CLASS[c.severity]}`}>{t(`sev.${c.severity}`)} · {c.count}</span>
      </div>
      <p style={{ margin: "8px 0" }}>{c.message}</p>
      {c.suggestion && <div className="summary small">{c.suggestion}</div>}
      {c.groups && c.groups.length > 1 && (
        <div className="row" style={{ marginTop: 8 }}>
          <button className="btn ok" disabled={busy} onClick={() => void keepAll(c.groups!)}>
            {t("health.keepAll", { n: c.groups.length })}
          </button>
          {c.count > c.groups.reduce((n, g) => n + g.length - 1, 0) && (
            <span className="small muted">{t("health.partial")}</span>
          )}
        </div>
      )}

      {c.groups?.map((g, i) => (
        <div key={i} className="card" style={{ padding: 10, marginTop: 8 }}>
          <div className="small muted" style={{ marginBottom: 6 }}>{t("health.sameText", { n: g.length })}</div>
          {g.map((it, j) => (
            <div key={it.id} className="row" style={{ justifyContent: "space-between", padding: "3px 0" }}>
              <ItemLink it={it} />
              <button className={`btn sm ${j === 0 ? "ok" : ""}`} disabled={busy} onClick={() => void keep(it, g)}>
                {t("health.keepThis")}{j === 0 ? t("health.recommended") : ""}
              </button>
            </div>
          ))}
        </div>
      ))}

      {c.pairs && c.pairs.length > 0 && (
        <table style={{ marginTop: 8 }}>
          <tbody>
            {c.pairs.map((p, i) => (
              <tr key={i}>
                <td><ItemLink it={p.a} /></td>
                <td className="nowrap muted small">{p.rel ? `— ${p.rel} →` : t("health.similarity", { pct: Math.round((p.similarity ?? 0) * 100) })}</td>
                <td><ItemLink it={p.b} /></td>
                {c.id === "near_duplicate_titles" && (
                  <td className="nowrap">
                    <button className="btn sm" disabled={busy} onClick={() => void link(p.a, p.b)}>{t("common.link")}</button>{" "}
                    <button className="btn sm" disabled={busy} onClick={() => void supersede(p.a, p.b)} title={t("health.supersedeTitle")}>{t("health.supersedeSecond")}</button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {c.items && c.items.length > 0 && (
        <div style={{ marginTop: 8 }}>
          {c.items.map((it) => (
            <div key={it.id} className="row" style={{ justifyContent: "space-between", padding: "3px 0" }}>
              <ItemLink it={it} />
              <span className="muted small">{it.detail}</span>
            </div>
          ))}
        </div>
      )}

      {c.stats && c.stats.length > 0 && (
        <table style={{ marginTop: 8 }}>
          <thead><tr><th>{t("common.who")}</th><th>{t("common.action")}</th><th>{t("health.colCalls")}</th><th>{t("health.colEmpty")}</th></tr></thead>
          <tbody>
            {c.stats.map((s, i) => (
              <tr key={i}><td>{s.principal}</td><td><code>{s.action}</code></td><td>{s.calls}</td><td>{s.empty} ({s.calls ? Math.round((s.empty / s.calls) * 100) : 0}%)</td></tr>
            ))}
          </tbody>
        </table>
      )}

      {c.sessions && c.sessions.length > 0 && (
        <table style={{ marginTop: 8 }}>
          <tbody>
            {c.sessions.map((s) => (
              <tr key={s.id}><td>{s.principal}</td><td>{s.project ?? s.space}</td><td className="small muted">{t("health.sessionStarted", { ago: fmtAgo(s.started_at), n: s.entries })}</td></tr>
            ))}
          </tbody>
        </table>
      )}

      {c.tokens && c.tokens.length > 0 && (
        <div className="small" style={{ marginTop: 8 }}>
          {c.tokens.map((tk) => <div key={tk.id}><code>kn_{tk.prefix}_…</code> {tk.principal} — {tk.scopes.join(" ")} · {t("health.tokenUsed", { ago: fmtAgo(tk.last_used_at) })}</div>)}
          <Link to="/access">{t("health.openAccess")}</Link>
        </div>
      )}
    </div>
  );
}
