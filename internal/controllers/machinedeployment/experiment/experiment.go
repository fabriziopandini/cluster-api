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
)

type rolloutPlanner struct {
	getCanUpdateDecision func(oldMS *clusterv1.MachineSet) bool
	scaleIntents         map[string]int32
}

func (p *rolloutPlanner) rolloutRolling(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet) ([]*clusterv1.MachineSet, error) {
	newMS, oldMSs, err := p.getAllMachineSetsAndSyncRevision(ctx, md, msList)
	if err != nil {
		return nil, err
	}

	allMSs := append(oldMSs, newMS)

	// FIXME: I'm not recomputing MD status because it looks like nothing in this func depends from this; double check this

	p.scaleIntents = make(map[string]int32)

	p.reconcileReplicasAfterMoveToNewMS(ctx, md, newMS)

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

	if err := p.reconcileInPlaceUpdateIntent(ctx, allMSs, oldMSs, newMS, md); err != nil {
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
		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas", newMS.Name, newReplicasCount))
		p.scaleIntents[newMS.Name] = newReplicasCount
	}
	if newReplicasCount > *(newMS.Spec.Replicas) {
		log.V(5).Info(fmt.Sprintf("Setting scale up intent for %s to %d replicas", newMS.Name, newReplicasCount))
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

		log.V(5).Info(fmt.Sprintf("Setting scale down intent for %s to %d replicas", targetMS.Name, newReplicasCount))
		p.scaleIntents[targetMS.Name] = newReplicasCount
		totalScaledDown += scaleDownCount
	}

	return totalScaledDown, nil
}

// reconcileReplicasAfterMoveToNewMS adjust the replica count for the newMS after a move operation from oldMSs to newMS has been completed.
// Note: This operation must be performed before computing scale up/down for all the MachineSets (so this operation can take into account also moved machines in the current reconcile).
func (p *rolloutPlanner) reconcileReplicasAfterMoveToNewMS(ctx context.Context, md *clusterv1.MachineDeployment, newMS *clusterv1.MachineSet) {
	log := ctrl.LoggerFrom(ctx)

	// Drop machines from the replicasAcknowledgeByMachineDeploymentAnnotation when those machines are not anymore in the replicasAcknowledgeByMachineSetAnnotation annotation.
	// Note: Under normal circumstances this happen after the newMS controller reconcile the replicasAcknowledgeByMachineDeployment signal for a replica,
	// but this code also acts a catch-all cleanup logic.
	for _, m := range getReplicasAcknowledgeByMachineDeployment(newMS).UnsortedList() {
		if !getReplicasAcknowledgeByMachineSet(newMS).Has(m) {
			removeReplicasAcknowledgeByMachineDeployment(newMS, m)
		}
	}

	// Acknowledge replicas already Acknowledged by the MS controller after a move operation, and not yet acknowledge by the MachineDeployment,
	// by scale up the newMS to reflect the fact that the MS controller has a new replica to take care of.
	totNewAcceptedReplicas := int32(0)
	acceptedReplicas := []string{}
	for _, m := range getReplicasAcknowledgeByMachineSet(newMS).UnsortedList() {
		if getReplicasAcknowledgeByMachineDeployment(newMS).Has(m) {
			continue
		}
		totNewAcceptedReplicas++
		acceptedReplicas = append(acceptedReplicas, m)
		addReplicasAcknowledgeByMachineDeployment(newMS, m)
	}
	if totNewAcceptedReplicas > 0 {
		replicaCount := min(ptr.Deref(newMS.Spec.Replicas, 0)+totNewAcceptedReplicas, ptr.Deref(md.Spec.Replicas, 0))
		newMS.Spec.Replicas = ptr.To(replicaCount)
		log.V(5).Info(fmt.Sprintf("Scale up %s to %d replicas to acknowledge %s moved from an old MachineSet", newMS.Name, replicaCount, sortAndJoin(acceptedReplicas)))
	}
}

