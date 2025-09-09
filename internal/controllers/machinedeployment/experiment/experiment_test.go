package experiment

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
)

// stateMutators are func that can change state in the middle of a rollout sequence.
// Note: those func are run before every iteration; when state are mutated, the func must return true.
type stateMutator func(log *logger, i int, scope *rolloutScope) bool

// minAvailableBreachSilencer are func that can be used to temporarily silence MinAvailable breaches in the middle of a rollout sequence.
type minAvailableBreachSilencer func(log *logger, i int, scope *rolloutScope, minAvailableReplicas, totAvailableReplicas int32) bool

// maxSurgeBreachSilencer are func that can be used to temporarily silence maxSurge breaches in the middle of a rollout sequence.
type maxSurgeBreachSilencer func(log *logger, i int, scope *rolloutScope, maxAllowedReplicas, totReplicas int32) bool

// directivesGenerator are func that return directives to be used by the fake MachineSet controller or by the fake Machine controller.
type directivesGenerator func(log *logger, i int, scope *rolloutScope) []string

type rolloutSequenceTestCase struct {
	name           string
	maxSurge       int32
	maxUnavailable int32
	// currentMachineNames is the list of machines before the rollout.
	// all the machines in this list are initialized as upToDate and owned by the new MD before the rollout.
	// Please name machines as "mX" where X is a progressive number starting from 1 (do not skip numbers),
	// e.g. "m1","m2","m3"
	currentMachineNames []string

	// Add another MS to the state before the rollout. This MS must not be used during the rollout.
	addAdditionalOldMachineSet bool

	// Add another MS to the state before the rollout. This MS must be used during the rollout and become owner of all the desired machines.
	addAdditionalOldMachineSetWithNewSpec bool

	// currentStateMutators allows to simulate users actions in the middle of a rollout
	// Note: those func are run before every iteration.
	//
	// currentStateMutators: []stateMutator{
	// 	func(log *logger, i int, scope *rolloutScope) bool {
	// 		if i == 5 {
	// 			t.Log("[User] scale up MD to 4 replicas")
	// 			scope.machineDeployment.Spec.Replicas = ptr.To(int32(4))
	// 			return true
	// 		}
	// 		return false
	// 	},
	// },
	currentStateMutators []stateMutator

	// minAvailableBreachSilencers can be used to temporarily silence MinAvailable breaches
	//
	// minAvailableBreachSilencers: []minAvailableBreachSilencer{
	// 	func(log *logger, i int, scope *rolloutScope, minAvailableReplicas, totAvailableReplicas int32) bool {
	// 		if i == 5 {
	// 			t.Log("[Toleration] tolerate minAvailable breach after scale up")
	// 			return true
	// 		}
	// 		return false
	// 	},
	// },
	minAvailableBreachSilencers []minAvailableBreachSilencer

	// maxSurgeBreachSilencers can be used to temporarily silence MaxSurge breaches
	// (see minAvailableBreachSilencers example)
	maxSurgeBreachSilencers []maxSurgeBreachSilencer

	// machineSetControllerDirectiveGenerators can be used to provide directives to be used by the fake MachineSet controller.
	//
	// machineSetControllerDirectiveGenerators: []directivesGenerator{
	// 	func(log *logger, i int, scope *rolloutScope) []string {
	// 		if i == 1 {
	// 			t.Log("[Directive] ms2 skip reconcile")
	// 			return []string{"ms2-SKIP-RECONCILE"}
	// 		}
	// 		return nil
	// 	},
	// },
	machineSetControllerDirectiveGenerators []directivesGenerator

	// desiredMachineNames is the list of machines at the end of the rollout.
	// all the machines in this list are expected to be upToDate and owned by the new MD after the rollout (which is different from the new MD before the rollout).
	// if this list contains old machines names (machine names already in currentMachineNames), it implies those machine have been upgraded in places.
	// if this list contains new machines names (machine names not in currentMachineNames), it implies those machines has been created during a rollout;
	// please name new machines names as "mX" where X is a progressive number starting after the max number in currentMachineNames (do not skip numbers),
	// e.g. desiredMachineNames "m4","m5","m6" (desired machine names after a regular rollout of a MD with currentMachineNames "m1","m2","m3")
	// e.g. desiredMachineNames "m1","m2","m3" (desired machine names after rollout performed using in-place upgrade for an MD with currentMachineNames "m1","m2","m3")
	desiredMachineNames []string

	// getCanUpdateDecision allows to inject a function that will be used to perform the canUpdate decision
	getCanUpdateDecision func(oldMS *clusterv1.MachineSet) bool

	// skipLogToFileAndGoldenFileCheck allows to skip storing the log to file and golden file Check.
	skipLogToFileAndGoldenFileCheck bool

	// randomControllerOrder force the tests to run controllers in random order, mimicking what happens in production.
	// NOTE. We are using a pseudo randomizer, so the random order remains consistent across runs of the same groups of tests.
	randomControllerOrder bool

	// TODO: introduce something to simulate errors in the rollout planner (may be, something like currentStateMutators, but it should be run after rolloutRolling)
	// TODO: dig into what happens when newMS is getting machines both from maxSurge and move (maxSurge>0, minAvailability>0).
	//   my assumption is that newMS will fist wait for machines from move, then take care of maxSurge
	//   might be we should give priority to maxSurge instead
	// TODO: introduce something to simulate when there are machines on multiple MS, and permutation of can updateInPlace choices
	//  note: this can be achieved already today by changing the md in the middle of a test, let's think if there are easier ways.
}

