// 思考内容翻译（KimiCode+ 适配插件）
//
// 移植自独立版 kimi-cot-translate 的渲染端逻辑（MIT，见同目录 LICENSE）。
// 与独立版的差异只在宿主接入方式：
//   - 思考块由 ctx.ui.decorate("thinking") 提供，不再自带 Observer；
//   - 翻译请求走 ctx.net.fetch，不再直接 fetch；
//   - 设置与缓存走 ctx.storage，不再用 localStorage；
//   - 自定义服务的密钥走凭据库注入，插件从不持有明文。

// 键名与迁移服务写入的 settings.v1 保持一致，否则迁移过来的设置读不到。
const SETTINGS_KEY = "settings.v1";
const CACHE_INDEX_KEY = "cache.index.v1";
const CACHE_ENTRY_PREFIX = "cache.e.v1:";
// 存储值是单个键的 32 KiB 上限，因此缓存按条存放，索引单独一条。
const CACHE_LIMIT = 200;
const CACHE_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_CHARS = 1500;
const BATCH_CHARS = 4000;
const CONCURRENCY = 3;
const STREAM_DEBOUNCE_MS = 700;
const STREAM_MAX_WAIT_MS = 3000;

const TARGET_LANGUAGES = ["zh", "en", "ja", "ko", "es", "fr", "de", "ru"];
const AUTO = "__auto__";
const PROVIDERS = ["bing", "google", "custom"];

const DEFAULTS = {
  targetLanguage: AUTO,
  provider: "bing",
  autoTranslate: true,
  customEndpoint: "",
  customModel: "",
  useCredential: true,
};

