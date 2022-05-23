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
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/internal/test/builder"
	"sigs.k8s.io/cluster-api/util/patch"
)

// NOTE: This test ensures the ServerSideApply works as expected when the object is co-authored by other controllers.
func TestServerSideApply(t *testing.T) {
	g := NewWithT(t)

	// Write the config file to access the test env for debugging.
	// g.Expect(os.WriteFile("test.conf", kubeconfig.FromEnvTestConfig(env.Config, &clusterv1.Cluster{
	//	ObjectMeta: metav1.ObjectMeta{Name: "test"},
	// }), 0777)).To(Succeed())

	// Create a namespace for running the test
	ns, err := env.CreateNamespace(ctx, "ssa")
	g.Expect(err).ToNot(HaveOccurred())

	// Build the test object to work with.
	obj := builder.InfrastructureClusterTemplate(ns.Name, "obj1").WithSpecFields(map[string]interface{}{
		"spec.version":         "v1.2.3",
		"spec.ignoreThisField": "", // this field is then explicitly ignored by the patch helper
	}).Build()
	g.Expect(unstructured.SetNestedField(obj.Object, "", "status", "foo")).To(Succeed()) // this field is then ignored by the patch helper (not allowed path).

	t.Run("When creating an object using server side apply, it should track managed fields for the topology controller", func(t *testing.T) {
		g := NewWithT(t)

		// Create a patch helper with original == nil and modified == obj, ensure this is detected as operation that triggers changes.
		p0, err := NewServerSidePatchHelper(fakeScheme, nil, obj.DeepCopy(), env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeTrue())
		g.Expect(p0.HasSpecChanges()).To(BeTrue())

		// Create the object using server side apply
		g.Expect(p0.Patch(ctx)).To(Succeed())

		// Check the object and verify managed field are properly set.
		got := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(got), got)).To(Succeed())
		fieldV1 := getTopologyManagedFields(got)
		g.Expect(fieldV1).ToNot(BeEmpty())
		g.Expect(fieldV1).To(HaveKey("f:spec"))      // topology controller should express opinions on spec.
		g.Expect(fieldV1).ToNot(HaveKey("f:status")) // topology controller should not express opinions on status/not allowed paths.

		specFieldV1 := fieldV1["f:spec"].(map[string]interface{})
		g.Expect(specFieldV1).ToNot(BeEmpty())
		g.Expect(specFieldV1).To(HaveKey("f:version"))            // topology controller should express opinions on spec.version.
		g.Expect(specFieldV1).ToNot(HaveKey("f:ignoreThisField")) // topology controller should not express opinions on ignore paths.
	})

	t.Run("Server side apply patch helper detects no changes", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with no changes.
		modified := obj.DeepCopy()
		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeFalse())
		g.Expect(p0.HasSpecChanges()).To(BeFalse())
	})

	t.Run("Server side apply patch helper discard changes in not allowed fields, e.g. status", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with changes only in status.
		modified := obj.DeepCopy()
		g.Expect(unstructured.SetNestedField(modified.Object, "changed", "status", "foo")).To(Succeed())

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeFalse())
		g.Expect(p0.HasSpecChanges()).To(BeFalse())
	})

	t.Run("Server side apply patch helper detect changes", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with changes only in metadata.
		modified := obj.DeepCopy()
		g.Expect(unstructured.SetNestedField(modified.Object, "changed", "spec", "version")).To(Succeed())

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeTrue())
		g.Expect(p0.HasSpecChanges()).To(BeTrue())
	})

	t.Run("Server side apply patch helper detect changes impacting only metadata", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with changes only in metadata.
		modified := obj.DeepCopy()
		modified.SetLabels(map[string]string{"foo": "changed"})

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeTrue())
		g.Expect(p0.HasSpecChanges()).To(BeFalse())
	})

	t.Run("Server side apply patch helper discard changes in ignore paths", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with changes only in an ignoredField.
		modified := obj.DeepCopy()
		g.Expect(unstructured.SetNestedField(modified.Object, "changed", "spec", "ignoreThisField")).To(Succeed())

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeFalse())
		g.Expect(p0.HasSpecChanges()).To(BeFalse())
	})

	t.Run("Another controller applies changes", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		obj := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())

		// Create a patch helper like we do/recommend doing in the controllers and use it to apply some changes.
		p, err := patch.NewHelper(obj, env.Client)
		g.Expect(err).ToNot(HaveOccurred())

		g.Expect(unstructured.SetNestedField(obj.Object, "changed", "spec", "ignoreThisField")).To(Succeed()) // Controller sets a well known field ignored in the topology controller
		g.Expect(unstructured.SetNestedField(obj.Object, "changed", "spec", "infra-foo")).To(Succeed())       // Controller sets an infra specific field the topology controller is not aware of
		g.Expect(unstructured.SetNestedField(obj.Object, "changed", "status", "infra-foo")).To(Succeed())     // Controller sets something in status

		g.Expect(p.Patch(ctx, obj)).To(Succeed())
	})

	t.Run("Topology controller reconcile again with no changes on topology managed fields", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with no changes to what previously applied by th topology manager.
		modified := obj.DeepCopy()

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeFalse())
		g.Expect(p0.HasSpecChanges()).To(BeFalse())

		// Create the object using server side apply
		g.Expect(p0.Patch(ctx)).To(Succeed())

		// Check the object and verify fields set by the other controller are preserved.
		got := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(got), got)).To(Succeed())

		v1, _, _ := unstructured.NestedString(got.Object, "spec", "ignoreThisField")
		g.Expect(v1).To(Equal("changed"))
		v2, _, _ := unstructured.NestedString(got.Object, "spec", "infra-foo")
		g.Expect(v2).To(Equal("changed"))
		v3, _, _ := unstructured.NestedString(got.Object, "status", "infra-foo")
		g.Expect(v3).To(Equal("changed"))

		fieldV1 := getTopologyManagedFields(got)
		g.Expect(fieldV1).ToNot(BeEmpty())
		g.Expect(fieldV1).To(HaveKey("f:spec"))      // topology controller should express opinions on spec.
		g.Expect(fieldV1).ToNot(HaveKey("f:status")) // topology controller should not express opinions on status/not allowed paths.

		specFieldV1 := fieldV1["f:spec"].(map[string]interface{})
		g.Expect(specFieldV1).ToNot(BeEmpty())
		g.Expect(specFieldV1).To(HaveKey("f:version"))            // topology controller should express opinions on spec.version.
		g.Expect(specFieldV1).ToNot(HaveKey("f:ignoreThisField")) // topology controller should not express opinions on ignore paths.
		g.Expect(specFieldV1).ToNot(HaveKey("f:infra-foo"))       // topology controller should not express opinions on fields managed by other controllers.
	})

	t.Run("Topology controller reconcile again with some changes on topology managed fields", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with some changes to what previously applied by th topology manager.
		modified := obj.DeepCopy()
		g.Expect(unstructured.SetNestedField(modified.Object, "changed", "spec", "version")).To(Succeed())

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeTrue())
		g.Expect(p0.HasSpecChanges()).To(BeTrue())

		// Create the object using server side apply
		g.Expect(p0.Patch(ctx)).To(Succeed())

		// Check the object and verify the change is applied as well as the fields set by the other controller are still preserved.
		got := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(got), got)).To(Succeed())

		v0, _, _ := unstructured.NestedString(got.Object, "spec", "version")
		g.Expect(v0).To(Equal("changed"))
		v1, _, _ := unstructured.NestedString(got.Object, "spec", "ignoreThisField")
		g.Expect(v1).To(Equal("changed"))
		v2, _, _ := unstructured.NestedString(got.Object, "spec", "infra-foo")
		g.Expect(v2).To(Equal("changed"))
		v3, _, _ := unstructured.NestedString(got.Object, "status", "infra-foo")
		g.Expect(v3).To(Equal("changed"))
	})

	t.Run("Topology controller reconcile again with an opinion on a field managed by another controller (force ownership)", func(t *testing.T) {
		g := NewWithT(t)

		// Get the current object (assumes tests to be run in sequence).
		original := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(original), original)).To(Succeed())

		// Create a patch helper for a modified object with some changes to what previously applied by th topology manager.
		modified := obj.DeepCopy()
		g.Expect(unstructured.SetNestedField(modified.Object, "changed", "spec", "version")).To(Succeed())
		g.Expect(unstructured.SetNestedField(modified.Object, "changed-by-topology-controller", "spec", "infra-foo")).To(Succeed())

		p0, err := NewServerSidePatchHelper(fakeScheme, original, modified, env.GetClient(), IgnorePaths{{"spec", "ignoreThisField"}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(p0.HasChanges()).To(BeTrue())
		g.Expect(p0.HasSpecChanges()).To(BeTrue())

		// Create the object using server side apply
		g.Expect(p0.Patch(ctx)).To(Succeed())

		// Check the object and verify the change is applied as well as managed field updated accordingly.
		got := obj.DeepCopy()
		g.Expect(env.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(got), got)).To(Succeed())

		v2, _, _ := unstructured.NestedString(got.Object, "spec", "infra-foo")
		g.Expect(v2).To(Equal("changed-by-topology-controller"))

		fieldV1 := getTopologyManagedFields(got)
		g.Expect(fieldV1).ToNot(BeEmpty())
		g.Expect(fieldV1).To(HaveKey("f:spec")) // topology controller should express opinions on spec.

		specFieldV1 := fieldV1["f:spec"].(map[string]interface{})
		g.Expect(specFieldV1).ToNot(BeEmpty())
		g.Expect(specFieldV1).To(HaveKey("f:version"))            // topology controller should express opinions on spec.version.
		g.Expect(specFieldV1).ToNot(HaveKey("f:ignoreThisField")) // topology controller should not express opinions on ignore paths.
		g.Expect(specFieldV1).To(HaveKey("f:infra-foo"))          // topology controller now has an opinion on a field previously managed by other controllers (force ownership).
	})
}
