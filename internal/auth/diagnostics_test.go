package auth

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

func TestInspectTokenScopes(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		source string
		known  bool
		want   []string
	}{
		{"scp array", map[string]any{"scp": []string{"profile", "openid", "profile"}}, "scp", true, []string{"openid", "profile"}},
		{"scp string", map[string]any{"scp": "openid profile"}, "scp", true, []string{"openid", "profile"}},
		{"scope fallback", map[string]any{"scope": "profile"}, "scope", true, []string{"profile"}},
		{"missing", map[string]any{}, "", false, nil},
		{"invalid scp does not fall back", map[string]any{"scp": 42, "scope": "profile"}, "scp", false, nil},
		{"mixed array", map[string]any{"scp": []any{"profile", 42}}, "scp", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(c.claims)
			token := "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".not-a-signature"
			got := InspectToken(token)
			if got.ScopeClaim != c.source || got.ScopesKnown != c.known || !reflect.DeepEqual(got.Scopes, c.want) {
				t.Fatalf("got %#v", got)
			}
		})
	}
}
