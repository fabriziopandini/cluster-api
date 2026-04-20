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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/testctl/core"
)

const MachineDeploymentSelectorPluginKey = "selector.machineDeployment.capi"

// MachineDeploymentSelectorObjectKey is the key used to identify the MachineDeployment object in TestObjects.
const MachineDeploymentSelectorObjectKey = "MachineDeployment"

// MachineDeploymentSelectorPlugin can be used to select MachineDeployments for a Cluster.
type MachineDeploymentSelectorPlugin struct{}

func init() {
	registerPlugin(MachineDeploymentSelectorPluginKey, &MachineDeploymentSelectorPlugin{})
}

var _ core.MessageGeneratorPlugin = &MachineDeploymentSelectorPlugin{}

// GenerateMessage generate a message text for the plugin call.
func (p MachineDeploymentSelectorPlugin) GenerateMessage(objects core.TestObjects, pluginConfigUntyped any) (string, error) {
	_, cluster, err := p.getInfo(objects, pluginConfigUntyped)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Select MachineDeployments in Cluster %s", klog.KObj(cluster)), nil
}

var _ core.CallStackValidatorPlugin = &MachineDeploymentSelectorPlugin{}

// ValidateCallStack validate the call for the plugin.
func (p MachineDeploymentSelectorPlugin) ValidateCallStack(_ context.Context, callStack []string) error {
	if !slices.Contains(callStack, ClusterSelectorPluginKey) {
		return errors.Errorf("this plugin can only be called as a child of %s", ClusterSelectorPluginKey)
	}
	return nil
}

var _ core.ConfigurablePlugin = &MachineDeploymentSelectorPlugin{}

// MachineDeploymentSelectorPluginConfig defines the config for the MachineDeploymentSelectorPlugin.
type MachineDeploymentSelectorPluginConfig struct {
	TopologyNameRegex string `json:"topologyNameRegex,omitempty"`
}

// ParseConfig parse the config for this plugin.
func (p MachineDeploymentSelectorPlugin) ParseConfig(_ context.Context, rawPluginConfig []byte) (any, error) {
	config := &MachineDeploymentSelectorPluginConfig{}
	if err := yaml.UnmarshalStrict(rawPluginConfig, config); err != nil {
		return nil, err
	}

	// FIXME: validate regex

	return config, nil
}

var _ core.SelectorPlugin = &MachineDeploymentSelectorPlugin{}

// Select the TestObjects to apply test to, in this case a Cluster's MachineDeployments.
func (p *MachineDeploymentSelectorPlugin) Select(ctx context.Context, c client.Client, objects core.TestObjects, pluginConfigUntyped any, runConfig core.RunConfig) ([]core.TestObjects, error) {
	config, cluster, err := p.getInfo(objects, pluginConfigUntyped)
	if err != nil {
		return nil, err
	}

	machineDeployments, err := getMachineDeployments(ctx, c, cluster, config.TopologyNameRegex, runConfig.Limit)
	if err != nil {
		return nil, err
	}

	ret := make([]core.TestObjects, len(machineDeployments))
	for i := range machineDeployments {
		ret[i] = map[string]any{
			ClusterObjectKey:                   cluster,
			MachineDeploymentSelectorObjectKey: machineDeployments[i],
		}
	}

	return ret, nil
}

func (p *MachineDeploymentSelectorPlugin) getInfo(objects core.TestObjects, pluginConfigUntyped any) (*MachineDeploymentSelectorPluginConfig, *clusterv1.Cluster, error) {
	config := &MachineDeploymentSelectorPluginConfig{}
	if pluginConfigUntyped != nil {
		config = pluginConfigUntyped.(*MachineDeploymentSelectorPluginConfig)
	}

	cluster, err := GetClusterObject(objects)
	if err != nil {
		return config, nil, err
	}

	return config, cluster, nil
}

// GetMachineDeploymentObject extract the MachineDeployment from TestObjects.
func GetMachineDeploymentObject(objects core.TestObjects) (*clusterv1.MachineDeployment, error) {
	c, ok := objects[MachineDeploymentSelectorObjectKey]
	if !ok {
		return nil, errors.Errorf("failed to get the %s object from test objects. You must run the selector.machineDeployment.capi plugin before invoking this plugin", MachineDeploymentSelectorObjectKey)
	}

	controlPlane, ok := c.(*clusterv1.MachineDeployment)
	if !ok {
		return nil, errors.Errorf("failed to cast the %s object to the target type", MachineDeploymentSelectorObjectKey)
	}
	return controlPlane, nil
}
