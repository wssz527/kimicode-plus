import { useCallback, useEffect, useState } from "react";
import { features } from "../features";
import {
  beginAddAccount,
  cancelPendingAccountSwitch,
  completeAddAccount,
  deleteAccount,
  formatAccountQuotaLine,
  listAccounts,
  preflightAccountSwitch,
  refreshAccountQuotas,
  renameAccount,
  resetAccountVault,
  saveCurrentAccount,
  switchAccount,
  type AccountSummary,
  type AccountSwitchPreflight,
  type AccountsState,
  type NativeHost,
} from "../native";
import { errorText } from "./workbench/pieces";
import { FallbackNotice } from "./OfficialSettingsSections";
import "../styles/native-settings-surface.css";

/** The two-step confirmation for retiring a vault nobody can unlock any more. */
function VaultResetPanel({
  systemManaged,
  busy,
  onConfirm,
  onCancel,
}: {
  systemManaged: boolean;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <div className="ekds-acc-confirm" role="group" aria-label="重置凭据库确认" data-ekd-vault-reset="confirm">
      <strong>确认重置凭据库？</strong>
      <p className="ekds-note">
        重置会清空旧凭据库中已加密的账号令牌和插件密钥，
        {systemManaged
          ? "并立即建立一个由系统凭据库托管的新库。"
          : "随后需要设置一个新的加密库密码。"}
        这不会让你退出当前官方 Kimi 账号，登录状态保持不变；重置后重新点一次「保存当前账号」即可继续使用。
      </p>
      <div className="ekds-acc-confirm-actions">
        <button type="button" className="ekds-btn is-danger" disabled={busy} onClick={onConfirm}>
          {busy ? "重置中…" : "确认重置凭据库"}
        </button>
        <button type="button" className="ekds-btn" disabled={busy} onClick={onCancel}>
          取消
        </button>
      </div>
    </div>
  );
}

function dispatchAccountSwitched() {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent("ekd:account-switched"));
  }
}

/** Names the store holding the vault key, for the one-line auto-unlock note. */
function keychainLabel(backend?: string): string {
  if (backend === "MACOS_KEYCHAIN") return "由 macOS 钥匙串自动解锁";
  if (backend === "WINDOWS_DPAPI") return "由 Windows DPAPI 自动解锁";
  return "系统凭据库已自动解锁";
}

function readActiveOfficialModel(): string | undefined {
  try {
    const app = (document.querySelector("#app") as (Element & {
      __vue_app__?: { config?: { globalProperties?: { $pinia?: { _s?: Map<string, Record<string, unknown>> } } } };
    }) | null)?.__vue_app__;
    const pinia = app?.config?.globalProperties?.$pinia?._s;
    const sessions = pinia?.get("kimi.sessions");
    const activeId = typeof sessions?.activeSessionId === "string" ? sessions.activeSessionId : "";
    const sessionList = Array.isArray(sessions?.sessions) ? (sessions.sessions as Record<string, unknown>[]) : [];
    const current = sessionList.find((item) => item && item.id === activeId);
    const sessionModel = typeof current?.model === "string" ? current.model : "";
    if (sessionModel) return sessionModel;
    const configStore = pinia?.get("kimi.config");
    const cfg = configStore?.config as Record<string, unknown> | undefined;
    if (typeof cfg?.defaultModel === "string" && cfg.defaultModel) return cfg.defaultModel;
    if (typeof cfg?.default_model === "string" && cfg.default_model) return cfg.default_model;
  } catch {
    // ignore DOM lookup errors in tests or detached mounts
  }
  return undefined;
}

function AccountAvatar({ name, avatar }: { name: string; avatar?: string }) {
  const [failed, setFailed] = useState(false);
  const initial = (name.trim()[0] ?? "K").toUpperCase();
  if (avatar && !failed) {
    return (
      <img
        className="ekds-acc-avatar"
        src={avatar}
        alt=""
        referrerPolicy="no-referrer"
        onError={() => setFailed(true)}
      />
    );
  }
  return (
    <span className="ekds-acc-avatar is-fallback" aria-hidden="true">
      {initial}
    </span>
  );
}

