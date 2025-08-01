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

package webhooks

import (
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilfeature "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/internal/webhooks/util"
)

var (
	ctx = ctrl.SetupSignalHandler()
)

func TestKubeadmControlPlaneDefault(t *testing.T) {
	g := NewWithT(t)

	kcp := &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "foo",
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			Version: "v1.18.3",
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "test",
						Kind:     "UnknownInfraMachine",
						Name:     "foo",
					},
				},
			},
		},
	}
	updateDefaultingValidationKCP := kcp.DeepCopy()
	updateDefaultingValidationKCP.Spec.Version = "v1.18.3"
	updateDefaultingValidationKCP.Spec.MachineTemplate.Spec.InfrastructureRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: "test",
		Kind:     "UnknownInfraMachine",
		Name:     "foo",
	}
	webhook := &KubeadmControlPlane{}
	t.Run("for KubeadmControlPlane", util.CustomDefaultValidateTest(ctx, updateDefaultingValidationKCP, webhook))
	g.Expect(webhook.Default(ctx, kcp)).To(Succeed())

	g.Expect(kcp.Spec.Version).To(Equal("v1.18.3"))
	g.Expect(kcp.Spec.Rollout.Strategy.Type).To(Equal(controlplanev1.RollingUpdateStrategyType))
	g.Expect(kcp.Spec.Rollout.Strategy.RollingUpdate.MaxSurge.IntVal).To(Equal(int32(1)))
}

func TestKubeadmControlPlaneValidateCreate(t *testing.T) {
	valid := &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "foo",
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "test",
						Kind:     "UnknownInfraMachine",
						Name:     "infraTemplate",
					},
				},
			},
			KubeadmConfigSpec: bootstrapv1.KubeadmConfigSpec{
				ClusterConfiguration: &bootstrapv1.ClusterConfiguration{},
			},
			Replicas: ptr.To[int32](1),
			Version:  "v1.19.0",
			Rollout: controlplanev1.KubeadmControlPlaneRolloutSpec{
				Strategy: controlplanev1.KubeadmControlPlaneRolloutStrategy{
					Type: controlplanev1.RollingUpdateStrategyType,
					RollingUpdate: controlplanev1.KubeadmControlPlaneRolloutStrategyRollingUpdate{
						MaxSurge: &intstr.IntOrString{
							IntVal: 1,
						},
					},
				},
			},
		},
	}

	invalidMaxSurge := valid.DeepCopy()
	invalidMaxSurge.Spec.Rollout.Strategy.RollingUpdate.MaxSurge.IntVal = int32(3)

	stringMaxSurge := valid.DeepCopy()
	val := intstr.FromString("1")
	stringMaxSurge.Spec.Rollout.Strategy.RollingUpdate.MaxSurge = &val

	missingReplicas := valid.DeepCopy()
	missingReplicas.Spec.Replicas = nil

	zeroReplicas := valid.DeepCopy()
	zeroReplicas.Spec.Replicas = ptr.To[int32](0)

	evenReplicas := valid.DeepCopy()
	evenReplicas.Spec.Replicas = ptr.To[int32](2)

	evenReplicasExternalEtcd := evenReplicas.DeepCopy()
	evenReplicasExternalEtcd.Spec.KubeadmConfigSpec = bootstrapv1.KubeadmConfigSpec{
		ClusterConfiguration: &bootstrapv1.ClusterConfiguration{
			Etcd: bootstrapv1.Etcd{
				External: &bootstrapv1.ExternalEtcd{},
			},
		},
	}

	validVersion := valid.DeepCopy()
	validVersion.Spec.Version = "v1.16.6"

	invalidVersion1 := valid.DeepCopy()
	invalidVersion1.Spec.Version = "vv1.16.6"

	invalidVersion2 := valid.DeepCopy()
	invalidVersion2.Spec.Version = "1.16.6"

	invalidCoreDNSVersion := valid.DeepCopy()
	invalidCoreDNSVersion.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS.ImageTag = "1-7" // not a valid semantic version

	invalidIgnitionConfiguration := valid.DeepCopy()
	invalidIgnitionConfiguration.Spec.KubeadmConfigSpec.Ignition = &bootstrapv1.IgnitionSpec{}

	validIgnitionConfiguration := valid.DeepCopy()
	validIgnitionConfiguration.Spec.KubeadmConfigSpec.Format = bootstrapv1.Ignition
	validIgnitionConfiguration.Spec.KubeadmConfigSpec.Ignition = &bootstrapv1.IgnitionSpec{}

	invalidMetadata := valid.DeepCopy()
	invalidMetadata.Spec.MachineTemplate.ObjectMeta.Labels = map[string]string{
		"foo":          "$invalid-key",
		"bar":          strings.Repeat("a", 64) + "too-long-value",
		"/invalid-key": "foo",
	}
	invalidMetadata.Spec.MachineTemplate.ObjectMeta.Annotations = map[string]string{
		"/invalid-key": "foo",
	}

	invalidControlPlaneComponentHealthCheckSeconds := valid.DeepCopy()
	invalidControlPlaneComponentHealthCheckSeconds.Spec.KubeadmConfigSpec.InitConfiguration = &bootstrapv1.InitConfiguration{Timeouts: &bootstrapv1.Timeouts{ControlPlaneComponentHealthCheckSeconds: ptr.To[int32](10)}}

	validControlPlaneComponentHealthCheckSeconds := valid.DeepCopy()
	validControlPlaneComponentHealthCheckSeconds.Spec.KubeadmConfigSpec.InitConfiguration = &bootstrapv1.InitConfiguration{Timeouts: &bootstrapv1.Timeouts{ControlPlaneComponentHealthCheckSeconds: ptr.To[int32](10)}}
	validControlPlaneComponentHealthCheckSeconds.Spec.KubeadmConfigSpec.JoinConfiguration = &bootstrapv1.JoinConfiguration{Timeouts: &bootstrapv1.Timeouts{ControlPlaneComponentHealthCheckSeconds: ptr.To[int32](10)}}

	invalidCertificateValidityPeriodDaysGreaterCA := valid.DeepCopy()
	invalidCertificateValidityPeriodDaysGreaterCA.Spec.KubeadmConfigSpec.ClusterConfiguration.CertificateValidityPeriodDays = 350
	invalidCertificateValidityPeriodDaysGreaterCA.Spec.KubeadmConfigSpec.ClusterConfiguration.CACertificateValidityPeriodDays = 300

	invalidCertificateValidityPeriodDaysGreaterDefault := valid.DeepCopy()
	invalidCertificateValidityPeriodDaysGreaterDefault.Spec.KubeadmConfigSpec.ClusterConfiguration.CertificateValidityPeriodDays = 3651
	invalidCertificateValidityPeriodDaysGreaterDefault.Spec.KubeadmConfigSpec.ClusterConfiguration.CACertificateValidityPeriodDays = 0 // default is 3650

	invalidRolloutBeforeCertificatesExpiryDays := valid.DeepCopy()
	invalidRolloutBeforeCertificatesExpiryDays.Spec.Rollout.Before.CertificatesExpiryDays = 8
	invalidRolloutBeforeCertificatesExpiryDays.Spec.KubeadmConfigSpec.ClusterConfiguration.CertificateValidityPeriodDays = 7

	tests := []struct {
		name                  string
		enableIgnitionFeature bool
		expectErr             bool
		kcp                   *controlplanev1.KubeadmControlPlane
	}{
		{
			name:      "should succeed when given a valid config",
			expectErr: false,
			kcp:       valid,
		},
		{
			name:      "should return error when replicas is nil",
			expectErr: true,
			kcp:       missingReplicas,
		},
		{
			name:      "should return error when replicas is zero",
			expectErr: true,
			kcp:       zeroReplicas,
		},
		{
			name:      "should return error when replicas is even",
			expectErr: true,
			kcp:       evenReplicas,
		},
		{
			name:      "should allow even replicas when using external etcd",
			expectErr: false,
			kcp:       evenReplicasExternalEtcd,
		},
		{
			name:      "should succeed when given a valid semantic version with prepended 'v'",
			expectErr: false,
			kcp:       validVersion,
		},
		{
			name:      "should error when given a valid semantic version without 'v'",
			expectErr: true,
			kcp:       invalidVersion2,
		},
		{
			name:      "should return error when given an invalid semantic version",
			expectErr: true,
			kcp:       invalidVersion1,
		},
		{
			name:      "should return error when given an invalid semantic CoreDNS version",
			expectErr: true,
			kcp:       invalidCoreDNSVersion,
		},
		{
			name:      "should return error when maxSurge is not 1",
			expectErr: true,
			kcp:       invalidMaxSurge,
		},
		{
			name:      "should succeed when maxSurge is a string",
			expectErr: false,
			kcp:       stringMaxSurge,
		},
		{
			name:                  "should return error when Ignition configuration is invalid",
			enableIgnitionFeature: true,
			expectErr:             true,
			kcp:                   invalidIgnitionConfiguration,
		},
		{
			name:                  "should succeed when Ignition configuration is valid",
			enableIgnitionFeature: true,
			expectErr:             false,
			kcp:                   validIgnitionConfiguration,
		},
		{
			name:                  "should return error for invalid metadata",
			enableIgnitionFeature: true,
			expectErr:             true,
			kcp:                   invalidMetadata,
		},
		{
			name:      "should return error for invalid Timeouts.ControlPlaneComponentHealthCheckSeconds",
			expectErr: true,
			kcp:       invalidControlPlaneComponentHealthCheckSeconds,
		},
		{
			name: "should pass for valid Timeouts.ControlPlaneComponentHealthCheckSeconds",
			kcp:  validControlPlaneComponentHealthCheckSeconds,
		},
		{
			name:      "should return error when CertificateValidityPeriodDays greater than CACertificateValidityPeriodDays",
			expectErr: true,
			kcp:       invalidCertificateValidityPeriodDaysGreaterCA,
		},
		{
			name:      "should return error when CertificateValidityPeriodDays greater than CACertificateValidityPeriodDays default",
			expectErr: true,
			kcp:       invalidCertificateValidityPeriodDaysGreaterDefault,
		},
		{
			name:      "should return error when rolloutBefore CertificatesExpiryDays greater than cluster CertificateValidityPeriodDays",
			expectErr: true,
			kcp:       invalidRolloutBeforeCertificatesExpiryDays,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.enableIgnitionFeature {
				// NOTE: KubeadmBootstrapFormatIgnition feature flag is disabled by default.
				// Enabling the feature flag temporarily for this test.
				utilfeature.SetFeatureGateDuringTest(t, feature.Gates, feature.KubeadmBootstrapFormatIgnition, true)
			}

			g := NewWithT(t)

			webhook := &KubeadmControlPlane{}

			warnings, err := webhook.ValidateCreate(ctx, tt.kcp)
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).ToNot(HaveOccurred())
			}
			g.Expect(warnings).To(BeEmpty())
		})
	}
}

