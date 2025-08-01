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

package internal

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	bootstrapv1beta1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta1"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/bootstrap/kubeadm/defaulting"
	"sigs.k8s.io/cluster-api/internal/util/compare"
	"sigs.k8s.io/cluster-api/util/collections"
)

// matchesMachineSpec checks if a Machine matches any of a set of KubeadmConfigs and a set of infra machine configs.
// If it doesn't, it returns the reasons why.
// Kubernetes version, infrastructure template, and KubeadmConfig field need to be equivalent.
// Note: We don't need to compare the entire MachineSpec to determine if a Machine needs to be rolled out,
// because all the fields in the MachineSpec, except for version, the infrastructureRef and bootstrap.ConfigRef, are either:
// - mutated in-place (ex: NodeDrainTimeoutSeconds)
// - are not dictated by KCP (ex: ProviderID)
// - are not relevant for the rollout decision (ex: failureDomain).
func matchesMachineSpec(infraConfigs map[string]*unstructured.Unstructured, machineConfigs map[string]*bootstrapv1.KubeadmConfig, kcp *controlplanev1.KubeadmControlPlane, machine *clusterv1.Machine) (bool, []string, []string, error) {
	logMessages := []string{}
	conditionMessages := []string{}

	if !collections.MatchesKubernetesVersion(kcp.Spec.Version)(machine) {
		machineVersion := ""
		if machine != nil && machine.Spec.Version != "" {
			machineVersion = machine.Spec.Version
		}
		logMessages = append(logMessages, fmt.Sprintf("Machine version %q is not equal to KCP version %q", machineVersion, kcp.Spec.Version))
		// Note: the code computing the message for KCP's RolloutOut condition is making assumptions on the format/content of this message.
		conditionMessages = append(conditionMessages, fmt.Sprintf("Version %s, %s required", machineVersion, kcp.Spec.Version))
	}

	reason, matches, err := matchesKubeadmBootstrapConfig(machineConfigs, kcp, machine)
	if err != nil {
		return false, nil, nil, errors.Wrapf(err, "failed to match Machine spec")
	}
	if !matches {
		logMessages = append(logMessages, reason)
		conditionMessages = append(conditionMessages, "KubeadmConfig is not up-to-date")
	}

	if reason, matches := matchesTemplateClonedFrom(infraConfigs, kcp, machine); !matches {
		logMessages = append(logMessages, reason)
		conditionMessages = append(conditionMessages, fmt.Sprintf("%s is not up-to-date", machine.Spec.InfrastructureRef.Kind))
	}

	if len(logMessages) > 0 || len(conditionMessages) > 0 {
		return false, logMessages, conditionMessages, nil
	}

	return true, nil, nil, nil
}

// UpToDate checks if a Machine is up to date with the control plane's configuration.
// If not, messages explaining why are provided with different level of detail for logs and conditions.
func UpToDate(machine *clusterv1.Machine, kcp *controlplanev1.KubeadmControlPlane, reconciliationTime *metav1.Time, infraConfigs map[string]*unstructured.Unstructured, machineConfigs map[string]*bootstrapv1.KubeadmConfig) (bool, []string, []string, error) {
	logMessages := []string{}
	conditionMessages := []string{}

	// Machines whose certificates are about to expire.
	if collections.ShouldRolloutBefore(reconciliationTime, kcp.Spec.Rollout.Before)(machine) {
		logMessages = append(logMessages, "certificates will expire soon, rolloutBefore expired")
		conditionMessages = append(conditionMessages, "Certificates will expire soon")
	}

	// Machines that are scheduled for rollout (KCP.Spec.RolloutAfter set,
	// the RolloutAfter deadline is expired, and the machine was created before the deadline).
	if collections.ShouldRolloutAfter(reconciliationTime, kcp.Spec.Rollout.After)(machine) {
		logMessages = append(logMessages, "rolloutAfter expired")
		conditionMessages = append(conditionMessages, "KubeadmControlPlane spec.rolloutAfter expired")
	}

	// Machines that do not match with KCP config.
	matches, specLogMessages, specConditionMessages, err := matchesMachineSpec(infraConfigs, machineConfigs, kcp, machine)
	if err != nil {
		return false, nil, nil, errors.Wrapf(err, "failed to determine if Machine %s is up-to-date", machine.Name)
	}
	if !matches {
		logMessages = append(logMessages, specLogMessages...)
		conditionMessages = append(conditionMessages, specConditionMessages...)
	}

	if len(logMessages) > 0 || len(conditionMessages) > 0 {
		return false, logMessages, conditionMessages, nil
	}

	return true, nil, nil, nil
}

