import type { Format, Status } from "./types";

export function fmtValue(v: number | null | undefined, format: Format, decimals: number): string {
  if (v === null || v === undefined || !isFinite(v)) return "—";
  switch (format) {
    case "percent":
      return `${(v * 100).toFixed(decimals)}%`;
    case "currency":
      return v.toLocaleString(undefined, { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
    default:
      return v.toLocaleString(undefined, { maximumFractionDigits: decimals });
  }
}

export function fmtPct(v: number | null | undefined, digits = 2, signed = true): string {
  if (v === null || v === undefined || !isFinite(v)) return "—";
  const s = (v * 100).toFixed(digits);
  return signed && v > 0 ? `+${s}%` : `${s}%`;
}

export function fmtInt(v: number | null | undefined): string {
  if (v === null || v === undefined) return "—";
  return v.toLocaleString();
}

export function fmtP(p: number | null | undefined): string {
  if (p === null || p === undefined || !isFinite(p)) return "—";
  if (p < 0.0001) return "<0.0001";
  return p.toFixed(4);
}

export function fmtDate(s: string | null | undefined): string {
  if (!s) return "—";
  return new Date(s).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export function fmtDateTime(s: string | null | undefined): string {
  if (!s) return "—";
  return new Date(s).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

export function ago(s: string | null | undefined): string {
  if (!s) return "never";
  const secs = (Date.now() - new Date(s).getTime()) / 1000;
  if (secs < 60) return "just now";
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export const trafficPct = (perMille: number) => `${(perMille / 10).toFixed(perMille % 10 ? 1 : 0)}%`;

export const statusLabel: Record<Status, string> = {
  draft: "Draft",
  in_review: "In review",
  approved: "Approved",
  rejected: "Rejected",
  active: "Running",
  paused: "Paused",
  stopped: "Stopped",
  launched: "Launched",
  archived: "Archived",
};

export const statusClass: Record<Status, string> = {
  draft: "",
  in_review: "b-warn",
  approved: "b-accent",
  rejected: "b-bad",
  active: "b-good",
  paused: "b-warn",
  stopped: "",
  launched: "b-accent",
  archived: "",
};

export const actionLabel: Record<string, string> = {
  submit: "Submit for review",
  withdraw: "Withdraw review",
  approve: "Approve",
  reject: "Reject",
  start: "Start",
  pause: "Pause",
  resume: "Resume",
  stop: "Stop",
  launch: "Launch a variant",
  archive: "Archive",
};

export function daysBetween(a: string, b: string): number {
  return Math.round((new Date(b).getTime() - new Date(a).getTime()) / 86400000) + 1;
}

export function isoDay(d: Date): string {
  return d.toISOString().slice(0, 10);
}
