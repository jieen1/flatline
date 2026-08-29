package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedSPAIsLocalAndServesFallback(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/", "/style.css", "/app.js", "/assets/skill:project:fixture"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, rec.Code)
		}
		body, err := io.ReadAll(rec.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(body) == 0 {
			t.Fatalf("GET %s returned empty body", path)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Flatline") || !strings.Contains(body, "app.js") {
		t.Fatalf("index missing local app markers: %q", body)
	}
	if strings.Contains(body, "https://") || strings.Contains(body, "http://") {
		t.Fatalf("index contains external resource URL: %q", body)
	}
	appJSReq := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	appJSRec := httptest.NewRecorder()
	handler.ServeHTTP(appJSRec, appJSReq)
	appJS := appJSRec.Body.String()
	for _, marker := range []string{"没有记录到与该资产相关的任务", "判定规则：", "观测等级", "参与记录", "section(\"没有相关任务记录\"", "section(\"不可观测\""} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing clear UI marker %q", marker)
		}
	}
	for _, term := range []string{"\u590d\u6d3b", "\u6682\u65e0\u673a\u4f1a", "\u672a\u53d1\u73b0\u53ef\u6bd4\u4efb\u52a1", "\u6062\u590d\u76d1\u62a4"} {
		if strings.Contains(appJS, term) {
			t.Fatalf("app.js contains forbidden user-facing term %q", term)
		}
	}
}

func TestEmbeddedSPAServesSessionFirstInformationArchitecture(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		"/api/v1/overview",
		"/api/v1/projects",
		"/api/v1/sessions/facets",
		"/annotation",
		"If-None-Match",
		"#/sessions?project=",
		"正在读取本地历史 ",
		"已记录笔记；源文件未改变。",
		"该事件不在已加载范围内。",
		"会话管理接口未就绪。",
		"总览接口未就绪。",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing session-first marker %q", marker)
		}
	}
	// Every route used to start with one loadOverview() that pulled four full
	// collections; the loading model must not come back.
	if strings.Contains(appJS, "async function loadOverview(force, fullAssets)") {
		t.Fatal("app.js still fetches every collection on each route change")
	}
}

func TestEmbeddedSPAServesSessionHierarchyAndDataPage(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		// Session hierarchy: defaults, both scope toggles, inline children.
		`thread: SESSION_THREADS.includes(thread) ? thread : "main"`,
		`empty: SESSION_EMPTY.includes(empty) ? empty : "0"`,
		"session-thread-toggle",
		"session-empty-toggle",
		"/api/v1/sessions?parent=",
		// Commands and files projection.
		"session-locate-event",
		"#/sessions?file=",
		"#/sessions?program=",
		// Aggregate endpoints consumed by the second phase.
		"/api/v1/stats/time",
		"/api/v1/projects/",
		"/api/v1/tools",
		"/api/v1/search?q=",
		"/api/v1/ingest/health",
		"/api/v1/ingest/refresh",
		"/api/v1/sessions/export?",
		// Recurring friction signatures.
		`["project", "category", "tool", "signature"]`,
		"friction-signature-filter",
		// Timeline paging.
		"/api/v1/timeline?limit=",
		"timeline-more",
		// Missing is still not recorded, and no interface is faked.
		"接口未就绪",
		"未记录",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing second-phase marker %q", marker)
		}
	}
	// The timeline first screen must not go back to pulling every record.
	if strings.Contains(appJS, "/api/v1/timeline?limit=5000") {
		t.Fatal("app.js still requests the full timeline on the first screen")
	}
}

