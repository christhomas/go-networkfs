package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestDetectImageProtocolOverrides(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want imageProtocol
	}{
		{map[string]string{"NETWORKFS_IMAGE": "kitty"}, imgKitty},
		{map[string]string{"NETWORKFS_IMAGE": "iterm"}, imgITerm},
		{map[string]string{"NETWORKFS_IMAGE": "off"}, imgNone},
		{map[string]string{"NETWORKFS_IMAGE": "", "KITTY_WINDOW_ID": "42"}, imgKitty},
		{map[string]string{"TERM": "xterm-kitty"}, imgKitty},
		{map[string]string{"TERM": "xterm-ghostty"}, imgKitty},
		// Inside tmux, TERM is usually screen-*/tmux-* — TERM_PROGRAM
		// survives from the outer terminal and is what we key on.
		{map[string]string{"TERM": "tmux-256color", "TERM_PROGRAM": "ghostty"}, imgKitty},
		{map[string]string{"TERM": "screen", "TERM_PROGRAM": "Ghostty"}, imgKitty},
		{map[string]string{"TERM_PROGRAM": "kitty"}, imgKitty},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, imgITerm},
		{map[string]string{"TERM_PROGRAM": "WezTerm"}, imgITerm},
		{map[string]string{"TERM": "xterm-256color"}, imgNone},
	}
	for _, c := range cases {
		// Clear all potentially-relevant vars then apply the case.
		for _, k := range []string{"NETWORKFS_IMAGE", "KITTY_WINDOW_ID", "TERM", "TERM_PROGRAM"} {
			t.Setenv(k, "")
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := detectImageProtocol(); got != c.want {
			t.Errorf("env=%v: got %v, want %v", c.env, got, c.want)
		}
	}
}

func TestInTmuxDetection(t *testing.T) {
	t.Setenv("TMUX", "")
	if inTmux() {
		t.Error("inTmux should be false when TMUX is unset")
	}
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	if !inTmux() {
		t.Error("inTmux should be true when TMUX is set")
	}
}

