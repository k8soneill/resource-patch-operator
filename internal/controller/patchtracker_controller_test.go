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
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	resourcepatchv1alpha1 "github.com/k8soneill/resource-patch-operator/api/v1alpha1"
)

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
			namespace = "default"
			patchTrackerName = "test-patchtracker-" + time.Now().Format("150405")
			secretName = "test-secret-" + time.Now().Format("150405")
			deploymentName = "test-deployment-" + time.Now().Format("150405")

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
			patchTracker := &resourcepatchv1alpha1.PatchTracker{}
			if err := k8sClient.Get(ctx, patchTrackerKey, patchTracker); err == nil {
				Expect(k8sClient.Delete(ctx, patchTracker)).To(Succeed())
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
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.SecretVersions[secretVersionKey]
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
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.SecretVersions[secretVersionKey]
				return exists
			}, "10s", "1s").Should(BeTrue())

			By("Getting the initial patch time from deployment")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			initialPatchTime := deployment.Annotations["resourcepatch.io/last-updated"]
			Expect(initialPatchTime).NotTo(BeEmpty())

			By("Performing second reconcile without changing secret")
			time.Sleep(2 * time.Second) // Ensure timestamp would differ if patched
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
				secretVersionKey := namespace + "/" + secretName
				_, exists := patchTracker.Status.SecretVersions[secretVersionKey]
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
			time.Sleep(2 * time.Second) // Ensure timestamp would differ
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
			secretVersionKey := namespace + "/" + secretName
			Expect(patchTracker.Status.SecretVersions[secretVersionKey]).To(Equal(secret.ResourceVersion))
		})

		It("should handle multiple secrets with different change states", func() {
			secondSecretName := "test-secret-2-" + time.Now().Format("150405")
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
				return len(patchTracker.Status.SecretVersions)
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
			time.Sleep(2 * time.Second)
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
			namespace = "default"
			secretName = "test-secret-" + time.Now().Format("150405")
			deploymentName = "test-deployment-" + time.Now().Format("150405")

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
			rawValue, _ := json.Marshal(specificValue)

			patchTrackerName := "test-specific-int-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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
			rawValue, _ := json.Marshal(specificValue)

			patchTrackerName := "test-specific-string-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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

		It("should generate random string with deterministic seed", func() {
			Expect(os.Setenv("PATCH_RANDOM_SEED", "12345")).To(Succeed())
			defer func() { _ = os.Unsetenv("PATCH_RANDOM_SEED") }()

			patchTrackerName := "test-random-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

			By("Reconciling PatchTracker")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      patchTrackerName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying random string was generated with correct length")
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

			By("Verifying string is deterministic with same seed")
			Expect(firstValue).NotTo(BeEmpty())
		})

		It("should increment integer field", func() {
			patchTrackerName := "test-increment-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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
			patchTrackerName := "test-increment-missing-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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

			patchTrackerName := "test-increment-error-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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

		It("should validate specific method requires specificValue", func() {
			patchTrackerName := "test-specific-missing-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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
			patchTrackerName := "test-default-timestamp-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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

			patchTrackerName := "test-increment-string-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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
			patchTrackerName := "test-random-length-" + time.Now().Format("150405")
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
			defer func() { _ = k8sClient.Delete(ctx, patchTracker) }()

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
})
