package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jamesonstone/outpost/internal/core"
)

type guardFixture struct {
	auth             map[string]any
	info             map[string]any
	pages            map[string]any
	posts, authCalls int
	status           int
}

func newGuardFixture() *guardFixture {
	return &guardFixture{
		auth: map[string]any{"ok": true, "team_id": "T123", "user_id": "UBOT", "bot_id": "B123"},
		info: map[string]any{"ok": true, "channel": map[string]any{
			"id": "C123", "is_private": true, "is_general": false, "is_shared": false,
			"is_ext_shared": false, "is_org_shared": false, "is_im": false, "is_mpim": false, "is_archived": false,
		}},
		pages: map[string]any{"": members([]string{"UHUMAN", "UBOT"}, "")},
	}
}

func members(ids []string, cursor string) any {
	return map[string]any{"ok": true, "members": ids, "response_metadata": map[string]any{"next_cursor": cursor}}
}

func (f *guardFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer synthetic-token" {
		panic("incorrect fixture authentication")
	}
	if strings.HasPrefix(r.URL.Path, "/conversations.") && r.URL.Query().Get("channel") != "C123" {
		panic("incorrect fixture channel")
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	var result any
	switch r.URL.Path {
	case "/auth.test":
		f.authCalls++
		result = f.auth
	case "/conversations.info":
		result = f.info
	case "/conversations.members":
		result = f.pages[r.URL.Query().Get("cursor")]
	case "/chat.postMessage":
		f.posts++
		result = map[string]any{"ok": true, "channel": "C123", "ts": "123.456"}
	default:
		panic("unexpected fixture method")
	}
	json.NewEncoder(w).Encode(result)
}

func guardClient(server *httptest.Server) Client {
	return Client{Token: "synthetic-token", Channel: "C123", Workspace: "T123", AllowedUsers: []string{"UHUMAN"}, APIBase: server.URL, Endpoint: server.URL + "/chat.postMessage", HTTP: server.Client()}
}

func TestGuardRejectsUnsafeDestinationsWithoutPost(t *testing.T) {
	cases := []struct {
		name   string
		change func(*guardFixture)
	}{
		{"wrong workspace", func(f *guardFixture) { f.auth["team_id"] = "T999" }},
		{"missing workspace", func(f *guardFixture) { delete(f.auth, "team_id") }},
		{"missing bot identity", func(f *guardFixture) { delete(f.auth, "bot_id") }},
		{"missing bot user", func(f *guardFixture) { delete(f.auth, "user_id") }},
		{"auth rejected", func(f *guardFixture) { f.auth["ok"] = false }},
		{"channel rejected", func(f *guardFixture) { f.info["ok"] = false }},
		{"wrong channel", func(f *guardFixture) { f.info["channel"].(map[string]any)["id"] = "C999" }},
		{"unexpected human", func(f *guardFixture) { f.pages[""] = members([]string{"UHUMAN", "UBOT", "UOTHER"}, "") }},
		{"missing human", func(f *guardFixture) { f.pages[""] = members([]string{"UBOT"}, "") }},
		{"missing bot", func(f *guardFixture) { f.pages[""] = members([]string{"UHUMAN"}, "") }},
		{"members rejected", func(f *guardFixture) { f.pages[""] = map[string]any{"ok": false} }},
		{"unknown pagination", func(f *guardFixture) { f.pages[""] = map[string]any{"ok": true, "members": []string{"UHUMAN", "UBOT"}} }},
		{"missing cursor", func(f *guardFixture) {
			f.pages[""] = map[string]any{"ok": true, "members": []string{"UHUMAN", "UBOT"}, "response_metadata": map[string]any{}}
		}},
		{"cursor loop", func(f *guardFixture) {
			f.pages[""] = members([]string{"UHUMAN"}, "again")
			f.pages["again"] = members([]string{"UBOT"}, "again")
		}},
		{"pagination extra human", func(f *guardFixture) {
			f.pages[""] = members([]string{"UHUMAN", "UBOT"}, "next")
			f.pages["next"] = members([]string{"UOTHER"}, "")
		}},
		{"HTTP failure", func(f *guardFixture) { f.status = 503 }},
	}
	for _, flag := range []string{"is_private", "is_general", "is_shared", "is_ext_shared", "is_org_shared", "is_im", "is_mpim", "is_archived"} {
		flag := flag
		cases = append(cases,
			struct {
				name   string
				change func(*guardFixture)
			}{"unsafe " + flag, func(f *guardFixture) { f.info["channel"].(map[string]any)[flag] = flag != "is_private" }},
			struct {
				name   string
				change func(*guardFixture)
			}{"missing " + flag, func(f *guardFixture) { delete(f.info["channel"].(map[string]any), flag) }},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGuardFixture()
			tc.change(f)
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			client := guardClient(server)
			_, err := client.Send(context.Background(), fixture())
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) || sendErr.Category != "destination_guard_rejected" || !sendErr.Permanent || sendErr.Ambiguous {
				t.Fatalf("unexpected error %v", err)
			}
			if f.posts != 0 {
				t.Fatal("unsafe destination received a post")
			}
			if strings.Contains(err.Error(), "synthetic-token") {
				t.Fatal("guard exposed credential")
			}
		})
	}
}

