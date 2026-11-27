package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

func TestParseKeySequences(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		input        []byte
		wantKey      Key
		wantConsumed int
	}{
		{name: "csi_up", input: []byte("\x1b[Arest"), wantKey: KeyUp, wantConsumed: 3},
		{name: "csi_down", input: []byte("\x1b[Brest"), wantKey: KeyDown, wantConsumed: 3},
		{name: "ss3_up", input: []byte("\x1bOA"), wantKey: KeyUp, wantConsumed: 3},
		{name: "ss3_down", input: []byte("\x1bOB"), wantKey: KeyDown, wantConsumed: 3},
		{name: "vim_k", input: []byte("k"), wantKey: KeyUp, wantConsumed: 1},
		{name: "vim_j", input: []byte("j"), wantKey: KeyDown, wantConsumed: 1},
		{name: "space", input: []byte(" "), wantKey: KeySpace, wantConsumed: 1},
		{name: "toggle_all_lower", input: []byte("a"), wantKey: KeyToggleAll, wantConsumed: 1},
		{name: "toggle_all_upper", input: []byte("A"), wantKey: KeyToggleAll, wantConsumed: 1},
		{name: "enter_cr", input: []byte("\r"), wantKey: KeyEnter, wantConsumed: 1},
		{name: "enter_lf", input: []byte("\n"), wantKey: KeyEnter, wantConsumed: 1},
		{name: "enter_crlf", input: []byte("\r\nx"), wantKey: KeyEnter, wantConsumed: 2},
		{name: "standalone_esc", input: []byte{0x1b}, wantKey: KeyEsc, wantConsumed: 1},
		{name: "double_esc", input: []byte{0x1b, 0x1b}, wantKey: KeyEsc, wantConsumed: 1},
		{name: "ctrl_c", input: []byte{0x03}, wantKey: KeyCtrlC, wantConsumed: 1},
		{name: "quit_q", input: []byte("q"), wantKey: KeyQuit, wantConsumed: 1},
		{name: "quit_Q", input: []byte("Q"), wantKey: KeyQuit, wantConsumed: 1},
		{name: "csi_right_ignored_not_esc", input: []byte("\x1b[Cq"), wantKey: KeyUnknown, wantConsumed: 3},
		{name: "csi_page_up_ignored_not_esc", input: []byte("\x1b[5~q"), wantKey: KeyUnknown, wantConsumed: 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotKey, gotConsumed := ParseKey(tc.input)
			if gotKey != tc.wantKey || gotConsumed != tc.wantConsumed {
				t.Fatalf("ParseKey(%q) = (%v, %d), want (%v, %d)",
					tc.input, gotKey, gotConsumed, tc.wantKey, tc.wantConsumed)
			}
		})
	}
}

