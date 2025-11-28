/*
Copyright 2023 The Crossplane Authors.
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

const (
	CredentialsSourceAWS xpv1.CredentialsSource = "AWS"
)

// AWSSecretsManagerReference holds configuration for AWS Secrets Manager storage.
type AWSSecretsManagerReference struct {
	Region     string  `json:"region"`
	SecretName *string `json:"secretName,omitempty"`
	KMSKeyID   *string `json:"kmsKeyId,omitempty"`
	SecretKey  *string `json:"secretKey,omitempty"`
}

// AWSCredentialsSource contains AWS credential source details.
type AWSCredentialsSource struct {
	SecretsManager *AWSSecretsManagerReference `json:"secretsManager,omitempty"`
}

// ProviderCredentials holds credentials source details.
type ProviderCredentials struct {
	Source xpv1.CredentialsSource `json:"source"`
	AWS    *AWSCredentialsSource  `json:"aws,omitempty"`
}

// ProviderConfigSpec defines the desired state of ProviderConfig.
type ProviderConfigSpec struct {
	Credentials ProviderCredentials `json:"credentials"`
}

// ProviderConfigStatus represents the observed state of ProviderConfig.
type ProviderConfigStatus struct {
	xpv1.ProviderConfigStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,mongodb}
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// GetCondition returns condition of this ProviderConfig.
func (pc *ProviderConfig) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return pc.Status.GetCondition(ct)
}

// SetConditions sets the conditions of this ProviderConfig.
func (pc *ProviderConfig) SetConditions(c ...xpv1.Condition) {
	pc.Status.SetConditions(c...)
}

// GetUsers returns number of users of this ProviderConfig.
func (pc *ProviderConfig) GetUsers() int64 {
	return pc.Status.Users
}

// SetUsers sets the number of users of this ProviderConfig.
func (pc *ProviderConfig) SetUsers(i int64) {
	pc.Status.Users = i
}

// +kubebuilder:object:root=true
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

var (
	ProviderConfigKind             = reflect.TypeOf(ProviderConfig{}).Name()
	ProviderConfigGroupKind        = schema.GroupKind{Group: Group, Kind: ProviderConfigKind}.String()
	ProviderConfigKindAPIVersion   = ProviderConfigKind + "." + SchemeGroupVersion.String()
	ProviderConfigGroupVersionKind = SchemeGroupVersion.WithKind(ProviderConfigKind)
)

func init() {
	SchemeBuilder.Register(&ProviderConfig{}, &ProviderConfigList{})
}
