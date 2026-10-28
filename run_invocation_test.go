package main

import (
	"reflect"
	"testing"
)

func TestNormalizeRunInvocationResumeFirst(t *testing.T) {
	got, err := normalizeRunInvocation([]string{"claude2kiro", "--resume", "session-123", "run", "--model", "sonnet"})
	if err != nil {
		t.Fatalf("normalize invocation: %v", err)
	}
	want := []string{"claude2kiro", "run", "--resume", "session-123", "--model", "sonnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized args = %#v, want %#v", got, want)
	}
}

func TestNormalizeRunInvocationLeavesCommandFirstFormAlone(t *testing.T) {
	want := []string{"claude2kiro", "run", "--resume", "session-123"}
	got, err := normalizeRunInvocation(want)
	if err != nil {
		t.Fatalf("normalize invocation: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized args = %#v, want %#v", got, want)
	}
}

func TestNormalizeRunInvocationRejectsIncompleteResumeFirstForm(t *testing.T) {
	if _, err := normalizeRunInvocation([]string{"claude2kiro", "--resume", "session-123"}); err == nil {
		t.Fatal("expected incomplete resume-first invocation to fail")
	}
}
