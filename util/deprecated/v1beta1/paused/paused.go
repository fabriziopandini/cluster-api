/*
Copyright 2024 The Kubernetes Authors.

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

// Package paused implements paused helper functions.
package paused

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/annotations"
	v1beta2conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions/v1beta2"
	"sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
)

// ConditionSetter combines the client.Object and Setter interface.
type ConditionSetter interface {
	v1beta2conditions.Setter
	client.Object
}

// EnsurePausedCondition sets the paused condition on the object and returns if it should be considered as paused.
func EnsurePausedCondition(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, v1beta1Obj ConditionSetter) (isPaused bool, requeue bool, err error) {
	oldCondition := v1beta2conditions.Get(v1beta1Obj, clusterv1beta1.PausedV1Beta2Condition)
	newCondition := pausedCondition(c.Scheme(), cluster, v1beta1Obj, clusterv1beta1.PausedV1Beta2Condition)

	isPaused = newCondition.Status == metav1.ConditionTrue
	pausedStatusChanged := oldCondition == nil || oldCondition.Status != newCondition.Status

	log := ctrl.LoggerFrom(ctx)

	switch {
	case pausedStatusChanged && isPaused:
		log.V(4).Info("Pausing reconciliation for this object", "reason", newCondition.Message)
	case pausedStatusChanged && !isPaused:
		log.V(4).Info("Unpausing reconciliation for this object")
	case !pausedStatusChanged && isPaused:
		log.V(6).Info("Reconciliation is paused for this object", "reason", newCondition.Message)
	}

	if oldCondition != nil {
		// Return early if the paused condition did not change at all.
		if v1beta2conditions.HasSameState(oldCondition, &newCondition) {
			return isPaused, false, nil
		}

		// Set condition and return early if only observed generation changed and obj is not paused.
		// In this case we want to avoid the additional reconcile that we would get by requeueing.
		if v1beta2conditions.HasSameStateExceptObservedGeneration(oldCondition, &newCondition) && !isPaused {
			v1beta2conditions.Set(v1beta1Obj, newCondition)
			return isPaused, false, nil
		}
	}

	patchHelper, err := patch.NewHelper(v1beta1Obj, c)
	if err != nil {
		return isPaused, false, err
	}

	v1beta2conditions.Set(v1beta1Obj, newCondition)

	if err := patchHelper.Patch(ctx, v1beta1Obj, patch.WithOwnedV1Beta2Conditions{Conditions: []string{
		clusterv1beta1.PausedV1Beta2Condition,
	}}); err != nil {
		return isPaused, false, err
	}

	return isPaused, true, nil
}

// pausedCondition sets the paused condition on the object and returns if it should be considered as paused.
func pausedCondition(scheme *runtime.Scheme, cluster *clusterv1.Cluster, obj ConditionSetter, targetConditionType string) metav1.Condition {
	if (cluster != nil && ptr.Deref(cluster.Spec.Paused, false)) || annotations.HasPaused(obj) {
		var messages []string
		if cluster != nil && ptr.Deref(cluster.Spec.Paused, false) {
			messages = append(messages, "Cluster spec.paused is set to true")
		}
		if annotations.HasPaused(obj) {
			kind := "Object"
			if gvk, err := apiutil.GVKForObject(obj, scheme); err == nil {
				kind = gvk.Kind
			}
			messages = append(messages, fmt.Sprintf("%s has the cluster.x-k8s.io/paused annotation", kind))
		}

		return metav1.Condition{
			Type:               targetConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             clusterv1beta1.PausedV1Beta2Reason,
			Message:            strings.Join(messages, ", "),
			ObservedGeneration: obj.GetGeneration(),
		}
	}

	return metav1.Condition{
		Type:               targetConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             clusterv1beta1.NotPausedV1Beta2Reason,
		ObservedGeneration: obj.GetGeneration(),
	}
}
