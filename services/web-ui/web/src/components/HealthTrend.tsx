import { useState } from "react";
import { type HealthCheck, type HealthSnapshot } from "../api";
import { useT, type TFunc } from "../i18n";

// Score trend from daily health snapshots (ADR-13): one series, so no legend — the title names it.
// A 2px line, a hover crosshair with a tooltip, and a table of per-check changes as the table view.
export function HealthTrend({ history, checks }: { history: HealthSnapshot[]; checks: HealthCheck[] }) {
  const { t } = useT();
  const [hover, setHover] = useState<number | null>(null);
  if (history.length === 0) {
    return <div className="small muted">{t("trend.empty")}</div>;
  }
  const W = 560, H = 120, P = { l: 30, r: 10, t: 10, b: 20 };
  const n = history.length;
  const x = (i: number) => P.l + (n === 1 ? (W - P.l - P.r) / 2 : (i * (W - P.l - P.r)) / (n - 1));
  const y = (s: number) => P.t + ((100 - s) * (H - P.t - P.b)) / 100;
  const path = history.map((h, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(h.score).toFixed(1)}`).join(" ");
  const last = history[n - 1];
  const weekAgo = new Date(Date.now() - 7 * 864e5).toISOString().slice(0, 10);
  const base = [...history].reverse().find((h) => h.day <= weekAgo) ?? history[0];
  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * W;
    let best = 0;
    history.forEach((_, i) => { if (Math.abs(x(i) - px) < Math.abs(x(best) - px)) best = i; });
    setHover(best);
  };
  const h = hover !== null ? history[hover] : null;
  const changed = checks
    .map((c) => ({ c, was: base.checks[c.id]?.count }))
    .filter(({ c, was }) => was !== undefined && was !== c.count);

  return (
    <div>
      <div style={{ position: "relative", maxWidth: W }}>
        <svg viewBox={`0 0 ${W} ${H}`} width={W} height={H} role="img" aria-label={t("trend.aria", { n, score: last.score })}
          onMouseMove={onMove} onMouseLeave={() => setHover(null)} style={{ display: "block", maxWidth: "100%", height: "auto" }}>
          {[0, 50, 100].map((g) => (
            <g key={g}>
              <line x1={P.l} x2={W - P.r} y1={y(g)} y2={y(g)} stroke="var(--border)" strokeWidth={1} />
              <text x={P.l - 6} y={y(g) + 4} textAnchor="end" fontSize={10} fill="var(--muted)">{g}</text>
            </g>
          ))}
          <text x={P.l} y={H - 4} fontSize={10} fill="var(--muted)">{history[0].day}</text>
          <text x={W - P.r} y={H - 4} fontSize={10} fill="var(--muted)" textAnchor="end">{last.day}</text>
          <path d={path} fill="none" stroke="var(--accent)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
          {n === 1 && <circle cx={x(0)} cy={y(last.score)} r={4} fill="var(--accent)" />}
          {h && hover !== null && (
            <g>
              <line x1={x(hover)} x2={x(hover)} y1={P.t} y2={H - P.b} stroke="var(--muted)" strokeWidth={1} strokeDasharray="3 3" />
              <circle cx={x(hover)} cy={y(h.score)} r={5} fill="var(--accent)" stroke="var(--surface)" strokeWidth={2} />
            </g>
          )}
        </svg>
        {h && hover !== null && (
          <div className="card small" style={{ position: "absolute", top: 0, left: `${(x(hover) / W) * 100}%`, transform: "translateX(-50%)",
            padding: "4px 8px", pointerEvents: "none", whiteSpace: "nowrap" }}>
            <strong>{h.score}</strong>/100 · {h.day} · {t("trend.entries", { n: h.live_entries })}
          </div>
        )}
      </div>
      <table style={{ marginTop: 8 }}>
        <thead><tr><th>{t("trend.check")}</th><th>{base.day}</th><th>{t("trend.now")}</th><th>{t("trend.change")}</th></tr></thead>
        <tbody>
          <tr><td>{t("trend.score")}</td><td>{base.score}</td><td>{last.score}</td><td>{fmtDelta(t, last.score - base.score, true)}</td></tr>
          {changed.map(({ c, was }) => (
            <tr key={c.id}><td>{c.title}</td><td>{was}</td><td>{c.count}</td><td>{fmtDelta(t, c.count - (was ?? 0), false)}</td></tr>
          ))}
        </tbody>
      </table>
      {changed.length === 0 && <div className="small muted">{t("trend.unchanged", { day: base.day })}</div>}
    </div>
  );
}

// For the score higher is better; for problem counts lower is better.
function fmtDelta(t: TFunc, d: number, higherBetter: boolean) {
  if (d === 0) return <span className="muted">0</span>;
  const good = higherBetter ? d > 0 : d < 0;
  return <span style={{ color: good ? "var(--ok)" : "var(--danger)" }}>{d > 0 ? `+${d}` : d} {good ? t("trend.better") : t("trend.worse")}</span>;
}