// matchesTemplateClonedFrom checks if a Machine has a corresponding infrastructure machine that
// matches a given KCP infra template and if it doesn't match returns the reason why.
// Note: Differences to the labels and annotations on the infrastructure machine are not considered for matching
// criteria, because changes to labels and annotations are propagated in-place to the infrastructure machines.
// TODO: This function will be renamed in a follow-up PR to something better. (ex: MatchesInfraMachine).
func matchesTemplateClonedFrom(infraConfigs map[string]*unstructured.Unstructured, kcp *controlplanev1.KubeadmControlPlane, machine *clusterv1.Machine) (string, bool) {
	if machine == nil {
		return "Machine cannot be compared with KCP.spec.machineTemplate.spec.infrastructureRef: Machine is nil", false
	}
	infraObj, found := infraConfigs[machine.Name]
	if !found {
		// Return true here because failing to get infrastructure machine should not be considered as unmatching.
		return "", true
	}

	clonedFromName, ok1 := infraObj.GetAnnotations()[clusterv1.TemplateClonedFromNameAnnotation]
	clonedFromGroupKind, ok2 := infraObj.GetAnnotations()[clusterv1.TemplateClonedFromGroupKindAnnotation]
	if !ok1 || !ok2 {
		// All kcp cloned infra machines should have this annotation.
		// Missing the annotation may be due to older version machines or adopted machines.
		// Should not be considered as mismatch.
		return "", true
	}

	// Check if the machine's infrastructure reference has been created from the current KCP infrastructure template.
	if clonedFromName != kcp.Spec.MachineTemplate.Spec.InfrastructureRef.Name ||
		clonedFromGroupKind != kcp.Spec.MachineTemplate.Spec.InfrastructureRef.GroupKind().String() {
		return fmt.Sprintf("Infrastructure template on KCP rotated from %s %s to %s %s",
			clonedFromGroupKind, clonedFromName,
			kcp.Spec.MachineTemplate.Spec.InfrastructureRef.GroupKind().String(), kcp.Spec.MachineTemplate.Spec.InfrastructureRef.Name), false
	}

	return "", true
}

