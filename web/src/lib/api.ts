import type {
  ApiKey,
  Attribute,
  AttrType,
  Diversion,
  DiversionDef,
  Gradual,
  LaunchedField,
  LaunchRecord,
  Rollout,
  MetricBrief,
  ParamUse,
  ParamValue,
  Platform,
  Scope,
  Targeting,
  AuditEntry,
  Business,
  EventSummary,
  Experiment,
  Hit,
  Layer,
  Measure,
  Metric,
  MetricGroup,
  PipelineStatus,
  Report,
  Step,
  TrendPoint,
  User,
  Variant,
} from "./types";

// Where the app is served from ("/libra/"), without the trailing slash.
export const BASE = import.meta.env.BASE_URL.replace(/\/$/, "");
export const asset = (path: string) => `${BASE}/${path.replace(/^\//, "")}`;

export class ApiError extends Error {
  code: string;
  status: number;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)libra_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken();
  const res = await fetch(`${BASE}/api${path}`, {
    method,
    headers,
    credentials: "same-origin",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!res.ok) {
    const err = data as { code?: string; message?: string } | null;
    if (res.status === 401 && !path.startsWith("/auth") && !path.startsWith("/setup") && path !== "/me") {
      window.location.assign(`${BASE}/login`);
    }
    throw new ApiError(res.status, err?.code ?? "error", err?.message ?? `request failed (${res.status})`);
  }
  return data as T;
}

const qs = (p: Record<string, string | number | undefined | null>) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(p)) if (v !== undefined && v !== null && v !== "") s.set(k, String(v));
  const out = s.toString();
  return out ? `?${out}` : "";
};

// The API prefix of a definitions scope.
export const scopePath = (s: Scope) => (s.kind === "platform" ? `/platforms/${s.id}` : `/businesses/${s.id}`);

export interface ExperimentInput {
  business_id: number;
  layer_id: number;
  name: string;
  hypothesis: string;
  description: string;
  owner_id: number | null;
  traffic_target: number;
  targeting: Targeting;
  metric_group_ids: number[];
  auto_diversion?: string; // set: a dedicated layer at 100%, split by this diversion
  variants: Variant[];
}

export type MeasureInput = Omit<Measure, "id" | "business_id" | "platform_id" | "inherited" | "pending_backfill" | "used_by" | "filters_text">;
export type GroupInput = { name: string; description: string; is_default: boolean; metric_ids: number[] };
type BusinessInput = { platform_id: number; key: string; name: string; description: string; require_review: boolean };
export type MetricInput = Pick<Metric, "key" | "name" | "description" | "formula" | "format" | "decimals" | "direction">;

