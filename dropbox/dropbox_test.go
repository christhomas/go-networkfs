// dropbox/dropbox_test.go - Unit tests for the Dropbox driver.
//
// Why only unit tests here?
//
// Upstream's driver uses the official dropbox-sdk-go-unofficial/v6 SDK.
// Its Mount() hands the SDK only Token + LogLevel; the SDK's Config does
// expose Client/URLGenerator/Domain testing seams, but Mount() does not
// read any config key that would let a test inject them. Without
// modifying dropbox.go we cannot redirect SDK traffic to an httptest
// server, so end-to-end mocking at the HTTP layer is infeasible.
//
// See dropbox_integration_test.go for real-Dropbox tests gated behind
// the dropbox_integration build tag.

package dropbox

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
	"golang.org/x/oauth2"
)

func TestDriverTypeID(t *testing.T) {
	if DriverTypeID != 4 {
		t.Fatalf("DriverTypeID = %d, want 4", DriverTypeID)
	}
}

func TestName(t *testing.T) {
	d := &DropboxDriver{}
	if d.Name() != "dropbox" {
		t.Fatalf("Name() = %q, want %q", d.Name(), "dropbox")
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("driver type %d not registered", DriverTypeID)
	}
	if d.Name() != "dropbox" {
		t.Fatalf("registry returned %q, want %q", d.Name(), "dropbox")
	}
	if _, ok := d.(*DropboxDriver); !ok {
		t.Fatalf("registry returned wrong concrete type: %T", d)
	}
}

func TestMountMissingAccessToken(t *testing.T) {
	d := &DropboxDriver{}
	if err := d.Mount(1, map[string]string{}); err == nil {
		t.Error("empty config should error (missing access_token)")
	}
	if err := d.Mount(1, nil); err == nil {
		t.Error("nil config should error")
	}
	if err := d.Mount(1, map[string]string{"access_token": ""}); err == nil {
		t.Error("blank access_token should error")
	}
}

// Mount() returns a *DriverError with Code 10 when access_token is
// missing. Pin the error shape so downstream error-handling code can
// rely on it.
func TestMountMissingAccessTokenErrorShape(t *testing.T) {
	d := &DropboxDriver{}
	err := d.Mount(1, map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	var de *api.DriverError
	if !errors.As(err, &de) {
		t.Fatalf("want *api.DriverError, got %T: %v", err, err)
	}
	if de.Code != 10 {
		t.Errorf("Code = %d, want 10", de.Code)
	}
}

// Every op must refuse to run when the driver was never mounted. This
// guards against nil-SDK-client panics.
func TestNotConnectedOperations(t *testing.T) {
	d := &DropboxDriver{}

	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Stat: want ErrNotConnected, got %v", err)
	}

	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("ListDir: want ErrNotConnected, got %v", err)
	}

	if _, err := d.OpenFile(1, "/x"); err == nil {
		t.Error("OpenFile should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("OpenFile: want ErrNotConnected, got %v", err)
	}

	if _, err := d.CreateFile(1, "/x"); err == nil {
		t.Error("CreateFile should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("CreateFile: want ErrNotConnected, got %v", err)
	}

	if err := d.Mkdir(1, "/x"); err == nil {
		t.Error("Mkdir should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Mkdir: want ErrNotConnected, got %v", err)
	}

	if err := d.Remove(1, "/x"); err == nil {
		t.Error("Remove should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Remove: want ErrNotConnected, got %v", err)
	}

	if err := d.Rename(1, "/a", "/b"); err == nil {
		t.Error("Rename should fail on unconnected driver")
	} else if !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("Rename: want ErrNotConnected, got %v", err)
	}
}

// Unmount is safe to call on a never-mounted driver and must not panic.
func TestUnmountUnmounted(t *testing.T) {
	d := &DropboxDriver{}
	if err := d.Unmount(1); err != nil {
		t.Fatalf("Unmount on fresh driver: %v", err)
	}
	// Still not connected afterwards.
	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat should still fail after Unmount")
	}
}

