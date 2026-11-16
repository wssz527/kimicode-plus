import { ROUTES, navigate } from "../routes";
import { Icon } from "../components/Icon";
import { PageHeader, Panel } from "../components/ui";
import { useResource } from "../lib/useResource";
import { api } from "../api";
import { useHostContext } from "../host";
import { getDemoHost } from "../native";
export function HomePage() {
  const tasks = useResource("home/tasks", () => api.tasks.list(0, 5));
  const context = useHostContext();
  const native = getDemoHost();
  const routes = native ? ROUTES.filter(r => !["/chat", "/handoff", "/workspace", "/browser"].includes(r.path)) : ROUTES;
  return <div className="page"><PageHeader title="增强设置" description={context ? `继续 ${context.title || context.sessionId} 的工作。` : "管理模型、查看用量和组织任务。本地数据保存在增强核心中。"} />
    <div className="feature-list">{routes.filter(r => r.path !== "/").map(route => <button className="feature-row" key={route.path} onClick={() => navigate(route.path)}><Icon name={route.icon} size={19} /><span><strong>{route.label}</strong><small>{route.summary}</small></span><span aria-hidden="true">›</span></button>)}</div>
    <Panel title="最近交接">{tasks.data?.items.length ? <ul className="reference-list">{tasks.data.items.map(t => <li key={t.id}><span>{t.task.goal}</span><small>{t.state}{t.receipt?.synthetic ? " · 模拟回执" : ""}</small></li>)}</ul> : <p className="field-hint">还没有任务交接记录。</p>}</Panel>
  </div>;
}
