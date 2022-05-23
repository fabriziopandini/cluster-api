/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package structuredmerge

import (
	"context"
)

// PatchHelper knows how to manage server side apply patches for Kubernetes objects owned by the topology controller.
// The patch intent is the modified object, or most specifically a subset of it the controller care about.
type PatchHelper interface {
	// HasChanges return true if the modified object is generating changes vs the original object.
	HasChanges() bool

	// HasSpecChanges return true if the modified object is generating spec changes vs the original object.
	HasSpecChanges() bool

	// Patch patches the given obj in the Kubernetes cluster.
	Patch(ctx context.Context) error
}
