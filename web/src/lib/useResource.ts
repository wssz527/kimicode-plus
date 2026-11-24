import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, BRIDGE_RECONNECT_TEXT, isBridgeCredentialError } from "../api";

export interface Resource<T> {
  data: T | null;
  error: ApiError | Error | null;
  loading: boolean;
  reload: () => void;
  revision: number;
}

export function useResource<T>(
  key: string,
  load: () => Promise<T>,
  options: { enabled?: boolean } = {},
): Resource<T> {
  const enabled = options.enabled !== false;
  const loadRef = useRef(load);
  loadRef.current = load;
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | Error | null>(null);
  const [loading, setLoading] = useState(enabled);
  const [revision, setRevision] = useState(0);
  const [activeKey, setActiveKey] = useState(key);

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    setActiveKey(key);
    loadRef
      .current()
      .then((value) => {
        if (!cancelled) setData(value);
      })
      .catch((reason: unknown) => {
        if (!cancelled) setError(reason instanceof Error ? reason : new Error(String(reason)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [key, enabled, revision]);

  const stale = activeKey !== key;

  // A stable identity keeps consumers from re-running effects that depend on
  // `reload` (pollers, timers) on every render of this hook.
  const reload = useCallback(() => setRevision((value) => value + 1), []);

  return {
    data,
    error,
    loading: loading || stale,
    reload,
    revision,
  };
}

export interface ActionState<T> {
  busy: boolean;
  error: ApiError | Error | null;
  result: T | null;
  run: (task: () => Promise<T>) => Promise<T | null>;
  reset: () => void;
  clearError: () => void;
}

export function useAction<T = unknown>(): ActionState<T> {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | Error | null>(null);
  const [result, setResult] = useState<T | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  async function run(task: () => Promise<T>): Promise<T | null> {
    setBusy(true);
    setError(null);
    try {
      const value = await task();
      if (mounted.current) setResult(value);
      return value;
    } catch (reason) {
      if (mounted.current) setError(reason instanceof Error ? reason : new Error(String(reason)));
      return null;
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  return {
    busy,
    error,
    result,
    run,
    reset: () => {
      setError(null);
      setResult(null);
    },
    clearError: () => setError(null),
  };
}

export function errorMessage(error: unknown): string {
  if (isBridgeCredentialError(error)) return BRIDGE_RECONNECT_TEXT;
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return "操作未完成，请重试。";
}

export function errorRecovery(error: unknown): string | null {
  if (isBridgeCredentialError(error)) {
    return "这是本地增强核心与界面之间的通信凭据，不是官方 Kimi 账号凭据。等待几秒后重试；持续出现时在「状态与诊断」确认增强核心仍在运行。";
  }
  if (!(error instanceof ApiError)) return null;
  switch (error.code) {
    case "AUTH_REQUIRED":
    case "PERMISSION_DENIED":
      return "会话没有此操作权限，或已经过期。请断开后重新配对。";
    case "CONFIG_CONFLICT":
    case "REVISION_CONFLICT":
      return "数据已被其他操作更新。请重新读取最新版本后再提交。";
    case "NETWORK":
      return "核心服务没有响应。确认增强程序仍在运行，然后重试。";
    case "NOT_FOUND":
      return "当前核心版本尚未提供这个模块。其余页面仍可使用。";
    case "SAFE_MODE":
      return "安全模式正在阻止分发与插件启用。可在设置中关闭后再试。";
    case "CHECKPOINT_IN_USE":
    case "CHECKPOINT_BRANCH":
      return "该检查点已被占用或已存在后继。刷新 Outbox 后选择最新已交付记录续接。";
    case "PARENT_HASH_MISMATCH":
    case "PARENT_NOT_DELIVERED":
      return "父检查点未确认送达或哈希不符。只能续接最新已交付记录，不能跨分支。";
    case "IDEMPOTENCY_CONFLICT":
      return "相同意图键已用于不同内容。修改目标后需要新的任务，而不是重试旧记录。";
    case "CONFIRMATION_STALE":
      return "预览后任务内容发生变化。请重新预览并再次确认。";
    case "ADAPTER_DEGRADED":
      return "Mock 适配器当前降级，提交被拒绝；历史仍可读取。把适配器切回 AVAILABLE 后重试。";
    case "ATTACHMENT_UNVERIFIED":
      return "附件缺少可校验字节。请删除该附件或重新选择本地文件。";
    case "PLUGIN_NOT_ENABLED":
      return "只有已启用的插件才能触发故障。请先启用该插件。";
    default:
      return null;
  }
}
