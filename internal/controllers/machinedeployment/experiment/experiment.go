package experiment

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/pointer"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
	"sigs.k8s.io/cluster-api/util"
)

type rolloutPlanner struct {
	getCanUpdateDecision func(oldMS *clusterv1.MachineSet) bool
	scaleIntents         map[string]int32
}

func (p *rolloutPlanner) rolloutRolling(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet, machines []*clusterv1.Machine) ([]*clusterv1.MachineSet, error) {
	newMS, oldMSs, err := p.getAllMachineSetsAndSyncRevision(ctx, md, msList)
	if err != nil {
		return nil, err
	}

	allMSs := append(oldMSs, newMS)

	// FIXME: I'm not recomputing MD status because it looks like nothing in this func depends from this; double check this

	p.scaleIntents = make(map[string]int32)

	p.reconcileReplicasPendingAcknowledgeMove(ctx, md, oldMSs, newMS, machines)

	// FIXME(feedback): how are reconcileNewMachineSet & reconcileOldMachineSets counting Machines that are going through an in-place update
	//  e.g. if they are just counted as available we won't respect maxUnavailable correctly: Answer
	//  => either in-place updating Machines will have Available false
	//  => or we're going to subtract in-place updating Machines from available when doing the calculations

	if err := p.reconcileNewMachineSet(ctx, allMSs, newMS, md); err != nil {
		return nil, err
	}

	if err := p.reconcileOldMachineSets(ctx, allMSs, oldMSs, newMS, md); err != nil {
		return nil, err
	}

	if err := p.reconcileInPlaceUpdateIntent(ctx, allMSs, oldMSs, newMS, md, machines); err != nil {
		return nil, err
	}

	// FIXME: change this to do a patch

	// Apply changes.
	if scaleIntent, ok := p.scaleIntents[newMS.Name]; ok {
		newMS.Spec.Replicas = ptr.To(int32(scaleIntent))
	}
	for _, oldMS := range oldMSs {
		if scaleIntent, ok := p.scaleIntents[oldMS.Name]; ok {
			oldMS.Spec.Replicas = ptr.To(int32(scaleIntent))
		}
	}

	// FIXME: make sure in place annotation are not propagated

	return allMSs, nil
}

func (p *rolloutPlanner) getAllMachineSetsAndSyncRevision(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet) (*clusterv1.MachineSet, []*clusterv1.MachineSet, error) {
	log := ctrl.LoggerFrom(ctx)

	// FIXME: this code is super fake
	var newMS *clusterv1.MachineSet
	oldMSs := make([]*clusterv1.MachineSet, 0, len(msList))
	for _, ms := range msList {
		if ms.Spec.ClusterName == md.Spec.ClusterName { // Note: using ClusterName to track MD revision and detect MD changes
			newMS = ms
			continue
		}
		oldMSs = append(oldMSs, ms)
	}
	if newMS == nil {
		newMS = &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ms%d", len(msList)+1), // Note: this assumes that we are not deleting MS (re-using names would be confusing)
			},
			Spec: clusterv1.MachineSetSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: md.Spec.ClusterName,
				// Note: without this the newMS.Spec.Replicas == nil will fail
				Replicas: pointer.Int32Ptr(0),
			},
		}

		log.V(5).Info(fmt.Sprintf("Creating %s", newMS.Name))
	}
	return newMS, oldMSs, nil
}

