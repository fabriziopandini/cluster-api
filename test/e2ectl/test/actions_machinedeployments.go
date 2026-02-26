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
	"regexp"
	"slices"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
)

func runMachineDeploymentsAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, input MachineDeploymentActionConfig, skipWait, dryRun bool) error {
	var machineDeployments []*clusterv1.MachineDeployment
	if input.Create != nil {
		for i := int32(0); i <= input.Create.Count; i++ {
			name := input.Create.Template.Name
			if name == "" {
				name = fmt.Sprintf("%s%d", input.Create.GenerateName, len(cluster.Spec.Topology.Workers.MachineDeployments)+1)
			}

			md := &clusterv1.MachineDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: cluster.Namespace,
					Name:      name,
					Labels: map[string]string{
						clusterv1.ClusterTopologyMachineDeploymentNameLabel: name,
					},
				},
			}
			machineDeployments = append(machineDeployments, md)
		}
	} else {
		var err error
		machineDeployments, err = getMachineDeployments(ctx, c, cluster, input.TopologyNameRegex, input.Limit)
		if err != nil {
			return err
		}
	}

	_, err := newTaskRunner[runMachineDeploymentActionsInput, runMachineDeploymentActionsOutput](
		1,     // Always edit one MachineDeployment at time
		false, // Always try to modify all the MachineDeployments, no matter if one fails
		runMachineDeploymentAction,
	).Run(ctx, c, newMachineDeploymentActionInputs(cluster, machineDeployments, input, skipWait), dryRun)

	return err
}

type runMachineDeploymentActionsInput struct {
	cluster           *clusterv1.Cluster
	machineDeployment *clusterv1.MachineDeployment
	action            MachineDeploymentActionConfig
	skipWait          bool
}

type runMachineDeploymentActionsOutput struct{}

func newMachineDeploymentActionInputs(cluster *clusterv1.Cluster, machineDeployments []*clusterv1.MachineDeployment, action MachineDeploymentActionConfig, skipWait bool) (r []runMachineDeploymentActionsInput) {
	for _, machineDeployment := range machineDeployments {
		r = append(r, runMachineDeploymentActionsInput{
			cluster:           cluster,
			machineDeployment: machineDeployment,
			action:            action,
			skipWait:          skipWait,
		})
	}
	return
}

func runMachineDeploymentAction(ctx context.Context, c client.Client, input runMachineDeploymentActionsInput, dryRun bool) (*runMachineDeploymentActionsOutput, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("MachineDeployment", klog.KObj(input.machineDeployment))
	ctx = ctrl.LoggerInto(ctx, log)

	mdTopologyName, ok := input.machineDeployment.Labels[clusterv1.ClusterTopologyMachineDeploymentNameLabel]
	if !ok {
		return nil, errors.Errorf("MachineDeployment doesn't have the %s label", clusterv1.ClusterTopologyMachineDeploymentNameLabel)
	}
	mdTopologyIndex := -1
	for i := range input.cluster.Spec.Topology.Workers.MachineDeployments {
		if input.cluster.Spec.Topology.Workers.MachineDeployments[i].Name == mdTopologyName {
			mdTopologyIndex = i
			break
		}
	}
	if mdTopologyIndex == -1 {
		if input.action.Delete != nil {
			// TODO: Log? MD CR still exists, but it has been already removed from the cluster.
			return nil, nil
		}
		if input.action.Scale != nil {
			return nil, errors.Errorf("Cannot file a MachineDeployment with name %s in cluster.spec.topology.workers.machineDeployments", clusterv1.ClusterTopologyMachineDeploymentNameLabel)
		}
	}

	if input.action.Scale != nil {
		replicas := ptr.Deref(input.cluster.Spec.Topology.Workers.MachineDeployments[mdTopologyIndex].Replicas, 0)
		if input.action.Scale.ReplicasDiff != nil {
			replicas += *input.action.Scale.ReplicasDiff
		}
		if input.action.Scale.Replicas != nil {
			replicas = *input.action.Scale.Replicas
		}

		if err := runMachineDeploymentScaleAction(ctx, c, input.cluster, input.machineDeployment, mdTopologyIndex, replicas, input.skipWait, dryRun); err != nil {
			return nil, errors.Wrapf(err, "failed to run MachineDeployment %s scale action", klog.KObj(input.machineDeployment))
		}
	}
	if input.action.Create != nil {
		mdTopology := input.action.Create.Template
		mdTopology.Name = input.machineDeployment.Name
		if err := runMachineDeploymentCreateAction(ctx, c, input.cluster, input.machineDeployment, mdTopologyIndex, mdTopology, input.skipWait, dryRun); err != nil {
			return nil, errors.Wrapf(err, "failed to run MachineDeployment %s create action", klog.KObj(input.machineDeployment))
		}
	}
	if input.action.Delete != nil {
		if err := runMachineDeploymentDeleteAction(ctx, c, input.cluster, input.machineDeployment, mdTopologyIndex, input.skipWait, dryRun); err != nil {
			return nil, errors.Wrapf(err, "failed to run MachineDeployment %s delete action", klog.KObj(input.machineDeployment))
		}
	}
	return nil, nil
}

func runMachineDeploymentScaleAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, machineDeployment *clusterv1.MachineDeployment, mdTopologyIndex int, replicas int32, skipWait, dryRun bool) error {
	log := ctrl.LoggerFrom(ctx)

	currentReplicas := ptr.Deref(machineDeployment.Status.Replicas, 0)
	if currentReplicas == replicas && ptr.Deref(cluster.Spec.Topology.Workers.MachineDeployments[mdTopologyIndex].Replicas, 0) == replicas {
		log.Info(fmt.Sprintf("Scaling MachineDeployment action skipped, MachineDeployment already have %d replicas", replicas))
		return nil
	}

	log.Info(fmt.Sprintf("Scaling MachineDeployment from %d to %d replicas", currentReplicas, replicas))
	if dryRun {
		return nil
	}

	original := cluster.DeepCopy()
	cluster.Spec.Topology.Workers.MachineDeployments[mdTopologyIndex].Replicas = ptr.To(replicas)
	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return errors.Wrapf(err, "failed to patch %s %s", cluster.Spec.ControlPlaneRef.Kind, klog.KObj(machineDeployment))
	}

	if skipWait {
		return nil
	}

	log.Info(fmt.Sprintf("Waiting for MachineDeployment to have %d replicas", replicas))
	var retryErr error
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		done, err, retryErr = waitForMachineDeploymentMachines(ctx, c, cluster, machineDeployment, replicas, cluster.Spec.Topology.Version)
		return done, err
	}); err != nil || retryErr != nil {
		return kerrors.NewAggregate([]error{retryErr, err})
	}

	log.Info(fmt.Sprintf("Scale MachineDeployment from %d to %d replicas completed", currentReplicas, replicas))
	return nil
}

func runMachineDeploymentCreateAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, machineDeployment *clusterv1.MachineDeployment, mdTopologyIndex int, mdTopology clusterv1.MachineDeploymentTopology, skipWait, dryRun bool) error {
	log := ctrl.LoggerFrom(ctx)

	if mdTopologyIndex >= 0 {
		log.Info("Creating MachineDeployment action skipped, MachineDeployment already exists")
		return nil
	}

	log.Info("Creating MachineDeployment")
	if dryRun {
		return nil
	}

	original := cluster.DeepCopy()
	cluster.Spec.Topology.Workers.MachineDeployments = append(cluster.Spec.Topology.Workers.MachineDeployments, mdTopology)
	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return errors.Wrapf(err, "failed to patch %s %s", cluster.Spec.ControlPlaneRef.Kind, klog.KObj(machineDeployment))
	}

	if skipWait {
		return nil
	}

	log.Info("Waiting for MachineDeployment to be created")
	var retryErr error
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		machineDeploymentList := &clusterv1.MachineDeploymentList{}
		listOptions := []client.ListOption{
			client.InNamespace(cluster.Namespace),
			client.MatchingLabels{
				clusterv1.ClusterNameLabel:                          cluster.Name,
				clusterv1.ClusterTopologyMachineDeploymentNameLabel: mdTopology.Name,
			},
		}
		if err := c.List(ctx, machineDeploymentList, listOptions...); err != nil {
			return false, err
		}
		if len(machineDeploymentList.Items) != 1 {
			retryErr = errors.New("waiting for MachineDeployment be created")
			return false, nil
		}

		done, err, retryErr = waitForMachineDeploymentMachines(ctx, c, cluster, &machineDeploymentList.Items[0], ptr.Deref(mdTopology.Replicas, 0), cluster.Spec.Topology.Version)
		return done, err
	}); err != nil || retryErr != nil {
		return kerrors.NewAggregate([]error{retryErr, err})
	}

	log.Info("Create MachineDeployment completed")
	return nil
}

func runMachineDeploymentDeleteAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, machineDeployment *clusterv1.MachineDeployment, mdTopologyIndex int, skipWait, dryRun bool) error {
	log := ctrl.LoggerFrom(ctx)

	log.Info("Deleting MachineDeployment")
	if dryRun {
		return nil
	}

	original := cluster.DeepCopy()
	cluster.Spec.Topology.Workers.MachineDeployments = slices.Delete(cluster.Spec.Topology.Workers.MachineDeployments, mdTopologyIndex, mdTopologyIndex+1)
	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return errors.Wrapf(err, "failed to patch %s %s", cluster.Spec.ControlPlaneRef.Kind, klog.KObj(machineDeployment))
	}

	if skipWait {
		return nil
	}

	log.Info("Waiting for MachineDeployment to be deleted")
	var retryErr error
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		md := &clusterv1.MachineDeployment{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(machineDeployment), md); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		retryErr = errors.New("waiting for MachineDeployment be deleted")
		return false, nil
	}); err != nil || retryErr != nil {
		return kerrors.NewAggregate([]error{retryErr, err})
	}

	log.Info("Delete MachineDeployment completed")
	return nil
}

func getMachineDeployments(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, topologyNameRegEx string, limit *int) ([]*clusterv1.MachineDeployment, error) {
	machineDeploymentList := &clusterv1.MachineDeploymentList{}

	listOptions := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: cluster.Name},
	}
	if err := c.List(ctx, machineDeploymentList, listOptions...); err != nil {
		return nil, errors.Wrapf(err, "failed to list MachineDeployments for Cluster %s", klog.KObj(cluster))
	}

	var regex *regexp.Regexp
	if topologyNameRegEx != "" {
		var err error
		regex, err = regexp.Compile(topologyNameRegEx)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse nameRegex %s", topologyNameRegEx)
		}
	}

	machineDeployments := make([]*clusterv1.MachineDeployment, 0, len(machineDeploymentList.Items))
	for _, machineDeployment := range machineDeploymentList.Items {
		if topologyName, ok := machineDeployment.Labels[clusterv1.ClusterTopologyMachineDeploymentNameLabel]; ok && regex != nil {
			if !regex.MatchString(topologyName) {
				continue
			}
		}

		machineDeployments = append(machineDeployments, &machineDeployment)
		if limit != nil && len(machineDeployments) >= ptr.Deref(limit, 0) {
			break
		}
	}
	return machineDeployments, nil
}

func getMachineDeploymentMachines(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, machineDeployment *clusterv1.MachineDeployment) ([]*clusterv1.Machine, error) {
	machineList := &clusterv1.MachineList{}

	listOptions := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{
			clusterv1.ClusterNameLabel: cluster.Name,
		},
		client.MatchingLabels(machineDeployment.Spec.Selector.MatchLabels),
	}
	if err := c.List(ctx, machineList, listOptions...); err != nil {
		return nil, errors.Wrapf(err, "failed to list Machines for MachineDeployment %s", klog.KObj(machineDeployment))
	}

	machines := []*clusterv1.Machine{}
	for _, machine := range machineList.Items {
		machines = append(machines, &machine)
	}
	return machines, nil
}

func waitForMachineDeploymentMachines(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, machineDeployment *clusterv1.MachineDeployment, replicas int32, version string) (done bool, err error, retryErr error) {
	machines, err := getMachineDeploymentMachines(ctx, c, cluster, machineDeployment)
	if err != nil {
		return false, err, nil
	}

	if int32(len(machines)) != replicas {
		return false, nil, errors.Errorf("waiting for %d MachineDeployment machines to exist, found %d", replicas, len(machines))
	}

	for _, m := range machines {
		if m.Spec.Version != version {
			return false, nil, errors.Errorf("waiting for %d KubeadmControlPlane machines to have version %s", replicas, version)
		}

		if !conditions.IsTrue(m, clusterv1.MachineAvailableCondition) {
			return false, nil, errors.Errorf("waiting for %d MachineDeployment machines to have Available condition True", replicas)
		}
	}
	return true, nil, retryErr
}
