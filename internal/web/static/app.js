(() => {
  "use strict";

  const root = document.getElementById("flatline-root");
  const toast = document.getElementById("flatline-toast");
  const cache = { assets: null, assetsMode: null, stats: null, notifications: null, timeline: null, friction: null, overview: null, overviewRange: "", insights: null };
  const httpCache = new Map();
  const shell = { dataVersion: null, status: null, projects: null, projectsReady: false, sourceCounts: null, pollTimer: null, built: false, chrome: "", screenKey: "", paintedRoute: "" };
  const WALL_ROW_CHUNK = 96;
  const SESSION_ROW_CHUNK = 120;
  const SESSION_LIST_PAGE_SIZE = 50;
  const SESSION_EVENT_PAGE_SIZE = 1000;
  const SESSION_SORTS = ["recent", "oldest", "duration", "events", "friction", "tool_calls", "tokens", "lines_changed", "active"];
  const SESSION_GROUPS = ["none", "project", "day", "week", "role"];
  const SESSION_THREADS = ["main", "subagent", "all"];
  const SESSION_EMPTY = ["0", "1", "all"];
  const OVERVIEW_RANGES = ["7", "30", "90", "all"];
  const FRICTION_ROW_CHUNK = 80;
  const TIMELINE_PAGE_SIZE = 1000;
  const SUBAGENT_PAGE_SIZE = 100;
  let wallLazySections = [];
  let wallLazyObserver = null;
  let sessionLazyBatches = [];
  let sessionLazyObserver = null;
  let sessionLazyRoot = null;
  let sessionLazyScrollHandler = null;
  const savedLocale = (() => { try { return localStorage.getItem("flatline-locale"); } catch (_) { return null; } })();
  const savedTheme = (() => { try { return localStorage.getItem("flatline-theme"); } catch (_) { return null; } })();
  const savedOverviewMore = (() => { try { return localStorage.getItem("flatline-overview-more"); } catch (_) { return null; } })();
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
    frictionProjectFilter: "all",
    frictionHarnessFilter: "all",
    frictionKindFilter: "all",
    frictionCategoryFilter: "all",
    frictionToolFilter: "all",
    frictionSignatureFilter: "all",
    frictionRange: "all",
    frictionFrom: "",
    frictionTo: "",
    frictionWindow: 7,
    frictionGroupBy: "project",
    frictionQuery: "",
    frictionSort: "count",
    frictionOverview: null,
    frictionDetail: null,
    frictionBriefSignature: null,
    frictionWatchBusy: false,
    frictionSelected: 0,
    frictionRequest: 0,
    frictionLoading: false,
    notificationHiddenKey: "",
    sessionList: { key: "", items: [], pagination: null, facets: null, loading: false, ready: true, error: "" },
    sessionTagEditor: "",
    sessionEventFocus: "",
    sessionRevealEvent: -1,
    sessionActivityDrag: null,
    sessionChildren: {},
    sessionCommandProgram: "all",
    sessionCommandFailedOnly: false,
    overviewData: null,
    overviewReady: true,
    overviewError: "",
    overviewTime: null,
    overviewTimeError: "",
    overviewMoreOpen: savedOverviewMore === "open",
    usageDefinition: "", usageDefinitionEN: "",
    projectPage: { key: "", data: null, error: "", loading: false, metric: "sessions" },
    dataPage: {
      health: null, healthError: "", tools: null, toolsError: "", loading: false,
      sources: null, sourcesError: "", sourcesNote: "", sourcesNoteEN: "",
      sourceForm: { kind: "", root: "", label: "" }, sourceBusy: false
    },
    toolFamilyOpen: {},
    timelineOffset: 0,
    timelineItems: null,
    timelineTotal: null,
    timelineClusters: null,
    timelinePageSize: 0,
    timelineAppended: 0,
    timelineHasMore: null,
    timelineLoading: false,
    timelineError: "",
    timelineTotalLoaded: 0,
    searchOpen: false,
    searchResults: null,
    searchError: "",
    searchLoading: false,
    searchIndex: 0,
    searchRequest: 0,
    popover: null,
    wallSearch: "",
    modifyAssetID: "",
    locale: savedLocale === "en" ? "en" : "zh",
    theme: savedTheme === "dark" ? "dark" : "light"
  };

  function applyPreferences() {
    document.documentElement.classList.toggle("dark", view.theme === "dark");
    document.documentElement.dataset.theme = view.theme;
    document.documentElement.lang = view.locale === "en" ? "en" : "zh-CN";
    document.title = view.locale === "en" ? "Flatline · Overview" : "Flatline · 总览";
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
  const sourceLabels = { claude_code: "Claude Code", codex: "Codex", opencode: "opencode", dsh: "DeepSeek Harness", hermes: "Hermes" };
  // harness = which tool wrote the transcript. originator = which program
  // inside that tool started the session. Claude Code records two originator
  // values that differ only by punctuation, so each gets a name that says
  // which one it is instead of printing the raw value twice.
  const ORIGINATOR_LABELS = {
    "codex-tui": ["Codex 终端", "Codex terminal"],
    codex_exec: ["Codex exec", "Codex exec"],
    "Claude Code": ["Claude Code 插件", "Claude Code plugin"],
    claude_code: ["Claude Code", "Claude Code"],
    opencode: ["opencode", "opencode"],
    dsh: ["dsh", "dsh"]
  };
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
  const frictionKindLabels = {
    tool_error: "工具调用出错",
    nonzero_exit: "非零退出码",
    asset_violation: "资产绕行"
  };
  const enFrictionKindLabels = {
    tool_error: "Tool call error",
    nonzero_exit: "Non-zero exit",
    asset_violation: "Asset bypass"
  };
  const frictionKindIcons = {
    tool_error: "triangle-alert",
    nonzero_exit: "circle-slash",
    asset_violation: "unlink"
  };
  const frictionKindTones = {
    tool_error: "bad",
    nonzero_exit: "warn",
    asset_violation: "bad"
  };
  const FRICTION_UNCLASSIFIED = "__unrecorded__";
  const frictionCategoryLabels = {
    user_interrupt: "用户中断",
    permission_denied: "权限被拒绝",
    command_not_found: "命令不存在",
    file_not_found: "文件不存在",
    tool_input_invalid: "工具入参无效",
    timeout: "超时",
    network_error: "网络错误",
    test_failure: "测试失败",
    build_error: "构建报错",
    nonzero_exit: "非零退出码",
    tool_error: "工具调用出错",
    __unrecorded__: "未分类"
  };
  const enFrictionCategoryLabels = {
    user_interrupt: "User interrupt",
    permission_denied: "Permission denied",
    command_not_found: "Command not found",
    file_not_found: "File not found",
    tool_input_invalid: "Invalid tool input",
    timeout: "Timeout",
    network_error: "Network error",
    test_failure: "Test failure",
    build_error: "Build error",
    nonzero_exit: "Non-zero exit",
    tool_error: "Tool call error",
    __unrecorded__: "Not classified"
  };
  // Icons come from the prototype set only (see prototypeIconNames); no new
  // icon vocabulary is introduced for this page.
  const frictionCategoryIcons = {
    user_interrupt: "power-off",
    permission_denied: "shield-off",
    command_not_found: "slash",
    file_not_found: "file-text",
    tool_input_invalid: "file-diff",
    timeout: "hourglass",
    network_error: "webhook",
    test_failure: "x",
    build_error: "file-code",
    nonzero_exit: "circle-slash",
    tool_error: "triangle-alert",
    __unrecorded__: "eye-off"
  };
  const frictionCategoryTones = {
    user_interrupt: "accent",
    permission_denied: "bad",
    command_not_found: "warn",
    file_not_found: "warn",
    tool_input_invalid: "warn",
    timeout: "warn",
    network_error: "bad",
    test_failure: "bad",
    build_error: "bad",
    nonzero_exit: "warn",
    tool_error: "bad",
    __unrecorded__: "muted"
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
    bookMarked: '<path d="M10 2v8l3-3 3 3V2"></path><path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H19a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H6.5a1 1 0 0 1 0-5H20"></path>',
    // Three deliberate additions to the prototype vocabulary (docs/qa/icon-additions.json):
    // pinning, tagging and adding a tag had no glyph of their own and were
    // borrowing chevron-up and hash, which read as "collapse" and "heading".
    pin: '<path d="M12 17v5"></path><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"></path>',
    tag: '<path d="M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z"></path><circle cx="7.5" cy="7.5" r=".5" fill="currentColor"></circle>',
    plus: '<path d="M5 12h14"></path><path d="M12 5v14"></path>'
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
  ICONS["list-filter"] = ICONS.listFilter;
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
  // A friction signature reads "category|tool|evidence line"; the line after
  // the last pipe is what a reader can actually see in the transcript.
  const frictionSignatureLine = (signature) => {
    const text = String(signature || "");
    const index = text.lastIndexOf("|");
    return index >= 0 && index + 1 < text.length ? text.slice(index + 1) : text;
  };
  const localized = (zh, en, value, fallback) => view.locale === "en" ? (en[value] || value || fallback) : (zh[value] || value || fallback);
  const date = (value) => value ? new Date(value).toLocaleString(view.locale === "en" ? "en-US" : "zh-CN", { hour12: false }) : (view.locale === "en" ? "Not recorded" : "未记录");
  const shortDate = (value) => value ? new Date(value).toLocaleString(view.locale === "en" ? "en-US" : "zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }) : (view.locale === "en" ? "Not recorded" : "未记录");
  const kind = (value) => localized(kindLabels, enKindLabels, value, view.locale === "en" ? "Asset" : "资产");
  const scope = (value) => value === "project" ? (view.locale === "en" ? "Project" : "项目级") : value === "user" ? (view.locale === "en" ? "User" : "用户级") : value || (view.locale === "en" ? "Scope not recorded" : "作用域未记录");
  const source = (value) => localized(sourceLabels, sourceLabels, value, view.locale === "en" ? "Not recorded" : "未记录");
  const originatorLabel = (value) => {
    const copy = ORIGINATOR_LABELS[value];
    if (copy) return uiText(copy[0], copy[1]);
    return value || uiText("发起方未记录", "Originator not recorded");
  };
  const frictionKindLabel = (value) => localized(frictionKindLabels, enFrictionKindLabels, value, view.locale === "en" ? "Friction" : "摩擦记录");
  const frictionProjectLabel = (value) => value === "__unrecorded__" ? (view.locale === "en" ? "Project not recorded" : "项目未记录") : (value || (view.locale === "en" ? "Project not recorded" : "项目未记录"));
  const frictionCategoryKey = (value) => value || FRICTION_UNCLASSIFIED;
  const frictionCategoryLabel = (value) => localized(frictionCategoryLabels, enFrictionCategoryLabels, frictionCategoryKey(value), view.locale === "en" ? "Not classified" : "未分类");
  const frictionToolLabel = (value) => value && value !== FRICTION_UNCLASSIFIED ? value : (view.locale === "en" ? "Tool not recorded" : "工具未记录");
  // This is the icon vocabulary actually present in the prototype, including
  // the values supplied by its dynamic asset/source/session data. A name not in
  // this set must not silently become a different Lucide glyph.
  // pin / tag / plus / list-filter / refresh-cw are the deliberate extensions of
  // that vocabulary; the reason for each is recorded in docs/qa/icon-additions.json.
  const prototypeIconNames = new Set([
    "activity", "archive", "arrow-left", "arrow-right", "bell-off", "book-open", "calendar", "camera", "chart-column", "check",
    "chevron-down", "chevron-right", "chevron-up", "circle-slash", "clock", "cpu", "eye-off", "file-code", "file-diff", "file-text", "folder",
    "git-commit-horizontal", "hash", "history", "hourglass", "layers", "list", "list-collapse", "package", "package-open",
    "power-off", "scale", "search", "shield-off", "slash", "trending-down", "triangle-alert", "unlink", "volume-x", "wallet", "webhook", "x",
    "rows-2", "rows-3", "pin", "tag", "plus", "list-filter", "refresh-cw"
  ]);
  const iconAliases = {
    activity: "activity", archive: "archive", arrowLeft: "arrow-left", arrowRight: "arrow-right", bellOff: "bell-off", calendar: "calendar",
    camera: "camera", chart: "chart-column", chartColumn: "chart-column", check: "check", chevronDown: "chevron-down", chevronRight: "chevron-right",
    chevronUp: "chevron-up", circleSlash: "circle-slash", clock: "clock", cpu: "cpu", eyeOff: "eye-off", fileDiff: "file-diff", folder: "folder",
    gitCommitHorizontal: "git-commit-horizontal", hash: "hash", history: "history", hourglass: "hourglass", layers: "layers", list: "list",
    listCollapse: "list-collapse", package: "package", packageOpen: "package-open", powerOff: "power-off", search: "search", shieldOff: "shield-off",
    slash: "slash", trendingDown: "trending-down", triangleAlert: "triangle-alert", unlink: "unlink", volumeX: "volume-x", wallet: "wallet", x: "x",
    rows2: "rows-2", rows3: "rows-3", pin: "pin", tag: "tag", plus: "plus",
    listFilter: "list-filter", refreshCw: "refresh-cw"
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
  // Brand marks are inlined so a re-render never re-fetches an <img> and
  // flashes. Colours are baked into the source SVG, as in the icon files.
  const BRAND_SVG = {
    claudecode: "<path clip-rule=\"evenodd\" d=\"M20.998 10.949H24v3.102h-3v3.028h-1.487V20H18v-2.921h-1.487V20H15v-2.921H9V20H7.488v-2.921H6V20H4.487v-2.921H3V14.05H0V10.95h3V5h17.998v5.949zM6 10.949h1.488V8.102H6v2.847zm10.51 0H18V8.102h-1.49v2.847z\" fill=\"#D97757\" fill-rule=\"evenodd\"></path>",
    codexLight: "<path clip-rule=\"evenodd\" d=\"M8.086.457a6.105 6.105 0 013.046-.415c1.333.153 2.521.72 3.564 1.7a.117.117 0 00.107.029c1.408-.346 2.762-.224 4.061.366l.063.03.154.076c1.357.703 2.33 1.77 2.918 3.198.278.679.418 1.388.421 2.126a5.655 5.655 0 01-.18 1.631.167.167 0 00.04.155 5.982 5.982 0 011.578 2.891c.385 1.901-.01 3.615-1.183 5.14l-.182.22a6.063 6.063 0 01-2.934 1.851.162.162 0 00-.108.102c-.255.736-.511 1.364-.987 1.992-1.199 1.582-2.962 2.462-4.948 2.451-1.583-.008-2.986-.587-4.21-1.736a.145.145 0 00-.14-.032c-.518.167-1.04.191-1.604.185a5.924 5.924 0 01-2.595-.622 6.058 6.058 0 01-2.146-1.781c-.203-.269-.404-.522-.551-.821a7.74 7.74 0 01-.495-1.283 6.11 6.11 0 01-.017-3.064.166.166 0 00.008-.074.115.115 0 00-.037-.064 5.958 5.958 0 01-1.38-2.202 5.196 5.196 0 01-.333-1.589 6.915 6.915 0 01.188-2.132c.45-1.484 1.309-2.648 2.577-3.493.282-.188.55-.334.802-.438.286-.12.573-.22.861-.304a.129.129 0 00.087-.087A6.016 6.016 0 015.635 2.31C6.315 1.464 7.132.846 8.086.457zm-.804 7.85a.848.848 0 00-1.473.842l1.694 2.965-1.688 2.848a.849.849 0 001.46.864l1.94-3.272a.849.849 0 00.007-.854l-1.94-3.393zm5.446 6.24a.849.849 0 000 1.695h4.848a.849.849 0 000-1.696h-4.848z\"></path>",
    codexDark: "<path clip-rule=\"evenodd\" d=\"M8.086.457a6.105 6.105 0 013.046-.415c1.333.153 2.521.72 3.564 1.7a.117.117 0 00.107.029c1.408-.346 2.762-.224 4.061.366l.063.03.154.076c1.357.703 2.33 1.77 2.918 3.198.278.679.418 1.388.421 2.126a5.655 5.655 0 01-.18 1.631.167.167 0 00.04.155 5.982 5.982 0 011.578 2.891c.385 1.901-.01 3.615-1.183 5.14l-.182.22a6.063 6.063 0 01-2.934 1.851.162.162 0 00-.108.102c-.255.736-.511 1.364-.987 1.992-1.199 1.582-2.962 2.462-4.948 2.451-1.583-.008-2.986-.587-4.21-1.736a.145.145 0 00-.14-.032c-.518.167-1.04.191-1.604.185a5.924 5.924 0 01-2.595-.622 6.058 6.058 0 01-2.146-1.781c-.203-.269-.404-.522-.551-.821a7.74 7.74 0 01-.495-1.283 6.11 6.11 0 01-.017-3.064.166.166 0 00.008-.074.115.115 0 00-.037-.064 5.958 5.958 0 01-1.38-2.202 5.196 5.196 0 01-.333-1.589 6.915 6.915 0 01.188-2.132c.45-1.484 1.309-2.648 2.577-3.493.282-.188.55-.334.802-.438.286-.12.573-.22.861-.304a.129.129 0 00.087-.087A6.016 6.016 0 015.635 2.31C6.315 1.464 7.132.846 8.086.457zm-.804 7.85a.848.848 0 00-1.473.842l1.694 2.965-1.688 2.848a.849.849 0 001.46.864l1.94-3.272a.849.849 0 00.007-.854l-1.94-3.393zm5.446 6.24a.849.849 0 000 1.695h4.848a.849.849 0 000-1.696h-4.848z\"></path>",
    deepseek: "<path d=\"M23.748 4.482c-.254-.124-.364.113-.512.234-.051.039-.094.09-.137.136-.372.397-.806.657-1.373.626-.829-.046-1.537.214-2.163.848-.133-.782-.575-1.248-1.247-1.548-.352-.156-.708-.311-.955-.65-.172-.241-.219-.51-.305-.774-.055-.16-.11-.323-.293-.35-.2-.031-.278.136-.356.276-.313.572-.434 1.202-.422 1.84.027 1.436.633 2.58 1.838 3.393.137.093.172.187.129.323-.082.28-.18.552-.266.833-.055.179-.137.217-.329.14a5.526 5.526 0 01-1.736-1.18c-.857-.828-1.631-1.742-2.597-2.458a11.365 11.365 0 00-.689-.471c-.985-.957.13-1.743.388-1.836.27-.098.093-.432-.779-.428-.872.004-1.67.295-2.687.684a3.055 3.055 0 01-.465.137 9.597 9.597 0 00-2.883-.102c-1.885.21-3.39 1.102-4.497 2.623C.082 8.606-.231 10.684.152 12.85c.403 2.284 1.569 4.175 3.36 5.653 1.858 1.533 3.997 2.284 6.438 2.14 1.482-.085 3.133-.284 4.994-1.86.47.234.962.327 1.78.397.63.059 1.236-.03 1.705-.128.735-.156.684-.837.419-.961-2.155-1.004-1.682-.595-2.113-.926 1.096-1.296 2.746-2.642 3.392-7.003.05-.347.007-.565 0-.845-.004-.17.035-.237.23-.256a4.173 4.173 0 001.545-.475c1.396-.763 1.96-2.015 2.093-3.517.02-.23-.004-.467-.247-.588zM11.581 18c-2.089-1.642-3.102-2.183-3.52-2.16-.392.024-.321.471-.235.763.09.288.207.486.371.739.114.167.192.416-.113.603-.673.416-1.842-.14-1.897-.167-1.361-.802-2.5-1.86-3.301-3.307-.774-1.393-1.224-2.887-1.298-4.482-.02-.386.093-.522.477-.592a4.696 4.696 0 011.529-.039c2.132.312 3.946 1.265 5.468 2.774.868.86 1.525 1.887 2.202 2.891.72 1.066 1.494 2.082 2.48 2.914.348.292.625.514.891.677-.802.09-2.14.11-3.054-.614zm1-6.44a.306.306 0 01.415-.287.302.302 0 01.2.288.306.306 0 01-.31.307.303.303 0 01-.304-.308zm3.11 1.596c-.2.081-.399.151-.59.16a1.245 1.245 0 01-.798-.254c-.274-.23-.47-.358-.552-.758a1.73 1.73 0 01.016-.588c.07-.327-.008-.537-.239-.727-.187-.156-.426-.199-.688-.199a.559.559 0 01-.254-.078c-.11-.054-.2-.19-.114-.358.028-.054.16-.186.192-.21.356-.202.767-.136 1.146.016.352.144.618.408 1.001.782.391.451.462.576.685.914.176.265.336.537.445.848.067.195-.019.354-.25.452z\" fill=\"#4D6BFE\"></path>"
  };
  // A source with no local artwork gets a lettered disc drawn on the same
  // 24-unit grid as the real marks, so a new adapter never falls back to a
  // borrowed logo or an external request.
  const LETTER_MARKS = {
    opencode: { letter: "O", fill: "#0f9d76" },
    dsh: { letter: "D", fill: "#4D6BFE" },
    hermes: { letter: "H", fill: "#8b5cf6" }
  };
  const letterMark = (entry) => '<circle cx="12" cy="12" r="11" fill="' + entry.fill + '"></circle>'
    + '<text x="12" y="12" fill="#ffffff" font-family="Hellix, ui-sans-serif, system-ui, sans-serif" font-size="12.5" font-weight="600" text-anchor="middle" dominant-baseline="central">' + entry.letter + "</text>";
  const brandSVG = (value) => {
    const letter = LETTER_MARKS[value];
    const glyph = letter ? letterMark(letter)
      : value === "codex" ? (view.theme === "dark" ? BRAND_SVG.codexDark : BRAND_SVG.codexLight)
      : value === "claude_code" ? BRAND_SVG.claudecode
      : BRAND_SVG.deepseek;
    return '<svg class="brand-glyph" viewBox="0 0 24 24" aria-hidden="true" focusable="false">' + glyph + "</svg>";
  };
  const sourceIcon = (value) => '<span class="source-brand" data-source="' + esc(value) + '">' + brandSVG(value) + "<span>" + esc(source(value)) + "</span></span>";
  function assetMark(item) {
    const glyph = item && item.kind === "skill" ? "book-open" : item && item.kind === "hook" ? "webhook" : item && item.kind === "rule" ? "scale" : "file-text";
    return icon(glyph);
  }
  const notificationMeta = {
    watch_verified: { state: "healthy", zhBadge: "修复验证通过", enBadge: "Fix verified", zhAction: "查看该签名", enAction: "Open the signature" },
    watch_no_change: { state: "silent", zhBadge: "修复未见改善", enBadge: "Fix did not help", zhAction: "查看该签名", enAction: "Open the signature" },
    watch_unobservable: { state: "not_evaluated", zhBadge: "修复验证无法判断", enBadge: "Fix verdict unavailable", zhAction: "查看该签名", enAction: "Open the signature" },
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
    const isWatch = Boolean(item.watch_id);
    const target = isWatch
      ? overviewRangeHref("#/friction", "group=signature&signature=" + encodeURIComponent(item.signature || ""))
      : hasSession ? "#/sessions/" + encodeURIComponent(item.session_id) : "#/assets/" + encodeURIComponent(item.asset_id || "");
    const badge = view.locale === "en" ? meta.enBadge : meta.zhBadge;
    const action = view.locale === "en" ? meta.enAction : meta.zhAction;
    const summary = view.locale === "en" && item.summary_en ? item.summary_en : (item.summary || item.rule || (view.locale === "en" ? "A state transition was recorded." : "状态迁移已记录。"));
    const subject = isWatch ? "" : item.asset_name ? item.asset_name + (view.locale === "en" ? ": " : "：") : "";
    host.innerHTML = '<div class="notification-card" data-kind="' + esc(item.kind || "state_transition") + '"><div class="notification-head"><span class="fl-state" data-state="' + esc(meta.state) + '">' + icon(stateIcons[meta.state] || "activity") + '<span>' + esc(badge) + '</span></span><button type="button" class="us-btn" data-variant="ghost" data-size="icon-sm" data-action="notification-close" data-notification-key="' + esc(notificationKey(item)) + '" aria-label="关闭">' + icon("x") + '</button></div><div class="notification-copy">' + esc(subject + (view.locale === "en" ? translateUI(summary) : summary)) + '</div><div class="notification-foot"><a class="us-btn" data-variant="outline" data-size="sm" href="' + esc(target) + '">' + esc(action) + '</a><span class="fl-level" data-level="unknown"><i></i>' + esc(view.locale === "en" ? (isWatch ? "Fix verification" : "State transition") : (isWatch ? "修复验证" : "状态迁移")) + '</span></div></div>';
    host.hidden = false;
  }

  // The prototype is authored in Chinese. Keep the render functions readable
  // while translating every static UI phrase, including labels and ARIA
  // attributes, at the DOM boundary. User asset names, paths and payloads are
  // deliberately excluded so real evidence is never rewritten.
  const EN_TEXT = [
    ["正在读取本地事实层…", "Reading local fact layer…"], ["Flatline 导航", "Flatline navigation"], ["Flatline 资产首页", "Flatline asset home"], ["搜索资产", "Search assets"], ["搜索资产、路径或证据", "Search assets, paths or evidence"],
    ["导航", "Navigation"], ["资产", "Assets"], ["会话", "Sessions"], ["摩擦", "Friction"], ["变化时间线", "Timeline"], ["统计", "Statistics"], ["项目", "Projects"], ["数据源", "Data sources"], ["全部项目", "All projects"], ["项目级", "Project"], ["用户级", "User"], ["本地模式", "Local mode"], ["数据不离开本机", "Data stays on this machine"], ["daemon 在线", "daemon online"], ["切换语言", "Switch language"], ["切换主题", "Switch theme"], ["深色模式", "Dark mode"], ["浅色模式", "Light mode"],
    ["需要注意", "Needs attention"], ["观察中", "Under observation"], ["正常", "Healthy"], ["几乎未使用", "Rarely used"], ["没有相关任务记录", "No related task record"], ["不可观测", "Unobservable"], ["其他", "Other"], ["当前没有需要注意的状态。", "There are no attention states."], ["当前没有处于观察中的资产。", "There are no assets under observation."], ["当前没有可确认正常的资产。", "There are no assets confirmed healthy."], ["当前没有达到几乎未使用判定的资产。", "No assets meet the rarely-used rule."], ["当前没有未评估或已归档资产。", "There are no unevaluated or archived assets."],
    ["资产变更", "Asset change"], ["环境变化", "Environment change"], ["修改后验证", "Post-change verification"], ["变化时间线", "Timeline"], ["按时间查看本地事实", "View local facts over time"], ["资产版本、环境变化与状态迁移按时间排列。相近时间只表示对齐关系，具体证据请下钻到资产或会话。", "Asset versions, environment changes and state transitions are ordered by time. Proximity indicates alignment only; drill into an asset or session for evidence."],
    ["每一行代表一个资产的当前状态；缺失记录显示为未记录。", "Each row is the current reading for one asset; missing records remain not recorded."], ["变化时间线", "Timeline"], ["整理几乎未使用的资产", "Organize rarely used assets"], ["整理", "Organize"], ["返回资产", "Back to assets"], ["这里仅生成可回滚的逻辑归档处置。Flatline 不会删除、改写或重命名任何源文件。", "This creates only reversible logical-archive dispositions. Flatline never deletes, rewrites or renames source files."],
    ["搜索资产、路径或证据", "Search assets, paths or evidence"], ["资产详情", "Asset details"], ["诊断", "Diagnosis"], ["原文", "Source"], ["版本", "Versions"], ["处置历史", "Disposition history"], ["资产事实", "Asset facts"], ["判定依据", "Decision evidence"], ["参与漏斗", "Participation funnel"], ["相关任务记录", "Related task records"], ["参与记录", "Participation records"], ["引用体检", "Reference checks"], ["相关会话", "Related sessions"], ["处置", "Disposition"], ["证据边界", "Evidence boundary"], ["查看原文", "View source"], ["查看版本", "View versions"], ["隐藏当前状态", "Hide current state"], ["归档", "Archive"], ["需要监测", "Needs monitoring"], ["轨迹", "Trajectory"], ["事件", "Events"], ["会话详情", "Session details"], ["原始证据", "Raw evidence"], ["原始事件", "Raw events"], ["会话轨迹", "Session trajectory"], ["事件详情", "Event details"], ["返回会话", "Back to sessions"],
    ["直接观测", "Directly observed"], ["观察到使用", "Observed use"], ["已加载", "Loaded"], ["已提供", "Offered"], ["推断", "Inferred"], ["明确参与", "Explicit participation"], ["调用记录", "Invocation record"], ["加载记录", "Load record"], ["明确绕行", "Explicit bypass"], ["未记录（unknown）", "Not recorded (unknown)"], ["未记录", "Not recorded"], ["未评估", "Not evaluated"], ["作用域未记录", "Scope not recorded"], ["源路径未记录", "Source path not recorded"], ["工作目录未记录", "Working directory not recorded"], ["参与形式未记录", "Participation form not recorded"], ["内容未记录", "Content not recorded"], ["内容已截断", "Content truncated"], ["只读预览", "Read-only preview"],
    ["参与比未记录", "Participation ratio not recorded"], ["未记录分子 / 分母", "Numerator / denominator not recorded"], ["基线 · 分子 / 分母", "Baseline · numerator / denominator"], ["当前 · 分子 / 分母", "Current · numerator / denominator"], ["当前窗口", "Current window"], ["任务形状未记录", "Task shape not recorded"], ["有参与记录", "Participation recorded"], ["没有参与记录", "No participation recorded"], ["缺失不转换为 0。", "Missing is not converted to zero."], ["当前没有参与记录。", "There are no participation records."], ["没有记录到与该资产相关的任务。", "No task related to this asset was recorded."], ["没有记录到与该资产相关的任务，无法判断参与。", "No task related to this asset was recorded, so participation cannot be judged."],
    ["只有记录到相关任务后，才判断资产是否参与。", "Participation is judged only after related tasks are recorded."], ["数据源必须提供加载或使用记录，才能判断资产是否参与。", "The data source must record loading or use before participation can be judged."], ["状态评估尚未运行，不对资产状态作判断。", "State evaluation has not run; no state judgment is made."], ["资产修改后，需要下一次可观察的参与记录来验证当前状态。", "After an asset change, the next observable participation record is needed to verify the current state."], ["当前没有触发不再被使用、使用减少或引用失效规则。", "No no-longer-used, reduced-use or broken-reference rule is currently triggered."], ["当前没有触发不再被使用、使用减少或引用失效判定。", "No no-longer-used, reduced-use or broken-reference decision is currently triggered."], ["历史参与至少达到 5 个相关任务且参与率至少 30%，最近 8 个相关任务记录到 0 次参与。", "At least five related tasks and a 30% participation baseline are recorded, while the latest eight related tasks record zero participation."], ["最近至少 5 个相关任务的参与率低于已记录基线的一半。", "Participation across at least five recent related tasks is below half of the recorded baseline."], ["已检查引用中有 1 个明确缺失，就标记为引用失效。", "One explicitly missing checked reference is enough to mark a broken reference."], ["同一会话中同时记录明确调用和明确绕行，才标记为调用后未遵循。", "Called but not followed is marked only when explicit invocation and explicit bypass occur in the same session."], ["资产记录时间至少 30 天，且累计参与不超过 2 次，才标记为几乎未使用。", "Rarely used is marked only after 30 days with no more than two recorded participations."],
    ["判定依据可在详情页继续查看。", "Decision evidence can be inspected further on the detail page."], ["尚未运行状态评估；缺少记录不代表数量为零。", "State evaluation has not run; missing records do not mean zero."], ["当前数据没有记录该资产是否被加载或使用，无法判断参与情况。", "Current data does not record whether this asset was loaded or used, so participation cannot be judged."], ["还没有运行状态评估，因此暂不判断。", "State evaluation has not run, so no judgment is made yet."], ["资产已经记录过修改，等待后续相关任务来验证当前状态。", "An asset change is recorded; a later related task is needed to verify the current state."], ["用户已归档这个资产；源文件没有被 Flatline 修改或删除。", "The user archived this asset; Flatline did not modify or delete the source file."], ["停止监测", "Stop monitoring"], ["忽略此状态", "Ignore this state"], ["类型", "Type"], ["基线 – 现在", "Baseline – current"], ["基线 未记录", "Baseline not recorded"], ["未记录相关任务", "Related tasks not recorded"], ["资产版本变化已记录", "Asset version change recorded"], ["资产版本事实已记录", "Asset version fact recorded"], ["环境变化已记录", "Environment change recorded"], ["环境事实", "Environment fact"], ["源路径：", "Source path: "], ["资产内容版本已记录。", "Asset content version recorded."], ["行数未记录", "Line count not recorded"], ["事件最多", "Most events"],
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
  const EN_SESSION_FIRST_TEXT = [
    ["项目接口未就绪", "Project interface not ready"], ["未记录项目", "No project recorded"], ["全部项目", "All projects"],
    ["详细统计", "Detailed statistics"], ["常见任务标签", "Common task tags"], ["最近会话", "Recent sessions"], ["资产关注", "Asset attention"],
    ["未记录会话来源", "Session sources not recorded"], ["返回会话列表", "Back to the session list"]
  ];
  // Friction rule sentences are generated by the classifier: only the Chinese
  // frame is UI copy, the quoted literal stays as the recorded evidence.
  const EN_FRICTION_TEXT = [
    ["工具是测试命令且输出包含 ", "Tool is a test command and output contains "],
    ["工具是构建命令且输出包含 ", "Tool is a build command and output contains "],
    ["输出包含 ", "Output contains "],
    ["消息文本以 ", "Message text starts with "],
    [" 开头", ""],
    ["明确记录 ", "Explicitly records "],
    [" 且未命中更具体规则", " with no more specific rule matched"],
    ["规则未记录", "Rule not recorded"],
    ["命中的规则", "Rule that matched"],
    ["工具未记录", "Tool not recorded"],
    ["未分类", "Not classified"]
  ];
  function translateUI(value) {
    if (view.locale !== "en") return String(value == null ? "" : value);
    let output = String(value == null ? "" : value);
    for (const [zh, en] of EN_TEXT.concat(EN_EXTRA_TEXT, EN_SESSION_FIRST_TEXT, EN_FRICTION_TEXT).filter(([zh]) => zh !== "条").slice().sort((a, b) => b[0].length - a[0].length)) output = output.split(zh).join(en);
    return output;
  }
  function localizeDOM() {
    if (view.locale !== "en" || !root) return;
    const skip = (node) => node.parentElement && node.parentElement.closest("[data-no-translate=\"true\"], .source-code, .event-payload, .fl-row-name, .session-title, .session-shell-title, .session-human-title, .session-task-value, .event-title, .event-evidence, .session-row-fact, .session-chat-preview, .session-chat-code, .session-chat-locator, .session-inspector-title, .cleanup-asset strong, .detail-title-line h1, .session-item-meta, .session-item-snippet, .session-tag, .sidebar-project-name, .overview-project-name, .overview-session-meta, .overview-tag, .session-note textarea");
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
  const SPARK_AREA_MIN_POINTS = 5;
  const sparkLabel = (sparse, size) => sparse
    ? uiText("参与率 · " + size + " 个样本点", "Participation rate, " + size + " sample points")
    : uiText("参与率曲线", "Participation rate curve");
  const sparkSampleNote = (large, sparse, size) => large && sparse && size
    ? '<span class="fl-spark-note">' + esc(uiText("样本 " + size + " 个 · 不足 " + SPARK_AREA_MIN_POINTS + " 个不画趋势面", size + " samples, under " + SPARK_AREA_MIN_POINTS + ": no trend area is drawn")) + "</span>"
    : "";
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
    // Under five points the fill has no shape to describe: two or three values
    // near the top flood the whole 34px box and read as a grey block rather
    // than a trend. Those series are drawn as bare points plus the line.
    const sparse = points.length < SPARK_AREA_MIN_POINTS;
    const areaPaths = [];
    if (!sparse) groups.filter((group) => group.length > 1).forEach((group) => {
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
    // A gap says "no value recorded here". Over a dense series that is a narrow
    // band; over two points it is 84% of the width and becomes the only thing
    // the row shows. Sparse series keep the fact as a strip on the floor.
    const gapTop = sparse ? floor - 2 : 0;
    const gapHeight = sparse ? 3 : height;
    const gapHTML = gaps.map((item) => {
      const left = Math.max(0, xOf(item.start) - step / 2);
      const right = Math.min(width, xOf(item.end) + step / 2);
      return '<rect class="fl-spark-gap" x="' + left.toFixed(2) + '" y="' + gapTop.toFixed(2) + '" width="' + Math.max(0, right - left).toFixed(2) + '" height="' + gapHeight + '"></rect>';
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
    const baseHTML = base == null || sparse ? "" : '<line class="fl-spark-base" x1="0" y1="' + yOf(base).toFixed(2) + '" x2="' + width + '" y2="' + yOf(base).toFixed(2) + '"></line>';
    const areaHTML = areaPaths.map((area) => '<path class="fl-spark-area" data-seg="' + area.tone + '" d="' + esc(area.d) + '"></path>').join("");
    // A zero-length round-capped stroke stays a circle under the non-uniform
    // viewBox scaling that an <circle r> would be squashed by.
    const pointHTML = !sparse ? "" : points.map((point) => {
      const [cx, cy] = coord(point);
      return '<path class="fl-spark-point" d="M' + cx.toFixed(2) + "," + cy.toFixed(2) + "L" + cx.toFixed(2) + "," + cy.toFixed(2) + '"></path>';
    }).join("");
    return '<span class="fl-spark" data-size="' + (large ? "lg" : "sm") + '" data-tone="' + tone + '" data-sparse="' + sparse + '" data-animated="true"><svg viewBox="0 0 ' + width + ' ' + height + '" preserveAspectRatio="none" shape-rendering="geometricPrecision" role="img" aria-label="' + esc(sparkLabel(sparse, points.length)) + '">' + gapHTML + baseHTML + ruleHTML + areaHTML + '<path class="fl-spark-line" d="' + esc(line) + '"></path>' + pointHTML + "</svg>" + sparkSampleNote(large, sparse, points.length) + '<span class="fl-spark-marks">' + markerHTML + '</span></span>';
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

  // friction_link_count is how many friction records hit this asset's hook. A
  // zero and a missing field are both silence, so neither draws a badge: the
  // asset detail page is where the absence is stated.
  function assetFrictionBadge(item) {
    const total = num(item && item.friction_link_count);
    if (!total) return "";
    return '<span class="fl-row-friction">' + icon("triangle-alert") + "<span>" + esc(uiText("摩擦关联 " + total, "Friction links " + total)) + "</span></span>";
  }
  function assetRow(item) {
    const state = stateOf(item);
    const facts = factsOf(item);
    const last = facts.last_participation_at ? "最后参与 " + shortDate(facts.last_participation_at) : "最后参与 未记录";
    return '<a class="fl-row" data-key="asset:' + esc(item.id) + '" href="#/assets/' + encodeURIComponent(item.id) + '" data-muted="' + (["dormant", "no_opportunity", "unobservable", "archived", "not_evaluated"].includes(state) ? "true" : "false") + '"><span class="fl-state" data-state="' + esc(state) + '">' + icon(stateIcons[state] || "circle-slash") + "<span>" + esc(stateLabel(state)) + '</span></span><span class="fl-row-identity"><span class="fl-row-name"><span class="fl-row-title">' + esc(item.name) + "</span>" + assetFrictionBadge(item) + '</span><span class="fl-row-sub">' + esc(kind(item.kind) + " · " + scope(item.scope) + " · " + (item.source_path || "源路径未记录")) + '</span></span><span class="fl-row-spark">' + sparkline(facts, false, state) + '</span><span class="fl-row-ratio">' + ratioHTML(facts, state) + "<small>" + esc(last) + "</small></span></a>";
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
  function navRow(href, key, label, glyph, countValue, active) {
    return '<a class="us-nav-row" data-nav="' + key + '" data-active="' + active + '" href="' + href + '">' + icon(glyph, "nav-icon") + "<span>" + label + '</span><span class="nav-count">' + (countValue == null ? "" : esc(countValue)) + "</span></a>";
  }
  // is_home_dir (§13.10) is a mark, not a regrouping: sessions started from the
  // home directory still count as their own project row, but the label says so
  // instead of reading like a real repository.
  function homeDirBadge(project) {
    if (!project || project.is_home_dir !== true) return "";
    return '<span class="fl-home-badge" title="' + esc(uiText("会话从家目录或它的一级子目录发起，那里没有 .git；这里只标记，不改分组也不改计数", "Sessions started from the home directory or a first-level child of it with no .git; this is only a mark and changes neither grouping nor counts")) + '">' + uiText("家目录", "Home directory") + "</span>";
  }
  function projectLabelOf(project) {
    if (project && project.label) return project.label;
    if (project && project.key === "__unrecorded__") return view.locale === "en" ? "Project not recorded" : "项目未记录";
    return view.locale === "en" ? "Project not recorded" : "项目未记录";
  }
  // Two sidebar rows that truncate to the same characters are two rows the eye
  // cannot tell apart. The CSS clips the tail, and for names that share a long
  // head the tail is exactly what differs — so the shared head is elided here
  // instead, and the full path stays available on hover.
  const SIDEBAR_NAME_LIMIT = 22;
  function sidebarProjectNames(labels) {
    return labels.map((label, index) => {
      if (label.length <= SIDEBAR_NAME_LIMIT) return label;
      let shared = 0;
      labels.forEach((other, otherIndex) => {
        if (otherIndex === index) return;
        let common = 0;
        while (common < label.length && common < other.length && label[common] === other[common]) common += 1;
        if (common > shared) shared = common;
      });
      if (shared < SIDEBAR_NAME_LIMIT - 4) return label;
      return "…" + label.slice(label.length - (SIDEBAR_NAME_LIMIT - 1));
    });
  }
  const SIDEBAR_PROJECT_ROWS = 8;
  function sidebarProjects() {
    if (!shell.projectsReady) return '<div class="sidebar-loading">项目接口未就绪</div>';
    const projects = (shell.projects || []).slice().sort((a, b) => String(b.last_started_at || "").localeCompare(String(a.last_started_at || "")));
    if (!projects.length) return '<div class="sidebar-loading">未记录项目</div>';
    const active = parseHash();
    const selected = active.path === "/sessions" ? active.params.getAll("project") : active.path.startsWith("/projects/") ? [decodeURIComponent(active.path.slice("/projects/".length))] : [];
    const shown = projects.slice(0, SIDEBAR_PROJECT_ROWS);
    const names = sidebarProjectNames(shown.map((project) => String(projectLabelOf(project) || "")));
    const rows = shown.map((project, index) => '<a class="us-nav-row" data-active="' + selected.includes(project.key) + '" href="#/projects/' + encodeURIComponent(project.key) + '" title="' + esc(project.cwd || project.key || projectLabelOf(project)) + '">' + icon("folder", "nav-icon") + '<span class="sidebar-project-name" data-no-translate="true">' + esc(names[index]) + "</span>" + homeDirBadge(project) + '<span class="nav-count">' + esc(count(project.sessions)) + "</span></a>").join("");
    const rest = projects.length - shown.length;
    const allLabel = rest > 0 ? uiText("全部项目（另有 " + rest + " 个）", "All projects (" + rest + " more)") : uiText("全部项目", "All projects");
    return rows + '<a class="us-nav-row sidebar-all-projects" href="#/sessions">' + icon("layers", "nav-icon") + "<span>" + esc(allLabel) + '</span><span class="nav-count">' + projects.length + "</span></a>";
  }
  function sourceTotals() {
    const stored = shell.sourceCounts;
    if (stored && Object.keys(stored).length) return Object.assign({}, stored);
    const totals = {};
    (shell.projects || []).forEach((project) => {
      Object.entries(project.harnesses || {}).forEach(([key, value]) => { totals[key] = (totals[key] || 0) + (num(value) || 0); });
    });
    return totals;
  }
  function sidebarSources() {
    if (!shell.projectsReady) return '<div class="sidebar-loading">未记录会话来源</div>';
    const totals = sourceTotals();
    return Object.keys(totals).sort((a, b) => (num(totals[b]) || 0) - (num(totals[a]) || 0) || a.localeCompare(b))
      .map((key) => '<div class="sidebar-source">' + sourceIcon(key) + '<small>' + esc(count(totals[key])) + (view.locale === "en" ? " sessions" : " 条会话") + "</small></div>").join("") || '<div class="sidebar-loading">未记录会话来源</div>';
  }
  function renderShell() {
    const active = routeKey();
    const overview = cache.overview || {};
    const searchLabel = view.locale === "en" ? "Search" : "搜索";
    const attention = num(overview.assets && overview.assets.attention);
    const frictionTotal = num(overview.friction && overview.friction.total);
    const frictionCount = frictionTotal != null ? frictionTotal : (cache.friction && cache.friction.summary && num(cache.friction.summary.total_events) != null ? cache.friction.summary.total_events : "");
    const sessionCount = num(shell.status && shell.status.sessions);
    const shellBeforeSearch = '<div class="prototype-shell"><aside class="us-sidebar" aria-label="Flatline 导航"><div class="sidebar-top"><a class="brand" href="#/" aria-label="Flatline 总览"><span class="fl-mark" data-size="sm" aria-hidden="true">' + icon("activity") + '</span><span class="brand-word">Flatline</span><span class="brand-beta">BETA</span></a><label class="us-nav-row sidebar-search sidebar-search-input" for="flatline-search">' + icon("search", "search-icon") + '<span class="sr-only">' + searchLabel + '</span><input id="flatline-search" type="search" aria-label="' + searchLabel + '" placeholder="' + searchLabel + '" value="';
    const shellAfterSearch = '" autocomplete="off"><span class="search-shortcut">⌘K</span></label></div><div class="fl-scroll sidebar-scroll"><div class="sidebar-group">' + navRow("#/", "overview", "总览", "chart-column", "", active === "overview") + navRow("#/sessions", "sessions", "会话", "layers", sessionCount == null ? "" : sessionCount, active === "sessions") + navRow("#/friction", "friction", "摩擦", "triangle-alert", frictionCount, active === "friction") + navRow("#/assets", "assets", "资产", "package", attention == null ? "" : attention || "", active === "assets") + navRow("#/timeline", "timeline", "变化时间线", "git-commit-horizontal", "", active === "timeline") + '</div><div class="sidebar-group" id="sidebar-projects"><div class="sidebar-group-label">项目</div>' + sidebarProjects() + '</div><div class="sidebar-group"><div class="sidebar-group-label">数据源</div>' + sidebarSources() + '</div></div><div class="sidebar-footer"><button class="us-nav-row sidebar-local-row" type="button" data-action="local-mode"><span class="local-mark">L</span><span><strong>本地模式</strong><small>数据不离开本机</small></span><i class="online-dot" title="daemon 在线"></i><span class="local-chevron">' + icon("chevronUp") + '</span></button></div></aside><main class="prototype-main" aria-live="polite">' + importProgress() + '<div id="flatline-screen"></div></main><div class="global-search-panel" id="flatline-search-panel" hidden></div></div>';
    root.innerHTML = shellBeforeSearch + esc(view.search) + shellAfterSearch;
    shell.built = true;
    shell.chrome = view.locale + "\x1f" + view.theme;
    const footer = root.querySelector(".sidebar-footer");
    if (footer) {
      footer.insertAdjacentHTML("beforeend", '<div class="sidebar-controls"><button class="sidebar-control" type="button" data-action="locale" title="切换语言"><span>' + (view.locale === "en" ? "中文" : "English") + '</span></button><button class="sidebar-control" type="button" data-action="theme" title="切换主题"><span>' + (view.theme === "dark" ? "浅色模式" : "深色模式") + '</span></button></div>');
    }
    localizeDOM();
    renderNotification();
    renderSearchPanel();
  }
  // The sidebar box is the global entry point: it queries /api/v1/search and
  // lists sessions, projects, assets, programs and friction categories. The
  // asset wall keeps its own filter input inside the wall page.
  function globalSearchItems() {
    const data = view.searchResults;
    if (!data) return [];
    const items = [];
    (Array.isArray(data.sessions) ? data.sessions : []).forEach((item) => items.push({
      group: uiText("会话", "Sessions"),
      href: "#/sessions/" + encodeURIComponent(item.id),
      label: compactEvidence(sessionTitleParts(item).title, 96),
      sub: [item.project_label || uiText("项目未记录", "Project not recorded"), shortDate(item.started_at)].join(" · "),
      glyph: "layers"
    }));
    (Array.isArray(data.projects) ? data.projects : []).forEach((item) => items.push({
      group: uiText("项目", "Projects"),
      href: "#/projects/" + encodeURIComponent(item.key),
      label: projectLabelOf(item),
      sub: item.cwd || (num(item.sessions) == null ? uiText("会话数未记录", "Session count not recorded") : quantity(item.sessions, "个会话", "session", "sessions")),
      glyph: "folder"
    }));
    (Array.isArray(data.assets) ? data.assets : []).forEach((item) => items.push({
      group: uiText("资产", "Assets"),
      href: "#/assets/" + encodeURIComponent(item.id),
      label: item.name || item.id,
      sub: [kind(item.kind), stateLabel(item.state || "not_evaluated")].join(" · "),
      glyph: "package"
    }));
    (Array.isArray(data.programs) ? data.programs : []).forEach((entry) => {
      const name = typeof entry === "string" ? entry : (entry.program || entry.key || "");
      if (!name) return;
      const calls = typeof entry === "string" ? null : num(entry.calls);
      items.push({
        group: uiText("命令", "Commands"),
        href: "#/sessions?program=" + encodeURIComponent(name),
        label: name,
        sub: calls == null ? uiText("调用次数未记录", "Call count not recorded") : quantity(calls, "次调用", "call", "calls"),
        glyph: "hash"
      });
    });
    (Array.isArray(data.friction_categories) ? data.friction_categories : []).forEach((entry) => items.push({
      group: uiText("摩擦类别", "Friction categories"),
      href: "#/friction?category=" + encodeURIComponent(entry.category || FRICTION_UNCLASSIFIED),
      label: frictionCategoryLabel(entry.category),
      sub: num(entry.count) == null ? uiText("计数未记录", "Count not recorded") : quantity(entry.count, "条", "record", "records"),
      glyph: "triangle-alert"
    }));
    return items;
  }
  function renderSearchPanel() {
    const host = document.getElementById("flatline-search-panel");
    if (!host) return;
    if (!view.searchOpen) { host.hidden = true; host.innerHTML = ""; return; }
    const items = globalSearchItems();
    view.searchIndex = Math.max(0, Math.min(view.searchIndex, Math.max(0, items.length - 1)));
    let body;
    if (view.searchError) {
      body = '<div class="global-search-empty"><strong>' + uiText("全局搜索接口未就绪。", "The global search interface is not ready.") + '</strong><span data-no-translate="true">' + esc(view.searchError) + "</span></div>";
    } else if (view.searchLoading && !view.searchResults) {
      body = '<div class="global-search-empty"><strong>' + uiText("正在搜索本地事实…", "Searching local facts…") + "</strong></div>";
    } else if (!items.length) {
      body = '<div class="global-search-empty"><strong>' + uiText("没有匹配的本地记录。", "No local record matches.") + '</strong><span>' + uiText("搜索会话、项目、资产、命令与摩擦类别。", "Searches sessions, projects, assets, commands and friction categories.") + "</span></div>";
    } else {
      let lastGroup = "";
      body = items.map((item, index) => {
        const head = item.group === lastGroup ? "" : '<div class="global-search-group">' + esc(item.group) + "</div>";
        lastGroup = item.group;
        return head + '<a class="global-search-row" data-search-index="' + index + '" data-selected="' + (index === view.searchIndex) + '" href="' + esc(item.href) + '">' + icon(item.glyph) + '<span class="global-search-copy"><strong data-no-translate="true">' + esc(item.label) + '</strong><small data-no-translate="true">' + esc(item.sub) + "</small></span></a>";
      }).join("");
    }
    host.innerHTML = '<div class="global-search-panel-inner"><div class="global-search-body">' + body + '</div><div class="global-search-foot">' + uiText("回车打开第一项 · 上下键选择 · Esc 关闭", "Enter opens the first result · arrow keys to move · Esc to close") + "</div></div>";
    host.hidden = false;
  }
  function closeGlobalSearch() {
    view.searchOpen = false;
    view.searchResults = null;
    view.searchError = "";
    view.searchIndex = 0;
    renderSearchPanel();
  }
  function openSearchResult(index) {
    const items = globalSearchItems();
    const target = items[index];
    if (!target) return;
    closeGlobalSearch();
    location.hash = target.href.replace(/^#/, "#");
  }
  function runGlobalSearch(value) {
    const query = String(value || "").trim();
    clearTimeout(view.searchTimer);
    if (!query) {
      view.searchOpen = false;
      view.searchResults = null;
      view.searchError = "";
      renderSearchPanel();
      return;
    }
    view.searchOpen = true;
    view.searchLoading = true;
    renderSearchPanel();
    view.searchTimer = setTimeout(async () => {
      const request = ++view.searchRequest;
      try {
        const data = await get("/api/v1/search?q=" + encodeURIComponent(query) + "&limit=10");
        if (request !== view.searchRequest) return;
        view.searchResults = data;
        view.searchError = "";
      } catch (error) {
        if (request !== view.searchRequest) return;
        view.searchResults = null;
        view.searchError = error.message || String(error);
      }
      view.searchLoading = false;
      view.searchIndex = 0;
      renderSearchPanel();
    }, 200);
  }
  // ── UI component layer ────────────────────────────────────────────────
  // One popover engine backs every dropdown and filter panel: options travel
  // in a data attribute, so a re-render never leaves a stale handle behind.
  const MOTION_MS = 150;
  const reducedMotion = () => Boolean(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  function popoverRoot() {
    let host = document.getElementById("flatline-popover");
    if (!host) {
      host = document.createElement("div");
      host.id = "flatline-popover";
      host.className = "fl-popover";
      host.hidden = true;
      document.body.appendChild(host);
    }
    return host;
  }
  function closePopover() {
    const host = document.getElementById("flatline-popover");
    view.popover = null;
    if (!host || host.hidden) return;
    host.dataset.state = "closed";
    const finish = () => { host.hidden = true; host.innerHTML = ""; };
    if (reducedMotion()) finish();
    else setTimeout(finish, MOTION_MS);
  }
  function attrJSON(value) {
    return esc(JSON.stringify(value));
  }
  function readJSON(value, fallback) {
    try { return JSON.parse(value); } catch (_) { return fallback; }
  }
  // fl-select replaces the native dropdown element: a trigger button plus a listbox
  // popover with type-ahead, arrow keys, Enter and Esc.
  function selectControl(action, label, options, selected, config) {
    const settings = config || {};
    const list = (Array.isArray(options) ? options : []).map((option) => ({ v: String(option.value == null ? "" : option.value), l: String(option.label == null ? "" : option.label) }));
    const current = String(selected == null ? "" : selected);
    const match = list.find((option) => option.v === current);
    const searchable = settings.searchable != null ? settings.searchable : list.length > 8;
    const display = match ? match.l : (settings.placeholder || label);
    return '<button type="button" class="fl-select" data-action="fl-select" data-select-action="' + esc(action) + '" data-select-value="' + esc(current) + '" data-select-label="' + esc(label) + '" data-select-searchable="' + searchable + '" data-select-options="' + attrJSON(list) + '" aria-haspopup="listbox" aria-expanded="false" aria-label="' + esc(label) + '"' + (settings.size ? ' data-size="' + esc(settings.size) + '"' : "") + '><span class="fl-select-value"' + (settings.raw ? ' data-no-translate="true"' : "") + ">" + esc(display) + "</span>" + icon("chevron-down", "fl-select-caret") + "</button>";
  }
  function segmentControl(action, key, options, selected) {
    return '<div class="fl-segment" role="group">' + (Array.isArray(options) ? options : []).map((option) => '<button class="fl-segment-btn" type="button" data-action="' + esc(action) + '" data-' + esc(key) + '="' + esc(option.value) + '" data-active="' + (String(option.value) === String(selected)) + '" aria-pressed="' + (String(option.value) === String(selected)) + '">' + esc(option.label) + "</button>").join("") + "</div>";
  }
  function tabControl(action, tabs, active) {
    return '<div class="fl-tabs" role="tablist">' + tabs.map((tab) => '<button class="fl-tab" type="button" role="tab" data-action="' + esc(action) + '" data-tab="' + esc(tab.value) + '" data-active="' + (tab.value === active) + '" aria-selected="' + (tab.value === active) + '">' + esc(tab.label) + "</button>").join("") + "</div>";
  }
  // A drawn checkbox: the input stays for keyboard and assistive tech, the box
  // is ours, and the checked state comes from the caller (which reads the URL).
  function checkControl(action, label, checked) {
    return '<label class="fl-check"><input type="checkbox" data-action="' + esc(action) + '"' + (checked ? " checked" : "") + '><span class="fl-check-box">' + icon("check") + "</span><span>" + esc(label) + "</span></label>";
  }
  function chipControl(label, clearAction, extra) {
    return '<span class="fl-chip"' + (extra || "") + '><span class="fl-chip-label" data-no-translate="true">' + esc(label) + "</span>" + (clearAction ? '<button type="button" class="fl-chip-clear" data-action="' + esc(clearAction.action) + '"' + (clearAction.data || "") + ' aria-label="' + esc(clearAction.label) + '">' + icon("x") + "</button>" : "") + "</span>";
  }
  // The filter button collects every low-frequency dimension into one grouped
  // panel; what is chosen comes back out as chips next to the search box.
  function filterControl(action, groups, activeCount) {
    const payload = groups.map((group) => ({ k: group.key, l: group.label, v: String(group.value == null ? "" : group.value), o: group.options.map((option) => ({ v: String(option.value == null ? "" : option.value), l: String(option.label == null ? "" : option.label) })) }));
    return '<button type="button" class="fl-filter-btn" data-action="fl-filter" data-filter-action="' + esc(action) + '" data-filter-groups="' + attrJSON(payload) + '" aria-haspopup="dialog" aria-expanded="false">' + icon("list-filter") + "<span>" + uiText("筛选", "Filters") + "</span>" + (activeCount ? '<span class="fl-filter-count">' + activeCount + "</span>" : "") + "</button>";
  }
  // fl-daterange replaces the pair of native date fields. The trigger
  // states the chosen range; the popover shows two month grids, month stepping
  // and the same quick ranges the segment control offers. Its value is the
  // {from,to} pair of YYYY-MM-DD days, the format the activity bar's drag
  // selection already produces, so both writers agree.
  const DAY_MS = 86400000;
  const dayKeyOf = (value) => {
    const date = value instanceof Date ? value : new Date(value);
    return Number.isFinite(date.getTime()) ? date.toISOString().slice(0, 10) : "";
  };
  const dayFromKey = (key) => {
    const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(key || ""));
    return match ? new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]))) : null;
  };
  const monthKeyOf = (key) => String(key || "").slice(0, 7);
  const shiftMonth = (monthKey, delta) => {
    const match = /^(\d{4})-(\d{2})$/.exec(String(monthKey || ""));
    if (!match) return monthKey;
    const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1 + delta, 1));
    return date.toISOString().slice(0, 7);
  };
  function dayLabel(key) {
    const date = dayFromKey(key);
    if (!date) return view.locale === "en" ? "Not recorded" : "未记录";
    return view.locale === "en"
      ? String(date.getUTCMonth() + 1) + "/" + date.getUTCDate()
      : (date.getUTCMonth() + 1) + "/" + date.getUTCDate();
  }
  function dateRangeLabel(from, to) {
    if (!from && !to) return view.locale === "en" ? "Custom range" : "自定义范围";
    if (from && !to) return dayLabel(from) + " " + uiText("起", "onwards");
    if (!from && to) return uiText("至 ", "until ") + dayLabel(to);
    return dayLabel(from) + uiText(" 至 ", " to ") + dayLabel(to);
  }
  function dateRangeControl(action, fromValue, toValue, label) {
    const from = String(fromValue || "").slice(0, 10);
    const to = String(toValue || "").slice(0, 10);
    const active = Boolean(from || to);
    return '<button type="button" class="fl-daterange" data-action="fl-daterange" data-daterange-action="' + esc(action) + '" data-daterange-from="' + esc(from || "") + '" data-daterange-to="' + esc(to || "") + '" data-active="' + active + '" aria-haspopup="dialog" aria-expanded="false" aria-label="' + esc(label) + '">' + icon("calendar") + '<span class="fl-daterange-value" data-no-translate="true">' + esc(dateRangeLabel(from, to)) + "</span>" + icon("chevron-down", "fl-select-caret") + "</button>";
  }
  function openDateRangePopover(trigger) {
    const rect = trigger.getBoundingClientRect();
    const from = trigger.dataset.daterangeFrom || "";
    const to = trigger.dataset.daterangeTo || "";
    const anchorMonth = monthKeyOf(from || to || dayKeyOf(new Date()));
    view.popover = {
      kind: "daterange",
      action: trigger.dataset.daterangeAction || "",
      label: uiText("时间范围", "Date range"),
      from: from,
      to: to,
      pendingFrom: "",
      month: shiftMonth(anchorMonth, -1),
      rect: { top: rect.top, bottom: rect.bottom, left: rect.left, width: Math.max(rect.width, 340) }
    };
    renderPopover();
  }
  function monthGrid(monthKey, state) {
    const match = /^(\d{4})-(\d{2})$/.exec(String(monthKey || ""));
    if (!match) return "";
    const year = Number(match[1]);
    const month = Number(match[2]) - 1;
    const first = new Date(Date.UTC(year, month, 1));
    const lead = (first.getUTCDay() + 6) % 7;
    const days = new Date(Date.UTC(year, month + 1, 0)).getUTCDate();
    const today = dayKeyOf(new Date());
    const start = state.pendingFrom || state.from;
    const end = state.pendingFrom ? "" : state.to;
    const cells = [];
    for (let index = 0; index < lead; index += 1) cells.push('<span class="fl-cal-cell" data-blank="true"></span>');
    for (let day = 1; day <= days; day += 1) {
      const key = new Date(Date.UTC(year, month, day)).toISOString().slice(0, 10);
      const selected = key === start || key === end;
      const inRange = Boolean(start && end && key > start && key < end);
      const future = key > today;
      cells.push('<button type="button" class="fl-cal-cell" data-action="fl-daterange-day" data-day="' + key + '" data-selected="' + selected + '" data-in-range="' + inRange + '" data-today="' + (key === today) + '"' + (future ? " disabled" : "") + ">" + day + "</button>");
    }
    const monthNames = view.locale === "en"
      ? ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"]
      : ["1 月", "2 月", "3 月", "4 月", "5 月", "6 月", "7 月", "8 月", "9 月", "10 月", "11 月", "12 月"];
    const weekNames = view.locale === "en" ? ["M", "T", "W", "T", "F", "S", "S"] : ["一", "二", "三", "四", "五", "六", "日"];
    const heading = view.locale === "en" ? monthNames[month] + " " + year : year + " 年 " + monthNames[month];
    return '<div class="fl-cal"><div class="fl-cal-head">' + esc(heading) + '</div><div class="fl-cal-week">' + weekNames.map((name) => "<span>" + esc(name) + "</span>").join("") + '</div><div class="fl-cal-grid">' + cells.join("") + "</div></div>";
  }
  function renderDateRangePanel(state) {
    const quick = [
      { days: 7, label: uiText("近 7 天", "Last 7 days") },
      { days: 30, label: uiText("近 30 天", "Last 30 days") },
      { days: 90, label: uiText("近 90 天", "Last 90 days") }
    ].map((entry) => '<button type="button" class="us-btn" data-variant="ghost" data-size="sm" data-action="fl-daterange-quick" data-days="' + entry.days + '">' + esc(entry.label) + "</button>").join("");
    const status = state.pendingFrom
      ? uiText("已选起始 " + state.pendingFrom + "，再点一天作为结束", "Start " + state.pendingFrom + " chosen; click another day for the end")
      : dateRangeLabel(state.from, state.to);
    return '<div class="fl-popover-panel fl-daterange-panel" role="dialog" aria-label="' + esc(state.label) + '">'
      + '<div class="fl-popover-head fl-daterange-head"><button type="button" class="fl-cal-nav" data-action="fl-daterange-month" data-delta="-1" aria-label="' + uiText("上一月", "Previous month") + '">' + icon("arrow-left") + '</button><span data-no-translate="true">' + esc(status) + '</span><button type="button" class="fl-cal-nav" data-action="fl-daterange-month" data-delta="1" aria-label="' + uiText("下一月", "Next month") + '">' + icon("arrow-right") + "</button></div>"
      + '<div class="fl-cal-pair">' + monthGrid(state.month, state) + monthGrid(shiftMonth(state.month, 1), state) + "</div>"
      + '<div class="fl-popover-foot fl-daterange-foot">' + quick + '<span class="fl-spacer"></span><button type="button" class="us-btn" data-variant="ghost" data-size="sm" data-action="fl-daterange-clear">' + uiText("清除", "Clear") + "</button></div></div>";
  }
  function popoverItems() {
    const state = view.popover;
    if (!state) return [];
    if (state.kind === "select") {
      const query = state.query.trim().toLocaleLowerCase();
      return state.options.filter((option) => !query || option.l.toLocaleLowerCase().includes(query)).map((option) => ({ value: option.v, label: option.l, group: "", selected: option.v === state.value }));
    }
    const query = state.query.trim().toLocaleLowerCase();
    const items = [];
    state.groups.forEach((group) => {
      group.o.filter((option) => !query || option.l.toLocaleLowerCase().includes(query)).forEach((option) => {
        items.push({ value: option.v, label: option.l, group: group.l, groupKey: group.k, selected: option.v === group.v });
      });
    });
    return items;
  }
  function renderPopover() {
    const state = view.popover;
    const host = popoverRoot();
    if (!state) { closePopover(); return; }
    if (state.kind === "daterange") {
      host.innerHTML = renderDateRangePanel(state);
      host.hidden = false;
      positionPopover(host, state.rect);
      requestAnimationFrame(() => { host.dataset.state = "open"; });
      return;
    }
    const items = popoverItems();
    if (state.active >= items.length) state.active = Math.max(0, items.length - 1);
    let lastGroup = "";
    const rows = items.map((item, index) => {
      const head = item.group && item.group !== lastGroup ? '<div class="fl-popover-group">' + esc(item.group) + "</div>" : "";
      lastGroup = item.group;
      return head + '<button type="button" class="fl-popover-row" role="option" data-popover-index="' + index + '" data-active="' + (index === state.active) + '" data-selected="' + item.selected + '"><span class="fl-popover-check">' + (item.selected ? icon("check") : "") + '</span><span class="fl-popover-label" data-no-translate="true">' + esc(item.label) + "</span></button>";
    }).join("");
    const search = state.searchable
      ? '<label class="fl-popover-search">' + icon("search") + '<input type="search" data-popover-search placeholder="' + uiText("筛选选项", "Filter options") + '" value="' + esc(state.query) + '" aria-label="' + uiText("筛选选项", "Filter options") + '"></label>'
      : "";
    const foot = state.kind === "filter"
      ? '<div class="fl-popover-foot"><button type="button" class="us-btn" data-variant="ghost" data-size="sm" data-action="fl-filter-clear">' + uiText("清空全部", "Clear all") + "</button></div>"
      : "";
    const empty = '<div class="fl-popover-empty">' + uiText("没有匹配的选项。", "No option matches.") + "</div>";
    host.innerHTML = '<div class="fl-popover-panel" role="' + (state.kind === "filter" ? "dialog" : "listbox") + '" aria-label="' + esc(state.label) + '"><div class="fl-popover-head">' + esc(state.label) + "</div>" + search + '<div class="fl-popover-body">' + (rows || empty) + "</div>" + foot + "</div>";
    host.hidden = false;
    positionPopover(host, state.rect);
    requestAnimationFrame(() => { host.dataset.state = "open"; });
    const field = host.querySelector("[data-popover-search]");
    if (field && state.focusSearch) { field.focus(); field.setSelectionRange(field.value.length, field.value.length); }
    const active = host.querySelector('.fl-popover-row[data-active="true"]');
    if (active && active.scrollIntoView) active.scrollIntoView({ block: "nearest" });
  }
  function positionPopover(host, rect) {
    const panel = host.querySelector(".fl-popover-panel");
    if (!panel || !rect) return;
    const width = Math.max(rect.width, 220);
    panel.style.minWidth = width + "px";
    const height = panel.offsetHeight;
    const below = window.innerHeight - rect.bottom;
    const flip = below < height + 16 && rect.top > height + 16;
    const top = flip ? Math.max(8, rect.top - height - 6) : rect.bottom + 6;
    const left = Math.min(Math.max(8, rect.left), Math.max(8, window.innerWidth - panel.offsetWidth - 8));
    host.style.top = top + "px";
    host.style.left = left + "px";
    host.dataset.flip = String(flip);
  }
  function openSelectPopover(trigger) {
    const rect = trigger.getBoundingClientRect();
    view.popover = {
      kind: "select",
      action: trigger.dataset.selectAction || "",
      label: trigger.dataset.selectLabel || "",
      value: trigger.dataset.selectValue || "",
      options: readJSON(trigger.dataset.selectOptions, []),
      searchable: trigger.dataset.selectSearchable === "true",
      focusSearch: true,
      query: "",
      active: 0,
      rect: { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width }
    };
    const index = view.popover.options.findIndex((option) => option.v === view.popover.value);
    view.popover.active = index < 0 ? 0 : index;
    renderPopover();
  }
  function openFilterPopover(trigger) {
    const rect = trigger.getBoundingClientRect();
    view.popover = {
      kind: "filter",
      action: trigger.dataset.filterAction || "",
      label: uiText("筛选", "Filters"),
      groups: readJSON(trigger.dataset.filterGroups, []),
      searchable: true,
      focusSearch: true,
      query: "",
      active: 0,
      rect: { top: rect.top, bottom: rect.bottom, left: rect.left, width: Math.max(rect.width, 320) }
    };
    renderPopover();
  }
  function commitPopover(index) {
    const state = view.popover;
    if (!state) return;
    const items = popoverItems();
    const item = items[index == null ? state.active : index];
    if (!item) return;
    const action = state.action;
    if (state.kind === "select") {
      closePopover();
      handleSelectChange(action, item.value);
      return;
    }
    const group = state.groups.find((entry) => entry.k === item.groupKey);
    if (group) group.v = item.value;
    renderPopover();
    handleFilterChange(action, item.groupKey, item.value);
  }
  // Keyed rows already survive a morphdom update. Capture their old positions so
  // a sort, filter or lazy page append can move the existing rows into place
  // instead of making the whole list jump. New rows get a short entrance pulse.
  function motionPositions(host) {
    const positions = new Map();
    if (reducedMotion() || !host || typeof host.querySelectorAll !== "function") return positions;
    const nodes = [...host.querySelectorAll("[data-key]")].slice(0, 240);
    nodes.forEach((node, index) => {
      const key = node.dataset && node.dataset.key;
      if (!key || positions.has(key)) return;
      const rect = node.getBoundingClientRect();
      if (!rect.width && !rect.height) return;
      positions.set(key, { node, left: rect.left, top: rect.top, index });
    });
    return positions;
  }
  function animateMotionPositions(before, host) {
    if (!before || reducedMotion() || !host || typeof Element === "undefined" || typeof Element.prototype.animate !== "function") return;
    const after = motionPositions(host);
    after.forEach((current, key) => {
      const previous = before.get(key);
      if (!previous) {
        if (current.index > 24) return;
        current.node.animate([
          { opacity: 0, transform: "translate3d(0, 5px, 0)" },
          { opacity: 1, transform: "translate3d(0, 0, 0)" }
        ], { duration: 190, delay: Math.min(current.index * 8, 120), easing: "cubic-bezier(0.22, 1, 0.36, 1)", fill: "both" });
        return;
      }
      const dx = previous.left - current.left;
      const dy = previous.top - current.top;
      if (Math.abs(dx) < 1 && Math.abs(dy) < 1) return;
      current.node.animate([
        { transform: "translate3d(" + dx + "px, " + dy + "px, 0)" },
        { transform: "translate3d(0, 0, 0)" }
      ], { duration: 220, easing: "cubic-bezier(0.22, 1, 0.36, 1)", fill: "both" });
    });
  }
  function pulseMotion(host) {
    if (reducedMotion() || !host) return;
    const token = (host.__flatlineMotionToken || 0) + 1;
    host.__flatlineMotionToken = token;
    host.dataset.motionPhase = "swap";
    void host.offsetWidth;
    setTimeout(() => {
      if (host.__flatlineMotionToken !== token || host.dataset.motionPhase !== "swap") return;
      host.dataset.motionPhase = "settle";
      requestAnimationFrame(() => {
        if (host.__flatlineMotionToken !== token || host.dataset.motionPhase !== "settle") return;
        delete host.dataset.motionPhase;
        delete host.__flatlineMotionToken;
      });
    }, Math.round(MOTION_MS * 0.5));
  }
  // Two different operations used to share one code path. They are separate now:
  //   in-page update (same route)  – patch the existing DOM with morphdom, no
  //                                  full-page rebuild, focus and scroll position
  //                                  survive while changed components settle;
  //   route change (different page) – replace the content and play the page
  //                                  transition once.
  function morphInto(host, html, childrenOnly) {
    if (typeof morphdom !== "function") {
      host.innerHTML = html;
      pulseMotion(host);
      return false;
    }
    const previousPositions = motionPositions(host);
    const active = document.activeElement;
    const activeSelection = active && (active.tagName === "INPUT" || active.tagName === "TEXTAREA") && typeof active.selectionStart === "number"
      ? { start: active.selectionStart, end: active.selectionEnd }
      : null;
    morphdom(host, "<" + host.tagName.toLowerCase() + ">" + html + "</" + host.tagName.toLowerCase() + ">", {
      childrenOnly: Boolean(childrenOnly),
      getNodeKey: (node) => (node.id || (node.dataset && node.dataset.key)) || undefined,
      onBeforeElUpdated: (from, to) => {
        if (from.isEqualNode(to)) {
          // An input's live value is not an attribute, so two nodes can be
          // "equal" while the field on screen still shows text the daemon
          // rejected. A field nobody is typing in follows the markup.
          if (from !== document.activeElement && from.tagName === "INPUT" && to.hasAttribute("value") && from.value !== to.getAttribute("value")) from.value = to.getAttribute("value");
          return false;
        }
        // A field the user is typing in keeps its value, caret and checked state.
        if (from === document.activeElement && (from.tagName === "INPUT" || from.tagName === "TEXTAREA")) {
          to.value = from.value;
          if (from.type === "checkbox" || from.type === "radio") to.checked = from.checked;
        }
        return true;
      }
    });
    if (active && activeSelection && document.contains(active) && typeof active.setSelectionRange === "function") {
      try { active.setSelectionRange(activeSelection.start, activeSelection.end); } catch (_) {}
    }
    animateMotionPositions(previousPositions, host);
    pulseMotion(host);
    return true;
  }
  function setScreen(html) {
    const host = document.getElementById("flatline-screen");
    if (!host) return;
    closePopover();
    const routeChanged = shell.paintedRoute !== shell.screenKey;
    shell.paintedRoute = shell.screenKey;
    if (!routeChanged && host.childElementCount) {
      morphInto(host, html, true);
      return;
    }
    host.innerHTML = html;
    if (!host.firstElementChild || reducedMotion()) return;
    host.dataset.entering = "true";
    requestAnimationFrame(() => {
      requestAnimationFrame(() => { host.dataset.entering = "false"; });
    });
  }
  // Switching a tab inside a page replaces only the affected panels, so the
  // page header, task line and annotations never repaint.
  function swapPanels(markup, selectors) {
    const holder = document.createElement("div");
    holder.innerHTML = markup;
    const pairs = selectors.map((selector) => [document.querySelector(selector), holder.querySelector(selector)]);
    if (pairs.some(([current, next]) => !current || !next)) return false;
    closePopover();
    pairs.forEach(([current, next]) => morphInto(current, next.innerHTML, true));
    return true;
  }
  // A skeleton has the shape of the finished page, so a route change never
  // shows blank space or a spinner while its data is being read.
  function skeletonBlock(height, width) {
    return '<span class="fl-skeleton" style="height:' + height + 'px' + (width ? ";width:" + width : "") + '"></span>';
  }
  function skeletonRows(count, height) {
    return Array.from({ length: count }, () => '<div class="fl-skeleton-row">' + skeletonBlock(height || 18) + "</div>").join("");
  }
  function skeletonFor(path) {
    const card = (body, wide) => '<section class="elevated-card card-pad stats-card' + (wide ? " wide" : "") + '">' + body + "</section>";
    if (path === "/sessions") {
      return header(uiText("会话", "Sessions"), uiText("查找与管理会话", "Find and manage sessions"), skeletonBlock(28, "150px"))
        + screenContent('<div class="session-toolbar">' + skeletonBlock(34) + '<div class="fl-skeleton-line">' + skeletonBlock(30, "150px") + skeletonBlock(30, "120px") + skeletonBlock(30, "220px") + '</div>' + skeletonBlock(74) + '</div><div class="session-list-scroll"><div class="session-list">' + skeletonRows(12, 44) + "</div></div>", "session-page", "session-page-scroll");
    }
    if (path.startsWith("/sessions/")) {
      return '<header class="detail-header session-shell-header">' + skeletonBlock(30, "260px") + "</header>" + screenContent(card(skeletonBlock(120) + skeletonRows(10, 26), true), "session-detail-page", "session-detail-scroll");
    }
    if (path.startsWith("/friction")) {
      return header(uiText("摩擦", "Friction"), "", skeletonBlock(28, "120px"))
        + screenContent(skeletonBlock(46) + '<div class="fl-skeleton-line">' + skeletonBlock(58, "160px") + skeletonBlock(58, "160px") + skeletonBlock(58, "160px") + skeletonBlock(58, "160px") + "</div>" + card(skeletonRows(9, 46), true), "friction-page", "friction-page-scroll");
    }
    if (path.startsWith("/projects")) {
      return header(uiText("项目", "Project"), "", skeletonBlock(28, "150px")) + screenContent('<div class="stats-grid overview-grid">' + card(skeletonBlock(64), true) + card(skeletonBlock(76), true) + card(skeletonBlock(180), true) + '<div class="overview-pair">' + card(skeletonRows(5, 20)) + card(skeletonRows(5, 20)) + "</div></div>", "prototype-page");
    }
    if (path === "/stats") {
      return header(uiText("数据", "Data"), "", skeletonBlock(28, "260px")) + screenContent('<div class="stats-grid">' + card(skeletonRows(6, 20), true) + card(skeletonBlock(60), true) + card(skeletonRows(8, 22), true) + "</div>", "prototype-page");
    }
    if (path === "/timeline") {
      return header(uiText("变化时间线", "Timeline"), "", skeletonBlock(28, "220px")) + screenContent('<div class="fl-track">' + skeletonRows(10, 58) + "</div>", "timeline-page");
    }
    if (path === "/assets" || path.startsWith("/assets/")) {
      return header(uiText("资产", "Assets"), "", skeletonBlock(28, "220px")) + screenContent(card(skeletonRows(10, 40), true), "wall-page");
    }
    return header(uiText("总览", "Overview"), "", skeletonBlock(28, "260px"))
      + screenContent('<div class="stats-grid overview-grid">' + card(skeletonBlock(74), true) + card(skeletonBlock(150), true) + card(skeletonBlock(150), true) + '<div class="overview-pair">' + card(skeletonRows(5, 20)) + card(skeletonRows(5, 20)) + "</div></div>", "prototype-page");
  }
  function header(title, summary, right) {
    return '<header class="screen-header"><div class="screen-header-left"><h1>' + esc(title) + '</h1>' + (summary ? '<span class="header-summary">' + summary + "</span>" : "") + '</div><div class="screen-header-right">' + (right || "") + "</div></header>";
  }
  function screenContent(body, className, scrollClass) {
    return '<div class="screen-scroll' + (scrollClass ? " " + scrollClass : "") + '"><div class="screen-content ' + (className || "") + '">' + body + "</div></div>";
  }
  function filteredAssets() {
    const all = cache.assets && cache.assets.assets || [];
    const query = view.wallSearch.trim().toLocaleLowerCase();
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
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>' + uiText("参与漏斗", "Participation funnel") + '</h3><span class="fl-aside">' + uiText("基线 – 现在", "Baseline – current") + ' · ' + esc(opportunity) + '</span></header>' + (rows ? '<div class="fl-funnel">' + rows + "</div>" : '<div class="empty-copy"><strong>' + uiText("参与记录未形成漏斗。", "No participation funnel was formed.") + '</strong>' + uiText("当前没有可展示的分子 / 分母。", "No displayable numerator / denominator is recorded.") + '</div>') + (funnel.note ? '<p class="evidence-note">' + esc(funnel.note) + "</p>" : "") + "</section>";
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
      const range = previous ? shortDate(previous.observed_at) + " – " + shortDate(item.observed_at) : shortDate(item.observed_at);
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
      const session = related.find((item) => item.id === id);
      const title = sessionTitle(session || { id: id });
      const meta = session ? source(session.source) + " · " + shortDate(session.started_at) : (view.locale === "en" ? "Session id: " : "会话标识：") + shortSessionID(id);
      const mark = recorded.has(id) ? '<span class="fl-flag" data-flag="new">' + icon("check") + uiText("已记录参与", "Participation recorded") + '</span>' : opportunityIDs.has(id) ? '<span class="fl-flag" data-flag="na">' + icon("slash") + uiText("未记录参与", "Participation not recorded") + '</span>' : "";
      return '<a class="session-link" href="#/sessions/' + encodeURIComponent(id) + '"><span><strong>' + esc(title) + '</strong><small>' + esc(meta) + '</small></span>' + mark + "</a>";
    }).join("");
    const note = ids.length > 3 ? uiText("最近 3 条", "Latest 3") : quantity(ids.length, "条", "session", "sessions");
    return '<section class="elevated-card card-pad"><header class="fl-head"><h3>' + uiText("关联会话", "Related sessions") + '</h3><span class="fl-aside">' + note + "</span></header>" + (rows ? '<div class="fl-list session-link-list" data-first-rule="false">' + rows + "</div>" : '<div class="empty-copy"><strong>' + uiText("当前没有相关会话。", "No related sessions.") + '</strong><span>' + uiText("没有记录到可下钻的会话关联。", "No session association is recorded for drill-down.") + '</span></div>') + "</section>";
  }
  // friction_links are the friction records that hit this asset's hook: each
  // one names the signature, the session it happened in and when. An absent
  // field is stated as not recorded rather than drawn as an empty list, which
  // would read as "no friction touched this asset".
  function assetFrictionCard(data) {
    const asset = data.asset || {};
    const links = Array.isArray(asset.friction_links) ? asset.friction_links
      : Array.isArray(data.friction_links) ? data.friction_links : null;
    const total = num(asset.friction_link_count);
    const aside = total == null ? uiText("未记录", "Not recorded") : quantity(total, "条", "record", "records");
    const head = '<section class="elevated-card card-pad"><header class="fl-head"><h3>' + uiText("来自摩擦的参与证据", "Participation evidence from friction") + '</h3><span class="fl-aside">' + esc(aside) + "</span></header>";
    if (links == null) return head + '<div class="empty-copy"><strong>' + uiText("摩擦关联未记录。", "Friction links are not recorded.") + '</strong><span>' + uiText("daemon 尚未返回 friction_links。", "The daemon does not return friction_links yet.") + "</span></div></section>";
    if (!links.length) return head + '<div class="empty-copy"><strong>' + uiText("没有摩擦记录撞到该资产的 hook。", "No friction record hit this asset's hook.") + "</strong></div></section>";
    const rows = links.slice(0, 5).map((link) => {
      const href = frictionSessionHref(link);
      const signature = link.sample_line || link.signature || uiText("签名未记录", "Signature not recorded");
      const session = link.session_title || (link.session_id ? shortSessionID(link.session_id) : uiText("会话未记录", "Session not recorded"));
      const open = href ? "a" : "span";
      return "<" + open + ' class="session-link"' + (href ? ' href="' + esc(href) + '"' : "") + '><span><strong data-no-translate="true">' + esc(signature) + "</strong><small>" + esc(session + " · " + shortDate(link.occurred_at)) + "</small></span></" + open + ">";
    }).join("");
    return head + '<div class="fl-list session-link-list" data-first-rule="false">' + rows + "</div></section>";
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
    const detailBody = '<div class="screen-scroll"><div class="detail-wrap"><div class="detail-grid"><main class="detail-main">' + main + "</main><aside class=\"detail-aside\">" + dispositionCard(item, data) + sessionsCard(data) + assetFrictionCard(data) + '</aside></div></div></div>';
    setScreen(detailHeader + detailTabs + detailBody);
    localizeDOM();
  }
  function noteDataVersion(value) {
    const version = num(value);
    if (version == null || version === shell.dataVersion) return false;
    shell.dataVersion = version;
    httpCache.clear();
    clearData();
    return true;
  }
  async function get(path) {
    const stored = httpCache.get(path);
    const headers = { Accept: "application/json" };
    if (stored && stored.etag) headers["If-None-Match"] = stored.etag;
    const response = await fetch(path, { headers });
    if (response.status === 304 && stored) return stored.body;
    if (!response.ok) throw new Error(response.status + " " + await response.text());
    const body = await response.json();
    noteDataVersion(body && body.data_version);
    const etag = response.headers.get("ETag");
    if (etag) httpCache.set(path, { etag, body });
    return body;
  }
  async function send(method, path, body) {
    const response = await fetch(path, { method, headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error(response.status + " " + await response.text());
    httpCache.clear();
    return response.json();
  }
  const post = (path, body) => send("POST", path, body);
  const put = (path, body) => send("PUT", path, body);
  function notify(message, tone) {
    if (!toast) return;
    toast.innerHTML = '<div class="toast" data-tone="' + esc(tone || "info") + '">' + icon(tone === "error" ? "triangle-alert" : tone === "success" ? "check" : "slash") + "<span>" + esc(translateUI(message)) + "</span></div>";
    toast.hidden = false;
    clearTimeout(notify.timer);
    notify.timer = setTimeout(() => { toast.hidden = true; }, 4200);
  }
  function withParams(path, params) {
    const text = params.toString();
    return text ? path + "?" + text : path;
  }
  function parseHash() {
    const raw = location.hash || "#/";
    const body = raw.startsWith("#") ? raw.slice(1) : raw;
    const mark = body.indexOf("?");
    return { path: (mark >= 0 ? body.slice(0, mark) : body) || "/", params: new URLSearchParams(mark >= 0 ? body.slice(mark + 1) : "") };
  }
  function routeKey() {
    const path = parseHash().path;
    if (path.startsWith("/assets") || path === "/cleanup") return "assets";
    if (path.startsWith("/sessions")) return "sessions";
    if (path.startsWith("/friction")) return "friction";
    if (path.startsWith("/timeline")) return "timeline";
    if (path.startsWith("/projects")) return "projects";
    if (path === "/stats") return "data";
    return "overview";
  }
  async function loadWallAssets() {
    if (cache.assets && cache.assetsMode === "wall") return cache.assets;
    cache.assets = await get("/api/v1/assets?view=wall&limit=5000");
    cache.assetsMode = "wall";
    return cache.assets;
  }
  async function loadNotifications() {
    if (cache.notifications) return cache.notifications;
    cache.notifications = (await get("/api/v1/notifications?limit=5000")).notifications || [];
    return cache.notifications;
  }
  async function loadShellData() {
    shell.status = await get("/api/v1/ingest/status");
    try {
      const data = await get("/api/v1/projects");
      shell.projects = Array.isArray(data.projects) ? data.projects : [];
      shell.projectsReady = true;
    } catch (_) {
      shell.projects = null;
      shell.projectsReady = false;
    }
    // Adapters are added over time; the source list is read from the store
    // rather than written into the page, so a new one appears on its own.
    try {
      const stats = await get("/api/v1/stats");
      shell.sourceCounts = stats && stats.source_counts && typeof stats.source_counts === "object" ? stats.source_counts : null;
    } catch (_) {
      shell.sourceCounts = null;
    }
  }
  function importing() {
    return Boolean(shell.status && shell.status.status === "importing");
  }
  // "Rescan" posts to the daemon. 202 starts a pass and the shell import bar
  // takes over; 409 means a pass is already running and nothing is started.
  async function triggerRefresh() {
    try {
      const response = await fetch("/api/v1/ingest/refresh", { method: "POST", headers: { Accept: "application/json" } });
      if (response.status === 409) {
        notify(uiText("正在导入，本轮完成后再试。", "An import is already running; try again when it finishes."), "info");
        return false;
      }
      if (response.status === 503) {
        notify(uiText("重新扫描接口未就绪：daemon 没有挂载 /api/v1/ingest/refresh。", "The rescan interface is not ready: the daemon has not mounted /api/v1/ingest/refresh."), "error");
        return false;
      }
      if (!response.ok) throw new Error(response.status + " " + await response.text());
      httpCache.clear();
      notify(uiText("已触发重新扫描；顶部显示导入进度。", "Rescan triggered; import progress is shown at the top."), "success");
      try {
        shell.status = await get("/api/v1/ingest/status");
        updateShellChrome();
      } catch (_) {}
      scheduleStatusPoll();
      return true;
    } catch (error) {
      notify(uiText("重新扫描未触发：", "Rescan was not triggered: ") + (error.message || error), "error");
      return false;
    }
  }
  function scheduleStatusPoll() {
    clearTimeout(shell.pollTimer);
    shell.pollTimer = setTimeout(pollStatus, importing() ? 3000 : 20000);
  }
  async function pollStatus() {
    try {
      const before = shell.dataVersion;
      shell.status = await get("/api/v1/ingest/status");
      if (shell.dataVersion !== before) {
        await loadShellData();
        await route(true);
      } else {
        updateShellChrome();
      }
    } catch (_) {}
    scheduleStatusPoll();
  }
  // The import bar names the pass that is running. "pairing" (§13.3) reads
  // native files only to record which tool result came from which call; it
  // writes no event. "reparse" is the same read-only shape for a re-derived
  // projection. Both report their own files_read/files, not the history pass's.
  const IMPORT_PHASE_COPY = {
    pairing: ["正在配对工具身份", "Pairing tool identity"],
    reparse: ["正在重新解析", "Re-parsing"]
  };
  function importPhaseProgress(progress) {
    const phase = progress && progress.phase;
    const copy = IMPORT_PHASE_COPY[phase];
    if (!copy) return null;
    const detail = progress[phase] || {};
    const read = num(detail.files_read);
    const total = num(detail.files);
    const known = read != null && total != null && total > 0;
    // reparse also reports what the re-read actually added (§20.3): events are
    // inserted idempotently, so this is the number of records the older parser
    // had missed, not a rewrite of anything already stored.
    const inserted = num(detail.events_inserted);
    const insertedLabel = phase === "reparse" && inserted != null
      ? uiText(" · 补回 " + inserted + " 条记录", " · " + inserted + " records recovered")
      : "";
    const label = (known
      ? uiText(copy[0] + " " + read + "/" + total + " 个文件", copy[1] + " " + read + "/" + total + " files")
      : uiText(copy[0] + " · 文件进度未记录", copy[1] + " · file progress not recorded")) + insertedLabel;
    return { label, percent: known ? Math.max(1, Math.min(100, read / total * 100)) : 0, known };
  }
  function importProgress() {
    if (!importing()) return "";
    const progress = (shell.status && shell.status.import) || {};
    const phase = importPhaseProgress(progress);
    const read = phase ? null : num(progress.files_read);
    const seen = phase ? null : num(progress.files_seen);
    const known = phase ? phase.known : (read != null && seen != null && seen > 0);
    const percent = phase ? phase.percent : (known ? Math.max(1, Math.min(100, read / seen * 100)) : 0);
    const label = phase ? phase.label : (known
      ? (view.locale === "en" ? "Reading local history " + read + "/" + seen + " files" : "正在读取本地历史 " + read + "/" + seen + " 个文件")
      : (view.locale === "en" ? "Reading local history · file progress not recorded" : "正在读取本地历史 · 文件进度未记录"));
    const warnings = Array.isArray(progress.warnings) ? progress.warnings.length : null;
    const warnLabel = warnings ? uiText(" · " + warnings + " 条告警", " · " + warnings + " warnings") : "";
    return '<div class="import-progress" role="status"><span class="import-progress-track"><i style="--w:' + percent.toFixed(1) + '%" data-known="' + known + '"></i></span><span class="import-progress-copy">' + esc(label + warnLabel) + '</span></div>';
  }
  function updateShellChrome() {
    const main = root.querySelector(".prototype-main");
    if (!main) return;
    const existing = main.querySelector(".import-progress");
    const markup = importProgress();
    if (!markup) { if (existing) existing.remove(); }
    else if (existing) existing.outerHTML = markup;
    else main.insertAdjacentHTML("afterbegin", markup);
    const overview = cache.overview || {};
    const attention = num(overview.assets && overview.assets.attention);
    const frictionTotal = num(overview.friction && overview.friction.total);
    const frictionCached = cache.friction && cache.friction.summary ? num(cache.friction.summary.total_events) : null;
    setNavBadge("sessions", num(shell.status && shell.status.sessions));
    setNavBadge("assets", attention || null);
    setNavBadge("friction", frictionTotal != null ? frictionTotal : frictionCached);
  }
  function setNavBadge(key, value) {
    const badge = root.querySelector('[data-nav="' + key + '"] .nav-count');
    if (badge) badge.textContent = value == null ? "" : String(value);
  }
  function metric(item) {
    const tone = item.tone || "";
    const missing = item.value === "未记录" || item.value === "Not recorded";
    return '<div class="stat-metric prototype-stat-metric" data-missing="' + missing + '"' + (item.title ? ' title="' + esc(item.title) + '"' : "") + '><span class="stat-label"><span class="stat-icon" data-tone="' + esc(tone || "muted") + '">' + icon(item.icon || "package") + '</span><span>' + esc(item.label) + '</span></span><span class="fl-metric"><b data-tone="' + esc(tone) + '">' + esc(item.value) + "</b>" + (item.detail ? "<small>" + esc(item.detail) + "</small>" : "") + "</span></div>";
  }
  function activityHeatmap(data) {
    const activity = data.activity_by_day || {};
    const values = Object.values(activity).map((entry) => num(entry && entry.sessions)).filter((value) => value != null);
    const maximum = values.reduce((value, item) => Math.max(value, item), 1);
    const end = data.last_event_at ? new Date(data.last_event_at) : new Date();
    end.setUTCHours(0, 0, 0, 0);
    const start = new Date(end.getTime() - 363 * 86400000);
    const cells = [];
    for (let offset = 363; offset >= 0; offset -= 1) {
      const day = new Date(end.getTime() - offset * 86400000);
      const key = day.toISOString().slice(0, 10);
      const entry = activity[key];
      const value = num(entry && entry.sessions);
      const level = value == null ? 0 : Math.max(1, Math.ceil(value / maximum * 4));
      const detail = value == null
        ? (view.locale === "en" ? "Not recorded" : "未记录")
        : [quantity(value, "个会话", "session", "sessions"), quantity(num(entry.events), "个事件", "event", "events"), quantity(num(entry.friction), "条摩擦", "friction record", "friction records")].join(" · ");
      cells.push('<i class="heat-cell" data-level="' + level + '" data-missing="' + (value == null) + '" title="' + esc(key + " · " + detail) + '"></i>');
    }
    const legend = view.locale === "en" ? "More sessions" : "会话更多";
    const month = (value) => value.toISOString().slice(0, 7);
    return '<div class="heatmap-wrap"><div class="heatmap" aria-label="' + (view.locale === "en" ? "52-week session activity heatmap" : "52 周会话活动热力图") + '">' + cells.join("") + '</div></div><div class="heatmap-legend"><span>' + esc(month(start)) + '</span><span class="heatmap-legend-spacer"></span><span>' + (view.locale === "en" ? "Not recorded" : "未记录") + '</span><i data-level="0"></i><i data-level="1"></i><i data-level="2"></i><i data-level="3"></i><i data-level="4"></i><span>' + legend + '</span><span class="heatmap-legend-spacer"></span><span>' + esc(month(end)) + '</span></div>';
  }
  function distributionBar(label, value, total, tone, iconName) {
    const actual = num(value);
    const width = actual != null && total > 0 ? Math.max(0, Math.min(100, actual / total * 100)) : 0;
    return '<div class="distribution-row"><span class="distribution-name">' + icon(iconName || "package", "stats-list-icon") + '<span>' + esc(label) + '</span></span><span class="distribution-bar" data-empty="' + (actual == null || total <= 0) + '"><i data-tone="' + esc(tone || "muted") + '" style="--w:' + width.toFixed(1) + '%"></i></span><strong>' + esc(actual == null ? (view.locale === "en" ? "Not recorded" : "未记录") : actual) + '</strong></div>';
  }
  function sourceMark(value) {
    return '<span class="fl-mark" data-size="sm">' + brandSVG(value) + "</span>";
  }
  function unavailableStatsCard(title, aside, message, listShape) {
    return '<section class="elevated-card stats-card stats-unavailable"><header class="fl-head"><h3>' + esc(title) + '</h3><span class="fl-aside">' + esc(aside) + '</span></header><div class="' + (listShape ? "fl-list " : "") + 'stats-unavailable-body">' + icon("slash") + '<span>' + esc(message) + '</span></div></section>';
  }
  async function loadStats() {
    if (!cache.stats) cache.stats = await get("/api/v1/stats");
    noteUsageDefinition(cache.stats && cache.stats.usage);
    return cache.stats;
  }
  async function loadSources() {
    try {
      const data = await get("/api/v1/sources");
      view.dataPage.sources = Array.isArray(data.sources) ? data.sources : [];
      view.dataPage.sourcesNote = data.note || "";
      view.dataPage.sourcesNoteEN = data.note_en || "";
      view.dataPage.sourcesError = "";
    } catch (error) {
      view.dataPage.sources = null;
      view.dataPage.sourcesError = error.message || String(error);
    }
  }
  async function loadDataPage() {
    await loadStats().catch(() => {});
    await loadSources();
    try {
      view.dataPage.health = await get("/api/v1/ingest/health");
      view.dataPage.healthError = "";
    } catch (error) {
      view.dataPage.health = null;
      view.dataPage.healthError = error.message || String(error);
    }
    try {
      view.dataPage.tools = await get("/api/v1/tools");
      view.dataPage.toolsError = "";
    } catch (error) {
      view.dataPage.tools = null;
      view.dataPage.toolsError = error.message || String(error);
    }
  }
  function byteSize(value) {
    const bytes = num(value);
    if (bytes == null) return uiText("未记录", "Not recorded");
    if (bytes < 1024) return bytes + " B";
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + " KB";
    if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + " MB";
    return (bytes / 1073741824).toFixed(2) + " GB";
  }
  function dataFactList(items) {
    return '<div class="fl-list stats-fl-list">' + items.map(([label, value]) => '<div class="fl-li"><span>' + esc(label) + '</span><b data-no-translate="true">' + esc(value) + "</b></div>").join("") + "</div>";
  }
  // The health panel prints what the daemon reported. Every unrecorded field is
  // named with its own count instead of being folded into a single score.
  function dataHealthCard() {
    const health = view.dataPage.health;
    if (!health) {
      return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("库健康", "Database health") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + uiText("健康接口未就绪。", "The health interface is not ready.") + '</strong><span>' + uiText("daemon 尚未提供 /api/v1/ingest/health。", "The daemon does not yet serve /api/v1/ingest/health.") + '</span><p class="empty-copy-detail" data-no-translate="true">' + esc(view.dataPage.healthError) + "</p></div></section>";
    }
    const lastImport = health.last_import || {};
    const counts = health.counts || {};
    const unrecorded = health.unrecorded || {};
    const warnings = Array.isArray(health.warnings) ? health.warnings : [];
    const store = dataFactList([
      [uiText("schema 版本", "Schema version"), count(health.schema_version)],
      [uiText("数据库大小", "Database size"), byteSize(health.db_bytes)],
      [uiText("WAL 大小", "WAL size"), byteSize(health.wal_bytes)]
    ]);
    const importFacts = dataFactList([
      [uiText("上次导入开始", "Last import started"), date(lastImport.started_at)],
      [uiText("上次导入结束", "Last import finished"), lastImport.finished_at ? date(lastImport.finished_at) : uiText("未记录（可能仍在进行）", "Not recorded (may still be running)")],
      [uiText("文件发现 / 读取 / 跳过", "Files seen / read / skipped"), [count(lastImport.files_seen), count(lastImport.files_read), count(lastImport.files_skipped)].join(" / ")],
      [uiText("入库会话", "Sessions ingested"), count(lastImport.sessions_ingested)],
      [uiText("上次错误", "Last error"), lastImport.last_error || uiText("未记录", "Not recorded")]
    ]);
    const countFacts = dataFactList([
      [uiText("会话（主 / 子代理 / 空）", "Sessions main/sub/empty"), [count(counts.main_sessions), count(counts.subagent_sessions), count(counts.empty_sessions)].join(" / ")],
      [uiText("会话总数", "Sessions total"), count(counts.sessions)],
      [uiText("事件", "Events"), count(counts.events)],
      [uiText("摩擦", "Friction"), count(counts.friction)],
      [uiText("命令 / 文件", "Commands / files"), [count(counts.commands), count(counts.files)].join(" / ")],
      [uiText("资产", "Assets"), count(counts.assets)]
    ]);
    const missingFacts = dataFactList([
      [uiText("会话缺标题", "Sessions without a title"), count(unrecorded.sessions_without_title)],
      [uiText("会话缺工作目录", "Sessions without a working directory"), count(unrecorded.sessions_without_cwd)],
      [uiText("会话缺模型", "Sessions without a model"), count(unrecorded.sessions_without_model)],
      [uiText("会话缺开始时间", "Sessions without a start time"), count(unrecorded.sessions_without_started_at)],
      [uiText("摩擦缺工具身份", "Friction without a tool identity"), count(unrecorded.friction_without_tool)]
    ]);
    const warningBody = warnings.length
      ? '<pre class="event-payload" data-no-translate="true">' + esc(warnings.join("\n")) + "</pre>"
      : '<div class="empty-copy"><strong>' + uiText("最近没有记录到导入告警。", "No import warning was recorded recently.") + "</strong></div>";
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("库健康", "Database health") + '</h3><span class="fl-aside">' + uiText("不缓存 · 每次进入重新读取", "Not cached · re-read on every visit") + '</span></header><div class="data-health-grid">' + store + importFacts + countFacts + missingFacts + '</div><div class="session-inspector-label">' + uiText("最近 20 条告警原文", "Latest 20 warnings, verbatim") + "</div>" + warningBody + "</section>";
  }
  // One row per adapter the daemon knows about. "not_found" means the daemon
  // looked and the root is not on this machine — that is a fact, not a fault;
  // "not_scanned" means this process has not looked yet.
  const SOURCE_STATUS = {
    ok: ["已探测", "Detected", "good"],
    not_found: ["未探测到", "Not detected", "muted"],
    no_sessions: ["无会话", "No session", "warn"],
    error: ["读取出错", "Read error", "bad"],
    not_scanned: ["本次尚未扫描", "Not scanned this run", "muted"],
    // A6 added this one: a root that is in the registry but that this process
    // has not probed yet — the state every newly added source is in until the
    // next refresh pass reaches it.
    configured: ["已登记 · 本轮未探测", "Registered · not probed this pass", "muted"]
  };
  function sourceStatusBadge(value) {
    const entry = SOURCE_STATUS[value];
    if (!entry) return '<span class="source-status" data-tone="muted" data-missing="true">' + esc(value ? String(value) : uiText("状态未记录", "Status not recorded")) + "</span>";
    return '<span class="source-status" data-tone="' + esc(entry[2]) + '">' + esc(uiText(entry[0], entry[1])) + "</span>";
  }
  // §25.11: the registry answers "which machine's which directory did this
  // session come from". It is the entry point for multi-machine coverage: a
  // ~/.codex/sessions directory rsynced from a laptop is, until it is
  // registered here, indistinguishable from the local one.
  //
  // | 谁做 | 做什么 | 结果 |
  // | --- | --- | --- |
  // | 用户改名字或开关 | 失焦即 PUT，只改 label / machine_label / enabled | 名字与开关变了；根本身与源目录一个字节不动 |
  // | 用户添加一个根 | POST {kind, root, label} | 行建好；**下一轮 refresh 才真正去读**，所以旁边给了“立即重新扫描” |
  // | daemon 每轮 refresh | 只从 enabled=1 的根读 | 关掉一个源不会删掉它已经读进来的会话，只是不再读那个目录 |
  //
  // 容易误读的地方：*“POST 之后数据就进来了”*——没有；POST 只记录根。
  // *“关掉一个源会删掉它的会话”*——不会。
  //
  // The rows come from /api/v1/sources (the registry, which is what PUT writes
  // and returns); the probe status comes from ingest health, matched by
  // source_id, so "detected" and "configured" stay one table instead of two.
  function sourceStatusOf(sourceID) {
    const health = view.dataPage.health;
    const rows = health && Array.isArray(health.sources) ? health.sources : [];
    const match = rows.find((entry) => num(entry.source_id) != null && num(entry.source_id) === num(sourceID));
    return match ? match.status : "";
  }
  function sourceKindOptions() {
    const rows = Array.isArray(view.dataPage.sources) ? view.dataPage.sources : [];
    const kinds = [];
    rows.forEach((entry) => { if (entry.kind && kinds.indexOf(entry.kind) < 0) kinds.push(entry.kind); });
    return kinds.map((kind) => ({ value: kind, label: source(kind) }));
  }
  function sourceField(entry, field, label, placeholder) {
    return '<input class="fl-input" type="text" data-source-field="' + esc(field) + '" data-source-id="' + esc(String(entry.id)) + '" value="' + esc(entry[field] == null ? "" : String(entry[field])) + '" placeholder="' + esc(placeholder) + '" aria-label="' + esc(label) + '" data-no-translate="true">';
  }
  function sourceRow(entry) {
    const enabled = entry.enabled !== false;
    return '<div class="source-row" data-key="source:' + esc(String(entry.id)) + '">'
      + '<span class="source-row-name">' + sourceMark(entry.kind) + '<small data-no-translate="true">' + esc(source(entry.kind)) + "</small></span>"
      + sourceField(entry, "label", uiText("来源显示名", "Source display name"), uiText("未命名", "Unnamed"))
      + sourceField(entry, "machine_label", uiText("机器标签", "Machine label"), uiText("未记录", "Not recorded"))
      + '<span class="source-row-detail" data-no-translate="true" title="' + esc(entry.root || "") + '">' + esc(entry.root || uiText("路径未记录", "Root not recorded")) + "</span>"
      + sourceStatusBadge(sourceStatusOf(entry.id))
      + '<label class="fl-check source-row-enabled"><input type="checkbox" data-source-toggle data-source-id="' + esc(String(entry.id)) + '"' + (enabled ? " checked" : "") + ' aria-label="' + uiText("启用这个来源", "Read this source") + '"><span class="fl-check-box">' + icon("check") + "</span><span>" + uiText("启用", "Read") + "</span></label>"
      + '<span class="program-counts"><b>' + esc(count(entry.sessions)) + "</b><small>" + uiText("已入库会话", "Stored sessions") + "</small></span>"
      + '<span class="source-row-time">' + esc(shortDate(entry.last_session_at)) + "</span></div>";
  }
  function sourceAddForm() {
    const form = view.dataPage.sourceForm;
    const options = sourceKindOptions();
    return '<div class="source-add"><div class="session-inspector-label">' + uiText("添加来源", "Add a source") + "</div>"
      + '<p class="evidence-note">' + uiText("多机覆盖就在这里：把另一台机器同步过来的目录（例如 rsync 回来的 ~/.codex/sessions）登记为一个来源，给它一个机器标签，daemon 下一轮就会只读地扫它，读进来的会话从此说得出自己来自哪台机器。", "This is where multi-machine coverage happens: register a directory synced from another machine (an rsynced ~/.codex/sessions, say) as a source and give it a machine label. The daemon reads it read-only on the next pass, and every session read from it can say which machine it came from.") + "</p>"
      + '<div class="source-add-row">'
      + selectControl("source-kind", uiText("来源类型", "Source kind"), options, form.kind, { placeholder: uiText("选择来源类型", "Choose a source kind"), size: "sm" })
      + '<input class="fl-input source-add-root" type="text" data-source-form="root" value="' + esc(form.root || "") + '" placeholder="' + uiText("根路径（绝对路径，只读扫描）", "Root path (absolute, read-only scan)") + '" aria-label="' + uiText("根路径", "Root path") + '" data-no-translate="true">'
      + '<input class="fl-input" type="text" data-source-form="label" value="' + esc(form.label || "") + '" placeholder="' + uiText("显示名（可空）", "Display name (optional)") + '" aria-label="' + uiText("显示名", "Display name") + '" data-no-translate="true">'
      + '<button class="us-btn" data-variant="default" data-size="sm" data-action="source-add"' + (view.dataPage.sourceBusy ? " disabled" : "") + ">" + icon("plus") + uiText("添加", "Add") + "</button>"
      + '<button class="us-btn" data-variant="outline" data-size="sm" data-action="data-refresh"' + (importing() ? " disabled" : "") + ">" + icon("refreshCw") + uiText("立即重新扫描", "Rescan now") + "</button>"
      + "</div></div>";
  }
  function dataSourcesCard() {
    const sources = Array.isArray(view.dataPage.sources) ? view.dataPage.sources : null;
    if (!sources) {
      return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("数据源管理", "Sources") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + uiText("数据源接口未就绪。", "The source registry interface is not ready.") + '</strong><span>' + uiText("daemon 尚未提供 /api/v1/sources。", "The daemon does not yet serve /api/v1/sources.") + '</span><p class="empty-copy-detail" data-no-translate="true">' + esc(view.dataPage.sourcesError) + "</p></div></section>";
    }
    const head = '<div class="source-head"><span>' + uiText("来源", "Source") + "</span><span>" + uiText("显示名", "Display name") + "</span><span>" + uiText("机器标签", "Machine label") + "</span><span>" + uiText("根路径", "Root") + "</span><span>" + uiText("探测状态", "Probe") + "</span><span>" + uiText("读取", "Read") + "</span><span>" + uiText("会话", "Sessions") + "</span><span>" + uiText("最近会话", "Last session") + "</span></div>";
    const rows = sources.map(sourceRow).join("");
    const note = uiText(
      "改名与开关失焦即写入本地库；根路径不可改——换一个根就是另一个来源，就地改名会悄悄把已经读进来的会话重新归属。关掉一个来源只是不再读那个目录，已经读进来的记录原样留着。新增的根要等下一轮 refresh 才真正被读取。",
      "A rename or a toggle is written to the local database on blur; the root itself cannot be edited — a different root is a different source, and renaming one in place would silently refile every session already read from it. Turning a source off only stops the daemon reading that directory; the records already read stay as they are. A newly added root is read on the next refresh pass, not now.");
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("数据源管理", "Sources") + '</h3><span class="fl-aside">' + esc(quantity(sources.length, "个来源", "source", "sources")) + '</span></header><div class="source-table">' + head + (rows || '<div class="empty-copy"><strong>' + uiText("没有登记任何来源。", "No source is registered.") + "</strong></div>") + "</div>" + sourceAddForm() + '<p class="evidence-note">' + esc(note) + "</p>" + daemonSentence(view.dataPage.sourcesNote, view.dataPage.sourcesNoteEN, "p", "evidence-note") + "</section>";
  }
  // A rename, a machine label and the read switch are the only three things a
  // user may change; the daemon refuses anything else, and the response is the
  // whole list, so the table is patched from the answer rather than refetched.
  async function saveSource(id, patch) {
    try {
      const result = await put("/api/v1/sources", Object.assign({ id: Number(id) }, patch));
      if (result && Array.isArray(result.sources)) view.dataPage.sources = result.sources;
      notify(uiText("已保存；只读扫描，源目录未改变。", "Saved. The scan is read-only; the source directory was not changed."), "success");
      drawStats();
    } catch (error) {
      notify(uiText("保存来源失败：", "Could not save the source: ") + (error.message || String(error)), "error");
    }
  }
  async function addSource() {
    const form = view.dataPage.sourceForm;
    const kind = String(form.kind || "").trim();
    const root = String(form.root || "").trim();
    if (!kind) return notify(uiText("请先选择来源类型。", "Choose a source kind first."), "error");
    if (!root) return notify(uiText("请填写根路径（绝对路径）。", "Enter the root path as an absolute path."), "error");
    view.dataPage.sourceBusy = true;
    drawStats();
    try {
      const created = await post("/api/v1/sources", { kind, root, label: String(form.label || "").trim() });
      view.dataPage.sourceForm = { kind: "", root: "", label: "" };
      await loadDataPage();
      notify((created && created.message) || uiText("已登记；下一轮扫描生效。", "Registered. It takes effect on the next scan."), "success");
    } catch (error) {
      notify(uiText("添加来源失败：", "Could not add the source: ") + (error.message || String(error)), "error");
    }
    view.dataPage.sourceBusy = false;
    drawStats();
  }
  function currentSessionExportParams() {
    const key = view.sessionList.key || "";
    const mark = key.indexOf("?");
    const params = new URLSearchParams(mark >= 0 ? key.slice(mark + 1) : "");
    params.delete("group");
    return params;
  }
  function dataExportCard() {
    const params = currentSessionExportParams();
    const describe = params.toString() || uiText("默认筛选（仅主会话、不含空会话）", "Default filters (main sessions only, empty sessions excluded)");
    const link = (format, label) => {
      const query = new URLSearchParams(params);
      query.set("format", format);
      return '<a class="us-btn" data-variant="outline" data-size="sm" href="/api/v1/sessions/export?' + esc(query.toString()) + '">' + icon("archive") + esc(label) + "</a>";
    };
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("导出当前筛选的会话", "Export the currently filtered sessions") + '</h3><span class="fl-aside">' + uiText("最多 5000 行", "Up to 5000 rows") + '</span></header><div class="data-export-row">' + link("json", "JSON") + link("csv", "CSV") + '</div><p class="evidence-note">' + uiText("导出使用会话页最近一次的筛选条件：", "The export uses the session page's most recent filters: ") + '<code data-no-translate="true">' + esc(describe) + "</code></p></section>";
  }
  const TOOL_TABLE_ROWS = 40;
  function toolRows(list, nameOf, extraCell) {
    const all = Array.isArray(list) ? list : [];
    const shown = all.slice(0, TOOL_TABLE_ROWS);
    const rows = shown.map((entry) => '<div class="tool-row" data-key="tool:' + esc(nameOf(entry)) + ":" + esc(entry.harness || "") + '"><span class="command-program" data-no-translate="true">' + esc(nameOf(entry)) + "</span>" + (extraCell ? extraCell(entry) : "") + '<span class="program-counts"><b>' + esc(count(entry.calls)) + '</b><small>' + uiText("调用", "Calls") + '</small></span><span class="program-counts"><b>' + esc(count(entry.sessions)) + '</b><small>' + uiText("会话", "Sessions") + '</small></span><span class="program-outcome" data-tone="' + (num(entry.failures) ? "bad" : "muted") + '">' + esc(failureText(entry)) + "</span></div>").join("");
    const more = all.length > shown.length ? '<div class="tool-more">' + uiText("显示前 " + shown.length + " 行，共 " + all.length + " 行。", "Showing the first " + shown.length + " of " + all.length + " rows.") + "</div>" : "";
    return rows + more;
  }
  // programs[].family normalises python / python3 (and the like) onto one name.
  // Calls, recorded outcomes, failures and expected exits are counts of calls,
  // so they add up across a family. Session counts do not — one session can run
  // both python and python3 — so they stay on the expanded per-name rows.
  function programFamilyGroups(list) {
    const groups = new Map();
    (Array.isArray(list) ? list : []).forEach((entry) => {
      const key = entry.family || entry.program || "";
      if (!groups.has(key)) groups.set(key, { family: key, members: [], calls: 0, known_outcomes: 0, failures: 0, expected_exits: 0 });
      const group = groups.get(key);
      group.members.push(entry);
      group.calls += num(entry.calls) || 0;
      group.known_outcomes += num(entry.known_outcomes) || 0;
      group.failures += num(entry.failures) || 0;
      group.expected_exits += num(entry.expected_exits) || 0;
    });
    return Array.from(groups.values()).sort((a, b) => b.calls - a.calls);
  }
  function programCountCell(value, label) {
    return '<span class="program-counts"><b>' + esc(count(value)) + "</b><small>" + esc(label) + "</small></span>";
  }
  // §20.1: an expected exit is a nonzero code the program documents as an
  // answer. It is counted separately so it never reads as a failure.
  function programExpectedCell(value) {
    const parsed = num(value);
    return '<span class="program-counts" data-zero="' + (parsed === 0) + '" title="' + esc(uiText("程序把这个非零码文档化为一种回答（如 rg/grep 无匹配），不计入失败", "The program documents this non-zero code as an answer (rg or grep found no match); it is not counted as a failure")) + '"><b>' + esc(count(value)) + "</b><small>" + uiText("预期退出", "Expected exits") + "</small></span>";
  }
  function programMemberRow(entry) {
    return '<div class="tool-row" data-key="prog:' + esc(entry.program || "") + '" data-member="true"><span class="command-program" data-no-translate="true">' + esc(entry.program || uiText("程序未记录", "Program not recorded")) + "</span>"
      + programCountCell(entry.calls, uiText("调用", "Calls"))
      + programCountCell(entry.sessions, uiText("会话", "Sessions"))
      + programExpectedCell(entry.expected_exits)
      + '<span class="program-outcome" data-tone="' + (num(entry.failures) ? "bad" : "muted") + '">' + esc(failureText(entry)) + "</span></div>";
  }
  function programFamilyRow(group) {
    const single = group.members.length === 1;
    if (single) return programMemberRow(group.members[0]);
    const open = Boolean(view.toolFamilyOpen[group.family]);
    const names = group.members.map((entry) => entry.program).filter(Boolean).join(" · ");
    const head = '<div class="tool-row" data-key="fam:' + esc(group.family) + '" data-family-open="' + open + '"><button type="button" class="tool-family-toggle" data-action="tool-family" data-family="' + esc(group.family) + '" aria-expanded="' + open + '" title="' + esc(names) + '">' + icon(open ? "chevron-down" : "chevron-right") + '<span class="command-program" data-no-translate="true">' + esc(group.family) + '</span><small>' + esc(uiText(group.members.length + " 个原名", group.members.length + " original names")) + "</small></button>"
      + programCountCell(group.calls, uiText("调用", "Calls"))
      + '<span class="program-counts" data-missing="true" title="' + esc(uiText("同一个会话可能同时用了这个家族里的多个程序，会话数不能相加；展开看每个原名的会话数。", "One session can use more than one program in this family, so session counts do not add up; expand to see the session count for each original name.")) + '"><b>' + uiText("展开看", "Expand") + "</b><small>" + uiText("会话", "Sessions") + "</small></span>"
      + programExpectedCell(group.expected_exits)
      + '<span class="program-outcome" data-tone="' + (group.failures ? "bad" : "muted") + '">' + esc(failureText(group)) + "</span></div>";
    return head + (open ? '<div class="tool-family-members">' + group.members.map(programMemberRow).join("") + "</div>" : "");
  }
  function programTable(list) {
    const groups = programFamilyGroups(list);
    const shown = groups.slice(0, TOOL_TABLE_ROWS);
    const rows = shown.map(programFamilyRow).join("");
    const more = groups.length > shown.length ? '<div class="tool-more">' + uiText("显示前 " + shown.length + " 个程序家族，共 " + groups.length + " 个。", "Showing the first " + shown.length + " of " + groups.length + " program families.") + "</div>" : "";
    return rows + more;
  }
  function dataToolsCard() {
    const tools = view.dataPage.tools;
    if (!tools) {
      return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("工具使用", "Tool usage") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + uiText("工具使用接口未就绪。", "The tool usage interface is not ready.") + '</strong><p class="empty-copy-detail" data-no-translate="true">' + esc(view.dataPage.toolsError) + "</p></div></section>";
    }
    const toolList = toolRows(tools.tools, (entry) => entry.tool_name || uiText("工具未记录", "Tool not recorded"), (entry) => '<span class="tool-harness">' + sourceIcon(entry.harness) + "</span>");
    const programList = programTable(tools.programs);
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("工具使用", "Tool usage") + '</h3><span class="fl-aside">' + uiText("失败来自退出码非零或 is_error；未记录不计入", "Failures come from a non-zero exit code or is_error; not recorded is not counted") + '</span></header><div class="session-inspector-label">' + uiText("按工具（失败 / 已记录结果；调用数减去已记录结果是未记录，不是成功）", "By tool (failures / recorded outcomes; calls minus recorded outcomes are not recorded, not successes)") + '</div><div class="tool-table">' + (toolList || '<div class="empty-copy"><strong>' + uiText("没有记录到工具调用。", "No tool call was recorded.") + '</strong></div>') + '</div><div class="session-inspector-label">' + uiText("按程序家族（python / python3 归一显示，展开看原名）", "By program family (python and python3 read as one; expand for the original names)") + '</div><div class="tool-table" data-programs="true">' + (programList || '<div class="empty-copy"><strong>' + uiText("没有记录到命令。", "No command was recorded.") + "</strong></div>") + "</div></section>";
  }
  function drawStats() {
    const data = cache.stats || {};
    const usage = data.usage || {};
    const metrics = [
      { label: "资产", value: count(data.asset_count), icon: "package" },
      { label: "会话", value: count(data.session_count), icon: "layers" },
      { label: "需要注意", value: count(["silent", "broken", "bypassed"].reduce((sum, key) => sum + (num((data.state_counts || {})[key]) || 0), 0)), icon: "triangle-alert", tone: "bad" },
      { label: "几乎未使用", value: count((data.state_counts || {}).dormant), icon: "package-open", tone: "warn" },
      // §25.3: /stats used to carry no measurement at all, so this page printed
      // "Token 未记录" while the overview printed 25.9B — one system, two
      // answers. It now returns the same usage object the overview reads, over
      // the whole database, with the same definition hung on the number.
      { label: uiText("成本", "Cost"), value: num(usage.cost) == null ? count(null) : "$" + Number(usage.cost).toFixed(2), icon: "wallet", detail: overviewRatio(usage.cost_sessions, usage.in_range, "个会话记录了成本", "sessions recorded a cost") },
      { label: uiText("工作 token", "Work tokens"), value: tokenText(usage.work_tokens), icon: "hash", detail: uiText("总 " + tokenText(usage.total_tokens) + " · " + count(usage.token_sessions) + "/" + count(usage.in_range) + " 个会话记录", "Total " + tokenText(usage.total_tokens) + " · " + count(usage.token_sessions) + "/" + count(usage.in_range) + " sessions recorded"), title: daemonProse(usage.work_definition, usage.work_definition_en) || tokenTitle() }
    ];
    const counts = data.state_counts || {};
    const total = Object.values(counts).reduce((sum, value) => sum + (num(value) || 0), 0);
    const distribution = Object.keys(counts).sort().map((key) => distributionBar(stateLabel(key), counts[key], total, stateTone(key), stateIcons[key])).join("");
    // B6: the harness-count card that read /stats.source_counts is gone. The
    // source registry table above already names every source and its state, and
    // two cards for one subject is how the two come to disagree.
    const body = '<div class="stats-grid">' + dataHealthCard() + dataSourcesCard() + dataExportCard() + dataToolsCard() + '<section class="elevated-card stats-card stats-card-metrics wide"><div class="metric-grid">' + metrics.map(metric).join("") + '</div></section><section class="elevated-card stats-card"><header class="fl-head"><h3>状态分布</h3><span class="fl-aside">' + esc(data.asset_count == null ? (view.locale === "en" ? "Not recorded" : "未记录") : data.asset_count) + '</span></header><div class="fl-list stats-fl-list">' + (distribution || '<div class="empty-copy">状态分布未记录。</div>') + '</div></section>' + unavailableStatsCard("每日成本", view.locale === "en" ? "Not recorded" : "未记录", view.locale === "en" ? "Daily cost is not recorded." : "每日成本未记录。") + '</div>';
    const dataHeaderRight = '<a class="us-btn" data-variant="outline" data-size="sm" href="#/">' + icon("arrowLeft") + uiText("返回总览", "Back to overview") + '</a><button class="us-btn" data-variant="default" data-size="sm" data-action="data-refresh"' + (importing() ? " disabled" : "") + '>' + icon("refreshCw") + uiText("立即重新扫描", "Rescan now") + '</button><button class="us-btn" data-variant="outline" data-size="sm" data-action="export-stats">' + uiText("导出统计快照", "Export statistics snapshot") + '</button>';
    setScreen(header(uiText("数据", "Data"), uiText("本地库健康、导入、导出与工具使用", "Local database health, import, export and tool usage"), dataHeaderRight) + screenContent(body, "prototype-page"));
    localizeDOM();
  }

  // The range is either one of the presets or an explicit from/to pair typed
  // into the two date fields; both forms live in the hash.
  function overviewRangeParams() {
    const params = parseHash().params;
    const from = params.get("from") || "";
    const to = params.get("to") || "";
    if (from || to) return { mode: "custom", from, to };
    const value = params.get("range");
    const range = OVERVIEW_RANGES.includes(value) ? value : "30";
    return { mode: range, from: range === "all" ? "all" : isoDay(Number(range)), to: "" };
  }
  function overviewRange() {
    return overviewRangeParams().mode;
  }
  function overviewRangeKey() {
    const range = overviewRangeParams();
    return range.mode + "\x1f" + range.from + "\x1f" + range.to;
  }
  function timezoneOffsetMinutes() {
    return -new Date().getTimezoneOffset();
  }
  async function loadOverviewPage() {
    const range = overviewRangeParams();
    const query = new URLSearchParams();
    query.set("from", range.from || "all");
    if (range.to) query.set("to", range.to);
    // §22: compare=1 asks the daemon for the same numbers over the preceding
    // window of equal length. A daemon that does not implement it simply
    // returns no previous/delta, and every block says the comparison is not
    // ready rather than computing one here from a different caliber.
    query.set("compare", "1");
    try {
      cache.overview = await get(withParams("/api/v1/overview", query));
      cache.overviewRange = overviewRangeKey();
      noteUsageDefinition(cache.overview && cache.overview.usage);
      view.overviewReady = true;
    } catch (error) {
      cache.overview = null;
      view.overviewReady = false;
      view.overviewError = error.message || String(error);
    }
    // ADR-20: the cost-and-opportunity block reads its own endpoint so the
    // overview response does not grow for a projection most screens fold away.
    // A daemon without the endpoint renders the page without the block.
    try {
      cache.insights = await get(withParams("/api/v1/insights", query));
      view.insightsError = "";
    } catch (error) {
      cache.insights = null;
      view.insightsError = error.message || String(error);
    }
    // P16-3: the now view is live state served with no-store; a daemon
    // without the endpoint renders the page without the block.
    try {
      cache.now = await get("/api/v1/now");
    } catch (error) {
      cache.now = null;
    }
    const timeQuery = new URLSearchParams(query);
    timeQuery.set("tz_offset_minutes", String(timezoneOffsetMinutes()));
    try {
      view.overviewTime = await get(withParams("/api/v1/stats/time", timeQuery));
      view.overviewTimeError = "";
    } catch (error) {
      view.overviewTime = null;
      view.overviewTimeError = error.message || String(error);
    }
    await loadNotifications().catch(() => {});
  }
  // §22: each block compares this period against the preceding window of the
  // same length. The daemon returns `previous` (the same numbers over that
  // window) and `delta`; when it returns neither, the block says the
  // comparison is not ready rather than inventing one from another caliber.
  // `previous` is a flat record of the same numbers over the preceding window;
  // `delta` gives each change as {value, direction} with value already
  // absolute, so the direction is read from the daemon rather than recomputed.
  function overviewPreviousValue(path) {
    const previous = (cache.overview || {}).previous;
    if (!previous) return undefined;
    const found = path.split(".").reduce((node, key) => (node == null ? undefined : node[key]), previous);
    return Array.isArray(found) ? found.length : found;
  }
  function overviewDelta(key) {
    const delta = (cache.overview || {}).delta;
    const entry = delta ? delta[key] : null;
    if (!entry || num(entry.value) == null) return null;
    return { value: num(entry.value), direction: entry.direction || "flat" };
  }
  const DELTA_SIGNS = { up: "+", down: "-", flat: "" };
  function overviewDeltaText(deltaKey, format) {
    const entry = overviewDelta(deltaKey);
    if (!entry) return uiText("上期对比未就绪", "Previous-period comparison not ready");
    const render = format || ((value) => String(value));
    const value = Math.abs(entry.value);
    return (DELTA_SIGNS[entry.direction] || "") + render(value);
  }
  function compareText(previousPath, deltaKey, format) {
    const render = format || ((value) => String(value));
    const previous = num(overviewPreviousValue(previousPath));
    const delta = overviewDelta(deltaKey);
    if (previous == null && !delta) return uiText("上期对比未就绪", "Previous-period comparison not ready");
    const previousPart = previous == null ? count(null) : render(previous);
    if (!delta) return uiText("上期 " + previousPart, "Previous " + previousPart);
    const change = overviewDeltaText(deltaKey, format);
    return uiText("上期 " + previousPart + " · " + change, "Previous " + previousPart + " · " + change);
  }
  function compareAside(previousPath, deltaKey, format) {
    const text = overviewDeltaText(deltaKey, format);
    const delta = overviewDelta(deltaKey);
    const ready = Boolean(delta);
    const title = compareText(previousPath, deltaKey, format);
    return '<span class="overview-delta overview-compare" data-ready="' + ready + '" data-direction="' + esc(delta ? delta.direction : "") + '" title="' + esc(title) + '">' + esc(text) + "</span>";
  }
  // The window `previous` was measured over, so "previous" is never a word the
  // reader has to guess the length of.
  function overviewPreviousRange() {
    const range = (cache.overview || {}).previous && (cache.overview || {}).previous.range;
    if (!range || !range.from) return uiText("上期＝紧邻的同长度区间", "Previous = the immediately preceding window of the same length");
    return uiText("上期 " + String(range.from).slice(0, 10) + " – " + String(range.to || "").slice(0, 10), "Previous " + String(range.from).slice(0, 10) + " – " + String(range.to || "").slice(0, 10));
  }
  function overviewBlock(title, aside, body, extraClass) {
    return '<section class="elevated-card card-pad stats-card ' + (extraClass || "") + '"><header class="fl-head"><h3>' + esc(title) + '</h3><span class="fl-aside">' + aside + "</span></header>" + body + "</section>";
  }
  function overviewNotReady(what) {
    return '<div class="empty-copy"><strong>' + esc(uiText("总览尚未返回" + what + "。", "The overview does not return " + what + " yet.")) + '</strong><span>' + uiText("接口就绪前这里不用其它接口拼凑替代数字。", "No substitute numbers are assembled from other interfaces until the interface is ready.") + "</span></div>";
  }
  // The friction mechanism dictionary, the matched rule and every caliber note
  // are written by the daemon, and the page prints them verbatim as evidence
  // rather than paraphrasing. A9 gives each one an English wording beside the
  // Chinese, so the English page reads that. A record written before that field
  // existed still has only the Chinese sentence; it is printed as it stands and
  // flagged, so a reader does not take it for a missed translation.
  const HAN_TEXT = /[一-鿿]/;
  const daemonProse = (zh, en) => view.locale === "en" && en ? String(en) : (zh == null ? "" : String(zh));
  const daemonCopyFlag = (text) => view.locale === "en" && HAN_TEXT.test(String(text == null ? "" : text))
    ? '<span class="fl-daemon-copy-flag" title="The daemon has no English wording for this explanation yet; the page shows its Chinese verbatim as evidence rather than paraphrasing it.">zh</span>'
    : "";
  const daemonSentence = (zh, en, tag, cls) => {
    const text = daemonProse(zh, en);
    if (!text) return "";
    return "<" + tag + (cls ? ' class="' + cls + '"' : "") + '><span data-no-translate="true">' + esc(text) + "</span>" + daemonCopyFlag(text) + "</" + tag + ">";
  };
  // The daemon states the caliber of each block in its own `note`. Printing
  // that note is how the page stays honest without restating the rule here.
  function overviewCaliber(note, noteEN) {
    return daemonSentence(note, noteEN, "p", "evidence-note");
  }
  // Peak concurrency: how many sessions overlapped at the busiest moment in
  // this range, and when. Only sessions with both a start and an end are in the
  // denominator; the ones still open are counted separately.
  function overviewParallelism(data) {
    const parallelism = data && data.parallelism;
    if (!parallelism) return overviewBlock(uiText("并行度", "Parallelism"), uiText("接口未就绪", "Interface not ready"), overviewNotReady(uiText("并行度", "parallelism")));
    const peak = num(parallelism.peak);
    const at = parallelism.peak_at || null;
    const day = at ? String(at).slice(0, 10) : "";
    const value = peak == null ? count(peak) : uiText("最多同时 " + peak + " 个", "Up to " + peak + " at once");
    const denominator = overviewRatio(parallelism.sessions_considered, (data.sessions || {}).in_range, "个会话有完整起止时间", "sessions have both a start and an end");
    const unbounded = num(parallelism.unbounded_sessions);
    const peakCell = '<b data-no-translate="true">' + esc(value) + "</b><small>" + esc(at ? date(at) : uiText("峰值时间未记录", "Peak time not recorded")) + "</small>";
    const body = '<div class="overview-parallelism">' + (day
      ? '<a class="overview-parallelism-peak" href="' + esc("#/sessions?from=" + encodeURIComponent(day) + "&to=" + encodeURIComponent(day)) + '">' + peakCell + "</a>"
      : '<span class="overview-parallelism-peak">' + peakCell + "</span>")
      + '<span class="overview-compare">' + esc(denominator + (unbounded ? uiText(" · 另有 " + unbounded + " 个会话没有结束时间，不计入", " · " + unbounded + " sessions have no end time and are excluded") : "")) + "</span>"
      + overviewCaliber(parallelism.note, parallelism.note_en) + "</div>";
    return overviewBlock(uiText("并行度", "Parallelism"), compareAside("parallelism.peak", "parallel_peak"), body);
  }
  // The "no command name could be parsed" bucket is one row among real command
  // names. A7 renames its key from __unrecorded__ to __unparsed__; both keys
  // are read here so the page says the same thing before and after that
  // change. An empty key means the same thing and is treated the same way.
  const UNPARSED_COMMAND_KEYS = ["__unparsed__", "__unrecorded__", "unrecorded"];
  const isUnparsedCommand = (value) => !value || UNPARSED_COMMAND_KEYS.includes(value);
  // Environment health: commands the shell could not find, and the commands
  // that fail most often. The failure rate's denominator is known_outcomes, so
  // a call whose result was never recorded is neither a success nor a failure.
  function overviewEnvironment(data) {
    const environment = data && data.environment;
    if (!environment) return overviewBlock(uiText("环境健康", "Environment health"), uiText("接口未就绪", "Interface not ready"), overviewNotReady(uiText("环境健康", "environment health")), "wide");
    const missing = Array.isArray(environment.missing_commands) ? environment.missing_commands : [];
    const failing = Array.isArray(environment.failing_programs) ? environment.failing_programs : [];
    const threshold = num(environment.min_known_outcomes);
    // The unparsed bucket is not a command, so it never competes for a rank.
    // It is held out of the ranking, appended after the named commands, and
    // drawn muted; the eight-row cut applies to the named commands only, so
    // sinking the bucket can never be what hides it.
    const namedMissing = missing.filter((entry) => !isUnparsedCommand(entry && entry.command));
    const unparsedMissing = missing.filter((entry) => isUnparsedCommand(entry && entry.command));
    const missingRows = namedMissing.slice(0, 8).concat(unparsedMissing)
      .map((entry) => {
        const unparsed = isUnparsedCommand(entry.command);
        const label = unparsed ? uiText("未解析出命令名", "Command name not parsed") : entry.command;
        return '<a class="overview-list-row" data-muted="' + unparsed + '" href="' + esc(overviewRangeHref("#/friction", "category=command_not_found&q=" + encodeURIComponent(unparsed ? "" : entry.command))) + '"><span' + (unparsed ? "" : ' data-no-translate="true"') + ">" + esc(label) + '</span><span class="overview-list-aside">' + esc([quantity(entry.sessions, "个会话", "session", "sessions"), shortDate(entry.last_at)].join(" · ")) + "</span><strong>" + esc(count(entry.count)) + "</strong></a>";
      }).join("");
    const failingRows = failing.slice(0, 8).map((entry) => '<a class="overview-list-row" href="' + esc(overviewRangeHref("#/sessions", "program=" + encodeURIComponent(entry.program || ""))) + '"><span data-no-translate="true">' + esc(entry.program || uiText("程序未记录", "Program not recorded")) + '</span><span class="overview-list-aside">' + esc(failureText(entry)) + "</span><strong>" + esc(num(entry.rate) == null ? count(entry.rate) : Math.round(entry.rate * 100) + "%") + "</strong></a>").join("");
    const failingLabel = threshold == null
      ? uiText("失败率最高的命令", "Commands with the highest failure rate")
      : uiText("失败率最高的命令（已记录结果 ≥ " + threshold + " 次）", "Commands with the highest failure rate (" + threshold + " or more recorded outcomes)");
    const body = '<div class="overview-pair-inner"><div class="overview-subblock"><div class="session-inspector-label">' + uiText("找不到的命令", "Commands not found") + '</div><div class="overview-list">' + (missingRows || '<div class="empty-copy"><strong>' + uiText("没有记录到找不到的命令。", "No missing command was recorded.") + '</strong></div>') + '</div></div><div class="overview-subblock"><div class="session-inspector-label">' + esc(failingLabel) + '</div><div class="overview-list">' + (failingRows || '<div class="empty-copy"><strong>' + uiText("没有命令达到已记录结果的门槛。", "No command reached the recorded-outcome threshold.") + "</strong></div>") + "</div></div></div>" + overviewCaliber(environment.note, environment.note_en);
    return overviewBlock(uiText("环境健康", "Environment health"), compareAside("environment.missing_commands", "missing_commands"), body, "wide");
  }
  // ADR-20: the cost-and-opportunity block. Each item is a closed-kind
  // projection the daemon computes from facts already in the database; the
  // criterion sentence comes from the daemon and is printed verbatim.
  function overviewInsights() {
    const payload = cache.insights;
    if (!payload) return "";
    const items = Array.isArray(payload.insights) ? payload.insights : [];
    if (!items.length) return "";
    const icons = {
      interrupts: "circle-slash", zero_edit_heavy: "wallet", stuck_loops: "history",
      reread: "book-open", coverage_gaps: "scale", missing_commands: "slash"
    };
    const rowsFor = (item) => {
      const facts = item.facts || {};
      const rows = [];
      if (item.kind === "interrupts") {
        if (num(facts.turn_measured) > 0) {
          rows.push({
            label: uiText("被中断轮次已投入", "Already spent by interrupted turns"),
            aside: uiText("可测 " + count(facts.turn_measured) + " 次（消息级记录了 token 的来源）", count(facts.turn_measured) + " measurable (sources with per-message tokens)"),
            strong: tokenText(facts.turn_tokens_total),
            href: overviewRangeHref("#/friction", "kind=user_interrupt")
          });
        }
        (facts.top_projects || []).forEach((entry) => rows.push({
          label: entry.key === "__unrecorded__" ? uiText("项目未记录", "Project not recorded") : entry.key,
          aside: num(entry.turn_tokens) > 0 ? uiText("被中断轮次 ", "interrupted turns ") + tokenText(entry.turn_tokens) : "",
          strong: quantity(entry.count, "次", "time", "times"),
          href: overviewRangeHref("#/friction", "kind=user_interrupt&project=" + encodeURIComponent(entry.key === "__unrecorded__" ? "__unrecorded__" : entry.key))
        }));
        (facts.last_tools || []).forEach((entry) => rows.push({
          label: uiText("中断前工具 ", "Tool before interrupt ") + entry.key,
          aside: "",
          strong: quantity(entry.count, "次", "time", "times"),
          href: overviewRangeHref("#/friction", "kind=user_interrupt&q=" + encodeURIComponent(entry.key))
        }));
      } else if (item.kind === "zero_edit_heavy") {
        (facts.top || []).forEach((entry) => rows.push({
          label: entry.title || uiText("未命名会话", "Untitled session"),
          aside: [tokenText(entry.total_tokens), durationText(entry.duration_ms)].filter(Boolean).join(" · "),
          strong: tokenText(entry.total_tokens),
          href: "#/sessions/" + encodeURIComponent(entry.session_id)
        }));
      } else if (item.kind === "stuck_loops") {
        (facts.top || []).forEach((entry) => rows.push({
          label: frictionSignatureLine(entry.signature),
          aside: [entry.project_key, num(entry.turn_tokens) > 0 ? tokenText(entry.turn_tokens) : ""].filter(Boolean).join(" · "),
          strong: quantity(entry.count, "次", "times", "times"),
          href: overviewRangeHref("#/friction", "signature=" + encodeURIComponent(entry.signature || ""))
        }));
      } else if (item.kind === "reread") {
        (facts.top_files || []).forEach((entry) => rows.push({
          label: entry.path,
          aside: quantity(entry.sessions, "个会话", "session", "sessions"),
          strong: quantity(entry.reads, "次读取", "reads", "reads"),
          href: overviewRangeHref("#/sessions", "file=" + encodeURIComponent(entry.path || ""))
        }));
      } else if (item.kind === "coverage_gaps") {
        (facts.gaps || []).forEach((entry) => rows.push({
          label: frictionSignatureLine(entry.signature),
          aside: [entry.project_key, entry.mechanism].filter(Boolean).join(" · "),
          strong: quantity(entry.session_count, "个会话", "sessions", "sessions"),
          href: overviewRangeHref("#/friction", "signature=" + encodeURIComponent(entry.signature || ""))
        }));
      } else if (item.kind === "missing_commands") {
        (facts.commands || []).forEach((entry) => rows.push({
          label: entry.command,
          aside: quantity(entry.sessions, "个会话", "session", "sessions"),
          strong: String(entry.count),
          href: overviewRangeHref("#/friction", "category=command_not_found&q=" + encodeURIComponent(entry.command === "__unparsed__" ? "" : entry.command))
        }));
      }
      return rows.slice(0, 5).map((row) => '<a class="overview-list-row insight-row" href="' + esc(row.href) + '"><span data-no-translate="true" title="' + esc(row.label) + '">' + esc(row.label) + '</span><span class="overview-list-aside">' + esc(row.aside || "") + "</span><strong>" + esc(row.strong) + "</strong></a>").join("");
    };
    const body = items.map((item) => '<div class="overview-subblock insight-item" data-kind="' + esc(item.kind) + '">'
      + '<div class="insight-head">' + icon(icons[item.kind] || "activity", "insight-icon") + '<span class="insight-title">' + esc(item.title) + '</span><span class="insight-summary">' + esc(item.summary) + "</span></div>"
      + '<div class="overview-list">' + rowsFor(item) + "</div>"
      + '<p class="insight-criterion" title="' + esc(item.criterion_en || "") + '">' + esc(uiText("判定规则：", "Rule: ") + item.criterion) + "</p>"
      + "</div>").join("");
    const scope = payload.scope && payload.scope.note ? daemonSentence(payload.scope.note, payload.scope.note_en, "p", "evidence-note") : "";
    const watches = payload.watches || {};
    let aside = "";
    if (num(watches.watching) + num(watches.verified) + num(watches.no_change) + num(watches.unobservable) > 0) {
      const parts = [];
      if (num(watches.verified) > 0) parts.push(uiText(count(watches.verified) + " 条修复有效", count(watches.verified) + " verified"));
      if (num(watches.no_change) > 0) parts.push(uiText(count(watches.no_change) + " 条未见改善", count(watches.no_change) + " no change"));
      if (num(watches.watching) > 0) parts.push(uiText(count(watches.watching) + " 条验证中", count(watches.watching) + " watching"));
      if (num(watches.unobservable) > 0) parts.push(uiText(count(watches.unobservable) + " 条无法判断", count(watches.unobservable) + " unobservable"));
      aside = '<a class="us-btn" data-variant="ghost" data-size="sm" href="' + esc(overviewRangeHref("#/friction", "group=signature")) + '">' + icon("history") + esc(parts.join(uiText(" · ", " · "))) + "</a>";
    }
    return overviewBlock(uiText("代价与机会", "Costs and opportunities"), aside, '<div class="overview-pair-inner insight-grid">' + body + "</div>" + scope, "wide");
  }
  // Subagent use: how many sessions dispatched subagents, how many each, which
  // roles, and what share of the friction those subagent sessions carry.
  function overviewSubagents(data) {
    const subagents = data && data.subagents;
    if (!subagents) return overviewBlock(uiText("子代理使用", "Subagent use"), uiText("接口未就绪", "Interface not ready"), overviewNotReady(uiText("子代理使用", "subagent use")), "wide");
    const dispatched = num(subagents.sessions_with_subagents);
    const roles = Array.isArray(subagents.by_role) ? subagents.by_role : [];
    const roleRows = roles.slice(0, 8).map((entry) => '<a class="overview-list-row" href="' + esc(overviewRangeHref("#/sessions", "thread=subagent&role=" + encodeURIComponent(entry.key === "__unrecorded__" ? "" : entry.key || ""))) + '"><span data-no-translate="true">' + esc(facetLabel(entry.key, uiText("角色未记录", "Role not recorded"))) + "</span><strong>" + esc(count(entry.count)) + "</strong></a>").join("");
    const average = num(subagents.avg_per_session);
    const share = subagents.friction_share || {};
    const metrics = '<div class="overview-metric-grid">'
      + overviewMetric(uiText("派出子代理的会话", "Sessions that dispatched subagents"), count(dispatched), overviewRatio(dispatched, (data.sessions || {}).in_range, "个主会话", "main sessions"), "git-commit-horizontal", "", "", overviewRangeHref("#/sessions"))
      + overviewMetric(uiText("子代理会话", "Subagent sessions"), count(subagents.subagent_sessions), average == null ? count(average) : uiText("平均每个派出会话 " + average.toFixed(1) + " 个", average.toFixed(1) + " per dispatching session"), "layers", "", "", overviewRangeHref("#/sessions", "thread=subagent"))
      + overviewMetric(uiText("子代理会话的摩擦占比", "Friction share of subagent sessions"), overviewRatio(share.numerator, share.denominator, "条摩擦", "friction records"), uiText("分子是子代理会话的摩擦，分母是范围内全部摩擦", "Numerator is friction in subagent sessions, denominator is all friction in range"), "triangle-alert")
      + "</div>";
    const body = metrics + '<div class="session-inspector-label">' + uiText("角色分布（只统计子代理自己的角色）", "Role split (subagent roles only)") + '</div><div class="overview-list">' + (roleRows || '<div class="empty-copy"><strong>' + uiText("没有记录到子代理角色。", "No subagent role was recorded.") + "</strong></div>") + "</div>" + overviewCaliber(subagents.note, subagents.note_en);
    return overviewBlock(uiText("子代理使用", "Subagent use"), compareAside("subagents.sessions_with_subagents", "sessions_with_subagents"), body, "wide");
  }
  // Re-reading: the same path read three or more times inside one session. It
  // is a count of recorded reads, not a judgement about them.
  function overviewReread(data) {
    const reread = data && data.reread;
    if (!reread) return overviewBlock(uiText("反复读取", "Re-reading"), uiText("接口未就绪", "Interface not ready"), overviewNotReady(uiText("反复读取", "re-reading")));
    const sessions = num(reread.sessions);
    const threshold = num(reread.threshold);
    const body = '<div class="overview-metric-grid">'
      + overviewMetric(uiText("有反复读取的会话", "Sessions with re-reading"), count(sessions), overviewRatio(sessions, (data.sessions || {}).in_range, "个会话", "sessions"), "history")
      + overviewMetric(uiText("反复读取次数", "Re-read count"), count(reread.reads), threshold == null ? count(threshold) : uiText("同一会话内同一路径读 ≥ " + threshold + " 次才计入", "Counted when one path is read " + threshold + " or more times inside one session"), "file-text")
      + "</div>" + overviewCaliber(reread.note, reread.note_en);
    return overviewBlock(uiText("反复读取", "Re-reading"), compareAside("reread.sessions", "reread_sessions"), body);
  }
  // Every number that has a filter behind it is a link to that filter, so the
  // reader can go from the summary to the records it was computed from.
  function overviewMetric(label, value, detail, iconName, tone, compare, href, title) {
    const missing = value === (view.locale === "en" ? "Not recorded" : "未记录");
    const open = href ? "a" : "div";
    return "<" + open + ' class="overview-metric" data-wide-value="' + (String(value).length > 12) + '" data-missing="' + missing + '"' + (href ? ' href="' + esc(href) + '"' : "") + (title ? ' title="' + esc(title) + '"' : "") + '><span class="stat-label"><span class="stat-icon" data-tone="' + esc(tone || "muted") + '">' + icon(iconName || "package") + "</span><span>" + esc(label) + '</span></span><span class="fl-metric"><span class="overview-value-stack"><b data-tone="' + esc(tone || "") + '">' + esc(value) + "</b>" + (compare || "") + "</span><small>" + esc(detail) + "</small></span></" + open + ">";
  }
  // The range the overview is showing, as session-page query parameters.
  function overviewRangeHref(path, extra) {
    const range = overviewRangeParams();
    const params = new URLSearchParams(extra || "");
    if (path.indexOf("#/friction") === 0 && range.mode !== "custom") {
      if (range.mode === "all") params.set("from", "all");
      else params.set("range", range.mode + "d");
    } else {
      if (range.mode === "all") params.set("from", "all");
      else if (range.from) params.set("from", range.from);
      if (range.to) params.set("to", range.to);
    }
    const text = params.toString();
    return path + (text ? "?" + text : "");
  }
  function overviewRatio(numerator, denominator, zhUnit, enUnit) {
    if (num(numerator) == null || num(denominator) == null) return uiText("分子 / 分母未记录", "Numerator / denominator not recorded");
    return numerator + " / " + denominator + " " + uiText(zhUnit, enUnit);
  }
  // /overview returns scope as an object, and scope.key is the one word naming
  // the scope in force. Comparing the object itself to "all" is never true, so
  // every reader goes through here.
  const overviewScopeIsAll = (data) => Boolean(data && data.scope && data.scope.key === "all");
  // The overview counts main, non-empty sessions unless include=all, and the
  // file projection drops scratch directories; both facts are stated.
  function overviewScopeNote(data) {
    const sessions = data.sessions || {};
    const scratch = num(data.scratch_files);
    const scope = overviewScopeIsAll(data)
      ? uiText("以上数字统计全部会话。", "These numbers count every session.")
      : uiText("以上数字仅统计主会话、不含空会话；子代理会话 " + count(sessions.subagent) + " 个、空会话 " + count(sessions.empty) + " 个另计。", "These numbers count main sessions only and exclude empty sessions; " + count(sessions.subagent) + " subagent sessions and " + count(sessions.empty) + " empty sessions are counted separately.");
    return scope + (scratch == null ? "" : uiText(" 热点文件已排除 " + scratch + " 个临时目录文件。", " Hot files exclude " + scratch + " files in scratch directories."));
  }
  // The scope used to be a paragraph under the numbers. It is the caliber of
  // every number above it, so it became a badge on the heading; the full
  // sentence stays available on hover and to screen readers.
  function overviewScopeBadge(data) {
    const label = overviewScopeIsAll(data) ? uiText("全部会话", "All sessions") : uiText("主会话 · 非空", "Main · non-empty");
    return '<span class="fl-scope-badge" title="' + esc(overviewScopeNote(data)) + '">' + icon("circle-slash") + "<span>" + esc(label) + '</span><span class="sr-only">' + esc(overviewScopeNote(data)) + "</span></span>";
  }
  function overviewMetrics(data) {
    const sessions = data.sessions || {};
    const projects = data.projects || {};
    const friction = data.friction || {};
    const duration = data.duration || {};
    const totalMs = num(duration.total_ms);
    const usage = data.usage || {};
    // overview-kpi-card is the size container the KPI grid measures itself
    // against, so the column count follows the card, not the viewport.
    return '<section class="elevated-card stats-card wide overview-kpi-card"><header class="fl-head overview-kpi-head"><h3>' + uiText("本期 vs 上期", "This period vs the previous one") + '</h3><span class="fl-aside">' + overviewScopeBadge(data) + '<span class="overview-compare-note">' + esc(overviewPreviousRange()) + "</span></span></header>" + '<div class="overview-metric-grid">'
      + overviewMetric(uiText("会话", "Sessions"), count(sessions.in_range), overviewRatio(sessions.in_range, sessions.total, "个（全部）", "of all"), "layers", "", compareAside("sessions", "sessions"), overviewRangeHref("#/sessions"))
      + overviewMetric(uiText("活跃项目", "Active projects"), count(projects.in_range), overviewRatio(projects.in_range, projects.total, "个（全部）", "of all"), "folder", "", "", overviewRangeHref("#/sessions", "group=project"))
      + overviewMetric(uiText("工具调用", "Tool calls"), count(data.tool_calls), uiText("已记录事件 " + count(data.events), "Recorded events " + count(data.events)), "cpu", "", compareAside("tool_calls", "tool_calls"), overviewRangeHref("#/sessions", "sort=tool_calls"))
      + overviewMetric(uiText("摩擦事件", "Friction records"), count(friction.total), overviewRatio(friction.sessions_with_friction, sessions.in_range, "个会话有摩擦", "sessions with friction"), "triangle-alert", friction.total ? "warn" : "", compareAside("friction", "friction"), overviewRangeHref("#/friction"))
      + overviewMetric(uiText("已记录时长", "Recorded duration"), totalMs == null ? count(totalMs) : durationText(totalMs), overviewRatio(duration.known_sessions, sessions.in_range, "个会话已记录时长", "sessions with a recorded duration"), "clock", "", compareAside("duration_ms", "duration_ms", durationText), overviewRangeHref("#/sessions", "sort=duration"))
      + usageMetrics(usage, sessions.in_range, true)
      + "</div></section>";
  }
  // §20.4: the aggregate carries its own denominator. token_sessions is how
  // many sessions in range actually recorded a token count, so a gap reads as
  // a gap rather than pulling the total down toward zero.
  function usageMetrics(usage, inRange, compare) {
    if (!usage || !Object.keys(usage).length) {
      return overviewMetric(uiText("token", "Tokens"), count(null), uiText("度量接口未就绪", "The measurement interface is not ready"), "cpu")
        + overviewMetric(uiText("改动行", "Changed lines"), count(null), uiText("度量接口未就绪", "The measurement interface is not ready"), "file-diff");
    }
    const denominator = num(usage.in_range) != null ? usage.in_range : inRange;
    // ADR-25: work tokens lead. Cache reads are ~98% of the local total, so
    // the total alone overstates what a window cost by tens of times; it stays
    // visible one line below, with its cache-read share, never hidden.
    const totalLine = num(usage.total_tokens) == null
      ? overviewRatio(usage.token_sessions, denominator, "个会话记录了 token", "sessions recorded a token count")
      : uiText("总 " + tokenText(usage.total_tokens) + "（缓存读取 " + tokenText(usage.cached_input_tokens) + "）· " + count(usage.token_sessions) + "/" + count(denominator) + " 个会话记录",
          "Total " + tokenText(usage.total_tokens) + " (" + tokenText(usage.cached_input_tokens) + " cache reads) · " + count(usage.token_sessions) + "/" + count(denominator) + " sessions recorded");
    return overviewMetric(uiText("工作 token", "Work tokens"), tokenText(usage.work_tokens), totalLine, "cpu", "", compare ? compareAside("work_tokens", "work_tokens", tokenText) : "", compare ? overviewRangeHref("#/sessions", "sort=tokens") : "", daemonProse(usage.work_definition, usage.work_definition_en) || tokenTitle())
      + overviewMetric(uiText("改动行", "Changed lines"), linesChangedText(usage.lines_added, usage.lines_removed), overviewRatio(usage.known_sessions, denominator, "个会话记录了度量", "sessions carry a measurement"), "file-diff", "", compare ? compareAside("lines_added", "lines_added") : "", compare ? overviewRangeHref("#/sessions", "sort=lines_changed") : "");
  }
  // by_model splits the same tokens by the model that spent them. It is a
  // distribution, not a ranking: no model is called better than another.
  //
  // The bar is a log10 scale, not a proportion. On this machine the spread runs
  // from 354K to 4.6B — four orders of magnitude — and a linear bar draws every
  // small model as an empty sliver. One hairline per power of ten is drawn
  // inside each bar (from --decades, so the ticks cannot drift out of alignment
  // the way a separate axis row would) and the block says so, because on a log
  // bar twice the length means ten times the tokens; the number beside each bar
  // is the real value and the only thing that can be compared by ratio.
  function overviewModelUsage(data) {
    const models = Array.isArray(data && data.by_model) ? data.by_model : null;
    if (!models) {
      return '<section class="elevated-card card-pad stats-card"><header class="fl-head"><h3>' + uiText("按模型的 token", "Tokens by model") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + uiText("这个接口尚未返回按模型的度量。", "This interface does not return a per-model measurement yet.") + "</strong></div></section>";
    }
    const measured = models.map((entry) => num(entry.total_tokens)).filter((value) => value != null && value > 0);
    const top = measured.length ? Math.ceil(Math.log10(Math.max.apply(null, measured))) : 1;
    const floor = measured.length ? Math.floor(Math.log10(Math.min.apply(null, measured))) : 0;
    const decades = Math.max(1, top - floor);
    const widthOf = (value) => {
      const amount = num(value);
      if (amount == null || amount <= 0) return 0;
      return Math.max(2, Math.min(100, (Math.log10(amount) - floor) / decades * 100));
    };
    const rows = models.map((entry) => {
      const measurement = num(entry.total_tokens);
      return '<a class="overview-model-row" data-key="model:' + esc(entry.model) + '" href="' + esc(overviewRangeHref("#/sessions", "model=" + encodeURIComponent(entry.model || ""))) + '" title="' + esc(tokenTitle()) + '"><span data-no-translate="true">' + esc(entry.model || uiText("模型未记录", "Model not recorded")) + '</span><span class="overview-model-bar" data-empty="' + (measurement == null) + '"><i style="--w:' + widthOf(measurement).toFixed(2) + '%"></i></span><span class="overview-model-aside">' + esc(uiText(count(entry.sessions) + " 个会话 · " + count(entry.turns) + " 轮", count(entry.sessions) + " sessions · " + count(entry.turns) + " turns")) + '</span><b data-no-translate="true">' + esc(tokenText(entry.total_tokens)) + "</b></a>";
    }).join("");
    const scale = measured.length
      ? '<p class="evidence-note overview-model-scale">' + esc(uiText(
        "刻度：条形按对数刻度，每一格 ×10，从 " + tokenText(Math.pow(10, floor)) + " 到 " + tokenText(Math.pow(10, top)) + "。条长看的是数量级，不是比例——比例只能看右边的数字。",
        "Scale: the bars are log10 — each tick is ten times the last, from " + tokenText(Math.pow(10, floor)) + " to " + tokenText(Math.pow(10, top)) + ". A bar shows the order of magnitude, not a proportion; only the number on the right can be compared as a ratio.")) + "</p>"
      : "";
    return '<section class="elevated-card card-pad stats-card"><header class="fl-head"><h3>' + uiText("按模型的 token", "Tokens by model") + '</h3><span class="fl-aside">' + uiText("条形为对数刻度 · 点击按模型筛选会话", "Bars are on a log scale · click to filter sessions by model") + '</span></header><div class="overview-list overview-model-list" style="--decades:' + decades + '">' + (rows || '<div class="empty-copy"><strong>' + uiText("没有记录到按模型的 token。", "No per-model token count was recorded.") + "</strong></div>") + "</div>" + scale + "</section>";
  }
  function overviewProjects(data) {
    const projects = Array.isArray(data.top_projects) ? data.top_projects : [];
    const rows = projects.map((project) => {
      const harnesses = Object.entries(project.harnesses || {}).map(([key, value]) => source(key) + " " + count(value)).join(" · ") || uiText("harness 未记录", "Harness not recorded");
      return '<a class="overview-table-row" href="' + esc(overviewRangeHref("#/sessions", "project=" + encodeURIComponent(project.key))) + '"><span class="overview-project-name">' + esc(projectLabelOf(project)) + '</span><span class="overview-project-harness">' + esc(harnesses) + '</span><span>' + esc(count(project.sessions)) + '</span><span data-tone="warn">' + esc(count(project.friction_count)) + '</span><span>' + esc(shortDate(project.last_started_at)) + "</span></a>";
    }).join("");
    const head = '<div class="overview-table-head"><span>' + uiText("项目", "Project") + '</span><span>' + uiText("harness 分布", "Harness split") + '</span><span>' + uiText("会话", "Sessions") + '</span><span>' + uiText("摩擦", "Friction") + '</span><span>' + uiText("最近活动", "Last activity") + "</span></div>";
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>项目</h3><span class="fl-aside">' + esc(quantity(projects.length, "个项目", "project", "projects")) + '</span></header><div class="overview-table">' + (rows ? head + rows : '<div class="empty-copy"><strong>' + uiText("当前时间范围内没有记录到项目。", "No project was recorded in this time range.") + "</strong></div>") + "</div></section>";
  }
  function overviewRecent(data) {
    const sessions = Array.isArray(data.recent_sessions) ? data.recent_sessions : [];
    const rows = sessions.map((item) => {
      const friction = num(item.friction_count);
      return '<a class="overview-session-row" href="#/sessions/' + encodeURIComponent(item.id) + '">' + sourceMark(item.source) + '<span class="overview-session-main">' + sessionTitleHTML(item) + '<span class="overview-session-meta">' + esc((item.project_label || uiText("项目未记录", "Project not recorded")) + " · " + shortDate(item.started_at)) + '</span></span>' + (friction ? '<span class="session-item-friction" data-static="true">' + icon("triangle-alert") + "<span>" + friction + "</span></span>" : "") + "</a>";
    }).join("");
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>最近会话</h3><span class="fl-aside"><a href="' + esc(overviewRangeHref("#/sessions")) + '">' + icon("chevron-right") + uiText("全部会话", "All sessions") + '</a></span></header><div class="overview-session-list">' + (rows || '<div class="empty-copy"><strong>' + uiText("当前时间范围内没有会话记录。", "No session was recorded in this time range.") + "</strong></div>") + "</div></section>";
  }
  function overviewAssets(data) {
    const assets = data.assets || {};
    const attention = num(assets.attention);
    return '<section class="elevated-card card-pad stats-card"><header class="fl-head"><h3>资产关注</h3><span class="fl-aside"><a href="#/assets">' + icon("chevron-right") + uiText("资产墙", "Asset wall") + '</a></span></header><div class="overview-assets"><span class="fl-metric"><b data-tone="' + (attention ? "bad" : "") + '">' + esc(count(assets.attention)) + '</b><small>' + uiText("不再被使用 / 引用失效 / 调用后未遵循", "No longer used / broken reference / called but not followed") + '</small></span><span class="fl-metric"><b>' + esc(count(assets.total)) + '</b><small>' + uiText("已记录资产", "Recorded assets") + "</small></span></div></section>";
  }
  // Hour × weekday, converted with the browser's own offset so the grid reads
  // in the user's local time rather than UTC. Weekday 0 is Monday.
  function workHoursHeatmap() {
    const data = view.overviewTime;
    if (!data) {
      return '<div class="empty-copy"><strong>' + uiText("工作时段接口未就绪。", "The work-hours interface is not ready.") + '</strong><span data-no-translate="true">' + esc(view.overviewTimeError) + "</span></div>";
    }
    const grid = Array.isArray(data.hour_weekday) ? data.hour_weekday : [];
    if (grid.length !== 7) {
      return '<div class="empty-copy"><strong>' + uiText("小时 × 星期分布未记录。", "The hour × weekday distribution is not recorded.") + "</strong></div>";
    }
    let maximum = 1;
    grid.forEach((row) => (Array.isArray(row) ? row : []).forEach((value) => { if (num(value) != null) maximum = Math.max(maximum, value); }));
    const dayNames = view.locale === "en" ? ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"] : ["一", "二", "三", "四", "五", "六", "日"];
    const rows = grid.map((row, dayIndex) => {
      const cells = Array.from({ length: 24 }, (_, hour) => {
        const value = num(Array.isArray(row) ? row[hour] : null);
        const level = value == null ? 0 : value === 0 ? 0 : Math.max(1, Math.ceil(value / maximum * 4));
        const label = dayNames[dayIndex] + " " + String(hour).padStart(2, "0") + ":00 · " + (value == null ? uiText("未记录", "Not recorded") : quantity(value, "个会话", "session", "sessions"));
        return '<i class="heat-cell" data-level="' + level + '" data-missing="' + (value == null) + '" title="' + esc(label) + '"></i>';
      }).join("");
      return '<div class="workhours-row"><span class="workhours-day">' + esc(dayNames[dayIndex]) + "</span>" + cells + "</div>";
    }).join("");
    const hourLabels = '<div class="workhours-row workhours-axis"><span class="workhours-day"></span>' + Array.from({ length: 24 }, (_, hour) => '<span class="workhours-hour">' + (hour % 3 === 0 ? hour : "") + "</span>").join("") + "</div>";
    const offset = num(data.tz_offset_minutes) == null ? timezoneOffsetMinutes() : data.tz_offset_minutes;
    const note = '<p class="evidence-note">' + uiText("按浏览器时区偏移 " + offset + " 分钟换算；只统计主会话且非空会话。", "Converted with the browser time-zone offset of " + offset + " minutes; only main, non-empty sessions are counted.") + "</p>";
    return '<div class="workhours-grid">' + rows + hourLabels + "</div>" + note;
  }
  // Friction lifecycle, three columns (§13.9). Each column is one status, and
  // each row carries the evidence the status was decided from: the normalized
  // sample, its category, the mechanism, how many sessions it appeared in and
  // when it was last recorded. The "gone quiet" column additionally states how
  // many sessions those projects ran inside the selected range, because
  // "quiet" only means "not recorded in this range" — a project that ran
  // nothing is quiet too.
  function frictionLifecycleRow(group, status, preserveOverviewRange) {
    const sessions = num(group.session_count);
    const sessionText = sessions == null ? uiText("会话数未记录", "Session count not recorded") : uiText("出现在 " + sessions + " 个会话", "Seen in " + sessions + " sessions");
    const quiet = status === "quiet"
      ? (num(group.project_sessions_last_7d) == null
        ? '<small class="friction-quiet-note" data-missing="true">' + esc(uiText("当前筛选范围内的同项目会话数未记录", "Sessions in the same projects in the selected range are not recorded")) + "</small>"
        : '<small class="friction-quiet-note">' + esc(uiText("当前筛选范围内同项目 " + group.project_sessions_last_7d + " 个会话", group.project_sessions_last_7d + " sessions in the same projects in the selected range")) + "</small>")
      : "";
    const href = preserveOverviewRange
      ? overviewRangeHref("#/friction", "group=signature&signature=" + encodeURIComponent(group.signature || ""))
      : "#/friction?group=signature&signature=" + encodeURIComponent(group.signature || "");
    return '<a class="lifecycle-row" data-key="lc:' + esc(status) + ":" + esc(group.signature || "") + '" href="' + esc(href) + '">'
      + '<span class="lifecycle-sample" data-no-translate="true">' + esc(frictionSignatureSample(group)) + '</span>'
      + frictionCategoryBadge(group.category)
      + frictionHintLine(group)
      + '<span class="lifecycle-meta">' + esc(sessionText) + " · " + esc(shortDate(group.last_occurred_at)) + "</span>"
      + quiet + "</a>";
  }
  function frictionLifecycleColumn(lifecycle, status, preserveOverviewRange) {
    const key = "top_" + status;
    const items = Array.isArray(lifecycle[key]) ? lifecycle[key] : [];
    const total = num(lifecycle[status]);
    const rows = items.map((group) => frictionLifecycleRow(group, status, preserveOverviewRange)).join("");
    const empty = status === "new" ? uiText("当前筛选范围内没有新出现的摩擦签名。", "No friction signature first appeared inside the selected range.")
      : status === "active" ? uiText("当前筛选范围内没有仍在发生的摩擦签名。", "No friction signature is still happening inside the selected range.")
      : uiText("没有记录到已消失的摩擦签名。", "No friction signature has gone quiet.");
    return '<section class="lifecycle-column" data-status="' + esc(status) + '"><header class="lifecycle-column-head">' + frictionStatusBadge(status)
      + '<strong>' + esc(count(total)) + '</strong><small>' + esc(uiText("个签名", "signatures")) + '</small></header>'
      + '<div class="lifecycle-list">' + (rows || '<div class="empty-copy lifecycle-empty"><strong>' + esc(empty) + "</strong></div>") + "</div></section>";
  }
  function frictionLifecycleCard(lifecycle, notReadyText, preserveOverviewRange) {
    if (!lifecycle) {
      return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("摩擦", "Friction") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + esc(notReadyText) + "</strong></div></section>";
    }
    const aside = uiText("只出现过一次 " + count(lifecycle.once) + " 个签名未列入", count(lifecycle.once) + " signatures seen once are not listed");
    const note = uiText(
      "新出现＝首次记录落在当前筛选范围内；仍在发生＝当前范围内还有记录且首次记录更早；已消失＝当前范围内没有记录但历史上出现在 2 个及以上会话。已消失不等于已修复：同一批项目在当前范围内本来就没跑会话时它也会安静，所以每条一并给出同项目在当前范围内的会话数。",
      "New = the first record falls inside the selected range; still happening = there are records inside the selected range and the first record is older; gone quiet = no record inside the selected range but the signature appeared in two or more sessions historically. Gone quiet is not fixed: a signature also goes quiet when those projects ran no sessions in the selected range, so every row states how many sessions they ran.");
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("摩擦", "Friction") + '</h3><span class="fl-aside">' + esc(aside) + '</span></header><div class="lifecycle-grid">'
      + frictionLifecycleColumn(lifecycle, "new", preserveOverviewRange)
      + frictionLifecycleColumn(lifecycle, "active", preserveOverviewRange)
      + frictionLifecycleColumn(lifecycle, "quiet", preserveOverviewRange)
      + '</div><p class="evidence-note lifecycle-note">' + esc(note) + "</p></section>";
  }
  // One disclosure, not seven blocks. Everything here answers a second
  // question — how much ran at once, which model spent the tokens, what the
  // subagents did, what was read over and over, when the work happened — so it
  // is closed until asked for. The open state is remembered per browser in
  // localStorage; nothing about it is sent anywhere.
  const OVERVIEW_MORE_KEY = "flatline-overview-more";
  function overviewMore(data, range) {
    const open = Boolean(view.overviewMoreOpen);
    const heatmapAside = uiText(
      "每格一天 · 值为会话数 · 所选时间范围以外的日期显示未记录",
      "One cell per day · value is session count · days outside the selected range show as not recorded"
    );
    const body = open
      ? '<div class="overview-more-body stats-grid overview-grid">'
        + '<div class="overview-pair">' + overviewParallelism(data) + overviewModelUsage(data) + "</div>"
        + overviewSubagents(data)
        + overviewReread(data)
        + '<section class="elevated-card stats-card wide"><header class="fl-head"><h3>' + uiText("活动", "Activity") + '</h3><span class="fl-aside">' + esc(heatmapAside) + "</span></header>" + activityHeatmap(data) + "</section>"
        + '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("工作时段", "Work hours") + '</h3><span class="fl-aside">' + uiText("小时 × 星期 · 按浏览器时区", "Hour × weekday · in the browser time zone") + "</span></header>" + workHoursHeatmap() + "</section>"
        + '<div class="overview-pair">' + overviewAssets(data) + "</div>"
        + overviewRecent(data)
        + "</div>"
      : "";
    const summary = uiText("并行度 · 按模型 token · 子代理 · 反复读取 · 活动 · 工作时段 · 资产关注 · 最近会话",
      "Parallelism · tokens by model · subagents · re-reading · activity · work hours · asset attention · recent sessions");
    return '<section class="elevated-card card-pad stats-card wide overview-more" data-open="' + open + '"><button type="button" class="overview-more-head" data-action="overview-more" aria-expanded="' + open + '">'
      + icon(open ? "chevron-down" : "chevron-right")
      + '<span class="overview-more-title">' + uiText("更多", "More") + "</span>"
      + '<span class="fl-aside">' + esc(summary) + "</span></button>" + body + "</section>";
  }
  // overviewNow is the monitor's first screen (P16-3): the sessions whose
  // transcripts are being written right now. Silence is the normal state — a
  // quiet machine renders no block at all rather than an empty card.
  function overviewNow(now) {
    if (!now || !Array.isArray(now.sessions) || !now.sessions.length) return "";
    const rows = now.sessions.slice(0, 8).map((item) => {
      const usage = item.usage || {};
      const work = num(usage.input_tokens) == null && num(usage.output_tokens) == null && num(usage.cache_write_tokens) == null
        ? null
        : (num(usage.input_tokens) || 0) + (num(usage.output_tokens) || 0) + (num(usage.cache_write_tokens) || 0);
      const name = item.display_title || uiText("标题未记录", "Title not recorded");
      const fleet = item.live_children > 0
        ? '<span class="fl-flag" data-flag="new">' + esc(uiText(item.live_children + " 个子代理在写", item.live_children + " subagents writing")) + "</span>"
        : "";
      const role = item.thread_kind === "subagent"
        ? '<span class="session-fleet-role" data-no-translate="true">' + esc(item.agent_role || uiText("角色未记录", "Role not recorded")) + "</span>"
        : "";
      const frictionAside = num(item.friction_count) != null && item.friction_count > 0
        ? quantity(item.friction_count, "条摩擦", "friction", "friction")
        : "";
      // The loop badge is the strongest sentence a live view can say: the
      // daemon counted one signature recurring inside its stated window. The
      // sample line rides in the tooltip, the claim stays count-and-window.
      const loop = item.loop
        ? '<span class="fl-flag overview-now-loop" data-flag="warn" title="' + esc((item.loop.sample_line || item.loop.signature || "") + " · " + uiText("最近 60 分钟", "last 60 minutes")) + '">' + esc(uiText("同一失败 ×" + item.loop.count, "Same failure ×" + item.loop.count)) + "</span>"
        : "";
      return '<a class="overview-list-row overview-now-row" href="#/sessions/' + encodeURIComponent(item.id) + '">'
        + '<span class="overview-now-pulse" aria-hidden="true"></span>' + role
        + '<span class="session-fleet-name" data-no-translate="true" title="' + esc(name) + '">' + esc(name) + "</span>" + fleet + loop
        + '<span class="overview-list-aside">' + esc([item.project_label || "", frictionAside].filter(Boolean).join(" · ")) + "</span>"
        + "<strong data-no-translate=\"true\">" + esc(work == null ? uiText("token 未记录", "Tokens not recorded") : tokenText(work) + uiText(" 工作 token", " work tokens")) + "</strong></a>";
    }).join("");
    const more = now.sessions.length > 8
      ? '<p class="evidence-note">' + esc(uiText("另有 " + (now.sessions.length - 8) + " 个进行中的会话未列出。", (now.sessions.length - 8) + " more in-progress sessions are not listed.")) + "</p>"
      : "";
    return '<section class="elevated-card overview-now-card"><header class="fl-head"><h3>' + uiText("正在进行", "Happening now") + '</h3><span class="fl-aside">' + esc(quantity(now.count, "个会话在写", "session writing", "sessions writing")) + '</span></header>'
      + '<div class="overview-list">' + rows + "</div>" + more
      + '<p class="friction-method-note">' + esc(daemonProse(now.note, now.note_en)) + "</p></section>";
  }
  function drawOverview() {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const range = overviewRange();
    const rangeOptions = [
      { value: "7", label: uiText("近 7 天", "7 days") },
      { value: "30", label: uiText("近 30 天", "30 days") },
      { value: "90", label: uiText("近 90 天", "90 days") },
      { value: "all", label: uiText("全部", "All") }
    ];
    const custom = overviewRangeParams();
    const effective = cache.overview && cache.overview.range ? cache.overview.range : {};
    const customRange = dateRangeControl("overview-range",
      custom.mode === "custom" ? custom.from : (effective.from || ""),
      custom.mode === "custom" ? custom.to : (effective.to || ""),
      uiText("自定义时间范围", "Custom date range"));
    const headerRight = segmentControl("overview-range", "range", rangeOptions.concat(custom.mode === "custom" ? [{ value: "custom", label: uiText("自定义", "Custom") }] : []), range) + customRange + '<a class="us-btn" data-variant="outline" data-size="sm" href="#/stats">' + icon("chart-column") + uiText("数据", "Data") + "</a>";
    if (!view.overviewReady) {
      setScreen(header("总览", uiText("这段时间在哪些项目、摩擦集中在哪", "Which projects, and where friction concentrates"), headerRight) + screenContent('<section class="elevated-card card-pad"><div class="empty-copy"><strong>' + uiText("总览接口未就绪。", "The overview interface is not ready.") + '</strong><span>' + uiText("daemon 尚未提供 /api/v1/overview；这里不用其它接口拼凑替代数字。", "The daemon does not yet serve /api/v1/overview; no substitute numbers are assembled from other interfaces here.") + '</span><p class="empty-copy-detail">' + esc(view.overviewError) + '</p><button class="us-btn" data-variant="outline" data-size="sm" data-action="reload-overview">' + icon("refreshCw") + uiText("重试", "Retry") + "</button></div></section>", "prototype-page"));
      localizeDOM();
      return;
    }
    const data = cache.overview || {};
    const rangeLabel = data.range && data.range.from ? esc(String(data.range.from).slice(0, 10)) + " – " + esc(data.range.to ? String(data.range.to).slice(0, 10) : uiText("现在", "now")) : uiText("时间范围未记录", "Time range not recorded");
    // B5: the overview had grown to sixteen blocks and 4400px, which is a wall,
    // not a summary. Four blocks answer "what happened in this window" and stay
    // on the first screen: the KPIs against the previous window, the friction
    // lifecycle, environment health, and the projects. Everything that answers
    // a second question is behind one disclosure. Five blocks were removed
    // outright rather than folded, because each already exists on the page that
    // owns it — friction hotspots by tool and by category, and recurring
    // friction, on the friction page; common tags, top commands and hot files
    // on the project page. Two places for one number is how the two disagree.
    const body = '<div class="stats-grid overview-grid">' + overviewNow(cache.now)
      + overviewMetrics(data)
      + overviewInsights()
      + frictionLifecycleCard(data.friction_lifecycle, uiText("总览尚未返回摩擦生命周期。", "The overview does not return the friction lifecycle yet."), true)
      + overviewEnvironment(data)
      + overviewProjects(data)
      + overviewMore(data, range)
      + "</div>";
    setScreen(header("总览", uiText("当前范围 · ", "Selected range · ") + rangeLabel, headerRight) + screenContent(body, "prototype-page"));
    localizeDOM();
  }

  async function loadProjectPage(key, params) {
    const state = view.projectPage;
    const from = params.get("from") || "";
    const to = params.get("to") || "";
    const cacheKey = key + "\x1f" + from + "\x1f" + to;
    if (state.key === cacheKey && state.data) return;
    state.key = cacheKey;
    state.data = null;
    state.error = "";
    const query = new URLSearchParams();
    if (from) query.set("from", from);
    if (to) query.set("to", to);
    try {
      state.data = await get(withParams("/api/v1/projects/" + encodeURIComponent(key), query));
      noteUsageDefinition(state.data && state.data.usage);
    } catch (error) {
      state.data = null;
      state.error = error.message || String(error);
    }
  }
  function distributionCard(title, aside, items, labelOf, hrefOf, emptyText) {
    const list = Array.isArray(items) ? items : [];
    const total = list.reduce((sum, entry) => sum + (num(entry.count) || 0), 0);
    const rows = list.map((entry) => {
      const label = labelOf(entry);
      const value = num(entry.count);
      const width = value != null && total > 0 ? Math.max(0, Math.min(100, value / total * 100)) : 0;
      const inner = '<span class="distribution-name"><span data-no-translate="true">' + esc(label) + '</span></span><span class="distribution-bar" data-empty="' + (value == null) + '"><i data-tone="accent" style="--w:' + width.toFixed(1) + '%"></i></span><strong>' + esc(count(entry.count)) + "</strong>";
      const href = hrefOf ? hrefOf(entry) : "";
      return href ? '<a class="distribution-row" href="' + esc(href) + '">' + inner + "</a>" : '<div class="distribution-row">' + inner + "</div>";
    }).join("");
    return '<section class="elevated-card card-pad stats-card"><header class="fl-head"><h3>' + esc(title) + '</h3><span class="fl-aside">' + esc(aside) + '</span></header><div class="fl-list stats-fl-list">' + (rows || '<div class="empty-copy"><strong>' + esc(emptyText) + "</strong></div>") + "</div></section>";
  }
  const PROJECT_WEEK_MIN_BARS = 3;
  const projectWeekValueText = (week, metric, value) => metric === "duration"
    ? (num(week.duration_ms) == null ? uiText("未记录", "Not recorded") : compactDuration(new Date(0).toISOString(), new Date(week.duration_ms).toISOString()))
    : count(value);
  const projectWeekTitle = (week) => [week.week, quantity(week.sessions, "个会话", "session", "sessions"), quantity(week.tool_calls, "次工具调用", "tool call", "tool calls"), quantity(week.friction, "条摩擦", "friction record", "friction records")].join(" · ");
  function projectWeekSummary(list, valueOf, metric) {
    const rows = list.map((week) => '<div class="fl-li" title="' + esc(projectWeekTitle(week)) + '"><span data-no-translate="true">' + esc(week.week) + '</span><b data-no-translate="true">' + esc(projectWeekValueText(week, metric, valueOf(week))) + "</b></div>").join("");
    const heading = uiText(list.length + " 周 · 不足 " + PROJECT_WEEK_MIN_BARS + " 个周桶不画柱状图", list.length + (list.length === 1 ? " week" : " weeks") + " · under " + PROJECT_WEEK_MIN_BARS + " buckets, no bar chart is drawn");
    return '<p class="project-week-summary">' + esc(heading) + '</p><div class="fl-list stats-fl-list project-week-table">' + rows + "</div>";
  }
  // Three metrics over the same weeks; each bar carries its own numerator in
  // the tooltip so the chart is never the only place a number exists.
  function projectWeekChart(weeks) {
    const list = Array.isArray(weeks) ? weeks : [];
    if (!list.length) return '<div class="empty-copy"><strong>' + uiText("没有记录到按周的活动。", "No weekly activity was recorded.") + "</strong></div>";
    const metric = view.projectPage.metric;
    const valueOf = (week) => metric === "duration" ? num(week.duration_ms) : metric === "friction" ? num(week.friction) : num(week.sessions);
    // One or two buckets have no shape to plot: a lone bar sits at the far left
    // with nine tenths of the plot empty, and the start/end labels print the
    // same date twice. Under three buckets the same numbers go in a table.
    if (list.length < PROJECT_WEEK_MIN_BARS) return projectWeekSummary(list, valueOf, metric);
    const maximum = list.reduce((value, week) => Math.max(value, valueOf(week) || 0), 1);
    const bars = list.map((week) => {
      const value = valueOf(week);
      const height = value == null ? 0 : Math.max(2, value / maximum * 100);
      return '<span class="project-week-bar" data-missing="' + (value == null) + '" title="' + esc(projectWeekTitle(week)) + '"><i style="--h:' + height.toFixed(1) + '%"></i><small data-no-translate="true">' + esc(projectWeekValueText(week, metric, value)) + "</small></span>";
    }).join("");
    const from = list[0].week;
    const to = list[list.length - 1].week;
    const legend = from === to
      ? '<span data-no-translate="true">' + esc(from) + "</span>"
      : '<span data-no-translate="true">' + esc(from) + '</span><span data-no-translate="true">' + esc(to) + "</span>";
    return '<div class="project-week-plot">' + bars + '</div><div class="project-week-legend" data-single="' + (from === to) + '">' + legend + "</div>";
  }
  // The aggregate names a friction dimension either explicitly (category /
  // tool_name) or generically (key); accept both rather than printing blanks.
  function frictionDimension(entry, field) {
    if (!entry) return "";
    if (entry[field] != null && entry[field] !== "") return entry[field];
    return entry.key == null ? "" : entry.key;
  }
  function projectFrictionCard(data) {
    const friction = data.friction || {};
    const recurring = Array.isArray(friction.recurring) ? friction.recurring : [];
    const rows = recurring.map((group) => '<a class="overview-list-row lifecycle-recurring-row" href="#/friction?group=signature&signature=' + encodeURIComponent(group.signature || "") + '"><span class="lifecycle-recurring-main">' + frictionStatusBadge(group.status) + '<span data-no-translate="true">' + esc(frictionSignatureSample(group)) + '</span>' + frictionHintLine(group) + '</span><span class="overview-list-aside">' + esc(num(group.session_count) == null ? uiText("会话数未记录", "Session count not recorded") : uiText("出现在 " + group.session_count + " 个会话", "Seen in " + group.session_count + " sessions")) + '</span><strong>' + esc(count(group.count)) + "</strong></a>").join("");
    return frictionLifecycleCard(friction.lifecycle, uiText("项目页尚未返回摩擦生命周期。", "The project page does not return the friction lifecycle yet."))
      + '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("摩擦分布", "Friction split") + '</h3><span class="fl-aside">' + esc(count(friction.total)) + '</span></header>'
      + frictionHintKindRow(friction)
      + '<div class="overview-pair">'
      + distributionCard(uiText("按类别", "By category"), uiText("点击进入摩擦页", "Click to open the friction page"), friction.by_category || [], (entry) => frictionCategoryLabel(frictionDimension(entry, "category")), (entry) => "#/friction?category=" + encodeURIComponent(frictionDimension(entry, "category") || FRICTION_UNCLASSIFIED), uiText("摩擦类别未记录。", "Friction categories are not recorded."))
      + distributionCard(uiText("按工具", "By tool"), uiText("点击进入摩擦页", "Click to open the friction page"), friction.by_tool || [], (entry) => frictionToolLabel(frictionDimension(entry, "tool_name") === "__unrecorded__" ? "" : frictionDimension(entry, "tool_name")), (entry) => "#/friction?tool=" + encodeURIComponent(frictionDimension(entry, "tool_name") || "__unrecorded__"), uiText("没有记录到按工具的摩擦。", "No friction by tool was recorded."))
      + '</div><div class="overview-list project-recurring">' + (rows || '<div class="empty-copy"><strong>' + uiText("没有记录到反复出现的摩擦签名。", "No recurring friction signature was recorded.") + "</strong></div>") + "</div></section>";
  }
  function hotFilesAside(data) {
    const outside = num(data.outside_project_files);
    return outside == null
      ? uiText("点击按文件筛选会话", "Click to filter sessions by file")
      : uiText("点击按文件筛选会话 · 另有 " + outside + " 个项目外文件未计入", "Click to filter sessions by file · " + outside + " files outside the project are excluded");
  }
  function projectFilesCard(data) {
    const files = Array.isArray(data.hot_files) ? data.hot_files : [];
    const rows = files.map((entry) => '<a class="file-row" data-key="file:' + esc(entry.path || "") + '" href="#/sessions?file=' + encodeURIComponent(entry.path || "") + '"><span class="file-path" data-no-translate="true">' + esc(entry.path || uiText("路径未记录", "Path not recorded")) + '</span><span class="file-counts"><b>' + esc(count(entry.reads)) + '</b><small>' + uiText("读", "Read") + '</small></span><span class="file-counts"><b>' + esc(count(entry.edits)) + '</b><small>' + uiText("改", "Edit") + '</small></span><span class="file-counts"><b>' + esc(count(entry.writes)) + '</b><small>' + uiText("写", "Write") + '</small></span><span class="file-counts"><b>' + esc(count(entry.deletes)) + '</b><small>' + uiText("删", "Delete") + '</small></span><time>' + esc(shortDate(entry.last_at)) + "</time></a>").join("");
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("热点文件", "Hot files") + '</h3><span class="fl-aside">' + esc(hotFilesAside(data)) + '</span></header><div class="file-table">' + (rows || '<div class="empty-copy"><strong>' + uiText("没有记录到文件读写。", "No file read or write was recorded.") + "</strong></div>") + "</div></section>";
  }
  function projectProgramsCard(data) {
    const programs = Array.isArray(data.top_programs) ? data.top_programs : [];
    const rows = programs.map((entry) => '<a class="program-row" data-key="program:' + esc(entry.program || "") + '" href="#/sessions?program=' + encodeURIComponent(entry.program || "") + '"><span class="command-program" data-no-translate="true">' + esc(entry.program || uiText("程序未记录", "Program not recorded")) + '</span><span class="program-counts"><b>' + esc(count(entry.calls)) + '</b><small>' + uiText("调用", "Calls") + '</small></span><span class="program-counts"><b>' + esc(count(entry.sessions)) + '</b><small>' + uiText("会话", "Sessions") + '</small></span><span class="program-outcome" data-tone="' + (num(entry.failures) ? "bad" : "muted") + '">' + esc(failureText(entry)) + "</span></a>").join("");
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("常用命令", "Top commands") + '</h3><span class="fl-aside">' + uiText("失败来自退出码非零或 is_error；未记录不计入", "Failures come from a non-zero exit code or is_error; not recorded is not counted") + '</span></header><div class="program-table">' + (rows || '<div class="empty-copy"><strong>' + uiText("没有记录到命令。", "No command was recorded.") + "</strong></div>") + "</div></section>";
  }
  function drawProjectPage(key) {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const state = view.projectPage;
    const allSessionsHref = "#/sessions?project=" + encodeURIComponent(key);
    if (!state.data) {
      setScreen(header(uiText("项目", "Project"), esc(key), '<a class="us-btn" data-variant="outline" data-size="sm" href="' + esc(allSessionsHref) + '">' + uiText("查看全部会话", "View all sessions") + "</a>")
        + screenContent('<section class="elevated-card card-pad"><div class="empty-copy"><strong>' + uiText("项目页接口未就绪。", "The project page interface is not ready.") + '</strong><span>' + uiText("daemon 尚未提供 /api/v1/projects/{key}；这里不用其它接口拼凑替代数字。", "The daemon does not yet serve /api/v1/projects/{key}; no substitute numbers are assembled from other interfaces here.") + '</span><p class="empty-copy-detail" data-no-translate="true">' + esc(state.error) + "</p></div></section>", "prototype-page"));
      localizeDOM();
      return;
    }
    const data = state.data;
    const project = data.project || {};
    const sessions = data.sessions || {};
    const duration = data.duration || {};
    const totalMs = num(duration.total_ms);
    // harness is which tool wrote the transcript; originator is which program
    // inside that tool started the session. They used to run together in one
    // string, where "Claude Code" appeared twice meaning two different things.
    const harnesses = Object.entries(project.harnesses || {}).sort((a, b) => b[1] - a[1]).map(([name, value]) => source(name) + " " + count(value)).join(" · ") || uiText("harness 未记录", "Harness not recorded");
    const originators = Object.entries(project.originators || {}).sort((a, b) => b[1] - a[1]).map(([name, value]) => originatorLabel(name) + " " + count(value)).join(" · ") || uiText("发起方未记录", "Originator not recorded");
    const range = data.range && data.range.from
      ? esc(String(data.range.from).slice(0, 10)) + " – " + esc(data.range.to ? String(data.range.to).slice(0, 10) : uiText("现在", "now"))
      : data.range ? uiText("全部时间", "All time") : uiText("时间范围未记录", "Time range not recorded");
    const identity = '<section class="elevated-card card-pad stats-card wide"><div class="project-identity"><strong data-no-translate="true">' + esc(project.cwd || projectLabelOf(project)) + '</strong>' + homeDirBadge(project)
      + '<span class="project-identity-line"><span class="project-identity-label">' + uiText("harness：", "Harness: ") + '</span><span data-no-translate="true">' + esc(harnesses) + '</span></span>'
      + '<span class="project-identity-line"><span class="project-identity-label">' + uiText("发起方：", "Originator: ") + '</span><span data-no-translate="true">' + esc(originators) + '</span></span>'
      + '<span>' + esc([shortDate(project.first_started_at), shortDate(project.last_started_at)].join(" – ")) + "</span></div></section>";
    // The project page carries the same seven KPIs as the overview, so it needs
    // the same size container; without it the row lands on six tracks and
    // strands the seventh.
    const metrics = '<section class="elevated-card stats-card wide overview-kpi-card"><div class="overview-metric-grid">'
      + overviewMetric(uiText("主会话", "Main sessions"), count(sessions.main), uiText("thread_kind=main", "thread_kind=main"), "layers")
      + overviewMetric(uiText("子代理会话", "Subagent sessions"), count(sessions.subagent), uiText("thread_kind=subagent", "thread_kind=subagent"), "git-commit-horizontal")
      + overviewMetric(uiText("空会话", "Empty sessions"), count(sessions.empty), uiText("无用户消息且无工具调用", "No user message and no tool call"), "circle-slash")
      + overviewMetric(uiText("时间范围内会话", "Sessions in range"), count(sessions.in_range), esc(range), "calendar")
      + overviewMetric(uiText("已记录时长", "Recorded duration"), totalMs == null ? count(totalMs) : durationText(totalMs), overviewRatio(duration.known_sessions, sessions.in_range, "个会话已记录时长", "sessions with a recorded duration"), "clock")
      + usageMetrics(data.usage, sessions.in_range)
      + "</div></section>";
    const metricOptions = [
      { value: "sessions", label: uiText("会话数", "Sessions") },
      { value: "duration", label: uiText("已记录时长", "Recorded duration") },
      { value: "friction", label: uiText("摩擦", "Friction") }
    ];
    const trend = '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("周趋势", "Weekly trend") + '</h3><span class="fl-aside">' + segmentControl("project-metric", "metric", metricOptions, state.metric) + "</span></header>" + projectWeekChart(data.by_week) + "</section>";
    const distributions = '<div class="overview-pair">'
      + distributionCard(uiText("模型", "Models"), uiText("按会话计数", "Counted per session"), data.models, (entry) => facetLabel(entry.key, uiText("模型未记录", "Model not recorded")), (entry) => "#/sessions?project=" + encodeURIComponent(key) + "&model=" + encodeURIComponent(entry.key || ""), uiText("没有记录到模型。", "No model was recorded."))
      + distributionCard(uiText("子代理角色", "Subagent roles"), uiText("按会话计数", "Counted per session"), data.roles, (entry) => facetLabel(entry.key, uiText("角色未记录", "Role not recorded")), (entry) => "#/sessions?project=" + encodeURIComponent(key) + "&thread=subagent&role=" + encodeURIComponent(entry.key || ""), uiText("没有记录到子代理角色。", "No subagent role was recorded."))
      + "</div>";
    const tags = distributionCard(uiText("标签", "Tags"), uiText("规则标签与用户标签", "Rule tags and user tags"), (data.tags || []).map((entry) => Object.assign({}, entry, { key: entry.tag })), (entry) => entry.tag, (entry) => "#/sessions?project=" + encodeURIComponent(key) + "&tag=" + encodeURIComponent(entry.tag || ""), uiText("没有记录到标签。", "No tag was recorded."));
    const assets = data.assets || {};
    const assetCard = '<section class="elevated-card card-pad stats-card"><header class="fl-head"><h3>' + uiText("资产关注", "Asset attention") + '</h3><span class="fl-aside"><a href="#/assets">' + icon("chevron-right") + uiText("资产墙", "Asset wall") + '</a></span></header><div class="overview-assets"><span class="fl-metric"><b data-tone="' + (num(assets.attention) ? "bad" : "") + '">' + esc(count(assets.attention)) + '</b><small>' + uiText("需要注意", "Need attention") + '</small></span><span class="fl-metric"><b>' + esc(count(assets.total)) + '</b><small>' + uiText("已记录资产", "Recorded assets") + "</small></span></div></section>";
    const recent = overviewRecent({ recent_sessions: data.recent_sessions });
    const body = '<div class="stats-grid overview-grid">' + identity + metrics + trend + distributions + '<div class="overview-pair">' + overviewModelUsage(data) + tags + "</div>" + '<div class="overview-pair">' + assetCard + "</div>" + projectFrictionCard(data) + projectFilesCard(data) + projectProgramsCard(data) + recent + "</div>";
    const headerRight = '<a class="us-btn" data-variant="outline" data-size="sm" href="' + esc(allSessionsHref) + '">' + icon("layers") + uiText("查看全部会话", "View all sessions") + "</a>";
    setScreen(header(projectLabelOf(project), homeDirBadge(project) + esc(range) + " · " + esc(quantity(project.sessions, "个会话", "session", "sessions")), headerRight) + screenContent(body, "prototype-page"));
    localizeDOM();
  }

  const FRICTION_RANGE_DAYS = { "7d": 7, "30d": 30, "90d": 90 };
  // The lifecycle window (§13.9): how far back "still happening" reaches. The
  // response echoes it back as window_days, and every "last N days" label on
  // the page reads from that echo, not from this constant.
  const FRICTION_WINDOWS = [7, 14, 30];
  const FRICTION_DEFAULT_WINDOW = 7;
  // §13.9: with group=signature and sort=sessions the daemon already orders
  // "still happening" first. The page takes that order as given and never
  // re-sorts it in the browser.
  const frictionDefaultSort = (groupBy) => groupBy === "signature" ? "sessions" : "count";

  function frictionRangeFrom() {
    if (view.frictionRange === "custom") return view.frictionFrom || "";
    const days = FRICTION_RANGE_DAYS[view.frictionRange];
    if (!days) return "";
    return new Date(Date.now() - days * 86400000).toISOString();
  }
  function frictionRangeTo() {
    return view.frictionRange === "custom" ? view.frictionTo || "" : "";
  }

  function frictionFilterParams() {
    const params = new URLSearchParams();
    if (view.frictionProjectFilter !== "all") params.set("project", view.frictionProjectFilter);
    if (view.frictionHarnessFilter !== "all") params.set("harness", view.frictionHarnessFilter);
    if (view.frictionKindFilter !== "all") params.set("kind", view.frictionKindFilter);
    if (view.frictionCategoryFilter !== "all") params.set("category", view.frictionCategoryFilter);
    if (view.frictionToolFilter !== "all") params.set("tool", view.frictionToolFilter);
    if (view.frictionSignatureFilter !== "all") params.set("signature", view.frictionSignatureFilter);
    if (view.frictionRange !== "all" && view.frictionRange !== "custom") params.set("range", view.frictionRange);
    if (view.frictionRange === "custom") {
      if (view.frictionFrom) params.set("from", view.frictionFrom);
      if (view.frictionTo) params.set("to", view.frictionTo);
    }
    if (num(view.frictionWindow) !== FRICTION_DEFAULT_WINDOW) params.set("window", String(view.frictionWindow));
    if (view.frictionQuery.trim()) params.set("q", view.frictionQuery.trim());
    if (view.frictionSort !== frictionDefaultSort(view.frictionGroupBy)) params.set("sort", view.frictionSort);
    if (view.frictionGroupBy !== "project") params.set("group", view.frictionGroupBy);
    return params;
  }

  // Filters live in the hash so a reload, a bookmark or a link from the
  // overview page restores the same query.
  function applyFrictionHash() {
    const query = location.hash.includes("?") ? location.hash.slice(location.hash.indexOf("?") + 1) : "";
    const params = new URLSearchParams(query);
    const read = (key, fallback) => params.get(key) || fallback;
    view.frictionProjectFilter = read("project", "all");
    view.frictionHarnessFilter = read("harness", "all");
    view.frictionKindFilter = read("kind", "all");
    view.frictionCategoryFilter = read("category", "all");
    view.frictionToolFilter = read("tool", "all");
    view.frictionSignatureFilter = read("signature", "all");
    const from = params.get("from") || "";
    const to = params.get("to") || "";
    const presetFrom = !to && FRICTION_RANGE_DAYS[from] ? from : "";
    if (from === "all" && !to) {
      view.frictionRange = "all";
      view.frictionFrom = "";
      view.frictionTo = "";
    } else if (presetFrom) {
      view.frictionRange = presetFrom;
      view.frictionFrom = "";
      view.frictionTo = "";
    } else if (from || to) {
      view.frictionRange = "custom";
      view.frictionFrom = from;
      view.frictionTo = to;
    } else {
      view.frictionRange = FRICTION_RANGE_DAYS[params.get("range")] ? params.get("range") : "all";
      view.frictionFrom = "";
      view.frictionTo = "";
    }
    view.frictionWindow = FRICTION_WINDOWS.indexOf(Number(params.get("window"))) >= 0 ? Number(params.get("window")) : FRICTION_DEFAULT_WINDOW;
    view.frictionQuery = read("q", "");
    view.frictionGroupBy = ["project", "category", "tool", "signature"].indexOf(read("group", "project")) >= 0 ? read("group", "project") : "project";
    const defaultSort = frictionDefaultSort(view.frictionGroupBy);
    view.frictionSort = ["count", "recent", "sessions"].indexOf(read("sort", defaultSort)) >= 0 ? read("sort", defaultSort) : defaultSort;
  }

  function frictionHashKey() {
    return frictionRoutePath() + "?" + frictionFilterParams().toString();
  }

  function frictionRoutePath() {
    return location.hash.split("?")[0] || "#/friction";
  }

  function writeFrictionHash() {
    const next = frictionHashKey();
    if (location.hash !== next) history.replaceState(null, "", next);
  }

  function reloadFriction() {
    writeFrictionHash();
    if (frictionRoutePath().startsWith("#/friction/")) {
      const group = currentFrictionGroup();
      if (group) loadFrictionDetail(group, true).catch(renderError);
      return;
    }
    loadFrictionOverview(true).catch(renderError);
  }

  function frictionQueryPath(detail, group, offset) {
    const params = frictionFilterParams();
    params.delete("range");
    const from = frictionRangeFrom();
    const to = frictionRangeTo();
    params.delete("from");
    params.delete("to");
    params.set("from", from || "all");
    if (to) params.set("to", to);
    if (detail) {
      params.delete("group");
      params.set("view", "detail");
      params.set("project", group.project || group.project_key || "");
      params.set("harness", group.harness);
    }
    params.set("sort", view.frictionSort);
    if (view.frictionRange === "all") params.set("window", "all");
    else if (num(view.frictionWindow) !== FRICTION_DEFAULT_WINDOW) params.set("window", String(view.frictionWindow));
    params.set("limit", String(FRICTION_ROW_CHUNK));
    params.set("offset", String(offset || 0));
    return "/api/v1/friction?" + params.toString();
  }

  function currentFrictionGroup() {
    const prefix = "#/friction/";
    const path = frictionRoutePath();
    if (!path.startsWith(prefix)) return null;
    const parts = path.slice(prefix.length).split("/");
    if (parts.length < 2 || !parts[0] || !parts[1]) return null;
    try {
      return { project: decodeURIComponent(parts[0]), harness: decodeURIComponent(parts[1]) };
    } catch (_) {
      return null;
    }
  }

  // One source list for every harness filter: the facet when the endpoint
  // returns one, otherwise the store's own per-source session counts.
  function harnessOptions(facet) {
    const list = Array.isArray(facet) ? facet.map((entry) => ({ key: entry.key || entry.harness, count: num(entry.count) })) : [];
    if (list.length) return list.filter((entry) => entry.key).map((entry) => ({ value: entry.key, label: source(entry.key) + (entry.count == null ? "" : " · " + count(entry.count)) }));
    const totals = sourceTotals();
    return Object.keys(totals).sort((a, b) => (num(totals[b]) || 0) - (num(totals[a]) || 0) || a.localeCompare(b))
      .map((key) => ({ value: key, label: source(key) + " · " + count(totals[key]) }));
  }
  function frictionProjectOptionLabel(option) {
    if (!option) return view.locale === "en" ? "Project not recorded" : "项目未记录";
    if (option.key === "__unrecorded__") return view.locale === "en" ? "Project not recorded" : "项目未记录";
    return option.label || option.key;
  }

  function frictionGroupProjectLabel(group) {
    const projectKey = group && (group.project_key || group.project);
    if (!projectKey || projectKey === "__unrecorded__") return view.locale === "en" ? "Project not recorded" : "项目未记录";
    return group.project_label || frictionProjectLabel(projectKey);
  }

  function frictionSummaryValue(value) {
    return num(value) == null ? (view.locale === "en" ? "Not recorded" : "未记录") : String(value);
  }

  function frictionStatCard(label, value, iconName, tone) {
    return '<div class="fl-kpi" data-tone="' + esc(tone || "muted") + '"><span class="fl-kpi-label">' + icon(iconName) + "<span>" + esc(label) + '</span></span><strong>' + esc(frictionSummaryValue(value)) + "</strong></div>";
  }

  function frictionTypeChips(record, skipCategory) {
    const kinds = Array.isArray(record && record.friction_kinds) ? record.friction_kinds : [];
    const categoryLabel = skipCategory ? frictionCategoryLabel(record && record.category) : "";
    return (kinds.length ? kinds : [record && record.friction_kind || ""])
      .filter(Boolean)
      .filter((kindValue) => frictionKindLabel(kindValue) !== categoryLabel)
      .map((kindValue) => '<span class="friction-kind-chip" data-kind="' + esc(kindValue) + '">' + icon(frictionKindIcons[kindValue] || "triangle-alert") + '<span>' + esc(frictionKindLabel(kindValue)) + '</span></span>').join("");
  }

  function frictionCategoryBadge(value) {
    const key = frictionCategoryKey(value);
    return '<span class="friction-category-badge" data-tone="' + esc(frictionCategoryTones[key] || "muted") + '">' + icon(frictionCategoryIcons[key] || "circleHelp") + '<span>' + esc(frictionCategoryLabel(value)) + '</span></span>';
  }

  // Lifecycle (§13.9): four statuses decided in order, so they are exhaustive
  // and mutually exclusive. "quiet" is not "fixed" — the row must also say how
  // many sessions those projects ran in the window, or the reader assumes it.
  const FRICTION_STATUS_TONES = { new: "accent", active: "bad", quiet: "muted", once: "muted" };
  const frictionStatusLabels = { new: "新出现", active: "仍在发生", quiet: "已消失", once: "只出现过一次" };
  const enFrictionStatusLabels = { new: "New", active: "Still happening", quiet: "Gone quiet", once: "Seen once" };
  function frictionStatusLabel(value) {
    return localized(frictionStatusLabels, enFrictionStatusLabels, value, view.locale === "en" ? "Status not recorded" : "状态未记录");
  }
  function frictionStatusBadge(value) {
    if (!value) return '<span class="friction-status-badge" data-tone="muted" data-missing="true">' + uiText("状态未记录", "Status not recorded") + "</span>";
    return '<span class="friction-status-badge" data-status="' + esc(value) + '" data-tone="' + esc(FRICTION_STATUS_TONES[value] || "muted") + '">' + esc(frictionStatusLabel(value)) + "</span>";
  }
  const hintKindLabels = {
    environment: "环境", harness_rule: "harness 规则", user_hook: "用户 hook", tool_misuse: "工具用法",
    permission: "权限", timeout: "超时", test: "测试", build: "构建",
    user_interrupt: "用户中断", __unrecorded__: "字典未覆盖"
  };
  const enHintKindLabels = {
    environment: "Environment", harness_rule: "Harness rule", user_hook: "User hook", tool_misuse: "Tool misuse",
    permission: "Permission", timeout: "Timeout", test: "Test", build: "Build",
    user_interrupt: "User interrupt", __unrecorded__: "Not in the dictionary"
  };
  function hintKindLabel(value) {
    return localized(hintKindLabels, enHintKindLabels, value, view.locale === "en" ? "Not in the dictionary" : "字典未覆盖");
  }
  function frictionHintBadge(hint) {
    if (!hint || !hint.kind) return "";
    return '<span class="friction-hint-badge" data-kind="' + esc(hint.kind) + '">' + esc(hintKindLabel(hint.kind)) + "</span>";
  }
  // hint.mechanism states what mechanism produced the line. It is not advice
  // and not a cause; a null hint means the dictionary does not cover this
  // signature, not that there is no mechanism.
  // An uncovered signature prints nothing: the dictionary's coverage is stated
  // once in the table header, and repeating "not covered" under every row was
  // noise, not information.
  function frictionHintLine(group) {
    const hint = group && group.hint;
    if (!hint || !hint.mechanism) return "";
    const mechanism = daemonProse(hint.mechanism, hint.mechanism_en);
    return '<small class="friction-hint-line">' + frictionHintBadge(hint) + '<span data-no-translate="true">' + esc(mechanism) + "</span>" + daemonCopyFlag(mechanism) + "</small>";
  }
  // by_hint_kind counts signatures per mechanism kind, with __unrecorded__ for
  // the ones the dictionary does not cover. Coverage is that split read as one
  // ratio: how many signatures the dictionary has a mechanism for.
  function frictionHintCoverage(summary) {
    const items = Array.isArray(summary && summary.by_hint_kind) ? summary.by_hint_kind : null;
    if (!items || !items.length) return null;
    let covered = 0;
    let total = 0;
    items.forEach((item) => {
      const signatures = num(item.signatures) || 0;
      total += signatures;
      if (item.kind && item.kind !== "__unrecorded__") covered += signatures;
    });
    return total ? { covered, total } : null;
  }
  function frictionQuietNote(group) {
    if (!group || group.status !== "quiet") return "";
    const sessions = num(group.project_sessions_last_7d);
    const text = sessions == null
      ? uiText("当前筛选范围内的同项目会话数未记录", "Sessions in the same projects in the selected range are not recorded")
      : uiText("当前筛选范围内同项目 " + sessions + " 个会话", sessions + " sessions in the same projects in the selected range");
    return '<small class="friction-quiet-note"' + (sessions == null ? ' data-missing="true"' : "") + ">" + esc(text) + "</small>";
  }
  const FRICTION_KIND_ORDER = ["tool_error", "nonzero_exit", "asset_violation", "user_interrupt"];
  // user_interrupt is named by the category table, the other three by the kind
  // table; one helper so the bar, the legend and the columns say the same word.
  const frictionKindName = (key) => key === "user_interrupt" ? frictionCategoryLabel("user_interrupt") : frictionKindLabel(key);
  function frictionKindParts(group) {
    return FRICTION_KIND_ORDER
      .map((key) => ({ key, value: num(group && group[key + "_count"]) }))
      .filter((part) => part.value != null);
  }
  // The four kind columns became one stacked bar: a column that is zero for
  // every row disappears with its segment instead of printing a row of zeros.
  function frictionKindBar(group) {
    const parts = frictionKindParts(group);
    if (!parts.length) return '<span class="friction-kind-bar" data-empty="true">' + uiText("未记录", "Not recorded") + "</span>";
    const total = parts.reduce((sum, part) => sum + part.value, 0);
    if (!total) return '<span class="friction-kind-bar" data-empty="true">0</span>';
    const segments = parts.filter((part) => part.value > 0).map((part) => '<i data-kind="' + esc(part.key) + '" style="--w:' + (part.value / total * 100).toFixed(2) + '%" title="' + esc(frictionKindName(part.key) + " " + part.value + " / " + total) + '"></i>').join("");
    return '<span class="friction-kind-bar" title="' + esc(parts.filter((part) => part.value > 0).map((part) => frictionKindName(part.key) + " " + part.value).join(" · ")) + '">' + segments + "</span>";
  }
  function frictionKindLegend(groups) {
    const totals = {};
    (Array.isArray(groups) ? groups : []).forEach((group) => frictionKindParts(group).forEach((part) => { totals[part.key] = (totals[part.key] || 0) + part.value; }));
    const present = FRICTION_KIND_ORDER.filter((key) => totals[key]);
    if (!present.length) return "";
    const hidden = FRICTION_KIND_ORDER.filter((key) => totals[key] === 0);
    const chips = present.map((key) => '<span class="friction-kind-legend-item"><i data-kind="' + esc(key) + '"></i><span>' + esc(frictionKindName(key)) + "</span><b>" + totals[key] + "</b></span>").join("");
    const collapsed = hidden.length
      ? '<span class="friction-kind-legend-item" data-zero="true">' + esc(uiText("本页全为 0 已折叠：", "All zero on this page, collapsed: ") + hidden.map(frictionKindName).join(" · ")) + "</span>"
      : "";
    return '<div class="friction-kind-legend">' + chips + collapsed + "</div>";
  }
  // A zero here is a recorded zero, not a link; it is greyed so the eye skips
  // it, while "not recorded" keeps its own wording.
  function frictionNumberCell(value, tone) {
    const parsed = num(value);
    const zero = parsed === 0;
    return '<span class="friction-number-cell"' + (zero || parsed == null ? "" : ' data-tone="' + esc(tone || "") + '"') + ' data-zero="' + zero + '" data-missing="' + (parsed == null) + '">' + esc(frictionSummaryValue(value)) + "</span>";
  }
  function frictionRecordToolName(record) {
    const payload = jsonObject(record && record.payload);
    const name = record && record.tool_name || payload.tool_name || "";
    return /^(toolu_|call_)/.test(name) ? "" : name;
  }

  function frictionRecordEvidence(record) {
    const parts = [];
    if (record && record.is_error === true) parts.push("is_error=true");
    if (record && record.exit_code != null) parts.push("exit_code=" + record.exit_code);
    if (!parts.length && record && record.event_type) parts.push(record.event_type);
    return parts.length ? parts.join(" · ") : (view.locale === "en" ? "Explicit friction evidence recorded" : "已记录明确摩擦证据");
  }

  function frictionRecordSession(record) {
    const title = record && record.session_title;
    const session = record && record.session_id ? shortSessionID(record.session_id) : (view.locale === "en" ? "Session not recorded" : "会话未记录");
    return (title || (view.locale === "en" ? "Session name not recorded" : "会话名称未记录")) + " · " + session;
  }

  // The session link carries the events-table id so the session detail page can
  // land on the exact event; without one the link says so instead of pretending.
  function frictionSessionHref(record) {
    if (!record || !record.session_id) return "";
    const base = "#/sessions/" + encodeURIComponent(record.session_id);
    return record.event_id ? base + "?event=" + encodeURIComponent(record.event_id) : base;
  }

  // A signature groups records whose category, tool and normalized output line
  // are identical. The row shows the sample line and how many sessions it
  // appeared in; it never claims the occurrences share a cause.
  // A program's failure count is only meaningful against the number of calls
  // whose outcome was actually recorded; a bare "0 failures" would read as
  // "nothing failed" when it means "nothing was recorded".
  function failureText(entry) {
    const failures = num(entry && entry.failures);
    const known = num(entry && entry.known_outcomes);
    if (failures == null) return uiText("失败未记录", "Failures not recorded");
    if (known == null) return uiText(failures + " 次失败 / 已记录结果未记录", failures + " failures / recorded outcomes not recorded");
    return uiText(failures + " 次失败 / " + known + " 次已记录结果", failures + " failures / " + known + " recorded outcomes");
  }
  function frictionSignatureSample(group) {
    const line = group && (group.sample_line || group.label);
    return line || (view.locale === "en" ? "Sample line not recorded" : "样例行未记录");
  }
  const frictionWatchLabels = {
    watching: ["验证中", "Verifying"], verified: ["修复有效", "Fix verified"],
    no_change: ["未见改善", "No change"], unobservable: ["无法判断", "Unobservable"],
    cancelled: ["已取消", "Cancelled"]
  };
  function frictionWatchBadgeHTML(watch) {
    if (!watch) return "";
    const labels = frictionWatchLabels[watch.status] || [watch.status, watch.status];
    const tone = watch.status === "verified" ? "good" : watch.status === "no_change" ? "bad" : "muted";
    const detail = watch.status === "verified"
      ? uiText("窗口内零发生，同项目会话 " + count(watch.project_sessions_in_window) + " 个", "zero occurrences in the window; " + count(watch.project_sessions_in_window) + " same-project sessions ran")
      : watch.status === "no_change"
        ? uiText("创建后仍发生 " + count(watch.window_count) + " 次", "still occurred " + count(watch.window_count) + " times after the rule was written")
        : watch.status === "unobservable"
          ? uiText("窗口内同项目没有会话", "no session ran in the watched projects")
          : uiText("已观察 " + count(watch.window_days) + " 天窗口", "a " + count(watch.window_days) + "-day window is being observed");
    return '<span class="fl-flag" data-flag="' + tone + '" data-watch-status="' + esc(watch.status) + '" title="' + esc(detail) + '">' + icon(watch.status === "verified" ? "check" : "hourglass") + esc(labels[0]) + "</span>";
  }
  function frictionBriefPanel(group) {
    const brief = group && group.brief;
    if (!brief) return "";
    const mechanism = brief.mechanism && brief.mechanism.mechanism
      ? daemonProse(brief.mechanism.mechanism, brief.mechanism.mechanism_en)
      : uiText("机制字典未覆盖；请先带样例问你的 agent。", "Not in the mechanism dictionary; ask your agent with the samples first.");
    const evidence = brief.evidence || {};
    const samples = (evidence.sample_lines || []).map((line) => '<li data-no-translate="true">' + esc(line) + "</li>").join("");
    const watch = group.watch;
    const watchBlock = watch
      ? '<div class="friction-brief-watch" data-watch-status="' + esc(watch.status) + '"><strong>' + esc(frictionWatchLabels[watch.status] ? frictionWatchLabels[watch.status][0] : watch.status) + '</strong><span>' + esc(uiText("验证窗口 " + watch.window_days + " 天；创建后发生 " + count(watch.window_count) + " 次，同项目会话 " + count(watch.project_sessions_in_window) + " 个。", "a " + watch.window_days + "-day window; " + count(watch.window_count) + " occurrences after the rule, " + count(watch.project_sessions_in_window) + " same-project sessions.")) + '</span><button class="us-btn" data-variant="ghost" data-size="sm" data-action="friction-watch-cancel" data-watch-id="' + esc(watch.id) + '">' + uiText("取消验证", "Cancel verification") + "</button></div>"
      : '<div class="friction-brief-watch"><span>' + esc(uiText("把简报交给你的 agent 写好规则后，在这里开始验证：Flatline 会盯着这个签名，窗口内安静且同项目仍在跑会话，就判“修复有效”。", "After your agent drafts the rule from the brief, start verification here: Flatline watches the signature and reports “fix verified” once it stays quiet for a window while the same projects keep running.")) + '</span><button class="us-btn" data-variant="outline" data-size="sm" data-action="friction-watch-create" data-signature="' + esc(group.signature || "") + '">' + icon("history") + uiText("写入规则后开始验证", "Verify after writing the rule") + "</button></div>";
    return '<div class="friction-brief-panel" data-signature="' + esc(group.signature || "") + '">'
      + '<div class="friction-brief-row"><span class="friction-brief-label">' + uiText("机制", "Mechanism") + '</span><span data-no-translate="true">' + esc(mechanism) + "</span>" + (brief.mechanism && brief.mechanism.mechanism ? daemonCopyFlag(mechanism) : "") + "</div>"
      + '<div class="friction-brief-row"><span class="friction-brief-label">' + uiText("建议落点", "Suggested target") + '</span><span><strong data-no-translate="true">' + esc(brief.target.kind_label) + '</strong> <span data-no-translate="true">' + esc(brief.target.reason) + "</span></span></div>"
      + '<div class="friction-brief-row"><span class="friction-brief-label">' + uiText("证据", "Evidence") + '</span><span>' + esc(quantity(evidence.count, "次", "time", "times") + " · " + quantity(evidence.session_count, "个会话", "session", "sessions") + " · " + quantity(evidence.project_count, "个项目", "project", "projects") + (evidence.last_seen_at ? " · " + uiText("最近 ", "last ") + shortDate(evidence.last_seen_at) : "")) + "</span></div>"
      + (samples ? '<div class="friction-brief-row"><span class="friction-brief-label">' + uiText("样例", "Samples") + '</span><ul class="friction-brief-samples">' + samples + "</ul></div>" : "")
      + '<div class="friction-brief-row friction-brief-prompt-row"><span class="friction-brief-label">' + uiText("给 agent 的简报", "Brief for your agent") + '</span><textarea class="friction-brief-prompt" rows="6" readonly data-no-translate="true">' + esc(view.locale === "en" ? brief.paste_prompt_en : brief.paste_prompt) + "</textarea>"
      + '<button class="us-btn" data-variant="outline" data-size="sm" data-action="friction-brief-copy" data-signature="' + esc(group.signature || "") + '">' + icon("file-text") + uiText("复制简报", "Copy brief") + "</button></div>"
      + '<p class="insight-criterion">' + esc(uiText("判定规则：", "Rule: ") + (view.locale === "en" ? brief.criterion_en : brief.criterion)) + "</p>"
      + watchBlock + "</div>";
  }
  function frictionSignatureGroupRow(group) {
    const toolRecorded = group.tool_name && group.tool_name !== "__unrecorded__";
    const open = view.frictionBriefSignature && view.frictionBriefSignature === group.signature;
    const row = '<button type="button" class="friction-group-row" data-key="fs:' + esc(group.signature || "") + '" data-action="friction-signature-filter" data-signature="' + esc(group.signature || "") + '"><span class="friction-project-cell">'
      + '<span class="friction-signature-head">' + frictionStatusBadge(group.status) + frictionCategoryBadge(group.category) + frictionWatchBadgeHTML(group.watch) + '</span>'
      + '<small class="friction-signature-sample" data-no-translate="true">' + esc(frictionSignatureSample(group)) + '</small>'
      + frictionHintLine(group) + frictionQuietNote(group)
      + '</span><span class="friction-rule-cell"' + (toolRecorded ? ' data-no-translate="true"' : ' data-missing="true"') + '>' + esc(frictionToolLabel(group.tool_name)) + '</span>'
      + '<strong class="friction-count-cell">' + esc(frictionSummaryValue(group.count == null ? group.friction_count : group.count)) + '</strong>'
      + frictionNumberCell(group.session_count) + frictionNumberCell(group.project_count)
      + '<span class="friction-last-cell">' + esc(shortDate(group.last_occurred_at)) + '</span><span class="friction-row-action friction-brief-toggle' + (open ? '" data-open="true' : '') + '" data-action="friction-brief-toggle" data-signature="' + esc(group.signature || "") + '" title="' + esc(uiText("规则简报与修复验证", "Rule brief and fix verification")) + '">' + icon("book-open") + "</span></button>";
    if (!open) return row;
    return '<div class="friction-signature-wrap">' + row + frictionBriefPanel(group) + "</div>";
  }
  function frictionGroupRow(group) {
    if (group && group.group_by === "category") return frictionCategoryGroupRow(group);
    if (group && group.group_by === "tool") return frictionToolGroupRow(group);
    if (group && (group.group_by === "signature" || (view.frictionGroupBy === "signature" && group.signature != null))) return frictionSignatureGroupRow(group);
    const href = "#/friction/" + encodeURIComponent(group.project_key || "__unrecorded__") + "/" + encodeURIComponent(group.harness || "") + "?" + frictionFilterParams().toString();
    const rowKey = "fg:" + (group.project_key || "__unrecorded__") + ":" + (group.harness || "");
    const path = group.cwd || (view.locale === "en" ? "Working directory not recorded" : "工作目录未记录");
    return '<a class="friction-group-row" data-key="' + esc(rowKey) + '" href="' + esc(href) + '"><span class="friction-project-cell"><strong data-no-translate="true">' + esc(frictionGroupProjectLabel(group)) + '</strong><small data-no-translate="true">' + esc(path) + '</small></span><span class="friction-harness-cell">' + sourceIcon(group.harness) + '</span><strong class="friction-count-cell">' + esc(frictionSummaryValue(group.friction_count)) + '</strong>' + frictionKindBar(group) + frictionNumberCell(group.session_count) + '<span class="friction-last-cell">' + esc(shortDate(group.last_occurred_at)) + '</span><span class="friction-row-action">' + icon("chevronRight") + '</span></a>';
  }

  function frictionCategoryGroupRow(group) {
    const key = frictionCategoryKey(group.category);
    const rule = daemonProse(group.category_rule, group.category_rule_en) || (view.locale === "en" ? "Rule not recorded" : "规则未记录");
    return '<button type="button" class="friction-group-row" data-key="fc:' + esc(key) + '" data-action="friction-category-filter" data-category="' + esc(key) + '"><span class="friction-project-cell">' + frictionCategoryBadge(group.category) + '</span><span class="friction-rule-cell"><span data-no-translate="true">' + esc(rule) + "</span>" + daemonCopyFlag(rule) + '</span><strong class="friction-count-cell">' + esc(frictionSummaryValue(group.friction_count)) + '</strong>' + frictionNumberCell(group.session_count) + frictionNumberCell(group.project_count) + '<span class="friction-last-cell">' + esc(shortDate(group.last_occurred_at)) + '</span><span class="friction-row-action">' + icon("chevronRight") + '</span></button>';
  }

  function frictionToolGroupRow(group) {
    const recorded = group.tool_name && group.tool_name !== "__unrecorded__";
    const key = recorded ? group.tool_name : "__unrecorded__";
    const label = frictionToolLabel(group.tool_name);
    return '<button type="button" class="friction-group-row" data-key="ft:' + esc(key) + '" data-action="friction-tool-filter" data-tool="' + esc(key) + '"><span class="friction-project-cell"><strong' + (recorded ? ' data-no-translate="true"' : '') + '>' + esc(label) + '</strong><small>' + (recorded ? esc(view.locale === "en" ? "Tool name resolved from the tool call" : "工具名来自同会话的工具调用") : esc(view.locale === "en" ? "The source history records no tool identity" : "来源历史没有记录工具身份")) + '</small></span><strong class="friction-count-cell">' + esc(frictionSummaryValue(group.friction_count)) + '</strong>' + frictionKindBar(group) + frictionNumberCell(group.session_count) + frictionNumberCell(group.project_count) + '<span class="friction-last-cell">' + esc(shortDate(group.last_occurred_at)) + '</span><span class="friction-row-action">' + icon("chevronRight") + '</span></button>';
  }

  function frictionTableHead(groupBy) {
    const cell = (label) => '<span>' + esc(label) + '</span>';
    const numCell = (label) => '<span class="friction-head-number">' + esc(label) + '</span>';
    const kindCell = cell(view.locale === "en" ? "Kind split" : "类别分布");
    if (groupBy === "category") {
      return '<div class="friction-table-head">' + cell(view.locale === "en" ? "Category" : "类别") + cell(view.locale === "en" ? "Rule that matched" : "命中的规则") + numCell(view.locale === "en" ? "Total" : "总数") + numCell(view.locale === "en" ? "Sessions" : "会话") + numCell(view.locale === "en" ? "Projects" : "项目") + cell(view.locale === "en" ? "Last seen" : "最近发生") + '<span></span></div>';
    }
    if (groupBy === "signature") {
      return '<div class="friction-table-head">' + cell(view.locale === "en" ? "Status, sample and mechanism" : "状态 · 样例 · 机制") + cell(view.locale === "en" ? "Tool" : "工具") + numCell(view.locale === "en" ? "Total" : "总数") + numCell(view.locale === "en" ? "Sessions" : "会话") + numCell(view.locale === "en" ? "Projects" : "项目") + cell(view.locale === "en" ? "Last seen" : "最近发生") + '<span></span></div>';
    }
    if (groupBy === "tool") {
      return '<div class="friction-table-head">' + cell(view.locale === "en" ? "Tool" : "工具") + numCell(view.locale === "en" ? "Total" : "总数") + kindCell + numCell(view.locale === "en" ? "Sessions" : "会话") + numCell(view.locale === "en" ? "Projects" : "项目") + cell(view.locale === "en" ? "Last seen" : "最近发生") + '<span></span></div>';
    }
    return '<div class="friction-table-head">' + cell(view.locale === "en" ? "Project" : "项目") + cell(view.locale === "en" ? "Harness" : "harness") + numCell(view.locale === "en" ? "Total" : "总数") + kindCell + numCell(view.locale === "en" ? "Sessions" : "会话") + cell(view.locale === "en" ? "Last seen" : "最近发生") + '<span></span></div>';
  }

  function frictionSegment(action, dataKey, current, options) {
    return segmentControl("friction-" + action, dataKey, options.map(([value, label]) => ({ value, label })), current);
  }

  function frictionSelectControl(action, dataKey, current, options, ariaLabel) {
    return selectControl("friction-" + action, ariaLabel, options.map(([value, label]) => ({ value, label })), current);
  }

  // One filter bar: a search box, one Filters button that opens the grouped
  // popover, and the chosen conditions as removable chips underneath.
  function frictionActiveChips(detailGroup) {
    const chips = [];
    const clear = (action, label) => ({ action, label });
    if (!detailGroup && view.frictionProjectFilter !== "all") chips.push(chipControl(uiText("项目：", "Project: ") + frictionProjectLabel(view.frictionProjectFilter), clear("friction-clear-project", uiText("清除项目筛选", "Clear project filter"))));
    if (!detailGroup && view.frictionHarnessFilter !== "all") chips.push(chipControl("harness: " + source(view.frictionHarnessFilter), clear("friction-clear-harness", uiText("清除 harness 筛选", "Clear harness filter"))));
    if (view.frictionKindFilter !== "all") chips.push(chipControl(uiText("类型：", "Type: ") + (view.frictionKindFilter === "user_interrupt" ? frictionCategoryLabel("user_interrupt") : frictionKindLabel(view.frictionKindFilter)), clear("friction-clear-kind", uiText("清除类型筛选", "Clear type filter"))));
    if (view.frictionCategoryFilter === "expected_exit") {
      chips.push(chipControl(uiText("记录范围：预期非零退出", "Record set: expected non-zero exits"), clear("friction-clear-category", uiText("清除记录范围筛选", "Clear record-set filter"))));
    } else if (view.frictionCategoryFilter !== "all") {
      chips.push(chipControl(uiText("类别：", "Category: ") + frictionCategoryLabel(view.frictionCategoryFilter === FRICTION_UNCLASSIFIED ? "" : view.frictionCategoryFilter), clear("friction-clear-category", uiText("清除类别筛选", "Clear category filter"))));
    }
    if (view.frictionToolFilter !== "all") chips.push(chipControl(uiText("工具：", "Tool: ") + frictionToolLabel(view.frictionToolFilter === "__unrecorded__" ? "" : view.frictionToolFilter), clear("friction-clear-tool", uiText("清除工具筛选", "Clear tool filter"))));
    if (view.frictionRange !== "all") {
      const rangeLabel = view.frictionRange === "custom"
        ? dateRangeLabel(view.frictionFrom, view.frictionTo)
        : uiText("近 " + FRICTION_RANGE_DAYS[view.frictionRange] + " 天", "Last " + FRICTION_RANGE_DAYS[view.frictionRange] + " days");
      chips.push(chipControl(uiText("时间：", "Range: ") + rangeLabel, clear("friction-clear-range", uiText("清除时间筛选", "Clear range filter"))));
    }
    if (view.frictionSignatureFilter !== "all") chips.push(chipControl(uiText("签名：", "Signature: ") + view.frictionSignatureFilter, clear("friction-clear-signature", uiText("清除签名筛选", "Clear signature filter"))));
    if (!chips.length) return "";
    return '<div class="fl-chip-row"><span class="fl-chip-row-label">' + uiText("当前筛选", "Active filters") + "</span>" + chips.join("") + '<button type="button" class="fl-chip-clear-all" data-action="friction-clear-all">' + uiText("清空全部", "Clear all") + "</button></div>";
  }
  function frictionFilterGroups(data, detailGroup) {
    const summary = data && data.summary || {};
    const groups = [];
    const expectedExitCount = num(summary.expected_exit_count);
    const scopeOptions = [{ value: "all", label: uiText("明确摩擦", "Explicit friction") }];
    if (expectedExitCount != null) {
      scopeOptions.push({ value: "expected_exit", label: uiText("预期非零退出（已排除） · " + count(expectedExitCount), "Expected non-zero exits (excluded) · " + count(expectedExitCount)) });
    }
    groups.push({ key: "scope", label: uiText("记录范围", "Record set"), value: view.frictionCategoryFilter === "expected_exit" ? "expected_exit" : "all", options: scopeOptions });
    groups.push({ key: "kind", label: uiText("类型", "Type"), value: view.frictionKindFilter, options: [
      { value: "all", label: uiText("全部类型", "All types") },
      { value: "tool_error", label: frictionKindLabel("tool_error") + " · " + count(summary.tool_error_count) },
      { value: "nonzero_exit", label: frictionKindLabel("nonzero_exit") + " · " + count(summary.nonzero_exit_count) },
      { value: "asset_violation", label: frictionKindLabel("asset_violation") + " · " + count(summary.asset_violation_count) },
      { value: "user_interrupt", label: frictionCategoryLabel("user_interrupt") + " · " + count(summary.user_interrupt_count) }
    ] });
    groups.push({ key: "category", label: uiText("类别", "Category"), value: view.frictionCategoryFilter === "expected_exit" ? "all" : view.frictionCategoryFilter, options: [{ value: "all", label: uiText("全部类别", "All categories") }].concat((Array.isArray(summary.by_category) ? summary.by_category : []).map((item) => ({ value: item.key, label: frictionCategoryLabel(item.key === "__unrecorded__" ? "" : item.key) + " · " + count(item.count) }))) });
    groups.push({ key: "tool", label: uiText("工具", "Tool"), value: view.frictionToolFilter, options: [{ value: "all", label: uiText("全部工具", "All tools") }].concat((Array.isArray(summary.by_tool) ? summary.by_tool : []).map((item) => ({ value: item.key, label: frictionToolLabel(item.key === "__unrecorded__" ? "" : item.key) + " · " + count(item.count) }))) });
    return groups;
  }
  function frictionActiveCount(detailGroup) {
    let total = 0;
    if (view.frictionKindFilter !== "all") total += 1;
    if (view.frictionCategoryFilter !== "all") total += 1;
    if (view.frictionToolFilter !== "all") total += 1;
    if (view.frictionSignatureFilter !== "all") total += 1;
    return total;
  }
  function frictionExclusionNote(summary) {
    const excluded = num(summary && summary.expected_exit_count);
    if (excluded == null) return '<span class="friction-filter-exclusion" data-showing="false">' + esc(uiText("预期非零退出数量未记录。", "Expected non-zero exit count is not recorded.")) + "</span>";
    const showing = view.frictionCategoryFilter === "expected_exit";
    const text = showing
      ? uiText("当前正在查看 " + count(excluded) + " 条预期非零退出。", "Showing " + count(excluded) + " expected non-zero exits.")
      : uiText("默认未计入 " + count(excluded) + " 条预期非零退出；可在“筛选”里的“记录范围”中查看。", count(excluded) + " expected non-zero exits are excluded by default; view them under “Record set” in Filters.");
    return '<span class="friction-filter-exclusion" data-showing="' + showing + '">' + esc(text) + "</span>";
  }
  function frictionQuickFilters(data, detailGroup) {
    const projects = Array.isArray(data && data.projects) ? data.projects : [];
    const projectOptions = [{ value: "all", label: uiText("全部项目", "All projects") }].concat(projects.map((project) => ({ value: project.key, label: frictionProjectOptionLabel(project) })));
    const harnessOptionsList = [{ value: "all", label: uiText("全部 harness", "All harnesses") }].concat(harnessOptions(data && data.summary && data.summary.by_harness));
    const project = detailGroup
      ? '<span class="friction-quick-fixed"><strong data-no-translate="true">' + esc(frictionGroupProjectLabel(detailGroup)) + '</strong><small data-no-translate="true">' + esc(detailGroup.cwd || uiText("工作目录未记录", "Working directory not recorded")) + "</small></span>"
      : selectControl("friction-project-filter", uiText("项目", "Project"), projectOptions, view.frictionProjectFilter, { searchable: projectOptions.length > 8 });
    const harness = detailGroup
      ? '<span class="friction-quick-fixed friction-quick-harness">' + sourceIcon(detailGroup.harness) + '<strong>' + esc(source(detailGroup.harness)) + "</strong></span>"
      : selectControl("friction-harness-filter", "harness", harnessOptionsList, view.frictionHarnessFilter, { searchable: false });
    const rangeOptions = [
      { value: "all", label: uiText("全部", "All") },
      { value: "7d", label: uiText("7 天", "7 days") },
      { value: "30d", label: uiText("30 天", "30 days") },
      { value: "90d", label: uiText("90 天", "90 days") },
      { value: "custom", label: uiText("自定义", "Custom") }
    ];
    const range = segmentControl("friction-range-filter", "range", rangeOptions, view.frictionRange === "custom" ? "custom" : view.frictionRange)
      + (view.frictionRange === "custom" ? dateRangeControl("friction-range", view.frictionFrom, view.frictionTo, uiText("摩擦时间范围", "Friction date range")) : "");
    const search = '<label class="fl-search"><span class="sr-only">' + uiText("搜索摩擦", "Search friction") + "</span>" + icon("search", "fl-search-icon") + '<input type="search" data-friction-search placeholder="' + uiText("搜索项目、工具或会话", "Search project, tool or session") + '" value="' + esc(view.frictionQuery) + '"></label>';
    const active = frictionActiveChips(detailGroup);
    const exclusion = frictionExclusionNote(data && data.summary);
    const footer = active || exclusion ? '<div class="friction-filter-footer">' + (active || "") + exclusion + "</div>" : "";
    return '<section class="friction-filter-panel" aria-label="' + esc(uiText("摩擦筛选条件", "Friction filters")) + '"><div class="friction-filter-head"><div><strong>' + uiText("筛选条件", "Filters") + '</strong><span>' + uiText("搜索、范围和类型筛选会同时更新统计与列表", "Search, range and type filters update the summary and list together") + '</span></div></div><div class="friction-filter-search-row">' + search + filterControl("friction-filters", frictionFilterGroups(data, detailGroup), frictionActiveCount(detailGroup)) + '</div><div class="friction-quick-fields"><div class="friction-quick-field"><span>' + uiText("项目", "Project") + '</span>' + project + '</div><div class="friction-quick-field"><span>harness</span>' + harness + '</div><div class="friction-quick-field friction-quick-range"><span>' + uiText("时间范围", "Time range") + '</span><div class="friction-quick-range-controls">' + range + '</div></div></div>' + footer + "</section>";
  }
  function frictionToolbar(data, detailGroup) {
    return frictionQuickFilters(data, detailGroup);
  }
  function frictionArrangeBar(groupBy, detailGroup) {
    if (detailGroup) return "";
    const groups = [
      { value: "project", label: uiText("按项目", "By project") },
      { value: "category", label: uiText("按类别", "By category") },
      { value: "tool", label: uiText("按工具", "By tool") },
      { value: "signature", label: uiText("反复出现", "Recurring") }
    ];
    const sortOptions = [
      { value: "count", label: uiText("摩擦最多", "Most friction") },
      { value: "recent", label: uiText("最近发生", "Most recent") },
      { value: "sessions", label: uiText("涉及会话最多", "Most sessions") }
    ];
    return '<div class="friction-view-controls" aria-label="' + esc(uiText("结果展示", "Result display")) + '"><span class="friction-view-label">' + uiText("结果展示", "View") + '</span>' + segmentControl("friction-group-by", "group", groups, groupBy) + selectControl("friction-sort", uiText("摩擦排序", "Sort friction"), sortOptions, view.frictionSort, { searchable: false, size: "sm" }) + "</div>";
  }
  function drawFrictionLoading(detail) {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const title = detail ? (view.locale === "en" ? "Friction details" : "摩擦详情") : (view.locale === "en" ? "Friction" : "摩擦");
    setScreen(header(title, view.locale === "en" ? "Reading local friction evidence" : "正在读取本地摩擦证据", "") + screenContent('<section class="elevated-card card-pad friction-loading-card"><div class="prototype-loading"><i class="prototype-loading-mark"></i><span>' + (view.locale === "en" ? "Reading local friction evidence…" : "正在读取本地摩擦证据…") + '</span></div></section>', "friction-page"));
  }

  async function loadFrictionOverview(reset) {
    const request = ++view.frictionRequest;
    const previous = view.frictionOverview;
    view.frictionLoading = true;
    // The full-page loading card is only for a first load. Once there is
    // something on screen, a filter change patches it in place, so the search
    // box the user is typing in survives with its caret.
    if (previous) drawFrictionOverview(previous, true);
    else drawFrictionLoading(false);
    const offset = reset ? 0 : previous && previous.groups ? previous.groups.length : 0;
    const data = await get(frictionQueryPath(false, null, offset));
    if (request !== view.frictionRequest) return;
    if (reset || !previous) {
      view.frictionOverview = data;
    } else {
      view.frictionOverview = Object.assign({}, previous, data, { groups: (previous.groups || []).concat(data.groups || []) });
    }
    cache.friction = view.frictionOverview;
    view.frictionLoading = false;
    drawFrictionOverview(view.frictionOverview, !reset);
    const frictionNavCount = document.querySelector('[data-nav="friction"] .nav-count');
    if (frictionNavCount && view.frictionOverview.summary) frictionNavCount.textContent = frictionSummaryValue(view.frictionOverview.summary.total_events);
  }

  async function loadMoreFrictionOverview() {
    const data = view.frictionOverview;
    if (!data || view.frictionLoading || !data.pagination || !data.pagination.has_more) return;
    await loadFrictionOverview(false);
  }

  function armFrictionLazyRows() {
    const scroll = document.querySelector(".friction-page-scroll");
    if (!scroll) return;
    const check = () => {
      if (scroll.scrollTop + scroll.clientHeight >= scroll.scrollHeight - 240) loadMoreFrictionOverview().catch(renderError);
    };
    scroll.addEventListener("scroll", check, { passive: true });
    setTimeout(check, 0);
  }

  function frictionStatRow(summary, detail) {
    const cards = [
      frictionStatCard(view.locale === "en" ? "Explicit friction events" : "明确摩擦事件", summary.total_events, "triangle-alert", "bad"),
      frictionStatCard(frictionKindLabel("tool_error"), summary.tool_error_count, frictionKindIcons.tool_error, "bad"),
      frictionStatCard(frictionKindLabel("nonzero_exit"), summary.nonzero_exit_count, frictionKindIcons.nonzero_exit, "warn"),
      frictionStatCard(frictionCategoryLabel("user_interrupt"), summary.user_interrupt_count, frictionCategoryIcons.user_interrupt, "accent")
    ];
    if (detail) cards.push(frictionStatCard(frictionKindLabel("asset_violation"), summary.asset_violation_count, frictionKindIcons.asset_violation, "bad"));
    if (num(summary.recurring_signatures) != null) cards.push(frictionStatCard(view.locale === "en" ? "Recurring signatures (≥2 sessions)" : "反复出现的签名（≥2 个会话）", summary.recurring_signatures, "history", "warn"));
    cards.push(frictionStatCard(view.locale === "en" ? "Sessions involved" : "涉及会话", summary.session_count, "layers", "accent"));
    return '<div class="friction-summary" aria-label="' + esc(uiText("统计摘要", "Summary")) + '"><div class="fl-kpi-row">' + cards.join("") + "</div></div>";
  }

  // by_hint_kind (§13.8) reads as one row: each mechanism kind with the share
  // of signatures it covers. __unrecorded__ means "the dictionary does not
  // cover it", not "there is no mechanism".
  function frictionHintKindRow(summary) {
    const items = Array.isArray(summary && summary.by_hint_kind) ? summary.by_hint_kind : null;
    if (!items) return "";
    const isUncovered = (item) => !item.kind || item.kind === "__unrecorded__";
    const covered = items.filter((item) => !isUncovered(item));
    // "Not in the dictionary" is the absence of a mechanism, not a mechanism.
    // It stops taking a coloured segment and becomes a note beside the bar.
    const uncovered = items.filter(isUncovered).reduce((sum, item) => ({
      count: sum.count + (num(item.count) || 0),
      signatures: sum.signatures + (num(item.signatures) || 0),
      sessions: sum.sessions + (num(item.session_count) || 0)
    }), { count: 0, signatures: 0, sessions: 0 });
    const total = covered.reduce((sum, item) => sum + (num(item.count) || 0), 0);
    if (!total && !uncovered.count) return "";
    const bar = covered.map((item) => '<i data-kind="' + esc(item.kind) + '" style="--w:' + ((num(item.count) || 0) / (total || 1) * 100).toFixed(2) + '%" title="' + esc(hintKindLabel(item.kind) + " " + count(item.count) + " / " + total) + '"></i>').join("");
    const legend = covered.map((item) => '<span class="friction-hint-kind-item"><i data-kind="' + esc(item.kind) + '"></i>' + esc(hintKindLabel(item.kind)) + "<b>" + esc(count(item.count)) + "</b><small>" + esc(uiText(count(item.signatures) + " 个签名 · " + count(item.session_count) + " 个会话", count(item.signatures) + " signatures · " + count(item.session_count) + " sessions")) + "</small></span>").join("");
    const aside = uncovered.signatures || uncovered.count
      ? '<small class="friction-hint-uncovered">' + esc(uiText(
        "另有 " + uncovered.signatures + " 条签名 / " + uncovered.count + " 条摩擦 / " + uncovered.sessions + " 个会话，机制字典未覆盖，不占色块。",
        "A further " + uncovered.signatures + " signatures, " + uncovered.count + " friction records and " + uncovered.sessions + " sessions are not in the dictionary and take no segment.")) + "</small>"
      : "";
    return '<div class="friction-hint-kinds"><div class="friction-hint-kinds-head"><span>' + uiText("机制分布", "Mechanism split") + '</span><small>' + uiText("按摩擦条数；只画字典覆盖到的机制", "By friction records; only mechanisms the dictionary covers are drawn") + '</small></div><span class="friction-hint-kind-bar">' + bar + '</span>' + aside + '<div class="friction-hint-kind-legend">' + legend + "</div></div>";
  }
  // coverage_gaps names the signatures that recur and that no rule asset
  // mentions the mechanism of. The table states the three recorded facts and
  // stops there; it does not say what to write. Until the daemon sends the
  // field the table is absent rather than empty.
  function frictionCoverageGapTable(summary) {
    const gaps = Array.isArray(summary && summary.coverage_gaps) ? summary.coverage_gaps : null;
    if (!gaps || !gaps.length) return "";
    const rows = gaps.slice(0, 10).map((gap) => {
      const signature = gap.sample_line || gap.signature || uiText("签名未记录", "Signature not recorded");
      const mechanism = daemonProse(gap.mechanism, gap.mechanism_en) || (gap.hint_kind ? hintKindLabel(gap.hint_kind) : "");
      const project = gap.project_key || uiText("项目未记录", "Project not recorded");
      const href = overviewRangeHref("#/friction", "signature=" + encodeURIComponent(gap.signature || "") + "&project=" + encodeURIComponent(gap.project_key || "__unrecorded__"));
      return '<div class="friction-gap-row"><a class="friction-gap-signature" href="' + esc(href) + '" data-no-translate="true" title="' + esc(signature) + '">' + esc(signature) + '</a><span class="friction-gap-project" data-no-translate="true" title="' + esc(project) + '">' + esc(project) + "</span>"
        + frictionNumberCell(gap.session_count)
        + '<span class="friction-gap-mechanism"' + (mechanism ? "" : ' data-missing="true"') + ">" + esc(mechanism || uiText("机制未记录", "Mechanism not recorded")) + "</span></div>";
    }).join("");
    return '<section class="elevated-card friction-gap-card"><header class="fl-head"><h3>' + uiText("规则覆盖缺口", "Rule coverage gaps") + '</h3><span class="fl-aside">' + esc(quantity(gaps.length, "个组合", "pairs", "pairs")) + '</span></header><p class="friction-method-note">' + uiText("反复出现（同项目 ≥2 个会话）、且该项目适用的规则资产（用户级 + 项目目录下）都没提到该机制的（签名 × 项目）组合。", "Signature-and-project pairs that recur (2+ sessions in the same project) while no rule applicable to that project (user-scope plus files under it) mentions the mechanism.") + '</p><div class="friction-gap-table"><div class="friction-gap-head"><span>' + uiText("签名", "Signature") + "</span><span>" + uiText("项目", "Project") + "</span><span>" + uiText("出现会话数", "Sessions") + "</span><span>" + uiText("机制", "Mechanism") + "</span></div>" + rows + "</div></section>";
  }
  function frictionToolCoverageNote(summary) {
    const unrecorded = num(summary.tool_unrecorded_count);
    const total = num(summary.total_events);
    if (unrecorded == null || total == null) return "";
    return view.locale === "en"
      ? "Tool identity resolved for " + (total - unrecorded) + "/" + total + " events; the rest keep “Tool not recorded”."
      : "已解析出工具身份的事件 " + (total - unrecorded) + "/" + total + " 条；其余保持“工具未记录”。";
  }

  function drawFrictionOverview(data, keepScroll) {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const oldScroll = document.querySelector(".friction-page-scroll");
    const oldScrollTop = keepScroll && oldScroll ? oldScroll.scrollTop : 0;
    const summary = data && data.summary || {};
    const groups = data && Array.isArray(data.groups) ? data.groups : [];
    const groupBy = (data && data.group_by) || view.frictionGroupBy;
    const total = num(summary.total_events) == null ? (view.locale === "en" ? "Not recorded" : "未记录") : summary.total_events;
    const headerSummary = (view.locale === "en" ? String(total) + " explicit events · " + frictionSummaryValue(summary.project_count) + " projects" : String(total) + " 条明确事件 · " + frictionSummaryValue(summary.project_count) + " 个项目");
    const empty = '<div class="empty-copy friction-empty"><strong>' + (view.locale === "en" ? "No explicit friction records found." : "没有检测到明确摩擦记录。") + '</strong><span>' + (view.locale === "en" ? "Only explicit tool errors, non-zero exit codes, user interrupts and asset bypass records are included; missing evidence remains not recorded." : "这里只统计明确的工具错误、非零退出码、用户中断和资产绕行记录；缺失证据保持未记录。") + '</span></div>';
    const rows = groups.length ? groups.map(frictionGroupRow).join("") : empty;
    const more = data && data.pagination && data.pagination.has_more ? '<div class="friction-load-note">' + (view.frictionLoading ? (view.locale === "en" ? "Loading more local groups…" : "正在读取更多本地分组…") : (view.locale === "en" ? "Scroll to load more" : "继续滚动加载更多")) + '</div>' : '';
    const loadingFlag = view.frictionLoading ? '<span class="friction-loading-flag">' + uiText("正在按新条件重新读取…", "Re-reading with the new filters…") + "</span>" : "";
    const title = groupBy === "category" ? (view.locale === "en" ? "By category" : "按类别") : groupBy === "tool" ? (view.locale === "en" ? "By tool" : "按工具") : groupBy === "signature" ? (view.locale === "en" ? "Recurring signatures" : "反复出现的摩擦") : (view.locale === "en" ? "Project × harness" : "项目 × harness");
    const note = (view.locale === "en" ? "Total events are deduplicated by source event; category counts may overlap. " : "总事件按来源事件去重；同一事件可能同时属于多个明确类型。") + frictionToolCoverageNote(summary);
    const windowNote = groupBy === "signature"
      ? '<p class="friction-method-note friction-window-note">' + esc(uiText("生命周期状态按当前筛选的时间范围判定；只出现过一次的签名单独计数。已消失不等于已修复，行内一并给出同项目在当前筛选范围内的会话数。", "Lifecycle status follows the selected time range; signatures seen once are counted separately. Gone quiet is not fixed, so each row also states how many sessions those projects ran in the selected range.")) + "</p>"
      : "";
    const coverage = frictionHintCoverage(summary);
    const mechanismNote = groupBy === "signature"
      ? '<p class="friction-method-note friction-mechanism-note">' + esc(coverage
        ? uiText("机制：" + coverage.covered + "/" + coverage.total + " 条签名已被机制字典覆盖；未覆盖的行不写说明。",
          "Mechanism: the dictionary covers " + coverage.covered + " of " + coverage.total + " signatures; the rows it does not cover carry no sentence.")
        : uiText("机制覆盖率未记录。", "Mechanism coverage is not recorded.")) + "</p>"
      : "";
    const body = frictionToolbar(data, null) + frictionStatRow(summary, false) + frictionHintKindRow(summary) + frictionCoverageGapTable(summary) + '<section class="elevated-card friction-groups-card"><header class="fl-head"><h3>' + esc(title) + '</h3>' + loadingFlag + '<span class="fl-aside">' + frictionArrangeBar(groupBy, null) + '</span></header><p class="friction-method-note" data-no-translate="true">' + esc(note) + '</p>' + mechanismNote + '<div class="friction-table" data-group="' + esc(groupBy) + '">' + frictionTableHead(groupBy) + '<div class="friction-group-list">' + rows + '</div>' + more + '</div>' + (groupBy === "project" || groupBy === "tool" ? frictionKindLegend(groups) : "") + windowNote + '</section>';
    setScreen(header("摩擦", headerSummary, '<button class="us-btn" data-variant="outline" data-size="sm" data-action="reload-friction">' + icon("refreshCw") + (view.locale === "en" ? "Rescan" : "重新扫描") + '</button>') + screenContent(body, "friction-page", "friction-page-scroll"));
    const scroll = document.querySelector(".friction-page-scroll");
    if (scroll) scroll.scrollTop = oldScrollTop;
    localizeDOM();
    armFrictionLazyRows();
  }

  async function loadFrictionDetail(group, reset) {
    const request = ++view.frictionRequest;
    const previous = view.frictionDetail;
    if (reset) view.frictionSelected = 0;
    view.frictionLoading = true;
    if (previous) drawFrictionDetail(previous, group);
    else drawFrictionLoading(true);
    const offset = reset ? 0 : previous && previous.records ? previous.records.length : 0;
    const data = await get(frictionQueryPath(true, group, offset));
    if (request !== view.frictionRequest) return;
    if (reset || !previous) view.frictionDetail = data;
    else view.frictionDetail = Object.assign({}, previous, data, { records: (previous.records || []).concat(data.records || []) });
    view.frictionLoading = false;
    drawFrictionDetail(view.frictionDetail, group);
  }

  async function loadMoreFrictionDetail(group) {
    const data = view.frictionDetail;
    if (!data || view.frictionLoading || !data.pagination || !data.pagination.has_more) return;
    await loadFrictionDetail(group, false);
  }

  function frictionDetailRecordRow(record, index) {
    const tool = frictionRecordToolName(record);
    const toolHTML = tool ? '<strong data-no-translate="true">' + esc(tool) + '</strong>' : '<strong class="data-missing">' + esc(frictionToolLabel("")) + '</strong>';
    const ruleText = daemonProse(record.category_rule, record.category_rule_en);
    const rule = ruleText ? '<small class="friction-record-rule"><span data-no-translate="true">' + esc(ruleText) + "</span>" + daemonCopyFlag(ruleText) + '</small>' : '<small class="friction-record-rule data-missing">' + (view.locale === "en" ? "Rule not recorded" : "规则未记录") + '</small>';
    return '<button type="button" class="friction-record-row" data-key="fr:' + esc(record.source_event_id || record.id || index) + '" data-action="friction-select" data-index="' + index + '" data-selected="' + (index === view.frictionSelected) + '"><time>' + esc(shortDate(record.occurred_at)) + '</time><span class="friction-record-main"><span class="friction-record-kinds">' + frictionCategoryBadge(record.category) + frictionTypeChips(record, true) + '</span>' + toolHTML + rule + '<small>' + esc(frictionRecordEvidence(record)) + '</small><small class="friction-record-session" data-no-translate="true">' + esc(frictionRecordSession(record)) + '</small></span><span class="friction-record-chevron">' + icon("chevronRight") + '</span></button>';
  }

  function frictionInspector(record, index) {
    if (!record) return '<div class="empty-copy friction-inspector-empty"><strong>' + (view.locale === "en" ? "No friction event selected." : "未选择摩擦事件。") + '</strong><span>' + (view.locale === "en" ? "Select a record on the left to inspect its evidence." : "选择左侧记录查看明确证据。") + '</span></div>';
    const tool = frictionRecordToolName(record);
    const sessionHref = frictionSessionHref(record);
    const fields = [
      [view.locale === "en" ? "Event time" : "事件时间", date(record.occurred_at)],
      [view.locale === "en" ? "Category" : "类别", frictionCategoryLabel(record.category)],
      [view.locale === "en" ? "Rule that matched" : "命中的规则", daemonProse(record.category_rule, record.category_rule_en) || (view.locale === "en" ? "Not recorded" : "未记录")],
      [view.locale === "en" ? "Tool" : "工具", tool || frictionToolLabel("")],
      [view.locale === "en" ? "Harness" : "harness", source(record.harness)],
      [view.locale === "en" ? "Session" : "会话", frictionRecordSession(record)],
      [view.locale === "en" ? "Observation" : "观测等级", obs(record.observation_level)],
      ["is_error", record.is_error == null ? (view.locale === "en" ? "Not recorded" : "未记录") : String(record.is_error)],
      ["exit_code", record.exit_code == null ? (view.locale === "en" ? "Not recorded" : "未记录") : String(record.exit_code)]
    ];
    const titleHTML = tool ? '<p class="session-inspector-title" data-no-translate="true">' + esc(tool) + '</p>' : '<p class="session-inspector-title">' + esc(frictionToolLabel("")) + '</p>';
    const linkNote = record.event_id
      ? (view.locale === "en" ? "Opens the session and lands on this event." : "跳转到会话并定位到这条事件。")
      : (view.locale === "en" ? "Event position not recorded; the link only reaches the session." : "事件位置未记录；链接只能定位到会话。");
    return '<div class="session-inspector-kicker"><span class="session-inspector-index">#' + (index + 1) + '</span>' + frictionCategoryBadge(record.category) + frictionTypeChips(record, true) + '</div>' + titleHTML + '<p class="session-inspector-evidence">' + esc(frictionRecordEvidence(record)) + '</p>' + frictionInspectorList(fields) + '<div class="session-inspector-label">' + (view.locale === "en" ? "Locator" : "定位信息") + '</div><pre class="event-payload friction-locator">' + esc(displayValue(record.locator, view.locale === "en" ? "Locator not recorded" : "定位信息未记录")) + '</pre><div class="session-inspector-label">' + (view.locale === "en" ? "Event payload" : "事件载荷") + '</div><pre class="event-payload">' + esc(displayValue(record.payload, view.locale === "en" ? "Payload not recorded" : "事件载荷未记录")) + '</pre><div class="friction-inspector-links">' + (sessionHref ? '<a class="us-btn" data-variant="outline" data-size="sm" href="' + esc(sessionHref) + '">' + icon("arrowRight") + (view.locale === "en" ? "Open session" : "查看会话") + '</a>' : '<span class="data-missing">' + (view.locale === "en" ? "Session link not recorded" : "会话链接未记录") + '</span>') + '<span class="friction-link-note">' + esc(linkNote) + '</span><span class="data-missing" data-no-translate="true">' + esc(record.source_event_id || (view.locale === "en" ? "Source event ID not recorded" : "来源事件 ID 未记录")) + '</span></div>';
  }

  function frictionInspectorList(items) {
    return '<div class="fl-list session-inspector-list">' + items.map(([label, value]) => '<div class="fl-li"><span>' + esc(label) + '</span><b data-no-translate="true">' + esc(value) + '</b></div>').join("") + '</div>';
  }

  function armFrictionDetailLazy(group) {
    const scroll = document.querySelector(".friction-record-scroll");
    if (!scroll) return;
    const check = () => {
      if (scroll.scrollTop + scroll.clientHeight >= scroll.scrollHeight - 220) loadMoreFrictionDetail(group).catch(renderError);
    };
    scroll.addEventListener("scroll", check, { passive: true });
    setTimeout(check, 0);
  }

  function drawFrictionDetail(data, group) {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const actualGroup = data && data.group || group;
    const records = data && Array.isArray(data.records) ? data.records : [];
    const summary = data && data.summary || {};
    const selected = records[view.frictionSelected] || null;
    const headerTitle = frictionGroupProjectLabel(actualGroup) + " · " + source(actualGroup.harness);
    const headerSubline = (actualGroup.cwd || (view.locale === "en" ? "Working directory not recorded" : "工作目录未记录")) + " · " + (view.locale === "en" ? "Project × harness evidence" : "项目 × harness 摩擦证据");
    const backHref = "#/friction?" + frictionFilterParams().toString();
    const detailHeader = '<header class="detail-header friction-detail-header"><a class="back-link" href="' + esc(backHref) + '" aria-label="' + (view.locale === "en" ? "Back to friction" : "返回摩擦") + '">' + icon("arrowLeft") + '</a>' + sourceMark(actualGroup.harness) + '<span class="detail-identity"><span class="detail-title-line"><h1>' + esc(headerTitle) + '</h1></span><span class="detail-subline">' + esc(headerSubline) + '</span></span><span class="detail-header-actions"><span class="fl-flag" data-flag="new">' + icon("triangleAlert") + (view.locale === "en" ? "Recorded" : "已记录") + '</span></span></header>';
    const list = records.length ? records.map(frictionDetailRecordRow).join("") : '<div class="empty-copy friction-empty"><strong>' + (view.locale === "en" ? "No matching friction records." : "没有匹配的摩擦记录。") + '</strong><span>' + (view.locale === "en" ? "Try another type, category, tool or search term." : "请切换类型、类别、工具或更换搜索词。") + '</span></div>';
    const more = data && data.pagination && data.pagination.has_more ? '<div class="friction-load-note">' + (view.frictionLoading ? (view.locale === "en" ? "Loading more records…" : "正在读取更多记录…") : (view.locale === "en" ? "Scroll to load more records" : "继续滚动加载更多记录")) + '</div>' : '';
    const method = (view.locale === "en" ? "All explicit events are deduplicated by source event. Category counts may overlap when one event records more than one explicit signal. " : "全部明确事件按来源事件去重；同一事件记录多个明确信号时，类型统计可能重叠。") + frictionToolCoverageNote(summary);
    const body = frictionToolbar(data, actualGroup) + frictionStatRow(summary, true) + '<p class="friction-method-note friction-detail-method" data-no-translate="true">' + esc(method) + '</p><div class="friction-detail-canvas"><section class="friction-record-pane"><header class="friction-pane-head"><h3>' + (view.locale === "en" ? "Friction records" : "摩擦事件记录") + '</h3><span>' + esc(quantity(data && data.pagination ? data.pagination.total : records.length, "条", "record", "records")) + '</span></header><div class="friction-record-scroll">' + list + more + '</div></section><aside class="session-inspector-pane friction-inspector-pane"><div class="session-inspector-head"><div><h2>' + (view.locale === "en" ? "Evidence inspector" : "证据检查器") + '</h2></div><span>' + (selected ? "#" + (view.frictionSelected + 1) : (view.locale === "en" ? "No selection" : "未选择")) + '</span></div><div class="session-inspector-scroll">' + frictionInspector(selected, view.frictionSelected) + '</div></aside></div>';
    setScreen(detailHeader + screenContent(body, "friction-detail-page", "friction-detail-scroll"));
    localizeDOM();
    armFrictionDetailLazy(actualGroup);
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
    const transition = from || to ? (view.locale === "en" ? " (" + (from || "Not recorded") + " – " + (to || "Not recorded") + ")" : "（" + (from || "未记录") + " – " + (to || "未记录") + "）") : "";
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
  // Real paging: "load more" asks for the next window and appends it, instead
  // of re-requesting the whole window with a larger limit. Records are keyed so
  // a daemon that ignores offset produces no duplicates — and the page says so
  // rather than silently claiming everything is loaded.
  function timelineKeyOf(item) {
    return [item.kind, item.asset_id, item.occurred_at, String(item.evidence || "").slice(0, 80)].join("\x1f");
  }
  async function loadTimeline(more) {
    if (view.timelineLoading) return;
    view.timelineLoading = true;
    const offset = more && Array.isArray(view.timelineItems) ? view.timelineItems.length : 0;
    try {
      const data = await get("/api/v1/timeline?limit=" + TIMELINE_PAGE_SIZE + "&offset=" + offset);
      const page = Array.isArray(data.timeline) ? data.timeline : [];
      view.timelinePageSize = page.length;
      if (!more || !Array.isArray(view.timelineItems)) {
        view.timelineItems = page;
        view.timelineAppended = page.length;
      } else {
        const seen = new Set(view.timelineItems.map(timelineKeyOf));
        const fresh = page.filter((item) => !seen.has(timelineKeyOf(item)));
        view.timelineAppended = fresh.length;
        view.timelineItems = view.timelineItems.concat(fresh);
      }
      const pagination = data.pagination || {};
      view.timelineTotal = num(pagination.total) != null ? num(pagination.total) : num(data.total);
      view.timelineHasMore = typeof pagination.has_more === "boolean" ? pagination.has_more : null;
      if (Array.isArray(data.clusters)) view.timelineClusters = data.clusters;
      view.timelineOffset = offset;
      view.timelineError = "";
      cache.timeline = { timeline: view.timelineItems, clusters: view.timelineClusters, total: view.timelineTotal };
    } catch (error) {
      view.timelineError = error.message || String(error);
      if (!more) { view.timelineItems = null; view.timelineClusters = null; }
    }
    view.timelineLoading = false;
    drawTimeline();
  }
  function drawTimeline() {
    const data = { timeline: view.timelineItems || [], clusters: view.timelineClusters || [] };
    const all = data.timeline || [];
    const assets = Object.fromEntries((cache.assets && cache.assets.assets || []).map((item) => [item.id, item]));
    const filters = [["all", "全部"], ["state_transition", "状态迁移"], ["asset_version", "资产变更"], ["environment_changed", "环境变化"]];
    const items = all.filter((item) => view.timelineFilter === "all" || item.kind === view.timelineFilter);
    const clusters = data.clusters || [];
    const clusterHTML = clusters.map((cluster) => '<div class="fl-cluster"><strong>' + esc(shortDate(cluster.at)) + "</strong> · " + esc(cluster.summary || ((cluster.asset_names || []).length + " 个资产的时间对齐记录")) + "</div>").join("");
    // A first scan, a bulk rescan or a startup re-evaluation writes hundreds of
    // same-instant rows, which used to bury everything else. In the combined
    // view those runs collapse into one factual row — except transitions into
    // an attention state, which stay individual because they are the alarm.
    // The kind filters still show every record individually.
    const attentionState = (item) => ["silent", "broken", "bypassed", "degraded", "awaiting_resurrection"].includes(item.state);
    const collapseSameInstantVersions = (list) => {
      const out = [];
      let run = null;
      const flush = () => { if (run) { out.push(run); run = null; } };
      for (const item of list) {
        const collapsible = item.kind === "asset_version" || item.kind === "environment_changed"
          || (item.kind === "state_transition" && !attentionState(item));
        if (!collapsible) { flush(); out.push(item); continue; }
        if (run && run.kind === item.kind && run.at === item.occurred_at) { run.items.push(item); continue; }
        flush();
        run = { bulk: true, kind: item.kind, at: item.occurred_at, items: [item] };
      }
      flush();
      return out;
    };
    const visible = view.timelineFilter === "all" ? collapseSameInstantVersions(items) : items;
    const nodes = visible.map((entry) => {
      if (entry.bulk && entry.items.length > 1) {
        const kindLabel = entry.kind === "asset_version" ? uiText("资产版本变化", "Asset version change")
          : entry.kind === "environment_changed" ? uiText("环境变化", "Environment change")
            : uiText("状态迁移", "State transition");
        const stateSplit = {};
        entry.items.forEach((item) => { const key = item.state || ""; if (key) stateSplit[key] = (stateSplit[key] || 0) + 1; });
        const names = entry.items.slice(0, 3).map((item) => assets[item.asset_id] ? assets[item.asset_id].name : item.asset_id);
        const rest = entry.items.length - names.length;
        const text = entry.items.length + " 条「" + kindLabel + "」在同一时刻记录（批量快照或评估）" + (rest > 0 ? uiText("，还有 " + rest + " 条未列出", ", " + rest + " more not listed") : "");
        const splitText = Object.keys(stateSplit).map((key) => stateLabel(key) + " " + stateSplit[key]).join(" · ");
        const detail = (splitText ? splitText + uiText("；", "; ") : "") + names.join(" · ") + (rest > 0 ? " …" : "") + uiText("；逐条查看请用对应的筛选。", "; use the kind filters to see each one.");
        return '<article class="fl-node" data-kind="' + (entry.kind === "environment_changed" ? "env" : entry.kind === "asset_version" ? "asset" : "state") + '" data-tone="muted" data-bulk="true"><div class="fl-node-meta"><time>' + esc(shortDate(entry.at)) + '</time><span class="fl-flag" data-flag="neutral">' + icon("package-open") + esc(kindLabel) + '</span></div><div class="fl-node-text">' + esc(text) + '</div><div class="fl-node-detail">' + esc(detail) + "</div></article>";
      }
      const item = entry.items ? entry.items[0] : entry;
      const detail = timelineDetail(item, assets);
      const nodeKind = item.kind === "environment_changed" ? "env" : item.kind === "asset_version" ? "asset" : "state";
      const stateHTML = detail.state ? '<span class="fl-state" data-state="' + esc(detail.state) + '">' + icon(detail.stateIcon) + esc(detail.stateText) + '</span>' : "";
      const linkHTML = detail.href ? '<a class="timeline-node-link" href="' + esc(detail.href) + '">' + esc(detail.link) + "</a>" : "";
      return '<article class="fl-node" data-kind="' + nodeKind + '" data-tone="' + detail.tone + '"><div class="fl-node-meta"><time>' + esc(shortDate(item.occurred_at)) + '</time><span class="fl-flag" data-flag="' + detail.flag + '">' + icon(timelineIcon(item)) + esc(detail.title) + '</span>' + stateHTML + '</div><div class="fl-node-text">' + esc(detail.text) + '</div><div class="fl-node-detail">' + esc(detail.detail) + '</div>' + linkHTML + "</article>";
    }).join("");
    const filtersHTML = '<div class="segmented timeline-filters">' + filters.map((item) => '<button class="segment-btn" type="button" data-action="timeline-filter" data-filter="' + item[0] + '" data-active="' + (view.timelineFilter === item[0]) + '">' + item[1] + "</button>").join("") + '</div>';
    view.timelineTotalLoaded = all.length;
    const total = num(view.timelineTotal);
    const loadedNote = total == null
      ? uiText("已加载 " + all.length + " 条；接口未返回总数。", "Loaded " + all.length + " records; the interface does not return a total.")
      : uiText("已加载 " + all.length + " / " + total + " 条已记录变化。", "Loaded " + all.length + " of " + total + " recorded changes.");
    // A daemon that ignores offset hands back the same window again. That is a
    // contract gap, not "everything is loaded", and it is named as such.
    const pagingIgnored = view.timelineOffset > 0 && view.timelinePageSize > 0 && view.timelineAppended === 0;
    const hasMore = pagingIgnored ? false
      : view.timelineHasMore != null ? view.timelineHasMore
        : total != null ? all.length < total : view.timelinePageSize === TIMELINE_PAGE_SIZE;
    const more = pagingIgnored
      ? '<div class="timeline-more"><span class="timeline-more-note" data-missing="true">' + esc(uiText("接口没有按 offset 分页：offset=" + view.timelineOffset + " 返回的仍是同一批记录，没有新增。", "The interface does not page by offset: offset=" + view.timelineOffset + " returned the same records, so nothing was added.")) + "</span></div>"
      : hasMore
        ? '<div class="timeline-more"><button class="us-btn" data-variant="outline" data-size="sm" data-action="timeline-more"' + (view.timelineLoading ? " disabled" : "") + ">" + icon("chevronDown") + (view.timelineLoading ? uiText("正在读取…", "Loading…") : uiText("加载更多", "Load more")) + '</button><span class="timeline-more-note">' + esc(loadedNote + uiText(" 再点一次追加 " + TIMELINE_PAGE_SIZE + " 条。", " Each click appends " + TIMELINE_PAGE_SIZE + " more.")) + "</span></div>"
        : '<div class="timeline-more"><span class="timeline-more-note">' + esc(loadedNote) + "</span></div>";
    const failure = view.timelineError ? '<div class="empty-copy"><strong>' + uiText("时间线接口未就绪。", "The timeline interface is not ready.") + '</strong><p class="empty-copy-detail" data-no-translate="true">' + esc(view.timelineError) + "</p></div>" : "";
    const body = '<div class="fl-track">' + (clusterHTML || "") + failure + (nodes || (failure ? "" : '<div class="empty-copy"><strong>尚无变化时间线记录。</strong>当前数据没有写入可展示的版本、环境或状态变化。</div>')) + "</div>" + more;
    setScreen(header("变化时间线", quantity(items.length, "条", "record", "records") + " · 本地事实", filtersHTML) + screenContent(body, "timeline-page"));
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
  // display_title is the daemon's one answer for "what do we call this session";
  // title_source says where it came from. §20.10 made the parent its own field
  // (parent_title), so the name carries no arrow and nothing is split back out
  // of it; the parent is shown as its own secondary line.
  const oneLine = (value) => String(value == null ? "" : value).replace(/\s+/g, " ").trim();
  function sessionTitleParts(item) {
    if (!item) return { title: uiText("未记录", "Not recorded"), parent: "", missing: true };
    const display = item.display_title;
    if (display == null || !String(display).trim()) {
      if (item.title_source) return { title: uiText("未记录", "Not recorded"), parent: "", missing: true };
      const legacy = oneLine(item.title || item.task_text);
      return { title: legacy || uiText("未记录", "Not recorded"), parent: "", missing: !legacy };
    }
    // §20.10: the name is only the name. The parent is its own field, so it is
    // read from parent_title rather than split back out of the name.
    return { title: oneLine(display), parent: oneLine(item.parent_title || ""), missing: false };
  }
  function sessionTitle(item) {
    return sessionTitleParts(item).title;
  }
  function sessionTitleHTML(item, extraClass) {
    const parts = sessionTitleParts(item);
    const flag = item && item.title_source === "synthesized"
      ? '<span class="session-title-flag" title="' + esc(uiText("会话本身没有标题；这个名字由子代理身份与父会话合成", "The session records no title; this name is composed from the subagent identity and its parent session")) + '">' + uiText("合成名", "Synthesized") + "</span>"
      : "";
    const parent = parts.parent
      ? '<small class="session-title-parent" data-no-translate="true">' + esc(uiText("父会话：", "Parent: ") + parts.parent) + "</small>"
      : "";
    return '<span class="session-title' + (extraClass ? " " + extraClass : "") + '"' + (parts.missing ? ' data-missing="true"' : "") + '><span class="session-title-text"'
      + (parts.missing ? "" : ' data-no-translate="true"') + ">" + esc(parts.title) + "</span>" + flag + parent + "</span>";
  }
  function sessionTask(item) {
    return (item && (item.task_text || item.title)) || (view.locale === "en" ? "Task text not recorded" : "任务文本未记录");
  }
  function sessionQuery() {
    const params = parseHash().params;
    const sort = params.get("sort");
    const group = params.get("group");
    const thread = params.get("thread");
    const empty = params.get("empty");
    return {
      q: params.get("q") || "",
      deep: params.get("deep") === "1",
      projects: params.getAll("project"),
      harness: params.get("harness") || "",
      from: params.get("from") || isoDay(7),
      to: params.get("to") || "",
      tags: params.getAll("tag"),
      hasFriction: params.get("has_friction") === "1",
      pinned: params.get("pinned") === "1",
      model: params.get("model") || "",
      thread: SESSION_THREADS.includes(thread) ? thread : "main",
      empty: SESSION_EMPTY.includes(empty) ? empty : "0",
      program: params.get("program") || "",
      file: params.get("file") || "",
      role: params.get("role") || "",
      sort: SESSION_SORTS.includes(sort) ? sort : "recent",
      group: SESSION_GROUPS.includes(group) ? group : "none"
    };
  }
  function sessionParams(query) {
    const params = new URLSearchParams();
    if (query.q) params.set("q", query.q);
    if (query.deep) params.set("deep", "1");
    query.projects.forEach((value) => params.append("project", value));
    if (query.harness) params.set("harness", query.harness);
    if (query.from) params.set("from", query.from);
    if (query.to) params.set("to", query.to);
    query.tags.forEach((value) => params.append("tag", value));
    if (query.hasFriction) params.set("has_friction", "1");
    if (query.pinned) params.set("pinned", "1");
    if (query.model) params.set("model", query.model);
    if (query.thread !== "main") params.set("thread", query.thread);
    if (query.empty !== "0") params.set("empty", query.empty);
    if (query.program) params.set("program", query.program);
    if (query.file) params.set("file", query.file);
    if (query.role) params.set("role", query.role);
    if (query.sort !== "recent") params.set("sort", query.sort);
    return params;
  }
  function sessionHash(query) {
    const params = sessionParams(query);
    if (query.group !== "none") params.set("group", query.group);
    const text = params.toString();
    return "#/sessions" + (text ? "?" + text : "");
  }
  function applySessionQuery(patch) {
    const next = Object.assign(sessionQuery(), patch);
    const hash = sessionHash(next);
    if (location.hash === hash) route(true);
    else location.hash = hash;
  }
  function isoDay(offsetDays) {
    return new Date(Date.now() - offsetDays * 86400000).toISOString().slice(0, 10);
  }
  function sessionRangePreset(query) {
    if (query.from === "all" && !query.to) return "all";
    if (!query.from && !query.to) return "7";
    if (query.to) return "custom";
    const match = ["7", "30", "90"].find((days) => query.from === isoDay(Number(days)));
    return match || "custom";
  }
  async function loadSessions(more) {
    const query = sessionQuery();
    const state = view.sessionList;
    const key = sessionHash(query);
    if (!more || state.key !== key) {
      Object.assign(state, { key, items: [], pagination: null, facets: null, loading: false, ready: true, error: "" });
      more = false;
    }
    if (state.loading || (more && !(state.pagination && state.pagination.has_more))) return;
    state.loading = true;
    if (!more) drawSessions();
    const listParams = sessionParams(query);
    listParams.set("limit", String(SESSION_LIST_PAGE_SIZE));
    listParams.set("offset", String(more ? state.items.length : 0));
    try {
      const list = await get(withParams("/api/v1/sessions", listParams));
      if (state.key !== key) return;
      if (!list.pagination) throw new Error("sessions pagination not implemented");
      state.items = (more ? state.items : []).concat(Array.isArray(list.sessions) ? list.sessions : []);
      state.pagination = list.pagination;
      if (!more) state.facets = await get(withParams("/api/v1/sessions/facets", sessionParams(query)));
      state.ready = true;
    } catch (error) {
      state.ready = false;
      state.error = error.message || String(error);
    }
    state.loading = false;
    if (state.key === key && parseHash().path === "/sessions") drawSessions();
  }
  // Subagent sessions are listed with parent=<id>, which ignores the
  // thread/empty defaults, so the expanded rows always match subagent_count.
  async function toggleSubagents(id) {
    const entry = view.sessionChildren[id] || { open: false, items: null, total: null, error: "", loading: false };
    view.sessionChildren[id] = entry;
    if (entry.open) { entry.open = false; drawSessions(); return; }
    entry.open = true;
    if (entry.items) { drawSessions(); return; }
    entry.loading = true;
    drawSessions();
    try {
      const data = await get("/api/v1/sessions?parent=" + encodeURIComponent(id) + "&limit=" + SUBAGENT_PAGE_SIZE);
      entry.items = Array.isArray(data.sessions) ? data.sessions : [];
      entry.total = data.pagination ? num(data.pagination.total) : entry.items.length;
      entry.error = "";
    } catch (error) {
      entry.error = error.message || String(error);
    }
    entry.loading = false;
    drawSessions();
  }
  function facetOptions(list) {
    return Array.isArray(list) ? list : [];
  }
  // Aggregates mark an absent dimension value with their own sentinel key.
  // Render every sentinel as "not recorded" instead of printing it raw.
  function facetLabel(value, fallback) {
    const key = String(value == null ? "" : value);
    return !key || key === "__unrecorded__" || key === "unrecorded" ? fallback : key;
  }
  function sessionActivityBar(facets) {
    const days = Array.isArray(facets && facets.date_histogram) ? facets.date_histogram : [];
    if (!days.length) return '<div class="session-activity" data-empty="true">' + (view.locale === "en" ? "Daily activity not recorded under the current filters." : "当前筛选下按日活动未记录。") + "</div>";
    const maximum = days.reduce((value, day) => Math.max(value, num(day.count) || 0), 1);
    const bars = days.map((day) => {
      const value = num(day.count) || 0;
      const label = day.day + " · " + quantity(value, "个会话", "session", "sessions");
      return '<button type="button" class="session-activity-bar" data-action="session-activity" data-day="' + esc(day.day) + '" style="--h:' + Math.max(3, value / maximum * 100).toFixed(1) + '%" title="' + esc(label) + '" aria-label="' + esc(label) + '"></button>';
    }).join("");
    const first = days[0].day;
    const last = days[days.length - 1].day;
    return '<div class="session-activity"><div class="session-activity-plot">' + bars + '</div><div class="session-activity-legend"><span>' + esc(first) + '</span><span>' + (view.locale === "en" ? "Drag across the bars to pick a range" : "在柱状条上拖选时间范围") + '</span><span>' + esc(last) + "</span></div></div>";
  }
  // The session toolbar follows the same shape as the friction one: search plus
  // one Filters button; only the time range, the scope toggles and the grouping
  // stay outside, because those are the switches used on every visit.
  function sessionScopeCount(entry) {
    return num(entry) == null ? uiText("未记录", "Not recorded") : String(entry);
  }
  function sessionFilterGroups(query, facets) {
    const notRecorded = uiText("未记录", "Not recorded");
    const groups = [];
    groups.push({ key: "project", label: uiText("项目", "Project"), value: query.projects[0] || "", options: [{ value: "", label: uiText("全部项目", "All projects") }].concat(facetOptions(facets && facets.projects).map((project) => ({ value: project.key, label: projectLabelOf(project) + " · " + count(project.count) }))) });
    groups.push({ key: "harness", label: "harness", value: query.harness, options: [{ value: "", label: uiText("全部 harness", "All harnesses") }].concat(facetOptions(facets && facets.harnesses).map((entry) => ({ value: entry.key, label: source(entry.key) + " · " + count(entry.count) }))) });
    groups.push({ key: "tag", label: uiText("标签", "Tag"), value: query.tags[0] || "", options: [{ value: "", label: uiText("全部标签", "All tags") }].concat(facetOptions(facets && facets.tags).map((entry) => ({ value: entry.tag, label: entry.tag + " · " + count(entry.count) }))) });
    groups.push({ key: "model", label: uiText("模型", "Model"), value: query.model, options: [{ value: "", label: uiText("全部模型", "All models") }].concat(facetOptions(facets && facets.models).map((entry) => ({ value: entry.key, label: facetLabel(entry.key, notRecorded) + " · " + count(entry.count) }))) });
    // facets.programs[].count is the number of sessions the program appeared
    // in, not the number of calls; the label says which.
    groups.push({ key: "program", label: uiText("命令", "Command"), value: query.program, options: [{ value: "", label: uiText("全部命令", "All commands") }].concat(facetOptions(facets && facets.programs).map((entry) => ({ value: entry.key, label: facetLabel(entry.key, notRecorded) + " · " + (num(entry.count) == null ? notRecorded : uiText(entry.count + " 个会话", entry.count + " sessions")) }))) });
    const roles = facetOptions(facets && facets.roles);
    if (roles.length) groups.push({ key: "role", label: uiText("子代理角色", "Subagent role"), value: query.role, options: [{ value: "", label: uiText("全部角色", "All roles") }].concat(roles.map((entry) => ({ value: entry.key, label: facetLabel(entry.key, uiText("角色未记录", "Role not recorded")) + " · " + count(entry.count) }))) });
    return groups;
  }
  function sessionActiveChips(query, facets) {
    const chips = [];
    const clear = (dimension, label) => ({ action: "session-clear-" + dimension, label });
    if (query.projects[0]) chips.push(chipControl(uiText("项目：", "Project: ") + query.projects[0], clear("project", uiText("清除项目筛选", "Clear project filter"))));
    if (query.harness) chips.push(chipControl("harness: " + source(query.harness), clear("harness", uiText("清除 harness 筛选", "Clear harness filter"))));
    if (query.tags[0]) chips.push(chipControl(uiText("标签：", "Tag: ") + query.tags[0], clear("tag", uiText("清除标签筛选", "Clear tag filter"))));
    if (query.model) chips.push(chipControl(uiText("模型：", "Model: ") + query.model, clear("model", uiText("清除模型筛选", "Clear model filter"))));
    if (query.program) chips.push(chipControl(uiText("命令：", "Command: ") + query.program, clear("program", uiText("清除命令筛选", "Clear command filter"))));
    if (query.role) chips.push(chipControl(uiText("角色：", "Role: ") + query.role, clear("role", uiText("清除角色筛选", "Clear role filter"))));
    if (query.file) chips.push(chipControl(uiText("文件：", "File: ") + query.file, clear("file", uiText("清除文件筛选", "Clear file filter"))));
    if (query.hasFriction) chips.push(chipControl(uiText("仅有摩擦", "With friction only"), clear("friction", uiText("清除摩擦筛选", "Clear friction filter"))));
    if (query.pinned) chips.push(chipControl(uiText("仅置顶", "Pinned only"), clear("pinned", uiText("清除置顶筛选", "Clear pinned filter"))));
    const unrecordedThreads = facetOptions(facets && facets.threads).find((entry) => entry.key === "__unrecorded__" || !entry.key);
    const note = unrecordedThreads && num(unrecordedThreads.count)
      ? '<span class="fl-chip-note">' + uiText("另有 " + unrecordedThreads.count + " 个会话的层级未记录，随「含子代理会话」一起显示。", unrecordedThreads.count + " sessions have no recorded thread level; they appear with the subagent toggle.") + "</span>"
      : "";
    if (!chips.length) return note ? '<div class="fl-chip-row">' + note + "</div>" : "";
    return '<div class="fl-chip-row"><span class="fl-chip-row-label">' + uiText("当前筛选", "Active filters") + "</span>" + chips.join("") + '<button type="button" class="fl-chip-clear-all" data-action="session-clear-all">' + uiText("清空全部", "Clear all") + "</button>" + note + "</div>";
  }
  function sessionActiveFilterCount(query) {
    return [query.projects[0], query.harness, query.tags[0], query.model, query.program, query.role].filter(Boolean).length;
  }
  function sessionFilters(query, facets) {
    const rangeOptions = [
      { value: "7", label: uiText("近 7 天", "7 days") },
      { value: "30", label: uiText("近 30 天", "30 days") },
      { value: "90", label: uiText("近 90 天", "90 days") },
      { value: "all", label: uiText("全部", "All") }
    ];
    const preset = sessionRangePreset(query);
    const rangeSegment = segmentControl("session-range", "range", rangeOptions, preset === "custom" ? "" : preset)
      + dateRangeControl("session-range", query.from === "all" ? "" : query.from, query.to, uiText("自定义时间范围", "Custom date range"));
    const groupOptions = [
      { value: "none", label: uiText("不分组", "None") },
      { value: "project", label: uiText("项目", "Project") },
      { value: "day", label: uiText("日", "Day") },
      { value: "week", label: uiText("周", "Week") },
      { value: "role", label: uiText("角色", "Role") }
    ];
    // tokens / lines_changed / active sort on session_usage (§20.4). A session
    // with no measurement row is ordered last by the daemon rather than being
    // treated as zero, so the tail of these lists is "not recorded", not "0".
    const sortLabels = { recent: ["最近开始", "Most recent"], oldest: ["最早开始", "Oldest first"], duration: ["时长最长", "Longest duration"], events: ["事件最多", "Most events"], friction: ["摩擦最多", "Most friction"], tool_calls: ["工具调用最多", "Most tool calls"], tokens: ["工作 token 最多", "Most work tokens"], lines_changed: ["改动行最多", "Most changed lines"], active: ["活跃时长最长", "Longest active time"] };
    const sortOptions = SESSION_SORTS.map((value) => ({ value, label: uiText(sortLabels[value][0], sortLabels[value][1]) }));
    const threads = facetOptions(facets && facets.threads);
    const subagentFacet = threads.find((entry) => entry.key === "subagent");
    const emptyFacet = facets && facets.empty ? facets.empty : null;
    const friction = facets && facets.friction ? facets.friction : null;
    // Every toggle carries its own facet count in the same shape, so the four
    // stay one line wide in both languages.
    const toggleLabel = (zh, en, value) => uiText(zh, en) + " (" + sessionScopeCount(value) + ")";
    const subagentLabel = toggleLabel("含子代理会话", "Subagents", subagentFacet && subagentFacet.count);
    const emptyLabel = toggleLabel("含空会话", "Empty", emptyFacet && emptyFacet.yes);
    const frictionLabel = toggleLabel("仅有摩擦", "Friction only", friction && friction.with);
    const pinnedLabel = toggleLabel("仅置顶", "Pinned only", facets && facets.pinned);
    const search = '<label class="fl-search"><span class="sr-only">' + uiText("搜索标题、任务、目录或模型", "Search title, task, directory or model") + "</span>" + icon("search", "fl-search-icon") + '<input type="search" data-session-list-search placeholder="' + uiText("搜索：标题 / 任务 / 目录 / 模型", "Search: title / task / directory / model") + '" value="' + esc(query.q) + '"></label>';
    const deep = checkControl("session-deep", uiText("同时搜正文", "Also search transcript text"), query.deep);
    const filterBar = '<div class="fl-filter-bar">' + search + filterControl("session-filters", sessionFilterGroups(query, facets), sessionActiveFilterCount(query)) + deep + "</div>";
    // Three groups, not eleven controls. The row wraps between groups, so a
    // longer English label never drops the sort control onto a line by itself.
    const scopeRow = '<div class="fl-scope-row">'
      + '<span class="fl-scope-group">' + rangeSegment + "</span>"
      + '<span class="fl-scope-group">'
      + '<button type="button" class="us-toggle" data-action="session-thread-toggle" data-pressed="' + (query.thread !== "main") + '">' + icon("git-commit-horizontal") + esc(subagentLabel) + "</button>"
      + '<button type="button" class="us-toggle" data-action="session-empty-toggle" data-pressed="' + (query.empty !== "0") + '">' + icon("circle-slash") + esc(emptyLabel) + "</button>"
      + '<button type="button" class="us-toggle" data-action="session-friction-only" data-pressed="' + query.hasFriction + '">' + icon("triangle-alert") + esc(frictionLabel) + "</button>"
      + '<button type="button" class="us-toggle" data-action="session-pinned-only" data-pressed="' + query.pinned + '">' + esc(pinnedLabel) + "</button>"
      + "</span>"
      + '<span class="fl-spacer"></span><span class="fl-scope-group"><span class="fl-arrange-label">' + uiText("分组", "Group") + "</span>" + segmentControl("session-group", "group", groupOptions, query.group)
      + selectControl("session-sort", uiText("会话排序", "Sort sessions"), sortOptions, query.sort, { searchable: false, size: "sm" }) + "</span></div>";
    return filterBar + sessionActiveChips(query, facets) + scopeRow;
  }
  function sessionDuration(item) {
    const value = num(item && item.duration_ms);
    if (value == null) return compactDuration(item && item.started_at, item && item.ended_at);
    return compactDuration(new Date(0).toISOString(), new Date(value).toISOString());
  }
  // §20.4: every measured field is null when the source recorded nothing. A
  // null reads as "not recorded"; only a recorded zero is printed as 0.
  function usageOf(item) { return (item && item.usage) || {}; }
  // §25.5: the daemon states in one sentence what a token total counted, and
  // it is the same sentence wherever a token number appears. The page keeps
  // the last one it was told and hangs it on every token number, so a reader
  // never has to guess whether cached input is inside the total.
  function noteUsageDefinition(usage) {
    if (usage && usage.definition) view.usageDefinition = String(usage.definition);
    if (usage && usage.definition_en) view.usageDefinitionEN = String(usage.definition_en);
  }
  function tokenTitle(extra) {
    return [extra, daemonProse(view.usageDefinition, view.usageDefinitionEN)].filter(Boolean).join("\n");
  }
  function tokenText(value) {
    const amount = num(value);
    if (amount == null) return count(value);
    const trim = (text) => text.replace(/\.0$/, "");
    if (amount >= 1e9) return trim((amount / 1e9).toFixed(1)) + "B";
    if (amount >= 1e6) return trim((amount / 1e6).toFixed(1)) + "M";
    if (amount >= 1e3) return trim((amount / 1e3).toFixed(1)) + "K";
    return String(amount);
  }
  function durationText(value) {
    const amount = num(value);
    if (amount == null) return count(value);
    return compactDuration(new Date(0).toISOString(), new Date(amount).toISOString());
  }
  // "+195 −30" is one fact with two halves. When only one half was recorded the
  // other says so rather than printing a zero it never measured.
  function linesChangedText(added, removed) {
    const plus = num(added);
    const minus = num(removed);
    if (plus == null && minus == null) return count(null);
    return (plus == null ? count(null) : "+" + plus) + " " + (minus == null ? count(null) : "−" + minus);
  }
  const USAGE_SOURCE_LABELS = {
    claude_usage: ["Claude 转写 message.usage", "Claude transcript message.usage"],
    codex_token_count: ["Codex token_count 事件", "Codex token_count events"],
    opencode_session: ["opencode session 行", "opencode session row"],
    dsh_message_usage: ["dsh 消息 usage", "dsh message usage"],
    unrecorded: ["原文没有度量记录", "The transcript records no measurement"]
  };
  function usageSourceText(value) {
    const copy = USAGE_SOURCE_LABELS[value];
    if (copy) return uiText(copy[0], copy[1]);
    return value ? String(value) : count(null);
  }
  // The session row's third line is the measurement. It carries token, changed
  // lines and active time; expected_exit_count is a friction-page caliber and
  // is deliberately not repeated here.
  // workTokensOf is ADR-25's cost-shaped number, computed from the recorded
  // components: null when none of them was recorded, never zero.
  function workTokensOf(usage) {
    if (num(usage.input_tokens) == null && num(usage.output_tokens) == null && num(usage.cache_write_tokens) == null) return null;
    return (num(usage.input_tokens) || 0) + (num(usage.output_tokens) || 0) + (num(usage.cache_write_tokens) || 0);
  }
  function sessionUsageLine(item) {
    const usage = usageOf(item);
    const parts = [
      uiText("工作 token " + tokenText(workTokensOf(usage)), tokenText(workTokensOf(usage)) + " work tokens"),
      uiText("改动 " + linesChangedText(usage.lines_added, usage.lines_removed), "lines " + linesChangedText(usage.lines_added, usage.lines_removed)),
      uiText("活跃 " + durationText(usage.active_ms), "active " + durationText(usage.active_ms))
    ];
    return '<span class="session-item-usage" data-source="' + esc(usage.source || "unrecorded") + '" title="' + esc(tokenTitle(uiText("度量来源：", "Measured from: ") + usageSourceText(usage.source))) + '">' + esc(parts.join(" · ")) + "</span>";
  }
  // The row already names the project on its second line, so a workspace-*
  // rule tag would repeat it; the row keeps at most three chips and folds the
  // rest into one "+N" that names them on hover.
  const SESSION_ROW_TAG_LIMIT = 3;
  const isWorkspaceTag = (entry) => Boolean(entry) && (entry.kind === "workspace" || /^workspace-/.test(String(entry.tag || "")));
  function sessionTagChips(item, compact) {
    const all = Array.isArray(item && item.tags) ? item.tags : [];
    const tags = compact ? all.filter((entry) => !isWorkspaceTag(entry)) : all;
    const shown = compact ? tags.slice(0, SESSION_ROW_TAG_LIMIT) : tags;
    const hidden = tags.slice(shown.length);
    const chips = shown.map((entry) => entry.kind === "user"
      ? '<span class="session-tag" data-kind="user">' + esc(entry.tag) + '<button type="button" data-action="session-tag-remove" data-session-id="' + esc(item.id) + '" data-tag="' + esc(entry.tag) + '" aria-label="' + uiText("删除标签", "Remove tag") + '">' + icon("x") + "</button></span>"
      : '<span class="session-tag" data-kind="' + esc(entry.kind || "task") + '">' + esc(entry.tag) + "</span>").join("");
    const overflow = hidden.length
      ? '<span class="session-tag" data-kind="more" title="' + esc(hidden.map((entry) => entry.tag).join(" · ")) + '">+' + hidden.length + "</span>"
      : "";
    const editor = view.sessionTagEditor === item.id
      ? '<input class="session-tag-input" type="text" data-session-tag-input data-session-id="' + esc(item.id) + '" placeholder="' + uiText("新标签，回车添加", "New tag, press Enter") + '" aria-label="' + uiText("添加用户标签", "Add user tag") + '">'
      : '<button type="button" class="session-tag-add" data-action="session-tag-editor" data-session-id="' + esc(item.id) + '" aria-label="' + uiText("添加用户标签", "Add user tag") + '" title="' + uiText("添加用户标签", "Add user tag") + '">' + icon("plus") + "</button>";
    // The detail page has room for the strip to name itself; the list row does
    // not, and its chips already read as tags.
    const marker = compact ? "" : '<span class="session-tag-marker" title="' + uiText("标签", "Tags") + '">' + icon("tag") + "</span>";
    return '<span class="session-item-tags"' + (compact ? ' data-compact="true"' : "") + ">" + marker + chips + overflow + editor + "</span>";
  }
  function sessionPinButton(item, pinned) {
    const label = pinned ? uiText("取消置顶", "Unpin") : uiText("置顶", "Pin");
    return '<button type="button" class="session-item-pin" data-action="session-pin" data-session-id="' + esc(item.id) + '" data-pinned="' + Boolean(pinned) + '" aria-pressed="' + Boolean(pinned) + '" aria-label="' + esc(label) + '" title="' + esc(label) + '">' + icon("pin") + "</button>";
  }
  // §25.6: in_progress is a reading of the file, not a statement from the
  // harness. It means the newest transcript for this session had been written
  // within the last ten minutes as of the last time the daemon read it — no
  // source records "the session is still open". Everything measured from a
  // transcript that is still growing is still growing with it, which is why
  // the badge exists at all: so a turn count of 0/1 or an active time close to
  // the total duration reads as "not finished yet", not as a wrong number.
  const IN_PROGRESS_NOTE = [
    "上次读到时这个会话的转写文件在 10 分钟内被写过。轮次、活跃时长、token、改动行都还会变。没有任何来源记录“会话还开着”，这只是文件刚被写过的读法。",
    "The newest transcript for this session had been written within the last ten minutes when the daemon last read it. Turns, active time, tokens and changed lines are all still growing. No source records \u201cthe session is still open\u201d; this is a reading of the file, not a statement from the tool."
  ];
  function inProgressBadge(item) {
    if (!item || item.in_progress !== true) return "";
    return '<span class="session-progress-badge" title="' + esc(uiText(IN_PROGRESS_NOTE[0], IN_PROGRESS_NOTE[1])) + '">' + icon("activity") + uiText("进行中", "In progress") + "</span>";
  }
  // Where this session was read from. The source label is printed only when the
  // user renamed the root: the default label repeats the harness the brand mark
  // already shows, and a line that says the same thing twice is noise. The
  // machine label and the worktree are printed whenever they exist, because
  // both are unrecorded until someone or something records them.
  function sessionOriginParts(item) {
    if (!item) return [];
    const parts = [];
    const label = item.source_label;
    if (label && String(label) !== source(item.source)) parts.push(uiText("来源 " + label, "Source " + label));
    if (item.machine_label) parts.push(uiText("机器 " + item.machine_label, "Machine " + item.machine_label));
    if (item.worktree) parts.push("worktree: " + item.worktree);
    return parts;
  }
  function sessionOriginLine(item) {
    const parts = sessionOriginParts(item);
    if (!parts.length) return "";
    return '<span class="session-item-origin" data-no-translate="true" title="' + esc(uiText("worktree 是 harness 为一次任务开的临时 checkout，已折叠回仓库根；来源与机器标签在数据页登记。", "A worktree is a temporary checkout the harness opened for one task, folded back onto the repository root; the source and machine labels are registered on the data page.")) + '">' + esc(parts.join(" · ")) + "</span>";
  }
  // A subagent row states its role and nickname; an absent value stays
  // "not recorded" instead of borrowing the parent's identity.
  function subagentBadge(item) {
    if (!item || item.thread_kind !== "subagent") return "";
    const role = item.agent_role || (view.locale === "en" ? "Role not recorded" : "角色未记录");
    const nickname = item.agent_nickname || (view.locale === "en" ? "Nickname not recorded" : "昵称未记录");
    return '<span class="session-role-badge" data-no-translate="true">' + icon("git-commit-horizontal") + esc(role + " · " + nickname) + "</span>";
  }
  function subagentToggle(item) {
    const total = num(item && item.subagent_count);
    if (!total) return "";
    const open = Boolean(view.sessionChildren[item.id] && view.sessionChildren[item.id].open);
    const label = uiText("子代理 " + total, total + " subagents");
    return '<button type="button" class="session-subagent-toggle" data-action="session-subagents" data-session-id="' + esc(item.id) + '" data-open="' + open + '" aria-expanded="' + open + '">' + esc(label) + "</button>";
  }
  function subagentPanel(item) {
    const entry = view.sessionChildren[item.id];
    if (!entry || !entry.open) return "";
    if (entry.error) return '<div class="session-subagent-panel"><div class="empty-copy"><strong>' + uiText("子会话接口未就绪。", "The subagent interface is not ready.") + '</strong><span data-no-translate="true">' + esc(entry.error) + "</span></div></div>";
    if (entry.loading && !entry.items) return '<div class="session-subagent-panel"><div class="session-list-loading">' + uiText("正在读取子会话…", "Reading subagent sessions…") + "</div></div>";
    const items = Array.isArray(entry.items) ? entry.items : [];
    if (!items.length) {
      // Claude Code keeps sidechain records inside the parent session, so
      // subagent_count there counts distinct agent_id values, not child rows.
      const merged = item.source === "claude_code";
      const copy = merged
        ? uiText("这个来源把子代理记录并入父会话；子代理计数来自事件里的 agent_id，没有单独的子会话行。", "This source keeps subagent records inside the parent session; the subagent count comes from agent_id in the events, and there are no separate child rows.")
        : uiText("没有记录到单独的子会话行。", "No separate subagent session row was recorded.");
      return '<div class="session-subagent-panel"><div class="session-list-loading">' + copy + "</div></div>";
    }
    const note = num(entry.total) != null && entry.total > items.length ? '<div class="session-subagent-note">' + uiText("已展开 " + items.length + " / " + entry.total + " 个子会话", "Showing " + items.length + " of " + entry.total + " subagent sessions") + "</div>" : "";
    return '<div class="session-subagent-panel">' + items.map(sessionItemHTML).join("") + note + "</div>";
  }
  function sessionItemHTML(item) {
    const projectLabel = item.project_label || (view.locale === "en" ? "Project not recorded" : "项目未记录");
    const calls = num(item.tool_call_count);
    const meta = [projectLabel, shortDate(item.started_at), sessionDuration(item), calls == null ? uiText("工具调用未记录", "Tool calls not recorded") : quantity(calls, "调用", "call", "calls")].join(" · ");
    const friction = num(item.friction_count);
    const frictionFlag = friction ? '<a class="session-item-friction" href="#/sessions/' + encodeURIComponent(item.id) + '?pane=friction" title="' + esc(uiText("查看该会话的摩擦轴", "Open this session's friction axis")) + '">' + icon("triangle-alert") + "<span>" + friction + "</span></a>" : "";
    const note = item.note_preview ? '<span class="session-item-note" title="' + esc(String(item.note_preview).slice(0, 80)) + '">' + icon("file-text") + "</span>" : "";
    const snippet = item.match_snippet ? '<span class="session-item-snippet">' + esc(compactEvidence(item.match_snippet, 160)) + (num(item.match_count) == null ? "" : " · " + quantity(item.match_count, "处匹配", "match", "matches")) + "</span>" : "";
    const row = '<div class="session-item" data-key="session:' + esc(item.id) + '" data-pinned="' + Boolean(item.pinned) + '" data-thread="' + esc(item.thread_kind || "unrecorded") + '" data-session-id="' + esc(item.id) + '">' + sourceMark(item.source) + '<a class="session-item-body" href="#/sessions/' + encodeURIComponent(item.id) + '">' + sessionTitleHTML(item) + '<span class="session-item-meta">' + esc(meta) + "</span>" + sessionUsageLine(item) + sessionOriginLine(item) + snippet + "</a>" + inProgressBadge(item) + subagentBadge(item) + sessionTagChips(item, true) + '<span class="session-item-actions">' + subagentToggle(item) + frictionFlag + note + sessionPinButton(item, item.pinned) + "</span></div>";
    return row + subagentPanel(item);
  }
  function sessionGroupLabel(item, group) {
    if (group === "project") return item.project_label || (view.locale === "en" ? "Project not recorded" : "项目未记录");
    if (group === "role") return item.agent_role || (item.thread_kind === "main" ? (view.locale === "en" ? "Main session" : "主会话") : (view.locale === "en" ? "Role not recorded" : "角色未记录"));
    const started = String(item.started_at || "");
    if (!started) return view.locale === "en" ? "Start time not recorded" : "开始时间未记录";
    if (group === "day") return started.slice(0, 10);
    const day = new Date(started);
    if (!Number.isFinite(day.getTime())) return view.locale === "en" ? "Start time not recorded" : "开始时间未记录";
    const monday = new Date(day.getTime() - ((day.getUTCDay() + 6) % 7) * 86400000);
    return monday.toISOString().slice(0, 10) + (view.locale === "en" ? " week" : " 当周");
  }
  function sessionGroupedMarkup(items, group) {
    if (group === "none") return sessionLazyMarkup(items, sessionItemHTML, 64);
    const buckets = [];
    const byLabel = new Map();
    items.forEach((item) => {
      const label = sessionGroupLabel(item, group);
      let bucket = byLabel.get(label);
      if (!bucket) { bucket = { label, items: [] }; byLabel.set(label, bucket); buckets.push(bucket); }
      bucket.items.push(item);
    });
    return sessionLazyMarkup(buckets, (bucket) => '<div class="session-group-head" data-key="group:' + esc(bucket.label) + '"><span>' + esc(bucket.label) + '</span><span class="session-group-line"></span><span>' + esc(quantity(bucket.items.length, "个会话", "session", "sessions")) + "</span></div>" + bucket.items.map(sessionItemHTML).join(""), 64, (bucket) => bucket.items.length + 1);
  }
  function drawSessions() {
    resetSessionLazyRows();
    const query = sessionQuery();
    const state = view.sessionList;
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const headerRight = '<span class="session-list-count">' + esc(state.pagination && num(state.pagination.total) != null ? quantity(state.pagination.total, "个会话", "session", "sessions") : (view.locale === "en" ? "Not recorded" : "未记录")) + '</span><button class="us-btn" data-variant="default" data-size="sm" data-action="reload-sessions">' + icon("refreshCw") + uiText("重新扫描", "Rescan") + "</button>";
    if (!state.ready) {
      setScreen(header("会话", uiText("查找与管理会话", "Find and manage sessions"), "") + screenContent('<section class="elevated-card card-pad"><div class="empty-copy"><strong>' + uiText("会话管理接口未就绪。", "The session management interface is not ready.") + '</strong><span>' + uiText("daemon 尚未提供服务端筛选、分面与分页；这里不显示未经筛选的替代数据。", "The daemon does not yet serve server-side filtering, facets and pagination; no unfiltered substitute data is shown here.") + '</span><p class="empty-copy-detail">' + esc(state.error) + '</p><button class="us-btn" data-variant="outline" data-size="sm" data-action="reload-sessions">' + icon("refreshCw") + uiText("重试", "Retry") + "</button></div></section>", "session-page"));
      localizeDOM();
      return;
    }
    // While a new filter loads, keep the list's shape with skeleton rows rather
    // than collapsing to a single line of text.
    const loadingRow = state.loading && !state.items.length ? '<div class="session-list-loading">' + uiText("正在读取本地会话…", "Reading local sessions…") + "</div>" + skeletonRows(10, 44) : "";
    const empty = '<div class="empty-copy"><strong>' + uiText("当前筛选下没有会话记录。", "No sessions match the current filters.") + '</strong><span>' + uiText("请更换项目、harness、时间范围或标签，或清空搜索。", "Change the project, harness, time range or tag, or clear the search.") + "</span></div>";
    const rows = state.items.length ? sessionGroupedMarkup(state.items, query.group) : (loadingRow || empty);
    const sentinel = state.pagination && state.pagination.has_more ? '<div class="session-page-sentinel" data-session-page-sentinel>' + uiText("继续滚动读取下一页会话", "Scroll to load the next page of sessions") + "</div>" : "";
    const body = '<div class="session-toolbar">' + sessionFilters(query, state.facets) + sessionActivityBar(state.facets) + '</div><div class="session-list-scroll"><div class="session-list">' + rows + "</div>" + sentinel + "</div>";
    const previousScroll = document.querySelector(".session-list-scroll");
    const previousTop = previousScroll ? previousScroll.scrollTop : 0;
    setScreen(header("会话", uiText("查找与管理会话", "Find and manage sessions"), headerRight) + screenContent(body, "session-page", "session-page-scroll"));
    const nextScroll = document.querySelector(".session-list-scroll");
    if (nextScroll && previousTop) nextScroll.scrollTop = previousTop;
    localizeDOM();
    armSessionLazyRows();
    const editor = screen.querySelector("[data-session-tag-input]");
    if (editor) editor.focus();
  }
  function annotationTargets(id) {
    const detail = view.sessionData && view.sessionData.session && view.sessionData.session.id === id ? view.sessionData.session : null;
    return [view.sessionList.items.find((item) => item.id === id), detail].filter(Boolean);
  }
  function userTagsOf(id) {
    const target = annotationTargets(id)[0];
    return (Array.isArray(target && target.tags) ? target.tags : []).filter((entry) => entry.kind === "user").map((entry) => entry.tag);
  }
  function redrawAnnotated() {
    if (parseHash().path === "/sessions") drawSessions();
    else if (view.sessionData) drawSessionDetail(view.sessionData);
  }
  async function saveAnnotation(id, body, message) {
    try {
      const result = await put("/api/v1/sessions/" + encodeURIComponent(id) + "/annotation", body);
      annotationTargets(id).forEach((target) => {
        if (result.annotation) {
          target.pinned = Boolean(result.annotation.pinned);
          target.note_preview = result.annotation.note || null;
          target.annotation = result.annotation;
        }
        if (Array.isArray(result.tags)) target.tags = result.tags;
      });
      notify(message, "success");
      redrawAnnotated();
    } catch (error) {
      notify(uiText("标注未写入：", "Annotation was not written: ") + error.message, "error");
    }
  }
  function togglePin(id, pinned) {
    return saveAnnotation(id, { pinned }, uiText(pinned ? "已记录置顶；源文件未改变。" : "已取消置顶；源文件未改变。", pinned ? "Pin recorded; source file unchanged." : "Pin removed; source file unchanged."));
  }
  function addSessionTag(id, tag) {
    const tags = userTagsOf(id);
    if (!tag || tags.includes(tag)) return Promise.resolve();
    return saveAnnotation(id, { tags: tags.concat([tag]) }, uiText("已记录标签；源文件未改变。", "Tag recorded; source file unchanged."));
  }
  function removeSessionTag(id, tag) {
    return saveAnnotation(id, { tags: userTagsOf(id).filter((value) => value !== tag) }, uiText("已删除标签；源文件未改变。", "Tag removed; source file unchanged."));
  }
  function saveSessionNote(id, note) {
    return saveAnnotation(id, { note }, uiText("已记录笔记；源文件未改变。", "Note recorded; source file unchanged."));
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
    const listRoute = parseHash().path === "/sessions";
    const root = listRoute ? document.querySelector(".session-list-scroll") : document.querySelector(view.sessionTab === "chat" ? ".session-chat-scroll" : ".session-event-scroll");
    if (!root) return;
    const loadNextPage = () => (listRoute ? loadSessions(true) : loadNextSessionPage());
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
          if (target.hasAttribute("data-session-page-sentinel")) loadNextPage();
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
      if (root.scrollTop + root.clientHeight >= root.scrollHeight - 720) loadNextPage();
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

  // Claude Code history often has no paired tool result, so exit_code is
  // missing. Missing is "not recorded", never a green success: only an explicit
  // is_error=true is treated as failure evidence on its own.
  function exitCodeBadge(command) {
    const code = num(command && command.exit_code);
    if (code != null) return '<span class="exit-badge" data-tone="' + (code === 0 ? "good" : "bad") + '">exit ' + code + "</span>";
    if (command && command.is_error === true) return '<span class="exit-badge" data-tone="bad">is_error=true</span>';
    return '<span class="exit-badge" data-tone="muted">' + uiText("退出码未记录", "Exit code not recorded") + "</span>";
  }
  function commandFailed(command) {
    const code = num(command && command.exit_code);
    return Boolean(command && command.is_error === true) || (code != null && code !== 0);
  }
  function sessionCommandsTable(data) {
    const commands = Array.isArray(data.commands) ? data.commands : null;
    if (!commands) {
      return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("命令", "Commands") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + uiText("命令投影接口未就绪。", "The command projection interface is not ready.") + '</strong><span>' + uiText("daemon 尚未在会话详情里返回 commands；这里不用事件流猜测命令。", "The daemon does not yet return commands in the session detail; commands are not guessed from the event stream here.") + "</span></div></section>";
    }
    const programs = [...new Set(commands.map((entry) => entry.program).filter(Boolean))].sort();
    const programOptions = [{ value: "all", label: uiText("全部程序", "All programs") }].concat(programs.map((value) => ({ value, label: value })));
    const filtered = commands.filter((entry) => (view.sessionCommandProgram === "all" || entry.program === view.sessionCommandProgram) && (!view.sessionCommandFailedOnly || commandFailed(entry)));
    const total = num(data.commands_total);
    const aside = total != null && total > commands.length
      ? uiText("已加载 " + commands.length + " / " + total + " 条", "Loaded " + commands.length + " of " + total)
      : quantity(commands.length, "条", "record", "records");
    const rows = filtered.map((entry) => '<button type="button" class="command-row" data-key="cmd:' + esc(entry.event_id == null ? entry.command : entry.event_id) + '" data-action="session-locate-event" data-event-id="' + esc(entry.event_id == null ? "" : entry.event_id) + '"><span class="command-program" data-no-translate="true">' + esc(entry.program || uiText("程序未记录", "Program not recorded")) + '</span><code class="command-text" data-no-translate="true">' + esc(entry.command || uiText("命令未记录", "Command not recorded")) + "</code>" + exitCodeBadge(entry) + '<time>' + esc(shortDate(entry.occurred_at)) + "</time></button>").join("");
    const controls = '<div class="command-controls">' + selectControl("session-command-program", uiText("按程序筛选", "Filter by program"), programOptions, view.sessionCommandProgram) + '<button type="button" class="us-toggle" data-action="session-command-failed" data-pressed="' + view.sessionCommandFailedOnly + '">' + icon("triangle-alert") + uiText("只看失败", "Failures only") + "</button></div>";
    const body = rows
      ? '<div class="command-table"><div class="command-table-head"><span>' + uiText("程序", "Program") + '</span><span>' + uiText("命令", "Command") + '</span><span>' + uiText("退出码", "Exit code") + '</span><span>' + uiText("时间", "Time") + "</span></div>" + rows + "</div>"
      : '<div class="empty-copy"><strong>' + uiText("当前筛选下没有命令记录。", "No command matches the current filter.") + "</strong></div>";
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("命令", "Commands") + '</h3><span class="fl-aside">' + esc(aside) + "</span></header>" + controls + body + '<p class="evidence-note">' + uiText("点击一行定位到会话里的那条事件。退出码来自配对的工具结果；未配对时保持未记录。", "Click a row to land on that event in the session. Exit codes come from the paired tool result; unpaired calls stay not recorded.") + "</p></section>";
  }
  function sessionFilesTable(data) {
    const files = Array.isArray(data.files) ? data.files : null;
    if (!files) {
      return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("文件", "Files") + '</h3><span class="fl-aside">' + uiText("接口未就绪", "Interface not ready") + '</span></header><div class="empty-copy"><strong>' + uiText("文件投影接口未就绪。", "The file projection interface is not ready.") + "</strong></div></section>";
    }
    const total = num(data.files_total);
    const aside = total != null && total > files.length
      ? uiText("已加载 " + files.length + " / " + total + " 个", "Loaded " + files.length + " of " + total)
      : quantity(files.length, "个文件", "file", "files")
    const rows = files.map((entry) => '<a class="file-row" data-key="file:' + esc(entry.path || "") + '" href="#/sessions?file=' + encodeURIComponent(entry.path || "") + '"><span class="file-path" data-no-translate="true">' + esc(entry.path || uiText("路径未记录", "Path not recorded")) + '</span><span class="file-counts"><b>' + esc(count(entry.reads)) + '</b><small>' + uiText("读", "Read") + '</small></span><span class="file-counts"><b>' + esc(count(entry.edits)) + '</b><small>' + uiText("改", "Edit") + '</small></span><span class="file-counts"><b>' + esc(count(entry.writes)) + '</b><small>' + uiText("写", "Write") + '</small></span><span class="file-counts"><b>' + esc(count(entry.deletes)) + '</b><small>' + uiText("删", "Delete") + '</small></span><time>' + esc(shortDate(entry.last_at)) + "</time></a>").join("");
    const body = rows ? '<div class="file-table">' + rows + "</div>" : '<div class="empty-copy"><strong>' + uiText("没有记录到文件读写。", "No file read or write was recorded.") + "</strong></div>";
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("文件", "Files") + '</h3><span class="fl-aside">' + esc(aside) + "</span></header>" + body + '<p class="evidence-note">' + uiText("点击一行到会话页，按这个路径筛选全部会话。", "Click a row to open the session list filtered by this path.") + "</p></section>";
  }
  function sessionChildrenCard(data) {
    const children = Array.isArray(data.children) ? data.children : null;
    if (!children) return "";
    const rows = children.map((child) => '<a class="overview-session-row" href="#/sessions/' + encodeURIComponent(child.id) + '">' + sourceMark(child.source) + '<span class="overview-session-main">' + sessionTitleHTML(child) + '<span class="overview-session-meta">' + esc([child.agent_role || uiText("角色未记录", "Role not recorded"), child.agent_nickname || uiText("昵称未记录", "Nickname not recorded"), shortDate(child.started_at)].join(" · ")) + "</span></span></a>").join("");
    return '<section class="elevated-card card-pad stats-card wide"><header class="fl-head"><h3>' + uiText("子会话", "Subagent sessions") + '</h3><span class="fl-aside">' + esc(quantity(children.length, "个", "session", "sessions")) + '</span></header><div class="overview-session-list">' + (rows || '<div class="empty-copy"><strong>' + uiText("没有记录到子会话。", "No subagent session was recorded.") + "</strong></div>") + "</div></section>";
  }
  function sessionCommandsPane(data) {
    return '<div class="session-commands-pane">' + sessionCommandsTable(data) + sessionFilesTable(data) + sessionChildrenCard(data) + "</div>";
  }
  // The usage bar (§23) states what one session cost and changed. Every cell is
  // one measured fact with its own name; a field the source never recorded
  // reads "not recorded" instead of 0, and the total names the record it was
  // read out of.
  function sessionUsageCell(label, value, detail) {
    const missing = String(value).indexOf(count(null)) >= 0;
    return '<div class="session-usage-cell" data-missing="' + missing + '"><span class="session-usage-label">' + esc(label) + '</span><b data-no-translate="true">' + esc(value) + "</b>" + (detail ? "<small>" + esc(detail) + "</small>" : "") + "</div>";
  }
  function sessionModelTable(usage) {
    const models = Array.isArray(usage && usage.by_model) ? usage.by_model : [];
    if (!models.length) return '<div class="session-usage-models" data-missing="true">' + uiText("没有记录到按模型的 token 拆分。", "No per-model token split was recorded.") + "</div>";
    const rows = models.map((entry) => '<div class="session-usage-model-row" data-key="model:' + esc(entry.model) + '"><span data-no-translate="true">' + esc(entry.model) + "</span><span>" + esc(count(entry.turns)) + "</span><span>" + esc(tokenText(entry.input_tokens)) + "</span><span>" + esc(tokenText(entry.output_tokens)) + '</span><b data-no-translate="true">' + esc(tokenText(entry.total_tokens)) + "</b></div>").join("");
    return '<div class="session-usage-models" title="' + esc(tokenTitle()) + '"><div class="session-usage-model-head"><span>' + uiText("模型", "Model") + "</span><span>" + uiText("轮次", "Turns") + "</span><span>" + uiText("输入", "Input") + "</span><span>" + uiText("输出", "Output") + "</span><span>" + uiText("总 token", "Total tokens") + "</span></div>" + rows + "</div>";
  }
  function sessionUsageBar(item) {
    const usage = usageOf(item);
    const turns = num(usage.assistant_turns) == null && num(usage.user_turns) == null
      ? count(null)
      : count(usage.assistant_turns) + " / " + count(usage.user_turns);
    const cells = [
      sessionUsageCell(uiText("总 token", "Total tokens"), tokenText(usage.total_tokens), tokenTitle()),
      sessionUsageCell(uiText("输入 token", "Input tokens"), tokenText(usage.input_tokens)),
      sessionUsageCell(uiText("缓存读取 token", "Cached input tokens"), tokenText(usage.cached_input_tokens)),
      sessionUsageCell(uiText("输出 token", "Output tokens"), tokenText(usage.output_tokens)),
      sessionUsageCell(uiText("推理 token", "Reasoning tokens"), tokenText(usage.reasoning_tokens)),
      sessionUsageCell(uiText("轮次 助手 / 用户", "Turns assistant / user"), turns),
      sessionUsageCell(uiText("改动行", "Changed lines"), linesChangedText(usage.lines_added, usage.lines_removed)),
      sessionUsageCell(uiText("改动文件", "Files changed"), count(usage.files_changed)),
      sessionUsageCell(uiText("活跃 / 总时长", "Active / total time"), durationText(usage.active_ms) + " / " + durationText(item && item.duration_ms), uiText("相邻记录间隔 ≤ 10 分钟才计入活跃", "Only gaps of 10 minutes or less count as active"))
    ];
    if (num(usage.cost) != null) cells.push(sessionUsageCell(uiText("成本", "Cost"), "$" + Number(usage.cost).toFixed(4)));
    const total = '<div class="session-usage-total" title="' + esc(uiText("工作 token = 输入 + 输出 + 缓存写入，不含缓存读取（缓存读取约为新输入价格的十分之一）", "Work tokens = input + output + cache write; cache reads excluded (a cache read is priced around a tenth of fresh input)")) + '"><span class="session-usage-label">' + uiText("工作 token", "Work tokens") + '</span><b data-no-translate="true">' + esc(tokenText(workTokensOf(usage))) + "</b><small>" + esc(uiText("度量来源：", "Measured from: ") + usageSourceText(usage.source)) + "</small></div>";
    // A session whose transcript is still being written has transient numbers.
    // They are stated plainly rather than emphasised: a turn count of 0/1 or an
    // active time equal to the whole span is a partial reading, not a warning,
    // so nothing here is coloured — only the sentence changes.
    const progress = item && item.in_progress === true
      ? '<p class="evidence-note session-usage-progress">' + esc(uiText("进行中，数字会变。" + IN_PROGRESS_NOTE[0], "In progress; these numbers will change. " + IN_PROGRESS_NOTE[1])) + "</p>"
      : "";
    return '<section class="session-usage-bar" data-in-progress="' + Boolean(item && item.in_progress === true) + '">' + total + '<div class="session-usage-cells">' + cells.join("") + "</div>" + progress + sessionModelTable(usage) + "</section>";
  }
  // sessionFleetBlock is the whole subagent tree as one unit (ADR-25): the
  // rollup leads with work tokens — input + output + cache write — because on
  // this machine 98% of the total is cache reads and the total alone
  // overstates a run's cost by around fifty-fold. Outcome states recorded git
  // evidence and stops there: no recorded failure is not success.
  function sessionFleetBlock(item) {
    if (num(item && item.subagent_count) == null || item.subagent_count <= 0) return "";
    const fleet = view.sessionFleet && view.sessionFleet.session_id === item.id ? view.sessionFleet : null;
    if (!fleet) {
      return '<section class="elevated-card session-fleet-card"><header class="fl-head"><h3>' + uiText("团队", "Team") + '</h3><span class="fl-aside">' + esc(uiText("正在读取子代理树…", "Reading the subagent tree…")) + "</span></header></section>";
    }
    const rollup = fleet.rollup || {};
    const children = Array.isArray(fleet.children) ? fleet.children : [];
    if (!children.length) {
      return '<section class="elevated-card session-fleet-card"><header class="fl-head"><h3>' + uiText("团队", "Team") + '</h3></header><p class="evidence-note">' + esc(uiText("这个来源把子代理记录并入父会话，没有单独的子会话行可以汇总。", "This source keeps subagent records inside the parent session; there are no child rows to roll up.")) + "</p></section>";
    }
    const outcome = fleet.outcome || {};
    const cellsRow = [
      sessionUsageCell(uiText("工作 token（树）", "Work tokens (tree)"), tokenText(rollup.work_tokens), uiText("输入 + 输出 + 缓存写入，不含缓存读取", "input + output + cache write; cache reads excluded")),
      sessionUsageCell(uiText("总 token（树）", "Total tokens (tree)"), tokenText(rollup.total_tokens), uiText("其中缓存读取 " + tokenText(rollup.cached_input_tokens), "of which " + tokenText(rollup.cached_input_tokens) + " cache reads")),
      sessionUsageCell(uiText("摩擦（树）", "Friction (tree)"), count(rollup.friction_count)),
      sessionUsageCell(uiText("改动行（树）", "Changed lines (tree)"), linesChangedText(rollup.lines_added, rollup.lines_removed)),
      sessionUsageCell(uiText("git 结局证据", "git outcome evidence"),
        (outcome.commits_recorded || outcome.pushes_recorded || outcome.merges_recorded)
          ? uiText("commit " + count(outcome.commits_recorded) + " 次（" + count(outcome.commits_no_failure) + " 次未见失败）· push " + count(outcome.pushes_recorded) + " 次",
              count(outcome.commits_recorded) + " commits (" + count(outcome.commits_no_failure) + " with no recorded failure) · " + count(outcome.pushes_recorded) + " pushes")
          : uiText("树内没有记录到 git commit / push / merge", "No git commit / push / merge recorded in the tree"),
        daemonProse(outcome.note, outcome.note_en))
    ].join("");
    const tokenNote = rollup.token_sessions != null && rollup.sessions != null && rollup.token_sessions < rollup.sessions
      ? '<p class="evidence-note">' + esc(uiText(rollup.sessions + " 个会话里 " + rollup.token_sessions + " 个记录了 token；求和只覆盖记录了的。", rollup.token_sessions + " of " + rollup.sessions + " sessions recorded tokens; the sums cover only those.")) + "</p>"
      : "";
    const rows = children.map((child) => {
      const usage = child.usage || {};
      const workTokens = num(usage.input_tokens) == null && num(usage.output_tokens) == null && num(usage.cache_write_tokens) == null
        ? null
        : (num(usage.input_tokens) || 0) + (num(usage.output_tokens) || 0) + (num(usage.cache_write_tokens) || 0);
      const name = child.display_title || uiText("标题未记录", "Title not recorded");
      const live = child.in_progress === true ? ' <span class="fl-flag" data-flag="new">' + uiText("进行中", "In progress") + "</span>" : "";
      return '<a class="overview-list-row session-fleet-row" href="#/sessions/' + encodeURIComponent(child.id) + '">'
        + '<span class="session-fleet-role" data-no-translate="true">' + esc(child.agent_role || uiText("角色未记录", "Role not recorded")) + "</span>"
        + '<span class="session-fleet-name" data-no-translate="true" title="' + esc(name) + '">' + esc(name) + live + "</span>"
        + '<span class="overview-list-aside">' + esc(quantity(child.friction_count, "条摩擦", "friction", "friction")) + "</span>"
        + "<strong data-no-translate=\"true\">" + esc(workTokens == null ? uiText("token 未记录", "Tokens not recorded") : tokenText(workTokens)) + "</strong></a>";
    }).join("");
    return '<section class="elevated-card session-fleet-card"><header class="fl-head"><h3>' + uiText("团队", "Team") + '</h3><span class="fl-aside">' + esc(uiText("1 + " + children.length + " 个会话", "1 + " + children.length + " sessions")) + '</span></header>'
      + '<div class="session-usage-cells session-fleet-cells">' + cellsRow + "</div>" + tokenNote
      + '<div class="overview-list session-fleet-list">' + rows + "</div>"
      + '<p class="friction-method-note">' + esc(uiText("孩子按总 token 降序；工作 token = 输入 + 输出 + 缓存写入。", "Children in descending total-token order; work tokens = input + output + cache write.")) + "</p></section>";
  }
  function drawSessionDetail(data, panelOnly) {
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
    const showingLedger = view.sessionTab === "trajectory";
    const laneOf = (kind) => kind === "tool" || kind === "subtool" ? 2 : kind === "message" || kind === "compacted" ? 1 : 0;
    const laneLabel = view.locale === "en" ? ["Input", "Model", "Tools"] : ["输入", "模型", "工具"];
    const overviewBars = !showingLedger ? "" : rows.map((row) => {
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
    const annotation = item.annotation || {};
    const pinned = Boolean(item.pinned || annotation.pinned);
    // The harness is the tool that wrote the transcript; the source label and
    // machine label say which registered root it was read from, so they belong
    // next to it rather than in a separate line the reader has to correlate.
    const originParts = sessionOriginParts(item);
    const identity = [item.project_label || uiText("项目未记录", "Project not recorded"), [source(item.source)].concat(originParts).join(" · "), item.model || uiText("模型未记录", "Model not recorded")].join(" · ");
    const pinButton = sessionPinButton(item, pinned);
    const parent = data.parent || null;
    const parentTarget = parent ? parent.id : item.parent_session_id || "";
    // §20.10: the name is the name and the parent is its own field, so the
    // parent gets its own line and no arrow is spliced into the title.
    const parentLabel = item.parent_title || (parent ? (parent.title || shortSessionID(parent.id)) : parentTarget ? shortSessionID(parentTarget) : "");
    const parentLine = parentLabel
      ? '<div class="session-parent-line"><span class="session-parent-label">' + uiText("父会话：", "Parent session: ") + "</span>" + (parentTarget
        ? '<a href="#/sessions/' + encodeURIComponent(parentTarget) + '" data-no-translate="true">' + esc(parentLabel) + "</a>"
        : '<span data-no-translate="true">' + esc(parentLabel) + "</span>") + "</div>"
      : "";
    const threadBadge = item.thread_kind === "subagent"
      ? '<span class="session-role-badge" data-no-translate="true">' + icon("git-commit-horizontal") + esc([item.agent_role || uiText("角色未记录", "Role not recorded"), item.agent_nickname || uiText("昵称未记录", "Nickname not recorded")].join(" · ")) + "</span>"
      : item.thread_kind === "main"
        ? '<span class="session-role-badge" data-thread="main">' + uiText("主会话", "Main session") + "</span>"
        : '<span class="session-role-badge" data-thread="unrecorded">' + uiText("会话层级未记录", "Thread level not recorded") + "</span>";
    const headTitle = sessionTitleParts(item);
    const headFlag = item.title_source === "synthesized"
      ? '<span class="session-title-flag" title="' + esc(uiText("会话本身没有标题；这个名字由子代理身份与父会话合成", "The session records no title; this name is composed from the subagent identity and its parent session")) + '">' + uiText("合成名", "Synthesized") + "</span>"
      : "";
    const sessionHeader ='<header class="detail-header session-shell-header"><a class="back-link" href="#/sessions" aria-label="返回会话列表">' + icon("arrowLeft") + '</a><div class="detail-identity"><span class="session-shell-title"><h1' + (headTitle.missing ? ' data-missing="true"' : ' data-no-translate="true"') + '>' + esc(headTitle.title) + '</h1>' + headFlag + inProgressBadge(item) + threadBadge + '</span><span class="detail-subline">' + esc(shortSessionID(item.id || item.source_session_id) + " \u00b7 " + identity + (item.cwd ? " \u00b7 " + item.cwd : "")) + '</span></div><div class="session-header-actions">' + pinButton + '<span class="fl-flag" data-flag="new">' + icon("activity") + (view.locale === "en" ? "Recorded" : "已记录") + '</span></div></header>';
    const annotationLine = '<div class="session-annotation">' + sessionTagChips(item) + '<label class="session-note"><span class="sr-only">' + uiText("会话笔记", "Session note") + '</span><textarea data-session-note data-session-id="' + esc(item.id) + '" rows="2" placeholder="' + uiText("给这次会话写一条笔记（失焦保存，只写本地数据库）", "Write a note for this session (saved on blur, local database only)") + '">' + esc(annotation.note || "") + '</textarea></label></div>';
    const taskLine = '<div class="session-task-line"><span class="session-task-label">' + (view.locale === "en" ? "Task" : "任务") + '</span><span class="session-task-value">' + esc(taskText) + '</span></div>';
    const tabs = '<div class="detail-tabbar session-tabbar">' + tabControl("session-tab", [
      { value: "trajectory", label: uiText("轨迹", "Trajectory") },
      { value: "chat", label: uiText("对话", "Conversation") },
      { value: "commands", label: uiText("命令与文件", "Commands and files") }
    ], view.sessionTab) + "</div>";
    const filteredGroups = !showingLedger ? [] : groups.map((group) => {
      let groupRows = group.events.map((entry) => rows[entry.index]);
      if (view.sessionFoldCalls) groupRows = groupRows.filter((row) => row.kind !== "tool" && row.kind !== "subtool");
      if (query) groupRows = groupRows.filter((row) => row.searchable.includes(query));
      const collapsed = view.sessionFoldTurns || view.sessionCollapsedTurns[group.number];
      return { group, rows: groupRows, collapsed };
    }).filter((group) => group.rows.length || !query);
    const ledgerChunks = sessionLedgerChunks(filteredGroups);
    const ledgerGroups = !showingLedger ? "" : sessionLazyMarkup(ledgerChunks, (batch) => sessionLedgerBatchHTML(batch, selectedIndex), 52, (batch) => batch.reduce((total, chunk) => total + chunk.rows.length, 0));
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
    const mainContent = view.sessionTab === "chat" ? chat : view.sessionTab === "commands" ? sessionCommandsPane(data) : overview + toolbar + ledger;
    const body = '<div class="session-detail-canvas"><main class="session-detail-main-pane">' + mainContent + '</main><aside class="session-inspector-pane"><div class="session-inspector-head"><div class="session-inspector-tabs"><button type="button" data-action="session-inspector-tab" data-tab="inspector" data-active="' + (view.sessionInspectorTab === "inspector") + '">' + inspectorTab + '</button><button type="button" data-action="session-inspector-tab" data-tab="ecm" data-active="' + (view.sessionInspectorTab === "ecm") + '">' + ecmTab + '</button><button type="button" data-action="session-inspector-tab" data-tab="friction" data-active="' + (view.sessionInspectorTab === "friction") + '">' + frictionTab + '</button></div><span>' + (selected ? esc("#" + (selectedIndex + 1)) : (view.locale === "en" ? "No selection" : "未选择")) + '</span></div><div class="session-inspector-scroll">' + inspectorBody + '</div></aside></div>';
    const markup = sessionHeader + parentLine + taskLine + sessionUsageBar(item) + sessionFleetBlock(item) + annotationLine + tabs + screenContent(body, "session-detail-page", "session-detail-scroll");
    if (!(panelOnly && swapPanels(markup, [".session-tabbar", ".session-detail-canvas"]))) setScreen(markup);
    const turnsButton = document.querySelector('[data-action="session-mode"][data-mode="turns"]');
    const callsButton = document.querySelector('[data-action="session-mode"][data-mode="calls"]');
    if (turnsButton) turnsButton.innerHTML = icon(view.sessionFoldTurns ? "rows-3" : "rows-2") + (view.locale === "en" ? "Turns" : "回合");
    if (callsButton) callsButton.innerHTML = icon(view.sessionFoldCalls ? "list" : "list-collapse") + (view.locale === "en" ? "Calls" : "调用");
    localizeDOM();
    armSessionLazyRows();
    if (view.sessionRevealEvent === selectedIndex) revealSelectedLedgerRow(selectedIndex);
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
    setScreen(cleanupHeader + screenContent(metrics + table + plan, "cleanup-page"));
    localizeDOM();
    updateCleanupSummary();
  }
  function currentAssetID() {
    const path = parseHash().path;
    return path.startsWith("/assets/") ? decodeURIComponent(path.slice("/assets/".length)) : "";
  }
  function clearData() {
    cache.assets = null;
    cache.assetsMode = null;
    cache.stats = null;
    cache.notifications = null;
    cache.timeline = null;
    view.timelineItems = null;
    view.timelineClusters = null;
    view.timelineTotal = null;
    view.timelineOffset = 0;
    cache.friction = null;
    cache.overview = null;
    cache.overviewRange = "";
    view.sessionList.key = "";
    view.sessionChildren = {};
    view.overviewTime = null;
    view.projectPage.key = "";
    view.projectPage.data = null;
    view.dataPage.health = null;
    view.dataPage.tools = null;
    view.dataPage.sources = null;
  }
  function renderError(error) {
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    console.error("[Flatline] route error", error && error.stack ? error.stack : error);
    setScreen(screenContent('<section class="elevated-card card-pad"><div class="empty-copy"><strong>无法读取本地事实层。</strong><span>' + esc(error.message || error) + '</span><p>请确认 Flatline daemon 正在运行，并检查 loopback 地址。</p></div></section>', "narrow"));
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
  async function focusSessionEvent(eventID) {
    if (!eventID) return;
    const target = String(eventID);
    const eventsOf = () => (view.sessionData && Array.isArray(view.sessionData.events) ? view.sessionData.events : []);
    let index = eventsOf().findIndex((event) => String(event.id) === target);
    while (index < 0 && view.sessionPageState && view.sessionPageState.hasMore) {
      const before = eventsOf().length;
      await loadNextSessionPage();
      if (eventsOf().length === before) break;
      index = eventsOf().findIndex((event) => String(event.id) === target);
    }
    if (index < 0) {
      notify(uiText("该事件不在已加载范围内。", "That event is not within the loaded range."), "error");
      return;
    }
    view.selectedFriction = null;
    view.selectedEvent = index;
    view.sessionRevealEvent = index;
    drawSessionDetail(view.sessionData);
  }
  // The ledger renders in lazy batches, so a deep-linked row may not exist yet.
  function revealSelectedLedgerRow(index) {
    for (let guard = 0; guard < 400; guard += 1) {
      const row = document.querySelector('.session-ledger-row[data-event-index="' + index + '"], .session-chat-row[data-event-index="' + index + '"]');
      if (row) { row.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "center" }); return; }
      const sentinel = document.querySelector("[data-session-sentinel]");
      if (!sentinel) return;
      hydrateSessionBatch(Number(sentinel.getAttribute("data-session-sentinel")));
    }
  }
  async function dispatch(path, params) {
    if (path === "/" && params.get("scope")) {
      location.hash = "#/assets?scope=" + encodeURIComponent(params.get("scope"));
      return;
    }
    if (path.startsWith("/assets/")) {
      const id = currentAssetID();
      if (view.assetAssetID !== id) { view.assetAssetID = id; view.assetTab = "diagnosis"; }
      await loadWallAssets();
      const detail = await get("/api/v1/assets/" + encodeURIComponent(id));
      const cached = cache.assets && cache.assets.assets && cache.assets.assets.find((asset) => asset.id === id);
      if (cached && detail.asset) Object.assign(cached, detail.asset);
      await drawDetail(detail);
      return;
    }
    if (path === "/assets") {
      view.scope = params.get("scope") || "all";
      await loadWallAssets();
      await loadNotifications().catch(() => {});
      renderNotification();
      drawWall();
      return;
    }
    if (path === "/sessions") {
      if (view.sessionList.items.length && view.sessionList.key === sessionHash(sessionQuery())) drawSessions();
      else await loadSessions(false);
      return;
    }
    if (path.startsWith("/sessions/")) {
      const id = decodeURIComponent(path.slice("/sessions/".length));
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
        view.sessionEventFocus = "";
        view.sessionRevealEvent = -1;
        view.sessionCommandProgram = "all";
        view.sessionCommandFailedOnly = false;
        view.sessionFleet = null;
      }
      if (params.get("pane") === "friction") view.sessionInspectorTab = "friction";
      const sessionData = view.sessionData && view.sessionPageState ? view.sessionData : await loadSessionFirstPage(id);
      drawSessionDetail(sessionData);
      // The fleet rollup (ADR-25) loads after the first paint: the tree can
      // hold a hundred children and the session itself must not wait for it.
      const detailItem = sessionData.session || {};
      if (num(detailItem.subagent_count) > 0 && (!view.sessionFleet || view.sessionFleet.session_id !== detailItem.id)) {
        get("/api/v1/sessions/" + encodeURIComponent(id) + "/fleet").then((fleet) => {
          if (view.sessionData && view.sessionData.session && view.sessionData.session.id === fleet.session_id) {
            view.sessionFleet = fleet;
            drawSessionDetail(view.sessionData);
          }
        }).catch(() => {});
      }
      const wanted = params.get("event") || "";
      if (wanted && view.sessionEventFocus !== wanted) {
        view.sessionEventFocus = wanted;
        await focusSessionEvent(wanted);
      }
      hydrateSelectedEventPayload();
      return;
    }
    if (path === "/friction") {
      applyFrictionHash();
      view.frictionDetail = null;
      view.frictionDetailKey = "";
      await loadFrictionOverview(true);
      return;
    }
    if (path.startsWith("/friction/")) {
      applyFrictionHash();
      const group = currentFrictionGroup();
      if (!group) {
        location.hash = "#/friction";
        return;
      }
      const key = group.project + "\x1f" + group.harness + "\x1f" + frictionFilterParams().toString();
      if (view.frictionDetailKey !== key) {
        view.frictionDetailKey = key;
        view.frictionDetail = null;
        view.frictionSelected = 0;
      }
      if (view.frictionDetail) drawFrictionDetail(view.frictionDetail, group);
      else await loadFrictionDetail(group, true);
      return;
    }
    if (path === "/timeline") {
      if (Array.isArray(view.timelineItems)) drawTimeline();
      else await loadTimeline(false);
      return;
    }
    if (path.startsWith("/projects")) {
      const key = path === "/projects" ? "" : decodeURIComponent(path.slice("/projects/".length));
      if (!key) { location.hash = "#/sessions"; return; }
      await loadProjectPage(key, params);
      drawProjectPage(key);
      return;
    }
    if (path === "/stats") { await loadDataPage(); drawStats(); return; }
    if (path === "/cleanup") { await loadWallAssets(); await drawCleanup(); return; }
    if (!cache.overview || cache.overviewRange !== overviewRangeKey()) await loadOverviewPage();
    renderNotification();
    drawOverview();
  }
  // The sidebar and header stay mounted across routes; only #flatline-screen
  // changes, so a route change can never repaint the shell.
  function refreshShell() {
    if (!shell.built || !root.querySelector(".prototype-shell") || shell.chrome !== view.locale + "\x1f" + view.theme) {
      renderShell();
      return;
    }
    const active = routeKey();
    root.querySelectorAll(".us-nav-row[data-nav]").forEach((row) => { row.dataset.active = String(row.dataset.nav === active); });
    const group = document.getElementById("sidebar-projects");
    if (group) group.innerHTML = '<div class="sidebar-group-label">' + uiText("项目", "Projects") + "</div>" + sidebarProjects();
    renderNotification();
    renderSearchPanel();
    localizeDOM();
  }
  async function route(refresh) {
    const { path, params } = parseHash();
    closePopover();
    window.scrollTo(0, 0);
    try {
      if (!shell.status || refresh) await loadShellData();
      refreshShell();
      const key = path + "\x1f" + view.locale + "\x1f" + view.theme;
      if (shell.screenKey !== key) {
        shell.screenKey = key;
        setScreen(skeletonFor(path));
      }
      await dispatch(path, params);
      updateShellChrome();
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
  // Every dropdown commits through here, so the popover component never needs
  // to know what a page does with the value.
  function handleSelectChange(action, value) {
    if (action === "session-sort") return applySessionQuery({ sort: value || "recent" });
    if (action === "session-project") return applySessionQuery({ projects: value ? [value] : [] });
    if (action === "session-harness") return applySessionQuery({ harness: value });
    if (action === "session-tag") return applySessionQuery({ tags: value ? [value] : [] });
    if (action === "session-model") return applySessionQuery({ model: value });
    if (action === "session-program") return applySessionQuery({ program: value });
    if (action === "session-role") return applySessionQuery({ role: value });
    if (action === "source-kind") {
      view.dataPage.sourceForm.kind = value || "";
      drawStats();
      return;
    }
    if (action === "session-command-program") {
      view.sessionCommandProgram = value || "all";
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (action === "friction-project-filter") { view.frictionProjectFilter = value || "all"; return reloadFriction(); }
    if (action === "friction-scope-filter") { view.frictionCategoryFilter = value === "expected_exit" ? "expected_exit" : "all"; return reloadFriction(); }
    if (action === "friction-category-filter") { view.frictionCategoryFilter = value || "all"; return reloadFriction(); }
    if (action === "friction-tool-filter") { view.frictionToolFilter = value || "all"; return reloadFriction(); }
    if (action === "friction-harness-filter") { view.frictionHarnessFilter = value || "all"; return reloadFriction(); }
    if (action === "friction-kind-filter") { view.frictionKindFilter = value || "all"; return reloadFriction(); }
    if (action === "friction-range-filter") {
      view.frictionRange = value || "all";
      if (view.frictionRange !== "custom") {
        view.frictionFrom = "";
        view.frictionTo = "";
      }
      return reloadFriction();
    }
    if (action === "friction-window-filter") { view.frictionWindow = FRICTION_WINDOWS.indexOf(Number(value)) >= 0 ? Number(value) : FRICTION_DEFAULT_WINDOW; return reloadFriction(); }
    if (action === "friction-sort") { view.frictionSort = value || "count"; return reloadFriction(); }
    if (action === "friction-group-by") {
      const next = value || "project";
      if (view.frictionSort === frictionDefaultSort(view.frictionGroupBy)) view.frictionSort = frictionDefaultSort(next);
      view.frictionGroupBy = next;
      return reloadFriction();
    }
  }
  function handleDateRangeChange(action, from, to) {
    if (action === "overview-range") {
      const params = new URLSearchParams();
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      const text = params.toString();
      location.hash = text ? "#/?" + text : "#/";
      return;
    }
    if (action === "session-range") return applySessionQuery({ from: from, to: to });
    if (action === "friction-range") {
      view.frictionRange = from || to ? "custom" : "all";
      view.frictionFrom = from || "";
      view.frictionTo = to || "";
      return reloadFriction();
    }
  }
  function handleFilterChange(action, group, value) {
    if (action === "session-filters") {
      if (group === "project") return applySessionQuery({ projects: value ? [value] : [] });
      if (group === "harness") return applySessionQuery({ harness: value });
      if (group === "tag") return applySessionQuery({ tags: value ? [value] : [] });
      if (group === "model") return applySessionQuery({ model: value });
      if (group === "program") return applySessionQuery({ program: value });
      if (group === "role") return applySessionQuery({ role: value });
      return;
    }
    if (action === "friction-filters") return handleSelectChange("friction-" + group + "-filter", value);
  }
  function clearFilters(action) {
    if (action === "session-filters") return applySessionQuery({ projects: [], harness: "", tags: [], model: "", program: "", role: "" });
    if (action === "friction-filters") {
      view.frictionProjectFilter = "all";
      view.frictionHarnessFilter = "all";
      view.frictionKindFilter = "all";
      view.frictionCategoryFilter = "all";
      view.frictionToolFilter = "all";
      view.frictionSignatureFilter = "all";
      view.frictionRange = "all";
      view.frictionFrom = "";
      view.frictionTo = "";
      view.frictionWindow = FRICTION_DEFAULT_WINDOW;
      reloadFriction();
    }
  }
  document.addEventListener("click", async (event) => {
    if (view.searchOpen && event.target instanceof Element) {
      if (event.target.closest(".global-search-row")) closeGlobalSearch();
      else if (!event.target.closest("#flatline-search-panel, .sidebar-search-input")) closeGlobalSearch();
    }
    const row = event.target instanceof Element ? event.target.closest(".fl-popover-row") : null;
    if (row) {
      event.preventDefault();
      commitPopover(Number(row.dataset.popoverIndex));
      return;
    }
    const target = event.target instanceof Element ? event.target.closest("[data-action]") : null;
    if (view.popover && (!target || (target.dataset.action !== "fl-select" && target.dataset.action !== "fl-filter" && target.dataset.action !== "fl-filter-clear" && target.dataset.action !== "fl-daterange"))) {
      if (!(event.target instanceof Element) || !event.target.closest("#flatline-popover")) closePopover();
    }
    if (!target) return;
    const action = target.dataset.action;
    if (action === "fl-select" || action === "fl-filter") {
      event.preventDefault();
      const open = view.popover && view.popover.trigger === target;
      const wasOpen = Boolean(view.popover);
      closePopover();
      if (open || (wasOpen && view.popover)) return;
      if (action === "fl-select") openSelectPopover(target);
      else openFilterPopover(target);
      if (view.popover) view.popover.trigger = target;
      target.setAttribute("aria-expanded", "true");
      return;
    }
    if (action === "fl-daterange") {
      event.preventDefault();
      const open = view.popover && view.popover.trigger === target;
      const wasOpen = Boolean(view.popover);
      closePopover();
      if (open || (wasOpen && view.popover)) return;
      openDateRangePopover(target);
      if (view.popover) view.popover.trigger = target;
      target.setAttribute("aria-expanded", "true");
      return;
    }
    if (action === "fl-daterange-month") {
      event.preventDefault();
      if (!view.popover || view.popover.kind !== "daterange") return;
      view.popover.month = shiftMonth(view.popover.month, Number(target.dataset.delta) || 0);
      renderPopover();
      return;
    }
    if (action === "fl-daterange-day") {
      event.preventDefault();
      const state = view.popover;
      if (!state || state.kind !== "daterange") return;
      const day = target.dataset.day || "";
      if (!state.pendingFrom) {
        state.pendingFrom = day;
        state.from = day;
        state.to = "";
        renderPopover();
        return;
      }
      const start = state.pendingFrom <= day ? state.pendingFrom : day;
      const end = state.pendingFrom <= day ? day : state.pendingFrom;
      const rangeAction = state.action;
      closePopover();
      handleDateRangeChange(rangeAction, start, end);
      return;
    }
    if (action === "fl-daterange-quick") {
      event.preventDefault();
      const state = view.popover;
      if (!state || state.kind !== "daterange") return;
      const days = Number(target.dataset.days) || 7;
      const rangeAction = state.action;
      closePopover();
      handleDateRangeChange(rangeAction, isoDay(days), isoDay(0));
      return;
    }
    if (action === "fl-daterange-clear") {
      event.preventDefault();
      const state = view.popover;
      if (!state || state.kind !== "daterange") return;
      const rangeAction = state.action;
      closePopover();
      handleDateRangeChange(rangeAction, "", "");
      return;
    }
    if (action === "fl-filter-clear") {
      event.preventDefault();
      const filterAction = view.popover ? view.popover.action : "";
      closePopover();
      clearFilters(filterAction);
      return;
    }
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
    if (action === "overview-range") {
      event.preventDefault();
      const range = target.dataset.range || "30";
      if (range === "custom") return;
      location.hash = range === "30" ? "#/" : "#/?range=" + encodeURIComponent(range);
      return;
    }
    if (action === "project-metric") {
      event.preventDefault();
      view.projectPage.metric = target.dataset.metric || "sessions";
      drawProjectPage(view.projectPage.key.split("\x1f")[0]);
      return;
    }
    if (action === "overview-more") {
      event.preventDefault();
      view.overviewMoreOpen = target.getAttribute("aria-expanded") !== "true";
      try { localStorage.setItem(OVERVIEW_MORE_KEY, view.overviewMoreOpen ? "open" : "closed"); } catch (_) {}
      drawOverview();
      return;
    }
    if (action === "reload-overview") {
      event.preventDefault();
      clearData();
      route(true);
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
      if (Array.isArray(view.timelineItems)) drawTimeline();
      else loadTimeline(false).catch(renderError);
      return;
    }
    if (action === "timeline-more") {
      event.preventDefault();
      target.disabled = true;
      loadTimeline(true).catch(renderError);
      return;
    }
    if (action === "session-range") {
      event.preventDefault();
      const range = target.dataset.range || "all";
      if (range === "custom") return;
      applySessionQuery({ from: range === "all" ? "all" : isoDay(Number(range)), to: "" });
      return;
    }
    if (action === "session-group") {
      event.preventDefault();
      applySessionQuery({ group: target.dataset.group || "none" });
      return;
    }
    if (action === "session-friction-only") {
      event.preventDefault();
      applySessionQuery({ hasFriction: target.getAttribute("data-pressed") !== "true" });
      return;
    }
    if (action === "session-pinned-only") {
      event.preventDefault();
      applySessionQuery({ pinned: target.getAttribute("data-pressed") !== "true" });
      return;
    }
    if (action === "session-thread-toggle") {
      event.preventDefault();
      applySessionQuery({ thread: target.getAttribute("data-pressed") === "true" ? "main" : "all" });
      return;
    }
    if (action === "session-empty-toggle") {
      event.preventDefault();
      applySessionQuery({ empty: target.getAttribute("data-pressed") === "true" ? "0" : "all" });
      return;
    }
    if (action && action.startsWith("session-clear-")) {
      event.preventDefault();
      const dimension = action.slice("session-clear-".length);
      if (dimension === "all") return clearFilters("session-filters");
      if (dimension === "project") return applySessionQuery({ projects: [] });
      if (dimension === "tag") return applySessionQuery({ tags: [] });
      if (dimension === "friction") return applySessionQuery({ hasFriction: false });
      if (dimension === "pinned") return applySessionQuery({ pinned: false });
      const patch = {};
      patch[dimension] = "";
      return applySessionQuery(patch);
    }
    if (action === "session-subagents") {
      event.preventDefault();
      toggleSubagents(target.dataset.sessionId).catch(renderError);
      return;
    }
    if (action === "session-activity") {
      event.preventDefault();
      const days = view.sessionActivityDrag && view.sessionActivityDrag.length === 2 ? view.sessionActivityDrag.slice().sort() : [target.dataset.day, target.dataset.day];
      view.sessionActivityDrag = null;
      applySessionQuery({ from: days[0], to: days[1] });
      return;
    }
    if (action === "session-tag-editor") {
      event.preventDefault();
      view.sessionTagEditor = view.sessionTagEditor === target.dataset.sessionId ? "" : target.dataset.sessionId;
      redrawAnnotated();
      return;
    }
    if (action === "session-tag-remove") {
      event.preventDefault();
      removeSessionTag(target.dataset.sessionId, target.dataset.tag);
      return;
    }
    if (action === "session-pin") {
      event.preventDefault();
      togglePin(target.dataset.sessionId, target.getAttribute("aria-pressed") !== "true");
      return;
    }
    if (action === "friction-harness-filter") {
      event.preventDefault();
      view.frictionHarnessFilter = target.dataset.harness || "all";
      reloadFriction();
      return;
    }
    if (action === "friction-kind-filter") {
      event.preventDefault();
      view.frictionKindFilter = target.dataset.kind || "all";
      reloadFriction();
      return;
    }
    if (action === "friction-range-filter") {
      event.preventDefault();
      view.frictionRange = target.dataset.range || "all";
      view.frictionFrom = "";
      view.frictionTo = "";
      reloadFriction();
      return;
    }
    if (action === "friction-group-by") {
      event.preventDefault();
      view.frictionGroupBy = target.dataset.group || "project";
      reloadFriction();
      return;
    }
    if (action === "friction-category-filter" && target.dataset.category != null) {
      event.preventDefault();
      const next = target.dataset.category;
      view.frictionCategoryFilter = view.frictionCategoryFilter === next ? "all" : next;
      reloadFriction();
      return;
    }
    if (action === "friction-signature-filter" && target.dataset.signature != null) {
      event.preventDefault();
      const next = target.dataset.signature;
      view.frictionSignatureFilter = view.frictionSignatureFilter === next ? "all" : next;
      reloadFriction();
      return;
    }
    // ADR-21: the rule loop. The toggle opens one signature's brief; the other
    // three actions drive the paste / write / verify cycle. Every write asks
    // for confirmation first, and nothing here touches the raw transcripts.
    if (action === "friction-brief-toggle" && target.dataset.signature != null) {
      event.preventDefault();
      view.frictionBriefSignature = view.frictionBriefSignature === target.dataset.signature ? null : target.dataset.signature;
      if (cache.friction) drawFrictionOverview(cache.friction, true);
      return;
    }
    if (action === "friction-brief-copy" && target.dataset.signature != null) {
      event.preventDefault();
      const groups = cache.friction && cache.friction.groups || [];
      const group = groups.find((item) => item.signature === target.dataset.signature);
      const text = group && group.brief ? (view.locale === "en" ? group.brief.paste_prompt_en : group.brief.paste_prompt) : "";
      if (!text) { notify(uiText("简报未就绪。", "The brief is not ready."), "error"); return; }
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(() => notify(uiText("简报已复制；贴给你的 agent 起草规则。", "Brief copied; paste it to your agent to draft the rule."), "success")).catch(() => notify(uiText("复制失败：请手动选择文本。", "Copy failed: select the text manually."), "error"));
      }
      return;
    }
    if (action === "friction-watch-create" && target.dataset.signature != null) {
      event.preventDefault();
      if (view.frictionWatchBusy) return;
      const signature = target.dataset.signature;
      const message = uiText(
        "确认开始验证「" + frictionSignatureLine(signature) + "」？Flatline 将在本地库记录一条验证（默认 14 天窗口），只读事实层，不改动任何转写文件。",
        "Start verifying “" + frictionSignatureLine(signature) + "”? Flatline records one verification in the local database (a 14-day window by default), reads the fact layer only, and touches no transcript file.");
      if (!window.confirm(message)) return;
      view.frictionWatchBusy = true;
      try {
        await post("/api/v1/signature-watches", { signature: signature, confirmed: true, window_days: 14 });
        notify(uiText("已开始验证；窗口结束后这里会显示“修复有效”或“未见改善”。", "Verification started; this row will show “fix verified” or “no change” once the window closes."), "success");
        view.frictionBriefSignature = null;
        await reloadFriction();
      } catch (error) {
        notify(uiText("验证未开始：", "Verification did not start: ") + (error.message || String(error)), "error");
      } finally {
        view.frictionWatchBusy = false;
      }
      return;
    }
    if (action === "friction-watch-cancel" && target.dataset.watchId != null) {
      event.preventDefault();
      if (!window.confirm(uiText("确认取消这条验证？记录会保留（标记为已取消），不会删除。", "Cancel this verification? The record is kept (marked cancelled), not deleted."))) return;
      try {
        await post("/api/v1/signature-watches/cancel", { id: Number(target.dataset.watchId), confirmed: true });
        notify(uiText("已取消验证；记录保留。", "Verification cancelled; the record is kept."), "success");
        await reloadFriction();
      } catch (error) {
        notify(uiText("取消未完成：", "Cancel incomplete: ") + (error.message || String(error)), "error");
      }
      return;
    }
    if (action && action.startsWith("friction-clear-")) {
      event.preventDefault();
      const dimension = action.slice("friction-clear-".length);
      if (dimension === "all") return clearFilters("friction-filters");
      if (dimension === "project") view.frictionProjectFilter = "all";
      if (dimension === "harness") view.frictionHarnessFilter = "all";
      if (dimension === "kind") view.frictionKindFilter = "all";
      if (dimension === "category") view.frictionCategoryFilter = "all";
      if (dimension === "tool") view.frictionToolFilter = "all";
      if (dimension === "range") {
        view.frictionRange = "all";
        view.frictionFrom = "";
        view.frictionTo = "";
      }
      if (dimension === "signature") view.frictionSignatureFilter = "all";
      if (dimension === "window") view.frictionWindow = FRICTION_DEFAULT_WINDOW;
      reloadFriction();
      return;
    }
    if (action === "friction-tool-filter" && target.dataset.tool != null) {
      event.preventDefault();
      const next = target.dataset.tool;
      view.frictionToolFilter = view.frictionToolFilter === next ? "all" : next;
      reloadFriction();
      return;
    }
    if (action === "friction-select") {
      event.preventDefault();
      view.frictionSelected = Math.max(0, Number(target.dataset.index) || 0);
      if (view.frictionDetail) drawFrictionDetail(view.frictionDetail, currentFrictionGroup());
      return;
    }
    if (action === "reload-friction") {
      event.preventDefault();
      view.frictionOverview = null;
      view.frictionDetail = null;
      reloadFriction();
      return;
    }
    if (action === "session-deep") {
      // Only the input carries the new state; a label click reaches here again
      // through the synthetic click the browser sends to the input.
      if (!(target instanceof HTMLInputElement)) return;
      applySessionQuery({ deep: target.checked });
      return;
    }
    if (action === "reload-sessions") {
      event.preventDefault();
      triggerRefresh().then(() => { clearData(); httpCache.clear(); route(true); });
      return;
    }
    if (action === "session-tab") {
      event.preventDefault();
      view.sessionTab = target.dataset.tab || "trajectory";
      if (view.sessionData) {
        drawSessionDetail(view.sessionData, true);
        hydrateSelectedEventPayload();
      } else {
        loadSessionFirstPage(view.sessionID).then((data) => { drawSessionDetail(data); hydrateSelectedEventPayload(); }).catch(renderError);
      }
      return;
    }
    if (action === "session-command-failed") {
      event.preventDefault();
      view.sessionCommandFailedOnly = target.getAttribute("data-pressed") !== "true";
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (action === "tool-family") {
      event.preventDefault();
      const family = target.dataset.family || "";
      view.toolFamilyOpen[family] = !view.toolFamilyOpen[family];
      drawStats();
      return;
    }
    if (action === "session-locate-event") {
      event.preventDefault();
      const eventID = target.dataset.eventId || "";
      if (!eventID) { notify(uiText("这条命令没有记录事件位置。", "This command has no recorded event position."), "error"); return; }
      view.sessionTab = "trajectory";
      view.sessionEventFocus = eventID;
      focusSessionEvent(eventID).then(() => {
        const base = "#/sessions/" + encodeURIComponent(view.sessionID) + "?event=" + encodeURIComponent(eventID);
        if (location.hash !== base) history.replaceState(null, "", base);
      }).catch(renderError);
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
      if (view.sessionData) drawSessionDetail(view.sessionData, true);
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
      view.sessionRevealEvent = -1;
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
    if (action === "source-add") { event.preventDefault(); addSource(); return; }
    if (action === "export-stats") { event.preventDefault(); exportStats(); return; }
    if (action === "data-refresh") {
      event.preventDefault();
      target.disabled = true;
      triggerRefresh().then(() => { clearData(); route(true); });
      return;
    }
  });
  document.addEventListener("input", (event) => {
    if (!(event.target instanceof HTMLInputElement)) return;
    if (event.target.id === "flatline-search") {
      view.search = event.target.value;
      runGlobalSearch(view.search);
      return;
    }
    if (event.target.matches("[data-popover-search]") && view.popover) {
      view.popover.query = event.target.value;
      view.popover.active = 0;
      view.popover.focusSearch = true;
      renderPopover();
      return;
    }
    if (event.target.matches("[data-wall-search]")) {
      view.wallSearch = event.target.value;
      const cursor = event.target.selectionStart;
      drawWall();
      const next = document.querySelector("[data-wall-search]");
      if (next) { next.focus(); try { next.setSelectionRange(cursor, cursor); } catch (_) {} }
      return;
    }
    if (event.target.matches("[data-source-form]")) {
      // Kept in state so picking a kind from the popover does not blank the
      // path the user already typed; no redraw, so the caret never moves.
      view.dataPage.sourceForm[event.target.dataset.sourceForm] = event.target.value;
      return;
    }
    if (event.target.matches("[data-session-list-search]")) {
      const value = event.target.value;
      clearTimeout(view.sessionSearchTimer);
      view.sessionSearchTimer = setTimeout(() => applySessionQuery({ q: value }), 260);
      return;
    }
    if (event.target.matches("[data-session-search]")) {
      view.sessionQuery = event.target.value;
      if (view.sessionData) drawSessionDetail(view.sessionData);
      return;
    }
    if (event.target.matches("[data-friction-search]")) {
      view.frictionQuery = event.target.value;
      clearTimeout(view.frictionSearchTimer);
      view.frictionSearchTimer = setTimeout(reloadFriction, 180);
    }
  });
  document.addEventListener("change", (event) => {
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-source-toggle]")) {
      saveSource(event.target.dataset.sourceId, { enabled: event.target.checked });
      return;
    }
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-cleanup-id]")) updateCleanupSummary();
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-modify-ack]")) {
      const submit = document.querySelector("[data-action=\"confirm-modify\"]");
      if (submit) submit.disabled = !event.target.checked;
    }
  });
  document.addEventListener("pointerdown", (event) => {
    const bar = event.target instanceof Element ? event.target.closest("[data-action=\"session-activity\"]") : null;
    if (bar) view.sessionActivityDrag = [bar.dataset.day];
  });
  document.addEventListener("pointerup", (event) => {
    const bar = event.target instanceof Element ? event.target.closest("[data-action=\"session-activity\"]") : null;
    if (bar && view.sessionActivityDrag) view.sessionActivityDrag = [view.sessionActivityDrag[0], bar.dataset.day];
  });
  document.addEventListener("focusout", (event) => {
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-source-field]")) {
      const input = event.target;
      if (input.value === input.defaultValue) return;
      input.defaultValue = input.value;
      const patch = {};
      patch[input.dataset.sourceField] = input.value.trim();
      saveSource(input.dataset.sourceId, patch);
      return;
    }
    const field = event.target instanceof HTMLTextAreaElement && event.target.matches("[data-session-note]") ? event.target : null;
    if (!field || field.value === field.defaultValue) return;
    field.defaultValue = field.value;
    saveSessionNote(field.dataset.sessionId, field.value);
  });
  document.addEventListener("keydown", (event) => {
    if (event.target instanceof HTMLInputElement && event.target.matches("[data-session-tag-input]")) {
      if (event.key === "Enter") {
        event.preventDefault();
        const value = event.target.value.trim();
        view.sessionTagEditor = "";
        if (value) addSessionTag(event.target.dataset.sessionId, value);
        else redrawAnnotated();
      }
      if (event.key === "Escape") {
        event.preventDefault();
        view.sessionTagEditor = "";
        redrawAnnotated();
      }
      return;
    }
    if (view.popover && view.popover.kind === "daterange") {
      if (event.key === "Escape") { event.preventDefault(); closePopover(); }
      return;
    }
    if (view.popover) {
      const items = popoverItems();
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        if (!items.length) return;
        view.popover.active = (view.popover.active + (event.key === "ArrowDown" ? 1 : items.length - 1)) % items.length;
        view.popover.focusSearch = false;
        renderPopover();
        return;
      }
      if (event.key === "Enter") { event.preventDefault(); commitPopover(); return; }
      if (event.key === "Escape") { event.preventDefault(); closePopover(); return; }
    }
    if (event.target instanceof HTMLInputElement && event.target.id === "flatline-search" && view.searchOpen) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const total = globalSearchItems().length;
        if (!total) return;
        view.searchIndex = (view.searchIndex + (event.key === "ArrowDown" ? 1 : total - 1)) % total;
        renderSearchPanel();
        return;
      }
      if (event.key === "Enter") {
        event.preventDefault();
        openSearchResult(view.searchIndex);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        closeGlobalSearch();
        event.target.blur();
        return;
      }
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k") {
      event.preventDefault();
      const input = document.getElementById("flatline-search");
      if (input) { input.focus(); input.select(); if (input.value.trim()) runGlobalSearch(input.value); }
    }
    if (event.key === "Escape") {
      if (view.searchOpen) { closeGlobalSearch(); return; }
      if (document.querySelector("[data-modify-modal]")) { closeModifyViewer(); return; }
      const input = document.getElementById("flatline-search");
      if (input && document.activeElement === input) input.blur();
    }
  });
  window.addEventListener("hashchange", () => route());

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
    const summary = '<strong>' + all.length + (view.locale === "en" ? " assets</strong> · <strong>" : " 个资产</strong> · <strong>") + attention + (view.locale === "en" ? " need attention</strong> · " : " 个需要注意</strong> · ") + otherCount + (view.locale === "en" ? " have no related task record or are archived" : " 个没有相关任务记录或已归档") + (view.wallSearch ? (view.locale === "en" ? " · Showing " + assets.length : " · 当前显示 " + assets.length + " 个") : "");
    const wallSearch = '<label class="wall-search"><span class="sr-only">' + uiText("筛选资产", "Filter assets") + '</span>' + icon("search", "search-icon") + '<input type="search" data-wall-search placeholder="' + uiText("筛选资产、路径或证据", "Filter assets, paths or evidence") + '" value="' + esc(view.wallSearch) + '"></label>';
    const legend = wallSearch + '<span class="fl-legend"><span><i data-mark="asset"></i>' + uiText("资产变更", "Asset change") + '</span><span><i data-mark="env"></i>' + uiText("环境变化", "Environment change") + '</span><span><i data-mark="alive"></i>' + uiText("恢复使用", "Restored use") + '</span></span>';
    const screen = document.getElementById("flatline-screen");
    if (!screen) return;
    const zones = [
      section("需要注意", ["silent", "broken", "bypassed"], "过去 14 天没有状态变化。", "bad", true, groups),
      section("观察中", ["degraded", "awaiting_resurrection"], "当前没有处于观察中的资产。", "warn", true, groups),
      section("正常", ["healthy"], "当前没有可确认正常的资产。", "good", true, groups),
      section("几乎未使用", ["dormant"], "当前没有达到几乎未使用判定的资产。", "muted", true, groups),
      // 900+ 个"没有相关任务记录"的资产把有信号的分 区压到首屏之外：这一段
      // 默认折叠，"几乎未使用"（清理候选所在）默认展开。
      section("没有相关任务记录", ["no_opportunity"], "没有记录到与该资产相关的任务。", "muted", false, groups),
      section("不可观测", ["unobservable"], "当前数据没有记录该资产是否被加载或使用。", "muted", false, groups),
      section("其他", ["not_evaluated", "archived"], "当前没有未评估或已归档资产。", "muted", false, groups)
    ].join("");
    setScreen(header("资产", summary, legend) + screenContent(zones, "wall-page"));
    localizeDOM();
    armWallLazyRows();
  }
  route().then(scheduleStatusPoll);
})();
