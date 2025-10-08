/*
Copyright 2018 The Kubernetes Authors.

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

package machinedeployment

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
	"sigs.k8s.io/cluster-api/internal/util/hash"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util/annotations"
)

type rolloutPlanner struct {
	md *clusterv1.MachineDeployment

	originalMS map[string]*clusterv1.MachineSet
	machines   []*clusterv1.Machine

	newMS        *clusterv1.MachineSet
	createReason string

	oldMSs                  []*clusterv1.MachineSet
	oldMSNotUpToDateResults map[string]mdutil.NotUpToDateResult

	scaleIntents     map[string]int32
	computeDesiredMS func(ctx context.Context, deployment *clusterv1.MachineDeployment, currentMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error)
}

func newRolloutPlanner() *rolloutPlanner {
	return &rolloutPlanner{
		scaleIntents:     make(map[string]int32),
		computeDesiredMS: computeDesiredMS,
	}
}

// init rollout planner internal state by taking care of:
//   - Identifying newMS and oldMSs
//   - Create the newMS if it not exists
//   - Compute the initial version of desired state for newMS and oldMSs with mandatory labels, in place propagate fields
//     and the annotations derived from the MachineDeployment.
//
// Note: rollout planner might change desired state later on in the planning phase, e.g. scale up/down replica count
// and add/remove annotations about how to perform those operations.
func (p *rolloutPlanner) init(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet, machines []*clusterv1.Machine, createNewMSIfNotExist bool, mdTemplateExists bool) error {
	if md == nil {
		return errors.New("machineDeployment is nil, this is unexpected")
	}

	if md.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineDeployment %v is nil, this is unexpected", client.ObjectKeyFromObject(p.md))
	}

	for _, ms := range msList {
		if ms.Spec.Replicas == nil {
			return errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected", client.ObjectKeyFromObject(ms))
		}
	}

	// Store md and machines.
	p.md = md
	p.machines = machines

	// Store original MS, for usage later when generating patches.
	p.originalMS = make(map[string]*clusterv1.MachineSet)
	for _, ms := range msList {
		p.originalMS[ms.Name] = ms.DeepCopy()
	}

	// Try to find a MachineSet which matches the MachineDeployments intent, the newMS; consider all the other MachineSets as oldMs.
	// NOTE: Fields propagated in-place from the MD are not considered by the comparison, they are not relevant for the rollout decision.
	// NOTE: Expiration of MD rolloutAfter is relevant for the rollout decision, and thus it is considered in FindNewAndOldMachineSets.
	currentNewMS, oldMSs, oldMSNotUpToDateResults, createReason := mdutil.FindNewAndOldMachineSets(md, msList, metav1.Now())

	// Compute desired state for the old MS, with mandatory labels, fields in-place propagated from the MachineDeployment etc.
	for _, currentOldMS := range oldMSs {
		desiredOldMS, err := p.computeDesiredOldMS(ctx, currentOldMS)
		if err != nil {
			return err
		}
		p.oldMSs = append(p.oldMSs, desiredOldMS)
	}
	p.oldMSNotUpToDateResults = oldMSNotUpToDateResults

	// If there is a current NewMS, compute the desired state for it with mandatory labels, fields in-place propagated from the MachineDeployment etc.
	if currentNewMS != nil {
		desiredNewMS, err := p.computeDesiredNewMS(ctx, currentNewMS)
		if err != nil {
			return err
		}
		p.newMS = desiredNewMS
		return nil
	}

	// If there is no current NewMS, create one if required and possible.
	if !createNewMSIfNotExist {
		return nil
	}

	if !mdTemplateExists {
		return errors.New("cannot create a new MachineSet when templates do not exist")
	}

	// Compute a new MachineSet with mandatory labels, fields in-place propagated from the MachineDeployment etc.
	desiredNewMS, err := p.computeDesiredNewMS(ctx, currentNewMS)
	if err != nil {
		return err
	}
	p.newMS = desiredNewMS
	p.createReason = createReason
	return nil
}

// computeDesiredNewMS with mandatory labels, in place propagate fields and the annotations derived from the MachineDeployment.
// Additionally, this procedure ensure the annotations tracking revisions numbers on the newMS is upToDate.
// Note: because we are using Server-Side-Apply we always have to calculate the full object.
func (p *rolloutPlanner) computeDesiredNewMS(ctx context.Context, currentNewMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
	desiredNewMS, err := p.computeDesiredMS(ctx, p.md, currentNewMS)
	if err != nil {
		return nil, err
	}

	// For newMS, make sure the revision annotation has the highest revision number across all MS + update the revision history annotation accordingly.
	revisionAnnotations, err := mdutil.ComputeRevisionAnnotations(ctx, currentNewMS, p.oldMSs)
	if err != nil {
		return nil, err
	}
	annotations.AddAnnotations(desiredNewMS, revisionAnnotations)
	return desiredNewMS, nil
}

// computeDesiredNewMS with mandatory labels, in place propagate fields and the annotations derived from the MachineDeployment.
// Additionally, this procedure ensure the annotations tracking revisions numbers are carried over.
// Note: because we are using Server-Side-Apply we always have to calculate the full object.
func (p *rolloutPlanner) computeDesiredOldMS(ctx context.Context, currentOldMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
	desiredOldMS, err := p.computeDesiredMS(ctx, p.md, currentOldMS)
	if err != nil {
		return nil, err
	}

	// For oldMS, carry over the revision annotations (those annotations should not be updated for oldMSs).
	revisionAnnotations := mdutil.GetRevisionAnnotations(ctx, currentOldMS)
	annotations.AddAnnotations(desiredOldMS, revisionAnnotations)
	return desiredOldMS, nil
}

// computeDesiredMS computes the desired MachineSet, which could be either a newly created newMS, or the new desired version of an existing newMS/OldMS.
// Note: because we are using Server-Side-Apply we always have to calculate the full object.
func computeDesiredMS(ctx context.Context, deployment *clusterv1.MachineDeployment, currentMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
	var name string
	var uid types.UID
	var finalizers []string
	var uniqueIdentifierLabelValue string
	var machineTemplateSpec clusterv1.MachineSpec
	var status clusterv1.MachineSetStatus
	var replicas int32
	var creationTimestamp metav1.Time

	if currentMS == nil {
		// For a new MachineSet: compute a new uniqueIdentifier, a new MachineSet name, finalizers, replicas and machine template spec (take the one from MachineDeployment)
		// Note: Replicas count might be updated by the rollout planner later in the same reconcile or in following reconcile.

		// Note: In previous Cluster API versions (< v1.4.0), the label value was the hash of the full machine
		// template. Since the introduction of in-place mutation we are ignoring all in-place mutable fields,
		// and using it as a info to be used for building a unique label selector. Instead, the rollout decision
		// is not using the hash anymore.
		templateHash, err := hash.Compute(mdutil.MachineTemplateDeepCopyRolloutFields(&deployment.Spec.Template))
		if err != nil {
			return nil, errors.Wrap(err, "failed to compute desired MachineSet: failed to compute machine template hash")
		}
		// Append a random string at the end of template hash. This is required to distinguish MachineSets that
		// could be created with the same spec as a result of rolloutAfter.
		var randomSuffix string
		name, randomSuffix = computeNewMachineSetName(deployment.Name + "-")
		uniqueIdentifierLabelValue = fmt.Sprintf("%d-%s", templateHash, randomSuffix)
		replicas = 0
		machineTemplateSpec = *deployment.Spec.Template.Spec.DeepCopy()
		creationTimestamp = metav1.NewTime(time.Now())
	} else {
		// For updating an existing MachineSet use name, uid, finalizers, replicas, uniqueIdentifier and machine template spec from existingMS.
		// Note: We use the uid, to ensure that the Server-Side-Apply only updates the existingMS.
		// Note: Replicas count might be updated by the rollout planner later in the same reconcile or in following reconcile.
		var uniqueIdentifierLabelExists bool
		uniqueIdentifierLabelValue, uniqueIdentifierLabelExists = currentMS.Labels[clusterv1.MachineDeploymentUniqueLabel]
		if !uniqueIdentifierLabelExists {
			return nil, errors.Errorf("failed to compute desired MachineSet: failed to get unique identifier from %q annotation",
				clusterv1.MachineDeploymentUniqueLabel)
		}

		name = currentMS.Name
		uid = currentMS.UID
		finalizers = currentMS.Finalizers
		replicas = *currentMS.Spec.Replicas
		machineTemplateSpec = *currentMS.Spec.Template.Spec.DeepCopy()
		status = currentMS.Status
		creationTimestamp = currentMS.CreationTimestamp
	}

	// Construct the basic MachineSet.
	desiredMS := &clusterv1.MachineSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "MachineSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: deployment.Namespace,
			// NOTE: Carry over creationTimestamp from current MS, because it is required by the sorting functions
			// used in the planning phase, e.g. MachineSetsByCreationTimestamp.
			// NOTE: For newMS, this value is set to now, but it will be overridden when actual creation happens
			// NOTE: CreationTimestamp will be dropped from the SSA intent by the SSA helper.
			CreationTimestamp: creationTimestamp,
			// Note: By setting the ownerRef on creation we signal to the MachineSet controller that this is not a stand-alone MachineSet.
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, machineDeploymentKind)},
			UID:             uid,
			Finalizers:      finalizers,
		},
		Spec: clusterv1.MachineSetSpec{
			Replicas:    &replicas,
			ClusterName: deployment.Spec.ClusterName,
			Template: clusterv1.MachineTemplateSpec{
				Spec: machineTemplateSpec,
			},
		},
		// NOTE: Carry over status from current MS, because it is required by mdutil functions
		// used in the planning phase, e.g. GetAvailableReplicaCountForMachineSets.
		// NOTE: Status will be dropped from the SSA intent by the SSA helper.
		Status: status,
	}

	// Set the in-place mutable fields.
	// When we create a new MachineSet we will just create the MachineSet with those fields.
	// When we update an existing MachineSet will we update the fields on the existing MachineSet (in-place mutate).

	// Set labels and .spec.template.labels.
	desiredMS.Labels = mdutil.CloneAndAddLabel(deployment.Spec.Template.Labels,
		clusterv1.MachineDeploymentUniqueLabel, uniqueIdentifierLabelValue)

	// Always set the MachineDeploymentNameLabel.
	// Note: If a client tries to create a MachineDeployment without a selector, the MachineDeployment webhook
	// will add this label automatically. But we want this label to always be present even if the MachineDeployment
	// has a selector which doesn't include it. Therefore, we have to set it here explicitly.
	desiredMS.Labels[clusterv1.MachineDeploymentNameLabel] = deployment.Name
	desiredMS.Spec.Template.Labels = mdutil.CloneAndAddLabel(deployment.Spec.Template.Labels,
		clusterv1.MachineDeploymentUniqueLabel, uniqueIdentifierLabelValue)

	// Set selector.
	desiredMS.Spec.Selector = *mdutil.CloneSelectorAndAddLabel(&deployment.Spec.Selector, clusterv1.MachineDeploymentUniqueLabel, uniqueIdentifierLabelValue)

	// Set annotations and .spec.template.annotations.
	// Note: Additional annotations might be added by the rollout planner later in the same reconcile.
	// Note: Intentionally, we are not setting the following labels:
	// - clusterv1.RevisionAnnotation + the deprecated revisionHistoryAnnotation
	//   - for newMS, we should add keep those annotations upToDate
	//   - for oldMS, we should carry over those annotations from previous reconcile
	// - clusterv1.DisableMachineCreateAnnotation
	//	 - it should be added only on oldMS and if strategy is on delete.
	//   - cleanup of this annotation will happen automatically as soon as the above conditions are not true anymore
	//     (SSA patch will remove this annotation because this annotation is not part of the output of computeDesiredMS / not set by the rollout planner).
	desiredMS.Annotations = mdutil.MachineSetAnnotationsFromMachineDeployment(ctx, deployment)
	desiredMS.Spec.Template.Annotations = cloneStringMap(deployment.Spec.Template.Annotations)

	// Set all other in-place mutable fields.
	desiredMS.Spec.Deletion.Order = deployment.Spec.Deletion.Order
	desiredMS.Spec.MachineNaming = deployment.Spec.MachineNaming
	desiredMS.Spec.Template.Spec.MinReadySeconds = deployment.Spec.Template.Spec.MinReadySeconds
	desiredMS.Spec.Template.Spec.ReadinessGates = deployment.Spec.Template.Spec.ReadinessGates
	desiredMS.Spec.Template.Spec.Deletion.NodeDrainTimeoutSeconds = deployment.Spec.Template.Spec.Deletion.NodeDrainTimeoutSeconds
	desiredMS.Spec.Template.Spec.Deletion.NodeDeletionTimeoutSeconds = deployment.Spec.Template.Spec.Deletion.NodeDeletionTimeoutSeconds
	desiredMS.Spec.Template.Spec.Deletion.NodeVolumeDetachTimeoutSeconds = deployment.Spec.Template.Spec.Deletion.NodeVolumeDetachTimeoutSeconds

	return desiredMS, nil
}

// createOrUpdateMachineSets apply changes identified by the rolloutPlanner to both newMS and oldMSs
// Note: whe the newMS has been created by the rollout planner, also wait for the cache to be up to date.
func (p *rolloutPlanner) createOrUpdateMachineSets(ctx context.Context, c client.Client, recorder record.EventRecorder, ssaCache ssa.Cache) error {
	log := ctrl.LoggerFrom(ctx)
	allMSs := append(p.oldMSs, p.newMS)

	for _, ms := range allMSs {
		log = log.WithValues("MachineSet", klog.KObj(ms))
		ctx = ctrl.LoggerInto(ctx, log)

		if scaleIntent, ok := p.scaleIntents[ms.Name]; ok {
			ms.Spec.Replicas = &scaleIntent
		}

		if ms.GetUID() == "" {
			// Create the MachineSet.
			if err := ssa.Patch(ctx, c, machineDeploymentManagerName, ms); err != nil {
				recorder.Eventf(p.md, corev1.EventTypeWarning, "FailedCreate", "Failed to create MachineSet %s: %v", klog.KObj(ms), err)
				return errors.Wrapf(err, "failed to create new MachineSet %s", klog.KObj(ms))
			}
			log.Info(fmt.Sprintf("MachineSet created (%s)", p.createReason))
			recorder.Eventf(p.md, corev1.EventTypeNormal, "SuccessfulCreate", "Created MachineSet %s", klog.KObj(ms))

			// Keep trying to get the MachineSet. This will force the cache to update and prevent any future reconciliation of
			// the MachineDeployment to reconcile with an outdated list of MachineSets which could lead to unwanted creation of
			// a duplicate MachineSet.
			var pollErrors []error
			tmpMS := &clusterv1.MachineSet{}
			if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
				if err := c.Get(ctx, client.ObjectKeyFromObject(ms), tmpMS); err != nil {
					// Do not return error here. Continue to poll even if we hit an error
					// so that we avoid existing because of transient errors like network flakes.
					// Capture all the errors and return the aggregate error if the poll fails eventually.
					pollErrors = append(pollErrors, err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				return errors.Wrapf(kerrors.NewAggregate(pollErrors), "failed to get the MachineSet %s after creation", klog.KObj(ms))
			}

			// Report back creation timestamp, because sync leverage on this info.
			// TODO(in-place): drop this as soon as sync is moved to the rollout planner
			ms.CreationTimestamp = tmpMS.CreationTimestamp
			continue
		}

		// Update the MachineSet to propagate in-place mutable fields from the MachineDeployment and/or changes applied by the rollout planner.
		originalMS, ok := p.originalMS[ms.Name]
		if !ok {
			return errors.Errorf("failed to update MachineSet %s, original MS is missing", klog.KObj(ms))
		}

		err := ssa.Patch(ctx, c, machineDeploymentManagerName, ms, ssa.WithCachingProxy{Cache: ssaCache, Original: originalMS})
		if err != nil {
			recorder.Eventf(p.md, corev1.EventTypeWarning, "FailedUpdate", "Failed to update MachineSet %s: %v", klog.KObj(ms), err)
			return errors.Wrapf(err, "failed to update MachineSet %s", klog.KObj(ms))
		}

		log.V(4).Info("Updated MachineSet")
	}

	// FIXME: make sure to log changes in replicas + review event generation / compare with before
	return nil
}
