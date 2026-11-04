/*
Copyright 2026.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type ForgejoRunnerSpec struct {
	// Image is the runner container image.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	ConfigSecretRef corev1.LocalObjectReference `json:"configSecretRef"`
}

type ForgejoRunnerStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	ReadyReplicas      int32 `json:"readyReplicas,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=".status.readyReplicas"
type ForgejoRunner struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ForgejoRunner
	// +required
	Spec ForgejoRunnerSpec `json:"spec"`

	// status defines the observed state of ForgejoRunner
	// +optional
	Status ForgejoRunnerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true
type ForgejoRunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ForgejoRunner `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ForgejoRunner{}, &ForgejoRunnerList{})
		return nil
	})
}
