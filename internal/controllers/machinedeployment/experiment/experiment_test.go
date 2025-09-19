package experiment

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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

type rolloutSequenceTestCase struct {
	name           string
	maxSurge       int32
	maxUnavailable int32

	// currentMachineNames is the list of machines before the rollout, and provides a simplified alternative to currentScope.
	// all the machines in this list are initialized as upToDate and owned by the new MD before the rollout.
	// Please name machines as "mX" where X is a progressive number starting from 1 (do not skip numbers),
	// e.g. "m1","m2","m3"
	currentMachineNames []string

	// currentScope defines the current state at the beginning of the test case.
	// When the test case start from a stable state (there are no previous rollout in progress), use  currentMachineNames instead.
	// Please name machines as "mX" where X is a progressive number starting from 1 (do not skip numbers),
	// e.g. "m1","m2","m3"
	// machineUID must be set to the last used number.
	currentScope *rolloutScope

	// minAvailableBreachSilencers can be used to temporarily silence MinAvailable breaches
	//
	// minAvailableBreachToleration: func(log *logger, i int, scope *rolloutScope, minAvailableReplicas, totAvailableReplicas int32) bool {
	// 		if i == 5 {
	// 			t.Log("[Toleration] tolerate minAvailable breach after scale up")
	// 			return true
	// 		}
	// 		return false
	// 	},
	minAvailableBreachToleration func(log *logger, i int, scope *rolloutScope, minAvailableReplicas, totAvailableReplicas int32) bool

	// maxSurgeBreachToleration can be used to temporarily silence MaxSurge breaches
	// (see minAvailableBreachToleration example)
	maxSurgeBreachToleration func(log *logger, i int, scope *rolloutScope, maxAllowedReplicas, totReplicas int32) bool

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

	// name of the log to file and golden file.
	logAndGoldenFileName string

	// randomControllerOrder force the tests to run controllers in random order, mimicking what happens in production.
	// NOTE. We are using a pseudo randomizer, so the random order remains consistent across runs of the same groups of tests.
	randomControllerOrder bool

	// seed value to initialize the generator
	seed int64
}