func (p *rolloutPlanner) reconcileNewMachineSet(ctx context.Context, allMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, md *clusterv1.MachineDeployment) error {
	// FIXME: cleanupDisableMachineCreateAnnotation
	log := ctrl.LoggerFrom(ctx)

	if md.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineDeployment %v is nil, this is unexpected", client.ObjectKeyFromObject(md))
	}

	if newMS.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected", client.ObjectKeyFromObject(newMS))
	}

	if *(newMS.Spec.Replicas) == *(md.Spec.Replicas) {
		// Scaling not required.
		return nil
	}

	if *(newMS.Spec.Replicas) > *(md.Spec.Replicas) {
		// Scale down.
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas", newMS.Name, *(md.Spec.Replicas)))
		p.scaleIntents[newMS.Name] = *(md.Spec.Replicas)
		return nil
	}

	newReplicasCount, err := mdutil.NewMSNewReplicas(md, allMSs, *newMS.Spec.Replicas)
	if err != nil {
		return err
	}

	if newReplicasCount < *(newMS.Spec.Replicas) {
		scaleDownCount := *(newMS.Spec.Replicas) - newReplicasCount
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d)", newMS.Name, newReplicasCount, scaleDownCount))
		p.scaleIntents[newMS.Name] = newReplicasCount
	}
	if newReplicasCount > *(newMS.Spec.Replicas) {
		scaleUpCount := newReplicasCount - *(newMS.Spec.Replicas)
		log.V(5).Info(fmt.Sprintf("Setting scale up intent for %s to %d replicas (+%d)", newMS.Name, newReplicasCount, scaleUpCount))
		p.scaleIntents[newMS.Name] = newReplicasCount
	}
	return nil
}

func (p *rolloutPlanner) reconcileOldMachineSets(ctx context.Context, allMSs []*clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, md *clusterv1.MachineDeployment) error {
	// FIXME: Log?

	if md.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineDeployment %v is nil, this is unexpected",
			client.ObjectKeyFromObject(md))
	}

	if newMS.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected",
			client.ObjectKeyFromObject(newMS))
	}

	oldMachinesCount := mdutil.GetReplicaCountForMachineSets(oldMSs)
	if oldMachinesCount == 0 {
		// Can't scale down further
		return nil
	}

	allMachinesCount := mdutil.GetReplicaCountForMachineSets(allMSs)
	maxUnavailable := mdutil.MaxUnavailable(*md)

	// Check if we can scale down. We can scale down in the following 2 cases:
	// * Some old MachineSets have unhealthy replicas, we could safely scale down those unhealthy replicas since that won't further
	//  increase unavailability.
	// * New MachineSet has scaled up and it's replicas becomes ready, then we can scale down old MachineSets in a further step.
	//
	// maxScaledDown := allMachinesCount - minAvailable - newMachineSetMachinesUnavailable
	// take into account not only maxUnavailable and any surge machines that have been created, but also unavailable machines from
	// the newMS, so that the unavailable machines from the newMS would not make us scale down old MachineSets in a further
	// step(that will increase unavailability).
	//
	// Concrete example:
	//
	// * 10 replicas
	// * 2 maxUnavailable (absolute number, not percent)
	// * 3 maxSurge (absolute number, not percent)
	//
	// case 1:
	// * Deployment is updated, newMS is created with 3 replicas, oldMS is scaled down to 8, and newMS is scaled up to 5.
	// * The new MachineSet machines crashloop and never become available.
	// * allMachinesCount is 13. minAvailable is 8. newMSMachinesUnavailable is 5.
	// * A node fails and causes one of the oldMS machines to become unavailable. However, 13 - 8 - 5 = 0, so the oldMS won't be scaled down.
	// * The user notices the crashloop and does kubectl rollout undo to rollback.
	// * newMSMachinesUnavailable is 1, since we rolled back to the good MachineSet, so maxScaledDown = 13 - 8 - 1 = 4. 4 of the crashlooping machines will be scaled down.
	// * The total number of machines will then be 9 and the newMS can be scaled up to 10.
	//
	// case 2:
	// Same example, but pushing a new machine template instead of rolling back (aka "roll over"):
	// * The new MachineSet created must start with 0 replicas because allMachinesCount is already at 13.
	// * However, newMSMachinesUnavailable would also be 0, so the 2 old MachineSets could be scaled down by 5 (13 - 8 - 0), which would then
	// allow the new MachineSet to be scaled up by 5.
	availableReplicas := ptr.Deref(newMS.Status.AvailableReplicas, 0)

	minAvailable := *(md.Spec.Replicas) - maxUnavailable
	newMSUnavailableMachineCount := *(newMS.Spec.Replicas) - availableReplicas
	maxScaledDown := allMachinesCount - minAvailable - newMSUnavailableMachineCount
	if maxScaledDown <= 0 {
		return nil
	}

	// Clean up unhealthy replicas first, otherwise unhealthy replicas will block deployment
	// and cause timeout. See https://github.com/kubernetes/kubernetes/issues/16737
	// FIXME: cleanupUnhealthyReplicas
	//  Dig into this (it looks like it doesn't respect MaxUnavailable, but most probably this is intentional)

	// Scale down old MachineSets, need check maxUnavailable to ensure we can scale down
	allMSs = oldMSs
	allMSs = append(allMSs, newMS)
	_, err := p.scaleDownOldMachineSetsForRollingUpdate(ctx, allMSs, oldMSs, md)
	if err != nil {
		return err
	}

	return nil
}

