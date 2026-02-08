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
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	resourcepatchv1alpha1 "github.com/k8soneill/resource-patch-operator/api/v1alpha1"
)

const (
	testNamespace = "default"
)

// uniqueSuffix generates a unique suffix for test resource names using nanosecond precision
// to avoid name collisions in parallel test execution
func uniqueSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// cleanupPatchTracker deletes a PatchTracker, reconciles to process finalizer removal,
// and validates the object is fully deleted
func cleanupPatchTracker(ctx context.Context, reconciler *PatchTrackerReconciler, k8sClient client.Client, key types.NamespacedName) {
	patchTracker := &resourcepatchv1alpha1.PatchTracker{}
	if err := k8sClient.Get(ctx, key, patchTracker); err == nil {
		Expect(k8sClient.Delete(ctx, patchTracker)).To(Succeed())
		// Reconcile to process finalizer removal
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred(), "Reconcile should succeed during finalizer removal")
		// Validate the object is gone by checking for IsNotFound error
		Eventually(func() bool {
			err := k8sClient.Get(ctx, key, patchTracker)
			return apierrors.IsNotFound(err)
		}, "10s", "500ms").Should(BeTrue(), "PatchTracker should be fully deleted after finalizer removal")
	}
}

var _ = Describe("PatchTracker Controller", func() {
	Context("When reconciling with secret version tracking", func() {
		var (
			ctx              context.Context
			reconciler       *PatchTrackerReconciler
			namespace        string
			patchTrackerName string
			secretName       string
			deploymentName   string
			patchTrackerKey  types.NamespacedName
			secretKey        types.NamespacedName
			deploymentKey    types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = testNamespace
			patchTrackerName = "test-patchtracker-" + uniqueSuffix()
			secretName = "test-secret-" + uniqueSuffix()
			deploymentName = "test-deployment-" + uniqueSuffix()

			patchTrackerKey = types.NamespacedName{Name: patchTrackerName, Namespace: namespace}
			secretKey = types.NamespacedName{Name: secretName, Namespace: namespace}
			deploymentKey = types.NamespacedName{Name: deploymentName, Namespace: namespace}

			reconciler = &PatchTrackerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create a test Secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"key": []byte("initial-value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			// Create a test Deployment
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			// Create PatchTracker
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Optional:  false,
									Watch:     true,
								},
							},
						},
					},
					IgnoreMissingTarget: false,
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup resources
			cleanupPatchTracker(ctx, reconciler, k8sClient, patchTrackerKey)

			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, deploymentKey, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, secretKey, secret); err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should patch deployment on first reconcile (establishes baseline)", func() {
			By("Performing first reconcile")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PatchTracker status tracks the secret version")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "1s").Should(BeTrue())

			By("Verifying the deployment was patched with annotation")
			deployment := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "1s").Should(BeTrue())
		})

		It("should NOT patch deployment when secret has not changed", func() {
			By("Performing first reconcile to establish baseline")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for status to be updated")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "1s").Should(BeTrue())

			By("Getting the initial patch time from deployment")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			initialPatchTime := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(initialPatchTime).NotTo(BeEmpty())

			By("Performing second reconcile without changing secret")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was NOT patched again (timestamp unchanged)")
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			currentPatchTime := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(currentPatchTime).To(Equal(initialPatchTime), "Deployment should not be patched when secret hasn't changed")
		})

		It("should patch deployment when secret is updated", func() {
			By("Performing first reconcile to establish baseline")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for initial status update")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "1s").Should(BeTrue())

			By("Getting the initial patch time from deployment")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			initialPatchTime := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(initialPatchTime).NotTo(BeEmpty())

			By("Updating the secret (which changes its resourceVersion)")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			initialSecretVersion := secret.ResourceVersion
			secret.Data["key"] = []byte("updated-value")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("Verifying secret resourceVersion changed")
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			Expect(secret.ResourceVersion).NotTo(Equal(initialSecretVersion))

			By("Performing second reconcile after secret update")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment WAS patched (timestamp changed)")
			Eventually(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["resourcepatch.io/last-updated"]
			}, "10s", "1s").ShouldNot(Equal(initialPatchTime), "Deployment should be patched when secret changes")

			By("Verifying PatchTracker status has updated secret version")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets).NotTo(BeEmpty())
			secretVersionKey := namespace + "/" + secretName
			Expect(patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]).To(Equal(secret.ResourceVersion))
		})

		It("should handle multiple secrets with different change states", func() {
			secondSecretName := "test-secret-2-" + uniqueSuffix()
			secondSecretKey := types.NamespacedName{Name: secondSecretName, Namespace: namespace}

			By("Creating a second secret")
			secret2 := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secondSecretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret2)).To(Succeed())
			defer func() {
				if err := k8sClient.Get(ctx, secondSecretKey, secret2); err == nil {
					_ = k8sClient.Delete(ctx, secret2)
				}
			}()

			By("Updating PatchTracker to watch both secrets")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			patchTracker.Spec.Targets[0].SecretDeps = append(patchTracker.Spec.Targets[0].SecretDeps,
				resourcepatchv1alpha1.SecretRef{
					Name:      secondSecretName,
					Namespace: namespace,
					Optional:  false,
					Watch:     true,
				})
			Expect(k8sClient.Update(ctx, patchTracker)).To(Succeed())

			By("First reconcile - establishes baseline for both secrets")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for both secrets to be tracked")
			Eventually(func() int {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return 0
				}
				if len(patchTracker.Status.Targets) == 0 {
					return 0
				}
				return len(patchTracker.Status.Targets[0].SecretVersions)
			}, "10s", "1s").Should(Equal(2))

			By("Getting initial patch time")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			initialPatchTime := deployment.Annotations["resourcepatch.io/last-updated"]

			By("Updating only the second secret")
			Expect(k8sClient.Get(ctx, secondSecretKey, secret2)).To(Succeed())
			secret2.Data["key"] = []byte("updated-value")
			Expect(k8sClient.Update(ctx, secret2)).To(Succeed())

			By("Reconciling after one secret changed")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched because one secret changed")
			Eventually(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["resourcepatch.io/last-updated"]
			}, "10s", "1s").ShouldNot(Equal(initialPatchTime))
		})
	})

	Context("When using different patch methods", func() {
		var (
			ctx            context.Context
			reconciler     *PatchTrackerReconciler
			namespace      string
			secretName     string
			deploymentName string
			secretKey      types.NamespacedName
			deploymentKey  types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = testNamespace
			secretName = "test-secret-" + uniqueSuffix()
			deploymentName = "test-deployment-" + uniqueSuffix()

			secretKey = types.NamespacedName{Name: secretName, Namespace: namespace}
			deploymentKey = types.NamespacedName{Name: deploymentName, Namespace: namespace}

			reconciler = &PatchTrackerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create test Secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{"key": []byte("value")},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			// Create test Deployment
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "test",
								Image: "nginx:latest",
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		})

		AfterEach(func() {
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, deploymentKey, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, secretKey, secret); err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should patch with specific integer value", func() {
			specificValue := int64(5)
			rawValue, marshalErr := json.Marshal(specificValue)
			Expect(marshalErr).NotTo(HaveOccurred(), "json.Marshal should succeed for integer value")

			patchTrackerName := "test-specific-int-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:          "spec.replicas",
							Method:        "specific",
							SpecificValue: &apiextensionsv1.JSON{Raw: rawValue},
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment replicas updated to specific value")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return -1
				}
				if deployment.Spec.Replicas == nil {
					return -1
				}
				return *deployment.Spec.Replicas
			}, "10s", "1s").Should(Equal(int32(5)))
		})

		It("should patch with specific string value", func() {
			specificValue := "custom-annotation-value"
			rawValue, marshalErr := json.Marshal(specificValue)
			Expect(marshalErr).NotTo(HaveOccurred(), "json.Marshal should succeed for string value")

			patchTrackerName := "test-specific-string-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:          "metadata.annotations.custom-key",
							Method:        "specific",
							SpecificValue: &apiextensionsv1.JSON{Raw: rawValue},
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment annotation set to specific string")
			Eventually(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["custom-key"]
			}, "10s", "1s").Should(Equal("custom-annotation-value"))
		})

		It("should normalize float64 to int64 for whole numbers", func() {
			// JSON unmarshaling produces float64 for numbers, but we normalize to int64
			// when there's no fractional part (5.0 → 5)
			specificValue := 3 // Will be marshaled to JSON as 3, unmarshaled as float64(3)
			rawValue, marshalErr := json.Marshal(specificValue)
			Expect(marshalErr).NotTo(HaveOccurred(), "json.Marshal should succeed for numeric value")

			patchTrackerName := "test-float-normalize-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:          "spec.replicas",
							Method:        "specific",
							SpecificValue: &apiextensionsv1.JSON{Raw: rawValue},
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying replicas set to integer value (float64 normalized to int64)")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil || deployment.Spec.Replicas == nil {
					return -1
				}
				return *deployment.Spec.Replicas
			}, "10s", "1s").Should(Equal(int32(3)))
		})

		// Serial execution required: This test uses process-global env var PATCH_RANDOM_SEED
		It("should generate random string with deterministic seed", Serial, func() {
			Expect(os.Setenv("PATCH_RANDOM_SEED", "12345")).To(Succeed())
			defer func() { _ = os.Unsetenv("PATCH_RANDOM_SEED") }()

			patchTrackerName := "test-random-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:               "metadata.annotations.random-id",
							Method:             "randomString",
							RandomStringLength: 16,
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker for first time")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Capturing the first generated value")
			var firstValue string
			Eventually(func() int {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return 0
				}
				firstValue = deployment.Annotations["random-id"]
				return len(firstValue)
			}, "10s", "1s").Should(Equal(16))
			Expect(firstValue).NotTo(BeEmpty())

			By("Updating secret to trigger another patch with same seed")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			secret.Data["key"] = []byte("updated-value")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("Reconciling PatchTracker again with same seed")
			time.Sleep(1 * time.Second)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the same deterministic value is generated")
			Eventually(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["random-id"]
			}, "10s", "1s").Should(Equal(firstValue), "Same seed should produce identical random string")
		})

		It("should increment integer field", func() {
			patchTrackerName := "test-increment-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:   "spec.replicas",
							Method: "increasingInteger",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Getting initial replicas count")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			initialReplicas := *deployment.Spec.Replicas

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying replicas incremented by 1")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil || deployment.Spec.Replicas == nil {
					return -1
				}
				return *deployment.Spec.Replicas
			}, "10s", "1s").Should(Equal(initialReplicas + 1))
		})

		It("should handle missing field for increasingInteger (start from 0)", func() {
			patchTrackerName := "test-increment-missing-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:   "metadata.annotations.counter",
							Method: "increasingInteger",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying counter starts at 0")
			Eventually(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["counter"]
			}, "10s", "1s").Should(Equal("0"))
		})

		It("should return error for increasingInteger on non-numeric field", func() {
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			if deployment.Annotations == nil {
				deployment.Annotations = make(map[string]string)
			}
			deployment.Annotations["bad-field"] = "not-a-number"
			Expect(k8sClient.Update(ctx, deployment)).To(Succeed())

			patchTrackerName := "test-increment-error-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:   "metadata.annotations.bad-field",
							Method: "increasingInteger",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker - error is logged but not returned (partial failure handling)")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred(), "Reconcile completes but logs the error")

			By("Verifying the field was not modified")
			Consistently(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["bad-field"]
			}, "2s", "500ms").Should(Equal("not-a-number"), "Field should remain unchanged due to validation error")
		})

		It("should accept whole-number float (like 5.0) for increasingInteger", func() {
			// Note: This test verifies that whole-number floats (5.0) work correctly.
			// The code also rejects fractional floats (5.7) with an error to prevent
			// silent truncation, but testing that requires unstructured data which is
			// difficult to inject into typed Kubernetes resources in integration tests.
			// The validation is tested implicitly through the type system.

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			initialReplicas := *deployment.Spec.Replicas

			patchTrackerName := "test-increment-float-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:   "spec.replicas",
							Method: "increasingInteger",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying replicas were incremented (whole-number floats like 1.0 → 2 work correctly)")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil || deployment.Spec.Replicas == nil {
					return -1
				}
				return *deployment.Spec.Replicas
			}, "10s", "1s").Should(Equal(initialReplicas + 1))
		})

		It("should validate specific method requires specificValue", func() {
			patchTrackerName := "test-specific-missing-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:   "spec.replicas",
							Method: "specific",
							// Missing SpecificValue
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling - error is logged but not returned (partial failure handling)")
			initialReplicas := int32(1)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred(), "Reconcile completes but logs the validation error")

			By("Verifying replicas were not changed due to validation error")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			Expect(*deployment.Spec.Replicas).To(Equal(initialReplicas), "Replicas should remain unchanged")
		})

		It("should use default timestamp method when method field omitted", func() {
			patchTrackerName := "test-default-timestamp-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
							// Method omitted - should default to "timestamp"
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying timestamp annotation was added (default behavior)")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "1s").Should(BeTrue())
		})

		It("should increment string number field", func() {
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			if deployment.Annotations == nil {
				deployment.Annotations = make(map[string]string)
			}
			deployment.Annotations["counter"] = "42"
			Expect(k8sClient.Update(ctx, deployment)).To(Succeed())

			patchTrackerName := "test-increment-string-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:   "metadata.annotations.counter",
							Method: "increasingInteger",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying counter incremented from string '42' to '43'")
			Eventually(func() string {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return ""
				}
				return deployment.Annotations["counter"]
			}, "10s", "1s").Should(Equal("43"))
		})

		It("should use custom random string length", func() {
			patchTrackerName := "test-random-length-" + uniqueSuffix()
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path:               "metadata.annotations.random-id",
							Method:             "randomString",
							RandomStringLength: 64,
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())
			defer cleanupPatchTracker(ctx, reconciler, k8sClient, types.NamespacedName{
				Name:      patchTrackerName,
				Namespace: namespace,
			})

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying random string has custom length of 64")
			Eventually(func() int {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return 0
				}
				value := deployment.Annotations["random-id"]
				return len(value)
			}, "10s", "1s").Should(Equal(64))
		})
	})

	Context("When updating status fields", func() {
		var (
			ctx              context.Context
			reconciler       *PatchTrackerReconciler
			namespace        string
			patchTrackerName string
			secretName       string
			deploymentName   string
			patchTrackerKey  types.NamespacedName
			secretKey        types.NamespacedName
			deploymentKey    types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = testNamespace
			patchTrackerName = "test-status-" + uniqueSuffix()
			secretName = "test-secret-" + uniqueSuffix()
			deploymentName = "test-deployment-" + uniqueSuffix()

			patchTrackerKey = types.NamespacedName{Name: patchTrackerName, Namespace: namespace}
			secretKey = types.NamespacedName{Name: secretName, Namespace: namespace}
			deploymentKey = types.NamespacedName{Name: deploymentName, Namespace: namespace}

			reconciler = &PatchTrackerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create a test Secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"key": []byte("initial-value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			// Create a test Deployment
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup resources
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			if err := k8sClient.Get(ctx, patchTrackerKey, patchTracker); err == nil {
				cleanupPatchTracker(ctx, reconciler, k8sClient, patchTrackerKey)
			}

			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, deploymentKey, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, secretKey, secret); err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should set LastPatchTime on first successful patch", func() {
			By("Creating PatchTracker")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Performing first reconcile")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying LastPatchTime is set")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				return patchTracker.Status.Targets[0].LastPatchTime != nil
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying no error fields are set")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets[0].LastError).To(BeEmpty())
			Expect(patchTracker.Status.Targets[0].LastErrorTime).To(BeNil())
		})

		It("should update LastPatchTime on subsequent successful patches", func() {
			By("Creating PatchTracker")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Performing first reconcile")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Capturing first LastPatchTime")
			var firstPatchTime *metav1.Time
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				if patchTracker.Status.Targets[0].LastPatchTime == nil {
					return false
				}
				firstPatchTime = patchTracker.Status.Targets[0].LastPatchTime
				return true
			}, "10s", "500ms").Should(BeTrue())

			By("Updating secret to trigger second patch")
			// Delay needed for test infrastructure: ensures status update fully propagates
			// through test client cache before next patch. Not a timestamp precision issue -
			// metav1.Now() has nanosecond precision. This is purely for test reliability.
			time.Sleep(1 * time.Second)
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			secret.Data["key"] = []byte("updated-value")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("Performing second reconcile")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying LastPatchTime was updated (this is the bug we fixed)")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secondPatchTime := patchTracker.Status.Targets[0].LastPatchTime
				if secondPatchTime == nil {
					return false
				}
				return secondPatchTime.After(firstPatchTime.Time)
			}, "10s", "500ms").Should(BeTrue(), "LastPatchTime should be updated on every successful patch")
		})

		It("should set LastError and LastErrorTime on patch failure", func() {
			By("Creating PatchTracker with invalid patch operation (invalid JSON patch)")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								// Try to patch a nested field that doesn't exist
								Path: "spec.nonexistent.deeply.nested.field",
							},
							PatchStrategy: "jsonPatch", // JSON patch will fail on non-existent path
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Performing reconcile that will fail")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred()) // Controller doesn't return error, it logs it

			By("Verifying LastError is set")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				return patchTracker.Status.Targets[0].LastError != ""
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying LastErrorTime is set")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets[0].LastErrorTime).NotTo(BeNil())
			Expect(patchTracker.Status.Targets[0].LastError).NotTo(BeEmpty())
		})

		It("should clear error fields when patch succeeds after previous error", func() {
			By("Creating PatchTracker with invalid patch operation initially")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								// Start with invalid path that will fail
								Path: "spec.nonexistent.field",
							},
							PatchStrategy: "jsonPatch",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Performing first reconcile that will fail")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for error to be recorded")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				return patchTracker.Status.Targets[0].LastError != ""
			}, "10s", "500ms").Should(BeTrue())

			By("Updating PatchTracker to use valid patch path")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			patchTracker.Spec.Targets[0].PatchField.Path = "metadata.annotations"
			patchTracker.Spec.Targets[0].PatchStrategy = "strategicMerge"
			Expect(k8sClient.Update(ctx, patchTracker)).To(Succeed())

			By("Updating secret to trigger reconcile")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			secret.Data["key"] = []byte("updated-value")
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("Performing second reconcile that will succeed")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying error fields are cleared")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				return patchTracker.Status.Targets[0].LastError == "" &&
					patchTracker.Status.Targets[0].LastErrorTime == nil
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying LastPatchTime is now set")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets[0].LastPatchTime).NotTo(BeNil())
		})

		It("should update SecretVersions on successful patch", func() {
			By("Creating PatchTracker")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Getting current secret version")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			currentSecretVersion := secret.ResourceVersion

			By("Performing reconcile")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying SecretVersions is updated with current version")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretKey := namespace + "/" + secretName
				version, exists := patchTracker.Status.Targets[0].SecretVersions[secretKey]
				return exists && version == currentSecretVersion
			}, "10s", "500ms").Should(BeTrue())
		})

		It("should NOT update SecretVersions when patch fails", func() {
			By("Creating PatchTracker with invalid patch operation")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "spec.invalid.path.that.does.not.exist",
							},
							PatchStrategy: "jsonPatch",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Performing reconcile that will fail")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for status to be created with error")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				if len(patchTracker.Status.Targets) == 0 {
					return false
				}
				return patchTracker.Status.Targets[0].LastError != ""
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying SecretVersions is NOT populated (empty)")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			secretKey := namespace + "/" + secretName
			_, exists := patchTracker.Status.Targets[0].SecretVersions[secretKey]
			Expect(exists).To(BeFalse(), "SecretVersions should not be updated when patch fails")
		})

		It("should track multiple consecutive successful patches with updated timestamps", func() {
			By("Creating PatchTracker")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			var patchTimes []*metav1.Time

			for i := 0; i < 3; i++ {
				By(fmt.Sprintf("Performing patch %d", i+1))

				// Update secret to trigger patch (except first time)
				if i > 0 {
					// Delay for test infrastructure (see explanation in previous test)
					time.Sleep(1 * time.Second)
					secret := &corev1.Secret{}
					Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
					secret.Data["key"] = []byte(fmt.Sprintf("value-%d", i))
					Expect(k8sClient.Update(ctx, secret)).To(Succeed())
				}

				// Reconcile
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
				Expect(err).NotTo(HaveOccurred())

				// Capture patch time
				var currentPatchTime *metav1.Time
				Eventually(func() bool {
					patchTracker := &resourcepatchv1alpha1.PatchTracker{}
					err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
					if err != nil {
						return false
					}
					if len(patchTracker.Status.Targets) == 0 {
						return false
					}
					if patchTracker.Status.Targets[0].LastPatchTime == nil {
						return false
					}
					// For subsequent patches, ensure time has advanced
					if i > 0 && !patchTracker.Status.Targets[0].LastPatchTime.After(patchTimes[i-1].Time) {
						return false
					}
					currentPatchTime = patchTracker.Status.Targets[0].LastPatchTime
					return true
				}, "10s", "500ms").Should(BeTrue())

				patchTimes = append(patchTimes, currentPatchTime)

				By(fmt.Sprintf("Verified patch %d has timestamp: %v", i+1, currentPatchTime.Time))
			}

			By("Verifying all three timestamps are different and increasing")
			Expect(patchTimes[0].Time).To(BeTemporally("<", patchTimes[1].Time))
			Expect(patchTimes[1].Time).To(BeTemporally("<", patchTimes[2].Time))
		})
	})

	Context("When handling missing targets with IgnoreMissingTarget", func() {
		var (
			ctx              context.Context
			reconciler       *PatchTrackerReconciler
			namespace        string
			patchTrackerName string
			secretName       string
			deploymentName   string
			patchTrackerKey  types.NamespacedName
			secretKey        types.NamespacedName
			deploymentKey    types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = testNamespace
			patchTrackerName = "test-ignore-missing-" + uniqueSuffix()
			secretName = "test-secret-" + uniqueSuffix()
			deploymentName = "test-deployment-" + uniqueSuffix()

			patchTrackerKey = types.NamespacedName{Name: patchTrackerName, Namespace: namespace}
			secretKey = types.NamespacedName{Name: secretName, Namespace: namespace}
			deploymentKey = types.NamespacedName{Name: deploymentName, Namespace: namespace}

			reconciler = &PatchTrackerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create test Secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{"key": []byte("value")},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup PatchTracker
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			if err := k8sClient.Get(ctx, patchTrackerKey, patchTracker); err == nil {
				cleanupPatchTracker(ctx, reconciler, k8sClient, patchTrackerKey)
			}

			// Cleanup Deployment (may not exist in all tests)
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, deploymentKey, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			// Cleanup Secret
			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, secretKey, secret); err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should not update SecretVersions when target missing and IgnoreMissingTarget=true", func() {
			By("Creating PatchTracker with IgnoreMissingTarget=true and non-existent target")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					IgnoreMissingTarget: true,
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName, // Doesn't exist yet
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling with missing target")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status was created but SecretVersions is empty")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil {
					return false
				}
				// Status should be created
				return len(patchTracker.Status.Targets) > 0
			}, "10s", "500ms").Should(BeTrue())

			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			targetStatus := patchTracker.Status.Targets[0]

			// SecretVersions should be empty (not updated)
			secretVersionKey := namespace + "/" + secretName
			_, exists := targetStatus.SecretVersions[secretVersionKey]
			Expect(exists).To(BeFalse(), "SecretVersions should NOT be updated when target is missing")

			// LastPatchTime should be nil (not updated)
			Expect(targetStatus.LastPatchTime).To(BeNil(), "LastPatchTime should NOT be set when target is missing")

			// No error should be recorded (this is expected behavior)
			Expect(targetStatus.LastError).To(BeEmpty(), "LastError should be empty for missing target with IgnoreMissingTarget=true")
			Expect(targetStatus.LastErrorTime).To(BeNil(), "LastErrorTime should be nil for missing target with IgnoreMissingTarget=true")
		})

		It("should patch target when created after initial reconcile with IgnoreMissingTarget=true", func() {
			By("Creating PatchTracker with IgnoreMissingTarget=true and non-existent target")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					IgnoreMissingTarget: true,
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling with missing target (first time)")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying SecretVersions not updated for missing target")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil || len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return !exists // Should NOT exist
			}, "10s", "500ms").Should(BeTrue())

			By("Creating the target deployment")
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("Reconciling again now that target exists")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue(), "Deployment should be patched when it's created")

			By("Verifying SecretVersions now updated")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil || len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "500ms").Should(BeTrue(), "SecretVersions should be updated after successful patch")

			By("Verifying LastPatchTime is set")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets[0].LastPatchTime).NotTo(BeNil(), "LastPatchTime should be set after successful patch")
		})

		It("should patch normally when target exists and IgnoreMissingTarget=true", func() {
			By("Creating target deployment first")
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("Creating PatchTracker with IgnoreMissingTarget=true")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					IgnoreMissingTarget: true,
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling with existing target")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying SecretVersions updated")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil || len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying LastPatchTime is set")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets[0].LastPatchTime).NotTo(BeNil())
			Expect(patchTracker.Status.Targets[0].LastError).To(BeEmpty())
		})

		It("should error when target missing and IgnoreMissingTarget=false", func() {
			By("Creating PatchTracker with IgnoreMissingTarget=false and non-existent target")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					IgnoreMissingTarget: false,
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName, // Doesn't exist
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling with missing target")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred()) // Controller logs error but doesn't return it

			By("Verifying error status is recorded")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil || len(patchTracker.Status.Targets) == 0 {
					return false
				}
				return patchTracker.Status.Targets[0].LastError != ""
			}, "10s", "500ms").Should(BeTrue())

			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			targetStatus := patchTracker.Status.Targets[0]

			// Error should be recorded
			Expect(targetStatus.LastError).To(ContainSubstring("not found"), "Error should mention resource not found")
			Expect(targetStatus.LastErrorTime).NotTo(BeNil(), "LastErrorTime should be set")

			// SecretVersions should NOT be updated (patch failed)
			secretVersionKey := namespace + "/" + secretName
			_, exists := targetStatus.SecretVersions[secretVersionKey]
			Expect(exists).To(BeFalse(), "SecretVersions should NOT be updated when patch fails")

			// LastPatchTime should be nil
			Expect(targetStatus.LastPatchTime).To(BeNil(), "LastPatchTime should NOT be set when patch fails")
		})

		It("should patch normally when target exists and IgnoreMissingTarget=false", func() {
			By("Creating target deployment first")
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("Creating PatchTracker with IgnoreMissingTarget=false")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					IgnoreMissingTarget: false,
					Targets: []resourcepatchv1alpha1.TargetRef{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       deploymentName,
							Namespace:  namespace,
							PatchField: resourcepatchv1alpha1.PatchField{
								Path: "metadata.annotations",
							},
							PatchStrategy: "strategicMerge",
							SecretDeps: []resourcepatchv1alpha1.SecretRef{
								{
									Name:      secretName,
									Namespace: namespace,
									Watch:     true,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling with existing target")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying SecretVersions updated")
			Eventually(func() bool {
				patchTracker := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, patchTracker)
				if err != nil || len(patchTracker.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying LastPatchTime is set and no errors")
			patchTracker = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, patchTracker)).To(Succeed())
			Expect(patchTracker.Status.Targets[0].LastPatchTime).NotTo(BeNil())
			Expect(patchTracker.Status.Targets[0].LastError).To(BeEmpty())
			Expect(patchTracker.Status.Targets[0].LastErrorTime).To(BeNil())
		})
	})

	Context("When using maintenance windows", func() {
		var (
			ctx              context.Context
			reconciler       *PatchTrackerReconciler
			namespace        string
			patchTrackerName string
			secretName       string
			deploymentName   string
			patchTrackerKey  types.NamespacedName
			secretKey        types.NamespacedName
			deploymentKey    types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			namespace = testNamespace
			patchTrackerName = "test-mw-" + uniqueSuffix()
			secretName = "test-secret-" + uniqueSuffix()
			deploymentName = "test-deployment-" + uniqueSuffix()

			patchTrackerKey = types.NamespacedName{Name: patchTrackerName, Namespace: namespace}
			secretKey = types.NamespacedName{Name: secretName, Namespace: namespace}
			deploymentKey = types.NamespacedName{Name: deploymentName, Namespace: namespace}

			reconciler = &PatchTrackerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create test Secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{"key": []byte("value")},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			// Create test Deployment
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "test",
								Image: "nginx:latest",
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		})

		AfterEach(func() {
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			if err := k8sClient.Get(ctx, patchTrackerKey, patchTracker); err == nil {
				cleanupPatchTracker(ctx, reconciler, k8sClient, patchTrackerKey)
			}

			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, deploymentKey, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, secretKey, secret); err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should defer patches when before maintenance window start", func() {
			By("Creating PatchTracker with maintenance window 1 hour in the future")
			futureStart := metav1.NewTime(time.Now().Add(1 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start:    futureStart,
							Duration: &metav1.Duration{Duration: 2 * time.Hour},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be deferred")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying result has RequeueAfter set")
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "Should requeue for maintenance window")
			Expect(result.RequeueAfter).To(BeNumerically("<=", 1*time.Hour), "RequeueAfter should be approximately time until window start")

			By("Verifying deployment was NOT patched")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(exists).To(BeFalse(), "Deployment should not be patched before maintenance window")

			By("Verifying PatchesDeferred condition is set")
			Eventually(func() bool {
				pt := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, pt)
				if err != nil {
					return false
				}
				for _, c := range pt.Status.Conditions {
					if c.Type == conditionPatchesDeferred && c.Status == metav1.ConditionTrue && c.Reason == "MaintenanceWindowNotActive" {
						return true
					}
				}
				return false
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying PendingPatchCount is set")
			pt := &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, pt)).To(Succeed())
			Expect(pt.Status.PendingPatchCount).To(Equal(1))
		})

		It("should apply patches when inside maintenance window", func() {
			By("Creating PatchTracker with maintenance window starting in the past")
			pastStart := metav1.NewTime(time.Now().Add(-1 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start:    pastStart,
							Duration: &metav1.Duration{Duration: 2 * time.Hour},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be applied")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying PendingPatchCount is 0")
			pt := &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, pt)).To(Succeed())
			Expect(pt.Status.PendingPatchCount).To(Equal(0))
		})

		It("should block patches when maintenance window has expired (strict)", func() {
			By("Creating PatchTracker with expired maintenance window")
			pastStart := metav1.NewTime(time.Now().Add(-2 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start:    pastStart,
							Duration: &metav1.Duration{Duration: 1 * time.Hour},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be blocked")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no requeue (user must intervene)")
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)), "Should not requeue when window expired")

			By("Verifying deployment was NOT patched")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(exists).To(BeFalse(), "Deployment should not be patched when window expired")

			By("Verifying PatchesDeferred condition shows expired")
			Eventually(func() bool {
				pt := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, pt)
				if err != nil {
					return false
				}
				for _, c := range pt.Status.Conditions {
					if c.Type == conditionPatchesDeferred && c.Status == metav1.ConditionTrue && c.Reason == "MaintenanceWindowExpired" {
						return true
					}
				}
				return false
			}, "10s", "500ms").Should(BeTrue())
		})

		It("should patch immediately when no maintenance window is set (backward compat)", func() {
			By("Creating PatchTracker without maintenance window")
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be applied immediately")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue())
		})

		It("should patch when notBefore (no duration) is in the past", func() {
			By("Creating PatchTracker with notBefore in the past (no duration)")
			pastStart := metav1.NewTime(time.Now().Add(-1 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start: pastStart,
							// No Duration - pure notBefore
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be applied")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue())
		})

		It("should defer when notBefore (no duration) is in the future", func() {
			By("Creating PatchTracker with notBefore in the future (no duration)")
			futureStart := metav1.NewTime(time.Now().Add(1 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start: futureStart,
							// No Duration - pure notBefore
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be deferred")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			By("Verifying deployment was NOT patched")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(exists).To(BeFalse())
		})

		It("should apply deferred patches when maintenance window opens", func() {
			By("Creating PatchTracker with future maintenance window")
			futureStart := metav1.NewTime(time.Now().Add(1 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start:    futureStart,
							Duration: &metav1.Duration{Duration: 2 * time.Hour},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be deferred")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying SecretVersions are NOT updated (deferred)")
			Eventually(func() bool {
				pt := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, pt)
				if err != nil {
					return false
				}
				return pt.Status.PendingPatchCount == 1
			}, "10s", "500ms").Should(BeTrue())

			By("Moving maintenance window to current time (simulating window opening)")
			pt := &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, pt)).To(Succeed())
			pt.Spec.Reconcile.MaintenanceWindow.Start = metav1.NewTime(time.Now().Add(-1 * time.Hour))
			Expect(k8sClient.Update(ctx, pt)).To(Succeed())

			By("Reconciling again - patches should now be applied")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying deployment was patched")
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, deploymentKey, deployment)
				if err != nil {
					return false
				}
				_, exists := deployment.Annotations["resourcepatch.io/last-updated"]
				return exists
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying SecretVersions are now updated")
			Eventually(func() bool {
				pt := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, pt)
				if err != nil || len(pt.Status.Targets) == 0 {
					return false
				}
				secretVersionKey := namespace + "/" + secretName
				_, exists := pt.Status.Targets[0].SecretVersions[secretVersionKey]
				return exists
			}, "10s", "500ms").Should(BeTrue())
		})

		It("should clear PatchesDeferred condition after patches are applied", func() {
			By("Creating PatchTracker with future maintenance window")
			futureStart := metav1.NewTime(time.Now().Add(1 * time.Hour))
			patchTracker := &resourcepatchv1alpha1.PatchTracker{
				ObjectMeta: metav1.ObjectMeta{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
				Spec: resourcepatchv1alpha1.PatchTrackerSpec{
					Targets: []resourcepatchv1alpha1.TargetRef{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
						Namespace:  namespace,
						PatchField: resourcepatchv1alpha1.PatchField{
							Path: "metadata.annotations",
						},
						PatchStrategy: "strategicMerge",
						SecretDeps: []resourcepatchv1alpha1.SecretRef{{
							Name:      secretName,
							Namespace: namespace,
							Watch:     true,
						}},
					}},
					Reconcile: resourcepatchv1alpha1.ReconcileOptions{
						MaintenanceWindow: &resourcepatchv1alpha1.MaintenanceWindow{
							Start:    futureStart,
							Duration: &metav1.Duration{Duration: 2 * time.Hour},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, patchTracker)).To(Succeed())

			By("Reconciling - patches should be deferred")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PatchesDeferred condition is True")
			Eventually(func() bool {
				pt := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, pt)
				if err != nil {
					return false
				}
				for _, c := range pt.Status.Conditions {
					if c.Type == conditionPatchesDeferred && c.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, "10s", "500ms").Should(BeTrue())

			By("Moving maintenance window to current time")
			pt := &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, pt)).To(Succeed())
			pt.Spec.Reconcile.MaintenanceWindow.Start = metav1.NewTime(time.Now().Add(-1 * time.Hour))
			Expect(k8sClient.Update(ctx, pt)).To(Succeed())

			By("Reconciling again - patches should now be applied")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: patchTrackerKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PatchesDeferred condition is now False")
			Eventually(func() bool {
				pt := &resourcepatchv1alpha1.PatchTracker{}
				err := k8sClient.Get(ctx, patchTrackerKey, pt)
				if err != nil {
					return false
				}
				for _, c := range pt.Status.Conditions {
					if c.Type == conditionPatchesDeferred && c.Status == metav1.ConditionFalse && c.Reason == "PatchesApplied" {
						return true
					}
				}
				return false
			}, "10s", "500ms").Should(BeTrue())

			By("Verifying PendingPatchCount is 0")
			pt = &resourcepatchv1alpha1.PatchTracker{}
			Expect(k8sClient.Get(ctx, patchTrackerKey, pt)).To(Succeed())
			Expect(pt.Status.PendingPatchCount).To(Equal(0))
		})
	})
})
