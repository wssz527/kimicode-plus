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
const KCT_DISPOSE_KEY = "__ekdpKctDispose";

const TARGET_LANGUAGES = ["zh", "en", "ja", "ko", "es", "fr", "de", "ru"];
const AUTO = "__auto__";
const PROVIDERS = ["bing", "google", "custom"];
const CREDENTIAL = "translator";
// 自定义服务的本机写法：清单只授权这几个字面目标，其余主机必须走 HTTPS。
const LOCAL_ENDPOINT_HOSTS = new Set(["127.0.0.1", "[::1]", "localhost"]);
const PROVIDER_LABELS = { bing: "Bing 翻译", google: "Google 翻译", custom: "自定义服务" };
const LANGUAGE_LABELS = {
  zh: "简体中文",
  en: "English",
  ja: "日本語",
  ko: "한국어",
  es: "Español",
  fr: "Français",
  de: "Deutsch",
  ru: "Русский",
};

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
  injectStyles(document);
  ctx.onDispose(() => removeStyles(document));
  const blocks = new Set();
  const popover = createSettingsPopover({ ctx, engine, blocks, root: document });

  function current() {
    return engine.settings;
  }

  async function reload(source) {
    await engine.loadSettings();
    // 每块记录它是用哪一组设置翻出来的；与当前设置不同的块才作废译文。仅切换
    // 自动翻译或凭据开关时签名不变，已经显示的译文仍然有效，清空它只会让用户
    // 失去正在读的内容。
    const signature = translationSignature(engine.settings);
    for (const state of blocks) {
      if (state.signature !== signature) {
        // 与独立版的 resetAll 一致：旧译文属于旧设置，必须作废并按新设置重新
        // 调度；否则 done 标记会让 maybeRun 直接跳过，切换语言或服务看起来
        // 毫无效果。state.requested 保留，用户手动发起过的块会继续重译。
        cancel(state);
        state.done = false;
        state.source = "";
        state.rendered = false;
        state.skipped = false;
        state.translatedSource = "";
        state.translatedPrefix = "";
        state.completedSource = "";
        state.completedPrefix = "";
        state.failedSource = "";
        state.failedSignature = "";
        state.preferOriginal = false;
        state.body.querySelector(".ekdp-kct-translated")?.remove();
        setView(state, "original");
        setStatus(state, "ok", "");
      }
      schedule(state, false);
    }
    // 内联浮层已经持有最新值；它自己的保存不应重建正在操作的控件。
    if (source !== "inline") popover.refresh();
  }

  function onSettingsChanged(event) {
    void reload(event?.detail?.source);
  }
  function onCacheCleared(event) {
    engine.clearMemoryCache();
    if (event?.detail?.source !== "inline") popover.refresh();
  }
  window.addEventListener("ekdp-kct:settings", onSettingsChanged);
  window.addEventListener("ekdp-kct:cache", onCacheCleared);

  const decoration = ctx.ui.decorate("thinking", (block, info) => {
    const prevDispose = block[KCT_DISPOSE_KEY];
    if (typeof prevDispose === "function") {
      try {
        prevDispose();
      } catch {}
    }
    const enhanced = block.matches("[data-ekdp-thinking]");
    const head = enhanced ? block.querySelector("summary") : block.querySelector("button.think-head");
    const body = enhanced ? block.querySelector("[data-ekdp-thinking-body]") : block.querySelector(".think-body-inner") ?? block.querySelector(".think-body");
    if (!head || !body) return;
    const root = block.getRootNode();
    const style = enhanced ? injectStyles(root) : null;

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

    const settings = document.createElement("button");
    settings.type = "button";
    settings.className = "ekdp-kct-btn";
    settings.textContent = "设置";
    settings.title = "翻译设置";
    settings.setAttribute("aria-label", "翻译设置");
    settings.setAttribute("aria-haspopup", "dialog");
    settings.setAttribute("aria-expanded", "false");

    bar.append(status, toggle, settings);
    head.after(bar);

    const state = {
      block,
      body,
      bar,
      enhanced,
      style,
      root,
      status,
      toggle,
      info,
      sourceNode: null,
      source: "",
      translatedSource: "",
      translatedPrefix: "",
      completedSource: "",
      completedPrefix: "",
      // 上次失败的原文与配置签名；相同前缀在同配置下不再自动重试，避免 429 时的重试风暴。
      failedSource: "",
      failedSignature: "",
      view: "original",
      rendered: false,
      running: false,
      done: false,
      requested: false,
      preferOriginal: false,
      skipped: false,
      generation: 0,
      timer: 0,
      timerDue: 0,
      timerForce: false,
      streamSince: 0,
      // 译文是用哪一组设置产生的；设置变化后与之比较以决定是否重译。
      signature: translationSignature(engine.settings),
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
      state.failedSource = "";
      state.failedSignature = "";
      state.requested = true;
      schedule(state, true);
    });
    status.addEventListener("click", () => {
      if (status.dataset.state !== "error") return;
      // 用户手动重试就是这个标记唯一的清除入口（另一个是稳定前缀或配置变化）。
      state.failedSource = "";
      state.failedSignature = "";
      state.requested = true;
      schedule(state, true);
    });
    const manual = createSettingsPopover({ ctx, engine, blocks: state.enhanced ? new Set([state]) : blocks, root: state.root?.ownerDocument ?? document });
    state.manual = manual;
    settings.addEventListener("click", () => manual.toggle(settings));

    let disposed = false;
    const dispose = () => {
      if (disposed) return;
      disposed = true;
      if (block[KCT_DISPOSE_KEY] === dispose) {
        delete block[KCT_DISPOSE_KEY];
      }
      cancel(state);
      blocks.delete(state);
      for (const node of state.body.querySelectorAll("[data-ekdp-kct-hidden]")) node.removeAttribute("data-ekdp-kct-hidden");
      state.manual.dispose();
      popover.closeFor(settings);
      if (state.style) removeStyles(state.root);
      bar.remove();
      const node = state.body.querySelector(".ekdp-kct-translated");
      node?.remove();
      state.block.removeAttribute("data-ekdp-kct-view");
    };
    Object.defineProperty(block, KCT_DISPOSE_KEY, {
      value: dispose,
      configurable: true,
      writable: true,
    });

    schedule(state, false);

    return {
      update(next) {
        if (disposed) return;
        state.info = next;
        bar.hidden = !next.open;
        if (!next.open) state.manual.closeFor(settings);
        schedule(state, false);
      },
      dispose,
    };
  }, { includeEnhanced: true });
  ctx.onDispose(() => {
    window.removeEventListener("ekdp-kct:settings", onSettingsChanged);
    window.removeEventListener("ekdp-kct:cache", onCacheCleared);
    popover.dispose();
    decoration.dispose();
    for (const state of blocks) cancel(state);
    blocks.clear();
    engine.dispose();
  });

  ctx.log("info", "思考内容翻译已启用");
  return { status: "active" };

  // ---------------------------------------------------------------- 调度

  function monotonicNow() {
    return typeof performance !== "undefined" && typeof performance.now === "function"
      ? performance.now()
      : Date.now();
  }

  function clearTimer(state) {
    if (state.timer) {
      clearTimeout(state.timer);
      state.timer = 0;
    }
    state.timerDue = 0;
    state.timerForce = false;
  }

  function armTimer(state, delayMs, force) {
    const due = monotonicNow() + delayMs;
    if (state.timer) {
      if (state.timerDue <= due) {
        if (force) state.timerForce = true;
        return;
      }
      clearTimer(state);
    }
    state.timerDue = due;
    state.timerForce = Boolean(force);
    state.timer = window.setTimeout(() => {
      const runForce = state.timerForce;
      state.timer = 0;
      state.timerDue = 0;
      state.timerForce = false;
      void maybeRun(state, runForce);
    }, delayMs);
  }

  function scheduleStreamWait(state, fullSource, currentSource, signature, force) {
    if (!state.info.streaming || !state.info.open || !state.block.isConnected) return;
    if (state.enhanced) return;
    const waitedSource = stableStreamSource(fullSource, true);
    if (waitedSource.length <= currentSource.length || !waitedSource.trim()) return;
    if (waitedSource === state.source && state.rendered) return;
    if (!force && state.failedSource && waitedSource === state.failedSource && signature === state.failedSignature) {
      return;
    }
    const elapsed = state.streamSince > 0 ? Date.now() - state.streamSince : 0;
    const remaining = Math.max(20, STREAM_MAX_WAIT_MS - elapsed);
    armTimer(state, remaining, false);
  }

  function schedule(state, force) {
    if (!state.info.open) {
      cancel(state);
      return;
    }
    // 流式输出期间按固定间隔重试，避免每个 token 都发一次请求。
    if (state.info.streaming) {
      if (force) {
        clearTimer(state);
        void maybeRun(state, true);
        return;
      }
      if (!state.streamSince) state.streamSince = Date.now();
      armTimer(state, STREAM_DEBOUNCE_MS, false);
      return;
    }
    state.streamSince = 0;
    clearTimer(state);
    void maybeRun(state, force);
  }

  function cancel(state) {
    state.generation += 1;
    state.running = false;
    clearTimer(state);
    state.streamSince = 0;
  }

  async function maybeRun(state, force) {
    if (!state.block.isConnected || !state.info.open) return;
    state.bar.hidden = false;

    // 宿主可能替换正文节点（虚拟列表），此时旧译文与旧状态全部作废。
    const body = state.enhanced ? state.block.querySelector("[data-ekdp-thinking-body]") : state.block.querySelector(".think-body-inner") ?? state.block.querySelector(".think-body");
    if (!body) return;
    if (body !== state.body) {
      state.generation += 1;
      state.running = false;
      clearTimer(state);
      state.body = body;
      state.sourceNode = state.enhanced ? null : pickSource(body);
      state.source = "";
      state.done = false;
      state.rendered = false;
      state.skipped = false;
      state.translatedSource = "";
      state.translatedPrefix = "";
      state.completedSource = "";
      state.completedPrefix = "";
      state.failedSource = "";
      state.failedSignature = "";
      state.streamSince = 0;
      setView(state, "original");
    }
    if (!state.bar.isConnected) {
      const head = state.enhanced ? state.block.querySelector("summary") : state.block.querySelector("button.think-head");
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
      state.translatedPrefix = "";
      state.completedSource = "";
      state.completedPrefix = "";
      setView(state, "original");
    }
    if (source !== state.source && state.done) {
      state.done = false;
      state.rendered = false;
      setView(state, "original");
      if (state.enhanced && state.requested) state.requested = false;
    } else if (state.done) {
      if (!state.skipped && !state.body.querySelector(".ekdp-kct-translated")) state.done = false;
      setView(state, state.view);
    }

    const settings = current();
    if (!state.running && state.info.open && ((!state.enhanced && settings.autoTranslate) || state.requested || force)) {
      if (!source.trim()) return;
      if (source === state.source && state.done) return;
      if (settings.provider === "custom" && !customEndpoint(settings)) {
        const raw = String(settings.customEndpoint || "").trim();
        setStatus(
          state,
          "error",
          raw ? "端点地址无效" : "请先设置自定义端点",
          raw
            ? "远程端点必须使用 HTTPS；本机地址可用 http://127.0.0.1、http://[::1] 或 http://localhost。"
            : "展开思考块工具栏的“设置”，填写自定义服务地址。",
        );
        return;
      }
      await run(state, force);
    }
  }

  async function run(state, force = false) {
    if (state.running) return;
    if (state.enhanced && state.info.streaming) {
      setStatus(state, "loading", "等待思考完成");
      state.running = false;
      state.generation += 1;
      return;
    }
    const fullSource = readSource(state);
    const settings = current();
    const lang = resolveTarget(settings);
    const signature = translationSignature(settings);
    let source = fullSource;
    let waited = false;
    if (state.info.streaming) {
      if (!state.streamSince) state.streamSince = Date.now();
      waited = Date.now() - state.streamSince >= STREAM_MAX_WAIT_MS;
      source = stableStreamSource(fullSource, waited);
      if (!source.trim()) {
        if (!waited) scheduleStreamWait(state, fullSource, source, signature, force);
        return;
      }
    }
    if (force) {
      state.failedSource = "";
      state.failedSignature = "";
    } else if (state.failedSource) {
      if (source === state.failedSource && signature === state.failedSignature) {
        if (state.info.streaming && !waited) scheduleStreamWait(state, fullSource, source, signature, false);
        return;
      }
      state.failedSource = "";
      state.failedSignature = "";
    }
    if (translationPieces(fullSource, lang).every(piece => piece.skip)) {
      render(state, fullSource);
      if (state.info.streaming && fullSource !== state.source) {
        state.streamSince = Date.now();
      }
      state.source = fullSource;
      state.done = !state.info.streaming;
      state.failedSource = "";
      state.failedSignature = "";
      setStatus(state, "ok", "");
      return;
    }
    if (source === state.source && state.rendered) {
      if (!state.info.streaming) {
        state.done = true;
        // 流式期间的收尾分支：全部内容已译出但状态还停在「后续内容待翻译」，
        // 这里必须一并清掉。
        setStatus(state, "ok", "");
      } else if (!waited) {
        scheduleStreamWait(state, fullSource, source, signature, force);
      }
      return;
    }

    const baseSource = state.completedSource;
    const baseTranslated = state.completedPrefix;
    const incremental =
      Boolean(baseSource) &&
      state.signature === signature &&
      hasSafePrefixBoundary(baseSource, source);
    const delta = incremental ? source.slice(baseSource.length) : source;
    if (incremental && translationPieces(delta, lang).every(piece => piece.skip)) {
      const combined = preserveFencedCode(source, baseTranslated + delta);
      state.translatedPrefix = combined;
      state.translatedSource = source;
      state.completedPrefix = combined;
      state.completedSource = source;
      render(state, combined + fullSource.slice(source.length));
      state.done = fullSource === source && !state.info.streaming;
      if (state.info.streaming && source !== state.source) state.streamSince = Date.now();
      state.source = source;
      state.failedSource = "";
      state.failedSignature = "";
      state.signature = signature;
      setStatus(state, state.done ? "ok" : "loading", state.done ? "" : "后续内容待翻译");
      if (state.info.streaming) scheduleStreamWait(state, fullSource, source, signature, false);
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
      const translated = await engine.translate(delta, lang, (text, done, total) => {
        if (!valid()) return;
        const text2 = readSource(state);
        if (!text2.startsWith(source)) return;
        const combined = incremental ? preserveFencedCode(source, baseTranslated + text) : text;
        state.translatedPrefix = combined;
        state.translatedSource = source;
        render(state, combined + text2.slice(source.length));
        setStatus(state, "loading", `翻译中 ${done}/${total}`);
      }, valid);
      if (!valid()) return;
      const nowSource = readSource(state);
      if (!nowSource.startsWith(source)) return;
      const finalTranslated = incremental ? preserveFencedCode(source, baseTranslated + translated) : translated;
      state.translatedPrefix = finalTranslated;
      state.translatedSource = source;
      state.completedPrefix = finalTranslated;
      state.completedSource = source;
      render(state, finalTranslated + nowSource.slice(source.length));
      state.done = nowSource === source && !state.info.streaming;
      if (state.info.streaming && source !== state.source) state.streamSince = Date.now();
      state.source = source;
      state.failedSource = "";
      state.failedSignature = "";
      state.signature = signature;
      setStatus(state, state.done ? "ok" : "loading", state.done ? "" : "后续内容待翻译");
    } catch (error) {
      if (!valid()) return;
      state.failedSource = source;
      state.failedSignature = signature;
      setStatus(state, "error", `重试 · ${errorBrief(error)}`, `翻译失败：${messageOf(error)}`);
    } finally {
      if (state.generation === generation) {
        state.running = false;
        const currentFull = readSource(state);
        // valid() 为假除了折叠与断开，还可能是原文被整体替换：这时必须重新调
        // 度，否则新原文永远得不到翻译，状态也停在「翻译中…」。schedule 会经
        // maybeRun 按当前状态重新判断，块已折叠或已断开时它自己会退出。
        if (!valid() || currentFull !== fullSource) {
          schedule(state, false);
        } else if (state.info.streaming && state.block.isConnected && state.info.open) {
          const unlocked = stableStreamSource(currentFull, true);
          if (
            unlocked.length > source.length &&
            !(state.failedSource && state.failedSignature === signature && unlocked === state.failedSource)
          ) {
            scheduleStreamWait(state, currentFull, source, signature, false);
          }
        }
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
    const source = state.enhanced ? null : pickSource(state.body);
    if (state.enhanced || source) {
      if (source) state.sourceNode = source;
      // Written as an attribute rather than a class: the hub observes class
      // changes on host nodes, and a class toggle here would look like host churn
      // and invalidate the very translation that caused it.
      const originals = source ? [source] : [];
      const siblings = state.body.children;
      if (siblings && typeof siblings.length === "number") {
        for (let index = 0; index < siblings.length; index += 1) {
          const child = siblings[index];
          if (!child || child === source) continue;
          if (child.classList?.contains("ekdp-kct-translated")) continue;
          originals.push(child);
        }
      }
      for (const node of originals) {
        if (view === "translated") node.setAttribute("data-ekdp-kct-hidden", "1");
        else node.removeAttribute("data-ekdp-kct-hidden");
      }
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
      const source = state.enhanced ? null : pickSource(state.body);
      node = source?.cloneNode(false) ?? document.createElement(state.enhanced ? "div" : "pre");
      node.removeAttribute("id");
      node.removeAttribute("data-ekdp-kct-hidden");
      node.classList.add("ekdp-kct-translated");
      node.setAttribute("data-ekdp-owned", "1");
      state.body.appendChild(node);
    }
    if (node.textContent !== text) node.textContent = text;
    state.rendered = true;
    setView(state, state.preferOriginal ? "original" : "translated");
  }

  function readSource(state) {
    if (state.enhanced) {
      const source = state.body.getAttribute("data-ekdp-thinking-source");
      return source ?? sourceFromBody(state.body);
    }
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
  // 只写回新增或变更的条目：整表重写会在每次 flush 时把 200 条译文全部
  // 重新 set 一遍（每条都是一次桥调用）。
  const dirtyKeys = new Set();
  // prune 淘汰的键要先从磁盘删掉，否则它们会一直占着存储却再也读不到。
  const deletedKeys = new Set();
  // 清除缓存会递增它。清除时仍在途的翻译属于上一代缓存，它们完成时不能再把
  // 刚删掉的条目写回磁盘，也不能把旧设置下的译文当作命中。
  let cacheGeneration = 0;

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
    const removed = [...deletedKeys];
    deletedKeys.clear();
    for (const key of removed) {
      await ctx.storage.delete(CACHE_ENTRY_PREFIX + key).catch(() => {});
    }
    const changed = [...dirtyKeys];
    dirtyKeys.clear();
    for (const key of changed) {
      if (!index.has(key)) continue;
      const text = cache.get(key);
      if (typeof text !== "string") continue;
      if (text.length * 2 > 32000) continue;
      await ctx.storage.set(CACHE_ENTRY_PREFIX + key, text).catch(() => {});
    }
  }

  function prune(now = Date.now()) {
    const drop = (key) => {
      index.delete(key);
      cache.delete(key);
      dirtyKeys.delete(key);
      deletedKeys.add(key);
      indexDirty = true;
    };
    for (const [key, usedAt] of index) {
      if (now - usedAt >= CACHE_TTL_MS) drop(key);
    }
    if (index.size <= CACHE_LIMIT) return;
    const stale = [...index.entries()].sort((a, b) => a[1] - b[1]).slice(0, index.size - CACHE_LIMIT);
    for (const [key] of stale) drop(key);
  }

  function remember(key, text) {
    if (text.length * 2 > 32000) return;
    cache.set(key, text);
    index.delete(key);
    index.set(key, Date.now());
    dirtyKeys.add(key);
    deletedKeys.delete(key);
    indexDirty = true;
    persist();
  }

  function clearMemoryCache() {
    cacheGeneration += 1;
    cache.clear();
    index.clear();
    dirtyKeys.clear();
    deletedKeys.clear();
    indexDirty = false;
    if (persistTimer) window.clearTimeout(persistTimer);
    persistTimer = 0;
  }

  await loadSettings();
  await loadCache().catch(() => {});

  const queue = [];
  let active = 0;
  let cooldownUntil = 0;
  // 在途请求登记表：键由缓存代次、服务、端点/模型、凭据口径、语言与片段摘要组
  // 成，值是可共享的请求槽位。相同内容、相同配置的翻译挂到同一槽位，只发一次
  // 请求；槽位的消费者全部失效后，还没出队的任务会被丢弃，不再发往任何服务。
  const inflight = new Map();

  // 出队与重试前核对：摘掉已失效的消费者，返回是否还有效。
  function slotLive(slot) {
    let live = false;
    for (const isValid of slot.lives) {
      if (isValid()) live = true;
      else slot.lives.delete(isValid);
    }
    return live;
  }

  function settleSlot(slot, text, error) {
    if (slot.done) return;
    slot.done = true;
    slot.text = text;
    slot.error = error;
    if (inflight.get(slot.key) === slot) inflight.delete(slot.key);
    const waiters = slot.waiters;
    slot.waiters = [];
    for (const waiter of waiters) waiter();
  }

  function snapshotSettings() {
    return {
      provider: settings.provider,
      customEndpoint: settings.customEndpoint,
      customModel: settings.customModel,
      useCredential: settings.useCredential,
    };
  }

  function pump() {
    while (active < CONCURRENCY && queue.length) {
      const job = queue.shift();
      if (!job.slots.some(slotLive)) {
        job.cancel();
        continue;
      }
      if (Date.now() < cooldownUntil) {
        queue.unshift(job);
        setTimeout(pump, cooldownUntil - Date.now());
        return;
      }
      for (const slot of job.slots) slot.started = true;
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
          // 退避期间消费者可能已失效（折叠、设置变更、停用）：不再重试。
          if (!job.slots.some(slotLive)) {
            job.cancel();
            return;
          }
          continue;
        }
        job.reject(error);
        return;
      }
    }
  }

  function callProvider(job) {
    const snapshot = job.snapshot;
    if (snapshot.provider === "google") return translateGoogle(ctx, job.texts[0], job.lang);
    if (snapshot.provider === "bing") return translateBing(ctx, job.texts, job.lang);
    return translateCustom(ctx, job.texts, job.lang, snapshot);
  }

  async function translate(source, lang, onProgress, valid) {
    // 设置在进入时定格：缓存键与实际请求必须来自同一组服务信息，排队或重试期
    // 间被改动的设置不能改变已在途任务的去向，也不能让响应串进别的服务的缓存。
    const snapshot = snapshotSettings();
    const generation = cacheGeneration;
    const provider = snapshot.provider;
    const scope = provider === "custom" ? `${snapshot.customEndpoint}\u0000${snapshot.customModel}` : "";
    const prefix = hashKey(`${provider}\u0000${lang}\u0000${scope ? scope + "\u0000" : ""}`);
    const sharePrefix = hashKey(
      `${provider}\u0000${lang}\u0000${provider === "custom" ? `${scope}\u0000${snapshot.useCredential}\u0000` : ""}`,
    );
    const pieces = translationPieces(source, lang);
    const out = pieces.map(piece => piece.text);
    const missing = [];
    let completed = 0;
    let firstError = null;
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

    // 已有在途请求且仍在执行（或仍有有效消费者）的片段直接挂靠，不再发请求；
    // 其余片段新建槽位并按服务要求分批。挂靠与新建在此汇合，下面的等待、进度
    // 与缓存逻辑对两者一视同仁。
    for (const item of missing) {
      const shareKey = `${generation}\u0000${sharePrefix}:${hashKey(item.text)}`;
      const existing = inflight.get(shareKey);
      if (existing && (existing.started || slotLive(existing))) {
        existing.lives.add(valid);
        item.slot = existing;
      } else {
        const slot = { key: shareKey, lives: new Set([valid]), waiters: [], started: false, done: false, text: "", error: null };
        inflight.set(shareKey, slot);
        item.slot = slot;
        item.fresh = true;
      }
    }

    let pending = missing.length;
    function applyItem(item, translated) {
      const safe = preserveFencedCode(item.text, translated);
      out[item.index] = safe;
      // The user cleared the cache while this request was running: the text is
      // still delivered, but it must not repopulate the cache that was deleted.
      if (generation === cacheGeneration) remember(item.key, safe);
    }
    function settle(item, error) {
      if (item.settled) return;
      item.settled = true;
      pending = Math.max(0, pending - 1);
      if (error) {
        firstError ??= error;
        persist();
      }
      if (!valid()) return;
      // The count is paragraphs, so a batch of eight still reports eight.
      const done = total - pending;
      completed = done;
      onProgress(preserveFencedCode(source, out.join("")), done, total);
      persist();
    }
    // 槽位由发请求的一方结算；挂靠方与请求方都在这里等到同一个结果。
    const awaited = missing.map(item => new Promise(resolve => {
      item.slot.waiters.push(() => {
        if (item.slot.error) settle(item, item.slot.error);
        else {
          applyItem(item, item.slot.text);
          settle(item, null);
        }
        resolve();
      });
    }));

    const batches = [];
    if (provider === "google") {
      for (const item of missing) if (item.fresh) batches.push([item]);
    } else {
      let batch = [];
      let chars = 0;
      const numbering = provider === "custom" ? 4 : 0;
      for (const item of missing) {
        if (!item.fresh) continue;
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
    const retryAlone = item => {
      queue.push({
        snapshot,
        lang,
        texts: [item.text],
        slots: [item.slot],
        resolve: results => {
          const text = results?.[0];
          if (typeof text === "string" && text.trim()) settleSlot(item.slot, text, null);
          else settleSlot(item.slot, null, new Error("译文为空，请重试"));
        },
        reject: error => settleSlot(item.slot, null, error),
        cancel: () => settleSlot(item.slot, null, new Error("翻译已取消")),
      });
      pump();
    };

    for (const batch of batches) {
      const slots = batch.map(item => item.slot);
      queue.push({
        snapshot,
        lang,
        texts: batch.map(item => item.text),
        slots,
        resolve: results => {
          const failed = [];
          for (const [i, item] of batch.entries()) {
            const translated = results?.[i];
            if (typeof translated === "string" && translated.trim()) settleSlot(slots[i], translated, null);
            else failed.push(item);
          }
          // Batching changes the service's answer shape, not the user's text;
          // retrying one by one keeps the parts that worked.
          for (const item of failed) retryAlone(item);
        },
        reject: error => {
          for (const slot of slots) settleSlot(slot, null, error);
        },
        cancel: () => {
          for (const slot of slots) settleSlot(slot, null, new Error("翻译已取消"));
        },
      });
    }
    pump();

    await Promise.all(awaited);
    persist();
    if (firstError) throw firstError;
    const final = preserveFencedCode(source, out.join(""));
    if (valid()) onProgress(final, total, total);
    return final;
  }

  function dispose() {
    // 排队中的任务不会再被处理：槽位结算后所有等待它的 promise 都会结束，调度
    // 方不会挂起；冷却与退避定时器也要清掉，否则插件停用后仍有回调在跑。
    for (const job of queue.splice(0)) job.cancel();
    for (const slot of [...inflight.values()]) settleSlot(slot, null, new Error("翻译已停止"));
    active = 0;
    cooldownUntil = 0;
    if (persistTimer) window.clearTimeout(persistTimer);
    persistTimer = 0;
    dirtyKeys.clear();
    deletedKeys.clear();
  }

  return { settings, translate, loadSettings, clearMemoryCache, dispose, cacheCount: () => cache.size };
}

// ------------------------------------------------------------- 内联设置

// 每个思考块工具栏上的“设置”共用同一个浮层。浮层写入与设置页相同的存储键并
// 派发 ekdp-kct:settings，因此内联改动会让已展开的思考块按新设置重译，设置页
// 也能读到同一份值。
function createSettingsPopover({ ctx, engine, blocks, root, doc = root.ownerDocument ?? document }) {
  let panel = null;
  let anchor = null;

  function toggle(button) {
    if (panel && anchor === button) {
      close();
      return;
    }
    open(button);
  }

  function open(button) {
    close();
    anchor = button;
    panel = document.createElement("div");
    panel.className = "ekdp-kct-panel";
    panel.dataset.ekdpOwned = "1";
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-label", "思考内容翻译设置");
    anchor.setAttribute("aria-expanded", "true");
    doc.body.appendChild(panel);
    render();
    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeyDown, true);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
  }

  function close(restoreFocus = false) {
    if (!panel) return;
    panel.remove();
    panel = null;
    if (anchor?.isConnected) {
      anchor.setAttribute("aria-expanded", "false");
      if (restoreFocus) anchor.focus();
    }
    anchor = null;
    window.removeEventListener("pointerdown", onPointerDown, true);
    window.removeEventListener("keydown", onKeyDown, true);
    window.removeEventListener("resize", place);
    window.removeEventListener("scroll", place, true);
  }

  function closeFor(button) {
    if (anchor === button) close();
  }

  function refresh() {
    if (panel) render();
  }

  function dispose() {
    close();
  }

  function onPointerDown(event) {
    if (!panel) return;
    const target = event.composedPath?.()[0] ?? event.target;
    if (panel.contains(target) || anchor?.contains(target)) return;
    close();
  }

  function onKeyDown(event) {
    if (!panel || event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    close(true);
  }

  function place() {
    if (!panel) return;
    if (!anchor?.isConnected) {
      close();
      return;
    }
    const rect = anchor.getBoundingClientRect();
    const width = Math.min(320, Math.max(240, window.innerWidth - 16));
    panel.style.width = `${width}px`;
    panel.style.visibility = "hidden";
    const height = panel.offsetHeight;
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 8));
    let top = rect.bottom + 6;
    if (top + height > window.innerHeight - 8) {
      top = Math.max(8, rect.top - height - 6);
    }
    panel.style.left = `${left}px`;
    panel.style.top = `${top}px`;
    panel.style.visibility = "visible";
  }

  function render() {
    if (!panel) return;
    panel.replaceChildren();

    const heading = document.createElement("div");
    heading.className = "ekdp-kct-panel-head";
    const title = document.createElement("strong");
    title.textContent = "翻译设置";
    const closeButton = document.createElement("button");
    closeButton.type = "button";
    closeButton.className = "ekdp-kct-btn";
    closeButton.textContent = "关闭";
    closeButton.addEventListener("click", () => close(true));
    heading.append(title, closeButton);

    const note = document.createElement("p");
    note.className = "ekdp-kct-note";
    note.setAttribute("aria-live", "polite");

    async function apply(patch) {
      const next = { ...engine.settings, ...patch };
      try {
        await ctx.storage.set(SETTINGS_KEY, next);
        Object.assign(engine.settings, next);
        window.dispatchEvent(new CustomEvent("ekdp-kct:settings", { detail: { source: "inline" } }));
      } catch (error) {
        note.textContent = `保存失败：${messageOf(error)}`;
      }
    }

    const language = selectControl(
      [[AUTO, "跟随界面语言"], ...TARGET_LANGUAGES.map(code => [code, LANGUAGE_LABELS[code] ?? code])],
      engine.settings.targetLanguage,
      value => void apply({ targetLanguage: value }),
    );
    const provider = selectControl(
      PROVIDERS.map(value => [value, PROVIDER_LABELS[value]]),
      engine.settings.provider,
      value => {
        void apply({ provider: value }).then(render);
      },
    );

    const auto = document.createElement("label");
    auto.className = "ekdp-kct-check";
    const autoInput = document.createElement("input");
    autoInput.type = "checkbox";
    autoInput.checked = engine.settings.autoTranslate !== false;
    autoInput.addEventListener("change", () => void apply({ autoTranslate: autoInput.checked }));
    auto.append(autoInput, document.createTextNode(" 展开后自动翻译"));

    panel.append(heading, field("目标语言", language), field("翻译服务", provider), auto);
    if (engine.settings.provider === "custom") panel.append(customFields(apply));

    const stats = document.createElement("p");
    stats.className = "ekdp-kct-note ekdp-kct-stats";
    const refreshStats = () => {
      stats.textContent =
        `${blocks.size} 个思考块 · 缓存 ${engine.cacheCount()} 段 · ` +
        `${PROVIDER_LABELS[engine.settings.provider] ?? engine.settings.provider}`;
    };
    refreshStats();

    const clear = document.createElement("button");
    clear.type = "button";
    clear.className = "ekdp-kct-btn";
    clear.textContent = "清除翻译缓存";
    clear.addEventListener("click", async () => {
      clear.disabled = true;
      try {
        const keys = await ctx.storage.list(CACHE_ENTRY_PREFIX);
        for (const key of keys) await ctx.storage.delete(key);
        await ctx.storage.delete(CACHE_INDEX_KEY);
        await ctx.storage.delete("cache.v1");
        engine.clearMemoryCache();
        window.dispatchEvent(new CustomEvent("ekdp-kct:cache", { detail: { source: "inline" } }));
        clear.textContent = "缓存已清除";
        refreshStats();
      } catch (error) {
        stats.textContent = `清除缓存失败：${messageOf(error)}`;
      } finally {
        clear.disabled = false;
      }
    });

    const tools = document.createElement("div");
    tools.className = "ekdp-kct-panel-actions";
    tools.append(clear);

    const hint = document.createElement("p");
    hint.className = "ekdp-kct-note";
    hint.textContent =
      engine.settings.provider === "custom"
        ? "思考原文会发送到你配置的自定义端点。"
        : "思考原文会发送到所选公共翻译服务，可能包含代码与会话细节。";

    panel.append(tools, stats, hint, note);
    place();
  }

  function customFields(apply) {
    const wrap = document.createElement("div");
    wrap.className = "ekdp-kct-custom";

    const endpointInput = document.createElement("input");
    endpointInput.type = "url";
    endpointInput.className = "ekdp-kct-input";
    endpointInput.placeholder = "https://…/v1/chat/completions";
    endpointInput.value = engine.settings.customEndpoint || "";
    const endpointNote = document.createElement("p");
    endpointNote.className = "ekdp-kct-note";
    endpointNote.textContent = engine.settings.customEndpoint ? "" : "尚未填写端点，翻译会暂停。";
    endpointInput.addEventListener("change", () => {
      const raw = endpointInput.value.trim();
      if (raw && !customEndpoint({ customEndpoint: raw })) {
        endpointNote.textContent =
          "远程端点必须使用 HTTPS；本机地址可用 http://127.0.0.1、http://[::1] 或 http://localhost。";
        return;
      }
      endpointNote.textContent = raw ? "" : "尚未填写端点，翻译会暂停。";
      void apply({ customEndpoint: customEndpoint({ customEndpoint: raw }) });
    });

    const modelInput = document.createElement("input");
    modelInput.type = "text";
    modelInput.className = "ekdp-kct-input";
    modelInput.placeholder = "模型名（可选）";
    modelInput.value = engine.settings.customModel || "";
    modelInput.addEventListener("change", () => void apply({ customModel: modelInput.value.trim() }));

    const credential = document.createElement("p");
    credential.className = "ekdp-kct-note";
    void refreshCredential(credential);

    const openSettings = document.createElement("button");
    openSettings.type = "button";
    openSettings.className = "ekdp-kct-btn";
    openSettings.textContent = "打开插件设置";
    openSettings.addEventListener("click", () => {
      if (!ctx.ui.openSettings) {
        credential.textContent = "当前宿主没有打开设置页的入口，请在插件列表里打开“思考内容翻译”。";
        return;
      }
      void ctx.ui.openSettings("plugins-ekd").catch(error => {
        credential.textContent = `无法打开设置页：${messageOf(error)}`;
      });
    });

    const endpointField = field("自定义端点（OpenAI 兼容）", endpointInput);
    endpointField.append(endpointNote);
    wrap.append(endpointField, field("模型名", modelInput), credential, openSettings);
    return wrap;
  }

  async function refreshCredential(node) {
    try {
      const result = await ctx.core.request("credentials.list");
      const entry = Array.isArray(result?.items) ? result.items.find(item => item?.name === CREDENTIAL) : null;
      node.textContent = entry?.stored
        ? entry.unlocked
          ? "密钥已保存，请求时由核心注入。"
          : "密钥已保存，但凭据库处于锁定状态；解锁后才能请求。"
        : "尚未保存密钥；多数自定义服务需要密钥。";
    } catch (error) {
      node.textContent = `无法读取凭据状态：${messageOf(error)}`;
    }
  }

  function field(labelText, control) {
    const wrap = document.createElement("label");
    wrap.className = "ekdp-kct-field";
    const caption = document.createElement("span");
    caption.textContent = labelText;
    wrap.append(caption, control);
    return wrap;
  }

  function selectControl(options, value, onChange) {
    const select = document.createElement("select");
    select.className = "ekdp-kct-input";
    for (const [optionValue, label] of options) {
      const option = document.createElement("option");
      option.value = optionValue;
      option.textContent = label;
      select.append(option);
    }
    select.value = value;
    select.addEventListener("change", () => onChange(select.value));
    return select;
  }

  return { toggle, closeFor, refresh, dispose };
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
  if (url.username || url.password || url.hash) return "";
  // 远程端点由清单的 net:https 授权（只允许 TLS）；本机服务用清单列出的字面目标。
  if (url.protocol === "https:") return url.href;
  if (url.protocol === "http:" && LOCAL_ENDPOINT_HOSTS.has(url.hostname)) return url.href;
  return "";
}

