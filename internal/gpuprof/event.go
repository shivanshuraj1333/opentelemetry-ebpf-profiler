// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpuprof

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/process"
	"go.opentelemetry.io/ebpf-profiler/reporter"
	"go.opentelemetry.io/ebpf-profiler/reporter/samples"
	"go.opentelemetry.io/ebpf-profiler/support"
	"go.opentelemetry.io/ebpf-profiler/traceutil"
)

// KernelEvent is the JSON line format written by libotel_cuda_inject.so.
type KernelEvent struct {
	Ver   int    `json:"ver"`
	PID   int32  `json:"pid"`
	TID   int32  `json:"tid"`
	Dev   int32  `json:"dev"`
	Name  string `json:"name"`
	Start uint64 `json:"start_ns"`
	End   uint64 `json:"end_ns"`
}

// ParseKernelEvent decodes one JSON object from the injection library.
func ParseKernelEvent(line []byte) (KernelEvent, error) {
	var ev KernelEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, err
	}
	if ev.Ver != 1 {
		return ev, fmt.Errorf("unsupported event ver %d", ev.Ver)
	}
	if ev.PID <= 0 {
		return ev, fmt.Errorf("invalid pid %d", ev.PID)
	}
	if ev.Name == "" {
		ev.Name = "<unnamed_cuda_kernel>"
	}
	return ev, nil
}

func kernelFrameAddress(name string, dev int32) libpf.AddressOrLineno {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	_, _ = h.Write([]byte{byte(dev), byte(dev >> 8), byte(dev >> 16), byte(dev >> 24)})
	return libpf.AddressOrLineno(h.Sum32())
}

// ReportKernelEvent builds a single-frame GPU trace and reports it.
func ReportKernelEvent(rep reporter.TraceReporter, envVars libpf.Set[string], ev KernelEvent) error {
	dur := int64(ev.End) - int64(ev.Start)
	if dur < 0 {
		dur = 0
	}

	pid := libpf.PID(ev.PID)
	tid := libpf.PID(ev.TID)
	if tid <= 0 {
		tid = pid
	}

	proc := process.New(pid, tid)
	metaCfg := process.MetaConfig{IncludeEnvVars: envVars}
	pm := proc.GetProcessMeta(metaCfg)

	trace := &libpf.Trace{
		Frames: make(libpf.Frames, 0, 1),
	}
	trace.Frames.Append(&libpf.Frame{
		Type:            libpf.UnknownFrame,
		FunctionName:    libpf.Intern(ev.Name),
		SourceFile:      libpf.Intern("cuda:gpu"),
		AddressOrLineno: kernelFrameAddress(ev.Name, ev.Dev),
		FunctionOffset:  0,
		SourceLine:      0,
		SourceColumn:    0,
		Mapping:         libpf.FrameMapping{},
	})
	trace.Hash = traceutil.HashTrace(trace)

	ts := time.Now().UnixNano()
	if ev.End != 0 {
		ts = int64(ev.End)
	}

	tm := &samples.TraceEventMeta{
		Comm:           pm.Name,
		ProcessName:    pm.Name,
		ExecutablePath: pm.Executable,
		ContainerID:    pm.ContainerID,
		EnvVars:        pm.EnvVariables,
		Timestamp:      libpf.UnixTime64(ts),
		CPU:            -1,
		Origin:         libpf.Origin(support.TraceOriginGPU),
		OffTime:        dur,
		PID:            pid,
		TID:            tid,
		GPUDevice:      ev.Dev,
	}

	return rep.ReportTraceEvent(trace, tm)
}
