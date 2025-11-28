## 4. AWS Client Utilities (internal/clients/aws/client.go)

```go
/*
Copyright 2023 The Crossplane Authors.

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

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/pkg/errors"
)

// Client wraps AWS services for KMS and Secrets Manager operations
// ELIMINATES ALL Kubernetes secret interactions
type Client struct {
	KMSClient            *kms.Client
	SecretsManagerClient *secretsmanager.Client
	Region               string
}

// MongoDBAPICredentials represents the structure of MongoDB API credentials
// Stored ONLY in AWS Secrets Manager - NO Kubernetes secrets
type MongoDBAPICredentials struct {
	APIKey      string   `json:"apiKey"`
	Description string   `json:"description"`
	OrgID       string   `json:"orgId"`
	Roles       []string `json:"roles"`
	CreatedAt   string   `json:"createdAt"`
}

// NewClient creates a new AWS client with KMS and Secrets Manager services
// NO Kubernetes credential extraction - pure AWS IAM authentication
func NewClient(ctx context.Context, region string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, errors.Wrap(err, "cannot load AWS config")
	}

	return &Client{
		KMSClient:            kms.NewFromConfig(cfg),
		SecretsManagerClient: secretsmanager.NewFromConfig(cfg),
		Region:               region,
	}, nil
}

// CreateSecret creates a new secret in AWS Secrets Manager with KMS encryption
// REPLACES Kubernetes secret creation entirely
func (c *Client) CreateSecret(ctx context.Context, secretName string, credentials MongoDBAPICredentials, kmsKeyID *string) (*secretsmanager.CreateSecretOutput, error) {
	// Marshal credentials to JSON
	credentialsJSON, err := json.Marshal(credentials)
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal credentials to JSON")
	}

	input := &secretsmanager.CreateSecretInput{
		Name:         aws.String(secretName),
		SecretString: aws.String(string(credentialsJSON)),
		Description:  aws.String(fmt.Sprintf("MongoDB API credentials for organization %s", credentials.OrgID)),
		Tags: []smtypes.Tag{
			{
				Key:   aws.String("Provider"),
				Value: aws.String("mongodb-crossplane"),
			},
			{
				Key:   aws.String("OrgID"),
				Value: aws.String(credentials.OrgID),
			},
			{
				Key:   aws.String("CreatedBy"),
				Value: aws.String("crossplane-mongodb-provider"),
			},
		},
	}

	// Use custom KMS key if specified, otherwise use default aws/secretsmanager key
	if kmsKeyID != nil && *kmsKeyID != "" {
		input.KmsKeyId = kmsKeyID
	}

	result, err := c.SecretsManagerClient.CreateSecret(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create secret in AWS Secrets Manager")
	}

	return result, nil
}

// GetSecret retrieves and decrypts a secret from AWS Secrets Manager
// REPLACES Kubernetes secret retrieval entirely
func (c *Client) GetSecret(ctx context.Context, secretName string) (*MongoDBAPICredentials, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	}

	result, err := c.SecretsManagerClient.GetSecretValue(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get secret from AWS Secrets Manager")
	}

	if result.SecretString == nil {
		return nil, errors.New("secret string is nil")
	}

	var credentials MongoDBAPICredentials
	if err := json.Unmarshal([]byte(*result.SecretString), &credentials); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal secret JSON")
	}

	return &credentials, nil
}

// UpdateSecret updates an existing secret in AWS Secrets Manager
func (c *Client) UpdateSecret(ctx context.Context, secretName string, credentials MongoDBAPICredentials) (*secretsmanager.UpdateSecretOutput, error) {
	// Marshal credentials to JSON
	credentialsJSON, err := json.Marshal(credentials)
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal credentials to JSON")
	}

	input := &secretsmanager.UpdateSecretInput{
		SecretId:     aws.String(secretName),
		SecretString: aws.String(string(credentialsJSON)),
		Description:  aws.String(fmt.Sprintf("MongoDB API credentials for organization %s (updated)", credentials.OrgID)),
	}

	result, err := c.SecretsManagerClient.UpdateSecret(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot update secret in AWS Secrets Manager")
	}

	return result, nil
}

// DeleteSecret deletes a secret from AWS Secrets Manager
// REPLACES Kubernetes secret deletion
func (c *Client) DeleteSecret(ctx context.Context, secretName string, forceDelete bool) (*secretsmanager.DeleteSecretOutput, error) {
	input := &secretsmanager.DeleteSecretInput{
		SecretId: aws.String(secretName),
	}

	if forceDelete {
		input.ForceDeleteWithoutRecovery = aws.Bool(true)
	} else {
		// Set recovery window to 7 days (minimum)
		input.RecoveryWindowInDays = aws.Int64(7)
	}

	result, err := c.SecretsManagerClient.DeleteSecret(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot delete secret from AWS Secrets Manager")
	}

	return result, nil
}

// EncryptData encrypts data using AWS KMS
func (c *Client) EncryptData(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	input := &kms.EncryptInput{
		KeyId:     aws.String(keyID),
		Plaintext: plaintext,
	}

	result, err := c.KMSClient.Encrypt(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot encrypt data with AWS KMS")
	}

	return result.CiphertextBlob, nil
}

// DecryptData decrypts data using AWS KMS
func (c *Client) DecryptData(ctx context.Context, ciphertextBlob []byte) ([]byte, error) {
	input := &kms.DecryptInput{
		CiphertextBlob: ciphertextBlob,
	}

	result, err := c.KMSClient.Decrypt(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot decrypt data with AWS KMS")
	}

	return result.Plaintext, nil
}

// ListSecrets lists secrets with a specific prefix
func (c *Client) ListSecrets(ctx context.Context, namePrefix string) ([]string, error) {
	input := &secretsmanager.ListSecretsInput{}

	if namePrefix != "" {
		input.Filters = []smtypes.Filter{
			{
				Key:    smtypes.FilterNameStringTypeName,
				Values: []string{namePrefix},
			},
		}
	}

	var secretNames []string
	paginator := secretsmanager.NewListSecretsPaginator(c.SecretsManagerClient, input)

	for paginator.HasMorePages() {
		result, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "cannot list secrets from AWS Secrets Manager")
		}

		for _, secret := range result.SecretList {
			if secret.Name != nil {
				secretNames = append(secretNames, *secret.Name)
			}
		}
	}

	return secretNames, nil
}

// ValidateKMSKey validates that the specified KMS key exists and is usable
func (c *Client) ValidateKMSKey(ctx context.Context, keyID string) error {
	input := &kms.DescribeKeyInput{
		KeyId: aws.String(keyID),
	}

	result, err := c.KMSClient.DescribeKey(ctx, input)
	if err != nil {
		return errors.Wrap(err, "cannot describe KMS key")
	}

	if result.KeyMetadata == nil {
		return errors.New("KMS key metadata is nil")
	}

	if result.KeyMetadata.KeyState != kmstypes.KeyStateEnabled {
		return errors.Errorf("KMS key is not enabled, current state: %s", result.KeyMetadata.KeyState)
	}

	return nil
}

// GenerateSecretName generates a unique secret name for MongoDB organization credentials
func GenerateSecretName(orgID string) string {
	return fmt.Sprintf("mongodb-crossplane/org-%s/credentials", orgID)
}

// IsNotFoundError checks if an error is a not found error from AWS services
func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	
	// Check for AWS Secrets Manager ResourceNotFoundException
	if strings.Contains(err.Error(), "ResourceNotFoundException") {
		return true
	}
	
	// Check for KMS NotFoundException
	if strings.Contains(err.Error(), "NotFoundException") {
		return true
	}
	
	return false
}

// GetProviderCredentials retrieves provider credentials from AWS Secrets Manager
// ELIMINATES provider credential retrieval from Kubernetes secrets
func (c *Client) GetProviderCredentials(ctx context.Context, secretName, secretKey string) (string, error) {
	// Get the complete secret
	credentials, err := c.GetSecret(ctx, secretName)
	if err != nil {
		return "", errors.Wrap(err, "cannot get provider credentials from AWS Secrets Manager")
	}

	// Extract the specific key (default: "apiKey")
	switch secretKey {
	case "apiKey", "":
		return credentials.APIKey, nil
	default:
		return "", errors.Errorf("unsupported provider credential key: %s", secretKey)
	}
}
```