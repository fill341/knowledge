// "Galaxy" view of the knowledge base: every project is a star system, entries are planets. Mass is the volume
// of knowledge and links are gravity — explicit links pull hard, implicit ones (entries about the same thing)
// weakly; strongly attracted entries orbit as bound groups lit by a nebula, free planets follow Kepler. The
// physics lives in galaxyModel.ts; this file renders it with three.js and bloom (loaded lazily).
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { EffectComposer } from "three/examples/jsm/postprocessing/EffectComposer.js";
import { OutputPass } from "three/examples/jsm/postprocessing/OutputPass.js";
import { RenderPass } from "three/examples/jsm/postprocessing/RenderPass.js";
import { UnrealBloomPass } from "three/examples/jsm/postprocessing/UnrealBloomPass.js";
import { api, type EntryDetail, type GraphData, type GraphNode } from "../api";
import { activityPower, activityTimeline, ACTIVE_HOURS, buildWorld, kindColor, ORBIT_ORDER, placeGroup, placePlanet, placeSystem, potentialAt, rand, REL_COLORS, STAR_COLORS, type World } from "./galaxyModel";
import "./galaxyPlayer.css";
import { formatDate, formatNumber, translate, useT } from "../i18n";
import { fmtAgo, KindBadge, statusLabel, StatusBadge, Tags, useToast } from "./ui";

// Links between projects: one muted colour, dim and without sparks, so the galaxy does not turn into one tangle;
// they brighten only when their star or planet is selected.
const CROSS_COLOR = "#4f5f82";
const crossDim = (selected: boolean, touches: boolean) => (!selected ? 1 : touches ? 3 : 0.25);

// Gravity well under the galaxy: a lattice of WELL_N × WELL_N points bent by the potential of the systems.
const WELL_N = 72;

const fmtWeight = (x: number) => formatNumber(x, { maximumFractionDigits: x >= 100 ? 0 : 1 });
// Brightness of a planet: knowledge agents actually use glows, unused knowledge is dimmer, and knowledge past
// its re-check date fades to grey. heat = log(1 + used) / log(1 + most used).
function planetColor(n: GraphNode, heat: number) {
  const c = new THREE.Color(kindColor(n.kind));
  if (n.review_due) c.lerp(new THREE.Color("#6b7280"), 0.7).multiplyScalar(0.55);
  return c.multiplyScalar((n.status === "proposed" ? 1.6 : 1) * (0.7 + 0.9 * heat));
}

// Volume of knowledge for people: characters → "12K chars" / "12 тыс. знаков".
const fmtMass = (m: number) => translate("galaxy.chars", { n: formatNumber(m, { notation: "compact", maximumFractionDigits: 1 }) });

// ── Textures ─────────────────────────────────────────────────────────────────

function glowTexture(inner = "rgba(255,255,255,1)", mid = "rgba(255,255,255,0.25)") {
  const c = document.createElement("canvas");
  c.width = c.height = 128;
  const g = c.getContext("2d")!;
  const grd = g.createRadialGradient(64, 64, 0, 64, 64, 64);
  grd.addColorStop(0, inner);
  grd.addColorStop(0.25, mid);
  grd.addColorStop(1, "rgba(255,255,255,0)");
  g.fillStyle = grd;
  g.fillRect(0, 0, 128, 128);
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  return t;
}

function ringTexture() {
  const c = document.createElement("canvas");
  c.width = c.height = 128;
  const g = c.getContext("2d")!;
  g.strokeStyle = "white";
  g.lineWidth = 6;
  g.beginPath();
  g.arc(64, 64, 52, 0, Math.PI * 2);
  g.stroke();
  return new THREE.CanvasTexture(c);
}

// ── Activity halo ────────────────────────────────────────────────────────────

// A project developed in the last day wears a soft halo: a ring of light around its star that grows and brightens
// with activity (activityPower) and breathes slowly. No activity for a day — no halo. Deliberately calm: bloom
// amplifies anything that flashes, so the halo changes over seconds, never per frame.
function haloTexture() {
  const c = document.createElement("canvas");
  c.width = c.height = 256;
  const g = c.getContext("2d")!;
  const grd = g.createRadialGradient(128, 128, 0, 128, 128, 128);
  grd.addColorStop(0, "rgba(255,255,255,0)");
  grd.addColorStop(0.42, "rgba(255,255,255,0.05)");
  grd.addColorStop(0.62, "rgba(255,255,255,0.55)");
  grd.addColorStop(0.72, "rgba(255,255,255,0.25)");
  grd.addColorStop(1, "rgba(255,255,255,0)");
  g.fillStyle = grd;
  g.fillRect(0, 0, 256, 256);
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  return t;
}

interface Halo { sprite: THREE.Sprite; power: number; phase: number }

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

type Sel = { type: "planet"; i: number } | { type: "sys"; i: number } | null;

// ── Component ────────────────────────────────────────────────────────────────

