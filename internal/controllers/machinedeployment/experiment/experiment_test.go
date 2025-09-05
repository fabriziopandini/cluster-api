package experiment

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
)

var ctx = context.Background()

func Test_rolloutSequences(t *testing.T) {
	tests := []struct {
		name           string
		maxSurge       int32
		maxUnavailable int32
		// currentMachineNames is the list of machines before the rollout.
		// all the machines in this list are initialized as upToDate and owned by the new MD before the rollout.
		// Please name machines as "mX" where X is a progressive number starting from 1 (do not skip numbers),
		// e.g. "m1","m2","m3"
		currentMachineNames []string
		// desiredMachineNames is the list of machines at the end of the rollout.
		// all the machines in this list are expected to be upToDate and owned by the new MD after the rollout (which is different from the new MD before the rollout).
		// if this list contains old machines names (machine names already in currentMachineNames), it implies those machine have been upgraded in places.
		// if this list contains new machines names (machine names not in currentMachineNames), it implies those machines has been created during a rollout;
		// please name new machines names as "mX" where X is a progressive number starting after the max number in currentMachineNames (do not skip numbers),
		// e.g. desiredMachineNames "m4","m5","m6" (desired machine names after a regular rollout of a MD with currentMachineNames "m1","m2","m3")
		// e.g. desiredMachineNames "m1","m2","m3" (desired machine names after rollout performed using in-place upgrade for an MD with currentMachineNames "m1","m2","m3")
		desiredMachineNames []string

		// TODO: support use cases where there is one or more old, empty MS
		// TODO: support use cases where desired replica count changes in flight
		// TODO: support use cases where desired state changes in flight
		// TODO: support use cases where MachineSet or Machine reconcile is delayed
		// TODO: TBD support use cases where remediation kicks in
		// TODO: in-place...
		// TODO: save golden files + compare with golden files
	}{
		{
			name:                "test1",
			maxSurge:            0,
			maxUnavailable:      1,
			currentMachineNames: []string{"m1", "m2", "m3"},
			desiredMachineNames: []string{"m4", "m5", "m6"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Init current and desired state from test case
			current, desired := Init(tt.maxSurge, tt.maxUnavailable, tt.currentMachineNames, tt.desiredMachineNames)

			// Log initial state
			t.Logf("[Test] initial state\n%s", current)
			i := 1
			maxIterations := 20
			for {
				// Running a small subset of MD reconcile (the rollout logic and a bit of setReplicas)
				machineSets, err := rolloutRolling(ctx, current.machineDeployment, current.machineSets)
				g.Expect(err).ToNot(HaveOccurred())
				current.machineSets = machineSets
				current.machineDeployment.Status.Replicas = mdutil.GetActualReplicaCountForMachineSets(machineSets)
				current.machineDeployment.Status.AvailableReplicas = mdutil.GetAvailableReplicaCountForMachineSets(machineSets)

				// Log state after this reconcile
				t.Logf("[MD controller] iteration %d\n%s", i, current)

				// Check we are not breaching rollout constraints
				minAvailable := ptr.Deref(current.machineDeployment.Spec.Replicas, 0) - mdutil.MaxUnavailable(*current.machineDeployment)
				totAvailable := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(current.machineSets), 0)
				g.Expect(totAvailable).To(BeNumerically(">=", minAvailable), "totAvailable machines is less than MaxUnavailable")

				maxReplicas := ptr.Deref(current.machineDeployment.Spec.Replicas, 0) + mdutil.MaxSurge(*current.machineDeployment)
				totReplicas := mdutil.GetReplicaCountForMachineSets(current.machineSets)
				g.Expect(totReplicas).To(BeNumerically("<=", maxReplicas), "totReplicas machines is greater than MaxSurge")
				totActualReplicas := ptr.Deref(mdutil.GetActualReplicaCountForMachineSets(current.machineSets), 0)
				g.Expect(totActualReplicas).To(BeNumerically("<=", maxReplicas), "totActualReplicas machines is greater than MaxSurge")

				// Run mutators faking other controllers
				msControllerMutator(t, current)

				// Check if we are at the desired state
				if current.Equal(desired) {
					t.Logf("[Test] final state\n%s", desired)
					break
				}

				// Safeguard for infinite reconcile
				i++
				if i > maxIterations {
					current.Equal(desired)
					// Log desired state we never reached
					t.Logf("[Test] desired state\n%s", desired)
					g.Fail(fmt.Sprintf("Failed to reach desired state in less than %d iterations", maxIterations))
				}
			}
		})
	}
}

type rolloutScope struct {
	machineDeployment  *clusterv1.MachineDeployment
	machineSets        []*clusterv1.MachineSet
	machineSetMachines map[string][]*clusterv1.Machine

	machineUID int32
}

