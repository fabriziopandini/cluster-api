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
	"sort"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
	"sigs.k8s.io/cluster-api/util"
)

// rolloutRollingUpdate reconcile machine sets controlled by a MachineDeployment that is using the RolloutUpdate strategy.
func (r *Reconciler) rolloutRollingUpdate(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet, templateExists bool) error {
	planner := newRolloutPlanner()
	// FIXME: pass machines
	if err := planner.init(ctx, md, msList, nil, true, templateExists); err != nil {
		return err
	}

	// TODO(in-place): TBD if we want to always prevent machine creation on oldMS.
	if err := planner.planRollingUpdate(ctx); err != nil {
		return err
	}

	if err := r.createOrUpdateMachineSets(ctx, planner); err != nil {
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

// planRollingUpdate determine how to proceed with the rollout when using the RollingUpdate strategy if the system is not yet at the desired state.
func (p *rolloutPlanner) planRollingUpdate(ctx context.Context) error {
	if p.md.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineDeployment %v is nil, this is unexpected", client.ObjectKeyFromObject(p.md))
	}

	if p.newMS.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected", client.ObjectKeyFromObject(p.newMS))
	}

	for _, oldMS := range p.oldMSs {
		if oldMS.Spec.Replicas == nil {
			return errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected", client.ObjectKeyFromObject(oldMS))
		}
	}

	// Adjust the replica count for the newMS after a move operation has been completed.
	p.reconcileReplicasPendingAcknowledgeMove(ctx)

	// Scale up, if we can.
	if err := p.reconcileNewMachineSet(ctx); err != nil {
		return err
	}

	// Scale down, if we can.
	if err := p.reconcileOldMachineSetsRollingUpdate(ctx); err != nil {
		return err
	}

	// Ensures CAPI rollouts changes by performing in-place updates whenever possible.
	if err := p.reconcileInPlaceUpdateIntent(ctx); err != nil {
		return err
	}

	// This func tries to detect and address the case when a rollout is not making progress because both scaling down and scaling up are blocked.
	// Note: This func must be called after computing scale up/down intent for all the MachineSets.
	// Note: This func only addresses deadlocks due to unavailable replicas not getting deleted on oldMSs, which can happen
	// because reconcileOldMachineSetsRollingUpdate called above always assumes the worst case when deleting replicas e.g.
	//  - MD with spec.replicas 3, MaxSurge 1, MaxUnavailable 0
	//  - OldMS with 3 replicas, 2 available replica (and thus 1 unavailable replica)
	//  - NewMS with 1 replica, 1 available replica
	//  - In theory it is possible to scale down oldMS from 3->2 replicas by deleting the unavailable replica.
	//  - However, reconcileOldMachineSetsRollingUpdate cannot assume that the MachineSet controller is going to delete
	//    the unavailable replica when scaling down from 3->2, because it might happen that one of the available replicas
	//    is deleted instead.
	//  - As a consequence reconcileOldMachineSetsRollingUpdate, which assumes the worst case when deleting replicas, did not scaled down oldMS.
	// This situation, rollout not proceeding due to unavailable replicas, is considered a deadlock to be addressed by reconcileDeadlockBreaker.
	// Note: Unblocking deadlocks when unavailable replicas exist only on oldMSs, is required also because replicas on oldMSs are not remediated by MHC.
	p.reconcileDeadlockBreaker(ctx)
	return nil
}

