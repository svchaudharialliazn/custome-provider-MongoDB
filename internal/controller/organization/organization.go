// internal/controller/organization/organization.go
package organization

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"

    "github.com/pkg/errors"
    corev1 "k8s.io/api/core/v1"
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"

    xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
    "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
    "github.com/crossplane/crossplane-runtime/v2/pkg/event"
    "github.com/crossplane/crossplane-runtime/v2/pkg/logging"
    "github.com/crossplane/crossplane-runtime/v2/pkg/meta"
    "github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
    "github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
    "github.com/crossplane/crossplane-runtime/v2/pkg/resource"

    "github.com/svchaudharialliazn/swapnil-provider-mongodb/apis/organization/v1alpha1"
    apisv1alpha1 "github.com/svchaudharialliazn/swapnil-provider-mongodb/apis/v1alpha1"
    awsclient "github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/clients/aws"
    svc "github.com/svchaudharialliazn/swapnil-provider-mongodb/internal/clients/mongodb"
)

const (
    errNotOrganization = "managed resource is not an Organization custom resource"
    errTrackPCUsage    = "cannot track ProviderConfig usage"
    errGetPC           = "cannot get ProviderConfig"
    errInvalidPCConfig = "ProviderConfig credentials must be configured properly"
    errAWSClient       = "cannot create AWS client"
    secretPrefix       = "product/mongodb/"

    defaultK8sSecretNamespace = "crossplane-system"
)

// Setup wires up the controller to the manager.
func Setup(mgr ctrl.Manager, o controller.Options) error {
    name := managed.ControllerName(v1alpha1.OrganizationGroupKind)

    r := managed.NewReconciler(
        mgr,
        resource.ManagedKind(v1alpha1.OrganizationGroupVersionKind),
        managed.WithExternalConnector(&connector{
            kube:           mgr.GetClient(),
            usage:          resource.NewLegacyProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
            logger:         o.Logger,
            newServiceFn:   svc.NewService,
            newAWSClientFn: awsclient.NewClient,
        }),
        managed.WithInitializers(),
        managed.WithLogger(o.Logger.WithValues("controller", name)),
        managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
    )

    return ctrl.NewControllerManagedBy(mgr).
        Named(name).
        WithOptions(o.ForControllerRuntime()).
        For(&v1alpha1.Organization{}).
        Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
    kube           client.Client
    usage          resource.LegacyTracker
    logger         logging.Logger
    newServiceFn   func(creds svc.Credentials) svc.Service
    newAWSClientFn func(ctx context.Context, region string) (*awsclient.Client, error)
}

