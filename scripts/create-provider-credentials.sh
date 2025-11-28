#!/bin/bash
# Script: create-provider-credentials.sh
# Creates MongoDB Atlas provider credentials in AWS Secrets Manager

PROVIDER_SECRET_NAME="mongodb-crossplane/provider/atlas-credentials"
PROVIDER_API_KEY="YOUR_MONGODB_ATLAS_API_KEY_HERE"  # Replace with actual API key
AWS_REGION="us-east-1"
KMS_KEY_ID="arn:aws:kms:us-east-1:123456789012:key/your-kms-key-id"  # Optional

echo "Creating provider credentials in AWS Secrets Manager..."

aws secretsmanager create-secret \
  --name "$PROVIDER_SECRET_NAME" \
  --description "MongoDB Atlas provider API credentials for Crossplane" \
  --secret-string "{
    \"apiKey\": \"$PROVIDER_API_KEY\",
    \"description\": \"MongoDB Atlas provider credentials for Crossplane provider-mongodb-swap\"
  }" \
  --kms-key-id "$KMS_KEY_ID" \
  --tags '[
    {"Key": "Provider", "Value": "mongodb-crossplane"},
    {"Key": "Type", "Value": "provider-credentials"},
    {"Key": "Environment", "Value": "development"}
  ]' \
  --region "$AWS_REGION"

echo "Provider credentials created successfully!"
echo "Secret Name: $PROVIDER_SECRET_NAME"
echo "Region: $AWS_REGION"

# Verify the secret was created
echo -e "\nVerifying secret creation..."
aws secretsmanager describe-secret --secret-id "$PROVIDER_SECRET_NAME" --region "$AWS_REGION"
