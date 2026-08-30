package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testToken   = "test-token"
	testZone    = "zone-123"
	testTunnel  = "tunnel-abc"
	testHost    = "my-app.dm.getmerchanttech.com"
	testContent = "tunnel-abc.cfargotunnel.com"
)

func newTestCloudflare(srv *httptest.Server, client *http.Client) *Cloudflare {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return newCloudflare(srv.URL, testToken, testZone, testTunnel, client)
}

func TestEnsurePreviewRecordCreatesCNAME(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotCT string
	var gotBody dnsRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotCT = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	err := newTestCloudflare(srv, nil).EnsurePreviewRecord(context.Background(), testHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/zones/"+testZone+"/dns_records" {
		t.Errorf("path = %s, want /zones/%s/dns_records", gotPath, testZone)
	}
	if gotAuth != "Bearer "+testToken {
		t.Errorf("auth = %q, want Bearer %s", gotAuth, testToken)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if gotBody.Type != "CNAME" || gotBody.Name != testHost || gotBody.Content != testContent || !gotBody.Proxied {
		t.Errorf("body = %+v, want CNAME %s -> %s proxied", gotBody, testHost, testContent)
	}
}

func TestEnsurePreviewRecordAlreadyExistsIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":false,"errors":[{"code":81053,"message":"record already exists"}]}`))
	}))
	defer srv.Close()

	if err := newTestCloudflare(srv, nil).EnsurePreviewRecord(context.Background(), testHost); err != nil {
		t.Fatalf("81053 should be treated as success, got: %v", err)
	}
}

func TestEnsurePreviewRecordAPIErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"something bad"}]}`))
	}))
	defer srv.Close()

	err := newTestCloudflare(srv, nil).EnsurePreviewRecord(context.Background(), testHost)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "10000") || !strings.Contains(err.Error(), "something bad") {
		t.Errorf("error should carry code and message, got: %v", err)
	}
}

func TestEnsurePreviewRecordHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"invalid token"}]}`))
	}))
	defer srv.Close()

	if err := newTestCloudflare(srv, nil).EnsurePreviewRecord(context.Background(), testHost); err == nil {
		t.Fatal("expected error for revoked token, got nil")
	}
}

func TestEnsurePreviewRecordTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	if err := newTestCloudflare(srv, client).EnsurePreviewRecord(context.Background(), testHost); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestEnsurePreviewRecordGarbageResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	if err := newTestCloudflare(srv, nil).EnsurePreviewRecord(context.Background(), testHost); err == nil {
		t.Fatal("expected error on garbage response, got nil")
	}
}