func Test_rolloutSequencesWithPredictableReconcileOrder(t *testing.T) {
	logOptions := logs.NewOptions()
	logOptions.Verbosity = logsv1.VerbosityLevel(5)
	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		t.Errorf("Unable to validate and apply log options: %v", err)
	}
	logger := klog.Background()

	var ctx = context.Background()
	ctx = ctrl.LoggerInto(ctx, logger)

	fileLogger := newLogger(t)

	tests := []rolloutSequenceTestCase{
		// Regular rollout (no in-place)

		{ // scale out by 1 (maxSurge 0)
			name:                "Regular rollout, 3 Replicas, maxSurge 1, MaxUnavailable 0",
			maxSurge:            1,
			maxUnavailable:      0,
			currentMachineNames: []string{"m1", "m2", "m3"},
			desiredMachineNames: []string{"m4", "m5", "m6"},
		},
		{ // scale in by 1 (maxUnavailable 0)
			name:                "Regular rollout, 3 Replicas, maxSurge 0, MaxUnavailable 1",
			maxSurge:            0,
			maxUnavailable:      1,
			currentMachineNames: []string{"m1", "m2", "m3"},
			desiredMachineNames: []string{"m4", "m5", "m6"},
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable)
			name:                "Regular rollout, 6 Replicas, maxSurge 3, MaxUnavailable 1",
			maxSurge:            3,
			maxUnavailable:      1,
			currentMachineNames: []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames: []string{"m7", "m8", "m9", "m10", "m11", "m12"},
		},
		{ // scale out by 1, scale in by 3 (maxSurge < maxUnavailable)
			name:                "Regular rollout, 6 Replicas, maxSurge 1, MaxUnavailable 3",
			maxSurge:            1,
			maxUnavailable:      3,
			currentMachineNames: []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames: []string{"m7", "m8", "m9", "m10", "m11", "m12"},
		},
		{ // scale out by 10 (maxSurge >= replicas)
			name:                "Regular rollout, 6 Replicas, maxSurge 10, MaxUnavailable 0",
			maxSurge:            10,
			maxUnavailable:      0,
			currentMachineNames: []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames: []string{"m7", "m8", "m9", "m10", "m11", "m12"},
		},
		{ // scale in by 10 (maxUnavailable >= replicas)
			name:                "Regular rollout, 6 Replicas, maxSurge 0, MaxUnavailable 10",
			maxSurge:            0,
			maxUnavailable:      10,
			currentMachineNames: []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames: []string{"m7", "m8", "m9", "m10", "m11", "m12"},
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable) + scale up machine deployment in the middle
			name:           "Regular rollout, 6 Replicas, maxSurge 3, MaxUnavailable 1, scale up to 12",
			maxSurge:       3,
			maxUnavailable: 1,
			currentScope: &rolloutScope{ // Manually providing a scope simulating a MD originally with 6 replica in the middle of a rollout, with 3 machines already created in the newMS and 3 still on the oldMS, and then MD scaled up to 12.
				machineDeployment: createMD("v2", 12, 3, 1),
				machineSets: []*clusterv1.MachineSet{
					createMS("ms1", "v1", 3),
					createMS("ms2", "v2", 3),
				},
				machineSetMachines: map[string][]*clusterv1.Machine{
					"ms1": []*clusterv1.Machine{
						// "m1", "m2", "m3" already deleted
						createM("m4", "ms1", "v1"),
						createM("m5", "ms1", "v1"),
						createM("m6", "ms1", "v1"),
					},
					"ms2": []*clusterv1.Machine{
						createM("m7", "ms2", "v2"),
						createM("m8", "ms2", "v2"),
						createM("m9", "ms2", "v2"),
					},
				},
				machineUID: 9,
			},
			desiredMachineNames:          []string{"m7", "m8", "m9", "m10", "m11", "m12", "m13", "m14", "m15", "m16", "m17", "m18"},
			minAvailableBreachToleration: minAvailableBreachToleration(),
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable) + scale down machine deployment in the middle
			name:                "Regular rollout, 12 Replicas, maxSurge 3, MaxUnavailable 1, scale up to 6",
			maxSurge:            3,
			maxUnavailable:      1,
			currentMachineNames: []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10", "m11", "m12"},
			currentScope: &rolloutScope{ // Manually providing a scope simulating a MD originally with 12 replica in the middle of a rollout, with 3 machines already created in the newMS and 9 still on the oldMS, and then MD scaled down to 6.
				machineDeployment: createMD("v2", 6, 3, 1),
				machineSets: []*clusterv1.MachineSet{
					createMS("ms1", "v1", 9),
					createMS("ms2", "v2", 3),
				},
				machineSetMachines: map[string][]*clusterv1.Machine{
					"ms1": []*clusterv1.Machine{
						// "m1", "m2", "m3" already deleted
						createM("m4", "ms1", "v1"),
						createM("m5", "ms1", "v1"),
						createM("m6", "ms1", "v1"),
						createM("m7", "ms1", "v1"),
						createM("m8", "ms1", "v1"),
						createM("m9", "ms1", "v1"),
						createM("m10", "ms1", "v1"),
						createM("m11", "ms1", "v1"),
						createM("m12", "ms1", "v1"),
					},
					"ms2": []*clusterv1.Machine{
						createM("m13", "ms2", "v2"),
						createM("m14", "ms2", "v2"),
						createM("m15", "ms2", "v2"),
					},
				},
				machineUID: 15,
			},
			desiredMachineNames:      []string{"m13", "m14", "m15", "m16", "m17", "m18"},
			maxSurgeBreachToleration: maxSurgeToleration(),
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable) + change spec in the middle
			name:           "Regular rollout, 6 Replicas, maxSurge 3, MaxUnavailable 1, change spec",
			maxSurge:       3,
			maxUnavailable: 1,
			currentScope: &rolloutScope{ // Manually providing a scope simulating a MD with 6 replica in the middle of a rollout, with 3 machines already created in the newMS and 3 still on the oldMS, and then MD spec is changed.
				machineDeployment: createMD("v3", 6, 3, 1),
				machineSets: []*clusterv1.MachineSet{
					createMS("ms1", "v1", 3),
					createMS("ms2", "v2", 3),
				},
				machineSetMachines: map[string][]*clusterv1.Machine{
					"ms1": []*clusterv1.Machine{
						// "m1", "m2", "m3" already deleted
						createM("m4", "ms1", "v1"),
						createM("m5", "ms1", "v1"),
						createM("m6", "ms1", "v1"),
					},
					"ms2": []*clusterv1.Machine{
						createM("m7", "ms2", "v2"),
						createM("m8", "ms2", "v2"),
						createM("m9", "ms2", "v2"),
					},
				},
				machineUID: 9,
			},
			desiredMachineNames: []string{"m10", "m11", "m12", "m13", "m14", "m15"}, // NOTE: Machines created before the spec change are deleted
		},

		// Rollout with In-place updates

		{ // scale out by 1 (maxSurge 0)
			name:                 "In-place rollout, 3 Replicas, maxSurge 1, MaxUnavailable 0",
			maxSurge:             1,
			maxUnavailable:       0,
			currentMachineNames:  []string{"m1", "m2", "m3"},
			desiredMachineNames:  []string{"m1", "m2", "m4"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale in by 1 (maxUnavailable 0)
			name:                 "In-place rollout, 3 Replicas, maxSurge 0, MaxUnavailable 1",
			maxSurge:             0,
			maxUnavailable:       1,
			currentMachineNames:  []string{"m1", "m2", "m3"},
			desiredMachineNames:  []string{"m1", "m2", "m3"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable)
			name:                "In-place rollout, 6 Replicas, maxSurge 3, MaxUnavailable 1",
			maxSurge:            3,
			maxUnavailable:      1,
			currentMachineNames: []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			// desiredMachineNames: []string{"m1", "m2", "m3", "m4", "m7", "m8"}, // V1, planner is not taking full advantage of maxSurge (depending on ms reconcile order)
			// desiredMachineNames: []string{"m1", "m2", "m3", "m7", "m8", "m9"}, // V2. planner is now taking full advantage of maxSurge; after discussing this, we decide to go in another direction:
			desiredMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"}, // V3: when in-place is possible, try to do as much as possible machines with in-place, even if this implies "ignoring" maxSurge (maxSurge must still be used when doing regular rollouts)
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale out by 1, scale in by 3 (maxSurge < maxUnavailable)
			name:                 "In-place rollout, 6 Replicas, maxSurge 1, MaxUnavailable 3",
			maxSurge:             1,
			maxUnavailable:       3,
			currentMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale out by 10 (maxSurge >= replicas)
			name:                 "In-place rollout, 6 Replicas, maxSurge 10, MaxUnavailable 0",
			maxSurge:             10,
			maxUnavailable:       0,
			currentMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m7"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale in by 10 (maxUnavailable >= replicas)
			name:                 "In-place rollout, 6 Replicas, maxSurge 0, MaxUnavailable 10",
			maxSurge:             0,
			maxUnavailable:       10,
			currentMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			desiredMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable) + scale up machine deployment in the middle
			name:           "In-place rollout, 6 Replicas, maxSurge 3, MaxUnavailable 1, scale up to 12",
			maxSurge:       3,
			maxUnavailable: 1,
			currentScope: &rolloutScope{ // Manually providing a scope simulating a MD originally with 6 replica in the middle of a rollout, with 3 machines already move to the newMS and 3 still on the oldMS, and then MD scaled up to 12.
				machineDeployment: createMD("v2", 12, 3, 1),
				machineSets: []*clusterv1.MachineSet{
					createMS("ms1", "v1", 3),
					createMS("ms2", "v2", 3),
				},
				machineSetMachines: map[string][]*clusterv1.Machine{
					"ms1": []*clusterv1.Machine{
						// "m1", "m2", "m3" already moved to ms2
						createM("m4", "ms1", "v1"),
						createM("m5", "ms1", "v1"),
						createM("m6", "ms1", "v1"),
					},
					"ms2": []*clusterv1.Machine{
						// "m1", "m2", "m3" already updated in place
						createM("m1", "ms2", "v2"),
						createM("m2", "ms2", "v2"),
						createM("m3", "ms2", "v2"),
					},
				},
				machineUID: 6,
			},
			desiredMachineNames:          []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10", "m11", "m12"},
			minAvailableBreachToleration: minAvailableBreachToleration(),
			getCanUpdateDecision:         oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable) + scale down machine deployment in the middle
			name:           "In-place rollout, 12 Replicas, maxSurge 3, MaxUnavailable 1, scale down to 6",
			maxSurge:       3,
			maxUnavailable: 1,
			currentScope: &rolloutScope{ // Manually providing a scope simulating a MD originally with 12 replica in the middle of a rollout, with 3 machines already move to the newMS and 9 still on the oldMS, and then MD scaled down to 6.
				machineDeployment: createMD("v2", 6, 3, 1),
				machineSets: []*clusterv1.MachineSet{
					createMS("ms1", "v1", 9),
					createMS("ms2", "v2", 3),
				},
				machineSetMachines: map[string][]*clusterv1.Machine{
					"ms1": []*clusterv1.Machine{
						// "m1", "m2", "m3" already moved to ms2
						createM("m4", "ms1", "v1"),
						createM("m5", "ms1", "v1"),
						createM("m6", "ms1", "v1"),
						createM("m7", "ms1", "v1"),
						createM("m8", "ms1", "v1"),
						createM("m9", "ms1", "v1"),
						createM("m10", "ms1", "v1"),
						createM("m11", "ms1", "v1"),
						createM("m12", "ms1", "v1"),
					},
					"ms2": []*clusterv1.Machine{
						// "m1", "m2", "m3" already updated in place
						createM("m1", "ms2", "v2"),
						createM("m2", "ms2", "v2"),
						createM("m3", "ms2", "v2"),
					},
				},
				machineUID: 15,
			},
			desiredMachineNames:      []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			maxSurgeBreachToleration: maxSurgeToleration(),
			getCanUpdateDecision:     oldMSCanAlwaysInPlaceUpdate,
		},
		{ // scale out by 3, scale in by 1 (maxSurge > maxUnavailable) + change spec in the middle
			name:           "In-place rollout, 6 Replicas, maxSurge 3, MaxUnavailable 1, change spec",
			maxSurge:       3,
			maxUnavailable: 1,
			currentScope: &rolloutScope{ // Manually providing a scope simulating a MD originally with 6 replica in the middle of a rollout, with 3 machines already move to the newMS and 3 still on the oldMS, and then MD spec is changed.
				machineDeployment: createMD("v3", 6, 3, 1),
				machineSets: []*clusterv1.MachineSet{
					createMS("ms1", "v1", 3),
					createMS("ms2", "v2", 3),
				},
				machineSetMachines: map[string][]*clusterv1.Machine{
					"ms1": []*clusterv1.Machine{
						// "m1", "m2", "m3" already moved to ms2
						createM("m4", "ms1", "v1"),
						createM("m5", "ms1", "v1"),
						createM("m6", "ms1", "v1"),
					},
					"ms2": []*clusterv1.Machine{
						// "m1", "m2", "m3" already updated in place
						createM("m1", "ms2", "v2"),
						createM("m2", "ms2", "v2"),
						createM("m3", "ms2", "v2"),
					},
				},
				machineUID: 6,
			},
			desiredMachineNames:  []string{"m1", "m2", "m3", "m4", "m5", "m6"},
			getCanUpdateDecision: oldMSCanAlwaysInPlaceUpdate,
		},
	}

	testWithPredictableReconcileOrder := true
	testWithRandomReconcileOrderFromConstantSeed := true
	testWithRandomReconcileOrderFromRandomSeed := false
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if testWithPredictableReconcileOrder {
				tt.randomControllerOrder = false
				if tt.logAndGoldenFileName == "" {
					tt.logAndGoldenFileName = strings.ToLower(tt.name)
				}
				t.Run("default", func(t *testing.T) {
					runTestCase(ctx, t, tt, fileLogger)
				})
			}

			if testWithRandomReconcileOrderFromConstantSeed {
				tt.name = fmt.Sprintf("%s, random(0)", tt.name)
				tt.randomControllerOrder = true
				tt.seed = 0
				if tt.logAndGoldenFileName == "" {
					tt.logAndGoldenFileName = strings.ToLower(tt.name)
				}
				t.Run("random(0)", func(t *testing.T) {
					runTestCase(ctx, t, tt, fileLogger)
				})
			}

			if testWithRandomReconcileOrderFromRandomSeed {
				tt.name = fmt.Sprintf("%s, random(x)", tt.name)
				tt.randomControllerOrder = true
				tt.seed = time.Now().UnixNano()
				tt.skipLogToFileAndGoldenFileCheck = true
				t.Run("random(x)", func(t *testing.T) {
					runTestCase(ctx, t, tt, fileLogger)
				})
			}
		})
	}
}

