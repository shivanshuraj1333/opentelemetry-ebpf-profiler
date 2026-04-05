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

func TestParseLineEventKernel(t *testing.T) {
	ev, err := ParseLineEvent([]byte(`{"ver":1,"pid":42,"tid":7,"dev":0,"name":"my_kernel","start_ns":10,"end_ns":100}`))
	require.NoError(t, err)
	require.Equal(t, int32(42), ev.PID)
	require.Equal(t, int32(7), ev.TID)
	require.Equal(t, int32(0), ev.Dev)
	require.Equal(t, "my_kernel", ev.Name)
	require.Equal(t, uint64(10), ev.Start)
	require.Equal(t, uint64(100), ev.End)
}

func TestParseLineEventLaunch(t *testing.T) {
	ev, err := ParseLineEvent([]byte(`{"ver":1,"kind":"launch","pid":1,"tid":1,"correlation_id":99,"frames":["a","b"]}`))
	require.NoError(t, err)
	require.Equal(t, "launch", ev.Kind)
	require.Equal(t, uint64(99), ev.CorrelationID)
	require.Equal(t, []string{"a", "b"}, ev.Frames)
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

func TestHandleLineKernelOrigin(t *testing.T) {
	fr := &fakeReporter{}
	ev := LineEvent{Ver: 1, PID: 1, TID: 1, Dev: 2, Name: "k", Start: 5, End: 15}
	require.NoError(t, HandleLine(fr, nil, nil, ev))
	require.NotNil(t, fr.lastMeta)
	require.Equal(t, libpf.Origin(support.TraceOriginGPU), fr.lastMeta.Origin)
	require.Equal(t, int32(2), fr.lastMeta.GPUDevice)
	require.Equal(t, int64(10), fr.lastMeta.OffTime)
}

func TestCorrelatorMergesLaunchStack(t *testing.T) {
	fr := &fakeReporter{}
	corr := NewCorrelator(0, 0)
	require.NoError(t, HandleLine(fr, nil, corr, LineEvent{
		Ver: 1, Kind: "launch", PID: 1, TID: 1, CorrelationID: 7,
		Frames: []string{"cpu_a", "cpu_b"},
	}))
	require.NoError(t, HandleLine(fr, nil, corr, LineEvent{
		Ver: 1, PID: 1, TID: 1, Dev: 0, Name: "gpu_k", CorrelationID: 7,
		Start: 1, End: 2,
	}))
	require.NotNil(t, fr.lastTrace)
	require.GreaterOrEqual(t, len(fr.lastTrace.Frames), 3, "cpu frames + gpu frame")
}
