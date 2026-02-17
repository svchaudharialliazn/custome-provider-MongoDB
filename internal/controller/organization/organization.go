// internal/controller/organization/organization.go
package organization

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	"github.com/svchaudharialliazn/swapnil-provider-mongodb/apis/organization/v1alpha1"
	apisv1alpha1 "github.com/svchaudharialliazn/swapnil-provider-mongodb/apis/v1alpha1"
	awsclient "github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/clients/aws"
	svc "github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/clients/mongodb"
	"github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/controller/features"
)

const (
	errNotOrganization   = "managed resource is not an Organization custom resource"
	errTrackPCUsage      = "cannot track ProviderConfig usage"
	errGetPC             = "cannot get ProviderConfig"
	errInvalidPCConfig   = "ProviderConfig must use AWS Secret containing publicKey and privateKey"
	errAWSClient         = "cannot create AWS client"
	secretPrefix         = "product/mongodb/"
	CredentialsSourceAWS = "AWS"

	FinalizerOrganizationCleanup = "organization.platform.allianz.io/cleanup"
	crossplaneManagedFinalizer   = "finalizer.managedresource.crossplane.io"
)

// Setup wires up the controller to the manager.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.OrganizationGroupKind)

	cps := []managed.ConnectionPublisher{
		managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme()),
	}
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

	if cr.GetProviderConfigReference() == nil {
		return nil, errors.New(errGetPC)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	if pc.Spec.Credentials.Source != CredentialsSourceAWS {
		return nil, errors.New(errInvalidPCConfig)
	}

	awsCfg := pc.Spec.Credentials.AWS.SecretsManager
	if awsCfg == nil {
		return nil, errors.New("ProviderConfig AWS secrets manager config missing")
	}
	if awsCfg.SecretName == nil || *awsCfg.SecretName == "" {
		return nil, errors.New("ProviderConfig AWS secret name cannot be empty")
	}

	awsClient, err := c.newAWSClientFn(ctx, awsCfg.Region)
	if err != nil {
		return nil, errors.Wrap(err, errAWSClient)
	}

	credsAWS, err := awsClient.GetSecret(ctx, *awsCfg.SecretName)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get AWS secret from ProviderConfig")
	}

	creds := svc.Credentials{
		PublicKey:  credsAWS.PublicKey,
		PrivateKey: credsAWS.PrivateKey,
	}

	return &external{
		kube:         c.kube,
		client:       c.newServiceFn(creds),
		logger:       c.logger,
		awsClient:    awsClient,
		newServiceFn: c.newServiceFn,
		cr:           cr,
		awsCfg:       awsCfg,
	}, nil
}

type external struct {
	kube         client.Client
	client       svc.Service
	logger       logging.Logger
	awsClient    *awsclient.Client
	newServiceFn func(creds svc.Credentials) svc.Service
	cr           *v1alpha1.Organization
	awsCfg       *apisv1alpha1.AWSSecretsManagerReference
}

func finalSecretName(cr *v1alpha1.Organization) string {
	if cr.Spec.ForProvider.AWSSecretsConfig.SecretName != nil && *cr.Spec.ForProvider.AWSSecretsConfig.SecretName != "" {
		return secretPrefix + *cr.Spec.ForProvider.AWSSecretsConfig.SecretName
	}
	return secretPrefix + cr.Name
}

func maskAPIKey(k string) string {
	if len(k) < 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

func containsRole(list []string, role string) bool {
	for _, r := range list {
		if r == role {
			return true
		}
	}
	return false
}

func isUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "401") || strings.Contains(e, "unauthorized") || strings.Contains(e, "forbidden") || strings.Contains(e, "403")
}

func isAtlasTransientOrOpaqueDeleteErr(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	if strings.Contains(e, "api error 0") {
		return true
	}
	if strings.Contains(e, "internal server error") || strings.Contains(e, "500") {
		return true
	}
	if strings.Contains(e, "context canceled") || strings.Contains(e, "context cancelled") {
		return true
	}
	if strings.Contains(e, "network error") || strings.Contains(e, "request canceled") || strings.Contains(e, "retryable") {
		return true
	}
	return false
}

// updateStatusWithRetry updates status and retries once on resourceVersion conflict.
func (c *external) updateStatusWithRetry(ctx context.Context, obj *v1alpha1.Organization) error {
	if err := c.kube.Status().Update(ctx, obj); err != nil {
		if apierrors.IsConflict(err) {
			latest := &v1alpha1.Organization{}
			if gerr := c.kube.Get(ctx, types.NamespacedName{Name: obj.Name}, latest); gerr != nil {
				return errors.Wrap(gerr, "re-get after status conflict")
			}
			latest.Status = obj.Status
			return c.kube.Status().Update(ctx, latest)
		}
		return err
	}
	return nil
}

