package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	// ErrCancelled is returned when the user exits the interactive picker via Esc, q, or EOF.
	ErrCancelled = errors.New("setup cancelled")
	// ErrInterrupted is returned when the user interrupts the interactive picker via Ctrl+C.
	ErrInterrupted = errors.New("setup interrupted")
)

// Key represents a parsed interactive keypress in the setup multi-select picker.
type Key int

const (
	KeyUnknown Key = iota
	KeyUp
	KeyDown
	KeySpace
	KeyToggleAll
	KeyEnter
	KeyEsc
	KeyQuit
	KeyCtrlC
)

// PickerItem describes one selectable target row in the interactive setup checklist.
type PickerItem struct {
	ID          string
	Label       string
	DisplayPath string
	Status      string
	Selected    bool
}

// PickerConfig configures interactive multi-select checklist execution.
type PickerConfig struct {
	In        io.Reader
	Out       io.Writer
	Scope     Scope
	Uninstall bool
	DryRun    bool
	Color     bool
	RawMode   func() (restore func(), err error)
}

// IsTerminal reports whether v is an *os.File attached to an interactive terminal.
func IsTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok || f == nil {
		return false
	}
	return isFileTerminal(f)
}

// DiscoverPickerItems inspects the target global or project paths and builds the
// interactive checklist items with status badges and safe default pre-selections.
func DiscoverPickerItems(opts Options) ([]PickerItem, error) {
	scope := opts.Scope
	if scope == "" {
		scope = ScopeGlobal
	}
	if scope != ScopeGlobal && scope != ScopeProject {
		return nil, fmt.Errorf("invalid setup scope %q (expected %q or %q)", opts.Scope, ScopeGlobal, ScopeProject)
	}

	switch scope {
	case ScopeGlobal:
		homeDir := strings.TrimSpace(opts.HomeDir)
		if homeDir == "" {
			return nil, errors.New("HomeDir is required for global setup")
		}
		homeDir = filepath.Clean(homeDir)
		if !dirExists(homeDir) {
			return nil, fmt.Errorf("home directory %q does not exist", homeDir)
		}

		items := make([]PickerItem, 0, len(defaultGlobalAgents))
		for _, id := range defaultGlobalAgents {
			explicitSpec, ok := resolveOneGlobalTargetWithDSH(homeDir, opts.DSHHome, id, true)
			if !ok {
				continue
			}
			autoSpec, autoInclude := resolveOneGlobalTargetWithDSH(homeDir, opts.DSHHome, id, false)
			detected := autoInclude && autoSpec.skipReason == ""
			configured := fileHasMarkers(explicitSpec.path)

			status := "not detected"
			switch {
			case configured:
				status = "configured"
			case detected:
				status = "detected"
			}

			selected := detected || configured
			if opts.Uninstall {
				selected = configured
			}

			items = append(items, PickerItem{
				ID:          id,
				Label:       fmt.Sprintf("%s (%s)", agentDisplayName(id), id),
				DisplayPath: explicitSpec.displayPath,
				Status:      status,
				Selected:    selected,
			})
		}
		return items, nil

	case ScopeProject:
		projectRoot := strings.TrimSpace(opts.ProjectRoot)
		if projectRoot == "" {
			return nil, errors.New("ProjectRoot is required for project setup")
		}
		projectRoot = filepath.Clean(projectRoot)
		if !dirExists(projectRoot) {
			return nil, fmt.Errorf("project root %q does not exist", projectRoot)
		}

		defs := []struct {
			id      string
			label   string
			relPath string
		}{
			{id: "agents", label: "AGENTS.md (shared standard)", relPath: "AGENTS.md"},
			{id: "claude", label: "Claude Code (CLAUDE.md)", relPath: "CLAUDE.md"},
			{id: "cursor", label: "Cursor (.cursorrules)", relPath: ".cursorrules"},
			{id: "windsurf", label: "Windsurf (.windsurfrules)", relPath: ".windsurfrules"},
		}

		items := make([]PickerItem, 0, len(defs))
		for _, d := range defs {
			fullPath := filepath.Join(projectRoot, d.relPath)
			exists := fileExists(fullPath)
			configured := fileHasMarkers(fullPath)

			status := "not detected"
			switch {
			case configured:
				status = "configured"
			case exists:
				status = "detected"
			case d.id == "agents" && !opts.Uninstall:
				status = "default"
			}

			selected := d.id == "agents" || exists
			if opts.Uninstall {
				selected = configured
			}

			items = append(items, PickerItem{
				ID:          d.id,
				Label:       d.label,
				DisplayPath: d.relPath,
				Status:      status,
				Selected:    selected,
			})
		}
		return items, nil
	}

	return nil, nil
}

