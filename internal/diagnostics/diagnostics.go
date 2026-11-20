// Package diagnostics builds the read-only health report printed by
// `claude2kiro doctor`. The functions here turn already collected facts (file
// contents, probe results) into checks; reading files and calling the network
// is left to the caller, so every rule can be tested without a real machine.
//
// A check never carries a secret: token values, the profile ARN and API keys
// are reported as present or absent, never printed.
package diagnostics

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// Severity is the outcome of one check.
type Severity string

const (
	SeverityOK    Severity = "ok"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Check is one line of the report.
type Check struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail"`
}

// Report is the whole doctor output.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Version     string    `json:"version"`
	Profile     string    `json:"profile"`
	Checks      []Check   `json:"checks"`
}

// Worst returns the most severe outcome in the report.
func (r Report) Worst() Severity {
	worst := SeverityOK
	for _, c := range r.Checks {
		switch c.Severity {
		case SeverityError:
			return SeverityError
		case SeverityWarn:
			worst = SeverityWarn
		}
	}
	return worst
}

// RenderText writes the report for a terminal.
func RenderText(w io.Writer, r Report) error {
	if _, err := fmt.Fprintf(w, "Claude2Kiro doctor (version %s, profile %s)\n\n", r.Version, r.Profile); err != nil {
		return err
	}
	for _, c := range r.Checks {
		if _, err := fmt.Fprintf(w, "%-5s %-22s %s\n", strings.ToUpper(string(c.Severity)), c.Label, c.Detail); err != nil {
			return err
		}
	}
	return nil
}

// RenderJSON writes the report as indented JSON.
func RenderJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// tokenFields are the token file fields the report looks at. The values of
// accessToken, refreshToken and profileArn are only tested for presence.
type tokenFields struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
	AuthMethod   string `json:"authMethod"`
	Provider     string `json:"provider"`
	Region       string `json:"region"`
	ProfileArn   string `json:"profileArn"`
}

// TokenFacts is what a token check learned, for callers that need more than
// the printed line (the catalogue check needs to know the token is usable).
type TokenFacts struct {
	Usable      bool // access token present and not expired
	AccessToken string
	ProfileArn  string
}

// TokenCheck reports on a Kiro token file. data and readErr are the result of
// reading the file; missing tells whether readErr means the file does not
// exist. fallback is true for a profile of auth.fallback_profiles: a missing
// token there is a warning (failover skips it), and an expired access token
// with a refresh token is normal, since failover renews it when it switches.
func TokenCheck(id, label string, data []byte, readErr error, missing bool, now time.Time, fallback bool) (Check, TokenFacts) {
	c := Check{ID: id, Label: label}
	failSeverity := SeverityError
	if fallback {
		failSeverity = SeverityWarn
	}
	fail := func(detail string) (Check, TokenFacts) {
		c.Severity = failSeverity
		c.Detail = detail
		return c, TokenFacts{}
	}
	if readErr != nil {
		if missing {
			return fail("no token file; run `claude2kiro login`")
		}
		return fail("token file unreadable: " + readErr.Error())
	}
	var t tokenFields
	if err := json.Unmarshal(data, &t); err != nil {
		return fail("token file is not valid JSON; run `claude2kiro login`")
	}
	if t.AccessToken == "" {
		return fail("token file has no access token; run `claude2kiro login`")
	}

	parts := []string{}
	if t.AuthMethod != "" {
		parts = append(parts, "auth "+t.AuthMethod)
	}
	if t.Provider != "" {
		parts = append(parts, "provider "+t.Provider)
	}
	if t.Region != "" {
		parts = append(parts, "region "+t.Region)
	}
	parts = append(parts, "profile ARN "+presence(t.ProfileArn), "refresh token "+presence(t.RefreshToken))

	c.Severity = SeverityOK
	facts := TokenFacts{Usable: true, AccessToken: t.AccessToken, ProfileArn: t.ProfileArn}
	switch expiresAt, err := time.Parse(time.RFC3339, t.ExpiresAt); {
	case t.ExpiresAt == "":
		c.Severity = SeverityWarn
		parts = append(parts, "no expiry recorded")
	case err != nil:
		c.Severity = SeverityWarn
		parts = append(parts, "expiry unreadable")
	case !expiresAt.After(now):
		facts.Usable = false
		switch {
		case t.RefreshToken != "" && fallback:
			parts = append(parts, "access token expired "+formatDuration(now.Sub(expiresAt))+" ago; failover renews it when it switches to this profile")
		case t.RefreshToken != "":
			c.Severity = SeverityWarn
			parts = append(parts, "access token expired "+formatDuration(now.Sub(expiresAt))+" ago; the proxy renews it on the next request, or run `claude2kiro refresh`")
		default:
			c.Severity = failSeverity
			parts = append(parts, "access token expired and no refresh token; run `claude2kiro login`")
		}
	default:
		parts = append(parts, "access token valid for "+formatDuration(expiresAt.Sub(now)))
	}
	c.Detail = strings.Join(parts, ", ")
	return c, facts
}