func TestEmbeddedSPAPatchesInPageUpdatesInsteadOfRebuilding(t *testing.T) {
	handler := Handler()
	// 200 and a non-empty body is not proof the file is there. Anything the
	// embed does not hold falls through to the SPA, which answers 200 with
	// index.html — and the browser then parses "<!doctype html>" as
	// JavaScript. That is exactly what happened while .gitignore's unanchored
	// `vendor/` kept morphdom.js out of the repo: this test stayed green, the
	// build stayed green, and every in-page update silently rebuilt the DOM
	// instead of patching it. So the body has to be checked, not just counted.
	for _, path := range []string{"/vendor/morphdom.js", "/vendor/morphdom-LICENSE.txt"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("GET %s status = %d, %d bytes", path, rec.Code, rec.Body.Len())
		}
		body := rec.Body.String()
		if strings.Contains(strings.ToLower(body[:min(64, len(body))]), "<!doctype") {
			t.Fatalf("GET %s served the SPA fallback, so the file is missing from the embed", path)
		}
	}
	// The script has to be the library, not merely some non-HTML bytes.
	if body := fetch(t, handler, "/vendor/morphdom.js"); !strings.Contains(body, "morphdom") {
		t.Fatal("/vendor/morphdom.js does not define morphdom; the embed holds something else")
	}
	if body := fetch(t, handler, "/vendor/morphdom-LICENSE.txt"); !strings.Contains(body, "MIT") {
		t.Fatal("/vendor/morphdom-LICENSE.txt is not the MIT licence text")
	}
	// The script tag is only useful if the daemon labels it as script.
	script := httptest.NewRecorder()
	handler.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/vendor/morphdom.js", nil))
	if contentType := script.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("/vendor/morphdom.js Content-Type = %q, want a JavaScript type", contentType)
	}
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(index.Body.String(), "/vendor/morphdom.js") {
		t.Fatal("index.html does not load the local morphdom build")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		"function morphInto(",
		"childrenOnly:",
		"getNodeKey:",
		"onBeforeElUpdated:",
		// Stable keys keep rows from being rebuilt on sort, filter and paging.
		`data-key="session:`,
		`data-key="fs:`,
		`data-key="file:`,
		// Brand marks are inline SVG; an <img> would re-fetch and flash.
		"BRAND_SVG",
		"brand-glyph",
		// A drawn checkbox, and deep search as its own URL flag.
		"fl-check-box",
		`if (query.deep) params.set("deep", "1");`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing in-place-update marker %q", marker)
		}
	}
	if strings.Contains(appJS, `<img src="/icons/`) {
		t.Fatal("app.js still renders brand marks as <img>, which reloads on every re-render")
	}
	// Decorative glyphs were removed on request; icons come from the closed set.
	for _, glyph := range []string{"⤷", "⤴", "★", "»"} {
		if strings.Contains(appJS, glyph) {
			t.Fatalf("app.js still renders the decorative glyph %q", glyph)
		}
	}
}

func TestEmbeddedSPAProvidesComponentMotionAndReducedMotionContract(t *testing.T) {
	handler := Handler()
	appRec := httptest.NewRecorder()
	handler.ServeHTTP(appRec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := appRec.Body.String()
	for _, marker := range []string{
		"function motionPositions(",
		"function animateMotionPositions(",
		"dataset.motionPhase",
		"host.offsetWidth",
		"requestAnimationFrame(() => {",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing motion marker %q", marker)
		}
	}

	styleRec := httptest.NewRecorder()
	handler.ServeHTTP(styleRec, httptest.NewRequest(http.MethodGet, "/style.css", nil))
	style := styleRec.Body.String()
	for _, marker := range []string{
		"--motion-duration-panel",
		"--motion-ease-standard",
		"scroll-behavior: smooth",
		"[data-motion-phase=\"swap\"]",
		".session-page .session-row:active",
		".friction-group-row:active",
		".fl-tab:active",
		"@media (prefers-reduced-motion: reduce)",
		"animation-duration: 1ms !important",
	} {
		if !strings.Contains(style, marker) {
			t.Fatalf("style.css is missing motion marker %q", marker)
		}
	}
}

func TestEmbeddedSPAKeepsIconVocabularyClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	// icon() renders nothing for a name outside the prototype set, so a typo
	// silently produces an empty slot. Keep the new call sites inside the set.
	for _, forbidden := range []string{`icon("user")`, `icon("terminal")`, `icon("database")`, `icon("settings")`} {
		if strings.Contains(appJS, forbidden) {
			t.Fatalf("app.js calls icon() with a name outside the prototype set: %s", forbidden)
		}
	}
}

