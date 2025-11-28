#!/bin/bash
# Script: test-pure-aws-provider.sh
# Tests the complete pure AWS MongoDB provider

set -e

# Configuration
MONGODB_USER_ID="YOUR_MONGODB_USER_ID_HERE"  # Replace with your MongoDB Atlas user ID
AWS_REGION="us-east-1"
PROVIDER_SECRET_NAME="mongodb-crossplane/provider/atlas-credentials"

echo "=== Testing Pure AWS MongoDB Provider ==="

# Step 1: Verify AWS credentials
echo -e "\n1. Verifying AWS credentials..."
aws sts get-caller-identity
if [ $? -ne 0 ]; then
    echo "ERROR: AWS credentials not configured properly"
    exit 1
fi

# Step 2: Verify provider credentials exist in AWS
echo -e "\n2. Verifying provider credentials in AWS Secrets Manager..."
aws secretsmanager describe-secret --secret-id "$PROVIDER_SECRET_NAME" --region "$AWS_REGION" > /dev/null
if [ $? -ne 0 ]; then
    echo "ERROR: Provider credentials not found in AWS Secrets Manager"
    echo "Please run create-provider-credentials.sh first"
    exit 1
fi
echo "Provider credentials found: $PROVIDER_SECRET_NAME"

# Step 3: Build the provider
echo -e "\n3. Building the provider..."
cd provider-mongodb-swap
make generate
make build
echo "Provider built successfully"

# Step 4: Apply CRDs
echo -e "\n4. Applying CRDs..."
kubectl apply -f package/crds/ -R
echo "CRDs applied successfully"

# Step 5: Apply ProviderConfig
echo -e "\n5. Applying ProviderConfig..."
cat <<EOF | kubectl apply -f -
apiVersion: v1alpha1.mongodb.allianz.io/v1alpha1
kind: ProviderConfig
metadata:
  name: atlas-provider-aws-only
spec:
  credentials:
    source: AWS
    aws:
      secretsManager:
        region: "$AWS_REGION"
        secretName: "$PROVIDER_SECRET_NAME"
EOF
echo "ProviderConfig applied successfully"

# Step 6: Start provider in background
echo -e "\n6. Starting provider controller..."
./bin/provider-mongodb-controller -d &
PROVIDER_PID=$!
echo "Provider started with PID: $PROVIDER_PID"

# Step 7: Wait for provider to be ready
echo -e "\n7. Waiting for provider to be ready..."
sleep 5

# Step 8: Apply Organization resource
echo -e "\n8. Creating Organization resource..."
cat <<EOF | kubectl apply -f -
apiVersion: organization.mongodb.allianz.io/v1alpha1
kind: Organization
metadata:
  name: test-org-pure-aws
spec:
  forProvider:
    ownerID: "$MONGODB_USER_ID"
    apiKey:
      description: "Pure AWS organization API key - no Kubernetes secrets"
      roles: ["ORG_OWNER"]
    awsSecretsConfig:
      region: "$AWS_REGION"
  providerConfigRef:
    name: atlas-provider-aws-only
EOF
echo "Organization resource created"

# Step 9: Monitor organization status
echo -e "\n9. Monitoring organization status..."
for i in {1..30}; do
    STATUS=$(kubectl get organization test-org-pure-aws -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
    if [ "$STATUS" = "True" ]; then
        echo "Organization is ready!"
        break
    elif [ "$STATUS" = "False" ]; then
        echo "Organization creation failed"
        kubectl describe organization test-org-pure-aws
        exit 1
    fi
    echo "Waiting for organization to be ready... ($i/30)"
    sleep 10
done

# Step 10: Verify no Kubernetes secrets were created
echo -e "\n10. Verifying NO Kubernetes secrets were created..."
MONGODB_SECRETS=$(kubectl get secrets -A | grep -i mongodb || true)
if [ -n "$MONGODB_SECRETS" ]; then
    echo "ERROR: Found MongoDB-related Kubernetes secrets:"
    echo "$MONGODB_SECRETS"
    echo "This violates the pure AWS requirement!"
    exit 1
else
    echo "✅ VERIFIED: No MongoDB-related Kubernetes secrets found"
fi

# Step 11: Verify AWS secret was created
echo -e "\n11. Verifying organization credentials in AWS Secrets Manager..."
ORG_ID=$(kubectl get organization test-org-pure-aws -o jsonpath='{.status.atProvider.orgID}')
SECRET_ARN=$(kubectl get organization test-org-pure-aws -o jsonpath='{.status.atProvider.secretARN}')

if [ -n "$SECRET_ARN" ]; then
    echo "✅ Organization API key stored in AWS: $SECRET_ARN"
    # Get the secret content (be careful with this in production)
    echo "Secret content:"
    aws secretsmanager get-secret-value --secret-id "$SECRET_ARN" --query SecretString --output text | jq .
else
    echo "ERROR: Organization secret ARN not found in status"
    exit 1
fi

# Step 12: List all AWS secrets created by the provider
echo -e "\n12. Listing all provider-created secrets in AWS..."
aws secretsmanager list-secrets \
    --filters Key=name,Values="mongodb-crossplane" \
    --query 'SecretList[].{Name:Name,Description:Description,CreatedDate:CreatedDate}' \
    --output table

# Step 13: Cleanup (optional)
read -p -e "\n13. Do you want to cleanup the test resources? (y/N): " CLEANUP
if [ "$CLEANUP" = "y" ] || [ "$CLEANUP" = "Y" ]; then
    echo "Cleaning up test resources..."
    kubectl delete organization test-org-pure-aws
    kubectl delete providerconfig atlas-provider-aws-only
    kill $PROVIDER_PID 2>/dev/null || true
    echo "Cleanup completed"
else
    echo "Keeping test resources. Provider PID: $PROVIDER_PID"
fi

echo -e "\n=== Test Completed Successfully ==="
echo "✅ Pure AWS integration verified"
echo "✅ Zero Kubernetes secrets confirmed"
echo "✅ All credentials stored in AWS Secrets Manager"
