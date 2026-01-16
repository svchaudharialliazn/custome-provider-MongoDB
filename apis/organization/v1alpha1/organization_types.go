// apis/organization/v1alpha1/organization_types.go
package v1alpha1

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// ===============================================================
// AWS Secrets Manager Configuration
// ===============================================================

// AWSSecretsManagerReference defines the AWS Secrets Manager configuration
type AWSSecretsManagerReference struct {
	// Region where the MongoDB API key secret will be stored.
	Region string `json:"region"`

	// SecretName is appended to "product/mongodb/".
	// If omitted, defaults to metadata.name.
	SecretName *string `json:"secretName,omitempty"`

	// Optional custom AWS KMS key.
	KMSKeyID *string `json:"kmsKeyId,omitempty"`
}

// ===============================================================
// IP Access Types
// ===============================================================

// IPAccessEntry represents a single IP address in the access list
type IPAccessEntry struct {
	// IP is a single IPv4 address (e.g., "203.0.113.45")
	IP string `json:"ip"`

	// Comment is an optional description of the IP access
	Comment *string `json:"comment,omitempty"`

	// CIDRBlock is the CIDR notation of the IP (always {IP}/32 for single IPs)
	// Optional: controller will populate in status.
	CIDRBlock string `json:"cidrBlock,omitempty"`

	// CreatedAt tracks when this IP was provisioned
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
}

// NetworkAccessConfig defines IP access provisioning for the organization
type NetworkAccessConfig struct {
	// Enabled controls whether IP access provisioning is active
	Enabled bool `json:"enabled"`

	// IPs is the list of individual IP addresses to whitelist
	IPs []IPAccessEntry `json:"ips,omitempty"`

	// AutoCleanup determines if IPs should be removed when org is deleted
	AutoCleanup bool `json:"autoCleanup"`

	// APIAccessListRequired enables Atlas organization-level enforcement:
	// "Require IP Access List for the Atlas Administration API".
	// When true, the controller will call PATCH /orgs/{orgId}/settings
	// to set apiAccessListRequired: true after org creation.
	APIAccessListRequired bool `json:"apiAccessListRequired,omitempty"`
}

// ===============================================================
// Organization Parameters
// ===============================================================

// OrganizationParameters defines the desired state of the Organization
type OrganizationParameters struct {
	// APIKey configuration for the organization
	APIKey OrganizationAPIKey `json:"apiKey"`

	// OwnerID is the MongoDB Atlas user ID who will own the organization
	OwnerID string `json:"ownerID"`

	// AWSSecretsConfig defines where to store the API key in AWS Secrets Manager
	AWSSecretsConfig AWSSecretsManagerReference `json:"awsSecretsConfig"`

	// NetworkAccessConfig defines IP access provisioning
	NetworkAccessConfig NetworkAccessConfig `json:"networkAccessConfig,omitempty"`
}

// OrganizationAPIKey defines the API key configuration
type OrganizationAPIKey struct {
	// Description for the API key
	Description string `json:"description"`

	// Roles assigned to the API key
	Roles []string `json:"roles"`
}

// ===============================================================
// Organization Observation (Status Fields)
// ===============================================================

