package experiment

import (
	"context"
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
)

var logger klog.Logger

func TestMain(m *testing.M) {
	logOptions := logs.NewOptions()
	logOptions.Verbosity = logsv1.VerbosityLevel(5)
	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		fmt.Printf("Unable to validate and apply log options: %v\n", err)
		os.Exit(1)
	}
	logger = klog.Background()
	os.Exit(m.Run())
}

func Test_reconcileOldMachineSets(t *testing.T) {
	var ctx = context.Background()
	ctx = ctrl.LoggerInto(ctx, logger)

	tests := []struct {
		name                       string
		md                         *clusterv1.MachineDeployment
		scaleIntent                map[string]int32
		newMS                      *clusterv1.MachineSet
		oldMSs                     []*clusterv1.MachineSet
		expectScaleIntent          map[string]int32
		skipMaxUnavailabilityCheck bool
	}{
		{
			name:        "no op if there are no replicas on old machinesets",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 10, 1, 0),
			newMS:       createMS("ms2", "v2", 10, withStatusReplicas(10), withStatusAvailableReplicas(10)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 0, withStatusReplicas(0), withStatusAvailableReplicas(0)),
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name:        "do not scale down if replicas is equal to minAvailable replicas",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v2", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 2, withStatusReplicas(2), withStatusAvailableReplicas(2)),
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				// 2 available replicas from ms1 + 1 available replica from ms22 = 3 available replicas <= minAvailability, we cannot scale down
			},
		},
		{
			name:        "do not scale down if replicas is less then minAvailable replicas",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v2", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 2, withStatusReplicas(2), withStatusAvailableReplicas(1)),
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				// 1 available replicas from ms1 + 1 available replica from ms22 = 3 available replicas = minAvailability, we cannot scale down
			},
			skipMaxUnavailabilityCheck: true,
		},
		{
			name:        "do not scale down if there are more replicas than minAvailable replicas, but scale down from a previous reconcile already takes the availability buffer",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v2", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 2, withStatusReplicas(3), withStatusAvailableReplicas(3)), // OldMS is scaling down from a previous reconcile
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				// 3 available replicas from ms1 - 1 replica already scaling down from ms1 + 1 available replica from ms2 = 3 available replicas = minAvailability, we cannot further scale down
			},
		},
		{
			name: "do not scale down if there are more replicas than minAvailable replicas, but scale down from current reconcile already takes the availability buffer",
			scaleIntent: map[string]int32{
				"ms2": 1, // newMS (ms2) has a scaling down intent from current reconcile
			},
			md:    createMD("v2", 3, 1, 0),
			newMS: createMS("ms2", "v2", 2, withStatusReplicas(2), withStatusAvailableReplicas(2)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 2, withStatusReplicas(2), withStatusAvailableReplicas(2)),
			},
			expectScaleIntent: map[string]int32{
				"ms2": 1,
				// no new scale down intent for oldMSs (ms1):
				// 2 available replicas from ms1 + 2 available replicas from ms2 - 1 replica already scaling down from ms2 = 3 available replicas = minAvailability, we cannot further scale down
			},
		},
		{
			name:        "do not scale down replicas when there are more replicas than minAvailable replicas, but not all the replicas are available (unavailability on newMS)",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v2", 1, withStatusReplicas(1), withStatusAvailableReplicas(0)), // no replicas are available
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 3, withStatusReplicas(3), withStatusAvailableReplicas(3)),
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				// 3 available replicas from ms1 + 0 available replicas from ms2 = 3 available replicas = minAvailability, we cannot further scale down
			},
		},
		{
			name:        "do not scale down replicas when there are more replicas than minAvailable replicas, but not all the replicas are available (unavailability on oldMS)",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v2", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 3, withStatusReplicas(3), withStatusAvailableReplicas(2)), // only 2 replicas are available
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				// 2 available replicas from ms1 + 1 available replicas from ms2 = 3 available replicas = minAvailability, we cannot further scale down
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, all replicas are available",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v2", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 3, withStatusReplicas(3), withStatusAvailableReplicas(3)),
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs (ms1):
				"ms1": 2, // 3 available replicas from ms1 + 1 available replicas from ms2 = 4 available replicas > minAvailability, scale down to 2 replicas (-1)
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, unavailable replicas are scaled down first",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms4", "v3", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 2, withStatusReplicas(2), withStatusAvailableReplicas(2)),
				createMS("ms2", "v1", 1, withStatusReplicas(1), withStatusAvailableReplicas(0)), // no replicas are available
				createMS("ms3", "v2", 2, withStatusReplicas(2), withStatusAvailableReplicas(0)), // no replicas are available
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs:
				// ms1 skipped in the first iteration because it does not have any unavailable replica
				"ms2": 0, // 0 available replicas from ms2, it can be scaled down with impact on availability (-1)
				"ms3": 0, // 0 available replicas from ms3, it can be scaled down with impact on availability (-2)
				// no need to further scale down.
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, unavailable replicas are scaled down first, available replicas are scaled down when unavailable replicas are gone",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms4", "v3", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 3, withStatusReplicas(3), withStatusAvailableReplicas(3)),
				createMS("ms2", "v1", 1, withStatusReplicas(1), withStatusAvailableReplicas(0)), // no replicas are available
				createMS("ms3", "v2", 2, withStatusReplicas(2), withStatusAvailableReplicas(0)), // no replicas are available
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs:
				// ms1 skipped in the first iteration because it does not have any unavailable replica
				"ms2": 0, // 0 available replicas from ms2, it can be scaled down to 0 without any impact on availability (-1)
				"ms3": 0, // 0 available replicas from ms3, it can be scaled down to 0 without any impact on availability (-1)
				"ms1": 2, // 3 available replicas from ms2 + 1 available replica from ms4 = 4 available replicas > minAvailability, scale down to 2 replicas (-1)
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, unavailable replicas are scaled down first, available replicas are scaled down when unavailable replicas are gone is not affected by replicas without machines",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms4", "v3", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 4, withStatusReplicas(3), withStatusAvailableReplicas(3)), // 1 replica without machine
				createMS("ms2", "v1", 2, withStatusReplicas(1), withStatusAvailableReplicas(0)), // no replicas are available, 1 replica without machine
				createMS("ms3", "v2", 5, withStatusReplicas(2), withStatusAvailableReplicas(0)), // no replicas are available, 2 replicas without machines
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs:
				// ms1 skipped in the first iteration because it does not have any unavailable replica
				"ms2": 0, // 0 available replicas from ms2, it can be scaled down to 0 without any impact on availability (-1)
				"ms3": 0, // 0 available replicas from ms3, it can be scaled down to 0 without any impact on availability (-5)
				"ms1": 2, // 3 available replicas from ms2 + 1 available replica from ms4 = 4 available replicas > minAvailability, scale down to 2 replicas, also drop the replica without machines (-2)
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, unavailable replicas are scaled down first, scale down stops before breaching minAvailable replicas",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms4", "v3", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
				createMS("ms2", "v1", 1, withStatusReplicas(1), withStatusAvailableReplicas(0)), // no replicas are available
				createMS("ms3", "v2", 2, withStatusReplicas(2), withStatusAvailableReplicas(1)), // only 1 replica is available
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs:
				"ms2": 0, // 0 available replicas from ms2, it can be scaled down to 0 without any impact on availability (-1)
				// even if there is still room to scale down, we cannot scale down ms3: 1 available replica from ms1 + 1 available replica from ms4 = 2 available replicas < minAvailability
				// does not make sense to continue scale down.
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, unavailable replicas are scaled down first, scale down stops before breaching minAvailable replicas is not affected by replicas without machines",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms4", "v3", 1, withStatusReplicas(1), withStatusAvailableReplicas(1)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 2, withStatusReplicas(1), withStatusAvailableReplicas(1)), // 1 replica without machine
				createMS("ms2", "v1", 3, withStatusReplicas(1), withStatusAvailableReplicas(0)), // no replicas are available, 2 replica without machines
				createMS("ms3", "v2", 3, withStatusReplicas(2), withStatusAvailableReplicas(1)), // only 1 replica is available, 1 replica without machine
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs:
				"ms1": 1, // 1 replica without machine, it can be scaled down to 1 without any impact on availability (-1)
				"ms2": 0, // 1 replica without machine, 0 available replicas from ms2, it can be scaled down to 0 without any impact on availability (-2)
				"ms3": 2, // 1 replica without machine, it can be scaled down to 2 without any impact on availability (-1); even if there is still room to scale down, we cannot further scale down ms3: 1 available replica from ms1 + 1 available replica from ms4 = 2 available replicas < minAvailability
				// does not make sense to continue scale down.
			},
		},
		{
			name:        "scale down replicas when there are more replicas than minAvailable replicas, scale down keeps into account scale downs from a previous reconcile",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 3, 1, 0),
			newMS:       createMS("ms2", "v3", 2, withStatusReplicas(2), withStatusAvailableReplicas(2)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 3, withStatusReplicas(4), withStatusAvailableReplicas(4)), // OldMS is scaling down from a previous reconcile
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs:
				"ms1": 2, // 4 available replicas from ms1 - 1 replica already scaling down from ms1 + 2 available replicas from ms2 = 4 available replicas > minAvailability, scale down to 2 (-1)
			},
		},
		{
			name: "scale down replicas when there are more replicas than minAvailable replicas, scale down keeps into account scale downs from the current reconcile",
			scaleIntent: map[string]int32{
				"ms2": 1, // newMS (ms2) has a scaling down intent from current reconcile
			},
			md:    createMD("v2", 3, 1, 0),
			newMS: createMS("ms2", "v3", 2, withStatusReplicas(2), withStatusAvailableReplicas(2)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v0", 3, withStatusReplicas(3), withStatusAvailableReplicas(3)),
			},
			expectScaleIntent: map[string]int32{
				"ms2": 1,
				// new scale down intent for oldMSs:
				"ms1": 2, // 3 available replicas from ms1 + 2 available replicas from ms2 - 1 replica already scaling down from ms2 = 4 available replicas > minAvailability, scale down to 2 (-1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			p := &rolloutPlanner{
				scaleIntents: tt.scaleIntent,
			}
			err := p.reconcileOldMachineSets(ctx, tt.md, tt.newMS, tt.oldMSs)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(p.scaleIntents).To(Equal(tt.expectScaleIntent), "unexpected scaleIntents")

			// Check we are not breaching minAvailability by simulating what will happen by applying intent + worst scenario when a machine deletion is always an available machine deletion.
			for _, oldMS := range tt.oldMSs {
				scaleIntent, ok := p.scaleIntents[oldMS.Name]
				if !ok {
					continue
				}
				machineScaleDown := max(ptr.Deref(oldMS.Status.Replicas, 0)-scaleIntent, 0)
				if machineScaleDown > 0 {
					oldMS.Status.AvailableReplicas = ptr.To(max(ptr.Deref(oldMS.Status.AvailableReplicas, 0)-machineScaleDown, 0))
				}
			}
			minAvailableReplicas := ptr.Deref(tt.md.Spec.Replicas, 0) - mdutil.MaxUnavailable(*tt.md)
			totAvailableReplicas := ptr.Deref(mdutil.GetAvailableReplicaCountForMachineSets(append(tt.oldMSs, tt.newMS)), 0)
			if !tt.skipMaxUnavailabilityCheck {
				g.Expect(totAvailableReplicas).To(BeNumerically(">=", minAvailableReplicas), "totAvailable machines is less than MinUnavailable")
			} else {
				t.Logf("skipping MaxUnavailability check (totAvailableReplicas: %d, minAvailableReplicas: %d)", totAvailableReplicas, minAvailableReplicas)
			}
		})
	}
}