func TestDiscoverPickerItemsPreselectionGlobalProjectAndUninstall(t *testing.T) {
	t.Parallel()

	t.Run("global_install_and_uninstall_defaults", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()

		// Antigravity: detected (unconfigured)
		mustMkdirAll(t, filepath.Join(home, ".gemini"))
		// DeepSeek Harness: detected (unconfigured)
		mustMkdirAll(t, filepath.Join(home, ".dsh"))
		// Claude: configured
		claudeRule := filepath.Join(home, ".claude", "rules", "git-by.md")
		mustWriteFile(t, claudeRule, []byte(RenderManagedBlock("claude-code")+"\n"), 0o644)
		// Cursor: existing unconfigured ~/.cursorrules
		mustWriteFile(t, filepath.Join(home, ".cursorrules"), []byte("# user rules\n"), 0o644)

		installItems, err := DiscoverPickerItems(Options{
			Scope:   ScopeGlobal,
			HomeDir: home,
		})
		if err != nil {
			t.Fatalf("DiscoverPickerItems global install: %v", err)
		}
		if len(installItems) != len(SupportedAgentIDs()) {
			t.Fatalf("expected all %d global agents, got %d", len(SupportedAgentIDs()), len(installItems))
		}

		gotSelected := make(map[string]bool, len(installItems))
		gotStatus := make(map[string]string, len(installItems))
		for _, it := range installItems {
			gotSelected[it.ID] = it.Selected
			gotStatus[it.ID] = it.Status
		}

		wantInstallSelected := make(map[string]bool, len(SupportedAgentIDs()))
		for _, id := range SupportedAgentIDs() {
			wantInstallSelected[id] = false
		}
		wantInstallSelected["agy"] = true
		wantInstallSelected["claude"] = true
		wantInstallSelected["dsh"] = true
		wantInstallSelected["cursor"] = true

		if !reflect.DeepEqual(gotSelected, wantInstallSelected) {
			t.Fatalf("global install pre-selection = %v, want %v", gotSelected, wantInstallSelected)
		}
		if gotStatus["agy"] != "detected" || gotStatus["dsh"] != "detected" ||
			gotStatus["claude"] != "configured" || gotStatus["codex"] != "not detected" ||
			gotStatus["gemini"] != "not detected" {
			t.Fatalf("unexpected statuses: %v", gotStatus)
		}

		// On --uninstall, ONLY configured targets (claude) should be pre-selected!
		uninstallItems, err := DiscoverPickerItems(Options{
			Scope:     ScopeGlobal,
			HomeDir:   home,
			Uninstall: true,
		})
		if err != nil {
			t.Fatalf("DiscoverPickerItems global uninstall: %v", err)
		}
		gotUninstallSelected := make(map[string]bool, len(uninstallItems))
		for _, it := range uninstallItems {
			gotUninstallSelected[it.ID] = it.Selected
		}
		wantUninstallSelected := make(map[string]bool, len(SupportedAgentIDs()))
		for _, id := range SupportedAgentIDs() {
			wantUninstallSelected[id] = false
		}
		wantUninstallSelected["claude"] = true
		if !reflect.DeepEqual(gotUninstallSelected, wantUninstallSelected) {
			t.Fatalf("global uninstall pre-selection = %v, want %v", gotUninstallSelected, wantUninstallSelected)
		}
	})

	t.Run("project_install_and_uninstall_defaults", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()

		// Existing unconfigured CLAUDE.md + existing configured .cursorrules; AGENTS.md and others absent.
		mustWriteFile(t, filepath.Join(root, "CLAUDE.md"), []byte("# Claude\n"), 0o644)
		mustWriteFile(t, filepath.Join(root, ".cursorrules"), []byte(RenderManagedBlock("cursor")+"\n"), 0o644)

		projItems, err := DiscoverPickerItems(Options{
			Scope:       ScopeProject,
			ProjectRoot: root,
		})
		if err != nil {
			t.Fatalf("DiscoverPickerItems project install: %v", err)
		}
		wantProjCount := 1 + len(projectDedicatedOrder)
		if len(projItems) != wantProjCount {
			t.Fatalf("expected %d project targets, got %d", wantProjCount, len(projItems))
		}

		gotProjSel := make(map[string]bool, wantProjCount)
		gotProjStatus := make(map[string]string, wantProjCount)
		for _, it := range projItems {
			gotProjSel[it.ID] = it.Selected
			gotProjStatus[it.ID] = it.Status
		}
		wantProjSel := make(map[string]bool, wantProjCount)
		wantProjSel["agents"] = true
		for _, id := range projectDedicatedOrder {
			wantProjSel[id] = false
		}
		wantProjSel["claude"] = true
		wantProjSel["cursor"] = true
		if !reflect.DeepEqual(gotProjSel, wantProjSel) {
			t.Fatalf("project install pre-selection = %v, want %v", gotProjSel, wantProjSel)
		}
		if gotProjStatus["agents"] != "default" || gotProjStatus["claude"] != "detected" ||
			gotProjStatus["cursor"] != "configured" || gotProjStatus["windsurf"] != "not detected" ||
			gotProjStatus["copilot"] != "not detected" {
			t.Fatalf("unexpected project statuses: %v", gotProjStatus)
		}

		projUninstall, err := DiscoverPickerItems(Options{
			Scope:       ScopeProject,
			ProjectRoot: root,
			Uninstall:   true,
		})
		if err != nil {
			t.Fatalf("DiscoverPickerItems project uninstall: %v", err)
		}
		gotUninstallSel := make(map[string]bool, wantProjCount)
		for _, it := range projUninstall {
			gotUninstallSel[it.ID] = it.Selected
		}
		wantUninstallSel := make(map[string]bool, wantProjCount)
		wantUninstallSel["agents"] = false
		for _, id := range projectDedicatedOrder {
			wantUninstallSel[id] = false
		}
		wantUninstallSel["cursor"] = true
		if !reflect.DeepEqual(gotUninstallSel, wantUninstallSel) {
			t.Fatalf("project uninstall pre-selection = %v, want %v", gotUninstallSel, wantUninstallSel)
		}
	})
}

