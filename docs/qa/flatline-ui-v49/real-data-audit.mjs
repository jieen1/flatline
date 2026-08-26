#!/usr/bin/env node

/**
 * Read-only smoke audit for the local daemon's persisted data.
 *
 * This intentionally records counts, shapes, and evidence-presence checks,
 * but never writes session titles, task text, paths, locators, or transcript
 * content to the QA artifact. The daemon remains the only data owner.
 */

import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

const baseURL = (process.argv[2] || "http://127.0.0.1:18899").replace(/\/$/, "");
const outputPath = process.argv[3] || path.resolve("docs/qa/flatline-ui-v49/real-data-audit.json");
const validObservationLevels = new Set(["invoked", "observed-use", "loaded", "offered", "inferred", "unknown"]);

async function get(endpoint) {
  const response = await fetch(baseURL + endpoint, { headers: { Accept: "application/json" } });
  const text = await response.text();
  let body;
  try {
    body = JSON.parse(text);
  } catch (error) {
    throw new Error(`${endpoint} returned non-JSON (${response.status}): ${error.message}`);
  }
  if (!response.ok) {
    throw new Error(`${endpoint} returned HTTP ${response.status}`);
  }
  return { status: response.status, body };
}

function increment(map, key) {
  map[key] = (map[key] || 0) + 1;
}

function eventTypeCounts(events) {
  return events.reduce((counts, event) => {
    increment(counts, event.event_type || "unknown");
    return counts;
  }, {});
}

function observationCounts(events) {
  return events.reduce((counts, event) => {
    increment(counts, event.observation_level || "unknown");
    return counts;
  }, {});
}

function uniqueTurnIds(events) {
  const turnIDs = new Set();
  for (const event of events) {
    const turnID = event?.payload?.turn_id;
    if (typeof turnID === "string" && turnID.trim() !== "") turnIDs.add(turnID);
  }
  return turnIDs.size;
}

function hasAny(value) {
  return Array.isArray(value) ? value.length > 0 : value !== null && value !== undefined && value !== "";
}

function digest(value) {
  return crypto.createHash("sha256").update(String(value)).digest("hex").slice(0, 12);
}

function sortedSessions(sessions) {
  return [...sessions].sort((a, b) => {
    const aInRange = a.event_count >= 1000 && a.event_count <= 10000;
    const bInRange = b.event_count >= 1000 && b.event_count <= 10000;
    if (aInRange !== bInRange) return aInRange ? -1 : 1;
    if (b.event_count !== a.event_count) return b.event_count - a.event_count;
    return String(a.id).localeCompare(String(b.id));
  });
}

const checkedAt = new Date().toISOString();
const statsResult = await get("/api/v1/stats");
const assetsResult = await get("/api/v1/assets?limit=5000");
const sessionsResult = await get("/api/v1/sessions?limit=5000");
const timelineResult = await get("/api/v1/timeline?limit=5000");

const assets = Array.isArray(assetsResult.body.assets) ? assetsResult.body.assets : [];
const sessions = Array.isArray(sessionsResult.body.sessions) ? sessionsResult.body.sessions : [];
const relatedAsset = assets
  .filter((item) => item?.facts?.opportunity_count > 0 && item?.facts?.participation_count > 0)
  .sort((a, b) => (b.facts.opportunity_count || 0) - (a.facts.opportunity_count || 0))[0];
const noOpportunityAsset = assets.find((item) => item?.current_state?.state === "no_opportunity");
if (!relatedAsset) throw new Error("No real asset with both opportunity and participation records was found");
if (!noOpportunityAsset) throw new Error("No real asset with the no_opportunity state was found");

const relatedResult = await get("/api/v1/assets/" + encodeURIComponent(relatedAsset.id));
const noOpportunityResult = await get("/api/v1/assets/" + encodeURIComponent(noOpportunityAsset.id));
const selectedSession = sortedSessions(sessions)[0];
if (!selectedSession) throw new Error("No persisted session was found");
const sessionResult = await get("/api/v1/sessions/" + encodeURIComponent(selectedSession.id));

