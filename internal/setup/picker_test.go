package setup

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
		if len(installItems) != 8 {
			t.Fatalf("expected all 8 global agents, got %d", len(installItems))
		}

		gotSelected := make(map[string]bool, len(installItems))
		gotStatus := make(map[string]string, len(installItems))
		for _, it := range installItems {
			gotSelected[it.ID] = it.Selected
			gotStatus[it.ID] = it.Status
		}

		wantInstallSelected := map[string]bool{
			"agy":      true,
			"pi":       false,
			"claude":   true,
			"codex":    false,
			"opencode": false,
			"dsh":      true,
			"cursor":   true,
			"windsurf": false,
		}
		if !reflect.DeepEqual(gotSelected, wantInstallSelected) {
			t.Fatalf("global install pre-selection = %v, want %v", gotSelected, wantInstallSelected)
		}
		if gotStatus["agy"] != "detected" || gotStatus["dsh"] != "detected" || gotStatus["claude"] != "configured" || gotStatus["codex"] != "not detected" {
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
		wantUninstallSelected := map[string]bool{
			"agy":      false,
			"pi":       false,
			"claude":   true,
			"codex":    false,
			"opencode": false,
			"dsh":      false,
			"cursor":   false,
			"windsurf": false,
		}
		if !reflect.DeepEqual(gotUninstallSelected, wantUninstallSelected) {
			t.Fatalf("global uninstall pre-selection = %v, want %v", gotUninstallSelected, wantUninstallSelected)
		}
	})

	t.Run("project_install_and_uninstall_defaults", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()

		// Existing unconfigured CLAUDE.md + existing configured .cursorrules; AGENTS.md and .windsurfrules absent.
		mustWriteFile(t, filepath.Join(root, "CLAUDE.md"), []byte("# Claude\n"), 0o644)
		mustWriteFile(t, filepath.Join(root, ".cursorrules"), []byte(RenderManagedBlock("cursor")+"\n"), 0o644)

		projItems, err := DiscoverPickerItems(Options{
			Scope:       ScopeProject,
			ProjectRoot: root,
		})
		if err != nil {
			t.Fatalf("DiscoverPickerItems project install: %v", err)
		}
		if len(projItems) != 4 {
			t.Fatalf("expected 4 project targets, got %d", len(projItems))
		}

		gotProjSel := make(map[string]bool, 4)
		gotProjStatus := make(map[string]string, 4)
		for _, it := range projItems {
			gotProjSel[it.ID] = it.Selected
			gotProjStatus[it.ID] = it.Status
		}
		wantProjSel := map[string]bool{
			"agents":   true,
			"claude":   true,
			"cursor":   true,
			"windsurf": false,
		}
		if !reflect.DeepEqual(gotProjSel, wantProjSel) {
			t.Fatalf("project install pre-selection = %v, want %v", gotProjSel, wantProjSel)
		}
		if gotProjStatus["agents"] != "default" || gotProjStatus["claude"] != "detected" ||
			gotProjStatus["cursor"] != "configured" || gotProjStatus["windsurf"] != "not detected" {
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
		gotUninstallSel := make(map[string]bool, 4)
		for _, it := range projUninstall {
			gotUninstallSel[it.ID] = it.Selected
		}
		wantUninstallSel := map[string]bool{
			"agents":   false,
			"claude":   false,
			"cursor":   true,
			"windsurf": false,
		}
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
