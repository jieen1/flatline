#!/usr/bin/env node

import fs from "node:fs/promises";
import WebSocket from "/home/bot/node_modules/ws/index.js";

const baseURL = "http://127.0.0.1:18899";
const outputDir = "docs/qa/flatline-ui-v49/visual-v87";
await fs.mkdir(outputDir, { recursive: true });
const targets = await (await fetch("http://127.0.0.1:9225/json/list")).json();
const target = targets.find((item) => item.type === "page" && item.webSocketDebuggerUrl);
if (!target) throw new Error("No Chrome page target");
const ws = new WebSocket(target.webSocketDebuggerUrl);
let nextID = 1;
const pending = new Map();
ws.on("message", (raw) => {
  const message = JSON.parse(String(raw));
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
async function capture(name, hash, ready) {
  await send("Page.navigate", { url: `${baseURL}/?capture=87-${Date.now()}${hash}` });
  await waitFor("Boolean(document.querySelector('.prototype-shell'))");
  await evaluate('localStorage.setItem("flatline-locale", "zh"); localStorage.setItem("flatline-theme", "light"); location.reload()');
  await waitFor(ready);
  await sleep(700);
  const screenshot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  await fs.writeFile(`${outputDir}/${name}.png`, Buffer.from(screenshot.data, "base64"));
  return evaluate(`({ route: location.hash, nodes: document.querySelectorAll("*").length, bodyText: document.body.innerText.length, html: document.documentElement.outerHTML.length })`);
}

const sessions = await (await fetch(`${baseURL}/api/v1/sessions?limit=5000`)).json();
const selected = [...(sessions.sessions || [])].filter((item) => (item.event_count || 0) >= 100).sort((a, b) => (b.event_count || 0) - (a.event_count || 0))[0];
if (!selected) throw new Error("No persisted session available");
const results = {};
results.wall = await capture("wall-zh-light", "#/", "Boolean(document.querySelector('.wall-page') && document.querySelectorAll('.fl-row').length > 0)");
results.sessionDetail = await capture(`session-detail-zh-light`, `#/sessions/${encodeURIComponent(selected.id)}`, "Boolean(document.querySelector('.session-detail-page') && document.querySelectorAll('.session-ledger-row').length > 0)");
results.timeline = await capture("timeline-zh-light", "#/timeline", "Boolean(document.querySelector('.timeline-page') && document.querySelector('.fl-node'))");
results.stats = await capture("stats-zh-light", "#/stats", "Boolean(document.querySelector('.stats-page') || document.querySelector('.stats-grid'))");
await fs.writeFile(`${outputDir}/manifest.json`, JSON.stringify({ generatedAt: new Date().toISOString(), viewport: { width: 1440, height: 900 }, selectedSession: { idDigest: selected.id.slice(0, 12), eventCount: selected.event_count }, results }, null, 2) + "\n");
console.log(JSON.stringify({ outputDir, results }, null, 2));
ws.close();
