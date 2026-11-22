import { useState } from "react";
import { features } from "../features";
import type { PluginRecord } from "../api";
import { ActionButton, ErrorNotice, Field, InfoNote, Panel } from "./ui";
import { errorMessage, errorRecovery, useAction, useResource } from "../lib/useResource";
import { credentialState, declaredCredentialNames, deleteCredential, grantedCredentialNames } from "../lib/pluginCredentials";

function CredentialRow({
  plugin,
  name,
  stored,
  unlocked,
  onChanged,
}: {
  plugin: PluginRecord;
  name: string;
  stored: boolean;
  unlocked: boolean;
  onChanged: () => void;
}) {
  const action = useAction<unknown>();
  const [value, setValue] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const state = credentialState(stored, unlocked);
  const inputId = `plugin-credential-${plugin.id}-${name}`.replace(/[^A-Za-z0-9_-]/g, "-");

  async function save() {
    // The plaintext lives only in this controlled input. It goes to the vault and
    // is cleared immediately; no result, activity row or error echoes it back.
    const done = await action.run(() => features.saveSecret(name, value));
    if (done) {
      setValue("");
      onChanged();
    }
  }

  async function remove() {
    await deleteCredential(action.run, () => features.deleteSecret(name), () => {
      setConfirmDelete(false);
      onChanged();
    });
  }

  return (
    <li className="plugin-credential">
      <div className="plugin-credential-head">
        <code>{name}</code>
        <span className={`plugin-credential-state is-${state.tone}`}>{state.label}</span>
      </div>
      <p className="plugin-credential-detail">{state.detail}</p>
      <form
        className="plugin-credential-form"
        onSubmit={event => {
          event.preventDefault();
          void save();
        }}
      >
        <Field label={stored ? "替换值" : "凭据值"} htmlFor={inputId} hint="只保存在增强程序自己的加密库里，不会写回 Kimi Code 配置。">
          <input
            id={inputId}
            className="input"
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={value}
            disabled={action.busy}
            onChange={event => setValue(event.target.value)}
          />
        </Field>
        <div className="inline-actions">
          <ActionButton type="submit" icon={stored ? "refresh" : "plus"} busy={action.busy} disabled={!value || !unlocked}>
            {stored ? "替换" : "保存"}
          </ActionButton>
          {stored ? (
            confirmDelete ? (
              <>
                <ActionButton variant="danger" icon="check" onClick={() => void remove()} busy={action.busy}>
                  确认删除
                </ActionButton>
                <ActionButton variant="quiet" onClick={() => setConfirmDelete(false)}>
                  取消
                </ActionButton>
              </>
            ) : (
              <ActionButton variant="quiet" icon="trash" onClick={() => setConfirmDelete(true)} disabled={action.busy || !unlocked}>
                删除
              </ActionButton>
            )
          ) : null}
        </div>
      </form>
      {!unlocked ? <p className="plugin-credential-detail">解锁凭据库后才能写入或删除。</p> : null}
      {action.error ? (
        <ErrorNotice message={errorMessage(action.error)} recovery={errorRecovery(action.error)} onDismiss={action.clearError} />
      ) : null}
    </li>
  );
}

/**
 * The whole credential surface of one plugin. The unlocked state and the list of
 * stored references come from the core; the page never receives a plaintext
 * value, only whether one exists.
 */
