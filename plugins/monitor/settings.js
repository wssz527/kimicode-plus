// Monitor · 插件设置子页。
//
// 三个设置项都只影响插件自己：默认迷你、是否显示 Kimi 形象、默认统计窗口。
// 全部经 ctx.storage 持久化，写入成功后才更新界面并通知侧栏；写入失败时保留
// 上一次已保存的值并说明原因。设置模块与挂载模块是两个 blob 模块，因此共享
// 通道由 renderer.js 在 ctx 上建立的命名空间提供；若侧栏当前未运行（例如插件
// 刚启用而宿主还没挂载），这里只写存储，下次挂载时自然读到最新值。

const STORAGE_KEY = "settings";
const HUB_KEY = "ekdpMonitorSettingsHub";
const RANGES = ["24h", "7d", "30d"];
const RANGE_TEXT = { "24h": "24 小时", "7d": "7 天", "30d": "30 天" };
const DEFAULTS = { mini: false, pet: true, range: "24h" };

function normalize(raw) {
  const source = raw && typeof raw === "object" ? raw : {};
  return {
    mini: source.mini === true,
    pet: source.pet !== false,
    range: RANGES.includes(source.range) ? source.range : DEFAULTS.range,
  };
}

function publish(ctx, settings) {
  const hub = ctx[HUB_KEY];
  if (hub && typeof hub.publish === "function") hub.publish(settings);
}

// 设置页挂载在宿主自己的设置容器里，不走插件挂载的 shadow root，所以样式随
// 模块注入一份局部作用域的表。颜色仍取宿主 CSS 变量，不另建配色体系。
const STYLE = String.raw`
.ekdp-mon-settings {
  --mon-text: var(--color-text, currentColor);
  --mon-muted: var(--color-text-secondary, color-mix(in srgb, currentColor 72%, transparent));
  --mon-line: var(--color-line, color-mix(in srgb, currentColor 16%, transparent));
  --mon-fill: color-mix(in srgb, currentColor 6%, transparent);
  --mon-accent: var(--accent, var(--blue, #1783ff));
  --mon-ok: var(--success, #16a34a);
  --mon-danger: var(--danger, #dc2626);
  display: flex;
  flex-direction: column;
  gap: 2px;
  color: var(--mon-text);
  font: inherit;
  font-family: var(--font-ui, var(--font-sans, system-ui, sans-serif));
  font-size: 13px;
  line-height: 1.5;
}
.ekdp-mon-settings *,
.ekdp-mon-settings *::before,
.ekdp-mon-settings *::after { box-sizing: border-box; }
.ekdp-mon-settings p { margin: 0; }
.ekdp-mon-settings button { font: inherit; color: inherit; }

.ekdp-mon-intro {
  margin-bottom: 6px !important;
  color: var(--mon-muted);
  font-size: 12px;
}

.ekdp-mon-field {
  display: flex;
  align-items: flex-start;
  gap: 16px;
  padding: 11px 0;
  border-top: 1px solid var(--mon-line);
}
.ekdp-mon-field:first-of-type { border-top: 0; }
.ekdp-mon-field-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1 1 auto;
}
.ekdp-mon-field-copy strong {
  font-size: 13px;
  font-weight: 600;
}
.ekdp-mon-field-copy small {
  color: var(--mon-muted);
  font-size: 12px;
  line-height: 1.5;
}

.ekdp-mon-switch {
  position: relative;
  flex: none;
  display: inline-flex;
  align-items: center;
  width: 34px;
  height: 20px;
  margin-top: 1px;
  cursor: pointer;
}
.ekdp-mon-switch input {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  opacity: 0;
  cursor: pointer;
}
.ekdp-mon-track {
  position: relative;
  display: block;
  width: 34px;
  height: 20px;
  border-radius: 100px;
  background: var(--mon-fill);
  box-shadow: inset 0 0 0 1px var(--mon-line);
  transition: background-color 150ms ease, box-shadow 150ms ease;
}
.ekdp-mon-track::after {
  content: "";
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--color-bg, #fff);
  box-shadow: 0 1px 2px rgb(0 0 0 / 22%);
  transition: transform 150ms ease;
}
.ekdp-mon-switch input:checked + .ekdp-mon-track {
  background: var(--mon-accent);
  box-shadow: inset 0 0 0 1px transparent;
}
.ekdp-mon-switch input:checked + .ekdp-mon-track::after { transform: translateX(14px); }
.ekdp-mon-switch input:focus-visible + .ekdp-mon-track {
  outline: 2px solid var(--mon-accent);
  outline-offset: 2px;
}

.ekdp-mon-range {
  flex: none;
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  border-radius: 8px;
  background: var(--mon-fill);
  box-shadow: inset 0 0 0 1px var(--mon-line);
}
.ekdp-mon-range button {
  min-width: 40px;
  padding: 3px 9px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--mon-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  cursor: pointer;
  transition: background-color 150ms ease, color 150ms ease;
}
.ekdp-mon-range button:hover { color: var(--mon-text); }
.ekdp-mon-range button[aria-pressed="true"] {
  background: var(--color-bg, #fff);
  color: var(--mon-text);
  font-weight: 600;
  box-shadow: 0 1px 2px rgb(0 0 0 / 14%);
}
.ekdp-mon-range button:focus-visible {
  outline: 2px solid var(--mon-accent);
  outline-offset: 1px;
}

.ekdp-mon-status {
  min-height: 18px;
  font-size: 12px;
  line-height: 1.5;
}
.ekdp-mon-status:empty { display: none; }
.ekdp-mon-status[data-tone="ok"] { color: var(--mon-ok); }
.ekdp-mon-status[data-tone="error"] { color: var(--mon-danger); }

.ekdp-mon-foot {
  margin-top: 10px !important;
  color: var(--mon-muted);
  font-size: 11px;
  opacity: 0.8;
}

@media (prefers-reduced-motion: reduce) {
  .ekdp-mon-track,
  .ekdp-mon-track::after,
  .ekdp-mon-range button { transition: none; }
}
`;

