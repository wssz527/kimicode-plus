export type HandoffDirection =
  | "CHAT_TO_CODE"
  | "CODE_TO_CHAT"
  | "CHAT_TO_CODE_CONTINUE";

export type HandoffMode = "PLAN" | "EXECUTE" | "TOWER";

export type MockBehavior = "OK" | "TIMEOUT" | "EMPTY" | "OCCUPIED" | "PANIC";

export type AttachmentStatus = "verified" | "unavailable" | "UNKNOWN" | string;

export interface HandoffAttachment {
  name: string;
  mime: string;
  size: number;
  sha256: string;
  dataBase64?: string;
  blobRef?: string;
  sourceUrl?: string;
  status: AttachmentStatus;
}

export interface HandoffMessage {
  id: string;
  text: string;
  role: string;
}

export interface HandoffSource {
  site: string;
  url: string;
  threadId: string;
  messageIds: string[];
}

export interface WebTarget {site: string; origin: string; tabId: string; threadId: string}

export interface TaskPackage {
  hostId?: string;
  webTarget?: WebTarget;
  version: 1;
  taskId: string;
  direction: HandoffDirection;
  sessionId: string;
  projectId: string;
  source: HandoffSource;
  goal: string;
  messages: HandoffMessage[];
  diff?: string;
  attachments: HandoffAttachment[];
  checkpoint: number;
  parentCheckpoint: number;
  parentHash?: string;
  linkedTaskId?: string;
  mode: HandoffMode;
}

export interface DeliveryReceipt {
  receiptId?: string;
  taskId?: string;
  sessionId?: string;
  checkpoint?: number;
  deliveredAt?: string;
  [key: string]: unknown;
}

export interface OutboxRecord {
  id: string;
  intent: string;
  state: string;
  task: TaskPackage;
  receipt?: DeliveryReceipt | null;
  findings?: string[];
  confirmationHash?: string;
  checkpointHash?: string;
  checkpointAdvanced?: boolean;
  note?: string;
  replayed?: boolean;
  createdAt?: string;
  updatedAt?: string;
  [key: string]: unknown;
}

export type CapabilityState =
  | "AVAILABLE"
  | "UNVERIFIED"
  | "UNAVAILABLE"
  | "UNKNOWN";

export interface CapabilityResult {
  state: CapabilityState;
  detail: string;
  synthetic: boolean;
  siteVersion?: string;
  checkedAt?: string;
}

export interface AdapterStatus {
  mode: "AVAILABLE" | "UNAVAILABLE" | "CHANGED" | string;
  capabilities?: Record<string, boolean>;
  detail?: string;
  synthetic?: boolean;
  [key: string]: unknown;
}

export interface SessionReference {
  id: string;
  title?: string;
  projectId?: string;
  state?: string;
  owner?: string;
  synthetic?: boolean;
  [key: string]: unknown;
}

export interface DesktopAdapter {
  readonly kind: string;
  status(): Promise<AdapterStatus>;
  sessions(): Promise<SessionReference[]>;
  submit(request: DesktopSubmitRequest): Promise<DesktopSubmitResult>;
  reconcile(reference: DesktopReconcileReference): Promise<DesktopReconcileResult>;
  dispose(): Promise<void>;
}

export interface DesktopSubmitRequest {
  intent: string;
  task: TaskPackage;
  confirmed: boolean;
  mockBehavior?: MockBehavior;
}

export interface DesktopSubmitResult {
  state: string;
  taskId: string;
  receipt: DeliveryReceipt | null;
  replayed: boolean;
  synthetic: true;
}

export interface DesktopReconcileReference {
  taskId: string;
  receiptId?: string;
}

export interface DesktopReconcileResult {
  state: string;
  receipt: DeliveryReceipt | null;
  checkedAt: string;
  synthetic: true;
}

export interface BrowserAdapter {
  readonly mode: "EXTERNAL_BROWSER";
  capability(origin: string): CapabilityResult;
  link(url: string): ExternalLink;
  dispose(): Promise<void>;
}

export interface ExternalLink {
  url: string;
  target: "_blank";
  rel: "noopener noreferrer";
  openedByAdapter: false;
}

export interface UntrustedMessage {
  id: string;
  role: string;
  text: string;
  sequence?: number;
}

export interface MessageSelection {
  mode: "SELECTED" | "ALL_VISIBLE";
  messageIds?: string[];
}

export interface MessagePage {
  messages: UntrustedMessage[];
  partial: boolean;
  loaded: number;
  totalKnown: number | null;
}

export interface UntrustedConversation {
  site: string;
  url: string;
  threadId: string;
  messages: UntrustedMessage[];
  partial: boolean;
  trust: "UNTRUSTED_WEB";
  extractedAt: string;
}

export interface DraftReceipt {
  ok: boolean;
  guardMatched: boolean;
  readBackMatched: boolean;
  expectedDraftHash: string;
  observedDraftHash: string;
  readBack: string;
  reason?: string;
}

export type ReplyState = "STREAMING" | "COMPLETE" | "UNKNOWN";

export interface AttachmentRef {
  id?: string;
  name?: string;
  sourceUrl?: string;
}

export interface AttachmentObtainResult {
  status: "verified" | "unavailable" | "UNKNOWN";
  name?: string;
  sha256?: string;
  size?: number;
  reason?: string;
}

export interface ChatAdapter {
  readonly site: string;
  readonly origin: string;
  readonly siteVersion: string;
  detect(context: ChatDetectContext): CapabilityResult;
  enumerateMessages(): Promise<MessagePage>;
  extract(selection: MessageSelection): Promise<UntrustedConversation>;
  prepareDraft(text: string, expectedDraftHash: string): Promise<DraftReceipt>;
  observeReply(): Promise<ReplyState>;
  obtainAttachment(ref: AttachmentRef): Promise<AttachmentObtainResult>;
  dispose(): Promise<void>;
}

export interface ChatDetectContext {
  origin: string;
  document?: Document;
}

export interface GatewayBalance {
  balance: number | null;
  currency: string | null;
  source: "UNKNOWN" | "MOCK" | "MANUAL" | string;
  fetchedAt: string | null;
}

export interface GatewayAdapter {
  readonly id: string;
  readonly name: string;
  balance(): Promise<GatewayBalance>;
  dispose(): Promise<void>;
}

export interface CredentialDescriptor {
  ref: string;
  present: boolean | null;
  source: "MEMORY_MOCK_ONLY" | "UNKNOWN" | string;
  secretExposed: false;
  detail: string;
}

export interface CredentialProvider {
  describeSecretRef(ref: string): Promise<CredentialDescriptor>;
}

export interface SiteAdapterDescriptor {
  id: string;
  name: string;
  origin: string;
  status: CapabilityState;
  capabilities: {
    readMessages: CapabilityState;
    fillDraft: CapabilityState;
    sendMessage: CapabilityState;
    attachments: CapabilityState;
  };
  note: string;
}