async function request(ctx, url, init) {
  let response;
  try {
    response = await ctx.net.fetch(url, init);
  } catch (error) {
    // 核心的错误信息说明了拒绝原因（DNS、代理、能力、凭据……），换掉它会让
    // 用户只能看到“失败”而无法判断该改哪里。
    throw Object.assign(new Error(messageOf(error) || "网络请求失败"), {
      status: Number(error?.status) || 0,
      cause: error,
    });
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

const FENCE_OPEN_RE = /^ {0,3}(`{3,})[^`\n]*(?:\n|$)|^ {0,3}(~{3,})[^\n]*(?:\n|$)/gm;
const BARE_URL_RE = /https?:\/\/[^\s`<>。！？；，]+/g;
const UNCLOSED_URL_RE = /https?:\/\/[^\s`<>。！？；，)]*$/;
const EMAIL_LOCAL_CHAR_RE = /^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]$/;

function splitFencedSegments(text) {
  const segments = [];
  const re = new RegExp(FENCE_OPEN_RE.source, "gm");
  let last = 0;
  let match;
  while ((match = re.exec(text)) !== null) {
    if (match.index > last) {
      segments.push({ start: last, end: match.index, fenced: false, unclosed: false });
    }
    const fence = match[1] || match[2];
    const closing = new RegExp(`^ {0,3}${fence[0]}{${fence.length},}[ \\t]*\\r?$`, "gm");
    closing.lastIndex = re.lastIndex;
    const end = closing.exec(text);
    const fenceEnd = end ? closing.lastIndex : text.length;
    segments.push({ start: match.index, end: fenceEnd, fenced: true, unclosed: !end });
    re.lastIndex = fenceEnd;
    last = fenceEnd;
  }
  if (last < text.length) {
    segments.push({ start: last, end: text.length, fenced: false, unclosed: false });
  }
  return segments;
}