func TestKubeadmControlPlaneValidateUpdate(t *testing.T) {
	before := &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "foo",
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "test",
						Kind:     "UnknownInfraMachine",
						Name:     "infraTemplate",
					},
					Deletion: controlplanev1.KubeadmControlPlaneMachineTemplateDeletionSpec{
						NodeDrainTimeoutSeconds:        ptr.To(int32(1)),
						NodeVolumeDetachTimeoutSeconds: ptr.To(int32(1)),
						NodeDeletionTimeoutSeconds:     ptr.To(int32(1)),
					},
				},
			},
			Replicas: ptr.To[int32](1),
			KubeadmConfigSpec: bootstrapv1.KubeadmConfigSpec{
				InitConfiguration: &bootstrapv1.InitConfiguration{
					LocalAPIEndpoint: bootstrapv1.APIEndpoint{
						AdvertiseAddress: "127.0.0.1",
						BindPort:         int32(443),
					},
					NodeRegistration: bootstrapv1.NodeRegistrationOptions{
						Name: "test",
					},
					Timeouts: &bootstrapv1.Timeouts{
						ControlPlaneComponentHealthCheckSeconds: ptr.To[int32](10),
						KubeletHealthCheckSeconds:               ptr.To[int32](40),
					},
				},
				ClusterConfiguration: &bootstrapv1.ClusterConfiguration{
					DNS: bootstrapv1.DNS{
						ImageMeta: bootstrapv1.ImageMeta{
							ImageRepository: "registry.k8s.io/coredns",
							ImageTag:        "1.6.5",
						},
					},
					CertificateValidityPeriodDays:   100,
					CACertificateValidityPeriodDays: 365,
				},
				JoinConfiguration: &bootstrapv1.JoinConfiguration{
					NodeRegistration: bootstrapv1.NodeRegistrationOptions{
						Name: "test",
					},
					Timeouts: &bootstrapv1.Timeouts{
						ControlPlaneComponentHealthCheckSeconds: ptr.To[int32](10),
						KubeletHealthCheckSeconds:               ptr.To[int32](40),
					},
				},
				PreKubeadmCommands: []string{
					"test", "foo",
				},
				PostKubeadmCommands: []string{
					"test", "foo",
				},
				Files: []bootstrapv1.File{
					{
						Path: "test",
					},
				},
				Users: []bootstrapv1.User{
					{
						Name: "user",
						SSHAuthorizedKeys: []string{
							"ssh-rsa foo",
						},
					},
				},
				NTP: &bootstrapv1.NTP{
					Servers: []string{"test-server-1", "test-server-2"},
					Enabled: ptr.To(true),
				},
			},
			Version: "v1.16.6",
			Rollout: controlplanev1.KubeadmControlPlaneRolloutSpec{
				Before: controlplanev1.KubeadmControlPlaneRolloutBeforeSpec{
					CertificatesExpiryDays: 7,
				},
				Strategy: controlplanev1.KubeadmControlPlaneRolloutStrategy{
					Type: controlplanev1.RollingUpdateStrategyType,
					RollingUpdate: controlplanev1.KubeadmControlPlaneRolloutStrategyRollingUpdate{
						MaxSurge: &intstr.IntOrString{
							IntVal: 1,
						},
					},
				},
			},
		},
	}

	updateMaxSurgeVal := before.DeepCopy()
	updateMaxSurgeVal.Spec.Rollout.Strategy.RollingUpdate.MaxSurge.IntVal = int32(0)
	updateMaxSurgeVal.Spec.Replicas = ptr.To[int32](3)

	wrongReplicaCountForScaleIn := before.DeepCopy()
	wrongReplicaCountForScaleIn.Spec.Rollout.Strategy.RollingUpdate.MaxSurge.IntVal = int32(0)

	validUpdateKubeadmConfigInit := before.DeepCopy()
	validUpdateKubeadmConfigInit.Spec.KubeadmConfigSpec.InitConfiguration.NodeRegistration = bootstrapv1.NodeRegistrationOptions{}

	invalidUpdateKubeadmConfigCluster := before.DeepCopy()
	invalidUpdateKubeadmConfigCluster.Spec.KubeadmConfigSpec.ClusterConfiguration = &bootstrapv1.ClusterConfiguration{
		CertificatesDir: "some-other-value",
	}

	validUpdateKubeadmConfigJoin := before.DeepCopy()
	validUpdateKubeadmConfigJoin.Spec.KubeadmConfigSpec.JoinConfiguration.NodeRegistration = bootstrapv1.NodeRegistrationOptions{}

	beforeKubeadmConfigFormatSet := before.DeepCopy()
	beforeKubeadmConfigFormatSet.Spec.KubeadmConfigSpec.Format = bootstrapv1.CloudConfig
	invalidUpdateKubeadmConfigFormat := beforeKubeadmConfigFormatSet.DeepCopy()
	invalidUpdateKubeadmConfigFormat.Spec.KubeadmConfigSpec.Format = bootstrapv1.Ignition

	validUpdate := before.DeepCopy()
	validUpdate.Labels = map[string]string{"blue": "green"}
	validUpdate.Spec.KubeadmConfigSpec.BootCommands = []string{"ab", "abc"}
	validUpdate.Spec.KubeadmConfigSpec.PreKubeadmCommands = []string{"ab", "abc"}
	validUpdate.Spec.KubeadmConfigSpec.PostKubeadmCommands = []string{"ab", "abc"}
	validUpdate.Spec.KubeadmConfigSpec.Files = []bootstrapv1.File{
		{
			Path: "ab",
		},
		{
			Path: "abc",
		},
	}
	validUpdate.Spec.Version = "v1.17.1"
	validUpdate.Spec.KubeadmConfigSpec.Users = []bootstrapv1.User{
		{
			Name: "bar",
			SSHAuthorizedKeys: []string{
				"ssh-rsa bar",
				"ssh-rsa foo",
			},
		},
	}
	validUpdate.Spec.MachineTemplate.ObjectMeta.Labels = map[string]string{
		"label": "labelValue",
	}
	validUpdate.Spec.MachineTemplate.ObjectMeta.Annotations = map[string]string{
		"annotation": "labelAnnotation",
	}
	validUpdate.Spec.MachineTemplate.Spec.InfrastructureRef.APIGroup = "test-changed"
	validUpdate.Spec.MachineTemplate.Spec.InfrastructureRef.Name = "orange"
	validUpdate.Spec.MachineTemplate.Spec.Deletion.NodeDrainTimeoutSeconds = ptr.To(int32(10))
	validUpdate.Spec.MachineTemplate.Spec.Deletion.NodeVolumeDetachTimeoutSeconds = ptr.To(int32(10))
	validUpdate.Spec.MachineTemplate.Spec.Deletion.NodeDeletionTimeoutSeconds = ptr.To(int32(10))
	validUpdate.Spec.Replicas = ptr.To[int32](5)
	now := metav1.NewTime(time.Now())
	validUpdate.Spec.Rollout.After = now
	validUpdate.Spec.Rollout.Before.CertificatesExpiryDays = 14
	validUpdate.Spec.Remediation = controlplanev1.KubeadmControlPlaneRemediationSpec{
		MaxRetry:                ptr.To[int32](50),
		MinHealthyPeriodSeconds: ptr.To(int32(10 * 60 * 60)),
		RetryPeriodSeconds:      ptr.To[int32](10 * 60),
	}
	validUpdate.Spec.KubeadmConfigSpec.Format = bootstrapv1.CloudConfig

	scaleToZero := before.DeepCopy()
	scaleToZero.Spec.Replicas = ptr.To[int32](0)

	scaleToEven := before.DeepCopy()
	scaleToEven.Spec.Replicas = ptr.To[int32](2)

	missingReplicas := before.DeepCopy()
	missingReplicas.Spec.Replicas = nil

	etcdLocalImageTag := before.DeepCopy()
	etcdLocalImageTag.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageTag: "v9.1.1",
		},
	}
	etcdLocalImageTagAndDataDir := etcdLocalImageTag.DeepCopy()
	etcdLocalImageTagAndDataDir.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local.DataDir = "/foo"

	etcdLocalImageBuildTag := before.DeepCopy()
	etcdLocalImageBuildTag.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageTag: "v9.1.1_validBuild1",
		},
	}

	etcdLocalImageInvalidTag := before.DeepCopy()
	etcdLocalImageInvalidTag.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageTag: "v9.1.1+invalidBuild1",
		},
	}

	unsetEtcdLocal := etcdLocalImageTag.DeepCopy()
	unsetEtcdLocal.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = nil

	controlPlaneEndpoint := before.DeepCopy()
	controlPlaneEndpoint.Spec.KubeadmConfigSpec.ClusterConfiguration.ControlPlaneEndpoint = "some control plane endpoint"

	apiServer := before.DeepCopy()
	apiServer.Spec.KubeadmConfigSpec.ClusterConfiguration.APIServer = bootstrapv1.APIServer{
		ExtraArgs: []bootstrapv1.Arg{
			{
				Name:  "foo",
				Value: "bar",
			},
		},
		ExtraVolumes: []bootstrapv1.HostPathMount{{Name: "mount1"}},
		CertSANs:     []string{"foo", "bar"},
	}

	controllerManager := before.DeepCopy()
	controllerManager.Spec.KubeadmConfigSpec.ClusterConfiguration.ControllerManager = bootstrapv1.ControllerManager{
		ExtraArgs: []bootstrapv1.Arg{
			{
				Name:  "controller manager field",
				Value: "controller manager value",
			},
		},
		ExtraVolumes: []bootstrapv1.HostPathMount{{Name: "mount", HostPath: "/foo", MountPath: "bar", ReadOnly: ptr.To(true), PathType: "File"}},
	}

	scheduler := before.DeepCopy()
	scheduler.Spec.KubeadmConfigSpec.ClusterConfiguration.Scheduler = bootstrapv1.Scheduler{
		ExtraArgs: []bootstrapv1.Arg{
			{
				Name:  "scheduler field",
				Value: "scheduler value",
			},
		},
		ExtraVolumes: []bootstrapv1.HostPathMount{{Name: "mount", HostPath: "/foo", MountPath: "bar", ReadOnly: ptr.To(true), PathType: "File"}},
	}

	dns := before.DeepCopy()
	dns.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "v1.6.6_foobar.1",
		},
	}

	dnsBuildTag := before.DeepCopy()
	dnsBuildTag.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "1.6.7",
		},
	}

	dnsInvalidTag := before.DeepCopy()
	dnsInvalidTag.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "v0.20.0+invalidBuild1",
		},
	}

	dnsInvalidCoreDNSToVersion := dns.DeepCopy()
	dnsInvalidCoreDNSToVersion.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "1.6.5",
		},
	}

	validCoreDNSCustomToVersion := dns.DeepCopy()
	validCoreDNSCustomToVersion.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "v1.6.6_foobar.2",
		},
	}
	validUnsupportedCoreDNSVersion := dns.DeepCopy()
	validUnsupportedCoreDNSVersion.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "v99.99.99",
		},
	}

	validUnsupportedCoreDNSVersionWithSkipAnnotation := dns.DeepCopy()
	validUnsupportedCoreDNSVersionWithSkipAnnotation.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "gcr.io/capi-test",
			ImageTag:        "v99.99.99",
		},
	}
	validUnsupportedCoreDNSVersionWithSkipAnnotation.Annotations = map[string]string{
		controlplanev1.SkipCoreDNSAnnotation: "",
	}

	unsetCoreDNSToVersion := dns.DeepCopy()
	unsetCoreDNSToVersion.Spec.KubeadmConfigSpec.ClusterConfiguration.DNS = bootstrapv1.DNS{
		ImageMeta: bootstrapv1.ImageMeta{
			ImageRepository: "",
			ImageTag:        "",
		},
	}

	certificatesDir := before.DeepCopy()
	certificatesDir.Spec.KubeadmConfigSpec.ClusterConfiguration.CertificatesDir = "a new certificates directory"

	imageRepository := before.DeepCopy()
	imageRepository.Spec.KubeadmConfigSpec.ClusterConfiguration.ImageRepository = "a new image repository"

	featureGates := before.DeepCopy()
	featureGates.Spec.KubeadmConfigSpec.ClusterConfiguration.FeatureGates = map[string]bool{"a feature gate": true}

	externalEtcd := before.DeepCopy()
	externalEtcd.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.External = &bootstrapv1.ExternalEtcd{
		KeyFile: "some key file",
	}
	externalEtcdChanged := before.DeepCopy()
	externalEtcdChanged.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.External = &bootstrapv1.ExternalEtcd{
		KeyFile: "another key file",
	}

	localDataDir := before.DeepCopy()
	localDataDir.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		DataDir: "some local data dir",
	}

	localPeerCertSANs := before.DeepCopy()
	localPeerCertSANs.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		PeerCertSANs: []string{"a cert"},
	}

	localServerCertSANs := before.DeepCopy()
	localServerCertSANs.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		ServerCertSANs: []string{"a cert"},
	}

	localExtraArgs := before.DeepCopy()
	localExtraArgs.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd.Local = &bootstrapv1.LocalEtcd{
		ExtraArgs: []bootstrapv1.Arg{
			{
				Name:  "an arg",
				Value: "a value",
			},
		},
	}

	beforeExternalEtcdCluster := before.DeepCopy()
	beforeExternalEtcdCluster.Spec.KubeadmConfigSpec.ClusterConfiguration = &bootstrapv1.ClusterConfiguration{
		Etcd: bootstrapv1.Etcd{
			External: &bootstrapv1.ExternalEtcd{
				Endpoints: []string{"127.0.0.1"},
			},
		},
	}
	scaleToEvenExternalEtcdCluster := beforeExternalEtcdCluster.DeepCopy()
	scaleToEvenExternalEtcdCluster.Spec.Replicas = ptr.To[int32](2)

	beforeInvalidEtcdCluster := before.DeepCopy()
	beforeInvalidEtcdCluster.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd = bootstrapv1.Etcd{
		Local: &bootstrapv1.LocalEtcd{
			ImageMeta: bootstrapv1.ImageMeta{
				ImageRepository: "image-repository",
				ImageTag:        "latest",
			},
		},
	}

	afterInvalidEtcdCluster := beforeInvalidEtcdCluster.DeepCopy()
	afterInvalidEtcdCluster.Spec.KubeadmConfigSpec.ClusterConfiguration.Etcd = bootstrapv1.Etcd{
		External: &bootstrapv1.ExternalEtcd{
			Endpoints: []string{"127.0.0.1"},
		},
	}

	withoutClusterConfiguration := before.DeepCopy()
	withoutClusterConfiguration.Spec.KubeadmConfigSpec.ClusterConfiguration = nil

	updateNTPServers := before.DeepCopy()
	updateNTPServers.Spec.KubeadmConfigSpec.NTP.Servers = []string{"new-server"}

	disableNTPServers := before.DeepCopy()
	disableNTPServers.Spec.KubeadmConfigSpec.NTP.Enabled = ptr.To(false)

	unsetRolloutBefore := before.DeepCopy()
	unsetRolloutBefore.Spec.Rollout.Before = controlplanev1.KubeadmControlPlaneRolloutBeforeSpec{}

	invalidIgnitionConfiguration := before.DeepCopy()
	invalidIgnitionConfiguration.Spec.KubeadmConfigSpec.Ignition = &bootstrapv1.IgnitionSpec{}

	validIgnitionConfigurationBefore := before.DeepCopy()
	validIgnitionConfigurationBefore.Spec.KubeadmConfigSpec.Format = bootstrapv1.Ignition
	validIgnitionConfigurationBefore.Spec.KubeadmConfigSpec.Ignition = &bootstrapv1.IgnitionSpec{
		ContainerLinuxConfig: &bootstrapv1.ContainerLinuxConfig{},
	}

	validIgnitionConfigurationAfter := validIgnitionConfigurationBefore.DeepCopy()
	validIgnitionConfigurationAfter.Spec.KubeadmConfigSpec.Ignition.ContainerLinuxConfig.AdditionalConfig = "foo: bar"

	updateInitConfigurationPatches := before.DeepCopy()
	updateInitConfigurationPatches.Spec.KubeadmConfigSpec.InitConfiguration.Patches = &bootstrapv1.Patches{
		Directory: "/tmp/patches",
	}

	updateJoinConfigurationPatches := before.DeepCopy()
	updateJoinConfigurationPatches.Spec.KubeadmConfigSpec.InitConfiguration.Patches = &bootstrapv1.Patches{
		Directory: "/tmp/patches",
	}

	updateInitConfigurationSkipPhases := before.DeepCopy()
	updateInitConfigurationSkipPhases.Spec.KubeadmConfigSpec.InitConfiguration.SkipPhases = []string{"addon/kube-proxy"}

	updateJoinConfigurationSkipPhases := before.DeepCopy()
	updateJoinConfigurationSkipPhases.Spec.KubeadmConfigSpec.JoinConfiguration.SkipPhases = []string{"addon/kube-proxy"}

	updateDiskSetup := before.DeepCopy()
	updateDiskSetup.Spec.KubeadmConfigSpec.DiskSetup = &bootstrapv1.DiskSetup{
		Filesystems: []bootstrapv1.Filesystem{
			{
				Device:     "/dev/sda",
				Filesystem: "ext4",
			},
		},
	}

	switchFromCloudInitToIgnition := before.DeepCopy()
	switchFromCloudInitToIgnition.Spec.KubeadmConfigSpec.Format = bootstrapv1.Ignition
	switchFromCloudInitToIgnition.Spec.KubeadmConfigSpec.Mounts = []bootstrapv1.MountPoints{
		{"/var/lib/testdir", "/var/lib/etcd/data"},
	}

	invalidMetadata := before.DeepCopy()
	invalidMetadata.Spec.MachineTemplate.ObjectMeta.Labels = map[string]string{
		"foo":          "$invalid-key",
		"bar":          strings.Repeat("a", 64) + "too-long-value",
		"/invalid-key": "foo",
	}
	invalidMetadata.Spec.MachineTemplate.ObjectMeta.Annotations = map[string]string{
		"/invalid-key": "foo",
	}

	changeTimeouts := before.DeepCopy()
	changeTimeouts.Spec.KubeadmConfigSpec.InitConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds = ptr.To[int32](20) // before 10
	changeTimeouts.Spec.KubeadmConfigSpec.InitConfiguration.Timeouts.KubeletHealthCheckSeconds = nil                             // before set
	changeTimeouts.Spec.KubeadmConfigSpec.InitConfiguration.Timeouts.EtcdAPICallSeconds = ptr.To[int32](20)                      // before not set
	changeTimeouts.Spec.KubeadmConfigSpec.JoinConfiguration.Timeouts.ControlPlaneComponentHealthCheckSeconds = ptr.To[int32](20) // before 10
	changeTimeouts.Spec.KubeadmConfigSpec.JoinConfiguration.Timeouts.KubeletHealthCheckSeconds = nil                             // before set
	changeTimeouts.Spec.KubeadmConfigSpec.JoinConfiguration.Timeouts.EtcdAPICallSeconds = ptr.To[int32](20)                      // before not set

	unsetTimeouts := before.DeepCopy()
	unsetTimeouts.Spec.KubeadmConfigSpec.InitConfiguration.Timeouts = nil
	unsetTimeouts.Spec.KubeadmConfigSpec.JoinConfiguration.Timeouts = nil

	validUpdateCertificateValidityPeriod := before.DeepCopy()
	validUpdateCertificateValidityPeriod.Spec.KubeadmConfigSpec.ClusterConfiguration.CertificateValidityPeriodDays = 150

	invalidUpdateCACertificateValidityPeriodDays := before.DeepCopy()
	invalidUpdateCACertificateValidityPeriodDays.Spec.KubeadmConfigSpec.ClusterConfiguration = &bootstrapv1.ClusterConfiguration{
		CACertificateValidityPeriodDays: 730,
	}

	tests := []struct {
		name                  string
		enableIgnitionFeature bool
		expectErr             bool
		before                *controlplanev1.KubeadmControlPlane
		kcp                   *controlplanev1.KubeadmControlPlane
	}{
		{
			name:      "should succeed when given a valid config",
			expectErr: false,
			before:    before,
			kcp:       validUpdate,
		},
		{
			name:      "should not return an error when trying to mutate the kubeadmconfigspec initconfiguration noderegistration",
			expectErr: false,
			before:    before,
			kcp:       validUpdateKubeadmConfigInit,
		},
		{
			name:      "should return error when trying to mutate the kubeadmconfigspec clusterconfiguration",
			expectErr: true,
			before:    before,
			kcp:       invalidUpdateKubeadmConfigCluster,
		},
		{
			name:      "should not return an error when trying to mutate the kubeadmconfigspec joinconfiguration noderegistration",
			expectErr: false,
			before:    before,
			kcp:       validUpdateKubeadmConfigJoin,
		},
		{
			name:      "should return error when trying to mutate the kubeadmconfigspec format from cloud-config to ignition",
			expectErr: true,
			before:    beforeKubeadmConfigFormatSet,
			kcp:       invalidUpdateKubeadmConfigFormat,
		},
		{
			name:      "should return error when trying to scale to zero",
			expectErr: true,
			before:    before,
			kcp:       scaleToZero,
		},
		{
			name:      "should return error when trying to scale to an even number",
			expectErr: true,
			before:    before,
			kcp:       scaleToEven,
		},
		{
			name:      "should return error when trying to scale to nil",
			expectErr: true,
			before:    before,
			kcp:       missingReplicas,
		},
		{
			name:      "should succeed when trying to scale to an even number with external etcd defined in ClusterConfiguration",
			expectErr: false,
			before:    beforeExternalEtcdCluster,
			kcp:       scaleToEvenExternalEtcdCluster,
		},
		{
			name:      "should succeed when making a change to the local etcd image tag",
			expectErr: false,
			before:    before,
			kcp:       etcdLocalImageTag,
		},
		{
			name:      "should succeed when making a change to the local etcd image tag",
			expectErr: false,
			before:    before,
			kcp:       etcdLocalImageBuildTag,
		},
		{
			name:      "should fail when using an invalid etcd image tag",
			expectErr: true,
			before:    before,
			kcp:       etcdLocalImageInvalidTag,
		},
		{
			name:      "should fail when making a change to the cluster config's controlPlaneEndpoint",
			expectErr: true,
			before:    before,
			kcp:       controlPlaneEndpoint,
		},
		{
			name:      "should allow changes to the cluster config's apiServer",
			expectErr: false,
			before:    before,
			kcp:       apiServer,
		},
		{
			name:      "should allow changes to the cluster config's controllerManager",
			expectErr: false,
			before:    before,
			kcp:       controllerManager,
		},
		{
			name:      "should allow changes to the cluster config's scheduler",
			expectErr: false,
			before:    before,
			kcp:       scheduler,
		},
		{
			name:      "should succeed when making a change to the cluster config's dns",
			expectErr: false,
			before:    before,
			kcp:       dns,
		},
		{
			name:      "should succeed when changing to a valid custom CoreDNS version",
			expectErr: false,
			before:    dns,
			kcp:       validCoreDNSCustomToVersion,
		},
		{
			name:      "should succeed when CoreDNS ImageTag is unset",
			expectErr: false,
			before:    dns,
			kcp:       unsetCoreDNSToVersion,
		},
		{
			name:      "should succeed when DNS is set to nil",
			expectErr: false,
			before:    dns,
			kcp:       unsetCoreDNSToVersion,
		},
		{
			name:      "should succeed when using an valid DNS build",
			expectErr: false,
			before:    before,
			kcp:       dnsBuildTag,
		},
		{
			name:   "should succeed when using the same CoreDNS version",
			before: dns,
			kcp:    dns.DeepCopy(),
		},
		{
			name:   "should succeed when using the same CoreDNS version - not supported",
			before: validUnsupportedCoreDNSVersion,
			kcp:    validUnsupportedCoreDNSVersion,
		},
		{
			name:      "should fail when upgrading to an unsupported version",
			before:    dns,
			kcp:       validUnsupportedCoreDNSVersion,
			expectErr: true,
		},
		{
			name:   "should succeed when upgrading to an unsupported version and KCP has skip annotation set",
			before: dns,
			kcp:    validUnsupportedCoreDNSVersionWithSkipAnnotation,
		},
		{
			name:      "should fail when using an invalid DNS build",
			expectErr: true,
			before:    before,
			kcp:       dnsInvalidTag,
		},
		{
			name:      "should fail when using an invalid CoreDNS version",
			expectErr: true,
			before:    dns,
			kcp:       dnsInvalidCoreDNSToVersion,
		},

		{
			name:      "should fail when making a change to the cluster config's certificatesDir",
			expectErr: true,
			before:    before,
			kcp:       certificatesDir,
		},
		{
			name:      "should fail when making a change to the cluster config's imageRepository",
			expectErr: false,
			before:    before,
			kcp:       imageRepository,
		},
		{
			name:      "should succeed when making a change to the cluster config's featureGates",
			expectErr: false,
			before:    before,
			kcp:       featureGates,
		},
		{
			name:      "should succeed when making a change to the cluster config's local etcd's configuration localDataDir field",
			expectErr: false,
			before:    before,
			kcp:       localDataDir,
		},
		{
			name:      "should succeed when making a change to the cluster config's local etcd's configuration localPeerCertSANs field",
			expectErr: false,
			before:    before,
			kcp:       localPeerCertSANs,
		},
		{
			name:      "should succeed when making a change to the cluster config's local etcd's configuration localServerCertSANs field",
			expectErr: false,
			before:    before,
			kcp:       localServerCertSANs,
		},
		{
			name:      "should succeed when making a change to the cluster config's local etcd's configuration localExtraArgs field",
			expectErr: false,
			before:    before,
			kcp:       localExtraArgs,
		},
		{
			name:      "should succeed when making a change to the cluster config's external etcd's configuration",
			expectErr: false,
			before:    externalEtcd,
			kcp:       externalEtcdChanged,
		},
		{
			name:      "should succeed when adding the cluster config's local etcd's configuration",
			expectErr: false,
			before:    unsetEtcdLocal,
			kcp:       etcdLocalImageTag,
		},
		{
			name:      "should succeed when making a change to the cluster config's local etcd's configuration",
			expectErr: false,
			before:    etcdLocalImageTag,
			kcp:       etcdLocalImageTagAndDataDir,
		},
		{
			name:      "should succeed when attempting to unset the etcd local object to fallback to the default",
			expectErr: false,
			before:    etcdLocalImageTag,
			kcp:       unsetEtcdLocal,
		},
		{
			name:      "should fail if both local and external etcd are set",
			expectErr: true,
			before:    beforeInvalidEtcdCluster,
			kcp:       afterInvalidEtcdCluster,
		},
		{
			name:      "should pass if ClusterConfiguration is nil",
			expectErr: false,
			before:    withoutClusterConfiguration,
			kcp:       withoutClusterConfiguration,
		},
		{
			name:      "should not return an error when maxSurge value is updated to 0",
			expectErr: false,
			before:    before,
			kcp:       updateMaxSurgeVal,
		},
		{
			name:      "should return an error when maxSurge value is updated to 0, but replica count is < 3",
			expectErr: true,
			before:    before,
			kcp:       wrongReplicaCountForScaleIn,
		},
		{
			name:      "should pass if NTP servers are updated",
			expectErr: false,
			before:    before,
			kcp:       updateNTPServers,
		},
		{
			name:      "should pass if NTP servers is disabled during update",
			expectErr: false,
			before:    before,
			kcp:       disableNTPServers,
		},
		{
			name:      "should allow changes to initConfiguration.patches",
			expectErr: false,
			before:    before,
			kcp:       updateInitConfigurationPatches,
		},
		{
			name:      "should allow changes to joinConfiguration.patches",
			expectErr: false,
			before:    before,
			kcp:       updateJoinConfigurationPatches,
		},
		{
			name:      "should allow changes to initConfiguration.skipPhases",
			expectErr: false,
			before:    before,
			kcp:       updateInitConfigurationSkipPhases,
		},
		{
			name:      "should allow changes to joinConfiguration.skipPhases",
			expectErr: false,
			before:    before,
			kcp:       updateJoinConfigurationSkipPhases,
		},
		{
			name:      "should allow changes to diskSetup",
			expectErr: false,
			before:    before,
			kcp:       updateDiskSetup,
		},
		{
			name:      "should allow unsetting rolloutBefore",
			expectErr: false,
			before:    before,
			kcp:       unsetRolloutBefore,
		},
		{
			name:                  "should return error when Ignition configuration is invalid",
			enableIgnitionFeature: true,
			expectErr:             true,
			before:                invalidIgnitionConfiguration,
			kcp:                   invalidIgnitionConfiguration,
		},
		{
			name:                  "should succeed when Ignition configuration is modified",
			enableIgnitionFeature: true,
			expectErr:             false,
			before:                validIgnitionConfigurationBefore,
			kcp:                   validIgnitionConfigurationAfter,
		},
		{
			name:                  "should succeed when CloudInit was used before",
			enableIgnitionFeature: true,
			expectErr:             false,
			before:                before,
			kcp:                   switchFromCloudInitToIgnition,
		},
		{
			name:                  "should return error for invalid metadata",
			enableIgnitionFeature: true,
			expectErr:             true,
			before:                before,
			kcp:                   invalidMetadata,
		},
		{
			name:      "should succeed when changing timeouts",
			expectErr: false,
			before:    before,
			kcp:       changeTimeouts,
		},
		{
			name:      "should succeed when unsetting timeouts",
			expectErr: false,
			before:    before,
			kcp:       unsetTimeouts,
		},
		{
			name:      "should succeed when setting timeouts",
			expectErr: false,
			before:    unsetTimeouts,
			kcp:       changeTimeouts,
		},
		{
			name:      "should succeed when making a change to the cluster config's certificateValidityPeriod",
			expectErr: false,
			before:    before,
			kcp:       validUpdateCertificateValidityPeriod,
		},
		{
			name:      "should return error when trying to mutate the cluster config's caCertificateValidityPeriodDays",
			expectErr: true,
			before:    before,
			kcp:       invalidUpdateCACertificateValidityPeriodDays,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.enableIgnitionFeature {
				// NOTE: KubeadmBootstrapFormatIgnition feature flag is disabled by default.
				// Enabling the feature flag temporarily for this test.
				utilfeature.SetFeatureGateDuringTest(t, feature.Gates, feature.KubeadmBootstrapFormatIgnition, true)
			}

			g := NewWithT(t)

			webhook := &KubeadmControlPlane{}

			warnings, err := webhook.ValidateUpdate(ctx, tt.before.DeepCopy(), tt.kcp)
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).To(Succeed())
			}
			g.Expect(warnings).To(BeEmpty())
		})
	}
}

