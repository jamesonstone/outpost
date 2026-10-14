package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jamesonstone/outpost/internal/core"
)

func publicGuardFixture() *guardFixture {
	f := newGuardFixture()
	ch := f.info["channel"].(map[string]any)
	ch["is_private"] = false
	ch["is_member"] = true
	ch["is_pending_ext_shared"] = false
	ch["pending_shared"] = []string{}
	ch["pending_connected_team_ids"] = []string{}
	ch["shared_team_ids"] = []string{"T123"}
	ch["context_team_id"] = "T123"
	return f
}

func publicClient(server *httptest.Server) Client {
	c := guardClient(server)
	c.AudiencePolicy = "workspace_public"
	c.AllowedUsers = nil
	return c
}

func TestPublicGuardRequiresExplicitAudience(t *testing.T) {
	f := publicGuardFixture()
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	c := guardClient(server)
	if _, err := c.Send(context.Background(), fixture()); err == nil {
		t.Fatal("default private audience accepted public channel")
	}
	c = publicClient(server)
	if _, err := c.Send(context.Background(), fixture()); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 {
		t.Fatalf("posts=%d", f.posts)
	}
	// Unknown/extra channel members are deliberately permitted by the public policy.
	f.pages[""] = members([]string{"UANYONE"}, "")
	if _, err := c.Send(context.Background(), fixture()); err != nil {
		t.Fatal(err)
	}
	// Every attempt must still recheck channel state after public opt-in.
	f.info["channel"].(map[string]any)["is_shared"] = true
	if _, err := c.Send(context.Background(), fixture()); err == nil {
		t.Fatal("shared channel change accepted")
	}
	if f.posts != 2 {
		t.Fatalf("posts=%d", f.posts)
	}
}

func TestPublicGuardRejectsUnsafeChannels(t *testing.T) {
	cases := []struct {
		name   string
		change func(*guardFixture)
	}{
		{"wrong workspace", func(f *guardFixture) { f.auth["team_id"] = "T999" }},
		{"wrong channel", func(f *guardFixture) { f.info["channel"].(map[string]any)["id"] = "C999" }},
		{"wrong context", func(f *guardFixture) { f.info["channel"].(map[string]any)["context_team_id"] = "T999" }},
		{"pending share", func(f *guardFixture) { f.info["channel"].(map[string]any)["pending_shared"] = []string{"T999"} }},
		{"pending connect", func(f *guardFixture) {
			f.info["channel"].(map[string]any)["pending_connected_team_ids"] = []string{"T999"}
		}},
		{"shared teams", func(f *guardFixture) {
			f.info["channel"].(map[string]any)["shared_team_ids"] = []string{"T123", "T999"}
		}},
		{"unknown sharing", func(f *guardFixture) { f.info["channel"].(map[string]any)["shared_team_ids"] = []string{} }},
		{"non-bot token", func(f *guardFixture) { delete(f.auth, "bot_id") }},
	}
	for _, flag := range []string{"is_private", "is_general", "is_shared", "is_ext_shared", "is_org_shared", "is_im", "is_mpim", "is_archived", "is_pending_ext_shared", "is_member"} {
		flag := flag
		cases = append(cases, struct {
			name   string
			change func(*guardFixture)
		}{"unsafe " + flag, func(f *guardFixture) { f.info["channel"].(map[string]any)[flag] = flag != "is_member" }})
	}
	for _, field := range []string{"is_member", "is_pending_ext_shared", "pending_shared", "pending_connected_team_ids", "shared_team_ids", "context_team_id"} {
		field := field
		cases = append(cases, struct {
			name   string
			change func(*guardFixture)
		}{"missing " + field, func(f *guardFixture) { delete(f.info["channel"].(map[string]any), field) }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := publicGuardFixture()
			tc.change(f)
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			c := publicClient(server)
			_, err := c.Send(context.Background(), fixture())
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) || sendErr.Category != "destination_guard_rejected" || !sendErr.Permanent || sendErr.Ambiguous {
				t.Fatalf("unexpected error %v", err)
			}
			if f.posts != 0 {
				t.Fatal("unsafe destination received post")
			}
		})
	}
}

func TestPublicGuardRejectsConflictingOrIncompleteConfiguration(t *testing.T) {
	for _, c := range []Client{
		{AudiencePolicy: "workspace_public"},
		{AudiencePolicy: "private_allowlist"},
		{AudiencePolicy: "unknown"},
		{Token: "synthetic-token", Channel: "C123", Workspace: "T123", AudiencePolicy: "workspace_public", AllowedUsers: []string{"UHUMAN"}},
	} {
		c.HTTP = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })}
		if err := c.Verify(context.Background()); err == nil {
			t.Fatal("incomplete or conflicting audience accepted")
		}
	}
}