function fencedTexts(text) {
  return splitFencedSegments(text)
    .filter(seg => seg.fenced)
    .map(seg => text.slice(seg.start, seg.end));
}

// 译文里的围栏必须与原文一一对应，否则说明服务改写了代码，整块退回原文。
function preserveFencedCode(source, translated) {
  const original = fencedTexts(source);
  const result = fencedTexts(translated);
  return original.length === result.length && original.every((text, i) => text === result[i])
    ? translated
    : source;
}

function lineBreakLength(text, index) {
  if (text[index] === "\n") return 1;
  if (text[index] === "\r" && text[index + 1] === "\n") return 2;
  return 0;
}

function isBlankLineAt(text, index, lb = lineBreakLength(text, index)) {
  if (!lb) return false;
  let p = index + lb;
  while (p < text.length && (text[p] === " " || text[p] === "\t")) p += 1;
  return lineBreakLength(text, p) > 0;
}

function isEscaped(text, index) {
  let backslashes = 0;
  for (let i = index - 1; i >= 0 && text[i] === "\\"; i -= 1) backslashes += 1;
  return backslashes % 2 === 1;
}

function findClosingBackticks(text, searchStart, tickLen) {
  const len = text.length;
  let p = searchStart;
  while (p < len) {
    const lb = lineBreakLength(text, p);
    if (lb > 0) {
      if (isBlankLineAt(text, p, lb)) return { status: "blank" };
      p += lb;
      continue;
    }
    if (text[p] === "`") {
      let q = p + 1;
      while (q < len && text[q] === "`") q += 1;
      if (q - p === tickLen) return { status: "closed", end: q };
      p = q;
      continue;
    }
    p += 1;
  }
  return { status: "unclosed" };
}

