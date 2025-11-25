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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	resourcepatchv1alpha1 "github.com/k8soneill/resource-patch-operator/api/v1alpha1"
)

// PatchTrackerReconciler reconciles a PatchTracker object
type PatchTrackerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=resourcepatch.io.github.k8soneill,resources=patchtrackers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=resourcepatch.io.github.k8soneill,resources=patchtrackers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=resourcepatch.io.github.k8soneill,resources=patchtrackers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;services;pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets;replicasets,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch;patch
func (r *PatchTrackerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Setup logger object
	logger := logf.FromContext(ctx)

	// Fetch patchTracker instance
	patchTracker := &resourcepatchv1alpha1.PatchTracker{}
	if err := r.Get(ctx, req.NamespacedName, patchTracker); err != nil {
		logger.Error(err, "unable to fetch PatchTracker")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Print PatchTracker name for testing
	logger.Info("RECONCILING PatchTracker",
		"name", patchTracker.Name,
		"namespace", patchTracker.Namespace,
		"targets", len(patchTracker.Spec.Targets))
	// Finalizer name
	finalizerName := "patchtracker.resourcepatch.io/finalizer"
	// Examine DeletionTimestamp to determine if object is under deletion
	if patchTracker.GetDeletionTimestamp() == nil {
		// Object is not being deleted. Check for finalizer and add if not present.
		if !controllerutil.ContainsFinalizer(patchTracker, finalizerName) {
			// Copy object to perform a merge from patch
			originalObject := patchTracker.DeepCopy()
			// Updates object in memory
			controllerutil.AddFinalizer(patchTracker, finalizerName)
			// Updates object in the cluster
			if err := r.Patch(ctx, patchTracker, client.MergeFrom(originalObject)); err != nil {
				return ctrl.Result{}, err
			}
		}

	} else {
		// Object being deleted
		if controllerutil.ContainsFinalizer(patchTracker, finalizerName) {
			// Pre delete logic here
			// Copy object to perform a merge from patch
			originalObject := patchTracker.DeepCopy()
			controllerutil.RemoveFinalizer(patchTracker, finalizerName)
			if err := r.Patch(ctx, patchTracker, client.MergeFrom(originalObject)); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Get list of targets that need patching based on secret changes
	targetsToPatch, err := r.getTargetsToPatch(ctx, patchTracker)
	if err != nil {
		logger.Error(err, "Failed to determine targets to patch")
		return ctrl.Result{}, err
	}

	if len(targetsToPatch) > 0 {
		logger.Info("Applying patches due to secret changes", "targetCount", len(targetsToPatch))

		// Apply patch to each target
		for _, target := range targetsToPatch {
			if err := r.applyPatchToTarget(ctx, patchTracker, target); err != nil {
				logger.Error(err, "Failed to apply patch to target",
					"target", fmt.Sprintf("%s/%s", target.Namespace, target.Name),
					"kind", target.Kind)
				// Continue with other patches, but record the error
				// TODO: Consider how to handle partial failures
				continue
			}
			logger.Info("Successfully applied patch to target",
				"target", fmt.Sprintf("%s/%s", target.Namespace, target.Name),
				"kind", target.Kind,
				"patchPath", target.PatchField.Path)
		}

		// Update tracking status after successful patch
		if err := r.updateTrackingStatus(ctx, patchTracker); err != nil {
			logger.Error(err, "Failed to update tracking status")
			return ctrl.Result{}, err
		}
	} else {
		logger.Info("No patches needed, all secrets up to date")
	}

	return ctrl.Result{}, nil
}

func (r *PatchTrackerReconciler) updateTrackingStatus(ctx context.Context, patchTracker *resourcepatchv1alpha1.PatchTracker) error {
	logger := logf.FromContext(ctx)

	// Get the latest version of the object to avoid conflicts
	latest := &resourcepatchv1alpha1.PatchTracker{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(patchTracker), latest); err != nil {
		return err
	}

	statusPatch := latest.DeepCopy()

	// Initialize status fields if needed
	if statusPatch.Status.SecretVersions == nil {
		statusPatch.Status.SecretVersions = make(map[string]string)
	}

	// Update secret versions based on current state
	updated := false
	for _, target := range patchTracker.Spec.Targets {
		for _, secretDep := range target.SecretDeps {
			if secretDep.Watch {
				secretName := secretDep.Name
				secretNamespace := secretDep.Namespace
				if secretNamespace == "" {
					secretNamespace = target.Namespace
				}

				// Fetch current secret version
				secretNamespacedName := types.NamespacedName{
					Name:      secretName,
					Namespace: secretNamespace,
				}
				secret := &corev1.Secret{}
				if err := r.Get(ctx, secretNamespacedName, secret); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Secret not found, skip version tracking
					continue
				}

				secretKey := secretNamespace + "/" + secretName
				currentVersion := secret.ResourceVersion
				if statusPatch.Status.SecretVersions[secretKey] != currentVersion {
					statusPatch.Status.SecretVersions[secretKey] = currentVersion
					updated = true
				}
			}
		}
	}

	// Only update if something changed
	if updated {
		statusPatch.Status.LastPatchTime = &metav1.Time{Time: time.Now()}

		if err := r.Status().Update(ctx, statusPatch); err != nil {
			if apierrors.IsConflict(err) {
				logger.Info("Status update conflict, will retry on next reconcile", "error", err.Error())
				return nil // Don't treat conflicts as fatal errors
			}
			logger.Error(err, "Failed to update PatchTracker status")
			return err
		}
		logger.Info("Updated PatchTracker status with new secret versions")
	}

	return nil
}

// getTargetsToPatch determines which targets need patching based on secret changes
// Returns a list of TargetRefs for targets whose secrets have changed
func (r *PatchTrackerReconciler) getTargetsToPatch(ctx context.Context, patchTracker *resourcepatchv1alpha1.PatchTracker) ([]resourcepatchv1alpha1.TargetRef, error) {
	logger := logf.FromContext(ctx)
	var targetsToPatch []resourcepatchv1alpha1.TargetRef

	// Iterate through targets list
	for _, target := range patchTracker.Spec.Targets {
		needsPatch := false

		// Iterate through secret dependencies of target
		for _, secretDep := range target.SecretDeps {
			// Set secret name and namespace
			secretName := secretDep.Name
			secretNamespace := secretDep.Namespace
			// Default to target namespace if not explicitly set on secret
			if secretNamespace == "" {
				secretNamespace = target.Namespace
			}

			// Check if secret should be watched for changes
			if secretDep.Watch {
				// Create namespaced name secret
				secretNamespacedName := types.NamespacedName{
					Name:      secretName,
					Namespace: secretNamespace,
				}
				// Fetch the secret
				secret := &corev1.Secret{}
				if err := r.Get(ctx, secretNamespacedName, secret); err != nil {
					// If not found and optional move on to next secret in the loop
					if apierrors.IsNotFound(err) {
						if secretDep.Optional {
							logger.Info("Secret is missing but optional. Not erroring.", "secret", secretName, "namespace", secretNamespace)
							continue
						} else {
							logger.Error(err, "Required secret not found", "secret", secretName, "namespace", secretNamespace)
							return nil, err
						}
					}
					// Other errors should be returned
					logger.Error(err, "Failed to fetch secret", "secret", secretName, "namespace", secretNamespace)
					return nil, err
				}

				// Check if this secret version has changed
				secretKey := secretNamespace + "/" + secretName
				currentVersion := secret.ResourceVersion
				lastKnownVersion, exists := patchTracker.Status.SecretVersions[secretKey]

				if !exists || lastKnownVersion != currentVersion {
					logger.Info("Secret version changed, target needs patching",
						"secret", secretKey,
						"currentVersion", currentVersion,
						"lastKnownVersion", lastKnownVersion)
					needsPatch = true
					break // No need to check other secrets for this target
				}
			} else {
				logger.V(1).Info("Skipping unwatched secret", "secret", secretName, "namespace", secretNamespace)
			}
		}

		// If any secrets changed for this target, add it to the list
		if needsPatch {
			targetsToPatch = append(targetsToPatch, target)
		}
	}

	if len(targetsToPatch) == 0 {
		logger.Info("No secret changes detected, skipping patch")
	}

	return targetsToPatch, nil
}

// applyPatchToTarget applies the patch to the target resource using a dynamic client
func (r *PatchTrackerReconciler) applyPatchToTarget(ctx context.Context, patchTracker *resourcepatchv1alpha1.PatchTracker, target resourcepatchv1alpha1.TargetRef) error {
	logger := logf.FromContext(ctx)

	// Parse the apiVersion to get group and version
	gv, err := schema.ParseGroupVersion(target.APIVersion)
	if err != nil {
		return fmt.Errorf("invalid apiVersion %q: %w", target.APIVersion, err)
	}

	// Create the GVK for the target resource
	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    target.Kind,
	}

	logger.Info("Applying patch to target",
		"gvk", gvk.String(),
		"name", target.Name,
		"namespace", target.Namespace,
		"patchPath", target.PatchField.Path)

	// Use unstructured to handle any resource type
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	// Get the target resource
	if err := r.Get(ctx, types.NamespacedName{
		Name:      target.Name,
		Namespace: target.Namespace,
	}, obj); err != nil {
		if apierrors.IsNotFound(err) && patchTracker.Spec.IgnoreMissingTarget {
			logger.Info("Target resource not found, ignoring due to IgnoreMissingTarget=true",
				"name", target.Name,
				"namespace", target.Namespace)
			return nil
		}
		return fmt.Errorf("failed to get target resource: %w", err)
	}

	// Build the patch value (timestamp annotation to trigger rollout)
	patchValue := r.buildPatchValue()

	// Apply the patch based on the target's strategy
	switch target.PatchStrategy {
	case "none":
		logger.Info("PatchStrategy is 'none', skipping actual patch application")
		return nil
	case "jsonPatch":
		return r.applyJSONPatch(ctx, obj, target.PatchField.Path, patchValue)
	case "strategicMerge", "":
		return r.applyStrategicMergePatch(ctx, obj, target.PatchField.Path, patchValue)
	case "serverSideApply":
		return r.applyServerSideApply(ctx, obj, target.PatchField.Path, patchValue, patchTracker.Name)
	default:
		return fmt.Errorf("unknown patch strategy: %s", target.PatchStrategy)
	}
}

// buildPatchValue constructs the annotation value to trigger rollouts
func (r *PatchTrackerReconciler) buildPatchValue() map[string]interface{} {
	return map[string]interface{}{
		"resourcepatch.io/last-updated": time.Now().Format(time.RFC3339),
	}
}

// setNestedField sets a value at a nested path in an unstructured object
// path is in format "spec.template.spec.containers[0].env"
func setNestedField(obj map[string]interface{}, value interface{}, path string) error {
	parts := strings.Split(path, ".")
	current := obj

	for i, part := range parts {
		if i == len(parts)-1 {
			// Last part - set the value
			current[part] = value
			return nil
		}

		// Navigate to the next level
		if next, ok := current[part]; ok {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return fmt.Errorf("path element %q is not an object", part)
			}
		} else {
			// Create the path if it doesn't exist
			newMap := make(map[string]interface{})
			current[part] = newMap
			current = newMap
		}
	}

	return nil
}

