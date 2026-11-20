// Physics of the "Galaxy" view, kept apart from rendering so it can be checked headless.
//
// Mass is the volume of knowledge: a planet's mass is the length of its text, its radius grows as the cube
// root (mass ∝ volume), and a star weighs as much as all its planets. Links are the gravitational potential:
// explicit links pull hard, implicit ones (entries that talk about the same thing, computed by the API) pull
// weakly, and the force moves light entries more than heavy ones. Strongly attracted entries become bound
// groups that orbit the star as one body, tidally locked, so a group never tears; free planets follow
// Kepler's law — inner orbits turn faster. Each kind keeps a weak home orbit (rules closest to the star,
// documents out in the belt).
//
// Between systems the same physics works on the scale of the galaxy. A system's gravitational weight is the
// mass of its knowledge times its cohesion (how strongly its entries pull each other): Φ = M/1000 · (1 + U/n).
// The heaviest system is the galactic core; heavier systems sink closer to it, cross-project links pull
// systems together, and every system orbits the core by Kepler with the weight enclosed by its orbit.
// The potential Σ √Φ·s/√(d²+s²) is drawn as a gravity well under the galaxy.
//
// Time: every entry and link has a birth moment, and every project an hourly record of development (entry
// revisions and agent sessions). A project developed in the last ACTIVE_HOURS hours wears a halo, the brighter the
// more recent work there was. The evolution player replays the same world at any moment of its history: entries
// appear when they were written, halos light up when work happened.
import * as THREE from "three";
import type { GraphData, GraphNode } from "../api";


// ── Palette and orbit order ──────────────────────────────────────────────────

export const KIND_COLORS: Record<string, string> = {
  rule: "#ff6b6b", decision: "#8b8cff", fact: "#2ee6c5", lesson: "#ffc93c",
  snippet: "#5cf08a", note: "#a7b4c8", session_log: "#7d8aa3", document: "#6aa8ff",
};
export const ORBIT_ORDER = ["rule", "decision", "fact", "lesson", "snippet", "note", "session_log", "document"];
export const STAR_COLORS = ["#ffb347", "#7fd8ff", "#ff7eb6", "#b4ff7a", "#ffd86b", "#c49bff", "#ff8f6b", "#6bffe1", "#f7a1ff", "#9fb6ff"];
export const REL_COLORS: Record<string, string> = {
  depends_on: "#ffc93c", supersedes: "#ff6b6b", derived_from: "#c49bff",
  contradicts: "#ff6b9a", references: "#7fd8ff", relates_to: "#e8eefc",
};
export const kindColor = (k: string) => KIND_COLORS[k] ?? "#cbd5e1";

// Deterministic pseudo-random in [0,1) from a string, so the layout is stable between visits.
export function rand(seed: string, salt = 0) {
  let h = 2166136261 ^ salt;
  for (let i = 0; i < seed.length; i++) h = Math.imul(h ^ seed.charCodeAt(i), 16777619);
  return ((h >>> 0) % 100000) / 100000;
}

// ── Physical constants ───────────────────────────────────────────────────────

// Planet radius from mass (characters): 100 → 0.6, 1 000 → 1.0, 10 000 → 1.9, 100 000 → 4.
export const planetRadius = (mass: number, pinned = false) => (0.25 + 0.08 * Math.cbrt(Math.max(mass, 50))) * (pinned ? 1.25 : 1);
// Star radius from the mass of its system: 10 000 → 2.8, 2 000 000 → 9.2.
export const starRadius = (mass: number) => 1.5 + 0.06 * Math.cbrt(Math.max(mass, 1000));
// Gravity of a link: explicit links pull hard, implicit ones by how similar the entries are.
const REL_PULL: Record<string, number> = { depends_on: 1.3, derived_from: 1.2, supersedes: 1.2, contradicts: 1, references: 0.9, relates_to: 0.9 };
const implicitPull = (score: number) => 0.18 + Math.min(score, 0.6) * 0.9;
// Entries attracted at least this hard end up in one bound group.
const BIND = 0.36;
// Kepler: ω = K·√(GM)/r^1.5; the star's GM is taken as its radius so systems of very different mass stay watchable.
const KEPLER = 2.5;
const MAX_OMEGA = 0.15;

