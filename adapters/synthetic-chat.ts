import {
  type ChatAdapter,
  type CapabilityResult,
  type ChatDetectContext,
  type MessagePage,
  type MessageSelection,
  type DraftReceipt,
  type ReplyState,
  type AttachmentRef,
  type AttachmentObtainResult,
  type SiteAdapterDescriptor,
  type UntrustedConversation,
  type UntrustedMessage,
} from "./types.js";
import { sha256Hex } from "./canonical.js";

export const SYNTHETIC_SITE_VERSION = "fixture-2026-11-10";

export interface SyntheticMessage extends UntrustedMessage {
  sequence: number;
}

export interface SyntheticSiteFixture {
  id: string;
  name: string;
  origin: string;
  threadId: string;
  title: string;
  messages: SyntheticMessage[];
  visibleCount: number;
  replyText: string;
}

const FIXTURES: SyntheticSiteFixture[] = [
  {
    id: "kimi",
    name: "Kimi 官网",
    origin: "https://www.kimi.com",
    threadId: "synthetic-kimi-thread-01",
    title: "合成会话：接口字段讨论",
    visibleCount: 4,
    replyText: "合成网页回复：已收到消息。此文本来自本地夹具，不代表真实 Kimi 回复。",
    messages: [
      { id: "kimi-m1", role: "user", sequence: 1, text: "我们需要把模型能力的来源与时间分开记录。" },
      { id: "kimi-m2", role: "assistant", sequence: 2, text: "建议每个字段保存 value、source、fetchedAt，未知保持 UNKNOWN。" },
      { id: "kimi-m3", role: "user", sequence: 3, text: "如果用户手动设为未知，目录更新不应该覆盖它。" },
      { id: "kimi-m4", role: "assistant", sequence: 4, text: "手动覆盖优先级最高，回滚时按 revision 做冲突检查。" },
      { id: "kimi-m5", role: "user", sequence: 5, text: "任务交接需要把选中的消息原样带过去，并保留来源 URL。" },
      { id: "kimi-m6", role: "assistant", sequence: 6, text: "任务包应带 checkpoint 与 parentHash，续接要绑定同一个 session。" },
    ],
  },
  {
    id: "chatgpt",
    name: "ChatGPT",
    origin: "https://chatgpt.com",
    threadId: "synthetic-chatgpt-thread-01",
    title: "合成会话：构建失败排查",
    visibleCount: 4,
    replyText: "合成网页回复：这是本地确定的 Mock 文本，没有调用任何模型。",
    messages: [
      { id: "chatgpt-m1", role: "user", sequence: 1, text: "构建在 CI 上失败，本地通过，先不要改代码。" },
      { id: "chatgpt-m2", role: "assistant", sequence: 2, text: "先比较两边的 Node 与锁文件版本，再确认是否使用了缓存。" },
      { id: "chatgpt-m3", role: "user", sequence: 3, text: "锁文件版本一致，CI 使用 npm ci，本地是 npm install。" },
      { id: "chatgpt-m4", role: "assistant", sequence: 4, text: "npm ci 严格按锁安装，缺失依赖会直接失败，先看报错的第一行。" },
      { id: "chatgpt-m5", role: "user", sequence: 5, text: "报错是 peer dependency 冲突，需要把版本对齐。" },
    ],
  },
  {
    id: "gemini",
    name: "Gemini",
    origin: "https://gemini.google.com",
    threadId: "synthetic-gemini-thread-01",
    title: "合成会话：用量口径",
    visibleCount: 3,
    replyText: "合成网页回复：估算与订阅额度在本工作台中分开显示。",
    messages: [
      { id: "gemini-m1", role: "user", sequence: 1, text: "为什么订阅额度不能和 API 用量相加？" },
      { id: "gemini-m2", role: "assistant", sequence: 2, text: "两者计价主体不同，网页会员不映射为 API 额度。" },
      { id: "gemini-m3", role: "user", sequence: 3, text: "那估算价格需要标记来源和抓取时间。" },
      { id: "gemini-m4", role: "assistant", sequence: 4, text: "是的，缺失价格时保持 UNKNOWN，不用 0 代替。" },
    ],
  },
  {
    id: "deepseek",
    name: "DeepSeek",
    origin: "https://chat.deepseek.com",
    threadId: "synthetic-deepseek-thread-01",
    title: "合成会话：插件资源",
    visibleCount: 3,
    replyText: "合成网页回复：插件启停只影响模拟资源计数。",
    messages: [
      { id: "deepseek-m1", role: "user", sequence: 1, text: "插件启用失败时会不会影响任务提交？" },
      { id: "deepseek-m2", role: "assistant", sequence: 2, text: "插件与任务模块隔离，插件故障只标记自身状态。" },
      { id: "deepseek-m3", role: "user", sequence: 3, text: "安全模式下插件应该全部停用。" },
    ],
  },
];

