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

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	ignition "github.com/flatcar/ignition/config/v2_3"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	utilfeature "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	bootstrapbuilder "sigs.k8s.io/cluster-api/bootstrap/kubeadm/internal/builder"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	"sigs.k8s.io/cluster-api/util/test/builder"
	utilyaml "sigs.k8s.io/cluster-api/util/yaml"
)

// MachineToBootstrapMapFunc return kubeadm bootstrap configref name when configref exists.
func TestKubeadmConfigReconciler_MachineToBootstrapMapFuncReturn(t *testing.T) {
	g := NewWithT(t)
	cluster := builder.Cluster("my-cluster", metav1.NamespaceDefault).Build()
	objs := []client.Object{cluster}
	machineObjs := []client.Object{}
	var expectedConfigName string
	for i := range 3 {
		configName := fmt.Sprintf("my-config-%d", i)
		m := builder.Machine(metav1.NamespaceDefault, fmt.Sprintf("my-machine-%d", i)).
			WithVersion("v1.23.1").
			WithClusterName(cluster.Name).
			WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "").Unstructured()).
			Build()
		if i == 1 {
			c := newKubeadmConfig(metav1.NamespaceDefault, configName)
			addKubeadmConfigToMachine(c, m)
			objs = append(objs, m, c)
			expectedConfigName = configName
		} else {
			objs = append(objs, m)
		}
		machineObjs = append(machineObjs, m)
	}
	fakeClient := fake.NewClientBuilder().WithObjects(objs...).Build()
	reconciler := &KubeadmConfigReconciler{
		Client:              fakeClient,
		SecretCachingClient: fakeClient,
	}
	for i := range 3 {
		o := machineObjs[i]
		configs := reconciler.MachineToBootstrapMapFunc(ctx, o)
		if i == 1 {
			g.Expect(configs[0].Name).To(Equal(expectedConfigName))
		} else {
			g.Expect(configs[0].Name).To(Equal(""))
		}
	}
}

// Reconcile returns early if the kubeadm config is ready because it should never re-generate bootstrap data.
func TestKubeadmConfigReconciler_Reconcile_ReturnEarlyIfKubeadmConfigIsReady(t *testing.T) {
	g := NewWithT(t)
	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster1").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	machine := builder.Machine(metav1.NamespaceDefault, "m1").WithClusterName("cluster1").Build()
	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")
	addKubeadmConfigToMachine(config, machine)

	config.Status.Initialization.DataSecretCreated = ptr.To(true)

	objects := []client.Object{
		cluster,
		machine,
		config,
	}
	myclient := fake.NewClientBuilder().
		WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).
		WithObjects(objects...).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}
	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
}

// Reconcile should update owner references on bootstrap secrets on creation and update.
func TestKubeadmConfigReconciler_TestSecretOwnerReferenceReconciliation(t *testing.T) {
	g := NewWithT(t)

	clusterName := "my-cluster"
	cluster := builder.Cluster(metav1.NamespaceDefault, clusterName).Build()
	machine := builder.Machine(metav1.NamespaceDefault, "machine").
		WithVersion("v1.23.1").
		WithClusterName(clusterName).
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		Build()
	machine.Spec.Bootstrap.DataSecretName = ptr.To("something")

	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")
	config.SetOwnerReferences(util.EnsureOwnerRef(config.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Machine",
		Name:       machine.Name,
		UID:        machine.UID,
	}))
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.Name,
			Namespace: config.Namespace,
		},
		Type: corev1.SecretTypeBootstrapToken,
	}
	config.Status.Initialization.DataSecretCreated = ptr.To(true)

	objects := []client.Object{
		config,
		machine,
		secret,
		cluster,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}
	var err error
	key := client.ObjectKeyFromObject(config)
	actual := &corev1.Secret{}

	t.Run("KubeadmConfig ownerReference is added on first reconcile", func(*testing.T) {
		_, err = k.Reconcile(ctx, request)
		g.Expect(err).ToNot(HaveOccurred())

		g.Expect(myclient.Get(ctx, key, actual)).To(Succeed())

		controllerOwner := metav1.GetControllerOf(actual)
		g.Expect(controllerOwner).To(Not(BeNil()))
		g.Expect(controllerOwner.Kind).To(Equal(config.Kind))
		g.Expect(controllerOwner.Name).To(Equal(config.Name))
	})

	t.Run("KubeadmConfig ownerReference re-reconciled without error", func(*testing.T) {
		_, err = k.Reconcile(ctx, request)
		g.Expect(err).ToNot(HaveOccurred())

		g.Expect(myclient.Get(ctx, key, actual)).To(Succeed())

		controllerOwner := metav1.GetControllerOf(actual)
		g.Expect(controllerOwner).To(Not(BeNil()))
		g.Expect(controllerOwner.Kind).To(Equal(config.Kind))
		g.Expect(controllerOwner.Name).To(Equal(config.Name))
	})
	t.Run("non-KubeadmConfig controller OwnerReference is replaced", func(*testing.T) {
		g.Expect(myclient.Get(ctx, key, actual)).To(Succeed())

		actual.SetOwnerReferences([]metav1.OwnerReference{
			{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Machine",
				Name:       machine.Name,
				UID:        machine.UID,
				Controller: ptr.To(true),
			},
		})
		g.Expect(myclient.Update(ctx, actual)).To(Succeed())

		_, err = k.Reconcile(ctx, request)
		g.Expect(err).ToNot(HaveOccurred())

		g.Expect(myclient.Get(ctx, key, actual)).To(Succeed())

		controllerOwner := metav1.GetControllerOf(actual)
		g.Expect(controllerOwner).To(Not(BeNil()))
		g.Expect(controllerOwner.Kind).To(Equal(config.Kind))
		g.Expect(controllerOwner.Name).To(Equal(config.Name))
	})
}

// Reconcile returns nil if the referenced Machine cannot be found.
func TestKubeadmConfigReconciler_Reconcile_ReturnNilIfReferencedMachineIsNotFound(t *testing.T) {
	g := NewWithT(t)

	machine := builder.Machine(metav1.NamespaceDefault, "machine").
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		WithVersion("v1.23.1").
		Build()
	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")
	addKubeadmConfigToMachine(config, machine)
	objects := []client.Object{
		// intentionally omitting machine
		config,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}
	_, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
}

// If the machine has bootstrap data secret reference, there is no need to generate more bootstrap data.
func TestKubeadmConfigReconciler_Reconcile_ReturnEarlyIfMachineHasDataSecretName(t *testing.T) {
	g := NewWithT(t)
	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster1").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)

	machine := builder.Machine(metav1.NamespaceDefault, "machine").
		WithVersion("v1.23.1").
		WithClusterName("cluster1").
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		Build()
	machine.Spec.Bootstrap.DataSecretName = ptr.To("something")

	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")
	addKubeadmConfigToMachine(config, machine)
	objects := []client.Object{
		cluster,
		machine,
		config,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}
	result, err := k.Reconcile(ctx, request)
	actual := &bootstrapv1.KubeadmConfig{}
	g.Expect(myclient.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: config.Name}, actual)).To(Succeed())
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
	assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigDataSecretAvailableCondition)
}

func TestKubeadmConfigReconciler_ReturnEarlyIfClusterInfraNotReady(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	machine := builder.Machine(metav1.NamespaceDefault, "machine").
		WithVersion("v1.23.1").
		WithClusterName(cluster.Name).
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		Build()
	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")
	addKubeadmConfigToMachine(config, machine)

	// cluster infra not ready
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(false)

	objects := []client.Object{
		cluster,
		machine,
		config,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}

	expectedResult := reconcile.Result{}
	actualResult, actualError := k.Reconcile(ctx, request)
	g.Expect(actualResult).To(BeComparableTo(expectedResult))
	g.Expect(actualError).ToNot(HaveOccurred())
	assertHasFalseCondition(g, myclient, request, bootstrapv1.KubeadmConfigDataSecretAvailableCondition, bootstrapv1.KubeadmConfigDataSecretNotAvailableReason)
}

// Return early If the owning machine does not have an associated cluster.
func TestKubeadmConfigReconciler_Reconcile_ReturnEarlyIfMachineHasNoCluster(t *testing.T) {
	g := NewWithT(t)
	machine := builder.Machine(metav1.NamespaceDefault, "machine").
		WithVersion("v1.23.1").
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		Build()
	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")
	addKubeadmConfigToMachine(config, machine)

	objects := []client.Object{
		machine,
		config,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}
	_, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
}

// This does not expect an error, hoping that the associated cluster will be created.
func TestKubeadmConfigReconciler_Reconcile_ReturnNilIfAssociatedClusterIsNotFound(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	machine := builder.Machine(metav1.NamespaceDefault, "machine").
		WithVersion("v1.23.1").
		WithClusterName(cluster.Name).
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		Build()
	config := newKubeadmConfig(metav1.NamespaceDefault, "cfg")

	addKubeadmConfigToMachine(config, machine)
	objects := []client.Object{
		// intentionally omitting cluster
		machine,
		config,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "cfg",
		},
	}
	_, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
}

