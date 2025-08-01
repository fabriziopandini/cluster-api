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
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/internal/contract"
	"sigs.k8s.io/cluster-api/internal/topology/check"
	"sigs.k8s.io/cluster-api/internal/topology/variables"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/version"
)

// SetupWebhookWithManager sets up Cluster webhooks.
func (webhook *Cluster) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if webhook.decoder == nil {
		webhook.decoder = admission.NewDecoder(mgr.GetScheme())
	}

	return ctrl.NewWebhookManagedBy(mgr).
		For(&clusterv1.Cluster{}).
		WithDefaulter(webhook).
		WithValidator(webhook).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update;delete,path=/validate-cluster-x-k8s-io-v1beta2-cluster,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,groups=cluster.x-k8s.io,resources=clusters,versions=v1beta2,name=validation.cluster.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1
// +kubebuilder:webhook:verbs=create;update,path=/mutate-cluster-x-k8s-io-v1beta2-cluster,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=cluster.x-k8s.io,resources=clusters,versions=v1beta2,name=default.cluster.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1

// ClusterCacheReader is a scoped-down interface from ClusterCacheTracker that only allows to get a reader client.
type ClusterCacheReader interface {
	GetReader(ctx context.Context, cluster client.ObjectKey) (client.Reader, error)
}

// Cluster implements a validating and defaulting webhook for Cluster.
type Cluster struct {
	Client             client.Reader
	ClusterCacheReader ClusterCacheReader

	decoder admission.Decoder
}

var _ webhook.CustomDefaulter = &Cluster{}
var _ webhook.CustomValidator = &Cluster{}

var errClusterClassNotReconciled = errors.New("ClusterClass is not successfully reconciled")

// Default satisfies the defaulting webhook interface.
func (webhook *Cluster) Default(ctx context.Context, obj runtime.Object) error {
	// We gather all defaulting errors and return them together.
	var allErrs field.ErrorList

	cluster, ok := obj.(*clusterv1.Cluster)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a Cluster but got a %T", obj))
	}

	// Additional defaulting if the Cluster uses a managed topology.
	if cluster.Spec.Topology != nil {
		// Tolerate version strings without a "v" prefix: prepend it if it's not there.
		if !strings.HasPrefix(cluster.Spec.Topology.Version, "v") {
			cluster.Spec.Topology.Version = "v" + cluster.Spec.Topology.Version
		}

		if cluster.GetClassKey().Name == "" {
			allErrs = append(
				allErrs,
				field.Required(
					field.NewPath("spec", "topology", "classRef"),
					"classRef.name cannot be empty",
				),
			)
			return apierrors.NewInvalid(clusterv1.GroupVersion.WithKind("Cluster").GroupKind(), cluster.Name, allErrs)
		}

		clusterClass, err := webhook.pollClusterClassForCluster(ctx, cluster)
		if err != nil {
			// If the ClusterClass can't be found or is not up to date ignore the error.
			if apierrors.IsNotFound(err) || errors.Is(err, errClusterClassNotReconciled) {
				return nil
			}
			return apierrors.NewInternalError(errors.Wrapf(err, "Cluster %s can't be defaulted. ClusterClass %s can not be retrieved", cluster.Name, cluster.GetClassKey().Name))
		}

		// Validate cluster class variables transitions that may be enforced by CEL validation rules on variables.
		// If no request found in context, then this has not come via a webhook request, so skip validation of old cluster.
		var oldCluster *clusterv1.Cluster
		req, err := admission.RequestFromContext(ctx)

		if err == nil && len(req.OldObject.Raw) > 0 {
			oldCluster = &clusterv1.Cluster{}
			if err := webhook.decoder.DecodeRaw(req.OldObject, oldCluster); err != nil {
				return apierrors.NewBadRequest(errors.Wrap(err, "failed to decode old cluster object").Error())
			}
		}

		// Doing both defaulting and validating here prevents a race condition where the ClusterClass could be
		// different in the defaulting and validating webhook.
		allErrs = append(allErrs, DefaultAndValidateVariables(ctx, cluster, oldCluster, clusterClass)...)
		if len(allErrs) > 0 {
			return apierrors.NewInvalid(clusterv1.GroupVersion.WithKind("Cluster").GroupKind(), cluster.Name, allErrs)
		}
	}
	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type.
func (webhook *Cluster) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cluster, ok := obj.(*clusterv1.Cluster)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Cluster but got a %T", obj))
	}
	return webhook.validate(ctx, nil, cluster)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type.
func (webhook *Cluster) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newCluster, ok := newObj.(*clusterv1.Cluster)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Cluster but got a %T", newObj))
	}
	oldCluster, ok := oldObj.(*clusterv1.Cluster)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Cluster but got a %T", oldObj))
	}
	return webhook.validate(ctx, oldCluster, newCluster)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type.
