# API contract v1 — 0.2.0

Base `/enhance/v1`. JSON fields camelCase; enums UPPER_SNAKE. Errors `{error:{code,message}}`. Lists `{items:[],total}` use `offset` / `limit`. JSON bodies normally have a 2 MiB limit, saved models.dev catalogs 16 MiB. Unknown fields are rejected. Edits to existing versioned configuration require an explicit integer `revision`; omitted/null revisions are rejected. Provider creation uses a unique ID and atomically advances the shared revision.

## Authentication and local boundaries

`POST /pair {pairingToken,permissions?}` exchanges this enhancement service's boot credential for an in-memory session, expiring after 12 hours or restart. Browser `#pair=` is consumed and removed; tokens stay in memory. The private runtime file belongs only to this enhancement service. A boot credential also authorizes local CLI requests. Each API checks exact Host, same-origin Origin when present, cross-site marker, resource permission and request limits. A loopback address alone is not authorization. No account Cookie is used.

Permissions include `models:read/write`, `usage:read/write`, `tasks:read/write`, `chat:read/write`, `plugins:read/write`, `core:read`, `settings:write`, `adapter:write`, `credentials:read/write`, `workspace:read/write`, `browser:read/write`.

## Core

- `GET /health`: status, version, module health, safeMode and adapter status.
- `GET /settings`: revision, safeMode, compactionIdleMinutes (default 25), defaultChatSource (`kimi`), enabled modules.
- `PUT /settings`: complete settings plus expected revision. Module enable/disable takes effect on core restart. Independent admitted chat/task calls share a read permit and may run concurrently. Settings writes take the exclusive permit, drain admitted effects, and block new admission while waiting; after safe mode is saved, later calls are rejected. Worktree commands and browser migration applies use the same gate. It cannot revoke a sent request. Plugins are stopped; local configuration/history remain usable.

## Registry storage and gateway metadata

Provider instance IDs follow `^[a-z0-9][a-z0-9._-]{0,63}$`. Model IDs remain provider-facing strings. Base URLs allow HTTPS or HTTP with literal loopback / localhost; credentials, query, fragment, whitespace and invalid ports are rejected. Remote HTTP and shortened IPv4 forms such as `127.1` are rejected. `contracts/provider-policy.json` is shared by Go and TypeScript tests.

- Standalone `/providers`, `/provider-presets`, `/models`, `/models/*` and `/model-changes/*` HTTP routes were removed in the 2026-11-18 correction round (R2): provider and model capabilities, thinking modes, context/output limits and efforts are edited directly on the official client's provider form via `providers.updateModel`, while internal SQLite tables remain migrated for local gateway/chat/planner compatibility.
- `GET /gateways` retains read-only Mock gateway metadata interfaces for standalone diagnostics.

## Provider accounts and explicit credential import

- `GET/POST /providers/{id}/accounts`, `PUT/DELETE /providers/{id}/accounts/{accountId}` manage account slots with label, secretRef, enabled, priority, weight and revision. Updates require the current revision. Secret references are unique per account and provider deletion cascades. Selection is deterministic by priority then creation order; there is no multi-account scheduler or failover. A provider with no account rows keeps its legacy provider secretRef; existing but disabled accounts do not silently fall back.
- `GET /accounts/import-contract`, `POST /accounts/import/preview {path}`, and `POST /providers/{id}/accounts/import {path,label?,fingerprint,accountId?}` implement explicit read-only import. The caller must name an absolute regular file; symlinks, empty files and files over 64 KiB are rejected. This can include an official client file when the user explicitly names it, but there is no ambient discovery, directory scan, environment lookup or write-back.
- Preview returns field names, unknown field names, size, SHA-256 fingerprint, credential-presence flags and normalized expiry, never credential values. Official numeric `expires_at` Unix seconds and compatible RFC3339 strings become UTC RFC3339; invalid values remain absent. Apply rechecks the fingerprint and creates a disabled account in the encrypted vault. Credential insertion is create-only at the database uniqueness boundary: a duplicate account/reference returns 409 without overwriting or deleting the existing ciphertext. `refresh_token` is neither stored nor refreshed in this stage.

## Usage observations

