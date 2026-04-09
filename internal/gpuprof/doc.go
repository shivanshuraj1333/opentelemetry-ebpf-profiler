// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package gpuprof receives CUDA activity from a CUPTI-based injection library
// (see support/cuda-inject) over a Unix domain socket and forwards it into the
// standard TraceReporter pipeline.
//
// Event kinds (JSON lines, ver 1, optional schema 1):
//   - launch: CPU backtrace at cudaLaunch* entry, with correlation_id
//   - kernel: completed kernel (optional correlation_id); merged with pending launch stacks
//   - memcpy: async memcpy activity (optional correlation_id; start_ns/end_ns when available)
//   - pcsample: PC sampling / stall metadata (optional; build inject with PC_SAMPLE=1 for CUPTI PC_SAMPLING)
//
// End-to-end (GPU timelines, zymtrace-style):
//  1. Build: make -C support/cuda-inject CUDA=/usr/local/cuda
//  2. Start the agent with -gpu-profiling and optionally -gpu-profiling-socket matching the workload.
//  3. Run the CUDA process with CUDA_INJECTION64_PATH pointing at libotel_cuda_inject.so and
//     OTEL_CUDA_PROFILER_SOCKET set to the same socket path as the agent (default /tmp/opentelemetry-ebpf-gpu.sock).
//
// This mirrors the zymtrace-style split: in-process GPU instrumentation plus
// correlation with CPU launch stacks, without sampling the GPU from eBPF alone.
package gpuprof // import "go.opentelemetry.io/ebpf-profiler/internal/gpuprof"
