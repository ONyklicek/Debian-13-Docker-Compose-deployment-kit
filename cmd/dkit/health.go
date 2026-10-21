package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type composeContainerStatus struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Status  string `json:"Status"`
}

type appHealthReport struct {
	App        string                   `json:"app"`
	Ready      bool                     `json:"ready"`
	Containers []composeContainerStatus `json:"containers"`
	Error      string                   `json:"error,omitempty"`
}

func parseComposeStatuses(data []byte) ([]composeContainerStatus, error) {
	var array []composeContainerStatus
	if err := json.Unmarshal(data, &array); err == nil {
		return array, nil
	}

	var statuses []composeContainerStatus
	for lineNumber, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var status composeContainerStatus
		if err := json.Unmarshal(line, &status); err != nil {
			return nil, fmt.Errorf("parse Compose container status line %d: %w", lineNumber+1, err)
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func inspectAppHealth(app string, data []byte) (appHealthReport, error) {
	report := appHealthReport{App: app}
	statuses, err := parseComposeStatuses(data)
	if err != nil {
		return report, err
	}
	report.Containers = statuses
	if len(statuses) == 0 {
		report.Error = fmt.Sprintf("app %q has no Compose containers; run `dkit app compose up %s` first", app, app)
		return report, errors.New(report.Error)
	}

	hasApp, probeFound, probeReady, ready := false, false, true, true
	for _, status := range statuses {
		service := strings.TrimSpace(status.Service)
		state := strings.ToLower(strings.TrimSpace(status.State))
		health := strings.ToLower(strings.TrimSpace(status.Health))
		if service == "" {
			report.Error = "Compose returned a container without a service name"
			return report, errors.New(report.Error)
		}
		if state != "running" || (health != "" && health != "healthy") {
			ready = false
		}
		if service == "app" && state == "running" {
			hasApp = true
		}
		if service == "health-probe" {
			probeFound = true
			probeReady = probeReady && state == "running" && health == "healthy"
		}
	}
	if !hasApp {
		report.Error = fmt.Sprintf("app %q is not ready: no running app service", app)
		return report, errors.New(report.Error)
	}
	if !probeFound || !probeReady {
		report.Error = fmt.Sprintf("app %q is not ready: health-probe is missing or has not passed its HTTP check", app)
		return report, errors.New(report.Error)
	}
	if !ready {
		report.Error = fmt.Sprintf("app %q has one or more stopped or unhealthy Compose services", app)
		return report, errors.New(report.Error)
	}
	report.Ready = true
	return report, nil
}

func printAppHealth(output io.Writer, report appHealthReport) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SERVICE\tSTATE\tHEALTH\tSTATUS"); err != nil {
		return err
	}
	for _, status := range report.Containers {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", status.Service,
			defaultText(strings.ToLower(strings.TrimSpace(status.State)), "(unknown)"),
			defaultText(strings.ToLower(strings.TrimSpace(status.Health)), "(no healthcheck)"), status.Status); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if report.Error != "" {
		_, err := fmt.Fprintln(output, report.Error)
		return err
	}
	_, err := fmt.Fprintf(output, "App %q and its HTTP health probe are ready. This does not verify public routing or blue/green state.\n", report.App)
	return err
}
