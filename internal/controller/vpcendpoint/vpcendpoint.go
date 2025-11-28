package vpcendpoint

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/svchaudhari/Swap-Provider-MongoDB/apis/connectivity/v1alpha1"
	apisv1alpha1 "github.com/svchaudhari/Swap-Provider-MongoDB/apis/v1alpha1"
	"github.com/svchaudhari/Swap-Provider-MongoDB/internal/clients/aws"
	svc "github.com/svchaudhari/Swap-Provider-MongoDB/internal/clients/connectivity"
)

const (
	errNotVPCEndpoint = "managed resource is not a VPCEndpoint custom resource"
	errTrackPCUsage   = "cannot track ProviderConfig usage"
	errGetPC          = "cannot get ProviderConfig"
	errGetCreds       = "cannot get credentials"
	errInvalidPC      = "ProviderConfig must use AWS credential source"
)

// Setup adds a controller that reconciles VPCEndpoint managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.VPCEndpointGroupKind)

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.VPCEndpointGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:          mgr.GetClient(),
			usage:         resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			logger:        o.Logger,
			newAWSClientFn: aws.NewClient,
		}),
		managed.WithInitializers(),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.VPCEndpoint{}).
		WithEventFilter(resource.DesiredStateChanged()).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
	kube          client.Client
	usage         resource.Tracker
	logger        logging.Logger
	newAWSClientFn func(ctx context.Context, region string) (*aws.Client, error)
}

// Connect produces an ExternalClient using AWS-only credentials
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.VPCEndpoint)
	if !ok {
		return nil, errors.New(errNotVPCEndpoint)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	ref := cr.GetProviderConfigReference()
	if ref == nil || ref.Name == "" {
		return nil, errors.New(errGetPC)
	}

	if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	if pc.Spec.Credentials.Source != apisv1alpha1.CredentialsSourceAWS || pc.Spec.Credentials.AWS == nil {
		return nil, errors.New(errInvalidPC)
	}

	awsCfg := pc.Spec.Credentials.AWS
	if awsCfg.SecretsManager == nil {
		return nil, errors.New("provider config: aws.secretsManager config is required")
	}
	if awsCfg.SecretsManager.Region == "" {
		return nil, errors.New("provider config: aws.secretsManager.region is required")
	}
	if awsCfg.SecretsManager.SecretName == nil || *awsCfg.SecretsManager.SecretName == "" {
		return nil, errors.New("provider config: aws.secretsManager.secretName is required")
	}

	awsClient, err := c.newAWSClientFn(ctx, awsCfg.SecretsManager.Region)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create AWS client")
	}

	// Use GetSecret instead of GetProviderCredentials
	apiKeyCreds, err := awsClient.GetSecret(ctx, *awsCfg.SecretsManager.SecretName)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	client, err := svc.NewConnectivityClient("", apiKeyCreds.PrivateKey)
	if err != nil {
		return nil, err
	}

	return &external{
		kube:   c.kube,
		client: *client,
		logger: c.logger,
	}, nil
}

type external struct {
	kube   client.Client
	client svc.Client
	logger logging.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.VPCEndpoint)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotVPCEndpoint)
	}

	c.logger.Debug("Observing", "resource", cr.Name)
	id := meta.GetExternalName(cr)
	cr.Status.AtProvider.VpcEndpointID = id

	if id == "" {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	vpcEndpoint, err := c.client.GetVPCEndpointStatus(ctx, cr.Spec.ForProvider.AccountID, id, cr.Spec.ForProvider.Region)
	if errors.Is(err, svc.ErrNotFound) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.State = vpcEndpoint.State
	if vpcEndpoint.State == "available" {
		cr.SetConditions(xpv1.Available())
	} else {
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

	c.logger.Debug("Creating", "vpc-endpoint", cr.Name)
	params := svc.CreateVPCEndpointParams{
		VpcID:            cr.Spec.ForProvider.VpcID,
		ServiceName:      cr.Spec.ForProvider.ServiceName,
		SubnetIDs:        cr.Spec.ForProvider.SubnetIDs,
		SecurityGroupIDs: cr.Spec.ForProvider.SecurityGroupIDs,
		VpcEndpointType:  cr.Spec.ForProvider.VPCEndpointType,
		IPAddressType:    cr.Spec.ForProvider.IPAddressType,
		AccountID:        cr.Spec.ForProvider.AccountID,
		Region:           cr.Spec.ForProvider.Region,
	}

	res, err := c.client.CreateVPCEndpoint(ctx, params)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	c.logger.Debug("VPCEndpoint was created successfully", "id", res.VpcEndpoint.VpcEndpointID)
	meta.SetExternalName(cr, res.VpcEndpoint.VpcEndpointID)

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(_ context.Context, _ resource.Managed) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, errors.New("update is not supported")
}

func (c *external) Delete(_ context.Context, _ resource.Managed) error {
	return errors.New("delete is not supported")
}
