import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  api,
  ApiError,
  consumePairingFragment,
  type AuthSnapshot,
  type Health,
} from "./api";

export type AuthPhase = "booting" | "unpaired" | "pairing" | "paired";

interface AuthContextValue {
  phase: AuthPhase;
  snapshot: AuthSnapshot;
  health: Health | null;
  healthError: string | null;
  pairingError: string | null;
  pairWithToken: (token: string) => Promise<boolean>;
  disconnect: () => void;
  refreshHealth: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [phase, setPhase] = useState<AuthPhase>("booting");
  const [snapshot, setSnapshot] = useState<AuthSnapshot>(api.auth.snapshot());
  const [health, setHealth] = useState<Health | null>(null);
  const [healthError, setHealthError] = useState<string | null>(null);
  const [pairingError, setPairingError] = useState<string | null>(null);
  const [healthTick, setHealthTick] = useState(0);
  const booted = useRef(false);

  useEffect(() => {
    const unsubscribe = api.auth.subscribe(() => {
      const next = api.auth.snapshot();
      setSnapshot(next);
      if (next.status === "paired") setPhase("paired");
      else if (next.status === "pairing") setPhase("pairing");
      else setPhase("unpaired");
    });
    return unsubscribe;
  }, []);

  const pairWithToken = useCallback(async (token: string): Promise<boolean> => {
    setPairingError(null);
    try {
      await api.pair(token);
      setPhase("paired");
      return true;
    } catch (error) {
      setPairingError(error instanceof ApiError ? error.message : "配对失败，请检查凭据");
      setPhase("unpaired");
      return false;
    }
  }, []);

  useEffect(() => {
    if (booted.current) return;
    booted.current = true;
    const fragment = consumePairingFragment();
    if (fragment) {
      void pairWithToken(fragment).then((ok) => {
        if (!ok) setPhase("unpaired");
      });
      return;
    }
    setPhase(api.auth.snapshot().status === "paired" ? "paired" : "unpaired");
  }, [pairWithToken]);

  useEffect(() => {
    if (phase !== "paired") {
      setHealth(null);
      setHealthError(null);
      return;
    }
    let cancelled = false;
    setHealthError(null);
    api
      .health()
      .then((value) => {
        if (!cancelled) setHealth(value);
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setHealth(null);
          setHealthError(error instanceof Error ? error.message : "无法读取核心状态");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [phase, healthTick]);

  const disconnect = useCallback(() => {
    api.auth.clear("已断开本地核心。配对凭据只保存在内存中，刷新页面同样需要重新配对。");
    setHealth(null);
    setPhase("unpaired");
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      phase,
      snapshot,
      health,
      healthError,
      pairingError,
      pairWithToken,
      disconnect,
      refreshHealth: () => setHealthTick((tick) => tick + 1),
    }),
    [phase, snapshot, health, healthError, pairingError, pairWithToken, disconnect],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) throw new Error("useAuth 必须在 AuthProvider 内使用");
  return context;
}
