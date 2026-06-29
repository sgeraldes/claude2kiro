package main

import "testing"

func TestIsContextLengthExceeded(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "codewhisperer threshold reason",
			body: `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`,
			want: true,
		},
		{
			name: "input too long message only",
			body: `{"message":"Input is too long."}`,
			want: true,
		},
		{
			name: "case insensitive reason",
			body: `{"reason":"content_length_exceeds_threshold"}`,
			want: true,
		},
		{
			name: "unrelated 400 improperly formed",
			body: `{"message":"Improperly formed request."}`,
			want: false,
		},
		{
			name: "input too long phrase echoed in unrelated error body",
			body: `{"message":"Improperly formed request.","detail":"tool description: input is too long"}`,
			want: false,
		},
		{
			name: "non-json body with threshold reason",
			body: `AccessDeniedException: CONTENT_LENGTH_EXCEEDS_THRESHOLD`,
			want: true,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextLengthExceeded([]byte(tc.body)); got != tc.want {
				t.Errorf("isContextLengthExceeded(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