export async function activate(ctx) {
  const engine = await createEngine(ctx);
  const style = injectStyles();
  const blocks = new Set();

  function current() {
    return engine.settings;
  }

  async function reload() {
    await engine.loadSettings();
    for (const state of blocks) schedule(state, true);
  }

  function onSettingsChanged() {
    void reload();
  }
  function onCacheCleared() {
    engine.clearMemoryCache();
  }
  window.addEventListener("ekdp-kct:settings", onSettingsChanged);
  window.addEventListener("ekdp-kct:cache", onCacheCleared);

  const decoration = ctx.ui.decorate("thinking", (block, info) => {
    const head = block.querySelector("button.think-head");
    const body = block.querySelector(".think-body-inner") ?? block.querySelector(".think-body");
    // 没有头部或正文的块不是思考块，宿主可能只是复用了类名。
    if (!head || !body) return;

    const bar = document.createElement("div");
    bar.className = "ekdp-kct-bar";
    bar.dataset.ekdpOwned = "1";

    const status = document.createElement("button");
    status.type = "button";
    status.className = "ekdp-kct-btn ekdp-kct-status";
    status.setAttribute("aria-live", "polite");
    status.disabled = true;

    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "ekdp-kct-btn";
    toggle.textContent = "翻译";
    toggle.title = "翻译这段思考";
    toggle.setAttribute("aria-label", "翻译这段思考");
    toggle.setAttribute("aria-pressed", "false");

    bar.append(status, toggle);
    head.after(bar);

    const state = {
      block,
      body,
      bar,
      status,
      toggle,
      info,
      sourceNode: null,
      source: "",
      translatedSource: "",
      translatedPrefix: "",
      view: "original",
      rendered: false,
      running: false,
      done: false,
      requested: false,
      preferOriginal: false,
      skipped: false,
      generation: 0,
      timer: 0,
      streamSince: 0,
    };
    blocks.add(state);
    bar.hidden = !info.open;

    toggle.addEventListener("click", () => {
      if (state.rendered) {
        state.preferOriginal = state.view === "translated";
        setView(state, state.view === "original" ? "translated" : "original");
        return;
      }
      if (state.running) return;
      state.requested = true;
      schedule(state, true);
    });
    status.addEventListener("click", () => {
      if (status.dataset.state !== "error") return;
      state.requested = true;
      schedule(state, true);
    });

    schedule(state, false);

    return {
      update(next) {
        state.info = next;
        bar.hidden = !next.open;
        schedule(state, false);
      },
      dispose() {
        cancel(state);
        blocks.delete(state);
        bar.remove();
        if (state.sourceNode) state.sourceNode.removeAttribute("data-ekdp-kct-hidden");
        const node = state.body.querySelector(".ekdp-kct-translated");
        node?.remove();
        state.block.removeAttribute("data-ekdp-kct-view");
      },
    };
  });
  ctx.onDispose(() => {
    window.removeEventListener("ekdp-kct:settings", onSettingsChanged);
    window.removeEventListener("ekdp-kct:cache", onCacheCleared);
    decoration.dispose();
    blocks.clear();
    style.remove();
  });

  ctx.log("info", "思考内容翻译已启用");
  return { status: "active" };

  // ---------------------------------------------------------------- 调度

  function schedule(state, force) {
    if (!state.info.open) {
      cancel(state);
      return;
    }
    // 流式输出期间按固定间隔重试，避免每个 token 都发一次请求。
    if (state.info.streaming) {
      if (!state.streamSince) state.streamSince = Date.now();
      if (state.timer) return;
      const waited = Date.now() - state.streamSince >= STREAM_MAX_WAIT_MS;
      state.timer = window.setTimeout(() => {
        state.timer = 0;
        void maybeRun(state, force);
      }, waited ? 0 : STREAM_DEBOUNCE_MS);
      return;
    }
    if (state.timer) {
      clearTimeout(state.timer);
      state.timer = 0;
    }
    void maybeRun(state, force);
  }

  function cancel(state) {
    state.generation += 1;
    state.running = false;
    clearTimeout(state.timer);
    state.timer = 0;
    state.streamSince = 0;
  }

  async function maybeRun(state, force) {
    if (!state.block.isConnected || !state.info.open) return;
    state.bar.hidden = false;

    // 宿主可能替换正文节点（虚拟列表），此时旧译文与旧状态全部作废。
    const body = state.block.querySelector(".think-body-inner") ?? state.block.querySelector(".think-body");
    if (!body) return;
    if (body !== state.body) {
      state.generation += 1;
      state.running = false;
      state.body = body;
      state.sourceNode = pickSource(body);
      state.source = "";
      state.done = false;
      state.rendered = false;
      state.skipped = false;
      state.translatedSource = "";
      setView(state, "original");
    }
    if (!state.bar.isConnected) {
      const head = state.block.querySelector("button.think-head");
      if (head) head.after(state.bar);
      else state.block.prepend(state.bar);
    }

    const source = readSource(state);
    if (state.skipped && source !== state.source) {
      state.skipped = false;
      state.done = false;
      setView(state, "original");
    }
    if (state.rendered && source.startsWith(state.translatedSource)) {
      // 流式追加：把新增的原文接在已翻译前缀之后，用户看到的是连贯内容。
      render(state, state.translatedPrefix + source.slice(state.translatedSource.length));
    } else if (state.rendered) {
      state.rendered = false;
      state.translatedSource = "";
      setView(state, "original");
    }
    if (source !== state.source && state.done) {
      state.done = false;
      state.rendered = false;
      setView(state, "original");
    } else if (state.done) {
      if (!state.skipped && !state.body.querySelector(".ekdp-kct-translated")) state.done = false;
      setView(state, state.view);
    }

    const settings = current();
    if (!state.running && state.info.open && (settings.autoTranslate || state.requested || force)) {
      if (!source.trim()) return;
      if (source === state.source && state.done) return;
      if (settings.provider === "custom" && !customEndpoint(settings)) {
        setStatus(state, "error", "请先设置自定义端点", "在插件设置里填写自定义服务地址");
        return;
      }
      await run(state, source);
    }
  }

  async function run(state, sourceArg) {
    if (state.running) return;
    const fullSource = readSource(state);
    let source = fullSource;
    if (state.info.streaming) {
      source = stableStreamSource(fullSource, Date.now() - state.streamSince >= STREAM_MAX_WAIT_MS);
      if (!source.trim()) return;
    }
    const settings = current();
    const lang = resolveTarget(settings);
    if (translationPieces(fullSource, lang).every(piece => piece.skip)) {
      render(state, fullSource);
      state.source = fullSource;
      state.done = !state.info.streaming;
      setStatus(state, "ok", "");
      return;
    }
    if (source === state.source && state.rendered) {
      if (!state.info.streaming) state.done = true;
      return;
    }

    const generation = ++state.generation;
    const body = state.body;
    const valid = () =>
      state.generation === generation &&
      state.block.isConnected &&
      body.isConnected &&
      state.info.open &&
      readSource(state).startsWith(source);

    state.running = true;
    setStatus(state, "loading", "翻译中…");

    try {
      const translated = await engine.translate(source, lang, (text, done, total) => {
        if (!valid()) return;
        const text2 = readSource(state);
        if (!text2.startsWith(source)) return;
        state.translatedPrefix = text;
        state.translatedSource = source;
        render(state, text + text2.slice(source.length));
        setStatus(state, "loading", `翻译中 ${done}/${total}`);
      }, valid);
      if (!valid()) return;
      const nowSource = readSource(state);
      if (!nowSource.startsWith(source)) return;
      state.translatedPrefix = translated;
      state.translatedSource = source;
      render(state, translated + nowSource.slice(source.length));
      state.done = nowSource === source && !state.info.streaming;
      state.source = source;
      setStatus(state, state.done ? "ok" : "loading", state.done ? "" : "后续内容待翻译");
    } catch (error) {
      if (!valid()) return;
      setStatus(state, "error", "重试", `翻译失败：${messageOf(error)}`);
    } finally {
      if (state.generation === generation) {
        state.running = false;
        if (valid() && readSource(state) !== fullSource) schedule(state, false);
      }
    }
  }

  function setStatus(state, kind, text, title) {
    state.status.dataset.state = kind;
    state.status.textContent = text;
    state.status.title = title ?? "";
    state.status.disabled = kind !== "error";
  }

  function setView(state, view) {
    state.view = view;
    state.block.setAttribute("data-ekdp-kct-view", view);
    const source = pickSource(state.body);
    if (source) {
      state.sourceNode = source;
      // Written as an attribute rather than a class: the hub observes class
      // changes on host nodes, and a class toggle here would look like host churn
      // and invalidate the very translation that caused it.
      if (view === "translated") source.setAttribute("data-ekdp-kct-hidden", "1");
      else source.removeAttribute("data-ekdp-kct-hidden");
    }
    state.toggle.textContent = view === "translated" ? "原文" : state.rendered ? "译文" : "翻译";
    state.toggle.title = view === "translated" ? "查看原文" : state.rendered ? "查看译文" : "翻译这段思考";
    state.toggle.setAttribute("aria-label", state.toggle.title);
    state.toggle.setAttribute("aria-pressed", String(view === "translated"));
    state.toggle.hidden = state.skipped;
  }

  function render(state, text) {
    let node = state.body.querySelector(".ekdp-kct-translated");
    state.skipped = text === readSource(state);
    if (state.skipped) {
      node?.remove();
      state.rendered = false;
      setView(state, "original");
      return;
    }
    if (!node) {
      node = pickSource(state.body)?.cloneNode(false) ?? document.createElement("pre");
      node.removeAttribute("id");
      node.className = "ekdp-kct-translated";
      node.setAttribute("data-ekdp-owned", "1");
      state.body.appendChild(node);
    }
    if (node.textContent !== text) node.textContent = text;
    state.rendered = true;
    setView(state, state.preferOriginal ? "original" : "translated");
  }

  function readSource(state) {
    const cached = state.sourceNode;
    if (cached && state.body.contains(cached)) return cached.textContent ?? "";
    const node = pickSource(state.body);
    if (node) {
      state.sourceNode = node;
      return node.textContent ?? "";
    }
    return sourceFromBody(state.body);
  }
}

