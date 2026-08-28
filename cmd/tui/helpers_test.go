package main

// Tests for the terminal-output helpers and the preview builder.
//
// These are the parts that write escape sequences and format listings. They
// are pure enough to test directly, and the escape sequences in particular are
// worth pinning: a wrong byte there is invisible in review and shows up as a
// corrupted terminal.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// fakeDriver serves canned responses so the preview and listing paths can be
// driven without a server.
type fakeDriver struct {
	stat     api.FileInfo
	statErr  error
	entries  []api.FileInfo
	listErr  error
	body     []byte
	openErr  error
	openCall int
}

func (f *fakeDriver) Name() string                                { return "fake" }
func (f *fakeDriver) Mount(int, map[string]string) error          { return nil }
func (f *fakeDriver) Unmount(int) error                           { return nil }
func (f *fakeDriver) Stat(int, string) (api.FileInfo, error)      { return f.stat, f.statErr }
func (f *fakeDriver) ListDir(int, string) ([]api.FileInfo, error) { return f.entries, f.listErr }
func (f *fakeDriver) Mkdir(int, string) error                     { return nil }
func (f *fakeDriver) Remove(int, string) error                    { return nil }
func (f *fakeDriver) Rename(int, string, string) error            { return nil }
func (f *fakeDriver) CreateFile(int, string) (io.WriteCloser, error) {
	return nil, errors.New("not supported")
}

func (f *fakeDriver) OpenFile(int, string) (io.ReadCloser, error) {
	f.openCall++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(bytes.NewReader(f.body)), nil
}

func TestKittyDeleteAll(t *testing.T) {
	plain := kittyDeleteAll(false)
	if !strings.Contains(plain, "a=d,d=A") {
		t.Errorf("delete sequence %q does not ask Kitty to delete all", plain)
	}
	if strings.Contains(plain, "\x1bPtmux;") {
		t.Error("plain sequence is tmux-wrapped")
	}

	wrapped := kittyDeleteAll(true)
	if !strings.HasPrefix(wrapped, "\x1bPtmux;") {
		t.Errorf("tmux sequence %q is not DCS-wrapped", wrapped)
	}
}

// The cursor must be saved, moved, and restored, so bubbletea's next redraw
// lands where it expects.
func TestPositionImageSavesAndRestoresCursor(t *testing.T) {
	seq := positionImage("IMAGE", 4, 51, false)

	if !strings.HasPrefix(seq, "\x1b7") {
		t.Error("cursor is not saved first")
	}
	if !strings.HasSuffix(seq, "\x1b8") {
		t.Error("cursor is not restored last")
	}
	if !strings.Contains(seq, "\x1b[4;51H") {
		t.Errorf("cursor is not moved to row 4 col 51: %q", seq)
	}
	if !strings.Contains(seq, "IMAGE") {
		t.Error("the image payload is missing")
	}
	if strings.Index(seq, "a=d,d=A") > strings.Index(seq, "IMAGE") {
		t.Error("the previous image is deleted after the new one is drawn")
	}
}

// The image is drawn at the top-left of the right-hand panel, below the
// title, meta and blank rows.
func TestRightPanelTopLeft(t *testing.T) {
	for _, tt := range []struct {
		width   int
		wantCol int
		whatFor string
	}{
		{100, 43, "40% of 100 plus the separator"},
		{40, 27, "clamped to the 24-column minimum"},
		{0, 27, "a zero width still yields a usable column"},
	} {
		m := model{width: tt.width}
		row, col := m.rightPanelTopLeft()
		if row != 4 {
			t.Errorf("width %d: row %d, want 4 (title, meta, blank, body)", tt.width, row)
		}
		if col != tt.wantCol {
			t.Errorf("width %d: col %d, want %d (%s)", tt.width, col, tt.wantCol, tt.whatFor)
		}
	}
}