// reconcileReplicasPendingAcknowledgeMove adjust the replica count for the newMS after a move operation has been completed.
// Note: This operation must be performed before computing scale up/down intent for all the MachineSets (so this operation can take into account also moved machines in the current reconcile).
func (p *rolloutPlanner) reconcileReplicasPendingAcknowledgeMove(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)

	// Acknowledge replicas after a move operation.
	// NOTE: pendingMoveAcknowledgeMove annotation from machine (managed by the MS controller) and acknowledgeMove annotation on the newMS (managed by the rollout planner)
	// are used in combination to ensure moved replicas are counted only once by the rollout planner.
	oldAcknowledgeMoveReplicas := sets.Set[string]{}
	if originalMS, ok := p.originalMS[p.newMS.Name]; ok {
		if replicaNames, ok := originalMS.Annotations[acknowledgeMoveAnnotationName]; ok && replicaNames != "" {
			oldAcknowledgeMoveReplicas.Insert(strings.Split(replicaNames, ",")...)
		}
	}
	newAcknowledgeMoveReplicas := sets.Set[string]{}
	totNewAcknowledgeMoveReplicasToScaleUp := int32(0)
	for _, m := range p.machines {
		if !util.IsControlledBy(m, p.newMS, clusterv1.GroupVersion.WithKind("MachineSet").GroupKind()) {
			continue
		}
		if _, ok := m.Annotations[pendingAcknowledgeMoveAnnotationName]; !ok {
			continue
		}
		if !oldAcknowledgeMoveReplicas.Has(m.Name) {
			totNewAcknowledgeMoveReplicasToScaleUp++
		}
		newAcknowledgeMoveReplicas.Insert(m.Name)
	}
	if totNewAcknowledgeMoveReplicasToScaleUp > 0 {
		replicaCount := min(ptr.Deref(p.newMS.Spec.Replicas, 0)+totNewAcknowledgeMoveReplicasToScaleUp, ptr.Deref(p.md.Spec.Replicas, 0))
		scaleUpCount := replicaCount - ptr.Deref(p.newMS.Spec.Replicas, 0)
		// FIXME: check that we apply changes to replica not stored as intent!
		p.newMS.Spec.Replicas = ptr.To(replicaCount)
		log.V(5).Info(fmt.Sprintf("Acknowledge replicas %s moved from an old MachineSet. Scale up MachineSet %s to %d (+%d)", sortAndJoin(newAcknowledgeMoveReplicas.UnsortedList()), p.newMS.Name, replicaCount, scaleUpCount), "MachineSet", klog.KObj(p.newMS))
	}

	// Track the list or replicas for which acknowledgeMove is not yet completed;
	// The MachineSetController will use this info to cleanup the pendingMoveAcknowledgeMove annotation on machines.
	// NOTE: cleanup of the acknowledgeMove annotation will happen automatically as soon as the rollout planner stops
	// to set it, because this annotation is not part of the output of computeDesiredMS
	// (same applies to oldMS, so annotation will always be removed from oldMS).
	if p.newMS.Annotations == nil {
		p.newMS.Annotations = map[string]string{}
	}
	if newAcknowledgeMoveReplicas.Len() > 0 {
		p.newMS.Annotations[acknowledgeMoveAnnotationName] = sortAndJoin(newAcknowledgeMoveReplicas.UnsortedList())
	}
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

