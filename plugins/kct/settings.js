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
  ["custom", "自定义服务", "OpenAI 兼容端点：远程 HTTPS 或本机 HTTP，密钥由核心托管注入"],
];

const LOCAL_ENDPOINT_HOSTS = new Set(["127.0.0.1", "[::1]", "localhost"]);

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

  const intro = document.createElement("p");
  intro.className = "ekdp-intro";
  intro.textContent = "展开思考块后按段落翻译，代码块与链接保持原样。会话文件与模型上下文始终保存原文。";

  const primaryGroup = document.createElement("div");
  primaryGroup.className = "ekdp-group";

  const languageSelect = document.createElement("select");
  languageSelect.className = "ekdp-select";
  languageSelect.setAttribute("aria-label", "目标语言");
  for (const [value, label] of LANGUAGES) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    languageSelect.append(option);
  }
  languageSelect.value = settings.targetLanguage;
  const languageRow = settingRow("目标语言", "跟随界面语言时读取宿主当前语言。", languageSelect);

  const serviceSelect = document.createElement("select");
  serviceSelect.className = "ekdp-select";
  serviceSelect.setAttribute("aria-label", "翻译服务");
  for (const [value, label] of SERVICES) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    serviceSelect.append(option);
  }
  serviceSelect.value = settings.provider;
  const serviceHint = document.createElement("small");
  serviceHint.className = "ekdp-hint";
  const serviceRow = settingRow("翻译服务", serviceHint, serviceSelect);

  let autoChecked = settings.autoTranslate !== false;
  const autoSwitch = createSwitch("展开后自动翻译", autoChecked, next => {
    autoChecked = next;
  });
  const autoRow = settingRow("展开后自动翻译", "关闭后只在点击思考块上的「翻译」时请求。", autoSwitch);

  primaryGroup.append(languageRow, serviceRow, autoRow);

  const customGroup = document.createElement("div");
  customGroup.className = "ekdp-group ekdp-subcard";

  const endpointInput = document.createElement("input");
  endpointInput.type = "url";
  endpointInput.className = "ekdp-input";
  endpointInput.placeholder = "https://…/v1/chat/completions";
  endpointInput.value = settings.customEndpoint || "";
  const endpointField = stackedField(
    "自定义端点",
    "OpenAI 兼容地址。远程端点必须使用 HTTPS；本机服务可用 http://127.0.0.1 或 http://localhost。",
    endpointInput,
  );

  const modelInput = document.createElement("input");
  modelInput.type = "text";
  modelInput.className = "ekdp-input";
  modelInput.placeholder = "例如 gpt-4o-mini / llama3.1（可留空）";
  modelInput.value = settings.customModel || "";
  const modelField = stackedField("模型名", "自定义端点需要的模型标识，不要求时可留空。", modelInput);

  const credentialState = document.createElement("small");
  credentialState.className = "ekdp-hint";
  const credentialInput = document.createElement("input");
  credentialInput.type = "password";
  credentialInput.className = "ekdp-input";
  credentialInput.autocomplete = "off";
  credentialInput.placeholder = "粘贴 API 密钥后保存";
  const credentialSave = document.createElement("button");
  credentialSave.type = "button";
  credentialSave.className = "ekdp-button";
  credentialSave.textContent = "保存密钥";
  const credentialClear = document.createElement("button");
  credentialClear.type = "button";
  credentialClear.className = "ekdp-button ekdp-button-quiet";
  credentialClear.textContent = "清除";
  const credentialBar = document.createElement("div");
  credentialBar.className = "ekdp-input-row";
  credentialBar.append(credentialInput, credentialSave, credentialClear);
  const credentialField = stackedField("API 密钥（translator）", credentialState, credentialBar);

  let useCredentialChecked = settings.useCredential !== false;
  const credentialUseSwitch = createSwitch("请求时由核心注入凭据", useCredentialChecked, next => {
    useCredentialChecked = next;
  });
  const credentialUseRow = settingRow(
    "请求时注入凭据",
    "明文保存在系统加密凭据库中，插件只引用名称 translator。",
    credentialUseSwitch,
  );

  customGroup.append(endpointField, modelField, credentialField, credentialUseRow);

  const cacheGroup = document.createElement("div");
  cacheGroup.className = "ekdp-group";
  const cacheState = document.createElement("small");
  cacheState.className = "ekdp-hint";
  cacheState.textContent = "正在检查缓存…";
  const cacheClear = document.createElement("button");
  cacheClear.type = "button";
  cacheClear.className = "ekdp-button ekdp-button-quiet";
  cacheClear.textContent = "清除翻译缓存";
  const cacheRow = settingRow("翻译缓存", cacheState, cacheClear);
  cacheGroup.append(cacheRow);

  const footer = document.createElement("div");
  footer.className = "ekdp-footer";
  const status = document.createElement("p");
  status.className = "ekdp-status";
  status.setAttribute("aria-live", "polite");
  const save = document.createElement("button");
  save.type = "button";
  save.className = "ekdp-button ekdp-button-primary";
  save.textContent = "保存设置";
  footer.append(status, save);

  const diagnostics = document.createElement("p");
  diagnostics.className = "ekdp-diag";
  diagnostics.setAttribute("aria-live", "polite");
  let credentialSummary = "凭据未检查";
  let cacheSummary = "缓存未检查";

  card.append(intro, primaryGroup, customGroup, cacheGroup, footer, diagnostics);
  element.append(card);

  function setStatus(text, tone = "neutral") {
    status.textContent = text;
    status.dataset.tone = tone;
  }

  function providerLabel() {
    return SERVICES.find(([value]) => value === serviceSelect.value)?.[1] ?? serviceSelect.value;
  }

  function updateDiagnostics() {
    const languageLabel = languageSelect.options[languageSelect.selectedIndex]?.textContent ?? languageSelect.value;
    diagnostics.textContent = `${providerLabel()} · ${languageLabel} · ${cacheSummary} · ${credentialSummary} · v${ctx.plugin.version}`;
  }

  function syncVisibility() {
    const customOn = serviceSelect.value === "custom";
    customGroup.hidden = !customOn;
    serviceHint.textContent = SERVICES.find(([value]) => value === serviceSelect.value)?.[2] ?? "";
    if (!customOn) {
      useCredentialChecked = false;
      setSwitchState(credentialUseSwitch, false);
    } else {
      useCredentialChecked = settings.useCredential !== false;
      setSwitchState(credentialUseSwitch, useCredentialChecked);
    }
    updateDiagnostics();
  }
  serviceSelect.addEventListener("change", syncVisibility);
  languageSelect.addEventListener("change", updateDiagnostics);
  syncVisibility();

  function syncControls(next) {
    Object.assign(settings, next);
    if (next.targetLanguage !== undefined) languageSelect.value = next.targetLanguage;
    if (next.provider !== undefined) serviceSelect.value = next.provider;
    if (next.autoTranslate !== undefined) {
      autoChecked = next.autoTranslate !== false;
      setSwitchState(autoSwitch, autoChecked);
    }
    if (next.customEndpoint !== undefined) endpointInput.value = next.customEndpoint;
    if (next.customModel !== undefined) modelInput.value = next.customModel;
    if (next.useCredential !== undefined) {
      useCredentialChecked = next.useCredential !== false;
      setSwitchState(credentialUseSwitch, useCredentialChecked);
    }
    syncVisibility();
  }

  async function onExternalChange() {
    const next = await ctx.storage.get(SETTINGS_KEY).catch(() => undefined);
    if (!next || typeof next !== "object") return;
    syncControls({ ...settings, ...next });
  }
  window.addEventListener("ekdp-kct:settings", onExternalChange);

  async function refreshCredential() {
    try {
      const result = await ctx.core.request("credentials.list");
      const entry = Array.isArray(result?.items)
        ? result.items.find(item => item?.name === CREDENTIAL)
        : null;
      credentialState.textContent = entry?.stored
        ? entry.unlocked
          ? "已保存密钥，请求时可自动注入。"
          : "已保存密钥，但凭据库处于锁定状态；解锁后才能带上它。"
        : "尚未保存密钥。";
      credentialSummary = entry?.stored ? (entry.unlocked ? "凭据已保存" : "凭据已锁定") : "未保存凭据";
      credentialClear.disabled = !entry?.stored;
      credentialUseSwitch.disabled = !entry?.stored;
    } catch (error) {
      credentialState.textContent = `无法读取凭据状态：${messageOf(error)}`;
      credentialSummary = "凭据状态不可读";
      credentialClear.disabled = true;
    }
    updateDiagnostics();
  }

  async function saveSettings() {
    const next = {
      targetLanguage: languageSelect.value,
      provider: serviceSelect.value,
      autoTranslate: autoChecked,
      customEndpoint: endpointInput.value.trim(),
      customModel: modelInput.value.trim(),
      useCredential: useCredentialChecked,
    };
    if (next.provider === "custom" && !validEndpoint(next.customEndpoint)) {
      setStatus(
        next.customEndpoint
          ? "自定义端点无效：远程端点必须使用 HTTPS；本机地址可用 http://127.0.0.1、http://[::1] 或 http://localhost。"
          : "请填写自定义端点地址，例如 https://…/v1/chat/completions。",
        "error",
      );
      return;
    }
    save.disabled = true;
    try {
      if (validEndpoint(next.customEndpoint)) next.customEndpoint = new URL(next.customEndpoint).href;
      await ctx.storage.set(SETTINGS_KEY, next);
      Object.assign(settings, next);
      setStatus("已保存。已展开的思考块会按新设置重新翻译。", "ok");
      window.dispatchEvent(new Event("ekdp-kct:settings"));
    } catch (error) {
      setStatus(`保存失败：${messageOf(error)}`, "error");
    } finally {
      save.disabled = false;
    }
  }

  save.addEventListener("click", () => void saveSettings());

  credentialSave.addEventListener("click", async () => {
    const value = credentialInput.value.trim();
    if (!value) {
      setStatus("请先粘贴密钥。", "error");
      return;
    }
    credentialSave.disabled = true;
    try {
      await ctx.core.request("credentials.save", { name: CREDENTIAL, value });
      credentialInput.value = "";
      setStatus("密钥已写入凭据库，插件不再接触明文。", "ok");
      await refreshCredential();
    } catch (error) {
      setStatus(`保存密钥失败：${messageOf(error)}`, "error");
    } finally {
      credentialSave.disabled = false;
    }
  });

  credentialClear.addEventListener("click", async () => {
    credentialClear.disabled = true;
    try {
      await ctx.core.request("credentials.delete", { name: CREDENTIAL });
      setStatus("已清除密钥引用。", "neutral");
      await refreshCredential();
    } catch (error) {
      setStatus(`清除失败：${messageOf(error)}`, "error");
    } finally {
      credentialClear.disabled = false;
    }
  });

  async function refreshCache() {
    try {
      const keys = await ctx.storage.list(CACHE_ENTRY_PREFIX);
      cacheState.textContent = keys.length
        ? `已缓存 ${keys.length} 个段落，命中时直接复用译文。`
        : "暂无缓存段落。";
      cacheSummary = keys.length ? `缓存 ${keys.length} 段` : "无缓存";
      cacheClear.disabled = keys.length === 0;
    } catch {
      cacheState.textContent = "无法读取缓存状态。";
      cacheSummary = "缓存状态不可读";
    }
    updateDiagnostics();
  }

  cacheClear.addEventListener("click", async () => {
    cacheClear.disabled = true;
    try {
      const keys = await ctx.storage.list(CACHE_ENTRY_PREFIX);
      for (const key of keys) await ctx.storage.delete(key);
      await ctx.storage.delete(CACHE_INDEX_KEY);
      await ctx.storage.delete("cache.v1");
      setStatus("翻译缓存已清除。", "ok");
      window.dispatchEvent(new Event("ekdp-kct:cache"));
      await refreshCache();
    } catch (error) {
      setStatus(`清除缓存失败：${messageOf(error)}`, "error");
    } finally {
      cacheClear.disabled = false;
    }
  });

  await refreshCredential().catch(() => {});
  await refreshCache().catch(() => {});

  function onCacheChanged() {
    void refreshCache();
  }
  window.addEventListener("ekdp-kct:cache", onCacheChanged);

  return () => {
    window.removeEventListener("ekdp-kct:settings", onExternalChange);
    window.removeEventListener("ekdp-kct:cache", onCacheChanged);
    card.remove();
  };
}

