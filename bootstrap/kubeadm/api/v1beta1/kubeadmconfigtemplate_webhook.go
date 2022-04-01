/*
Copyright 2021 The Kubernetes Authors.

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

package v1beta1

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	tkr2 "sigs.k8s.io/cluster-api/util/tkr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func (r *KubeadmConfigTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-bootstrap-cluster-x-k8s-io-v1beta1-kubeadmconfigtemplate,mutating=true,failurePolicy=fail,groups=bootstrap.cluster.x-k8s.io,resources=kubeadmconfigtemplates,versions=v1beta1,name=default.kubeadmconfigtemplate.bootstrap.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1

var _ webhook.Defaulter = &KubeadmConfigTemplate{}

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (r *KubeadmConfigTemplate) Default() {

	// ********************
	template := r

	// if the object is part of a managed cluster
	if _, ok := template.Labels[clusterv1.ClusterTopologyOwnedLabel]; ok {

		// TODO: get corresponding cluster, check if tkr resolution is required for this Cluster/KubeadmControlPlane

		// NOTE: we are resolving TKR for owner-version, given that it might be different from the
		// cluster.topology.version during an upgrade sequence.
		version, ok := template.Annotations[clusterv1.ClusterTopologyKubernetesVersionAnnotation]
		if !ok {
			// handle error
		}
		tkr := tkr2.ResolveFor(version)

		// Add node labels
		template.Spec.Template.Spec.SetNodeLabels(&tkr)
	}

	// ********************

	DefaultKubeadmConfigSpec(&r.Spec.Template.Spec)
}

// +kubebuilder:webhook:verbs=create;update,path=/validate-bootstrap-cluster-x-k8s-io-v1beta1-kubeadmconfigtemplate,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,groups=bootstrap.cluster.x-k8s.io,resources=kubeadmconfigtemplates,versions=v1beta1,name=validation.kubeadmconfigtemplate.bootstrap.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1

var _ webhook.Validator = &KubeadmConfigTemplate{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (r *KubeadmConfigTemplate) ValidateCreate() error {
	return r.Spec.validate(r.Name)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (r *KubeadmConfigTemplate) ValidateUpdate(old runtime.Object) error {
	return r.Spec.validate(r.Name)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (r *KubeadmConfigTemplate) ValidateDelete() error {
	return nil
}

func (r *KubeadmConfigTemplateSpec) validate(name string) error {
	var allErrs field.ErrorList

	allErrs = append(allErrs, r.Template.Spec.Validate(field.NewPath("spec", "template", "spec"))...)

	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(GroupVersion.WithKind("KubeadmConfigTemplate").GroupKind(), name, allErrs)
}
