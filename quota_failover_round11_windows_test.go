//go:build windows

package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// N31: while the token file cannot be read at all (a handle with no sharing,
// after a login in another process), a cache hit is not trusted: the
// request gets an error, not the replaced credentials; once the file can be
// read again, the login is served.
func TestACacheHitIsNotTrustedWhileTheFileCannotBeRead(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-X" {
		t.Fatalf("%+v %v", tok, err)
	}
	loginFromChildProcess(t, TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()})
	name, err := windows.UTF16PtrFromString(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(identityFile("")); err == nil {
		windows.CloseHandle(h)
		t.Skip("this environment lets the file be read while a handle without sharing is open")
	}
	for i := range 3 {
		if tok, err := getToken(); err == nil {
			windows.CloseHandle(h)
			t.Fatalf("read %d served %+v while the file could not be read", i, tok)
		}
	}
	windows.CloseHandle(h)
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
		t.Fatalf("after the hold: %+v %v", tok, err)
	}
}