func (p *rolloutPlanner) scaleDownOldMachineSetsForRollingUpdate(ctx context.Context, allMSs []*clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, md *clusterv1.MachineDeployment) (int32, error) {
	log := ctrl.LoggerFrom(ctx)

	if md.Spec.Replicas == nil {
		return 0, errors.Errorf("spec.replicas for MachineDeployment %v is nil, this is unexpected", client.ObjectKeyFromObject(md))
	}

	maxUnavailable := mdutil.MaxUnavailable(*md)
	minAvailable := *(md.Spec.Replicas) - maxUnavailable

	// Find the number of available machines.
	availableMachineCount := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(allMSs), 0)

	// Check if we can scale down.
	if availableMachineCount <= minAvailable {
		// Cannot scale down.
		return 0, nil
	}

	sort.Sort(mdutil.MachineSetsByCreationTimestamp(oldMSs))

	totalScaledDown := int32(0)
	totalScaleDownCount := availableMachineCount - minAvailable
	for _, targetMS := range oldMSs {
		if targetMS.Spec.Replicas == nil {
			return 0, errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected", client.ObjectKeyFromObject(targetMS))
		}

		if totalScaledDown >= totalScaleDownCount {
			// No further scaling required.
			break
		}

		if *(targetMS.Spec.Replicas) == 0 {
			// cannot scale down this MachineSet.
			continue
		}

		// Scale down.
		scaleDownCount := min(*(targetMS.Spec.Replicas), totalScaleDownCount-totalScaledDown)
		newReplicasCount := *(targetMS.Spec.Replicas) - scaleDownCount
		if newReplicasCount > *(targetMS.Spec.Replicas) {
			return totalScaledDown, errors.Errorf("when scaling down old MachineSet, got invalid request to scale down %v: %d -> %d",
				client.ObjectKeyFromObject(targetMS), *(targetMS.Spec.Replicas), newReplicasCount)
		}

		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d)", targetMS.Name, newReplicasCount, scaleDownCount))
		p.scaleIntents[targetMS.Name] = newReplicasCount
		totalScaledDown += scaleDownCount
	}

	return totalScaledDown, nil
}

