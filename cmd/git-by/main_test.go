package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/XYZ1024-alt/git-by/internal/setup"
	"github.com/XYZ1024-alt/git-by/internal/stats"
)

var (
	buildOnce sync.Once
	builtBin  string
	builtDir  string
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		_ = os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

// ensureTestBinary compiles ./cmd/git-by once into a temporary directory so
// real `git by ...` subcommand dispatch and `git commit` hook invocations can
// be exercised end-to-end alongside in-process App.Run tests.
func ensureTestBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "git-by-e2e-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		builtDir = dir
		binName := "git-by"
		outPath := filepath.Join(dir, binName)
		pkgDir, err := os.Getwd()
		if err != nil {
			buildErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", outPath, pkgDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &cmdBuildError{err: err, out: string(out)}
			return
		}
		builtBin = outPath
	})
	if buildErr != nil {
		t.Fatalf("failed to build git-by test binary: %v", buildErr)
	}
	return builtBin
}

type cmdBuildError struct {
	err error
	out string
}

func (e *cmdBuildError) Error() string {
	return e.err.Error() + "\n" + e.out
}

type testEnv struct {
	t         *testing.T
	rootDir   string
	repoDir   string
	homeDir   string
	globalCfg string
	binDir    string
}

func newTestRepo(t *testing.T) *testEnv {
	t.Helper()
	binPath := ensureTestBinary(t)
	binDir := filepath.Dir(binPath)

	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	globalCfg := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(globalCfg, []byte(""), 0o644); err != nil {
		t.Fatalf("create global gitconfig: %v", err)
	}

	env := &testEnv{
		t:         t,
		rootDir:   root,
		repoDir:   repoDir,
		homeDir:   homeDir,
		globalCfg: globalCfg,
		binDir:    binDir,
	}

	env.mustGit(repoDir, nil, "init", "-b", "main")
	env.mustGit(repoDir, nil, "config", "user.name", "Alice Lovelace")
	env.mustGit(repoDir, nil, "config", "user.email", "alice@example.com")
	return env
}

func (e *testEnv) applyProcessEnv(extra map[string]string) func(string) (string, bool) {
	e.t.Helper()
	origPath := os.Getenv("PATH")
	newPath := e.binDir + string(os.PathListSeparator) + origPath

	e.t.Setenv("PATH", newPath)
	e.t.Setenv("HOME", e.homeDir)
	e.t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	e.t.Setenv("GIT_CONFIG_GLOBAL", e.globalCfg)
	e.t.Setenv("GIT_CEILING_DIRECTORIES", e.rootDir)
	// Clear any ambient GIT_BY_* / GIT_AUTHOR_* / GIT_COMMITTER_* / NO_COLOR in host shell.
	for _, k := range []string{
		"GIT_BY_ORCHESTRATOR",
		"GIT_BY_PLANNER",
		"GIT_BY_IMPLEMENTER",
		"GIT_BY_REVIEWER",
		"GIT_BY_COMMITTER",
		"GIT_BY_HARNESS",
		"GIT_BY_TOOLS",
		"GIT_BY_ACTIVE",
		"GIT_AUTHOR_NAME",
		"GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME",
		"GIT_COMMITTER_EMAIL",
		"NO_COLOR",
		"DSH_HOME",
		"DSH_AGENTS_HOME",
		"PI_CODING_AGENT_DIR",
		"OMP_CODING_AGENT_DIR",
		"CODEX_HOME",
		"COPILOT_HOME",
		"CLINE_DIR",
		"KIMI_CODE_HOME",
		"HERMES_HOME",
	} {
		if _, exists := extra[k]; !exists {
			e.t.Setenv(k, "")
			_ = os.Unsetenv(k)
		}
	}
	for k, v := range extra {
		e.t.Setenv(k, v)
	}
	return os.LookupEnv
}

func (e *testEnv) runApp(workDir string, extraEnv map[string]string, args ...string) (int, string, string) {
	e.t.Helper()
	lookup := e.applyProcessEnv(extraEnv)
	var stdout, stderr bytes.Buffer
	app := &App{
		stdin:     strings.NewReader(""),
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookup,
		workDir:   workDir,
	}
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

func (e *testEnv) mustRunApp(workDir string, extraEnv map[string]string, args ...string) string {
	e.t.Helper()
	code, out, errOut := e.runApp(workDir, extraEnv, args...)
	if code != 0 {
		e.t.Fatalf("git-by %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
	}
	return out
}

func (e *testEnv) gitCmd(workDir string, extraEnv map[string]string, args ...string) (int, string, string) {
	e.t.Helper()
	_ = e.applyProcessEnv(extraEnv)
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			e.t.Fatalf("failed to spawn git %v: %v", args, err)
		}
	}
	return code, stdout.String(), stderr.String()
}

func (e *testEnv) mustGit(workDir string, extraEnv map[string]string, args ...string) string {
	e.t.Helper()
	code, out, errOut := e.gitCmd(workDir, extraEnv, args...)
	if code != 0 {
		e.t.Fatalf("git %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
	}
	return out
}

func (e *testEnv) writeFile(dir, rel, content string) {
	e.t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		e.t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		e.t.Fatalf("write %s: %v", full, err)
	}
}

