package stats

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func initTempRepo(t *testing.T, repoDir string) {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repoDir, err)
	}
	runGitTest(t, repoDir, nil, "init")
	runGitTest(t, repoDir, nil, "config", "user.name", "Shelby Miller")
	runGitTest(t, repoDir, nil, "config", "user.email", "shelby@example.com")
	// Disable commit signing in case host environment has global gpgsign enabled.
	runGitTest(t, repoDir, nil, "config", "commit.gpgsign", "false")
}

func runGitTest(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	env = append(env, extraEnv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\noutput: %s", args, err, string(out))
	}
	return string(out)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func commitWithEnv(t *testing.T, repoDir string, authorName, dateISO, message string) {
	t.Helper()
	runGitTest(t, repoDir, nil, "add", "-A")
	env := []string{
		"GIT_AUTHOR_DATE=" + dateISO,
		"GIT_COMMITTER_DATE=" + dateISO,
	}
	if authorName != "" {
		env = append(env, "GIT_AUTHOR_NAME="+authorName)
	}
	runGitTest(t, repoDir, env, "commit", "-m", message)
}

func validateStrictSVG(t *testing.T, data []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true

	sawRootSVG := false
	hasSVGNS := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid SVG XML: %v\nSVG:\n%s", err, string(data))
		}
		if start, ok := tok.(xml.StartElement); ok && !sawRootSVG {
			sawRootSVG = true
			if start.Name.Local != "svg" {
				t.Fatalf("root XML element = %q, want \"svg\"", start.Name.Local)
			}
			if start.Name.Space == "http://www.w3.org/2000/svg" {
				hasSVGNS = true
			}
			for _, attr := range start.Attr {
				if attr.Name.Local == "xmlns" && attr.Value == "http://www.w3.org/2000/svg" {
					hasSVGNS = true
				}
			}
		}
	}
	if !sawRootSVG {
		t.Fatal("SVG document had no root element")
	}
	if !hasSVGNS {
		t.Fatal("SVG root missing xmlns=\"http://www.w3.org/2000/svg\"")
	}
}

func TestCollect_EmptyRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "empty-repo")
	initTempRepo(t, root)

	rep, err := Collect(root, Options{})
	if err != nil {
		t.Fatalf("Collect on empty repo returned error: %v", err)
	}
	if rep.RepoName != "empty-repo" {
		t.Errorf("RepoName = %q, want %q", rep.RepoName, "empty-repo")
	}
	if rep.TotalCommits != 0 || rep.TotalLines != 0 {
		t.Errorf("expected 0 commits & lines, got %d commits, %d lines", rep.TotalCommits, rep.TotalLines)
	}
	if rep.Human != (Breakdown{}) || rep.AI != (Breakdown{}) {
		t.Errorf("expected zeroed breakdowns, got human=%+v ai=%+v", rep.Human, rep.AI)
	}
	if rep.Implementers == nil || len(rep.Implementers) != 0 {
		t.Errorf("Implementers should be empty non-nil slice, got %#v", rep.Implementers)
	}
	if rep.Planners == nil || len(rep.Planners) != 0 {
		t.Errorf("Planners should be empty non-nil slice, got %#v", rep.Planners)
	}

	// Renderers should all handle empty reports cleanly.
	var jsonBuf, termBuf bytes.Buffer
	if err := RenderJSON(&jsonBuf, rep); err != nil {
		t.Fatalf("RenderJSON failed: %v", err)
	}
	if !strings.Contains(jsonBuf.String(), `"implementers": []`) {
		t.Errorf("expected empty JSON array for implementers, got:\n%s", jsonBuf.String())
	}
	if err := RenderTerminal(&termBuf, rep, false); err != nil {
		t.Fatalf("RenderTerminal failed: %v", err)
	}
	svg, err := RenderBadgeSVG(rep)
	if err != nil {
		t.Fatalf("RenderBadgeSVG failed: %v", err)
	}
	validateStrictSVG(t, svg)
}

