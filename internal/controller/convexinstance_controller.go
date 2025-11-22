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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
)

// ConvexInstanceReconciler reconciles a ConvexInstance object
type ConvexInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=convex.icod.de,resources=convexinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=convex.icod.de,resources=convexinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=convex.icod.de,resources=convexinstances/finalizers,verbs=update

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

	updated := instance.DeepCopy()
	updated.Status.ObservedGeneration = instance.GetGeneration()
	if updated.Status.Phase == "" {
		updated.Status.Phase = "Pending"
	}

	if err := r.Status().Patch(ctx, updated, client.MergeFrom(instance)); err != nil {
		if errors.IsConflict(err) {
			log.V(1).Info("status conflict, will retry", "name", req.NamespacedName)
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConvexInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&convexv1alpha1.ConvexInstance{}).
		Named("convexinstance").
		Complete(r)
}
