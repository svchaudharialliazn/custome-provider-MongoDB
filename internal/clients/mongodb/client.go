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

	// Legacy org-level access list (not used for API-key provisioning now)
	AddIPToAccessList(ctx context.Context, orgID string, input AddIPInput) error
	RemoveIPFromAccessList(ctx context.Context, orgID string, ip string) error
	GetIPAccessList(ctx context.Context, orgID string) ([]IPAccessListEntry, error)

	// Org API key–scoped IP access list
	FindAPIKeyID(ctx context.Context, orgID, publicKey, description string) (string, error)
	AddIPsToAPIKeyAccessList(ctx context.Context, orgID, apiKeyID string, inputs []AddIPInput) error
	RemoveIPFromAPIKeyAccessList(ctx context.Context, orgID, apiKeyID, ip string) error
}

type Credentials struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

type Organization struct {
	ID         string    `json:"id,omitempty"`
	Name       string    `json:"name"`
	OrgOwnerId string    `json:"orgOwnerId"`
	IsDeleted  bool      `json:"isDeleted"`
	Created    time.Time `json:"created,omitempty"`
}

type APIKey struct {
	Description string   `json:"desc"`
	Roles       []string `json:"roles"`
}

type CreateOrganizationInput struct {
	Name    string `json:"name"`
	OwnerID string `json:"orgOwnerId"`
	APIKey  APIKey `json:"apiKey"`
}

type UpdateOrganizationInput struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type APIKeyPair struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

type Error struct {
	Code   int    `json:"error"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`
}

func (e Error) Error() string {
	return fmt.Sprintf("MongoDB Atlas API error %d: %s - %s", e.Code, e.Reason, e.Detail)
}

type NotFoundError struct{ Err Error }

func (e *NotFoundError) Error() string { return e.Err.Error() }

type RetryableError struct {
	Err error
	Msg string
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %s - %v", e.Msg, e.Err)
}

type ConflictError struct {
	Err error
	Msg string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("conflict error (resource in transition): %s - %v", e.Msg, e.Err)
}

type client struct {
	httpClient  *http.Client
	baseURL     string
	credentials Credentials
}

func NewService(creds Credentials) Service {
	return NewServiceWithTimeout(creds, 30*time.Second)
}

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

func (c *client) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	org := &Organization{}
	if err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s", id), nil, org); err != nil {
		return nil, err
	}
	return org, nil
}

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

func (c *client) GetOrganizationByID(ctx context.Context, id string) (*Organization, error) {
	return c.GetOrganization(ctx, id)
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

func (c *client) DeleteOrganization(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("organization id cannot be empty")
	}
	return c.makeRequest(ctx, http.MethodDelete, fmt.Sprintf("/orgs/%s", id), nil, nil)
}

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

// IP access types
type AddIPInput struct {
	IP      string  `json:"ip"`
	Comment *string `json:"comment,omitempty"`
}

type IPAccessListEntry struct {
	IPAddress string    `json:"ipAddress"`
	CIDR      string    `json:"cidr"`
	Comment   string    `json:"comment"`
	Created   time.Time `json:"created"`
}

// Legacy org-level IP access implementations
func (c *client) AddIPToAccessList(ctx context.Context, orgID string, input AddIPInput) error {
	if orgID == "" {
		return errors.New("organization ID cannot be empty")
	}
	if input.IP == "" {
		return errors.New("IP address cannot be empty")
	}

	payload := map[string]interface{}{
		"ipAddress": input.IP,
	}
	if input.Comment != nil && *input.Comment != "" {
		payload["comment"] = *input.Comment
	}

	endpoint := fmt.Sprintf("/orgs/%s/accessList", orgID)
	return c.makeRequest(ctx, http.MethodPost, endpoint, payload, nil)
}

func (c *client) RemoveIPFromAccessList(ctx context.Context, orgID string, ip string) error {
	if orgID == "" {
		return errors.New("organization ID cannot be empty")
	}
	if ip == "" {
		return errors.New("IP address cannot be empty")
	}

	endpoint := fmt.Sprintf("/orgs/%s/accessList/%s", orgID, ip)
	return c.makeRequest(ctx, http.MethodDelete, endpoint, nil, nil)
}

func (c *client) GetIPAccessList(ctx context.Context, orgID string) ([]IPAccessListEntry, error) {
	if orgID == "" {
		return nil, errors.New("organization ID cannot be empty")
	}

	var result struct {
		Results []IPAccessListEntry `json:"results"`
		Links   []interface{}       `json:"links"`
	}

	endpoint := fmt.Sprintf("/orgs/%s/accessList", orgID)
	if err := c.makeRequest(ctx, http.MethodGet, endpoint, nil, &result); err != nil {
		if IsNotFoundError(err) {
			return []IPAccessListEntry{}, nil
		}
		return nil, errors.Wrap(err, "failed to get IP access list")
	}

	return result.Results, nil
}
