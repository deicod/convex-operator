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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ericlagergren/siv"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	finalizerName            = "convex.icod.de/finalizer"
	adminKeyKey              = "adminKey"
	instanceSecretKey        = "instanceSecret"
	adminKeyVersion          = uint8(1)
	adminKeyPurpose          = "admin key"
	adminKeyMemberID         = uint64(0)
	instanceSecretBytes      = 32
	instanceSecretHexLen     = instanceSecretBytes * 2
	defaultBackendPortName   = "http"
	defaultBackendPort       = 3210
	actionPortName           = "http-action"
	actionPort               = 3211
	defaultDashboardPortName = "http"
	defaultDashboardPort     = 6791
	configMapKey             = "convex.conf"
	conditionReady           = "Ready"
	conditionConfigMap       = "ConfigMapReady"
	conditionSecrets         = "SecretsReady"
	conditionPVC             = "PVCReady"
	conditionService         = "ServiceReady"
	conditionStatefulSet     = "StatefulSetReady"
	conditionDashboard       = "DashboardReady"
	conditionDashboardSvc    = "DashboardServiceReady"
	conditionGateway         = "GatewayReady"
	conditionHTTPRoute       = "HTTPRouteReady"
	conditionUpgrade         = "UpgradeInProgress"
	conditionExport          = "ExportCompleted"
	conditionImport          = "ImportCompleted"
	conditionRollingUpdate   = "RollingUpdate"
	conditionBackendReady    = "BackendReady"
	msgInstanceReady         = "Instance ready"
	msgBackendReady          = "Backend ready"
	phasePending             = "Pending"
	phaseReady               = "Ready"
	phaseUpgrading           = "Upgrading"
	phaseError               = "Error"
	upgradeStrategyInPlace   = "inPlace"
	upgradeStrategyExport    = "exportImport"
	storageModeExternal      = "external"
	dbEnginePostgres         = "postgres"
	dbEngineMySQL            = "mysql"
	dbEngineSQLite           = "sqlite"
	restartTriggerAnnotation = "convex.icod.de/restart-trigger"
	lastRestartAnnotation    = "convex.icod.de/last-restart"
	defaultGatewayClassName  = "nginx"
	defaultGatewayIssuerKey  = "cert-manager.io/cluster-issuer"
	defaultGatewayIssuer     = "letsencrypt-prod-rfc2136"
	upgradeExportJobSuffix   = "upgrade-export"
	upgradeImportJobSuffix   = "upgrade-import"
	upgradePVCNameSuffix     = "upgrade-pvc"
	upgradeHashAnnotation    = "convex.icod.de/upgrade-hash"
	defaultRestartInterval   = 7 * 24 * time.Hour
	obcOwnerKindClaim        = "ObjectBucketClaim"
	obcOwnerKindBucket       = "ObjectBucket"
	obcLabelProvisioner      = "bucket-provisioner"
	obcLabelProvisionerAlt   = "objectbucket.io/provisioner"
	obcAccessKeyIDKey        = "AWS_ACCESS_KEY_ID"
	obcSecretAccessKeyKey    = "AWS_SECRET_ACCESS_KEY"
	obcBucketNameKey         = "BUCKET_NAME"
	obcBucketHostKey         = "BUCKET_HOST"
	obcBucketPortKey         = "BUCKET_PORT"
	obcBucketRegionKey       = "BUCKET_REGION"
	defaultOBCRegion         = "us-east-1"
)

// ConvexInstanceReconciler reconciles a ConvexInstance object
type ConvexInstanceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=convex.icod.de,resources=convexinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=convex.icod.de,resources=convexinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=convex.icod.de,resources=convexinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;services;persistentvolumeclaims;events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status;httproutes/status,verbs=get

// Reconcile moves the cluster state toward the desired ConvexInstance state.
//
// The flow is:
// 1. Resolve the ConvexInstance and handle finalizers.
// 2. Observe current backend/dashboard state (images, versions).
// 3. Determine the upgrade plan (in-place vs export-import) based on spec changes and current job status.
// 4. Validate external references (DB/S3 secrets) to fail fast if missing.
// 5. Reconcile core resources (ConfigMap, Secrets, PVC, Services, Dashboard, StatefulSet, Gateway/Route).
// 6. Execute the upgrade plan (rolling update or export/import orchestration).
// 7. Update status conditions and phase.
func (r *ConvexInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &convexv1alpha1.ConvexInstance{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	oldPhase := instance.Status.Phase

	if stop, err := r.ensureFinalizer(ctx, instance); err != nil || stop {
		return ctrl.Result{}, err
	}

	// Capture current backend/dashboard state to drive upgrade decisions.
	var existingBackend appsv1.StatefulSet
	backendExists := false
	if err := r.Get(ctx, client.ObjectKey{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, &existingBackend); err == nil {
		backendExists = true
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	var existingDashboard appsv1.Deployment
	dashboardExists := false
	if err := r.Get(ctx, client.ObjectKey{Name: dashboardDeploymentName(instance), Namespace: instance.Namespace}, &existingDashboard); err == nil {
		dashboardExists = true
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	currentBackendImage := ""
	currentBackendEnv := []corev1.EnvVar{}
	if backendExists && len(existingBackend.Spec.Template.Spec.Containers) > 0 {
		currentBackendImage = existingBackend.Spec.Template.Spec.Containers[0].Image
	}
	currentDashboardImage := ""
	if dashboardExists && len(existingDashboard.Spec.Template.Spec.Containers) > 0 {
		currentDashboardImage = existingDashboard.Spec.Template.Spec.Containers[0].Image
	}
	currentBackendVersion := ""
	if backendExists && len(existingBackend.Spec.Template.Spec.Containers) > 0 {
		currentBackendVersion = backendVersionFromStatefulSet(&existingBackend)
		currentBackendEnv = deduplicateEnvs(existingBackend.Spec.Template.Spec.Containers[0].Env)
	}

	extVersions, err := r.validateExternalRefs(ctx, instance)
	if err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "ValidationFailed", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "ValidationFailed", err, conditionFalse(conditionSecrets, "ValidationFailed", err.Error()))
	}

	desiredEnv := backendEnvWithS3(instance, instance.Spec.Version, generatedSecretName(instance), extVersions.s3Resolved)
	desiredHash := desiredUpgradeHash(instance, desiredEnv)
	exportSucceeded, importSucceeded, exportFailed, importFailed := r.observeUpgradeJobs(ctx, instance, desiredHash)

	// buildUpgradePlan determines if an upgrade is needed and tracks the state of export/import jobs.
	// It calculates the effective images/versions to use for the core resources (e.g. keeping old version during export).
	plan := buildUpgradePlan(instance, backendExists, currentBackendImage, currentDashboardImage, currentBackendVersion, currentBackendEnv, exportSucceeded, importSucceeded, exportFailed, importFailed, desiredHash)

	coreRes, resErr := r.reconcileCoreResources(ctx, instance, plan, extVersions)
	if resErr != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, resErr.reason, resErr.err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, resErr.reason, resErr.err, resErr.cond)
	}

	// handleUpgrade executes the state transitions for upgrades (e.g. launching export jobs, blocking rollouts).
	// It returns the resulting status phase and conditions.
	status, err := r.handleUpgrade(ctx, instance, plan, coreRes.backendReady, coreRes.dashboardReady, coreRes.gatewayReady, coreRes.routeReady, coreRes.serviceName, coreRes.secretName, coreRes.conds)
	if err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "UpgradeError", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "UpgradeError", err, conditionFalse(conditionUpgrade, "UpgradeError", err.Error()))
	}

	readyEvent := status.phase == phaseReady && oldPhase != phaseReady
	if err := r.updateStatus(ctx, instance, status.phase, status.reason, coreRes.serviceName, status.message, coreRes.gatewayReady && coreRes.routeReady, status.appliedHash, status.conditions...); err != nil {
		if errors.IsConflict(err) {
			log.V(1).Info("status conflict, will retry", "name", req.NamespacedName)
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	if readyEvent {
		r.recordEvent(instance, corev1.EventTypeNormal, conditionReady, "Backend is ready")
	}

	requeueAfter := time.Duration(0)
	if status.phase != phaseReady || (!coreRes.dashboardReady && instance.Spec.Dashboard.Enabled) || !coreRes.gatewayReady || !coreRes.routeReady {
		requeueAfter = 5 * time.Second
	}
	if coreRes.nextRestartIn > 0 {
		if requeueAfter == 0 || coreRes.nextRestartIn < requeueAfter {
			requeueAfter = coreRes.nextRestartIn
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConvexInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&convexv1alpha1.ConvexInstance{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.Job{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				return nil
			}
			var instances convexv1alpha1.ConvexInstanceList
			if err := r.List(ctx, &instances, client.InNamespace(secret.Namespace)); err != nil {
				return nil
			}
			var requests []reconcile.Request
			for _, inst := range instances.Items {
				if inst.Spec.Backend.DB.SecretRef == secret.Name ||
					inst.Spec.Backend.S3.SecretRef == secret.Name ||
					inst.Spec.Networking.TLSSecretRef == secret.Name ||
					generatedSecretName(&inst) == secret.Name {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&inst)})
					continue
				}
				secretRefs, _ := backendEnvRefs(&inst)
				for _, ref := range secretRefs {
					if ref.name == secret.Name {
						requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&inst)})
						break
					}
				}
			}
			return requests
		})).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok {
				return nil
			}
			var instances convexv1alpha1.ConvexInstanceList
			if err := r.List(ctx, &instances, client.InNamespace(cm.Namespace)); err != nil {
				return nil
			}
			var requests []reconcile.Request
			for _, inst := range instances.Items {
				if inst.Spec.Backend.S3.Enabled {
					s3 := inst.Spec.Backend.S3
					if s3.ConfigMapRef == cm.Name {
						requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&inst)})
						continue
					}
					if s3.ConfigMapRef == "" && s3.SecretRef == cm.Name && autoDetectOBCEnabled(s3) {
						requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&inst)})
						continue
					}
				}
				_, configRefs := backendEnvRefs(&inst)
				for _, ref := range configRefs {
					if ref.name == cm.Name {
						requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&inst)})
						break
					}
				}
			}
			return requests
		})).
		Named("convexinstance").
		Complete(r)
}

type externalSecretVersions struct {
	dbResourceVersion  string
	s3ResourceVersion  string
	tlsResourceVersion string
	envSecretVersions  map[string]string
	envConfigVersions  map[string]string
	s3Resolved         convexv1alpha1.BackendS3Spec
}

type envRef struct {
	name     string
	key      string
	optional bool
}

func autoDetectOBCEnabled(s3 convexv1alpha1.BackendS3Spec) bool {
	return ptr.Deref(s3.AutoDetectOBC, true)
}

func isOBCManagedSecret(secret *corev1.Secret) bool {
	for _, owner := range secret.OwnerReferences {
		if owner.Kind == obcOwnerKindClaim || owner.Kind == obcOwnerKindBucket {
			return true
		}
	}
	labels := secret.Labels
	if labels == nil {
		return false
	}
	if _, ok := labels[obcLabelProvisioner]; ok {
		return true
	}
	if _, ok := labels[obcLabelProvisionerAlt]; ok {
		return true
	}
	return false
}

func (r *ConvexInstanceReconciler) validateDBRef(ctx context.Context, namespace string, db convexv1alpha1.BackendDatabaseSpec) (string, error) {
	if db.Engine != dbEngineSQLite && db.SecretRef == "" {
		return "", fmt.Errorf("db secret is required for engine %q", db.Engine)
	}
	if db.Engine != dbEngineSQLite && db.URLKey == "" {
		return "", fmt.Errorf("db urlKey is required for engine %q", db.Engine)
	}
	if ref := db.SecretRef; ref != "" {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: ref, Namespace: namespace}, secret); err != nil {
			return "", fmt.Errorf("db secret %q: %w", ref, err)
		}
		if db.URLKey != "" {
			if _, ok := secret.Data[db.URLKey]; !ok {
				return "", fmt.Errorf("db secret %q missing key %q", ref, db.URLKey)
			}
		}
		return secret.ResourceVersion, nil
	}
	return "", nil
}

type s3InlineFlags struct {
	bucket   bool
	region   bool
	endpoint bool
}

func s3InlineFlagsFor(s3 convexv1alpha1.BackendS3Spec) s3InlineFlags {
	return s3InlineFlags{
		bucket:   s3.Bucket != "",
		region:   s3.Region != "",
		endpoint: s3.Endpoint != "",
	}
}

func resolveS3WithOBC(s3 convexv1alpha1.BackendS3Spec, secret *corev1.Secret, inline s3InlineFlags) convexv1alpha1.BackendS3Spec {
	if !autoDetectOBCEnabled(s3) || !isOBCManagedSecret(secret) {
		return s3
	}
	needsConfigMap := !inline.bucket || !inline.region || !inline.endpoint
	if s3.ConfigMapRef == "" && needsConfigMap {
		s3.ConfigMapRef = s3.SecretRef
	}
	if s3.AccessKeyIDKey == "" {
		s3.AccessKeyIDKey = obcAccessKeyIDKey
	}
	if s3.SecretAccessKeyKey == "" {
		s3.SecretAccessKeyKey = obcSecretAccessKeyKey
	}
	if s3.ConfigMapRef == s3.SecretRef {
		if !inline.bucket && s3.BucketKey == "" {
			s3.BucketKey = obcBucketNameKey
		}
		if !inline.region && s3.RegionKey == "" {
			s3.RegionKey = obcBucketRegionKey
		}
		if !inline.endpoint && s3.EndpointKey == "" && s3.EndpointHostKey == "" {
			s3.EndpointHostKey = obcBucketHostKey
		}
		if !inline.endpoint && s3.EndpointKey == "" && s3.EndpointHostKey == obcBucketHostKey && s3.EndpointPortKey == "" {
			s3.EndpointPortKey = obcBucketPortKey
		}
	}
	return s3
}