func TestRunPickerNavigationTogglesCancelAndRestore(t *testing.T) {
	t.Parallel()

	sampleItems := func() []PickerItem {
		return []PickerItem{
			{ID: "agy", Label: "Antigravity (agy)", DisplayPath: "~/.gemini/config/rules/git-by.md", Status: "detected", Selected: true},
			{ID: "pi", Label: "Pi Agent (pi)", DisplayPath: "~/.pi/agent/APPEND_SYSTEM.md", Status: "not detected", Selected: false},
			{ID: "codex", Label: "Codex (codex)", DisplayPath: "~/.codex/AGENTS.md", Status: "not detected", Selected: false},
		}
	}

	t.Run("chunked_escape_arrows_space_wraparound_and_enter", func(t *testing.T) {
		t.Parallel()
		// Start at index 0 (agy=[x], pi=[ ], codex=[ ]).
		// Space -> uncheck agy ([ ]).
		// Up arrow (\x1b[A) -> wraps around to index 2 (codex)!
		// Space -> check codex ([x]).
		// Down arrow (\x1b[B) -> wraps around to index 0 (agy).
		// 'j' -> moves to index 1 (pi).
		// Space -> check pi ([x]).
		// 'k' -> moves back to index 0.
		// Enter (\r) -> confirms ["pi", "codex"].
		seq := []byte(" \x1b[A \x1b[Bj k\r")
		var out bytes.Buffer
		restored := 0

		selected, err := RunPicker(PickerConfig{
			In:    &oneByteReader{data: seq},
			Out:   &out,
			Scope: ScopeGlobal,
			Color: false,
			RawMode: func() (func(), error) {
				return func() { restored++ }, nil
			},
		}, sampleItems())
		if err != nil {
			t.Fatalf("RunPicker unexpected error: %v", err)
		}
		if restored != 1 {
			t.Fatalf("expected restore called once, got %d", restored)
		}
		want := []string{"pi", "codex"}
		if !reflect.DeepEqual(selected, want) {
			t.Fatalf("selected = %v, want %v", selected, want)
		}
		rendered := out.String()
		if !strings.Contains(rendered, "↑/↓ move · Space select · a toggle all · Enter install · q/Esc cancel") {
			t.Fatalf("missing keybinding hint in output:\n%s", rendered)
		}
		if !strings.HasSuffix(rendered, "\x1b[?25h") {
			t.Fatalf("expected cursor visibility restored at end of output")
		}
	})

	t.Run("toggle_all_and_zero_selected_enter", func(t *testing.T) {
		t.Parallel()
		// First 'a' selects all 3 (since only 1/3 was selected); second 'a' deselects all 3; '\n' confirms empty slice.
		var out bytes.Buffer
		selected, err := RunPicker(PickerConfig{
			In:    strings.NewReader("aa\n"),
			Out:   &out,
			Scope: ScopeGlobal,
			Color: true,
		}, sampleItems())
		if err != nil {
			t.Fatalf("RunPicker error: %v", err)
		}
		if len(selected) != 0 {
			t.Fatalf("expected 0 selected items, got %v", selected)
		}
		if !strings.Contains(out.String(), "3 of 3 selected") || !strings.Contains(out.String(), "0 of 3 selected") {
			t.Fatalf("expected toggle-all counters in rendered output:\n%s", out.String())
		}
	})

	t.Run("standalone_esc_q_and_ctrl_c_cancel_and_restore", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name    string
			input   string
			wantErr error
		}{
			{name: "esc", input: "\x1b", wantErr: ErrCancelled},
			{name: "quit_q", input: "q", wantErr: ErrCancelled},
			{name: "ctrl_c", input: "\x03", wantErr: ErrInterrupted},
			{name: "eof", input: "", wantErr: ErrCancelled},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var out bytes.Buffer
				restored := 0
				selected, err := RunPicker(PickerConfig{
					In:    strings.NewReader(tc.input),
					Out:   &out,
					Scope: ScopeGlobal,
					RawMode: func() (func(), error) {
						return func() { restored++ }, nil
					},
				}, sampleItems())
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if selected != nil {
					t.Fatalf("expected nil selection on cancel, got %v", selected)
				}
				if restored != 1 {
					t.Fatalf("expected restore called once on %s, got %d", tc.name, restored)
				}
				if !strings.Contains(out.String(), "\x1b[?25h") {
					t.Fatalf("expected cursor visibility restored on %s", tc.name)
				}
			})
		}
	})
}