func runTestCase(ctx context.Context, t *testing.T, tt rolloutSequenceTestCase, fileLogger *logger) {
	g := NewWithT(t)

	rng := rand.New(rand.NewSource(tt.seed))
	fileLogger.NewTestCase(tt.name, fmt.Sprintf("resources/%s", tt.logAndGoldenFileName))

	// Init current and desired state from test case
	current := tt.currentScope
	if current == nil {
		current = initCurrentRolloutScope(tt)
	}
	desired := computeDesiredRolloutScope(current, tt.desiredMachineNames)

	// Log initial state
	fileLogger.Logf("[Test] Initial state\n%s", current)
	random := ""
	if tt.randomControllerOrder {
		random = fmt.Sprintf(", random(%d)", tt.seed)
	}
	fileLogger.Logf("[Test] Rollout %d replicas, MaxSurge=%d, MaxUnavailable=%d%s\n", len(tt.currentMachineNames), tt.maxSurge, tt.maxUnavailable, random)
	i := 1
	maxIterations := 100
	for {
		taskOrder := defaultTaskOrder(current)
		if tt.randomControllerOrder {
			taskOrder = randomTaskOrder(current, rng)
		}
		for _, taskID := range taskOrder {
			if taskID == 0 {
				fileLogger.Logf("[MD controller] Iteration %d, Reconcile md", i)
				fileLogger.Logf("[MD controller] - Input to rollout planner\n%s", current)

				// Running a small subset of MD reconcile (the rollout logic and a bit of setReplicas)
				p := &rolloutPlanner{
					getCanUpdateDecision: func(oldMS *clusterv1.MachineSet) bool {
						if tt.getCanUpdateDecision != nil {
							return tt.getCanUpdateDecision(oldMS)
						}
						return false
					},
				}
				machineSets, err := p.rolloutRolling(ctx, current.machineDeployment, current.machineSets, current.machines())
				g.Expect(err).ToNot(HaveOccurred())
				current.machineSets = machineSets
				current.machineDeployment.Status.Replicas = mdutil.GetActualReplicaCountForMachineSets(machineSets)
				current.machineDeployment.Status.AvailableReplicas = mdutil.GetAvailableReplicaCountForMachineSets(machineSets)

				// Log state after this reconcile
				fileLogger.Logf("[MD controller] - Result of rollout planner\n%s", current)

				// Check we are not breaching rollout constraints
				minAvailableReplicas := ptr.Deref(current.machineDeployment.Spec.Replicas, 0) - mdutil.MaxUnavailable(*current.machineDeployment)
				totAvailableReplicas := ptr.Deref(current.machineDeployment.Status.AvailableReplicas, 0)
				if totAvailableReplicas < minAvailableReplicas {
					tolerateBreach := false
					if tt.minAvailableBreachToleration != nil {
						tolerateBreach = tt.minAvailableBreachToleration(fileLogger, i, current, minAvailableReplicas, totAvailableReplicas)
					}
					if !tolerateBreach {
						g.Expect(totAvailableReplicas).To(BeNumerically(">=", minAvailableReplicas), "totAvailable machines is less than MinUnavailable")
					}
				}

				maxAllowedReplicas := ptr.Deref(current.machineDeployment.Spec.Replicas, 0) + mdutil.MaxSurge(*current.machineDeployment)
				// TODO: double check this change
				totReplicas := mdutil.TotalMachineSetsReplicaSum(current.machineSets)
				if totReplicas > maxAllowedReplicas {
					tolerateBreach := false
					if tt.maxSurgeBreachToleration != nil {
						tolerateBreach = tt.maxSurgeBreachToleration(fileLogger, i, current, maxAllowedReplicas, totReplicas)
					}
					if !tolerateBreach {
						g.Expect(totReplicas).To(BeNumerically("<=", maxAllowedReplicas), "totReplicas machines is greater than MaxSurge")
					}
				}
			}

			// Run mutators faking other controllers
			for _, ms := range current.machineSets {
				if fmt.Sprintf("ms%d", taskID) == ms.Name {
					fileLogger.Logf("[MS controller] Iteration %d, Reconcile ms%d, %s", i, taskID, msLog(ms, current.machineSetMachines[ms.Name]))
					machineSetControllerMutator(fileLogger, ms, current)
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
}

// machineSetControllerMutator fakes a small part of the MachineSet controller, just what is required for the rollout to progress.
func machineSetControllerMutator(log *logger, ms *clusterv1.MachineSet, scope *rolloutScope) {
	// Update counters
	// Note: this should not be implemented in production code
	ms.Status.Replicas = ptr.To(int32(len(scope.machineSetMachines[ms.Name])))

	// FIXME: when implementing in production code make sure to:
	//  - detect if there are replicas still pending AcknowledgeMove first, including also handling cleanup of the pendingAcknowledgeMoveAnnotationName on machines
	//  - when deleting or moving
	//  	- first move if possible, then delete
	// 		- move machines should be capped to avoid unnecessary in-place upgrades (in case of scale down in the middle of rollouts); remaining part should be deleted
	//      - do not move machines pending a move Acknowledge, with an in-place upgrade in progress, deleted or marked for deletion, unhealthy

	// Sort machines to ensure stable results of move/delete operations during tests.
	// Note: this should not be implemented in production code
	sortMachineSetMachines(scope.machineSetMachines[ms.Name])

	// Removing updatingInPlaceAnnotation after pendingAcknowledgeMove is gone in a previous reconcile (so inPlaceUpdating lasts one reconcile more)
	// Note: this should not be implemented in production code
	replicasEndingInPlaceUpdate := sets.Set[string]{}
	for _, m := range scope.machineSetMachines[ms.Name] {
		if _, ok := m.Annotations[pendingAcknowledgeMoveAnnotationName]; ok {
			continue
		}
		if _, ok := m.Annotations[updatingInPlaceAnnotationName]; ok {
			delete(m.Annotations, updatingInPlaceAnnotationName)
			replicasEndingInPlaceUpdate.Insert(m.Name)
		}
	}
	if replicasEndingInPlaceUpdate.Len() > 0 {
		log.Logf("[MS controller] - Replicas %s completed in place update", sortAndJoin(replicasEndingInPlaceUpdate.UnsortedList()))
	}

	// If the MachineSet is accepting replicas from other MS (this is the newMS controller by a MD),
	// detect if there are replicas still pending AcknowledgeMove.
	acknowledgeMoveReplicas := getAcknowledgeMoveMachines(ms)
	notAcknowledgeMoveReplicas := sets.Set[string]{}
	if sourceMSs, ok := ms.Annotations[acceptReplicasFromAnnotationName]; ok && sourceMSs != "" {
		for _, m := range scope.machineSetMachines[ms.Name] {
			if _, ok := m.Annotations[pendingAcknowledgeMoveAnnotationName]; !ok {
				continue
			}

			// If machine has been acknowledged by the MachineDeployment, cleanup pending AcknowledgeMove annotation from the machine
			if acknowledgeMoveReplicas.Has(m.Name) {
				delete(m.Annotations, pendingAcknowledgeMoveAnnotationName)
				continue
			}

			// Otherwise keep track of replicas not yet acknowledged.
			notAcknowledgeMoveReplicas.Insert(m.Name)
		}
	} else {
		// Otherwise this MachineSet is not accepting replicas from other MS (this is an oldMS controller by a MD).
		// Drop pendingAcknowledgeMoveAnnotationName from controlled Machines.
		// Note: if there are machines recently moved but not yet accepted, those machines will be managed
		// as any other machine and either moved to the new MS (after completing the in-place upgrade) or deleted.
		for _, m := range scope.machineSetMachines[ms.Name] {
			delete(m.Annotations, pendingAcknowledgeMoveAnnotationName)
		}
	}

	if notAcknowledgeMoveReplicas.Len() > 0 {
		log.Logf("[MS controller] - Replicas %s moved from an old MachineSet still pending acknowledge from machine deployment %s", sortAndJoin(notAcknowledgeMoveReplicas.UnsortedList()), klog.KObj(scope.machineDeployment))
	}

	// if too few machines, create missing machine.
	// new machines are created with a predictable name, so it is easier to write test case and validate rollout sequences.
	// e.g. if the cluster is initialized with m1, m2, m3, new machines will be m4, m5, m6
	machinesToAdd := ptr.Deref(ms.Spec.Replicas, 0) - ptr.Deref(ms.Status.Replicas, 0)
	if machinesToAdd > 0 {
		machinesAdded := []string{}
		for range machinesToAdd {
			machineName := fmt.Sprintf("m%d", scope.GetNextMachineUID())
			scope.machineSetMachines[ms.Name] = append(scope.machineSetMachines[ms.Name],
				&clusterv1.Machine{
					ObjectMeta: metav1.ObjectMeta{
						Name: machineName,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: clusterv1.GroupVersion.String(),
								Kind:       "MachineSet",
								Name:       ms.Name,
								Controller: ptr.To(true),
							},
						},
					},
				},
			)
			machinesAdded = append(machinesAdded, machineName)
		}

		log.Logf("[MS controller] - %s scale up to %d/%[2]d replicas (%s created)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesAdded, ","))
	}

	// if too many replicas, delete exceeding machines.
	// exceeding machines are deleted in predictable order, so it is easier to write test case and validate rollout sequences.
	// e.g. if a ms has m1,m2,m3 created in this order, m1 will be deleted first, then m2 and finally m3.
	// Note: replicas still pending AcknowledgeMove should not be counted when computing the numbers of machines to delete, because those machines are not included in ms.Spec.Replicas yet.
	// Without this check, the following logic would try to align the number of replicas to "an incomplete" ms.Spec.Replicas thus wrongly deleting replicas that should be preserved.
	machinesToDeleteOrMove := max(ptr.Deref(ms.Status.Replicas, 0)-int32(notAcknowledgeMoveReplicas.Len())-ptr.Deref(ms.Spec.Replicas, 0), 0)
	if machinesToDeleteOrMove > 0 {
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
					log.Logf("[MS controller] - PANIC! %s is set to send replicas to %s, which does not exists", ms.Name, targetMSName)
					return
				}

				// Limit the number of machines to be moved to avoid to exceed the final number of replicas for the target MS.
				machinesToMove := min(machinesToDeleteOrMove, ptr.Deref(scope.machineDeployment.Spec.Replicas, 0)-ptr.Deref(targetMS.Spec.Replicas, 0))
				if machinesToMove != machinesToDeleteOrMove {
					log.Logf("[MS controller] - Move capped to %d replicas to avoid unnecessary in-place upgrades", machinesToMove)
				}
				machinesToDeleteOrMove = machinesToDeleteOrMove - machinesToMove

				validSourceMSs, _ := targetMS.Annotations[acceptReplicasFromAnnotationName]
				sourcesSet := sets.Set[string]{}
				sourcesSet.Insert(strings.Split(validSourceMSs, ",")...)
				if !sourcesSet.Has(ms.Name) {
					log.Logf("[MS controller] - PANIC! %s is set to send replicas to %s, but %[2]s only accepts machines from %s", ms.Name, targetMS.Name, validSourceMSs)
					return
				}

				machinesMoved := []string{}
				machinesSetMachines := []*clusterv1.Machine{}
				for i, m := range scope.machineSetMachines[ms.Name] {
					// Make sure we are not deleting machines still pending AcknowledgeMove
					if notAcknowledgeMoveReplicas.Has(m.Name) {
						machinesSetMachines = append(machinesSetMachines, m)
						continue
					}

					if int32(len(machinesMoved)) >= machinesToMove {
						machinesSetMachines = append(machinesSetMachines, scope.machineSetMachines[ms.Name][i:]...)
						break
					}

					m := scope.machineSetMachines[ms.Name][i]
					if m.Annotations == nil {
						m.Annotations = map[string]string{}
					}
					m.OwnerReferences = []metav1.OwnerReference{
						{
							APIVersion: clusterv1.GroupVersion.String(),
							Kind:       "MachineSet",
							Name:       targetMS.Name,
							Controller: ptr.To(true),
						},
					}
					m.Annotations[pendingAcknowledgeMoveAnnotationName] = ""
					m.Annotations[updatingInPlaceAnnotationName] = ""
					scope.machineSetMachines[targetMS.Name] = append(scope.machineSetMachines[targetMS.Name], m)
					machinesMoved = append(machinesMoved, m.Name)
				}
				scope.machineSetMachines[ms.Name] = machinesSetMachines
				log.Logf("[MS controller] - %s scale down to %d/%[2]d replicas (%s moved to %s)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesMoved, ","), targetMS.Name)

				// Sort machines of the target MS to ensure consistent reporting during tests.
				// Note: this is required because can be moved to a target MachineSet reconciled before the source MachineSet (it won't sort machine by itself until the next reconcile).
				sortMachineSetMachines(scope.machineSetMachines[targetMS.Name])
			}
		}
	}

	if machinesToDeleteOrMove > 0 {
		machinesToDelete := machinesToDeleteOrMove
		machinesDeleted := []string{}
		machinesSetMachines := []*clusterv1.Machine{}
		for i, m := range scope.machineSetMachines[ms.Name] {
			if notAcknowledgeMoveReplicas.Has(m.Name) {
				machinesSetMachines = append(machinesSetMachines, m)
				continue
			}
			if int32(len(machinesDeleted)) >= machinesToDelete {
				machinesSetMachines = append(machinesSetMachines, scope.machineSetMachines[ms.Name][i:]...)
				break
			}
			machinesDeleted = append(machinesDeleted, m.Name)
		}
		scope.machineSetMachines[ms.Name] = machinesSetMachines
		log.Logf("[MS controller] - %s scale down to %d/%[2]d replicas (%s deleted)", ms.Name, ptr.Deref(ms.Spec.Replicas, 0), strings.Join(machinesDeleted, ","))
	}

	// Update counters
	// FIXME: this should be implemented in a different way in production code (it depends on how we are going to surface unavailability of machines updating in place).
	ms.Status.Replicas = ptr.To(int32(len(scope.machineSetMachines[ms.Name])))
	availableReplicas := int32(0)
	for _, m := range scope.machineSetMachines[ms.Name] {
		if _, ok := m.Annotations[updatingInPlaceAnnotationName]; ok {
			continue
		}
		availableReplicas++
	}
	ms.Status.AvailableReplicas = ptr.To(availableReplicas)
}

type rolloutScope struct {
	machineDeployment  *clusterv1.MachineDeployment
	machineSets        []*clusterv1.MachineSet
	machineSetMachines map[string][]*clusterv1.Machine

	machineUID int32
}

// Init creates current state and desired state for rolling out a md from currentMachines to wantMachineNames.
func initCurrentRolloutScope(tt rolloutSequenceTestCase) (current *rolloutScope) {
	// create current state, with a MD with
	// - given MaxSurge, MaxUnavailable
	// - replica counters assuming all the machines are at stable state
	// - spec different from the MachineSets and Machines we are going to create down below (to simulate a change that triggers a rollout, but it is not yet started)
	mdReplicaCount := int32(len(tt.currentMachineNames))
	current = &rolloutScope{
		machineDeployment: createMD("v2", mdReplicaCount, tt.maxSurge, tt.maxUnavailable),
	}

	// Create current MS, with
	// - replica counters assuming all the machines are at stable state
	// - spec at stable state (rollout is not yet propagated to machines)
	ms := createMS("ms1", "v1", mdReplicaCount)
	current.machineSets = append(current.machineSets, ms)

	// Create current Machines, with
	// - spec at stable state (rollout is not yet propagated to machines)
	var totMachines int32
	currentMachines := []*clusterv1.Machine{}
	for _, machineSetMachineName := range tt.currentMachineNames {
		totMachines++
		currentMachines = append(currentMachines, createM(machineSetMachineName, ms.Name, ms.Spec.ClusterName))
	}
	current.machineSetMachines = map[string][]*clusterv1.Machine{}
	current.machineSetMachines[ms.Name] = currentMachines

	current.machineDeployment.Spec.Replicas = ptr.To(mdReplicaCount)
	current.machineUID = totMachines

	return current
}

func createMD(spec string, replicas int32, maxSurge, maxUnavailable int32) *clusterv1.MachineDeployment {
	return &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "md"},
		Spec: clusterv1.MachineDeploymentSpec{
			// Note: using ClusterName to track MD revision and detect MD changes
			ClusterName: spec,
			Replicas:    &replicas,
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
			Replicas:          &replicas,
			AvailableReplicas: &replicas,
		},
	}
}

