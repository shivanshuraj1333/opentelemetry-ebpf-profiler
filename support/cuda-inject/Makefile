# Copyright The OpenTelemetry Authors
# SPDX-License-Identifier: Apache-2.0
#
# Optional: build the CUDA injection library when CUDA + CUPTI are installed.
#   make -C support/cuda-inject CUDA=/usr/local/cuda

CUDA ?= /usr/local/cuda
OUT := libotel_cuda_inject.so

CFLAGS := -fPIC -O2 -Wall -Wextra -shared -fvisibility=hidden
# Optional: PC sampling activity (CUDA 12+ CUPTI; requires CUpti_ActivityPCSampling3 in headers).
# PC sampling must also be configured per context via cuptiActivityConfigurePCSampling in-app.
ifneq ($(PC_SAMPLE),)
CFLAGS += -DOTEL_CUPTI_PC_SAMPLING
endif
CPPFLAGS := -I$(CUDA)/include -I$(CUDA)/extras/CUPTI/include
LDFLAGS := -L$(CUDA)/extras/CUPTI/lib64 -Wl,-rpath,$(CUDA)/extras/CUPTI/lib64
LDLIBS := -lcupti -lcuda -lpthread -ldl

$(OUT): otel_cuda_inject.c
	$(CC) $(CFLAGS) $(CPPFLAGS) $(LDFLAGS) -o $@ $< $(LDLIBS)

.PHONY: clean
clean:
	rm -f $(OUT)