func TestMultiRoleRelayStatusCommitAndDenoising(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	// Stage multi-agent relay across all role aliases + tools + harness.
	env.mustRunApp(repo, nil, "set", "orch", "openai/o3-pro", "--harness", "pi@1.0.0")
	env.mustRunApp(repo, nil, "set", "plan", "google/gemini-2.5-pro")
	env.mustRunApp(repo, nil, "set", "impl", "openai/gpt-6-astra", "--tool", "golangci-lint")
	env.mustRunApp(repo, nil, "set", "code", "anthropic/claude-sonnet-4", "--tool=go-test")
	env.mustRunApp(repo, nil, "set", "rev", "anthropic/claude-opus-4")
	env.mustRunApp(repo, nil, "set", "commit", "anthropic/claude-haiku-4")

	// Verify colored status panel (default) vs NO_COLOR=1.
	coloredStatus := env.mustRunApp(repo, map[string]string{"TERM": "xterm-256color"}, "status")
	if !strings.Contains(coloredStatus, "\x1b[") {
		t.Fatalf("expected ANSI escape codes in default status output, got:\n%s", coloredStatus)
	}

	plainStatus := env.mustRunApp(repo, map[string]string{"NO_COLOR": "1"}, "status")
	if strings.Contains(plainStatus, "\x1b[") {
		t.Fatalf("expected zero ANSI escape codes when NO_COLOR=1, got:\n%s", plainStatus)
	}
	for _, want := range []string{
		"Alice Lovelace by gpt-6-astra & 1 more <alice@example.com>",
		"Alice Lovelace <alice@example.com> (untouched)",
		"Assisted-by: pi:gpt-6-astra [golangci-lint] [go-test]",
		"X-Agent-Orchestrator: openai/o3-pro",
		"X-Agent-Planner: google/gemini-2.5-pro",
		"X-Agent-Implementer: openai/gpt-6-astra, anthropic/claude-sonnet-4",
		"X-Agent-Reviewer: anthropic/claude-opus-4",
		"X-Agent-Committer: anthropic/claude-haiku-4",
		"X-Agent-Harness: pi@1.0.0",
	} {
		if !strings.Contains(plainStatus, want) {
			t.Fatalf("status output missing %q:\n%s", want, plainStatus)
		}
	}

	// Stage a file and commit via real `git by commit` subcommand dispatch too!
	env.writeFile(repo, "relay.go", "package relay\n")
	env.mustGit(repo, nil, "add", "relay.go")
	env.mustGit(repo, map[string]string{
		"GIT_COMMITTER_NAME":  "Alice Lovelace",
		"GIT_COMMITTER_EMAIL": "alice@example.com",
	}, "by", "commit", "-m", "feat: multi-agent relay")

	// Session file must be cleared after commit.
	sessionFile := strings.TrimSpace(env.mustGit(repo, nil, "rev-parse", "--git-path", "by-session.json"))
	if !filepath.IsAbs(sessionFile) {
		sessionFile = filepath.Join(repo, sessionFile)
	}
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("expected by-session.json to be deleted after successful commit, got err=%v", err)
	}

	// Verify Author vs untouched Committer.
	identLine := strings.TrimSpace(env.mustGit(repo, nil, "log", "-1", "--format=%an|%ae|%cn|%ce"))
	wantIdent := "Alice Lovelace by gpt-6-astra & 1 more|alice@example.com|Alice Lovelace|alice@example.com"
	if identLine != wantIdent {
		t.Fatalf("commit identity = %q, want %q", identLine, wantIdent)
	}

	// Verify exact trailer ordering and absence of Co-authored-by / Signed-off-by.
	body := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	if strings.Contains(body, "Co-authored-by:") || strings.Contains(body, "Signed-off-by:") {
		t.Fatalf("unexpected Co-authored-by or Signed-off-by in commit body:\n%s", body)
	}

	expectedOrder := []string{
		"Assisted-by: pi:gpt-6-astra [golangci-lint] [go-test]",
		"X-Agent-Orchestrator: openai/o3-pro",
		"X-Agent-Planner: google/gemini-2.5-pro",
		"X-Agent-Implementer: openai/gpt-6-astra, anthropic/claude-sonnet-4",
		"X-Agent-Reviewer: anthropic/claude-opus-4",
		"X-Agent-Committer: anthropic/claude-haiku-4",
		"X-Agent-Harness: pi@1.0.0",
	}
	lastIdx := -1
	for _, line := range expectedOrder {
		idx := strings.Index(body, line)
		if idx == -1 {
			t.Fatalf("commit body missing trailer %q:\n%s", line, body)
		}
		if idx <= lastIdx {
			t.Fatalf("trailer %q appeared out of order in commit body:\n%s", line, body)
		}
		lastIdx = idx
	}

	// Test --replace and smart X-Agent-Committer denoising when Committer == sole Implementer.
	env.mustRunApp(repo, nil, "set", "impl", "openai/old-model")
	env.mustRunApp(repo, nil, "set", "--replace", "impl", "anthropic/claude-sonnet-4")
	env.mustRunApp(repo, nil, "set", "commit", "anthropic/claude-sonnet-4")

	env.writeFile(repo, "denoise.go", "package relay\n\nconst X = 1\n")
	env.mustGit(repo, nil, "add", "denoise.go")
	env.mustRunApp(repo, nil, "commit", "-m", "feat: denoised sole implementer")

	body2 := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	if !strings.Contains(body2, "Assisted-by: claude-sonnet-4") {
		t.Fatalf("expected Assisted-by: claude-sonnet-4, got:\n%s", body2)
	}
	if !strings.Contains(body2, "X-Agent-Implementer: anthropic/claude-sonnet-4") {
		t.Fatalf("expected sole X-Agent-Implementer, got:\n%s", body2)
	}
	if strings.Contains(body2, "openai/old-model") {
		t.Fatalf("--replace failed to overwrite previous implementer:\n%s", body2)
	}
	if strings.Contains(body2, "X-Agent-Committer:") {
		t.Fatalf("expected X-Agent-Committer to be denoised when identical to sole Implementer:\n%s", body2)
	}

	// Test `git by clear`.
	env.mustRunApp(repo, nil, "set", "plan", "openai/o3")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("expected session file after set: %v", err)
	}
	env.mustRunApp(repo, nil, "clear")
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("expected session file removed after clear, got err=%v", err)
	}
}

