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
	"unicode"
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
	In         io.Reader
	Out        io.Writer
	Scope      Scope
	Uninstall  bool
	DryRun     bool
	Color      bool
	MaxVisible int // optional viewport row cap for items (0 auto-fits terminal height)
	MaxWidth   int // optional terminal column cap (0 auto-detects via termWidth; <=0 means unconstrained)
	RawMode    func() (restore func(), err error)
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

		env := globalEnvFromOptions(opts)
		items := make([]PickerItem, 0, len(defaultGlobalAgents))
		for _, id := range defaultGlobalAgents {
			explicitSpec, ok := resolveOneGlobalTargetWithEnv(homeDir, env, id, true)
			if !ok {
				continue
			}
			autoSpec, autoInclude := resolveOneGlobalTargetWithEnv(homeDir, env, id, false)
			detected := autoInclude && autoSpec.skipReason == ""
			configured := fileHasMarkers(explicitSpec.path) || (autoInclude && fileHasMarkers(autoSpec.path))

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

		items := make([]PickerItem, 0, 1+len(projectDedicatedOrder))

		agentsFull := filepath.Join(projectRoot, "AGENTS.md")
		agentsExists := fileExists(agentsFull)
		agentsConfigured := fileHasMarkers(agentsFull)
		agentsStatus := "not detected"
		switch {
		case agentsConfigured:
			agentsStatus = "configured"
		case agentsExists:
			agentsStatus = "detected"
		case !opts.Uninstall:
			agentsStatus = "default"
		}
		agentsSelected := true
		if opts.Uninstall {
			agentsSelected = agentsConfigured
		}
		items = append(items, PickerItem{
			ID:          "agents",
			Label:       "AGENTS.md (shared standard)",
			DisplayPath: "AGENTS.md",
			Status:      agentsStatus,
			Selected:    agentsSelected,
		})

		for _, id := range projectDedicatedOrder {
			spec, label, detected := resolveProjectDedicatedTarget(projectRoot, id)
			configured := fileHasMarkers(spec.path)

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
				Label:       label,
				DisplayPath: spec.displayPath,
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
	return RenderPickerLinesConstrained(scope, uninstall, dryRun, color, items, cursor, 0, 0)
}

func visibleWindow(total, cursor, maxVisible int) (start, end int) {
	if maxVisible <= 0 || maxVisible >= total {
		return 0, total
	}
	start = cursor - maxVisible/2
	if start < 0 {
		start = 0
	}
	if start+maxVisible > total {
		start = total - maxVisible
	}
	return start, start + maxVisible
}

func runeDisplayWidth(r rune) int {
	if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x20000 && r <= 0x3FFFD)
}

func stringDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

