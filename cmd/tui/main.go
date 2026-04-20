// cmd/tui/main.go - Interactive file browser for exercising go-networkfs drivers.
//
// Lets you pick any registered driver, fill in its config, mount it and
// browse the remote filesystem. Useful as an integration harness: if the
// TUI works against a real server, the Go package is wired up correctly.

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	"github.com/christhomas/go-networkfs/pkg/api"

	// Register all drivers.
	_ "github.com/christhomas/go-networkfs/dropbox"
	_ "github.com/christhomas/go-networkfs/ftp"
	_ "github.com/christhomas/go-networkfs/gdrive"
	_ "github.com/christhomas/go-networkfs/onedrive"
	_ "github.com/christhomas/go-networkfs/s3"
	_ "github.com/christhomas/go-networkfs/sftp"
	_ "github.com/christhomas/go-networkfs/smb"
	_ "github.com/christhomas/go-networkfs/webdav"
)

// presetFileName is the YAML file the TUI looks for next to its binary
// (and the current working directory as a fallback). It contains named
// account presets. Selected with `--account <name>`, an account
// prefills the config form and picks the right driver:
//
//	accounts:
//	  docker-ftp:
//	    type: ftp
//	    host: localhost
//	    port: "2121"
//	    user: testuser
//	    pass: testpass
//	  docker-sftp:
//	    type: sftp
//	    host: localhost
//	    port: "2223"
//	    user: testuser
//	    pass: testpass
//	    insecure_host_key: "true"
//
// `type` selects the driver (ftp/sftp/smb/dropbox/webdav); every other
// key is passed through to that driver's Mount(). Without --account the
// TUI starts at the driver picker as before.
const presetFileName = ".env.yaml"

// presetFile is the YAML schema.
type presetFile struct {
	Accounts map[string]map[string]string `yaml:"accounts"`
}

// configField describes one input on the config screen.
type configField struct {
	key    string
	label  string
	def    string
	secret bool
}

// driverSchemas maps a driver type id to the fields we prompt for. Keep
// this in sync with the corresponding driver's Mount() expectations.
var driverSchemas = map[int][]configField{
	1: { // FTP
		{key: "host", label: "host"},
		{key: "port", label: "port", def: "21"},
		{key: "user", label: "user"},
		{key: "pass", label: "pass", secret: true},
		{key: "root", label: "root", def: "/"},
		{key: "ftps", label: "ftps (true/false)"},
	},
	2: { // SFTP
		{key: "host", label: "host"},
		{key: "port", label: "port", def: "22"},
		{key: "user", label: "user"},
		{key: "pass", label: "pass", secret: true},
		{key: "root", label: "root", def: "/"},
		{key: "insecure_host_key", label: "insecure host key (true/false)"},
	},
	3: { // SMB
		{key: "host", label: "host"},
		{key: "port", label: "port", def: "445"},
		{key: "share", label: "share"},
		{key: "user", label: "user"},
		{key: "pass", label: "pass", secret: true},
		{key: "domain", label: "domain"},
	},
	4: { // Dropbox
		{key: "token", label: "access token", secret: true},
		{key: "root", label: "root (blank = Dropbox root)"},
	},
	5: { // WebDAV
		{key: "url", label: "url"},
		{key: "user", label: "user"},
		{key: "pass", label: "pass", secret: true},
		{key: "insecure", label: "skip TLS verify (true/false)"},
	},
	6: { // GDrive
		{key: "client_id", label: "client id"},
		{key: "client_secret", label: "client secret", secret: true},
		{key: "refresh_token", label: "refresh token", secret: true},
	},
	7: { // S3
		{key: "endpoint", label: "endpoint (host[:port])"},
		{key: "region", label: "region", def: "us-east-1"},
		{key: "bucket", label: "bucket"},
		{key: "access_key_id", label: "access key id"},
		{key: "secret_access_key", label: "secret access key", secret: true},
		{key: "secure", label: "use TLS (true/false)", def: "true"},
		{key: "use_path_style", label: "path-style addressing (true/false)"},
		{key: "prefix", label: "key prefix"},
	},
	8: { // OneDrive
		{key: "client_id", label: "client id"},
		{key: "client_secret", label: "client secret (blank for PKCE)", secret: true},
		{key: "refresh_token", label: "refresh token", secret: true},
	},
}

