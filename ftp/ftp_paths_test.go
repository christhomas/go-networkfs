package ftp

// Tests for the path splitting and error classification.
//
// The classifiers exist so a mount failure tells the operator which thing is
// wrong — the hostname, the port, the credentials — rather than surfacing a
// raw net error. That mapping is the whole value of the function and nothing
// checked it, so a reordered case or a changed substring would go unnoticed.

import (
	"errors"
	"strings"
	"testing"
)

// statusError stands in for goftp.Error, the client's error interface. The
// classifiers type-assert to it and branch on Code(), so a stand-in has to
// satisfy the whole interface rather than merely carry a code.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string   { return e.msg }
func (e statusError) Code() int       { return e.code }
func (e statusError) Temporary() bool { return false }
func (e statusError) Message() string { return e.msg }

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
		{"coded 550", statusError{550, "550 no such file"}, "does not exist"},
		{"coded 530", statusError{530, "530 please login"}, "authentication required"},
		{"uncoded 550 in the message", errors.New("550 File not found"), "does not exist"},
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
	authErr := statusError{530, "530 login incorrect"}

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
		{statusError{530, "530 not logged in"}, true},
		{statusError{532, "532 need account"}, true},
		{statusError{550, "550 no such file"}, false},
		{errors.New("530 Login incorrect"), true},
		{errors.New("Login incorrect"), true},
		{errors.New("connection refused"), false},
	} {
		if got := isFTPAuthError(tt.err); got != tt.want {
			t.Errorf("isFTPAuthError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
