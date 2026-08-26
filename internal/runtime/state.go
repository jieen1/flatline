package runtime

import (
	"context"
	"log"
	"time"
)

// refreshRequests carries a manual refresh from the API to the daemon's serial
// refresh loop. One buffered slot is the whole queue: the loop runs one pass at
// a time, so a second request arriving before the first is picked up asks for
// the same pass.
var refreshRequests = make(chan struct{}, 1)

// RequestRefresh asks the daemon to run a refresh pass now. It never blocks and
// never runs the pass itself: it reports false while an import is in flight, so
// the caller answers "already running" rather than starting a second pass.
func (a *App) RequestRefresh() bool {
	if a.Progress().Phase != PhaseIdle {
		return false
	}
	select {
	case refreshRequests <- struct{}{}:
	default:
	}
	return true
}

// RefreshRequests is what the daemon's refresh loop selects on alongside its
// ticker, which is what keeps ticked and requested passes serial.
func (a *App) RefreshRequests() <-chan struct{} { return refreshRequests }

// ImportProgress is the daemon's own account of the refresh pass that is
// running right now. Every field is an observed count; nothing is estimated.
type ImportProgress struct {
	Phase            string           `json:"phase"`
	FilesSeen        int              `json:"files_seen"`
	FilesRead        int              `json:"files_read"`
	FilesSkipped     int              `json:"files_skipped"`
	SessionsIngested int              `json:"sessions_ingested"`
	StartedAt        *string          `json:"started_at"`
	FinishedAt       *string          `json:"finished_at"`
	LastError        *string          `json:"last_error"`
	Warnings         []string         `json:"warnings,omitempty"`
	Pairing          *PairingProgress `json:"pairing,omitempty"`
	Reparse          *ReparseProgress `json:"reparse,omitempty"`
}

// ReparseProgress is the daemon's account of the versioned re-read: how many
// local transcripts a newer parser still has to read, how many it has read,
// and how many records the older parser had missed. It is present only while
// Phase is PhaseReparse.
type ReparseProgress struct {
	Files          int `json:"files"`
	FilesRead      int `json:"files_read"`
	EventsInserted int `json:"events_inserted"`
}

// PairingProgress is the daemon's account of the one-off pass that links tool
// results to their calls. It is present only while Phase is PhasePairing.
//
// Step names which part is running: "reading" re-reads the transcripts whose
// recorded ids do not line up, "projecting" rebuilds the command, file and
// tool projections from the recovered pairs, and "reclassifying" recomputes
// the friction rows those pairs just gave a tool name to.
type PairingProgress struct {
	Step      string `json:"step"`
	Files     int    `json:"files"`
	FilesRead int    `json:"files_read"`
	Pairs     int    `json:"pairs"`
}

// importWarningBound keeps the daemon's own account small enough to render.
const importWarningBound = 20

const (
	PhaseIdle     = "idle"
	PhaseReparse  = "reparse"
	PhasePairing  = "pairing"
	PhaseAssets   = "assets"
	PhaseHistory  = "history"
	PhaseEvaluate = "evaluate"
)

// BeginPairing opens the catch-up pass that runs behind the listener, before
// the first refresh. It is reported as its own phase because it can take
// minutes on a large local history while every API endpoint stays answerable.
func (a *App) BeginPairing(at time.Time) {
	started := formatTime(at)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress = ImportProgress{Phase: PhasePairing, StartedAt: &started, Pairing: &PairingProgress{Step: "reading"}}
}

// SetPairingProgress publishes the observed counts of the pairing pass. Every
// number is something the pass has already done; nothing is estimated.
func (a *App) SetPairingProgress(step string, files, filesRead, pairs int) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.progress.Pairing == nil {
		a.progress.Pairing = &PairingProgress{}
	}
	a.progress.Pairing.Step = step
	a.progress.Pairing.Files = files
	a.progress.Pairing.FilesRead = filesRead
	a.progress.Pairing.Pairs = pairs
}