func (webhook *Cluster) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (webhook *Cluster) validate(ctx context.Context, oldCluster, newCluster *clusterv1.Cluster) (admission.Warnings, error) {
	var allErrs field.ErrorList
	var allWarnings admission.Warnings
	// The Cluster name is used as a label value. This check ensures that names which are not valid label values are rejected.
	if errs := validation.IsValidLabelValue(newCluster.Name); len(errs) != 0 {
		for _, err := range errs {
			allErrs = append(
				allErrs,
				field.Invalid(
					field.NewPath("metadata", "name"),
					newCluster.Name,
					fmt.Sprintf("must be a valid label value %s", err),
				),
			)
		}
	}

	specPath := field.NewPath("spec")
	if newCluster.Spec.InfrastructureRef == nil && oldCluster != nil && oldCluster.Spec.InfrastructureRef != nil {
		allErrs = append(
			allErrs,
			field.Forbidden(
				specPath.Child("infrastructureRef"),
				"cannot be removed",
			),
		)
	}

	if newCluster.Spec.ControlPlaneRef == nil && newCluster.Spec.InfrastructureRef == nil &&
		newCluster.Spec.Topology == nil {
		allErrs = append(
			allErrs,
			field.Forbidden(
				specPath,
				"one of spec.controlPlaneRef, spec.infrastructureRef or spec.topology must be set",
			),
		)
	}

	if newCluster.Spec.ControlPlaneRef == nil && oldCluster != nil && oldCluster.Spec.ControlPlaneRef != nil {
		allErrs = append(
			allErrs,
			field.Forbidden(
				specPath.Child("controlPlaneRef"),
				"cannot be removed",
			),
		)
	}

	// Ensure that the CIDR blocks defined under ClusterNetwork are valid.
	allErrs = append(allErrs, validateCIDRBlocks(specPath.Child("clusterNetwork", "pods", "cidrBlocks"),
		newCluster.Spec.ClusterNetwork.Pods.CIDRBlocks)...)
	allErrs = append(allErrs, validateCIDRBlocks(specPath.Child("clusterNetwork", "services", "cidrBlocks"),
		newCluster.Spec.ClusterNetwork.Services.CIDRBlocks)...)

	topologyPath := specPath.Child("topology")

	// Validate the managed topology, if defined.
	if newCluster.Spec.Topology != nil {
		topologyWarnings, topologyErrs := webhook.validateTopology(ctx, oldCluster, newCluster, topologyPath)
		allWarnings = append(allWarnings, topologyWarnings...)
		allErrs = append(allErrs, topologyErrs...)
	}

	// On update.
	if oldCluster != nil {
		// Error if the update moves the cluster from Managed to Unmanaged i.e. the managed topology is removed on update.
		if oldCluster.Spec.Topology != nil && newCluster.Spec.Topology == nil {
			allErrs = append(allErrs, field.Forbidden(
				topologyPath,
				"cannot be removed from an existing Cluster",
			))
		}
	}

	if len(allErrs) > 0 {
		return allWarnings, apierrors.NewInvalid(clusterv1.GroupVersion.WithKind("Cluster").GroupKind(), newCluster.Name, allErrs)
	}
	return allWarnings, nil
}

