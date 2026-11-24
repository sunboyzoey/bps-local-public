package proxy

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

func TestGPT62ProtocolDiagnosticEffort(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		for _, tc := range []struct{ requested, wire string }{{"max", "max"}, {"none", "low"}} {
			t.Run(model+"/"+tc.requested, func(t *testing.T) {
				var shots atomic.Int32
				upstream := wsMock(t, func(ctx context.Context, conn *websocket.Conn, request map[string]any, _ *http.Request) {
					shots.Add(1)
					if request["model"] != model || request["reasoning_effort"] != tc.wire {
						t.Error("diagnostic changed model or effort")
					}
					completeWS(t, ctx, conn, []any{})
				})
				proxyURL, proxyCalls := diagnosticTestProxy(t, upstream.URL)
				report, err := RunProtocolDiagnostic(context.Background(), Config{Upstream: upstream.URL, ProxyURL: proxyURL}, diagnosticTestCredentials(), ProtocolDiagnosticOptions{Model: model, ReasoningEffort: tc.requested})
				if err != nil {
					t.Fatal(err)
				}
				if report.Model != model || report.ReasoningEffort != tc.requested || report.UpstreamReasoningEffort != tc.wire || shots.Load() != 2 || proxyCalls.Load() != 2 || len(report.Shots) != 2 || !report.Shots[0].Completed || !report.Shots[1].Completed {
					t.Fatal("diagnostic report differs from observed requests")
				}
			})
		}
	}
}

func TestGPT62ProtocolDiagnosticUnsupportedEffort(t *testing.T) {
	for _, options := range []ProtocolDiagnosticOptions{
		{Model: "gpt-6.2-sol", ReasoningEffort: "ultra"},
		{Model: "gpt-6.2-astra", ReasoningEffort: "ultra"},
		{Model: "gpt-6-astra", ReasoningEffort: "max"},
		{Model: "unknown-model", ReasoningEffort: "max"},
	} {
		// A nil credential source and empty proxy are deliberate: invalid effort
		// must be rejected before reading credentials or constructing a connection.
		report, err := RunProtocolDiagnostic(context.Background(), Config{}, nil, options)
		if err == nil || report.Failure != "diagnostic_invalid_effort" || len(report.Shots) != 0 {
			t.Fatal("invalid diagnostic effort reached credentials/network", options)
		}
	}
}
