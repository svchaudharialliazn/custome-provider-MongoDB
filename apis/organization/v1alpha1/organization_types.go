// apis/organization/v1alpha1/organization_types.go
package v1alpha1

import (
    "reflect"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime/schema"

    xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// ===============================================================
// Secret Storage Configuration (Dual Storage Support)
// ===============================================================

// SecretStorageType defines where organization API keys are stored
// +kubebuilder:validation:Enum=AWS;Kubernetes
type SecretStorageType string

const (
    // SecretStorageTypeAWS stores secrets in AWS Secrets Manager
    SecretStorageTypeAWS SecretStorageType = "AWS"

    // SecretStorageTypeKubernetes stores secrets in Kubernetes Secrets
    SecretStorageTypeKubernetes SecretStorageType = "Kubernetes"
)

// KubernetesSecretReference defines Kubernetes Secret storage configuration
type KubernetesSecretReference struct {
    // Name of the Kubernetes Secret (optional, defaults to {org-name}-mongodb-credentials)
    // +optional
    Name *string `json:"name,omitempty"`

    // Namespace where the secret will be created (optional, defaults to crossplane-system)
    // +optional
    Namespace *string `json:"namespace,omitempty"`
}

// SecretStorageConfig defines where organization API keys should be stored
type SecretStorageConfig struct {
    // Type specifies the storage backend (AWS or Kubernetes)
    // +kubebuilder:validation:Required
    Type SecretStorageType `json:"type"`

    // AWS configuration (used when Type is "AWS")
    // +optional
    AWS *AWSSecretsManagerReference `json:"aws,omitempty"`

    // Kubernetes configuration (used when Type is "Kubernetes")
    // +optional
    Kubernetes *KubernetesSecretReference `json:"kubernetes,omitempty"`
}

// ===============================================================
// AWS Secrets Manager Configuration (Kept for Compatibility)
// ===============================================================

// AWSSecretsManagerReference defines the AWS Secrets Manager configuration
type AWSSecretsManagerReference struct {
    // Region where the MongoDB API key secret will be stored.
    // +kubebuilder:validation:Required
    Region string `json:"region"`

    // SecretName is appended to "product/mongodb/".
    // If omitted, defaults to metadata.name.
    // +optional
    SecretName *string `json:"secretName,omitempty"`

    // Optional custom AWS KMS key.
    // +optional
    KMSKeyID *string `json:"kmsKeyId,omitempty"`
}

// ===============================================================
// IP Access Types
// ===============================================================

// IPAccessEntry represents a single IP address in the access list
type IPAccessEntry struct {
    // IP is a single IPv4 address (e.g., "203.0.113.45")
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){3}[0-9]{1,3}$`
    IP string `json:"ip"`

    // Comment is an optional description of the IP access
    // +optional
    Comment *string `json:"comment,omitempty"`

    // CIDRBlock is the CIDR notation of the IP (always {IP}/32 for single IPs)
    // Optional: controller will populate in status.
    // +optional
    CIDRBlock string `json:"cidrBlock,omitempty"`

    // CreatedAt tracks when this IP was provisioned
    // +optional
    CreatedAt *metav1.Time `json:"createdAt,omitempty"`
}

// NetworkAccessConfig defines IP access provisioning for the organization
type NetworkAccessConfig struct {
    // Enabled controls whether IP access provisioning is active
    // +optional
    Enabled bool `json:"enabled"`

    // IPs is the list of individual IP addresses to whitelist
    // +optional
    IPs []IPAccessEntry `json:"ips,omitempty"`

    // AutoCleanup determines if IPs should be removed when org is deleted
    // +optional
    AutoCleanup bool `json:"autoCleanup"`

    // APIAccessListRequired enables Atlas organization-level enforcement:
    // "Require IP Access List for the Atlas Administration API".
    // When true, the controller will call PATCH /orgs/{orgId}/settings
    // to set apiAccessListRequired: true after org creation.
    // +optional
    APIAccessListRequired bool `json:"apiAccessListRequired,omitempty"`
}

// ===============================================================
// Organization Parameters
// ===============================================================

// OrganizationAPIKey defines the API key configuration
type OrganizationAPIKey struct {
    // Description for the API key
    // +kubebuilder:validation:Required
    Description string `json:"description"`

    // Roles assigned to the API key
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    Roles []string `json:"roles"`
}

// OrganizationParameters defines the desired state of the Organization
type OrganizationParameters struct {
    // APIKey configuration for the organization
    // +kubebuilder:validation:Required
    APIKey OrganizationAPIKey `json:"apiKey"`

    // OwnerID is the MongoDB Atlas user ID who will own the organization
    // +kubebuilder:validation:Required
    OwnerID string `json:"ownerID"`

    // SecretStorageConfig defines where to store the API key (preferred)
    // If not specified, falls back to AWSSecretsConfig for backward compatibility
    // +optional
    SecretStorageConfig *SecretStorageConfig `json:"secretStorageConfig,omitempty"`

    // AWSSecretsConfig defines where to store the API key in AWS Secrets Manager
    // DEPRECATED: Use SecretStorageConfig instead. Kept for backward compatibility.
    // +optional
    AWSSecretsConfig *AWSSecretsManagerReference `json:"awsSecretsConfig,omitempty"`

    // NetworkAccessConfig defines IP access provisioning
    // +optional
    NetworkAccessConfig NetworkAccessConfig `json:"networkAccessConfig,omitempty"`
}

// ===============================================================
// Organization Observation (Status Fields)
// ===============================================================

// OrganizationObservation contains the observed state of the Organization
type OrganizationObservation struct {
    // OrgID is the MongoDB Atlas organization ID
    // +optional
    OrgID string `json:"orgID,omitempty"`

    // OrgName is the name of the organization
    // +optional
    OrgName string `json:"orgName,omitempty"`

    // SecretName is the name of the secret (AWS or Kubernetes)
    // +optional
    SecretName string `json:"secretName,omitempty"`

    // SecretARN is the ARN of the AWS secret (only for AWS storage)
    // +optional
    SecretARN string `json:"secretARN,omitempty"`

    // KMSKeyID is the KMS key used to encrypt the secret (optional informational field)
    // +optional
    KMSKeyID string `json:"kmsKeyId,omitempty"`

    // SecretNamespace is the Kubernetes namespace (only for Kubernetes storage)
    // +optional
    SecretNamespace string `json:"secretNamespace,omitempty"`

    // SecretStorageType indicates where the secret is stored (AWS or Kubernetes)
    // +optional
    SecretStorageType string `json:"secretStorageType,omitempty"`

    // CreatedAt is when the organization was created
    // +optional
    CreatedAt *metav1.Time `json:"createdAt,omitempty"`

    // ===============================================================
    // API Key Audit Fields
    // ===============================================================

    // CreatedWithCredentialSource indicates the credential source used (e.g., "AWS", "Kubernetes")
    // +optional
    CreatedWithCredentialSource string `json:"createdWithCredentialSource,omitempty"`

    // CreatedWithAPIKeyID is the masked API key ID used for creation
    // +optional
    CreatedWithAPIKeyID string `json:"createdWithAPIKeyID,omitempty"`

    // APIKeyRoles are the roles assigned to the API key
    // +optional
    APIKeyRoles []string `json:"apiKeyRoles,omitempty"`

    // ===============================================================
    // Deletion Tracking
    // ===============================================================

    // LastDeletionAttemptTime is the timestamp of the last deletion attempt
    // +optional
    LastDeletionAttemptTime *metav1.Time `json:"lastDeletionAttemptTime,omitempty"`

    // DeletionAttemptCount tracks how many times deletion has been attempted
    // +optional
    DeletionAttemptCount int `json:"deletionAttemptCount,omitempty"`

    // LastDeletionAttemptError contains the error message from the last deletion attempt
    // +optional
    LastDeletionAttemptError string `json:"lastDeletionAttemptError,omitempty"`

    // DeletionPhase tracks the current phase of the deletion state machine
    // +optional
    DeletionPhase string `json:"deletionPhase,omitempty"`

    // EnforcementDisabledAt is when apiAccessListRequired was disabled during deletion
    // +optional
    EnforcementDisabledAt *metav1.Time `json:"enforcementDisabledAt,omitempty"`

    // EnforcementDisabled indicates if API access list enforcement was disabled during deletion
    // This prevents repeating enforcement disable on retry
    // +optional
    EnforcementDisabled bool `json:"enforcementDisabled,omitempty"`

    // IPsCleanedUp indicates whether IPs have been cleaned up during deletion
    // This prevents repeating IP cleanup on retry
    // +optional
    IPsCleanedUp bool `json:"ipsCleanedUp,omitempty"`

    // DependenciesChecked indicates whether dependencies have been checked
    // +optional
    DependenciesChecked bool `json:"dependenciesChecked,omitempty"`

    // LastRateLimitHitTime is when the last rate limit (429) was encountered
    // +optional
    LastRateLimitHitTime *metav1.Time `json:"lastRateLimitHitTime,omitempty"`

    // ForceDeleteAfterFailures indicates deletion failed and force delete is available
    // +optional
    ForceDeleteAfterFailures bool `json:"forceDeleteAfterFailures,omitempty"`

    // ===============================================================
    // State Tracking
    // ===============================================================

    // State represents the current state (PENDING, ACTIVE, DELETING, DELETED)
    // +optional
    // +kubebuilder:validation:Enum=PENDING;ACTIVE;DELETING;DELETED
    State *string `json:"state,omitempty"`

    // DeletedAt is the timestamp when the organization was deleted
    // +optional
    DeletedAt *metav1.Time `json:"deletedAt,omitempty"`

    // ===============================================================
    // Org-level Enforcement Status
    // ===============================================================

    // APIAccessListRequired indicates whether IP access list is required for the org
    // +optional
    APIAccessListRequired *bool `json:"apiAccessListRequired,omitempty"`

    // LastEnforcementToggleTime is when apiAccessListRequired was last changed
    // +optional
    LastEnforcementToggleTime *metav1.Time `json:"lastEnforcementToggleTime,omitempty"`

    // EnforcementError contains any error from enforcement operations
    // +optional
    EnforcementError string `json:"enforcementError,omitempty"`

    // ===============================================================
    // IP Access Status
    // ===============================================================

    // ProvisionedIPs is the list of IPs provisioned to the access list
    // +optional
    ProvisionedIPs []IPAccessEntry `json:"provisionedIPs,omitempty"`

    // IPAccessEntryCount is the number of IPs in the access list
    // +optional
    IPAccessEntryCount int `json:"ipAccessEntryCount,omitempty"`

    // LastIPAccessUpdate is the timestamp of the last IP access list update
    // +optional
    LastIPAccessUpdate *metav1.Time `json:"lastIPAccessUpdate,omitempty"`

    // IPAccessError contains any error from IP access list operations
    // +optional
    IPAccessError string `json:"ipAccessError,omitempty"`
}

// ===============================================================
// Spec & Status
// ===============================================================

// OrganizationSpec defines the desired state of Organization
type OrganizationSpec struct {
    xpv1.ResourceSpec `json:",inline"`
    ForProvider       OrganizationParameters `json:"forProvider"`
}

// OrganizationStatus defines the observed state of Organization
type OrganizationStatus struct {
    xpv1.ResourceStatus `json:",inline"`
    AtProvider          OrganizationObservation `json:"atProvider,omitempty"`
}

// ===============================================================
// CRD Definition
// ===============================================================

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="ORG-ID",type="string",JSONPath=".status.atProvider.orgID"
// +kubebuilder:printcolumn:name="STATE",type="string",JSONPath=".status.atProvider.state"
// +kubebuilder:printcolumn:name="STORAGE",type="string",JSONPath=".status.atProvider.secretStorageType"
// +kubebuilder:printcolumn:name="SECRET-NAME",type="string",JSONPath=".status.atProvider.secretName"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,mongodb}

// Organization is the Schema for the organizations API
type Organization struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   OrganizationSpec   `json:"spec"`
    Status OrganizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OrganizationList contains a list of Organization
type OrganizationList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []Organization `json:"items"`
}

// ===============================================================
// Type Metadata
// ===============================================================

var (
    OrganizationKind             = reflect.TypeOf(Organization{}).Name()
    OrganizationGroupKind        = schema.GroupKind{Group: Group, Kind: OrganizationKind}.String()
    OrganizationKindAPIVersion   = OrganizationKind + "." + SchemeGroupVersion.String()
    OrganizationGroupVersionKind = SchemeGroupVersion.WithKind(OrganizationKind)
)

// ===============================================================
// Organization State Constants
// ===============================================================

const (
    // OrganizationStatePending indicates the organization is being created
    OrganizationStatePending = "PENDING"

    // OrganizationStateActive indicates the organization is active
    OrganizationStateActive = "ACTIVE"

    // OrganizationStateDeleting indicates the organization is being deleted
    OrganizationStateDeleting = "DELETING"

    // OrganizationStateDeleted indicates the organization has been deleted
    OrganizationStateDeleted = "DELETED"
)

// ===============================================================
// Deletion Phase Constants
// ===============================================================

const (
    // DeletionPhaseInitial is the initial phase of deletion
    DeletionPhaseInitial = "Initial"

    // DeletionPhaseDisablingEnforcement is when enforcement is being disabled
    DeletionPhaseDisablingEnforcement = "DisablingEnforcement"

    // DeletionPhaseCleaningIPs is when IPs are being cleaned up
    DeletionPhaseCleaningIPs = "CleaningIPs"

    // DeletionPhaseDeletingOrg is when the org is being deleted from Atlas
    DeletionPhaseDeletingOrg = "DeletingOrg"

    // DeletionPhaseVerifying is when deletion is being verified
    DeletionPhaseVerifying = "Verifying"

    // DeletionPhaseDeletingSecret is when the secret is being deleted
    DeletionPhaseDeletingSecret = "DeletingSecret"

    // DeletionPhaseRemovingFinalizers is when finalizers are being removed
    DeletionPhaseRemovingFinalizers = "RemovingFinalizers"

    // DeletionPhaseCompleted indicates deletion is complete
    DeletionPhaseCompleted = "Completed"
)

// ===============================================================
// Finalizer Constants
// ===============================================================

const (
    // FinalizerOrganizationCleanup is the finalizer for organization cleanup
    FinalizerOrganizationCleanup = "organization.platform.allianz.io/cleanup"

    // CrossplaneManagedFinalizer is the Crossplane managed resource finalizer
    CrossplaneManagedFinalizer = "finalizer.managedresource.crossplane.io"
)

// ===============================================================
// Annotation Constants
// ===============================================================

const (
    // AnnotationForceDelete can be set to "true" to force delete an organization
    // even if Atlas deletion fails after multiple attempts
    AnnotationForceDelete = "organization.platform.allianz.io/force-delete"
)

func init() {
    SchemeBuilder.Register(&Organization{}, &OrganizationList{})
}