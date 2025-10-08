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

/*
import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
	"sigs.k8s.io/cluster-api/internal/util/hash"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util/annotations"
)

// rolloutRolling implements the logic for rolling a new MachineSet.
func (r *Reconciler) rolloutRolling(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet, templateExists bool) error {
	planner := newRolloutPlanner()
	if err := planner.init(ctx, md, msList, nil, true, templateExists); err != nil {
		return err
	}

	if err := planner.planRolloutRolling(ctx); err != nil {
		return err
	}

	if err := planner.createOrUpdateMachineSets(ctx, r.Client, r.recorder, r.ssaCache); err != nil {
		return err
	}

	newMS := planner.newMS
	oldMSs := planner.oldMSs
	allMSs := append(oldMSs, newMS)

	if err := r.syncDeploymentStatus(allMSs, newMS, md); err != nil {
		return err
	}

	if mdutil.DeploymentComplete(md, &md.Status) {
		if err := r.cleanupDeployment(ctx, oldMSs, md); err != nil {
			return err
		}
	}

	return nil
}

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

// planRolloutRolling determine how to proceed with the rollout when using the RolloutRolling strategy if we are not yet at the desired state.
func (p *rolloutPlanner) planRolloutRolling(ctx context.Context) error {
	// Scale up, if we can.
	if err := p.reconcileNewMachineSet(ctx); err != nil {
		return err
	}

	// Scale down, if we can.
	if err := p.reconcileOldMachineSetsRolloutRolling(ctx); err != nil {
		return err
	}

	// This funcs tries to detect and address the case when a rollout is not making progress because both scaling down and scaling up are blocked.
	// Note. this func must be called after computing scale up/down intent for all the MachineSets.
	// Note. this func only address deadlock due to unavailable machines not getting deleted on oldMSs, e.g. due to a wrong configuration.
	// unblocking deadlock when unavailable machines exists only on oldMSs, is required also because failures on old machines set are not remediated by MHC.
	p.reconcileDeadlockBreaker(ctx)
	return nil
}

// planOnDelete determine how to proceed with the rollout when using the OnDelete strategy if we are not yet at the desired state.
func (p *rolloutPlanner) planOnDelete(ctx context.Context) error {
	// Scale up, if we can.
	if err := p.reconcileNewMachineSet(ctx); err != nil {
		return err
	}

	// Scale down, if we can.
	p.reconcileOldMachineSetsOnDelete(ctx)
	return nil
}

func (p *rolloutPlanner) reconcileNewMachineSet(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)
	allMSs := append(p.oldMSs, p.newMS)

	if *(p.newMS.Spec.Replicas) == *(p.md.Spec.Replicas) {
		// Scaling not required.
		return nil
	}

	if *(p.newMS.Spec.Replicas) > *(p.md.Spec.Replicas) {
		// Scale down.
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for MachineSet %s to %d replicas", p.newMS.Name, *(p.md.Spec.Replicas)), "MachineSet", klog.KObj(p.newMS))
		p.scaleIntents[p.newMS.Name] = *(p.md.Spec.Replicas)
		return nil
	}

	newReplicasCount, err := mdutil.NewMSNewReplicas(p.md, allMSs, *p.newMS.Spec.Replicas)
	if err != nil {
		return err
	}

	if newReplicasCount < *(p.newMS.Spec.Replicas) {
		scaleDownCount := *(p.newMS.Spec.Replicas) - newReplicasCount
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for MachineSet %s to %d replicas (-%d)", p.newMS.Name, newReplicasCount, scaleDownCount), "MachineSet", klog.KObj(p.newMS))
		p.scaleIntents[p.newMS.Name] = newReplicasCount
	}
	if newReplicasCount > *(p.newMS.Spec.Replicas) {
		scaleUpCount := newReplicasCount - *(p.newMS.Spec.Replicas)
		log.V(5).Info(fmt.Sprintf("Setting scale up intent for MachineSet %s to %d replicas (+%d)", p.newMS.Name, newReplicasCount, scaleUpCount), "MachineSet", klog.KObj(p.newMS))
		p.scaleIntents[p.newMS.Name] = newReplicasCount
	}
	return nil
}

func (p *rolloutPlanner) reconcileOldMachineSetsRolloutRolling(ctx context.Context) error {
	allMSs := append(p.oldMSs, p.newMS)

	// no op if there are no replicas on old machinesets
	if mdutil.GetReplicaCountForMachineSets(p.oldMSs) == 0 {
		return nil
	}

	maxUnavailable := mdutil.MaxUnavailable(*p.md)
	minAvailable := max(ptr.Deref(p.md.Spec.Replicas, 0)-maxUnavailable, 0)

	totReplicas := mdutil.GetReplicaCountForMachineSets(allMSs)

	// Find the number of available machines.
	totAvailableReplicas := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(allMSs), 0)

	// Find the number of pending scale down from previous reconcile/from current reconcile;
	// This is required because whenever we are reducing the number of replicas, this operation could further impact availability e.g.
	// - in case of regular rollout, there is no certainty about which machine is going to be deleted (and if this machine is currently available or not):
	// 	 - e.g. MS controller is going to delete first machines with deletion annotation; also MS controller has a slight different notion of unavailable as of now.
	// - in case of in-place rollout, in-place upgrade are always assumed as impacting availability (they can always fail).
	totPendingScaleDown := int32(0)
	for _, ms := range allMSs {
		scaleIntent := ptr.Deref(ms.Spec.Replicas, 0)
		if v, ok := p.scaleIntents[ms.Name]; ok {
			scaleIntent = min(scaleIntent, v)
		}

		// NOTE: we are counting only pending scale down from the current status.replicas (so scale down of actual machines).
		if scaleIntent < ptr.Deref(ms.Status.Replicas, 0) {
			totPendingScaleDown += max(ptr.Deref(ms.Status.Replicas, 0)-scaleIntent, 0)
		}
	}

	// Compute the total number of replicas that can be scaled down.
	// Exit immediately if there is no room for scaling down.
	totalScaleDownCount := max(totReplicas-totPendingScaleDown-minAvailable, 0)
	if totalScaleDownCount <= 0 {
		return nil
	}

	sort.Sort(mdutil.MachineSetsByCreationTimestamp(p.oldMSs))

	// Scale down only unavailable replicas / up to residual totalScaleDownCount.
	// NOTE: we are scaling up unavailable machines first in order to increase chances for the rollout to progress;
	// however, the MS controller might have different opinion on which machines to scale down.
	// As a consequence, the scale down operation must continuously assess if reducing the number of replicas
	// for an older MS could further impact availability under the assumption than any scale down could further impact availability (same as above).
	// if reducing the number of replicase might lead to breaching minAvailable, scale down extent must be limited accordingly.
	totalScaleDownCount, totAvailableReplicas = p.scaleDownOldMSs(ctx, totalScaleDownCount, totAvailableReplicas, minAvailable, false)

	// Then scale down old MS up to zero replicas / up to residual totalScaleDownCount.
	// NOTE: also in this case, continuously assess if reducing the number of replicase could further impact availability,
	// and if necessary, limit scale down extent to ensure the operation respects minAvailable limits.
	_, _ = p.scaleDownOldMSs(ctx, totalScaleDownCount, totAvailableReplicas, minAvailable, true)

	return nil
}

func (p *rolloutPlanner) scaleDownOldMSs(ctx context.Context, totalScaleDownCount, totAvailableReplicas, minAvailable int32, scaleToZero bool) (int32, int32) {
	log := ctrl.LoggerFrom(ctx)

	for _, oldMS := range p.oldMSs {
		// No op if there is no scaling down left.
		if totalScaleDownCount <= 0 {
			break
		}

		// No op if this MS has been already scaled down to zero.
		scaleIntent := ptr.Deref(oldMS.Spec.Replicas, 0)
		if v, ok := p.scaleIntents[oldMS.Name]; ok {
			scaleIntent = min(scaleIntent, v)
		}

		if scaleIntent <= 0 {
			continue
		}

		// Compute the scale down extent by considering either unavailable replicas or, if scaleToZero is set, all replicas.
		// In both cases, scale down is limited to totalScaleDownCount.
		maxScaleDown := max(scaleIntent-ptr.Deref(oldMS.Status.AvailableReplicas, 0), 0)
		if scaleToZero {
			maxScaleDown = scaleIntent
		}
		scaleDown := min(maxScaleDown, totalScaleDownCount)

		// Exit if there is no room for scaling down the MS.
		if scaleDown == 0 {
			continue
		}

		// Before scaling down validate if the operation will lead to a breach to minAvailability
		// In order to do so, consider how many machines will be actually deleted, and consider this operation as impacting availability;
		// if the projected state breaches minAvailability, reduce the scale down extend accordingly.
		availableMachineScaleDown := int32(0)
		if ptr.Deref(oldMS.Status.AvailableReplicas, 0) > 0 {
			newScaleIntent := scaleIntent - scaleDown
			machineScaleDownIntent := max(ptr.Deref(oldMS.Status.Replicas, 0)-newScaleIntent, 0)
			if totAvailableReplicas-machineScaleDownIntent < minAvailable {
				availableMachineScaleDown = max(totAvailableReplicas-minAvailable, 0)
				scaleDown = scaleDown - machineScaleDownIntent + availableMachineScaleDown

				totAvailableReplicas = max(totAvailableReplicas-availableMachineScaleDown, 0)
			}
		}

		if scaleDown > 0 {
			newScaleIntent := max(scaleIntent-scaleDown, 0)
			log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d)", oldMS.Name, newScaleIntent, scaleDown), "machineset", client.ObjectKeyFromObject(oldMS).String())
			p.scaleIntents[oldMS.Name] = newScaleIntent
			totalScaleDownCount = max(totalScaleDownCount-scaleDown, 0)
		}
	}

	return totalScaleDownCount, totAvailableReplicas
}

// This funcs tries to detect and address the case when a rollout is not making progress because both scaling down and scaling up are blocked.
// Note. this func must be called after computing scale up/down intent for all the MachineSets.
// Note. this func only address deadlock due to unavailable machines not getting deleted on oldMSs, e.g. due to a wrong configuration.
// unblocking deadlock when unavailable machines exists only on oldMSs, is required also because failures on old machines set are not remediated by MHC.
func (p *rolloutPlanner) reconcileDeadlockBreaker(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)
	allMSs := append(p.oldMSs, p.newMS)

	// if there are no replicas on the old MS, rollout is completed, no deadlock (actually no rollout in progress).
	if ptr.Deref(mdutil.GetActualReplicaCountForMachineSets(p.oldMSs), 0) == 0 {
		return
	}

	// if all the replicas on OldMS are available, no deadlock (regular scale up newMS and scale down oldMS should take over from here).
	if ptr.Deref(mdutil.GetActualReplicaCountForMachineSets(p.oldMSs), 0) == ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(p.oldMSs), 0) {
		return
	}

	// if there are scale operation in progress, no deadlock.
	// Note: we are considering both scale operation from previous and current reconcile.
	// Note: we are counting only pending scale up & down from the current status.replicas (so actual scale up & down of replicas number, not any other possible "re-alignment" of spec.replicas).
	for _, ms := range allMSs {
		scaleIntent := ptr.Deref(ms.Spec.Replicas, 0)
		if v, ok := p.scaleIntents[ms.Name]; ok {
			scaleIntent = v
		}
		if scaleIntent != ptr.Deref(ms.Status.Replicas, 0) {
			return
		}
	}

	// if there are unavailable replicas on the newMS, wait for them to become available first.
	// Note: a rollout cannot be unblocked if new machines do not become available.
	// Note: if the replicas on the newMS are not going to become available for any issue either:
	// - automatic remediation can help in addressing temporary failures.
	// - user intervention is required to fix more permanent issues e.g. to fix a wrong configuration.
	if ptr.Deref(p.newMS.Status.AvailableReplicas, 0) != ptr.Deref(p.newMS.Status.Replicas, 0) {
		return
	}

	// At this point we can assume there is a deadlock that can be remediated by breaching maxUnavailability constraint
	// and scaling down an oldMS with unavailable machines by one.
	//
	// Note: in most cases this is only a formal violation of maxUnavailability, because there is a good changes
	// that the machine that will be deleted is one of the unavailable machines
	for _, oldMS := range p.oldMSs {
		if ptr.Deref(oldMS.Status.AvailableReplicas, 0) == ptr.Deref(oldMS.Status.Replicas, 0) || ptr.Deref(oldMS.Spec.Replicas, 0) == 0 {
			continue
		}

		newScaleIntent := max(ptr.Deref(oldMS.Spec.Replicas, 0)-1, 0)
		log.Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d) to unblock rollout stuck due to unavailable machine on oldMS only", oldMS.Name, newScaleIntent, 1), "machineset", client.ObjectKeyFromObject(oldMS).String())
		p.scaleIntents[oldMS.Name] = newScaleIntent
		return
	}
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
*/
