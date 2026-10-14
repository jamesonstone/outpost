// Package slack implements the allowlisted Slack delivery gateway.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
)

const defaultEndpoint = "https://slack.com/api/chat.postMessage"
const requestTimeout = 15 * time.Second
const responseLimit = 64 * 1024

var channelID = regexp.MustCompile(`^[CG][A-Z0-9]+$`)
var timestamp = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
var escape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Client sends one attempt. The dispatcher owns durable retries and ambiguity.
// Endpoint is an explicit HTTP fixture override; production uses the default.
type Client struct {
	Token    string
	Channel  string
	HTTP     *http.Client
	Endpoint string
	// Workspace and AllowedUsers enable strict private-destination verification.
	Workspace    string
	AllowedUsers []string
	// APIBase overrides the verification API base only for HTTP fixtures.
	APIBase string
}

var _ core.Destination = (*Client)(nil)

type message struct {
	Channel        string `json:"channel"`
	Text           string `json:"text"`
	Parse          string `json:"parse"`
	LinkNames      bool   `json:"link_names"`
	UnfurlLinks    bool   `json:"unfurl_links"`
	UnfurlMedia    bool   `json:"unfurl_media"`
	UnfurlAppLinks bool   `json:"unfurl_app_links"`
}

type response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

func (c *Client) Send(ctx context.Context, event core.Event) (core.Receipt, error) {
	if c.Token == "" || !channelID.MatchString(c.Channel) {
		return core.Receipt{}, failure("invalid_configuration", true, false)
	}
	text, ok := render(event)
	if !ok {
		return core.Receipt{}, failure("invalid_payload", true, false)
	}
	if ctx.Err() != nil {
		return core.Receipt{}, failure("canceled", false, false)
	}
	if err := c.Verify(ctx); err != nil {
		return core.Receipt{}, err
	}
	payload, _ := json.Marshal(message{Channel: c.Channel, Text: text, Parse: "none"})
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return core.Receipt{}, failure("invalid_configuration", true, false)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := http.Client{Timeout: requestTimeout}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	if client.Timeout <= 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	// Never follow a redirect that could expose the token or change the destination.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return core.Receipt{}, failure("transport_error", false, true)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return core.Receipt{}, &core.SendError{Category: "rate_limited", RetryAfter: retryAfter(res.Header.Get("Retry-After"))}
	}
	if res.StatusCode >= 500 || res.StatusCode == http.StatusRequestTimeout {
		return core.Receipt{}, failure("upstream_error", false, true)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && res.StatusCode != http.StatusOK {
		return core.Receipt{}, failure("invalid_response", false, true)
	}
	if res.StatusCode != http.StatusOK {
		return core.Receipt{}, failure("http_rejected", true, false)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
	if err != nil || len(body) > responseLimit {
		return core.Receipt{}, failure("invalid_response", false, true)
	}
	var result response
	if json.Unmarshal(body, &result) != nil {
		return core.Receipt{}, failure("invalid_response", false, true)
	}
	if !result.OK {
		return core.Receipt{}, classify(result.Error, res.Header.Get("Retry-After"))
	}
	if result.Channel != c.Channel || !timestamp.MatchString(result.TS) {
		return core.Receipt{}, failure("invalid_receipt", false, true)
	}
	return core.Receipt{Channel: result.Channel, Timestamp: result.TS}, nil
}

func render(e core.Event) (string, bool) {
	if e.OrderID == "" || e.Partner == "" || e.PlacedAt.IsZero() {
		return "", false
	}
	for _, value := range []string{e.OrderID, e.Partner, e.Link} {
		if len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
			return "", false
		}
	}
	link, err := url.Parse(e.Link)
	if err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil || link.Fragment != "" {
		return "", false
	}
	// A pipe must not become a Slack link-label separator.
	safeLink := strings.ReplaceAll(escape.Replace(link.String()), "|", "%7C")
	return "Order ID: " + escape.Replace(e.OrderID) + "\nPartner: " + escape.Replace(e.Partner) +
		"\nPlaced at: " + e.PlacedAt.UTC().Format(time.RFC3339Nano) + "\n<" + safeLink + "|View order>", true
}

func failure(category string, permanent, ambiguous bool) *core.SendError {
	return &core.SendError{Category: category, Permanent: permanent, Ambiguous: ambiguous}
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	// Reject overflow; do not replace a valid long server delay with a shorter retry.
	if err == nil && seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
		return time.Duration(seconds) * time.Second
	}
	return time.Minute
}

func classify(code, delay string) *core.SendError {
	switch code {
	case "ratelimited", "rate_limited":
		return &core.SendError{Category: "rate_limited", RetryAfter: retryAfter(delay)}
	case "internal_error", "fatal_error", "request_timeout", "service_unavailable":
		return failure("upstream_error", false, true)
	case "invalid_auth", "not_authed", "token_revoked", "account_inactive":
		return failure("authentication_rejected", true, false)
	case "missing_scope", "not_in_channel", "channel_not_found", "is_archived", "restricted_action":
		return failure("destination_rejected", true, false)
	case "":
		return failure("invalid_response", false, true)
	default:
		return failure("slack_rejected", true, false)
	}
}