`POST /usage {events:[...]}` accepts requestId/attemptId/sessionId/projectId/agentId, provider/model, protocol/source, at, nullable counts or raw protocol usage, optional price, observationId, `observationState` and `sequence`.

Lifecycle is PARTIAL, FINAL or explicit CORRECTION. Omitted lifecycle is inferred from output presence for compatibility; omitted sequence uses parsed observation time. One canonical request attempt is counted once. Same-source ordered partial completion can update it. Duplicated terminal observations do not add billing, old sequence cannot overwrite terminal state, and a changed terminal count requires CORRECTION. A conflicting higher-authority partial cannot erase known values. Every decision is retained in `usage_observations`; no per-field maximum merge is used. Missing attribution goes to unlinked records; retries use separate attempts. Overflow produces UNKNOWN, not wrapped integers.

- `GET /usage` lists canonical events, lifecycle, sequence, conflict status and provenance; supports project/session/agent/provider/model/role/requestKind/from/to filters.
- `GET /usage/observations`, `/usage/provenance`, `/usage/unlinked` expose evidence and unresolved inputs.
- `GET /usage/summary` separates reported Token counts, coverage, estimate, actual billing and quotas; unknown remains null.
- `POST /usage/actual`, `GET/POST /quotas` preserve separate ledgers. Actual billing is not silently attributed to a session.
- `POST /usage/demo {}` writes deterministic synthetic `session-demo` / `project-demo` and `demo-` identities. Source labels simulate trust levels, not real upstream events.
- `POST /usage/demo/clear {}` removes only the fixed demo event/observation identities and exact Mock quota snapshots in one transaction. Requests with additional observations are preserved. Shared fixture price metadata is retained, explicitly marked fixture, and is not an independent charge.

## Attachments and task bridge

`POST /attachments` accepts an authenticated `application/octet-stream` body (1 MiB per file), headers `X-Attachment-SHA256` and `X-Attachment-Type`, and returns `{blobRef,sha256,size,mime,status:"STORED"}`. The private SQLite store caps blobs at 64 MiB. Zero bytes are explicitly rejected. UI permits up to eight files / 8 MiB and uploads each separately.

TaskPackage v1 contains taskId, direction, sessionId, projectId, optional hostId, selected original source/message IDs, goal, messages, diff, attachment metadata, checkpoint/parentCheckpoint/parentHash, optional linkedTaskId and mode. Directions are CHAT_TO_CODE, CODE_TO_CHAT, CHAT_TO_CODE_CONTINUE. Code-to-Chat also requires exact `webTarget {site,origin,tabId,threadId}` from `GET /web-targets`. Sessions are independent synthetic references from `GET /sessions`; the core does not own official sessions.

- `POST /tasks/preview {task}` stores provided attachment bytes, returns only references/metadata and redacted task/findings plus confirmationHash. Legacy dataBase64 input is accepted, but stripped from output. It does not transmit.
- `POST /tasks {intent,task,confirmed:true,confirmationHash,mockBehavior?}` atomically claims Outbox identity and attachment references. Attachment dispatch requires the preview hash. Only the approved task's blob references are available to its transport reader.
- Effective Session project/host and adapter host must match; Web direction independently matches site/origin/tab/thread. Code and Web use separate transports and receipt markers MOCK_CODE / MOCK_WEB.
- A valid DELIVERED receipt must match task, session, digest, checkpoint and every attachment ref/hash/size. Attachments become UPLOADED only in that target receipt; STORED is not delivery. The current targets are Mock. A local HTTP receiver test verifies actual byte hashes across database restart, without claiming native upload support.
- `GET /tasks`, `POST /tasks/{id}/reconcile {}` query/check receipt only. Timeout, empty receipt or interrupted dispatch stays UNKNOWN; reconciliation never resubmits. Alternate intent cannot bypass an occupied checkpoint.
- `GET /tasks/{id}/attachments/{blob}` downloads only blobs associated with that authorized task. `POST /attachments/collect` removes unreferenced blobs older than 24 hours. Referenced history is retained. REJECTED tasks release their reference claims.

Text redaction does not modify or certify attachment bytes. Preview warns that scanning is bounded (at most 256 KiB) and attachments may contain secrets. No arbitrary file path read or attachment URL fetch is performed.

## Adapter, plugins and chat