func TestCollect_NonRepoErrors(t *testing.T) {
	parent := t.TempDir()
	nonRepo := filepath.Join(parent, "plain-dir")
	if err := os.MkdirAll(nonRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", parent)

	if _, err := Collect(nonRepo, Options{}); err == nil {
		t.Fatal("expected error for non-git directory, got nil")
	}
	if _, err := Collect(filepath.Join(parent, "does-not-exist"), Options{}); err == nil {
		t.Fatal("expected error for missing directory, got nil")
	}
}

func TestCollect_MultiRoleProvenanceAndBreakdowns(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "awesome-agent-repo")
	initTempRepo(t, repoDir)

	// 1. Pure Human commit (2025) - author "Shelby Miller" contains substring "by" without spaces;
	// body also contains an indented example trailer that must NOT trigger AI detection.
	writeTestFile(t, filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"v1\")\n}\n"))
	commitWithEnv(t, repoDir, "Shelby Miller", "2025-06-10T10:00:00Z",
		"feat: initial human scaffold\n\nExample trailer format:\n  Assisted-by: fake-indented-model\n\nImplemented manually.")

	// 2. Full Multi-Role AI Relay commit + binary asset (binary "-" numstat must be ignored).
	writeTestFile(t, filepath.Join(repoDir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"v2\")\n\tfmt.Println(\"relay\")\n}\n"))
	writeTestFile(t, filepath.Join(repoDir, "logo.bin"), []byte{0x00, 0xff, 0x89, 0x50, 0x4e, 0x47, 0x00})
	commitWithEnv(t, repoDir, "Shelby Miller by claude-sonnet-4 & 1 more", "2026-02-01T12:00:00Z",
		strings.Join([]string{
			"feat: multi-agent auth relay",
			"",
			"10\t20\tfake_numstat_in_body.go",
			"",
			"Assisted-by: pi:claude-sonnet-4 [golangci-lint] [go-test]",
			"X-Agent-Orchestrator: anthropic/claude-opus-4",
			"X-Agent-Planner: openai/o3",
			"X-Agent-Implementer: anthropic/claude-sonnet-4, google/gemini-2.5-pro",
			"X-Agent-Reviewer: openai/gpt-4.1",
			"X-Agent-Committer: google/gemini-2.5-flash",
			"X-Agent-Harness: pi@1.0.0",
		}, "\n"))

	// 3. Kernel-style Assisted-by-only commit (no X-Agent-Implementer trailer).
	writeTestFile(t, filepath.Join(repoDir, "kernel_style.go"), []byte("package main\n\nfunc helper() int {\n\treturn 42\n}\n"))
	commitWithEnv(t, repoDir, "Shelby Miller", "2026-02-05T12:00:00Z",
		"fix: handle edge case\n\nAssisted-by: cursor:anthropic/claude-sonnet-4 [rg] [go-test]")

	// 4. Planner-only AI commit (Assisted-by echoes short Planner model; should count Planner without polluting Implementers).
	writeTestFile(t, filepath.Join(repoDir, "ADR.md"), []byte("# ADR 001\nUse NUL-delimited git log parsing.\n"))
	commitWithEnv(t, repoDir, "Shelby Miller by o3", "2026-02-10T12:00:00Z",
		"docs: add architecture decision record\n\nAssisted-by: pi:o3\nX-Agent-Planner: openai/o3\nX-Agent-Harness: pi@1.0.0")

	// 5. AuthorName-only provenance commit ("<name> by <model>") on a feature branch, merged with --no-ff.
	defaultBranch := strings.TrimSpace(runGitTest(t, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD"))
	runGitTest(t, repoDir, nil, "checkout", "-b", "feat-branch")
	writeTestFile(t, filepath.Join(repoDir, "branch.go"), []byte("package main\n\nconst version = \"0.1.0\"\n"))
	commitWithEnv(t, repoDir, "Alice Wonderland by deepseek-r1", "2026-02-15T12:00:00Z", "chore: add version constant")

	runGitTest(t, repoDir, nil, "checkout", defaultBranch)
	writeTestFile(t, filepath.Join(repoDir, "README.txt"), []byte("human docs\nline two\n"))
	commitWithEnv(t, repoDir, "Shelby Miller", "2026-02-16T12:00:00Z", "docs: add human readme")

	// Merge commit (--no-merges must skip the merge commit itself).
	mergeEnv := []string{
		"GIT_AUTHOR_DATE=2026-02-17T12:00:00Z",
		"GIT_COMMITTER_DATE=2026-02-17T12:00:00Z",
	}
	runGitTest(t, repoDir, mergeEnv, "merge", "--no-ff", "feat-branch", "-m", "Merge branch 'feat-branch'")

	// Collect from a nested subdirectory to verify git rev-parse --show-toplevel RepoName resolution.
	subDir := filepath.Join(repoDir, "nested", "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rep, err := Collect(subDir, Options{})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if rep.RepoName != "awesome-agent-repo" {
		t.Errorf("RepoName = %q, want %q", rep.RepoName, "awesome-agent-repo")
	}
	// 6 non-merge commits total: 2 Human (#1, #6), 4 AI (#2, #3, #4, #5).
	if rep.TotalCommits != 6 {
		t.Fatalf("TotalCommits = %d, want 6", rep.TotalCommits)
	}
	if rep.Human.Commits != 2 || rep.AI.Commits != 4 {
		t.Fatalf("Human.Commits=%d AI.Commits=%d, want 2 and 4", rep.Human.Commits, rep.AI.Commits)
	}
	if rep.Human.CommitPercent != 33.33 || rep.AI.CommitPercent != 66.67 {
		t.Errorf("CommitPercent: Human=%.2f AI=%.2f, want 33.33 and 66.67", rep.Human.CommitPercent, rep.AI.CommitPercent)
	}

	// Verify line math:
	// Human: #1 (+5/-0), #6 (+2/-0) => +7 / -0 = 7 lines
	// AI: #2 (+4/-1 on main.go, logo.bin skipped => +4/-1 = 5), #3 (+5/-0 = 5), #4 (+2/-0 = 2), #5 (+3/-0 = 3) => +14 / -1 = 15 lines
	// TotalLines = 7 + 15 = 22
	if rep.Human.Additions != 7 || rep.Human.Deletions != 0 {
		t.Errorf("Human lines = +%d/-%d, want +7/-0", rep.Human.Additions, rep.Human.Deletions)
	}
	if rep.AI.Additions != 14 || rep.AI.Deletions != 1 {
		t.Errorf("AI lines = +%d/-%d, want +14/-1", rep.AI.Additions, rep.AI.Deletions)
	}
	if rep.TotalLines != 22 {
		t.Errorf("TotalLines = %d, want 22", rep.TotalLines)
	}

	// Verify Implementers leaderboard (4 AI commits total):
	// anthropic/claude-sonnet-4: 2 commits (#2 explicit + #3 Assisted-by fallback) -> 50%
	// google/gemini-2.5-pro: 1 commit (#2) -> 25%
	wantImpl := []ModelCount{
		{Model: "anthropic/claude-sonnet-4", Commits: 2, Percent: 50},
		{Model: "google/gemini-2.5-pro", Commits: 1, Percent: 25},
	}
	if len(rep.Implementers) != len(wantImpl) {
		t.Fatalf("Implementers = %+v, want %+v", rep.Implementers, wantImpl)
	}
	for i := range wantImpl {
		if rep.Implementers[i] != wantImpl[i] {
			t.Errorf("Implementers[%d] = %+v, want %+v", i, rep.Implementers[i], wantImpl[i])
		}
	}

	// Planners: openai/o3 in #2 and #4 -> 2 commits (50%)
	if len(rep.Planners) != 1 || rep.Planners[0] != (ModelCount{Model: "openai/o3", Commits: 2, Percent: 50}) {
		t.Errorf("Planners = %+v, want [{openai/o3 2 50}]", rep.Planners)
	}
	if len(rep.Orchestrators) != 1 || rep.Orchestrators[0].Model != "anthropic/claude-opus-4" {
		t.Errorf("Orchestrators = %+v", rep.Orchestrators)
	}
	if len(rep.Reviewers) != 1 || rep.Reviewers[0].Model != "openai/gpt-4.1" {
		t.Errorf("Reviewers = %+v", rep.Reviewers)
	}
	if len(rep.Committers) != 1 || rep.Committers[0].Model != "google/gemini-2.5-flash" {
		t.Errorf("Committers = %+v", rep.Committers)
	}
	if len(rep.Harnesses) != 1 || rep.Harnesses[0] != (ModelCount{Model: "pi@1.0.0", Commits: 2, Percent: 50}) {
		t.Errorf("Harnesses = %+v, want [{pi@1.0.0 2 50}]", rep.Harnesses)
	}

	// Test --since filtering (excludes 2025 human commit #1 -> 5 commits left: 1 Human, 4 AI).
	sinceRep, err := Collect(repoDir, Options{Since: "2026-01-01", TopN: 1})
	if err != nil {
		t.Fatalf("Collect with Since failed: %v", err)
	}
	if sinceRep.Since != "2026-01-01" {
		t.Errorf("Since = %q, want 2026-01-01", sinceRep.Since)
	}
	if sinceRep.TotalCommits != 5 || sinceRep.Human.Commits != 1 || sinceRep.AI.Commits != 4 {
		t.Errorf("Since report commits: total=%d human=%d ai=%d", sinceRep.TotalCommits, sinceRep.Human.Commits, sinceRep.AI.Commits)
	}
	if len(sinceRep.Implementers) != 1 || sinceRep.Implementers[0].Model != "anthropic/claude-sonnet-4" {
		t.Errorf("TopN=1 Implementers = %+v", sinceRep.Implementers)
	}
}

func TestLeaderboardTieBreakAndDefaultTopN(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "tie-break-repo")
	initTempRepo(t, repoDir)

	models := []string{
		"z-provider/zeta",
		"a-provider/alpha",
		"m-provider/mu",
		"b-provider/beta",
		"c-provider/gamma",
		"d-provider/delta",
	}
	writeTestFile(t, filepath.Join(repoDir, "f.txt"), []byte("seed\n"))
	commitWithEnv(t, repoDir, "Dev", "2026-03-01T10:00:00Z",
		"feat: six models\n\nX-Agent-Implementer: "+strings.Join(models, ", "))

	// Default TopN (<=0) caps at 5, sorted alphabetically when tied on 1 commit.
	rep, err := Collect(repoDir, Options{})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if len(rep.Implementers) != 5 {
		t.Fatalf("len(Implementers) = %d, want 5 (default TopN)", len(rep.Implementers))
	}
	wantOrder := []string{
		"a-provider/alpha",
		"b-provider/beta",
		"c-provider/gamma",
		"d-provider/delta",
		"m-provider/mu",
	}
	for i, want := range wantOrder {
		if rep.Implementers[i].Model != want {
			t.Errorf("Implementers[%d].Model = %q, want %q", i, rep.Implementers[i].Model, want)
		}
	}
}

