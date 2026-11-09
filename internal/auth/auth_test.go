package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeJWT builds a token with the given exp and optional account id, matching the
// claim path Codex uses.
func makeJWT(t *testing.T, exp time.Time, accountID, plan string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := map[string]any{"exp": exp.Unix()}
	if accountID != "" || plan != "" {
		claims["https://api.openai.com/auth"] = map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  plan,
		}
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}

func writeAuth(t *testing.T, content map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func chatGPTAuth(t *testing.T, exp time.Time, accountID string) map[string]any {
	t.Helper()
	return map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  makeJWT(t, exp, accountID, "pro"),
			"refresh_token": "refresh-value",
			"id_token":      makeJWT(t, exp, accountID, "pro"),
			"account_id":    accountID,
		},
	}
}

func TestLoadReadsChatGPTLogin(t *testing.T) {
	exp := time.Now().Add(24 * time.Hour)
	path := writeAuth(t, chatGPTAuth(t, exp, "acct-123"))

	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	token, accountID, err := store.Credentials()
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if accountID != "acct-123" {
		t.Fatalf("accountID = %q, want acct-123", accountID)
	}
	if got := store.Expiry().Unix(); got != exp.Unix() {
		t.Fatalf("expiry = %d, want %d", got, exp.Unix())
	}
}

// An API key cannot authenticate against Basispoints, so this must fail loudly
// rather than produce a confusing 401 later.
func TestLoadRejectsAPIKeyLogin(t *testing.T) {
	key := "sk-test"
	path := writeAuth(t, map[string]any{
		"auth_mode":      "apikey",
		"OPENAI_API_KEY": key,
		"tokens":         nil,
	})
	if _, err := Load(path); err != ErrNoChatGPTLogin {
		t.Fatalf("err = %v, want ErrNoChatGPTLogin", err)
	}
}

func TestLoadReportsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// Codex CLI rewrites auth.json when it refreshes, so a stale in-memory token must
// be replaced by the new one.
func TestPicksUpExternalRefresh(t *testing.T) {
	oldExp := time.Now().Add(2 * time.Minute) // inside the 5-minute refresh window
	newExp := time.Now().Add(10 * time.Hour)
	path := writeAuth(t, chatGPTAuth(t, oldExp, "acct-old"))

	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, _, err := store.Credentials(); err != nil {
		t.Fatalf("Credentials: %v", err)
	}

	// Simulate Codex CLI refreshing the file in place.
	refreshed := chatGPTAuth(t, newExp, "acct-new")
	if err := rewrite(path, refreshed); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	_, accountID, err := store.Credentials()
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if accountID != "acct-new" {
		t.Fatalf("accountID = %q, want acct-new (did not re-read the file)", accountID)
	}
}

// A long-lived token should not force a disk read on every request.
func TestFreshTokenIsCached(t *testing.T) {
	path := writeAuth(t, chatGPTAuth(t, time.Now().Add(24*time.Hour), "acct-fresh"))
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, _, err := store.Credentials(); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	stamp := store.lastLoad

	// Delete the file; a cached read must still succeed.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, _, err := store.Credentials(); err != nil {
		t.Fatalf("Credentials after remove: %v", err)
	}
	if !store.lastLoad.Equal(stamp) {
		t.Fatal("expected the cached token to be reused without touching disk")
	}
}

func TestAccountIDFromToken(t *testing.T) {
	tok := makeJWT(t, time.Now().Add(time.Hour), "acct-xyz", "pro")
	if got := AccountIDFromToken(tok); got != "acct-xyz" {
		t.Fatalf("got %q", got)
	}
	if got := AccountIDFromToken("not-a-jwt"); got != "" {
		t.Fatalf("expected empty for malformed token, got %q", got)
	}
}

func TestPlanType(t *testing.T) {
	tok := makeJWT(t, time.Now().Add(time.Hour), "a", "pro")
	if got := PlanType(tok); got != "pro" {
		t.Fatalf("got %q", got)
	}
}

func TestJWTExpiryToleratesGarbage(t *testing.T) {
	if got := jwtExpiry("garbage"); !got.IsZero() {
		t.Fatalf("expected zero time, got %v", got)
	}
}

func rewrite(path string, content map[string]any) error {
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func TestDefaultPathIsIsolated(t *testing.T) {
	t.Setenv("BPS_AUTH_FILE", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	home, _ := os.UserHomeDir()
	if got := DefaultPath(); got != filepath.Join(home, ".bps-local", "oauth.json") {
		t.Fatalf("unexpected default %s", got)
	}
	override := filepath.Join(t.TempDir(), "separate.json")
	t.Setenv("BPS_AUTH_FILE", override)
	if DefaultPath() != override {
		t.Fatal("environment override ignored")
	}
	explicit := writeAuth(t, chatGPTAuth(t, time.Now().Add(time.Hour), "explicit"))
	s, e := Load(explicit)
	if e != nil {
		t.Fatal(e)
	}
	_, account, e := s.Credentials()
	if e != nil || account != "explicit" {
		t.Fatal("explicit path must win")
	}
}

func TestFlatOAuthAndExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	token := makeJWT(t, exp, "account-from-at", "pro")
	path := writeAuth(t, map[string]any{"access_token": token, "refresh_token": "unused-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "openid profile"})
	before, _ := os.ReadFile(path)
	s, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	got, account, e := s.Credentials()
	if e != nil || got != token || account != "account-from-at" {
		t.Fatal("flat token failed")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("credentials were modified")
	}
	expired := writeAuth(t, map[string]any{"access_token": makeJWT(t, time.Now().Add(-time.Minute), "a", ""), "refresh_token": "must-not-refresh"})
	s, e = Load(expired)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Credentials(); e != ErrExpired {
		t.Fatalf("expired token error=%v", e)
	}
}

func TestOAuthExpiryDoesNotResetOnReload(t *testing.T) {
	acquired := time.Now().Add(-time.Hour).Truncate(time.Second)
	path := writeAuth(t, map[string]any{"access_token": "opaque", "account_id": "a", "expires_in": 7200, "obtained_at": acquired.Format(time.RFC3339)})
	s, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	want := acquired.Add(2 * time.Hour)
	if !s.Expiry().Equal(want) {
		t.Fatal("incorrect initial expiry")
	}
	if e = s.reload(); e != nil {
		t.Fatal(e)
	}
	if !s.Expiry().Equal(want) {
		t.Fatal("reload extended expiry")
	}
	path = writeAuth(t, map[string]any{"access_token": "opaque", "expires_in": 7200})
	if _, e = Load(path); e == nil {
		t.Fatal("relative expiry without acquisition time accepted")
	}
}

func TestOAuthRejectsAmbiguousFormats(t *testing.T) {
	for _, d := range []map[string]any{
		{"access_token": "one", "tokens": map[string]any{"access_token": "two"}},
		{"access_token": "one", "token_type": "Basic"},
		{"access_token": "one", "expires_at": "invalid"},
	} {
		if _, e := Load(writeAuth(t, d)); e == nil {
			t.Fatal("invalid OAuth file accepted")
		}
	}
}

func TestFailedReloadDoesNotServeExpiredToken(t *testing.T) {
	path := writeAuth(t, chatGPTAuth(t, time.Now().Add(time.Minute), "valid"))
	s, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Credentials(); e != nil {
		t.Fatal("still-valid in-memory token should survive transient read failure")
	}
	s.expiresAt = time.Now().Add(-time.Second)
	if _, _, e = s.Credentials(); e == nil {
		t.Fatal("expired in-memory token was served")
	}
}