func validateS3Spec(s3 convexv1alpha1.BackendS3Spec, inline s3InlineFlags) error {
	useConfigMap := s3.ConfigMapRef != ""
	if s3.AccessKeyIDKey == "" {
		return fmt.Errorf("s3 accessKeyIdKey is required when s3 is enabled")
	}
	if s3.SecretAccessKeyKey == "" {
		return fmt.Errorf("s3 secretAccessKeyKey is required when s3 is enabled")
	}
	if !inline.bucket && s3.BucketKey == "" {
		return fmt.Errorf("s3 bucket or bucketKey is required when s3 is enabled")
	}
	if !inline.region && s3.RegionKey == "" {
		return fmt.Errorf("s3 region or regionKey is required when s3 is enabled")
	}
	if inline.endpoint {
		return nil
	}
	if useConfigMap {
		if s3.EndpointKey != "" && s3.EndpointHostKey != "" {
			return fmt.Errorf("s3 endpointKey and endpointHostKey are mutually exclusive")
		}
		if s3.EndpointPortKey != "" && s3.EndpointHostKey == "" {
			return fmt.Errorf("s3 endpointPortKey requires endpointHostKey")
		}
		if s3.EndpointKey == "" && s3.EndpointHostKey == "" {
			return fmt.Errorf("s3 endpoint or endpointKey/endpointHostKey is required when s3 is enabled")
		}
		return nil
	}
	if s3.EndpointHostKey != "" || s3.EndpointPortKey != "" {
		return fmt.Errorf("s3 endpointHostKey requires configMapRef")
	}
	if s3.EndpointKey == "" {
		return fmt.Errorf("s3 endpoint or endpointKey is required when s3 is enabled")
	}
	return nil
}

func s3SecretKeys(s3 convexv1alpha1.BackendS3Spec, inline s3InlineFlags, useConfigMap bool) []string {
	keys := []string{s3.AccessKeyIDKey, s3.SecretAccessKeyKey}
	if useConfigMap {
		return keys
	}
	if !inline.endpoint {
		keys = append(keys, s3.EndpointKey)
	}
	if !inline.region {
		keys = append(keys, s3.RegionKey)
	}
	if !inline.bucket {
		keys = append(keys, s3.BucketKey)
	}
	return keys
}

func s3ConfigKeys(s3 convexv1alpha1.BackendS3Spec, inline s3InlineFlags) []string {
	keys := []string{}
	if !inline.bucket {
		keys = append(keys, s3.BucketKey)
	}
	if !inline.region {
		keys = append(keys, s3.RegionKey)
	}
	if !inline.endpoint {
		if s3.EndpointKey != "" {
			keys = append(keys, s3.EndpointKey)
		} else {
			keys = append(keys, s3.EndpointHostKey)
			if s3.EndpointPortKey != "" {
				keys = append(keys, s3.EndpointPortKey)
			}
		}
	}
	return keys
}

func s3NeedsConfigMap(s3 convexv1alpha1.BackendS3Spec, inline s3InlineFlags) bool {
	return s3.ConfigMapRef != "" && (!inline.bucket || !inline.region || !inline.endpoint)
}

func validateSecretKeys(secret *corev1.Secret, name string, keys []string) error {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := secret.Data[key]; !ok {
			return fmt.Errorf("s3 secret %q missing key %q", name, key)
		}
	}
	return nil
}

func validateConfigMapKeys(cm *corev1.ConfigMap, name string, keys []string) error {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := cm.Data[key]; !ok {
			return fmt.Errorf("s3 configmap %q missing key %q", name, key)
		}
	}
	return nil
}

func defaultS3RegionIfEmpty(s3 convexv1alpha1.BackendS3Spec, inline s3InlineFlags, cm *corev1.ConfigMap) convexv1alpha1.BackendS3Spec {
	if inline.region || s3.RegionKey == "" || s3.RegionKey != obcBucketRegionKey || cm == nil {
		return s3
	}
	if strings.TrimSpace(cm.Data[s3.RegionKey]) != "" {
		return s3
	}
	s3.Region = defaultOBCRegion
	return s3
}

func (r *ConvexInstanceReconciler) validateS3Ref(ctx context.Context, namespace string, s3 convexv1alpha1.BackendS3Spec) (convexv1alpha1.BackendS3Spec, string, error) {
	if !s3.Enabled {
		return s3, "", nil
	}
	if s3.SecretRef == "" {
		return s3, "", fmt.Errorf("s3 secret is required when s3 is enabled")
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: s3.SecretRef, Namespace: namespace}, secret); err != nil {
		return s3, "", fmt.Errorf("s3 secret %q: %w", s3.SecretRef, err)
	}
	inline := s3InlineFlagsFor(s3)
	s3 = resolveS3WithOBC(s3, secret, inline)
	if err := validateS3Spec(s3, inline); err != nil {
		return s3, "", err
	}
	if err := validateSecretKeys(secret, s3.SecretRef, s3SecretKeys(s3, inline, s3.ConfigMapRef != "")); err != nil {
		return s3, "", err
	}
	if !s3NeedsConfigMap(s3, inline) {
		return s3, secret.ResourceVersion, nil
	}
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Name: s3.ConfigMapRef, Namespace: namespace}, cm); err != nil {
		return s3, "", fmt.Errorf("s3 configmap %q: %w", s3.ConfigMapRef, err)
	}
	if err := validateConfigMapKeys(cm, s3.ConfigMapRef, s3ConfigKeys(s3, inline)); err != nil {
		return s3, "", err
	}
	s3 = defaultS3RegionIfEmpty(s3, inline, cm)
	return s3, configHash(secret.ResourceVersion, cm.ResourceVersion), nil
}

func (r *ConvexInstanceReconciler) validateTLSRef(ctx context.Context, namespace, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: ref, Namespace: namespace}, secret); err != nil {
		return "", fmt.Errorf("tls secret %q: %w", ref, err)
	}
	return secret.ResourceVersion, nil
}

func (r *ConvexInstanceReconciler) validateBackendEnvRefs(ctx context.Context, namespace string, instance *convexv1alpha1.ConvexInstance) (map[string]string, map[string]string, error) {
	secretVersions := map[string]string{}
	configVersions := map[string]string{}
	secretRefs, configRefs := backendEnvRefs(instance)
	for _, ref := range secretRefs {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: ref.name, Namespace: namespace}, secret); err != nil {
			if ref.optional && errors.IsNotFound(err) {
				continue
			}
			return secretVersions, configVersions, fmt.Errorf("env secret %q: %w", ref.name, err)
		}
		if _, ok := secret.Data[ref.key]; !ok {
			if ref.optional {
				continue
			}
			return secretVersions, configVersions, fmt.Errorf("env secret %q missing key %q", ref.name, ref.key)
		}
		secretVersions[ref.name] = secret.ResourceVersion
	}
	for _, ref := range configRefs {
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{Name: ref.name, Namespace: namespace}, cm); err != nil {
			if ref.optional && errors.IsNotFound(err) {
				continue
			}
			return secretVersions, configVersions, fmt.Errorf("env configmap %q: %w", ref.name, err)
		}
		if _, ok := cm.Data[ref.key]; !ok {
			if ref.optional {
				continue
			}
			return secretVersions, configVersions, fmt.Errorf("env configmap %q missing key %q", ref.name, ref.key)
		}
		configVersions[ref.name] = cm.ResourceVersion
	}
	return secretVersions, configVersions, nil
}

type upgradePlan struct {
	strategy                string
	desiredHash             string
	appliedHash             string
	upgradePending          bool
	upgradePlanned          bool
	exportDone              bool
	importDone              bool
	exportFailed            bool
	importFailed            bool
	backendChanged          bool
	dashboardChanged        bool
	effectiveBackendImage   string
	effectiveDashboardImage string
	effectiveVersion        string
	currentBackendImage     string
	currentDashboardImage   string
	currentBackendVersion   string
}

type upgradeStatus struct {
	phase       string
	reason      string
	message     string
	appliedHash string
	conditions  []metav1.Condition
}

type reconcileOutcome struct {
	serviceName          string
	dashboardServiceName string
	dashboardReady       bool
	gatewayReady         bool
	routeReady           bool
	backendReady         bool
	secretName           string
	secretRV             string
	conds                []metav1.Condition
	nextRestartIn        time.Duration
}

type resourceErr struct {
	reason string
	cond   metav1.Condition
	err    error
}

func (r *ConvexInstanceReconciler) observeUpgradeJobs(ctx context.Context, instance *convexv1alpha1.ConvexInstance, desiredHash string) (bool, bool, bool, bool) {
	exportSucceeded := false
	importSucceeded := false
	exportFailed := false
	importFailed := false

	expJob := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: exportJobName(instance), Namespace: instance.Namespace}, expJob); err == nil {
		matchesHash := expJob.Annotations != nil && expJob.Annotations[upgradeHashAnnotation] == desiredHash
		if matchesHash && jobSucceeded(expJob) {
			exportSucceeded = true
		}
		if matchesHash && jobFailed(expJob) {
			exportFailed = true
		}
	}
	impJob := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: importJobName(instance), Namespace: instance.Namespace}, impJob); err == nil {
		matchesHash := impJob.Annotations != nil && impJob.Annotations[upgradeHashAnnotation] == desiredHash
		if matchesHash && jobSucceeded(impJob) {
			importSucceeded = true
		}
		if matchesHash && jobFailed(impJob) {
			importFailed = true
		}
	}
	return exportSucceeded, importSucceeded, exportFailed, importFailed
}

// ensureFinalizer adds or removes the controller finalizer. It returns true when reconciliation should stop (during deletion) or when an update occurred.
func (r *ConvexInstanceReconciler) ensureFinalizer(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (bool, error) {
	if instance.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(instance, finalizerName) {
			controllerutil.AddFinalizer(instance, finalizerName)
			if err := r.Update(ctx, instance); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	controllerutil.RemoveFinalizer(instance, finalizerName)
	if err := r.Update(ctx, instance); err != nil {
		return true, err
	}
	return true, nil
}

func (r *ConvexInstanceReconciler) validateExternalRefs(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (externalSecretVersions, error) {
	result := externalSecretVersions{}
	result.envSecretVersions = map[string]string{}
	result.envConfigVersions = map[string]string{}
	var err error
	if result.dbResourceVersion, err = r.validateDBRef(ctx, instance.Namespace, instance.Spec.Backend.DB); err != nil {
		return result, err
	}
	if result.s3Resolved, result.s3ResourceVersion, err = r.validateS3Ref(ctx, instance.Namespace, instance.Spec.Backend.S3); err != nil {
		return result, err
	}
	if result.tlsResourceVersion, err = r.validateTLSRef(ctx, instance.Namespace, instance.Spec.Networking.TLSSecretRef); err != nil {
		return result, err
	}
	if result.envSecretVersions, result.envConfigVersions, err = r.validateBackendEnvRefs(ctx, instance.Namespace, instance); err != nil {
		return result, err
	}
	return result, nil
}

func (r *ConvexInstanceReconciler) reconcileConfigMap(ctx context.Context, instance *convexv1alpha1.ConvexInstance, version string) error {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: backendConfigMapName(instance), Namespace: instance.Namespace}
	err := r.Get(ctx, key, cm)
	if errors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendConfigMapName(instance),
				Namespace: instance.Namespace,
			},
			Data: map[string]string{
				configMapKey: r.renderBackendConfig(instance, version),
			},
		}
		if err := controllerutil.SetControllerReference(instance, cm, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, cm)
	}
	if err != nil {
		return err
	}

	ownerChanged, err := ensureOwner(instance, cm, r.Scheme)
	if err != nil {
		return err
	}

	expected := r.renderBackendConfig(instance, version)
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	changed := ownerChanged
	if cm.Data[configMapKey] != expected {
		cm.Data[configMapKey] = expected
		changed = true
	}
	if changed {
		return r.Update(ctx, cm)
	}
	return nil
}

func (r *ConvexInstanceReconciler) renderBackendConfig(instance *convexv1alpha1.ConvexInstance, version string) string {
	return fmt.Sprintf("CONVEX_PORT=%d\nCONVEX_ENV=%s\nCONVEX_VERSION=%s\n", defaultBackendPort, instance.Spec.Environment, version)
}