// ------------------------------------------------------------------ 引擎

async function createEngine(ctx) {
  const settings = { ...DEFAULTS };
  const cache = new Map();
  const index = new Map();
  let indexDirty = false;
  let persistTimer = 0;

  async function loadSettings() {
    const stored = await ctx.storage.get(SETTINGS_KEY).catch(() => undefined);
    const merged = { ...DEFAULTS, ...(stored && typeof stored === "object" ? stored : {}) };
    if (merged.targetLanguage !== AUTO && !TARGET_LANGUAGES.includes(merged.targetLanguage)) {
      merged.targetLanguage = AUTO;
    }
    if (!PROVIDERS.includes(merged.provider)) merged.provider = "bing";
    settings.targetLanguage = merged.targetLanguage;
    settings.provider = merged.provider;
    settings.autoTranslate = merged.autoTranslate !== false;
    settings.customEndpoint = typeof merged.customEndpoint === "string" ? merged.customEndpoint : "";
    settings.customModel = typeof merged.customModel === "string" ? merged.customModel : "";
    settings.useCredential = merged.useCredential !== false;
    return settings;
  }

  async function loadCache() {
    const stored = await ctx.storage.get(CACHE_INDEX_KEY).catch(() => undefined);
    if (!Array.isArray(stored)) return;
    const now = Date.now();
    for (const entry of stored) {
      if (!Array.isArray(entry) || typeof entry[0] !== "string" || !Number.isFinite(entry[1])) continue;
      if (now - entry[1] >= CACHE_TTL_MS) continue;
      index.set(entry[0], entry[1]);
    }
    indexDirty = true;
    // 只预取最近使用的一小部分：一次设置页打开不该发起上百次桥调用。
    const recent = [...index.entries()].sort((a, b) => b[1] - a[1]).slice(0, 20);
    await Promise.all(recent.map(async ([key]) => {
      const text = await ctx.storage.get(CACHE_ENTRY_PREFIX + key).catch(() => undefined);
      if (typeof text === "string" && text.trim()) cache.set(key, text);
      else index.delete(key);
    }));
  }

  function persist() {
    if (persistTimer) return;
    persistTimer = window.setTimeout(() => {
      persistTimer = 0;
      void flush();
    }, 1000);
  }

  async function flush() {
    if (!indexDirty) return;
    indexDirty = false;
    prune();
    const entries = [...index.entries()].sort((a, b) => b[1] - a[1]);
    await ctx.storage.set(CACHE_INDEX_KEY, entries).catch(() => {});
    for (const [key, text] of cache) {
      if (!index.has(key)) continue;
      if (text.length * 2 > 32000) continue;
      await ctx.storage.set(CACHE_ENTRY_PREFIX + key, text).catch(() => {});
    }
  }

  function prune(now = Date.now()) {
    for (const [key, usedAt] of index) {
      if (now - usedAt >= CACHE_TTL_MS) {
        index.delete(key);
        cache.delete(key);
      }
    }
    if (index.size <= CACHE_LIMIT) return;
    const stale = [...index.entries()].sort((a, b) => a[1] - b[1]).slice(0, index.size - CACHE_LIMIT);
    for (const [key] of stale) {
      index.delete(key);
      cache.delete(key);
    }
  }

  function remember(key, text) {
    if (text.length * 2 > 32000) return;
    cache.set(key, text);
    index.delete(key);
    index.set(key, Date.now());
    indexDirty = true;
    persist();
  }

  function clearMemoryCache() {
    cache.clear();
    index.clear();
    indexDirty = false;
  }

  await loadSettings();
  await loadCache().catch(() => {});

  const queue = [];
  let active = 0;
  let cooldownUntil = 0;

  function pump() {
    while (active < CONCURRENCY && queue.length) {
      if (Date.now() < cooldownUntil) {
        const delay = cooldownUntil - Date.now();
        setTimeout(pump, delay);
        return;
      }
      const job = queue.shift();
      active += 1;
      void execute(job)
        .catch(error => job.reject(error))
        .finally(() => {
          active -= 1;
          pump();
        });
    }
  }

  async function execute(job) {
    let attempt = 0;
    for (;;) {
      try {
        const results = await callProvider(job);
        job.resolve(results);
        return;
      } catch (error) {
        const status = Number(error?.status) || 0;
        if (status === 429) {
          cooldownUntil = Date.now() + (Number(error.retryAfterMs) || 60000);
        }
        const retryable = status === 429 || status >= 500 || status === 0;
        if (retryable && attempt < 2) {
          attempt += 1;
          await new Promise(resolve => setTimeout(resolve, 1000 * 2 ** (attempt - 1) * (0.8 + Math.random() * 0.4)));
          continue;
        }
        job.reject(error);
        return;
      }
    }
  }

  function callProvider(job) {
    const provider = settings.provider;
    if (provider === "google") return translateGoogle(ctx, job.texts[0], job.lang);
    if (provider === "bing") return translateBing(ctx, job.texts, job.lang);
    return translateCustom(ctx, job.texts, job.lang, settings);
  }

  async function translate(source, lang, onProgress, valid) {
    const provider = settings.provider;
    const scope = provider === "custom" ? `${settings.customEndpoint}\u0000${settings.customModel}` : "";
    const prefix = hashKey(`${provider}\u0000${lang}\u0000${scope ? scope + "\u0000" : ""}`);
    const pieces = translationPieces(source, lang);
    const out = pieces.map(piece => piece.text);
    const missing = [];
    let completed = 0;
    const total = pieces.filter(piece => !piece.skip).length;

    for (const [i, piece] of pieces.entries()) {
      if (piece.skip) continue;
      const key = `${prefix}:${hashKey(piece.text)}`;
      const hit = cache.get(key);
      if (hit) {
        out[i] = preserveFencedCode(piece.text, hit);
        index.set(key, Date.now());
        indexDirty = true;
        completed += 1;
        continue;
      }
      missing.push({ index: i, text: piece.text, key });
    }
    if (completed) onProgress(preserveFencedCode(source, out.join("")), completed, total);

    const batches = [];
    if (provider === "google") {
      for (const item of missing) batches.push([item]);
    } else {
      let batch = [];
      let chars = 0;
      const numbering = provider === "custom" ? 4 : 0;
      for (const item of missing) {
        if (batch.length && (batch.length >= 8 || chars + item.text.length + numbering > BATCH_CHARS)) {
          batches.push(batch);
          batch = [];
          chars = 0;
        }
        batch.push(item);
        chars += item.text.length + numbering;
      }
      if (batch.length) batches.push(batch);
    }

    // A batch is one request, so one failure must not throw away the items that
    // did translate: each item settles on its own and a missing one is retried
    // alone. Only a request that fails outright fails the items it carried.
    const retryAlone = item =>
      new Promise((resolve, reject) => {
        queue.push({
          texts: [item.text],
          lang,
          resolve: single => {
            const text = single?.[0];
            if (typeof text === "string" && text.trim()) {
              apply(item, text);
              settle(item, null);
              resolve();
              return;
            }
            const error = new Error("译文为空，请重试");
            settle(item, error);
            reject(error);
          },
          reject: error => {
            settle(item, error);
            reject(error);
          },
        });
        pump();
      });

    const runs = batches.map(batch =>
      new Promise((resolve, reject) => {
        queue.push({
          texts: batch.map(item => item.text),
          lang,
          resolve: results => {
            const failed = [];
            for (const [i, item] of batch.entries()) {
              const translated = results?.[i];
              if (typeof translated === "string" && translated.trim()) {
                apply(item, translated);
                settle(item, null);
              } else {
                failed.push(item);
              }
            }
            if (!failed.length) {
              resolve();
              return;
            }
            // Batching changes the service's answer shape, not the user's text;
            // retrying one by one keeps the parts that worked.
            void Promise.allSettled(failed.map(item => retryAlone(item))).then(results => {
              const firstError = results.find(result => result.status === "rejected");
              if (firstError) reject(firstError.reason);
              else resolve();
            });
          },
          reject: error => {
            for (const item of batch) settle(item, error);
            reject(error);
          },
        });
        pump();
      }),
    );

    // A cache hit was already reported above, so only the missing paragraphs are
    // still pending.
    let pending = missing.length;
    function apply(item, translated) {
      const safe = preserveFencedCode(item.text, translated);
      out[item.index] = safe;
      remember(item.key, safe);
    }
    function settle(item, error) {
      if (item.settled) return;
      item.settled = true;
      pending = Math.max(0, pending - 1);
      if (error) persist();
      if (!valid()) return;
      // The count is paragraphs, so a batch of eight still reports eight.
      const done = total - pending;
      completed = done;
      onProgress(preserveFencedCode(source, out.join("")), done, total);
      persist();
    }

    await Promise.all(runs);
    persist();
    const final = preserveFencedCode(source, out.join(""));
    if (valid()) onProgress(final, total, total);
    return final;
  }

  return { settings, translate, loadSettings, clearMemoryCache };
}

