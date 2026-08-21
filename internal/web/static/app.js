(() => {
  "use strict";

  const root = document.getElementById("flatline-root");
  const toast = document.getElementById("flatline-toast");
  const cache = { assets: null, assetsMode: null, stats: null, notifications: null, sessions: null, timeline: null };
  const WALL_ROW_CHUNK = 96;
  const SESSION_ROW_CHUNK = 120;
  const SESSION_EVENT_PAGE_SIZE = 1000;
  let wallLazySections = [];
  let wallLazyObserver = null;
  let sessionLazyBatches = [];
  let sessionLazyObserver = null;
  let sessionLazyRoot = null;
  let sessionLazyScrollHandler = null;
  const savedLocale = (() => { try { return localStorage.getItem("flatline-locale"); } catch (_) { return null; } })();
  const savedTheme = (() => { try { return localStorage.getItem("flatline-theme"); } catch (_) { return null; } })();
  const view = {
    search: "",
    scope: "all",
    zoneOpen: { "silent,broken,bypassed": true, "degraded,awaiting_resurrection": true, healthy: true, dormant: false, no_opportunity: true, unobservable: false, "not_evaluated,archived": false },
    assetTab: "diagnosis",
    timelineFilter: "all",
    sessionTab: "trajectory",
    selectedEvent: 0,
    selectedFriction: null,
    sessionData: null,
    sessionDerived: null,
    sessionPageState: null,
    sessionShowDuration: true,
    sessionFoldTurns: false,
    sessionFoldCalls: false,
    sessionCollapsedTurns: {},
    sessionQuery: "",
    sessionChatFilter: "all",
    sessionInspectorTab: "inspector",
    sessionSourceFilter: "all",
    sessionOnlyRecorded: false,
    sessionSort: "recent",
    notificationHiddenKey: "",
    modifyAssetID: "",
    locale: savedLocale === "en" ? "en" : "zh",
    theme: savedTheme === "dark" ? "dark" : "light"
  };

  function applyPreferences() {
    document.documentElement.classList.toggle("dark", view.theme === "dark");
    document.documentElement.dataset.theme = view.theme;
    document.documentElement.lang = view.locale === "en" ? "en" : "zh-CN";
    document.title = view.locale === "en" ? "Flatline · Assets" : "Flatline · 资产";
  }
  applyPreferences();

  const stateLabels = {
    healthy: "正常",
    degraded: "使用减少",
    silent: "不再被使用",
    broken: "引用失效",
    bypassed: "调用后未遵循",
    awaiting_resurrection: "需要监测",
    dormant: "几乎未使用",
    no_opportunity: "没有相关任务记录",
    unobservable: "不可观测",
    not_evaluated: "未评估",
    archived: "已归档"
  };
  const stateIcons = {
    healthy: "activity",
    degraded: "trending-down",
    silent: "power-off",
    broken: "unlink",
    bypassed: "shield-off",
    awaiting_resurrection: "hourglass",
    dormant: "package-open",
    no_opportunity: "circle-slash",
    unobservable: "eye-off",
    not_evaluated: "clock",
    archived: "archive"
  };
  const stateTones = {
    healthy: "good", degraded: "warn", silent: "warn", broken: "bad", bypassed: "bad",
    awaiting_resurrection: "accent", dormant: "muted", no_opportunity: "muted",
    unobservable: "muted", not_evaluated: "muted", archived: "muted"
  };
  const sourceLabels = { claude_code: "Claude Code", codex: "Codex" };
  const kindLabels = { skill: "Skill", rule: "Rule", hook: "Hook", agents_md: "AGENTS.md" };
  const observationLabels = {
    invoked: "直接观测",
    "observed-use": "观察到使用",
    loaded: "已加载",
    offered: "已提供",
    inferred: "推断",
    unknown: "未记录"
  };
  const signalLabels = {
    followed: "明确参与",
    "observed-use": "观察到使用",
    observed_use: "观察到使用",
    invoked: "调用记录",
    loaded: "加载记录",
    offered: "已提供",
    violated: "明确绕行"
  };
  const eventLabels = {
    state_transition: "状态迁移",
    asset_version: "资产版本变化",
    environment_changed: "环境变化",
    asset_invoked: "资产调用",
    asset_observed_use: "观察到资产使用",
    asset_violation: "资产绕行记录",
    transcript_message: "会话消息",
    transcript_tool_call: "工具调用",
    transcript_tool_result: "工具结果"
  };
  const enStateLabels = {
    healthy: "Healthy",
    degraded: "Reduced use",
    silent: "No longer used",
    broken: "Broken reference",
    bypassed: "Called but not followed",
    awaiting_resurrection: "Needs monitoring",
    dormant: "Rarely used",
    no_opportunity: "No related task record",
    unobservable: "Unobservable",
    not_evaluated: "Not evaluated",
    archived: "Archived"
  };
  const enKindLabels = { skill: "Skill", rule: "Rule", hook: "Hook", agents_md: "AGENTS.md" };
  const enObservationLabels = { invoked: "Directly observed", "observed-use": "Observed use", loaded: "Loaded", offered: "Offered", inferred: "Inferred", unknown: "Not recorded" };
  const enSignalLabels = { followed: "Explicit participation", "observed-use": "Observed use", observed_use: "Observed use", invoked: "Invocation record", loaded: "Load record", offered: "Offered", violated: "Explicit bypass" };
  const enEventLabels = {
    state_transition: "State transition",
    asset_version: "Asset version change",
    environment_changed: "Environment change",
    asset_invoked: "Asset invocation",
    asset_observed_use: "Observed asset use",
    asset_violation: "Asset bypass record",
    transcript_message: "Session message",
    transcript_tool_call: "Tool call",
    transcript_tool_result: "Tool result"
  };

  const ICONS = {
    search: '<circle cx="11" cy="11" r="8"></circle><path d="m21 21-4.3-4.3"></path>',
    package: '<path d="m7.5 4.27 9 5.15"></path><path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"></path><path d="m3.3 7 8.7 5 8.7-5"></path><path d="M12 22V12"></path>',
    layers: '<path d="m12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z"></path><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"></path><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"></path>',
    activity: '<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"></path>',
    chart: '<path d="M3 3v16a2 2 0 0 0 2 2h16"></path><path d="M18 17V9"></path><path d="M13 17V5"></path><path d="M8 17v-3"></path>',
    chartColumn: '<path d="M3 3v16a2 2 0 0 0 2 2h16"></path><path d="M18 17V9"></path><path d="M13 17V5"></path><path d="M8 17v-3"></path>',
    layoutDashboard: '<rect width="7" height="9" x="3" y="3" rx="1"></rect><rect width="7" height="5" x="14" y="3" rx="1"></rect><rect width="7" height="9" x="14" y="12" rx="1"></rect><rect width="7" height="5" x="3" y="16" rx="1"></rect>',
    folder: '<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"></path>',
    camera: '<path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"></path><circle cx="12" cy="13" r="3"></circle>',
    cpu: '<rect width="16" height="16" x="4" y="4" rx="2"></rect><rect width="6" height="6" x="9" y="9" rx="1"></rect><path d="M15 2v2"></path><path d="M15 20v2"></path><path d="M2 15h2"></path><path d="M2 9h2"></path><path d="M20 15h2"></path><path d="M20 9h2"></path><path d="M9 2v2"></path><path d="M9 20v2"></path>',
    git: '<line x1="6" x2="6" y1="3" y2="15"></line><circle cx="18" cy="6" r="3"></circle><circle cx="6" cy="18" r="3"></circle><path d="M18 9a9 9 0 0 1-9 9"></path>',
    gitCommitHorizontal: '<circle cx="12" cy="12" r="3"></circle><line x1="3" x2="9" y1="12" y2="12"></line><line x1="15" x2="21" y1="12" y2="12"></line>',
    calendar: '<path d="M8 2v4"></path><path d="M16 2v4"></path><rect width="18" height="18" x="3" y="4" rx="2"></rect><path d="M3 10h18"></path>',
    arrowLeft: '<path d="m12 19-7-7 7-7"></path><path d="M19 12H5"></path>',
    arrowRight: '<path d="M5 12h14"></path><path d="m12 5 7 7-7 7"></path>',
    chevronDown: '<path d="m6 9 6 6 6-6"></path>',
    chevronUp: '<path d="m18 15-6-6-6 6"></path>',
    chevronRight: '<path d="m9 18 6-6-6-6"></path>',
    external: '<path d="M15 3h6v6"></path><path d="M10 14 21 3"></path><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path>',
    clock: '<circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 16 14"></polyline>',
    history: '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"></path><path d="M3 3v5h5"></path><path d="M12 7v5l4 2"></path>',
    refreshCw: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"></path><path d="M21 3v5h-5"></path><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"></path><path d="M8 16H3v5"></path>',
    unlink: '<path d="m18.84 12.25 1.72-1.71h-.02a5.004 5.004 0 0 0-.12-7.07 5.006 5.006 0 0 0-6.95 0l-1.72 1.71"></path><path d="m5.17 11.75-1.71 1.71a5.004 5.004 0 0 0 .12 7.07 5.006 5.006 0 0 0 6.95 0l1.71-1.71"></path><line x1="8" x2="8" y1="2" y2="5"></line><line x1="2" x2="5" y1="8" y2="8"></line><line x1="16" x2="16" y1="19" y2="22"></line><line x1="19" x2="22" y1="16" y2="16"></line>',
    power: '<path d="M12 2v10"></path><path d="M18.4 6.6a9 9 0 1 1-12.77.04"></path>',
    shield: '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"></path>',
    hourglass: '<path d="M5 22h14"></path><path d="M5 2h14"></path><path d="M17 22v-4.172a2 2 0 0 0-.586-1.414L12 12l-4.414 4.414A2 2 0 0 0 7 17.828V22"></path><path d="M7 2v4.172a2 2 0 0 0 .586 1.414L12 12l4.414-4.414A2 2 0 0 0 17 6.172V2"></path>',
    archive: '<rect width="20" height="5" x="2" y="3" rx="1"></rect><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"></path><path d="M10 12h4"></path>',
    eyeOff: '<path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"></path><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"></path><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"></path><path d="m2 2 20 20"></path>',
    circleSlash: '<circle cx="12" cy="12" r="10"></circle><line x1="9" x2="15" y1="15" y2="9"></line>',
    triangle: '<path d="M13.73 4a2 2 0 0 0-3.46 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"></path>',
    triangleAlert: '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"></path><path d="M12 9v4"></path><path d="M12 17h.01"></path>',
    fileDiff: '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"></path><path d="M9 10h6"></path><path d="M12 13V7"></path><path d="M9 17h6"></path>',
    file: '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"></path><path d="M14 2v4a2 2 0 0 0 2 2h4"></path>',
    diff: '<path d="M12 3v14"></path><path d="M5 10h14"></path><path d="M5 21h14"></path>',
    rows: '<rect width="18" height="18" x="3" y="3" rx="2"></rect><path d="M3 12h18"></path>',
    list: '<line x1="8" x2="21" y1="6" y2="6"></line><line x1="8" x2="21" y1="12" y2="12"></line><line x1="8" x2="21" y1="18" y2="18"></line><line x1="3" x2="3.01" y1="6" y2="6"></line><line x1="3" x2="3.01" y1="12" y2="12"></line><line x1="3" x2="3.01" y1="18" y2="18"></line>',
    listCollapse: '<path d="m3 10 2.5-2.5L3 5"></path><path d="m3 19 2.5-2.5L3 14"></path><path d="M10 6h11"></path><path d="M10 12h11"></path><path d="M10 18h11"></path>',
    rows2: '<rect width="18" height="18" x="3" y="3" rx="2"></rect><path d="M3 12h18"></path>',
    rows3: '<rect width="18" height="18" x="3" y="3" rx="2"></rect><path d="M21 9H3"></path><path d="M21 15H3"></path>',
    x: '<path d="M18 6 6 18"></path><path d="m6 6 12 12"></path>',
    check: '<path d="M20 6 9 17l-5-5"></path>',
    copy: '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"></rect><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"></path>',
    plus: '<path d="M5 12h14"></path><path d="M12 5v14"></path>',
    info: '<circle cx="12" cy="12" r="10"></circle><path d="M12 16v-4"></path><path d="M12 8h.01"></path>',
    terminal: '<polyline points="4 17 10 11 4 5"></polyline><line x1="12" x2="20" y1="19" y2="19"></line>',
    settings: '<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"></path><circle cx="12" cy="12" r="3"></circle>',
    circle: '<circle cx="12" cy="12" r="10"></circle>',
    bookOpen: '<path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"></path><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"></path>',
    webhook: '<path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2"></path><path d="m6 17 3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06"></path><path d="m12 6 3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8"></path>',
    scale: '<path d="m16 16 3-8 3 8c-.87.65-1.92 1-3 1s-2.13-.35-3-1Z"></path><path d="m2 16 3-8 3 8c-.87.65-1.92 1-3 1s-2.13-.35-3-1Z"></path><path d="M7 21h10"></path><path d="M12 3v18"></path><path d="M3 7h2c2 0 5-1 7-2 2 1 5 2 7 2h2"></path>',
    trendingDown: '<polyline points="22 17 13.5 8.5 8.5 13.5 2 7"></polyline><polyline points="16 17 22 17 22 11"></polyline>',
    volumeX: '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><line x1="22" x2="16" y1="9" y2="15"></line><line x1="16" x2="22" y1="9" y2="15"></line>',
    powerOff: '<path d="M18.36 6.64A9 9 0 0 1 20.77 15"></path><path d="M6.16 6.16a9 9 0 1 0 12.68 12.68"></path><path d="M12 2v4"></path><path d="m2 2 20 20"></path>',
    shieldOff: '<path d="m2 2 20 20"></path><path d="M5 5a1 1 0 0 0-1 1v7c0 5 3.5 7.5 7.67 8.94a1 1 0 0 0 .67.01c2.35-.82 4.48-1.97 5.9-3.71"></path><path d="M9.309 3.652A12.252 12.252 0 0 0 11.24 2.28a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1v7a9.784 9.784 0 0 1-.08 1.264"></path>',
    packageOpen: '<path d="M12 22v-9"></path><path d="M15.17 2.21a1.67 1.67 0 0 1 1.63 0L21 4.57a1.93 1.93 0 0 1 0 3.36L8.82 14.79a1.655 1.655 0 0 1-1.64 0L3 12.43a1.93 1.93 0 0 1 0-3.36z"></path><path d="M20 13v3.87a2.06 2.06 0 0 1-1.11 1.83l-6 3.08a1.93 1.93 0 0 1-1.78 0l-6-3.08A2.06 2.06 0 0 1 4 16.87V13"></path><path d="M21 12.43a1.93 1.93 0 0 0 0-3.36L8.83 2.2a1.64 1.64 0 0 0-1.63 0L3 4.57a1.93 1.93 0 0 0 0 3.36l12.18 6.86a1.636 1.636 0 0 0 1.63 0z"></path>',
    fileText: '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"></path><path d="M14 2v4a2 2 0 0 0 2 2h4"></path><path d="M10 9H8"></path><path d="M16 13H8"></path><path d="M16 17H8"></path>',
    fileCode: '<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"></path><path d="M14 2v4a2 2 0 0 0 2 2h4"></path><path d="m10 13-2 2 2 2"></path><path d="m14 13 2 2-2 2"></path>',
    hash: '<line x1="4" x2="20" y1="9" y2="9"></line><line x1="4" x2="20" y1="15" y2="15"></line><line x1="10" x2="8" y1="3" y2="21"></line><line x1="16" x2="14" y1="3" y2="21"></line>',
    wallet: '<path d="M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1"></path><path d="M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4"></path>',
    bellOff: '<path d="M8.7 3A6 6 0 0 1 18 8a21.3 21.3 0 0 0 .6 5"></path><path d="M17 17H3s3-2 3-9a4.67 4.67 0 0 1 .3-1.7"></path><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"></path><path d="m2 2 20 20"></path>',
    slash: '<path d="M22 2 2 22"></path>',
    listFilter: '<path d="M3 6h18"></path><path d="M7 12h10"></path><path d="M10 18h4"></path>',
    sparkles: '<path d="M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z"></path><path d="M20 3v4"></path><path d="M22 5h-4"></path><path d="M4 17v2"></path><path d="M5 18H3"></path>',
    sun: '<circle cx="12" cy="12" r="4"></circle><path d="M12 2v2"></path><path d="M12 20v2"></path><path d="m4.93 4.93 1.41 1.41"></path><path d="m17.66 17.66 1.41 1.41"></path><path d="M2 12h2"></path><path d="M20 12h2"></path><path d="m6.34 17.66-1.41 1.41"></path><path d="m19.07 4.93-1.41 1.41"></path>',
    moon: '<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"></path>',
    languages: '<path d="m5 8 6 6"></path><path d="m4 14 6-6 2-3"></path><path d="M2 5h12"></path><path d="M7 2h1"></path><path d="m22 22-5-10-5 10"></path><path d="M14 18h6"></path>',
    monitor: '<rect width="20" height="14" x="2" y="3" rx="2"></rect><line x1="8" x2="16" y1="21" y2="21"></line><line x1="12" x2="12" y1="17" y2="21"></line>',
    circleDot: '<circle cx="12" cy="12" r="10"></circle><circle cx="12" cy="12" r="1"></circle>',
    moreHorizontal: '<circle cx="12" cy="12" r="1"></circle><circle cx="19" cy="12" r="1"></circle><circle cx="5" cy="12" r="1"></circle>',
    menu: '<line x1="4" x2="20" y1="12" y2="12"></line><line x1="4" x2="20" y1="6" y2="6"></line><line x1="4" x2="20" y1="18" y2="18"></line>',
    database: '<ellipse cx="12" cy="5" rx="9" ry="3"></ellipse><path d="M3 5V19A9 3 0 0 0 21 19V5"></path><path d="M3 12A9 3 0 0 0 21 12"></path>',
    clock3: '<circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 16.5 12"></polyline>',
    user: '<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"></path><circle cx="12" cy="7" r="4"></circle>',
    command: '<path d="M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3"></path>',
    arrowUpRight: '<path d="M7 7h10v10"></path><path d="M7 17 17 7"></path>',
    checkCircle: '<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"></path><path d="m9 11 3 3L22 4"></path>',
    alertCircle: '<circle cx="12" cy="12" r="10"></circle><line x1="12" x2="12" y1="8" y2="12"></line><line x1="12" x2="12.01" y1="16" y2="16"></line>',
    circleHelp: '<circle cx="12" cy="12" r="10"></circle><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"></path><path d="M12 17h.01"></path>',
    lineChart: '<path d="M3 3v16a2 2 0 0 0 2 2h16"></path><path d="m19 9-5 5-4-4-3 3"></path>',
    barChart3: '<path d="M3 3v16a2 2 0 0 0 2 2h16"></path><path d="M18 17V9"></path><path d="M13 17V5"></path><path d="M8 17v-3"></path>',
    pieChart: '<path d="M21 12c.552 0 1.005-.449.95-.998a10 10 0 0 0-8.953-8.951c-.55-.055-.998.398-.998.95v8a1 1 0 0 0 1 1z"></path><path d="M21.21 15.89A10 10 0 1 1 8 2.83"></path>',
    target: '<circle cx="12" cy="12" r="10"></circle><circle cx="12" cy="12" r="6"></circle><circle cx="12" cy="12" r="2"></circle>',
    layers3: '<path d="m12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z"></path><path d="m6.08 9.5-3.5 1.6a1 1 0 0 0 0 1.81l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9a1 1 0 0 0 0-1.83l-3.5-1.59"></path><path d="m6.08 14.5-3.5 1.6a1 1 0 0 0 0 1.81l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9a1 1 0 0 0 0-1.83l-3.5-1.59"></path>',
    braces: '<path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5c0 1.1.9 2 2 2h1"></path><path d="M16 21h1a2 2 0 0 0 2-2v-5c0-1.1.9-2 2-2a2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1"></path>',
    bookMarked: '<path d="M10 2v8l3-3 3 3V2"></path><path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H19a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H6.5a1 1 0 0 1 0-5H20"></path>'
  };

  // The prototype uses Lucide's kebab-case names. Keep aliases explicit so
  // every prototype icon slot resolves to a real glyph instead of silently
  // falling back to the generic circle.
  ICONS["triangle-alert"] = ICONS.triangleAlert;
  ICONS.alertTriangle = ICONS.triangleAlert;
  ICONS["alert-triangle"] = ICONS.triangleAlert;
  ICONS["arrow-left"] = ICONS.arrowLeft;
  ICONS["arrow-right"] = ICONS.arrowRight;
  ICONS["chevron-up"] = ICONS.chevronUp;
  ICONS["chevron-down"] = ICONS.chevronDown;
  ICONS["chevron-right"] = ICONS.chevronRight;
  ICONS["list-collapse"] = ICONS.listCollapse;
  ICONS["rows-2"] = ICONS.rows2;
  ICONS["rows-3"] = ICONS.rows3;
  ICONS["layout-dashboard"] = ICONS.layoutDashboard;
  ICONS["chart-column"] = ICONS.chartColumn;
  ICONS["git-commit-horizontal"] = ICONS.gitCommitHorizontal;
  ICONS["file-diff"] = ICONS.fileDiff;
  ICONS["volume-x"] = ICONS.volumeX;
  ICONS.chevronRight = ICONS.chevronRight;
  ICONS["refresh-cw"] = ICONS.refreshCw;
  ICONS["circle-help"] = ICONS.circleHelp;
  ICONS["circle-slash"] = ICONS.circleSlash;
  ICONS["eye-off"] = ICONS.eyeOff;
  ICONS["package-open"] = ICONS.packageOpen;
  ICONS["power-off"] = ICONS.powerOff;
  ICONS["shield-off"] = ICONS.shieldOff;
  ICONS["trending-down"] = ICONS.trendingDown;
  ICONS["file-text"] = ICONS.fileText;
  ICONS["file-code"] = ICONS.fileCode;
  ICONS["book-open"] = ICONS.bookOpen;
  ICONS["more-horizontal"] = ICONS.moreHorizontal;

  const esc = (value) => String(value == null ? "" : value).replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;" })[ch]);
  const num = (value) => typeof value === "number" && Number.isFinite(value) ? value : null;
  const uiText = (zh, en) => view.locale === "en" ? en : zh;
  const count = (value) => num(value) == null ? (view.locale === "en" ? "Not recorded" : "未记录") : String(value);
  const quantity = (value, zhUnit, enSingular, enPlural) => {
    const amount = num(value);
    if (amount == null) return count(value);
    if (view.locale === "en") return amount + " " + (amount === 1 ? enSingular : (enPlural || enSingular + "s"));
    return amount + " " + zhUnit;
  };
  const localized = (zh, en, value, fallback) => view.locale === "en" ? (en[value] || value || fallback) : (zh[value] || value || fallback);
  const date = (value) => value ? new Date(value).toLocaleString(view.locale === "en" ? "en-US" : "zh-CN", { hour12: false }) : (view.locale === "en" ? "Not recorded" : "未记录");
  const shortDate = (value) => value ? new Date(value).toLocaleString(view.locale === "en" ? "en-US" : "zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }) : (view.locale === "en" ? "Not recorded" : "未记录");
  const kind = (value) => localized(kindLabels, enKindLabels, value, view.locale === "en" ? "Asset" : "资产");
  const scope = (value) => value === "project" ? (view.locale === "en" ? "Project" : "项目级") : value === "user" ? (view.locale === "en" ? "User" : "用户级") : value || (view.locale === "en" ? "Scope not recorded" : "作用域未记录");
  const source = (value) => localized(sourceLabels, { claude_code: "Claude Code", codex: "Codex" }, value, view.locale === "en" ? "Not recorded" : "未记录");
  // This is the icon vocabulary actually present in the prototype, including
  // the values supplied by its dynamic asset/source/session data. A name not in
  // this set must not silently become a different Lucide glyph.
  const prototypeIconNames = new Set([
    "activity", "archive", "arrow-left", "arrow-right", "bell-off", "book-open", "calendar", "camera", "chart-column", "check",
    "chevron-down", "chevron-right", "chevron-up", "circle-slash", "clock", "cpu", "eye-off", "file-code", "file-diff", "file-text", "folder",
    "git-commit-horizontal", "hash", "history", "hourglass", "layers", "list", "list-collapse", "package", "package-open",
    "power-off", "scale", "search", "shield-off", "slash", "trending-down", "triangle-alert", "unlink", "volume-x", "wallet", "webhook", "x",
    "rows-2", "rows-3"
  ]);
  const iconAliases = {
    activity: "activity", archive: "archive", arrowLeft: "arrow-left", arrowRight: "arrow-right", bellOff: "bell-off", calendar: "calendar",
    camera: "camera", chart: "chart-column", chartColumn: "chart-column", check: "check", chevronDown: "chevron-down", chevronRight: "chevron-right",
    chevronUp: "chevron-up", circleSlash: "circle-slash", clock: "clock", cpu: "cpu", eyeOff: "eye-off", fileDiff: "file-diff", folder: "folder",
    gitCommitHorizontal: "git-commit-horizontal", hash: "hash", history: "history", hourglass: "hourglass", layers: "layers", list: "list",
    listCollapse: "list-collapse", package: "package", packageOpen: "package-open", powerOff: "power-off", search: "search", shieldOff: "shield-off",
    slash: "slash", trendingDown: "trending-down", triangleAlert: "triangle-alert", unlink: "unlink", volumeX: "volume-x", wallet: "wallet", x: "x",
    rows2: "rows-2", rows3: "rows-3"
  };
  const icon = (name, extra) => {
    const requested = String(name || "");
    const kebab = requested.replace(/([a-z])([A-Z])/g, "$1-$2").toLowerCase();
    const canonical = iconAliases[requested] || (prototypeIconNames.has(kebab) ? kebab : "");
    if (!canonical || !prototypeIconNames.has(canonical)) return "";
    const glyph = ICONS[canonical] || ICONS[requested] || ICONS[kebab];
    if (!glyph) return "";
    return '<span class="fl-icon' + (extra ? " " + extra : "") + '" data-icon="' + esc(canonical) + '" aria-hidden="true"><svg viewBox="0 0 24 24">' + glyph + "</svg></span>";
  };
  const obs = (value) => value ? localized(observationLabels, enObservationLabels, value, value) + (view.locale === "en" ? " (" + value + ")" : "（" + value + "）") : (view.locale === "en" ? "Not recorded (unknown)" : "未记录（unknown）");
  const signal = (value) => value ? localized(signalLabels, enSignalLabels, value, value) : (view.locale === "en" ? "Not recorded" : "未记录");
  const eventLabel = (value) => {
    const labels = view.locale === "en" ? enEventLabels : eventLabels;
    return labels[value] || (view.locale === "en" ? "Event" : "事件");
  };
  const sourceIcon = (value) => { const file = value === "codex" ? (view.theme === "dark" ? "codex-dark" : "codex-light") : value === "claude_code" ? "claudecode" : "deepseek"; return '<span class="source-brand" data-source="' + esc(value) + '"><img src="/icons/' + file + '.svg" alt="" aria-hidden="true"><span>' + esc(source(value)) + '</span></span>'; };
  function assetMark(item) {
    const glyph = item && item.kind === "skill" ? "book-open" : item && item.kind === "hook" ? "webhook" : item && item.kind === "rule" ? "scale" : "file-text";
    return icon(glyph);
  }
  const notificationMeta = {
    verification_passed: { state: "healthy", zhBadge: "修改后验证通过", enBadge: "Verification passed after change", zhAction: "查看相关会话", enAction: "Open related session" },
    verification_failed: { state: "silent", zhBadge: "修改后验证未通过", enBadge: "Verification did not pass", zhAction: "查看相关会话", enAction: "Open related session" },
    broken: { state: "broken", zhBadge: "引用检查发现失效", enBadge: "Broken reference found", zhAction: "查看资产", enAction: "View asset" },
    silent: { state: "silent", zhBadge: "资产进入沉默", enBadge: "Asset entered no-use state", zhAction: "查看资产", enAction: "View asset" },
    bypassed: { state: "bypassed", zhBadge: "记录到资产被绕行", enBadge: "Asset bypass recorded", zhAction: "查看资产", enAction: "View asset" }
  };
  function notificationKey(item) { return String(item && (item.id || (item.asset_id + ":" + item.kind + ":" + item.occurred_at)) || ""); }
  function renderNotification() {
    const host = document.getElementById("flatline-toast");
    if (!host) return;
    const item = (cache.notifications || []).find((candidate) => notificationKey(candidate) !== view.notificationHiddenKey);
    if (!item) { host.hidden = true; host.innerHTML = ""; return; }
    const meta = notificationMeta[item.kind] || { state: "not_evaluated", zhBadge: "状态迁移已记录", enBadge: "State transition recorded", zhAction: "查看资产", enAction: "View asset" };
    const hasSession = Boolean(item.session_id);
    const target = hasSession ? "#/sessions/" + encodeURIComponent(item.session_id) : "#/assets/" + encodeURIComponent(item.asset_id || "");
    const badge = view.locale === "en" ? meta.enBadge : meta.zhBadge;
    const action = view.locale === "en" ? meta.enAction : meta.zhAction;
    const summary = item.summary || item.rule || (view.locale === "en" ? "A state transition was recorded." : "状态迁移已记录。");
    const subject = item.asset_name ? item.asset_name + (view.locale === "en" ? ": " : "：") : "";
    host.innerHTML = '<div class="notification-card" data-kind="' + esc(item.kind || "state_transition") + '"><div class="notification-head"><span class="fl-state" data-state="' + esc(meta.state) + '">' + icon(stateIcons[meta.state] || "activity") + '<span>' + esc(badge) + '</span></span><button type="button" class="us-btn" data-variant="ghost" data-size="icon-sm" data-action="notification-close" data-notification-key="' + esc(notificationKey(item)) + '" aria-label="关闭">' + icon("x") + '</button></div><div class="notification-copy">' + esc(subject + (view.locale === "en" ? translateUI(summary) : summary)) + '</div><div class="notification-foot"><a class="us-btn" data-variant="outline" data-size="sm" href="' + esc(target) + '">' + esc(action) + '</a><span class="fl-level" data-level="unknown"><i></i>' + (view.locale === "en" ? "State transition" : "状态迁移") + '</span></div></div>';
    host.hidden = false;
  }

  // The prototype is authored in Chinese. Keep the render functions readable
  // while translating every static UI phrase, including labels and ARIA
  // attributes, at the DOM boundary. User asset names, paths and payloads are
  // deliberately excluded so real evidence is never rewritten.
  const EN_TEXT = [
    ["正在读取本地事实层…", "Reading local fact layer…"], ["Flatline 导航", "Flatline navigation"], ["Flatline 资产首页", "Flatline asset home"], ["搜索资产", "Search assets"], ["搜索资产、路径或证据", "Search assets, paths or evidence"],
    ["导航", "Navigation"], ["资产", "Assets"], ["会话", "Sessions"], ["变化时间线", "Timeline"], ["统计", "Statistics"], ["项目", "Projects"], ["数据源", "Data sources"], ["全部项目", "All projects"], ["项目级", "Project"], ["用户级", "User"], ["本地模式", "Local mode"], ["数据不离开本机", "Data stays on this machine"], ["daemon 在线", "daemon online"], ["切换语言", "Switch language"], ["切换主题", "Switch theme"], ["深色模式", "Dark mode"], ["浅色模式", "Light mode"],
    ["需要注意", "Needs attention"], ["观察中", "Under observation"], ["正常", "Healthy"], ["几乎未使用", "Rarely used"], ["没有相关任务记录", "No related task record"], ["不可观测", "Unobservable"], ["其他", "Other"], ["当前没有需要注意的状态。", "There are no attention states."], ["当前没有处于观察中的资产。", "There are no assets under observation."], ["当前没有可确认正常的资产。", "There are no assets confirmed healthy."], ["当前没有达到几乎未使用判定的资产。", "No assets meet the rarely-used rule."], ["当前没有未评估或已归档资产。", "There are no unevaluated or archived assets."],
    ["资产变更", "Asset change"], ["环境变化", "Environment change"], ["修改后验证", "Post-change verification"], ["变化时间线", "Timeline"], ["按时间查看本地事实", "View local facts over time"], ["资产版本、环境变化与状态迁移按时间排列。相近时间只表示对齐关系，具体证据请下钻到资产或会话。", "Asset versions, environment changes and state transitions are ordered by time. Proximity indicates alignment only; drill into an asset or session for evidence."],
    ["每一行代表一个资产的当前状态；缺失记录显示为未记录。", "Each row is the current reading for one asset; missing records remain not recorded."], ["变化时间线", "Timeline"], ["整理几乎未使用的资产", "Organize rarely used assets"], ["整理", "Organize"], ["返回资产", "Back to assets"], ["这里仅生成可回滚的逻辑归档处置。Flatline 不会删除、改写或重命名任何源文件。", "This creates only reversible logical-archive dispositions. Flatline never deletes, rewrites or renames source files."],
    ["搜索资产、路径或证据", "Search assets, paths or evidence"], ["资产详情", "Asset details"], ["诊断", "Diagnosis"], ["原文", "Source"], ["版本", "Versions"], ["处置历史", "Disposition history"], ["资产事实", "Asset facts"], ["判定依据", "Decision evidence"], ["参与漏斗", "Participation funnel"], ["相关任务记录", "Related task records"], ["参与记录", "Participation records"], ["引用体检", "Reference checks"], ["相关会话", "Related sessions"], ["处置", "Disposition"], ["证据边界", "Evidence boundary"], ["查看原文", "View source"], ["查看版本", "View versions"], ["隐藏当前状态", "Hide current state"], ["归档", "Archive"], ["需要监测", "Needs monitoring"], ["轨迹", "Trajectory"], ["事件", "Events"], ["会话详情", "Session details"], ["原始证据", "Raw evidence"], ["原始事件", "Raw events"], ["会话轨迹", "Session trajectory"], ["事件详情", "Event details"], ["返回会话", "Back to sessions"],
    ["直接观测", "Directly observed"], ["观察到使用", "Observed use"], ["已加载", "Loaded"], ["已提供", "Offered"], ["推断", "Inferred"], ["明确参与", "Explicit participation"], ["调用记录", "Invocation record"], ["加载记录", "Load record"], ["明确绕行", "Explicit bypass"], ["未记录（unknown）", "Not recorded (unknown)"], ["未记录", "Not recorded"], ["未评估", "Not evaluated"], ["作用域未记录", "Scope not recorded"], ["源路径未记录", "Source path not recorded"], ["工作目录未记录", "Working directory not recorded"], ["参与形式未记录", "Participation form not recorded"], ["内容未记录", "Content not recorded"], ["内容已截断", "Content truncated"], ["只读预览", "Read-only preview"],
    ["参与比未记录", "Participation ratio not recorded"], ["未记录分子 / 分母", "Numerator / denominator not recorded"], ["基线 · 分子 / 分母", "Baseline · numerator / denominator"], ["当前 · 分子 / 分母", "Current · numerator / denominator"], ["当前窗口", "Current window"], ["任务形状未记录", "Task shape not recorded"], ["有参与记录", "Participation recorded"], ["没有参与记录", "No participation recorded"], ["缺失不转换为 0。", "Missing is not converted to zero."], ["当前没有参与记录。", "There are no participation records."], ["没有记录到与该资产相关的任务。", "No task related to this asset was recorded."], ["没有记录到与该资产相关的任务，无法判断参与。", "No task related to this asset was recorded, so participation cannot be judged."],
    ["只有记录到相关任务后，才判断资产是否参与。", "Participation is judged only after related tasks are recorded."], ["数据源必须提供加载或使用记录，才能判断资产是否参与。", "The data source must record loading or use before participation can be judged."], ["状态评估尚未运行，不对资产状态作判断。", "State evaluation has not run; no state judgment is made."], ["资产修改后，需要下一次可观察的参与记录来验证当前状态。", "After an asset change, the next observable participation record is needed to verify the current state."], ["当前没有触发不再被使用、使用减少或引用失效规则。", "No no-longer-used, reduced-use or broken-reference rule is currently triggered."], ["当前没有触发不再被使用、使用减少或引用失效判定。", "No no-longer-used, reduced-use or broken-reference decision is currently triggered."], ["历史参与至少达到 5 个相关任务且参与率至少 30%，最近 8 个相关任务记录到 0 次参与。", "At least five related tasks and a 30% participation baseline are recorded, while the latest eight related tasks record zero participation."], ["最近至少 5 个相关任务的参与率低于已记录基线的一半。", "Participation across at least five recent related tasks is below half of the recorded baseline."], ["已检查引用中有 1 个明确缺失，就标记为引用失效。", "One explicitly missing checked reference is enough to mark a broken reference."], ["同一会话中同时记录明确调用和明确绕行，才标记为调用后未遵循。", "Called but not followed is marked only when explicit invocation and explicit bypass occur in the same session."], ["资产记录时间至少 30 天，且累计参与不超过 2 次，才标记为几乎未使用。", "Rarely used is marked only after 30 days with no more than two recorded participations."],
    ["判定依据可在详情页继续查看。", "Decision evidence can be inspected further on the detail page."], ["尚未运行状态评估；缺少记录不代表数量为零。", "State evaluation has not run; missing records do not mean zero."], ["当前数据没有记录该资产是否被加载或使用，无法判断参与情况。", "Current data does not record whether this asset was loaded or used, so participation cannot be judged."], ["还没有运行状态评估，因此暂不判断。", "State evaluation has not run, so no judgment is made yet."], ["资产已经记录过修改，等待后续相关任务来验证当前状态。", "An asset change is recorded; a later related task is needed to verify the current state."], ["用户已归档这个资产；源文件没有被 Flatline 修改或删除。", "The user archived this asset; Flatline did not modify or delete the source file."], ["停止监测", "Stop monitoring"], ["忽略此状态", "Ignore this state"], ["类型", "Type"], ["基线 → 现在", "Baseline → current"], ["基线 未记录", "Baseline not recorded"], ["未记录相关任务", "Related tasks not recorded"], ["资产版本变化已记录", "Asset version change recorded"], ["资产版本事实已记录", "Asset version fact recorded"], ["环境变化已记录", "Environment change recorded"], ["环境事实", "Environment fact"], ["源路径：", "Source path: "], ["资产内容版本已记录。", "Asset content version recorded."], ["行数未记录", "Line count not recorded"], ["事件最多", "Most events"],
    ["版本数", "Versions"], ["相关任务", "Related tasks"], ["参与记录", "Participation records"], ["当前状态", "Current state"], ["状态起点", "State started"], ["规则版本", "Rule version"], ["首次发现", "First seen"], ["最后发现", "Last seen"], ["次检查", "checks"], ["个版本", "versions"], ["个相关任务", "related tasks"], ["个候选", "candidates"], ["个资产", "assets"], ["个需要注意", "need attention"], ["个没有相关任务记录或已归档", "have no related task record or are archived"], ["条本地会话", "local sessions"], ["条会话", "sessions"], ["条", "records"], ["秒", "seconds"], ["分钟", "minutes"], ["小时", "hours"], ["最后参与", "Last participation"], ["最后参与 未记录", "Last participation not recorded"],
    ["活动分布", "Activity distribution"], ["近 30 天", "Last 30 days"], ["导出当前统计", "Export current statistics"], ["状态分布", "State distribution"], ["成本与上下文", "Cost and context"], ["最近事件", "Latest event"], ["接口当前只提供总事件数与最后事件时间，按日分布显示为未记录。", "The current interface provides only total events and the latest event time; daily distribution remains not recorded."], ["成本、token 与按日上下文开销未记录", "Cost, tokens and daily context usage are not recorded"], ["具体事件可在变化时间线与会话页下钻。", "Drill into the timeline or sessions page for individual events."], ["没有生成空的清理收益。", "No empty cleanup benefit was generated."],
    ["符合当前规则的资产", "Assets matching the current rule"], ["token / 成本节省", "tokens / cost saved"], ["已核实的源文件大小", "Verified source size"], ["可整理资产", "Assets available to organize"], ["每项都需要明确确认", "Each item requires explicit confirmation"], ["最后参与", "Last participation"], ["累计参与", "Total participation"], ["建议", "Recommendation"], ["可生成回滚记录", "Reversible record available"], ["已保存回滚记录", "Rollback record saved"], ["未涉及源文件", "Source file not involved"], ["尚未选择任何资产。", "No assets selected."], ["已选择 ", "Selected "], ["个资产 · 每个处置均保留回滚记录", " assets · every disposition keeps a rollback record"], ["确认整理所选资产", "Confirm organization of selected assets"], ["仅在用户确认后写入处置记录；source_files_changed: false", "Disposition records are written only after user confirmation; source_files_changed: false"], ["已核实大小：", "Verified size: "], ["没有把缺失记录当成低参与，也没有生成空的清理收益。", "Missing records were not treated as low participation, and no empty cleanup benefit was generated."], ["存在 ≥ 30 天，累计参与 ≤ 2 次", "Recorded for ≥ 30 days, total participation ≤ 2"], ["每次会话可省", "Saved per session"], ["已选择归档", "Selected for archive"], ["按建议全选", "Select all recommended"], ["逻辑归档 · 可回滚", "Logical archive · reversible"], ["描述占用", "Description size"], ["将执行的操作", "Operations to execute"],
    ["尚无会话记录。", "No session records yet."], ["当前没有写入本地会话事实。", "No local session facts have been written."], ["打开会话原始记录", "Open raw session record"], ["会话级事件", "Session event"], ["选择一条事件查看载荷。", "Select an event to inspect its payload."], ["事件载荷未记录。", "Event payload not recorded."], ["定位信息已记录", "Locator recorded"], ["定位信息未记录", "Locator not recorded"], ["检查详情未记录", "Check details not recorded"], ["检查结果未记录", "Check result not recorded"], ["引用", "Reference"], ["存在", "Present"], ["缺失", "Missing"], ["尚无引用检查记录。", "No reference checks yet."], ["系统没有用空白替代未记录。", "The system does not replace not recorded with blank space."], ["尚无资产版本记录。", "No asset versions yet."], ["尚无处置记录。", "No disposition records yet."], ["当前没有相关会话。", "There are no related sessions."], ["没有记录到可下钻的会话关联。", "No session association is recorded for drill-down."], ["尚无变化时间线记录。", "No timeline records yet."], ["当前数据没有写入可展示的版本、环境或状态变化。", "Current data has no displayable version, environment or state changes."], ["无法读取本地事实层。", "Unable to read the local fact layer."], ["请确认 Flatline daemon 正在运行，并检查 loopback 地址。", "Confirm that the Flatline daemon is running and check the loopback address."], ["源路径未记录。", "Source path not recorded."], ["当前无法读取资产原文。", "The asset source cannot be read right now."], ["正在读取本地源文件…", "Reading local source file…"], ["读取失败：", "Read failed: "], ["内容未记录", "Content not recorded"], ["时间", "Time"], ["动作", "Action"], ["说明", "Notes"], ["回滚", "Rollback"], ["归档", "Archive"], ["生成清理处置", "Create cleanup disposition"], ["隐藏当前状态", "Hide current state"], ["来源：daemon 独占 SQLite · 本地数据不上传", "Source: daemon-owned SQLite · local data is not uploaded"], ["观测等级随事实保留：invoked、observed-use、loaded、offered、inferred、unknown。unknown 与推断不会被展示成确定事实。", "Observation levels remain attached to facts: invoked, observed-use, loaded, offered, inferred, unknown. unknown and inferred are never presented as certainty."],
    ["确认执行“", "Confirm “"], ["”？Flatline 不会修改或删除源文件。", "”? Flatline will not modify or delete source files."], ["确认将该资产标记为“需要监测”？Flatline 不会修改或删除源文件。", "Mark this asset as “Needs monitoring”? Flatline will not modify or delete source files."], ["确认整理 ", "Organize "], [" 个资产？源文件不会被修改或删除。", " assets? Source files will not be modified or deleted."], ["已记录“", "Recorded “"], ["”；源文件未改变。", "”; source file unchanged."], ["已标记为需要监测；等待后续可观测记录。", "Marked as Needs monitoring; waiting for a later observable record."], ["已生成 ", "Created "], [" 条可回滚处置记录；源文件未改变。", " reversible disposition records; source file unchanged."], ["处置未完成：", "Disposition incomplete: "], ["操作未完成：", "Operation incomplete: "], ["整理未完成：", "Organization incomplete: "], ["归档需要已记录的源路径，当前源路径未记录。", "Archiving requires a recorded source path; the current source path is not recorded."], ["当前资产没有可用的状态实例，无法提交处置。", "This asset has no usable state instance, so the disposition cannot be submitted."], ["已导出当前统计快照。", "Exported the current statistics snapshot."], ["用户在整理页面明确确认", "User explicitly confirmed on the organization page"], ["用户在资产详情页明确确认", "User explicitly confirmed on the asset detail page"]
  ];
  const EN_EXTRA_TEXT = [
    ["会话消息", "Session message"], ["工具调用", "Tool call"], ["工具结果", "Tool result"], ["工具输入未记录", "Tool input not recorded"], ["工具输出未记录", "Tool output not recorded"], ["消息文本未记录", "Message text not recorded"], ["未命名本地会话", "Untitled local session"], ["条消息", "transcripts"], ["角色", "Role"], ["参与率无法计算", "Participation rate unavailable"], ["为什么没有参与率", "Why no rate is shown"], ["没有相关任务记录，因此 Flatline 不计算百分比；这不是 0%。", "No related task record is available, so Flatline does not calculate a percentage. This is not 0%."], ["参与记录：", "Participation records: "], ["没有相关任务记录", "No related task record"], ["参与记录趋势 · 不是参与率", "Recorded participation trend · not a rate"], ["参与率分母", "Rate denominator"], ["已记录参与次数除以已记录相关任务数计算。", "recorded participations divided by recorded related tasks."],
    ["几乎未使用的资产", "Rarely used assets"], ["导出", "Export"], ["本地事实", "Local facts"], ["任务", "Task"],
    ["任务文本未记录", "Task text not recorded"], ["总览", "Overview"], ["事件流", "Event stream"], ["事件时间未记录", "Event time not recorded"], ["事件时间", "Event time"], ["参与形式", "Participation form"], ["定位信息", "Locator"], ["事件载荷", "Event payload"], ["未选择事件。", "No event selected."], ["选择左侧事件查看载荷。", "Select an event on the left to inspect its payload."], ["模型未记录", "Model not recorded"], ["存在 ≥ 30 天，累计参与 ≤ 2 次", "Recorded for ≥ 30 days, total participation ≤ 2"], ["每次会话可省", "Saved per session"], ["已选择归档", "Selected for archive"], ["已核实源文件大小", "Verified source size"], ["按建议全选", "Select all recommended"], ["逻辑归档 · 可回滚", "Logical archive · reversible"], ["描述占用", "Description size"], ["将执行的操作", "Operations to execute"], ["Flatline 不会删除、改写或重命名任何源文件。", "Flatline never deletes, rewrites or renames source files."],
    ["浏览", "Browse"], ["活动", "Activity"], ["近 52 周 · 每格一天", "Last 52 weeks · one cell per day"], ["每日成本", "Daily cost"], ["上下文开销", "Context usage"], ["每次会话", "Per session"], ["Token 数", "Token count"], ["成本", "Cost"],
    ["观测等级", "Observation levels"], ["参与覆盖", "Participation coverage"], ["相关任务参与", "Related-task participation"], ["最后事件：", "Latest event: "], ["事件记录", "event records"], ["52 周 · 事件记录", "52 weeks · event records"],
    ["全部", "All"], ["状态迁移", "State transition"], ["过去 14 天没有状态变化。", "No state changes in the past 14 days."], ["个事件", "events"], [" 条记录", " records"], [" 条", " records"], ["时间对齐记录可下钻", "Drill into the time-alignment record"], ["个资产的时间对齐记录", " assets aligned in time"], ["未关联资产", "Unlinked asset"],
    ["没有记录到与该资产相关的任务", "No task related to this asset was recorded"], ["没有记录到与该资产相关的任务，无法判断它是否参与。", "No task related to this asset was recorded, so its participation cannot be judged."], ["判定规则：", "Decision rule: "], ["当前判定没有记录分子 / 分母。", "The current decision has no recorded numerator / denominator."], ["当前窗口取最近 8 个相关任务；更早的相关任务才会进入基线。没有相关任务记录时不计算参与率。", "The current window uses the latest eight related tasks; older related tasks form the baseline. Participation rate is not calculated when no related task record exists."], ["没有相关任务记录，无法建立任务分母。", "No related task record is available, so a task denominator cannot be established."], ["检查器", "Checker"],
    ["所有处置都需要本次明确确认。Flatline 只记录决定或逻辑归档，不写入、删除或重命名源文件。", "Every disposition requires explicit confirmation in this interaction. Flatline records only the decision or logical archive; it does not write, delete or rename source files."],
    ["会话是原始证据入口。资产页只展示与资产对齐的事实，这里保留真实会话的来源、事件、工作目录和时间信息。", "Sessions are the entry point for raw evidence. Asset pages show only facts aligned to an asset; this page retains each real session's source, events, working directory and timestamps."],
    ["当前没有符合条件的资产。", "No assets match the current rule."],
    ["这里展示已写入本地事实层的数量。统计只提供可下钻的分子、分母和记录数，不输出不透明质量分。", "This shows counts written to the local fact layer. Statistics expose drill-down numerators, denominators and record counts without an opaque quality score."],
    ["仅使用数据库中按日期记录的真实事件；没有日期记录的格子显示为未记录。", "Only real database events with recorded dates are used; cells without a recorded date remain not recorded."],
    ["分子：已记录参与次数；分母：已记录相关任务数。任一项缺失都保持未记录。", "Numerator: recorded participations; denominator: recorded related tasks. If either is unavailable, the result remains not recorded."],
    ["修改后验证通过", "Verification passed after change"], ["修改后验证未通过", "Verification did not pass"], ["引用检查发现失效", "Broken reference found"], ["资产进入沉默", "Asset entered no-use state"], ["记录到资产被绕行", "Asset bypass recorded"], ["状态迁移已记录", "State transition recorded"], ["修改后首次记录到符合条件的参与。", "The first qualifying participation after the change was recorded."], ["修改后验证窗口内没有记录到符合条件的参与。", "No qualifying participation was recorded in the post-change verification window."], ["引用检查记录到缺失项；具体分子、分母和条目见证据。", "The reference check recorded missing items; inspect the evidence for the numerator, denominator and entries."], ["最近相关任务的参与记录触发沉默判定。", "Participation records from recent related tasks triggered the no-use decision."], ["同一会话中记录到资产调用和明确绕行。", "An asset invocation and an explicit bypass were recorded in the same session."], ["修改后首次记录到参与且没有明确违背记录。", "The first participation after the change was recorded without an explicit bypass."], ["修改后连续 8 个可判定机会没有符合条件的参与。", "Eight consecutive judgeable opportunities after the change had no qualifying participation."], ["已检查引用中至少有 1 个明确缺失。", "At least one checked reference is explicitly missing."], ["同一会话中同时记录资产调用和明确绕行。", "An asset invocation and an explicit bypass were both recorded in the same session."],
    ["起点前后对齐", "Alignment around the starting point"], ["±3 天 · 时间对齐", "±3 days · time alignment"], ["相近时间只表示事实对齐，不代表因果。", "Proximity in time indicates factual alignment only; it does not imply causality."], ["环境变化", "Environment change"], ["资产变更", "Asset change"], ["时间对齐", "Time alignment"], ["对齐记录未记录", "Alignment record not recorded"], ["候选原因", "Candidate explanations"], ["仅陈述证据与对齐", "Evidence and alignment only"], ["引用检查缺失项", "Missing reference item"], ["起点附近存在时间对齐记录", "A time-alignment record exists near the starting point"], ["任务机会未记录", "Task opportunity not recorded"], ["未知原因", "Unknown explanation"], ["当前事实层没有足够证据形成候选原因；缺失保持未记录。", "The current fact layer has insufficient evidence to form a candidate explanation; missing remains not recorded."], ["没有记录到与该资产相关的任务，无法建立任务分母或判断参与。", "No task related to this asset was recorded, so a task denominator cannot be established and participation cannot be judged."], ["条起点附近变化", " aligned changes near the starting point"], ["参与率", "Participation rate"], ["行", "lines"], ["环境变化已记录：", "Environment change recorded: "], ["资产版本变化已记录。", "Asset version change recorded."], ["记录到 ", "Recorded "], ["；这只是时间关系，不代表因果。", "; this is a time relationship, not causality."], ["时间关系", "time relationship"],
    ["修改资产", "Modify asset"], ["修改前检查", "Pre-change check"], ["查看当前原文", "View current source"], ["复制路径", "Copy path"], ["确认进入需要监测", "Confirm Needs monitoring"], ["取消", "Cancel"], ["关闭修改检查", "Close modification check"], ["最新记录版本", "Latest recorded version"], ["当前源文件", "Current source file"], ["当前源文件与最近记录版本一致。", "The current source file matches the latest recorded version."], ["当前源文件内容哈希与最近记录版本不一致。", "The current source file hash differs from the latest recorded version."], ["历史正文未记录，无法生成逐行差异。", "Historical source text was not recorded, so a line-by-line diff cannot be generated."], ["历史版本只保留内容哈希与定位信息。", "Historical versions retain only content hashes and locator information."], ["Flatline 只读取证据，不会写入、删除或重命名源文件。", "Flatline only reads evidence; it never writes, deletes or renames source files."], ["请先在外部编辑器完成确认，再记录需要监测。", "Confirm the change in an external editor first, then record Needs monitoring."], ["我已在外部编辑器确认当前资产内容，允许进入需要监测。", "I confirmed the current asset content in an external editor and allow Needs monitoring."], ["源文件内容未记录，无法进行修改前检查。", "Source content was not recorded, so the pre-change check is unavailable."], ["当前源文件", "Current source file"], ["最近观测", "Last observed"], ["当前哈希", "Current hash"], ["没有可比较的记录版本", "No recorded version is available for comparison"], ["已复制源路径。", "Source path copied."], ["请先确认外部编辑器中的当前内容。", "First confirm the current content in the external editor."], ["已进入需要监测；等待后续可观测记录。", "Needs monitoring started; waiting for a later observable record."]
  ];
  function translateUI(value) {
    if (view.locale !== "en") return String(value == null ? "" : value);
    let output = String(value == null ? "" : value);
    for (const [zh, en] of EN_TEXT.concat(EN_EXTRA_TEXT).filter(([zh]) => zh !== "条").slice().sort((a, b) => b[0].length - a[0].length)) output = output.split(zh).join(en);
    return output;
  }
  function localizeDOM() {
    if (view.locale !== "en" || !root) return;
    const skip = (node) => node.parentElement && node.parentElement.closest(".source-code, .event-payload, .fl-row-name, .session-title, .session-shell-title, .session-human-title, .session-task-value, .event-title, .event-evidence, .session-row-fact, .session-chat-preview, .session-chat-code, .session-chat-locator, .session-inspector-title, .cleanup-asset strong, .detail-title-line h1");
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const node of nodes) {
      if (skip(node)) continue;
      node.nodeValue = translateUI(node.nodeValue);
    }
    root.querySelectorAll("[aria-label], [title], [placeholder]").forEach((element) => {
      ["aria-label", "title", "placeholder"].forEach((attribute) => {
        if (!element.hasAttribute(attribute)) return;
        element.setAttribute(attribute, translateUI(element.getAttribute(attribute)));
      });
    });
  }
  const stateTone = (value) => stateTones[value] || "muted";

  function jsonObject(value) {
    if (!value) return {};
    if (typeof value === "object") return value;
    try { return JSON.parse(value); } catch (_) { return {}; }
  }
  function factsOf(item) {
    return item && item.facts ? item.facts : { version_count: null, session_count: null, opportunity_count: null, participation_count: null, observation_levels: [], sparkline: [], change_markers: [] };
  }
  function stateOf(item) {
    if (!item || item.state_status === "not_evaluated" || !item.current_state) return "not_evaluated";
    const current = item.current_state;
    const evidence = jsonObject(current.evidence);
    if (current.broken_overlay || (evidence.broken && evidence.broken.triggered)) return "broken";
    return String(current.state || "not_evaluated");
  }
  function stateLabel(value) { return localized(stateLabels, enStateLabels, value, view.locale === "en" ? "Not evaluated" : "未评估"); }
  function stateBadge(value, large) {
    const key = value || "not_evaluated";
    return '<span class="fl-state" data-state="' + esc(key) + '"' + (large ? ' data-size="lg"' : "") + ">" + icon(stateIcons[key] || "circle-slash") + "<span>" + esc(stateLabel(key)) + "</span></span>";
  }
  function observationLevels(facts) {
    const levels = Array.isArray(facts.observation_levels) ? facts.observation_levels : [];
    return levels.length ? levels.map(obs).join("、") : "未记录（unknown）";
  }
  function verdictFor(item, state) {
    const evidence = jsonObject(item && item.current_state && item.current_state.evidence);
    return evidence[state] || evidence.decision || evidence;
  }
  function evidenceRule(state, verdict) {
    if (state === "no_opportunity") return "只有记录到相关任务后，才判断资产是否参与。";
    if (state === "unobservable") return "数据源必须提供加载或使用记录，才能判断资产是否参与。";
    if (state === "not_evaluated") return "状态评估尚未运行，不对资产状态作判断。";
    if (state === "awaiting_resurrection") return "资产修改后，需要下一次可观察的参与记录来验证当前状态。";
    if (state === "archived") return "用户归档后保持已归档状态，直到用户明确选择需要监测。";
    if (state === "silent") return "历史参与至少达到 5 个相关任务且参与率至少 30%，最近 8 个相关任务记录到 0 次参与。";
    if (state === "degraded") return "最近至少 5 个相关任务的参与率低于已记录基线的一半。";
    if (state === "broken") return "已检查引用中有 1 个明确缺失，就标记为引用失效。";
    if (state === "bypassed") return "同一会话中同时记录明确调用和明确绕行，才标记为调用后未遵循。";
    if (state === "dormant") return "资产记录时间至少 30 天，且累计参与不超过 2 次，才标记为几乎未使用。";
    if (state === "healthy") return "当前没有触发不再被使用、使用减少或引用失效规则。";
    return verdict && verdict.rule ? verdict.rule : "当前状态的判定规则未记录。";
  }
  function humanEvidence(item) {
    const state = stateOf(item);
    const facts = factsOf(item);
    const current = item && item.current_state;
    if (!current) return "尚未运行状态评估；缺少记录不代表数量为零。";
    if (state === "no_opportunity") return "没有记录到与该资产相关的任务，无法判断它是否参与。";
    if (state === "unobservable") return "当前数据没有记录该资产是否被加载或使用，无法判断参与情况。";
    if (state === "not_evaluated") return "还没有运行状态评估，因此暂不判断。";
    if (state === "awaiting_resurrection") return "资产已经记录过修改，等待后续相关任务来验证当前状态。";
    if (state === "archived") return "用户已归档这个资产；源文件没有被 Flatline 修改或删除。";
    const verdict = verdictFor(item, state);
    const evidence = verdict && verdict.evidence || {};
    const details = evidence.details || {};
    const numerator = num(evidence.numerator);
    const denominator = num(evidence.denominator);
    if (state === "silent") return "判定为不再被使用：" + (numerator == null ? "未记录" : numerator) + "/" + (denominator == null ? "未记录" : denominator) + " 次参与；最近 " + (details.recent_required == null ? "未记录" : details.recent_required) + " 个相关任务中记录到 0 次参与。";
    if (state === "degraded") return "判定为使用减少：最近记录到 " + (numerator == null ? "未记录" : numerator) + "/" + (denominator == null ? "未记录" : denominator) + " 次参与，低于已记录基线的一半。";
    if (state === "broken") return "判定为引用失效：已检查引用中有 " + (details.failed == null ? "未记录" : details.failed) + "/" + (details.checked == null ? "未记录" : details.checked) + " 个缺失。";
    if (state === "bypassed") return "同一会话中同时记录了资产调用和明确绕行。";
    if (state === "dormant") return "资产已满足年龄与低参与规则，累计记录到 " + (details.cumulative_participations == null ? count(facts.participation_count) : details.cumulative_participations) + " 次参与。";
    if (state === "healthy") return "当前没有触发不再被使用、使用减少或引用失效判定。";
    return verdict && (verdict.summary || verdict.reason) || "判定依据可在详情页继续查看。";
  }
  function ratioParts(facts) {
    const display = (a, b) => a == null || b == null ? "未记录" : a + "/" + b;
    const pair = (numerator, denominator) => {
      const a = num(numerator);
      const b = num(denominator);
      return a == null || b == null ? null : display(a, b);
    };
    if (num(facts.baseline_participation_denominator) != null || num(facts.current_participation_denominator) != null) {
      return {
        baseline: pair(facts.baseline_participation_numerator, facts.baseline_participation_denominator),
        current: pair(facts.current_participation_numerator, facts.current_participation_denominator)
      };
    }
    if (num(facts.opportunity_count) != null && facts.opportunity_count > 0) return { baseline: null, current: display(num(facts.participation_count), facts.opportunity_count) };
    return { baseline: null, current: null };
  }
  function ratioHTML(facts, state) {
    const parts = ratioParts(facts);
    if (state === "no_opportunity") return '<span class="fl-flag" data-flag="na">' + icon("slash") + uiText("没有相关任务记录", "No related task record") + "</span>";
    if (!parts.baseline && !parts.current) return '<span class="fl-flag" data-flag="na">' + icon("slash") + uiText("未记录分子 / 分母", "Numerator / denominator not recorded") + "</span>";
    if (parts.baseline && parts.current) return '<span class="fl-shift">' + ratioHTML({ current_participation_numerator: num(facts.baseline_participation_numerator), current_participation_denominator: num(facts.baseline_participation_denominator), opportunity_count: 1, participation_count: num(facts.baseline_participation_numerator) }, "metric") + icon("arrowRight") + ratioHTML({ current_participation_numerator: num(facts.current_participation_numerator), current_participation_denominator: num(facts.current_participation_denominator), opportunity_count: 1, participation_count: num(facts.current_participation_numerator) }, "metric") + "</span>";
    return '<span class="fl-ratio" data-tone="' + stateTone(state) + '"><b>' + esc(parts.current) + "</b></span>";
  }
  function sparkline(facts, large, state) {
    const rawSeries = Array.isArray(facts.sparkline) ? facts.sparkline : [];
    const series = rawSeries.filter((point) => point && point.at).map((point, index) => ({ ...point, __index: index }));
    const points = series.filter((point) => num(point.value) != null).map((point) => ({ ...point, value: Math.max(0, Math.min(100, point.value)) / 100 }));
    const markerSource = (Array.isArray(facts.change_markers) ? facts.change_markers : []).filter((marker) => marker && marker.at);
    // Environment events are high-volume session facts. Keep their visual cap,
    // but never let that cap hide a real asset/version or use marker.
    const environmentLimit = large ? 128 : 16;
    const environmentMarkers = markerSource.filter((marker) => marker.kind === "environment" || marker.kind === "environment_changed").slice(-environmentLimit);
    const assetMarkers = markerSource.filter((marker) => marker.kind !== "environment" && marker.kind !== "environment_changed").slice(-32);
    const markers = assetMarkers.concat(environmentMarkers).sort((a, b) => String(a.at).localeCompare(String(b.at)));
    const width = large ? 600 : 132;
    const height = large ? 132 : 34;
    const padX = 5;
    const padY = 6;
    const floor = height - padY;
    const tone = stateTone(state);
    const step = series.length > 1 ? (width - padX * 2) / (series.length - 1) : 0;
    const xOf = (index) => series.length > 1 ? padX + index * step : width / 2;
    const yOf = (value) => height - padY - Math.max(0, Math.min(1, value)) * (height - padY * 2);
    const coord = (point) => [xOf(point.__index), yOf(point.value)];
    const groups = [];
    points.forEach((point) => {
      const previous = groups[groups.length - 1];
      if (!previous || previous[previous.length - 1].__index !== point.__index - 1) groups.push([point]);
      else previous.push(point);
    });
    const pathFor = (group) => {
      const coords = group.map(coord);
      let path = "M" + coords[0][0].toFixed(2) + "," + coords[0][1].toFixed(2);
      for (let index = 0; index < coords.length - 1; index += 1) {
        const from = coords[index];
        const to = coords[index + 1];
        const handle = (to[0] - from[0]) / 3;
        path += "C" + (from[0] + handle).toFixed(2) + "," + from[1].toFixed(2) + " " + (to[0] - handle).toFixed(2) + "," + to[1].toFixed(2) + " " + to[0].toFixed(2) + "," + to[1].toFixed(2);
      }
      return path;
    };
    const lineParts = groups.map(pathFor);
    const line = lineParts.join(" ");
    const times = series.map((point) => new Date(point.at).getTime()).filter(Number.isFinite);
    const markerTimes = markers.map((point) => new Date(point.at).getTime()).filter(Number.isFinite);
    const first = Math.min(...times.concat(markerTimes, [0]));
    const last = Math.max(...times.concat(markerTimes, [first + 1]));
    const timeRange = last > first ? last - first : 1;
    const markerX = (marker) => {
      const at = new Date(marker.at).getTime();
      if (!Number.isFinite(at)) return width / 2;
      return Math.max(padX, Math.min(width - padX, (at - first) / timeRange * width));
    };
    const areaPaths = [];
    groups.filter((group) => group.length > 1).forEach((group) => {
      const path = pathFor(group);
      const from = coord(group[0]);
      const to = coord(group[group.length - 1]);
      areaPaths.push({ d: path + " L" + to[0].toFixed(2) + "," + floor.toFixed(2) + " L" + from[0].toFixed(2) + "," + floor.toFixed(2) + "Z", tone: "base" });
    });
    const gaps = [];
    let gap = null;
    series.forEach((point, index) => {
      if (num(point.value) == null) {
        if (!gap) gap = { start: index, end: index };
        else gap.end = index;
      } else if (gap) { gaps.push(gap); gap = null; }
    });
    if (gap) gaps.push(gap);
    const known = points.map((point) => point.value);
    const base = known.length ? known.slice(0, Math.max(1, Math.ceil(known.length / 2))).reduce((sum, value) => sum + value, 0) / Math.max(1, Math.ceil(known.length / 2)) : null;
    const ruleHTML = markers.filter((marker) => marker.kind === "environment" || marker.kind === "environment_changed").map((marker) => '<line class="fl-spark-rule" x1="' + markerX(marker).toFixed(2) + '" y1="0" x2="' + markerX(marker).toFixed(2) + '" y2="' + height + '"></line>').join("");
    const gapHTML = gaps.map((item) => {
      const left = Math.max(0, xOf(item.start) - step / 2);
      const right = Math.min(width, xOf(item.end) + step / 2);
      return '<rect class="fl-spark-gap" x="' + left.toFixed(2) + '" y="0" width="' + Math.max(0, right - left).toFixed(2) + '" height="' + height + '"></rect>';
    }).join("");
    const nearestY = (marker) => {
      if (!points.length) return yOf(base == null ? 0.5 : base);
      const at = new Date(marker.at).getTime();
      return points.reduce((best, point) => Math.abs(new Date(point.at).getTime() - at) < Math.abs(new Date(best.at).getTime() - at) ? point : best, points[0]);
    };
    const markerHTML = markers.map((marker) => {
      const markerKind = marker.kind === "environment" || marker.kind === "environment_changed" ? "env" : marker.kind === "alive" ? "alive" : "asset";
      const top = markerKind === "env" ? padY - 1 : nearestY(marker) ? yOf(nearestY(marker).value) : yOf(base == null ? 0.5 : base);
      const label = markerKind === "env" ? (view.locale === "en" ? "Environment change" : "环境变化") : markerKind === "alive" ? (view.locale === "en" ? "Use record" : "使用记录") : (view.locale === "en" ? "Asset change" : "资产变更");
      return '<span class="fl-spark-mark" data-mark="' + markerKind + '" style="left:' + (markerX(marker) / width * 100).toFixed(2) + '%;top:' + top.toFixed(2) + 'px" title="' + esc(label + " · " + date(marker.at)) + '"></span>';
    }).join("");
    const baseHTML = base == null ? "" : '<line class="fl-spark-base" x1="0" y1="' + yOf(base).toFixed(2) + '" x2="' + width + '" y2="' + yOf(base).toFixed(2) + '"></line>';
    const areaHTML = areaPaths.map((area) => '<path class="fl-spark-area" data-seg="' + area.tone + '" d="' + esc(area.d) + '"></path>').join("");
    return '<span class="fl-spark" data-size="' + (large ? "lg" : "sm") + '" data-tone="' + tone + '" data-animated="true"><svg viewBox="0 0 ' + width + ' ' + height + '" preserveAspectRatio="none" shape-rendering="geometricPrecision" role="img" aria-label="' + (view.locale === "en" ? "Participation rate curve" : "参与率曲线") + '">' + gapHTML + baseHTML + ruleHTML + areaHTML + '<path class="fl-spark-line" d="' + esc(line) + '"></path></svg><span class="fl-spark-marks">' + markerHTML + '</span></span>';
  }

  function evidenceVisuals(facts, state) {
    const chart = sparkline(facts, true, state);
    const from = facts && Array.isArray(facts.sparkline) && facts.sparkline.length ? shortDate(facts.sparkline[0].at) : count(null);
    const values = facts && Array.isArray(facts.sparkline) ? facts.sparkline.filter((point) => num(point && point.value) != null) : [];
    const to = values.length ? shortDate(values[values.length - 1].at) : count(null);
    return '<div class="diagnosis-chart-panel">' + chart + '<div class="diagnosis-chart-meta"><span>' + esc(from) + '</span><span class="fl-legend"><span><i data-mark="asset"></i>' + uiText("资产变更", "Asset change") + '</span><span><i data-mark="env"></i>' + uiText("环境变化", "Environment change") + '</span></span><span>' + esc(to) + '</span></div></div>';
  }

  function sorted(items) {
    return items.slice().sort((a, b) => String(b.last_seen_at || b.first_seen_at || "").localeCompare(String(a.last_seen_at || a.first_seen_at || "")) || String(a.name || "").localeCompare(String(b.name || "")));
  }

  function hydrateWallSection(index) {
    const entry = wallLazySections[index];
    if (!entry || entry.rendered >= entry.rows.length) return;
    const container = document.querySelector('[data-wall-lazy="' + index + '"]');
    const sentinel = container && container.querySelector("[data-wall-sentinel]");
    if (!container || !sentinel) return;
    const next = entry.rows.slice(entry.rendered, entry.rendered + WALL_ROW_CHUNK).map(assetRow).join("");
    if (next) sentinel.insertAdjacentHTML("beforebegin", next);
    entry.rendered = Math.min(entry.rows.length, entry.rendered + WALL_ROW_CHUNK);
    const remaining = entry.rows.length - entry.rendered;
    if (!remaining) {
      if (wallLazyObserver) wallLazyObserver.unobserve(sentinel);
      sentinel.remove();
      return;
    }
    sentinel.style.height = (remaining * 58) + "px";
    sentinel.dataset.remaining = String(remaining);
  }

  function armWallLazyRows() {
    const scroll = document.querySelector(".wall-page")?.closest(".screen-scroll");
    if (!scroll || !wallLazySections.length) return;
    const sentinels = [...scroll.querySelectorAll("[data-wall-sentinel]")];
    const hydrateVisible = () => sentinels.forEach((sentinel) => {
      const box = sentinel.getBoundingClientRect();
      const rootBox = scroll.getBoundingClientRect();
      if (box.top < rootBox.bottom + 720 && box.bottom > rootBox.top - 720) hydrateWallSection(Number(sentinel.dataset.wallSection));
    });
    if (typeof IntersectionObserver === "function") {
      wallLazyObserver = new IntersectionObserver((entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) hydrateWallSection(Number(entry.target.dataset.wallSection));
        });
      }, { root: scroll, rootMargin: "720px 0px" });
      sentinels.forEach((sentinel) => wallLazyObserver.observe(sentinel));
    } else {
      scroll.addEventListener("scroll", hydrateVisible, { passive: true });
    }
    hydrateVisible();
  }

  function assetRow(item) {
    const state = stateOf(item);
    const facts = factsOf(item);
    const last = facts.last_participation_at ? "最后参与 " + shortDate(facts.last_participation_at) : "最后参与 未记录";
    return '<a class="fl-row" href="#/assets/' + encodeURIComponent(item.id) + '" data-muted="' + (["dormant", "no_opportunity", "unobservable", "archived", "not_evaluated"].includes(state) ? "true" : "false") + '"><span class="fl-state" data-state="' + esc(state) + '">' + icon(stateIcons[state] || "circle-slash") + "<span>" + esc(stateLabel(state)) + '</span></span><span class="fl-row-identity"><span class="fl-row-name">' + esc(item.name) + '</span><span class="fl-row-sub">' + esc(kind(item.kind) + " · " + scope(item.scope) + " · " + (item.source_path || "源路径未记录")) + '</span></span><span class="fl-row-spark">' + sparkline(facts, false, state) + '</span><span class="fl-row-ratio">' + ratioHTML(facts, state) + "<small>" + esc(last) + "</small></span></a>";
  }
  function section(title, keys, emptyText, tone, open, groups) {
    const rows = sorted(keys.flatMap((key) => groups[key] || []));
    const sectionKey = keys.join(",");
    const expanded = view.zoneOpen[sectionKey] !== undefined ? view.zoneOpen[sectionKey] : open !== false;
    let body = "";
    if (expanded && rows.length) {
      if (rows.length <= WALL_ROW_CHUNK) {
        body = '<div class="fl-zone-rows">' + rows.map(assetRow).join("") + "</div>";
      } else {
        const index = wallLazySections.length;
        wallLazySections.push({ rows, rendered: WALL_ROW_CHUNK });
        const initial = rows.slice(0, WALL_ROW_CHUNK).map(assetRow).join("");
        const remaining = rows.length - WALL_ROW_CHUNK;
        body = '<div class="fl-zone-rows" data-wall-lazy="' + index + '">' + initial + '<span class="wall-lazy-spacer" data-wall-sentinel="true" data-wall-section="' + index + '" data-remaining="' + remaining + '" style="height:' + (remaining * 58) + 'px" aria-hidden="true"></span></div>';
      }
    } else if (expanded) {
      body = '<div class="fl-empty">' + esc(emptyText) + "</div>";
    }
    return '<section class="fl-zone" data-tone="' + esc(tone || "muted") + '" data-empty="' + (rows.length ? "false" : "true") + '"><div class="fl-zone-head"><button type="button" data-action="zone" data-zone="' + esc(sectionKey) + '" aria-expanded="' + expanded + '">' + icon(expanded ? "chevronUp" : "chevronDown") + "<span>" + esc(title) + '</span><span class="fl-zone-count">' + rows.length + '</span></button><span class="fl-zone-line"></span></div>' + body + "</section>";
  }
  function navRow(href, key, label, glyph, countValue, hash) {
    const active = key === "assets" ? hash === "#/" || hash.startsWith("#/?") || hash.startsWith("#/assets/") || hash === "#/cleanup" : hash.startsWith("#/" + key);
    return '<a class="us-nav-row" data-nav="' + key + '" data-active="' + active + '" href="' + href + '">' + icon(glyph, "nav-icon") + "<span>" + label + '</span><span class="nav-count">' + (countValue == null ? "" : esc(countValue)) + "</span></a>";
  }
  function renderShell() {
    const hash = location.hash || "#/";
    const assets = cache.assets && cache.assets.assets || [];
    const stats = cache.stats || {};
    const scopeCounts = { all: assets.length, project: assets.filter((item) => item.scope === "project").length, user: assets.filter((item) => item.scope === "user").length };
    const sources = stats.source_counts || {};
    const attention = assets.filter((item) => ["silent", "broken", "bypassed"].includes(stateOf(item))).length;
    const searchLabel = view.locale === "en" ? "Search" : "搜索";
    let projectRows = '<button class="us-nav-row" type="button" data-action="scope" data-scope="all" data-active="' + (view.scope === "all") + '">' + icon("layers", "nav-icon") + "<span>全部项目</span><span class=\"nav-count\">" + scopeCounts.all + "</span></button>";
    projectRows += '<button class="us-nav-row" type="button" data-action="scope" data-scope="project" data-active="' + (view.scope === "project") + '">' + icon("folder", "nav-icon") + "<span>项目级</span><span class=\"nav-count\">" + scopeCounts.project + "</span></button>";
    projectRows += '<button class="us-nav-row" type="button" data-action="scope" data-scope="user" data-active="' + (view.scope === "user") + '">' + icon("folder", "nav-icon") + "<span>用户级</span><span class=\"nav-count\">" + scopeCounts.user + "</span></button>";
    const sourceRows = Object.keys(sources).sort().map((key) => '<div class="sidebar-source">' + sourceIcon(key) + '<small>' + esc(sources[key]) + (view.locale === "en" ? " sessions" : " 条会话") + "</small></div>").join("") || '<div class="sidebar-loading">未记录会话来源</div>';
    const shellBeforeSearch = '<div class="prototype-shell"><aside class="us-sidebar" aria-label="Flatline 导航"><div class="sidebar-top"><a class="brand" href="#/" aria-label="Flatline 资产首页"><span class="fl-mark" data-size="sm" aria-hidden="true">' + icon("activity") + '</span><span class="brand-word">Flatline</span><span class="brand-beta">BETA</span></a><label class="us-nav-row sidebar-search sidebar-search-input" for="flatline-search">' + icon("search", "search-icon") + '<span class="sr-only">' + searchLabel + '</span><input id="flatline-search" type="search" aria-label="' + searchLabel + '" placeholder="' + searchLabel + '" value="';
    const shellAfterSearch = '" autocomplete="off"><span class="search-shortcut">⌘K</span></label></div><div class="fl-scroll sidebar-scroll"><div class="sidebar-group">' + navRow("#/", "assets", "资产", "package", attention || "", hash) + navRow("#/sessions", "sessions", "会话", "layers", cache.sessions && cache.sessions.length || "", hash) + navRow("#/timeline", "timeline", "变化时间线", "git-commit-horizontal", "", hash) + navRow("#/stats", "stats", "统计", "chart-column", "", hash) + '</div><div class="sidebar-group"><div class="sidebar-group-label">项目</div>' + projectRows + '</div><div class="sidebar-group"><div class="sidebar-group-label">数据源</div>' + sourceRows + '</div></div><div class="sidebar-footer"><button class="us-nav-row sidebar-local-row" type="button" data-action="local-mode"><span class="local-mark">L</span><span><strong>本地模式</strong><small>数据不离开本机</small></span><i class="online-dot" title="daemon 在线"></i><span class="local-chevron">' + icon("chevronUp") + '</span></button></div></aside><main class="prototype-main" aria-live="polite"><div id="flatline-screen"></div></main></div>';
    root.innerHTML = shellBeforeSearch + esc(view.search) + shellAfterSearch;
    const footer = root.querySelector(".sidebar-footer");
    if (footer) {
      footer.insertAdjacentHTML("beforeend", '<div class="sidebar-controls"><button class="sidebar-control" type="button" data-action="locale" title="切换语言"><span>' + (view.locale === "en" ? "中文" : "English") + '</span></button><button class="sidebar-control" type="button" data-action="theme" title="切换主题"><span>' + (view.theme === "dark" ? "浅色模式" : "深色模式") + '</span></button></div>');
    }
    localizeDOM();
    renderNotification();
  }
  function header(title, summary, right) {
    return '<header class="screen-header"><div class="screen-header-left"><h1>' + esc(title) + '</h1>' + (summary ? '<span class="header-summary">' + summary + "</span>" : "") + '</div><div class="screen-header-right">' + (right || "") + "</div></header>";
  }
  function screenContent(body, className, scrollClass) {
    return '<div class="screen-scroll' + (scrollClass ? " " + scrollClass : "") + '"><div class="screen-content ' + (className || "") + '">' + body + "</div></div>";
  }
  function filteredAssets() {
    const all = cache.assets && cache.assets.assets || [];
    const query = view.search.trim().toLocaleLowerCase();
    return all.filter((item) => view.scope === "all" || item.scope === view.scope).filter((item) => !query || [item.name, item.kind, item.scope, item.source_path, humanEvidence(item)].join(" ").toLocaleLowerCase().includes(query));
  }
  function alertFor(item, state) {
    if (state === "healthy") return "";
    const tone = stateTone(state);
    const title = state === "no_opportunity" ? "没有记录到与该资产相关的任务" : state === "unobservable" ? "不可观测" : state === "awaiting_resurrection" ? "需要监测" : stateLabel(state);
    return '<div class="us-alert" data-tone="' + tone + '">' + icon(stateIcons[state] || "slash", "alert-icon") + '<div><strong>' + esc(title) + '</strong><p>' + esc(humanEvidence(item)) + "</p></div></div>";
  }
  function evidenceCard(item, data) {
    const state = stateOf(item);
    const facts = factsOf(item);
    const parts = ratioParts(facts);
    const verdict = verdictFor(item, state);
    const evidence = verdict && verdict.evidence || {};
    const levels = facts.observation_levels || [];
    const currentState = item.current_state || {};
    const since = currentState.started_at ? shortDate(currentState.started_at) : "";
    const levelText = levels.length ? levels.map(obs).join("、") : "未记录（unknown）";
    const baseRatio = parts.baseline || "未记录";
    const currentRatio = parts.current || "未记录";
    const missingNote = parts.current ? "" : (view.locale === "en" ? " · Missing is not zero." : " · 缺失不转换为 0。") ;
    return '<section class="elevated-card card-pad evidence-card" data-state="' + esc(state) + '"><header class="fl-head"><h3>判定依据</h3><span class="fl-aside"><span class="fl-level" data-level="' + esc(levels[0] || "unknown") + '"><i></i>' + esc(levelText) + '</span></span></header><div class="detail-verdict-metrics"><span class="fl-metric"><b data-tone="' + stateTone(state) + '">' + esc(baseRatio) + '</b><small>' + icon("history") + '基线 · 分子 / 分母</small></span><span class="detail-verdict-arrow">' + icon("arrowRight") + '</span><span class="fl-metric"><b data-tone="' + stateTone(state) + '">' + esc(currentRatio) + '</b><small>' + icon(stateIcons[state] || "clock") + '当前 · 分子 / 分母</small></span>' + (since ? '<span class="fl-metric"><b>' + esc(since) + '</b><small>' + icon("calendar") + '状态起点</small></span>' : "") + '</div>' + evidenceVisuals(facts, state) + '<p class="evidence-note">判定规则：' + esc(evidenceRule(state, verdict)) + '</p><p class="evidence-note">' + esc(humanEvidence(item)) + missingNote + (evidence.numerator == null && evidence.denominator == null ? " · 当前判定没有记录分子 / 分母。" : "") + "</p></section>";
  }
  function funnelCard(data) {
    const funnel = data.funnel || {};
    const current = funnel.current || {};
    const steps = Array.isArray(current.steps) ? current.steps : [];
    const rows = steps.map((step) => {
      const numerator = num(step.numerator);
      const denominator = num(step.denominator);
      const width = numerator != null && denominator != null && denominator > 0 ? Math.max(0, Math.min(100, numerator / denominator * 100)) : 0;
      const levels = Array.isArray(step.observation_levels) ? step.observation_levels : [];
      const level = levels[0] || "unknown";
      const label = levels.length ? levels.map(obs).join("、") : "未记录（unknown）";
      const value = numerator == null || denominator == null ? '<span class="fl-flag" data-flag="na">' + icon("slash") + uiText("未记录", "Not recorded") + '</span>' : '<b data-tone="accent">' + esc(numerator + "/" + denominator) + '</b><span class="fl-bar" data-size="sm"><i data-tone="accent" style="--w:' + width.toFixed(1) + '%"></i></span><span class="funnel-baseline">' + uiText("基线 未记录", "Baseline not recorded") + '</span>';
      return '<div class="fl-step"><div class="fl-step-top"><span>' + esc(signal(step.signal)) + '</span><span class="funnel-step-spacer"></span><span class="fl-level" data-level="' + esc(level) + '"><i></i>' + esc(label) + '</span></div>' + value + '</div>';
    }).join("");
    const opportunity = current.opportunity_count == null ? uiText("未记录相关任务", "Related tasks not recorded") : quantity(current.opportunity_count, "个相关任务", "related task", "related tasks");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>' + uiText("参与漏斗", "Participation funnel") + '</h3><span class="fl-aside">' + uiText("基线 → 现在", "Baseline → current") + ' · ' + esc(opportunity) + '</span></header>' + (rows ? '<div class="fl-funnel">' + rows + "</div>" : '<div class="empty-copy"><strong>' + uiText("参与记录未形成漏斗。", "No participation funnel was formed.") + '</strong>' + uiText("当前没有可展示的分子 / 分母。", "No displayable numerator / denominator is recorded.") + '</div>') + (funnel.note ? '<p class="evidence-note">' + esc(funnel.note) + "</p>" : "") + "</section>";
  }
  function opportunityCard(data) {
    const rows = (data.opportunities || []).slice(-8).reverse().map((item) => {
      const participation = item.participation_known == null ? "未记录" : item.participation_known ? (item.participated ? "有参与记录" : "没有参与记录") : "未记录";
      return '<div class="fl-li" data-align="start"><span class="fl-flag" data-flag="' + (item.participated ? "new" : "na") + '">' + (item.participated ? icon("check") : icon("slash")) + esc(participation) + '</span><span style="flex:1;min-width:0;display:flex;flex-direction:column;gap:3px"><span style="font-family:var(--font-mono);font-size:var(--text-ui-12);word-break:break-word">' + esc(item.shape_class || "任务形状未记录") + '</span><span style="font-size:var(--text-ui-11p5);color:var(--panel-surface-fg-muted)">' + esc(shortDate(item.detected_at) + " · 会话 " + (item.session_id || "未记录")) + '</span></span></div>';
    }).join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>相关任务记录</h3><span class="fl-aside">' + esc(quantity((data.opportunities || []).length, "条", "record", "records")) + "</span></header>" + (rows ? '<div class="fl-list" data-first-rule="false">' + rows + "</div>" : '<div class="empty-copy"><strong>没有记录到与该资产相关的任务。</strong>缺失不转换为 0。</div>') + "</section>";
  }
  function participationCard(data) {
    const rows = (data.participations || []).slice(-8).reverse().map((item) => '<div class="candidate-item">' + icon("check", "good-icon") + '<span class="candidate-body"><strong>' + esc(signal(item.signal)) + '</strong><small>' + esc(shortDate(item.occurred_at) + " · " + obs(item.observation_level) + " · 会话 " + (item.session_id || "未记录")) + "</small></span></div>").join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>参与记录</h3><span class="fl-aside">' + esc(quantity((data.participations || []).length, "条", "record", "records")) + "</span></header>" + (rows ? '<div class="fl-list" data-first-rule="false">' + rows + "</div>" : '<div class="empty-copy"><strong>当前没有参与记录。</strong>这不代表系统已经记录到 0 次，只代表当前接口没有对应事实。</div>') + "</section>";
  }
  function referenceCard(data) {
    const checks = data.reference_checks || [];
    const rows = checks.slice().reverse().flatMap((check) => {
      const items = Array.isArray(check.items) && check.items.length ? check.items : [{ value: check.checker_version || "引用检查", exists: check.overall_status === "failed" ? false : check.overall_status === "ok" ? true : null, detail: shortDate(check.checked_at) }];
      return items.map((item) => {
        const exists = item.exists == null ? "未记录" : item.exists ? "存在" : "缺失";
        const glyph = item.exists === false ? "unlink" : item.exists === true ? "check" : "circle-slash";
        const tone = item.exists === false ? "var(--destructive)" : item.exists === true ? "var(--verified)" : "var(--panel-surface-fg-muted)";
        return '<div class="fl-li" data-align="start"><span class="reference-glyph" style="color:' + tone + '">' + icon(glyph) + '</span><span style="flex:1;min-width:0;display:flex;flex-direction:column;gap:3px"><span style="font-family:var(--font-mono);font-size:var(--text-ui-12);word-break:break-word">' + esc(item.value || item.kind || "引用") + '</span><span style="font-size:var(--text-ui-11p5);color:var(--panel-surface-fg-muted)">' + esc((item.detail || "检查详情未记录") + " · " + exists) + '</span></span></div>';
      });
    }).join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>引用体检</h3><span class="fl-aside">' + quantity(checks.length, "次检查", "check", "checks") + '</span></header>' + (rows ? '<div class="fl-list" data-first-rule="false">' + rows + "</div>" : '<div class="empty-copy"><strong>尚无引用检查记录。</strong>系统没有用空白替代未记录。</div>') + "</section>";
  }
  function alignmentRecords(data) {
    // Environment anchors are collected around state assessments. Without a
    // recorded opportunity they are not related-task evidence and must not be
    // displayed on a no-opportunity asset detail.
    if (!Array.isArray(data.opportunities) || data.opportunities.length === 0) return [];
    const records = [];
    (data.transitions || []).forEach((transition) => {
      const parsed = jsonObject(transition.alignment_json);
      const items = Array.isArray(parsed) ? parsed : Array.isArray(parsed.items) ? parsed.items : [];
      items.forEach((item) => records.push({ item, origin: transition.occurred_at }));
    });
    return records.sort((a, b) => String(a.item.occurred_at || "").localeCompare(String(b.item.occurred_at || "")));
  }
  function alignmentCard(data) {
    const records = alignmentRecords(data);
    if (!records.length) return "";
    const rows = records.map(({ item, origin }) => {
      const at = item.occurred_at || item.at;
      const delta = at && origin ? (new Date(at).getTime() - new Date(origin).getTime()) / 86400000 : null;
      const offset = Number.isFinite(delta) ? (delta >= 0 ? "+" : "") + delta.toFixed(1) + " d" : "未记录";
      const kindText = item.kind === "environment_changed" ? "环境变化" : item.kind === "asset_version" ? "资产变更" : item.kind || "时间对齐";
      return '<div class="fl-li" data-align="start"><span style="flex-shrink:0;width:62px;font-family:var(--font-mono);font-size:var(--text-ui-11p5);color:var(--panel-surface-fg-muted)">' + esc(shortDate(at)) + '</span><span class="fl-flag" data-flag="' + (item.kind === "environment_changed" ? "neutral" : "new") + '" style="flex-shrink:0">' + icon(item.kind === "environment_changed" ? "cpu" : "file-diff") + esc(kindText) + '</span><span style="flex:1;min-width:0;font-size:var(--text-ui-13);line-height:1.55;text-wrap:pretty">' + esc(item.summary || "对齐记录未记录") + '</span><span style="flex-shrink:0;font-size:var(--text-ui-11);font-variant-numeric:tabular-nums;color:var(--panel-surface-fg-muted)">' + esc(offset) + "</span></div>";
    }).join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>起点前后对齐</h3><span class="fl-aside">±3 天 · 时间对齐</span></header><div class="fl-list" data-first-rule="false">' + rows + '</div><p class="evidence-note">相近时间只表示事实对齐，不代表因果。</p></section>';
  }
  function candidateCard(item, data) {
    const candidates = [];
    const failed = (data.reference_checks || []).flatMap((check) => (check.items || []).filter((entry) => entry.exists === false));
    if (failed.length) {
      candidates.push({ strength: "强", title: "引用检查缺失项", detail: "已记录 " + failed.length + " 个明确缺失引用；具体条目可在引用体检中查看。", level: "unknown" });
    }
    const aligned = alignmentRecords(data);
    if (aligned.length) {
      candidates.push({ strength: "弱", title: "起点附近存在时间对齐记录", detail: "记录到 " + aligned.length + " 条起点附近变化；这只是时间关系，不代表因果。", level: "unknown" });
    }
    if (!(data.opportunities || []).length) {
      candidates.push({ strength: "弱", title: "任务机会未记录", detail: "没有记录到与该资产相关的任务，无法建立任务分母或判断参与。", level: "unknown" });
    }
    if (!candidates.length) {
      candidates.push({ strength: "—", title: "未知原因", detail: "当前事实层没有足够证据形成候选原因；缺失保持未记录。", level: "unknown" });
    }
    const rows = candidates.map((candidate) => {
      const strength = candidate.strength === "强" ? { pct: 100, tone: "bad", color: "var(--destructive)" } : candidate.strength === "中" ? { pct: 60, tone: "warn", color: "var(--bypass)" } : candidate.strength === "弱" ? { pct: 30, tone: "muted", color: "var(--panel-surface-fg-muted)" } : { pct: 0, tone: "muted", color: "var(--panel-surface-fg-muted)" };
      const strengthLabel = localized({ "强": "强", "中": "中", "弱": "弱", "—": "—" }, { "强": "Strong", "中": "Medium", "弱": "Weak", "—": "—" }, candidate.strength, candidate.strength);
      return '<div class="fl-li" data-align="start" style="gap:14px"><span style="flex-shrink:0;display:flex;flex-direction:column;align-items:center;gap:6px;width:44px;padding-top:2px"><span style="font-size:var(--text-ui-11);font-weight:600;font-variant-numeric:tabular-nums;color:' + strength.color + '">' + esc(strengthLabel) + '</span><span class="fl-bar" data-size="sm" style="width:40px"><i data-tone="' + strength.tone + '" style="--w:' + strength.pct + '%"></i></span></span><span style="flex:1;min-width:0;display:flex;flex-direction:column;gap:6px"><span style="display:flex;align-items:center;gap:8px;flex-wrap:wrap"><span style="font-size:var(--text-ui-13p5);font-weight:500">' + esc(candidate.title) + '</span><span class="fl-level" data-level="' + esc(candidate.level) + '"><i></i>' + esc(obs(candidate.level)) + '</span></span><span style="font-size:var(--text-ui-12p5);line-height:1.6;color:var(--panel-surface-fg-muted);text-wrap:pretty">' + esc(candidate.detail) + '</span></span></div>';
    }).join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>候选原因</h3><span class="fl-aside">仅陈述证据与对齐</span></header><div class="fl-list" data-first-rule="false">' + rows + "</div></section>";
  }
  function factsCard(item, data) {
    const facts = factsOf(item);
    const current = data.current_state || item.current_state;
    const state = stateOf(item);
    const opportunityUnavailable = state === "no_opportunity" || state === "unobservable" || state === "not_evaluated";
    const opportunityCount = opportunityUnavailable ? "未记录" : count(facts.opportunity_count);
    const participationCount = opportunityUnavailable ? "未记录" : count(facts.participation_count);
    const values = [
      ["当前状态", stateLabel(state)],
      ["状态起点", current && current.started_at ? shortDate(current.started_at) : "未记录"],
      ["规则版本", current && current.threshold_version || "未记录"],
      ["首次发现", shortDate(item.first_seen_at)],
      ["最后发现", shortDate(item.last_seen_at)],
      ["版本数", count(facts.version_count)],
      ["相关任务", opportunityCount],
      ["参与记录", participationCount],
      ["观测等级", observationLevels(facts)]
    ];
    return '<section class="elevated-card card-pad"><div class="card-head"><h3>资产事实</h3></div><dl class="facts-list">' + values.map((row) => "<dt>" + esc(row[0]) + "</dt><dd>" + (row[0] === "当前状态" ? stateBadge(state) : esc(row[1])) + "</dd>").join("") + "</dl></section>";
  }
  function detailFactValues(item, data) {
    const current = data.current_state || item.current_state;
    const state = stateOf(item);
    const versions = Array.isArray(data.versions) ? data.versions : [];
    const latest = versions.length ? versions[versions.length - 1] : null;
    const hash = latest && latest.content_hash ? String(latest.content_hash).replace(/^sha256:/, "").slice(0, 8) : "";
    const versionValue = latest ? "v" + latest.version + (hash ? " · " + hash : "") : count(null);
    const threshold = current ? evidenceRule(state, verdictFor(item, state)) : "";
    const thresholdVersion = current && current.threshold_version ? current.threshold_version : "";
    const thresholdValue = threshold ? (view.locale === "en" ? translateUI(threshold) : threshold) + (thresholdVersion ? " · " + thresholdVersion : "") : count(null);
    return [
      [uiText("类型", "Type"), kind(item.kind) + " · " + scope(item.scope)],
      [uiText("当前版本", "Current version"), versionValue],
      [uiText("状态起点", "State started"), current && current.started_at ? shortDate(current.started_at) : count(null)],
      [uiText("判定阈值", "Decision threshold"), thresholdValue]
    ];
  }
  function dispositionCard(item, data) {
    const state = stateOf(item);
    const primary = state === "archived" ? '<button class="us-btn" data-variant="primary" data-size="sm" data-action="restore" data-asset-id="' + esc(item.id) + '" style="width:100%">' + uiText("需要监测", "Needs monitoring") + '</button>' : item.source_path ? '<button class="us-btn" data-variant="primary" data-size="sm" data-action="asset-open-editor" data-source-path="' + esc(item.source_path) + '" style="width:100%">' + uiText("在编辑器中打开", "Open in editor") + '</button>' : '<button class="us-btn" data-variant="primary" data-size="sm" data-action="asset-tab" data-tab="versions" style="width:100%">' + uiText("查看版本", "View versions") + '</button>';
    const actions = '<button class="detail-action-row" type="button" data-action="disposition" data-disposition="archive" data-asset-id="' + esc(item.id) + '" style="color:var(--bypass);background:color-mix(in srgb,var(--bypass) 15%,transparent)">' + icon("archive") + '<span>' + uiText("归档", "Archive") + '</span></button><button class="detail-action-row" type="button" data-action="disposition" data-disposition="ignore" data-asset-id="' + esc(item.id) + '" style="color:var(--destructive);background:color-mix(in srgb,var(--destructive) 12%,transparent)">' + icon("bellOff") + '<span>' + uiText("停止监测", "Stop monitoring") + '</span></button><button class="detail-action-row" type="button" data-action="disposition" data-disposition="ignore" data-asset-id="' + esc(item.id) + '" style="color:var(--panel-surface-fg-muted);background:var(--muted)">' + icon("eyeOff") + '<span>' + uiText("忽略此状态", "Ignore this state") + '</span></button>';
    const facts = detailFactValues(item, data).map((row) => '<div class="fl-li" data-align="start" style="padding:8px 0"><span style="flex-shrink:0;width:76px;font-size:var(--text-ui-11p5);color:var(--panel-surface-fg-muted)">' + esc(row[0]) + '</span><span style="flex:1;min-width:0;font-size:var(--text-ui-12p5);line-height:1.5;text-wrap:pretty">' + esc(row[1]) + '</span></div>').join("");
    return '<section class="elevated-card card-pad disposition-card"><header class="fl-head"><h3>处置</h3></header><div class="detail-disposition-actions">' + primary + actions + '</div><div class="us-sep" data-orientation="horizontal" style="margin:16px 0"></div><div class="fl-list" data-first-rule="false">' + facts + '</div></section>';
  }
  function dispositionHistory(data) {
    const rows = (data.dispositions || []).map((item) => '<tr><td>' + esc(shortDate(item.created_at)) + '</td><td>' + esc(({ modify: "需要监测", archive: "归档", prune: "生成清理处置", ignore: "隐藏当前状态" }[item.action] || item.action)) + '</td><td>' + esc(item.reason || "未记录") + '</td><td>' + (item.rollback ? "已保存回滚记录" : "未涉及源文件") + "</td></tr>").join("");
    return '<section class="elevated-card card-pad"><div class="card-head"><h3>处置历史</h3><span class="card-note">' + quantity((data.dispositions || []).length, "条", "record", "records") + "</span></div><div class=\"table-wrap\"><table class=\"history-table\"><thead><tr><th>时间</th><th>动作</th><th>说明</th><th>回滚</th></tr></thead><tbody>" + (rows || '<tr><td colspan="4" class="muted">尚无处置记录。</td></tr>') + "</tbody></table></div></section>";
  }
  function prototypeDispositionHistory(data) {
    const actionMeta = {
      modify: { label: "需要监测", flag: "new", kind: "asset", tone: "accent" },
      archive: { label: "归档", flag: "thin", kind: "state", tone: "warn" },
      prune: { label: "生成清理处置", flag: "thin", kind: "asset", tone: "muted" },
      ignore: { label: "隐藏当前状态", flag: "na", kind: "state", tone: "muted" }
    };
    const rows = (data.dispositions || []).slice().reverse().map((item) => {
      const meta = actionMeta[item.action] || { label: item.action || "未记录", flag: "na", kind: "state", tone: "muted" };
      const rollback = item.rollback ? "已保存回滚记录" : "未涉及源文件";
      return '<div class="fl-node" data-kind="' + esc(meta.kind) + '" data-tone="' + esc(meta.tone) + '"><div style="display:flex;align-items:center;gap:9px;flex-wrap:wrap;margin-bottom:4px"><span style="font-family:var(--font-mono);font-size:var(--text-ui-11p5);color:var(--panel-surface-fg-muted)">' + esc(shortDate(item.created_at)) + '</span><span class="fl-flag" data-flag="' + esc(meta.flag) + '">' + esc(meta.label) + '</span></div><div style="font-size:var(--text-ui-13);line-height:1.6;text-wrap:pretty">' + esc(item.reason || "处置说明未记录") + '</div><div style="margin-top:4px;color:var(--panel-surface-fg-muted);font-size:var(--text-ui-11p5)">' + esc(rollback) + '</div></div>';
    }).join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>处置历史</h3><span class="fl-aside">' + quantity((data.dispositions || []).length, "条", "record", "records") + '</span></header>' + (rows ? '<div class="fl-track">' + rows + '</div>' : '<div class="empty-copy">尚无处置记录。</div>') + '</section>';
  }
  function versionCard(data) {
    const rows = (data.versions || []).map((item) => '<div class="version-row"><strong>v' + esc(item.version) + '</strong><code>' + esc(item.content_hash || "内容哈希未记录") + '</code><time>' + esc(shortDate(item.observed_at)) + "<br>" + esc(obs(item.observation_level)) + "</time></div>").join("");
    return '<section class="elevated-card card-pad"><div class="card-head"><h3>版本</h3><span class="card-note">' + quantity((data.versions || []).length, "个版本", "version", "versions") + "</span></div>" + (rows || '<div class="empty-copy">尚无资产版本记录。</div>') + "</section>";
  }
  function prototypeVersionCard(data) {
    const versions = data.versions || [];
    const rows = versions.slice().reverse().map((item, index) => {
      const previous = versions[versions.length - index - 2];
      const range = previous ? shortDate(previous.observed_at) + " → " + shortDate(item.observed_at) : shortDate(item.observed_at);
      const current = index === 0 ? '<span class="fl-flag" data-flag="new">当前</span>' : '';
      const hash = item.content_hash || "内容哈希未记录";
      return '<div class="fl-li" data-align="start" style="gap:14px"><span style="flex-shrink:0;display:flex;align-items:center;gap:8px;width:118px"><span style="font-family:var(--font-mono);font-size:var(--text-ui-12p5)">v' + esc(item.version) + '</span>' + current + '</span><span style="flex:1;min-width:0;display:flex;flex-direction:column;gap:3px"><span style="font-size:var(--text-ui-13);line-height:1.5;text-wrap:pretty">资产内容版本已记录。</span><span style="font-family:var(--font-mono);font-size:var(--text-ui-11);color:var(--panel-surface-fg-muted)">' + esc(range + " · " + hash + " · " + obs(item.observation_level)) + '</span></span><span style="flex-shrink:0;width:92px;text-align:right"><span class="fl-flag" data-flag="na">' + icon("slash") + '未记录</span></span></div>';
    }).join("");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>版本</h3><span class="fl-aside">' + quantity(versions.length, "个版本", "version", "versions") + '</span></header>' + (rows ? '<div class="fl-list" data-first-rule="false">' + rows + '</div>' : '<div class="empty-copy">尚无资产版本记录。</div>') + '</section>';
  }
  function sourceCard(asset, sourceData) {
    if (!asset.source_path) return '<section class="elevated-card card-pad"><div class="card-head"><h3>原文</h3></div><div class="empty-copy"><strong>源路径未记录。</strong>当前无法读取资产原文。</div></section>';
    if (!sourceData) return '<section class="elevated-card card-pad"><div class="card-head"><h3>原文</h3></div><div class="empty-copy">正在读取本地源文件…</div></section>';
    const lineCount = sourceData.content ? sourceData.content.split(/\r?\n/).length : null;
    return '<section class="elevated-card card-pad"><div class="card-head"><h3>原文</h3><span class="card-note">' + esc(sourceData.truncated ? "内容已截断" : "只读预览") + (lineCount == null ? "" : " · " + quantity(lineCount, "行", "line", "lines")) + '</span></div><p class="evidence-note">' + esc(sourceData.source_path || asset.source_path) + '</p><pre class="source-code">' + esc(sourceData.content || "内容未记录") + "</pre></section>";
  }
  function prototypeSourceCard(asset, sourceData) {
    if (!asset.source_path) return '<section class="elevated-card card-pad"><header class="fl-head"><h3>原文</h3></header><div class="empty-copy"><strong>源路径未记录。</strong>当前无法读取资产原文。</div></section>';
    if (!sourceData) return '<section class="elevated-card card-pad"><header class="fl-head"><h3>原文</h3></header><div class="empty-copy">正在读取本地源文件…</div></section>';
    const lineCount = sourceData.content ? sourceData.content.split(/\r?\n/).length : null;
    const sourcePath = sourceData.source_path || asset.source_path;
    const parts = sourcePath.split(/[\\/]/);
    const fileName = parts[parts.length - 1] || sourcePath;
    const directory = parts.length > 1 ? parts.slice(0, -1).join("/") : "本地源文件";
    const glyph = /\.py$/i.test(fileName) ? "file-code" : "file-text";
    const tag = sourceData.truncated ? "内容已截断" : "只读预览";
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>原文</h3><span class="fl-aside"><span>' + esc(directory) + '</span><span class="fl-flag" data-flag="neutral">' + esc(tag) + '</span></span></header><div style="display:flex;flex-wrap:wrap;gap:14px;align-items:flex-start"><div style="flex:0 1 216px;min-width:min(216px,100%);display:flex;flex-direction:column;margin:0 -8px"><button type="button" class="source-file-row" data-active="true"><span class="source-file-depth"></span>' + icon(glyph) + '<span>' + esc(fileName) + '</span></button></div><div style="flex:1 1 320px;min-width:min(320px,100%)"><div style="display:flex;align-items:center;gap:10px;margin-bottom:10px"><span class="source-path-label">' + esc(sourcePath) + '</span><div style="flex:1"></div><span class="source-line-count">' + esc(lineCount == null ? "行数未记录" : quantity(lineCount, "行", "line", "lines")) + '</span></div><div class="us-terminal fl-scroll source-terminal"><div class="us-terminal-line source-code-line">' + esc(sourceData.content || "内容未记录") + '</div></div></div></div></section>';
  }
  function modifyViewer(item, data, sourceData) {
    const versions = Array.isArray(data && data.versions) ? data.versions : [];
    const latest = versions.length ? versions[versions.length - 1] : null;
    const sourceAvailable = Boolean(sourceData && typeof sourceData.content === "string" && !sourceData.error);
    const currentHash = sourceAvailable ? sourceData.content_hash : "";
    const latestHash = latest && latest.content_hash || "";
    const hashMatches = Boolean(currentHash && latestHash && currentHash === latestHash);
    const statusTone = !sourceAvailable ? "muted" : hashMatches ? "good" : "warn";
    const statusTitle = !sourceAvailable ? "源文件内容未记录，无法进行修改前检查。" : hashMatches ? "当前源文件与最近记录版本一致。" : "当前源文件内容哈希与最近记录版本不一致。";
    const statusDetail = !sourceAvailable ? (sourceData && sourceData.error ? "读取失败：" + sourceData.error : "当前没有可供只读检查的源文件内容。") : "历史正文未记录，无法生成逐行差异。";
    const lineCount = sourceAvailable ? sourceData.content.split(/\r?\n/).length : null;
    const preview = sourceAvailable ? sourceData.content : (sourceData && sourceData.error ? "读取失败：" + sourceData.error : "内容未记录");
    const canConfirm = Boolean(sourceAvailable && item.current_state && item.current_state.instance_id);
    return '<div class="modify-modal-backdrop" data-modify-modal="true"><section class="modify-modal diff-viewer" role="dialog" aria-modal="true" aria-labelledby="modify-viewer-title"><header class="modify-modal-head"><div><span class="modify-kicker">' + icon("file-diff") + '修改前检查</span><h2 id="modify-viewer-title">' + esc(item.name) + '</h2><p>' + esc(kind(item.kind) + " · " + scope(item.scope)) + '</p></div><button class="us-btn" data-variant="ghost" data-size="icon-sm" data-action="modify-close" aria-label="关闭修改检查">' + icon("x") + '</button></header><div class="modify-modal-body"><div class="modify-path-row"><code>' + esc(item.source_path || "源路径未记录") + '</code><button class="us-btn" data-variant="outline" data-size="sm" data-action="copy-source-path" data-source-path="' + esc(item.source_path || "") + '"' + (item.source_path ? "" : " disabled") + '>复制路径</button></div><div class="modify-status" data-tone="' + statusTone + '"><span class="modify-status-icon">' + icon(statusTone === "good" ? "check" : statusTone === "warn" ? "triangle-alert" : "slash") + '</span><div><strong>' + esc(statusTitle) + '</strong><p>' + esc(statusDetail) + '</p></div></div><div class="modify-hash-grid"><div><span>当前源文件</span><strong>' + esc(currentHash || "未记录") + '</strong></div><div><span>最近观测</span><strong>' + esc(latest ? "v" + latest.version + " · " + shortDate(latest.observed_at) : "没有可比较的记录版本") + '</strong><code>' + esc(latestHash || "未记录") + '</code></div></div><p class="modify-boundary">历史版本只保留内容哈希与定位信息。Flatline 只读取证据，不会写入、删除或重命名源文件。</p><div class="modify-source-head"><strong>当前源文件</strong><span>' + esc(lineCount == null ? "内容未记录" : quantity(lineCount, "行", "line", "lines") + (sourceData.truncated ? " · 内容已截断" : "")) + '</span></div><pre class="modify-source-code">' + esc(preview) + '</pre><label class="modify-ack"><input type="checkbox" data-modify-ack' + (canConfirm ? "" : " disabled") + '><span>我已在外部编辑器确认当前资产内容，允许进入需要监测。</span></label><p class="modify-helper">请先在外部编辑器完成确认，再记录需要监测。</p></div><footer class="modify-modal-foot"><button class="us-btn" data-variant="ghost" data-action="modify-close">取消</button><button class="us-btn" data-variant="outline" data-action="asset-tab" data-tab="source" data-close-modify="true"' + (item.source_path ? "" : " disabled") + '>查看当前原文</button><button class="us-btn" data-variant="primary" data-action="confirm-modify" data-asset-id="' + esc(item.id) + '" disabled>确认进入需要监测</button></footer></section></div>';
  }
  async function openModifyViewer(button) {
    const id = button.dataset.assetId;
    const item = (cache.assets && cache.assets.assets || []).find((asset) => asset.id === id);
    if (!item) { notify("当前资产没有可用的状态实例，无法提交处置。", "error"); return; }
    view.modifyAssetID = id;
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    screen.insertAdjacentHTML("beforeend", '<div class="modify-modal-backdrop" data-modify-modal="true"><section class="modify-modal" role="dialog" aria-modal="true"><div class="prototype-loading"><span class="prototype-loading-mark"></span><span>正在读取本地源文件…</span></div></section></div>');
    try {
      const result = await get("/api/v1/assets/" + encodeURIComponent(id));
      let sourceData = null;
      if (item.source_path) {
        try { sourceData = await get("/api/v1/assets/" + encodeURIComponent(id) + "/source"); } catch (error) { sourceData = { source_path: item.source_path, error: error.message, content: "" }; }
      }
      const modal = document.querySelector("[data-modify-modal]");
      if (modal) modal.outerHTML = modifyViewer(result.asset || item, result, sourceData);
      localizeDOM();
      const close = document.querySelector("[data-action=\"modify-close\"]");
      if (close) close.focus();
    } catch (error) {
      const modal = document.querySelector("[data-modify-modal]");
      if (modal) modal.outerHTML = '<div class="modify-modal-backdrop" data-modify-modal="true"><section class="modify-modal" role="dialog" aria-modal="true"><div class="empty-copy"><strong>无法读取本地事实层。</strong><span>' + esc(error.message || error) + '</span><button class="us-btn" data-variant="outline" data-action="modify-close">取消</button></div></section></div>';
      localizeDOM();
    }
  }
  function closeModifyViewer() {
    const modal = document.querySelector("[data-modify-modal]");
    if (modal) modal.remove();
    view.modifyAssetID = "";
  }
  async function recordDisposition(item, action, reason) {
    const current = item && item.current_state;
    if (!item || !current || !current.instance_id) throw new Error("当前资产没有可用的状态实例，无法提交处置。");
    const body = { action: action, state_instance_id: current.instance_id, confirmed: true, reason: reason };
    if (action === "archive") {
      if (!item.source_path) throw new Error("归档需要已记录的源路径，当前源路径未记录。");
      body.rollback = { source_path: item.source_path, strategy: "保留源文件；仅撤销逻辑归档标记", reversible: true };
    }
    return post("/api/v1/assets/" + encodeURIComponent(item.id) + "/dispositions", body);
  }
  async function confirmModify(button) {
    const modal = document.querySelector("[data-modify-modal]");
    const acknowledgement = modal && modal.querySelector("[data-modify-ack]");
    if (!acknowledgement || !acknowledgement.checked) { notify("请先确认外部编辑器中的当前内容。", "error"); return; }
    const item = (cache.assets && cache.assets.assets || []).find((asset) => asset.id === button.dataset.assetId);
    if (!item || !window.confirm("确认已在外部编辑器完成修改，并进入需要监测？Flatline 不会修改或删除源文件。")) return;
    try {
      await recordDisposition(item, "modify", "用户已在外部编辑器确认修改后明确进入需要监测");
      closeModifyViewer();
      notify("已进入需要监测；等待后续可观测记录。", "success");
      clearData();
      await route();
    } catch (error) {
      notify("处置未完成：" + error.message, "error");
    }
  }
  function sessionsCard(data) {
    const related = Array.isArray(data.related_sessions) ? data.related_sessions : [];
    const opportunities = Array.isArray(data.opportunities) ? data.opportunities : [];
    const participations = Array.isArray(data.participations) ? data.participations : [];
    const ids = [...new Set(related.map((item) => item.id).concat(opportunities.map((item) => item.session_id), participations.map((item) => item.session_id)).filter(Boolean))];
    if (!ids.length) return "";
    const recorded = new Set(participations.map((item) => item.session_id).filter(Boolean));
    const opportunityIDs = new Set(opportunities.map((item) => item.session_id).filter(Boolean));
    const visibleIDs = ids.slice(0, 3);
    const rows = visibleIDs.map((id) => {
      const session = related.find((item) => item.id === id) || (cache.sessions || []).find((item) => item.id === id);
      const title = sessionTitle(session || { id: id });
      const meta = session ? source(session.source) + " · " + shortDate(session.started_at) : (view.locale === "en" ? "Session id: " : "会话标识：") + shortSessionID(id);
      const mark = recorded.has(id) ? '<span class="fl-flag" data-flag="new">' + icon("check") + uiText("已记录参与", "Participation recorded") + '</span>' : opportunityIDs.has(id) ? '<span class="fl-flag" data-flag="na">' + icon("slash") + uiText("未记录参与", "Participation not recorded") + '</span>' : "";
      return '<a class="session-link" href="#/sessions/' + encodeURIComponent(id) + '"><span><strong>' + esc(title) + '</strong><small>' + esc(meta) + '</small></span>' + mark + "</a>";
    }).join("");
    const note = ids.length > 3 ? uiText("最近 3 条", "Latest 3") : quantity(ids.length, "条", "session", "sessions");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>' + uiText("关联会话", "Related sessions") + '</h3><span class="fl-aside">' + note + "</span></header>" + (rows ? '<div class="fl-list session-link-list" data-first-rule="false">' + rows + "</div>" : '<div class="empty-copy"><strong>' + uiText("当前没有相关会话。", "No related sessions.") + '</strong><span>' + uiText("没有记录到可下钻的会话关联。", "No session association is recorded for drill-down.") + '</span></div>') + "</section>";
  }
  function detailActions(item) {
    const state = stateOf(item);
    if (state === "archived") return '<button class="us-btn" data-variant="primary" data-action="restore" data-asset-id="' + esc(item.id) + '">' + uiText("需要监测", "Needs monitoring") + '</button>';
    const primary = item.source_path ? '<button class="us-btn" data-variant="primary" data-action="asset-open-editor" data-source-path="' + esc(item.source_path) + '">' + uiText("在编辑器中打开", "Open in editor") + '</button>' : '<button class="us-btn" data-variant="primary" data-action="asset-tab" data-tab="versions">' + uiText("查看版本", "View versions") + '</button>';
    return primary + '<button class="us-btn" data-variant="outline" data-action="disposition" data-disposition="modify" data-asset-id="' + esc(item.id) + '">' + uiText("需要监测", "Needs monitoring") + '</button><button class="us-btn" data-variant="ghost" data-action="disposition" data-disposition="ignore" data-asset-id="' + esc(item.id) + '">' + uiText("隐藏当前状态", "Hide current state") + '</button><button class="us-btn" data-variant="danger" data-action="disposition" data-disposition="archive" data-asset-id="' + esc(item.id) + '">' + uiText("归档", "Archive") + '</button>';
  }
  async function drawDetail(data) {
    const item = data.asset;
    const state = stateOf(item);
    let sourceData = null;
    if (view.assetTab === "source" && item.source_path) {
      try { sourceData = await get("/api/v1/assets/" + encodeURIComponent(item.id) + "/source"); } catch (error) { sourceData = { source_path: item.source_path, content: "读取失败：" + error.message, truncated: false }; }
    }
    const tabButtons = [["diagnosis", "诊断", true], ["source", "原文", Boolean(item.source_path)], ["versions", "版本", (data.versions || []).length > 0], ["history", "处置历史", (data.dispositions || []).length > 0]].filter((entry) => entry[2]).map((entry) => '<button class="us-tabs-trigger" type="button" data-action="asset-tab" data-tab="' + entry[0] + '" data-active="' + (view.assetTab === entry[0]) + '">' + entry[1] + "</button>").join("");
    let main = "";
    if (view.assetTab === "source") main = prototypeSourceCard(item, sourceData);
    else if (view.assetTab === "versions") main = prototypeVersionCard(data);
    else if (view.assetTab === "history") main = prototypeDispositionHistory(data);
    else {
      const notice = state === "awaiting_resurrection" ? '<div class="us-alert" data-tone="accent"><div><strong>需要监测</strong><p>' + esc(humanEvidence(item)) + '</p></div></div>' : "";
      main = notice + evidenceCard(item, data) + alignmentCard(data) + candidateCard(item, data) + funnelCard(data) + referenceCard(data);
    }
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const detailHeader = '<header class="detail-header"><a class="back-link" href="#/" aria-label="返回资产">' + icon("arrowLeft") + '</a><span class="fl-mark">' + assetMark(item) + '</span><span class="detail-identity"><span class="detail-title-line"><h1>' + esc(item.name) + "</h1>" + stateBadge(state, true) + '</span><span class="detail-subline">' + esc(kind(item.kind) + " · " + scope(item.scope) + " · " + (item.source_path || "源路径未记录")) + "</span></span><span class=\"detail-header-actions\">" + detailActions(item) + '</span></header>';
    const detailTabs = '<div class="detail-tabbar"><div class="us-tabs">' + tabButtons + '</div></div>';
    const detailBody = '<div class="screen-scroll"><div class="detail-wrap"><div class="detail-grid"><main class="detail-main">' + main + "</main><aside class=\"detail-aside\">" + dispositionCard(item, data) + sessionsCard(data) + '</aside></div></div></div>';
    screen.innerHTML = detailHeader + detailTabs + detailBody;
    localizeDOM();
  }
  async function get(path) {
    const response = await fetch(path, { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(response.status + " " + await response.text());
    return response.json();
  }
  async function post(path, body) {
    const response = await fetch(path, { method: "POST", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error(response.status + " " + await response.text());
    return response.json();
  }
  function notify(message, tone) {
    if (!toast) return;
    toast.innerHTML = '<div class="toast" data-tone="' + esc(tone || "info") + '">' + icon(tone === "error" ? "triangle-alert" : tone === "success" ? "check" : "slash") + "<span>" + esc(translateUI(message)) + "</span></div>";
    toast.hidden = false;
    clearTimeout(notify.timer);
    notify.timer = setTimeout(() => { toast.hidden = true; }, 4200);
  }
  async function loadOverview(force, fullAssets) {
    const assetMode = fullAssets ? "wall" : "summary";
    if (!force && cache.assets && cache.assetsMode === assetMode && cache.stats && cache.notifications && cache.sessions) return cache;
    const assetPath = fullAssets ? "/api/v1/assets?view=wall&limit=5000" : "/api/v1/assets?summary=1&limit=5000";
    const sessionPath = fullAssets ? "/api/v1/sessions?summary=1&limit=5000" : "/api/v1/sessions?limit=5000";
    const results = await Promise.all([get(assetPath), get("/api/v1/stats"), get("/api/v1/notifications?limit=5000"), get(sessionPath)]);
    cache.assets = results[0];
    cache.assetsMode = assetMode;
    cache.stats = results[1];
    cache.notifications = results[2].notifications || [];
    cache.sessions = results[3].sessions || [];
    return cache;
  }
  function metric(item) {
    const tone = item.tone || "";
    const missing = item.value === "未记录" || item.value === "Not recorded";
    return '<div class="stat-metric prototype-stat-metric" data-missing="' + missing + '"><span class="stat-label"><span class="stat-icon" data-tone="' + esc(tone || "muted") + '">' + icon(item.icon || "package") + '</span><span>' + esc(item.label) + '</span></span><span class="fl-metric"><b data-tone="' + esc(tone) + '">' + esc(item.value) + '</b></span></div>';
  }
  function activityHeatmap(data) {
    const activity = data.activity_by_day || {};
    const values = Object.values(activity).map(num).filter((value) => value != null);
    const maximum = values.reduce((value, item) => Math.max(value, item), 1);
    const end = data.last_event_at ? new Date(data.last_event_at) : new Date();
    end.setUTCHours(0, 0, 0, 0);
    const start = new Date(end.getTime() - 363 * 86400000);
    const cells = [];
    for (let offset = 363; offset >= 0; offset -= 1) {
      const day = new Date(end.getTime() - offset * 86400000);
      const key = day.toISOString().slice(0, 10);
      const value = num(activity[key]);
      const level = value == null ? 0 : Math.max(1, Math.ceil(value / maximum * 4));
      cells.push('<i class="heat-cell" data-level="' + level + '" data-missing="' + (value == null) + '" title="' + esc(key + " · " + (value == null ? (view.locale === "en" ? "Not recorded" : "未记录") : value + (view.locale === "en" ? " events" : " 个事件"))) + '"></i>');
    }
    const legend = view.locale === "en" ? "More events" : "事件更多";
    const month = (value) => value.toISOString().slice(0, 7);
    return '<div class="heatmap-wrap"><div class="heatmap" aria-label="' + (view.locale === "en" ? "52-week event activity heatmap" : "52 周事件活动热力图") + '">' + cells.join("") + '</div></div><div class="heatmap-legend"><span>' + esc(month(start)) + '</span><span class="heatmap-legend-spacer"></span><span>' + (view.locale === "en" ? "Not recorded" : "未记录") + '</span><i data-level="0"></i><i data-level="1"></i><i data-level="2"></i><i data-level="3"></i><i data-level="4"></i><span>' + legend + '</span><span class="heatmap-legend-spacer"></span><span>' + esc(month(end)) + '</span></div>';
  }
  function distributionBar(label, value, total, tone, iconName) {
    const actual = num(value);
    const width = actual != null && total > 0 ? Math.max(0, Math.min(100, actual / total * 100)) : 0;
    return '<div class="distribution-row"><span class="distribution-name">' + icon(iconName || "package", "stats-list-icon") + '<span>' + esc(label) + '</span></span><span class="distribution-bar" data-empty="' + (actual == null || total <= 0) + '"><i data-tone="' + esc(tone || "muted") + '" style="--w:' + width.toFixed(1) + '%"></i></span><strong>' + esc(actual == null ? (view.locale === "en" ? "Not recorded" : "未记录") : actual) + '</strong></div>';
  }
  function sourceMark(value) {
    const file = value === "codex" ? (view.theme === "dark" ? "codex-dark" : "codex-light") : value === "claude_code" ? "claudecode" : "deepseek";
    return '<span class="fl-mark" data-size="sm"><img src="/icons/' + file + '.svg" alt="" aria-hidden="true"></span>';
  }
  function unavailableStatsCard(title, aside, message, listShape) {
    return '<section class="elevated-card stats-card stats-unavailable"><header class="fl-head"><h3>' + esc(title) + '</h3><span class="fl-aside">' + esc(aside) + '</span></header><div class="' + (listShape ? "fl-list " : "") + 'stats-unavailable-body">' + icon("slash") + '<span>' + esc(message) + '</span></div></section>';
  }
  function drawStats() {
    const data = cache.stats || {};
    const metrics = [
      { label: "资产", value: count(data.asset_count), icon: "package" },
      { label: "会话", value: count(data.session_count), icon: "layers" },
      { label: "需要注意", value: count(["silent", "broken", "bypassed"].reduce((sum, key) => sum + (num((data.state_counts || {})[key]) || 0), 0)), icon: "triangle-alert", tone: "bad" },
      { label: "几乎未使用", value: count((data.state_counts || {}).dormant), icon: "package-open", tone: "warn" },
      { label: "成本", value: view.locale === "en" ? "Not recorded" : "未记录", icon: "wallet" },
      { label: "Token 数", value: view.locale === "en" ? "Not recorded" : "未记录", icon: "hash" }
    ];
    const counts = data.state_counts || {};
    const total = Object.values(counts).reduce((sum, value) => sum + (num(value) || 0), 0);
    const distribution = Object.keys(counts).sort().map((key) => distributionBar(stateLabel(key), counts[key], total, stateTone(key), stateIcons[key])).join("");
    const sourceCounts = data.source_counts || {};
    const sourceTotal = Object.values(sourceCounts).reduce((sum, value) => sum + (num(value) || 0), 0);
    const sources = Object.keys(sourceCounts).sort().map((key) => '<div class="source-stat-row"><span class="source-stat-mark">' + sourceMark(key) + '</span><span class="source-stat-main"><span class="source-stat-name">' + esc(source(key)) + '</span><span class="source-stat-bar"><i style="--w:' + (sourceTotal ? (sourceCounts[key] / sourceTotal * 100).toFixed(1) : 0) + '%"></i></span></span><strong>' + esc(sourceCounts[key] == null ? (view.locale === "en" ? "Not recorded" : "未记录") : sourceCounts[key]) + '</strong><span class="source-stat-coverage">' + (view.locale === "en" ? "Not recorded" : "未记录") + '</span></div>').join("");
    const body = '<div class="stats-grid"><section class="elevated-card stats-card stats-card-metrics wide"><div class="metric-grid">' + metrics.map(metric).join("") + '</div></section><section class="elevated-card stats-card stats-card-activity wide"><header class="fl-head"><h3>活动</h3><span class="fl-aside">近 52 周 · 每格一天</span></header>' + activityHeatmap(data) + '</section><section class="elevated-card stats-card"><header class="fl-head"><h3>状态分布</h3><span class="fl-aside">' + esc(data.asset_count == null ? (view.locale === "en" ? "Not recorded" : "未记录") : data.asset_count) + '</span></header><div class="fl-list stats-fl-list">' + (distribution || '<div class="empty-copy">状态分布未记录。</div>') + '</div></section><section class="elevated-card stats-card"><header class="fl-head"><h3>数据源</h3><span class="fl-aside">' + esc(sourceTotal ? sourceTotal + (view.locale === "en" ? " sessions" : " 个会话") : (view.locale === "en" ? "Not recorded" : "未记录")) + '</span></header><div class="fl-list source-stat-list">' + (sources || '<div class="empty-copy">数据源未记录。</div>') + '</div></section>' + unavailableStatsCard("每日成本", view.locale === "en" ? "Not recorded" : "未记录", view.locale === "en" ? "Daily cost is not recorded." : "每日成本未记录。") + unavailableStatsCard("上下文开销", view.locale === "en" ? "Per session" : "每次会话", view.locale === "en" ? "Token and context usage are not recorded." : "Token 与上下文开销未记录。", true) + '</div>';
    document.getElementById("flatline-screen").innerHTML = header("统计", "近 30 天", '<button class="us-btn" data-variant="outline" data-size="sm" data-action="export-stats">导出</button>') + screenContent(body, "prototype-page");
    localizeDOM();
  }
  function timelineKind(value) {
    return value === "state_transition" ? (view.locale === "en" ? "State transition" : "状态迁移") : value === "asset_version" ? (view.locale === "en" ? "Asset change" : "资产变更") : value === "environment_changed" ? (view.locale === "en" ? "Environment change" : "环境变化") : eventLabel(value);
  }
  function timelineState(item, parsed) {
    const decision = parsed && parsed.decision && typeof parsed.decision === "object" ? parsed.decision : {};
    return (item && item.state) || parsed.to_state || parsed.state || decision.to_state || "";
  }
  function timelineEvidenceText(value) {
    const text = String(value == null ? "" : value).trim();
    if (!text || view.locale === "en") return text;
    const exact = {
      "participation is observed and no higher-priority detector is triggered": "已观察到参与，未触发更高优先级判定。",
      "no higher-priority detector is triggered": "未触发更高优先级判定。",
      "no same-shape opportunity is recorded in the current window": "当前窗口没有记录到同形状任务。",
      "opportunity denominator is absent; no opportunity is not zero participation": "相关任务分母未记录；没有相关任务不等于参与次数为零。",
      "no silent, broken, bypass, dormant, or degraded detector is triggered": "未触发沉默、引用失效、调用后未遵循、几乎未使用或使用减少判定。"
    };
    if (exact[text]) return exact[text];
    if (text === "asset age >= 720h0m0s and cumulative participation <= 2") return "资产记录至少 30 天且累计参与不超过 2 次。";
    const dormant = text.match(/^.+? is dormant: age (.+?); cumulative participation (\d+) \(threshold <= (\d+)\)$/);
    if (dormant) return "资产进入几乎未使用判定：记录时长 " + dormant[1] + "，累计参与 " + dormant[2] + " 次（阈值 ≤ " + dormant[3] + " 次）。";
    const notDormant = text.match(/^.+?: not dormant; age (.+?); cumulative participation (\d+) \(threshold <= (\d+)\)$/);
    if (notDormant) return "当前未达到几乎未使用判定：记录时长 " + notDormant[1] + "，累计参与 " + notDormant[2] + " 次（阈值 ≤ " + notDormant[3] + " 次）。";
    return "判定依据已记录。";
  }
  function timelineTone(value, evidence, stateValue) {
    const parsed = jsonObject(evidence);
    const state = stateValue || timelineState({}, parsed);
    if (["broken", "bypassed"].includes(state)) return "bad";
    if (["silent", "degraded", "awaiting_resurrection"].includes(state)) return "warn";
    if (state === "healthy") return "good";
    return value === "environment_changed" ? "accent" : value === "asset_version" ? "good" : "muted";
  }
  function timelineDetail(item, assets) {
    const parsed = jsonObject(item.evidence);
    const asset = assets[item.asset_id];
    const assetName = asset ? asset.name : item.asset_id || "未关联资产";
    const state = timelineState(item, parsed);
    const decision = parsed.decision && typeof parsed.decision === "object" ? parsed.decision : {};
    const stateText = state ? stateLabel(state) : "";
    const source = parsed.source || parsed.harness || parsed.agent || "";
    const field = parsed.field || "";
    const from = parsed.from == null ? "" : String(parsed.from);
    const to = parsed.to == null ? "" : String(parsed.to);
    const transition = from || to ? (view.locale === "en" ? " (" + (from || "Not recorded") + " → " + (to || "Not recorded") + ")" : "（" + (from || "未记录") + " → " + (to || "未记录") + "）") : "";
    let text = "";
    let detail = "";
    if (item.kind === "environment_changed") {
      text = field ? uiText("环境变化已记录：", "Environment change recorded: ") + field + transition : uiText("环境变化已记录", "Environment change recorded");
      detail = (source ? source + " · " : "") + (item.alignment ? uiText("时间对齐记录可下钻", "Drill into the time-alignment record") : uiText("环境事实", "Environment fact"));
    } else if (item.kind === "asset_version") {
      text = assetName + " · " + uiText("资产版本变化已记录", "Asset version change recorded");
      detail = item.alignment && item.alignment.startsWith("/") ? uiText("源路径：", "Source path: ") + item.alignment : uiText("资产版本事实已记录", "Asset version fact recorded");
    } else {
      if (view.locale === "en") {
        text = decision.summary || decision.reason || (assetName + (stateText ? " · transitioned to " + stateText : " · state transition recorded"));
        detail = decision.rule ? "Decision rule: " + decision.rule : (stateText ? "Current state: " + stateText : "State fact recorded");
      } else {
        text = assetName + (stateText ? " · 状态迁移为" + stateText : " · 状态迁移已记录");
        const readableRule = timelineEvidenceText(decision.rule);
        const readableReason = timelineEvidenceText(decision.summary || decision.reason);
        detail = decision.rule ? "判定规则：" + readableRule : (decision.summary || decision.reason ? readableReason : (stateText ? "当前状态：" + stateText : "状态事实已记录"));
      }
    }
    return {
      title: timelineKind(item.kind),
      text,
      detail,
      tone: timelineTone(item.kind, item.evidence, state),
      state,
      stateText,
      stateIcon: state ? (stateIcons[state] || "activity") : "",
      flag: item.kind === "state_transition" && ["broken", "bypassed", "silent"].includes(state) ? "thin" : item.kind === "state_transition" ? "new" : "neutral",
      link: item.asset_id ? (view.locale === "en" ? "View asset details" : "查看资产详情") : "",
      href: item.asset_id ? "#/assets/" + encodeURIComponent(item.asset_id) : ""
    };
  }
  function timelineIcon(item) {
    const evidence = jsonObject(item && item.evidence);
    if (item.kind === "environment_changed") return evidence.field === "initial_import" ? "camera" : "cpu";
    if (item.kind === "asset_version") return "file-diff";
    const state = timelineState(item, evidence);
    return stateIcons[state] || "activity";
  }
  function drawTimeline(data) {
    cache.timeline = data;
    const all = data.timeline || [];
    const assets = Object.fromEntries((cache.assets && cache.assets.assets || []).map((item) => [item.id, item]));
    const filters = [["all", "全部"], ["state_transition", "状态迁移"], ["asset_version", "资产变更"], ["environment_changed", "环境变化"]];
    const items = all.filter((item) => view.timelineFilter === "all" || item.kind === view.timelineFilter);
    const clusters = data.clusters || [];
    const clusterHTML = clusters.map((cluster) => '<div class="fl-cluster"><strong>' + esc(shortDate(cluster.at)) + "</strong> · " + esc(cluster.summary || ((cluster.asset_names || []).length + " 个资产的时间对齐记录")) + "</div>").join("");
    const nodes = items.slice().reverse().map((item) => {
      const detail = timelineDetail(item, assets);
      const nodeKind = item.kind === "environment_changed" ? "env" : item.kind === "asset_version" ? "asset" : "state";
      const stateHTML = detail.state ? '<span class="fl-state" data-state="' + esc(detail.state) + '">' + icon(detail.stateIcon) + esc(detail.stateText) + '</span>' : "";
      const linkHTML = detail.href ? '<a class="timeline-node-link" href="' + esc(detail.href) + '">' + esc(detail.link) + "</a>" : "";
      return '<article class="fl-node" data-kind="' + nodeKind + '" data-tone="' + detail.tone + '"><div class="fl-node-meta"><time>' + esc(shortDate(item.occurred_at)) + '</time><span class="fl-flag" data-flag="' + detail.flag + '">' + icon(timelineIcon(item)) + esc(detail.title) + '</span>' + stateHTML + '</div><div class="fl-node-text">' + esc(detail.text) + '</div><div class="fl-node-detail">' + esc(detail.detail) + '</div>' + linkHTML + "</article>";
    }).join("");
    const filtersHTML = '<div class="segmented timeline-filters">' + filters.map((item) => '<button class="segment-btn" type="button" data-action="timeline-filter" data-filter="' + item[0] + '" data-active="' + (view.timelineFilter === item[0]) + '">' + item[1] + "</button>").join("") + '</div>';
    const body = '<div class="fl-track">' + (clusterHTML || "") + (nodes || '<div class="empty-copy"><strong>尚无变化时间线记录。</strong>当前数据没有写入可展示的版本、环境或状态变化。</div>') + "</div>";
    document.getElementById("flatline-screen").innerHTML = header("变化时间线", quantity(items.length, "条", "record", "records") + " · 本地事实", filtersHTML) + screenContent(body, "timeline-page");
    localizeDOM();
  }
  function compactDuration(start, end) {
    if (!start || !end) return view.locale === "en" ? "Not recorded" : "未记录";
    const startTime = new Date(start).getTime();
    const endTime = new Date(end).getTime();
    if (!Number.isFinite(startTime) || !Number.isFinite(endTime)) return view.locale === "en" ? "Not recorded" : "未记录";
    const milliseconds = Math.max(0, endTime - startTime);
    if (milliseconds < 1000) return (milliseconds / 1000).toFixed(2) + "s";
    if (milliseconds < 60000) return (milliseconds / 1000).toFixed(1) + "s";
    const totalSeconds = Math.round(milliseconds / 1000);
    const totalMinutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    if (totalMinutes < 60) return totalMinutes + "m" + String(seconds).padStart(2, "0") + "s";
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    return hours + "h" + (minutes ? String(minutes).padStart(2, "0") + "m" : "");
  }
  function shortSessionID(value) {
    const text = String(value || "");
    const sourcePrefix = text.includes(":") ? text.slice(0, text.indexOf(":")) + ":" : "";
    const id = text.includes(":") ? text.slice(text.indexOf(":") + 1) : text;
    return sourcePrefix + (id.length > 12 ? id.slice(0, 8) + "…" + id.slice(-4) : id);
  }
  function compactEvidence(value, limit) {
    const text = String(value == null ? "" : value).replace(/\s+/g, " ").trim();
    if (!text) return "";
    const max = limit || 140;
    return text.length > max ? text.slice(0, max - 1) + "…" : text;
  }
  function sessionTitle(item) {
    return (item && (item.title || item.task_text)) || (view.locale === "en" ? "Untitled local session" : "未命名本地会话");
  }
  function sessionTask(item) {
    return (item && (item.task_text || item.title)) || (view.locale === "en" ? "Task text not recorded" : "任务文本未记录");
  }
  function sessionAssetLabel(item) {
    const value = num(item && item.asset_count);
    if (value == null) return view.locale === "en" ? "Asset participation not recorded" : "资产参与未记录";
    if (value === 0) return view.locale === "en" ? "No recorded asset use" : "没有资产被使用";
    return quantity(value, "个资产被使用", "asset used", "assets used");
  }
  function sessionAssetBadge(item) {
    const value = num(item && item.asset_count);
    if (value == null) return "";
    if (value === 0) return '<span class="fl-flag" data-flag="neutral">' + icon("slash") + (view.locale === "en" ? "No related asset record" : "没有相关资产记录") + '</span>';
    return '<span class="fl-flag" data-flag="new">' + icon("activity") + (view.locale === "en" ? "Asset use recorded" : "有资产参与记录") + '</span>';
  }
  function sessionListRowHTML(item) {
    return '<a class="fl-row session-row" href="#/sessions/' + encodeURIComponent(item.id) + '">' + sourceMark(item.source) + '<span class="session-main"><span class="session-title-line"><span class="session-title">' + esc(sessionTitle(item)) + '</span>' + sessionAssetBadge(item) + '</span><span class="session-meta">' + esc(source(item.source) + " · " + (item.cwd || "工作目录未记录") + " · " + shortDate(item.started_at) + " · " + shortSessionID(item.id)) + '</span></span><span class="session-assets-label">' + esc(sessionAssetLabel(item)) + '</span><span class="session-duration">' + esc(compactDuration(item.started_at, item.ended_at)) + '</span></a>';
  }
  function drawSessions() {
    resetSessionLazyRows();
    const allItems = cache.sessions || [];
    const query = view.search.trim().toLocaleLowerCase();
    const items = allItems.filter((item) => view.sessionSourceFilter === "all" || item.source === view.sessionSourceFilter).filter((item) => !view.sessionOnlyRecorded || num(item.transcript_count) > 0).filter((item) => !query || [item.id, item.source_session_id, item.title, item.task_text, item.source, item.cwd, item.model].join(" ").toLocaleLowerCase().includes(query)).sort((a, b) => {
      if (view.sessionSort === "events") return (num(b.event_count) || 0) - (num(a.event_count) || 0);
      return String(b.started_at || "").localeCompare(String(a.started_at || ""));
    });
    const sourceFilters = [["all", view.locale === "en" ? "All harnesses" : "全部 harness"], ["claude_code", "Claude Code"], ["codex", "Codex"]];
    const filterButtons = sourceFilters.map(([value, label]) => '<button class="segment-btn" type="button" data-action="session-filter" data-source-filter="' + value + '" data-active="' + (view.sessionSourceFilter === value) + '">' + label + '</button>').join("");
    const toolbar = '<div class="session-list-toolbar"><div class="segmented session-source-filters">' + filterButtons + '</div><label class="session-recorded-filter"><input type="checkbox" data-action="session-recorded" ' + (view.sessionOnlyRecorded ? "checked" : "") + '><span>' + (view.locale === "en" ? "Only sessions with transcript" : "只看有 transcript 的会话") + '</span></label><div class="session-list-spacer"></div><select class="session-sort-select" data-action="session-sort" aria-label="' + (view.locale === "en" ? "Sort sessions" : "会话排序") + '"><option value="recent" ' + (view.sessionSort === "recent" ? "selected" : "") + '>' + (view.locale === "en" ? "Most recent" : "最近开始") + '</option><option value="events" ' + (view.sessionSort === "events" ? "selected" : "") + '>' + (view.locale === "en" ? "Most events" : "事件最多") + '</option></select></div>';
    const empty = view.locale === "en" ? "No sessions match the current filters." : "当前筛选下没有会话记录。";
    const rows = sessionLazyMarkup(items, sessionListRowHTML, 58);
    const body = toolbar + '<div class="session-list-scroll"><div class="session-list">' + (rows || '<div class="empty-copy"><strong>' + empty + '</strong><span>' + (view.locale === "en" ? "Change the harness or transcript filter, or clear the search." : "请更换 harness、transcript 筛选，或清空搜索。") + '</span></div>') + "</div></div>";
    const headerRight = '<span class="session-list-count">' + esc(quantity(items.length, "个会话", "session", "sessions")) + '</span><button class="us-btn" data-variant="default" data-size="sm" data-action="reload-sessions">' + icon("refreshCw") + (view.locale === "en" ? "Rescan" : "重新扫描") + '</button>';
    document.getElementById("flatline-screen").innerHTML = header("会话", view.locale === "en" ? "Browse and filter sessions" : "浏览与筛选会话", headerRight) + screenContent(body, "session-page", "session-page-scroll");
    localizeDOM();
    armSessionLazyRows();
  }
  function eventKind(item) {
    const value = item.event_type || "event";
    const payload = jsonObject(item && item.payload);
    if (value === "transcript_tool_call") return "tool";
    // The prototype's trajectory has seven visual kinds. A native tool
    // result is the output of the same tool lane, not a new green kind.
    if (value === "transcript_tool_result") return "tool";
    if (value === "transcript_message") return payload.role === "user" ? "user" : payload.role === "assistant" ? "message" : "context";
    if (value.includes("tool")) return "tool";
    if (value.includes("user")) return "user";
    if (value.includes("context") || value.includes("load") || value.includes("environment")) return "context";
    // Asset evidence is a recorded context fact in the prototype ledger;
    // keep the event title intact while using the canonical context palette.
    if (value.includes("asset")) return "context";
    return "system";
  }
  function ledgerKindLabel(kind) {
    const labels = {
      user: view.locale === "en" ? "User" : "用户",
      message: view.locale === "en" ? "Message" : "消息",
      tool: view.locale === "en" ? "Tool" : "工具",
      context: view.locale === "en" ? "Context" : "上下文",
      compacted: view.locale === "en" ? "Compacted" : "压缩",
      subtool: view.locale === "en" ? "Sub-call" : "子调用",
      system: view.locale === "en" ? "System" : "系统"
    };
    return labels[kind] || labels.system;
  }
  function toolDisplayName(payload, fallback) {
    const name = typeof payload.tool_name === "string" ? payload.tool_name.trim() : "";
    if (!name || /^(?:toolu|tool_use|call)_[a-z0-9_-]+$/i.test(name)) return fallback;
    return name;
  }
  function eventTitle(item) {
    const payload = jsonObject(item && item.payload);
    if (item && item.asset_id) return item.asset_id;
    if (item && item.event_type === "transcript_tool_call") return toolDisplayName(payload, view.locale === "en" ? "Tool call" : "工具调用");
    if (item && item.event_type === "transcript_tool_result") {
      if (payload.is_error === true || (payload.exit_code != null && String(payload.exit_code) !== "0")) return view.locale === "en" ? "Tool failure" : "工具失败";
      return toolDisplayName(payload, view.locale === "en" ? "Tool result" : "工具结果");
    }
    if (item && item.event_type === "transcript_message") return compactEvidence(payload.text, 180) || (view.locale === "en" ? "Session message" : "会话消息");
    if (item && item.event_type === "environment_changed") return view.locale === "en" ? "Environment change" : "环境变化";
    if (item && item.event_type === "session_started") return view.locale === "en" ? "Session started" : "会话开始";
    return eventLabel(item && item.event_type);
  }
  function eventPreview(item) {
    const payload = jsonObject(item && item.payload);
    if (item && item.event_type === "transcript_message") return compactEvidence(payload.text, 180) || (view.locale === "en" ? "Message text not recorded" : "消息文本未记录");
    if (item && item.event_type === "transcript_tool_call") return compactEvidence(payload.tool_input, 160) || (view.locale === "en" ? "Tool input not recorded" : "工具输入未记录");
    if (item && item.event_type === "transcript_tool_result") {
      const output = compactEvidence(payload.tool_output, 160) || (view.locale === "en" ? "Tool output not recorded" : "工具输出未记录");
      if (payload.is_error === true) return (view.locale === "en" ? "Tool failed · " : "工具失败 · ") + output;
      if (payload.exit_code != null && String(payload.exit_code) !== "0") return (view.locale === "en" ? "Non-zero exit " : "非零退出码 ") + payload.exit_code + " · " + output;
      return output;
    }
    return obs(item && item.observation_level) + " · " + (item && item.participation_signal ? signal(item.participation_signal) : (view.locale === "en" ? "Participation form not recorded" : "参与形式未记录"));
  }
  function eventRow(event, index) {
    return '<div class="event-row" data-action="select-event" data-event-index="' + index + '" data-selected="' + (index === view.selectedEvent) + '"><time>' + esc(shortDate(event.occurred_at)) + '</time><span class="fl-kind" data-kind="' + eventKind(event) + '">' + esc(eventLabel(event.event_type)) + '</span><span class="event-detail"><strong class="event-title">' + esc(eventTitle(event)) + '</strong><small class="event-evidence">' + esc(eventPreview(event)) + '</small></span></div>';
  }
  function eventClock(value) {
    if (!value) return view.locale === "en" ? "Not recorded" : "未记录";
    const parsed = new Date(value);
    if (!Number.isFinite(parsed.getTime())) return view.locale === "en" ? "Not recorded" : "未记录";
    return parsed.toISOString().slice(11, 19);
  }
  function eventElapsed(current, previous) {
    if (!current || !previous) return view.locale === "en" ? "—" : "—";
    const delta = new Date(current).getTime() - new Date(previous).getTime();
    if (!Number.isFinite(delta) || delta < 0) return "—";
    if (delta < 1000) return delta + " ms";
    if (delta < 60000) return (delta / 1000).toFixed(1) + " s";
    return Math.floor(delta / 60000) + "m " + Math.floor((delta % 60000) / 1000) + "s";
  }
  function payloadSize(value) {
    const text = typeof value === "string" ? value : displayValue(value, "");
    if (!text) return "—";
    const bytes = new TextEncoder().encode(text).length;
    return bytes < 1024 ? bytes + " B" : (bytes / 1024).toFixed(1) + " KB";
  }
  function sessionTurnGroups(events) {
    const groups = [];
    let current = null;
    events.forEach((event, index) => {
      if (!current || eventKind(event) === "user") {
        current = { number: groups.length + 1, events: [] };
        groups.push(current);
      }
      current.events.push({ event, index });
    });
    return groups;
  }
  function sessionRowCells(event, index, previous) {
    const payload = jsonObject(event && event.payload);
    const kind = eventKind(event);
    const title = eventTitle(event);
    const preview = eventPreview(event);
    const input = event.event_type === "transcript_tool_call" ? payloadSize(payload.tool_input) : "—";
    const output = event.event_type === "transcript_tool_result" ? payloadSize(payload.tool_output) : "—";
    const step = kind === "tool" ? (event.event_type === "transcript_tool_result" ? (view.locale === "en" ? "tool output" : "工具输出") : toolDisplayName(payload, title)) : "";
    return {
      event,
      index,
      kind,
      title,
      preview,
      label: ledgerKindLabel(kind),
      clock: eventClock(event.occurred_at),
      elapsed: view.sessionShowDuration ? eventElapsed(event.occurred_at, previous && previous.occurred_at) : "—",
      input,
      output,
      step,
      searchable: [title, preview, step, payload.role, payload.tool_name, event.event_type].join(" ").toLocaleLowerCase()
    };
  }
  function eventPayload(item) {
    const payload = jsonObject(item && item.payload);
    return Object.keys(payload).length ? JSON.stringify(payload, null, 2) : "事件载荷未记录。";
  }
  function displayValue(value, fallback) {
    if (value == null || value === "") return fallback;
    if (typeof value === "string") return value;
    try { return JSON.stringify(value); } catch (_) { return String(value); }
  }
  function inspectorList(items) {
    return '<div class="fl-list session-inspector-list">' + items.map(([label, value]) => '<div class="fl-li"><span>' + esc(label) + '</span><b>' + esc(value) + '</b></div>').join("") + '</div>';
  }

  function sessionDerivedFor(data, events) {
    if (view.sessionDerived && view.sessionDerived.data === data && view.sessionDerived.locale === view.locale) return view.sessionDerived;
    const rows = events.map((event, index) => sessionRowCells(event, index, events[index - 1]));
    const eventTimes = events.map((event) => new Date(event.occurred_at).getTime()).filter(Number.isFinite);
    const groups = sessionTurnGroups(events);
    const loadedBySource = new Map(events.map((event, index) => [event.source_event_id || String(event.id || ""), { event, index }]));
    const frictionRows = [];
    const seenFriction = new Set();
    const projected = data && data.friction && Array.isArray(data.friction.records) ? data.friction.records : [];
    projected.forEach((record) => {
      const key = record.source_event_id || String(record.id || "");
      const loaded = loadedBySource.get(key);
      const event = loaded ? Object.assign({}, loaded.event, record, { id: loaded.event.id, payload: record.payload || loaded.event.payload, locator: record.locator || loaded.event.locator }) : Object.assign({}, record);
      frictionRows.push({ event, index: loaded ? loaded.index : -1, projected: true });
      if (key) seenFriction.add(key);
    });
    events.forEach((event, index) => {
      const payload = jsonObject(event && event.payload);
      const key = event.source_event_id || String(event.id || "");
      const explicit = event.event_type === "asset_violation" || payload.is_error === true || (payload.exit_code != null && String(payload.exit_code) !== "0");
      if (explicit && !seenFriction.has(key)) {
        frictionRows.push({ event, index, projected: false });
        if (key) seenFriction.add(key);
      }
    });
    const searchable = events.map((event) => [eventTitle(event), eventPreview(event), event.event_type, JSON.stringify(event.payload || {})].join(" ").toLocaleLowerCase());
    view.sessionDerived = { data, locale: view.locale, rows, eventTimes, groups, frictionRows, searchable };
    return view.sessionDerived;
  }

  function resetSessionLazyRows() {
    if (sessionLazyObserver) sessionLazyObserver.disconnect();
    sessionLazyObserver = null;
    if (sessionLazyRoot && sessionLazyScrollHandler) sessionLazyRoot.removeEventListener("scroll", sessionLazyScrollHandler);
    sessionLazyRoot = null;
    sessionLazyScrollHandler = null;
    sessionLazyBatches = [];
  }

  function sessionLazyMarkup(items, renderItem, intrinsicSize, itemWeight) {
    if (!items.length) return "";
    const weightOf = itemWeight || (() => 1);
    const totalWeight = items.reduce((total, item) => total + Math.max(1, weightOf(item)), 0);
    if (totalWeight <= SESSION_ROW_CHUNK) return items.map(renderItem).join("");
    const batchIndex = sessionLazyBatches.length;
    let rendered = 0;
    let renderedWeight = 0;
    while (rendered < items.length && (renderedWeight === 0 || renderedWeight < SESSION_ROW_CHUNK)) {
      renderedWeight += Math.max(1, weightOf(items[rendered]));
      rendered += 1;
    }
    sessionLazyBatches.push({ items, renderItem, rendered, renderedWeight, totalWeight, weightOf, intrinsicSize });
    const remainingWeight = totalWeight - renderedWeight;
    return items.slice(0, rendered).map(renderItem).join("") + '<span class="session-lazy-spacer" data-session-sentinel="' + batchIndex + '" style="height:' + Math.max(intrinsicSize, remainingWeight * intrinsicSize) + 'px" aria-hidden="true"></span>';
  }

  function hydrateSessionBatch(batchIndex) {
    const batch = sessionLazyBatches[batchIndex];
    const sentinel = document.querySelector('[data-session-sentinel="' + batchIndex + '"]');
    if (!batch || !sentinel) return;
    let next = batch.rendered;
    let addedWeight = 0;
    while (next < batch.items.length && (addedWeight === 0 || addedWeight < SESSION_ROW_CHUNK)) {
      addedWeight += Math.max(1, batch.weightOf(batch.items[next]));
      next += 1;
    }
    if (next <= batch.rendered) {
      sentinel.remove();
      return;
    }
    sentinel.insertAdjacentHTML("beforebegin", batch.items.slice(batch.rendered, next).map(batch.renderItem).join(""));
    batch.rendered = next;
    batch.renderedWeight += addedWeight;
    const remainingWeight = batch.totalWeight - batch.renderedWeight;
    if (remainingWeight <= 0) sentinel.remove();
    else sentinel.style.height = Math.max(batch.intrinsicSize, remainingWeight * batch.intrinsicSize) + "px";
  }

  function sessionLedgerRowHTML(row, selectedIndex) {
    return '<button type="button" class="session-ledger-row" data-action="select-event" data-event-index="' + row.index + '" data-selected="' + (row.index === selectedIndex) + '" data-kind="' + row.kind + '"><span class="session-row-index">#' + (row.index + 1) + '</span><span class="session-row-branch">' + (row.event.event_type === "transcript_tool_result" ? "└" : "") + '</span><span class="fl-kind" data-kind="' + row.kind + '">' + esc(row.label) + '</span><span class="session-row-fact"><strong class="event-title">' + esc(row.title) + '</strong><small class="event-evidence">' + esc(row.preview) + '</small></span><span class="session-row-measures"><span>' + esc(row.input) + '</span><span>' + esc(row.output) + '</span><span>' + esc(row.step || "—") + '</span><span>' + esc(row.elapsed) + '</span></span></button>';
  }

  function sessionLedgerChunks(filteredGroups) {
    const segments = [];
    filteredGroups.forEach(({ group, rows: groupRows, collapsed }) => {
      if (collapsed || !groupRows.length) {
        segments.push({ group, rows: [], collapsed, sourceCount: group.events.length });
        return;
      }
      for (let offset = 0; offset < groupRows.length; offset += SESSION_ROW_CHUNK) {
        segments.push({ group, rows: groupRows.slice(offset, offset + SESSION_ROW_CHUNK), collapsed: false, continued: offset > 0, sourceCount: group.events.length });
      }
    });
    const batches = [];
    let batch = [];
    let rowCount = 0;
    segments.forEach((segment) => {
      const segmentRows = segment.rows.length;
      if (batch.length && rowCount + segmentRows > SESSION_ROW_CHUNK) {
        batches.push(batch);
        batch = [];
        rowCount = 0;
      }
      batch.push(segment);
      rowCount += segmentRows;
      if (rowCount >= SESSION_ROW_CHUNK) {
        batches.push(batch);
        batch = [];
        rowCount = 0;
      }
    });
    if (batch.length) batches.push(batch);
    return batches;
  }

  function sessionLedgerChunkHTML(chunk, selectedIndex) {
    const headerCount = view.locale === "en" ? chunk.sourceCount + " records" : chunk.sourceCount + " 条记录";
    const rowHTML = chunk.rows.map((row) => sessionLedgerRowHTML(row, selectedIndex)).join("");
    return '<section class="session-turn-group" data-open="' + (!chunk.collapsed) + '"' + (chunk.continued ? ' data-continuation="true"' : "") + '><div class="session-turn-head"><button type="button" data-action="session-turn" data-turn="' + chunk.group.number + '" aria-expanded="' + (!chunk.collapsed) + '"><strong>' + (view.locale === "en" ? "Turn " : "第 ") + chunk.group.number + (view.locale === "en" ? "" : " 轮") + '</strong><span>' + esc(headerCount) + '</span></button><span class="session-turn-columns"><span>' + (view.locale === "en" ? "Input" : "输入") + '</span><span>' + (view.locale === "en" ? "Output" : "输出") + '</span><span>' + (view.locale === "en" ? "Thinking" : "思考") + '</span><span>' + (view.locale === "en" ? "Duration" : "耗时") + '</span></span></div>' + rowHTML + '</section>';
  }

  function sessionLedgerBatchHTML(batch, selectedIndex) {
    return batch.map((chunk) => sessionLedgerChunkHTML(chunk, selectedIndex)).join("");
  }

  function sessionChatRowHTML(entry, selectedIndex) {
    const event = entry.event;
    const index = entry.index;
    const payload = jsonObject(event.payload);
    const kind = eventKind(event);
    const role = payload.role || (kind === "tool" ? "tool" : kind);
    const code = event.event_type === "transcript_tool_call" ? payload.tool_input : event.event_type === "transcript_tool_result" ? payload.tool_output : "";
    const action = index >= 0 ? "select-event" : "select-friction";
    const frictionIndex = entry.frictionIndex == null ? "" : ' data-friction-index="' + entry.frictionIndex + '"';
    return '<button type="button" class="session-chat-row" data-action="' + action + '" data-event-index="' + index + '"' + frictionIndex + ' data-selected="' + (index === selectedIndex) + '"><span class="session-chat-time"><span>' + esc(eventClock(event.occurred_at)) + '</span><small>' + esc(role) + '</small></span><span class="session-chat-content"><span class="session-chat-kind"><span class="fl-kind" data-kind="' + kind + '">' + esc(ledgerKindLabel(kind)) + '</span>' + (event.participation_signal ? '<span class="session-chat-signal">' + esc(signal(event.participation_signal)) + '</span>' : '') + '</span><strong class="event-title">' + esc(eventTitle(event)) + '</strong><span class="session-chat-preview">' + esc(eventPreview(event)) + '</span>' + (code ? '<pre class="session-chat-code">' + esc(code) + '</pre>' : '') + '<small class="session-chat-locator">' + esc(displayValue(event.locator, view.locale === "en" ? "Locator not recorded" : "定位信息未记录")) + '</small></span></button>';
  }

  async function loadNextSessionPage() {
    const state = view.sessionPageState;
    const data = view.sessionData;
    const sessionID = view.sessionID;
    if (!state || !data || !sessionID || state.loading || !state.hasMore) return;
    state.loading = true;
    const rootBefore = sessionLazyRoot;
    const scrollTop = rootBefore ? rootBefore.scrollTop : 0;
    const offset = state.offset;
    try {
      const result = await get("/api/v1/sessions/" + encodeURIComponent(sessionID) + "?events=page&offset=" + offset + "&limit=" + SESSION_EVENT_PAGE_SIZE);
      if (view.sessionData !== data || view.sessionID !== sessionID) return;
      const incoming = Array.isArray(result.events) ? result.events : [];
      if (!incoming.length) {
        state.hasMore = false;
        state.loading = false;
        drawSessionDetail(data);
        return;
      }
      data.events = (Array.isArray(data.events) ? data.events : []).concat(incoming);
      state.offset = offset + incoming.length;
      state.total = num(result.event_total) == null ? state.total : result.event_total;
      state.hasMore = Boolean(result.events_has_more);
      state.loading = false;
      view.sessionDerived = null;
      drawSessionDetail(data);
      const rootAfter = document.querySelector(view.sessionTab === "chat" ? ".session-chat-scroll" : ".session-event-scroll");
      if (rootAfter) rootAfter.scrollTop = scrollTop;
    } catch (error) {
      state.loading = false;
      notify((view.locale === "en" ? "Could not load the next local event page: " : "无法继续读取本地事件：") + error.message, "error");
    }
  }

  function armSessionLazyRows() {
    const root = location.hash === "#/sessions" ? document.querySelector(".session-list-scroll") : document.querySelector(view.sessionTab === "chat" ? ".session-chat-scroll" : ".session-event-scroll");
    if (!root) return;
    sessionLazyRoot = root;
    const observe = () => {
      root.querySelectorAll("[data-session-sentinel], [data-session-page-sentinel]").forEach((sentinel) => sessionLazyObserver && sessionLazyObserver.observe(sentinel));
    };
    if (typeof IntersectionObserver === "function") {
      sessionLazyObserver = new IntersectionObserver((entries) => {
        entries.forEach((entry) => {
          if (!entry.isIntersecting) return;
          const target = entry.target;
          if (target.hasAttribute("data-session-sentinel")) hydrateSessionBatch(Number(target.getAttribute("data-session-sentinel")));
          if (target.hasAttribute("data-session-page-sentinel")) loadNextSessionPage();
        });
      }, { root, rootMargin: "720px 0px" });
      observe();
      return;
    }
    sessionLazyScrollHandler = () => {
      root.querySelectorAll("[data-session-sentinel]").forEach((sentinel) => {
        const rect = sentinel.getBoundingClientRect();
        if (rect.top < window.innerHeight + 720) hydrateSessionBatch(Number(sentinel.getAttribute("data-session-sentinel")));
      });
      if (root.scrollTop + root.clientHeight >= root.scrollHeight - 720) loadNextSessionPage();
    };
    root.addEventListener("scroll", sessionLazyScrollHandler, { passive: true });
  }

  function hydrateSelectedEventPayload() {
    const data = view.sessionData;
    const index = view.selectedEvent;
    if (view.selectedFriction || index < 0) return;
    const event = data && Array.isArray(data.events) ? data.events[index] : null;
    if (!event || (!event.payload_truncated && !event.locator_truncated) || event.__fullPayload) return;
    const sessionID = view.sessionID;
    get("/api/v1/sessions/" + encodeURIComponent(sessionID) + "/events/" + encodeURIComponent(event.id)).then((result) => {
      if (view.sessionData !== data || view.selectedEvent !== index) return;
      Object.assign(event, result.event || {});
      event.__fullPayload = true;
      view.sessionDerived = null;
      drawSessionDetail(data);
    }).catch((error) => notify((view.locale === "en" ? "Could not load the full selected event: " : "无法读取选中事件的完整本地证据：") + error.message, "error"));
  }

  function drawSessionDetail(data) {
    resetSessionLazyRows();
    view.sessionData = data;
    const item = data.session || {};
    const events = Array.isArray(data.events) ? data.events : [];
    const derived = sessionDerivedFor(data, events);
    const selectedFriction = view.selectedFriction;
    const selectedIndex = selectedFriction ? -1 : Math.max(0, Math.min(view.selectedEvent, Math.max(0, events.length - 1)));
    view.selectedEvent = selectedIndex;
    const selected = selectedFriction || events[selectedIndex];
    const rows = derived.rows;
    const query = view.sessionQuery.trim().toLocaleLowerCase();
    const eventTimes = derived.eventTimes;
    const startTime = eventTimes.length ? eventTimes.reduce((value, item) => Math.min(value, item), Infinity) : 0;
    const endTime = eventTimes.length ? eventTimes.reduce((value, item) => Math.max(value, item), -Infinity) : startTime + 1;
    const timeSpan = Math.max(1, endTime - startTime);
    const groups = derived.groups;
    const showingChat = view.sessionTab === "chat";
    const laneOf = (kind) => kind === "tool" || kind === "subtool" ? 2 : kind === "message" || kind === "compacted" ? 1 : 0;
    const laneLabel = view.locale === "en" ? ["Input", "Model", "Tools"] : ["输入", "模型", "工具"];
    const overviewBars = showingChat ? "" : rows.map((row) => {
      const occurred = new Date(row.event.occurred_at).getTime();
      const left = Number.isFinite(occurred) ? Math.max(0, Math.min(99.5, (occurred - startTime) / timeSpan * 100)) : 0;
      const next = events[row.index + 1] && new Date(events[row.index + 1].occurred_at).getTime();
      const measuredWidth = Number.isFinite(next) && Number.isFinite(occurred) ? (next - occurred) / timeSpan * 100 : 0;
      const width = eventTimes.length > 1 ? Math.max(0.55, Math.min(16, measuredWidth || 100 / eventTimes.length)) : 2;
      return '<button type="button" class="session-overview-event" data-kind="' + row.kind + '" data-action="select-event" data-event-index="' + row.index + '" data-selected="' + (row.index === selectedIndex) + '" style="left:' + left.toFixed(3) + '%;width:' + width.toFixed(3) + '%;--lane:' + laneOf(row.kind) + '" title="' + esc("#" + (row.index + 1) + " " + row.label + " · " + eventClock(row.event.occurred_at)) + '"></button>';
    }).join("");
    const overviewRange = eventTimes.length ? (view.locale === "en" ? "Actual duration · " : "实际耗时 · ") + compactDuration(new Date(startTime).toISOString(), new Date(endTime).toISOString()) : (view.locale === "en" ? "Event time not recorded" : "事件时间未记录");
    const totalEventCount = num(item.event_count);
    const loadedEventLabel = totalEventCount != null && totalEventCount > events.length ? (view.locale === "en" ? "Loaded " + events.length + " / " + totalEventCount + " records" : "已加载 " + events.length + " / " + totalEventCount + " 条记录") : quantity(events.length, "条记录", "record", "records");
    const overviewCount = loadedEventLabel + " · " + (view.locale === "en" ? groups.length + (totalEventCount != null && totalEventCount > events.length ? " loaded turns" : " turns") : groups.length + (totalEventCount != null && totalEventCount > events.length ? " 个已加载回合" : " 轮"));
    const taskText = sessionTask(item);
    const sessionHeader = '<header class="detail-header session-shell-header"><a class="back-link" href="#/sessions" aria-label="返回会话列表">' + icon("arrowLeft") + '</a><div class="detail-identity"><span class="session-shell-title"><h1>' + (view.locale === "en" ? "Session" : "会话") + '</h1></span><span class="detail-subline">' + esc(shortSessionID(item.id || item.source_session_id) + " · " + source(item.source) + (item.cwd ? " · " + item.cwd : "")) + '</span></div><div class="session-header-actions"><span class="fl-flag" data-flag="new">' + icon("activity") + (view.locale === "en" ? "Recorded" : "已记录") + '</span></div></header>';
    const taskLine = '<div class="session-task-line"><span class="session-task-label">' + (view.locale === "en" ? "Task" : "任务") + '</span><span class="session-task-value">' + esc(taskText) + '</span></div>';
    const tabs = '<div class="detail-tabbar session-tabbar"><div class="us-tabs"><button class="us-tabs-trigger" type="button" data-action="session-tab" data-tab="trajectory" data-active="' + (view.sessionTab === "trajectory") + '">' + (view.locale === "en" ? "Trajectory" : "轨迹") + '</button><button class="us-tabs-trigger" type="button" data-action="session-tab" data-tab="chat" data-active="' + (view.sessionTab === "chat") + '">' + (view.locale === "en" ? "Conversation" : "对话") + '</button></div></div>';
    const filteredGroups = showingChat ? [] : groups.map((group) => {
      let groupRows = group.events.map((entry) => rows[entry.index]);
      if (view.sessionFoldCalls) groupRows = groupRows.filter((row) => row.kind !== "tool" && row.kind !== "subtool");
      if (query) groupRows = groupRows.filter((row) => row.searchable.includes(query));
      const collapsed = view.sessionFoldTurns || view.sessionCollapsedTurns[group.number];
      return { group, rows: groupRows, collapsed };
    }).filter((group) => group.rows.length || !query);
    const ledgerChunks = sessionLedgerChunks(filteredGroups);
    const ledgerGroups = showingChat ? "" : sessionLazyMarkup(ledgerChunks, (batch) => sessionLedgerBatchHTML(batch, selectedIndex), 52, (batch) => batch.reduce((total, chunk) => total + chunk.rows.length, 0));
    const selectedPayload = jsonObject(selected && selected.payload);
    const notRecorded = view.locale === "en" ? "Not recorded" : "未记录";
    const inspector = selected ? '<div class="session-inspector-selected"><div class="session-inspector-kicker"><span class="session-inspector-index">' + (selectedIndex >= 0 ? "#" + (selectedIndex + 1) : (view.locale === "en" ? "Friction" : "摩擦记录")) + '</span><span class="fl-kind" data-kind="' + eventKind(selected) + '">' + esc(ledgerKindLabel(eventKind(selected))) + '</span>' + (selected.participation_signal ? '<span class="fl-flag" data-flag="new">' + esc(signal(selected.participation_signal)) + '</span>' : '') + '</div><p class="session-inspector-title">' + esc(eventTitle(selected)) + '</p><p class="session-inspector-evidence event-evidence">' + esc(eventPreview(selected)) + '</p><div class="session-inspector-label">' + (view.locale === "en" ? "Timing" : "耗时") + '</div>' + inspectorList([[view.locale === "en" ? "Event time" : "事件时间", date(selected.occurred_at)], [view.locale === "en" ? "Relative gap" : "相对间隔", rows[selectedIndex] && rows[selectedIndex].elapsed || "—"], [view.locale === "en" ? "Role" : "角色", selectedPayload.role || notRecorded]]) + '<div class="session-inspector-label">' + (view.locale === "en" ? "Token usage" : "Token 用量") + '</div>' + inspectorList([[view.locale === "en" ? "Input" : "输入", rows[selectedIndex] && rows[selectedIndex].input || "—"], [view.locale === "en" ? "Output" : "输出", rows[selectedIndex] && rows[selectedIndex].output || "—"], [view.locale === "en" ? "Tokens" : "Token 数", notRecorded]]) + '<div class="session-inspector-label">' + (view.locale === "en" ? "Observation" : "观测") + '</div>' + inspectorList([[view.locale === "en" ? "Observation level" : "观测等级", obs(selected.observation_level)], [view.locale === "en" ? "Participation" : "参与形式", selected.participation_signal ? signal(selected.participation_signal) : notRecorded]]) + '<div class="session-inspector-locator"><span>' + (view.locale === "en" ? "Locator" : "定位信息") + '</span><code>' + esc(displayValue(selected.locator, notRecorded)) + '</code></div><div class="session-inspector-label">' + (view.locale === "en" ? "Event payload" : "事件载荷") + '</div><pre class="event-payload">' + esc(eventPayload(selected)) + "</pre></div>" : '<div class="empty-copy"><strong>' + (view.locale === "en" ? "No event selected." : "未选择事件。") + '</strong><span>' + (view.locale === "en" ? "Select a ledger row to inspect its evidence." : "选择左侧记录查看证据。") + '</span></div>';
    const ecm = '<div class="session-ecm"><div class="session-ecm-intro"><strong>' + (view.locale === "en" ? "Session evidence context" : "会话证据上下文") + '</strong><span>' + (view.locale === "en" ? "Facts below are read from the local native history." : "以下事实来自本地原生历史记录。") + '</span></div><dl class="session-inspector-facts"><div><dt>' + (view.locale === "en" ? "Source" : "来源") + '</dt><dd>' + esc(source(item.source)) + '</dd></div><div><dt>' + (view.locale === "en" ? "Model" : "模型") + '</dt><dd>' + esc(item.model || (view.locale === "en" ? "Not recorded" : "未记录")) + '</dd></div><div><dt>' + (view.locale === "en" ? "Messages" : "消息数") + '</dt><dd>' + esc(item.transcript_count == null ? count(item.transcript_count) : item.transcript_count) + '</dd></div><div><dt>' + (view.locale === "en" ? "Events" : "事件数") + '</dt><dd>' + esc(item.event_count == null ? events.length : item.event_count) + '</dd></div><div><dt>' + (view.locale === "en" ? "Working directory" : "工作目录") + '</dt><dd>' + esc(item.cwd || (view.locale === "en" ? "Not recorded" : "未记录")) + '</dd></div></dl><div class="session-ecm-note">' + icon("slash") + '<span>' + (view.locale === "en" ? "Transcript fields are bounded during ingestion. Missing fields remain not recorded." : "采集时会限制 transcript 字段长度；缺失字段保持未记录。") + '</span></div></div>';
    const frictionRows = derived.frictionRows;
    const hasFrictionProjection = Boolean(data && data.friction && data.friction.complete);
    const frictionCount = hasFrictionProjection ? Number(data.friction.count || 0) : frictionRows.length;
    const frictionBrowseNote = hasFrictionProjection && data.friction.records_truncated ? (view.locale === "en" ? "Showing the first " + frictionRows.length + " loaded records; total explicit records: " + frictionCount + "." : "当前展示前 " + frictionRows.length + " 条已加载记录；明确摩擦总数：" + frictionCount + " 条。") : "";
    const friction = view.sessionInspectorTab === "friction" ? '<div class="session-friction-pane"><div class="session-ecm-intro"><strong>' + (view.locale === "en" ? "Friction axis" : "摩擦轴") + '</strong><span>' + (view.locale === "en" ? "Tool failures require an explicit is_error or non-zero exit code; asset bypass remains a separate record. Missing tool results stay not recorded." : "工具失败必须有明确的 is_error 或非零退出码；资产绕行单独记录。缺失的工具结果保持未记录。") + '</span></div><div class="session-friction-summary"><strong>' + (hasFrictionProjection ? frictionCount : (view.locale === "en" ? "Not recorded" : "未记录")) + '</strong><span>' + (view.locale === "en" ? "explicit friction records" : "明确摩擦记录") + '</span></div>' + (frictionBrowseNote ? '<p class="session-friction-note">' + esc(frictionBrowseNote) + '</p>' : '') + (frictionRows.length ? '<div class="session-friction-list">' + frictionRows.map(({ event, index }, frictionIndex) => '<button type="button" class="session-friction-row" data-action="select-friction" data-event-index="' + index + '" data-friction-index="' + frictionIndex + '"><span class="fl-kind" data-kind="' + eventKind(event) + '">' + esc(ledgerKindLabel(eventKind(event))) + '</span><span><strong class="event-title">' + esc(eventTitle(event)) + '</strong><small class="event-evidence">' + esc(eventPreview(event)) + '</small></span><span>' + esc(eventClock(event.occurred_at)) + '</span></button>').join("") + '</div>' : '<div class="empty-copy"><strong>' + (hasFrictionProjection ? (view.locale === "en" ? "No explicit tool failure or asset bypass was detected." : "没有检测到明确的工具失败或资产绕行记录。") : (view.locale === "en" ? "Friction projection not recorded." : "摩擦投影尚未记录。")) + '</strong><span>' + (view.locale === "en" ? "The daemon checked the stored session evidence; ordinary output is not classified as failure." : "daemon 已检查已存储的会话证据；普通工具输出不会被判定为失败。") + '</span></div>') + '</div>' : "";
    const inspectorBody = view.sessionInspectorTab === "ecm" ? ecm : view.sessionInspectorTab === "friction" ? friction : inspector;
    const overview = '<div class="session-detail-overview"><div class="session-overview-head"><span class="session-section-label">' + (view.locale === "en" ? "Overview" : "总览") + '</span><span class="session-overview-range">' + esc(overviewRange) + '</span><span class="session-overview-count" data-loaded="' + events.length + '" data-total="' + (totalEventCount == null ? events.length : totalEventCount) + '">' + esc(overviewCount) + '</span></div><div class="session-overview-ledger"><div class="session-overview-labels">' + laneLabel.map((label) => '<span>' + label + '</span>').join("") + '</div><div class="session-overview-plot">' + (overviewBars || '<span class="session-overview-missing">' + (view.locale === "en" ? "Event time not recorded" : "事件时间未记录") + '</span>') + '</div></div></div>';
    const toolbar = '<div class="session-detail-toolbar"><div class="session-ledger-actions"><button class="fl-tbtn" type="button" data-action="session-mode" data-mode="duration" data-on="' + view.sessionShowDuration + '">' + icon("clock") + (view.locale === "en" ? "Duration" : "耗时") + '</button><button class="fl-tbtn" type="button" data-action="session-mode" data-mode="turns" data-on="' + view.sessionFoldTurns + '">' + icon(view.sessionFoldTurns ? "rows-3" : "rows-2") + (view.locale === "en" ? "Turns" : "回合") + '</button><button class="fl-tbtn" type="button" data-action="session-mode" data-mode="calls" data-on="' + view.sessionFoldCalls + '">' + icon(view.sessionFoldCalls ? "list" : "list-collapse") + (view.locale === "en" ? "Calls" : "调用") + '</button></div><label class="session-ledger-search"><span class="sr-only">' + (view.locale === "en" ? "Search events" : "搜索事件") + '</span><input type="search" data-session-search placeholder="' + (view.locale === "en" ? "Search" : "搜索") + '" value="' + esc(view.sessionQuery) + '"></label></div>';
    const pageSentinel = view.sessionPageState && view.sessionPageState.hasMore ? '<div class="session-page-sentinel" data-session-page-sentinel>' + (view.locale === "en" ? "Scroll to load more local events" : "继续滚动读取本地事件") + '</div>' : "";
    const ledger = '<div class="session-event-scroll">' + (ledgerGroups || '<div class="empty-copy session-ledger-empty"><strong>' + (view.locale === "en" ? "No matching events." : "没有匹配的事件。") + '</strong><span>' + (view.locale === "en" ? "Try a different search or clear the call filter." : "请更换搜索词，或取消调用筛选。") + '</span></div>') + pageSentinel + '</div>';
    const chatAllLabel = totalEventCount != null && totalEventCount > events.length ? (view.locale === "en" ? "All " + totalEventCount + " · loaded " + events.length : "全部 " + totalEventCount + " · 已加载 " + events.length) : (view.locale === "en" ? "All " + events.length : "全部 " + events.length);
    const chatFilters = [["all", chatAllLabel], ["context", view.locale === "en" ? "Context" : "上下文注入"], ["tool", view.locale === "en" ? "Tool calls" : "工具调用"], ["message", view.locale === "en" ? "Messages" : "消息"], ["asset", view.locale === "en" ? "Assets" : "资产"], ["friction", view.locale === "en" ? "Friction only" : "只看摩擦"]];
    const chatSource = view.sessionChatFilter === "friction" ? frictionRows.map((row, frictionIndex) => ({ event: row.event, index: row.index, frictionIndex })) : events.map((event, index) => ({ event, index }));
    const chatEvents = showingChat ? chatSource.filter(({ event, index }) => {
      const kind = eventKind(event);
      const matchesFilter = view.sessionChatFilter === "all" || (view.sessionChatFilter === "tool" && (kind === "tool" || kind === "subtool")) || (view.sessionChatFilter === "friction" && frictionRows.some((row) => row.index === index)) || kind === view.sessionChatFilter;
      const searchable = index >= 0 ? derived.searchable[index] : [eventTitle(event), eventPreview(event), event.event_type, JSON.stringify(event.payload || {})].join(" ").toLocaleLowerCase();
      return matchesFilter && (!query || searchable.includes(query));
    }) : [];
    const chatToolbar = '<div class="session-chat-toolbar"><div class="session-chat-filters">' + chatFilters.map(([value, label]) => '<button type="button" class="us-toggle" data-action="session-chat-filter" data-chat-filter="' + value + '" data-pressed="' + (view.sessionChatFilter === value) + '">' + esc(label) + '</button>').join("") + '</div><label class="session-ledger-search"><span class="sr-only">' + (view.locale === "en" ? "Search events" : "搜索事件") + '</span><input type="search" data-session-search placeholder="' + (view.locale === "en" ? "Search" : "搜索") + '" value="' + esc(view.sessionQuery) + '"></label></div>';
    const chatRows = showingChat ? sessionLazyMarkup(chatEvents, (entry) => sessionChatRowHTML(entry, selectedIndex), 76) : "";
    const chat = '<div class="session-chat-pane">' + chatToolbar + '<div class="session-chat-scroll">' + (chatRows || '<div class="empty-copy"><strong>' + (view.locale === "en" ? "No matching events." : "没有匹配的事件。") + '</strong><span>' + (view.locale === "en" ? "Try another source filter or clear the search." : "请切换来源筛选，或清空搜索。") + '</span></div>') + pageSentinel + '</div></div>';
    const inspectorTab = view.locale === "en" ? "Inspector" : "记录详情";
    const ecmTab = view.locale === "en" ? "ECM" : "生效配置";
    const frictionTab = view.locale === "en" ? "Friction" : "摩擦轴";
    const mainContent = view.sessionTab === "chat" ? chat : overview + toolbar + ledger;
    const body = '<div class="session-detail-canvas"><main class="session-detail-main-pane">' + mainContent + '</main><aside class="session-inspector-pane"><div class="session-inspector-head"><div class="session-inspector-tabs"><button type="button" data-action="session-inspector-tab" data-tab="inspector" data-active="' + (view.sessionInspectorTab === "inspector") + '">' + inspectorTab + '</button><button type="button" data-action="session-inspector-tab" data-tab="ecm" data-active="' + (view.sessionInspectorTab === "ecm") + '">' + ecmTab + '</button><button type="button" data-action="session-inspector-tab" data-tab="friction" data-active="' + (view.sessionInspectorTab === "friction") + '">' + frictionTab + '</button></div><span>' + (selected ? esc("#" + (selectedIndex + 1)) : (view.locale === "en" ? "No selection" : "未选择")) + '</span></div><div class="session-inspector-scroll">' + inspectorBody + '</div></aside></div>';
    document.getElementById("flatline-screen").innerHTML = sessionHeader + taskLine + tabs + screenContent(body, "session-detail-page", "session-detail-scroll");
    const turnsButton = document.querySelector('[data-action="session-mode"][data-mode="turns"]');
    const callsButton = document.querySelector('[data-action="session-mode"][data-mode="calls"]');
    if (turnsButton) turnsButton.innerHTML = icon(view.sessionFoldTurns ? "rows-3" : "rows-2") + (view.locale === "en" ? "Turns" : "回合");
    if (callsButton) callsButton.innerHTML = icon(view.sessionFoldCalls ? "list" : "list-collapse") + (view.locale === "en" ? "Calls" : "调用");
    localizeDOM();
    armSessionLazyRows();
  }
  async function drawCleanup() {
    const data = await get("/api/v1/cleanup");
    const candidates = data.candidates || [];
    const knownBytes = candidates.map((candidate) => factsOf(candidate.asset).source_bytes).filter((value) => num(value) != null);
    const totalBytes = knownBytes.reduce((sum, value) => sum + value, 0);
    const readyCount = candidates.filter((candidate) => Boolean(candidate.rollback && candidate.rollback.reversible && candidate.rollback.source_path)).length;
    const rows = candidates.map((candidate) => {
      const item = candidate.asset;
      const rollback = candidate.rollback || {};
      const ready = Boolean(rollback.reversible && rollback.source_path);
      const bytes = num(factsOf(item).source_bytes) == null ? "未记录" : factsOf(item).source_bytes + " B";
      return '<label class="cleanup-table-row"><input type="checkbox" data-cleanup-id="' + esc(item.id) + '" data-cleanup-ready="' + ready + '"' + (ready ? "" : " disabled") + '><span class="cleanup-asset"><strong>' + esc(item.name) + '</strong><small>' + esc(kind(item.kind) + " · " + (item.source_path || "源路径未记录")) + '</small></span><span>' + esc(shortDate(factsOf(item).last_participation_at)) + '</span><span>' + esc(count(factsOf(item).participation_count)) + '</span><span class="cleanup-size">' + esc(bytes) + '</span><span>' + (ready ? "可生成回滚记录" : "源路径未记录") + "</span></label>";
    }).join("");
    const cleanupHeader = '<header class="detail-header cleanup-shell-header"><a class="back-link" href="#/" aria-label="返回资产">' + icon("arrowLeft") + '</a><div class="detail-identity"><span class="cleanup-shell-title"><h1>整理几乎未使用的资产</h1></span><span class="detail-subline">存在 ≥ 30 天，累计参与 ≤ 2 次 · ' + esc(quantity(candidates.length, "个候选", "candidate", "candidates")) + '</span></div><span class="detail-header-actions"><button class="us-btn" data-variant="primary" data-action="batch-cleanup" disabled>确认整理所选资产</button></span></header>';
    const metrics = '<section class="elevated-card cleanup-summary-card"><div class="cleanup-metric-row"><span class="fl-metric"><b>未记录</b><small>' + icon("hash") + '每次会话可省</small></span><span class="fl-metric"><b data-cleanup-selected>0</b><small>' + icon("archive") + '已选择归档</small></span><span class="fl-metric"><b>' + (knownBytes.length ? totalBytes + " B" : "未记录") + '</b><small>' + icon("wallet") + '已核实源文件大小</small></span><span class="cleanup-metric-spacer"></span><button type="button" class="cleanup-select-all" data-action="select-all-cleanup" aria-pressed="false"' + (readyCount ? "" : " disabled") + '>' + icon("check") + '按建议全选</button></div></section>';
    const table = '<section class="elevated-card card-pad cleanup-candidates-card"><header class="fl-head"><h3>几乎未使用的资产</h3><span class="fl-aside">存在 ≥ 30 天，累计参与 ≤ 2 次</span></header><div class="cleanup-table">' + (rows ? '<div class="cleanup-table-head"><span></span><span>资产</span><span>最后参与</span><span>累计参与</span><span>描述占用</span><span>建议</span></div>' + rows : '<div class="empty-copy cleanup-empty"><strong>当前没有符合条件的资产。</strong>没有把缺失记录当成低参与，也没有生成空的清理收益。</div>') + '</div><div class="cleanup-actions"><p data-cleanup-summary>尚未选择任何资产。</p><button class="us-btn" data-variant="primary" data-action="batch-cleanup" disabled>确认整理所选资产</button></div></section>';
    const plan = '<section class="elevated-card card-pad cleanup-plan-card"><header class="fl-head"><h3>将执行的操作</h3><span class="fl-aside">逻辑归档 · 可回滚</span></header><div class="cleanup-terminal"><div>$ flatline cleanup --logical-archive</div><div class="terminal-muted">仅在用户确认后写入处置记录；source_files_changed: false</div><div class="terminal-muted">已核实大小：' + esc(knownBytes.length ? knownBytes.length + "/" + candidates.length + " 个候选" : "未记录") + "</div></div><div class=\"cleanup-actions cleanup-plan-actions\"><p>Flatline 不会删除、改写或重命名任何源文件。</p><button class=\"us-btn\" data-variant=\"primary\" data-action=\"batch-cleanup\" disabled>确认整理所选资产</button></div></section>";
    document.getElementById("flatline-screen").innerHTML = cleanupHeader + screenContent(metrics + table + plan, "cleanup-page");
    localizeDOM();
    updateCleanupSummary();
  }
  function currentAssetID() {
    return location.hash.startsWith("#/assets/") ? decodeURIComponent(location.hash.slice("#/assets/".length)) : "";
  }
  function clearData() {
    cache.assets = null;
    cache.assetsMode = null;
    cache.stats = null;
    cache.notifications = null;
    cache.sessions = null;
    cache.timeline = null;
  }
  function renderError(error) {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    console.error("[Flatline] route error", error && error.stack ? error.stack : error);
    screen.innerHTML = screenContent('<section class="elevated-card card-pad"><div class="empty-copy"><strong>无法读取本地事实层。</strong><span>' + esc(error.message || error) + '</span><p>请确认 Flatline daemon 正在运行，并检查 loopback 地址。</p></div></section>', "narrow");
    localizeDOM();
  }
  async function loadSessionFirstPage(id) {
    const data = await get("/api/v1/sessions/" + encodeURIComponent(id) + "?events=page&offset=0&limit=" + SESSION_EVENT_PAGE_SIZE);
    view.sessionPageState = {
      offset: Array.isArray(data.events) ? data.events.length : 0,
      total: num(data.event_total) == null ? (data.session && num(data.session.event_count)) : data.event_total,
      hasMore: Boolean(data.events_has_more),
      loading: false
    };
    return data;
  }
  async function route() {
    const hash = location.hash || "#/";
    window.scrollTo(0, 0);
    try {
      const fullAssets = hash === "#/" || hash === "#/assets" || hash.startsWith("#/?") || hash === "#/cleanup";
      await loadOverview(false, fullAssets);
      renderShell();
      if (hash.startsWith("#/assets/")) {
        const id = currentAssetID();
        if (view.assetAssetID !== id) { view.assetAssetID = id; view.assetTab = "diagnosis"; }
        const detail = await get("/api/v1/assets/" + encodeURIComponent(id));
        const cached = cache.assets && cache.assets.assets && cache.assets.assets.find((asset) => asset.id === id);
        if (cached && detail.asset) Object.assign(cached, detail.asset);
        await drawDetail(detail);
        return;
      }
      if (hash === "#/sessions") { drawSessions(); return; }
      if (hash.startsWith("#/sessions/")) {
        const id = decodeURIComponent(hash.slice("#/sessions/".length));
        if (view.sessionID !== id) {
          view.sessionID = id;
          view.selectedEvent = 0;
          view.selectedFriction = null;
          view.sessionTab = "trajectory";
          view.sessionQuery = "";
          view.sessionChatFilter = "all";
          view.sessionFoldTurns = false;
          view.sessionFoldCalls = false;
          view.sessionCollapsedTurns = {};
          view.sessionInspectorTab = "inspector";
          view.sessionData = null;
          view.sessionDerived = null;
          view.sessionPageState = null;
        }
        const sessionData = view.sessionData && view.sessionPageState ? view.sessionData : await loadSessionFirstPage(id);
        drawSessionDetail(sessionData);
        hydrateSelectedEventPayload();
        return;
      }
      if (hash === "#/timeline") { drawTimeline(await get("/api/v1/timeline?limit=5000")); return; }
      if (hash === "#/stats") { drawStats(); return; }
      if (hash === "#/cleanup") { await drawCleanup(); return; }
      if (hash.startsWith("#/?")) {
        const params = new URLSearchParams(hash.slice(hash.indexOf("?") + 1));
        if (params.get("scope")) view.scope = params.get("scope");
      } else {
        view.scope = "all";
      }
      drawWall();
    } catch (error) {
      renderError(error);
    }
  }
  async function handleDisposition(button) {
    const id = button.dataset.assetId;
    const item = (cache.assets && cache.assets.assets || []).find((asset) => asset.id === id);
    const current = item && item.current_state;
    if (!item || !current || !current.instance_id) { notify("当前资产没有可用的状态实例，无法提交处置。", "error"); return; }
    const action = button.dataset.disposition;
    if (action === "modify") { await openModifyViewer(button); return; }
    const actionLabel = { modify: "需要监测", ignore: "隐藏当前状态", archive: "归档" }[action] || action;
    if (!window.confirm("确认执行“" + actionLabel + "”？Flatline 不会修改或删除源文件。")) return;
    try {
      await recordDisposition(item, action, "用户在资产详情页明确确认");
      notify("已记录“" + actionLabel + "”；源文件未改变。", "success");
      clearData();
      await route();
    } catch (error) {
      notify("处置未完成：" + error.message, "error");
    }
  }
  async function handleRestore(button) {
    if (!window.confirm("确认将该资产标记为“需要监测”？Flatline 不会修改或删除源文件。")) return;
    try {
      await post("/api/v1/assets/" + encodeURIComponent(button.dataset.assetId) + "/restore", { confirmed: true });
      notify("已标记为需要监测；等待后续可观测记录。", "success");
      clearData();
      await route();
    } catch (error) {
      notify("操作未完成：" + error.message, "error");
    }
  }
  function updateCleanupSummary() {
    const boxes = [...document.querySelectorAll("[data-cleanup-id]")];
    const selected = boxes.filter((box) => box.checked && box.dataset.cleanupReady === "true");
    const ready = boxes.filter((box) => box.dataset.cleanupReady === "true");
    const summary = document.querySelector("[data-cleanup-summary]");
    const submits = [...document.querySelectorAll("[data-action=\"batch-cleanup\"]")];
    const selectedMetric = document.querySelector("[data-cleanup-selected]");
    const selectAll = document.querySelector("[data-action=\"select-all-cleanup\"]");
    const selectedText = selected.length ? (view.locale === "en" ? "Selected " + selected.length + " assets · every disposition keeps a rollback record" : "已选择 " + selected.length + " 个资产 · 每个处置均保留回滚记录") : (view.locale === "en" ? "No assets selected." : "尚未选择任何资产。");
    if (summary) summary.textContent = selectedText;
    submits.forEach((submit) => { submit.disabled = selected.length === 0; });
    if (selectedMetric) selectedMetric.textContent = String(selected.length);
    if (selectAll) {
      selectAll.disabled = ready.length === 0;
      selectAll.setAttribute("aria-pressed", String(ready.length > 0 && selected.length === ready.length));
    }
  }
  async function handleBatchCleanup() {
    const ids = [...document.querySelectorAll("[data-cleanup-id]")].filter((box) => box.checked && box.dataset.cleanupReady === "true").map((box) => box.dataset.cleanupId);
    if (!ids.length) return;
    if (!window.confirm("确认整理 " + ids.length + " 个资产？源文件不会被修改或删除。")) return;
    try {
      const result = await post("/api/v1/cleanup", { asset_ids: ids, confirmed: true, reason: "用户在整理页面明确确认" });
      notify("已生成 " + ((result.archived || []).length || ids.length) + " 条可回滚处置记录；源文件未改变。", "success");
      clearData();
      await route();
    } catch (error) {
      notify("整理未完成：" + error.message, "error");
    }
  }
  function exportStats() {
    const payload = JSON.stringify(cache.stats || {}, null, 2);
    const blob = new Blob([payload], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "flatline-stats.json";
    link.click();
    URL.revokeObjectURL(url);
    notify("已导出当前统计快照。", "success");
  }
  function setLocale(locale) {
    view.locale = locale === "en" ? "en" : "zh";
    try { localStorage.setItem("flatline-locale", view.locale); } catch (_) {}
    applyPreferences();
    route();
  }
  function setTheme(theme) {
    view.theme = theme === "dark" ? "dark" : "light";
    try { localStorage.setItem("flatline-theme", view.theme); } catch (_) {}
    applyPreferences();
    route();
  }
  document.addEventListener("click", (event) => {
    const target = event.target instanceof Element ? event.target.closest("[data-action]") : null;
    if (!target) return;
    const action = target.dataset.action;
    if (action === "locale") {
      event.preventDefault();
      setLocale(view.locale === "en" ? "zh" : "en");
      return;
    }
    if (action === "theme") {
      event.preventDefault();
      setTheme(view.theme === "dark" ? "light" : "dark");
      return;
    }
    if (action === "notification-close") {
      event.preventDefault();
      view.notificationHiddenKey = target.dataset.notificationKey || "";
      const host = document.getElementById("flatline-toast");
      if (host) { host.hidden = true; host.innerHTML = ""; }
      return;
    }
    if (action === "modify-close") {
      event.preventDefault();
      closeModifyViewer();
      return;
    }
    if (action === "copy-source-path") {
      event.preventDefault();
      const path = target.dataset.sourcePath || "";
      if (!path) return;
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(path).then(() => notify("已复制源路径。", "success")).catch(() => notify("复制失败：请手动选择路径。", "error"));
      } else {
        notify("当前浏览器不允许自动复制，请手动选择路径。", "error");
      }
      return;
    }
    if (action === "confirm-modify") {
      event.preventDefault();
      confirmModify(target);
      return;
    }
    if (action === "scope") {
      event.preventDefault();
      view.scope = target.dataset.scope || "all";
      if (location.hash !== "#/") location.hash = "#/";
      else { renderShell(); drawWall(); }
      return;
    }
    if (action === "zone") {
      event.preventDefault();
      view.zoneOpen[target.dataset.zone] = target.getAttribute("aria-expanded") !== "true";
      drawWall();
      return;
    }
    if (action === "asset-tab") {
      event.preventDefault();
      if (target.dataset.closeModify === "true") closeModifyViewer();
      view.assetTab = target.dataset.tab || "diagnosis";
      route();
      return;
    }
    if (action === "timeline-filter") {
      event.preventDefault();
      view.timelineFilter = target.dataset.filter || "all";
      if (cache.timeline) drawTimeline(cache.timeline);
      else get("/api/v1/timeline?limit=5000").then(drawTimeline).catch(renderError);
      return;
    }
    if (action === "session-filter") {
      event.preventDefault();
      view.sessionSourceFilter = target.dataset.sourceFilter || "all";
      drawSessions();
      return;
    }
    if (action === "session-recorded") {
      view.sessionOnlyRecorded = target.checked;
      drawSessions();
      return;
    }
    if (action === "session-sort") {
      // Native select controls commit through the change event. Do not cancel
      // the click here or the browser cannot open the option list reliably.
      return;
    }
    if (action === "reload-sessions") {
      event.preventDefault();
      clearData();
      route();
      return;
    }
    if (action === "session-tab") {
      event.preventDefault();
      view.sessionTab = target.dataset.tab || "trajectory";
      if (view.sessionData) {
        drawSessionDetail(view.sessionData);
        hydrateSelectedEventPayload();
      } else {
        loadSessionFirstPage(view.sessionID).then((data) => { drawSessionDetail(data); hydrateSelectedEventPayload(); }).catch(renderError);
      }
      return;
    }
    if (action === "session-chat-filter") {
      event.preventDefault();
      view.sessionChatFilter = target.dataset.chatFilter || "all";
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (action === "session-mode") {
      event.preventDefault();
      const mode = target.dataset.mode;
      if (mode === "duration") view.sessionShowDuration = !view.sessionShowDuration;
      if (mode === "turns") view.sessionFoldTurns = !view.sessionFoldTurns;
      if (mode === "calls") view.sessionFoldCalls = !view.sessionFoldCalls;
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (action === "session-turn") {
      event.preventDefault();
      const turn = target.dataset.turn;
      view.sessionCollapsedTurns[turn] = target.getAttribute("aria-expanded") === "true";
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (action === "session-inspector-tab") {
      event.preventDefault();
      view.sessionInspectorTab = target.dataset.tab || "inspector";
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (action === "asset-open-editor") {
      event.preventDefault();
      const sourcePath = target.dataset.sourcePath || "";
      if (!sourcePath) {
        view.assetTab = "source";
        route();
        return;
      }
      const editorURI = "vscode://file/" + encodeURI(sourcePath);
      let requested = false;
      try {
        requested = Boolean(window.open(editorURI, "_blank", "noopener"));
      } catch (_) {
        requested = false;
      }
      if (requested) notify(view.locale === "en" ? "Requested the external editor to open the source file." : "已请求外部编辑器打开源文件。", "info");
      else {
        view.assetTab = "source";
        route();
      }
      return;
    }
    if (action === "session-open-editor") {
      event.preventDefault();
      notify(view.locale === "en" ? "This local viewer does not launch an external editor." : "当前本地查看器不启动外部编辑器。", "info");
      return;
    }
    if (action === "select-event") {
      event.preventDefault();
      view.selectedFriction = null;
      view.selectedEvent = Number(target.dataset.eventIndex) || 0;
      if (view.sessionData) {
        drawSessionDetail(view.sessionData);
        hydrateSelectedEventPayload();
      } else {
        loadSessionFirstPage(view.sessionID).then((data) => { drawSessionDetail(data); hydrateSelectedEventPayload(); }).catch(renderError);
      }
      return;
    }
    if (action === "select-friction") {
      event.preventDefault();
      const index = Number(target.dataset.frictionIndex);
      const data = view.sessionData;
      const derived = data ? sessionDerivedFor(data, Array.isArray(data.events) ? data.events : []) : null;
      const row = derived && Number.isFinite(index) ? derived.frictionRows[index] : null;
      if (!row) return;
      view.selectedFriction = row.event;
      view.selectedEvent = row.index;
      view.sessionInspectorTab = "inspector";
      if (view.sessionData) {
        drawSessionDetail(view.sessionData);
        hydrateSelectedEventPayload();
      }
      return;
    }
    if (action === "disposition") { event.preventDefault(); handleDisposition(target); return; }
    if (action === "restore") { event.preventDefault(); handleRestore(target); return; }
    if (action === "select-all-cleanup") {
      event.preventDefault();
      const boxes = [...document.querySelectorAll("[data-cleanup-id][data-cleanup-ready=\"true\"]")];
      const next = target.getAttribute("aria-pressed") !== "true";
      boxes.forEach((box) => { box.checked = next; });
      updateCleanupSummary();
      return;
    }
    if (action === "batch-cleanup") { event.preventDefault(); handleBatchCleanup(); return; }
    if (action === "export-stats") { event.preventDefault(); exportStats(); return; }
  });
  document.addEventListener("input", (event) => {
    if (!(event.target instanceof HTMLInputElement)) return;
    if (event.target.id === "flatline-search") {
      view.search = event.target.value;
      if ((location.hash || "#/") === "#/" || location.hash.startsWith("#/?")) drawWall();
      if (location.hash === "#/sessions") drawSessions();
      return;
    }
    if (event.target.matches("[data-session-search]")) {
      view.sessionQuery = event.target.value;
      if (view.sessionData) drawSessionDetail(view.sessionData);
    }
  });
  document.addEventListener("change", (event) => {
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-cleanup-id]")) updateCleanupSummary();
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-modify-ack]")) {
      const submit = document.querySelector("[data-action=\"confirm-modify\"]");
      if (submit) submit.disabled = !event.target.checked;
    }
    if (event.target instanceof HTMLSelectElement && event.target.matches("[data-action=\"session-sort\"]")) {
      view.sessionSort = event.target.value || "recent";
      drawSessions();
    }
  });
  document.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k") {
      event.preventDefault();
      const input = document.getElementById("flatline-search");
      if (input) { input.focus(); input.select(); }
    }
    if (event.key === "Escape") {
      if (document.querySelector("[data-modify-modal]")) { closeModifyViewer(); return; }
      const input = document.getElementById("flatline-search");
      if (input && document.activeElement === input) input.blur();
    }
  });
  window.addEventListener("hashchange", route);

  function drawWall() {
    if (wallLazyObserver) {
      wallLazyObserver.disconnect();
      wallLazyObserver = null;
    }
    wallLazySections = [];
    const all = cache.assets && cache.assets.assets || [];
    const assets = filteredAssets();
    const groups = { silent: [], broken: [], bypassed: [], degraded: [], awaiting_resurrection: [], healthy: [], dormant: [], no_opportunity: [], unobservable: [], not_evaluated: [], archived: [] };
    assets.forEach((item) => { const key = stateOf(item); (groups[key] || groups.not_evaluated).push(item); });
    const attention = all.filter((item) => ["silent", "broken", "bypassed"].includes(stateOf(item))).length;
    const otherCount = all.filter((item) => ["dormant", "no_opportunity", "unobservable", "not_evaluated", "archived"].includes(stateOf(item))).length;
    const summary = '<strong>' + all.length + (view.locale === "en" ? " assets</strong> · <strong>" : " 个资产</strong> · <strong>") + attention + (view.locale === "en" ? " need attention</strong> · " : " 个需要注意</strong> · ") + otherCount + (view.locale === "en" ? " have no related task record or are archived" : " 个没有相关任务记录或已归档") + (view.search ? (view.locale === "en" ? " · Showing " + assets.length : " · 当前显示 " + assets.length + " 个") : "");
    const legend = '<span class="fl-legend"><span><i data-mark="asset"></i>' + uiText("资产变更", "Asset change") + '</span><span><i data-mark="env"></i>' + uiText("环境变化", "Environment change") + '</span><span><i data-mark="alive"></i>' + uiText("恢复使用", "Restored use") + '</span></span>';
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const zones = [
      section("需要注意", ["silent", "broken", "bypassed"], "过去 14 天没有状态变化。", "bad", true, groups),
      section("观察中", ["degraded", "awaiting_resurrection"], "当前没有处于观察中的资产。", "warn", true, groups),
      section("正常", ["healthy"], "当前没有可确认正常的资产。", "good", true, groups),
      section("几乎未使用", ["dormant"], "当前没有达到几乎未使用判定的资产。", "muted", false, groups),
      section("没有相关任务记录", ["no_opportunity"], "没有记录到与该资产相关的任务。", "muted", true, groups),
      section("不可观测", ["unobservable"], "当前数据没有记录该资产是否被加载或使用。", "muted", false, groups),
      section("其他", ["not_evaluated", "archived"], "当前没有未评估或已归档资产。", "muted", false, groups)
    ].join("");
    screen.innerHTML = header("资产", summary, legend) + screenContent(zones, "wall-page");
    localizeDOM();
    armWallLazyRows();
  }
  route();
})();
