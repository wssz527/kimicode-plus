// 最小插件示例：侧栏一行文字 + 一个可停用的定时刷新。
// 所有能力都经 ctx 提供，插件不直接访问 fetch、localStorage 或宿主全局对象。

export async function activate(ctx) {
  const stored = ctx.storage ? await ctx.storage.get("greetings").catch(() => 0) : 0;
  // A first run has no value, and an earlier version of this plugin could store
  // NaN (which JSON turns into null). Treat both as "never enabled before" rather
  // than letting the number render as NaN forever.
  const count = Number.isFinite(Number(stored)) ? Number(stored) : 0;

  const mount = ctx.ui.mount("sidebar", element => {
    const row = document.createElement("div");
    row.className = "ekdp-row";
    const dot = document.createElement("span");
    dot.className = "ekdp-dot";
    const text = document.createElement("span");
    text.className = "ekdp-text";
    text.textContent = `示例插件：你好（第 ${Number(count) + 1} 次启用）`;
    row.append(dot, text);
    element.append(row);

    if (ctx.storage) void ctx.storage.set("greetings", Number(count) + 1);
    ctx.log("info", "示例插件侧栏已挂载");

    return () => row.remove();
  });

  ctx.onDispose(() => mount && mount.dispose());
  return { status: "active" };
}
