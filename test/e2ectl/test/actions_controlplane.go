/*
Copyright 2026 The Kubernetes Authors.

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

package test

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
)

func runControlPlaneAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, input ControlPlaneActionConfig, skipWait, dryRun bool) error {
	if cluster.Spec.ControlPlaneRef.Kind != "KubeadmControlPlane" {
		panic("Support for control plane with kind different than KubeadmControlPlane not implemented yet")
	}

	controlPlane, err := getControlPlane(ctx, c, cluster)
	if err != nil {
		return err
	}

	log := ctrl.LoggerFrom(ctx).WithValues(cluster.Spec.ControlPlaneRef.Kind, klog.KObj(controlPlane))
	ctx = ctrl.LoggerInto(ctx, log)

	if input.Scale != nil {
		if err := runControlPlaneScaleAction(ctx, c, cluster, controlPlane, input.Scale.Replicas, skipWait, dryRun); err != nil {
			return errors.Wrapf(err, "failed to run %s %s scale action", cluster.Spec.ControlPlaneRef.Kind, klog.KObj(controlPlane))
		}
	}

	return nil
}

func runControlPlaneScaleAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, controlPlane *controlplanev1.KubeadmControlPlane, replicas int32, skipWait, dryRun bool) error {
	log := ctrl.LoggerFrom(ctx)

	currentReplicas := ptr.Deref(controlPlane.Status.Replicas, 0)
	if currentReplicas == replicas && ptr.Deref(cluster.Spec.Topology.ControlPlane.Replicas, 0) == replicas {
		log.Info(fmt.Sprintf("Scaling KubeadmControlPlane action skipped, KubeadmControlPlane already have %d replicas", replicas))
		return nil
	}

	log.Info(fmt.Sprintf("Scaling KubeadmControlPlane from %d to %d replicas", currentReplicas, replicas))
	if dryRun {
		return nil
	}

	original := cluster.DeepCopy()
	cluster.Spec.Topology.ControlPlane.Replicas = ptr.To(replicas)
	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return errors.Wrapf(err, "failed to patch %s %s", cluster.Spec.ControlPlaneRef.Kind, klog.KObj(controlPlane))
	}

	if skipWait {
		return nil
	}

	log.Info(fmt.Sprintf("Waiting for KubeadmControlPlane to have %d replicas", replicas))
	var retryErr error
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		done, err, retryErr = waitForControlPlaneMachines(ctx, c, cluster, controlPlane, replicas, cluster.Spec.Topology.Version)
		return done, err
	}); err != nil || retryErr != nil {
		return kerrors.NewAggregate([]error{retryErr, err})
	}

	log.Info(fmt.Sprintf("Scale KubeadmControlPlane from %d to %d replicas completed", currentReplicas, replicas))
	return nil
}

func getControlPlane(ctx context.Context, c client.Client, cluster *clusterv1.Cluster) (*controlplanev1.KubeadmControlPlane, error) {
	controlPlane := &controlplanev1.KubeadmControlPlane{}
	key := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      cluster.Spec.ControlPlaneRef.Name,
	}
	if err := c.Get(ctx, key, controlPlane); err != nil {
		return nil, errors.Wrapf(err, "failed to get %s %s for Cluster %s", cluster.Spec.ControlPlaneRef.Kind, klog.KObj(controlPlane), klog.KObj(cluster))
	}
	return controlPlane, nil
}

func getControlPlaneMachines(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, controlPlane *controlplanev1.KubeadmControlPlane) ([]*clusterv1.Machine, error) {
	machineList := &clusterv1.MachineList{}

	listOptions := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{
			clusterv1.MachineControlPlaneLabel: "",
			clusterv1.ClusterNameLabel:         cluster.Name,
		},
	}
	if err := c.List(ctx, machineList, listOptions...); err != nil {
		return nil, errors.Wrapf(err, "failed to list Machines for KubeadmControlPlane %s", klog.KObj(controlPlane))
	}

	machines := []*clusterv1.Machine{}
	for _, machine := range machineList.Items {
		machines = append(machines, &machine)
	}
	return machines, nil
}

func waitForControlPlaneMachines(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, controlPlane *controlplanev1.KubeadmControlPlane, replicas int32, version string) (done bool, err error, retryErr error) {
	machines, err := getControlPlaneMachines(ctx, c, cluster, controlPlane)
	if err != nil {
		return false, err, nil
	}

	if int32(len(machines)) != replicas {
		return false, nil, errors.Errorf("waiting for %d KubeadmControlPlane machines to exist, found %d", replicas, len(machines))
	}

	for _, m := range machines {
		if m.Spec.Version != version {
			return false, nil, errors.Errorf("waiting for %d KubeadmControlPlane machines to have version %s", replicas, version)
		}

		if !conditions.IsTrue(m, clusterv1.MachineAvailableCondition) {
			return false, nil, errors.Errorf("waiting for %d KubeadmControlPlane machines to have Available condition True", replicas)
		}
	}
	return true, nil, retryErr
}