function skipLinkWhitespace(text, k) {
  const len = text.length;
  let lineBreaks = 0;
  let sawSpace = false;
  while (k < len) {
    const ch = text[k];
    if (ch === " " || ch === "\t") {
      sawSpace = true;
      k += 1;
      continue;
    }
    const lb = lineBreakLength(text, k);
    if (lb > 0) {
      lineBreaks += 1;
      if (lineBreaks > 1) return { invalid: true, next: k, sawSpace };
      sawSpace = true;
      k += lb;
      continue;
    }
    break;
  }
  return { invalid: false, next: k, sawSpace };
}

function parseLinkParen(text, openParen) {
  const len = text.length;
  const lead = skipLinkWhitespace(text, openParen + 1);
  if (lead.invalid) return { status: "invalid" };
  let k = lead.next;
  if (k >= len) return { status: "unclosed" };
  if (text[k] === ")") return { status: "complete", end: k + 1 };

  if (text[k] === "<") {
    k += 1;
    let closedAngle = false;
    while (k < len) {
      const ch = text[k];
      if (ch === "\r" || ch === "\n" || ch === "<") return { status: "invalid" };
      if (ch === "\\") {
        if (k + 1 >= len) return { status: "unclosed" };
        if (text[k + 1] === "\r" || text[k + 1] === "\n") return { status: "invalid" };
        k += 2;
        continue;
      }
      if (ch === ">") {
        k += 1;
        closedAngle = true;
        break;
      }
      k += 1;
    }
    if (!closedAngle) return { status: "unclosed" };
  } else {
    let parenDepth = 0;
    const destStart = k;
    while (k < len) {
      const ch = text[k];
      if (ch === "\\") {
        if (k + 1 >= len) return { status: "unclosed" };
        const nextCode = text.charCodeAt(k + 1);
        if (nextCode <= 0x20 || nextCode === 0x7f) {
          k += 1;
          continue;
        }
        k += 2;
        continue;
      }
      if (ch === "(") {
        parenDepth += 1;
        k += 1;
        continue;
      }
      if (ch === ")") {
        if (parenDepth === 0) return { status: "complete", end: k + 1 };
        parenDepth -= 1;
        k += 1;
        continue;
      }
      if (ch === " " || ch === "\t" || ch === "\n" || ch === "\r") {
        if (parenDepth > 0) return { status: "invalid" };
        break;
      }
      const code = ch.charCodeAt(0);
      if (code < 0x20 || code === 0x7f) return { status: "invalid" };
      k += 1;
    }
    if (k >= len) return { status: "unclosed" };
    if (k === destStart) return { status: "invalid" };
  }

  if (k >= len) return { status: "unclosed" };
  if (text[k] === ")") return { status: "complete", end: k + 1 };

  const afterDest = skipLinkWhitespace(text, k);
  if (afterDest.invalid || !afterDest.sawSpace) return { status: "invalid" };
  k = afterDest.next;
  if (k >= len) return { status: "unclosed" };
  if (text[k] === ")") return { status: "complete", end: k + 1 };

  const openQuote = text[k];
  if (openQuote !== '"' && openQuote !== "'" && openQuote !== "(") return { status: "invalid" };
  const closeQuote = openQuote === "(" ? ")" : openQuote;
  k += 1;
  let closedTitle = false;
  while (k < len) {
    const lb = lineBreakLength(text, k);
    if (lb > 0) {
      if (isBlankLineAt(text, k, lb)) return { status: "invalid" };
      k += lb;
      continue;
    }
    const ch = text[k];
    if (ch === "\\") {
      if (k + 1 >= len) return { status: "unclosed" };
      if (text[k + 1] === "\r" || text[k + 1] === "\n") {
        k += 1;
        continue;
      }
      k += 2;
      continue;
    }
    if (closeQuote === ")" && ch === "(") return { status: "invalid" };
    if (ch === closeQuote) {
      k += 1;
      closedTitle = true;
      break;
    }
    k += 1;
  }
  if (!closedTitle) return { status: "unclosed" };

  const afterTitle = skipLinkWhitespace(text, k);
  if (afterTitle.invalid) return { status: "invalid" };
  k = afterTitle.next;
  if (k >= len) return { status: "unclosed" };
  if (text[k] === ")") return { status: "complete", end: k + 1 };
  return { status: "invalid" };
}

