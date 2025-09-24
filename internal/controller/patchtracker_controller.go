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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	naradav1alpha1 "github.com/k8soneill/narada-operator/api/v1alpha1"
)

// PatchTrackerReconciler reconciles a PatchTracker object
type PatchTrackerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=narada.io.github.k8soneill,resources=patchtrackers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=narada.io.github.k8soneill,resources=patchtrackers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=narada.io.github.k8soneill,resources=patchtrackers/finalizers,verbs=update

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
	patchTracker := &naradav1alpha1.PatchTracker{}
	if err := r.Get(ctx, req.NamespacedName, patchTracker); err != nil {
		logger.Error(err, "unable to fetch PatchTracker")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Finalizer name
	finalizerName := "patchtracker.narada.io/finalizer"
	// Examine DeletionTimestamp to determine if object is under deletion
	if patchTracker.ObjectMeta.DeletionTimestamp.IsZero() {
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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PatchTrackerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&naradav1alpha1.PatchTracker{}).
		Named("patchtracker").
		Complete(r)
}
