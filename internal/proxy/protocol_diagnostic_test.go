package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/sunboyzoey/GPT-BPS/internal/auth"
)

func diagnosticTestCredentials() diagnosticCredentials {
	claims, _ := json.Marshal(map[string]any{"client_id": auth.ExcelClientID, "sub": "private-subject", "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "private-account"}})
	return diagnosticCredentials{"e30." + base64.RawURLEncoding.EncodeToString(claims) + ".private-signature", "private-account", time.Now().Add(time.Hour)}
}

func diagnosticTestProxy(t *testing.T, target string) (string, *atomic.Int32) {
	t.Helper()
	u, _ := url.Parse(target)
	reverse := httputil.NewSingleHostReverseProxy(u)
	reverse.Transport = &http.Transport{Proxy: nil}
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); reverse.ServeHTTP(w, r) }))
	t.Cleanup(proxy.Close)
	return proxy.URL, &calls
}

func TestProtocolDiagnosticObservesBPSWithoutInventingQualityVerdict(t *testing.T) {
	for _, scenario := range []string{"no_ticket", "metadata_same", "metadata_changed", "handshake_not_returned", "ambiguous", "late_ticket"} {
		t.Run(scenario, func(t *testing.T) {
			var shots atomic.Int32
			var sessions []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := shots.Add(1)
				wantReplay := n == 2 && (scenario == "metadata_same" || scenario == "metadata_changed" || scenario == "handshake_not_returned")
				if wantReplay {
					if r.Header.Get("X-Codex-Turn-State") != "private-ticket-one" || !strings.Contains(r.Header.Get("Cookie"), "__cflb=private-route") || strings.Contains(r.Header.Get("Cookie"), "unrelated") {
						t.Error("wrong ticket or cookie replay")
					}
				} else if r.Header.Get("X-Codex-Turn-State") != "" || r.Header.Get("Cookie") != "" {
					t.Error("baseline inherited ticket/cookie")
				}
				w.Header().Add("Set-Cookie", "__cflb=private-route; Path=/")
				w.Header().Add("Set-Cookie", "unrelated=must-never-replay; Path=/")
				w.Header().Set("X-Debug-Secret", "private-header-value")
				if n == 1 && (scenario == "handshake_not_returned" || scenario == "ambiguous") {
					w.Header().Set("X-Codex-Turn-State", "private-ticket-one")
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"responses"}, OriginPatterns: []string{"bps.openai.com"}})
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				var request map[string]any
				if err := wsjson.Read(r.Context(), conn, &request); err != nil {
					t.Error(err)
					return
				}
				if request["type"] != "response.create" || request["model"] != "gpt-6-astra" || request["store"] != false || request["reasoning_effort"] != "medium" {
					t.Error("not a normal BPS request")
				}
				sessions = append(sessions, request["metadata"].(map[string]any)["task_id"].(string))
				if r.Header.Get("Authorization") != "Bearer "+diagnosticTestCredentials().token || r.Header.Get("Origin") != "https://bps.openai.com" {
					t.Error("wrong BPS credential or client profile")
				}
				headers := map[string]string{"X-OpenAI-Internal-Basispoints-Agent-Prompt-Sha256": "private-prompt-hash", "Set-Cookie": "private-metadata-cookie"}
				if scenario == "metadata_same" || scenario == "metadata_changed" {
					headers["X-Codex-Turn-State"] = "private-ticket-one"
				}
				if scenario == "metadata_changed" && n == 2 {
					headers["X-Codex-Turn-State"] = "private-ticket-two"
				}
				if scenario == "ambiguous" {
					headers["X-Codex-Turn-State"] = "private-ticket-other"
				}
				if scenario == "late_ticket" && n == 2 {
					headers["X-Codex-Turn-State"] = "private-ticket-one"
				}
				sendWS(t, r.Context(), conn, map[string]any{"type": "basispoints.response.metadata", "headers": headers})
				sendWS(t, r.Context(), conn, map[string]any{"type": "basispoints.response.resume_token", "resume_token": "private-resume-token"})
				completeWS(t, r.Context(), conn, []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "private-answer-body"}}}})
			}))
			defer upstream.Close()
			proxyURL, proxyCalls := diagnosticTestProxy(t, upstream.URL)
			report, err := RunProtocolDiagnostic(context.Background(), Config{Upstream: upstream.URL, ProxyURL: proxyURL, Headers: map[string]string{"X-Codex-Turn-State": "must-be-cleared"}}, diagnosticTestCredentials(), ProtocolDiagnosticOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if shots.Load() != 2 || proxyCalls.Load() != 2 || len(report.Shots) != 2 || report.QualityAssessment != "not_assessed" || !report.Shots[0].Completed || !report.Shots[1].Completed || report.FinishedAt.Before(report.StartedAt) {
				t.Fatalf("invalid observation: %+v", report)
			}
			if sessions[0] == sessions[1] {
				t.Fatal("independent shots reused a business conversation")
			}
			wantConclusion, wantComparison := "ticket_replay_unverified", "not_available"
			switch scenario {
			case "no_ticket":
				wantConclusion = "no_ticket_signal"
			case "metadata_same":
				wantComparison = "same"
			case "metadata_changed":
				wantComparison = "changed"
			case "handshake_not_returned":
				wantComparison = "not_returned"
			case "ambiguous":
				wantConclusion, wantComparison = "ambiguous_ticket_signal", "ambiguous"
			case "late_ticket":
				wantConclusion = "ticket_observed_without_replay"
			}
			if report.Conclusion != wantConclusion || report.TicketComparison != wantComparison {
				t.Fatalf("unexpected conclusion %s/%s", report.Conclusion, report.TicketComparison)
			}
			raw, _ := json.Marshal(report)
			for _, secret := range []string{"private-ticket", "private-route", "private-header-value", "private-prompt-hash", "private-metadata-cookie", "private-resume-token", "private-cursor", "private-answer-body", "private-account", "private-signature", "must-never-replay"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("report leaked %s", secret)
				}
			}
			if !strings.Contains(string(raw), "x-openai-internal-basispoints-agent-prompt-sha256") || report.Shots[0].EventCounts["basispoints.response.metadata"] != 1 {
				t.Fatal("filtered away raw metadata observation")
			}
		})
	}
}