export function PluginCredentials({ plugin }: { plugin: PluginRecord }) {
  const names = declaredCredentialNames(plugin);
  const granted = grantedCredentialNames(plugin);
  const vault = useResource("vault", features.vault);
  const action = useAction<unknown>();
  const resetAction = useAction<unknown>();
  const [password, setPassword] = useState("");
  const [confirmReset, setConfirmReset] = useState(false);

  if (names.length === 0) return null;

  const unlocked = Boolean(vault.data?.unlocked);
  const osKeychain = vault.data?.osKeychain === true;
  const osKeychainAvailable = vault.data?.osKeychainAvailable === true;
  const legacyPasswordVault = vault.data?.legacyPasswordVault === true;
  const refs = new Set(vault.data?.refs ?? []);
  const ungranted = names.filter(name => !granted.has(name));

  async function unlock() {
    const done = await action.run(() => features.unlock(password));
    if (done) {
      setPassword("");
      vault.reload();
    }
  }

  async function reset() {
    const done = await resetAction.run(() => features.resetVault());
    if (done) {
      setConfirmReset(false);
      setPassword("");
      vault.reload();
    }
  }

  return (
    <Panel title="凭据">
      <p className="panel-note">
        这是增强插件声明要用的凭据。明文只在核心，界面只显示是否已设置；添加、替换和删除都在这里完成。
      </p>
      {ungranted.length > 0 ? (
        <InfoNote tone="warn">
          <p>
            {ungranted.join("、")} 声明了但你没有授予凭据能力，增强插件无法使用。重新安装这个增强插件并勾选对应能力后才能生效。
          </p>
        </InfoNote>
      ) : null}
      {vault.error ? (
        <ErrorNotice message={errorMessage(vault.error)} recovery={errorRecovery(vault.error)} />
      ) : null}
      {unlocked && osKeychain ? (
        <InfoNote>
          <p>
            凭据由系统凭据库自动托管（macOS 钥匙串 / Windows DPAPI），无需再记密码；换机或清空系统钥匙串后才需要重新解锁。
          </p>
        </InfoNote>
      ) : null}
      {!unlocked && !vault.error ? (
        <form
          className="plugin-credential-unlock"
          onSubmit={event => {
            event.preventDefault();
            void unlock();
          }}
        >
          <Field
            label={legacyPasswordVault ? "原解锁口令" : "解锁口令"}
            htmlFor="plugin-credential-password"
            hint={
              legacyPasswordVault
                ? osKeychainAvailable
                  ? "输入一次原口令即可解锁，随后会转入系统凭据库自动托管，以后不再询问。"
                  : "这是旧版密码保护的凭据库。输入原口令解锁；忘记口令可重置。"
                : osKeychainAvailable
                  ? "首次设置需至少 12 个字符。设置后会存入系统凭据库，重启时自动解锁。"
                  : "首次设置需至少 12 个字符。口令只在内存中用于解密，重启后需要重新输入。"
            }
          >
            <input
              id="plugin-credential-password"
              className="input"
              type="password"
              autoComplete="current-password"
              value={password}
              disabled={action.busy || resetAction.busy}
              onChange={event => setPassword(event.target.value)}
            />
          </Field>
          <div className="inline-actions">
            <ActionButton type="submit" icon="lock" busy={action.busy} disabled={password.length < 12 || resetAction.busy}>
              {legacyPasswordVault ? (osKeychainAvailable ? "解锁并转为自动托管" : "解锁") : "解锁 / 初始化凭据库"}
            </ActionButton>
          </div>
          {action.error ? (
            <ErrorNotice message={errorMessage(action.error)} recovery={errorRecovery(action.error)} onDismiss={action.clearError} />
          ) : null}
        </form>
      ) : null}
      {!unlocked && !vault.error && (legacyPasswordVault || osKeychainAvailable) ? (
        confirmReset ? (
          <div className="plugin-credential-form" role="group" aria-label="重置凭据库确认">
            <p className="plugin-credential-detail">
              {osKeychainAvailable
                ? "重置会清空旧凭据库中已加密的全部凭据，并立即建立一个由系统凭据库托管、已解锁的新库。已保存的凭据值无法恢复，需要重新填写。"
                : "重置会清空旧凭据库中已加密的全部凭据，随后需要设置一个新的解锁口令。已保存的凭据值无法恢复，需要重新填写。"}
            </p>
            <div className="inline-actions">
              <ActionButton variant="danger" icon="check" busy={resetAction.busy} onClick={() => void reset()}>
                确认重置凭据库
              </ActionButton>
              <ActionButton variant="quiet" disabled={resetAction.busy} onClick={() => setConfirmReset(false)}>
                取消
              </ActionButton>
            </div>
            {resetAction.error ? (
              <ErrorNotice
                message={errorMessage(resetAction.error)}
                recovery={errorRecovery(resetAction.error)}
                onDismiss={resetAction.clearError}
              />
            ) : null}
          </div>
        ) : (
          <div className="inline-actions">
            <ActionButton variant="quiet" icon="refresh" disabled={action.busy || resetAction.busy} onClick={() => setConfirmReset(true)}>
              忘记旧密码？重置凭据库
            </ActionButton>
          </div>
        )
      ) : null}
      <ul className="plugin-credential-list">
        {names.map(name => (
          <CredentialRow
            key={name}
            plugin={plugin}
            name={name}
            stored={refs.has(name)}
            unlocked={unlocked}
            onChanged={() => vault.reload()}
          />
        ))}
      </ul>
    </Panel>
  );
}
