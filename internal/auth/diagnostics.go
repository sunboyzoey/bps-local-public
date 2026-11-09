package auth

import (
	"sort"
	"strings"
)

// TokenDiagnostics inspects unverified claims for troubleshooting only.
// It does not establish permission to use BPS or validate the JWT signature.
type TokenDiagnostics struct {
	ClientID    string
	ScopeClaim  string
	Scopes      []string
	ScopesKnown bool
}

func InspectToken(token string) TokenDiagnostics {
	claims := decodeJWT(token)
	var result TokenDiagnostics
	if claims == nil {
		return result
	}
	result.ClientID, _ = claims["client_id"].(string)
	value, ok := claims["scp"]
	if ok {
		result.ScopeClaim = "scp"
	} else {
		value, ok = claims["scope"]
		if ok {
			result.ScopeClaim = "scope"
		}
	}
	if !ok {
		return result
	}
	var scopes []string
	switch v := value.(type) {
	case string:
		scopes = strings.Fields(v)
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return result
			}
			scopes = append(scopes, s)
		}
	default:
		return result
	}
	seen := map[string]bool{}
	for _, s := range scopes {
		if s != "" && !seen[s] {
			result.Scopes = append(result.Scopes, s)
			seen[s] = true
		}
	}
	sort.Strings(result.Scopes)
	result.ScopesKnown = true
	return result
}