// The third phase consumes the A3 fields and drops the last native controls.
// Every assertion here corresponds to one thing a person can see on the page.
func TestEmbeddedSPAConsumesThirdPhaseFields(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		// Display name: one helper, the synthesized flag, and the missing case.
		"function sessionTitleParts(",
		"title_source",
		"display_title",
		`"合成名"`,
		// Friction lifecycle: four statuses, the window, and the quiet caveat.
		"function frictionStatusBadge(",
		"function frictionLifecycleCard(",
		`"top_" + status`,
		`frictionLifecycleColumn(lifecycle, "new"`,
		`frictionLifecycleColumn(lifecycle, "active"`,
		`frictionLifecycleColumn(lifecycle, "quiet"`,
		"project_sessions_last_7d",
		"window_days",
		`params.set("window",`,
		"FRICTION_WINDOWS = [7, 14, 30]",
		// Hints: kind badge plus the mechanism sentence.
		"function frictionHintLine(",
		"by_hint_kind",
		"hint.mechanism",
		// Kind columns collapsed into one stacked bar with a legend.
		"function frictionKindBar(",
		"function frictionKindLegend(",
		"function frictionNumberCell(",
		// Project home-directory mark, tool outcomes, pairing progress.
		"is_home_dir",
		"known_outcomes",
		`pairing: ["正在配对工具身份", "Pairing tool identity"]`,
		`reparse: ["正在重新解析", "Re-parsing"]`,
		// Our own range picker, and the ⌘K panel outside the sidebar.
		"function dateRangeControl(",
		"fl-daterange",
		"global-search-panel-inner",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing third-phase marker %q", marker)
		}
	}
	// The native date fields were the last <select>-class control on the page.
	for _, forbidden := range []string{"<select", `type="date"`, "data-overview-from", "data-overview-to", "<img "} {
		if strings.Contains(appJS, forbidden) {
			t.Fatalf("app.js still renders the native control %q", forbidden)
		}
	}
	// A "0 failures" line reads as "nothing failed" when it means "nothing was
	// recorded"; the denominator must always travel with the numerator.
	if strings.Contains(appJS, `count(entry.failures) + " 次失败"`) {
		t.Fatal("app.js prints a failure count without its recorded-outcome denominator")
	}
}

func TestEmbeddedSPAUsesLinkedOverviewDeltasAndFrictionFilters(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		// Overview deltas are compact, direction-aware values beside the main number.
		"function overviewDeltaText(",
		"Math.abs(entry.value)",
		`class="overview-delta`,
		`class="overview-value-stack"`,
		// Overview links preserve the selected all-time range instead of falling
		// back to the sessions page default.
		`range.mode === "all"`,
		// Friction keeps overview presets as presets, so they do not turn into an
		// empty custom-date control when the user drills down.
		`path.indexOf("#/friction") === 0`,
		`params.set("range", range.mode + "d")`,
		// A direct sessions visit starts with the product's seven-day window.
		`from: params.get("from") || isoDay(7)`,
		// Friction keeps search, filters and quick scope controls in one coherent panel.
		"function frictionFilterGroups(",
		"function frictionQuickFilters(",
		`class="friction-filter-panel"`,
		`class="friction-filter-search-row"`,
		`filterControl("friction-filters"`,
		`friction-scope-filter`,
		`segmentControl("friction-range-filter"`,
		"frictionFrom",
		`dateRangeControl("friction-range"`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing linked-filter marker %q", marker)
		}
	}
	if strings.Contains(appJS, "生命周期窗口：近") {
		t.Fatal("friction lifecycle still describes a fixed seven-day window")
	}
	styleRec := httptest.NewRecorder()
	Handler().ServeHTTP(styleRec, httptest.NewRequest(http.MethodGet, "/style.css", nil))
	for _, marker := range []string{
		`.overview-delta[data-direction="up"] { color: var(--destructive); }`,
		`.overview-delta[data-direction="down"] { color: var(--verified); }`,
		".overview-value-stack { display: inline-flex;",
		"flex-direction: row;",
		"a.overview-metric {",
		"padding: 8px 10px;",
		"margin: -8px -10px;",
		".friction-filter-panel {",
		".friction-filter-search-row {",
		".friction-filter-footer {",
		".friction-filter-exclusion",
		".friction-view-controls {",
		".friction-quick-range-controls .fl-segment { min-width: 0; flex: 0 0 auto;",
	} {
		if !strings.Contains(styleRec.Body.String(), marker) {
			t.Fatalf("style.css is missing delta color marker %q", marker)
		}
	}
}

// Decorative glyphs are banned across the whole served bundle, not only in the
// places that once carried them.
func TestEmbeddedSPAHasNoDecorativeGlyphsOrNativeControls(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/app.js", "/style.css", "/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		for _, glyph := range []string{"\u2192", "\u2190", "\u2937", "\u2934", "\u2605", "\u00bb", "\u00ab", "\u2713", "\u27a4", "\u21d2"} {
			if strings.Contains(body, glyph) {
				t.Fatalf("%s contains the decorative glyph %q", path, glyph)
			}
		}
		for _, control := range []string{"<select", "<img ", `type="date"`} {
			if strings.Contains(body, control) {
				t.Fatalf("%s contains the native control %q", path, control)
			}
		}
	}
}

