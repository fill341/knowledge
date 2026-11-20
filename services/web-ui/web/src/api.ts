// Client for the Knowledge API through the kn-ui proxy (/api/* → /v1/*).
// The token lives in an HttpOnly cookie; every state-changing call carries X-KN-UI.
import { getLang } from "./i18n";

export type Status = "draft" | "proposed" | "active" | "deprecated" | "superseded" | "archived";
export const STATUSES: Status[] = ["draft", "proposed", "active", "deprecated", "superseded", "archived"];
export type Level = "none" | "read" | "write" | "review" | "admin";
export const LINK_RELS = ["relates_to", "depends_on", "supersedes", "derived_from", "contradicts", "references"] as const;

export interface Entry {
  id: string;
  space: string;
  project: string | null;
  kind: string;
  title: string;
  summary: string | null;
  body: string;
  attributes: Record<string, unknown>;
  tags: string[];
  status: Status;
  pinned: boolean;
  confidence: number | null;
  version: number;
  superseded_by: string | null;
  review_after: string | null;
  session_id: string | null;
  source_ref: string | null;
  created_by: string;
  updated_by: string;
  created_at: string;
  updated_at: string;
}

export interface Link {
  from_id: string;
  to_id: string;
  rel: string;
  title: string;
  created_at: string;
}

export interface EntryDetail extends Entry {
  links: { outgoing: Link[]; incoming: Link[] };
  usage?: { days: number; shown: number; delivered: number; opened: number; last_day: string | null };
  review_due?: string;
}

export interface Dependent extends HealthItem {
  depth: number;
  rel: string;
  via: string;
}

export interface HealthSnapshot {
  day: string;
  score: number;
  severity: HealthCheck["severity"];
  live_entries: number;
  checks: Record<string, { severity: HealthCheck["severity"]; count: number }>;
}

export interface Similar {
  id: string;
  title: string;
  status: string;
  similarity: number;
}

export interface Warning {
  code: string;
  message: string;
  items?: Similar[];
}

export interface EntryResult {
  entry: Entry;
  warnings: Warning[];
}

export interface Version {
  version: number;
  project: string | null;
  kind: string;
  title: string;
  summary: string | null;
  body: string;
  attributes: Record<string, unknown>;
  tags: string[];
  status: Status;
  pinned: boolean;
  changed_by: string;
  change_note: string | null;
  created_at: string;
}

export interface SearchItem {
  id: string;
  space: string;
  project: string | null;
  kind: string;
  title: string;
  summary: string | null;
  status: Status;
  version: number;
  tags: string[];
  score: number;
  highlight: string;
  updated_at: string;
}

export interface Page<T> {
  items: T[];
  next_cursor?: string;
}

export interface Project {
  id: string;
  space: string;
  slug: string;
  ref: string;
  name: string;
  description: string;
  repo_urls: string[];
  created_at: string;
  updated_at: string;
  archived_at: string | null;
}

export interface SpaceAccess {
  slug: string;
  name: string;
  level: Level;
}

export interface Me {
  principal: { id: string; kind: string; name: string };
  scopes: string[];
  spaces: SpaceAccess[];
}

export interface Principal {
  id: string;
  kind: "user" | "agent" | "system";
  name: string;
  description: string;
  created_at: string;
  disabled_at: string | null;
}

export interface Token {
  id: string;
  principal: string;
  name: string;
  prefix: string;
  scopes: string[];
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
  revoked_at: string | null;
  token?: string;
}

export interface AuditRecord {
  id: number;
  at: string;
  principal: string | null;
  space: string | null;
  action: string;
  target_type: string | null;
  target_id: string | null;
  details: Record<string, unknown>;
}

export interface Kind {
  kind: string;
  description: string;
  attributes_schema: Record<string, unknown>;
  agent_requires_review: boolean;
  review_days: number | null;
}

export interface Stats {
  total: number;
  by_space: Record<string, Record<string, number>>;
  by_kind: Record<string, number>;
  review_queue: number;
  projects: number;
}

export interface GraphNode {
  id: string;
  space: string;
  project: string | null;
  kind: string;
  title: string;
  status: Status;
  pinned: boolean;
  tags: string[];
  updated_at: string;
  created_at: string; // the evolution player shows the entry from this moment
  mass: number; // characters of title, summary and body
  review_due: boolean; // active and past its re-check date
  used: number; // delivered to agents or opened in the last 90 days
}

export interface GraphData {
  nodes: GraphNode[];
  links: { from: string; to: string; rel: string; created_at?: string }[];
  affinity: { from: string; to: string; score: number }[]; // implicit links by content
  activity?: { project: string; hour: string; events: number }[]; // hourly development events: jets and the player

  projects: Project[];
  truncated: boolean;
}

export interface HealthItem {
  id: string;
  title: string;
  kind: string;
  space: string;
  project: string | null;
  status: Status;
  links: number;
  created_at: string;
  created_by?: string;
  detail?: string;
}