// ParseKey decodes the next key event from b and returns the parsed Key and the
// number of bytes consumed. Caller should ensure incomplete escape sequences have
// already had a chance to read trailing bytes via needsMoreEscapeBytes.
func ParseKey(b []byte) (Key, int) {
	if len(b) == 0 {
		return KeyUnknown, 0
	}

	switch b[0] {
	case 0x03:
		return KeyCtrlC, 1
	case '\r':
		if len(b) >= 2 && b[1] == '\n' {
			return KeyEnter, 2
		}
		return KeyEnter, 1
	case '\n':
		return KeyEnter, 1
	case ' ':
		return KeySpace, 1
	case 'k', 'K':
		return KeyUp, 1
	case 'j', 'J':
		return KeyDown, 1
	case 'a', 'A':
		return KeyToggleAll, 1
	case 'q', 'Q':
		return KeyQuit, 1
	case 0x1b:
		if len(b) == 1 {
			return KeyEsc, 1
		}
		switch b[1] {
		case '[':
			// ANSI CSI sequence: ESC [ <params 0x30..0x3f>* <intermediates 0x20..0x2f>* <final 0x40..0x7e>
			idx := 2
			for idx < len(b) && b[idx] >= 0x20 && b[idx] <= 0x3f {
				idx++
			}
			if idx < len(b) && b[idx] >= 0x40 && b[idx] <= 0x7e {
				final := b[idx]
				consumed := idx + 1
				switch final {
				case 'A':
					return KeyUp, consumed
				case 'B':
					return KeyDown, consumed
				default:
					return KeyUnknown, consumed
				}
			}
			return KeyUnknown, idx
		case 'O':
			// SS3 sequence: ESC O <final>
			if len(b) >= 3 {
				switch b[2] {
				case 'A':
					return KeyUp, 3
				case 'B':
					return KeyDown, 3
				default:
					return KeyUnknown, 3
				}
			}
			return KeyUnknown, len(b)
		case 0x1b:
			return KeyEsc, 1
		default:
			return KeyUnknown, 2
		}
	default:
		_, width := utf8.DecodeRune(b)
		if width <= 0 {
			width = 1
		}
		return KeyUnknown, width
	}
}

func needsMoreEscapeBytes(b []byte) bool {
	if len(b) == 0 || b[0] != 0x1b {
		return false
	}
	if len(b) == 1 {
		return true
	}
	if b[1] == 'O' {
		return len(b) < 3
	}
	if b[1] == '[' {
		if len(b) > 16 {
			return false
		}
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return false
			}
			if b[i] < 0x20 || b[i] > 0x3f {
				return false
			}
		}
		return true
	}
	return false
}

// RenderPickerLines renders the lines of a single multi-select picker frame.
func RenderPickerLines(scope Scope, uninstall, dryRun, color bool, items []PickerItem, cursor int) []string {
	if scope == "" {
		scope = ScopeGlobal
	}

	header := fmt.Sprintf("git-by setup (%s)", scope)
	if color {
		header = wrapANSI(ansiBold, header)
	}
	if uninstall {
		tag := "[uninstall]"
		if color {
			tag = wrapANSI(ansiYellow, tag)
		}
		header += " " + tag
	}
	if dryRun {
		tag := "[dry-run]"
		if color {
			tag = wrapANSI(ansiYellow, tag)
		}
		header += " " + tag
	}

	confirmVerb := "install"
	if uninstall {
		confirmVerb = "uninstall"
	}
	hint := fmt.Sprintf("  ↑/↓ move · Space select · a toggle all · Enter %s · q/Esc cancel", confirmVerb)
	if color {
		hint = wrapANSI(ansiDim, hint)
	}

	lines := make([]string, 0, len(items)+5)
	lines = append(lines, header, hint, "")

	labelWidth := 0
	pathWidth := 0
	for _, it := range items {
		if n := utf8.RuneCountInString(it.Label); n > labelWidth {
			labelWidth = n
		}
		if n := utf8.RuneCountInString(it.DisplayPath); n > pathWidth {
			pathWidth = n
		}
	}

	selectedCount := 0
	for i, it := range items {
		if it.Selected {
			selectedCount++
		}
		focused := i == cursor

		pointer := " "
		if focused {
			pointer = ">"
			if color {
				pointer = wrapANSI(ansiBold+ansiCyan, pointer)
			}
		}

		box := "[ ]"
		if it.Selected {
			box = "[x]"
			if color {
				box = wrapANSI(ansiBold+ansiGreen, box)
			}
		} else if color {
			box = wrapANSI(ansiDim, box)
		}

		labelPad := strings.Repeat(" ", max(0, labelWidth-utf8.RuneCountInString(it.Label)))
		pathPad := strings.Repeat(" ", max(0, pathWidth-utf8.RuneCountInString(it.DisplayPath)))

		renderedLabel := it.Label
		renderedPath := it.DisplayPath
		renderedStatus := it.Status

		if color {
			switch {
			case focused:
				renderedLabel = wrapANSI(ansiBold+ansiCyan, it.Label)
			case !it.Selected:
				renderedLabel = wrapANSI(ansiDim, it.Label)
			}
			renderedPath = wrapANSI(ansiDim, it.DisplayPath)

			switch it.Status {
			case "configured":
				renderedStatus = wrapANSI(ansiCyan, it.Status)
			case "detected":
				renderedStatus = wrapANSI(ansiGreen, it.Status)
			case "default":
				renderedStatus = wrapANSI(ansiCyan, it.Status)
			default:
				renderedStatus = wrapANSI(ansiDim, it.Status)
			}
		}

		row := fmt.Sprintf("  %s %s %s%s  %s%s  %s",
			pointer,
			box,
			renderedLabel,
			labelPad,
			renderedPath,
			pathPad,
			renderedStatus,
		)
		lines = append(lines, row)
	}

	footer := fmt.Sprintf("  %d of %d selected", selectedCount, len(items))
	if color {
		footer = wrapANSI(ansiDim, footer)
	}
	lines = append(lines, "", footer)
	return lines
}