export function syntheticSiteFixtures(): readonly SyntheticSiteFixture[] {
  return FIXTURES;
}

export function fixtureForOrigin(origin: string): SyntheticSiteFixture | undefined {
  const normalized = origin.replace(/\/$/, "").toLowerCase();
  return FIXTURES.find((fixture) => fixture.origin === normalized);
}

export function siteDescriptors(): SiteAdapterDescriptor[] {
  return FIXTURES.map((fixture) => ({
    id: fixture.id,
    name: fixture.name,
    origin: fixture.origin,
    status: "UNVERIFIED",
    capabilities: {
      readMessages: "UNVERIFIED",
      fillDraft: "UNVERIFIED",
      sendMessage: "UNVERIFIED",
      attachments: "UNVERIFIED",
    },
    note: `${fixture.origin} 的真实网站适配尚未验证；下方内容全部来自本地合成夹具。`,
  }));
}

export class SyntheticChatAdapter implements ChatAdapter {
  readonly site: string;
  readonly origin: string;
  readonly siteVersion = SYNTHETIC_SITE_VERSION;
  private readonly fixture: SyntheticSiteFixture;
  private draft: string | null = null;
  private replies = 0;

  constructor(fixture: SyntheticSiteFixture) {
    this.fixture = fixture;
    this.site = fixture.id;
    this.origin = fixture.origin;
  }

  detect(context: ChatDetectContext): CapabilityResult {
    const matches = context.origin.replace(/\/$/, "") === this.origin;
    return {
      state: matches ? "UNVERIFIED" : "UNAVAILABLE",
      detail: matches
        ? "本地合成夹具可读；真实站点 DOM 尚未验证，不会抓取网页。"
        : "来源与夹具不匹配，容量未验证。",
      synthetic: true,
      siteVersion: this.siteVersion,
      checkedAt: new Date().toISOString(),
    };
  }

  async enumerateMessages(): Promise<MessagePage> {
    return {
      messages: this.fixture.messages.slice(0, this.fixture.visibleCount),
      partial: this.fixture.visibleCount < this.fixture.messages.length,
      loaded: this.fixture.visibleCount,
      totalKnown: this.fixture.messages.length,
    };
  }

  async extract(selection: MessageSelection): Promise<UntrustedConversation> {
    const page = await this.enumerateMessages();
    const seen = new Set(page.messages.map((message) => message.id));
    const chosen =
      selection.mode === "ALL_VISIBLE"
        ? page.messages
        : page.messages.filter((message) => seen.has(message.id) && (selection.messageIds ?? []).includes(message.id));
    const partial = chosen.length !== this.fixture.messages.length;
    return {
      site: this.site,
      url: `${this.origin}/chat/${this.fixture.threadId}`,
      threadId: this.fixture.threadId,
      messages: chosen.map((message) => ({ ...message })),
      partial,
      trust: "UNTRUSTED_WEB",
      extractedAt: new Date().toISOString(),
    };
  }

  async prepareDraft(text: string, expectedDraftHash: string): Promise<DraftReceipt> {
    const guardMatched = expectedDraftHash === (await sha256Hex(normalizeDraft(this.draft ?? "")));
    const observed = await sha256Hex(normalizeDraft(text));
    if (!guardMatched) {
      return {
        ok: false,
        guardMatched: false,
        readBackMatched: false,
        expectedDraftHash,
        observedDraftHash: observed,
        readBack: this.draft ?? "",
        reason: "当前草稿与预期哈希不一致，已停止写入",
      };
    }
    this.draft = text;
    return {
      ok: true,
      guardMatched: true,
      readBackMatched: observed === (await sha256Hex(normalizeDraft(this.draft))),
      expectedDraftHash,
      observedDraftHash: observed,
      readBack: this.draft,
    };
  }

  async observeReply(): Promise<ReplyState> {
    this.replies += 1;
    return this.replies % 2 === 0 ? "UNKNOWN" : "COMPLETE";
  }

  async obtainAttachment(ref: AttachmentRef): Promise<AttachmentObtainResult> {
    return {
      status: "unavailable",
      name: ref.name,
      reason: "合成适配器不读取站点附件，也不会访问任何 URL",
    };
  }

  draftText(): string {
    return this.draft ?? "";
  }

  async dispose(): Promise<void> {
    this.draft = null;
  }
}

export function normalizeDraft(text: string): string {
  return text.replace(/\r\n/g, "\n").replace(/[ \t]+\n/g, "\n").trimEnd();
}

export function syntheticChatAdapters(): SyntheticChatAdapter[] {
  return FIXTURES.map((fixture) => new SyntheticChatAdapter(fixture));
}