// --------------------------------------------------------------- 三种服务

async function translateGoogle(ctx, text, lang) {
  const url = new URL("https://translate.googleapis.com/translate_a/single");
  url.searchParams.set("client", "gtx");
  url.searchParams.set("sl", "auto");
  url.searchParams.set("tl", targetFor("google", lang));
  url.searchParams.set("dt", "t");
  const response = await request(ctx, url.href, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ q: text }).toString(),
  });
  const first = Array.isArray(response) ? response[0] : null;
  if (!Array.isArray(first) || first.length === 0) throw new Error("google bad shape");
  return [first.map(segment => (Array.isArray(segment) && typeof segment[0] === "string" ? segment[0] : "")).join("")];
}

async function translateBing(ctx, texts, lang) {
  const url = new URL("https://edge.microsoft.com/translate/translatetext");
  url.searchParams.set("to", targetFor("bing", lang));
  url.searchParams.set("isEnterpriseClient", "false");
  const payload = await request(ctx, url.href, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(texts),
  });
  if (!Array.isArray(payload) || payload.length !== texts.length) throw new Error("bing bad shape");
  return payload.map(item => {
    const translated = item?.translations?.[0]?.text;
    return typeof translated === "string" && translated.trim() ? translated : null;
  });
}

async function translateCustom(ctx, texts, lang, settings) {
  const numbered = texts.length > 1;
  const text = numbered ? texts.map((item, i) => `${i + 1}. ${item}`).join("\n") : texts[0];
  const endpoint = customEndpoint(settings);
  if (!endpoint) throw new Error("自定义端点未设置");
  const headers = { "content-type": "application/json" };
  const init = {
    method: "POST",
    headers,
    body: JSON.stringify({
      model: settings.customModel || undefined,
      messages: [
        {
          role: "system",
          content:
            `You are a translation engine. Translate the user's text into ${lang}. ` +
            "Preserve line breaks, Markdown and code exactly. Output only the translation." +
            (numbered ? " Keep each numbered item and its number, starting each item on a new line." : ""),
        },
        { role: "user", content: text },
      ],
      temperature: 0,
    }),
  };
  if (settings.useCredential) {
    // 明文只在核心进程内注入，插件看不到，也写不进缓存或日志。
    init.credential = "translator";
    init.credentialHeader = "Authorization";
    init.credentialPrefix = "Bearer ";
  }
  const payload = await request(ctx, endpoint, init);
  const content = payload?.choices?.[0]?.message?.content;
  if (typeof content !== "string" || content === "") throw new Error("custom bad shape");
  if (!numbered) return [content];
  const lines = [...content.matchAll(/^(\d+)\.[ \t]+/gm)];
  if (lines.length !== texts.length || content.slice(0, lines[0]?.index).trim() ||
    lines.some((line, i) => Number(line[1]) !== i + 1)) return texts.map(() => null);
  const results = lines.map((line, i) =>
    content.slice(line.index + line[0].length, lines[i + 1]?.index ?? content.length).trim());
  return results.every(Boolean) ? results : texts.map(() => null);
}