func Test_rolloutSequences(t *testing.T) {
	oldMSCanAlwaysInPlaceUpdate := func(oldMS *clusterv1.MachineSet) bool {
		return true
	}
	tests := []rolloutSequenceTestCase{
		{
			name:                            "test1", // Scale out, regular rollout
			maxSurge:                        1,
			maxUnavailable:                  0,
			currentMachineNames:             []string{"m1", "m2", "m3"},
			desiredMachineNames:             []string{"m4", "m5", "m6"},
			randomControllerOrder:           true,
			skipLogToFileAndGoldenFileCheck: true,
		},
		{
			name:                "test2", // Scale in, regular rollout
			maxSurge:            0,
			maxUnavailable:      1,
			currentMachineNames: []string{"m1", "m2", "m3"},
			desiredMachineNames: []string{"m4", "m5", "m6"},
		},
		{
			name:                 "test3A", // Scale out, in-place
			maxSurge:             1,
			maxUnavailable:       0,
			currentMachineNames:  []string{"m1", "m2", "m3"},
			desiredMachineNames:  []string{"m4", "m1", "m2"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{
			name:                            "test3B", // Scale out, in-place with no unavailability (should not scale out), but the receiving MS do not recognizes new machines immediately --> Availability flips
			maxSurge:                        0,
			maxUnavailable:                  1,
			currentMachineNames:             []string{"m1", "m2", "m3"},
			desiredMachineNames:             []string{"m1", "m2", "m3"},
			getCanUpdateDecision:            oldMSCanAlwaysInPlaceUpdate,
			randomControllerOrder:           true,
			skipLogToFileAndGoldenFileCheck: true,
		},
	}

	var ctx = context.Background()

	// Get logger
	logOptions := logs.NewOptions()
	logOptions.Verbosity = logsv1.VerbosityLevel(5)
	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		t.Errorf("Unable to validate and apply log options: %v", err)
	}
	logger := klog.Background()
	ctx = ctrl.LoggerInto(ctx, logger)

	fileLogger := newLogger(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			fileLogger.NewTestCase(tt.name)

			// Init current and desired state from test case
			current := initCurrentRolloutScope(tt)
			desired := computeDesiredRolloutScope(current, tt.desiredMachineNames)

			// Log initial state
			fileLogger.Logf("[Test] Initial state\n%s", current)
			i := 1
			maxIterations := 20
			for {
				// Run scope mutators faking users actions in the middle of a rollout
				if len(tt.currentStateMutators) > 0 {
					stateChanged := false
					for _, mutator := range tt.currentStateMutators {
						stateChanged = stateChanged || mutator(fileLogger, i, current)
					}
					if !stateChanged {
						// update desire state according to the mutated current scope
						desired = computeDesiredRolloutScope(current, tt.desiredMachineNames)
					}
				}

				// Compute directives that can be used to influence the MS controller
				directives := []string{}
				for _, generator := range tt.machineSetControllerDirectiveGenerators {
					directives = append(directives, generator(fileLogger, i, current)...)
				}

				taskOrder := defaultTaskOrder(current)
				if tt.randomControllerOrder {
					taskOrder = randomTaskOrder(current)
				}
				for _, taskID := range taskOrder {
					if taskID == 0 {
						if tt.randomControllerOrder {
							t.Logf("[MD controller] Iternation %d, Reconcile md", i)
						}
						// Running a small subset of MD reconcile (the rollout logic and a bit of setReplicas)
						p := &rolloutPlanner{
							getCanUpdateDecision: func(oldMS *clusterv1.MachineSet) bool {
								if tt.getCanUpdateDecision != nil {
									return tt.getCanUpdateDecision(oldMS)
								}
								return false
							},
						}
						machineSets, err := p.rolloutRolling(ctx, current.machineDeployment, current.machineSets)
						g.Expect(err).ToNot(HaveOccurred())
						current.machineSets = machineSets
						current.machineDeployment.Status.Replicas = mdutil.GetActualReplicaCountForMachineSets(machineSets)
						current.machineDeployment.Status.AvailableReplicas = mdutil.GetAvailableReplicaCountForMachineSets(machineSets)

						// Log state after this reconcile
						fileLogger.Logf("[MD controller] Result of rollout planner, iteration %d\n%s", i, current)

						// Check we are not breaching rollout constraints
						minAvailableReplicas := ptr.Deref(current.machineDeployment.Spec.Replicas, 0) - mdutil.MaxUnavailable(*current.machineDeployment)
						totAvailableReplicas := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(current.machineSets), 0)
						if totAvailableReplicas < minAvailableReplicas {
							tolerateBreach := false
							for _, tolerationFunc := range tt.minAvailableBreachSilencers {
								if tolerationFunc(fileLogger, i, current, minAvailableReplicas, totAvailableReplicas) {
									tolerateBreach = true
									break
								}
							}
							if !tolerateBreach {
								g.Expect(totAvailableReplicas).To(BeNumerically(">=", minAvailableReplicas), "totAvailable machines is less than MaxUnavailable")
							}
						}

						maxAllowedReplicas := ptr.Deref(current.machineDeployment.Spec.Replicas, 0) + mdutil.MaxSurge(*current.machineDeployment)
						totReplicas := mdutil.GetReplicaCountForMachineSets(current.machineSets)
						totActualReplicas := ptr.Deref(mdutil.GetActualReplicaCountForMachineSets(current.machineSets), 0)
						if max(totReplicas, totActualReplicas) > maxAllowedReplicas {
							tolerateBreach := false
							for _, tolerationFunc := range tt.maxSurgeBreachSilencers {
								if tolerationFunc(fileLogger, i, current, maxAllowedReplicas, max(totReplicas, totActualReplicas)) {
									tolerateBreach = true
									break
								}
							}
							if !tolerateBreach {
								g.Expect(totReplicas).To(BeNumerically("<=", maxAllowedReplicas), "totReplicas machines is greater than MaxSurge")
								g.Expect(totActualReplicas).To(BeNumerically("<=", maxAllowedReplicas), "totActualReplicas machines is greater than MaxSurge")
							}
						}
					}

					// Run mutators faking other controllers
					for _, ms := range current.machineSets {
						if fmt.Sprintf("ms%d", taskID) == ms.Name {
							if tt.randomControllerOrder {
								t.Logf("[MS controller] Iternation %d, Reconcile ms%d", i, taskID)
							}
							msControllerMutator(fileLogger, ms, current, directives)
							break
						}
					}
				}

				// Check if we are at the desired state
				if current.Equal(desired) {
					fileLogger.Logf("[Test] Final state\n%s", current)
					break
				}

				// Safeguard for infinite reconcile
				i++
				if i > maxIterations {
					current.Equal(desired)
					// Log desired state we never reached
					fileLogger.Logf("[Test] Desired state\n%s", desired)
					g.Fail(fmt.Sprintf("Failed to reach desired state in less than %d iterations", maxIterations))
				}
			}

			if !tt.skipLogToFileAndGoldenFileCheck {
				currentLog, goldenLog := fileLogger.EndTestCase()
				g.Expect(currentLog).To(Equal(goldenLog), "current test case log and golden test case log are different\n%s", cmp.Diff(currentLog, goldenLog))
			}
		})
	}
}

