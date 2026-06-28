package server

import (
	"fmt"
	"sync"
	"time"
)

// ringLog is a fixed-capacity circular buffer of log lines.
type ringLog struct {
	mu    sync.Mutex
	lines_ []string
	cap_   int
	head  int
	full  bool
}

func newRingLog(cap int) *ringLog {
	return &ringLog{cap_: cap, lines_: make([]string, cap)}
}

func (r *ringLog) printf(format string, a ...any) {
	line := fmt.Sprintf(time.Now().Format("15:04:05")+" "+format, a...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines_[r.head] = line
	r.head = (r.head + 1) % r.cap_
	if r.head == 0 {
		r.full = true
	}
}

func (r *ringLog) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]string, r.head)
		copy(out, r.lines_[:r.head])
		return out
	}
	out := make([]string, r.cap_)
	copy(out, r.lines_[r.head:])
	copy(out[r.cap_-r.head:], r.lines_[:r.head])
	return out
}
