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
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flatline/internal/api"
	"flatline/internal/config"
	"flatline/internal/storage"
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
  help      show this help`)
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	listen := fs.String("listen", "", "listen address (default "+config.DefaultListenAddr+"; loopback only)")
	dbPath := fs.String("db", "", "SQLite database path (default: XDG data dir)")
	if err := fs.Parse(args); err != nil {
		return err
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