function settingRow(label, hint, control) {
  const row = document.createElement("div");
  row.className = "ekdp-setting-row";
  const copy = document.createElement("div");
  copy.className = "ekdp-setting-copy";
  const heading = document.createElement("strong");
  heading.className = "ekdp-label";
  heading.textContent = label;
  copy.append(heading);
  if (typeof hint === "string") {
    const note = document.createElement("small");
    note.className = "ekdp-hint";
    note.textContent = hint;
    copy.append(note);
  } else if (hint) {
    copy.append(hint);
  }
  const slot = document.createElement("div");
  slot.className = "ekdp-setting-control";
  slot.append(control);
  row.append(copy, slot);
  return row;
}

function stackedField(label, hint, control) {
  const wrapper = document.createElement("div");
  wrapper.className = "ekdp-field";
  const heading = document.createElement("strong");
  heading.className = "ekdp-label";
  heading.textContent = label;
  wrapper.append(heading);
  if (typeof hint === "string") {
    const note = document.createElement("small");
    note.className = "ekdp-hint";
    note.textContent = hint;
    wrapper.append(note);
  } else if (hint) {
    wrapper.append(hint);
  }
  wrapper.append(control);
  return wrapper;
}

function setSwitchState(button, checked) {
  button.setAttribute("aria-checked", String(Boolean(checked)));
  button.dataset.checked = checked ? "true" : "false";
}

function createSwitch(label, initial, onChange) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "ekdp-switch";
  button.setAttribute("role", "switch");
  button.setAttribute("aria-label", label);
  setSwitchState(button, initial);
  button.addEventListener("click", () => {
    if (button.disabled) return;
    const next = button.getAttribute("aria-checked") !== "true";
    setSwitchState(button, next);
    onChange(next);
  });
  return button;
}

function validEndpoint(raw) {
  if (!raw) return false;
  let url;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  if (url.username || url.password || url.hash) return false;
  if (url.protocol === "https:") return true;
  return url.protocol === "http:" && LOCAL_ENDPOINT_HOSTS.has(url.hostname);
}

function messageOf(error) {
  const text = String(error?.message || error || "");
  return text.length > 200 ? text.slice(0, 200) + "…" : text;
}