var driverNames = map[int]string{
	1: "FTP",
	2: "SFTP",
	3: "SMB",
	4: "Dropbox",
	5: "WebDAV",
	6: "GDrive",
	7: "S3",
	8: "OneDrive",
}

type screen int

const (
	screenPicker screen = iota
	screenConfig
	screenBrowser
)

type model struct {
	screen screen

	// picker
	driverTypes []int
	pickerIdx   int

	// config
	selectedDriver int
	fields         []configField
	values         []string
	fieldIdx       int


	// browser
	driver    api.Driver
	mountID   int
	cwd       string
	entries   []api.FileInfo
	browseIdx int
	status    string

	// preview state; populated by a previewMsg after the user opens a
	// file. A new Enter on a different file supersedes the previous
	// preview.
	preview  preview
	imgProto imageProtocol
	imgTmux  bool

	width  int
	height int
}

func initialModel() model {
	types := api.ListDriverTypes()
	sort.Ints(types)
	return model{
		screen:      screenPicker,
		driverTypes: types,
		imgProto:    detectImageProtocol(),
		imgTmux:     inTmux(),
	}
}

// resolveAccount loads .env.yaml, picks the named account, and returns
// the driver type id plus the config map to hand to Mount(). The
// account's `type` field is consumed here (it's metadata about which
// driver to use, not a driver config key) and stripped from the result.
func resolveAccount(name string) (int, map[string]string, error) {
	file := loadPresetFile()
	if len(file.Accounts) == 0 {
		return 0, nil, fmt.Errorf("--account %q given but no %s found in %s",
			name, presetFileName, strings.Join(presetSearchPaths(), " or "))
	}
	acct, ok := file.Accounts[name]
	if !ok {
		known := make([]string, 0, len(file.Accounts))
		for k := range file.Accounts {
			known = append(known, k)
		}
		sort.Strings(known)
		return 0, nil, fmt.Errorf("--account %q not in %s (known: %s)",
			name, presetFileName, strings.Join(known, ", "))
	}
	typeName := strings.ToLower(strings.TrimSpace(acct["type"]))
	if typeName == "" {
		return 0, nil, fmt.Errorf("account %q: missing 'type' field", name)
	}
	driverType, ok := driverTypeByName[typeName]
	if !ok {
		return 0, nil, fmt.Errorf("account %q: unknown type %q", name, typeName)
	}
	cfg := make(map[string]string, len(acct))
	for k, v := range acct {
		if k == "type" {
			continue
		}
		cfg[k] = v
	}
	return driverType, cfg, nil
}

// driverTypeByName is the inverse of driverNames, mapping a lowercased
// driver name (e.g. "ftp") to its type id. Populated by init() so new
// entries in driverNames are picked up without manual bookkeeping.
var driverTypeByName = map[string]int{}

func init() {
	for id, name := range driverNames {
		driverTypeByName[strings.ToLower(name)] = id
	}
}

// loadPresetFile looks for .env.yaml next to the TUI binary first,
// then the current working directory. Any error (missing file,
// unreadable, invalid YAML) produces an empty presetFile rather than
// blocking startup — a TUI without presets is still fine.
func loadPresetFile() presetFile {
	for _, p := range presetSearchPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var out presetFile
		if err := yaml.Unmarshal(data, &out); err != nil {
			continue
		}
		return out
	}
	return presetFile{}
}

func presetSearchPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), presetFileName))
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, presetFileName))
	}
	return paths
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Size + ctrl+c are global; handle first so every screen gets them.
	if wm, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = wm.Width, wm.Height
		return m, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// Non-key messages (entriesMsg, previewMsg, etc.) need to reach
	// the active screen's handler — the previous gatekeep-by-type only
	// dispatched tea.KeyMsg, silently dropping async results.
	switch m.screen {
	case screenPicker:
		if km, ok := msg.(tea.KeyMsg); ok {
			return m.updatePicker(km)
		}
	case screenConfig:
		if km, ok := msg.(tea.KeyMsg); ok {
			return m.updateConfig(km)
		}
	case screenBrowser:
		return m.updateBrowser(msg)
	}
	return m, nil
}

