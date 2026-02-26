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

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func runClusterActions(ctx context.Context, c client.Client, input runClusterActionsInput, dryRun bool) (*runClusterActionsOutput, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("Cluster", klog.KObj(input.cluster))
	ctx = ctrl.LoggerInto(ctx, log)

	if !input.cluster.Spec.Topology.IsDefined() {
		panic("Support for clusters without spec.topology not implemented yet")
	}

	for i, s := range input.actions {
		log := ctrl.LoggerFrom(ctx).WithValues("step", i)
		ctx = ctrl.LoggerInto(ctx, log)

		if s.Cluster != nil {
			if s.Cluster.Upgrade != nil {
				if err := runClusterUpgradeAction(ctx, c, input.cluster, s.Cluster.Upgrade.Version, ptr.Deref(s.SkipWait, false), dryRun); err != nil {
					return nil, errors.Wrapf(err, "failed to run Cluster %s upgrade action", klog.KObj(input.cluster))
				}
			}
		}

		if s.ControlPlane != nil {
			if err := runControlPlaneAction(ctx, c, input.cluster, *s.ControlPlane, ptr.Deref(s.SkipWait, false), dryRun); err != nil {
				return nil, errors.Wrapf(err, "failed to run Cluster %s control plane action", klog.KObj(input.cluster))
			}
		}

		if s.MachineDeployments != nil {
			if err := runMachineDeploymentsAction(ctx, c, input.cluster, *s.MachineDeployments, ptr.Deref(s.SkipWait, false), dryRun); err != nil {
				return nil, errors.Wrapf(err, "failed to run Cluster %s MachineDeployments action", klog.KObj(input.cluster))
			}
		}

		if s.debug != nil {
			s.debug.f(ctx, input.cluster)
		}
	}
	return nil, nil
}

func runClusterUpgradeAction(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, version string, skipWait, dryRun bool) error {
	log := ctrl.LoggerFrom(ctx)

	currentVersion := cluster.Spec.Topology.Version
	if currentVersion == version {
		log.Info(fmt.Sprintf("Upgrade Cluster action skipped, Cluster already have version %s", version))
		return nil
	}

	log.Info(fmt.Sprintf("Upgrading Cluster from version %s to %s", currentVersion, version))
	if dryRun {
		return nil
	}

	original := cluster.DeepCopy()
	cluster.Spec.Topology.Version = version
	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return errors.Wrapf(err, "failed to patch Cluster %s", klog.KObj(cluster))
	}

	if skipWait {
		return nil
	}

	log.Info(fmt.Sprintf("Waiting for Cluster to have version %s", version))
	controlPlane, err := getControlPlane(ctx, c, cluster)
	if err != nil {
		return err
	}
	machineDeployments, err := getMachineDeployments(ctx, c, cluster, "", nil)
	if err != nil {
		return err
	}
	var retryErr error
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		done, err, retryErr = waitForControlPlaneMachines(ctx, c, cluster, controlPlane, ptr.Deref(controlPlane.Spec.Replicas, 0), cluster.Spec.Topology.Version)
		if err != nil {
			return done, err
		}
		if retryErr != nil {
			return false, nil //nolint:nilerr
		}

		for _, machineDeployment := range machineDeployments {
			done, err, retryErr = waitForMachineDeploymentMachines(ctx, c, cluster, machineDeployment, ptr.Deref(machineDeployment.Spec.Replicas, 0), cluster.Spec.Topology.Version)
			if err != nil {
				return done, err
			}
			if retryErr != nil {
				return false, nil //nolint:nilerr
			}
		}
		return true, nil
	}); err != nil || retryErr != nil {
		return kerrors.NewAggregate([]error{retryErr, err})
	}

	log.Info(fmt.Sprintf("Upgrade Cluster from version %s to %s completed", currentVersion, version))
	return nil
}
