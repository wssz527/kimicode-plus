import { useState, type FormEvent } from "react";
import { useAuth } from "./auth";
import { Icon } from "./components/Icon";
import { Spinner } from "./components/ui";

export function PairingScreen() {
  const { pairWithToken, pairingError, phase } = useAuth();
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const busy = phase === "pairing" || submitting;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!token.trim() || busy) return;
    setSubmitting(true);
    await pairWithToken(token);
    setSubmitting(false);
  }

  return (
    <main className="pairing">
      <div className="pairing-frame">
        <div className="pairing-ledger" aria-hidden="true">
          <div className="ledger-head">
            <span>本地核心</span>
            <span className="num">127.0.0.1</span>
          </div>
          <ol>
            <li>
              <span>启动增强程序</span>
              <span className="num">01</span>
            </li>
            <li>
              <span>读取运行目录配对凭据</span>
              <span className="num">02</span>
            </li>
            <li>
              <span>在此页面配对一次</span>
              <span className="num">03</span>
            </li>
          </ol>
          <p className="ledger-note">
            凭据只保存在当前页面内存中。刷新、关闭或断开后都需要重新配对；不写入
            localStorage，也不随网络请求之外的路径传输。
          </p>
          <div className="ledger-stamp">
            <Icon name="shield" size={16} />
            <span>loopback · 同源 · 无外部凭据</span>
          </div>
        </div>

        <div className="pairing-form">
          <div className="brand brand-large">
            <span className="brand-mark" aria-hidden="true">
              <span />
              <span />
              <span />
            </span>
            <div className="brand-copy">
              <strong>Enhanced Kimi</strong>
              <span>独立增强工作台</span>
            </div>
          </div>
          <h1>连接本地核心</h1>
          <p className="pairing-intro">
            从增强程序运行目录的 <code>runtime.json</code> 读取 <code>pairingToken</code>，或使用启动时输出的
            <code> #pair= </code>地址打开本页。配对成功后，接入的只是本地增强服务，不涉及官方账号或 Cookie。
          </p>
          <form onSubmit={submit} className="pairing-form-fields">
            <label htmlFor="pair-token" className="field-label">
              配对凭据
            </label>
            <input
              id="pair-token"
              name="pairingToken"
              type="password"
              autoComplete="off"
              spellCheck={false}
              className="input input-code"
              placeholder="粘贴 64 位十六进制配对凭据"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              aria-describedby="pair-help"
              autoFocus
            />
            <p id="pair-help" className="field-hint">
              凭据由核心在启动时随机生成；不是 Kimi 账号密码。
            </p>
            {pairingError ? (
              <p className="pairing-error" role="alert">
                <Icon name="alert" size={16} />
                {pairingError}
              </p>
            ) : null}
            <button type="submit" className="btn btn-primary btn-full" disabled={busy || !token.trim()}>
              {busy ? <Spinner size={15} /> : <Icon name="lock" size={16} />}
              {busy ? "正在配对…" : "配对并进入工作台"}
            </button>
          </form>
          <div className="pairing-foot">
            <span className="trust-line">
              <Icon name="info" size={15} />
              如果地址带 <code>#pair=</code>，本页会立即使用并清除地址栏中的凭据片段。
            </span>
          </div>
        </div>
      </div>
    </main>
  );
}