func TestStatelessFlagsEnvPrecedenceAndNativePassthrough(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	// Initial commit so -am and --amend have tracked history.
	env.writeFile(repo, "a.txt", "v1\n")
	env.writeFile(repo, "b.txt", "v1\n")
	env.mustGit(repo, nil, "add", "a.txt", "b.txt")
	env.mustGit(repo, nil, "commit", "-m", "chore: initial human commit")

	// 1) Merge precedence: by-session.json < GIT_BY_* env < CLI flags, plus `-am` passthrough.
	env.mustRunApp(repo, nil, "set", "orch", "session/orch-model", "--tool", "session-tool")
	env.mustRunApp(repo, nil, "set", "plan", "session/plan-model")
	env.mustRunApp(repo, nil, "set", "impl", "session/impl-model")

	env.writeFile(repo, "a.txt", "v2\n")
	extraEnv := map[string]string{
		"GIT_BY_PLANNER":     "env/plan-model",
		"GIT_BY_IMPLEMENTER": "env/impl-model",
		"GIT_BY_REVIEWER":    "env/rev-model",
		"GIT_BY_HARNESS":     "cursor@0.49.0",
		"GIT_BY_TOOLS":       "env-tool",
	}
	// Note `-m` value intentionally mentions `--impl` to ensure flag stripping ignores `-m` arg!
	env.mustRunApp(repo, extraEnv,
		"commit",
		"--impl", "cli/impl-winner",
		"--tool=cli-tool",
		"-am", "feat: preserve --impl text inside commit message",
	)

	body := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	for _, want := range []string{
		"feat: preserve --impl text inside commit message",
		"Assisted-by: cursor:impl-winner [session-tool] [env-tool] [cli-tool]",
		"X-Agent-Orchestrator: session/orch-model",
		"X-Agent-Planner: env/plan-model",
		"X-Agent-Implementer: cli/impl-winner",
		"X-Agent-Reviewer: env/rev-model",
		"X-Agent-Harness: cursor@0.49.0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in merged commit body:\n%s", want, body)
		}
	}

	// 2) `--amend` passthrough.
	env.mustRunApp(repo, nil,
		"commit",
		"--impl=google/gemini-2.5-pro",
		"--amend",
		"-m", "feat: amended via git by commit",
	)
	amendedBody := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	if !strings.Contains(amendedBody, "feat: amended via git by commit") ||
		!strings.Contains(amendedBody, "X-Agent-Implementer: google/gemini-2.5-pro") {
		t.Fatalf("unexpected amended commit body:\n%s", amendedBody)
	}

	// 3) `-- <file>` pathspec passthrough (only commit a.txt while b.txt stays dirty).
	env.writeFile(repo, "a.txt", "v3\n")
	env.writeFile(repo, "b.txt", "v3\n")
	env.mustGit(repo, nil, "add", "a.txt", "b.txt")
	env.mustRunApp(repo, nil,
		"commit",
		"--impl", "openai/o3",
		"-m", "feat: pathspec a.txt only",
		"--", "a.txt",
	)
	changed := strings.TrimSpace(env.mustGit(repo, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"))
	if changed != "a.txt" {
		t.Fatalf("expected only a.txt in commit, got %q", changed)
	}
	statusPorcelain := strings.TrimSpace(env.mustGit(repo, nil, "status", "--porcelain"))
	if !strings.Contains(statusPorcelain, "b.txt") {
		t.Fatalf("expected b.txt to remain staged/uncommitted, got status %q", statusPorcelain)
	}
}

func TestWorktreeConcurrentSessionIsolation(t *testing.T) {
	env := newTestRepo(t)
	mainWT := env.repoDir

	env.writeFile(mainWT, "README.txt", "seed\n")
	env.mustGit(mainWT, nil, "add", "README.txt")
	env.mustGit(mainWT, nil, "commit", "-m", "chore: seed")

	linkedWT := filepath.Join(env.rootDir, "wt-feature")
	env.mustGit(mainWT, nil, "worktree", "add", linkedWT, "-b", "feature/isolated")

	// Stage completely distinct models concurrently in mainWT and linkedWT.
	env.mustRunApp(mainWT, nil, "set", "impl", "anthropic/claude-sonnet-4", "--harness", "claude-code@1.0.0")
	env.mustRunApp(linkedWT, nil, "set", "impl", "google/gemini-2.5-pro", "--harness", "antigravity@2.0.0")

	mainStatus := env.mustRunApp(mainWT, map[string]string{"NO_COLOR": "1"}, "status")
	linkedStatus := env.mustRunApp(linkedWT, map[string]string{"NO_COLOR": "1"}, "status")

	if !strings.Contains(mainStatus, "anthropic/claude-sonnet-4") || strings.Contains(mainStatus, "google/gemini-2.5-pro") {
		t.Fatalf("main worktree status leaked or missing model:\n%s", mainStatus)
	}
	if !strings.Contains(linkedStatus, "google/gemini-2.5-pro") || strings.Contains(linkedStatus, "anthropic/claude-sonnet-4") {
		t.Fatalf("linked worktree status leaked or missing model:\n%s", linkedStatus)
	}

	// Commit in linkedWT first; mainWT session MUST stay untouched!
	env.writeFile(linkedWT, "feature.txt", "linked worktree\n")
	env.mustGit(linkedWT, nil, "add", "feature.txt")
	env.mustRunApp(linkedWT, nil, "commit", "-m", "feat: linked worktree commit")

	mainStatusAfter := env.mustRunApp(mainWT, map[string]string{"NO_COLOR": "1"}, "status")
	if !strings.Contains(mainStatusAfter, "anthropic/claude-sonnet-4") {
		t.Fatalf("committing in linked worktree unexpectedly cleared main worktree session:\n%s", mainStatusAfter)
	}

	// Now commit in mainWT and verify both commits recorded isolated authors & trailers.
	env.writeFile(mainWT, "main.txt", "main worktree\n")
	env.mustGit(mainWT, nil, "add", "main.txt")
	env.mustRunApp(mainWT, nil, "commit", "-m", "feat: main worktree commit")

	linkedAuthor := strings.TrimSpace(env.mustGit(linkedWT, nil, "log", "-1", "--format=%an"))
	mainAuthor := strings.TrimSpace(env.mustGit(mainWT, nil, "log", "-1", "--format=%an"))
	if linkedAuthor != "Alice Lovelace by gemini-2.5-pro" {
		t.Fatalf("linked worktree author = %q", linkedAuthor)
	}
	if mainAuthor != "Alice Lovelace by claude-sonnet-4" {
		t.Fatalf("main worktree author = %q", mainAuthor)
	}
}

func TestPreCommitFailurePreservesSessionAndPrepareCommitMsgChaining(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}

	// Failing pre-commit hook (exits 23) + custom prepare-commit-msg hook that prefixes "[TICKET-7] ".
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommitPath, []byte("#!/bin/sh\necho 'lint failed' >&2\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write pre-commit: %v", err)
	}
	prepMsgPath := filepath.Join(hooksDir, "prepare-commit-msg")
	prepScript := "#!/bin/sh\nTMP=\"$1.tmp\"\nprintf '[TICKET-7] ' > \"$TMP\"\ncat \"$1\" >> \"$TMP\"\nmv \"$TMP\" \"$1\"\n"
	if err := os.WriteFile(prepMsgPath, []byte(prepScript), 0o755); err != nil {
		t.Fatalf("write prepare-commit-msg: %v", err)
	}

	env.mustRunApp(repo, nil, "set", "impl", "anthropic/claude-sonnet-4")
	env.mustRunApp(repo, nil, "set", "rev", "openai/o3")

	env.writeFile(repo, "app.go", "package app\n")
	env.mustGit(repo, nil, "add", "app.go")

	code, _, errOut := env.runApp(repo, nil, "commit", "-m", "feat: guarded change")
	if code != 1 {
		t.Fatalf("expected failing pre-commit exit code 1 from git commit, got %d (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "lint failed") {
		t.Fatalf("expected pre-commit stderr 'lint failed', got %q", errOut)
	}

	// Also verify native git option error exit code (129) is forwarded verbatim while preserving session.
	badFlagCode, _, _ := env.runApp(repo, nil, "commit", "--unknown-native-git-flag-xyz")
	if badFlagCode != 129 {
		t.Fatalf("expected git commit usage exit code 129, got %d", badFlagCode)
	}

	sessionFile := filepath.Join(repo, ".git", "by-session.json")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("expected by-session.json to be preserved on failed pre-commit: %v", err)
	}

	// Fix pre-commit hook and retry `git by commit` without re-staging provenance.
	if err := os.WriteFile(preCommitPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("fix pre-commit: %v", err)
	}
	env.mustRunApp(repo, nil, "commit", "-m", "feat: guarded change")

	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("expected by-session.json cleared after retry succeeded, got err=%v", err)
	}

	body := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	if !strings.HasPrefix(body, "[TICKET-7] feat: guarded change") {
		t.Fatalf("expected original prepare-commit-msg prefix '[TICKET-7] ', got:\n%s", body)
	}
	if !strings.Contains(body, "Assisted-by: claude-sonnet-4") ||
		!strings.Contains(body, "X-Agent-Reviewer: openai/o3") {
		t.Fatalf("expected AI trailers after chained prepare-commit-msg, got:\n%s", body)
	}
}

