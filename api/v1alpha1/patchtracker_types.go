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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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
	// When true, missing targets are skipped and will be patched when they appear.
	// When false, missing targets cause an error to be recorded in status.
	// +kubebuilder:default=true
	IgnoreMissingTarget bool `json:"ignoreMissingTarget"`
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

	// Method determines how the patch value is generated.
	// Allowed values: "timestamp", "specific", "randomString", "increasingInteger".
	// +kubebuilder:validation:Enum=timestamp;specific;randomString;increasingInteger
	// +kubebuilder:default="timestamp"
	Method string `json:"method,omitempty"`

	// SpecificValue is used when Method is "specific". Can be any JSON value.
	// +optional
	SpecificValue *apiextensionsv1.JSON `json:"specificValue,omitempty"`

	// RandomStringLength specifies the length for randomString method.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=256
	// +kubebuilder:default=32
	RandomStringLength int `json:"randomStringLength,omitempty"`
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
	// MaintenanceWindow restricts when patches can be applied.
	// When set, detected secret changes are deferred until the window is active.
	// +optional
	MaintenanceWindow *MaintenanceWindow `json:"maintenanceWindow,omitempty"`
}

// MaintenanceWindow defines a time window during which patches are allowed.
// Outside this window, detected changes are deferred until the window opens.
type MaintenanceWindow struct {
	// Start is the earliest time at which patches may be applied.
	// +kubebuilder:validation:Required
	Start metav1.Time `json:"start"`
	// Duration is how long the window remains open after Start.
	// If omitted, the window has no end (equivalent to a simple notBefore).
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`
}

// TargetStatus tracks the state of a single target resource.
type TargetStatus struct {
	// Reference to the target resource
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`

	// SecretVersions tracks the last successfully processed version of each secret for this target.
	// Key: "namespace/secretname", Value: resourceVersion
	SecretVersions map[string]string `json:"secretVersions,omitempty"`

	// LastPatchTime is when this target was last successfully patched.
	LastPatchTime *metav1.Time `json:"lastPatchTime,omitempty"`

	// LastError contains the error message from the most recent patch attempt.
	// Empty string indicates success.
	LastError string `json:"lastError,omitempty"`

	// LastErrorTime is when the last error occurred.
	LastErrorTime *metav1.Time `json:"lastErrorTime,omitempty"`
}

// PatchTrackerStatus defines the observed state of PatchTracker.
type PatchTrackerStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastReconcileTime is the time the controller last completed a reconcile.
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// Targets tracks the status of each target independently.
	// This provides per-target secret version tracking and error reporting.
	Targets []TargetStatus `json:"targets,omitempty"`

	// PendingPatchCount is the number of targets with detected changes
	// that are deferred due to a maintenance window.
	// +optional
	PendingPatchCount int `json:"pendingPatchCount,omitempty"`
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