func (p *rolloutPlanner) reconcileOldMachineSetsRollingUpdate(ctx context.Context) error {
	allMSs := append(p.oldMSs, p.newMS)

	// no op if there are no replicas on old machinesets
	if mdutil.GetReplicaCountForMachineSets(p.oldMSs) == 0 {
		return nil
	}

	maxUnavailable := mdutil.MaxUnavailable(*p.md)
	minAvailable := max(ptr.Deref(p.md.Spec.Replicas, 0)-maxUnavailable, 0)

	totalSpecReplicas := mdutil.GetReplicaCountForMachineSets(allMSs)

	// Find the total number of available replicas.
	totalAvailableReplicas := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(allMSs), 0)

	// Find the number of pending scale down from previous reconcile/from current reconcile.
	// This is required because whenever the system is reducing the number of replicas, this operation could further impact availability e.g.
	// - in case of regular rollouts, there is no certainty about which replica is going to be deleted (and if the replica being deleted is currently available or not):
	// 	 - e.g. MS controller is going to first delete replicas with deletion annotation; also MS controller has a slightly different notion of unavailable as of now.
	// - in case of in-place rollout, in-place updates are always assumed as impacting availability (they can always fail).
	totalPendingScaleDown := int32(0)
	for _, ms := range allMSs {
		scaleIntent := ptr.Deref(ms.Spec.Replicas, 0)
		if v, ok := p.scaleIntents[ms.Name]; ok {
			scaleIntent = min(scaleIntent, v)
		}

		// NOTE: Count only pending scale down from the current status.replicas (so scale down of existing replicas).
		if scaleIntent < ptr.Deref(ms.Status.Replicas, 0) {
			totalPendingScaleDown += max(ptr.Deref(ms.Status.Replicas, 0)-scaleIntent, 0)
		}
	}

	// Compute the total number of replicas that can be scaled down.
	// Exit immediately if there is no room for scaling down.
	// NOTE: this is a quick preliminary check to verify if there is room for scaling down any of the oldMSs; further down the code
	// will make additional checks to ensure scale down actually happens without breaching MaxUnavailable, and
	// if necessary, it will reduce the extent of the scale down accordingly.
	totalScaleDownCount := max(totalSpecReplicas-totalPendingScaleDown-minAvailable, 0)
	if totalScaleDownCount <= 0 {
		return nil
	}

	// Sort oldMSs so the system will start deleting from the oldest MS first.
	sort.Sort(mdutil.MachineSetsByCreationTimestamp(p.oldMSs))

	// Scale down only unavailable replicas / up to residual totalScaleDownCount.
	// NOTE: The system must scale down unavailable replicas first in order to increase chances for the rollout to progress.
	// However, rollout planner must also take into account the fact that the MS controller might have a different opinion on
	// which replica to delete when a scale down happens.
	//
	// As a consequence, the rollout planner cannot assume a scale down operation deletes an unavailable replica. e.g.
	//  - MD with spec.replicas 3, MaxSurge 1, MaxUnavailable 0
	//  - OldMS with 3 replicas, 2 available replica (and thus 1 unavailable replica)
	//  - NewMS with 1 replica, 1 available replica
	//  - In theory it is possible to scale down oldMS from 3->2 replicas by deleting the unavailable replica.
	//  - However,  rollout planner cannot assume that the MachineSet controller is going to delete
	//    the unavailable replica when scaling down from 3->2, because it might happen that one of the available replicas
	//    is deleted instead.
	//
	// In the example above, the scaleDownOldMSs should not scale down OldMS from 3->2 replicas.
	// In other use cases, e.g. when scaling down an oldMS from 5->3 replicas, the scaleDownOldMSs should limit scale down extent
	// only partially if this doesn't breach MaxUnavailable (e.g. scale down 5->4 instead of 5->3).
	totalScaleDownCount, totalAvailableReplicas = p.scaleDownOldMSs(ctx, totalScaleDownCount, totalAvailableReplicas, minAvailable, true)

	// Then scale down old MS down to zero replicas / down to residual totalScaleDownCount.
	// NOTE: Also in this case, we should continuously assess if reducing the number of replicase could further impact availability,
	// and if necessary, limit scale down extent to ensure the operation respects MaxUnavailable limits.
	_, _ = p.scaleDownOldMSs(ctx, totalScaleDownCount, totalAvailableReplicas, minAvailable, false)

	return nil
}

