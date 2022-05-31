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
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/internal/contract"
)

const topologyManagerName = "topology"

type serverSidePatchHelper struct {
	client         client.Client
	modified       *unstructured.Unstructured
	hasChanges     bool
	hasSpecChanges bool
}

// NewServerSidePatchHelper returns a new PatchHelper using server side apply.
func NewServerSidePatchHelper(original, modified client.Object, c client.Client, opts ...HelperOption) (PatchHelper, error) {
	helperOptions := &HelperOptions{}
	helperOptions = helperOptions.ApplyOptions(opts)
	helperOptions.allowedPaths = []contract.Path{
		// apiVersion, kind, name and namespace are required field for a server side apply intent.
		{"apiVersion"},
		{"kind"},
		{"metadata", "name"},
		{"metadata", "namespace"},
		// the topology controller controls/has an opinion for labels, annotation and spec only.
		{"metadata", "labels"},
		{"metadata", "annotations"},
		{"metadata", "ownerReferences"},
		{"spec"},
	}

	// If required, convert the original and modified objects to unstructured and filter out all the information
	// not relevant for the topology controller.

	var originalUnstructured *unstructured.Unstructured
	if original != nil {
		originalUnstructured = &unstructured.Unstructured{}
		switch original.(type) {
		case *unstructured.Unstructured:
			originalUnstructured = original.DeepCopyObject().(*unstructured.Unstructured)
		default:
			if err := c.Scheme().Convert(original, originalUnstructured, nil); err != nil {
				return nil, errors.Wrap(err, "failed to convert original object to Unstructured")
			}
		}
		filterObject(originalUnstructured, helperOptions)
	}

	modifiedUnstructured := &unstructured.Unstructured{}
	switch modified.(type) {
	case *unstructured.Unstructured:
		modifiedUnstructured = modified.DeepCopyObject().(*unstructured.Unstructured)
	default:
		if err := c.Scheme().Convert(modified, modifiedUnstructured, nil); err != nil {
			return nil, errors.Wrap(err, "failed to convert original object to Unstructured")
		}
	}
	filterObject(modifiedUnstructured, helperOptions)

	// Determine if the intent defined in the modified object is going to trigger
	// an actual change when running server side apply, and if this change might impact the object spec or not.
	var hasChanges, hasSpecChanges bool
	switch original {
	case nil:
		hasChanges, hasSpecChanges = true, true
	default:
		hasChanges, hasSpecChanges = dryRunPatch(&hasChangesContext{
			path:     contract.Path{},
			fieldsV1: getTopologyManagedFields(original),
			original: originalUnstructured.Object,
			modified: modifiedUnstructured.Object,
		})
	}

	return &serverSidePatchHelper{
		client:         c,
		modified:       modifiedUnstructured,
		hasChanges:     hasChanges,
		hasSpecChanges: hasSpecChanges,
	}, nil
}

// HasSpecChanges return true if the patch has changes to the spec field.
func (h *serverSidePatchHelper) HasSpecChanges() bool {
	return h.hasSpecChanges
}

// HasChanges return true if the patch has changes.
func (h *serverSidePatchHelper) HasChanges() bool {
	return h.hasChanges
}

// Patch will server side apply the current intent (the modified object.
func (h *serverSidePatchHelper) Patch(ctx context.Context) error {
	if !h.HasChanges() {
		return nil
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(5).Info("Patching object", "Patch", string("TODO")) // TODO: we can eventually log the intent

	options := []client.PatchOption{
		client.FieldOwner(topologyManagerName),
		// NOTE: we are using force ownership so in case of conflicts the topology controller
		// overwrite values and become sole manager.
		client.ForceOwnership,
	}
	return h.client.Patch(ctx, h.modified, client.Apply, options...)
}