// The fourth phase consumes the A4 measurement (§20.4) and the §22 overview.
// Every assertion here is one thing a person can see on the page.
func TestEmbeddedSPAConsumesSessionUsageAndPeriodSummary(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		// Measurement: every field the session list and detail read.
		"function usageOf(",
		"function tokenText(",
		"function linesChangedText(",
		"function sessionUsageLine(",
		"function sessionUsageBar(",
		"function sessionModelTable(",
		"usage.total_tokens",
		"usage.cached_input_tokens",
		"usage.reasoning_tokens",
		"usage.assistant_turns",
		"usage.lines_added",
		"usage.active_ms",
		"usage.files_changed",
		"usage.by_model",
		"USAGE_SOURCE_LABELS",
		"claude_usage",
		"codex_token_count",
		"opencode_session",
		// Sorting on the measurement.
		`"tokens", "lines_changed", "active"`,
		// parent_title is its own line, not a suffix on the name.
		"item.parent_title",
		"session-parent-line",
		// Overview: the previous period, the measurement KPIs, by_model.
		`query.set("compare", "1")`,
		"function compareAside(",
		"function overviewParallelism(",
		"function overviewEnvironment(",
		"function overviewSubagents(",
		"function overviewReread(",
		"function overviewModelUsage(",
		"missing_commands",
		"failing_programs",
		"sessions_with_subagents",
		"friction_share",
		"parallel_peak",
		"reread_sessions",
		"peak_at",
		"min_known_outcomes",
		"DELTA_SIGNS",
		"token_sessions",
		// Friction: expected exits are one option in the unified filter panel, and
		// mechanism coverage is stated once.
		"function frictionExclusionNote(",
		"friction-scope-filter",
		`"expected_exit"`,
		"expected_exit_count",
		"function frictionHintCoverage(",
		"item.session_count",
		// Programs grouped by family, with their own expected-exit column.
		"function programFamilyGroups(",
		"entry.family",
		"expected_exits",
		"tool-family",
		// Re-parse progress reports what it recovered.
		"detail.events_inserted",
		// Timeline paging appends instead of re-pulling a bigger window.
		"function loadTimeline(",
		"function timelineKeyOf(",
		`"&offset=" + offset`,
		"view.timelineAppended",
		// Sidebar names stay distinguishable, and only the first eight show.
		"function sidebarProjectNames(",
		"SIDEBAR_PROJECT_ROWS = 8",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing fourth-phase marker %q", marker)
		}
	}
	// A missing measurement must never be rendered as a zero.
	for _, forbidden := range []string{
		`usage.total_tokens || 0`,
		`usage.lines_added || 0`,
		`usage.active_ms || 0`,
		// The mechanism dictionary's gaps are stated once in the header, not
		// under every uncovered row.
		"机制字典未覆盖这条签名",
	} {
		if strings.Contains(appJS, forbidden) {
			t.Fatalf("app.js renders a missing measurement as zero or repeats a header note per row: %q", forbidden)
		}
	}
}

// The three deliberate additions to the icon vocabulary are registered in all
// three places icon() consults, and the QA record of why exists.
func TestEmbeddedSPARegistersTheThreeNewIcons(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		// The glyph itself.
		"    pin: '<path d=\"M12 17v5\"",
		"    tag: '<path d=\"M12.586 2.586",
		"    plus: '<path d=\"M5 12h14\"",
		// The closed set and the kebab-case alias table.
		`"pin", "tag", "plus"`,
		`pin: "pin", tag: "tag", plus: "plus"`,
		// The call sites that needed them.
		`icon("pin")`,
		`icon("plus")`,
		`icon("tag")`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing new-icon marker %q", marker)
		}
	}
	// The pin button used chevron-up, which reads as "collapse"; the add-tag
	// button used hash, which is the identifier glyph.
	if strings.Contains(appJS, `data-action="session-pin"`) && strings.Contains(appJS, `title="' + esc(label) + '">' + icon("chevron-up")`) {
		t.Fatal("the pin button still renders chevron-up")
	}
}

