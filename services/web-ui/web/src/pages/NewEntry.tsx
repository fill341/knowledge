import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, ApiError, type Warning } from "../api";
import { attributesTemplate, emptyForm, EntryForm, toPayload } from "../components/EntryForm";
import { notifyChanged } from "../components/Layout";
import { ErrorBox, Spinner, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

export function NewEntry() {
  const navigate = useNavigate();
  const toast = useToast();
  const { t } = useT();
  const kinds = useAsync(() => api.kinds(), []);
  const spaces = useAsync(() => api.spaces(), []);
  const projects = useAsync(() => api.projects(), []);
  const [value, setValue] = useState(emptyForm);
  const [draft, setDraft] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [duplicate, setDuplicate] = useState<string | null>(null);
  const [warnings, setWarnings] = useState<{ id: string; list: Warning[] } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (kinds.data && value.attributes === "{}") {
      setValue((v) => ({ ...v, attributes: attributesTemplate(kinds.data!.find((k) => k.kind === v.kind)) }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kinds.data]);

  if (!kinds.data || !spaces.data || !projects.data) return kinds.error ? <ErrorBox error={kinds.error} /> : <Spinner />;

  const submit = async () => {
    const { payload, error: perr } = toPayload(value);
    if (perr) return setError(new Error(perr));
    if (!value.space && !value.project) return setError(new Error(t("newEntry.chooseSpace")));
    payload.kind = value.kind;
    if (value.project) payload.project = value.project;
    else payload.space = value.space;
    for (const k of ["review_after", "source_ref", "summary"]) if (!payload[k]) delete payload[k];
    if (draft) payload.draft = true;
    setBusy(true);
    setError(null);
    setDuplicate(null);
    try {
      const r = await api.createEntry(payload);
      notifyChanged();
      if (r.warnings.length) {
        setWarnings({ id: r.entry.id, list: r.warnings });
      } else {
        toast.ok(t("newEntry.created"));
        navigate(`/entries/${r.entry.id}`);
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "duplicate" && e.existingId) setDuplicate(e.existingId);
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  if (warnings) {
    return (
      <div className="card stack">
        <h2>{t("newEntry.createdNearby")}</h2>
        {warnings.list.map((w) => (
          <div key={w.code}>
            <div className="muted">{w.code === "duplicate_content"
              ? t("newEntry.dupContent")
              : w.code === "suggested_links"
                ? t("newEntry.suggested")
                : t("newEntry.similarTitles")}</div>
            {w.items?.map((i) => (
              <div key={i.id} className="row" style={{ gap: 8 }}><Link to={`/entries/${i.id}`}>{i.title}</Link>
                {w.code === "suggested_links"
                  ? <button className="btn sm" onClick={() => void api.addLink(warnings.id, i.id, "relates_to").then(() => toast.ok(t("common.linkAdded")), toast.error)}>{t("common.link")}</button>
                  : <span className="muted small">{w.code === "duplicate_content" ? i.status : t("newEntry.similarity", { value: i.similarity.toFixed(2) })}</span>}
              </div>
            ))}
          </div>
        ))}
        <div className="row"><Link className="btn primary" to={`/entries/${warnings.id}`}>{t("newEntry.openNew")}</Link></div>
      </div>
    );
  }

  return (
    <>
      <div className="page-head"><div><h1>{t("common.newEntry")}</h1><p>{t("newEntry.subtitle")}</p></div></div>
      <div className="card stack">
        <EntryForm value={value} onChange={setValue} kinds={kinds.data} spaces={spaces.data} projects={projects.data}
          isNew kindInfo={kinds.data.find((k) => k.kind === value.kind)} />
        <label className="check">
          <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} />
          {t("newEntry.draft")}
        </label>
        <ErrorBox error={error} />
        {duplicate && <div><Link to={`/entries/${duplicate}`}>{t("newEntry.openExisting")}</Link></div>}
        <div className="row"><button className="btn primary" disabled={busy} onClick={() => void submit()}>{t("common.create")}</button></div>
      </div>
    </>
  );
}