func TestGuardVerifiesEveryAttemptAndAllPages(t *testing.T) {
	f := newGuardFixture()
	f.pages[""] = members([]string{"UHUMAN"}, "next")
	f.pages["next"] = members([]string{"UBOT"}, "")
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	defer server.Close()
	client := guardClient(server)
	if err := client.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.posts != 0 {
		t.Fatal("verification wrote a message")
	}
	if _, err := client.Send(context.Background(), fixture()); err != nil {
		t.Fatal(err)
	}
	f.pages["next"] = members([]string{"UBOT", "UOTHER"}, "")
	if _, err := client.Send(context.Background(), fixture()); err == nil {
		t.Fatal("membership change not checked")
	}
	if f.posts != 1 || f.authCalls != 3 {
		t.Fatalf("posts=%d auth calls=%d", f.posts, f.authCalls)
	}
}

func TestGuardRequiresCompleteConfiguration(t *testing.T) {
	for _, client := range []Client{
		{Workspace: "T123"}, {AllowedUsers: []string{"UHUMAN"}},
		{Token: "synthetic-token", Channel: "C123", Workspace: "T123", AllowedUsers: []string{"UHUMAN", "UHUMAN"}},
		{Token: "synthetic-token", Channel: "C123", Workspace: "T123", AllowedUsers: []string{"invalid"}},
	} {
		client.HTTP = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })}
		if err := client.Verify(context.Background()); err == nil {
			t.Fatal("incomplete config accepted")
		}
	}
}

func TestGuardSanitizesMalformedAndTransportFailures(t *testing.T) {
	for _, body := range []string{"sensitive-token", strings.Repeat("s", responseLimit+1), `{"ok":true} trailing`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
		client := guardClient(server)
		err := client.Verify(context.Background())
		server.Close()
		if err == nil || strings.Contains(err.Error(), "sensitive-token") {
			t.Fatalf("unexpected result %v", err)
		}
	}
	client := Client{Token: "synthetic-token", Channel: "C123", Workspace: "T123", AllowedUsers: []string{"UHUMAN"}, HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("sensitive-token") })}}
	if err := client.Verify(context.Background()); err == nil || strings.Contains(err.Error(), "sensitive-token") {
		t.Fatalf("unexpected result %v", err)
	}
}

func TestGuardDoesNotFollowRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := guardClient(server)
	if err := client.Verify(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected {
		t.Fatal("verification forwarded the token")
	}
}

func TestGuardRejectsUnknownFlagsAndIncompletePages(t *testing.T) {
	cases := []struct {
		name   string
		change func(*guardFixture)
	}{
		{"wrong flag type", func(f *guardFixture) { f.info["channel"].(map[string]any)["is_shared"] = "false" }},
		{"null flag", func(f *guardFixture) { f.info["channel"].(map[string]any)["is_shared"] = nil }},
		{"duplicate member", func(f *guardFixture) { f.pages[""] = members([]string{"UHUMAN", "UHUMAN", "UBOT"}, "") }},
		{"unknown later page", func(f *guardFixture) { f.pages[""] = members([]string{"UHUMAN"}, "unknown") }},
		{"missing members", func(f *guardFixture) {
			f.pages[""] = map[string]any{"ok": true, "response_metadata": map[string]any{"next_cursor": ""}}
		}},
		{"bot as allowed human", func(f *guardFixture) { f.auth["user_id"] = "UHUMAN" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGuardFixture()
			tc.change(f)
			server := httptest.NewServer(http.HandlerFunc(f.serve))
			defer server.Close()
			client := guardClient(server)
			if _, err := client.Send(context.Background(), fixture()); err == nil {
				t.Fatal("unsafe guard response accepted")
			}
			if f.posts != 0 {
				t.Fatal("unsafe destination received a post")
			}
		})
	}
}
