package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
)

func fixture() core.Event {
	return core.Event{ID: "publication-secret", Tenant: "tenant-secret", Environment: "production", OrderID: "order-1", Partner: "partner-1", PlacedAt: time.Date(2026, 10, 14, 11, 0, 0, 0, time.FixedZone("offset", 3600)), Link: "https://orders.example.test/order-1"}
}

func TestSendAllowlistedEscapedPayload(t *testing.T) {
	event := fixture()
	event.OrderID = "order<&> <!here> <!channel> <!everyone>"
	event.Partner = "partner<&> <@U123> @everyone"
	event.Link = "https://orders.example.test/order?x=a&y=b|c"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("incorrect request authentication or method")
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Error("incorrect content type")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload) != 7 {
			t.Errorf("unexpected payload fields: %d", len(payload))
		}
		if payload["channel"] != "C123" || payload["parse"] != "none" {
			t.Error("destination or parsing changed")
		}
		for _, key := range []string{"link_names", "unfurl_links", "unfurl_media", "unfurl_app_links"} {
			if payload[key] != false {
				t.Errorf("%s must be false", key)
			}
		}
		text, _ := payload["text"].(string)
		for _, want := range []string{"order&lt;&amp;&gt; &lt;!here&gt; &lt;!channel&gt; &lt;!everyone&gt;", "partner&lt;&amp;&gt; &lt;@U123&gt;", "2026-10-14T10:00:00Z", "x=a&amp;y=b%7Cc|View order>"} {
			if !strings.Contains(text, want) {
				t.Errorf("missing escaped payload element %q", want)
			}
		}
		for _, excluded := range []string{"publication-secret", "tenant-secret", "synthetic-token", "<!here>", "<!channel>", "<!everyone>", "<@U123>"} {
			if strings.Contains(text, excluded) {
				t.Errorf("payload contains excluded field %q", excluded)
			}
		}
		io.WriteString(w, `{"ok":true,"channel":"C123","ts":"123.456"}`)
	}))
	defer server.Close()
	client := Client{Token: "synthetic-token", Channel: "C123", Endpoint: server.URL, HTTP: server.Client()}
	receipt, err := client.Send(context.Background(), event)
	if err != nil || receipt != (core.Receipt{Channel: "C123", Timestamp: "123.456"}) {
		t.Fatalf("receipt=%+v error=%v", receipt, err)
	}
}

func TestSendClassifiesFailures(t *testing.T) {
	cases := []struct {
		name                 string
		status               int
		body, category       string
		permanent, ambiguous bool
		retry                time.Duration
	}{
		{"rate limit", 429, `sensitive body`, "rate_limited", false, false, 17 * time.Second},
		{"request timeout", 408, `sensitive body`, "upstream_error", false, true, 0},
		{"unexpected success", 204, ``, "invalid_response", false, true, 0},
		{"server failure", 503, `sensitive body`, "upstream_error", false, true, 0},
		{"HTTP rejection", 403, `sensitive body`, "http_rejected", true, false, 0},
		{"malformed", 200, `sensitive body`, "invalid_response", false, true, 0},
		{"trailing JSON", 200, `{"ok":true,"channel":"C123","ts":"123.456"}{}`, "invalid_response", false, true, 0},
		{"wrong channel", 200, `{"ok":true,"channel":"C999","ts":"123.456"}`, "invalid_receipt", false, true, 0},
		{"missing timestamp", 200, `{"ok":true,"channel":"C123"}`, "invalid_receipt", false, true, 0},
		{"invalid timestamp", 200, `{"ok":true,"channel":"C123","ts":"sensitive body"}`, "invalid_receipt", false, true, 0},
		{"missing ok", 200, `{}`, "invalid_response", false, true, 0},
		{"auth rejection", 200, `{"ok":false,"error":"invalid_auth"}`, "authentication_rejected", true, false, 0},
		{"destination rejection", 200, `{"ok":false,"error":"not_in_channel"}`, "destination_rejected", true, false, 0},
		{"transient rejection", 200, `{"ok":false,"error":"internal_error"}`, "upstream_error", false, true, 0},
		{"API rate limit", 200, `{"ok":false,"error":"ratelimited"}`, "rate_limited", false, false, 17 * time.Second},
		{"unknown rejection", 200, `{"ok":false,"error":"sensitive body"}`, "slack_rejected", true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := Client{Token: "synthetic-token", Channel: "C123", Endpoint: server.URL, HTTP: server.Client()}
			receipt, err := client.Send(context.Background(), fixture())
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) {
				t.Fatalf("expected SendError, got %v", err)
			}
			if receipt != (core.Receipt{}) || sendErr.Category != tc.category || sendErr.Permanent != tc.permanent || sendErr.Ambiguous != tc.ambiguous || sendErr.RetryAfter != tc.retry {
				t.Fatalf("receipt=%+v error=%+v", receipt, sendErr)
			}
			if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "synthetic-token") {
				t.Fatal("upstream data exposed")
			}
		})
	}
}

