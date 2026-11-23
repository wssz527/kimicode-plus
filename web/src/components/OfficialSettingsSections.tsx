import { useEffect, useState } from "react";
import { api, type PluginActivity, type PluginLegal, type PluginRecord, type PluginUpdateItem } from "../api";
import { readAddonFeatures, writeAddonFeatures, type AddonFeatures, type NativeHost } from "../native";
import { syncPluginHost } from "../plugin-runtime/host";
import { AccountsManagerSection } from "./AccountsSection";
import { PluginSettings, capabilityLabel } from "./PluginSettings";
import { Toggle } from "./ui";
import { errorText } from "./workbench/pieces";
import { useResource } from "../lib/useResource";
import { fmtDateTime } from "../lib/format";
import "../styles/native-settings-surface.css";

const PLANNER_PLUGIN_ID = "kimicode-plus.planner";

export function FallbackNotice({ label, reason }: { label: string; reason?: string }) {
  return (
    <p className="ekds-fallback-note" role="status" data-ekd-fallback-notice="true">
      <strong>官方页面结构已变，暂时显示在这里。</strong>
      <span>{reason ? `${label}：${reason}` : `当前无法定位官方「${label}」设置区，恢复后会自动回到原位置。`}</span>
    </p>
  );
}

/**
 * Mount point rendered at the top of the official "账户" settings tab (and
 * inside KimiCode+ when the official account panel probe is disabled).
 */
export function AccountSettingsSection({
  host,
  fallbackReason,
}: {
  host: NativeHost;
  fallbackReason?: string;
}) {
  return <AccountsManagerSection host={host} fallbackReason={fallbackReason} />;
}

/**
 * Mount point for the official "插件" settings tab. The enhanced plugins are
 * separate from the official marketplace, so the section says so before the
 * list; the surface may also render it as a KimiCode+ fallback.
 */
export function PluginsSettingsSection({
  host,
  fallbackReason,
}: {
  host: NativeHost;
  fallbackReason?: string;
}) {
  return (
    <div className="ekds ekds-official-sec" data-ekd-official-content="plugins">
      {fallbackReason ? (
        <>
          <div className="ekds-sec-head">
            <h3 className="ekds-sec-title">KimiCode+ 增强插件</h3>
          </div>
          <FallbackNotice label="插件" reason={fallbackReason} />
        </>
      ) : null}
      <PluginSettings embedded host={host} />
    </div>
  );
}

/**
 * Mount point appended to the official "实验室" settings tab (and inside
 * KimiCode+ when the official lab panel probe is disabled).
 */