`GET/PUT /adapter` controls independent Mock availability only.

## Plugins

Plugins are local trusted packages (manifest + ES module) hosted by the core. `GET /plugins` lists installs with state, granted capabilities, slots and the compatibility verdict; `GET /plugins/health` summarizes them; `GET /plugins/{id}` reads one.

To install a local folder or ZIP, call `POST /plugins/preview {root}` and show its manifest and capabilities for approval. Pass the preview's `packageDigest` to `POST /plugins/install {root, packageDigest, capabilities, confirmPrivileged?}`. Omitting it returns `400 PLUGIN_PACKAGE_DIGEST_REQUIRED`. If package contents change between preview and installation, the core returns `409 PLUGIN_PACKAGE_CHANGED`. Preview again and obtain fresh approval.

Send an explicit `capabilities` list, including `[]` when granting no capabilities. Omitting it returns `400 PLUGIN_CONSENT_REQUIRED`. The list can only contain capabilities declared by the manifest. Granting a v2 privileged capability requires `confirmPrivileged: true`. The core validates and compares the staged package before replacing an existing install. New installs stay disabled. A ZIP can contain up to 64 entries, 4 MiB per file and 16 MiB total. The core refuses absolute paths, parent traversal and symbolic links.

`POST /plugins/{id}/enable` returns the plugin state plus a per-renderer identity token. `POST /plugins/{id}/disable` stops the plugin and revokes that token in the same step. `POST /plugins/{id}/uninstall` deletes files and private storage and leaves a tombstone, so a bundled plugin the user removed is never released again automatically. `GET /plugins/{id}/activity` returns the redacted activity log.

On Windows, plugin feature availability depends on the host connection. The verified Electron main-process bridge requests `host:electron-main` during pairing. List, preview, and detail responses reflect the paired session's capabilities. A renderer-only session receives the platform limitations.

Plugin actions run at `POST /plugin-runtime/{action}` with body `{token, params}`. The token alone decides which plugin is acting, so neither the URL nor the params can address another plugin; an invalid or revoked token answers `401 PLUGIN_IDENTITY_REJECTED`. Each action is authorized by the capability table before any work happens, and an action absent from that table is refused rather than forwarded, answering `403 PLUGIN_CAPABILITY_DENIED`.

Capabilities: `metrics:read`, `sessions:read`, `sessions:send`, `config:read`, `config:write`, `ui:sidebar`, `ui:settings`, `ui:thinking`, `ui:message`, `storage`, `net:<host>` and `credentials:<name>`. A `net:*.example.com` grant covers one label of subdomain and never the bare domain. Network requests are proxied by Go with only http/https, no embedded URL credentials, no redirect following, an 8 MiB response cap and `Set-Cookie` stripped. A credential is resolved by the core and injected into the outbound header; the plaintext never reaches the renderer, an activity row or an error message, and a locked vault is reported as such instead of yielding an empty value.

`GET /plugins/{id}/assets/{file}` serves an enabled plugin's own files for `import()`, restricted to that plugin's directory. `POST /plugins/{id}/token` re-issues the same identity token to a renderer that adopted an already-enabled plugin. Safe mode refuses install and enable, stops running plugins and revokes their tokens. A plugin whose manifest files fail validation is marked `CORRUPT` with its files kept in place, and a host version outside `hostVersions` marks it `INCOMPATIBLE`; neither case deletes user files.

The core owns no plugin-host process and no simulated plugin. Monitor and KCT are, by contract, ordinary plugins that use these interfaces; the core has no private channel or hardcoded mount for either.

`GET/POST /chat/sources` manages HTTPS URL entries, Kimi default. Opening a site remains URL_ONLY. `POST /chat/threads {title,sourceId,providerId,model,projectId}` creates a PROVIDER lightweight thread when providerId is given; legacy calls without providerId create explicitly MOCK threads. `GET /chat/threads`, `GET /chat/threads/{id}` read durable history.

