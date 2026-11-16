// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import "context"

// Test-only re-exports for the external command_test package.
var (
	BuildIncrementPatch              = buildIncrementPatch
	ParsePeriodTokenOrdinal          = parsePeriodTokenOrdinal
	RankSameSlugCandidatesDescending = rankSameSlugCandidatesDescending
	SplitTitleToken                  = splitTitleToken
	SupersedePriorUpdateGoTasks      = supersedePriorUpdateGoTasks
)

// VerifyDueForTest runs one verification pass, so a test can assert the readback
// outcome without waiting on the ticker.
func VerifyDueForTest(v WriteVerifier, ctx context.Context) {
	v.(*writeVerifier).verifyDue(ctx)
}
