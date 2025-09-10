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

	// FIXME(feedback): how are reconcileNewMachineSet & reconcileOldMachineSets counting Machines that are going through an in-place update
	// e.g. if they are just counted as available we won't respect maxUnavailable correctly

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
	// - oldMs are not waiting for replicas (only newMS could be waiting)
	// - newMs are not moving replicas to another MS (only oldMS could be moving to the newMS)
	// - oldMs are not moving replicas when there are no more replicas to move
	// - newMs are not waiting for replicas when all the expected replicas already exist
	// NOTE: those cleanup step are necessary to handle properly changes to the MD during a rollout (newMS changes).
	// Also this ensures annotations are removed when an MS has completed a move operation for an in-place update,
	// or otherwise it ensures those annotation are kept to preserve a move decision across multiple MD reconcile.
	cleanupOutdatedInPlaceMoveAnnotations(oldMSs, newMS)

	// Find all the oldMSs for which it make sense perform an in-place update:
	// If old MS are scaling down, and new MS doesn't have yet all replicas, those old MS are candidates for in-place update.
	// FIXME(feedback) this only detects inPlaceUpdateCandidates based on old MS scale down, I assume this is correct as only if we have these scale downs we can do in-place, otherwise we'll just to scale up on the new MD
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
			// Important!
			// If the old old MS is already sending machines to the newMS,
			// we should not count them twice when computing the number that we are going to user
			// for increasing the replica count on the newMS (re-entrancy).
			if !hasScaleDownMovingTo(oldMS, newMS.Name) { // FIXME(feedback) not clear to me how this accounts for all Machines of the old MS correctly, e.g. first reconcile inPlaceUpdated: 1, next inPlaceUpdated: 1 => will not be counted (also related to the next point where we tell the old MS to move all Machines, I think)
				totInPlaceUpdated += inPlaceUpdated
			}

			// Set the annotation informing the old MS to move machine to the new MS instead of delete them.
			setScaleDownMovingTo(oldMS, newMS.Name) // FIXME(feedback): will this tell the old MS to move all Machines? (i.e. looks like the old MS won't respect maxUnavailable). To double-check: in general maxSurge / maxUnavailable would be always respected if we stick to the Machine create/delete the original logic "allowed us" (while deletes signal that in-place update is okay)
		}
	}

	// Exit quickly if there are no suitable candidates/machines to be updated in place.
	if inPlaceUpdateCandidates.Len() == 0 || totInPlaceUpdated == 0 {
		return nil
	}

	// At this point, we know there are oldMS that can be moved to the newMS and then upgraded in place,
	// so we should increase the replica count of the newMS for the replicas that are being moved.
	// Note: those machines already exist, so this operation is not breaching maxSurge constraint.
	// TODO: figure it out how to give precedence to max surge
	totScaleUp := int32(0)
	if scaleIntent, ok := p.scaleIntents[newMS.Name]; ok {
		scaleUp := max(ptr.Deref(newMS.Spec.Replicas, 0)-scaleIntent, 0) // FIXME(feedback) is this the wrong way around? example newMS replicas: 5, scaleIntent 6 => scaleUp = -1 (aka 0)
		totScaleUp += scaleUp
	}
	totScaleUp += totInPlaceUpdated // FIXME(feedback) is it correct that we don't subtract totInPlaceUpdated from scaleUpIntent delta?

	scaleIntentOverride := min(*newMS.Spec.Replicas+totScaleUp, *md.Spec.Replicas)
	p.scaleIntents[newMS.Name] = scaleIntentOverride
	setScaleUpWaitForReplicasFromMS(newMS, inPlaceUpdateCandidates.UnsortedList()...) // FIXME(feedback) How does the newMS know how many Machines to create? (Can new MS only wait or also create at the same time?)

	// FIXME(feedback) Let's talk about old/new MS controller race conditions
	// * Wondering about scenarios where the move & wait annotations on MSs change over time and MS controllers might still act on the old annotations

	log.V(5).Info(fmt.Sprintf("Overriding scale up intent for %s to %d replicas to account for machines that can be moved to %s", newMS.Name, scaleIntentOverride, sortAndJoin(inPlaceUpdateCandidates.UnsortedList())))
	return nil
}

// scaleDownMovingToAnnotationName is an annotation applied by the rollout planner to MachineSets.
// This annotation instructs the MS controller to scale down by moving machines to the target MS instead of deleting machines.
// Note: the annotation is not enough to make the actual move to happen, also the newMS
// must have an annotation accepting machines from this MS.
const scaleDownMovingToAnnotationName = "cluster.x-k8s.io/scale-down-moving-to"

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

