import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { api, canReview, isAdmin } from "../api";
import { useAuth } from "../auth";
import { useT } from "../i18n";
import { PrefsControls } from "../prefs";

// Pages call notifyChanged() after mutations so counters (review queue) refresh.
export const notifyChanged = () => window.dispatchEvent(new Event("kn:changed"));

export function Layout() {
  const { me, logout } = useAuth();
  const { t } = useT();
  const [reviewCount, setReviewCount] = useState(0);

  useEffect(() => {
    const load = () => api.stats().then((s) => setReviewCount(s.review_queue), () => undefined);
    load();
    window.addEventListener("kn:changed", load);
    const t = setInterval(load, 60_000);
    return () => {
      window.removeEventListener("kn:changed", load);
      clearInterval(t);
    };
  }, []);

  const link = (to: string, label: string, count?: number) => (
    <NavLink to={to} end={to === "/"} className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
      <span>{label}</span>
      {!!count && <span className="nav-count">{count}</span>}
    </NavLink>
  );

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-logo">kn</div>
          Knowledge
        </div>
        {link("/", t("nav.overview"))}
        {canReview(me) && link("/review", t("nav.review"), reviewCount)}
        {link("/entries", t("nav.entries"))}
        {link("/entries/new", t("common.newEntry"))}
        {link("/projects", t("nav.projects"))}
        {link("/health", t("nav.health"))}
        {isAdmin(me) && (
          <>
            <div className="nav-section">{t("nav.admin")}</div>
            {link("/access", t("nav.access"))}
            {link("/audit", t("nav.audit"))}
            {link("/kinds", t("nav.kinds"))}
            {link("/data", t("nav.data"))}
          </>
        )}
        <PrefsControls className="sidebar-prefs" />
        <div className="sidebar-footer">
          <div>
            <strong style={{ color: "var(--text)" }}>{me?.principal.name}</strong> · {me?.principal.kind}
          </div>
          <div className="row" style={{ marginTop: 6 }}>
            {me?.spaces.map((s) => (
              <span key={s.slug} className="badge" title={t("layout.levelTitle", { level: s.level })}>{s.slug}: {s.level}</span>
            ))}
          </div>
          <button className="btn sm" style={{ marginTop: 10 }} onClick={() => void logout()}>{t("layout.logout")}</button>
        </div>
      </aside>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