func (p *rolloutPlanner) scaleDownOldMSs(ctx context.Context, totalScaleDownCount, totalAvailableReplicas, minAvailable int32, scaleDownOnlyUnavailableReplicas bool) (int32, int32) {
	log := ctrl.LoggerFrom(ctx)

	for _, oldMS := range p.oldMSs {
		// No op if there is no scaling down left.
		if totalScaleDownCount <= 0 {
			break
		}

		replicas := ptr.Deref(oldMS.Spec.Replicas, 0)
		if v, ok := p.scaleIntents[oldMS.Name]; ok {
			replicas = v
		}

		// No op if this MS has been already scaled down to zero.
		if replicas <= 0 {
			continue
		}

		// Compute the scale down extent by considering either all replicas or, if scaleDownOnlyUnavailableReplicas is set, unavailable replicas only.
		// In both cases, scale down is limited to totalScaleDownCount.
		// Exit if there is no room for scaling down the MS.
		// NOTE: this is a preliminary check to verify if there is room for scaling down this specific MS; further down the code
		// will make additional checks to ensure scale down actually happens without breaching MaxUnavailable, and
		// if necessary, it will reduce the extent of the scale down accordingly.
		maxScaleDown := replicas
		if scaleDownOnlyUnavailableReplicas {
			maxScaleDown = max(replicas-ptr.Deref(oldMS.Status.AvailableReplicas, 0), 0)
		}
		scaleDown := min(maxScaleDown, totalScaleDownCount)
		if scaleDown == 0 {
			continue
		}

		// Before scaling down validate if the operation will lead to a breach of minAvailability.
		// In order to do so, consider how many existing replicas will be actually deleted, and consider
		// this operation as impacting availability because there are no guarantees that the MS controller is going to
		// delete unavailable replicas first; if the projected state breaches minAvailability, reduce the scale down extend accordingly.

		// If there are no available replicas on this MS, scale down won't impact totalAvailableReplicas at all.
		if ptr.Deref(oldMS.Status.AvailableReplicas, 0) > 0 {
			// If instead there are AvailableReplicas on this MS:
			// compute the new spec.replicas assuming we are going to use the entire scale down extent.
			newSpecReplicas := max(replicas-scaleDown, 0)

			// compute how many existing replicas the operation is going to delete:
			// e.g. if MS is scaling down spec.replicas from 5 to 3, but status.replicas is 4, it is scaling down 1 existing replica.
			// e.g. if MS is scaling down spec.replicas from 5 to 3, but status.replicas is 6, it is scaling down 3 existing replicas.
			existingReplicasToBeDeleted := max(ptr.Deref(oldMS.Status.Replicas, 0)-newSpecReplicas, 0)

			// If we are deleting at least one existing replicas
			if existingReplicasToBeDeleted > 0 {
				// Check if we are scaling down more existing replica than what is allowed by MaxUnavailability.
				if totalAvailableReplicas-minAvailable < existingReplicasToBeDeleted {
					// If we are scaling down more existing replica than what is allowed by MaxUnavailability, then
					// rollout planner must revisit the scale down extent.

					// Determine how many replicas can be deleted overall without further impacting availability.
					maxExistingReplicasThatCanBeDeleted := max(totalAvailableReplicas-minAvailable, 0)

					// Compute the revisited new spec.replicas:
					// e.g. MS spec.replicas 20, scale down 8, newSpecReplicas 12 (20-8), status.replicas 15 -> existingReplicasToBeDeleted 3 (15-12)
					//   assuming that maxExistingReplicasThatCanBeDeleted is 2, newSpecReplicasRevisited should be 15-2 = 13
					// e.g. MS spec.replicas 16, scale down 3, newSpecReplicas 13 (16-3), status.replicas 21 -> existingReplicasToBeDeleted 8 (21-13)
					//   assuming that maxExistingReplicasThatCanBeDeleted is 7, newSpecReplicasRevisited should be 21-7 = 14
					// NOTE: there is a safeguard preventing to go above the initial replicas number (this is scale down oldMS).
					newSpecReplicasRevisited := min(ptr.Deref(oldMS.Status.Replicas, 0)-maxExistingReplicasThatCanBeDeleted, replicas)

					// Re-compute the scale down extent by using newSpecReplicasRevisited.
					scaleDown = max(replicas-newSpecReplicasRevisited, 0)

					// Re-compute how many existing replicas the operation is going to delete by using newSpecReplicasRevisited.
					existingReplicasToBeDeleted = max(ptr.Deref(oldMS.Status.Replicas, 0)-newSpecReplicasRevisited, 0)
				}

				// keep track that we are deleting existing replicas by assuming that this operation
				// will reduce totalAvailableReplicas (worst scenario, deletion of available machines happen first).
				totalAvailableReplicas -= min(ptr.Deref(oldMS.Status.AvailableReplicas, 0), existingReplicasToBeDeleted)
			}
		}

		if scaleDown > 0 {
			newScaleIntent := max(replicas-scaleDown, 0)
			log.V(5).Info(fmt.Sprintf("Setting scale down intent for MachineSet %s to %d replicas (-%d)", oldMS.Name, newScaleIntent, scaleDown), "MachineSet", klog.KObj(oldMS))
			p.scaleIntents[oldMS.Name] = newScaleIntent
			totalScaleDownCount = max(totalScaleDownCount-scaleDown, 0)
		}
	}

	return totalScaleDownCount, totalAvailableReplicas
}