export const kepler = (gm: number, r: number) => Math.min(MAX_OMEGA, (KEPLER * Math.sqrt(gm)) / Math.pow(Math.max(r, 1), 1.5));

// ── Model ────────────────────────────────────────────────────────────────────

export interface Sys {
  key: string;      // project ref, or "space:<slug>"
  label: string;
  color: THREE.Color;
  colorHex: string;
  count: number;
  mass: number;     // characters of all its entries
  cohesion: number; // mean pull of links per entry inside the system
  weight: number;   // gravitational weight Φ = mass/1000 · (1 + cohesion)
  pos: THREE.Vector3; // live position; the orbit around the galactic core below
  orbitR: number;
  a0: number;
  omega: number;
  y: number;
  tilt: THREE.Quaternion; // plane of the system's disk
  sunR: number;
  radius: number;   // outer radius of the system
  project: string | null;
  space: string;
}

export interface Planet {
  node: GraphNode;
  born: number;     // ms since epoch when the entry was written (0 = unknown, always present)
  sys: number;
  mass: number;
  size: number;
  group: number;    // bound group, or -1 for a free planet
  x: number;        // place in the disk at t = 0
  z: number;
  r: number;        // free planet: orbit radius, start angle and angular speed
  a0: number;
  omega: number;
  lift: number;     // height above the disk
  phase: number;    // small drift around the place, so the disk does not look frozen
  pos: THREE.Vector3;
}

// A bound group: entries pulled together by their links, orbiting the star as one body.
export interface Group {
  sys: number;
  x: number;        // barycentre at t = 0
  z: number;
  r: number;
  a0: number;
  omega: number;
  lift: number;
  spread: number;
  mass: number;
  size: number;
  color: string;
  pos: THREE.Vector3;
}

export interface Affinity { from: number; to: number; score: number }

export interface World {
  systems: Sys[];
  planets: Planet[];
  links: { from: number; to: number; rel: string; born: number }[];
  affinity: Affinity[];
  rings: { sys: number; r: number; color: string }[];
  groups: Group[];
  extent: number;
  activity: Map<string, { t: Float64Array; n: Float64Array }>; // per project: hour starts (ms) and events
  start: number;    // first moment of history (ms), for the evolution player
}

export interface Filter { projects: Set<string>; kinds: Set<string>; onlyProposed: boolean; superseded: boolean }

const sysKey = (n: GraphNode) => n.project ?? `space:${n.space}`;

