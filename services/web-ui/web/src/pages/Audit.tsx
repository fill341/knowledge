import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type AuditRecord } from "../api";
import { ErrorBox, fmtDate } from "../components/ui";
import { useT } from "../i18n";

const ACTIONS = [
  "entry.create", "entry.update", "entry.transition", "entry.review", "entry.promote", "entry.revert", "entry.import",
  "link.create", "link.delete", "project.create", "project.update", "session.start", "session.end",
  "token.create", "token.revoke", "principal.create", "principal.disable", "principal.enable", "kind.put",
  "search", "context", "export", "import", "auth.failed",
];

export function Audit() {
  const { t } = useT();
  const [principal, setPrincipal] = useState("");
  const [action, setAction] = useState("");
  const [rows, setRows] = useState<AuditRecord[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [more, setMore] = useState(false);

  const load = async (beforeId?: number) => {
    setError(null);
    try {
      const page = await api.audit({ principal: principal || undefined, action: action || undefined, before_id: beforeId, limit: 100 });
      setRows((r) => (beforeId ? [...r, ...page] : page));
      setMore(page.length === 100);
    } catch (e) {
      setError(e);
    }
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => void load(), [principal, action]);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.audit")}</h1>
          <p>{t("audit.subtitle")}</p>
        </div>
      </div>
      <div className="card form-grid" style={{ marginBottom: 12 }}>
        <label className="field"><span>Principal</span><input value={principal} placeholder="claude-code@personal" onChange={(e) => setPrincipal(e.target.value.trim())} /></label>
        <label className="field"><span>{t("common.action")}</span>
          <select value={action} onChange={(e) => setAction(e.target.value)}>
            <option value="">{t("audit.all")}</option>
            {ACTIONS.map((a) => <option key={a}>{a}</option>)}
          </select>
        </label>
      </div>
      <ErrorBox error={error} />
      <div className="card" style={{ padding: 0 }}>
        <table>
          <thead><tr><th>{t("common.when")}</th><th>{t("common.who")}</th><th>{t("common.action")}</th><th>{t("common.space")}</th><th>{t("audit.colObject")}</th><th>{t("audit.colDetails")}</th></tr></thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.id}>
                <td className="nowrap small">{fmtDate(a.at)}</td>
                <td>{a.principal ?? <span className="muted">—</span>}</td>
                <td><code style={a.action === "auth.failed" ? { color: "var(--danger)" } : undefined}>{a.action}</code></td>
                <td>{a.space ?? ""}</td>
                <td className="small">
                  {a.target_type === "entry" && a.target_id ? <Link to={`/entries/${a.target_id}`}>{t("audit.entryRef", { id: a.target_id.slice(0, 8) })}</Link>
                    : a.target_id ? `${a.target_type} ${a.target_id.slice(0, 8)}` : ""}
                </td>
                <td className="mono muted" style={{ maxWidth: 360, wordBreak: "break-word" }}>{JSON.stringify(a.details)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {rows.length === 0 && <div className="empty">{t("audit.none")}</div>}
      </div>
      {more && (
        <div className="row" style={{ justifyContent: "center", marginTop: 12 }}>
          <button className="btn" onClick={() => void load(rows[rows.length - 1]?.id)}>{t("common.showMore")}</button>
        </div>
      )}
    </>
  );
}