func TestHookInstallUninstallAndNativeGitCommit(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	// Pre-populate an existing custom prepare-commit-msg hook to verify backup/chaining/restore.
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	customPrep := filepath.Join(hooksDir, "prepare-commit-msg")
	customScript := "#!/bin/sh\nTMP=\"$1.origtmp\"\nprintf '[ORIG-HOOK] ' > \"$TMP\"\ncat \"$1\" >> \"$TMP\"\nmv \"$TMP\" \"$1\"\n"
	if err := os.WriteFile(customPrep, []byte(customScript), 0o755); err != nil {
		t.Fatalf("write custom prepare-commit-msg: %v", err)
	}

	// Install local git-by hooks.
	env.mustRunApp(repo, nil, "hook", "install")

	// Stage AI provenance and commit using NATIVE `git commit`!
	env.mustRunApp(repo, nil, "set", "impl", "openai/gpt-5", "--harness", "codex@1.1.0")
	env.writeFile(repo, "native.go", "package native\n")
	env.mustGit(repo, nil, "add", "native.go")
	env.mustGit(repo, nil, "commit", "-m", "feat: native commit with sense-free hook")

	body := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	for _, want := range []string{
		"[ORIG-HOOK] feat: native commit with sense-free hook",
		"Assisted-by: codex:gpt-5",
		"X-Agent-Implementer: openai/gpt-5",
		"X-Agent-Harness: codex@1.1.0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("native commit missing %q:\n%s", want, body)
		}
	}

	// Verify post-commit hook cleared by-session.json automatically!
	sessionFile := filepath.Join(repo, ".git", "by-session.json")
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("expected post-commit hook to clear by-session.json, got err=%v", err)
	}

	// Verify `git by commit` while hooks are installed does NOT duplicate trailers (GIT_BY_ACTIVE=1).
	env.writeFile(repo, "combo.go", "package native\nconst C = 2\n")
	env.mustGit(repo, nil, "add", "combo.go")
	env.mustRunApp(repo, nil, "commit", "--impl", "anthropic/claude-sonnet-4", "-m", "feat: git-by commit with installed hooks")
	comboBody := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	if strings.Count(comboBody, "Assisted-by:") != 1 {
		t.Fatalf("expected exactly 1 Assisted-by trailer, got:\n%s", comboBody)
	}

	// Uninstall local hooks and verify original custom hook is restored intact.
	env.mustRunApp(repo, nil, "hook", "uninstall")
	env.mustRunApp(repo, nil, "set", "impl", "openai/gpt-5")
	env.writeFile(repo, "after_uninstall.go", "package native\nconst D = 3\n")
	env.mustGit(repo, nil, "add", "after_uninstall.go")
	env.mustGit(repo, nil, "commit", "-m", "chore: after uninstall")

	afterBody := env.mustGit(repo, nil, "log", "-1", "--format=%B")
	if !strings.HasPrefix(afterBody, "[ORIG-HOOK] chore: after uninstall") {
		t.Fatalf("expected restored custom prepare-commit-msg hook to run, got:\n%s", afterBody)
	}
	if strings.Contains(afterBody, "Assisted-by:") {
		t.Fatalf("expected no git-by trailers after hook uninstall, got:\n%s", afterBody)
	}

	// Test --global hook install / uninstall inside isolated HOME/GIT_CONFIG_GLOBAL.
	installGlobalOut := env.mustRunApp(repo, nil, "hook", "install", "--global")
	if !strings.Contains(installGlobalOut, filepath.Join(env.homeDir, ".config", "git", "hooks")) {
		t.Fatalf("unexpected global install output: %s", installGlobalOut)
	}
	uninstallGlobalOut := env.mustRunApp(repo, nil, "hook", "uninstall", "--global")
	if !strings.Contains(uninstallGlobalOut, filepath.Join(env.homeDir, ".config", "git", "hooks")) {
		t.Fatalf("unexpected global uninstall output: %s", uninstallGlobalOut)
	}
}

