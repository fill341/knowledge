// 3D knowledge graph for the overview page: projects are glowing hubs, entries orbit them,
// explicit links glow with moving particles, optional tag nodes bridge projects.
// Loaded lazily (three.js is heavy) — see Dashboard.
import ForceGraph3D, { type ForceGraph3DInstance } from "3d-force-graph";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import * as THREE from "three";
import SpriteText from "three-spritetext";
import { api, type EntryDetail, type GraphData, type Status } from "../api";
import { useT, type TFunc } from "../i18n";
import { fmtAgo, KindBadge, StatusBadge, Tags } from "./ui";

type NodeType = "project" | "space" | "entry" | "tag";

interface GNode {
  id: string;
  type: NodeType;
  label: string;
  color: string;
  val: number;
  kind?: string;
  status?: Status;
  project?: string | null;
  space?: string;
  pinned?: boolean;
  tags?: string[];
  updated_at?: string;
  count?: number;
  x?: number;
  y?: number;
  z?: number;
}

interface GLink {
  source: string | GNode;
  target: string | GNode;
  type: "member" | "link" | "tag";
  rel?: string;
}

export const KIND_COLORS: Record<string, string> = {
  decision: "#818cf8",
  lesson: "#fbbf24",
  fact: "#2dd4bf",
  rule: "#f87171",
  note: "#94a3b8",
  document: "#60a5fa",
  snippet: "#4ade80",
  session_log: "#64748b",
};
const PROJECT_COLORS = ["#f472b6", "#38bdf8", "#facc15", "#34d399", "#fb923c", "#a78bfa", "#f87171", "#22d3ee", "#e879f9", "#a3e635"];
const REL_COLORS: Record<string, string> = {
  depends_on: "#fbbf24", supersedes: "#f87171", derived_from: "#a78bfa",
  contradicts: "#fb7185", references: "#38bdf8", relates_to: "#e2e8f0",
};
const BG = "#070a12";
const DIM = "rgba(120,130,150,0.12)";
const kindColor = (k?: string) => KIND_COLORS[k ?? ""] ?? "#cbd5e1";
const nodeId = (x: string | GNode) => (typeof x === "string" ? x : x.id);
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

interface Options {
  tags: boolean;
  kinds: Set<string>;   // empty = all
  projects: Set<string>; // empty = all
  onlyProposed: boolean;
}

function buildGraph(data: GraphData, o: Options, t: TFunc): { nodes: GNode[]; links: GLink[] } {
  const nodes: GNode[] = [];
  const links: GLink[] = [];
  const projectColor = new Map<string, string>();
  data.projects.forEach((p, i) => projectColor.set(p.ref, PROJECT_COLORS[i % PROJECT_COLORS.length]));

  const entries = data.nodes.filter((n) =>
    (o.kinds.size === 0 || o.kinds.has(n.kind)) &&
    (o.projects.size === 0 || o.projects.has(n.project ?? `space:${n.space}`)) &&
    (!o.onlyProposed || n.status === "proposed"));
  const ids = new Set(entries.map((e) => e.id));

  const degree = new Map<string, number>();
  const explicit = data.links.filter((l) => ids.has(l.from) && ids.has(l.to));
  explicit.forEach((l) => {
    degree.set(l.from, (degree.get(l.from) ?? 0) + 1);
    degree.set(l.to, (degree.get(l.to) ?? 0) + 1);
  });

  // Hubs: one per project, one per space for entries without a project.
  const hubCount = new Map<string, number>();
  entries.forEach((e) => {
    const hub = e.project ? `p:${e.project}` : `s:${e.space}`;
    hubCount.set(hub, (hubCount.get(hub) ?? 0) + 1);
  });
  for (const [hub, count] of hubCount) {
    const isProject = hub.startsWith("p:");
    const ref = hub.slice(2);
    const project = data.projects.find((p) => p.ref === ref);
    nodes.push({
      id: hub, type: isProject ? "project" : "space", count,
      label: isProject ? (project?.name ?? ref) : t("graph.noProject", { space: ref }),
      color: isProject ? (projectColor.get(ref) ?? "#e2e8f0") : "#94a3b8",
      val: 10 + Math.sqrt(count) * 3, project: isProject ? ref : null, space: isProject ? ref.split("/")[0] : ref,
    });
  }

  entries.forEach((e) => {
    nodes.push({
      id: e.id, type: "entry", label: e.title, kind: e.kind, status: e.status, project: e.project, space: e.space,
      pinned: e.pinned, tags: e.tags, updated_at: e.updated_at, color: kindColor(e.kind),
      val: 1 + (degree.get(e.id) ?? 0) * 0.8 + (e.pinned ? 3 : 0) + (e.kind === "decision" || e.kind === "rule" ? 1 : 0),
    });
    links.push({ source: e.id, target: e.project ? `p:${e.project}` : `s:${e.space}`, type: "member" });
  });

  explicit.forEach((l) => links.push({ source: l.from, target: l.to, type: "link", rel: l.rel }));

  if (o.tags) {
    const byTag = new Map<string, string[]>();
    entries.forEach((e) => e.tags.forEach((t) => byTag.set(t, [...(byTag.get(t) ?? []), e.id])));
    for (const [tag, members] of byTag) {
      if (members.length < 2) continue; // a tag on one entry connects nothing
      nodes.push({ id: `t:${tag}`, type: "tag", label: `#${tag}`, color: "#cbd5e1", val: 0.6 + Math.sqrt(members.length) * 0.4, count: members.length });
      members.forEach((m) => links.push({ source: m, target: `t:${tag}`, type: "tag" }));
    }
  }
  return { nodes, links };
}

