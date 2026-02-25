/*
Copyright 2020 The Kubernetes Authors.

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
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/structured-merge-diff/v6/typed"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/controlplane/kubeadm/internal"
	"sigs.k8s.io/cluster-api/controlplane/kubeadm/internal/desiredstate"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util"
	acclusterv1 "sigs.k8s.io/cluster-api/util/applyconfigurations/core/v1beta2/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/certs"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/conditions/deprecated/v1beta1"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
)

func (r *KubeadmControlPlaneReconciler) reconcileKubeconfig(ctx context.Context, controlPlane *internal.ControlPlane) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	endpoint := controlPlane.Cluster.Spec.ControlPlaneEndpoint
	if endpoint.IsZero() {
		return ctrl.Result{}, nil
	}
	controllerOwnerRef := *metav1.NewControllerRef(controlPlane.KCP, controlplanev1.GroupVersion.WithKind(kubeadmControlPlaneKind))
	clusterName := util.ObjectKey(controlPlane.Cluster)
	configSecret, err := secret.GetFromNamespacedName(ctx, r.SecretCachingClient, clusterName, secret.Kubeconfig)
	switch {
	case apierrors.IsNotFound(err):
		createErr := kubeconfig.CreateSecretWithOwner(
			ctx,
			r.SecretCachingClient,
			clusterName,
			endpoint.String(),
			controllerOwnerRef,
			kubeconfig.KeyEncryptionAlgorithm(controlPlane.GetKeyEncryptionAlgorithm()),
		)
		if errors.Is(createErr, kubeconfig.ErrDependentCertificateNotFound) {
			return ctrl.Result{RequeueAfter: dependentCertRequeueAfter}, nil
		}
		// always return if we have just created in order to skip rotation checks
		return ctrl.Result{}, createErr
	case err != nil:
		return ctrl.Result{}, errors.Wrap(err, "failed to retrieve kubeconfig Secret")
	}

	if err := r.adoptKubeconfigSecret(ctx, configSecret, controlPlane.KCP); err != nil {
		return ctrl.Result{}, err
	}

	// only do rotation on owned secrets
	if !util.IsControlledBy(configSecret, controlPlane.KCP, controlplanev1.GroupVersion.WithKind("KubeadmControlPlane").GroupKind()) {
		return ctrl.Result{}, nil
	}

	needsRotation, err := kubeconfig.NeedsClientCertRotation(configSecret, certs.ClientCertificateRenewalDuration)
	if err != nil {
		return ctrl.Result{}, err
	}

	if needsRotation {
		log.Info("Rotating kubeconfig secret")
		if err := kubeconfig.RegenerateSecret(ctx, r.Client, configSecret, kubeconfig.KeyEncryptionAlgorithm(controlPlane.GetKeyEncryptionAlgorithm())); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to regenerate kubeconfig")
		}
	}

	return ctrl.Result{}, nil
}

// Ensure the KubeadmConfigSecret has an owner reference to the control plane if it is not a user-provided secret.
func (r *KubeadmControlPlaneReconciler) adoptKubeconfigSecret(ctx context.Context, configSecret *corev1.Secret, kcp *controlplanev1.KubeadmControlPlane) (reterr error) {
	patchHelper, err := patch.NewHelper(configSecret, r.Client)
	if err != nil {
		return err
	}
	defer func() {
		if err := patchHelper.Patch(ctx, configSecret); err != nil {
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}
	}()
	controller := metav1.GetControllerOf(configSecret)

	// If the current controller is KCP, ensure the owner reference is up to date and return early.
	// Note: This ensures secrets created prior to v1alpha4 are updated to have the correct owner reference apiVersion.
	if controller != nil && controller.Kind == kubeadmControlPlaneKind {
		configSecret.SetOwnerReferences(util.EnsureOwnerRef(configSecret.GetOwnerReferences(), *metav1.NewControllerRef(kcp, controlplanev1.GroupVersion.WithKind(kubeadmControlPlaneKind))))
		return nil
	}

	// If secret type is a CAPI-created secret ensure the owner reference is to KCP.
	if configSecret.Type == clusterv1.ClusterSecretType {
		// Remove the current controller if one exists and ensure KCP is the controller of the secret.
		if controller != nil {
			configSecret.SetOwnerReferences(util.RemoveOwnerRef(configSecret.GetOwnerReferences(), *controller))
		}
		configSecret.SetOwnerReferences(util.EnsureOwnerRef(configSecret.GetOwnerReferences(), *metav1.NewControllerRef(kcp, controlplanev1.GroupVersion.WithKind(kubeadmControlPlaneKind))))
	}
	return nil
}

func (r *KubeadmControlPlaneReconciler) reconcileExternalReference(ctx context.Context, controlPlane *internal.ControlPlane) error {
	ref := controlPlane.KCP.Spec.MachineTemplate.Spec.InfrastructureRef
	if !strings.HasSuffix(ref.Kind, clusterv1.TemplateSuffix) {
		return nil
	}

	obj, err := external.GetObjectFromContractVersionedRef(ctx, r.Client, ref, controlPlane.KCP.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			controlPlane.InfraMachineTemplateIsNotFound = true
		}
		return err
	}

	// Note: We intentionally do not handle checking for the paused label on an external template reference

	desiredOwnerRef := metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       controlPlane.Cluster.Name,
		UID:        controlPlane.Cluster.UID,
	}

	if util.HasExactOwnerRef(obj.GetOwnerReferences(), desiredOwnerRef) {
		return nil
	}

	patchHelper, err := patch.NewHelper(obj, r.Client)
	if err != nil {
		return err
	}

	obj.SetOwnerReferences(util.EnsureOwnerRef(obj.GetOwnerReferences(), desiredOwnerRef))

	return patchHelper.Patch(ctx, obj)
}

func (r *KubeadmControlPlaneReconciler) cloneConfigsAndGenerateMachine(ctx context.Context, cluster *clusterv1.Cluster, kcp *controlplanev1.KubeadmControlPlane, isJoin bool, failureDomain string) (*clusterv1.Machine, error) {
	var errs []error

	machine, err := desiredstate.ComputeDesiredMachine(kcp, cluster, failureDomain, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Machine")
	}

	infraMachine, infraRef, err := r.createInfraMachine(ctx, kcp, cluster, machine.Name)
	if err != nil {
		// Safe to return early here since no resources have been created yet.
		v1beta1conditions.MarkFalse(kcp, controlplanev1.MachinesCreatedV1Beta1Condition, controlplanev1.InfrastructureTemplateCloningFailedV1Beta1Reason,
			clusterv1.ConditionSeverityError, "%s", err.Error())
		return nil, errors.Wrap(err, "failed to create Machine")
	}
	machine.Spec.InfrastructureRef = infraRef

	// Clone the bootstrap configuration
	bootstrapConfig, bootstrapRef, err := r.createKubeadmConfig(ctx, kcp, cluster, isJoin, machine.Name)
	if err != nil {
		v1beta1conditions.MarkFalse(kcp, controlplanev1.MachinesCreatedV1Beta1Condition, controlplanev1.BootstrapTemplateCloningFailedV1Beta1Reason,
			clusterv1.ConditionSeverityError, "%s", err.Error())
		errs = append(errs, errors.Wrap(err, "failed to create Machine"))
	}

	// Only proceed to creating the Machine if we haven't encountered an error
	if len(errs) == 0 {
		machine.Spec.Bootstrap.ConfigRef = bootstrapRef

		if err := r.createMachine(ctx, kcp, machine); err != nil {
			v1beta1conditions.MarkFalse(kcp, controlplanev1.MachinesCreatedV1Beta1Condition, controlplanev1.MachineGenerationFailedV1Beta1Reason,
				clusterv1.ConditionSeverityError, "%s", err.Error())
			errs = append(errs, errors.Wrap(err, "failed to create Machine"))
		}
	}

	// If we encountered any errors, attempt to clean up any dangling resources
	if len(errs) > 0 {
		if err := r.cleanupFromGeneration(ctx, infraMachine, bootstrapConfig); err != nil {
			errs = append(errs, errors.Wrap(err, "failed to cleanup created objects"))
		}
		return nil, kerrors.NewAggregate(errs)
	}

	return machine, nil
}

func (r *KubeadmControlPlaneReconciler) cleanupFromGeneration(ctx context.Context, objects ...client.Object) error {
	var errs []error

	for _, obj := range objects {
		if obj == nil {
			continue
		}
		if err := r.Client.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, errors.Wrap(err, "failed to cleanup generated resources after error"))
		}
	}

	return kerrors.NewAggregate(errs)
}

func (r *KubeadmControlPlaneReconciler) createInfraMachine(ctx context.Context, kcp *controlplanev1.KubeadmControlPlane, cluster *clusterv1.Cluster, name string) (*unstructured.Unstructured, clusterv1.ContractVersionedObjectReference, error) {
	infraMachine, err := desiredstate.ComputeDesiredInfraMachine(ctx, r.Client, kcp, cluster, name, nil)
	if err != nil {
		return nil, clusterv1.ContractVersionedObjectReference{}, errors.Wrapf(err, "failed to create InfraMachine")
	}

	// Create the full object with capi-kubeadmcontrolplane.
	// Below ssa.RemoveManagedFieldsForLabelsAndAnnotations will drop ownership for labels and annotations
	// so that in a subsequent syncMachines call capi-kubeadmcontrolplane-metadata can take ownership for them.
	// Note: This is done in way that it does not rely on managedFields being stored in the cache, so we can optimize
	// memory usage by dropping managedFields before storing objects in the cache.
	if err := ssa.Patch(ctx, r.Client, kcpManagerName, infraMachine); err != nil {
		return nil, clusterv1.ContractVersionedObjectReference{}, errors.Wrapf(err, "failed to create InfraMachine")
	}

	// Note: This field is only used for unit tests that use fake client because the fake client does not properly set resourceVersion
	//       on KubeadmConfig/InfraMachine after ssa.Patch and then ssa.RemoveManagedFieldsForLabelsAndAnnotations would fail.
	if !r.disableRemoveManagedFieldsForLabelsAndAnnotations {
		if err := ssa.RemoveManagedFieldsForLabelsAndAnnotations(ctx, r.Client, r.APIReader, infraMachine, kcpManagerName); err != nil {
			return nil, clusterv1.ContractVersionedObjectReference{}, errors.Wrapf(err, "failed to create InfraMachine")
		}
	}

	return infraMachine, clusterv1.ContractVersionedObjectReference{
		APIGroup: infraMachine.GroupVersionKind().Group,
		Kind:     infraMachine.GetKind(),
		Name:     infraMachine.GetName(),
	}, nil
}

func (r *KubeadmControlPlaneReconciler) createKubeadmConfig(ctx context.Context, kcp *controlplanev1.KubeadmControlPlane, cluster *clusterv1.Cluster, isJoin bool, name string) (*bootstrapv1.KubeadmConfig, clusterv1.ContractVersionedObjectReference, error) {
	kubeadmConfig, err := desiredstate.ComputeDesiredKubeadmConfig(kcp, cluster, isJoin, name, nil)
	if err != nil {
		return nil, clusterv1.ContractVersionedObjectReference{}, errors.Wrapf(err, "failed to create KubeadmConfig")
	}

	// Create the full object with capi-kubeadmcontrolplane.
	// Below ssa.RemoveManagedFieldsForLabelsAndAnnotations will drop ownership for labels and annotations
	// so that in a subsequent syncMachines call capi-kubeadmcontrolplane-metadata can take ownership for them.
	// Note: This is done in way that it does not rely on managedFields being stored in the cache, so we can optimize
	// memory usage by dropping managedFields before storing objects in the cache.
	if err := ssa.Patch(ctx, r.Client, kcpManagerName, kubeadmConfig); err != nil {
		return nil, clusterv1.ContractVersionedObjectReference{}, errors.Wrapf(err, "failed to create KubeadmConfig")
	}

	// Note: This field is only used for unit tests that use fake client because the fake client does not properly set resourceVersion
	//       on KubeadmConfig/InfraMachine after ssa.Patch and then ssa.RemoveManagedFieldsForLabelsAndAnnotations would fail.
	if !r.disableRemoveManagedFieldsForLabelsAndAnnotations {
		if err := ssa.RemoveManagedFieldsForLabelsAndAnnotations(ctx, r.Client, r.APIReader, kubeadmConfig, kcpManagerName); err != nil {
			return nil, clusterv1.ContractVersionedObjectReference{}, errors.Wrapf(err, "failed to create KubeadmConfig")
		}
	}

	return kubeadmConfig, clusterv1.ContractVersionedObjectReference{
		APIGroup: bootstrapv1.GroupVersion.Group,
		Kind:     "KubeadmConfig",
		Name:     kubeadmConfig.GetName(),
	}, nil
}

// updateLabelsAndAnnotations updates the external object with the labels and annotations from KCP.
func (r *KubeadmControlPlaneReconciler) updateLabelsAndAnnotations(ctx context.Context, obj client.Object, objGVK schema.GroupVersionKind, kcp *controlplanev1.KubeadmControlPlane, cluster *clusterv1.Cluster) error {
	updatedObject := &unstructured.Unstructured{}
	updatedObject.SetGroupVersionKind(objGVK)
	updatedObject.SetNamespace(obj.GetNamespace())
	updatedObject.SetName(obj.GetName())
	// Set the UID to ensure that Server-Side-Apply only performs an update
	// and does not perform an accidental create.
	updatedObject.SetUID(obj.GetUID())

	updatedObject.SetLabels(desiredstate.ControlPlaneMachineLabels(kcp, cluster.Name))
	updatedObject.SetAnnotations(desiredstate.ControlPlaneMachineAnnotations(kcp))

	currentPartialObjectMetadata := &metav1.PartialObjectMetadata{}
	currentPartialObjectMetadata.SetGroupVersionKind(objGVK)
	managedFields := []metav1.ManagedFieldsEntry{}
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager == kcpMetadataManagerName && mf.Operation == metav1.ManagedFieldsOperationApply && mf.Subresource == "" {
			managedFields = append(managedFields, mf)
			break
		}
	}
	if len(managedFields) > 0 {
		currentPartialObjectMetadata.SetManagedFields(managedFields)
		currentPartialObjectMetadata.SetLabels(obj.GetLabels())
		currentPartialObjectMetadata.SetAnnotations(obj.GetAnnotations())

		currentPartialObjectMetaOwnedByFieldManager := &metav1.PartialObjectMetadata{}
		currentPartialObjectMetaOwnedByFieldManager.SetGroupVersionKind(objGVK)
		err := managedfields.ExtractInto(currentPartialObjectMetadata, Parser().Type("io.k8s.apimachinery.pkg.apis.meta.v1.PartialObjectMeta"), kcpMetadataManagerName, currentPartialObjectMetaOwnedByFieldManager, "")
		if err != nil {
			return err // FIXME: decide what to do on errors
		}

		if maps.Equal(currentPartialObjectMetaOwnedByFieldManager.Labels, updatedObject.GetLabels()) &&
			maps.Equal(currentPartialObjectMetaOwnedByFieldManager.Annotations, updatedObject.GetAnnotations()) {
			return nil
		}
	}

	return ssa.Patch(ctx, r.Client, kcpMetadataManagerName, updatedObject, ssa.WithCachingProxy{Cache: r.ssaCache, Original: obj})
}

var parserOnce sync.Once
var parser *typed.Parser

func Parser() *typed.Parser {
	parserOnce.Do(func() {
		var err error
		parser, err = typed.NewParser(schemaYAML)
		if err != nil {
			panic(fmt.Sprintf("Failed to parse schema: %v", err))
		}
	})
	return parser
}

var schemaYAML = typed.YAMLObject(`types:
- name: io.k8s.apimachinery.pkg.apis.meta.v1.PartialObjectMeta
  map:
    fields:
    - name: apiVersion
      type:
        scalar: string
    - name: kind
      type:
        scalar: string
    - name: metadata
      type:
        namedType: io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta
- name: io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta
  map:
    fields:
    - name: annotations
      type:
        map:
          elementType:
            scalar: string
    - name: labels
      type:
        map:
          elementType:
            scalar: string
    - name: managedFields
      type:
        list:
          elementType:
            namedType: io.k8s.apimachinery.pkg.apis.meta.v1.ManagedFieldsEntry
          elementRelationship: atomic
- name: io.k8s.apimachinery.pkg.apis.meta.v1.ManagedFieldsEntry
  scalar: untyped
  list:
    elementType:
      namedType: __untyped_atomic_
    elementRelationship: atomic
  map:
    elementType:
      namedType: __untyped_deduced_
    elementRelationship: separable
- name: __untyped_atomic_
  scalar: untyped
  list:
    elementType:
      namedType: __untyped_atomic_
    elementRelationship: atomic
  map:
    elementType:
      namedType: __untyped_atomic_
    elementRelationship: atomic
- name: __untyped_deduced_
  scalar: untyped
  list:
    elementType:
      namedType: __untyped_atomic_
    elementRelationship: atomic
  map:
    elementType:
      namedType: __untyped_deduced_
    elementRelationship: separable
`)

func (r *KubeadmControlPlaneReconciler) createMachine(ctx context.Context, kcp *controlplanev1.KubeadmControlPlane, machine *clusterv1.Machine) error {
	if err := ssa.Patch(ctx, r.Client, kcpManagerName, machine); err != nil {
		return err
	}
	// Remove the annotation tracking that a remediation is in progress (the remediation completed when
	// the replacement machine has been created above).
	delete(kcp.Annotations, controlplanev1.RemediationInProgressAnnotation)
	return nil
}

func (r *KubeadmControlPlaneReconciler) updateMachine(ctx context.Context, machine *clusterv1.Machine, kcp *controlplanev1.KubeadmControlPlane, cluster *clusterv1.Cluster) (*clusterv1.Machine, error) {
	updatedMachine, err := desiredstate.ComputeDesiredMachine(kcp, cluster, machine.Spec.FailureDomain, machine)
	if err != nil {
		return nil, errors.Wrap(err, "failed to apply Machine")
	}

	updatedMachineApplyConfiguration := &acclusterv1.MachineApplyConfiguration{}
	updatedMachineBytes, err := json.Marshal(updatedMachine)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(updatedMachineBytes, updatedMachineApplyConfiguration); err != nil {
		return nil, err
	}

	currentMachineApplyConfiguration, err := acclusterv1.ExtractMachine(machine, kcpManagerName)
	if err != nil {
		return nil, err // FIXME: decide what to do on errors
	}
	currentMachineApplyConfiguration.UID = ptr.To(machine.UID) // FIXME: why does Extract not set UID? (probably no ownership, but we need it in updateMachine)

	if !reflect.DeepEqual(currentMachineApplyConfiguration, updatedMachineApplyConfiguration) {
		err = ssa.Patch(ctx, r.Client, kcpManagerName, updatedMachine, ssa.WithCachingProxy{Cache: r.ssaCache, Original: machine})
		if err != nil {
			return nil, err
		}
		return updatedMachine, nil
	}
	return machine, nil
}
