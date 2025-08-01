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

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
)

const (
	// ClusterFinalizer allows DockerClusterReconciler to clean up resources associated with DockerCluster before
	// removing it from the apiserver.
	ClusterFinalizer = "dockercluster.infrastructure.cluster.x-k8s.io"
)

// DockerClusterSpec defines the desired state of DockerCluster.
type DockerClusterSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// ControlPlaneEndpoint represents the endpoint used to communicate with the control plane.
	// +optional
	ControlPlaneEndpoint APIEndpoint `json:"controlPlaneEndpoint"`

	// FailureDomains are usually not defined in the spec.
	// The docker provider is special since failure domains don't mean anything in a local docker environment.
	// Instead, the docker cluster controller will simply copy these into the Status and allow the Cluster API
	// controllers to do what they will with the defined failure domains.
	// +optional
	FailureDomains clusterv1beta1.FailureDomains `json:"failureDomains,omitempty"`

	// LoadBalancer allows defining configurations for the cluster load balancer.
	// +optional
	LoadBalancer DockerLoadBalancer `json:"loadBalancer,omitempty"`
}

// DockerLoadBalancer allows defining configurations for the cluster load balancer.
type DockerLoadBalancer struct {
	// ImageMeta allows customizing the image used for the cluster load balancer.
	ImageMeta `json:",inline"`

	// CustomHAProxyConfigTemplateRef allows you to replace the default HAProxy config file.
	// This field is a reference to a config map that contains the configuration template. The key of the config map should be equal to 'value'.
	// The content of the config map will be processed and will replace the default HAProxy config file. Please use it with caution, as there are
	// no checks to ensure the validity of the configuration. This template will support the following variables that will be passed by the controller:
	// $IPv6 (bool) indicates if the cluster is IPv6, $FrontendControlPlanePort (string) indicates the frontend control plane port,
	// $BackendControlPlanePort (string) indicates the backend control plane port, $BackendServers (map[string]string) indicates the backend server
	// where the key is the server name and the value is the address. This map is dynamic and is updated every time a new control plane
	// node is added or removed. The template will also support the JoinHostPort function to join the host and port of the backend server.
	// +optional
	CustomHAProxyConfigTemplateRef *corev1.LocalObjectReference `json:"customHAProxyConfigTemplateRef,omitempty"`
}

// ImageMeta allows customizing the image used for components that are not
// originated from the Kubernetes/Kubernetes release process.
type ImageMeta struct {
	// ImageRepository sets the container registry to pull the haproxy image from.
	// if not set, "kindest" will be used instead.
	// +optional
	ImageRepository string `json:"imageRepository,omitempty"`

	// ImageTag allows to specify a tag for the haproxy image.
	// if not set, "v20210715-a6da3463" will be used instead.
	// +optional
	ImageTag string `json:"imageTag,omitempty"`
}

// DockerClusterStatus defines the observed state of DockerCluster.
type DockerClusterStatus struct {
	// Ready denotes that the docker cluster (infrastructure) is ready.
	// +optional
	Ready bool `json:"ready"`

	// FailureDomains don't mean much in CAPD since it's all local, but we can see how the rest of cluster API
	// will use this if we populate it.
	// +optional
	FailureDomains clusterv1beta1.FailureDomains `json:"failureDomains,omitempty"`

	// Conditions defines current service state of the DockerCluster.
	// +optional
	Conditions clusterv1beta1.Conditions `json:"conditions,omitempty"`

	// v1beta2 groups all the fields that will be added or modified in DockerCluster's's status with the V1Beta2 version.
	// +optional
	V1Beta2 *DockerClusterV1Beta2Status `json:"v1beta2,omitempty"`
}

// DockerClusterV1Beta2Status groups all the fields that will be added or modified in DockerCluster with the V1Beta2 version.
// See https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20240916-improve-status-in-CAPI-resources.md for more context.
type DockerClusterV1Beta2Status struct {
	// conditions represents the observations of a DockerCluster's current state.
	// Known condition types are Ready, LoadBalancerAvailable, Deleting, Paused.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// APIEndpoint represents a reachable Kubernetes API endpoint.
type APIEndpoint struct {
	// Host is the hostname on which the API server is serving.
	Host string `json:"host"`

	// Port is the port on which the API server is serving.
	// Defaults to 6443 if not set.
	Port int `json:"port"`
}

// +kubebuilder:resource:path=dockerclusters,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:deprecatedversion
// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of DockerCluster"

// DockerCluster is the Schema for the dockerclusters API.
type DockerCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DockerClusterSpec   `json:"spec,omitempty"`
	Status DockerClusterStatus `json:"status,omitempty"`
}

// GetV1Beta1Conditions returns the set of conditions for this object.
func (c *DockerCluster) GetV1Beta1Conditions() clusterv1beta1.Conditions {
	return c.Status.Conditions
}

// SetV1Beta1Conditions sets the conditions on this object.
func (c *DockerCluster) SetV1Beta1Conditions(conditions clusterv1beta1.Conditions) {
	c.Status.Conditions = conditions
}

// GetConditions returns the set of conditions for this object.
func (c *DockerCluster) GetConditions() []metav1.Condition {
	if c.Status.V1Beta2 == nil {
		return nil
	}
	return c.Status.V1Beta2.Conditions
}

// SetConditions sets conditions for an API object.
func (c *DockerCluster) SetConditions(conditions []metav1.Condition) {
	if c.Status.V1Beta2 == nil {
		c.Status.V1Beta2 = &DockerClusterV1Beta2Status{}
	}
	c.Status.V1Beta2.Conditions = conditions
}

// +kubebuilder:object:root=true

// DockerClusterList contains a list of DockerCluster.
type DockerClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DockerCluster `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &DockerCluster{}, &DockerClusterList{})
}
