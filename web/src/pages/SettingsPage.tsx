import { useEffect, useMemo, useRef, useState } from "react";
import { api, type Settings } from "../api";
import { useAuth } from "../auth";
import { Icon } from "../components/Icon";
import { StandaloneWindowsAttachStatus } from "../components/WindowsAttachStatus";
import {
  ActionButton,
  Badge,
  ErrorNotice,
  Field,
  InfoNote,
  PageHeader,
  Panel,
  SkeletonRows,
  Toggle,
} from "../components/ui";
import { PageError } from "../AppShell";
import { useAction, useResource, errorMessage, errorRecovery } from "../lib/useResource";
import { fmtDateTime } from "../lib/format";

const MODULE_LABELS: Record<string, { name: string; detail: string }> = {
  registry: { name: "模型目录", detail: "供应商、模型能力与回滚快照" },
  usage: { name: "用量账本", detail: "请求事件、汇总与配额快照" },
  bridge: { name: "任务桥", detail: "预览、Outbox 与回执核对" },
  plugins: { name: "插件宿主", detail: "可移植处理组件与故障隔离" },
};

export function SettingsPage() {
  const { health, refreshHealth } = useAuth();
  const settingsResource = useResource("settings", () => api.settings.get());
  const [draft, setDraft] = useState<Settings | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const save = useAction<Settings>();
  const [flashed, setFlashed] = useState<string | null>(null);

  useEffect(() => {
    if (settingsResource.data) setDraft({ ...settingsResource.data, modules: { ...settingsResource.data.modules } });
  }, [settingsResource.data]);

  const savedRevision = useRef<number | null>(null);

  useEffect(() => {
    const result = save.result;
    if (!result || savedRevision.current === result.revision) return;
    savedRevision.current = result.revision;
    setDraft({ ...result, modules: { ...result.modules } });
    setSavedAt(new Date().toISOString());
    setFlashed("设置已保存");
    refreshHealth();
  }, [save.result, refreshHealth]);

  useEffect(() => {
    if (!flashed) return;
    const timer = setTimeout(() => setFlashed(null), 3200);
    return () => clearTimeout(timer);
  }, [flashed]);

  const dirty = useMemo(() => {
    if (!draft || !settingsResource.data) return false;
    return JSON.stringify(draft) !== JSON.stringify(settingsResource.data);
  }, [draft, settingsResource.data]);

  if (settingsResource.loading && !draft) {
    return (
      <div className="page">
        <PageHeader title="设置" description="读取核心设置、修订号与安全模式。" />
        <Panel title="设置">
          <SkeletonRows rows={5} cols={2} />
        </Panel>
      </div>
    );
  }

  if (settingsResource.error) {
    return (
      <div className="page">
        <PageHeader title="设置" description="读取核心设置、修订号与安全模式。" />
        <PageError error={settingsResource.error} />
        <ActionButton icon="refresh" onClick={settingsResource.reload}>
          重试读取
        </ActionButton>
      </div>
    );
  }

  if (!draft) return null;

  const modules = draft.modules ?? {};

  function patch(next: Partial<Settings>) {
    setDraft((current) => (current ? { ...current, ...next } : current));
  }

  async function submit() {
    if (!draft) return;
    await save.run(() => api.settings.put(draft));
  }

  return (
    <div className="page">
      <PageHeader
        title="设置"
        description="安全模式和模块开关会改变核心的实际行为。保存时携带 revision，若其他地方已更新会返回冲突，而不是静默覆盖。"
        meta={
          <>
            <span className="num">revision {draft.revision}</span>
            <span>压缩空闲阈值 {draft.compactionIdleMinutes} 分钟</span>
            {savedAt ? <span>上次保存 {fmtDateTime(savedAt)}</span> : null}
          </>
        }
        actions={
          <>
            <ActionButton
              variant="primary"
              icon="check"
              onClick={submit}
              disabled={!dirty}
              busy={save.busy}
            >
              保存设置
            </ActionButton>
            <ActionButton
              icon="refresh"
              onClick={() => {
                save.reset();
                settingsResource.reload();
              }}
              disabled={save.busy}
            >
              丢弃未保存修改
            </ActionButton>
          </>
        }
      />

      {flashed ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          {flashed}，核心 revision 已更新。
        </div>
      ) : null}

      {save.error ? (
        <ErrorNotice
          message={errorMessage(save.error)}
          recovery={errorRecovery(save.error)}
          onDismiss={save.clearError}
        />
      ) : null}

      {!dirty && !save.result ? (
        <p className="dirty-hint">
          <Icon name="info" size={14} />
          当前表单与核心记录一致。
        </p>
      ) : null}

      <div className="settings-grid">
        <div className="settings-main">
          <StandaloneWindowsAttachStatus />
          <Panel title="安全模式" aside={<Badge tone={draft.safeMode ? "warn" : "success"}>{draft.safeMode ? "已开启" : "已关闭"}</Badge>}>
            <Toggle
              checked={draft.safeMode}
              onChange={(next) => patch({ safeMode: next })}
              label="开启安全模式"
              hint="禁用任务分发与插件启用；历史、读取和聊天仍可用。"
            />
            {draft.safeMode ? (
              <InfoNote tone="warn">
                <p>
                  安全模式下，任务提交会被核心拒绝，插件启用也会被阻止。已存在的 Outbox 记录保持不变；需要核对
                  UNKNOWN 记录时先关闭安全模式。
                </p>
              </InfoNote>
            ) : (
              <p className="panel-note">
                当前允许 Mock 分发与插件启用。分发目标仍然是合成适配器，不会连接官方客户端。
              </p>
            )}
          </Panel>

          <Panel title="模块">
            <div className="module-switches">
              {Object.keys(MODULE_LABELS).map((key) => {
                const meta = MODULE_LABELS[key]!;
                const value = modules[key] ?? false;
                return (
                  <div className="module-switch" key={key}>
                    <Toggle
                      checked={value}
                      onChange={(next) => patch({ modules: { ...modules, [key]: next } })}
                      label={meta.name}
                      hint={meta.detail}
                    />
                    <span className="module-switch-state num">{value ? "ON" : "OFF"}</span>
                  </div>
                );
              })}
            </div>
            <InfoNote>
              <p>
                模块设置改变后需要重启增强程序才会重新初始化模块。关闭不会删除已有数据；重新启用时按现有 SQLite
                内容恢复。
              </p>
            </InfoNote>
          </Panel>

          <Panel title="默认聊天来源">
            <ChatSourceField
              value={draft.defaultChatSource}
              onChange={(value) => patch({ defaultChatSource: value })}
            />
            <Field
              label="压缩空闲分钟数"
              htmlFor="compaction-minutes"
              hint="核心保留 25 分钟策略；本阶段真实压缩默认禁用，此值只记录偏好，不会触发自动压缩。"
            >
              <input
                id="compaction-minutes"
                className="input num"
                type="number"
                min={1}
                max={1440}
                step={1}
                value={draft.compactionIdleMinutes}
                onChange={(event) => {
                  const parsed = Number(event.target.value);
                  if (Number.isFinite(parsed)) patch({ compactionIdleMinutes: Math.trunc(parsed) });
                }}
              />
            </Field>
            {draft.compactionIdleMinutes !== 25 ? (
              <InfoNote tone="warn">
                <p>
                  非 25 分钟的阈值会被核心保存，但自动压缩仍处于禁用状态。该字段当前只影响显示和未来策略；
                  本轮没有可验证的真实压缩执行。
                </p>
              </InfoNote>
            ) : null}
          </Panel>
        </div>

        <aside className="settings-rail">
          <Panel title="核心记录">
            <dl className="rail-facts">
              <div>
                <dt>revision</dt>
                <dd className="num">{draft.revision}</dd>
              </div>
              <div>
                <dt>版本</dt>
                <dd className="num">{health?.version ?? "UNKNOWN"}</dd>
              </div>
              <div>
                <dt>适配器</dt>
                <dd>{health?.adapter?.mode ?? "UNKNOWN"}</dd>
              </div>
              <div>
                <dt>压缩</dt>
                <dd>{health?.compaction === "DISABLED_UNVERIFIED" ? "DISABLED_UNVERIFIED" : (health?.compaction ?? "UNKNOWN")}</dd>
              </div>
              <div>
                <dt>凭据</dt>
                <dd>{health?.credentials ?? "UNKNOWN"}</dd>
              </div>
            </dl>
          </Panel>

          <Panel title="本阶段限制">
            <ul className="limits-list">
              <li>会话页可以保存与评估空闲策略。原生压缩执行尚未连接。</li>
              <li>插件运行可信内置 Go 处理能力，不支持任意代码或外部下载。</li>
              <li>安全模式不会删除已有历史，也不改变任务回执。</li>
              <li>页面不读取官方客户端凭据、配置、Cookie 或会话文件。</li>
            </ul>
          </Panel>
        </aside>
      </div>
    </div>
  );
}

function ChatSourceField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const sources = useResource("settings/sources", () => api.chat.sources());
  const options = sources.data?.items ?? [];
  return (
    <Field
      label="默认来源"
      htmlFor="default-chat-source"
      hint="默认 Kimi 官网。自定义来源在聊天页添加，添加后才会出现在此列表。"
    >
      {sources.loading ? (
        <SkeletonRows rows={1} cols={1} />
      ) : (
        <select
          id="default-chat-source"
          className="select"
          value={value}
          onChange={(event) => onChange(event.target.value)}
        >
          {options.length === 0 ? <option value={value}>{value || "UNKNOWN"}</option> : null}
          {options.map((source) => (
            <option key={source.id} value={source.id}>
              {source.name} · {source.id}
            </option>
          ))}
        </select>
      )}
    </Field>
  );
}
