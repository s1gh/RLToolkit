package replaywatch

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"rl-toolkit/backend/internal/bus"
)

// Broadcaster is the slim view the watcher needs into the SSE bus.
// *bus.Bus satisfies it; tests use a recording fake.
type Broadcaster interface {
	Broadcast(evt bus.Event)
}

// State is the public snapshot returned by Watcher.State() and shipped
// over /api/replay-watcher.
type State struct {
	Configured   string `json:"configured,omitempty"`
	AutoDetected string `json:"autoDetected,omitempty"`
	Effective    string `json:"effective,omitempty"`
	Status       string `json:"status"`                 // "active" | "inactive"
	StatusReason string `json:"statusReason,omitempty"` // human-readable when inactive
}

// WatcherOptions tunes the watcher; zero values fall back to defaults.
type WatcherOptions struct {
	// StableDelay is how long the file size must remain unchanged
	// before _SavedReplay fires. Defaults to DefaultStableDelay.
	StableDelay time.Duration
}

// Watcher observes a Demos directory and broadcasts _SavedReplay when
// a .replay file finishes being written.
type Watcher struct {
	store *Store
	bus   Broadcaster
	opts  WatcherOptions

	mu    sync.RWMutex
	state State

	// reload is closed-and-recreated on each Reload(). The Run loop
	// selects on it to know when settings changed.
	reloadMu sync.Mutex
	reloadCh chan struct{}
}

// NewWatcher constructs a Watcher. Run must be invoked separately.
func NewWatcher(store *Store, b Broadcaster, opts WatcherOptions) *Watcher {
	if opts.StableDelay == 0 {
		opts.StableDelay = DefaultStableDelay
	}
	return &Watcher{
		store:    store,
		bus:      b,
		opts:     opts,
		reloadCh: make(chan struct{}),
	}
}

// State returns the current public state for /api/replay-watcher.
func (w *Watcher) State() State {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// setState updates state under the write lock.
func (w *Watcher) setState(s State) {
	w.mu.Lock()
	w.state = s
	w.mu.Unlock()
}

// Reload signals Run to tear down the current fsnotify watcher and
// re-resolve the directory. Called by main.go's store.Notify hook.
func (w *Watcher) Reload() {
	w.reloadMu.Lock()
	close(w.reloadCh)
	w.reloadCh = make(chan struct{})
	w.reloadMu.Unlock()
}

// reloadChan returns the current reload channel. The Run loop reads
// this once per session.
func (w *Watcher) reloadChan() chan struct{} {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()
	return w.reloadCh
}

// Run drives the watcher until ctx is canceled. Each iteration of the
// outer loop boots one fsnotify watcher; Reload() and ctx.Done() both
// break out and re-resolve.
func (w *Watcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		w.runSession(ctx)
		// runSession returns when ctx is canceled or Reload is called;
		// the outer loop re-resolves on the next iteration. If ctx is
		// already done, the top-of-loop select catches it.
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// runSession resolves the current effective dir and runs one fsnotify
// loop until ctx or reload signals.
func (w *Watcher) runSession(ctx context.Context) {
	reload := w.reloadChan()

	configured := w.store.Get()
	auto, autoSrc := ResolveDir("", runtime.GOOS, os.Getenv, homeDir(), pathExists)
	effective, effSrc := ResolveDir(configured, runtime.GOOS, os.Getenv, homeDir(), pathExists)
	autoPath := ""
	if autoSrc == "auto" {
		autoPath = auto
	}

	if effSrc == "none" {
		w.setState(State{
			Configured:   configured,
			AutoDetected: autoPath,
			Effective:    "",
			Status:       "inactive",
			StatusReason: "directory not found",
		})
		w.waitForReloadOrCancel(ctx, reload)
		return
	}

	if !pathExists(effective) {
		w.setState(State{
			Configured:   configured,
			AutoDetected: autoPath,
			Effective:    effective,
			Status:       "inactive",
			StatusReason: "directory not found: " + effective,
		})
		log.Printf("[replaywatch] directory not found: %s", effective)
		w.waitForReloadOrCancel(ctx, reload)
		return
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		w.setState(State{
			Configured:   configured,
			AutoDetected: autoPath,
			Effective:    effective,
			Status:       "inactive",
			StatusReason: "watcher error: " + err.Error(),
		})
		log.Printf("[replaywatch] NewWatcher: %v", err)
		w.waitForReloadOrCancel(ctx, reload)
		return
	}
	defer fsw.Close()

	if err := fsw.Add(effective); err != nil {
		w.setState(State{
			Configured:   configured,
			AutoDetected: autoPath,
			Effective:    effective,
			Status:       "inactive",
			StatusReason: "watcher error: " + err.Error(),
		})
		log.Printf("[replaywatch] Add(%s): %v", effective, err)
		w.waitForReloadOrCancel(ctx, reload)
		return
	}

	w.setState(State{
		Configured:   configured,
		AutoDetected: autoPath,
		Effective:    effective,
		Status:       "active",
	})
	log.Printf("[replaywatch] watching %s", effective)

	debouncer := newDebouncer(w.opts.StableDelay, func(evt savedReplayPayload) {
		w.fire(evt)
	})
	defer debouncer.stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-reload:
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			// Only Create/Write are interesting; Remove/Rename clear
			// any pending entry for that path.
			if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				if isReplayFile(ev.Name) {
					debouncer.touch(ev.Name)
				}
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				debouncer.drop(ev.Name)
			}
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			log.Printf("[replaywatch] fsnotify error: %v", err)
		}
	}
}

