import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, type ReviewItem, type SearchItem } from "../api";
import { Attributes, Markdown } from "../components/content";
import { notifyChanged } from "../components/Layout";
import { ErrorBox, fmtAgo, fmtDate, KindBadge, Spinner, StatusBadge, Tags, useAsync, useToast } from "../components/ui";
import { useT } from "../i18n";

type Action = "approve" | "reject" | "request_changes";

export function Review() {
  const toast = useToast();
  const { t } = useT();
  const queue = useAsync(() => api.reviewQueue(), []);
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focus, setFocus] = useState(0);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (queue.data) setItems(queue.data);
  }, [queue.data]);

  const act = useCallback(async (ids: string[], action: Action, comment?: string) => {
    setBusy(true);
    const done: string[] = [];
    for (const id of ids) {
      try {
        await api.review(id, action, comment);
        done.push(id);
      } catch (e) {
        toast.error(e);
      }
    }
    setItems((list) => list.filter((e) => !done.includes(e.id)));
    setSelected((s) => new Set([...s].filter((id) => !done.includes(id))));
    if (done.length) {
      toast.ok(`${t(`review.done.${action}`)}: ${done.length}`);
      notifyChanged();
    }
    setBusy(false);
  }, [toast, t]);

  // Keyboard: j/k — move, a — approve, r — reject, x — select.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (busy || t.tagName === "INPUT" || t.tagName === "TEXTAREA" || e.metaKey || e.ctrlKey) return;
      const cur = items[focus];
      if (e.key === "j") setFocus((f) => Math.min(f + 1, items.length - 1));
      else if (e.key === "k") setFocus((f) => Math.max(f - 1, 0));
      else if (e.key === "a" && cur) void act([cur.id], "approve");
      else if (e.key === "r" && cur) void act([cur.id], "reject");
      else if (e.key === "x" && cur)
        setSelected((s) => {
          const n = new Set(s);
          if (n.has(cur.id)) n.delete(cur.id);
          else n.add(cur.id);
          return n;
        });
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [items, focus, busy, act]);

  useEffect(() => {
    if (focus >= items.length) setFocus(Math.max(items.length - 1, 0));
    document.getElementById(`review-${items[focus]?.id}`)?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [focus, items]);

  if (queue.loading && !queue.data) return <Spinner />;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{t("nav.review")}</h1>
          <p>
            {t("review.subtitle")}{" "}
            <kbd>j</kbd>/<kbd>k</kbd> — {t("review.keyMove")}, <kbd>a</kbd> — {t("review.keyApprove")}, <kbd>r</kbd> — {t("review.keyReject")}, <kbd>x</kbd> — {t("review.keySelect")}.
          </p>
        </div>
        {items.length > 0 && (
          <div className="row">
            <label className="check">
              <input
                type="checkbox"
                checked={selected.size === items.length}
                onChange={(e) => setSelected(e.target.checked ? new Set(items.map((i) => i.id)) : new Set())}
              />
              {t("review.selectAll")}
            </label>
            <button className="btn ok" disabled={!selected.size || busy} onClick={() => void act([...selected], "approve")}>
              {t("review.approveSelected", { n: selected.size })}
            </button>
            <button className="btn danger" disabled={!selected.size || busy} onClick={() => void act([...selected], "reject")}>
              {t("common.reject")}
            </button>
          </div>
        )}
      </div>
      <ErrorBox error={queue.error} />
      {items.length === 0 && !queue.error && (
        <div className="card empty">{t("review.empty")}</div>
      )}
      <div className="stack">
        {items.map((e, i) => (
          <ReviewCard
            key={e.id}
            entry={e}
            focused={i === focus}
            selected={selected.has(e.id)}
            busy={busy}
            onFocus={() => setFocus(i)}
            onSelect={(on) =>
              setSelected((s) => {
                const n = new Set(s);
                if (on) n.add(e.id);
                else n.delete(e.id);
                return n;
              })
            }
            onAction={(action, comment) => void act([e.id], action, comment)}
          />
        ))}
      </div>
    </>
  );
}

