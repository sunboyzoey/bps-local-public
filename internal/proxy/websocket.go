package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Leave space for SSE framing below the existing bridge's 16 MiB event limit.
const maxWSMessage = (16 << 20) - 1024

// excelClientInfo mirrors the public client profile seen in the Excel capture.
// No cookies, account IDs, tokens, prompt versions or captured session IDs are
// copied from that file. The actual platform is reported instead of always PC.
func excelClientInfo() map[string]string {
	platform := "PC"
	if runtime.GOOS == "darwin" {
		platform = "Mac"
	}
	return map[string]string{
		"X-Openai-Internal-Basispoints-Client-Product":        "basispoints-excel-plugin",
		"X-Openai-Internal-Basispoints-Client-Platform":       "excel",
		"X-Openai-Internal-Basispoints-Client-Agent-Profile":  "excel",
		"X-Openai-Internal-Basispoints-Client-Editor":         "excel",
		"X-Openai-Internal-Basispoints-Client-Host":           "office",
		"X-Openai-Internal-Basispoints-Client-Runtime":        "desktop",
		"X-Openai-Internal-Basispoints-Client-Platform-Class": platform,
		"X-Openai-Internal-Basispoints-Office-Host":           "Excel",
		"X-Openai-Internal-Basispoints-Office-Platform":       platform,
		"X-Openai-Internal-Basispoints-Browser-Name":          "unknown",
	}
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read always fills the buffer or terminates.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (s *Server) openWebSocket(ctx context.Context, body []byte, token, accountID string) (io.ReadCloser, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("invalid prepared WebSocket request")
	}
	requestID := newRequestID()
	doc["type"] = json.RawMessage(`"response.create"`)
	doc["basispoints_request_id"], _ = json.Marshal(requestID)
	var resume [16]byte
	_, _ = rand.Read(resume[:])
	doc["basispoints_resume_token"], _ = json.Marshal("resume_" + hex.EncodeToString(resume[:]))
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("could not encode WebSocket request")
	}
	if s.cfg.Verbose {
		var model, effort string
		var input []json.RawMessage
		var metadata map[string]json.RawMessage
		_ = json.Unmarshal(doc["model"], &model)
		_ = json.Unmarshal(doc["reasoning_effort"], &effort)
		_ = json.Unmarshal(doc["input"], &input)
		_ = json.Unmarshal(doc["metadata"], &metadata)
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		log.Printf("upstream ws prepared model=%q effort=%q input_items=%d metadata_keys=%q", clipModel(model), clipModel(effort), len(input), keys)
	}

	u, _ := url.Parse(s.cfg.Upstream) // Validated by New.
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	profile := excelClientInfo()
	profileJSON, _ := json.Marshal(profile)
	q := u.Query()
	q.Set("bps_client_info", string(profileJSON))
	q.Set("bps_auth_mode", "chatgpt")
	q.Set("bps_ws_affinity", requestID)
	// Only request control behavior implemented here. Resume tokens are scoped
	// to this request, but reconnection/replay is deliberately not attempted.
	q.Set("bps_control_frames", "upstream_sent,heartbeat")
	u.RawQuery = q.Encode()

	request, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	applyUpstreamHeaders(request, token, accountID)
	s.applyCustomHeaders(request)
	request.Header.Del("Content-Type")
	request.Header.Set("Accept", "*/*")
	for name, value := range profile {
		request.Header.Set(name, value)
	}
	conn, releaseTransport, err := s.dialWebSocket(ctx, u.String(), request.Header, token)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			releaseTransport()
		}
	}()
	observer := protocolObserverFromContext(ctx)
	conn.SetReadLimit(maxWSMessage)
	if s.cfg.Verbose {
		log.Printf("upstream ws handshake=101")
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, 30*time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, payload)
	cancelWrite()
	if err != nil {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("could not send WebSocket response.create; request was not retried")
	}
	readCtx, cancelRead := context.WithCancel(ctx)
	var requestedModel string
	_ = json.Unmarshal(doc["model"], &requestedModel)
	transferred = true
	return &webSocketBody{
		releaseTransport: releaseTransport,
		conn:             conn, ctx: readCtx, cancel: cancelRead,
		idleTimeout: s.cfg.WSIdleTimeout, pending: bytes.NewReader(nil),
		requestedModel: requestedModel, verbose: s.cfg.Verbose,
		observer: observer,
	}, nil
}

