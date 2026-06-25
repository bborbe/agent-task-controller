// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitrestclient

import (
	"time"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// NewGitRestClientForTest creates a GitRestClient with a custom backoff for use in tests.
// Pass a function returning 0 or 1ms to make retry tests run fast.
func NewGitRestClientForTest(
	baseURL, gatewaySecret, gatewayInitiator string,
	backoff func(attempt int) time.Duration,
) GitRestClient {
	return newGitRestClientWithBackoff(
		baseURL,
		gatewaySecret,
		gatewayInitiator,
		backoff,
		metrics.New(),
	)
}

// ExponentialBackoff exposes the package-level exponentialBackoff function for testing.
func ExponentialBackoff(attempt int) time.Duration {
	return exponentialBackoff(attempt)
}
