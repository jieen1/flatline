#!/usr/bin/env node

import fs from "node:fs/promises";
import WebSocket from "/home/bot/node_modules/ws/index.js";

const baseURL = "http://127.0.0.1:18899";
const outputPath = "docs/qa/flatline-ui-v49/interaction-audit-v62.json";
const sessions = await (await fetch(`${baseURL}/api/v1/sessions?limit=5000`)).json();
const selected = (sessions.sessions || []).find((item) => item.id === "codex:019df670-9adb-74c3-91f8-46d3d61e70fc") || [...(sessions.sessions || [])].filter((item) => (item.event_count || 0) >= 100 && (item.event_count || 0) <= 3000).sort((a, b) => (b.event_count || 0) - (a.event_count || 0))[0];
if (!selected) throw new Error("No persisted session available for interaction audit");

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
async function waitFor(expression) {
  for (let attempt = 0; attempt < 300; attempt += 1) {
    if (await evaluate(expression)) return;
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${expression}`);
}
async function click(selector) {
  const clicked = await evaluate(`(() => { const node = document.querySelector(${JSON.stringify(selector)}); if (!node) return false; node.click(); return true; })()`);
  if (!clicked) throw new Error(`Missing clickable selector: ${selector}`);
}
async function snapshot(label) {
  return { label, ...(await evaluate(`(() => {
    const button = (mode) => document.querySelector('[data-action="session-mode"][data-mode="' + mode + '"]');
    const active = (action) => document.querySelector('[data-action="' + action + '"][data-active="true"]');
    const scroll = (selector) => { const node = document.querySelector(selector); if (!node) return null; return { scrollHeight: node.scrollHeight, clientHeight: node.clientHeight, scrollWidth: node.scrollWidth, clientWidth: node.clientWidth, overflowY: getComputedStyle(node).overflowY }; };
    const selectedRow = document.querySelector('.session-ledger-row[data-selected="true"]');
    return {
      route: location.hash,
      trajectory: Boolean(document.querySelector('.session-detail-overview')),
      chat: Boolean(document.querySelector('.session-chat-pane')),
      activeSessionTab: active('session-tab')?.dataset.tab || null,
      activeInspectorTab: active('session-inspector-tab')?.dataset.tab || null,
      trajectoryRows: document.querySelectorAll('.session-ledger-row').length,
      chatRows: document.querySelectorAll('.session-chat-row').length,
      turnGroups: document.querySelectorAll('.session-turn-group').length,
      selectedEvent: selectedRow?.dataset.eventIndex || null,
      toolbarIcons: [...document.querySelectorAll('[data-action="session-mode"]')].map((node) => {
        const glyph = node.querySelector('[data-icon]');
        const box = glyph?.getBoundingClientRect();
        return { mode: node.dataset.mode, icon: glyph?.getAttribute('data-icon') || null, width: box?.width || null, height: box?.height || null };
      }),
      turns: { on: button('turns')?.dataset.on || null, icon: button('turns')?.querySelector('[data-icon]')?.getAttribute('data-icon') || null },
      calls: { on: button('calls')?.dataset.on || null, icon: button('calls')?.querySelector('[data-icon]')?.getAttribute('data-icon') || null },
      searchPanel: { open: document.querySelector('[data-search-panel]')?.dataset.open || null, rect: (() => { const node = document.querySelector('[data-search-panel]'); if (!node) return null; const box = node.getBoundingClientRect(); return { x: box.x, y: box.y, width: box.width, height: box.height }; })() },
      scroll: { page: scroll('.session-detail-scroll'), events: scroll('.session-event-scroll'), inspector: scroll('.session-inspector-scroll'), chat: scroll('.session-chat-scroll') }
    };
  })()`))};
}

await send("Page.navigate", { url: `${baseURL}/#/sessions/${encodeURIComponent(selected.id)}` });
await sleep(100);
await evaluate(`localStorage.setItem("flatline-locale", "zh"); localStorage.setItem("flatline-theme", "light"); location.reload()`);
await waitFor("Boolean(document.querySelector('.session-detail-page'))");
await sleep(500);

const report = {
  generatedAt: new Date().toISOString(),
  source: "daemon_owned_sqlite",
  session: { source: selected.source, eventCount: selected.event_count, idDigest: String(selected.id).slice(0, 12) },
  states: {}
};
report.states.initial = await snapshot("trajectory-initial");
await click('[data-action="session-tab"][data-tab="chat"]');
await waitFor("Boolean(document.querySelector('.session-chat-pane'))");
report.states.chat = await snapshot("chat-selected");
await click('[data-action="session-tab"][data-tab="trajectory"]');
await waitFor("Boolean(document.querySelector('.session-detail-overview'))");
report.states.trajectoryRestored = await snapshot("trajectory-restored");

await click('[data-action="session-mode"][data-mode="turns"]');
await waitFor("document.querySelector('[data-action=\"session-mode\"][data-mode=\"turns\"]')?.dataset.on === 'true'");
report.states.turnsFolded = await snapshot("turns-folded");
await click('[data-action="session-mode"][data-mode="turns"]');
await waitFor("document.querySelector('[data-action=\"session-mode\"][data-mode=\"turns\"]')?.dataset.on === 'false'");
report.states.turnsRestored = await snapshot("turns-restored");

await click('[data-action="session-mode"][data-mode="calls"]');
await waitFor("document.querySelector('[data-action=\"session-mode\"][data-mode=\"calls\"]')?.dataset.on === 'true'");
report.states.callsFolded = await snapshot("calls-folded");
await click('[data-action="session-mode"][data-mode="calls"]');
await waitFor("document.querySelector('[data-action=\"session-mode\"][data-mode=\"calls\"]')?.dataset.on === 'false'");
report.states.callsRestored = await snapshot("calls-restored");

await click('.session-ledger-row[data-event-index="3"]');
await sleep(150);
report.states.eventSelected = await snapshot("event-selected");
await click('[data-action="session-inspector-tab"][data-tab="ecm"]');
await waitFor("document.querySelector('[data-action=\"session-inspector-tab\"][data-tab=\"ecm\"]')?.dataset.active === 'true'");
report.states.ecm = await snapshot("effective-config-selected");
await click('[data-action="session-inspector-tab"][data-tab="inspector"]');
await waitFor("document.querySelector('[data-action=\"session-inspector-tab\"][data-tab=\"inspector\"]')?.dataset.active === 'true'");

await click('[data-action="search"]');
await waitFor("document.querySelector('[data-search-panel]')?.dataset.open === 'true'");
report.states.searchPanel = await snapshot("search-panel-open");

report.checks = {
  chatTabChangesContent: report.states.initial.trajectory && !report.states.chat.trajectory && report.states.chat.chat && report.states.chat.chatRows > 0,
  trajectoryRestores: report.states.trajectoryRestored.trajectory && report.states.trajectoryRestored.activeSessionTab === "trajectory" && report.states.trajectoryRestored.trajectoryRows > 0,
  turnsToggleChangesRows: report.states.turnsFolded.trajectoryRows < report.states.turnsRestored.trajectoryRows && report.states.turnsFolded.turns.on === "true" && report.states.turnsRestored.turns.on === "false",
  callsToggleChangesRows: report.states.callsFolded.trajectoryRows < report.states.callsRestored.trajectoryRows && report.states.callsFolded.calls.on === "true" && report.states.callsRestored.calls.on === "false",
  eventSelectionChangesInspector: report.states.eventSelected.selectedEvent === "3",
  inspectorTabChangesContent: report.states.ecm.activeInspectorTab === "ecm" && report.states.ecm.trajectoryRows === report.states.eventSelected.trajectoryRows,
  toolbarIconsMatchPrototype: [report.states.initial, report.states.turnsFolded, report.states.callsFolded].every((state) => {
    const byMode = Object.fromEntries((state.toolbarIcons || []).map((item) => [item.mode, item]));
    return byMode.duration?.icon === "clock"
      && byMode.duration.width === 14 && byMode.duration.height === 14
      && byMode.turns?.icon === (state === report.states.turnsFolded ? "rows-3" : "rows-2")
      && byMode.turns.width === 14 && byMode.turns.height === 14
      && byMode.calls?.icon === (state === report.states.callsFolded ? "list" : "list-collapse")
      && byMode.calls.width === 14 && byMode.calls.height === 14;
  }),
  nestedEventScrollAvailable: report.states.initial.scroll.events?.scrollHeight > report.states.initial.scroll.events?.clientHeight,
  nestedInspectorScrollAvailable: report.states.initial.scroll.inspector?.scrollHeight > report.states.initial.scroll.inspector?.clientHeight,
  searchPanelOpensInSidebar: report.states.searchPanel.searchPanel.open === "true" && report.states.searchPanel.searchPanel.rect?.x < 256
};
report.allPassed = Object.values(report.checks).every(Boolean);
await fs.writeFile(outputPath, JSON.stringify(report, null, 2) + "\n");
console.log(JSON.stringify({ outputPath, allPassed: report.allPassed, checks: report.checks, rows: { initial: report.states.initial.trajectoryRows, turnsFolded: report.states.turnsFolded.trajectoryRows, callsFolded: report.states.callsFolded.trajectoryRows, restored: report.states.callsRestored.trajectoryRows } }, null, 2));
ws.close();
if (!report.allPassed) process.exitCode = 1;