func (webhook *Cluster) validateTopology(ctx context.Context, oldCluster, newCluster *clusterv1.Cluster, fldPath *field.Path) (admission.Warnings, field.ErrorList) {
	var allWarnings admission.Warnings

	// NOTE: ClusterClass and managed topologies are behind ClusterTopology feature gate flag; the web hook
	// must prevent the usage of Cluster.Topology in case the feature flag is disabled.
	if !feature.Gates.Enabled(feature.ClusterTopology) {
		return allWarnings, field.ErrorList{
			field.Forbidden(
				fldPath,
				"can be set only if the ClusterTopology feature flag is enabled",
			),
		}
	}

	var allErrs field.ErrorList

	// class should be defined.
	if newCluster.GetClassKey().Name == "" {
		allErrs = append(
			allErrs,
			field.Required(
				fldPath.Child("class"),
				"classRef.name cannot be empty",
			),
		)
		// Return early if there is no defined class to validate.
		return allWarnings, allErrs
	}

	// version should be valid.
	if !version.KubeSemver.MatchString(newCluster.Spec.Topology.Version) {
		allErrs = append(
			allErrs,
			field.Invalid(
				fldPath.Child("version"),
				newCluster.Spec.Topology.Version,
				"version must be a valid semantic version",
			),
		)
	}

	// metadata in topology should be valid
	allErrs = append(allErrs, validateTopologyMetadata(newCluster.Spec.Topology, fldPath)...)

	// upgrade concurrency should be a numeric value.
	if concurrency, ok := newCluster.Annotations[clusterv1.ClusterTopologyUpgradeConcurrencyAnnotation]; ok {
		concurrencyAnnotationField := field.NewPath("metadata", "annotations", clusterv1.ClusterTopologyUpgradeConcurrencyAnnotation)
		concurrencyInt, err := strconv.Atoi(concurrency)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(
				concurrencyAnnotationField,
				concurrency,
				errors.Wrap(err, "could not parse the value of the annotation").Error(),
			))
		} else if concurrencyInt < 1 {
			allErrs = append(allErrs, field.Invalid(
				concurrencyAnnotationField,
				concurrency,
				"value cannot be less than 1",
			))
		}
	}

	// Get the ClusterClass referenced in the Cluster.
	clusterClass, warnings, clusterClassPollErr := webhook.validateClusterClassExistsAndIsReconciled(ctx, newCluster)
	// If the error is anything other than "NotFound" or "NotReconciled" return all errors.
	if clusterClassPollErr != nil && (!apierrors.IsNotFound(clusterClassPollErr) && !errors.Is(clusterClassPollErr, errClusterClassNotReconciled)) {
		allErrs = append(
			allErrs, field.InternalError(
				fldPath.Child("class"),
				clusterClassPollErr))
		return allWarnings, allErrs
	}

	// Add the warnings if no error was returned.
	allWarnings = append(allWarnings, warnings...)

	// If there's no error validate the Cluster based on the ClusterClass.
	if clusterClassPollErr == nil {
		allErrs = append(allErrs, ValidateClusterForClusterClass(newCluster, clusterClass)...)
	}

	// Validate the Cluster and associated ClusterClass' autoscaler annotations.
	allErrs = append(allErrs, validateAutoscalerAnnotationsForCluster(newCluster, clusterClass)...)

	if oldCluster != nil { // On update
		// The ClusterClass must exist to proceed with update validation. Return an error if the ClusterClass was
		// not found.
		if apierrors.IsNotFound(clusterClassPollErr) {
			allErrs = append(
				allErrs, field.InternalError(
					fldPath.Child("class"),
					clusterClassPollErr))
			return allWarnings, allErrs
		}

		// Topology or Class can not be added on update unless ClusterTopologyUnsafeUpdateClassNameAnnotation is set.
		if oldCluster.Spec.Topology == nil || oldCluster.GetClassKey().Name == "" {
			if _, ok := newCluster.Annotations[clusterv1.ClusterTopologyUnsafeUpdateClassNameAnnotation]; ok {
				return allWarnings, allErrs
			}

			allErrs = append(
				allErrs,
				field.Forbidden(
					fldPath.Child("class"),
					"classRef cannot be set on an existing Cluster",
				),
			)
			// return early here if there is no class to compare.
			return allWarnings, allErrs
		}

		inVersion, err := semver.ParseTolerant(newCluster.Spec.Topology.Version)
		if err != nil {
			allErrs = append(
				allErrs,
				field.Invalid(
					fldPath.Child("version"),
					newCluster.Spec.Topology.Version,
					"version must be a valid semantic version",
				),
			)
		}
		oldVersion, err := semver.ParseTolerant(oldCluster.Spec.Topology.Version)
		if err != nil {
			// NOTE: this should never happen. Nevertheless, handling this for extra caution.
			allErrs = append(
				allErrs,
				field.Invalid(
					fldPath.Child("version"),
					oldCluster.Spec.Topology.Version,
					"old version must be a valid semantic version",
				),
			)
		}

		if _, ok := newCluster.GetAnnotations()[clusterv1.ClusterTopologyUnsafeUpdateVersionAnnotation]; ok {
			log := ctrl.LoggerFrom(ctx)
			warningMsg := fmt.Sprintf("Skipping version validation for Cluster because annotation %q is set.", clusterv1.ClusterTopologyUnsafeUpdateVersionAnnotation)
			log.Info(warningMsg)
			allWarnings = append(allWarnings, warningMsg)
		} else {
			if err := webhook.validateTopologyVersion(ctx, fldPath.Child("version"), newCluster.Spec.Topology.Version, inVersion, oldVersion, oldCluster); err != nil {
				allErrs = append(allErrs, err)
			}
		}

		// If the ClusterClass referenced in the Topology has changed compatibility checks are needed.
		if oldCluster.GetClassKey() != newCluster.GetClassKey() {
			if clusterClassPollErr != nil {
				allErrs = append(
					allErrs, field.Forbidden(
						fldPath.Child("class"),
						fmt.Sprintf("cannot rebase to ClusterClass %q: %s",
							newCluster.GetClassKey(), clusterClassPollErr.Error())))
				// Return early with errors if the new ClusterClass can't be retrieved.
				return allWarnings, allErrs
			}

			// Check to see if the ClusterClass referenced in the old version of the Cluster exists.
			oldClusterClass, err := webhook.pollClusterClassForCluster(ctx, oldCluster)
			if err != nil {
				allErrs = append(
					allErrs, field.Forbidden(
						fldPath.Child("class"),
						fmt.Sprintf("valid ClusterClass with name %q could not be retrieved, change from class %[1]q to class %q cannot be validated. Error: %s",
							oldCluster.GetClassKey(), newCluster.GetClassKey(), err.Error())))

				// Return early with errors if the old ClusterClass can't be retrieved.
				return allWarnings, allErrs
			}

			// Check if the new and old ClusterClasses are compatible with one another.
			allErrs = append(allErrs, check.ClusterClassesAreCompatible(oldClusterClass, clusterClass)...)
		}
	}

	return allWarnings, allErrs
}

func (webhook *Cluster) validateTopologyVersion(ctx context.Context, fldPath *field.Path, fldValue string, inVersion, oldVersion semver.Version, oldCluster *clusterv1.Cluster) *field.Error {
	// Nothing to do if the version doesn't change.
	if inVersion.String() == oldVersion.String() {
		return nil
	}

	// Version could only be increased.
	if inVersion.NE(semver.Version{}) && oldVersion.NE(semver.Version{}) && version.Compare(inVersion, oldVersion, version.WithoutPreReleases()) < 0 {
		return field.Invalid(
			fldPath,
			fldValue,
			fmt.Sprintf("version cannot be decreased from %q to %q", oldVersion, inVersion),
		)
	}

	// A +2 minor version upgrade is not allowed.
	ceilVersion := semver.Version{
		Major: oldVersion.Major,
		Minor: oldVersion.Minor + 2,
		Patch: 0,
	}
	if version.Compare(inVersion, ceilVersion, version.WithoutPreReleases()) >= 0 {
		return field.Invalid(
			fldPath,
			fldValue,
			fmt.Sprintf("version cannot be increased from %q to %q", oldVersion, inVersion),
		)
	}

	allErrs := []error{}
	// minor version cannot be increased if control plane is upgrading or not yet on the current version
	if err := validateTopologyControlPlaneVersion(ctx, webhook.Client, oldCluster, oldVersion); err != nil {
		allErrs = append(allErrs, err)
	}

	// minor version cannot be increased if MachineDeployments are upgrading or not yet on the current version
	if err := validateTopologyMachineDeploymentVersions(ctx, webhook.Client, oldCluster, oldVersion); err != nil {
		allErrs = append(allErrs, err)
	}

	// minor version cannot be increased if MachinePools are upgrading or not yet on the current version
	if err := validateTopologyMachinePoolVersions(ctx, webhook.Client, webhook.ClusterCacheReader, oldCluster, oldVersion); err != nil {
		allErrs = append(allErrs, err)
	}

	if len(allErrs) > 0 {
		return field.Invalid(
			fldPath,
			fldValue,
			fmt.Sprintf("version cannot be changed: %v", kerrors.NewAggregate(allErrs)),
		)
	}

	return nil
}

