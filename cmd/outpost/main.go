package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jamesonstone/outpost/internal/config"
	"github.com/jamesonstone/outpost/internal/core"
	"github.com/jamesonstone/outpost/internal/service"
	"github.com/jamesonstone/outpost/internal/slack"
	"github.com/jamesonstone/outpost/internal/source"
	"github.com/jamesonstone/outpost/internal/state"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	flags := flag.NewFlagSet("outpost", flag.ContinueOnError)
	path := flags.String("config", "outpost.json", "nonsecret configuration file")
	command := flags.String("command", "run", "run, inspect, status, attempts, retry, backup, validate, healthcheck")
	key := flags.String("key", "", "logical delivery key for attempts or retry")
	backup := flags.String("backup", "", "new backup file path")
	limit := flags.Int("limit", 100, "inspection limit (1-1000)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *command == "healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get("http://" + c.Listen + "/healthz")
		if err != nil {
			return errors.New("health check failed")
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return errors.New("health check failed")
		}
		return nil
	}
	query, err := os.ReadFile(c.QueryFile)
	if err != nil {
		return errors.New("projection file open failed")
	}
	if err := source.ValidateProjection(string(query)); err != nil {
		return err
	}
	if *command == "validate" {
		fmt.Println("configuration valid; no external connections made")
		return nil
	}
	var pg *source.Postgres
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *command == "run" {
		if c.DispatchEnabled && os.Getenv("OUTPOST_SLACK_TOKEN") == "" {
			return errors.New("Slack token required for enabled dispatch")
		}
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		pg, err = source.New(connectCtx, os.Getenv("OUTPOST_POSTGRES_DSN"), string(query))
		if err == nil {
			upper := time.Now().UTC()
			if upper.Before(c.Activation) {
				upper = c.Activation
			}
			_, err = pg.Page(connectCtx, c.Activation, upper, core.Cursor{}, 1)
		}
		cancel()
		if err != nil {
			if pg != nil {
				pg.Close()
			}
			return err
		}
		defer pg.Close()
	} else {
		if _, err := os.Stat(c.StatePath); err != nil {
			return errors.New("existing state file required")
		}
	}
	st, err := state.Open(c.StatePath, c.Policy(string(query)))
	if err != nil {
		return err
	}
	defer st.Close()
	switch *command {
	case "status":
		stats, err := st.Stats(time.Now().UTC())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(stats)
	case "inspect":
		records, err := st.List(*limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(records)
	case "attempts":
		records, err := st.Attempts(*key)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(records)
	case "retry":
		return st.Retry(*key, time.Now().UTC())
	case "backup":
		if *backup == "" {
			return errors.New("backup path required")
		}
		if _, err := os.Stat(*backup); !os.IsNotExist(err) {
			return errors.New("backup path must be new")
		}
		return st.Backup(*backup)
	case "run":
	default:
		return errors.New("unknown command")
	}
	destination := &slack.Client{Token: os.Getenv("OUTPOST_SLACK_TOKEN"), Channel: c.Channel, Workspace: c.WorkspaceID, AllowedUsers: c.AllowedUserIDs}
	if c.DispatchEnabled {
		guardCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := destination.Verify(guardCtx)
		cancel()
		if err != nil {
			return err
		}
	}
	if err := st.BeginRun(time.Now().UTC()); err != nil {
		return err
	}
	activation, err := st.EffectiveActivation()
	if err != nil {
		return err
	}
	e := &service.Engine{Store: st, Source: pg, Destination: destination, Activation: activation, PageSize: c.PageSize, Enabled: c.DispatchEnabled, Lease: time.Minute, RequestTimeout: 15 * time.Second}
	return serve(ctx, e, c)
}
