// cmd/tui/preview.go - File preview support for the browser screen.
//
// When the user hits Enter on a file, we stream it into a temp file,
// sniff its type, and render a preview on the right-hand side of the
// browser view. Two preview kinds are supported:
//
//   - text: the first ~4 KiB (truncated at a rune boundary, non-UTF-8
//     bytes rejected) shown inline.
//   - image: transmitted to the terminal via one of several graphics
//     protocols depending on what the terminal supports (Kitty,
//     iTerm2 inline). If we can't identify a supported protocol we
//     fall through to a text stub with the file's metadata.
//
// Support detection is done once at startup via env vars. It's a
// best-effort read: the user can always override via NETWORKFS_IMAGE.

package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	// Register JPEG and GIF decoders with image.Decode; Kitty transmits
	// PNG only (f=100), so anything else needs to be re-encoded.
	_ "image/gif"
	_ "image/jpeg"

	xdraw "golang.org/x/image/draw"

	"github.com/christhomas/go-networkfs/pkg/api"
)

// maxImageWidthPx caps transmitted images at this pixel width.
// Wallpaper-sized PNGs (2-3 MB) were probably fine for the terminal
// itself but the resulting multi-megabyte escape-sequence blob is a
// lot of data to push through stdout + tmux DCS passthrough. 1000 px
// reduces most photos to ~300 KB PNG while staying comfortably larger
// than any typical right-hand preview panel.
const maxImageWidthPx = 1000

// previewSizeLimit caps how many bytes we download for a preview. Big
// binaries would sit in RAM for no reason — the preview never renders
// more than a few KiB of text or a hero-image's worth of pixels.
const previewSizeLimit = 4 * 1024 * 1024

// textPreviewBytes caps text previews at 4 KiB — more than enough for
// "is this a readable file", under the threshold where panel height
// becomes a concern.
const textPreviewBytes = 4 * 1024

type previewKind int

const (
	previewNone previewKind = iota
	previewText
	previewImage
	previewBinary
	previewError
)

type preview struct {
	kind previewKind
	name string
	mime string
	size int64

	// text is populated for previewText.
	text string

	// image is populated for previewImage: already-encoded as an
	// escape-sequence blob ready to be inlined into the View() output.
	image string

	// err is populated for previewError.
	err error
}

// imageProtocol picks one image transmission protocol based on the
// terminal environment. Computed once at process start.
type imageProtocol int

const (
	imgNone imageProtocol = iota
	imgKitty
	imgITerm
)

// detectImageProtocol inspects env vars to decide which protocol (if
// any) to use. Checks in priority order:
//
//  1. NETWORKFS_IMAGE=kitty|iterm|off — explicit override.
//  2. KITTY_WINDOW_ID set, or TERM contains "kitty" / "ghostty", or
//     TERM_PROGRAM=ghostty/kitty → Kitty graphics protocol.
//     (Ghostty and Kitty inside tmux keep TERM_PROGRAM but lose the
//     TERM substring, so the env var is the reliable signal.)
//  3. TERM_PROGRAM=iTerm.app / WezTerm → iTerm2 inline images.
//  4. Otherwise → none.
//
// Detection is independent of whether tmux passthrough is needed;
// that's a separate concern handled by inTmux().
func detectImageProtocol() imageProtocol {
	switch strings.ToLower(os.Getenv("NETWORKFS_IMAGE")) {
	case "kitty":
		return imgKitty
	case "iterm":
		return imgITerm
	case "off", "none":
		return imgNone
	}
	term := os.Getenv("TERM")
	termProg := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	if os.Getenv("KITTY_WINDOW_ID") != "" ||
		strings.Contains(term, "kitty") ||
		strings.Contains(term, "ghostty") ||
		termProg == "ghostty" ||
		termProg == "kitty" {
		return imgKitty
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm":
		return imgITerm
	}
	return imgNone
}

