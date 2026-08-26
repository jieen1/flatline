import fs from "node:fs";
import path from "node:path";
import WebSocket from "/home/bot/node_modules/ws/index.js";

const browserURL = "http://127.0.0.1:9225/json/list";
const appURL = "http://127.0.0.1:18899/";
const prototypeURL = "file:///tmp/flatline-prototype-src.ko1X6F/Flatline.dc.html";
const outputDir = path.resolve("docs/qa/flatline-ui-v49/prototype-parity-v62");
const routes = {
  wall: "#/",
  sessions: "#/sessions",
  sessionDetail: "#/sessions/claude_code%3A8548a8cb-7b54-4dca-9dc7-6d4f7cc9b58a",
  assetDetail: "#/assets/agents_md%3Aproject%3Aagents",
  timeline: "#/timeline",
  stats: "#/stats",
  cleanup: "#/cleanup"
};
const currentReady = {
  wall: "Boolean(document.querySelector('.wall-page'))",
  sessions: "Boolean(document.querySelector('.session-page'))",
  sessionDetail: "Boolean(document.querySelector('.session-detail-page'))",
  assetDetail: "Boolean(document.querySelector('.evidence-card'))",
  timeline: "Boolean(document.querySelector('.timeline-page'))",
  stats: "Boolean(document.querySelector('.stats-card'))",
  cleanup: "Boolean(document.querySelector('.cleanup-page'))"
};
const themes = ["light", "dark"];
const tokenNames = [
  "background", "foreground", "card", "primary", "primary-foreground", "secondary", "secondary-foreground", "muted",
  "muted-foreground", "accent", "accent-foreground", "destructive", "destructive-foreground", "bypass", "border", "input",
  "ring", "control-accent", "verified", "sidebar", "sidebar-foreground", "sidebar-accent", "sidebar-border", "nav-fg",
  "nav-fg-muted", "nav-surface-hover", "nav-icon-idle", "panel-surface", "panel-surface-fg-muted", "panel-input-surface",
  "panel-surface-hover", "panel-slider-fg", "sidebar-width"
];
const prototypeIcons = [
  "activity", "archive", "arrow-left", "arrow-right", "bell-off", "book-open", "calendar", "camera", "chart-column", "check",
  "chevron-down", "chevron-right", "chevron-up", "circle-slash", "clock", "cpu", "eye-off", "file-code", "file-diff", "file-text", "folder",
  "git-commit-horizontal", "hash", "history", "hourglass", "layers", "list", "list-collapse", "package", "package-open",
  "power-off", "scale", "search", "shield-off", "slash", "trending-down", "triangle-alert", "unlink", "volume-x", "wallet", "webhook", "x",
  "rows-2", "rows-3"
];

fs.mkdirSync(outputDir, { recursive: true });

const pages = await (await fetch(browserURL)).json();
const target = pages.find((page) => page.type === "page" && page.webSocketDebuggerUrl);
if (!target) throw new Error("No browser page target is available");

const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.once("open", resolve);
  ws.once("error", reject);
});

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

function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = ++nextID;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
}

