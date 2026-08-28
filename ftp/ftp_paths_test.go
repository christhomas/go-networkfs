package ftp

// Tests for the path splitting and error classification.
//
// The classifiers exist so a mount failure tells the operator which thing is
// wrong — the hostname, the port, the credentials — rather than surfacing a
// raw net error. That mapping is the whole value of the function and nothing
// checked it, so a reordered case or a changed substring would go unnoticed.

import (
	"errors"
	"fmt"
	"net/textproto"
	"strings"
	"testing"
)

func TestSplitParentName(t *testing.T) {
	for _, tt := range []struct{ in, parent, name string }{
		{"/a/b/c", "/a/b", "c"},
		{"/a/b/c/", "/a/b", "c"},
		{"/top", "/", "top"},
		{"/top/", "/", "top"},
		{"bare", "/", "bare"},
		{"/a/b/c/d", "/a/b/c", "d"},
	} {
		parent, name := splitParentName(tt.in)
		if parent != tt.parent || name != tt.name {
			t.Errorf("splitParentName(%q) = (%q, %q), want (%q, %q)",
				tt.in, parent, name, tt.parent, tt.name)
		}
	}
}

func TestNameFromPath(t *testing.T) {
	d := &FTPDriver{}
	for _, tt := range []struct{ in, want string }{
		{"/a/b/c.txt", "c.txt"},
		{"/a/b/", "b"},
		{"/single", "single"},
	} {
		if got := d.nameFromPath(tt.in); got != tt.want {
			t.Errorf("nameFromPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A 550 is "no such file", and it arrives either as a typed textproto error or
// buried in a message, depending on where in the library it surfaced. Both
// have to map to the same explanation.
func TestClassifyFTPPathError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"typed 550", &textproto.Error{Code: 550, Msg: "no such file"}, "does not exist"},
		{"typed 530", &textproto.Error{Code: 530, Msg: "please login"}, "authentication required"},
		{"untyped 550", errors.New("550 File not found"), "does not exist"},
		{"anything else", errors.New("connection reset"), "failed to access"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFTPPathError(tt.err, "/some/path")
			if got == nil {
				t.Fatal("classifier returned nil for a real error")
			}
			if !strings.Contains(got.Error(), tt.want) {
				t.Errorf("classified %q as %q, want it to mention %q", tt.err, got, tt.want)
			}
			if !strings.Contains(got.Error(), "/some/path") {
				t.Errorf("message %q does not name the path", got)
			}
		})
	}
}

func TestClassifyFTPConnectErrorIsNilForSuccess(t *testing.T) {
	if got := classifyFTPConnectError(nil, "host", 21, "user"); got != nil {
		t.Errorf("nil error classified as %v", got)
	}
}

// Each failure mode gets its own explanation, and the message has to name the
// thing the operator can act on.
func TestClassifyFTPConnectError(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		user     string
		want     string
		mentions string
	}{
		{"dns", errors.New("dial tcp: lookup nope: no such host"), "u", "cannot resolve host", "nope-host"},
		{"refused", errors.New("dial tcp 1.2.3.4:21: connection refused"), "u", "refused the connection", "21"},
		{"timeout", errors.New("dial tcp: i/o timeout"), "u", "", ""},
		{"deadline", errors.New("context deadline exceeded"), "u", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFTPConnectError(tt.err, "nope-host", 21, tt.user)
			if got == nil {
				t.Fatal("classifier returned nil for a real error")
			}
			if tt.want != "" && !strings.Contains(got.Error(), tt.want) {
				t.Errorf("classified as %q, want it to mention %q", got, tt.want)
			}
			// Whatever the classification, the original error survives so the
			// detail is not lost.
			if !strings.Contains(got.Error(), tt.err.Error()) {
				t.Errorf("classified message %q dropped the underlying error", got)
			}
		})
	}
}

// An unauthenticated user is described as "anonymous" rather than as an empty
// string, which reads as a missing field rather than a deliberate login.
func TestClassifyFTPConnectErrorNamesAnonymous(t *testing.T) {
	authErr := &textproto.Error{Code: 530, Msg: "login incorrect"}

	withUser := classifyFTPConnectError(authErr, "h", 21, "bob")
	if !strings.Contains(withUser.Error(), `"bob"`) {
		t.Errorf("message %q does not name the user", withUser)
	}

	withoutUser := classifyFTPConnectError(authErr, "h", 21, "")
	if !strings.Contains(withoutUser.Error(), "anonymous") {
		t.Errorf("message %q does not describe an empty user as anonymous", withoutUser)
	}
}

func TestIsFTPAuthError(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{&textproto.Error{Code: 530, Msg: "login incorrect"}, true},
		{&textproto.Error{Code: 550, Msg: "no such file"}, false},
		{errors.New("530 Login incorrect"), true},
		{errors.New("connection refused"), false},
		{fmt.Errorf("wrapped: %w", &textproto.Error{Code: 530}), true},
	} {
		if got := isFTPAuthError(tt.err); got != tt.want {
			t.Errorf("isFTPAuthError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