function isAsciiLetter(code) {
  return (code >= 0x41 && code <= 0x5a) || (code >= 0x61 && code <= 0x7a);
}

function isAsciiDigit(code) {
  return code >= 0x30 && code <= 0x39;
}

function isAsciiAlnum(code) {
  return isAsciiLetter(code) || isAsciiDigit(code);
}

function isSchemeChar(code) {
  return isAsciiAlnum(code) || code === 0x2b || code === 0x2d || code === 0x2e;
}

function isTagNameChar(code) {
  return isAsciiAlnum(code) || code === 0x2d;
}

function isAttrNameStart(code) {
  return isAsciiLetter(code) || code === 0x5f || code === 0x3a;
}

function isAttrNameChar(code) {
  return isAsciiAlnum(code) || code === 0x5f || code === 0x2e || code === 0x3a || code === 0x2d;
}

function isUnquotedAttrChar(ch) {
  return ch !== " " && ch !== "\t" && ch !== "\n" && ch !== "\r" &&
    ch !== '"' && ch !== "'" && ch !== "=" && ch !== "<" && ch !== ">" && ch !== "`";
}

function scanUriAutolink(text, openAngle, canUnclose) {
  const len = text.length;
  let k = openAngle + 1;
  if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
  if (!isAsciiLetter(text.charCodeAt(k))) return { status: "invalid" };
  const schemeStart = k;
  k += 1;
  while (k < len && k - schemeStart < 32 && isSchemeChar(text.charCodeAt(k))) k += 1;
  if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
  if (text[k] !== ":" || k - schemeStart < 2) return { status: "invalid" };
  k += 1;
  while (k < len) {
    const ch = text[k];
    if (ch === ">") return { status: "closed", end: k + 1 };
    const code = ch.charCodeAt(0);
    if (code <= 0x20 || code === 0x7f || ch === "<") return { status: "invalid" };
    k += 1;
  }
  return canUnclose ? { status: "unclosed" } : { status: "invalid" };
}

