import fs from "node:fs";
import wsPackage from "/home/bot/node_modules/ws/index.js";

const { WebSocket } = wsPackage;
const outputPath = new URL("./final-dom-audit.json", import.meta.url);
const prototype = fs.readFileSync("/tmp/flatline-prototype-src.ko1X6F/Flatline.dc.html", "utf8") + "\n" +
  fs.readFileSync("/tmp/flatline-prototype-src.ko1X6F/atoms/flatline-atoms.css", "utf8");
// The prototype renders several icons through runtime bindings such as
// data-icon="{{ dynamic }}". Keep those concrete values in the audit set so
// a valid runtime icon is not reported as an unsupported custom icon merely
// because it is absent from the prototype's literal HTML attributes.
const runtimePrototypeIcons = [
  "activity", "archive", "arrow-left", "arrow-right", "bell-off", "book-open",
  "calendar", "camera", "chart-column", "check", "chevron-down", "chevron-right",
  "chevron-up", "circle-slash", "clock", "cpu", "eye-off", "file-code", "file-diff",
  "file-text", "folder", "git-commit-horizontal", "hash", "history", "hourglass",
  "layers", "list", "list-collapse", "package", "package-open", "power-off", "scale",
  "search", "shield-off", "slash", "trending-down", "triangle-alert", "unlink",
  "volume-x", "wallet", "webhook", "x", "rows-2", "rows-3"
];
const prototypeIcons = [...new Set([
  ...[...prototype.matchAll(/data-icon=\"([^\"]+)\"/g)].map((match) => match[1]),
  ...runtimePrototypeIcons
])].sort();
const routes = {
  wall: "#/",
  sessions: "#/sessions",
  sessionDetail: "#/sessions/claude_code%3A8548a8cb-7b54-4dca-9dc7-6d4f7cc9b58a",
  assetRelated: "#/assets/agents_md%3Aproject%3Aagents",
  assetNoOpportunity: "#/assets/agents_md%3Auser%3Aplugins%3Acache%3Acaveman%3Acaveman%3A84cc3c14fa1e",
  timeline: "#/timeline",
  stats: "#/stats",
  cleanup: "#/cleanup"
};

const targets = await (await fetch("http://127.0.0.1:9225/json/list")).json();
const target = targets.find((item) => item.type === "page" && item.webSocketDebuggerUrl);
if (!target) throw new Error("No Chrome page target");

let nextId = 1;
const pending = new Map();
const ws = new WebSocket(target.webSocketDebuggerUrl);
ws.on("message", (raw) => {
  const message = JSON.parse(String(raw));
  if (!message.id || !pending.has(message.id)) return;
  const record = pending.get(message.id);
  pending.delete(message.id);
  if (message.error) record.reject(new Error(message.error.message));
  else record.resolve(message.result);
});
await new Promise((resolve, reject) => { ws.once("open", resolve); ws.once("error", reject); });

function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = nextId++;
    const timeout = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`CDP timeout: ${method}`));
    }, 30000);
    pending.set(id, {
      resolve: (value) => { clearTimeout(timeout); resolve(value); },
      reject: (error) => { clearTimeout(timeout); reject(error); }
    });
    ws.send(JSON.stringify({ id, method, params }));
  });
}

async function evaluate(expression) {
  const result = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || "Runtime evaluation failed");
  return result.result?.value;
}

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function waitForScreen(route) {
  for (let attempt = 0; attempt < 1200; attempt += 1) {
    const ready = await evaluate("Boolean(document.querySelector('#flatline-screen') && !document.querySelector('.prototype-loading'))");
    const timelineReady = !route.includes("timeline") || await evaluate("Boolean(document.querySelector('.fl-track') || document.querySelector('.empty-copy'))");
    if (ready && timelineReady) {
      await sleep(route.includes("timeline") ? 900 : 350);
      return;
    }
    await sleep(100);
  }
  throw new Error(`render timeout: ${route}`);
}

async function navigate(route, locale, theme) {
  await send("Page.navigate", { url: `http://127.0.0.1:18899/${route}` });
  await sleep(100);
  await evaluate(`localStorage.setItem("flatline-locale", ${JSON.stringify(locale)}); localStorage.setItem("flatline-theme", ${JSON.stringify(theme)}); location.reload()`);
  await waitForScreen(route);
}