func createMS(name, spec string, replicas int32) *clusterv1.MachineSet {
	return &clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: clusterv1.MachineSetSpec{
			// Note: using ClusterName to track MD revision and detect MD changes
			ClusterName: spec,
			Replicas:    ptr.To(replicas),
		},
		Status: clusterv1.MachineSetStatus{
			Replicas:          ptr.To(replicas),
			AvailableReplicas: ptr.To(replicas),
		},
	}
}

func createM(name, ownedByMS, spec string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "MachineSet",
					Name:       ownedByMS,
					Controller: ptr.To(true),
				},
			},
		},
		Spec: clusterv1.MachineSpec{
			// Note: using ClusterName to track MD revision and detect MD changes
			ClusterName: spec,
		},
	}
}

func computeDesiredRolloutScope(current *rolloutScope, desiredMachineNames []string) (desired *rolloutScope) {
	var totMachineSets, totMachines int32
	totMachineSets = int32(len(current.machineSets))
	for _, msMachines := range current.machineSetMachines {
		totMachines += int32(len(msMachines))
	}

	// Create current state, with a MD equal to the one we started from because:
	// - spec was already changed in current to simulate a change that triggers a rollout
	// - desired replica counters are the same than current replica counters (we start with all the machines at stable state v1, we should end with all the machines at stable state v2)
	desired = &rolloutScope{
		machineDeployment: current.machineDeployment.DeepCopy(),
	}
	desired.machineDeployment.Status.Replicas = desired.machineDeployment.Spec.Replicas
	desired.machineDeployment.Status.AvailableReplicas = desired.machineDeployment.Spec.Replicas

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
		newMS = createMS(fmt.Sprintf("ms%d", totMachineSets), desired.machineDeployment.Spec.ClusterName, *desired.machineDeployment.Spec.Replicas)
		desired.machineSets = append(desired.machineSets, newMS)
	}

	// Add a want machines to desired state, with
	// - the new spec from the MD (steady state)
	desiredMachines := []*clusterv1.Machine{}
	for _, machineSetMachineName := range desiredMachineNames {
		totMachines++
		desiredMachines = append(desiredMachines, createM(machineSetMachineName, newMS.Name, newMS.Spec.ClusterName))
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
		sb.WriteString(fmt.Sprintf("- %s, %s\n", ms.Name, msLog(ms, r.machineSetMachines[ms.Name])))
	}
	return sb.String()
}

