package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

func model62SSEEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("invalid SSE JSON: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func assertModel62Create(t *testing.T, doc map[string]any, model, effort string) {
	t.Helper()
	for key, want := range map[string]any{
		"type": "response.create", "model": model, "model_selection": "explicit",
		"reasoning_effort": effort, "stream": true, "store": false,
	} {
		if doc[key] != want {
			t.Errorf("upstream %s = %v, want %v", key, doc[key], want)
		}
	}
}

func TestModel62WebSocketStreamingAndEfforts(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		for _, tc := range []struct{ effort, want string }{
			{"", "medium"}, {"low", "low"}, {"medium", "medium"},
			{"high", "high"}, {"xhigh", "xhigh"}, {"max", "max"},
			{"none", "low"}, {"minimal", "low"},
		} {
			t.Run(model+"/"+tc.effort, func(t *testing.T) {
				usage := map[string]any{
					"input_tokens": float64(120), "output_tokens": float64(30), "total_tokens": float64(150),
					"input_tokens_details":  map[string]any{"cached_tokens": float64(40)},
					"output_tokens_details": map[string]any{"reasoning_tokens": float64(20)},
				}
				up := wsMock(t, func(ctx context.Context, c *websocket.Conn, doc map[string]any, _ *http.Request) {
					assertModel62Create(t, doc, model, tc.want)
					sendWS(t, ctx, c, map[string]any{"type": "response.output_text.delta", "delta": "OK"})
					sendWS(t, ctx, c, map[string]any{
						"type": "response.completed", "response": map[string]any{
							"id": "resp_model62", "status": "completed", "model": model + "-reported",
							"output": []any{}, "usage": usage,
						},
					})
				})
				s := wsBridge(t, up, nil)
				doc := map[string]any{"model": model, "input": "Only reply OK", "stream": true}
				if tc.effort != "" {
					doc["reasoning"] = map[string]any{"effort": tc.effort}
				}
				raw, _ := json.Marshal(doc)
				rec := postResponses(t, s.Handler(), string(raw))
				if rec.Code != http.StatusOK {
					t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
				}
				var delta string
				var completed map[string]any
				for _, event := range model62SSEEvents(t, rec.Body.String()) {
					switch event["type"] {
					case "response.output_text.delta":
						delta += event["delta"].(string)
					case "response.completed":
						completed, _ = event["response"].(map[string]any)
					}
				}
				if delta != "OK" || completed == nil || completed["status"] != "completed" {
					t.Fatalf("missing streamed text/completion: %s", rec.Body.String())
				}
				if completed["model"] != model+"-reported" || !reflect.DeepEqual(completed["usage"], usage) {
					t.Fatalf("upstream model or usage was changed: %#v", completed)
				}
			})
		}
	}
}

func TestModel62HistoricalMaxAndCurrentOverride(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		for _, current := range []string{"", "medium"} {
			t.Run(model+"/current="+current, func(t *testing.T) {
				want := "max"
				if current != "" {
					want = current
				}
				up := wsMock(t, func(ctx context.Context, c *websocket.Conn, doc map[string]any, _ *http.Request) {
					assertModel62Create(t, doc, model, want)
					completeWS(t, ctx, c, []any{})
				})
				s := wsBridge(t, up, nil)
				doc := map[string]any{"model": model, "input": []any{
					map[string]any{"type": "configuration_update", "reasoning": map[string]any{"effort": "max"}},
					map[string]any{"role": "user", "content": "continue"},
				}}
				if current != "" {
					doc["reasoning"] = map[string]any{"effort": current}
				}
				raw, _ := json.Marshal(doc)
				rec := postResponses(t, s.Handler(), string(raw))
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response.completed") {
					t.Fatalf("history request failed: HTTP %d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestModel62RejectsUltraBeforeConnecting(t *testing.T) {
	var attempts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(up.Close)
	s := wsBridge(t, up, nil)
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		rec := postResponses(t, s.Handler(), `{"model":"`+model+`","input":"test","reasoning":{"effort":"ultra"}}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ultra") {
			t.Errorf("%s: want explicit effort validation error; HTTP %d %s", model, rec.Code, rec.Body.String())
		}
	}
	if attempts.Load() != 0 {
		t.Fatalf("invalid effort reached upstream %d times", attempts.Load())
	}
}

func TestModel62ChangePreservesLegacyEffortMapping(t *testing.T) {
	for _, effort := range []string{"max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			up := wsMock(t, func(ctx context.Context, c *websocket.Conn, doc map[string]any, _ *http.Request) {
				assertModel62Create(t, doc, "gpt-6-astra", "xhigh")
				completeWS(t, ctx, c, []any{})
			})
			s := wsBridge(t, up, nil)
			rec := postResponses(t, s.Handler(), `{"model":"gpt-6-astra","input":"test","reasoning":{"effort":"`+effort+`"}}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response.completed") {
				t.Fatalf("legacy request failed: HTTP %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestModel62UpstreamErrorDoesNotRetryOrDowngrade(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		t.Run(model, func(t *testing.T) {
			var attempts atomic.Int32
			up := wsMock(t, func(ctx context.Context, c *websocket.Conn, doc map[string]any, _ *http.Request) {
				attempts.Add(1)
				assertModel62Create(t, doc, model, "max")
				sendWS(t, ctx, c, map[string]any{
					"type": "error", "code": "unsupported_model", "message": "fixture model is unavailable",
				})
			})
			s := wsBridge(t, up, nil)
			rec := postResponses(t, s.Handler(), `{"model":"`+model+`","input":"test","reasoning":{"effort":"max"}}`)
			if attempts.Load() != 1 || !strings.Contains(rec.Body.String(), "unsupported_model") || strings.Contains(rec.Body.String(), "response.completed") {
				t.Fatalf("error was hidden or retried: attempts=%d HTTP %d %s", attempts.Load(), rec.Code, rec.Body.String())
			}
		})
	}
}

func TestModel62ClientFunctionToolTranslation(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		t.Run(model, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{
				"summary": "Call test tool", "extended_summary": "", "destructive": false, "references": []any{},
				"code": `{"name":"test_tool","arguments":{"value":42}}`,
			})
			native := map[string]any{"type": "function_call", "name": "run_officejs", "id": "fc_model62", "call_id": "call_model62", "status": "completed", "arguments": string(args)}
			up := wsMock(t, func(ctx context.Context, c *websocket.Conn, doc map[string]any, _ *http.Request) {
				assertModel62Create(t, doc, model, "max")
				if _, exists := doc["tools"]; exists {
					t.Error("client tools must be converted into the BPS prologue")
				}
				input, _ := json.Marshal(doc["input"])
				if !strings.Contains(string(input), "test_tool") {
					t.Error("tool definition missing from BPS input")
				}
				sendWS(t, ctx, c, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": native})
				completeWS(t, ctx, c, []any{native})
			})
			s := wsBridge(t, up, nil)
			rec := postResponses(t, s.Handler(), `{"model":"`+model+`","input":"Call test_tool","reasoning":{"effort":"max"},"tools":[{"type":"function","name":"test_tool","parameters":{"type":"object"}}]}`)
			var translated map[string]any
			for _, event := range model62SSEEvents(t, rec.Body.String()) {
				if event["type"] == "response.output_item.done" {
					translated, _ = event["item"].(map[string]any)
				}
			}
			if rec.Code != http.StatusOK || translated == nil || translated["name"] != "test_tool" || translated["call_id"] != "call_model62" || translated["arguments"] != `{"value":42}` || !strings.Contains(rec.Body.String(), "response.completed") {
				t.Fatalf("client function tool was not translated: HTTP %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
