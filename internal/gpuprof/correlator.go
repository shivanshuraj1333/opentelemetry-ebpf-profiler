// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpuprof

import (
	"sync"
	"time"

	"go.opentelemetry.io/ebpf-profiler/libpf"
)

type correlationKey struct {
	pid libpf.PID
	cid uint64
}

type launchRecord struct {
	frames  []string
	expires time.Time
}

// Correlator stores launch-time CPU symbol stacks keyed by (pid, correlation_id) until
// the matching CUPTI kernel activity arrives (zymtrace-style CPU→GPU join).
type Correlator struct {
	mu         sync.Mutex
	pending    map[correlationKey]*launchRecord
	ttl        time.Duration
	maxEntries int
}

// NewCorrelator returns a correlator that drops entries after ttl and caps map size.
func NewCorrelator(ttl time.Duration, maxEntries int) *Correlator {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = 65536
	}
	return &Correlator{
		pending:    make(map[correlationKey]*launchRecord),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

func (c *Correlator) pruneLocked(now time.Time) {
	for k, v := range c.pending {
		if now.After(v.expires) {
			delete(c.pending, k)
		}
	}
	for len(c.pending) > c.maxEntries {
		for k := range c.pending {
			delete(c.pending, k)
			break
		}
	}
}

// StoreLaunch saves CPU-side stack symbols for later merge.
func (c *Correlator) StoreLaunch(pid libpf.PID, correlationID uint64, frames []string) {
	if correlationID == 0 || len(frames) == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	c.pending[correlationKey{pid: pid, cid: correlationID}] = &launchRecord{
		frames:  append([]string(nil), frames...),
		expires: now.Add(c.ttl),
	}
}

// TakeLaunch removes and returns pending frames for (pid, correlationID), if any.
func (c *Correlator) TakeLaunch(pid libpf.PID, correlationID uint64) []string {
	if correlationID == 0 {
		return nil
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	key := correlationKey{pid: pid, cid: correlationID}
	rec, ok := c.pending[key]
	if !ok {
		return nil
	}
	delete(c.pending, key)
	if now.After(rec.expires) {
		return nil
	}
	return rec.frames
}

// FramesFromSymbols converts raw backtrace strings into libpf frames (host / launch side).
func FramesFromSymbols(syms []string) libpf.Frames {
	out := make(libpf.Frames, 0, len(syms))
	for i := range syms {
		name := syms[i]
		if name == "" {
			continue
		}
		out.Append(&libpf.Frame{
			Type:            libpf.UnknownFrame,
			FunctionName:    libpf.Intern(name),
			SourceFile:      libpf.Intern("cuda:launch"),
			AddressOrLineno: libpf.AddressOrLineno(i),
			Mapping:         libpf.FrameMapping{},
		})
	}
	return out
}
