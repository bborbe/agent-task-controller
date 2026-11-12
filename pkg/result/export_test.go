// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result

import (
	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
)

// Test-only re-exports for the external result_test package.

// ApplyTriggerCapForTest runs the cap check on a minimally-populated writer. A nil
// humanAssignees map is the production default when no human set is configured, and
// the escalation section needs a clock to stamp its timestamp, so the zero value is
// not enough on its own.
func ApplyTriggerCapForTest(
	merged, existing lib.TaskFrontmatter,
	triggerCount int,
	body string,
) string {
	w := &resultWriter{currentDateTime: libtime.NewCurrentDateTime()}
	return w.applyTriggerCap(merged, existing, triggerCount, body)
}
