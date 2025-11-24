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

			var adminKeyEnv *corev1.EnvVar
			envValues := map[string]string{}
			for i := range container.Env {
				env := container.Env[i]
				envValues[env.Name] = env.Value
				if env.Name == "NEXT_PUBLIC_ADMIN_KEY" {
					adminKeyEnv = &env
				}
			}
			Expect(envValues).To(HaveKeyWithValue("NEXT_PUBLIC_DEPLOYMENT_URL", "http://test-resource-backend.default.svc.cluster.local:3210"))
			Expect(adminKeyEnv).NotTo(BeNil())
			Expect(adminKeyEnv.ValueFrom).NotTo(BeNil())
			Expect(adminKeyEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(adminKeyEnv.ValueFrom.SecretKeyRef.Name).To(Equal("test-resource-convex-secrets"))
			Expect(adminKeyEnv.ValueFrom.SecretKeyRef.Key).To(Equal("adminKey"))
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
			instance.Spec.Backend.DB.Engine = "postgres"
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
			instance.Spec.Backend.DB.Engine = "postgres"
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
		desiredHash := desiredUpgradeHash(instance)
		instance.Status.UpgradeHash = desiredHash

		plan := buildUpgradePlan(instance, true, instance.Spec.Backend.Image, instance.Spec.Dashboard.Image, instance.Spec.Version, false, false, true, true, desiredHash)

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
})
