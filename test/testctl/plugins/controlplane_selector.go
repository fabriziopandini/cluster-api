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

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	"sigs.k8s.io/cluster-api/test/testctl/core"
)

const ControlPlaneSelectorPluginKey = "selector.controlPlane.capi"

// ControlPlaneObjectKey is the key used to identify the ControlPlane object in TestObjects.
const ControlPlaneObjectKey = "ControlPlane"

// ControlPlaneSelectorPlugin can be used to select the Cluster's ControlPlane.
type ControlPlaneSelectorPlugin struct{}

func init() {
	registerPlugin(ControlPlaneSelectorPluginKey, &ControlPlaneSelectorPlugin{})
}

var _ core.MessageGeneratorPlugin = &ControlPlaneSelectorPlugin{}

// GenerateMessage generate a message text for the plugin call.
func (p ControlPlaneSelectorPlugin) GenerateMessage(objects core.TestObjects, _ any) (string, error) {
	cluster, err := p.getInfo(objects)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Select control plane in Cluster %s", klog.KObj(cluster)), nil
}

var _ core.CallStackValidatorPlugin = &ControlPlaneSelectorPlugin{}

// ValidateCallStack validate the call for the plugin.
func (p ControlPlaneSelectorPlugin) ValidateCallStack(_ context.Context, callStack []string) error {
	if !slices.Contains(callStack, ClusterSelectorPluginKey) {
		return errors.Errorf("this plugin can only be called as a child of %s", ClusterSelectorPluginKey)
	}
	return nil
}

var _ core.SelectorPlugin = &ControlPlaneSelectorPlugin{}

// Select the TestObjects to apply test to, in this case the Cluster's ControlPlane.
func (p *ControlPlaneSelectorPlugin) Select(ctx context.Context, c client.Client, objects core.TestObjects, _ any, _ core.RunConfig) ([]core.TestObjects, error) {
	cluster, err := p.getInfo(objects)
	if err != nil {
		return nil, err
	}

	controlPlane, err := getControlPlane(ctx, c, cluster)
	if err != nil {
		return nil, err
	}

	return []core.TestObjects{
		{
			ClusterObjectKey:      cluster,
			ControlPlaneObjectKey: controlPlane,
		},
	}, nil
}

func (p *ControlPlaneSelectorPlugin) getInfo(objects core.TestObjects) (*clusterv1.Cluster, error) {
	cluster, err := GetClusterObject(objects)
	if err != nil {
		return nil, err
	}

	if cluster.Spec.ControlPlaneRef.Kind != "KubeadmControlPlane" {
		return nil, errors.Errorf("unable to select control plane for Cluster %s. Support for control plane with kind different than KubeadmControlPlane not implemented yet", klog.KObj(cluster))
	}

	return cluster, nil
}

// GetControlPlaneObject extract the ControlPlane from TestObjects.
func GetControlPlaneObject(objects core.TestObjects) (*controlplanev1.KubeadmControlPlane, error) {
	c, ok := objects[ControlPlaneObjectKey]
	if !ok {
		return nil, errors.Errorf("failed to get the %s object from test objects. You must run the selector.controlPlane.capi plugin before invoking this plugin", ControlPlaneObjectKey)
	}

	controlPlane, ok := c.(*controlplanev1.KubeadmControlPlane)
	if !ok {
		return nil, errors.Errorf("failed to cast the %s object to the target type", ControlPlaneObjectKey)
	}
	return controlPlane, nil
}
