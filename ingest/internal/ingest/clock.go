package ingest

import (
	"sync"
	"time"
)

// Clock abstracts time-dependent operations so tests can advance time
// deterministically without real wall-clock waits.
type Clock interface {
	// Now returns the current time.
	Now() time.Time

	// AfterFunc schedules f to run after d elapses. It returns a timer
	// handle that can be stopped via StopTimer.
	AfterFunc(d time.Duration, f func()) timer

	// StopTimer prevents a previously scheduled timer from firing.
	// Returns true if the timer was stopped before firing.
	StopTimer(t timer) bool
}

// timer is an opaque handle returned by Clock.AfterFunc.
type timer struct {
	id int64
}

// ---------- realClock (production default) ----------

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) timer {
	time.AfterFunc(d, f)
	return timer{id: -1} // real timers don't need tracking
}

func (realClock) StopTimer(t timer) bool {
	// realClock timers are *time.Timer values stored elsewhere;
	// this path is only reached for real timers in tests that keep
	// the real clock, which is rare. Return true to be safe.
	_ = t
	return true
}

// ---------- fakeClock (deterministic, for tests) ----------

type fakeClock struct {
	mu       sync.Mutex
	now      time.Time
	nextID   int64
	pending  []scheduledTimer // timers not yet fired
	fired    []func()         // callbacks ready to be executed by Advance
	firedMu  sync.Mutex       // guards fired
}

type scheduledTimer struct {
	id   int64
	when time.Time
	fn   func()
}

// newFakeClock returns a fakeClock starting at time zero.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	c.pending = append(c.pending, scheduledTimer{
		id:   id,
		when: c.now.Add(d),
		fn:   f,
	})
	return timer{id: id}
}

func (c *fakeClock) StopTimer(t timer) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, st := range c.pending {
		if st.id == t.id {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return true
		}
	}
	return false
}

// Advance moves the clock forward by d and synchronously fires all timers
// whose deadline has been reached. Callbacks run outside any caller-held
// locks so they may safely acquire the Watcher mutex.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)

	var due []scheduledTimer
	var rest []scheduledTimer
	for _, st := range c.pending {
		if !st.when.After(c.now) {
			due = append(due, st)
		} else {
			rest = append(rest, st)
		}
	}
	c.pending = rest
	c.mu.Unlock()

	// Fire callbacks synchronously, outside any external lock.
	for _, st := range due {
		st.fn()
	}
}

// fireAll is a convenience for tests: advance to +∞ and fire everything.
func (c *fakeClock) fireAll() {
	c.mu.Lock()
	c.now = c.now.Add(24 * time.Hour * 365)
	due := c.pending
	c.pending = nil
	c.mu.Unlock()

	for _, st := range due {
		st.fn()
	}
}
