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
// Organization Parameters
// ===============================================================
type OrganizationParameters struct {
    APIKey           OrganizationAPIKey         `json:"apiKey"`
    OwnerID          string                     `json:"ownerID"`
    AWSSecretsConfig AWSSecretsManagerReference `json:"awsSecretsConfig"`
}

type OrganizationAPIKey struct {
    Description string   `json:"description"`
    Roles       []string `json:"roles"`
}

// ===============================================================
// Organization Observation (Status Fields)
// ===============================================================
type OrganizationObservation struct {
    OrgID      string       `json:"orgID,omitempty"`
    OrgName    string       `json:"orgName,omitempty"`
    SecretName string       `json:"secretName,omitempty"`
    SecretARN  string       `json:"secretARN,omitempty"`
    KMSKeyID   string       `json:"kmsKeyID,omitempty"`
    CreatedAt  *metav1.Time `json:"createdAt,omitempty"`

    // API key audit fields
    CreatedWithCredentialSource string   `json:"createdWithCredentialSource,omitempty"`
    CreatedWithAPIKeyID         string   `json:"createdWithAPIKeyID,omitempty"`
    APIKeyRoles                 []string `json:"apiKeyRoles,omitempty"`

    // Deletion tracking
    LastDeletionAttemptTime  *metav1.Time `json:"lastDeletionAttemptTime,omitempty"`
    DeletionAttemptCount     int          `json:"deletionAttemptCount,omitempty"`
    LastDeletionAttemptError string       `json:"lastDeletionAttemptError,omitempty"`

    State     *string      `json:"state,omitempty"`     // PENDING | ACTIVE | DELETING | DELETED
    DeletedAt *metav1.Time `json:"deletedAt,omitempty"` // Deletion timestamp
}

// ===============================================================
// Spec & Status
// ===============================================================
type OrganizationSpec struct {
    xpv1.ResourceSpec `json:",inline"`
    ForProvider       OrganizationParameters `json:"forProvider"`
}

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
// +kubebuilder:printcolumn:name="SECRET-NAME",type="string",JSONPath=".status.atProvider.secretName"
// +kubebuilder:printcolumn:name="SECRET-ARN",type="string",JSONPath=".status.atProvider.secretARN"
// +kubebuilder:printcolumn:name="STATE",type="string",JSONPath=".status.atProvider.state"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,mongodb}
type Organization struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   OrganizationSpec   `json:"spec"`
    Status OrganizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
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
// Deletion Constants
// ===============================================================
const (
    FinalizerOrganizationCleanup = "organization.platform.allianz.io/cleanup"

    OrganizationStatePending  = "PENDING"
    OrganizationStateActive   = "ACTIVE"
    OrganizationStateDeleting = "DELETING"
    OrganizationStateDeleted  = "DELETED"
)

func init() {
    SchemeBuilder.Register(&Organization{}, &OrganizationList{})
}

