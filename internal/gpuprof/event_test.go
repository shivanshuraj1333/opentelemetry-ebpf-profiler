// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpuprof

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/reporter/samples"
	"go.opentelemetry.io/ebpf-profiler/support"
)

func TestParseKernelEvent(t *testing.T) {
	ev, err := ParseKernelEvent([]byte(`{"ver":1,"pid":42,"tid":7,"dev":0,"name":"my_kernel","start_ns":10,"end_ns":100}`))
	require.NoError(t, err)
	require.Equal(t, int32(42), ev.PID)
	require.Equal(t, int32(7), ev.TID)
	require.Equal(t, int32(0), ev.Dev)
	require.Equal(t, "my_kernel", ev.Name)
	require.Equal(t, uint64(10), ev.Start)
	require.Equal(t, uint64(100), ev.End)
}

func TestParseKernelEventEmptyName(t *testing.T) {
	ev, err := ParseKernelEvent([]byte(`{"ver":1,"pid":1,"tid":1,"dev":0,"name":"","start_ns":0,"end_ns":1}`))
	require.NoError(t, err)
	require.Equal(t, "<unnamed_cuda_kernel>", ev.Name)
}

type fakeReporter struct {
	lastTrace *libpf.Trace
	lastMeta  *samples.TraceEventMeta
	err       error
}

func (f *fakeReporter) ReportTraceEvent(trace *libpf.Trace, meta *samples.TraceEventMeta) error {
	f.lastTrace = trace
	f.lastMeta = meta
	return f.err
}

func TestReportKernelEventOrigin(t *testing.T) {
	fr := &fakeReporter{}
	ev := KernelEvent{Ver: 1, PID: 1, TID: 1, Dev: 2, Name: "k", Start: 5, End: 15}
	// PID 1 may not exist on machine; still exercises code paths that do not require /proc.
	_ = ReportKernelEvent(fr, nil, ev)
	require.NotNil(t, fr.lastMeta)
	require.Equal(t, libpf.Origin(support.TraceOriginGPU), fr.lastMeta.Origin)
	require.Equal(t, int32(2), fr.lastMeta.GPUDevice)
	require.Equal(t, int64(10), fr.lastMeta.OffTime)
}
