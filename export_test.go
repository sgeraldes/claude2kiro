package main

import (
	"bytes"
	"strings"
	"testing"
)

// A made-up value with no resemblance to a real credential: the tests only
// need to see whether it reaches the output.
const fakeAccessToken = "FAKE-ACCESS-TOKEN-FOR-TESTS"

func TestParseExportArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
	}{
		{"no arguments", nil, false, false},
		{"show token", []string{"--show-token"}, true, false},
		{"unknown flag", []string{"--token"}, false, true},
		{"extra word", []string{"--show-token", "now"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseExportArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseExportArgs(%v) error = %v, wantErr %v", tc.args, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("parseExportArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestWriteExportEnvDefaultHidesToken(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			var buf bytes.Buffer
			writeExportEnv(&buf, goos, "51666", exportProxyAPIKey, false)
			out := buf.String()

			if strings.Contains(out, fakeAccessToken) {
				t.Fatalf("default export printed the access token:\n%s", out)
			}
			if !strings.Contains(out, "ANTHROPIC_BASE_URL") || !strings.Contains(out, "http://localhost:51666") {
				t.Fatalf("default export lacks the base URL:\n%s", out)
			}
			if !strings.Contains(out, "claude2kiro export --show-token") {
				t.Fatalf("default export does not say how to get the token:\n%s", out)
			}
			if !strings.Contains(out, "ANTHROPIC_API_KEY") || !strings.Contains(out, exportProxyAPIKey) {
				t.Fatalf("default export lacks the proxy key:\n%s", out)
			}
		})
	}
}

func TestWriteExportEnvShowTokenPrintsIt(t *testing.T) {
	cases := []struct {
		goos string
		want []string
	}{
		{"windows", []string{
			"set ANTHROPIC_BASE_URL=http://localhost:51666\n",
			"set ANTHROPIC_API_KEY=" + fakeAccessToken + "\n",
			`$env:ANTHROPIC_BASE_URL="http://localhost:51666"` + "\n",
			`$env:ANTHROPIC_API_KEY="` + fakeAccessToken + `"` + "\n",
		}},
		{"linux", []string{
			"export ANTHROPIC_BASE_URL=http://localhost:51666\n",
			`export ANTHROPIC_API_KEY="` + fakeAccessToken + `"` + "\n",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			var buf bytes.Buffer
			writeExportEnv(&buf, tc.goos, "51666", fakeAccessToken, true)
			out := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("export --show-token lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "--show-token") {
				t.Fatalf("export --show-token still prints the hint:\n%s", out)
			}
		})
	}
}

// On Unix the output is meant for eval: every line is either an export or a
// comment, with or without the token.
func TestWriteExportEnvUnixOutputIsEvalSafe(t *testing.T) {
	for _, showToken := range []bool{false, true} {
		key := exportProxyAPIKey
		if showToken {
			key = fakeAccessToken
		}
		var buf bytes.Buffer
		writeExportEnv(&buf, "linux", "8080", key, showToken)
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if !strings.HasPrefix(line, "export ") && !strings.HasPrefix(line, "# ") {
				t.Fatalf("showToken=%v: line is neither an export nor a comment: %q", showToken, line)
			}
		}
	}
}