func TestStatsJSONTerminalBadgeAndInitSkill(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	// Commit 1: pure human commit (3 lines added).
	env.writeFile(repo, "human.txt", "h1\nh2\nh3\n")
	env.mustGit(repo, nil, "add", "human.txt")
	env.mustGit(repo, nil, "commit", "-m", "docs: human notes")

	// Commit 2: AI multi-role commit (4 lines added).
	env.writeFile(repo, "ai1.go", "package ai\n\nfunc A() int {\n\treturn 42\n}\n")
	env.mustGit(repo, nil, "add", "ai1.go")
	env.mustRunApp(repo, nil,
		"commit",
		"--plan", "openai/o3",
		"--impl", "anthropic/claude-sonnet-4",
		"--harness", "claude-code@1.0.0",
		"-m", "feat: ai module A",
	)

	// Commit 3: second AI commit (2 lines added).
	env.writeFile(repo, "ai2.go", "package ai\nvar B = 7\n")
	env.mustGit(repo, nil, "add", "ai2.go")
	env.mustRunApp(repo, nil,
		"commit",
		"--impl", "openai/gpt-5",
		"-m", "feat: ai module B",
	)

	// 1) `git by stats --json`
	jsonOut := env.mustRunApp(repo, nil, "stats", "--json", "--top", "3")
	var report stats.Report
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("failed to decode stats --json output: %v\nraw:\n%s", err, jsonOut)
	}
	if report.TotalCommits != 3 || report.Human.Commits != 1 || report.AI.Commits != 2 {
		t.Fatalf("unexpected commit counts in stats report: %+v", report)
	}
	if len(report.Implementers) != 2 || len(report.Planners) != 1 {
		t.Fatalf("unexpected role leaderboards in stats report: %+v", report)
	}

	// 2) `git by stats` terminal + `--badge` SVG generation + XML validity.
	badgeRel := filepath.Join("badges", "ai-provenance.svg")
	termOut := env.mustRunApp(repo, map[string]string{"NO_COLOR": "1"}, "stats", "--badge", badgeRel)
	if strings.Contains(termOut, "\x1b[") {
		t.Fatalf("expected NO_COLOR=1 stats output to have no ANSI codes:\n%s", termOut)
	}
	if !strings.Contains(termOut, "claude-sonnet-4") && !strings.Contains(termOut, "anthropic/claude-sonnet-4") {
		t.Fatalf("expected implementer model in terminal stats output:\n%s", termOut)
	}

	badgeFull := filepath.Join(repo, badgeRel)
	svgBytes, err := os.ReadFile(badgeFull)
	if err != nil {
		t.Fatalf("expected SVG badge file at %s: %v", badgeFull, err)
	}
	if !strings.Contains(string(svgBytes), "<svg") {
		t.Fatalf("badge file missing <svg> element:\n%s", string(svgBytes))
	}
	dec := xml.NewDecoder(bytes.NewReader(svgBytes))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("generated SVG badge is not well-formed XML: %v\nsvg:\n%s", err, string(svgBytes))
		}
	}

	// 3) `git by init-skill` and alias `git by prompt`, plus `version` and `help`.
	skillOut := env.mustRunApp(repo, nil, "init-skill")
	promptOut := env.mustRunApp(repo, nil, "prompt")
	if skillOut != promptOut {
		t.Fatalf("expected 'init-skill' and 'prompt' outputs to match")
	}
	wantUnified := setup.RenderManagedBlock("") + "\n"
	if skillOut != wantUnified {
		t.Fatalf("init-skill output did not match setup.RenderManagedBlock(\"\")+\\n:\ngot:\n%s\nwant:\n%s", skillOut, wantUnified)
	}
	for _, want := range []string{
		"<!-- git-by:start -->",
		"<!-- git-by:end -->",
		"<your-harness-name>",
		"git by set",
		"git by commit",
	} {
		if !strings.Contains(skillOut, want) {
			t.Fatalf("init-skill output missing %q:\n%s", want, skillOut)
		}
	}

	verOut := env.mustRunApp(repo, nil, "version")
	if !strings.Contains(verOut, version) {
		t.Fatalf("unexpected version output: %q", verOut)
	}
	helpOut := env.mustRunApp(repo, nil, "--help")
	if !strings.Contains(helpOut, "Workflow Commands:") || !strings.Contains(helpOut, "setup [flags]") {
		t.Fatalf("unexpected help output: %q", helpOut)
	}
}

