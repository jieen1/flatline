import fs from "node:fs";
import path from "node:path";
import WebSocket from "/home/bot/node_modules/ws/index.js";

const baseURL = "http://127.0.0.1:18899";
const prototypeURL = "file:///tmp/flatline-prototype-src.ko1X6F/Flatline.dc.html";
const outputDir = path.resolve("docs/qa/flatline-ui-v49/geometry-v83");
fs.mkdirSync(outputDir, { recursive: true });

const [assetData, sessionData] = await Promise.all([
  fetch(`${baseURL}/api/v1/assets?limit=5000`).then((response) => response.json()),
  fetch(`${baseURL}/api/v1/sessions?limit=5000`).then((response) => response.json())
]);
const relatedAsset = (assetData.assets || []).find((item) => item.kind === "skill" && (item.facts?.opportunity_count || 0) > 0)
  || (assetData.assets || []).find((item) => item.facts?.opportunity_count > 0)
  || (assetData.assets || [])[0];
const noOpportunityAsset = (assetData.assets || []).find((item) => item.facts?.opportunity_count === 0) || (assetData.assets || [])[0];
const session = (sessionData.sessions || []).find((item) => (item.event_count || 0) >= 100 && (item.event_count || 0) <= 3000)
  || (sessionData.sessions || [])[0];

const targets = await (await fetch("http://127.0.0.1:9225/json/list")).json();
const target = targets.find((item) => item.type === "page" && item.webSocketDebuggerUrl);
if (!target) throw new Error("No Chrome page target");
const ws = new WebSocket(target.webSocketDebuggerUrl);
let nextID = 0;
const pending = new Map();
ws.on("message", (raw) => {
  const message = JSON.parse(String(raw));
  const request = pending.get(message.id);
  if (!request) return;
  pending.delete(message.id);
  if (message.error) request.reject(new Error(message.error.message));
  else request.resolve(message.result);
});
await new Promise((resolve, reject) => { ws.once("open", resolve); ws.once("error", reject); });