func configHash(content string, values ...string) string {
	buf := []byte(content)
	for _, v := range values {
		buf = append(buf, []byte(v)...)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

func (r *ConvexInstanceReconciler) reconcileSecrets(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (string, string, error) {
	secretName := generatedSecretName(instance)
	instanceName := backendInstanceName(instance)
	secret := &corev1.Secret{}
	key := client.ObjectKey{Name: secretName, Namespace: instance.Namespace}
	err := r.Get(ctx, key, secret)
	if errors.IsNotFound(err) {
		instanceSecret, err := generateInstanceSecret()
		if err != nil {
			return "", "", err
		}
		adminKey, err := generateAdminKey(instanceName, instanceSecret)
		if err != nil {
			return "", "", err
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: instance.Namespace,
			},
			StringData: map[string]string{
				adminKeyKey:       adminKey,
				instanceSecretKey: instanceSecret,
			},
		}
		if err := controllerutil.SetControllerReference(instance, secret, r.Scheme); err != nil {
			return "", "", err
		}
		if err := r.Create(ctx, secret); err != nil {
			return "", "", err
		}
		return secretName, secret.ResourceVersion, nil
	}
	if err != nil {
		return "", "", err
	}
	ownerChanged, err := ensureOwner(instance, secret, r.Scheme)
	if err != nil {
		return "", "", err
	}

	changed := ownerChanged
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	instanceSecretVal, instanceSecretValid := normalizeInstanceSecret(secret.Data[instanceSecretKey])
	if !instanceSecretValid {
		instanceSecretVal, err = generateInstanceSecret()
		if err != nil {
			return "", "", err
		}
		secret.Data[instanceSecretKey] = []byte(instanceSecretVal)
		changed = true
	}
	adminKeyValid := adminKeyLooksValid(secret.Data[adminKeyKey])
	if !adminKeyValid || !instanceSecretValid {
		adminKey, err := generateAdminKey(instanceName, instanceSecretVal)
		if err != nil {
			return "", "", err
		}
		secret.Data[adminKeyKey] = []byte(adminKey)
		changed = true
	}

	if changed {
		if err := r.Update(ctx, secret); err != nil {
			return "", "", err
		}
	}
	return secretName, secret.ResourceVersion, nil
}

func (r *ConvexInstanceReconciler) reconcilePVC(ctx context.Context, instance *convexv1alpha1.ConvexInstance) error {
	if instance.Spec.Backend.Storage.Mode == storageModeExternal || !instance.Spec.Backend.Storage.PVC.Enabled {
		return nil
	}
	pvc := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Name: backendPVCName(instance), Namespace: instance.Namespace}
	err := r.Get(ctx, key, pvc)
	if errors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendPVCName(instance),
				Namespace: instance.Namespace,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: pvcSize(instance),
					},
				},
				StorageClassName: storageClassPtr(instance),
			},
		}
		if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, pvc)
	}
	if err != nil {
		return err
	}

	ownerChanged, err := ensureOwner(instance, pvc, r.Scheme)
	if err != nil {
		return err
	}

	desiredSize := pvcSize(instance)
	desiredSC := storageClassPtr(instance)
	if pvc.Spec.Resources.Requests == nil {
		pvc.Spec.Resources.Requests = corev1.ResourceList{}
	}
	currentSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	needsUpdate := false
	if desiredSize.Cmp(currentSize) != 0 {
		if !currentSize.IsZero() && desiredSize.Cmp(currentSize) < 0 {
			return fmt.Errorf("pvc size shrink not supported: current=%s desired=%s", currentSize.String(), desiredSize.String())
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = desiredSize
		needsUpdate = true
	}
	if desiredSC != nil && !storageClassEqual(desiredSC, pvc.Spec.StorageClassName) {
		if pvc.Spec.StorageClassName != nil {
			return fmt.Errorf("pvc storageClassName is immutable: current=%s desired=%s", ptr.Deref(pvc.Spec.StorageClassName, ""), ptr.Deref(desiredSC, ""))
		}
		pvc.Spec.StorageClassName = desiredSC
		needsUpdate = true
	}
	if needsUpdate || ownerChanged {
		return r.Update(ctx, pvc)
	}
	return nil
}

func (r *ConvexInstanceReconciler) reconcileUpgradePVC(ctx context.Context, instance *convexv1alpha1.ConvexInstance) error {
	pvc := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Name: upgradePVCName(instance), Namespace: instance.Namespace}
	err := r.Get(ctx, key, pvc)
	size := resource.MustParse("1Gi")
	desiredSC := storageClassPtr(instance)
	if errors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upgradePVCName(instance),
				Namespace: instance.Namespace,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: desiredSC,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: size,
					},
				},
			},
		}
		if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, pvc)
	}
	if err != nil {
		return err
	}

	ownerChanged, err := ensureOwner(instance, pvc, r.Scheme)
	if err != nil {
		return err
	}
	needsUpdate := ownerChanged
	currentSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if currentSize.IsZero() || currentSize.Cmp(size) < 0 {
		if pvc.Spec.Resources.Requests == nil {
			pvc.Spec.Resources.Requests = corev1.ResourceList{}
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = size
		needsUpdate = true
	}
	if desiredSC != nil && !storageClassEqual(desiredSC, pvc.Spec.StorageClassName) {
		if pvc.Spec.StorageClassName != nil {
			return fmt.Errorf("upgrade pvc storageClassName is immutable: current=%s desired=%s", ptr.Deref(pvc.Spec.StorageClassName, ""), ptr.Deref(desiredSC, ""))
		}
		pvc.Spec.StorageClassName = desiredSC
		needsUpdate = true
	}
	if needsUpdate {
		return r.Update(ctx, pvc)
	}
	return nil
}

func pvcSize(instance *convexv1alpha1.ConvexInstance) resource.Quantity {
	if !instance.Spec.Backend.Storage.PVC.Size.IsZero() {
		return instance.Spec.Backend.Storage.PVC.Size
	}
	return resource.MustParse("10Gi")
}

func storageClassPtr(instance *convexv1alpha1.ConvexInstance) *string {
	if instance.Spec.Backend.Storage.PVC.StorageClassName != "" {
		return ptr.To(instance.Spec.Backend.Storage.PVC.StorageClassName)
	}
	return nil
}

func storageClassEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func (r *ConvexInstanceReconciler) reconcileService(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (string, error) {
	name := backendServiceName(instance)
	svc := &corev1.Service{}
	key := client.ObjectKey{Name: name, Namespace: instance.Namespace}
	err := r.Get(ctx, key, svc)
	ports := []corev1.ServicePort{
		{
			Name:       defaultBackendPortName,
			Port:       defaultBackendPort,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt(defaultBackendPort),
		},
		{
			Name:       actionPortName,
			Port:       actionPort,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt(actionPort),
		},
	}
	selector := map[string]string{
		"app.kubernetes.io/name":      "convex-backend",
		"app.kubernetes.io/instance":  instance.Name,
		"app.kubernetes.io/component": "backend",
	}
	if errors.IsNotFound(err) {
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: instance.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Ports:    ports,
				Selector: selector,
			},
		}
		if err := controllerutil.SetControllerReference(instance, svc, r.Scheme); err != nil {
			return name, err
		}
		return name, r.Create(ctx, svc)
	}
	if err != nil {
		return name, err
	}
	ownerChanged, err := ensureOwner(instance, svc, r.Scheme)
	if err != nil {
		return name, err
	}
	if ownerChanged || !servicePortsEqual(svc.Spec.Ports, ports) || !selectorsEqual(svc.Spec.Selector, selector) {
		svc.Spec.Ports = ports
		svc.Spec.Selector = selector
		return name, r.Update(ctx, svc)
	}
	return name, nil
}

func (r *ConvexInstanceReconciler) reconcileDashboardService(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (string, metav1.Condition, error) {
	name := dashboardServiceName(instance)
	key := client.ObjectKey{Name: name, Namespace: instance.Namespace}

	if !instance.Spec.Dashboard.Enabled {
		svc := &corev1.Service{}
		if err := r.Get(ctx, key, svc); err == nil {
			if err := r.Delete(ctx, svc); err != nil && !errors.IsNotFound(err) {
				return "", metav1.Condition{}, err
			}
		} else if !errors.IsNotFound(err) {
			return "", metav1.Condition{}, err
		}
		return "", conditionTrue(conditionDashboardSvc, "Disabled", "Dashboard disabled"), nil
	}

	ports := []corev1.ServicePort{
		{
			Name:       defaultDashboardPortName,
			Port:       defaultDashboardPort,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt(defaultDashboardPort),
		},
	}
	selector := map[string]string{
		"app.kubernetes.io/name":      "convex-dashboard",
		"app.kubernetes.io/instance":  instance.Name,
		"app.kubernetes.io/component": "dashboard",
	}

	svc := &corev1.Service{}
	err := r.Get(ctx, key, svc)
	if errors.IsNotFound(err) {
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: instance.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Ports:    ports,
				Selector: selector,
			},
		}
		if err := controllerutil.SetControllerReference(instance, svc, r.Scheme); err != nil {
			return name, metav1.Condition{}, err
		}
		if err := r.Create(ctx, svc); err != nil {
			return name, metav1.Condition{}, err
		}
		return name, conditionFalse(conditionDashboardSvc, "Provisioning", "Dashboard service created"), nil
	}
	if err != nil {
		return name, metav1.Condition{}, err
	}

	ownerChanged, err := ensureOwner(instance, svc, r.Scheme)
	if err != nil {
		return name, metav1.Condition{}, err
	}
	if ownerChanged || !servicePortsEqual(svc.Spec.Ports, ports) || !selectorsEqual(svc.Spec.Selector, selector) {
		svc.Spec.Ports = ports
		svc.Spec.Selector = selector
		if err := r.Update(ctx, svc); err != nil {
			return name, metav1.Condition{}, err
		}
		return name, conditionFalse(conditionDashboardSvc, "Provisioning", "Dashboard service updated"), nil
	}
	return name, conditionTrue(conditionDashboardSvc, "Available", "Dashboard service ready"), nil
}

func dashboardSecurityContexts(instance *convexv1alpha1.ConvexInstance) (*corev1.PodSecurityContext, *corev1.SecurityContext) {
	pod := &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		RunAsUser:    ptr.To(int64(1001)),
	}
	container := &corev1.SecurityContext{
		RunAsNonRoot: ptr.To(true),
		RunAsUser:    ptr.To(int64(1001)),
	}
	if instance.Spec.Dashboard.Security.Pod != nil {
		pod = instance.Spec.Dashboard.Security.Pod.DeepCopy()
	}
	if instance.Spec.Dashboard.Security.Container != nil {
		container = instance.Spec.Dashboard.Security.Container.DeepCopy()
	}
	return pod, container
}

func backendSecurityContexts(instance *convexv1alpha1.ConvexInstance) (*corev1.PodSecurityContext, *corev1.SecurityContext) {
	pod := instance.Spec.Backend.Security.Pod
	container := instance.Spec.Backend.Security.Container
	if pod != nil {
		pod = pod.DeepCopy()
	}
	if container != nil {
		container = container.DeepCopy()
	}
	return pod, container
}

func dbURLVarNames(engine string) []string {
	switch engine {
	case dbEnginePostgres:
		return []string{"POSTGRES_URL"}
	case dbEngineMySQL:
		return []string{"MYSQL_URL"}
	default:
		return nil
	}
}

func requireDBSSL(instance *convexv1alpha1.ConvexInstance) bool {
	if instance.Spec.Backend.DB.RequireSSL == nil {
		return true
	}
	return *instance.Spec.Backend.DB.RequireSSL
}

func backendInstanceName(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Backend.DB.DatabaseName != "" {
		return instance.Spec.Backend.DB.DatabaseName
	}
	return strings.ReplaceAll(instance.Name, "-", "_")
}

func restartInterval(instance *convexv1alpha1.ConvexInstance) time.Duration {
	interval := defaultRestartInterval
	if instance.Spec.Maintenance.RestartInterval != nil {
		interval = instance.Spec.Maintenance.RestartInterval.Duration
	}
	if interval <= 0 {
		return 0
	}
	return interval
}

func annotationValue(annotations map[string]string, key string) string {
	if annotations == nil {
		return ""
	}
	return annotations[key]
}

func parseRestartTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func resolveLastRestart(sts *appsv1.StatefulSet, now time.Time) time.Time {
	if sts == nil {
		return now
	}
	if ts := parseRestartTimestamp(annotationValue(sts.Annotations, lastRestartAnnotation)); !ts.IsZero() {
		return ts
	}
	if ts := parseRestartTimestamp(annotationValue(sts.Spec.Template.Annotations, restartTriggerAnnotation)); !ts.IsZero() {
		return ts
	}
	if !sts.CreationTimestamp.IsZero() {
		return sts.CreationTimestamp.Time
	}
	return now
}

type restartPlan struct {
	enabled               bool
	nextRestartIn         time.Duration
	desiredLastRestart    time.Time
	desiredRestartTrigger string
}

