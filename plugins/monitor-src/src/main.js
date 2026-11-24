/**
 * Monitor 侧栏面板的装配入口。
 *
 * 面板本体是上游 bfjnbvf/kimi-code-monitor 的 panel-app 移植（vendor/ 原样
 * 保留）：DOM 结构、content.css、交互与中文文案都不改，只替换数据源。
 * 装配顺序照 upstream/src/panel-app.js，差别在：
 * - 数据来自插件接口而不是直连 kap-server（见 datasource.js）；
 * - 宿主是 ctx.ui.mount('sidebar') 给的容器，不是页面级浮动面板；
 * - 不复刻浏览器独有的桌宠、回复书签、分享卡片、自动归档与 popup。
 *
 * 第 9 项「侧栏整理」在桌面上单独成立。上游把它绑在 pet 的 ≡ 菜单上，但
 * applySidebarTidy 在 standaloneMode 下恒为 false，菜单项也随之隐藏。这里
 * 保留同一个配置键（modules.pet.sidebarTidy，默认关闭），由本文件在每次配置
 * 生效后自己切换 html 上的类；CSS 仍用 content.css 里那组选择器。
 */

// host.js 必须第一个求值：上游 pet-panel.js 在模块顶层调用
// chrome.runtime.getURL，那一刻替身与资产表都要在位。
import { bindStorage, disposeHost, syncStorageKey } from "./host.js";
import { panel, resetMetrics } from "../vendor/content/panel-state.js";
import {
  applyModeClasses,
  applyWidgetConfig,
  disposeWidgetStructure,
  initWidgetStructure,
  loadWidgetConfig,
  mountWidget,
  CONFIG_STORAGE_KEY,
  MINI_STORAGE_KEY
} from "../vendor/content/widget-structure.js";
import { cancelRenderAll, initRender, renderAll, disposeRender } from "../vendor/content/render.js";
import { initPet, petUpdateStatus, disposePet } from "../vendor/content/pet-panel.js";
import { disposeBridge, getSessionId, installBridge, installStatusTicker, markBridgeReady } from "../vendor/panel-app/bridge.js";
import { syncLocaleFromPage } from "../vendor/i18n.js";
import { startDatasource } from "./datasource.js";
import { ASSETS } from "./shim-assets.js";

/** 面板默认布局，与上游 panel-app.js 的 PANEL_WIDGET_CONFIG 一致：
 *  Mini 区是宠物 + 两个额度条，完整区是外部账户与四个数值，其余进隐藏区。
 *  侧栏整理是桌面版新增的可选项，默认关闭（上游网页端默认开启）。 */