func TestValidateVersion(t *testing.T) {
	tests := []struct {
		name                 string
		clusterConfiguration *bootstrapv1.ClusterConfiguration
		oldVersion           string
		newVersion           string
		expectErr            bool
	}{
		// Basic validation of old and new version.
		{
			name:       "error when old version is empty",
			oldVersion: "",
			newVersion: "v1.16.6",
			expectErr:  true,
		},
		{
			name:       "error when old version is invalid",
			oldVersion: "invalid-version",
			newVersion: "v1.18.1",
			expectErr:  true,
		},
		{
			name:       "error when new version is empty",
			oldVersion: "v1.16.6",
			newVersion: "",
			expectErr:  true,
		},
		{
			name:       "error when new version is invalid",
			oldVersion: "v1.18.1",
			newVersion: "invalid-version",
			expectErr:  true,
		},
		{
			name:       "pass when both versions are v1.19.0",
			oldVersion: "v1.19.0",
			newVersion: "v1.19.0",
			expectErr:  false,
		},
		// Validation for skip-level upgrades.
		{
			name:       "error when upgrading two minor versions",
			oldVersion: "v1.18.8",
			newVersion: "v1.20.0-alpha.0.734_ba502ee555924a",
			expectErr:  true,
		},
		{
			name:       "pass when upgrading one minor version",
			oldVersion: "v1.20.1",
			newVersion: "v1.21.18",
			expectErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			kcpNew := controlplanev1.KubeadmControlPlane{
				Spec: controlplanev1.KubeadmControlPlaneSpec{
					KubeadmConfigSpec: bootstrapv1.KubeadmConfigSpec{
						ClusterConfiguration: tt.clusterConfiguration,
					},
					Version: tt.newVersion,
				},
			}

			kcpOld := controlplanev1.KubeadmControlPlane{
				Spec: controlplanev1.KubeadmControlPlaneSpec{
					KubeadmConfigSpec: bootstrapv1.KubeadmConfigSpec{
						ClusterConfiguration: tt.clusterConfiguration,
					},
					Version: tt.oldVersion,
				},
			}

			webhook := &KubeadmControlPlane{}

			allErrs := webhook.validateVersion(&kcpOld, &kcpNew)
			if tt.expectErr {
				g.Expect(allErrs).ToNot(BeEmpty())
			} else {
				g.Expect(allErrs).To(BeEmpty())
			}
		})
	}
}
func TestKubeadmControlPlaneValidateUpdateAfterDefaulting(t *testing.T) {
	g := NewWithT(t)

	before := &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "foo",
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			Version: "v1.19.0",
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "test",
						Kind:     "UnknownInfraMachine",
						Name:     "infraTemplate",
					},
				},
			},
		},
	}

	afterDefault := before.DeepCopy()
	webhook := &KubeadmControlPlane{}
	g.Expect(webhook.Default(ctx, afterDefault)).To(Succeed())

	tests := []struct {
		name      string
		expectErr bool
		before    *controlplanev1.KubeadmControlPlane
		kcp       *controlplanev1.KubeadmControlPlane
	}{
		{
			name:      "update should succeed after defaulting",
			expectErr: false,
			before:    before,
			kcp:       afterDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			webhook := &KubeadmControlPlane{}

			warnings, err := webhook.ValidateUpdate(ctx, tt.before.DeepCopy(), tt.kcp)
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).To(Succeed())
				g.Expect(tt.kcp.Spec.Version).To(Equal("v1.19.0"))
				g.Expect(tt.kcp.Spec.Rollout.Strategy.Type).To(Equal(controlplanev1.RollingUpdateStrategyType))
				g.Expect(tt.kcp.Spec.Rollout.Strategy.RollingUpdate.MaxSurge.IntVal).To(Equal(int32(1)))
				g.Expect(tt.kcp.Spec.Replicas).To(Equal(ptr.To[int32](1)))
			}
			g.Expect(warnings).To(BeEmpty())
		})
	}
}

