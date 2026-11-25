package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRenderManagedBlock(t *testing.T) {
	t.Parallel()

	generic := RenderManagedBlock("")
	if !strings.HasPrefix(generic, MarkerStart+"\n") {
		t.Fatalf("expected prefix %q, got %q", MarkerStart, generic[:min(40, len(generic))])
	}
	if !strings.HasSuffix(generic, "\n"+MarkerEnd) {
		t.Fatalf("expected suffix %q without trailing newline, got %q", MarkerEnd, generic[max(0, len(generic)-40):])
	}
	if strings.HasSuffix(generic, "\n") {
		t.Fatalf("RenderManagedBlock must not end with trailing newline")
	}
	if !strings.Contains(generic, "--harness <your-harness-name>") {
		t.Fatalf("expected generic harness placeholder, got:\n%s", generic)
	}
	if count := strings.Count(generic, "<your-harness-name>"); count != 6 {
		t.Fatalf("expected 6 harness occurrences, got %d", count)
	}

	piBlock := RenderManagedBlock("  pi  ")
	if strings.Contains(piBlock, "<your-harness-name>") || strings.Contains(piBlock, "<harness-name>") {
		t.Fatalf("unexpected placeholder in pi block:\n%s", piBlock)
	}
	if count := strings.Count(piBlock, "--harness pi"); count != 6 {
		t.Fatalf("expected 6 '--harness pi' occurrences, got %d", count)
	}
}

func TestParseAgents(t *testing.T) {
	t.Parallel()

	got, err := ParseAgents(nil)
	if err != nil || got != nil {
		t.Fatalf("ParseAgents(nil) = (%v, %v), want (nil, nil)", got, err)
	}

	got, err = ParseAgents([]string{
		" Antigravity , pi-agent ",
		"CLAUDE-CODE,openai-codex,opencode",
		"agy,Cursor,WINDSURF,claude",
	})
	if err != nil {
		t.Fatalf("ParseAgents unexpected error: %v", err)
	}
	want := []string{"agy", "pi", "claude", "codex", "opencode", "cursor", "windsurf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAgents = %v, want %v", got, want)
	}

	invalidInputs := [][]string{
		{""},
		{"   "},
		{"agy,"},
		{",pi"},
		{"agy,,pi"},
		{"unknown-agent"},
		{"agy,copilot"},
	}
	for _, raw := range invalidInputs {
		if _, err := ParseAgents(raw); err == nil {
			t.Errorf("ParseAgents(%q) expected error, got nil", raw)
		}
	}
}

