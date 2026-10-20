// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync_test

import (
	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgsync "github.com/bborbe/agent-task-controller/pkg/sync"
)

var _ = DescribeTable(
	"ClusterFilter.ShouldPublish",
	func(filter pkgsync.ClusterFilter, taskCluster string, expected bool) {
		frontmatter := lib.TaskFrontmatter{"status": "in_progress"}
		if taskCluster != "" {
			frontmatter["cluster"] = taskCluster
		}
		task := lib.Task{TaskIdentifier: "tid", Frontmatter: frontmatter}
		Expect(filter.ShouldPublish(task)).To(Equal(expected))
	},
	Entry("filter disabled publishes a tagged task", pkgsync.ClusterFilter{}, "sun", true),
	Entry("filter disabled publishes an untagged task", pkgsync.ClusterFilter{}, "", true),
	Entry("matching cluster is published", pkgsync.ClusterFilter{Cluster: "sun"}, "sun", true),
	Entry(
		"foreign cluster is not published",
		pkgsync.ClusterFilter{Cluster: "sun"},
		"nukedev",
		false,
	),
	Entry(
		"untagged on the owning cluster is published",
		pkgsync.ClusterFilter{Cluster: "nukedev", PublishUntagged: true},
		"",
		true,
	),
	Entry(
		"untagged on another cluster is not published",
		pkgsync.ClusterFilter{Cluster: "sun"},
		"",
		false,
	),
)
