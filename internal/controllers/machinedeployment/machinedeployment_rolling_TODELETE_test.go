/*
Copyright 2021 The Kubernetes Authors.

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

/*
import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	apirand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/machinedeployment/mdutil"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conversion"
)

func TestReconcileNewMachineSet(t *testing.T) {
	testCases := []struct {
		name                          string
		machineDeployment             *clusterv1.MachineDeployment
		newMachineSet                 *clusterv1.MachineSet
		oldMachineSets                []*clusterv1.MachineSet
		expectedNewMachineSetReplicas int
		error                         error
	}{
		{
			name: "RollingUpdate strategy: Scale up: 0 -> 2",
			machineDeployment: &clusterv1.MachineDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineDeploymentSpec{
					Rollout: clusterv1.MachineDeploymentRolloutSpec{
						Strategy: clusterv1.MachineDeploymentRolloutStrategy{
							Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
							RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
								MaxUnavailable: intOrStrPtr(0),
								MaxSurge:       intOrStrPtr(2),
							},
						},
					},
					Replicas: ptr.To[int32](2),
				},
			},
			newMachineSet: &clusterv1.MachineSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineSetSpec{
					Replicas: ptr.To[int32](0),
				},
			},
			expectedNewMachineSetReplicas: 2,
		},
		{
			name: "RollingUpdate strategy: Scale down: 2 -> 0",
			machineDeployment: &clusterv1.MachineDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineDeploymentSpec{
					Rollout: clusterv1.MachineDeploymentRolloutSpec{
						Strategy: clusterv1.MachineDeploymentRolloutStrategy{
							Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
							RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
								MaxUnavailable: intOrStrPtr(0),
								MaxSurge:       intOrStrPtr(2),
							},
						},
					},
					Replicas: ptr.To[int32](0),
				},
			},
			newMachineSet: &clusterv1.MachineSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineSetSpec{
					Replicas: ptr.To[int32](2),
				},
			},
			expectedNewMachineSetReplicas: 0,
		},
		{
			name: "RollingUpdate strategy: Scale up does not go above maxSurge (3+2)",
			machineDeployment: &clusterv1.MachineDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineDeploymentSpec{
					Rollout: clusterv1.MachineDeploymentRolloutSpec{
						Strategy: clusterv1.MachineDeploymentRolloutStrategy{
							Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
							RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
								MaxUnavailable: intOrStrPtr(0),
								MaxSurge:       intOrStrPtr(2),
							},
						},
					},
					Replicas: ptr.To[int32](3),
				},
			},
			newMachineSet: &clusterv1.MachineSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineSetSpec{
					Replicas: ptr.To[int32](1),
				},
			},
			expectedNewMachineSetReplicas: 2,
			oldMachineSets: []*clusterv1.MachineSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "foo",
						Name:      "3replicas",
					},
					Spec: clusterv1.MachineSetSpec{
						Replicas: ptr.To[int32](3),
					},
					Status: clusterv1.MachineSetStatus{
						Replicas: ptr.To[int32](3),
					},
				},
			},
			error: nil,
		},
		{
			name: "RollingUpdate strategy: Scale up accounts for deleting Machines to honour maxSurge",
			machineDeployment: &clusterv1.MachineDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineDeploymentSpec{
					Rollout: clusterv1.MachineDeploymentRolloutSpec{
						Strategy: clusterv1.MachineDeploymentRolloutStrategy{
							Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
							RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
								MaxUnavailable: intOrStrPtr(0),
								MaxSurge:       intOrStrPtr(0),
							},
						},
					},
					Replicas: ptr.To[int32](1),
				},
			},
			newMachineSet: &clusterv1.MachineSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineSetSpec{
					Replicas: ptr.To[int32](0),
				},
			},
			expectedNewMachineSetReplicas: 0,
			oldMachineSets: []*clusterv1.MachineSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "foo",
						Name:      "machine-not-yet-deleted",
					},
					Spec: clusterv1.MachineSetSpec{
						Replicas: ptr.To[int32](0),
					},
					Status: clusterv1.MachineSetStatus{
						Replicas: ptr.To[int32](1),
					},
				},
			},
			error: nil,
		},
		{
			name: "Rolling Updated Cleanup disable machine create annotation",
			machineDeployment: &clusterv1.MachineDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
				},
				Spec: clusterv1.MachineDeploymentSpec{
					Rollout: clusterv1.MachineDeploymentRolloutSpec{
						Strategy: clusterv1.MachineDeploymentRolloutStrategy{
							Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
							RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
								MaxUnavailable: intOrStrPtr(0),
								MaxSurge:       intOrStrPtr(0),
							},
						},
					},
					Replicas: ptr.To[int32](2),
				},
			},
			newMachineSet: &clusterv1.MachineSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
					Name:      "bar",
					Annotations: map[string]string{
						clusterv1.DisableMachineCreateAnnotation: "true",
						clusterv1.DesiredReplicasAnnotation:      "2",
						clusterv1.MaxReplicasAnnotation:          "2",
					},
				},
				Spec: clusterv1.MachineSetSpec{
					Replicas: ptr.To[int32](2),
				},
			},
			expectedNewMachineSetReplicas: 2,
			error:                         nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			planner := newRolloutPlanner()
			planner.md = tc.machineDeployment
			planner.newMS = tc.newMachineSet
			planner.oldMSs = tc.oldMachineSets

			err := planner.reconcileNewMachineSet(ctx)
			if tc.error != nil {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(BeEquivalentTo(tc.error.Error()))
				return
			}

			g.Expect(err).ToNot(HaveOccurred())

			scaleIntent := ptr.Deref(tc.newMachineSet.Spec.Replicas, 0)
			if v, ok := planner.scaleIntents[tc.newMachineSet.Name]; ok {
				scaleIntent = v
			}
			g.Expect(scaleIntent).To(BeEquivalentTo(tc.expectedNewMachineSetReplicas))
		})
	}
}

func Test_reconcileOldMachineSetsRolloutRolling(t *testing.T) {
	var ctx = context.Background()

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
			md:          createMD("v2", 10, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			name:        "do not scale down if there are more replicas than minAvailable replicas, but scale down from a previous reconcile already takes the availability buffer, scale down from a previous reconcile on another MSr",
			scaleIntent: map[string]int32{},
			md:          createMD("v2", 6, withRolloutUpdateStrategy(3, 1)),
			newMS:       createMS("ms3", "v2", 3, withStatusReplicas(0), withStatusAvailableReplicas(0)), // NewMS is scaling up from previous reconcile, but replicas do not exists yet
			oldMSs: []*clusterv1.MachineSet{
				createMS("ms1", "v1", 2, withStatusReplicas(3), withStatusAvailableReplicas(3)), // OldMS is scaling down from a previous reconcile
				createMS("ms2", "v1", 3, withStatusReplicas(3), withStatusAvailableReplicas(3)), // OldMS
			},
			expectScaleIntent: map[string]int32{
				// no new scale down intent for oldMSs (ms1):
				// 3 available replicas from ms1 - 1 replica already scaling down from ms1 + 3 available replica from ms2 = 5 available replicas = minAvailability, we cannot further scale down
			},
		},
		{
			name: "do not scale down if there are more replicas than minAvailable replicas, but scale down from current reconcile already takes the availability buffer",
			scaleIntent: map[string]int32{
				"ms2": 1, // newMS (ms2) has a scaling down intent from current reconcile
			},
			md:    createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:          createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
			md:    createMD("v2", 3, withRolloutUpdateStrategy(1, 0)),
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
				md:           tt.md,
				newMS:        tt.newMS,
				oldMSs:       tt.oldMSs,
				scaleIntents: tt.scaleIntent,
			}
			err := p.reconcileOldMachineSetsRolloutRolling(ctx)
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
				newMS:        tt.newMS,
				oldMSs:       tt.oldMSs,
				scaleIntents: tt.scaleIntent,
			}
			p.reconcileDeadlockBreaker(ctx)
			g.Expect(p.scaleIntents).To(Equal(tt.expectScaleIntent), "unexpected scaleIntents")
		})
	}
}

func TestComputeDesiredNewMS(t *testing.T) {
	t.Run("should compute Revision annotations for newMS, no oldMS", func(t *testing.T) {
		g := NewWithT(t)

		deployment := &clusterv1.MachineDeployment{}

		p := rolloutPlanner{
			md: deployment,
			// Add a dummy computeDesiredMS, that simply return an empty MS object.
			// Note: there is dedicate test to validate the actual computeDesiredMS func, it is ok to simplify the unit test here.
			computeDesiredMS: func(_ context.Context, deployment *clusterv1.MachineDeployment, _ *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
				return &clusterv1.MachineSet{}, nil
			},
		}
		actualNewMS, err := p.computeDesiredNewMS(ctx, nil)
		g.Expect(err).ToNot(HaveOccurred())

		// annotations that we are intentionally not setting in this func are not there
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue(clusterv1.RevisionAnnotation, "1"))
		g.Expect(actualNewMS.Annotations).ShouldNot(HaveKey("machinedeployment.clusters.x-k8s.io/revision-history"))
	})
	t.Run("should update Revision annotations for newMS when required", func(t *testing.T) {
		g := NewWithT(t)

		deployment := &clusterv1.MachineDeployment{}
		currentMS := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					clusterv1.RevisionAnnotation: "1",
				},
			},
		}
		oldMS := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					clusterv1.RevisionAnnotation: "2",
				},
			},
		}

		p := rolloutPlanner{
			md: deployment,
			// Add a dummy computeDesiredMS, that simply pass through the currentNewMS.
			// Note: there is dedicate test to validate the actual computeDesiredMS func, it is ok to simplify the unit test here.
			computeDesiredMS: func(_ context.Context, deployment *clusterv1.MachineDeployment, currentNewMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
				return currentNewMS, nil
			},
			oldMSs: []*clusterv1.MachineSet{oldMS},
		}
		actualNewMS, err := p.computeDesiredNewMS(ctx, currentMS)
		g.Expect(err).ToNot(HaveOccurred())

		// annotations that we are intentionally not setting in this func are not there
		// Note: there is a dedicated test for ComputeRevisionAnnotations, so it is ok to have a minimal coverage here about revision management.
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue(clusterv1.RevisionAnnotation, "3"))
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue("machinedeployment.clusters.x-k8s.io/revision-history", "1"))
	})
	t.Run("should preserve Revision annotations for newMS when already up to date", func(t *testing.T) {
		g := NewWithT(t)

		deployment := &clusterv1.MachineDeployment{}
		currentMS := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					clusterv1.RevisionAnnotation:                           "3",
					"machinedeployment.clusters.x-k8s.io/revision-history": "1",
				},
			},
		}
		oldMS := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					clusterv1.RevisionAnnotation: "2",
				},
			},
		}

		p := rolloutPlanner{
			md: deployment,
			// Add a dummy computeDesiredMS, that simply pass through the currentNewMS.
			// Note: there is dedicate test to validate the actual computeDesiredMS func, it is ok to simplify the unit test here.
			computeDesiredMS: func(_ context.Context, deployment *clusterv1.MachineDeployment, currentNewMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
				return currentNewMS, nil
			},
			oldMSs: []*clusterv1.MachineSet{oldMS},
		}
		actualNewMS, err := p.computeDesiredNewMS(ctx, currentMS)
		g.Expect(err).ToNot(HaveOccurred())

		// annotations that we are intentionally not setting in this func are not there
		// Note: there is a dedicated test for ComputeRevisionAnnotations, so it is ok to have a minimal coverage here about revision management.
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue(clusterv1.RevisionAnnotation, "3"))
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue("machinedeployment.clusters.x-k8s.io/revision-history", "1"))
	})
}

func TestComputeDesiredOldMS(t *testing.T) {
	t.Run("should carry over Revision annotations from oldMS", func(t *testing.T) {
		g := NewWithT(t)
		const revision = "4"
		const revisionHistory = "1,3"

		deployment := &clusterv1.MachineDeployment{}
		currentMS := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					clusterv1.RevisionAnnotation:                           revision,
					"machinedeployment.clusters.x-k8s.io/revision-history": revisionHistory,
				},
			},
		}

		p := rolloutPlanner{
			md: deployment,
			// Add a dummy computeDesiredMS, that simply pass through the currentNewMS.
			// Note: there is dedicate test to validate the actual computeDesiredMS func, it is ok to simplify the unit test here.
			computeDesiredMS: func(_ context.Context, deployment *clusterv1.MachineDeployment, currentNewMS *clusterv1.MachineSet) (*clusterv1.MachineSet, error) {
				return currentNewMS, nil
			},
		}
		actualNewMS, err := p.computeDesiredOldMS(ctx, currentMS)
		g.Expect(err).ToNot(HaveOccurred())

		// annotations that we are intentionally not setting in this func are not there
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue(clusterv1.RevisionAnnotation, revision))
		g.Expect(actualNewMS.Annotations).Should(HaveKeyWithValue("machinedeployment.clusters.x-k8s.io/revision-history", revisionHistory))
	})
}

func TestComputeDesiredMS(t *testing.T) {
	duration10s := ptr.To(int32(10))
	namingTemplateKey := "test"

	infraRef := clusterv1.ContractVersionedObjectReference{
		Kind:     "GenericInfrastructureMachineTemplate",
		Name:     "infra-template-1",
		APIGroup: clusterv1.GroupVersionInfrastructure.Group,
	}
	bootstrapRef := clusterv1.ContractVersionedObjectReference{
		Kind:     "GenericBootstrapConfigTemplate",
		Name:     "bootstrap-template-1",
		APIGroup: clusterv1.GroupVersionBootstrap.Group,
	}

	deployment := &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "md1",
			Annotations: map[string]string{"top-level-annotation": "top-level-annotation-value"},
		},
		Spec: clusterv1.MachineDeploymentSpec{
			ClusterName: "test-cluster",
			Replicas:    ptr.To[int32](3),
			Rollout: clusterv1.MachineDeploymentRolloutSpec{
				Strategy: clusterv1.MachineDeploymentRolloutStrategy{
					Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
					RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
						MaxSurge:       intOrStrPtr(1),
						MaxUnavailable: intOrStrPtr(0),
					},
				},
			},
			Deletion: clusterv1.MachineDeploymentDeletionSpec{
				Order: clusterv1.RandomMachineSetDeletionOrder,
			},
			MachineNaming: clusterv1.MachineNamingSpec{
				Template: "{{ .machineSet.name }}" + namingTemplateKey + "-{{ .random }}",
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"k1": "v1"},
			},
			Template: clusterv1.MachineTemplateSpec{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels:      map[string]string{"machine-label1": "machine-value1"},
					Annotations: map[string]string{"machine-annotation1": "machine-value1"},
				},
				Spec: clusterv1.MachineSpec{
					Version:           "v1.25.3",
					InfrastructureRef: infraRef,
					Bootstrap: clusterv1.Bootstrap{
						ConfigRef: bootstrapRef,
					},
					MinReadySeconds: ptr.To[int32](3),
					ReadinessGates:  []clusterv1.MachineReadinessGate{{ConditionType: "foo"}},
					Deletion: clusterv1.MachineDeletionSpec{
						NodeDrainTimeoutSeconds:        duration10s,
						NodeVolumeDetachTimeoutSeconds: duration10s,
						NodeDeletionTimeoutSeconds:     duration10s,
					},
				},
			},
		},
	}

	skeletonMSBasedOnMD := &clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "default",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				// labels that must be propagated to MS.
				"machine-label1": "machine-value1",
			},
			Annotations: map[string]string{
				// annotations that must be propagated to MS.
				"top-level-annotation": "top-level-annotation-value",
			},
		},
		Spec: clusterv1.MachineSetSpec{
			// Info that we do expect to be copied from the MD.
			ClusterName: deployment.Spec.ClusterName,
			Deletion: clusterv1.MachineSetDeletionSpec{
				Order: deployment.Spec.Deletion.Order,
			},
			Selector:      deployment.Spec.Selector,
			Template:      *deployment.Spec.Template.DeepCopy(),
			MachineNaming: deployment.Spec.MachineNaming,
		},
	}

	t.Run("should compute a new MachineSet when current MS is nil", func(t *testing.T) {
		expectedMS := skeletonMSBasedOnMD.DeepCopy()
		// Replicas should always be set to zero on newMS.
		expectedMS.Spec.Replicas = ptr.To[int32](0)

		g := NewWithT(t)
		actualMS, err := computeDesiredMS(ctx, deployment, nil)
		g.Expect(err).ToNot(HaveOccurred())
		assertDesiredMS(g, deployment, actualMS, expectedMS)
	})

	t.Run("should compute the updated MachineSet when current MS is not nil", func(t *testing.T) {
		uid := apirand.String(5)
		name := "foo"
		finalizers := []string{"pre-existing-finalizer"}
		replicas := ptr.To(int32(1))
		uniqueLabelValue := "123"
		currentMS := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "default",
				CreationTimestamp: metav1.Now(),
				Labels: map[string]string{
					// labels that must be carried over
					clusterv1.MachineDeploymentUniqueLabel: uniqueLabelValue,
					// unknown labels from current MS should not be considered for desired state (known labels are inferred from MD).
					"foo": "bar",
				},
				Annotations: map[string]string{
					// unknown annotations from current MS should not be considered for desired state (known annotations are inferred from MD).
					"foo": "bar",
				},
				// value that must be preserved
				UID:        types.UID(uid),
				Name:       name,
				Finalizers: finalizers,
			},
			Spec: clusterv1.MachineSetSpec{
				// value that must be preserved
				Replicas: replicas,
				// Info that we do expect to be copied from the MD (set to another value to make sure it is overridden).
				Deletion: clusterv1.MachineSetDeletionSpec{
					Order: clusterv1.OldestMachineSetDeletionOrder,
				},
				MachineNaming: clusterv1.MachineNamingSpec{
					Template: "foo",
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "v1"},
				},
				Template: clusterv1.MachineTemplateSpec{
					ObjectMeta: clusterv1.ObjectMeta{
						Labels:      map[string]string{"foo": "machine-value1"},
						Annotations: map[string]string{"foo": "machine-value1"},
					},
					Spec: clusterv1.MachineSpec{
						Version: "foo",
					},
				},
			},
		}

		expectedMS := skeletonMSBasedOnMD.DeepCopy()
		// Fields that are expected to be carried over from oldMS.
		expectedMS.ObjectMeta.Name = name
		expectedMS.Labels[clusterv1.MachineDeploymentUniqueLabel] = uniqueLabelValue
		expectedMS.ObjectMeta.UID = types.UID(uid)
		expectedMS.ObjectMeta.Finalizers = finalizers
		expectedMS.Spec.Replicas = replicas
		expectedMS.Spec.Template = *currentMS.Spec.Template.DeepCopy()
		// Fields that must be taken from the MD
		expectedMS.Spec.Deletion.Order = deployment.Spec.Deletion.Order
		expectedMS.Spec.MachineNaming = deployment.Spec.MachineNaming
		expectedMS.Spec.Template.Labels = mdutil.CloneAndAddLabel(deployment.Spec.Template.Labels, clusterv1.MachineDeploymentUniqueLabel, uniqueLabelValue)
		expectedMS.Spec.Template.Annotations = cloneStringMap(deployment.Spec.Template.Annotations)
		expectedMS.Spec.Template.Spec.MinReadySeconds = deployment.Spec.Template.Spec.MinReadySeconds
		expectedMS.Spec.Template.Spec.ReadinessGates = deployment.Spec.Template.Spec.ReadinessGates
		expectedMS.Spec.Template.Spec.Deletion.NodeDrainTimeoutSeconds = deployment.Spec.Template.Spec.Deletion.NodeDrainTimeoutSeconds
		expectedMS.Spec.Template.Spec.Deletion.NodeDeletionTimeoutSeconds = deployment.Spec.Template.Spec.Deletion.NodeDeletionTimeoutSeconds
		expectedMS.Spec.Template.Spec.Deletion.NodeVolumeDetachTimeoutSeconds = deployment.Spec.Template.Spec.Deletion.NodeVolumeDetachTimeoutSeconds

		g := NewWithT(t)
		actualMS, err := computeDesiredMS(ctx, deployment, currentMS)
		g.Expect(err).ToNot(HaveOccurred())
		assertDesiredMS(g, deployment, actualMS, expectedMS)
	})
}

func assertDesiredMS(g *WithT, md *clusterv1.MachineDeployment, actualMS *clusterv1.MachineSet, expectedMS *clusterv1.MachineSet) {
	// check UID
	if expectedMS.UID != "" {
		g.Expect(actualMS.UID).Should(Equal(expectedMS.UID))
	}
	// Check Name
	if expectedMS.Name != "" {
		g.Expect(actualMS.Name).Should(Equal(expectedMS.Name))
	}
	// Check Namespace
	g.Expect(actualMS.Namespace).Should(Equal(expectedMS.Namespace))

	// Check CreationTimestamp
	g.Expect(actualMS.CreationTimestamp.IsZero()).Should(BeFalse())

	// Check Ownership
	g.Expect(util.IsControlledBy(actualMS, md, clusterv1.GroupVersion.WithKind("MachineDeployment").GroupKind())).Should(BeTrue())

	// Check finalizers
	g.Expect(actualMS.Finalizers).Should(Equal(expectedMS.Finalizers))

	// Check Replicas
	g.Expect(actualMS.Spec.Replicas).ShouldNot(BeNil())
	g.Expect(actualMS.Spec.Replicas).Should(HaveValue(Equal(*expectedMS.Spec.Replicas)))

	// Check ClusterName
	g.Expect(actualMS.Spec.ClusterName).Should(Equal(expectedMS.Spec.ClusterName))

	// Check Labels
	for k, v := range expectedMS.Labels {
		g.Expect(actualMS.Labels).Should(HaveKeyWithValue(k, v))
	}
	for k, v := range expectedMS.Spec.Template.Labels {
		g.Expect(actualMS.Spec.Template.Labels).Should(HaveKeyWithValue(k, v))
	}
	// Verify that the labels also has the unique identifier key.
	g.Expect(actualMS.Labels).Should(HaveKey(clusterv1.MachineDeploymentUniqueLabel))
	g.Expect(actualMS.Spec.Template.Labels).Should(HaveKey(clusterv1.MachineDeploymentUniqueLabel))

	// Check Annotations
	// Note: More nuanced validation of the Revision annotation calculations are done when testing `ComputeMachineSetAnnotations`.
	for k, v := range expectedMS.Annotations {
		g.Expect(actualMS.Annotations).Should(HaveKeyWithValue(k, v))
	}
	// annotations that must not be propagated from MD have been removed.
	g.Expect(actualMS.Annotations).ShouldNot(HaveKey(corev1.LastAppliedConfigAnnotation))
	g.Expect(actualMS.Annotations).ShouldNot(HaveKey(conversion.DataAnnotation))

	// annotations that must be derived from MD have been set.
	g.Expect(actualMS.Annotations).Should(HaveKeyWithValue(clusterv1.DesiredReplicasAnnotation, fmt.Sprintf("%d", *md.Spec.Replicas)))
	g.Expect(actualMS.Annotations).Should(HaveKeyWithValue(clusterv1.MaxReplicasAnnotation, fmt.Sprintf("%d", *(md.Spec.Replicas)+mdutil.MaxSurge(*md))))

	// annotations that we are intentionally not setting in this func are not there
	g.Expect(actualMS.Annotations).ShouldNot(HaveKey(clusterv1.RevisionAnnotation))
	g.Expect(actualMS.Annotations).ShouldNot(HaveKey("machinedeployment.clusters.x-k8s.io/revision-history"))
	g.Expect(actualMS.Annotations).ShouldNot(HaveKey(clusterv1.DisableMachineCreateAnnotation))

	for k, v := range expectedMS.Spec.Template.Annotations {
		g.Expect(actualMS.Spec.Template.Annotations).Should(HaveKeyWithValue(k, v))
	}

	// Check MinReadySeconds
	g.Expect(actualMS.Spec.Template.Spec.MinReadySeconds).Should(Equal(expectedMS.Spec.Template.Spec.MinReadySeconds))

	// Check Order
	g.Expect(actualMS.Spec.Deletion.Order).Should(Equal(expectedMS.Spec.Deletion.Order))

	// Check MachineTemplateSpec
	g.Expect(actualMS.Spec.Template.Spec).Should(BeComparableTo(expectedMS.Spec.Template.Spec))

	// Check MachineNamingSpec
	g.Expect(actualMS.Spec.MachineNaming.Template).Should(BeComparableTo(expectedMS.Spec.MachineNaming.Template))
}
*/
