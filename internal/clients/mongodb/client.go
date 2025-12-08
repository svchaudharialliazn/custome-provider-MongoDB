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
	GetOrganizationByName(ctx context.Context, name string) (*Organization, error)
	GetOrganizationByID(ctx context.Context, id string) (*Organization, error)
	UpdateOrganization(ctx context.Context, input UpdateOrganizationInput) (*Organization, error)
	DeleteOrganization(ctx context.Context, id string) error
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
	OwnerID string `json:"orgOwnerId"`
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
type NotFoundError struct{ Err Error }

func (e *NotFoundError) Error() string { return e.Err.Error() }

// RetryableError signals an error that should be retried
type RetryableError struct {
	Err error
	Msg string
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %s - %v", e.Msg, e.Err)
}

// ConflictError signals resource is in transition
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

// NewService returns a new MongoDB client with default timeout (30s).
func NewService(creds Credentials) Service {
	return NewServiceWithTimeout(creds, 30*time.Second)
}

// NewServiceWithTimeout returns a new MongoDB client with the provided HTTP timeout.
func NewServiceWithTimeout(creds Credentials, timeout time.Duration) Service {
	transport := &digest.Transport{Username: creds.PublicKey, Password: creds.PrivateKey}
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
	return &client{
		httpClient:  httpClient,
		baseURL:     "https://cloud.mongodb.com/api/atlas/v1.0",
		credentials: creds,
	}
}

// NewLongTimeoutService returns a client with a longer timeout suitable for slow delete operations.
func NewLongTimeoutService(creds Credentials) Service {
	transport := &digest.Transport{Username: creds.PublicKey, Password: creds.PrivateKey}
	httpClient := &http.Client{
		Timeout:   120 * time.Second,
		Transport: transport,
	}
	return &client{
		httpClient:  httpClient,
		baseURL:     "https://cloud.mongodb.com/api/atlas/v1.0",
		credentials: creds,
	}
}

// ErrNotFound standard error for missing resources.
var ErrNotFound = errors.New("not found")

func IsNotFoundError(err error) bool {
	_, ok := err.(*NotFoundError)
	return ok
}

func IsRetryableError(err error) bool {
	_, ok := err.(*RetryableError)
	return ok
}

func IsConflictError(err error) bool {
	_, ok := err.(*ConflictError)
	return ok
}

// CreateOrganization creates a new organization and returns API keys.
func (c *client) CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, APIKeyPair, error) {
	if input.Name == "" {
		return nil, APIKeyPair{}, errors.New("organization name cannot be empty")
	}
	if input.OwnerID == "" {
		return nil, APIKeyPair{}, errors.New("organization ownerID cannot be empty")
	}

	payload := map[string]interface{}{
		"name":       input.Name,
		"orgOwnerId": input.OwnerID,
		"apiKey":     input.APIKey,
	}

	resp := struct {
		Organization struct {
			ID        string `json:"id"`
			IsDeleted bool   `json:"isDeleted"`
			Name      string `json:"name"`
		} `json:"organization"`
		APIKey struct {
			PublicKey  string `json:"publicKey"`
			PrivateKey string `json:"privateKey"`
		} `json:"apiKey"`
	}{}

	if err := c.makeRequest(ctx, http.MethodPost, "/orgs", payload, &resp); err != nil {
		return nil, APIKeyPair{}, errors.Wrap(err, "cannot create organization")
	}

	org := &Organization{
		ID:         resp.Organization.ID,
		Name:       resp.Organization.Name,
		OrgOwnerId: input.OwnerID,
		IsDeleted:  resp.Organization.IsDeleted,
	}

	keys := APIKeyPair{
		PublicKey:  resp.APIKey.PublicKey,
		PrivateKey: resp.APIKey.PrivateKey,
	}

	return org, keys, nil
}

// GetOrganization retrieves org by ID.
func (c *client) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	org := &Organization{}
	if err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s", id), nil, org); err != nil {
		return nil, err
	}
	return org, nil
}

// GetOrganizationByName retrieves org by name.
func (c *client) GetOrganizationByName(ctx context.Context, name string) (*Organization, error) {
	if name == "" {
		return nil, errors.New("organization name cannot be empty")
	}

	var result struct {
		Results []Organization `json:"results"`
	}
	if err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs?name=%s", name), nil, &result); err != nil {
		if IsNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}

	if len(result.Results) == 0 {
		return nil, nil
	}

	return &result.Results[0], nil
}

// GetOrganizationByID wrapper for GetOrganization
func (c *client) GetOrganizationByID(ctx context.Context, id string) (*Organization, error) {
	return c.GetOrganization(ctx, id)
}

// UpdateOrganization updates org.
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

// DeleteOrganization deletes org by ID.
func (c *client) DeleteOrganization(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("organization id cannot be empty")
	}
	return c.makeRequest(ctx, http.MethodDelete, fmt.Sprintf("/orgs/%s", id), nil, nil)
}

// VerifyOrganizationDeletion verifies org deletion.
func (c *client) VerifyOrganizationDeletion(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("organization id cannot be empty")
	}

	org := &Organization{}
	err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s", id), nil, org)

	if IsNotFoundError(err) {
		return nil
	}

	if err != nil {
		return errors.Wrap(err, "failed to verify organization deletion")
	}

	return errors.Errorf("organization %s still exists", id)
}

// makeRequest performs HTTP request with digest auth and error handling.
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
		// treat context cancellations and timeouts as retryable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "timeout") {
			return &RetryableError{Err: err, Msg: "HTTP request failed (network error)"}
		}
		return &RetryableError{Err: err, Msg: "HTTP request failed (network error)"}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		var apiErr Error
		_ = json.Unmarshal(raw, &apiErr)

		if resp.StatusCode == 404 {
			return &NotFoundError{Err: apiErr}
		}
		if resp.StatusCode == 409 {
			return &ConflictError{Err: apiErr, Msg: "resource in conflict state"}
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			// server errors and rate limits are retryable
			return &RetryableError{Err: apiErr, Msg: "retryable HTTP error"}
		}
		return apiErr
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return errors.Wrap(err, "decode response")
		}
	}

	return nil
}

