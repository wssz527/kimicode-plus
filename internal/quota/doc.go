// Package quota answers one question per account: "what is left, as of when,
// from which source?" It never answers "what is my total", because a
// subscription window, a Coding Plan window, a topped-up balance and a
// gateway's own self-set limit are different units on different clocks. No
// value here is summed across sources, and no missing value is written as 0.
//
// The reading model is ported from the user's own Kimi Work+ project
// (internal/quota). The vendor coverage is the union of that project and
// bfjnbvf/kimi-code-monitor's src/providers.js: DeepSeek, Moonshot (CN and
// global), BigModel/Zhipu, MiniMax, StepFun, the local Kimi Code subscription
// usage endpoint and the local Magpie gateway.
//
// What this package deliberately does not do:
//
//   - It sends no inference request. Every adapter is a documented read-only
//     balance/usage endpoint.
//   - It reads no browser cookie or console session. A credential arrives from
//     the caller through an in-memory resolver, is used for exactly one request,
//     and is never written to disk, never returned from State and never put in
//     an error message.
//   - It does not poll. State is pure. Refresh runs only when the host asked for
//     it, which the plugin does only while its balance module is visible.
package quota