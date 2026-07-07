// Package agents derives per-subagent statistics from Claude Code's local
// session transcripts. Claude Code does not put a subagent identifier on the
// wire (all subagents share the parent session_id), but it persists each
// subagent's transcript under
//
//	~/.claude/projects/<project>/<sessionID>/subagents/agent-a<name>-<hash>.jsonl
//	                                                    agent-a<name>-<hash>.meta.json
//
// The .meta.json carries the human name/description/model/color; the .jsonl
// carries every turn with token usage and timestamps. This package reads them to
// answer "tokens/sec, turns, and duration per named subagent" — which the proxy
// alone can never attribute.
package agents

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// sessionIDRe bounds a session id to the shape Claude Code actually uses (a
// UUID: hex + dashes). It is critical that the session id — which reaches here
// from the /agents query parameter — never contains glob metacharacters or path
// separators, because it is joined into a filepath.Glob pattern below. Without
// this, `?session=*` would match every project/session on the machine.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Info is the subagent identity from its .meta.json sidecar.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Color       string `json:"color"`
	AgentType   string `json:"agentType"`
}

// Stats is the derived activity for one subagent in one session.
type Stats struct {
	Info
	Turns          int       `json:"turns"`
	OutputTokens   int64     `json:"outputTokens"`
	PeakInputToken int64     `json:"peakInputTokens"`
	TotalInputTok  int64     `json:"totalInputTokens"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	Partial        bool      `json:"partial"` // true if the transcript couldn't be fully scanned (e.g. an oversized line), so stats undercount
}

// MarshalJSON adds the derived duration/throughput fields so API/dashboard
// consumers get them without recomputing (methods aren't serialized).
func (s Stats) MarshalJSON() ([]byte, error) {
	type alias Stats
	return json.Marshal(struct {
		alias
		DurationSec        float64 `json:"durationSec"`
		OutputTokensPerSec float64 `json:"outputTokensPerSec"`
		InputTokensPerSec  float64 `json:"inputTokensPerSec"`
	}{alias(s), s.Duration().Seconds(), s.OutputTokensPerSec(), s.InputTokensPerSec()})
}

// InputTokensPerSec is total ingested tokens per second of active wall-clock —
// the meaningful throughput/load signal since Kiro reports output_tokens as 0.
func (s Stats) InputTokensPerSec() float64 {
	d := s.Duration().Seconds()
	if d <= 0 {
		return 0
	}
	return float64(s.TotalInputTok) / d
}

// Duration is wall-clock from the first to last recorded turn.
func (s Stats) Duration() time.Duration {
	if s.End.Before(s.Start) || s.Start.IsZero() {
		return 0
	}
	return s.End.Sub(s.Start)
}

// OutputTokensPerSec is generated tokens per second of active wall-clock.
func (s Stats) OutputTokensPerSec() float64 {
	d := s.Duration().Seconds()
	if d <= 0 {
		return 0
	}
	return float64(s.OutputTokens) / d
}

// claudeProjectsDir returns ~/.claude/projects, overridable via CLAUDE_CONFIG_DIR.
func claudeProjectsDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// findSubagentsDir locates the subagents directory for a session across all
// projects: <projects>/<project>/<sessionID>/subagents.
func findSubagentsDir(sessionID string) string {
	if sessionID == "" || !sessionIDRe.MatchString(sessionID) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(claudeProjectsDir(), "*", sessionID, "subagents"))
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			return m
		}
	}
	return ""
}

// MostRecentSession returns the session id whose subagents directory was
// modified most recently, so callers can default to "the session you're running
// now" when none is specified. Empty string if none found.
func MostRecentSession() string {
	matches, _ := filepath.Glob(filepath.Join(claudeProjectsDir(), "*", "*", "subagents"))
	var newest string
	var newestMod time.Time
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || !fi.IsDir() {
			continue
		}
		if fi.ModTime().After(newestMod) {
			newestMod = fi.ModTime()
			// parent of "subagents" is the session id dir
			newest = filepath.Base(filepath.Dir(m))
		}
	}
	return newest
}

// SessionStats returns per-subagent stats for a session, newest activity first.
// Returns an empty slice (not an error) when the session has no local subagent
// transcripts (e.g. headless runs), so callers degrade gracefully.
func SessionStats(sessionID string) []Stats {
	dir := findSubagentsDir(sessionID)
	if dir == "" {
		return nil
	}
	metas, _ := filepath.Glob(filepath.Join(dir, "*.meta.json"))
	out := make([]Stats, 0, len(metas))
	for _, metaPath := range metas {
		jsonlPath := strings.TrimSuffix(metaPath, ".meta.json") + ".jsonl"
		s, ok := statsForAgent(metaPath, jsonlPath)
		if ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].End.After(out[j].End) })
	return out
}

func statsForAgent(metaPath, jsonlPath string) (Stats, bool) {
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		return Stats{}, false
	}
	var info Info
	if err := json.Unmarshal(metaBytes, &info); err != nil {
		return Stats{}, false
	}
	s := Stats{Info: info}

	f, err := os.Open(jsonlPath)
	if err != nil {
		// Meta without a transcript yet: still surface the agent with zero activity.
		return s, true
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024) // subagent lines embed large tool outputs
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"type":"assistant"`) {
			continue
		}
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &rec); err != nil || rec.Type != "assistant" {
			continue
		}
		s.Turns++
		s.OutputTokens += rec.Message.Usage.OutputTokens
		s.TotalInputTok += rec.Message.Usage.InputTokens
		if rec.Message.Usage.InputTokens > s.PeakInputToken {
			s.PeakInputToken = rec.Message.Usage.InputTokens
		}
		if info.Model == "" && rec.Message.Model != "" {
			s.Model = rec.Message.Model
		}
		if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			if s.Start.IsZero() || t.Before(s.Start) {
				s.Start = t
			}
			if t.After(s.End) {
				s.End = t
			}
		}
	}
	// A scan error (e.g. bufio.ErrTooLong on a line past the 64MB buffer) means
	// we stopped early; report partial stats rather than silently undercounting.
	if sc.Err() != nil {
		s.Partial = true
	}
	return s, true
}