// The fifth phase converges the overview and consumes the A6 fields (§25).
// The overview had grown to sixteen blocks and 4400px; it is now four blocks
// and one disclosure, and the blocks that repeated another page are gone.
// P16-3 adds one block ahead of them that answers a different question —
// what is being written right now — and renders nothing on a quiet machine,
// so the "what happened in this window" screen is still the same four blocks.
func TestEmbeddedSPAConvergesTheOverviewToFourBlocksAndOneFold(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()

	// The first screen is the now block plus exactly the four blocks plus the
	// fold. Read the body expression drawOverview composes rather than
	// trusting a marker.
	const bodyStart = `const body = '<div class="stats-grid overview-grid">' + overviewNow(cache.now)`
	from := strings.Index(appJS, bodyStart)
	if from < 0 {
		t.Fatal("drawOverview no longer composes its body from the overview grid")
	}
	to := strings.Index(appJS[from:], `setScreen(header("总览"`)
	if to < 0 {
		t.Fatal("drawOverview no longer paints the overview header")
	}
	body := appJS[from : from+to]
	for _, block := range []string{"overviewMetrics(data)", "frictionLifecycleCard(", "overviewEnvironment(data)", "overviewProjects(data)", "overviewMore(data, range)"} {
		if strings.Count(body, block) != 1 {
			t.Fatalf("the overview first screen must carry %q exactly once: %q", block, body)
		}
	}
	// Everything that answers a second question belongs behind the fold, and
	// must not be composed into the first screen again.
	for _, folded := range []string{"overviewParallelism(", "overviewModelUsage(", "overviewSubagents(", "overviewReread(", "activityHeatmap(", "workHoursHeatmap(", "overviewRecent(", "overviewAssets("} {
		if strings.Contains(body, folded) {
			t.Fatalf("%s is back on the overview first screen; it belongs in the fold", folded)
		}
	}
	// Five blocks repeated the friction page or the project page and were
	// removed outright. Two places for one number is how the two disagree.
	for _, removed := range []string{"function overviewFrictionList(", "function overviewTags(", "function overviewRecurringFriction(", "function overviewPrograms(", "function overviewHotFiles("} {
		if strings.Contains(appJS, removed) {
			t.Fatalf("app.js still defines the duplicated overview block %q", removed)
		}
	}
	for _, marker := range []string{
		// The fold, its action, and the remembered open state.
		"function overviewMore(",
		`OVERVIEW_MORE_KEY = "flatline-overview-more"`,
		`data-action="overview-more"`,
		"view.overviewMoreOpen",
		// by_model on a log scale, with the ticks inside the bars.
		"Math.log10(",
		"overview-model-list",
		"--decades:",
		// in_progress states that the numbers under it are still growing.
		"function inProgressBadge(",
		"item.in_progress !== true",
		"IN_PROGRESS_NOTE",
		"session-usage-progress",
		// worktree and the registered root a session was read from.
		"function sessionOriginParts(",
		"item.worktree",
		"item.source_label",
		"item.machine_label",
		// One definition of a token total, hung on every token number.
		"function noteUsageDefinition(",
		"function tokenTitle(",
		"usage.definition",
		// The timeline total lives in pagination, not at the top level.
		"num(pagination.total)",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing fifth-phase marker %q", marker)
		}
	}
}

// The data page is where a second machine's directory becomes a source. Every
// assertion here is one control a person can operate.
func TestEmbeddedSPAServesTheSourceRegistryUI(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		"/api/v1/sources",
		"function loadSources(",
		"function dataSourcesCard(",
		"function sourceRow(",
		"function sourceAddForm(",
		"function saveSource(",
		"function addSource(",
		`data-source-field="`,
		`sourceField(entry, "label"`,
		`sourceField(entry, "machine_label"`,
		"data-source-toggle",
		`data-source-form="root"`,
		`data-action="source-add"`,
		`"source-kind"`,
		// A rename is a write to the local database and nothing else.
		"已保存；只读扫描，源目录未改变。",
		"已登记；下一轮扫描生效。",
		// The registered-but-unprobed state A6 added to ingest health.
		"已登记 · 本轮未探测",
		// Adding a root does not import anything, so the rescan is offered here.
		`data-action="data-refresh"`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing source-registry marker %q", marker)
		}
	}
	// The root itself is not editable: a different root is a different source.
	if strings.Contains(appJS, `sourceField(entry, "root"`) {
		t.Fatal("app.js offers an editable root; renaming a root in place would refile every session read from it")
	}
	// /stats carries the measurement now (§25.3); the page must not still print
	// "Token 未记录" beside an overview that prints a number.
	if strings.Contains(appJS, `{ label: "Token 数", value: view.locale === "en" ? "Not recorded" : "未记录"`) {
		t.Fatal("the data page still hardcodes an unrecorded token count")
	}
}

