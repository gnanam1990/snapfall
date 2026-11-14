import assert from 'node:assert/strict';
import test from 'node:test';

import {
  OWNER_SESSION_CHANNEL,
  OWNER_SESSION_EVENT,
  announceOwnerSessionChange,
  listenForOwnerSessionChanges,
  resetOwnerSessionChannelForTests,
} from './ownerSessionClient';

/** This "tab": a window that counts OWNER_SESSION_EVENT dispatches. */
function installWindow() {
  const win = new EventTarget();
  let heard = 0;
  win.addEventListener(OWNER_SESSION_EVENT, () => heard++);
  (globalThis as { window?: unknown }).window = win;
  return { heard: () => heard };
}

const tick = (ms = 30) => new Promise((resolve) => setTimeout(resolve, ms));

test('a sign-out in another tab reaches this tab as an owner-session event', async () => {
  resetOwnerSessionChannelForTests();
  const tab = installWindow();
  const otherTab = new BroadcastChannel(OWNER_SESSION_CHANNEL);
  try {
    listenForOwnerSessionChanges(); // what every stream subscriber does on mount
    otherTab.postMessage({ kind: 'owner-session-changed' });
    await tick();
    assert.equal(tab.heard(), 1, 'the open stream in this tab is told to reconnect through the guard');
  } finally {
    otherTab.close();
    resetOwnerSessionChannelForTests();
    delete (globalThis as { window?: unknown }).window;
  }
});

test('announcing a change tells this tab once and every other tab once', async () => {
  resetOwnerSessionChannelForTests();
  const tab = installWindow();
  const otherTab = new BroadcastChannel(OWNER_SESSION_CHANNEL);
  let otherHeard = 0;
  otherTab.onmessage = () => otherHeard++;
  try {
    listenForOwnerSessionChanges();
    announceOwnerSessionChange();
    await tick();
    assert.equal(tab.heard(), 1, 'no echo: this tab does not also hear its own broadcast');
    assert.equal(otherHeard, 1);
  } finally {
    otherTab.close();
    resetOwnerSessionChannelForTests();
    delete (globalThis as { window?: unknown }).window;
  }
});

test('without BroadcastChannel the same-tab notice still works', async () => {
  resetOwnerSessionChannelForTests();
  const tab = installWindow();
  const saved = globalThis.BroadcastChannel;
  (globalThis as { BroadcastChannel?: unknown }).BroadcastChannel = undefined;
  try {
    listenForOwnerSessionChanges();
    announceOwnerSessionChange();
    assert.equal(tab.heard(), 1);
  } finally {
    globalThis.BroadcastChannel = saved;
    resetOwnerSessionChannelForTests();
    delete (globalThis as { window?: unknown }).window;
  }
});