func TestInteractiveSelectedTargetsCreatesMissingDirsOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Note: NONE of ~/.gemini, ~/.codex, ~/.config/opencode exist initially!

	// 1. Interactive selection with CreateMissingDirs=true + DryRun=true previews install without creating dirs.
	drySum, err := Run(Options{
		Scope:             ScopeGlobal,
		HomeDir:           home,
		SelectedTargets:   []string{"agy", "codex", "opencode"},
		CreateMissingDirs: true,
		DryRun:            true,
	})
	if err != nil {
		t.Fatalf("DryRun interactive install failed: %v", err)
	}
	if drySum.ActiveCount != 3 {
		t.Fatalf("DryRun ActiveCount = %d, want 3", drySum.ActiveCount)
	}
	assertNotExists(t, filepath.Join(home, ".gemini"))
	assertNotExists(t, filepath.Join(home, ".codex"))
	assertNotExists(t, filepath.Join(home, ".config"))

	// 2. Real interactive install creates rule files for explicitly checked agents (including dsh)!
	sum, err := Run(Options{
		Scope:             ScopeGlobal,
		HomeDir:           home,
		SelectedTargets:   []string{"agy", "codex", "opencode", "dsh"},
		CreateMissingDirs: true,
	})
	if err != nil {
		t.Fatalf("Interactive install failed: %v", err)
	}
	if sum.ActiveCount != 4 {
		t.Fatalf("ActiveCount = %d, want 4", sum.ActiveCount)
	}
	assertFileExists(t, filepath.Join(home, ".gemini", "config", "rules", "git-by.md"))
	assertFileExists(t, filepath.Join(home, ".codex", "AGENTS.md"))
	assertFileExists(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"))
	assertFileExists(t, filepath.Join(home, ".dsh", "AGENTS.md"))
	if !strings.Contains(string(mustReadFile(t, filepath.Join(home, ".dsh", "AGENTS.md"))), "--harness dsh") {
		t.Fatalf("expected --harness dsh in ~/.dsh/AGENTS.md")
	}
	assertNotExists(t, filepath.Join(home, ".pi"))
	assertNotExists(t, filepath.Join(home, ".claude"))

	// 3. Uninstall with CreateMissingDirs=true still safely skips unconfigured/missing agents without creating dirs!
	unSum, err := Run(Options{
		Scope:             ScopeGlobal,
		HomeDir:           home,
		SelectedTargets:   []string{"agy", "codex", "dsh", "pi"},
		CreateMissingDirs: true,
		Uninstall:         true,
	})
	if err != nil {
		t.Fatalf("Interactive uninstall failed: %v", err)
	}
	if unSum.ActiveCount != 3 {
		t.Fatalf("Uninstall ActiveCount = %d, want 3 (agy + codex + dsh removed, pi skipped)", unSum.ActiveCount)
	}
	assertNotExists(t, filepath.Join(home, ".pi"))
	assertNotExists(t, filepath.Join(home, ".gemini", "config", "rules", "git-by.md"))
	assertNotExists(t, filepath.Join(home, ".codex", "AGENTS.md"))
	assertNotExists(t, filepath.Join(home, ".dsh", "AGENTS.md"))
}