func TestRenderJSON_Terminal_And_SVG(t *testing.T) {
	rep := &Report{
		RepoName:     `git-by<&>"'demo`,
		Since:        `2026-01-01 & <latest>`,
		TotalCommits: 10,
		TotalLines:   500,
		Human: Breakdown{
			Commits:       3,
			CommitPercent: 30,
			Additions:     100,
			Deletions:     50,
			LinesPercent:  30,
		},
		AI: Breakdown{
			Commits:       7,
			CommitPercent: 70,
			Additions:     280,
			Deletions:     70,
			LinesPercent:  70,
		},
		Implementers: []ModelCount{
			{Model: `anthropic/claude-sonnet-4<&>"'`, Commits: 5, Percent: 71.43},
			{Model: "google/gemini-2.5-pro", Commits: 2, Percent: 28.57},
		},
		Planners: []ModelCount{
			{Model: "openai/o3", Commits: 4, Percent: 57.14},
		},
		Orchestrators: []ModelCount{
			{Model: "anthropic/claude-opus-4", Commits: 2, Percent: 28.57},
		},
		Reviewers: []ModelCount{
			{Model: "openai/gpt-4.1", Commits: 3, Percent: 42.86},
		},
		Harnesses: []ModelCount{
			{Model: "pi@1.0.0", Commits: 6, Percent: 85.71},
		},
	}

	// 1. JSON output validation
	var jsonBuf bytes.Buffer
	if err := RenderJSON(&jsonBuf, rep); err != nil {
		t.Fatalf("RenderJSON failed: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(jsonBuf.Bytes(), &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v\nJSON:\n%s", err, jsonBuf.String())
	}
	if decoded.RepoName != rep.RepoName || decoded.TotalCommits != 10 || len(decoded.Implementers) != 2 {
		t.Errorf("decoded JSON mismatch: %+v", decoded)
	}

	// 2. Terminal output validation (without color and with color)
	var plainBuf, colorBuf bytes.Buffer
	if err := RenderTerminal(&plainBuf, rep, false); err != nil {
		t.Fatalf("RenderTerminal(color=false) failed: %v", err)
	}
	plainOut := plainBuf.String()
	if strings.Contains(plainOut, "\x1b[") {
		t.Errorf("RenderTerminal(color=false) emitted ANSI escape codes:\n%s", plainOut)
	}
	for _, mustContain := range []string{
		"Total Commits: 10",
		"Changed LOC: 500",
		"█",
		"░",
		"Top Implementer Models",
		"Top Planner Models",
		"Top Orchestrator Models",
		"Top Reviewer Models",
		"Top Agent Harnesses",
		"openai/o3",
		"pi@1.0.0",
	} {
		if !strings.Contains(plainOut, mustContain) {
			t.Errorf("plain terminal output missing %q:\n%s", mustContain, plainOut)
		}
	}

	if err := RenderTerminal(&colorBuf, rep, true); err != nil {
		t.Fatalf("RenderTerminal(color=true) failed: %v", err)
	}
	colorOut := colorBuf.String()
	if !strings.Contains(colorOut, "\x1b[") {
		t.Errorf("RenderTerminal(color=true) did not emit ANSI escape codes")
	}
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	stripped := ansiRe.ReplaceAllString(colorOut, "")
	if stripped != plainOut {
		t.Errorf("color output stripped of ANSI differs from plain output!\n--- stripped ---\n%s\n--- plain ---\n%s", stripped, plainOut)
	}

	// 3. SVG rendering & WriteBadgeSVG strict XML check (including XML special characters)
	outPath := filepath.Join(t.TempDir(), "badges", "nested", "ai-provenance.svg")
	if err := WriteBadgeSVG(outPath, rep); err != nil {
		t.Fatalf("WriteBadgeSVG failed: %v", err)
	}
	svgBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read written SVG: %v", err)
	}
	validateStrictSVG(t, svgBytes)

	svgStr := string(svgBytes)
	for _, themeToken := range []string{"#0d1117", "#161b22", "#30363d", "Top Implementer Models", "Top Planner Models"} {
		if !strings.Contains(svgStr, themeToken) {
			t.Errorf("SVG missing expected token %q", themeToken)
		}
	}
}

