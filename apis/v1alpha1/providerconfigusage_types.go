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
	"github.com/crossplane/crossplane-runtime/pkg/resource"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,mongodb}
type ProviderConfigUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	
	ProviderConfigReference *xpv1.Reference     `json:"providerConfigReference,omitempty"`
	ResourceReference       xpv1.TypedReference `json:"resourceReference"`
}

// Interface implementations for ProviderConfigUsage
func (u *ProviderConfigUsage) GetProviderConfigReference() xpv1.Reference {
	if u.ProviderConfigReference == nil {
		return xpv1.Reference{}
	}
	return *u.ProviderConfigReference
}

func (u *ProviderConfigUsage) SetProviderConfigReference(r xpv1.Reference) {
	u.ProviderConfigReference = &r
}

func (u *ProviderConfigUsage) GetResourceReference() xpv1.TypedReference {
	return u.ResourceReference
}

func (u *ProviderConfigUsage) SetResourceReference(r xpv1.TypedReference) {
	u.ResourceReference = r
}

// +kubebuilder:object:root=true
type ProviderConfigUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfigUsage `json:"items"`
}

// GetItems returns the list of provider config usages (matches exact interface signature)
func (ul *ProviderConfigUsageList) GetItems() []resource.ProviderConfigUsage {
	if ul == nil {
		return nil
	}
	items := make([]resource.ProviderConfigUsage, len(ul.Items))
	for i := range ul.Items {
		items[i] = &ul.Items[i]
	}
	return items
}

var (
	ProviderConfigUsageKind                 = reflect.TypeOf(ProviderConfigUsage{}).Name()
	ProviderConfigUsageGroupKind            = schema.GroupKind{Group: Group, Kind: ProviderConfigUsageKind}.String()
	ProviderConfigUsageKindAPIVersion       = ProviderConfigUsageKind + "." + SchemeGroupVersion.String()
	ProviderConfigUsageGroupVersionKind     = SchemeGroupVersion.WithKind(ProviderConfigUsageKind)
	ProviderConfigUsageListGroupVersionKind = SchemeGroupVersion.WithKind("ProviderConfigUsageList")
)

func init() {
	SchemeBuilder.Register(&ProviderConfigUsage{}, &ProviderConfigUsageList{})
}
