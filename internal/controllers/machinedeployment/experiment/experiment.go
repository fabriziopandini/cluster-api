package experiment

import (
	"context"
	"fmt"
	"sort"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
)

func rolloutRolling(ctx context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet) ([]*clusterv1.MachineSet, error) {
	newMS, oldMSs, err := getAllMachineSetsAndSyncRevision(ctx, md, msList)
	if err != nil {
		return nil, err
	}

	allMSs := append(oldMSs, newMS)

	// FIXME: I'm not recomputing MD status because it looks like nothing in this func depends from this; double check this

	if err := reconcileNewMachineSet(ctx, allMSs, newMS, md); err != nil {
		return nil, err
	}

	if err := reconcileOldMachineSets(ctx, allMSs, oldMSs, newMS, md); err != nil {
		return nil, err
	}

	return allMSs, nil
}

func getAllMachineSetsAndSyncRevision(_ context.Context, md *clusterv1.MachineDeployment, msList []*clusterv1.MachineSet) (*clusterv1.MachineSet, []*clusterv1.MachineSet, error) {
	// FIXME: this code is super fake
	var newMs *clusterv1.MachineSet
	oldMs := make([]*clusterv1.MachineSet, 0, len(msList))
	for _, ms := range msList {
		if ms.Spec.ClusterName == md.Spec.ClusterName { // Note: using ClusterName to track MD revision and detect MD changes
			newMs = ms
			continue
		}
		oldMs = append(oldMs, ms)
	}
	if newMs == nil {
		newMs = &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ms%d", len(msList)+1), // Note: this assumes that we are not deleting MS (re-using names would be confusing)
			},
			Spec: clusterv1.MachineSetSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: md.Spec.ClusterName,
				// Note: without this the newMS.Spec.Replicas == nil will fail
				//  Also may be this is correct, why to create a new MS if we are not moving machine to it?
				Replicas: pointer.Int32Ptr(1),
			},
		}
	}
	return newMs, oldMs, nil
}

func reconcileNewMachineSet(ctx context.Context, allMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, md *clusterv1.MachineDeployment) error {
	// FIXME: cleanupDisableMachineCreateAnnotation

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
		return scaleMachineSet(ctx, newMS, *(md.Spec.Replicas))
	}

	newReplicasCount, err := mdutil.NewMSNewReplicas(md, allMSs, *newMS.Spec.Replicas)
	if err != nil {
		return err
	}
	return scaleMachineSet(ctx, newMS, newReplicasCount)
}

func reconcileOldMachineSets(ctx context.Context, allMSs []*clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, newMS *clusterv1.MachineSet, md *clusterv1.MachineDeployment) error {
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
	_, err := scaleDownOldMachineSetsForRollingUpdate(ctx, allMSs, oldMSs, md)
	if err != nil {
		return err
	}

	return nil
}

func scaleDownOldMachineSetsForRollingUpdate(ctx context.Context, allMSs []*clusterv1.MachineSet, oldMSs []*clusterv1.MachineSet, md *clusterv1.MachineDeployment) (int32, error) {
	// FIXME: Log?

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

		if err := scaleMachineSet(ctx, targetMS, newReplicasCount); err != nil {
			return totalScaledDown, err
		}

		totalScaledDown += scaleDownCount
	}

	return totalScaledDown, nil
}

func scaleMachineSet(_ context.Context, ms *clusterv1.MachineSet, replicas int32) error {
	// FIXME: most probably we need something different to track "scale intent" and apply it later
	ms.Spec.Replicas = &replicas
	return nil
}
