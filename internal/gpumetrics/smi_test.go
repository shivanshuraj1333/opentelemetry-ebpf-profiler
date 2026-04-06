// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpumetrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSMILine(t *testing.T) {
	s, ok := parseSMILine("0, 45, 1024, 8192, 55, 120.50")
	require.True(t, ok)
	require.Equal(t, 0, s.Index)
	require.Equal(t, int64(45), s.UtilPct)
	require.Equal(t, int64(1024), s.MemUsedMiB)
	require.Equal(t, int64(8192), s.MemTotalMiB)
	require.Equal(t, int64(55), s.TempC)
	require.True(t, s.HasPower)
	require.InDelta(t, 120.5, s.PowerW, 0.01)
}

func TestParseSMILineNAPower(t *testing.T) {
	s, ok := parseSMILine("1, 0, 0, 8192, 40, [N/A]")
	require.True(t, ok)
	require.Equal(t, 1, s.Index)
	require.False(t, s.HasPower)
}
