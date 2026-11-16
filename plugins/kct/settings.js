// 思考内容翻译的设置子页。
//
// 设置写在插件私有存储里（与迁移服务写入的是同一个键），密钥不进设置：
// 只用凭据名 translator，明文由核心在出站请求时注入。

const SETTINGS_KEY = "settings.v1";
const CACHE_INDEX_KEY = "cache.index.v1";
const CACHE_ENTRY_PREFIX = "cache.e.v1:";
const CREDENTIAL = "translator";

const LANGUAGES = [
  ["__auto__", "跟随界面语言"],
  ["zh", "简体中文"],
  ["en", "English"],
  ["ja", "日本語"],
  ["ko", "한국어"],
  ["es", "Español"],
  ["fr", "Français"],
  ["de", "Deutsch"],
  ["ru", "Русский"],
];

const SERVICES = [
  ["bing", "Bing 翻译", "公共端点，无需密钥"],
  ["google", "Google 翻译", "公共端点，无需密钥"],
  ["custom", "自定义服务", "OpenAI 兼容端点，需要密钥"],
];

export async function renderSettings(ctx, element) {
  const stored = await ctx.storage.get(SETTINGS_KEY).catch(() => undefined);
  const settings = {
    targetLanguage: "__auto__",
    provider: "bing",
    autoTranslate: true,
    customEndpoint: "",
    customModel: "",
    useCredential: true,
    ...(stored && typeof stored === "object" ? stored : {}),
  };

  const card = document.createElement("section");
  card.className = "ekdp-settings";

  const title = document.createElement("h3");
  title.textContent = "思考内容翻译";

  const intro = document.createElement("p");
  intro.textContent = "展开思考块后按段落翻译，代码块与链接保持原样。会话文件与模型上下文保存的始终是原文。";

  const language = field("目标语言", "译文使用哪种语言；跟随界面语言时读宿主当前语言。");
  const languageSelect = document.createElement("select");
  languageSelect.className = "ekdp-input";
  for (const [value, label] of LANGUAGES) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    languageSelect.append(option);
  }
  languageSelect.value = settings.targetLanguage;
  language.append(languageSelect);

  const service = field("翻译服务", "公共端点无需密钥；自定义服务只接受本机地址，密钥存入凭据库。");
  const serviceSelect = document.createElement("select");
  serviceSelect.className = "ekdp-input";
  for (const [value, label] of SERVICES) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    serviceSelect.append(option);
  }
  serviceSelect.value = settings.provider;
  const serviceNote = document.createElement("p");
  serviceNote.className = "ekdp-muted";
  service.append(serviceSelect, serviceNote);

  const auto = document.createElement("label");
  auto.className = "ekdp-check";
  const autoInput = document.createElement("input");
  autoInput.type = "checkbox";
  autoInput.checked = settings.autoTranslate !== false;
  auto.append(autoInput, document.createTextNode(" 展开后自动翻译（关闭时只在你点“翻译”时请求）"));

  const custom = document.createElement("div");
  custom.className = "ekdp-stack";
  const endpoint = field("自定义端点", "例如 http://localhost:11434/v1/chat/completions。只允许本机地址。");
  const endpointInput = document.createElement("input");
  endpointInput.type = "url";
  endpointInput.className = "ekdp-input";
  endpointInput.placeholder = "http://localhost:11434/v1/chat/completions";
  endpointInput.value = settings.customEndpoint || "";
  endpoint.append(endpointInput);

  const model = field("模型名", "自定义服务需要的模型标识，可留空。");
  const modelInput = document.createElement("input");
  modelInput.type = "text";
  modelInput.className = "ekdp-input";
  modelInput.placeholder = "llama3.1";
  modelInput.value = settings.customModel || "";
  model.append(modelInput);

  const credential = field("凭据", "密钥保存在核心的加密凭据库，插件只引用名字 translator，拿不到明文。");
  const credentialState = document.createElement("p");
  credentialState.className = "ekdp-muted";
  const credentialInput = document.createElement("input");
  credentialInput.type = "password";
  credentialInput.className = "ekdp-input";
  credentialInput.autocomplete = "off";
  credentialInput.placeholder = "粘贴密钥后保存";
  const credentialUse = document.createElement("label");
  credentialUse.className = "ekdp-check";
  const credentialUseInput = document.createElement("input");
  credentialUseInput.type = "checkbox";
  credentialUseInput.checked = settings.useCredential !== false;
  credentialUse.append(credentialUseInput, document.createTextNode(" 请求时由核心注入该凭据"));
  const credentialSave = document.createElement("button");
  credentialSave.type = "button";
  credentialSave.className = "ekdp-button";
  credentialSave.textContent = "保存密钥";
  const credentialClear = document.createElement("button");
  credentialClear.type = "button";
  credentialClear.className = "ekdp-button ekdp-button-quiet";
  credentialClear.textContent = "清除引用";
  credential.append(credentialState, credentialInput, credentialSave, credentialUse, credentialClear);
  custom.append(endpoint, model, credential);

  const cache = field("翻译缓存", "缓存按段落保存，命中时不再请求服务。清除后已显示的译文保留到下次重新翻译。");
  const cacheState = document.createElement("p");
  cacheState.className = "ekdp-muted";
  const cacheClear = document.createElement("button");
  cacheClear.type = "button";
  cacheClear.className = "ekdp-button";
  cacheClear.textContent = "清除翻译缓存";
  cache.append(cacheState, cacheClear);

  const status = document.createElement("p");
  status.className = "ekdp-muted";
  status.setAttribute("aria-live", "polite");

  const save = document.createElement("button");
  save.type = "button";
  save.className = "ekdp-button ekdp-button-primary";
  save.textContent = "保存设置";
  const actions = document.createElement("div");
  actions.className = "ekdp-actions";
  actions.append(save);

  const identity = document.createElement("p");
  identity.className = "ekdp-muted";
  identity.textContent = `插件 ${ctx.plugin.id} · 版本 ${ctx.plugin.version}`;

  element.append(card);
  card.append(title, intro, language, service, auto, custom, cache, actions, status, identity);

  function syncVisibility() {
    const customOn = serviceSelect.value === "custom";
    custom.hidden = !customOn;
    credential.hidden = !customOn;
    credentialUse.parentElement.hidden = !customOn;
    serviceNote.textContent = SERVICES.find(([value]) => value === serviceSelect.value)?.[2] ?? "";
    // 公共端点没有密钥可注入，关掉时也避免留下一个看似生效的勾选。
    if (!customOn) credentialUseInput.checked = false;
    else credentialUseInput.checked = settings.useCredential !== false;
  }
  serviceSelect.addEventListener("change", syncVisibility);
  syncVisibility();

  async function refreshCredential() {
    try {
      const result = await ctx.core.request("credentials.list");
      const entry = Array.isArray(result?.items)
        ? result.items.find(item => item?.name === CREDENTIAL)
        : null;
      credentialState.textContent = entry?.stored
        ? entry.unlocked
          ? "已保存密钥，可用于请求。"
          : "已保存密钥，但凭据库处于锁定状态；解锁后请求才能带上它。"
        : "尚未保存密钥。";
      credentialClear.disabled = !entry?.stored;
      credentialUseInput.disabled = !entry?.stored;
    } catch (error) {
      credentialState.textContent = `无法读取凭据状态：${messageOf(error)}`;
      credentialClear.disabled = true;
    }
  }

  async function saveSettings() {
    const next = {
      targetLanguage: languageSelect.value,
      provider: serviceSelect.value,
      autoTranslate: autoInput.checked,
      customEndpoint: endpointInput.value.trim(),
      customModel: modelInput.value.trim(),
      useCredential: credentialUseInput.checked,
    };
    if (next.provider === "custom" && !localEndpoint(next.customEndpoint)) {
      status.textContent = "自定义端点必须是本机地址（localhost / 127.0.0.1）且以 http 或 https 开头。";
      return;
    }
    save.disabled = true;
    try {
      await ctx.storage.set(SETTINGS_KEY, next);
      Object.assign(settings, next);
      status.textContent = "已保存。已展开的思考块会按新设置重新翻译。";
      window.dispatchEvent(new Event("ekdp-kct:settings"));
    } catch (error) {
      status.textContent = `保存失败：${messageOf(error)}`;
    } finally {
      save.disabled = false;
    }
  }

  save.addEventListener("click", () => void saveSettings());

  credentialSave.addEventListener("click", async () => {
    const value = credentialInput.value.trim();
    if (!value) {
      status.textContent = "请先粘贴密钥。";
      return;
    }
    credentialSave.disabled = true;
    try {
      await ctx.core.request("credentials.save", { name: CREDENTIAL, value });
      credentialInput.value = "";
      status.textContent = "密钥已写入凭据库，插件不再接触明文。";
      await refreshCredential();
    } catch (error) {
      status.textContent = `保存密钥失败：${messageOf(error)}`;
    } finally {
      credentialSave.disabled = false;
    }
  });

  credentialClear.addEventListener("click", async () => {
    credentialClear.disabled = true;
    try {
      await ctx.core.request("credentials.delete", { name: CREDENTIAL });
      status.textContent = "已清除凭据引用。自定义服务下次请求会失败，直到重新保存密钥。";
      await refreshCredential();
    } catch (error) {
      status.textContent = `清除失败：${messageOf(error)}`;
    } finally {
      credentialClear.disabled = false;
    }
  });

  async function refreshCache() {
    try {
      const index = await ctx.storage.get(CACHE_INDEX_KEY);
      const count = Array.isArray(index) ? index.length : 0;
      cacheState.textContent = count ? `已缓存 ${count} 个段落。` : "暂无缓存。";
    } catch {
      cacheState.textContent = "无法读取缓存状态。";
    }
  }

  cacheClear.addEventListener("click", async () => {
    cacheClear.disabled = true;
    try {
      const index = await ctx.storage.get(CACHE_INDEX_KEY).catch(() => undefined);
      const keys = Array.isArray(index) ? index.map(entry => entry?.[0]).filter(Boolean) : [];
      for (const key of keys) await ctx.storage.delete(CACHE_ENTRY_PREFIX + key).catch(() => {});
      await ctx.storage.delete(CACHE_INDEX_KEY).catch(() => {});
      await ctx.storage.delete("cache.v1").catch(() => {});
      status.textContent = "翻译缓存已清除。";
      window.dispatchEvent(new Event("ekdp-kct:cache"));
      await refreshCache();
    } catch (error) {
      status.textContent = `清除缓存失败：${messageOf(error)}`;
    } finally {
      cacheClear.disabled = false;
    }
  });

  await refreshCredential().catch(() => {});
  await refreshCache().catch(() => {});

  return () => card.remove();
}

function field(label, hint) {
  const wrapper = document.createElement("div");
  wrapper.className = "ekdp-field";
  const heading = document.createElement("span");
  heading.className = "ekdp-label";
  heading.textContent = label;
  const note = document.createElement("p");
  note.className = "ekdp-muted";
  note.textContent = hint;
  wrapper.append(heading, note);
  return wrapper;
}

function localEndpoint(raw) {
  if (!raw) return false;
  let url;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return false;
  return url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "::1" || url.hostname === "[::1]";
}

function messageOf(error) {
  const text = String(error?.message || error || "");
  return text.length > 200 ? text.slice(0, 200) + "…" : text;
}