export function LabSettingsSection({
  host,
  fallbackReason,
}: {
  host: NativeHost;
  fallbackReason?: string;
}) {
  const [features, setFeatures] = useState<AddonFeatures | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [planner, setPlanner] = useState<PluginRecord | null>(null);
  const [plannerUpdate, setPlannerUpdate] = useState<PluginUpdateItem | null>(null);
  const [plannerUpdating, setPlannerUpdating] = useState(false);
  const [plannerConsentOpen, setPlannerConsentOpen] = useState(false);
  const [plannerPrivConfirmed, setPlannerPrivConfirmed] = useState(false);
  const [plannerFlash, setPlannerFlash] = useState<string | null>(null);
  const [plannerBusy, setPlannerBusy] = useState(false);
  const [plannerError, setPlannerError] = useState<string | null>(null);
  const [safeMode, setSafeMode] = useState(false);
  const [checkingPlannerUpdate, setCheckingPlannerUpdate] = useState(false);

  useEffect(() => {
    let alive = true;
    readAddonFeatures(host)
      .then((next) => {
        if (alive) setFeatures(next);
      })
      .catch((cause) => {
        if (alive) setError(errorText(cause));
      });
    const unsubscribe = host.subscribe<Partial<AddonFeatures>>("features", (next) => {
      if (!alive || !next || typeof next !== "object") return;
      if (typeof next.multiSession === "boolean") {
        setFeatures((current) => ({
          ...(current ?? { planner: false, multiSession: false }),
          multiSession: next.multiSession!,
        }));
      }
    });
    return () => {
      alive = false;
      unsubscribe();
    };
  }, [host]);

  useEffect(() => {
    let alive = true;
    const loadPlanner = async () => {
      // The list already carries the installed row and, for an uninstalled
      // bundled plugin, the restorable row. Asking GET /plugins/<id> as well
      // would answer 404 for exactly the uninstalled case this panel exists for.
      const [list, updates] = await Promise.allSettled([api.plugins.list(), api.plugins.updates()]);
      if (!alive) return;
      if (list.status === "fulfilled") {
        setSafeMode(Boolean(list.value.safeMode));
        setPlanner(list.value.items.find((item) => item.id === PLANNER_PLUGIN_ID) ?? null);
      }
      if (updates.status === "fulfilled") {
        const item = updates.value.items?.[PLANNER_PLUGIN_ID] ?? null;
        setPlannerUpdate(item?.updateAvailable ? item : null);
      }
    };
    void loadPlanner();
    const unsubscribe = host.subscribe("plugins", () => void loadPlanner());
    return () => {
      alive = false;
      unsubscribe();
    };
  }, [host]);

  async function toggleMultiSession(nextValue: boolean) {
    if (!features || busy) return;
    setBusy(true);
    setError(null);
    try {
      setFeatures(await writeAddonFeatures(host, { ...features, multiSession: nextValue }));
    } catch (cause) {
      setError(errorText(cause));
      setFeatures(await readAddonFeatures(host).catch(() => features));
    } finally {
      setBusy(false);
    }
  }

  async function syncAddonFlag(nextValue: boolean) {
    const current = features ?? (await readAddonFeatures(host).catch(() => null));
    if (!current) return;
    await writeAddonFeatures(host, { ...current, planner: nextValue })
      .then(setFeatures)
      .catch(() => {
        // The plugin is the source of truth for the entry; a stale addon flag
        // only affects hosts that predate the plugin.
      });
  }

  async function togglePlanner(nextValue: boolean) {
    if (plannerBusy) return;
    setPlannerBusy(true);
    setPlannerError(null);
    try {
      const record = nextValue
        ? await api.plugins.enable(PLANNER_PLUGIN_ID)
        : await api.plugins.disable(PLANNER_PLUGIN_ID);
      setPlanner(record);
      const syncMessage = await syncPluginHost();
      if (syncMessage) setPlannerError(syncMessage);
      else void syncAddonFlag(nextValue);
    } catch (cause) {
      setPlannerError(errorText(cause));
      await api.plugins.get(PLANNER_PLUGIN_ID).then(setPlanner).catch(() => undefined);
    } finally {
      setPlannerBusy(false);
    }
  }

  async function restorePlanner() {
    if (plannerBusy) return;
    setPlannerBusy(true);
    setPlannerError(null);
    try {
      await api.plugins.restore(PLANNER_PLUGIN_ID);
      const record = await api.plugins.enable(PLANNER_PLUGIN_ID);
      setPlanner(record);
      const syncMessage = await syncPluginHost();
      if (syncMessage) setPlannerError(syncMessage);
      else void syncAddonFlag(true);
    } catch (cause) {
      setPlannerError(errorText(cause));
    } finally {
      setPlannerBusy(false);
    }
  }

  async function checkPlannerUpdate() {
    if (checkingPlannerUpdate || plannerUpdating || plannerBusy) return;
    setCheckingPlannerUpdate(true);
    setPlannerError(null);
    setPlannerFlash(null);
    try {
      const status = await api.plugins.checkUpdates();
      const item = status.items?.[PLANNER_PLUGIN_ID] ?? null;
      setPlannerUpdate(item?.updateAvailable ? item : null);
      setPlannerFlash(
        item?.updateAvailable
          ? `「规划」可更新到 v${item.latestVersion}。`
          : "「规划」已是最新版本。",
      );
    } catch (cause) {
      setPlannerError(errorText(cause) || "无法检查「规划」更新，请稍后重试。");
    } finally {
      setCheckingPlannerUpdate(false);
    }
  }

  async function updatePlanner(confirmConsent = false) {
    if (!plannerUpdate || plannerUpdating || plannerBusy) return;
    const hasAddedPriv = (plannerUpdate.addedPrivileged?.length ?? 0) > 0;
    if (plannerUpdate.needsConsent && !confirmConsent) {
      setPlannerPrivConfirmed(false);
      setPlannerConsentOpen(true);
      return;
    }
    if (hasAddedPriv && !plannerPrivConfirmed) return;
    setPlannerUpdating(true);
    setPlannerError(null);
    setPlannerFlash(null);
    try {
      const updated = await api.plugins.update(PLANNER_PLUGIN_ID, {
        expectedVersion: plannerUpdate.latestVersion,
        confirmCapabilities: plannerUpdate.needsConsent ? true : undefined,
        confirmPrivileged: hasAddedPriv ? true : undefined,
      });
      setPlanner(updated);
      setPlannerUpdate(null);
      setPlannerConsentOpen(false);
      setPlannerPrivConfirmed(false);
      const syncMessage = await syncPluginHost();
      if (syncMessage) {
        setPlannerError(`已热更新到 v${updated.version}，但界面同步失败：${syncMessage}`);
      } else {
        setPlannerFlash(`「规划」已热更新到 v${updated.version}，无需重启 Kimi Code。`);
      }
    } catch (cause) {
      setPlannerError(errorText(cause));
    } finally {
      setPlannerUpdating(false);
    }
  }

  const enabled = features?.multiSession === true;
  const plannerUninstalled = planner?.state === "UNINSTALLED";
  const plannerEnabled = planner?.enabled === true;
  const plannerStatusIssue = !plannerError
    ? planner?.error
      ? `「规划」运行异常：${planner.error}`
      : planner?.state === "CORRUPT"
        ? "「规划」插件文件异常，暂时无法启用。"
        : planner?.state === "BLOCKED"
          ? "「规划」当前已被阻止启用。"
          : planner?.state === "FAULTED"
            ? "「规划」运行异常，已自动停用。"
            : null
    : null;
  const plannerIncompatible = Boolean(planner && !plannerEnabled && !plannerUninstalled && planner.compatible === false);

  return (
    <div className="ekds ekds-official-sec" data-ekd-official-content="lab">
      <div className="ekds-sec-head">
        <h3 className="ekds-sec-title">KimiCode+ 实验</h3>
      </div>
      {fallbackReason ? <FallbackNotice label="实验室" reason={fallbackReason} /> : null}
      {error ? <p className="ekds-note is-error">{error}</p> : null}
      <div className="ekds-group" role="group" aria-label="KimiCode+ 实验开关">
        <div className="ekds-row" data-ekd-feature="multiSession">
          <span className="ekds-row-label">
            <strong>会话并排</strong>
            <small>
              在同一个聊天区最多并排三个已有会话，用于对照和交叉引用。关闭后立即移除入口和已打开的并排面板。
            </small>
          </span>
          <Toggle
            label="会话并排"
            checked={enabled}
            disabled={!features || busy}
            onChange={(value) => void toggleMultiSession(value)}
          />
        </div>
        <div className="ekds-row" data-ekd-feature="planner">
          <span className="ekds-row-label">
            <strong>规划</strong>
            <small>
              在右侧面板中提供网页对话提取与本地规划引擎，支持可编辑预览后交接给会话。开启后立即显示入口，关闭后立即隐藏。
            </small>
          </span>
          <div className="ekds-row-actions">
            {!plannerUninstalled ? (
              <button
                type="button"
                className="ekds-btn"
                onClick={() => void checkPlannerUpdate()}
                disabled={checkingPlannerUpdate || plannerUpdating || plannerBusy}
              >
                {checkingPlannerUpdate ? "检查中…" : "检查更新"}
              </button>
            ) : null}
            {!plannerUninstalled && plannerUpdate?.updateAvailable && plannerUpdate.canApply ? (
              <button
                type="button"
                className="ekds-btn"
                onClick={() => void updatePlanner()}
                disabled={plannerUpdating || plannerBusy || safeMode}
                title={plannerUpdate.releaseNotes || `更新规划插件到 v${plannerUpdate.latestVersion}`}
              >
                {plannerUpdating ? "更新中…" : `更新到 v${plannerUpdate.latestVersion}`}
              </button>
            ) : null}
            {plannerUninstalled ? (
              <button
                type="button"
                className="ekds-btn"
                onClick={() => void restorePlanner()}
                disabled={plannerBusy || safeMode}
              >
                {plannerBusy ? "正在恢复…" : "恢复并启用"}
              </button>
            ) : (
              <Toggle
                label="规划"
                checked={plannerEnabled}
                disabled={!planner || plannerBusy || plannerUpdating || (!plannerEnabled && (safeMode || planner.state === "CORRUPT"))}
                onChange={(value) => void togglePlanner(value)}
              />
            )}
          </div>
        </div>
      </div>
      {plannerConsentOpen && plannerUpdate?.updateAvailable && plannerUpdate.needsConsent ? (
        <div className="plugin-confirm" role="dialog" aria-modal="true" aria-label="确认规划插件新增权限">
          <p>
            <strong>「规划」v{plannerUpdate.latestVersion} 申请了新增权限：</strong>
          </p>
          <ul className="ekds-note-list">
            {(plannerUpdate.addedCapabilities ?? []).map((name) => {
              const label = capabilityLabel(name);
              return (
                <li key={name}>
                  <strong>{label.title}</strong>
                  {(plannerUpdate.addedPrivileged ?? []).includes(name) ? "（高权限）" : ""}
                  <span className="ekds-muted"> · {label.detail}</span>
                </li>
              );
            })}
          </ul>
          {(plannerUpdate.addedPrivileged?.length ?? 0) > 0 ? (
            <label className="capability-choice">
              <input
                type="checkbox"
                checked={plannerPrivConfirmed}
                onChange={(event) => setPlannerPrivConfirmed(event.target.checked)}
              />
              <span>我了解上述新增高权限能力的影响范围，确认授予并更新</span>
            </label>
          ) : null}
          <div className="inline-actions">
            <button
              type="button"
              className="ekds-btn"
              onClick={() => void updatePlanner(true)}
              disabled={plannerUpdating || ((plannerUpdate.addedPrivileged?.length ?? 0) > 0 && !plannerPrivConfirmed)}
            >
              {plannerUpdating ? "更新中…" : `确认并更新到 v${plannerUpdate.latestVersion}`}
            </button>
            <button
              type="button"
              className="ekds-btn"
              onClick={() => setPlannerConsentOpen(false)}
              disabled={plannerUpdating}
            >
              取消
            </button>
          </div>
        </div>
      ) : null}
      {plannerFlash ? <p className="ekds-note" role="status">{plannerFlash}</p> : null}
      {!plannerUninstalled && plannerUpdate?.updateAvailable && !plannerUpdate.canApply && plannerUpdate.cannotApplyReason ? (
        <p className="ekds-note">「规划」有新版本 v{plannerUpdate.latestVersion}：{plannerUpdate.cannotApplyReason}</p>
      ) : null}
      {plannerError ? <p className="ekds-note is-error" role="alert">{plannerError}</p> : null}
      {plannerStatusIssue ? (
        <p className="ekds-note is-error" role="alert">{plannerStatusIssue}</p>
      ) : null}
      {plannerIncompatible && !plannerError && !plannerStatusIssue ? (
        <p className="ekds-note">「规划」尚未在当前宿主版本验证，启用可能受限。</p>
      ) : null}
      {safeMode && !plannerError ? (
        <p className="ekds-note">安全模式已开启，所有增强插件都被停用，「规划」暂时无法启用。</p>
      ) : null}
      {features === null && !error ? <p className="ekds-note">正在读取实验开关状态…</p> : null}
      <PlannerDetails planner={planner} />
    </div>
  );
}

