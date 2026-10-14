package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitSlackAudiencePolicy(t *testing.T) {
	base := `{"state_path":"state.db","source_id":"fixture","query_file":"events.sql","partner":"Example","channel":"C123","activation":"2030-01-01T00:00:00Z"`
	cases := []struct {
		name, extra string
		valid       bool
	}{
		{"legacy", `}`, true},
		{"private explicit", `,"audience_policy":"private_allowlist","workspace_id":"T123","allowed_user_ids":["U123"]}`, true},
		{"private implicit", `,"workspace_id":"T123","allowed_user_ids":["U123"]}`, true},
		{"public explicit", `,"audience_policy":"workspace_public","workspace_id":"T123"}`, true},
		{"public empty users", `,"audience_policy":"workspace_public","workspace_id":"T123","allowed_user_ids":[]}`, true},
		{"public conflicting users", `,"audience_policy":"workspace_public","workspace_id":"T123","allowed_user_ids":["U123"]}`, false},
		{"public missing workspace", `,"audience_policy":"workspace_public"}`, false},
		{"public invalid workspace", `,"audience_policy":"workspace_public","workspace_id":"unknown"}`, false},
		{"private missing guard", `,"audience_policy":"private_allowlist"}`, false},
		{"private missing users", `,"audience_policy":"private_allowlist","workspace_id":"T123"}`, false},
		{"unknown policy", `,"audience_policy":"public","workspace_id":"T123"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(base+tc.extra), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && c.DispatchEnabled {
				t.Fatal("audience selection enabled delivery")
			}
		})
	}
}
