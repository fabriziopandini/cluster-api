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
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	tkr2 "sigs.k8s.io/cluster-api/util/tkr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const dockerMachineTemplateImmutableMsg = "DockerMachineTemplate spec.template.spec field is immutable. Please create a new resource instead."

func (m *DockerMachineTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(m).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-dockermachinetemplate,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=infrastructure.cluster.x-k8s.io,resources=dockermachinetemplates,versions=v1beta1,name=default.dockermachinetemplate.infrastructure.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1
// +kubebuilder:webhook:verbs=create;update,path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-dockermachinetemplate,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,groups=infrastructure.cluster.x-k8s.io,resources=dockermachinetemplates,versions=v1beta1,name=validation.dockermachinetemplate.infrastructure.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1

var _ webhook.Defaulter = &DockerMachineTemplate{}
var _ webhook.Validator = &DockerMachineTemplate{}

func (m *DockerMachineTemplate) Default() {
	// ****
	template := m

	// if the object is part of a managed cluster
	if _, ok := template.Labels[clusterv1.ClusterTopologyOwnedLabel]; ok {

		// TODO: get corresponding cluster, check if tkr resolution is required for this Cluster/KubeadmControlPlane

		// NOTE: we are resolving TKR for owner-version, given that it might be different from the
		// cluster.topology.version during an upgrade sequence.
		version, ok := template.Annotations[clusterv1.ClusterTopologyKubernetesVersionAnnotation]
		if !ok {
			return
		}
		tkr := tkr2.ResolveFor(version)

		// Set machine image
		template.Spec.Template.Spec.CustomImage = tkr.MachineImage
	}
	// ****
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (m *DockerMachineTemplate) ValidateCreate() error {
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (m *DockerMachineTemplate) ValidateUpdate(oldRaw runtime.Object) error {
	var allErrs field.ErrorList
	old, ok := oldRaw.(*DockerMachineTemplate)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a DockerMachineTemplate but got a %T", oldRaw))
	}
	if !reflect.DeepEqual(m.Spec.Template.Spec, old.Spec.Template.Spec) {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "template", "spec"), m, dockerMachineTemplateImmutableMsg))
	}
	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(GroupVersion.WithKind("DockerMachineTemplate").GroupKind(), m.Name, allErrs)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (m *DockerMachineTemplate) ValidateDelete() error {
	return nil
}
