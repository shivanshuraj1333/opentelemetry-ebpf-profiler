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

// LineEvent is the JSON line format from libotel_cuda_inject.so (ver 1).
// kind: omitted or "kernel" (default), "launch" (CPU stack at cuda launch), "memcpy",
// "pcsample" (CUPTI PC sampling / stall metadata when enabled in the inject library).
// schema is optional JSON protocol revision (1 = current); ver remains the top-level format.
type LineEvent struct {
	Ver           int      `json:"ver"`
	Schema        int      `json:"schema,omitempty"`
	Kind          string   `json:"kind"`
	PID           int32    `json:"pid"`
	TID           int32    `json:"tid"`
	Dev           int32    `json:"dev"`
	Name          string   `json:"name"`
	Start         uint64   `json:"start_ns"`
	End           uint64   `json:"end_ns"`
	CorrelationID uint64   `json:"correlation_id"`
	Frames        []string `json:"frames"`
	Bytes         uint64   `json:"bytes"`
	CopyKind      string   `json:"copy_kind"`
	StreamID      uint32   `json:"stream_id"`
	PCOffset      uint64   `json:"pc_offset,omitempty"`
	StallReason   uint32   `json:"stall_reason,omitempty"`
	Samples       uint32   `json:"samples,omitempty"`
}

// ParseLineEvent decodes one JSON object from the injection library.
func ParseLineEvent(line []byte) (LineEvent, error) {
	var ev LineEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, err
	}
	if ev.Ver != 1 {
		return ev, fmt.Errorf("unsupported event ver %d", ev.Ver)
	}
	if ev.Schema != 0 && ev.Schema != 1 {
		return ev, fmt.Errorf("unsupported event schema %d", ev.Schema)
	}
	if ev.PID <= 0 {
		return ev, fmt.Errorf("invalid pid %d", ev.PID)
	}
	return ev, nil
}

func kernelFrameAddress(name string, dev int32) libpf.AddressOrLineno {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	_, _ = h.Write([]byte{byte(dev), byte(dev >> 8), byte(dev >> 16), byte(dev >> 24)})
	return libpf.AddressOrLineno(h.Sum32())
}

// HandleLine dispatches a parsed event: launch correlation, memcpy, or kernel (with optional merge).
func HandleLine(rep reporter.TraceReporter, envVars libpf.Set[string], corr *Correlator, ev LineEvent) error {
	kind := ev.Kind
	if kind == "" {
		kind = "kernel"
	}
	switch kind {
	case "launch":
		return handleLaunch(corr, ev)
	case "memcpy":
		return reportMemcpy(rep, envVars, corr, ev)
	case "pcsample":
		return reportPCSample(rep, envVars, corr, ev)
	default:
		return reportKernel(rep, envVars, corr, ev)
	}
}

func handleLaunch(corr *Correlator, ev LineEvent) error {
	if corr == nil {
		return nil
	}
	pid := libpf.PID(ev.PID)
	corr.StoreLaunch(pid, ev.CorrelationID, ev.Frames)
	return nil
}

func reportPCSample(rep reporter.TraceReporter, envVars libpf.Set[string], corr *Correlator, ev LineEvent) error {
	// Stall reason is a CUpti_ActivityPCSamplingStallReason value; decode names in tooling if needed.
	kname := fmt.Sprintf("pc+0x%x stall#%d", ev.PCOffset, ev.StallReason)
	if ev.Samples > 0 {
		kname = fmt.Sprintf("%s (x%d)", kname, ev.Samples)
	}
	ev2 := LineEvent{
		Ver: ev.Ver, Schema: ev.Schema, Kind: "kernel", PID: ev.PID, TID: ev.TID, Dev: ev.Dev,
		Name: kname, Start: ev.Start, End: ev.End, CorrelationID: ev.CorrelationID,
	}
	return reportKernel(rep, envVars, corr, ev2)
}

func reportMemcpy(rep reporter.TraceReporter, envVars libpf.Set[string], corr *Correlator, ev LineEvent) error {
	name := ev.Name
	if name == "" {
		name = "cudaMemcpy"
		if ev.CopyKind != "" {
			name = "cudaMemcpy (" + ev.CopyKind + ")"
		}
	}
	ev2 := LineEvent{
		Ver: ev.Ver, Kind: "kernel", PID: ev.PID, TID: ev.TID, Dev: ev.Dev,
		Name: name, Start: ev.Start, End: ev.End, CorrelationID: ev.CorrelationID,
	}
	return reportKernel(rep, envVars, corr, ev2)
}

func reportKernel(rep reporter.TraceReporter, envVars libpf.Set[string], corr *Correlator, ev LineEvent) error {
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

	trace := &libpf.Trace{Frames: make(libpf.Frames, 0, 64)}
	if corr != nil && ev.CorrelationID != 0 {
		if syms := corr.TakeLaunch(pid, ev.CorrelationID); len(syms) > 0 {
			hostFrames := FramesFromSymbols(syms)
			trace.Frames = append(trace.Frames, hostFrames...)
		}
	}

	if ev.Name == "" {
		ev.Name = "<unnamed_cuda_kernel>"
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