`POST /chat/threads/{id}/messages {text}` requires the existing idempotency header. PROVIDER mode uses Go HTTP for openai / responses / anthropic / gemini, sends at most the last 12 messages and saves request intent/state before dispatch. An unavailable configured credential returns 423 before registering a send. INFLIGHT becomes UNKNOWN after restart; an unresolved UNKNOWN or another pending intent blocks new sends to that thread. Reusing intent returns persisted state, never a repeat inference. Safe mode rejects dispatch. Valid text produces COMPLETED and protocol usage enters the separate gateway ledger. Missing usage stays unknown. No streaming, tools, vision input or actual billing integration is claimed for this lightweight chat API. Protocols were exercised with local synthetic HTTP services only.

`GET /chat/threads/{id}/requests` lists dispatch audit history. `POST /chat/threads/{id}/requests/{requestId}/resolve {resolution,note?}` accepts only NOT_FOUND_UPSTREAM or SEEN_UPSTREAM after the user explicitly checks upstream. It is idempotent for the same conclusion, rejects conflicting conclusions, and never resolves INFLIGHT or terminal states. The audit state remains UNKNOWN; resolution only unblocks later messages and never resends the old request.

The separate `desktop.PairIsolatedHost` protocol is a synthetic developer test bridge documented in NATIVE_INTEGRATION.md. It is not an official Kimi protocol or a production connection.


## Local OpenAI/Kimi gateway

`GET /enhance/v1/gateway` reports actual capabilities and a masked token. `GET /enhance/v1/gateway/preview` returns the local `{type:"kimi",base_url,api_key}` provider preview and never writes client configuration. The stable local token is also held in the private 0600 `gateway.json`; it is distinct from the enhancement pairing/session credentials. Kimi model aliases still come from static client `[models]` tables, not from a required `/v1/models` probe.

The loopback-only `/v1` surface provides `GET /models`, `GET /models/{id}` and `POST /chat/completions`. It requires its own Bearer token, rejects forwarded/non-loopback requests, and never forwards the gateway token to a supplier. Model routing uses imported local catalog rows; ambiguous bare model IDs are rejected rather than silently billed to another provider. Supplier credentials come from the selected account's vault reference.

Chat supports OpenAI-compatible buffered and SSE calls, text and reasoning_content in both directions, assistant tool_calls, streamed tool-call fragments, tool results with tool_call_id, tools/tool_choice/parallel_tool_calls, thinking, max_completion_tokens, stream_options and builtin_function tool declarations. Unknown top-level fields do not cause a hard rejection, but unrecognized fields are not promised to be forwarded. Requests are capped at 1 MiB, 200 messages and 256 KiB per message; tool counts/fragments also have explicit limits. Image/file parts are rejected rather than dropped. Streaming is limited to OpenAI-compatible providers; other protocols return STREAM_PROTOCOL. Multi-account scheduling/failover and token refresh are not implemented.

A successful upstream stream must contain a non-empty finish_reason, a pure usage chunk when usage is supplied, and `data:[DONE]`; missing finish or terminator is an error. Early upstream authentication/rate-limit failures remain HTTP errors before an SSE 200 is committed. An interrupted committed stream emits an in-band error and `[DONE]`, is recorded as partial/unknown, and is never automatically retried. The dispatch permit covers the entire stream, so safe-mode/settings writes wait for admitted generations.

Validation used the real Kimi CLI 2.4.1 with an isolated KIMI_CODE_HOME, isolated Core database and synthetic loopback upstream. Both a text turn and an actual Read tool turn completed; no real credential, official real configuration or paid model endpoint was used.

## Encrypted credentials

- `GET /credentials` returns unlocked, refs, backend and osKeychain=false, never values.
- `POST /credentials/unlock {password}` initializes or unlocks; 12–1024 byte password, PBKDF2-SHA256 (600000 iterations), AES-256-GCM. `POST /credentials/lock {}` clears the active key; restart is locked.
- `PUT /credentials {ref,value}` saves encrypted data up to 32 KiB; `DELETE /credentials/{ref...}` deletes while unlocked. Ref is the supplier secretRef; slashes are supported. This API only accesses provider credentials; browser ciphertext is stored separately and cannot be listed, read or deleted through it. It does not access OS Keychain or discover official credentials; explicit read-only file import uses the separate account-import contract above.

## Local workspace and policy evaluation

