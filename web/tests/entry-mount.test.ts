import test from "node:test";
import assert from "node:assert/strict";
import { createEntryMountManager, mountMainAreaSurface, type EntryPlan, type SidebarNavPlan } from "../src/plugin-runtime/entries.js";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";
import { FakeDocument, FakeElement } from "./helpers/fake-dom.js";

const PLUGIN = "kimicode-plus.planner";
const ACTION = `plugin:${PLUGIN}:planner`;
const ICON = "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Zm3.5 5-2 5-5 2 2-5 5-2Z";

function officialButton(document: FakeDocument, className: string, label: string): FakeElement {
  const button = document.createElement("button");
  button.className = className;
  button.setAttribute("data-v-test", "");
  const icon = document.createElement("svg");
  icon.setAttribute("viewBox", "0 0 16 16");
  const text = document.createElement("span");
  text.className = "sit-name";
  text.textContent = label;
  button.append(icon, text);
  return button;
}

interface Harness {
  document: FakeDocument;
  manager: ReturnType<typeof createEntryMountManager>;
  sessionInfo: FakeElement;
  available: FakeElement;
  emptyList: FakeElement;
  launcher: FakeElement;
  addMenu: FakeElement;
  sidebar: FakeElement;
  newChat: FakeElement;
}

function harness(): Harness {
  const document = new FakeDocument();
  const app = document.createElement("div");
  document.body.append(app);

  const aside = document.createElement("aside");
  aside.className = "session-info is-sidebar";
  const sit = document.createElement("div");
  sit.className = "sit";
  const opened = document.createElement("section");
  opened.className = "sit-section";
  const openedButton = officialButton(document, "sit-link sit-opened", "侧边聊天");
  opened.append(openedButton);
  const available = document.createElement("section");
  available.className = "sit-section";
  available.append(
    officialButton(document, "sit-link sit-available", "文件"),
    officialButton(document, "sit-link sit-available", "侧边聊天"),
  );
  sit.append(opened, available);
  aside.append(sit);
  app.append(aside);

  const panel = document.createElement("div");
  panel.className = "pt-body";
  const emptyList = document.createElement("div");
  emptyList.className = "sit";
  const emptySection = document.createElement("section");
  emptySection.className = "sit-section";
  emptySection.append(
    officialButton(document, "sit-link sit-available", "文件"),
    officialButton(document, "sit-link sit-available", "侧边聊天"),
  );
  emptyList.append(emptySection);
  const launcher = document.createElement("div");
  launcher.className = "pl";
  launcher.append(
    officialButton(document, "ui-menu-item", "文件"),
    officialButton(document, "ui-menu-item", "侧边聊天"),
  );
  panel.append(emptyList, launcher);
  app.append(panel);

  const addMenu = document.createElement("div");
  addMenu.className = "panel-add-menu";
  addMenu.append(
    officialButton(document, "ui-menu-item", "文件"),
    officialButton(document, "ui-menu-item", "侧边聊天"),
  );
  app.append(addMenu);

  const column = document.createElement("div");
  column.className = "sidebar-actions";
  const newChat = officialButton(document, "btn-new-chat", "新建会话");
  column.append(newChat);
  document.body.append(column);

  const manager = createEntryMountManager({ document: document as unknown as Document });
  return { document, manager, sessionInfo: aside, available, emptyList, launcher, addMenu, sidebar: column, newChat };
}

function plan(open: () => void | Promise<void> = () => {}): EntryPlan {
  return { action: ACTION, title: "规划", icon: ICON, pluginId: PLUGIN, open };
}

function navPlan(open: () => void | Promise<void> = () => {}): SidebarNavPlan {
  return { action: `sidebar:${PLUGIN}:tasks`, title: "定时任务", icon: ICON, pluginId: PLUGIN, open };
}

function entriesIn(container: FakeElement): FakeElement[] {
  return container.querySelectorAll("[data-ekd-entry]");
}