func (m model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.pickerIdx > 0 {
			m.pickerIdx--
		}
	case "down", "j":
		if m.pickerIdx < len(m.driverTypes)-1 {
			m.pickerIdx++
		}
	case "enter":
		if len(m.driverTypes) == 0 {
			return m, nil
		}
		m.selectedDriver = m.driverTypes[m.pickerIdx]
		m.fields = driverSchemas[m.selectedDriver]
		m.values = make([]string, len(m.fields))
		for i, f := range m.fields {
			m.values[i] = f.def
		}
		m.fieldIdx = 0
		m.screen = screenConfig
		m.status = ""
	}
	return m, nil
}

func (m model) updateConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenPicker
		return m, nil
	case "tab", "down":
		if m.fieldIdx < len(m.fields)-1 {
			m.fieldIdx++
		}
	case "shift+tab", "up":
		if m.fieldIdx > 0 {
			m.fieldIdx--
		}
	case "enter":
		if m.fieldIdx < len(m.fields)-1 {
			m.fieldIdx++
			return m, nil
		}
		return m.mount()
	case "backspace":
		v := m.values[m.fieldIdx]
		if len(v) > 0 {
			m.values[m.fieldIdx] = v[:len(v)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.values[m.fieldIdx] += msg.String()
		}
	}
	return m, nil
}

func (m model) mount() (tea.Model, tea.Cmd) {
	drv, ok := api.GetDriver(m.selectedDriver)
	if !ok {
		m.status = "driver not registered"
		return m, nil
	}
	config := make(map[string]string, len(m.fields))
	for i, f := range m.fields {
		if v := strings.TrimSpace(m.values[i]); v != "" {
			config[f.key] = v
		}
	}
	m.mountID = 1
	if err := drv.Mount(m.mountID, config); err != nil {
		m.status = "mount failed: " + err.Error()
		return m, nil
	}
	m.driver = drv
	m.cwd = "/"
	m.screen = screenBrowser
	return m, m.refreshCmd()
}

type entriesMsg struct {
	entries []api.FileInfo
	err     error
}

// previewMsg carries a completed file preview back to the browser.
// Kicked off by the Enter handler when the highlighted entry is a
// file. Arrival clears any in-flight preview from a previous Enter.
type previewMsg struct{ preview preview }

func (m model) refreshCmd() tea.Cmd {
	d, id, path := m.driver, m.mountID, m.cwd
	return func() tea.Msg {
		e, err := d.ListDir(id, path)
		return entriesMsg{entries: e, err: err}
	}
}

// emitImageCmd writes the given escape sequence straight to stdout,
// bypassing Bubble Tea's renderer. For Kitty / iTerm2 image protocols
// this is the only reliable approach — the renderer assumes its View
// output is pure text to diff line-by-line, and mangles or truncates
// long non-printing escape blobs. Images draw as an overlay so they
// stay visible even when Bubble Tea redraws the character grid.
func emitImageCmd(seq string) tea.Cmd {
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(seq)
		return nil
	}
}

// cleanupImageCmd emits just the Kitty delete-all escape so any image
// lingering from a previous preview goes away when the user navigates.
// iTerm2 images live inside the text cell grid, so they get overwritten
// naturally by Bubble Tea's next redraw and don't need explicit cleanup.
func cleanupImageCmd(tmux bool) tea.Cmd {
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(kittyDeleteAll(tmux))
		return nil
	}
}

// rightPanelTopLeft returns the 1-based (row, col) of the first usable
// character cell inside the right-hand preview panel. Kitty draws an
// image at the current cursor position, so we move the cursor there
// before transmitting. Layout is: row 1 = title, row 2 = mime/size
// meta, row 3 = blank, row 4 = body (text or image).
func (m model) rightPanelTopLeft() (row, col int) {
	leftW := m.width * 4 / 10
	if leftW < 24 {
		leftW = 24
	}
	// "  " separator between panels + 1-col buffer.
	return 4, leftW + 3
}

