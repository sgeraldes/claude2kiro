package main

import (
	"encoding/json"
	"testing"
)

func TestJSONCompletionSuffix(t *testing.T) {
	for _, tc := range []struct{ partial, want string }{
		{``, ``},
		{`{`, `}`},
		{`{"file_path": "a.html"`, `}`},
		{`{"file_path": "a.html", "content": "<html>ab`, `"}`},
		{`{"file_path": "a.html", "content": "x\`, `\"}`},
		{`{"file_path": "a.html", "content": "x\u00`, `00"}`},
		{`{"file_path"`, `:null}`},
		{`{"file_path":`, `null}`},
		{`{"file_path": "a",`, `"":null}`},
		{`{"a": tr`, `ue}`},
		{`{"a": nu`, `ll}`},
		{`{"a": 1.`, `0}`},
		{`{"a": -`, `0}`},
		{`{"a": 1e+`, `0}`},
		{`{"a": [1, `, `null]}`},
		{`{"a": [`, `]}`},
		{`{"a": {"b": "c"`, `}}`},
		{`{"a": "x"}`, ``},
	} {
		got := jsonCompletionSuffix(tc.partial)
		if got != tc.want {
			t.Errorf("jsonCompletionSuffix(%q) = %q, want %q", tc.partial, got, tc.want)
		}
		if tc.partial != "" && !json.Valid([]byte(tc.partial+got)) {
			t.Errorf("%q + %q is not valid JSON", tc.partial, got)
		}
	}
}

// Every prefix of a valid document, completed, is valid JSON.
func TestJSONCompletionOfEveryPrefixIsValid(t *testing.T) {
	doc := `{"file_path": "G:\\dir\\a.html", "content": "<p>\"q\" \u00e9 \n</p>", "n": -12.5e+3, "ok": true, "none": null, "list": [1, "two", {"three": [false]}], "empty": {}, "arr": []}`
	if !json.Valid([]byte(doc)) {
		t.Fatal("test document is not valid JSON")
	}
	for i := 1; i <= len(doc); i++ {
		prefix := doc[:i]
		if full := prefix + jsonCompletionSuffix(prefix); !json.Valid([]byte(full)) {
			t.Fatalf("prefix %d completes to invalid JSON: %s", i, full)
		}
	}
}
