package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sunboyzoey/GPT-BPS/internal/auth"
	"github.com/sunboyzoey/GPT-BPS/internal/basispoints"
)

// Explicit opt-in only: ordinary tests never access real credentials or BPS.
// Reuses the bridge client and headers, with no retries or captured credentials.
func TestLiveBPSAccessDiagnostic(t *testing.T) {
	if os.Getenv("BPS_LIVE_DIAGNOSTIC") != "1" {
		t.Skip("live diagnostic disabled")
	}
	store, err := auth.Load("")
	if err != nil {
		t.Fatal("could not load local credentials")
	}
	s, err := New(Config{ProxyURL: os.Getenv("BPS_LOCAL_PROXY")}, store)
	if err != nil {
		t.Fatal("could not initialize bridge client")
	}
	defer s.client.CloseIdleConnections()
	if os.Getenv("BPS_DIAGNOSTIC_COOKIE_JAR") == "1" {
		// Fresh, in-memory cookies issued to this client only. No HAR/browser imports.
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal("could not initialize cookie jar")
		}
		s.client.Jar = jar
	}
	token, account, err := store.Credentials()
	if err != nil {
		t.Fatal("local credentials unavailable")
	}
	if account == "" {
		account = auth.AccountIDFromToken(token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://bps.openai.com/basispoints/api/responses/access?include_models=true", nil)
	applyUpstreamHeaders(req, token, account)
	req.Header.Del("Content-Type")
	req.Header.Set("Accept", "application/json")
	for k, v := range excelClientInfo() {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal("access transport failed (details suppressed to protect credentials)")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close() // Finish preflight before reusing the client.
	if err != nil {
		t.Fatal("could not read access response")
	}
	var doc map[string]json.RawMessage
	parseErr := json.Unmarshal(raw, &doc)
	out := map[string]any{"status": resp.StatusCode, "http_protocol": resp.Proto, "valid_json": parseErr == nil, "cf_ray": resp.Header.Get("CF-Ray"), "request_id": resp.Header.Get("X-Request-ID")}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out["response_keys"] = keys
	for _, k := range []string{"allowed", "denial_reason", "auth_mode", "plan_type", "error_code", "error_name", "error_category", "cloudflare_error", "retryable", "owner_action_required"} {
		if v, ok := doc[k]; ok {
			out[k] = v
		}
	}
	out["set_cookie_count"] = len(resp.Cookies())
	out["cookie_jar_enabled"] = s.client.Jar != nil
	if os.Getenv("BPS_DIAGNOSTIC_GENERATE") == "1" {
		var allowed bool
		_ = json.Unmarshal(doc["allowed"], &allowed)
		if resp.StatusCode == http.StatusOK && allowed {
			out["generation"] = diagnoseGeneration(t, s, token, account)
		} else {
			out["generation_skipped"] = "access check did not allow generation"
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("BPS_DIAGNOSTIC_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal("could not save sanitized report")
		}
	}
	// A policy rejection is diagnostic data, not a transport test failure.
}

func diagnoseGeneration(t *testing.T, s *Server, token, account string) map[string]any {
	t.Helper()
	body, _, err := basispoints.Prepare([]byte(`{"model":"gpt-6-astra","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}`), "local", &basispoints.ReplayCache{})
	if err != nil {
		t.Fatal("could not prepare diagnostic request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := s.openUpstream(ctx, body, token, account)
	if err != nil {
		return map[string]any{"transport_failed": true, "error": err.Error()}
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return map[string]any{"read_failed": true}
	}
	events := []string{}
	codes := []string{}
	completed := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		events = append(events, event.Type)
		if event.Code != "" {
			codes = append(codes, event.Code)
		}
		completed = completed || event.Type == "response.completed"
	}
	return map[string]any{"events": events, "error_codes": codes, "completed": completed}
}

func TestLiveARCTextSession(t *testing.T) {
	if os.Getenv("BPS_LIVE_DIAGNOSTIC") != "1" {
		t.Skip("live diagnostic disabled")
	}
	store, err := auth.Load("")
	if err != nil {
		t.Fatal("local credentials unavailable")
	}
	s, err := New(Config{ProxyURL: os.Getenv("BPS_LOCAL_PROXY"), ARCTextSession: true, Verbose: true}, store)
	if err != nil {
		t.Fatal("could not initialize bridge")
	}
	defer s.client.CloseIdleConnections()
	token, account, err := store.Credentials()
	if err != nil {
		t.Fatal("credentials unavailable")
	}
	if account == "" {
		account = auth.AccountIDFromToken(token)
	}
	out := diagnoseGeneration(t, s, token, account)
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("BPS_DIAGNOSTIC_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal("could not save sanitized report")
		}
	}
}

// Explicit credential source for controlled A/B comparisons. No refresh or
// credential writes occur, and the normal request/transport remain unchanged.
func TestLiveBPSCredentialComparison(t *testing.T) {
	if os.Getenv("BPS_LIVE_DIAGNOSTIC") != "1" {
		t.Skip("live diagnostic disabled")
	}
	path := os.Getenv("BPS_DIAGNOSTIC_AUTH_PATH")
	if path == "" {
		t.Skip("explicit credential path required")
	}
	store, err := auth.Load(path)
	if err != nil {
		t.Fatal("diagnostic credentials unavailable")
	}
	s, err := New(Config{ProxyURL: os.Getenv("BPS_LOCAL_PROXY"), Verbose: true}, store)
	if err != nil {
		t.Fatal("could not initialize bridge")
	}
	defer s.client.CloseIdleConnections()
	token, account, err := store.Credentials()
	if err != nil {
		t.Fatal("credentials unavailable")
	}
	if account == "" {
		account = auth.AccountIDFromToken(token)
	}
	out := diagnoseGeneration(t, s, token, account)
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("BPS_DIAGNOSTIC_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal("could not save sanitized report")
		}
	}
}

// Test-only adapter: preserve the underlying transport and all other headers.
type wsAuthorizationDiagnostic struct {
	base           http.RoundTripper
	removed        bool
	removeAccounts bool
}

func (d *wsAuthorizationDiagnostic) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		r = r.Clone(r.Context())
		d.removed = r.Header.Get("Authorization") != ""
		r.Header.Del("Authorization")
		if d.removeAccounts {
			r.Header.Del("Chatgpt-Account-Id")
			r.Header.Del("X-Openai-Account-Id")
		}
	}
	return d.base.RoundTrip(r)
}
func (d *wsAuthorizationDiagnostic) CloseIdleConnections() {
	if c, ok := d.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func TestLiveWSSubprotocolAuthorization(t *testing.T) {
	if os.Getenv("BPS_LIVE_DIAGNOSTIC") != "1" {
		t.Skip("live diagnostic disabled")
	}
	store, err := auth.Load("")
	if err != nil {
		t.Fatal("local credentials unavailable")
	}
	s, err := New(Config{ProxyURL: os.Getenv("BPS_LOCAL_PROXY"), Verbose: true}, store)
	if err != nil {
		t.Fatal("could not initialize bridge")
	}
	adapter := &wsAuthorizationDiagnostic{base: s.client.Transport, removeAccounts: os.Getenv("BPS_DIAGNOSTIC_NO_ACCOUNT_HEADERS") == "1"}
	s.client.Transport = adapter
	defer s.client.CloseIdleConnections()
	token, account, err := store.Credentials()
	if err != nil {
		t.Fatal("credentials unavailable")
	}
	if account == "" {
		account = auth.AccountIDFromToken(token)
	}
	out := diagnoseGeneration(t, s, token, account)
	out["authorization_header_removed"] = adapter.removed
	out["change"] = "remove only Authorization from WS handshake; bearer subprotocol and account headers unchanged"
	if adapter.removeAccounts {
		out["change"] = "remove Authorization and both account selection headers; same bearer subprotocol retained"
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("BPS_DIAGNOSTIC_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal("could not save sanitized report")
		}
	}
}

// Checks only metadata ID representation; no captured IDs or auth are reused.
func TestLiveBPSMetadataIDFormat(t *testing.T) {
	if os.Getenv("BPS_LIVE_DIAGNOSTIC") != "1" {
		t.Skip("live diagnostic disabled")
	}
	store, err := auth.Load("")
	if err != nil {
		t.Fatal("local credentials unavailable")
	}
	s, err := New(Config{ProxyURL: os.Getenv("BPS_LOCAL_PROXY"), Verbose: true}, store)
	if err != nil {
		t.Fatal("could not initialize bridge")
	}
	defer s.client.CloseIdleConnections()
	body, _, err := basispoints.Prepare([]byte(`{"model":"gpt-6-astra","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}`), "local", &basispoints.ReplayCache{})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(body, &doc)
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(doc["metadata"], &metadata)
	// Preserve all 32 hex digits, adding only UUID-style separators.
	for _, key := range []string{"task_id", "turn_id"} {
		var id string
		_ = json.Unmarshal(metadata[key], &id)
		if len(id) != 32 {
			t.Fatal("unexpected generated ID shape")
		}
		metadata[key], _ = json.Marshal(id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:])
	}
	doc["metadata"], _ = json.Marshal(metadata)
	body, _ = json.Marshal(doc)
	token, account, err := store.Credentials()
	if err != nil {
		t.Fatal("credentials unavailable")
	}
	if account == "" {
		account = auth.AccountIDFromToken(token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := s.openUpstream(ctx, body, token, account)
	if err != nil {
		t.Fatal("upstream transport failed")
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		t.Fatal("could not read upstream response")
	}
	events := []string{}
	codes := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var d struct {
			Type string `json:"type"`
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d) == nil {
			events = append(events, d.Type)
			if d.Code != "" {
				codes = append(codes, d.Code)
			}
		}
	}
	b, _ := json.MarshalIndent(map[string]any{"change": "task_id and turn_id separators only; original 32 hex digits preserved", "events": events, "error_codes": codes}, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("BPS_DIAGNOSTIC_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal("could not save sanitized report")
		}
	}
}

// A single-variable comparison against the already captured bridge baseline.
func TestLiveBPSResponsesMetadata(t *testing.T) {
	if os.Getenv("BPS_LIVE_DIAGNOSTIC") != "1" {
		t.Skip("live diagnostic disabled")
	}
	version := os.Getenv("BPS_DIAGNOSTIC_TOOLS_VERSION")
	if version == "" {
		t.Skip("explicit tool version required")
	}
	store, err := auth.Load("")
	if err != nil {
		t.Fatal("could not load local credentials")
	}
	transport := os.Getenv("BPS_DIAGNOSTIC_TRANSPORT")
	if transport == "" {
		transport = "ws"
	}
	s, err := New(Config{ProxyURL: os.Getenv("BPS_LOCAL_PROXY"), ToolsVersion: version, Verbose: true, UpstreamTransport: transport}, store)
	if err != nil {
		t.Fatal("could not initialize bridge")
	}
	defer s.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	events := []string{}
	codes := []string{}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var d struct {
			Type string `json:"type"`
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d) == nil {
			events = append(events, d.Type)
			if d.Code != "" {
				codes = append(codes, d.Code)
			}
		}
	}
	var localError struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &localError)
	out := map[string]any{"local_http_status": rec.Code, "transport": transport, "tools_version": version, "events": events, "error_codes": codes, "local_error_code": localError.Error.Code, "local_error_message": localError.Error.Message}
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Log(string(b))
	if path := os.Getenv("BPS_DIAGNOSTIC_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal("could not save sanitized report")
		}
	}
}