const PANEL_WIDGET_CONFIG = {
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

/**
 * 侧栏整理：隐藏官方品牌 logo，把新建对话行与折叠按钮对齐。
 *
 * 规则挂在 html.ksb-sidebar-tidy 上，选择器是官方侧栏结构
 * （aside.side > .col > .ch，含 .ch-brand/.ch-collapse/.sidebar-actions）；
 * 桌面客户端是同一套结构，规则可以直接复用。
 *
 * 安全闸沿用上游判断：头部除了 logo 与折叠按钮之外还有可见内容时
 * （某些版本的设备选择器）不挂，否则压平高度后会与绝对定位的按钮重叠。
 */
function tidyHeaderSafe() {
  const header = document.querySelector("aside.side .ch");
  if (!header) return true;
  const allowed = new Set();
  for (const selector of [".ch-brand", ".ch-collapse"]) {
    const element = header.querySelector(selector);
    if (!element) continue;
    for (let node = element; node && node !== header; node = node.parentElement) allowed.add(node);
    element.querySelectorAll("*").forEach((node) => allowed.add(node));
  }
  return ![...header.querySelectorAll("*")].some((element) => {
    if (allowed.has(element)) return false;
    const rect = element.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  });
}

function applyTidy(enabled) {
  const on = Boolean(enabled) && tidyHeaderSafe();
  document.documentElement.classList.toggle("ksb-sidebar-tidy", on);
}

// 桌面端 1.3.7 将窗口红绿灯与折叠按钮放在 .ch 之外的顶部栏，.ch 内没有 .ch-collapse；
// 保留 42px 顶栏高度以免「新建会话」上移后与折叠按钮重叠，同时仍紧凑收起多余留白。
const DESKTOP_TIDY_CSS = `
html.ksb-sidebar-tidy aside.side .ch:not(:has(.ch-collapse)) {
  height: 42px;
  min-height: 42px;
  padding: 0;
}
html.ksb-sidebar-tidy aside.side:has(.ch:not(:has(.ch-collapse))) .btn-wrap,
html.ksb-sidebar-tidy aside.side:has(.ch:not(:has(.ch-collapse))) .sidebar-actions {
  margin-top: 2px;
}
`;

export async function activate(ctx) {
  // localStorage 是同步接口，上游在模块求值时与渲染过程中都会读，先预取
  await bindStorage(ctx);
  const style = document.createElement("style");
  style.dataset.ekdpMonitor = "";
  style.textContent = MONITOR_CSS + DESKTOP_TIDY_CSS;
  document.head.append(style);

  const disposables = [];
  const track = (handle) => {
    if (handle) disposables.push(handle);
    return handle;
  };
  ctx.onDispose(() => {
    disposed = true;
    cancelRenderAll();
    if (tidyScan) clearTimeout(tidyScan);
    tidyScan = null;
    for (const handle of disposables.reverse()) {
      try {
        if (typeof handle === "function") handle();
        else handle.dispose?.();
      } catch (error) {
        ctx.log("warn", `面板释放失败：${error?.message || error}`);
      }
    }
    // 停用插件后不能留下改造过的侧栏
    document.documentElement.classList.remove("ksb-sidebar-tidy");
    style.remove();
    disposeHost();
  });

  // 独立面板标记：上游据此切换状态短词与空态文案（「在客户端里添加供应商」
  // 而不是「在扩展弹窗中配置 API Key」）。侧栏整理由本文件负责，与它无关。
  panel.standaloneMode = true;

  // 配置每次生效后同步侧栏整理。上游的 applySidebarTidy 在 standaloneMode
  // 下是空操作，所以这里挂在自己的写入路径上而不是替换它的实现。
  const syncTidy = () => applyTidy(panel.widgetConfig.modules.pet?.sidebarTidy === true);

  let datasource = null;
  let disposed = false;
  const isDisposed = () => disposed;

  initWidgetStructure({
    isDisposed,
    // 标题行点击：本地数值与折线样本清空重计，随后由数据源补齐
    manualRefresh: () => {
      resetMetrics();
      panel.sessionSamples.length = 0;
      panel.turnDurations.length = 0;
      renderAll();
      void datasource?.refresh();
    },
    refreshBalance: () => datasource?.refreshBalance(),
    // 授权由核心负责；恢复额度/外部账户模块可见时触发余额补拉
    beginOAuth: () => {},
    fetchQuota: () => datasource?.refreshBalance(),
    fetchExternalProviders: () => datasource?.refreshBalance(),
    // 没有「连接本地 CLI」的目录授权动作：锁位改述为统计积累中
    cliLockAccumulate: true,
    disableCacheHint: true,
    enableSidebarTidyMenu: true,
    onConfigChange: () => syncTidy()
  });

  initPet({ isDisposed, getSessionId, assets: ASSETS });
  initRender({
    isDisposed,
    petUpdateStatus,
    // 桌面漫游宠物不启用
    roamPetSetStatus: () => {},
    refreshBalance: () => datasource?.refreshBalance(),
    // 额度到点重置后立刻补拉一次，而不是等下一个 60 秒周期
    onQuotaReset: () => void datasource?.refresh(true)
  });

  installBridge();

  track(ctx.ui.mount("sidebar", (element) => {
    const mountPoint = document.createElement("div");
    mountPoint.className = "ekdp-monitor-host";
    element.append(mountPoint);
    mountWidget(mountPoint);
    return () => {
      disposePet();
      mountPoint.remove();
    };
  }));

  // 语言跟随客户端页面已经设好的 kimi-locale；缺省保持上游默认中文
  try {
    if (globalThis.localStorage?.getItem("kimi-locale")) syncLocaleFromPage();
  } catch (error) {
    // 读不到就保持默认语言
  }

  // 默认布局只在首次运行时写入：之后用户的拖拽结果优先
  try {
    if ((await ctx.storage.get(CONFIG_STORAGE_KEY)) == null) {
      await ctx.storage.set(CONFIG_STORAGE_KEY, PANEL_WIDGET_CONFIG);
      syncStorageKey(CONFIG_STORAGE_KEY, PANEL_WIDGET_CONFIG);
    }
  } catch (error) {
    ctx.log("warn", `面板布局写入失败：${error?.message || error}`);
  }
  await loadWidgetConfig();
  syncTidy();

  // 官方侧栏会随会话切换重建，类挂在 html 上不会丢；但 ≡ 菜单改动配置后
  // 需要重新评估安全闸，所以每次结构渲染后再同步一次。
  // The host re-renders the sidebar constantly. Evaluating the safety gate on
  // every mutation batch measured layout over and over, so the check is
  // coalesced into one pass after the DOM settles and skipped while the feature
  // is off.
  let tidyScan = null;
  const scheduleTidyScan = () => {
    if (tidyScan || disposed) return;
    tidyScan = setTimeout(() => {
      tidyScan = null;
      if (disposed) return;
      const wanted = panel.widgetConfig.modules.pet?.sidebarTidy === true;
      const applied = document.documentElement.classList.contains("ksb-sidebar-tidy");
      if (wanted !== applied) syncTidy();
    }, 250);
  };
  const tidyWatch = new MutationObserver(scheduleTidyScan);
  tidyWatch.observe(document.documentElement, { childList: true, subtree: true });
  track(() => {
    if (tidyScan) clearTimeout(tidyScan);
    tidyScan = null;
    tidyWatch.disconnect();
  });

  const onSettingsStorage = (event) => {
    const key = event?.detail?.key;
    if (key !== CONFIG_STORAGE_KEY && key !== MINI_STORAGE_KEY) return;
    syncStorageKey(key, event.detail.value);
    if (key === CONFIG_STORAGE_KEY) {
      applyWidgetConfig(event.detail.value);
      syncTidy();
    } else if (key === MINI_STORAGE_KEY) {
      applyModeClasses();
    }
  };
  globalThis.addEventListener?.("ekdp-monitor-storage-changed", onSettingsStorage);
  track(() => globalThis.removeEventListener?.("ekdp-monitor-storage-changed", onSettingsStorage));

  markBridgeReady();
  installStatusTicker();
  track(disposeBridge);
  track(disposeWidgetStructure);
  track(disposeRender);

  datasource = startDatasource(
    ctx,
    (message) => globalThis.__kcm?.push(message),
    // 上游 bridge 的 currentSessionId 默认是 'panel'（扩展在页面里自己找会话）。
    // 桌面上由宿主给出焦点会话：返回空串时退回 'panel'，数据源据此跳过逐轮读取，
    // 面板保留历史数字而不是清零。
    () => ctx.activeSessionId?.() || getSessionId(),
    // 余额只在当前视图可见的余额相关模块激活时轮询，模块收起或处于 Mini 未展示时即停止
    () => {
      const modules = panel.widgetConfig.modules;
      const isMini = Boolean(panel.els?.widget?.classList.contains("ksb-mini"));
      const isVisible = (m) => Boolean(m && m.show !== "hidden" && (!isMini || m.show === "mini"));
      return isVisible(modules.external)
        || (isVisible(modules.header) && modules.header?.stat === "balance")
        || (isVisible(modules.pet) && modules.pet?.stat === "balance");
    }
  );
  track(datasource);
  track(datasource.watchActivity());
  const onActiveSession = () => { void datasource?.syncSession(); };
  globalThis.addEventListener?.("ekd:active-session", onActiveSession);
  track(() => globalThis.removeEventListener?.("ekd:active-session", onActiveSession));

  // A different account has different quota, wallet and providers. The previous
  // numbers belong to someone else, so they are dropped before the forced
  // refresh instead of being shown until the next tick.
  const onAccountSwitched = () => {
    if (disposed) return;
    panel.lastWallet = null;
    panel.lastWalletFetchedAt = "";
    panel.lastQuotaPct = { "5h": null, week: null, month: null };
    panel.quotaResetAt = { "5h": null, week: null, month: null };
    panel.externalProviders = [];
    panel.externalUnsupported = [];
    resetMetrics();
    renderAll();
    void datasource?.resetForAccountSwitch();
  };
  globalThis.addEventListener?.("ekd:account-switched", onAccountSwitched);
  track(() => globalThis.removeEventListener?.("ekd:account-switched", onAccountSwitched));
  void datasource.start();

  return { status: "active" };
}