func msLog(ms *clusterv1.MachineSet, machines []*clusterv1.Machine) string {
	sb := strings.Builder{}
	machineNames := []string{}
	acknowledgeMoveMachines := getAcknowledgeMoveMachines(ms)
	for _, m := range machines {
		name := m.Name
		if _, ok := m.Annotations[pendingAcknowledgeMoveAnnotationName]; ok && !acknowledgeMoveMachines.Has(name) {
			name = name + "🟠"
		}
		if _, ok := m.Annotations[updatingInPlaceAnnotationName]; ok {
			name = name + "🟡"
		}
		machineNames = append(machineNames, name)
	}
	sb.WriteString(strings.Join(machineNames, ","))
	if moveTo, ok := ms.Annotations[scaleDownMovingToAnnotationName]; ok {
		sb.WriteString(fmt.Sprintf(" => %s", moveTo))
	}
	if moveFrom, ok := ms.Annotations[acceptReplicasFromAnnotationName]; ok {
		sb.WriteString(fmt.Sprintf(" <= %s", moveFrom))
	}
	msLog := fmt.Sprintf("%d/%d replicas (%s)", ptr.Deref(ms.Status.Replicas, 0), ptr.Deref(ms.Spec.Replicas, 0), sb.String())
	return msLog
}

func (r rolloutScope) machines() []*clusterv1.Machine {
	machines := []*clusterv1.Machine{}
	for _, ms := range r.machineSets {
		for _, m := range r.machineSetMachines[ms.Name] {
			machines = append(machines, m)
		}
	}
	return machines
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
			if len(aMachines[i].OwnerReferences) != 1 || len(bMachines[i].OwnerReferences) != 1 || aMachines[i].OwnerReferences[0].Name != bMachines[i].OwnerReferences[0].Name {
				return false
			}
		}
	}
	return true
}