// webSocketBody converts one upstream WebSocket JSON message into one SSE event
// for the existing tool/structured-output bridge. It buffers at most one event.
// Bridge.Stream owns the reader goroutine; no second pump is needed here.
type webSocketBody struct {
	releaseTransport func()
	conn             *websocket.Conn
	ctx              context.Context
	cancel           context.CancelFunc
	idleTimeout      time.Duration
	pending          *bytes.Reader
	terminal         bool
	requestedModel   string
	verbose          bool
	observer         protocolObserver
	closeOnce        sync.Once
	eventCount       int
}

func (b *webSocketBody) Close() error {
	b.closeOnce.Do(func() {
		b.cancel()
		_ = b.conn.CloseNow()
		if b.releaseTransport != nil {
			b.releaseTransport()
		}
	})
	return nil
}

func (b *webSocketBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for b.pending.Len() == 0 {
		if b.terminal {
			return 0, io.EOF
		}
		ctx, cancel := context.WithTimeout(b.ctx, b.idleTimeout)
		kind, data, err := b.conn.Read(ctx) // Handles WebSocket ping/pong internally.
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		if err != nil {
			if b.ctx.Err() != nil {
				return 0, b.ctx.Err()
			}
			if timedOut {
				b.fail("basispoints_ws_timeout", "upstream WebSocket was idle too long; request was not retried")
			} else {
				b.fail("basispoints_ws_disconnected", "upstream WebSocket closed before a terminal event; request was not retried")
			}
			continue
		}
		if kind == websocket.MessageText {
			// Observe before validation, control-frame filtering, and cursor removal.
			b.observer.observeEvent(data)
		}
		var event map[string]json.RawMessage
		if kind != websocket.MessageText || json.Unmarshal(data, &event) != nil || event == nil {
			b.fail("basispoints_ws_protocol_error", "upstream sent an invalid WebSocket JSON event")
			continue
		}
		var eventType string
		if json.Unmarshal(event["type"], &eventType) != nil || !validEventType(eventType) {
			b.fail("basispoints_ws_protocol_error", "upstream WebSocket event has an invalid type")
			continue
		}
		b.eventCount++
		if b.verbose {
			// Only protocol event names; never log response bodies or tokens.
			if b.eventCount == 1 || eventType == "error" || eventType == "response.completed" || eventType == "response.failed" {
				log.Printf("upstream ws event_number=%d type=%q", b.eventCount, eventType)
			}
		}
		if eventType == "basispoints.response.server_draining" {
			b.fail("basispoints_ws_draining", "upstream is draining; automatic resume is not enabled")
			continue
		}
		switch eventType {
		case "basispoints.response.resume_token", "basispoints.response.metadata",
			"basispoints.response.upstream_sent", "basispoints.response.heartbeat":
			// Transport metadata, heartbeat and resume tokens are not Responses
			// output and must not leak into the client's stream or logs.
			continue
		}
		if eventType != "error" && !strings.HasPrefix(eventType, "response.") {
			b.fail("basispoints_ws_protocol_error", "unexpected upstream WebSocket event type")
			continue
		}
		delete(event, "basispoints_replay_cursor")
		b.terminal = eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" || eventType == "error"
		if b.verbose && eventType == "response.completed" {
			var response struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(event["response"], &response) == nil && response.Model != "" {
				log.Printf("upstream ws model requested=%q reported=%q mismatch=%t", clipModel(b.requestedModel), clipModel(response.Model), b.requestedModel != response.Model)
			}
		}
		compact, _ := json.Marshal(event)
		b.pending.Reset([]byte("event: " + eventType + "\ndata: " + string(compact) + "\n\n"))
	}
	return b.pending.Read(p)
}

func (b *webSocketBody) fail(code, message string) {
	event := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status": "failed", "output": []any{},
			"error": map[string]string{"code": code, "message": message},
		},
	}
	raw, _ := json.Marshal(event)
	b.terminal = true
	b.pending.Reset([]byte("event: response.failed\ndata: " + string(raw) + "\n\n"))
}

func validEventType(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

func clipModel(s string) string {
	if len(s) > 128 {
		return s[:128]
	}
	return s
}
