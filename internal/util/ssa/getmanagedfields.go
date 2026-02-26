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

package ssa

import (
	"encoding/json"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

func GetUnstructuredManagedFields(u *unstructured.Unstructured, fieldManager string) ([]metav1.ManagedFieldsEntry, error) {
	// FIXME: looks like this function is mostly more efficient if we only want one specific managedField entry instead of all of them

	field, ok, err := unstructured.NestedFieldNoCopy(u.Object, "metadata", "managedFields")
	if err != nil || !ok { // FIXME: fix the ok handling (same below
		return nil, err
	}
	fieldArray, ok := field.([]any)
	if !ok {
		return nil, errors.New("error")
	}
	managedFields := make([]metav1.ManagedFieldsEntry, 0, len(fieldArray))
	for _, fieldInterface := range fieldArray {
		field := fieldInterface.(map[string]any)

		manager := field["manager"].(string)
		if fieldManager != "" && manager != fieldManager {
			continue
		}

		var subresource string
		if subresourceAny, ok := field["subresource"]; ok {
			subresource = subresourceAny.(string)
		}

		fieldsV1Raw, err := json.Marshal(field["fieldsV1"])
		if err != nil {
			return nil, err
		}

		managedFields = append(managedFields, metav1.ManagedFieldsEntry{
			Manager:     manager,
			Operation:   metav1.ManagedFieldsOperationType(field["operation"].(string)),
			APIVersion:  field["apiVersion"].(string),
			FieldsType:  field["fieldsType"].(string),
			Subresource: subresource,
			Time:        ptr.To(metav1.Now()),
			FieldsV1: &metav1.FieldsV1{
				Raw: fieldsV1Raw,
			},
		})
	}
	return managedFields, nil
}
