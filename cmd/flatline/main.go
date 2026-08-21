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
	refresh := func() error {
		observedAt := time.Now().UTC()
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
		historyReport, err := app.ImportNativeHistory(ctx, history.Config{ClaudeRoot: *claudeHistoryRoot, CodexRoot: *codexHistoryRoot, IncludeSubagents: true})
		if err != nil {
			return fmt.Errorf("native history import: %w", err)
		}
		log.Printf("native history pass files_seen=%d files_read=%d files_skipped=%d sessions=%d ingested=%d asset_evidence=%d events_inserted=%d warnings=%d", historyReport.FilesSeen, historyReport.FilesRead, historyReport.FilesSkipped, historyReport.SessionsFound, historyReport.SessionsIngested, historyReport.AssetEvidenceFound, historyReport.EventsInserted, len(historyReport.Warnings))
		for _, warning := range historyReport.Warnings {
			log.Printf("native history warning: %s", warning)
		}
		if _, err := app.EvaluateAll(ctx, observedAt); err != nil {
			return fmt.Errorf("state evaluation: %w", err)
		}
		return nil
	}
	if err := refresh(); err != nil {
		return fmt.Errorf("initial refresh: %w", err)
	}
	go func() {
		ticker := time.NewTicker(*scanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := refresh(); err != nil {
					log.Printf("refresh failed: %v", err)
				}
			}
		}
	}()

	srv := &http.Server{
		Handler:           api.NewServer(db).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind loopback explicitly. config.ValidateListen already guarantees the
	// address is loopback; net.Listen on that address is the enforcement.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("bind %s: %w", cfg.Listen, err)
	}

	log.Printf("flatline daemon listening on %s (db: %s)", ln.Addr(), cfg.DBPath)

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
	return registry, nil
}

type nativeRoots struct {
	UserAssetRoot     string
	CodexAssetRoot    string
	ClaudeHistoryRoot string
	CodexHistoryRoot  string
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
	}, nil
}

func parseScope(value string) (assets.Scope, error) {
	switch assets.Scope(value) {
	case assets.ScopeProject, assets.ScopeUser:
		return assets.Scope(value), nil
	default:
		return "", fmt.Errorf("invalid scope %q: want project or user", value)
	}
}