func presence(v string) string {
	if v == "" {
		return "absent"
	}
	return "present"
}

// ConfigCheck reports which configuration file is in use.
func ConfigCheck(path string, exists bool, statErr error, loadErr error) Check {
	c := Check{ID: "config", Label: "Config"}
	switch {
	case statErr != nil:
		c.Severity, c.Detail = SeverityError, "cannot read "+path+": "+statErr.Error()
	case !exists:
		c.Severity, c.Detail = SeverityOK, "no config file at "+path+"; defaults in use"
	case loadErr != nil:
		// YAML errors can quote values from the file, which holds
		// anthropic_api_key; keep only the line numbers.
		where := ""
		if lines := yamlLineRE.FindAllString(loadErr.Error(), -1); len(lines) > 0 {
			where = " (" + strings.Join(lines, ", ") + ")"
		}
		c.Severity, c.Detail = SeverityError, path+" does not parse"+where+"; the proxy runs on the defaults until it is fixed"
	default:
		c.Severity, c.Detail = SeverityOK, path
	}
	return c
}

var yamlLineRE = regexp.MustCompile(`line \d+`)

// ProxyProbe is what the caller found about the local proxy.
type ProxyProbe struct {
	PortFile        string
	PortFileMissing bool
	PortFileErr     error  // read error other than missing
	Port            string // trimmed content of the port file
	ProbeErr        error  // GET /health failed
	Status          int    // HTTP status of /health
	ServedProfile   string // X-Claude2Kiro-Profile header of /health, "" when absent
	ExpectedProfile string // profile label this binary runs as
	DefaultProfile  bool   // this binary runs the default profile (accepts a header-less answer)
}

// ProxyCheck reports whether a proxy for this profile answers.
func ProxyCheck(p ProxyProbe) (Check, bool) {
	c := Check{ID: "proxy", Label: "Proxy"}
	switch {
	case p.PortFileMissing:
		c.Severity, c.Detail = SeverityOK, "not running (no "+p.PortFile+"); `claude2kiro run` starts one"
		return c, false
	case p.PortFileErr != nil:
		c.Severity, c.Detail = SeverityWarn, "cannot read "+p.PortFile+": "+p.PortFileErr.Error()
		return c, false
	case p.Port == "":
		c.Severity, c.Detail = SeverityWarn, p.PortFile+" is empty"
		return c, false
	}
	base := "http://127.0.0.1:" + p.Port
	switch {
	case p.ProbeErr != nil:
		c.Severity, c.Detail = SeverityWarn, "stale port file: nothing answers at "+base+" ("+p.ProbeErr.Error()+")"
		return c, false
	case p.Status != 200:
		c.Severity, c.Detail = SeverityWarn, fmt.Sprintf("%s/health answered HTTP %d", base, p.Status)
		return c, false
	case p.ServedProfile == "" && !p.DefaultProfile:
		c.Severity, c.Detail = SeverityWarn, base+" answers but does not declare a profile; it is not the proxy of profile "+p.ExpectedProfile
		return c, false
	case p.ServedProfile != "" && p.ServedProfile != p.ExpectedProfile:
		c.Severity, c.Detail = SeverityWarn, base+" is the proxy of profile "+p.ServedProfile+", not "+p.ExpectedProfile
		return c, false
	}
	c.Severity, c.Detail = SeverityOK, "running at "+base
	return c, true
}