// If the control plane isn't initialized then there is no cluster for either a worker or control plane node to join.
func TestKubeadmConfigReconciler_Reconcile_RequeueJoiningNodesIfControlPlaneNotInitialized(t *testing.T) {
	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)

	workerMachine := newWorkerMachineForCluster(cluster)
	workerJoinConfig := newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, "worker-join-cfg")
	addKubeadmConfigToMachine(workerJoinConfig, workerMachine)

	controlPlaneJoinMachine := newControlPlaneMachine(cluster, "control-plane-join-machine")
	controlPlaneJoinConfig := newControlPlaneJoinKubeadmConfig(controlPlaneJoinMachine.Namespace, "control-plane-join-cfg")
	addKubeadmConfigToMachine(controlPlaneJoinConfig, controlPlaneJoinMachine)

	testcases := []struct {
		name    string
		request ctrl.Request
		objects []client.Object
		lock    bool
	}{
		{
			name: "requeue worker when control plane is not yet initialized",
			request: ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: workerJoinConfig.Namespace,
					Name:      workerJoinConfig.Name,
				},
			},
			objects: []client.Object{
				cluster,
				workerMachine,
				workerJoinConfig,
			},
		},
		{
			name: "requeue a secondary control plane when the control plane is not yet initialized",
			request: ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: controlPlaneJoinConfig.Namespace,
					Name:      controlPlaneJoinConfig.Name,
				},
			},
			objects: []client.Object{
				cluster,
				controlPlaneJoinMachine,
				controlPlaneJoinConfig,
			},
			lock: true,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			myclient := fake.NewClientBuilder().WithObjects(tc.objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				KubeadmInitLock:     &myInitLocker{},
			}

			if tc.lock {
				k.KubeadmInitLock.Lock(ctx, nil, nil)
			}

			result, err := k.Reconcile(ctx, tc.request)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(result.Requeue).To(BeFalse())
			g.Expect(result.RequeueAfter).To(Equal(30 * time.Second))
			assertHasFalseCondition(g, myclient, tc.request, bootstrapv1.KubeadmConfigDataSecretAvailableCondition, bootstrapv1.KubeadmConfigDataSecretNotAvailableReason)
		})
	}
}

// This generates cloud-config data but does not test the validity of it.
func TestKubeadmConfigReconciler_Reconcile_GenerateCloudConfigData(t *testing.T) {
	g := NewWithT(t)

	configName := "control-plane-init-cfg"
	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "validhost", Port: 6443}
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}

	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	controlPlaneInitConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, configName)
	controlPlaneInitConfig.Spec.JoinConfiguration = &bootstrapv1.JoinConfiguration{}
	controlPlaneInitConfig.Spec.JoinConfiguration.Discovery.BootstrapToken = &bootstrapv1.BootstrapTokenDiscovery{
		CACertHashes: []string{"...."},
	}

	addKubeadmConfigToMachine(controlPlaneInitConfig, controlPlaneInitMachine)

	objects := []client.Object{
		cluster,
		controlPlaneInitMachine,
		controlPlaneInitConfig,
	}
	objects = append(objects, createSecrets(t, cluster, controlPlaneInitConfig)...)

	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
		KubeadmInitLock:     &myInitLocker{},
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "control-plane-init-cfg",
		},
	}
	s := &corev1.Secret{}
	g.Expect(myclient.Get(ctx, client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: configName}, s)).ToNot(Succeed())

	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

	cfg, err := getKubeadmConfig(myclient, "control-plane-init-cfg", metav1.NamespaceDefault)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
	g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
	g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())
	assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigCertificatesAvailableCondition)
	assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigDataSecretAvailableCondition)

	// Expect the Secret to exist, and for it to contain some data under the "value" key.
	g.Expect(myclient.Get(ctx, client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: configName}, s)).To(Succeed())
	g.Expect(s.Data["value"]).ToNot(BeEmpty())
	// Ensure that we don't fail trying to refresh any bootstrap tokens
	_, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
}

// If a control plane has no JoinConfiguration, then we will create a default and no error will occur.
func TestKubeadmConfigReconciler_Reconcile_ErrorIfJoiningControlPlaneHasInvalidConfiguration(t *testing.T) {
	g := NewWithT(t)
	// TODO: extract this kind of code into a setup function that puts the state of objects into an initialized controlplane (implies secrets exist)
	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}
	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	controlPlaneInitConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-cfg")
	addKubeadmConfigToMachine(controlPlaneInitConfig, controlPlaneInitMachine)

	controlPlaneJoinMachine := newControlPlaneMachine(cluster, "control-plane-join-machine")
	controlPlaneJoinConfig := newControlPlaneJoinKubeadmConfig(controlPlaneJoinMachine.Namespace, "control-plane-join-cfg")
	controlPlaneJoinConfig.Spec.JoinConfiguration.ControlPlane = nil // Makes controlPlaneJoinConfig invalid for a control plane machine
	addKubeadmConfigToMachine(controlPlaneJoinConfig, controlPlaneJoinMachine)

	objects := []client.Object{
		cluster,
		controlPlaneJoinMachine,
		controlPlaneJoinConfig,
	}
	objects = append(objects, createSecrets(t, cluster, controlPlaneInitConfig)...)
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
		KubeadmInitLock:     &myInitLocker{},
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      controlPlaneJoinConfig.Name,
		},
	}
	_, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	actualConfig := &bootstrapv1.KubeadmConfig{}
	g.Expect(myclient.Get(ctx, client.ObjectKey{Namespace: controlPlaneJoinConfig.Namespace, Name: controlPlaneJoinConfig.Name}, actualConfig)).To(Succeed())
	assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigDataSecretAvailableCondition)
	assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigCertificatesAvailableCondition)
}

// If there is no APIEndpoint but everything is ready then requeue in hopes of a new APIEndpoint showing up eventually.
func TestKubeadmConfigReconciler_Reconcile_RequeueIfControlPlaneIsMissingAPIEndpoints(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	controlPlaneInitConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-cfg")
	addKubeadmConfigToMachine(controlPlaneInitConfig, controlPlaneInitMachine)

	workerMachine := newWorkerMachineForCluster(cluster)
	workerJoinConfig := newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, "worker-join-cfg")
	addKubeadmConfigToMachine(workerJoinConfig, workerMachine)

	objects := []client.Object{
		cluster,
		workerMachine,
		workerJoinConfig,
	}
	objects = append(objects, createSecrets(t, cluster, controlPlaneInitConfig)...)

	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		KubeadmInitLock:     &myInitLocker{},
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "worker-join-cfg",
		},
	}
	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(10 * time.Second))

	actualConfig := &bootstrapv1.KubeadmConfig{}
	g.Expect(myclient.Get(ctx, client.ObjectKey{Namespace: workerJoinConfig.Namespace, Name: workerJoinConfig.Name}, actualConfig)).To(Succeed())

	// At this point the DataSecretAvailableCondition should not be set. CertificatesAvailableCondition should be true.
	g.Expect(conditions.Get(actualConfig, bootstrapv1.KubeadmConfigDataSecretAvailableCondition)).To(BeNil())
	assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigCertificatesAvailableCondition)
}

func TestReconcileIfJoinCertificatesAvailableConditioninNodesAndControlPlaneIsReady(t *testing.T) {
	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

	useCases := []struct {
		name          string
		machine       *clusterv1.Machine
		configName    string
		configBuilder func(string, string) *bootstrapv1.KubeadmConfig
	}{
		{
			name:          "Join a worker node with a fully compiled kubeadm config object",
			machine:       newWorkerMachineForCluster(cluster),
			configName:    "worker-join-cfg",
			configBuilder: newWorkerJoinKubeadmConfig,
		},
		{
			name:          "Join a worker node  with an empty kubeadm config object (defaults apply)",
			machine:       newWorkerMachineForCluster(cluster),
			configName:    "worker-join-cfg",
			configBuilder: newKubeadmConfig,
		},
		{
			name:          "Join a control plane node with a fully compiled kubeadm config object",
			machine:       newControlPlaneMachine(cluster, "control-plane-join-machine"),
			configName:    "control-plane-join-cfg",
			configBuilder: newControlPlaneJoinKubeadmConfig,
		},
		{
			name:          "Join a control plane node with an empty kubeadm config object (defaults apply)",
			machine:       newControlPlaneMachine(cluster, "control-plane-join-machine"),
			configName:    "control-plane-join-cfg",
			configBuilder: newKubeadmConfig,
		},
	}

	for _, rt := range useCases {
		t.Run(rt.name, func(t *testing.T) {
			g := NewWithT(t)

			config := rt.configBuilder(rt.machine.Namespace, rt.configName)
			addKubeadmConfigToMachine(config, rt.machine)

			objects := []client.Object{
				cluster,
				rt.machine,
				config,
			}
			objects = append(objects, createSecrets(t, cluster, config)...)
			myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()
			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
				KubeadmInitLock:     &myInitLocker{},
			}

			request := ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: config.GetNamespace(),
					Name:      rt.configName,
				},
			}
			result, err := k.Reconcile(ctx, request)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(result.Requeue).To(BeFalse())
			g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			cfg, err := getKubeadmConfig(myclient, rt.configName, metav1.NamespaceDefault)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
			g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
			g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())
			assertHasTrueCondition(g, myclient, request, bootstrapv1.KubeadmConfigDataSecretAvailableCondition)

			l := &corev1.SecretList{}
			err = myclient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(l.Items).To(HaveLen(1))
		})
	}
}

