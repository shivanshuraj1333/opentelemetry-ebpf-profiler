// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package gpumetrics collects host GPU telemetry via nvidia-smi and optionally
// records OTLP metrics when a MeterProvider is configured (Collector receiver or
// standalone main when -gpu-metrics and -collection-agent are set).
package gpumetrics // import "go.opentelemetry.io/ebpf-profiler/internal/gpumetrics"

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/ebpf-profiler/internal/log"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Config controls GPU metrics collection.
type Config struct {
	MeterProvider metric.MeterProvider
	Interval      time.Duration
}

// Start polls nvidia-smi until ctx is cancelled.
func Start(ctx context.Context, cfg Config) {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	var sink *otlpSink
	if cfg.MeterProvider != nil {
		sink = newOTLPSink(cfg.MeterProvider)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snapshot(ctx, sink)
		}
	}
}

func snapshot(ctx context.Context, sink *otlpSink) {
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(qctx, "nvidia-smi",
		"--query-gpu=index,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		log.Debugf("gpu metrics: nvidia-smi: %v", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		s, ok := parseSMILine(line)
		if !ok {
			log.Debugf("gpu metrics: skip unparsable line: %q", line)
			continue
		}
		if sink != nil {
			sink.record(qctx, s)
		}
		log.Debugf("gpu metrics: gpu %d util=%d%% mem=%d/%d MiB temp=%d°C power=%s",
			s.Index, s.UtilPct, s.MemUsedMiB, s.MemTotalMiB, s.TempC, s.PowerRaw)
	}
}

// gpuSample holds one row from nvidia-smi CSV output.
type gpuSample struct {
	Index       int
	UtilPct     int64
	MemUsedMiB  int64
	MemTotalMiB int64
	TempC       int64
	PowerW      float64
	HasPower    bool
	PowerRaw    string
}

func parseSMILine(line string) (gpuSample, bool) {
	parts := strings.Split(line, ",")
	if len(parts) < 6 {
		return gpuSample{}, false
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return gpuSample{}, false
	}
	util, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return gpuSample{}, false
	}
	mu, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return gpuSample{}, false
	}
	mt, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return gpuSample{}, false
	}
	temp, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		return gpuSample{}, false
	}
	var (
		pw     float64
		hasPow bool
	)
	pr := parts[5]
	if pr != "" && !strings.Contains(strings.ToUpper(pr), "N/A") {
		if f, err := strconv.ParseFloat(pr, 64); err == nil {
			pw = f
			hasPow = true
		}
	}
	return gpuSample{
		Index:       idx,
		UtilPct:     util,
		MemUsedMiB:  mu,
		MemTotalMiB: mt,
		TempC:       temp,
		PowerW:      pw,
		HasPower:    hasPow,
		PowerRaw:    pr,
	}, true
}

const (
	nameUtil      = "otel.ebpf.gpu.utilization"
	nameMemUsed   = "otel.ebpf.gpu.memory.used"
	nameMemTotal  = "otel.ebpf.gpu.memory.total"
	nameTemp      = "otel.ebpf.gpu.temperature"
	namePower     = "otel.ebpf.gpu.power.draw"
	unitPercent   = "%"
	unitMebibytes = "MiBy"
	unitCelsius   = "Cel"
	unitWatts     = "W"
)

type otlpSink struct {
	mu     sync.Mutex
	meter  metric.Meter
	util   metric.Int64Gauge
	memU   metric.Int64Gauge
	memT   metric.Int64Gauge
	temp   metric.Int64Gauge
	power  metric.Float64Gauge
	initEr error
}

func newOTLPSink(mp metric.MeterProvider) *otlpSink {
	return &otlpSink{
		meter: mp.Meter("go.opentelemetry.io/ebpf-profiler/gpu"),
	}
}

func (s *otlpSink) ensure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initEr != nil {
		return s.initEr
	}
	if s.util != nil {
		return nil
	}
	var err error
	s.util, err = s.meter.Int64Gauge(nameUtil,
		metric.WithDescription("GPU utilization percentage (0-100)"),
		metric.WithUnit(unitPercent))
	if err != nil {
		s.initEr = err
		return err
	}
	s.memU, err = s.meter.Int64Gauge(nameMemUsed,
		metric.WithDescription("GPU memory used"),
		metric.WithUnit(unitMebibytes))
	if err != nil {
		s.initEr = err
		return err
	}
	s.memT, err = s.meter.Int64Gauge(nameMemTotal,
		metric.WithDescription("GPU memory total"),
		metric.WithUnit(unitMebibytes))
	if err != nil {
		s.initEr = err
		return err
	}
	s.temp, err = s.meter.Int64Gauge(nameTemp,
		metric.WithDescription("GPU temperature"),
		metric.WithUnit(unitCelsius))
	if err != nil {
		s.initEr = err
		return err
	}
	s.power, err = s.meter.Float64Gauge(namePower,
		metric.WithDescription("GPU power draw"),
		metric.WithUnit(unitWatts))
	if err != nil {
		s.initEr = err
		return err
	}
	return nil
}

func (s *otlpSink) record(ctx context.Context, g gpuSample) {
	if err := s.ensure(); err != nil {
		log.Debugf("gpu metrics: otlp init: %v", err)
		return
	}
	dev := attribute.String("gpu.device.id", strconv.Itoa(g.Index))
	opts := metric.WithAttributes(dev)

	s.util.Record(ctx, g.UtilPct, opts)
	s.memU.Record(ctx, g.MemUsedMiB, opts)
	s.memT.Record(ctx, g.MemTotalMiB, opts)
	s.temp.Record(ctx, g.TempC, opts)
	if g.HasPower {
		s.power.Record(ctx, g.PowerW, opts)
	}
}
