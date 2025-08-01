/*
Copyright 2019 The Kubernetes Authors.

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

package v1alpha3

import (
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryconversion "k8s.io/apimachinery/pkg/conversion"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterv1alpha3 "sigs.k8s.io/cluster-api/internal/api/core/v1alpha3"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"
)

func (src *KubeadmConfig) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*bootstrapv1.KubeadmConfig)

	if err := Convert_v1alpha3_KubeadmConfig_To_v1beta2_KubeadmConfig(src, dst, nil); err != nil {
		return err
	}

	// Reset conditions from autogenerated conversions
	// NOTE: v1alpha3 conditions should not be automatically be converted into v1beta2 conditions.
	dst.Status.Conditions = nil

	// Move legacy conditions (v1alpha3), failureReason and failureMessage to the deprecated field.
	dst.Status.Deprecated = &bootstrapv1.KubeadmConfigDeprecatedStatus{}
	dst.Status.Deprecated.V1Beta1 = &bootstrapv1.KubeadmConfigV1Beta1DeprecatedStatus{}
	if src.Status.Conditions != nil {
		clusterv1alpha3.Convert_v1alpha3_Conditions_To_v1beta2_Deprecated_V1Beta1_Conditions(&src.Status.Conditions, &dst.Status.Deprecated.V1Beta1.Conditions)
	}
	dst.Status.Deprecated.V1Beta1.FailureReason = src.Status.FailureReason
	dst.Status.Deprecated.V1Beta1.FailureMessage = src.Status.FailureMessage

	// Manually restore data.
	restored := &bootstrapv1.KubeadmConfig{}
	ok, err := utilconversion.UnmarshalData(src, restored)
	if err != nil {
		return err
	}

	// Recover intent for bool values converted to *bool.
	initialization := bootstrapv1.KubeadmConfigInitializationStatus{}
	restoredBootstrapDataSecretCreated := restored.Status.Initialization.DataSecretCreated
	clusterv1.Convert_bool_To_Pointer_bool(src.Status.Ready, ok, restoredBootstrapDataSecretCreated, &initialization.DataSecretCreated)
	if !reflect.DeepEqual(initialization, bootstrapv1.KubeadmConfigInitializationStatus{}) {
		dst.Status.Initialization = initialization
	}
	if err := RestoreBoolIntentKubeadmConfigSpec(&src.Spec, &dst.Spec, ok, &restored.Spec); err != nil {
		return err
	}

	// Recover other values
	if ok {
		RestoreKubeadmConfigSpec(&dst.Spec, &restored.Spec)
		dst.Status.Conditions = restored.Status.Conditions
	}

	// Override restored data with timeouts values already existing in v1beta1 but in other structs.
	src.Spec.ConvertTo(&dst.Spec)
	return nil
}

func RestoreKubeadmConfigSpec(dst *bootstrapv1.KubeadmConfigSpec, restored *bootstrapv1.KubeadmConfigSpec) {
	dst.Files = restored.Files

	dst.Users = restored.Users
	if restored.Users != nil {
		for i := range restored.Users {
			if restored.Users[i].PasswdFrom != nil {
				dst.Users[i].PasswdFrom = restored.Users[i].PasswdFrom
			}
		}
	}

	dst.BootCommands = restored.BootCommands
	dst.Ignition = restored.Ignition

	if restored.ClusterConfiguration != nil {
		if dst.ClusterConfiguration == nil {
			dst.ClusterConfiguration = &bootstrapv1.ClusterConfiguration{}
		}
		dst.ClusterConfiguration.APIServer.ExtraEnvs = restored.ClusterConfiguration.APIServer.ExtraEnvs
		dst.ClusterConfiguration.ControllerManager.ExtraEnvs = restored.ClusterConfiguration.ControllerManager.ExtraEnvs
		dst.ClusterConfiguration.Scheduler.ExtraEnvs = restored.ClusterConfiguration.Scheduler.ExtraEnvs
		dst.ClusterConfiguration.CertificateValidityPeriodDays = restored.ClusterConfiguration.CertificateValidityPeriodDays
		dst.ClusterConfiguration.CACertificateValidityPeriodDays = restored.ClusterConfiguration.CACertificateValidityPeriodDays

		if restored.ClusterConfiguration.Etcd.Local != nil {
			if dst.ClusterConfiguration.Etcd.Local == nil {
				dst.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{}
			}
			dst.ClusterConfiguration.Etcd.Local.ExtraEnvs = restored.ClusterConfiguration.Etcd.Local.ExtraEnvs
		}
	}

	if restored.InitConfiguration != nil {
		if dst.InitConfiguration == nil {
			dst.InitConfiguration = &bootstrapv1.InitConfiguration{}
		}
		dst.InitConfiguration.Timeouts = restored.InitConfiguration.Timeouts
		dst.InitConfiguration.Patches = restored.InitConfiguration.Patches
		dst.InitConfiguration.SkipPhases = restored.InitConfiguration.SkipPhases

		// Important! whenever adding fields to NodeRegistration, same fields must be added to hub.NodeRegistration's custom serialization func
		// otherwise those field won't exist in restored.

		dst.InitConfiguration.NodeRegistration.IgnorePreflightErrors = restored.InitConfiguration.NodeRegistration.IgnorePreflightErrors
		dst.InitConfiguration.NodeRegistration.ImagePullPolicy = restored.InitConfiguration.NodeRegistration.ImagePullPolicy
		dst.InitConfiguration.NodeRegistration.ImagePullSerial = restored.InitConfiguration.NodeRegistration.ImagePullSerial
	}

	if restored.JoinConfiguration != nil {
		if dst.JoinConfiguration == nil {
			dst.JoinConfiguration = &bootstrapv1.JoinConfiguration{}
		}
		dst.JoinConfiguration.Timeouts = restored.JoinConfiguration.Timeouts
		dst.JoinConfiguration.Patches = restored.JoinConfiguration.Patches
		dst.JoinConfiguration.SkipPhases = restored.JoinConfiguration.SkipPhases

		if restored.JoinConfiguration.Discovery.File != nil && restored.JoinConfiguration.Discovery.File.KubeConfig != nil {
			if dst.JoinConfiguration.Discovery.File == nil {
				dst.JoinConfiguration.Discovery.File = &bootstrapv1.FileDiscovery{}
			}
			dst.JoinConfiguration.Discovery.File.KubeConfig = restored.JoinConfiguration.Discovery.File.KubeConfig
		}

		// Important! whenever adding fields to NodeRegistration, same fields must be added to hub.NodeRegistration's custom serialization func
		// otherwise those field won't exist in restored.

		dst.JoinConfiguration.NodeRegistration.IgnorePreflightErrors = restored.JoinConfiguration.NodeRegistration.IgnorePreflightErrors
		dst.JoinConfiguration.NodeRegistration.ImagePullPolicy = restored.JoinConfiguration.NodeRegistration.ImagePullPolicy
		dst.JoinConfiguration.NodeRegistration.ImagePullSerial = restored.JoinConfiguration.NodeRegistration.ImagePullSerial
	}
}

func RestoreBoolIntentKubeadmConfigSpec(src *KubeadmConfigSpec, dst *bootstrapv1.KubeadmConfigSpec, hasRestored bool, restored *bootstrapv1.KubeadmConfigSpec) error {
	if dst.JoinConfiguration != nil {
		if dst.JoinConfiguration.Discovery.BootstrapToken != nil {
			var restoredUnsafeSkipCAVerification *bool
			if restored.JoinConfiguration != nil && restored.JoinConfiguration.Discovery.BootstrapToken != nil {
				restoredUnsafeSkipCAVerification = restored.JoinConfiguration.Discovery.BootstrapToken.UnsafeSkipCAVerification
			}
			clusterv1.Convert_bool_To_Pointer_bool(src.JoinConfiguration.Discovery.BootstrapToken.UnsafeSkipCAVerification, hasRestored, restoredUnsafeSkipCAVerification, &dst.JoinConfiguration.Discovery.BootstrapToken.UnsafeSkipCAVerification)
		}
	}

	if dst.ClusterConfiguration != nil {
		for i, volume := range dst.ClusterConfiguration.APIServer.ExtraVolumes {
			var srcVolume *HostPathMount
			if src.ClusterConfiguration != nil {
				for _, v := range src.ClusterConfiguration.APIServer.ExtraVolumes {
					if v.HostPath == volume.HostPath {
						srcVolume = &v
						break
					}
				}
			}
			if srcVolume == nil {
				return fmt.Errorf("apiServer extraVolume with hostPath %q not found in source data", volume.HostPath)
			}
			var restoredVolumeReadOnly *bool
			if restored.ClusterConfiguration != nil {
				for _, v := range restored.ClusterConfiguration.APIServer.ExtraVolumes {
					if v.HostPath == volume.HostPath {
						restoredVolumeReadOnly = v.ReadOnly
						break
					}
				}
			}
			clusterv1.Convert_bool_To_Pointer_bool(srcVolume.ReadOnly, hasRestored, restoredVolumeReadOnly, &volume.ReadOnly)
			dst.ClusterConfiguration.APIServer.ExtraVolumes[i] = volume
		}
		for i, volume := range dst.ClusterConfiguration.ControllerManager.ExtraVolumes {
			var srcVolume *HostPathMount
			if src.ClusterConfiguration != nil {
				for _, v := range src.ClusterConfiguration.ControllerManager.ExtraVolumes {
					if v.HostPath == volume.HostPath {
						srcVolume = &v
						break
					}
				}
			}
			if srcVolume == nil {
				return fmt.Errorf("controllerManager extraVolume with hostPath %q not found in source data", volume.HostPath)
			}
			var restoredVolumeReadOnly *bool
			if restored.ClusterConfiguration != nil {
				for _, v := range restored.ClusterConfiguration.ControllerManager.ExtraVolumes {
					if v.HostPath == volume.HostPath {
						restoredVolumeReadOnly = v.ReadOnly
						break
					}
				}
			}
			clusterv1.Convert_bool_To_Pointer_bool(srcVolume.ReadOnly, hasRestored, restoredVolumeReadOnly, &volume.ReadOnly)
			dst.ClusterConfiguration.ControllerManager.ExtraVolumes[i] = volume
		}
		for i, volume := range dst.ClusterConfiguration.Scheduler.ExtraVolumes {
			var srcVolume *HostPathMount
			if src.ClusterConfiguration != nil {
				for _, v := range src.ClusterConfiguration.Scheduler.ExtraVolumes {
					if v.HostPath == volume.HostPath {
						srcVolume = &v
						break
					}
				}
			}
			if srcVolume == nil {
				return fmt.Errorf("scheduler extraVolume with hostPath %q not found in source data", volume.HostPath)
			}
			var restoredVolumeReadOnly *bool
			if restored.ClusterConfiguration != nil {
				for _, v := range restored.ClusterConfiguration.Scheduler.ExtraVolumes {
					if v.HostPath == volume.HostPath {
						restoredVolumeReadOnly = v.ReadOnly
						break
					}
				}
			}
			clusterv1.Convert_bool_To_Pointer_bool(srcVolume.ReadOnly, hasRestored, restoredVolumeReadOnly, &volume.ReadOnly)
			dst.ClusterConfiguration.Scheduler.ExtraVolumes[i] = volume
		}
	}
	return nil
}

func (src *KubeadmConfigSpec) ConvertTo(dst *bootstrapv1.KubeadmConfigSpec) {
	// Override with timeouts values already existing in v1beta1.
	var initControlPlaneComponentHealthCheckSeconds *int32
	if src.ClusterConfiguration != nil && src.ClusterConfiguration.APIServer.TimeoutForControlPlane != nil {
		if dst.InitConfiguration == nil {
			dst.InitConfiguration = &bootstrapv1.InitConfiguration{}
		}
		if dst.InitConfiguration.Timeouts == nil {
			dst.InitConfiguration.Timeouts = &bootstrapv1.Timeouts{}
		}
		dst.InitConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds = clusterv1.ConvertToSeconds(src.ClusterConfiguration.APIServer.TimeoutForControlPlane)
		initControlPlaneComponentHealthCheckSeconds = dst.InitConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds
	}
	if (src.JoinConfiguration != nil && src.JoinConfiguration.Discovery.Timeout != nil) || initControlPlaneComponentHealthCheckSeconds != nil {
		if dst.JoinConfiguration == nil {
			dst.JoinConfiguration = &bootstrapv1.JoinConfiguration{}
		}
		if dst.JoinConfiguration.Timeouts == nil {
			dst.JoinConfiguration.Timeouts = &bootstrapv1.Timeouts{}
		}
		dst.JoinConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds = initControlPlaneComponentHealthCheckSeconds
		if src.JoinConfiguration != nil && src.JoinConfiguration.Discovery.Timeout != nil {
			dst.JoinConfiguration.Timeouts.TLSBootstrapSeconds = clusterv1.ConvertToSeconds(src.JoinConfiguration.Discovery.Timeout)
		}
	}

	if reflect.DeepEqual(dst.ClusterConfiguration, &bootstrapv1.ClusterConfiguration{}) {
		dst.ClusterConfiguration = nil
	}
}

func (dst *KubeadmConfig) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*bootstrapv1.KubeadmConfig)

	if err := Convert_v1beta2_KubeadmConfig_To_v1alpha3_KubeadmConfig(src, dst, nil); err != nil {
		return err
	}

	// Reset conditions from autogenerated conversions
	// NOTE: v1beta2 conditions should not be automatically be converted into legacy conditions (v1alpha4).
	dst.Status.Conditions = nil

	// Retrieve legacy conditions (v1alpha4), failureReason and failureMessage from the deprecated field.
	if src.Status.Deprecated != nil {
		if src.Status.Deprecated.V1Beta1 != nil {
			if src.Status.Deprecated.V1Beta1.Conditions != nil {
				clusterv1alpha3.Convert_v1beta2_Deprecated_V1Beta1_Conditions_To_v1alpha3_Conditions(&src.Status.Deprecated.V1Beta1.Conditions, &dst.Status.Conditions)
			}
			dst.Status.FailureReason = src.Status.Deprecated.V1Beta1.FailureReason
			dst.Status.FailureMessage = src.Status.Deprecated.V1Beta1.FailureMessage
		}
	}

	// Move initialization to old fields
	dst.Status.Ready = ptr.Deref(src.Status.Initialization.DataSecretCreated, false)

	// Convert timeouts moved from one struct to another.
	dst.Spec.ConvertFrom(&src.Spec)

	dropEmptyStringsKubeadmConfigSpec(&dst.Spec)
	dropEmptyStringsKubeadmConfigStatus(&dst.Status)

	// Preserve Hub data on down-conversion except for metadata
	return utilconversion.MarshalData(src, dst)
}

func (dst *KubeadmConfigSpec) ConvertFrom(src *bootstrapv1.KubeadmConfigSpec) {
	// Convert timeouts moved from one struct to another.
	if src.InitConfiguration != nil && src.InitConfiguration.Timeouts != nil && src.InitConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds != nil {
		if dst.ClusterConfiguration == nil {
			dst.ClusterConfiguration = &ClusterConfiguration{}
		}
		dst.ClusterConfiguration.APIServer.TimeoutForControlPlane = clusterv1.ConvertFromSeconds(src.InitConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds)
	}
	if reflect.DeepEqual(dst.InitConfiguration, &InitConfiguration{}) {
		dst.InitConfiguration = nil
	}
	if src.JoinConfiguration != nil && src.JoinConfiguration.Timeouts != nil && src.JoinConfiguration.Timeouts.TLSBootstrapSeconds != nil {
		if dst.JoinConfiguration == nil {
			dst.JoinConfiguration = &JoinConfiguration{}
		}
		dst.JoinConfiguration.Discovery.Timeout = clusterv1.ConvertFromSeconds(src.JoinConfiguration.Timeouts.TLSBootstrapSeconds)
	}
	if reflect.DeepEqual(dst.JoinConfiguration, &JoinConfiguration{}) {
		dst.JoinConfiguration = nil
	}
}

func (src *KubeadmConfigTemplate) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*bootstrapv1.KubeadmConfigTemplate)

	if err := Convert_v1alpha3_KubeadmConfigTemplate_To_v1beta2_KubeadmConfigTemplate(src, dst, nil); err != nil {
		return err
	}

	// Manually restore data.
	restored := &bootstrapv1.KubeadmConfigTemplate{}
	ok, err := utilconversion.UnmarshalData(src, restored)
	if err != nil {
		return err
	}

	// Recover intent for bool values converted to *bool.
	if err := RestoreBoolIntentKubeadmConfigSpec(&src.Spec.Template.Spec, &dst.Spec.Template.Spec, ok, &restored.Spec.Template.Spec); err != nil {
		return err
	}

	// Recover other values
	if ok {
		RestoreKubeadmConfigSpec(&dst.Spec.Template.Spec, &restored.Spec.Template.Spec)
		dst.Spec.Template.ObjectMeta = restored.Spec.Template.ObjectMeta
	}

	// Override restored data with timeouts values already existing in v1beta1 but in other structs.
	src.Spec.Template.Spec.ConvertTo(&dst.Spec.Template.Spec)
	return nil
}

func (dst *KubeadmConfigTemplate) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*bootstrapv1.KubeadmConfigTemplate)

	if err := Convert_v1beta2_KubeadmConfigTemplate_To_v1alpha3_KubeadmConfigTemplate(src, dst, nil); err != nil {
		return err
	}

	// Convert timeouts moved from one struct to another.
	dst.Spec.Template.Spec.ConvertFrom(&src.Spec.Template.Spec)

	dropEmptyStringsKubeadmConfigSpec(&dst.Spec.Template.Spec)

	// Preserve Hub data on down-conversion except for metadata.
	return utilconversion.MarshalData(src, dst)
}

func Convert_v1alpha3_KubeadmConfigSpec_To_v1beta2_KubeadmConfigSpec(in *KubeadmConfigSpec, out *bootstrapv1.KubeadmConfigSpec, s apimachineryconversion.Scope) error {
	// NOTE: v1beta2 KubeadmConfigSpec does not have UseExperimentalRetryJoin anymore, so it's fine to just lose this field.
	return autoConvert_v1alpha3_KubeadmConfigSpec_To_v1beta2_KubeadmConfigSpec(in, out, s)
}

func Convert_v1alpha3_KubeadmConfigStatus_To_v1beta2_KubeadmConfigStatus(in *KubeadmConfigStatus, out *bootstrapv1.KubeadmConfigStatus, s apimachineryconversion.Scope) error {
	// KubeadmConfigStatus.BootstrapData has been removed in v1alpha4 because its content has been moved to the bootstrap data secret, value will be lost during conversion.
	return autoConvert_v1alpha3_KubeadmConfigStatus_To_v1beta2_KubeadmConfigStatus(in, out, s)
}

func Convert_v1alpha3_BootstrapToken_To_v1beta2_BootstrapToken(in *BootstrapToken, out *bootstrapv1.BootstrapToken, s apimachineryconversion.Scope) error {
	if err := autoConvert_v1alpha3_BootstrapToken_To_v1beta2_BootstrapToken(in, out, s); err != nil {
		return err
	}
	out.TTLSeconds = clusterv1.ConvertToSeconds(in.TTL)
	if in.Expires != nil && !reflect.DeepEqual(in.Expires, &metav1.Time{}) {
		out.Expires = *in.Expires
	}
	return nil
}

func Convert_v1beta2_KubeadmConfigSpec_To_v1alpha3_KubeadmConfigSpec(in *bootstrapv1.KubeadmConfigSpec, out *KubeadmConfigSpec, s apimachineryconversion.Scope) error {
	// KubeadmConfigSpec.Ignition does not exist in kubeadm v1alpha3 API.
	return autoConvert_v1beta2_KubeadmConfigSpec_To_v1alpha3_KubeadmConfigSpec(in, out, s)
}

func Convert_v1beta2_File_To_v1alpha3_File(in *bootstrapv1.File, out *File, s apimachineryconversion.Scope) error {
	// File.Append does not exist in kubeadm v1alpha3 API.
	return autoConvert_v1beta2_File_To_v1alpha3_File(in, out, s)
}

func Convert_v1beta2_User_To_v1alpha3_User(in *bootstrapv1.User, out *User, s apimachineryconversion.Scope) error {
	// User.PasswdFrom does not exist in kubeadm v1alpha3 API.
	return autoConvert_v1beta2_User_To_v1alpha3_User(in, out, s)
}

func Convert_v1beta2_KubeadmConfigTemplateResource_To_v1alpha3_KubeadmConfigTemplateResource(in *bootstrapv1.KubeadmConfigTemplateResource, out *KubeadmConfigTemplateResource, s apimachineryconversion.Scope) error {
	// KubeadmConfigTemplateResource.metadata does not exist in kubeadm v1alpha3.
	return autoConvert_v1beta2_KubeadmConfigTemplateResource_To_v1alpha3_KubeadmConfigTemplateResource(in, out, s)
}

func Convert_v1beta2_KubeadmConfigStatus_To_v1alpha3_KubeadmConfigStatus(in *bootstrapv1.KubeadmConfigStatus, out *KubeadmConfigStatus, s apimachineryconversion.Scope) error {
	// V1Beta2 was added in v1beta1.
	return autoConvert_v1beta2_KubeadmConfigStatus_To_v1alpha3_KubeadmConfigStatus(in, out, s)
}

func Convert_v1beta2_BootstrapToken_To_v1alpha3_BootstrapToken(in *bootstrapv1.BootstrapToken, out *BootstrapToken, s apimachineryconversion.Scope) error {
	if err := autoConvert_v1beta2_BootstrapToken_To_v1alpha3_BootstrapToken(in, out, s); err != nil {
		return err
	}
	out.TTL = clusterv1.ConvertFromSeconds(in.TTLSeconds)
	if !reflect.DeepEqual(in.Expires, metav1.Time{}) {
		out.Expires = ptr.To(in.Expires)
	}
	return nil
}

func Convert_v1alpha3_InitConfiguration_To_v1beta2_InitConfiguration(in *InitConfiguration, out *bootstrapv1.InitConfiguration, s apimachineryconversion.Scope) error {
	// Type neta has been dropped in v1beta2
	return autoConvert_v1alpha3_InitConfiguration_To_v1beta2_InitConfiguration(in, out, s)
}

// Implement local conversion func because conversion-gen is not aware of conversion func in other packages (see https://github.com/kubernetes/code-generator/issues/94)

func Convert_v1_Condition_To_v1alpha3_Condition(in *metav1.Condition, out *clusterv1alpha3.Condition, s apimachineryconversion.Scope) error {
	return clusterv1alpha3.Convert_v1_Condition_To_v1alpha3_Condition(in, out, s)
}

func Convert_v1alpha3_Condition_To_v1_Condition(in *clusterv1alpha3.Condition, out *metav1.Condition, s apimachineryconversion.Scope) error {
	return clusterv1alpha3.Convert_v1alpha3_Condition_To_v1_Condition(in, out, s)
}

func Convert_v1beta2_ClusterConfiguration_To_v1alpha3_ClusterConfiguration(in *bootstrapv1.ClusterConfiguration, out *ClusterConfiguration, s apimachineryconversion.Scope) error {
	return autoConvert_v1beta2_ClusterConfiguration_To_v1alpha3_ClusterConfiguration(in, out, s)
}

func dropEmptyStringsKubeadmConfigSpec(dst *KubeadmConfigSpec) {
	for i, u := range dst.Users {
		dropEmptyString(&u.Gecos)
		dropEmptyString(&u.Groups)
		dropEmptyString(&u.HomeDir)
		dropEmptyString(&u.Shell)
		dropEmptyString(&u.Passwd)
		dropEmptyString(&u.PrimaryGroup)
		dropEmptyString(&u.Sudo)
		dst.Users[i] = u
	}

	if dst.DiskSetup != nil {
		for i, p := range dst.DiskSetup.Partitions {
			dropEmptyString(&p.TableType)
			dst.DiskSetup.Partitions[i] = p
		}
		for i, f := range dst.DiskSetup.Filesystems {
			dropEmptyString(&f.Partition)
			dropEmptyString(&f.ReplaceFS)
			dst.DiskSetup.Filesystems[i] = f
		}
	}
}

func dropEmptyStringsKubeadmConfigStatus(dst *KubeadmConfigStatus) {
	dropEmptyString(&dst.DataSecretName)
}

func dropEmptyString(s **string) {
	if *s != nil && **s == "" {
		*s = nil
	}
}