// Connect establishes connection based on ProviderConfig credentials source
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
    cr, ok := mg.(*v1alpha1.Organization)
    if !ok {
        return nil, errors.New(errNotOrganization)
    }

    if err := c.usage.Track(ctx, cr); err != nil {
        return nil, errors.Wrap(err, errTrackPCUsage)
    }

    if cr.GetProviderConfigReference() == nil {
        return nil, errors.New(errGetPC)
    }

    pc := &apisv1alpha1.ProviderConfig{}
    if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
        return nil, errors.Wrap(err, errGetPC)
    }

    var creds svc.Credentials
    var awsClient *awsclient.Client
    var awsCfg *apisv1alpha1.AWSSecretsManagerReference

    // Handle credentials based on source type
    switch pc.Spec.Credentials.Source {
    case xpv1.CredentialsSourceSecret:
        // Kubernetes Secret source
        if pc.Spec.Credentials.SecretRef == nil {
            return nil, errors.New("ProviderConfig SecretRef cannot be nil when source is Secret")
        }

        secretRef := pc.Spec.Credentials.SecretRef
        secret := &corev1.Secret{}
        secretNN := types.NamespacedName{
            Name:      secretRef.Name,
            Namespace: secretRef.Namespace,
        }

        if err := c.kube.Get(ctx, secretNN, secret); err != nil {
            return nil, errors.Wrap(err, "cannot get ProviderConfig Kubernetes Secret")
        }

        credData, ok := secret.Data[secretRef.Key]
        if !ok {
            return nil, errors.Errorf("key %s not found in secret %s/%s", secretRef.Key, secretRef.Namespace, secretRef.Name)
        }

        if err := json.Unmarshal(credData, &creds); err != nil {
            return nil, errors.Wrap(err, "cannot unmarshal credentials from Kubernetes Secret")
        }

        c.logger.Debug("Using credentials from Kubernetes Secret", "secretName", secretRef.Name, "namespace", secretRef.Namespace)

    case apisv1alpha1.CredentialsSourceAWS:
        // AWS Secrets Manager source
        awsCfg = pc.Spec.Credentials.AWS.SecretsManager
        if awsCfg == nil {
            return nil, errors.New("ProviderConfig AWS secrets manager config missing")
        }
        if awsCfg.SecretName == nil || *awsCfg.SecretName == "" {
            return nil, errors.New("ProviderConfig AWS secret name cannot be empty")
        }

        var err error
        awsClient, err = c.newAWSClientFn(ctx, awsCfg.Region)
        if err != nil {
            return nil, errors.Wrap(err, errAWSClient)
        }

        credsAWS, err := awsClient.GetSecret(ctx, *awsCfg.SecretName)
        if err != nil {
            return nil, errors.Wrap(err, "cannot get AWS secret from ProviderConfig")
        }

        creds = svc.Credentials{
            PublicKey:  credsAWS.PublicKey,
            PrivateKey: credsAWS.PrivateKey,
        }

        c.logger.Debug("Using credentials from AWS Secrets Manager", "secretName", *awsCfg.SecretName, "region", awsCfg.Region)

    default:
        return nil, errors.Errorf("unsupported credentials source: %s", pc.Spec.Credentials.Source)
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

// ===============================================================
// Kubernetes Secret Management Functions
// ===============================================================

// k8sSecretNamespace returns the namespace for Kubernetes secrets
func (c *external) k8sSecretNamespace(cr *v1alpha1.Organization) string {
    if cr.Spec.ForProvider.SecretStorageConfig != nil &&
        cr.Spec.ForProvider.SecretStorageConfig.Kubernetes != nil &&
        cr.Spec.ForProvider.SecretStorageConfig.Kubernetes.Namespace != nil {
        return *cr.Spec.ForProvider.SecretStorageConfig.Kubernetes.Namespace
    }
    return defaultK8sSecretNamespace
}

// k8sSecretName returns the name for Kubernetes secrets
func (c *external) k8sSecretName(cr *v1alpha1.Organization) string {
    if cr.Spec.ForProvider.SecretStorageConfig != nil &&
        cr.Spec.ForProvider.SecretStorageConfig.Kubernetes != nil &&
        cr.Spec.ForProvider.SecretStorageConfig.Kubernetes.Name != nil {
        return *cr.Spec.ForProvider.SecretStorageConfig.Kubernetes.Name
    }
    return fmt.Sprintf("%s-mongodb-credentials", cr.Name)
}

// createOrUpdateK8sSecret creates or updates a Kubernetes Secret with organization API keys
func (c *external) createOrUpdateK8sSecret(ctx context.Context, cr *v1alpha1.Organization, apiKey svc.APIKeyPair) error {
    secretName := c.k8sSecretName(cr)
    secretNamespace := c.k8sSecretNamespace(cr)

    credData, err := json.Marshal(svc.Credentials{
        PublicKey:  apiKey.PublicKey,
        PrivateKey: apiKey.PrivateKey,
    })
    if err != nil {
        return errors.Wrap(err, "failed to marshal credentials")
    }

    // Get orgID - might be empty during creation
    orgID := meta.GetExternalName(cr)
    if orgID == "" {
        orgID = cr.Status.AtProvider.OrgID
    }

    secret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      secretName,
            Namespace: secretNamespace,
            Labels: map[string]string{
                "app.kubernetes.io/managed-by": "crossplane",
                "crossplane.io/claim-name":     cr.Name,
            },
        },
        Type: corev1.SecretTypeOpaque,
        Data: map[string][]byte{
            "credentials": credData,
        },
    }

    // Only add org-id label if we have it
    if orgID != "" {
        secret.Labels["mongodb.crossplane.io/org-id"] = orgID
    }

    existing := &corev1.Secret{}
    err = c.kube.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNamespace}, existing)
    if err != nil {
        if apierrors.IsNotFound(err) {
            c.logger.Info("Creating Kubernetes secret", "name", secretName, "namespace", secretNamespace)
            if err := c.kube.Create(ctx, secret); err != nil {
                return errors.Wrap(err, "failed to create Kubernetes secret")
            }
            c.logger.Info("Successfully created Kubernetes secret", "name", secretName, "namespace", secretNamespace)
            return nil
        }
        return errors.Wrap(err, "failed to check if Kubernetes secret exists")
    }

    c.logger.Info("Updating existing Kubernetes secret", "name", secretName, "namespace", secretNamespace)
    existing.Data = secret.Data
    existing.Labels = secret.Labels
    if err := c.kube.Update(ctx, existing); err != nil {
        return errors.Wrap(err, "failed to update Kubernetes secret")
    }
    c.logger.Info("Successfully updated Kubernetes secret", "name", secretName, "namespace", secretNamespace)
    return nil
}

