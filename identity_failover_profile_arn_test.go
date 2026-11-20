package main

import "testing"

// requestProfileArn is the profile ARN a request for that token carries: the
// token's own, the shared consumer profile for social logins, none for an IdC
// login without a discovered profile.
func TestRequestProfileArnMatchesTheBuiltRequest(t *testing.T) {
	withOfflineCatalog(t)
	cases := []struct {
		name string
		tok  TokenData
		want string
	}{
		{"token's own", TokenData{AuthMethod: "IdC", ProfileArn: "arn:own"}, "arn:own"},
		{"social", TokenData{AuthMethod: "Social"}, consumerProfileArn},
		{"IdC without a profile", TokenData{AuthMethod: "IdC"}, ""},
	}
	for _, c := range cases {
		if got := requestProfileArn(c.tok); got != c.want {
			t.Errorf("%s: requestProfileArn = %q, want %q", c.name, got, c.want)
		}
		if built := buildCodeWhispererRequest(testRequest(), c.tok).ProfileArn; built != c.want {
			t.Errorf("%s: built request profileArn = %q, want %q", c.name, built, c.want)
		}
	}
}
