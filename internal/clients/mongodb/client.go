package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	// Legacy org-level access list (kept for compatibility)
	AddIPToAccessList(ctx context.Context, orgID string, input AddIPInput) error
	RemoveIPFromAccessList(ctx context.Context, orgID string, ip string) error
	GetIPAccessList(ctx context.Context, orgID string) ([]IPAccessListEntry, error)

	// Org API key–scoped IP access list (preferred for this provider)
	FindAPIKeyID(ctx context.Context, orgID, publicKey, description string) (string, error)
	AddIPsToAPIKeyAccessList(ctx context.Context, orgID, apiKeyID string, inputs []AddIPInput) error
	RemoveIPFromAPIKeyAccessList(ctx context.Context, orgID, apiKeyID, ip string) error

	// Org-level Admin API IP enforcement (v2 endpoint)
	SetOrgAdminAPIIPEnforcement(ctx context.Context, orgID string, required bool) error
	GetOrgAdminAPIIPEnforcement(ctx context.Context, orgID string) (bool, error)

	// Organization settings (v2 endpoint) - full settings management
	GetOrganizationSettings(ctx context.Context, orgID string) (*OrganizationSettings, error)
	UpdateOrganizationSettings(ctx context.Context, orgID string, settings OrganizationSettingsUpdate) (*OrganizationSettings, error)
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

// OrganizationSettings represents the settings returned by GET /orgs/{orgId}/settings
type OrganizationSettings struct {
	APIAccessListRequired   bool `json:"apiAccessListRequired"`
	GenAIFeaturesEnabled    bool `json:"genAIFeaturesEnabled"`
	MultiFactorAuthRequired bool `json:"multiFactorAuthRequired"`
	RestrictEmployeeAccess  bool `json:"restrictEmployeeAccess"`
}

