import type { BrowserAdapter, CapabilityResult, ExternalLink } from "./types.js";

export const EXTERNAL_LINK_REL = "noopener noreferrer";

export function isHttpsUrl(raw: string): boolean {
  try {
    const url = new URL(raw);
    return url.protocol === "https:" && url.hostname.length > 0 && url.username === "" && url.password === "";
  } catch {
    return false;
  }
}

export class ExternalBrowserAdapter implements BrowserAdapter {
  readonly mode = "EXTERNAL_BROWSER" as const;

  capability(origin: string): CapabilityResult {
    const https = origin.startsWith("https://");
    return {
      state: https ? "UNVERIFIED" : "UNAVAILABLE",
      detail: https
        ? "外部网站适配尚未验证；本工作台只提供 HTTPS 跳转，不读取页面、不注入脚本。"
        : "只允许 HTTPS 来源；http 或自定义协议不会打开。",
      synthetic: false,
      checkedAt: new Date().toISOString(),
    };
  }

  link(url: string): ExternalLink {
    if (!isHttpsUrl(url)) throw new Error("只支持不含凭据的 HTTPS 网址");
    return {
      url,
      target: "_blank",
      rel: EXTERNAL_LINK_REL,
      openedByAdapter: false,
    };
  }

  async dispose(): Promise<void> {
    return;
  }
}

export function createExternalBrowserAdapter(): ExternalBrowserAdapter {
  return new ExternalBrowserAdapter();
}
