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
	"os"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Config define the configuration for a sequence of test actions.
type Config struct {
	// tests defines a list of test sequences.
	// tests are run in parallel.
	Tests []ActionList `json:"tests"`
}

// ActionList define a sequence of test actions to be run on a selected group of Clusters.
type ActionList struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// clusterSelectors defines the list of Clusters which are candidate for this test.
	//
	// If clusterSelectors is not set, the test applies to all Clusters.
	// If clusterSelectors contains multiple selectors, the results are ORed.
	ClusterSelectors []metav1.LabelSelector `json:"clusterSelectors,omitempty"`

	// clusterNameRegex defines a regex used to select among the list of Clusters which are candidate.
	// If clusterNameRegex is not set, all candidate Clusters will be considered.
	ClusterNameRegex string `json:"clusterNameRegex,omitempty"`

	// limitClusters defines the max number of Clusters that will be considered for this test.
	// If limitClusters is not set, all candidate Clusters will be considered.
	LimitClusters *int32 `json:"limitClusters,omitempty"`

	// concurrency defines the number of Clusters to be tested in parallel.
	// If not set, it defaults to 1.
	Concurrency *int32 `json:"concurrency,omitempty"`

	// failFast defines the behavior in case a test action fails for a Cluster.
	// If not set, it defaults to false.
	// Note that in case concurrency is greater than 1, the system will try to
	// complete other tests running in parallel before exit,
	FailFast *bool `json:"failFast,omitempty"`

	// FIXME: Add timeout per action (here or at lower levels) + sane defaults, e.g. X times replicas / operation.

	// Actions defines the list of test actions to be performed on a Cluster.
	Actions []ClusterTestActionConfig `json:"actions"`
}

// ClusterTestActionConfig defines a test action to be performed on a Cluster.
type ClusterTestActionConfig struct {
	Cluster            *ClusterActionConfig           `json:"cluster,omitempty"`
	ControlPlane       *ControlPlaneActionConfig      `json:"controlPlane,omitempty"`
	MachineDeployments *MachineDeploymentActionConfig `json:"machineDeployments,omitempty"`

	SkipWait *bool `json:"skipWait,omitempty"`

	debug *debugStepConfig
}

// ClusterActionConfig defines configuration for a Cluster test action.
type ClusterActionConfig struct {
	Upgrade *ClusterUpgradeActionConfig `json:"upgrade,omitempty"`
}

// ClusterUpgradeActionConfig defines configuration for a Cluster upgrade action.
type ClusterUpgradeActionConfig struct {
	Version string `json:"version"`
}

// ControlPlaneActionConfig defines configuration for a control plane test action.
type ControlPlaneActionConfig struct {
	Scale *ControlPlaneScaleActionConfig `json:"scale,omitempty"`
}

// ControlPlaneScaleActionConfig defines configuration for a control plane scale action.
type ControlPlaneScaleActionConfig struct {
	Replicas int32 `json:"replicas"`
}

// MachineDeploymentActionConfig defines configuration for a test action to be run on a selected group of MachineDeployments.
type MachineDeploymentActionConfig struct {
	TopologyNameRegex string `json:"topologyNameRegex,omitempty"`
	Limit             *int   `json:"limit,omitempty"`

	Scale  *MachineDeploymentScaleActionConfig  `json:"scale,omitempty"`
	Create *MachineDeploymentCreateActionConfig `json:"create,omitempty"`
	Delete *MachineDeploymentDeleteActionConfig `json:"delete,omitempty"`
}

// MachineDeploymentScaleActionConfig defines configuration for a MachineDeployment scale action.
type MachineDeploymentScaleActionConfig struct {
	Replicas     *int32 `json:"replicas,omitempty"`
	ReplicasDiff *int32 `json:"replicasDiff,omitempty"`
}

// MachineDeploymentCreateActionConfig defines configuration for a MachineDeployment create action.
type MachineDeploymentCreateActionConfig struct {
	Count int32 `json:"count"`

	// GenerateName is an optional prefix, used by the server, to generate a unique
	// name ONLY IF the template.name field has not been provided.
	GenerateName string                              `json:"generateName,omitempty"`
	Template     clusterv1.MachineDeploymentTopology `json:"template,omitempty"`
}

// MachineDeploymentDeleteActionConfig defines configuration for a MachineDeployment delete action.
type MachineDeploymentDeleteActionConfig struct {
}

type debugStepConfig struct {
	f func(ctx context.Context, i *clusterv1.Cluster)
}

// ReadConfig reads the configuration for tests to be run on CAPI Clusters.
func ReadConfig(file string) (*Config, error) {
	_, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.Errorf("file %s does not exist", file)
	}
	configData, err := os.ReadFile(file) //nolint:gosec
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read file %s", file)
	}

	config := &Config{}
	if err := yaml.Unmarshal(configData, config); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal file %s", file)
	}

	return config, nil
}