test("one ui:entry plan reaches the session-info available group, the panel empty list, the launcher and the + menu", () => {
  const store = harness();
  store.manager.sync([plan()]);

  assert.equal(entriesIn(store.available).length, 1);
  assert.equal(entriesIn(store.emptyList).length, 1);
  assert.equal(entriesIn(store.launcher).length, 1);
  assert.equal(entriesIn(store.addMenu).length, 1);

  // injected rows follow every official row in their container
  const officials = store.available.querySelectorAll(".sit-available").filter(node => !node.hasAttribute("data-ekd-entry"));
  assert.equal(officials.at(-1)?.nextElementSibling, entriesIn(store.available)[0]);

  const entry = entriesIn(store.available)[0]!;
  assert.equal(entry.getAttribute("data-ekd-entry"), ACTION);
  assert.equal(entry.getAttribute("aria-label"), "规划");
  assert.ok(entry.classList.contains("ekd-native-entry"));
  assert.ok(entry.classList.contains("sit-link"));
  assert.ok(entry.classList.contains("sit-available"));
  // The official scoped attribute is copied, so the official CSS still applies.
  assert.equal(entry.getAttribute("data-v-test"), "");
  assert.equal(entry.querySelector("svg path")?.getAttribute("d"), ICON);
  assert.equal(entry.querySelector(".sit-name")?.textContent, "规划");
  assert.equal(entry.listenerCount(), 1);
});

test("disabling the plugin removes every surface entry and leaves no listener or observer behind", () => {
  const store = harness();
  for (let index = 0; index < 20; index += 1) {
    store.manager.sync([plan()]);
    assert.equal(store.manager.stats().entries, 4);
    assert.equal(store.manager.stats().observers, 0);
    store.manager.sync([]);
    assert.equal(store.manager.stats().entries, 0);
    assert.equal(entriesIn(store.available).length, 0);
    assert.equal(entriesIn(store.emptyList).length, 0);
    assert.equal(entriesIn(store.launcher).length, 0);
    assert.equal(entriesIn(store.addMenu).length, 0);
  }
  assert.equal(store.manager.stats().observers, 0);
});

test("a sync never duplicates a row and an official re-render gets exactly one row back", () => {
  const store = harness();
  store.manager.sync([plan()]);
  store.manager.sync([plan()]);
  store.manager.sync([plan()]);
  assert.equal(entriesIn(store.available).length, 1);
  assert.equal(entriesIn(store.launcher).length, 1);

  // Vue removed the injected node while patching the official list.
  entriesIn(store.available)[0]!.remove();
  store.manager.sync([plan()]);
  assert.equal(entriesIn(store.available).length, 1);
  const officials = store.available.querySelectorAll(".sit-available").filter(node => !node.hasAttribute("data-ekd-entry"));
  assert.equal(entriesIn(store.available)[0]!.previousElementSibling, officials.at(-1));

  // The whole official group was replaced.
  store.available.remove();
  const fresh = store.document.createElement("section");
  fresh.className = "sit-section";
  fresh.append(officialButton(store.document, "sit-link sit-available", "侧边聊天"));
  store.sessionInfo.querySelector(".sit")!.append(fresh);
  store.manager.sync([plan()]);
  // The removed section is detached, so its old row left the document with it and
  // only the freshly rendered group holds a row.
  assert.equal(store.document.querySelectorAll("[data-ekd-entry]").length, 4);
  assert.equal(entriesIn(fresh).length, 1);
});

test("clicking the session-info entry opens the official panel first, then the plugin tab", async () => {
  const calls: string[] = [];
  const store = harness();
  const manager = createEntryMountManager({
    document: store.document as unknown as Document,
    openPanel: () => calls.push("panel"),
    onError: error => { throw error; },
  });
  manager.sync([plan(() => { calls.push("tab"); })]);
  entriesIn(store.available)[0]!.click();
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(calls, ["panel", "tab"]);

  // A row inside the right panel does not re-open the panel; it only opens the tab.
  calls.length = 0;
  entriesIn(store.launcher)[0]!.click();
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(calls, ["tab"]);
  manager.dispose();
});

test("a failing plugin entry is reported instead of becoming an unhandled rejection", async () => {
  const seen: string[] = [];
  const store = harness();
  const manager = createEntryMountManager({
    document: store.document as unknown as Document,
    onError: (pluginId, error) => seen.push(`${pluginId}:${(error as Error).message}`),
  });
  manager.sync([plan(() => { throw new Error("boom"); })]);
  entriesIn(store.available)[0]!.click();
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(seen, [`${PLUGIN}:boom`]);
  manager.dispose();
});