func TestProtocolDiagnosticFailureNeverRetries(t *testing.T) {
	for _, scenario := range []string{"401", "redirect", "stream_error", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			entered := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if scenario == "401" {
					w.Header().Set("X-Private", "secret-bearer")
					w.WriteHeader(401)
					return
				}
				if scenario == "redirect" {
					w.Header().Set("Location", "https://not-allowed.invalid/secret")
					w.WriteHeader(302)
					return
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"responses"}, OriginPatterns: []string{"bps.openai.com"}})
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				var payload map[string]any
				if err := wsjson.Read(r.Context(), conn, &payload); err != nil {
					t.Error(err)
					return
				}
				if scenario == "stream_error" {
					sendWS(t, r.Context(), conn, map[string]any{"type": "error", "message": "private-upstream-error"})
					return
				}
				close(entered)
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			proxyURL, _ := diagnosticTestProxy(t, upstream.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				go func() { <-entered; cancel() }()
			}
			report, err := RunProtocolDiagnostic(ctx, Config{Upstream: upstream.URL, ProxyURL: proxyURL}, diagnosticTestCredentials(), ProtocolDiagnosticOptions{})
			if err == nil || calls.Load() != 1 || len(report.Shots) != 1 || report.Shots[0].Completed || report.QualityAssessment != "not_assessed" {
				t.Fatalf("failure retried or graded: %+v error=%v", report, err)
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "private-upstream-error") || strings.Contains(string(raw), "secret-bearer") || strings.Contains(err.Error(), "private") {
				t.Fatal("failure leaked remote details")
			}
		})
	}
}

type diagnosticBadStore struct{}

func (diagnosticBadStore) Expiry() time.Time { return time.Time{} }
func (diagnosticBadStore) Credentials() (string, string, error) {
	return "", "", errors.New("private-credential-error")
}

func TestProtocolDiagnosticPreflightWithoutNetwork(t *testing.T) {
	for _, scenario := range []string{"no_proxy", "direct", "bad_proxy", "wrong_client", "wrong_account", "expired", "unknown_expiry", "unreadable", "bad_model", "bad_effort"} {
		t.Run(scenario, func(t *testing.T) {
			credentials := diagnosticTestCredentials()
			var store CredentialSource = credentials
			cfg := Config{ProxyURL: "http://127.0.0.1:1"}
			options := ProtocolDiagnosticOptions{}
			switch scenario {
			case "no_proxy":
				cfg.ProxyURL = ""
			case "direct":
				cfg.ProxyURL = "direct://"
			case "bad_proxy":
				cfg.ProxyURL = "http://private-user:private-pass@proxy/?token=private"
			case "wrong_client":
				credentials.token = "opaque"
				store = credentials
			case "wrong_account":
				credentials.account = "different"
				store = credentials
			case "expired":
				credentials.expiry = time.Now().Add(-time.Hour)
				store = credentials
			case "unknown_expiry":
				credentials.expiry = time.Time{}
				store = credentials
			case "unreadable":
				store = diagnosticBadStore{}
			case "bad_model":
				options.Model = "private\nmodel"
			case "bad_effort":
				options.ReasoningEffort = "invalid"
			}
			report, err := RunProtocolDiagnostic(context.Background(), cfg, store, options)
			if err == nil || len(report.Shots) != 0 || report.Failure == "" || report.QualityAssessment != "not_assessed" || strings.Contains(err.Error(), "private") {
				t.Fatalf("preflight not safe: %+v error=%v", report, err)
			}
		})
	}
}

func TestProtocolDiagnosticNeverNormalizesTicketOrEchoesUnknownNames(t *testing.T) {
	for _, value := range []string{" ticket ", "ticket\r\nvalue", "ticket\x00value", "ticket\tvalue", "票据"} {
		collector := &protocolCollector{key: []byte("one-run-key"), counts: map[string]int{}, tickets: map[string]bool{}, cancel: func() {}}
		collector.ticket("metadata", "ticket")
		collector.ticket("metadata", value)
		_, ticket, ambiguous, _ := collector.snapshot()
		if !ambiguous || ticket != "" {
			t.Fatal("normalized or accepted an unsafe opaque ticket")
		}
	}
	collector := &protocolCollector{key: []byte("one-run-key"), counts: map[string]int{}, tickets: map[string]bool{}, cancel: func() {}}
	collector.handshake(101, http.Header{"X-Private-Cookie-Value": {"private-value"}})
	collector.event([]byte(`{"type":"response.private_secret","delta":"private-text"}`))
	collector.event([]byte(`{"type":"basispoints.response.metadata","headers":{"X-Private-Token-Name":"private-value"}}`))
	report, _, _, _ := collector.snapshot()
	raw, _ := json.Marshal(report)
	if strings.Contains(strings.ToLower(string(raw)), "private") || report.EventCounts["other"] != 1 || len(report.Signals) != 3 {
		t.Fatal("unknown structural names were not fingerprinted")
	}
}