// dbxPath normalizes paths to the Dropbox API convention: root is "",
// leading slash is required for everything else.
func TestDbxPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", ""},
		{"/foo", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"foo", "/foo"},         // missing leading slash added
		{"foo/bar", "/foo/bar"}, // missing leading slash added
		{"/a/b/c.txt", "/a/b/c.txt"},
	}
	for _, c := range cases {
		if got := dbxPath(c.in); got != c.want {
			t.Errorf("dbxPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// nameFromPath returns the trailing non-empty component.
// wrapDbxError special-cases missing_scope errors with a friendlier
// hint; everything else passes through untouched.
func TestWrapDbxError(t *testing.T) {
	if got := wrapDbxError(nil); got != nil {
		t.Errorf("wrapDbxError(nil) = %v, want nil", got)
	}

	plain := fmt.Errorf("boom")
	if got := wrapDbxError(plain); got != plain {
		t.Errorf("wrapDbxError pass-through changed error: got %v, want %v", got, plain)
	}

	scope := fmt.Errorf("api error: missing_scope: files.content.read")
	got := wrapDbxError(scope)
	if got == nil {
		t.Fatal("wrapDbxError on missing_scope returned nil")
	}
	if got == scope {
		t.Error("wrapDbxError on missing_scope should wrap, not pass through")
	}
	if msg := got.Error(); msg == "" ||
		!containsAll(msg, "missing required permission scope", "missing_scope") {
		t.Errorf("wrapped message lacks expected text: %q", msg)
	}
}

// retrieveErr constructs an *oauth2.RetrieveError suitable for tests.
// A non-nil Response is required because the type's Error() method
// dereferences r.Response.Status; tests that exercise the typed-body
// path don't depend on Error() but it's safer to keep the value
// usable as a real error.
func retrieveErr(status int, body string) *oauth2.RetrieveError {
	return &oauth2.RetrieveError{
		Response: &http.Response{StatusCode: status, Status: fmt.Sprintf("%d", status)},
		Body:     []byte(body),
	}
}

// TestIsInvalidGrant locks the field-aware detection contract.
//
// The function is the load-bearing OAuth signal used by the host
// supervisor to auto-pop a re-auth flow, so a false positive (popping
// the browser when the token is fine) and a false negative (silent
// dead mount) are both bad. Cases below cover the two paths
// `isInvalidGrant` exposes — typed `*oauth2.RetrieveError` and
// formatted-message fallback — plus the false-positive guard for
// `invalid_grant` strings appearing inside `error_description`.
func TestIsInvalidGrant(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "typed RetrieveError, structured invalid_grant body",
			err:  retrieveErr(400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`),
			want: true,
		},
		{
			name: "typed RetrieveError, invalid_client body",
			err:  retrieveErr(401, `{"error":"invalid_client","error_description":"Bad client credentials"}`),
			want: false,
		},
		{
			name: "typed RetrieveError, invalid_grant only in error_description",
			err:  retrieveErr(400, `{"error":"invalid_client","error_description":"Some text mentioning invalid_grant for context"}`),
			want: false,
		},
		{
			name: "wrapped string fallback, structured invalid_grant in message",
			err:  fmt.Errorf("dbx: refresh failed: oauth2: cannot fetch token: 400 Bad Request\nResponse: {\"error\":\"invalid_grant\"}"),
			want: true,
		},
		{
			name: "wrapped string fallback, whitespace between field and value",
			err:  fmt.Errorf(`dbx: refresh failed: {"error" : "invalid_grant"}`),
			want: true,
		},
		{
			name: "wrapped string fallback, bare invalid_grant substring (not in field shape)",
			err:  fmt.Errorf("an unrelated message that happens to contain invalid_grant somewhere"),
			want: false,
		},
		{
			name: "wrapped string fallback, invalid_grant only in error_description",
			err:  fmt.Errorf(`dbx: refresh failed: {"error":"invalid_client","error_description":"docs mention invalid_grant"}`),
			want: false,
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("network unreachable"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInvalidGrant(tc.err); got != tc.want {
				t.Errorf("isInvalidGrant(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestWrapDbxError_OAuthReauth locks the supervisor-facing contract:
// when the OAuth signal is genuine, the wrapped error MUST start
// with the `oauth_reauth_required:` prefix; when the signal is a
// false positive (substring inside `error_description`), it MUST
// NOT.
func TestWrapDbxError_OAuthReauth(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantMarker  bool
		wantContain string
	}{
		{
			name:        "typed RetrieveError, structured invalid_grant",
			err:         retrieveErr(400, `{"error":"invalid_grant"}`),
			wantMarker:  true,
			wantContain: "oauth_reauth_required:",
		},
		{
			name:        "wrapped string fallback, structured invalid_grant",
			err:         fmt.Errorf(`dbx: oauth2: {"error":"invalid_grant"}`),
			wantMarker:  true,
			wantContain: "oauth_reauth_required:",
		},
		{
			name:        "invalid_client with invalid_grant only in error_description",
			err:         fmt.Errorf(`dbx: oauth2: {"error":"invalid_client","error_description":"hint: invalid_grant"}`),
			wantMarker:  false,
			wantContain: "",
		},
		{
			name:        "missing_scope still wraps with the scope hint",
			err:         fmt.Errorf("api error: missing_scope: files.content.read"),
			wantMarker:  false,
			wantContain: "missing required permission scope",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapDbxError(tc.err)
			if got == nil {
				t.Fatal("wrapDbxError returned nil for non-nil input")
			}
			msg := got.Error()
			hasMarker := strings.HasPrefix(msg, "oauth_reauth_required:")
			if hasMarker != tc.wantMarker {
				t.Errorf("oauth_reauth_required prefix: got %v, want %v (msg=%q)", hasMarker, tc.wantMarker, msg)
			}
			if tc.wantContain != "" && !strings.Contains(msg, tc.wantContain) {
				t.Errorf("expected message to contain %q, got %q", tc.wantContain, msg)
			}
		})
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