func TestImageCommandsRun(t *testing.T) {
	if cmd := emitImageCmd("x"); cmd == nil {
		t.Fatal("emitImageCmd returned no command")
	} else if msg := cmd(); msg != nil {
		t.Errorf("emitImageCmd produced a message: %v", msg)
	}

	if cmd := cleanupImageCmd(true); cmd == nil {
		t.Fatal("cleanupImageCmd returned no command")
	} else if msg := cmd(); msg != nil {
		t.Errorf("cleanupImageCmd produced a message: %v", msg)
	}
}

func TestBuildPreviewText(t *testing.T) {
	d := &fakeDriver{body: []byte("hello, this is plain text\n")}

	p := buildPreview(d, 1, "/a.txt", "a.txt", 26, imgNone, false)
	if p.kind != previewText {
		t.Fatalf("kind %v, want text", p.kind)
	}
	if !strings.Contains(p.text, "plain text") {
		t.Errorf("preview text %q does not carry the content", p.text)
	}
	if p.name != "a.txt" || p.size != 26 {
		t.Errorf("metadata not carried through: %+v", p)
	}
}

func TestBuildPreviewBinary(t *testing.T) {
	// A run of zero bytes sniffs as binary rather than text.
	d := &fakeDriver{body: append([]byte{0x00, 0x01, 0x02}, bytes.Repeat([]byte{0}, 600)...)}

	p := buildPreview(d, 1, "/a.bin", "a.bin", 603, imgNone, false)
	if p.kind != previewBinary {
		t.Errorf("kind %v, want binary", p.kind)
	}
}

func TestBuildPreviewReportsOpenFailure(t *testing.T) {
	d := &fakeDriver{openErr: errors.New("permission denied")}

	p := buildPreview(d, 1, "/nope", "nope", 0, imgNone, false)
	if p.kind != previewError {
		t.Fatalf("kind %v, want error", p.kind)
	}
	if p.err == nil || !strings.Contains(p.err.Error(), "permission denied") {
		t.Errorf("error not carried through: %v", p.err)
	}
}

func TestWriteRowMarksDirectories(t *testing.T) {
	var buf bytes.Buffer
	d := &fakeDriver{
		stat: api.FileInfo{Name: "dir", IsDir: true},
		entries: []api.FileInfo{
			{Name: "sub", IsDir: true, Size: 4096},
			{Name: "file.txt", Size: 123},
		},
	}

	if err := printPath(&buf, d, 1, "/dir"); err != nil {
		t.Fatalf("printPath: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "sub") || !strings.Contains(out, "file.txt") {
		t.Errorf("listing missing entries:\n%s", out)
	}
	// A directory's size is shown as "-" rather than whatever the server said.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sub") && !strings.Contains(line, "-") {
			t.Errorf("directory row has no dash: %q", line)
		}
		if strings.Contains(line, "file.txt") && !strings.Contains(line, "123") {
			t.Errorf("file row lost its size: %q", line)
		}
	}
}

func TestPrintPathOnFilePrintsOneRow(t *testing.T) {
	var buf bytes.Buffer
	d := &fakeDriver{stat: api.FileInfo{Name: "one.txt", Size: 7}}

	if err := printPath(&buf, d, 1, "/one.txt"); err != nil {
		t.Fatalf("printPath: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Errorf("printed %d lines for a single file:\n%s", len(lines), buf.String())
	}
}

func TestPrintPathReportsErrors(t *testing.T) {
	var buf bytes.Buffer

	statFail := &fakeDriver{statErr: errors.New("gone")}
	if err := printPath(&buf, statFail, 1, "/x"); err == nil {
		t.Error("printPath succeeded despite a stat failure")
	}

	listFail := &fakeDriver{stat: api.FileInfo{IsDir: true}, listErr: errors.New("denied")}
	if err := printPath(&buf, listFail, 1, "/x"); err == nil {
		t.Error("printPath succeeded despite a list failure")
	}
}
