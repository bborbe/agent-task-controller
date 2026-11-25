// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

// Test-only re-exports for the external factory_test package.
var (
	NewCommandOffsetManager = newCommandOffsetManager
	NewCommandHandler       = newCommandHandler
)
