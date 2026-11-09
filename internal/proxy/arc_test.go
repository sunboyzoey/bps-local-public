package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestARCLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "opted_out", "registration_rejected", "wrong_topic", "unsafe_url", "not_ready"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			steps := []string{}
			mark := func(s string) { mu.Lock(); steps = append(steps, s); mu.Unlock() }
			var up *httptest.Server
			mux := http.NewServeMux()
			mux.HandleFunc("/basispoints/api/", func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/basispoints/api/")
				mark(path)
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing expected API credential")
				}
				w.Header().Set("Content-Type", "application/json")
				switch path {
				case "responses/access":
					io.WriteString(w, `{"allowed":true}`)
				case "accounts/check":
					io.WriteString(w, `{"accounts":[]}`)
				case "arc/settings":
					json.NewEncoder(w).Encode(map[string]bool{"opted_out": scenario == "opted_out"})
				case "arc/executors/register":
					if scenario == "registration_rejected" {
						w.WriteHeader(403)
						io.WriteString(w, `{"secret":"do-not-log"}`)
						return
					}
					var d struct {
						Tools []any  `json:"supported_tools"`
						Title string `json:"document_title"`
					}
					if json.NewDecoder(r.Body).Decode(&d) != nil || len(d.Tools) != 0 || !strings.Contains(d.Title, "no workbook") {
						t.Error("registration must not claim workbook tools")
					}
					control := strings.Replace(up.URL, "http://", "ws://", 1) + "/control?verify=private-test-value"
					if scenario == "unsafe_url" {
						control = "wss://example.com/control?verify=do-not-log"
					}
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "executor_session_id": "bp_arc_e_test", "client_sync": map[string]string{"websocket_url": control, "command_topic_id": "topic-test"}})
				case "arc/executors/bp_arc_e_test/ready":
					json.NewEncoder(w).Encode(map[string]any{"ok": scenario != "not_ready", "executor_session_id": "bp_arc_e_test"})
				case "task-title":
					var d map[string]string
					json.NewDecoder(r.Body).Decode(&d)
					if d["task_id"] != "task-test" || d["user_message"] != "只回复 OK" {
						t.Error("title did not share prepared task and prompt")
					}
					io.WriteString(w, `{"title":"test"}`)
				case "responses":
					c, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"bps.openai.com"}, Subprotocols: []string{"responses"}})
					if e != nil {
						t.Error(e)
						return
					}
					defer c.CloseNow()
					ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
					defer cancel()
					_, _, e = c.Read(ctx)
					if e != nil {
						return
					}
					_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.completed","response":{"model":"test","status":"completed","output":[]}}`))
				default:
					t.Errorf("unexpected API path %s", path)
					w.WriteHeader(404)
				}
			})
			mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
				mark("control")
				if r.Header.Get("Authorization") != "" || r.Header.Get("Chatgpt-Account-Id") != "" || r.Header.Get("Sec-WebSocket-Protocol") != "" {
					t.Error("account credentials leaked to control socket")
				}
				c, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"bps.openai.com"}})
				if e != nil {
					t.Error(e)
					return
				}
				defer c.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				_, b, e := c.Read(ctx)
				if e != nil {
					return
				}
				var commands []struct {
					ID      int `json:"id"`
					Command struct {
						Type  string `json:"type"`
						Topic string `json:"topic_id"`
					} `json:"command"`
				}
				if json.Unmarshal(b, &commands) != nil || len(commands) != 2 || commands[0].Command.Type != "connect" || commands[1].Command.Topic != "topic-test" {
					t.Error("invalid subscription")
					return
				}
				topic := "topic-test"
				if scenario == "wrong_topic" {
					topic = "wrong"
				}
				ack, _ := json.Marshal([]any{map[string]any{"id": 1, "type": "reply", "reply": map[string]string{"type": "connect"}}, map[string]any{"id": 2, "type": "reply", "reply": map[string]string{"type": "subscribe", "topic_id": topic}}})
				_ = c.Write(ctx, websocket.MessageText, ack)
				_, _, _ = c.Read(ctx) // Client must close its control connection during cleanup.
			})
			up = httptest.NewServer(mux)
			defer up.Close()
			s, e := New(Config{Upstream: up.URL + "/basispoints/api/responses", ARCTextSession: true}, nil)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			body := []byte(`{"model":"test","input":[{"role":"user","content":[{"type":"input_text","text":"只回复 OK"}]}],"metadata":{"task_id":"task-test"}}`)
			r, e := s.openUpstream(ctx, body, "test-token", "test-account")
			if scenario == "success" {
				if e != nil {
					t.Fatal(e)
				}
				b, e := io.ReadAll(r)
				if e != nil {
					t.Fatal(e)
				}
				if !strings.Contains(string(b), "response.completed") {
					t.Fatal("missing completion")
				}
				_ = r.Close()
				_ = r.Close()
			} else {
				if e == nil {
					r.Close()
					t.Fatal("expected setup failure")
				}
				if strings.Contains(e.Error(), "verify") || strings.Contains(e.Error(), "do-not-log") {
					t.Fatal("sensitive error leaked")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			joined := strings.Join(steps, ",")
			if scenario == "success" {
				want := "responses/access,accounts/check,arc/settings,arc/executors/register,control,arc/executors/bp_arc_e_test/ready,task-title,responses"
				if joined != want {
					t.Errorf("order=%s", joined)
				}
			} else {
				for _, x := range steps {
					if x == "responses" {
						t.Fatal("generation sent after setup failed")
					}
				}
			}
		})
	}
}

func TestARCTextOnlyAndURLValidation(t *testing.T) {
	for _, raw := range []string{`{"input":"hi","tools":[{"type":"function"}]}`, `{"input":[{"type":"function_call_output"}]}`} {
		if arcTextOnly([]byte(raw)) {
			t.Error("tool request accepted")
		}
	}
	if !arcTextOnly([]byte(`{"input":"hi"}`)) {
		t.Error("text rejected")
	}
	for _, u := range []string{"wss://evil.example/p5/ws/user/a", "wss://ws.chatgpt.com.evil.example/p5/ws/user/a", "ws://ws.chatgpt.com/p5/ws/user/a", "wss://user:pass@ws.chatgpt.com/p5/ws/user/a"} {
		if validARCControlURL(u, "https://bps.openai.com/basispoints/api/responses") {
			t.Error("unsafe URL accepted")
		}
	}
}