func (p *rolloutPlanner) reconcileInPlaceUpdateIntent(ctx context.Context, allMSs []*clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, md *clusterv1.MachineDeployment) error {
	log := ctrl.LoggerFrom(ctx)

	// When calling this func, new and old MS have their scale intent, which was computed under the assumption that
	// rollout is going to happen by delete/re-create, and thus it will impact availability.

	// This function checks if it is possible to rollout changes by performing in-place updates.
	// A key assumption for the logic implemented in this function is the fact that
	// in place updates impact availability (even if the in place update technically is not impacting workloads,
	// the system must account for scenarios when the operation fails, leading to remediation of the machine/unavailability).
	// As a consequence, unless the user accounts for this unavailability by setting MaxUnavailable >= 1,
	// rollout with in-place will create at least 1 additional machine to ensure MaxUnavailable == 0 is respected.
	// NOTE: if maxSurge is >= 1, machine deployment will create more additional machines.

	// First, ensure that all the outdated move annotations from previous reconcile are cleaned up:
	// - oldMs are not accepting replicas (only newMS could be accepting replicas)
	// - newMs are not moving replicas to another MS (only oldMS could be moving to the newMS)
	// - oldMs are not moving replicas when there are no more replicas to move
	// NOTE: those cleanup step are necessary to handle properly changes to the MD during a rollout (newMS changes).
	// Also this ensures annotations are removed when an MS has completed a move operation for an in-place update,
	// or otherwise it ensures those annotation are kept to preserve a move decision across multiple MD reconcile.
	cleanupOutdatedInPlaceMoveAnnotations(oldMSs, newMS)

	// Find all the oldMSs for which it make sense perform an in-place update:
	// If old MS are scaling down, and new MS doesn't have yet all replicas, those old MS are candidates for in-place update.
	inPlaceUpdateCandidates := sets.Set[string]{}
	for _, oldMS := range oldMSs {
		if scaleIntent, ok := p.scaleIntents[oldMS.Name]; ok && scaleIntent < ptr.Deref(oldMS.Spec.Replicas, 0) {
			if ptr.Deref(newMS.Spec.Replicas, 0) < ptr.Deref(md.Spec.Replicas, 0) {
				inPlaceUpdateCandidates.Insert(oldMS.Name)
			}
		}
	}

	// Check if candidate MS can update in place.
	totInPlaceUpdated := int32(0)
	for _, oldMS := range oldMSs {
		if !inPlaceUpdateCandidates.Has(oldMS.Name) {
			continue
		}

		// FIXME: Think about how to propagate all the info required for the canUpdate from getAllMachineSetsAndSyncRevision to here.
		// TODO: Possible optimization, if a move to the same target is already in progress, do not ask again
		canUpdateDecision := p.getCanUpdateDecision(oldMS)

		log.V(5).Info(fmt.Sprintf("CanUpdate decision for %s: %t", oldMS.Name, canUpdateDecision))

		// drop the candidate if it can't update in place
		if !canUpdateDecision {
			inPlaceUpdateCandidates.Delete(oldMS.Name)
			continue
		}

		// keep track of how many machines are going to be updated in-place.
		scaleIntent, ok := p.scaleIntents[oldMS.Name]
		if !ok {
			// Note: this condition can't happen because in-place candidates are oldMs with a scale down intent
			continue
		}

		inPlaceUpdated := max(ptr.Deref(oldMS.Spec.Replicas, 0)-scaleIntent, 0)
		if inPlaceUpdated > 0 {
			totInPlaceUpdated += inPlaceUpdated

			// Set the annotation tracking that the old MS must move machines to the new MS instead of deleting them.
			// Note: After move is completed, the new MS will take care of the in-place upgrade process.
			setScaleDownMovingTo(oldMS, newMS.Name)
		}
	}

	// Exit quickly if there are no suitable candidates/machines to be updated in place.
	if inPlaceUpdateCandidates.Len() == 0 || totInPlaceUpdated == 0 {
		return nil
	}

	// At this point, we know there are oldMS that can be moved to the newMS and then upgraded in place;
	// Trace this information in the newMS thus allowing a two-ways check before the move operation:
	// 	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
	addMachineSetToAcceptReplicasFrom(newMS, inPlaceUpdateCandidates.UnsortedList()...)

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

func hasScaleDownMovingTo(ms *clusterv1.MachineSet, targetMS string) bool {
	if val, ok := ms.Annotations[scaleDownMovingToAnnotationName]; ok && val == targetMS {
		return true
	}
	return false
}

// acceptReplicasFromAnnotationName is an internal annotation added by the MD controller to the newMS
// when it should accept replicas from an old machines as a first step of an in-place upgrade operation.
// Note: This annotation is used to perform a two-ways check before moving a machine from oldMS to new MS:
//
//	"oldMS must have: move to newMS" and "newMS must have: accept replicas from oldMS"
const acceptReplicasFromAnnotationName = "internal.cluster.x-k8s.io/accept-replicas-from"

// addMachineSetToAcceptReplicasFrom updates the acceptReplicasFromAnnotationName for a machine set
// when one or more source MS are added.
// Note: all the existing source MS will be preserved, thus tracking intent across reconcile acting on
// different MachineSets.
func addMachineSetToAcceptReplicasFrom(ms *clusterv1.MachineSet, fromMSs ...string) {
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}

	sourcesSet := sets.Set[string]{}
	currentList := ms.Annotations[acceptReplicasFromAnnotationName]
	if currentList != "" {
		sourcesSet.Insert(strings.Split(currentList, ",")...)
	}
	sourcesSet.Insert(fromMSs...)

	sourcesList := sourcesSet.UnsortedList()
	sort.Strings(sourcesList)
	ms.Annotations[acceptReplicasFromAnnotationName] = strings.Join(sourcesList, ",")
}