func TestReconcileIfJoinNodePoolsAndControlPlaneIsReady(t *testing.T) {
	utilfeature.SetFeatureGateDuringTest(t, feature.Gates, feature.MachinePool, true)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

	useCases := []struct {
		name          string
		machinePool   *clusterv1.MachinePool
		configName    string
		configBuilder func(string, string) *bootstrapv1.KubeadmConfig
	}{
		{
			name:        "Join a worker node with a fully compiled kubeadm config object",
			machinePool: newWorkerMachinePoolForCluster(cluster),
			configName:  "workerpool-join-cfg",
			configBuilder: func(namespace, _ string) *bootstrapv1.KubeadmConfig {
				return newWorkerJoinKubeadmConfig(namespace, "workerpool-join-cfg")
			},
		},
		{
			name:          "Join a worker node  with an empty kubeadm config object (defaults apply)",
			machinePool:   newWorkerMachinePoolForCluster(cluster),
			configName:    "workerpool-join-cfg",
			configBuilder: newKubeadmConfig,
		},
	}

	for _, rt := range useCases {
		t.Run(rt.name, func(t *testing.T) {
			g := NewWithT(t)

			config := rt.configBuilder(rt.machinePool.Namespace, rt.configName)
			addKubeadmConfigToMachinePool(config, rt.machinePool)

			objects := []client.Object{
				cluster,
				rt.machinePool,
				config,
			}
			objects = append(objects, createSecrets(t, cluster, config)...)
			myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()
			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
				KubeadmInitLock:     &myInitLocker{},
			}

			request := ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: config.GetNamespace(),
					Name:      rt.configName,
				},
			}
			result, err := k.Reconcile(ctx, request)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(result.Requeue).To(BeFalse())
			g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			cfg, err := getKubeadmConfig(myclient, rt.configName, metav1.NamespaceDefault)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
			g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
			g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())

			l := &corev1.SecretList{}
			err = myclient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(l.Items).To(HaveLen(1))
		})
	}
}

// Ensure bootstrap data is generated in the correct format based on the format specified in the
// KubeadmConfig resource.
func TestBootstrapDataFormat(t *testing.T) {
	testcases := []struct {
		name               string
		isWorker           bool
		format             bootstrapv1.Format
		clusterInitialized bool
	}{
		{
			name:   "cloud-config init config",
			format: bootstrapv1.CloudConfig,
		},
		{
			name:   "Ignition init config",
			format: bootstrapv1.Ignition,
		},
		{
			name:               "Ignition control plane join config",
			format:             bootstrapv1.Ignition,
			clusterInitialized: true,
		},
		{
			name:               "Ignition worker join config",
			isWorker:           true,
			format:             bootstrapv1.Ignition,
			clusterInitialized: true,
		},
		{
			name:   "Empty format field",
			format: bootstrapv1.CloudConfig,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
			cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
			cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}
			if tc.clusterInitialized {
				cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
			}

			var machine *clusterv1.Machine
			var config *bootstrapv1.KubeadmConfig
			var configName string
			if tc.isWorker {
				machine = newWorkerMachineForCluster(cluster)
				configName = "worker-join-cfg"
				config = newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, configName)
				addKubeadmConfigToMachine(config, machine)
			} else {
				machine = newControlPlaneMachine(cluster, "machine")
				configName = "cfg"
				config = newControlPlaneInitKubeadmConfig(metav1.NamespaceDefault, configName)
				addKubeadmConfigToMachine(config, machine)
			}
			config.Spec.Format = tc.format

			objects := []client.Object{
				cluster,
				machine,
				config,
			}
			objects = append(objects, createSecrets(t, cluster, config)...)

			myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
				KubeadmInitLock:     &myInitLocker{},
			}
			request := ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      configName,
				},
			}

			// Reconcile the KubeadmConfig resource.
			_, err := k.Reconcile(ctx, request)
			g.Expect(err).ToNot(HaveOccurred())

			// Verify the KubeadmConfig resource state is correct.
			cfg, err := getKubeadmConfig(myclient, configName, metav1.NamespaceDefault)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
			g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())

			// Read the secret containing the bootstrap data which was generated by the
			// KubeadmConfig controller.
			key := client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      cfg.Status.DataSecretName,
			}
			secret := &corev1.Secret{}
			err = myclient.Get(ctx, key, secret)
			g.Expect(err).ToNot(HaveOccurred())

			// Verify the format field of the bootstrap data secret is correct.
			g.Expect(string(secret.Data["format"])).To(Equal(string(tc.format)))

			// Verify the bootstrap data value is in the correct format.
			data := secret.Data["value"]
			switch tc.format {
			case bootstrapv1.CloudConfig, "":
				// Verify the bootstrap data is valid YAML.
				// TODO: Verify the YAML document is valid cloud-config?
				var out interface{}
				err = yaml.Unmarshal(data, &out)
				g.Expect(err).ToNot(HaveOccurred())
			case bootstrapv1.Ignition:
				// Verify the bootstrap data is valid Ignition.
				_, reports, err := ignition.Parse(data)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(reports.IsFatal()).NotTo(BeTrue())
			}
		})
	}
}

// during kubeadmconfig reconcile it is possible that bootstrap secret gets created
// but kubeadmconfig is not patched, do not error if secret already exists.
// ignore the alreadyexists error and update the status to ready.
func TestKubeadmConfigSecretCreatedStatusNotPatched(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	initConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-config")
	addKubeadmConfigToMachine(initConfig, controlPlaneInitMachine)

	workerMachine := newWorkerMachineForCluster(cluster)
	workerJoinConfig := newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, "worker-join-cfg")
	addKubeadmConfigToMachine(workerJoinConfig, workerMachine)
	objects := []client.Object{
		cluster,
		workerMachine,
		workerJoinConfig,
	}

	objects = append(objects, createSecrets(t, cluster, initConfig)...)
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()
	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
		KubeadmInitLock:     &myInitLocker{},
	}
	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "worker-join-cfg",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workerJoinConfig.Name,
			Namespace: workerJoinConfig.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: bootstrapv1.GroupVersion.String(),
					Kind:       "KubeadmConfig",
					Name:       workerJoinConfig.Name,
					UID:        workerJoinConfig.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Data: map[string][]byte{
			"value": nil,
		},
		Type: clusterv1.ClusterSecretType,
	}

	err := myclient.Create(ctx, secret)
	g.Expect(err).ToNot(HaveOccurred())
	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

	cfg, err := getKubeadmConfig(myclient, "worker-join-cfg", metav1.NamespaceDefault)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
	g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
	g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())
}

func TestBootstrapTokenTTLExtension(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	initConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-config")
	addKubeadmConfigToMachine(initConfig, controlPlaneInitMachine)

	workerMachine := newWorkerMachineForCluster(cluster)
	workerJoinConfig := newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, "worker-join-cfg")
	addKubeadmConfigToMachine(workerJoinConfig, workerMachine)

	controlPlaneJoinMachine := newControlPlaneMachine(cluster, "control-plane-join-machine")
	controlPlaneJoinConfig := newControlPlaneJoinKubeadmConfig(controlPlaneJoinMachine.Namespace, "control-plane-join-cfg")
	addKubeadmConfigToMachine(controlPlaneJoinConfig, controlPlaneJoinMachine)
	objects := []client.Object{
		cluster,
		workerMachine,
		workerJoinConfig,
		controlPlaneJoinMachine,
		controlPlaneJoinConfig,
	}

	objects = append(objects, createSecrets(t, cluster, initConfig)...)
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}, &clusterv1.Machine{}).Build()
	remoteClient := fake.NewClientBuilder().Build()
	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		KubeadmInitLock:     &myInitLocker{},
		TokenTTL:            DefaultTokenTTL,
		ClusterCache:        clustercache.NewFakeClusterCache(remoteClient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
	}
	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "worker-join-cfg",
		},
	}
	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	cfg, err := getKubeadmConfig(myclient, "worker-join-cfg", metav1.NamespaceDefault)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
	g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
	g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())

	request = ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "control-plane-join-cfg",
		},
	}
	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	cfg, err = getKubeadmConfig(myclient, "control-plane-join-cfg", metav1.NamespaceDefault)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
	g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
	g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())

	l := &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(2)) // control plane vs. worker

	t.Log("Ensure that the token secret is not updated while it's still fresh")
	tokenExpires := make([][]byte, len(l.Items))

	for i, item := range l.Items {
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	for _, req := range []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "worker-join-cfg",
			},
		},
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "control-plane-join-cfg",
			},
		},
	} {
		result, err := k.Reconcile(ctx, req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))
	}

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(2))

	for i, item := range l.Items {
		// No refresh should have happened since no time passed and the token is therefore still fresh
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeTrue())
	}

	t.Log("Ensure that the token secret is updated if expiration time is soon")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL/2 from now. This should trigger a refresh.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL / 2).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	for _, req := range []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "worker-join-cfg",
			},
		},
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "control-plane-join-cfg",
			},
		},
	} {
		result, err := k.Reconcile(ctx, req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))
	}

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(2))

	for i, item := range l.Items {
		// Refresh should have happened since expiration is soon
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeFalse())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	t.Log("If infrastructure is marked ready, the token should still be refreshed")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL/2 from now. This should trigger a refresh.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL / 2).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	patchHelper, err := patch.NewHelper(workerMachine, myclient)
	g.Expect(err).ShouldNot(HaveOccurred())
	workerMachine.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	g.Expect(patchHelper.Patch(ctx, workerMachine)).To(Succeed())

	patchHelper, err = patch.NewHelper(controlPlaneJoinMachine, myclient)
	g.Expect(err).ShouldNot(HaveOccurred())
	controlPlaneJoinMachine.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	g.Expect(patchHelper.Patch(ctx, controlPlaneJoinMachine)).To(Succeed())

	for _, req := range []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "worker-join-cfg",
			},
		},
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "control-plane-join-cfg",
			},
		},
	} {
		result, err := k.Reconcile(ctx, req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))
	}

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(2))

	for i, item := range l.Items {
		// Refresh should have happened since expiration is soon, even if infrastructure is ready
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeFalse())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	t.Log("When the Nodes have actually joined the cluster and we get a nodeRef, no more refresh should happen")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL/2 from now. This would normally trigger a refresh.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL / 2).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	patchHelper, err = patch.NewHelper(workerMachine, myclient)
	g.Expect(err).ShouldNot(HaveOccurred())
	workerMachine.Status.NodeRef = &clusterv1.MachineNodeReference{
		Name: "worker-node",
	}
	g.Expect(patchHelper.Patch(ctx, workerMachine)).To(Succeed())

	patchHelper, err = patch.NewHelper(controlPlaneJoinMachine, myclient)
	g.Expect(err).ShouldNot(HaveOccurred())
	controlPlaneJoinMachine.Status.NodeRef = &clusterv1.MachineNodeReference{
		Name: "control-plane-node",
	}
	g.Expect(patchHelper.Patch(ctx, controlPlaneJoinMachine)).To(Succeed())

	for _, req := range []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "worker-join-cfg",
			},
		},
		{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "control-plane-join-cfg",
			},
		},
	} {
		result, err := k.Reconcile(ctx, req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.Requeue).To(BeFalse())
		g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
	}

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(2))

	for i, item := range l.Items {
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeTrue())
	}
}

