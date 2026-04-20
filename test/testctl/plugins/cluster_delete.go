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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/testctl/core"
)

const ClusterDeletePluginKey = "delete.cluster.capi"

// ClusterDeletePlugin can be used to delete a Cluster.
type ClusterDeletePlugin struct{}

func init() {
	registerPlugin(ClusterDeletePluginKey, &ClusterDeletePlugin{})
}

var _ core.MessageGeneratorPlugin = &ClusterDeletePlugin{}

// GenerateMessage generate a message text for the plugin call.
func (p ClusterDeletePlugin) GenerateMessage(objects core.TestObjects, _ any) (string, error) {
	cluster, err := p.getInfo(objects)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Delete Cluster %s", klog.KObj(cluster)), nil
}

var _ core.CallStackValidatorPlugin = &ClusterDeletePlugin{}

// ValidateCallStack validate the call for the plugin.
func (p ClusterDeletePlugin) ValidateCallStack(_ context.Context, callStack []string) error {
	if !slices.Contains(callStack, ClusterSelectorPluginKey) {
		return errors.Errorf("this plugin can only be called as a child of %s", ClusterSelectorPluginKey)
	}
	return nil
}

var _ core.ExecutorPlugin = &ClusterDeletePlugin{}

// Exec this plugin for the given TestObject.
func (p ClusterDeletePlugin) Exec(ctx context.Context, c client.Client, objects core.TestObjects, _ any, runConfig core.RunConfig) error {
	cluster, err := p.getInfo(objects)
	if err != nil {
		return err
	}

	log := ctrl.LoggerFrom(ctx).WithValues("Cluster", klog.KObj(cluster))
	ctx = ctrl.LoggerInto(ctx, log)

	if !cluster.DeletionTimestamp.IsZero() {
		log.Info("Upgrade Cluster action skipped, Cluster already is deleting")
		return nil
	}

	log.Info("Deleting Cluster")
	if ptr.Deref(runConfig.DryRun, false) {
		return nil
	}

	if err := c.Delete(ctx, cluster); err != nil {
		return errors.Wrapf(err, "failed to delete Cluster %s", klog.KObj(cluster))
	}

	if ptr.Deref(runConfig.SkipWait, false) {
		return nil
	}

	log.Info("Waiting for Cluster to be deleted")
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		tmpCluster := &clusterv1.Cluster{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(cluster), tmpCluster); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	}); err != nil {
		return err
	}

	log.Info("Deleted Cluster")

	return nil
}

func (p ClusterDeletePlugin) getInfo(objects core.TestObjects) (*clusterv1.Cluster, error) {
	cluster, err := GetClusterObject(objects)
	if err != nil {
		return nil, err
	}
	return cluster, nil
}