export function buildWorld(data: GraphData, filter: Filter, spaceLabel: (space: string) => string = (s) => s): World {
  // Superseded entries are retired copies (e.g. after duplicate cleanup) — hidden unless asked for; when shown,
  // their supersedes links pull each copy next to its replacement.
  const nodes = data.nodes.filter((n) => (filter.superseded || n.status !== "superseded") &&
    (filter.kinds.size === 0 || filter.kinds.has(n.kind)) &&
    (filter.projects.size === 0 || filter.projects.has(sysKey(n))) &&
    (!filter.onlyProposed || n.status === "proposed"));
  const present = new Set(nodes.map((n) => n.id));

  // All gravity between visible entries: explicit links and implicit affinity.
  const pulls: { from: string; to: string; w: number }[] = [];
  data.links.forEach((l) => present.has(l.from) && present.has(l.to) && pulls.push({ from: l.from, to: l.to, w: REL_PULL[l.rel] ?? 1 }));
  (data.affinity ?? []).forEach((a) => present.has(a.from) && present.has(a.to) && pulls.push({ from: a.from, to: a.to, w: implicitPull(a.score) }));

  const bySys = new Map<string, GraphNode[]>();
  nodes.forEach((n) => bySys.set(sysKey(n), [...(bySys.get(sysKey(n)) ?? []), n]));
  // Binding of each system: the sum of pulls between its own entries.
  const keyOf = new Map(nodes.map((n) => [n.id, sysKey(n)]));
  const binding = new Map<string, number>();
  pulls.forEach((p) => {
    const a = keyOf.get(p.from);
    if (a !== undefined && a === keyOf.get(p.to)) binding.set(a, (binding.get(a) ?? 0) + p.w);
  });
  const massOf = (n: GraphNode) => Math.max(n.mass ?? 0, 50);

  const projectIndex = new Map(data.projects.map((p, i) => [p.ref, i]));
  const keys = [...bySys.keys()].sort((a, b) => sysMass(bySys.get(b)!) - sysMass(bySys.get(a)!));
  function sysMass(ms: GraphNode[]) { return ms.reduce((t, n) => t + massOf(n), 0); }

  const systems: Sys[] = [];
  const planets: Planet[] = [];
  const rings: World["rings"] = [];
  const groups: Group[] = [];

  keys.forEach((key) => {
    const members = bySys.get(key)!;
    const isSpace = key.startsWith("space:");
    const project = isSpace ? null : data.projects.find((p) => p.ref === key) ?? null;
    const colorHex = isSpace ? "#cfd8e6" : STAR_COLORS[(projectIndex.get(key) ?? 0) % STAR_COLORS.length];
    const mass = sysMass(members);
    const cohesion = (binding.get(key) ?? 0) / members.length;
    const sunR = starRadius(mass);
    const sysIdx = systems.length;

    // Home orbit of each kind, inner to outer; a belt is as wide as its planets need.
    const byKind = new Map<string, GraphNode[]>();
    members.forEach((m) => byKind.set(m.kind, [...(byKind.get(m.kind) ?? []), m]));
    const kinds = [...byKind.keys()].sort((a, b) => {
      const ia = ORBIT_ORDER.indexOf(a), ib = ORBIT_ORDER.indexOf(b);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
    });
    const home = new Map<string, { r: number; width: number; belt: boolean }>();
    let r = sunR * 2.2 + 6;
    for (const kind of kinds) {
      const list = byKind.get(kind)!;
      const belt = kind === "document" || list.length > 60;
      const area = list.reduce((t, m) => t + planetRadius(massOf(m)) ** 2, 0);
      const width = 1.2 + Math.sqrt(area) * (belt ? 0.55 : 0.3);
      r += width;
      home.set(kind, { r, width, belt });
      rings.push({ sys: sysIdx, r, color: kindColor(kind) });
      r += width + 4;
    }

    const lay = layoutSystem(members, pulls, home, sunR, massOf);
    lay.nodes.forEach((p) => planets.push({ ...p, sys: sysIdx, group: p.group < 0 ? -1 : p.group + groups.length, pos: new THREE.Vector3(),
      born: ms(p.node.created_at) }));
    lay.groups.forEach((g) => groups.push({ ...g, sys: sysIdx, pos: new THREE.Vector3() }));
    r = Math.max(r, lay.extent + 4);
    systems.push({
      key, label: isSpace ? spaceLabel(key.slice(6)) : project?.name ?? key,
      color: new THREE.Color(colorHex), colorHex, count: members.length, mass, cohesion,
      weight: (mass / 1000) * (1 + cohesion), pos: new THREE.Vector3(), orbitR: 0, a0: 0, omega: 0, y: 0,
      tilt: new THREE.Quaternion().setFromEuler(new THREE.Euler((rand(key, 1) - 0.5) * 0.35, rand(key, 2) * Math.PI, (rand(key, 3) - 0.5) * 0.25)),
      sunR, radius: r, project: isSpace ? null : key, space: isSpace ? key.slice(6) : key.split("/")[0],
    });
  });

  // Orbital speeds: Kepler around each star.
  planets.forEach((p) => (p.omega = kepler(systems[p.sys].sunR, p.r)));
  groups.forEach((g) => (g.omega = kepler(systems[g.sys].sunR, g.r)));

  layoutGalaxy(systems, planets, pulls);
  const extent = Math.max(60, ...systems.map((s) => s.pos.length() + s.radius));

  const index = new Map(planets.map((p, i) => [p.node.id, i]));
  const links = data.links
    .filter((l) => index.has(l.from) && index.has(l.to))
    .map((l) => ({ from: index.get(l.from)!, to: index.get(l.to)!, rel: l.rel, born: ms(l.created_at) }));
  const affinity = (data.affinity ?? [])
    .filter((a) => index.has(a.from) && index.has(a.to))
    .map((a) => ({ from: index.get(a.from)!, to: index.get(a.to)!, score: a.score }));

  const activity = new Map<string, { t: number[]; n: number[] }>();
  (data.activity ?? []).forEach((a) => {
    const rec = activity.get(a.project) ?? { t: [], n: [] };
    rec.t.push(ms(a.hour));
    rec.n.push(a.events);
    activity.set(a.project, rec);
  });
  const born = planets.map((p) => p.born).filter((b) => b > 0);
  const firstWork = [...activity.values()].map((a) => a.t[0]).filter((x) => x > 0);
  const start = Math.min(Date.now(), ...born, ...firstWork);
  return {
    systems, planets, links, affinity, rings, groups, extent, start,
    activity: new Map([...activity].map(([k, v]) => [k, { t: Float64Array.from(v.t), n: Float64Array.from(v.n) }])),
  };
}