- `GET /workspace/sessions`, `PUT /workspace/sessions/{id}` list/update local references; writes carry revision, creation uses 0. A reference is not ownership of an official session.
- `GET /workspace/compaction` returns policies/evaluations with nativeConnected=false; `PUT /workspace/compaction/{id} {scope,target,enabled,idleMinutes}` saves policy. session > project > model > global, 1–1440 minutes, default 25. Busy/approval/recovery/Worker activity prevents eligibility. Eligible legacy evaluation remains BLOCKED_NATIVE_UNCONNECTED; this legacy API does not schedule native actions. The separately authorized `internal/desktop` scheduler handles native keep-alive, compaction and recovery.
- `GET/PUT /workspace/layout` stores columns (1–4 in this legacy API, up to 12 panes and 8 reviewers). The current native multi-session UI exposes only 1–3 columns; it does not use extra legacy references as extra native panes. Existing PaneBinding/ReviewerRoute fields use Go names HostID/SessionID/ProjectID/Worktree and ModelID/Role/Enforcement. Enforcement is always LOCAL_PREFERENCE_ONLY.
- `POST /workspace/worktrees {repository,name,ref}` requires an absolute repository path and safe name, invokes git worktree add --detach into repository/.ekd-worktrees/name. The worktree command enters the shared dispatch gate, so safe-mode changes wait for admitted commands and reject later ones. The 20-second command deadline returns HTTP 504 `WORKTREE_TIMEOUT`; inspect the repository before retrying. No commit or push. Requires Git only when used.

## Browser export isolation

- `POST /browser/migrations/preview {browser,target,mode,sites,cookies,consent}` accepts user-provided JSON only. Browser=chrome/edge/export; mode=SITES/ALL, explicit consent, at most 1000 cookies, unlocked vault. Expired/out-of-scope items are skipped; target conflicts ignore source storeId. The UI accepts export files up to 1 MiB. Values use a separate encrypted browser table with an 8 MiB limit per normalized preview or merged target; capacity failures return HTTP 413 `BROWSER_STORE_FULL`, not `VAULT_LOCKED`. Credential list/read/delete operations cannot access this table. Upgrade moves only legacy entries linked to migration records, retaining ciphertext. Preview returns metadata only and expires after 24 hours; expired values are inaccessible and collected at startup and browser requests, including while locked. History reports expired previews as `EXPIRED`.
- `POST /browser/migrations/{id}/apply {conflict:KEEP|REPLACE}` merges into the enhancement's encrypted isolated store, state STORED_ISOLATED. It does not restore a real browser login. Apply uses the shared dispatch gate and serializes target merges; safe mode blocks admission. Expired or collected previews return HTTP 410 `MIGRATION_EXPIRED`; create a fresh preview.
- `GET /browser/migrations` lists recent metadata with nativeConnected=false. `GET/PUT /browser/permissions` manages per-HTTPS-origin `{origin,permissions}` records. Permission flags are independent of imported login data and do not activate a browser adapter.

## Embeddable UI module

`web/dist-host` (embedded under `/host/` by the build script) contains mountEnhancement(element,{core,permissions,stylesheetURL,stylesheetText?,assetBaseURL?,colorScheme?,embed?,context,back,openBrowser?}). The caller explicitly provides an authenticated CoreTransport; no private Kimi token, new CORS allowance or automatic injection is implemented by the module. Shadow DOM isolates CSS; `embed:true` hides the standalone header while retaining nested-page navigation. updateContext/unmount manage context and cleanup.

On 2026-11-13 the module was mounted into the real Kimi Code 1.3.4 settings dialog in an isolated profile and isolated KIMI_CODE_HOME. That was an early console-message demonstration bridge. The current 1.3.5 runtime instead uses authenticated main-process loopback RPC and `mountSurface` for planner, sessions, settings and Monitor; core credentials remain in the main process. Settings are embedded in the official settings dialog, and the request-ID fallback covers opaque origins without `crypto.randomUUID()`. Native surface screenshots and interactions support the current layout; they do not establish a complete composited-window capture or other-platform compatibility.

## Native desktop actions

`POST /desktop/actions/{action}` accepts a JSON object and returns `{value}`. It is the Core side of the authenticated native bridge, not a public anonymous Electron API. Supported groups are:

