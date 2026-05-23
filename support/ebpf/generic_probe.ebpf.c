#include "bpfdefs.h"
#include "tracemgmt.h"
#include "types.h"

static EBPF_INLINE int probe__generic(struct pt_regs *ctx)
{
  u32 pid = get_pid_in_profiler_ns();
  u32 tid = (u32)(bpf_get_current_pid_tgid() & 0xFFFFFFFF);

  if (pid == 0 || tid == 0) {
    return 0;
  }

  u64 ts = bpf_ktime_get_ns();

  return collect_trace(ctx, TRACE_PROBE, pid, tid, ts, 0);
}

// kprobe__generic serves as entry point for kprobe based profiling.
SEC("kprobe/generic")
int kprobe__generic(struct pt_regs *ctx)
{
  return probe__generic(ctx);
}
