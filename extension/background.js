// Tunas domain tracker (service worker). It reports ONE thing to the agent
// running on this laptop: the hostname of the active tab of the focused
// browser window, e.g. "youtube.com". Never a path, query, title or content.
import { hostFromUrl } from "./host.js";

const DEFAULTS = { port: 47821, token: "" };
let last = { host: null, at: 0 };

async function settings() {
  const s = await chrome.storage.local.get(DEFAULTS);
  return { port: Number(s.port) || DEFAULTS.port, token: String(s.token || "") };
}

async function send(host) {
  const now = Date.now();
  // a repeated host is only re-sent now and then (the agent treats silence as stale)
  if (host === last.host && now - last.at < 10000) return;
  const { port, token } = await settings();
  if (!token) return;
  try {
    const res = await fetch(`http://127.0.0.1:${port}/v1/tab`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify({ host, ts: Math.floor(now / 1000) }),
    });
    if (res.ok) last = { host, at: now };
  } catch {
    // the agent is not running: nothing to do
  }
}

// The active tab of the focused window, or "" when there is nothing to report
// (no focus, a private window, a browser page).
async function report() {
  let win;
  try {
    win = await chrome.windows.getLastFocused({ populate: true });
  } catch {
    return send("");
  }
  if (!win.focused || win.incognito) return send("");
  const tab = (win.tabs || []).find((t) => t.active);
  if (!tab || tab.incognito) return send("");
  return send(hostFromUrl(tab.url));
}

chrome.tabs.onActivated.addListener(report);
chrome.tabs.onUpdated.addListener((_id, change, tab) => {
  if (tab.active && (change.url || change.status === "complete")) report();
});
chrome.windows.onFocusChanged.addListener(report);
chrome.runtime.onStartup.addListener(report);
chrome.runtime.onInstalled.addListener(report);
chrome.storage.onChanged.addListener(() => {
  last = { host: null, at: 0 };
  report();
});