// getK8sSecret retrieves credentials from a Kubernetes Secret
func (c *external) getK8sSecret(ctx context.Context, cr *v1alpha1.Organization) (*svc.Credentials, error) {
    secretName := c.k8sSecretName(cr)
    secretNamespace := c.k8sSecretNamespace(cr)

    secret := &corev1.Secret{}
    if err := c.kube.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNamespace}, secret); err != nil {
        return nil, errors.Wrap(err, "failed to get Kubernetes secret")
    }

    credData, ok := secret.Data["credentials"]
    if !ok {
        return nil, errors.New("credentials key not found in Kubernetes secret")
    }

    var creds svc.Credentials
    if err := json.Unmarshal(credData, &creds); err != nil {
        return nil, errors.Wrap(err, "failed to unmarshal credentials from Kubernetes secret")
    }

    return &creds, nil
}

// deleteK8sSecret deletes the Kubernetes Secret
func (c *external) deleteK8sSecret(ctx context.Context, cr *v1alpha1.Organization) error {
    secretName := c.k8sSecretName(cr)
    secretNamespace := c.k8sSecretNamespace(cr)

    secret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      secretName,
            Namespace: secretNamespace,
        },
    }

    c.logger.Info("Deleting Kubernetes secret", "name", secretName, "namespace", secretNamespace)
    if err := c.kube.Delete(ctx, secret); err != nil {
        if apierrors.IsNotFound(err) {
            c.logger.Debug("Kubernetes secret already deleted", "name", secretName, "namespace", secretNamespace)
            return nil
        }
        return errors.Wrap(err, "failed to delete Kubernetes secret")
    }

    c.logger.Info("Successfully deleted Kubernetes secret", "name", secretName, "namespace", secretNamespace)
    return nil
}

// ===============================================================
// Helper Functions
// ===============================================================

