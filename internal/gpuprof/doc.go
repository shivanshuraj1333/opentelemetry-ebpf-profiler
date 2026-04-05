// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package gpuprof receives CUDA kernel activity from a CUPTI-based injection
// library (see support/cuda-inject) over a Unix domain socket and forwards it
// into the standard TraceReporter pipeline. This mirrors the zymtrace model:
// in-process GPU instrumentation correlated with the host agent via a side
// channel rather than eBPF on the GPU itself.
package gpuprof // import "go.opentelemetry.io/ebpf-profiler/internal/gpuprof"
