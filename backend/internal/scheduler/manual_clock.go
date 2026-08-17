package scheduler

import (
	"sort"
	"sync"
	"time"
)

type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	nextID int64
	timers map[int64]*manualTimer
}

func NewManualClock(now time.Time) *ManualClock {
	return &ManualClock{now: now.UTC(), timers: make(map[int64]*manualTimer)}
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	timer := &manualTimer{clock: c, id: c.nextID, due: c.now.Add(d), callback: f}
	c.timers[timer.id] = timer
	return timer
}

func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()

	for {
		due := c.popDue()
		if len(due) == 0 {
			return
		}
		for _, timer := range due {
			timer.callback()
		}
	}
}

func (c *ManualClock) popDue() []*manualTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	due := make([]*manualTimer, 0)
	for id, timer := range c.timers {
		if !timer.stopped && !timer.due.After(c.now) {
			timer.stopped = true
			delete(c.timers, id)
			due = append(due, timer)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].due.Equal(due[j].due) {
			return due[i].id < due[j].id
		}
		return due[i].due.Before(due[j].due)
	})
	return due
}

type manualTimer struct {
	clock    *ManualClock
	id       int64
	due      time.Time
	stopped  bool
	callback func()
}

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := !t.stopped
	t.stopped = true
	delete(t.clock.timers, t.id)
	return active
}

func (t *manualTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := !t.stopped
	t.stopped = false
	t.due = t.clock.now.Add(d)
	t.clock.timers[t.id] = t
	return active
}
