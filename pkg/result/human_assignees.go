// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result

import "strings"

// HumanAssignees is the configured set of assignee values that name a human owner.
//
// It exists because ClearAssigneeIfHumanReview cannot tell a human from an agent by
// looking at a name: every assignee is a free-form string, and the controller holds
// no Config lister, so the identities an agent answers to (a Config's spec.assignee,
// e.g. pr-reviewer-agent) are not readable from here. The set is therefore supplied
// by configuration rather than inferred.
//
// An empty set retains nothing, which is the pre-existing behaviour: every
// human_review record is cleared. That default is deliberate rather than incidental
// — no person's name is hard-coded in this repository — and main logs the effective
// set at startup so an unconfigured deployment is visible in the log rather than
// inferred later from records that lost their owner.
//
// ⚠️ Membership is exact string equality. An assignee that is not in the set is
// treated as an agent and cleared, so a typo in the configuration silently removes
// a human's record from their queue — the failure this type exists to prevent,
// reachable through misconfiguration.
type HumanAssignees map[string]struct{}

// ParseHumanAssignees builds the set from a comma-separated list. Surrounding
// whitespace and empty entries are ignored, so an unset or blank value yields an
// empty set rather than a set containing "".
func ParseHumanAssignees(csv string) HumanAssignees {
	humans := HumanAssignees{}
	for _, name := range strings.Split(csv, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		humans[name] = struct{}{}
	}
	return humans
}

// Contains reports whether name is a configured human assignee. An empty name is
// never a member: an unowned record has no human owner to retain.
func (h HumanAssignees) Contains(name string) bool {
	if name == "" {
		return false
	}
	_, ok := h[name]
	return ok
}

// IsEmpty reports whether no human assignee is configured.
func (h HumanAssignees) IsEmpty() bool {
	return len(h) == 0
}
