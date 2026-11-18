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
const STYLE = `
.ekdp-mon-settings { display: flex; flex-direction: column; gap: 18px; padding: 4px 2px 18px; color: var(--color-text, #eee); font-size: 13px; }
.ekdp-mon-settings h3 { margin: 0; font-size: 14px; font-weight: 600; }
.ekdp-mon-settings p { margin: 0; color: var(--color-text-secondary, #999); line-height: 1.6; }
.ekdp-mon-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 12px; border: 1px solid var(--color-border, #333); border-radius: 8px; }
.ekdp-mon-row-text { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.ekdp-mon-row-text strong { font-weight: 500; }
.ekdp-mon-seg { display: inline-flex; gap: 2px; padding: 2px; border-radius: 7px; background: var(--color-fill, #1e1e1e); }
.ekdp-mon-seg button { padding: 4px 10px; border: 0; border-radius: 5px; background: transparent; color: inherit; font: inherit; cursor: pointer; }
.ekdp-mon-seg button[aria-pressed="true"] { background: var(--color-selected, #333); }
.ekdp-mon-switch { width: 38px; height: 22px; padding: 0; border: 0; border-radius: 11px; background: var(--color-fill, #333); cursor: pointer; position: relative; }
.ekdp-mon-switch::after { content: ""; position: absolute; top: 3px; left: 3px; width: 16px; height: 16px; border-radius: 50%; background: #fff; transition: transform .15s; }
.ekdp-mon-switch[aria-pressed="true"] { background: var(--color-accent, #1783ff); }
.ekdp-mon-switch[aria-pressed="true"]::after { transform: translateX(16px); }
.ekdp-mon-note { font-size: 12px; color: var(--color-text-secondary, #999); }
.ekdp-mon-error { font-size: 12px; color: var(--color-danger, #e5534b); }
`;

function node(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text != null) element.textContent = text;
  return element;
}

export function renderSettings(ctx, element) {
  const style = document.createElement("style");
  style.textContent = STYLE;
  element.append(style);

  const wrap = node("div", "ekdp-mon-settings");
  wrap.append(node("h3", "", "Monitor 侧栏面板"));
  wrap.append(node("p", "", "面板数据来自本机缓存指标与核心的余额读取，不读取会话正文，也不保存任何供应商密钥。"));

  const status = node("p", "ekdp-mon-note", "");
  let config = null;
  let miniEnabled = "0";
  let disposed = false;

  function report(text, isError) {
    status.className = isError ? "ekdp-mon-error" : "ekdp-mon-note";
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
      report(`保存失败，仍在使用上次保存的设置：${error?.message || error}`, true);
      return;
    }
    if (disposed) return;
    config = next;
    globalThis.dispatchEvent?.(new CustomEvent("ekdp-monitor-storage-changed", {
      detail: { key: CONFIG_KEY, value: next }
    }));
    render();
    report("已保存", false);
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
    // 面板把 Mini 模式存在同一个键上；设置页只认这份存储，不碰浏览器私有存储
    toggle.setAttribute("aria-pressed", String(miniEnabled === "1"));
    toggle.addEventListener("click", async () => {
      const next = miniEnabled === "1" ? "0" : "1";
      try {
        await ctx.storage.set(MINI_KEY, next);
      } catch (error) {
        report(`保存失败，仍在使用上次保存的设置：${error?.message || error}`, true);
        return;
      }
      if (disposed) return;
      miniEnabled = next;
      globalThis.dispatchEvent?.(new CustomEvent("ekdp-monitor-storage-changed", {
        detail: { key: MINI_KEY, value: next }
      }));
      toggle.setAttribute("aria-pressed", String(next === "1"));
      report("已保存", false);
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
    toggle.setAttribute("aria-pressed", String(config?.modules?.pet?.sidebarTidy === true));
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
    wrap.append(node("h3", "", "Monitor 侧栏面板"));
    wrap.append(node("p", "", "面板数据来自本机缓存指标与核心的余额读取，不读取会话正文，也不保存任何供应商密钥。"));
    if (!config) {
      wrap.append(node("p", "ekdp-mon-note", "正在读取已保存的设置…"));
      return;
    }
    wrap.append(rangeRow(), miniRow(), tidyRow(), status);
  }

  element.append(wrap);
  render();

  // 面板与设置页可能同时打开：读的是同一份存储，所以只加载一次，不轮询
  void (async () => {
    try {
      const stored = await ctx.storage.get(CONFIG_KEY);
      if (disposed) return;
      if (stored && typeof stored === "object") config = stored;
      else config = null;
      const mini = await ctx.storage.get(MINI_KEY);
      if (!disposed) miniEnabled = mini === "1" ? "1" : "0";
    } catch (error) {
      if (!disposed) report(`读取设置失败：${error?.message || error}`, true);
    }
    if (!disposed) {
      if (config) render();
      else report("面板还没有运行过，先在侧栏打开一次 Monitor 再回来设置。", true);
    }
  })();

  return () => {
    disposed = true;
    style.remove();
  };
}
