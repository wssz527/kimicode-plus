/**
 * Monitor 设置页。
 *
 * 与侧栏面板共用同一份存储：面板配置在 kimi-statusbar.config，迷你模式在
 * kimi-statusbar.mini。设置页只读写这两处，不复制一份自己的状态——否则面板
 * 里拖出来的布局会被设置页的旧副本覆盖。
 *
 * 写入顺序沿用面板：先 await 落盘，成功后才更新界面并提示；失败时保留上一次
 * 已保存的值并说明原因。
 */

const CONFIG_KEY = "kimi-statusbar.config";
const MINI_KEY = "kimi-statusbar.mini";

const RANGE_LABEL = { day: "24 小时", week: "7 天", month: "30 天" };

const DEFAULT_WIDGET_CONFIG = {
  version: 3,
  modules: {
    header: { show: "hidden", span: 2, stat: "balance", dataLink: "subscription" },
    input: { show: "full", span: 1 },
    cache: { show: "full", span: 1 },
    output: { show: "full", span: 1 },
    speed: { show: "full", span: 1 },
    duration: { show: "hidden", span: 2 },
    quota5h: { show: "mini", span: 1, pace: true, resetFormat: "countdown" },
    quotaWeek: { show: "mini", span: 1, pace: true, resetFormat: "countdown" },
    usageChart: { show: "hidden", span: 2, chartRange: "week" },
    pet: { show: "mini", span: 2, stat: "daily", sidebarTidy: false, ballLink: "subscription" },
    agents: { show: "hidden", span: 2, hiddenAgents: [] },
    external: { show: "full", span: 2, hiddenAccounts: [] }
  },
  orderFull: ["external", "output", "cache", "input", "speed"],
  orderMini: ["pet", "quota5h", "quotaWeek"],
  orderHidden: ["header", "duration", "agents", "usageChart"]
};

const STYLE = `
.ekdp-mon-settings {
  display: flex;
  flex-direction: column;
  gap: 12px;
  color: var(--ekds-text, var(--color-text, inherit));
  font: 13px/1.55 var(--font-ui, system-ui, sans-serif);
}
.ekdp-mon-intro {
  margin: 0;
  font-size: 12px;
  line-height: 1.55;
  color: var(--ekds-muted, var(--color-text-muted, var(--color-text-secondary, light-dark(#5c6370, #a3aab8))));
}
.ekdp-mon-group {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: var(--ekds-hairline, 1px solid var(--color-line, rgba(128, 128, 128, 0.22)));
  border-radius: var(--ekds-radius, 12px);
  background: var(--ekds-surface, var(--color-surface, light-dark(#f8f9fb, rgba(255, 255, 255, 0.04))));
}
.ekdp-mon-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 16px;
  border-top: var(--ekds-hairline, 1px solid var(--color-line, rgba(128, 128, 128, 0.2)));
}
.ekdp-mon-row:first-child { border-top: 0; }
.ekdp-mon-row-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ekdp-mon-row-text strong {
  font-size: 13px;
  font-weight: 500;
  color: var(--ekds-text, var(--color-text, inherit));
}
.ekdp-mon-seg {
  display: inline-flex;
  flex: none;
  gap: 2px;
  padding: 3px;
  border: var(--ekds-hairline, 1px solid var(--color-line, rgba(128, 128, 128, 0.22)));
  border-radius: 8px;
  background: var(--ekds-bg, var(--color-bg, light-dark(#eef0f4, rgba(0, 0, 0, 0.26))));
}
.ekdp-mon-seg button {
  height: 26px;
  padding: 0 10px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--ekds-muted, var(--color-text-muted, var(--color-text-secondary, light-dark(#5c6370, #a3aab8))));
  font: 500 12px/1 var(--font-ui, system-ui, sans-serif);
  cursor: pointer;
  white-space: nowrap;
  transition: background 120ms ease, color 120ms ease;
}
.ekdp-mon-seg button:hover {
  color: var(--ekds-text, var(--color-text, inherit));
}
.ekdp-mon-seg button[aria-pressed="true"] {
  background: var(--ekds-surface, var(--color-surface-raised, light-dark(#ffffff, rgba(255, 255, 255, 0.14))));
  color: var(--ekds-text, var(--color-text, inherit));
  box-shadow: 0 1px 2px light-dark(rgba(15, 23, 42, 0.08), rgba(0, 0, 0, 0.35));
}
.ekdp-mon-switch {
  flex: none;
  width: 36px;
  height: 20px;
  padding: 0;
  border: 1px solid color-mix(in srgb, var(--ekds-line, rgba(128, 128, 128, 0.3)) 85%, transparent);
  border-radius: 999px;
  background: color-mix(in srgb, var(--ekds-muted, #888) 26%, transparent);
  cursor: pointer;
  position: relative;
  transition: background 140ms ease, border-color 140ms ease;
}
.ekdp-mon-switch::after {
  content: "";
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
  transition: transform 140ms ease;
}
.ekdp-mon-switch[aria-pressed="true"] {
  border-color: transparent;
  background: var(--ekds-accent, var(--color-accent, #2563eb));
}
.ekdp-mon-switch[aria-pressed="true"]::after {
  transform: translateX(16px);
}
.ekdp-mon-switch:focus-visible,
.ekdp-mon-seg button:focus-visible {
  outline: none;
  box-shadow: var(--ekds-ring, 0 0 0 2px var(--color-accent, #2563eb));
}
.ekdp-mon-note {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--ekds-muted, var(--color-text-muted, var(--color-text-secondary, light-dark(#5c6370, #a3aab8))));
}
.ekdp-mon-ok {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--success, #27865a);
}
.ekdp-mon-error {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-danger, var(--danger, #c64b52));
}
`;