const related = relatedResult.body;
const noOpportunity = noOpportunityResult.body;
const session = sessionResult.body.session || {};
const events = Array.isArray(sessionResult.body.events) ? sessionResult.body.events : [];
const timeline = Array.isArray(timelineResult.body.timeline) ? timelineResult.body.timeline : [];
const timelineKinds = timeline.reduce((counts, item) => {
  increment(counts, item.kind || "unknown");
  return counts;
}, {});
const rawLocatorCount = events.filter((event) => event?.locator && typeof event.locator === "object" && hasAny(event.locator.raw_ref)).length;
const eventObservationLevels = Object.keys(observationCounts(events));
const relatedObservationLevels = related.asset?.facts?.observation_levels || [];
const noOpportunitySteps = noOpportunity.funnel?.current?.steps || [];
const opportunitySessionIDs = new Set((related.opportunities || []).map((item) => item.session_id).filter(Boolean));
const relatedSessions = Array.isArray(related.related_sessions) ? related.related_sessions : [];
const relatedSessionsWithTaskMetadata = relatedSessions.filter((item) => opportunitySessionIDs.has(item.id) && (hasAny(item.title) || hasAny(item.task_text))).length;

const checks = {
  statsHTTP200: statsResult.status === 200,
  assetsHTTP200: assetsResult.status === 200,
  sessionsHTTP200: sessionsResult.status === 200,
  timelineHTTP200: timelineResult.status === 200,
  relatedAssetHTTP200: relatedResult.status === 200,
  noOpportunityAssetHTTP200: noOpportunityResult.status === 200,
  sessionHTTP200: sessionResult.status === 200,
  persistedCountsNonZero: statsResult.body.asset_count > 0 && statsResult.body.session_count > 0 && statsResult.body.event_count > 0,
  relatedAssetHasOpportunityAndParticipation:
    related.asset?.facts?.opportunity_count > 0 && related.asset?.facts?.participation_count > 0,
  relatedAssetHasAssociatedDetailRows:
    (related.opportunities?.length || 0) > 0 && (related.participations?.length || 0) > 0,
  relatedAssetSessionsExposeTaskMetadata:
    relatedSessions.length > 0 && relatedSessionsWithTaskMetadata > 0,
  relatedAssetHasVersionAndObservation:
    (related.versions?.length || 0) > 0 && relatedObservationLevels.every((level) => validObservationLevels.has(level)),
  relatedAssetSparklineUsesPersistedPoints: (related.asset?.facts?.sparkline?.length || 0) > 0,
  noOpportunityStateIsExplicit: noOpportunity.current_state?.state === "no_opportunity",
  noOpportunityRateIsNotFabricated:
    noOpportunity.asset?.facts?.current_participation_numerator === undefined &&
    noOpportunity.asset?.facts?.current_participation_denominator === undefined &&
    (noOpportunity.asset?.facts?.sparkline?.length || 0) === 0 &&
    noOpportunitySteps.every((step) => step.numerator === undefined && step.denominator === undefined),
  sessionEventsMatchPersistedCount: events.length === session.event_count && session.event_count > 1,
  sessionHasHumanReadableMetadata: hasAny(session.title) || hasAny(session.task_text),
  sessionHasTurnEvidence: uniqueTurnIds(events) > 0,
  sessionHasTranscriptEvidence: events.some((event) => String(event.event_type).startsWith("transcript_")),
  sessionPreservesRawLocators: rawLocatorCount > 0,
  sessionObservationLevelsAreClosed: eventObservationLevels.every((level) => validObservationLevels.has(level)),
  timelineHasPersistedFacts: timeline.length > 0 && Object.keys(timelineKinds).length > 0,
  timelineHasEnvironmentAlignment: timeline.some((item) => item.kind === "environment_changed" && hasAny(item.alignment)),
};

