package organization

import (
	"context"

	"github.com/pkg/errors"
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

	"github.com/svchaudhari/Swap-Provider-MongoDB/apis/organization/v1alpha1"
	apisv1alpha1 "github.com/svchaudhari/Swap-Provider-MongoDB/apis/v1alpha1"
	svc "github.com/svchaudhari/Swap-Provider-MongoDB/internal/clients/mongodb"
	awsclient "github.com/svchaudhari/Swap-Provider-MongoDB/internal/clients/aws"
	"github.com/svchaudhari/Swap-Provider-MongoDB/internal/controller/features"
)

const (
	errNotOrganization = "managed resource is not an Organization custom resource"
	errTrackPCUsage    = "cannot track ProviderConfig usage"
	errGetPC           = "cannot get ProviderConfig"
	errInvalidPCConfig = "ProviderConfig must use AWS Secret containing publicKey and privateKey"
	errAWSClient       = "cannot create AWS client"
	errNoOrgSecret     = "org API key secret not set in status"
	secretPrefix       = "product/mongodb/"
	errObserveExternal = "cannot observe external organization"
	errDeleteExternal  = "cannot delete external organization"
)

// ADD: Deletion finalizer constant
const (
	CredentialsSourceAWS            = "AWS"
	FinalizerOrganizationCleanup    = "organization.platform.allianz.io/cleanup"
)

func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.OrganizationGroupKind)
	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	r := managed.NewReconciler(
		mgr,
		resource.ManagedKind(v1alpha1.OrganizationGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:           mgr.GetClient(),
			usage:          resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			logger:         o.Logger,
			newServiceFn:   svc.NewService,
			newAWSClientFn: awsclient.NewClient,
		}),
		managed.WithInitializers(),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...),
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.Organization{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
	kube           client.Client
	usage          resource.Tracker
	logger         logging.Logger
	newServiceFn   func(creds svc.Credentials) svc.Service
	newAWSClientFn func(ctx context.Context, region string) (*awsclient.Client, error)
}

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
	if pc.Spec.Credentials.Source != CredentialsSourceAWS {
		return nil, errors.New(errInvalidPCConfig)
	}

	awsCfg := pc.Spec.Credentials.AWS.SecretsManager
	awsClient, err := c.newAWSClientFn(ctx, awsCfg.Region)
	if err != nil {
		return nil, errors.Wrap(err, errAWSClient)
	}

	credsAWS, err := awsClient.GetSecret(ctx, *awsCfg.SecretName)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get AWS secret from ProviderConfig")
	}
	creds := svc.Credentials{PublicKey: credsAWS.PublicKey, PrivateKey: credsAWS.PrivateKey}

	return &external{
		kube:         c.kube,
		client:       c.newServiceFn(creds),
		logger:       c.logger,
		awsClient:    awsClient,
		newServiceFn: c.newServiceFn,
	}, nil
}

type external struct {
	kube         client.Client
	client       svc.Service
	logger       logging.Logger
	awsClient    *awsclient.Client
	newServiceFn func(creds svc.Credentials) svc.Service
}

// derive final secret name
func finalSecretName(cr *v1alpha1.Organization) string {
	if cr.Spec.ForProvider.AWSSecretsConfig.SecretName != nil &&
		*cr.Spec.ForProvider.AWSSecretsConfig.SecretName != "" {
		return secretPrefix + *cr.Spec.ForProvider.AWSSecretsConfig.SecretName
	}
	return secretPrefix + cr.Name
}

