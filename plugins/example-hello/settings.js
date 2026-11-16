// 插件设置子页。ctx 与 activate 相同，ctx.ui.mount("settings", ...) 把内容
// 挂到"插件"页里该插件的设置子页，停用时按作用域释放。

export async function renderSettings(ctx, element) {
  const card = document.createElement("section");
  card.className = "ekdp-settings";

  const title = document.createElement("h3");
  title.textContent = "示例插件设置";

  const note = document.createElement("p");
  note.textContent = "这个页面演示插件如何声明自己的设置项。示例插件没有真正的配置，只有一段说明和插件的身份信息。";

  const identity = document.createElement("p");
  identity.className = "ekdp-muted";
  identity.textContent = `插件 ${ctx.plugin.id} · 版本 ${ctx.plugin.version} · API v${ctx.plugin.dataVersion}`;

  element.append(card);
  card.append(title, note, identity);

  return () => card.remove();
}