func Init(maxSurge int32, maxUnavailable int32, currentMachineNames []string, wantMachineNames []string) (current, desired *rolloutScope) {
	// Create current state, with a MD with
	// - given MaxSurge, MaxUnavailable
	// - replica counters assuming all the machines are at stable state
	// - spec different from the MachineSets and Machines we are going to create down below (to simulate a change that triggers a rollout, but it is not yet started)
	mdReplicaCount := int32(len(currentMachineNames))
	current = &rolloutScope{
		machineDeployment: &clusterv1.MachineDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "md"},
			Spec: clusterv1.MachineDeploymentSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: "v2",
				Replicas:    &mdReplicaCount,
				Rollout: clusterv1.MachineDeploymentRolloutSpec{
					Strategy: clusterv1.MachineDeploymentRolloutStrategy{
						Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
						RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
							MaxSurge:       ptr.To(intstr.FromInt32(maxSurge)),
							MaxUnavailable: ptr.To(intstr.FromInt32(maxUnavailable)),
						},
					},
				},
			},
			Status: clusterv1.MachineDeploymentStatus{
				Replicas:          &mdReplicaCount,
				AvailableReplicas: &mdReplicaCount,
			},
		},
	}

	// Create current MS, with
	// - replica counters assuming all the machines are at stable state
	// - spec at stable state (rollout is not yet propagated to machines)
	var totMachineSets, totMachines int32
	totMachineSets++
	ms := &clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("ms%d", totMachineSets),
		},
		Spec: clusterv1.MachineSetSpec{
			// Note: using ClusterName to track MD revision and detect MD changes
			ClusterName: "v1",
			Replicas:    &mdReplicaCount,
		},
		Status: clusterv1.MachineSetStatus{
			Replicas:          &mdReplicaCount,
			AvailableReplicas: &mdReplicaCount,
		},
	}
	current.machineSets = append(current.machineSets, ms)

	// Create current Machines, with
	// - spec at stable state (rollout is not yet propagated to machines)
	currentMachines := []*clusterv1.Machine{}
	for _, machineSetMachineName := range currentMachineNames {
		totMachines++
		currentMachines = append(currentMachines, &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: machineSetMachineName},
			Spec: clusterv1.MachineSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: ms.Spec.ClusterName,
			},
		})
	}
	current.machineSetMachines = map[string][]*clusterv1.Machine{}
	current.machineSetMachines[ms.Name] = currentMachines

	current.machineDeployment.Spec.Replicas = ptr.To(totMachines)
	current.machineUID = totMachines

	// Create current state, with a MD equal to the one we started from because:
	// - spec was already changed in current to simulate a change that triggers a rollout
	// - desired replica counters are the same than current replica counters (we star with all the machines at stable state v1, we should end with all the machines at stable state v2)
	desired = &rolloutScope{
		machineDeployment: current.machineDeployment.DeepCopy(),
	}

	// Add current MS to desired state, but set replica counters to zero because all the machines must be moved to the new MS.
	for _, currentMS := range current.machineSets {
		oldMS := currentMS.DeepCopy()
		oldMS.Spec.Replicas = ptr.To(int32(0))
		oldMS.Status.Replicas = ptr.To(int32(0))
		oldMS.Status.AvailableReplicas = ptr.To(int32(0))
		desired.machineSets = append(desired.machineSets, oldMS)
	}

	// Add a new MS to desired state, with
	// - the new spec from the MD
	// - replica counters assuming all the replicas must be here at the end of the rollout.
	totMachineSets++
	newMS := &clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("ms%d", totMachineSets),
		},
		Spec: clusterv1.MachineSetSpec{
			// Note: using ClusterName to track MD revision and detect MD changes
			ClusterName: desired.machineDeployment.Spec.ClusterName,
			Replicas:    desired.machineDeployment.Spec.Replicas,
		},
		Status: clusterv1.MachineSetStatus{
			Replicas:          desired.machineDeployment.Spec.Replicas,
			AvailableReplicas: desired.machineDeployment.Spec.Replicas,
		},
	}
	desired.machineSets = append(desired.machineSets, newMS)

	// Add a want machines to desired state, with
	// - the new spec from the MD (steady state)
	desiredMachines := []*clusterv1.Machine{}
	for _, machineSetMachineName := range wantMachineNames {
		totMachines++
		desiredMachines = append(desiredMachines, &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: machineSetMachineName},
			Spec: clusterv1.MachineSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: newMS.Spec.ClusterName,
			},
		})
	}
	desired.machineSetMachines = map[string][]*clusterv1.Machine{}
	desired.machineSetMachines[newMS.Name] = desiredMachines

	return current, desired
}

