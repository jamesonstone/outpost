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
	c.Activation = c.Activation.UTC()
	return c, nil
}
func (c Config) Policy(query string) core.Policy {
	// Bind the projection contract as well as the operator-assigned source identity.
	h := sha256.Sum256([]byte(query))
	return core.Policy{Source: fmt.Sprintf("%s:%x", c.SourceID, h), Channel: c.Channel, Partner: c.Partner, Activation: c.Activation}
}
