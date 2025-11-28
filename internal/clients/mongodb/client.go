package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/icholy/digest"
	"github.com/pkg/errors"
)

// Service defines operations for managing MongoDB Atlas organizations.
type Service interface {
	CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, APIKeyPair, error)
	GetOrganization(ctx context.Context, id string) (*Organization, error)
	UpdateOrganization(ctx context.Context, input UpdateOrganizationInput) (*Organization, error)
	DeleteOrganization(ctx context.Context, id string) error
	// ADD: Verify organization deletion with child resource checking
	VerifyOrganizationDeletion(ctx context.Context, id string) error
}

// Credentials stores public/private API keys.
type Credentials struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

// Organization represents a MongoDB Atlas organization.
type Organization struct {
	ID         string    `json:"id,omitempty"`
	Name       string    `json:"name"`
	OrgOwnerId string    `json:"orgOwnerId"`
	IsDeleted  bool      `json:"isDeleted"`
	Created    time.Time `json:"created,omitempty"`
}

// APIKey describes an API key for creation payload
type APIKey struct {
	Description string   `json:"desc"`
	Roles       []string `json:"roles"`
}

// CreateOrganizationInput specifies details for org creation.
type CreateOrganizationInput struct {
	Name    string `json:"name"`
	OwnerID string `json:"ownerId"`
	APIKey  APIKey `json:"apiKey"`
}

// UpdateOrganizationInput specifies details for org update.
type UpdateOrganizationInput struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// APIKeyPair stores public/private keys.
type APIKeyPair struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

// Error represents an API error response.
type Error struct {
	Code   int    `json:"error"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`
}

func (e Error) Error() string {
	return fmt.Sprintf("MongoDB Atlas API error %d: %s - %s", e.Code, e.Reason, e.Detail)
}

// NotFoundError signals a missing resource.
// ADD: Enhanced NotFoundError for deletion handling
type NotFoundError struct{ Err Error }

func (e *NotFoundError) Error() string { return e.Err.Error() }

// ADD: RetryableError signals an error that should be retried
type RetryableError struct {
	Err error
	Msg string
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %s - %v", e.Msg, e.Err)
}

// ADD: ConflictError signals resource is in transition
type ConflictError struct {
	Err error
	Msg string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("conflict error (resource in transition): %s - %v", e.Msg, e.Err)
}

// client implements Service.
type client struct {
	httpClient  *http.Client
	baseURL     string
	credentials Credentials
}

// NewService returns a new MongoDB client.
func NewService(creds Credentials) Service {
	transport := &digest.Transport{Username: creds.PublicKey, Password: creds.PrivateKey}
	return &client{
		httpClient:  &http.Client{Timeout: 30 * time.Second, Transport: transport},
		baseURL:     "https://cloud.mongodb.com/api/atlas/v1.0",
		credentials: creds,
	}
}

// ErrNotFound standard error for missing resources.
var ErrNotFound = errors.New("not found")

// ADD: Error classification functions
func IsNotFoundError(err error) bool {
	_, ok := err.(*NotFoundError)
	return ok
}

func IsRetryableError(err error) bool {
	switch err.(type) {
	case *RetryableError:
		return true
	}
	return false
}

func IsConflictError(err error) bool {
	switch err.(type) {
	case *ConflictError:
		return true
	}
	return false
}

// CreateOrgPayload allows to serialize combined org + API key request
type CreateOrgPayload struct {
	Name       string `json:"name"`
	OrgOwnerID string `json:"orgOwnerId"`
	APIKey     APIKey `json:"apiKey"`
}

// CreateOrgResponse contains both org + API key
type CreateOrgResponse struct {
	APIKey struct {
		ID         string `json:"id"`
		PrivateKey string `json:"privateKey"`
		PublicKey  string `json:"publicKey"`
	} `json:"apiKey"`
	Organization struct {
		ID        string `json:"id"`
		IsDeleted bool   `json:"isDeleted"`
		Name      string `json:"name"`
	} `json:"organization"`
}