// reconcileReplicasPendingAcknowledgeMove adjust the replica count for the newMS after a move operation has been completed.
// Note: This operation must be performed before computing scale up/down for all the MachineSets (so this operation can take into account also moved machines in the current reconcile).
func (p *rolloutPlanner) reconcileReplicasPendingAcknowledgeMove(ctx context.Context, md *clusterv1.MachineDeployment, oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, machines []*clusterv1.Machine) {
	log := ctrl.LoggerFrom(ctx)

	// Cleanup the acknowledgeMove annotation from oldMS to handle the case newMS was different in previous reconcile.
	// Note: we must preserve acknowledgeMove annotation in the newMS to avoid double accounting of moved replicas.
	// this is not an issue because the annotation is recomputed entirely overwritten at every reconcile after achieving this goal;
	// Recomputing and overwriting the annotation at every reconcile also ensures cleanup or replicas after pendingAcknowledgeMove
	// annotation is removed from machine, and also cleanup for replicas deleted out of band or other race conditions making the
	// previous list of replicas outdated.
	for _, oldMS := range oldMSs {
		delete(oldMS.Annotations, acknowledgeMoveAnnotationName)
	}

	// Acknowledge replicas after a move operation.
	// NOTE: pendingMoveAcknowledgeMove annotation from machine (managed by the MS controller) and acknowledgeMove annotation on the newMS (managed by the rollout planner)
	// are used in combination to ensure moved replicas are counted only once by the rollout planner.
	oldAcknowledgeMoveReplicas := getAcknowledgeMoveMachines(newMS)
	newAcknowledgeMoveReplicas := sets.Set[string]{}
	totNewAcknowledgeMoveReplicasToScaleUp := int32(0)
	for _, m := range machines {
		if !util.IsControlledBy(m, newMS, clusterv1.GroupVersion.WithKind("MachineSet").GroupKind()) {
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
		replicaCount := min(ptr.Deref(newMS.Spec.Replicas, 0)+totNewAcknowledgeMoveReplicasToScaleUp, ptr.Deref(md.Spec.Replicas, 0))
		scaleUpCount := replicaCount - ptr.Deref(newMS.Spec.Replicas, 0)
		newMS.Spec.Replicas = ptr.To(replicaCount)
		log.V(5).Info(fmt.Sprintf("Acknowledge replicas %s moved from an old MachineSet. Scale up %s to %d (+%d)", sortAndJoin(newAcknowledgeMoveReplicas.UnsortedList()), newMS.Name, replicaCount, scaleUpCount))
	}
	setAcknowledgeMachines(newMS, newAcknowledgeMoveReplicas.UnsortedList()...)
}

// reconcileInPlaceUpdateIntent ensures CAPI rollouts changes by performing in-place updates whenever possible.
//
// When calling this func, new and old MS have their scale intent, which was computed under the assumption that
// rollout is going to happen by delete/re-create, and thus it will impact availability.
//
// Also in place updates ares assumed to impact availability (even if the in place update technically is not impacting workloads,
// the system must account for scenarios when the operation fails, leading to remediation of the machine/unavailability).
// As a consequence, unless the user accounts for this unavailability by setting MaxUnavailable >= 1,
// rollout with in-place will create one additional machine to ensure MaxUnavailable == 0 is respected.
//
// NOTE: if an in-place upgrade is possible and maxSurge is >= 1, creation of additional machines due to maxSurge is capped to 1 or entirely dropped.
// Instead, creation of new machines due to scale up goes through as usual.
func (p *rolloutPlanner) reconcileInPlaceUpdateIntent(ctx context.Context, allMSs []*clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, md *clusterv1.MachineDeployment, machines []*clusterv1.Machine) error {
	log := ctrl.LoggerFrom(ctx)

	// TODO move comment a func level, comment about priority on in place

	// Cleanup acknowledgeMoveAnnotation from previous reconcile.
	// Note: Cleanup account also for the case when newMS was different in previous reconcile.
	delete(newMS.Annotations, acceptReplicasFromAnnotationName)
	delete(newMS.Annotations, scaleDownMovingToAnnotationName)
	for _, oldMS := range oldMSs {
		delete(oldMS.Annotations, acceptReplicasFromAnnotationName)
		delete(oldMS.Annotations, scaleDownMovingToAnnotationName)
	}

	// If new MS already has all desired replicas, it does not make sense to perform in-place updates
	// (existing replicas should be deleted).
	if ptr.Deref(newMS.Spec.Replicas, 0) >= ptr.Deref(md.Spec.Replicas, 0) {
		return nil
	}

	// Find if there are oldMSs for which it possible to perform an in-place update
	inPlaceUpdateCandidates := sets.Set[string]{}
	for _, oldMS := range oldMSs {
		// If the oldMS doesn't have replicas anymore, nothing left to do
		if ptr.Deref(oldMS.Status.Replicas, 0) <= 0 {
			continue
		}

		// FIXME: Think about how to propagate all the info required for the canUpdate from getAllMachineSetsAndSyncRevision to here.
		// TODO: Possible optimization, if a move to the same target is already in progress, do not ask again
		canUpdateDecision := p.getCanUpdateDecision(oldMS)
		log.V(5).Info(fmt.Sprintf("CanUpdate decision for %s: %t", oldMS.Name, canUpdateDecision))

		// drop the candidate if it can't update in place
		if !canUpdateDecision {
			continue
		}

		// Set the annotation tracking that the old MS must move machines to the new MS instead of deleting them.
		// Note: After move is completed, the new MS will take care of the in-place upgrade process.
		setScaleDownMovingTo(oldMS, newMS.Name)
		inPlaceUpdateCandidates.Insert(oldMS.Name)
	}

	// If there are no inPlaceUpdateCandidates, nothing left to do.
	if inPlaceUpdateCandidates.Len() <= 0 {
		return nil
	}

	// Track that the newMS must accept move from in-place candidates. thus allowing a two-ways check before the move operation:
	// 	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
	setAcceptReplicasFromMachineSets(newMS, inPlaceUpdateCandidates.UnsortedList()...)

	// If the newMS is not scaling up, nothing left to do.
	if scaleIntent, ok := p.scaleIntents[newMS.Name]; !ok || scaleIntent < ptr.Deref(newMS.Spec.Replicas, 0) {
		return nil
	}

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
	for _, oldMS := range oldMSs {
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
	for _, m := range machines {
		if !util.IsControlledBy(m, newMS, clusterv1.GroupVersion.WithKind("MachineSet").GroupKind()) {
			continue
		}
		if _, ok := m.Annotations[updatingInPlaceAnnotationName]; !ok {
			continue
		}
		totUpdatingInPlace++
	}

	minAvailableReplicas := ptr.Deref(md.Spec.Replicas, 0) - mdutil.MaxUnavailable(*md)
	totAvailableReplicas := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(allMSs), 0) + totUpdatingInPlace

	scaleUpCount := p.scaleIntents[newMS.Name] - ptr.Deref(newMS.Spec.Replicas, 0)
	scaleUpCountWithoutMaxSurge := max(ptr.Deref(md.Spec.Replicas, 0)-mdutil.TotalMachineSetsReplicaSum(allMSs), 0)

	// if it is required to revisit the scale up target for the newMS thus deferring creation of new machines due to maxSurge (give priority to in place)
	if scaleUpCount > scaleUpCountWithoutMaxSurge {
		// Scenario 1:
		// There are already OldMS scaling down or with then intent of scale down: the newMS should drop the scale up count that can be traced back to maxSurge.
		newScaleUpCount := scaleUpCountWithoutMaxSurge
		if newScaleUpCount == 0 && !hasOldMSScalingDown && totAvailableReplicas <= minAvailableReplicas {
			// Scenario 2:
			// There no OldMS scaling down or with then intent of scale down and
			// available replicas(*) is less or equal to minAvailableReplicas, the newMS should drop the scale up count that can be traced back to maxSurge && it is exceeding 1
			// Note: in this case creating one replicas from MaxSurge is required to allow in place upgrades to (re)start; we should not want to create more than one new machines (give priority to in place).
			newScaleUpCount = 1
		}
		newScaleIntent := ptr.Deref(newMS.Spec.Replicas, 0) + newScaleUpCount
		log.V(5).Info(fmt.Sprintf("Revisit scale up intent for %s to %d replicas (+%d) to prevent creation of new machines while there are still in place updates to be performed", newMS.Name, newScaleIntent, newScaleUpCount))
		if newScaleUpCount == 0 {
			delete(p.scaleIntents, newMS.Name)
		} else {
			p.scaleIntents[newMS.Name] = newScaleIntent
		}

		if !hasOldMSScalingDown && minAvailableReplicas < totAvailableReplicas {
			// Scenario 3:
			// There no OldMS scaling down or with then intent of scale down and
			// there are more available replicas(*) than minAvailableReplicas, the newMS should drop the scale up count that can be traced back to maxSurge;
			// then we should trigger another round of scaleDownOldMachineSetsForRollingUpdate (give priority to in place).
			return p.reconcileOldMachineSets(ctx, allMSs, oldMSs, newMS, md)
		}
	}

	// FIXME(feedback) Let's talk about old/new MS controller race conditions
	//  * Wondering about scenarios where the move & accept annotations on MSs change over time and MS controllers might still act on the old annotations
	return nil
}

// scaleDownMovingToAnnotationName is an internal annotation added by the MD controller to the oldMS
// when it should scale down by moving machines that can be update in-place to the newMS instead of deleting them.
// Note: This annotation is used to perform a two-ways check before moving a machine from oldMS to new MS:
//
//	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
const scaleDownMovingToAnnotationName = "internal.cluster.x-k8s.io/scale-down-moving-to"

func setScaleDownMovingTo(ms *clusterv1.MachineSet, targetMS string) {
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}
	ms.Annotations[scaleDownMovingToAnnotationName] = targetMS
}

