import { useEffect, useState } from "react";
import { api, type PluginRecord } from "../api";
import { readAddonFeatures, writeAddonFeatures, type AddonFeatures, type NativeHost } from "../native";
import { syncPluginHost } from "../plugin-runtime/host";
import { AccountsManagerSection } from "./AccountsSection";
import { PluginSettings } from "./PluginSettings";
import { Toggle } from "./ui";
import { errorText } from "./workbench/pieces";
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
  const [plannerBusy, setPlannerBusy] = useState(false);
  const [plannerError, setPlannerError] = useState<string | null>(null);
  const [safeMode, setSafeMode] = useState(false);

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
      const [record, list] = await Promise.allSettled([
        api.plugins.get(PLANNER_PLUGIN_ID),
        api.plugins.list(),
      ]);
      if (!alive) return;
      if (list.status === "fulfilled") setSafeMode(Boolean(list.value.safeMode));
      if (record.status === "fulfilled") {
        setPlanner(record.value);
      } else if (list.status === "fulfilled") {
        // An uninstalled bundled plugin keeps a row in the list, and that row is
        // the only way to offer 恢复并启用.
        setPlanner(list.value.items.find((item) => item.id === PLANNER_PLUGIN_ID) ?? null);
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
              disabled={!planner || plannerBusy || (!plannerEnabled && (safeMode || planner.state === "CORRUPT"))}
              onChange={(value) => void togglePlanner(value)}
            />
          )}
        </div>
      </div>
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
    </div>
  );
}
