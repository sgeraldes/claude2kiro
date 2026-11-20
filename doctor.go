package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/diagnostics"
	"github.com/sgeraldes/claude2kiro/internal/kiroide"
	"github.com/sgeraldes/claude2kiro/internal/models"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tokenfile"
	"github.com/sgeraldes/claude2kiro/internal/tui/menu"
)

// doctorOptions are the parts of a doctor run a test replaces. Paths come from
// the same helpers the proxy uses (profile-aware), so a test points them at a
// temporary home through USERPROFILE/HOME.
type doctorOptions struct {
	Now          time.Time
	Offline      bool // skip the live model catalogue call
	LocalAppData string
	HealthClient *http.Client
	FindClaude   func() (string, error)
	// FetchModelIDs calls ListAvailableModels with a token as it is on disk.
	FetchModelIDs func(cfg *config.Config, accessToken, profileArn string) ([]string, error)
}

// doctorCatalogTimeout bounds the catalogue call so a diagnostic never hangs
// on the network.
const doctorCatalogTimeout = 15 * time.Second

func defaultDoctorOptions() doctorOptions {
	return doctorOptions{
		Now:           time.Now(),
		LocalAppData:  os.Getenv("LOCALAPPDATA"),
		HealthClient:  &http.Client{Timeout: 2 * time.Second},
		FindClaude:    findClaudeBinary,
		FetchModelIDs: fetchModelIDsForDoctor,
	}
}

func fetchModelIDsForDoctor(cfg *config.Config, accessToken, profileArn string) ([]string, error) {
	timeout := cfg.Network.HTTPTimeout
	if timeout <= 0 || timeout > doctorCatalogTimeout {
		timeout = doctorCatalogTimeout
	}
	list, err := models.Fetch(cfg.Advanced.CodeWhispererEndpoint, accessToken, profileArn, kiroide.UserAgent(), timeout)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ModelID)
	}
	return ids, nil
}

// collectDoctorReport gathers the read-only health report. It never writes a
// file, never refreshes a token and never sends a model request; the only
// network calls are GET /health on the local proxy and, unless Offline,
// ListAvailableModels, which costs no credits.
func collectDoctorReport(opts doctorOptions) diagnostics.Report {
	report := diagnostics.Report{GeneratedAt: opts.Now, Version: menu.Version, Profile: profile.Label()}
	add := func(c diagnostics.Check) { report.Checks = append(report.Checks, c) }

	// Config: read it fresh so a parse error is reported, not hidden behind
	// the defaults config.Get() falls back to.
	homeDir, _ := os.UserHomeDir()
	cfgPath := profile.ConfigReadPath(homeDir)
	cfg, loadErr := config.Load()
	exists, statErr := FileExists(cfgPath)
	add(diagnostics.ConfigCheck(cfgPath, exists, statErr, loadErr))

	// Token of the active identity, and of every quota-failover profile.
	tokenCheck := func(name, label string, fallback bool) (diagnostics.Check, diagnostics.TokenFacts) {
		path := tokenFilePathFor(name)
		data, err := tokenfile.ReadFile(path)
		id := "kiro_token"
		if name != profile.Active() {
			id = "kiro_token_" + name
		}
		return diagnostics.TokenCheck(id, label, data, err, errors.Is(err, os.ErrNotExist), opts.Now, fallback)
	}
	check, facts := tokenCheck(profile.Active(), "Kiro token", false)
	add(check)
	for _, name := range cfg.Auth.FallbackProfiles {
		if name == profile.Active() {
			continue
		}
		fb, _ := tokenCheck(name, "Fallback "+name, true)
		add(fb)
	}

	add(doctorCatalogCheck(opts, cfg, facts))

	proxy, proxyRunning := diagnostics.ProxyCheck(probeProxy(opts.HealthClient))
	add(proxy)
	add(doctorClaudeCodeCheck(opts, cfg, proxyRunning))
	add(doctorDesktopCheck(opts))
	add(diagnostics.RequestSettingsCheck(cfg.Advanced))
	return report
}

func doctorCatalogCheck(opts doctorOptions, cfg *config.Config, facts diagnostics.TokenFacts) diagnostics.Check {
	switch {
	case opts.Offline:
		return diagnostics.CatalogCheck(diagnostics.CatalogOffline, nil, nil)
	case !facts.Usable:
		return diagnostics.CatalogCheck("no usable access token", nil, nil)
	}
	ids, err := opts.FetchModelIDs(cfg, facts.AccessToken, facts.ProfileArn)
	if err != nil {
		err = errors.New(redact(err.Error(), facts.AccessToken, facts.ProfileArn))
	}
	return diagnostics.CatalogCheck("", ids, err)
}