// applyJSONPatch applies a JSON Patch (RFC 6902) to the target
func (r *PatchTrackerReconciler) applyJSONPatch(ctx context.Context, obj *unstructured.Unstructured, path string, value interface{}) error {
	logger := logf.FromContext(ctx)

	// Convert path from dot notation to JSON Pointer (RFC 6901)
	jsonPointerPath := "/" + strings.ReplaceAll(path, ".", "/")

	// Build JSON Patch operation
	patchOps := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  jsonPointerPath,
			"value": value,
		},
	}

	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON patch: %w", err)
	}

	logger.Info("Applying JSON patch", "patch", string(patchBytes))

	// Apply the patch
	if err := r.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, patchBytes)); err != nil {
		return fmt.Errorf("failed to apply JSON patch: %w", err)
	}

	return nil
}

// applyStrategicMergePatch applies a strategic merge patch to the target
func (r *PatchTrackerReconciler) applyStrategicMergePatch(ctx context.Context, obj *unstructured.Unstructured, path string, value interface{}) error {
	logger := logf.FromContext(ctx)

	// Build the patch document with the value at the specified path
	patchDoc := make(map[string]interface{})
	if err := setNestedField(patchDoc, value, path); err != nil {
		return fmt.Errorf("failed to build patch document: %w", err)
	}

	patchBytes, err := json.Marshal(patchDoc)
	if err != nil {
		return fmt.Errorf("failed to marshal patch document: %w", err)
	}

	logger.Info("Applying strategic merge patch", "patch", string(patchBytes))

	// Apply the patch
	if err := r.Patch(ctx, obj, client.RawPatch(types.StrategicMergePatchType, patchBytes)); err != nil {
		// Fall back to merge patch for custom resources that don't support strategic merge
		logger.Info("Strategic merge patch failed, falling back to merge patch", "error", err.Error())
		if err := r.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
			return fmt.Errorf("failed to apply merge patch: %w", err)
		}
	}

	return nil
}