test("ui:sidebar-nav inserts one official-looking row under 新建会话 and cleans it up", async () => {
  const store = harness();
  const calls: string[] = [];
  for (let index = 0; index < 20; index += 1) {
    store.manager.sync([], [navPlan(() => { calls.push("open"); })]);
    assert.equal(store.manager.stats().navs, 1);
    assert.equal(store.manager.stats().observers, 0);
    store.manager.sync([], []);
    assert.equal(store.manager.stats().navs, 0);
  }
  store.manager.sync([], [navPlan(() => { calls.push("open"); })]);
  const row = store.sidebar.querySelector("[data-ekd-sidebar-nav]")!;
  assert.equal(row.previousElementSibling, store.newChat);
  assert.ok(row.classList.contains("btn-new-chat"));
  assert.ok(row.classList.contains("ekd-sidebar-nav"));
  assert.equal(row.querySelector("span")?.textContent, "定时任务");
  row.click();
  await Promise.resolve();
  assert.deepEqual(calls, ["open"]);

  // The official sidebar re-render replaces the button; the row comes back once.
  store.newChat.remove();
  const replacement = officialButton(store.document, "btn-new-chat", "新建会话");
  store.sidebar.append(replacement);
  store.manager.sync([], [navPlan()]);
  assert.equal(store.sidebar.querySelectorAll("[data-ekd-sidebar-nav]").length, 1);
  assert.equal(store.sidebar.querySelector("[data-ekd-sidebar-nav]")!.previousElementSibling, replacement);
  store.manager.dispose();
  assert.equal(store.manager.stats().navs, 0);
});

test("the runtime only exposes sidebarNav with the ui:sidebar-nav grant and disposes the row", async () => {
  const rows: string[] = [];
  const hosts = {
    sidebarNavHost: {
      register(pluginId: string, definition: { id: string; title: string; open(): void }) {
        rows.push(`${pluginId}:${definition.id}:${definition.title}`);
        return { dispose: () => { rows.splice(rows.indexOf(`${pluginId}:${definition.id}:${definition.title}`), 1); } };
      },
    },
  };
  const runtime = createPluginRuntime({ slots: {}, request: async () => ({ value: {} }), ...hosts });
  await runtime.activate(
    { activate: async context => {
      assert.equal(context.sidebarNav, undefined);
    } },
    { id: "test.local", version: "2.0.0", granted: [], token: "t" },
  );
  await runtime.activate(
    { activate: async context => {
      context.sidebarNav!.add({ id: "tasks", title: "定时任务", open: () => {} });
    } },
    { id: "test.official", version: "2.0.0", granted: ["ui:sidebar-nav"], token: "t" },
  );
  assert.deepEqual(rows, ["test.official:tasks:定时任务"]);
  runtime.deactivate("test.official");
  assert.deepEqual(rows, []);
  runtime.dispose();
});