// removeMachineSetFromAcceptReplicasFrom updates the acceptReplicasFromAnnotationName for a machine set
// when one source MS is removed.
// Note: all the existing source MS will be preserved, thus tracking intent across reconcile acting on
// different MachineSets.
func removeMachineSetFromAcceptReplicasFrom(ms *clusterv1.MachineSet, fromMS string) {
	currentList, ok := ms.Annotations[acceptReplicasFromAnnotationName]
	if !ok {
		return
	}

	sourcesSet := sets.Set[string]{}
	sourcesSet.Insert(strings.Split(currentList, ",")...)
	if !sourcesSet.Has(fromMS) {
		return
	}

	sourcesSet.Delete(fromMS)
	if sourcesSet.Len() == 0 {
		delete(ms.Annotations, acceptReplicasFromAnnotationName)
		return
	}

	sourcesList := sourcesSet.UnsortedList()
	ms.Annotations[acceptReplicasFromAnnotationName] = sortAndJoin(sourcesList)
}

// replicaPendingMachineDeploymentAcknowledgeAnnotationName is an internal annotation added by the MS controller to a machine when being
// moved from the oldMS to the newMS. The annotation is removed as soon as the machine is accepted by the newMS and recognized by the corresponding MD.
// Note: the annotation is added when reconciling the oldMS, and it is removed when reconciling the newMS:
const replicaPendingMachineDeploymentAcknowledgeAnnotationName = "internal.cluster.x-k8s.io/replica-pending-md-acknowledge"

// replicasMachineSetAcknowledgeAnnotationName is an internal annotation with a list of machines added by the MS controller
// to a MachineSet when it acknowledges a machine replica that has neen moved from an oldMS (and yet to be updated in-place).
// A machine is dropped from this annotation as soon as also the MD controller acknowledge the same replica;
// the annotation is dropped when empty.
// Note: this annotation is used in pair with replicasMachineDeploymentAcknowledgeAnnotation and replicaPendingMachineDeploymentAcknowledgeAnnotation.
const replicasMachineSetAcknowledgeAnnotationName = "internal.cluster.x-k8s.io/replicas-acknowledge"

func getReplicasAcknowledgeByMachineSet(ms *clusterv1.MachineSet) sets.Set[string] {
	sourcesSet := sets.Set[string]{}
	currentList := ms.Annotations[replicasMachineSetAcknowledgeAnnotationName]
	if currentList != "" {
		sourcesSet.Insert(strings.Split(currentList, ",")...)
	}
	return sourcesSet
}

func addReplicaAcknowledgeByMachineSet(ms *clusterv1.MachineSet, machines ...string) {
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}

	sourcesSet := getReplicasAcknowledgeByMachineSet(ms)
	sourcesSet.Insert(machines...)

	sourcesList := sourcesSet.UnsortedList()
	sort.Strings(sourcesList)
	ms.Annotations[replicasMachineSetAcknowledgeAnnotationName] = strings.Join(sourcesList, ",")
}

func removeReplicaAcknowledgeByMachineSet(ms *clusterv1.MachineSet, machine string) {
	sourcesSet := getReplicasAcknowledgeByMachineSet(ms)
	if !sourcesSet.Has(machine) {
		return
	}

	sourcesSet.Delete(machine)
	if sourcesSet.Len() == 0 {
		delete(ms.Annotations, replicasMachineSetAcknowledgeAnnotationName)
		return
	}

	sourcesList := sourcesSet.UnsortedList()
	ms.Annotations[replicasMachineSetAcknowledgeAnnotationName] = sortAndJoin(sourcesList)
}