export default function Galaxy() {
  const navigate = useNavigate();
  const { t, lang } = useT();
  const box = useRef<HTMLDivElement>(null);
  const tip = useRef<HTMLDivElement>(null);
  const labelsBox = useRef<HTMLDivElement>(null);
  const [data, setData] = useState<GraphData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState({ projects: new Set<string>(), kinds: new Set<string>(), onlyProposed: false, superseded: false });
  const [paused, setPaused] = useState(() => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false);
  const [showLinks, setShowLinks] = useState(true);
  const [showAffinity, setShowAffinity] = useState(true);
  const [showWell, setShowWell] = useState(true);
  const [sel, setSel] = useState<Sel>(null);
  const [detail, setDetail] = useState<EntryDetail | null>(null);
  const [query, setQuery] = useState("");
  const [full, setFull] = useState(false);
  // Evolution player: null = live (now); otherwise the moment of history shown, in ms.
  const [playAt, setPlayAt] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(1);

  const world = useMemo(() => (data ? buildWorld(data, filter, (space) => translate("graph.noProject", { space })) : null), [data, filter, lang]);

  // Everything the render loop reads lives in a ref, so the loop never restarts.
  const live = useRef({ world: null as World | null, paused, showLinks, showAffinity, showWell, sel: null as Sel, select: (_: Sel) => {},
    playAt: null as number | null, playing: false, playRate: 0, onPlayEnd: () => {} });
  live.current.world = world;
  live.current.paused = paused;
  live.current.showLinks = showLinks;
  live.current.showAffinity = showAffinity;
  live.current.showWell = showWell;
  live.current.sel = sel;
  live.current.playing = playing;

  const engine = useRef<{
    camera: THREE.PerspectiveCamera;
    controls: OrbitControls;
    rebuild: (w: World) => void;
    flyTo: (s: Sel, overview?: boolean) => void;
  } | null>(null);

  useEffect(() => {
    api.graph().then(setData, (e) => setError(String(e?.message ?? e)));
  }, []);

  // ── Scene setup (once) ─────────────────────────────────────────────────────
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const renderer = new THREE.WebGLRenderer({ antialias: true, powerPreference: "high-performance" });
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    renderer.setSize(el.clientWidth, el.clientHeight);
    renderer.toneMapping = THREE.ACESFilmicToneMapping;
    renderer.toneMappingExposure = 1.05;
    renderer.outputColorSpace = THREE.SRGBColorSpace;
    el.appendChild(renderer.domElement);

    const scene = new THREE.Scene();
    scene.background = new THREE.Color("#03040a");
    const camera = new THREE.PerspectiveCamera(55, el.clientWidth / el.clientHeight, 0.5, 20000);
    camera.position.set(0, 220, 420);
    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = true;
    controls.dampingFactor = 0.08;
    controls.minDistance = 6;
    controls.maxDistance = 4000;

    // Post-processing renders into its own target, where the canvas' antialias does not apply: without MSAA here
    // the thin, slowly moving lines of the gravity well and links crawl and flicker.
    const composer = new EffectComposer(renderer, new THREE.WebGLRenderTarget(el.clientWidth, el.clientHeight,
      { type: THREE.HalfFloatType, samples: Math.min(4, renderer.capabilities.maxSamples) }));
    composer.setSize(el.clientWidth, el.clientHeight); // applies the pixel ratio to the custom target
    composer.addPass(new RenderPass(scene, camera));
    const bloom = new UnrealBloomPass(new THREE.Vector2(el.clientWidth, el.clientHeight), 0.8, 0.5, 0.32);
    composer.addPass(bloom);
    composer.addPass(new OutputPass());

    scene.add(new THREE.AmbientLight("#9aa8c8", 1.1));

    // Starfield and nebulae: deliberately dim so labels and planets stay readable.
    const glow = glowTexture();
    const starGeo = new THREE.BufferGeometry();
    const starPos: number[] = [];
    const starCol: number[] = [];
    for (let i = 0; i < 5000; i++) {
      const v = new THREE.Vector3().randomDirection().multiplyScalar(2500 + Math.random() * 4000);
      starPos.push(v.x, v.y, v.z);
      const c = new THREE.Color().setHSL(0.58 + Math.random() * 0.1, 0.3, 0.45 + Math.random() * 0.25);
      starCol.push(c.r, c.g, c.b);
    }
    starGeo.setAttribute("position", new THREE.Float32BufferAttribute(starPos, 3));
    starGeo.setAttribute("color", new THREE.Float32BufferAttribute(starCol, 3));
    scene.add(new THREE.Points(starGeo, new THREE.PointsMaterial({ size: 1, sizeAttenuation: false, vertexColors: true, transparent: true, opacity: 0.22, depthWrite: false })));
    ["#3b1d6e", "#0e3a5c", "#5c1d4a", "#123d3a", "#2a2370"].forEach((c, i) => {
      const s = new THREE.Sprite(new THREE.SpriteMaterial({ map: glow, color: c, transparent: true, opacity: 0.12, blending: THREE.AdditiveBlending, depthWrite: false }));
      const dir = new THREE.Vector3(Math.cos(i * 1.3), i % 2 ? 0.3 : -0.25, Math.sin(i * 1.3)).normalize();
      s.position.copy(dir.multiplyScalar(3200));
      s.scale.setScalar(2600 + i * 300);
      scene.add(s);
    });

    // World objects, rebuilt when the data or filters change.
    let group = new THREE.Group();
    scene.add(group);
    let planetMesh: THREE.InstancedMesh | null = null;
    let sysGroups: THREE.Group[] = []; // star, corona, light and kind rings of each system: they orbit the core together
    let sysCore: { core: THREE.Mesh; corona: THREE.Sprite; coronaScale: number }[] = [];
    let halos: (Halo | null)[] = [];
    const haloTex = haloTexture();
    let labelCounts: number[] = [];
    let well: { lines: THREE.LineSegments; xs: Float32Array; zs: Float32Array; y0: number; depth: number; unit: number } | null = null;
    let nebulae: THREE.Sprite[] = [];
    let linkObjs: { line: THREE.Line; sparks: THREE.Sprite[]; mat: THREE.LineBasicMaterial; rel: string; cross: boolean }[] = [];
    // Implicit links: one buffer of segments, faint, coloured per vertex (additive, so darker means fainter).
    let affinity: { lines: THREE.LineSegments; base: Float32Array; rest: Float32Array; cross: Uint8Array } | null = null;
    const halo = new THREE.Sprite(new THREE.SpriteMaterial({ map: ringTexture(), color: "#ffffff", transparent: true, blending: THREE.AdditiveBlending, depthTest: false }));
    halo.visible = false;
    halo.renderOrder = 20;
    scene.add(halo);
    const sparkTex = glowTexture("rgba(255,255,255,1)", "rgba(255,255,255,0.5)");

    const disposeGroup = (g: THREE.Object3D) =>
      g.traverse((o) => {
        const m = o as THREE.Mesh;
        m.geometry?.dispose?.();
        const mat = m.material as THREE.Material | THREE.Material[] | undefined;
        (Array.isArray(mat) ? mat : mat ? [mat] : []).forEach((x) => x.dispose());
      });

    // Star names are HTML: crisp at any distance and untouched by bloom.
    let labelEls: HTMLDivElement[] = [];
    const rebuild = (w: World) => {
      const lb = labelsBox.current;
      if (lb) {
        lb.replaceChildren();
        labelEls = w.systems.map((s) => {
          const d = document.createElement("div");
          d.className = "galaxy-label";
          d.style.color = s.colorHex;
          d.style.fontSize = `${Math.round(11 + Math.min(6, Math.cbrt(s.count)))}px`;
          d.innerHTML = `${esc(s.label)} <span>${s.count}</span>`;
          d.dataset.label = s.label;
          d.title = fmtMass(s.mass);
          lb.appendChild(d);
          return d;
        });
      }
      scene.remove(group);
      disposeGroup(group);
      group = new THREE.Group();
      scene.add(group);
      linkObjs = [];
      affinity = null;
      sysCore = [];
      labelCounts = w.systems.map((s) => s.count);

      sysGroups = w.systems.map((s) => {
        const sg = new THREE.Group();
        sg.position.copy(s.pos);
        const core = new THREE.Mesh(
          new THREE.SphereGeometry(s.sunR, 48, 48),
          new THREE.MeshBasicMaterial({ color: s.color.clone().multiplyScalar(1.8), toneMapped: false }),
        );
        const corona = new THREE.Sprite(new THREE.SpriteMaterial({ map: glow, color: s.color, transparent: true, opacity: 0.6, blending: THREE.AdditiveBlending, depthWrite: false }));
        corona.scale.setScalar(s.sunR * 5);
        const light = new THREE.PointLight(s.color, 4, s.radius * 3, 0);
        sg.add(core, corona, light);
        sysCore.push({ core, corona, coronaScale: s.sunR * 5 });
        group.add(sg);
        return sg;
      });
      halos = w.systems.map((s, i) => {
        if (!s.project) return null;
        const sprite = new THREE.Sprite(new THREE.SpriteMaterial({ map: haloTex, color: s.color, transparent: true, opacity: 0, blending: THREE.AdditiveBlending, depthWrite: false }));
        sprite.visible = false;
        sysGroups[i].add(sprite);
        return { sprite, power: 0, phase: rand(s.key, 11) * Math.PI * 2 };
      });

      w.rings.forEach((rg) => {
        const pts: THREE.Vector3[] = [];
        for (let i = 0; i <= 128; i++) {
          const a = (i / 128) * Math.PI * 2;
          pts.push(new THREE.Vector3(Math.cos(a) * rg.r, 0, Math.sin(a) * rg.r).applyQuaternion(w.systems[rg.sys].tilt));
        }
        const line = new THREE.Line(new THREE.BufferGeometry().setFromPoints(pts),
          new THREE.LineBasicMaterial({ color: rg.color, transparent: true, opacity: 0.08, depthWrite: false }));
        sysGroups[rg.sys].add(line);
      });

      // Gravity well: horizontal and vertical lattice segments; heights and brightness are set every frame.
      {
        const E = w.extent * 1.15;
        const xs = new Float32Array(WELL_N), zs = new Float32Array(WELL_N);
        for (let i = 0; i < WELL_N; i++) xs[i] = zs[i] = -E + (2 * E * i) / (WELL_N - 1);
        const segs = 2 * WELL_N * (WELL_N - 1);
        const geo = new THREE.BufferGeometry();
        geo.setAttribute("position", new THREE.Float32BufferAttribute(new Float32Array(segs * 6), 3));
        geo.setAttribute("color", new THREE.Float32BufferAttribute(new Float32Array(segs * 6), 3));
        const lines = new THREE.LineSegments(geo, new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, opacity: 0.6, blending: THREE.AdditiveBlending, depthWrite: false }));
        lines.frustumCulled = false;
        group.add(lines);
        const coreS = w.systems.reduce((a, b) => (b.weight > a.weight ? b : a), w.systems[0]);
        well = { lines, xs, zs, y0: -(w.extent * 0.1 + 40), depth: w.extent * 0.22, unit: coreS ? potentialAt(w, coreS.pos.x, coreS.pos.z) : 1 };
      }

      // Nebulae light up bound groups of three or more entries.
      nebulae = w.groups.map((c) => {
        const sp = new THREE.Sprite(new THREE.SpriteMaterial({ map: glow, color: c.color, transparent: true, opacity: 0.12 + Math.min(c.size, 12) * 0.008, blending: THREE.AdditiveBlending, depthWrite: false }));
        sp.scale.setScalar(c.spread * 2.6 + 6);
        sp.visible = c.size >= 3;
        group.add(sp);
        return sp;
      });

      const sphere = new THREE.SphereGeometry(1, 16, 16);
      const mat = new THREE.MeshStandardMaterial({ roughness: 0.55, metalness: 0.1, emissive: new THREE.Color("#000000") });
      planetMesh = new THREE.InstancedMesh(sphere, mat, Math.max(1, w.planets.length));
      planetMesh.count = w.planets.length;
      const hot = Math.log1p(Math.max(1, ...w.planets.map((p) => p.node.used ?? 0)));
      w.planets.forEach((p, i) => planetMesh!.setColorAt(i, planetColor(p.node, Math.log1p(p.node.used ?? 0) / hot)));
      if (planetMesh.instanceColor) planetMesh.instanceColor.needsUpdate = true;
      planetMesh.frustumCulled = false;
      group.add(planetMesh);

      if (w.affinity.length) {
        const geo = new THREE.BufferGeometry();
        geo.setAttribute("position", new THREE.Float32BufferAttribute(new Float32Array(w.affinity.length * 6), 3));
        geo.setAttribute("color", new THREE.Float32BufferAttribute(new Float32Array(w.affinity.length * 6), 3));
        const base = new Float32Array(w.affinity.length * 6);
        const rest = new Float32Array(w.affinity.length);
        const cross = new Uint8Array(w.affinity.length);
        const ca = new THREE.Color(), cb = new THREE.Color(), cc = new THREE.Color(CROSS_COLOR);
        w.affinity.forEach((a, k) => {
          const pa = w.planets[a.from], pb = w.planets[a.to];
          cross[k] = pa.sys !== pb.sys ? 1 : 0;
          if (cross[k]) {
            ca.copy(cc).multiplyScalar(0.3);
            cb.copy(ca);
          } else {
            const glowK = 0.35 + Math.min(a.score, 0.6);
            ca.set(kindColor(pa.node.kind)).lerp(cc.set("#9fb6ff"), 0.5).multiplyScalar(glowK);
            cb.set(kindColor(pb.node.kind)).lerp(cc, 0.5).multiplyScalar(glowK);
            cc.set(CROSS_COLOR);
          }
          base.set([ca.r, ca.g, ca.b, cb.r, cb.g, cb.b], k * 6);
          rest[k] = cross[k] ? 0 : Math.hypot(pa.x - pb.x, pa.z - pb.z); // length when laid out: stretching fades it
        });
        const lines = new THREE.LineSegments(geo, new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, opacity: 0.55, blending: THREE.AdditiveBlending, depthWrite: false }));
        lines.frustumCulled = false;
        group.add(lines);
        affinity = { lines, base, rest, cross };
      }

      w.links.forEach((l) => {
        const cross = w.planets[l.from].sys !== w.planets[l.to].sys;
        const lmat = new THREE.LineBasicMaterial({ color: cross ? CROSS_COLOR : REL_COLORS[l.rel] ?? "#e8eefc", transparent: true, opacity: 0.6, blending: THREE.AdditiveBlending, depthWrite: false });
        const geo = new THREE.BufferGeometry();
        geo.setAttribute("position", new THREE.Float32BufferAttribute(new Float32Array(41 * 3), 3));
        const line = new THREE.Line(geo, lmat);
        line.frustumCulled = false;
        const sparks = (cross ? [] : [0, 1, 2]).map(() => {
          const sp = new THREE.Sprite(new THREE.SpriteMaterial({ map: sparkTex, color: REL_COLORS[l.rel] ?? "#ffffff", transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }));
          sp.scale.setScalar(2.2);
          group.add(sp);
          return sp;
        });
        group.add(line);
        linkObjs.push({ line, sparks, mat: lmat, rel: l.rel, cross });
      });
    };

    // Camera tweening; follows a moving planet after arrival.
    let tween: { from: THREE.Vector3; fromT: THREE.Vector3; to: () => { pos: THREE.Vector3; target: THREE.Vector3 }; t0: number; dur: number } | null = null;
    let follow: (() => THREE.Vector3) | null = null;
    let lastFollow = new THREE.Vector3();

    const flyTo = (s: Sel, overview = false) => {
      const w = live.current.world;
      if (!w) return;
      let to: () => { pos: THREE.Vector3; target: THREE.Vector3 };
      follow = null;
      if (overview || !s) {
        const e = w.extent;
        to = () => ({ pos: new THREE.Vector3(0, e * 0.55, e * 1.1), target: new THREE.Vector3(0, 0, 0) });
      } else if (s.type === "sys") {
        const sys = w.systems[s.i];
        to = () => ({ pos: sys.pos.clone().add(new THREE.Vector3(0, sys.radius * 0.95, sys.radius * 1.7)), target: sys.pos.clone() });
        follow = () => sys.pos; // the system orbits the galactic core
      } else {
        const p = w.planets[s.i];
        const sys = w.systems[p.sys];
        to = () => {
          const out = p.pos.clone().sub(sys.pos).setY(0).normalize();
          const d = 30 + p.size * 8;
          return { pos: p.pos.clone().add(out.multiplyScalar(d)).add(new THREE.Vector3(0, d * 0.55, 0)), target: p.pos.clone() };
        };
        follow = () => p.pos;
      }
      tween = { from: camera.position.clone(), fromT: controls.target.clone(), to, t0: performance.now(), dur: 1600 };
    };

    engine.current = { camera, controls, rebuild, flyTo };

    // Picking in screen space: robust for tiny planets.
    const ndc = new THREE.Vector3();
    const pick = (cx: number, cy: number): Sel => {
      const w = live.current.world;
      if (!w) return null;
      const rect = el.getBoundingClientRect();
      const x = cx - rect.left, y = cy - rect.top;
      let best: Sel = null;
      let bestD = Infinity;
      const proj = (v: THREE.Vector3) => {
        ndc.copy(v).project(camera);
        if (ndc.z > 1) return null;
        return { sx: (ndc.x * 0.5 + 0.5) * rect.width, sy: (-ndc.y * 0.5 + 0.5) * rect.height, depth: camera.position.distanceTo(v) };
      };
      const pxPerUnit = (depth: number) => rect.height / (2 * Math.tan((camera.fov * Math.PI) / 360) * depth);
      w.systems.forEach((s, i) => {
        const p = proj(s.pos);
        if (!p) return;
        const d = Math.hypot(p.sx - x, p.sy - y);
        const rad = Math.max(10, s.sunR * pxPerUnit(p.depth) * 1.4);
        if (d < rad && d - rad < bestD) { bestD = d - rad; best = { type: "sys", i }; }
      });
      w.planets.forEach((pl, i) => {
        const p = proj(pl.pos);
        if (!p) return;
        const d = Math.hypot(p.sx - x, p.sy - y);
        const rad = Math.max(7, pl.size * pxPerUnit(p.depth) * 1.3);
        if (d < rad && d - rad < bestD) { bestD = d - rad; best = { type: "planet", i }; }
      });
      return best;
    };

    let down: { x: number; y: number } | null = null;
    const onDown = (e: PointerEvent) => { down = { x: e.clientX, y: e.clientY }; };
    const onUp = (e: PointerEvent) => {
      if (down && Math.hypot(e.clientX - down.x, e.clientY - down.y) < 5) live.current.select(pick(e.clientX, e.clientY));
      down = null;
    };
    let hoverFrame = 0;
    const onMove = (e: PointerEvent) => {
      if (hoverFrame) return;
      hoverFrame = requestAnimationFrame(() => {
        hoverFrame = 0;
        const t = tip.current;
        const w = live.current.world;
        if (!t || !w) return;
        const h = pick(e.clientX, e.clientY);
        el.style.cursor = h ? "pointer" : "grab";
        if (!h) { t.style.display = "none"; return; }
        const rect = el.getBoundingClientRect();
        if (h.type === "sys") {
          const s = w.systems[h.i];
          t.innerHTML = `<strong>${esc(s.label)}</strong><span>${esc(translate("galaxy.tipStar", { entries: translate("count.entries", { count: s.count }), mass: fmtMass(s.mass), weight: fmtWeight(s.weight) }))}</span>`;
        } else {
          const p = w.planets[h.i];
          const n = p.node;
          t.innerHTML = `<strong>${esc(n.title)}</strong><span>${esc(`${n.kind} · ${n.project ?? n.space} · ${statusLabel(n.status)} · ${fmtMass(p.mass)} · ${n.used ? translate("galaxy.usedTimes", { count: n.used }) : translate("galaxy.notUsed")}${n.review_due ? ` · ${translate("galaxy.reviewDue")}` : ""}`)}</span>`;
        }
        t.style.display = "flex";
        t.style.left = `${e.clientX - rect.left + 14}px`;
        t.style.top = `${e.clientY - rect.top + 14}px`;
      });
    };
    const onLeave = () => { if (tip.current) tip.current.style.display = "none"; };
    renderer.domElement.addEventListener("pointerdown", onDown);
    renderer.domElement.addEventListener("pointerup", onUp);
    renderer.domElement.addEventListener("pointermove", onMove);
    renderer.domElement.addEventListener("pointerleave", onLeave);

    const ro = new ResizeObserver(() => {
      const w = el.clientWidth, h = el.clientHeight;
      camera.aspect = w / h;
      camera.updateProjectionMatrix();
      renderer.setSize(w, h);
      composer.setSize(w, h);
      bloom.resolution.set(w, h);
    });
    ro.observe(el);

    // ── Render loop ──────────────────────────────────────────────────────────
    const m4 = new THREE.Matrix4();
    const q = new THREE.Quaternion();
    const sc = new THREE.Vector3();
    const curve = new THREE.QuadraticBezierCurve3(new THREE.Vector3(), new THREE.Vector3(), new THREE.Vector3());
    const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
    const clock = new THREE.Clock();
    let simTime = 0;
    let raf = 0;
    const frame = () => {
      raf = requestAnimationFrame(frame);
      const dt = Math.min(clock.getDelta(), 0.1);
      const L = live.current;
      const w = L.world;
      if (!L.paused) simTime += dt;
      // The moment shown: now, or the evolution player's position, which advances while it plays.
      if (L.playAt !== null && L.playing && w) {
        L.playAt += dt * L.playRate;
        if (L.playAt >= Date.now()) { L.playAt = null; L.onPlayEnd(); }
      }
      const at = L.playAt ?? Date.now();
      const replay = L.playAt !== null;
      if (w && planetMesh) {
        // Systems orbit the galactic core; their stars and rings ride along.
        // Growth during replay: a system appears with its first entry and its star swells with its knowledge.
        const bornMass = new Float64Array(w.systems.length);
        const bornCount = new Int32Array(w.systems.length);
        const grow = new Float32Array(w.planets.length);
        const popIn = Math.max((Date.now() - w.start) * 0.015, 3600_000);
        w.planets.forEach((p, i) => {
          const g = !replay || p.born === 0 ? 1 : Math.max(0, Math.min(1, (at - p.born) / popIn));
          grow[i] = g;
          if (g > 0) { bornMass[p.sys] += p.mass; bornCount[p.sys]++; }
        });
        w.systems.forEach((s, i) => {
          placeSystem(w, i, simTime, s.pos);
          const sg = sysGroups[i];
          if (!sg) return;
          sg.position.copy(s.pos);
          sg.visible = bornCount[i] > 0;
          const k = replay ? 0.35 + 0.65 * Math.cbrt(bornMass[i] / s.mass) : 1;
          sysCore[i]?.core.scale.setScalar(k);
          sysCore[i]?.corona.scale.setScalar(sysCore[i].coronaScale * k);
          const lbl = labelEls[i];
          if (lbl && labelCounts[i] !== bornCount[i]) {
            labelCounts[i] = bornCount[i];
            lbl.innerHTML = `${esc(lbl.dataset.label ?? "")} <span>${bornCount[i]}</span>`;
          }
          const h = halos[i];
          if (h) {
            const target = activityPower(w, s.project, at);
            h.power += (target - h.power) * Math.min(1, dt * (replay ? 6 : 1.5));
            h.sprite.visible = h.power > 0.02 && sg.visible;
            if (h.sprite.visible) {
              const breath = 1 + 0.05 * Math.sin(clock.elapsedTime * 0.8 + h.phase);
              h.sprite.scale.setScalar(s.sunR * (5 + 5 * h.power) * k * breath);
              (h.sprite.material as THREE.SpriteMaterial).opacity = 0.18 + 0.4 * h.power;
            }
          }
        });
        if (well) {
          well.lines.visible = L.showWell;
          if (L.showWell) {
            const { xs, zs, y0, depth, unit } = well;
            const pos = well.lines.geometry.getAttribute("position") as THREE.BufferAttribute;
            const col = well.lines.geometry.getAttribute("color") as THREE.BufferAttribute;
            const u = new Float32Array(WELL_N * WELL_N);
            for (let i = 0; i < WELL_N; i++) for (let j = 0; j < WELL_N; j++) u[i * WELL_N + j] = potentialAt(w, xs[j], zs[i]) / unit;
            let k = 0;
            const put = (i: number, j: number) => {
              const v = u[i * WELL_N + j];
              const b = 0.05 + 0.3 * Math.min(v, 1.2);
              pos.setXYZ(k, xs[j], y0 - depth * v, zs[i]);
              col.setXYZ(k, 0.42 * b, 0.52 * b, 0.85 * b);
              k++;
            };
            for (let i = 0; i < WELL_N; i++) for (let j = 0; j < WELL_N - 1; j++) { put(i, j); put(i, j + 1); put(j, i); put(j + 1, i); }
            pos.needsUpdate = true;
            col.needsUpdate = true;
          }
        }
        // Free planets follow Kepler, bound groups orbit as one body (see galaxyModel).
        w.planets.forEach((p, i) => {
          placePlanet(w, i, simTime, p.pos);
          m4.compose(p.pos, q, sc.setScalar(Math.max(p.size * grow[i], 1e-4)));
          planetMesh!.setMatrixAt(i, m4);
        });
        planetMesh.instanceMatrix.needsUpdate = true;
        w.groups.forEach((_, i) => nebulae[i] && placeGroup(w, i, simTime, nebulae[i].position));
        if (replay) {
          const groupBorn = new Float32Array(w.groups.length);
          w.planets.forEach((p, i) => { if (p.group >= 0 && grow[i] > 0) groupBorn[p.group]++; });
          nebulae.forEach((n, i) => (n.visible = w.groups[i].size >= 3 && groupBorn[i] >= 3));
        } else nebulae.forEach((n, i) => (n.visible = w.groups[i].size >= 3));

        const selPlanet = L.sel?.type === "planet" ? L.sel.i : -1;
        const selSys = L.sel?.type === "sys" ? L.sel.i : -1;
        if (affinity) {
          affinity.lines.visible = L.showAffinity;
          if (L.showAffinity) {
            const pos = affinity.lines.geometry.getAttribute("position") as THREE.BufferAttribute;
            const col = affinity.lines.geometry.getAttribute("color") as THREE.BufferAttribute;
            w.affinity.forEach((a, k) => {
              const pa = w.planets[a.from].pos, pb = w.planets[a.to].pos;
              pos.setXYZ(2 * k, pa.x, pa.y, pa.z);
              pos.setXYZ(2 * k + 1, pb.x, pb.y, pb.z);
              const touches = selPlanet >= 0 ? a.from === selPlanet || a.to === selPlanet
                : selSys >= 0 ? w.planets[a.from].sys === selSys || w.planets[a.to].sys === selSys : true;
              const rest = affinity!.rest[k];
              const f = affinity!.cross[k] ? crossDim(selPlanet >= 0 || selSys >= 0, touches)
                : (touches ? (selPlanet >= 0 ? 2.2 : 1) : 0.12) * Math.min(1, (rest + 8) / (pa.distanceTo(pb) + 1));
              const alive = grow[a.from] > 0 && grow[a.to] > 0 ? 1 : 0;
              for (let j = 0; j < 6; j++) col.array[k * 6 + j] = affinity!.base[k * 6 + j] * f * alive;
            });
            pos.needsUpdate = true;
            col.needsUpdate = true;
          }
        }
        linkObjs.forEach((lo, k) => {
          const l = w.links[k];
          const alive = grow[l.from] > 0 && grow[l.to] > 0 && (!replay || l.born === 0 || l.born <= at);
          lo.line.visible = L.showLinks && alive;
          lo.sparks.forEach((s) => (s.visible = L.showLinks && alive));
          if (!L.showLinks || !alive) return;
          const a = w.planets[l.from].pos, b = w.planets[l.to].pos;
          const mid = a.clone().add(b).multiplyScalar(0.5);
          mid.y += a.distanceTo(b) * 0.3 + 4;
          curve.v0.copy(a); curve.v1.copy(mid); curve.v2.copy(b);
          const attr = lo.line.geometry.getAttribute("position") as THREE.BufferAttribute;
          for (let i = 0; i <= 40; i++) {
            const pt = curve.getPoint(i / 40);
            attr.setXYZ(i, pt.x, pt.y, pt.z);
          }
          attr.needsUpdate = true;
          const touches = selPlanet >= 0 ? l.from === selPlanet || l.to === selPlanet
            : selSys >= 0 ? w.planets[l.from].sys === selSys || w.planets[l.to].sys === selSys : true;
          lo.mat.opacity = lo.cross ? 0.1 * crossDim(selPlanet >= 0 || selSys >= 0, touches) : touches ? 0.75 : 0.12;
          lo.sparks.forEach((s, j) => {
            s.position.copy(curve.getPoint((clock.elapsedTime * 0.22 + j / 3) % 1));
            (s.material as THREE.SpriteMaterial).opacity = touches ? 1 : 0.15;
          });
        });

        if (selPlanet >= 0) {
          const p = w.planets[selPlanet];
          halo.visible = true;
          halo.position.copy(p.pos);
          halo.scale.setScalar(p.size * 5 + Math.sin(clock.elapsedTime * 3) * 0.6);
        } else halo.visible = false;
      }

      if (tween) {
        const t = Math.min(1, (performance.now() - tween.t0) / tween.dur);
        const k = ease(t);
        const goal = tween.to();
        camera.position.lerpVectors(tween.from, goal.pos, k);
        controls.target.lerpVectors(tween.fromT, goal.target, k);
        if (t >= 1) {
          tween = null;
          if (follow) lastFollow = follow().clone();
        }
      } else if (follow) {
        const now = follow();
        const delta = now.clone().sub(lastFollow);
        camera.position.add(delta);
        controls.target.add(delta);
        lastFollow.copy(now);
      }
      controls.update();
      if (w) {
        const rect = el.getBoundingClientRect();
        w.systems.forEach((s, i) => {
          const d = labelEls[i];
          if (!d) return;
          ndc.copy(s.pos).add(new THREE.Vector3(0, s.sunR * 1.6, 0)).project(camera);
          const visible = ndc.z < 1 && Math.abs(ndc.x) < 1.1 && Math.abs(ndc.y) < 1.1 && (sysGroups[i]?.visible ?? true);
          d.style.display = visible ? "block" : "none";
          if (visible) d.style.transform = `translate(${(ndc.x * 0.5 + 0.5) * rect.width}px, ${(-ndc.y * 0.5 + 0.5) * rect.height}px) translate(-50%, -100%)`;
        });
      }
      composer.render();
    };
    frame();

    // Stop following when the user grabs the camera.
    const stopFollow = () => { tween = null; follow = null; };
    controls.addEventListener("start", stopFollow);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      controls.removeEventListener("start", stopFollow);
      controls.dispose();
      renderer.domElement.removeEventListener("pointerdown", onDown);
      renderer.domElement.removeEventListener("pointerup", onUp);
      renderer.domElement.removeEventListener("pointermove", onMove);
      renderer.domElement.removeEventListener("pointerleave", onLeave);
      disposeGroup(scene);
      composer.dispose();
      renderer.dispose();
      el.removeChild(renderer.domElement);
      engine.current = null;
    };
  }, []);

  // Rebuild the world when data/filters change, then frame it.
  const framed = useRef(false);
  const keep = useRef<string | null>(null); // entry to stay on after a rebuild (e.g. after linking from the card)
  useEffect(() => {
    const e = engine.current;
    if (!e || !world) return;
    e.rebuild(world);
    const kept = keep.current ? world.planets.findIndex((p) => p.node.id === keep.current) : -1;
    keep.current = null;
    if (kept >= 0) return live.current.select({ type: "planet", i: kept });
    setSel(null);
    setDetail(null);
    if (!framed.current) {
      framed.current = true;
      e.camera.position.set(0, world.extent * 2.2, world.extent * 3.2); // start far, then glide in
    }
    e.flyTo(null, true);
  }, [world]);

  const select = (s: Sel) => {
    setSel(s);
    setDetail(null);
    engine.current?.flyTo(s);
    if (s?.type === "planet" && world) api.entry(world.planets[s.i].node.id).then(setDetail, () => undefined);
  };
  live.current.select = select;

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q || !world) return [];
    const sys = world.systems.map((s, i) => ({ sel: { type: "sys", i } as Sel, label: s.label, color: s.colorHex, meta: t("galaxy.star") }))
      .filter((x) => x.label.toLowerCase().includes(q));
    const pl = world.planets.map((p, i) => ({ sel: { type: "planet", i } as Sel, label: p.node.title, color: kindColor(p.node.kind), meta: p.node.kind }))
      .filter((x) => x.label.toLowerCase().includes(q));
    return [...sys, ...pl].slice(0, 9);
  }, [query, world]);

  const chips = useMemo(() => {
    if (!data) return { projects: [], kinds: [] as [string, number][] };
    const counts = new Map<string, number>();
    const kinds = new Map<string, number>();
    data.nodes.forEach((n) => {
      if (!filter.superseded && n.status === "superseded") return; // same set as the galaxy shows
      const k = n.project ?? `space:${n.space}`;
      counts.set(k, (counts.get(k) ?? 0) + 1);
      kinds.set(n.kind, (kinds.get(n.kind) ?? 0) + 1);
    });
    const idx = new Map(data.projects.map((p, i) => [p.ref, i]));
    return {
      projects: [...counts].sort((a, b) => b[1] - a[1]).map(([k, c]) => ({
        key: k, count: c,
        label: k.startsWith("space:") ? t("graph.noProject", { space: k.slice(6) }) : data.projects.find((p) => p.ref === k)?.name ?? k,
        color: k.startsWith("space:") ? "#cfd8e6" : STAR_COLORS[(idx.get(k) ?? 0) % STAR_COLORS.length],
      })),
      kinds: [...kinds].sort((a, b) => ORBIT_ORDER.indexOf(a[0]) - ORBIT_ORDER.indexOf(b[0])),
    };
  }, [data, filter.superseded, t]);

  const toggle = (key: "projects" | "kinds", v: string) =>
    setFilter((f) => {
      const s = new Set(f[key]);
      if (s.has(v)) s.delete(v);
      else s.add(v);
      return { ...f, [key]: s };
    });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).tagName === "INPUT") return;
      if (e.key === "Escape") { if (sel) select(null); else if (full) setFull(false); }
      if (e.key === " ") { e.preventDefault(); setPaused((p) => !p); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  const sysStats = useMemo(() => {
    if (sel?.type !== "sys" || !world) return null;
    const s = world.systems[sel.i];
    const ps = world.planets.filter((p) => p.sys === sel.i);
    const byKind = new Map<string, number>();
    ps.forEach((p) => byKind.set(p.node.kind, (byKind.get(p.node.kind) ?? 0) + 1));
    return {
      s, byKind: [...byKind].sort((a, b) => ORBIT_ORDER.indexOf(a[0]) - ORBIT_ORDER.indexOf(b[0])),
      proposed: ps.filter((p) => p.node.status === "proposed").length,
      active: activityPower(world, s.project, Date.now()),
      events: (() => {
        const a = s.project ? world.activity.get(s.project) : undefined;
        let n = 0;
        a?.t.forEach((t, i) => { if (Date.now() - t < ACTIVE_HOURS * 3600_000) n += a.n[i]; });
        return n;
      })(),
      recent: ps.map((p) => ({ p, i: world.planets.indexOf(p) })).sort((a, b) => b.p.node.updated_at.localeCompare(a.p.node.updated_at)).slice(0, 6),
    };
  }, [sel, world]);

  // ── Evolution player ──────────────────────────────────────────────────────
  const PLAY_SECONDS = 40; // the whole history at 1×
  const history = useMemo(() => {
    if (!world) return null;
    const end = Date.now();
    const strip = activityTimeline(world, world.start, end, 160);
    const max = Math.max(1, ...strip);
    let d = "M0 24";
    strip.forEach((v, i) => { d += ` L${i} ${24 - 22 * Math.sqrt(v / max)} L${i + 1} ${24 - 22 * Math.sqrt(v / max)}`; });
    return { start: world.start, end, path: `${d} L160 24 Z` };
  }, [world]);
  useEffect(() => {
    if (history) live.current.playRate = ((Date.now() - history.start) / PLAY_SECONDS) * speed;
  }, [history, speed]);
  live.current.onPlayEnd = () => { setPlaying(false); setPlayAt(null); };
  useEffect(() => {
    if (!playing) return;
    const id = setInterval(() => setPlayAt(live.current.playAt), 120);
    return () => clearInterval(id);
  }, [playing]);
  const play = () => {
    if (!history) return;
    const from = live.current.playAt;
    live.current.playAt = from === null || from >= Date.now() - 60_000 ? history.start : from;
    setPlayAt(live.current.playAt);
    setPlaying(true);
  };
  const seek = (v: number) => {
    live.current.playAt = v >= Date.now() - 60_000 ? null : v;
    setPlayAt(live.current.playAt);
    setPlaying(false);
  };
  const toLive = () => { live.current.playAt = null; setPlayAt(null); setPlaying(false); };
  const fmtWhen = (at: number) => formatDate(at, { dateStyle: "medium", timeStyle: "short" });
  const activeNow = useMemo(() => (world ? world.systems.filter((s) => activityPower(world, s.project, Date.now()) > 0).length : 0), [world]);

  const planet = sel?.type === "planet" && world ? world.planets[sel.i] : null;
  const neighbours = useMemo(() => {
    if (sel?.type !== "planet" || !world) return [];
    return world.affinity.filter((a) => a.from === sel.i || a.to === sel.i)
      .map((a) => ({ i: a.from === sel.i ? a.to : a.from, score: a.score }))
      .sort((a, b) => b.score - a.score);
  }, [sel, world]);
  // Turns an implicit link into an explicit one: the pair now pulls as hard as any link, and the card stays open.
  const toast = useToast();
  const [linking, setLinking] = useState<string | null>(null);
  const linkTo = (from: string, to: string) => {
    setLinking(to);
    api.addLink(from, to, "relates_to").then(() => {
      keep.current = from;
      setData((d) => d && {
        ...d,
        links: [...d.links, { from, to, rel: "relates_to" }],
        affinity: d.affinity.filter((a) => !((a.from === from && a.to === to) || (a.from === to && a.to === from))),
      });
      toast.ok(t("galaxy.linked"));
    }, toast.error).finally(() => setLinking(null));
  };
  const planetIndex = (id: string) => world?.planets.findIndex((p) => p.node.id === id) ?? -1;

  return (
    <div className={`graph-wrap galaxy ${full ? "full" : ""}`}>
      <div ref={box} className="graph-canvas" />
      <div ref={labelsBox} className="galaxy-labels" />
      <div ref={tip} className="graph-tip galaxy-tip" style={{ display: "none" }} />

      <div className="graph-toolbar">
        <div className="graph-search">
          <input placeholder={t("galaxy.searchPh")} value={query} onChange={(e) => setQuery(e.target.value)} />
          {matches.length > 0 && (
            <div className="graph-matches">
              {matches.map((m, k) => (
                <button key={k} onClick={() => { setQuery(""); select(m.sel); }}>
                  <span className="dot" style={{ background: m.color }} />
                  <span className="grow">{m.label}</span>
                  <span className="graph-muted">{m.meta}</span>
                </button>
              ))}
            </div>
          )}
        </div>
        <button className="graph-btn" onClick={() => setPaused((p) => !p)} title={t("galaxy.spaceKey")}>{paused ? t("galaxy.orbitsPlay") : t("galaxy.orbitsPause")}</button>
        <label className="graph-toggle"><input type="checkbox" checked={showLinks} onChange={(e) => setShowLinks(e.target.checked)} /> {t("galaxy.links")}</label>
        <label className="graph-toggle" title={t("galaxy.implicitTitle")}>
          <input type="checkbox" checked={showAffinity} onChange={(e) => setShowAffinity(e.target.checked)} /> {t("galaxy.implicit")}
        </label>
        <label className="graph-toggle" title={t("galaxy.gravityTitle")}>
          <input type="checkbox" checked={showWell} onChange={(e) => setShowWell(e.target.checked)} /> {t("galaxy.gravity")}
        </label>
        <label className="graph-toggle"><input type="checkbox" checked={filter.onlyProposed} onChange={(e) => setFilter((f) => ({ ...f, onlyProposed: e.target.checked }))} /> {t("graph.onlyProposed")}</label>
        <label className="graph-toggle" title={t("galaxy.supersededTitle")}>
          <input type="checkbox" checked={filter.superseded} onChange={(e) => setFilter((f) => ({ ...f, superseded: e.target.checked }))} /> {t("galaxy.superseded")}
        </label>
        <button className="graph-btn" onClick={() => select(null)}>{t("galaxy.whole")}</button>
        <button className="graph-btn" onClick={() => setFull((f) => !f)}>{full ? t("graph.exitFull") : t("graph.full")}</button>
      </div>

      <div className="graph-legend">
        {history && (
          <div className={`galaxy-player ${playAt !== null ? "on" : ""}`}>
            <button className="graph-btn" onClick={playing ? () => setPlaying(false) : play} title={t("galaxy.playTitle")}>
              {playing ? "⏸" : "▶"} {t("galaxy.play")}
            </button>
            <div className="galaxy-timeline">
              <svg viewBox="0 0 160 24" preserveAspectRatio="none" aria-hidden="true"><path d={history.path} /></svg>
              <input type="range" aria-label={t("galaxy.moment")} min={history.start} max={history.end} step={60_000}
                value={playAt ?? history.end} onChange={(e) => seek(Number(e.target.value))} />
            </div>
            <span className={`galaxy-date ${playAt === null ? "live" : ""}`}>
              {playAt === null ? t("galaxy.nowActive", { count: activeNow }) : fmtWhen(playAt)}
            </span>
            <button className="graph-btn sm" onClick={() => setSpeed((v) => (v === 1 ? 4 : v === 4 ? 12 : 1))} title={t("galaxy.speed")}>{speed}×</button>
            {playAt !== null && <button className="graph-btn sm" onClick={toLive}>{t("galaxy.toNow")}</button>}
          </div>
        )}
        <div className="graph-legend-row">
          {chips.projects.map((p) => (
            <button key={p.key} className={`graph-chip ${filter.projects.size && !filter.projects.has(p.key) ? "off" : ""}`} onClick={() => toggle("projects", p.key)}>
              <span className="dot" style={{ background: p.color, color: p.color }} />{p.label} <span className="graph-muted">{p.count}</span>
            </button>
          ))}
        </div>
        <div className="graph-legend-row">
          {chips.kinds.map(([k, c]) => (
            <button key={k} className={`graph-chip ${filter.kinds.size && !filter.kinds.has(k) ? "off" : ""}`} onClick={() => toggle("kinds", k)}>
              <span className="dot" style={{ background: kindColor(k), color: kindColor(k) }} />{k} <span className="graph-muted">{c}</span>
            </button>
          ))}
          {(filter.kinds.size > 0 || filter.projects.size > 0) && (
            <button className="graph-chip" onClick={() => setFilter((f) => ({ ...f, kinds: new Set(), projects: new Set() }))}>{t("graph.resetFilters")}</button>
          )}
        </div>
      </div>

      {!data && !error && <div className="graph-overlay">{t("galaxy.loading")}</div>}
      {error && <div className="graph-overlay">{t("galaxy.loadError", { error })}</div>}
      <div className="graph-hint">{t("galaxy.hint")}</div>

      {planet && (
        <div className="graph-panel">
          <button className="graph-close" onClick={() => select(null)} aria-label={t("common.close")}>✕</button>
          <div className="graph-panel-title">{planet.node.title}</div>
          <div className="row small" style={{ margin: "8px 0" }}>
            <KindBadge kind={planet.node.kind} />
            <StatusBadge status={planet.node.status} />
            {planet.node.pinned && <span className="badge pin">{t("common.pinned")}</span>}
          </div>
          <div className="graph-muted small">
            {planet.group >= 0 ? t("galaxy.groupAround", { count: world!.groups[planet.group].size, star: world!.systems[planet.sys].label }) : t("galaxy.orbitAround", { kind: planet.node.kind, star: world!.systems[planet.sys].label })}
            {" · "}{fmtMass(planet.mass)} · {fmtAgo(planet.node.updated_at)}
          </div>
          <div className="graph-muted small">
            {planet.node.used ? t("galaxy.used90", { count: planet.node.used }) : t("galaxy.unused90")}
            {planet.node.review_due && <span className="badge warn" style={{ marginLeft: 6 }}>{t("galaxy.reviewDue")}</span>}
          </div>
          {detail?.summary && <div className="graph-summary">{detail.summary}</div>}
          {!detail?.summary && detail?.body && <div className="graph-summary">{detail.body.slice(0, 280)}{detail.body.length > 280 ? "…" : ""}</div>}
          {detail && <div className="row small" style={{ marginTop: 8 }}><Tags tags={detail.tags} /></div>}
          {detail && detail.links.outgoing.length + detail.links.incoming.length > 0 && (
            <div className="graph-links">
              {detail.links.outgoing.map((l) => (
                <button key={`o${l.to_id}${l.rel}`} onClick={() => { const i = planetIndex(l.to_id); if (i >= 0) select({ type: "planet", i }); }}>
                  → <span style={{ color: REL_COLORS[l.rel] }}>{l.rel}</span> {l.title}
                </button>
              ))}
              {detail.links.incoming.map((l) => (
                <button key={`i${l.from_id}${l.rel}`} onClick={() => { const i = planetIndex(l.from_id); if (i >= 0) select({ type: "planet", i }); }}>
                  ← <span style={{ color: REL_COLORS[l.rel] }}>{l.rel}</span> {l.title}
                </button>
              ))}
            </div>
          )}
          {neighbours.length > 0 && (
            <>
              <div className="graph-muted small" style={{ margin: "12px 0 4px" }}>{t("galaxy.implicitHead")}</div>
              <div className="graph-links">
                {neighbours.map(({ i, score }) => (
                  <div key={i} className="graph-link-row">
                    <button onClick={() => select({ type: "planet", i })} title={t("galaxy.similarity", { p: Math.round(score * 100) })}>
                      <span className="dot" style={{ background: kindColor(world!.planets[i].node.kind), color: kindColor(world!.planets[i].node.kind) }} />
                      {" "}{world!.planets[i].node.title}
                      <span className="graph-muted"> · {Math.round(score * 100)} %</span>
                    </button>
                    <button className="graph-btn sm" disabled={linking !== null} title={t("galaxy.linkTitle")}
                      onClick={() => linkTo(planet.node.id, world!.planets[i].node.id)}>
                      {linking === world!.planets[i].node.id ? "…" : t("galaxy.link")}
                    </button>
                  </div>
                ))}
              </div>
            </>
          )}
          <div className="row" style={{ marginTop: 12 }}>
            <button className="btn primary sm" onClick={() => navigate(`/entries/${planet.node.id}`)}>{t("graph.openEntry")}</button>
            <button className="graph-btn" onClick={() => select({ type: "sys", i: planet.sys })}>{t("galaxy.toStar")}</button>
          </div>
        </div>
      )}

      {sysStats && (
        <div className="graph-panel">
          <button className="graph-close" onClick={() => select(null)} aria-label={t("common.close")}>✕</button>
          <div className="graph-panel-title" style={{ color: sysStats.s.colorHex }}>☀ {sysStats.s.label}</div>
          <div className="graph-muted small" style={{ margin: "4px 0 10px" }}>
            {t("count.entries", { count: sysStats.s.count })} · {fmtMass(sysStats.s.mass)} · {t("galaxy.orbits", { count: sysStats.byKind.length })}{sysStats.proposed ? ` · ${t("graph.proposedCount", { count: sysStats.proposed })}` : ""}
          </div>
          <div className="graph-muted small" style={{ margin: "0 0 10px" }} title={t("galaxy.weightTitle")}>
            {t("galaxy.weight", { weight: fmtWeight(sysStats.s.weight), cohesion: formatNumber(sysStats.s.cohesion, { maximumFractionDigits: 2 }) })}
            {sysStats.s.omega ? t("galaxy.period", { min: Math.round((2 * Math.PI) / sysStats.s.omega / 60) }) : t("galaxy.core")}
          </div>
          {sysStats.s.project && (
            <div className="graph-muted small" style={{ margin: "0 0 10px" }} title={t("galaxy.activeTitle")}>
              {sysStats.active > 0 ? t("galaxy.active", { p: Math.round(sysStats.active * 100), count: sysStats.events }) : t("galaxy.quiet")}
            </div>
          )}
          <div className="graph-bars">
            {sysStats.byKind.map(([k, c]) => (
              <div key={k} className="graph-bar-row">
                <span className="dot" style={{ background: kindColor(k), color: kindColor(k) }} />
                <span className="grow">{k}</span>
                <span className="graph-bar"><span style={{ width: `${(c / (sysStats.s.count || 1)) * 100}%`, background: kindColor(k) }} /></span>
                <span className="graph-muted">{c}</span>
              </div>
            ))}
          </div>
          <div className="graph-muted small" style={{ margin: "12px 0 4px" }}>{t("galaxy.recentPlanets")}</div>
          <div className="graph-links">
            {sysStats.recent.map(({ p, i }) => (
              <button key={p.node.id} onClick={() => select({ type: "planet", i })}>
                <span className="dot" style={{ background: kindColor(p.node.kind), color: kindColor(p.node.kind) }} /> {p.node.title}
              </button>
            ))}
          </div>
          <div className="row" style={{ marginTop: 12 }}>
            <Link className="btn primary sm" to={sysStats.s.project ? `/entries?project=${sysStats.s.project}` : `/entries?space=${sysStats.s.space}`}>{t("graph.allEntries")}</Link>
            {sysStats.s.project && (
              <button className="graph-btn" onClick={() => setFilter((f) => ({ ...f, projects: new Set([sysStats.s.project!]) }))}>{t("galaxy.onlyThis")}</button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