// computeRestartPlan calculates whether a rolling restart is due based on the RestartInterval.
// It checks the last restart timestamp (from annotations) and returns the next scheduled restart time.
func computeRestartPlan(instance *convexv1alpha1.ConvexInstance, sts *appsv1.StatefulSet, now time.Time) restartPlan {
	interval := restartInterval(instance)
	plan := restartPlan{
		desiredLastRestart: now,
	}
	templateAnnotations := map[string]string{}
	if sts != nil && sts.Spec.Template.Annotations != nil {
		templateAnnotations = sts.Spec.Template.Annotations
	}
	if sts != nil {
		plan.desiredLastRestart = resolveLastRestart(sts, now)
	}

	if interval <= 0 {
		plan.desiredRestartTrigger = annotationValue(templateAnnotations, restartTriggerAnnotation)
		return plan
	}

	plan.enabled = true
	lastRestart := plan.desiredLastRestart
	if lastRestart.IsZero() {
		lastRestart = now
	}
	restartDue := now.Sub(lastRestart) >= interval
	if restartDue {
		plan.desiredLastRestart = now
	}

	if restartDue {
		plan.nextRestartIn = interval
	} else {
		plan.nextRestartIn = time.Until(lastRestart.Add(interval))
		if plan.nextRestartIn < 0 {
			plan.nextRestartIn = interval
		}
	}

	switch {
	case restartDue:
		plan.desiredRestartTrigger = plan.desiredLastRestart.Format(time.RFC3339)
	case sts == nil:
		plan.desiredRestartTrigger = plan.desiredLastRestart.Format(time.RFC3339)
	default:
		plan.desiredRestartTrigger = annotationValue(templateAnnotations, restartTriggerAnnotation)
	}

	return plan
}

func deduplicateEnvs(env []corev1.EnvVar) []corev1.EnvVar {
	if len(env) == 0 {
		return env
	}
	seen := map[string]bool{}
	dedup := make([]corev1.EnvVar, 0, len(env))
	for i := len(env) - 1; i >= 0; i-- {
		if seen[env[i].Name] {
			continue
		}
		seen[env[i].Name] = true
		dedup = append(dedup, env[i])
	}
	for i, j := 0, len(dedup)-1; i < j; i, j = i+1, j-1 {
		dedup[i], dedup[j] = dedup[j], dedup[i]
	}
	return dedup
}

func backendEnvRefs(instance *convexv1alpha1.ConvexInstance) ([]envRef, []envRef) {
	var secretRefs []envRef
	var configRefs []envRef
	for _, env := range instance.Spec.Backend.Env {
		if env.ValueFrom == nil {
			continue
		}
		if ref := env.ValueFrom.SecretKeyRef; ref != nil && ref.Name != "" && ref.Key != "" {
			secretRefs = append(secretRefs, envRef{name: ref.Name, key: ref.Key, optional: ptr.Deref(ref.Optional, false)})
		}
		if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil && ref.Name != "" && ref.Key != "" {
			configRefs = append(configRefs, envRef{name: ref.Name, key: ref.Key, optional: ptr.Deref(ref.Optional, false)})
		}
	}
	return secretRefs, configRefs
}

func backendEnv(instance *convexv1alpha1.ConvexInstance, backendVersion, secretName string) []corev1.EnvVar {
	return backendEnvWithS3(instance, backendVersion, secretName, instance.Spec.Backend.S3)
}

func backendEnvWithS3(instance *convexv1alpha1.ConvexInstance, backendVersion, secretName string, s3 convexv1alpha1.BackendS3Spec) []corev1.EnvVar {
	if backendVersion == "" {
		backendVersion = instance.Spec.Version
	}
	env := []corev1.EnvVar{}
	addEnv := func(e corev1.EnvVar) {
		env = append(env, e)
	}
	addEnv(corev1.EnvVar{
		Name:  "CONVEX_PORT",
		Value: fmt.Sprintf("%d", defaultBackendPort),
	})
	addEnv(corev1.EnvVar{
		Name:  "CONVEX_ENV",
		Value: instance.Spec.Environment,
	})
	addEnv(corev1.EnvVar{
		Name:  "CONVEX_VERSION",
		Value: backendVersion,
	})
	addEnv(corev1.EnvVar{
		Name:  "INSTANCE_NAME",
		Value: backendInstanceName(instance),
	})
	addEnv(corev1.EnvVar{
		Name: "CONVEX_ADMIN_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  adminKeyKey,
			},
		},
	})
	addEnv(corev1.EnvVar{
		Name: "CONVEX_INSTANCE_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  instanceSecretKey,
			},
		},
	})

	if co := cloudOrigin(instance); co != "" {
		addEnv(corev1.EnvVar{
			Name:  "CONVEX_CLOUD_ORIGIN",
			Value: co,
		})
	}
	if so := siteOrigin(instance); so != "" {
		addEnv(corev1.EnvVar{
			Name:  "CONVEX_SITE_ORIGIN",
			Value: so,
		})
	}

	if ref := instance.Spec.Backend.DB.SecretRef; ref != "" && instance.Spec.Backend.DB.URLKey != "" {
		dbEnvSource := &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref},
				Key:                  instance.Spec.Backend.DB.URLKey,
			},
		}
		for _, name := range dbURLVarNames(instance.Spec.Backend.DB.Engine) {
			addEnv(corev1.EnvVar{
				Name:      name,
				ValueFrom: dbEnvSource,
			})
		}
	}

	if !requireDBSSL(instance) {
		addEnv(corev1.EnvVar{
			Name:  "DO_NOT_REQUIRE_SSL",
			Value: "1",
		})
	}

	if s3.Enabled && s3.SecretRef != "" {
		appendSecretEnv := func(name, key string) {
			if key == "" {
				return
			}
			addEnv(corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: s3.SecretRef},
						Key:                  key,
					},
				},
			})
		}
		appendConfigEnv := func(name, key string) {
			if key == "" || s3.ConfigMapRef == "" {
				return
			}
			addEnv(corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: s3.ConfigMapRef},
						Key:                  key,
					},
				},
			})
		}
		appendValueEnv := func(name, value string) {
			if value == "" {
				return
			}
			addEnv(corev1.EnvVar{
				Name:  name,
				Value: value,
			})
		}
		appendInlineOrSecret := func(name, inline, key string) {
			if inline != "" {
				appendValueEnv(name, inline)
				return
			}
			appendSecretEnv(name, key)
		}
		appendInlineOrConfig := func(name, inline, key string) {
			if inline != "" {
				appendValueEnv(name, inline)
				return
			}
			appendConfigEnv(name, key)
		}
		appendBucketEnv := func(name string) {
			if s3.ConfigMapRef != "" {
				appendInlineOrConfig(name, s3.Bucket, s3.BucketKey)
				return
			}
			appendInlineOrSecret(name, s3.Bucket, s3.BucketKey)
		}

		appendSecretEnv("AWS_ACCESS_KEY_ID", s3.AccessKeyIDKey)
		appendSecretEnv("AWS_SECRET_ACCESS_KEY", s3.SecretAccessKeyKey)

		if s3.ConfigMapRef != "" {
			appendInlineOrConfig("AWS_REGION", s3.Region, s3.RegionKey)
		} else {
			appendInlineOrSecret("AWS_REGION", s3.Region, s3.RegionKey)
		}

		if s3.Endpoint != "" {
			appendValueEnv("AWS_ENDPOINT_URL_S3", s3.Endpoint)
			appendValueEnv("AWS_ENDPOINT_URL", s3.Endpoint)
			if s3.EmitS3EndpointUrl {
				appendValueEnv("S3_ENDPOINT_URL", s3.Endpoint)
			}
		} else if s3.ConfigMapRef != "" {
			if s3.EndpointHostKey != "" {
				const hostEnv = "CONVEX_S3_ENDPOINT_HOST"
				const portEnv = "CONVEX_S3_ENDPOINT_PORT"
				appendConfigEnv(hostEnv, s3.EndpointHostKey)
				endpointScheme := s3.EndpointScheme
				if endpointScheme == "" {
					endpointScheme = "http"
				}
				endpointValue := fmt.Sprintf("%s://$(%s)", endpointScheme, hostEnv)
				if s3.EndpointPortKey != "" {
					appendConfigEnv(portEnv, s3.EndpointPortKey)
					endpointValue = fmt.Sprintf("%s://$(%s):$(%s)", endpointScheme, hostEnv, portEnv)
				}
				appendValueEnv("AWS_ENDPOINT_URL_S3", endpointValue)
				appendValueEnv("AWS_ENDPOINT_URL", endpointValue)
				if s3.EmitS3EndpointUrl {
					appendValueEnv("S3_ENDPOINT_URL", endpointValue)
				}
			} else {
				appendConfigEnv("AWS_ENDPOINT_URL_S3", s3.EndpointKey)
				appendConfigEnv("AWS_ENDPOINT_URL", s3.EndpointKey)
				if s3.EmitS3EndpointUrl {
					appendConfigEnv("S3_ENDPOINT_URL", s3.EndpointKey)
				}
			}
		} else {
			appendSecretEnv("AWS_ENDPOINT_URL_S3", s3.EndpointKey)
			appendSecretEnv("AWS_ENDPOINT_URL", s3.EndpointKey)
			if s3.EmitS3EndpointUrl {
				appendSecretEnv("S3_ENDPOINT_URL", s3.EndpointKey)
			}
		}

		appendBucketEnv("S3_STORAGE_EXPORTS_BUCKET")
		appendBucketEnv("S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET")
		appendBucketEnv("S3_STORAGE_MODULES_BUCKET")
		appendBucketEnv("S3_STORAGE_FILES_BUCKET")
		appendBucketEnv("S3_STORAGE_SEARCH_BUCKET")
	}

	if instance.Spec.Backend.Telemetry.DisableBeacon {
		addEnv(corev1.EnvVar{
			Name:  "DISABLE_BEACON",
			Value: "true",
		})
	}
	if instance.Spec.Backend.Logging.RedactLogsToClient {
		addEnv(corev1.EnvVar{
			Name:  "REDACT_LOGS_TO_CLIENT",
			Value: "true",
		})
	}

	if len(instance.Spec.Backend.Env) > 0 {
		env = append(env, instance.Spec.Backend.Env...)
	}

	return deduplicateEnvs(env)
}

func envSignature(env []corev1.EnvVar) string {
	if len(env) == 0 {
		return ""
	}
	parts := make([]string, 0, len(env)*2)
	for _, e := range env {
		parts = append(parts, e.Name)
		switch {
		case e.Value != "":
			parts = append(parts, "val:"+e.Value)
		case e.ValueFrom != nil:
			if ref := e.ValueFrom.SecretKeyRef; ref != nil {
				parts = append(parts, fmt.Sprintf("secret:%s/%s", ref.Name, ref.Key))
			} else if ref := e.ValueFrom.ConfigMapKeyRef; ref != nil {
				parts = append(parts, fmt.Sprintf("cm:%s/%s", ref.Name, ref.Key))
			} else if ref := e.ValueFrom.FieldRef; ref != nil {
				parts = append(parts, fmt.Sprintf("field:%s", ref.FieldPath))
			} else if ref := e.ValueFrom.ResourceFieldRef; ref != nil {
				parts = append(parts, fmt.Sprintf("resource:%s", ref.Resource))
			} else {
				parts = append(parts, "valueFrom")
			}
		default:
			parts = append(parts, "empty")
		}
	}
	return configHash(parts[0], parts[1:]...)
}

func envResourceVersionSignature(ext externalSecretVersions) []string {
	parts := []string{}
	for name, rv := range ext.envSecretVersions {
		parts = append(parts, fmt.Sprintf("secret:%s:%s", name, rv))
	}
	for name, rv := range ext.envConfigVersions {
		parts = append(parts, fmt.Sprintf("config:%s:%s", name, rv))
	}
	sort.Strings(parts)
	return parts
}