func validateTopologyControlPlaneVersion(ctx context.Context, ctrlClient client.Reader, oldCluster *clusterv1.Cluster, oldVersion semver.Version) error {
	cp, err := external.GetObjectFromContractVersionedRef(ctx, ctrlClient, oldCluster.Spec.ControlPlaneRef, oldCluster.Namespace)
	if err != nil {
		return errors.Wrap(err, "failed to check if control plane is upgrading: failed to get control plane object")
	}

	cpVersionString, err := contract.ControlPlane().Version().Get(cp)
	if err != nil {
		return errors.Wrap(err, "failed to check if control plane is upgrading: failed to get control plane version")
	}

	cpVersion, err := semver.ParseTolerant(*cpVersionString)
	if err != nil {
		// NOTE: this should never happen. Nevertheless, handling this for extra caution.
		return errors.Wrapf(err, "failed to check if control plane is upgrading: failed to parse control plane version %s", *cpVersionString)
	}
	if cpVersion.String() != oldVersion.String() {
		return fmt.Errorf("Cluster.spec.topology.version %s was not propagated to control plane yet (control plane version %s)", oldVersion, cpVersion) //nolint:staticcheck // capitalization is intentional
	}

	provisioning, err := contract.ControlPlane().IsProvisioning(cp)
	if err != nil {
		return errors.Wrap(err, "failed to check if control plane is provisioning")
	}

	if provisioning {
		return errors.New("control plane is currently provisioning")
	}

	upgrading, err := contract.ControlPlane().IsUpgrading(cp)
	if err != nil {
		return errors.Wrap(err, "failed to check if control plane is upgrading")
	}

	if upgrading {
		return errors.New("control plane is still completing a previous upgrade")
	}

	return nil
}

func validateTopologyMachineDeploymentVersions(ctx context.Context, ctrlClient client.Reader, oldCluster *clusterv1.Cluster, oldVersion semver.Version) error {
	// List all the machine deployments in the current cluster and in a managed topology.
	// FROM: current_state.go getCurrentMachineDeploymentState
	mds := &clusterv1.MachineDeploymentList{}
	err := ctrlClient.List(ctx, mds,
		client.MatchingLabels{
			clusterv1.ClusterNameLabel:          oldCluster.Name,
			clusterv1.ClusterTopologyOwnedLabel: "",
		},
		client.InNamespace(oldCluster.Namespace),
	)
	if err != nil {
		return errors.Wrap(err, "failed to check if MachineDeployments are upgrading: failed to get MachineDeployments")
	}

	if len(mds.Items) == 0 {
		return nil
	}

	mdUpgradingNames := []string{}

	for i := range mds.Items {
		md := &mds.Items[i]

		mdVersion, err := semver.ParseTolerant(md.Spec.Template.Spec.Version)
		if err != nil {
			// NOTE: this should never happen. Nevertheless, handling this for extra caution.
			return errors.Wrapf(err, "failed to check if MachineDeployment %s is upgrading: failed to parse version %s", md.Name, md.Spec.Template.Spec.Version)
		}

		if mdVersion.String() != oldVersion.String() {
			mdUpgradingNames = append(mdUpgradingNames, md.Name)
			continue
		}

		upgrading, err := check.IsMachineDeploymentUpgrading(ctx, ctrlClient, md)
		if err != nil {
			return err
		}
		if upgrading {
			mdUpgradingNames = append(mdUpgradingNames, md.Name)
		}
	}

	if len(mdUpgradingNames) > 0 {
		return fmt.Errorf("there are still MachineDeployments completing a previous upgrade: [%s]", strings.Join(mdUpgradingNames, ", "))
	}

	return nil
}

