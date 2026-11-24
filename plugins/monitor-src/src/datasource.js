/**
 * 数据源：把插件接口读到的数据翻译成上游 bridge.js 认识的推送消息。
 *
 * 上游 panel-app 由 direct.js 直连 kap-server（WS 事件流 + REST 轮询）供数；
 * 桌面插件没有那条通路，这里改用核心的 metrics:read 动作：
 *   cache.summary   -> 官方额度状态（quota.state / source / note）
 *   cache.history   -> 24h / 7d / 30d 的日与小时桶（消耗图表、每日用量）
 *   cache.turns     -> 本会话逐轮 step（面板的输入/输出/缓存/速度；以 stepId 去重，
 *                      不用 cache.sessions.recent 做底数，避免与逐轮重放双算）
 *   quota.balances  -> 加油包与外部供应商余额
 * 实时状态来自作用域 activity：只有 turn/tool 生命周期，不含正文。
 *
 * 数字口径沿用上游与核心：
 * - 用量以 `turn.step.completed` 事件进面板，stepId 是去重键（上游按
 *   completedStepIds 去重，重放不会双算）；
 * - 读取失败不写 0：失败只是不推新数，面板保留上一次显示；
 * - 外部账户的 total/granted/paid 必须是数字（上游 .toFixed 会抛错），但只在
 *   有读数时给数字；失败卡片带 error，让上游走「沿用旧值 + 标年龄」分支。
 */

const REFRESH_MS = 60_000;
const TURNS_LIMIT = 120;