func (r *ConvexInstanceReconciler) reconcileDashboardDeployment(ctx context.Context, instance *convexv1alpha1.ConvexInstance, secretName, generatedSecretRV, dashboardImage string) (bool, metav1.Condition, error) {
	name := dashboardDeploymentName(instance)
	key := client.ObjectKey{Name: name, Namespace: instance.Namespace}

	if !instance.Spec.Dashboard.Enabled {
		dep := &appsv1.Deployment{}
		if err := r.Get(ctx, key, dep); err == nil {
			if err := r.Delete(ctx, dep); err != nil && !errors.IsNotFound(err) {
				return false, metav1.Condition{}, err
			}
		} else if !errors.IsNotFound(err) {
			return false, metav1.Condition{}, err
		}
		return true, conditionTrue(conditionDashboard, "Disabled", "Dashboard disabled"), nil
	}

	replicas := int32(1)
	if instance.Spec.Dashboard.Replicas != nil {
		replicas = *instance.Spec.Dashboard.Replicas
	}
	if dashboardImage == "" {
		dashboardImage = instance.Spec.Dashboard.Image
	}

	labels := map[string]string{
		"app.kubernetes.io/name":      "convex-dashboard",
		"app.kubernetes.io/instance":  instance.Name,
		"app.kubernetes.io/component": "dashboard",
	}
	publicBackendURL := deploymentURL(instance)

	env := []corev1.EnvVar{
		{
			Name:  "NEXT_PUBLIC_DEPLOYMENT_URL",
			Value: publicBackendURL,
		},
	}
	if instance.Spec.Dashboard.PrefillAdminKey {
		env = append(env, corev1.EnvVar{
			Name: "NEXT_PUBLIC_ADMIN_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  adminKeyKey,
				},
			},
		})
	}
	if co := cloudOrigin(instance); co != "" {
		env = append(env, corev1.EnvVar{
			Name:  "CONVEX_CLOUD_ORIGIN",
			Value: co,
		})
	}
	if so := siteOrigin(instance); so != "" {
		env = append(env, corev1.EnvVar{
			Name:  "CONVEX_SITE_ORIGIN",
			Value: so,
		})
	}

	podSC, containerSC := dashboardSecurityContexts(instance)

	podSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Annotations: map[string]string{
				"convex.icod.de/dashboard-hash": configHash(publicBackendURL, generatedSecretRV),
			},
		},
		Spec: corev1.PodSpec{
			SecurityContext: podSC,
			Containers: []corev1.Container{
				{
					Name:            "dashboard",
					Image:           dashboardImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{
						Name:          defaultDashboardPortName,
						ContainerPort: defaultDashboardPort,
					}},
					Env:             env,
					Resources:       instance.Spec.Dashboard.Resources,
					SecurityContext: containerSC,
				},
			},
		},
	}

	depSpec := appsv1.DeploymentSpec{
		Replicas: &replicas,
		Selector: &metav1.LabelSelector{
			MatchLabels: labels,
		},
		Template: podSpec,
	}

	dep := &appsv1.Deployment{}
	err := r.Get(ctx, key, dep)
	if errors.IsNotFound(err) {
		dep = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: instance.Namespace,
			},
			Spec: depSpec,
		}
		if err := controllerutil.SetControllerReference(instance, dep, r.Scheme); err != nil {
			return false, metav1.Condition{}, err
		}
		if err := r.Create(ctx, dep); err != nil {
			return false, metav1.Condition{}, err
		}
		return false, conditionFalse(conditionDashboard, "Provisioning", "Waiting for dashboard rollout"), nil
	}
	if err != nil {
		return false, metav1.Condition{}, err
	}

	ownerChanged, err := ensureOwner(instance, dep, r.Scheme)
	if err != nil {
		return false, metav1.Condition{}, err
	}
	alignDeploymentDefaults(&dep.Spec, &depSpec)
	if ownerChanged || !deploymentSpecEqual(dep.Spec, depSpec) {
		dep.Spec = depSpec
		if err := r.Update(ctx, dep); err != nil {
			return false, metav1.Condition{}, err
		}
	}

	if deploymentReady(dep) {
		return true, conditionTrue(conditionDashboard, "Ready", "Dashboard deployment ready"), nil
	}
	return false, conditionFalse(conditionDashboard, "Provisioning", "Waiting for dashboard rollout"), nil
}

func (r *ConvexInstanceReconciler) reconcileStatefulSet(ctx context.Context, instance *convexv1alpha1.ConvexInstance, serviceName, secretName, generatedSecretRV, backendImage, backendVersion string, extVersions externalSecretVersions) (time.Duration, error) {
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKey{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}
	err := r.Get(ctx, key, sts)
	stsExists := err == nil
	if err != nil && !errors.IsNotFound(err) {
		return 0, err
	}

	now := time.Now().UTC()
	var plan restartPlan
	if stsExists {
		plan = computeRestartPlan(instance, sts, now)
	} else {
		plan = computeRestartPlan(instance, nil, now)
	}

	replicas := int32(1)
	if backendImage == "" {
		backendImage = instance.Spec.Backend.Image
	}
	if backendVersion == "" {
		backendVersion = instance.Spec.Version
	}
	labels := map[string]string{
		"app.kubernetes.io/name":      "convex-backend",
		"app.kubernetes.io/instance":  instance.Name,
		"app.kubernetes.io/component": "backend",
	}
	resolvedS3 := instance.Spec.Backend.S3
	if extVersions.s3Resolved.Enabled || extVersions.s3Resolved.SecretRef != "" || extVersions.s3Resolved.ConfigMapRef != "" {
		resolvedS3 = extVersions.s3Resolved
	}
	env := backendEnvWithS3(instance, backendVersion, secretName, resolvedS3)

	if len(env) > 0 {
		seen := map[string]bool{}
		dedup := make([]corev1.EnvVar, 0, len(env))
		for i := len(env) - 1; i >= 0; i-- {
			if seen[env[i].Name] {
				continue
			}
			seen[env[i].Name] = true
			dedup = append(dedup, env[i])
		}
		for i, j := 0, len(dedup)-1; i < j; i, j = i+1, j-1 {
			dedup[i], dedup[j] = dedup[j], dedup[i]
		}
		env = dedup
	}

	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: backendConfigMapName(instance),
					},
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "config",
			MountPath: "/etc/convex",
		},
	}
	if instance.Spec.Backend.Storage.Mode != storageModeExternal && instance.Spec.Backend.Storage.PVC.Enabled {
		volumes = append(volumes, corev1.Volume{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: backendPVCName(instance),
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "data",
			MountPath: "/var/lib/convex",
		})
	}

	podSC, containerSC := backendSecurityContexts(instance)
	podAnnotations := map[string]string{}

	podSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      labels,
			Annotations: podAnnotations,
		},
		Spec: corev1.PodSpec{
			SecurityContext: podSC,
			Containers: []corev1.Container{
				{
					Name:            "backend",
					Image:           backendImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{
						Name:          defaultBackendPortName,
						ContainerPort: defaultBackendPort,
					}, {
						Name:          actionPortName,
						ContainerPort: actionPort,
					}},
					Env:       env,
					Resources: instance.Spec.Backend.Resources,
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/",
								Port: intstr.FromInt(defaultBackendPort),
							},
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/",
								Port: intstr.FromInt(defaultBackendPort),
							},
						},
					},
					SecurityContext: containerSC,
					VolumeMounts:    volumeMounts,
				},
			},
			Volumes: volumes,
		},
	}
	hashValues := []string{
		generatedSecretRV,
		extVersions.dbResourceVersion,
		extVersions.s3ResourceVersion,
	}
	hashValues = append(hashValues, envResourceVersionSignature(extVersions)...)
	podAnnotations["convex.icod.de/config-hash"] = configHash(
		r.renderBackendConfig(instance, backendVersion),
		hashValues...,
	)
	if plan.desiredRestartTrigger != "" {
		podAnnotations[restartTriggerAnnotation] = plan.desiredRestartTrigger
	}

	stsSpec := appsv1.StatefulSetSpec{
		ServiceName: serviceName,
		Replicas:    &replicas,
		Selector: &metav1.LabelSelector{
			MatchLabels: labels,
		},
		Template: podSpec,
	}

	if !stsExists {
		var annotations map[string]string
		if plan.enabled {
			annotations = map[string]string{
				lastRestartAnnotation: plan.desiredLastRestart.Format(time.RFC3339),
			}
		}
		sts = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        backendStatefulSetName(instance),
				Namespace:   instance.Namespace,
				Annotations: annotations,
			},
			Spec: stsSpec,
		}
		if err := controllerutil.SetControllerReference(instance, sts, r.Scheme); err != nil {
			return 0, err
		}
		return plan.nextRestartIn, r.Create(ctx, sts)
	}

	ownerChanged, err := ensureOwner(instance, sts, r.Scheme)
	if err != nil {
		return 0, err
	}
	metaChanged := false
	if plan.enabled {
		if sts.Annotations == nil {
			sts.Annotations = map[string]string{}
		}
		desiredLast := plan.desiredLastRestart.Format(time.RFC3339)
		if sts.Annotations[lastRestartAnnotation] != desiredLast {
			sts.Annotations[lastRestartAnnotation] = desiredLast
			metaChanged = true
		}
	}
	alignStatefulSetDefaults(&sts.Spec, &stsSpec)
	if ownerChanged || metaChanged || !statefulSetSpecEqual(sts.Spec, stsSpec) {
		sts.Spec = stsSpec
		return plan.nextRestartIn, r.Update(ctx, sts)
	}
	return plan.nextRestartIn, nil
}

func (r *ConvexInstanceReconciler) reconcileExportJob(ctx context.Context, instance *convexv1alpha1.ConvexInstance, serviceName, secretName, image, upgradeHash string) (bool, error) {
	if image == "" {
		image = instance.Spec.Backend.Image
	}
	job := &batchv1.Job{}
	key := client.ObjectKey{Name: exportJobName(instance), Namespace: instance.Namespace}
	if err := r.Get(ctx, key, job); err != nil {
		if !errors.IsNotFound(err) {
			return false, err
		}
		podSpec := corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: upgradePVCName(instance),
					},
				},
			}},
			Containers: []corev1.Container{{
				Name:    "export",
				Image:   image,
				Command: []string{"/bin/sh", "-c", "convex export --url $API_URL --admin-key $ADMIN_KEY --output /data/export.tar.gz"},
				Env: []corev1.EnvVar{
					{
						Name:  "API_URL",
						Value: backendServiceURL(instance, serviceName),
					},
					{
						Name: "ADMIN_KEY",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
								Key:                  adminKeyKey,
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "data",
					MountPath: "/data",
				}},
			}},
		}
		job = &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      exportJobName(instance),
				Namespace: instance.Namespace,
				Annotations: map[string]string{
					upgradeHashAnnotation: upgradeHash,
				},
			},
			Spec: batchv1.JobSpec{
				BackoffLimit:            ptr.To(int32(3)),
				TTLSecondsAfterFinished: ptr.To(int32(300)),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/instance":  instance.Name,
							"app.kubernetes.io/component": "upgrade-export",
						},
					},
					Spec: podSpec,
				},
			},
		}
		if err := controllerutil.SetControllerReference(instance, job, r.Scheme); err != nil {
			return false, err
		}
		if err := r.Create(ctx, job); err != nil {
			return false, err
		}
		return false, nil
	}

	if job.Annotations == nil || job.Annotations[upgradeHashAnnotation] != upgradeHash {
		propagation := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
		return false, nil
	}

	if jobFailed(job) {
		propagation := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
		return false, fmt.Errorf("export job %q failed", job.Name)
	}
	if jobSucceeded(job) {
		return true, nil
	}
	return false, nil
}

func (r *ConvexInstanceReconciler) reconcileImportJob(ctx context.Context, instance *convexv1alpha1.ConvexInstance, serviceName, secretName, image, upgradeHash string) (bool, error) {
	if image == "" {
		image = instance.Spec.Backend.Image
	}
	job := &batchv1.Job{}
	key := client.ObjectKey{Name: importJobName(instance), Namespace: instance.Namespace}
	if err := r.Get(ctx, key, job); err != nil {
		if !errors.IsNotFound(err) {
			return false, err
		}
		podSpec := corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: upgradePVCName(instance),
					},
				},
			}},
			Containers: []corev1.Container{{
				Name:    "import",
				Image:   image,
				Command: []string{"/bin/sh", "-c", "convex import --url $API_URL --admin-key $ADMIN_KEY --input /data/export.tar.gz"},
				Env: []corev1.EnvVar{
					{
						Name:  "API_URL",
						Value: backendServiceURL(instance, serviceName),
					},
					{
						Name: "ADMIN_KEY",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
								Key:                  adminKeyKey,
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "data",
					MountPath: "/data",
				}},
			}},
		}
		job = &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      importJobName(instance),
				Namespace: instance.Namespace,
				Annotations: map[string]string{
					upgradeHashAnnotation: upgradeHash,
				},
			},
			Spec: batchv1.JobSpec{
				BackoffLimit:            ptr.To(int32(3)),
				TTLSecondsAfterFinished: ptr.To(int32(300)),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/instance":  instance.Name,
							"app.kubernetes.io/component": "upgrade-import",
						},
					},
					Spec: podSpec,
				},
			},
		}
		if err := controllerutil.SetControllerReference(instance, job, r.Scheme); err != nil {
			return false, err
		}
		if err := r.Create(ctx, job); err != nil {
			return false, err
		}
		return false, nil
	}

	if job.Annotations == nil || job.Annotations[upgradeHashAnnotation] != upgradeHash {
		propagation := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
		return false, nil
	}

	if jobFailed(job) {
		propagation := metav1.DeletePropagationBackground
		_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
		return false, fmt.Errorf("import job %q failed", job.Name)
	}
	if jobSucceeded(job) {
		return true, nil
	}
	return false, nil
}

func (r *ConvexInstanceReconciler) cleanupUpgradeArtifacts(ctx context.Context, instance *convexv1alpha1.ConvexInstance) {
	propagation := metav1.DeletePropagationBackground
	for _, name := range []string{exportJobName(instance), importJobName(instance)} {
		job := &batchv1.Job{}
		if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: instance.Namespace}, job); err == nil {
			_ = r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
		}
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: upgradePVCName(instance), Namespace: instance.Namespace}, pvc); err == nil {
		_ = r.Delete(ctx, pvc, &client.DeleteOptions{PropagationPolicy: &propagation})
	}
}

func (r *ConvexInstanceReconciler) deleteManagedGateway(ctx context.Context, instance *convexv1alpha1.ConvexInstance) error {
	gw := &gatewayv1.Gateway{}
	key := client.ObjectKey{Name: gatewayName(instance), Namespace: instance.Namespace}
	if err := r.Get(ctx, key, gw); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if owner := metav1.GetControllerOf(gw); owner != nil {
		if owner.UID == instance.UID || (owner.APIVersion == convexv1alpha1.GroupVersion.String() && owner.Kind == "ConvexInstance" && owner.Name == instance.Name) {
			return r.Delete(ctx, gw)
		}
	}
	return nil
}

