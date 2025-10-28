/*
Copyright 2025 The Kubernetes Authors.

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

package desiredstate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
	"sigs.k8s.io/cluster-api/exp/topology/scope"
	"sigs.k8s.io/cluster-api/internal/hooks"
)

func (g *generator) callBeforeClusterUpgradeHook(ctx context.Context, s *scope.Scope, currentVersion *string, topologyVersion string) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	// NOTE: the hook should be called only at the beginning of either a regular upgrade or a multistep upgrade sequence (it should not be called when in the middle of a multistep upgrade sequence);
	// to detect if we are at the beginning of an upgrade, we check if the intent to call the AfterClusterUpgrade is not yet tracked.
	if !hooks.IsPending(runtimehooksv1.AfterClusterUpgrade, s.Current.Cluster) {
		var hookAnnotations []string
		for key := range s.Current.Cluster.Annotations {
			if strings.HasPrefix(key, clusterv1.BeforeClusterUpgradeHookAnnotationPrefix) {
				hookAnnotations = append(hookAnnotations, key)
			}
		}
		if len(hookAnnotations) > 0 {
			slices.Sort(hookAnnotations)
			message := fmt.Sprintf("annotations [%s] are set", strings.Join(hookAnnotations, ", "))
			if len(hookAnnotations) == 1 {
				message = fmt.Sprintf("annotation [%s] is set", strings.Join(hookAnnotations, ", "))
			}
			// Add the hook with a response to the tracker so we can later update the condition.
			s.HookResponseTracker.Add(runtimehooksv1.BeforeClusterUpgrade, &runtimehooksv1.BeforeClusterUpgradeResponse{
				CommonRetryResponse: runtimehooksv1.CommonRetryResponse{
					// RetryAfterSeconds needs to be set because having only hooks without RetryAfterSeconds
					// would lead to not updating the condition. We can rely on getting an event when the
					// annotation gets removed so we set twice of the default sync-period to not cause additional reconciles.
					RetryAfterSeconds: 20 * 60,
					CommonResponse: runtimehooksv1.CommonResponse{
						Message: message,
					},
				},
			})

			log.Info(fmt.Sprintf("Cluster upgrade to version %q is blocked by %q hook (via annotations)", topologyVersion, runtimecatalog.HookName(runtimehooksv1.BeforeClusterUpgrade)), "hooks", strings.Join(hookAnnotations, ","))
			return false, nil
		}

		v1beta1Cluster := &clusterv1beta1.Cluster{}
		// DeepCopy cluster because ConvertFrom has side effects like adding the conversion annotation.
		if err := v1beta1Cluster.ConvertFrom(s.Current.Cluster.DeepCopy()); err != nil {
			return false, errors.Wrap(err, "error converting Cluster to v1beta1 Cluster")
		}

		hookRequest := &runtimehooksv1.BeforeClusterUpgradeRequest{
			Cluster:               *cleanupCluster(v1beta1Cluster),
			FromKubernetesVersion: *currentVersion,
			ToKubernetesVersion:   topologyVersion,
			ControlPlaneUpgrades:  toUpgradeStep(s.UpgradeTracker.ControlPlane.UpgradePlan),
			WorkersUpgrades:       toUpgradeStep(s.UpgradeTracker.MachineDeployments.UpgradePlan, s.UpgradeTracker.MachinePools.UpgradePlan),
		}
		hookResponse := &runtimehooksv1.BeforeClusterUpgradeResponse{}
		if err := g.RuntimeClient.CallAllExtensions(ctx, runtimehooksv1.BeforeClusterUpgrade, s.Current.Cluster, hookRequest, hookResponse); err != nil {
			return false, err
		}
		// Add the response to the tracker so we can later update condition or requeue when required.
		s.HookResponseTracker.Add(runtimehooksv1.BeforeClusterUpgrade, hookResponse)
		if hookResponse.RetryAfterSeconds != 0 {
			// Cannot pickup the new version right now. Need to try again later.
			log.Info(fmt.Sprintf("Cluster upgrade to version %q is blocked by %q hook", topologyVersion, runtimecatalog.HookName(runtimehooksv1.BeforeClusterUpgrade)))
			return false, nil
		}
	}
	return true, nil
}

func (g *generator) callAfterControlPlaneUpgradeHook(ctx context.Context, s *scope.Scope, currentVersion *string, topologyVersion string) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	// Call the hook only if we are tracking the intent to do so. If it is not tracked it means we don't need to call the
	// hook because we didn't go through an upgrade or we already called the hook after the upgrade.
	if hooks.IsPending(runtimehooksv1.AfterControlPlaneUpgrade, s.Current.Cluster) {
		v1beta1Cluster := &clusterv1beta1.Cluster{}
		// DeepCopy cluster because ConvertFrom has side effects like adding the conversion annotation.
		if err := v1beta1Cluster.ConvertFrom(s.Current.Cluster.DeepCopy()); err != nil {
			return false, errors.Wrap(err, "error converting Cluster to v1beta1 Cluster")
		}

		// Call all the registered extension for the hook.
		hookRequest := &runtimehooksv1.AfterControlPlaneUpgradeRequest{
			Cluster:              *cleanupCluster(v1beta1Cluster),
			KubernetesVersion:    *currentVersion,
			ControlPlaneUpgrades: toUpgradeStep(s.UpgradeTracker.ControlPlane.UpgradePlan),
			WorkersUpgrades:      toUpgradeStep(s.UpgradeTracker.MachineDeployments.UpgradePlan, s.UpgradeTracker.MachinePools.UpgradePlan),
		}
		hookResponse := &runtimehooksv1.AfterControlPlaneUpgradeResponse{}
		if err := g.RuntimeClient.CallAllExtensions(ctx, runtimehooksv1.AfterControlPlaneUpgrade, s.Current.Cluster, hookRequest, hookResponse); err != nil {
			return false, err
		}
		// Add the response to the tracker so we can later update condition or requeue when required.
		s.HookResponseTracker.Add(runtimehooksv1.AfterControlPlaneUpgrade, hookResponse)

		// If the extension responds to hold off on starting Machine deployments upgrades,
		// change the UpgradeTracker accordingly, otherwise the hook call is completed and we
		// can remove this hook from the list of pending-hooks.
		if hookResponse.RetryAfterSeconds != 0 {
			v := topologyVersion
			if len(s.UpgradeTracker.ControlPlane.UpgradePlan) > 0 {
				v = s.UpgradeTracker.ControlPlane.UpgradePlan[0]
			}
			log.Info(fmt.Sprintf("Upgrade to version %q is blocked by %q hook", v, runtimecatalog.HookName(runtimehooksv1.AfterControlPlaneUpgrade)))
			return false, nil
		}
		if err := hooks.MarkAsDone(ctx, g.Client, s.Current.Cluster, runtimehooksv1.AfterControlPlaneUpgrade); err != nil {
			return false, err
		}
	}
	return true, nil
}

// toUpgradeStep converts a list of version to a list of upgrade steps.
// Note. when called for workers, the function will receive in input two plans one for the MachineDeployments if any, the other for MachinePools if any.
// Considering that both plans, if defined, have to be equal, the function picks the first one not empty.
func toUpgradeStep(plans ...[]string) []runtimehooksv1.UpgradeStepInfo {
	var steps []runtimehooksv1.UpgradeStepInfo
	for _, plan := range plans {
		if len(plan) != 0 {
			for _, step := range plan {
				steps = append(steps, runtimehooksv1.UpgradeStepInfo{Version: step})
			}
			break
		}
	}
	return steps
}
