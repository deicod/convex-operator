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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
)

const (
	finalizerName          = "convex.icod.de/finalizer"
	adminKeyKey            = "adminKey"
	instanceSecretKey      = "instanceSecret"
	defaultBackendPortName = "http"
	defaultBackendPort     = 3210
	actionPortName         = "http-action"
	actionPort             = 3211
	configMapKey           = "convex.conf"
	conditionReady         = "Ready"
	conditionConfigMap     = "ConfigMapReady"
	conditionSecrets       = "SecretsReady"
	conditionPVC           = "PVCReady"
	conditionService       = "ServiceReady"
	conditionStatefulSet   = "StatefulSetReady"
	phasePending           = "Pending"
	phaseReady             = "Ready"
	phaseError             = "Error"
	storageModeExternal    = "external"
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
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ConvexInstance object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *ConvexInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &convexv1alpha1.ConvexInstance{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	oldPhase := instance.Status.Phase

	if instance.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(instance, finalizerName) {
			controllerutil.AddFinalizer(instance, finalizerName)
			if err := r.Update(ctx, instance); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		controllerutil.RemoveFinalizer(instance, finalizerName)
		if err := r.Update(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	conds := []metav1.Condition{}

	if err := r.validateExternalRefs(ctx, instance); err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "ValidationFailed", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "ValidationFailed", err, conditionFalse(conditionSecrets, "ValidationFailed", err.Error()))
	}

	if err := r.reconcileConfigMap(ctx, instance); err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "ConfigMapError", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "ConfigMapError", err, conditionFalse(conditionConfigMap, "ConfigMapError", err.Error()))
	}
	conds = append(conds, conditionTrue(conditionConfigMap, "Available", "Backend config rendered"))

	secretName, err := r.reconcileSecrets(ctx, instance)
	if err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "SecretError", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "SecretError", err, conditionFalse(conditionSecrets, "SecretError", err.Error()))
	}
	conds = append(conds, conditionTrue(conditionSecrets, "Available", "Instance/admin secrets ensured"))

	if err := r.reconcilePVC(ctx, instance); err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "PVCError", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "PVCError", err, conditionFalse(conditionPVC, "PVCError", err.Error()))
	}
	if instance.Spec.Backend.Storage.Mode == storageModeExternal || !instance.Spec.Backend.Storage.PVC.Enabled {
		conds = append(conds, conditionTrue(conditionPVC, "Skipped", "PVC not required"))
	} else {
		conds = append(conds, conditionTrue(conditionPVC, "Available", "PVC ensured"))
	}

	serviceName, err := r.reconcileService(ctx, instance)
	if err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "ServiceError", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "ServiceError", err, conditionFalse(conditionService, "ServiceError", err.Error()))
	}
	conds = append(conds, conditionTrue(conditionService, "Available", "Backend service ready"))

	if err := r.reconcileStatefulSet(ctx, instance, serviceName, secretName); err != nil {
		r.recordEvent(instance, corev1.EventTypeWarning, "StatefulSetError", err.Error())
		return ctrl.Result{}, r.updateStatusPhase(ctx, instance, "StatefulSetError", err, conditionFalse(conditionStatefulSet, "StatefulSetError", err.Error()))
	}

	phase := phasePending
	conditionReason := "WaitingForBackend"
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKey{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}, sts); err == nil {
		if sts.Status.ReadyReplicas > 0 {
			phase = phaseReady
			conditionReason = "BackendReady"
			conds = append(conds, conditionTrue(conditionStatefulSet, phaseReady, "Backend pod ready"))
		} else {
			conds = append(conds, conditionFalse(conditionStatefulSet, "Provisioning", "Waiting for backend pod readiness"))
		}
	}

	statusMsg := "Waiting for backend readiness"
	if phase == phaseReady {
		statusMsg = "Backend ready"
	}
	if err := r.updateStatus(ctx, instance, phase, conditionReason, serviceName, statusMsg, conds...); err != nil {
		if errors.IsConflict(err) {
			log.V(1).Info("status conflict, will retry", "name", req.NamespacedName)
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	if phase == phaseReady && oldPhase != phaseReady {
		r.recordEvent(instance, corev1.EventTypeNormal, conditionReady, "Backend is ready")
	}

	if phase != phaseReady {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConvexInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&convexv1alpha1.ConvexInstance{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("convexinstance").
		Complete(r)
}

func (r *ConvexInstanceReconciler) validateExternalRefs(ctx context.Context, instance *convexv1alpha1.ConvexInstance) error {
	db := instance.Spec.Backend.DB
	if db.Engine != "sqlite" && db.SecretRef == "" {
		return fmt.Errorf("db secret is required for engine %q", db.Engine)
	}
	if db.Engine != "sqlite" && db.URLKey == "" {
		return fmt.Errorf("db urlKey is required for engine %q", db.Engine)
	}
	if ref := db.SecretRef; ref != "" {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: ref, Namespace: instance.Namespace}, secret); err != nil {
			return fmt.Errorf("db secret %q: %w", ref, err)
		}
		if db.URLKey != "" {
			if _, ok := secret.Data[db.URLKey]; !ok {
				return fmt.Errorf("db secret %q missing key %q", ref, db.URLKey)
			}
		}
	}
	if instance.Spec.Backend.S3.Enabled {
		if instance.Spec.Backend.S3.SecretRef == "" {
			return fmt.Errorf("s3 secret is required when s3 is enabled")
		}
		requiredS3Keys := map[string]string{
			"endpointKey":        instance.Spec.Backend.S3.EndpointKey,
			"accessKeyIdKey":     instance.Spec.Backend.S3.AccessKeyIDKey,
			"secretAccessKeyKey": instance.Spec.Backend.S3.SecretAccessKeyKey,
			"bucketKey":          instance.Spec.Backend.S3.BucketKey,
		}
		for field, val := range requiredS3Keys {
			if val == "" {
				return fmt.Errorf("s3 %s is required when s3 is enabled", field)
			}
		}
		if ref := instance.Spec.Backend.S3.SecretRef; ref != "" {
			secret := &corev1.Secret{}
			if err := r.Get(ctx, client.ObjectKey{Name: ref, Namespace: instance.Namespace}, secret); err != nil {
				return fmt.Errorf("s3 secret %q: %w", ref, err)
			}
			keys := []string{
				instance.Spec.Backend.S3.EndpointKey,
				instance.Spec.Backend.S3.AccessKeyIDKey,
				instance.Spec.Backend.S3.SecretAccessKeyKey,
				instance.Spec.Backend.S3.BucketKey,
			}
			for _, key := range keys {
				if key == "" {
					continue
				}
				if _, ok := secret.Data[key]; !ok {
					return fmt.Errorf("s3 secret %q missing key %q", ref, key)
				}
			}
		}
	}
	return nil
}

func (r *ConvexInstanceReconciler) reconcileConfigMap(ctx context.Context, instance *convexv1alpha1.ConvexInstance) error {
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
				configMapKey: r.renderBackendConfig(instance),
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

	expected := r.renderBackendConfig(instance)
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

func (r *ConvexInstanceReconciler) renderBackendConfig(instance *convexv1alpha1.ConvexInstance) string {
	return fmt.Sprintf("CONVEX_PORT=%d\nCONVEX_ENV=%s\nCONVEX_VERSION=%s\n", defaultBackendPort, instance.Spec.Environment, instance.Spec.Version)
}

func configHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (r *ConvexInstanceReconciler) reconcileSecrets(ctx context.Context, instance *convexv1alpha1.ConvexInstance) (string, error) {
	secretName := generatedSecretName(instance)
	secret := &corev1.Secret{}
	key := client.ObjectKey{Name: secretName, Namespace: instance.Namespace}
	err := r.Get(ctx, key, secret)
	if errors.IsNotFound(err) {
		adminKey, err := randomString(24)
		if err != nil {
			return "", err
		}
		instanceSecret, err := randomString(24)
		if err != nil {
			return "", err
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
			return "", err
		}
		return secretName, r.Create(ctx, secret)
	}
	if err != nil {
		return "", err
	}
	ownerChanged, err := ensureOwner(instance, secret, r.Scheme)
	if err != nil {
		return "", err
	}
	if ownerChanged {
		return secretName, r.Update(ctx, secret)
	}
	return secretName, nil
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
	if !storageClassEqual(desiredSC, pvc.Spec.StorageClassName) {
		pvc.Spec.StorageClassName = desiredSC
		needsUpdate = true
	}
	if needsUpdate || ownerChanged {
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

func (r *ConvexInstanceReconciler) reconcileStatefulSet(ctx context.Context, instance *convexv1alpha1.ConvexInstance, serviceName, secretName string) error {
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKey{Name: backendStatefulSetName(instance), Namespace: instance.Namespace}
	err := r.Get(ctx, key, sts)
	replicas := int32(1)
	labels := map[string]string{
		"app.kubernetes.io/name":      "convex-backend",
		"app.kubernetes.io/instance":  instance.Name,
		"app.kubernetes.io/component": "backend",
	}
	env := []corev1.EnvVar{
		{
			Name:  "CONVEX_PORT",
			Value: fmt.Sprintf("%d", defaultBackendPort),
		},
		{
			Name:  "CONVEX_ENV",
			Value: instance.Spec.Environment,
		},
		{
			Name:  "CONVEX_VERSION",
			Value: instance.Spec.Version,
		},
		{
			Name: "CONVEX_ADMIN_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  adminKeyKey,
				},
			},
		},
		{
			Name: "CONVEX_INSTANCE_SECRET",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  instanceSecretKey,
				},
			},
		},
	}

	if ref := instance.Spec.Backend.DB.SecretRef; ref != "" && instance.Spec.Backend.DB.URLKey != "" {
		env = append(env, corev1.EnvVar{
			Name: "CONVEX_DB_URL",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ref},
					Key:                  instance.Spec.Backend.DB.URLKey,
				},
			},
		})
	}

	if instance.Spec.Backend.S3.Enabled && instance.Spec.Backend.S3.SecretRef != "" {
		s3 := instance.Spec.Backend.S3
		appendS3Env := func(name, key string) {
			if key == "" {
				return
			}
			env = append(env, corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: s3.SecretRef},
						Key:                  key,
					},
				},
			})
		}
		appendS3Env("CONVEX_S3_ENDPOINT", s3.EndpointKey)
		appendS3Env("CONVEX_S3_ACCESS_KEY_ID", s3.AccessKeyIDKey)
		appendS3Env("CONVEX_S3_SECRET_ACCESS_KEY", s3.SecretAccessKeyKey)
		appendS3Env("CONVEX_S3_BUCKET", s3.BucketKey)
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

	podSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Annotations: map[string]string{
				"convex.icod.de/config-hash": configHash(r.renderBackendConfig(instance)),
			},
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true),
			},
			Containers: []corev1.Container{
				{
					Name:            "backend",
					Image:           instance.Spec.Backend.Image,
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
					SecurityContext: &corev1.SecurityContext{
						RunAsNonRoot: ptr.To(true),
					},
					VolumeMounts: volumeMounts,
				},
			},
			Volumes: volumes,
		},
	}

	stsSpec := appsv1.StatefulSetSpec{
		ServiceName: serviceName,
		Replicas:    &replicas,
		Selector: &metav1.LabelSelector{
			MatchLabels: labels,
		},
		Template: podSpec,
	}

	if errors.IsNotFound(err) {
		sts = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendStatefulSetName(instance),
				Namespace: instance.Namespace,
			},
			Spec: stsSpec,
		}
		if err := controllerutil.SetControllerReference(instance, sts, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, sts)
	}
	if err != nil {
		return err
	}

	ownerChanged, err := ensureOwner(instance, sts, r.Scheme)
	if err != nil {
		return err
	}
	alignStatefulSetDefaults(&sts.Spec, &stsSpec)
	if ownerChanged || !statefulSetSpecEqual(sts.Spec, stsSpec) {
		sts.Spec = stsSpec
		return r.Update(ctx, sts)
	}
	return nil
}

func (r *ConvexInstanceReconciler) updateStatus(ctx context.Context, instance *convexv1alpha1.ConvexInstance, phase, reason, serviceName, message string, conds ...metav1.Condition) error {
	current := instance.DeepCopy()
	current.Status.ObservedGeneration = instance.GetGeneration()
	current.Status.Phase = phase
	current.Status.Endpoints.APIURL = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, instance.Namespace, defaultBackendPort)
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
	if err := r.updateStatus(ctx, instance, phaseError, reason, backendServiceName(instance), msg, conds...); err != nil {
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
	return reflect.DeepEqual(a, b)
}

func selectorsEqual(a, b map[string]string) bool {
	return reflect.DeepEqual(a, b)
}

func statefulSetSpecEqual(a, b appsv1.StatefulSetSpec) bool {
	return reflect.DeepEqual(a, b)
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
}

func randomString(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
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