func TestPathsMatch(t *testing.T) {
	tests := []struct {
		name          string
		allowed, path []string
		match         bool
	}{
		{
			name:    "a simple match case",
			allowed: []string{"a", "b", "c"},
			path:    []string{"a", "b", "c"},
			match:   true,
		},
		{
			name:    "a case can't match",
			allowed: []string{"a", "b", "c"},
			path:    []string{"a"},
			match:   false,
		},
		{
			name:    "an empty path for whatever reason",
			allowed: []string{"a"},
			path:    []string{""},
			match:   false,
		},
		{
			name:    "empty allowed matches nothing",
			allowed: []string{},
			path:    []string{"a"},
			match:   false,
		},
		{
			name:    "wildcard match",
			allowed: []string{"a", "b", "c", "d", "*"},
			path:    []string{"a", "b", "c", "d", "e", "f", "g"},
			match:   true,
		},
		{
			name:    "long path",
			allowed: []string{"a"},
			path:    []string{"a", "b", "c", "d", "e", "f", "g"},
			match:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(pathsMatch(tt.allowed, tt.path)).To(Equal(tt.match))
		})
	}
}

func TestAllowed(t *testing.T) {
	tests := []struct {
		name      string
		allowList [][]string
		path      []string
		match     bool
	}{
		{
			name: "matches the first and none of the others",
			allowList: [][]string{
				{"a", "b", "c"},
				{"b", "d", "x"},
			},
			path:  []string{"a", "b", "c"},
			match: true,
		},
		{
			name: "matches none in the allow list",
			allowList: [][]string{
				{"a", "b", "c"},
				{"b", "c", "d"},
				{"e", "*"},
			},
			path:  []string{"a"},
			match: false,
		},
		{
			name: "an empty path matches nothing",
			allowList: [][]string{
				{"a", "b", "c"},
				{"*"},
				{"b", "c"},
			},
			path:  []string{},
			match: false,
		},
		{
			name:      "empty allowList matches nothing",
			allowList: [][]string{},
			path:      []string{"a"},
			match:     false,
		},
		{
			name: "length test check",
			allowList: [][]string{
				{"a", "b", "c", "d", "e", "f"},
				{"a", "b", "c", "d", "e", "f", "g", "h"},
			},
			path:  []string{"a", "b", "c", "d", "e", "f", "g"},
			match: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(allowed(tt.allowList, tt.path)).To(Equal(tt.match))
		})
	}
}