// default task order ensure the controllers are run in a consistent and predictable way: md, ms1, ms2 and so on
func defaultTaskOrder(current *rolloutScope) []int {
	taskOrder := []int{}
	for t := range len(current.machineSets) + 1 + 1 { // +1 is for the machine ms that might be created when reconciling md, +1 is for the md itself
		taskOrder = append(taskOrder, t)
	}
	return taskOrder
}

var rng = rand.New(rand.NewSource(0))

func randomTaskOrder(current *rolloutScope) []int {
	u := &UniqueRand{
		generated: map[int]bool{},
		max:       len(current.machineSets) + 1 + 1, // +1 is for the machine ms that might be created when reconciling md,
	}
	taskOrder := []int{}
	for {
		n := u.Int()
		if rng.Int()%10 < 3 { // skip a step in the 30% of cases
			continue
		}
		taskOrder = append(taskOrder, n)
		if r := rng.Int() % 10; r < 3 { // repeat a step in the 30% of cases
			delete(u.generated, n)
		}
		if len(u.generated) >= u.max {
			break
		}
	}
	return taskOrder
}

type UniqueRand struct {
	generated map[int]bool // keeps track of
	max       int          // max number to be generated
}

func (u *UniqueRand) Int() int {
	if len(u.generated) >= u.max {
		return -1
	}
	for {
		i := rng.Int() % u.max
		if !u.generated[i] {
			u.generated[i] = true
			return i
		}
	}
}