const ms = (iso?: string | null) => (iso ? Date.parse(iso) || 0 : 0);

// ── Activity halo ────────────────────────────────────────────────────────

export const ACTIVE_HOURS = 24;
const HOUR = 3600_000;

// activityPower is how hard a project is being developed at moment `at`: events of the last ACTIVE_HOURS hours,
// each weighted by how recent it is (linear fade to zero at ACTIVE_HOURS), saturated into 0…1. Zero when nothing
// happened in the window — the halo goes out after a quiet day.
export function activityPower(w: World, project: string | null, at: number) {
  const a = project ? w.activity.get(project) : undefined;
  if (!a) return 0;
  let score = 0;
  // hours are sorted: walk back from the last one not after `at`
  let hi = a.t.length - 1;
  while (hi >= 0 && a.t[hi] > at) hi--;
  for (let i = hi; i >= 0; i--) {
    const age = (at - a.t[i]) / HOUR;
    if (age >= ACTIVE_HOURS) break;
    score += a.n[i] * (1 - age / ACTIVE_HOURS);
  }
  return score > 0 ? 1 - Math.exp(-score / 8) : 0;
}

// activityTimeline sums events of all visible projects into `buckets` equal slices of [start, end]:
// the strip drawn behind the evolution player's slider.
export function activityTimeline(w: World, start: number, end: number, buckets: number) {
  const out = new Float64Array(buckets);
  const span = Math.max(end - start, 1);
  w.systems.forEach((s) => {
    const a = s.project ? w.activity.get(s.project) : undefined;
    a?.t.forEach((t, i) => {
      const b = Math.floor(((t - start) / span) * buckets);
      if (b >= 0 && b < buckets) out[b] += a.n[i];
    });
  });
  return out;
}

// ── Gravity inside a system ──────────────────────────────────────────────────

type Laid = Omit<Planet, "sys" | "pos" | "born">;
type LaidGroup = Omit<Group, "sys" | "pos">;

