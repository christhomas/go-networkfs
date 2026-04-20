// onedrive/onedrive_test.go - Unit tests for pure helpers and driver surface.
//
// These tests cover:
//   - path helpers (graphPath, normPath, joinPath, splitParent)
//   - retry policy (shouldRetry, backoff)
//   - body-reader nil guard (bodyReaderOrNil)
//   - driveItem.toFileInfo parsing
//   - itemURL (pure URL construction; no HTTP)
//   - driver surface: Name, DriverTypeID, registration, not-connected guards
//
// No network calls are made.

package onedrive

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// ---------- graphPath -----------------------------------------------------

func TestGraphPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"root", "/", ""},
		{"single segment", "/foo", ":/foo"},
		{"nested", "/foo/bar", ":/foo/bar"},
		{"no leading slash", "foo/bar", ":/foo/bar"},
		{"trailing slash", "/foo/bar/", ":/foo/bar"},
		{"spaces escaped", "/my docs/hello world.txt", ":/my%20docs/hello%20world.txt"},
		{"unicode escaped", "/café/naïve", ":/caf%C3%A9/na%C3%AFve"},
		{"reserved chars per segment", "/a+b/c&d", ":/a+b/c&d"},
		{"percent sign escaped", "/50%/done", ":/50%25/done"},
		{"question mark escaped", "/a?b/c", ":/a%3Fb/c"},
		{"hash escaped", "/a#b/c", ":/a%23b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := graphPath(tt.in)
			if got != tt.want {
				t.Errorf("graphPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Literal slashes must remain as separators, not be percent-encoded.
func TestGraphPathSlashesAreSeparators(t *testing.T) {
	got := graphPath("/a/b/c")
	if strings.Contains(got, "%2F") {
		t.Errorf("graphPath should not escape path separators: %q", got)
	}
	if got != ":/a/b/c" {
		t.Errorf("graphPath(/a/b/c) = %q, want :/a/b/c", got)
	}
}

// ---------- normPath ------------------------------------------------------

func TestNormPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "/"},
		{"/", "/"},
		{"foo", "/foo"},
		{"/foo", "/foo"},
		{"/foo/", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/foo/bar/", "/foo/bar"},
		{"foo/bar", "/foo/bar"},
		{"/café", "/café"},
	}
	for _, tt := range tests {
		got := normPath(tt.in)
		if got != tt.want {
			t.Errorf("normPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------- joinPath ------------------------------------------------------

func TestJoinPath(t *testing.T) {
	tests := []struct {
		dir, name, want string
	}{
		{"/", "foo", "/foo"},
		{"/foo", "bar", "/foo/bar"},
		{"/foo/bar", "baz", "/foo/bar/baz"},
		{"/", "café", "/café"},
		{"/foo", "hello world.txt", "/foo/hello world.txt"},
	}
	for _, tt := range tests {
		got := joinPath(tt.dir, tt.name)
		if got != tt.want {
			t.Errorf("joinPath(%q, %q) = %q, want %q", tt.dir, tt.name, got, tt.want)
		}
	}
}

// ---------- splitParent ---------------------------------------------------

func TestSplitParent(t *testing.T) {
	tests := []struct {
		in, parent, name string
	}{
		{"", "/", ""},
		{"/", "/", ""},
		{"/foo", "/", "foo"},
		{"/foo/bar", "/foo", "bar"},
		{"/foo/bar/baz", "/foo/bar", "baz"},
		{"/foo/", "/", "foo"},
		{"foo/bar", "/foo", "bar"},
		{"/café/naïve", "/café", "naïve"},
	}
	for _, tt := range tests {
		p, n := splitParent(tt.in)
		if p != tt.parent || n != tt.name {
			t.Errorf("splitParent(%q) = (%q, %q), want (%q, %q)",
				tt.in, p, n, tt.parent, tt.name)
		}
	}
}

// ---------- shouldRetry ---------------------------------------------------

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{200, false},
		{201, false},
		{301, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{409, false},
		{429, true},
		{500, false},
		{501, false},
		{502, true},
		{503, true},
		{504, true},
		{505, false},
		{599, false},
	}
	for _, tt := range tests {
		if got := shouldRetry(tt.status); got != tt.want {
			t.Errorf("shouldRetry(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// ---------- backoff -------------------------------------------------------

// backoff sleeps internally; we verify the sleep duration is non-decreasing
// (within cap) by measuring elapsed time. Tolerances are loose so this does
// not become flaky on loaded systems.
//
// Expected schedule (attempt: sleep): 0:500ms, 1:1s, 2:2s, 3:4s, 4+:5s (cap).
//
// Checking attempts 5 and 6 would take 10+ seconds total; we limit to 0..4.
func TestBackoffMonotonicAndCapped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive backoff test in short mode")
	}
	var prev time.Duration
	for attempt := 0; attempt <= 4; attempt++ {
		start := time.Now()
		backoff(attempt)
		elapsed := time.Since(start)

		// Must never exceed cap + tolerance.
		if elapsed > 6*time.Second {
			t.Errorf("attempt %d: elapsed %v exceeds cap", attempt, elapsed)
		}
		// Monotonic non-decreasing (allow small scheduler jitter).
		if attempt > 0 && elapsed+100*time.Millisecond < prev {
			t.Errorf("attempt %d: elapsed %v decreased from prev %v",
				attempt, elapsed, prev)
		}
		prev = elapsed
	}
}

// Verify cap is actually 5s for attempts that would otherwise exceed it.
// attempt=4 → 500*(1<<4) = 8000ms, must cap to 5s.
func TestBackoffCapAtFive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive backoff test in short mode")
	}
	start := time.Now()
	backoff(4)
	elapsed := time.Since(start)
	if elapsed > 6*time.Second {
		t.Errorf("backoff(4) slept %v, expected ~5s cap", elapsed)
	}
	if elapsed < 4500*time.Millisecond {
		t.Errorf("backoff(4) slept %v, expected ~5s cap (lower bound)", elapsed)
	}
}

// ---------- bodyReaderOrNil -----------------------------------------------

func TestBodyReaderOrNil_Nil(t *testing.T) {
	got := bodyReaderOrNil(nil)
	if got != nil {
		t.Errorf("bodyReaderOrNil(nil) = %v, want nil", got)
	}
}

func TestBodyReaderOrNil_NonNil(t *testing.T) {
	data := []byte("hello onedrive")
	r := bytes.NewReader(data)
	got := bodyReaderOrNil(r)
	if got == nil {
		t.Fatal("bodyReaderOrNil(nonNil) = nil, want io.Reader")
	}
	// Must be readable and yield the same bytes.
	out, err := io.ReadAll(got)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("read back %q, want %q", out, data)
	}
}

// Typed-nil quirk: a *bytes.Reader that is nil should still return nil per
// the implementation's explicit check. This pins down the contract.
func TestBodyReaderOrNil_TypedNil(t *testing.T) {
	var r *bytes.Reader // typed nil
	got := bodyReaderOrNil(r)
	if got != nil {
		t.Errorf("bodyReaderOrNil((*bytes.Reader)(nil)) = %v, want nil", got)
	}
}

// ---------- driveItem.toFileInfo ------------------------------------------

func TestDriveItemToFileInfo_File(t *testing.T) {
	it := &driveItem{
		Name:                 "report.pdf",
		Size:                 1024,
		LastModifiedDateTime: "2024-01-15T10:30:45Z",
	}
	fi := it.toFileInfo("/docs/report.pdf")

	if fi.Name != "report.pdf" {
		t.Errorf("Name = %q, want report.pdf", fi.Name)
	}
	if fi.Path != "/docs/report.pdf" {
		t.Errorf("Path = %q, want /docs/report.pdf", fi.Path)
	}
	if fi.IsDir {
		t.Error("IsDir = true, want false")
	}
	if fi.Size != 1024 {
		t.Errorf("Size = %d, want 1024", fi.Size)
	}
	want, _ := time.Parse(time.RFC3339, "2024-01-15T10:30:45Z")
	if fi.ModTime != want.Unix() {
		t.Errorf("ModTime = %d, want %d", fi.ModTime, want.Unix())
	}
}

func TestDriveItemToFileInfo_Folder(t *testing.T) {
	it := &driveItem{
		Name:                 "docs",
		Size:                 9999, // Graph may report aggregate size; driver must zero it.
		LastModifiedDateTime: "2024-01-15T10:30:45Z",
		Folder: &struct {
			ChildCount int `json:"childCount"`
		}{ChildCount: 3},
	}
	fi := it.toFileInfo("/docs")

	if !fi.IsDir {
		t.Error("IsDir = false, want true")
	}
	if fi.Size != 0 {
		t.Errorf("Size = %d, want 0 for folder", fi.Size)
	}
	if fi.Name != "docs" {
		t.Errorf("Name = %q, want docs", fi.Name)
	}
	if fi.Path != "/docs" {
		t.Errorf("Path = %q, want /docs", fi.Path)
	}
}

func TestDriveItemToFileInfo_BadTimestamp(t *testing.T) {
	it := &driveItem{
		Name:                 "broken.txt",
		Size:                 42,
		LastModifiedDateTime: "not-a-date",
	}
	// Must not panic.
	fi := it.toFileInfo("/broken.txt")
	if fi.ModTime != 0 {
		t.Errorf("ModTime = %d, want 0 for bad timestamp", fi.ModTime)
	}
	if fi.Size != 42 {
		t.Errorf("Size = %d, want 42", fi.Size)
	}
}

func TestDriveItemToFileInfo_EmptyTimestamp(t *testing.T) {
	it := &driveItem{Name: "x", Size: 1, LastModifiedDateTime: ""}
	fi := it.toFileInfo("/x")
	if fi.ModTime != 0 {
		t.Errorf("ModTime = %d, want 0 for empty timestamp", fi.ModTime)
	}
}

// ---------- itemURL (pure path-to-URL) ------------------------------------

func TestItemURL(t *testing.T) {
	d := &OneDriveDriver{}
	tests := []struct {
		name   string
		path   string
		suffix string
		want   string
	}{
		{
			"root metadata",
			"/", "",
			"https://graph.microsoft.com/v1.0/me/drive/root",
		},
		{
			"root children",
			"/", "/children",
			"https://graph.microsoft.com/v1.0/me/drive/root/children",
		},
		{
			"file metadata",
			"/foo/bar.txt", "",
			"https://graph.microsoft.com/v1.0/me/drive/root:/foo/bar.txt",
		},
		{
			"file content",
			"/foo/bar.txt", "/content",
			"https://graph.microsoft.com/v1.0/me/drive/root:/foo/bar.txt:/content",
		},
		{
			"folder children",
			"/photos", "/children",
			"https://graph.microsoft.com/v1.0/me/drive/root:/photos:/children",
		},
		{
			"upload session with spaces",
			"/my docs/a.txt", "/createUploadSession",
			"https://graph.microsoft.com/v1.0/me/drive/root:/my%20docs/a.txt:/createUploadSession",
		},
		{
			"empty path treated as root",
			"", "/children",
			"https://graph.microsoft.com/v1.0/me/drive/root/children",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.itemURL(tt.path, tt.suffix)
			if got != tt.want {
				t.Errorf("itemURL(%q, %q) = %q, want %q",
					tt.path, tt.suffix, got, tt.want)
			}
		})
	}
}

// ---------- driver surface ------------------------------------------------

func TestDriverName(t *testing.T) {
	d := &OneDriveDriver{}
	if got := d.Name(); got != "onedrive" {
		t.Errorf("Name() = %q, want onedrive", got)
	}
}

func TestDriverTypeIDConstant(t *testing.T) {
	if DriverTypeID != 8 {
		t.Errorf("DriverTypeID = %d, want 8", DriverTypeID)
	}
}

func TestDriverRegistered(t *testing.T) {
	d, ok := api.GetDriver(DriverTypeID)
	if !ok {
		t.Fatalf("GetDriver(%d) not registered", DriverTypeID)
	}
	if d == nil {
		t.Fatal("GetDriver returned nil driver")
	}
	if d.Name() != "onedrive" {
		t.Errorf("registered driver Name() = %q, want onedrive", d.Name())
	}
	if _, isOD := d.(*OneDriveDriver); !isOD {
		t.Errorf("registered driver is %T, want *OneDriveDriver", d)
	}
}

// ---------- not-connected guards ------------------------------------------

// A zero-value OneDriveDriver (connected=false) must reject every operation
// with a non-nil error. This guards against accidental calls pre-Mount.
func TestNotConnectedGuards(t *testing.T) {
	d := &OneDriveDriver{}

	if _, err := d.Stat(1, "/"); err == nil {
		t.Error("Stat on zero driver returned nil error")
	}
	if _, err := d.ListDir(1, "/"); err == nil {
		t.Error("ListDir on zero driver returned nil error")
	}
	if _, err := d.OpenFile(1, "/x"); err == nil {
		t.Error("OpenFile on zero driver returned nil error")
	}
	// CreateFile returns a writer without touching the network; its Write
	// path is what ultimately fails. The contract under test is that the
	// not-connected state is detected up front.
	if _, err := d.CreateFile(1, "/x"); err == nil {
		t.Error("CreateFile on zero driver returned nil error")
	}
	if err := d.Mkdir(1, "/x"); err == nil {
		t.Error("Mkdir on zero driver returned nil error")
	}
	if err := d.Remove(1, "/x"); err == nil {
		t.Error("Remove on zero driver returned nil error")
	}
	if err := d.Rename(1, "/a", "/b"); err == nil {
		t.Error("Rename on zero driver returned nil error")
	}
}

// Unmount is idempotent and should not error on a zero-state driver.
func TestUnmountOnZeroDriver(t *testing.T) {
	d := &OneDriveDriver{}
	if err := d.Unmount(1); err != nil {
		t.Errorf("Unmount on zero driver returned %v, want nil", err)
	}
}

// Mount with missing required config must fail without network I/O
// (client_id and refresh_token are both empty, so the early validation
// branch returns before hitting the token endpoint).
func TestMountMissingConfig(t *testing.T) {
	d := &OneDriveDriver{}
	err := d.Mount(1, map[string]string{})
	if err == nil {
		t.Fatal("Mount with empty config returned nil error")
	}
	if d.connected {
		t.Error("driver reports connected=true after failed Mount")
	}
}

// --- newHTTPError baseline tests ----------------------------------------
//
// Pins the current contract of newHTTPError before any refactor. The
// function reads the Response body, closes it, and maps the status to
// either a known api sentinel (404 / 403 / 409) or a formatted
// fallback that includes the trimmed body. These tests exercise each
// branch via synthetic *http.Response values — no network.

func fakeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNewHTTPErrorSentinelMappings(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{404, "not found", api.ErrNotFound},
		{403, "forbidden", api.ErrPermissionDenied},
		{409, "name conflict", api.ErrExists},
	}
	for _, c := range cases {
		got := newHTTPError(fakeResp(c.status, c.body))
		if got != c.want {
			t.Errorf("status=%d body=%q: got %v, want %v", c.status, c.body, got, c.want)
		}
	}
}

func TestNewHTTPErrorFallbackIncludesStatusAndBody(t *testing.T) {
	got := newHTTPError(fakeResp(500, "  internal server error\n"))
	if got == nil {
		t.Fatal("expected non-nil error for 500")
	}
	msg := got.Error()
	if !strings.Contains(msg, "500") {
		t.Errorf("error message missing status: %q", msg)
	}
	// Body must be trimmed of surrounding whitespace/newlines.
	if !strings.Contains(msg, "internal server error") {
		t.Errorf("error message missing body text: %q", msg)
	}
	if strings.Contains(msg, "  internal") || strings.HasSuffix(msg, "\n") {
		t.Errorf("body not trimmed: %q", msg)
	}
}

func TestNewHTTPErrorFallbackOnEmptyBody(t *testing.T) {
	got := newHTTPError(fakeResp(418, ""))
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(got.Error(), "418") {
		t.Errorf("status missing: %q", got.Error())
	}
}

func TestNewHTTPErrorFallbackOnUnmappedClientErrors(t *testing.T) {
	// 4xx codes that aren't mapped to sentinels (400, 401, 429) fall
	// through to the formatted fallback so higher-level code still
	// sees the status + body.
	for _, status := range []int{400, 401, 405, 429} {
		got := newHTTPError(fakeResp(status, "ohno"))
		if got == api.ErrNotFound || got == api.ErrPermissionDenied || got == api.ErrExists {
			t.Errorf("status=%d: got sentinel %v, want formatted fallback", status, got)
		}
	}
}

// --- mapHTTPError (pure kernel) -----------------------------------------
//
// mapHTTPError is what newHTTPError delegates to after reading+closing
// the response body. Testing it directly removes the need to construct
// *http.Response values and makes every branch trivially reachable.

func TestMapHTTPErrorSentinels(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{404, api.ErrNotFound},
		{403, api.ErrPermissionDenied},
		{409, api.ErrExists},
	}
	for _, c := range cases {
		got := mapHTTPError(c.status, []byte("ignored"))
		if got != c.want {
			t.Errorf("mapHTTPError(%d) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestMapHTTPErrorFallbackTrimsBody(t *testing.T) {
	got := mapHTTPError(500, []byte("  boom\n\t"))
	if got == nil {
		t.Fatal("nil error")
	}
	// Trimmed: no leading spaces, no trailing newline/tab.
	if !strings.Contains(got.Error(), "boom") {
		t.Errorf("missing body: %q", got.Error())
	}
	if strings.Contains(got.Error(), "  boom") || strings.HasSuffix(got.Error(), "\t") {
		t.Errorf("body not trimmed: %q", got.Error())
	}
}

func TestMapHTTPErrorFallbackOnNilBody(t *testing.T) {
	got := mapHTTPError(418, nil)
	if got == nil {
		t.Fatal("nil error")
	}
	if !strings.Contains(got.Error(), "418") {
		t.Errorf("status missing from fallback: %q", got.Error())
	}
}

// Every non-sentinel status must yield a non-sentinel error so
// callers can distinguish "expected" (ErrNotFound etc.) from
// "arbitrary HTTP failure".
func TestMapHTTPErrorNonSentinelIsNotSentinel(t *testing.T) {
	for _, status := range []int{200, 301, 400, 401, 405, 429, 500, 502, 503, 504} {
		got := mapHTTPError(status, []byte("x"))
		if got == api.ErrNotFound || got == api.ErrPermissionDenied || got == api.ErrExists {
			t.Errorf("status=%d returned sentinel %v", status, got)
		}
	}
}
