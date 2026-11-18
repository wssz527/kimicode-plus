/**
 * The credential rules the plugin page and its tests share. They are kept out of
 * the component so the decisions (which names exist, which state a name is in)
 * are testable without a DOM or a JSX transform.
 */

export interface CredentialPluginLike {
  declaredCapabilities?: string[];
  capabilities?: string[];
}

/**
 * Credential names a plugin declared as `credentials:<name>`. Only declared names
 * are offered: the core authorizes every write against the manifest, so a name
 * typed by hand could never be stored under this plugin's grant anyway.
 */
export function declaredCredentialNames(plugin: CredentialPluginLike): string[] {
  const declared = plugin.declaredCapabilities ?? plugin.capabilities ?? [];
  const names = declared
    .filter(name => name.startsWith("credentials:"))
    .map(name => name.slice("credentials:".length))
    .filter(name => name.length > 0);
  return [...new Set(names)].sort();
}

/** Credential names the user actually granted, as bare names. */
export function grantedCredentialNames(plugin: CredentialPluginLike): Set<string> {
  const granted = plugin.capabilities ?? [];
  return new Set(
    granted
      .filter(name => name.startsWith("credentials:"))
      .map(name => name.slice("credentials:".length)),
  );
}

export interface CredentialState {
  label: string;
  detail: string;
  tone: "ok" | "warn" | "muted";
}

/**
 * A credential is usable only when a value is stored and the vault is unlocked.
 * A stored-but-locked entry is reported as its own state: calling it "未设置"
 * would push the user to save a second copy of a secret that is already there.
 */
export function credentialState(stored: boolean, unlocked: boolean): CredentialState {
  if (stored && unlocked) return { label: "已设置", detail: "出站请求时由核心注入。", tone: "ok" };
  if (stored) return { label: "已设置，凭据库锁定", detail: "解锁后才会注入。", tone: "warn" };
  return { label: "未设置", detail: "插件拿不到明文；使用前需要在这里保存。", tone: "muted" };
}

/**
 * Runs a credential deletion through useAction without relying on the response
 * body being truthy: DELETE /credentials/{ref} answers HTTP 204 (undefined).
 */
export async function deleteCredential(
  run: (task: () => Promise<boolean>) => Promise<unknown>,
  deleteSecret: () => Promise<unknown>,
  onDeleted: () => void,
): Promise<boolean> {
  const done = await run(async () => {
    await deleteSecret();
    return true;
  });
  if (done !== true) return false;
  onDeleted();
  return true;
}
