// Monitor · 插件设置子页。
//
// 三个设置项都只影响插件自己：默认迷你、是否显示 Kimi 形象、默认统计窗口。
// 全部经 ctx.storage 持久化，写入后通过同一个 ctx 上的设置通道即时通知侧栏，
// 不需要重新启用插件。设置模块与挂载模块是两个 blob 模块，因此共享通道由
// renderer.js 在 ctx 上建立的命名空间提供；若侧栏当前未运行（例如插件刚启用
// 而宿主还没挂载），这里只写存储，下次挂载时自然读到最新值。

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
  return control;
}

export async function renderSettings(ctx, element) {
  let settings = DEFAULTS;
  try {
    settings = normalize(await ctx.storage.get(STORAGE_KEY));
  } catch {
    settings = normalize(null);
  }

  const card = document.createElement("section");
  card.className = "ekdp-mon-settings";
  const intro = document.createElement("p");
  intro.className = "ekdp-mon-intro";
  intro.textContent = "Monitor 的所有读数来自本机缓存指标，与“缓存与额度”页同源。插件停用后，核心不保留任何常驻缓存显示。";

  const note = document.createElement("p");
  note.className = "ekdp-mon-status";
  note.setAttribute("role", "status");

  async function save(patch, message) {
    settings = normalize({ ...settings, ...patch });
    try {
      await ctx.storage.set(STORAGE_KEY, settings);
      note.textContent = message;
      note.dataset.tone = "ok";
    } catch (error) {
      note.textContent = `保存失败：${error?.message || error}`;
      note.dataset.tone = "error";
      return;
    }
    publish(ctx, settings);
  }

  const miniRow = field("侧栏默认迷你", "侧栏挂载时先收起为一行；点标题行的折叠按钮随时展开。");
  miniRow.append(switchControl("侧栏默认迷你", settings.mini, value => {
    void save({ mini: value }, value ? "下次挂载将默认迷你。" : "下次挂载将默认展开。");
  }));

  const petRow = field("Kimi 形象", "显示插件自绘的几何形象，并跟随额度与样本状态改变表情。不使用官方商标素材，也不联网下载。");
  petRow.append(switchControl("显示 Kimi 形象", settings.pet, value => {
    void save({ pet: value }, value ? "Kimi 形象已开启。" : "Kimi 形象已关闭。");
  }));

  const rangeRow = field("默认统计窗口", "侧栏的历史柱图按这个区间统计；随时可在侧栏里临时切换。");
  const segmented = document.createElement("div");
  segmented.className = "ekdp-mon-range";
  segmented.setAttribute("role", "group");
  segmented.setAttribute("aria-label", "默认统计窗口");
  const buttons = [];
  for (const range of RANGES) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = range;
    button.title = `默认按${RANGE_TEXT[range]}统计`;
    button.setAttribute("aria-pressed", String(settings.range === range));
    button.addEventListener("click", () => {
      for (const item of buttons) item.setAttribute("aria-pressed", String(item === button));
      void save({ range }, `默认统计窗口已改为${RANGE_TEXT[range]}。`);
    });
    buttons.push(button);
    segmented.append(button);
  }
  rangeRow.append(segmented);

  const foot = document.createElement("p");
  foot.className = "ekdp-mon-foot";
  foot.textContent = `插件 ${ctx.plugin.id} · 版本 ${ctx.plugin.version} · 结构与样式参考 bfjnbvf/kimi-code-monitor（MIT）`;

  card.append(intro, miniRow, petRow, rangeRow, note, foot);
  element.append(card);

  return () => card.remove();
}
