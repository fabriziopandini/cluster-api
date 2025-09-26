package experiment

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
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
	newMS, oldMSs := p.getAllMachineSetsAndSyncRevision(ctx, md, msList)

	allMSs := append(oldMSs, newMS)

	// FIXME: I'm not recomputing MD status because it looks like nothing in this func depends from this; double check this

	p.scaleIntents = make(map[string]int32)

	p.reconcileReplicasPendingAcknowledgeMove(ctx, md, newMS, oldMSs, machines)

	// FIXME(feedback): how are reconcileNewMachineSet & reconcileOldMachineSets counting Machines that are going through an in-place update
	//  e.g. if they are just counted as available we won't respect maxUnavailable correctly: Answer
	//  => either in-place updating Machines will have Available false
	//  => or we're going to subtract in-place updating Machines from available when doing the calculations

	if err := p.reconcileNewMachineSet(ctx, md, newMS, oldMSs); err != nil {
		return nil, err
	}

	if err := p.reconcileOldMachineSets(ctx, md, newMS, oldMSs); err != nil {
		return nil, err
	}

	if err := p.reconcileInPlaceUpdateIntent(ctx, md, newMS, oldMSs, machines); err != nil {
		return nil, err
	}

	// This funcs tries to detect and address the case when a rollout is not making progress because both scaling down and scaling up are blocked.
	// Note. this func must be called after computing scale up/down intent for all the MachineSets.
	// Note. this func only address deadlock due to unavailable machines not getting deleted on oldMSs, e.g. due to a wrong configuration.
	// unblocking deadlock when unavailable machines exists only on oldMSs, is required also because failures on old machines set are not remediated by MHC.
	p.reconcileDeadlockBreaker(ctx, newMS, oldMSs)

	// FIXME: change this to do a patch
	// FIXME: make sure in place annotation are not propagated

	// Apply changes.
	if scaleIntent, ok := p.scaleIntents[newMS.Name]; ok {
		newMS.Spec.Replicas = ptr.To(scaleIntent)
	}
	for _, oldMS := range oldMSs {
		if scaleIntent, ok := p.scaleIntents[oldMS.Name]; ok {
			oldMS.Spec.Replicas = ptr.To(scaleIntent)
		}
	}
	return allMSs, nil
}

func (p *rolloutPlanner) getAllMachineSetsAndSyncRevision(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet) (*clusterv1.MachineSet, []*clusterv1.MachineSet) {
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
				Replicas: ptr.To(int32(0)),
			},
		}

		log.V(5).Info(fmt.Sprintf("Creating %s", newMS.Name), "machineset", client.ObjectKeyFromObject(newMS).String())
	}
	return newMS, oldMSs
}

// reconcileReplicasPendingAcknowledgeMove adjust the replica count for the newMS after a move operation has been completed.
// Note: This operation must be performed before computing scale up/down intent for all the MachineSets (so this operation can take into account also moved machines in the current reconcile).
func (p *rolloutPlanner) reconcileReplicasPendingAcknowledgeMove(ctx context.Context, md *clusterv1.MachineDeployment, newMS *clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, machines []*clusterv1.Machine) {
	log := ctrl.LoggerFrom(ctx)

	// Cleanup the acknowledgeMove annotation from oldMS to handle the case newMS was different in previous reconcile.
	// Note: we must preserve acknowledgeMove annotation in the newMS to avoid double accounting of moved replicas.
	// However, the annotation is recomputed from scratch at every reconcile and overwritten once accounting of moved replicas is completed;
	// by recomputing the annotation we perform cleanup of replica names after pendingAcknowledgeMove annotation is removed from machine,
	// as well as cleanup for replicas deleted out of band or other race conditions making the previous list of replica names outdated.
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
		log.V(5).Info(fmt.Sprintf("Acknowledge replicas %s moved from an old MachineSet. Scale up %s to %d (+%d)", sortAndJoin(newAcknowledgeMoveReplicas.UnsortedList()), newMS.Name, replicaCount, scaleUpCount), "machineset", client.ObjectKeyFromObject(newMS).String())
	}
	setAcknowledgeMachines(newMS, newAcknowledgeMoveReplicas.UnsortedList()...)
}