func TestHasProvenanceAuthor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		author string
		want   bool
	}{
		// New "& N more" multi-model format
		{"Alice by gpt-6-astra & 1 more", true},
		{"Alice by gpt-6-astra & 2 more", true},
		{"Alice by gpt-6-astra & 12 more", true},
		{"  Alice Chen by claude-sonnet-4 & 1 more  ", true},
		{"by gpt-6-astra & 1 more", true},
		{"by gpt-6-astra & 2 more", true},
		{"Alice & Bob by gpt-6-astra & 1 more", true},

		// Single-model format
		{"Alice by gpt-6-astra", true},
		{"by gpt-6-astra", true},
		{"Shelby Miller by deepseek-r1", true},

		// Legacy "+N" multi-model format
		{"Alice by gpt-6-astra+1", true},
		{"Alice by gpt-6-astra+2", true},
		{"by gpt-6-astra+1", true},

		// Non-AI / invalid suffixes
		{"", false},
		{"   ", false},
		{"Shelby Miller", false},
		{"Bobby", false},
		{"Alice by ", false},
		{"by ", false},
		{"Alice by & 1 more", false},
		{"by & 1 more", false},
		{"Alice by gpt-6-astra & 0 more", false},
		{"Alice by gpt-6-astra & 01 more", false},
		{"Alice by gpt-6-astra & -1 more", false},
		{"Alice by gpt-6-astra & +1 more", false},
		{"Alice by gpt-6-astra & abc more", false},
		{"Alice by bad model & 1 more", false},
		{"Alice by gpt-6-astra  & 1 more", false},
	}

	for _, tc := range cases {
		if got := hasProvenanceAuthor(tc.author); got != tc.want {
			t.Errorf("hasProvenanceAuthor(%q) = %v, want %v", tc.author, got, tc.want)
		}
		parsed := parseProvenance(tc.author, "chore: author-only commit without trailers\n")
		if parsed.isAI != tc.want {
			t.Errorf("parseProvenance(%q, body).isAI = %v, want %v", tc.author, parsed.isAI, tc.want)
		}
	}
}