// CatalogOffline is the skip reason when the user asked for no network call;
// that skip is not a warning.
const CatalogOffline = "--offline"

// CatalogCheck reports whether the live model catalogue answered. skipped,
// when not empty, says why the call was not made.
func CatalogCheck(skipped string, modelIDs []string, err error) Check {
	c := Check{ID: "model_catalog", Label: "Model catalogue"}
	switch {
	case skipped == CatalogOffline:
		c.Severity, c.Detail = SeverityOK, "not checked: "+skipped
	case skipped != "":
		c.Severity, c.Detail = SeverityWarn, "not checked: "+skipped
	case err != nil:
		c.Severity, c.Detail = SeverityError, "ListAvailableModels failed: "+err.Error()
	default:
		hasAuto := false
		for _, id := range modelIDs {
			if id == "auto" {
				hasAuto = true
			}
		}
		c.Severity = SeverityOK
		c.Detail = fmt.Sprintf("%d models available", len(modelIDs))
		if hasAuto {
			c.Detail += ", including auto"
		} else {
			c.Severity = SeverityWarn
			c.Detail += "; auto is not among them, so requests for auto will fail"
		}
	}
	return c
}

// RequestSettingsCheck reports the settings that change what the proxy sends
// to Kiro, and so the credits a request costs. Values other than the defaults
// are flagged so a bug report shows them at a glance.
func RequestSettingsCheck(adv config.AdvancedConfig) Check {
	def := config.Default().Advanced
	thinking := adv.EffectiveThinkingMode()
	changed := []string{}
	unknown := []string{}

	// The proxy treats any value it does not know as full, so a typo is
	// silently the default; say so.
	describe := func(key, value string, known ...string) string {
		value = orDefault(value, "full")
		for _, k := range known {
			if value == k {
				if value != "full" {
					changed = append(changed, key)
				}
				return value
			}
		}
		unknown = append(unknown, fmt.Sprintf("%s %q is not a known value and works as full", key, value))
		return "full"
	}
	if v := strings.ToLower(strings.TrimSpace(adv.ThinkingMode)); v != "" && v != config.ThinkingModeClient && v != config.ThinkingModeBackend {
		unknown = append(unknown, fmt.Sprintf("thinking_mode %q is not a known value and works as %s", adv.ThinkingMode, config.ThinkingModeClient))
	}
	history := describe("history_mode", adv.HistoryMode, "full", "recent", "current_only")
	tools := describe("tool_mode", adv.ToolMode, "full", "compact", "none_text")

	parts := []string{"history " + history}
	if history == "recent" {
		parts[0] += fmt.Sprintf(" (%d turns)", adv.HistoryRecentTurns)
	}
	parts = append(parts, "tools "+tools, "thinking "+thinking)
	if adv.AggressiveCachePoints {
		parts = append(parts, "aggressive cache points on")
		changed = append(changed, "aggressive_cache_points")
	}
	if !adv.StableConversationID {
		parts = append(parts, "stable conversation id off")
		changed = append(changed, "stable_conversation_id")
	}
	if thinking != def.ThinkingMode {
		changed = append(changed, "thinking_mode")
	}
	if !adv.InterruptedToolRecovery {
		parts = append(parts, "interrupted tool recovery off")
		changed = append(changed, "interrupted_tool_recovery")
	}
	if adv.ComparisonMode || adv.AnthropicDirect {
		parts = append(parts, "Anthropic comparison/direct mode on")
		changed = append(changed, "comparison_mode/anthropic_direct")
	}

	c := Check{ID: "request_settings", Label: "Request settings", Severity: SeverityOK, Detail: strings.Join(parts, ", ")}
	if len(changed) > 0 {
		c.Severity = SeverityWarn
		c.Detail += "; changed from the defaults: " + strings.Join(changed, ", ")
	}
	if len(unknown) > 0 {
		c.Severity = SeverityWarn
		c.Detail += "; " + strings.Join(unknown, "; ")
	}
	return c
}

func orDefault(v, def string) string {
	if v = strings.TrimSpace(v); v == "" {
		return def
	}
	return v
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}
