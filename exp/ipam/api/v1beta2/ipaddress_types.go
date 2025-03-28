/*
Copyright 2025 The Kubernetes Authors.

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

package v1beta2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IPAddressSpec is the desired state of an IPAddress.
type IPAddressSpec struct {
	// claimRef is a reference to the claim this IPAddress was created for.
	// +required
	ClaimRef corev1.LocalObjectReference `json:"claimRef"`

	// poolRef is a reference to the pool that this IPAddress was created from.
	// +required
	PoolRef corev1.TypedLocalObjectReference `json:"poolRef"`

	// address is the IP address.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=39
	Address string `json:"address"`

	// prefix is the prefix of the address.
	// +required
	Prefix int `json:"prefix"`

	// gateway is the network gateway of the network the address is from.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=39
	Gateway string `json:"gateway,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=ipaddresses,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Address",type="string",JSONPath=".spec.address",description="Address"
// +kubebuilder:printcolumn:name="Pool Name",type="string",JSONPath=".spec.poolRef.name",description="Name of the pool the address is from"
// +kubebuilder:printcolumn:name="Pool Kind",type="string",JSONPath=".spec.poolRef.kind",description="Kind of the pool the address is from"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of IPAdress"

// IPAddress is the Schema for the ipaddress API.
type IPAddress struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the desired state of IPAddress.
	// +optional
	Spec IPAddressSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// IPAddressList is a list of IPAddress.
type IPAddressList struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard list's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#lists-and-simple-kinds
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	// items is the list of IPAddresses.
	Items []IPAddress `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &IPAddress{}, &IPAddressList{})
}
