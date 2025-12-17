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

// Package vpcendpoint for the mongodb controller
package vpcendpoint

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/connection"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/svchaudharialliazn/swapnil-provider-mongodb/apis/connectivity/v1alpha1"
	apisv1alpha1 "github.com/svchaudharialliazn/swapnil-provider-mongodb/apis/v1alpha1"
	svc "github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/clients/connectivity"
	"github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/controller/features"
)

const (
	errNotVPCEndpoint = "managed resource is not a VPCEndpoint custom resource"
	errTrackPCUsage   = "cannot track ProviderConfig usage"
	errGetPC          = "cannot get ProviderConfig"
	errGetCreds       = "cannot get credentials"
)

// Setup adds a controller that reconciles VPCEndpoint managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.VPCEndpointGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.VPCEndpointGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:   mgr.GetClient(),
			usage:  resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			logger: o.Logger,
		}),
		managed.WithInitializers(),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.VPCEndpoint{}).
		WithEventFilter(resource.DesiredStateChanged()).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method is called.
type connector struct {
	kube   client.Client
	usage  resource.Tracker
	logger logging.Logger
}

// Connect produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using AWS credentials to form an AWS VPC endpoint client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.VPCEndpoint)
	if !ok {
		return nil, errors.New(errNotVPCEndpoint)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	credentials := pc.Spec.Credentials
	var creds []byte
	var err error

	// Handle Kubernetes Secret source (AWS credentials in K8s Secret)
	if credentials.Source == "Secret" || credentials.Source == xpv1.CredentialsSourceSecret {
		if credentials.SecretRef == nil {
			return nil, errors.New("secretRef must be specified when using Secret source")
		}

		secret := &corev1.Secret{}

		// Determine namespace - use secretRef namespace or fallback to ProviderConfig namespace
		secretNamespace := credentials.SecretRef.Namespace
		if secretNamespace == "" {
			secretNamespace = pc.Namespace
		}

		// Get the Kubernetes Secret
		if err := c.kube.Get(ctx, types.NamespacedName{
			Name:      credentials.SecretRef.Name,
			Namespace: secretNamespace,
		}, secret); err != nil {
			return nil, errors.Wrap(err, "cannot get AWS credentials secret")
		}

		// Extract credentials from secret data
		secretKey := credentials.SecretRef.Key
		if secretKey == "" {
			secretKey = "credentials" // Default key name
		}

		creds = secret.Data[secretKey]
		if len(creds) == 0 {
			return nil, errors.Wrapf(err, "credentials key %q not found in secret %q", secretKey, credentials.SecretRef.Name)
		}
	} else {
		return nil, errors.Wrapf(err, "unsupported credentials source: %s", credentials.Source)
	}

	if len(creds) == 0 {
		return nil, errors.New("no credentials found")
	}

	// Parse AWS credentials from JSON
	var awsCreds svc.AWSCredentials
	if err := json.Unmarshal(creds, &awsCreds); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal AWS credentials from secret")
	}
	c.logger.Info("Parsed AWS Credentials",
		"AccessKeyID", awsCreds.AccessKeyID[:4]+"...",
		"Region", awsCreds.Region,
		"HasSessionToken", awsCreds.SessionToken != "",
	)

	if awsCreds.Region == "" {
		return nil, errors.New("AWS region not found in credentials")
	}
	// Validate required fields
	if awsCreds.AccessKeyID == "" || awsCreds.SecretAccessKey == "" {
		return nil, errors.New("AWS credentials missing required fields: accessKeyId and secretAccessKey")
	}

	// Create AWS VPC endpoint client with credentials
	awsClient, err := svc.NewVPCEndpointClient(ctx, awsCreds)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create AWS VPC endpoint client")
	}

	return &external{
		kube:      c.kube,
		awsClient: awsClient,
		logger:    c.logger,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	kube      client.Client
	awsClient *svc.VPCEndpointClient
	logger    logging.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.VPCEndpoint)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotVPCEndpoint)
	}

	id := meta.GetExternalName(cr)
	if id == "" {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	c.logger.Debug("Observing VPC endpoint", "id", id)

	vpcEndpoint, err := c.awsClient.GetVPCEndpointStatus(ctx, id)
	if err != nil {
		c.logger.Debug("VPC endpoint not found", "id", id, "error", err)
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// Update status
	cr.Status.AtProvider.State = vpcEndpoint.State
	cr.Status.AtProvider.PrivateDNSName = vpcEndpoint.PrivateDNSName
	cr.Status.AtProvider.NetworkInterfaceIds = extractENIIDs(vpcEndpoint.NetworkInterfaces)

	// Set Crossplane conditions based on state
	switch vpcEndpoint.State {
	case "available":
		cr.SetConditions(xpv1.Available())
	case "pending":
		cr.SetConditions(xpv1.Creating())
	case "failed", "deleted":
		cr.SetConditions(xpv1.Unavailable().WithMessage(vpcEndpoint.State))
	default:
		cr.SetConditions(xpv1.Unavailable().WithMessage(vpcEndpoint.State))
	}

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.VPCEndpoint)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotVPCEndpoint)
	}

	c.logger.Debug("Creating VPC endpoint with AWS SDK", "vpc-endpoint", cr.Name)

	params := svc.CreateVPCEndpointInput{
		VpcID:             cr.Spec.ForProvider.VpcID,
		ServiceName:       cr.Spec.ForProvider.ServiceName,
		SubnetIDs:         cr.Spec.ForProvider.SubnetIDs,
		SecurityGroupIDs:  cr.Spec.ForProvider.SecurityGroupIDs,
		VPCEndpointType:   cr.Spec.ForProvider.VPCEndpointType,
		IPAddressType:     cr.Spec.ForProvider.IPAddressType,
		TagSpecifications: cr.Spec.ForProvider.TagSpecifications,
		PolicyDocument:    cr.Spec.ForProvider.PolicyDocument,
	}

	if cr.Spec.ForProvider.PrivateDNSEnabled != nil {
		params.PrivateDNSEnabled = *cr.Spec.ForProvider.PrivateDNSEnabled
	}

	res, err := c.awsClient.CreateVPCEndpoint(ctx, params)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "cannot create VPC endpoint")
	}

	c.logger.Debug("VPC endpoint created successfully", "id", res.VPCEndpointID, "state", res.State)

	// Set external name and initial status
	meta.SetExternalName(cr, res.VPCEndpointID)
	cr.Status.AtProvider.VpcEndpointID = res.VPCEndpointID
	cr.Status.AtProvider.State = res.State
	cr.Status.AtProvider.PrivateDNSName = res.PrivateDNSName

	return managed.ExternalCreation{
		ExternalNameAssigned: true,
		ConnectionDetails: managed.ConnectionDetails{
			"vpcEndpointId": []byte(res.VPCEndpointID),
			"dnsName":       []byte(res.PrivateDNSName),
		},
	}, nil
}

// Update is not supported for VPC endpoints
func (c *external) Update(_ context.Context, _ resource.Managed) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, errors.New("update is not supported for VPC endpoints")
}

// Delete removes the VPC endpoint
func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.VPCEndpoint)
	if !ok {
		return errors.New(errNotVPCEndpoint)
	}

	id := meta.GetExternalName(cr)
	if id == "" {
		return nil
	}

	c.logger.Debug("Deleting VPC endpoint", "id", id)
	return c.awsClient.DeleteVPCEndpoint(ctx, id)
}

// extractENIIDs extracts network interface IDs from NetworkInterface slice
func extractENIIDs(enis []svc.NetworkInterface) []string {
	ids := make([]string, len(enis))
	for i, eni := range enis {
		ids[i] = eni.NetworkInterfaceID
	}
	return ids
}
