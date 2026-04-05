// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package gpumetrics collects coarse GPU telemetry via nvidia-smi when available.
// It complements CUPTI-based profiling (profiles) with utilization-style signals
// similar to zymtrace's optional NVML metrics path, without CGO.
package gpumetrics // import "go.opentelemetry.io/ebpf-profiler/internal/gpumetrics"

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"go.opentelemetry.io/ebpf-profiler/internal/log"
)

// Start runs nvidia-smi queries on each tick until ctx is cancelled.
func Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snapshot(ctx)
		}
	}
}

func snapshot(ctx context.Context) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// gpu_index, util%, mem_used_mib, mem_total_mib, temp_c, power_w
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
		log.Infof("gpu metrics: %s", line)
	}
}