// Status is spelled out in words. A tick, a dot or a triangle standing in for a
// state is exactly the decoration this UI does not use.
func TestEmbeddedSPAUsesNoDecorativeStatusSymbols(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/app.js", "/style.css", "/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		for _, glyph := range []string{"✓", "✗", "●", "○", "◐", "★", "▲", "▼", "◆", "■", "⋯", "※", "▶", "◀", "✦", "✱"} {
			if strings.Contains(body, glyph) {
				t.Fatalf("%s contains the decorative status symbol %q", path, glyph)
			}
		}
	}
}

func TestEmbeddedSPARevalidatesStaticFilesAfterRebuild(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	first, err := http.Get(srv.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.Header.Get("Cache-Control") != "no-cache" || first.Header.Get("ETag") == "" {
		t.Fatalf("static files must carry Cache-Control: no-cache and an ETag, got %q / %q", first.Header.Get("Cache-Control"), first.Header.Get("ETag"))
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/app.js", nil)
	req.Header.Set("If-None-Match", first.Header.Get("ETag"))
	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusNotModified {
		t.Fatalf("matching If-None-Match must answer 304, got %d", second.StatusCode)
	}
}

// B6 收口: the data page keeps one card per subject, the KPI row never leaves a
// metric alone on a row of its own, the bucket holding command names that could
// not be parsed is named for what it is and kept last, and the two fields A7
// adds are read where they land without being faked before they do.
func TestEmbeddedSPAConvergesDataPageKPIRowAndFrictionEvidence(t *testing.T) {
	handler := Handler()
	appRec := httptest.NewRecorder()
	handler.ServeHTTP(appRec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := appRec.Body.String()
	cssRec := httptest.NewRecorder()
	handler.ServeHTTP(cssRec, httptest.NewRequest(http.MethodGet, "/style.css", nil))
	css := cssRec.Body.String()

	// The data page had two cards for one subject: a harness-count card built
	// from /stats.source_counts, and the source registry table. The count card
	// is gone; the registry, which names every source and its state, stays.
	for _, gone := range []string{`class="source-stat-row"`, "source-stat-list", "source-stat-coverage", "source-stat-bar", "source-stat-mark"} {
		if strings.Contains(appJS, gone) {
			t.Fatalf("app.js still builds the removed data-page source card (%q)", gone)
		}
	}
	for _, gone := range []string{".source-stat-row", ".source-stat-list", ".source-stat-bar", ".source-stat-coverage", ".source-stat-mark"} {
		if strings.Contains(css, gone) {
			t.Fatalf("style.css still carries the removed source card rule %q", gone)
		}
	}
	if !strings.Contains(appJS, "function dataSourcesCard(") {
		t.Fatal("the source registry card must survive the removal of the harness-count card")
	}

	// Seven KPIs. The card is a size container so the column count follows the
	// card and not the viewport, and below the width that fits all seven the
	// grid is pinned to four tracks: 4 + 3, never 6 + 1.
	if !strings.Contains(appJS, "stats-card wide overview-kpi-card") {
		t.Fatal("the KPI card must carry overview-kpi-card so the grid can query its width")
	}
	if !strings.Contains(css, "container-type: inline-size") || !strings.Contains(css, "container-name: overview-kpi") {
		t.Fatal("style.css must make the KPI card a size container")
	}
	if !strings.Contains(css, "grid-template-columns: repeat(auto-fit, minmax(180px, 1fr))") {
		t.Fatal("the KPI grid must auto-fit on a 180px track minimum")
	}
	if !strings.Contains(css, "@container overview-kpi (max-width: 1415px)") || !strings.Contains(css, "grid-template-columns: repeat(4, minmax(0, 1fr))") {
		t.Fatal("the KPI grid must fall back to four tracks so seven metrics never leave one alone on a row")
	}
	if strings.Contains(css, "repeat(auto-fit, minmax(160px, 1fr))") {
		t.Fatal("the KPI grid still uses the 160px track that lands on six columns")
	}
	// Long numbers keep the rule B4 wrote rather than gaining a second one.
	if !strings.Contains(appJS, `data-wide-value="' + (String(value).length > 12)`) {
		t.Fatal("the 12-character wide-value rule must still size long KPI numbers down")
	}

	// A7 renames the unparsed-command bucket; both keys read the same here, the
	// row sinks below every named command, and it is drawn muted.
	for _, marker := range []string{
		`UNPARSED_COMMAND_KEYS = ["__unparsed__", "__unrecorded__", "unrecorded"]`,
		"const isUnparsedCommand = ",
		"未解析出命令名",
		"Command name not parsed",
		"namedMissing.slice(0, 8).concat(unparsedMissing)",
		`data-muted="' + unparsed + '"`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing unparsed-command marker %q", marker)
		}
	}
	if !strings.Contains(css, `.overview-list-row[data-muted="true"]`) {
		t.Fatal("style.css must weaken the unparsed-command row")
	}

	// friction_link_count and friction_links land on the asset. Zero draws no
	// badge; an absent field is stated as not recorded, never as an empty list.
	for _, marker := range []string{
		"function assetFrictionBadge(",
		"function assetFrictionCard(",
		"item.friction_link_count",
		"asset.friction_links",
		"assetFrictionCard(data)",
		"摩擦关联 ",
		"来自摩擦的参与证据",
		"摩擦关联未记录。",
		"没有摩擦记录撞到该资产的 hook。",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing asset friction-link marker %q", marker)
		}
	}

	// coverage_gaps is a table of three recorded facts under the mechanism
	// split. It carries no advice, and it is absent until the field lands.
	for _, marker := range []string{
		"function frictionCoverageGapTable(",
		"summary.coverage_gaps",
		"frictionHintKindRow(summary) + frictionCoverageGapTable(summary)",
		"规则覆盖缺口",
		"出现会话数",
		`if (!gaps || !gaps.length) return "";`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing coverage-gap marker %q", marker)
		}
	}
}