function hubObject(n: GNode): THREE.Object3D {
  const group = new THREE.Group();
  const r = Math.cbrt(n.val) * 2.2;
  const core = new THREE.Mesh(
    new THREE.SphereGeometry(r, 32, 32),
    new THREE.MeshStandardMaterial({ color: n.color, emissive: n.color, emissiveIntensity: 0.55, roughness: 0.35 }),
  );
  const halo = new THREE.Mesh(
    new THREE.SphereGeometry(r * 1.7, 32, 32),
    new THREE.MeshBasicMaterial({ color: n.color, transparent: true, opacity: 0.07, depthWrite: false }),
  );
  // Labels scale with the cluster and are drawn on top of the node cloud.
  const label = new SpriteText(`${n.label}  ·  ${n.count}`, 6 + Math.sqrt(n.count ?? 1) * 0.3, n.color);
  label.material.depthTest = false;
  label.renderOrder = 10;
  label.fontWeight = "600";
  label.backgroundColor = "rgba(7,10,18,0.55)";
  label.padding = 2;
  label.borderRadius = 3;
  label.position.y = r * 1.7 + 8;
  group.add(core, halo, label);
  return group;
}

export default function KnowledgeGraph() {
  const navigate = useNavigate();
  const { t } = useT();
  // The renderer binds its tooltip callback once; it reads the latest translator through this ref.
  const tRef = useRef(t);
  tRef.current = t;
  const box = useRef<HTMLDivElement>(null);
  const fg = useRef<ForceGraph3DInstance<GNode, GLink> | null>(null);
  const [data, setData] = useState<GraphData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [opts, setOpts] = useState<Options>({ tags: false, kinds: new Set(), projects: new Set(), onlyProposed: false });
  const [selected, setSelected] = useState<GNode | null>(null);
  const [detail, setDetail] = useState<EntryDetail | null>(null);
  const [query, setQuery] = useState("");
  const [full, setFull] = useState(false);
  const highlight = useRef<{ nodes: Set<string>; links: Set<GLink>; focus: string | null }>({ nodes: new Set(), links: new Set(), focus: null });
  const fitted = useRef(false);
  // The renderer binds callbacks once; they call the latest handlers through this ref.
  const handlers = useRef<{ select: (n: GNode | null) => void; hover: (n: GNode | null) => void }>({ select: () => {}, hover: () => {} });

  useEffect(() => {
    api.graph().then(setData, (e) => setError(String(e?.message ?? e)));
  }, []);

  const graph = useMemo(() => (data ? buildGraph(data, opts, t) : null), [data, opts, t]);

  // Neighbor index for hover/selection highlighting.
  const neighbors = useMemo(() => {
    const m = new Map<string, { nodes: Set<string>; links: Set<GLink> }>();
    graph?.links.forEach((l) => {
      const a = nodeId(l.source), b = nodeId(l.target);
      for (const [x, y] of [[a, b], [b, a]]) {
        if (!m.has(x)) m.set(x, { nodes: new Set(), links: new Set() });
        m.get(x)!.nodes.add(y);
        m.get(x)!.links.add(l);
      }
    });
    return m;
  }, [graph]);

  const setFocus = (n: GNode | null) => {
    const h = highlight.current;
    h.nodes = new Set(n ? [n.id, ...(neighbors.get(n.id)?.nodes ?? [])] : []);
    h.links = new Set(n ? neighbors.get(n.id)?.links ?? [] : []);
    h.focus = n?.id ?? null;
    const g = fg.current;
    if (g) g.nodeColor(g.nodeColor()).nodeVal(g.nodeVal()).linkColor(g.linkColor()).linkWidth(g.linkWidth());
  };

  const flyTo = (n: GNode) => {
    const g = fg.current;
    if (!g || n.x === undefined) return;
    // Hubs: back off proportionally to the cluster size; entries: close-up.
    const dist = n.type === "project" || n.type === "space" ? 90 + Math.sqrt(n.count ?? 1) * 9 : 110;
    const ratio = 1 + dist / Math.hypot(n.x || 1, n.y || 1, n.z || 1);
    g.cameraPosition({ x: (n.x || 1) * ratio, y: (n.y || 1) * ratio, z: (n.z || 1) * ratio }, { x: n.x!, y: n.y!, z: n.z! }, 1200);
  };

  const select = (n: GNode | null) => {
    fitted.current = true; // the user took over the camera: no auto zoom-to-fit later
    setSelected(n);
    setDetail(null);
    setFocus(n);
    if (n) {
      flyTo(n);
      if (n.type === "entry") api.entry(n.id).then(setDetail, () => undefined);
    }
  };

  // Create the renderer once.
  useEffect(() => {
    if (!box.current || fg.current) return;
    const g = new ForceGraph3D(box.current, { controlType: "orbit" }) as unknown as ForceGraph3DInstance<GNode, GLink>;
    fg.current = g;
    g.backgroundColor(BG)
      .showNavInfo(false)
      .nodeRelSize(4)
      .nodeOpacity(0.92)
      .nodeResolution(12)
      .nodeVal((n) => (n.type === "entry" && highlight.current.focus === n.id ? n.val * 6 + 6 : n.val)) // selected entry stands out
      .nodeLabel((n) => {
        const tr = tRef.current;
        const entries = tr("count.entries", { count: n.count ?? 0 });
        const meta = n.type === "entry" ? `${n.kind} · ${n.project ?? n.space} · ${n.status}` : n.type === "tag" ? `${tr("graph.tag")} · ${entries}` : entries;
        return `<div class="graph-tip"><strong>${esc(n.label)}</strong><span>${esc(meta)}</span></div>`;
      })
      .nodeThreeObject((n) => (n.type === "project" || n.type === "space" ? hubObject(n) : (null as unknown as THREE.Object3D)))
      .linkDirectionalArrowLength((l) => (l.type === "link" ? 3.5 : 0))
      .linkDirectionalArrowRelPos(1)
      .linkDirectionalParticles((l) => (l.type === "link" ? 3 : 0))
      .linkDirectionalParticleWidth(1.6)
      .linkDirectionalParticleSpeed(0.006)
      .linkCurvature((l) => (l.type === "link" ? 0.15 : 0))
      .cooldownTime(7000)
      .enableNodeDrag(true)
      .onNodeHover((n) => {
        if (box.current) box.current.style.cursor = n ? "pointer" : "grab";
        handlers.current.hover(n);
      })
      .onNodeClick((n) => handlers.current.select(n))
      .onBackgroundClick(() => handlers.current.select(null))
      .onEngineStop(() => {
        if (!fitted.current) {
          fitted.current = true;
          g.zoomToFit(900, 40);
        }
      });
    // Limited-range repulsion keeps small project clusters next to big ones instead of flying away.
    g.d3Force("charge")?.strength(-40).distanceMax(240);
    g.d3Force("link")?.distance((l: GLink) => (l.type === "member" ? 28 : l.type === "tag" ? 55 : 45));

    const ro = new ResizeObserver(([e]) => g.width(e.contentRect.width).height(e.contentRect.height));
    ro.observe(box.current);
    return () => {
      ro.disconnect();
      g._destructor();
      fg.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Hover highlighting without changing the selection.
  const setFocusHover = (n: GNode | null) => {
    const h = highlight.current;
    if (h.focus) return; // a selected node keeps its highlight
    h.nodes = new Set(n ? [n.id, ...(neighbors.get(n.id)?.nodes ?? [])] : []);
    h.links = new Set(n ? neighbors.get(n.id)?.links ?? [] : []);
    const g = fg.current;
    if (g) g.nodeColor(g.nodeColor()).linkColor(g.linkColor()).linkWidth(g.linkWidth());
  };

  // Color/width accessors read the highlight ref, so they are re-bound when neighbors change.
  useEffect(() => {
    const g = fg.current;
    if (!g) return;
    const h = highlight.current;
    g.nodeColor((n) => (h.nodes.size && !h.nodes.has(n.id) ? DIM : n.color))
      .linkColor((l) => {
        if (h.links.size && !h.links.has(l)) return "rgba(120,130,150,0.04)";
        if (l.type === "link") return REL_COLORS[l.rel ?? ""] ?? "#e2e8f0";
        if (l.type === "tag") return h.links.has(l) ? "rgba(203,213,225,0.5)" : "rgba(203,213,225,0.10)";
        return h.links.has(l) ? "rgba(255,255,255,0.45)" : "rgba(255,255,255,0.07)";
      })
      .linkWidth((l) => (l.type === "link" ? (h.links.has(l) ? 2.2 : 1.2) : h.links.has(l) ? 0.8 : 0));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [neighbors]);

  useEffect(() => {
    if (fg.current && graph) {
      fitted.current = false;
      fg.current.graphData(graph);
    }
  }, [graph]);

  handlers.current = { select, hover: setFocusHover };

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q || !graph) return [];
    return graph.nodes.filter((n) => n.label.toLowerCase().includes(q)).slice(0, 8);
  }, [query, graph]);

  const toggle = (key: "kinds" | "projects", value: string) =>
    setOpts((o) => {
      const s = new Set(o[key]);
      if (s.has(value)) s.delete(value);
      else s.add(value);
      return { ...o, [key]: s };
    });

  const kindCounts = useMemo(() => {
    const m = new Map<string, number>();
    data?.nodes.forEach((n) => m.set(n.kind, (m.get(n.kind) ?? 0) + 1));
    return [...m].sort((a, b) => b[1] - a[1]);
  }, [data]);

  const projectChips = useMemo(() => {
    if (!data) return [];
    const counts = new Map<string, number>();
    data.nodes.forEach((n) => {
      const k = n.project ?? `space:${n.space}`;
      counts.set(k, (counts.get(k) ?? 0) + 1);
    });
    return [...counts].sort((a, b) => b[1] - a[1]).map(([k, c]) => ({
      key: k, count: c,
      label: k.startsWith("space:") ? t("graph.noProject", { space: k.slice(6) }) : data.projects.find((p) => p.ref === k)?.name ?? k,
      color: k.startsWith("space:") ? "#94a3b8" : PROJECT_COLORS[data.projects.findIndex((p) => p.ref === k) % PROJECT_COLORS.length],
    }));
  }, [data, t]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && full && setFull(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [full]);

  const projectStats = useMemo(() => {
    if (!selected || (selected.type !== "project" && selected.type !== "space") || !data) return null;
    const inHub = data.nodes.filter((n) => (selected.type === "project" ? n.project === selected.project : !n.project && n.space === selected.space));
    const byKind = new Map<string, number>();
    inHub.forEach((n) => byKind.set(n.kind, (byKind.get(n.kind) ?? 0) + 1));
    const proposed = inHub.filter((n) => n.status === "proposed").length;
    const recent = [...inHub].sort((a, b) => b.updated_at.localeCompare(a.updated_at)).slice(0, 6);
    return { byKind: [...byKind].sort((a, b) => b[1] - a[1]), proposed, recent };
  }, [selected, data]);

  return (
    <div className={`graph-wrap ${full ? "full" : ""}`}>
      <div ref={box} className="graph-canvas" />

      <div className="graph-toolbar">
        <div className="graph-search">
          <input placeholder={t("graph.searchPh")} value={query} onChange={(e) => setQuery(e.target.value)} />
          {matches.length > 0 && (
            <div className="graph-matches">
              {matches.map((n) => (
                <button key={n.id} onClick={() => { setQuery(""); select(n); }}>
                  <span className="dot" style={{ background: n.color }} />
                  <span className="grow">{n.label}</span>
                  <span className="graph-muted">{n.type === "entry" ? n.kind : n.type === "tag" ? t("graph.tag") : t("graph.project")}</span>
                </button>
              ))}
            </div>
          )}
        </div>
        <label className="graph-toggle"><input type="checkbox" checked={opts.tags} onChange={(e) => setOpts((o) => ({ ...o, tags: e.target.checked }))} /> {t("graph.tagsAsNodes")}</label>
        <label className="graph-toggle"><input type="checkbox" checked={opts.onlyProposed} onChange={(e) => setOpts((o) => ({ ...o, onlyProposed: e.target.checked }))} /> {t("graph.onlyProposed")}</label>
        <button className="graph-btn" onClick={() => { select(null); fg.current?.zoomToFit(800, 40); }}>{t("graph.showAll")}</button>
        <button className="graph-btn" onClick={() => setFull((f) => !f)}>{full ? t("graph.exitFull") : t("graph.full")}</button>
      </div>

      <div className="graph-legend">
        <div className="graph-legend-row">
          {projectChips.map((p) => (
            <button key={p.key} className={`graph-chip ${opts.projects.size && !opts.projects.has(p.key) ? "off" : ""}`} onClick={() => toggle("projects", p.key)}>
              <span className="dot" style={{ background: p.color }} />{p.label} <span className="graph-muted">{p.count}</span>
            </button>
          ))}
        </div>
        <div className="graph-legend-row">
          {kindCounts.map(([k, c]) => (
            <button key={k} className={`graph-chip ${opts.kinds.size && !opts.kinds.has(k) ? "off" : ""}`} onClick={() => toggle("kinds", k)}>
              <span className="dot" style={{ background: kindColor(k) }} />{k} <span className="graph-muted">{c}</span>
            </button>
          ))}
          {(opts.kinds.size > 0 || opts.projects.size > 0) && (
            <button className="graph-chip" onClick={() => setOpts((o) => ({ ...o, kinds: new Set(), projects: new Set() }))}>{t("graph.resetFilters")}</button>
          )}
        </div>
      </div>

      {!data && !error && <div className="graph-overlay">{t("graph.building")}</div>}
      {error && <div className="graph-overlay">{t("graph.loadError", { error })}</div>}
      {data?.truncated && <div className="graph-note">{t("graph.truncated")}</div>}
      <div className="graph-hint">{t("graph.hint")}</div>

      {selected && (
        <div className="graph-panel">
          <button className="graph-close" onClick={() => select(null)} aria-label={t("common.close")}>✕</button>
          {selected.type === "entry" && (
            <>
              <div className="graph-panel-title">{selected.label}</div>
              <div className="row small" style={{ margin: "8px 0" }}>
                <KindBadge kind={selected.kind!} />
                {selected.status && <StatusBadge status={selected.status} />}
                {selected.pinned && <span className="badge pin">{t("common.pinned")}</span>}
              </div>
              <div className="graph-muted small">{selected.project ?? selected.space} · {fmtAgo(selected.updated_at)}</div>
              {detail?.summary && <div className="graph-summary">{detail.summary}</div>}
              {!detail?.summary && detail?.body && <div className="graph-summary">{detail.body.slice(0, 280)}{detail.body.length > 280 ? "…" : ""}</div>}
              {detail && <div className="row small" style={{ marginTop: 8 }}><Tags tags={detail.tags} /></div>}
              {detail && detail.links.outgoing.length + detail.links.incoming.length > 0 && (
                <div className="graph-links">
                  {detail.links.outgoing.map((l) => (
                    <button key={`o${l.to_id}${l.rel}`} onClick={() => { const n = graph?.nodes.find((x) => x.id === l.to_id); if (n) select(n); }}>
                      → <span style={{ color: REL_COLORS[l.rel] }}>{l.rel}</span> {l.title}
                    </button>
                  ))}
                  {detail.links.incoming.map((l) => (
                    <button key={`i${l.from_id}${l.rel}`} onClick={() => { const n = graph?.nodes.find((x) => x.id === l.from_id); if (n) select(n); }}>
                      ← <span style={{ color: REL_COLORS[l.rel] }}>{l.rel}</span> {l.title}
                    </button>
                  ))}
                </div>
              )}
              <div className="row" style={{ marginTop: 12 }}>
                <button className="btn primary sm" onClick={() => navigate(`/entries/${selected.id}`)}>{t("graph.openEntry")}</button>
                {selected.project && (
                  <button className="graph-btn" onClick={() => { const hub = graph?.nodes.find((x) => x.id === `p:${selected.project}`); if (hub) select(hub); }}>{t("graph.toProject")}</button>
                )}
              </div>
            </>
          )}
          {(selected.type === "project" || selected.type === "space") && projectStats && (
            <>
              <div className="graph-panel-title" style={{ color: selected.color }}>{selected.label}</div>
              <div className="graph-muted small" style={{ margin: "4px 0 10px" }}>
                {t("count.entries", { count: selected.count ?? 0 })}{projectStats.proposed ? ` · ${t("graph.proposedCount", { count: projectStats.proposed })}` : ""}
              </div>
              <div className="graph-bars">
                {projectStats.byKind.map(([k, c]) => (
                  <div key={k} className="graph-bar-row">
                    <span className="dot" style={{ background: kindColor(k) }} />
                    <span className="grow">{k}</span>
                    <span className="graph-bar"><span style={{ width: `${(c / (selected.count || 1)) * 100}%`, background: kindColor(k) }} /></span>
                    <span className="graph-muted">{c}</span>
                  </div>
                ))}
              </div>
              <div className="graph-muted small" style={{ margin: "12px 0 4px" }}>{t("graph.recent")}</div>
              <div className="graph-links">
                {projectStats.recent.map((n) => (
                  <button key={n.id} onClick={() => { const x = graph?.nodes.find((g) => g.id === n.id); if (x) select(x); }}>
                    <span className="dot" style={{ background: kindColor(n.kind) }} /> {n.title}
                  </button>
                ))}
              </div>
              <div className="row" style={{ marginTop: 12 }}>
                <Link className="btn primary sm" to={selected.type === "project" ? `/entries?project=${selected.project}` : `/entries?space=${selected.space}`}>{t("graph.allEntries")}</Link>
                {selected.type === "project" && (
                  <button className="graph-btn" onClick={() => { setOpts((o) => ({ ...o, projects: new Set([selected.project!]) })); }}>{t("graph.onlyThisProject")}</button>
                )}
              </div>
            </>
          )}
          {selected.type === "tag" && (
            <>
              <div className="graph-panel-title">{selected.label}</div>
              <div className="graph-muted small" style={{ margin: "4px 0 10px" }}>{t("graph.tag")} · {t("count.entries", { count: selected.count ?? 0 })}</div>
              <Link className="btn primary sm" to={`/entries?tag=${selected.label.slice(1)}`}>{t("graph.tagEntries")}</Link>
            </>
          )}
        </div>
      )}
    </div>
  );
}