func (r *ConvexInstanceReconciler) reconcileGateway(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (bool, metav1.Condition, error) {
	gw := &gatewayv1.Gateway{}
	key := client.ObjectKey{Name: gatewayName(instance), Namespace: instance.Namespace}
	spec := gatewayv1.GatewaySpec{
		GatewayClassName: gatewayv1.ObjectName(gatewayClassName(instance)),
		Listeners:        gatewayListeners(instance),
	}

	err := r.Get(ctx, key, gw)
	if errors.IsNotFound(err) {
		gw = &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:        gatewayName(instance),
				Namespace:   instance.Namespace,
				Annotations: gatewayAnnotations(instance),
			},
			Spec: spec,
		}
		if err := controllerutil.SetControllerReference(instance, gw, r.Scheme); err != nil {
			return false, metav1.Condition{}, err
		}
		if err := r.Create(ctx, gw); err != nil {
			return false, metav1.Condition{}, err
		}
		return false, conditionFalse(conditionGateway, "Provisioning", "Gateway created"), nil
	}
	if err != nil {
		return false, metav1.Condition{}, err
	}

	ownerChanged, err := ensureOwner(instance, gw, r.Scheme)
	if err != nil {
		return false, metav1.Condition{}, err
	}
	annotationsChanged := ensureGatewayAnnotations(gw, gatewayAnnotations(instance))
	if ownerChanged || annotationsChanged || !gatewaySpecEqual(gw.Spec, spec) {
		gw.Spec = spec
		if err := r.Update(ctx, gw); err != nil {
			return false, metav1.Condition{}, err
		}
	}

	if gatewayIsReady(gw) {
		return true, conditionTrue(conditionGateway, "Ready", "Gateway ready"), nil
	}
	return false, conditionFalse(conditionGateway, "Provisioning", "Waiting for Gateway readiness"), nil
}

func (r *ConvexInstanceReconciler) reconcileHTTPRoute(ctx context.Context, instance *convexv1alpha1.ConvexInstance, backendServiceName, dashboardServiceName string) (bool, metav1.Condition, error) {
	route := &gatewayv1.HTTPRoute{}
	key := client.ObjectKey{Name: httpRouteName(instance), Namespace: instance.Namespace}
	spec := gatewayv1.HTTPRouteSpec{
		CommonRouteSpec: gatewayv1.CommonRouteSpec{
			ParentRefs: routeParentRefs(instance),
		},
		Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(instance.Spec.Networking.Host)},
		Rules:     httpRouteRules(instance, backendServiceName, dashboardServiceName),
	}

	err := r.Get(ctx, key, route)
	if errors.IsNotFound(err) {
		route = &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      httpRouteName(instance),
				Namespace: instance.Namespace,
			},
			Spec: spec,
		}
		if err := controllerutil.SetControllerReference(instance, route, r.Scheme); err != nil {
			return false, metav1.Condition{}, err
		}
		if err := r.Create(ctx, route); err != nil {
			return false, metav1.Condition{}, err
		}
		return false, conditionFalse(conditionHTTPRoute, "Provisioning", "HTTPRoute created"), nil
	}
	if err != nil {
		return false, metav1.Condition{}, err
	}

	ownerChanged, err := ensureOwner(instance, route, r.Scheme)
	if err != nil {
		return false, metav1.Condition{}, err
	}
	if ownerChanged || !httpRouteSpecEqual(route.Spec, spec) {
		route.Spec = spec
		if err := r.Update(ctx, route); err != nil {
			return false, metav1.Condition{}, err
		}
	}

	readyCond := routeAccepted(route)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return true, conditionTrue(conditionHTTPRoute, "Ready", readyCond.Message), nil
	}

	msg := "Waiting for HTTPRoute acceptance"
	if readyCond != nil && readyCond.Message != "" {
		msg = readyCond.Message
	}
	return false, conditionFalse(conditionHTTPRoute, "Provisioning", msg), nil
}

func gatewayClassName(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Networking.GatewayClassName != "" {
		return instance.Spec.Networking.GatewayClassName
	}
	return defaultGatewayClassName
}

func gatewayAnnotations(instance *convexv1alpha1.ConvexInstance) map[string]string {
	if instance.Spec.Networking.GatewayAnnotations != nil {
		return copyStringMap(instance.Spec.Networking.GatewayAnnotations)
	}
	return defaultGatewayAnnotations()
}

func externalHostURL(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Networking.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", externalScheme(instance), instance.Spec.Networking.Host)
}

func deploymentURL(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Networking.DeploymentURL != "" {
		return instance.Spec.Networking.DeploymentURL
	}
	if url := externalHostURL(instance); url != "" {
		return url
	}
	return backendServiceURL(instance, backendServiceName(instance))
}

func cloudOrigin(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Networking.CloudOrigin != "" {
		return instance.Spec.Networking.CloudOrigin
	}
	return externalHostURL(instance)
}

func siteOrigin(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Networking.SiteOrigin != "" {
		return instance.Spec.Networking.SiteOrigin
	}
	return externalHostURL(instance)
}

func useCustomParentRefs(instance *convexv1alpha1.ConvexInstance) bool {
	return len(instance.Spec.Networking.ParentRefs) > 0
}

func routeParentRefs(instance *convexv1alpha1.ConvexInstance) []gatewayv1.ParentReference {
	if !useCustomParentRefs(instance) {
		return []gatewayv1.ParentReference{{
			Name:      gatewayv1.ObjectName(gatewayName(instance)),
			Namespace: ptr.To(gatewayv1.Namespace(instance.Namespace)),
		}}
	}
	refs := make([]gatewayv1.ParentReference, 0, len(instance.Spec.Networking.ParentRefs))
	for _, ref := range instance.Spec.Networking.ParentRefs {
		ns := ref.Namespace
		if ns == "" {
			ns = instance.Namespace
		}
		parent := gatewayv1.ParentReference{
			Name:      gatewayv1.ObjectName(ref.Name),
			Namespace: ptr.To(gatewayv1.Namespace(ns)),
		}
		if ref.SectionName != "" {
			parent.SectionName = ptr.To(gatewayv1.SectionName(ref.SectionName))
		}
		refs = append(refs, parent)
	}
	return refs
}

func gatewayListeners(instance *convexv1alpha1.ConvexInstance) []gatewayv1.Listener {
	hostname := gatewayv1.Hostname(instance.Spec.Networking.Host)
	allowedRoutes := &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{
			From: ptr.To(gatewayv1.NamespacesFromSame),
		},
	}
	if instance.Spec.Networking.TLSSecretRef != "" {
		return []gatewayv1.Listener{{
			Name:     "https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     gatewayv1.PortNumber(443),
			Hostname: ptr.To(hostname),
			TLS: &gatewayv1.GatewayTLSConfig{
				CertificateRefs: []gatewayv1.SecretObjectReference{{
					Kind:      ptr.To(gatewayv1.Kind("Secret")),
					Name:      gatewayv1.ObjectName(instance.Spec.Networking.TLSSecretRef),
					Namespace: ptr.To(gatewayv1.Namespace(instance.Namespace)),
				}},
			},
			AllowedRoutes: allowedRoutes,
		}}
	}
	return []gatewayv1.Listener{{
		Name:          "http",
		Protocol:      gatewayv1.HTTPProtocolType,
		Port:          gatewayv1.PortNumber(80),
		Hostname:      ptr.To(hostname),
		AllowedRoutes: allowedRoutes,
	}}
}

func httpRouteRules(instance *convexv1alpha1.ConvexInstance, backendServiceName, dashboardServiceName string) []gatewayv1.HTTPRouteRule {
	var rules []gatewayv1.HTTPRouteRule
	if instance.Spec.Dashboard.Enabled && dashboardServiceName != "" {
		rules = append(rules, gatewayv1.HTTPRouteRule{
			Matches: []gatewayv1.HTTPRouteMatch{
				{
					Path: &gatewayv1.HTTPPathMatch{
						Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
						Value: ptr.To("/dashboard"),
					},
				},
			},
			BackendRefs: []gatewayv1.HTTPBackendRef{{
				BackendRef: gatewayv1.BackendRef{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(dashboardServiceName),
						Port: ptr.To(gatewayv1.PortNumber(defaultDashboardPort)),
					},
				},
			}},
		})
	}

	actionRule := gatewayv1.HTTPRouteRule{
		Matches: []gatewayv1.HTTPRouteMatch{
			{
				Path: &gatewayv1.HTTPPathMatch{
					Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
					Value: ptr.To("/http_action/"),
				},
			},
		},
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName(backendServiceName),
					Port: ptr.To(gatewayv1.PortNumber(actionPort)),
				},
			},
		}},
	}

	backendMatches := []gatewayv1.HTTPRouteMatch{
		{
			Path: &gatewayv1.HTTPPathMatch{
				Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
				Value: ptr.To("/api/"),
			},
		},
		{
			Path: &gatewayv1.HTTPPathMatch{
				Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
				Value: ptr.To("/sync"),
			},
		},
		{
			Path: &gatewayv1.HTTPPathMatch{
				Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
				Value: ptr.To("/"),
			},
		},
	}

	rules = append(rules, actionRule, gatewayv1.HTTPRouteRule{
		Matches: backendMatches,
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName(backendServiceName),
					Port: ptr.To(gatewayv1.PortNumber(defaultBackendPort)),
				},
			},
		}},
	})
	return rules
}

func gatewayIsReady(gw *gatewayv1.Gateway) bool {
	readyCond := meta.FindStatusCondition(gw.Status.Conditions, string(gatewayv1.GatewayConditionReady))
	return readyCond != nil &&
		readyCond.Status == metav1.ConditionTrue &&
		readyCond.ObservedGeneration >= gw.Generation
}

func routeAccepted(route *gatewayv1.HTTPRoute) *metav1.Condition {
	for _, parent := range route.Status.Parents {
		cond := meta.FindStatusCondition(parent.Conditions, string(gatewayv1.RouteConditionAccepted))
		if cond != nil && cond.ObservedGeneration >= route.Generation {
			return cond
		}
	}
	return nil
}

func desiredUpgradeHash(instance *convexv1alpha1.ConvexInstance, env []corev1.EnvVar) string {
	dashboardImage := instance.Spec.Dashboard.Image
	if !instance.Spec.Dashboard.Enabled {
		dashboardImage = ""
	}
	envSig := envSignature(env)
	return configHash(
		instance.Spec.Version,
		instance.Spec.Backend.Image,
		dashboardImage,
		envSig,
	)
}

func observedUpgradeHash(instance *convexv1alpha1.ConvexInstance, currentVersion, currentBackendImage, currentDashboardImage string, currentEnv []corev1.EnvVar) string {
	if currentBackendImage == "" && currentDashboardImage == "" {
		return ""
	}
	if currentVersion == "" {
		currentVersion = instance.Spec.Version
	}
	envSig := envSignature(deduplicateEnvs(currentEnv))
	return configHash(currentVersion, currentBackendImage, currentDashboardImage, envSig)
}

func backendVersionFromStatefulSet(sts *appsv1.StatefulSet) string {
	for _, env := range sts.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "CONVEX_VERSION" {
			return env.Value
		}
	}
	return ""
}

func conditionTrueForGeneration(conditions []metav1.Condition, condType string, generation int64) bool {
	cond := meta.FindStatusCondition(conditions, condType)
	if cond == nil {
		return false
	}
	return cond.Status == metav1.ConditionTrue && cond.ObservedGeneration >= generation
}

func upgradePVCName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-%s", instance.Name, upgradePVCNameSuffix)
}

func exportJobName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-%s", instance.Name, upgradeExportJobSuffix)
}

func importJobName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-%s", instance.Name, upgradeImportJobSuffix)
}

func (r *ConvexInstanceReconciler) updateStatus(ctx context.Context, instance *convexv1alpha1.ConvexInstance, phase, reason, serviceName, message string, routeReady bool, upgradeHash string, conds ...metav1.Condition) error {
	current := instance.DeepCopy()
	current.Status.ObservedGeneration = instance.GetGeneration()
	current.Status.Phase = phase
	current.Status.Endpoints.APIURL = apiEndpoint(instance, serviceName, routeReady)
	if instance.Spec.Dashboard.Enabled {
		current.Status.Endpoints.DashboardURL = dashboardEndpoint(instance, routeReady)
	} else {
		current.Status.Endpoints.DashboardURL = ""
	}
	if upgradeHash == "" {
		upgradeHash = instance.Status.UpgradeHash
	}
	current.Status.UpgradeHash = upgradeHash
	conditionStatus := metav1.ConditionFalse
	if phase == phaseReady {
		conditionStatus = metav1.ConditionTrue
	}
	for i := range conds {
		conds[i].ObservedGeneration = instance.GetGeneration()
		meta.SetStatusCondition(&current.Status.Conditions, conds[i])
	}
	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: instance.GetGeneration(),
	})
	return r.Status().Update(ctx, current)
}

func (r *ConvexInstanceReconciler) updateStatusPhase(ctx context.Context, instance *convexv1alpha1.ConvexInstance, reason string, originalErr error, conds ...metav1.Condition) error {
	msg := ""
	if originalErr != nil {
		msg = originalErr.Error()
	}
	if err := r.updateStatus(ctx, instance, phaseError, reason, backendServiceName(instance), msg, false, instance.Status.UpgradeHash, conds...); err != nil {
		return err
	}
	return originalErr
}

func backendConfigMapName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-backend-config", instance.Name)
}

func generatedSecretName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-convex-secrets", instance.Name)
}

