/*
Copyright 2023.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PodStatusInfo defines basic pod status information
type PodStatusInfo struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	RestartCount int32  `json:"restartCount"`
}

// CustomReplicaSetSpec defines the desired state of CustomReplicaSet
type CustomReplicaSetSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Keeps track of the number of replicas
	Replicas int32 `json:"replicas"`

	// Stores Pod Spec used for creating new pods
	// Template v1.PodTemplateSpec `json:"template"`
}

// CustomReplicaSetStatus defines the observed state of CustomReplicaSet
type CustomReplicaSetStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Current number of observed pods
	CurrentReplicas int32 `json:"currentReplicas"`

	// Stores mapping of cluster pod names to podInfo
	PodsMap map[string]PodStatusInfo `json:"pods,omitempty"`

	// List of pod statuses
	// PodStatus []PodStatusInfo `json:"podStatus,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// CustomReplicaSet is the Schema for the customreplicasets API
type CustomReplicaSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CustomReplicaSetSpec   `json:"spec,omitempty"`
	Status CustomReplicaSetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// CustomReplicaSetList contains a list of CustomReplicaSet
type CustomReplicaSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CustomReplicaSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CustomReplicaSet{}, &CustomReplicaSetList{})
}