func validateTopologyMachinePoolVersions(ctx context.Context, ctrlClient client.Reader, clusterCacheReader ClusterCacheReader, oldCluster *clusterv1.Cluster, oldVersion semver.Version) error {
	// List all the machine pools in the current cluster and in a managed topology.
	// FROM: current_state.go getCurrentMachinePoolState
	mps := &clusterv1.MachinePoolList{}
	err := ctrlClient.List(ctx, mps,
		client.MatchingLabels{
			clusterv1.ClusterNameLabel:          oldCluster.Name,
			clusterv1.ClusterTopologyOwnedLabel: "",
		},
		client.InNamespace(oldCluster.Namespace),
	)
	if err != nil {
		return errors.Wrap(err, "failed to check if MachinePools are upgrading: failed to get MachinePools")
	}

	if len(mps.Items) == 0 {
		return nil
	}

	wlClient, err := clusterCacheReader.GetReader(ctx, client.ObjectKeyFromObject(oldCluster))
	if err != nil {
		return errors.Wrap(err, "failed to check if MachinePools are upgrading: unable to get client for workload cluster")
	}

	mpUpgradingNames := []string{}

	for i := range mps.Items {
		mp := &mps.Items[i]

		mpVersion, err := semver.ParseTolerant(mp.Spec.Template.Spec.Version)
		if err != nil {
			// NOTE: this should never happen. Nevertheless, handling this for extra caution.
			return errors.Wrapf(err, "failed to check if MachinePool %s is upgrading: failed to parse version %s", mp.Name, mp.Spec.Template.Spec.Version)
		}

		if mpVersion.String() != oldVersion.String() {
			mpUpgradingNames = append(mpUpgradingNames, mp.Name)
			continue
		}

		upgrading, err := check.IsMachinePoolUpgrading(ctx, wlClient, mp)
		if err != nil {
			return err
		}
		if upgrading {
			mpUpgradingNames = append(mpUpgradingNames, mp.Name)
		}
	}

	if len(mpUpgradingNames) > 0 {
		return fmt.Errorf("there are still MachinePools completing a previous upgrade: [%s]", strings.Join(mpUpgradingNames, ", "))
	}

	return nil
}

func validateClusterRollout(cluster *clusterv1.Cluster) field.ErrorList {
	var allErrs field.ErrorList

	for _, md := range cluster.Spec.Topology.Workers.MachineDeployments {
		fldPath := field.NewPath("spec", "topology", "workers", "machineDeployments").Key(md.Name).Child("rollout")
		allErrs = append(allErrs, validateRolloutStrategy(fldPath.Child("strategy"), md.Rollout.Strategy.RollingUpdate.MaxUnavailable, md.Rollout.Strategy.RollingUpdate.MaxSurge)...)
	}

	return allErrs
}

func validateMachineHealthChecks(cluster *clusterv1.Cluster, clusterClass *clusterv1.ClusterClass) field.ErrorList {
	var allErrs field.ErrorList

	fldPath := field.NewPath("spec", "topology", "controlPlane", "healthCheck")

	// Validate ControlPlane MachineHealthCheck if defined.
	if cluster.Spec.Topology.ControlPlane.HealthCheck.IsDefined() {
		// Ensure ControlPlane does not define a MachineHealthCheck if the ClusterClass does not define MachineInfrastructure.
		if clusterClass.Spec.ControlPlane.MachineInfrastructure == nil {
			allErrs = append(allErrs, field.Forbidden(
				fldPath,
				"can be only set if spec.controlPlane.machineInfrastructure is set in ClusterClass",
			))
		}
		allErrs = append(allErrs, validateMachineHealthCheckNodeStartupTimeoutSeconds(fldPath, cluster.Spec.Topology.ControlPlane.HealthCheck.Checks.NodeStartupTimeoutSeconds)...)
		allErrs = append(allErrs, validateMachineHealthCheckUnhealthyLessThanOrEqualTo(fldPath, cluster.Spec.Topology.ControlPlane.HealthCheck.Remediation.TriggerIf.UnhealthyLessThanOrEqualTo)...)
	}

	// If MachineHealthCheck is explicitly enabled then make sure that a MachineHealthCheck definition is
	// available either in the Cluster topology or in the ClusterClass.
	// (One of these definitions will be used in the controller to create the MachineHealthCheck)

	// Check if the machineHealthCheck is explicitly enabled in the ControlPlaneTopology.
	if cluster.Spec.Topology.ControlPlane.HealthCheck.Enabled != nil && *cluster.Spec.Topology.ControlPlane.HealthCheck.Enabled {
		// Ensure the MHC is defined in at least one of the ControlPlaneTopology of the Cluster or the ControlPlaneClass of the ClusterClass.
		if !cluster.Spec.Topology.ControlPlane.HealthCheck.IsDefined() && !clusterClass.Spec.ControlPlane.HealthCheck.IsDefined() {
			allErrs = append(allErrs, field.Forbidden(
				fldPath.Child("enable"),
				fmt.Sprintf("cannot be set to %t as healthCheck definition is not available in the Cluster topology or the ClusterClass", *cluster.Spec.Topology.ControlPlane.HealthCheck.Enabled),
			))
		}
	}

	for i := range cluster.Spec.Topology.Workers.MachineDeployments {
		md := cluster.Spec.Topology.Workers.MachineDeployments[i]
		fldPath := field.NewPath("spec", "topology", "workers", "machineDeployments").Key(md.Name).Child("healthCheck")

		// Validate the MachineDeployment MachineHealthCheck if defined.
		if md.HealthCheck.IsDefined() {
			allErrs = append(allErrs, validateMachineHealthCheckNodeStartupTimeoutSeconds(fldPath, md.HealthCheck.Checks.NodeStartupTimeoutSeconds)...)
			allErrs = append(allErrs, validateMachineHealthCheckUnhealthyLessThanOrEqualTo(fldPath, md.HealthCheck.Remediation.TriggerIf.UnhealthyLessThanOrEqualTo)...)
			allErrs = append(allErrs, validateRemediationMaxInFlight(fldPath.Child("remediation"), md.HealthCheck.Remediation.MaxInFlight)...)
		}

		// If MachineHealthCheck is explicitly enabled then make sure that a MachineHealthCheck definition is
		// available either in the Cluster topology or in the ClusterClass.
		// (One of these definitions will be used in the controller to create the MachineHealthCheck)
		mdClass := machineDeploymentClassOfName(clusterClass, md.Class)
		if mdClass != nil { // Note: we skip handling the nil case here as it is already handled in previous validations.
			// Check if the machineHealthCheck is explicitly enabled in the machineDeploymentTopology.
			if md.HealthCheck.Enabled != nil && *md.HealthCheck.Enabled {
				// Ensure the MHC is defined in at least one of the MachineDeploymentTopology of the Cluster or the MachineDeploymentClass of the ClusterClass.
				if !md.HealthCheck.IsDefined() && !mdClass.HealthCheck.IsDefined() {
					allErrs = append(allErrs, field.Forbidden(
						fldPath.Child("enable"),
						fmt.Sprintf("cannot be set to %t as healthCheck definition is not available in the Cluster topology or the ClusterClass", *md.HealthCheck.Enabled),
					))
				}
			}
		}
	}

	return allErrs
}

