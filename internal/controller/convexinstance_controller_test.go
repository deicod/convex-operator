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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
)

var _ = Describe("ConvexInstance Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		convexinstance := &convexv1alpha1.ConvexInstance{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind ConvexInstance")
			err := k8sClient.Get(ctx, typeNamespacedName, convexinstance)
			if err != nil && errors.IsNotFound(err) {
				resource := &convexv1alpha1.ConvexInstance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					// Minimal spec to satisfy CRD validation and exercise reconciliation.
					Spec: convexv1alpha1.ConvexInstanceSpec{
						Environment: "dev",
						Version:     "1.0.0",
						Backend: convexv1alpha1.BackendSpec{
							DB: convexv1alpha1.BackendDatabaseSpec{
								Engine: "sqlite",
							},
						},
						Networking: convexv1alpha1.NetworkingSpec{
							Host: "convex-dev.example.com",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &convexv1alpha1.ConvexInstance{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ConvexInstance")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ConvexInstanceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("ensuring status is initialized")
			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.GetGeneration()))
			Expect(updated.Status.Phase).To(Equal("Pending"))
		})
	})
})
