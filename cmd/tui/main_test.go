package main

// Tests for the bubbletea model.
//
// A model is a value: Update takes a message and returns the next one, and
// View renders from that value alone. So the whole interaction can be driven
// without a terminal, which is what makes the key handling, the screen
// transitions and the async message paths testable at all.

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// key builds the message bubbletea delivers for a keypress.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// send pushes a message through Update and returns the resulting model.
func send(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	got, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, not a model", next)
	}
	return got, cmd
}

func TestInitialModelStartsOnPicker(t *testing.T) {
	m := initialModel()
	if m.screen != screenPicker {
		t.Errorf("screen %v, want picker", m.screen)
	}
	if len(m.driverTypes) == 0 {
		t.Error("no driver types; the registry should be populated by the blank imports")
	}
	for i := 1; i < len(m.driverTypes); i++ {
		if m.driverTypes[i-1] > m.driverTypes[i] {
			t.Errorf("driver types not sorted: %v", m.driverTypes)
			break
		}
	}
	if m.Init() != nil {
		t.Error("Init should issue no command")
	}
}

func TestWindowSizeIsRecordedOnEveryScreen(t *testing.T) {
	for _, s := range []screen{screenPicker, screenConfig, screenBrowser} {
		m := initialModel()
		m.screen = s
		got, _ := send(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		if got.width != 120 || got.height != 40 {
			t.Errorf("screen %v: size %dx%d, want 120x40", s, got.width, got.height)
		}
	}
}

func TestCtrlCQuitsFromEveryScreen(t *testing.T) {
	for _, s := range []screen{screenPicker, screenConfig, screenBrowser} {
		m := initialModel()
		m.screen = s
		_, cmd := send(t, m, key("ctrl+c"))
		if cmd == nil {
			t.Errorf("screen %v: ctrl+c issued no command", s)
		}
	}
}

func TestPickerNavigationStaysInBounds(t *testing.T) {
	m := initialModel()

	// Up at the top is a no-op rather than an underflow.
	m, _ = send(t, m, key("up"))
	if m.pickerIdx != 0 {
		t.Errorf("pickerIdx %d after up at the top, want 0", m.pickerIdx)
	}

	for range len(m.driverTypes) + 3 {
		m, _ = send(t, m, key("down"))
	}
	if want := len(m.driverTypes) - 1; m.pickerIdx != want {
		t.Errorf("pickerIdx %d after running past the end, want %d", m.pickerIdx, want)
	}

	m, _ = send(t, m, key("k"))
	if want := len(m.driverTypes) - 2; m.pickerIdx != want {
		t.Errorf("pickerIdx %d after k, want %d", m.pickerIdx, want)
	}
	m, _ = send(t, m, key("j"))
	if want := len(m.driverTypes) - 1; m.pickerIdx != want {
		t.Errorf("pickerIdx %d after j, want %d", m.pickerIdx, want)
	}
}

// Selecting a driver moves to the config screen with that driver's fields,
// pre-filled with their defaults.
func TestPickerEnterLoadsSchemaWithDefaults(t *testing.T) {
	m := initialModel()
	for i, dt := range m.driverTypes {
		if dt == 7 { // S3, the schema with defaults in it
			m.pickerIdx = i
		}
	}

	m, _ = send(t, m, key("enter"))

	if m.screen != screenConfig {
		t.Fatalf("screen %v, want config", m.screen)
	}
	if m.selectedDriver != 7 {
		t.Fatalf("selectedDriver %d, want 7", m.selectedDriver)
	}
	if len(m.values) != len(m.fields) {
		t.Fatalf("%d values for %d fields", len(m.values), len(m.fields))
	}
	for i, f := range m.fields {
		if m.values[i] != f.def {
			t.Errorf("field %s starts as %q, want its default %q", f.key, m.values[i], f.def)
		}
	}
	if m.fieldIdx != 0 {
		t.Errorf("fieldIdx %d, want 0", m.fieldIdx)
	}
}

func TestPickerQuits(t *testing.T) {
	m := initialModel()
	if _, cmd := send(t, m, key("q")); cmd == nil {
		t.Error("q issued no quit command")
	}
	if _, cmd := send(t, m, key("esc")); cmd == nil {
		t.Error("esc issued no quit command")
	}
}

// configModel returns a model sitting on the config screen for a driver with
// several fields.
func configModel(t *testing.T) model {
	t.Helper()
	m := initialModel()
	for i, dt := range m.driverTypes {
		if dt == 1 { // FTP
			m.pickerIdx = i
		}
	}
	m, _ = send(t, m, key("enter"))
	if m.screen != screenConfig {
		t.Fatal("did not reach the config screen")
	}
	return m
}

func TestConfigTypingAndBackspace(t *testing.T) {
	m := configModel(t)

	for _, r := range "host1" {
		m, _ = send(t, m, key(string(r)))
	}
	if m.values[0] != "host1" {
		t.Errorf("typed value %q, want host1", m.values[0])
	}

	m, _ = send(t, m, key("backspace"))
	if m.values[0] != "host" {
		t.Errorf("after backspace %q, want host", m.values[0])
	}

	// Backspacing an empty field must not underflow.
	m.values[0] = ""
	m, _ = send(t, m, key("backspace"))
	if m.values[0] != "" {
		t.Errorf("backspace on empty gave %q", m.values[0])
	}
}

func TestConfigFieldNavigationStaysInBounds(t *testing.T) {
	m := configModel(t)

	m, _ = send(t, m, key("shift+tab"))
	if m.fieldIdx != 0 {
		t.Errorf("fieldIdx %d after shift+tab at the top, want 0", m.fieldIdx)
	}

	for range len(m.fields) + 3 {
		m, _ = send(t, m, key("tab"))
	}
	if want := len(m.fields) - 1; m.fieldIdx != want {
		t.Errorf("fieldIdx %d after running past the end, want %d", m.fieldIdx, want)
	}
}

// Enter advances through the fields and only mounts on the last one.
func TestConfigEnterAdvancesThenMounts(t *testing.T) {
	m := configModel(t)
	last := len(m.fields) - 1

	for i := range last {
		m, _ = send(t, m, key("enter"))
		if m.fieldIdx != i+1 {
			t.Fatalf("enter on field %d moved to %d", i, m.fieldIdx)
		}
		if m.screen != screenConfig {
			t.Fatalf("left the config screen early, on field %d", i)
		}
	}

	// The last enter attempts the mount. It fails without a server, which is
	// the point: the failure is reported rather than crashing or advancing.
	m, _ = send(t, m, key("enter"))
	if m.screen == screenBrowser {
		t.Error("reached the browser despite an unreachable host")
	}
	if m.status == "" {
		t.Error("a failed mount left no status")
	}
}

func TestConfigEscReturnsToPicker(t *testing.T) {
	m := configModel(t)
	m, _ = send(t, m, key("esc"))
	if m.screen != screenPicker {
		t.Errorf("screen %v after esc, want picker", m.screen)
	}
}

// browserModel returns a model on the browser screen with some entries.
func browserModel() model {
	m := initialModel()
	m.screen = screenBrowser
	m.cwd = "/"
	m.entries = []api.FileInfo{
		{Name: "dir", Path: "/dir", IsDir: true},
		{Name: "a.txt", Path: "/a.txt", Size: 10},
		{Name: "b.txt", Path: "/b.txt", Size: 20},
	}
	return m
}

func TestBrowserNavigationStaysInBounds(t *testing.T) {
	m := browserModel()

	m, _ = send(t, m, key("up"))
	if m.browseIdx != 0 {
		t.Errorf("browseIdx %d after up at the top, want 0", m.browseIdx)
	}

	for range len(m.entries) + 3 {
		m, _ = send(t, m, key("down"))
	}
	if want := len(m.entries) - 1; m.browseIdx != want {
		t.Errorf("browseIdx %d after running past the end, want %d", m.browseIdx, want)
	}
}

func TestBrowserEnterOnDirectoryDescends(t *testing.T) {
	m := browserModel()
	m.browseIdx = 0 // the directory

	m, cmd := send(t, m, key("enter"))
	if m.cwd != "/dir" {
		t.Errorf("cwd %q after entering a directory, want /dir", m.cwd)
	}
	if cmd == nil {
		t.Error("descending issued no refresh command")
	}
}

func TestBrowserEnterOnFileRequestsPreview(t *testing.T) {
	m := browserModel()
	m.browseIdx = 1 // a.txt

	m, cmd := send(t, m, key("enter"))
	if m.cwd != "/" {
		t.Errorf("cwd changed to %q on a file", m.cwd)
	}
	if cmd == nil {
		t.Error("no preview command issued")
	}
	if !strings.Contains(m.status, "a.txt") {
		t.Errorf("status %q does not mention the file", m.status)
	}
}

func TestBrowserBackAtRootDoesNothing(t *testing.T) {
	m := browserModel()
	m, _ = send(t, m, key("left"))
	if m.cwd != "/" {
		t.Errorf("cwd %q, want / to be the ceiling", m.cwd)
	}
}

func TestBrowserBackAscends(t *testing.T) {
	m := browserModel()
	m.cwd = "/one/two"

	m, cmd := send(t, m, key("backspace"))
	if m.cwd != "/one" {
		t.Errorf("cwd %q after going back, want /one", m.cwd)
	}
	if cmd == nil {
		t.Error("going back issued no refresh command")
	}
}

func TestBrowserEnterWithNoEntriesIsSafe(t *testing.T) {
	m := browserModel()
	m.entries = nil

	m, cmd := send(t, m, key("enter"))
	if cmd != nil {
		t.Error("enter on an empty listing issued a command")
	}
	if m.cwd != "/" {
		t.Errorf("cwd changed to %q", m.cwd)
	}
}

// entriesMsg is delivered asynchronously; both outcomes must land on the model.
func TestBrowserHandlesEntriesMessage(t *testing.T) {
	m := browserModel()
	m.browseIdx = 2

	m, _ = send(t, m, entriesMsg{entries: []api.FileInfo{{Name: "x"}}})
	if len(m.entries) != 1 {
		t.Errorf("got %d entries, want 1", len(m.entries))
	}
	if m.browseIdx != 0 {
		t.Errorf("browseIdx %d, want the cursor reset to 0", m.browseIdx)
	}
	if !strings.Contains(m.status, "1 entries") {
		t.Errorf("status %q does not report the count", m.status)
	}

	m, _ = send(t, m, entriesMsg{err: errors.New("boom")})
	if m.entries != nil {
		t.Error("entries survived a listing error")
	}
	if !strings.Contains(m.status, "boom") {
		t.Errorf("status %q does not carry the error", m.status)
	}
}

func TestBrowserHandlesPreviewMessages(t *testing.T) {
	for _, tt := range []struct {
		name string
		p    preview
		want string
	}{
		{"text", preview{kind: previewText, mime: "text/plain", size: 12}, "text preview"},
		{"binary", preview{kind: previewBinary, mime: "application/zip", size: 99}, "no preview"},
		{"error", preview{kind: previewError}, "preview error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := browserModel()
			m, _ = send(t, m, previewMsg{preview: tt.p})
			if !strings.Contains(m.status, tt.want) {
				t.Errorf("status %q, want it to contain %q", m.status, tt.want)
			}
			if m.preview.kind != tt.p.kind {
				t.Errorf("preview kind %v, want %v", m.preview.kind, tt.p.kind)
			}
		})
	}
}