// applyServerSideApply applies changes using server-side apply
func (r *PatchTrackerReconciler) applyServerSideApply(ctx context.Context, obj *unstructured.Unstructured, path string, value interface{}, fieldManager string) error {
	logger := logf.FromContext(ctx)

	// Build the patch document with the value at the specified path
	patchDoc := map[string]interface{}{
		"apiVersion": obj.GetAPIVersion(),
		"kind":       obj.GetKind(),
		"metadata": map[string]interface{}{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
		},
	}
	if err := setNestedField(patchDoc, value, path); err != nil {
		return fmt.Errorf("failed to build SSA patch document: %w", err)
	}

	patchBytes, err := json.Marshal(patchDoc)
	if err != nil {
		return fmt.Errorf("failed to marshal SSA patch document: %w", err)
	}

	logger.Info("Applying server-side apply", "patch", string(patchBytes), "fieldManager", fieldManager)

	// Apply using server-side apply
	patchObj := &unstructured.Unstructured{}
	patchObj.SetGroupVersionKind(obj.GroupVersionKind())
	if err := json.Unmarshal(patchBytes, &patchObj.Object); err != nil {
		return fmt.Errorf("failed to unmarshal patch object: %w", err)
	}

	if err := r.Patch(ctx, patchObj, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("failed to apply server-side apply: %w", err)
	}

	return nil
}

