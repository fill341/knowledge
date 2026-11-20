import { useState } from "react";
import { api, type Kind } from "../api";
import { ErrorBox, Modal, Spinner, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

export function Kinds() {
  const { t } = useT();
  const kinds = useAsync(() => api.kinds(), []);
  const [edit, setEdit] = useState<Kind | "new" | null>(null);
  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.kinds")}</h1>
          <p>{t("kinds.subtitle")}</p>
        </div>
        <button className="btn primary" onClick={() => setEdit("new")}>{t("kinds.new")}</button>
      </div>
      <ErrorBox error={kinds.error} />
      {kinds.loading && !kinds.data ? <Spinner /> : (
        <div className="grid grid-2">
          {kinds.data?.map((k) => (
            <div key={k.kind} className="card">
              <div className="row between">
                <span className="badge kind" style={{ fontSize: 13 }}>{k.kind}</span>
                <button className="btn sm" onClick={() => setEdit(k)}>{t("common.edit")}</button>
              </div>
              <div style={{ margin: "8px 0" }}>{k.description}</div>
              <div className="small muted">
                {k.agent_requires_review ? t("kinds.agentReview") : t("kinds.agentActive")}
                {" · "}{k.review_days ? t("kinds.reviewEvery", { n: k.review_days }) : t("kinds.noReview")}
              </div>
              <div className="small muted" style={{ marginTop: 6 }}>
                {t("kinds.fields", { fields: Object.keys((k.attributes_schema?.properties ?? {}) as object).join(", ") || "—" })}
              </div>
            </div>
          ))}
        </div>
      )}
      {edit && <KindModal kind={edit === "new" ? null : edit} onClose={() => setEdit(null)} onSaved={kinds.reload} />}
    </>
  );
}

function KindModal({ kind, onClose, onSaved }: { kind: Kind | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const [name, setName] = useState(kind?.kind ?? "");
  const [description, setDescription] = useState(kind?.description ?? "");
  const [review, setReview] = useState(kind?.agent_requires_review ?? false);
  const [days, setDays] = useState(kind?.review_days ? String(kind.review_days) : "");
  const [schema, setSchema] = useState(JSON.stringify(kind?.attributes_schema ?? { type: "object", properties: {} }, null, 2));
  const [error, setError] = useState<unknown>(null);

  const save = async () => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(schema);
    } catch (e) {
      return setError(new Error(t("kinds.schemaInvalid", { error: (e as Error).message })));
    }
    try {
      await api.putKind(name, { description, attributes_schema: parsed, agent_requires_review: review,
        review_days: days.trim() ? Number(days) : null });
      toast.ok(t("kinds.saved"));
      onSaved();
      onClose();
    } catch (e) {
      setError(e);
    }
  };

  return (
    <Modal title={kind ? t("kinds.modalTitle", { kind: kind.kind }) : t("kinds.new")} onClose={onClose} wide
      footer={<><button className="btn" onClick={onClose}>{t("common.cancel")}</button><button className="btn primary" onClick={() => void save()}>{t("common.save")}</button></>}>
      <div className="stack">
        <div className="form-grid">
          <label className="field"><span>{t("common.name")}</span><input value={name} disabled={!!kind} placeholder="runbook" onChange={(e) => setName(e.target.value)} /></label>
          <label className="field"><span>{t("common.description")}</span><input value={description} onChange={(e) => setDescription(e.target.value)} /></label>
        </div>
        <label className="check"><input type="checkbox" checked={review} onChange={(e) => setReview(e.target.checked)} /> {t("kinds.reviewCheck")}</label>
        <label className="field"><span>{t("kinds.reviewDays")}</span>
          <input type="number" min={1} max={3650} value={days} placeholder={t("kinds.reviewDaysPh")} style={{ width: 200 }} onChange={(e) => setDays(e.target.value)} />
          <span className="small muted">{t("kinds.reviewDaysHint")}</span>
        </label>
        <label className="field"><span>{t("kinds.schema")}</span><textarea rows={14} value={schema} onChange={(e) => setSchema(e.target.value)} /></label>
        {kind && <div className="small muted">{t("kinds.existingHint")}</div>}
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}