// machineDeploymentClassOfName find a MachineDeploymentClass of the given name in the provided ClusterClass.
// Returns nil if it can not find one.
// TODO: Check if there is already a helper function that can do this.
func machineDeploymentClassOfName(clusterClass *clusterv1.ClusterClass, name string) *clusterv1.MachineDeploymentClass {
	for _, mdClass := range clusterClass.Spec.Workers.MachineDeployments {
		if mdClass.Class == name {
			return &mdClass
		}
	}
	return nil
}

// validateCIDRBlocks ensures the passed CIDR is valid.
func validateCIDRBlocks(fldPath *field.Path, cidrs []string) field.ErrorList {
	var allErrs field.ErrorList
	for i, cidr := range cidrs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Index(i),
				cidr,
				err.Error()))
		}
	}
	return allErrs
}

// DefaultAndValidateVariables defaults and validates variables in the Cluster and MachineDeployment/MachinePool topologies based
// on the definitions in the ClusterClass.
func DefaultAndValidateVariables(ctx context.Context, cluster, oldCluster *clusterv1.Cluster, clusterClass *clusterv1.ClusterClass) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, DefaultVariables(cluster, clusterClass)...)

	// Capture variables from old cluster if it is present to be used in validation for transitions that may be specified
	// via CEL validation rules.
	var (
		oldClusterVariables, oldCPOverrides []clusterv1.ClusterVariable
		oldMDVariables                      map[string][]clusterv1.ClusterVariable
		oldMPVariables                      map[string][]clusterv1.ClusterVariable
	)
	if oldCluster != nil {
		oldClusterVariables = oldCluster.Spec.Topology.Variables
		oldCPOverrides = oldCluster.Spec.Topology.ControlPlane.Variables.Overrides

		oldMDVariables = make(map[string][]clusterv1.ClusterVariable, len(oldCluster.Spec.Topology.Workers.MachineDeployments))
		for _, md := range oldCluster.Spec.Topology.Workers.MachineDeployments {
			oldMDVariables[md.Name] = md.Variables.Overrides
		}

		oldMPVariables = make(map[string][]clusterv1.ClusterVariable, len(oldCluster.Spec.Topology.Workers.MachinePools))
		for _, mp := range oldCluster.Spec.Topology.Workers.MachinePools {
			oldMPVariables[mp.Name] = mp.Variables.Overrides
		}
	}

	// Variables must be validated in the defaulting webhook. Variable definitions are stored in the ClusterClass status
	// and are patched in the ClusterClass reconcile.

	// Validate cluster-wide variables.
	allErrs = append(allErrs, variables.ValidateClusterVariables(
		ctx,
		cluster.Spec.Topology.Variables,
		oldClusterVariables,
		clusterClass.Status.Variables,
		field.NewPath("spec", "topology", "variables"))...)

	// Validate ControlPlane variable overrides.
	if len(cluster.Spec.Topology.ControlPlane.Variables.Overrides) > 0 {
		allErrs = append(allErrs, variables.ValidateControlPlaneVariables(
			ctx,
			cluster.Spec.Topology.ControlPlane.Variables.Overrides,
			oldCPOverrides,
			clusterClass.Status.Variables,
			field.NewPath("spec", "topology", "controlPlane", "variables", "overrides"))...,
		)
	}

	// Validate MachineDeployment variable overrides.
	for _, md := range cluster.Spec.Topology.Workers.MachineDeployments {
		// Continue if there are no variable overrides.
		if len(md.Variables.Overrides) == 0 {
			continue
		}
		allErrs = append(allErrs, variables.ValidateMachineVariables(
			ctx,
			md.Variables.Overrides,
			oldMDVariables[md.Name],
			clusterClass.Status.Variables,
			field.NewPath("spec", "topology", "workers", "machineDeployments").Key(md.Name).Child("variables", "overrides"))...,
		)
	}

	// Validate MachinePool variable overrides.
	for _, mp := range cluster.Spec.Topology.Workers.MachinePools {
		// Continue if there are no variable overrides.
		if len(mp.Variables.Overrides) == 0 {
			continue
		}
		allErrs = append(allErrs, variables.ValidateMachineVariables(
			ctx,
			mp.Variables.Overrides,
			oldMPVariables[mp.Name],
			clusterClass.Status.Variables,
			field.NewPath("spec", "topology", "workers", "machinePools").Key(mp.Name).Child("variables", "overrides"))...,
		)
	}
	return allErrs
}