type UniqueRand struct {
	rng       *rand.Rand
	generated map[int]bool // keeps track of
	max       int          // max number to be generated
}

func (u *UniqueRand) Int() int {
	if len(u.generated) >= u.max {
		return -1
	}
	for {
		i := u.rng.Int() % u.max
		if !u.generated[i] {
			u.generated[i] = true
			return i
		}
	}
}

type logger struct {
	t *testing.T

	testCase              string
	fileName              string
	testCaseStringBuilder strings.Builder
}

func newLogger(t *testing.T) *logger {
	return &logger{t: t, testCaseStringBuilder: strings.Builder{}}
}

func (l *logger) NewTestCase(name, fileName string) {
	if l.testCase != "" {
		l.testCaseStringBuilder.Reset()
	}
	l.testCaseStringBuilder.WriteString(fmt.Sprintf("## %s\n\n", name))
	l.testCase = name
	l.fileName = fileName
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
	os.WriteFile(fmt.Sprintf("%s.test.log", l.fileName), []byte(l.testCaseStringBuilder.String()), 0666)
	os.WriteFile(fmt.Sprintf("%s.test.log.golden", l.fileName), []byte(l.testCaseStringBuilder.String()), 0666)

	currentBytes, _ := os.ReadFile(fmt.Sprintf("%s.test.log", l.fileName))
	current := string(currentBytes)

	goldenBytes, _ := os.ReadFile(fmt.Sprintf("%s.test.log.golden", l.fileName))
	golden := string(goldenBytes)

	return current, golden
}