export function AccountSwitchConfirmPanel({
  preflight,
  busy,
  busyKind,
  error,
  onConfirm,
  onForceConfirm,
  onWaitUntilIdle,
  onRelogin,
  onCancel,
}: {
  preflight: AccountSwitchPreflight;
  busy: boolean;
  busyKind?: "confirm" | "force" | "wait" | null;
  error: string | null;
  onConfirm: () => void;
  onForceConfirm: () => void;
  onWaitUntilIdle: () => void;
  onRelogin: () => void;
  onCancel: () => void;
}) {
  const target = preflight.target;
  const hasBusy = preflight.busySessions.length > 0;
  const canForce = preflight.canForceSwitch ?? (preflight.vaultUnlocked && !preflight.requiresLogin && !target.current);

  return (
    <div className="ekds-acc-confirm" role="dialog" aria-label={`切换到 ${target.label}`} data-ekd-account-confirm="true">
      <div className="ekds-acc-confirm-head">
        <AccountAvatar name={target.label} avatar={target.avatar} />
        <div className="ekds-acc-confirm-title">
          <strong>切换到「{target.label}」</strong>
          <small>{target.plan ? `套餐：${target.plan}` : target.nickname}</small>
        </div>
      </div>

      {preflight.requiresLogin ? (
        <p className="ekds-note is-warn">
          该账号保存的令牌已失效，无法直接切换。请先重新登录后再切换。
        </p>
      ) : null}

      {!preflight.vaultUnlocked ? (
        <p className="ekds-note is-warn">
          加密凭据库尚未解锁，请先解锁后再执行切换。
        </p>
      ) : null}

      {hasBusy ? (
        <div className="ekds-acc-busy-box">
          <strong>以下会话仍在运行中：</strong>
          <ul>
            {preflight.busySessions.map((session) => (
              <li key={session.id}>
                <span className="ekds-acc-busy-title" title={session.title}>{session.title}</span>
                <small>{session.reason}</small>
              </li>
            ))}
          </ul>
          <p className="ekds-note">
            建议等它们完成后自动切换；也可以仍然立即切换，运行中的会话后续请求将改用新账号（首次不会命中缓存）。
          </p>
        </div>
      ) : null}

      {preflight.warnings.length > 0 && !preflight.requiresLogin ? (
        <ul className="ekds-acc-warnings">
          {preflight.warnings.map((warning, index) => (
            <li key={index}>{warning}</li>
          ))}
        </ul>
      ) : null}

      {error ? <p className="ekds-note is-error">{error}</p> : null}

      <div className="ekds-acc-confirm-actions">
        <button type="button" className="ekds-btn" disabled={busy} onClick={onCancel}>
          取消
        </button>
        {preflight.requiresLogin ? (
          <button type="button" className="ekds-btn is-primary" disabled={busy} onClick={onRelogin}>
            重新登录
          </button>
        ) : hasBusy ? (
          <>
            <button
              type="button"
              className="ekds-btn"
              data-ekd-account-force-switch="true"
              disabled={busy || !canForce}
              onClick={onForceConfirm}
            >
              {busy && busyKind === "force" ? "正在切换…" : "仍然立即切换"}
            </button>
            <button
              type="button"
              className="ekds-btn is-primary"
              disabled={busy || !canForce}
              onClick={onWaitUntilIdle}
            >
              {busy && busyKind === "wait" ? "正在排队…" : "等完成后自动切换"}
            </button>
          </>
        ) : (
          <button
            type="button"
            className="ekds-btn is-primary"
            disabled={busy || !preflight.canSwitch}
            onClick={onConfirm}
          >
            {busy && busyKind !== "wait" ? "正在切换…" : "确认切换"}
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * Modal surface used when the user selects a non-current account from the
 * official sidebar bottom user menu (.user-menu).
 */
export function AccountSwitchModal({
  host,
  targetId,
  onClose,
}: {
  host: NativeHost;
  targetId: string;
  onClose: () => void;
}) {
  const [preflight, setPreflight] = useState<AccountSwitchPreflight | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyKind, setBusyKind] = useState<"confirm" | "force" | "wait" | null>(null);
  const busy = busyKind !== null;
  const [error, setError] = useState<string | null>(null);
  const [backupWarning, setBackupWarning] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    preflightAccountSwitch(host, targetId, readActiveOfficialModel())
      .then((res) => {
        if (alive) setPreflight(res);
      })
      .catch((cause) => {
        if (alive) setError(errorText(cause));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [host, targetId]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy, onClose]);

  async function runSwitch(waitUntilIdle: boolean, force = false) {
    if (busy) return;
    setBusyKind(force ? "force" : waitUntilIdle ? "wait" : "confirm");
    setError(null);
    try {
      const result = await switchAccount(host, targetId, waitUntilIdle, force);
      if (result.switched) {
        dispatchAccountSwitched();
        if (result.backupCleanupWarning) {
          setBackupWarning(result.backupCleanupWarning);
          return;
        }
      }
      onClose();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyKind(null);
    }
  }

  async function runRelogin() {
    setBusyKind("confirm");
    try {
      await beginAddAccount(host, targetId);
      await host.openSettings("account").catch(() => {});
      onClose();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyKind(null);
    }
  }

  return (
    <div
      className="ekds ekds-acc-modal-backdrop"
      role="presentation"
      onClick={(event) => {
        if (event.target === event.currentTarget && !busy) onClose();
      }}
    >
      <div className="ekds-acc-modal-card">
        {backupWarning ? (
          <div className="ekds-acc-confirm">
            <strong>账号已切换</strong>
            <p className="ekds-note is-warn" role="status">{backupWarning}</p>
            <div className="ekds-acc-confirm-actions">
              <button type="button" className="ekds-btn" onClick={onClose}>关闭</button>
            </div>
          </div>
        ) : loading ? (
          <p className="ekds-note">正在检查会话状态与账号信息…</p>
        ) : preflight ? (
          <AccountSwitchConfirmPanel
            preflight={preflight}
            busy={busy}
            busyKind={busyKind}
            error={error}
            onConfirm={() => void runSwitch(false)}
            onForceConfirm={() => void runSwitch(false, true)}
            onWaitUntilIdle={() => void runSwitch(true)}
            onRelogin={() => void runRelogin()}
            onCancel={onClose}
          />
        ) : (
          <div className="ekds-acc-confirm">
            <p className="ekds-note is-error">{error ?? "无法读取目标账号信息"}</p>
            <div className="ekds-acc-confirm-actions">
              <button type="button" className="ekds-btn" onClick={onClose}>
                关闭
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * Mounted at the top of the official "账户" settings tab (and inside KimiCode+
 * only when the official account panel probe is disabled).
 */
export function AccountsManagerSection({
  host,
  fallbackReason,
}: {
  host: NativeHost;
  fallbackReason?: string;
}) {
  const [state, setState] = useState<AccountsState | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyAction, setBusyAction] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [backupWarning, setBackupWarning] = useState<string | null>(null);

  const [vaultPassword, setVaultPassword] = useState("");
  const [confirmResetVault, setConfirmResetVault] = useState(false);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameText, setRenameText] = useState("");
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);

  const [preflight, setPreflight] = useState<AccountSwitchPreflight | null>(null);
  const [switchError, setSwitchError] = useState<string | null>(null);

  const load = useCallback(
    async (refreshQuota = false) => {
      try {
        const next = await listAccounts(host, refreshQuota);
        setState(next);
        setError(null);
      } catch (cause) {
        setError(errorText(cause));
      } finally {
        setLoading(false);
      }
    },
    [host],
  );

  useEffect(() => {
    void load(false);
    const unsub = host.subscribe<{ type?: string; refreshAuth?: boolean; error?: string; backupCleanupWarning?: string }>("accounts", (event) => {
      if (event?.type === "account.switched" && event.refreshAuth) {
        dispatchAccountSwitched();
        setNotice("已完成账号切换。");
        setBackupWarning(event.backupCleanupWarning || null);
      } else if (event?.type === "account.switch_failed" && event.error) {
        setError(event.error);
      }
      void load(false);
    });
    return () => {
      unsub();
    };
  }, [host, load]);

  // Refresh non-current account quotas once after vault unlocks if any reading is missing or >60s old.
  const nonCurrentAccounts = state ? state.accounts.filter((a) => !a.current) : [];
  const needsAutoQuotaRefresh = nonCurrentAccounts.some((a) => {
    if (a.status === "relogin_required") return false;
    if (!a.quota?.checkedAt) return true;
    const checkedMs = Date.parse(a.quota.checkedAt);
    return !Number.isFinite(checkedMs) || Date.now() - checkedMs > 60_000;
  });
  const vaultUnlocked = state?.vaultUnlocked === true;
  // A locked vault on a machine with an OS key store, or one the core already
  // marked as a password vault, is the old manual kind: the user may not
  // remember the password and needs the reset path, not just a prompt.
  const legacyVault = Boolean(state && !state.vaultUnlocked && (state.legacyPasswordVault || state.vaultOsKeychainAvailable));
  // Only promise automatic handover where a key store can actually take the
  // password; without one the vault stays manual and must keep saying so.
  const migratingVault = Boolean(state && !state.vaultUnlocked && state.vaultOsKeychainAvailable);
  const [autoQuotaFetched, setAutoQuotaFetched] = useState(false);
  useEffect(() => {
    if (!vaultUnlocked || !needsAutoQuotaRefresh || autoQuotaFetched) return;
    setAutoQuotaFetched(true);
    void refreshAccountQuotas(host)
      .then(() => load(false))
      .catch(() => {});
  }, [vaultUnlocked, needsAutoQuotaRefresh, autoQuotaFetched, host, load]);

  // Auto-detect new sign-in while guided add/relogin flow is active.
  const addFlowActive = state?.addFlow?.active === true;
  useEffect(() => {
    if (!addFlowActive) return;
    const timer = window.setInterval(() => {
      void completeAddAccount(host)
        .then((res) => {
          if (res.completed && res.account) {
            setNotice(`已保存账号「${res.account.label}」。`);
            void load(false);
          }
        })
        .catch(() => {});
    }, 2500);
    return () => window.clearInterval(timer);
  }, [addFlowActive, host, load]);

  async function handleUnlockVault() {
    if (!vaultPassword.trim() || busyAction) return;
    setBusyAction("unlock");
    setError(null);
    setNotice(null);
    try {
      await features.unlock(vaultPassword);
      setVaultPassword("");
      setNotice(migratingVault ? "凭据库已解锁，以后将由系统凭据库自动托管。" : "加密凭据库已解锁。");
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleResetVault() {
    if (busyAction) return;
    setBusyAction("reset-vault");
    setError(null);
    setNotice(null);
    try {
      const next = await resetAccountVault(host);
      // The reset answer is the fresh state, so no extra read is needed.
      setState(next);
      setConfirmResetVault(false);
      setVaultPassword("");
      setNotice(
        next.vaultUnlocked
          ? "已重置并切换为系统自动托管，现在可以直接保存和切换账号。"
          : "凭据库已重置。请设置一个新的加密库密码后继续保存账号。",
      );
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleSaveCurrent() {
    if (busyAction) return;
    setBusyAction("save-current");
    setError(null);
    setNotice(null);
    try {
      const saved = await saveCurrentAccount(host);
      setNotice(`已保存当前账号「${saved.label}」。`);
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleBeginAdd(reloginId?: string) {
    if (busyAction) return;
    setBusyAction(reloginId ? `relogin:${reloginId}` : "begin-add");
    setError(null);
    setNotice(null);
    setPreflight(null);
    try {
      await beginAddAccount(host, reloginId);
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleCompleteAdd(cancel = false) {
    if (busyAction) return;
    setBusyAction("complete-add");
    setError(null);
    try {
      const res = await completeAddAccount(host, cancel ? { cancel: true } : {});
      if (res.cancelled) {
        setNotice("已取消添加账号。");
      } else if (res.completed && res.account) {
        setNotice(`已保存账号「${res.account.label}」。`);
      } else if (res.reason) {
        setNotice(res.reason);
      }
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleRefreshQuotas() {
    if (busyAction) return;
    setBusyAction("refresh-quotas");
    setError(null);
    try {
      await refreshAccountQuotas(host);
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleOpenSwitch(account: AccountSummary) {
    if (busyAction) return;
    setBusyAction(`preflight:${account.id}`);
    setError(null);
    setSwitchError(null);
    setNotice(null);
    try {
      const res = await preflightAccountSwitch(host, account.id, readActiveOfficialModel());
      setPreflight(res);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleConfirmSwitch(waitUntilIdle: boolean, force = false) {
    if (!preflight || busyAction) return;
    setBusyAction(force ? "switch:force" : waitUntilIdle ? "switch:wait" : "switch:confirm");
    setSwitchError(null);
    try {
      const res = await switchAccount(host, preflight.target.id, waitUntilIdle, force);
      setPreflight(null);
      if (res.queued && res.pendingSwitch) {
        setNotice(`已加入等待队列：待运行中的会话空闲后将自动切换到「${res.pendingSwitch.targetLabel}」。`);
      } else if (res.switched && res.account) {
        dispatchAccountSwitched();
        setNotice(`已切换到账号「${res.account.label}」，现有 ${res.sessionsCount ?? 0} 个会话均保持完整。`);
        setBackupWarning(res.backupCleanupWarning || null);
      }
      await load(false);
    } catch (cause) {
      setSwitchError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleForceSwitchPending(targetId: string) {
    if (busyAction) return;
    setBusyAction("force-pending");
    setError(null);
    setNotice(null);
    try {
      const res = await switchAccount(host, targetId, false, true);
      if (res.switched && res.account) {
        dispatchAccountSwitched();
        setNotice(`已立即切换到账号「${res.account.label}」，现有 ${res.sessionsCount ?? 0} 个会话均保持完整。`);
        setBackupWarning(res.backupCleanupWarning || null);
      }
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleCancelPending() {
    if (busyAction) return;
    setBusyAction("cancel-pending");
    try {
      await cancelPendingAccountSwitch(host);
      setNotice("已取消自动切换排队。");
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleRenameSubmit(id: string) {
    const trimmed = renameText.trim();
    if (!trimmed || busyAction) return;
    setBusyAction(`rename:${id}`);
    setError(null);
    try {
      await renameAccount(host, id, trimmed);
      setRenamingId(null);
      setRenameText("");
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  async function handleDelete(id: string) {
    if (busyAction) return;
    setBusyAction(`delete:${id}`);
    setError(null);
    try {
      await deleteAccount(host, id);
      setConfirmDeleteId(null);
      setNotice("已从已保存列表中移除该账号。");
      await load(false);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusyAction(null);
    }
  }

  const nonCurrentCount = nonCurrentAccounts.length;
  const accounts = state?.accounts ?? [];
  const currentAlreadySaved = Boolean(state?.currentAccountId);

  return (
    <div className="ekds ekds-official-sec" data-ekd-official-content="account">
      <div className="ekds-sec-head">
        <h3 className="ekds-sec-title">账号</h3>
        <div className="ekds-acc-head-actions">
          {vaultUnlocked && nonCurrentCount > 0 ? (
            <button
              type="button"
              className="ekds-btn"
              disabled={busyAction !== null}
              onClick={() => void handleRefreshQuotas()}
            >
              {busyAction === "refresh-quotas" ? "刷新中…" : "刷新额度"}
            </button>
          ) : null}
        </div>
      </div>

      {fallbackReason ? <FallbackNotice label="账户" reason={fallbackReason} /> : null}
      {error ? <p className="ekds-note is-error" role="alert">{error}</p> : null}
      {notice ? <p className="ekds-note is-ok" role="status">{notice}</p> : null}
      {backupWarning ? <p className="ekds-note is-warn" role="status">{backupWarning}</p> : null}

      {state?.pendingSwitch ? (
        <div className="ekds-acc-banner" role="status" data-ekd-account-pending="true">
          <span>
            正在等待 {state.pendingSwitch.busySessionIds.length || 1} 个会话空闲，完成后将自动切换到「{state.pendingSwitch.targetLabel}」。
          </span>
          <div className="ekds-acc-banner-actions">
            <button
              type="button"
              className="ekds-btn is-primary"
              data-ekd-account-force-pending="true"
              disabled={busyAction !== null}
              onClick={() => void handleForceSwitchPending(state.pendingSwitch!.targetId)}
            >
              {busyAction === "force-pending" ? "切换中…" : "立即切换"}
            </button>
            <button
              type="button"
              className="ekds-btn"
              disabled={busyAction !== null}
              onClick={() => void handleCancelPending()}
            >
              取消等待
            </button>
          </div>
        </div>
      ) : null}

      {state?.addFlow?.active ? (
        <div className="ekds-acc-banner" role="status" data-ekd-account-addflow="true">
          <div className="ekds-acc-banner-copy">
            <strong>{state.addFlow.mode === "relogin" ? "正在重新登录账号" : "正在添加另一个账号"}</strong>
            <span>
              当前账号已先加密备份。请在下方官方账户区退出并登录目标账号，登录完成后会自动检测并保存。
            </span>
          </div>
          <div className="ekds-acc-banner-actions">
            <button
              type="button"
              className="ekds-btn is-primary"
              disabled={busyAction !== null}
              onClick={() => void handleCompleteAdd(false)}
            >
              {busyAction === "complete-add" ? "检测中…" : "检测并保存"}
            </button>
            <button
              type="button"
              className="ekds-btn"
              disabled={busyAction !== null}
              onClick={() => void handleCompleteAdd(true)}
            >
              取消
            </button>
          </div>
        </div>
      ) : null}

      {state && state.vaultUnlocked && state.vaultOsKeychain ? (
        <p className="ekds-note" role="status" data-ekd-vault-os="true">
          {keychainLabel(state.vaultOsKeychainBackend)}
        </p>
      ) : null}

      {state && !state.vaultUnlocked ? (
        <form
          className="ekds-group ekds-acc-unlock"
          onSubmit={(event) => {
            event.preventDefault();
            void handleUnlockVault();
          }}
        >
          <div className="ekds-row">
            <span className="ekds-row-label">
              <strong>{legacyVault ? "检测到旧版密码保护的凭据库" : "解锁加密凭据库"}</strong>
              <small>
                {legacyVault
                  ? migratingVault
                    ? "新版本已支持由系统凭据库自动托管，无需记忆密码。你可以输入原密码解锁一次（解锁后会自动转入系统托管，以后不再询问）；如果忘记了旧密码，可直接重置凭据库并重新保存账号。"
                    : "这是旧版密码保护的凭据库，需要原密码才能解锁。如果忘记了旧密码，可以重置凭据库并重新保存账号。"
                  : "账号令牌只加密保存在本地增强核心，渲染界面不会接触明文令牌。输入主密码解锁后即可保存与切换账号。"}
              </small>
            </span>
            <div className="ekds-acc-unlock-controls">
              <input
                type="password"
                className="ekds-input"
                placeholder={legacyVault ? "输入原密码" : "输入加密库密码"}
                autoComplete="current-password"
                value={vaultPassword}
                disabled={busyAction !== null}
                onChange={(event) => setVaultPassword(event.target.value)}
              />
              <button
                type="submit"
                className="ekds-btn is-primary"
                disabled={!vaultPassword.trim() || busyAction !== null}
              >
                {busyAction === "unlock" ? "解锁中…" : migratingVault ? "解锁并转为自动托管" : "解锁"}
              </button>
            </div>
          </div>
          {legacyVault ? (
            <div className="ekds-row">
              <span className="ekds-row-label">
                <strong>忘记了旧密码？</strong>
                <small>
                  {migratingVault
                    ? "可以重置凭据库：旧的加密账号令牌与插件密钥会被清空，并立即建立一个由系统托管的新库；当前官方登录不受影响。"
                    : "可以重置凭据库：旧的加密账号令牌与插件密钥会被清空，随后可设置新的加密库密码；当前官方登录不受影响。"}
                </small>
              </span>
              <button
                type="button"
                className="ekds-btn"
                disabled={busyAction !== null}
                data-ekd-vault-reset="open"
                onClick={() => setConfirmResetVault(true)}
              >
                忘记密码，重置凭据库
              </button>
            </div>
          ) : null}
        </form>
      ) : null}

      {state && !state.vaultUnlocked && confirmResetVault ? (
        <VaultResetPanel
          systemManaged={migratingVault}
          busy={busyAction === "reset-vault"}
          onConfirm={() => void handleResetVault()}
          onCancel={() => setConfirmResetVault(false)}
        />
      ) : null}

      <div className="ekds-group" role="list" aria-label="已保存的 Kimi Code 账号">
        {loading ? (
          <div className="ekds-row">
            <span className="ekds-row-label">
              <small>正在读取已保存账号…</small>
            </span>
          </div>
        ) : accounts.length === 0 ? (
          <div className="ekds-row">
            <span className="ekds-row-label">
              <strong>暂未保存账号</strong>
              <small>
                {state?.signedIn
                  ? `当前已登录「${state.currentProfile?.nickname ?? "Kimi Code 账号"}」，点击下方「保存当前账号」即可加入切换列表。`
                  : "请先在下方官方账户区登录 Kimi Code 订阅账号，再点击「保存当前账号」。"}
              </small>
            </span>
          </div>
        ) : (
          accounts.map((account) => {
            const needsRelogin = account.status === "relogin_required";
            const isRenaming = renamingId === account.id;
            const isDeleting = confirmDeleteId === account.id;
            return (
              <div
                key={account.id}
                className={`ekds-row ekds-acc-row${account.current ? " is-current" : ""}`}
                role="listitem"
                data-ekd-account-id={account.id}
                data-ekd-account-current={account.current ? "true" : "false"}
              >
                <div className="ekds-acc-main">
                  <AccountAvatar name={account.label} avatar={account.avatar} />
                  <div className="ekds-row-label">
                    {isRenaming ? (
                      <form
                        className="ekds-acc-rename"
                        onSubmit={(event) => {
                          event.preventDefault();
                          void handleRenameSubmit(account.id);
                        }}
                      >
                        <input
                          type="text"
                          className="ekds-input"
                          value={renameText}
                          maxLength={80}
                          autoFocus
                          onChange={(event) => setRenameText(event.target.value)}
                        />
                        <button type="submit" className="ekds-btn is-primary" disabled={!renameText.trim() || busyAction !== null}>
                          保存
                        </button>
                        <button type="button" className="ekds-btn" onClick={() => setRenamingId(null)}>
                          取消
                        </button>
                      </form>
                    ) : (
                      <>
                        <span className="ekds-acc-title-line">
                          <strong>{account.label}</strong>
                          {account.plan ? <span className="ekds-acc-plan">{account.plan}</span> : null}
                          {account.current ? (
                            <span className="ekds-acc-check" aria-label="当前账号">
                              ✓ 当前
                            </span>
                          ) : null}
                          {needsRelogin ? <span className="ekds-state is-warn">需要重新登录</span> : null}
                        </span>
                        <small>
                          {account.current
                            ? "当前账号（下方官方「套餐用量」显示实时额度）"
                            : needsRelogin
                              ? account.statusReason || "令牌已失效，请先重新登录"
                              : formatAccountQuotaLine(account.quota)}
                        </small>
                      </>
                    )}
                  </div>
                </div>

                {!isRenaming ? (
                  <div className="ekds-acc-actions">
                    {!account.current ? (
                      needsRelogin ? (
                        <button
                          type="button"
                          className="ekds-btn is-primary"
                          disabled={!vaultUnlocked || busyAction !== null}
                          onClick={() => void handleBeginAdd(account.id)}
                        >
                          重新登录
                        </button>
                      ) : (
                        <button
                          type="button"
                          className="ekds-btn is-primary"
                          disabled={!vaultUnlocked || busyAction !== null}
                          onClick={() => void handleOpenSwitch(account)}
                        >
                          {busyAction === `preflight:${account.id}` ? "检查中…" : "切换"}
                        </button>
                      )
                    ) : null}
                    <button
                      type="button"
                      className="ekds-btn is-ghost"
                      disabled={busyAction !== null}
                      onClick={() => {
                        setRenamingId(account.id);
                        setRenameText(account.label);
                        setConfirmDeleteId(null);
                      }}
                    >
                      重命名
                    </button>
                    {isDeleting ? (
                      <>
                        <button
                          type="button"
                          className="ekds-btn is-danger"
                          disabled={!vaultUnlocked || busyAction !== null}
                          onClick={() => void handleDelete(account.id)}
                        >
                          确认删除
                        </button>
                        <button type="button" className="ekds-btn" onClick={() => setConfirmDeleteId(null)}>
                          取消
                        </button>
                      </>
                    ) : (
                      <button
                        type="button"
                        className="ekds-btn is-ghost"
                        disabled={!vaultUnlocked || busyAction !== null}
                        onClick={() => setConfirmDeleteId(account.id)}
                      >
                        删除
                      </button>
                    )}
                  </div>
                ) : null}
              </div>
            );
          })
        )}
      </div>

      {preflight ? (
        <AccountSwitchConfirmPanel
          preflight={preflight}
          busy={busyAction !== null && busyAction.startsWith("switch:")}
          busyKind={busyAction === "switch:force" ? "force" : busyAction === "switch:wait" ? "wait" : busyAction === "switch:confirm" ? "confirm" : null}
          error={switchError}
          onConfirm={() => void handleConfirmSwitch(false)}
          onForceConfirm={() => void handleConfirmSwitch(false, true)}
          onWaitUntilIdle={() => void handleConfirmSwitch(true)}
          onRelogin={() => void handleBeginAdd(preflight.target.id)}
          onCancel={() => {
            setPreflight(null);
            setSwitchError(null);
          }}
        />
      ) : null}

      <div className="ekds-acc-toolbar">
        {state?.signedIn ? (
          <button
            type="button"
            className="ekds-btn"
            disabled={!vaultUnlocked || busyAction !== null}
            onClick={() => void handleSaveCurrent()}
          >
            {busyAction === "save-current" ? "保存中…" : currentAlreadySaved ? "更新当前账号" : "保存当前账号"}
          </button>
        ) : null}
        <button
          type="button"
          className="ekds-btn"
          disabled={!vaultUnlocked || busyAction !== null}
          onClick={() => void handleBeginAdd()}
        >
          添加账号
        </button>
        {state?.cliRunning ? (
          <span className="ekds-note">检测到本机正在运行 kimi 命令行，切换账号会同步生效。</span>
        ) : null}
      </div>
    </div>
  );
}