// layoutSystem places the entries of one system in its disk with a deterministic force simulation: links pull
// linked entries together with their strength, entries collide, and a spring pulls every entry to the home
// orbit of its kind — the weaker the more the entry is attracted elsewhere. Forces move an entry inversely to
// its mass, so small facts gather around heavy documents rather than the other way round. Entries attracted
// at least BIND hard form bound groups (label propagation), which start next to each other and later orbit
// as one body.
function layoutSystem(members: GraphNode[], pulls: { from: string; to: string; w: number }[],
  home: Map<string, { r: number; width: number; belt: boolean }>, sunR: number, massOf: (n: GraphNode) => number,
): { nodes: Laid[]; groups: LaidGroup[]; extent: number } {
  const n = members.length;
  const idx = new Map(members.map((m, i) => [m.id, i]));
  const edges: { a: number; b: number; w: number }[] = [];
  pulls.forEach((p) => {
    const a = idx.get(p.from), b = idx.get(p.to);
    if (a !== undefined && b !== undefined && a !== b) edges.push({ a, b, w: p.w });
  });
  const mass = members.map(massOf);
  const size = members.map((m, i) => planetRadius(mass[i], m.pinned));
  const median = [...mass].sort((a, b) => a - b)[Math.floor(n / 2)] ?? 1;
  const mobility = mass.map((m) => Math.min(2.5, Math.max(0.3, Math.sqrt(median / m))));
  const pullSum = new Float64Array(n);
  edges.forEach((e) => { pullSum[e.a] += e.w; pullSum[e.b] += e.w; });

  const group = boundGroups(n, edges.filter((e) => e.w >= BIND), members);
  const groupList: number[][] = [];
  group.forEach((g, v) => { if (g >= 0) (groupList[g] ??= []).push(v); });

  // Start: groups at evenly spaced angles, each member on its home orbit next to the previous one;
  // free entries spread evenly around their home orbit.
  const x = new Float64Array(n), z = new Float64Array(n);
  const put = (v: number, a: number) => {
    const h = home.get(members[v].kind)!;
    const rr = h.r + (rand(members[v].id, 4) - 0.5) * h.width * (h.belt ? 1.8 : 0.7);
    x[v] = Math.cos(a) * rr;
    z[v] = Math.sin(a) * rr;
  };
  const turn = rand(members[0]?.id ?? "", 8) * Math.PI * 2;
  const order = groupList.map((_, i) => i).sort((a, b) => groupList[b].length - groupList[a].length);
  const slots = order.reduce((t, gi) => t + groupList[gi].length, 0);
  let acc = 0;
  order.forEach((gi) => {
    const g = groupList[gi];
    const centre = turn + ((acc + g.length / 2) / Math.max(slots, 1)) * Math.PI * 2;
    acc += g.length;
    g.forEach((v, k) => put(v, centre + ((k - (g.length - 1) / 2) * (size[v] * 2.5)) / home.get(members[v].kind)!.r));
  });
  const freeByKind = new Map<string, number[]>();
  group.forEach((g, v) => g < 0 && freeByKind.set(members[v].kind, [...(freeByKind.get(members[v].kind) ?? []), v]));
  freeByKind.forEach((list, kind) => {
    const off = rand(kind, 5) * Math.PI * 2;
    list.forEach((v, i) => put(v, off + (i / list.length) * Math.PI * 2 + (rand(members[v].id, 5) - 0.5) * 0.2));
  });

  // Force simulation. O(n²) collisions with an early exit are fine for a few hundred entries per system.
  const minR = sunR * 2 + 2;
  const iters = n > 600 ? 140 : 320;
  const fx = new Float64Array(n), fz = new Float64Array(n);
  for (let it = 0; it < iters; it++) {
    const alpha = 1 - it / iters;
    fx.fill(0); fz.fill(0);
    for (const { a, b, w } of edges) {
      const dx = x[b] - x[a], dz = z[b] - z[a];
      const d = Math.hypot(dx, dz) || 0.01;
      const f = (0.1 * w * (d - (size[a] + size[b]) * 1.4 - 1.5)) / d;
      fx[a] += dx * f; fz[a] += dz * f; fx[b] -= dx * f; fz[b] -= dz * f;
    }
    for (let a = 0; a < n; a++) {
      for (let b = a + 1; b < n; b++) {
        const dx = x[b] - x[a], dz = z[b] - z[a];
        const sep = (size[a] + size[b]) * 1.2 + 1;
        if (Math.abs(dx) > sep || Math.abs(dz) > sep) continue;
        const d = Math.hypot(dx, dz) || 0.01;
        if (d >= sep) continue;
        const f = (0.5 * (sep - d)) / d;
        fx[a] -= dx * f; fz[a] -= dz * f; fx[b] += dx * f; fz[b] += dz * f;
      }
      const rr = Math.hypot(x[a], z[a]) || 0.01;
      const k = 0.08 / (1 + 2.5 * pullSum[a]);
      const f = (k * (home.get(members[a].kind)!.r - rr)) / rr;
      fx[a] += x[a] * f; fz[a] += z[a] * f;
    }
    for (let a = 0; a < n; a++) {
      x[a] += fx[a] * alpha * mobility[a]; z[a] += fz[a] * alpha * mobility[a];
      const rr = Math.hypot(x[a], z[a]);
      if (rr < minR) { x[a] *= minR / (rr || 1); z[a] *= minR / (rr || 1); }
    }
  }

  // Bound groups: barycentre by mass; the group floats a little above or below the disk.
  const lift = new Float64Array(n);
  const groups: LaidGroup[] = groupList.map((g) => {
    const m = g.reduce((t, v) => t + mass[v], 0);
    const cx = g.reduce((t, v) => t + x[v] * mass[v], 0) / m, cz = g.reduce((t, v) => t + z[v] * mass[v], 0) / m;
    const cl = (rand(members[g[0]].id, 9) - 0.5) * 6;
    g.forEach((v) => (lift[v] = cl + (rand(members[v].id, 7) - 0.5) * 1.2));
    const kindMass = new Map<string, number>();
    g.forEach((v) => kindMass.set(members[v].kind, (kindMass.get(members[v].kind) ?? 0) + mass[v]));
    const top = [...kindMass.entries()].sort((a, b) => b[1] - a[1])[0][0];
    return {
      x: cx, z: cz, r: Math.hypot(cx, cz), a0: Math.atan2(cz, cx), omega: 0, lift: cl,
      spread: Math.max(...g.map((v) => Math.hypot(x[v] - cx, z[v] - cz) + size[v])),
      mass: m, size: g.length, color: kindColor(top),
    };
  });
  const nodes: Laid[] = [];
  let extent = 0;
  members.forEach((m, i) => {
    const h = home.get(m.kind)!;
    if (group[i] < 0) lift[i] = (rand(m.id, 7) - 0.5) * (h.belt ? h.width * 0.6 : 0.6);
    nodes.push({
      node: m, mass: mass[i], size: size[i], group: group[i], x: x[i], z: z[i],
      r: Math.hypot(x[i], z[i]), a0: Math.atan2(z[i], x[i]), omega: 0,
      lift: lift[i], phase: rand(m.id, 6) * Math.PI * 2,
    });
    extent = Math.max(extent, Math.hypot(x[i], z[i]) + size[i]);
  });
  return { nodes, groups, extent };
}

