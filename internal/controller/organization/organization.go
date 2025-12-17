package organization

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/pkg/errors"

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

	// Custom finalizer used by this controller
	FinalizerOrganizationCleanup = "organization.platform.allianz.io/cleanup"

	// Crossplane managed finalizer that Crossplane normally removes. We'll remove it
	// after we're confident AWS secret cleanup + Atlas deletion are done.
	crossplaneManagedFinalizer = "finalizer.managedresource.crossplane.io"
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

	// Defensive provider config reference check
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
	e := err.Error()
	return strings.Contains(e, "401") || strings.Contains(strings.ToLower(e), "unauthorized") || strings.Contains(strings.ToLower(e), "forbidden")
}

// helper: treat various Atlas/network transient errors as "transient/opaque deletion success"
func isAtlasTransientOrOpaqueDeleteErr(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	// Common patterns observed: "MongoDB Atlas API error 0: -", "Internal Server Error", "500", "context canceled", "network error", "retryable"
	if strings.Contains(e, "mongo") && strings.Contains(e, "api error 0") {
		return true
	}
	if strings.Contains(e, "internal server error") || strings.Contains(e, "500") {
		return true
	}
	if strings.Contains(e, "context canceled") || strings.Contains(e, "context canceled") {
		return true
	}
	if strings.Contains(e, "network error") || strings.Contains(e, "request canceled") || strings.Contains(e, "retryable") {
		return true
	}
	return false
}