function num(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

/** cache.turns 的一行 -> 上游 turn.step.completed 的 payload。
 *  只带计数与模型名：这一层是「用量事件」，不是会话正文。 */
function stepEvent(step, sessionId, seq) {
  const output = num(step?.output) || 0;
  const decodeMS = num(step?.decodeMs) || 0;
  return {
    v: 1,
    type: "event",
    sessionId,
    event: {
      type: "turn.step.completed",
      session_id: sessionId,
      agent_id: step?.agent || "main",
      seq,
      // cache.turns 读取的是已落盘的历史 step 快照，标成重放只补本会话计数与折线，
      // 不会触发 bridge 把空闲面板切进「思考中」，也不重复累加到 kcm.daily.v2。
      kcmReplay: true,
      kcmSkipDaily: true,
      payload: {
        stepId: step?.stepId || "",
        model: step?.model || "",
        usage: {
          inputOther: num(step?.inputOther) || 0,
          inputCacheRead: num(step?.inputCacheRead) || 0,
          inputCacheCreation: num(step?.inputCacheCreation) || 0,
          output
        },
        llmTiming: decodeMS > 0 ? { llmServerDecodeMs: decodeMS } : undefined
      }
    }
  };
}

function bucketFromTotals(totals) {
  return {
    input: (num(totals.inputOther) || 0) + (num(totals.inputCacheRead) || 0) + (num(totals.inputCacheCreation) || 0),
    output: num(totals.output) || 0,
    cacheRead: num(totals.inputCacheRead) || 0
  };
}

/** cache.history 的分桶 -> usageDaily 的 daily/hourly。
 *  键要与 metrics.usageHourKey / usageDayKey 同形（本地时间），图表才认得。 */
function bucketsFromHistory(history) {
  const daily = {};
  const hourly = {};
  const unit = history?.unit === "hour" ? "hour" : "day";
  const points = Array.isArray(history?.points) ? history.points : [];
  const pad = (value) => String(value).padStart(2, "0");
  const addBucket = (current, bucket) => {
    current.input += bucket.input;
    current.output += bucket.output;
    current.cacheRead += bucket.cacheRead;
    if (bucket.sub) {
      const sub = current.sub || (current.sub = { input: 0, output: 0, cacheRead: 0 });
      sub.input += bucket.sub.input;
      sub.output += bucket.sub.output;
      sub.cacheRead += bucket.sub.cacheRead;
    }
  };
  const accumulate = (target, key, bucket) => {
    const current = target[key] || { input: 0, output: 0, cacheRead: 0 };
    addBucket(current, bucket);
    target[key] = current;
  };
  for (const point of points) {
    const totals = point?.totals;
    if (!totals) continue;
    // 桶口径与 metrics.addUsageToBucket 一致：input 记全部输入
    const bucket = bucketFromTotals(totals);
    if (point.sub) bucket.sub = bucketFromTotals(point.sub);
    const recorded = String(point.bucket || "");
    const hasTZ = /(?:Z|[+-]\d{2}:?\d{2})$/.test(recorded);
    const date = unit === "hour"
      ? new Date(hasTZ ? recorded : `${recorded}:00Z`)
      : new Date(`${recorded}T00:00`);
    if (Number.isNaN(date.getTime())) continue;
    const day = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    accumulate(daily, day, bucket);
    if (unit === "hour") accumulate(hourly, `${day}T${pad(date.getHours())}`, bucket);
  }
  return { daily, hourly };
}

function currencySymbol(unit) {
  const code = String(unit || "").trim().toUpperCase();
  if (code === "CNY" || code === "RMB" || code === "CENTS") return "¥";
  if (code === "USD") return "$";
  if (code === "EUR") return "€";
  if (code === "GBP") return "£";
  return unit || "¥";
}

function partAmount(parts, labels) {
  for (const part of Array.isArray(parts) ? parts : []) {
    if (labels.includes(part?.label) && num(part?.amount) != null) return num(part.amount);
  }
  return 0;
}

function externalFromBalances(state) {
  const providers = [];
  const unsupported = [];
  for (const card of Array.isArray(state?.cards) ? state.cards : []) {
    if (card?.status === "unsupported" || card?.status === "not_configured") {
      const reason = card.status === "not_configured"
        ? "未配置 API Key"
        : (Array.isArray(card?.notes) ? card.notes[0] || "" : "") || card?.errorReason || "";
      unsupported.push({
        id: card.providerId,
        name: card.providerName,
        reason
      });
      continue;
    }
    const records = Array.isArray(card?.records) ? card.records : [];
    const error = card?.errorReason || (card?.status === "stale" ? card?.staleReason : "") || "";
    const windows = records
      .filter((record) => (record?.kind === "coding_plan" || record?.kind === "windows") && num(record.used) != null && num(record.limit) > 0)
      .map((record) => ({
        label: record.windowLabel || "",
        pct: Math.min(100, Math.max(0, (num(record.used) / num(record.limit)) * 100)),
        resetAt: record.resetAt || ""
      }));
    const balance = records.find((record) => (record?.kind === "api_balance" || record?.kind === "balance") && num(record.remaining) != null);
    const cents = balance?.unit === "cents";
    const currency = balance ? currencySymbol(balance.unit) : "";
    const amount = balance ? num(balance.remaining) : null;
    const observedMs = Date.parse(card?.observedAt || "");
    providers.push({
      id: card.providerId,
      name: card.providerName,
      // 上游按 key 尾号区分同供应商多账户；这里是密钥散列后的尾号，不是密钥本身
      keyTail: card.accountTag || "",
      kind: balance ? "balance" : "plan",
      total: amount == null ? 0 : (cents ? amount / 100 : amount),
      granted: partAmount(card?.parts, ["赠金", "代金券"]),
      paid: partAmount(card?.parts, ["充值", "现金"]),
      currency,
      windows,
      plan: card.plan || "",
      consoleUrl: card.consoleUrl || card.officialPage || "",
      error,
      fetchedAt: Number.isFinite(observedMs) && observedMs > 0 ? observedMs : 0
    });
  }
  return { providers, unsupported };
}

/** quota.balances 的 Kimi Code 订阅 -> 上游 quota 消息。
 *  窗口缺一个就只发一个：上游按 limit5h / limit7d 分别判断。 */
function quotaFromBalances(state) {
  const code = state?.kimiCode;
  const quota = {};
  for (const window of Array.isArray(code?.windows) ? code.windows : []) {
    const ratio = num(window?.usedRatio);
    if (ratio == null) continue;
    const entry = { usedRatio: Math.min(1, Math.max(0, ratio)), resetAt: window?.resetAt || "" };
    const key = String(window?.key || "").toLowerCase();
    if (key.includes("5h") || key.includes("five")) quota.limit5h = entry;
    else if (key.includes("week") || key.includes("7d")) quota.limit7d = entry;
    else if (key.includes("month")) quota.limitMonth = entry;
  }
  const wallet = code?.wallet;
  let walletFetchedAt = "";
  if (wallet && num(wallet.balanceCents) != null) {
    quota.wallet = {
      balanceCents: num(wallet.balanceCents),
      totalCents: num(wallet.totalCents),
      currency: wallet.currency || "CNY"
    };
    walletFetchedAt = code?.observedAt || "";
  }
  return { quota, walletFetchedAt };
}

/** 官方额度状态 -> 面板顶部状态灯。UNKNOWN 保持 idle 而不是报警：
 *  读不到额度不代表模型不可用。 */
function statusFromSummary(summary) {
  const state = summary?.quota?.state;
  if (state === "EXHAUSTED") return "waiting";
  return "idle";
}

/**
 * 启动数据源。`push` 是 bridge.js 的 `__kcm.push`：装配顺序要求 bridge 先就绪。
 * getSessionId 由 bridge 提供，切会话后事件才能归到正确的会话。
 * `shouldPollBalance` 每轮判断余额模块是否可见：模块收起后核心不再被要求
 * 去读供应商（上游语义），但额度窗口仍随缓存摘要更新。
 */
export function startDatasource(ctx, push, getSessionId, shouldPollBalance = () => true) {
  let disposed = false;
  let timer = null;
  let followUpTimer = null;
  let cooldownTimer = null;
  let inFlight = null;
  let pendingRefresh = false;
  let pendingIncludeBalances = false;
  let seenSeq = 0;
  let currentSession = "";
  let activeTurnSession = "";
  // Every step already pushed for the current session. A refresh re-reads the
  // same cache.turns page, so replaying it would re-send up to 120 steps each
  // minute and re-render the whole panel for data it already had.
  const pushedStepIds = new Set();
  // A forced refresh (manual, or an account switch) must not be swallowed by the
  // "nothing moved" short circuit below.
  let forceNext = false;
  // Every account switch starts a new generation. A refresh that was already in
  // flight belongs to the previous account, and its quota, wallet and provider
  // numbers must not be painted over the cleared panel when it finally lands.
  let accountGeneration = 0;

  const send = (message) => {
    if (!disposed) push(message);
  };

  async function request(action, params) {
    try {
      return await ctx.core.request(action, params);
    } catch (error) {
      // 失败只是这一项没有新数据，不上报 0
      ctx.log("warn", `${action} 读取失败：${error?.message || error}`);
      return null;
    }
  }

  /** 切会话：上游以 session 消息为准，会话 id 一变就重置本地面板数值。 */
  function enterSession(sessionId, busy, updatedAt) {
    if (!sessionId || sessionId === currentSession) return;
    currentSession = sessionId;
    // Step ids belong to one session's transcript; another session's ids are a
    // different space, so the seen set must not suppress its first steps.
    pushedStepIds.clear();
    send({
      v: 1,
      type: "session",
      sid: sessionId,
      snapshot: { busy: Boolean(busy), updatedAt: updatedAt || "" }
    });
  }

  /** 把本会话尚未推送过的 step 逐条推成 turn.step.completed。seq 单调递增，
   *  上游也按 stepId 去重；这里先过滤一次，避免每分钟把同一页重放一遍。 */
  function pushSteps(steps, sessionId) {
    for (const step of Array.isArray(steps) ? steps : []) {
      if (!step?.stepId) continue;
      if (pushedStepIds.has(step.stepId)) continue;
      pushedStepIds.add(step.stepId);
      seenSeq += 1;
      send(stepEvent(step, sessionId, seenSeq));
    }
  }

  async function refresh(includeBalances = true) {
    const generation = accountGeneration;
    const forced = forceNext;
    forceNext = false;
    const sessionId = getSessionId();
    const [summary, history24h, history30d, turns, balances] = await Promise.all([
      request("cache.summary"),
      request("cache.history", { range: "24h" }),
      request("cache.history", { range: "30d" }),
      // 没有焦点会话时退回 'panel'（上游默认值），逐轮读取跳过：宁可不更新，
      // 也不拿别的会话的数据顶替
      sessionId && sessionId !== "panel" ? request("cache.turns", { id: sessionId, limit: TURNS_LIMIT }) : Promise.resolve(null),
      includeBalances && shouldPollBalance() ? request("quota.balances", { refresh: true }) : Promise.resolve(null)
    ]);
    // The account changed while these requests were running. Everything in this
    // page belongs to the previous account, including the session it was read
    // for, so the whole page is dropped rather than partially applied.
    if (disposed || generation !== accountGeneration) return null;

    if (summary) {
      const nextStatus = statusFromSummary(summary);
      if (forced || activeTurnSession !== sessionId || nextStatus !== "idle") {
        send({ v: 1, type: "status", sessionId, status: nextStatus });
      }
      const quota = summary.quota;
      if (quota?.state === "AVAILABLE" || quota?.state === "EXHAUSTED") {
        // 官方 /oauth/usage 的窗口是缓存页同源读数；没有窗口就不发，避免清空
        const usages = quota?.value?.quota?.usages;
        if (usages && typeof usages === "object") {
          const windows = {};
          for (const [key, window] of Object.entries(usages)) {
            const ratio = num(window?.usedRatio);
            if (ratio == null) continue;
            const entry = { usedRatio: Math.min(1, Math.max(0, ratio)), resetAt: window?.resetAt || "" };
            const lower = String(key || "").toLowerCase();
            if (lower.includes("5h") || lower.includes("five")) windows.limit5h = entry;
            else if (lower.includes("week") || lower.includes("7d")) windows.limit7d = entry;
            else if (lower.includes("month")) windows.limitMonth = entry;
            else windows[key] = entry;
          }
          if (Object.keys(windows).length) send({ v: 1, type: "quota", quota: windows });
        }
      }
    }
    if (history24h || history30d) {
      const msg = {
        v: 1,
        type: "usageDaily",
        connected: true
      };
      const h24 = history24h ? bucketsFromHistory(history24h) : null;
      if (history30d) {
        const daily = bucketsFromHistory(history30d).daily;
        if (h24) {
          const now = new Date();
          const pad = (value) => String(value).padStart(2, "0");
          const localToday = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
          if (h24.daily[localToday]) daily[localToday] = h24.daily[localToday];
        }
        msg.daily = daily;
        msg.authoritative = true;
      }
      if (h24) {
        msg.hourly = h24.hourly;
      }
      send(msg);
    }
    if (sessionId && sessionId !== "panel") {
      enterSession(sessionId, activeTurnSession === sessionId, Array.isArray(turns) ? turns.at(-1)?.at || "" : "");
    }
    if (Array.isArray(turns) && sessionId) {
      pushSteps(turns, sessionId);
    }
    if (balances) {
      const quota = quotaFromBalances(balances);
      if (Object.keys(quota.quota).length) {
        send({ v: 1, type: "quota", quota: quota.quota, walletFetchedAt: quota.walletFetchedAt || undefined });
      }
      const external = externalFromBalances(balances);
      if (external.providers.length || external.unsupported.length) {
        send({ v: 1, type: "external", providers: external.providers, unsupported: external.unsupported });
      }
    }
    return true;
  }

  function schedule() {
    if (disposed) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      void run(true);
    }, REFRESH_MS);
  }

  async function run(includeBalances = true, options = {}) {
    if (disposed) return null;
    if (inFlight && !options.supersede) {
      pendingRefresh = true;
      if (includeBalances) pendingIncludeBalances = true;
      return inFlight;
    }
    // A superseding run orphans the promise it replaces: that promise still
    // settles, but its finally must not clear or reschedule this newer one.
    const previous = inFlight;
    const current = refresh(includeBalances)
      .catch((error) => {
        ctx.log("warn", `面板刷新失败：${error?.message || error}`);
        return null;
      })
      .finally(() => {
        if (inFlight !== current) return;
        inFlight = null;
        if (!disposed && pendingRefresh) {
          const nextBalances = pendingIncludeBalances;
          pendingRefresh = false;
          pendingIncludeBalances = false;
          void run(nextBalances);
          return;
        }
        schedule();
      });
    inFlight = current;
    void previous?.catch(() => {});
    return current;
  }

  function scheduleFollowUpLocal() {
    if (disposed) return;
    if (followUpTimer) clearTimeout(followUpTimer);
    followUpTimer = setTimeout(() => {
      followUpTimer = null;
      if (!disposed) void run(false);
    }, 600);
    // cache.Store 强制 5s 扫描冷却；若 turn.ended 落在冷却窗口内，补一次 5.6s 后的续扫
    if (cooldownTimer) clearTimeout(cooldownTimer);
    cooldownTimer = setTimeout(() => {
      cooldownTimer = null;
      if (!disposed) void run(false);
    }, 5_600);
  }

  return {
    /** 首次装载：拉一次全量（含余额）。 */
    start() {
      return run(true);
    },
    /** 余额模块刚展开：立刻补读一次，不等下一个 60 秒周期。 */
    refreshBalance() {
      if (disposed || !shouldPollBalance()) return;
      const generation = accountGeneration;
      void request("quota.balances", { refresh: true }).then((balances) => {
        // Same rule as a full refresh: a page read for the previous account is
        // dropped instead of being written over the panel's cleared numbers.
        if (disposed || !balances || generation !== accountGeneration) return;
        const quota = quotaFromBalances(balances);
        if (Object.keys(quota.quota).length) {
          send({ v: 1, type: "quota", quota: quota.quota, walletFetchedAt: quota.walletFetchedAt || undefined });
        }
        const external = externalFromBalances(balances);
        if (external.providers.length || external.unsupported.length) {
          send({ v: 1, type: "external", providers: external.providers, unsupported: external.unsupported });
        }
      });
    },
    /** 标题行的手动刷新：立刻重读一次，不等 60s。 */
    refresh() {
      if (disposed) return Promise.resolve(null);
      if (timer) clearTimeout(timer);
      timer = null;
      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = null;
      return run(true);
    },
    /**
     * 切换账号后立刻重读。上一账号的额度与余额属于另一个人，保留它们会让
     * 面板继续显示旧数字；先清空快照与逐轮去重表，再绕过 Mini 模式的跳过
     * 判断强制刷新一次。
     */
    resetForAccountSwitch() {
      if (disposed) return Promise.resolve(null);
      // Bumping the generation orphans every response already in flight; the
      // refresh started below is the only one whose page may be applied.
      accountGeneration += 1;
      pushedStepIds.clear();
      seenSeq = 0;
      currentSession = "";
      activeTurnSession = "";
      forceNext = true;
      pendingRefresh = false;
      pendingIncludeBalances = false;
      if (timer) clearTimeout(timer);
      timer = null;
      if (followUpTimer) clearTimeout(followUpTimer);
      followUpTimer = null;
      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = null;
      // The in-flight promise keeps running and is awaited by its own finally,
      // but it belongs to the retired generation: a superseding run starts a
      // fresh refresh instead of queueing behind it, so the panel does not sit
      // on the cleared state until the previous account's request returns.
      return run(true, { supersede: true });
    },
    /** 宿主焦点会话切换：立即切会话并补读本地指标，不重复轮询外部余额。 */
    syncSession() {
      if (disposed) return Promise.resolve(null);
      const sessionId = getSessionId();
      if (sessionId && sessionId !== "panel") {
        enterSession(sessionId, activeTurnSession === sessionId, "");
      }
      if (timer) clearTimeout(timer);
      timer = null;
      return run(false);
    },
    /** 订阅 activity 作用域：只转发上游认识的生命周期事件。 */
    watchActivity() {
      if (disposed) return null;
      return ctx.events.on("activity", (payload) => {
        if (disposed || !payload || typeof payload !== "object") return;
        const event = payload;
        if (event.type === "turn.started" && event.session_id) activeTurnSession = event.session_id;
        else if ((event.type === "turn.ended" || event.type === "turn.completed") && event.session_id === activeTurnSession) activeTurnSession = "";
        if (event.session_id) enterSession(event.session_id, event.type === "turn.started", "");
        seenSeq += 1;
        send({
          v: 1,
          type: "event",
          sessionId: event.session_id || undefined,
          event: { ...event, seq: seenSeq }
        });
        if (event.type === "turn.ended" || event.type === "turn.completed") {
          if (timer) clearTimeout(timer);
          timer = null;
          void run(false);
          scheduleFollowUpLocal();
        }
      });
    },
    dispose() {
      disposed = true;
      if (timer) clearTimeout(timer);
      timer = null;
      if (followUpTimer) clearTimeout(followUpTimer);
      followUpTimer = null;
      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = null;
    }
  };
}
