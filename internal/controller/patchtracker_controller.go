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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PatchTracker object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
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

	// Logic for checking watched object
	// Iterate through targets list
	for _, target := range patchTracker.Spec.Targets {
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
						}
					}
				}

			} else {
				logger.Info("Skipping unwatched secret", "secret", secretName, "namespace", secretNamespace)
			}
		}
	}

	return ctrl.Result{}, nil
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