// reconcileInPlaceUpdateIntent ensures CAPI rollouts changes by performing in-place updates whenever possible.
//
// When calling this func, new and old MS already have their scale intent, which was computed under the assumption that
// rollout is going to happen by delete/re-create, and thus it will impact availability.
//
// Also in place updates are assumed to impact availability, even if the in place update technically is not impacting workloads,
// the system must account for scenarios when the operation fails, leading to remediation of the machine/unavailability.
//
// As a consequence:
//   - this function can rely on scale intent previously computed, and just influence how rollout is performed.
//   - unless the user accounts for this unavailability by setting MaxUnavailable >= 1,
//     rollout with in-place will create one additional machine to ensure MaxUnavailable == 0 is respected.
//
// NOTE: if an in-place upgrade is possible and maxSurge is >= 1, creation of additional machines due to maxSurge is capped to 1 or entirely dropped.
// Instead, creation of new machines due to scale up goes through as usual.
func (p *rolloutPlanner) reconcileInPlaceUpdateIntent(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)
	allMSs := append(p.oldMSs, p.newMS)

	// If new MS already has all desired replicas, it does not make sense to perform in-place updates
	// (existing replicas should be deleted).
	if ptr.Deref(p.newMS.Spec.Replicas, 0) >= ptr.Deref(p.md.Spec.Replicas, 0) {
		return nil
	}

	// Find if there are oldMSs for which it possible to perform an in-place update
	inPlaceUpdateCandidates := sets.Set[string]{}
	for _, oldMS := range p.oldMSs {
		// If the oldMS doesn't have replicas anymore, nothing left to do
		if ptr.Deref(oldMS.Status.Replicas, 0) <= 0 {
			continue
		}

		// If the oldMS is not eligible for in place updates, move to next.
		if result, ok := p.oldMSNotUpToDateResults[oldMS.Name]; !ok || !result.EligibleForInPlaceUpdate {
			continue
		}

		canUpdateDecision := p.getCanUpdateDecision(oldMS)
		log.V(5).Info(fmt.Sprintf("CanUpdate decision for MachineSet %s: %t", oldMS.Name, canUpdateDecision), "MachineSet", klog.KObj(p.newMS))

		// drop the candidate if it can't update in place
		if !canUpdateDecision {
			continue
		}

		// Set the annotation tracking that the old MS must move machines to the new MS instead of deleting them.
		// Note: After move is completed, the new MS will take care of the in-place upgrade process.
		// NOTE: Cleanup of the scaleDownMovingToAnnotationName annotation will happen automatically as soon as the rollout planner stops
		// to set it, because this annotation is not part of the output of computeDesiredMS
		// (same applies to newMS, so annotation will always be removed from newMS).
		if oldMS.Annotations == nil {
			oldMS.Annotations = map[string]string{}
		}
		oldMS.Annotations[scaleDownMovingToAnnotationName] = p.newMS.Name
		inPlaceUpdateCandidates.Insert(oldMS.Name)
	}

	// If there are no inPlaceUpdateCandidates, nothing left to do.
	if inPlaceUpdateCandidates.Len() <= 0 {
		return nil
	}

	// Track that the newMS must accept move from in-place candidates. thus allowing a two-ways check before the move operation:
	// 	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
	// NOTE: Cleanup of the acceptReplicasFromAnnotationName annotation will happen automatically as soon as the rollout planner stops
	// to set it, because this annotation is not part of the output of computeDesiredMS
	// (same applies to oldMS, so annotation will always be removed from oldMS).
	if p.newMS.Annotations == nil {
		p.newMS.Annotations = map[string]string{}
	}
	p.newMS.Annotations[acceptReplicasFromAnnotationName] = sortAndJoin(inPlaceUpdateCandidates.UnsortedList())

	// If the newMS is not scaling up, nothing left to do.
	if scaleIntent, ok := p.scaleIntents[p.newMS.Name]; !ok || scaleIntent < ptr.Deref(p.newMS.Spec.Replicas, 0) {
		return nil
	}

	// FIXME: Test and document
	// If the newMS is scaling up while there are still in place updates to be performed,
	// then consider if it is required to revisit the scale up target for the newMS thus deferring creation of new machines due to maxSurge (give priority to in place).
	//
	// Three outcomes are possible:
	// - There are already OldMS scaling down or with then intent of scale down: the newMS should drop the scale up count that can be traced back to maxSurge.
	// - There no OldMS scaling down or with then intent of scale down:
	// 	 - If available replicas(*) is less or equal to minAvailableReplicas, the newMS should drop the scale up count that can be traced back to maxSurge && it is exceeding 1
	//     Note: in this case creating one replicas from MaxSurge is required to allow in place upgrades to (re)start; we should not want to create more than one new machines (give priority to in place).
	// 	 - If there are more available replicas(*) than minAvailableReplicas, the newMS should drop the scale up count that can be traced back to maxSurge;
	// 	   then we should trigger another round of scaleDownOldMachineSetsForRollingUpdate (give priority to in place).
	//
	// (*) available replicas for this computation must include also replicas still in place upgrading because we should give them
	// time to complete the upgrade before resolving to create new machines.
	// If in-place upgrade will fail instead, it will unblock creating of machines after remediation happens.

	hasOldMSScalingDown := false
	for _, oldMS := range p.oldMSs {
		if ptr.Deref(oldMS.Spec.Replicas, 0) < ptr.Deref(oldMS.Status.Replicas, 0) {
			hasOldMSScalingDown = true
			break
		}
		if scaleIntent, ok := p.scaleIntents[oldMS.Name]; ok && scaleIntent < ptr.Deref(oldMS.Spec.Replicas, 0) {
			hasOldMSScalingDown = true
			break
		}
	}

	totUpdatingInPlace := int32(0)
	for _, m := range p.machines {
		if !util.IsControlledBy(m, p.newMS, clusterv1.GroupVersion.WithKind("MachineSet").GroupKind()) {
			continue
		}
		if _, ok := m.Annotations[updatingInPlaceAnnotationName]; !ok {
			continue
		}
		totUpdatingInPlace++
	}

	minAvailableReplicas := ptr.Deref(p.md.Spec.Replicas, 0) - mdutil.MaxUnavailable(*p.md)
	totAvailableReplicas := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(allMSs), 0) + totUpdatingInPlace

	scaleUpCount := p.scaleIntents[p.newMS.Name] - ptr.Deref(p.newMS.Spec.Replicas, 0)
	scaleUpCountWithoutMaxSurge := max(ptr.Deref(p.md.Spec.Replicas, 0)-mdutil.TotalMachineSetsReplicaSum(allMSs), 0)

	// if it is required to revisit the scale up target for the newMS thus deferring creation of new machines due to maxSurge (give priority to in place)
	if scaleUpCount > scaleUpCountWithoutMaxSurge {
		// Scenario 1:
		// There are already OldMS scaling down or with then intent of scale down: the newMS should drop the scale up count that can be traced back to maxSurge.
		newScaleUpCount := scaleUpCountWithoutMaxSurge
		if newScaleUpCount == 0 && !hasOldMSScalingDown && totAvailableReplicas <= minAvailableReplicas {
			// FIXME: test
			// Scenario 2:
			// There no OldMS scaling down or with then intent of scale down and
			// available replicas(*) is less or equal to minAvailableReplicas, the newMS should drop the scale up count that can be traced back to maxSurge && it is exceeding 1
			// Note: in this case creating one replicas from MaxSurge is required to allow in place upgrades to (re)start; we should not want to create more than one new machines (give priority to in place).
			// Note: in case newMS is already scaling up, do not add more.
			if ptr.Deref(p.newMS.Spec.Replicas, 0) <= ptr.Deref(p.newMS.Status.Replicas, 0) {
				newScaleUpCount = 1
			}
		}
		newScaleIntent := ptr.Deref(p.newMS.Spec.Replicas, 0) + newScaleUpCount
		log.V(5).Info(fmt.Sprintf("Revisit scale up intent for MachineSet %s to %d replicas (+%d) to prevent creation of new machines while there are still in place updates to be performed", p.newMS.Name, newScaleIntent, newScaleUpCount), "MachineSet", klog.KObj(p.newMS))
		if newScaleUpCount == 0 {
			// FIXME: test
			delete(p.scaleIntents, p.newMS.Name)
		} else {
			p.scaleIntents[p.newMS.Name] = newScaleIntent
		}

		if !hasOldMSScalingDown && minAvailableReplicas < totAvailableReplicas {
			// FIXME: test
			// Scenario 3:
			// There no OldMS scaling down or with then intent of scale down and
			// there are more available replicas(*) than minAvailableReplicas, the newMS should drop the scale up count that can be traced back to maxSurge;
			// then we should trigger another round of scaleDownOldMachineSetsForRollingUpdate (give priority to in place).
			return p.reconcileOldMachineSetsRollingUpdate(ctx)
		}
	}

	return nil
}

