// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package gpuprof receives CUDA activity from a CUPTI-based injection library
// (see support/cuda-inject) over a Unix domain socket and forwards it into the
// standard TraceReporter pipeline.
//
// Event kinds (JSON lines, ver 1, optional schema 1):
//   - launch: CPU backtrace at cudaLaunch* entry, with correlation_id
//   - kernel: completed kernel (optional correlation_id); merged with pending launch stacks
//   - memcpy: async memcpy activity (optional correlation_id)
//
// This mirrors the zymtrace-style split: in-process GPU instrumentation plus
// correlation with CPU launch stacks, without sampling the GPU from eBPF alone.
package gpuprof // import "go.opentelemetry.io/ebpf-profiler/internal/gpuprof"
