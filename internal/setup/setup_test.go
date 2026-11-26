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
		" Antigravity , pi-agent, pi-coding-agent ",
		"CLAUDE-CODE,openai-codex,codex-cli,open-code,DEEPSEEK-HARNESS",
		"agy,dsh,Cursor,codeium-windsurf,oh-my-pi,omp,claude,gemini-cli,github-copilot,gh-copilot,copilot-cli",
		"cline,roo-code,roocode,roo-cline,kilocode,kilo-code,factory,factory-droid",
		"ampcode,amp-code,block-goose,charm-crush,kiro-cli,aws-kiro,continue-dev,cn",
		"trae-ide,auggie,augment-code,qwen-code,kimi-code,kimi-cli,codebuddy-code,tencent-codebuddy",
		"jetbrains-junie,zed-editor,aider,open-hands,hermes-agent",
	})
	if err != nil {
		t.Fatalf("ParseAgents unexpected error: %v", err)
	}
	want := SupportedAgentIDs()
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
		{"agy,not-a-real-harness"},
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
	wantAutoResults := len(SupportedAgentIDs()) - 2 // all global harnesses except cursor/windsurf dotfiles
	if len(drySummary.Results) != wantAutoResults {
		t.Fatalf("DryRun len(Results) = %d, want %d", len(drySummary.Results), wantAutoResults)
	}
	assertNotExists(t, filepath.Join(home, ".gemini", "config"))
	assertNotExists(t, filepath.Join(home, ".gemini", "GEMINI.md"))
	assertNotExists(t, filepath.Join(home, ".claude", "rules"))
	assertNotExists(t, filepath.Join(home, ".codex"))
	assertNotExists(t, filepath.Join(home, ".config"))
	assertNotExists(t, filepath.Join(home, ".dsh"))
	assertNotExists(t, filepath.Join(home, ".copilot"))
	assertNotExists(t, filepath.Join(home, ".cline"))
	assertNotExists(t, filepath.Join(home, ".roo"))
	assertNotExists(t, filepath.Join(home, ".kilocode"))
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
		"  ✔ Antigravity        installed   ~/.gemini/config/rules/git-by.md",
		"  ✔ Pi Agent           updated     ~/.pi/agent/APPEND_SYSTEM.md",
		"  ✔ Claude Code        installed   ~/.claude/rules/git-by.md",
		"  - Codex              skipped     (~/.codex not found)",
		"  - OpenCode           skipped     (~/.config/opencode not found)",
		"  - DeepSeek Harness   skipped     (~/.dsh not found)",
		"  - Oh My Pi           skipped     (~/.omp not found)",
		"  - Gemini CLI         skipped     (~/.gemini/settings.json not found)",
		"  - GitHub Copilot     skipped     (~/.copilot not found)",
		"  - Cline              skipped     (~/.cline not found)",
		"  - Roo Code           skipped     (~/.roo not found)",
		"  - Kilo Code          skipped     (~/.kilocode not found)",
		"  - Factory Droid      skipped     (~/.factory not found)",
		"  - Amp                skipped     (~/.config/amp not found)",
		"  - Goose              skipped     (~/.config/goose not found)",
		"  - Crush              skipped     (~/.config/crush not found)",
		"  - Kiro               skipped     (~/.kiro not found)",
		"  - Continue           skipped     (~/.continue not found)",
		"  - Trae               skipped     (~/.trae not found)",
		"  - Augment            skipped     (~/.augment not found)",
		"  - Qwen Code          skipped     (~/.qwen not found)",
		"  - Kimi Code          skipped     (~/.kimi-code not found)",
		"  - CodeBuddy          skipped     (~/.codebuddy not found)",
		"  - Junie              skipped     (~/.junie not found)",
		"  - Zed                skipped     (~/.config/zed not found)",
		"  - Aider              skipped     (~/.aider not found)",
		"  - OpenHands          skipped     (~/.openhands not found)",
		"  - Hermes Agent       skipped     (~/.hermes not found)",
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
	assertNotExists(t, filepath.Join(home, ".dsh"))
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
		if len(autoSum.Results) != len(SupportedAgentIDs()) {
			t.Fatalf("expected %d targets in auto-scan when cursor+windsurf rules exist, got %d", len(SupportedAgentIDs()), len(autoSum.Results))
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

func TestDeepSeekHarnessGlobalDSHHomeAndProjectMapping(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	origDshNotes := []byte("# Global DeepSeek Harness Instructions\n\n- Keep diffs focused.\n")
	dshAgentsPath := filepath.Join(home, ".dsh", "AGENTS.md")
	mustWriteFile(t, dshAgentsPath, origDshNotes, 0o644)

	// 1. Auto-detects ~/.dsh and updates ~/.dsh/AGENTS.md with --harness dsh.
	sum, err := Run(Options{Scope: ScopeGlobal, HomeDir: home})
	if err != nil {
		t.Fatalf("Run global with ~/.dsh failed: %v", err)
	}
	if sum.ActiveCount != 1 {
		t.Fatalf("ActiveCount = %d, want 1", sum.ActiveCount)
	}
	gotDsh := string(mustReadFile(t, dshAgentsPath))
	if !strings.HasPrefix(gotDsh, string(origDshNotes)) || !strings.Contains(gotDsh, "--harness dsh") {
		t.Fatalf("unexpected ~/.dsh/AGENTS.md content:\n%s", gotDsh)
	}

	// Lossless uninstall restores ~/.dsh/AGENTS.md byte-for-byte.
	if _, err := Run(Options{Scope: ScopeGlobal, HomeDir: home, Uninstall: true}); err != nil {
		t.Fatalf("Uninstall ~/.dsh/AGENTS.md failed: %v", err)
	}
	if restored := mustReadFile(t, dshAgentsPath); !bytes.Equal(restored, origDshNotes) {
		t.Fatalf("~/.dsh/AGENTS.md not restored byte-for-byte:\ngot: %q\nwant: %q", restored, origDshNotes)
	}

	// 2. Respects $DSH_HOME (including tilde expansion ~/custom-dsh).
	customDir := filepath.Join(home, "custom-dsh")
	mustMkdirAll(t, customDir)
	sumCustom, err := Run(Options{
		Scope:   ScopeGlobal,
		HomeDir: home,
		DSHHome: "~/custom-dsh",
		Agents:  []string{"dsh"},
	})
	if err != nil {
		t.Fatalf("Run with DSHHome failed: %v", err)
	}
	if sumCustom.ActiveCount != 1 {
		t.Fatalf("ActiveCount = %d, want 1", sumCustom.ActiveCount)
	}
	customAgents := filepath.Join(customDir, "AGENTS.md")
	assertFileExists(t, customAgents)
	if !strings.Contains(string(mustReadFile(t, customAgents)), "--harness dsh") {
		t.Fatalf("expected --harness dsh in %s", customAgents)
	}

	// 3. Project scope --agent dsh maps to AGENTS.md with --harness dsh.
	projRoot := t.TempDir()
	projSum, err := Run(Options{
		Scope:       ScopeProject,
		ProjectRoot: projRoot,
		Agents:      []string{"dsh"},
	})
	if err != nil {
		t.Fatalf("Run project --agent dsh failed: %v", err)
	}
	if projSum.ActiveCount != 1 || projSum.Results[0].DisplayName != "DeepSeek Harness" {
		t.Fatalf("unexpected project dsh summary: %+v", projSum)
	}
	if !strings.Contains(string(mustReadFile(t, filepath.Join(projRoot, "AGENTS.md"))), "--harness dsh") {
		t.Fatalf("expected --harness dsh in project AGENTS.md")
	}
}

func TestAllMainstreamHarnessesGlobalAndProjectRoundtrip(t *testing.T) {
	t.Parallel()

	t.Run("global_all_harnesses_install_and_lossless_uninstall", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()

		// Simulate Gemini CLI marker + user content in a few shared global files to verify lossless roundtrip.
		mustWriteFile(t, filepath.Join(home, ".gemini", "settings.json"), []byte("{}\n"), 0o644)
		origCopilot := []byte("# Personal Copilot Rules\n\n- Prefer small PRs.\n")
		mustWriteFile(t, filepath.Join(home, ".copilot", "copilot-instructions.md"), origCopilot, 0o644)
		origGoose := []byte("# Personal Goose Hints\n\n- Run tests first.\n")
		mustWriteFile(t, filepath.Join(home, ".config", "goose", ".goosehints"), origGoose, 0o644)
		origHermes := []byte("# Hermes Soul\n\n- Be direct and precise.\n")
		mustWriteFile(t, filepath.Join(home, ".hermes", "SOUL.md"), origHermes, 0o644)

		allIDs := SupportedAgentIDs()
		// Interactive selection installs all 29 harnesses even when directories were absent!
		sum, err := Run(Options{
			Scope:             ScopeGlobal,
			HomeDir:           home,
			SelectedTargets:   allIDs,
			CreateMissingDirs: true,
		})
		if err != nil {
			t.Fatalf("install all harnesses failed: %v", err)
		}
		if sum.ActiveCount != len(allIDs) {
			t.Fatalf("ActiveCount = %d, want %d", sum.ActiveCount, len(allIDs))
		}

		expectedGlobal := []struct {
			id      string
			rel     string
			harness string
		}{
			{"agy", ".gemini/config/rules/git-by.md", "antigravity"},
			{"pi", ".pi/agent/APPEND_SYSTEM.md", "pi"},
			{"claude", ".claude/rules/git-by.md", "claude-code"},
			{"codex", ".codex/AGENTS.md", "codex"},
			{"opencode", ".config/opencode/AGENTS.md", "opencode"},
			{"dsh", ".dsh/AGENTS.md", "dsh"},
			{"cursor", ".cursorrules", "cursor"},
			{"windsurf", ".windsurfrules", "windsurf"},
			{"omp", ".omp/agent/APPEND_SYSTEM.md", "omp"},
			{"gemini", ".gemini/GEMINI.md", "gemini-cli"},
			{"copilot", ".copilot/copilot-instructions.md", "copilot"},
			{"cline", ".cline/rules/git-by.md", "cline"},
			{"roo", ".roo/rules/git-by.md", "roo-code"},
			{"kilo", ".kilocode/rules/git-by.md", "kilocode"},
			{"droid", ".factory/AGENTS.md", "droid"},
			{"amp", ".config/amp/AGENTS.md", "amp"},
			{"goose", ".config/goose/.goosehints", "goose"},
			{"crush", ".config/crush/CRUSH.md", "crush"},
			{"kiro", ".kiro/steering/git-by.md", "kiro"},
			{"continue", ".continue/rules/git-by.md", "continue"},
			{"trae", ".trae/user_rules.md", "trae"},
			{"augment", ".augment/rules/git-by.md", "augment"},
			{"qwen", ".qwen/QWEN.md", "qwen-code"},
			{"kimi", ".kimi-code/AGENTS.md", "kimi-code"},
			{"codebuddy", ".codebuddy/rules/git-by.md", "codebuddy"},
			{"junie", ".junie/AGENTS.md", "junie"},
			{"zed", ".config/zed/AGENTS.md", "zed"},
			{"aider", ".aider/CONVENTIONS.md", "aider"},
			{"openhands", ".openhands/microagents/git-by.md", "openhands"},
			{"hermes", ".hermes/SOUL.md", "hermes"},
		}

		for _, exp := range expectedGlobal {
			full := filepath.Join(home, filepath.FromSlash(exp.rel))
			assertFileExists(t, full)
			got := string(mustReadFile(t, full))
			if !strings.Contains(got, "--harness "+exp.harness) {
				t.Fatalf("agent %s (%s) missing '--harness %s':\n%s", exp.id, exp.rel, exp.harness, got)
			}
		}

		// Auto-scan uninstall removes all 29 targets and restores pre-existing shared files byte-for-byte!
		unSum, err := Run(Options{
			Scope:     ScopeGlobal,
			HomeDir:   home,
			Uninstall: true,
		})
		if err != nil {
			t.Fatalf("uninstall all harnesses failed: %v", err)
		}
		if unSum.ActiveCount != len(allIDs) {
			t.Fatalf("uninstall ActiveCount = %d, want %d", unSum.ActiveCount, len(allIDs))
		}
		if got := mustReadFile(t, filepath.Join(home, ".copilot", "copilot-instructions.md")); !bytes.Equal(got, origCopilot) {
			t.Fatalf("copilot-instructions.md not restored byte-for-byte")
		}
		if got := mustReadFile(t, filepath.Join(home, ".config", "goose", ".goosehints")); !bytes.Equal(got, origGoose) {
			t.Fatalf(".goosehints not restored byte-for-byte")
		}
		if got := mustReadFile(t, filepath.Join(home, ".hermes", "SOUL.md")); !bytes.Equal(got, origHermes) {
			t.Fatalf("SOUL.md not restored byte-for-byte")
		}
		for _, standalone := range []string{
			".cline/rules/git-by.md",
			".roo/rules/git-by.md",
			".kilocode/rules/git-by.md",
			".kiro/steering/git-by.md",
			".continue/rules/git-by.md",
			".augment/rules/git-by.md",
			".codebuddy/rules/git-by.md",
			".openhands/microagents/git-by.md",
		} {
			assertNotExists(t, filepath.Join(home, filepath.FromSlash(standalone)))
		}
	})

	t.Run("alternate_global_paths_and_env_overrides", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()

		// Classic Cline ~/Documents/Cline, modern Kilo ~/.config/kilo, legacy Kimi ~/.kimi,
		// Trae directory ~/.trae/user_rules/, Junie legacy ~/.junie/guidelines.md, plus env overrides.
		mustMkdirAll(t, filepath.Join(home, "Documents", "Cline"))
		mustMkdirAll(t, filepath.Join(home, ".config", "kilo"))
		mustMkdirAll(t, filepath.Join(home, ".kimi"))
		mustMkdirAll(t, filepath.Join(home, ".trae", "user_rules"))
		mustWriteFile(t, filepath.Join(home, ".junie", "guidelines.md"), []byte("# Junie\n"), 0o644)
		mustMkdirAll(t, filepath.Join(home, "custom-codex"))
		mustMkdirAll(t, filepath.Join(home, "custom-copilot"))
		mustMkdirAll(t, filepath.Join(home, "custom-hermes"))
		mustMkdirAll(t, filepath.Join(home, "custom-omp", "agent"))
		mustMkdirAll(t, filepath.Join(home, "custom-pi", "agent"))

		sum, err := Run(Options{
			Scope:       ScopeGlobal,
			HomeDir:     home,
			PiAgentDir:  "~/custom-pi/agent",
			OmpAgentDir: "~/custom-omp/agent",
			CodexHome:   "~/custom-codex",
			CopilotHome: "~/custom-copilot",
			HermesHome:  "~/custom-hermes",
			Agents:      []string{"cline", "kilo", "kimi", "trae", "junie", "codex", "copilot", "hermes", "omp", "pi"},
		})
		if err != nil {
			t.Fatalf("alternate global paths Run failed: %v", err)
		}
		if sum.ActiveCount != 10 {
			t.Fatalf("ActiveCount = %d, want 10", sum.ActiveCount)
		}
		assertFileExists(t, filepath.Join(home, "Documents", "Cline", "Rules", "git-by.md"))
		assertFileExists(t, filepath.Join(home, ".config", "kilo", "AGENTS.md"))
		assertFileExists(t, filepath.Join(home, ".kimi", "AGENTS.md"))
		assertFileExists(t, filepath.Join(home, ".trae", "user_rules", "user_rules.md"))
		assertFileExists(t, filepath.Join(home, ".junie", "guidelines.md"))
		assertFileExists(t, filepath.Join(home, "custom-codex", "AGENTS.md"))
		assertFileExists(t, filepath.Join(home, "custom-copilot", "copilot-instructions.md"))
		assertFileExists(t, filepath.Join(home, "custom-hermes", "SOUL.md"))
		assertFileExists(t, filepath.Join(home, "custom-omp", "agent", "APPEND_SYSTEM.md"))
		assertFileExists(t, filepath.Join(home, "custom-pi", "agent", "APPEND_SYSTEM.md"))
	})

	t.Run("project_scope_all_dedicated_and_agents_md_mappings", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()

		// .github/workflows existing MUST NOT falsely trigger GitHub Copilot auto-detection!
		mustWriteFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644)
		// Existing .kiro/steering/product.md SHOULD trigger Kiro auto-detection!
		mustWriteFile(t, filepath.Join(root, ".kiro", "steering", "product.md"), []byte("# Product\n"), 0o644)

		autoSum, err := Run(Options{Scope: ScopeProject, ProjectRoot: root})
		if err != nil {
			t.Fatalf("project auto Run failed: %v", err)
		}
		if autoSum.ActiveCount != 2 {
			t.Fatalf("expected 2 targets (AGENTS.md + Kiro), got %d: %+v", autoSum.ActiveCount, autoSum.Results)
		}
		assertNotExists(t, filepath.Join(root, ".github", "copilot-instructions.md"))
		assertFileExists(t, filepath.Join(root, ".kiro", "steering", "git-by.md"))

		// Explicit dedicated project targets + single AGENTS.md-mapped target (droid).
		expSum, err := Run(Options{
			Scope:       ScopeProject,
			ProjectRoot: root,
			Agents: []string{
				"droid", "gemini", "copilot", "cline", "roo", "kilo", "goose",
				"crush", "continue", "trae", "augment", "qwen", "codebuddy", "junie", "zed", "aider",
			},
		})
		if err != nil {
			t.Fatalf("project explicit Run failed: %v", err)
		}
		if expSum.ActiveCount != 16 {
			t.Fatalf("ActiveCount = %d, want 16", expSum.ActiveCount)
		}
		if !strings.Contains(string(mustReadFile(t, filepath.Join(root, "AGENTS.md"))), "--harness droid") {
			t.Fatalf("expected --harness droid in AGENTS.md")
		}
		for _, rel := range []string{
			"GEMINI.md",
			".github/copilot-instructions.md",
			".clinerules/git-by.md",
			".roo/rules/git-by.md",
			".kilocode/rules/git-by.md",
			".goosehints",
			"CRUSH.md",
			".continue/rules/git-by.md",
			".trae/rules/git-by.md",
			".augment/rules/git-by.md",
			"QWEN.md",
			"CODEBUDDY.md",
			".junie/guidelines.md",
			".rules",
			"CONVENTIONS.md",
		} {
			assertFileExists(t, filepath.Join(root, filepath.FromSlash(rel)))
		}

		// Project uninstall removes all 17 configured targets cleanly!
		unSum, err := Run(Options{Scope: ScopeProject, ProjectRoot: root, Uninstall: true})
		if err != nil {
			t.Fatalf("project uninstall failed: %v", err)
		}
		if unSum.ActiveCount != 17 {
			t.Fatalf("project uninstall ActiveCount = %d, want 17", unSum.ActiveCount)
		}
		assertNotExists(t, filepath.Join(root, ".github", "copilot-instructions.md"))
		assertNotExists(t, filepath.Join(root, ".kiro", "steering", "git-by.md"))
		assertFileExists(t, filepath.Join(root, ".kiro", "steering", "product.md"))
	})
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