// This funcs tries to detect and address the case when a rollout is not making progress because both scaling down and scaling up are blocked.
// Note: This func must be called after computing scale up/down intent for all the MachineSets.
// Note: This func only address deadlock due to unavailable machines not getting deleted on oldMSs, e.g. due to a wrong configuration.
// Note: Unblocking deadlocks when unavailable replicas exist only on oldMSs, is required also because replicas on oldMSs are not remediated by MHC.
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

	// If there are scale operation in progress, no deadlock.
	// Note: we are considering both scale operation from previous and current reconcile.
	for _, ms := range allMSs {
		if ptr.Deref(ms.Spec.Replicas, 0) != ptr.Deref(ms.Status.Replicas, 0) {
			return
		}
		if _, ok := p.scaleIntents[ms.Name]; ok {
			return
		}
	}

	// if there are unavailable replicas on the newMS, wait for them to become available first.
	// Note: A rollout cannot be unblocked if new machines do not become available.
	// Note: If the replicas on the newMS are not becoming available either:
	// - automatic remediation can help in addressing temporary failures.
	// - user intervention is required to fix more permanent issues e.g. to fix a wrong configuration.
	if ptr.Deref(p.newMS.Status.AvailableReplicas, 0) != ptr.Deref(p.newMS.Status.Replicas, 0) {
		return
	}

	// At this point we can assume there is a deadlock that can only be remediated by breaching maxUnavailability constraint
	// and scaling down an oldMS with unavailable machines by one.
	//
	// Note: In most cases this is only a formal violation of maxUnavailability, because there is a good chance
	// that the machine that will be deleted is one of the unavailable machines.
	// Note: This for loop relies on the same ordering of oldMSs that has been applied by reconcileOldMachineSetsRollingUpdate.
	for _, oldMS := range p.oldMSs {
		if ptr.Deref(oldMS.Status.AvailableReplicas, 0) == ptr.Deref(oldMS.Status.Replicas, 0) || ptr.Deref(oldMS.Spec.Replicas, 0) == 0 {
			continue
		}

		newScaleIntent := max(ptr.Deref(oldMS.Spec.Replicas, 0)-1, 0)
		log.Info(fmt.Sprintf("Setting scale down intent for MachineSet %s to %d replicas (-%d) to unblock rollout stuck due to unavailable Machine on oldMS", oldMS.Name, newScaleIntent, 1), "MachineSet", klog.KObj(oldMS))
		p.scaleIntents[oldMS.Name] = newScaleIntent
		return
	}
}