func TestIsTerminalNonTerminalValues(t *testing.T) {
	t.Parallel()

	if IsTerminal(nil) {
		t.Fatal("IsTerminal(nil) = true, want false")
	}
	if IsTerminal(strings.NewReader("abc")) {
		t.Fatal("IsTerminal(strings.Reader) = true, want false")
	}
	var nilFile *os.File
	if IsTerminal(nilFile) {
		t.Fatal("IsTerminal((*os.File)(nil)) = true, want false")
	}

	tmpFile, err := os.CreateTemp(t.TempDir(), "not-a-tty-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer tmpFile.Close()
	if IsTerminal(tmpFile) {
		t.Fatal("IsTerminal(regular file) = true, want false")
	}
}

func TestPickerViewportWindowing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	items, err := DiscoverPickerItems(Options{Scope: ScopeGlobal, HomeDir: home})
	if err != nil {
		t.Fatalf("DiscoverPickerItems: %v", err)
	}

	// Render with maxVisible=6 at top (cursor=0), middle (cursor=14), and bottom (cursor=len-1).
	topLines := RenderPickerLinesWindowed(ScopeGlobal, false, false, false, items, 0, 6)
	midLines := RenderPickerLinesWindowed(ScopeGlobal, false, false, false, items, 14, 6)
	botLines := RenderPickerLinesWindowed(ScopeGlobal, false, false, false, items, len(items)-1, 6)

	// Constant line height (6 visible items + 5 chrome lines = 11) prevents cursor-up drift!
	if len(topLines) != 11 || len(midLines) != 11 || len(botLines) != 11 {
		t.Fatalf("expected constant 11 lines across frames, got top=%d mid=%d bot=%d",
			len(topLines), len(midLines), len(botLines))
	}
	if !strings.Contains(topLines[len(topLines)-1], "(showing 1-6 of 30)") {
		t.Fatalf("unexpected top footer: %q", topLines[len(topLines)-1])
	}
	if !strings.Contains(botLines[len(botLines)-1], "(showing 25-30 of 30)") {
		t.Fatalf("unexpected bottom footer: %q", botLines[len(botLines)-1])
	}

	// Exercise RunPicker with MaxVisible=5: press Up (wraps to last item: hermes), Space, Enter!
	var out bytes.Buffer
	selected, err := RunPicker(PickerConfig{
		In:         strings.NewReader("\x1b[A \r"),
		Out:        &out,
		Scope:      ScopeGlobal,
		MaxVisible: 5,
	}, items)
	if err != nil {
		t.Fatalf("RunPicker windowed failed: %v", err)
	}
	if !reflect.DeepEqual(selected, []string{"hermes"}) {
		t.Fatalf("selected = %v, want [hermes]", selected)
	}
}

var (
	ansiSGRRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	ansiCSIRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
)

func assertValidBalancedSGR(t *testing.T, line string) {
	t.Helper()
	active := false
	for i := 0; i < len(line); i++ {
		if line[i] != 0x1b {
			continue
		}
		loc := ansiSGRRe.FindStringIndex(line[i:])
		if loc == nil || loc[0] != 0 {
			t.Fatalf("truncated or invalid ANSI escape sequence at byte %d in %q", i, line)
		}
		seq := line[i : i+loc[1]]
		if seq == ansiReset {
			if !active {
				t.Fatalf("stray ANSI reset %q without active style in %q", seq, line)
			}
			active = false
		} else {
			active = true
		}
		i += loc[1] - 1
	}
	if active {
		t.Fatalf("unclosed ANSI SGR sequence in %q", line)
	}
}

