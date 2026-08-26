package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"flatline/internal/history"
)

// LoadNativeFiles restores the transcript fingerprints recorded by earlier
// daemon runs. Without it every restart re-parses the whole local history.
func (a *App) LoadNativeFiles(ctx context.Context) (int, error) {
	if a == nil || a.db == nil {
		return 0, fmt.Errorf("runtime: native file store is not wired")
	}
	rows, err := a.db.QueryContext(ctx, `SELECT path, size, mtime_ns FROM native_files`)
	if err != nil {
		return 0, fmt.Errorf("runtime: load native files: %w", err)
	}
	defer rows.Close()
	loaded := make(map[string]history.FileStamp)
	for rows.Next() {
		var path string
		var size, mtime int64
		if err := rows.Scan(&path, &size, &mtime); err != nil {
			return 0, fmt.Errorf("runtime: scan native file: %w", err)
		}
		loaded[path] = history.FileStamp{Size: size, ModTimeUnixNano: mtime}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("runtime: iterate native files: %w", err)
	}
	a.nativeMu.Lock()
	for path, stamp := range loaded {
		a.nativeFiles[path] = stamp
	}
	a.nativeMu.Unlock()
	return len(loaded), nil
}

func (a *App) persistNativeFiles(ctx context.Context, stamps map[string]history.FileStamp, sessionByPath map[string]string, at time.Time) error {
	if len(stamps) == 0 {
		return nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runtime: begin native file write: %w", err)
	}
	readAt := formatTime(at)
	for path, stamp := range stamps {
		var sessionID any
		if id, ok := sessionByPath[path]; ok && id != "" {
			sessionID = id
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO native_files (path, size, mtime_ns, session_id, last_read_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET
				size = excluded.size,
				mtime_ns = excluded.mtime_ns,
				session_id = COALESCE(excluded.session_id, native_files.session_id),
				last_read_at = excluded.last_read_at`,
			path, stamp.Size, stamp.ModTimeUnixNano, sessionID, readAt); err != nil {
			tx.Rollback()
			return fmt.Errorf("runtime: record native file %s: %w", path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runtime: commit native file write: %w", err)
	}
	return nil
}

// PruneLinkedNativeFiles drops the fingerprint rows written for a path that is
// only a symbolic link to a transcript recorded under its real path, and
// returns how many it dropped.
//
// Discovery now files a transcript under the path it really lives at, so the
// linked path is never read again. The row it left behind is not harmless: it
// makes the session look like it has one more transcript than it has, and the
// evidence channel of §25.7 only reconciles a session whose transcripts were
// all read this pass.
func (a *App) PruneLinkedNativeFiles(ctx context.Context) (int, error) {
	if a == nil || a.db == nil {
		return 0, fmt.Errorf("runtime: native file store is not wired")
	}
	rows, err := a.db.QueryContext(ctx, `SELECT path FROM native_files`)
	if err != nil {
		return 0, fmt.Errorf("runtime: list native files: %w", err)
	}
	var linked []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return 0, fmt.Errorf("runtime: scan native file: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved == path {
			continue
		}
		linked = append(linked, path)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("runtime: iterate native files: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("runtime: close native files: %w", err)
	}
	for _, path := range linked {
		if _, err := a.db.ExecContext(ctx, `DELETE FROM native_files WHERE path = ?`, path); err != nil {
			return 0, fmt.Errorf("runtime: drop linked native file %s: %w", path, err)
		}
		a.nativeMu.Lock()
		delete(a.nativeFiles, path)
		a.nativeMu.Unlock()
	}
	return len(linked), nil
}

// forgetNativeFile drops a fingerprint so the next pass re-reads the file. It
// is used when a parsed file could not be ingested: recording the fingerprint
// anyway would silently retire the file forever.
func (a *App) forgetNativeFile(ctx context.Context, path string) {
	a.nativeMu.Lock()
	delete(a.nativeFiles, path)
	a.nativeMu.Unlock()
	if _, err := a.db.ExecContext(ctx, `DELETE FROM native_files WHERE path = ?`, path); err != nil {
		a.SetImportError(fmt.Errorf("runtime: forget native file %s: %w", path, err))
	}
}