// enableAPIAccessListRequired enables the "Require IP Access List for the Atlas Administration API"
// setting on the organization using the v2 API endpoint.
func (c *external) enableAPIAccessListRequired(
	ctx context.Context,
	orgID string,
	apiKey svc.APIKeyPair,
) error {
	c.logger.Info("Enabling apiAccessListRequired for organization", "orgID", orgID)

	// Use the org-specific API key credentials to update settings
	orgService := c.newServiceFn(svc.Credentials{
		PublicKey:  apiKey.PublicKey,
		PrivateKey: apiKey.PrivateKey,
	})

	if err := orgService.SetOrgAdminAPIIPEnforcement(ctx, orgID, true); err != nil {
		c.logger.Info("Failed to enable apiAccessListRequired", "orgID", orgID, "error", err)
		return errors.Wrap(err, "failed to enable apiAccessListRequired")
	}

	c.logger.Info("Successfully enabled apiAccessListRequired", "orgID", orgID)
	return nil
}

// provisionIPAccess adds desired IPs to the org API key access list using a single bulk POST.
func (c *external) provisionIPAccess(
	ctx context.Context,
	orgID string,
	ipEntries []v1alpha1.IPAccessEntry,
	org *svc.Organization,
	apiKey svc.APIKeyPair,
) ([]v1alpha1.IPAccessEntry, error) {

	if len(ipEntries) == 0 {
		c.logger.Debug("No IPs to provision")
		return []v1alpha1.IPAccessEntry{}, nil
	}

	orgService := c.newServiceFn(svc.Credentials{
		PublicKey:  apiKey.PublicKey,
		PrivateKey: apiKey.PrivateKey,
	})

	// Resolve API key ID for the newly created org API key
	apiKeyID, err := orgService.FindAPIKeyID(ctx, orgID, apiKey.PublicKey, "")
	if err != nil || apiKeyID == "" {
		c.logger.Info("Could not resolve organization API key ID for IP access provisioning; skipping",
			"orgID", orgID, "error", err)
		return nil, nil
	}

	// Build bulk payload (omit comment to avoid 400 on API-key access list)
	bulk := make([]svc.AddIPInput, 0, len(ipEntries))
	for _, e := range ipEntries {
		bulk = append(bulk, svc.AddIPInput{IP: e.IP})
	}

	c.logger.Info("Bulk adding IPs to org API key access list",
		"orgID", orgID, "apiKeyID", apiKeyID, "count", len(bulk))

	if err := orgService.AddIPsToAPIKeyAccessList(ctx, orgID, apiKeyID, bulk); err != nil {
		c.logger.Info("Failed bulk add to org API key access list",
			"orgID", orgID, "apiKeyID", apiKeyID, "error", err)
		return nil, errors.Wrap(err, "bulk add IPs to org API key access list")
	}

	// Prepare status
	now := metav1.Now()
	provisioned := make([]v1alpha1.IPAccessEntry, 0, len(ipEntries))
	for _, e := range ipEntries {
		entry := e
		entry.CIDRBlock = e.IP + "/32"
		entry.CreatedAt = &now
		provisioned = append(provisioned, entry)
	}

	c.logger.Info("Successfully bulk provisioned IP access for org API key",
		"orgID", orgID, "apiKeyID", apiKeyID, "count", len(provisioned))

	return provisioned, nil
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)

	// Restore external-name from status if it was cleared in the manifest.
	if orgID == "" && cr.Status.AtProvider.OrgID != "" {
		restoreID := cr.Status.AtProvider.OrgID
		c.logger.Debug("Restoring external-name from status", "name", cr.Name, "orgID", restoreID)

		meta.SetExternalName(cr, restoreID)
		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, "fetching latest object to restore external-name from status")
		}
		meta.SetExternalName(latest, restoreID)
		if err := c.kube.Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
					meta.SetExternalName(latest, restoreID)
					if err3 := c.kube.Update(ctx, latest); err3 != nil {
						return managed.ExternalObservation{}, errors.Wrap(err3, "updating external-name after conflict (restore)")
					}
				} else {
					return managed.ExternalObservation{}, errors.Wrap(err2, "re-get after conflict for external-name restore")
				}
			} else {
				return managed.ExternalObservation{}, errors.Wrap(err, "updating external-name after restore")
			}
		}

		// After restoring external-name, trigger Update to reconcile desired settings.
		cr.SetConditions(xpv1.Available())
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
	}

	// No external name: try to adopt by name.
	if orgID == "" {
		c.logger.Debug("External name not set — searching Atlas by organization name", "name", cr.Name)

		found, err := c.client.GetOrganizationByName(ctx, cr.Name)
		if err != nil {
			// To avoid duplicate creation, do not proceed to create when we cannot verify by name.
			if isUnauthorizedError(err) || svc.IsRetryableError(err) {
				msg := "adoption-by-name blocked (auth/network); cannot safely create"
				c.logger.Info(msg, "error", err)
				return managed.ExternalObservation{}, errors.Wrap(err, msg)
			}
			if svc.IsNotFoundError(err) {
				return managed.ExternalObservation{ResourceExists: false}, nil
			}
			return managed.ExternalObservation{}, errors.Wrap(err, "searching organization by name")
		}

		if found != nil {
			// Adopt existing org by name (regardless of ownerID) to avoid duplicates.
			meta.SetExternalName(cr, found.ID)
			cr.Status.AtProvider.OrgID = found.ID
			cr.Status.AtProvider.OrgName = found.Name
			stateActive := v1alpha1.OrganizationStateActive
			cr.Status.AtProvider.State = &stateActive

			latest := &v1alpha1.Organization{}
			if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
				return managed.ExternalObservation{}, errors.Wrap(err, "fetching latest object to persist external-name")
			}
			meta.SetExternalName(latest, found.ID)
			if err := c.kube.Update(ctx, latest); err != nil {
				if apierrors.IsConflict(err) {
					if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
						meta.SetExternalName(latest, found.ID)
						if err3 := c.kube.Update(ctx, latest); err3 != nil {
							return managed.ExternalObservation{}, errors.Wrap(err3, "updating external-name after conflict")
						}
					} else {
						return managed.ExternalObservation{}, errors.Wrap(err2, "re-get after conflict for external-name update")
					}
				} else {
					return managed.ExternalObservation{}, errors.Wrap(err, "updating external-name after adopting existing org")
				}
			}
			latest.Status = cr.Status
			if err := c.kube.Status().Update(ctx, latest); err != nil {
				return managed.ExternalObservation{}, errors.Wrap(err, "updating status after adopting existing org")
			}

			// After adoption, trigger Update to reconcile desired IPs/enforcement.
			return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
		}

		// Not found: proceed to create.
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// Handle deletion short-circuit.
	if cr.DeletionTimestamp != nil {
		c.logger.Debug("Organization marked for deletion", "name", cr.Name, "orgID", orgID)
		cr.SetConditions(xpv1.Deleting())
		return managed.ExternalObservation{ResourceExists: true}, nil
	}

	// Update secret metadata if present.
	secretName := finalSecretName(cr)
	desc, err := c.awsClient.DescribeSecret(ctx, secretName)
	if err == nil && desc.ARN != nil {
		cr.Status.AtProvider.SecretARN = *desc.ARN
		cr.Status.AtProvider.SecretName = secretName
	}

	// Decide if resource is up to date with desired spec.
	upToDate := true

	// Compare desired vs current secret name to trigger Update for migration.
	if cr.Status.AtProvider.SecretName != "" && cr.Status.AtProvider.SecretName != secretName {
		upToDate = false
	}

	// Compare enforcement flag
	desiredEnforcement := cr.Spec.ForProvider.NetworkAccessConfig.APIAccessListRequired
	currentEnforcement := cr.Status.AtProvider.APIAccessListRequired
	if currentEnforcement == nil || *currentEnforcement != desiredEnforcement {
		upToDate = false
	}

	// Compare desired IPs vs provisioned IPs (only if enabled)
	if cr.Spec.ForProvider.NetworkAccessConfig.Enabled {
		desiredIPs := make(map[string]struct{}, len(cr.Spec.ForProvider.NetworkAccessConfig.IPs))
		for _, e := range cr.Spec.ForProvider.NetworkAccessConfig.IPs {
			desiredIPs[e.IP] = struct{}{}
		}
		currentIPs := make(map[string]struct{}, len(cr.Status.AtProvider.ProvisionedIPs))
		for _, e := range cr.Status.AtProvider.ProvisionedIPs {
			currentIPs[e.IP] = struct{}{}
		}
		// If counts differ or any mismatched member, mark not up-to-date.
		if len(desiredIPs) != len(currentIPs) {
			upToDate = false
		} else {
			for ip := range desiredIPs {
				if _, ok := currentIPs[ip]; !ok {
					upToDate = false
					break
				}
			}
		}
	}

	cr.SetConditions(xpv1.Available())
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: upToDate,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr := mg.(*v1alpha1.Organization)

	// If external-name is set, or can be restored from status, skip creation.
	if en := meta.GetExternalName(cr); en != "" {
		c.logger.Info("External name already set; skipping creation", "orgID", en)
		return managed.ExternalCreation{}, nil
	}
	if cr.Status.AtProvider.OrgID != "" {
		restoreID := cr.Status.AtProvider.OrgID
		c.logger.Info("External name empty but status has OrgID — restoring and skipping create", "orgID", restoreID)

		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			return managed.ExternalCreation{}, errors.Wrap(err, "re-get before restoring external-name in create")
		}
		meta.SetExternalName(latest, restoreID)
		if err := c.kube.Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
					meta.SetExternalName(latest, restoreID)
					if err3 := c.kube.Update(ctx, latest); err3 != nil {
						return managed.ExternalCreation{}, errors.Wrap(err3, "persisting external-name restore after conflict")
					}
				} else {
					return managed.ExternalCreation{}, errors.Wrap(err2, "re-get after conflict while restoring external-name")
				}
			} else {
				return managed.ExternalCreation{}, errors.Wrap(err, "persisting external-name restore")
			}
		}
		return managed.ExternalCreation{}, nil
	}

	// Guard: ensure we don't create duplicates when we can't verify uniqueness by name.
	existingOrg, err := c.client.GetOrganizationByName(ctx, cr.Name)
	if err != nil && !svc.IsNotFoundError(err) {
		if isUnauthorizedError(err) || svc.IsRetryableError(err) {
			msg := "cannot safely create: adoption-by-name failed due to authorization/network constraints"
			c.logger.Info(msg, "error", err)
			return managed.ExternalCreation{}, errors.Wrap(err, msg)
		}
		return managed.ExternalCreation{}, errors.Wrap(err, "checking existing organization")
	}
	if existingOrg != nil {
		// Reuse existing org by name (avoid duplicates).
		c.logger.Info("Organization already exists in Atlas; adopting by name",
			"orgID", existingOrg.ID, "orgName", existingOrg.Name)

		meta.SetExternalName(cr, existingOrg.ID)
		cr.Status.AtProvider.OrgID = existingOrg.ID
		cr.Status.AtProvider.OrgName = existingOrg.Name
		stateActive := v1alpha1.OrganizationStateActive
		cr.Status.AtProvider.State = &stateActive

		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			return managed.ExternalCreation{}, errors.Wrap(err, "re-get before persisting external-name")
		}
		meta.SetExternalName(latest, existingOrg.ID)
		if err := c.kube.Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
					meta.SetExternalName(latest, existingOrg.ID)
					if err3 := c.kube.Update(ctx, latest); err3 != nil {
						return managed.ExternalCreation{}, errors.Wrap(err3, "updating external-name after conflict")
					}
				} else {
					return managed.ExternalCreation{}, errors.Wrap(err2, "re-get after conflict")
				}
			} else {
				return managed.ExternalCreation{}, errors.Wrap(err, "updating external-name after reuse")
			}
		}
		latest.Status = cr.Status
		if err := c.kube.Status().Update(ctx, latest); err != nil {
			return managed.ExternalCreation{}, errors.Wrap(err, "updating status after reuse")
		}
		return managed.ExternalCreation{}, nil
	}

	// Add our cleanup finalizer up front.
	if !meta.FinalizerExists(cr, FinalizerOrganizationCleanup) {
		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			return managed.ExternalCreation{}, errors.Wrap(err, "re-get before persisting finalizer")
		}
		if !meta.FinalizerExists(latest, FinalizerOrganizationCleanup) {
			meta.AddFinalizer(latest, FinalizerOrganizationCleanup)
			if err := c.kube.Update(ctx, latest); err != nil {
				return managed.ExternalCreation{}, errors.Wrap(err, "failed to persist finalizer on creation")
			}
		}
	}

	c.logger.Info("Creating MongoDB Atlas Organization", "name", cr.Name, "ownerID", cr.Spec.ForProvider.OwnerID)

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

	// Persist external name and status
	meta.SetExternalName(cr, org.ID)
	cr.Status.AtProvider.OrgID = org.ID
	cr.Status.AtProvider.OrgName = org.Name
	cr.Status.AtProvider.CreatedWithCredentialSource = CredentialsSourceAWS
	cr.Status.AtProvider.CreatedWithAPIKeyID = maskAPIKey(apiKey.PublicKey)
	cr.Status.AtProvider.APIKeyRoles = cr.Spec.ForProvider.APIKey.Roles

	latest := &v1alpha1.Organization{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "re-get before persisting external-name after create")
	}
	meta.SetExternalName(latest, org.ID)
	if err := c.kube.Update(ctx, latest); err != nil {
		if apierrors.IsConflict(err) {
			if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
				meta.SetExternalName(latest, org.ID)
				if err3 := c.kube.Update(ctx, latest); err3 != nil {
					return managed.ExternalCreation{}, errors.Wrap(err3, "updating external-name after conflict on create")
				}
			} else {
				return managed.ExternalCreation{}, errors.Wrap(err2, "re-get after conflict on create")
			}
		} else {
			return managed.ExternalCreation{}, errors.Wrap(err, "updating external-name after create")
		}
	}

	// Store org API key in AWS Secrets Manager
	secretName := finalSecretName(cr)
	arn, err := c.awsClient.PutSecret(ctx, secretName, awsclient.MongoDBAPICredentials{
		PublicKey:  apiKey.PublicKey,
		PrivateKey: apiKey.PrivateKey,
	}, org.ID, cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to store API key in AWS Secrets Manager")
	}
	cr.Status.AtProvider.SecretARN = arn
	cr.Status.AtProvider.SecretName = secretName
	stateActive := v1alpha1.OrganizationStateActive
	cr.Status.AtProvider.State = &stateActive

	// Provision IP access FIRST (before enabling enforcement)
	if cr.Spec.ForProvider.NetworkAccessConfig.Enabled && len(cr.Spec.ForProvider.NetworkAccessConfig.IPs) > 0 {
		c.logger.Info("Provisioning IP access for new organization",
			"orgID", org.ID, "ipCount", len(cr.Spec.ForProvider.NetworkAccessConfig.IPs))

		provisionedIPs, perr := c.provisionIPAccess(ctx, org.ID, cr.Spec.ForProvider.NetworkAccessConfig.IPs, org, apiKey)
		if perr != nil {
			c.logger.Info("Failed to provision IP access (will retry on next reconcile)", "orgID", org.ID, "error", perr)
			cr.Status.AtProvider.IPAccessError = perr.Error()
		} else {
			cr.Status.AtProvider.ProvisionedIPs = provisionedIPs
			cr.Status.AtProvider.IPAccessEntryCount = len(provisionedIPs)
			now := metav1.Now()
			cr.Status.AtProvider.LastIPAccessUpdate = &now
			cr.Status.AtProvider.IPAccessError = ""
			c.logger.Info("IP access provisioning completed", "orgID", org.ID, "provisionedCount", len(provisionedIPs))
		}
	}

	// Enable apiAccessListRequired AFTER IPs are provisioned
	if cr.Spec.ForProvider.NetworkAccessConfig.APIAccessListRequired {
		c.logger.Info("Enabling apiAccessListRequired for organization after IP provisioning",
			"orgID", org.ID)

		if err := c.enableAPIAccessListRequired(ctx, org.ID, apiKey); err != nil {
			c.logger.Info("Failed to enable apiAccessListRequired (will retry on next reconcile)",
				"orgID", org.ID, "error", err)
			cr.Status.AtProvider.EnforcementError = err.Error()
		} else {
			enforced := true
			cr.Status.AtProvider.APIAccessListRequired = &enforced
			now := metav1.Now()
			cr.Status.AtProvider.LastEnforcementToggleTime = &now
			cr.Status.AtProvider.EnforcementError = ""
			c.logger.Info("Successfully enabled apiAccessListRequired", "orgID", org.ID)
		}
	}

	if err := c.updateStatusWithRetry(ctx, cr); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "updating status after create")
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{
			"publicKey":  []byte(apiKey.PublicKey),
			"privateKey": []byte(apiKey.PrivateKey),
			"secretARN":  []byte(arn),
		},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)
	if orgID == "" {
		return managed.ExternalUpdate{}, nil
	}

	now := metav1.Now()
	statusChanged := false

	// Determine desired and current secret names
	desiredSecretName := finalSecretName(cr)
	currentSecretName := cr.Status.AtProvider.SecretName
	var secretNameUsed string

	// Fetch org credentials from AWS SM (prefer current secret from status)
	var orgCreds *awsclient.MongoDBAPICredentials
	if currentSecretName != "" {
		creds, err := c.awsClient.GetSecret(ctx, currentSecretName)
		if err == nil {
			orgCreds = creds
			secretNameUsed = currentSecretName
		} else {
			c.logger.Info("Failed to read current secret; will try desired", "currentSecretName", currentSecretName, "error", err)
		}
	}
	if orgCreds == nil {
		creds, err := c.awsClient.GetSecret(ctx, desiredSecretName)
		if err != nil {
			c.logger.Info("Failed to read desired secret for update", "desiredSecretName", desiredSecretName, "error", err)
			return managed.ExternalUpdate{}, errors.Wrap(err, "failed to fetch org credentials for update")
		}
		orgCreds = creds
		secretNameUsed = desiredSecretName
	}

	orgService := c.newServiceFn(svc.Credentials{
		PublicKey:  orgCreds.PublicKey,
		PrivateKey: orgCreds.PrivateKey,
	})

	// Secret migration if spec name changed
	if currentSecretName != "" && desiredSecretName != currentSecretName {
		c.logger.Info("Migrating AWS secret to new name per spec change", "oldName", currentSecretName, "newName", desiredSecretName)
		arn, err := c.awsClient.PutSecret(ctx, desiredSecretName, awsclient.MongoDBAPICredentials{
			PublicKey:  orgCreds.PublicKey,
			PrivateKey: orgCreds.PrivateKey,
		}, orgID, cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
		if err != nil {
			c.logger.Info("Failed secret migration", "oldName", currentSecretName, "newName", desiredSecretName, "error", err)
			return managed.ExternalUpdate{}, errors.Wrap(err, "failed to migrate AWS secret to new name")
		}
		// Update status with new secret details
		cr.Status.AtProvider.SecretARN = arn
		cr.Status.AtProvider.SecretName = desiredSecretName
		statusChanged = true

		// Best-effort delete of old secret
		if delErr := c.awsClient.DeleteSecret(ctx, currentSecretName, true); delErr != nil {
			c.logger.Info("Failed to delete old AWS secret after migration (continuing)", "oldName", currentSecretName, "error", delErr)
		}

		secretNameUsed = desiredSecretName
	}

	// Update KMS Key ID on the existing secret if provided:
	// Use PutSecret on the same name to update KMS association (no UpdateSecretKMSKey method).
	if cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID != nil {
		c.logger.Info("Ensuring AWS secret has desired KMS Key ID", "secretName", secretNameUsed, "kmsKeyID", *cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
		arn, err := c.awsClient.PutSecret(ctx, secretNameUsed, awsclient.MongoDBAPICredentials{
			PublicKey:  orgCreds.PublicKey,
			PrivateKey: orgCreds.PrivateKey,
		}, orgID, cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
		if err != nil {
			c.logger.Info("Failed to update secret KMS Key ID", "secretName", secretNameUsed, "error", err)
			return managed.ExternalUpdate{}, errors.Wrap(err, "failed to update AWS secret KMS Key ID")
		}
		cr.Status.AtProvider.SecretARN = arn
		statusChanged = true
	}

	// Update API key description (if provided)
	apiKeyID, err := orgService.FindAPIKeyID(ctx, orgID, orgCreds.PublicKey, "")
	if err != nil || apiKeyID == "" {
		c.logger.Info("Could not resolve organization API key ID for API key updates; skipping", "orgID", orgID, "error", err)
	} else {
		desiredDesc := cr.Spec.ForProvider.APIKey.Description
		if strings.TrimSpace(desiredDesc) != "" {
			c.logger.Info("Updating org API key description", "orgID", orgID, "apiKeyID", apiKeyID)
			if err := orgService.UpdateOrgAPIKeyDescription(ctx, orgID, apiKeyID, desiredDesc); err != nil {
				c.logger.Info("Failed to update API key description", "orgID", orgID, "error", err)
				cr.Status.AtProvider.EnforcementError = errors.Wrap(err, "update API key description").Error()
				statusChanged = true
			}
		}

		// Roles: reconcile differences
		desiredRoles := cr.Spec.ForProvider.APIKey.Roles
		if len(desiredRoles) > 0 && !equalStringSets(cr.Status.AtProvider.APIKeyRoles, desiredRoles) {
			c.logger.Info("Updating org API key roles", "orgID", orgID, "apiKeyID", apiKeyID, "roles", desiredRoles)
			if err := orgService.UpdateOrgAPIKeyRoles(ctx, orgID, apiKeyID, desiredRoles); err != nil {
				c.logger.Info("Failed to update API key roles", "orgID", orgID, "error", err)
				cr.Status.AtProvider.EnforcementError = errors.Wrap(err, "update API key roles").Error()
				statusChanged = true
			} else {
				cr.Status.AtProvider.APIKeyRoles = desiredRoles
				statusChanged = true
			}
		}
	}

	// Handle apiAccessListRequired enforcement updates
	desiredEnforcement := cr.Spec.ForProvider.NetworkAccessConfig.APIAccessListRequired
	currentEnforcement := cr.Status.AtProvider.APIAccessListRequired

	needsEnforcementUpdate := false
	if currentEnforcement == nil {
		needsEnforcementUpdate = desiredEnforcement // only update if desired true
	} else if *currentEnforcement != desiredEnforcement {
		needsEnforcementUpdate = true
	}

	if needsEnforcementUpdate {
		c.logger.Info("Updating apiAccessListRequired setting",
			"orgID", orgID, "desired", desiredEnforcement)

		if err := orgService.SetOrgAdminAPIIPEnforcement(ctx, orgID, desiredEnforcement); err != nil {
			c.logger.Info("Failed to update apiAccessListRequired", "orgID", orgID, "error", err)
			cr.Status.AtProvider.EnforcementError = err.Error()
			statusChanged = true
		} else {
			cr.Status.AtProvider.APIAccessListRequired = &desiredEnforcement
			cr.Status.AtProvider.LastEnforcementToggleTime = &now
			cr.Status.AtProvider.EnforcementError = ""
			statusChanged = true
			c.logger.Info("Successfully updated apiAccessListRequired",
				"orgID", orgID, "enabled", desiredEnforcement)
		}
	}

	// Handle IP access updates (only if enabled)
	if cr.Spec.ForProvider.NetworkAccessConfig.Enabled {
		c.logger.Debug("Checking for IP access updates",
			"orgID", orgID, "desiredIPCount", len(cr.Spec.ForProvider.NetworkAccessConfig.IPs))

		apiKeyID, err := orgService.FindAPIKeyID(ctx, orgID, orgCreds.PublicKey, "")
		if err != nil || apiKeyID == "" {
			c.logger.Info("Could not resolve organization API key ID for IP update; skipping",
				"orgID", orgID, "error", err)
		} else {
			// Build sets
			currentIPs := make(map[string]bool)
			for _, ip := range cr.Status.AtProvider.ProvisionedIPs {
				currentIPs[ip.IP] = true
			}
			desiredIPMap := make(map[string]v1alpha1.IPAccessEntry)
			for _, ip := range cr.Spec.ForProvider.NetworkAccessConfig.IPs {
				desiredIPMap[ip.IP] = ip
			}

			// Bulk add new IPs
			toAdd := make([]svc.AddIPInput, 0)
			for desiredIP := range desiredIPMap {
				if !currentIPs[desiredIP] {
					toAdd = append(toAdd, svc.AddIPInput{IP: desiredIP})
				}
			}
			if len(toAdd) > 0 {
				c.logger.Info("Bulk adding new IPs to org API key access list",
					"orgID", orgID, "apiKeyID", apiKeyID, "count", len(toAdd))
				if err := orgService.AddIPsToAPIKeyAccessList(ctx, orgID, apiKeyID, toAdd); err != nil {
					c.logger.Info("Failed bulk add of new IPs", "error", err)
					cr.Status.AtProvider.IPAccessError = errors.Wrap(err, "bulk add new IPs").Error()
					statusChanged = true
				}
			}

			// Remove obsolete IPs
			for currentIP := range currentIPs {
				if _, ok := desiredIPMap[currentIP]; !ok {
					c.logger.Info("Removing IP from org API key access list",
						"orgID", orgID, "apiKeyID", apiKeyID, "ip", currentIP)
					if err := orgService.RemoveIPFromAPIKeyAccessList(ctx, orgID, apiKeyID, currentIP); err != nil {
						c.logger.Info("Failed to remove IP (continuing)", "ip", currentIP, "error", err)
					}
				}
			}

			// Update status
			provisioned := make([]v1alpha1.IPAccessEntry, 0, len(cr.Spec.ForProvider.NetworkAccessConfig.IPs))
			for _, desired := range cr.Spec.ForProvider.NetworkAccessConfig.IPs {
				entry := desired
				if entry.CIDRBlock == "" {
					entry.CIDRBlock = entry.IP + "/32"
				}
				if entry.CreatedAt == nil {
					entry.CreatedAt = &now
				}
				provisioned = append(provisioned, entry)
			}
			cr.Status.AtProvider.ProvisionedIPs = provisioned
			cr.Status.AtProvider.IPAccessEntryCount = len(provisioned)
			cr.Status.AtProvider.IPAccessError = ""
			cr.Status.AtProvider.LastIPAccessUpdate = &now
			statusChanged = true

			c.logger.Info("IP access update completed",
				"orgID", orgID, "apiKeyID", apiKeyID, "currentIPCount", len(provisioned))
		}
	}

	if statusChanged {
		if err := c.updateStatusWithRetry(ctx, cr); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, "updating status after update")
		}
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)

	// Cleanup IPs if requested
	if cr.Spec.ForProvider.NetworkAccessConfig.AutoCleanup &&
		len(cr.Status.AtProvider.ProvisionedIPs) > 0 {

		c.logger.Info("Cleaning up provisioned IPs during organization deletion",
			"orgID", orgID, "ipCount", len(cr.Status.AtProvider.ProvisionedIPs))

		secretName := finalSecretName(cr)
		orgCreds, err := c.awsClient.GetSecret(ctx, secretName)
		if err == nil {
			orgService := c.newServiceFn(svc.Credentials{
				PublicKey:  orgCreds.PublicKey,
				PrivateKey: orgCreds.PrivateKey,
			})
			apiKeyID, _ := orgService.FindAPIKeyID(ctx, orgID, orgCreds.PublicKey, "")
			if apiKeyID == "" {
				c.logger.Info("Could not resolve API key ID for cleanup; skipping IP removals", "orgID", orgID)
			} else {
				for _, ipEntry := range cr.Status.AtProvider.ProvisionedIPs {
					if err := orgService.RemoveIPFromAPIKeyAccessList(ctx, orgID, apiKeyID, ipEntry.IP); err != nil {
						c.logger.Info("Failed to remove IP during cleanup (continuing)", "ip", ipEntry.IP, "error", err)
					} else {
						c.logger.Info("Removed IP during cleanup", "ip", ipEntry.IP)
					}
				}
			}
		} else {
			c.logger.Info("Could not fetch credentials for IP cleanup", "error", err)
		}
	}

	// If nothing external was created, just remove our finalizer and return.
	if orgID == "" {
		c.logger.Info("No external orgID present — removing finalizer")
		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return errors.Wrap(err, "re-get before removing finalizer for resource without external name")
		}
		if meta.FinalizerExists(latest, FinalizerOrganizationCleanup) {
			meta.RemoveFinalizer(latest, FinalizerOrganizationCleanup)
			if err := c.kube.Update(ctx, latest); err != nil {
				return errors.Wrap(err, "persisting finalizer removal for resource without external name")
			}
		}
		return nil
	}

	secretName := finalSecretName(cr)
	c.logger.Info("Starting deletion flow", "name", cr.Name, "orgID", orgID, "secretName", secretName)

	now := metav1.Now()
	cr.Status.AtProvider.LastDeletionAttemptTime = &now
	cr.Status.AtProvider.DeletionAttemptCount++
	stateDeleting := v1alpha1.OrganizationStateDeleting
	cr.Status.AtProvider.State = &stateDeleting
	if err := c.updateStatusWithRetry(ctx, cr); err != nil {
		c.logger.Debug("warning: failed to persist deletion attempt status (continuing)", "error", err)
	}

	// Optional pre-verify
	c.logger.Info("Checking if organization still exists", "orgID", orgID)
	verifyErr := c.client.VerifyOrganizationDeletion(ctx, orgID)
	if verifyErr == nil || svc.IsNotFoundError(verifyErr) {
		c.logger.Info("Organization already deleted — performing cleanup")
		return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
	}
	if isAtlasTransientOrOpaqueDeleteErr(verifyErr) {
		c.logger.Info("Atlas verify returned transient/opaque error — treating as deleted", "orgID", orgID, "error", verifyErr)
		return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
	}

	// Use org-specific credentials to delete the org
	c.logger.Info("Fetching org-specific AWS secret", "secretName", secretName)
	orgCreds, err := c.awsClient.GetSecret(ctx, secretName)
	if err != nil {
		c.logger.Info("Unable to fetch org AWS secret", "secretName", secretName, "error", err)
		cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
		cr.SetConditions(xpv1.ReconcileError(err))
		_ = c.updateStatusWithRetry(ctx, cr)
		return errors.Wrap(err, "failed to fetch org AWS secret")
	}

	orgService := c.newServiceFn(svc.Credentials{
		PublicKey:  orgCreds.PublicKey,
		PrivateKey: orgCreds.PrivateKey,
	})

	c.logger.Info("Attempting to delete organization in Atlas", "orgID", orgID)
	err = orgService.DeleteOrganization(ctx, orgID)

	switch {
	case svc.IsNotFoundError(err):
		c.logger.Info("Organization not found — cleaning up AWS secret")
		return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)

	case isUnauthorizedError(err):
		c.logger.Info("Unauthorized (401/403) during delete", "orgID", orgID, "error", err)
		cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
		cr.SetConditions(xpv1.ReconcileError(err))
		_ = c.updateStatusWithRetry(ctx, cr)
		return errors.Wrap(err, "unauthorized delete attempt")

	case err != nil:
		if isAtlasTransientOrOpaqueDeleteErr(err) {
			c.logger.Info("Atlas returned transient/opaque delete error — treating as deleted and performing cleanup", "orgID", orgID, "error", err)
			return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
		}
		c.logger.Info("Organization delete failed, will retry", "orgID", orgID, "error", err)
		cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
		cr.SetConditions(xpv1.ReconcileError(err))
		_ = c.updateStatusWithRetry(ctx, cr)
		return errors.Wrap(err, "delete organization failed")
	}

	// Verify deletion
	c.logger.Info("Verifying deletion", "orgID", orgID)
	verifyErr = orgService.VerifyOrganizationDeletion(ctx, orgID)
	if verifyErr != nil {
		if isAtlasTransientOrOpaqueDeleteErr(verifyErr) {
			c.logger.Info("Atlas verify returned transient/opaque error after delete — treating as deleted", "orgID", orgID, "error", verifyErr)
			return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
		}
		c.logger.Info("Organization still exists after delete attempt", "orgID", orgID, "error", verifyErr)
		cr.Status.AtProvider.LastDeletionAttemptError = verifyErr.Error()
		_ = c.updateStatusWithRetry(ctx, cr)
		return verifyErr
	}

	return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
}