function scanEmailAutolink(text, openAngle, canUnclose) {
  const len = text.length;
  let k = openAngle + 1;
  const localStart = k;
  while (k < len && EMAIL_LOCAL_CHAR_RE.test(text[k])) k += 1;
  if (k === localStart || k >= len || text[k] !== "@") return { status: "invalid" };
  k += 1;
  if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };

  while (k < len) {
    const labelStart = k;
    if (!isAsciiAlnum(text.charCodeAt(k))) return { status: "invalid" };
    k += 1;
    while (k < len && (isAsciiAlnum(text.charCodeAt(k)) || text[k] === "-")) {
      if (k - labelStart >= 63) return { status: "invalid" };
      k += 1;
    }
    const endsWithHyphen = text[k - 1] === "-";
    if (k >= len) {
      if (endsWithHyphen && k - labelStart >= 63) return { status: "invalid" };
      return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    }
    if (endsWithHyphen) return { status: "invalid" };
    if (text[k] === ">") return { status: "closed", end: k + 1 };
    if (text[k] === ".") {
      k += 1;
      if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
      continue;
    }
    return { status: "invalid" };
  }
  return { status: "invalid" };
}

function scanOpenHtmlTag(text, openAngle, canUnclose) {
  const len = text.length;
  let k = openAngle + 1;
  if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
  if (!isAsciiLetter(text.charCodeAt(k))) return { status: "invalid" };
  k += 1;
  while (k < len && isTagNameChar(text.charCodeAt(k))) k += 1;

  while (true) {
    const ws = skipLinkWhitespace(text, k);
    if (ws.invalid) return { status: "invalid" };
    k = ws.next;
    if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };

    if (text[k] === ">") return { status: "closed", end: k + 1 };
    if (text[k] === "/") {
      k += 1;
      if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
      if (text[k] === ">") return { status: "closed", end: k + 1 };
      return { status: "invalid" };
    }

    if (!ws.sawSpace || !isAttrNameStart(text.charCodeAt(k))) return { status: "invalid" };
    k += 1;
    while (k < len && isAttrNameChar(text.charCodeAt(k))) k += 1;

    const afterName = skipLinkWhitespace(text, k);
    if (afterName.invalid) return { status: "invalid" };
    if (afterName.next >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    if (text[afterName.next] !== "=") continue;

    const afterEq = skipLinkWhitespace(text, afterName.next + 1);
    if (afterEq.invalid) return { status: "invalid" };
    k = afterEq.next;
    if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };

    const quote = text[k];
    if (quote === '"' || quote === "'") {
      k += 1;
      let closedQuote = false;
      while (k < len) {
        const lb = lineBreakLength(text, k);
        if (lb > 0) {
          if (isBlankLineAt(text, k, lb)) return { status: "invalid" };
          k += lb;
          continue;
        }
        if (text[k] === quote) {
          k += 1;
          closedQuote = true;
          break;
        }
        k += 1;
      }
      if (!closedQuote) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    } else {
      const valStart = k;
      while (k < len && isUnquotedAttrChar(text[k])) k += 1;
      if (k === valStart) return { status: "invalid" };
    }
  }
}

