package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictConfiguration(t *testing.T) {
	valid := `{"state_path":"state.db","source_id":"fixture","query_file":"events.sql","partner":"Example","channel":"C123","activation":"2030-01-01T00:00:00Z"}`
	for _, tt := range []struct {
		data string
		ok   bool
	}{
		{valid, true}, {strings.Replace(valid, "C123", "#channel", 1), false}, {strings.Replace(valid, "\"state_path\"", "\"secret_token\"", 1), false}, {valid + ` {}`, false}, {strings.Replace(valid, "}", `,"listen":"0.0.0.0:8080"}`, 1), false}, {strings.Replace(valid, "}", `,"poll_seconds":301}`, 1), false},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, []byte(tt.data), 0600)
		c, err := Load(path)
		if (err == nil) != tt.ok {
			t.Fatalf("valid=%v error=%v", tt.ok, err)
		}
		if tt.ok && (c.DispatchEnabled || c.PageSize != 500 || c.PollSeconds != 60) {
			t.Fatal("unsafe defaults")
		}
	}
}