function customEndpoint(settings) {
  const raw = String(settings.customEndpoint || "").trim();
  if (!raw) return "";
  let url;
  try {
    url = new URL(raw);
  } catch {
    return "";
  }
  // 主机名可能经 DNS 指向其他地址，只使用清单逐项授权的本机字面 IP。
  const local = url.hostname === "127.0.0.1" || url.hostname === "[::1]";
  if (!local) return "";
  if (url.protocol !== "https:" && url.protocol !== "http:") return "";
  return url.href;
}

async function request(ctx, url, init) {
  let response;
  try {
    response = await ctx.net.fetch(url, init);
  } catch (error) {
    throw Object.assign(new Error("网络请求失败"), { status: 0, cause: error });
  }
  if (response.status < 200 || response.status >= 300) {
    const error = Object.assign(new Error(`HTTP ${response.status}`), { status: response.status });
    const retryAfter = response.headers?.["retry-after"] ?? response.headers?.["Retry-After"];
    if (retryAfter) {
      const seconds = Number(retryAfter);
      error.retryAfterMs = Number.isFinite(seconds) ? seconds * 1000 : Math.max(0, Date.parse(retryAfter) - Date.now());
    }
    if (response.status === 429 && !Number.isFinite(error.retryAfterMs)) error.retryAfterMs = 60000;
    throw error;
  }
  try {
    return JSON.parse(response.body);
  } catch {
    throw Object.assign(new Error("响应不是 JSON"), { status: 0 });
  }
}