func TestBootstrapTokenRotationMachinePool(t *testing.T) {
	utilfeature.SetFeatureGateDuringTest(t, feature.Gates, feature.MachinePool, true)
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	initConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-config")

	addKubeadmConfigToMachine(initConfig, controlPlaneInitMachine)

	workerMachinePool := newWorkerMachinePoolForCluster(cluster)
	workerJoinConfig := newWorkerJoinKubeadmConfig(workerMachinePool.Namespace, "workerpool-join-cfg")
	addKubeadmConfigToMachinePool(workerJoinConfig, workerMachinePool)
	objects := []client.Object{
		cluster,
		workerMachinePool,
		workerJoinConfig,
	}

	objects = append(objects, createSecrets(t, cluster, initConfig)...)
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}, &clusterv1.MachinePool{}).Build()
	remoteClient := fake.NewClientBuilder().Build()
	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		KubeadmInitLock:     &myInitLocker{},
		TokenTTL:            DefaultTokenTTL,
		ClusterCache:        clustercache.NewFakeClusterCache(remoteClient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
	}
	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "workerpool-join-cfg",
		},
	}
	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	cfg, err := getKubeadmConfig(myclient, "workerpool-join-cfg", metav1.NamespaceDefault)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
	g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
	g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())

	l := &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(1))

	t.Log("Ensure that the token secret is not updated while it's still fresh")
	tokenExpires := make([][]byte, len(l.Items))

	for i, item := range l.Items {
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(1))

	for i, item := range l.Items {
		// No refresh should have happened since no time passed and the token is therefore still fresh
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeTrue())
	}

	t.Log("Ensure that the token secret is updated if expiration time is soon")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL*3/4 from now. This should trigger a refresh.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL * 3 / 4).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(1))

	for i, item := range l.Items {
		// Refresh should have happened since expiration is soon
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeFalse())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	t.Log("If infrastructure is marked ready, the token should still be refreshed")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL*3/4 from now. This should trigger a refresh.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL * 3 / 4).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	patchHelper, err := patch.NewHelper(workerMachinePool, myclient)
	g.Expect(err).ShouldNot(HaveOccurred())
	workerMachinePool.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	g.Expect(patchHelper.Patch(ctx, workerMachinePool, patch.WithStatusObservedGeneration{})).To(Succeed())

	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(1))

	for i, item := range l.Items {
		// Refresh should have happened since expiration is soon, even if infrastructure is ready
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeFalse())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	t.Log("When the Nodes have actually joined the cluster and we get a nodeRef, no more refresh should happen")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL*3/4 from now. This would normally trigger a refresh.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL * 3 / 4).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	workerMachinePool.Status.NodeRefs = []corev1.ObjectReference{
		{
			Kind:      "Node",
			Namespace: metav1.NamespaceDefault,
			Name:      "node-0",
		},
	}
	g.Expect(patchHelper.Patch(ctx, workerMachinePool, patch.WithStatusObservedGeneration{})).To(Succeed())

	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(1))

	for i, item := range l.Items {
		g.Expect(bytes.Equal(tokenExpires[i], item.Data[bootstrapapi.BootstrapTokenExpirationKey])).To(BeTrue())
	}

	t.Log("Token must be rotated before it expires")

	for i, item := range l.Items {
		// Simulate that expiry time is only TTL*4/10 from now. This should trigger rotation.
		item.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().UTC().Add(k.TokenTTL * 4 / 10).Format(time.RFC3339))
		g.Expect(remoteClient.Update(ctx, &l.Items[i])).To(Succeed())
		tokenExpires[i] = item.Data[bootstrapapi.BootstrapTokenExpirationKey]
	}

	request = ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "workerpool-join-cfg",
		},
	}
	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

	l = &corev1.SecretList{}
	g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
	g.Expect(l.Items).To(HaveLen(2)) // old and new token
	foundOld := false
	foundNew := true
	for _, item := range l.Items {
		if bytes.Equal(item.Data[bootstrapapi.BootstrapTokenExpirationKey], tokenExpires[0]) {
			foundOld = true
		} else {
			expirationTime, err := time.Parse(time.RFC3339, string(item.Data[bootstrapapi.BootstrapTokenExpirationKey]))
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(expirationTime).Should(BeTemporally("~", time.Now().UTC().Add(k.TokenTTL), 10*time.Second))
			foundNew = true
		}
	}
	g.Expect(foundOld).To(BeTrue())
	g.Expect(foundNew).To(BeTrue())
}

func TestBootstrapTokenRefreshIfTokenSecretCleaned(t *testing.T) {
	t.Run("should not recreate the token for Machines", func(t *testing.T) {
		g := NewWithT(t)

		cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
		cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
		cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
		cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

		controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
		initConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-config")

		addKubeadmConfigToMachine(initConfig, controlPlaneInitMachine)

		workerMachine := newWorkerMachineForCluster(cluster)
		workerJoinConfig := newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, "worker-join-cfg")
		addKubeadmConfigToMachine(workerJoinConfig, workerMachine)
		objects := []client.Object{
			cluster,
			workerMachine,
			workerJoinConfig,
		}

		objects = append(objects, createSecrets(t, cluster, initConfig)...)
		myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()
		remoteClient := fake.NewClientBuilder().Build()
		k := &KubeadmConfigReconciler{
			Client:              myclient,
			SecretCachingClient: myclient,
			KubeadmInitLock:     &myInitLocker{},
			TokenTTL:            DefaultTokenTTL,
			ClusterCache:        clustercache.NewFakeClusterCache(remoteClient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
		}
		request := ctrl.Request{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "worker-join-cfg",
			},
		}
		result, err := k.Reconcile(ctx, request)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

		cfg, err := getKubeadmConfig(myclient, "worker-join-cfg", metav1.NamespaceDefault)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
		g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
		g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())
		g.Expect(cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token).ToNot(BeEmpty())
		firstToken := cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token

		l := &corev1.SecretList{}
		g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
		g.Expect(l.Items).To(HaveLen(1))

		t.Log("Token should not get recreated for single Machine since it will not use the new token if spec.bootstrap.dataSecretName was already set")

		// Simulate token cleaner of Kubernetes having deleted the token secret
		err = remoteClient.Delete(ctx, &l.Items[0])
		g.Expect(err).ToNot(HaveOccurred())

		result, err = k.Reconcile(ctx, request)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("failed to get bootstrap token secret in order to refresh it"))
		// New token should not have been created
		cfg, err = getKubeadmConfig(myclient, "worker-join-cfg", metav1.NamespaceDefault)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token).To(Equal(firstToken))

		l = &corev1.SecretList{}
		g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
		g.Expect(l.Items).To(BeEmpty())
	})
	t.Run("should recreate the token for MachinePools", func(t *testing.T) {
		utilfeature.SetFeatureGateDuringTest(t, feature.Gates, feature.MachinePool, true)
		g := NewWithT(t)

		cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
		cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
		cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
		cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "100.105.150.1", Port: 6443}

		controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
		initConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-config")

		addKubeadmConfigToMachine(initConfig, controlPlaneInitMachine)

		workerMachinePool := newWorkerMachinePoolForCluster(cluster)
		workerJoinConfig := newWorkerJoinKubeadmConfig(workerMachinePool.Namespace, "workerpool-join-cfg")
		addKubeadmConfigToMachinePool(workerJoinConfig, workerMachinePool)
		objects := []client.Object{
			cluster,
			workerMachinePool,
			workerJoinConfig,
		}

		objects = append(objects, createSecrets(t, cluster, initConfig)...)
		myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}, &clusterv1.MachinePool{}).Build()
		remoteClient := fake.NewClientBuilder().Build()
		k := &KubeadmConfigReconciler{
			Client:              myclient,
			SecretCachingClient: myclient,
			KubeadmInitLock:     &myInitLocker{},
			TokenTTL:            DefaultTokenTTL,
			ClusterCache:        clustercache.NewFakeClusterCache(remoteClient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
		}
		request := ctrl.Request{
			NamespacedName: client.ObjectKey{
				Namespace: metav1.NamespaceDefault,
				Name:      "workerpool-join-cfg",
			},
		}
		result, err := k.Reconcile(ctx, request)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))

		cfg, err := getKubeadmConfig(myclient, "workerpool-join-cfg", metav1.NamespaceDefault)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false)).To(BeTrue())
		g.Expect(cfg.Status.DataSecretName).NotTo(BeEmpty())
		g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())
		g.Expect(cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token).ToNot(BeEmpty())
		firstToken := cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token

		l := &corev1.SecretList{}
		g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
		g.Expect(l.Items).To(HaveLen(1))

		t.Log("Ensure that the token gets recreated if it was cleaned up by Kubernetes (e.g. on expiry)")

		// Simulate token cleaner of Kubernetes having deleted the token secret
		err = remoteClient.Delete(ctx, &l.Items[0])
		g.Expect(err).ToNot(HaveOccurred())

		result, err = k.Reconcile(ctx, request)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result.RequeueAfter).To(Equal(k.TokenTTL / 3))
		// New token should have been created
		cfg, err = getKubeadmConfig(myclient, "workerpool-join-cfg", metav1.NamespaceDefault)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token).ToNot(BeEmpty())
		g.Expect(cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.Token).ToNot(Equal(firstToken))

		l = &corev1.SecretList{}
		g.Expect(remoteClient.List(ctx, l, client.ListOption(client.InNamespace(metav1.NamespaceSystem)))).To(Succeed())
		g.Expect(l.Items).To(HaveLen(1))
	})
}

