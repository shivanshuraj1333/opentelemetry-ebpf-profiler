/*
 * Copyright The OpenTelemetry Authors
 * SPDX-License-Identifier: Apache-2.0
 *
 * CUPTI injection library: CUDA runtime launch callbacks (CPU backtrace +
 * correlation id) plus activity records (kernels, memcpy). Loaded via:
 *   export CUDA_INJECTION64_PATH=/path/to/libotel_cuda_inject.so
 *   export OTEL_CUDA_PROFILER_SOCKET=/tmp/opentelemetry-ebpf-gpu.sock
 *
 * Requires CUDA 11.8+ / CUPTI with CUpti_ActivityKernel8 and
 * cuptiActivityPushCorrelationId / Pop (toolkit extras/CUPTI).
 */
#define _GNU_SOURCE
#include <cupti.h>
#include <cupti_activity.h>
#include <execinfo.h>
#include <errno.h>
#include <pthread.h>
#include <stdarg.h>
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
#define MAX_JSON (64U * 1024U)

static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static int g_sock = -1;
static CUpti_SubscriberHandle g_subscriber = NULL;
static uint64_t g_corr_seq = 1;

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

static void write_line(const char *line, size_t len) {
  pthread_mutex_lock(&g_mu);
  for (int attempt = 0; attempt < 4; attempt++) {
    if (g_sock < 0) {
      ensure_socket_unlocked();
    }
    if (g_sock >= 0) {
      ssize_t w = write(g_sock, line, len);
      if (w >= 0 && (size_t)w == len) {
        pthread_mutex_unlock(&g_mu);
        return;
      }
      close(g_sock);
      g_sock = -1;
    }
    usleep(500 * (unsigned)(attempt + 1));
  }
  pthread_mutex_unlock(&g_mu);
}

static bool is_launch_api(const char *fn) {
  if (!fn) {
    return false;
  }
  /* Covers cudaLaunchKernel, cudaLaunchKernelExC, cudaGraphLaunch, ... */
  return strstr(fn, "cudaLaunch") != NULL || strstr(fn, "cuLaunch") != NULL;
}

static void emit_launch(uint64_t corr_id) {
  void *bt[64];
  int n = backtrace(bt, 64);
  char **syms = backtrace_symbols(bt, n);
  if (!syms || n <= 0) {
    return;
  }
  char *buf = (char *)malloc(MAX_JSON);
  if (!buf) {
    free(syms);
    return;
  }
  size_t pos = 0;
  int w = snprintf(buf + pos, MAX_JSON - pos,
                   "{\"ver\":1,\"schema\":1,\"kind\":\"launch\",\"pid\":%d,\"tid\":%d,\"correlation_id\":%llu,\"frames\":[",
                   (int)getpid(), (int)otel_tid(), (unsigned long long)corr_id);
  if (w < 0 || (size_t)w >= MAX_JSON - pos) {
    goto done;
  }
  pos += (size_t)w;
  for (int i = 0; i < n; i++) {
    char esc[1024];
    json_escape(syms[i], esc, sizeof(esc));
    const char *fmt = (i == 0) ? "\"%s\"" : ",\"%s\"";
    w = snprintf(buf + pos, MAX_JSON - pos, fmt, esc);
    if (w < 0 || (size_t)w >= MAX_JSON - pos) {
      goto done;
    }
    pos += (size_t)w;
  }
  w = snprintf(buf + pos, MAX_JSON - pos, "]}\n");
  if (w < 0 || (size_t)w >= MAX_JSON - pos) {
    goto done;
  }
  pos += (size_t)w;
  write_line(buf, pos);
done:
  free(buf);
  free(syms);
}

static void emit_kernel_line(int32_t dev, uint32_t stream, uint64_t corr, const char *name,
                             uint64_t start, uint64_t end) {
  char esc[2048];
  json_escape(name ? name : "", esc, sizeof(esc));
  char line[4096];
  int n = snprintf(line, sizeof(line),
                   "{\"ver\":1,\"schema\":1,\"kind\":\"kernel\",\"pid\":%d,\"tid\":%d,\"dev\":%d,\"stream_id\":%u,"
                   "\"correlation_id\":%llu,\"name\":\"%s\",\"start_ns\":%llu,\"end_ns\":%llu}\n",
                   (int)getpid(), (int)otel_tid(), (int)dev, (unsigned)stream,
                   (unsigned long long)corr, esc, (unsigned long long)start,
                   (unsigned long long)end);
  if (n > 0 && (size_t)n < sizeof(line)) {
    write_line(line, (size_t)n);
  }
}

static const char *memcpy_kind_str(uint8_t k) {
  switch (k) {
  case 1:
    return "HtoD";
  case 2:
    return "DtoH";
  case 3:
    return "DtoD";
  case 4:
    return "HtoA";
  case 5:
    return "AtoH";
  case 6:
    return "AtoA";
  case 7:
    return "HtoH";
  default:
    return "other";
  }
}