func sortMachineSetMachines(machines []*clusterv1.Machine) {
	sort.Slice(machines, func(i, j int) bool {
		iIndex, _ := strconv.Atoi(strings.TrimPrefix(machines[i].Name, "m"))
		jiIndex, _ := strconv.Atoi(strings.TrimPrefix(machines[j].Name, "m"))
		return iIndex < jiIndex
	})
}

func oldMSCanAlwaysInPlaceUpdate(oldMS *clusterv1.MachineSet) bool {
	return true
}

func minAvailableBreachToleration() func(log *logger, i int, scope *rolloutScope, minAvailableReplicas, totAvailableReplicas int32) bool {
	return func(log *logger, i int, scope *rolloutScope, minAvailableReplicas, totAvailableReplicas int32) bool {
		log.Logf("[Toleration] tolerate minAvailable breach")
		return true
	}
}

func maxSurgeToleration() func(log *logger, i int, scope *rolloutScope, maxAllowedReplicas, totReplicas int32) bool {
	return func(log *logger, i int, scope *rolloutScope, maxAllowedReplicas, totReplicas int32) bool {
		log.Logf("[Toleration] tolerate maxSurge breach")
		return true
	}
}

// default task order ensure the controllers are run in a consistent and predictable way: md, ms1, ms2 and so on
func defaultTaskOrder(current *rolloutScope) []int {
	taskOrder := []int{}
	for t := range len(current.machineSets) + 1 + 1 { // +1 is for the MachineSet that might be created when reconciling md, +1 is for the md itself
		taskOrder = append(taskOrder, t)
	}
	return taskOrder
}

func randomTaskOrder(current *rolloutScope, rng *rand.Rand) []int {
	u := &UniqueRand{
		rng:       rng,
		generated: map[int]bool{},
		max:       len(current.machineSets) + 1 + 1, // +1 is for the MachineSet that might be created when reconciling md, +1 is for the md itself
	}
	taskOrder := []int{}
	for {
		n := u.Int()
		if u.rng.Int()%10 < 3 { // skip a step in the 30% of cases
			continue
		}
		taskOrder = append(taskOrder, n)
		if r := u.rng.Int() % 10; r < 3 { // repeat a step in the 30% of cases
			delete(u.generated, n)
		}
		if len(u.generated) >= u.max {
			break
		}
	}
	return taskOrder
}
