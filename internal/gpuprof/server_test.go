// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpuprof

import (
	"fmt"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/reporter/samples"
)

type countingReporter struct {
	mu    sync.Mutex
	n     int
	lastM *samples.TraceEventMeta
}

func (c *countingReporter) ReportTraceEvent(_ *libpf.Trace, meta *samples.TraceEventMeta) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	c.lastM = meta
	return nil
}

func (c *countingReporter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestServerServeConn(t *testing.T) {
	pid := int32(os.Getpid())
	line := fmt.Sprintf(`{"ver":1,"pid":%d,"tid":%d,"dev":1,"name":"vec_add","start_ns":100,"end_ns":200}`+"\n", pid, pid)

	rep := &countingReporter{}
	s := NewServer("", rep, nil, nil)

	client, srv := net.Pipe()
	go func() {
		_, _ = client.Write([]byte(line))
		_ = client.Close()
	}()

	s.serveConn(srv)
	_ = srv.Close()

	require.GreaterOrEqual(t, rep.count(), 1)
	require.NotNil(t, rep.lastM)
	require.Equal(t, int32(1), rep.lastM.GPUDevice)
}
