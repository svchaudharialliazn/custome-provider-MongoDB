## 6. Organization Controller (internal/controller/organization/organization.go) - Pure AWS

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

package organization

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/svchaudhari/Swap-Provider-MongoDB/apis/organization/v1alpha1"
	apisv1alpha1 "github.com/svchaudhari/Swap-Provider-MongoDB/apis/v1alpha1"
	"github.com/svchaudhari/Swap-Provider-MongoDB/internal/clients/aws"
	"github.com/svchaudhari/Swap-Provider-MongoDB/internal/clients/mongodb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	errNotOrganization     = "managed resource is not a Organization custom resource"
	errTrackPCUsage        = "cannot track ProviderConfig usage"
	errGetPC               = "cannot get ProviderConfig"
	errGetCreds            = "cannot get credentials from AWS Secrets Manager"
	errNewClient           = "cannot create new Service"
	errCreateOrg           = "cannot create MongoDB organization"
	errDeleteOrg           = "cannot delete MongoDB organization"
	errGetOrg              = "cannot get MongoDB organization"
	errCreateAWSSecret     = "cannot create AWS Secrets Manager secret"
	errGetAWSSecret        = "cannot get AWS Secrets Manager secret"
	errDeleteAWSSecret     = "cannot delete AWS Secrets Manager secret"
	errValidateKMSKey      = "cannot validate KMS key"
	errInvalidPCConfig     = "ProviderConfig must use AWS Secrets Manager source"
	errCreateProviderCreds = "cannot get provider credentials from AWS"
)

// Setup adds a controller that reconciles Organization managed resources.
// COMPLETELY ELIMINATES connection publishers - NO Kubernetes secret creation
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.OrganizationGroupKind)

	// NO CONNECTION PUBLISHERS - Eliminates ALL Kubernetes secret creation
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.OrganizationGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:           mgr.GetClient(),
			usage:          resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newServiceFn:   mongodb.NewService,
			newAWSClientFn: aws.NewClient,
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))
		// REMOVED: managed.WithConnectionPublishers - NO Kubernetes secrets

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.Organization{}).
		Complete(r)
}

// A connector produces an ExternalClient with AWS-ONLY credential access
// ELIMINATES ALL Kubernetes secret interactions
type connector struct {
	kube           client.Client
	usage          resource.Tracker
	newServiceFn   func(creds mongodb.Credentials) mongodb.Service
	newAWSClientFn func(ctx context.Context, region string) (*aws.Client, error)
}

// Connect gets provider credentials from AWS Secrets Manager (NOT Kubernetes secrets)
// COMPLETELY ELIMINATES Kubernetes secret credential extraction
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return nil, errors.New(errNotOrganization)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	// Validate ProviderConfig uses AWS source ONLY - NO Kubernetes secrets allowed
	if pc.Spec.Credentials.Source != apisv1alpha1.CredentialsSourceAWS || pc.Spec.Credentials.AWS == nil || pc.Spec.Credentials.AWS.SecretsManager == nil {
		return nil, errors.New(errInvalidPCConfig)
	}

	// Create AWS client for provider credentials region
	awsCredRegion := pc.Spec.Credentials.AWS.SecretsManager.Region
	providerAWSClient, err := c.newAWSClientFn(ctx, awsCredRegion)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	// Get provider credentials from AWS Secrets Manager ONLY
	providerSecretName := pc.Spec.Credentials.AWS.SecretsManager.SecretName
	secretKey := "apiKey"
	if pc.Spec.Credentials.AWS.SecretsManager.SecretKey != nil {
		secretKey = *pc.Spec.Credentials.AWS.SecretsManager.SecretKey
	}

	apiKey, err := providerAWSClient.GetProviderCredentials(ctx, providerSecretName, secretKey)
	if err != nil {
		return nil, errors.Wrap(err, errCreateProviderCreds)
	}

	creds := mongodb.Credentials{
		APIKey: apiKey,
	}

	service := c.newServiceFn(creds)

	// Create AWS client for organization secrets region
	orgAwsClient, err := c.newAWSClientFn(ctx, cr.Spec.ForProvider.AWSSecretsConfig.Region)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{
		service:   service,
		awsClient: orgAwsClient,
		kube:      c.kube,
	}, nil
}

