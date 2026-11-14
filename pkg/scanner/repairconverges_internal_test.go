// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// repairConverges mirrors the convergence guard the identifier repair ran before
// it wrote: a candidate is accepted only when re-parsing it yields the identifier
// the repair intended to set.
//
// ⚠️ Production no longer calls this. The typed re-address answers with the task
// it stored, so a success IS the convergence proof and a refusal is an error the
// caller reports. The helper stays only so the specs that exercise the property
// keep their shape; they are to be re-stated against the API's response when the
// scanner suite is finished. Do not reintroduce it into a write path.

import (
	"context"

	"github.com/golang/glog"
	"gopkg.in/yaml.v3"
)

func repairConverges(ctx context.Context, candidate []byte, id string) bool {
	fmYAML, extractErr := extractFrontmatter(ctx, candidate)
	if extractErr != nil {
		glog.V(3).Infof("convergence guard: extract frontmatter failed: %v", extractErr)
		return false
	}
	dedupedYAML, _, dedupErr := DeduplicateFrontmatter(ctx, fmYAML)
	if dedupErr != nil {
		glog.V(3).Infof("convergence guard: deduplicate frontmatter failed: %v", dedupErr)
		return false
	}
	var fmMap map[string]interface{}
	if unmarshalErr := yaml.Unmarshal([]byte(dedupedYAML), &fmMap); unmarshalErr != nil {
		glog.V(3).Infof("convergence guard: unmarshal frontmatter failed: %v", unmarshalErr)
		return false
	}
	raw, present := fmMap["task_identifier"]
	if !present {
		return false
	}
	resolved, isString := raw.(string)
	if !isString {
		return false
	}
	return resolved == id
}
