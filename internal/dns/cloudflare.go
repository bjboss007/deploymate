package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// apiBaseURL is the Cloudflare v4 API root.
const apiBaseURL = "https://api.cloudflare.com/client/v4"

// errCodeAlreadyExists is Cloudflare's "record already exists" error
// code; creating an existing record is the goal state reached.
const errCodeAlreadyExists = 81053

// Cloudflare implements Creator against the Cloudflare DNS API. The
// token needs Zone.DNS:Edit permission on the zone.
type Cloudflare struct {
	token    string
	zoneID   string
	tunnelID string
	baseURL  string
	client   *http.Client
}

// NewCloudflare builds a Cloudflare creator for a named tunnel. Callers
// gate construction on config.CloudflareEnabled().
func NewCloudflare(token, zoneID, tunnelID string) *Cloudflare {
	return newCloudflare(apiBaseURL, token, zoneID, tunnelID, &http.Client{Timeout: 5 * time.Second})
}

// Cloudflare implements both sides of auto-DNS — create and remove.
var _ Manager = (*Cloudflare)(nil)

// newCloudflare is the test seam: baseURL and client are injectable.
func newCloudflare(baseURL, token, zoneID, tunnelID string, client *http.Client) *Cloudflare {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Cloudflare{token: token, zoneID: zoneID, tunnelID: tunnelID, baseURL: baseURL, client: client}
}

type dnsRecord struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// dnsRecordSummary is the subset of a listed record RemovePreviewRecord
// needs: its id, for the DELETE call.
type dnsRecordSummary struct {
	ID string `json:"id"`
}

// apiResponse is the Cloudflare v4 envelope: API-level errors arrive as
// HTTP 200 with success:false, not as HTTP 4xx. Result is left raw because
// it is an array for LIST responses, a single object for mutation
// responses, and empty for some deletes — only recordIDs decodes it.
type apiResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// EnsurePreviewRecord creates the tunnel CNAME for host, proxied so
// Cloudflare issues an edge cert. Already-exists (81053) is success.
func (c *Cloudflare) EnsurePreviewRecord(ctx context.Context, host string) error {
	payload, err := json.Marshal(dnsRecord{
		Type:    "CNAME",
		Name:    host,
		Content: c.tunnelID + ".cfargotunnel.com",
		Proxied: true,
	})
	if err != nil {
		return fmt.Errorf("cloudflare dns: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/zones/"+c.zoneID+"/dns_records", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cloudflare dns: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare dns: create %s: %w", host, err)
	}
	defer resp.Body.Close()

	var api apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&api); err != nil {
		return fmt.Errorf("cloudflare dns: decode response (%d): %w", resp.StatusCode, err)
	}
	if api.Success {
		return nil
	}
	for _, e := range api.Errors {
		if e.Code == errCodeAlreadyExists {
			return nil // record already exists — goal state reached
		}
	}
	if len(api.Errors) > 0 {
		e := api.Errors[0]
		return fmt.Errorf("cloudflare dns: create %s: code %d: %s", host, e.Code, e.Message)
	}
	return fmt.Errorf("cloudflare dns: create %s: HTTP %d, success=false", host, resp.StatusCode)
}

// RemovePreviewRecord deletes every DNS record at name host — the auto-DNS
// CNAME plus any duplicates (human or from a botched earlier cleanup).
// Idempotent: no matching record is the goal state reached, as is a 404
// racing a concurrent delete.
func (c *Cloudflare) RemovePreviewRecord(ctx context.Context, host string) error {
	ids, err := c.recordIDs(ctx, host)
	if err != nil {
		return err
	}
	for _, id := range ids {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
			c.baseURL+"/zones/"+c.zoneID+"/dns_records/"+id, nil)
		if err != nil {
			return fmt.Errorf("cloudflare dns: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)

		resp, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("cloudflare dns: delete %s: %w", host, err)
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil // already gone (raced the delete) — goal state reached
		}
		var api apiResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&api)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("cloudflare dns: decode response (%d): %w", resp.StatusCode, decodeErr)
		}
		if api.Success {
			continue
		}
		if len(api.Errors) > 0 {
			e := api.Errors[0]
			return fmt.Errorf("cloudflare dns: delete %s: code %d: %s", host, e.Code, e.Message)
		}
		return fmt.Errorf("cloudflare dns: delete %s: HTTP %d, success=false", host, resp.StatusCode)
	}
	return nil
}

// recordIDs lists the ids of every record named host (any type) so
// RemovePreviewRecord can delete them.
func (c *Cloudflare) recordIDs(ctx context.Context, host string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/zones/"+c.zoneID+"/dns_records?name="+url.QueryEscape(host), nil)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns: list %s: %w", host, err)
	}
	defer resp.Body.Close()

	var api apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&api); err != nil {
		return nil, fmt.Errorf("cloudflare dns: decode response (%d): %w", resp.StatusCode, err)
	}
	if api.Success {
		var records []dnsRecordSummary
		if len(api.Result) > 0 {
			if err := json.Unmarshal(api.Result, &records); err != nil {
				return nil, fmt.Errorf("cloudflare dns: decode result: %w", err)
			}
		}
		ids := make([]string, 0, len(records))
		for _, rec := range records {
			ids = append(ids, rec.ID)
		}
		return ids, nil
	}
	if len(api.Errors) > 0 {
		e := api.Errors[0]
		return nil, fmt.Errorf("cloudflare dns: list %s: code %d: %s", host, e.Code, e.Message)
	}
	return nil, fmt.Errorf("cloudflare dns: list %s: HTTP %d, success=false", host, resp.StatusCode)
}
