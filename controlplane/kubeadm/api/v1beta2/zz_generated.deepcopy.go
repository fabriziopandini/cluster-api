//go:build !ignore_autogenerated

/*
Copyright The Kubernetes Authors.

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

// Code generated by controller-gen. DO NOT EDIT.

package v1beta2

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	apiv1beta2 "sigs.k8s.io/cluster-api/api/v1beta2"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlane) DeepCopyInto(out *KubeadmControlPlane) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlane.
func (in *KubeadmControlPlane) DeepCopy() *KubeadmControlPlane {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlane)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *KubeadmControlPlane) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneDeprecatedStatus) DeepCopyInto(out *KubeadmControlPlaneDeprecatedStatus) {
	*out = *in
	if in.V1Beta1 != nil {
		in, out := &in.V1Beta1, &out.V1Beta1
		*out = new(KubeadmControlPlaneV1Beta1DeprecatedStatus)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneDeprecatedStatus.
func (in *KubeadmControlPlaneDeprecatedStatus) DeepCopy() *KubeadmControlPlaneDeprecatedStatus {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneDeprecatedStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneInitializationStatus) DeepCopyInto(out *KubeadmControlPlaneInitializationStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneInitializationStatus.
func (in *KubeadmControlPlaneInitializationStatus) DeepCopy() *KubeadmControlPlaneInitializationStatus {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneInitializationStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneList) DeepCopyInto(out *KubeadmControlPlaneList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]KubeadmControlPlane, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneList.
func (in *KubeadmControlPlaneList) DeepCopy() *KubeadmControlPlaneList {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *KubeadmControlPlaneList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneMachineTemplate) DeepCopyInto(out *KubeadmControlPlaneMachineTemplate) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.InfrastructureRef = in.InfrastructureRef
	if in.ReadinessGates != nil {
		in, out := &in.ReadinessGates, &out.ReadinessGates
		*out = make([]apiv1beta2.MachineReadinessGate, len(*in))
		copy(*out, *in)
	}
	if in.NodeDrainTimeout != nil {
		in, out := &in.NodeDrainTimeout, &out.NodeDrainTimeout
		*out = new(v1.Duration)
		**out = **in
	}
	if in.NodeVolumeDetachTimeout != nil {
		in, out := &in.NodeVolumeDetachTimeout, &out.NodeVolumeDetachTimeout
		*out = new(v1.Duration)
		**out = **in
	}
	if in.NodeDeletionTimeout != nil {
		in, out := &in.NodeDeletionTimeout, &out.NodeDeletionTimeout
		*out = new(v1.Duration)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneMachineTemplate.
func (in *KubeadmControlPlaneMachineTemplate) DeepCopy() *KubeadmControlPlaneMachineTemplate {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneMachineTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneSpec) DeepCopyInto(out *KubeadmControlPlaneSpec) {
	*out = *in
	if in.Replicas != nil {
		in, out := &in.Replicas, &out.Replicas
		*out = new(int32)
		**out = **in
	}
	in.MachineTemplate.DeepCopyInto(&out.MachineTemplate)
	in.KubeadmConfigSpec.DeepCopyInto(&out.KubeadmConfigSpec)
	if in.RolloutBefore != nil {
		in, out := &in.RolloutBefore, &out.RolloutBefore
		*out = new(RolloutBefore)
		(*in).DeepCopyInto(*out)
	}
	if in.RolloutAfter != nil {
		in, out := &in.RolloutAfter, &out.RolloutAfter
		*out = (*in).DeepCopy()
	}
	if in.RolloutStrategy != nil {
		in, out := &in.RolloutStrategy, &out.RolloutStrategy
		*out = new(RolloutStrategy)
		(*in).DeepCopyInto(*out)
	}
	if in.RemediationStrategy != nil {
		in, out := &in.RemediationStrategy, &out.RemediationStrategy
		*out = new(RemediationStrategy)
		(*in).DeepCopyInto(*out)
	}
	if in.MachineNamingStrategy != nil {
		in, out := &in.MachineNamingStrategy, &out.MachineNamingStrategy
		*out = new(MachineNamingStrategy)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneSpec.
func (in *KubeadmControlPlaneSpec) DeepCopy() *KubeadmControlPlaneSpec {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneStatus) DeepCopyInto(out *KubeadmControlPlaneStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]v1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Initialization != nil {
		in, out := &in.Initialization, &out.Initialization
		*out = new(KubeadmControlPlaneInitializationStatus)
		**out = **in
	}
	if in.ReadyReplicas != nil {
		in, out := &in.ReadyReplicas, &out.ReadyReplicas
		*out = new(int32)
		**out = **in
	}
	if in.AvailableReplicas != nil {
		in, out := &in.AvailableReplicas, &out.AvailableReplicas
		*out = new(int32)
		**out = **in
	}
	if in.UpToDateReplicas != nil {
		in, out := &in.UpToDateReplicas, &out.UpToDateReplicas
		*out = new(int32)
		**out = **in
	}
	if in.Version != nil {
		in, out := &in.Version, &out.Version
		*out = new(string)
		**out = **in
	}
	if in.LastRemediation != nil {
		in, out := &in.LastRemediation, &out.LastRemediation
		*out = new(LastRemediationStatus)
		(*in).DeepCopyInto(*out)
	}
	if in.Deprecated != nil {
		in, out := &in.Deprecated, &out.Deprecated
		*out = new(KubeadmControlPlaneDeprecatedStatus)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneStatus.
func (in *KubeadmControlPlaneStatus) DeepCopy() *KubeadmControlPlaneStatus {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneTemplate) DeepCopyInto(out *KubeadmControlPlaneTemplate) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneTemplate.
func (in *KubeadmControlPlaneTemplate) DeepCopy() *KubeadmControlPlaneTemplate {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *KubeadmControlPlaneTemplate) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneTemplateList) DeepCopyInto(out *KubeadmControlPlaneTemplateList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]KubeadmControlPlaneTemplate, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneTemplateList.
func (in *KubeadmControlPlaneTemplateList) DeepCopy() *KubeadmControlPlaneTemplateList {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneTemplateList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *KubeadmControlPlaneTemplateList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneTemplateMachineTemplate) DeepCopyInto(out *KubeadmControlPlaneTemplateMachineTemplate) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	if in.NodeDrainTimeout != nil {
		in, out := &in.NodeDrainTimeout, &out.NodeDrainTimeout
		*out = new(v1.Duration)
		**out = **in
	}
	if in.NodeVolumeDetachTimeout != nil {
		in, out := &in.NodeVolumeDetachTimeout, &out.NodeVolumeDetachTimeout
		*out = new(v1.Duration)
		**out = **in
	}
	if in.NodeDeletionTimeout != nil {
		in, out := &in.NodeDeletionTimeout, &out.NodeDeletionTimeout
		*out = new(v1.Duration)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneTemplateMachineTemplate.
func (in *KubeadmControlPlaneTemplateMachineTemplate) DeepCopy() *KubeadmControlPlaneTemplateMachineTemplate {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneTemplateMachineTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneTemplateResource) DeepCopyInto(out *KubeadmControlPlaneTemplateResource) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneTemplateResource.
func (in *KubeadmControlPlaneTemplateResource) DeepCopy() *KubeadmControlPlaneTemplateResource {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneTemplateResource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneTemplateResourceSpec) DeepCopyInto(out *KubeadmControlPlaneTemplateResourceSpec) {
	*out = *in
	if in.MachineTemplate != nil {
		in, out := &in.MachineTemplate, &out.MachineTemplate
		*out = new(KubeadmControlPlaneTemplateMachineTemplate)
		(*in).DeepCopyInto(*out)
	}
	in.KubeadmConfigSpec.DeepCopyInto(&out.KubeadmConfigSpec)
	if in.RolloutBefore != nil {
		in, out := &in.RolloutBefore, &out.RolloutBefore
		*out = new(RolloutBefore)
		(*in).DeepCopyInto(*out)
	}
	if in.RolloutAfter != nil {
		in, out := &in.RolloutAfter, &out.RolloutAfter
		*out = (*in).DeepCopy()
	}
	if in.RolloutStrategy != nil {
		in, out := &in.RolloutStrategy, &out.RolloutStrategy
		*out = new(RolloutStrategy)
		(*in).DeepCopyInto(*out)
	}
	if in.RemediationStrategy != nil {
		in, out := &in.RemediationStrategy, &out.RemediationStrategy
		*out = new(RemediationStrategy)
		(*in).DeepCopyInto(*out)
	}
	if in.MachineNamingStrategy != nil {
		in, out := &in.MachineNamingStrategy, &out.MachineNamingStrategy
		*out = new(MachineNamingStrategy)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneTemplateResourceSpec.
func (in *KubeadmControlPlaneTemplateResourceSpec) DeepCopy() *KubeadmControlPlaneTemplateResourceSpec {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneTemplateResourceSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneTemplateSpec) DeepCopyInto(out *KubeadmControlPlaneTemplateSpec) {
	*out = *in
	in.Template.DeepCopyInto(&out.Template)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneTemplateSpec.
func (in *KubeadmControlPlaneTemplateSpec) DeepCopy() *KubeadmControlPlaneTemplateSpec {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneTemplateSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KubeadmControlPlaneV1Beta1DeprecatedStatus) DeepCopyInto(out *KubeadmControlPlaneV1Beta1DeprecatedStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make(apiv1beta2.Conditions, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.FailureMessage != nil {
		in, out := &in.FailureMessage, &out.FailureMessage
		*out = new(string)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KubeadmControlPlaneV1Beta1DeprecatedStatus.
func (in *KubeadmControlPlaneV1Beta1DeprecatedStatus) DeepCopy() *KubeadmControlPlaneV1Beta1DeprecatedStatus {
	if in == nil {
		return nil
	}
	out := new(KubeadmControlPlaneV1Beta1DeprecatedStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LastRemediationStatus) DeepCopyInto(out *LastRemediationStatus) {
	*out = *in
	in.Timestamp.DeepCopyInto(&out.Timestamp)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LastRemediationStatus.
func (in *LastRemediationStatus) DeepCopy() *LastRemediationStatus {
	if in == nil {
		return nil
	}
	out := new(LastRemediationStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MachineNamingStrategy) DeepCopyInto(out *MachineNamingStrategy) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MachineNamingStrategy.
func (in *MachineNamingStrategy) DeepCopy() *MachineNamingStrategy {
	if in == nil {
		return nil
	}
	out := new(MachineNamingStrategy)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RemediationStrategy) DeepCopyInto(out *RemediationStrategy) {
	*out = *in
	if in.MaxRetry != nil {
		in, out := &in.MaxRetry, &out.MaxRetry
		*out = new(int32)
		**out = **in
	}
	out.RetryPeriod = in.RetryPeriod
	if in.MinHealthyPeriod != nil {
		in, out := &in.MinHealthyPeriod, &out.MinHealthyPeriod
		*out = new(v1.Duration)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RemediationStrategy.
func (in *RemediationStrategy) DeepCopy() *RemediationStrategy {
	if in == nil {
		return nil
	}
	out := new(RemediationStrategy)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RollingUpdate) DeepCopyInto(out *RollingUpdate) {
	*out = *in
	if in.MaxSurge != nil {
		in, out := &in.MaxSurge, &out.MaxSurge
		*out = new(intstr.IntOrString)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RollingUpdate.
func (in *RollingUpdate) DeepCopy() *RollingUpdate {
	if in == nil {
		return nil
	}
	out := new(RollingUpdate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RolloutBefore) DeepCopyInto(out *RolloutBefore) {
	*out = *in
	if in.CertificatesExpiryDays != nil {
		in, out := &in.CertificatesExpiryDays, &out.CertificatesExpiryDays
		*out = new(int32)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RolloutBefore.
func (in *RolloutBefore) DeepCopy() *RolloutBefore {
	if in == nil {
		return nil
	}
	out := new(RolloutBefore)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RolloutStrategy) DeepCopyInto(out *RolloutStrategy) {
	*out = *in
	if in.RollingUpdate != nil {
		in, out := &in.RollingUpdate, &out.RollingUpdate
		*out = new(RollingUpdate)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RolloutStrategy.
func (in *RolloutStrategy) DeepCopy() *RolloutStrategy {
	if in == nil {
		return nil
	}
	out := new(RolloutStrategy)
	in.DeepCopyInto(out)
	return out
}
