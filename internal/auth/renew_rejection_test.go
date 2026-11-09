package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshDefinitiveInvalidGrantOnly(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
	}{
		{"oauth_string", `{"error":"invalid_grant","error_description":"private-token"}`, 400, true},
		{"code_object", `{"error":{"code":"invalid_grant","message":"private-token"}}`, 401, true},
		{"unknown_bad_request", `{"error":"invalid_request"}`, 400, false},
		{"server_error", `{"error":"invalid_grant"}`, 500, false},
		{"unsafe_code", `{"error":"private-token"}`, 400, false},
		{"unsafe_identifier", `{"error":"private_token"}`, 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &DeviceClient{HTTP: srv.Client(), base: srv.URL}
			_, err := c.Refresh(context.Background(), DeviceCredentials{ClientID: ExcelClientID, RefreshToken: "rt-private"})
			if err == nil || errors.Is(err, ErrRefreshGrantRejected) != tc.rejected || calls != 1 {
				t.Fatalf("bad rejection: %v calls=%d", err, calls)
			}
			if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "private_token") || strings.Contains(err.Error(), "rt-private") {
				t.Fatal("private OAuth details leaked")
			}
		})
	}
}
