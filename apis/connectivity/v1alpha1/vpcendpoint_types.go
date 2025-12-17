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

// VPCEndpointParameters are the configurable fields of a VPC Endpoint using AWS SDK.
type VPCEndpointParameters struct {
	VpcID            string   `json:"vpcId"`            // example vpc-03a75e9d856407da5
	ServiceName      string   `json:"serviceName"`      // example com.amazonaws.vpce.eu-central-1.vpce-svc-02c21ee840752cff7
	AccountID        string   `json:"accountId"`        // example 198927051560
	SubnetIDs        []string `json:"subnetIds"`        // example [ subnet-000ff8403aca2347d ]
	SecurityGroupIDs []string `json:"securityGroupIds"` // example [ sg-0333847892bf56879 ]
	Region           string   `json:"region"`           // example eu-central-1
	IPAddressType    string   `json:"ipAddressType"`    // example ipv4
	VPCEndpointType  string   `json:"vpcEndpointType"`  // example Interface or Gateway

	// NEW AWS SDK specific fields
	PrivateDNSEnabled *bool             `json:"privateDnsEnabled,omitempty"` // Enable private DNS for Interface endpoints
	PolicyDocument    string            `json:"policyDocument,omitempty"`    // Optional restrictive policy JSON
	TagSpecifications map[string]string `json:"tagSpecifications,omitempty"` // AWS resource tags
}

// VPCEndpointObservation are the observable fields of a VPC Endpoint.
type VPCEndpointObservation struct {
	State               string                `json:"state"`
	VpcEndpointID       string                `json:"vpcEndpointId"`
	PrivateDNSName      string                `json:"privateDnsName,omitempty"`      // NEW: Private DNS hostname
	NetworkInterfaceIds []string              `json:"networkInterfaceIds,omitempty"` // NEW: ENI details
	NetworkInterfaces   []NetworkInterfaceObs `json:"networkInterfaces,omitempty"`   // NEW: Detailed ENI info
	CreationTimestamp   string                `json:"creationTimestamp,omitempty"`   // NEW: Endpoint creation time
}

// NetworkInterfaceObs represents Elastic Network Interface details for the VPC endpoint
type NetworkInterfaceObs struct {
	NetworkInterfaceID string `json:"networkInterfaceId"`
	SubnetID           string `json:"subnetId"`
	PrivateIP          string `json:"privateIp"`
	Status             string `json:"status"`
}

// A VPCEndpointSpec defines the desired state of a VPC Endpoint.
type VPCEndpointSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       VPCEndpointParameters `json:"forProvider"`
}

// A VPCEndpointStatus represents the observed state of a VPC Endpoint.
type VPCEndpointStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          VPCEndpointObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A VPCEndpoint is a managed resource for AWS VPC Endpoints via Crossplane.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="STATE",type="string",JSONPath=".status.atProvider.state"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,mongodb}
type VPCEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VPCEndpointSpec   `json:"spec"`
	Status VPCEndpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VPCEndpointList contains a list of VPCEndpoint resources.
type VPCEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPCEndpoint `json:"items"`
}

// VPCEndpoint type metadata.
var (
	VPCEndpointKind             = reflect.TypeOf(VPCEndpoint{}).Name()
	VPCEndpointGroupKind        = schema.GroupKind{Group: Group, Kind: VPCEndpointKind}.String()
	VPCEndpointKindAPIVersion   = VPCEndpointKind + "." + SchemeGroupVersion.String()
	VPCEndpointGroupVersionKind = SchemeGroupVersion.WithKind(VPCEndpointKind)
)

func init() {
	SchemeBuilder.Register(&VPCEndpoint{}, &VPCEndpointList{})
}