func backendPVCName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-backend-pvc", instance.Name)
}

func backendServiceName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-backend", instance.Name)
}

func backendStatefulSetName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-backend", instance.Name)
}

func dashboardDeploymentName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-dashboard", instance.Name)
}

func dashboardServiceName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-dashboard", instance.Name)
}

func gatewayName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-gateway", instance.Name)
}

func httpRouteName(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("%s-route", instance.Name)
}

func backendServiceURL(instance *convexv1alpha1.ConvexInstance, serviceName string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, instance.Namespace, defaultBackendPort)
}

func apiEndpoint(instance *convexv1alpha1.ConvexInstance, serviceName string, routeReady bool) string {
	if routeReady && instance.Spec.Networking.Host != "" {
		return fmt.Sprintf("%s://%s", externalScheme(instance), instance.Spec.Networking.Host)
	}
	return backendServiceURL(instance, serviceName)
}

func dashboardEndpoint(instance *convexv1alpha1.ConvexInstance, routeReady bool) string {
	if routeReady && instance.Spec.Dashboard.Enabled && instance.Spec.Networking.Host != "" {
		return fmt.Sprintf("%s://%s/dashboard", externalScheme(instance), instance.Spec.Networking.Host)
	}
	return ""
}

func ensureGatewayAnnotations(gw *gatewayv1.Gateway, desired map[string]string) bool {
	changed := false
	if gw.Annotations == nil {
		gw.Annotations = map[string]string{}
	}
	if _, wantDefault := desired[defaultGatewayIssuerKey]; !wantDefault {
		if _, hasDefault := gw.Annotations[defaultGatewayIssuerKey]; hasDefault {
			delete(gw.Annotations, defaultGatewayIssuerKey)
			changed = true
		}
	}
	for key, value := range desired {
		if gw.Annotations[key] != value {
			gw.Annotations[key] = value
			changed = true
		}
	}
	return changed
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func defaultGatewayAnnotations() map[string]string {
	return map[string]string{
		defaultGatewayIssuerKey: defaultGatewayIssuer,
	}
}

func externalScheme(instance *convexv1alpha1.ConvexInstance) string {
	if instance.Spec.Networking.TLSSecretRef != "" {
		return "https"
	}
	return "http"
}

func (r *ConvexInstanceReconciler) backendStatus(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (bool, metav1.Condition, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKey{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts); err != nil {
		if errors.IsNotFound(err) {
			return false, conditionFalse(conditionStatefulSet, "Provisioning", "Waiting for backend pod readiness"), nil
		}
		return false, metav1.Condition{}, err
	}
	observedUpToDate := sts.Status.ObservedGeneration >= sts.Generation &&
		sts.Status.UpdatedReplicas == ptr.Deref(sts.Spec.Replicas, 0) &&
		sts.Status.ReadyReplicas == sts.Status.UpdatedReplicas
	if observedUpToDate && sts.Status.ReadyReplicas > 0 {
		return true, conditionTrue(conditionStatefulSet, phaseReady, "Backend pod ready"), nil
	}
	return false, conditionFalse(conditionStatefulSet, "Provisioning", "Waiting for backend pod readiness"), nil
}

func ensureOwner(instance *convexv1alpha1.ConvexInstance, obj client.Object, scheme *runtime.Scheme) (bool, error) {
	if owner := metav1.GetControllerOf(obj); owner != nil {
		if owner.UID == instance.UID {
			return false, nil
		}
		if owner.APIVersion == convexv1alpha1.GroupVersion.String() && owner.Kind == "ConvexInstance" && owner.Name == instance.Name {
			// Adopt resources left behind by a previous instance with the same name.
			var refs []metav1.OwnerReference
			for _, ref := range obj.GetOwnerReferences() {
				if ref.Controller != nil && *ref.Controller {
					continue
				}
				refs = append(refs, ref)
			}
			obj.SetOwnerReferences(refs)
		} else {
			return false, fmt.Errorf("object %s/%s already owned by %s", obj.GetNamespace(), obj.GetName(), owner.Name)
		}
	}
	if err := controllerutil.SetControllerReference(instance, obj, scheme); err != nil {
		return false, err
	}
	return true, nil
}

func servicePortsEqual(a, b []corev1.ServicePort) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func selectorsEqual(a, b map[string]string) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func statefulSetSpecEqual(a, b appsv1.StatefulSetSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func deploymentSpecEqual(a, b appsv1.DeploymentSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func gatewaySpecEqual(a, b gatewayv1.GatewaySpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func httpRouteSpecEqual(a, b gatewayv1.HTTPRouteSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func jobSucceeded(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func jobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *ConvexInstanceReconciler) backendHasReadyReplica(ctx context.Context, instance *convexv1alpha1.ConvexInstance) bool {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKey{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts); err != nil {
		return false
	}
	return sts.Status.ReadyReplicas > 0
}

func (r *ConvexInstanceReconciler) handleUpgrade(ctx context.Context, instance *convexv1alpha1.ConvexInstance, plan upgradePlan, backendReady, dashboardReady, gatewayReady, routeReady bool, serviceName, secretName string, baseConds []metav1.Condition) (upgradeStatus, error) {
	status := upgradeStatus{
		phase:       phasePending,
		reason:      "WaitingForBackend",
		message:     "Waiting for backend readiness",
		appliedHash: "",
		conditions:  append([]metav1.Condition{}, baseConds...),
	}

	switch plan.strategy {
	case upgradeStrategyExport:
		if plan.backendChanged || plan.exportDone || plan.exportFailed || plan.importDone || plan.importFailed {
			return r.handleExportImport(ctx, instance, plan, backendReady, dashboardReady, gatewayReady, routeReady, serviceName, secretName, status)
		}
		return handleInPlace(plan, instance, backendReady, dashboardReady, gatewayReady, routeReady, status, func() {
			if plan.exportDone || plan.importDone {
				r.cleanupUpgradeArtifacts(ctx, instance)
			}
		}), nil
	default:
		return handleInPlace(plan, instance, backendReady, dashboardReady, gatewayReady, routeReady, status, func() {
			r.cleanupUpgradeArtifacts(ctx, instance)
		}), nil
	}
}

func handleInPlace(plan upgradePlan, instance *convexv1alpha1.ConvexInstance, backendReady, dashboardReady, gatewayReady, routeReady bool, status upgradeStatus, cleanup func()) upgradeStatus {
	readyAll := backendReady && (!instance.Spec.Dashboard.Enabled || dashboardReady) && gatewayReady && routeReady
	rolloutPending := plan.desiredHash != "" && plan.desiredHash == plan.appliedHash && !readyAll
	upgradeCond := conditionFalse(conditionUpgrade, "Idle", "No upgrade in progress")
	if plan.upgradePending || rolloutPending {
		status.phase = phaseUpgrading
		status.reason = conditionRollingUpdate
		status.message = "Rolling out new version"
		upgradeCond = conditionTrue(conditionUpgrade, "InProgress", "Upgrade in progress")
	}

	if readyAll {
		status.phase = phaseReady
		status.reason = conditionBackendReady
		status.message = msgBackendReady
		if plan.desiredHash != "" {
			status.appliedHash = plan.desiredHash
		}
		upgradeCond = conditionFalse(conditionUpgrade, "Idle", "No upgrade in progress")
		if cleanup != nil {
			cleanup()
		}
	} else {
		status.reason, status.message = readinessReason(instance, backendReady, dashboardReady, gatewayReady, routeReady)
	}

	status.conditions = append(status.conditions, upgradeCond)
	return status
}

func (r *ConvexInstanceReconciler) handleExportImport(ctx context.Context, instance *convexv1alpha1.ConvexInstance, plan upgradePlan, backendReady, dashboardReady, gatewayReady, routeReady bool, serviceName, secretName string, status upgradeStatus) (upgradeStatus, error) {
	readyAll := backendReady && (!instance.Spec.Dashboard.Enabled || dashboardReady) && gatewayReady && routeReady
	importPending := plan.exportDone && !plan.importDone

	upgradeCond := conditionFalse(conditionUpgrade, "Idle", "No upgrade in progress")
	exportCond := conditionFalse(conditionExport, "Pending", "Waiting for export job")
	importCond := conditionFalse(conditionImport, "Pending", "Waiting for import job")
	if plan.exportDone {
		exportCond = conditionTrue(conditionExport, "Completed", "Export job completed")
	}
	if plan.importDone {
		importCond = conditionTrue(conditionImport, "Completed", "Import job completed")
	}
	if plan.exportFailed {
		exportCond = conditionFalse(conditionExport, "Failed", "Export job failed")
	}
	if plan.importFailed {
		importCond = conditionFalse(conditionImport, "Failed", "Import job failed")
	}

	if !plan.upgradePending && !importPending {
		r.cleanupUpgradeArtifacts(ctx, instance)
		if readyAll {
			status.phase = phaseReady
			status.reason = conditionReady
			status.message = msgInstanceReady
			if plan.desiredHash != "" {
				status.appliedHash = plan.desiredHash
			}
		} else {
			status.reason, status.message = readinessReason(instance, backendReady, dashboardReady, gatewayReady, routeReady)
		}
		status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
		return status, nil
	}

	if err := r.reconcileUpgradePVC(ctx, instance); err != nil {
		return status, err
	}

	upgradeCond = conditionTrue(conditionUpgrade, "InProgress", "Upgrade in progress")
	exportComplete := plan.exportDone
	importComplete := plan.importDone

	if !backendReady && !r.backendHasReadyReplica(ctx, instance) {
		status.phase = phaseUpgrading
		status.reason, status.message = readinessReason(instance, backendReady, dashboardReady, gatewayReady, routeReady)
		status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
		return status, nil
	}

	if !exportComplete {
		complete, err := r.reconcileExportJob(ctx, instance, serviceName, secretName, plan.currentBackendImage, plan.desiredHash)
		if err != nil {
			status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
			return status, err
		}
		exportComplete = complete
	}
	if exportComplete {
		exportCond = conditionTrue(conditionExport, "Completed", "Export job completed")
	} else {
		status.phase = phaseUpgrading
		status.reason = "Exporting"
		status.message = "Running export job"
		status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
		return status, nil
	}

	if !backendReady {
		status.phase = phaseUpgrading
		status.reason = conditionRollingUpdate
		status.message = "Rolling out new version after export"
		status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
		return status, nil
	}
	if plan.currentBackendImage != instance.Spec.Backend.Image {
		status.phase = phaseUpgrading
		status.reason = conditionRollingUpdate
		status.message = "Rolling out new version after export"
		status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
		return status, nil
	}

	if !importComplete {
		complete, err := r.reconcileImportJob(ctx, instance, serviceName, secretName, instance.Spec.Backend.Image, plan.desiredHash)
		if err != nil {
			status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
			return status, err
		}
		importComplete = complete
	}
	if importComplete {
		importCond = conditionTrue(conditionImport, "Completed", "Import job completed")
	} else {
		status.phase = phaseUpgrading
		status.reason = "Importing"
		status.message = "Running import job"
		status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
		return status, nil
	}

	if readyAll && importComplete {
		status.phase = phaseReady
		status.reason = conditionBackendReady
		status.message = msgBackendReady
		status.appliedHash = plan.desiredHash
		upgradeCond = conditionFalse(conditionUpgrade, "Idle", "No upgrade in progress")
		r.cleanupUpgradeArtifacts(ctx, instance)
	} else {
		status.phase = phaseUpgrading
		status.reason, status.message = readinessReason(instance, backendReady, dashboardReady, gatewayReady, routeReady)
	}

	status.conditions = append(status.conditions, upgradeCond, exportCond, importCond)
	return status, nil
}

// alignStatefulSetDefaults copies defaults from the current spec into the desired spec to avoid spurious updates.
func alignStatefulSetDefaults(current, desired *appsv1.StatefulSetSpec) {
	if desired.RevisionHistoryLimit == nil && current.RevisionHistoryLimit != nil {
		desired.RevisionHistoryLimit = ptr.To(*current.RevisionHistoryLimit)
	}
	if desired.PodManagementPolicy == "" && current.PodManagementPolicy != "" {
		desired.PodManagementPolicy = current.PodManagementPolicy
	}
	if desired.UpdateStrategy.Type == "" && current.UpdateStrategy.Type != "" {
		desired.UpdateStrategy = *current.UpdateStrategy.DeepCopy()
	}
	alignPodSpecDefaults(&current.Template.Spec, &desired.Template.Spec)
}

// alignDeploymentDefaults copies defaults from the current spec into the desired spec to avoid spurious updates.
func alignDeploymentDefaults(current, desired *appsv1.DeploymentSpec) {
	if desired.RevisionHistoryLimit == nil && current.RevisionHistoryLimit != nil {
		desired.RevisionHistoryLimit = ptr.To(*current.RevisionHistoryLimit)
	}
	if desired.Strategy.Type == "" && current.Strategy.Type != "" {
		desired.Strategy = *current.Strategy.DeepCopy()
	}
	if desired.ProgressDeadlineSeconds == nil && current.ProgressDeadlineSeconds != nil {
		val := *current.ProgressDeadlineSeconds
		desired.ProgressDeadlineSeconds = &val
	}
	alignPodSpecDefaults(&current.Template.Spec, &desired.Template.Spec)
}

func alignPodSpecDefaults(current, desired *corev1.PodSpec) {
	if desired.RestartPolicy == "" && current.RestartPolicy != "" {
		desired.RestartPolicy = current.RestartPolicy
	}
	if desired.DNSPolicy == "" && current.DNSPolicy != "" {
		desired.DNSPolicy = current.DNSPolicy
	}
	if desired.SchedulerName == "" && current.SchedulerName != "" {
		desired.SchedulerName = current.SchedulerName
	}
	if desired.ServiceAccountName == "" && current.ServiceAccountName != "" {
		desired.ServiceAccountName = current.ServiceAccountName
	}
	if desired.TerminationGracePeriodSeconds == nil && current.TerminationGracePeriodSeconds != nil {
		val := *current.TerminationGracePeriodSeconds
		desired.TerminationGracePeriodSeconds = &val
	}
	if desired.EnableServiceLinks == nil && current.EnableServiceLinks != nil {
		val := *current.EnableServiceLinks
		desired.EnableServiceLinks = &val
	}
}

func deploymentReady(dep *appsv1.Deployment) bool {
	desired := ptr.Deref(dep.Spec.Replicas, int32(0))
	return dep.Status.ObservedGeneration >= dep.Generation &&
		dep.Status.UpdatedReplicas == desired &&
		dep.Status.ReadyReplicas == desired
}

func generateInstanceSecret() (string, error) {
	return randomHexString(instanceSecretBytes)
}

func randomHexString(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func normalizeInstanceSecret(value []byte) (string, bool) {
	if len(value) == 0 {
		return "", false
	}
	secret := strings.TrimSpace(string(value))
	if len(secret) != instanceSecretHexLen {
		return "", false
	}
	decoded, err := hex.DecodeString(secret)
	if err != nil || len(decoded) != instanceSecretBytes {
		return "", false
	}
	return secret, true
}

func adminKeyLooksValid(value []byte) bool {
	key := strings.TrimSpace(string(value))
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return false
	}
	return true
}

func generateAdminKey(instanceName, instanceSecret string) (string, error) {
	if instanceName == "" {
		return "", fmt.Errorf("admin key instance name is required")
	}
	secretBytes, err := hex.DecodeString(instanceSecret)
	if err != nil {
		return "", fmt.Errorf("instance secret must be hex: %w", err)
	}
	if len(secretBytes) != instanceSecretBytes {
		return "", fmt.Errorf("instance secret must be %d bytes", instanceSecretBytes)
	}
	derivedKey, err := deriveKBKDFCTR(secretBytes, []byte(adminKeyPurpose), 16)
	if err != nil {
		return "", err
	}
	aead, err := siv.NewGCM(derivedKey)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	issued := uint64(time.Now().Unix())
	proto := encodeAdminKeyProto(issued, adminKeyMemberID, false)
	ciphertext := aead.Seal(nil, nonce, proto, []byte{adminKeyVersion})
	buf := make([]byte, 0, 1+len(nonce)+len(ciphertext))
	buf = append(buf, adminKeyVersion)
	buf = append(buf, nonce...)
	buf = append(buf, ciphertext...)
	encoded := hex.EncodeToString(buf)
	return fmt.Sprintf("%s|%s", instanceName, encoded), nil
}

func encodeAdminKeyProto(issuedS uint64, memberID uint64, isReadOnly bool) []byte {
	buf := make([]byte, 0, 20)
	buf = appendVarintField(buf, 2, issuedS)
	buf = appendVarintField(buf, 3, memberID)
	if isReadOnly {
		buf = appendVarintField(buf, 5, 1)
	}
	return buf
}

func appendVarintField(buf []byte, fieldNum int, value uint64) []byte {
	tag := uint64(fieldNum << 3)
	buf = appendVarint(buf, tag)
	return appendVarint(buf, value)
}

func appendVarint(buf []byte, value uint64) []byte {
	for value >= 0x80 {
		buf = append(buf, byte(value)|0x80)
		value >>= 7
	}
	return append(buf, byte(value))
}

func deriveKBKDFCTR(key []byte, label []byte, outLen int) ([]byte, error) {
	if outLen <= 0 {
		return nil, fmt.Errorf("kbkdf output length must be positive")
	}
	lBits := uint32(outLen * 8)
	result := make([]byte, 0, outLen)
	counter := uint32(1)
	for len(result) < outLen {
		mac := hmac.New(sha256.New, key)
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], counter)
		mac.Write(buf[:])
		mac.Write(label)
		mac.Write([]byte{0})
		binary.BigEndian.PutUint32(buf[:], lBits)
		mac.Write(buf[:])
		result = append(result, mac.Sum(nil)...)
		counter++
	}
	return result[:outLen], nil
}

func (r *ConvexInstanceReconciler) recordEvent(instance *convexv1alpha1.ConvexInstance, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(instance, eventType, reason, message)
	}
}

func conditionTrue(condType, reason, message string) metav1.Condition {
	return condition(condType, reason, message, metav1.ConditionTrue)
}

func conditionFalse(condType, reason, message string) metav1.Condition {
	return condition(condType, reason, message, metav1.ConditionFalse)
}

func condition(condType, reason, message string, status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

func readinessReason(instance *convexv1alpha1.ConvexInstance, backendReady, dashboardReady, gatewayReady, routeReady bool) (string, string) {
	if !backendReady {
		return "WaitingForBackend", "Waiting for backend readiness"
	}
	if instance.Spec.Dashboard.Enabled && !dashboardReady {
		return "WaitingForDashboard", "Waiting for dashboard readiness"
	}
	if !gatewayReady || !routeReady {
		return "WaitingForGateway", "Waiting for Gateway/HTTPRoute readiness"
	}
	return "BackendReady", "Backend ready"
}

func (r *ConvexInstanceReconciler) reconcileCoreResources(ctx context.Context, instance *convexv1alpha1.ConvexInstance, plan upgradePlan, extVersions externalSecretVersions) (reconcileOutcome, *resourceErr) {
	result := reconcileOutcome{conds: []metav1.Condition{}}

	if err := r.reconcileConfigMap(ctx, instance, plan.effectiveVersion); err != nil {
		return result, &resourceErr{
			reason: "ConfigMapError",
			cond:   conditionFalse(conditionConfigMap, "ConfigMapError", err.Error()),
			err:    err,
		}
	}
	result.conds = append(result.conds, conditionTrue(conditionConfigMap, "Available", "Backend config rendered"))

	secretName, secretRV, err := r.reconcileSecrets(ctx, instance)
	if err != nil {
		return result, &resourceErr{
			reason: "SecretError",
			cond:   conditionFalse(conditionSecrets, "SecretError", err.Error()),
			err:    err,
		}
	}
	result.secretName = secretName
	result.secretRV = secretRV
	result.conds = append(result.conds, conditionTrue(conditionSecrets, "Available", "Instance/admin secrets ensured"))

	if err := r.reconcilePVC(ctx, instance); err != nil {
		return result, &resourceErr{
			reason: "PVCError",
			cond:   conditionFalse(conditionPVC, "PVCError", err.Error()),
			err:    err,
		}
	}
	if instance.Spec.Backend.Storage.Mode == storageModeExternal || !instance.Spec.Backend.Storage.PVC.Enabled {
		result.conds = append(result.conds, conditionTrue(conditionPVC, "Skipped", "PVC not required"))
	} else {
		result.conds = append(result.conds, conditionTrue(conditionPVC, "Available", "PVC ensured"))
	}

	serviceName, err := r.reconcileService(ctx, instance)
	if err != nil {
		return result, &resourceErr{
			reason: "ServiceError",
			cond:   conditionFalse(conditionService, "ServiceError", err.Error()),
			err:    err,
		}
	}
	result.serviceName = serviceName
	result.conds = append(result.conds, conditionTrue(conditionService, "Available", "Backend service ready"))

	dashSvcName, dashSvcCond, err := r.reconcileDashboardService(ctx, instance)
	if err != nil {
		return result, &resourceErr{
			reason: "DashboardServiceError",
			cond:   conditionFalse(conditionDashboardSvc, "DashboardServiceError", err.Error()),
			err:    err,
		}
	}
	result.dashboardServiceName = dashSvcName
	result.conds = append(result.conds, dashSvcCond)

	dashboardReady, dashboardCond, err := r.reconcileDashboardDeployment(ctx, instance, secretName, secretRV, plan.effectiveDashboardImage)
	if err != nil {
		return result, &resourceErr{
			reason: "DashboardError",
			cond:   conditionFalse(conditionDashboard, "DashboardError", err.Error()),
			err:    err,
		}
	}
	result.dashboardReady = dashboardReady
	result.conds = append(result.conds, dashboardCond)

	nextRestartIn, err := r.reconcileStatefulSet(ctx, instance, serviceName, secretName, secretRV, plan.effectiveBackendImage, plan.effectiveVersion, extVersions)
	if err != nil {
		return result, &resourceErr{
			reason: "StatefulSetError",
			cond:   conditionFalse(conditionStatefulSet, "StatefulSetError", err.Error()),
			err:    err,
		}
	}
	result.nextRestartIn = nextRestartIn

	if useCustomParentRefs(instance) {
		if err := r.deleteManagedGateway(ctx, instance); err != nil {
			return result, &resourceErr{
				reason: "GatewayError",
				cond:   conditionFalse(conditionGateway, "GatewayError", err.Error()),
				err:    err,
			}
		}
		result.gatewayReady = true
		result.conds = append(result.conds, conditionTrue(conditionGateway, "Skipped", "Using provided parentRefs"))
	} else {
		gatewayReady, gatewayCond, err := r.reconcileGateway(ctx, instance)
		if err != nil {
			return result, &resourceErr{
				reason: "GatewayError",
				cond:   conditionFalse(conditionGateway, "GatewayError", err.Error()),
				err:    err,
			}
		}
		result.gatewayReady = gatewayReady
		result.conds = append(result.conds, gatewayCond)
	}

	routeReady, routeCond, err := r.reconcileHTTPRoute(ctx, instance, serviceName, dashSvcName)
	if err != nil {
		return result, &resourceErr{
			reason: "HTTPRouteError",
			cond:   conditionFalse(conditionHTTPRoute, "HTTPRouteError", err.Error()),
			err:    err,
		}
	}
	result.routeReady = routeReady
	result.conds = append(result.conds, routeCond)

	backendReady, backendCond, err := r.backendStatus(ctx, instance)
	if err != nil {
		return result, &resourceErr{
			reason: "StatefulSetError",
			cond:   conditionFalse(conditionStatefulSet, "StatefulSetError", err.Error()),
			err:    err,
		}
	}
	result.backendReady = backendReady
	result.conds = append(result.conds, backendCond)

	return result, nil
}
func buildUpgradePlan(instance *convexv1alpha1.ConvexInstance, backendExists bool, currentBackendImage, currentDashboardImage, currentBackendVersion string, currentBackendEnv []corev1.EnvVar, exportSucceeded, importSucceeded, exportFailed, importFailed bool, desiredHash string) upgradePlan {
	strategy := instance.Spec.Maintenance.UpgradeStrategy
	if strategy == "" {
		strategy = upgradeStrategyInPlace
	}
	exportDone := conditionTrueForGeneration(instance.Status.Conditions, conditionExport, instance.GetGeneration())
	importDone := conditionTrueForGeneration(instance.Status.Conditions, conditionImport, instance.GetGeneration())
	if desiredHash != instance.Status.UpgradeHash {
		exportDone = false
		importDone = false
	}
	if exportSucceeded {
		exportDone = true
	}
	if importSucceeded {
		importDone = true
	}
	if exportFailed {
		exportDone = false
	}
	if importFailed {
		importDone = false
	}
	appliedHash := observedUpgradeHash(instance, currentBackendVersion, currentBackendImage, currentDashboardImage, currentBackendEnv)
	upgradePlanned := backendExists && desiredHash != appliedHash
	upgradePending := backendExists && (desiredHash != appliedHash || (exportDone && !importDone))

	backendChanged := backendExists && (instance.Spec.Backend.Image != currentBackendImage || instance.Spec.Version != currentBackendVersion)
	dashboardChanged := instance.Spec.Dashboard.Image != "" && instance.Spec.Dashboard.Image != currentDashboardImage

	backendImage := instance.Spec.Backend.Image
	dashboardImage := instance.Spec.Dashboard.Image
	backendVersion := instance.Spec.Version
	if strategy == upgradeStrategyExport && backendExists && !exportDone {
		if currentBackendImage != "" {
			backendImage = currentBackendImage
		}
		if currentBackendVersion != "" {
			backendVersion = currentBackendVersion
		}
	}

	return upgradePlan{
		strategy:                strategy,
		desiredHash:             desiredHash,
		appliedHash:             appliedHash,
		upgradePending:          upgradePending,
		upgradePlanned:          upgradePlanned,
		exportDone:              exportDone,
		importDone:              importDone,
		exportFailed:            exportFailed,
		importFailed:            importFailed,
		backendChanged:          backendChanged,
		dashboardChanged:        dashboardChanged,
		effectiveBackendImage:   backendImage,
		effectiveDashboardImage: dashboardImage,
		effectiveVersion:        backendVersion,
		currentBackendImage:     currentBackendImage,
		currentDashboardImage:   currentDashboardImage,
		currentBackendVersion:   currentBackendVersion,
	}
}