func TestGlobalAutoDetectionSafetyAndRoundtrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	// Create .gemini, .pi (with existing user rules), and .claude; leave .codex and .config/opencode absent.
	mustMkdirAll(t, filepath.Join(home, ".gemini"))
	mustMkdirAll(t, filepath.Join(home, ".pi", "agent"))
	mustMkdirAll(t, filepath.Join(home, ".claude"))

	piPath := filepath.Join(home, ".pi", "agent", "APPEND_SYSTEM.md")
	origPiContent := []byte("# Personal Pi System Prompt\n\n- Prefer small, focused functions.\n- Always run unit tests before finishing.\n")
	mustWriteFile(t, piPath, origPiContent, 0o600)

	// 1. Dry-run first: zero filesystem mutations.
	drySummary, err := Run(Options{
		Scope:   ScopeGlobal,
		HomeDir: home,
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("DryRun failed: %v", err)
	}
	if drySummary.ActiveCount != 3 {
		t.Fatalf("DryRun ActiveCount = %d, want 3", drySummary.ActiveCount)
	}
	if len(drySummary.Results) != 5 {
		t.Fatalf("DryRun len(Results) = %d, want 5", len(drySummary.Results))
	}
	assertNotExists(t, filepath.Join(home, ".gemini", "config"))
	assertNotExists(t, filepath.Join(home, ".claude", "rules"))
	assertNotExists(t, filepath.Join(home, ".codex"))
	assertNotExists(t, filepath.Join(home, ".config"))
	if gotPi := mustReadFile(t, piPath); !bytes.Equal(gotPi, origPiContent) {
		t.Fatalf("DryRun mutated %s", piPath)
	}

	// 2. Real global install.
	sum1, err := Run(Options{
		Scope:   ScopeGlobal,
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("Run global install failed: %v", err)
	}
	if sum1.ActiveCount != 3 {
		t.Fatalf("ActiveCount = %d, want 3", sum1.ActiveCount)
	}

	var rendered bytes.Buffer
	if err := RenderSummary(&rendered, sum1, false); err != nil {
		t.Fatalf("RenderSummary error: %v", err)
	}
	wantSummary := strings.Join([]string{
		"git-by setup (global)",
		"",
		"  ✔ Antigravity   installed   ~/.gemini/config/rules/git-by.md",
		"  ✔ Pi Agent      updated     ~/.pi/agent/APPEND_SYSTEM.md",
		"  ✔ Claude Code   installed   ~/.claude/rules/git-by.md",
		"  - Codex         skipped     (~/.codex not found)",
		"  - OpenCode      skipped     (~/.config/opencode not found)",
		"",
		"Configured 3 agent(s). Your AI commits will now carry provenance automatically.",
		"",
	}, "\n")
	if rendered.String() != wantSummary {
		t.Fatalf("RenderSummary mismatch:\ngot:\n%s\nwant:\n%s", rendered.String(), wantSummary)
	}

	// Verify colored output renders without error and includes ANSI sequences.
	var colored bytes.Buffer
	if err := RenderSummary(&colored, sum1, true); err != nil {
		t.Fatalf("RenderSummary(color=true) error: %v", err)
	}
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("expected ANSI codes when color=true")
	}

	// Uninstalled tools MUST NOT have top-level dirs created.
	assertNotExists(t, filepath.Join(home, ".codex"))
	assertNotExists(t, filepath.Join(home, ".config"))
	assertNotExists(t, filepath.Join(home, ".cursor"))
	assertNotExists(t, filepath.Join(home, ".cursorrules"))
	assertNotExists(t, filepath.Join(home, ".windsurfrules"))

	agyPath := filepath.Join(home, ".gemini", "config", "rules", "git-by.md")
	claudePath := filepath.Join(home, ".claude", "rules", "git-by.md")

	if got := string(mustReadFile(t, agyPath)); got != RenderManagedBlock("antigravity")+"\n" {
		t.Fatalf("unexpected Antigravity rule content:\n%s", got)
	}
	if got := string(mustReadFile(t, claudePath)); got != RenderManagedBlock("claude-code")+"\n" {
		t.Fatalf("unexpected Claude rule content:\n%s", got)
	}
	piUpdated := mustReadFile(t, piPath)
	if count := bytes.Count(piUpdated, []byte(MarkerStart)); count != 1 {
		t.Fatalf("expected 1 MarkerStart in APPEND_SYSTEM.md, got %d", count)
	}
	if !bytes.Contains(piUpdated, []byte("--harness pi")) {
		t.Fatalf("expected '--harness pi' in APPEND_SYSTEM.md")
	}
	// Verify file permissions preserved on APPEND_SYSTEM.md (0600).
	if info, err := os.Stat(piPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600 preserved on %s, got %v (err=%v)", piPath, info.Mode().Perm(), err)
	}

	// 3. Stale block update + second-run idempotency.
	stalePi := strings.Replace(string(piUpdated), "git by commit", "OLD_STALE_INSTRUCTION", 1)
	mustWriteFile(t, piPath, []byte(stalePi), 0o600)

	sum2, err := Run(Options{
		Scope:   ScopeGlobal,
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if sum2.ActiveCount != 3 {
		t.Fatalf("second Run ActiveCount = %d, want 3", sum2.ActiveCount)
	}
	piAfterSecond := mustReadFile(t, piPath)
	if !bytes.Equal(piAfterSecond, piUpdated) {
		t.Fatalf("stale block was not cleanly updated:\ngot:\n%s\nwant:\n%s", piAfterSecond, piUpdated)
	}
	if count := bytes.Count(piAfterSecond, []byte(MarkerStart)); count != 1 {
		t.Fatalf("duplicate block after second run: count=%d", count)
	}

	// Third run with identical content remains 100% idempotent.
	if _, err := Run(Options{Scope: ScopeGlobal, HomeDir: home}); err != nil {
		t.Fatalf("third Run failed: %v", err)
	}
	if got := mustReadFile(t, piPath); !bytes.Equal(got, piUpdated) {
		t.Fatalf("third Run changed content unexpectedly")
	}

	// 4. Uninstall dry-run: zero mutations.
	dryUninstall, err := Run(Options{
		Scope:     ScopeGlobal,
		HomeDir:   home,
		Uninstall: true,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("DryRun uninstall failed: %v", err)
	}
	if dryUninstall.ActiveCount != 3 {
		t.Fatalf("DryRun uninstall ActiveCount = %d, want 3", dryUninstall.ActiveCount)
	}
	assertFileExists(t, agyPath)
	assertFileExists(t, claudePath)

	// 5. Real uninstall: standalone files deleted, shared file restored byte-for-byte.
	unSum, err := Run(Options{
		Scope:     ScopeGlobal,
		HomeDir:   home,
		Uninstall: true,
	})
	if err != nil {
		t.Fatalf("Uninstall failed: %v", err)
	}
	if unSum.ActiveCount != 3 {
		t.Fatalf("Uninstall ActiveCount = %d, want 3", unSum.ActiveCount)
	}
	assertNotExists(t, agyPath)
	assertNotExists(t, claudePath)
	assertFileExists(t, piPath)

	restoredPi := mustReadFile(t, piPath)
	if !bytes.Equal(restoredPi, origPiContent) {
		t.Fatalf("APPEND_SYSTEM.md not restored byte-for-byte!\ngot:  %q\nwant: %q", restoredPi, origPiContent)
	}

	// Re-running uninstall when already clean skips all targets.
	unSum2, err := Run(Options{
		Scope:     ScopeGlobal,
		HomeDir:   home,
		Uninstall: true,
	})
	if err != nil {
		t.Fatalf("second Uninstall failed: %v", err)
	}
	if unSum2.ActiveCount != 0 {
		t.Fatalf("second Uninstall ActiveCount = %d, want 0", unSum2.ActiveCount)
	}
}

func TestByteForByteLosslessRoundtripAllPositions(t *testing.T) {
	t.Parallel()

	t.Run("user_content_before_single_and_multi_newline_and_crlf", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			orig []byte
		}{
			{
				name: "standard_posix",
				orig: []byte("# Team Engineering Rules\n\n1. Write table-driven tests.\n2. Keep commits atomic.\n"),
			},
			{
				name: "trailing_blank_line",
				orig: []byte("# Header\n\nIndented code:\n    go test ./...\n\n"),
			},
			{
				name: "crlf_windows_style",
				orig: []byte("# Windows Rules\r\n\r\n- Keep CRLF intact.\r\n"),
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				root := t.TempDir()
				agentsMD := filepath.Join(root, "AGENTS.md")
				mustWriteFile(t, agentsMD, tc.orig, 0o644)

				if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root}); err != nil {
					t.Fatalf("install failed: %v", err)
				}
				installed := mustReadFile(t, agentsMD)
				if !bytes.Contains(installed, []byte(MarkerStart)) || !bytes.Contains(installed, []byte(MarkerEnd)) {
					t.Fatalf("markers missing after install")
				}

				if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root, Uninstall: true}); err != nil {
					t.Fatalf("uninstall failed: %v", err)
				}
				restored := mustReadFile(t, agentsMD)
				if !bytes.Equal(restored, tc.orig) {
					t.Fatalf("roundtrip mismatch:\ngot:  %q\nwant: %q", restored, tc.orig)
				}
			})
		}
	})

	t.Run("user_content_after_block", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		agentsMD := filepath.Join(root, "AGENTS.md")

		// Install into empty project first (block at top), then user appends content below block.
		if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root}); err != nil {
			t.Fatalf("initial install failed: %v", err)
		}
		userAfter := []byte("## Custom Project Guidelines\n\n- Never break public APIs.\n")
		combined := append(mustReadFile(t, agentsMD), append([]byte("\n"), userAfter...)...)
		mustWriteFile(t, agentsMD, combined, 0o644)

		// Upsert updates in-place preserving trailing user content.
		if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root, Agents: []string{"codex"}}); err != nil {
			t.Fatalf("upsert failed: %v", err)
		}
		updated := mustReadFile(t, agentsMD)
		if !bytes.Contains(updated, []byte("--harness codex")) {
			t.Fatalf("expected codex harness after upsert")
		}
		if !bytes.HasSuffix(updated, userAfter) {
			t.Fatalf("user content after block was not preserved")
		}

		// Uninstall strips top block + blank separator, leaving exact userAfter bytes.
		if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root, Uninstall: true}); err != nil {
			t.Fatalf("uninstall failed: %v", err)
		}
		restored := mustReadFile(t, agentsMD)
		if !bytes.Equal(restored, userAfter) {
			t.Fatalf("after-block roundtrip mismatch:\ngot:  %q\nwant: %q", restored, userAfter)
		}
	})

	t.Run("user_content_before_and_after_block", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		agentsMD := filepath.Join(root, "AGENTS.md")

		userBefore := "# Architecture Overview\n\nAll packages live under `internal/`.\n"
		userAfter := "## Testing Policy\n\nRun `go test -race ./...` before committing.\n"
		origSurrounding := []byte(userBefore + "\n" + userAfter)

		withMiddleBlock := []byte(userBefore + "\n" + RenderManagedBlock("old-harness") + "\n\n" + userAfter)
		mustWriteFile(t, agentsMD, withMiddleBlock, 0o644)

		// Upsert replaces only managed block in the middle, preserving 100% of bytes before and after.
		if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root}); err != nil {
			t.Fatalf("upsert middle block failed: %v", err)
		}
		afterUpsert := string(mustReadFile(t, agentsMD))
		wantMiddle := userBefore + "\n" + RenderManagedBlock("") + "\n\n" + userAfter
		if afterUpsert != wantMiddle {
			t.Fatalf("middle upsert mismatch:\ngot:\n%s\nwant:\n%s", afterUpsert, wantMiddle)
		}

		// Uninstall restores surrounding document with a single blank line between before and after.
		if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root, Uninstall: true}); err != nil {
			t.Fatalf("uninstall middle block failed: %v", err)
		}
		restored := mustReadFile(t, agentsMD)
		if !bytes.Equal(restored, origSurrounding) {
			t.Fatalf("middle roundtrip mismatch:\ngot:  %q\nwant: %q", restored, origSurrounding)
		}
	})

	t.Run("shared_empty_file_deleted_on_uninstall", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		agentsMD := filepath.Join(root, "AGENTS.md")
		mustWriteFile(t, agentsMD, []byte{}, 0o644)

		sum, err := Run(Options{Scope: ScopeProject, ProjectRoot: root})
		if err != nil {
			t.Fatalf("install into empty shared file failed: %v", err)
		}
		if sum.Results[0].Action != ActionUpdated {
			t.Fatalf("Action = %q, want %q", sum.Results[0].Action, ActionUpdated)
		}

		if _, err := Run(Options{Scope: ScopeProject, ProjectRoot: root, Uninstall: true}); err != nil {
			t.Fatalf("uninstall failed: %v", err)
		}
		assertNotExists(t, agentsMD)
	})
}