func TestWrapTmuxDoublesEscapes(t *testing.T) {
	// Input: two Kitty chunks, each \x1b_G...\x1b\\
	// Output: wrapped in \x1bPtmux; ... \x1b\\ with every inner ESC
	// doubled.
	in := "\x1b_Ga=T;abc\x1b\\\x1b_Gm=0;def\x1b\\"
	out := wrapTmux(in)

	if !strings.HasPrefix(out, "\x1bPtmux;") {
		t.Errorf("missing DCS prefix: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Errorf("missing ST terminator: %q", out)
	}
	// Every inner ESC byte (there were 4 in the input) must appear
	// doubled inside the DCS, so we expect 8 ESC bytes in the payload
	// plus the 2 from the outer wrap = 10 total ESCs.
	if c := strings.Count(out, "\x1b"); c != 10 {
		t.Errorf("wrapped escape count = %d, want 10: %q", c, out)
	}
}

func TestIsTextish(t *testing.T) {
	cases := []struct {
		mime, name string
		want       bool
	}{
		{"text/plain; charset=utf-8", "foo.txt", true},
		{"application/json", "data", true},
		{"application/octet-stream", "README.md", true},
		{"application/octet-stream", "main.go", true},
		{"application/octet-stream", "mystery.bin", false},
		{"image/png", "pic.png", false},
		{"application/octet-stream", "notes.yaml", true},
		{"application/octet-stream", "config.toml", true},
		{"application/octet-stream", "page.html", false}, // html is detected by mime anyway
	}
	for _, c := range cases {
		if got := isTextish(c.mime, c.name); got != c.want {
			t.Errorf("isTextish(%q,%q) = %v, want %v", c.mime, c.name, got, c.want)
		}
	}
}

func TestLoadTextTruncatesAtRuneBoundary(t *testing.T) {
	// Three-byte rune just at the limit — only the whole rune or
	// nothing should appear, never a partial sequence.
	body := strings.Repeat("a", textPreviewBytes-1) + "€" // € is 3 bytes
	p := loadText(preview{}, bytes.NewReader([]byte(body)))
	if p.kind != previewText {
		t.Fatalf("kind = %v, want previewText", p.kind)
	}
	// The last 3 bytes might get trimmed; len(p.text) should be
	// strictly less than or equal to textPreviewBytes and be valid UTF-8.
	if len(p.text) > textPreviewBytes {
		t.Errorf("text too long: %d > %d", len(p.text), textPreviewBytes)
	}
}

func TestLoadTextStripsBOM(t *testing.T) {
	in := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	p := loadText(preview{}, bytes.NewReader(in))
	if p.text != "hello" {
		t.Errorf("text = %q, want %q", p.text, "hello")
	}
}

func TestEncodeKittyChunks(t *testing.T) {
	// Generate enough bytes that base64 output exceeds the 4096
	// per-chunk limit at least once. 10 KiB of binary → 13.6 KiB
	// base64 → four chunks.
	data := bytes.Repeat([]byte{0x89}, 10_000)
	out := encodeKitty(data)

	if !strings.Contains(out, "\x1b_Ga=T,f=100,t=d,q=2,m=1;") {
		t.Error("first chunk must carry the transmit action, q=2, and m=1")
	}
	if !strings.Contains(out, "m=0;") {
		t.Error("final chunk must set m=0")
	}
	// Every escape starts with \x1b_G and ends with \x1b\\.
	opens := strings.Count(out, "\x1b_G")
	closes := strings.Count(out, "\x1b\\")
	if opens != closes {
		t.Errorf("unmatched escape markers: opens=%d closes=%d", opens, closes)
	}
	// Trailing newline so the cursor drops below the image.
	if !strings.HasSuffix(out, "\n") {
		t.Error("output must end with newline")
	}
}

func TestEncodeKittySingleShot(t *testing.T) {
	// Small payload → one escape sequence, no m= key.
	data := []byte("tinypng")
	out := encodeKitty(data)

	if strings.Contains(out, "m=") {
		t.Errorf("single-chunk payload must not emit m=: %q", out)
	}
	if !strings.Contains(out, "\x1b_Ga=T,f=100,t=d,q=2;") {
		t.Errorf("single-shot missing expected header: %q", out)
	}
	// Exactly one open and close.
	if strings.Count(out, "\x1b_G") != 1 {
		t.Errorf("expected exactly one escape: %q", out)
	}
}

func TestEnsurePNGPassthrough(t *testing.T) {
	// A real 1×1 PNG under maxImageWidthPx: ensurePNG must return the
	// bytes unchanged (no resize, no re-encode).
	in := tinyPNGFixture(t)
	out, err := ensurePNG(in, "image/png")
	if err != nil {
		t.Fatalf("ensurePNG(png): %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Error("under-size PNG bytes must pass through unchanged")
	}
}

// tinyPNGFixture builds a 1×1 RGBA PNG so the passthrough test has a
// valid image to feed ensurePNG rather than random magic bytes.
func tinyPNGFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 1, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestEnsurePNGTranscodesJPEG(t *testing.T) {
	// Minimal valid 1×1 JPEG. Decoding → re-encoding as PNG should
	// succeed and produce bytes starting with the PNG magic.
	jpeg1x1 := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x01, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43,
		0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09,
		0x09, 0x08, 0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12,
		0x13, 0x0F, 0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20,
		0x24, 0x2E, 0x27, 0x20, 0x22, 0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29,
		0x2C, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39, 0x3D, 0x38, 0x32,
		0x3C, 0x2E, 0x33, 0x34, 0x32, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01,
		0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xFF, 0xC4, 0x00, 0x14, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0xFF, 0xC4, 0x00, 0x14, 0x10, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00,
		0x37, 0xFF, 0xD9,
	}
	out, err := ensurePNG(jpeg1x1, "image/jpeg")
	if err != nil {
		t.Fatalf("ensurePNG(jpeg): %v", err)
	}
	if len(out) < 8 || !bytes.Equal(out[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Errorf("transcoded output must start with PNG magic, got %v...", out[:min(len(out), 8)])
	}
}

func TestEncodeITermIsSingleShot(t *testing.T) {
	out := encodeITerm([]byte("fake"), "pic.png")
	if strings.Count(out, "\x1b]1337;File=") != 1 {
		t.Errorf("expected exactly one iTerm2 File escape, got:\n%q", out)
	}
	if !strings.Contains(out, "inline=1") {
		t.Error("iTerm2 sequence must request inline rendering")
	}
}

func TestPreviewBodyText(t *testing.T) {
	cases := []struct {
		p    preview
		want string
	}{
		{preview{kind: previewNone}, ""},
		{preview{kind: previewText, text: "hi"}, "hi"},
		{preview{kind: previewBinary, mime: "application/x-exec", size: 999}, "binary · application/x-exec · 999 bytes\n(no preview)"},
	}
	for _, c := range cases {
		if got := c.p.bodyText(); got != c.want {
			t.Errorf("bodyText() = %q, want %q", got, c.want)
		}
	}
}
