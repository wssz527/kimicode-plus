import assert from "node:assert/strict";
import { test } from "node:test";
import {
  catalogRetrievedAt,
  isModelsDevCatalog,
  providerKeys,
  summarizeModelsDev,
} from "../src/lib/modelsdev.js";
import { hasChanges, renderPreview } from "../src/lib/previewChanges.js";
import {
  eventTokens,
  groupEvents,
  headlineFromSummary,
  histogram,
  totalDisplay,
} from "../src/lib/usage.js";
import type { ModelPreview, UsageEvent } from "../src/api.js";

const catalog = {
  source: "https://models.dev/api.json",
  retrieved_at: "2026-11-10T08:12:17.608929+00:00",
  providers: {
    deepseek: {
      id: "deepseek",
      name: "DeepSeek",
      models: {
        "deepseek-flash": { id: "deepseek-flash", name: "DeepSeek V4.1 Flash" },
        "deepseek-unknown": { id: "deepseek-unknown", name: "Unknown limits" },
      },
    },
    anthropic: {
      id: "anthropic",
      name: "Anthropic",
      models: { "claude-opus-5-5": { id: "claude-opus-5-5", name: "Claude Opus 5.5" } },
    },
  },
};

// 与 Go internal/registry/service.go 的 PreviewChange 同形。
const goPreview: ModelPreview = {
  revision: 7,
  summary: { created: 1, updated: 1, unchanged: 1 },
  changes: [
    {
      providerId: "deepseek",
      id: "deepseek-new",
      name: "DeepSeek New",
      state: "created",
      fields: [
        {
          field: "context",
          before: { value: null, source: "UNKNOWN", fetchedAt: null },
          after: { value: 131072, source: "modelsdev", fetchedAt: "2026-11-10T08:12:17Z" },
        },
      ],
      after: {
        providerId: "deepseek",
        id: "deepseek-new",
        name: "DeepSeek New",
        capabilities: {
          context: { value: 131072, source: "modelsdev", fetchedAt: "2026-11-10T08:12:17Z" },
        },
      },
    },
    {
      providerId: "deepseek",
      id: "deepseek-flash",
      name: "DeepSeek V4.1 Flash",
      state: "updated",
      fields: [
        {
          field: "output",
          before: { value: 8192, source: "registry", fetchedAt: "2026-11-01T00:00:00Z" },
          after: { value: 8192, source: "modelsdev", fetchedAt: "2026-11-10T08:12:17Z" },
        },
      ],
      after: {
        providerId: "deepseek",
        id: "deepseek-flash",
        name: "DeepSeek V4.1 Flash",
        capabilities: {
          output: { value: 8192, source: "modelsdev", fetchedAt: "2026-11-10T08:12:17Z" },
        },
      },
    },
  ],
};

test("catalog provenance comes from retrieved_at and is never fabricated", () => {
  assert.equal(catalogRetrievedAt(catalog), "2026-11-10T08:12:17.608Z");
  assert.equal(catalogRetrievedAt({ providers: catalog.providers }), null);
  assert.equal(catalogRetrievedAt({ ...catalog, retrieved_at: "not-a-date" }), null);
});

test("root provider map with metadata keys is accepted", () => {
  const rootMap = {
    retrieved_at: "2026-11-10T08:12:17.608Z",
    deepseek: catalog.providers.deepseek,
    anthropic: catalog.providers.anthropic,
  };
  assert.equal(isModelsDevCatalog(rootMap), true);
  assert.deepEqual(providerKeys(rootMap), ["deepseek", "anthropic"]);
  assert.equal(summarizeModelsDev(rootMap).providers, 2);

  // 纯元数据、数组、垃圾仍拒绝
  assert.equal(isModelsDevCatalog({ retrieved_at: "x", source: "y" }), false);
  assert.equal(isModelsDevCatalog([]), false);
  assert.equal(isModelsDevCatalog({}), false);
  assert.equal(isModelsDevCatalog({ deepseek: "not-an-object" }), false);
  assert.equal(isModelsDevCatalog({ providers: [] }), false);
  assert.equal(isModelsDevCatalog(catalog), true);
});

test("summaries and provider keys read the provider map", () => {
  assert.deepEqual(summarizeModelsDev(catalog), {
    providers: 2,
    models: 3,
    providerNames: ["DeepSeek", "Anthropic"],
  });
  assert.deepEqual(providerKeys(catalog), ["deepseek", "anthropic"]);
  assert.deepEqual(summarizeModelsDev(null), { providers: 0, models: 0, providerNames: [] });
});