// boundGroups splits strongly attracted entries into groups by weighted label propagation (deterministic:
// fixed visiting order, ties to the smaller label). Returns a group index per entry, -1 for free entries.
// Groups larger than a quarter of the system are split by the same process, so one theme cannot swallow it.
function boundGroups(n: number, edges: { a: number; b: number; w: number }[], members: GraphNode[]): number[] {
  const adj: { j: number; w: number }[][] = Array.from({ length: n }, () => []);
  edges.forEach(({ a, b, w }) => { adj[a].push({ j: b, w }); adj[b].push({ j: a, w }); });
  const label = Array.from({ length: n }, (_, i) => i);
  const order = [...label].sort((a, b) => (members[a].id < members[b].id ? -1 : 1));
  for (let round = 0; round < 25; round++) {
    let changed = false;
    for (const v of order) {
      if (adj[v].length === 0) continue;
      const score = new Map<number, number>();
      adj[v].forEach(({ j, w }) => score.set(label[j], (score.get(label[j]) ?? 0) + w));
      let best = label[v], bestS = score.get(label[v]) ?? 0;
      score.forEach((s, l) => { if (s > bestS + 1e-9 || (Math.abs(s - bestS) < 1e-9 && l < best)) { best = l; bestS = s; } });
      if (best !== label[v]) { label[v] = best; changed = true; }
    }
    if (!changed) break;
  }
  const sizes = new Map<number, number>();
  label.forEach((l) => sizes.set(l, (sizes.get(l) ?? 0) + 1));
  const ids = new Map<number, number>();
  return label.map((l) => {
    if ((sizes.get(l) ?? 0) < 2) return -1;
    if (!ids.has(l)) ids.set(l, ids.size);
    return ids.get(l)!;
  });
}

