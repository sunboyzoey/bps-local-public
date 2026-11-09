package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshPreservesGrant(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("client_id") != ExcelClientID || r.Form.Get("refresh_token") != "old" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Has("scope") {
			t.Error("wrong refresh form")
		}
		ioJSON(w, map[string]any{"access_token": "new-at", "refresh_token": "new-rt", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer srv.Close()
	c := &DeviceClient{HTTP: srv.Client(), base: srv.URL}
	n, err := c.Refresh(context.Background(), DeviceCredentials{ClientID: ExcelClientID, RefreshToken: "old", Scope: "openid", IDToken: "identity"})
	if err != nil {
		t.Fatal(err)
	}
	if n.RefreshToken != "new-rt" || n.Scope != "openid" || n.IDToken != "identity" || n.ObtainedAt == "" {
		t.Fatal("lost grant fields")
	}
	_, err = c.Refresh(context.Background(), DeviceCredentials{ClientID: "codex", RefreshToken: "old"})
	if err == nil || calls != 1 {
		t.Fatal("cross-client refresh permitted")
	}
}