// SetReparseProgress publishes the observed counts of the versioned re-read and
// puts the daemon in the reparse phase. Every number is something the pass has
// already done; nothing is estimated.
func (a *App) SetReparseProgress(files, filesRead, eventsInserted int) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.Phase = PhaseReparse
	// Each phase publishes only its own block, so a reader never has to work
	// out which of two progress objects is the live one.
	a.progress.Pairing = nil
	a.progress.Reparse = &ReparseProgress{Files: files, FilesRead: filesRead, EventsInserted: eventsInserted}
}

// EndReparse hands the catch-up back to the pairing phase.
func (a *App) EndReparse() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.Phase = PhasePairing
	a.progress.Reparse = nil
	a.progress.Pairing = &PairingProgress{Step: "reading"}
}

// EndPairing returns the daemon to idle so the refresh loop reports its own
// phases from a clean slate.
func (a *App) EndPairing() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.Phase = PhaseIdle
	a.progress.Pairing = nil
}

// DataVersion is the monotonic counter every cacheable API response is keyed
// on. It changes only when persisted data can have changed.
func (a *App) DataVersion() int64 {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.dataVersion
}

// LoadDataVersion continues the counter from where the previous process left
// it. A restarted daemon that began again at 1 would hand a browser the
// version it already had cached, and the page would keep showing the old
// numbers beside freshly fetched ones.
func (a *App) LoadDataVersion(ctx context.Context) (int64, error) {
	stored, err := a.events.LoadDataVersion(ctx)
	if err != nil {
		return 0, err
	}
	a.stateMu.Lock()
	if stored > a.dataVersion {
		a.dataVersion = stored
	}
	current := a.dataVersion
	a.stateMu.Unlock()
	return current, nil
}

// BumpDataVersion publishes a new version and writes it down first, so a crash
// between the two can only make the next process skip a number, never repeat
// one.
func (a *App) BumpDataVersion() int64 {
	a.stateMu.Lock()
	a.dataVersion++
	current := a.dataVersion
	a.stateMu.Unlock()
	a.persistDataVersion(current)
	return current
}

func (a *App) persistDataVersion(version int64) {
	if a.events == nil {
		return
	}
	if err := a.events.SaveDataVersion(context.Background(), version); err != nil {
		log.Printf("runtime: persist data version %d: %v", version, err)
	}
}

func (a *App) Progress() ImportProgress {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.progress
}

// BeginImport resets the per-pass counters. The previous pass's error is
// cleared only when a new pass starts, so a failure stays visible until then.
func (a *App) BeginImport(at time.Time) {
	started := formatTime(at)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress = ImportProgress{Phase: PhaseAssets, StartedAt: &started}
}

// SetImportWarnings records the verbatim warnings of the pass that is running.
// They are the source's own words: a file that could not be normalized says so
// here rather than disappearing into the log.
func (a *App) SetImportWarnings(warnings []string) {
	if len(warnings) > importWarningBound {
		warnings = warnings[len(warnings)-importWarningBound:]
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.Warnings = append([]string(nil), warnings...)
}

func (a *App) SetPhase(phase string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.Phase = phase
}

func (a *App) SetImportError(err error) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if err == nil {
		a.progress.LastError = nil
		return
	}
	message := err.Error()
	a.progress.LastError = &message
}

// FinishImport closes the pass and publishes a new data version, which is what
// releases every cached API response.
func (a *App) FinishImport(at time.Time) {
	finished := formatTime(at)
	a.stateMu.Lock()
	a.progress.Phase = PhaseIdle
	a.progress.FinishedAt = &finished
	a.dataVersion++
	current := a.dataVersion
	a.stateMu.Unlock()
	a.persistDataVersion(current)
}

func (a *App) setFileCounts(seen, read, skipped int) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.FilesSeen, a.progress.FilesRead, a.progress.FilesSkipped = seen, read, skipped
}

func (a *App) setSessionsIngested(count int) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.progress.SessionsIngested = count
}