// matchesKubeadmBootstrapConfig checks if machine's KubeadmConfigSpec is equivalent with KCP's KubeadmConfigSpec.
// Note: Differences to the labels and annotations on the KubeadmConfig are not considered for matching
// criteria, because changes to labels and annotations are propagated in-place to KubeadmConfig.
func matchesKubeadmBootstrapConfig(machineConfigs map[string]*bootstrapv1.KubeadmConfig, kcp *controlplanev1.KubeadmControlPlane, machine *clusterv1.Machine) (string, bool, error) {
	if machine == nil {
		return "Machine KubeadmConfig cannot be compared: Machine is nil", false, nil
	}

	// Check if KCP and machine ClusterConfiguration matches, if not return
	match, diff, err := matchClusterConfiguration(kcp, machine)
	if err != nil {
		return "", false, errors.Wrapf(err, "failed to match KubeadmConfig")
	}
	if !match {
		return fmt.Sprintf("Machine KubeadmConfig ClusterConfiguration is outdated: diff: %s", diff), false, nil
	}

	bootstrapRef := machine.Spec.Bootstrap.ConfigRef
	if bootstrapRef == nil {
		// Missing bootstrap reference should not be considered as unmatching.
		// This is a safety precaution to avoid selecting machines that are broken, which in the future should be remediated separately.
		return "", true, nil
	}

	machineConfig, found := machineConfigs[machine.Name]
	if !found {
		// Return true here because failing to get KubeadmConfig should not be considered as unmatching.
		// This is a safety precaution to avoid rolling out machines if the client or the api-server is misbehaving.
		return "", true, nil
	}

	// Check if KCP and machine InitConfiguration or JoinConfiguration matches
	// NOTE: only one between init configuration and join configuration is set on a machine, depending
	// on the fact that the machine was the initial control plane node or a joining control plane node.
	match, diff, err = matchInitOrJoinConfiguration(machineConfig, kcp)
	if err != nil {
		return "", false, errors.Wrapf(err, "failed to match KubeadmConfig")
	}
	if !match {
		return fmt.Sprintf("Machine KubeadmConfig InitConfiguration or JoinConfiguration are outdated: diff: %s", diff), false, nil
	}

	return "", true, nil
}

// matchClusterConfiguration verifies if KCP and machine ClusterConfiguration matches.
// NOTE: Machines that have KubeadmClusterConfigurationAnnotation will have to match with KCP ClusterConfiguration.
// If the annotation is not present (machine is either old or adopted), we won't roll out on any possible changes
// made in KCP's ClusterConfiguration given that we don't have enough information to make a decision.
// Users should use KCP.Spec.RolloutAfter field to force a rollout in this case.
func matchClusterConfiguration(kcp *controlplanev1.KubeadmControlPlane, machine *clusterv1.Machine) (bool, string, error) {
	if _, ok := machine.GetAnnotations()[controlplanev1.KubeadmClusterConfigurationAnnotation]; !ok {
		// We don't have enough information to make a decision; don't' trigger a roll out.
		return true, "", nil
	}

	machineClusterConfig, err := ClusterConfigurationFromMachine(machine)
	if err != nil {
		// ClusterConfiguration annotation is not correct, only solution is to rollout.
		// The call to json.Unmarshal has to take a pointer to the pointer struct defined above,
		// otherwise we won't be able to handle a nil ClusterConfiguration (that is serialized into "null").
		// See https://github.com/kubernetes-sigs/cluster-api/issues/3353.
		return false, "", nil //nolint:nilerr // Intentionally not returning the error here
	}

	// If any of the compared values are nil, treat them the same as an empty ClusterConfiguration.
	if machineClusterConfig == nil {
		machineClusterConfig = &bootstrapv1.ClusterConfiguration{}
	}

	kcpLocalClusterConfiguration := kcp.Spec.KubeadmConfigSpec.ClusterConfiguration.DeepCopy()
	if kcpLocalClusterConfiguration == nil {
		kcpLocalClusterConfiguration = &bootstrapv1.ClusterConfiguration{}
	}

	// Skip checking DNS fields because we can update the configuration of the working cluster in place.
	machineClusterConfig.DNS = kcpLocalClusterConfiguration.DNS

	// Drop differences that do not lead to changes to Machines, but that might exist due
	// to changes in how we serialize objects or how webhooks work.
	dropOmittableFields(&bootstrapv1.KubeadmConfigSpec{ClusterConfiguration: kcpLocalClusterConfiguration})
	dropOmittableFields(&bootstrapv1.KubeadmConfigSpec{ClusterConfiguration: machineClusterConfig})

	// Compare and return.
	match, diff, err := compare.Diff(machineClusterConfig, kcpLocalClusterConfiguration)
	if err != nil {
		return false, "", errors.Wrapf(err, "failed to match ClusterConfiguration")
	}
	return match, diff, nil
}

type versionedClusterConfiguration struct {
	MarshalVersion string `json:"marshalVersion,omitempty"`
	*bootstrapv1.ClusterConfiguration
}

