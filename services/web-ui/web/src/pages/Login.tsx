import { useState } from "react";
import { useAuth } from "../auth";
import { Credits } from "../components/Credits";
import { CopyButton, errorText } from "../components/ui";
import { useT } from "../i18n";
import { PrefsControls } from "../prefs";

const UI_CMD = "kn ui";

export function Login() {
  const { login } = useAuth();
  const { t, rich } = useT();
  const [token, setToken] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const linkExpired = new URLSearchParams(window.location.search).get("login") === "expired";

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(token);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login">
      <PrefsControls className="login-prefs" />
      <form className="card stack" onSubmit={submit}>
        <div className="brand" style={{ padding: 0 }}>
          <div className="brand-logo">kn</div>
          Knowledge
        </div>
        <div>
          <h2 style={{ marginBottom: 4 }}>{t("login.title")}</h2>
          <div className="muted small">{t("login.hint")}</div>
        </div>
        {linkExpired && (
          <div className="alert error">{t("login.expired")}</div>
        )}
        <div className="row">
          <code className="secret-box grow" style={{ flex: 1 }}>{UI_CMD}</code>
          <CopyButton text={UI_CMD} />
        </div>
        <hr style={{ margin: "4px 0" }} />
        <div className="small muted">
          {rich("login.tokenHint", { me: <code>me</code> })}
        </div>
        <input
          type="password"
          placeholder="kn_…"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          autoComplete="off"
        />
        {!!error && <div className="alert error">{errorText(error)}</div>}
        <button className="btn primary" disabled={busy || !token.trim()}>{t("login.submit")}</button>
      </form>
      <Credits className="login-credits" />
    </div>
  );
}