// Observe implements managed.ExternalClient Observe.
// Behavior:
//   - If external-name is set -> observe using that org id.
//   - If external-name is empty -> try to find an existing org by name and adopt it (set external-name).
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)

	// If we don't have an external-name, try to find org by name and adopt.
	if orgID == "" {
		c.logger.Debug("External name not set — searching Atlas by organization name", "name", cr.Name)
		found, err := c.client.GetOrganizationByName(ctx, cr.Name)
		if err != nil {
			// If error indicates not found, return not-exists; otherwise surface error
			if svc.IsNotFoundError(err) {
				return managed.ExternalObservation{ResourceExists: false}, nil
			}
			return managed.ExternalObservation{}, errors.Wrap(err, "searching organization by name")
		}
		if found != nil {
			// Adopt the existing org — persist external-name metadata and status
			meta.SetExternalName(cr, found.ID)
			cr.Status.AtProvider.OrgID = found.ID
			cr.Status.AtProvider.OrgName = found.Name
			stateActive := v1alpha1.OrganizationStateActive
			cr.Status.AtProvider.State = &stateActive

			// Persist metadata (external-name) first, then status
			latest := &v1alpha1.Organization{}
			if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
				return managed.ExternalObservation{}, errors.Wrap(err, "fetching latest object to persist external-name")
			}
			meta.SetExternalName(latest, found.ID)
			if err := c.kube.Update(ctx, latest); err != nil {
				if apierrors.IsConflict(err) {
					// try once after re-get
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

			return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
		}

		// Not found by name -> report not exists (Create will run)
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// If deletion in progress on CR, short-circuit to avoid remote calls.
	if cr.DeletionTimestamp != nil {
		c.logger.Debug("Organization marked for deletion", "name", cr.Name, "orgID", orgID)
		cr.SetConditions(xpv1.Deleting())
		// Report exists true so delete flow runs.
		return managed.ExternalObservation{ResourceExists: true}, nil
	}

	// We have an external-name — we can observe AWS secret and mark status.
	secretName := finalSecretName(cr)
	desc, err := c.awsClient.DescribeSecret(ctx, secretName)
	if err == nil && desc.ARN != nil {
		cr.Status.AtProvider.SecretARN = *desc.ARN
		cr.Status.AtProvider.SecretName = secretName
	}

	// Optionally we could call Atlas to fetch more state here if required.

	cr.SetConditions(xpv1.Available())
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// Create implements managed.ExternalClient Create.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr := mg.(*v1alpha1.Organization)

	// If external name got set concurrently (adopted), skip create.
	if meta.GetExternalName(cr) != "" {
		c.logger.Info("External name already set; skipping creation", "orgID", meta.GetExternalName(cr))
		return managed.ExternalCreation{}, nil
	}

	// Check if org already exists by name (defensive)
	existingOrg, err := c.client.GetOrganizationByName(ctx, cr.Name)
	if err != nil && !svc.IsNotFoundError(err) {
		return managed.ExternalCreation{}, errors.Wrap(err, "checking existing organization")
	}

	if existingOrg != nil && existingOrg.OrgOwnerId == cr.Spec.ForProvider.OwnerID {
		c.logger.Info("Organization already exists in Atlas with same owner; reusing existing org",
			"orgID", existingOrg.ID, "orgName", existingOrg.Name)
		meta.SetExternalName(cr, existingOrg.ID)
		cr.Status.AtProvider.OrgID = existingOrg.ID
		cr.Status.AtProvider.OrgName = existingOrg.Name
		stateActive := v1alpha1.OrganizationStateActive
		cr.Status.AtProvider.State = &stateActive

		// persist metadata and status
		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			return managed.ExternalCreation{}, errors.Wrap(err, "re-get before persisting external-name")
		}
		meta.SetExternalName(latest, existingOrg.ID)
		if err := c.kube.Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				// retry once
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

	// Add finalizer for cleanup (and persist)
	if !meta.FinalizerExists(cr, FinalizerOrganizationCleanup) {
		// Persist finalizer safely (get -> modify -> update)
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

	// Persist external-name (metadata) first so subsequent reconciles see it.
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
			// try once more
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

	if err := c.kube.Status().Update(ctx, cr); err != nil {
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

// Update is a no-op
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, nil
}

// Delete implements managed.ExternalClient Delete.
// Delete handles deletion of the external MongoDB Atlas Organization.
func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr := mg.(*v1alpha1.Organization)
	orgID := meta.GetExternalName(cr)

	// No orgID = Nothing created in Atlas → remove our finalizer immediately
	if orgID == "" {
		c.logger.Info("No external orgID present — removing finalizer")
		// remove custom finalizer only; then persist change
		latest := &v1alpha1.Organization{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
			// if not found, just return
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
	c.logger.Info("Starting deletion flow",
		"name", cr.Name,
		"orgID", orgID,
		"secretName", secretName)

	now := metav1.Now()
	cr.Status.AtProvider.LastDeletionAttemptTime = &now
	cr.Status.AtProvider.DeletionAttemptCount++
	stateDeleting := v1alpha1.OrganizationStateDeleting
	cr.Status.AtProvider.State = &stateDeleting

	// Persist status update so attempts are visible in kubernetes; ignore error but log
	if err := c.kube.Status().Update(ctx, cr); err != nil {
		c.logger.Debug("warning: failed to persist deletion attempt status (continuing)", "error", err)
	}

	// Ensure API key has ORG_OWNER
	if !containsRole(cr.Spec.ForProvider.APIKey.Roles, "ORG_OWNER") {
		err := errors.New("API key does not have ORG_OWNER — cannot delete organization")
		c.logger.Info("API key missing ORG_OWNER", "roles", cr.Spec.ForProvider.APIKey.Roles)
		cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
		cr.SetConditions(xpv1.ReconcileError(err))
		_ = c.kube.Status().Update(ctx, cr)
		return err
	}

	// STEP 1 — Verify if already deleted
	c.logger.Info("Checking if organization still exists", "orgID", orgID)
	verifyErr := c.client.VerifyOrganizationDeletion(ctx, orgID)
	if verifyErr == nil || svc.IsNotFoundError(verifyErr) {
		c.logger.Info("Organization already deleted — performing cleanup")
		return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
	}
	// If verifyErr is transient/opaque we should treat as deleted
	if isAtlasTransientOrOpaqueDeleteErr(verifyErr) {
		c.logger.Info("Atlas verify returned transient/opaque error — treating as deleted", "orgID", orgID, "error", verifyErr)
		return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
	}

	// STEP 2 — Fetch org-level AWS secret (ORG owner API key)
	c.logger.Info("Fetching org-specific AWS secret", "secretName", secretName)
	orgCreds, err := c.awsClient.GetSecret(ctx, secretName)
	if err != nil {
		c.logger.Info("Unable to fetch org AWS secret", "secretName", secretName, "error", err)
		cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
		cr.SetConditions(xpv1.ReconcileError(err))
		_ = c.kube.Status().Update(ctx, cr)
		return errors.Wrap(err, "failed to fetch org AWS secret")
	}

	// Create org-scoped service
	orgService := c.newServiceFn(svc.Credentials{
		PublicKey:  orgCreds.PublicKey,
		PrivateKey: orgCreds.PrivateKey,
	})

	// STEP 3 — Attempt deletion
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
		_ = c.kube.Status().Update(ctx, cr)
		return errors.Wrap(err, "unauthorized delete attempt")

	case err != nil:
		// If Atlas returned opaque/transient error treat it as success and continue cleanup.
		if isAtlasTransientOrOpaqueDeleteErr(err) {
			c.logger.Info("Atlas returned transient/opaque delete error — treating as deleted and performing cleanup", "orgID", orgID, "error", err)
			return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
		}
		c.logger.Info("Organization delete failed, will retry", "orgID", orgID, "error", err)
		cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
		cr.SetConditions(xpv1.ReconcileError(err))
		_ = c.kube.Status().Update(ctx, cr)
		return errors.Wrap(err, "delete organization failed")
	}

	// STEP 4 — Confirm deletion success
	c.logger.Info("Verifying deletion", "orgID", orgID)
	verifyErr = orgService.VerifyOrganizationDeletion(ctx, orgID)
	if verifyErr != nil {
		// If verify returns transient/opaque error, treat as deleted (Atlas backend sometimes returns 0/500 while finalizing)
		if isAtlasTransientOrOpaqueDeleteErr(verifyErr) {
			c.logger.Info("Atlas verify returned transient/opaque error after delete — treating as deleted", "orgID", orgID, "error", verifyErr)
			return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
		}
		c.logger.Info("Organization still exists after delete attempt", "orgID", orgID, "error", verifyErr)
		cr.Status.AtProvider.LastDeletionAttemptError = verifyErr.Error()
		_ = c.kube.Status().Update(ctx, cr)
		return verifyErr
	}

	// STEP 5 — Cleanup AWS secret & remove finalizer(s)
	return c.cleanupAWSSecretAfterDelete(ctx, cr, secretName, now)
}

// cleanupAWSSecretAfterDelete removes the AWS secret & finalizer(s).
// It will remove both our custom finalizer and Crossplane's managed finalizer
// when it has confirmed that AWS secret deletion and Atlas deletion are both done.
func (c *external) cleanupAWSSecretAfterDelete(
	ctx context.Context,
	cr *v1alpha1.Organization,
	secretName string,
	now metav1.Time,
) error {

	c.logger.Info("Cleanup: deleting AWS secret", "secretName", secretName)

	// Delete secret (ignore not-found). Record any error in status but continue.
	if delErr := c.awsClient.DeleteSecret(ctx, secretName, true); delErr != nil {
		c.logger.Info("AWS Secret deletion returned error (continuing)", "error", delErr)
		cr.Status.AtProvider.LastDeletionAttemptError = delErr.Error()
		_ = c.kube.Status().Update(ctx, cr)
	} else {
		// clear last deletion attempt error if delete succeeded
		cr.Status.AtProvider.LastDeletionAttemptError = ""
	}

	// Remove our custom finalizer and Crossplane's managed finalizer so CR can be removed.
	latest := &v1alpha1.Organization{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err != nil {
		// If the object can't be fetched, return the error so the reconciler will requeue.
		return errors.Wrap(err, "re-get before removing finalizers")
	}

	changed := false
	if meta.FinalizerExists(latest, FinalizerOrganizationCleanup) {
		meta.RemoveFinalizer(latest, FinalizerOrganizationCleanup)
		changed = true
	}
	// Remove crossplane managed finalizer only when we're confident cleanup is done.
	if meta.FinalizerExists(latest, crossplaneManagedFinalizer) {
		meta.RemoveFinalizer(latest, crossplaneManagedFinalizer)
		changed = true
	}

	if changed {
		// persist metadata change
		if err := c.kube.Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				// retry once after re-get
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

	// update status to mark deleted state
	latest.Status = cr.Status
	stateDeleted := v1alpha1.OrganizationStateDeleted
	latest.Status.AtProvider.State = &stateDeleted
	latest.Status.AtProvider.DeletedAt = &now
	latest.Status.AtProvider.LastDeletionAttemptError = cr.Status.AtProvider.LastDeletionAttemptError
	latest.SetConditions(xpv1.ReconcileSuccess())

	if err := c.kube.Status().Update(ctx, latest); err != nil {
		if apierrors.IsConflict(err) {
			// Try one more time after re-get
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