func TestSetupGlobalAutoDetectDryRunIdempotencyAndLosslessUninstall(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	// Use an isolated virtual HOME passed via lookupEnv/extraEnv so real $HOME is never touched.
	virtHome := filepath.Join(env.rootDir, "virtual-home")
	if err := os.MkdirAll(filepath.Join(virtHome, ".gemini"), 0o755); err != nil {
		t.Fatalf("mkdir .gemini: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(virtHome, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	origPiContent := "# Personal Pi System Rules\n\n- Prefer pure stdlib Go.\n- Always run tests with -race.\n"
	piRulesPath := filepath.Join(virtHome, ".pi", "agent", "APPEND_SYSTEM.md")
	if err := os.MkdirAll(filepath.Dir(piRulesPath), 0o755); err != nil {
		t.Fatalf("mkdir .pi/agent: %v", err)
	}
	if err := os.WriteFile(piRulesPath, []byte(origPiContent), 0o644); err != nil {
		t.Fatalf("write APPEND_SYSTEM.md: %v", err)
	}
	// Note: .codex and .config/opencode are intentionally absent!

	homeEnv := map[string]string{"HOME": virtHome}
	agyRulesPath := filepath.Join(virtHome, ".gemini", "config", "rules", "git-by.md")
	claudeRulesPath := filepath.Join(virtHome, ".claude", "rules", "git-by.md")
	codexDir := filepath.Join(virtHome, ".codex")
	opencodeDir := filepath.Join(virtHome, ".config", "opencode")

	// 1) --dry-run + --no-color must preview actions and write zero files/dirs.
	dryOut := env.mustRunApp(repo, homeEnv, "setup", "--dry-run", "--no-color")
	if strings.Contains(dryOut, "\x1b[") {
		t.Fatalf("expected no ANSI escape codes with --no-color, got:\n%s", dryOut)
	}
	for _, want := range []string{"Antigravity", "Pi Agent", "Claude Code", "Codex", "OpenCode"} {
		if !strings.Contains(dryOut, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, dryOut)
		}
	}
	for _, mustNotExist := range []string{agyRulesPath, claudeRulesPath, codexDir, opencodeDir} {
		if _, err := os.Stat(mustNotExist); !os.IsNotExist(err) {
			t.Fatalf("dry-run unexpectedly created %s (err=%v)", mustNotExist, err)
		}
	}
	afterDryPi, err := os.ReadFile(piRulesPath)
	if err != nil {
		t.Fatalf("read APPEND_SYSTEM.md after dry-run: %v", err)
	}
	if !bytes.Equal(afterDryPi, []byte(origPiContent)) {
		t.Fatalf("dry-run mutated APPEND_SYSTEM.md:\ngot:  %q\nwant: %q", string(afterDryPi), origPiContent)
	}

	// 2) Default `git by setup` (global scope) installs/updates detected agents & skips absent ones.
	installOut := env.mustRunApp(repo, homeEnv, "setup")
	for _, want := range []string{"Antigravity", "Pi Agent", "Claude Code", "Codex", "OpenCode"} {
		if !strings.Contains(installOut, want) {
			t.Fatalf("setup output missing %q:\n%s", want, installOut)
		}
	}

	// Absent directories MUST NOT be created.
	for _, absentDir := range []string{codexDir, opencodeDir} {
		if _, err := os.Stat(absentDir); !os.IsNotExist(err) {
			t.Fatalf("setup unexpectedly created uninstalled agent dir %s (err=%v)", absentDir, err)
		}
	}

	agyBytes, err := os.ReadFile(agyRulesPath)
	if err != nil {
		t.Fatalf("expected %s to be created: %v", agyRulesPath, err)
	}
	if !strings.Contains(string(agyBytes), "--harness antigravity") ||
		!strings.Contains(string(agyBytes), setup.MarkerStart) ||
		!strings.Contains(string(agyBytes), setup.MarkerEnd) {
		t.Fatalf("unexpected Antigravity rule content:\n%s", string(agyBytes))
	}

	claudeBytes, err := os.ReadFile(claudeRulesPath)
	if err != nil {
		t.Fatalf("expected %s to be created: %v", claudeRulesPath, err)
	}
	if !strings.Contains(string(claudeBytes), "--harness claude-code") {
		t.Fatalf("unexpected Claude Code rule content:\n%s", string(claudeBytes))
	}

	piBytesAfterFirst, err := os.ReadFile(piRulesPath)
	if err != nil {
		t.Fatalf("read APPEND_SYSTEM.md: %v", err)
	}
	if !strings.HasPrefix(string(piBytesAfterFirst), origPiContent) ||
		!strings.Contains(string(piBytesAfterFirst), "--harness pi") {
		t.Fatalf("APPEND_SYSTEM.md missing original header or pi harness:\n%s", string(piBytesAfterFirst))
	}

	// 3) Second run (`--global`) must be 100% idempotent with zero duplicate markers.
	env.mustRunApp(repo, homeEnv, "setup", "--global")
	for _, path := range []string{agyRulesPath, claudeRulesPath, piRulesPath} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Count(string(b), setup.MarkerStart) != 1 || strings.Count(string(b), setup.MarkerEnd) != 1 {
			t.Fatalf("expected exactly 1 managed block in %s, got:\n%s", path, string(b))
		}
	}
	piBytesAfterSecond, err := os.ReadFile(piRulesPath)
	if err != nil {
		t.Fatalf("read APPEND_SYSTEM.md after second run: %v", err)
	}
	if !bytes.Equal(piBytesAfterFirst, piBytesAfterSecond) {
		t.Fatalf("second setup run was not idempotent on APPEND_SYSTEM.md:\nfirst:\n%s\nsecond:\n%s",
			string(piBytesAfterFirst), string(piBytesAfterSecond))
	}

	// 4) `--agent` filtering + `--remove` alias on explicit global agent (`cursor`).
	env.mustRunApp(repo, homeEnv, "setup", "--global", "--agent", "cursor")
	cursorRulesPath := filepath.Join(virtHome, ".cursorrules")
	cursorBytes, err := os.ReadFile(cursorRulesPath)
	if err != nil {
		t.Fatalf("expected %s after --agent cursor: %v", cursorRulesPath, err)
	}
	if !strings.Contains(string(cursorBytes), "--harness cursor") {
		t.Fatalf("expected --harness cursor in .cursorrules:\n%s", string(cursorBytes))
	}
	if _, err := os.Stat(filepath.Join(virtHome, ".cursor")); !os.IsNotExist(err) {
		t.Fatalf("expected ~/.cursor directory not to be created when absent, got err=%v", err)
	}
	env.mustRunApp(repo, homeEnv, "setup", "--global", "--agent=cursor", "--remove")
	if _, err := os.Stat(cursorRulesPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s deleted after --remove, got err=%v", cursorRulesPath, err)
	}

	// 5) `--uninstall` deletes standalone files and restores APPEND_SYSTEM.md byte-for-byte.
	env.mustGit(repo, homeEnv, "by", "setup", "--uninstall")
	for _, deleted := range []string{agyRulesPath, claudeRulesPath} {
		if _, err := os.Stat(deleted); !os.IsNotExist(err) {
			t.Fatalf("expected standalone rule file %s deleted on --uninstall, got err=%v", deleted, err)
		}
	}
	restoredPiBytes, err := os.ReadFile(piRulesPath)
	if err != nil {
		t.Fatalf("expected APPEND_SYSTEM.md preserved after --uninstall: %v", err)
	}
	if !bytes.Equal(restoredPiBytes, []byte(origPiContent)) {
		t.Fatalf("APPEND_SYSTEM.md was not restored byte-for-byte!\ngot:  %q\nwant: %q",
			string(restoredPiBytes), origPiContent)
	}
}

