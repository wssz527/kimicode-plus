import { useState } from "react";
import { features } from "../features";
import { ActionButton, Field, PageHeader, Panel } from "../components/ui";
import { useAction, useResource } from "../lib/useResource";
import { PageError } from "../AppShell";
export function CredentialsPage() {
  const vault = useResource("vault", features.vault);
  const action = useAction<unknown>();
  const [password, setPassword] = useState("");
  const [ref, setRef] = useState("");
  const [value, setValue] = useState("");
  return <div className="page"><PageHeader title="凭据库" description="密钥保存在增强程序自己的加密库中。首次设置解锁口令，重启后需要重新解锁；请妥善保管口令。" />
    <Panel title={vault.data?.unlocked ? "已解锁" : "已锁定"}>
      {!vault.data?.unlocked ? <form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { await features.unlock(password); setPassword(""); vault.reload(); }); }}><Field label="解锁口令（首次至少 12 字节）" htmlFor="vault-password"><input id="vault-password" type="password" autoComplete="off" className="input" value={password} onChange={e => setPassword(e.target.value)} /></Field><div className="form-actions"><ActionButton type="submit" busy={action.busy} disabled={password.length < 12}>解锁 / 初始化</ActionButton></div></form> : <>
        <form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { await features.saveSecret(ref, value); setValue(""); vault.reload(); }); }}>
          <Field label="凭据引用" htmlFor="secret-ref" hint="在供应商的 secretRef 中填写同一引用，例如 provider/team。"><input id="secret-ref" className="input" value={ref} onChange={e => setRef(e.target.value)} /></Field>
          <Field label="API 密钥" htmlFor="secret-value"><input id="secret-value" type="password" autoComplete="off" className="input" value={value} onChange={e => setValue(e.target.value)} /></Field>
          <div className="form-actions"><ActionButton type="submit" busy={action.busy} disabled={!ref || !value}>加密保存</ActionButton><ActionButton onClick={() => { void action.run(async () => { await features.lock(); setValue(""); vault.reload(); }); }}>锁定凭据库</ActionButton></div>
        </form>
      </>}
      <PageError error={action.error ?? vault.error} />
    </Panel>
    <Panel title="已保存的引用"><ul className="reference-list">{vault.data?.refs.map(item => <li key={item}><code>{item}</code><ActionButton disabled={!vault.data?.unlocked} onClick={() => { void action.run(async () => { await features.deleteSecret(item); vault.reload(); }); }}>删除</ActionButton></li>)}</ul><p className="field-hint">AES-256-GCM，PBKDF2-SHA256。接口不提供明文读取，默认不访问系统钥匙串。</p></Panel>
  </div>;
}