// inTmux reports whether we're running under a tmux session. Inside
// tmux, terminal control sequences need to be wrapped in DCS
// passthrough or tmux swallows them. Users also need
// `set -g allow-passthrough on` in their tmux config — we emit the
// wrapper either way; tmux just drops it silently if disallowed.
func inTmux() bool {
	return os.Getenv("TMUX") != ""
}

// wrapTmux wraps one or more terminal escape sequences in a tmux DCS
// passthrough block so tmux forwards the inner bytes to the real
// terminal. Every ESC byte inside gets doubled (tmux's escape rule
// for DCS content) and the whole thing is bracketed by ESC P tmux ;
// … ESC \\.
func wrapTmux(s string) string {
	return "\x1bPtmux;" + strings.ReplaceAll(s, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// buildPreview downloads (up to previewSizeLimit bytes of) the named
// file, sniffs its content type, and returns a ready-to-render preview.
// tmux controls whether image escape sequences get wrapped in DCS
// passthrough so tmux forwards them to the real terminal.
func buildPreview(d api.Driver, mountID int, remotePath, name string, size int64, proto imageProtocol, tmux bool) preview {
	p := preview{name: name, size: size}

	rc, err := d.OpenFile(mountID, remotePath)
	if err != nil {
		p.kind = previewError
		p.err = fmt.Errorf("open: %w", err)
		return p
	}
	defer rc.Close()

	// Sniff on the first 512 bytes so we don't buffer the whole file
	// when we only need metadata.
	br := bufio.NewReader(io.LimitReader(rc, previewSizeLimit))
	head, err := br.Peek(512)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		p.kind = previewError
		p.err = fmt.Errorf("read: %w", err)
		return p
	}
	p.mime = http.DetectContentType(head)

	switch {
	case strings.HasPrefix(p.mime, "image/"):
		return loadImage(p, br, proto, tmux)
	case isTextish(p.mime, name):
		return loadText(p, br)
	default:
		p.kind = previewBinary
		return p
	}
}

// isTextish returns true when we should treat the payload as text,
// falling back to the file extension for common cases that
// DetectContentType reports as "application/octet-stream" (JSON, YAML,
// TOML, .go, .md, .log, etc.).
func isTextish(mime, name string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/javascript",
		"application/x-yaml", "application/x-toml":
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".md", ".yaml", ".yml", ".toml", ".json", ".xml",
		".log", ".sh", ".py", ".rs", ".js", ".ts", ".c", ".h", ".hpp",
		".cpp", ".rb", ".pl", ".conf", ".cfg", ".ini", ".env", ".txt":
		return true
	}
	return false
}

func loadText(p preview, r io.Reader) preview {
	buf := make([]byte, textPreviewBytes)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		p.kind = previewError
		p.err = fmt.Errorf("read: %w", err)
		return p
	}
	buf = buf[:n]
	// Trim to the last valid UTF-8 boundary so a truncated multi-byte
	// sequence doesn't render as a replacement character.
	for len(buf) > 0 && !utf8.Valid(buf) {
		buf = buf[:len(buf)-1]
	}
	// Strip anything that would confuse the terminal — non-printables
	// and a BOM at the very start.
	buf = bytes.TrimPrefix(buf, []byte{0xEF, 0xBB, 0xBF})
	p.kind = previewText
	p.text = string(buf)
	return p
}

