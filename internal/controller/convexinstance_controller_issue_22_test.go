package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
)

var _ = Describe("Issue #22 Enhancement Verification", func() {
	Context("Backend Env Extensibility", func() {
		ctx := context.Background()
		var reconciler *ConvexInstanceReconciler

		BeforeEach(func() {
			reconciler = &ConvexInstanceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should correctly apply all extensibility features", func() {
			name := types.NamespacedName{Name: "issue-22-test", Namespace: "default"}
			instance := &convexv1alpha1.ConvexInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name.Name,
					Namespace: name.Namespace,
				},
				Spec: convexv1alpha1.ConvexInstanceSpec{
					Environment: "prod",
					Version:     "1.0.0",
					Backend: convexv1alpha1.BackendSpec{
						Image: "ghcr.io/get-convex/convex-backend:1.0.0",
						DB: convexv1alpha1.BackendDatabaseSpec{
							Engine: "sqlite",
						},
						Telemetry: convexv1alpha1.BackendTelemetrySpec{
							DisableBeacon: true,
						},
						Logging: convexv1alpha1.BackendLoggingSpec{
							RedactLogsToClient: true,
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
						Env: []corev1.EnvVar{
							{Name: "CUSTOM_VAR", Value: "custom_value"},
							{Name: "CONVEX_VERSION", Value: "override_version"},
						},
					},
					Networking: convexv1alpha1.NetworkingSpec{
						Host: "issue22.example.com",
					},
				},
			}

			// Create S3 secret
			s3Secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"endpoint":        []byte("https://s3.example.com"),
					"region":          []byte("us-east-1"),
					"accessKey":       []byte("key"),
					"secretAccessKey": []byte("secret"),
					"bucket":          []byte("bucket"),
				},
			}
			Expect(k8sClient.Create(ctx, s3Secret)).To(Succeed())
			DeferCleanup(func() {
				k8sClient.Delete(ctx, s3Secret)
			})

			Expect(k8sClient.Create(ctx, instance)).To(Succeed())
			DeferCleanup(func() {
				k8sClient.Delete(ctx, instance)
			})

			// Run reconcile
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
			Expect(err).NotTo(HaveOccurred())

			// Check StatefulSet
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-backend", name.Name), Namespace: name.Namespace}, sts)).To(Succeed())

			envs := sts.Spec.Template.Spec.Containers[0].Env
			envMap := map[string]string{}
			for _, e := range envs {
				if e.Value != "" {
					envMap[e.Name] = e.Value
				} else if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
					envMap[e.Name] = fmt.Sprintf("secret:%s:%s", e.ValueFrom.SecretKeyRef.Name, e.ValueFrom.SecretKeyRef.Key)
				}
			}

			// Verify Features
			Expect(envMap).To(HaveKeyWithValue("DISABLE_BEACON", "true"))
			Expect(envMap).To(HaveKeyWithValue("REDACT_LOGS_TO_CLIENT", "true"))
			Expect(envMap).To(HaveKeyWithValue("S3_ENDPOINT_URL", "secret:s3-secret:endpoint"))
			Expect(envMap).To(HaveKeyWithValue("CUSTOM_VAR", "custom_value"))
			// CONVEX_VERSION should be overridden
			Expect(envMap).To(HaveKeyWithValue("CONVEX_VERSION", "override_version"))
		})
	})
})