// DefaultVariables defaults variables in the Cluster based on information in the ClusterClass.
func DefaultVariables(cluster *clusterv1.Cluster, clusterClass *clusterv1.ClusterClass) field.ErrorList {
	var allErrs field.ErrorList
	if cluster == nil {
		return field.ErrorList{field.InternalError(field.NewPath(""), errors.New("Cluster can not be nil"))}
	}
	if clusterClass == nil {
		return field.ErrorList{field.InternalError(field.NewPath(""), errors.New("ClusterClass can not be nil"))}
	}

	// Default cluster-wide variables.
	defaultedVariables, errs := variables.DefaultClusterVariables(cluster.Spec.Topology.Variables, clusterClass.Status.Variables,
		field.NewPath("spec", "topology", "variables"))
	if len(errs) > 0 {
		allErrs = append(allErrs, errs...)
	} else {
		cluster.Spec.Topology.Variables = defaultedVariables
	}

	// Default ControlPlane variable overrides.
	if len(cluster.Spec.Topology.ControlPlane.Variables.Overrides) > 0 {
		defaultedVariables, errs := variables.DefaultMachineVariables(cluster.Spec.Topology.ControlPlane.Variables.Overrides, clusterClass.Status.Variables,
			field.NewPath("spec", "topology", "controlPlane", "variables", "overrides"))
		if len(errs) > 0 {
			allErrs = append(allErrs, errs...)
		} else {
			cluster.Spec.Topology.ControlPlane.Variables.Overrides = defaultedVariables
		}
	}

	// Default MachineDeployment variable overrides.
	for i, md := range cluster.Spec.Topology.Workers.MachineDeployments {
		// Continue if there are no variable overrides.
		if len(md.Variables.Overrides) == 0 {
			continue
		}
		defaultedVariables, errs := variables.DefaultMachineVariables(md.Variables.Overrides, clusterClass.Status.Variables,
			field.NewPath("spec", "topology", "workers", "machineDeployments").Key(md.Name).Child("variables", "overrides"))
		if len(errs) > 0 {
			allErrs = append(allErrs, errs...)
		} else {
			cluster.Spec.Topology.Workers.MachineDeployments[i].Variables.Overrides = defaultedVariables
		}
	}

	// Default MachinePool variable overrides.
	for i, mp := range cluster.Spec.Topology.Workers.MachinePools {
		// Continue if there are no variable overrides.
		if len(mp.Variables.Overrides) == 0 {
			continue
		}
		defaultedVariables, errs := variables.DefaultMachineVariables(mp.Variables.Overrides, clusterClass.Status.Variables,
			field.NewPath("spec", "topology", "workers", "machinePools").Key(mp.Name).Child("variables", "overrides"))
		if len(errs) > 0 {
			allErrs = append(allErrs, errs...)
		} else {
			cluster.Spec.Topology.Workers.MachinePools[i].Variables.Overrides = defaultedVariables
		}
	}
	return allErrs
}

// ValidateClusterForClusterClass uses information in the ClusterClass to validate the Cluster.
func ValidateClusterForClusterClass(cluster *clusterv1.Cluster, clusterClass *clusterv1.ClusterClass) field.ErrorList {
	var allErrs field.ErrorList
	if cluster == nil {
		return field.ErrorList{field.InternalError(field.NewPath(""), errors.New("Cluster can not be nil"))}
	}
	if clusterClass == nil {
		return field.ErrorList{field.InternalError(field.NewPath(""), errors.New("ClusterClass can not be nil"))}
	}
	allErrs = append(allErrs, check.MachineDeploymentTopologiesAreValidAndDefinedInClusterClass(cluster, clusterClass)...)

	allErrs = append(allErrs, check.MachinePoolTopologiesAreValidAndDefinedInClusterClass(cluster, clusterClass)...)

	allErrs = append(allErrs, validateClusterRollout(cluster)...)

	// Validate the MachineHealthChecks defined in the cluster topology.
	allErrs = append(allErrs, validateMachineHealthChecks(cluster, clusterClass)...)
	return allErrs
}

// validateClusterClassExistsAndIsReconciled will try to get the ClusterClass referenced in the Cluster. If it does not exist or is not reconciled it will add a warning.
// In any other case it will return an error.
func (webhook *Cluster) validateClusterClassExistsAndIsReconciled(ctx context.Context, newCluster *clusterv1.Cluster) (*clusterv1.ClusterClass, admission.Warnings, error) {
	var allWarnings admission.Warnings
	clusterClass, clusterClassPollErr := webhook.pollClusterClassForCluster(ctx, newCluster)
	if clusterClassPollErr != nil {
		// Add a warning if the Class does not exist or if it has not been successfully reconciled.
		switch {
		case apierrors.IsNotFound(clusterClassPollErr):
			allWarnings = append(allWarnings,
				fmt.Sprintf(
					"Cluster refers to ClusterClass %s, but this ClusterClass does not exist. "+
						"Cluster topology has not been fully validated. "+
						"The ClusterClass must be created to reconcile the Cluster", newCluster.GetClassKey()),
			)
		case errors.Is(clusterClassPollErr, errClusterClassNotReconciled):
			allWarnings = append(allWarnings,
				fmt.Sprintf(
					"Cluster refers to ClusterClass %s, but this ClusterClass hasn't been successfully reconciled. "+
						"Cluster topology has not been fully validated. "+
						"Please take a look at the ClusterClass status", newCluster.GetClassKey()),
			)
		// If there's any other error return a generic warning with the error message.
		default:
			allWarnings = append(allWarnings,
				fmt.Sprintf(
					"Cluster refers to ClusterClass %s, but this ClusterClass could not be retrieved. "+
						"Cluster topology has not been fully validated: %s", newCluster.GetClassKey(), clusterClassPollErr.Error()),
			)
		}
	}
	return clusterClass, allWarnings, clusterClassPollErr
}