func (p *rolloutPlanner) reconcileNewMachineSet(ctx context.Context, md *clusterv1.MachineDeployment, newMS *clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet) error {
	// FIXME: cleanupDisableMachineCreateAnnotation

	log := ctrl.LoggerFrom(ctx)
	allMSs := append(oldMSs, newMS)

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
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas", newMS.Name, *(md.Spec.Replicas)), "machineset", client.ObjectKeyFromObject(newMS).String())
		p.scaleIntents[newMS.Name] = *(md.Spec.Replicas)
		return nil
	}

	newReplicasCount, err := mdutil.NewMSNewReplicas(md, allMSs, *newMS.Spec.Replicas)
	if err != nil {
		return err
	}

	if newReplicasCount < *(newMS.Spec.Replicas) {
		scaleDownCount := *(newMS.Spec.Replicas) - newReplicasCount
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d)", newMS.Name, newReplicasCount, scaleDownCount), "machineset", client.ObjectKeyFromObject(newMS).String())
		p.scaleIntents[newMS.Name] = newReplicasCount
	}
	if newReplicasCount > *(newMS.Spec.Replicas) {
		scaleUpCount := newReplicasCount - *(newMS.Spec.Replicas)
		log.V(5).Info(fmt.Sprintf("Setting scale up intent for %s to %d replicas (+%d)", newMS.Name, newReplicasCount, scaleUpCount), "machineset", client.ObjectKeyFromObject(newMS).String())
		p.scaleIntents[newMS.Name] = newReplicasCount
	}
	return nil
}

func (p *rolloutPlanner) reconcileOldMachineSets(ctx context.Context, md *clusterv1.MachineDeployment, newMS *clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet) error {
	allMSs := append(oldMSs, newMS)

	if md.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineDeployment %v is nil, this is unexpected",
			client.ObjectKeyFromObject(md))
	}

	if newMS.Spec.Replicas == nil {
		return errors.Errorf("spec.replicas for MachineSet %v is nil, this is unexpected",
			client.ObjectKeyFromObject(newMS))
	}

	// no op if there are no replicas on old machinesets
	if mdutil.GetReplicaCountForMachineSets(oldMSs) == 0 {
		return nil
	}

	maxUnavailable := mdutil.MaxUnavailable(*md)
	minAvailable := max(ptr.Deref(md.Spec.Replicas, 0)-maxUnavailable, 0)

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
	// Exit immediately if there is no room for scaling dowm.
	totalScaleDownCount := max(totReplicas-totPendingScaleDown-minAvailable, 0)
	if totalScaleDownCount <= 0 {
		return nil
	}

	sort.Sort(mdutil.MachineSetsByCreationTimestamp(oldMSs))

	// Scale down only unavailable replicas / up to residual totalScaleDownCount.
	// NOTE: we are scaling up unavailable machines first in order to increase chances for the rollout to progress;
	// however, the MS controller might have different opinion on which machines to scale down.
	// As a consequence, the scale down operation must continuously assess if reducing the number of replicas
	// for an older MS could further impact availability under the assumption than any scale down could further impact availability (same as above).
	// if reducing the number of replicase might lead to breaching minAvailable, scale down extent must be limited accordingly.
	totalScaleDownCount, totAvailableReplicas = p.scaleDownOldMSs(ctx, oldMSs, totalScaleDownCount, totAvailableReplicas, minAvailable, false)

	// Then scale down old MS up to zero replicas / up to residual totalScaleDownCount.
	// NOTE: also in this case, continuously assess if reducing the number of replicase could further impact availability,
	// and if necessary, limit scale down extent to ensure the operation respects minAvailable limits.
	_, _ = p.scaleDownOldMSs(ctx, oldMSs, totalScaleDownCount, totAvailableReplicas, minAvailable, true)

	return nil
}

