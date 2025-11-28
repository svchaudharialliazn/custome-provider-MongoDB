## 5. MongoDB Client (internal/clients/mongodb/client.go)

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

package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/icholy/digest"
)

// Service interface defines MongoDB Atlas operations
type Service interface {
	CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, string, error)
	GetOrganization(ctx context.Context, id string) (*Organization, error)
	UpdateOrganization(ctx context.Context, input UpdateOrganizationInput) (*Organization, error)
	DeleteOrganization(ctx context.Context, id string) error
}

// Credentials represents MongoDB Atlas API credentials
// Retrieved from AWS Secrets Manager - NOT Kubernetes secrets
type Credentials struct {
	APIKey string
}

// Organization represents a MongoDB Atlas organization
type Organization struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	OrgOwnerId string    `json:"orgOwnerId"`
	IsDeleted  bool      `json:"isDeleted"`
	Created    time.Time `json:"created"`
	Links      []Link    `json:"links"`
}

// Link represents API links in Atlas responses
type Link struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

// CreateOrganizationInput defines input for creating an organization
type CreateOrganizationInput struct {
	OwnerID string      `json:"ownerID"`
	APIKey  APIKeyInput `json:"apiKey"`
}

// APIKeyInput defines API key creation parameters
type APIKeyInput struct {
	Description string   `json:"description"`
	Roles       []string `json:"roles"`
}

// UpdateOrganizationInput defines input for updating an organization
type UpdateOrganizationInput struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// APIKeyResponse represents API key creation response
type APIKeyResponse struct {
	ID          string `json:"id"`
	Description string `json:"desc"`
	Roles       []Role `json:"roles"`
	PrivateKey  string `json:"privateKey"`
	PublicKey   string `json:"publicKey"`
}

// Role represents an API key role
type Role struct {
	RoleName string `json:"roleName"`
	OrgId    string `json:"orgId"`
}

// Error represents MongoDB Atlas API error
type Error struct {
	Code   int    `json:"error"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`
}

func (e Error) Error() string {
	return fmt.Sprintf("MongoDB Atlas API error %d: %s - %s", e.Code, e.Reason, e.Detail)
}

// NotFoundError represents a 404 error from the API
type NotFoundError struct {
	Err Error
}

func (e *NotFoundError) Error() string {
	return e.Err.Error()
}

// client implements the Service interface
type client struct {
	httpClient  *http.Client
	baseURL     string
	credentials Credentials
}

// NewService creates a new MongoDB Atlas service client
// Uses credentials retrieved from AWS Secrets Manager
func NewService(creds Credentials) Service {
	return &client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &digest.Transport{
				Username: creds.APIKey,
				Password: "", // Atlas uses API key as username with empty password
			},
		},
		baseURL:     "https://cloud.mongodb.com/api/atlas/v1.0",
		credentials: creds,
	}
}

// CreateOrganization creates a new MongoDB Atlas organization and returns the API key
// API key will be stored in AWS Secrets Manager - NOT Kubernetes secrets
func (c *client) CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, string, error) {
	// First, create the organization
	orgPayload := map[string]interface{}{
		"name":       fmt.Sprintf("Org-%s", input.OwnerID[:8]), // Generate name from owner ID
		"orgOwnerId": input.OwnerID,
	}

	org := &Organization{}
	if err := c.makeRequest(ctx, "POST", "/orgs", orgPayload, org); err != nil {
		return nil, "", errors.Wrap(err, "cannot create organization")
	}

	// Create API key for the organization
	apiKeyPayload := map[string]interface{}{
		"desc":  input.APIKey.Description,
		"roles": convertRolesToAPIFormat(input.APIKey.Roles, org.ID),
	}

	apiKeyResp := &APIKeyResponse{}
	endpoint := fmt.Sprintf("/orgs/%s/apiKeys", org.ID)
	if err := c.makeRequest(ctx, "POST", endpoint, apiKeyPayload, apiKeyResp); err != nil {
		// Clean up the organization if API key creation fails
		_ = c.DeleteOrganization(ctx, org.ID)
		return nil, "", errors.Wrap(err, "cannot create API key for organization")
	}

	// The private key is the actual API key that will be stored in AWS Secrets Manager
	return org, apiKeyResp.PrivateKey, nil
}

// GetOrganization retrieves an organization by ID
func (c *client) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	org := &Organization{}
	endpoint := fmt.Sprintf("/orgs/%s", id)
	if err := c.makeRequest(ctx, "GET", endpoint, nil, org); err != nil {
		return nil, errors.Wrap(err, "cannot get organization")
	}
	return org, nil
}

// UpdateOrganization updates an organization
func (c *client) UpdateOrganization(ctx context.Context, input UpdateOrganizationInput) (*Organization, error) {
	payload := make(map[string]interface{})
	if input.Name != "" {
		payload["name"] = input.Name
	}

	org := &Organization{}
	endpoint := fmt.Sprintf("/orgs/%s", input.ID)
	if err := c.makeRequest(ctx, "PATCH", endpoint, payload, org); err != nil {
		return nil, errors.Wrap(err, "cannot update organization")
	}
	return org, nil
}

// DeleteOrganization deletes an organization
func (c *client) DeleteOrganization(ctx context.Context, id string) error {
	endpoint := fmt.Sprintf("/orgs/%s", id)
	if err := c.makeRequest(ctx, "DELETE", endpoint, nil, nil); err != nil {
		return errors.Wrap(err, "cannot delete organization")
	}
	return nil
}

// makeRequest makes an HTTP request to the MongoDB Atlas API
func (c *client) makeRequest(ctx context.Context, method, endpoint string, payload interface{}, result interface{}) error {
	url := c.baseURL + endpoint

	var body *strings.Reader
	if payload != nil {
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return errors.Wrap(err, "cannot marshal payload")
		}
		body = strings.NewReader(string(jsonData))
	}

	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return errors.Wrap(err, "cannot create request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "cannot make request")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr Error
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
			return errors.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
		}

		// Handle specific error cases
		if resp.StatusCode == 404 {
			return &NotFoundError{Err: apiErr}
		}

		return apiErr
	}

	if result != nil && resp.StatusCode != 204 {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return errors.Wrap(err, "cannot decode response")
		}
	}

	return nil
}

// convertRolesToAPIFormat converts role strings to Atlas API format
func convertRolesToAPIFormat(roles []string, orgID string) []map[string]string {
	apiRoles := make([]map[string]string, len(roles))
	for i, role := range roles {
		apiRoles[i] = map[string]string{
			"roleName": role,
			"orgId":    orgID,
		}
	}
	return apiRoles
}

// IsNotFoundError checks if an error is a not found error
func IsNotFoundError(err error) bool {
	_, ok := err.(*NotFoundError)
	return ok
}
```