async function evaluate(expression) {
  const result = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || "Runtime.evaluate failed");
  return result.result?.value;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(selectorExpression, timeout = 30000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (await evaluate(selectorExpression)) return;
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${selectorExpression}`);
}

async function navigate(url) {
  await send("Page.navigate", { url });
  await sleep(250);
}

async function setCurrentPreferences(locale, theme) {
  await evaluate(`localStorage.setItem("flatline-locale", ${JSON.stringify(locale)}); localStorage.setItem("flatline-theme", ${JSON.stringify(theme)}); location.reload()`);
  await waitFor("Boolean(document.querySelector('#flatline-screen') && !document.querySelector('.prototype-loading'))");
  await sleep(500);
}

async function clickPrototypeText(text) {
  const started = Date.now();
  while (Date.now() - started < 10000) {
    const clicked = await evaluate(`(() => {
      const node = [...document.querySelectorAll("button")].find((item) => item.textContent.trim() === ${JSON.stringify(text)});
      if (!node) return false;
      node.click();
      return true;
    })()`);
    if (clicked) {
      await sleep(350);
      return;
    }
    await sleep(100);
  }
  throw new Error(`Prototype button not found: ${text}`);
}

async function inspect(label, source, theme) {
  return evaluate(`(() => {
    const rect = (node) => node ? (() => { const box = node.getBoundingClientRect(); return { x: box.x, y: box.y, width: box.width, height: box.height }; })() : null;
    const first = (selectors) => selectors.map((selector) => document.querySelector(selector)).find(Boolean);
    const root = document.documentElement;
    const styles = getComputedStyle(root);
    const iconNames = [...new Set([...document.querySelectorAll("[data-icon]")].map((node) => node.getAttribute("data-icon")).filter(Boolean))].sort();
    const iconPaths = Object.fromEntries([...document.querySelectorAll("[data-icon]")]
      .filter((node) => node.querySelector("svg"))
      .map((node) => [node.getAttribute("data-icon"), node.querySelector("svg").innerHTML])
      .filter(([name], index, entries) => entries.findIndex(([candidate]) => candidate === name) === index));
    const scrollNodes = [...document.querySelectorAll("*" )].filter((node) => {
      const style = getComputedStyle(node);
      return (style.overflowY === "auto" || style.overflowY === "scroll") && node.scrollHeight > node.clientHeight;
    }).slice(0, 12).map((node) => ({ className: String(node.className || ""), clientHeight: node.clientHeight, scrollHeight: node.scrollHeight }));
    const counts = (selectors) => Object.fromEntries(selectors.map((selector) => [selector, document.querySelectorAll(selector).length]));
    return {
      label: ${JSON.stringify(label)}, source: ${JSON.stringify(source)}, theme: ${JSON.stringify(theme)},
      viewport: {
        width: innerWidth,
        height: innerHeight,
        documentHeight: document.documentElement.scrollHeight,
        pageOverflowY: getComputedStyle(document.documentElement).overflowY,
        bodyOverflowY: getComputedStyle(document.body).overflowY
      },
      tokens: Object.fromEntries(${JSON.stringify(tokenNames)}.map((name) => ["--" + name, styles.getPropertyValue("--" + name).trim()])),
      iconNames,
      iconPaths,
      unsupportedIcons: iconNames.filter((name) => !${JSON.stringify(prototypeIcons)}.includes(name)),
      counts: counts([".fl-row", ".elevated-card", ".fl-spark", ".fl-funnel", ".fl-list", ".fl-node", ".fl-tbtn", ".session-ledger-row", ".session-chat-row", ".stats-card", ".heat-cell"]),
      rects: {
        sidebar: rect(first([".us-sidebar", "aside"])),
        main: rect(first([".prototype-main", "main"])),
        header: rect(first([".screen-header", ".detail-header", "header"])),
        scroll: rect(first([".screen-scroll", ".fl-scroll"])),
        firstRow: rect(first([".fl-row", ".session-row", ".session-ledger-row"])),
        firstCard: rect(first([".elevated-card", ".stats-card"]))
      },
      scrollNodes,
      text: (document.body.innerText || "").slice(0, 1600)
    };
  })()`);
}

async function capture(fileName) {
  const image = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  fs.writeFileSync(path.join(outputDir, fileName), Buffer.from(image.data, "base64"));
}

const report = { generatedAt: new Date().toISOString(), prototype: [], current: [] };

for (const theme of themes) {
  await navigate(prototypeURL);
  await evaluate(`document.documentElement.classList.toggle("dark", ${JSON.stringify(theme)} === "dark")`);
  await sleep(250);
  await waitFor("Boolean(document.querySelector('.us-sidebar'))");

  await capture(`prototype-wall-${theme}.png`);
  report.prototype.push(await inspect("wall", "prototype", theme));

  await clickPrototypeText("会话");
  await capture(`prototype-sessions-${theme}.png`);
  report.prototype.push(await inspect("sessions", "prototype", theme));

  const openedSession = await evaluate(`(() => { const row = document.querySelector(".fl-row"); if (!row) return false; row.click(); return true; })()`);
  if (openedSession) {
    await sleep(350);
    await capture(`prototype-session-detail-${theme}.png`);
    report.prototype.push(await inspect("sessionDetail", "prototype", theme));
  }

  await navigate(prototypeURL);
  await evaluate(`document.documentElement.classList.toggle("dark", ${JSON.stringify(theme)} === "dark")`);
  await sleep(250);
  await clickPrototypeText("变化时间线");
  await capture(`prototype-timeline-${theme}.png`);
  report.prototype.push(await inspect("timeline", "prototype", theme));

  await clickPrototypeText("统计");
  await capture(`prototype-stats-${theme}.png`);
  report.prototype.push(await inspect("stats", "prototype", theme));

  await navigate(prototypeURL);
  await evaluate(`document.documentElement.classList.toggle("dark", ${JSON.stringify(theme)} === "dark")`);
  await sleep(250);
  await clickPrototypeText("资产");
  const openedAsset = await evaluate(`(() => { const row = document.querySelector(".fl-row"); if (!row) return false; row.click(); return true; })()`);
  if (openedAsset) {
    await sleep(350);
    await capture(`prototype-asset-detail-${theme}.png`);
    report.prototype.push(await inspect("assetDetail", "prototype", theme));
  }

  await navigate(appURL);
  await setCurrentPreferences("zh", theme);
  for (const [label, route] of Object.entries(routes)) {
    await evaluate(`location.hash = ${JSON.stringify(route)}`);
    await waitFor(currentReady[label]);
    await sleep(label === "sessionDetail" ? 900 : 350);
    await capture(`current-${label}-${theme}.png`);
    report.current.push(await inspect(label, "current", theme));
  }
}

const summary = {
  prototypeScreens: report.prototype.length,
  currentScreens: report.current.length,
  prototypeUnsupportedIcons: [...new Set(report.prototype.flatMap((item) => item.unsupportedIcons))],
  currentUnsupportedIcons: [...new Set(report.current.flatMap((item) => item.unsupportedIcons))],
  iconPathMismatches: [...new Set(report.current.map((current) => {
    const prototype = report.prototype.find((item) => item.label === current.label && item.theme === current.theme);
    if (!prototype) return [];
    return Object.keys(current.iconPaths).filter((name) => prototype.iconPaths[name] && prototype.iconPaths[name] !== current.iconPaths[name]);
  }).flat())],
  currentScrollFailures: report.current
    .filter((item) => item.scrollNodes.length === 0
      && item.viewport.documentHeight > item.viewport.height + 1
      && item.viewport.pageOverflowY === "hidden"
      && item.viewport.bodyOverflowY === "hidden")
    .map((item) => `${item.label}:${item.theme}`),
  routeCounts: Object.fromEntries(Object.keys(routes).map((route) => [route, {
    prototype: report.prototype.filter((item) => item.label === route).map((item) => item.counts),
    current: report.current.filter((item) => item.label === route).map((item) => item.counts)
  }]))
};

fs.writeFileSync(path.join(outputDir, "report.json"), JSON.stringify(report, null, 2));
fs.writeFileSync(path.join(outputDir, "summary.json"), JSON.stringify(summary, null, 2));
await navigate(appURL + routes.wall);
await sleep(350);
ws.close();
console.log(JSON.stringify(summary, null, 2));