// ------------------------------------------------------------- 文本分段

// 与独立版一致：围栏代码、行内代码与链接整体跳过，翻译不会改动它们。
function protectedPieces(text) {
  const pieces = [];
  const tokens = /^ {0,3}(`{3,})[^`\n]*(?:\n|$)|^ {0,3}(~{3,})[^\n]*(?:\n|$)|(?<!`)(`+)(?!`)[\s\S]*?(?<!`)\3(?!`)|https?:\/\/[^\s`<>。！？；，]+/gm;
  let last = 0;
  let match;
  while ((match = tokens.exec(text)) !== null) {
    if (match.index > last) pieces.push({ text: text.slice(last, match.index), skip: false });
    const fence = match[1] || match[2];
    if (fence) {
      const closing = new RegExp(`^ {0,3}${fence[0]}{${fence.length},}[ \\t]*\\r?$`, "gm");
      closing.lastIndex = tokens.lastIndex;
      const end = closing.exec(text);
      tokens.lastIndex = end ? closing.lastIndex : text.length;
    }
    pieces.push({ text: text.slice(match.index, tokens.lastIndex), skip: true, fenced: !!fence });
    last = tokens.lastIndex;
  }
  if (last < text.length) pieces.push({ text: text.slice(last), skip: false });
  return pieces;
}

// 译文里的围栏必须与原文一一对应，否则说明服务改写了代码，整块退回原文。
function preserveFencedCode(source, translated) {
  const fences = text => protectedPieces(text).filter(piece => piece.fenced).map(piece => piece.text);
  const original = fences(source);
  const result = fences(translated);
  return original.length === result.length && original.every((text, i) => text === result[i])
    ? translated
    : source;
}

function endsWithAbbreviation(text) {
  return /\b(?:Dr\.|Mr\.|Mrs\.|Ms\.|e\.g\.|i\.e\.|etc\.|vs\.|v?\d+(?:\.\d+)+\.?)$/i.test(text.trimEnd());
}

function sentenceUnits(text) {
  const units = [];
  let last = 0;
  for (const match of text.matchAll(/\n+|[。！？]|[.!?](?:[ \t]+|(?=[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]))/gu)) {
    if (match[0][0] === "." && endsWithAbbreviation(text.slice(last, match.index + 1))) continue;
    const end = match.index + match[0].length;
    units.push(text.slice(last, end));
    last = end;
  }
  if (last < text.length) units.push(text.slice(last));
  return units;
}