static void emit_memcpy_line(int32_t dev, uint64_t corr, uint64_t bytes, uint8_t kind,
                             uint64_t start_ns, uint64_t end_ns) {
  char line[768];
  int n = snprintf(line, sizeof(line),
                   "{\"ver\":1,\"schema\":1,\"kind\":\"memcpy\",\"pid\":%d,\"tid\":%d,\"dev\":%d,"
                   "\"correlation_id\":%llu,\"bytes\":%llu,\"copy_kind\":\"%s\","
                   "\"start_ns\":%llu,\"end_ns\":%llu}\n",
                   (int)getpid(), (int)otel_tid(), (int)dev, (unsigned long long)corr,
                   (unsigned long long)bytes, memcpy_kind_str(kind),
                   (unsigned long long)start_ns, (unsigned long long)end_ns);
  if (n > 0 && (size_t)n < sizeof(line)) {
    write_line(line, (size_t)n);
  }
}

#ifdef OTEL_CUPTI_PC_SAMPLING
static void emit_pc_sample_line(int32_t dev, int32_t tid, uint64_t corr, uint64_t pc_off,
                                uint32_t stall, uint32_t samples, uint64_t start_ns,
                                uint64_t end_ns) {
  char line[768];
  int n = snprintf(line, sizeof(line),
                   "{\"ver\":1,\"schema\":1,\"kind\":\"pcsample\",\"pid\":%d,\"tid\":%d,\"dev\":%d,"
                   "\"correlation_id\":%llu,\"pc_offset\":%llu,\"stall_reason\":%u,\"samples\":%u,"
                   "\"start_ns\":%llu,\"end_ns\":%llu}\n",
                   (int)getpid(), (int)tid, (int)dev, (unsigned long long)corr,
                   (unsigned long long)pc_off, stall, samples, (unsigned long long)start_ns,
                   (unsigned long long)end_ns);
  if (n > 0 && (size_t)n < sizeof(line)) {
    write_line(line, (size_t)n);
  }
}
#endif

void CUPTIAPI onRuntimeAPI(void *userdata, CUpti_CallbackDomain domain, CUpti_CallbackId cbid,
                           const CUpti_CallbackData *cb) {
  (void)userdata;
  (void)cbid;
  if (domain != CUPTI_CB_DOMAIN_RUNTIME_API || !cb || !cb->functionName) {
    return;
  }
  if (!is_launch_api(cb->functionName)) {
    return;
  }
  if (cb->callbackSite == CUPTI_API_ENTER) {
    uint64_t cid = __sync_add_and_fetch(&g_corr_seq, 1);
    /* Foreign correlation id ties launch callback to following activities. */
    (void)cuptiActivityPushCorrelationId((CUpti_CorrelationIdType)2, cid);
    emit_launch(cid);
  } else if (cb->callbackSite == CUPTI_API_EXIT) {
    uint64_t last = 0;
    (void)cuptiActivityPopCorrelationId((CUpti_CorrelationIdType)2, &last);
  }
}

static void handle_record(CUpti_Activity *rec) {
  switch (rec->kind) {
  case CUPTI_ACTIVITY_KIND_KERNEL:
  case CUPTI_ACTIVITY_KIND_CONCURRENT_KERNEL: {
    CUpti_ActivityKernel8 *k = (CUpti_ActivityKernel8 *)rec;
    const char *nm = k->name;
    emit_kernel_line((int32_t)k->deviceId, k->streamId, k->correlationId, nm ? nm : "", k->start,
                     k->end);
    break;
  }
  case CUPTI_ACTIVITY_KIND_MEMCPY: {
    /* Memcpy4 is widely available in recent CUPTI toolkits. */
    CUpti_ActivityMemcpy4 *m = (CUpti_ActivityMemcpy4 *)rec;
    emit_memcpy_line((int32_t)m->deviceId, m->correlationId, m->bytes, (uint8_t)m->copyKind,
                     m->start, m->end);
    break;
  }
#ifdef OTEL_CUPTI_PC_SAMPLING
  case CUPTI_ACTIVITY_KIND_PC_SAMPLING: {
    CUpti_ActivityPCSampling3 *p = (CUpti_ActivityPCSampling3 *)rec;
    emit_pc_sample_line(0, otel_tid(), (uint64_t)p->correlationId, p->pcOffset,
                        (uint32_t)p->stallReason, p->samples, 0, 0);
    break;
  }
#endif
  default:
    break;
  }
}

void CUPTIAPI bufferCompleted(CUcontext ctx, uint32_t streamId, uint8_t *buffer, size_t size,
                              size_t validSize) {
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
  CUptiResult r = cuptiSubscribe(&g_subscriber, (CUpti_CallbackFunc)onRuntimeAPI, NULL);
  if (r != CUPTI_SUCCESS) {
    return;
  }
  r = cuptiEnableDomain(1, g_subscriber, CUPTI_CB_DOMAIN_RUNTIME_API);
  if (r != CUPTI_SUCCESS) {
    return;
  }
  r = cuptiActivityRegisterBufferCallbacks(bufferRequested, bufferCompleted, NULL);
  if (r != CUPTI_SUCCESS) {
    return;
  }
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_RUNTIME_TRACE);
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_CONCURRENT_KERNEL);
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_KERNEL);
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_MEMCPY);
#ifdef OTEL_CUPTI_PC_SAMPLING
  cuptiActivityEnable(CUPTI_ACTIVITY_KIND_PC_SAMPLING);
#endif
}

__attribute__((constructor)) static void otel_ctor(void) { otel_cupti_start(); }