function field(title, hint) {
  const wrapper = document.createElement("div");
  wrapper.className = "ekdp-mon-field";
  const text = document.createElement("div");
  text.className = "ekdp-mon-field-copy";
  const heading = document.createElement("strong");
  heading.textContent = title;
  text.append(heading);
  if (hint) {
    const small = document.createElement("small");
    small.textContent = hint;
    text.append(small);
  }
  wrapper.append(text);
  return wrapper;
}

/** A real checkbox with a drawn track: the input keeps native focus, keyboard and
 * screen-reader behaviour, and only its visual is replaced. */
function switchControl(label, checked, onChange) {
  const control = document.createElement("label");
  control.className = "ekdp-mon-switch";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.checked = checked;
  input.setAttribute("aria-label", label);
  const track = document.createElement("span");
  track.className = "ekdp-mon-track";
  track.setAttribute("aria-hidden", "true");
  input.addEventListener("change", () => onChange(input.checked));
  control.append(input, track);
  control.ref = input;
  return control;
}

export async function renderSettings(ctx, element) {
  let settings = DEFAULTS;
  try {
    settings = normalize(await ctx.storage.get(STORAGE_KEY));
  } catch {
    settings = normalize(null);
  }

  const style = document.createElement("style");
  style.textContent = STYLE;

  const card = document.createElement("section");
  card.className = "ekdp-mon-settings";
  const intro = document.createElement("p");
  intro.className = "ekdp-mon-intro";
  intro.textContent = "Monitor 的所有读数来自本机缓存指标，与“缓存与额度”页同源。插件停用后，核心不保留任何常驻缓存显示。";

  const note = document.createElement("p");
  note.className = "ekdp-mon-status";
  note.setAttribute("role", "status");
  note.setAttribute("aria-live", "polite");

  const miniSwitch = switchControl("侧栏默认迷你", settings.mini, value => {
    void save({ mini: value }, value ? "侧栏默认迷你已开启。" : "侧栏默认迷你已关闭。");
  });
  const petSwitch = switchControl("显示 Kimi 形象", settings.pet, value => {
    void save({ pet: value }, value ? "Kimi 形象已开启。" : "Kimi 形象已关闭。");
  });

  const rangeButtons = [];

  /**
   * Writes first, then commits the UI. A failed write restores the controls to the
   * value that is actually stored, so the page never claims a setting that will be
   * gone after a restart.
   */
  async function save(patch, message) {
    const candidate = normalize({ ...settings, ...patch });
    note.textContent = "正在保存…";
    note.dataset.tone = "pending";
    try {
      await ctx.storage.set(STORAGE_KEY, candidate);
    } catch (error) {
      note.textContent = `保存失败：${error?.message || error}。仍在使用上次保存的设置。`;
      note.dataset.tone = "error";
      syncControls();
      return;
    }
    settings = candidate;
    syncControls();
    note.textContent = message;
    note.dataset.tone = "ok";
    publish(ctx, settings);
  }

  function syncControls() {
    miniSwitch.ref.checked = settings.mini;
    petSwitch.ref.checked = settings.pet;
    for (const button of rangeButtons) {
      button.setAttribute("aria-pressed", String(settings.range === button.dataset.range));
    }
  }

  const miniRow = field("侧栏默认迷你", "侧栏挂载时先收起为一行；点标题行的折叠按钮随时展开。");
  miniRow.append(miniSwitch);

  const petRow = field("Kimi 形象", "显示插件自绘的几何形象，并跟随额度与样本状态改变表情。不使用官方商标素材，也不联网下载。");
  petRow.append(petSwitch);

  const rangeRow = field("默认统计窗口", "新挂载时按这个区间显示历史柱图。侧栏里的区间切换只影响当前查看，会回到这里配置的默认值。");
  const segmented = document.createElement("div");
  segmented.className = "ekdp-mon-range";
  segmented.setAttribute("role", "group");
  segmented.setAttribute("aria-label", "默认统计窗口");
  for (const range of RANGES) {
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.range = range;
    button.textContent = range;
    button.title = `默认按${RANGE_TEXT[range]}统计`;
    button.setAttribute("aria-pressed", String(settings.range === range));
    button.addEventListener("click", () => void save({ range }, `默认统计窗口已改为${RANGE_TEXT[range]}。`));
    rangeButtons.push(button);
    segmented.append(button);
  }
  rangeRow.append(segmented);

  const foot = document.createElement("p");
  foot.className = "ekdp-mon-foot";
  foot.textContent = `插件 ${ctx.plugin.id} · 版本 ${ctx.plugin.version} · 结构与样式参考 bfjnbvf/kimi-code-monitor（MIT）`;

  card.append(intro, miniRow, petRow, rangeRow, note, foot);
  element.append(style, card);

  return () => {
    card.remove();
    style.remove();
  };
}