const report = {
  checkedAt,
  baseURL,
  source: "daemon_owned_sqlite",
  privacy: {
    rawSessionContentPersisted: false,
    selectedSessionKey: `${session.source || "unknown"}:${digest(selectedSession.id)}`,
  },
  http: {
    stats: statsResult.status,
    assets: assetsResult.status,
    sessions: sessionsResult.status,
    timeline: timelineResult.status,
    relatedAsset: relatedResult.status,
    noOpportunityAsset: noOpportunityResult.status,
    selectedSession: sessionResult.status,
  },
  counts: {
    assets: statsResult.body.asset_count,
    versions: statsResult.body.version_count,
    sessions: statsResult.body.session_count,
    events: statsResult.body.event_count,
    opportunities: statsResult.body.opportunity_count,
    participations: statsResult.body.participation_count,
    timelineItems: timeline.length,
    timelineClusters: Array.isArray(timelineResult.body.clusters) ? timelineResult.body.clusters.length : 0,
  },
  relatedAsset: {
    id: relatedAsset.id,
    state: related.current_state?.state || null,
    versionCount: related.asset?.facts?.version_count || 0,
    sessionCount: related.asset?.facts?.session_count || 0,
    opportunityCount: related.asset?.facts?.opportunity_count || 0,
    participationCount: related.asset?.facts?.participation_count || 0,
    detailOpportunityRows: related.opportunities?.length || 0,
    detailParticipationRows: related.participations?.length || 0,
    relatedSessionCount: relatedSessions.length,
    relatedSessionsWithTaskMetadata,
    observationLevels: relatedObservationLevels,
    sparklinePointCount: related.asset?.facts?.sparkline?.length || 0,
    changeMarkerKinds: [...new Set((related.asset?.facts?.change_markers || []).map((marker) => marker.kind))],
    referenceCheckCount: related.reference_checks?.length || 0,
  },
  noOpportunityAsset: {
    id: noOpportunityAsset.id,
    state: noOpportunity.current_state?.state || null,
    versionCount: noOpportunity.asset?.facts?.version_count || 0,
    opportunityCount: noOpportunity.asset?.facts?.opportunity_count || 0,
    participationCount: noOpportunity.asset?.facts?.participation_count || 0,
    hasCurrentNumerator: noOpportunity.asset?.facts?.current_participation_numerator !== undefined,
    hasCurrentDenominator: noOpportunity.asset?.facts?.current_participation_denominator !== undefined,
    sparklinePointCount: noOpportunity.asset?.facts?.sparkline?.length || 0,
    funnelStepCount: noOpportunitySteps.length,
    funnelStepNumerators: noOpportunitySteps.map((step) => step.numerator ?? null),
    funnelStepDenominators: noOpportunitySteps.map((step) => step.denominator ?? null),
    hasDecisionEvidence: hasAny(noOpportunity.current_state?.evidence),
  },
  selectedSession: {
    source: session.source || null,
    eventCount: session.event_count || 0,
    eventRowsReturned: events.length,
    transcriptCount: session.transcript_count || 0,
    assetCount: session.asset_count || 0,
    hasTitle: hasAny(session.title),
    hasTaskText: hasAny(session.task_text),
    uniqueTurnIDs: uniqueTurnIds(events),
    rawLocatorCount,
    eventTypeCounts: eventTypeCounts(events),
    observationLevelCounts: observationCounts(events),
  },
  timeline: {
    kindCounts: timelineKinds,
    hasEnvironmentAlignment: checks.timelineHasEnvironmentAlignment,
  },
  checks,
  allPassed: Object.values(checks).every(Boolean),
};

await fs.mkdir(path.dirname(outputPath), { recursive: true });
await fs.writeFile(outputPath, JSON.stringify(report, null, 2) + "\n", "utf8");
console.log(JSON.stringify({
  outputPath,
  allPassed: report.allPassed,
  counts: report.counts,
  relatedAsset: report.relatedAsset,
  noOpportunityAsset: report.noOpportunityAsset,
  selectedSession: {
    source: report.selectedSession.source,
    eventCount: report.selectedSession.eventCount,
    transcriptCount: report.selectedSession.transcriptCount,
    uniqueTurnIDs: report.selectedSession.uniqueTurnIDs,
  },
  failedChecks: Object.entries(checks).filter(([, passed]) => !passed).map(([name]) => name),
}, null, 2));

if (!report.allPassed) process.exitCode = 1;
