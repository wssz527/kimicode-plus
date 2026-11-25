import { useState } from "react";
import type { NativeApproval, NativeApprovalDecision, NativeQuestion } from "../../native";
import { Icon } from "../Icon";
import { Spinner } from "../ui";

export type ApprovalOption = NonNullable<NativeApproval["options"]>[number];
type QuestionAnswer = { kind: "single"; option_id: string } | { kind: "multi"; option_ids: string[] }
  | { kind: "other"; text: string } | { kind: "multi_with_other"; option_ids: string[]; other_text: string };
type ApprovalReply = { decision: NativeApprovalDecision; selectedLabel?: string; feedback?: string };
export type InteractionReply = ApprovalReply | { answers: Record<string, QuestionAnswer> } | { dismissed: true };

const DEFAULT_APPROVAL_OPTIONS: ApprovalOption[] = [
  { label: "允许", value: "approved" },
  { label: "拒绝", value: "rejected" },
];

const APPROVAL_DECISIONS: Record<string, NativeApprovalDecision> = {
  approved: "approved", allow: "approved", rejected: "rejected", deny: "rejected", denied: "rejected",
  cancelled: "cancelled", cancel: "cancelled",
};

export function approvalOptions(options?: ApprovalOption[] | null): ApprovalOption[] {
  return options?.length ? options : DEFAULT_APPROVAL_OPTIONS;
}

export function approvalDecision(option: ApprovalOption, explicit?: NativeApprovalDecision): ApprovalReply | null {
  const value = option.value.trim();
  if (!value) return null;
  const normalized = Object.hasOwn(APPROVAL_DECISIONS, value) ? APPROVAL_DECISIONS[value] : undefined;
  const decision = normalized ?? explicit ?? option.decision;
  if (decision !== "approved" && decision !== "rejected" && decision !== "cancelled") return null;
  return normalized ? { decision } : { decision, selectedLabel: value };
}

type QuestionInput = { selected: string[]; other: boolean; text: string };
const EMPTY_QUESTION: QuestionInput = { selected: [], other: false, text: "" };

export function questionAnswer(question: NativeQuestion, input: QuestionInput): QuestionAnswer | null {
  const selected = input.selected;
  const text = input.text.trim();
  if (input.other && !text) return null;
  if (input.other && selected.length && question.multi_select) return { kind: "multi_with_other", option_ids: selected, other_text: text };
  if (input.other) return { kind: "other", text };
  if (!selected.length) return null;
  return question.multi_select ? { kind: "multi", option_ids: selected } : { kind: "single", option_id: selected[0]! };
}

function Questions({ questions, busy, onReply }: { questions: NativeQuestion[]; busy: boolean; onReply: (reply: InteractionReply) => void }) {
  const [inputs, setInputs] = useState<Record<string, QuestionInput>>({});
  const completed = questions.map(question => [question.id, questionAnswer(question, inputs[question.id] ?? EMPTY_QUESTION)] as const);
  const answers = Object.fromEntries(completed.filter((entry): entry is readonly [string, QuestionAnswer] => entry[1] !== null));
  return <form onSubmit={event => {
    event.preventDefault();
    if (completed.every(([, answer]) => answer !== null)) onReply({ answers });
  }}>
    {questions.map(question => {
      const input = inputs[question.id] ?? EMPTY_QUESTION;
      const update = (value: QuestionInput) => setInputs(current => ({ ...current, [question.id]: value }));
      const type = question.multi_select ? "checkbox" : "radio";
      const options = question.options ?? [];
      return <fieldset key={question.id} disabled={busy} className="wb-question">
        <legend>{question.question || question.header}</legend>
        {question.body ? <p>{question.body}</p> : null}
        {options.map(option => <label key={option.id}><input type={type} name={question.id} checked={input.selected.includes(option.id)} onChange={event => {
          const selected = !question.multi_select ? [option.id] : event.target.checked ? [...input.selected, option.id] : input.selected.filter(id => id !== option.id);
          update({ ...input, selected, other: Boolean(question.multi_select && input.other) });
        }} />{option.label}</label>)}
        {question.allow_other || !options.length ? <>
          <label><input type={type} name={question.id} checked={input.other} onChange={event => update({ ...input, selected: question.multi_select ? input.selected : [], other: event.target.checked })} />{question.other_label || "其他答案"}</label>
          {input.other || !options.length ? <textarea aria-label={question.question || question.header} rows={2} value={input.text}
            onChange={event => update({ ...input, other: true, text: event.target.value })} /> : null}
        </> : null}
      </fieldset>;
    })}
    <div className="wb-approval-actions">
      <button type="submit" className="wb-btn" disabled={busy || completed.some(([, answer]) => answer === null)}>提交回答</button>
      <button type="button" className="wb-btn is-quiet" disabled={busy} onClick={() => onReply({ dismissed: true })}>跳过提问</button>
    </div>
  </form>;
}

export function ApprovalCard({ approval, busy, active, onReply }: { approval: NativeApproval; busy: boolean; active: boolean; onReply: (reply: InteractionReply) => void }) {
  const [feedback, setFeedback] = useState("");
  const [selected, setSelected] = useState<ApprovalOption | null>(null);
  function submit(option: ApprovalOption, explicit?: NativeApprovalDecision) {
    const value = approvalDecision(option, explicit);
    if (!value) {
      setSelected(option);
      return;
    }
    onReply({ ...value, ...(feedback.trim() ? { feedback: feedback.trim() } : {}) });
  }
  return <div className="wb-approval">
    <div className="wb-approval-head"><Icon name="shield" size={14} /><span>{approval.title || "需要你的确认"}</span>{active ? <Spinner size={12} /> : null}</div>
    {approval.detail ? <pre className="wb-approval-detail">{approval.detail}</pre> : null}
    {approval.questions?.length ? <Questions questions={approval.questions} busy={busy} onReply={onReply} /> : <>
      <label className="wb-approval-feedback">反馈（可选）<textarea rows={2} value={feedback} disabled={busy} onChange={event => setFeedback(event.target.value)} /></label>
      <div className="wb-approval-actions">
        {approvalOptions(approval.options).map(option => <button key={option.value} type="button" className="wb-btn"
          aria-pressed={approvalDecision(option) ? undefined : selected?.value === option.value} disabled={busy}
          onClick={() => submit(option)}>{option.label}</button>)}
      </div>
      {selected && approval.options?.some(option => option.value === selected.value) ? <div className="wb-approval-selection">
        <p>确认如何提交「{selected.label}」</p>
        <div className="wb-approval-actions">
          <button type="button" className="wb-btn" disabled={busy} onClick={() => submit(selected, "approved")}>允许并提交</button>
          <button type="button" className="wb-btn" disabled={busy} onClick={() => submit(selected, "rejected")}>拒绝并提交</button>
          <button type="button" className="wb-btn is-quiet" disabled={busy} onClick={() => setSelected(null)}>返回</button>
        </div>
      </div> : null}
    </>}
  </div>;
}