func TestPaths(t *testing.T) {
	tests := []struct {
		name     string
		path     []string
		diff     map[string]interface{}
		expected [][]string
	}{
		{
			name: "basic check",
			diff: map[string]interface{}{
				"spec": map[string]interface{}{
					"replicas": 4,
					"version":  "1.17.3",
					"kubeadmConfigSpec": map[string]interface{}{
						"clusterConfiguration": map[string]interface{}{
							"version": "v2.0.1",
						},
						"initConfiguration": map[string]interface{}{
							"bootstrapToken": []string{"abcd", "defg"},
						},
						"joinConfiguration": nil,
					},
				},
			},
			expected: [][]string{
				{"spec", "replicas"},
				{"spec", "version"},
				{"spec", "kubeadmConfigSpec", "joinConfiguration"},
				{"spec", "kubeadmConfigSpec", "clusterConfiguration", "version"},
				{"spec", "kubeadmConfigSpec", "initConfiguration", "bootstrapToken"},
			},
		},
		{
			name:     "empty input makes for empty output",
			path:     []string{"a"},
			diff:     map[string]interface{}{},
			expected: [][]string{},
		},
		{
			name: "long recursive check with two keys",
			diff: map[string]interface{}{
				"spec": map[string]interface{}{
					"kubeadmConfigSpec": map[string]interface{}{
						"clusterConfiguration": map[string]interface{}{
							"version": "v2.0.1",
							"abc":     "d",
						},
					},
				},
			},
			expected: [][]string{
				{"spec", "kubeadmConfigSpec", "clusterConfiguration", "version"},
				{"spec", "kubeadmConfigSpec", "clusterConfiguration", "abc"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(paths(tt.path, tt.diff)).To(ConsistOf(tt.expected))
		})
	}
}