// OrganizationObservation contains the observed state of the Organization
type OrganizationObservation struct {
	// OrgID is the MongoDB Atlas organization ID
	OrgID string `json:"orgID,omitempty"`

	// OrgName is the name of the organization
	OrgName string `json:"orgName,omitempty"`

	// SecretName is the name of the AWS secret
	SecretName string `json:"secretName,omitempty"`

	// SecretARN is the ARN of the AWS secret
	SecretARN string `json:"secretARN,omitempty"`

	// KMSKeyID is the KMS key used to encrypt the secret
	KMSKeyID string `json:"kmsKeyId,omitempty"`

	// CreatedAt is when the organization was created
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`

	// ===============================================================
	// API Key Audit Fields
	// ===============================================================

	// CreatedWithCredentialSource indicates the credential source used
	CreatedWithCredentialSource string `json:"createdWithCredentialSource,omitempty"`

	// CreatedWithAPIKeyID is the masked API key ID used for creation
	CreatedWithAPIKeyID string `json:"createdWithAPIKeyID,omitempty"`

	// APIKeyRoles are the roles assigned to the API key
	APIKeyRoles []string `json:"apiKeyRoles,omitempty"`

	// ===============================================================
	// Deletion Tracking
	// ===============================================================

	// LastDeletionAttemptTime is the timestamp of the last deletion attempt
	LastDeletionAttemptTime *metav1.Time `json:"lastDeletionAttemptTime,omitempty"`

	// DeletionAttemptCount tracks how many times deletion has been attempted
	DeletionAttemptCount int `json:"deletionAttemptCount,omitempty"`

	// LastDeletionAttemptError contains the error message from the last deletion attempt
	LastDeletionAttemptError string `json:"lastDeletionAttemptError,omitempty"`

	// DeletionPhase tracks the current phase of the deletion state machine
	DeletionPhase string `json:"deletionPhase,omitempty"`

	// EnforcementDisabledAt is when apiAccessListRequired was disabled during deletion
	EnforcementDisabledAt *metav1.Time `json:"enforcementDisabledAt,omitempty"`

	// IPsCleanedUp indicates whether IPs have been cleaned up during deletion
	IPsCleanedUp bool `json:"ipsCleanedUp,omitempty"`

	// DependenciesChecked indicates whether dependencies have been checked
	DependenciesChecked bool `json:"dependenciesChecked,omitempty"`

	// LastRateLimitHitTime is when the last rate limit (429) was encountered
	LastRateLimitHitTime *metav1.Time `json:"lastRateLimitHitTime,omitempty"`

	// ForceDeleteAfterFailures indicates deletion failed and force delete is available
	ForceDeleteAfterFailures bool `json:"forceDeleteAfterFailures,omitempty"`

	// ===============================================================
	// State Tracking
	// ===============================================================

	// State represents the current state (PENDING, ACTIVE, DELETING, DELETED)
	State *string `json:"state,omitempty"`

	// DeletedAt is the timestamp when the organization was deleted
	DeletedAt *metav1.Time `json:"deletedAt,omitempty"`

	// ===============================================================
	// Org-level Enforcement Status
	// ===============================================================

	// APIAccessListRequired indicates whether IP access list is required
	APIAccessListRequired *bool `json:"apiAccessListRequired,omitempty"`

	// LastEnforcementToggleTime is when apiAccessListRequired was last changed
	LastEnforcementToggleTime *metav1.Time `json:"lastEnforcementToggleTime,omitempty"`

	// EnforcementError contains any error from enforcement operations
	EnforcementError string `json:"enforcementError,omitempty"`

	// ===============================================================
	// IP Access Status
	// ===============================================================

	// ProvisionedIPs is the list of IPs provisioned to the access list
	ProvisionedIPs []IPAccessEntry `json:"provisionedIPs,omitempty"`

	// IPAccessEntryCount is the number of IPs in the access list
	IPAccessEntryCount int `json:"ipAccessEntryCount,omitempty"`

	// LastIPAccessUpdate is the timestamp of the last IP access list update
	LastIPAccessUpdate *metav1.Time `json:"lastIPAccessUpdate,omitempty"`

	// IPAccessError contains any error from IP access list operations
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
// +kubebuilder:printcolumn:name="DEL-PHASE",type="string",JSONPath=".status.atProvider.deletionPhase",priority=1
// +kubebuilder:printcolumn:name="DEL-ATTEMPTS",type="integer",JSONPath=".status.atProvider.deletionAttemptCount",priority=1
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

// // ===============================================================
// // Deletion Phase Constants
// // ===============================================================

// const (
// 	// DeletionPhaseInit is the initial state (empty string)
// 	DeletionPhaseInit = ""

// 	// DeletionPhaseCheckDependencies checks for projects/clusters before deletion
// 	DeletionPhaseCheckDependencies = "CHECK_DEPENDENCIES"

// 	// DeletionPhaseDisableEnforcement disables apiAccessListRequired before deletion
// 	DeletionPhaseDisableEnforcement = "DISABLE_ENFORCEMENT"

// 	// DeletionPhaseWaitForRateLimit waits for rate limit backoff to complete
// 	DeletionPhaseWaitForRateLimit = "WAIT_RATE_LIMIT"

// 	// DeletionPhaseCleanupIPs removes IPs from the access list
// 	DeletionPhaseCleanupIPs = "CLEANUP_IPS"

// 	// DeletionPhaseDeleteOrg attempts to delete the organization in Atlas
// 	DeletionPhaseDeleteOrg = "DELETE_ORG"

// 	// DeletionPhaseVerifyDeletion verifies the organization was deleted
// 	DeletionPhaseVerifyDeletion = "VERIFY_DELETION"

// 	// DeletionPhaseCleanupAWSSecret deletes the AWS secret
// 	DeletionPhaseCleanupAWSSecret = "CLEANUP_AWS_SECRET"

// 	// DeletionPhaseRemoveFinalizers removes finalizers to complete deletion
// 	DeletionPhaseRemoveFinalizers = "REMOVE_FINALIZERS"

// 	// DeletionPhaseFailed indicates deletion has failed after max retries
// 	DeletionPhaseFailed = "FAILED"

// 	// DeletionPhaseComplete indicates deletion is complete
// 	DeletionPhaseComplete = "COMPLETE"
// )

// ===============================================================
// Finalizer Constants
// ===============================================================

const (
	// FinalizerOrganizationCleanup is the finalizer for organization cleanup
	FinalizerOrganizationCleanup = "organization.platform.allianz.io/cleanup"
)

// ===============================================================
// Annotation Constants
// ===============================================================

// const (
// 	// AnnotationForceDelete allows force deletion of the Kubernetes resource
// 	// even if the Atlas organization couldn't be deleted
// 	AnnotationForceDelete = "organization.mongodb.swapnil.io/force-delete"
// )

func init() {
	SchemeBuilder.Register(&Organization{}, &OrganizationList{})
}