// ClusterConfigurationAnnotationFromMachineIsOutdated return true if the annotation is outdated.
// Note: this is intentionally implemented with a string check to prevent an additional json.Unmarshal operation.
func ClusterConfigurationAnnotationFromMachineIsOutdated(annotation string) bool {
	return !strings.Contains(annotation, fmt.Sprintf("\"marshalVersion\":%q", bootstrapv1.GroupVersion.Version))
}

// ClusterConfigurationToMachineAnnotationValue returns an annotation valued to add on machines for
// tracking the ClusterConfiguration value at the time the machine was created.
func ClusterConfigurationToMachineAnnotationValue(clusterConfiguration *bootstrapv1.ClusterConfiguration) (string, error) {
	machineClusterConfig := &versionedClusterConfiguration{
		MarshalVersion:       bootstrapv1.GroupVersion.Version,
		ClusterConfiguration: clusterConfiguration,
	}

	annotationBytes, err := json.Marshal(machineClusterConfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal cluster configuration")
	}
	return string(annotationBytes), nil
}

// ClusterConfigurationFromMachine returns the ClusterConfiguration value at the time the machine was created.
// Note: In case the annotation was created with an older version of the KCP API, the value is converted to the current API version.
func ClusterConfigurationFromMachine(machine *clusterv1.Machine) (*bootstrapv1.ClusterConfiguration, error) {
	machineClusterConfigStr, ok := machine.GetAnnotations()[controlplanev1.KubeadmClusterConfigurationAnnotation]
	if !ok {
		return nil, nil
	}

	if ClusterConfigurationAnnotationFromMachineIsOutdated(machineClusterConfigStr) {
		// Note: Only conversion from v1beta1 is supported as of today.
		machineClusterConfigV1Beta1 := &bootstrapv1beta1.ClusterConfiguration{}
		if err := json.Unmarshal([]byte(machineClusterConfigStr), &machineClusterConfigV1Beta1); err != nil {
			return nil, errors.Wrapf(err, "failed to unmarshal ClusterConfiguration from Machine %s", klog.KObj(machine))
		}

		kubeadmConfigV1Beta1 := &bootstrapv1beta1.KubeadmConfig{
			Spec: bootstrapv1beta1.KubeadmConfigSpec{
				ClusterConfiguration: machineClusterConfigV1Beta1,
			},
		}
		kubeadmConfig := &bootstrapv1.KubeadmConfig{}
		err := kubeadmConfigV1Beta1.ConvertTo(kubeadmConfig)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to convert ClusterConfiguration from Machine %s", klog.KObj(machine))
		}

		machineClusterConfig := kubeadmConfig.Spec.ClusterConfiguration
		return machineClusterConfig, nil
	}

	machineClusterConfig := &versionedClusterConfiguration{}
	if err := json.Unmarshal([]byte(machineClusterConfigStr), &machineClusterConfig); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal ClusterConfiguration from Machine %s", klog.KObj(machine))
	}
	return machineClusterConfig.ClusterConfiguration, nil
}