function splitLong(text) {
  const parts = [];
  while (text.length > MAX_CHARS) {
    const prefix = text.slice(0, MAX_CHARS);
    const boundaries = [...prefix.matchAll(/(?:[。！？.!?]\s+|\n+|\s+)/g)]
      .filter(match => !endsWithAbbreviation(prefix.slice(0, match.index + match[0].length)));
    const boundary = boundaries.at(-1);
    let end = boundary && boundary.index > MAX_CHARS / 2 ? boundary.index + boundary[0].length : MAX_CHARS;
    if (/[\uD800-\uDBFF]/.test(text[end - 1])) end -= 1;
    parts.push(text.slice(0, end));
    text = text.slice(end);
  }
  if (text) parts.push(text);
  return parts;
}

function splitParagraphs(text) {
  const parts = [];
  const re = /(\n[ \t]*\n)/g;
  let last = 0;
  let match;
  while ((match = re.exec(text)) !== null) {
    parts.push({ body: text.slice(last, match.index), sep: match[1] });
    last = match.index + match[1].length;
  }
  parts.push({ body: text.slice(last), sep: "" });
  return parts;
}

// 流式输出时只翻译已经稳定的段落，最后一句永远等它写完。
function stableStreamSource(text, waited = false) {
  const masked = protectedPieces(text).map(piece => (piece.skip ? " ".repeat(piece.text.length) : piece.text)).join("");
  const paragraphs = splitParagraphs(masked);
  let end = paragraphs.slice(0, -1).reduce((length, part) => length + part.body.length + part.sep.length, 0);
  const tail = masked.slice(end);
  if (tail.length >= MAX_CHARS || waited) {
    end += sentenceUnits(tail).slice(0, -1).reduce((length, unit) => length + unit.length, 0);
  }
  return text.slice(0, end);
}

function translationPieces(source, lang) {
  const masked = protectedPieces(source).map(piece => (piece.skip ? " ".repeat(piece.text.length) : piece.text)).join("");
  let offset = 0;
  return splitParagraphs(masked).flatMap(({ body, sep }) => {
    const groups = [];
    for (const unit of sentenceUnits(body)) {
      const skip = unit.trim() ? isTargetLanguage(unit, lang) : groups.at(-1)?.skip ?? true;
      for (const piece of protectedPieces(source.slice(offset, offset + unit.length))) {
        const last = groups.at(-1);
        const preserve = skip || piece.skip;
        if (last && last.skip === preserve) last.text += piece.text;
        else groups.push({ text: piece.text, skip: preserve });
      }
      offset += unit.length;
    }
    const separator = source.slice(offset, offset + sep.length);
    offset += sep.length;
    const expanded = groups.flatMap(({ text, skip }) => {
      if (skip) return [{ text, skip }];
      return splitLong(text).flatMap(text => {
        const body = text.trim();
        if (!body) return [{ text, skip: true }];
        const start = text.indexOf(body);
        return [
          { text: text.slice(0, start), skip: true },
          { text: body, skip: false },
          { text: text.slice(start + body.length), skip: true },
        ].filter(item => item.text);
      });
    });
    return [...expanded, { text: separator, skip: true }];
  });
}

// 已经是目标语言的部分不送去翻译，省请求也避免译文被二次改写。
function isTargetLanguage(text, lang) {
  const prose = protectedPieces(text).filter(piece => !piece.skip).map(piece => piece.text).join("");
  const letters = prose.match(/\p{L}/gu) || [];
  if (!letters.length) return true;
  const count = pattern => (prose.match(pattern) || []).length;
  const han = count(/\p{Script=Han}/gu);
  const kana = count(/[\p{Script=Hiragana}\p{Script=Katakana}]/gu);
  const hangul = count(/\p{Script=Hangul}/gu);
  const cyrillic = count(/\p{Script=Cyrillic}/gu);
  if (lang === "zh") return han > 0 && !kana && !hangul && !cyrillic && han / letters.length >= 0.5;
  if (lang === "ja") return kana > 0 && !hangul && !cyrillic && (han + kana) / letters.length >= 0.5;
  if (lang === "ko") return hangul > 0 && !kana && !cyrillic && (han + hangul) / letters.length >= 0.5;
  const commonWords = {
    en: "the this that these those is are was were be been being and or but with without for from into of to in on at by as it its we our you your i my can could will would should have has had not do does did let first then need check",
    es: "el la los las un una unos unas es son está están ser estar y o pero con sin para por del de al en como este esta estos estas que se su sus nosotros nuestro podemos puede debe hay primero luego verificar",
    fr: "le la les un une des est sont être et ou mais avec sans pour par du de au aux en dans sur ce cette ces qui que nous notre vous votre peut faut pas ne puis vérifier",
    de: "der die das ein eine eines ist sind war waren sein und oder aber mit ohne für von zu zum zur den dem des im in auf aus als diese dieser dieses wir unser können kann muss nicht dann prüfen",
    ru: "это этот эта эти для при или как что чтобы который которая которые мы вы они его её их не нет есть будет будут нужно можно сначала затем проверить",
  };
  if (!commonWords[lang] || han || kana || hangul ||
    (lang === "ru" ? cyrillic / letters.length < 0.8 || /[іїєґў]/iu.test(prose) : cyrillic)) return false;
  const words = prose.toLowerCase().match(/\p{L}+/gu) || [];
  const scores = Object.entries(commonWords).map(([language, vocabulary]) => {
    const known = new Set(vocabulary.split(" "));
    return [language, words.filter(word => known.has(word)).length];
  }).sort((a, b) => b[1] - a[1]);
  return scores[0][0] === lang && scores[0][1] >= 2 &&
    scores[0][1] >= words.length * 0.15 && scores[0][1] - scores[1][1] >= 2;
}

