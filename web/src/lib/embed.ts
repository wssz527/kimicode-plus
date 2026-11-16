export type ColorScheme = "light" | "dark" | "system";

export function normalizeColorScheme(value: string | null | undefined): ColorScheme {
  return value === "light" || value === "dark" ? value : "system";
}

export function embedRootClassName(embed: boolean): string {
  return embed ? "ekd-root ekd-embedded" : "ekd-root";
}

export function resolveAssetBase(assetBaseURL: string | undefined): string | null {
  const value = assetBaseURL?.trim();
  if (!value) return null;
  return value.endsWith("/") ? value.slice(0, -1) : value;
}

/** 把样式表里以 / 开头的静态资源地址改写到给定基址，供跨源内联场景使用。 */
export function rewriteAssetURLs(css: string, assetBaseURL: string | undefined): string {
  const base = resolveAssetBase(assetBaseURL);
  if (!base) return css;
  return css.replace(/url\((['"]?)\/(?!\/)/g, (_match, quote: string) => `url(${quote}${base}/`);
}