function node(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text != null) element.textContent = text;
  return element;
}

function hydrateConfig(raw) {
  const base = structuredClone(DEFAULT_WIDGET_CONFIG);
  if (!raw || typeof raw !== "object") return base;
  const next = structuredClone(raw);
  next.modules = next.modules && typeof next.modules === "object" ? next.modules : {};
  next.modules.usageChart = { ...base.modules.usageChart, ...(next.modules.usageChart || {}) };
  next.modules.pet = { ...base.modules.pet, ...(next.modules.pet || {}) };
  return next;
}

export function renderSettings(ctx, element) {
  const style = document.createElement("style");
  style.textContent = STYLE;
  element.append(style);

  const wrap = node("div", "ekdp-mon-settings");
  const status = node("p", "ekdp-mon-note", "");
  let config = null;
  let miniEnabled = "0";
  let loaded = false;
  let disposed = false;

  function report(text, tone) {
    status.className = tone === "error" ? "ekdp-mon-error" : tone === "ok" ? "ekdp-mon-ok" : "ekdp-mon-note";
    status.textContent = text;
  }

  /** 先落盘再提交：写失败时界面保持在已保存的值上。 */
  async function saveConfig(mutate) {
    if (!config) return;
    const next = structuredClone(config);
    mutate(next);
    try {
      await ctx.storage.set(CONFIG_KEY, next);
    } catch (error) {
      report(`保存失败，仍在使用上次保存的设置：${error?.message || error}`, "error");
      return;
    }
    if (disposed) return;
    config = next;
    globalThis.dispatchEvent?.(new CustomEvent("ekdp-monitor-storage-changed", {
      detail: { key: CONFIG_KEY, value: next }
    }));
    render();
    report("已保存", "ok");
  }

  function rangeRow() {
    const row = node("div", "ekdp-mon-row");
    const text = node("div", "ekdp-mon-row-text");
    text.append(node("strong", "", "消耗图默认窗口"));
    text.append(node("span", "ekdp-mon-note", "打开面板时图表显示的区间；面板内可临时切换。"));
    const seg = node("div", "ekdp-mon-seg");
    const current = config?.modules?.usageChart?.chartRange || "week";
    for (const [value, label] of Object.entries(RANGE_LABEL)) {
      const button = node("button", "", label);
      button.type = "button";
      button.setAttribute("aria-pressed", String(current === value));
      button.addEventListener("click", () => {
        void saveConfig((next) => {
          next.modules.usageChart.chartRange = value;
        });
      });
      seg.append(button);
    }
    row.append(text, seg);
    return row;
  }

  function miniRow() {
    const row = node("div", "ekdp-mon-row");
    const text = node("div", "ekdp-mon-row-text");
    text.append(node("strong", "", "迷你模式"));
    text.append(node("span", "ekdp-mon-note", "只保留宠物与额度条，点击面板展开完整区。"));
    const toggle = node("button", "ekdp-mon-switch");
    toggle.type = "button";
    toggle.setAttribute("role", "switch");
    toggle.setAttribute("aria-label", "迷你模式");
    // 面板把 Mini 模式存在同一个键上；设置页只认这份存储，不碰浏览器私有存储
    toggle.setAttribute("aria-pressed", String(miniEnabled === "1"));
    toggle.setAttribute("aria-checked", String(miniEnabled === "1"));
    toggle.addEventListener("click", async () => {
      const next = miniEnabled === "1" ? "0" : "1";
      try {
        await ctx.storage.set(MINI_KEY, next);
      } catch (error) {
        report(`保存失败，仍在使用上次保存的设置：${error?.message || error}`, "error");
        return;
      }
      if (disposed) return;
      miniEnabled = next;
      globalThis.dispatchEvent?.(new CustomEvent("ekdp-monitor-storage-changed", {
        detail: { key: MINI_KEY, value: next }
      }));
      toggle.setAttribute("aria-pressed", String(next === "1"));
      toggle.setAttribute("aria-checked", String(next === "1"));
      report("已保存", "ok");
    });
    row.append(text, toggle);
    return row;
  }

  function tidyRow() {
    const row = node("div", "ekdp-mon-row");
    const text = node("div", "ekdp-mon-row-text");
    text.append(node("strong", "", "侧栏整理"));
    text.append(node("span", "ekdp-mon-note", "隐藏官方侧栏顶部的品牌标志，把新建对话行与折叠按钮对齐。"));
    const toggle = node("button", "ekdp-mon-switch");
    toggle.type = "button";
    toggle.setAttribute("role", "switch");
    toggle.setAttribute("aria-label", "侧栏整理");
    const checked = config?.modules?.pet?.sidebarTidy === true;
    toggle.setAttribute("aria-pressed", String(checked));
    toggle.setAttribute("aria-checked", String(checked));
    toggle.addEventListener("click", () => {
      void saveConfig((next) => {
        next.modules.pet.sidebarTidy = next.modules.pet.sidebarTidy !== true;
      });
    });
    row.append(text, toggle);
    return row;
  }

  function render() {
    wrap.replaceChildren();
    wrap.append(node("p", "ekdp-mon-intro", "面板数据来自本机缓存指标与核心余额读取，不读取会话正文，也不保存任何供应商密钥。"));
    if (!loaded) {
      wrap.append(node("p", "ekdp-mon-note", "正在读取已保存的设置…"));
      return;
    }
    if (config) {
      const group = node("div", "ekdp-mon-group");
      group.append(rangeRow(), miniRow(), tidyRow());
      wrap.append(group);
    }
    wrap.append(status);
  }

  element.append(wrap);
  render();

  // 面板与设置页可能同时打开：读的是同一份存储，所以只加载一次，不轮询
  void (async () => {
    try {
      const stored = await ctx.storage.get(CONFIG_KEY);
      if (disposed) return;
      config = hydrateConfig(stored);
      const mini = await ctx.storage.get(MINI_KEY);
      if (!disposed) miniEnabled = mini === "1" ? "1" : "0";
      loaded = true;
      if (!disposed) render();
    } catch (error) {
      if (disposed) return;
      loaded = true;
      config = null;
      report(`读取设置失败：${error?.message || error}`, "error");
      render();
    }
  })();

  return () => {
    disposed = true;
    style.remove();
  };
}