func (m model) previewCmd(e api.FileInfo) tea.Cmd {
	d, id := m.driver, m.mountID
	remote := joinPath(m.cwd, e.Name)
	proto := m.imgProto
	tmux := m.imgTmux
	return func() tea.Msg {
		return previewMsg{preview: buildPreview(d, id, remote, e.Name, e.Size, proto, tmux)}
	}
}

func (m model) updateBrowser(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case entriesMsg:
		hadImage := m.preview.kind == previewImage
		if msg.err != nil {
			m.status = "list error: " + msg.err.Error()
			m.entries = nil
		} else {
			m.entries = msg.entries
			m.browseIdx = 0
			m.status = fmt.Sprintf("%d entries", len(m.entries))
		}
		// Any navigation invalidates the preview.
		m.preview = preview{}
		if hadImage {
			return m, cleanupImageCmd(m.imgTmux)
		}
		return m, nil
	case previewMsg:
		m.preview = msg.preview
		switch m.preview.kind {
		case previewError:
			m.status = "preview error"
		case previewText:
			m.status = fmt.Sprintf("text preview · %s · %d bytes", m.preview.mime, m.preview.size)
		case previewImage:
			m.status = fmt.Sprintf("image preview · %s · %d bytes", m.preview.mime, m.preview.size)
			if m.preview.image != "" {
				// Build: save cursor → move to right-panel origin →
				// delete any previous Kitty image → transmit + display
				// the new one → restore cursor. Cursor moves are plain
				// ANSI (tmux passes them through natively); only the
				// Kitty-specific escapes need DCS wrapping, which
				// buildPreview already applied.
				row, col := m.rightPanelTopLeft()
				seq := positionImage(m.preview.image, row, col, m.imgTmux)
				return m, emitImageCmd(seq)
			}
		case previewBinary:
			m.status = fmt.Sprintf("no preview · %s · %d bytes", m.preview.mime, m.preview.size)
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q":
			if m.driver != nil {
				_ = m.driver.Unmount(m.mountID)
			}
			return m, tea.Quit
		case "up", "k":
			if m.browseIdx > 0 {
				m.browseIdx--
				hadImage := m.preview.kind == previewImage
				m.preview = preview{}
				if hadImage {
					return m, cleanupImageCmd(m.imgTmux)
				}
			}
		case "down", "j":
			if m.browseIdx < len(m.entries)-1 {
				m.browseIdx++
				hadImage := m.preview.kind == previewImage
				m.preview = preview{}
				if hadImage {
					return m, cleanupImageCmd(m.imgTmux)
				}
			}
		case "enter":
			if len(m.entries) == 0 {
				return m, nil
			}
			e := m.entries[m.browseIdx]
			if !e.IsDir {
				m.status = "loading preview: " + e.Name
				return m, m.previewCmd(e)
			}
			m.cwd = joinPath(m.cwd, e.Name)
			return m, m.refreshCmd()
		case "backspace", "h", "left":
			if m.cwd == "/" || m.cwd == "" {
				return m, nil
			}
			m.cwd = parentPath(m.cwd)
			return m, m.refreshCmd()
		case "r":
			return m, m.refreshCmd()
		}
	}
	return m, nil
}

func joinPath(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return strings.TrimRight(dir, "/") + "/" + name
}

func parentPath(p string) string {
	p = strings.TrimRight(p, "/")
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	highlightLine = lipgloss.NewStyle().Background(lipgloss.Color("237"))
)

func (m model) View() string {
	switch m.screen {
	case screenPicker:
		return m.viewPicker()
	case screenConfig:
		return m.viewConfig()
	case screenBrowser:
		return m.viewBrowser()
	}
	return ""
}

func (m model) viewPicker() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("go-networkfs — pick a driver"))
	b.WriteString("\n\n")
	if len(m.driverTypes) == 0 {
		b.WriteString(errorStyle.Render("no drivers registered"))
		return b.String()
	}
	for i, t := range m.driverTypes {
		name := driverNames[t]
		if name == "" {
			name = fmt.Sprintf("driver-%d", t)
		}
		line := fmt.Sprintf("  [%d] %s", t, name)
		if i == m.pickerIdx {
			line = cursorStyle.Render("> " + line[2:])
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("↑/↓ move · enter select · q quit"))
	return b.String()
}