func (c *client) CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, APIKeyPair, error) {
	if input.Name == "" {
		return nil, APIKeyPair{}, errors.New("organization name cannot be empty")
	}
	if input.OwnerID == "" {
		return nil, APIKeyPair{}, errors.New("organization ownerID cannot be empty")
	}

	payload := CreateOrgPayload{
		Name:       input.Name,
		OrgOwnerID: input.OwnerID,
		APIKey:     input.APIKey,
	}

	orgResp := &CreateOrgResponse{}
	if err := c.makeRequest(ctx, http.MethodPost, "/orgs", payload, orgResp); err != nil {
		return nil, APIKeyPair{}, errors.Wrap(err, "cannot create organization")
	}

	org := &Organization{
		ID:         orgResp.Organization.ID,
		Name:       orgResp.Organization.Name,
		OrgOwnerId: input.OwnerID,
		IsDeleted:  orgResp.Organization.IsDeleted,
	}

	keys := APIKeyPair{
		PublicKey:  orgResp.APIKey.PublicKey,
		PrivateKey: orgResp.APIKey.PrivateKey,
	}

	return org, keys, nil
}

func (c *client) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	org := &Organization{}
	if err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s", id), nil, org); err != nil {
		return nil, err
	}
	return org, nil
}

func (c *client) UpdateOrganization(ctx context.Context, input UpdateOrganizationInput) (*Organization, error) {
	org := &Organization{}
	payload := map[string]interface{}{}
	if input.Name != "" {
		payload["name"] = input.Name
	}
	if err := c.makeRequest(ctx, http.MethodPatch, fmt.Sprintf("/orgs/%s", input.ID), payload, org); err != nil {
		return nil, err
	}
	return org, nil
}

// ADD: Enhanced DeleteOrganization with better error handling
func (c *client) DeleteOrganization(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("organization id cannot be empty")
	}
	
	return c.makeRequest(ctx, http.MethodDelete, fmt.Sprintf("/orgs/%s", id), nil, nil)
}

// ADD: VerifyOrganizationDeletion checks if organization is deleted
// Returns nil if deleted, error if still exists or verification failed
func (c *client) VerifyOrganizationDeletion(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("organization id cannot be empty")
	}

	// Try to get the organization
	org := &Organization{}
	err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s", id), nil, org)
	
	// 404 means successfully deleted
	if IsNotFoundError(err) {
		return nil
	}
	
	// Other errors are failures
	if err != nil {
		return errors.Wrap(err, "failed to verify organization deletion")
	}
	
	// If we got here, organization still exists
	return errors.Errorf("organization %s still exists", id)
}

// ADD: Enhanced makeRequest with better error categorization
func (c *client) makeRequest(ctx context.Context, method, endpoint string, payload interface{}, result interface{}) error {
	url := c.baseURL + endpoint
	var body io.Reader
	if payload != nil {
		j, err := json.Marshal(payload)
		if err != nil {
			return errors.Wrap(err, "marshal payload")
		}
		body = strings.NewReader(string(j))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return errors.Wrap(err, "create HTTP request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Network errors are retryable
		return &RetryableError{Err: err, Msg: "HTTP request failed (network error)"}
	}
	defer resp.Body.Close()

	// ADD: Categorize HTTP errors for better handling
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		var apiErr Error
		if err := json.Unmarshal(raw, &apiErr); err != nil {
			// If we can't parse the error, return generic error
			errorMsg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)
			if resp.StatusCode >= 500 {
				return &RetryableError{Err: errors.New(errorMsg), Msg: "server error"}
			}
			return errors.New(errorMsg)
		}

		// 404 Not Found - resource doesn't exist (success for deletion)
		if resp.StatusCode == 404 {
			return &NotFoundError{Err: apiErr}
		}

		// 409 Conflict - resource in transition (retryable)
		if resp.StatusCode == 409 {
			return &ConflictError{Err: apiErr, Msg: "resource in conflict state"}
		}

		// 429 Too Many Requests - rate limited (retryable)
		if resp.StatusCode == 429 {
			return &RetryableError{Err: apiErr, Msg: "rate limited"}
		}

		// 5xx Server Errors - retryable
		if resp.StatusCode >= 500 {
			return &RetryableError{Err: apiErr, Msg: "server error"}
		}

		// 4xx Client Errors (except those above) - not retryable
		return apiErr
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return errors.Wrap(err, "decode response")
		}
	}
	return nil
}
