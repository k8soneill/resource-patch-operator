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
	Targets []TargetRef `json:"targets,omitempty"`

	// Fields defines which fields on the target resources should be observed.
	// JSONPath or JSONPointer expressions are allowed.
	Fields []FieldSelector `json:"fields,omitempty"`

	// SecretDeps lists Secrets which, when changed, should trigger reconciles.
	SecretDeps []SecretRef `json:"secretDeps,omitempty"`

	// Reconcile controls when reconciles are triggered and timing options.
	Reconcile ReconcileOptions `json:"reconcile,omitempty"`

	// PatchStrategy determines how the controller will apply changes when acting.
	// Allowed values: "none", "jsonPatch", "strategicMerge", "serverSideApply".
	PatchStrategy string `json:"patchStrategy,omitempty"`

	// ServiceAccountName is an optional hint for RBAC (which SA will be used to read targets).
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// IgnoreMissingTarget controls whether a missing target resource is treated as an error.
	IgnoreMissingTarget bool `json:"ignoreMissingTarget,omitempty"`
}

// TargetRef identifies one or more Kubernetes objects to observe.
type TargetRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	// If Name is empty and LabelSelector is set, the selector is used to match multiple objects.
	Name          string                `json:"name,omitempty"`
	Namespace     string                `json:"namespace,omitempty"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
	// AllowCrossNamespace permits observing objects in other namespaces when true.
	AllowCrossNamespace bool `json:"allowCrossNamespace,omitempty"`
}

// FieldSelector describes a single field to observe on the target resource.
type FieldSelector struct {
	// Path is a JSONPath or JSONPointer expression for the field to observe (e.g. "$.spec.replicas").
	Path string `json:"path"`
	// MatchType controls how changes are evaluated: "exists", "equals", "regex", "changed".
	MatchType string `json:"matchType,omitempty"`
	// Value is used with "equals" or "regex" match types.
	Value string `json:"value,omitempty"`
	// Watch indicates whether changes to this field should trigger reconciliation.
	Watch bool `json:"watch,omitempty"`
}

// SecretRef identifies Secret dependencies which can trigger reconciles.
type SecretRef struct {
	Name          string                `json:"name,omitempty"`
	Namespace     string                `json:"namespace,omitempty"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
	// Keys limits watching to specific keys in the Secret; empty means watch whole Secret.
	Keys []string `json:"keys,omitempty"`
	// Optional indicates the Secret may be absent without causing an error.
	Optional bool `json:"optional,omitempty"`
	// Watch controls whether changes to the Secret trigger reconciles (default true).
	Watch bool `json:"watch,omitempty"`
}

// ReconcileOptions configures trigger types and timing for reconciles.
type ReconcileOptions struct {
	// On is a list of triggers to reconcile on: e.g. ["fieldChange","secretChange","anyChange"].
	On []string `json:"on,omitempty"`
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

	// LastAppliedHash is a hash of the watched fields and secret data used to detect changes.
	LastAppliedHash string `json:"lastAppliedHash,omitempty"`

	// TrackedResources lists resources that this PatchTracker is observing.
	TrackedResources []TrackedResource `json:"trackedResources,omitempty"`
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
