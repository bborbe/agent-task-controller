// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import lib "github.com/bborbe/agent"

// ClusterFilter decides whether this controller publishes a task, based on the
// task's optional "cluster" frontmatter field.
type ClusterFilter struct {
	// Cluster is the cluster this controller runs in. Empty disables the
	// filter, so every task is published as before the field existed.
	Cluster string
	// PublishUntagged controls whether a task without a cluster field is
	// published. Only the cluster that owns untagged tasks sets it.
	PublishUntagged bool
}

// ShouldPublish reports whether the task belongs to this controller's cluster.
func (f ClusterFilter) ShouldPublish(task lib.Task) bool {
	if f.Cluster == "" {
		return true
	}
	taskCluster, _ := task.Frontmatter["cluster"].(string)
	if taskCluster == "" {
		return f.PublishUntagged
	}
	return taskCluster == f.Cluster
}
