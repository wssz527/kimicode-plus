import { useState } from "react";
import { coreRequest } from "../api";
import { ActionButton, Field, Panel } from "./ui";
import { UsageImport } from "./UsageImport";
import { useAction } from "../lib/useResource";
import { PageError } from "../AppShell";
export function PortableTools({ instructions, monitor }: { instructions: boolean; monitor: boolean }) {
  const [goal, setGoal] = useState("");
  const [context, setContext] = useState("");
  const [constraints, setConstraints] = useState("");
  const render = useAction<{ text: string; sent: boolean }>();
  return <>
    {instructions ? <Panel title="任务指令整理"><form className="form-grid" onSubmit={e => { e.preventDefault(); void render.run(() => coreRequest("/plugins/kct/render", { method: "POST", body: { goal, context, constraints } })); }}><Field label="目标" htmlFor="instruction-goal"><textarea className="textarea" id="instruction-goal" value={goal} onChange={e => setGoal(e.target.value)} /></Field><Field label="引用上下文" htmlFor="instruction-context"><textarea className="textarea" id="instruction-context" value={context} onChange={e => setContext(e.target.value)} /></Field><Field label="约束" htmlFor="instruction-constraints"><textarea className="textarea" id="instruction-constraints" value={constraints} onChange={e => setConstraints(e.target.value)} /></Field><ActionButton type="submit" busy={render.busy} disabled={!goal}>生成并脱敏预览</ActionButton></form>{render.result ? <pre className="code-block">{render.result.text}</pre> : null}<PageError error={render.error} /><p className="field-hint">可移植 Go 实现，未运行原版 KCT，不自动发送任务。</p></Panel> : null}
    {monitor ? <UsageImport monitor onImported={() => {}} /> : null}
  </>;
}