// scaleUpWaitForReplicasAnnotationName is an annotation applied by the rollout planner to MachineSets.
// This annotation instructs the MS controller to not scale up by creating machines, but instead wait
// for machines being moved by other MachineSets.
const scaleUpWaitForReplicasAnnotationName = "cluster.x-k8s.io/scale-up-wait-for-replicas"

// setScaleUpWaitForReplicasFromMS updates the scaleUpWaitForReplicasAnnotationName for a machine set
// when one or more source MS are added.
// Note: all the existing source MS will be preserved, thus tracking intent across reconcile acting on
// different MachineSets.
func setScaleUpWaitForReplicasFromMS(ms *clusterv1.MachineSet, fromMSs ...string) {
	if ms.Annotations == nil {
		ms.Annotations = map[string]string{}
	}

	sourcesSet := sets.Set[string]{}
	currentList := ms.Annotations[scaleUpWaitForReplicasAnnotationName]
	if currentList != "" {
		sourcesSet.Insert(strings.Split(currentList, ",")...)
	}
	// FIXME(feedback) this seems more "add" then "set" (aka we preserve the ones that were in the list before), would be probably good to make this clearer in the func name (similar for the unset func below)
	sourcesSet.Insert(fromMSs...)

	sourcesList := sourcesSet.UnsortedList()
	sort.Strings(sourcesList)
	ms.Annotations[scaleUpWaitForReplicasAnnotationName] = strings.Join(sourcesList, ",")
}

// unsetScaleUpWaitForReplicasFromMS updates the scaleUpWaitForReplicasAnnotationName for a machine set
// when one source MS is removed.
// Note: all the existing source MS will be preserved, thus tracking intent across reconcile acting on
// different MachineSets.
func unsetScaleUpWaitForReplicasFromMS(ms *clusterv1.MachineSet, fromMS string) {
	currentList, ok := ms.Annotations[scaleUpWaitForReplicasAnnotationName]
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
		delete(ms.Annotations, scaleUpWaitForReplicasAnnotationName)
		return
	}

	sourcesList := sourcesSet.UnsortedList()
	ms.Annotations[scaleUpWaitForReplicasAnnotationName] = sortAndJoin(sourcesList)
}

// cleanupOutdatedInPlaceMoveAnnotations ensure thet all the outdated move annotations from previous reconcile are cleaned up:
// NOTE: those cleanup step are necessary to handle properly changes to the MD during a rollout (newMS changes).
// Also this ensures annotations are removed when an MS has completed a move operation for an in-place update,
// or otherwise it ensures those annotation are kept to preserve intent/move decision across multiple MD reconcile.
func cleanupOutdatedInPlaceMoveAnnotations(oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet) {
	// - newMs are not moving replicas to another MS (only oldMS could be moving to the newMS)
	delete(newMS.Annotations, scaleDownMovingToAnnotationName)

	// - newMs are not waiting for replicas when all the expected replicas already exist
	// FIXME: This cleanup should be done also outside of the rollout process.
	//   when doing this, might be it can help generalize this cleanup (any ms should not be waiting for replicas when all the expected replicas already exist)
	if _, ok := newMS.Annotations[scaleUpWaitForReplicasAnnotationName]; ok && ptr.Deref(newMS.Status.Replicas, 0) == ptr.Deref(newMS.Spec.Replicas, 0) {
		delete(newMS.Annotations, scaleDownMovingToAnnotationName) // FIXME(feedback): should this delete scaleUpWaitForReplicasAnnotationName instead?
	}

	for _, oldMS := range oldMSs {
		// - oldMs are not waiting for replicas (only newMS could be waiting)
		delete(oldMS.Annotations, scaleUpWaitForReplicasAnnotationName) // FIXME(feedback): what if an in-place update is still going on and the user reverts the MD? don't wait have to wait for the move to complete?

		// - oldMs are not moving replicas when there are no more replicas to move
		// FIXME: This cleanup should be done also outside of the rollout process.
		//   when doing this, might be it can help generalize this cleanup (any ms should not be moving replicas when there are no more replicas to move)
		if ptr.Deref(oldMS.Status.Replicas, 0) == ptr.Deref(oldMS.Spec.Replicas, 0) {
			delete(oldMS.Annotations, scaleDownMovingToAnnotationName)

			// NOTE: also drop this MS from the list of MachineSets from which the new MS is waiting for replicas.
			unsetScaleUpWaitForReplicasFromMS(newMS, oldMS.Name) // FIXME(feedback) what if the old MS is being deleted, will the old MS be removed from the new MS annotation then?
		}
	}
}

func sortAndJoin(a []string) string {
	sort.Strings(a)
	return strings.Join(a, ",")
}
