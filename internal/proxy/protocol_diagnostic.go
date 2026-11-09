package proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sunboyzoey/GPT-BPS/internal/auth"
)

type ProtocolDiagnosticOptions struct {
	Model           string
	ReasoningEffort string
}

// Fingerprints are keyed for one run only. Raw values, the key, credentials,
// response bodies, cookies and account identifiers never enter the report.
type ProtocolSignal struct {
	Source      string `json:"source"`
	Name        string `json:"name"`
	Bytes       int    `json:"bytes"`
	Fingerprint string `json:"fingerprint"`
}

type ProtocolDiagnosticShot struct {
	Mode                 string           `json:"mode"`
	HandshakeStatus      int              `json:"handshake_status"`
	LocalStatus          int              `json:"local_status"`
	Completed            bool             `json:"completed"`
	TicketReplayed       bool             `json:"ticket_replayed"`
	RouteCookiesReplayed int              `json:"route_cookies_replayed"`
	HandshakeHeaderNames []string         `json:"handshake_header_names"`
	MetadataHeaderNames  []string         `json:"metadata_header_names"`
	EventCounts          map[string]int   `json:"event_counts"`
	Signals              []ProtocolSignal `json:"signals"`
	Failure              string           `json:"failure,omitempty"`
	DurationMS           int64            `json:"duration_ms"`
}

type ProtocolDiagnosticReport struct {
	Version           int                      `json:"version"`
	Transport         string                   `json:"transport"`
	Model             string                   `json:"model"`
	ReasoningEffort   string                   `json:"reasoning_effort"`
	QualityAssessment string                   `json:"quality_assessment"`
	Conclusion        string                   `json:"conclusion"`
	TicketComparison  string                   `json:"ticket_comparison"`
	Failure           string                   `json:"failure,omitempty"`
	StartedAt         time.Time                `json:"started_at"`
	FinishedAt        time.Time                `json:"finished_at"`
	Shots             []ProtocolDiagnosticShot `json:"shots"`
}

var diagnosticModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{0,99}$`)

func diagnosticKnownHeader(name string) bool {
	switch strings.ToLower(name) {
	case "x-codex-turn-state", "content-type", "content-length", "connection", "upgrade", "sec-websocket-accept", "sec-websocket-protocol", "server", "date", "vary", "cache-control", "strict-transport-security", "set-cookie", "x-request-id", "cf-ray", "cf-cache-status", "alt-svc", "openai-processing-ms",
		"x-openai-internal-basispoints-agent-prompt-sha256", "x-openai-internal-basispoints-agent-tools-sha256", "x-openai-internal-basispoints-prompt-version-id", "x-openai-internal-basispoints-tools-version-id":
		return true
	}
	return false
}

func diagnosticKnownEvent(kind string) bool {
	switch kind {
	case "error", "basispoints.response.resume_token", "basispoints.response.metadata", "basispoints.response.upstream_sent", "basispoints.response.heartbeat", "basispoints.response.server_draining",
		"response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete",
		"response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done", "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return true
	}
	return false
}

type diagnosticCredentials struct {
	token, account string
	expiry         time.Time
}

func (s diagnosticCredentials) Expiry() time.Time { return s.expiry }
func (s diagnosticCredentials) Credentials() (string, string, error) {
	if !s.expiry.After(time.Now()) {
		return "", "", auth.ErrExpired
	}
	return s.token, s.account, nil
}

// RunProtocolDiagnostic reuses the real BPS translation and WS path, without a
// listener, RT exchange, automatic retry, native fallback or account mutation.
// Its observations deliberately do not implement a healthy/degraded verdict.
func RunProtocolDiagnostic(ctx context.Context, cfg Config, store CredentialSource, options ProtocolDiagnosticOptions) (report ProtocolDiagnosticReport, err error) {
	report = ProtocolDiagnosticReport{Version: 1, Transport: "bps_websocket", QualityAssessment: "not_assessed", Conclusion: "not_started", TicketComparison: "not_available", StartedAt: time.Now().UTC(), Shots: []ProtocolDiagnosticShot{}}
	defer func() { report.FinishedAt = time.Now().UTC() }()
	fail := func(code string) (ProtocolDiagnosticReport, error) {
		report.Failure = code
		return report, errors.New(code)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return fail("diagnostic_cancelled")
	}
	options.Model, options.ReasoningEffort = strings.TrimSpace(options.Model), strings.TrimSpace(options.ReasoningEffort)
	if options.Model == "" {
		options.Model = "gpt-6-astra"
	}
	if options.ReasoningEffort == "" {
		options.ReasoningEffort = "medium"
	}
	if !diagnosticModel.MatchString(options.Model) {
		return fail("diagnostic_invalid_model")
	}
	switch options.ReasoningEffort {
	case "none", "low", "medium", "high", "xhigh":
	default:
		return fail("diagnostic_invalid_effort")
	}
	report.Model, report.ReasoningEffort = options.Model, options.ReasoningEffort
	u, proxyErr := url.Parse(strings.TrimSpace(cfg.ProxyURL))
	if proxyErr != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
		return fail("diagnostic_proxy_required")
	}
	if store == nil {
		return fail("diagnostic_credentials_required")
	}
	token, account, credErr := store.Credentials()
	if credErr != nil {
		if errors.Is(credErr, auth.ErrExpired) {
			return fail("diagnostic_credentials_expired")
		}
		return fail("diagnostic_credentials_unavailable")
	}
	fromToken := auth.AccountIDFromToken(token)
	if auth.InspectToken(token).ClientID != auth.ExcelClientID || fromToken == "" || (account != "" && account != fromToken) {
		return fail("diagnostic_bps_credentials_required")
	}
	expiry := store.Expiry()
	if expiry.IsZero() || !expiry.After(time.Now()) {
		return fail("diagnostic_credentials_expired_or_unknown")
	}
	// Fresh configuration and one credential snapshot make both shots comparable.
	// In particular, no existing ticket, browser cookie or ARC session is copied.
	cfg.Headers, cfg.Verbose, cfg.ARCTextSession = nil, false, false
	cfg.ProxyURL, cfg.UpstreamTransport = strings.TrimSpace(cfg.ProxyURL), "ws"
	server, initErr := New(cfg, diagnosticCredentials{token, fromToken, expiry})
	if initErr != nil {
		return fail("diagnostic_configuration_invalid")
	}
	defer server.client.CloseIdleConnections()
	var key [32]byte
	if _, keyErr := rand.Read(key[:]); keyErr != nil {
		return fail("diagnostic_random_unavailable")
	}
	payload, _ := json.Marshal(map[string]any{
		"model": options.Model, "instructions": "Reply with OK.",
		"input":  []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "Reply with OK."}}}},
		"stream": true, "store": false, "parallel_tool_calls": true,
		"include": []string{"reasoning.encrypted_content"}, "reasoning": map[string]string{"effort": options.ReasoningEffort},
	})
	var firstTicket string
	var firstAmbiguous bool
	var routeCookies []*http.Cookie
	for shot := 0; shot < 2; shot++ {
		shotCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		collector := &protocolCollector{key: key[:], cancel: cancel, counts: map[string]int{}, tickets: map[string]bool{}}
		mode, replayed, cookieCount := "baseline", false, 0
		selected := server
		if shot == 1 && firstTicket != "" && !firstAmbiguous {
			mode, replayed = "ticket_replay", true
			selected = server.WithHeaders(map[string]string{"X-Codex-Turn-State": firstTicket})
			jar, _ := cookiejar.New(nil)
			target, _ := url.Parse(server.cfg.Upstream)
			if target.Scheme == "wss" {
				target.Scheme = "https"
			}
			if target.Scheme == "ws" {
				target.Scheme = "http"
			}
			jar.SetCookies(target, routeCookies)
			cookieCount = len(jar.Cookies(target))
			copyClient := *server.client
			copyClient.Jar = jar
			selected.client = &copyClient
		}
		observed := withProtocolObserver(shotCtx, protocolObserver{OnHandshake: collector.handshake, OnEvent: collector.event})
		request, _ := http.NewRequestWithContext(observed, http.MethodPost, "http://127.0.0.1/v1/responses", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Session_id", "bps-protocol-diagnostic-"+newRequestID())
		writer := &diagnosticWriter{header: http.Header{}}
		started := time.Now()
		selected.Handler().ServeHTTP(writer, request)
		cancelled := shotCtx.Err() != nil
		cancel()
		result, ticket, ambiguous, cookies := collector.snapshot()
		result.Mode, result.TicketReplayed, result.RouteCookiesReplayed = mode, replayed, cookieCount
		result.LocalStatus, result.DurationMS = writer.status, time.Since(started).Milliseconds()
		if cancelled {
			result.Completed, result.Failure = false, "cancelled_or_timed_out"
		}
		if writer.status != 200 || !result.Completed {
			if result.Failure == "" {
				result.Failure = "request_not_completed"
			}
			report.Shots = append(report.Shots, result)
			report.Conclusion = "request_failed"
			return fail("diagnostic_request_failed")
		}
		report.Shots = append(report.Shots, result)
		if shot == 0 {
			firstTicket, firstAmbiguous, routeCookies = ticket, ambiguous, cookies
			continue
		}
		switch {
		case firstAmbiguous || ambiguous:
			report.Conclusion, report.TicketComparison = "ambiguous_ticket_signal", "ambiguous"
		case firstTicket == "" && ticket == "":
			report.Conclusion = "no_ticket_signal"
		case !replayed:
			report.Conclusion = "ticket_observed_without_replay"
		case ticket == "":
			report.Conclusion, report.TicketComparison = "ticket_replay_unverified", "not_returned"
		case ticket == firstTicket:
			report.Conclusion, report.TicketComparison = "ticket_replay_unverified", "same"
		default:
			report.Conclusion, report.TicketComparison = "ticket_replay_unverified", "changed"
		}
	}
	return report, nil
}

type diagnosticWriter struct {
	header http.Header
	status int
}

func (w *diagnosticWriter) Header() http.Header { return w.header }
func (w *diagnosticWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *diagnosticWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(b), nil
}
func (w *diagnosticWriter) Flush() {}

type protocolCollector struct {
	sync.Mutex
	key                         []byte
	cancel                      context.CancelFunc
	status                      int
	headers, metadataHeaders    []string
	counts                      map[string]int
	signals                     []ProtocolSignal
	tickets                     map[string]bool
	ambiguous, complete, failed bool
	cookies                     []*http.Cookie
	frames                      int
}

func (c *protocolCollector) signal(source, name, value string) {
	if len(c.signals) >= 128 {
		return
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(value))
	c.signals = append(c.signals, ProtocolSignal{Source: source, Name: name, Bytes: len(value), Fingerprint: hex.EncodeToString(mac.Sum(nil)[:12])})
}

func (c *protocolCollector) ticket(source, value string) {
	if value == "" {
		return
	}
	c.signal(source, "x-codex-turn-state", value)
	valid := strings.TrimSpace(value) == value
	for _, ch := range value {
		if ch < 32 || ch > 126 {
			valid = false
		}
	}
	if len(value) > 8192 || !valid || len(c.tickets) >= 8 {
		c.ambiguous = true
		return
	}
	c.tickets[value] = true
}

func (c *protocolCollector) handshake(status int, headers http.Header) {
	c.Lock()
	defer c.Unlock()
	c.status = status
	for name, values := range headers {
		if diagnosticKnownHeader(name) && len(c.headers) < 128 {
			c.headers = append(c.headers, strings.ToLower(name))
		} else {
			c.signal("handshake_header_name", "unrecognized", name)
		}
		if strings.EqualFold(name, "x-codex-turn-state") {
			for _, value := range values {
				c.ticket("handshake", value)
			}
		}
	}
	response := http.Response{Header: headers}
	for _, cookie := range response.Cookies() {
		if (cookie.Name == "__cflb" || cookie.Name == "__oailb") && len(cookie.Value) <= 4096 && len(c.cookies) < 8 {
			c.cookies = append(c.cookies, cookie)
		}
	}
}

func (c *protocolCollector) event(raw []byte) {
	c.Lock()
	defer c.Unlock()
	c.frames++
	if c.frames > 10000 {
		c.failed = true
		c.cancel()
		return
	}
	var event struct {
		Type         string                     `json:"type"`
		Headers      map[string]json.RawMessage `json:"headers"`
		ResumeToken  string                     `json:"resume_token"`
		ReplayCursor json.RawMessage            `json:"basispoints_replay_cursor"`
		Response     struct {
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &event) != nil {
		c.failed = true
		return
	}
	kind := event.Type
	if !diagnosticKnownEvent(kind) {
		c.signal("event_type", "unrecognized", kind)
		kind = "other"
	}
	c.counts[kind]++
	if kind == "error" || kind == "response.failed" || kind == "response.incomplete" || kind == "basispoints.response.server_draining" {
		c.failed = true
	}
	if kind == "response.completed" {
		c.complete = event.Response.Status == "completed" && (len(event.Response.Error) == 0 || string(event.Response.Error) == "null")
	}
	if kind == "basispoints.response.resume_token" && event.ResumeToken != "" {
		c.signal("control_frame", "resume_token", event.ResumeToken)
	}
	if len(event.ReplayCursor) != 0 {
		c.signal("control_frame", "basispoints_replay_cursor", string(event.ReplayCursor))
	}
	if kind != "basispoints.response.metadata" {
		return
	}
	for name, rawValue := range event.Headers {
		if diagnosticKnownHeader(name) && len(c.metadataHeaders) < 128 {
			c.metadataHeaders = append(c.metadataHeaders, strings.ToLower(name))
		} else {
			c.signal("metadata_header_name", "unrecognized", name)
		}
		if strings.EqualFold(name, "x-codex-turn-state") {
			var value string
			var values []string
			if json.Unmarshal(rawValue, &value) == nil {
				c.ticket("metadata", value)
			} else if json.Unmarshal(rawValue, &values) == nil {
				for _, value := range values {
					c.ticket("metadata", value)
				}
			} else {
				c.ambiguous = true
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(name), "x-openai-internal-basispoints-") {
			switch strings.ToLower(name) {
			case "x-openai-internal-basispoints-agent-prompt-sha256", "x-openai-internal-basispoints-agent-tools-sha256", "x-openai-internal-basispoints-prompt-version-id", "x-openai-internal-basispoints-tools-version-id":
				c.signal("metadata", strings.ToLower(name), string(rawValue))
			}
		}
	}
}

func (c *protocolCollector) snapshot() (ProtocolDiagnosticShot, string, bool, []*http.Cookie) {
	c.Lock()
	defer c.Unlock()
	result := ProtocolDiagnosticShot{HandshakeStatus: c.status, Completed: c.complete && !c.failed, HandshakeHeaderNames: append([]string{}, c.headers...), MetadataHeaderNames: append([]string{}, c.metadataHeaders...), Signals: append([]ProtocolSignal{}, c.signals...), EventCounts: map[string]int{}}
	for name, count := range c.counts {
		result.EventCounts[name] = count
	}
	sort.Strings(result.HandshakeHeaderNames)
	sort.Strings(result.MetadataHeaderNames)
	if c.failed {
		result.Failure = "upstream_protocol_or_response_error"
	}
	ticket := ""
	ambiguous := c.ambiguous || len(c.tickets) > 1
	if !ambiguous {
		for value := range c.tickets {
			ticket = value
		}
	}
	return result, ticket, ambiguous, append([]*http.Cookie(nil), c.cookies...)
}