function targetFor(provider, lang) {
  if (provider === "google") return lang === "zh" ? "zh-CN" : lang;
  return lang === "zh" ? "zh-Hans" : lang;
}

function resolveTarget(settings) {
  if (settings.targetLanguage !== AUTO && TARGET_LANGUAGES.includes(settings.targetLanguage)) {
    return settings.targetLanguage;
  }
  const locale = (typeof navigator !== "undefined" ? navigator.language : "") ||
    document.documentElement.lang || "en";
  const base = String(locale).split("-")[0].toLowerCase();
  return TARGET_LANGUAGES.includes(base) ? base : "en";
}

// 缓存键只需稳定且足够分散，不能依赖 crypto.subtle。
function hashKey(text) {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (let i = 0; i < text.length; i += 1) {
    const c = text.charCodeAt(i);
    h1 = Math.imul(h1 ^ c, 0x01000193);
    h2 = Math.imul(h2 ^ c, 0x85ebca6b) ^ (h2 >>> 13);
  }
  return `${text.length.toString(36)}-${(h1 >>> 0).toString(36)}-${(h2 >>> 0).toString(36)}`;
}

// ------------------------------------------------------------------ DOM

function pickSource(body) {
  const pres = [...body.querySelectorAll("pre")].filter(el => !el.closest(".ekdp-kct-translated"));
  if (pres.length > 0) return pres[0];
  for (const child of body.children) {
    if (child.classList.contains("ekdp-kct-translated")) continue;
    if ((child.textContent || "").trim() !== "") return child;
  }
  return null;
}

function sourceFromBody(body) {
  const clone = body.cloneNode(true);
  for (const el of clone.querySelectorAll(".ekdp-kct-translated, .ekdp-kct-bar")) el.remove();
  return clone.textContent || "";
}

let injected = null;

function injectStyles() {
  if (injected?.isConnected) return injected;
  const style = document.createElement("style");
  style.setAttribute("data-ekdp-owned", "1");
  style.textContent = `
.think[data-ekdp-kct-view] [data-ekdp-kct-hidden]{display:none!important}
.think[data-ekdp-kct-view="original"] .ekdp-kct-translated{display:none!important}
.ekdp-kct-bar{display:flex;align-items:center;justify-content:flex-end;gap:4px;font:12px/20px var(--font-ui,sans-serif);color:var(--color-text-secondary,#777)}
.ekdp-kct-bar[hidden]{display:none}
.ekdp-kct-btn{font:inherit;color:inherit;background:transparent;border:0;border-radius:4px;padding:0 5px;cursor:pointer;white-space:nowrap}
.ekdp-kct-btn:hover{color:var(--color-text,#222);background:var(--color-hover,rgba(128,128,128,.1))}
.ekdp-kct-btn:focus-visible{outline:2px solid var(--blue,#4385ef);outline-offset:2px}
.ekdp-kct-btn:disabled{cursor:default}
.ekdp-kct-status{color:var(--color-text-secondary,#888);font-size:11px}
.ekdp-kct-status:empty{display:none}
.ekdp-kct-status[data-state="error"]{color:var(--red,#b9433e)}
.ekdp-kct-translated{white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word}
`;
  document.head.appendChild(style);
  injected = style;
  return style;
}

function messageOf(error) {
  const text = String(error?.message || error || "");
  return text.length > 200 ? text.slice(0, 200) + "…" : text;
}
