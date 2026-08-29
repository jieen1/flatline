// Command flatline is the Flatline daemon entry point.
//
// Usage:
//
//	flatline daemon [-listen 127.0.0.1:8787] [-db /path/to/flatline.db]
//
// The daemon binds loopback only (ADR-1/ADR-2) and is the single data owner.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/adapters/claudecode"
	"flatline/internal/adapters/codex"
	"flatline/internal/adapters/dsh"
	"flatline/internal/adapters/opencode"
	"flatline/internal/api"
	"flatline/internal/assets"
	"flatline/internal/config"
	"flatline/internal/history"
	"flatline/internal/runtime"
	"flatline/internal/storage"
	"flatline/internal/vital"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("flatline: %v", err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no subcommand given")
	}

	switch args[0] {
	case "daemon":
		return runDaemon(args[1:])
	case "status":
		return runStatus(args[1:])
	case "scan":
		return runScan(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: flatline <subcommand> [flags]

subcommands:
  daemon    run the local daemon (loopback API + SQLite)
  status    query the running local daemon (read-only)
  scan      read-only scan an explicit asset root and update local facts
  help      show this help`)
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	listen := fs.String("listen", "", "listen address (default "+config.DefaultListenAddr+"; loopback only)")
	dbPath := fs.String("db", "", "SQLite database path (default: XDG data dir)")
	assetRoot := fs.String("asset-root", "", "asset root to scan read-only on startup and on the scan interval (default: current project directory for project scope)")
	assetScopeValue := fs.String("asset-scope", "project", "scope for -asset-root: project or user")
	userAssetRoot := fs.String("user-asset-root", "", "optional user asset root to scan read-only (defaults to ~/.claude when present)")
	codexAssetRoot := fs.String("codex-asset-root", "", "optional Codex asset root to scan read-only (defaults to ~/.codex when present)")
	claudeHistoryRoot := fs.String("claude-history-root", "", "optional Claude Code JSONL history root (defaults to all local ~/.claude/projects history)")
	codexHistoryRoot := fs.String("codex-history-root", "", "optional Codex JSONL history root (defaults to ~/.codex/sessions)")
	openCodeDB := fs.String("opencode-db", "", "optional opencode SQLite history database, opened read-only (defaults to ~/.local/share/opencode/opencode.db)")
	dshRoot := fs.String("dsh-root", "", "optional dsh session root holding zstd JSONL transcripts (defaults to ~/.dsh/sessions)")
	hermesRoot := fs.String("hermes-root", "", "optional Hermes home to probe for sessions (defaults to ~/.hermes)")
	scanInterval := fs.Duration("scan-interval", 5*time.Minute, "interval for repeated read-only asset scans")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *scanInterval <= 0 {
		return fmt.Errorf("scan-interval must be positive")
	}
	assetScope, err := parseScope(*assetScopeValue)
	if err != nil {
		return err
	}
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current project root: %w", err)
	}
	if *assetRoot == "" && assetScope == assets.ScopeProject {
		*assetRoot = projectRoot
	}
	if *assetRoot != "" && assetScope == assets.ScopeProject {
		projectRoot = *assetRoot
	}
	nativeRoots, err := defaultNativeRoots(projectRoot)
	if err != nil {
		return err
	}
	if *userAssetRoot == "" {
		*userAssetRoot = nativeRoots.UserAssetRoot
	}
	if *codexAssetRoot == "" {
		*codexAssetRoot = nativeRoots.CodexAssetRoot
	}
	if *claudeHistoryRoot == "" {
		*claudeHistoryRoot = nativeRoots.ClaudeHistoryRoot
	}
	if *codexHistoryRoot == "" {
		*codexHistoryRoot = nativeRoots.CodexHistoryRoot
	}
	// A source that is not installed is not an error: the root stays empty and
	// /ingest/health reports status "not_found" for it.
	if *openCodeDB == "" {
		*openCodeDB = nativeRoots.OpenCodeDB
	}
	if *dshRoot == "" {
		*dshRoot = nativeRoots.DSHRoot
	}
	if *hermesRoot == "" {
		*hermesRoot = nativeRoots.HermesRoot
	}

	cfg, err := config.Load(*listen, *dbPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	adapterRegistry, err := defaultAdapters()
	if err != nil {
		return err
	}
	app := runtime.New(db, adapterRegistry, vital.DefaultConfig())
	// The response version continues from the previous process. Starting over
	// would hand a browser a version it already has cached and tell it the old
	// page is current.
	if version, err := app.LoadDataVersion(ctx); err != nil {
		return fmt.Errorf("load data version: %w", err)
	} else {
		log.Printf("data version resumed at %d", version)
	}
	// Every root this run reads from is registered before the first pass, so
	// the data page can name it and turn it off. An existing row keeps the name
	// the user already gave it.
	if err := app.RegisterSourceRoots(ctx, map[adapters.Source][]string{
		adapters.SourceClaudeCode: {*claudeHistoryRoot},
		adapters.SourceCodex:      {*codexHistoryRoot},
		adapters.SourceOpenCode:   {*openCodeDB},
		adapters.SourceDSH:        {*dshRoot},
		adapters.SourceHermes:     {*hermesRoot},
	}); err != nil {
		return fmt.Errorf("register source roots: %w", err)
	}
	// A transcript is filed under the path it really lives at, so the rows an
	// earlier run wrote for a symlink to one are dropped before the
	// fingerprints are loaded: they would otherwise still count as a second
	// transcript of the same session.
	if pruned, err := app.PruneLinkedNativeFiles(ctx); err != nil {
		return fmt.Errorf("prune linked native files: %w", err)
	} else if pruned > 0 {
		log.Printf("native files filed under a symlink dropped=%d", pruned)
	}
	if loaded, err := app.LoadNativeFiles(ctx); err != nil {
		return fmt.Errorf("load native file fingerprints: %w", err)
	} else {
		log.Printf("native file fingerprints loaded=%d", loaded)
	}
	// Unchanged transcript files are no longer replayed, so a tool identity the
	// stored ids never linked, rows written by an older classifier version, and
	// results whose outcome the harness only printed into the output text would
	// never be revisited. These passes are their only chance. They run behind
	// the listener, before the first refresh, and report themselves as the
	// "pairing" phase, because the first one re-reads local transcripts and can
	// take minutes while every endpoint stays answerable.
	catchUp := func() {
		app.BeginPairing(time.Now().UTC())
		defer app.EndPairing()
		// Re-deriving the session projections is the first step of the pass.
		// A counting rule can change without a single new event, so every
		// session stamped with an older projection version is recomputed here
		// — after a version bump that is all of them. This ran in front of the
		// listener until 2026-08-29, which left the API refusing connections
		// for 197 s on a 973-session history, with no way to see why: the
		// endpoint that reports progress was the one not yet up. ADR-18 §4
		// asks the daemon to listen first and import behind it, and this pass
		// is an import like the others.
		app.SetPairingProgress("recounting", 0, 0, 0)
		if filled, err := app.RecomputeMissingSessionStats(ctx); err != nil {
			log.Printf("recompute session stats failed: %v", err)
			app.SetImportError(err)
		} else if filled > 0 {
			log.Printf("session stats backfilled sessions=%d", filled)
		}
		pairing, err := app.BackfillEventPairs(ctx)
		reparse := pairing.Reparse
		// The versioned re-read is the first step of this pass and the only one
		// that can withdraw stored evidence, so its own counts are reported
		// here rather than only inside the pass.
		log.Printf("catch-up reparse files=%d read=%d missing=%d skipped=%d events_inserted=%d events_relocated=%d sessions_refiled=%d evidence_checked=%d evidence_superseded=%d evidence_restored=%d participations_superseded=%d opportunities_superseded=%d warnings=%d",
			reparse.Files, reparse.FilesRead, reparse.FilesMissing, reparse.FilesSkipped,
			reparse.EventsInserted, reparse.EventsRelocated, reparse.SessionsRefiled,
			reparse.EvidenceSessionsChecked, reparse.EvidenceSuperseded, reparse.EvidenceRestored,
			reparse.EvidenceParticipations, reparse.EvidenceOpportunities, reparse.Warnings)
		if err != nil {
			log.Printf("event pairing failed: %v", err)
			app.SetImportError(err)
		} else {
			log.Printf("event pairing projected=%d candidates=%d files_read=%d files_missing=%d reparse_pairs=%d reprojected=%d",
				pairing.Projected, pairing.Candidates, pairing.FilesRead, pairing.FilesMissing,
				pairing.PairsWritten, pairing.Reprojected)
		}
		app.SetPairingProgress("reclassifying", pairing.Candidates, pairing.FilesRead+pairing.FilesMissing, pairing.PairsWritten)
		if recomputed, err := app.ReclassifyFriction(ctx); err != nil {
			log.Printf("friction reclassify failed: %v", err)
		} else if recomputed > 0 {
			log.Printf("friction reclassified rows=%d", recomputed)
		}
		if derived, err := app.DeriveMissingFriction(ctx); err != nil {
			log.Printf("friction derive failed: %v", err)
		} else if derived > 0 {
			log.Printf("friction derived rows=%d", derived)
		}
	}
	refresh := func() error {
		observedAt := time.Now().UTC()
		app.BeginImport(observedAt)
		if *assetRoot != "" {
			report, err := app.ScanAssets(ctx, *assetRoot, assetScope, observedAt)
			if err != nil {
				return fmt.Errorf("asset scan root=%s: %w", *assetRoot, err)
			}
			log.Printf("asset scan complete root=%s discovered=%d versions_created=%d", report.Root, report.Discovered, report.VersionsCreated)
		}
		if *userAssetRoot != "" && filepath.Clean(*userAssetRoot) != filepath.Clean(*assetRoot) {
			report, err := app.ScanAssets(ctx, *userAssetRoot, assets.ScopeUser, observedAt)
			if err != nil {
				return fmt.Errorf("user asset scan root=%s: %w", *userAssetRoot, err)
			}
			log.Printf("user asset scan complete root=%s discovered=%d versions_created=%d", report.Root, report.Discovered, report.VersionsCreated)
		}
		if *codexAssetRoot != "" && filepath.Clean(*codexAssetRoot) != filepath.Clean(*assetRoot) && filepath.Clean(*codexAssetRoot) != filepath.Clean(*userAssetRoot) {
			report, err := app.ScanAssets(ctx, *codexAssetRoot, assets.ScopeUser, observedAt)
			if err != nil {
				return fmt.Errorf("Codex asset scan root=%s: %w", *codexAssetRoot, err)
			}
			log.Printf("Codex asset scan complete root=%s discovered=%d versions_created=%d", report.Root, report.Discovered, report.VersionsCreated)
		}
		app.SetPhase(runtime.PhaseHistory)
		// The roots of this pass come from the registry: a root the user turned
		// off is not read, and a root the user added is.
		historyConfig, err := app.ConfiguredHistory(ctx, history.Config{
			ClaudeRoot: *claudeHistoryRoot, CodexRoot: *codexHistoryRoot,
			OpenCodeDB: *openCodeDB, DSHRoot: *dshRoot, HermesRoot: *hermesRoot,
			IncludeSubagents: true})
		if err != nil {
			return fmt.Errorf("resolve configured sources: %w", err)
		}
		historyReport, err := app.ImportNativeHistory(ctx, historyConfig)
		if err != nil {
			return fmt.Errorf("native history import: %w", err)
		}
		if attached, err := app.AttachSessionSources(ctx); err != nil {
			return fmt.Errorf("attach session sources: %w", err)
		} else if attached > 0 {
			log.Printf("sessions filed under a configured source root=%d", attached)
		}
		log.Printf("native history pass files_seen=%d files_read=%d files_skipped=%d sessions=%d ingested=%d asset_evidence=%d events_inserted=%d warnings=%d", historyReport.FilesSeen, historyReport.FilesRead, historyReport.FilesSkipped, historyReport.SessionsFound, historyReport.SessionsIngested, historyReport.AssetEvidenceFound, historyReport.EventsInserted, len(historyReport.Warnings))
		app.SetImportWarnings(historyReport.Warnings)
		for _, warning := range historyReport.Warnings {
			log.Printf("native history warning: %s", warning)
		}
		app.SetPhase(runtime.PhaseEvaluate)
		evaluation, _, err := app.EvaluateIncremental(ctx, observedAt)
		if err != nil {
			return fmt.Errorf("state evaluation: %w", err)
		}
		log.Printf("state evaluation full=%t evaluated=%d skipped=%d reason=%s", evaluation.Full, evaluation.Evaluated, evaluation.Skipped, evaluation.Reason)
		return nil
	}
	runRefresh := func() {
		if err := refresh(); err != nil {
			app.SetImportError(err)
			log.Printf("refresh failed: %v", err)
		} else {
			app.SetImportError(nil)
		}
		app.FinishImport(time.Now().UTC())
	}

	apiServer := api.NewServer(db)
	apiServer.SetStatusSource(app)
	srv := &http.Server{
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind loopback explicitly. config.ValidateListen already guarantees the
	// address is loopback; net.Listen on that address is the enforcement.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("bind %s: %w", cfg.Listen, err)
	}

	log.Printf("flatline daemon listening on %s (db: %s)", ln.Addr(), cfg.DBPath)

	// The first import runs behind the listener: the UI is reachable while the
	// local history is still being read, and reports progress meanwhile.
	go func() {
		catchUp()
		runRefresh()
		ticker := time.NewTicker(*scanInterval)
		defer ticker.Stop()
		requests := app.RefreshRequests()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runRefresh()
			case <-requests:
				runRefresh()
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received; draining")
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Printf("flatline daemon stopped")
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	listen := fs.String("listen", "", "daemon address (default "+config.DefaultListenAddr+"; loopback only)")
	dbPath := fs.String("db", "", "database path used only to resolve config; status queries the daemon API")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*listen, *dbPath)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + cfg.Listen + "/api/v1/assets")
	if err != nil {
		return fmt.Errorf("status: daemon unavailable at %s: %w", cfg.Listen, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status: daemon returned %s: %s", resp.Status, string(body))
	}
	var payload struct {
		Assets []json.RawMessage `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("status: decode daemon response: %w", err)
	}
	fmt.Printf("flatline: %d assets recorded at %s\n", len(payload.Assets), cfg.Listen)
	return nil
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	root := fs.String("root", "", "asset root to scan (required; source files are read-only)")
	scope := fs.String("scope", "project", "asset scope: project or user")
	dbPath := fs.String("db", "", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("scan: -root is required")
	}
	assetScope, err := parseScope(*scope)
	if err != nil {
		return err
	}
	cfg, err := config.Load("", *dbPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	registry, err := defaultAdapters()
	if err != nil {
		return err
	}
	app := runtime.New(db, registry, vital.DefaultConfig())
	report, err := app.ScanAssets(ctx, *root, assetScope, time.Now().UTC())
	if err != nil {
		return err
	}
	decisions, err := app.EvaluateAll(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	alerts := 0
	for _, decision := range decisions {
		if decision.Alert {
			alerts++
		}
	}
	fmt.Printf("flatline: scanned %s; discovered=%d versions_created=%d assets=%d alerts=%d\n", report.Root, report.Discovered, report.VersionsCreated, len(decisions), alerts)
	return nil
}

func defaultAdapters() (*adapters.Registry, error) {
	registry := adapters.NewRegistry()
	if err := registry.Register(claudecode.New()); err != nil {
		return nil, err
	}
	if err := registry.Register(codex.New()); err != nil {
		return nil, err
	}
	if err := registry.Register(opencode.New()); err != nil {
		return nil, err
	}
	if err := registry.Register(dsh.New()); err != nil {
		return nil, err
	}
	// Hermes has a root to probe but no transcript reader yet, so its source is
	// declared without an adapter. That keeps it out of the adapter registry
	// while still making it a known source for health and for the UI.
	if err := adapters.RegisterSource(adapters.SourceHermes, "Hermes"); err != nil {
		return nil, err
	}
	return registry, nil
}

type nativeRoots struct {
	UserAssetRoot     string
	CodexAssetRoot    string
	ClaudeHistoryRoot string
	CodexHistoryRoot  string
	OpenCodeDB        string
	DSHRoot           string
	HermesRoot        string
}

func defaultNativeRoots(projectRoot string) (nativeRoots, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nativeRoots{}, fmt.Errorf("resolve home directory for native history: %w", err)
	}
	_ = projectRoot
	return nativeRoots{
		UserAssetRoot:     filepath.Join(home, ".claude"),
		CodexAssetRoot:    filepath.Join(home, ".codex"),
		ClaudeHistoryRoot: filepath.Join(home, ".claude", "projects"),
		CodexHistoryRoot:  filepath.Join(home, ".codex", "sessions"),
		OpenCodeDB:        existingPath(filepath.Join(home, ".local", "share", "opencode", "opencode.db")),
		DSHRoot:           existingPath(filepath.Join(home, ".dsh", "sessions")),
		HermesRoot:        existingPath(filepath.Join(home, ".hermes")),
	}, nil
}

// existingPath returns the path only when it is actually there, so a default
// for an uninstalled harness stays empty instead of becoming a root the daemon
// keeps failing to open.
func existingPath(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func parseScope(value string) (assets.Scope, error) {
	switch assets.Scope(value) {
	case assets.ScopeProject, assets.ScopeUser:
		return assets.Scope(value), nil
	default:
		return "", fmt.Errorf("invalid scope %q: want project or user", value)
	}
}
