# GPU profiling validation (OpenTelemetry eBPF Profiler)

This guide is for validating **CUDA GPU profiles** (CUPTI inject → Unix socket → agent → OTLP) on a **Linux GPU machine**, including from **Cursor** connected over SSH.

## Prerequisites

| Requirement | Notes |
|-------------|--------|
| **OS** | **Linux** (amd64 or arm64 per agent support). Not macOS for the agent. |
| **Privileges** | Enough for **eBPF** (often **root** or capabilities such as `CAP_SYS_ADMIN` / `CAP_BPF`). |
| **GPU** | **NVIDIA** GPU with a driver compatible with your CUDA toolkit. |
| **CUDA + CUPTI** | CUDA toolkit installed (e.g. `/usr/local/cuda`) with **extras/CUPTI** for building the inject library. |
| **Workload** | Any **CUDA** process (sample binary, PyTorch/JAX on CUDA, etc.). |

## 1. Clone and build the agent

On the GPU machine (or dev box with `GOOS=linux` cross-compile):

```bash
git clone <your-fork-or-repo-url> opentelemetry-ebpf-profiler
cd opentelemetry-ebpf-profiler
git checkout GPU-Profiling   # or your branch with GPU changes
go build -o ebpf-profiler .
```

Use the produced **`ebpf-profiler`** binary on **Linux** where eBPF is available.

## 2. Build the CUDA injection library

```bash
export CUDA=/usr/local/cuda   # adjust if different
make -C support/cuda-inject CUDA="$CUDA"
# Produces: support/cuda-inject/libotel_cuda_inject.so
```

Optional **PC sampling** activity (CUDA 12+ CUPTI headers; see Makefile):

```bash
make -C support/cuda-inject CUDA="$CUDA" PC_SAMPLE=1
```

## 3. OTLP endpoint

Point the agent at an OTLP **profiles** endpoint (same style as stock agent):

- `-collection-agent host:port`
- TLS: default on; use `-disable-tls` for plaintext gRPC if your backend expects it.

You need a collector or backend that accepts **OTLP profiles** (and optionally **metrics** if you enable GPU host metrics).

## 4. Run the eBPF profiler with GPU profiling enabled

Default Unix socket: **`/tmp/opentelemetry-ebpf-gpu.sock`** (override with `-gpu-profiling-socket`).

```bash
sudo ./ebpf-profiler \
  -collection-agent YOUR_OTLP_HOST:4317 \
  -gpu-profiling \
  -gpu-profiling-socket /tmp/opentelemetry-ebpf-gpu.sock \
  -v
```

Optional **GPU host metrics** (nvidia-smi → OTLP gauges) **in standalone mode**:

```bash
sudo ./ebpf-profiler \
  -collection-agent YOUR_OTLP_HOST:4317 \
  -gpu-profiling \
  -gpu-metrics \
  -v
```

Requires **`nvidia-smi`** on `PATH`. Without `-collection-agent`, `-gpu-metrics` only logs; with it, metrics export uses the same gRPC settings as profiles.

## 5. Run a CUDA workload with injection

In a **second terminal**, use the **same socket path** as the agent:

```bash
export CUDA_INJECTION64_PATH="$PWD/support/cuda-inject/libotel_cuda_inject.so"
export OTEL_CUDA_PROFILER_SOCKET=/tmp/opentelemetry-ebpf-gpu.sock

/path/to/your_cuda_app
```

Examples: CUDA **Samples** (`vectorAdd`), or a small PyTorch script that runs ops on **`cuda`**.

## 6. What to expect

| Check | Expected |
|-------|----------|
| Agent log | Message that GPU profiling is **listening** on the configured Unix socket. |
| OTLP / backend | **Profile** data with **GPU** origin: kernel names, durations, `gpu.device.id`, stacks that may include **host frames** from launch correlation. |
| Metrics (if `-gpu-metrics`) | Gauges derived from **nvidia-smi** (utilization, memory, temperature, power when present). |

## 7. Troubleshooting

| Symptom | Likely cause |
|---------|----------------|
| No GPU samples | **`OTEL_CUDA_PROFILER_SOCKET`** does not match **`-gpu-profiling-socket`**; or inject `.so` not loaded (`CUDA_INJECTION64_PATH` wrong). |
| App fails to start | Wrong path to **`libotel_cuda_inject.so`** or missing **libcupti** / CUDA libs on `LD_LIBRARY_PATH`. |
| Agent fails | Not on Linux, kernel too old for eBPF, or insufficient privileges. |
| Empty profiles | Backend not receiving **profiles** signal; wrong `-collection-agent` or TLS mismatch. |

## 8. Using Cursor on the GPU machine

1. **Remote SSH**: In Cursor, use **Remote - SSH** and open the folder where you cloned this repo.
2. **Read this file first**: `docs/GPU_PROFILING_VALIDATION.md`.
3. **Terminal**: Run the build steps, start the agent (sudo if needed), then run the CUDA workload with the `export` lines above.
4. **Branch**: Stay on **`GPU-Profiling`** (or merge your feature branch) so the GPU code paths and inject library match this documentation.

## Implementation pointers (for maintainers)

| Component | Location |
|-----------|----------|
| Inject library | `support/cuda-inject/otel_cuda_inject.c`, `Makefile` |
| Socket server + correlation | `internal/gpuprof/` |
| Controller flags | `cli_flags.go`; collector: `collector/config/config.go` (`gpu_profiling`, `gpu_metrics`, …) |
| Standalone OTLP GPU metrics | `internal/gpumetrics/otlp_provider.go`, wired from `main.go` when `-gpu-metrics` and `-collection-agent` are set |
| OTLP profile mapping | `reporter/internal/pdata/generate.go` (`TraceOriginGPU`, `gpu.device.id`) |

---

*This file is meant for manual validation and for Cursor agents continuing setup on a GPU host.*