func Test_reconcileDeadlockBreaker(t *testing.T) {
	var ctx = context.Background()
	ctx = ctrl.LoggerInto(ctx, logger)

	tests := []struct {
		name                       string
		scaleIntent                map[string]int32
		newMS                      *clusterv1.MachineSet
		oldMSs                     []*clusterv1.MachineSet
		expectScaleIntent          map[string]int32
		skipMaxUnavailabilityCheck bool
	}{
		{
			name:        "no op if there are no replicas on old machinesets",
			scaleIntent: map[string]int32{},
			newMS:       createMS("ms2", "v2", 10, withStatusReplicas(10), withStatusAvailableReplicas(10)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 0, withStatusReplicas(0), withStatusAvailableReplicas(0)),
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name:        "no op if all the replicas on OldMS are available",
			scaleIntent: map[string]int32{},
			newMS:       createMS("ms2", "v2", 5, withStatusReplicas(5), withStatusAvailableReplicas(5)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 5, withStatusReplicas(5), withStatusAvailableReplicas(5)),
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name:        "no op if there are scale operation still in progress from a previous reconcile on newMS",
			scaleIntent: map[string]int32{},
			newMS:       createMS("ms2", "v2", 6, withStatusReplicas(5), withStatusAvailableReplicas(5)), // scale up from a previous reconcile
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 5, withStatusReplicas(5), withStatusAvailableReplicas(4)), // one unavailable replica, potential deadlock
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name:        "no op if there are scale operation still in progress from a previous reconcile on oldMS",
			scaleIntent: map[string]int32{},
			newMS:       createMS("ms2", "v2", 5, withStatusReplicas(5), withStatusAvailableReplicas(5)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 4, withStatusReplicas(5), withStatusAvailableReplicas(4)), // one unavailable replica, potential deadlock, scale down from a previous reconcile
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name: "no op if there are scale operation from the current reconcile on newMS",
			scaleIntent: map[string]int32{
				"ms2": 6, // scale up intent for newMS
			},
			newMS: createMS("ms2", "v2", 5, withStatusReplicas(5), withStatusAvailableReplicas(5)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 5, withStatusReplicas(5), withStatusAvailableReplicas(4)), // one unavailable replica, potential deadlock
			},
			expectScaleIntent: map[string]int32{
				"ms2": 6,
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name: "no op if there are scale operation from the current reconcile on oldMS",
			scaleIntent: map[string]int32{
				"ms1": 4, // scale up intent for oldMS
			},
			newMS: createMS("ms2", "v2", 5, withStatusReplicas(5), withStatusAvailableReplicas(5)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 5, withStatusReplicas(5), withStatusAvailableReplicas(4)), // one unavailable replica, potential deadlock
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				"ms1": 4,
			},
		},
		{
			name:        "wait for unavailable replicas on the newMS if any",
			scaleIntent: map[string]int32{},
			newMS:       createMS("ms2", "v2", 5, withStatusReplicas(5), withStatusAvailableReplicas(3)), // one unavailable replica
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 5, withStatusReplicas(5), withStatusAvailableReplicas(4)), // one unavailable replica, potential deadlock
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
			},
		},
		{
			name:        "unblock a deadlock when necessary",
			scaleIntent: map[string]int32{},
			newMS:       createMS("ms2", "v2", 5, withStatusReplicas(5), withStatusAvailableReplicas(5)),
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 5, withStatusReplicas(5), withStatusAvailableReplicas(3)), // one unavailable replica, potential deadlock
			},
			expectScaleIntent: map[string]int32{
				// new scale down intent for oldMSs (ms1):
				"ms1": 4,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			p := &rolloutPlanner{
				scaleIntents: tt.scaleIntent,
			}
			p.reconcileDeadlockBreaker(ctx, tt.newMS, tt.oldMSs)
			g.Expect(p.scaleIntents).To(Equal(tt.expectScaleIntent), "unexpected scaleIntents")
		})
	}
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

type machineSetOpt func(*clusterv1.MachineSet)

func withStatusReplicas(r int32) machineSetOpt {
	return func(ms *clusterv1.MachineSet) {
		ms.Status.Replicas = ptr.To(r)
	}
}

func withStatusAvailableReplicas(r int32) machineSetOpt {
	return func(ms *clusterv1.MachineSet) {
		ms.Status.AvailableReplicas = ptr.To(r)
	}
}

func createMS(name, spec string, replicas int32, opts ...machineSetOpt) *clusterv1.MachineSet {
	ms := &clusterv1.MachineSet{
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
	for _, opt := range opts {
		opt(ms)
	}
	return ms
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