func (w *Watcher) waitForReloadOrCancel(ctx context.Context, reload chan struct{}) {
	select {
	case <-ctx.Done():
	case <-reload:
	}
}

// savedReplayPayload is the JSON shape broadcast as _SavedReplay.Data.
type savedReplayPayload struct {
	MatchGUID string `json:"matchGuid"`
	FileName  string `json:"fileName"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SavedAt   string `json:"savedAt"`
}

// fire builds the payload and broadcasts on the bus.
func (w *Watcher) fire(p savedReplayPayload) {
	body, err := json.Marshal(p)
	if err != nil {
		log.Printf("[replaywatch] marshal _SavedReplay: %v", err)
		return
	}
	w.bus.Broadcast(bus.Event{Name: "_SavedReplay", Data: body})
}

// isReplayFile is the basename filter — case-insensitive ".replay".
func isReplayFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".replay")
}

// pathExists is the production existsFn passed to ResolveDir. Returns
// true for any os.Stat that succeeds, regardless of file type.
func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// homeDir is wrapped so tests don't fail in stripped CI environments.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// ── debouncer ──────────────────────────────────────────────────

// debouncer tracks pending files and fires the callback when the file
// size has been stable for the configured delay. One goroutine owns the
// map; events arrive over the touch/drop channels.
type debouncer struct {
	delay  time.Duration
	fire   func(savedReplayPayload)
	touchC chan string
	dropC  chan string
	stopC  chan struct{}
	doneC  chan struct{}
}

type pendingFile struct {
	lastSize int64
	timer    *time.Timer
	expires  time.Time
}

func newDebouncer(delay time.Duration, fire func(savedReplayPayload)) *debouncer {
	d := &debouncer{
		delay:  delay,
		fire:   fire,
		touchC: make(chan string, 64),
		dropC:  make(chan string, 64),
		stopC:  make(chan struct{}),
		doneC:  make(chan struct{}),
	}
	go d.run()
	return d
}

func (d *debouncer) touch(path string) {
	select {
	case d.touchC <- path:
	case <-d.stopC:
	}
}

func (d *debouncer) drop(path string) {
	select {
	case d.dropC <- path:
	case <-d.stopC:
	}
}

func (d *debouncer) stop() {
	close(d.stopC)
	<-d.doneC
}

// run is the single goroutine that owns the pending map. Timer
// expirations come back via expiredC.
func (d *debouncer) run() {
	defer close(d.doneC)
	pending := make(map[string]*pendingFile)
	expiredC := make(chan string, 64)

	for {
		select {
		case <-d.stopC:
			for _, p := range pending {
				if p.timer != nil {
					p.timer.Stop()
				}
			}
			return
		case path := <-d.touchC:
			info, err := os.Stat(path)
			if err != nil {
				delete(pending, path)
				continue
			}
			size := info.Size()
			p, ok := pending[path]
			if !ok {
				p = &pendingFile{}
				pending[path] = p
			}
			p.lastSize = size
			p.expires = time.Now().Add(d.delay)
			if p.timer != nil {
				p.timer.Stop()
			}
			pathCopy := path
			p.timer = time.AfterFunc(d.delay, func() {
				select {
				case expiredC <- pathCopy:
				case <-d.stopC:
				}
			})
		case path := <-d.dropC:
			if p, ok := pending[path]; ok {
				if p.timer != nil {
					p.timer.Stop()
				}
				delete(pending, path)
			}
		case path := <-expiredC:
			p, ok := pending[path]
			if !ok {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				delete(pending, path)
				continue
			}
			if info.Size() != p.lastSize {
				// Still growing — re-arm.
				p.lastSize = info.Size()
				p.expires = time.Now().Add(d.delay)
				if p.timer != nil {
					p.timer.Stop()
				}
				pathCopy := path
				p.timer = time.AfterFunc(d.delay, func() {
					select {
					case expiredC <- pathCopy:
					case <-d.stopC:
					}
				})
				continue
			}
			// Stable — fire and forget.
			delete(pending, path)
			d.fire(savedReplayPayload{
				MatchGUID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
				FileName:  filepath.Base(path),
				Path:      path,
				SizeBytes: info.Size(),
				SavedAt:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			})
		}
	}
}