func (m model) viewConfig() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("configure " + driverNames[m.selectedDriver]))
	b.WriteString("\n\n")
	for i, f := range m.fields {
		val := m.values[i]
		if f.secret && val != "" {
			val = strings.Repeat("•", len(val))
		}
		line := fmt.Sprintf("  %-24s %s", f.label+":", val)
		if i == m.fieldIdx {
			line = cursorStyle.Render("> "+line[2:]) + cursorStyle.Render("_")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if m.status != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render(m.status))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("tab/↑↓ move · enter next/mount · esc back · ctrl+c quit"))
	return b.String()
}

func (m model) viewBrowser() string {
	left := m.viewBrowserLeft()
	right := m.viewBrowserRight()

	// Layout: 40% / 60% split when the terminal width is known, else
	// just the left column.
	if m.width < 60 || right == "" {
		return left
	}
	leftW := m.width * 4 / 10
	if leftW < 24 {
		leftW = 24
	}
	rightW := m.width - leftW - 2
	if rightW < 20 {
		return left
	}

	leftPanel := lipgloss.NewStyle().Width(leftW).Render(left)
	rightPanel := lipgloss.NewStyle().Width(rightW).Render(right)
	// Image escape sequences are emitted directly to stdout via
	// emitImageCmd when the preview arrives — not from View() — so
	// there's nothing special to append here. The terminal's graphics
	// protocol draws the image as an overlay on top of whatever text
	// Bubble Tea renders.
	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, "  ", rightPanel)
}

func (m model) viewBrowserLeft() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(driverNames[m.selectedDriver] + "  " + m.cwd))
	b.WriteString("\n\n")
	if len(m.entries) == 0 {
		b.WriteString(dimStyle.Render("  (empty)"))
		b.WriteString("\n")
	}
	for i, e := range m.entries {
		marker := "  "
		if e.IsDir {
			marker = "/ "
		}
		line := fmt.Sprintf("%s%s", marker, e.Name)
		if !e.IsDir {
			line += dimStyle.Render(fmt.Sprintf("  (%d B)", e.Size))
		}
		if i == m.browseIdx {
			line = highlightLine.Render(cursorStyle.Render("> ") + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if m.status != "" {
		b.WriteString(dimStyle.Render(m.status))
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render("↑/↓ move · enter preview · backspace up · r refresh · q quit"))
	return b.String()
}

// viewBrowserRight returns the text side of the preview panel.
// Returns "" when there's nothing to preview — the caller collapses
// back to a single-column layout in that case.
func (m model) viewBrowserRight() string {
	if m.preview.kind == previewNone {
		return ""
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("preview: " + m.preview.name))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("%s · %d bytes", m.preview.mime, m.preview.size)))
	b.WriteString("\n\n")
	body := m.preview.bodyText()
	if body != "" {
		b.WriteString(body)
	}
	return b.String()
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"Usage: %s [--account NAME] [path ...]\n\n"+
				"Without --account: launches the interactive driver picker.\n"+
				"With --account NAME and no paths: connects to the account and "+
				"opens the file browser.\n"+
				"With --account NAME and one or more paths: lists each path to "+
				"stdout in tab-separated columns and exits. Useful for scripted "+
				"smoke tests.\n\nFlags:\n",
			os.Args[0])
		flag.PrintDefaults()
	}
	account := flag.String("account", "", "use the named account from .env.yaml")
	imageTest := flag.String("image-test", "", "emit the chosen image protocol's escape sequence for a LOCAL file and exit (diagnostic)")
	flag.Parse()

	// Debug path: reads a local file, emits the image protocol escape
	// sequence for it, exits. Same code path the TUI uses — if this
	// doesn't render in your terminal, the TUI won't either, and
	// you've isolated the problem to the escape sequence itself
	// (or tmux passthrough) rather than the Bubble Tea integration.
	if *imageTest != "" {
		if err := runImageTest(*imageTest); err != nil {
			die(err)
		}
		return
	}

	// No account → legacy interactive flow from the picker.
	if *account == "" {
		runInteractive(initialModel())
		return
	}

	driverType, config, err := resolveAccount(*account)
	if err != nil {
		die(err)
	}
	drv, ok := api.GetDriver(driverType)
	if !ok {
		die(fmt.Errorf("no driver registered for type %d", driverType))
	}
	if err := drv.Mount(1, config); err != nil {
		die(fmt.Errorf("mount %s: %w", *account, err))
	}

	// With positional paths → non-interactive list+print+exit.
	if flag.NArg() > 0 {
		defer drv.Unmount(1)
		for _, p := range flag.Args() {
			if err := printPath(os.Stdout, drv, 1, p); err != nil {
				fmt.Fprintln(os.Stderr, "networkfs:", err)
				os.Exit(1)
			}
		}
		return
	}

	// No paths → jump straight to the browser, skip the form.
	entries, err := drv.ListDir(1, "/")
	if err != nil {
		_ = drv.Unmount(1)
		die(err)
	}
	m := initialModel()
	m.selectedDriver = driverType
	m.driver = drv
	m.mountID = 1
	m.cwd = "/"
	m.entries = entries
	m.screen = screenBrowser
	m.status = fmt.Sprintf("%s · %d entries", *account, len(entries))
	runInteractive(m)
}