func finalSecretName(cr *v1alpha1.Organization) string {
    if cr.Spec.ForProvider.AWSSecretsConfig != nil &&
        cr.Spec.ForProvider.AWSSecretsConfig.SecretName != nil &&
        *cr.Spec.ForProvider.AWSSecretsConfig.SecretName != "" {
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

func equalStringSets(a, b []string) bool {
    if len(a) != len(b) {
        return false
    }
    ma := make(map[string]bool, len(a))
    for _, s := range a {
        ma[s] = true
    }
    for _, s := range b {
        if !ma[s] {
            return false
        }
    }
    return true
}

func isUnauthorizedError(err error) bool {
    if err == nil {
        return false
    }
    e := strings.ToLower(err.Error())
    return strings.Contains(e, "401") || strings.Contains(e, "unauthorized") || strings.Contains(e, "forbidden") || strings.Contains(e, "403")
}

func isNotFoundError(err error) bool {
    if err == nil {
        return false
    }
    e := strings.ToLower(err.Error())
    return strings.Contains(e, "404") || strings.Contains(e, "not found")
}

// isAtlasTransientError checks for transient errors that should trigger a retry
func isAtlasTransientError(err error) bool {
    if err == nil {
        return false
    }
    e := strings.ToLower(err.Error())
    if strings.Contains(e, "internal server error") || strings.Contains(e, "500") {
        return true
    }
    if strings.Contains(e, "context canceled") || strings.Contains(e, "context cancelled") {
        return true
    }
    if strings.Contains(e, "network error") || strings.Contains(e, "request canceled") || strings.Contains(e, "retryable") {
        return true
    }
    if strings.Contains(e, "api error 0") {
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

// fetchOrgCredentials fetches organization credentials based on storage type
func (c *external) fetchOrgCredentials(ctx context.Context, cr *v1alpha1.Organization) (*svc.Credentials, error) {
    currentStorageType := cr.Status.AtProvider.SecretStorageType

    if currentStorageType == string(v1alpha1.SecretStorageTypeKubernetes) {
        c.logger.Debug("Fetching credentials from Kubernetes Secret")
        return c.getK8sSecret(ctx, cr)
    }

    // Default to AWS
    if c.awsClient == nil {
        return nil, errors.New("AWS client not available")
    }

    secretName := cr.Status.AtProvider.SecretName
    if secretName == "" {
        secretName = finalSecretName(cr)
    }

    c.logger.Debug("Fetching credentials from AWS Secrets Manager", "secretName", secretName)
    awsCreds, err := c.awsClient.GetSecret(ctx, secretName)
    if err != nil {
        return nil, err
    }

    return &svc.Credentials{
        PublicKey:  awsCreds.PublicKey,
        PrivateKey: awsCreds.PrivateKey,
    }, nil
}

// enableAPIAccessListRequired enables the "Require IP Access List for the Atlas Administration API"
func (c *external) enableAPIAccessListRequired(
    ctx context.Context,
    orgID string,
    apiKey svc.APIKeyPair,
) error {
    c.logger.Info("Enabling apiAccessListRequired for organization", "orgID", orgID)

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

    apiKeyID, err := orgService.FindAPIKeyID(ctx, orgID, apiKey.PublicKey, "")
    if err != nil || apiKeyID == "" {
        c.logger.Info("Could not resolve organization API key ID for IP access provisioning; skipping",
            "orgID", orgID, "error", err)
        return nil, nil
    }

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

// ===============================================================
// Observe
// ===============================================================

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

        cr.SetConditions(xpv1.Available())
        return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
    }

    // No external name: try to adopt by name.
    if orgID == "" {
        c.logger.Debug("External name not set — searching Atlas by organization name", "name", cr.Name)

        found, err := c.client.GetOrganizationByName(ctx, cr.Name)
        if err != nil {
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

            return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
        }

        return managed.ExternalObservation{ResourceExists: false}, nil
    }

    // Handle deletion short-circuit.
    if cr.DeletionTimestamp != nil {
        c.logger.Debug("Organization marked for deletion", "name", cr.Name, "orgID", orgID)
        cr.SetConditions(xpv1.Deleting())
        return managed.ExternalObservation{ResourceExists: true}, nil
    }

    // Update secret metadata based on storage type
    if cr.Spec.ForProvider.SecretStorageConfig != nil {
        storageType := string(cr.Spec.ForProvider.SecretStorageConfig.Type)
        cr.Status.AtProvider.SecretStorageType = storageType

        switch cr.Spec.ForProvider.SecretStorageConfig.Type {
        case v1alpha1.SecretStorageTypeKubernetes:
            secretName := c.k8sSecretName(cr)
            secretNamespace := c.k8sSecretNamespace(cr)
            cr.Status.AtProvider.SecretName = secretName
            cr.Status.AtProvider.SecretNamespace = secretNamespace
            cr.Status.AtProvider.SecretARN = ""

        case v1alpha1.SecretStorageTypeAWS:
            if c.awsClient != nil {
                secretName := finalSecretName(cr)
                desc, err := c.awsClient.DescribeSecret(ctx, secretName)
                if err == nil && desc.ARN != nil {
                    cr.Status.AtProvider.SecretARN = *desc.ARN
                    cr.Status.AtProvider.SecretName = secretName
                    cr.Status.AtProvider.SecretNamespace = ""
                }
            }
        }
    } else {
        cr.Status.AtProvider.SecretStorageType = string(v1alpha1.SecretStorageTypeAWS)
        if c.awsClient != nil {
            secretName := finalSecretName(cr)
            desc, err := c.awsClient.DescribeSecret(ctx, secretName)
            if err == nil && desc.ARN != nil {
                cr.Status.AtProvider.SecretARN = *desc.ARN
                cr.Status.AtProvider.SecretName = secretName
            }
        }
    }

    upToDate := true

    // Compare desired vs current secret name/storage to trigger Update for migration.
    desiredSecretName := ""
    if cr.Spec.ForProvider.SecretStorageConfig != nil && cr.Spec.ForProvider.SecretStorageConfig.Type == v1alpha1.SecretStorageTypeKubernetes {
        desiredSecretName = c.k8sSecretName(cr)
    } else {
        desiredSecretName = finalSecretName(cr)
    }

    if cr.Status.AtProvider.SecretName != "" && cr.Status.AtProvider.SecretName != desiredSecretName {
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

// ===============================================================
// Create
// ===============================================================

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
    cr := mg.(*v1alpha1.Organization)

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

    meta.SetExternalName(cr, org.ID)
    cr.Status.AtProvider.OrgID = org.ID
    cr.Status.AtProvider.OrgName = org.Name
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

    // Store org API key based on storage configuration
    var secretARN string
    if cr.Spec.ForProvider.SecretStorageConfig != nil {
        switch cr.Spec.ForProvider.SecretStorageConfig.Type {
        case v1alpha1.SecretStorageTypeKubernetes:
            c.logger.Info("Storing organization API key in Kubernetes Secret", "orgID", org.ID)
            if err := c.createOrUpdateK8sSecret(ctx, cr, apiKey); err != nil {
                return managed.ExternalCreation{}, errors.Wrap(err, "failed to store API key in Kubernetes Secret")
            }

            cr.Status.AtProvider.SecretName = c.k8sSecretName(cr)
            cr.Status.AtProvider.SecretNamespace = c.k8sSecretNamespace(cr)
            cr.Status.AtProvider.SecretStorageType = string(v1alpha1.SecretStorageTypeKubernetes)
            cr.Status.AtProvider.CreatedWithCredentialSource = "Kubernetes"

        case v1alpha1.SecretStorageTypeAWS:
            if c.awsClient == nil {
                return managed.ExternalCreation{}, errors.New("AWS client not available for AWS secret storage")
            }

            awsConfig := cr.Spec.ForProvider.SecretStorageConfig.AWS
            if awsConfig == nil {
                return managed.ExternalCreation{}, errors.New("AWS configuration is required when secretStorageConfig.type is AWS")
            }

            secretName := secretPrefix + org.Name
            if awsConfig.SecretName != nil && *awsConfig.SecretName != "" {
                secretName = secretPrefix + *awsConfig.SecretName
            }

            c.logger.Info("Storing organization API key in AWS Secrets Manager", "orgID", org.ID, "secretName", secretName)
            arn, err := c.awsClient.PutSecret(ctx, secretName, awsclient.MongoDBAPICredentials{
                PublicKey:  apiKey.PublicKey,
                PrivateKey: apiKey.PrivateKey,
            }, org.ID, awsConfig.KMSKeyID)
            if err != nil {
                return managed.ExternalCreation{}, errors.Wrap(err, "failed to store API key in AWS Secrets Manager")
            }

            secretARN = arn
            cr.Status.AtProvider.SecretARN = arn
            cr.Status.AtProvider.SecretName = secretName
            cr.Status.AtProvider.SecretStorageType = string(v1alpha1.SecretStorageTypeAWS)
            cr.Status.AtProvider.CreatedWithCredentialSource = "AWS"
        }
    } else {
        // Backward compatibility: use AWSSecretsConfig (deprecated)
        if c.awsClient == nil {
            return managed.ExternalCreation{}, errors.New("AWS client not available and no secretStorageConfig specified")
        }

        secretName := finalSecretName(cr)
        c.logger.Info("Storing organization API key in AWS Secrets Manager (backward compatibility)", "orgID", org.ID, "secretName", secretName)
        arn, err := c.awsClient.PutSecret(ctx, secretName, awsclient.MongoDBAPICredentials{
            PublicKey:  apiKey.PublicKey,
            PrivateKey: apiKey.PrivateKey,
        }, org.ID, cr.Spec.ForProvider.AWSSecretsConfig.KMSKeyID)
        if err != nil {
            return managed.ExternalCreation{}, errors.Wrap(err, "failed to store API key in AWS Secrets Manager")
        }

        secretARN = arn
        cr.Status.AtProvider.SecretARN = arn
        cr.Status.AtProvider.SecretName = secretName
        cr.Status.AtProvider.SecretStorageType = string(v1alpha1.SecretStorageTypeAWS)
        cr.Status.AtProvider.CreatedWithCredentialSource = "AWS"
    }

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

    connDetails := managed.ConnectionDetails{
        "publicKey":  []byte(apiKey.PublicKey),
        "privateKey": []byte(apiKey.PrivateKey),
    }
    if secretARN != "" {
        connDetails["secretARN"] = []byte(secretARN)
    }

    return managed.ExternalCreation{
        ConnectionDetails: connDetails,
    }, nil
}

// ===============================================================
// Update
// ===============================================================

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
    cr := mg.(*v1alpha1.Organization)
    orgID := meta.GetExternalName(cr)
    if orgID == "" {
        return managed.ExternalUpdate{}, nil
    }

    now := metav1.Now()
    statusChanged := false

    // Fetch org credentials based on current storage type
    orgCreds, err := c.fetchOrgCredentials(ctx, cr)
    if err != nil {
        c.logger.Info("Failed to read credentials for update", "error", err)
        return managed.ExternalUpdate{}, errors.Wrap(err, "failed to fetch org credentials")
    }

    orgService := c.newServiceFn(*orgCreds)

    // Handle storage migration if secretStorageConfig changed
    currentStorageType := cr.Status.AtProvider.SecretStorageType
    desiredStorageType := string(v1alpha1.SecretStorageTypeAWS) // default
    if cr.Spec.ForProvider.SecretStorageConfig != nil {
        desiredStorageType = string(cr.Spec.ForProvider.SecretStorageConfig.Type)
    }

    if currentStorageType != "" && desiredStorageType != currentStorageType {
        c.logger.Info("Storage type migration detected", "from", currentStorageType, "to", desiredStorageType)

        apiKeyPair := svc.APIKeyPair{
            PublicKey:  orgCreds.PublicKey,
            PrivateKey: orgCreds.PrivateKey,
        }

        switch desiredStorageType {
        case string(v1alpha1.SecretStorageTypeKubernetes):
            c.logger.Info("Migrating secret to Kubernetes", "orgID", orgID)
            if err := c.createOrUpdateK8sSecret(ctx, cr, apiKeyPair); err != nil {
                return managed.ExternalUpdate{}, errors.Wrap(err, "failed to migrate secret to Kubernetes")
            }

            cr.Status.AtProvider.SecretName = c.k8sSecretName(cr)
            cr.Status.AtProvider.SecretNamespace = c.k8sSecretNamespace(cr)
            cr.Status.AtProvider.SecretStorageType = string(v1alpha1.SecretStorageTypeKubernetes)
            cr.Status.AtProvider.SecretARN = ""
            statusChanged = true

            if c.awsClient != nil && currentStorageType == string(v1alpha1.SecretStorageTypeAWS) {
                oldSecretName := cr.Status.AtProvider.SecretName
                if oldSecretName != "" {
                    if delErr := c.awsClient.DeleteSecret(ctx, oldSecretName, true); delErr != nil {
                        c.logger.Info("Failed to delete old AWS secret after migration (continuing)", "secretName", oldSecretName, "error", delErr)
                    }
                }
            }

        case string(v1alpha1.SecretStorageTypeAWS):
            if c.awsClient == nil {
                return managed.ExternalUpdate{}, errors.New("AWS client not available for migration")
            }

            awsConfig := cr.Spec.ForProvider.SecretStorageConfig.AWS
            if awsConfig == nil {
                return managed.ExternalUpdate{}, errors.New("AWS configuration required for AWS storage migration")
            }

            newSecretName := secretPrefix + orgID
            if awsConfig.SecretName != nil && *awsConfig.SecretName != "" {
                newSecretName = secretPrefix + *awsConfig.SecretName
            }

            c.logger.Info("Migrating secret to AWS", "orgID", orgID, "secretName", newSecretName)
            arn, err := c.awsClient.PutSecret(ctx, newSecretName, awsclient.MongoDBAPICredentials{
                PublicKey:  orgCreds.PublicKey,
                PrivateKey: orgCreds.PrivateKey,
            }, orgID, awsConfig.KMSKeyID)
            if err != nil {
                return managed.ExternalUpdate{}, errors.Wrap(err, "failed to migrate secret to AWS")
            }

            cr.Status.AtProvider.SecretARN = arn
            cr.Status.AtProvider.SecretName = newSecretName
            cr.Status.AtProvider.SecretStorageType = string(v1alpha1.SecretStorageTypeAWS)
            cr.Status.AtProvider.SecretNamespace = ""
            statusChanged = true

            if currentStorageType == string(v1alpha1.SecretStorageTypeKubernetes) {
                if delErr := c.deleteK8sSecret(ctx, cr); delErr != nil {
                    c.logger.Info("Failed to delete old Kubernetes secret after migration (continuing)", "error", delErr)
                }
            }
        }
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
        needsEnforcementUpdate = desiredEnforcement
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
            currentIPs := make(map[string]bool)
            for _, ip := range cr.Status.AtProvider.ProvisionedIPs {
                currentIPs[ip.IP] = true
            }
            desiredIPMap := make(map[string]v1alpha1.IPAccessEntry)
            for _, ip := range cr.Spec.ForProvider.NetworkAccessConfig.IPs {
                desiredIPMap[ip.IP] = ip
            }

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

            for currentIP := range currentIPs {
                if _, ok := desiredIPMap[currentIP]; !ok {
                    c.logger.Info("Removing IP from org API key access list",
                        "orgID", orgID, "apiKeyID", apiKeyID, "ip", currentIP)
                    if err := orgService.RemoveIPFromAPIKeyAccessList(ctx, orgID, apiKeyID, currentIP); err != nil {
                        c.logger.Info("Failed to remove IP (continuing)", "ip", currentIP, "error", err)
                    }
                }
            }

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

// ===============================================================
// Delete - Uses org-specific credentials for all operations
// After confirmed deletion, removes ALL finalizers including Crossplane's
// ===============================================================

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
    return managed.ExternalDelete{}, c.delete(ctx, mg)
}

func (c *external) Disconnect(_ context.Context) error {
    return nil
}

func (c *external) delete(ctx context.Context, mg resource.Managed) error {
    cr := mg.(*v1alpha1.Organization)
    orgID := meta.GetExternalName(cr)

    // Cleanup IPs if requested
    if cr.Spec.ForProvider.NetworkAccessConfig.AutoCleanup &&
        len(cr.Status.AtProvider.ProvisionedIPs) > 0 {

        c.logger.Info("Cleaning up provisioned IPs during organization deletion",
            "orgID", orgID, "ipCount", len(cr.Status.AtProvider.ProvisionedIPs))

        orgCreds, err := c.fetchOrgCredentials(ctx, cr)
        if err == nil && orgCreds != nil {
            orgService := c.newServiceFn(*orgCreds)
            apiKeyID, _ := orgService.FindAPIKeyID(ctx, orgID, orgCreds.PublicKey, "")
            if apiKeyID == "" {
                c.logger.Info("Could not resolve API key ID for cleanup; skipping IP removals", "orgID", orgID)
            } else {
                for _, ipEntry := range cr.Status.AtProvider.ProvisionedIPs {
                    if rerr := orgService.RemoveIPFromAPIKeyAccessList(ctx, orgID, apiKeyID, ipEntry.IP); rerr != nil {
                        c.logger.Info("Failed to remove IP during cleanup (continuing)", "ip", ipEntry.IP, "error", rerr)
                    } else {
                        c.logger.Info("Removed IP during cleanup", "ip", ipEntry.IP)
                    }
                }
            }
        } else {
            c.logger.Info("Could not fetch credentials for IP cleanup", "error", err)
        }
    }

    // If nothing external was created, just delete the secret and return.
    if orgID == "" {
        c.logger.Info("No external orgID present — cleaning up secret")
        return c.cleanupSecretsAfterDelete(ctx, cr, metav1.Now())
    }

    c.logger.Info("Starting deletion flow", "name", cr.Name, "orgID", orgID)

    now := metav1.Now()
    cr.Status.AtProvider.LastDeletionAttemptTime = &now
    cr.Status.AtProvider.DeletionAttemptCount++
    stateDeleting := v1alpha1.OrganizationStateDeleting
    cr.Status.AtProvider.State = &stateDeleting
    if err := c.updateStatusWithRetry(ctx, cr); err != nil {
        c.logger.Debug("warning: failed to persist deletion attempt status (continuing)", "error", err)
    }

    // Optional pre-verify using parent credentials
    c.logger.Info("Checking if organization still exists", "orgID", orgID)
    verifyErr := c.client.VerifyOrganizationDeletion(ctx, orgID)
    if verifyErr == nil || svc.IsNotFoundError(verifyErr) {
        c.logger.Info("Organization already deleted — performing cleanup")
        return c.cleanupSecretsAfterDelete(ctx, cr, now)
    }
    if isAtlasTransientOrOpaqueDeleteErr(verifyErr) {
        c.logger.Info("Atlas verify returned transient/opaque error — treating as deleted", "orgID", orgID, "error", verifyErr)
        return c.cleanupSecretsAfterDelete(ctx, cr, now)
    }

    // Fetch org-specific credentials
    orgCreds, err := c.fetchOrgCredentials(ctx, cr)
    if err != nil {
        // Secret is gone — we can't delete via the org key anymore.
        // Treat as already cleaned up; the Atlas org must be removed manually.
        if strings.Contains(err.Error(), "ResourceNotFoundException") ||
            strings.Contains(err.Error(), "can't find the specified secret") {
            c.logger.Info("Org credential secret missing — skipping Atlas delete and finalizing cleanup", "error", err)
            return c.cleanupSecretsAfterDelete(ctx, cr, now)
        }
        c.logger.Info("Unable to fetch org credentials for deletion", "error", err)
        cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
        cr.SetConditions(xpv1.ReconcileError(err))
        _ = c.updateStatusWithRetry(ctx, cr)
        return errors.Wrap(err, "failed to fetch org credentials for deletion")
    }

    orgService := c.newServiceFn(*orgCreds)

    c.logger.Info("Attempting to delete organization in Atlas", "orgID", orgID)
    err = orgService.DeleteOrganization(ctx, orgID)

    switch {
    case svc.IsNotFoundError(err):
        c.logger.Info("Organization not found — cleaning up secrets")
        return c.cleanupSecretsAfterDelete(ctx, cr, now)

    case isUnauthorizedError(err):
        c.logger.Info("Unauthorized (401/403) during delete", "orgID", orgID, "error", err)
        cr.Status.AtProvider.LastDeletionAttemptError = err.Error()
        cr.SetConditions(xpv1.ReconcileError(err))
        _ = c.updateStatusWithRetry(ctx, cr)
        return errors.Wrap(err, "unauthorized delete attempt")

    case err != nil:
        if isAtlasTransientOrOpaqueDeleteErr(err) {
            c.logger.Info("Atlas returned transient/opaque delete error — treating as deleted and performing cleanup", "orgID", orgID, "error", err)
            return c.cleanupSecretsAfterDelete(ctx, cr, now)
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
            c.logger.Info("Atlas verify returned transient/opaque error after delete request — treating as deleted", "orgID", orgID, "error", verifyErr)
            return c.cleanupSecretsAfterDelete(ctx, cr, now)
        }
        if !svc.IsNotFoundError(verifyErr) {
            c.logger.Info("Organization still exists after delete request", "orgID", orgID, "error", verifyErr)
            cr.Status.AtProvider.LastDeletionAttemptError = "organization still exists after delete request"
            _ = c.updateStatusWithRetry(ctx, cr)
            return errors.Wrap(verifyErr, "organization delete verification failed")
        }
    }

    c.logger.Info("Organization deletion verified — cleaning up secrets")
    return c.cleanupSecretsAfterDelete(ctx, cr, now)
}

// isAtlasTransientOrOpaqueDeleteErr treats opaque/5xx/network errors as success
// because Atlas frequently returns 500 after the org has actually been deleted.
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
    if strings.Contains(e, "ip address") && strings.Contains(e, "is not allowed to access this resource") {
        return true
    }
    return false
}

// cleanupSecretsAfterDelete deletes the AWS/K8s secret after org deletion
// and force-removes the crossplane managed finalizer so the CR is garbage-collected.
// Force removal is required when the Atlas org can't be verified as deleted
// (e.g. the org-specific API key/secret is gone) — otherwise Observe would keep
// reporting the external resource as existing and the reconciler would loop.
func (c *external) cleanupSecretsAfterDelete(ctx context.Context, cr *v1alpha1.Organization, now metav1.Time) error {
    orgID := meta.GetExternalName(cr)

    currentStorageType := cr.Status.AtProvider.SecretStorageType
    if currentStorageType == string(v1alpha1.SecretStorageTypeKubernetes) {
        c.logger.Info("Deleting Kubernetes secret after org deletion", "orgID", orgID)
        if err := c.deleteK8sSecret(ctx, cr); err != nil {
            c.logger.Info("Failed to delete Kubernetes secret (continuing)", "error", err)
        }
    } else if c.awsClient != nil {
        secretName := cr.Status.AtProvider.SecretName
        if secretName == "" {
            secretName = finalSecretName(cr)
        }
        c.logger.Info("Deleting AWS secret after org deletion", "orgID", orgID, "secretName", secretName)
        if err := c.awsClient.DeleteSecret(ctx, secretName, true); err != nil {
            c.logger.Info("Failed to delete AWS secret (continuing)", "secretName", secretName, "error", err)
        }
    }

    stateDeleted := v1alpha1.OrganizationStateDeleted
    cr.Status.AtProvider.State = &stateDeleted
    cr.Status.AtProvider.DeletedAt = &now
    cr.Status.AtProvider.LastDeletionAttemptError = ""
    _ = c.updateStatusWithRetry(ctx, cr)

    // Force-remove the crossplane managed finalizer so the CR can be garbage collected.
    latest := &v1alpha1.Organization{}
    if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.Name}, latest); err == nil {
        if meta.FinalizerExists(latest, "finalizer.managedresource.crossplane.io") {
            meta.RemoveFinalizer(latest, "finalizer.managedresource.crossplane.io")
            if err := c.kube.Update(ctx, latest); err != nil && !apierrors.IsNotFound(err) {
                c.logger.Info("Failed to remove managed finalizer (continuing)", "error", err)
            }
        }
    }

    c.logger.Info("Organization deletion completed", "orgID", orgID)
    return nil
}
