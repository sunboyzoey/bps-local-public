// Package auth manages isolated BPS OAuth credentials. Explicit device login
// writes a new file; the bridge never refreshes tokens or reads Codex implicitly.
//
// The token is sent to bps.openai.com as an Authorization header and, for the
// WebSocket handshake, an openai-bearer subprotocol. It is never placed in URLs.
package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Tokens supports standard token names and the legacy nested tokens envelope.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
}

type authFile struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	AccountID    string          `json:"account_id"`
	TokenType    string          `json:"token_type"`
	ExpiresAt    json.RawMessage `json:"expires_at"`
	ExpiresIn    int64           `json:"expires_in"`
	ObtainedAt   string          `json:"obtained_at"`
	AuthMode     string          `json:"auth_mode"`
	OpenAIAPIKey *string         `json:"OPENAI_API_KEY"`
	Tokens       *Tokens         `json:"tokens"`
	LastRefresh  string          `json:"last_refresh"`
}

// Store reloads credentials supplied by an external authorized login process.
type Store struct {
	path string

	mu        sync.Mutex
	tokens    Tokens
	expiresAt time.Time
	lastLoad  time.Time
}

// ErrNoChatGPTLogin means the file holds an API key instead of a ChatGPT login.
// Basispoints only accepts ChatGPT credentials, so this is fatal, not retryable.
var ErrNoChatGPTLogin = errors.New("no ChatGPT login in auth.json (Basispoints requires a ChatGPT account, not an API key)")
var ErrExpired = errors.New("BPS access token expired; renew the independent OAuth credentials through their authorized issuer (automatic refresh is not configured)")

// DefaultPath deliberately ignores CODEX_HOME. Explicit --auth takes precedence.
func DefaultPath() string {
	if path := strings.TrimSpace(os.Getenv("BPS_AUTH_FILE")); path != "" {
		return path
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ".bps-local", "oauth.json")
}

// Load reads the credential file once and validates that it is a ChatGPT login.
func Load(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	s := &Store{path: path}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reload() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var f authFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Tokens != nil && f.AccessToken != "" {
		return errors.New("ambiguous OAuth file: use either top-level access_token or nested tokens, not both")
	}
	if f.Tokens == nil {
		f.Tokens = &Tokens{AccessToken: f.AccessToken, RefreshToken: f.RefreshToken, IDToken: f.IDToken, AccountID: f.AccountID}
	}
	if strings.TrimSpace(f.Tokens.AccessToken) == "" {
		return ErrNoChatGPTLogin
	}
	if f.TokenType != "" && !strings.EqualFold(f.TokenType, "Bearer") {
		return errors.New("unsupported OAuth token_type; expected Bearer")
	}
	expiry := jwtExpiry(f.Tokens.AccessToken)
	var declared time.Time
	if len(f.ExpiresAt) > 0 && string(f.ExpiresAt) != "null" {
		var timestamp int64
		var text string
		if json.Unmarshal(f.ExpiresAt, &timestamp) == nil && timestamp > 0 {
			declared = time.Unix(timestamp, 0)
		} else if json.Unmarshal(f.ExpiresAt, &text) == nil {
			declared, err = time.Parse(time.RFC3339, text)
			if err != nil {
				return errors.New("invalid OAuth expires_at; expected RFC3339 or Unix seconds")
			}
		} else {
			return errors.New("invalid OAuth expires_at; expected RFC3339 or Unix seconds")
		}
	} else if f.ExpiresIn > 0 && f.ObtainedAt != "" {
		acquired, e := time.Parse(time.RFC3339, f.ObtainedAt)
		if e != nil {
			return errors.New("invalid OAuth obtained_at; expected RFC3339")
		}
		if f.ExpiresIn > int64((365*24*time.Hour)/time.Second) {
			return errors.New("OAuth expires_in exceeds supported duration")
		}
		declared = acquired.Add(time.Duration(f.ExpiresIn) * time.Second)
	} else if f.ExpiresIn > 0 && expiry.IsZero() {
		return errors.New("opaque access token with expires_in requires obtained_at or expires_at; reload must not extend token lifetime")
	}
	if !declared.IsZero() && (expiry.IsZero() || declared.Before(expiry)) {
		expiry = declared
	}
	if f.Tokens.AccountID == "" {
		f.Tokens.AccountID = AccountIDFromToken(f.Tokens.AccessToken)
	}
	s.tokens = *f.Tokens
	s.lastLoad = time.Now()
	s.expiresAt = expiry
	return nil
}

// Credentials returns the current access token and ChatGPT account id,
// reloading the file when the token is close to expiring.
func (s *Store) Credentials() (token string, accountID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// An external authorized process may replace the independent credential file.
	if time.Since(s.lastLoad) > 60*time.Second || s.expiringSoon() {
		if err := s.reload(); err != nil {
			// Keep serving the in-memory token if the file is momentarily
			// unreadable but the token is still valid.
			if s.tokens.AccessToken == "" || s.expiresAt.IsZero() || !s.expiresAt.After(time.Now()) {
				return "", "", err
			}
		}
	}
	if s.tokens.AccessToken == "" {
		return "", "", ErrNoChatGPTLogin
	}
	if !s.expiresAt.IsZero() && !s.expiresAt.After(time.Now()) {
		return "", "", ErrExpired
	}
	return s.tokens.AccessToken, s.tokens.AccountID, nil
}

func (s *Store) expiringSoon() bool {
	if s.expiresAt.IsZero() {
		return false
	}
	return time.Until(s.expiresAt) < 5*time.Minute
}

// Expiry reports the access-token expiry as read from the JWT, if present.
func (s *Store) Expiry() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expiresAt
}

// AccountIDFromToken recovers the ChatGPT account id from an id_token when the
// auth file omits the explicit field.
func AccountIDFromToken(idToken string) string {
	claims := decodeJWT(idToken)
	if claims == nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return strings.TrimSpace(id)
}

// PlanType reports the subscription tier, for diagnostics only.
func PlanType(idToken string) string {
	claims := decodeJWT(idToken)
	if claims == nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return ""
	}
	plan, _ := auth["chatgpt_plan_type"].(string)
	return strings.TrimSpace(plan)
}

func jwtExpiry(token string) time.Time {
	claims := decodeJWT(token)
	if claims == nil {
		return time.Time{}
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0)
}

func decodeJWT(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some encoders pad; retry with padding tolerated.
		payload, err = base64.URLEncoding.DecodeString(parts[1] + strings.Repeat("=", (4-len(parts[1])%4)%4))
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&claims); err != nil {
		return nil
	}
	return claims
}