test("Go preview changes render with before/after provenance", () => {
  const rendered = renderPreview(goPreview);
  assert.equal(rendered.revision, 7);
  assert.equal(rendered.changes.length, 2);
  assert.deepEqual(rendered.summary, { created: 1, updated: 1, unchanged: 1 });

  const created = rendered.changes[0];
  assert.ok(created);
  assert.equal(created.key, "deepseek/deepseek-new");
  assert.equal(created.stateLabel, "新增");
  assert.equal(created.fields[0]?.before, "UNKNOWN");
  assert.equal(created.fields[0]?.after, "131072");
  assert.equal(created.fields[0]?.beforeSource, "UNKNOWN");
  assert.equal(created.fields[0]?.afterSource, "modelsdev");

  const updated = rendered.changes[1];
  assert.ok(updated);
  assert.equal(updated.state, "updated");
  // 值相同但 provenance 变化仍然是变化
  assert.equal(updated.fields[0]?.before, "8192");
  assert.equal(updated.fields[0]?.after, "8192");
  assert.equal(updated.fields[0]?.changed, true);
});

test("preview never invents removed models or overrides", () => {
  const rendered = renderPreview(goPreview);
  const keys = rendered.changes.map((change) => change.key);
  // Go 只返回批次内实际差异；未包含的模型不会出现在这里
  assert.deepEqual(keys, ["deepseek/deepseek-new", "deepseek/deepseek-flash"]);
  assert.equal(rendered.changes.some((change) => change.state === "removed"), false);
});

test("empty Go preview reports no effective change", () => {
  const empty = renderPreview({ revision: 3, changes: [], summary: { created: 0, updated: 0, unchanged: 2 } });
  assert.equal(hasChanges({ revision: 3, changes: [], summary: { created: 0, updated: 0, unchanged: 2 } }), false);
  assert.equal(empty.changes.length, 0);
  assert.equal(empty.summary.unchanged, 2);
  assert.equal(empty.revision, 3);
});

function event(overrides: Partial<UsageEvent> = {}): UsageEvent {
  return {
    providerId: "deepseek",
    model: "deepseek-flash",
    requestId: "r1",
    attemptId: "0",
    sessionId: "s1",
    projectId: "p1",
    agentId: "a1",
    role: "main",
    protocol: "openai",
    source: "TEST",
    at: new Date().toISOString().slice(0, 10) + "T00:00:00.000Z",
    raw: { prompt_tokens: 1_000_000, completion_tokens: 1_000_000 },
    price: {
      input: 1,
      output: 2,
      cacheRead: null,
      cacheWrite: null,
      currency: "USD",
      source: "test",
      fetchedAt: "2026-11-10T00:00:00Z",
    },
    ...overrides,
  };
}

test("event counts come only from Go normalized fields", () => {
  // 只有 raw 的旧协议事件不再被浏览器解析
  const rawOnly = event({ raw: { prompt_tokens: 10, completion_tokens: 5 } });
  delete (rawOnly as { input?: number | null }).input;
  delete (rawOnly as { output?: number | null }).output;
  assert.deepEqual(eventTokens(rawOnly), { input: null, output: null, cacheRead: null, cacheWrite: null });

  const normalized = event({
    input: 1234,
    output: 56,
    cacheRead: 100,
    cacheWrite: 7,
    raw: { prompt_tokens: 999_999, completion_tokens: 999_999 },
  });
  assert.deepEqual(eventTokens(normalized), { input: 1234, output: 56, cacheRead: 100, cacheWrite: 7 });
});

test("grouping keeps request and token counts but no money", () => {
  const events = [
    event({ providerId: "a", sessionId: "s1", input: 10, output: 2 }),
    event({ providerId: "a", sessionId: "s2", input: 20, output: 3 }),
    event({ providerId: "b", sessionId: "s1" }),
  ];
  const byProvider = groupEvents(events, "provider");
  assert.equal(byProvider.length, 2);
  assert.equal(byProvider[0]?.key, "a");
  assert.equal(byProvider[0]?.requests, 2);
  assert.deepEqual(byProvider[0]?.input, { kind: "complete", value: 30 });
  assert.deepEqual(byProvider[0]?.output, { kind: "complete", value: 5 });
  assert.equal("estimate" in (byProvider[0] ?? {}), false);
  const bySession = groupEvents(events, "session");
  assert.equal(bySession.find((group) => group.key === "s1")?.unknown, 1);
});