// OrganizationSettingsUpdate represents the payload for PATCH /orgs/{orgId}/settings
type OrganizationSettingsUpdate struct {
	APIAccessListRequired   *bool `json:"apiAccessListRequired,omitempty"`
	GenAIFeaturesEnabled    *bool `json:"genAIFeaturesEnabled,omitempty"`
	MultiFactorAuthRequired *bool `json:"multiFactorAuthRequired,omitempty"`
	RestrictEmployeeAccess  *bool `json:"restrictEmployeeAccess,omitempty"`
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

type UnauthorizedError struct{ Err Error }

func (e *UnauthorizedError) Error() string { return e.Err.Error() }

type RetryableError struct {
	Err error
	Msg string
}

func (e *RetryableError) Error() string { return fmt.Sprintf("retryable error: %s - %v", e.Msg, e.Err) }

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

const (
	atlasV1Base            = "https://cloud.mongodb.com/api/atlas/v1.0"
	atlasV2Base            = "https://cloud.mongodb.com/api/atlas/v2"
	atlasV2SettingsAccept  = "application/vnd.atlas.2025-03-12+json"
	contentTypeApplication = "application/json"
)

func NewService(creds Credentials) Service {
	return NewServiceWithTimeout(creds, 30*time.Second)
}

func NewServiceWithTimeout(creds Credentials, timeout time.Duration) Service {
	transport := &digest.Transport{Username: creds.PublicKey, Password: creds.PrivateKey}
	httpClient := &http.Client{Timeout: timeout, Transport: transport}
	return &client{httpClient: httpClient, baseURL: atlasV1Base, credentials: creds}
}

func NewLongTimeoutService(creds Credentials) Service {
	transport := &digest.Transport{Username: creds.PublicKey, Password: creds.PrivateKey}
	httpClient := &http.Client{Timeout: 120 * time.Second, Transport: transport}
	return &client{httpClient: httpClient, baseURL: atlasV1Base, credentials: creds}
}

var ErrNotFound = errors.New("not found")

func IsNotFoundError(err error) bool     { _, ok := err.(*NotFoundError); return ok }
func IsUnauthorizedError(err error) bool { _, ok := err.(*UnauthorizedError); return ok }
func IsRetryableError(err error) bool    { _, ok := err.(*RetryableError); return ok }
func IsConflictError(err error) bool     { _, ok := err.(*ConflictError); return ok }

func (c *client) CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, APIKeyPair, error) {
	if input.Name == "" {
		return nil, APIKeyPair{}, errors.New("organization name cannot be empty")
	}
	if input.OwnerID == "" {
		return nil, APIKeyPair{}, errors.New("organization ownerID cannot be empty")
	}

	payload := map[string]any{
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
	keys := APIKeyPair{PublicKey: resp.APIKey.PublicKey, PrivateKey: resp.APIKey.PrivateKey}
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
	q := url.QueryEscape(name)
	if err := c.makeRequest(ctx, http.MethodGet, fmt.Sprintf("/orgs?name=%s", q), nil, &result); err != nil {
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
	payload := map[string]any{}
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

// v1 helper
func (c *client) makeRequest(ctx context.Context, method, endpoint string, payload, result any) error {
	url := c.baseURL + endpoint
	return c.doRequest(ctx, url, method, payload, result, contentTypeApplication, "application/json")
}

// v2 helper (needed for org settings)
func (c *client) makeV2Request(ctx context.Context, method, endpoint string, payload, result any, accept string) error {
	url := atlasV2Base + endpoint
	if accept == "" {
		accept = "application/json"
	}
	return c.doRequest(ctx, url, method, payload, result, contentTypeApplication, accept)
}

// shared HTTP execution with digest auth
func (c *client) doRequest(ctx context.Context, url, method string, payload, result any, contentType, accept string) error {
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
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", accept)

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
		if apiErr.Code == 0 {
			apiErr.Code = resp.StatusCode
		}
		switch resp.StatusCode {
		case 404:
			return &NotFoundError{Err: apiErr}
		case 409:
			return &ConflictError{Err: apiErr, Msg: "resource in conflict state"}
		case 401, 403:
			return &UnauthorizedError{Err: apiErr}
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

// Legacy org-level IP access implementations (kept for compatibility)
func (c *client) AddIPToAccessList(ctx context.Context, orgID string, input AddIPInput) error {
	if orgID == "" {
		return errors.New("organization ID cannot be empty")
	}
	if input.IP == "" {
		return errors.New("IP address cannot be empty")
	}
	payload := map[string]any{"ipAddress": input.IP}
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
		Links   []any               `json:"links"`
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

// Org-level Admin API IP enforcement (v2 endpoint)
func (c *client) SetOrgAdminAPIIPEnforcement(ctx context.Context, orgID string, required bool) error {
	if orgID == "" {
		return errors.New("organization id cannot be empty")
	}
	payload := map[string]any{"apiAccessListRequired": required}
	endpoint := fmt.Sprintf("/orgs/%s/settings", url.PathEscape(orgID))
	return c.makeV2Request(ctx, http.MethodPatch, endpoint, payload, nil, atlasV2SettingsAccept)
}

func (c *client) GetOrgAdminAPIIPEnforcement(ctx context.Context, orgID string) (bool, error) {
	if orgID == "" {
		return false, errors.New("organization id cannot be empty")
	}
	var resp struct {
		APIAccessListRequired bool `json:"apiAccessListRequired"`
	}
	endpoint := fmt.Sprintf("/orgs/%s/settings", url.PathEscape(orgID))
	if err := c.makeV2Request(ctx, http.MethodGet, endpoint, nil, &resp, atlasV2SettingsAccept); err != nil {
		if IsNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return resp.APIAccessListRequired, nil
}

// GetOrganizationSettings retrieves the full organization settings
func (c *client) GetOrganizationSettings(ctx context.Context, orgID string) (*OrganizationSettings, error) {
	if orgID == "" {
		return nil, errors.New("organization id cannot be empty")
	}
	var settings OrganizationSettings
	endpoint := fmt.Sprintf("/orgs/%s/settings", url.PathEscape(orgID))
	if err := c.makeV2Request(ctx, http.MethodGet, endpoint, nil, &settings, atlasV2SettingsAccept); err != nil {
		return nil, errors.Wrap(err, "failed to get organization settings")
	}
	return &settings, nil
}

// UpdateOrganizationSettings updates the organization settings
func (c *client) UpdateOrganizationSettings(ctx context.Context, orgID string, settings OrganizationSettingsUpdate) (*OrganizationSettings, error) {
	if orgID == "" {
		return nil, errors.New("organization id cannot be empty")
	}
	var result OrganizationSettings
	endpoint := fmt.Sprintf("/orgs/%s/settings", url.PathEscape(orgID))
	if err := c.makeV2Request(ctx, http.MethodPatch, endpoint, settings, &result, atlasV2SettingsAccept); err != nil {
		return nil, errors.Wrap(err, "failed to update organization settings")
	}
	return &result, nil
}