type rolloutScope struct {
	machineDeployment  *clusterv1.MachineDeployment
	machineSets        []*clusterv1.MachineSet
	machineSetMachines map[string][]*clusterv1.Machine

	machineUID int32
}

// TODO: break this down in InitCurrentRolloutScope and computeDesiredRolloutScope
// Init creates current state and desired state for rolling out a md from currentMachines to wantMachineNames.
func initCurrentRolloutScope(tt rolloutSequenceTestCase) (current *rolloutScope) {
	// create current state, with a MD with
	// - given MaxSurge, MaxUnavailable
	// - replica counters assuming all the machines are at stable state
	// - spec different from the MachineSets and Machines we are going to create down below (to simulate a change that triggers a rollout, but it is not yet started)
	mdReplicaCount := int32(len(tt.currentMachineNames))
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
							MaxSurge:       ptr.To(intstr.FromInt32(tt.maxSurge)),
							MaxUnavailable: ptr.To(intstr.FromInt32(tt.maxUnavailable)),
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

	var totMachineSets, totMachines int32

	// if required, add an old MS to current state, with
	// - replica counters 0 assuming all the machines are at stable state
	// - outdate spec -- this MS won't be used during the rollout
	if tt.addAdditionalOldMachineSet {
		totMachineSets++
		ms := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ms%d", totMachineSets),
			},
			Spec: clusterv1.MachineSetSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: "v0",
				Replicas:    ptr.To(int32(0)),
			},
			Status: clusterv1.MachineSetStatus{
				Replicas:          ptr.To(int32(0)),
				AvailableReplicas: ptr.To(int32(0)),
			},
		}
		current.machineSets = append(current.machineSets, ms)
	}

	// if required, add an old MS to current state, with
	// - replica counters 0 assuming all the machines are at stable state
	// - the same spec the MD got after triggering rollout -- this MS must be used during the rollout
	if tt.addAdditionalOldMachineSetWithNewSpec {
		totMachineSets++
		ms := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ms%d", totMachineSets),
			},
			Spec: clusterv1.MachineSetSpec{
				// Note: using ClusterName to track MD revision and detect MD changes
				ClusterName: current.machineDeployment.Spec.ClusterName,
				Replicas:    ptr.To(int32(0)),
			},
			Status: clusterv1.MachineSetStatus{
				Replicas:          ptr.To(int32(0)),
				AvailableReplicas: ptr.To(int32(0)),
			},
		}
		current.machineSets = append(current.machineSets, ms)
	}

	// Create current MS, with
	// - replica counters assuming all the machines are at stable state
	// - spec at stable state (rollout is not yet propagated to machines)
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
	for _, machineSetMachineName := range tt.currentMachineNames {
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

	return current
}