// scaleDownMovingToAnnotationName is an internal annotation added by the MD controller to the oldMS
// when it should scale down by moving machines that can be update in-place to the newMS instead of deleting them.
// Note: This annotation is used to perform a two-ways check before moving a machine from oldMS to new MS:
//
//	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
const scaleDownMovingToAnnotationName = "internal.cluster.x-k8s.io/scale-down-moving-to"

// acceptReplicasFromAnnotationName is an internal annotation added by the MD controller to the newMS
// when it should accept replicas from an old machines as a first step of an in-place upgrade operation.
// Note: This annotation is used to perform a two-ways check before moving a machine from oldMS to new MS:
//
//	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
const acceptReplicasFromAnnotationName = "internal.cluster.x-k8s.io/accept-replicas-from"

// FIXME: find a better place, discuss with Stefan updatingInPlaceAnnotationName

// pendingAcknowledgeMoveAnnotationName is an internal annotation added by the MS controller to a machine when being
// moved from the oldMS to the newMS. The annotation is removed as soon as the MS controller get the acknowledge from the corresponding MD.
// Note: the annotation is added when reconciling the oldMS, and it is removed when reconciling the newMS.
const pendingAcknowledgeMoveAnnotationName = "internal.cluster.x-k8s.io/pending-acknowledge-move"

// FIXME: once defined, use a target signal for "this machine is upgrading in place".
const updatingInPlaceAnnotationName = "internal.cluster.x-k8s.io/updating-in-place"

// acknowledgeAnnotationName is an internal annotation with a list of machines added by the MD controller
// to a MachineSet when it acknowledges a machine pending acknowled after being moved from an oldMS.
// A machine is dropped from this annotation as soon as pending-acknowledge-move is removed from the machine;
// the annotation is dropped when empty.
// Note: this annotation is used in pair with replicasMachineDeploymentAcknowledgeAnnotation and replicaPendingMachineDeploymentAcknowledgeAnnotation.
const acknowledgeMoveAnnotationName = "internal.cluster.x-k8s.io/acknowledge-move"

func sortAndJoin(a []string) string {
	sort.Strings(a)
	return strings.Join(a, ",")
}