func TestSendRejectsInvalidPayloadWithoutNetwork(t *testing.T) {
	cases := []struct {
		name   string
		change func(*core.Event)
	}{
		{"missing order", func(e *core.Event) { e.OrderID = "" }},
		{"missing partner", func(e *core.Event) { e.Partner = "" }},
		{"missing time", func(e *core.Event) { e.PlacedAt = time.Time{} }},
		{"newline", func(e *core.Event) { e.OrderID = "order\nspoof" }},
		{"long field", func(e *core.Event) { e.Partner = strings.Repeat("a", 2049) }},
		{"non-HTTPS link", func(e *core.Event) { e.Link = "http://orders.example.test/order-1" }},
		{"credential link", func(e *core.Event) { e.Link = "https://secret@orders.example.test/order-1" }},
		{"fragment link", func(e *core.Event) { e.Link = "https://orders.example.test/order-1#secret" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := fixture()
			tc.change(&event)
			client := Client{Token: "synthetic-token", Channel: "C123", HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil })}}
			_, err := client.Send(context.Background(), event)
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) || sendErr.Category != "invalid_payload" || !sendErr.Permanent || sendErr.Ambiguous {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendTransportFailuresAreAmbiguousAndSanitized(t *testing.T) {
	client := Client{Token: "synthetic-token", Channel: "C123", HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic-token sensitive body") })}}
	_, err := client.Send(context.Background(), fixture())
	var sendErr *core.SendError
	if !errors.As(err, &sendErr) || sendErr.Category != "transport_error" || !sendErr.Ambiguous || sendErr.Permanent {
		t.Fatalf("unexpected error %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Send(ctx, fixture())
	if !errors.As(err, &sendErr) || sendErr.Category != "canceled" || sendErr.Ambiguous {
		t.Fatalf("unexpected cancellation %v", err)
	}
}

func TestSendRejectsRedirectAndOversizedResponse(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(map[bool]string{false: "oversized", true: "redirect"}[redirect], func(t *testing.T) {
			redirected := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if redirect {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				io.WriteString(w, strings.Repeat("a", responseLimit+1))
			}))
			defer server.Close()
			client := Client{Token: "synthetic-token", Channel: "C123", Endpoint: server.URL, HTTP: server.Client()}
			_, err := client.Send(context.Background(), fixture())
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) || sendErr.Ambiguous == redirect || sendErr.Permanent != redirect {
				t.Fatalf("unexpected error %v", err)
			}
			if redirected {
				t.Fatal("redirect forwarded the request")
			}
		})
	}
}

func TestSendTimeoutBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		io.WriteString(w, `{"ok":true,"channel":"C123","ts":"123.456"}`)
	}))
	defer server.Close()
	for _, callerTimeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP timeout", true: "context deadline"}[callerTimeout], func(t *testing.T) {
			client := Client{Token: "synthetic-token", Channel: "C123", Endpoint: server.URL, HTTP: &http.Client{Timeout: 5 * time.Millisecond}}
			ctx := context.Background()
			if callerTimeout {
				client.HTTP.Timeout = time.Second
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 5*time.Millisecond)
				defer cancel()
			}
			start := time.Now()
			_, err := client.Send(ctx, fixture())
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) || !sendErr.Ambiguous || sendErr.Permanent {
				t.Fatalf("unexpected error %v", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("timeout not bounded")
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "nonsense", "9223372036854775807"} {
		if got := retryAfter(value); got != time.Minute {
			t.Errorf("value %q got %s", value, got)
		}
	}
	if got := retryAfter(" 120 "); got != 2*time.Minute {
		t.Errorf("got %s", got)
	}
}
