# swapnil-provider-mongodb

A [Crossplane](https://crossplane.io/) Provider for managing **MongoDB Atlas** resources with **AWS Secrets Manager** integration. It enables Kubernetes-native lifecycle management of MongoDB Atlas organizations and AWS VPC private connectivity — with all credentials stored securely in AWS Secrets Manager (optionally encrypted via AWS KMS) instead of Kubernetes Secrets.

---

## Architecture

```
Kubernetes Cluster
│
├── Crossplane
│   └── swapnil-provider-mongodb
│       ├── Organization Controller  ──► MongoDB Atlas API (digest auth)
│       ├── VPCEndpoint Controller   ──► AWS Private Link / VPC Endpoint
│       └── Config Controller        ──► AWS Secrets Manager (credentials)
│
└── ProviderConfig
    └── AWS Secrets Manager
        └── secret: product/mongodb/{secretName}
            ├── publicKey
            └── privateKey (optionally KMS-encrypted)
```

The provider follows a **hybrid AWS + MongoDB Atlas** pattern:
- MongoDB Atlas API keys are stored in AWS Secrets Manager, not Kubernetes Secrets
- AWS IAM is used for authentication to Secrets Manager
- Optional AWS KMS encryption for additional security
- All resources follow Crossplane's managed resource reconciliation pattern

---

## Managed Resources

### `Organization` (`organization.mongodb.allianz.io/v1alpha1`)

Manages a MongoDB Atlas Organization lifecycle — create, read, update, delete.

```yaml
apiVersion: organization.mongodb.allianz.io/v1alpha1
kind: Organization
metadata:
  name: my-org
spec:
  forProvider:
    orgOwnerId: "<OWNER_USER_ID>"
    apiAccessListRequired: false
    multiFactorAuthRequired: false
    restrictEmployeeAccess: false
    roles:
      - roleName: ORG_OWNER
        userId: "<USER_ID>"
    aws:
      region: eu-central-1
      secretsManager:
        secretName: my-org-credentials
        kmsKeyId: "arn:aws:kms:eu-central-1:123456789:key/your-key-id"
  providerConfigRef:
    name: default
```

### `VPCEndpoint` (`connectivity.mongodb.allianz.io/v1alpha1`)

Manages AWS Private Link VPC Endpoint connectivity for MongoDB Atlas private networking.

```yaml
apiVersion: connectivity.mongodb.allianz.io/v1alpha1
kind: VPCEndpoint
metadata:
  name: my-vpc-endpoint
spec:
  forProvider:
    region: eu-central-1
    vpcId: vpc-0abc1234def567890
    subnetIds:
      - subnet-0abc1234def567890
    securityGroupIds:
      - sg-0abc1234def567890
    endpointType: Interface
    ipAddressType: ipv4
  providerConfigRef:
    name: default
```

### `ProviderConfig` (`mongodb.allianz.io/v1alpha1`)

Configures how the provider authenticates — pointing to credentials stored in AWS Secrets Manager.

```yaml
apiVersion: mongodb.allianz.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: AWS
    aws:
      secretsManager:
        region: eu-central-1
        secretName: mongodb-atlas-credentials
        kmsKeyId: "arn:aws:kms:eu-central-1:123456789:key/your-key-id"  # optional
```

---

## Prerequisites

- [Crossplane](https://crossplane.io/) v1.13+ installed in your Kubernetes cluster
- AWS IAM credentials with access to:
  - `secretsmanager:GetSecretValue`, `PutSecretValue`, `DeleteSecret`
  - `kms:Encrypt`, `kms:Decrypt` (if using KMS encryption)
- MongoDB Atlas API keys (Public + Private)
- Go 1.22+ (for development)
- Docker (for building images)

---

## Installation

### 1. Store MongoDB Atlas credentials in AWS Secrets Manager

```bash
aws secretsmanager create-secret \
  --name "product/mongodb/my-credentials" \
  --region eu-central-1 \
  --secret-string '{"publicKey":"<YOUR_PUBLIC_KEY>","privateKey":"<YOUR_PRIVATE_KEY>"}'
```

### 2. Install the provider

Apply the provider package to your Crossplane-enabled cluster:

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: swapnil-provider-mongodb
spec:
  package: docker.io/svchaudharialliazn/swap-provider-mongodb:v0.1.1
```

### 3. Configure AWS credentials for the provider pod

```bash
kubectl apply -f examples/test/runtimeconfig.yaml
kubectl apply -f examples/test/druntime.yaml
```

### 4. Create a ProviderConfig

```bash
kubectl apply -f examples/provider/providerconfig-aws-only.yaml
```

### 5. Create managed resources

```bash
# Create an Organization
kubectl apply -f examples/organization/organization-pure-aws.yaml

# Create a VPC Endpoint
kubectl apply -f examples/privatelink/example.yml
```

---

## Developing

### Setup

```bash
git clone https://github.com/svchaudharialliazn/custome-provider-MongoDB.git
cd custome-provider-MongoDB
make vendor
```

### Code generation

```bash
make generate      # Regenerate deepcopy, managed resource interfaces
make manifests     # Regenerate CRD manifests
```

### Build

```bash
make build                  # Build provider binary → ./_output/bin/provider
make build-all              # Build for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
make docker-build           # Build single-arch Docker image
make docker-build-multiarch # Build multi-arch image
```

### Test

```bash
make test           # Run unit tests
make test-coverage  # Run tests with coverage report
make lint           # Run golangci-lint
make fmt            # Format code with goimports
make vet            # Run go vet
make reviewable     # vendor + generate + lint + test (full pre-PR check)
```

### Local development with KinD

```bash
make dev-kind-create        # Create a local KinD cluster
make dev-install-crossplane # Install Crossplane via Helm
make dev-deploy             # Deploy provider to KinD cluster
make dev-run                # Run provider locally with debug logging
```

### Full release build

```bash
make release   # reviewable + build.xpkg + docker-build-multiarch + push.xpkg
```

---

## Docker Images

| Image | Registry |
|---|---|
| Provider controller | `docker.io/svchaudharialliazn/swap-provider-mongodb-controller:v0.1.0` |
| Crossplane package | `docker.io/svchaudharialliazn/swap-provider-mongodb:v0.1.1` |

---

## Project Structure

```
.
├── apis/
│   ├── v1alpha1/                        # ProviderConfig, StoreConfig types
│   ├── organization/v1alpha1/           # Organization managed resource type
│   ├── connectivity/v1alpha1/           # VPCEndpoint managed resource type
│   └── mongodb.go                       # API scheme registration
├── cmd/provider/main.go                 # Provider entrypoint
├── config/crd/                          # Generated CRD manifests
├── examples/
│   ├── provider/                        # ProviderConfig examples
│   ├── organization/                    # Organization examples
│   ├── privatelink/                     # VPC Endpoint examples
│   ├── composition-xrd-claim/           # Crossplane Composition examples
│   └── test/                            # Test resources
├── internal/
│   ├── clients/
│   │   ├── mongodb/client.go            # MongoDB Atlas HTTP client (digest auth)
│   │   ├── connectivity/connectivity.go # VPC Endpoint AWS client
│   │   └── atlas/organization.go        # Atlas organization API
│   └── controller/
│       ├── organization/                # Organization reconciler
│       ├── vpcendpoint/                 # VPCEndpoint reconciler
│       └── config/                      # ProviderConfig reconciler
├── package/
│   ├── crossplane.yaml                  # Provider package metadata
│   └── crds/                            # CRDs bundled in the package
├── scripts/
│   ├── create-provider-credentials.sh  # Set up credentials in AWS Secrets Manager
│   └── test-pure-aws-provider.sh       # End-to-end test script
├── Dockerfile                           # Multi-stage build (golang + distroless)
├── Makefile
└── Taskfile.dist.yaml
```

---

## Key Design Decisions

| Decision | Detail |
|---|---|
| Credential storage | AWS Secrets Manager (`product/mongodb/{secretName}`) — no Kubernetes Secrets |
| Authentication to Atlas | HTTP Digest authentication with public/private API keys |
| Encryption | Optional AWS KMS encryption for secrets at rest |
| AWS auth | AWS IAM (injected via pod environment / runtime config) |
| Error handling | Distinguishes `NotFound`, `Retryable`, `Conflict` for proper reconciliation |
| Resource cleanup | Custom finalizer `organization.platform.allianz.io/cleanup` for graceful deletion |
| External secret stores | Alpha feature flag support for Vault and other ESS backends |

---

## Dependencies

| Dependency | Version | Purpose |
|---|---|---|
| `crossplane/crossplane-runtime` | v1.13.0 | Crossplane managed resource framework |
| `aws/aws-sdk-go-v2` | v1.38.3 | AWS SDK (KMS + Secrets Manager) |
| `sigs.k8s.io/controller-runtime` | v0.15.0 | Kubernetes controller framework |
| `icholy/digest` | v1.1.0 | HTTP Digest authentication for Atlas API |
| `go.uber.org/zap` | v1.25.0 | Structured logging |

---

## Contributing

Refer to Crossplane's [CONTRIBUTING.md](https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md) and the [Provider Development Guide](https://github.com/crossplane/crossplane/blob/master/docs/contributing/provider_development_guide.md) for contribution guidelines.

This repository follows the [Developer Certificate of Origin (DCO)](DCO).

---

## License

[Apache License 2.0](LICENSE)