// Ensure the discovery portion of the JoinConfiguration gets generated correctly.
func TestKubeadmConfigReconciler_Reconcile_DiscoveryReconcileBehaviors(t *testing.T) {
	caHash := []string{"...."}
	bootstrapToken := bootstrapv1.Discovery{
		BootstrapToken: &bootstrapv1.BootstrapTokenDiscovery{
			CACertHashes: caHash,
		},
	}
	goodcluster := &clusterv1.Cluster{
		Spec: clusterv1.ClusterSpec{
			ControlPlaneEndpoint: clusterv1.APIEndpoint{
				Host: "example.com",
				Port: 6443,
			},
		},
	}
	testcases := []struct {
		name              string
		cluster           *clusterv1.Cluster
		config            *bootstrapv1.KubeadmConfig
		validateDiscovery func(*WithT, *bootstrapv1.KubeadmConfig) error
	}{
		{
			name:    "Automatically generate token if discovery not specified",
			cluster: goodcluster,
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapToken,
					},
				},
			},
			validateDiscovery: func(g *WithT, c *bootstrapv1.KubeadmConfig) error {
				d := c.Spec.JoinConfiguration.Discovery
				g.Expect(d.BootstrapToken).NotTo(BeNil())
				g.Expect(d.BootstrapToken.Token).NotTo(Equal(""))
				g.Expect(d.BootstrapToken.APIServerEndpoint).To(Equal("example.com:6443"))
				g.Expect(d.BootstrapToken.UnsafeSkipCAVerification).To(BeNil())
				return nil
			},
		},
		{
			name:    "Respect discoveryConfiguration.File",
			cluster: goodcluster,
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							File: &bootstrapv1.FileDiscovery{},
						},
					},
				},
			},
			validateDiscovery: func(g *WithT, c *bootstrapv1.KubeadmConfig) error {
				d := c.Spec.JoinConfiguration.Discovery
				g.Expect(d.BootstrapToken).To(BeNil())
				return nil
			},
		},
		{
			name:    "Respect discoveryConfiguration.File.KubeConfig",
			cluster: goodcluster,
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							File: &bootstrapv1.FileDiscovery{
								KubeConfigPath: "/bootstrap-kubeconfig.yaml",
								KubeConfig: &bootstrapv1.FileDiscoveryKubeConfig{
									User: bootstrapv1.KubeConfigUser{
										Exec: &bootstrapv1.KubeConfigAuthExec{
											Command: "/bootstrap",
										},
									},
								},
							},
						},
					},
				},
			},
			validateDiscovery: func(g *WithT, c *bootstrapv1.KubeadmConfig) error {
				d := c.Spec.JoinConfiguration.Discovery
				g.Expect(d.BootstrapToken).To(BeNil())
				g.Expect(d.File.KubeConfig.User.Exec.Command).To(Equal("/bootstrap"))
				g.Expect(d.File.KubeConfig.Cluster).ToNot(BeNil())
				g.Expect(d.File.KubeConfig.Cluster.Server).To(Equal("https://example.com:6443"))
				g.Expect(d.File.KubeConfig.Cluster.CertificateAuthorityData).To(BeEquivalentTo("ca-data"))
				return nil
			},
		},
		{
			name:    "Respect discoveryConfiguration.BootstrapToken.APIServerEndpoint",
			cluster: goodcluster,
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							BootstrapToken: &bootstrapv1.BootstrapTokenDiscovery{
								CACertHashes:      caHash,
								APIServerEndpoint: "bar.com:6443",
							},
						},
					},
				},
			},
			validateDiscovery: func(g *WithT, c *bootstrapv1.KubeadmConfig) error {
				d := c.Spec.JoinConfiguration.Discovery
				g.Expect(d.BootstrapToken.APIServerEndpoint).To(Equal("bar.com:6443"))
				return nil
			},
		},
		{
			name:    "Respect discoveryConfiguration.BootstrapToken.Token",
			cluster: goodcluster,
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							BootstrapToken: &bootstrapv1.BootstrapTokenDiscovery{
								CACertHashes: caHash,
								Token:        "abcdef.0123456789abcdef",
							},
						},
					},
				},
			},
			validateDiscovery: func(g *WithT, c *bootstrapv1.KubeadmConfig) error {
				d := c.Spec.JoinConfiguration.Discovery
				g.Expect(d.BootstrapToken.Token).To(Equal("abcdef.0123456789abcdef"))
				return nil
			},
		},
		{
			name:    "Respect discoveryConfiguration.BootstrapToken.CACertHashes",
			cluster: goodcluster,
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							BootstrapToken: &bootstrapv1.BootstrapTokenDiscovery{
								CACertHashes: caHash,
							},
						},
					},
				},
			},
			validateDiscovery: func(g *WithT, c *bootstrapv1.KubeadmConfig) error {
				d := c.Spec.JoinConfiguration.Discovery
				g.Expect(d.BootstrapToken.CACertHashes).To(BeComparableTo(caHash))
				return nil
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			fakeClient := fake.NewClientBuilder().Build()
			k := &KubeadmConfigReconciler{
				Client:              fakeClient,
				SecretCachingClient: fakeClient,
				ClusterCache:        clustercache.NewFakeClusterCache(fakeClient, client.ObjectKey{Name: tc.cluster.Name, Namespace: tc.cluster.Namespace}),
				KubeadmInitLock:     &myInitLocker{},
			}

			res, err := k.reconcileDiscovery(ctx, tc.cluster, tc.config, secret.Certificates{
				&secret.Certificate{
					Purpose: secret.ClusterCA,
					KeyPair: &certs.KeyPair{
						Cert: []byte("ca-data"),
						Key:  []byte("ca-key"),
					},
				},
			})
			g.Expect(res.IsZero()).To(BeTrue())
			g.Expect(err).ToNot(HaveOccurred())

			err = tc.validateDiscovery(g, tc.config)
			g.Expect(err).ToNot(HaveOccurred())
		})
	}
}

// Test failure cases for the discovery reconcile function.
func TestKubeadmConfigReconciler_Reconcile_DiscoveryReconcileFailureBehaviors(t *testing.T) {
	k := &KubeadmConfigReconciler{}

	testcases := []struct {
		name    string
		cluster *clusterv1.Cluster
		config  *bootstrapv1.KubeadmConfig

		result ctrl.Result
		err    error
	}{
		{
			name:    "Should requeue if cluster has not ControlPlaneEndpoint",
			cluster: &clusterv1.Cluster{}, // cluster without endpoints
			config: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							BootstrapToken: &bootstrapv1.BootstrapTokenDiscovery{
								CACertHashes: []string{"item"},
							},
						},
					},
				},
			},
			result: ctrl.Result{RequeueAfter: 10 * time.Second},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			res, err := k.reconcileDiscovery(ctx, tc.cluster, tc.config, secret.Certificates{})
			g.Expect(res).To(BeComparableTo(tc.result))
			if tc.err == nil {
				g.Expect(err).ToNot(HaveOccurred())
			} else {
				g.Expect(err).To(Equal(tc.err))
			}
		})
	}
}

// Set cluster configuration defaults based on dynamic values from the cluster object.
func TestKubeadmConfigReconciler_computeClusterConfigurationAndAdditionalData(t *testing.T) {
	k := &KubeadmConfigReconciler{}

	testcases := []struct {
		name              string
		cluster           *clusterv1.Cluster
		machine           *clusterv1.Machine
		config            *bootstrapv1.KubeadmConfig
		initConfiguration *bootstrapv1.InitConfiguration
	}{
		{
			name: "Propagate fields from Cluster & Machine & initConfiguration",
			cluster: &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mycluster",
				},
				Spec: clusterv1.ClusterSpec{
					ClusterNetwork: clusterv1.ClusterNetwork{
						Services:      clusterv1.NetworkRanges{CIDRBlocks: []string{"myServiceSubnet"}},
						Pods:          clusterv1.NetworkRanges{CIDRBlocks: []string{"myPodSubnet"}},
						ServiceDomain: "myDNSDomain",
					},
					ControlPlaneEndpoint: clusterv1.APIEndpoint{Host: "myControlPlaneEndpoint", Port: 6443},
				},
			},
			machine: &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: "v1.23.0",
				},
			},
			initConfiguration: &bootstrapv1.InitConfiguration{
				Timeouts: &bootstrapv1.Timeouts{
					ControlPlaneComponentHealthCheckSeconds: ptr.To[int32](10),
				},
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			clusterConfiguration := &bootstrapv1.ClusterConfiguration{}
			gotData := k.computeClusterConfigurationAndAdditionalData(tc.cluster, tc.machine, clusterConfiguration, tc.initConfiguration)
			g.Expect(clusterConfiguration.ControlPlaneEndpoint).To(Equal("myControlPlaneEndpoint:6443"))
			g.Expect(gotData.KubernetesVersion).To(Equal(ptr.To("v1.23.0")))
			g.Expect(gotData.ClusterName).To(Equal(ptr.To("mycluster")))
			g.Expect(gotData.PodSubnet).To(Equal(ptr.To("myPodSubnet")))
			g.Expect(gotData.ServiceSubnet).To(Equal(ptr.To("myServiceSubnet")))
			g.Expect(gotData.DNSDomain).To(Equal(ptr.To("myDNSDomain")))
			g.Expect(gotData.ControlPlaneComponentHealthCheckSeconds).To(Equal(ptr.To[int32](10)))
		})
	}
}