// ── Gravity between systems ──────────────────────────────────────────────────

// Galactic gravity works with √Φ, so a system a thousand times heavier does not swallow the galaxy.
const gravMass = (s: Sys) => Math.sqrt(Math.max(s.weight, 0.01));
const GALAXY_PERIOD_R = 200;   // at this distance from a core of the galaxy's total weight…
const GALAXY_OMEGA = 0.018;    // …a system turns this fast (one turn in about six minutes)

// layoutGalaxy places the stars. The heaviest system is the core at the centre; the rest start on a
// golden-angle spiral in order of weight. Forces: every system is pulled to the core the harder the heavier
// it is (so heavy systems sink inward, light ones stay on the outskirts), cross-project links pull systems
// together, systems never come closer than a gap; heavier systems move less. Then each system gets a Kepler
// orbit around the core from the weight enclosed by its orbit.
function layoutGalaxy(systems: Sys[], planets: Planet[], pulls: { from: string; to: string; w: number }[]) {
  const n = systems.length;
  if (n === 0) return;
  const sysOf = new Map(planets.map((p) => [p.node.id, p.sys]));
  const W = new Map<number, number>();
  pulls.forEach((p) => {
    const a = sysOf.get(p.from), b = sysOf.get(p.to);
    if (a === undefined || b === undefined || a === b) return;
    const k = Math.min(a, b) * n + Math.max(a, b);
    W.set(k, (W.get(k) ?? 0) + p.w);
  });
  const g = systems.map(gravMass);
  const byWeight = systems.map((_, i) => i).sort((a, b) => systems[b].weight - systems[a].weight);
  const core = byWeight[0];
  const gMax = g[core], gMin = Math.min(...g);

  let dist = 0;
  byWeight.forEach((i, k) => {
    const s = systems[i];
    s.y = k === 0 ? 0 : (rand(s.key, 8) - 0.5) * 40;
    if (k === 0) return s.pos.set(0, 0, 0);
    dist = Math.max(dist, systems[core].radius) + s.radius + 26 + (k > 1 ? systems[byWeight[k - 1]].radius * 0.35 : 0);
    const a = k * 2.39996;
    s.pos.set(Math.cos(a) * dist, s.y, Math.sin(a) * dist);
  });
  const mob = g.map((x) => Math.min(1, Math.max(0.15, (gMin / x) * 2)));
  const gap = 24;
  const d = new THREE.Vector3();
  const force = systems.map(() => new THREE.Vector3());
  const iters = 400, settle = 60; // the last passes only resolve collisions, so systems never overlap
  for (let it = 0; it < iters + settle; it++) {
    const alpha = it < iters ? 1 - it / iters : 1;
    force.forEach((f) => f.set(0, 0, 0));
    for (let a = 0; a < n; a++) {
      for (let b = a + 1; b < n; b++) {
        d.subVectors(systems[b].pos, systems[a].pos).setY(0);
        const len = d.length() || 0.01;
        const rest = systems[a].radius + systems[b].radius + gap;
        const w = W.get(a * n + b) ?? 0;
        let f = 0;
        if (len < rest) f = -0.5 * (rest - len);                                           // collision
        else if (w > 0 && it < iters) f = 0.02 * Math.min(Math.sqrt(w), 6) * (len - rest); // shared knowledge
        d.multiplyScalar(f / len);
        force[a].add(d);
        force[b].sub(d);
      }
      if (it < iters) force[a].addScaledVector(d.copy(systems[a].pos).setY(0), -(0.004 + 0.02 * (g[a] / gMax))); // the core's pull
    }
    for (let a = 0; a < n; a++) if (a !== core) systems[a].pos.addScaledVector(force[a], alpha * (it < iters ? mob[a] : 1));
  }

  // Kepler around the core with the weight inside the orbit (the core itself included). Systems whose orbital
  // annuli overlap would pass through each other at different speeds, so they form a band that turns together.
  const total = systems.reduce((t, s) => t + s.weight, 0);
  systems.forEach((s) => {
    s.orbitR = Math.hypot(s.pos.x, s.pos.z);
    s.a0 = Math.atan2(s.pos.z, s.pos.x);
  });
  const byR = systems.map((_, i) => i).filter((i) => i !== core).sort((a, b) => systems[a].orbitR - systems[b].orbitR);
  const bands: number[][] = [];
  let outer = -Infinity;
  byR.forEach((i) => {
    const s = systems[i];
    if (bands.length && s.orbitR - s.radius - gap < outer) bands[bands.length - 1].push(i);
    else bands.push([i]);
    outer = Math.max(outer, s.orbitR + s.radius);
  });
  let enclosed = systems[core].weight;
  bands.forEach((band) => {
    const m = band.reduce((t, i) => t + systems[i].weight, 0);
    const r = band.reduce((t, i) => t + systems[i].orbitR * systems[i].weight, 0) / m;
    const omega = Math.min(0.05, GALAXY_OMEGA * Math.sqrt(enclosed / total) * Math.pow(GALAXY_PERIOD_R / Math.max(r, 1), 1.5));
    band.forEach((i) => (systems[i].omega = omega));
    enclosed += m;
  });
}

