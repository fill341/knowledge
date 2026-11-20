import { useMemo, useState } from "react";
import { api, type Level, type Principal, type Token } from "../api";
import { useAuth } from "../auth";
import { CopyButton, ErrorBox, fmtAgo, fmtDate, Modal, Spinner, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

const LEVELS: Level[] = ["none", "read", "write", "review", "admin"];

export function Access() {
  const toast = useToast();
  const { t } = useT();
  const { me } = useAuth();
  const principals = useAsync(() => api.principals(), []);
  const tokens = useAsync(() => api.tokens(), []);
  const [showRevoked, setShowRevoked] = useState(false);
  const [modal, setModal] = useState<null | "token" | "principal">(null);
  const [tokenFor, setTokenFor] = useState<string>("");

  const tokensByPrincipal = useMemo(() => {
    const m = new Map<string, number>();
    tokens.data?.filter((t) => !t.revoked_at).forEach((t) => m.set(t.principal, (m.get(t.principal) ?? 0) + 1));
    return m;
  }, [tokens.data]);

  const reload = () => {
    principals.reload();
    tokens.reload();
  };

  const visibleTokens = tokens.data?.filter((t) => showRevoked || !t.revoked_at) ?? [];

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.access")}</h1>
          <p>{t("access.subtitle")}</p>
        </div>
        <div className="row">
          <button className="btn" onClick={() => setModal("principal")}>{t("access.newPrincipal")}</button>
          <button className="btn primary" onClick={() => { setTokenFor(""); setModal("token"); }}>{t("access.issueToken")}</button>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        <div style={{ padding: "16px 16px 0" }}><h2>{t("access.principals")}</h2></div>
        <ErrorBox error={principals.error} />
        {principals.loading && !principals.data ? <Spinner /> : (
          <table>
            <thead><tr><th>{t("common.name")}</th><th>{t("common.type")}</th><th>{t("access.colActiveTokens")}</th><th>{t("common.created")}</th><th>{t("common.status")}</th><th /></tr></thead>
            <tbody>
              {principals.data?.map((p) => (
                <tr key={p.id} className={p.disabled_at ? "dim" : ""}>
                  <td><strong>{p.name}</strong>{p.description && <div className="small muted">{p.description}</div>}</td>
                  <td><span className="badge">{p.kind}</span></td>
                  <td>{tokensByPrincipal.get(p.name) ?? 0}</td>
                  <td className="small">{fmtDate(p.created_at, false)}</td>
                  <td>{p.disabled_at ? <span className="badge s-deprecated">{t("access.disabled")}</span> : <span className="badge s-active">{t("access.active")}</span>}</td>
                  <td className="nowrap">
                    <button className="btn sm" onClick={() => { setTokenFor(p.name); setModal("token"); }}>{t("access.addToken")}</button>{" "}
                    {p.name !== me?.principal.name && (
                      <button className={`btn sm ${p.disabled_at ? "" : "danger"}`} onClick={() => {
                        if (!p.disabled_at && !confirm(t("access.confirmDisable", { name: p.name }))) return;
                        void api.setPrincipalDisabled(p.name, !p.disabled_at).then(() => {
                          toast.ok(p.disabled_at ? t("access.enabledToast") : t("access.disabledToast")); reload();
                        }, toast.error);
                      }}>
                        {p.disabled_at ? t("access.enable") : t("access.disable")}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="card" style={{ padding: 0, marginTop: 16 }}>
        <div className="row between" style={{ padding: "16px 16px 0" }}>
          <h2>{t("access.tokens")}</h2>
          <label className="check small"><input type="checkbox" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} /> {t("access.showRevoked")}</label>
        </div>
        <ErrorBox error={tokens.error} />
        {tokens.loading && !tokens.data ? <Spinner /> : (
          <table>
            <thead><tr><th>{t("access.colPrefix")}</th><th>Principal</th><th>{t("access.colScopes")}</th><th>{t("access.colLastUsed")}</th><th>{t("access.colExpires")}</th><th /></tr></thead>
            <tbody>
              {visibleTokens.map((tk) => (
                <tr key={tk.id} className={tk.revoked_at ? "dim" : ""}>
                  <td><code>kn_{tk.prefix}_…</code><div className="small muted">{tk.name}</div></td>
                  <td>{tk.principal}</td>
                  <td><Scopes scopes={tk.scopes} /></td>
                  <td className="small">{fmtAgo(tk.last_used_at)}</td>
                  <td className="small">{tk.expires_at ? fmtDate(tk.expires_at, false) : t("common.never")}</td>
                  <td className="nowrap">
                    {tk.revoked_at ? <span className="badge s-archived">{t("access.revokedAgo", { ago: fmtAgo(tk.revoked_at) })}</span> : (
                      <button className="btn danger sm" onClick={() => {
                        const own = tk.principal === me?.principal.name;
                        const msg = own
                          ? t("access.confirmRevokeOwn", { principal: tk.principal })
                          : t("access.confirmRevoke", { prefix: tk.prefix, principal: tk.principal });
                        if (!confirm(msg)) return;
                        void api.revokeToken(tk.id).then(() => { toast.ok(t("access.revokedToast")); tokens.reload(); }, toast.error);
                      }}>{t("access.revoke")}</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {modal === "token" && (
        <TokenModal principals={principals.data ?? []} initialPrincipal={tokenFor} onClose={() => setModal(null)} onCreated={reload} />
      )}
      {modal === "principal" && <PrincipalModal onClose={() => setModal(null)} onCreated={reload} />}
    </>
  );
}

function Scopes({ scopes }: { scopes: string[] }) {
  const { t } = useT();
  return (
    <div className="row">
      {scopes.map((s) => {
        if (s === "admin") return <span key={s} className="badge pin">{t("access.adminAll")}</span>;
        const [, space, level] = s.split(":");
        return <span key={s} className="badge">{space}: {(LEVELS as string[]).includes(level) ? t(`level.${level as Level}`) : level}</span>;
      })}
    </div>
  );
}

function PrincipalModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const toast = useToast();
  const { t, rich } = useT();
  const [kind, setKind] = useState("agent");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<unknown>(null);
  return (
    <Modal title={t("access.newPrincipal")} onClose={onClose}
      footer={<><button className="btn" onClick={onClose}>{t("common.cancel")}</button>
        <button className="btn primary" onClick={() => void api.createPrincipal({ kind, name, description }).then(() => {
          toast.ok(t("common.created")); onCreated(); onClose();
        }, setError)}>{t("common.create")}</button></>}>
      <div className="stack">
        <div className="form-grid">
          <label className="field"><span>{t("common.type")}</span>
            <select value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="agent">{t("access.kindAgent")}</option>
              <option value="user">{t("access.kindUser")}</option>
              <option value="system">{t("access.kindSystem")}</option>
            </select>
          </label>
          <label className="field"><span>{t("common.name")}</span><input value={name} placeholder="codex@work" onChange={(e) => setName(e.target.value)} /></label>
        </div>
        <label className="field"><span>{t("common.description")}</span><input value={description} onChange={(e) => setDescription(e.target.value)} /></label>
        <div className="small muted">{rich("access.convention", { pattern: <code>{t("access.pattern")}</code>, example: <code>cursor@personal</code> })}</div>
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}

function TokenModal({ principals, initialPrincipal, onClose, onCreated }: {
  principals: Principal[]; initialPrincipal: string; onClose: () => void; onCreated: () => void;
}) {
  const { t, rich } = useT();
  const spaces = useAsync(() => api.spaces(), []);
  const [principal, setPrincipal] = useState(initialPrincipal);
  const [newName, setNewName] = useState("");
  const [name, setName] = useState("");
  const [days, setDays] = useState("");
  const [globalAdmin, setGlobalAdmin] = useState(false);
  const [levels, setLevels] = useState<Record<string, Level>>({});
  const [error, setError] = useState<unknown>(null);
  const [issued, setIssued] = useState<Token | null>(null);

  const creatingNew = principal === "__new__";
  const target = creatingNew ? newName.trim().toLowerCase() : principal;
  const kind = creatingNew ? "agent" : principals.find((p) => p.name === principal)?.kind ?? "agent";
  const isAgent = kind === "agent";
  const allowed: Level[] = isAgent ? ["none", "read", "write"] : LEVELS;

  const scopes = [
    ...(globalAdmin && !isAgent ? ["admin"] : []),
    ...Object.entries(levels).filter(([, l]) => l !== "none" && allowed.includes(l)).map(([s, l]) => `space:${s}:${l}`),
  ];

  const presets: { label: string; apply: () => void }[] = [
    { label: t("access.presetPersonal"), apply: () => setLevels({ personal: "write", shared: "read", work: "none" }) },
    { label: t("access.presetWork"), apply: () => setLevels({ personal: "none", shared: "read", work: "write" }) },
    { label: t("access.presetReadAll"), apply: () => setLevels({ personal: "read", shared: "read", work: "read" }) },
  ];

  const submit = async () => {
    setError(null);
    try {
      const t = await api.createToken({
        principal: target, name: name || undefined, scopes,
        expires_in_days: days ? Number(days) : undefined, create_principal: creatingNew,
      });
      setIssued(t);
      onCreated();
    } catch (e) {
      setError(e);
    }
  };

  if (issued) {
    const keychain = `security add-generic-password -U -s kn-token -a '${issued.principal}' -w '${issued.token}'`;
    const [tool, profile] = issued.principal.split("@");
    return (
      <Modal title={t("access.tokenIssued")} onClose={onClose} footer={<button className="btn primary" onClick={onClose}>{t("common.done")}</button>}>
        <div className="stack">
          <div className="alert">{rich("access.shownOnce", { once: <strong>{t("access.once")}</strong> })}</div>
          <div className="secret-box">{issued.token}</div>
          <div className="row"><CopyButton text={issued.token!} label={t("access.copyToken")} /></div>
          <hr />
          <div className="small muted">{rich("access.keychain", { kn: <code>kn</code>, launch: <code>kn-mcp-launch</code> })}</div>
          <div className="secret-box">{keychain}</div>
          <div className="row"><CopyButton text={keychain} label={t("access.copyCommand")} /></div>
          {isAgent && profile && (
            <>
              <div className="small muted">{t("access.mcpLaunch")}</div>
              <div className="secret-box">scripts/kn-mcp-launch {tool} {profile}</div>
            </>
          )}
        </div>
      </Modal>
    );
  }

  return (
    <Modal title={t("access.issueToken")} onClose={onClose} wide
      footer={<><button className="btn" onClick={onClose}>{t("common.cancel")}</button>
        <button className="btn primary" disabled={!target || scopes.length === 0} onClick={() => void submit()}>{t("access.issue")}</button></>}>
      <div className="stack">
        <div className="form-grid">
          <label className="field"><span>Principal</span>
            <select value={principal} onChange={(e) => setPrincipal(e.target.value)}>
              <option value="">{t("common.choose")}</option>
              {principals.filter((p) => !p.disabled_at).map((p) => <option key={p.name} value={p.name}>{p.name} ({p.kind})</option>)}
              <option value="__new__">{t("access.newAgent")}</option>
            </select>
          </label>
          {creatingNew && (
            <label className="field"><span>{t("access.newAgentName")}</span><input value={newName} placeholder="codex@work" onChange={(e) => setNewName(e.target.value)} /></label>
          )}
          <label className="field"><span>{t("access.tokenName")}</span><input value={name} placeholder={t("access.tokenNamePh")} onChange={(e) => setName(e.target.value)} /></label>
          <label className="field"><span>{t("access.days")}</span><input type="number" min={1} value={days} onChange={(e) => setDays(e.target.value)} /></label>
        </div>

        <div>
          <div className="row between">
            <h3 style={{ margin: 0 }}>{t("access.scopesBySpace")}</h3>
            <div className="row">
              {presets.map((p) => <button key={p.label} className="btn ghost sm" onClick={p.apply}>{p.label}</button>)}
            </div>
          </div>
          {isAgent && <div className="small muted" style={{ margin: "6px 0" }}>{t("access.agentLimits")}</div>}
          <table className="scope-matrix">
            <thead><tr><th>{t("common.space")}</th>{LEVELS.map((l) => <th key={l}>{t(`level.${l}`)}</th>)}</tr></thead>
            <tbody>
              {spaces.data?.map((s) => (
                <tr key={s.slug}>
                  <td><strong>{s.slug}</strong></td>
                  {LEVELS.map((l) => (
                    <td key={l}>
                      <input type="radio" name={`lvl-${s.slug}`} disabled={!allowed.includes(l)}
                        checked={(levels[s.slug] ?? "none") === l} onChange={() => setLevels((x) => ({ ...x, [s.slug]: l }))} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          {!isAgent && (
            <label className="check" style={{ marginTop: 8 }}>
              <input type="checkbox" checked={globalAdmin} onChange={(e) => setGlobalAdmin(e.target.checked)} />
              {t("access.globalAdmin")}
            </label>
          )}
          <div className="small muted" style={{ marginTop: 8 }}>
            {t("access.result")} {scopes.length ? scopes.map((s) => <code key={s} style={{ marginRight: 6 }}>{s}</code>) : t("access.noRights")}
          </div>
          {levels.shared === "write" && isAgent && (
            <div className="alert" style={{ marginTop: 8 }}>
              {rich("access.sharedWarn", { shared: <code>shared</code> })}
            </div>
          )}
        </div>
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}