export interface HealthCheck {
  id: string;
  title: string;
  severity: "ok" | "info" | "warn" | "critical";
  count: number;
  message: string;
  suggestion?: string;
  items?: HealthItem[];
  pairs?: { a: HealthItem; b: HealthItem; similarity?: number; rel?: string }[];
  groups?: HealthItem[][];
  stats?: { principal: string; action: string; calls: number; empty: number }[];
  sessions?: { id: string; principal: string; agent: string; space: string; project: string | null; started_at: string; entries: number }[];
  tokens?: Token[];
}

export interface HealthReport {
  generated_at: string;
  project: string | null;
  spaces: string[];
  score: number;
  severity: HealthCheck["severity"];
  live_entries: number;
  summary: string;
  checks: HealthCheck[];
}

export interface ReviewItem extends Entry {
  duplicate_of?: Similar[];
}

export interface ImportResult {
  dry_run: boolean;
  projects_created: number;
  entries_created: number;
  entries_updated: number;
  entries_skipped: number;
  links_created: number;
  duplicates: { line: number; id: string; title: string; existing_id: string; skipped: boolean }[];
  errors: { line: number; error: string }[];
}

export class ApiError extends Error {
  status: number;
  code: string;
  existingId?: string;
  currentVersion?: number;
  constructor(status: number, body: Record<string, unknown>) {
    super(String(body.detail || body.code || `HTTP ${status}`));
    this.status = status;
    this.code = String(body.code || "error");
    this.existingId = body.existing_id as string | undefined;
    this.currentVersion = body.current_version as number | undefined;
  }
}

// Notified on 401 so the app can send the user back to the login screen.
let onUnauthenticated: () => void = () => {};
export function setUnauthenticatedHandler(fn: () => void) {
  onUnauthenticated = fn;
}

interface ReqOpts {
  headers?: Record<string, string>;
  raw?: BodyInit;
  rawType?: string;
}

export async function request<T>(method: string, path: string, body?: unknown, opts: ReqOpts = {}): Promise<T> {
  // Server-built texts (e.g. the health report) follow the UI language, not the browser's.
  const headers: Record<string, string> = { "X-KN-UI": "1", "Accept-Language": getLang(), ...opts.headers };
  let payload: BodyInit | undefined;
  if (opts.raw !== undefined) {
    payload = opts.raw;
    if (opts.rawType) headers["Content-Type"] = opts.rawType;
  } else if (body !== undefined) {
    payload = JSON.stringify(body);
    headers["Content-Type"] = "application/json";
  }
  const res = await fetch(path, { method, headers, body: payload, credentials: "same-origin" });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = { detail: text };
  }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith("/auth/login")) onUnauthenticated();
    throw new ApiError(res.status, (data as Record<string, unknown>) || {});
  }
  return data as T;
}

const qs = (params: Record<string, string | number | boolean | undefined | null | string[]>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0)) continue;
    p.set(k, Array.isArray(v) ? v.join(",") : String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
};

const ifMatch = (v: number) => ({ headers: { "If-Match": `"${v}"` } });