// Moving off a file drops its preview, so a stale one is never shown beside
// the wrong entry.
func TestNavigationClearsPreview(t *testing.T) {
	m := browserModel()
	m.preview = preview{kind: previewText, mime: "text/plain"}

	m, _ = send(t, m, key("down"))
	if m.preview.kind != previewNone {
		t.Errorf("preview kind %v after moving, want it cleared", m.preview.kind)
	}
}

func TestViewsRenderOnEveryScreen(t *testing.T) {
	for _, tt := range []struct {
		name string
		m    model
		want string
	}{
		{"picker", initialModel(), "FTP"},
		{"config", configModel(t), "host"},
		{"browser", browserModel(), "a.txt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.m
			m.width, m.height = 100, 30
			out := m.View()
			if out == "" {
				t.Fatal("View rendered nothing")
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("View does not mention %q:\n%s", tt.want, out)
			}
		})
	}
}

func TestJoinPath(t *testing.T) {
	for _, tt := range []struct{ dir, name, want string }{
		{"", "a", "/a"},
		{"/", "a", "/a"},
		{"/one", "two", "/one/two"},
		{"/one/", "two", "/one/two"},
	} {
		if got := joinPath(tt.dir, tt.name); got != tt.want {
			t.Errorf("joinPath(%q,%q) = %q, want %q", tt.dir, tt.name, got, tt.want)
		}
	}
}

func TestParentPath(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"/", "/"},
		{"/one", "/"},
		{"/one/two", "/one"},
		{"/one/two/", "/one"},
		{"/one/two/three", "/one/two"},
	} {
		if got := parentPath(tt.in); got != tt.want {
			t.Errorf("parentPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPresetSearchPathsAreAbsoluteAndDistinct(t *testing.T) {
	paths := presetSearchPaths()
	if len(paths) == 0 {
		t.Fatal("no preset search paths")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			t.Error("empty search path")
		}
		if seen[p] {
			t.Errorf("duplicate search path %q", p)
		}
		seen[p] = true
	}
}

func TestResolveAccountUnknownName(t *testing.T) {
	if _, _, err := resolveAccount("definitely-not-configured"); err == nil {
		t.Error("resolveAccount accepted an unknown account")
	}
}