async function inspect() {
  return evaluate(`(() => {
    const rawSelectors = ".source-code,.event-payload,.fl-row-name,.session-title,.session-task-value,.event-title,.event-evidence,.session-human-title,.session-row-fact,.session-chat-preview,.session-chat-code,.session-chat-locator,.session-inspector-title,.detail-title-line h1,.detail-subline,.cleanup-table .asset-name";
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const chinese = [];
    let node;
    while ((node = walker.nextNode()) && chinese.length < 80) {
      const text = (node.nodeValue || "").replace(/\\s+/g, " ").trim();
      if (!text || !/[一-龥]/.test(text) || node.parentElement?.closest(rawSelectors) || node.parentElement?.closest('[data-action="locale"]')) continue;
    chinese.push({ text: text.slice(0, 140), tag: node.parentElement?.tagName || "", className: String(node.parentElement?.className || "").slice(0, 120) });
    }
    const icons = [...new Set([...document.querySelectorAll("[data-icon]")].map((node) => node.getAttribute("data-icon")).filter(Boolean))].sort();
    const scrollInfo = (node) => {
      if (!node) return null;
      const style = getComputedStyle(node);
      return {
        selector: node.className ? "." + String(node.className).split(/\\s+/).filter(Boolean).join(".") : node.tagName.toLowerCase(),
        height: node.scrollHeight,
        client: node.clientHeight,
        width: node.scrollWidth,
        clientWidth: node.clientWidth,
        overflowY: style.overflowY,
        overflowX: style.overflowX,
        scrollableY: node.scrollHeight > node.clientHeight + 1 && ["auto", "scroll"].includes(style.overflowY),
        scrollableX: node.scrollWidth > node.clientWidth + 1 && ["auto", "scroll"].includes(style.overflowX)
      };
    };
    const scrollNodes = [...document.querySelectorAll(".screen-scroll, .sidebar-scroll, .session-event-scroll, .session-inspector-scroll, .session-chat-scroll, .source-code, .event-payload, .modify-modal-body")].map(scrollInfo).filter(Boolean);
    const screenScroll = scrollNodes.find((node) => node.selector.includes("screen-scroll")) || null;
    const nestedScroll = scrollNodes.filter((node) => !node.selector.includes("screen-scroll") && (node.scrollableY || node.scrollableX));
    const rootStyle = getComputedStyle(document.documentElement);
    const css = {};
    for (const name of ["--primary", "--background", "--foreground", "--card", "--muted", "--border", "--verified", "--bypass", "--destructive", "--radius-md", "--radius-2xl", "--font-sans", "--font-heading", "--font-mono"]) css[name] = rootStyle.getPropertyValue(name).trim();
    return {
      lang: document.documentElement.lang,
      dark: document.documentElement.classList.contains("dark"),
      title: document.title,
      textLength: (document.body.innerText || "").length,
      chinese,
      icons,
      iconCount: document.querySelectorAll("[data-icon]").length,
      sourceBrands: [...new Set([...document.querySelectorAll(".source-brand img,.fl-mark img")].map((node) => node.getAttribute("src")))].sort(),
      scroll: {
        screen: screenScroll,
        nested: nestedScroll,
        available: Boolean((screenScroll && (screenScroll.scrollableY || screenScroll.scrollableX || screenScroll.height <= screenScroll.client + 1)) || nestedScroll.length)
      },
      css
    };
  })()`);
}

const report = { generatedAt: new Date().toISOString(), prototypeIcons, captures: [] };
for (const locale of ["en", "zh"]) {
  for (const theme of ["light", "dark"]) {
    for (const [name, route] of Object.entries(routes)) {
      await navigate(route, locale, theme);
      const page = await inspect();
      page.unsupportedIcons = page.icons.filter((name) => !prototypeIcons.includes(name));
      report.captures.push({ name, locale, theme, route, page });
      fs.writeFileSync(outputPath, JSON.stringify(report, null, 2));
      console.log(`${locale}/${theme}/${name}: ${page.textLength} chars, ${page.iconCount} icons, ${page.chinese.length} non-raw Chinese nodes`);
    }
  }
}
ws.close();
console.log(`saved ${outputPath.pathname}`);
