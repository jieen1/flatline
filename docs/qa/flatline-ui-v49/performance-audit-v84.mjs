#!/usr/bin/env node

import fs from "node:fs/promises";
import WebSocket from "/home/bot/node_modules/ws/index.js";

const baseURL = "http://127.0.0.1:18899";
const outputPath = "docs/qa/flatline-ui-v49/performance-audit-v88.json";

const targets = await (await fetch("http://127.0.0.1:9225/json/list")).json();
const target = targets.find((item) => item.type === "page" && item.webSocketDebuggerUrl);
if (!target) throw new Error("No Chrome page target");
const ws = new WebSocket(target.webSocketDebuggerUrl);
let nextID = 1;
const pending = new Map();
const runtimeErrors = [];
ws.on("message", (raw) => {
  const message = JSON.parse(String(raw));
  if (message.method === "Runtime.exceptionThrown") runtimeErrors.push(message.params?.exceptionDetails?.text || "Runtime exception");
  const record = pending.get(message.id);
  if (!record) return;
  pending.delete(message.id);
  if (message.error) record.reject(new Error(message.error.message));
  else record.resolve(message.result);
});
await new Promise((resolve, reject) => { ws.once("open", resolve); ws.once("error", reject); });

function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = nextID++;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
}
async function evaluate(expression) {
  const result = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || "Runtime evaluation failed");
  return result.result?.value;
}
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function waitFor(expression, timeout = 60000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return;
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${expression}`);
}
async function navigate(hash, readyExpression) {
  const started = performance.now();
  // Hash-only navigation reuses the SPA's in-memory cache and would measure
  // the previous route rather than a fresh page. The nonce forces a document
  // reload while leaving the application hash unchanged.
  await send("Page.navigate", { url: `${baseURL}/?perf=${Date.now()}${hash}` });
  await waitFor(readyExpression);
  await sleep(150);
  return { wallClockMs: Math.round(performance.now() - started) };
}
async function fetchMeasure(path) {
  return evaluate(`(async () => {
    const started = performance.now();
    const response = await fetch(${JSON.stringify(path)}, { cache: "no-store" });
    const text = await response.text();
    return { status: response.status, bytes: text.length, elapsedMs: Math.round(performance.now() - started) };
  })()`);
}
async function pageMetrics() {
  return evaluate(`(() => {
    const first = document.querySelector(".fl-row");
    const navigation = performance.getEntriesByType("navigation")[0];
    const scroll = document.querySelector(".wall-page")?.closest(".screen-scroll");
    return {
      route: location.hash,
      htmlBytes: document.documentElement.outerHTML.length,
      bodyTextBytes: document.body.innerText.length,
      domNodes: document.querySelectorAll("*").length,
      rows: document.querySelectorAll(".fl-row").length,
      sparklines: document.querySelectorAll(".fl-spark").length,
      firstRowHTMLBytes: first ? first.outerHTML.length : 0,
      scrollHeight: document.documentElement.scrollHeight,
      wallScroll: scroll ? { scrollHeight: scroll.scrollHeight, clientHeight: scroll.clientHeight, scrollTop: scroll.scrollTop } : null,
      lazySentinels: document.querySelectorAll("[data-wall-sentinel]").length,
      sessionLazySentinels: document.querySelectorAll("[data-session-sentinel]").length,
      sessionPageSentinels: document.querySelectorAll("[data-session-page-sentinel]").length,
      sessionRows: document.querySelectorAll(".session-ledger-row").length,
      sessionChatRows: document.querySelectorAll(".session-chat-row").length,
      sessionLoaded: document.querySelector(".session-overview-count")?.dataset.loaded || null,
      sessionTotal: document.querySelector(".session-overview-count")?.dataset.total || null,
      navigation: navigation ? { responseEnd: Math.round(navigation.responseEnd), domInteractive: Math.round(navigation.domInteractive), loadEventEnd: Math.round(navigation.loadEventEnd) } : null
    };
  })()`);
}

await send("Runtime.enable");
await send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
const api = {
  wall: await fetchMeasure("/api/v1/assets?view=wall&limit=5000"),
  fullAssets: await fetchMeasure("/api/v1/assets?limit=5000"),
  sessions: await fetchMeasure("/api/v1/sessions?limit=5000"),
  timeline: await fetchMeasure("/api/v1/timeline?limit=5000")
};

const wallNavigation = await navigate("#/", "Boolean(document.querySelector('.wall-page') && document.querySelectorAll('.fl-row').length > 0)");
const wall = await pageMetrics();
const wallAssetCount = (await (await fetch(`${baseURL}/api/v1/assets?view=wall&limit=5000`)).json()).assets.length;
let wallDrain = null;
for (let attempt = 0; attempt < 240; attempt += 1) {
  wallDrain = await evaluate(`(() => {
    const scroll = document.querySelector('.wall-page')?.closest('.screen-scroll');
    if (!scroll) return null;
    scroll.scrollTop = Math.min(scroll.scrollHeight, scroll.scrollTop + Math.max(420, scroll.clientHeight * 0.8));
    return { rows: document.querySelectorAll('.fl-row').length, scrollHeight: scroll.scrollHeight, scrollTop: scroll.scrollTop };
  })()`);
  if (wallDrain && wallDrain.rows >= wallAssetCount) break;
  await sleep(35);
}
const wallAfterDrain = await pageMetrics();
const sessions = await (await fetch(`${baseURL}/api/v1/sessions?limit=5000`)).json();
const selected = [...(sessions.sessions || [])].filter((item) => (item.event_count || 0) >= 100).sort((a, b) => (b.event_count || 0) - (a.event_count || 0))[0];
if (!selected) throw new Error("No persisted session available for performance audit");
const sessionPageAPI = await fetchMeasure(`/api/v1/sessions/${encodeURIComponent(selected.id)}?events=page&offset=0&limit=1000`);

const sessionNavigation = await navigate(`#/sessions/${encodeURIComponent(selected.id)}`, "Boolean(document.querySelector('.session-detail-page'))");
const sessionInitial = await pageMetrics();
await evaluate(`document.querySelector('[data-action="session-tab"][data-tab="chat"]')?.click()`);
await sleep(120);
const sessionChat = await pageMetrics();
await evaluate(`document.querySelector('[data-action="session-tab"][data-tab="trajectory"]')?.click()`);
await sleep(120);
const sessionInteractions = await evaluate(`(() => {
  const measure = (selector) => {
    const started = performance.now();
    const node = document.querySelector(selector);
    if (!node) return { elapsedMs: null, found: false };
    node.click();
    return { elapsedMs: Math.round(performance.now() - started), found: true };
  };
  const chat = measure('[data-action="session-tab"][data-tab="chat"]');
  const trajectory = measure('[data-action="session-tab"][data-tab="trajectory"]');
  const event = measure('.session-ledger-row[data-event-index="3"]');
  return {
    chat,
    trajectory,
    event,
    chatRows: document.querySelectorAll('.session-chat-row').length,
    ledgerRows: document.querySelectorAll('.session-ledger-row').length,
    selectedEvent: document.querySelector('.session-ledger-row[data-selected="true"]')?.dataset.eventIndex || null
  };
})()`);
const sessionAfter = await pageMetrics();
const sessionBeforePaging = await pageMetrics();
let sessionPaging = null;
for (let attempt = 0; attempt < 80; attempt += 1) {
  await evaluate(`(() => {
    const scroll = document.querySelector('.session-event-scroll, .session-chat-scroll');
    if (scroll) scroll.scrollTop = scroll.scrollHeight;
  })()`);
  await sleep(100);
  sessionPaging = await pageMetrics();
  if (Number(sessionPaging.sessionLoaded || 0) > 1000 || sessionPaging.sessionPageSentinels === 0) break;
}
const sessionAfterPaging = await pageMetrics();

const timelineNavigation = await navigate("#/timeline", "Boolean(document.querySelector('.timeline-page'))");
const timeline = await pageMetrics();

const report = {
  generatedAt: new Date().toISOString(),
  source: "daemon_owned_sqlite",
  viewport: { width: 1440, height: 900 },
  api,
  wall: { assetCount: wallAssetCount, navigation: wallNavigation, metrics: wall, afterScrollDrain: wallAfterDrain },
  session: { idDigest: String(selected.id).slice(0, 12), eventCount: selected.event_count, pageAPI: sessionPageAPI, navigation: sessionNavigation, initial: sessionInitial, chat: sessionChat, interactions: sessionInteractions, after: sessionAfter, beforePaging: sessionBeforePaging, paging: sessionPaging, afterPaging: sessionAfterPaging },
  timeline: { navigation: timelineNavigation, metrics: timeline }
};
report.runtimeErrors = runtimeErrors;
await fs.writeFile(outputPath, JSON.stringify(report, null, 2) + "\n");
console.log(JSON.stringify(report, null, 2));
ws.close();
