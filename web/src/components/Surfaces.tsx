import { useCallback, useEffect, useState } from "react";
import type { DemoBootstrap, NativeHost } from "../native";
import { errorText } from "./workbench/pieces";

export function useNativeBootstrap(host: NativeHost) {
  const [boot, setBoot] = useState<DemoBootstrap | null>(null);
  const [error, setError] = useState("");
  const reload = useCallback(async () => {
    setError("");
    try {
      const active = host.activeSession();
      setBoot(await host.request<DemoBootstrap>("bootstrap", active ? { sourceSessionId: active } : {}));
    } catch (cause) {
      setError(errorText(cause));
    }
  }, [host]);
  useEffect(() => { void reload(); }, [reload]);
  return { boot, error, reload };
}
