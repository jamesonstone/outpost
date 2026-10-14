// Package config parses nonsecret policy. Secrets are runtime environment values.
package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/jamesonstone/outpost/internal/core"
)

type Config struct {
	AudiencePolicy  string    `json:"audience_policy"`
	NewStatePolicy  string    `json:"new_state_policy"`
	WorkspaceID     string    `json:"workspace_id"`
	AllowedUserIDs  []string  `json:"allowed_user_ids"`
	StatePath       string    `json:"state_path"`
	SourceID        string    `json:"source_id"`
	QueryFile       string    `json:"query_file"`
	Partner         string    `json:"partner"`
	Channel         string    `json:"channel"`
	Activation      time.Time `json:"activation"`
	DispatchEnabled bool      `json:"dispatch_enabled"`
	PageSize        int       `json:"page_size"`
	PollSeconds     int       `json:"poll_seconds"`
	Listen          string    `json:"listen"`
}

func Load(path string) (c Config, err error) {
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("configuration open failed")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, errors.New("configuration decode failed")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("configuration has trailing content")
	}
	if c.PageSize == 0 {
		c.PageSize = 500
	}
	if c.PollSeconds == 0 {
		c.PollSeconds = 60
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.StatePath == "" || c.SourceID == "" || c.QueryFile == "" || c.Partner == "" || c.Activation.IsZero() {
		return c, errors.New("required configuration missing")
	}
	if !regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`).MatchString(c.Channel) {
		return c, errors.New("invalid pinned Slack channel")
	}
	if c.PageSize < 1 || c.PageSize > 1000 || c.PollSeconds < 1 || c.PollSeconds > 120 {
		return c, errors.New("invalid scan bounds")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return c, errors.New("health listener must use a loopback IP")
	}
	if c.NewStatePolicy != "" && c.NewStatePolicy != "from_startup" {
		return c, errors.New("invalid new-state policy")
	}
	if err := c.validateAudience(); err != nil {
		return c, err
	}
	c.Activation = c.Activation.UTC()
	return c, nil
}
func (c Config) Policy(query string) core.Policy {
	// Bind the projection contract as well as the operator-assigned source identity.
	h := sha256.Sum256([]byte(query))
	return core.Policy{Source: fmt.Sprintf("%s:%x", c.SourceID, h), Channel: c.Channel, Partner: c.Partner, Activation: c.Activation, NewStatePolicy: c.NewStatePolicy}
}

func (c Config) validateAudience() error {
	if c.AudiencePolicy != "" && c.AudiencePolicy != "private_allowlist" && c.AudiencePolicy != "workspace_public" {
		return errors.New("invalid Slack audience policy")
	}
	if c.AudiencePolicy == "" && c.WorkspaceID == "" && len(c.AllowedUserIDs) == 0 {
		return nil
	}
	if !regexp.MustCompile(`^T[A-Z0-9]+$`).MatchString(c.WorkspaceID) {
		return errors.New("pinned workspace required for Slack audience policy")
	}
	if c.AudiencePolicy == "workspace_public" {
		if len(c.AllowedUserIDs) != 0 {
			return errors.New("public Slack audience conflicts with allowed users")
		}
		return nil
	}
	if len(c.AllowedUserIDs) == 0 {
		return errors.New("workspace and allowed users required together")
	}
	seen := map[string]bool{}
	for _, id := range c.AllowedUserIDs {
		if !regexp.MustCompile(`^[UW][A-Z0-9]+$`).MatchString(id) || seen[id] {
			return errors.New("invalid allowed user IDs")
		}
		seen[id] = true
	}
	return nil
}