// Allow users to skip CA Verification if they *really* want to.
func TestKubeadmConfigReconciler_Reconcile_AlwaysCheckCAVerificationUnlessRequestedToSkip(t *testing.T) {
	// Setup work for an initialized cluster
	clusterName := "my-cluster"
	cluster := builder.Cluster(metav1.NamespaceDefault, clusterName).Build()
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{
		Host: "example.com",
		Port: 6443,
	}
	controlPlaneInitMachine := newControlPlaneMachine(cluster, "my-control-plane-init-machine")
	initConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "my-control-plane-init-config")

	controlPlaneMachineName := "my-machine"
	machine := builder.Machine(metav1.NamespaceDefault, controlPlaneMachineName).
		WithVersion("v1.23.1").
		WithClusterName(cluster.Name).
		Build()

	workerMachineName := "my-worker"
	workerMachine := builder.Machine(metav1.NamespaceDefault, workerMachineName).
		WithVersion("v1.23.1").
		WithClusterName(cluster.Name).
		Build()

	controlPlaneConfigName := "my-config"
	config := newKubeadmConfig(metav1.NamespaceDefault, controlPlaneConfigName)

	objects := []client.Object{
		cluster, machine, workerMachine, config,
	}
	objects = append(objects, createSecrets(t, cluster, initConfig)...)

	testcases := []struct {
		name               string
		discovery          *bootstrapv1.BootstrapTokenDiscovery
		skipCAVerification *bool
	}{
		{
			name:               "Do not skip CA verification by default",
			discovery:          &bootstrapv1.BootstrapTokenDiscovery{},
			skipCAVerification: nil,
		},
		{
			name: "Skip CA verification if requested by the user",
			discovery: &bootstrapv1.BootstrapTokenDiscovery{
				UnsafeSkipCAVerification: ptr.To(true),
			},
			skipCAVerification: ptr.To(true),
		},
		{
			// skipCAVerification should be true since no Cert Hashes are provided, but reconcile will *always* get or create certs.
			// TODO: Certificate get/create behavior needs to be mocked to enable this test.
			name: "cannot test for defaulting behavior through the reconcile function",
			discovery: &bootstrapv1.BootstrapTokenDiscovery{
				CACertHashes: []string{""},
			},
			skipCAVerification: nil,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			myclient := fake.NewClientBuilder().WithObjects(objects...).Build()
			reconciler := KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
				KubeadmInitLock:     &myInitLocker{},
			}

			wc := newWorkerJoinKubeadmConfig(metav1.NamespaceDefault, "worker-join-cfg")
			wc.Spec.JoinConfiguration = &bootstrapv1.JoinConfiguration{}
			wc.Spec.JoinConfiguration.Discovery.BootstrapToken = tc.discovery
			key := client.ObjectKey{Namespace: wc.Namespace, Name: wc.Name}
			err := myclient.Create(ctx, wc)
			g.Expect(err).ToNot(HaveOccurred())

			req := ctrl.Request{NamespacedName: key}
			_, err = reconciler.Reconcile(ctx, req)
			g.Expect(err).ToNot(HaveOccurred())

			cfg := &bootstrapv1.KubeadmConfig{}
			err = myclient.Get(ctx, key, cfg)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(cfg.Spec.JoinConfiguration.Discovery.BootstrapToken.UnsafeSkipCAVerification).To(Equal(tc.skipCAVerification))
		})
	}
}

// If a cluster object changes then all associated KubeadmConfigs should be re-reconciled.
// This allows us to not requeue a kubeadm config while we wait for InfrastructureReady.
func TestKubeadmConfigReconciler_ClusterToKubeadmConfigs(t *testing.T) {
	utilfeature.SetFeatureGateDuringTest(t, feature.Gates, feature.MachinePool, true)
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "my-cluster").Build()
	objs := []client.Object{cluster}
	expectedNames := []string{}
	for i := range 3 {
		configName := fmt.Sprintf("my-config-%d", i)
		m := builder.Machine(metav1.NamespaceDefault, fmt.Sprintf("my-machine-%d", i)).
			WithVersion("v1.23.1").
			WithClusterName(cluster.Name).
			WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, configName).Unstructured()).
			Build()
		c := newKubeadmConfig(metav1.NamespaceDefault, configName)
		addKubeadmConfigToMachine(c, m)
		expectedNames = append(expectedNames, configName)
		objs = append(objs, m, c)
	}
	for i := 3; i < 6; i++ {
		mp := newMachinePool(cluster, fmt.Sprintf("my-machinepool-%d", i))
		configName := fmt.Sprintf("my-config-%d", i)
		c := newKubeadmConfig(mp.Namespace, configName)
		addKubeadmConfigToMachinePool(c, mp)
		expectedNames = append(expectedNames, configName)
		objs = append(objs, mp, c)
	}
	fakeClient := fake.NewClientBuilder().WithObjects(objs...).Build()
	reconciler := &KubeadmConfigReconciler{
		Client:              fakeClient,
		SecretCachingClient: fakeClient,
	}
	configs := reconciler.ClusterToKubeadmConfigs(ctx, cluster)
	names := make([]string, 6)
	for i := range configs {
		names[i] = configs[i].Name
	}
	for _, name := range expectedNames {
		found := false
		for _, foundName := range names {
			if foundName == name {
				found = true
			}
		}
		g.Expect(found).To(BeTrue())
	}
}

// Reconcile should not fail if the Etcd CA Secret already exists.
func TestKubeadmConfigReconciler_Reconcile_DoesNotFailIfCASecretsAlreadyExist(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "my-cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	m := newControlPlaneMachine(cluster, "control-plane-machine")
	configName := "my-config"
	c := newControlPlaneInitKubeadmConfig(m.Namespace, configName)
	scrt := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", cluster.Name, secret.EtcdCA),
			Namespace: metav1.NamespaceDefault,
		},
		Data: map[string][]byte{
			"tls.crt": []byte("hello world"),
			"tls.key": []byte("hello world"),
		},
	}
	fakec := fake.NewClientBuilder().WithObjects(cluster, m, c, scrt).Build()
	reconciler := &KubeadmConfigReconciler{
		Client:              fakec,
		SecretCachingClient: fakec,
		KubeadmInitLock:     &myInitLocker{},
	}
	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: configName},
	}
	_, err := reconciler.Reconcile(ctx, req)
	g.Expect(err).ToNot(HaveOccurred())
}

// Exactly one control plane machine initializes if there are multiple control plane machines defined.
func TestKubeadmConfigReconciler_Reconcile_ExactlyOneControlPlaneMachineInitializes(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)

	controlPlaneInitMachineFirst := newControlPlaneMachine(cluster, "control-plane-init-machine-first")
	controlPlaneInitConfigFirst := newControlPlaneInitKubeadmConfig(controlPlaneInitMachineFirst.Namespace, "control-plane-init-cfg-first")
	addKubeadmConfigToMachine(controlPlaneInitConfigFirst, controlPlaneInitMachineFirst)

	controlPlaneInitMachineSecond := newControlPlaneMachine(cluster, "control-plane-init-machine-second")
	controlPlaneInitConfigSecond := newControlPlaneInitKubeadmConfig(controlPlaneInitMachineSecond.Namespace, "control-plane-init-cfg-second")
	addKubeadmConfigToMachine(controlPlaneInitConfigSecond, controlPlaneInitMachineSecond)

	objects := []client.Object{
		cluster,
		controlPlaneInitMachineFirst,
		controlPlaneInitConfigFirst,
		controlPlaneInitMachineSecond,
		controlPlaneInitConfigSecond,
	}
	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()
	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		KubeadmInitLock:     &myInitLocker{},
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "control-plane-init-cfg-first",
		},
	}
	result, err := k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

	request = ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "control-plane-init-cfg-second",
		},
	}
	result, err = k.Reconcile(ctx, request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(30 * time.Second))
	confList := &bootstrapv1.KubeadmConfigList{}
	g.Expect(myclient.List(ctx, confList)).To(Succeed())
	for _, c := range confList.Items {
		// Ensure the DataSecretName is only set for controlPlaneInitConfigFirst.
		if c.Name == controlPlaneInitConfigFirst.Name {
			g.Expect(c.Status.DataSecretName).To(Not(BeEmpty()))
		}
		if c.Name == controlPlaneInitConfigSecond.Name {
			g.Expect(c.Status.DataSecretName).To(BeEmpty())
		}
	}
}

// Patch should be applied if there is an error in reconcile.
func TestKubeadmConfigReconciler_Reconcile_PatchWhenErrorOccurred(t *testing.T) {
	g := NewWithT(t)

	cluster := builder.Cluster(metav1.NamespaceDefault, "cluster").Build()
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)

	controlPlaneInitMachine := newControlPlaneMachine(cluster, "control-plane-init-machine")
	controlPlaneInitConfig := newControlPlaneInitKubeadmConfig(controlPlaneInitMachine.Namespace, "control-plane-init-cfg")
	addKubeadmConfigToMachine(controlPlaneInitConfig, controlPlaneInitMachine)
	// set InitConfiguration as nil, we will check this to determine if the kubeadm config has been patched
	controlPlaneInitConfig.Spec.InitConfiguration = nil

	objects := []client.Object{
		cluster,
		controlPlaneInitMachine,
		controlPlaneInitConfig,
	}

	secrets := createSecrets(t, cluster, controlPlaneInitConfig)
	for _, obj := range secrets {
		s := obj.(*corev1.Secret)
		delete(s.Data, secret.TLSCrtDataName) // destroy the secrets, which will cause Reconcile to fail
		objects = append(objects, s)
	}

	myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()
	k := &KubeadmConfigReconciler{
		Client:              myclient,
		SecretCachingClient: myclient,
		KubeadmInitLock:     &myInitLocker{},
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      "control-plane-init-cfg",
		},
	}

	result, err := k.Reconcile(ctx, request)
	g.Expect(err).To(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

	cfg, err := getKubeadmConfig(myclient, "control-plane-init-cfg", metav1.NamespaceDefault)
	g.Expect(err).ToNot(HaveOccurred())
	// check if the kubeadm config has been patched
	g.Expect(cfg.Spec.InitConfiguration).ToNot(BeNil())
	g.Expect(cfg.Status.ObservedGeneration).NotTo(BeNil())
}