func computeDesiredRolloutScope(current *rolloutScope, desiredMachineNames []string) (desired *rolloutScope) {
	var totMachineSets, totMachines int32
	totMachineSets = int32(len(current.machineSets))
	for _, msMachines := range current.machineSetMachines {
		for range msMachines {
			totMachines++
		}
	}

	// Create current state, with a MD equal to the one we started from because:
	// - spec was already changed in current to simulate a change that triggers a rollout
	// - desired replica counters are the same than current replica counters (we star with all the machines at stable state v1, we should end with all the machines at stable state v2)
	desired = &rolloutScope{
		machineDeployment: current.machineDeployment.DeepCopy(),
	}

	// Add current MS to desired state, but set replica counters to zero because all the machines must be moved to the new MS.
	// Note: one of the old MD could also be the NewMS, the MS that must become owner of all the desired machines.
	var newMS *clusterv1.MachineSet
	for _, currentMS := range current.machineSets {
		oldMS := currentMS.DeepCopy()
		oldMS.Spec.Replicas = ptr.To(int32(0))
		oldMS.Status.Replicas = ptr.To(int32(0))
		oldMS.Status.AvailableReplicas = ptr.To(int32(0))
		desired.machineSets = append(desired.machineSets, oldMS)

		if oldMS.Spec.ClusterName == desired.machineDeployment.Spec.ClusterName {
			newMS = oldMS
		}
	}

	// Add or update the new MS to desired state, with
	// - the new spec from the MD
	// - replica counters assuming all the replicas must be here at the end of the rollout.
	if newMS != nil {
		newMS.Spec.Replicas = desired.machineDeployment.Spec.Replicas
		newMS.Status.Replicas = desired.machineDeployment.Status.Replicas
		newMS.Status.AvailableReplicas = desired.machineDeployment.Status.AvailableReplicas
	} else {
		totMachineSets++
		newMS = &clusterv1.MachineSet{
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
	}

	// Add a want machines to desired state, with
	// - the new spec from the MD (steady state)
	desiredMachines := []*clusterv1.Machine{}
	for _, machineSetMachineName := range desiredMachineNames {
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
	return desired
}

// GetNextMachineUID provides a predictable UID for machines.
func (r *rolloutScope) GetNextMachineUID() int32 {
	r.machineUID++
	return r.machineUID
}

func (r rolloutScope) String() string {
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("%s, %d/%d replicas\n", r.machineDeployment.Name, ptr.Deref(r.machineDeployment.Status.Replicas, 0), ptr.Deref(r.machineDeployment.Spec.Replicas, 0)))

	sort.Slice(r.machineSets, func(i, j int) bool { return r.machineSets[i].Name < r.machineSets[j].Name })
	for _, ms := range r.machineSets {
		machineNames := []string{}
		for _, m := range r.machineSetMachines[ms.Name] {
			machineNames = append(machineNames, m.Name)
		}
		sb.WriteString(fmt.Sprintf("- %s, %d/%d replicas (%s)\n", ms.Name, ptr.Deref(ms.Status.Replicas, 0), ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machineNames, ",")))
	}
	return sb.String()
}

