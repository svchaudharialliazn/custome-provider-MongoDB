package v1alpha1

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// AWSSecretsManagerReference defines AWS Secrets Manager configuration
type AWSSecretsManagerReference struct {
	// AWS Region where the secret is stored
	Region string `json:"region"`

	// SecretName is just the short org identifier (e.g., "test-org").
	// The controller will prepend "product/mongodb/".
	// If omitted, defaults to metadata.name.
	SecretName *string `json:"secretName,omitempty"`

	// AWS KMS Key ID for encryption (optional).
	KMSKeyID *string `json:"kmsKeyId,omitempty"`
}

// OrganizationParameters are the configurable fields of an Organization.
type OrganizationParameters struct {
	APIKey           OrganizationAPIKey         `json:"apiKey"`
	OwnerID          string                     `json:"ownerID"`
	AWSSecretsConfig AWSSecretsManagerReference `json:"awsSecretsConfig"`
}

// OrganizationAPIKey defines the initial API key details.
type OrganizationAPIKey struct {
	Description string   `json:"description"`
	Roles       []string `json:"roles"`
}

// OrganizationObservation are the observable fields of an Organization.
type OrganizationObservation struct {
	OrgID      string       `json:"orgID,omitempty"`
	OrgName    string       `json:"orgName,omitempty"`
	SecretName string       `json:"secretName,omitempty"` // expanded name product/mongodb/<org>
	SecretARN  string       `json:"secretARN,omitempty"`
	KMSKeyID   string       `json:"kmsKeyID,omitempty"`
	CreatedAt  *metav1.Time `json:"createdAt,omitempty"`
	// ADD: Deletion state tracking
	State      *string      `json:"state,omitempty"`       // PENDING, ACTIVE, DELETING, DELETED
	DeletedAt  *metav1.Time `json:"deletedAt,omitempty"`   // When deletion started
}

// OrganizationSpec defines the desired state of an Organization.
type OrganizationSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       OrganizationParameters `json:"forProvider"`
}

// OrganizationStatus represents the observed state of an Organization.
type OrganizationStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          OrganizationObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// Organization manages MongoDB Atlas organizations with AWS-only credential storage.
// 
// Deletion Behavior:
// - When Organization is deleted, it will trigger deletion from MongoDB Atlas
// - The deletionPolicy field controls external resource handling:
//   - "Delete" (default): Organization is deleted from MongoDB Atlas
//   - "Orphan": Organization is preserved in MongoDB Atlas
// - Finalizers ensure proper cleanup sequence and prevent premature deletion
// - If child resources (Projects, Clusters) exist, deletion will be delayed until they are deleted
//
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

var (
	OrganizationKind             = reflect.TypeOf(Organization{}).Name()
	OrganizationGroupKind        = schema.GroupKind{Group: Group, Kind: OrganizationKind}.String()
	OrganizationKindAPIVersion   = OrganizationKind + "." + SchemeGroupVersion.String()
	OrganizationGroupVersionKind = SchemeGroupVersion.WithKind(OrganizationKind)
)

// ADD: Deletion constants
const (
	// FinalizerOrganizationCleanup is the finalizer for organization cleanup
	FinalizerOrganizationCleanup = "organization.platform.allianz.io/cleanup"

	// Organization deletion states
	OrganizationStatePending  = "PENDING"
	OrganizationStateActive   = "ACTIVE"
	OrganizationStateDeleting = "DELETING"
	OrganizationStateDeleted  = "DELETED"
)

func init() {
	SchemeBuilder.Register(&Organization{}, &OrganizationList{})
}