func TestKubeadmConfigReconciler_ResolveFiles(t *testing.T) {
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "source",
		},
		Data: map[string][]byte{
			"key": []byte("foo"),
		},
	}

	cases := map[string]struct {
		cfg     *bootstrapv1.KubeadmConfig
		objects []client.Object
		expect  []bootstrapv1.File
	}{
		"content should pass through": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					Files: []bootstrapv1.File{
						{
							Content:     "foo",
							Path:        "/path",
							Owner:       "root:root",
							Permissions: "0600",
						},
					},
				},
			},
			expect: []bootstrapv1.File{
				{
					Content:     "foo",
					Path:        "/path",
					Owner:       "root:root",
					Permissions: "0600",
				},
			},
		},
		"contentFrom should convert correctly": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					Files: []bootstrapv1.File{
						{
							ContentFrom: &bootstrapv1.FileSource{
								Secret: bootstrapv1.SecretFileSource{
									Name: "source",
									Key:  "key",
								},
							},
							Path:        "/path",
							Owner:       "root:root",
							Permissions: "0600",
						},
					},
				},
			},
			expect: []bootstrapv1.File{
				{
					Content:     "foo",
					Path:        "/path",
					Owner:       "root:root",
					Permissions: "0600",
				},
			},
			objects: []client.Object{testSecret},
		},
		"multiple files should work correctly": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					Files: []bootstrapv1.File{
						{
							Content:     "bar",
							Path:        "/bar",
							Owner:       "root:root",
							Permissions: "0600",
						},
						{
							ContentFrom: &bootstrapv1.FileSource{
								Secret: bootstrapv1.SecretFileSource{
									Name: "source",
									Key:  "key",
								},
							},
							Path:        "/path",
							Owner:       "root:root",
							Permissions: "0600",
						},
					},
				},
			},
			expect: []bootstrapv1.File{
				{
					Content:     "bar",
					Path:        "/bar",
					Owner:       "root:root",
					Permissions: "0600",
				},
				{
					Content:     "foo",
					Path:        "/path",
					Owner:       "root:root",
					Permissions: "0600",
				},
			},
			objects: []client.Object{testSecret},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			myclient := fake.NewClientBuilder().WithObjects(tc.objects...).Build()
			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				KubeadmInitLock:     &myInitLocker{},
			}

			// make a list of files we expect to be sourced from secrets
			// after we resolve files, assert that the original spec has
			// not been mutated and all paths we expected to be sourced
			// from secrets still are.
			contentFrom := map[string]bool{}
			for _, file := range tc.cfg.Spec.Files {
				if file.ContentFrom != nil {
					contentFrom[file.Path] = true
				}
			}

			files, err := k.resolveFiles(ctx, tc.cfg)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(files).To(BeComparableTo(tc.expect))
			for _, file := range tc.cfg.Spec.Files {
				if contentFrom[file.Path] {
					g.Expect(file.ContentFrom).NotTo(BeNil())
					g.Expect(file.Content).To(Equal(""))
				}
			}
		})
	}
}

func TestKubeadmConfigReconciler_ResolveDiscoveryFileKubeConfig(t *testing.T) {
	cases := map[string]struct {
		cfg    *bootstrapv1.KubeadmConfig
		expect *bootstrapv1.File
		err    string
	}{
		"should generate the bootstrap kubeconfig correctly": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						Discovery: bootstrapv1.Discovery{
							File: &bootstrapv1.FileDiscovery{
								KubeConfigPath: "/bootstrap-kubeconfig.yaml",
								KubeConfig: &bootstrapv1.FileDiscoveryKubeConfig{
									User: bootstrapv1.KubeConfigUser{
										Exec: &bootstrapv1.KubeConfigAuthExec{
											Command: "/usr/bin/bootstrap",
											Env: []bootstrapv1.KubeConfigAuthExecEnv{
												{Name: "ENV_TEST", Value: "value"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expect: &bootstrapv1.File{
				Path:        "/bootstrap-kubeconfig.yaml",
				Owner:       "root:root",
				Permissions: "0640",
				Content: utilyaml.Raw(`
clusters:
- cluster:
    certificate-authority-data: Y2EtZGF0YQ==
    server: https://example.com:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
preferences: {}
users:
- name: default
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      args: null
      command: /usr/bin/bootstrap
      env:
      - name: ENV_TEST
        value: value
      interactiveMode: Never
      provideClusterInfo: false
`),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			myclient := fake.NewClientBuilder().Build()
			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				KubeadmInitLock:     &myInitLocker{},
			}

			_, err := k.reconcileDiscoveryFile(ctx, &clusterv1.Cluster{
				Spec: clusterv1.ClusterSpec{
					ControlPlaneEndpoint: clusterv1.APIEndpoint{
						Host: "example.com",
						Port: 6443,
					},
				},
			}, tc.cfg, secret.Certificates{
				&secret.Certificate{
					Purpose: secret.ClusterCA,
					KeyPair: &certs.KeyPair{
						Cert: []byte("ca-data"),
						Key:  []byte("ca-key"),
					},
				},
			})
			g.Expect(err).ToNot(HaveOccurred())

			file, err := k.resolveDiscoveryKubeConfig(tc.cfg.Spec.JoinConfiguration.Discovery.File)
			if tc.err != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tc.err)))
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(file).To(BeEquivalentTo(tc.expect))
		})
	}
}

func TestKubeadmConfigReconciler_ResolveUsers(t *testing.T) {
	fakePasswd := "bar"
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "source",
		},
		Data: map[string][]byte{
			"key": []byte(fakePasswd),
		},
	}

	cases := map[string]struct {
		cfg     *bootstrapv1.KubeadmConfig
		objects []client.Object
		expect  []bootstrapv1.User
	}{
		"password should pass through": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					Users: []bootstrapv1.User{
						{
							Name:   "foo",
							Passwd: fakePasswd,
						},
					},
				},
			},
			expect: []bootstrapv1.User{
				{
					Name:   "foo",
					Passwd: fakePasswd,
				},
			},
		},
		"passwdFrom should convert correctly": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					Users: []bootstrapv1.User{
						{
							Name: "foo",
							PasswdFrom: &bootstrapv1.PasswdSource{
								Secret: bootstrapv1.SecretPasswdSource{
									Name: "source",
									Key:  "key",
								},
							},
						},
					},
				},
			},
			expect: []bootstrapv1.User{
				{
					Name:   "foo",
					Passwd: fakePasswd,
				},
			},
			objects: []client.Object{testSecret},
		},
		"multiple users should work correctly": {
			cfg: &bootstrapv1.KubeadmConfig{
				Spec: bootstrapv1.KubeadmConfigSpec{
					Users: []bootstrapv1.User{
						{
							Name:   "foo",
							Passwd: fakePasswd,
						},
						{
							Name: "bar",
							PasswdFrom: &bootstrapv1.PasswdSource{
								Secret: bootstrapv1.SecretPasswdSource{
									Name: "source",
									Key:  "key",
								},
							},
						},
					},
				},
			},
			expect: []bootstrapv1.User{
				{
					Name:   "foo",
					Passwd: fakePasswd,
				},
				{
					Name:   "bar",
					Passwd: fakePasswd,
				},
			},
			objects: []client.Object{testSecret},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			myclient := fake.NewClientBuilder().WithObjects(tc.objects...).Build()
			k := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				KubeadmInitLock:     &myInitLocker{},
			}

			// make a list of password we expect to be sourced from secrets
			// after we resolve users, assert that the original spec has
			// not been mutated and all password we expected to be sourced
			// from secret still are.
			passwdFrom := map[string]bool{}
			for _, user := range tc.cfg.Spec.Users {
				if user.PasswdFrom != nil {
					passwdFrom[user.Name] = true
				}
			}

			users, err := k.resolveUsers(ctx, tc.cfg)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(users).To(BeComparableTo(tc.expect))
			for _, user := range tc.cfg.Spec.Users {
				if passwdFrom[user.Name] {
					g.Expect(user.PasswdFrom).NotTo(BeNil())
					g.Expect(user.Passwd).To(BeEmpty())
				}
			}
		})
	}
}

// test utils.

// newWorkerMachineForCluster returns a Machine with the passed Cluster's information and a pre-configured name.
func newWorkerMachineForCluster(cluster *clusterv1.Cluster) *clusterv1.Machine {
	return builder.Machine(cluster.Namespace, "worker-machine").
		WithVersion("v1.23.1").
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(cluster.Namespace, "conf1").Unstructured()).
		WithInfrastructureMachine(builder.InfrastructureMachine(cluster.Namespace, "inframachine").Build()).
		WithClusterName(cluster.Name).
		Build()
}