// RunPicker displays the interactive multi-select TUI checklist and returns the
// IDs of all checked items when confirmed with Enter.
func RunPicker(cfg PickerConfig, initialItems []PickerItem) ([]string, error) {
	if cfg.In == nil {
		return nil, errors.New("nil picker input reader")
	}
	if cfg.Out == nil {
		return nil, errors.New("nil picker output writer")
	}
	if len(initialItems) == 0 {
		return nil, nil
	}

	items := make([]PickerItem, len(initialItems))
	copy(items, initialItems)

	rawFn := cfg.RawMode
	if rawFn == nil {
		rawFn = func() (func(), error) {
			return enterRawMode(cfg.In, cfg.Out)
		}
	}

	restore, err := rawFn()
	if err != nil {
		return nil, fmt.Errorf("enter raw terminal mode: %w", err)
	}

	renderedLines := 0
	var cleanupOnce sync.Once
	cleanup := func(clearMenu bool) {
		cleanupOnce.Do(func() {
			var buf bytes.Buffer
			if clearMenu && renderedLines > 0 {
				fmt.Fprintf(&buf, "\x1b[%dA\r\x1b[J", renderedLines)
			}
			buf.WriteString("\x1b[?25h")
			_, _ = cfg.Out.Write(buf.Bytes())
			if restore != nil {
				restore()
			}
		})
	}
	defer cleanup(false)

	cursor := 0
	draw := func() error {
		lines := RenderPickerLines(cfg.Scope, cfg.Uninstall, cfg.DryRun, cfg.Color, items, cursor)
		var buf bytes.Buffer
		if renderedLines == 0 {
			buf.WriteString("\x1b[?25l")
		} else {
			fmt.Fprintf(&buf, "\x1b[%dA", renderedLines)
		}
		for _, line := range lines {
			buf.WriteString("\r\x1b[2K")
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		renderedLines = len(lines)
		_, werr := cfg.Out.Write(buf.Bytes())
		return werr
	}

	if err := draw(); err != nil {
		return nil, err
	}

	readBuf := make([]byte, 64)
	tailBuf := make([]byte, 16)
	var pending []byte

	for {
		if len(pending) == 0 {
			n, rerr := cfg.In.Read(readBuf)
			if n > 0 {
				pending = append(pending, readBuf[:n]...)
			}
			if n == 0 {
				if errors.Is(rerr, io.EOF) {
					cleanup(true)
					return nil, ErrCancelled
				}
				if rerr != nil {
					cleanup(true)
					return nil, rerr
				}
				continue
			}
		}

		for attempts := 0; attempts < 3 && needsMoreEscapeBytes(pending); attempts++ {
			n, handled := readEscapeTail(cfg.In, tailBuf)
			if handled {
				if n == 0 {
					break
				}
				pending = append(pending, tailBuf[:n]...)
				continue
			}
			n, rerr := cfg.In.Read(tailBuf)
			if n > 0 {
				pending = append(pending, tailBuf[:n]...)
			}
			if n == 0 || rerr != nil {
				break
			}
		}

		key, consumed := ParseKey(pending)
		if consumed <= 0 {
			consumed = 1
		}
		pending = pending[consumed:]

		switch key {
		case KeyUp:
			cursor = (cursor - 1 + len(items)) % len(items)
			if err := draw(); err != nil {
				return nil, err
			}
		case KeyDown:
			cursor = (cursor + 1) % len(items)
			if err := draw(); err != nil {
				return nil, err
			}
		case KeySpace:
			items[cursor].Selected = !items[cursor].Selected
			if err := draw(); err != nil {
				return nil, err
			}
		case KeyToggleAll:
			allSelected := true
			for _, it := range items {
				if !it.Selected {
					allSelected = false
					break
				}
			}
			targetState := !allSelected
			for i := range items {
				items[i].Selected = targetState
			}
			if err := draw(); err != nil {
				return nil, err
			}
		case KeyEnter:
			cleanup(true)
			selected := make([]string, 0, len(items))
			for _, it := range items {
				if it.Selected {
					selected = append(selected, it.ID)
				}
			}
			return selected, nil
		case KeyEsc, KeyQuit:
			cleanup(true)
			return nil, ErrCancelled
		case KeyCtrlC:
			cleanup(true)
			return nil, ErrInterrupted
		}
	}
}