func TestRenderPickerLinesConstrained_NoOverflowAcrossWidths(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, ".gemini"))
	mustWriteFile(t, filepath.Join(home, ".claude", "rules", "git-by.md"), []byte(RenderManagedBlock("claude-code")+"\n"), 0o644)

	globalItems, err := DiscoverPickerItems(Options{Scope: ScopeGlobal, HomeDir: home})
	if err != nil {
		t.Fatalf("DiscoverPickerItems: %v", err)
	}
	if len(globalItems) != 30 {
		t.Fatalf("expected 30 global items, got %d", len(globalItems))
	}

	cjkAndCombiningItems := []PickerItem{
		{
			ID:          "zh-agent",
			Label:       "中文编程助手 (zh-agent)",
			DisplayPath: "~/项目工作区/深层配置目录/规则/git-by.md",
			Status:      "configured",
			Selected:    true,
		},
		{
			ID:          "combining",
			Label:       "Cafe\u0301 Agent (cafe\u0301)",
			DisplayPath: "~/work/re\u0301pertoire/sous-re\u0301pertoire/git-by.md",
			Status:      "detected",
			Selected:    false,
		},
		{
			ID:          "jp-agent",
			Label:       "日本語エージェント (jp)",
			DisplayPath: "~/設定/ディレクトリ/全角ファイル名テスト/AGENTS.md",
			Status:      "not detected",
			Selected:    true,
		},
	}

	datasets := []struct {
		name       string
		items      []PickerItem
		maxVisible int
		uninstall  bool
		dryRun     bool
	}{
		{name: "global_30_windowed", items: globalItems, maxVisible: 8, uninstall: false, dryRun: false},
		{name: "global_30_flags", items: globalItems, maxVisible: 6, uninstall: true, dryRun: true},
		{name: "cjk_and_combining", items: cjkAndCombiningItems, maxVisible: 0, uninstall: false, dryRun: false},
	}

	widths := []int{1, 2, 5, 10, 20, 30, 40, 55, 70, 80, 120}

	for _, ds := range datasets {
		for _, color := range []bool{false, true} {
			for _, maxW := range widths {
				t.Run(fmt.Sprintf("%s/color=%v/w=%d", ds.name, color, maxW), func(t *testing.T) {
					lines := RenderPickerLinesConstrained(
						ScopeGlobal,
						ds.uninstall,
						ds.dryRun,
						color,
						ds.items,
						1,
						ds.maxVisible,
						maxW,
					)

					wantVisible := len(ds.items)
					if ds.maxVisible > 0 && ds.maxVisible < wantVisible {
						wantVisible = ds.maxVisible
					}
					wantLen := wantVisible + 5
					if len(lines) != wantLen {
						t.Fatalf("line count = %d, want %d", len(lines), wantLen)
					}

					budget := max(0, maxW-1)
					for idx, line := range lines {
						if color {
							assertValidBalancedSGR(t, line)
						} else if strings.ContainsRune(line, 0x1b) {
							t.Fatalf("unexpected ANSI escape in color=false line %d: %q", idx, line)
						}

						plain := ansiSGRRe.ReplaceAllString(line, "")
						gotW := stringDisplayWidth(plain)
						if gotW > budget {
							t.Fatalf("line %d overflowed budget %d (got %d): %q", idx, budget, gotW, plain)
						}
					}
				})
			}
		}
	}

	// Verify combining marks are never orphaned immediately after "…" when head-truncating paths.
	truncComb := truncatePathDisplayHead("x/a\u0301bc/d", 5)
	if stringDisplayWidth(truncComb) > 5 {
		t.Fatalf("truncatePathDisplayHead overflowed: %q (width %d)", truncComb, stringDisplayWidth(truncComb))
	}
	if strings.HasPrefix(truncComb, "…\u0301") {
		t.Fatalf("truncatePathDisplayHead left orphaned combining mark after ellipsis: %q", truncComb)
	}
	truncComb2 := truncatePathDisplayHead("xa\u0301bc", 3)
	if truncComb2 != "…bc" {
		t.Fatalf("truncatePathDisplayHead(%q, 3) = %q, want %q", "xa\u0301bc", truncComb2, "…bc")
	}
}

func TestRenderPickerLinesConstrained_TierTransitions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, ".gemini"))
	mustWriteFile(t, filepath.Join(home, ".claude", "rules", "git-by.md"), []byte(RenderManagedBlock("claude-code")+"\n"), 0o644)

	items, err := DiscoverPickerItems(Options{Scope: ScopeGlobal, HomeDir: home})
	if err != nil {
		t.Fatalf("DiscoverPickerItems: %v", err)
	}

	// 1. maxWidth=0 must be byte-for-byte identical to RenderPickerLinesWindowed (both plain and colored).
	for _, color := range []bool{false, true} {
		want := RenderPickerLinesWindowed(ScopeGlobal, false, false, color, items, 0, 6)
		got := RenderPickerLinesConstrained(ScopeGlobal, false, false, color, items, 0, 6, 0)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("color=%v: RenderPickerLinesConstrained(..., 0) diverged from RenderPickerLinesWindowed", color)
		}
	}

	// Wide (maxWidth=120): Tier 1 - full Label, DisplayPath, Status.
	wide := RenderPickerLinesConstrained(ScopeGlobal, false, false, false, items, 0, 6, 120)
	wideRow0 := wide[3]
	if !strings.Contains(wideRow0, "Antigravity (agy)") ||
		!strings.Contains(wideRow0, "~/.gemini/config/rules/git-by.md") ||
		!strings.Contains(wideRow0, "detected") {
		t.Fatalf("wide tier row0 missing full columns: %q", wideRow0)
	}

	// Medium split (maxWidth=72): Tier 2 - full Label & Status, head-truncated DisplayPath with "…/".
	medium := RenderPickerLinesConstrained(ScopeGlobal, false, false, false, items, 0, 6, 72)
	medRow0 := medium[3]
	if !strings.Contains(medRow0, "Antigravity (agy)") || !strings.Contains(medRow0, "detected") {
		t.Fatalf("medium tier row0 should preserve full Label and Status: %q", medRow0)
	}
	if !strings.Contains(medRow0, "…/") {
		t.Fatalf("medium tier row0 should head-truncate path with \"…/\": %q", medRow0)
	}
	if strings.Contains(medRow0, "~/.gemini/config/rules/git-by.md") {
		t.Fatalf("medium tier row0 should compress long path: %q", medRow0)
	}

	// Narrow split (maxWidth=48): Tier 3 - DisplayPath hidden, Label & Status fully visible.
	narrow := RenderPickerLinesConstrained(ScopeGlobal, false, false, false, items, 0, 6, 48)
	narrowRow0 := narrow[3]
	if !strings.Contains(narrowRow0, "Antigravity (agy)") || !strings.Contains(narrowRow0, "detected") {
		t.Fatalf("narrow tier row0 should keep full Label and Status: %q", narrowRow0)
	}
	if strings.Contains(narrowRow0, "git-by.md") || strings.Contains(narrowRow0, "…/") {
		t.Fatalf("narrow tier row0 should hide DisplayPath: %q", narrowRow0)
	}

	// Ultra-narrow split (maxWidth=22): Tier 4 - keep "> [x]" and truncated Label only.
	ultra := RenderPickerLinesConstrained(ScopeGlobal, false, false, false, items, 0, 6, 22)
	ultraRow0 := ultra[3]
	if !strings.HasPrefix(ultraRow0, "  > [x] ") {
		t.Fatalf("ultra-narrow row0 should keep \"  > [x] \" prefix: %q", ultraRow0)
	}
	if !strings.Contains(ultraRow0, "Antigravity") || !strings.HasSuffix(ultraRow0, "…") {
		t.Fatalf("ultra-narrow row0 should show truncated Label: %q", ultraRow0)
	}
	if strings.Contains(ultraRow0, "detected") || strings.Contains(ultraRow0, "git-by.md") {
		t.Fatalf("ultra-narrow row0 should hide path and status: %q", ultraRow0)
	}
}