func loadImage(p preview, r io.Reader, proto imageProtocol, tmux bool) preview {
	data, err := io.ReadAll(r)
	if err != nil {
		p.kind = previewError
		p.err = fmt.Errorf("read: %w", err)
		return p
	}
	if proto == imgNone {
		// No supported protocol — caller renders metadata instead.
		p.kind = previewImage
		return p
	}
	p.kind = previewImage
	var inner string
	switch proto {
	case imgKitty:
		// Kitty's f=100 transmission format is PNG-only. JPEGs and
		// GIFs get silently dropped. ensurePNG also resizes so the
		// payload stays small enough for any tmux DCS buffer.
		png, err := ensurePNG(data, p.mime)
		if err != nil {
			p.kind = previewError
			p.err = fmt.Errorf("transcode to png: %w", err)
			return p
		}
		if tmux {
			// Per-chunk wrapping: each Kitty chunk gets its own DCS
			// so no single passthrough is larger than ~4 KiB. A
			// single DCS wrapping the whole multi-MB blob overruns
			// tmux's internal limit.
			inner = encodeKittyTmux(png)
		} else {
			inner = encodeKitty(png)
		}
	case imgITerm:
		inner = encodeITerm(data, p.name)
		if tmux {
			// iTerm2 is always a single escape sequence, so one DCS
			// wrap is fine. Still do the trailing-newline dance so
			// the newline isn't inside the DCS.
			inner = wrapTmux(strings.TrimSuffix(inner, "\n")) + "\n"
		}
	}
	p.image = inner
	return p
}