export const api = {
  providers: () => request<{ providers: string[]; setup_needed: boolean }>("GET", "/auth/providers"),
  setupStart: (token: string, provider: string) => request<{ redirect: string }>("POST", "/setup/start", { token, provider }),
  redeemLink: (token: string) => request<User>("POST", "/auth/link", { token }),
  logout: () => request<void>("POST", "/logout"),
  me: () => request<User>("GET", "/me"),

  users: () => request<User[]>("GET", "/users"),
  createUser: (u: { username: string; email: string; display_name: string; role: string }) => request<User>("POST", "/users", u),
  updateUser: (id: number, u: { username: string; email: string; display_name: string; role: string }) =>
    request<User>("PUT", `/users/${id}`, u),
  deleteUser: (id: number) => request<void>("DELETE", `/users/${id}`),
  apiKeys: () => request<ApiKey[]>("GET", "/api-keys"),
  createApiKey: (name: string, scopes: string[]) => request<ApiKey>("POST", "/api-keys", { name, scopes }),
  revokeApiKey: (id: number) => request<void>("DELETE", `/api-keys/${id}`),

  businesses: () => request<Business[]>("GET", "/businesses"),
  business: (id: number) => request<Business>("GET", `/businesses/${id}`),
  createBusiness: (b: BusinessInput) => request<Business>("POST", "/businesses", b),
  updateBusiness: (id: number, b: BusinessInput) => request<Business>("PUT", `/businesses/${id}`, b),
  platforms: () => request<Platform[]>("GET", "/platforms"),
  platform: (id: number) => request<Platform>("GET", `/platforms/${id}`),
  createPlatform: (p: { key: string; name: string; description: string }) => request<Platform>("POST", "/platforms", p),
  updatePlatform: (id: number, p: { key: string; name: string; description: string }) => request<Platform>("PUT", `/platforms/${id}`, p),
  deletePlatform: (id: number) => request<void>("DELETE", `/platforms/${id}`),
  deleteBusiness: (id: number) => request<void>("DELETE", `/businesses/${id}`),
  deleteCheck: (id: number) =>
    request<{
      experiments: number;
      active_experiments: number;
      measures: number;
      metrics: number;
      groups: number;
      events: number;
      other_experiments_use: number;
    }>("GET", `/businesses/${id}/delete-check`),

  measures: (sc: Scope) => request<Measure[]>("GET", `${scopePath(sc)}/measures`),
  createMeasure: (sc: Scope, m: MeasureInput) => request<Measure>("POST", `${scopePath(sc)}/measures`, m),
  updateMeasure: (id: number, m: MeasureInput) => request<Measure>("PUT", `/measures/${id}`, m),
  deleteMeasure: (id: number) => request<void>("DELETE", `/measures/${id}`),

  metrics: (sc: Scope) => request<Metric[]>("GET", `${scopePath(sc)}/metrics`),
  allMetrics: () => request<MetricBrief[]>("GET", "/metrics"),
  createMetric: (sc: Scope, m: MetricInput) => request<Metric>("POST", `${scopePath(sc)}/metrics`, m),
  updateMetric: (id: number, m: MetricInput) => request<Metric>("PUT", `/metrics/${id}`, m),
  deleteMetric: (id: number) => request<void>("DELETE", `/metrics/${id}`),
  validateFormula: (sc: Scope, formula: string, key: string) =>
    request<{ ok: boolean; error?: string; expanded?: string; kind?: string; measures?: string[] }>(
      "POST",
      `${scopePath(sc)}/formula/validate`,
      { formula, key }
    ),
  previewFormula: (sc: Scope, formula: string, days: number) =>
    request<{ ok: boolean; error?: string; value?: number | null; users?: number; measures?: Record<string, number>; from?: string; to?: string; kind?: string }>(
      "POST",
      `${scopePath(sc)}/formula/preview`,
      { formula, days }
    ),

  groups: (sc: Scope) => request<MetricGroup[]>("GET", `${scopePath(sc)}/metric-groups`),
  allGroups: () => request<MetricGroup[]>("GET", "/metric-groups"),
  createGroup: (sc: Scope, g: GroupInput) => request<MetricGroup>("POST", `${scopePath(sc)}/metric-groups`, g),
  updateGroup: (id: number, g: GroupInput) => request<MetricGroup>("PUT", `/metric-groups/${id}`, g),
  deleteGroup: (id: number) => request<void>("DELETE", `/metric-groups/${id}`),

  eventSummary: (bid: number) => request<EventSummary>("GET", `/businesses/${bid}/events/summary`),
  recentEvents: (bid: number) =>
    request<{ id: number; event: string; unit_id: string; ts: string; value: number; props: Record<string, unknown> }[]>(
      "GET",
      `/businesses/${bid}/events/recent`
    ),

  layers: () => request<Layer[]>("GET", "/layers"),
  createLayer: (name: string, description: string, diversion: Diversion) => request<Layer>("POST", "/layers", { name, description, diversion }),
  updateLayer: (id: number, name: string, description: string) => request<void>("PUT", `/layers/${id}`, { name, description }),

  experiments: (p: { platform?: string; business?: string; status?: string; q?: string; mine?: string } = {}) =>
    request<Experiment[]>("GET", `/experiments${qs(p)}`),
  experiment: (id: number) => request<Experiment>("GET", `/experiments/${id}`),
  createExperiment: (e: ExperimentInput) => request<Experiment>("POST", "/experiments", e),
  updateExperiment: (id: number, e: ExperimentInput) => request<Experiment>("PUT", `/experiments/${id}`, e),
  action: (id: number, action: string, body: { note?: string; variant_id?: number; gradual?: Gradual } = {}) =>
    request<Experiment>("POST", `/experiments/${id}/actions/${action}`, body),
  setTraffic: (id: number, traffic_target: number, gradual?: Gradual) =>
    request<Experiment>("PUT", `/experiments/${id}/traffic`, { traffic_target, gradual }),
  rollouts: (id: number) => request<Rollout[]>("GET", `/experiments/${id}/rollouts`),
  cancelRollout: (id: number, rid: number) => request<void>("DELETE", `/experiments/${id}/rollouts/${rid}`),
  addWhitelist: (id: number, unit_ids: string[], variant_id: number, note: string) =>
    request<void>("POST", `/experiments/${id}/whitelist`, { unit_ids, variant_id, note }),
  removeWhitelist: (id: number, unit: string) => request<void>("DELETE", `/experiments/${id}/whitelist/${encodeURIComponent(unit)}`),
  history: (id: number) => request<AuditEntry[]>("GET", `/experiments/${id}/history`),
  clone: (id: number) => request<{ id: number }>("POST", `/experiments/${id}/clone`),
  report: (id: number, p: { from?: string; to?: string; metrics?: string; groups?: string; dimension?: string; alpha?: string }) =>
    request<Report>("GET", `/experiments/${id}/report${qs(p)}`),
  trend: (id: number, metric: number, p: { from?: string; to?: string; alpha?: string }) =>
    request<TrendPoint[]>("GET", `/experiments/${id}/trend${qs({ metric, ...p })}`),
  exposures: (id: number) => request<{ day: string; variants: Record<string, number> }[]>("GET", `/experiments/${id}/exposures`),

  attributes: () => request<Attribute[]>("GET", "/attributes"),
  discoveredAttributes: () => request<{ key: string; seen: number; values: string[] }[]>("GET", "/attributes/discovered"),
  createAttribute: (a: { key: string; name: string; description: string; type: AttrType; options: string[] }) =>
    request<Attribute>("POST", "/attributes", a),
  updateAttribute: (id: number, a: { key: string; name: string; description: string; type: AttrType; options: string[] }) =>
    request<void>("PUT", `/attributes/${id}`, a),
  deleteAttribute: (id: number) => request<void>("DELETE", `/attributes/${id}`),
  paramUsage: (id: number) => request<{ priority_rules: string[]; paths: Record<string, ParamUse[]> }>("GET", `/experiments/${id}/params`),

  diversions: () => request<DiversionDef[]>("GET", "/diversions"),
  createDiversion: (d: { key: string; name: string; description: string }) => request<DiversionDef>("POST", "/diversions", d),
  updateDiversion: (key: string, d: { name: string; description: string }) => request<void>("PUT", `/diversions/${key}`, d),
  deleteDiversion: (key: string) => request<void>("DELETE", `/diversions/${key}`),
  parameters: () => request<ParamValue[]>("GET", "/parameters"),
  launchedConfig: () =>
    request<{ businesses: { key: string; platform: string; business: string; name: string; fields: LaunchedField[] }[]; history: LaunchRecord[] }>(
      "GET",
      "/parameters/launched"
    ),

  diagnose: (ids: Record<string, string>, platform: string, business: string, attrs: Record<string, unknown>) =>
    request<{ snapshot_version: number; result: { hits: Hit[]; params: Record<string, unknown>; trace: Step[]; conflicts?: unknown[] } }>(
      "POST",
      "/tools/diagnose",
      {
        user_id: ids.user_id ?? "",
        device_id: ids.device_id ?? "",
        ids: Object.fromEntries(Object.entries(ids).filter(([k, v]) => k !== "user_id" && k !== "device_id" && v)),
        platform,
        business,
        attrs,
      }
    ),
  paramSearch: (q: string) =>
    request<{ experiment_id: number; experiment: string; platform: string; business: string; status: string; variant: string; path: string }[]>(
      "GET",
      `/tools/params${qs({ q })}`
    ),
  pipeline: () => request<PipelineStatus>("GET", "/pipeline"),
  runPipeline: () => request<Record<string, number>>("POST", "/pipeline/run"),
};