func (r *rolloutScope) Equal(s *rolloutScope) bool {
	return machineDeploymentIsEqual(r.machineDeployment, s.machineDeployment) && machineSetsAreEqual(r.machineSets, s.machineSets) && machineSetMachinesAreEqual(r.machineSetMachines, s.machineSetMachines)
}

func machineDeploymentIsEqual(a, b *clusterv1.MachineDeployment) bool {
	if a.Spec.ClusterName != b.Spec.ClusterName ||
		ptr.Deref(a.Spec.Replicas, 0) != ptr.Deref(b.Spec.Replicas, 0) ||
		ptr.Deref(a.Status.Replicas, 0) != ptr.Deref(b.Status.Replicas, 0) ||
		ptr.Deref(a.Status.AvailableReplicas, 0) != ptr.Deref(b.Status.AvailableReplicas, 0) {
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
		if desiredMS.Spec.ClusterName != currentMS.Spec.ClusterName ||
			ptr.Deref(desiredMS.Spec.Replicas, 0) != ptr.Deref(currentMS.Spec.Replicas, 0) ||
			ptr.Deref(desiredMS.Status.Replicas, 0) != ptr.Deref(currentMS.Status.Replicas, 0) ||
			ptr.Deref(desiredMS.Status.AvailableReplicas, 0) != ptr.Deref(currentMS.Status.AvailableReplicas, 0) {
			return false
		}
	}
	return true
}

func machineSetMachinesAreEqual(a, b map[string][]*clusterv1.Machine) bool {
	for ms, aMachines := range a {
		bMachines, ok := b[ms]
		if !ok {
			if len(aMachines) > 0 {
				return false
			}
			continue
		}

		if len(aMachines) != len(bMachines) {
			return false
		}

		for i := range aMachines {
			if aMachines[i].Name != bMachines[i].Name {
				return false
			}
		}
	}
	return true
}

