// internal/clients/mongodb/service_api_key_access.go
// Org API key–scoped access list helpers.
package mongodb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// FindAPIKeyID lists org API keys and returns the ID of the key that matches the given publicKey.
// If publicKey is empty, this returns "" without error.
func (c *client) FindAPIKeyID(ctx context.Context, orgID, publicKey, _ string) (string, error) {
	if orgID == "" || publicKey == "" {
		return "", nil
	}
	type apiKeyMeta struct {
		ID        string `json:"id"`
		PublicKey string `json:"publicKey"`
	}
	var resp struct {
		Results []apiKeyMeta `json:"results"`
	}
	endpoint := fmt.Sprintf("/orgs/%s/apiKeys", url.PathEscape(orgID))
	if err := c.makeRequest(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return "", err
	}
	for _, k := range resp.Results {
		if k.PublicKey == publicKey {
			return k.ID, nil
		}
	}
	return "", nil
}

// AddIPsToAPIKeyAccessList bulk adds IPs to the org API key's access list in a single request.
// IMPORTANT: For API-key access list, omit comment to avoid 400 from Atlas.
func (c *client) AddIPsToAPIKeyAccessList(ctx context.Context, orgID, apiKeyID string, inputs []AddIPInput) error {
	if orgID == "" || apiKeyID == "" {
		return fmt.Errorf("orgID and apiKeyID cannot be empty")
	}
	endpoint := fmt.Sprintf("/orgs/%s/apiKeys/%s/accessList", url.PathEscape(orgID), url.PathEscape(apiKeyID))
	payload := make([]map[string]any, 0, len(inputs))
	for _, in := range inputs {
		payload = append(payload, map[string]any{"ipAddress": in.IP})
	}
	return c.makeRequest(ctx, http.MethodPost, endpoint, payload, nil)
}

// RemoveIPFromAPIKeyAccessList removes an IP from the org API key's access list.
func (c *client) RemoveIPFromAPIKeyAccessList(ctx context.Context, orgID, apiKeyID, ip string) error {
	if orgID == "" || apiKeyID == "" || ip == "" {
		return fmt.Errorf("orgID, apiKeyID and ip cannot be empty")
	}
	endpoint := fmt.Sprintf("/orgs/%s/apiKeys/%s/accessList/%s",
		url.PathEscape(orgID), url.PathEscape(apiKeyID), url.PathEscape(ip))
	return c.makeRequest(ctx, http.MethodDelete, endpoint, nil, nil)
}