func TestCollect_AuthorOnlyMultiModelFormats(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "multi-author-repo")
	initTempRepo(t, repoDir)

	writeTestFile(t, filepath.Join(repoDir, "a.txt"), []byte("1\n"))
	commitWithEnv(t, repoDir, "Alice by gpt-6-astra & 1 more", "2026-03-01T10:00:00Z", "feat: two implementers")

	writeTestFile(t, filepath.Join(repoDir, "b.txt"), []byte("2\n"))
	commitWithEnv(t, repoDir, "Alice by gpt-6-astra & 2 more", "2026-03-02T10:00:00Z", "feat: three implementers")

	writeTestFile(t, filepath.Join(repoDir, "c.txt"), []byte("3\n"))
	commitWithEnv(t, repoDir, "Alice by gpt-6-astra+1", "2026-03-03T10:00:00Z", "feat: legacy plus-one format")

	writeTestFile(t, filepath.Join(repoDir, "d.txt"), []byte("4\n"))
	commitWithEnv(t, repoDir, "Shelby Miller", "2026-03-04T10:00:00Z", "docs: human commit")

	rep, err := Collect(repoDir, Options{})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if rep.TotalCommits != 4 || rep.AI.Commits != 3 || rep.Human.Commits != 1 {
		t.Fatalf("Total=%d AI=%d Human=%d, want Total=4 AI=3 Human=1", rep.TotalCommits, rep.AI.Commits, rep.Human.Commits)
	}
}
