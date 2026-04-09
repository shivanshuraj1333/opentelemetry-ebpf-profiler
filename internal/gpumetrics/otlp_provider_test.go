// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpumetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewStandaloneMeterProviderEmptyEndpoint(t *testing.T) {
	_, _, err := NewStandaloneMeterProvider(context.Background(), OTLPStandaloneConfig{})
	require.Error(t, err)
}
