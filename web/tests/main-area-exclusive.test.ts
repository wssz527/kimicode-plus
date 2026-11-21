import test from "node:test";
import assert from "node:assert/strict";
import { mountMainAreaSurface } from "../src/plugin-runtime/entries.js";
import { FakeDocument, FakeElement } from "./helpers/fake-dom.js";

/**
 * `.chat-main` can carry the same-screen session host or a plugin main area, not
 * both. The session renderer hides every direct child except its own host, and
 * the plugin split wraps every direct child. Running both at once therefore put
 * the session host inside `.ekdp-main-split`, which the session CSS hides, and
 * the whole main column went blank.
 *
 * These tests drive the two orders against the real `mountMainAreaSurface`. The
 * renderer side of the handoff (`mountSessions` disposing plugin areas,
 * `pluginMainAreaHost.mount` closing sessions) is driven by the sequence the
 * tests model: a session host is present exactly while the session mode is on.
 */

interface ChatMain {
  document: FakeDocument;
  chatMain: FakeElement;
  chat: FakeElement;
  composer: FakeElement;
}

function chatMain(): ChatMain {
  const document = new FakeDocument();
  const container = document.createElement("div");
  container.className = "chat-main";
  const chat = document.createElement("div");
  chat.className = "chat";
  const composer = document.createElement("div");
  composer.className = "composer";
  container.append(chat, composer);
  document.body.append(container);
  return { document, chatMain: container, chat, composer };
}

/** Mirrors `mountSessions`: the host is appended last and the extra children are
 * hidden by the `[data-ekd-containment=sessions]` rule rather than removed. */
function enterSessionMode(host: ChatMain): FakeElement {
  const element = host.document.createElement("div");
  element.className = "ekd-session-host";
  host.chatMain.append(element);
  host.chatMain.setAttribute("data-ekd-containment", "sessions");
  return element;
}

function leaveSessionMode(host: ChatMain, element: FakeElement): void {
  element.remove();
  host.chatMain.removeAttribute("data-ekd-containment");
}

function mountSplit(host: ChatMain, pluginId = "test.split") {
  return mountMainAreaSurface({
    document: host.document as unknown as Document,
    container: host.chatMain as unknown as HTMLElement,
    pluginId,
    definition: {
      width: 420,
      render: pane => {
        const marker = host.document.createElement("div");
        marker.className = "plugin-pane-view";
        (pane as unknown as FakeElement).append(marker);
        return () => {
          marker.remove();
        };
      },
    },
  });
}

function officialChildren(host: ChatMain): FakeElement[] {
  return host.chatMain.children.filter(child => child.classList.contains("ekd-session-host") || child.classList.contains("ekdp-main-split"));
}

test("session mode then plugin split restores the official column after the split exits", () => {
  const host = chatMain();
  const sessionHost = enterSessionMode(host);

  // `mountSessions` releases every plugin main area before appending its host,
  // so the split cannot be wrapping the column at this point.
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 0);
  assert.ok(host.chatMain.children.includes(sessionHost));

  // Entering the plugin split first closes the session view; the renderer drops
  // the host and its containment attribute before the wrapper moves anything.
  leaveSessionMode(host, sessionHost);
  const split = mountSplit(host);

  assert.equal(host.chatMain.dataset.ekdpSplit, "test.split");
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 1);
  const native = host.chatMain.querySelector(".ekdp-main-native")!;
  // The session host is gone, so the wrapper only holds the official column. If
  // it had been wrapped, the session CSS would hide the entire main area.
  assert.deepEqual(native.children, [host.chat, host.composer]);
  assert.equal(native.querySelectorAll(".ekd-session-host").length, 0);
  assert.equal(host.chatMain.querySelectorAll(".ekd-session-host").length, 0);
  assert.equal(host.chatMain.dataset.ekdContainment, "");

  split.dispose();

  // Exit leaves the official column exactly as the shell produced it.
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 0);
  assert.equal(host.chatMain.dataset.ekdpSplit, "");
  assert.equal(host.chatMain.style.display, undefined);
  assert.equal(host.chatMain.children.length, 2);
  assert.equal(host.chatMain.children[0]!.tagName, "DIV");
  assert.equal(host.chatMain.children[0]!.className, "chat");
  assert.equal(host.chatMain.children[1]!.className, "composer");
  assert.deepEqual(officialChildren(host), []);
});

test("plugin split then session mode shows the session host and restores the column", () => {
  const host = chatMain();
  const split = mountSplit(host);
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 1);
  const nestedNativeChildren = host.chatMain.querySelector(".ekdp-main-native")!.children.length;
  assert.equal(nestedNativeChildren, 2);

  // `mountSessions` disposes every plugin main area before it appends its host,
  // so the wrapper is already gone when the session host arrives.
  split.dispose();
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 0);
  assert.equal(host.chatMain.children.length, nestedNativeChildren);

  const sessionHost = enterSessionMode(host);
  assert.ok(host.chatMain.children.includes(sessionHost));
  // The host is a direct child of the column, which is what the session CSS
  // expects; nothing wraps it.
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 0);
  assert.equal(host.chatMain.dataset.ekdContainment, "sessions");

  leaveSessionMode(host, sessionHost);
  assert.equal(host.chatMain.querySelectorAll(".ekd-session-host").length, 0);
  assert.equal(host.chatMain.dataset.ekdContainment, "");
  assert.equal(host.chatMain.children.length, 2);
  assert.equal(host.chatMain.children[0]!.className, "chat");
  assert.equal(host.chatMain.children[1]!.className, "composer");
  assert.equal(host.chatMain.style.display, undefined);
});

test("a plugin split refuses to wrap a column that still hosts the session view", () => {
  const host = chatMain();
  const sessionHost = enterSessionMode(host);

  // Even if a caller skipped the renderer handoff, the split must not move the
  // session host into the row: that is exactly the blank-main-area bug.
  assert.throws(
    () => mountSplit(host),
    /主聊天区仍被同屏会话占用/,
  );
  assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 0);
  assert.ok(host.chatMain.children.includes(sessionHost));
  assert.equal(host.chatMain.dataset.ekdContainment, "sessions");
  // The official children were never moved, so the chat is still intact.
  assert.equal(host.chatMain.children[0]!.className, "chat");
  assert.equal(host.chatMain.children[1]!.className, "composer");
});

test("alternating session and split never leaves a wrapper or a session host behind", () => {
  const host = chatMain();
  for (let round = 0; round < 5; round += 1) {
    const sessionHost = enterSessionMode(host);
    leaveSessionMode(host, sessionHost);
    const split = mountSplit(host, `test.round${round}`);
    assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 1);
    assert.equal(host.chatMain.querySelectorAll(".ekd-session-host").length, 0);
    split.dispose();
    assert.equal(host.chatMain.querySelectorAll(".ekdp-main-split").length, 0);
    assert.equal(host.chatMain.children.length, 2);
  }
  assert.equal(host.chatMain.children[0]!.className, "chat");
  assert.equal(host.chatMain.children[1]!.className, "composer");
  assert.equal(host.chatMain.dataset.ekdpSplit, "");
  assert.equal(host.chatMain.dataset.ekdpPage, "");
  assert.equal(host.chatMain.dataset.ekdContainment, "");
});