func runInteractive(m model) {
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "networkfs:", err)
		os.Exit(1)
	}
}

// runImageTest is a diagnostic: read a LOCAL file, emit exactly the
// image escape sequence the TUI would emit for it, and exit. Lets you
// check "is the escape sequence valid for my terminal" independent of
// any Bubble Tea, driver, or alt-screen-buffer interaction. Logs what
// it's doing to stderr so the output on stdout stays clean.
//
// Usage:
//
//	NETWORKFS_IMAGE=kitty ./networkfs --image-test path/to/pic.jpg
//
// If the image appears, the escape sequence works — any remaining TUI
// preview failure is in Bubble Tea or alt-screen handling. If it
// doesn't, the problem is the sequence itself (or tmux passthrough).
func runImageTest(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	mime := http.DetectContentType(head)

	proto := detectImageProtocol()
	tmux := inTmux()
	fmt.Fprintf(os.Stderr,
		"image-test: path=%s mime=%s proto=%d tmux=%v bytes=%d\n",
		path, mime, proto, tmux, len(data))

	if proto == imgNone {
		return fmt.Errorf("no image protocol detected — try NETWORKFS_IMAGE=kitty or =iterm")
	}

	var inner string
	switch proto {
	case imgKitty:
		png, err := ensurePNG(data, mime)
		if err != nil {
			return fmt.Errorf("transcode to png: %w", err)
		}
		fmt.Fprintf(os.Stderr, "image-test: png payload = %d bytes\n", len(png))
		inner = encodeKitty(png)
	case imgITerm:
		inner = encodeITerm(data, filepath.Base(path))
	}
	if tmux {
		inner = wrapTmux(strings.TrimSuffix(inner, "\n")) + "\n"
	}

	fmt.Fprintf(os.Stderr, "image-test: writing %d bytes of escape sequence\n", len(inner))
	_, err = os.Stdout.WriteString(inner)
	return err
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "networkfs:", err)
	os.Exit(1)
}

// printPath emits a plain-text, tab-separated listing of the given
// path to w. Directories print their entries; files print a single
// line with their own metadata. Output is diff-friendly so callers
// can pipe it into a test harness.
//
// Columns: type(d/-) \t size \t name  — one row per entry.
// Size for directories is reported as "-".
func printPath(w io.Writer, d api.Driver, mountID int, p string) error {
	info, err := d.Stat(mountID, p)
	if err != nil {
		return fmt.Errorf("stat %s: %w", p, err)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()

	if !info.IsDir {
		return writeRow(tw, info)
	}

	entries, err := d.ListDir(mountID, p)
	if err != nil {
		return fmt.Errorf("list %s: %w", p, err)
	}
	for _, e := range entries {
		if err := writeRow(tw, e); err != nil {
			return err
		}
	}
	return nil
}

func writeRow(tw *tabwriter.Writer, fi api.FileInfo) error {
	typeMark := "-"
	size := fmt.Sprintf("%d", fi.Size)
	if fi.IsDir {
		typeMark = "d"
		size = "-"
	}
	_, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", typeMark, size, fi.Name)
	return err
}
