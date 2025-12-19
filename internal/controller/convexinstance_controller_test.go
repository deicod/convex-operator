/*
Copyright (C) 2025 Darko Luketic <info@icod.de>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/

package controller

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	condSuccessCriteriaMet = batchv1.JobConditionType("SuccessCriteriaMet")
	condFailureTarget      = batchv1.JobConditionType("FailureTarget")
)

var _ = Describe("ConvexInstance Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const testBackendImageV2 = "ghcr.io/get-convex/convex-backend:2.0.0"
		const testVersionV2 = "2.0.0"
		const strategyExportImport = "exportImport"

		ctx := context.Background()
		newReconciler := func() (*ConvexInstanceReconciler, *record.FakeRecorder) {
			rec := record.NewFakeRecorder(64)
			return &ConvexInstanceReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: rec,
			}, rec
		}

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		convexinstance := &convexv1alpha1.ConvexInstance{}
		makeBackendReady := func() {
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			sts.Status.ReadyReplicas = 1
			sts.Status.Replicas = 1
			sts.Status.UpdatedReplicas = 1
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		}
		makeDashboardReady := func() {
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dep)).To(Succeed())
			dep.Status.Replicas = 1
			dep.Status.UpdatedReplicas = 1
			dep.Status.ReadyReplicas = 1
			dep.Status.ObservedGeneration = dep.Generation
			Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
		}
		makeGatewayReady := func() {
			gw := &gatewayv1.Gateway{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-gateway", Namespace: "default"}, gw)).To(Succeed())
			gw.Status.Conditions = []metav1.Condition{{
				Type:               string(gatewayv1.GatewayConditionReady),
				Status:             metav1.ConditionTrue,
				Reason:             "Ready",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: gw.GetGeneration(),
			}}
			Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())
		}
		makeRouteAccepted := func() {
			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-route", Namespace: "default"}, route)).To(Succeed())
			route.Status.Parents = []gatewayv1.RouteParentStatus{{
				ParentRef: gatewayv1.ParentReference{
					Name:      gatewayv1.ObjectName("test-resource-gateway"),
					Namespace: ptr.To(gatewayv1.Namespace("default")),
				},
				ControllerName: gatewayv1.GatewayController("test.example/controller"),
				Conditions: []metav1.Condition{{
					Type:               string(gatewayv1.RouteConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					Message:            "Attached to listener",
					LastTransitionTime: metav1.Now(),
					ObservedGeneration: route.GetGeneration(),
				}},
			}}
			Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
		}

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
			resource := &convexv1alpha1.ConvexInstance{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if errors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ConvexInstance")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			reconciler, _ := newReconciler()
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, &convexv1alpha1.ConvexInstance{})
				return errors.IsNotFound(err)
			}, 5*time.Second, 200*time.Millisecond).Should(BeTrue())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler, _ := newReconciler()

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			By("ensuring status is initialized")
			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.GetGeneration()))
			Expect(updated.Status.Phase).To(Equal("Pending"))

			By("ensuring core resources are created")
			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend-config", Namespace: "default"}, configMap)).To(Succeed())
			Expect(configMap.Data).To(HaveKey("convex.conf"))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-convex-secrets", Namespace: "default"}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("adminKey"))
			Expect(secret.Data).To(HaveKey("instanceSecret"))
			adminKey := string(secret.Data[adminKeyKey])
			Expect(adminKey).To(ContainSubstring("|"))
			adminKeyParts := strings.SplitN(adminKey, "|", 2)
			Expect(adminKeyParts[0]).To(Equal(strings.ReplaceAll(resourceName, "-", "_")))
			Expect(adminKeyParts[1]).NotTo(BeEmpty())
			instanceSecret := string(secret.Data[instanceSecretKey])
			Expect(len(instanceSecret)).To(Equal(instanceSecretHexLen))
			decodedSecret, err := hex.DecodeString(instanceSecret)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(decodedSecret)).To(Equal(instanceSecretBytes))

			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, service)).To(Succeed())
			Expect(service.Spec.Ports).To(HaveLen(2))

			dashboardSvc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dashboardSvc)).To(Succeed())
			Expect(dashboardSvc.Spec.Ports).To(HaveLen(1))
			Expect(dashboardSvc.Spec.Ports[0].Port).To(Equal(int32(6791)))

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			Expect(sts.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			if sts.Spec.Template.Spec.SecurityContext != nil {
				Expect(sts.Spec.Template.Spec.SecurityContext.RunAsUser).To(BeNil())
				Expect(sts.Spec.Template.Spec.SecurityContext.RunAsNonRoot).To(BeNil())
			}
			if sts.Spec.Template.Spec.Containers[0].SecurityContext != nil {
				Expect(sts.Spec.Template.Spec.Containers[0].SecurityContext.RunAsUser).To(BeNil())
				Expect(sts.Spec.Template.Spec.Containers[0].SecurityContext.RunAsNonRoot).To(BeNil())
			}

			dashboardDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dashboardDep)).To(Succeed())
			Expect(dashboardDep.Spec.Template.Spec.SecurityContext).NotTo(BeNil())
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.SecurityContext.RunAsUser, int64(0))).To(Equal(int64(1001)))
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.SecurityContext.RunAsNonRoot, false)).To(BeTrue())
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.Containers[0].SecurityContext.RunAsUser, int64(0))).To(Equal(int64(1001)))
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.Containers[0].SecurityContext.RunAsNonRoot, false)).To(BeTrue())

			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("WaitingForBackend"))
			Expect(readyCond.Message).To(ContainSubstring("Waiting for backend readiness"))

			configReady := meta.FindStatusCondition(updated.Status.Conditions, "ConfigMapReady")
			Expect(configReady).NotTo(BeNil())
			Expect(configReady.Status).To(Equal(metav1.ConditionTrue))
			Expect(configReady.Reason).To(Equal("Available"))

			secretReady := meta.FindStatusCondition(updated.Status.Conditions, "SecretsReady")
			Expect(secretReady).NotTo(BeNil())
			Expect(secretReady.Status).To(Equal(metav1.ConditionTrue))

			pvcReady := meta.FindStatusCondition(updated.Status.Conditions, "PVCReady")
			Expect(pvcReady).NotTo(BeNil())
			Expect(pvcReady.Status).To(Equal(metav1.ConditionTrue))
			Expect(pvcReady.Reason).To(Equal("Skipped"))

			svcReady := meta.FindStatusCondition(updated.Status.Conditions, "ServiceReady")
			Expect(svcReady).NotTo(BeNil())
			Expect(svcReady.Status).To(Equal(metav1.ConditionTrue))

			dashSvcReady := meta.FindStatusCondition(updated.Status.Conditions, "DashboardServiceReady")
			Expect(dashSvcReady).NotTo(BeNil())
			Expect(dashSvcReady.Status).To(Equal(metav1.ConditionFalse))
			Expect(dashSvcReady.Reason).To(Equal("Provisioning"))

			gwReady := meta.FindStatusCondition(updated.Status.Conditions, "GatewayReady")
			Expect(gwReady).NotTo(BeNil())
			Expect(gwReady.Status).To(Equal(metav1.ConditionFalse))

			routeReady := meta.FindStatusCondition(updated.Status.Conditions, "HTTPRouteReady")
			Expect(routeReady).NotTo(BeNil())
			Expect(routeReady.Status).To(Equal(metav1.ConditionFalse))

			stsReady := meta.FindStatusCondition(updated.Status.Conditions, "StatefulSetReady")
			Expect(stsReady).NotTo(BeNil())
			Expect(stsReady.Status).To(Equal(metav1.ConditionFalse))
			Expect(stsReady.Reason).To(Equal("Provisioning"))
		})

		It("applies security context overrides", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())

			backendPodUser := int64(1337)
			backendContainerUser := int64(1400)
			dashboardPodUser := int64(2001)
			updated.Spec.Backend.Security.Pod = &corev1.PodSecurityContext{
				RunAsUser: &backendPodUser,
			}
			updated.Spec.Backend.Security.Container = &corev1.SecurityContext{
				RunAsUser:    &backendContainerUser,
				RunAsNonRoot: ptr.To(false),
			}
			updated.Spec.Dashboard.Security.Pod = &corev1.PodSecurityContext{
				RunAsUser:    &dashboardPodUser,
				RunAsNonRoot: ptr.To(false),
			}
			updated.Spec.Dashboard.Security.Container = &corev1.SecurityContext{
				RunAsNonRoot: ptr.To(false),
			}
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			Expect(ptr.Deref(sts.Spec.Template.Spec.SecurityContext.RunAsUser, int64(0))).To(Equal(backendPodUser))
			Expect(ptr.Deref(sts.Spec.Template.Spec.Containers[0].SecurityContext.RunAsUser, int64(0))).To(Equal(backendContainerUser))
			Expect(ptr.Deref(sts.Spec.Template.Spec.Containers[0].SecurityContext.RunAsNonRoot, true)).To(BeFalse())

			dashboardDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dashboardDep)).To(Succeed())
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.SecurityContext.RunAsUser, int64(0))).To(Equal(dashboardPodUser))
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.SecurityContext.RunAsNonRoot, true)).To(BeFalse())
			Expect(ptr.Deref(dashboardDep.Spec.Template.Spec.Containers[0].SecurityContext.RunAsNonRoot, true)).To(BeFalse())
		})

		It("should move to Ready once the StatefulSet reports ready replicas", func() {
			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			makeBackendReady()
			makeDashboardReady()
			makeGatewayReady()
			makeRouteAccepted()

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Ready"))
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal("BackendReady"))
			Expect(readyCond.Message).To(Equal("Backend ready"))

			stsCond := meta.FindStatusCondition(updated.Status.Conditions, "StatefulSetReady")
			Expect(stsCond).NotTo(BeNil())
			Expect(stsCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(stsCond.Reason).To(Equal("Ready"))

			gwCond := meta.FindStatusCondition(updated.Status.Conditions, "GatewayReady")
			Expect(gwCond).NotTo(BeNil())
			Expect(gwCond.Status).To(Equal(metav1.ConditionTrue))

			routeCond := meta.FindStatusCondition(updated.Status.Conditions, "HTTPRouteReady")
			Expect(routeCond).NotTo(BeNil())
			Expect(routeCond.Status).To(Equal(metav1.ConditionTrue))

			Expect(updated.Status.UpgradeHash).NotTo(BeEmpty())
			upgradeCond := meta.FindStatusCondition(updated.Status.Conditions, "UpgradeInProgress")
			Expect(upgradeCond).NotTo(BeNil())
			Expect(upgradeCond.Status).To(Equal(metav1.ConditionFalse))

			Expect(updated.Status.Endpoints.APIURL).To(Equal("http://convex-dev.example.com"))
			Expect(updated.Status.Endpoints.DashboardURL).To(Equal("http://convex-dev.example.com/dashboard"))

			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Normal Ready Backend is ready")))
		})

		It("triggers periodic restarts when the interval elapses", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			makeBackendReady()
			makeDashboardReady()
			makeGatewayReady()
			makeRouteAccepted()
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			current.Spec.Maintenance.RestartInterval = &metav1.Duration{Duration: time.Second}
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			oldTimestamp := time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339)
			if sts.Annotations == nil {
				sts.Annotations = map[string]string{}
			}
			sts.Annotations[lastRestartAnnotation] = oldTimestamp
			if sts.Spec.Template.Annotations == nil {
				sts.Spec.Template.Annotations = map[string]string{}
			}
			sts.Spec.Template.Annotations[restartTriggerAnnotation] = oldTimestamp
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(result.RequeueAfter).To(BeNumerically("<=", 3*time.Second))

			updatedSTS := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, updatedSTS)).To(Succeed())
			newTrigger := updatedSTS.Spec.Template.Annotations[restartTriggerAnnotation]
			Expect(newTrigger).NotTo(Equal(oldTimestamp))
			Expect(updatedSTS.Annotations[lastRestartAnnotation]).To(Equal(newTrigger))
		})

		It("should handle an in-place upgrade rollout", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			makeBackendReady()
			makeDashboardReady()
			makeGatewayReady()
			makeRouteAccepted()
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			oldHash := current.Status.UpgradeHash

			current.Spec.Version = testVersionV2
			current.Spec.Backend.Image = testBackendImageV2
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pending := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, pending)).To(Succeed())
			Expect(pending.Status.Phase).To(Equal("Upgrading"))
			upgradeCond := meta.FindStatusCondition(pending.Status.Conditions, "UpgradeInProgress")
			Expect(upgradeCond).NotTo(BeNil())
			Expect(upgradeCond.Status).To(Equal(metav1.ConditionTrue))

			makeBackendReady()
			makeDashboardReady()
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			final := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, final)).To(Succeed())
			Expect(final.Status.Phase).To(Equal("Ready"))
			Expect(final.Status.UpgradeHash).NotTo(Equal(oldHash))
			upgradeCond = meta.FindStatusCondition(final.Status.Conditions, "UpgradeInProgress")
			Expect(upgradeCond).NotTo(BeNil())
			Expect(upgradeCond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("keeps UpgradeInProgress true until the new rollout is ready", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			makeBackendReady()
			makeDashboardReady()
			makeGatewayReady()
			makeRouteAccepted()
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			oldHash := current.Status.UpgradeHash
			current.Spec.Version = testVersionV2
			current.Spec.Backend.Image = testBackendImageV2
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			sts.Spec.Template.Spec.Containers[0].Image = testBackendImageV2
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())
			sts.Status.ReadyReplicas = 0
			sts.Status.Replicas = 1
			sts.Status.UpdatedReplicas = 0
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pending := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, pending)).To(Succeed())
			Expect(pending.Status.Phase).To(Equal("Upgrading"))
			upgradeCond := meta.FindStatusCondition(pending.Status.Conditions, "UpgradeInProgress")
			Expect(upgradeCond).NotTo(BeNil())
			Expect(upgradeCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(pending.Status.UpgradeHash).To(Equal(oldHash))
		})

		It("should orchestrate export/import upgrades", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			makeBackendReady()
			makeDashboardReady()
			makeGatewayReady()
			makeRouteAccepted()
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			oldHash := current.Status.UpgradeHash

			current.Spec.Maintenance.UpgradeStrategy = strategyExportImport
			current.Spec.Backend.Image = testBackendImageV2
			current.Spec.Version = testVersionV2
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			exportJob := &batchv1.Job{}
			Eventually(func() bool {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-upgrade-export", Namespace: "default"}, exportJob) == nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			makeBackendReady()
			exportJob.Status.Succeeded = 1
			now := metav1.Now()
			exportJob.Status.StartTime = &now
			exportJob.Status.CompletionTime = &now
			exportJob.Status.Conditions = []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}, {
				Type:               condSuccessCriteriaMet,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}}
			Expect(k8sClient.Status().Update(ctx, exportJob)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			sts.Spec.Template.Spec.Containers[0].Image = testBackendImageV2
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())
			sts.Status.ReadyReplicas = 1
			sts.Status.Replicas = 1
			sts.Status.UpdatedReplicas = 1
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			Eventually(func() bool {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				return err == nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			importJob := &batchv1.Job{}
			Eventually(func() bool {
				_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-upgrade-import", Namespace: "default"}, importJob)
				return err == nil
			}, 15*time.Second, 200*time.Millisecond).Should(BeTrue())
			importJob.Status.Succeeded = 1
			now = metav1.Now()
			importJob.Status.StartTime = &now
			importJob.Status.CompletionTime = &now
			importJob.Status.Conditions = []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}, {
				Type:               condSuccessCriteriaMet,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}}
			Expect(k8sClient.Status().Update(ctx, importJob)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			final := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, final)).To(Succeed())
			Expect(final.Status.Phase).To(Equal("Ready"))
			Expect(final.Status.UpgradeHash).NotTo(Equal(oldHash))

			upgradeCond := meta.FindStatusCondition(final.Status.Conditions, "UpgradeInProgress")
			Expect(upgradeCond).NotTo(BeNil())
			Expect(upgradeCond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("does not mark upgrade in progress once export/import are done and backend becomes unready", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			makeBackendReady()
			makeDashboardReady()
			makeGatewayReady()
			makeRouteAccepted()
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			current := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, current)).To(Succeed())
			current.Spec.Maintenance.UpgradeStrategy = strategyExportImport
			current.Spec.Backend.Image = testBackendImageV2
			current.Spec.Version = testVersionV2
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			exportJob := &batchv1.Job{}
			Eventually(func() bool {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-upgrade-export", Namespace: "default"}, exportJob) == nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			makeBackendReady()
			now := metav1.Now()
			exportJob.Status.Succeeded = 1
			exportJob.Status.StartTime = &now
			exportJob.Status.CompletionTime = &now
			exportJob.Status.Conditions = []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}, {
				Type:               condSuccessCriteriaMet,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}}
			Expect(k8sClient.Status().Update(ctx, exportJob)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			sts.Spec.Template.Spec.Containers[0].Image = testBackendImageV2
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())
			sts.Status.ReadyReplicas = 1
			sts.Status.Replicas = 1
			sts.Status.UpdatedReplicas = 1
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			importJob := &batchv1.Job{}
			Eventually(func() bool {
				_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-upgrade-import", Namespace: "default"}, importJob)
				return err == nil
			}, 15*time.Second, 200*time.Millisecond).Should(BeTrue())
			now = metav1.Now()
			importJob.Status.Succeeded = 1
			importJob.Status.StartTime = &now
			importJob.Status.CompletionTime = &now
			importJob.Status.Conditions = []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}, {
				Type:               condSuccessCriteriaMet,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: now,
				LastProbeTime:      now,
			}}
			Expect(k8sClient.Status().Update(ctx, importJob)).To(Succeed())

			// Simulate backend becoming unready after import completion.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend", Namespace: "default"}, sts)).To(Succeed())
			sts.Status.ReadyReplicas = 0
			sts.Status.UpdatedReplicas = 0
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pending := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, pending)).To(Succeed())
			Expect(pending.Status.Phase).To(Equal("Pending"))
			upgradeCond := meta.FindStatusCondition(pending.Status.Conditions, "UpgradeInProgress")
			Expect(upgradeCond).NotTo(BeNil())
			Expect(upgradeCond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("should reconcile the dashboard deployment when enabled", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dep)).To(Succeed())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, svc)).To(Succeed())
			Expect(svc.Spec.Selector["app.kubernetes.io/component"]).To(Equal("dashboard"))

			Expect(dep.Spec.Replicas).NotTo(BeNil())
			Expect(*dep.Spec.Replicas).To(Equal(int32(1)))

			Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			container := dep.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal("ghcr.io/get-convex/convex-dashboard:latest"))

			envValues := map[string]string{}
			for i := range container.Env {
				env := container.Env[i]
				envValues[env.Name] = env.Value
			}
			Expect(envValues).To(HaveKeyWithValue("NEXT_PUBLIC_DEPLOYMENT_URL", "http://convex-dev.example.com"))
			Expect(envValues).To(HaveKeyWithValue("CONVEX_CLOUD_ORIGIN", "http://convex-dev.example.com"))
			Expect(envValues).To(HaveKeyWithValue("CONVEX_SITE_ORIGIN", "http://convex-dev.example.com"))
			for i := range container.Env {
				Expect(container.Env[i].Name).NotTo(Equal("NEXT_PUBLIC_ADMIN_KEY"))
			}
		})

		It("should allow overriding deployment URL/origins and optionally prefill admin key", func() {
			controllerReconciler, _ := newReconciler()

			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			patch := []byte(`{"spec":{"networking":{"deploymentUrl":"https://api.example.com","cloudOrigin":"https://cloud.example.com","siteOrigin":"https://app.example.com"},"dashboard":{"prefillAdminKey":true}}}`)
			Expect(k8sClient.Patch(ctx, instance, client.RawPatch(types.MergePatchType, patch))).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dep)).To(Succeed())
			container := dep.Spec.Template.Spec.Containers[0]

			envMap := map[string]corev1.EnvVar{}
			for i := range container.Env {
				env := container.Env[i]
				envMap[env.Name] = env
			}

			Expect(envMap).To(HaveKey("NEXT_PUBLIC_DEPLOYMENT_URL"))
			Expect(envMap["NEXT_PUBLIC_DEPLOYMENT_URL"].Value).To(Equal("https://api.example.com"))
			Expect(envMap).To(HaveKey("CONVEX_CLOUD_ORIGIN"))
			Expect(envMap["CONVEX_CLOUD_ORIGIN"].Value).To(Equal("https://cloud.example.com"))
			Expect(envMap).To(HaveKey("CONVEX_SITE_ORIGIN"))
			Expect(envMap["CONVEX_SITE_ORIGIN"].Value).To(Equal("https://app.example.com"))
			Expect(envMap).To(HaveKey("NEXT_PUBLIC_ADMIN_KEY"))
			adminEnv := envMap["NEXT_PUBLIC_ADMIN_KEY"]
			Expect(adminEnv.ValueFrom).NotTo(BeNil())
			Expect(adminEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(adminEnv.ValueFrom.SecretKeyRef.Name).To(Equal("test-resource-convex-secrets"))
			Expect(adminEnv.ValueFrom.SecretKeyRef.Key).To(Equal("adminKey"))
		})

		It("should create Gateway and HTTPRoute with expected wiring", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			gw := &gatewayv1.Gateway{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-gateway", Namespace: "default"}, gw)).To(Succeed())
			Expect(gw.Spec.GatewayClassName).To(Equal(gatewayv1.ObjectName("nginx")))
			Expect(gw.Spec.Listeners).To(HaveLen(1))
			Expect(gw.Spec.Listeners[0].Protocol).To(Equal(gatewayv1.HTTPProtocolType))
			Expect(gw.Spec.Listeners[0].Hostname).NotTo(BeNil())
			Expect(string(*gw.Spec.Listeners[0].Hostname)).To(Equal("convex-dev.example.com"))
			Expect(gw.Annotations).To(HaveKeyWithValue("cert-manager.io/cluster-issuer", "letsencrypt-prod-rfc2136"))

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-route", Namespace: "default"}, route)).To(Succeed())
			Expect(route.Spec.Hostnames).To(ContainElement(gatewayv1.Hostname("convex-dev.example.com")))
			Expect(route.Spec.ParentRefs).To(HaveLen(1))
			Expect(route.Spec.ParentRefs[0].Name).To(Equal(gatewayv1.ObjectName("test-resource-gateway")))

			Expect(route.Spec.Rules).To(HaveLen(3))
			dashboardRule := route.Spec.Rules[0]
			Expect(dashboardRule.BackendRefs).To(HaveLen(1))
			Expect(*dashboardRule.BackendRefs[0].BackendRef.Port).To(Equal(gatewayv1.PortNumber(6791)))
			Expect(dashboardRule.Matches).NotTo(BeEmpty())
			Expect(dashboardRule.Matches[0].Path).NotTo(BeNil())
			Expect(dashboardRule.Matches[0].Path.Value).NotTo(BeNil())
			Expect(*dashboardRule.Matches[0].Path.Value).To(Equal("/dashboard"))

			actionRule := route.Spec.Rules[1]
			Expect(actionRule.BackendRefs).To(HaveLen(1))
			Expect(*actionRule.BackendRefs[0].BackendRef.Port).To(Equal(gatewayv1.PortNumber(3211)))
			Expect(actionRule.Matches).NotTo(BeEmpty())
			Expect(actionRule.Matches[0].Path).NotTo(BeNil())
			Expect(actionRule.Matches[0].Path.Value).NotTo(BeNil())
			Expect(*actionRule.Matches[0].Path.Value).To(Equal("/http_action/"))
		})

		It("should skip Gateway reconciliation and attach HTTPRoute to provided parentRef", func() {
			controllerReconciler, _ := newReconciler()

			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			patch := []byte(`{"spec":{"networking":{"parentRefs":[{"name":"public","namespace":"nginx-gateway","sectionName":"web"}]}}}`)
			Expect(k8sClient.Patch(ctx, instance, client.RawPatch(types.MergePatchType, patch))).To(Succeed())
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(instance.Spec.Networking.ParentRefs).To(HaveLen(1))

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				gw := &gatewayv1.Gateway{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-gateway", Namespace: "default"}, gw)
				return errors.IsNotFound(err)
			}, 2*time.Second, 100*time.Millisecond).Should(BeTrue())

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-route", Namespace: "default"}, route)).To(Succeed())
			Expect(route.Spec.ParentRefs).To(HaveLen(1))
			parent := route.Spec.ParentRefs[0]
			Expect(parent.Name).To(Equal(gatewayv1.ObjectName("public")))
			Expect(parent.Namespace).NotTo(BeNil())
			Expect(string(*parent.Namespace)).To(Equal("nginx-gateway"))
			Expect(parent.SectionName).NotTo(BeNil())
			Expect(string(*parent.SectionName)).To(Equal("web"))

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			gwCond := meta.FindStatusCondition(updated.Status.Conditions, "GatewayReady")
			Expect(gwCond).NotTo(BeNil())
			Expect(gwCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(gwCond.Reason).To(Equal("Skipped"))
		})

		It("should honor custom gateway annotations and override defaults", func() {
			controllerReconciler, _ := newReconciler()

			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			patch := []byte(`{"spec":{"networking":{"gatewayAnnotations":{"cert-manager.io/cluster-issuer":"letsencrypt-staging-rfc2136","example.com/owner":"platform"}}}}`)
			Expect(k8sClient.Patch(ctx, instance, client.RawPatch(types.MergePatchType, patch))).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			gw := &gatewayv1.Gateway{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-gateway", Namespace: "default"}, gw)).To(Succeed())
			Expect(gw.Annotations).To(HaveKeyWithValue("cert-manager.io/cluster-issuer", "letsencrypt-staging-rfc2136"))
			Expect(gw.Annotations).To(HaveKeyWithValue("example.com/owner", "platform"))
		})

		It("should remove the dashboard deployment when disabled", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dep)).To(Succeed())

			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Dashboard.Enabled = false
			patch := []byte(`{"spec":{"dashboard":{"enabled":false}}}`)
			Expect(k8sClient.Patch(ctx, instance, client.RawPatch(types.MergePatchType, patch))).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				dep := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, dep)
				if errors.IsNotFound(err) {
					return true
				}
				if err != nil {
					return false
				}
				return dep.DeletionTimestamp != nil
			}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())

			Eventually(func() bool {
				svc := &corev1.Service{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-dashboard", Namespace: "default"}, svc)
				return errors.IsNotFound(err)
			}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-route", Namespace: "default"}, route)).To(Succeed())
			Expect(route.Spec.Rules).To(HaveLen(2))
			Expect(route.Spec.Rules[0].Matches).NotTo(BeEmpty())
			Expect(route.Spec.Rules[0].Matches[0].Path).NotTo(BeNil())
			Expect(route.Spec.Rules[0].Matches[0].Path.Value).NotTo(BeNil())
			Expect(*route.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/http_action/"))
			Expect(route.Spec.Rules[0].BackendRefs[0].BackendRef.Name).To(Equal(gatewayv1.ObjectName("test-resource-backend")))

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			dashCond := meta.FindStatusCondition(updated.Status.Conditions, "DashboardReady")
			Expect(dashCond).NotTo(BeNil())
			Expect(dashCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(dashCond.Reason).To(Equal("Disabled"))
		})

		It("should surface errors when referenced DB secrets are missing", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.DB.SecretRef = "missing-db-secret"
			instance.Spec.Backend.DB.URLKey = "url"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Error"))
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ValidationFailed"))
			Expect(readyCond.Message).To(ContainSubstring("missing-db-secret"))

			secretCond := meta.FindStatusCondition(updated.Status.Conditions, "SecretsReady")
			Expect(secretCond).NotTo(BeNil())
			Expect(secretCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(secretCond.Reason).To(Equal("ValidationFailed"))

			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Warning ValidationFailed")))
		})

		It("should fail validation when DB secret is missing the urlKey", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.DB.Engine = dbEnginePostgres
			instance.Spec.Backend.DB.SecretRef = "pg-secret-missing-key"
			instance.Spec.Backend.DB.URLKey = "url"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pg-secret-missing-key",
					Namespace: typeNamespacedName.Namespace,
				},
				Data: map[string][]byte{},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, secret)
			})

			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ValidationFailed"))
			Expect(readyCond.Message).To(ContainSubstring("missing key \"url\""))
			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Warning ValidationFailed")))
		})

		It("should fail validation when DB engine is postgres and urlKey is missing", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.DB.Engine = dbEnginePostgres
			instance.Spec.Backend.DB.SecretRef = "pg-secret"
			instance.Spec.Backend.DB.URLKey = ""
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ValidationFailed"))
			Expect(readyCond.Message).To(ContainSubstring("db urlKey is required"))
			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Warning ValidationFailed")))
		})

		It("should fail validation when S3 is enabled without required keys", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.S3.Enabled = true
			instance.Spec.Backend.S3.SecretRef = "s3-secret"
			instance.Spec.Backend.S3.EndpointKey = ""
			instance.Spec.Backend.S3.RegionKey = "region"
			instance.Spec.Backend.S3.AccessKeyIDKey = "accessKeyId"
			instance.Spec.Backend.S3.SecretAccessKeyKey = "secretAccessKey"
			instance.Spec.Backend.S3.BucketKey = "bucket"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-secret",
					Namespace: typeNamespacedName.Namespace,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, secret)
			})

			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ValidationFailed"))
			Expect(readyCond.Message).To(ContainSubstring("s3"))
			Expect(readyCond.Message).To(ContainSubstring("required"))
			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Warning ValidationFailed")))
		})

		It("should fail validation when S3 secret is missing required key data", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.S3.Enabled = true
			instance.Spec.Backend.S3.SecretRef = "s3-secret-missing-key"
			instance.Spec.Backend.S3.EndpointKey = "endpoint"
			instance.Spec.Backend.S3.RegionKey = "region"
			instance.Spec.Backend.S3.AccessKeyIDKey = "accessKeyId"
			instance.Spec.Backend.S3.SecretAccessKeyKey = "secretAccessKey"
			instance.Spec.Backend.S3.BucketKey = "bucket"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-secret-missing-key",
					Namespace: typeNamespacedName.Namespace,
				},
				Data: map[string][]byte{
					"endpoint":        []byte("https://s3.example.com"),
					"region":          []byte("us-east-1"),
					"accessKeyId":     []byte("id"),
					"secretAccessKey": []byte("secret"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, secret)
			})

			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ValidationFailed"))
			Expect(readyCond.Message).To(ContainSubstring("missing key \"bucket\""))
			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Warning ValidationFailed")))
		})

		It("should fail validation when S3 configmap is missing required key data", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.S3.Enabled = true
			instance.Spec.Backend.S3.SecretRef = "s3-secret"
			instance.Spec.Backend.S3.ConfigMapRef = "s3-config"
			instance.Spec.Backend.S3.EndpointHostKey = "BUCKET_HOST"
			instance.Spec.Backend.S3.EndpointPortKey = "BUCKET_PORT"
			instance.Spec.Backend.S3.RegionKey = "BUCKET_REGION"
			instance.Spec.Backend.S3.AccessKeyIDKey = "AWS_ACCESS_KEY_ID"
			instance.Spec.Backend.S3.SecretAccessKeyKey = "AWS_SECRET_ACCESS_KEY"
			instance.Spec.Backend.S3.BucketKey = "BUCKET_NAME"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-secret",
					Namespace: typeNamespacedName.Namespace,
				},
				Data: map[string][]byte{
					"AWS_ACCESS_KEY_ID":     []byte("id"),
					"AWS_SECRET_ACCESS_KEY": []byte("secret"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, secret)
			})

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-config",
					Namespace: typeNamespacedName.Namespace,
				},
				Data: map[string]string{
					"BUCKET_HOST":   "rook-ceph-rgw-s3.rook-ceph.svc",
					"BUCKET_PORT":   "80",
					"BUCKET_REGION": "",
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, cm)
			})

			controllerReconciler, recorder := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("ValidationFailed"))
			Expect(readyCond.Message).To(ContainSubstring("configmap"))
			Expect(readyCond.Message).To(ContainSubstring("missing key \"BUCKET_NAME\""))
			Eventually(recorder.Events, 2*time.Second, 100*time.Millisecond).Should(Receive(ContainSubstring("Warning ValidationFailed")))
		})

		It("should create a PVC when storage is enabled", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.Storage.PVC.Enabled = true
			instance.Spec.Backend.Storage.PVC.Size = resource.MustParse("20Gi")
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend-pvc", Namespace: "default"}, pvc)).To(Succeed())
			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("20Gi")))

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			pvcCond := meta.FindStatusCondition(updated.Status.Conditions, "PVCReady")
			Expect(pvcCond).NotTo(BeNil())
			Expect(pvcCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(pvcCond.Reason).To(Equal("Available"))
		})

		It("sets storageClassName on the upgrade PVC during export/import upgrades", func() {
			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Maintenance.UpgradeStrategy = strategyExportImport
			instance.Spec.Backend.Storage.PVC.StorageClassName = "fast"
			instance.Spec.Backend.Image = testBackendImageV2
			instance.Spec.Version = testVersionV2
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-upgrade-pvc", Namespace: "default"}, pvc)).To(Succeed())
			Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
			Expect(*pvc.Spec.StorageClassName).To(Equal("fast"))
		})

		It("should surface an error when storageClassName changes after PVC creation", func() {
			instance := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.Storage.PVC.Enabled = true
			instance.Spec.Backend.Storage.PVC.Size = resource.MustParse("20Gi")
			instance.Spec.Backend.Storage.PVC.StorageClassName = "standard"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			controllerReconciler, _ := newReconciler()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-resource-backend-pvc", Namespace: "default"}, pvc)).To(Succeed())
			Expect(ptr.Deref(pvc.Spec.StorageClassName, "")).To(Equal("standard"))
			Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("20Gi")))

			// Change storageClassName and expect reconciliation to fail due to immutability.
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Backend.Storage.PVC.StorageClassName = "fast"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &convexv1alpha1.ConvexInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("PVCError"))
			Expect(readyCond.Message).To(ContainSubstring("storageClassName is immutable"))
		})
	})
})

var _ = Describe("upgrade plan helpers", func() {
	ctx := context.Background()
	newReconciler := func() *ConvexInstanceReconciler {
		return &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	It("propagates export/import failures into the plan", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name: "failure-propagation",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Version: "1.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:1.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
				},
				Dashboard: convexv1alpha1.DashboardSpec{
					Enabled: true,
					Image:   "ghcr.io/get-convex/convex-dashboard:1.0.0",
				},
				Maintenance: convexv1alpha1.MaintenanceSpec{
					UpgradeStrategy: upgradeStrategyExport,
				},
			},
		}
		desiredEnv := backendEnv(instance, instance.Spec.Version, generatedSecretName(instance))
		desiredHash := desiredUpgradeHash(instance, desiredEnv)
		instance.Status.UpgradeHash = desiredHash

		currentEnv := backendEnv(instance, instance.Spec.Version, generatedSecretName(instance))
		plan := buildUpgradePlan(instance, true, instance.Spec.Backend.Image, instance.Spec.Dashboard.Image, instance.Spec.Version, currentEnv, false, false, true, true, desiredHash)

		Expect(plan.exportFailed).To(BeTrue())
		Expect(plan.importFailed).To(BeTrue())
		Expect(plan.upgradePlanned).To(BeFalse())
		Expect(plan.upgradePending).To(BeFalse())
	})

	It("scopes job failures to the desired upgrade hash", func() {
		controllerReconciler := newReconciler()
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "hash-scope",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "dev",
				Version:     "1.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:1.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "hash-scope.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		desiredHash := "desired-hash"
		existing := &batchv1.Job{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: exportJobName(instance), Namespace: instance.Namespace}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      exportJobName(instance),
				Namespace: instance.Namespace,
				Annotations: map[string]string{
					upgradeHashAnnotation: "old-hash",
				},
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{{
							Name:  "noop",
							Image: "busybox",
						}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, job)
		})

		job.Status.Conditions = []batchv1.JobCondition{{
			Type:               batchv1.JobFailed,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			LastProbeTime:      metav1.Now(),
		}, {
			Type:               condFailureTarget,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			LastProbeTime:      metav1.Now(),
		}}
		now := metav1.Now()
		job.Status.StartTime = &now
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		_, _, exportFailed, _ := controllerReconciler.observeUpgradeJobs(ctx, instance, desiredHash)
		Expect(exportFailed).To(BeFalse())

		job.Annotations[upgradeHashAnnotation] = desiredHash
		Expect(k8sClient.Update(ctx, job)).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		_, _, exportFailed, _ = controllerReconciler.observeUpgradeJobs(ctx, instance, desiredHash)
		Expect(exportFailed).To(BeTrue())
	})

	It("derives applied upgrade hash from observed images even when status is set", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "hash-drift",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Version: "2.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:2.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
				},
				Dashboard: convexv1alpha1.DashboardSpec{
					Image: "ghcr.io/get-convex/convex-dashboard:2.0.0",
				},
			},
		}
		desiredEnv := backendEnv(instance, instance.Spec.Version, generatedSecretName(instance))
		desiredHash := desiredUpgradeHash(instance, desiredEnv)
		instance.Status.UpgradeHash = desiredHash

		currentBackendImage := "ghcr.io/get-convex/convex-backend:1.0.0"
		currentDashboardImage := "ghcr.io/get-convex/convex-dashboard:1.0.0"
		currentBackendVersion := "1.0.0"
		currentEnv := backendEnv(instance, currentBackendVersion, generatedSecretName(instance))

		appliedHash := observedUpgradeHash(instance, currentBackendVersion, currentBackendImage, currentDashboardImage, currentEnv)
		Expect(appliedHash).To(Equal(configHash(currentBackendVersion, currentBackendImage, currentDashboardImage, envSignature(currentEnv))))
		Expect(appliedHash).NotTo(Equal(desiredHash))

		plan := buildUpgradePlan(instance, true, currentBackendImage, currentDashboardImage, currentBackendVersion, currentEnv, false, false, false, false, desiredHash)
		Expect(plan.upgradePlanned).To(BeTrue())
		Expect(plan.upgradePending).To(BeTrue())
		Expect(plan.currentBackendImage).To(Equal(currentBackendImage))
	})

	It("does not flag upgrades when the dashboard is disabled", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "no-dashboard",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Version: "1.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:1.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
				},
				Dashboard: convexv1alpha1.DashboardSpec{
					Enabled: false,
					Image:   "ghcr.io/get-convex/convex-dashboard:1.0.0",
				},
			},
		}

		desiredEnv := backendEnv(instance, instance.Spec.Version, generatedSecretName(instance))
		desiredHash := desiredUpgradeHash(instance, desiredEnv)
		currentEnv := backendEnv(instance, instance.Spec.Version, generatedSecretName(instance))
		appliedHash := observedUpgradeHash(instance, instance.Spec.Version, instance.Spec.Backend.Image, "", currentEnv)
		Expect(desiredHash).To(Equal(appliedHash))

		plan := buildUpgradePlan(instance, true, instance.Spec.Backend.Image, "", instance.Spec.Version, currentEnv, false, false, false, false, desiredHash)
		Expect(plan.upgradePending).To(BeFalse())
		Expect(plan.upgradePlanned).To(BeFalse())
	})
})

var _ = Describe("config and secret helpers", func() {
	ctx := context.Background()

	It("renders backend env vars including DB and S3 secrets", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "env-vars",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:    dbEnginePostgres,
						SecretRef: "db-secret",
						URLKey:    "url",
					},
					S3: convexv1alpha1.BackendS3Spec{
						Enabled:            true,
						SecretRef:          "s3-secret",
						EndpointKey:        "endpoint",
						RegionKey:          "region",
						AccessKeyIDKey:     "accessKey",
						SecretAccessKeyKey: "secretAccessKey",
						BucketKey:          "bucket",
						EmitS3EndpointUrl:  true,
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "env-vars.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{
			dbResourceVersion: "db-rv",
			s3ResourceVersion: "s3-rv",
		})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("CONVEX_PORT")).NotTo(BeNil())
		Expect(getEnv("CONVEX_PORT").Value).To(Equal(fmt.Sprintf("%d", defaultBackendPort)))
		Expect(getEnv("CONVEX_ENV")).NotTo(BeNil())
		Expect(getEnv("CONVEX_ENV").Value).To(Equal("prod"))
		Expect(getEnv("CONVEX_VERSION")).NotTo(BeNil())
		Expect(getEnv("CONVEX_VERSION").Value).To(Equal("9.9.9"))
		Expect(getEnv("INSTANCE_NAME")).NotTo(BeNil())
		Expect(getEnv("INSTANCE_NAME").Value).To(Equal("env_vars"))

		adminKeyEnv := getEnv("CONVEX_ADMIN_KEY")
		Expect(adminKeyEnv).NotTo(BeNil())
		Expect(adminKeyEnv.ValueFrom).NotTo(BeNil())
		Expect(adminKeyEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(adminKeyEnv.ValueFrom.SecretKeyRef.Name).To(Equal(generatedSecretName(instance)))
		Expect(adminKeyEnv.ValueFrom.SecretKeyRef.Key).To(Equal(adminKeyKey))

		instanceSecretEnv := getEnv("CONVEX_INSTANCE_SECRET")
		Expect(instanceSecretEnv).NotTo(BeNil())
		Expect(instanceSecretEnv.ValueFrom).NotTo(BeNil())
		Expect(instanceSecretEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(instanceSecretEnv.ValueFrom.SecretKeyRef.Name).To(Equal(generatedSecretName(instance)))
		Expect(instanceSecretEnv.ValueFrom.SecretKeyRef.Key).To(Equal(instanceSecretKey))

		dbEnv := getEnv("POSTGRES_URL")
		Expect(dbEnv).NotTo(BeNil())
		Expect(dbEnv.ValueFrom).NotTo(BeNil())
		Expect(dbEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(dbEnv.ValueFrom.SecretKeyRef.Name).To(Equal("db-secret"))
		Expect(dbEnv.ValueFrom.SecretKeyRef.Key).To(Equal("url"))
		Expect(getEnv("DO_NOT_REQUIRE_SSL")).To(BeNil())

		Expect(getEnv("AWS_REGION")).NotTo(BeNil())
		Expect(getEnv("AWS_REGION").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_REGION").ValueFrom.SecretKeyRef.Key).To(Equal("region"))
		Expect(getEnv("AWS_ACCESS_KEY_ID")).NotTo(BeNil())
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Key).To(Equal("accessKey"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY")).NotTo(BeNil())
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Key).To(Equal("secretAccessKey"))
		Expect(getEnv("AWS_ENDPOINT_URL_S3")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL_S3").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ENDPOINT_URL_S3").ValueFrom.SecretKeyRef.Key).To(Equal("endpoint"))
		Expect(getEnv("AWS_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ENDPOINT_URL").ValueFrom.SecretKeyRef.Key).To(Equal("endpoint"))
		Expect(getEnv("S3_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("S3_ENDPOINT_URL").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_ENDPOINT_URL").ValueFrom.SecretKeyRef.Key).To(Equal("endpoint"))
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
	})

	It("renders backend env vars using S3 metadata from a ConfigMap", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "env-vars-configmap",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:    dbEnginePostgres,
						SecretRef: "db-secret",
						URLKey:    "url",
					},
					S3: convexv1alpha1.BackendS3Spec{
						Enabled:            true,
						SecretRef:          "s3-secret",
						ConfigMapRef:       "s3-config",
						EndpointHostKey:    "BUCKET_HOST",
						EndpointPortKey:    "BUCKET_PORT",
						RegionKey:          "BUCKET_REGION",
						AccessKeyIDKey:     "AWS_ACCESS_KEY_ID",
						SecretAccessKeyKey: "AWS_SECRET_ACCESS_KEY",
						BucketKey:          "BUCKET_NAME",
						EmitS3EndpointUrl:  true,
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "env-vars-configmap.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{
			dbResourceVersion: "db-rv",
			s3ResourceVersion: "s3-rv",
		})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("AWS_ACCESS_KEY_ID")).NotTo(BeNil())
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Key).To(Equal("AWS_ACCESS_KEY_ID"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY")).NotTo(BeNil())
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Key).To(Equal("AWS_SECRET_ACCESS_KEY"))

		Expect(getEnv("AWS_REGION")).NotTo(BeNil())
		Expect(getEnv("AWS_REGION").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("AWS_REGION").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_REGION"))

		Expect(getEnv("CONVEX_S3_ENDPOINT_HOST")).NotTo(BeNil())
		Expect(getEnv("CONVEX_S3_ENDPOINT_HOST").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("CONVEX_S3_ENDPOINT_HOST").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_HOST"))
		Expect(getEnv("CONVEX_S3_ENDPOINT_PORT")).NotTo(BeNil())
		Expect(getEnv("CONVEX_S3_ENDPOINT_PORT").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("CONVEX_S3_ENDPOINT_PORT").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_PORT"))

		Expect(getEnv("AWS_ENDPOINT_URL_S3")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL_S3").ValueFrom).To(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL_S3").Value).To(Equal("http://$(CONVEX_S3_ENDPOINT_HOST):$(CONVEX_S3_ENDPOINT_PORT)"))
		Expect(getEnv("AWS_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL").ValueFrom).To(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL").Value).To(Equal("http://$(CONVEX_S3_ENDPOINT_HOST):$(CONVEX_S3_ENDPOINT_PORT)"))
		Expect(getEnv("S3_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("S3_ENDPOINT_URL").ValueFrom).To(BeNil())
		Expect(getEnv("S3_ENDPOINT_URL").Value).To(Equal("http://$(CONVEX_S3_ENDPOINT_HOST):$(CONVEX_S3_ENDPOINT_PORT)"))

		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_NAME"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_NAME"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_NAME"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_NAME"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").ValueFrom.ConfigMapKeyRef.Name).To(Equal("s3-config"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").ValueFrom.ConfigMapKeyRef.Key).To(Equal("BUCKET_NAME"))
	})

	It("renders backend env vars using inline S3 metadata", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "env-vars-inline",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:    dbEnginePostgres,
						SecretRef: "db-secret",
						URLKey:    "url",
					},
					S3: convexv1alpha1.BackendS3Spec{
						Enabled:            true,
						SecretRef:          "s3-secret",
						AccessKeyIDKey:     "AWS_ACCESS_KEY_ID",
						SecretAccessKeyKey: "AWS_SECRET_ACCESS_KEY",
						Bucket:             "inline-bucket",
						Region:             "us-east-1",
						Endpoint:           "https://s3.us-east-1.amazonaws.com",
						EmitS3EndpointUrl:  true,
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "env-vars-inline.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{
			dbResourceVersion: "db-rv",
			s3ResourceVersion: "s3-rv",
		})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("AWS_ACCESS_KEY_ID")).NotTo(BeNil())
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Key).To(Equal("AWS_ACCESS_KEY_ID"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY")).NotTo(BeNil())
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Key).To(Equal("AWS_SECRET_ACCESS_KEY"))

		Expect(getEnv("AWS_REGION")).NotTo(BeNil())
		Expect(getEnv("AWS_REGION").Value).To(Equal("us-east-1"))
		Expect(getEnv("AWS_REGION").ValueFrom).To(BeNil())

		Expect(getEnv("AWS_ENDPOINT_URL_S3")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL_S3").Value).To(Equal("https://s3.us-east-1.amazonaws.com"))
		Expect(getEnv("AWS_ENDPOINT_URL_S3").ValueFrom).To(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL").Value).To(Equal("https://s3.us-east-1.amazonaws.com"))
		Expect(getEnv("AWS_ENDPOINT_URL").ValueFrom).To(BeNil())
		Expect(getEnv("S3_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("S3_ENDPOINT_URL").Value).To(Equal("https://s3.us-east-1.amazonaws.com"))
		Expect(getEnv("S3_ENDPOINT_URL").ValueFrom).To(BeNil())

		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").Value).To(Equal("inline-bucket"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").Value).To(Equal("inline-bucket"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").Value).To(Equal("inline-bucket"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").Value).To(Equal("inline-bucket"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").Value).To(Equal("inline-bucket"))
	})

	It("auto-detects OBC configmap defaults for S3 keys", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "obc-secret",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "objectbucket.io/v1alpha1",
					Kind:       "ObjectBucketClaim",
					Name:       "obc-secret",
					UID:        types.UID("obc-secret-uid"),
				}},
			},
			Data: map[string][]byte{
				"AWS_ACCESS_KEY_ID":     []byte("id"),
				"AWS_SECRET_ACCESS_KEY": []byte("secret"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, secret)
		})

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "obc-secret",
				Namespace: "default",
			},
			Data: map[string]string{
				"BUCKET_NAME":   "bucket-name",
				"BUCKET_HOST":   "rook-ceph-rgw-s3.rook-ceph.svc",
				"BUCKET_PORT":   "80",
				"BUCKET_REGION": "us-east-1",
			},
		}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, cm)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		s3Spec := convexv1alpha1.BackendS3Spec{
			Enabled:       true,
			SecretRef:     "obc-secret",
			AutoDetectOBC: ptr.To(true),
		}
		resolved, _, err := reconciler.validateS3Ref(ctx, "default", s3Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.ConfigMapRef).To(Equal("obc-secret"))
		Expect(resolved.AccessKeyIDKey).To(Equal("AWS_ACCESS_KEY_ID"))
		Expect(resolved.SecretAccessKeyKey).To(Equal("AWS_SECRET_ACCESS_KEY"))
		Expect(resolved.BucketKey).To(Equal("BUCKET_NAME"))
		Expect(resolved.RegionKey).To(Equal("BUCKET_REGION"))
		Expect(resolved.EndpointHostKey).To(Equal("BUCKET_HOST"))
		Expect(resolved.EndpointPortKey).To(Equal("BUCKET_PORT"))
	})

	It("defaults empty OBC region to us-east-1", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "obc-empty-region",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "objectbucket.io/v1alpha1",
					Kind:       "ObjectBucketClaim",
					Name:       "obc-empty-region",
					UID:        types.UID("obc-empty-region-uid"),
				}},
			},
			Data: map[string][]byte{
				"AWS_ACCESS_KEY_ID":     []byte("id"),
				"AWS_SECRET_ACCESS_KEY": []byte("secret"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, secret)
		})

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "obc-empty-region",
				Namespace: "default",
			},
			Data: map[string]string{
				"BUCKET_NAME":   "bucket-name",
				"BUCKET_HOST":   "rook-ceph-rgw-s3.rook-ceph.svc",
				"BUCKET_PORT":   "80",
				"BUCKET_REGION": "",
			},
		}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, cm)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		s3Spec := convexv1alpha1.BackendS3Spec{
			Enabled:       true,
			SecretRef:     "obc-empty-region",
			AutoDetectOBC: ptr.To(true),
		}
		resolved, _, err := reconciler.validateS3Ref(ctx, "default", s3Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Region).To(Equal(defaultOBCRegion))
		Expect(resolved.RegionKey).To(Equal("BUCKET_REGION"))
	})

	It("accepts inline S3 metadata values without keys", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "inline-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"AWS_ACCESS_KEY_ID":     []byte("id"),
				"AWS_SECRET_ACCESS_KEY": []byte("secret"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, secret)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		s3Spec := convexv1alpha1.BackendS3Spec{
			Enabled:            true,
			SecretRef:          "inline-secret",
			AccessKeyIDKey:     "AWS_ACCESS_KEY_ID",
			SecretAccessKeyKey: "AWS_SECRET_ACCESS_KEY",
			Bucket:             "inline-bucket",
			Region:             "us-east-1",
			Endpoint:           "https://s3.example.com",
		}
		resolved, _, err := reconciler.validateS3Ref(ctx, "default", s3Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Bucket).To(Equal("inline-bucket"))
		Expect(resolved.Region).To(Equal("us-east-1"))
		Expect(resolved.Endpoint).To(Equal("https://s3.example.com"))
	})

	It("sets DO_NOT_REQUIRE_SSL when db.requireSSL is false", func() {
		requireSSL := false
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "env-vars-no-db-ssl",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:     "postgres",
						SecretRef:  "db-secret",
						URLKey:     "url",
						RequireSSL: &requireSSL,
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "env-vars-no-db-ssl.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{
			dbResourceVersion: "db-rv",
		})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("DO_NOT_REQUIRE_SSL")).NotTo(BeNil())
		Expect(getEnv("DO_NOT_REQUIRE_SSL").Value).To(Equal("1"))
	})

	It("sets MYSQL_URL when mysql engine is configured", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "env-vars-mysql",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:    "mysql",
						SecretRef: "mysql-secret",
						URLKey:    "url",
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "env-vars-mysql.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{
			dbResourceVersion: "db-rv",
		})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		dbEnv := getEnv("MYSQL_URL")
		Expect(dbEnv).NotTo(BeNil())
		Expect(dbEnv.ValueFrom).NotTo(BeNil())
		Expect(dbEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(dbEnv.ValueFrom.SecretKeyRef.Name).To(Equal("mysql-secret"))
		Expect(dbEnv.ValueFrom.SecretKeyRef.Key).To(Equal("url"))
		Expect(getEnv("POSTGRES_URL")).To(BeNil())
	})

	It("renders telemetry/logging envs and merges custom env overrides", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "custom-envs",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
					Telemetry: convexv1alpha1.BackendTelemetrySpec{
						DisableBeacon: true,
					},
					Logging: convexv1alpha1.BackendLoggingSpec{
						RedactLogsToClient: true,
					},
					Env: []corev1.EnvVar{
						{Name: "CUSTOM_FLAG", Value: "on"},
						{Name: "INSTANCE_NAME", Value: "override_name"},
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "custom-envs.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("DISABLE_BEACON")).NotTo(BeNil())
		Expect(getEnv("DISABLE_BEACON").Value).To(Equal("true"))
		Expect(getEnv("REDACT_LOGS_TO_CLIENT")).NotTo(BeNil())
		Expect(getEnv("REDACT_LOGS_TO_CLIENT").Value).To(Equal("true"))
		Expect(getEnv("CUSTOM_FLAG")).NotTo(BeNil())
		Expect(getEnv("CUSTOM_FLAG").Value).To(Equal("on"))
		// User-supplied env should override the operator-provided INSTANCE_NAME.
		Expect(getEnv("INSTANCE_NAME")).NotTo(BeNil())
		Expect(getEnv("INSTANCE_NAME").Value).To(Equal("override_name"))
	})

	It("allows overriding INSTANCE_NAME via backend.db.databaseName", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "custom-name",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:       "sqlite",
						DatabaseName: "override_db",
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "custom-name.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("INSTANCE_NAME")).NotTo(BeNil())
		Expect(getEnv("INSTANCE_NAME").Value).To(Equal("override_db"))
	})
})

var _ = Describe("envtest lifecycle suites", func() {
	ctx := context.Background()
	newReconciler := func() *ConvexInstanceReconciler {
		return &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	makeBackendReady := func(name string) {
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-backend", name), Namespace: "default"}, sts)).To(Succeed())
		sts.Status.ReadyReplicas = 1
		sts.Status.Replicas = 1
		sts.Status.UpdatedReplicas = 1
		sts.Status.ObservedGeneration = sts.Generation
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}
	makeDashboardReady := func(name string) {
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-dashboard", name), Namespace: "default"}, dep)).To(Succeed())
		dep.Status.Replicas = 1
		dep.Status.UpdatedReplicas = 1
		dep.Status.ReadyReplicas = 1
		dep.Status.ObservedGeneration = dep.Generation
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
	}
	makeGatewayReady := func(name string) {
		gw := &gatewayv1.Gateway{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-gateway", name), Namespace: "default"}, gw)).To(Succeed())
		gw.Status.Conditions = []metav1.Condition{{
			Type:               string(gatewayv1.GatewayConditionReady),
			Status:             metav1.ConditionTrue,
			Reason:             "Ready",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: gw.GetGeneration(),
		}}
		Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())
	}
	makeRouteAccepted := func(name string) {
		route := &gatewayv1.HTTPRoute{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-route", name), Namespace: "default"}, route)).To(Succeed())
		route.Status.Parents = []gatewayv1.RouteParentStatus{{
			ParentRef: gatewayv1.ParentReference{
				Name: gatewayv1.ObjectName(fmt.Sprintf("%s-gateway", name)),
			},
			ControllerName: gatewayv1.GatewayController("test.example/controller"),
			Conditions: []metav1.Condition{{
				Type:               string(gatewayv1.RouteConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Attached to listener",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: route.GetGeneration(),
			}},
		}}
		Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
	}

	It("handles create/update/delete happy paths", func() {
		name := types.NamespacedName{Name: "lifecycle-env", Namespace: "default"}
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name.Name,
				Namespace: name.Namespace,
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "dev",
				Version:     "1.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:1.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "lifecycle.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := newReconciler()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "lifecycle-env-backend", Namespace: "default"}, sts)).To(Succeed())

		// Trigger an upgrade by bumping version and image.
		Expect(k8sClient.Get(ctx, name, instance)).To(Succeed())
		instance.Spec.Version = "1.1.0"
		instance.Spec.Backend.Image = "ghcr.io/get-convex/convex-backend:1.1.0"
		Expect(k8sClient.Update(ctx, instance)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		updated := &convexv1alpha1.ConvexInstance{}
		Expect(k8sClient.Get(ctx, name, updated)).To(Succeed())
		upgradeCond := meta.FindStatusCondition(updated.Status.Conditions, "UpgradeInProgress")
		Expect(upgradeCond).NotTo(BeNil())

		// Delete the instance and ensure owned resources are cleaned up.
		Expect(k8sClient.Delete(ctx, instance)).To(Succeed())
		Eventually(func() bool {
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
			err := k8sClient.Get(ctx, name, &convexv1alpha1.ConvexInstance{})
			if errors.IsNotFound(err) {
				return true
			}
			return err == nil
		}, 20*time.Second, 200*time.Millisecond).Should(BeTrue())
	})

	It("surfaces missing secret validation failures", func() {
		name := types.NamespacedName{Name: "missing-secret", Namespace: "default"}
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name.Name,
				Namespace: name.Namespace,
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "dev",
				Version:     "1.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:1.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:    dbEnginePostgres,
						SecretRef: "absent-secret",
						URLKey:    "url",
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "missing.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := newReconciler()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).To(HaveOccurred())

		updated := &convexv1alpha1.ConvexInstance{}
		Expect(k8sClient.Get(ctx, name, updated)).To(Succeed())
		readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Reason).To(Equal("ValidationFailed"))
	})

	It("covers export/import upgrade flow with mocked jobs", func() {
		name := types.NamespacedName{Name: "mock-upgrade", Namespace: "default"}
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name.Name,
				Namespace: name.Namespace,
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "dev",
				Version:     "1.0.0",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:1.0.0",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine: "sqlite",
					},
				},
				Maintenance: convexv1alpha1.MaintenanceSpec{
					UpgradeStrategy: upgradeStrategyExport,
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "mock-upgrade.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := newReconciler()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		makeBackendReady(name.Name)
		makeDashboardReady(name.Name)
		makeGatewayReady(name.Name)
		makeRouteAccepted(name.Name)
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		// Trigger export/import upgrade.
		Expect(k8sClient.Get(ctx, name, instance)).To(Succeed())
		instance.Spec.Backend.Image = "ghcr.io/get-convex/convex-backend:2.0.0"
		instance.Spec.Version = "2.0.0"
		Expect(k8sClient.Update(ctx, instance)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		desiredEnv := backendEnv(instance, instance.Spec.Version, generatedSecretName(instance))
		desiredHash := desiredUpgradeHash(instance, desiredEnv)

		exportJob := &batchv1.Job{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "mock-upgrade-upgrade-export", Namespace: "default"}, exportJob); errors.IsNotFound(err) {
			exportJob = &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mock-upgrade-upgrade-export",
					Namespace: "default",
					Annotations: map[string]string{
						upgradeHashAnnotation: desiredHash,
					},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{{
								Name:  "export",
								Image: "busybox",
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, exportJob)).To(Succeed())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
		now := metav1.Now()
		exportJob.Status.Succeeded = 1
		exportJob.Status.StartTime = &now
		exportJob.Status.CompletionTime = &now
		exportJob.Status.Conditions = []batchv1.JobCondition{{
			Type:               batchv1.JobComplete,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			LastProbeTime:      now,
		}, {
			Type:               condSuccessCriteriaMet,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			LastProbeTime:      now,
		}}
		Expect(k8sClient.Status().Update(ctx, exportJob)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "mock-upgrade-backend", Namespace: "default"}, sts)).To(Succeed())
		sts.Status.ReadyReplicas = 1
		sts.Status.Replicas = 1
		sts.Status.UpdatedReplicas = 1
		sts.Status.ObservedGeneration = sts.Generation
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

		importJob := &batchv1.Job{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "mock-upgrade-upgrade-import", Namespace: "default"}, importJob); errors.IsNotFound(err) {
			importJob = &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mock-upgrade-upgrade-import",
					Namespace: "default",
					Annotations: map[string]string{
						upgradeHashAnnotation: desiredHash,
					},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{{
								Name:  "import",
								Image: "busybox",
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, importJob)).To(Succeed())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
		now = metav1.Now()
		importJob.Status.Succeeded = 1
		importJob.Status.StartTime = &now
		importJob.Status.CompletionTime = &now
		importJob.Status.Conditions = []batchv1.JobCondition{{
			Type:               batchv1.JobComplete,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			LastProbeTime:      now,
		}, {
			Type:               condSuccessCriteriaMet,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			LastProbeTime:      now,
		}}
		Expect(k8sClient.Status().Update(ctx, importJob)).To(Succeed())

		Eventually(func() string {
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
			final := &convexv1alpha1.ConvexInstance{}
			if err := k8sClient.Get(ctx, name, final); err != nil {
				return ""
			}
			return final.Status.Phase
		}, 20*time.Second, 200*time.Millisecond).Should(Equal("Ready"))

		final := &convexv1alpha1.ConvexInstance{}
		Expect(k8sClient.Get(ctx, name, final)).To(Succeed())
		upgradeCond := meta.FindStatusCondition(final.Status.Conditions, "UpgradeInProgress")
		Expect(upgradeCond).NotTo(BeNil())
		Expect(upgradeCond.Status).To(Equal(metav1.ConditionFalse))
	})
})

var _ = Describe("config and secret helpers", func() {
	ctx := context.Background()

	It("renders backend env vars including DB and S3 secrets", func() {
		instance := &convexv1alpha1.ConvexInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "env-vars",
				Namespace: "default",
			},
			Spec: convexv1alpha1.ConvexInstanceSpec{
				Environment: "prod",
				Version:     "9.9.9",
				Backend: convexv1alpha1.BackendSpec{
					Image: "ghcr.io/get-convex/convex-backend:9.9.9",
					DB: convexv1alpha1.BackendDatabaseSpec{
						Engine:    dbEnginePostgres,
						SecretRef: "db-secret",
						URLKey:    "url",
					},
					S3: convexv1alpha1.BackendS3Spec{
						Enabled:            true,
						SecretRef:          "s3-secret",
						EndpointKey:        "endpoint",
						RegionKey:          "region",
						AccessKeyIDKey:     "accessKey",
						SecretAccessKeyKey: "secretAccessKey",
						BucketKey:          "bucket",
					},
				},
				Networking: convexv1alpha1.NetworkingSpec{
					Host: "env-vars.example.com",
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, instance)
		})

		reconciler := &ConvexInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		_, err := reconciler.reconcileStatefulSet(ctx, instance, backendServiceName(instance), generatedSecretName(instance), "rv-secret", instance.Spec.Backend.Image, instance.Spec.Version, externalSecretVersions{
			dbResourceVersion: "db-rv",
			s3ResourceVersion: "s3-rv",
		})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts)).To(Succeed())
		envs := sts.Spec.Template.Spec.Containers[0].Env

		getEnv := func(name string) *corev1.EnvVar {
			for i := range envs {
				if envs[i].Name == name {
					return &envs[i]
				}
			}
			return nil
		}

		Expect(getEnv("CONVEX_PORT")).NotTo(BeNil())
		Expect(getEnv("CONVEX_PORT").Value).To(Equal(fmt.Sprintf("%d", defaultBackendPort)))
		Expect(getEnv("CONVEX_ENV")).NotTo(BeNil())
		Expect(getEnv("CONVEX_ENV").Value).To(Equal("prod"))
		Expect(getEnv("CONVEX_VERSION")).NotTo(BeNil())
		Expect(getEnv("CONVEX_VERSION").Value).To(Equal("9.9.9"))

		adminKeyEnv := getEnv("CONVEX_ADMIN_KEY")
		Expect(adminKeyEnv).NotTo(BeNil())
		Expect(adminKeyEnv.ValueFrom).NotTo(BeNil())
		Expect(adminKeyEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(adminKeyEnv.ValueFrom.SecretKeyRef.Name).To(Equal(generatedSecretName(instance)))
		Expect(adminKeyEnv.ValueFrom.SecretKeyRef.Key).To(Equal(adminKeyKey))

		instanceSecretEnv := getEnv("CONVEX_INSTANCE_SECRET")
		Expect(instanceSecretEnv).NotTo(BeNil())
		Expect(instanceSecretEnv.ValueFrom).NotTo(BeNil())
		Expect(instanceSecretEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(instanceSecretEnv.ValueFrom.SecretKeyRef.Name).To(Equal(generatedSecretName(instance)))
		Expect(instanceSecretEnv.ValueFrom.SecretKeyRef.Key).To(Equal(instanceSecretKey))

		dbEnv := getEnv("POSTGRES_URL")
		Expect(dbEnv).NotTo(BeNil())
		Expect(dbEnv.ValueFrom).NotTo(BeNil())
		Expect(dbEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(dbEnv.ValueFrom.SecretKeyRef.Name).To(Equal("db-secret"))
		Expect(dbEnv.ValueFrom.SecretKeyRef.Key).To(Equal("url"))

		Expect(getEnv("AWS_REGION")).NotTo(BeNil())
		Expect(getEnv("AWS_REGION").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_REGION").ValueFrom.SecretKeyRef.Key).To(Equal("region"))
		Expect(getEnv("AWS_ACCESS_KEY_ID")).NotTo(BeNil())
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ACCESS_KEY_ID").ValueFrom.SecretKeyRef.Key).To(Equal("accessKey"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY")).NotTo(BeNil())
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_SECRET_ACCESS_KEY").ValueFrom.SecretKeyRef.Key).To(Equal("secretAccessKey"))
		Expect(getEnv("AWS_ENDPOINT_URL_S3")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL_S3").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ENDPOINT_URL_S3").ValueFrom.SecretKeyRef.Key).To(Equal("endpoint"))
		Expect(getEnv("AWS_ENDPOINT_URL")).NotTo(BeNil())
		Expect(getEnv("AWS_ENDPOINT_URL").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("AWS_ENDPOINT_URL").ValueFrom.SecretKeyRef.Key).To(Equal("endpoint"))
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_EXPORTS_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_MODULES_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_FILES_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET")).NotTo(BeNil())
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").ValueFrom.SecretKeyRef.Name).To(Equal("s3-secret"))
		Expect(getEnv("S3_STORAGE_SEARCH_BUCKET").ValueFrom.SecretKeyRef.Key).To(Equal("bucket"))
	})
})
