/*
Copyright 2025.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PatchTrackerSpec defines the desired state of PatchTracker.
type PatchTrackerSpec struct {
	// Targets selects one or more resources (built-in or custom) to observe.
	// +kubebuilder:validation:Required
	Targets []TargetRef `json:"targets"`

	// Reconcile controls when reconciles are triggered and timing options.
	Reconcile ReconcileOptions `json:"reconcile,omitempty"`

	// ServiceAccountName is an optional hint for RBAC (which SA will be used to read targets).
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// IgnoreMissingTarget controls whether a missing target resource is treated as an error.
	// +default:value=true
	IgnoreMissingTarget bool `json:"ignoreMissingTarget,omitempty"`
}

// TargetRef identifies one or more Kubernetes objects to observe.
type TargetRef struct {
	// +kubebuilder:validation:Required
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`
	// If Name is empty and LabelSelector is set, the selector is used to match multiple objects.
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:Required
	Namespace     string                `json:"namespace"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
	// +kubebuilder:validation:Required
	PatchField PatchField `json:"patchField"`
	// PatchStrategy determines how the controller will apply changes to this target.
	// Allowed values: "none", "jsonPatch", "strategicMerge", "serverSideApply".
	// +kubebuilder:validation:Enum=none;jsonPatch;strategicMerge;serverSideApply
	// +default:value="strategicMerge"
	PatchStrategy string `json:"patchStrategy,omitempty"`
	// +kubebuilder:validation:Required
	SecretDeps []SecretRef `json:"secretDeps"`
}

// PatchField describes a single field to patch on the target resource
type PatchField struct {
	// Path is the string path to the field to patch (e.g. "spec.replicas").
	// +kubebuilder:validation:Required
	Path string `json:"path"`
}

// SecretRef identifies Secret dependencies which can trigger reconciles.
type SecretRef struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// If no namespace provided defaults to patch tracker namespace
	Namespace string `json:"namespace,omitempty"`
	// If false then reconciliation will error if secret does not exist
	// +default:value=false
	Optional bool `json:"optional,omitempty"`
	// Watch controls whether changes to the Secret trigger reconciles (default true).
	// +default:value=true
	Watch bool `json:"watch,omitempty"`
}

// ReconcileOptions configures trigger types and timing for reconciles.
type ReconcileOptions struct {
	// RequeueAfter will requeue reconcile after the given duration if set.
	RequeueAfter *metav1.Duration `json:"requeueAfter,omitempty"`
	// Debounce coalesces rapid events occurring within this duration.
	Debounce *metav1.Duration `json:"debounce,omitempty"`
}

// PatchTrackerStatus defines the observed state of PatchTracker.
type PatchTrackerStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastReconcileTime is the time the controller last completed a reconcile.
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// TrackedResources lists resources that this PatchTracker is observing.
	TrackedResources []TrackedResource `json:"trackedResources,omitempty"`

	// LastPatchTime tracks when patches were last applied to targets
	LastPatchTime *metav1.Time `json:"lastPatchTime,omitempty"`

	// SecretVersions tracks the resourceVersion of each watched secret
	// Key: "namespace/secretname", Value: resourceVersion
	SecretVersions map[string]string `json:"secretVersions,omitempty"`
}

// TrackedResource records an observed resource and its last seen state.
type TrackedResource struct {
	APIVersion      string       `json:"apiVersion,omitempty"`
	Kind            string       `json:"kind,omitempty"`
	Name            string       `json:"name,omitempty"`
	Namespace       string       `json:"namespace,omitempty"`
	ResourceVersion string       `json:"resourceVersion,omitempty"`
	LastSeen        *metav1.Time `json:"lastSeen,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PatchTracker is the Schema for the patchtrackers API.
type PatchTracker struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PatchTrackerSpec   `json:"spec,omitempty"`
	Status PatchTrackerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PatchTrackerList contains a list of PatchTracker.
type PatchTrackerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PatchTracker `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PatchTracker{}, &PatchTrackerList{})
}
