import type { TuningAlgorithm } from "./types";

export const algorithms: { key: TuningAlgorithm; name: string; short: string; long: string }[] = [
  {
    key: "random",
    name: "Random sampling",
    short: "Uniform random points",
    long: "Each round tries fresh uniformly random points. Pure exploration — a baseline, and fine for a quick look at a large space.",
  },
  {
    key: "quasi_random",
    name: "Quasi-random sampling",
    short: "Even coverage (Halton)",
    long: "Points from a scrambled Halton sequence, continuing across rounds, so the space is covered evenly with no clumps or gaps. Ignores results.",
  },
  {
    key: "bayesian",
    name: "Bayesian (GP)",
    short: "Learns from every round",
    long: "A Gaussian process models the objective's lift over the whole space from every result so far. Each round picks the batch with the highest expected improvement — exploring where it's unsure, exploiting where it looks good. Starts with quasi-random points.",
  },
  {
    key: "constrained",
    name: "Constrained search",
    short: "Best within guardrails",
    long: "Like Bayesian, plus a model per guardrail: candidates maximise expected improvement × the probability every guardrail holds, inside a trust region around the best feasible point that grows after a better round and shrinks otherwise.",
  },
];

export const algorithmName = (k: string) => algorithms.find((a) => a.key === k)?.name ?? k;

export const sourceLabel: Record<string, string> = {
  control: "control",
  best_so_far: "best so far",
  warm_up: "warm-up",
  random: "random",
  quasi_random: "quasi-random",
  bayesian: "model",
  constrained: "model",
};
