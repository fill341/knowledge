import { Link } from "react-router-dom";
import { useState } from "react";
import { api, type ImportResult } from "../api";
import { notifyChanged } from "../components/Layout";
import { ErrorBox, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

export function Data() {
  const toast = useToast();
  const { t, rich } = useT();
  const spaces = useAsync(() => api.spaces(), []);
  const [selected, setSelected] = useState<string[]>([]);
  const [history, setHistory] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [mode, setMode] = useState("create_only");
  const [skipDup, setSkipDup] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const toggle = (s: string) => setSelected((x) => (x.includes(s) ? x.filter((y) => y !== s) : [...x, s]));

  const runImport = async (dryRun: boolean) => {
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      const r = await api.importJsonl(file, mode, dryRun, skipDup);
      setResult(r);
      if (!dryRun) {
        toast.ok(t("data.imported"));
        notifyChanged();
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.data")}</h1>
          <p>{t("data.subtitle")}</p>
        </div>
      </div>
      <div className="grid grid-2">
        <div className="card stack">
          <h2>{t("data.export")}</h2>
          <div className="row small">
            <span className="muted">{t("data.spaces")}</span>
            {spaces.data?.map((s) => (
              <span key={s.slug} className={`chip ${selected.includes(s.slug) ? "on" : ""}`} onClick={() => toggle(s.slug)}>{s.slug}</span>
            ))}
            <span className="muted">{selected.length ? "" : t("data.allAccessible")}</span>
          </div>
          <label className="check"><input type="checkbox" checked={history} onChange={(e) => setHistory(e.target.checked)} /> {t("data.history")}</label>
          <div className="row">
            <a className="btn primary" href={api.exportUrl("jsonl", selected, history)}>{t("data.downloadJsonl")}</a>
            <a className="btn" href={api.exportUrl("markdown", selected, false)}>{t("data.downloadMd")}</a>
          </div>
          <div className="small muted">{t("data.backups")}</div>
        </div>

        <div className="card stack">
          <h2>{t("data.import")}</h2>
          <input type="file" accept=".jsonl,.ndjson,application/x-ndjson" onChange={(e) => { setFile(e.target.files?.[0] ?? null); setResult(null); }} />
          <label className="field"><span>{t("data.mode")}</span>
            <select value={mode} onChange={(e) => setMode(e.target.value)}>
              <option value="create_only">{t("data.modeCreate")}</option>
              <option value="upsert_by_id">{t("data.modeUpsert")}</option>
            </select>
          </label>
          <label className="row small">
            <input type="checkbox" checked={skipDup} onChange={(e) => { setSkipDup(e.target.checked); setResult(null); }} />
            {t("data.skipDup")}
          </label>
          <div className="row">
            <button className="btn" disabled={!file || busy} onClick={() => void runImport(true)}>{t("data.dryRun")}</button>
            <button className="btn primary" disabled={!file || busy || !result?.dry_run}
              title={!result?.dry_run ? t("data.dryFirst") : ""} onClick={() => void runImport(false)}>{t("data.run")}</button>
          </div>
          <ErrorBox error={error} />
          {result && (
            <div className={`alert ${result.errors.length ? "error" : "ok"}`}>
              <strong>{result.dry_run ? t("data.dryRunDone") : t("data.imported")}:</strong>{" "}
              {t("data.summary", {
                projects: result.projects_created, created: result.entries_created, updated: result.entries_updated,
                skipped: result.entries_skipped, links: result.links_created, errors: result.errors.length, dups: result.duplicates.length,
              })}{result.duplicates.some((d) => d.skipped) ? t("data.skipped") : ""}
              {result.duplicates.slice(0, 10).map((d) => (
                <div key={d.line} className="small">{rich("data.dupLine", { line: d.line, title: d.title, link: <Link to={`/entries/${d.existing_id}`}>{d.existing_id.slice(0, 8)}</Link> })}</div>
              ))}
              {result.errors.slice(0, 10).map((e) => (
                <div key={e.line} className="small mono">{t("data.errLine", { line: e.line, error: e.error })}</div>
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  );
}
