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
type TaskPathResolver interface {
	Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)
}
