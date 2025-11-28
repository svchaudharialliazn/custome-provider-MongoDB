package aws

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/pkg/errors"
)

// Client wraps AWS services for KMS and Secrets Manager operations.
type Client struct {
	KMSClient            *kms.Client
	SecretsManagerClient *secretsmanager.Client
	Region               string
}

// MongoDBAPICredentials represents the structure of MongoDB API credentials.
type MongoDBAPICredentials struct {
	PublicKey  string   `json:"publicKey"`
	PrivateKey string   `json:"privateKey"`
//	OrgID      string   `json:"orgId"`
//	Roles      []string `json:"roles"`
//	CreatedAt  string   `json:"createdAt,omitempty"`
}

// NewClient creates a new AWS client with KMS and Secrets Manager services.
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

// PutSecret creates or updates a MongoDB API key secret in AWS Secrets Manager.
func (c *Client) PutSecret(ctx context.Context, secretName string, creds MongoDBAPICredentials, OrgID string, kmsKeyID *string) (string, error) {
	data, err := json.Marshal(creds)
	if err != nil {
		return "", errors.Wrap(err, "cannot marshal MongoDB credentials")
	}

	input := &secretsmanager.CreateSecretInput{
		Name:         aws.String(secretName),
		SecretString: aws.String(string(data)),
		Description:  aws.String(fmt.Sprintf("MongoDB API credentials for organization %s", OrgID)),
		Tags: []smtypes.Tag{
			{Key: aws.String("Provider"), Value: aws.String("mongodb-crossplane")},
			{Key: aws.String("OrgID"), Value: aws.String(OrgID)},
			{Key: aws.String("CreatedBy"), Value: aws.String("crossplane-mongodb-provider")},
		},
	}
	if kmsKeyID != nil {
		input.KmsKeyId = kmsKeyID
	}

	resp, err := c.SecretsManagerClient.CreateSecret(ctx, input)
	if err != nil {
		var existsErr *smtypes.ResourceExistsException
		if errors.As(err, &existsErr) {
			// Secret exists, update it
			updateInput := &secretsmanager.UpdateSecretInput{
				SecretId:     aws.String(secretName),
				SecretString: aws.String(string(data)),
			}
			if kmsKeyID != nil {
				updateInput.KmsKeyId = kmsKeyID
			}
			respUpdate, err := c.SecretsManagerClient.UpdateSecret(ctx, updateInput)
			if err != nil {
				return "", errors.Wrap(err, "cannot update existing AWS secret")
			}
			return aws.ToString(respUpdate.ARN), nil
		}
		return "", errors.Wrap(err, "cannot create AWS secret")
	}

	return aws.ToString(resp.ARN), nil
}

// GetSecret retrieves and decrypts a secret from AWS Secrets Manager.
func (c *Client) GetSecret(ctx context.Context, secretName string) (*MongoDBAPICredentials, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	}
	resp, err := c.SecretsManagerClient.GetSecretValue(ctx, input)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get secret from AWS Secrets Manager")
	}
	if resp.SecretString == nil {
		return nil, errors.New("secret string is nil")
	}

	var creds MongoDBAPICredentials
	if err := json.Unmarshal([]byte(*resp.SecretString), &creds); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal AWS secret JSON")
	}
	return &creds, nil
}

// DescribeSecret returns secret metadata including ARN.
func (c *Client) DescribeSecret(ctx context.Context, secretName string) (*secretsmanager.DescribeSecretOutput, error) {
	return c.SecretsManagerClient.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(secretName),
	})
}

// DeleteSecret deletes a secret from AWS Secrets Manager.
func (c *Client) DeleteSecret(ctx context.Context, secretName string, forceDelete bool) error {
	input := &secretsmanager.DeleteSecretInput{
		SecretId: aws.String(secretName),
	}
	if forceDelete {
		input.ForceDeleteWithoutRecovery = aws.Bool(true)
	} else {
		input.RecoveryWindowInDays = aws.Int64(7)
	}

	_, err := c.SecretsManagerClient.DeleteSecret(ctx, input)
	if err != nil {
		return errors.Wrap(err, "cannot delete secret from AWS Secrets Manager")
	}
	return nil
}
