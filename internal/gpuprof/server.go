// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpuprof

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"sync"

	"go.opentelemetry.io/ebpf-profiler/internal/log"
	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/reporter"
)

// DefaultSocketPath is used when GPU profiling is enabled without an explicit path.
const DefaultSocketPath = "/tmp/opentelemetry-ebpf-gpu.sock"

// Server accepts newline-delimited JSON kernel events from the CUDA injection library.
type Server struct {
	socketPath string
	reporter   reporter.TraceReporter
	envVars    libpf.Set[string]

	mu     sync.Mutex
	ln     net.Listener
	closed bool
}

// NewServer creates a GPU profiling socket server. socketPath must be non-empty.
func NewServer(socketPath string, rep reporter.TraceReporter, envVars libpf.Set[string]) *Server {
	return &Server{
		socketPath: socketPath,
		reporter:   rep,
		envVars:    envVars,
	}
}

// Addr returns the configured Unix socket path.
func (s *Server) Addr() string {
	return s.socketPath
}

// Start begins listening; it blocks until ctx is cancelled or Listen fails.
func (s *Server) Start(ctx context.Context) error {
	_ = os.Remove(s.socketPath)
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		log.Warnf("gpu profiling: chmod socket: %v", err)
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	log.Infof("GPU profiling: listening on unix:%s (set CUDA_INJECTION64_PATH + OTEL_CUDA_PROFILER_SOCKET for workloads)", s.socketPath)

	go func() {
		<-ctx.Done()
		s.closeListener()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				break
			}
			log.Warnf("GPU profiling accept: %v", err)
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer c.Close()
			s.serveConn(c)
		}(conn)
	}
	wg.Wait()
	return nil
}

func (s *Server) closeListener() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ln == nil {
		return
	}
	s.closed = true
	_ = s.ln.Close()
}

func (s *Server) serveConn(c net.Conn) {
	sc := bufio.NewScanner(c)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		ev, err := ParseKernelEvent(line)
		if err != nil {
			log.Warnf("GPU profiling: drop line: %v", err)
			continue
		}
		if err := ReportKernelEvent(s.reporter, s.envVars, ev); err != nil {
			log.Warnf("GPU profiling: report: %v", err)
		}
	}
	if err := sc.Err(); err != nil {
		log.Debugf("GPU profiling connection ended: %v", err)
	}
}