export const api = {
  // session
  login: (token: string) => request<{ principal: string }>("POST", "/auth/login", { token }),
  logout: () => request<void>("POST", "/auth/logout"),
  session: () => request<{ principal: string; expires: string }>("GET", "/auth/me"),

  me: () => request<Me>("GET", "/api/me"),
  stats: () => request<Stats>("GET", "/api/stats"),
  kinds: () => request<{ items: Kind[] }>("GET", "/api/kinds").then((r) => r.items),
  spaces: () => request<{ items: SpaceAccess[] }>("GET", "/api/spaces").then((r) => r.items),

  // projects
  projects: (includeArchived = false) =>
    request<{ items: Project[] }>("GET", `/api/projects${qs({ include_archived: includeArchived })}`).then((r) => r.items),
  createProject: (p: { space: string; slug: string; name: string; description: string; repo_urls: string[] }) =>
    request<Project>("POST", "/api/projects", p),
  patchProject: (ref: string, p: Partial<{ name: string; description: string; repo_urls: string[]; archived: boolean }>) =>
    request<Project>("PATCH", `/api/projects/${ref}`, p),

  // entries
  listEntries: (f: { space?: string; project?: string; kind?: string[]; status?: string[]; tag?: string[]; pinned?: boolean; limit?: number; cursor?: string }) =>
    request<Page<Entry>>("GET", `/api/entries${qs(f)}`),
  entry: (id: string) => request<EntryDetail>("GET", `/api/entries/${id}`),
  createEntry: (e: Record<string, unknown>) => request<EntryResult>("POST", "/api/entries", e),
  patchEntry: (id: string, version: number, patch: Record<string, unknown>) =>
    request<EntryResult>("PATCH", `/api/entries/${id}`, patch, ifMatch(version)),
  transition: (id: string, to: Status, opts: { superseded_by?: string; note?: string } = {}) =>
    request<EntryResult>("POST", `/api/entries/${id}/transition`, { to, ...opts }),
  promote: (id: string, to_space: string, to_project?: string) =>
    request<EntryResult>("POST", `/api/entries/${id}/promote`, { to_space, to_project: to_project || "" }),
  versions: (id: string) => request<{ items: Version[] }>("GET", `/api/entries/${id}/versions`).then((r) => r.items),
  revert: (id: string, version: number, toVersion: number) =>
    request<EntryResult>("POST", `/api/entries/${id}/revert`, { to_version: toVersion }, ifMatch(version)),
  addLink: (from: string, to: string, rel: string) => request<void>("POST", `/api/entries/${from}/links`, { to_id: to, rel }),
  deleteLink: (from: string, to: string, rel: string) => request<void>("DELETE", `/api/entries/${from}/links/${to}/${rel}`),

  // review
  reviewQueue: () => request<{ items: ReviewItem[] }>("GET", "/api/review-queue?limit=200").then((r) => r.items),
  review: (id: string, action: "approve" | "reject" | "request_changes", comment?: string) =>
    request<EntryResult>("POST", `/api/entries/${id}/review`, comment ? { action, comment } : { action }),

  graph: () => request<GraphData>("GET", "/api/graph"),
  health: (project?: string, limit = 30) => request<HealthReport>("GET", `/api/health${qs({ project, limit })}`),

  // search
  search: (s: { q: string; spaces?: string[]; project?: string; project_only?: boolean; kinds?: string[]; status?: string[]; tags?: string[]; limit?: number; cursor?: string }) =>
    request<Page<SearchItem>>("POST", "/api/search", s),

  // admin
  principals: () => request<{ items: Principal[] }>("GET", "/api/admin/principals").then((r) => r.items),
  createPrincipal: (p: { kind: string; name: string; description: string }) => request<Principal>("POST", "/api/admin/principals", p),
  setPrincipalDisabled: (name: string, disabled: boolean) =>
    request<Principal>("POST", `/api/admin/principals/${encodeURIComponent(name)}/${disabled ? "disable" : "enable"}`),
  tokens: () => request<{ items: Token[] }>("GET", "/api/admin/tokens").then((r) => r.items),
  createToken: (t: { principal: string; name?: string; scopes: string[]; expires_in_days?: number; create_principal?: boolean }) =>
    request<Token>("POST", "/api/admin/tokens", t),
  revokeToken: (id: string) => request<Token>("POST", `/api/admin/tokens/${id}/revoke`),
  audit: (f: { principal?: string; action?: string; target_id?: string; since?: string; before_id?: number; limit?: number }) =>
    request<{ items: AuditRecord[] }>("GET", `/api/admin/audit${qs(f)}`).then((r) => r.items),
  impact: (id: string) => request<{ count: number; direct: number; dependents: Dependent[] }>("GET", `/api/entries/${id}/impact`),
  linkSuggestions: (id: string) => request<{ items: Similar[] }>("GET", `/api/entries/${id}/link-suggestions`).then((r) => r.items),
  healthHistory: (days = 90) => request<{ items: HealthSnapshot[] }>("GET", `/api/health/history?days=${days}`).then((r) => r.items),
  putKind: (kind: string, k: { description: string; attributes_schema: unknown; agent_requires_review: boolean; review_days: number | null }) =>
    request<Kind>("PUT", `/api/admin/kinds/${kind}`, k),

  // data
  exportUrl: (format: "jsonl" | "markdown", spaces: string[], history: boolean) => {
    const p = new URLSearchParams({ format });
    spaces.forEach((s) => p.append("space", s));
    if (history) p.set("include_history", "true");
    return `/api/export?${p}`;
  },
  importJsonl: (file: File, mode: string, dryRun: boolean, skipDuplicateContent = false) =>
    request<ImportResult>("POST", `/api/import${qs({ mode, dry_run: dryRun, skip_duplicate_content: skipDuplicateContent || undefined })}`,
      undefined, { raw: file, rawType: "application/x-ndjson" }),
};

export const isAdmin = (me: Me | null) => !!me?.scopes.includes("admin");
export const canReview = (me: Me | null) =>
  !!me && me.principal.kind !== "agent" && (isAdmin(me) || me.spaces.some((s) => s.level === "review" || s.level === "admin"));

// Status transitions for a human with review rights (the server enforces the real rules).
export const TRANSITIONS: Record<Status, Status[]> = {
  draft: ["proposed", "active", "archived"],
  proposed: ["active", "archived", "draft"],
  active: ["deprecated", "superseded", "archived"],
  deprecated: ["active", "archived"],
  superseded: ["archived"],
  archived: ["active"],
};
