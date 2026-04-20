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

package plugins

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/testctl/core"
)

const MachineDeploymentDeletePluginKey = "delete.machineDeployment.capi"

// MachineDeploymentDeletePlugin can be used to delete MachineDeployments for a Cluster.
type MachineDeploymentDeletePlugin struct{}

func init() {
	registerPlugin(MachineDeploymentDeletePluginKey, &MachineDeploymentDeletePlugin{})
}

var _ core.MessageGeneratorPlugin = &MachineDeploymentDeletePlugin{}

// GenerateMessage generate a message text for the plugin call.
func (p MachineDeploymentDeletePlugin) GenerateMessage(objects core.TestObjects, _ any) (string, error) {
	cluster, machineDeployment, _, err := p.getInfo(objects)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Delete MachineDeployments %s in Cluster %s", klog.KObj(machineDeployment), klog.KObj(cluster)), nil
}

var _ core.CallStackValidatorPlugin = &MachineDeploymentDeletePlugin{}

// ValidateCallStack validate the call for the plugin.
func (p MachineDeploymentDeletePlugin) ValidateCallStack(_ context.Context, callStack []string) error {
	if !slices.Contains(callStack, MachineDeploymentSelectorPluginKey) {
		return errors.Errorf("this plugin can only be called as a child of %s", MachineDeploymentSelectorPluginKey)
	}
	return nil
}

var _ core.ExecutorPlugin = &MachineDeploymentDeletePlugin{}

// Exec this plugin for the given TestObject.
func (p MachineDeploymentDeletePlugin) Exec(ctx context.Context, c client.Client, objects core.TestObjects, _ any, runConfig core.RunConfig) error {
	cluster, machineDeployment, mdTopologyIndex, err := p.getInfo(objects)
	if err != nil {
		return err
	}

	if mdTopologyIndex == -1 {
		// TODO: Log? MD CR still exists, but it has been already removed from the cluster.
		return nil
	}

	log := ctrl.LoggerFrom(ctx).WithValues("Cluster", klog.KObj(cluster), "MachineDeployment", klog.KObj(machineDeployment))
	ctx = ctrl.LoggerInto(ctx, log)

	log.Info("Deleting MachineDeployment")
	if ptr.Deref(runConfig.DryRun, false) {
		return nil
	}

	original := cluster.DeepCopy()
	cluster.Spec.Topology.Workers.MachineDeployments = slices.Delete(cluster.Spec.Topology.Workers.MachineDeployments, mdTopologyIndex, mdTopologyIndex+1)
	if len(cluster.Spec.Topology.Workers.MachineDeployments) == 0 {
		cluster.Spec.Topology.Workers.MachineDeployments = nil
	}
	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return errors.Wrapf(err, "failed to patch Cluster %s", klog.KObj(cluster))
	}

	if ptr.Deref(runConfig.SkipWait, false) {
		return nil
	}

	log.Info("Waiting for MachineDeployment to be deleted")
	var retryErr error
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		retryErr = nil
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

func (p MachineDeploymentDeletePlugin) getInfo(objects core.TestObjects) (*clusterv1.Cluster, *clusterv1.MachineDeployment, int, error) {
	cluster, err := GetClusterObject(objects)
	if err != nil {
		return nil, nil, 0, err
	}

	if !cluster.Spec.Topology.IsDefined() {
		return nil, nil, 0, errors.Errorf("unable to delete MachineDeployments for Cluster %s. support for clusters without spec.topology not implemented yet", klog.KObj(cluster))
	}

	machineDeployment, err := GetMachineDeploymentObject(objects)
	if err != nil {
		return nil, nil, 0, err
	}

	mdTopologyName, ok := machineDeployment.Labels[clusterv1.ClusterTopologyMachineDeploymentNameLabel]
	if !ok {
		return nil, nil, 0, errors.Errorf("MachineDeployment doesn't have the %s label", clusterv1.ClusterTopologyMachineDeploymentNameLabel)
	}

	mdTopologyIndex := getMachineDeploymentTopologyIndex(cluster, mdTopologyName)

	return cluster, machineDeployment, mdTopologyIndex, nil
}

// FIXME: find a better place for this func.

func getMachineDeploymentTopologyIndex(cluster *clusterv1.Cluster, mdTopologyName string) int {
	mdTopologyIndex := -1
	for i := range cluster.Spec.Topology.Workers.MachineDeployments {
		if cluster.Spec.Topology.Workers.MachineDeployments[i].Name == mdTopologyName {
			mdTopologyIndex = i
			break
		}
	}
	return mdTopologyIndex
}