// An ExternalClient manages MongoDB resources with AWS-ONLY credential storage
// ELIMINATES ALL Kubernetes secret operations
type external struct {
	service   mongodb.Service
	awsClient *aws.Client
	kube      client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotOrganization)
	}

	// Check if organization exists in MongoDB Atlas
	orgID := meta.GetExternalName(cr)
	if orgID == "" {
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	org, err := c.service.GetOrganization(ctx, orgID)
	if err != nil {
		if mongodb.IsNotFoundError(err) {
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil
		}
		return managed.ExternalObservation{}, errors.Wrap(err, errGetOrg)
	}

	// Check if AWS secret exists
	secretName := c.generateSecretName(orgID, cr.Spec.ForProvider.AWSSecretsConfig.SecretName)
	_, err = c.awsClient.GetSecret(ctx, secretName)
	if err != nil {
		if aws.IsNotFoundError(err) {
			return managed.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil
		}
		return managed.ExternalObservation{}, errors.Wrap(err, errGetAWSSecret)
	}

	// Update status with organization information
	cr.Status.AtProvider.OrgID = org.ID
	cr.Status.AtProvider.SecretARN = c.generateSecretARN(secretName, cr.Spec.ForProvider.AWSSecretsConfig.Region)

	if cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID != nil {
		cr.Status.AtProvider.KMSKeyID = *cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID
	}

	cr.Status.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceUpToDate:        true,
		ResourceLateInitialized: false,
		// NO CONNECTION DETAILS - ELIMINATES Kubernetes secret creation entirely
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotOrganization)
	}

	cr.Status.SetConditions(xpv1.Creating())

	// Validate KMS key if specified
	if cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID != nil {
		if err := c.awsClient.ValidateKMSKey(ctx, *cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID); err != nil {
			return managed.ExternalCreation{}, errors.Wrap(err, errValidateKMSKey)
		}
	}

	// Create MongoDB organization
	orgInput := mongodb.CreateOrganizationInput{
		OwnerID: cr.Spec.ForProvider.OwnerID,
		APIKey: mongodb.APIKeyInput{
			Description: cr.Spec.ForProvider.APIKey.Description,
			Roles:       cr.Spec.ForProvider.APIKey.Roles,
		},
	}

	org, apiKey, err := c.service.CreateOrganization(ctx, orgInput)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateOrg)
	}

	// Set external name
	meta.SetExternalName(cr, org.ID)

	// Prepare credentials for AWS Secrets Manager ONLY
	credentials := aws.MongoDBAPICredentials{
		APIKey:      apiKey,
		Description: cr.Spec.ForProvider.APIKey.Description,
		OrgID:       org.ID,
		Roles:       cr.Spec.ForProvider.APIKey.Roles,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}

	// Store credentials in AWS Secrets Manager - NO Kubernetes secrets
	secretName := c.generateSecretName(org.ID, cr.Spec.ForProvider.AWSSecretsConfig.SecretName)
	secretOutput, err := c.awsClient.CreateSecret(ctx, secretName, credentials, cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
	if err != nil {
		// Attempt to clean up the created organization if secret creation fails
		if deleteErr := c.service.DeleteOrganization(ctx, org.ID); deleteErr != nil {
			fmt.Printf("Failed to cleanup organization %s after secret creation failure: %v\n", org.ID, deleteErr)
		}
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateAWSSecret)
	}

	// Update status with creation information
	cr.Status.AtProvider.OrgID = org.ID
	cr.Status.AtProvider.SecretARN = *secretOutput.ARN
	if secretOutput.KmsKeyId != nil {
		cr.Status.AtProvider.KMSKeyID = *secretOutput.KmsKeyId
	}
	cr.Status.AtProvider.CreatedAt = &metav1.Time{Time: time.Now()}

	// NO CONNECTION DETAILS PUBLISHED - ELIMINATES ALL Kubernetes secret creation
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotOrganization)
	}

	orgID := meta.GetExternalName(cr)
	if orgID == "" {
		return managed.ExternalUpdate{}, errors.New("organization ID not found")
	}

	// Update organization in MongoDB Atlas if needed
	updateInput := mongodb.UpdateOrganizationInput{
		ID: orgID,
		// Add any updatable fields here
	}

	org, err := c.service.UpdateOrganization(ctx, updateInput)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "cannot update MongoDB organization")
	}

	// Update credentials in AWS Secrets Manager if needed
	secretName := c.generateSecretName(orgID, cr.Spec.ForProvider.AWSSecretsConfig.SecretName)
	existingCredentials, err := c.awsClient.GetSecret(ctx, secretName)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetAWSSecret)
	}

	// Update the credentials with new information
	updatedCredentials := *existingCredentials
	updatedCredentials.Description = cr.Spec.ForProvider.APIKey.Description
	updatedCredentials.Roles = cr.Spec.ForProvider.APIKey.Roles

	_, err = c.awsClient.UpdateSecret(ctx, secretName, updatedCredentials)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "cannot update AWS secret")
	}

	// Update status
	cr.Status.AtProvider.OrgID = org.ID

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return errors.New(errNotOrganization)
	}

	cr.Status.SetConditions(xpv1.Deleting())

	orgID := meta.GetExternalName(cr)
	if orgID == "" {
		// Nothing to delete
		return nil
	}

	// Delete credentials from AWS Secrets Manager
	secretName := c.generateSecretName(orgID, cr.Spec.ForProvider.AWSSecretsConfig.SecretName)
	_, err := c.awsClient.DeleteSecret(ctx, secretName, false) // Use recovery window
	if err != nil && !aws.IsNotFoundError(err) {
		return errors.Wrap(err, errDeleteAWSSecret)
	}

	// Delete organization from MongoDB Atlas
	err = c.service.DeleteOrganization(ctx, orgID)
	if err != nil && !mongodb.IsNotFoundError(err) {
		return errors.Wrap(err, errDeleteOrg)
	}

	return nil
}

// generateSecretName creates a secret name, using custom name if provided or auto-generating
func (c *external) generateSecretName(orgID string, customName *string) string {
	if customName != nil && *customName != "" {
		return *customName
	}
	return aws.GenerateSecretName(orgID)
}

// generateSecretARN creates the full ARN for the secret
func (c *external) generateSecretARN(secretName, region string) string {
	return fmt.Sprintf("arn:aws:secretsmanager:%s:*:secret:%s", region, secretName)
}
```