// matchInitOrJoinConfiguration verifies if KCP and machine InitConfiguration or JoinConfiguration matches.
// NOTE: By extension this method takes care of detecting changes in other fields of the KubeadmConfig configuration (e.g. Files, Mounts etc.)
func matchInitOrJoinConfiguration(machineConfig *bootstrapv1.KubeadmConfig, kcp *controlplanev1.KubeadmControlPlane) (bool, string, error) {
	if machineConfig == nil {
		// Return true here because failing to get KubeadmConfig should not be considered as unmatching.
		// This is a safety precaution to avoid rolling out machines if the client or the api-server is misbehaving.
		return true, "", nil
	}
	machineConfig = machineConfig.DeepCopy()

	// takes the KubeadmConfigSpec from KCP and applies the transformations required
	// to allow a comparison with the KubeadmConfig referenced from the machine.
	kcpConfig := getAdjustedKcpConfig(kcp, machineConfig)

	// Default both KubeadmConfigSpecs before comparison.
	// *Note* This assumes that newly added default values never
	// introduce a semantic difference to the unset value.
	// But that is something that is ensured by our API guarantees.
	defaulting.ApplyPreviousKubeadmConfigDefaults(kcpConfig)
	defaulting.ApplyPreviousKubeadmConfigDefaults(&machineConfig.Spec)

	// cleanups all the fields that are not relevant for the comparison.
	cleanupConfigFields(kcpConfig, machineConfig)

	match, diff, err := compare.Diff(&machineConfig.Spec, kcpConfig)
	if err != nil {
		return false, "", errors.Wrapf(err, "failed to match InitConfiguration or JoinConfiguration")
	}
	return match, diff, nil
}

// getAdjustedKcpConfig takes the KubeadmConfigSpec from KCP and applies the transformations required
// to allow a comparison with the KubeadmConfig referenced from the machine.
// NOTE: The KCP controller applies a set of transformations when creating a KubeadmConfig referenced from the machine,
// mostly depending on the fact that the machine was the initial control plane node or a joining control plane node.
// In this function we don't have such information, so we are making the KubeadmConfigSpec similar to the KubeadmConfig.
func getAdjustedKcpConfig(kcp *controlplanev1.KubeadmControlPlane, machineConfig *bootstrapv1.KubeadmConfig) *bootstrapv1.KubeadmConfigSpec {
	kcpConfig := kcp.Spec.KubeadmConfigSpec.DeepCopy()

	// Init configuration is usually be set under normal circumstances, but with some test providers (e.g. CAPD in memory)
	// it is left empty; In this case, we are initializing it so the comparison code can be simplified.
	if kcpConfig.InitConfiguration == nil {
		kcpConfig.InitConfiguration = &bootstrapv1.InitConfiguration{}
	}

	// Machine's join configuration is nil when it is the first machine in the control plane.
	if machineConfig.Spec.JoinConfiguration == nil {
		kcpConfig.JoinConfiguration = nil
	}

	// Machine's init configuration is nil when the control plane is already initialized.
	if machineConfig.Spec.InitConfiguration == nil {
		kcpConfig.InitConfiguration = nil
	}

	return kcpConfig
}

// cleanupConfigFields cleanups all the fields that are not relevant for the comparison.
func cleanupConfigFields(kcpConfig *bootstrapv1.KubeadmConfigSpec, machineConfig *bootstrapv1.KubeadmConfig) {
	// KCP ClusterConfiguration will only be compared with a machine's ClusterConfiguration annotation, so
	// we are cleaning up from the reflect.DeepEqual comparison.
	kcpConfig.ClusterConfiguration = nil
	machineConfig.Spec.ClusterConfiguration = nil

	// If KCP JoinConfiguration is not present, set machine JoinConfiguration to nil (nothing can trigger rollout here).
	// NOTE: this is required because CABPK applies an empty joinConfiguration in case no one is provided.
	if kcpConfig.JoinConfiguration == nil {
		machineConfig.Spec.JoinConfiguration = nil
	}

	// Cleanup JoinConfiguration.Discovery from kcpConfig and machineConfig, because those info are relevant only for
	// the join process and not for comparing the configuration of the machine.
	emptyDiscovery := bootstrapv1.Discovery{}
	if kcpConfig.JoinConfiguration != nil {
		kcpConfig.JoinConfiguration.Discovery = emptyDiscovery
	}
	if machineConfig.Spec.JoinConfiguration != nil {
		machineConfig.Spec.JoinConfiguration.Discovery = emptyDiscovery
	}

	// If KCP JoinConfiguration.ControlPlane is not present, set machine join configuration to nil (nothing can trigger rollout here).
	// NOTE: this is required because CABPK applies an empty joinConfiguration.ControlPlane in case no one is provided.
	if kcpConfig.JoinConfiguration != nil && kcpConfig.JoinConfiguration.ControlPlane == nil &&
		machineConfig.Spec.JoinConfiguration != nil {
		machineConfig.Spec.JoinConfiguration.ControlPlane = nil
	}

	// If KCP's join NodeRegistration is empty, set machine's node registration to empty as no changes should trigger rollout.
	emptyNodeRegistration := bootstrapv1.NodeRegistrationOptions{}
	if kcpConfig.JoinConfiguration != nil && reflect.DeepEqual(kcpConfig.JoinConfiguration.NodeRegistration, emptyNodeRegistration) &&
		machineConfig.Spec.JoinConfiguration != nil {
		machineConfig.Spec.JoinConfiguration.NodeRegistration = emptyNodeRegistration
	}

	// Drop differences that do not lead to changes to Machines, but that might exist due
	// to changes in how we serialize objects or how webhooks work.
	dropOmittableFields(kcpConfig)
	dropOmittableFields(&machineConfig.Spec)
}