// potentialAt is the depth of the gravity well at (x, z) in the galactic plane: Σ √Φ·s/√(d²+s²), softened by
// each system's radius s so a well is wide and finite.
export function potentialAt(w: World, x: number, z: number) {
  let u = 0;
  for (const s of w.systems) {
    const soft = s.radius * 0.7;
    u += (gravMass(s) * soft) / Math.sqrt((x - s.pos.x) ** 2 + (z - s.pos.z) ** 2 + soft * soft);
  }
  return u;
}

export function placeSystem(w: World, i: number, t: number, out: THREE.Vector3) {
  const s = w.systems[i];
  const a = s.a0 + s.omega * t;
  return out.set(Math.cos(a) * s.orbitR, s.y, Math.sin(a) * s.orbitR);
}

// ── Motion ───────────────────────────────────────────────────────────────────

// placePlanet puts planet i where it is at time t: free planets follow their own Kepler orbit; members of a
// bound group ride with the group, keeping their place relative to it (tidally locked, so distances to the
// star never change and the group never sweeps through it).
export function placePlanet(w: World, i: number, t: number, out: THREE.Vector3) {
  const p = w.planets[i];
  const sys = w.systems[p.sys];
  let px: number, pz: number;
  if (p.group >= 0) {
    const g = w.groups[p.group];
    const turn = g.omega * t, c = Math.cos(turn), s = Math.sin(turn);
    px = p.x * c - p.z * s;
    pz = p.x * s + p.z * c;
  } else {
    const a = p.a0 + p.omega * t;
    px = Math.cos(a) * p.r;
    pz = Math.sin(a) * p.r;
  }
  const wob = t * 0.6 + p.phase;
  return out.set(px + Math.cos(wob) * 0.3, p.lift + Math.sin(wob * 1.3) * 0.25, pz + Math.sin(wob) * 0.3).applyQuaternion(sys.tilt).add(sys.pos);
}

export function placeGroup(w: World, gi: number, t: number, out: THREE.Vector3) {
  const g = w.groups[gi];
  const a = g.a0 + g.omega * t;
  return out.set(Math.cos(a) * g.r, g.lift, Math.sin(a) * g.r).applyQuaternion(w.systems[g.sys].tilt).add(w.systems[g.sys].pos);
}