// acceptReplicasFromAnnotationName is an internal annotation added by the MD controller to the newMS
// when it should accept replicas from an old machines as a first step of an in-place upgrade operation.
// Note: This annotation is used to perform a two-ways check before moving a machine from oldMS to new MS:
//
//	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
const acceptReplicasFromAnnotationName = "internal.cluster.x-k8s.io/accept-replicas-from"

func setAcceptReplicasFromMachineSets(ms *clusterv1.MachineSet, machineSets ...string) {
	if len(machineSets) == 0 {
		delete(ms.Annotations, acceptReplicasFromAnnotationName)
		return
	}
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}
	ms.Annotations[acceptReplicasFromAnnotationName] = sortAndJoin(machineSets)
}

// pendingAcknowledgeMoveAnnotationName is an internal annotation added by the MS controller to a machine when being
// moved from the oldMS to the newMS. The annotation is removed as soon as the MS controller get the acknowledge from the corresponding MD.
// Note: the annotation is added when reconciling the oldMS, and it is removed when reconciling the newMS:
const pendingAcknowledgeMoveAnnotationName = "internal.cluster.x-k8s.io/pending-acknowledge-move"

// FIXME: once defined, use a target signal for "this machine is upgrading in place".
const updatingInPlaceAnnotationName = "internal.cluster.x-k8s.io/updating-in-place"

