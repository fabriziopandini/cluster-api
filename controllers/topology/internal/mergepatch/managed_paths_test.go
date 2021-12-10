/*
Copyright 2021 The Kubernetes Authors.

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

package mergepatch

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/topology/internal/contract"
)

func Test_getManagedPaths(t *testing.T) {
	tests := []struct {
		name string
		obj  client.Object
		want []contract.Path
	}{
		{
			name: "Return no paths if the annotation does not exists",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{},
			},
			want: []contract.Path{},
		},
		{
			name: "Return paths from the annotation",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"annotations": map[string]interface{}{
							clusterv1.ClusterTopologyManagedFieldsAnnotation: "foo, bar.baz",
						},
					},
				},
			},
			want: []contract.Path{
				{"spec", "foo"},
				{"spec", "bar", "baz"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			got := getManagedPaths(tt.obj)
			g.Expect(got).To(Equal(tt.want))
		})
	}
}

func Test_storeManagedPaths(t *testing.T) {
	tests := []struct {
		name           string
		obj            client.Object
		wantAnnotation string
	}{
		{
			name: "Does not add annotation for typed objects",
			obj: &clusterv1.Cluster{
				Spec: clusterv1.ClusterSpec{
					ClusterNetwork: &clusterv1.ClusterNetwork{
						ServiceDomain: "foo.bar",
					},
				},
			},
			wantAnnotation: "",
		},
		{
			name: "Add empty annotation in case there are no changes to spec",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"foo": "bar",
						},
					},
				},
			},
			wantAnnotation: "",
		},
		{
			name: "Add empty annotation in case of changes to spec",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"foo": "foo",
						"bar": map[string]interface{}{
							"baz": "baz",
						},
					},
				},
			},
			wantAnnotation: "bar.baz, foo",
		},
		{
			name: "Add empty annotation handling properly deep nesting in spec",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"replicas": int64(4),
						"version":  "1.17.3",
						"kubeadmConfigSpec": map[string]interface{}{
							"clusterConfiguration": map[string]interface{}{
								"version": "v2.0.1",
							},
							"initConfiguration": map[string]interface{}{
								"bootstrapToken": []interface{}{"abcd", "defg"},
							},
							"joinConfiguration": nil,
						},
					},
				},
			},
			wantAnnotation: "kubeadmConfigSpec.clusterConfiguration.version, kubeadmConfigSpec.initConfiguration.bootstrapToken, kubeadmConfigSpec.joinConfiguration, replicas, version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			err := storeManagedPaths(tt.obj)
			g.Expect(err).ToNot(HaveOccurred())

			gotAnnotation := tt.obj.GetAnnotations()[clusterv1.ClusterTopologyManagedFieldsAnnotation]
			g.Expect(gotAnnotation).To(Equal(tt.wantAnnotation))
		})
	}
}