func truncateDisplayEnd(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	if stringDisplayWidth(s) <= maxCols {
		return s
	}
	if maxCols == 1 {
		return "…"
	}

	limit := maxCols - 1
	var b strings.Builder
	b.Grow(len(s))
	used := 0
	for _, r := range s {
		rw := runeDisplayWidth(r)
		if used+rw > limit {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteRune('…')
	return b.String()
}

func truncatePathDisplayHead(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	if stringDisplayWidth(s) <= maxCols {
		return s
	}
	if maxCols == 1 {
		return "…"
	}

	limit := maxCols - 1
	runes := []rune(s)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := runeDisplayWidth(runes[i])
		if used+rw > limit {
			break
		}
		used += rw
		start = i
	}
	// Avoid leaving an orphaned combining mark or zero-width rune directly after "…".
	for start < len(runes) && runeDisplayWidth(runes[start]) == 0 {
		start++
	}

	tail := string(runes[start:])
	if !strings.HasPrefix(tail, "/") {
		if idx := strings.IndexByte(tail, '/'); idx >= 0 {
			aligned := tail[idx:]
			if stringDisplayWidth(aligned) >= max(6, maxCols/2) {
				tail = aligned
			}
		}
	}
	return "…" + tail
}

func renderPickerHeader(scope Scope, uninstall, dryRun, color, constrained bool, availCols int) string {
	plainBase := fmt.Sprintf("git-by setup (%s)", scope)
	plainHeader := plainBase
	if uninstall {
		plainHeader += " [uninstall]"
	}
	if dryRun {
		plainHeader += " [dry-run]"
	}

	if constrained && stringDisplayWidth(plainHeader) > availCols {
		truncated := truncateDisplayEnd(plainHeader, availCols)
		if color {
			return wrapANSI(ansiBold, truncated)
		}
		return truncated
	}

	header := plainBase
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
	return header
}

func renderPickerHint(uninstall, color, constrained bool, availCols int) string {
	confirmVerb := "install"
	if uninstall {
		confirmVerb = "uninstall"
	}
	candidates := []string{
		fmt.Sprintf("  ↑/↓ move · Space select · a toggle all · Enter %s · q/Esc cancel", confirmVerb),
		fmt.Sprintf("  ↑/↓ · Space · a all · Enter %s · q/Esc", confirmVerb),
		"  ↑/↓ · Space · Enter · q/Esc",
	}

	hint := candidates[0]
	if constrained {
		matched := false
		for _, cand := range candidates {
			if stringDisplayWidth(cand) <= availCols {
				hint = cand
				matched = true
				break
			}
		}
		if !matched {
			hint = truncateDisplayEnd(candidates[len(candidates)-1], availCols)
		}
	}
	if color {
		hint = wrapANSI(ansiDim, hint)
	}
	return hint
}

func renderPickerFooter(selectedCount, total, start, end int, color, constrained bool, availCols int) string {
	windowed := end-start < total
	footer := fmt.Sprintf("  %d of %d selected", selectedCount, total)
	if windowed {
		footer += fmt.Sprintf("  (showing %d-%d of %d)", start+1, end, total)
	}
	if constrained && stringDisplayWidth(footer) > availCols {
		if windowed {
			compact := fmt.Sprintf("  %d/%d sel (%d-%d/%d)", selectedCount, total, start+1, end, total)
			if stringDisplayWidth(compact) <= availCols {
				footer = compact
			} else {
				footer = truncateDisplayEnd(compact, availCols)
			}
		} else {
			footer = truncateDisplayEnd(footer, availCols)
		}
	}
	if color {
		footer = wrapANSI(ansiDim, footer)
	}
	return footer
}

func stylePickerLabel(label string, focused, selected, color bool) string {
	if !color {
		return label
	}
	switch {
	case focused:
		return wrapANSI(ansiBold+ansiCyan, label)
	case !selected:
		return wrapANSI(ansiDim, label)
	default:
		return label
	}
}

func stylePickerStatus(status string, color bool) string {
	if !color {
		return status
	}
	switch status {
	case "configured":
		return wrapANSI(ansiCyan, status)
	case "detected":
		return wrapANSI(ansiGreen, status)
	case "default":
		return wrapANSI(ansiCyan, status)
	default:
		return wrapANSI(ansiDim, status)
	}
}

// RenderPickerLinesWindowed renders a picker frame, optionally windowing the item list to
// maxVisible rows (when maxVisible > 0 and maxVisible < len(items)) so small terminal panes
// never scroll past the top of the viewport.
func RenderPickerLinesWindowed(scope Scope, uninstall, dryRun, color bool, items []PickerItem, cursor, maxVisible int) []string {
	return RenderPickerLinesConstrained(scope, uninstall, dryRun, color, items, cursor, maxVisible, 0)
}

// RenderPickerLinesConstrained renders a picker frame constrained to maxVisible item rows
// (when maxVisible > 0) and maxWidth terminal columns (when maxWidth > 0, capping visible
// display width at max(0, maxWidth-1) columns so narrow terminal panes never auto-wrap).
func RenderPickerLinesConstrained(scope Scope, uninstall, dryRun, color bool, items []PickerItem, cursor, maxVisible, maxWidth int) []string {
	if scope == "" {
		scope = ScopeGlobal
	}

	constrained := maxWidth > 0
	availCols := 0
	if constrained {
		availCols = max(0, maxWidth-1)
	}

	header := renderPickerHeader(scope, uninstall, dryRun, color, constrained, availCols)
	hint := renderPickerHint(uninstall, color, constrained, availCols)

	naturalLabelW := 0
	naturalPathW := 0
	naturalStatusW := 0
	selectedCount := 0
	for _, it := range items {
		if it.Selected {
			selectedCount++
		}
		if w := stringDisplayWidth(it.Label); w > naturalLabelW {
			naturalLabelW = w
		}
		if w := stringDisplayWidth(it.DisplayPath); w > naturalPathW {
			naturalPathW = w
		}
		if w := stringDisplayWidth(it.Status); w > naturalStatusW {
			naturalStatusW = w
		}
	}

	const (
		itemPrefixWidth = 8 // "  " + pointer(1) + " " + box(3) + " "
		minPathWidth    = 10
		minLabelWidth   = 8
	)

	type rowTier int
	const (
		tierFull rowTier = iota
		tierCompressPath
		tierHidePath
		tierUltraNarrow
	)

	tier := tierFull
	labelW := naturalLabelW
	pathW := naturalPathW
	if constrained {
		fullWidth := itemPrefixWidth + naturalLabelW + 2 + naturalPathW + 2 + naturalStatusW
		if fullWidth <= availCols {
			tier = tierFull
		} else if remPath := availCols - (itemPrefixWidth + naturalLabelW + 2 + 2 + naturalStatusW); remPath >= minPathWidth {
			tier = tierCompressPath
			labelW = naturalLabelW
			pathW = min(naturalPathW, remPath)
		} else if remLabel := availCols - (itemPrefixWidth + 2 + naturalStatusW); remLabel >= minLabelWidth {
			tier = tierHidePath
			labelW = min(naturalLabelW, remLabel)
		} else {
			tier = tierUltraNarrow
		}
	}

	start, end := visibleWindow(len(items), cursor, maxVisible)
	lines := make([]string, 0, (end-start)+5)
	lines = append(lines, header, hint, "")

	for i := start; i < end; i++ {
		it := items[i]
		focused := i == cursor

		plainPointer := " "
		if focused {
			plainPointer = ">"
		}
		plainBox := "[ ]"
		if it.Selected {
			plainBox = "[x]"
		}

		if tier == tierUltraNarrow && availCols < itemPrefixWidth+1 {
			plainRow := fmt.Sprintf("%s %s %s", plainPointer, plainBox, it.Label)
			row := truncateDisplayEnd(plainRow, availCols)
			if color {
				switch {
				case focused:
					row = wrapANSI(ansiBold+ansiCyan, row)
				case it.Selected:
					row = wrapANSI(ansiGreen, row)
				default:
					row = wrapANSI(ansiDim, row)
				}
			}
			lines = append(lines, row)
			continue
		}

		pointer := plainPointer
		if focused && color {
			pointer = wrapANSI(ansiBold+ansiCyan, pointer)
		}

		box := plainBox
		if it.Selected {
			if color {
				box = wrapANSI(ansiBold+ansiGreen, box)
			}
		} else if color {
			box = wrapANSI(ansiDim, box)
		}

		var row string
		switch tier {
		case tierFull, tierCompressPath:
			plainLabel := it.Label
			plainPath := it.DisplayPath
			if tier == tierCompressPath {
				plainPath = truncatePathDisplayHead(it.DisplayPath, pathW)
			}

			labelPad := strings.Repeat(" ", max(0, labelW-stringDisplayWidth(plainLabel)))
			pathPad := strings.Repeat(" ", max(0, pathW-stringDisplayWidth(plainPath)))

			renderedLabel := stylePickerLabel(plainLabel, focused, it.Selected, color)
			renderedPath := plainPath
			if color {
				renderedPath = wrapANSI(ansiDim, plainPath)
			}
			renderedStatus := stylePickerStatus(it.Status, color)

			row = fmt.Sprintf("  %s %s %s%s  %s%s  %s",
				pointer,
				box,
				renderedLabel,
				labelPad,
				renderedPath,
				pathPad,
				renderedStatus,
			)

		case tierHidePath:
			plainLabel := truncateDisplayEnd(it.Label, labelW)
			labelPad := strings.Repeat(" ", max(0, labelW-stringDisplayWidth(plainLabel)))

			renderedLabel := stylePickerLabel(plainLabel, focused, it.Selected, color)
			renderedStatus := stylePickerStatus(it.Status, color)

			row = fmt.Sprintf("  %s %s %s%s  %s",
				pointer,
				box,
				renderedLabel,
				labelPad,
				renderedStatus,
			)

		case tierUltraNarrow:
			narrowLabelW := availCols - itemPrefixWidth
			plainLabel := truncateDisplayEnd(it.Label, narrowLabelW)
			renderedLabel := stylePickerLabel(plainLabel, focused, it.Selected, color)
			row = fmt.Sprintf("  %s %s %s", pointer, box, renderedLabel)
		}

		lines = append(lines, row)
	}

	footer := renderPickerFooter(selectedCount, len(items), start, end, color, constrained, availCols)
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
		maxVis := cfg.MaxVisible
		if maxVis <= 0 {
			if h := termHeight(cfg.Out); h > 0 && len(items)+5 >= h {
				maxVis = max(3, h-6)
			}
		}
		maxW := cfg.MaxWidth
		if maxW <= 0 {
			maxW = termWidth(cfg.Out)
		}
		lines := RenderPickerLinesConstrained(cfg.Scope, cfg.Uninstall, cfg.DryRun, cfg.Color, items, cursor, maxVis, maxW)
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
