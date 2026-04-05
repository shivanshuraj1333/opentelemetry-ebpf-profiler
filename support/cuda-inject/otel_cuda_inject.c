/*
 * Copyright The OpenTelemetry Authors
 * SPDX-License-Identifier: Apache-2.0
 *
 * CUPTI activity subscriber for CUDA kernel completion events. Built as a
 * shared library and loaded into CUDA workloads via:
 *   export CUDA_INJECTION64_PATH=/path/to/libotel_cuda_inject.so
 *   export OTEL_CUDA_PROFILER_SOCKET=/tmp/opentelemetry-ebpf-gpu.sock  # optional
 *
 * The OpenTelemetry eBPF profiler agent listens on that Unix socket and
 * merges these events into OTLP Profiles (origin=gpu), similar in spirit to
 * zymtrace's injection + agent correlation model.
 */
#define _GNU_SOURCE
#include <cupti_activity.h>
#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#if defined(__linux__)
#include <sys/syscall.h>
static int32_t otel_tid(void) { return (int32_t)syscall(SYS_gettid); }
#else
static int32_t otel_tid(void) { return (int32_t)getpid(); }
#endif

#define BUF_SZ (4U * 1024U * 1024U)
#define DEFAULT_SOCK "/tmp/opentelemetry-ebpf-gpu.sock"

static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static int g_sock = -1;

static void json_escape(const char *in, char *out, size_t cap) {
  size_t j = 0;
  if (!in) {
    out[0] = 0;
    return;
  }
  for (size_t i = 0; in[i] && j + 1 < cap; i++) {
    unsigned char c = (unsigned char)in[i];
    if (c == '"' || c == '\\') {
      if (j + 2 >= cap) {
        break;
      }
      out[j++] = '\\';
      out[j++] = (char)c;
    } else if (c < 0x20U) {
      continue;
    } else {
      out[j++] = (char)c;
    }
  }
  if (j < cap) {
    out[j] = 0;
  } else {
    out[cap - 1] = 0;
  }
}

static void ensure_socket_unlocked(void) {
  if (g_sock >= 0) {
    return;
  }
  const char *path = getenv("OTEL_CUDA_PROFILER_SOCKET");
  if (!path || !path[0]) {
    path = DEFAULT_SOCK;
  }
  int fd = socket(AF_UNIX, SOCK_STREAM, 0);
  if (fd < 0) {
    return;
  }
  struct sockaddr_un addr;
  memset(&addr, 0, sizeof(addr));
  addr.sun_family = AF_UNIX;
  strncpy(addr.sun_path, path, sizeof(addr.sun_path) - 1);
  if (connect(fd, (struct sockaddr *)&addr, sizeof(addr)) != 0) {
    close(fd);
    return;
  }
  g_sock = fd;
}

static void emit_kernel(int32_t dev, const char *name, uint64_t start, uint64_t end) {
  char esc[2048];
  json_escape(name, esc, sizeof(esc));
  char line[4096];
  int n = snprintf(line, sizeof(line),
                   "{\"ver\":1,\"pid\":%d,\"tid\":%d,\"dev\":%d,\"name\":\"%s\","
                   "\"start_ns\":%llu,\"end_ns\":%llu}\n",
                   (int)getpid(), (int)otel_tid(), (int)dev, esc,
                   (unsigned long long)start, (unsigned long long)end);
  if (n <= 0 || (size_t)n >= sizeof(line)) {
    return;
  }
  pthread_mutex_lock(&g_mu);
  ensure_socket_unlocked();
  if (g_sock >= 0) {
    ssize_t w = write(g_sock, line, (size_t)n);
    (void)w;
    if (w < 0) {
      close(g_sock);
      g_sock = -1;
    }
  }
  pthread_mutex_unlock(&g_mu);
}

static void handle_record(CUpti_Activity *rec) {
  if (rec->kind != CUPTI_ACTIVITY_KIND_KERNEL &&
      rec->kind != CUPTI_ACTIVITY_KIND_CONCURRENT_KERNEL) {
    return;
  }
  /* Requires CUDA Toolkit with CUpti_ActivityKernel8 (CUDA 11.8+). */
  CUpti_ActivityKernel8 *k = (CUpti_ActivityKernel8 *)rec;
  const char *nm = k->name;
  emit_kernel((int32_t)k->deviceId, nm ? nm : "", k->start, k->end);
}

void CUPTIAPI bufferCompleted(CUcontext ctx, uint32_t streamId, uint8_t *buffer,
                              size_t size, size_t validSize) {
  (void)ctx;
  (void)streamId;
  (void)size;
  if (validSize == 0) {
    free(buffer);
    return;
  }
  CUpti_Activity *record = NULL;
  CUptiResult st;
  do {
    st = cuptiActivityGetNextRecord(buffer, validSize, &record);
    if (st == CUPTI_SUCCESS) {
      handle_record(record);
    }
  } while (st == CUPTI_SUCCESS);
  free(buffer);
}

void CUPTIAPI bufferRequested(uint8_t **buffer, size_t *size, size_t *maxNumRecords) {
  *buffer = (uint8_t *)malloc(BUF_SZ);
  *size = (*buffer) ? BUF_SZ : 0;
  *maxNumRecords = 0;
}

static void otel_cupti_start(void) {
  CUptiResult r = cuptiActivityRegisterBufferCallbacks(bufferRequested, bufferCompleted, NULL);
  if (r != CUPTI_SUCCESS) {
    return;
  }
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_CONCURRENT_KERNEL);
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_KERNEL);
}

__attribute__((constructor)) static void otel_ctor(void) { otel_cupti_start(); }