// findPatchTrackersForSecret maps Secret changes to PatchTracker reconcile requests
func (r *PatchTrackerReconciler) findPatchTrackersForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)
	secretKey := secret.Namespace + "/" + secret.Name

	// Use the custom index to find PatchTrackers that reference this Secret
	var patchTrackers resourcepatchv1alpha1.PatchTrackerList
	if err := r.List(ctx, &patchTrackers, client.MatchingFields{"spec.secretRefs": secretKey}); err != nil {
		// Log error but don't prevent other processing
		logf.FromContext(ctx).Error(err, "Failed to find PatchTrackers for Secret", "secret", secretKey)
		return nil
	}

	// Convert PatchTrackers to reconcile requests
	requests := []reconcile.Request{}
	for _, pt := range patchTrackers.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      pt.Name,
				Namespace: pt.Namespace,
			},
		})
	}

	// Log how many PatchTrackers will be reconciled
	if len(requests) > 0 {
		logf.FromContext(ctx).Info("Secret change will trigger PatchTracker reconciles",
			"secret", secretKey,
			"patchTrackerCount", len(requests))
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *PatchTrackerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Runs an index function for every patchTracker object and stores a namespace/name value for all related secrets
	// in memory. This allows us to trigger patchTracker reconciles when indexed secrets change.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &resourcepatchv1alpha1.PatchTracker{}, "spec.secretRefs", func(obj client.Object) []string {
		patchtrack := obj.(*resourcepatchv1alpha1.PatchTracker)
		var keys []string
		for _, target := range patchtrack.Spec.Targets {
			for _, secret := range target.SecretDeps {
				ns := secret.Namespace
				if ns == "" {
					ns = patchtrack.Namespace
				}
				if secret.Name == "" {
					// skip unnamed secret refs. Can't be indexed but CRD validation should prevent this
					continue
				}
				keys = append(keys, ns+"/"+secret.Name)
			}
		}
		return keys
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&resourcepatchv1alpha1.PatchTracker{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findPatchTrackersForSecret),
		).
		Named("patchtracker").
		Complete(r)
}
