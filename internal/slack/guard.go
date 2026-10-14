package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var userID = regexp.MustCompile(`^[UW][A-Z0-9]+$`)
var workspaceID = regexp.MustCompile(`^T[A-Z0-9]+$`)

type authentication struct {
	OK   bool   `json:"ok"`
	Team string `json:"team_id"`
	User string `json:"user_id"`
	Bot  string `json:"bot_id"`
}

type channelInfo struct {
	OK      bool `json:"ok"`
	Channel struct {
		ID        string `json:"id"`
		Private   *bool  `json:"is_private"`
		General   *bool  `json:"is_general"`
		Shared    *bool  `json:"is_shared"`
		External  *bool  `json:"is_ext_shared"`
		OrgShared *bool  `json:"is_org_shared"`
		IM        *bool  `json:"is_im"`
		MPIM      *bool  `json:"is_mpim"`
		Archived  *bool  `json:"is_archived"`
	} `json:"channel"`
}

type memberPage struct {
	OK       bool      `json:"ok"`
	Members  *[]string `json:"members"`
	Metadata *struct {
		Cursor *string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// Verify rejects unsafe destinations before any write. Verification is repeated
// for every attempt so a changed channel membership cannot use a cached approval.
// With neither guard field configured, legacy clients retain their behavior.
func (c *Client) Verify(ctx context.Context) error {
	if c.Workspace == "" && len(c.AllowedUsers) == 0 {
		return nil
	}
	reject := failure("destination_guard_rejected", true, false)
	if c.Token == "" || !workspaceID.MatchString(c.Workspace) || !channelID.MatchString(c.Channel) || len(c.AllowedUsers) == 0 {
		return reject
	}
	allowed := make(map[string]bool, len(c.AllowedUsers)+1)
	for _, id := range c.AllowedUsers {
		if !userID.MatchString(id) || allowed[id] {
			return reject
		}
		allowed[id] = true
	}
	var auth authentication
	if c.guardRead(ctx, "auth.test", nil, &auth) != nil || !auth.OK || auth.Team != c.Workspace || !userID.MatchString(auth.User) || auth.Bot == "" || allowed[auth.User] {
		return reject
	}
	allowed[auth.User] = true
	var info channelInfo
	if c.guardRead(ctx, "conversations.info", url.Values{"channel": {c.Channel}}, &info) != nil || !info.OK || info.Channel.ID != c.Channel || info.Channel.Private == nil || !*info.Channel.Private {
		return reject
	}
	for _, flag := range []*bool{info.Channel.General, info.Channel.Shared, info.Channel.External, info.Channel.OrgShared, info.Channel.IM, info.Channel.MPIM, info.Channel.Archived} {
		if flag == nil || *flag {
			return reject
		}
	}
	seenMembers := make(map[string]bool, len(allowed))
	seenCursors := make(map[string]bool)
	cursor := ""
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		args := url.Values{"channel": {c.Channel}, "limit": {"200"}}
		if cursor != "" {
			args.Set("cursor", cursor)
		}
		var page memberPage
		if c.guardRead(ctx, "conversations.members", args, &page) != nil || !page.OK || page.Members == nil || page.Metadata == nil || page.Metadata.Cursor == nil {
			return reject
		}
		for _, member := range *page.Members {
			if !allowed[member] || seenMembers[member] {
				return reject
			}
			seenMembers[member] = true
		}
		cursor = *page.Metadata.Cursor
		if cursor == "" {
			if len(seenMembers) != len(allowed) {
				return reject
			}
			return nil
		}
		if strings.TrimSpace(cursor) != cursor || seenCursors[cursor] {
			return reject
		}
		seenCursors[cursor] = true
	}
	return reject
}

func (c *Client) guardRead(ctx context.Context, method string, args url.Values, target any) error {
	reject := failure("destination_guard_rejected", true, false)
	base := c.APIBase
	if base == "" {
		base = "https://slack.com/api/"
	}
	endpoint := strings.TrimRight(base, "/") + "/" + method
	if len(args) != 0 {
		endpoint += "?" + args.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return reject
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	client := http.Client{Timeout: requestTimeout}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	if client.Timeout <= 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return reject
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return reject
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
	if err != nil || len(body) > responseLimit || json.Unmarshal(body, target) != nil {
		return reject
	}
	return nil
}