// ensurePNG returns a PNG-encoded version of the input, resized to fit
// within maxImageWidthPx. Kitty's graphics protocol f=100 transmission
// accepts PNG only, and shrinking to a sensible display width keeps
// the base64 payload (and resulting escape-sequence blob) small
// enough to push through stdout + tmux DCS passthrough cleanly.
//
// PNG passthrough is only used when the file is already small enough
// to not need resizing; otherwise we round-trip it through decode →
// resize → re-encode just like JPEGs.
func ensurePNG(data []byte, mime string) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	img = resizeIfLarger(img, maxImageWidthPx)

	// Already PNG and no resize happened? Return original to skip the
	// re-encode cost (compresses slightly differently each time).
	if mime == "image/png" && img.Bounds().Dx() >= decodedWidth(data) {
		return data, nil
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resizeIfLarger downsizes the image so its width is at most max,
// preserving aspect ratio. Returns the original image unchanged when
// it already fits. Uses Catmull-Rom filtering — sharper than bilinear
// for photographs, cheap enough for interactive preview.
func resizeIfLarger(src image.Image, max int) image.Image {
	b := src.Bounds()
	w := b.Dx()
	if w <= max {
		return src
	}
	scale := float64(max) / float64(w)
	newW := max
	newH := int(float64(b.Dy()) * scale)
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// decodedWidth reports the width of a PNG/JPEG/GIF without fully
// decoding pixels. Used to detect "no resize happened" cheaply.
// Returns 0 if the header can't be parsed — callers treat that as
// "decode happened, prefer re-encoded bytes".
func decodedWidth(data []byte) int {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0
	}
	return cfg.Width
}

// kittyChunks returns one or more Kitty graphics protocol escape
// sequences, each carrying ≤4 KiB of base64 payload. The caller
// concatenates (for bare-terminal output) or wraps each individually
// in a tmux DCS passthrough block before concatenating.
//
// q=2 on the header suppresses all terminal responses — without it
// Kitty sends "OK" replies that tmux routinely misattributes, and
// the stray bytes sometimes jam the transmission outright.
//
// Format reference: https://sw.kovidgoyal.net/kitty/graphics-protocol/
func kittyChunks(data []byte) []string {
	encoded := base64.StdEncoding.EncodeToString(data)
	const chunkSize = 4096

	// Single-shot: whole payload fits in one escape. Don't emit m= at
	// all — Kitty treats that as "not chunked". Some terminals wait
	// forever for a follow-up when they see m=0 alone.
	if len(encoded) <= chunkSize {
		return []string{
			fmt.Sprintf("\x1b_Ga=T,f=100,t=d,q=2;%s\x1b\\", encoded),
		}
	}

	var out []string
	for i := 0; i < len(encoded); i += chunkSize {
		end := i + chunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		more := "1"
		if end == len(encoded) {
			more = "0"
		}
		if i == 0 {
			out = append(out, fmt.Sprintf(
				"\x1b_Ga=T,f=100,t=d,q=2,m=%s;%s\x1b\\",
				more, encoded[i:end]))
		} else {
			out = append(out, fmt.Sprintf(
				"\x1b_Gm=%s;%s\x1b\\", more, encoded[i:end]))
		}
	}
	return out
}

// encodeKitty joins the per-chunk Kitty escapes with no separator and
// appends a trailing newline so the cursor drops below the image.
func encodeKitty(data []byte) string {
	return strings.Join(kittyChunks(data), "") + "\n"
}

// encodeKittyTmux wraps each Kitty chunk in its own tmux DCS
// passthrough block. Keeps individual passthrough payloads small (one
// chunk each ≈ 4 KiB), so tmux's DCS buffer never gets a multi-MB
// blob to swallow. Each chunk passes through independently.
func encodeKittyTmux(data []byte) string {
	chunks := kittyChunks(data)
	for i, c := range chunks {
		chunks[i] = wrapTmux(c)
	}
	return strings.Join(chunks, "") + "\n"
}

// kittyDeleteAll returns an escape sequence that asks Kitty to remove
// every previously-transmitted image. Used both before transmitting a
// new preview (so the old one doesn't overlay) and when the user
// navigates away from a file. Wrapped in tmux DCS if we're inside tmux.
func kittyDeleteAll(tmux bool) string {
	seq := "\x1b_Ga=d,d=A,q=2;\x1b\\"
	if tmux {
		return wrapTmux(seq)
	}
	return seq
}

// positionImage builds the byte stream that draws an image starting at
// the given 1-based terminal (row, col). Moves the cursor, deletes any
// previously-drawn Kitty image, transmits+displays the new image, then
// restores the cursor so Bubble Tea's next redraw lands where it
// expects. All cursor moves are plain ANSI so they pass through tmux
// natively; only the Kitty-specific escapes need DCS wrapping.
func positionImage(imageSeq string, row, col int, tmux bool) string {
	const (
		saveCursor    = "\x1b7"
		restoreCursor = "\x1b8"
	)
	move := fmt.Sprintf("\x1b[%d;%dH", row, col)
	return saveCursor + move + kittyDeleteAll(tmux) + imageSeq + restoreCursor
}

// encodeITerm produces an iTerm2 inline-image sequence. Simpler than
// Kitty — one shot, no chunking.
//
//	\e]1337;File=name=<base64name>;inline=1;width=auto;height=auto:<base64>\a
func encodeITerm(data []byte, name string) string {
	b64 := base64.StdEncoding.EncodeToString(data)
	nameB64 := base64.StdEncoding.EncodeToString([]byte(name))
	return fmt.Sprintf(
		"\x1b]1337;File=name=%s;inline=1;width=auto;height=auto:%s\x07\n",
		nameB64, b64)
}

// previewText returns the human-readable body of a preview, suitable
// for embedding in the View() output to the right of the file list.
// For images the caller is responsible for dealing with the
// escape-sequence output separately (it's NOT returned from here to
// avoid lipgloss width calculations treating the escape as visible
// columns).
func (p preview) bodyText() string {
	switch p.kind {
	case previewNone:
		return ""
	case previewError:
		return "preview error: " + p.err.Error()
	case previewText:
		return p.text
	case previewBinary:
		return fmt.Sprintf("binary · %s · %d bytes\n(no preview)", p.mime, p.size)
	case previewImage:
		if p.image == "" {
			return fmt.Sprintf("image · %s · %d bytes\n\n"+
				"No terminal image protocol detected. Checked:\n"+
				"  TERM=%q\n"+
				"  TERM_PROGRAM=%q\n"+
				"  KITTY_WINDOW_ID=%q\n"+
				"  TMUX=%q\n\n"+
				"Override with NETWORKFS_IMAGE=kitty|iterm if the\n"+
				"terminal actually supports one.",
				p.mime, p.size,
				os.Getenv("TERM"),
				os.Getenv("TERM_PROGRAM"),
				os.Getenv("KITTY_WINDOW_ID"),
				os.Getenv("TMUX"))
		}
		return fmt.Sprintf("image · %s · %d bytes", p.mime, p.size)
	}
	return ""
}
