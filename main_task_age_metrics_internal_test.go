// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"
	"time"

	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/scanner"
)

func TestCreateVaultScannerPassesVaultNameAndMetricsInterval(t *testing.T) {
	app := &application{VaultName: "sentinel-vault"}

	var (
		gotVaultName       string
		gotMetricsInterval time.Duration
	)
	recording := func(
		_ gitrestclient.GitClient,
		_ string,
		_ time.Duration,
		_ <-chan struct{},
		_ metrics.Metrics,
		_ bool,
		vaultName string,
		metricsInterval time.Duration,
	) scanner.VaultScanner {
		gotVaultName = vaultName
		gotMetricsInterval = metricsInterval
		return nil
	}

	app.createVaultScanner(nil, make(chan struct{}), true, recording)

	if gotVaultName != "sentinel-vault" {
		t.Errorf("vault name = %q, want %q", gotVaultName, "sentinel-vault")
	}
	if gotMetricsInterval != metricsInterval {
		t.Errorf("metrics interval = %v, want %v", gotMetricsInterval, metricsInterval)
	}
	if metricsInterval != 5*time.Minute {
		t.Errorf("metricsInterval = %v, want 5m", metricsInterval)
	}
}