- `bootstrap`, `events.poll`, `session.view`, `session.watch`, `session.unwatch`, `session.create`, `session.send`, `session.abort`, `session.reply`, and `handoff`. Prompts use the real KAP session ID. `session.send` accepts `{id,text,mode?,model?,permissionMode?,attachments?:string[]}`. Modes are `normal` (default), `plan`, `tower`, or internal `inherit` (does not write plan/tower flags). `model` and `permission_mode` are omitted unless explicitly selected; permissionMode must be `manual`, `auto` or `yolo`. Text or attachments must be present. At most 16 attachments are accepted; each must be an absolute path to an existing local regular file. Supported image/video extensions map to KAP path sources; other files map to KAP file parts. This passes local paths to the same-machine KAP service, not the legacy `/attachments` blob store or website upload. `session.view` retains official message timestamps (`at`) and attachment metadata (`kind`, `name`); it does not return attachment bytes. Handoff uses `targetId` instead of `id` and creates a session only when `targetId` is empty.
- `planner.create/get/configure/send/stop/approve/delete/retry` for durable planning threads. Planner streaming is exposed through scoped event polling. Tool approval is explicit; stopping retains already received content. Retry carries the current revision and reuses the original request rather than adding a duplicate user message.
- `config.get`, `config.save` for the isolated official config; `models.list` for official configured models with capabilities, limits and efforts; `providers.updateModel` for reading a provider from KAP, updating only the target model's capability/thinking/limit/effort fields without resending `api_key`, and verifying read-back; `managed.usage` for `/oauth/usage`. Missing or unsupported usage remains UNKNOWN.
- `automation.get/set`, `recovery.get/set/watch/unwatch/check`, and `schedule.status/list/save/delete/toggle/runNow/runs/markRead/markAllRead/prefs` for the persistent scheduler engine (`schedule:manage`). Cache and quota readings are served by `cache.summary/sessions/history/turns/retention/receipts` and `quota.balances` (Kimi Code 5h/weekly quota, booster wallet, and configured external supplier balances via Go-side headers with identity-scoped fallback and never fabricating zero on failure); there is no `monitor.summary`: the sidebar widget is the preinstalled Monitor plugin and reads those actions through `metrics:read`. Keep-alive, compaction and recovery are independently disabled by default. The five-second scheduler executes admitted native actions with persisted intent/receipt state; safe mode suppresses execution. Compaction ACK is not completion, interrupted intents become UNKNOWN, and manual unwatch suppresses requeue of the same context revision until explicitly watched again.

Main-process-only actions include `core.fetch`, `window.openSettings`, `window.popout`, `window.close`, `window.ensureWidth`, `host.openSession`, `host.activateSession`, `host.pickSession`, `host.pickFiles`, `host.pickDirectory`, `host.openBrowser`, `host.browserBounds`, and `host.extractBrowser`. `host.activateSession {id}` changes the official active session while keeping the enhancement mounted; `host.openSession {id}` first restores the official single-session view. `host.pickFiles {}` opens a multi-file native dialog and returns `{path,name}[]` (empty on cancellation); `host.pickDirectory {title?,defaultPath?}` opens a native directory selection dialog (`openDirectory`, `createDirectory`) and returns `{path}` (`""` on cancellation). `window.popout` accepts planner `initialSource:local|web` (default web), preserving the caller's selected source. Browser URLs are limited to HTTP(S); views/windows use isolated enhancement sessions and no ambient Chrome/Edge cookies. Extraction reads the current selection, then main/body text as fallback. It does not send a prompt into a third-party website or observe a website reply.

Current native acceptance uses Kimi Code 1.3.7/Electron 45.1.0 and synthetic providers/sites in isolated profiles. It covers `ui:entry` (`aside.session-info`, empty right-panel launcher, and `browser-overlay` `+` menu), `ui:sidebar-nav` and `ui:main-area` (`mode:"page"`), provider capability editing and subagent pool catalog selection, Monitor desktop replica with quota/wallet/external balances, persistent scheduled task execution, planner/web handoff, local-file KAP sends, five-entry embedded settings persistence and manual recovery. File selection was deterministic via a temporary Electron dialog substitute; actual system-dialog GUI selection remains unverified. Real paid models, real managed-account automation, website-internal chat/upload, login-state migration and native execution on other platforms remain unverified.