test("mountMainAreaSurface supports split default and page full-area mode with clean disposal", () => {
  const doc = new FakeDocument();
  const chatMain = doc.createElement("div");
  chatMain.className = "chat-main";
  const nativeChat = doc.createElement("div");
  nativeChat.className = "chat";
  const composer = doc.createElement("div");
  composer.className = "composer";
  chatMain.append(nativeChat, composer);
  doc.body.append(chatMain);

  let splitCleaned = false;
  const splitHandle = mountMainAreaSurface({
    document: doc as unknown as Document,
    container: chatMain as unknown as HTMLElement,
    pluginId: "test.split",
    definition: {
      width: 440,
      render: pane => {
        const marker = doc.createElement("div");
        marker.className = "plugin-split-view";
        (pane as unknown as FakeElement).append(marker);
        return () => { splitCleaned = true; };
      },
    },
  });

  const splitRow = chatMain.querySelector(".ekdp-main-split")!;
  assert.equal(splitRow.dataset.ekdpMode, "split");
  assert.equal(chatMain.dataset.ekdpSplit, "test.split");
  const nativeWrap = splitRow.querySelector(".ekdp-main-native")!;
  const splitter = splitRow.querySelector(".ekdp-main-splitter")!;
  const splitPane = splitRow.querySelector(".ekdp-main-pane")!;
  assert.equal(nativeWrap.hidden, false);
  assert.equal(splitter.hidden, false);
  assert.deepEqual(nativeWrap.children, [nativeChat, composer]);
  assert.equal(splitPane.style.width, "440px");
  splitHandle.setWidth(520);
  assert.equal(splitHandle.width(), 520);
  assert.equal(splitPane.style.width, "520px");

  splitHandle.dispose();
  assert.equal(splitCleaned, true);
  assert.deepEqual(chatMain.children, [nativeChat, composer]);
  assert.equal(chatMain.dataset.ekdpSplit, "");
  assert.equal(chatMain.style.display, undefined);

  let pageCleaned = false;
  const pageHandle = mountMainAreaSurface({
    document: doc as unknown as Document,
    container: chatMain as unknown as HTMLElement,
    pluginId: "test.page",
    definition: {
      mode: "page",
      render: pane => {
        const marker = doc.createElement("div");
        marker.className = "plugin-page-view";
        (pane as unknown as FakeElement).append(marker);
        return () => { pageCleaned = true; };
      },
    },
  });

  const pageRow = chatMain.querySelector(".ekdp-main-split")!;
  assert.equal(pageRow.dataset.ekdpMode, "page");
  assert.equal(chatMain.dataset.ekdpPage, "test.page");
  assert.equal(chatMain.dataset.ekdpSplit, "");
  const pageNative = pageRow.querySelector(".ekdp-main-native")!;
  const pageSplitter = pageRow.querySelector(".ekdp-main-splitter")!;
  const pagePane = pageRow.querySelector(".ekdp-main-pane")!;
  assert.equal(pageNative.hidden, true);
  assert.equal(pageNative.style.display, "none");
  assert.equal(pageSplitter.hidden, true);
  assert.equal(pageSplitter.style.display, "none");
  assert.equal(pagePane.style.width, "100%");
  assert.equal(pagePane.style.flex, "1 1 auto");

  pageHandle.setWidth(300);
  assert.equal(pagePane.style.width, "100%");

  pageHandle.dispose();
  assert.equal(pageCleaned, true);
  assert.deepEqual(chatMain.children, [nativeChat, composer]);
  assert.equal(chatMain.dataset.ekdpPage, "");
  assert.equal(chatMain.style.display, undefined);
});
for (const closeOrder of [["a", "b"], ["b", "a"]] as const) {
  test(`opening two plugin splits preserves chat when closed in order ${closeOrder.join(", ")}`, () => {
    const document = new FakeDocument();
    const chatMain = document.createElement("div");
    const chat = document.createElement("div");
    const composer = document.createElement("div");
    const unrelated = document.createElement("div");
    chatMain.append(chat, composer);
    document.body.append(chatMain, unrelated);
    chatMain.style.display = "grid";
    const released: string[] = [];
    const mount = (id: string) => mountMainAreaSurface({
      document: document as unknown as Document,
      container: chatMain as unknown as HTMLElement,
      pluginId: id,
      definition: { render: () => () => { released.push(id); } },
    });
    const a = mount("a");
    const b = mount("b");
    assert.deepEqual(released, ["a"]);
    assert.equal(chatMain.querySelectorAll(".ekdp-main-split").length, 1);
    assert.equal(chatMain.dataset.ekdpSplit, "b");
    assert.deepEqual(chatMain.querySelector(".ekdp-main-native")!.children, [chat, composer]);
    a.setWidth(500);
    assert.equal(chatMain.dataset.ekdpSplit, "b");
    closeOrder.forEach(id => ({ a, b })[id].dispose());
    assert.deepEqual(released, ["a", "b"]);
    assert.deepEqual(chatMain.children, [chat, composer]);
    assert.deepEqual(document.body.children, [chatMain, unrelated]);
    assert.equal(chatMain.querySelectorAll(".ekdp-main-split").length, 0);
    assert.equal(chatMain.dataset.ekdpSplit, "");
    assert.equal(chatMain.style.display, "grid");
  });
}