// B8: the two names icon() could not resolve. list-filter and refresh-cw were
// called from seven places and rendered nothing at all, so the filter button
// and every rescan / retry button carried text and no glyph. Registering a name
// means all three tables agree: the glyph, the closed set, and the alias map.
func TestEmbeddedSPARegistersTheFilterAndRescanIcons(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		// The Lucide glyphs, and the kebab-case keys icon() looks the SVG up by.
		"    refreshCw: '<path d=\"M3 12a9 9 0 0 1 9-9",
		"    listFilter: '<path d=\"M3 6h18\"",
		`ICONS["refresh-cw"] = ICONS.refreshCw;`,
		`ICONS["list-filter"] = ICONS.listFilter;`,
		// The closed set icon() checks the resolved name against.
		`"list-filter", "refresh-cw"`,
		// The alias map, which is what the camelCase call sites go through.
		`listFilter: "list-filter", refreshCw: "refresh-cw"`,
		// The call sites that were drawing an empty button.
		`icon("list-filter")`,
		`icon("refreshCw")`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing filter/rescan icon marker %q", marker)
		}
	}
}

// B8: the five layout defects the v50 baseline recorded. Each assertion is one
// thing that reads differently on the page.
func TestEmbeddedSPADegradesSparseChartsAndGroupsTheSessionToolbar(t *testing.T) {
	handler := Handler()
	appRec := httptest.NewRecorder()
	handler.ServeHTTP(appRec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := appRec.Body.String()
	cssRec := httptest.NewRecorder()
	handler.ServeHTTP(cssRec, httptest.NewRequest(http.MethodGet, "/style.css", nil))
	css := cssRec.Body.String()

	// Under five samples the area fill floods the 34px box and the row reads as
	// a grey block. Those series draw points and the line instead, and the gap
	// band that says "not recorded" shrinks to a strip on the floor.
	for _, marker := range []string{
		"const SPARK_AREA_MIN_POINTS = 5",
		"const sparse = points.length < SPARK_AREA_MIN_POINTS",
		"if (!sparse) groups.filter((group) => group.length > 1)",
		`data-sparse="`,
		`class="fl-spark-point"`,
		"const gapTop = sparse ? floor - 2 : 0",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing sparse-sparkline marker %q", marker)
		}
	}
	if !strings.Contains(css, `.fl-spark[data-sparse="true"] .fl-spark-gap`) {
		t.Fatal("style.css must retone the not-recorded band on a sparse sparkline")
	}

	// One week bucket drew one bar at the far left and printed the same date at
	// both ends of the axis. Under three buckets the numbers go in a table, and
	// a single-bucket axis prints its date once.
	for _, marker := range []string{
		"const PROJECT_WEEK_MIN_BARS = 3",
		"if (list.length < PROJECT_WEEK_MIN_BARS) return projectWeekSummary(",
		"function projectWeekSummary(",
		"const legend = from === to",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing week-bucket marker %q", marker)
		}
	}

	// The session toolbar wrapped one control at a time, dropping the sort
	// control onto a line of its own. It wraps by group now, and the two long
	// English toggle labels are short enough to fit beside the others.
	for _, marker := range []string{
		`class="fl-scope-group"`,
		`const toggleLabel = (zh, en, value) =>`,
		`toggleLabel("含子代理会话", "Subagents",`,
		`toggleLabel("含空会话", "Empty",`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing session-toolbar marker %q", marker)
		}
	}
	if !strings.Contains(css, ".fl-scope-row > .fl-scope-group") {
		t.Fatal("style.css must make each toolbar group one flex item so the row breaks between groups")
	}
	for _, gone := range []string{"Include subagent sessions (", "Include empty sessions ("} {
		if strings.Contains(appJS, gone) {
			t.Fatalf("app.js still uses the long English toggle label %q", gone)
		}
	}

	// The health label wrapped and squeezed its value onto two lines.
	if !strings.Contains(appJS, `"Sessions main/sub/empty"`) {
		t.Fatal("the database-health session label must be the short form")
	}
	if !strings.Contains(css, ".stats-fl-list > .fl-li > b") {
		t.Fatal("style.css must keep a stats fact value on one line")
	}

	// The daemon writes its explanations in Chinese only. The English page says
	// so rather than letting a reader take it for a missed translation, and the
	// flag tests the text, so it retires itself once Go supplies English.
	for _, marker := range []string{
		"const daemonProse = (zh, en) =>",
		"const daemonCopyFlag = (text) =>",
		"const daemonSentence = (zh, en, tag, cls) =>",
		"HAN_TEXT.test(",
		`class="fl-daemon-copy-flag"`,
		// A9 supplies an English wording beside every Chinese one; the English
		// page reads it, and only a record without one is flagged.
		"daemonProse(hint.mechanism, hint.mechanism_en)",
		"overviewCaliber(parallelism.note, parallelism.note_en)",
		"overviewCaliber(environment.note, environment.note_en)",
		"overviewCaliber(subagents.note, subagents.note_en)",
		"overviewCaliber(reread.note, reread.note_en)",
		"daemonProse(group.category_rule, group.category_rule_en)",
		"daemonProse(record.category_rule, record.category_rule_en)",
		"daemonProse(gap.mechanism, gap.mechanism_en)",
		"daemonProse(view.usageDefinition, view.usageDefinitionEN)",
		`daemonSentence(view.dataPage.sourcesNote, view.dataPage.sourcesNoteEN, "p", "evidence-note")`,
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing daemon-copy-flag marker %q", marker)
		}
	}
	if !strings.Contains(css, ".fl-daemon-copy-flag") {
		t.Fatal("style.css must style the daemon-copy flag")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fetch(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

// The fleet block (ADR-25) is the subagent tree as one unit on the session
// page. The markers pin its contract: it reads the fleet endpoint, leads with
// work tokens, and states git evidence without claiming success.
func TestSessionPageCarriesTheFleetBlock(t *testing.T) {
	handler := Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		"function sessionFleetBlock(",
		"/fleet\"",
		"work_tokens",
		"token_sessions",
		"commits_no_failure",
		"session-fleet-list",
		"session-fleet-previous",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing fleet marker %q", marker)
		}
	}
}

// The now block (P16-3) and the work-token KPI (ADR-25) are the monitor's
// first screen: what is being written right now, and a cost number that is
// not 98% cache reads.
func TestOverviewLeadsWithNowAndWorkTokens(t *testing.T) {
	handler := Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	appJS := rec.Body.String()
	for _, marker := range []string{
		"function overviewNow(",
		"/api/v1/now",
		"live_children",
		"overview-now-card",
		"work_tokens",
		"work_definition",
		"overview-now-loop",
		"item.loop.count",
		"function weeklyStrip(",
		"/api/v1/friction/weekly?signature=",
		"function adherenceCard(",
		"function frictionResolutionRow(",
		"/api/v1/friction/resolution?signature=",
		"/adherence\"",
		"rescued_transcripts",
	} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing overview marker %q", marker)
		}
	}
}
