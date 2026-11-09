package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrRefreshGrantRejected identifies a definitive OAuth invalid_grant response.
// Other failures remain ambiguous because refresh tokens can rotate on use.
var ErrRefreshGrantRejected = errors.New("bps_refresh_grant_rejected")

type RefreshGrantRejection struct{}

func (*RefreshGrantRejection) Error() string { return "BPS refresh grant rejected: invalid_grant" }
func (*RefreshGrantRejection) Unwrap() error { return ErrRefreshGrantRejected }

// Refresh renews only an Excel/BPS grant. Persistence belongs to the caller.
// It never retries a token exchange because the server may rotate the RT.
func (c *DeviceClient) Refresh(ctx context.Context, old DeviceCredentials) (*DeviceCredentials, error) {
	if old.ClientID != ExcelClientID || old.RefreshToken == "" {
		return nil, errors.New("BPS refresh requires its original client ID and refresh token")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {old.ClientID}, "refresh_token": {old.RefreshToken}}
	var next DeviceCredentials
	status, code, err := c.post(ctx, "/oauth/token?unified=true", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &next)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		if (status == 400 || status == 401) && code == "invalid_grant" {
			return nil, &RefreshGrantRejection{}
		}
		return nil, fmt.Errorf("BPS refresh failed: HTTP %d (%s)", status, code)
	}
	if next.AccessToken == "" || !strings.EqualFold(next.TokenType, "Bearer") || next.ExpiresIn <= 0 || next.ExpiresIn > 31536000 {
		return nil, errors.New("invalid BPS refresh response")
	}
	if next.RefreshToken == "" {
		next.RefreshToken = old.RefreshToken
	}
	if next.IDToken == "" {
		next.IDToken = old.IDToken
	}
	if next.Scope == "" {
		next.Scope = old.Scope
	}
	next.ClientID = old.ClientID
	next.ObtainedAt = time.Now().UTC().Format(time.RFC3339)
	if AccountIDFromToken(old.AccessToken) != "" && AccountIDFromToken(next.AccessToken) != AccountIDFromToken(old.AccessToken) {
		return nil, errors.New("BPS refresh changed account identity")
	}
	return &next, nil
}
