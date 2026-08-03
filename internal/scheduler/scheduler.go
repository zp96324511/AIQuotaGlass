package scheduler

import (
	"context"
	"sync"
	"time"
)

// Scheduler runs a tick function at a configurable interval.
// It never runs two ticks concurrently and can be stopped/resumed.
type Scheduler struct {
	mu         sync.Mutex
	tickMu     sync.Mutex
	tick       func(context.Context)
	interval   time.Duration
	cancel     context.CancelFunc
	running    bool
	generation uint64
}

// New creates a scheduler with the given tick callback.
func New(interval time.Duration, tick func(context.Context)) *Scheduler {
	return &Scheduler{tick: tick, interval: interval}
}

// SetInterval updates the refresh interval; it applies on the next cycle.
func (s *Scheduler) SetInterval(d time.Duration) {
	s.mu.Lock()
	s.interval = d
	s.mu.Unlock()
}

// Start launches the periodic loop. If already running it is restarted.
func (s *Scheduler) Start() {
	s.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = true
	s.generation++
	generation := s.generation
	go func() {
		for {
			s.mu.Lock()
			interval := s.interval
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			s.runOnce(ctx, generation)
		}
	}()
}

// RunNow executes one tick immediately (guarded against overlap).
func (s *Scheduler) RunNow() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s.mu.Lock()
	running := s.running
	generation := s.generation
	s.mu.Unlock()
	if !running {
		return
	}
	s.runOnce(ctx, generation)
}

func (s *Scheduler) runOnce(ctx context.Context, generation uint64) {
	s.mu.Lock()
	if !s.running || generation != s.generation {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	s.mu.Lock()
	if !s.running || generation != s.generation {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if s.tick != nil {
		s.tick(ctx)
	}
}

// Stop halts the periodic loop.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.running = false
}
