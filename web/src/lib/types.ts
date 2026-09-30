export type Role = "admin" | "editor" | "viewer";

export interface User {
  id: number;
  username: string;
  email?: string;
  display_name: string;
  role: Role;
  is_owner: boolean;
  created_at: string;
  linked?: string[];
}

export interface Business {
  id: number;
  platform_id: number;
  platform_name: string;
  platform_key: string;
  key: string;
  name: string;
  description: string;
  require_review: boolean;
  created_at: string;
  measures: number;
  metrics: number;
  experiments: number;
}

export interface Platform {
  id: number;
  key: string;
  name: string;
  description: string;
  created_at: string;
  measures: number;
  metrics: number;
  metric_groups: number;
  businesses: Business[];
}

// Where definitions live: a business or a platform (shared by its businesses).
export interface Scope {
  kind: "business" | "platform";
  id: number;
}

export interface Filter {
  field: string;
  op: string;
  values: string[];
}

export interface Measure {
  id: number;
  business_id?: number;
  platform_id?: number;
  inherited?: boolean;
  key: string;
  name: string;
  description: string;
  event_name: string;
  aggregation: "count" | "sum" | "max" | "any";
  value_field: string;
  filters: Filter[];
  pending_backfill?: boolean;
  used_by?: string[];
  filters_text?: string[];
}

export type Kind = "ratio" | "total" | "mixed";
export type Format = "number" | "percent" | "currency";
export type Direction = "increase" | "decrease" | "neutral";

export interface Metric {
  id: number;
  business_id?: number;
  platform_id?: number;
  inherited?: boolean;
  key: string;
  name: string;
  description: string;
  formula: string;
  format: Format;
  decimals: number;
  direction: Direction;
  expanded: string;
  kind: Kind;
  measures: string[];
  error?: string;
}

export interface MetricGroup {
  id: number;
  business_id?: number;
  platform_id?: number;
  owner: string;
  owner_kind: "business" | "platform";
  platform: string;
  name: string;
  description: string;
  is_default: boolean;
  builtin?: boolean; // the platform's own default group
  metric_ids: number[];
  inherited?: boolean;
}

export interface Gradual {
  start?: number; // per mille to begin at
  step: number; // per mille per interval
  interval_minutes: number;
}

