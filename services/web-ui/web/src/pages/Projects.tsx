import { useState } from "react";
import { Link } from "react-router-dom";
import { api, type Project } from "../api";
import { ErrorBox, fmtDate, Modal, Spinner, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

export function Projects() {
  const toast = useToast();
  const { t, rich } = useT();
  const [showArchived, setShowArchived] = useState(false);
  const projects = useAsync(() => api.projects(showArchived), [showArchived]);
  const [edit, setEdit] = useState<Project | "new" | null>(null);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.projects")}</h1>
          <p>{rich("projects.subtitle", { remote: <code>git remote</code> })}</p>
        </div>
        <div className="row">
          <label className="check"><input type="checkbox" checked={showArchived} onChange={(e) => setShowArchived(e.target.checked)} /> {t("projects.archived")}</label>
          <button className="btn primary" onClick={() => setEdit("new")}>{t("projects.new")}</button>
        </div>
      </div>
      <ErrorBox error={projects.error} />
      {projects.loading && !projects.data ? <Spinner /> : (
        <div className="card" style={{ padding: 0 }}>
          {projects.data?.length === 0 && <div className="empty">{t("projects.none")}</div>}
          {!!projects.data?.length && (
            <table>
              <thead><tr><th>{t("common.project")}</th><th>{t("projects.colRepos")}</th><th>{t("common.created")}</th><th /></tr></thead>
              <tbody>
                {projects.data.map((p) => (
                  <tr key={p.id} className={p.archived_at ? "dim" : ""}>
                    <td>
                      <Link to={`/entries?project=${p.ref}`}><strong>{p.name}</strong></Link>
                      <div className="mono muted">{p.ref}</div>
                      {p.description && <div className="small muted">{p.description}</div>}
                    </td>
                    <td className="small">
                      {p.repo_urls.length ? p.repo_urls.map((u) => <div key={u} className="mono">{u}</div>)
                        : <span className="muted">{t("projects.notLinked")}</span>}
                    </td>
                    <td className="small nowrap">{fmtDate(p.created_at, false)}</td>
                    <td className="nowrap">
                      <button className="btn sm" onClick={() => setEdit(p)}>{t("common.edit")}</button>{" "}
                      <button className="btn ghost sm" onClick={() =>
                        void api.patchProject(p.ref, { archived: !p.archived_at }).then(() => {
                          toast.ok(p.archived_at ? t("projects.restored") : t("projects.archivedToast")); projects.reload();
                        }, toast.error)}>
                        {p.archived_at ? t("projects.restore") : t("projects.archive")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
      {edit && <ProjectModal project={edit === "new" ? null : edit} onClose={() => setEdit(null)} onSaved={projects.reload} />}
    </>
  );
}

function ProjectModal({ project, onClose, onSaved }: { project: Project | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const { t } = useT();
  const spaces = useAsync(() => api.spaces(), []);
  const [space, setSpace] = useState(project?.space ?? "");
  const [slug, setSlug] = useState(project?.slug ?? "");
  const [name, setName] = useState(project?.name ?? "");
  const [description, setDescription] = useState(project?.description ?? "");
  const [repos, setRepos] = useState(project?.repo_urls.join("\n") ?? "");
  const [error, setError] = useState<unknown>(null);

  const save = async () => {
    const repo_urls = repos.split(/\n+/).map((s) => s.trim()).filter(Boolean);
    try {
      if (project) await api.patchProject(project.ref, { name, description, repo_urls });
      else await api.createProject({ space, slug, name, description, repo_urls });
      toast.ok(t("common.saved"));
      onSaved();
      onClose();
    } catch (e) {
      setError(e);
    }
  };

  return (
    <Modal title={project ? t("projects.modalTitle", { ref: project.ref }) : t("projects.new")} onClose={onClose}
      footer={<><button className="btn" onClick={onClose}>{t("common.cancel")}</button><button className="btn primary" onClick={() => void save()}>{t("common.save")}</button></>}>
      <div className="stack">
        {!project && (
          <div className="form-grid">
            <label className="field"><span>{t("common.space")}</span>
              <select value={space} onChange={(e) => setSpace(e.target.value)}>
                <option value="">{t("common.choose")}</option>
                {spaces.data?.filter((s) => ["write", "review", "admin"].includes(s.level)).map((s) => <option key={s.slug}>{s.slug}</option>)}
              </select>
            </label>
            <label className="field"><span>Slug</span><input value={slug} placeholder="billing-service" onChange={(e) => setSlug(e.target.value)} /></label>
          </div>
        )}
        <label className="field"><span>{t("projects.nameLabel")}</span><input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <label className="field"><span>{t("common.description")}</span><input value={description} onChange={(e) => setDescription(e.target.value)} /></label>
        <label className="field"><span>{t("projects.remotes")}</span>
          <textarea rows={3} value={repos} placeholder="git@github.com:acme/billing.git" onChange={(e) => setRepos(e.target.value)} />
        </label>
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}