// GetNextMachineUID provides a predictable UID for machines.
func (r *rolloutScope) GetNextMachineUID() int32 {
	r.machineUID++
	return r.machineUID
}

func (r rolloutScope) String() string {
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("%s, %d/%d replicas \n", r.machineDeployment.Name, ptr.Deref(r.machineDeployment.Status.Replicas, 0), ptr.Deref(r.machineDeployment.Spec.Replicas, 0)))
	for _, ms := range r.machineSets {
		machineNames := []string{}
		for _, m := range r.machineSetMachines[ms.Name] {
			machineNames = append(machineNames, m.Name)
		}
		sb.WriteString(fmt.Sprintf("- %s, %d/%d replicas (%s) \n", ms.Name, ptr.Deref(ms.Status.Replicas, 0), ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machineNames, ",")))
	}
	return sb.String()
}

func (r *rolloutScope) Equal(s *rolloutScope) bool {
	return machineDeploymentIsEqual(r.machineDeployment, s.machineDeployment) && machineSetsAreEqual(r.machineSets, s.machineSets) && machineSetMachinesAreEqual(r.machineSetMachines, s.machineSetMachines)
}

func machineDeploymentIsEqual(a, b *clusterv1.MachineDeployment) bool {
	if ptr.Deref(a.Spec.Replicas, 0) != ptr.Deref(b.Spec.Replicas, 0) ||
		a.Spec.ClusterName != b.Spec.ClusterName {
		return false
	}
	return true
}

func machineSetsAreEqual(a, b []*clusterv1.MachineSet) bool {
	if len(a) != len(b) {
		return false
	}

	aMap := make(map[string]*clusterv1.MachineSet)
	for i := range a {
		aMap[a[i].Name] = a[i]
	}

	for i := range b {
		desiredMS := b[i]
		currentMS, ok := aMap[desiredMS.Name]
		if !ok {
			return false
		}
		if ptr.Deref(desiredMS.Spec.Replicas, 0) != ptr.Deref(currentMS.Spec.Replicas, 0) ||
			ptr.Deref(desiredMS.Status.Replicas, 0) != ptr.Deref(currentMS.Status.Replicas, 0) ||
			ptr.Deref(desiredMS.Status.AvailableReplicas, 0) != ptr.Deref(currentMS.Status.AvailableReplicas, 0) {
			return false
		}
	}
	return true
}

func machineSetMachinesAreEqual(a, b map[string][]*clusterv1.Machine) bool {

	return true
}

// msControllerMutator fakes a small part fo the MS controller, just what is require for the rollout to progress.
func msControllerMutator(t *testing.T, scope *rolloutScope) {
	for _, ms := range scope.machineSets {
		// if too few machines, create missing machine.
		// new machines are created with a predictable name, so it is easier to write test case and validate rollout sequences.
		// e.g. if the cluster is initialize with m1, m2, m3, new machines will be m4, m5, m6
		if ptr.Deref(ms.Spec.Replicas, 0) > ptr.Deref(ms.Status.Replicas, 0) {
			machinesToAdd := ptr.Deref(ms.Spec.Replicas, 0) - ptr.Deref(ms.Status.Replicas, 0)
			machinesAdded := []string{}
			for range machinesToAdd {
				machineName := fmt.Sprintf("m%d", scope.GetNextMachineUID())
				scope.machineSetMachines[ms.Name] = append(scope.machineSetMachines[ms.Name],
					&clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: machineName}},
				)
				machinesAdded = append(machinesAdded, machineName)
			}
			t.Logf("[MS controller] %s scale up to %d/%[2]d replicas (%s created)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesAdded, ","))
		}
		// if too many replicas, delete exceeding machines.
		// exceeding machines are deleted in predictable order, so it is easier to write test case and validate rollout sequences.
		// e.g. if a ms has m1,m2,m3 created in this order, m1 will be deleted first, then m2 and finally m3.
		if ptr.Deref(ms.Spec.Replicas, 0) < ptr.Deref(ms.Status.Replicas, 0) {
			machinesToDelete := ptr.Deref(ms.Status.Replicas, 0) - ptr.Deref(ms.Spec.Replicas, 0)
			machinesDeleted := []string{}
			for i := range machinesToDelete {
				machinesDeleted = append(machinesDeleted, scope.machineSetMachines[ms.Name][i].Name)
			}
			scope.machineSetMachines[ms.Name] = scope.machineSetMachines[ms.Name][machinesToDelete:]
			t.Logf("[MS controller] %s scale down to %d/%[2]d replicas (%s deleted)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesDeleted, ","))
		}

		// Update counters
		ms.Status.Replicas = ptr.To(int32(len(scope.machineSetMachines[ms.Name])))
		ms.Status.AvailableReplicas = ms.Status.Replicas
	}
}
