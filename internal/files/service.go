package files

import (
	"context"
	"fmt"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
)

// ServiceMapping is one local↔remote directory pairing the service keeps in
// sync, watched for local changes and polled on Interval for remote ones.
type ServiceMapping struct {
	Remote, Local string
	Opts          SyncOptions
	Interval      time.Duration
}

// Service is the sync-service supervisor: it starts a filesystem watcher and
// an interval ticker per mapping, coalesces their triggers through a
// debouncer into a per-mapping work queue, and runs NewSyncer(...).Run
// sequentially for each queued mapping (the store is single-writer, so passes
// never run concurrently).
type Service struct {
	Client     *api.Client
	Store      *Store
	VK         []byte
	Mappings   []ServiceMapping
	Debounce   time.Duration
	Log        func(string)
	Audit      *audit.Logger
	Heartbeat  func(state, detail string) (wipe bool)                           // may be nil
	NewWatcher func(root string, ignore *Matcher, hidden bool) (watcher, error) // nil → newFSWatcher
}

func (s *Service) logf(format string, a ...any) {
	if s.Log != nil {
		s.Log(fmt.Sprintf(format, a...))
	}
}

// Run starts watchers + tickers and processes a coalescing per-mapping queue
// until ctx is cancelled. Passes run sequentially (the store is single-writer).
func (s *Service) Run(ctx context.Context) error {
	if s.NewWatcher == nil {
		s.NewWatcher = newFSWatcher
	}
	deb := newDebouncer(s.Debounce)
	defer deb.stop()

	byKey := map[string]ServiceMapping{}
	for _, m := range s.Mappings {
		byKey[m.Local] = m
		if w, err := s.NewWatcher(m.Local, m.Opts.Ignore, m.Opts.Hidden); err != nil {
			s.logf("watch %s: %v (interval-only for this mapping)", m.Local, err)
		} else {
			go s.pump(ctx, w, m.Local, deb)
		}
		go s.tick(ctx, m, deb)
	}

	queued := map[string]bool{}
	pending := []string{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case key := <-deb.C():
			if !queued[key] {
				queued[key] = true
				pending = append(pending, key)
			}
		}
		for len(pending) > 0 {
			if ctx.Err() != nil {
				return nil
			}
			key := pending[0]
			pending = pending[1:]
			delete(queued, key)
			m, ok := byKey[key]
			if !ok {
				continue
			}
			if fatal := s.runPass(ctx, m); fatal != nil {
				return fatal
			}
			// Drain any triggers that arrived during the pass without blocking.
			for {
				select {
				case k := <-deb.C():
					if !queued[k] {
						queued[k] = true
						pending = append(pending, k)
					}
					continue
				default:
				}
				break
			}
		}
	}
}

// pump forwards a watcher's events into the debouncer, keyed by the mapping.
func (s *Service) pump(ctx context.Context, w watcher, key string, deb *debouncer) {
	defer w.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.Events():
			if !ok {
				return
			}
			deb.trigger(key)
		case err, ok := <-w.Errors():
			if !ok {
				return
			}
			s.logf("watch %s error: %v", key, err)
		}
	}
}

// tick enqueues a pass for one mapping on its interval (server-change poll).
func (s *Service) tick(ctx context.Context, m ServiceMapping, deb *debouncer) {
	iv := m.Interval
	if iv <= 0 {
		iv = time.Minute
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	deb.trigger(m.Local) // an initial pass at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			deb.trigger(m.Local)
		}
	}
}

// runPass runs one sync pass for a mapping. Kept minimal here — Tasks 5/6
// extend it with a sanity-guard and resilience handling; its signature stays
// stable.
func (s *Service) runPass(ctx context.Context, m ServiceMapping) (fatal error) {
	res, err := NewSyncer(s.Client, s.Store, s.VK, m.Local, m.Remote, m.Opts).Run(ctx)
	if err != nil {
		s.logf("sync %s: %v", m.Local, err)
		return nil
	}
	s.logf("sync %s: ↑%d ↓%d ✗%d !%d", m.Local,
		res.Uploaded, res.Downloaded, res.TrashedRemote+res.DeletedLocal, res.Conflicts)
	return nil
}