function summaryWith(overrides: Record<string, unknown> = {}) {
  return {
    requests: 0,
    knownRequests: 0,
    unknownRequests: 0,
    input: null,
    output: null,
    cacheRead: null,
    cacheWrite: null,
    partialKnown: {
      input: { sum: 0, known: 0, total: 0, complete: false },
      output: { sum: 0, known: 0, total: 0, complete: false },
      cacheRead: { sum: 0, known: 0, total: 0, complete: false },
      cacheWrite: { sum: 0, known: 0, total: 0, complete: false },
    },
    estimate: {
      kind: "UNKNOWN",
      amount: null,
      currency: null,
      currencies: [],
      complete: false,
      pricedEvents: 0,
      unpricedEvents: 0,
      unknownUsageEvents: 0,
      partialKnown: false,
      priceSources: [],
      priceFetchedAt: null,
    },
    actual: { kind: "UNKNOWN", amount: null, currency: null, currencies: [] },
    subscription: { kind: "UNKNOWN", remaining: null, resetAt: null, source: null, observedAt: null, note: "" },
    ...overrides,
  };
}

test("empty ledger shows UNKNOWN, never >=0", () => {
  const headline = headlineFromSummary(summaryWith() as never);
  assert.deepEqual(headline.input, { kind: "unknown" });
  assert.deepEqual(headline.output, { kind: "unknown" });
  assert.equal(headline.requests, 0);
  assert.equal(headline.available, true);
  assert.equal(headline.estimate, null);
  // 没有任何已知计数时不能展示为 0
  assert.deepEqual(totalDisplay(0, 0, 0), { kind: "unknown" });
});

test("all-unknown events keep UNKNOWN even when requests > 0", () => {
  const unknown = event({ requestId: "u1" });
  const eventList = [unknown, { ...unknown, requestId: "u2" }];
  assert.deepEqual(
    eventList.map((item) => eventTokens(item)),
    [
      { input: null, output: null, cacheRead: null, cacheWrite: null },
      { input: null, output: null, cacheRead: null, cacheWrite: null },
    ],
  );
  assert.deepEqual(totalDisplay(0, 0, eventList.length), { kind: "unknown" });

  const headline = headlineFromSummary(
    summaryWith({
      requests: 2,
      knownRequests: 0,
      unknownRequests: 2,
      partialKnown: {
        input: { sum: 0, known: 0, total: 2, complete: false },
        output: { sum: 0, known: 0, total: 2, complete: false },
        cacheRead: { sum: 0, known: 0, total: 2, complete: false },
        cacheWrite: { sum: 0, known: 0, total: 2, complete: false },
      },
    }) as never,
  );
  assert.deepEqual(headline.input, { kind: "unknown" });
});

test("partially known sums show >= and a real zero stays zero when fully known", () => {
  assert.deepEqual(totalDisplay(0, 1, 1), { kind: "complete", value: 0 });
  assert.deepEqual(totalDisplay(0, 1, 2), { kind: "partial", value: 0 });

  const headline = headlineFromSummary(
    summaryWith({
      requests: 2,
      knownRequests: 1,
      unknownRequests: 1,
      partialKnown: {
        input: { sum: 0, known: 1, total: 2, complete: false },
        output: { sum: 0, known: 1, total: 2, complete: false },
        cacheRead: { sum: 0, known: 0, total: 2, complete: false },
        cacheWrite: { sum: 0, known: 0, total: 2, complete: false },
      },
    }) as never,
  );
  assert.deepEqual(headline.input, { kind: "partial", value: 0 });
  assert.deepEqual(headline.cacheRead, { kind: "unknown" });

  const completeZero = headlineFromSummary(
    summaryWith({
      requests: 1,
      knownRequests: 1,
      input: 0,
      output: 0,
      partialKnown: {
        input: { sum: 0, known: 1, total: 1, complete: true },
        output: { sum: 0, known: 1, total: 1, complete: true },
        cacheRead: { sum: 0, known: 1, total: 1, complete: true },
        cacheWrite: { sum: 0, known: 0, total: 1, complete: false },
      },
    }) as never,
  );
  assert.deepEqual(completeZero.input, { kind: "complete", value: 0 });
  assert.deepEqual(completeZero.output, { kind: "complete", value: 0 });
});