// msControllerMutator fakes a small part fo the MS controller, just what is require for the rollout to progress.
func msControllerMutator(log *logger, ms *clusterv1.MachineSet, scope *rolloutScope, directives []string) {
	d := sets.NewString(directives...)

	if d.Has(fmt.Sprintf("%s-SKIP-RECONCILE", ms.Name)) {
		return
	}

	// Update counters
	ms.Status.Replicas = ptr.To(int32(len(scope.machineSetMachines[ms.Name])))
	ms.Status.AvailableReplicas = ms.Status.Replicas

	// if too few machines, create missing machine.
	// new machines are created with a predictable name, so it is easier to write test case and validate rollout sequences.
	// e.g. if the cluster is initialize with m1, m2, m3, new machines will be m4, m5, m6
	if ptr.Deref(ms.Spec.Replicas, 0) > ptr.Deref(ms.Status.Replicas, 0) {
		if sourceMSs, ok := ms.Annotations[scaleUpWaitForReplicasAnnotationName]; ok && sourceMSs != "" {
			log.Logf("[MS controller] %s is waiting for replicas from %s to scale up to %d/%[3]d replicas", ms.Name, sourceMSs, ptr.Deref(ms.Spec.Replicas, 0))
		} else {
			machinesToAdd := ptr.Deref(ms.Spec.Replicas, 0) - ptr.Deref(ms.Status.Replicas, 0)
			machinesAdded := []string{}
			for range machinesToAdd {
				machineName := fmt.Sprintf("m%d", scope.GetNextMachineUID())
				scope.machineSetMachines[ms.Name] = append(scope.machineSetMachines[ms.Name],
					&clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: machineName}},
				)
				machinesAdded = append(machinesAdded, machineName)
			}
			log.Logf("[MS controller] %s scale up to %d/%[2]d replicas (%s created)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesAdded, ","))
		}
	}
	// if too many replicas, delete exceeding machines.
	// exceeding machines are deleted in predictable order, so it is easier to write test case and validate rollout sequences.
	// e.g. if a ms has m1,m2,m3 created in this order, m1 will be deleted first, then m2 and finally m3.
	if ptr.Deref(ms.Spec.Replicas, 0) < ptr.Deref(ms.Status.Replicas, 0) {
		if targetMSName, ok := ms.Annotations[scaleDownMovingToAnnotationName]; ok && targetMSName != "" {
			{
				var targetMS *clusterv1.MachineSet
				for _, ms2 := range scope.machineSets {
					if ms2.Name == targetMSName {
						targetMS = ms2
						break
					}
				}
				if targetMS == nil {
					log.Logf("[MS controller] PANIC! %s is set to send replicas to %s, which does not exists", ms.Name, targetMSName)
					return
				}

				validSourceMSs, _ := targetMS.Annotations[scaleUpWaitForReplicasAnnotationName]
				sourcesSet := sets.Set[string]{}
				sourcesSet.Insert(strings.Split(validSourceMSs, ",")...)
				if !sourcesSet.Has(ms.Name) {
					log.Logf("[MS controller] PANIC! %s is set to send replicas to %s, but %[2]s only accepts machines from %s", ms.Name, targetMS.Name, validSourceMSs)
					return
				}

				machinesToMove := ptr.Deref(ms.Status.Replicas, 0) - ptr.Deref(ms.Spec.Replicas, 0)
				machinesMoved := []string{}
				for i := range machinesToMove {
					machinesMoved = append(machinesMoved, scope.machineSetMachines[ms.Name][i].Name)
					scope.machineSetMachines[targetMS.Name] = append(scope.machineSetMachines[targetMS.Name], scope.machineSetMachines[ms.Name][i])
				}
				scope.machineSetMachines[ms.Name] = scope.machineSetMachines[ms.Name][machinesToMove:]

				log.Logf("[MS controller] %s scale down to %d/%[2]d replicas (%s moved to %s)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesMoved, ","), targetMS.Name)
			}
		} else {
			machinesToDelete := ptr.Deref(ms.Status.Replicas, 0) - ptr.Deref(ms.Spec.Replicas, 0)
			machinesDeleted := []string{}
			for i := range machinesToDelete {
				machinesDeleted = append(machinesDeleted, scope.machineSetMachines[ms.Name][i].Name)
			}
			scope.machineSetMachines[ms.Name] = scope.machineSetMachines[ms.Name][machinesToDelete:]
			log.Logf("[MS controller] %s scale down to %d/%[2]d replicas (%s deleted)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesDeleted, ","))
		}
	}

	// Update counters
	ms.Status.Replicas = ptr.To(int32(len(scope.machineSetMachines[ms.Name])))
	ms.Status.AvailableReplicas = ms.Status.Replicas
}

type logger struct {
	t *testing.T

	testCase              string
	testCaseStringBuilder strings.Builder
}

func newLogger(t *testing.T) *logger {
	return &logger{t: t, testCaseStringBuilder: strings.Builder{}}
}

func (l *logger) NewTestCase(name string) {
	if l.testCase != "" {
		l.testCaseStringBuilder.Reset()
	}
	l.testCaseStringBuilder.WriteString(fmt.Sprintf("## %s\n\n", name))
	l.testCase = name
}

func (l *logger) Logf(format string, args ...interface{}) {
	l.t.Logf(format, args...)

	s := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")
	sb := &strings.Builder{}
	if strings.Contains(s, "\n") {
		lines := strings.Split(s, "\n")
		for _, line := range lines {
			indent := "  "
			if strings.HasPrefix(line, "[") {
				indent = ""
			}
			sb.WriteString(indent + line + "\n")
		}
	} else {
		sb.WriteString(s + "\n")
	}
	l.testCaseStringBuilder.WriteString(sb.String())
}

func (l *logger) EndTestCase() (string, string) {
	os.WriteFile(fmt.Sprintf("%s.test.log", l.testCase), []byte(l.testCaseStringBuilder.String()), 0666)

	currentBytes, _ := os.ReadFile(fmt.Sprintf("%s.test.log", l.testCase))
	current := string(currentBytes)

	goldenBytes, _ := os.ReadFile(fmt.Sprintf("%s.test.log.golden", l.testCase))
	golden := string(goldenBytes)

	return current, golden
}
