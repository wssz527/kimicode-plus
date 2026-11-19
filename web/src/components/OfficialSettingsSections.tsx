import { useEffect, useState } from "react";
import { readAddonFeatures, writeAddonFeatures, type AddonFeatures, type NativeHost } from "../native";
import { Toggle } from "./ui";
import { errorText } from "./workbench/pieces";
import "../styles/native-settings-surface.css";

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
  host: _host,
  fallbackReason,
}: {
  host: NativeHost;
  fallbackReason?: string;
}) {
  return (
    <div className="ekds ekds-official-sec" data-ekd-official-content="account">
      <div className="ekds-sec-head">
        <h3 className="ekds-sec-title">账号</h3>
      </div>
      {fallbackReason ? <FallbackNotice label="账户" reason={fallbackReason} /> : null}
      <div className="ekds-group" role="group" aria-label="Kimi Code 订阅账号">
        <div className="ekds-row">
          <span className="ekds-row-label">
            <strong>Kimi Code 订阅账号</strong>
            <small>
              这里用于管理多个 Kimi Code 订阅账号并一键切换，不退出客户端且不影响现有会话。下方官方账户与套餐用量保持原样显示当前账号。
            </small>
          </span>
          <span className="ekds-state">暂未保存其他账号</span>
        </div>
      </div>
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

  const enabled = features?.multiSession === true;

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
      </div>
      {features === null && !error ? <p className="ekds-note">正在读取实验开关状态…</p> : null}
    </div>
  );
}