func TestSetupProjectScopeSubdirAndValidationErrors(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	origClaude := "# Repository Claude Rules\n\nFollow Go style guidelines and run go vet.\n"
	claudePath := filepath.Join(repo, "CLAUDE.md")
	env.writeFile(repo, "CLAUDE.md", origClaude)

	subDir := filepath.Join(repo, "pkg", "nested")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir nested subdir: %v", err)
	}

	// 1) Run `git by setup --project` from a nested subdirectory; verify root resolution.
	projOut := env.mustRunApp(subDir, map[string]string{"NO_COLOR": "1"}, "setup", "--project")
	if !strings.Contains(projOut, "AGENTS.md") || !strings.Contains(projOut, "CLAUDE.md") {
		t.Fatalf("project setup output missing AGENTS.md or CLAUDE.md:\n%s", projOut)
	}
	if _, err := os.Stat(filepath.Join(subDir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("expected AGENTS.md at repo root, not inside nested subdir")
	}

	agentsPath := filepath.Join(repo, "AGENTS.md")
	agentsBytes, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("expected %s to be created: %v", agentsPath, err)
	}
	if !strings.Contains(string(agentsBytes), "<your-harness-name>") ||
		!strings.Contains(string(agentsBytes), setup.MarkerStart) {
		t.Fatalf("unexpected AGENTS.md content:\n%s", string(agentsBytes))
	}

	claudeUpdated, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.HasPrefix(string(claudeUpdated), origClaude) ||
		!strings.Contains(string(claudeUpdated), "--harness claude-code") {
		t.Fatalf("unexpected updated CLAUDE.md content:\n%s", string(claudeUpdated))
	}

	// Absent .cursorrules and .windsurfrules MUST NOT be created on default --project scan.
	for _, absent := range []string{
		filepath.Join(repo, ".cursorrules"),
		filepath.Join(repo, ".windsurfrules"),
	} {
		if _, err := os.Stat(absent); !os.IsNotExist(err) {
			t.Fatalf("expected %s NOT to be created during default --project setup (err=%v)", absent, err)
		}
	}

	// 2) Explicit `--project --agent cursor,windsurf` creates them, and `--remove` deletes them.
	env.mustRunApp(repo, nil, "setup", "--project", "--agent", "cursor,windsurf")
	for _, p := range []string{filepath.Join(repo, ".cursorrules"), filepath.Join(repo, ".windsurfrules")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s created when explicitly requested: %v", p, err)
		}
	}
	env.mustRunApp(repo, nil, "setup", "--project", "--agent", "cursor", "--agent", "windsurf", "--remove")
	for _, p := range []string{filepath.Join(repo, ".cursorrules"), filepath.Join(repo, ".windsurfrules")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s deleted after --remove, got err=%v", p, err)
		}
	}

	// 3) `git by setup --project --uninstall` deletes standalone-empty AGENTS.md & restores CLAUDE.md byte-for-byte.
	env.mustRunApp(repo, nil, "setup", "--project", "--uninstall")
	if _, err := os.Stat(agentsPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s deleted on --project --uninstall, got err=%v", agentsPath, err)
	}
	restoredClaude, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("expected CLAUDE.md preserved after --project --uninstall: %v", err)
	}
	if !bytes.Equal(restoredClaude, []byte(origClaude)) {
		t.Fatalf("CLAUDE.md was not restored byte-for-byte!\ngot:  %q\nwant: %q", string(restoredClaude), origClaude)
	}

	// 4) Help & validation error cases.
	helpSetup := env.mustRunApp(repo, nil, "help", "setup")
	for _, flag := range []string{
		"--global", "--project", "--agent", "dsh", "omp", "gemini", "copilot", "cline",
		"roo", "kilo", "droid", "amp", "goose", "crush", "kiro", "continue",
		"trae", "augment", "qwen", "kimi", "codebuddy", "junie", "zed", "aider",
		"openhands", "hermes", "--yes", "-y", "--uninstall", "--remove", "--dry-run",
	} {
		if !strings.Contains(helpSetup, flag) {
			t.Fatalf("help setup missing %q:\n%s", flag, helpSetup)
		}
	}

	if code, _, errOut := env.runApp(repo, nil, "setup", "--global", "--project"); code != 1 {
		t.Fatalf("expected exit 1 when combining --global and --project, got %d (stderr: %s)", code, errOut)
	}
	if code, _, errOut := env.runApp(repo, nil, "setup", "--agent", "nonexistent-agent"); code != 1 {
		t.Fatalf("expected exit 1 for unknown --agent, got %d (stderr: %s)", code, errOut)
	}
	if code, _, errOut := env.runApp(repo, nil, "setup", "--agent"); code != 1 {
		t.Fatalf("expected exit 1 for missing --agent value, got %d (stderr: %s)", code, errOut)
	}

	nonGitDir := filepath.Join(env.rootDir, "not-a-git-repo")
	if err := os.MkdirAll(nonGitDir, 0o755); err != nil {
		t.Fatalf("mkdir nonGitDir: %v", err)
	}
	if code, _, errOut := env.runApp(nonGitDir, nil, "setup", "--project"); code != 1 {
		t.Fatalf("expected exit 1 for --project outside git repo, got %d (stderr: %s)", code, errOut)
	}
}