// redact removes secrets an error message could carry (the request URL holds
// the profile ARN).
func redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	return s
}

// probeProxy reads the port file of this profile and asks /health, the same
// way `run` decides whether to attach to a running proxy.
func probeProxy(client *http.Client) diagnostics.ProxyProbe {
	p := diagnostics.ProxyProbe{
		PortFile:        proxyPortFilePath(),
		ExpectedProfile: profile.Label(),
		DefaultProfile:  profile.Name() == "",
	}
	data, err := os.ReadFile(p.PortFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		p.PortFileMissing = true
		return p
	case err != nil:
		p.PortFileErr = err
		return p
	}
	p.Port = strings.TrimSpace(string(data))
	if p.Port == "" {
		return p
	}
	resp, err := client.Get("http://127.0.0.1:" + p.Port + "/health")
	if err != nil {
		p.ProbeErr = err
		return p
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	p.Status = resp.StatusCode
	p.ServedProfile = resp.Header.Get(profileHeader)
	return p
}

func doctorClaudeCodeCheck(opts doctorOptions, cfg *config.Config, proxyRunning bool) diagnostics.Check {
	c := diagnostics.Check{ID: "claude_code", Label: "Claude Code", Severity: diagnostics.SeverityOK}
	parts := []string{}
	if path, err := opts.FindClaude(); err != nil {
		c.Severity = diagnostics.SeverityWarn
		parts = append(parts, "CLI not found; `claude2kiro run` offers to install it")
	} else {
		parts = append(parts, "CLI "+path)
	}

	routing, err := cmd.ClaudeCodeRouting(filepath.Join(claudeConfigDirFor(cfg), "settings.json"), claudeStateFileFor(cfg))
	switch {
	case err != nil:
		c.Severity = diagnostics.SeverityWarn
		parts = append(parts, err.Error())
	case len(routing) == 0:
		parts = append(parts, "no persistent routing; `claude2kiro run` routes each session it starts")
	default:
		parts = append(parts, strings.Join(routing, ", "))
		if !proxyRunning && strings.Contains(strings.Join(routing, " "), "settings.json env") {
			c.Severity = diagnostics.SeverityWarn
			parts = append(parts, "a plain `claude` fails while no proxy runs; `claude2kiro disable` removes the routing")
		}
	}
	c.Detail = strings.Join(parts, "; ")
	return c
}

func doctorDesktopCheck(opts doctorOptions) diagnostics.Check {
	c := diagnostics.Check{ID: "claude_desktop", Label: "Claude Desktop", Severity: diagnostics.SeverityOK}
	switch {
	case runtime.GOOS != "windows":
		c.Detail = "gateway setup is Windows-only; not checked"
	case cmd.DesktopGatewayConfigured(opts.LocalAppData):
		c.Detail = "gateway config written by Claude2Kiro present"
	default:
		c.Detail = "no Claude2Kiro gateway config; `claude2kiro desktop` sets it up"
	}
	return c
}

// runDoctor implements `claude2kiro doctor [--json] [--offline]`. It exits 1
// when a check fails, so scripts can use it as a health gate.
func runDoctor(args []string) {
	opts := defaultDoctorOptions()
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--offline":
			opts.Offline = true
		case "-h", "--help":
			fmt.Println("Usage: claude2kiro doctor [--json] [--offline]")
			fmt.Println("  Read-only health report: config, Kiro token, model catalogue, proxy,")
			fmt.Println("  Claude Code and Claude Desktop routing, request settings.")
			fmt.Println("  --json     machine-readable output for bug reports")
			fmt.Println("  --offline  skip the ListAvailableModels call")
			return
		default:
			fmt.Fprintf(os.Stderr, "doctor: unknown argument %q (try --help)\n", a)
			os.Exit(2)
		}
	}

	report := collectDoctorReport(opts)
	var err error
	if asJSON {
		err = diagnostics.RenderJSON(os.Stdout, report)
	} else {
		err = diagnostics.RenderText(os.Stdout, report)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: write report: %v\n", err)
		os.Exit(1)
	}
	if report.Worst() == diagnostics.SeverityError {
		os.Exit(1)
	}
}