func TestRunPicker_MaxWidthAndNonTerminalTermWidth(t *testing.T) {
	t.Parallel()

	// Verify termWidth returns 0 for non-terminals (*bytes.Buffer, nil, regular file).
	if w := termWidth(&bytes.Buffer{}); w != 0 {
		t.Fatalf("termWidth(&bytes.Buffer{}) = %d, want 0", w)
	}
	if w := termWidth(nil); w != 0 {
		t.Fatalf("termWidth(nil) = %d, want 0", w)
	}
	var nilFile *os.File
	if w := termWidth(nilFile); w != 0 {
		t.Fatalf("termWidth((*os.File)(nil)) = %d, want 0", w)
	}
	tmpFile, err := os.CreateTemp(t.TempDir(), "term-width-regular-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer tmpFile.Close()
	if w := termWidth(tmpFile); w != 0 {
		t.Fatalf("termWidth(regular file) = %d, want 0", w)
	}

	// Interactive RunPicker with MaxWidth: 50, MaxVisible: 5, Color: true.
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, ".gemini"))
	items, err := DiscoverPickerItems(Options{Scope: ScopeGlobal, HomeDir: home})
	if err != nil {
		t.Fatalf("DiscoverPickerItems: %v", err)
	}
	// Initial state: only index 0 ("agy") is selected.
	// Down -> index 1 ("pi"), Space -> select "pi", Down -> index 2 ("claude"), Space -> select "claude",
	// Up -> index 1 ("pi"), Up -> index 0 ("agy"), Space -> unselect "agy", Enter -> ["pi", "claude"].
	input := "\x1b[B \x1b[B \x1b[A\x1b[A \r"
	var out bytes.Buffer
	selected, err := RunPicker(PickerConfig{
		In:         strings.NewReader(input),
		Out:        &out,
		Scope:      ScopeGlobal,
		Color:      true,
		MaxVisible: 5,
		MaxWidth:   50,
	}, items)
	if err != nil {
		t.Fatalf("RunPicker error: %v", err)
	}
	want := []string{"pi", "claude"}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected = %v, want %v", selected, want)
	}

	rawLines := strings.Split(out.String(), "\n")
	for idx, rawLine := range rawLines {
		plain := strings.ReplaceAll(ansiCSIRe.ReplaceAllString(rawLine, ""), "\r", "")
		if gotW := stringDisplayWidth(plain); gotW > 49 {
			t.Fatalf("rendered frame line %d exceeded 49 cols (got %d): %q", idx, gotW, plain)
		}
	}
}