export interface Rollout {
  id: number;
  experiment_id: number;
  kind: "traffic" | "launch";
  start_value: number;
  target: number;
  step: number;
  interval_secs: number;
  next_at: string;
  status: "active" | "done" | "cancelled" | "blocked";
  note: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface LaunchedField {
  path: string;
  value: unknown;
  experiment_id: number;
  experiment: string;
  variant: string;
  launched_at: string;
  rollout: number;
  targeted: boolean;
  replaced: { path: string; value: unknown; experiment_id: number; experiment: string; launched_at: string }[];
}

export interface LaunchRecord {
  experiment_id: number;
  experiment: string;
  platform: string;
  business: string;
  status: Status;
  variant: string;
  variant_name: string;
  launched_at: string;
  rollout: number;
  targeted: boolean;
  params: Record<string, unknown>;
  live_fields: number;
  fields: number;
}

export interface MetricBrief {
  id: number;
  key: string;
  name: string;
  format: Format;
  owner: string;
}

export interface ParamValue {
  path: string;
  value: unknown;
  experiment_id: number;
  experiment: string;
  platform: string;
  business: string;
  status: Status;
  variant_key: string;
  variant_name: string;
  is_control: boolean;
  layer: string;
  layer_auto: boolean;
  diversion: string;
  traffic: number;
  default: boolean;
}

export interface Rule {
  attr: string;
  op: string;
  values: string[];
}

// OR of AND-groups: a unit matches when every rule of some group passes.
export interface Targeting {
  groups: Rule[][];
}

// A diversions key: user_id, device_id, or one an admin added.
export type Diversion = string;

export interface DiversionDef {
  key: string;
  name: string;
  description: string;
  builtin: boolean;
  layers: number;
  created_at: string;
}

export type AttrType = "string" | "number" | "version" | "boolean";

export interface Attribute {
  id: number;
  key: string;
  name: string;
  description: string;
  type: AttrType;
  options: string[];
  used_by: number;
}

export interface ParamUse {
  experiment_id: number;
  experiment: string;
  status: Status;
  layer: string;
  same_layer: boolean;
  relation: "same" | "inside" | "covers" | "shares_parent";
  their_paths: string[];
  variants: string[];
  conflict: boolean;
  winner: "this" | "other" | "none";
  reason: string;
}

export interface Variant {
  id?: number;
  key: string;
  name: string;
  is_control: boolean;
  weight: number; // per mille
  params: Record<string, unknown>;
}

export interface WhitelistEntry {
  unit_id: string;
  variant_id: number;
  note: string;
  created_at: string;
}

export type Status =
  | "draft"
  | "in_review"
  | "approved"
  | "rejected"
  | "active"
  | "paused"
  | "stopped"
  | "launched"
  | "archived";

export interface Experiment {
  id: number;
  business_id: number;
  business_key: string;
  business_name: string;
  platform_id: number;
  platform_key: string;
  platform_name: string;
  layer_id: number;
  layer_name: string;
  name: string;
  hypothesis: string;
  description: string;
  owner_id: number | null;
  owner_name: string;
  status: Status;
  traffic_target: number;
  traffic_held: number;
  targeting: Targeting;
  layer_diversion: Diversion;
  layer_auto: boolean;
  metric_group_ids: number[];
  review_note: string;
  reviewer_name: string;
  launched_variant_id: number | null;
  launch_rollout: number; // per mille of units the launched variant serves
  started_at: string | null;
  ended_at: string | null;
  launched_at: string | null;
  created_at: string;
  updated_at: string;
  variants?: Variant[];
  whitelist?: WhitelistEntry[];
  units: number;
  actions?: string[];
}

export interface Layer {
  id: number;
  name: string;
  description: string;
  diversion: Diversion;
  used_buckets: number;
  holders: { experiment_id: number; name: string; status: Status; buckets: number }[];
}

export interface ReportVariant {
  id: number;
  key: string;
  name: string;
  is_control: boolean;
  weight: number;
  units: number;
}

export interface ReportValue {
  variant_id: number;
  units: number;
  value: number | null;
  total: number | null;
  std_error: number | null;
}

export interface Comparison {
  variant_id: number;
  abs_diff: number | null;
  abs_ci_low: number | null;
  abs_ci_high: number | null;
  rel_diff: number | null;
  rel_ci_low: number | null;
  rel_ci_high: number | null;
  p_value: number | null;
  significant: boolean;
  testable: boolean;
  verdict: "better" | "worse" | "changed" | "flat" | "untestable";
}

export interface MetricResult {
  metric_id: number;
  key: string;
  name: string;
  formula: string;
  expanded: string;
  kind: Kind;
  format: Format;
  decimals: number;
  direction: Direction;
  error?: string;
  values: ReportValue[];
  comparisons: Comparison[];
}

export interface Report {
  experiment_id: number;
  from: string;
  to: string;
  alpha: number;
  dimension?: string;
  variants: ReportVariant[];
  srm: { chi_square: number | null; p_value: number | null; suspect: boolean };
  excluded_multi_variant_units: number;
  segments: { value: string; units: number; metrics: MetricResult[] }[];
  available_dimensions: string[];
  data_through?: string;
  computed_at: string;
  segments_not_shown?: number;
  groups: ReportGroup[];
}

export interface ReportGroup {
  id: number;
  name: string;
  owner: string;
  is_default: boolean;
  metric_ids: number[];
}

export interface TrendPoint {
  day: string;
  values: ReportValue[];
  comparisons: Comparison[];
}

export interface AuditEntry {
  id: number;
  entity: string;
  actor: string;
  action: string;
  from_status: string | null;
  to_status: string | null;
  detail: Record<string, unknown>;
  created_at: string;
}

export interface Hit {
  experiment_id: number;
  experiment: string;
  variant_id: number;
  variant: string;
  source: "experiment" | "whitelist" | "launch";
  unit_type: Diversion;
  unit_id?: string;
}

export interface Step {
  experiment_id: number;
  experiment: string;
  platform: string;
  business: string;
  status: Status;
  outcome: string;
  detail: string;
  layer_bucket: number;
  variant_bucket: number;
}

export interface ApiKey {
  id: number;
  name: string;
  prefix: string;
  scopes: string[];
  created_by: string;
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  key?: string;
}

export interface PipelineRun {
  id: number;
  trigger: string;
  started_at: string;
  finished_at: string | null;
  status: "running" | "succeeded" | "failed";
  stats: Record<string, number> | null;
  error: string;
}

export interface PipelineStatus {
  exposures_pending: number;
  events_pending: number;
  exposures_total: number;
  events_total: number;
  assignments: number;
  measure_rows: number;
  measures_pending_backfill: number;
  interval_seconds: number;
  exposures_dropped?: number;
  runs: PipelineRun[];
}

export interface EventSummary {
  since: string;
  events: {
    name: string;
    total: number;
    units: number;
    days: { day: string; count: number; value: number }[];
    props: string[];
  }[];
}