function scanHtmlInline(text, openAngle, canUnclose) {
  const len = text.length;
  if (openAngle + 1 >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
  const next = text[openAngle + 1];

  if (next === "!") {
    const k = openAngle + 2;
    if (k >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };

    if (text[k] === "-") {
      if (k + 1 >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
      if (text[k + 1] !== "-") return { status: "invalid" };
      let p = openAngle + 4;
      if (p >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
      if (text[p] === ">") return { status: "closed", end: p + 1 };
      if (text[p] === "-" && p + 1 < len && text[p + 1] === ">") return { status: "closed", end: p + 2 };
      while (p < len) {
        const lb = lineBreakLength(text, p);
        if (lb > 0) {
          if (isBlankLineAt(text, p, lb)) return { status: "invalid" };
          p += lb;
          continue;
        }
        if (text[p] === "-" && text[p + 1] === "-" && text[p + 2] === ">") {
          return { status: "closed", end: p + 3 };
        }
        p += 1;
      }
      return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    }

    if (text[k] === "[") {
      const prefix = "[CDATA[";
      let m = 0;
      while (m < prefix.length && k + m < len) {
        if (text[k + m] !== prefix[m]) return { status: "invalid" };
        m += 1;
      }
      if (m < prefix.length) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
      let p = k + prefix.length;
      while (p < len) {
        const lb = lineBreakLength(text, p);
        if (lb > 0) {
          if (isBlankLineAt(text, p, lb)) return { status: "invalid" };
          p += lb;
          continue;
        }
        if (text[p] === "]" && text[p + 1] === "]" && text[p + 2] === ">") {
          return { status: "closed", end: p + 3 };
        }
        p += 1;
      }
      return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    }

    if (isAsciiLetter(text.charCodeAt(k))) {
      let p = k + 1;
      while (p < len) {
        const lb = lineBreakLength(text, p);
        if (lb > 0) {
          if (isBlankLineAt(text, p, lb)) return { status: "invalid" };
          p += lb;
          continue;
        }
        if (text[p] === ">") return { status: "closed", end: p + 1 };
        p += 1;
      }
      return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    }

    return { status: "invalid" };
  }

  if (next === "?") {
    let p = openAngle + 2;
    while (p < len) {
      const lb = lineBreakLength(text, p);
      if (lb > 0) {
        if (isBlankLineAt(text, p, lb)) return { status: "invalid" };
        p += lb;
        continue;
      }
      if (text[p] === "?" && text[p + 1] === ">") {
        return { status: "closed", end: p + 2 };
      }
      p += 1;
    }
    return canUnclose ? { status: "unclosed" } : { status: "invalid" };
  }

  if (next === "/") {
    let p = openAngle + 2;
    if (p >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    if (!isAsciiLetter(text.charCodeAt(p))) return { status: "invalid" };
    p += 1;
    while (p < len && isTagNameChar(text.charCodeAt(p))) p += 1;
    const ws = skipLinkWhitespace(text, p);
    if (ws.invalid) return { status: "invalid" };
    p = ws.next;
    if (p >= len) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
    if (text[p] === ">") return { status: "closed", end: p + 1 };
    return { status: "invalid" };
  }

  return scanOpenHtmlTag(text, openAngle, canUnclose);
}

function scanAngleInline(text, openAngle, canUnclose) {
  if (openAngle + 1 >= text.length) return canUnclose ? { status: "unclosed" } : { status: "invalid" };
  const uri = scanUriAutolink(text, openAngle, canUnclose);
  if (uri.status === "closed") return uri;
  const email = scanEmailAutolink(text, openAngle, canUnclose);
  if (email.status === "closed") return email;
  const html = scanHtmlInline(text, openAngle, canUnclose);
  if (html.status === "closed") return html;
  if (canUnclose && (uri.status === "unclosed" || email.status === "unclosed" || html.status === "unclosed")) {
    return { status: "unclosed" };
  }
  return { status: "invalid" };
}

function pushPlainWithBareUrls(pieces, text, start, end) {
  if (start >= end) return;
  const slice = text.slice(start, end);
  let cursor = 0;
  for (const match of slice.matchAll(BARE_URL_RE)) {
    if (match.index > cursor) {
      const pStart = start + cursor;
      const pEnd = start + match.index;
      pieces.push({ text: text.slice(pStart, pEnd), start: pStart, end: pEnd, skip: false });
    }
    const uStart = start + match.index;
    const uEnd = uStart + match[0].length;
    pieces.push({ text: match[0], start: uStart, end: uEnd, skip: true, url: true });
    cursor = match.index + match[0].length;
  }
  if (cursor < slice.length) {
    const pStart = start + cursor;
    pieces.push({ text: text.slice(pStart, end), start: pStart, end, skip: false });
  }
}

// 块级围栏优先，非围栏段内按 CommonMark 优先级联动扫描行内代码、行内 HTML/自动链接、行内链接/图片与裸 URL。
function analyzeMarkdownSegments(text) {
  const segments = splitFencedSegments(text);
  const pieces = [];
  let unclosedStart = null;
  const noteUnclosed = pos => {
    if (unclosedStart === null || pos < unclosedStart) unclosedStart = pos;
  };

  for (let segIdx = 0; segIdx < segments.length; segIdx += 1) {
    const seg = segments[segIdx];
    if (seg.fenced) {
      if (seg.unclosed) noteUnclosed(seg.start);
      pieces.push({
        text: text.slice(seg.start, seg.end),
        start: seg.start,
        end: seg.end,
        skip: true,
        fenced: true,
        unclosed: seg.unclosed,
      });
      continue;
    }

    const nextSeg = segments[segIdx + 1];
    const pendingBacktickFence = Boolean(
      nextSeg &&
      nextSeg.fenced &&
      nextSeg.unclosed &&
      !text.slice(nextSeg.start, nextSeg.end).includes("\n") &&
      text.slice(nextSeg.start, nextSeg.end).trimStart().startsWith("`")
    );
    const canUnclose = seg.end === text.length || pendingBacktickFence;

    const raw = text.slice(seg.start, seg.end);
    const len = raw.length;
    const lastCloseParen = raw.lastIndexOf(")");
    const lastCloseAngle = raw.lastIndexOf(">");
    const ranges = [];
    const stack = [];
    let i = 0;

    while (i < len) {
      const lb = lineBreakLength(raw, i);
      if (lb > 0) {
        if (isBlankLineAt(raw, i, lb)) stack.length = 0;
        i += lb;
        continue;
      }
      const ch = raw[i];
      if (ch === "\\") {
        if (i + 1 >= len) {
          if (canUnclose) noteUnclosed(seg.start + i);
          break;
        }
        if (raw[i + 1] === "\r" || raw[i + 1] === "\n") i += 1;
        else i += 2;
        continue;
      }
      if (ch === "`") {
        let q = i + 1;
        while (q < len && raw[q] === "`") q += 1;
        const tickLen = q - i;
        const closed = findClosingBackticks(raw, q, tickLen);
        if (closed.status === "closed") {
          ranges.push({ start: seg.start + i, end: seg.start + closed.end, inlineCode: true });
          i = closed.end;
          continue;
        }
        if (closed.status === "unclosed" && canUnclose) {
          const firstActive = stack.find(entry => entry.active);
          noteUnclosed(seg.start + (firstActive ? firstActive.start : i));
        }
        i = q;
        continue;
      }
      if (ch === "<") {
        const firstActive = canUnclose ? stack.find(entry => entry.active) : null;
        const candidateUnclosed = seg.start + (firstActive ? firstActive.start : i);
        const canClose = i < lastCloseAngle;
        const needsUnclosedCheck = canUnclose && (unclosedStart === null || candidateUnclosed < unclosedStart);
        if (canClose || needsUnclosedCheck) {
          const angle = scanAngleInline(raw, i, canUnclose);
          if (angle.status === "closed") {
            ranges.push({ start: seg.start + i, end: seg.start + angle.end, angleInline: true });
            i = angle.end;
            continue;
          }
          if (angle.status === "unclosed" && canUnclose) {
            noteUnclosed(candidateUnclosed);
          }
        }
        i += 1;
        continue;
      }
      if (ch === "[") {
        const isImage = i > 0 && raw[i - 1] === "!" && !isEscaped(raw, i - 1);
        stack.push({ start: isImage ? i - 1 : i, isImage, active: true });
        i += 1;
        continue;
      }
      if (ch === "]") {
        const opener = stack.pop();
        if (!opener || !opener.active) {
          i += 1;
          continue;
        }
        if (i + 1 >= len) {
          if (canUnclose) noteUnclosed(seg.start + opener.start);
          i += 1;
          continue;
        }
        if (raw[i + 1] === "(") {
          const canComplete = i + 1 < lastCloseParen;
          const needsUnclosedCheck = canUnclose && (unclosedStart === null || seg.start + opener.start < unclosedStart);
          if (canComplete || needsUnclosedCheck) {
            const parsed = parseLinkParen(raw, i + 1);
            if (parsed.status === "complete") {
              const absStart = seg.start + opener.start;
              const absEnd = seg.start + parsed.end;
              while (ranges.length && ranges[ranges.length - 1].start >= absStart) {
                ranges.pop();
              }
              ranges.push({ start: absStart, end: absEnd, link: true });
              if (!opener.isImage) {
                for (const entry of stack) {
                  if (!entry.isImage) entry.active = false;
                }
              }
              i = parsed.end;
              continue;
            }
            if (parsed.status === "unclosed" && canUnclose) {
              noteUnclosed(seg.start + opener.start);
            }
          }
        }
        i += 1;
        continue;
      }
      i += 1;
    }

    if (canUnclose) {
      for (const entry of stack) {
        if (entry.active) {
          noteUnclosed(seg.start + entry.start);
          break;
        }
      }
    }

    let cursor = seg.start;
    for (const range of ranges) {
      pushPlainWithBareUrls(pieces, text, cursor, range.start);
      pieces.push({
        text: text.slice(range.start, range.end),
        start: range.start,
        end: range.end,
        skip: true,
        inlineCode: !!range.inlineCode,
        link: !!range.link,
        angleInline: !!range.angleInline,
      });
      cursor = range.end;
    }
    pushPlainWithBareUrls(pieces, text, cursor, seg.end);
  }

  let limit = unclosedStart === null ? text.length : unclosedStart;
  if (limit > 0) {
    for (const piece of pieces) {
      if (piece.start >= limit) break;
      if (piece.end >= limit) {
        if (!piece.skip || piece.url) {
          const head = piece.text.slice(0, limit - piece.start);
          const unclosedUrl = UNCLOSED_URL_RE.exec(head);
          if (unclosedUrl) limit = Math.min(limit, piece.start + unclosedUrl.index);
        }
        break;
      }
    }
  }

  return { pieces, limit };
}

function createPieceSlicer(pieces) {
  let index = 0;
  let offset = 0;
  return (start, end) => {
    const result = [];
    while (index < pieces.length && offset + pieces[index].text.length <= start) {
      offset += pieces[index].text.length;
      index += 1;
    }
    let i = index;
    let pos = offset;
    while (i < pieces.length && pos < end) {
      const piece = pieces[i];
      const pieceEnd = pos + piece.text.length;
      const from = Math.max(start, pos) - pos;
      const to = Math.min(end, pieceEnd) - pos;
      if (to > from) result.push({ text: piece.text.slice(from, to), skip: piece.skip });
      if (pieceEnd <= end) {
        pos = pieceEnd;
        i += 1;
      } else {
        break;
      }
    }
    index = i;
    offset = pos;
    return result;
  };
}

function hasSafePrefixBoundary(baseSource, source) {
  const boundary = baseSource.length;
  if (!boundary || boundary >= source.length || !source.startsWith(baseSource)) return false;
  const { pieces } = analyzeMarkdownSegments(source);
  for (const piece of pieces) {
    if (piece.skip && piece.start < boundary && piece.end > boundary) return false;
  }
  const combinedFences = [...fencedTexts(baseSource), ...fencedTexts(source.slice(boundary))];
  const fullFences = pieces.filter(piece => piece.fenced).map(piece => piece.text);
  return combinedFences.length === fullFences.length && fullFences.every((text, i) => text === combinedFences[i]);
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
  const re = /(\r?\n[ \t]*\r?\n)/g;
  let last = 0;
  let match;
  while ((match = re.exec(text)) !== null) {
    parts.push({ body: text.slice(last, match.index), sep: match[1] });
    last = match.index + match[1].length;
  }
  parts.push({ body: text.slice(last), sep: "" });
  return parts;
}

function closedStreamPrefix(text, analyzed = analyzeMarkdownSegments(text)) {
  return analyzed.limit;
}

// 流式输出时只翻译已经稳定的段落，最后一句永远等它写完。
function stableStreamSource(text, waited = false) {
  const analyzed = analyzeMarkdownSegments(text);
  const limit = closedStreamPrefix(text, analyzed);
  if (!limit) return "";
  const safe = text.slice(0, limit);
  const pieces = limit === text.length ? analyzed.pieces : analyzeMarkdownSegments(safe).pieces;
  const masked = pieces.map(piece => (piece.skip ? " ".repeat(piece.text.length) : piece.text)).join("");
  const scan = limit < text.length ? `${masked}x` : masked;
  const paragraphs = splitParagraphs(scan);
  let end = paragraphs.slice(0, -1).reduce((length, part) => length + part.body.length + part.sep.length, 0);
  const tail = scan.slice(end);
  if (masked.length - end >= MAX_CHARS || waited) {
    end += sentenceUnits(tail).slice(0, -1).reduce((length, unit) => length + unit.length, 0);
  }
  return safe.slice(0, Math.min(end, limit));
}

function translationPieces(source, lang) {
  const { pieces: allPieces } = analyzeMarkdownSegments(source);
  const masked = allPieces.map(piece => (piece.skip ? " ".repeat(piece.text.length) : piece.text)).join("");
  const sliceRange = createPieceSlicer(allPieces);
  let offset = 0;
  return splitParagraphs(masked).flatMap(({ body, sep }) => {
    const groups = [];
    for (const unit of sentenceUnits(body)) {
      const skip = unit.trim() ? isTargetLanguage(unit, lang) : groups.at(-1)?.skip ?? true;
      for (const piece of sliceRange(offset, offset + unit.length)) {
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
  const prose = analyzeMarkdownSegments(text).pieces
    .filter(piece => !piece.skip)
    .map(piece => piece.text)
    .join("");
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

// 译文取决于哪个服务、哪种语言，以及自定义端点和模型；自动翻译与凭据开关
// 只影响是否/如何请求，不改变已显示译文的内容。
function translationSignature(settings) {
  const provider = settings.provider;
  const scope = provider === "custom"
    ? `${String(settings.customEndpoint || "").trim()}\u0000${String(settings.customModel || "").trim()}`
    : "";
  return `${provider}\u0000${resolveTarget(settings)}\u0000${scope}`;
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

const injected = new WeakMap();

function injectStyles(root) {
  let entry = injected.get(root);
  if (entry?.node.isConnected) {
    entry.count += 1;
    return entry.node;
  }
  const style = (root.ownerDocument ?? root).createElement("style");
  style.setAttribute("data-ekdp-owned", "1");
  style.count = 1;
  entry = { node: style, count: 1 };
  injected.set(root, entry);
  style.textContent = `
[data-ekdp-kct-view="translated"] [data-ekdp-kct-hidden]{display:none!important}
[data-ekdp-kct-view="original"] .ekdp-kct-translated{display:none!important}
.ekdp-kct-bar{display:flex;align-items:center;justify-content:flex-end;gap:4px;font:12px/20px var(--font-ui,sans-serif);color:var(--color-text-secondary,var(--color-text-muted,light-dark(#666,#aaa)))}
.ekdp-kct-bar[hidden]{display:none}
.ekdp-kct-btn{font:inherit;color:inherit;background:transparent;border:0;border-radius:4px;padding:0 5px;cursor:pointer;white-space:nowrap}
.ekdp-kct-btn:hover{color:var(--color-text,light-dark(#222,#eee));background:var(--color-hover,rgba(128,128,128,.1))}
.ekdp-kct-btn:focus-visible{outline:2px solid var(--blue,#4385ef);outline-offset:2px}
.ekdp-kct-btn:disabled{cursor:default}
.ekdp-kct-status{color:var(--color-text-secondary,var(--color-text-muted,light-dark(#666,#aaa)));font-size:11px}
.ekdp-kct-status:empty{display:none}
.ekdp-kct-status[data-state="error"]{color:var(--color-danger,var(--red,light-dark(#b42318,#f97066)))}
:where(.ekdp-kct-translated){font:var(--text-base,13px)/var(--leading-normal,1.5) var(--font-ui,sans-serif);font-weight:var(--weight-regular,400);color:var(--color-text-muted,var(--color-text-secondary,light-dark(#666,#aaa)));margin:0;padding:6px 0 0}
.ekdp-kct-translated{white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word}
.ekdp-kct-panel{position:fixed;z-index:2147483000;display:flex;flex-direction:column;gap:10px;box-sizing:border-box;padding:12px 14px;border:1px solid var(--color-line,var(--line,light-dark(rgba(0,0,0,.14),rgba(255,255,255,.16))));border-radius:10px;background:var(--color-surface,var(--surface-raised,light-dark(#fff,#1e1f22)));color:var(--color-text,light-dark(#222,#eee));box-shadow:0 12px 32px light-dark(rgba(15,23,42,.18),rgba(0,0,0,.5));font:12px/1.5 var(--font-ui,sans-serif);text-align:left}
.ekdp-kct-panel-head{display:flex;align-items:center;justify-content:space-between;gap:12px}
.ekdp-kct-panel-head strong{font-size:13px;font-weight:600}
.ekdp-kct-field{display:flex;flex-direction:column;gap:4px}
.ekdp-kct-field>span{color:var(--color-text-secondary,var(--ink-muted,light-dark(#666,#aaa)));font-size:11px}
.ekdp-kct-input{width:100%;box-sizing:border-box;font:inherit;color:inherit;background:var(--surface-sunken,light-dark(#f3f5f8,#26272b));border:1px solid var(--color-line,var(--line,light-dark(rgba(0,0,0,.14),rgba(255,255,255,.16))));border-radius:6px;padding:4px 6px}
.ekdp-kct-input:focus-visible{outline:2px solid var(--blue,#4385ef);outline-offset:1px}
.ekdp-kct-check{display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer}
.ekdp-kct-check input{margin:0}
.ekdp-kct-note{margin:0;color:var(--color-text-secondary,var(--ink-muted,light-dark(#666,#aaa)));font-size:11px;line-height:1.5;overflow-wrap:anywhere}
.ekdp-kct-note:empty{display:none}
.ekdp-kct-panel-actions{display:flex;align-items:center;gap:8px}
.ekdp-kct-custom{display:flex;flex-direction:column;gap:10px}
.ekdp-kct-stats{opacity:.85}
`;
  (root.head ?? root).appendChild(style);
  return style;
}

export function removeStyles(root) {
  const entry = injected.get(root);
  if (!entry) return;
  entry.count -= 1;
  if (entry.count === 0) {
    entry.node.remove();
    injected.delete(root);
  }
}

function messageOf(error) {
  const text = String(error?.message || error || "");
  return text.length > 200 ? text.slice(0, 200) + "…" : text;
}

// 状态药丸只放得下一句短话；完整原因在 title 里。分类覆盖用户最常遇到的失败：
// 限流、被拒绝、服务错误、DNS、代理/连接与超时。
function errorBrief(error) {
  const status = Number(error?.status) || 0;
  const text = String(error?.message || error || "").trim();
  if (status === 429) return "请求过多";
  if (status === 401 || status === 403) return "无权限或凭据被拒绝";
  if (status >= 500) return `服务返回 ${status}`;
  if (status >= 400) return `HTTP ${status}`;
  if (/无法解析|no such host|lookup|dns/i.test(text)) return "DNS 解析失败";
  if (/proxy|代理|tunnel|connect/i.test(text)) return "连接失败";
  if (/超时|timeout|deadline/i.test(text)) return "请求超时";
  if (!text) return "网络请求失败";
  return text.length > 24 ? `${text.slice(0, 24)}…` : text;
}