// newControlPlaneMachine returns a Machine with the passed Cluster information and a MachineControlPlaneLabel.
func newControlPlaneMachine(cluster *clusterv1.Cluster, name string) *clusterv1.Machine {
	m := builder.Machine(cluster.Namespace, name).
		WithVersion("v1.23.1").
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "cfg").Unstructured()).
		WithClusterName(cluster.Name).
		WithLabels(map[string]string{clusterv1.MachineControlPlaneLabel: ""}).
		Build()
	return m
}

// newMachinePool return a MachinePool object with the passed Cluster information and a basic bootstrap template.
func newMachinePool(cluster *clusterv1.Cluster, name string) *clusterv1.MachinePool {
	m := builder.MachinePool(cluster.Namespace, name).
		WithClusterName(cluster.Name).
		WithLabels(map[string]string{clusterv1.ClusterNameLabel: cluster.Name}).
		WithBootstrap(bootstrapbuilder.KubeadmConfig(cluster.Namespace, "conf1").Unstructured()).
		WithVersion("v1.23.1").
		Build()
	return m
}

// newWorkerMachinePoolForCluster returns a MachinePool with the passed Cluster's information and a pre-configured name.
func newWorkerMachinePoolForCluster(cluster *clusterv1.Cluster) *clusterv1.MachinePool {
	return newMachinePool(cluster, "worker-machinepool")
}

// newKubeadmConfig return a CABPK KubeadmConfig object.
func newKubeadmConfig(namespace, name string) *bootstrapv1.KubeadmConfig {
	return bootstrapbuilder.KubeadmConfig(namespace, name).
		Build()
}

// newKubeadmConfig return a CABPK KubeadmConfig object with a worker JoinConfiguration.
func newWorkerJoinKubeadmConfig(namespace, name string) *bootstrapv1.KubeadmConfig {
	return bootstrapbuilder.KubeadmConfig(namespace, name).Build()
}

// newKubeadmConfig returns a CABPK KubeadmConfig object with a ControlPlane JoinConfiguration.
func newControlPlaneJoinKubeadmConfig(namespace, name string) *bootstrapv1.KubeadmConfig {
	return bootstrapbuilder.KubeadmConfig(namespace, name).
		WithJoinConfig(&bootstrapv1.JoinConfiguration{
			ControlPlane: &bootstrapv1.JoinControlPlane{},
		}).
		Build()
}

// newControlPlaneJoinConfig returns a CABPK KubeadmConfig object with a ControlPlane InitConfiguration and ClusterConfiguration.
func newControlPlaneInitKubeadmConfig(namespace, name string) *bootstrapv1.KubeadmConfig {
	return bootstrapbuilder.KubeadmConfig(namespace, name).Build()
}

// addKubeadmConfigToMachine adds the config details to the passed Machine, and adds the Machine to the KubeadmConfig as an ownerReference.
func addKubeadmConfigToMachine(config *bootstrapv1.KubeadmConfig, machine *clusterv1.Machine) {
	if machine == nil {
		panic("no machine passed to function")
	}
	config.OwnerReferences = []metav1.OwnerReference{
		{
			Kind:       "Machine",
			APIVersion: clusterv1.GroupVersion.String(),
			Name:       machine.Name,
			UID:        types.UID(fmt.Sprintf("%s uid", machine.Name)),
		},
	}

	if machine.Spec.Bootstrap.ConfigRef == nil {
		machine.Spec.Bootstrap.ConfigRef = &clusterv1.ContractVersionedObjectReference{}
	}

	machine.Spec.Bootstrap.ConfigRef.Name = config.Name
}

// addKubeadmConfigToMachine adds the config details to the passed MachinePool and adds the Machine to the KubeadmConfig as an ownerReference.
func addKubeadmConfigToMachinePool(config *bootstrapv1.KubeadmConfig, machinePool *clusterv1.MachinePool) {
	if machinePool == nil {
		panic("no machinePool passed to function")
	}
	config.OwnerReferences = []metav1.OwnerReference{
		{
			Kind:       "MachinePool",
			APIVersion: clusterv1.GroupVersion.String(),
			Name:       machinePool.Name,
			UID:        types.UID(fmt.Sprintf("%s uid", machinePool.Name)),
		},
	}
	machinePool.Spec.Template.Spec.Bootstrap.ConfigRef.Name = config.Name
}

func createSecrets(t *testing.T, cluster *clusterv1.Cluster, config *bootstrapv1.KubeadmConfig) []client.Object {
	t.Helper()

	out := []client.Object{}
	if config.Spec.ClusterConfiguration == nil {
		config.Spec.ClusterConfiguration = &bootstrapv1.ClusterConfiguration{}
	}
	certificates := secret.NewCertificatesForInitialControlPlane(config.Spec.ClusterConfiguration)
	if err := certificates.Generate(); err != nil {
		t.Fatal(err)
	}
	for _, certificate := range certificates {
		out = append(out, certificate.AsSecret(util.ObjectKey(cluster), *metav1.NewControllerRef(config, bootstrapv1.GroupVersion.WithKind("KubeadmConfig"))))
	}
	return out
}

type myInitLocker struct {
	locked bool
}

func (m *myInitLocker) Lock(_ context.Context, _ *clusterv1.Cluster, _ *clusterv1.Machine) bool {
	if !m.locked {
		m.locked = true
		return true
	}
	return false
}

func (m *myInitLocker) Unlock(_ context.Context, _ *clusterv1.Cluster) bool {
	if m.locked {
		m.locked = false
	}
	return true
}

func assertHasFalseCondition(g *WithT, myclient client.Client, req ctrl.Request, conditionType string, reason string) {
	config := &bootstrapv1.KubeadmConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
		},
	}

	configKey := client.ObjectKeyFromObject(config)
	g.Expect(myclient.Get(ctx, configKey, config)).To(Succeed())
	c := conditions.Get(config, conditionType)
	g.Expect(c).ToNot(BeNil())
	g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(c.Reason).To(Equal(reason))
}

func assertHasTrueCondition(g *WithT, myclient client.Client, req ctrl.Request, conditionType string) {
	config := &bootstrapv1.KubeadmConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
		},
	}
	configKey := client.ObjectKeyFromObject(config)
	g.Expect(myclient.Get(ctx, configKey, config)).To(Succeed())
	c := conditions.Get(config, conditionType)
	g.Expect(c).ToNot(BeNil())
	g.Expect(c.Status).To(Equal(metav1.ConditionTrue))
}

func TestKubeadmConfigReconciler_Reconcile_v1beta2_conditions(t *testing.T) {
	// Setup work for an initialized cluster
	clusterName := "my-cluster"
	cluster := builder.Cluster(metav1.NamespaceDefault, clusterName).Build()
	cluster.Status.Conditions = []metav1.Condition{{Type: clusterv1.ClusterControlPlaneInitializedCondition, Status: metav1.ConditionTrue}}
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{
		Host: "example.com",
		Port: 6443,
	}

	machine := builder.Machine(metav1.NamespaceDefault, "my-machine").
		WithVersion("v1.23.1").
		WithClusterName(cluster.Name).
		WithBootstrapTemplate(bootstrapbuilder.KubeadmConfig(metav1.NamespaceDefault, "").Unstructured()).
		Build()

	kubeadmConfig := newKubeadmConfig(metav1.NamespaceDefault, "kubeadmconfig")

	tests := []struct {
		name    string
		config  *bootstrapv1.KubeadmConfig
		machine *clusterv1.Machine
	}{
		{
			name:    "conditions should be true again after reconciling",
			config:  kubeadmConfig.DeepCopy(),
			machine: machine.DeepCopy(),
		},
		{
			name:   "conditions should be true again after status got emptied out",
			config: kubeadmConfig.DeepCopy(),
			machine: func() *clusterv1.Machine {
				m := machine.DeepCopy()
				m.Spec.Bootstrap.DataSecretName = ptr.To("foo")
				return m
			}(),
		},
		{
			name: "conditions should be true after upgrading to v1beta2",
			config: func() *bootstrapv1.KubeadmConfig {
				c := kubeadmConfig.DeepCopy()
				c.Status.Initialization.DataSecretCreated = ptr.To(true)
				return c
			}(),
			machine: machine.DeepCopy(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			cluster := cluster.DeepCopy()
			tt.config.SetOwnerReferences([]metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Machine",
				Name:       tt.machine.Name,
			}})

			objects := []client.Object{cluster, tt.machine, tt.config}
			objects = append(objects, createSecrets(t, cluster, tt.config)...)

			myclient := fake.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&bootstrapv1.KubeadmConfig{}).Build()

			r := &KubeadmConfigReconciler{
				Client:              myclient,
				SecretCachingClient: myclient,
				ClusterCache:        clustercache.NewFakeClusterCache(myclient, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}),
				KubeadmInitLock:     &myInitLocker{},
			}

			key := client.ObjectKey{Namespace: tt.config.Namespace, Name: tt.config.Name}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			g.Expect(err).ToNot(HaveOccurred())

			newConfig := &bootstrapv1.KubeadmConfig{}
			g.Expect(myclient.Get(ctx, key, newConfig)).To(Succeed())

			for _, conditionType := range []string{bootstrapv1.KubeadmConfigReadyCondition, bootstrapv1.KubeadmConfigCertificatesAvailableCondition, bootstrapv1.KubeadmConfigDataSecretAvailableCondition} {
				condition := conditions.Get(newConfig, conditionType)
				g.Expect(condition).ToNot(BeNil(), "condition %s is missing", conditionType)
				g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(condition.Message).To(BeEmpty())
			}
			for _, conditionType := range []string{clusterv1.PausedCondition} {
				condition := conditions.Get(newConfig, conditionType)
				g.Expect(condition).ToNot(BeNil(), "condition %s is missing", conditionType)
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Message).To(BeEmpty())
			}
		})
	}
}
