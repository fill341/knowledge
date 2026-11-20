import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError, type Status } from "../api";
import { formatDate, hasMsg, translate, useT, type TFunc } from "../i18n";

// statusLabel localizes an entry status; unknown values are shown as is.
export function statusLabel(status: Status | string, t: TFunc = translate) {
  const key = `status.${status}`;
  return hasMsg(key) ? t(key) : status;
}

export function StatusBadge({ status }: { status: Status }) {
  const { t } = useT();
  return <span className={`badge s-${status}`}>{statusLabel(status, t)}</span>;
}

export function KindBadge({ kind }: { kind: string }) {
  return <span className="badge kind">{kind}</span>;
}

export function Tags({ tags }: { tags: string[] }) {
  if (!tags?.length) return null;
  return (
    <>
      {tags.map((t) => (
        <span key={t} className="badge tag">#{t}</span>
      ))}
    </>
  );
}

// Date helpers follow the active UI language (see i18n.tsx); components re-render on language change via useT().
export function fmtDate(s: string | null | undefined, withTime = true) {
  if (!s) return "—";
  return formatDate(s, withTime ? { dateStyle: "medium", timeStyle: "short" } : { dateStyle: "medium" });
}

export function fmtAgo(s: string | null | undefined) {
  if (!s) return translate("common.never");
  const sec = (Date.now() - new Date(s).getTime()) / 1000;
  if (sec < 60) return translate("time.justNow");
  if (sec < 3600) return translate("time.minAgo", { n: Math.floor(sec / 60) });
  if (sec < 86400) return translate("time.hAgo", { n: Math.floor(sec / 3600) });
  if (sec < 86400 * 30) return translate("time.dAgo", { n: Math.floor(sec / 86400) });
  return fmtDate(s, false);
}

// Search highlights come from ts_headline with **…** markers; render them as <mark> without innerHTML.
export function Highlight({ text }: { text: string }) {
  const parts = text.split("**");
  return (
    <>
      {parts.map((p, i) => (i % 2 === 1 ? <mark key={i}>{p}</mark> : <span key={i}>{p}</span>))}
    </>
  );
}

export function errorText(e: unknown) {
  if (e instanceof ApiError) return `${e.message}${e.code && e.code !== "error" ? ` (${e.code})` : ""}`;
  return e instanceof Error ? e.message : String(e);
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return <div className="alert error">{errorText(error)}</div>;
}

export function Spinner() {
  const { t } = useT();
  return <div className="empty">{t("common.loading")}</div>;
}

// useAsync loads data and exposes reload; it ignores results of superseded calls.
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [state, setState] = useState<{ data?: T; error?: unknown; loading: boolean }>({ loading: true });
  const seq = useRef(0);
  const run = useCallback(() => {
    const n = ++seq.current;
    setState((s) => ({ ...s, loading: true }));
    fn().then(
      (data) => n === seq.current && setState({ data, loading: false }),
      (error) => n === seq.current && setState({ error, loading: false }),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(run, [run]);
  return { ...state, reload: run, setData: (data: T) => setState({ data, loading: false }) };
}

export function Modal({ title, onClose, children, footer, wide }: {
  title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean;
}) {
  const { t } = useT();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? "wide" : ""}`} role="dialog" aria-label={title}>
        <div className="modal-head">
          <h2 style={{ margin: 0 }}>{title}</h2>
          <button className="btn ghost sm" onClick={onClose} aria-label={t("common.close")}>✕</button>
        </div>
        {children}
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}

// ── Toasts ─────────────────────────────────────────────────────────────────

interface Toast { id: number; text: string; error?: boolean }
const ToastCtx = createContext<(text: string, error?: boolean) => void>(() => {});

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const push = useCallback((text: string, error?: boolean) => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t, { id, text, error }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), error ? 7000 : 3500);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.error ? "error" : ""}`}>{t.text}</div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

export function useToast() {
  const push = useContext(ToastCtx);
  return {
    ok: (text: string) => push(text),
    error: (e: unknown) => push(errorText(e), true),
  };
}

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useT();
  const [done, setDone] = useState(false);
  return (
    <button
      className="btn sm"
      onClick={() => {
        void navigator.clipboard.writeText(text).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        });
      }}
    >
      {done ? t("common.copied") : label ?? t("common.copy")}
    </button>
  );
}