function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = ++nextID;
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
async function waitFor(expression, timeout = 30000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return;
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${expression}`);
}
async function navigate(url) {
  await send("Page.navigate", { url });
  await sleep(250);
}
async function currentPreferences(theme = "light") {
  await evaluate(`localStorage.setItem("flatline-locale", "zh"); localStorage.setItem("flatline-theme", ${JSON.stringify(theme)}); location.reload()`);
  await waitFor("Boolean(document.querySelector('#flatline-screen') && !document.querySelector('.prototype-loading'))");
  await sleep(350);
}
async function clickText(text) {
  const clicked = await evaluate(`(() => { const node = [...document.querySelectorAll("button,a")].find((item) => item.textContent.trim() === ${JSON.stringify(text)}); if (!node) return false; node.click(); return true; })()`);
  if (!clicked) throw new Error(`Could not click prototype control: ${text}`);
  await sleep(350);
}
async function capture(name) {
  const screenshot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  fs.writeFileSync(path.join(outputDir, name), Buffer.from(screenshot.data, "base64"));
}
async function inspect(label, source, theme) {
  return evaluate(`(() => {
    const rect = (selector) => { const node = document.querySelector(selector); if (!node) return null; const box = node.getBoundingClientRect(); return { x: +box.x.toFixed(2), y: +box.y.toFixed(2), width: +box.width.toFixed(2), height: +box.height.toFixed(2) }; };
    const style = (selector) => { const node = document.querySelector(selector); if (!node) return null; const css = getComputedStyle(node); return { display: css.display, gap: css.gap, padding: css.padding, minHeight: css.minHeight, borderRadius: css.borderRadius, fontSize: css.fontSize, lineHeight: css.lineHeight, overflowY: css.overflowY }; };
    const scrolls = [...document.querySelectorAll("*")].filter((node) => { const css = getComputedStyle(node); return ["auto", "scroll"].includes(css.overflowY) && node.scrollHeight > node.clientHeight; }).slice(0, 10).map((node) => ({ className: String(node.className || ""), clientHeight: node.clientHeight, scrollHeight: node.scrollHeight, overflowY: getComputedStyle(node).overflowY }));
    return {
      label: ${JSON.stringify(label)}, source: ${JSON.stringify(source)}, theme: ${JSON.stringify(theme)},
      viewport: { width: innerWidth, height: innerHeight, htmlOverflowY: getComputedStyle(document.documentElement).overflowY, bodyOverflowY: getComputedStyle(document.body).overflowY },
      rects: {
        shell: rect(".prototype-shell"), sidebar: rect(".us-sidebar"), main: rect(".prototype-main"), header: rect(".screen-header, .detail-header"), scroll: rect(".screen-scroll, .fl-scroll"), content: rect(".screen-content"),
        row: rect(".fl-row"), state: rect(".fl-state"), mark: rect(".fl-mark"), spark: rect(".fl-spark"), card: rect(".elevated-card"), head: rect(".fl-head"), funnel: rect(".fl-funnel"), legend: rect(".fl-legend"), toolbar: rect(".fl-tbtn"), canvas: rect(".session-detail-canvas"), inspector: rect(".session-inspector-pane")
      },
      styles: {
        row: style(".fl-row"), state: style(".fl-state"), spark: style(".fl-spark"), card: style(".elevated-card"), head: style(".fl-head"), toolbar: style(".fl-tbtn"), inspector: style(".session-inspector-pane")
      },
      scrolls,
      counts: { rows: document.querySelectorAll(".fl-row").length, cards: document.querySelectorAll(".elevated-card").length, sparks: document.querySelectorAll(".fl-spark").length, funnels: document.querySelectorAll(".fl-funnel").length, nodes: document.querySelectorAll(".fl-node").length, toolbar: document.querySelectorAll(".fl-tbtn").length }
    };
  })()`);
}

await send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
const report = { generatedAt: new Date().toISOString(), viewport: { width: 1440, height: 900 }, assetIDs: { related: relatedAsset?.id, noOpportunity: noOpportunityAsset?.id }, sessionID: session?.id, prototype: [], current: [] };

await navigate(prototypeURL);
await waitFor("Boolean(document.querySelector('.us-sidebar'))");
await capture("prototype-wall-light.png");
report.prototype.push(await inspect("wall", "prototype", "light"));
await clickText("会话");
report.prototype.push(await inspect("sessions", "prototype", "light"));
await evaluate("document.querySelector('.fl-row')?.click()");
await sleep(400);
report.prototype.push(await inspect("sessionDetail", "prototype", "light"));
await navigate(prototypeURL);
await waitFor("Boolean(document.querySelector('.us-sidebar'))");
await clickText("资产");
await evaluate("document.querySelector('.fl-row')?.click()");
await sleep(400);
report.prototype.push(await inspect("assetDetail", "prototype", "light"));
await clickText("统计");
report.prototype.push(await inspect("stats", "prototype", "light"));

await navigate(`${baseURL}/`);
await currentPreferences("light");
for (const [label, route, ready] of [
  ["wall", "#/?scope=all", ".wall-page"],
  ["sessions", "#/sessions", ".session-page"],
  ["sessionDetail", `#/sessions/${encodeURIComponent(session.id)}`, ".session-detail-page"],
  ["assetDetail", `#/assets/${encodeURIComponent(relatedAsset.id)}`, ".evidence-card"],
  ["assetNoOpportunity", `#/assets/${encodeURIComponent(noOpportunityAsset.id)}`, ".evidence-card"],
  ["stats", "#/stats", ".stats-card"]
]) {
  await evaluate(`location.hash = ${JSON.stringify(route)}`);
  await waitFor(`Boolean(document.querySelector(${JSON.stringify(ready)}))`, label === "sessionDetail" ? 60000 : 30000);
  await sleep(label === "sessionDetail" ? 900 : 350);
  await capture(`current-${label}-light.png`);
  report.current.push(await inspect(label, "current", "light"));
}

const byKey = (items) => Object.fromEntries(items.map((item) => [`${item.label}:${item.theme}`, item]));
const proto = byKey(report.prototype);
const current = byKey(report.current);
const rectDiff = (a, b, key) => a?.rects?.[key] && b?.rects?.[key] ? Object.fromEntries(["x", "y", "width", "height"].map((field) => [field, +(b.rects[key][field] - a.rects[key][field]).toFixed(2)])) : null;
const comparisons = ["wall", "sessions", "sessionDetail", "assetDetail", "stats"].map((label) => ({ label, rects: Object.fromEntries(["shell", "sidebar", "main", "header", "content", "row", "state", "mark", "spark", "card", "head", "funnel", "legend", "toolbar", "canvas", "inspector"].map((key) => [key, rectDiff(proto[`${label}:light`], current[`${label}:light`], key)])) }));
report.comparisons = comparisons;
report.scrollFailures = report.current.filter((item) => item.scrolls.length === 0 && item.viewport.htmlOverflowY === "hidden" && item.viewport.bodyOverflowY === "hidden" && item.label !== "assetNoOpportunity").map((item) => item.label);
fs.writeFileSync(path.join(outputDir, "report.json"), JSON.stringify(report, null, 2) + "\n");
await navigate(`${baseURL}/#/`);
ws.close();
console.log(JSON.stringify({ output: path.join(outputDir, "report.json"), scrollFailures: report.scrollFailures, comparisons }, null, 2));