/**
 * The planner's licence and activity log, a disclosure rather than a link out:
 * the plugin page is filtered to official marketplaces, and a lab visitor who
 * wants to check what the planner did should not have to leave the tab.
 */
function PlannerDetails({ planner }: { planner: PluginRecord | null }) {
  const [section, setSection] = useState<"license" | "notice" | "activity" | null>(null);
  const installed = planner !== null && planner.state !== "UNINSTALLED";
  const legal = useResource<PluginLegal>(
    `lab/planner/legal/${planner?.version ?? ""}/${planner?.updatedAt ?? ""}`,
    () => api.pluginLegal(PLANNER_PLUGIN_ID),
    { enabled: installed && (section === "license" || section === "notice") },
  );
  const activity = useResource<PluginActivity[]>(
    `lab/planner/activity/${planner?.updatedAt ?? ""}`,
    async () => (await api.plugins.activity(PLANNER_PLUGIN_ID)).items,
    { enabled: installed && section === "activity" },
  );
  if (!installed) return null;
  const notice = section === "notice";
  const text = notice ? legal.data?.noticeText : legal.data?.licenseText;
  const truncated = notice ? legal.data?.noticeTruncated : legal.data?.licenseTruncated;
  const filename = notice ? legal.data?.noticeFile || "NOTICE" : "LICENSE";
  return (
    <details className="ekds-plugins planner-details" data-ekd-planner-details="true">
      <summary>
        <span>「规划」开源许可与活动记录</span>
        <span className="num">v{planner.version}</span>
      </summary>
      <div className="inline-actions">
        <button type="button" className="ekds-btn" onClick={() => setSection(section === "license" ? null : "license")}>
          查看 LICENSE
        </button>
        {planner.hasNotice ? (
          <button type="button" className="ekds-btn" onClick={() => setSection(section === "notice" ? null : "notice")}>
            查看 NOTICE
          </button>
        ) : null}
        <button type="button" className="ekds-btn" onClick={() => setSection(section === "activity" ? null : "activity")}>
          活动记录
        </button>
      </div>
      {section === "license" || section === "notice" ? (
        legal.loading ? (
          <p className="ekds-note">正在读取 {filename}…</p>
        ) : legal.error ? (
          <p className="ekds-note is-error" role="alert">无法读取 {filename}：{errorText(legal.error)}</p>
        ) : text ? (
          <>
            {truncated ? <p className="ekds-note">{filename} 超过 128 KiB，此处显示文件开头。</p> : null}
            <pre className="plugin-legal-text" tabIndex={0} aria-label={filename}>{text}</pre>
          </>
        ) : (
          <p className="ekds-note">{filename} 没有可显示的内容。</p>
        )
      ) : null}
      {section === "activity" ? (
        activity.loading ? (
          <p className="ekds-note">正在读取活动记录…</p>
        ) : activity.error ? (
          <p className="ekds-note is-error" role="alert">无法读取活动记录：{errorText(activity.error)}</p>
        ) : (activity.data ?? []).length === 0 ? (
          <p className="ekds-note">还没有记录。启用、停用和「规划」发起的动作都会记录在这里。</p>
        ) : (
          <ul className="activity-list">
            {(activity.data ?? []).map((item) => (
              <li key={item.id}>
                <span className="num">{fmtDateTime(item.at)}</span>
                <span>{item.detail}</span>
              </li>
            ))}
          </ul>
        )
      ) : null}
    </details>
  );
}