// acknowledgeAnnotationName is an internal annotation with a list of machines added by the MD controller
// to a MachineSet when it acknowledges a machine pending acknowled after being moved from an oldMS.
// A machine is dropped from this annotation as soon as pending-acknowledge-move is removed from the machine;
// the annotation is dropped when empty.
// Note: this annotation is used in pair with replicasMachineDeploymentAcknowledgeAnnotation and replicaPendingMachineDeploymentAcknowledgeAnnotation.
const acknowledgeMoveAnnotationName = "internal.cluster.x-k8s.io/acknowledge-move"

func getAcknowledgeMoveMachines(ms *clusterv1.MachineSet) sets.Set[string] {
	sourcesSet := sets.Set[string]{}
	currentList := ms.Annotations[acknowledgeMoveAnnotationName]
	if currentList != "" {
		sourcesSet.Insert(strings.Split(currentList, ",")...)
	}
	return sourcesSet
}

func setAcknowledgeMachines(ms *clusterv1.MachineSet, machines ...string) {
	if len(machines) == 0 {
		delete(ms.Annotations, acknowledgeMoveAnnotationName)
		return
	}
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}
	ms.Annotations[acknowledgeMoveAnnotationName] = sortAndJoin(machines)
}

func sortAndJoin(a []string) string {
	sort.Strings(a)
	return strings.Join(a, ",")
}