// replicasMachineDeploymentAcknowledgeAnnotationName is an internal annotation with a list of machines added by the MD controller
// to a MachineSet when it acknowledges a machine replica that has neen moved from an oldMS.
// A machine is dropped from the annotation is removed as soon as the MS controller removes it from the replicasMachineSetAcknowledgeAnnotation,
// which signals the completion of the replica acknowledge workflow; the annotation is dropped when empty.
// Note: this annotation is used in pair with replicasMachineSetAcknowledgeAnnotationName and replicaPendingMachineDeploymentAcknowledgeAnnotation.
const replicasMachineDeploymentAcknowledgeAnnotationName = "internal.cluster.x-k8s.io/replicas-machinedeployment-acknowledge"

func getReplicasAcknowledgeByMachineDeployment(ms *clusterv1.MachineSet) sets.Set[string] {
	sourcesSet := sets.Set[string]{}
	currentList := ms.Annotations[replicasMachineDeploymentAcknowledgeAnnotationName]
	if currentList != "" {
		sourcesSet.Insert(strings.Split(currentList, ",")...)
	}
	return sourcesSet
}

func addReplicasAcknowledgeByMachineDeployment(ms *clusterv1.MachineSet, machines ...string) {
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}

	sourcesSet := getReplicasAcknowledgeByMachineDeployment(ms)
	sourcesSet.Insert(machines...)

	sourcesList := sourcesSet.UnsortedList()
	sort.Strings(sourcesList)
	ms.Annotations[replicasMachineDeploymentAcknowledgeAnnotationName] = strings.Join(sourcesList, ",")
}

func removeReplicasAcknowledgeByMachineDeployment(ms *clusterv1.MachineSet, machine string) {
	sourcesSet := getReplicasAcknowledgeByMachineDeployment(ms)
	if !sourcesSet.Has(machine) {
		return
	}

	sourcesSet.Delete(machine)
	if sourcesSet.Len() == 0 {
		delete(ms.Annotations, replicasMachineDeploymentAcknowledgeAnnotationName)
		return
	}

	sourcesList := sourcesSet.UnsortedList()
	ms.Annotations[replicasMachineDeploymentAcknowledgeAnnotationName] = sortAndJoin(sourcesList)
}

// cleanupOutdatedInPlaceMoveAnnotations ensure thet all the outdated move annotations from previous reconcile are cleaned up:
// NOTE: those cleanup step are necessary to handle properly changes to the MD during a rollout (newMS changes).
// Also this ensures annotations are removed when an MS has completed a move operation for an in-place update,
// or otherwise it ensures those annotation are kept to preserve intent/move decision across multiple MD reconcile.
func cleanupOutdatedInPlaceMoveAnnotations(oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet) {
	// - newMs are not moving replicas to another MS (only oldMS could be moving to the newMS)
	delete(newMS.Annotations, scaleDownMovingToAnnotationName)

	for _, oldMS := range oldMSs {
		// - oldMs are not waiting for replicas (only newMS could be waiting)
		delete(oldMS.Annotations, acceptReplicasFromAnnotationName)
		delete(oldMS.Annotations, replicasMachineSetAcknowledgeAnnotationName) // FIXME: evealuate if/how to clean up this annotation int the MS controller (the "owner" of this annotation)
		delete(oldMS.Annotations, replicasMachineDeploymentAcknowledgeAnnotationName)

		// - oldMs are not moving replicas when there are no more replicas to move
		// FIXME: This cleanup should be done also outside of the rollout process.
		//   when doing this, might be it can help generalize this cleanup (any ms should not be moving replicas when there are no more replicas to move)
		if ptr.Deref(oldMS.Status.Replicas, 0) == ptr.Deref(oldMS.Spec.Replicas, 0) {
			delete(oldMS.Annotations, scaleDownMovingToAnnotationName)

			// NOTE: also drop this MS from the list of MachineSets from which the new MS is waiting for replicas.
			removeMachineSetFromAcceptReplicasFrom(newMS, oldMS.Name)
		}
	}

	// TODO: consider adding additional safeguard to drop non-existing MS from acceptReplicasFromAnnotationName, maybe also something for deleting MS
}

func sortAndJoin(a []string) string {
	sort.Strings(a)
	return strings.Join(a, ",")
}