// ADD: Enhanced Observe with deletion state detection
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)
	if orgID == "" {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	cr.Status.AtProvider.OrgID = orgID

	// ADD: Check if resource is marked for deletion
	if cr.DeletionTimestamp != nil {
		c.logger.Debug("Organization marked for deletion", "name", cr.Name, "orgID", orgID)
		cr.SetConditions(xpv1.Deleting())
		return managed.ExternalObservation{ResourceExists: true}, nil
	}

	// refresh secret metadata
	secretName := finalSecretName(cr)
	desc, err := c.awsClient.DescribeSecret(ctx, secretName)
	if err != nil {
		c.logger.Debug("Failed to describe secret", "error", err, "secretName", secretName)
		// Don't fail if secret doesn't exist - it might be cleaned up
		cr.SetConditions(xpv1.Available())
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}
	
	if desc.ARN != nil {
		cr.Status.AtProvider.SecretName = secretName
		cr.Status.AtProvider.SecretARN = *desc.ARN
	}

	cr.SetConditions(xpv1.Available())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr := mg.(*v1alpha1.Organization)

	org, apiKey, err := c.client.CreateOrganization(ctx, svc.CreateOrganizationInput{
		Name:    cr.Name,
		OwnerID: cr.Spec.ForProvider.OwnerID,
		APIKey: svc.APIKey{
			Description: cr.Spec.ForProvider.APIKey.Description,
			Roles:       cr.Spec.ForProvider.APIKey.Roles,
		},
	})
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	meta.SetExternalName(cr, org.ID)
	cr.Status.AtProvider.OrgID = org.ID
	cr.Status.AtProvider.OrgName = org.Name

	secretName := finalSecretName(cr)
	creds := awsclient.MongoDBAPICredentials{
		PublicKey:  apiKey.PublicKey,
		PrivateKey: apiKey.PrivateKey,
	}
	arn, err := c.awsClient.PutSecret(ctx, secretName, creds, org.ID, cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to put secret")
	}
	cr.Status.AtProvider.SecretName = secretName
	cr.Status.AtProvider.SecretARN = arn

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{
			"publicKey":  []byte(apiKey.PublicKey),
			"privateKey": []byte(apiKey.PrivateKey),
			"secretARN":  []byte(arn),
		},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, nil
}

// ADD: Enhanced Delete with finalizer management and error handling
func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)

	if orgID == "" {
		c.logger.Debug("Delete called but externalName (orgID) is empty — no external delete API will be invoked")
		return nil
	}

	// Debug: Starting delete workflow
	c.logger.Debug("Delete() invoked for Organization",
		"orgID", orgID,
		"name", cr.Name,
		"finalizerPresent", meta.FinalizerExists(cr, FinalizerOrganizationCleanup),
	)

	// ============================================================
	// 1. Add finalizer if missing
	// ============================================================
	if !meta.FinalizerExists(cr, FinalizerOrganizationCleanup) {
		c.logger.Debug("Finalizer missing — adding finalizer before delete", 
			"orgID", orgID,
		)
		meta.AddFinalizer(cr, FinalizerOrganizationCleanup)
		return nil
	}

	// ============================================================
	// 2. DEBUG: SHOW EXACT MONGODB ATLAS API CALL BEING TRIGGERED
	// ============================================================
	c.logger.Debug("Calling MongoDB Atlas API → DeleteOrganization()",
		"api", "DELETE /api/atlas/v2/orgs/{orgId}",
		"orgID", orgID,
	)

	err := c.client.DeleteOrganization(ctx, orgID)

	// 404 means already deleted
	if svc.IsNotFoundError(err) {
		c.logger.Debug("MongoDB Atlas API returned 404 — org already deleted",
			"orgID", orgID,
		)

		// ============================================================
		// AWS Secret Delete Debug
		// ============================================================
		secretName := finalSecretName(cr)
		c.logger.Debug("Secret cleanup step: calling AWS DeleteSecret()",
			"secretName", secretName,
			"awsAPI", "DeleteSecret",
		)
		if delErr := c.awsClient.DeleteSecret(ctx, secretName, true); delErr != nil {
			c.logger.Debug("AWS DeleteSecret failed",
				"secretName", secretName,
				"error", delErr,
			)
		}

		meta.RemoveFinalizer(cr, FinalizerOrganizationCleanup)
		cr.SetConditions(xpv1.ReconcileSuccess())
		return nil
	}

	// Handle other failures
	if err != nil {
		c.logger.Debug("Atlas DeleteOrganization() returned error",
			"orgID", orgID,
			"error", err,
		)
		cr.SetConditions(xpv1.ReconcileError(err))
		return errors.Wrap(err, errDeleteExternal)
	}

	// ============================================================
	// 3. SUCCESS — remove finalizer
	// ============================================================
	c.logger.Debug("MongoDB Atlas organization deleted successfully",
		"orgID", orgID,
	)

	// ============================================================
	// AWS Secret Delete Debug
	// ============================================================
	secretName := finalSecretName(cr)
	c.logger.Debug("Secret cleanup step: calling AWS DeleteSecret()",
		"secretName", secretName,
		"awsAPI", "DeleteSecret",
	)

	if delErr := c.awsClient.DeleteSecret(ctx, secretName, true); delErr != nil {
		c.logger.Debug("AWS DeleteSecret failed",
			"secretName", secretName,
			"error", delErr,
		)
	}

	meta.RemoveFinalizer(cr, FinalizerOrganizationCleanup)
	cr.SetConditions(xpv1.ReconcileSuccess())
	return nil
}
