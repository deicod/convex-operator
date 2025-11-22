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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConvexInstanceSpec defines the desired state of ConvexInstance.
type ConvexInstanceSpec struct {
	// Environment describes the deployment tier.
	// +kubebuilder:validation:Enum=dev;prod
	Environment string `json:"environment"`

	// Version is the Convex release tag used by backend and dashboard images.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Backend configures the Convex backend process.
	Backend BackendSpec `json:"backend"`

	// Dashboard configures the optional Convex dashboard.
	// +kubebuilder:default:={}
	Dashboard DashboardSpec `json:"dashboard,omitempty"`

	// Networking configures ingress and TLS wiring.
	Networking NetworkingSpec `json:"networking"`

	// Scale captures resource scaling preferences.
	// +optional
	Scale ScaleSpec `json:"scale,omitempty"`

	// Maintenance configures upgrade handling.
	// +optional
	Maintenance MaintenanceSpec `json:"maintenance,omitempty"`
}

// BackendSpec defines backend image, resources, and data wiring.
type BackendSpec struct {
	// +kubebuilder:default:="ghcr.io/get-convex/convex-backend:latest"
	Image string `json:"image,omitempty"`

	// Resources configures requests/limits applied to the backend StatefulSet.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// DB references the database connection secret and driver.
	DB BackendDatabaseSpec `json:"db"`

	// Storage configures local or external storage options.
	// +kubebuilder:default:={}
	Storage BackendStorageSpec `json:"storage,omitempty"`

	// S3 configures S3-compatible storage for blob usage.
	// +kubebuilder:default:={}
	S3 BackendS3Spec `json:"s3,omitempty"`
}

// BackendDatabaseSpec describes DB settings and secret references.
type BackendDatabaseSpec struct {
	// +kubebuilder:validation:Enum=postgres;mysql;sqlite
	Engine string `json:"engine"`

	// SecretRef names the Secret holding the DB connection URL.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// URLKey is the key inside the Secret containing the DB URL.
	// +optional
	URLKey string `json:"urlKey,omitempty"`
}

// BackendStorageSpec defines storage settings for Convex.
type BackendStorageSpec struct {
	// Mode selects SQLite (local) or external storage wiring.
	// +kubebuilder:default:=sqlite
	// +kubebuilder:validation:Enum=sqlite;external
	Mode string `json:"mode,omitempty"`

	// PVC manages the persistent volume claim when using SQLite/local storage.
	// +optional
	PVC BackendPVCSpec `json:"pvc,omitempty"`
}

// BackendPVCSpec configures the PVC used by the backend StatefulSet.
type BackendPVCSpec struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// Size is the requested capacity (e.g., "100Gi").
	// +optional
	Size resource.Quantity `json:"size,omitempty"`
}

// BackendS3Spec configures S3-compatible storage wiring.
type BackendS3Spec struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef names the Secret containing S3 connection settings.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// +optional
	EndpointKey string `json:"endpointKey,omitempty"`

	// +optional
	AccessKeyIDKey string `json:"accessKeyIdKey,omitempty"`

	// +optional
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`

	// +optional
	BucketKey string `json:"bucketKey,omitempty"`
}

// DashboardSpec configures the Convex dashboard deployment.
type DashboardSpec struct {
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`

	// +kubebuilder:default:="ghcr.io/get-convex/convex-dashboard:latest"
	Image string `json:"image,omitempty"`

	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// NetworkingSpec captures external routing details.
type NetworkingSpec struct {
	// Host is the external hostname routed to backend/dashboard.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// TLSSecretRef names the TLS secret used by Gateway/Ingress.
	// +optional
	TLSSecretRef string `json:"tlsSecretRef,omitempty"`
}

// ScaleSpec defines scaling hints for the backend.
type ScaleSpec struct {
	// Backend defines CPU-based scaling preferences.
	// +optional
	Backend BackendScaleSpec `json:"backend,omitempty"`
}

// BackendScaleSpec captures autoscaling hints.
type BackendScaleSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	CPUTargetUtilization *int32 `json:"cpuTargetUtilization,omitempty"`

	// MaxMemory sets an upper bound for memory allocations (e.g., "8Gi").
	// +optional
	MaxMemory resource.Quantity `json:"maxMemory,omitempty"`
}

// MaintenanceSpec defines upgrade behavior.
type MaintenanceSpec struct {
	// +kubebuilder:default:=inPlace
	// +kubebuilder:validation:Enum=inPlace;exportImport
	UpgradeStrategy string `json:"upgradeStrategy,omitempty"`
}

// InstanceEndpoints describes externally reachable URLs.
type InstanceEndpoints struct {
	// +optional
	APIURL string `json:"apiUrl,omitempty"`

	// +optional
	DashboardURL string `json:"dashboardUrl,omitempty"`
}

// ConvexInstanceStatus defines the observed state of ConvexInstance.
type ConvexInstanceStatus struct {
	// Phase is a coarse-grained lifecycle indicator.
	// +kubebuilder:validation:Enum=Pending;Ready;Error;Upgrading
	// +optional
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration reflects the last processed spec generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Endpoints captures external URLs.
	// +optional
	Endpoints InstanceEndpoints `json:"endpoints,omitempty"`

	// Conditions represent the current state of the ConvexInstance resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ConvexInstance is the Schema for the convexinstances API.
type ConvexInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConvexInstanceSpec   `json:"spec"`
	Status ConvexInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConvexInstanceList contains a list of ConvexInstance.
type ConvexInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConvexInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ConvexInstance{}, &ConvexInstanceList{})
}