function ReviewCard({ entry: e, focused, selected, busy, onFocus, onSelect, onAction }: {
  entry: ReviewItem; focused: boolean; selected: boolean; busy: boolean;
  onFocus: () => void; onSelect: (on: boolean) => void; onAction: (a: Action, comment?: string) => void;
}) {
  const { t, rich } = useT();
  const [expanded, setExpanded] = useState(false);
  const [comment, setComment] = useState("");
  const [similar, setSimilar] = useState<SearchItem[] | null>(null);
  const long = e.body.length > 900;
  const dueReview = e.status === "active";

  useEffect(() => {
    let alive = true;
    api.search({ q: e.title, limit: 4, status: ["active", "proposed", "deprecated"] }).then(
      (r) => alive && setSimilar(r.items.filter((x) => x.id !== e.id).slice(0, 3)),
      () => alive && setSimilar([]),
    );
    return () => {
      alive = false;
    };
  }, [e.id, e.title]);

  const attrs = useMemo(() => e.attributes || {}, [e.attributes]);

  return (
    <div id={`review-${e.id}`} className={`card review-card ${focused ? "focused" : ""}`} onClick={onFocus}>
      <div className="row" style={{ alignItems: "flex-start" }}>
        <input type="checkbox" checked={selected} onChange={(ev) => onSelect(ev.target.checked)} style={{ marginTop: 5 }} />
        <div className="grow" style={{ flex: 1 }}>
          <div className="row">
            <Link to={`/entries/${e.id}`} style={{ fontSize: 16, fontWeight: 650, color: "var(--text)" }}>{e.title}</Link>
          </div>
          <div className="row small muted" style={{ marginTop: 6 }}>
            <KindBadge kind={e.kind} />
            <StatusBadge status={e.status} />
            {dueReview && <span className="badge s-proposed">{t("review.due", { date: fmtDate(e.review_after, false) })}</span>}
            <span>{e.project ?? e.space}</span>
            <span>{rich("review.author", { name: <strong>{e.created_by}</strong> })}</span>
            <span title={fmtDate(e.created_at)}>· {fmtAgo(e.created_at)}</span>
            {e.source_ref && <span>{rich("review.source", { src: <code>{e.source_ref}</code> })}</span>}
            <Tags tags={e.tags} />
          </div>
        </div>
      </div>

      {!!e.duplicate_of?.length && (
        <div className="alert error" style={{ marginTop: 12 }}>
          <strong>{t("review.dupTitle")}</strong>{" "}
          {e.duplicate_of.map((d, i) => (
            <span key={d.id}>{i > 0 && ", "}<Link to={`/entries/${d.id}`}>{d.title}</Link> <span className="small muted">({d.status})</span></span>
          ))}
          <div className="small">{t("review.dupHint")}</div>
        </div>
      )}

      <div style={{ marginTop: 14 }}>
        {e.summary && <div className="summary">{e.summary}</div>}
        <Attributes attrs={attrs} body={e.body} />
        <div className={long && !expanded ? "clamp" : ""}>
          <Markdown text={e.body} />
        </div>
        {long && (
          <button className="btn ghost sm" onClick={() => setExpanded((x) => !x)}>
            {expanded ? t("common.collapse") : t("review.showFull")}
          </button>
        )}
      </div>

      {similar && similar.length > 0 && (
        <div className="alert" style={{ marginTop: 12 }}>
          <div className="small" style={{ fontWeight: 600, marginBottom: 4 }}>{t("review.similar")}</div>
          {similar.map((s) => (
            <div key={s.id} className="small row">
              <Link to={`/entries/${s.id}`}>{s.title}</Link>
              <StatusBadge status={s.status} />
              <span className="muted">{s.project ?? s.space}</span>
            </div>
          ))}
        </div>
      )}

      <div className="review-actions">
        <button className="btn ok" disabled={busy} onClick={() => onAction("approve", comment || undefined)}>
          {dueReview ? t("review.confirmCurrent") : t("common.approve")}
        </button>
        {!dueReview && (
          <>
            <button className="btn" disabled={busy || !comment.trim()} title={t("review.requestChangesTitle")}
              onClick={() => onAction("request_changes", comment)}>
              {t("review.requestChanges")}
            </button>
            <button className="btn danger" disabled={busy} onClick={() => onAction("reject", comment || undefined)}>
              {t("common.reject")}
            </button>
          </>
        )}
        <input
          className="grow"
          style={{ flex: 1, minWidth: 200 }}
          placeholder={t("review.commentPh")}
          value={comment}
          onChange={(ev) => setComment(ev.target.value)}
        />
        <Link to={`/entries/${e.id}?edit=1`} className="btn ghost">{t("common.editEntry")}</Link>
      </div>
    </div>
  );
}
