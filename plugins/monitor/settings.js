/* KimiCode+ Monitor · 结构源自 bfjnbvf/kimi-code-monitor (MIT)，见同目录 LICENSE */

var f="kimi-statusbar.config",x="kimi-statusbar.mini",L={day:"24 小时",week:"7 天",month:"30 天"},S={version:3,modules:{header:{show:"hidden",span:2,stat:"balance",dataLink:"subscription"},input:{show:"full",span:1},cache:{show:"full",span:1},output:{show:"full",span:1},speed:{show:"full",span:1},duration:{show:"hidden",span:2},quota5h:{show:"mini",span:1,pace:!0,resetFormat:"countdown"},quotaWeek:{show:"mini",span:1,pace:!0,resetFormat:"countdown"},usageChart:{show:"hidden",span:2,chartRange:"week"},pet:{show:"mini",span:2,stat:"daily",sidebarTidy:!1,ballLink:"subscription"},agents:{show:"hidden",span:2,hiddenAgents:[]},external:{show:"full",span:2,hiddenAccounts:[]}},orderFull:["external","output","cache","input","speed"],orderMini:["pet","quota5h","quotaWeek"],orderHidden:["header","duration","agents","usageChart"]},T=`
.ekdp-mon-settings {
  display: flex;
  flex-direction: column;
  gap: 12px;
  color: var(--ekds-text, var(--color-text, inherit));
  font: 13px/1.55 var(--font-ui, system-ui, sans-serif);
}
.ekdp-mon-intro {
  margin: 0;
  font-size: 12px;
  line-height: 1.55;
  color: var(--ekds-muted, var(--color-text-muted, var(--color-text-secondary, light-dark(#5c6370, #a3aab8))));
}
.ekdp-mon-group {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: var(--ekds-hairline, 1px solid var(--color-line, rgba(128, 128, 128, 0.22)));
  border-radius: var(--ekds-radius, 12px);
  background: var(--ekds-surface, var(--color-surface, light-dark(#f8f9fb, rgba(255, 255, 255, 0.04))));
}
.ekdp-mon-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 16px;
  border-top: var(--ekds-hairline, 1px solid var(--color-line, rgba(128, 128, 128, 0.2)));
}
.ekdp-mon-row:first-child { border-top: 0; }
.ekdp-mon-row-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.ekdp-mon-row-text strong {
  font-size: 13px;
  font-weight: 500;
  color: var(--ekds-text, var(--color-text, inherit));
}
.ekdp-mon-seg {
  display: inline-flex;
  flex: none;
  gap: 2px;
  padding: 3px;
  border: var(--ekds-hairline, 1px solid var(--color-line, rgba(128, 128, 128, 0.22)));
  border-radius: 8px;
  background: var(--ekds-bg, var(--color-bg, light-dark(#eef0f4, rgba(0, 0, 0, 0.26))));
}
.ekdp-mon-seg button {
  height: 26px;
  padding: 0 10px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--ekds-muted, var(--color-text-muted, var(--color-text-secondary, light-dark(#5c6370, #a3aab8))));
  font: 500 12px/1 var(--font-ui, system-ui, sans-serif);
  cursor: pointer;
  white-space: nowrap;
  transition: background 120ms ease, color 120ms ease;
}
.ekdp-mon-seg button:hover {
  color: var(--ekds-text, var(--color-text, inherit));
}
.ekdp-mon-seg button[aria-pressed="true"] {
  background: var(--ekds-surface, var(--color-surface-raised, light-dark(#ffffff, rgba(255, 255, 255, 0.14))));
  color: var(--ekds-text, var(--color-text, inherit));
  box-shadow: 0 1px 2px light-dark(rgba(15, 23, 42, 0.08), rgba(0, 0, 0, 0.35));
}
.ekdp-mon-switch {
  flex: none;
  width: 36px;
  height: 20px;
  padding: 0;
  border: 1px solid color-mix(in srgb, var(--ekds-line, rgba(128, 128, 128, 0.3)) 85%, transparent);
  border-radius: 999px;
  background: color-mix(in srgb, var(--ekds-muted, #888) 26%, transparent);
  cursor: pointer;
  position: relative;
  transition: background 140ms ease, border-color 140ms ease;
}
.ekdp-mon-switch::after {
  content: "";
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
  transition: transform 140ms ease;
}
.ekdp-mon-switch[aria-pressed="true"] {
  border-color: transparent;
  background: var(--ekds-accent, var(--color-accent, #2563eb));
}
.ekdp-mon-switch[aria-pressed="true"]::after {
  transform: translateX(16px);
}
.ekdp-mon-switch:focus-visible,
.ekdp-mon-seg button:focus-visible {
  outline: none;
  box-shadow: var(--ekds-ring, 0 0 0 2px var(--color-accent, #2563eb));
}
.ekdp-mon-note {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--ekds-muted, var(--color-text-muted, var(--color-text-secondary, light-dark(#5c6370, #a3aab8))));
}
.ekdp-mon-ok {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--success, #27865a);
}
.ekdp-mon-error {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-danger, var(--danger, #c64b52));
}
`;function r(a,d,n){let s=document.createElement(a);return d&&(s.className=d),n!=null&&(s.textContent=n),s}function R(a){let d=structuredClone(S);if(!a||typeof a!="object")return d;let n=structuredClone(a);return n.modules=n.modules&&typeof n.modules=="object"?n.modules:{},n.modules.usageChart={...d.modules.usageChart,...n.modules.usageChart||{}},n.modules.pet={...d.modules.pet,...n.modules.pet||{}},n}function z(a,d){let n=document.createElement("style");n.textContent=T,d.append(n);let s=r("div","ekdp-mon-settings"),h=r("p","ekdp-mon-note",""),p=null,l="0",b=!1,u=!1;function g(o,t){h.className=t==="error"?"ekdp-mon-error":t==="ok"?"ekdp-mon-ok":"ekdp-mon-note",h.textContent=o}async function v(o){if(!p)return;let t=structuredClone(p);o(t);try{await a.storage.set(f,t)}catch(e){g(`保存失败，仍在使用上次保存的设置：${e?.message||e}`,"error");return}u||(p=t,globalThis.dispatchEvent?.(new CustomEvent("ekdp-monitor-storage-changed",{detail:{key:f,value:t}})),m(),g("已保存","ok"))}function w(){let o=r("div","ekdp-mon-row"),t=r("div","ekdp-mon-row-text");t.append(r("strong","","消耗图默认窗口")),t.append(r("span","ekdp-mon-note","打开面板时图表显示的区间；面板内可临时切换。"));let e=r("div","ekdp-mon-seg"),i=p?.modules?.usageChart?.chartRange||"week";for(let[c,E]of Object.entries(L)){let k=r("button","",E);k.type="button",k.setAttribute("aria-pressed",String(i===c)),k.addEventListener("click",()=>{v(A=>{A.modules.usageChart.chartRange=c})}),e.append(k)}return o.append(t,e),o}function y(){let o=r("div","ekdp-mon-row"),t=r("div","ekdp-mon-row-text");t.append(r("strong","","迷你模式")),t.append(r("span","ekdp-mon-note","只保留宠物与额度条，点击面板展开完整区。"));let e=r("button","ekdp-mon-switch");return e.type="button",e.setAttribute("role","switch"),e.setAttribute("aria-label","迷你模式"),e.setAttribute("aria-pressed",String(l==="1")),e.setAttribute("aria-checked",String(l==="1")),e.addEventListener("click",async()=>{let i=l==="1"?"0":"1";try{await a.storage.set(x,i)}catch(c){g(`保存失败，仍在使用上次保存的设置：${c?.message||c}`,"error");return}u||(l=i,globalThis.dispatchEvent?.(new CustomEvent("ekdp-monitor-storage-changed",{detail:{key:x,value:i}})),e.setAttribute("aria-pressed",String(i==="1")),e.setAttribute("aria-checked",String(i==="1")),g("已保存","ok"))}),o.append(t,e),o}function C(){let o=r("div","ekdp-mon-row"),t=r("div","ekdp-mon-row-text");t.append(r("strong","","侧栏整理")),t.append(r("span","ekdp-mon-note","隐藏官方侧栏顶部的品牌标志，把新建对话行与折叠按钮对齐。"));let e=r("button","ekdp-mon-switch");e.type="button",e.setAttribute("role","switch"),e.setAttribute("aria-label","侧栏整理");let i=p?.modules?.pet?.sidebarTidy===!0;return e.setAttribute("aria-pressed",String(i)),e.setAttribute("aria-checked",String(i)),e.addEventListener("click",()=>{v(c=>{c.modules.pet.sidebarTidy=c.modules.pet.sidebarTidy!==!0})}),o.append(t,e),o}function m(){if(s.replaceChildren(),s.append(r("p","ekdp-mon-intro","面板数据来自本机缓存指标与核心余额读取，不读取会话正文，也不保存任何供应商密钥。")),!b){s.append(r("p","ekdp-mon-note","正在读取已保存的设置…"));return}if(p){let o=r("div","ekdp-mon-group");o.append(w(),y(),C()),s.append(o)}s.append(h)}return d.append(s),m(),(async()=>{try{let o=await a.storage.get(f);if(u)return;p=R(o);let t=await a.storage.get(x);u||(l=t==="1"?"1":"0"),b=!0,u||m()}catch(o){if(u)return;b=!0,p=null,g(`读取设置失败：${o?.message||o}`,"error"),m()}})(),()=>{u=!0,n.remove()}}export{z as renderSettings};