func (p *rolloutPlanner) scaleDownOldMSs(ctx context.Context, oldMSs []*clusterv1.MachineSet, totalScaleDownCount, totAvailableReplicas, minAvailable int32, scaleToZero bool) (int32, int32) {
	log := ctrl.LoggerFrom(ctx)

	for _, oldMS := range oldMSs {
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
			}
		}

		if scaleDown > 0 {
			newScaleIntent := max(scaleIntent-scaleDown, 0)
			log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d)", oldMS.Name, newScaleIntent, scaleDown), "machineset", client.ObjectKeyFromObject(oldMS).String())
			p.scaleIntents[oldMS.Name] = newScaleIntent
			totalScaleDownCount = max(totalScaleDownCount-scaleDown, 0)
			totAvailableReplicas = max(totAvailableReplicas-availableMachineScaleDown, 0)
		}
	}

	return totalScaleDownCount, totAvailableReplicas
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
func (p *rolloutPlanner) reconcileInPlaceUpdateIntent(ctx context.Context, md *clusterv1.MachineDeployment, newMS *clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, machines []*clusterv1.Machine) error {
	log := ctrl.LoggerFrom(ctx)
	allMSs := append(oldMSs, newMS)

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
		// FIXME: implement caching (let' avoid to keep asking if MD & MD are the same)
		canUpdateDecision := p.getCanUpdateDecision(oldMS)
		log.V(5).Info(fmt.Sprintf("CanUpdate decision for %s: %t", oldMS.Name, canUpdateDecision), "machineset", client.ObjectKeyFromObject(oldMS).String())

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
			// Note: in case newMS is already scaling up, do not add more.
			if ptr.Deref(newMS.Spec.Replicas, 0) <= ptr.Deref(newMS.Status.Replicas, 0) {
				newScaleUpCount = 1
			}
		}
		newScaleIntent := ptr.Deref(newMS.Spec.Replicas, 0) + newScaleUpCount
		log.V(5).Info(fmt.Sprintf("Revisit scale up intent for %s to %d replicas (+%d) to prevent creation of new machines while there are still in place updates to be performed", newMS.Name, newScaleIntent, newScaleUpCount), "machineset", client.ObjectKeyFromObject(newMS).String())
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
			return p.reconcileOldMachineSets(ctx, md, newMS, oldMSs)
		}
	}

	// FIXME(feedback) Let's talk about old/new MS controller race conditions
	//  * Wondering about scenarios where the move & accept annotations on MSs change over time and MS controllers might still act on the old annotations
	return nil
}

// This funcs tries to detect and address the case when a rollout is not making progress because both scaling down and scaling up are blocked.
// Note. this func must be called after computing scale up/down intent for all the MachineSets.
// Note. this func only address deadlock due to unavailable machines not getting deleted on oldMSs, e.g. due to a wrong configuration.
// unblocking deadlock when unavailable machines exists only on oldMSs, is required also because failures on old machines set are not remediated by MHC.
func (p *rolloutPlanner) reconcileDeadlockBreaker(ctx context.Context, newMS *clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet) {
	log := ctrl.LoggerFrom(ctx)
	allMSs := append(oldMSs, newMS)

	// if there are no replicas on the old MS, rollout is completed, no deadlock (actually no rollout in progress).
	if ptr.Deref(mdutil.GetActualReplicaCountForMachineSets(oldMSs), 0) == 0 {
		return
	}

	// if all the replicas on OldMS are available, no deadlock (regular scale up newMS and scale down oldMS should take over from here).
	if ptr.Deref(mdutil.GetActualReplicaCountForMachineSets(oldMSs), 0) == ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(oldMSs), 0) {
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
	if ptr.Deref(newMS.Status.AvailableReplicas, 0) != ptr.Deref(newMS.Status.Replicas, 0) {
		return
	}

	// At this point we can assume there is a deadlock that can be remediated by breaching maxUnavailability constraint
	// and scaling down an oldMS with unavailable machines by one.
	//
	// Note: in most cases this is only a formal violation of maxUnavailability, because there is a good changes
	// that the machine that will be deleted is one of the unavailable machines
	for _, oldMS := range oldMSs {
		if ptr.Deref(oldMS.Status.AvailableReplicas, 0) == ptr.Deref(oldMS.Status.Replicas, 0) || ptr.Deref(oldMS.Spec.Replicas, 0) == 0 {
			continue
		}

		newScaleIntent := max(ptr.Deref(oldMS.Spec.Replicas, 0)-1, 0)
		log.Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas (-%d) to unblock rollout stuck due to unavailable machine on oldMS only", oldMS.Name, newScaleIntent, 1), "machineset", client.ObjectKeyFromObject(oldMS).String())
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
