import type { UsageEvent, UsageSummary } from "../api";

export interface TokenCounts {
  input: number | null;
  output: number | null;
  cacheRead: number | null;
  cacheWrite: number | null;
}

function numeric(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
}

export function eventTokens(event: UsageEvent): TokenCounts {
  return {
    input: numeric(event.input ?? null),
    output: numeric(event.output ?? null),
    cacheRead: numeric(event.cacheRead ?? null),
    cacheWrite: numeric(event.cacheWrite ?? null),
  };
}

export type TotalDisplay =
  | { kind: "unknown" }
  | { kind: "complete"; value: number }
  | { kind: "partial"; value: number };

export function totalDisplay(value: number, known: number, requests: number): TotalDisplay {
  if (known <= 0) return { kind: "unknown" };
  if (known >= requests) return { kind: "complete", value };
  return { kind: "partial", value };
}

export interface SummaryHeadline {
  requests: number | null;
  knownRequests: number | null;
  unknownRequests: number | null;
  input: TotalDisplay;
  output: TotalDisplay;
  cacheRead: TotalDisplay;
  estimate: number | null;
  currency: string | null;
  estimateComplete: boolean;
  available: boolean;
}

export function headlineFromSummary(server: UsageSummary | null): SummaryHeadline {
  if (!server) {
    return {
      requests: null,
      knownRequests: null,
      unknownRequests: null,
      input: { kind: "unknown" },
      output: { kind: "unknown" },
      cacheRead: { kind: "unknown" },
      estimate: null,
      currency: null,
      estimateComplete: false,
      available: false,
    };
  }
  const requests = typeof server.requests === "number" ? server.requests : null;
  const knownRequests = typeof server.knownRequests === "number" ? server.knownRequests : null;
  const unknownRequests =
    typeof server.unknownRequests === "number"
      ? server.unknownRequests
      : requests !== null && knownRequests !== null
        ? Math.max(requests - knownRequests, 0)
        : null;
  const field = (key: "input" | "output" | "cacheRead"): TotalDisplay => {
    const partial = server.partialKnown?.[key];
    const known = typeof partial?.known === "number" ? partial.known : 0;
    const direct = server[key];
    const value = typeof direct === "number" ? direct : typeof partial?.sum === "number" ? partial.sum : null;
    if (value === null) return { kind: "unknown" };
    return totalDisplay(value, known, requests ?? 0);
  };
  const estimate = server.estimate;
  return {
    requests,
    knownRequests,
    unknownRequests,
    input: field("input"),
    output: field("output"),
    cacheRead: field("cacheRead"),
    estimate: estimate && typeof estimate.amount === "number" ? estimate.amount : null,
    currency: estimate && typeof estimate.currency === "string" ? estimate.currency : null,
    estimateComplete: Boolean(estimate?.complete),
    available: true,
  };
}

export interface UsageGroup {
  key: string;
  label: string;
  requests: number;
  input: TotalDisplay;
  output: TotalDisplay;
  unknown: number;
  volume: number;
}

export type GroupBy = "provider" | "session";

interface CountTally {
  sum: number;
  known: number;
}

function emptyTally(): CountTally {
  return { sum: 0, known: 0 };
}

function addCount(tally: CountTally, value: number | null): void {
  if (value === null) return;
  tally.sum += value;
  tally.known += 1;
}

export function groupEvents(events: UsageEvent[], by: GroupBy): UsageGroup[] {
  const groups = new Map<string, { requests: number; unknown: number; input: CountTally; output: CountTally }>();
  for (const event of events) {
    const key = by === "provider" ? event.providerId || "UNKNOWN" : event.sessionId || "UNKNOWN";
    const entry = groups.get(key) ?? { requests: 0, unknown: 0, input: emptyTally(), output: emptyTally() };
    const counts = eventTokens(event);
    entry.requests += 1;
    if (counts.input === null || counts.output === null) entry.unknown += 1;
    addCount(entry.input, counts.input);
    addCount(entry.output, counts.output);
    groups.set(key, entry);
  }
  return [...groups.entries()]
    .map(([key, entry]) => ({
      key,
      label: key,
      requests: entry.requests,
      unknown: entry.unknown,
      input: totalDisplay(entry.input.sum, entry.input.known, entry.requests),
      output: totalDisplay(entry.output.sum, entry.output.known, entry.requests),
      volume: entry.input.sum + entry.output.sum,
    }))
    .sort((a, b) => b.requests - a.requests);
}

export interface UsageHistogramBucket {
  label: string;
  events: number;
  input: TotalDisplay;
  output: TotalDisplay;
  total: TotalDisplay;
  volume: number;
}

export function histogram(events: UsageEvent[], days = 7): UsageHistogramBucket[] {
  const buckets = new Map<
    string,
    { events: number; input: CountTally; output: CountTally; complete: number; incomplete: number }
  >();
  const base = new Date();
  base.setUTCHours(0, 0, 0, 0);
  for (let offset = days - 1; offset >= 0; offset -= 1) {
    const date = new Date(base.getTime() - offset * 86_400_000);
    const key = date.toISOString().slice(0, 10);
    buckets.set(key, { events: 0, input: emptyTally(), output: emptyTally(), complete: 0, incomplete: 0 });
  }
  for (const event of events) {
    const date = new Date(event.at);
    if (Number.isNaN(date.getTime())) continue;
    const entry = buckets.get(date.toISOString().slice(0, 10));
    if (!entry) continue;
    const counts = eventTokens(event);
    entry.events += 1;
    addCount(entry.input, counts.input);
    addCount(entry.output, counts.output);
    if (counts.input !== null && counts.output !== null) entry.complete += 1;
    else entry.incomplete += 1;
  }
  return [...buckets.entries()].map(([key, entry]) => {
    const input = totalDisplay(entry.input.sum, entry.input.known, entry.events);
    const output = totalDisplay(entry.output.sum, entry.output.known, entry.events);
    const total =
      entry.input.known === 0 && entry.output.known === 0
        ? ({ kind: "unknown" } as TotalDisplay)
        : entry.complete === entry.events
          ? ({ kind: "complete", value: entry.input.sum + entry.output.sum } as TotalDisplay)
          : ({ kind: "partial", value: entry.input.sum + entry.output.sum } as TotalDisplay);
    return {
      label: key.slice(5),
      events: entry.events,
      input,
      output,
      total,
      volume: entry.input.sum + entry.output.sum,
    };
  });
}
