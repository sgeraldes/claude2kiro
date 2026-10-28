package main

import (
	"net/url"
	"testing"
)

func TestBuildSocialLoginURLUsesCurrentKiroPortal(t *testing.T) {
	got := buildSocialLoginURL("Google", "http://localhost:49153/oauth/callback", "challenge+/=", "state value")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse login URL: %v", err)
	}
	if want := "https://app.kiro.dev/signin"; u.Scheme+"://"+u.Host+u.Path != want {
		t.Fatalf("login endpoint = %q, want %q", u.Scheme+"://"+u.Host+u.Path, want)
	}
	want := map[string]string{
		"idp":                   "Google",
		"redirect_uri":          "http://localhost:49153/oauth/callback",
		"code_challenge":        "challenge+/=",
		"code_challenge_method": "S256",
		"state":                 "state value",
		"redirect_from":         "kirocli",
	}
	for key, value := range want {
		if actual := u.Query().Get(key); actual != value {
			t.Errorf("query parameter %s = %q, want %q", key, actual, value)
		}
	}
}