func TestSetupInteractiveAppFlowsYesFlagAndCancel(t *testing.T) {
	env := newTestRepo(t)
	repo := env.repoDir

	virtHome := filepath.Join(env.rootDir, "interactive-home")
	if err := os.MkdirAll(filepath.Join(virtHome, ".gemini"), 0o755); err != nil {
		t.Fatalf("mkdir .gemini: %v", err)
	}
	homeEnv := map[string]string{"HOME": virtHome, "NO_COLOR": "1"}

	runInteractive := func(workDir string, input string, args ...string) (int, string, string, int) {
		t.Helper()
		lookup := env.applyProcessEnv(homeEnv)
		var stdout, stderr bytes.Buffer
		restored := 0
		app := &App{
			stdin:     strings.NewReader(input),
			stdout:    &stdout,
			stderr:    &stderr,
			lookupEnv: lookup,
			workDir:   workDir,
			isTTY:     func() bool { return true },
			rawMode: func() (func(), error) {
				return func() { restored++ }, nil
			},
		}
		code := app.Run(args)
		return code, stdout.String(), stderr.String(), restored
	}

	// 1) Ctrl+C exits 130, restores terminal, writes nothing.
	code, out, _, restored := runInteractive(repo, "\x03", "setup")
	if code != 130 || restored != 1 {
		t.Fatalf("Ctrl+C expected code=130 restored=1, got code=%d restored=%d\nout:\n%s", code, restored, out)
	}
	if _, err := os.Stat(filepath.Join(virtHome, ".gemini", "config")); !os.IsNotExist(err) {
		t.Fatalf("Ctrl+C unexpectedly created files")
	}

	// 2) 0 agents selected + Enter prints clean notice, exits 0, writes nothing.
	// Only Antigravity (index 0) is pre-selected -> Space unchecks it -> Enter confirms 0 selected.
	code, out, _, restored = runInteractive(repo, " \r", "setup")
	if code != 0 || restored != 1 {
		t.Fatalf("0-selected expected code=0 restored=1, got code=%d restored=%d", code, restored)
	}
	if !strings.Contains(out, "No agents selected; nothing changed.") {
		t.Fatalf("expected 0-selected message, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(virtHome, ".gemini", "config")); !os.IsNotExist(err) {
		t.Fatalf("0-selected Enter unexpectedly created files")
	}

	// 3) Interactive global install: keep Antigravity checked, move down to OpenCode (index 4: jjjj)
	// and DeepSeek Harness (index 5: j), toggle both with Space, confirm with Enter -> creates both!
	code, out, errOut, restored := runInteractive(repo, "jjjj j \r", "setup")
	if code != 0 || restored != 1 {
		t.Fatalf("interactive install failed: code=%d restored=%d stderr=%s\nout:\n%s", code, restored, errOut, out)
	}
	if _, err := os.Stat(filepath.Join(virtHome, ".gemini", "config", "rules", "git-by.md")); err != nil {
		t.Fatalf("expected Antigravity rule installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(virtHome, ".config", "opencode", "AGENTS.md")); err != nil {
		t.Fatalf("expected OpenCode rule installed in newly created dir: %v", err)
	}
	dshGlobalRule := filepath.Join(virtHome, ".dsh", "AGENTS.md")
	if b, err := os.ReadFile(dshGlobalRule); err != nil || !strings.Contains(string(b), "--harness dsh") {
		t.Fatalf("expected DeepSeek Harness rule installed at %s with --harness dsh: err=%v", dshGlobalRule, err)
	}

	// 4) --yes (-y) on a TTY skips interactive picker completely (even with empty stdin!).
	code, out, errOut, restored = runInteractive(repo, "", "setup", "-y")
	if code != 0 || restored != 0 {
		t.Fatalf("--yes on TTY should not enter raw mode: code=%d restored=%d stderr=%s", code, restored, errOut)
	}
	if !strings.Contains(out, "Configured 3 agent(s).") {
		t.Fatalf("unexpected --yes output:\n%s", out)
	}

	// 5) Interactive --project install & --project --uninstall.
	// In empty repo, AGENTS.md (index 0) is pre-selected; move down to Cursor (.cursorrules, index 2: jj), Space, Enter.
	code, out, errOut, restored = runInteractive(repo, "jj \r", "setup", "--project")
	if code != 0 || restored != 1 {
		t.Fatalf("interactive --project failed: code=%d restored=%d stderr=%s", code, restored, errOut)
	}
	for _, rel := range []string{"AGENTS.md", ".cursorrules"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Fatalf("expected %s created in interactive --project: %v", rel, err)
		}
	}

	// Interactive --project --uninstall pre-selects both configured files -> Enter removes both!
	code, out, errOut, restored = runInteractive(repo, "\r", "setup", "--project", "--uninstall")
	if code != 0 || restored != 1 {
		t.Fatalf("interactive --project --uninstall failed: code=%d restored=%d stderr=%s", code, restored, errOut)
	}
	for _, rel := range []string{"AGENTS.md", ".cursorrules"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed in interactive --project --uninstall, err=%v", rel, err)
		}
	}
}