test("cost comes only from the Go summary estimate", () => {
  const authoritative = headlineFromSummary(
    summaryWith({
      requests: 2,
      knownRequests: 2,
      estimate: {
        kind: "ESTIMATE",
        amount: 0.0066525,
        currency: "USD",
        currencies: ["USD"],
        complete: true,
        pricedEvents: 2,
        unpricedEvents: 0,
        unknownUsageEvents: 0,
        partialKnown: false,
        priceSources: ["fixture"],
        priceFetchedAt: "2026-11-10T00:00:00Z",
      },
    }) as never,
  );
  assert.equal(authoritative.estimate, 0.0066525);
  assert.equal(authoritative.currency, "USD");
  assert.equal(authoritative.estimateComplete, true);
});

test("missing summary is unavailable instead of a fake zero", () => {
  const headline = headlineFromSummary(null);
  assert.equal(headline.available, false);
  assert.equal(headline.requests, null);
  assert.deepEqual(headline.input, { kind: "unknown" });
});

test("group keeps per-field coverage: all unknown vs only output known", () => {
  const google = event({ providerId: "google", requestId: "g1", input: null, output: null });
  const anthropicPartial = event({
    providerId: "anthropic",
    requestId: "a1",
    input: null,
    output: 160,
  });
  const groups = groupEvents([google, anthropicPartial], "provider");

  const googleGroup = groups.find((group) => group.key === "google");
  assert.ok(googleGroup);
  assert.deepEqual(googleGroup.input, { kind: "unknown" });
  assert.deepEqual(googleGroup.output, { kind: "unknown" });
  assert.equal(googleGroup.volume, 0);

  const anthropicGroup = groups.find((group) => group.key === "anthropic");
  assert.ok(anthropicGroup);
  // 输出全知且只有一个事件时是精确值；输入没有任何已知值，不能显示 0
  assert.deepEqual(anthropicGroup.output, { kind: "complete", value: 160 });
  assert.deepEqual(anthropicGroup.input, { kind: "unknown" });
  assert.equal(anthropicGroup.unknown, 1);
});

test("partially known group sums show >=", () => {
  const events = [
    event({ providerId: "openai", requestId: "o1", input: 100, output: 10 }),
    event({ providerId: "openai", requestId: "o2", input: null, output: 20 }),
  ];
  const group = groupEvents(events, "provider").find((item) => item.key === "openai");
  assert.ok(group);
  assert.deepEqual(group.input, { kind: "partial", value: 100 });
  assert.deepEqual(group.output, { kind: "complete", value: 30 });
  assert.equal(group.volume, 130);
});

test("histogram buckets recent days and ignores invalid timestamps", () => {
  const buckets = histogram([event({ input: 10, output: 5 }), event({ at: "not-a-date" })], 3);
  assert.equal(buckets.length, 3);
  assert.equal(buckets.reduce((sum, bucket) => sum + bucket.events, 0), 1);
  const filled = buckets.find((bucket) => bucket.events > 0);
  assert.ok(filled);
  assert.deepEqual(filled.input, { kind: "complete", value: 10 });
  assert.deepEqual(filled.output, { kind: "complete", value: 5 });
  assert.deepEqual(filled.total, { kind: "complete", value: 15 });
  const empty = buckets.find((bucket) => bucket.events === 0);
  assert.ok(empty);
  assert.deepEqual(empty.total, { kind: "unknown" });
});

test("histogram does not treat unknown counts as zero", () => {
  const buckets = histogram([event({ input: null, output: null })], 2);
  const filled = buckets.find((bucket) => bucket.events > 0);
  assert.ok(filled);
  assert.deepEqual(filled.total, { kind: "unknown" });
  assert.deepEqual(filled.input, { kind: "unknown" });

  const mixed = histogram(
    [event({ requestId: "h1", input: 40, output: 2 }), event({ requestId: "h2", input: null, output: null })],
    1,
  );
  const bucket = mixed[0];
  assert.ok(bucket);
  assert.deepEqual(bucket.input, { kind: "partial", value: 40 });
  assert.deepEqual(bucket.total, { kind: "partial", value: 42 });
});