func (c *external) cleanupAWSSecretAfterDelete(
	ctx context.Context,
	cr *v1alpha1.Organization,
	secretName string,
	now metav1.Time,
) error {

	c.logger.Info("Cleanup: deleting AWS secret", "secretName", secretName)

	if delErr := c.awsClient.DeleteSecret(ctx, secretName, true); delErr != nil {
		c.logger.Info("AWS Secret deletion returned error (continuing)", "error", delErr)
		cr.Status.AtProvider.LastDeletionAttemptError = delErr.Error()
		_ = c.updateStatusWithRetry(ctx, cr)
	} else {
		cr.Status.AtProvider.LastDeletionAttemptError = ""
	}

	latest := &v1alpha1.Organization{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
		return errors.Wrap(err, "re-get before removing finalizers")
	}

	changed := false
	if meta.FinalizerExists(latest, FinalizerOrganizationCleanup) {
		meta.RemoveFinalizer(latest, FinalizerOrganizationCleanup)
		changed = true
	}
	if meta.FinalizerExists(latest, crossplaneManagedFinalizer) {
		meta.RemoveFinalizer(latest, crossplaneManagedFinalizer)
		changed = true
	}

	if changed {
		if err := c.kube.Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
					if meta.FinalizerExists(latest, FinalizerOrganizationCleanup) {
						meta.RemoveFinalizer(latest, FinalizerOrganizationCleanup)
					}
					if meta.FinalizerExists(latest, crossplaneManagedFinalizer) {
						meta.RemoveFinalizer(latest, crossplaneManagedFinalizer)
					}
					if err3 := c.kube.Update(ctx, latest); err3 != nil {
						return errors.Wrap(err3, "failed to persist finalizer removal after conflict")
					}
				} else {
					return errors.Wrap(err2, "re-get after conflict while removing finalizers")
				}
			} else {
				return errors.Wrap(err, "failed to persist finalizer removal")
			}
		}
	}

	// Update status to Deleted
	latest.Status = cr.Status
	stateDeleted := v1alpha1.OrganizationStateDeleted
	latest.Status.AtProvider.State = &stateDeleted
	latest.Status.AtProvider.DeletedAt = &now
	latest.Status.AtProvider.LastDeletionAttemptError = cr.Status.AtProvider.LastDeletionAttemptError
	latest.SetConditions(xpv1.ReconcileSuccess())

	if err := c.kube.Status().Update(ctx, latest); err != nil {
		if apierrors.IsConflict(err) {
			if err2 := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err2 == nil {
				latest.Status = cr.Status
				latest.Status.AtProvider.State = &stateDeleted
				latest.Status.AtProvider.DeletedAt = &now
				latest.Status.AtProvider.LastDeletionAttemptError = cr.Status.AtProvider.LastDeletionAttemptError
				latest.SetConditions(xpv1.ReconcileSuccess())
				if err3 := c.kube.Status().Update(ctx, latest); err3 != nil {
					return errors.Wrap(err3, "failed to update status after deletion cleanup (conflict retry)")
				}
			} else {
				return errors.Wrap(err2, "re-get after conflict when updating status after deletion cleanup")
			}
		} else {
			return errors.Wrap(err, "failed to update status after deletion cleanup")
		}
	}

	c.logger.Info("Organization fully deleted — finalizer(s) removed (if present)", "orgID", meta.GetExternalName(latest))
	return nil
}

// helper to compare role slices as multi-sets (accounts for duplicates)
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		// length differs -> sets differ (quick check)
	}
	am := make(map[string]int, len(a))
	for _, s := range a {
		am[s]++
	}
	bm := make(map[string]int, len(b))
	for _, s := range b {
		bm[s]++
	}
	if len(am) != len(bm) {
		return false
	}
	for k, av := range am {
		if bv, ok := bm[k]; !ok || bv != av {
			return false
		}
	}
	return true
}