func TestGlobalCursorAndWindsurfDetectionAndExplicitFilter(t *testing.T) {
	t.Parallel()

	t.Run("explicit_without_dirs_creates_zero_directories", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()

		sum, err := Run(Options{
			Scope:   ScopeGlobal,
			HomeDir: home,
			Agents:  []string{"cursor", "windsurf", "codex"},
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if sum.ActiveCount != 2 {
			t.Fatalf("ActiveCount = %d, want 2", sum.ActiveCount)
		}
		assertFileExists(t, filepath.Join(home, ".cursorrules"))
		assertFileExists(t, filepath.Join(home, ".windsurfrules"))
		assertNotExists(t, filepath.Join(home, ".cursor"))
		assertNotExists(t, filepath.Join(home, ".codeium"))
		assertNotExists(t, filepath.Join(home, ".codex"))

		// Subsequent auto-scan now includes Cursor and Windsurf because .cursorrules/.windsurfrules exist.
		autoSum, err := Run(Options{Scope: ScopeGlobal, HomeDir: home})
		if err != nil {
			t.Fatalf("auto-scan failed: %v", err)
		}
		if len(autoSum.Results) != 7 {
			t.Fatalf("expected 7 targets in auto-scan when cursor+windsurf rules exist, got %d", len(autoSum.Results))
		}

		// Auto-scan uninstall removes both dot-files completely since they have no user content.
		if _, err := Run(Options{Scope: ScopeGlobal, HomeDir: home, Uninstall: true}); err != nil {
			t.Fatalf("uninstall failed: %v", err)
		}
		assertNotExists(t, filepath.Join(home, ".cursorrules"))
		assertNotExists(t, filepath.Join(home, ".windsurfrules"))
	})

	t.Run("explicit_with_existing_tool_dirs_uses_nested_paths", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		mustMkdirAll(t, filepath.Join(home, ".cursor"))
		mustMkdirAll(t, filepath.Join(home, ".codeium", "windsurf"))

		sum, err := Run(Options{
			Scope:   ScopeGlobal,
			HomeDir: home,
			Agents:  []string{"cursor", "windsurf"},
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if sum.ActiveCount != 2 {
			t.Fatalf("ActiveCount = %d, want 2", sum.ActiveCount)
		}
		cursorRule := filepath.Join(home, ".cursor", "rules", "git-by.md")
		windsurfRule := filepath.Join(home, ".codeium", "windsurf", "memories", "global_rules.md")
		assertFileExists(t, cursorRule)
		assertFileExists(t, windsurfRule)
		assertNotExists(t, filepath.Join(home, ".cursorrules"))
		assertNotExists(t, filepath.Join(home, ".windsurfrules"))

		// Auto-scan picks up both nested files and uninstalls them cleanly.
		unSum, err := Run(Options{Scope: ScopeGlobal, HomeDir: home, Uninstall: true})
		if err != nil {
			t.Fatalf("uninstall failed: %v", err)
		}
		if unSum.ActiveCount != 2 {
			t.Fatalf("uninstall ActiveCount = %d, want 2", unSum.ActiveCount)
		}
		assertNotExists(t, cursorRule)
		assertNotExists(t, windsurfRule)
	})
}

func TestProjectScopeRulesAndAgentMapping(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	claudeMD := filepath.Join(root, "CLAUDE.md")
	origClaude := []byte("# Claude Project Instructions\n\nFollow Go style.\n")
	mustWriteFile(t, claudeMD, origClaude, 0o644)

	// 1. Default --project: creates AGENTS.md (<your-harness-name>) and updates existing CLAUDE.md (claude-code);
	// does NOT create .cursorrules or .windsurfrules.
	sum, err := Run(Options{
		Scope:       ScopeProject,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatalf("Run project failed: %v", err)
	}
	if sum.ActiveCount != 2 || len(sum.Results) != 2 {
		t.Fatalf("unexpected project summary: ActiveCount=%d len=%d", sum.ActiveCount, len(sum.Results))
	}

	agentsMD := filepath.Join(root, "AGENTS.md")
	assertFileExists(t, agentsMD)
	assertNotExists(t, filepath.Join(root, ".cursorrules"))
	assertNotExists(t, filepath.Join(root, ".windsurfrules"))

	if got := string(mustReadFile(t, agentsMD)); !strings.Contains(got, "--harness <your-harness-name>") {
		t.Fatalf("expected generic harness in AGENTS.md, got:\n%s", got)
	}
	if got := string(mustReadFile(t, claudeMD)); !strings.Contains(got, "--harness claude-code") {
		t.Fatalf("expected claude-code harness in CLAUDE.md, got:\n%s", got)
	}

	// 2. Explicit single AGENTS.md agent (--agent agy) sets specific harness; explicit --agent cursor creates .cursorrules.
	sumSingle, err := Run(Options{
		Scope:       ScopeProject,
		ProjectRoot: root,
		Agents:      []string{"agy", "cursor"},
	})
	if err != nil {
		t.Fatalf("Run project --agent agy,cursor failed: %v", err)
	}
	if sumSingle.ActiveCount != 2 {
		t.Fatalf("ActiveCount = %d, want 2", sumSingle.ActiveCount)
	}
	if got := string(mustReadFile(t, agentsMD)); !strings.Contains(got, "--harness antigravity") {
		t.Fatalf("expected antigravity harness when single AGENTS.md agent requested, got:\n%s", got)
	}
	assertFileExists(t, filepath.Join(root, ".cursorrules"))

	// 3. Multiple AGENTS.md-mapped agents (--agent agy,pi) deduplicate AGENTS.md and use generic placeholder.
	sumMulti, err := Run(Options{
		Scope:       ScopeProject,
		ProjectRoot: root,
		Agents:      []string{"agy", "pi"},
	})
	if err != nil {
		t.Fatalf("Run project --agent agy,pi failed: %v", err)
	}
	if len(sumMulti.Results) != 1 {
		t.Fatalf("expected deduplicated single AGENTS.md result, got %d", len(sumMulti.Results))
	}
	if got := string(mustReadFile(t, agentsMD)); !strings.Contains(got, "--harness <your-harness-name>") {
		t.Fatalf("expected generic harness when multiple AGENTS.md agents requested, got:\n%s", got)
	}

	// 4. Uninstall all project rules: deletes created AGENTS.md and .cursorrules, restores CLAUDE.md byte-for-byte.
	unSum, err := Run(Options{
		Scope:       ScopeProject,
		ProjectRoot: root,
		Uninstall:   true,
	})
	if err != nil {
		t.Fatalf("project uninstall failed: %v", err)
	}
	if unSum.ActiveCount != 3 {
		t.Fatalf("project uninstall ActiveCount = %d, want 3", unSum.ActiveCount)
	}
	assertNotExists(t, agentsMD)
	assertNotExists(t, filepath.Join(root, ".cursorrules"))
	if got := mustReadFile(t, claudeMD); !bytes.Equal(got, origClaude) {
		t.Fatalf("CLAUDE.md not restored byte-for-byte:\ngot:  %q\nwant: %q", got, origClaude)
	}
}

func TestValidationErrors(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	if _, err := Run(Options{Scope: ScopeGlobal}); err == nil {
		t.Fatal("expected error when HomeDir is empty for ScopeGlobal")
	}
	if _, err := Run(Options{Scope: ScopeProject}); err == nil {
		t.Fatal("expected error when ProjectRoot is empty for ScopeProject")
	}
	if _, err := Run(Options{Scope: Scope("invalid"), HomeDir: tmp}); err == nil {
		t.Fatal("expected error for invalid Scope")
	}
	if _, err := Run(Options{Scope: ScopeGlobal, HomeDir: filepath.Join(tmp, "missing")}); err == nil {
		t.Fatal("expected error when HomeDir does not exist")
	}
	if _, err := Run(Options{Scope: ScopeGlobal, HomeDir: tmp, Agents: []string{"bad"}}); err == nil {
		t.Fatal("expected error for unknown agent in Run")
	}
	if err := RenderSummary(nil, &Summary{}, false); err == nil {
		t.Fatal("expected error for nil writer in RenderSummary")
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(Dir(%s)): %v", path, err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return data
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to not exist, got err=%v", path, err)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file %s to exist: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("expected %s to be a file, got directory", path)
	}
}