// dropOmittableFields makes the comparison tolerant to omittable fields being set in the go struct. It applies to:
// - empty array vs nil
// - empty map vs nil
// - empty struct vs nil (when struct is pointer and there are only omittable fields in the struct).
// Note: for the part of the KubeadmConfigSpec that is rendered using go templates, consideration might be a little bit different.
func dropOmittableFields(spec *bootstrapv1.KubeadmConfigSpec) {
	// When rendered to kubeadm config files there is no diff between nil and empty array or map.
	if spec.ClusterConfiguration != nil {
		if spec.ClusterConfiguration.Etcd.Local != nil {
			if len(spec.ClusterConfiguration.Etcd.Local.ExtraArgs) == 0 {
				spec.ClusterConfiguration.Etcd.Local.ExtraArgs = nil
			}
			if spec.ClusterConfiguration.Etcd.Local.ExtraEnvs != nil &&
				len(*spec.ClusterConfiguration.Etcd.Local.ExtraEnvs) == 0 {
				spec.ClusterConfiguration.Etcd.Local.ExtraEnvs = nil
			}
			if len(spec.ClusterConfiguration.Etcd.Local.ServerCertSANs) == 0 {
				spec.ClusterConfiguration.Etcd.Local.ServerCertSANs = nil
			}
			if len(spec.ClusterConfiguration.Etcd.Local.PeerCertSANs) == 0 {
				spec.ClusterConfiguration.Etcd.Local.PeerCertSANs = nil
			}
		}
		// NOTE: we are not dropping spec.ClusterConfiguration.Etcd.ExternalEtcd.Endpoints because this field
		// doesn't have omitempty, so [] array is different from nil when serialized.
		// But this field is also required and has MinItems=1, so it will
		// never actually be nil or an empty array so that difference also won't trigger any rollouts.
		if len(spec.ClusterConfiguration.APIServer.ExtraArgs) == 0 {
			spec.ClusterConfiguration.APIServer.ExtraArgs = nil
		}
		if spec.ClusterConfiguration.APIServer.ExtraEnvs != nil &&
			len(*spec.ClusterConfiguration.APIServer.ExtraEnvs) == 0 {
			spec.ClusterConfiguration.APIServer.ExtraEnvs = nil
		}
		if len(spec.ClusterConfiguration.APIServer.ExtraVolumes) == 0 {
			spec.ClusterConfiguration.APIServer.ExtraVolumes = nil
		}
		if len(spec.ClusterConfiguration.APIServer.CertSANs) == 0 {
			spec.ClusterConfiguration.APIServer.CertSANs = nil
		}
		if len(spec.ClusterConfiguration.ControllerManager.ExtraArgs) == 0 {
			spec.ClusterConfiguration.ControllerManager.ExtraArgs = nil
		}
		if spec.ClusterConfiguration.ControllerManager.ExtraEnvs != nil &&
			len(*spec.ClusterConfiguration.ControllerManager.ExtraEnvs) == 0 {
			spec.ClusterConfiguration.ControllerManager.ExtraEnvs = nil
		}
		if len(spec.ClusterConfiguration.ControllerManager.ExtraVolumes) == 0 {
			spec.ClusterConfiguration.ControllerManager.ExtraVolumes = nil
		}
		if len(spec.ClusterConfiguration.Scheduler.ExtraArgs) == 0 {
			spec.ClusterConfiguration.Scheduler.ExtraArgs = nil
		}
		if spec.ClusterConfiguration.Scheduler.ExtraEnvs != nil &&
			len(*spec.ClusterConfiguration.Scheduler.ExtraEnvs) == 0 {
			spec.ClusterConfiguration.Scheduler.ExtraEnvs = nil
		}
		if len(spec.ClusterConfiguration.Scheduler.ExtraVolumes) == 0 {
			spec.ClusterConfiguration.Scheduler.ExtraVolumes = nil
		}
		if len(spec.ClusterConfiguration.FeatureGates) == 0 {
			spec.ClusterConfiguration.FeatureGates = nil
		}
	}
	if spec.InitConfiguration != nil {
		if len(spec.InitConfiguration.BootstrapTokens) == 0 {
			spec.InitConfiguration.BootstrapTokens = nil
		}
		for i, token := range spec.InitConfiguration.BootstrapTokens {
			if len(token.Usages) == 0 {
				token.Usages = nil
			}
			if len(token.Groups) == 0 {
				token.Groups = nil
			}
			spec.InitConfiguration.BootstrapTokens[i] = token
		}
		if len(spec.InitConfiguration.NodeRegistration.KubeletExtraArgs) == 0 {
			spec.InitConfiguration.NodeRegistration.KubeletExtraArgs = nil
		}
		if len(spec.InitConfiguration.NodeRegistration.IgnorePreflightErrors) == 0 {
			spec.InitConfiguration.NodeRegistration.IgnorePreflightErrors = nil
		}
		if len(spec.InitConfiguration.SkipPhases) == 0 {
			spec.InitConfiguration.SkipPhases = nil
		}
		// NOTE: we are not dropping spec.InitConfiguration.Taints because for this field there
		// is a difference between not set (use kubeadm defaults) and empty (do not apply any taint).
	}
	if spec.JoinConfiguration != nil {
		if spec.JoinConfiguration.Discovery.BootstrapToken != nil {
			if len(spec.JoinConfiguration.Discovery.BootstrapToken.CACertHashes) == 0 {
				spec.JoinConfiguration.Discovery.BootstrapToken.CACertHashes = nil
			}
		}
		if spec.JoinConfiguration.Discovery.File != nil && spec.JoinConfiguration.Discovery.File.KubeConfig != nil {
			if spec.JoinConfiguration.Discovery.File.KubeConfig.Cluster != nil {
				if len(spec.JoinConfiguration.Discovery.File.KubeConfig.Cluster.CertificateAuthorityData) == 0 {
					spec.JoinConfiguration.Discovery.File.KubeConfig.Cluster.CertificateAuthorityData = nil
				}
				if reflect.DeepEqual(spec.JoinConfiguration.Discovery.File.KubeConfig.Cluster, &bootstrapv1.KubeConfigCluster{}) {
					spec.JoinConfiguration.Discovery.File.KubeConfig.Cluster = nil
				}
			}
			if spec.JoinConfiguration.Discovery.File.KubeConfig.User.AuthProvider != nil {
				if len(spec.JoinConfiguration.Discovery.File.KubeConfig.User.AuthProvider.Config) == 0 {
					spec.JoinConfiguration.Discovery.File.KubeConfig.User.AuthProvider.Config = nil
				}
				if reflect.DeepEqual(spec.JoinConfiguration.Discovery.File.KubeConfig.User.AuthProvider, &bootstrapv1.KubeConfigAuthProvider{}) {
					spec.JoinConfiguration.Discovery.File.KubeConfig.User.AuthProvider = nil
				}
			}
			if spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec != nil {
				if len(spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec.Args) == 0 {
					spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec.Args = nil
				}
				if len(spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec.Env) == 0 {
					spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec.Env = nil
				}
				if reflect.DeepEqual(spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec, &bootstrapv1.KubeConfigAuthExec{}) {
					spec.JoinConfiguration.Discovery.File.KubeConfig.User.Exec = nil
				}
			}
			if reflect.DeepEqual(spec.JoinConfiguration.Discovery.File.KubeConfig, &bootstrapv1.FileDiscoveryKubeConfig{}) {
				spec.JoinConfiguration.Discovery.File.KubeConfig = nil
			}
		}
		if len(spec.JoinConfiguration.NodeRegistration.KubeletExtraArgs) == 0 {
			spec.JoinConfiguration.NodeRegistration.KubeletExtraArgs = nil
		}
		if len(spec.JoinConfiguration.NodeRegistration.IgnorePreflightErrors) == 0 {
			spec.JoinConfiguration.NodeRegistration.IgnorePreflightErrors = nil
		}
		// NOTE: we are not dropping spec.JoinConfiguration.Taints because for this field there
		// is a difference between not set (use kubeadm defaults) and empty (do not apply any taint).
		if len(spec.JoinConfiguration.SkipPhases) == 0 {
			spec.JoinConfiguration.SkipPhases = nil
		}
	}

	// When rendered to cloud init, there is no diff between nil and empty files.
	if len(spec.Files) == 0 {
		spec.Files = nil
	}

	// When rendered to cloud init, there is no diff between nil and empty diskSetup.filesystems.
	// When rendered to cloud init, there is no diff between nil and empty diskSetup.filesystems[].extraOpts.
	// When rendered to cloud init, there is no diff between nil and empty diskSetup.partitions.
	if spec.DiskSetup != nil {
		if len(spec.DiskSetup.Filesystems) == 0 {
			spec.DiskSetup.Filesystems = nil
		}
		for i, fs := range spec.DiskSetup.Filesystems {
			if len(fs.ExtraOpts) == 0 {
				fs.ExtraOpts = nil
			}
			spec.DiskSetup.Filesystems[i] = fs
		}
		if len(spec.DiskSetup.Partitions) == 0 {
			spec.DiskSetup.Partitions = nil
		}
	}

	// When rendered to cloud init, there is no diff between nil and empty Mounts.
	if len(spec.Mounts) == 0 {
		spec.Mounts = nil
	}

	// When rendered to cloud init, there is no diff between nil and empty BootCommands.
	if len(spec.BootCommands) == 0 {
		spec.BootCommands = nil
	}

	// When rendered to cloud init, there is no diff between nil and empty PreKubeadmCommands.
	if len(spec.PreKubeadmCommands) == 0 {
		spec.PreKubeadmCommands = nil
	}

	// When rendered to cloud init, there is no diff between nil and empty PostKubeadmCommands.
	if len(spec.PostKubeadmCommands) == 0 {
		spec.PostKubeadmCommands = nil
	}

	// When rendered to cloud init, there is no diff between nil and empty Users.
	// When rendered to cloud init, there is no diff between nil and empty Users[].SSHAuthorizedKeys.
	if len(spec.Users) == 0 {
		spec.Users = nil
	}
	for i, user := range spec.Users {
		if len(user.SSHAuthorizedKeys) == 0 {
			user.SSHAuthorizedKeys = nil
		}
		spec.Users[i] = user
	}

	// When rendered to cloud init, there is no diff between nil and empty ntp.servers.
	if spec.NTP != nil {
		if len(spec.NTP.Servers) == 0 {
			spec.NTP.Servers = nil
		}
	}
}
