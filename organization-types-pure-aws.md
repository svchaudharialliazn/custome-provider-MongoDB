## 3. Updated Organization Types (apis/organization/v1alpha1/organization_types.go) - Pure AWS

```go
/*
Copyright 2022 The Crossplane Authors.

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
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// AWSSecretsManagerReference defines AWS Secrets Manager configuration
type AWSSecretsManagerReference struct {
	// AWS Region where the secret is stored
	Region string `json:"region"`
	// AWS Secrets Manager secret name or ARN (auto-generated if not specified)
	SecretName *string `json:"secretName,omitempty"`
	// AWS KMS Key ID for encryption (optional, uses aws/secretsmanager if not specified)
	KMSKeyID *string `json:"kmsKeyId,omitempty"`
}

// OrganizationParameters are the configurable fields of an Organization.
// COMPLETELY ELIMINATES ALL Kubernetes secret references
type OrganizationParameters struct {
	APIKey OrganizationAPIKey `json:"apiKey"`
	OwnerID string `json:"ownerID"`
	// AWS Secrets Manager configuration for storing MongoDB API credentials
	// NO Kubernetes secrets - ONLY AWS storage
	AWSSecretsConfig AWSSecretsManagerReference `json:"awsSecretsConfig"`
}

// OrganizationAPIKey is the initial API key of an organization
type OrganizationAPIKey struct {
	Description string `json:"description"`
	Roles []string `json:"roles"`
}

// OrganizationObservation are the observable fields of an Organization.
// NO connection details - credentials accessible ONLY via AWS Secrets Manager
type OrganizationObservation struct {
	OrgID string `json:"orgID"`
	// AWS Secrets Manager ARN where the API key is stored
	SecretARN string `json:"secretARN,omitempty"`
	// KMS Key ID used for encryption
	KMSKeyID string `json:"kmsKeyID,omitempty"`
	// Creation timestamp
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
	// ELIMINATES: ConnectionDetails, SecretRef, any Kubernetes secret references
}

// A OrganizationSpec defines the desired state of an Organization.
type OrganizationSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider OrganizationParameters `json:"forProvider"`
}

// A OrganizationStatus represents the observed state of an Organization.
type OrganizationStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider OrganizationObservation `json:"atProvider,omitempty"`
}

//+kubebuilder:object:root=true

// A Organization manages MongoDB Atlas organizations with AWS-ONLY credential storage
// COMPLETELY ELIMINATES Kubernetes secret integration
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="ORG-ID",type="string",JSONPath=".status.atProvider.orgID"
// +kubebuilder:printcolumn:name="SECRET-ARN",type="string",JSONPath=".status.atProvider.secretARN"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,mongodb}
type Organization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OrganizationSpec   `json:"spec"`
	Status OrganizationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OrganizationList contains a list of Organization
type OrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Organization `json:"items"`
}

// Organization type metadata.
var (
	OrganizationKind             = reflect.TypeOf(Organization{}).Name()
	OrganizationGroupKind        = schema.GroupKind{Group: Group, Kind: OrganizationKind}.String()
	OrganizationKindAPIVersion   = OrganizationKind + "." + SchemeGroupVersion.String()
	OrganizationGroupVersionKind = SchemeGroupVersion.WithKind(OrganizationKind)
)

func init() {
	SchemeBuilder.Register(&Organization{}, &OrganizationList{})
}
```