// pollClusterClassForCluster will retry getting the ClusterClass referenced in the Cluster for two seconds.
func (webhook *Cluster) pollClusterClassForCluster(ctx context.Context, cluster *clusterv1.Cluster) (*clusterv1.ClusterClass, error) {
	clusterClass := &clusterv1.ClusterClass{}
	var clusterClassPollErr error
	_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		if clusterClassPollErr = webhook.Client.Get(ctx, cluster.GetClassKey(), clusterClass); clusterClassPollErr != nil {
			return false, nil //nolint:nilerr
		}

		if clusterClassPollErr = clusterClassIsReconciled(clusterClass); clusterClassPollErr != nil {
			return false, nil //nolint:nilerr
		}
		clusterClassPollErr = nil
		return true, nil
	})
	if clusterClassPollErr != nil {
		return nil, clusterClassPollErr
	}
	return clusterClass, nil
}

// clusterClassIsReconciled returns errClusterClassNotReconciled if the ClusterClass has not successfully reconciled or if the
// ClusterClass variables have not been successfully reconciled.
func clusterClassIsReconciled(clusterClass *clusterv1.ClusterClass) error {
	// If the clusterClass metadata generation does not match the status observed generation, the ClusterClass has not been successfully reconciled.
	if clusterClass.Generation != clusterClass.Status.ObservedGeneration {
		return errClusterClassNotReconciled
	}
	// If the clusterClass does not have ClusterClassVariablesReconciled==True, the ClusterClass has not been successfully reconciled.
	if !conditions.Has(clusterClass, clusterv1.ClusterClassVariablesReadyCondition) ||
		conditions.IsFalse(clusterClass, clusterv1.ClusterClassVariablesReadyCondition) {
		return errClusterClassNotReconciled
	}
	return nil
}

func validateTopologyMetadata(topology *clusterv1.Topology, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, topology.ControlPlane.Metadata.Validate(fldPath.Child("controlPlane", "metadata"))...)
	for _, md := range topology.Workers.MachineDeployments {
		allErrs = append(allErrs, md.Metadata.Validate(
			fldPath.Child("workers", "machineDeployments").Key(md.Name).Child("metadata"),
		)...)
	}
	for _, mp := range topology.Workers.MachinePools {
		allErrs = append(allErrs, mp.Metadata.Validate(
			fldPath.Child("workers", "machinePools").Key(mp.Name).Child("metadata"),
		)...)
	}
	return allErrs
}

// validateAutoscalerAnnotationsForCluster iterates the MachineDeploymentsTopology objects under Workers and ensures the replicas
// field and min/max annotations for autoscaler are not set at the same time. Optionally it also checks if a given ClusterClass has
// the annotations that may apply to this Cluster.
func validateAutoscalerAnnotationsForCluster(cluster *clusterv1.Cluster, clusterClass *clusterv1.ClusterClass) field.ErrorList {
	var allErrs field.ErrorList

	if cluster.Spec.Topology == nil {
		return allErrs
	}

	fldPath := field.NewPath("spec", "topology")
	for _, mdt := range cluster.Spec.Topology.Workers.MachineDeployments {
		if mdt.Replicas == nil {
			continue
		}
		for k := range mdt.Metadata.Annotations {
			if k == clusterv1.AutoscalerMinSizeAnnotation || k == clusterv1.AutoscalerMaxSizeAnnotation {
				allErrs = append(
					allErrs,
					field.Invalid(
						fldPath.Child("workers", "machineDeployments").Key(mdt.Name).Child("replicas"),
						mdt.Replicas,
						fmt.Sprintf("cannot be set for cluster %q in namespace %q if the same MachineDeploymentTopology has autoscaler annotations",
							cluster.Name, cluster.Namespace),
					),
				)
				break
			}
		}

		// Find a matching MachineDeploymentClass for this MachineDeploymentTopology and make sure it does not have
		// the autoscaler annotations in its Template. Skip this step entirely if clusterClass is nil.
		if clusterClass == nil {
			continue
		}
		for _, mdc := range clusterClass.Spec.Workers.MachineDeployments {
			if mdc.Class != mdt.Class {
				continue
			}
			for k := range mdc.Metadata.Annotations {
				if k == clusterv1.AutoscalerMinSizeAnnotation || k == clusterv1.AutoscalerMaxSizeAnnotation {
					allErrs = append(
						allErrs,
						field.Invalid(
							fldPath.Child("workers", "machineDeployments").Key(mdt.Name).Child("replicas"),
							mdt.Replicas,
							fmt.Sprintf("cannot be set for cluster %q in namespace %q if the source class %q of this MachineDeploymentTopology has autoscaler annotations",
								cluster.Name, cluster.Namespace, mdt.Class),
						),
					)
					break
				}
			}
		}
	}
	return allErrs
}
