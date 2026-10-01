import { useEffect, useState } from "react";
import { api } from "./api";
import type { DiversionDef } from "./types";

// Diversions are admin-managed; every page reads the same list, so adding
// one shows up in layer forms, experiments, diagnose and the guide.
const builtin: DiversionDef[] = [
  { key: "user_id", name: "User id", description: "", builtin: true, layers: 0, dedicated_layers: 0, created_at: "" },
  { key: "device_id", name: "Device id", description: "", builtin: true, layers: 0, dedicated_layers: 0, created_at: "" },
];
let cache: DiversionDef[] | null = null;
let inflight: Promise<DiversionDef[]> | null = null;
const listeners = new Set<(d: DiversionDef[]) => void>();

export function reloadDiversions(): Promise<DiversionDef[]> {
  inflight = api.diversions().then((d) => {
    cache = d;
    inflight = null;
    listeners.forEach((f) => f(d));
    return d;
  });
  inflight.catch(() => (inflight = null));
  return inflight;
}

export function useDiversions(): DiversionDef[] {
  const [list, setList] = useState<DiversionDef[]>(cache ?? builtin);
  useEffect(() => {
    listeners.add(setList);
    if (!cache && !inflight) reloadDiversions().catch(() => undefined);
    return () => {
      listeners.delete(setList);
    };
  }, []);
  return list;
}

export function diversionName(list: DiversionDef[], key: string): string {
  return list.find((d) => d.key === key)?.name ?? key;
}
