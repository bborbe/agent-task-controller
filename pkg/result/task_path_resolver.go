// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result

import (
	"context"

	lib "github.com/bborbe/agent"
)

//counterfeiter:generate -o ../../mocks/task_path_resolver.go --fake-name TaskPathResolver . TaskPathResolver

// TaskPathResolver resolves a task_identifier to the vault-relative path of the
// task file that carries it.
//
// Three outcomes, and a caller must not conflate them:
//
//   - (path, true, nil) — the identifier is known. path is a path the resolver
//     itself observed; it is never built from the identifier.
//   - ("", false, nil) — the identifier is unknown, so the caller runs its own
//     lookup. A miss is NOT an error. The empty identifier is never indexed, so
//     it is unknown by construction.
//   - ("", false, err) — more than one file carries the identifier, and the error
//     names both paths. A resolver error is never a miss: treating it as one
//     re-derives the ambiguity and can silently resolve to one of the two files.
//
// Record fills the cache between rebuilds.
type TaskPathResolver interface {
	Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)
	// Record tells the resolver that relPath is the vault-relative path of the task
	// file carrying id, so a later Resolve(id) hits instead of missing. The caller
	// must have just established that fact — by writing that file or by a walk that
	// found exactly one file carrying id. It returns nothing: recording is a cache
	// fill, and a failure to record only costs a later walk. An empty id or empty
	// relPath is ignored. A resolver that already knows of two or more paths for id
	// keeps them, so a known duplicate is never hidden.
	Record(ctx context.Context, id lib.TaskIdentifier, relPath string)
	// RecordDuplicate adds relPath alongside the path(s) already known for id,
	// where Record would replace them. The caller must have just established that
	// relPath carries id AND that the path already known still carries it too — so
	// a different known path makes id a known duplicate that Resolve reports as an
	// error, and every later write refuses instead of landing on one of the two
	// files. Without a known path it records relPath alone, like Record. An empty id
	// or empty relPath is ignored, and a relPath already known for id is a no-op.
	RecordDuplicate(ctx context.Context, id lib.TaskIdentifier, relPath string)
}
