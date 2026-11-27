package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type testEnv struct {
	root    string
	home    string
	repoDir string
}

func setupTestRepo(t *testing.T) testEnv {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	// Clear any ambient git env vars that could interfere with tests.
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
	t.Setenv("GIT_BY_ACTIVE", "")

	// Unset empty GIT_AUTHOR_* / GIT_COMMITTER_* so Git falls back to config naturally.
	_ = os.Unsetenv("GIT_AUTHOR_NAME")
	_ = os.Unsetenv("GIT_AUTHOR_EMAIL")
	_ = os.Unsetenv("GIT_COMMITTER_NAME")
	_ = os.Unsetenv("GIT_COMMITTER_EMAIL")
	_ = os.Unsetenv("GIT_BY_ACTIVE")

	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	mustGit(t, repoDir, "init")
	mustGit(t, repoDir, "config", "user.name", "Ada Lovelace")
	mustGit(t, repoDir, "config", "user.email", "ada@analytical.engine")

	return testEnv{
		root:    root,
		home:    home,
		repoDir: repoDir,
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimRight(string(out), "\r\n")
}

func writeFile(t *testing.T, path string, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func TestDiscover_RepoRootSubdirNonRepoAndBare(t *testing.T) {
	env := setupTestRepo(t)

	// 1. Non-repo error with GIT_CEILING_DIRECTORIES
	nonRepo := filepath.Join(env.root, "not-a-repo")
	if err := os.MkdirAll(nonRepo, 0o755); err != nil {
		t.Fatalf("mkdir nonRepo: %v", err)
	}
	if _, err := Discover(nonRepo); err == nil {
		t.Fatalf("expected Discover to fail outside git repo")
	}

	// 2. Discover from repo root
	rRoot, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover(repoDir): %v", err)
	}
	if rRoot.WorkDir != env.repoDir {
		t.Errorf("WorkDir = %q, want %q", rRoot.WorkDir, env.repoDir)
	}
	if rRoot.TopLevel != env.repoDir {
		t.Errorf("TopLevel = %q, want %q", rRoot.TopLevel, env.repoDir)
	}
	wantGitDir := filepath.Join(env.repoDir, ".git")
	if rRoot.GitDir != wantGitDir || rRoot.GitCommonDir != wantGitDir {
		t.Errorf("GitDir=%q GitCommonDir=%q, want both %q", rRoot.GitDir, rRoot.GitCommonDir, wantGitDir)
	}

	// 3. Discover from nested subdirectory
	subDir := filepath.Join(env.repoDir, "pkg", "deep", "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}
	rSub, err := Discover(subDir)
	if err != nil {
		t.Fatalf("Discover(subDir): %v", err)
	}
	if rSub.WorkDir != subDir {
		t.Errorf("sub WorkDir = %q, want %q", rSub.WorkDir, subDir)
	}
	if rSub.TopLevel != env.repoDir {
		t.Errorf("sub TopLevel = %q, want %q", rSub.TopLevel, env.repoDir)
	}
	if rSub.GitDir != wantGitDir || rSub.GitCommonDir != wantGitDir {
		t.Errorf("sub GitDir=%q GitCommonDir=%q, want %q", rSub.GitDir, rSub.GitCommonDir, wantGitDir)
	}
	sessSub, err := rSub.SessionPath()
	if err != nil {
		t.Fatalf("rSub.SessionPath(): %v", err)
	}
	wantSess := filepath.Join(wantGitDir, "by-session.json")
	if sessSub != wantSess {
		t.Errorf("SessionPath = %q, want %q", sessSub, wantSess)
	}

	// 4. Discover bare repository
	bareDir := filepath.Join(env.root, "bare.git")
	mustGit(t, "", "init", "--bare", bareDir)
	rBare, err := Discover(bareDir)
	if err != nil {
		t.Fatalf("Discover(bareDir): %v", err)
	}
	if rBare.TopLevel != "" {
		t.Errorf("bare TopLevel = %q, want empty", rBare.TopLevel)
	}
	if rBare.GitDir != bareDir || rBare.GitCommonDir != bareDir {
		t.Errorf("bare GitDir=%q GitCommonDir=%q, want %q", rBare.GitDir, rBare.GitCommonDir, bareDir)
	}
	bareHooks, err := rBare.EffectiveHooksDir()
	if err != nil {
		t.Fatalf("rBare.EffectiveHooksDir(): %v", err)
	}
	if bareHooks != filepath.Join(bareDir, "hooks") {
		t.Errorf("bare EffectiveHooksDir = %q, want %q", bareHooks, filepath.Join(bareDir, "hooks"))
	}
	mustGit(t, bareDir, "config", "core.hooksPath", "custom-hooks")
	bareRelHooks, err := rBare.EffectiveHooksDir()
	if err != nil {
		t.Fatalf("rBare.EffectiveHooksDir() relative: %v", err)
	}
	if bareRelHooks != filepath.Join(bareDir, "custom-hooks") {
		t.Errorf("bare relative EffectiveHooksDir = %q, want %q", bareRelHooks, filepath.Join(bareDir, "custom-hooks"))
	}
}

func TestWorktreeDiscoveryAndIsolation(t *testing.T) {
	env := setupTestRepo(t)
	writeFile(t, filepath.Join(env.repoDir, "README.md"), "# hello\n", 0o644)
	mustGit(t, env.repoDir, "add", "README.md")
	mustGit(t, env.repoDir, "commit", "-m", "initial commit")

	wtDir := filepath.Join(env.root, "wt-feature")
	mustGit(t, env.repoDir, "worktree", "add", wtDir, "-b", "feature-wt")

	wtSub := filepath.Join(wtDir, "nested", "dir")
	if err := os.MkdirAll(wtSub, 0o755); err != nil {
		t.Fatalf("mkdir wtSub: %v", err)
	}

	mainRepo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover(main): %v", err)
	}
	wtRepo, err := Discover(wtSub)
	if err != nil {
		t.Fatalf("Discover(wtSub): %v", err)
	}

	if wtRepo.TopLevel != wtDir {
		t.Errorf("wt TopLevel = %q, want %q", wtRepo.TopLevel, wtDir)
	}
	if wtRepo.GitCommonDir != mainRepo.GitCommonDir {
		t.Errorf("wt GitCommonDir = %q, want %q", wtRepo.GitCommonDir, mainRepo.GitCommonDir)
	}
	if wtRepo.GitDir == mainRepo.GitDir {
		t.Errorf("expected linked worktree GitDir %q to differ from main GitDir %q", wtRepo.GitDir, mainRepo.GitDir)
	}

	mainSess, err := mainRepo.SessionPath()
	if err != nil {
		t.Fatalf("mainRepo.SessionPath(): %v", err)
	}
	wtSess, err := wtRepo.SessionPath()
	if err != nil {
		t.Fatalf("wtRepo.SessionPath(): %v", err)
	}
	if mainSess == wtSess {
		t.Fatalf("expected distinct SessionPath per worktree, both got %q", mainSess)
	}
	if !strings.HasPrefix(wtSess, wtRepo.GitDir+string(os.PathSeparator)) {
		t.Errorf("wt SessionPath = %q, expected inside %q", wtSess, wtRepo.GitDir)
	}

	// Default hooks dir is shared across worktrees via GitCommonDir/hooks
	wtHooks, err := wtRepo.EffectiveHooksDir()
	if err != nil {
		t.Fatalf("wtRepo.EffectiveHooksDir(): %v", err)
	}
	if wtHooks != filepath.Join(mainRepo.GitCommonDir, "hooks") {
		t.Errorf("wt EffectiveHooksDir = %q, want %q", wtHooks, filepath.Join(mainRepo.GitCommonDir, "hooks"))
	}

	// Commit inside linked worktree succeeds and preserves worktree isolation
	writeFile(t, filepath.Join(wtDir, "feature.txt"), "worktree change\n", 0o644)
	mustGit(t, wtDir, "add", "feature.txt")
	code, err := wtRepo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by gpt-5-luna",
		AuthorEmail: "ada@analytical.engine",
		Trailers: []string{
			"Assisted-by: agy:gpt-5-luna",
			"X-Agent-Implementer: openai/gpt-5-luna",
		},
		Args: []string{"-m", "feat: commit from linked worktree"},
	})
	if err != nil || code != 0 {
		t.Fatalf("wtRepo.Commit: code=%d err=%v", code, err)
	}

	body := mustGit(t, wtDir, "log", "-1", "--format=%B")
	if !strings.Contains(body, "Assisted-by: agy:gpt-5-luna\nX-Agent-Implementer: openai/gpt-5-luna") {
		t.Errorf("unexpected worktree commit body:\n%s", body)
	}
}

func TestUserIdentityAndEffectiveHooksDir(t *testing.T) {
	env := setupTestRepo(t)
	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Configured identity takes precedence over env fallback
	t.Setenv("GIT_AUTHOR_NAME", "Env Fallback Name")
	t.Setenv("GIT_AUTHOR_EMAIL", "env@fallback.dev")
	id, err := repo.UserIdentity()
	if err != nil {
		t.Fatalf("UserIdentity: %v", err)
	}
	if id.Name != "Ada Lovelace" || id.Email != "ada@analytical.engine" {
		t.Errorf("UserIdentity = %+v, want Ada Lovelace <ada@analytical.engine>", id)
	}

	// Fallback to GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL when unset in config
	mustGit(t, env.repoDir, "config", "--unset", "user.name")
	mustGit(t, env.repoDir, "config", "--unset", "user.email")
	idFallback, err := repo.UserIdentity()
	if err != nil {
		t.Fatalf("UserIdentity fallback: %v", err)
	}
	if idFallback.Name != "Env Fallback Name" || idFallback.Email != "env@fallback.dev" {
		t.Errorf("UserIdentity fallback = %+v, want Env Fallback Name <env@fallback.dev>", idFallback)
	}

	// EffectiveHooksDir: relative and ~/ expansion
	mustGit(t, env.repoDir, "config", "core.hooksPath", ".husky")
	hooksRel, err := repo.EffectiveHooksDir()
	if err != nil {
		t.Fatalf("EffectiveHooksDir(.husky): %v", err)
	}
	if hooksRel != filepath.Join(env.repoDir, ".husky") {
		t.Errorf("EffectiveHooksDir(.husky) = %q, want %q", hooksRel, filepath.Join(env.repoDir, ".husky"))
	}

	mustGit(t, env.repoDir, "config", "core.hooksPath", "~/my-global-hooks")
	hooksTilde, err := repo.EffectiveHooksDir()
	if err != nil {
		t.Fatalf("EffectiveHooksDir(~/...): %v", err)
	}
	if hooksTilde != filepath.Join(env.home, "my-global-hooks") {
		t.Errorf("EffectiveHooksDir(~/...) = %q, want %q", hooksTilde, filepath.Join(env.home, "my-global-hooks"))
	}
}

func TestApplyTrailersToFile(t *testing.T) {
	env := setupTestRepo(t)
	msgFile := filepath.Join(env.repoDir, "COMMIT_EDITMSG")
	orig := "feat: implement parser\n\nDetailed body paragraph.\n\nAssisted-by: stale-model\n\n# Please enter the commit message\n"
	writeFile(t, msgFile, orig, 0o644)

	// Empty trailers is a no-op
	if err := ApplyTrailersToFile(env.repoDir, msgFile, nil); err != nil {
		t.Fatalf("ApplyTrailersToFile(nil): %v", err)
	}
	gotNoop, _ := os.ReadFile(msgFile)
	if string(gotNoop) != orig {
		t.Fatalf("expected file untouched on empty trailers")
	}

	trailers := []string{
		"Assisted-by: pi:claude-sonnet-4-5+1 [bash] [edit]",
		"X-Agent-Orchestrator: anthropic/claude-opus-4-6",
		"X-Agent-Planner: google/gemini-3.1-pro",
		"X-Agent-Implementer: anthropic/claude-sonnet-4-5, openai/gpt-5",
		"X-Agent-Reviewer: openai/o3-pro",
		"X-Agent-Harness: pi@1.2.0",
	}
	if err := ApplyTrailersToFile(env.repoDir, "COMMIT_EDITMSG", trailers); err != nil {
		t.Fatalf("ApplyTrailersToFile: %v", err)
	}

	updatedBytes, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatalf("read msgFile: %v", err)
	}
	updated := string(updatedBytes)
	if strings.Contains(updated, "stale-model") {
		t.Errorf("expected stale Assisted-by trailer to be replaced, got:\n%s", updated)
	}

	wantBlock := strings.Join(trailers, "\n")
	if !strings.Contains(updated, wantBlock) {
		t.Errorf("expected exact ordered trailer block:\n%s\n\ngot:\n%s", wantBlock, updated)
	}
	if strings.Index(updated, wantBlock) > strings.Index(updated, "# Please enter") {
		t.Errorf("expected trailers to appear above git comment lines, got:\n%s", updated)
	}
}

func TestCommit_AuthorVsCommitterPreservation(t *testing.T) {
	env := setupTestRepo(t)
	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Case 1: Committer falls back to git config user.name/user.email while Author has `by <model>`
	writeFile(t, filepath.Join(env.repoDir, "a.go"), "package a\n", 0o644)
	mustGit(t, env.repoDir, "add", "a.go")

	trailers := []string{
		"Assisted-by: agy:claude-sonnet-4-5 [read] [write]",
		"X-Agent-Planner: anthropic/claude-opus-4-6",
		"X-Agent-Implementer: anthropic/claude-sonnet-4-5",
		"X-Agent-Harness: agy@2.0.0",
	}
	var stdout, stderr bytes.Buffer
	code, err := repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by claude-sonnet-4-5",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    trailers,
		Args:        []string{"-m", "feat: add package a"},
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("Commit failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}

	meta := mustGit(t, env.repoDir, "log", "-1", "--format=%an|%ae|%cn|%ce")
	wantMeta := "Ada Lovelace by claude-sonnet-4-5|ada@analytical.engine|Ada Lovelace|ada@analytical.engine"
	if meta != wantMeta {
		t.Errorf("commit identities = %q, want %q", meta, wantMeta)
	}

	body := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	if strings.Contains(body, "Co-authored-by") || strings.Contains(body, "Signed-off-by") {
		t.Errorf("must never emit Co-authored-by or Signed-off-by, got:\n%s", body)
	}
	wantBlock := strings.Join(trailers, "\n")
	if !strings.Contains(body, wantBlock) {
		t.Errorf("commit body missing exact trailer order:\nwant:\n%s\ngot:\n%s", wantBlock, body)
	}

	// Case 2: Explicit GIT_COMMITTER_NAME / GIT_COMMITTER_EMAIL in environment must NEVER be modified
	t.Setenv("GIT_COMMITTER_NAME", "Release Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@ci.local")

	writeFile(t, filepath.Join(env.repoDir, "b.go"), "package b\n", 0o644)
	mustGit(t, env.repoDir, "add", "b.go")
	code, err = repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by o3-pro",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    []string{"Assisted-by: o3-pro"},
		Args:        []string{"-m", "feat: add package b with O'Reilly quote"},
	})
	if err != nil || code != 0 {
		t.Fatalf("Commit 2 failed: code=%d err=%v", code, err)
	}

	meta2 := mustGit(t, env.repoDir, "log", "-1", "--format=%an|%ae|%cn|%ce")
	wantMeta2 := "Ada Lovelace by o3-pro|ada@analytical.engine|Release Bot|bot@ci.local"
	if meta2 != wantMeta2 {
		t.Errorf("commit 2 identities = %q, want %q", meta2, wantMeta2)
	}
}

func TestCommit_PreCommitExitCodeHuskyZeroArgAndPrepareCommitMsgChaining(t *testing.T) {
	env := setupTestRepo(t)
	// Configure custom .husky hooks directory with a non-executable helper in `_/`
	huskyDir := filepath.Join(env.repoDir, ".husky")
	mustGit(t, env.repoDir, "config", "core.hooksPath", ".husky")

	// Helper sourced via $(dirname "$0")/_/helper.sh just like Husky!
	writeFile(t, filepath.Join(huskyDir, "_", "helper.sh"), "HELPER_LOADED=yes\n", 0o644)

	// .sample file (even with 0755!) must be ignored
	writeFile(t, filepath.Join(huskyDir, "pre-commit.sample"), "#!/bin/sh\nexit 99\n", 0o755)

	// Failing pre-commit hook that also verifies $0 points to original .husky/pre-commit
	preCommitPath := filepath.Join(huskyDir, "pre-commit")
	failHook := "#!/bin/sh\n. \"$(dirname \"$0\")/_/helper.sh\"\nif [ \"$HELPER_LOADED\" != \"yes\" ]; then exit 55; fi\necho \"pre-commit blocked\" >&2\nexit 1\n"
	writeFile(t, preCommitPath, failHook, 0o755)

	// Existing prepare-commit-msg hook that prefixes "[PROJ-77] " and checks single-quote safety
	pcmPath := filepath.Join(huskyDir, "prepare-commit-msg")
	pcmScript := "#!/bin/sh\ntmp=\"$1.tmp\"\nprintf '[PROJ-77] ' > \"$tmp\"\ncat \"$1\" >> \"$tmp\"\nmv \"$tmp\" \"$1\"\n"
	writeFile(t, pcmPath, pcmScript, 0o755)

	writeFile(t, filepath.Join(env.repoDir, "app.go"), "package main\n", 0o644)
	mustGit(t, env.repoDir, "add", "app.go")

	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	var stderr bytes.Buffer
	code, err := repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by claude-sonnet-4-5",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    []string{"Assisted-by: claude-sonnet-4-5"},
		Args:        []string{"-m", "implement O'Reilly handler"},
		Stderr:      &stderr,
	})
	if err != nil {
		t.Fatalf("expected nil error on hook rejection, got: %v", err)
	}
	if code == 0 {
		t.Fatalf("expected non-zero exit code when pre-commit fails, got 0")
	}
	if !strings.Contains(stderr.String(), "pre-commit blocked") {
		t.Errorf("expected stderr from pre-commit hook, got: %q", stderr.String())
	}

	// Fix pre-commit to pass while still asserting $0 resolves to original .husky/pre-commit!
	argZeroRecord := filepath.Join(env.root, "pre_commit_arg0.txt")
	passHook := "#!/bin/sh\n. \"$(dirname \"$0\")/_/helper.sh\"\n[ \"$HELPER_LOADED\" = \"yes\" ] || exit 55\nprintf '%s' \"$0\" > '" + argZeroRecord + "'\nexit 0\n"
	writeFile(t, preCommitPath, passHook, 0o755)

	code, err = repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by claude-sonnet-4-5",
		AuthorEmail: "ada@analytical.engine",
		Trailers: []string{
			"Assisted-by: agy:claude-sonnet-4-5",
			"X-Agent-Implementer: anthropic/claude-sonnet-4-5",
		},
		Args: []string{"-m", "implement O'Reilly handler"},
	})
	if err != nil || code != 0 {
		t.Fatalf("expected Commit to pass after fixing pre-commit: code=%d err=%v", code, err)
	}

	gotArg0, err := os.ReadFile(argZeroRecord)
	if err != nil {
		t.Fatalf("read argZeroRecord: %v", err)
	}
	if string(gotArg0) != preCommitPath {
		t.Errorf("inside pre-commit $0 = %q, want %q", string(gotArg0), preCommitPath)
	}

	body := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	if !strings.HasPrefix(body, "[PROJ-77] implement O'Reilly handler") {
		t.Errorf("expected existing prepare-commit-msg prefix preserved, got:\n%s", body)
	}
	if !strings.Contains(body, "Assisted-by: agy:claude-sonnet-4-5\nX-Agent-Implementer: anthropic/claude-sonnet-4-5") {
		t.Errorf("expected trailers appended after chained prepare-commit-msg, got:\n%s", body)
	}
}

func TestInstallAndUninstallHooks_LocalChainingAndGitByActiveGuard(t *testing.T) {
	env := setupTestRepo(t)
	hooksDir := filepath.Join(env.repoDir, ".git", "hooks")

	// Existing user hooks: pre-commit (unmanaged), prepare-commit-msg, and post-commit
	origLog := filepath.Join(env.root, "orig_hooks.log")
	binLog := filepath.Join(env.root, "bin_invocations.log")

	writeFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\necho pre-commit >> '"+origLog+"'\n", 0o755)
	writeFile(t, filepath.Join(hooksDir, "prepare-commit-msg"), "#!/bin/sh\necho orig-pcm >> '"+origLog+"'\n", 0o755)
	writeFile(t, filepath.Join(hooksDir, "post-commit"), "#!/bin/sh\necho orig-post >> '"+origLog+"'\n", 0o755)

	// Fake `git-by` binary script to observe `hook run <name>` calls during native `git commit`
	fakeBinDir := filepath.Join(env.root, "bin with spaces")
	fakeBin := filepath.Join(fakeBinDir, "git-by")
	fakeBinScript := "#!/bin/sh\necho \"$1 $2 $3\" >> '" + binLog + "'\nif [ \"$2\" = \"run\" ] && [ \"$3\" = \"prepare-commit-msg\" ]; then\n\tgit interpret-trailers --in-place --if-exists replace --trailer 'Assisted-by: hook-model' \"$4\"\nfi\n"
	writeFile(t, fakeBin, fakeBinScript, 0o755)

	installedDir, err := InstallHooks(env.repoDir, HookScope{BinCmd: fakeBin})
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if installedDir != hooksDir {
		t.Errorf("installedDir = %q, want %q", installedDir, hooksDir)
	}

	// Re-running InstallHooks must be idempotent and NOT clobber .git-by-orig!
	if _, err := InstallHooks(env.repoDir, HookScope{BinCmd: fakeBin}); err != nil {
		t.Fatalf("second InstallHooks: %v", err)
	}
	origPCMBytes, err := os.ReadFile(filepath.Join(hooksDir, "prepare-commit-msg.git-by-orig"))
	if err != nil {
		t.Fatalf("expected prepare-commit-msg.git-by-orig to exist: %v", err)
	}
	if strings.Contains(string(origPCMBytes), managedHookMarker) {
		t.Fatalf("re-install clobbered .git-by-orig with managed hook!")
	}

	// Flow A: Native `git commit` -> runs orig hooks AND fakeBin hook run prepare-commit-msg + post-commit
	writeFile(t, filepath.Join(env.repoDir, "one.txt"), "1\n", 0o644)
	mustGit(t, env.repoDir, "add", "one.txt")
	mustGit(t, env.repoDir, "commit", "-m", "native commit")

	body1 := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	if !strings.Contains(body1, "Assisted-by: hook-model") {
		t.Errorf("native commit missing hook-injected trailer:\n%s", body1)
	}
	gotOrigA, _ := os.ReadFile(origLog)
	if string(gotOrigA) != "pre-commit\norig-pcm\norig-post\n" {
		t.Errorf("orig hooks log after native commit = %q", string(gotOrigA))
	}
	gotBinA, _ := os.ReadFile(binLog)
	if string(gotBinA) != "hook run prepare-commit-msg\nhook run post-commit\n" {
		t.Errorf("bin invocations after native commit = %q", string(gotBinA))
	}

	// Flow B: `repo.Commit` sets GIT_BY_ACTIVE=1 -> orig hooks STILL run once, fakeBin is SKIPPED (no double execution!)
	_ = os.Remove(origLog)
	_ = os.Remove(binLog)

	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	writeFile(t, filepath.Join(env.repoDir, "two.txt"), "2\n", 0o644)
	mustGit(t, env.repoDir, "add", "two.txt")
	code, err := repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by direct-model",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    []string{"Assisted-by: direct-model"},
		Args:        []string{"-m", "managed commit"},
	})
	if err != nil || code != 0 {
		t.Fatalf("repo.Commit with installed hooks: code=%d err=%v", code, err)
	}

	body2 := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	if !strings.Contains(body2, "Assisted-by: direct-model") || strings.Contains(body2, "hook-model") {
		t.Errorf("expected direct-model trailer without double hook execution, got:\n%s", body2)
	}
	gotOrigB, _ := os.ReadFile(origLog)
	if string(gotOrigB) != "pre-commit\norig-pcm\norig-post\n" {
		t.Errorf("orig hooks log after repo.Commit = %q, want each original hook once", string(gotOrigB))
	}
	if _, err := os.Stat(binLog); !os.IsNotExist(err) {
		gotBinB, _ := os.ReadFile(binLog)
		t.Errorf("expected GIT_BY_ACTIVE=1 to prevent installed git-by hook run, got: %q", string(gotBinB))
	}

	// UninstallHooks restores .git-by-orig back to original hooks and leaves pre-commit untouched
	uninstalledDir, err := UninstallHooks(env.repoDir, HookScope{})
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if uninstalledDir != hooksDir {
		t.Errorf("uninstalledDir = %q, want %q", uninstalledDir, hooksDir)
	}
	for _, h := range []string{"prepare-commit-msg", "post-commit"} {
		restored, err := os.ReadFile(filepath.Join(hooksDir, h))
		if err != nil {
			t.Fatalf("expected restored %s: %v", h, err)
		}
		if strings.Contains(string(restored), managedHookMarker) {
			t.Errorf("expected %s restored to original unmanaged hook", h)
		}
		if _, err := os.Lstat(filepath.Join(hooksDir, h+".git-by-orig")); !os.IsNotExist(err) {
			t.Errorf("expected %s.git-by-orig removed after restore", h)
		}
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "pre-commit")); err != nil {
		t.Errorf("expected unmanaged pre-commit preserved: %v", err)
	}

	// Calling UninstallHooks again when hooks are unmanaged leaves unmanaged hooks intact
	if _, err := UninstallHooks(env.repoDir, HookScope{}); err != nil {
		t.Fatalf("second UninstallHooks: %v", err)
	}
	for _, h := range []string{"prepare-commit-msg", "post-commit", "pre-commit"} {
		if _, err := os.Stat(filepath.Join(hooksDir, h)); err != nil {
			t.Errorf("expected unmanaged %s still preserved after second UninstallHooks: %v", h, err)
		}
	}
}

func TestCommit_StdinPassthroughAndFailingPrepareCommitMsg(t *testing.T) {
	env := setupTestRepo(t)
	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// 1. Stdin passthrough via `-F -`
	writeFile(t, filepath.Join(env.repoDir, "stdin.txt"), "hello stdin\n", 0o644)
	mustGit(t, env.repoDir, "add", "stdin.txt")
	code, err := repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by gemini-3.1-pro",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    []string{"Assisted-by: gemini-3.1-pro"},
		Args:        []string{"-F", "-"},
		Stdin:       strings.NewReader("feat: commit message via stdin\n"),
	})
	if err != nil || code != 0 {
		t.Fatalf("Commit with Stdin -F -: code=%d err=%v", code, err)
	}
	body := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	if !strings.HasPrefix(body, "feat: commit message via stdin") || !strings.Contains(body, "Assisted-by: gemini-3.1-pro") {
		t.Errorf("unexpected body from stdin commit:\n%s", body)
	}

	// 2. Failing existing prepare-commit-msg aborts Commit with non-zero exit code and nil err
	pcmPath := filepath.Join(env.repoDir, ".git", "hooks", "prepare-commit-msg")
	writeFile(t, pcmPath, "#!/bin/sh\necho 'pcm rejected' >&2\nexit 2\n", 0o755)
	writeFile(t, filepath.Join(env.repoDir, "fail.txt"), "nope\n", 0o644)
	mustGit(t, env.repoDir, "add", "fail.txt")

	var stderr bytes.Buffer
	code, err = repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by gemini-3.1-pro",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    []string{"Assisted-by: gemini-3.1-pro"},
		Args:        []string{"-m", "should abort"},
		Stderr:      &stderr,
	})
	if err != nil {
		t.Fatalf("expected nil error on prepare-commit-msg exit!=0, got: %v", err)
	}
	if code == 0 {
		t.Fatalf("expected non-zero exit code when prepare-commit-msg fails")
	}
	if !strings.Contains(stderr.String(), "pcm rejected") {
		t.Errorf("expected pcm stderr output, got: %q", stderr.String())
	}
}

func TestInstallAndUninstallHooks_Global(t *testing.T) {
	env := setupTestRepo(t)
	outsideDir := filepath.Join(env.root, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outsideDir: %v", err)
	}

	// 1. Default global hooks dir when core.hooksPath is unset
	hooksDir, err := InstallHooks(outsideDir, HookScope{Global: true})
	if err != nil {
		t.Fatalf("InstallHooks(Global): %v", err)
	}
	wantDefault := filepath.Join(env.home, ".config", "git", "hooks")
	if hooksDir != wantDefault {
		t.Errorf("global hooksDir = %q, want %q", hooksDir, wantDefault)
	}

	cfgVal := mustGit(t, outsideDir, "config", "--global", "--get", "core.hooksPath")
	if cfgVal != wantDefault {
		t.Errorf("global core.hooksPath = %q, want %q", cfgVal, wantDefault)
	}

	for _, h := range []string{"prepare-commit-msg", "post-commit"} {
		data, err := os.ReadFile(filepath.Join(hooksDir, h))
		if err != nil {
			t.Fatalf("read global %s: %v", h, err)
		}
		if !strings.Contains(string(data), managedHookMarker) || !strings.Contains(string(data), "'git-by' hook run "+h) {
			t.Errorf("unexpected global %s script:\n%s", h, string(data))
		}
	}

	if _, err := UninstallHooks(outsideDir, HookScope{Global: true}); err != nil {
		t.Fatalf("UninstallHooks(Global): %v", err)
	}
	for _, h := range []string{"prepare-commit-msg", "post-commit"} {
		if _, err := os.Stat(filepath.Join(hooksDir, h)); !os.IsNotExist(err) {
			t.Errorf("expected global %s removed after UninstallHooks", h)
		}
	}

	// 2. Respects existing global core.hooksPath with ~/ expansion
	mustGit(t, outsideDir, "config", "--global", "core.hooksPath", "~/custom-hooks")
	customGlobalDir, err := InstallHooks("", HookScope{Global: true})
	if err != nil {
		t.Fatalf("InstallHooks(Global ~/custom-hooks): %v", err)
	}
	wantCustom := filepath.Join(env.home, "custom-hooks")
	if customGlobalDir != wantCustom {
		t.Errorf("customGlobalDir = %q, want %q", customGlobalDir, wantCustom)
	}
	if _, err := UninstallHooks("", HookScope{Global: true}); err != nil {
		t.Fatalf("UninstallHooks(Global ~/custom-hooks): %v", err)
	}
}

func TestIsHookRunnable_CrossPlatform(t *testing.T) {
	dir := t.TempDir()

	pcmShebang := filepath.Join(dir, "prepare-commit-msg")
	writeFile(t, pcmShebang, "#!/bin/sh\necho hi\n", 0o644)

	huskyDir := filepath.Join(dir, "husky")
	pcmHuskyV9 := filepath.Join(huskyDir, "prepare-commit-msg")
	writeFile(t, pcmHuskyV9, "npm test\n", 0o644)

	p4Hook := filepath.Join(dir, "p4-prepare-changelist")
	writeFile(t, p4Hook, "echo p4\n", 0o644)

	emptyPreCommit := filepath.Join(dir, "pre-commit")
	writeFile(t, emptyPreCommit, "", 0o644)

	helperNoShebang := filepath.Join(dir, "helper.sh")
	writeFile(t, helperNoShebang, "HELPER=1\n", 0o644)

	customShebang := filepath.Join(dir, "custom-hook")
	writeFile(t, customShebang, "#!/bin/sh\nexit 0\n", 0o644)

	origBackup := filepath.Join(dir, "prepare-commit-msg.git-by-orig")
	writeFile(t, origBackup, "#!/bin/sh\nexit 0\n", 0o755)

	subDir := filepath.Join(dir, "_")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir _: %v", err)
	}

	batFile := filepath.Join(dir, "hook.bat")
	writeFile(t, batFile, "@echo off\n", 0o644)
	cmdFile := filepath.Join(dir, "hook.CMD")
	writeFile(t, cmdFile, "@echo off\n", 0o644)
	exeFile := filepath.Join(dir, "hook.exe")
	writeFile(t, exeFile, "MZ", 0o644)

	cases := []struct {
		name        string
		path        string
		wantLinux   bool
		wantWindows bool
	}{
		{"non-exec prepare-commit-msg with shebang", pcmShebang, false, true},
		{"non-exec Husky v9 prepare-commit-msg without shebang", pcmHuskyV9, false, true},
		{"non-exec p4-prepare-changelist without shebang", p4Hook, false, true},
		{"empty pre-commit", emptyPreCommit, false, false},
		{"non-standard helper without shebang", helperNoShebang, false, false},
		{"non-standard custom file with shebang", customShebang, false, true},
		{".git-by-orig backup ignored on windows", origBackup, true, false},
		{"directory ignored on both", subDir, false, false},
		{".bat executable on windows", batFile, false, true},
		{".CMD executable on windows", cmdFile, false, true},
		{".exe executable on windows", exeFile, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatalf("stat %s: %v", tc.path, err)
			}
			if got := isHookRunnable("linux", tc.path, info); got != tc.wantLinux {
				t.Errorf("isHookRunnable(linux, %s) = %v, want %v", tc.path, got, tc.wantLinux)
			}
			if got := isHookRunnable("windows", tc.path, info); got != tc.wantWindows {
				t.Errorf("isHookRunnable(windows, %s) = %v, want %v", tc.path, got, tc.wantWindows)
			}
		})
	}
}

func TestRenderHookScripts_WindowsAndUnix(t *testing.T) {
	winHook := `C:\Users\dev\repo\.husky\pre-commit`
	if got := hookExecPath("windows", winHook); got != "C:/Users/dev/repo/.husky/pre-commit" {
		t.Fatalf("hookExecPath(windows) = %q, want forward slashes", got)
	}
	if got := hookExecPath("linux", "/repo/.husky/pre-commit"); got != "/repo/.husky/pre-commit" {
		t.Fatalf("hookExecPath(linux) = %q", got)
	}

	winForward := renderForwardHookScript("windows", winHook)
	for _, want := range []string{
		"#!/bin/sh\n",
		"if [ -x 'C:/Users/dev/repo/.husky/pre-commit' ]; then\n",
		"\texec 'C:/Users/dev/repo/.husky/pre-commit' \"$@\"\n",
		"exec sh 'C:/Users/dev/repo/.husky/pre-commit' \"$@\"\n",
	} {
		if !strings.Contains(winForward, want) {
			t.Errorf("windows forward script missing %q:\n%s", want, winForward)
		}
	}
	if strings.Contains(winForward, `\`) {
		t.Errorf("windows forward script still contains backslash:\n%s", winForward)
	}

	linuxForward := renderForwardHookScript("linux", "/repo/.husky/pre-commit")
	wantLinuxForward := "#!/bin/sh\nexec '/repo/.husky/pre-commit' \"$@\"\n"
	if linuxForward != wantLinuxForward {
		t.Errorf("linux forward script =\n%q\nwant:\n%q", linuxForward, wantLinuxForward)
	}

	winOrig := `C:\Users\dev\repo\.git\hooks\prepare-commit-msg.git-by-orig`
	winManaged := renderManagedHookScript("windows", winOrig, "'git-by'", "prepare-commit-msg")
	for _, want := range []string{
		managedHookMarker,
		"if [ -x 'C:/Users/dev/repo/.git/hooks/prepare-commit-msg.git-by-orig' ]; then",
		"elif [ -s 'C:/Users/dev/repo/.git/hooks/prepare-commit-msg.git-by-orig' ]; then",
		"\tsh 'C:/Users/dev/repo/.git/hooks/prepare-commit-msg.git-by-orig' \"$@\" || exit $?",
		"[ \"${GIT_BY_ACTIVE:-}\" = \"1\" ] && exit 0",
		"exec 'git-by' hook run prepare-commit-msg \"$@\"",
	} {
		if !strings.Contains(winManaged, want) {
			t.Errorf("windows managed script missing %q:\n%s", want, winManaged)
		}
	}

	linuxManaged := renderManagedHookScript("linux", "/repo/.git/hooks/prepare-commit-msg.git-by-orig", "'git-by'", "prepare-commit-msg")
	if strings.Contains(linuxManaged, "elif [ -s") {
		t.Errorf("linux managed script should not contain windows elif fallback:\n%s", linuxManaged)
	}
	if !strings.Contains(linuxManaged, "if [ -x '/repo/.git/hooks/prepare-commit-msg.git-by-orig' ]; then") {
		t.Errorf("linux managed script missing [ -x ... ] guard:\n%s", linuxManaged)
	}

	winPCM := renderTempPrepareCommitMsg("windows", `C:\repo\.husky\prepare-commit-msg`, nil, true)
	if !strings.Contains(winPCM, "sh 'C:/repo/.husky/prepare-commit-msg' \"$@\" || exit $?") ||
		!strings.Contains(winPCM, "exec git interpret-trailers --in-place --trim-empty --if-exists replace") {
		t.Errorf("unexpected windows temp prepare-commit-msg:\n%s", winPCM)
	}
}

func TestMirrorHookEntry_SymlinkFallbackToRecursiveCopy(t *testing.T) {
	failSymlink := func(oldname, newname string) error {
		return os.ErrPermission
	}

	srcRoot := t.TempDir()
	dstRoot := t.TempDir()

	// 1. Regular read-only file copied cleanly with owner write bit for later cleanup.
	roFile := filepath.Join(srcRoot, "helper.txt")
	writeFile(t, roFile, "helper content\n", 0o444)
	dstFile := filepath.Join(dstRoot, "helper.txt")
	if err := mirrorHookEntry(roFile, dstFile, failSymlink); err != nil {
		t.Fatalf("mirrorHookEntry(file): %v", err)
	}
	gotFile, err := os.ReadFile(dstFile)
	if err != nil || string(gotFile) != "helper content\n" {
		t.Fatalf("copied file = %q (err=%v), want %q", string(gotFile), err, "helper content\n")
	}

	// 2. Nested directory structure (.husky/_/husky.sh + nested subdir) copied recursively.
	nestedSrc := filepath.Join(srcRoot, "_")
	writeFile(t, filepath.Join(nestedSrc, "husky.sh"), "HUSKY=1\n", 0o644)
	writeFile(t, filepath.Join(nestedSrc, "sub", "nested.sh"), "NESTED=2\n", 0o644)
	dstDir := filepath.Join(dstRoot, "_")
	if err := mirrorHookEntry(nestedSrc, dstDir, failSymlink); err != nil {
		t.Fatalf("mirrorHookEntry(dir): %v", err)
	}
	for rel, want := range map[string]string{
		"husky.sh":      "HUSKY=1\n",
		"sub/nested.sh": "NESTED=2\n",
	} {
		b, err := os.ReadFile(filepath.Join(dstDir, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Fatalf("copied %s = %q (err=%v), want %q", rel, string(b), err, want)
		}
	}

	// 3. Dangling symlink skipped without failing when symlinkFn fails.
	danglingSrc := filepath.Join(srcRoot, "dangling")
	if err := os.Symlink(filepath.Join(srcRoot, "does-not-exist"), danglingSrc); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}
	danglingDst := filepath.Join(dstRoot, "dangling")
	if err := mirrorHookEntry(danglingSrc, danglingDst, failSymlink); err != nil {
		t.Fatalf("mirrorHookEntry(dangling) returned error: %v", err)
	}
	if _, err := os.Lstat(danglingDst); !os.IsNotExist(err) {
		t.Fatalf("expected dangling symlink not to be created, err=%v", err)
	}

	// 4. Symlink cycle hits maxHookCopyDepth error instead of recursing forever.
	cycleDir := filepath.Join(srcRoot, "cycle")
	if err := os.MkdirAll(cycleDir, 0o755); err != nil {
		t.Fatalf("mkdir cycle: %v", err)
	}
	if err := os.Symlink(cycleDir, filepath.Join(cycleDir, "loop")); err != nil {
		t.Fatalf("create symlink cycle: %v", err)
	}
	if err := copyFileOrDir(cycleDir, filepath.Join(dstRoot, "cycle-copy")); err == nil {
		t.Fatalf("expected depth limit error for symlink cycle, got nil")
	}

	// 5. End-to-end repo.Commit with symlinkFn returning ErrPermission and .husky/_/helper.sh.
	env := setupTestRepo(t)
	huskyDir := filepath.Join(env.repoDir, ".husky")
	mustGit(t, env.repoDir, "config", "core.hooksPath", ".husky")
	writeFile(t, filepath.Join(huskyDir, "_", "helper.sh"), "HELPER_OK=yes\n", 0o644)
	writeFile(t, filepath.Join(huskyDir, "pre-commit"),
		"#!/bin/sh\n. \"$(dirname \"$0\")/_/helper.sh\"\n[ \"$HELPER_OK\" = \"yes\" ] || exit 42\n",
		0o755,
	)
	writeFile(t, filepath.Join(env.repoDir, "fallback.txt"), "copy fallback\n", 0o644)
	mustGit(t, env.repoDir, "add", "fallback.txt")

	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	repo.symlinkFn = failSymlink

	var stderr bytes.Buffer
	code, err := repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by gpt-5",
		AuthorEmail: "ada@analytical.engine",
		Trailers:    []string{"Assisted-by: gpt-5"},
		Args:        []string{"-m", "feat: commit with symlink copy fallback"},
		Stderr:      &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("repo.Commit with symlink fallback failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
}

func TestApplyTrailersToFile_StripsStaleManagedTrailersAndPreservesHuman(t *testing.T) {
	env := setupTestRepo(t)
	msgFile := filepath.Join(env.repoDir, "COMMIT_EDITMSG")
	orig := strings.Join([]string{
		"feat: refactor provenance parser",
		"",
		"Body paragraph explaining why.",
		"",
		"Signed-off-by: Ada Lovelace <ada@analytical.engine>",
		"Fixes: #42",
		"Assisted-by: claude-code:old-model",
		"X-Agent-Orchestrator: anthropic/claude-opus-4",
		"X-Agent-Planner: openai/o3",
		"X-Agent-Implementer: anthropic/claude-sonnet-4",
		"X-Agent-Reviewer: google/gemini-2.5-pro",
		"X-Agent-Committer: anthropic/claude-haiku-4",
		"X-Agent-Harness: claude-code@1.0.0",
		"",
		"# Please enter the commit message for your changes.",
		"",
	}, "\n")
	writeFile(t, msgFile, orig, 0o644)

	newTrailers := []string{
		"Assisted-by: gpt-5",
		"X-Agent-Implementer: openai/gpt-5",
	}
	if err := ApplyTrailersToFile(env.repoDir, msgFile, newTrailers); err != nil {
		t.Fatalf("ApplyTrailersToFile: %v", err)
	}

	updatedBytes, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatalf("read msgFile: %v", err)
	}
	updated := string(updatedBytes)

	for _, stale := range []string{
		"old-model",
		"X-Agent-Orchestrator:",
		"X-Agent-Planner:",
		"X-Agent-Reviewer:",
		"X-Agent-Committer:",
		"X-Agent-Harness:",
		"anthropic/claude-sonnet-4",
	} {
		if strings.Contains(updated, stale) {
			t.Errorf("expected stale trailer %q stripped, got:\n%s", stale, updated)
		}
	}

	for _, keep := range []string{
		"Signed-off-by: Ada Lovelace <ada@analytical.engine>",
		"Fixes: #42",
		"# Please enter the commit message for your changes.",
	} {
		if !strings.Contains(updated, keep) {
			t.Errorf("expected human trailer/comment %q preserved, got:\n%s", keep, updated)
		}
	}

	wantBlock := "Assisted-by: gpt-5\nX-Agent-Implementer: openai/gpt-5"
	blockIdx := strings.Index(updated, wantBlock)
	commentIdx := strings.Index(updated, "# Please enter")
	if blockIdx < 0 {
		t.Fatalf("expected new trailers in exact order %q, got:\n%s", wantBlock, updated)
	}
	if blockIdx > commentIdx {
		t.Errorf("expected trailers above git comment lines, got:\n%s", updated)
	}
}

func TestCommit_AmendReplacesStaleTrailersAndUpdatesAuthor(t *testing.T) {
	// Verify InspectCommitArgs edge cases first.
	inspectCases := []struct {
		name          string
		args          []string
		wantAmend     bool
		wantAuthor    bool
		wantResetAuth bool
	}{
		{"plain amend", []string{"--amend", "--no-edit"}, true, false, false},
		{"amend inside -m ignored", []string{"-m", "--amend"}, false, false, false},
		{"amend inside -am ignored", []string{"-am", "--amend"}, false, false, false},
		{"amend after -- pathspec ignored", []string{"-m", "hi", "--", "--amend"}, false, false, false},
		{"explicit --author space", []string{"--amend", "--author", "Bob <b@e.com>"}, true, true, false},
		{"explicit --author equals", []string{"--amend", "--author=Bob <b@e.com>"}, true, true, false},
		{"author inside -m ignored", []string{"--amend", "-m", "--author"}, true, false, false},
		{"reset-author detected", []string{"--amend", "--reset-author"}, true, false, true},
	}
	for _, tc := range inspectCases {
		gotAmend, gotAuthor, gotReset := InspectCommitArgs(tc.args)
		if gotAmend != tc.wantAmend || gotAuthor != tc.wantAuthor || gotReset != tc.wantResetAuth {
			t.Errorf("InspectCommitArgs(%v) = (%v, %v, %v), want (%v, %v, %v)",
				tc.args, gotAmend, gotAuthor, gotReset, tc.wantAmend, tc.wantAuthor, tc.wantResetAuth)
		}
	}

	env := setupTestRepo(t)
	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Unborn repo returns HeadAuthorIdentity == false.
	if _, ok := repo.HeadAuthorIdentity(); ok {
		t.Fatalf("expected HeadAuthorIdentity false on unborn repo")
	}

	// 1. Initial AI commit with Planner + Impl + Reviewer + Harness + Signed-off-by (-s).
	writeFile(t, filepath.Join(env.repoDir, "main.go"), "package main\n", 0o644)
	mustGit(t, env.repoDir, "add", "main.go")

	code, err := repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by claude-sonnet-4",
		AuthorEmail: "ada@analytical.engine",
		Trailers: []string{
			"Assisted-by: claude-code:claude-sonnet-4",
			"X-Agent-Planner: openai/o3",
			"X-Agent-Implementer: anthropic/claude-sonnet-4",
			"X-Agent-Reviewer: google/gemini-2.5-pro",
			"X-Agent-Harness: claude-code@1.0.0",
		},
		Args: []string{"-s", "-m", "feat: initial ai commit"},
	})
	if err != nil || code != 0 {
		t.Fatalf("initial Commit failed: code=%d err=%v", code, err)
	}

	headIdent, ok := repo.HeadAuthorIdentity()
	if !ok || headIdent.Name != "Ada Lovelace by claude-sonnet-4" || headIdent.Email != "ada@analytical.engine" {
		t.Fatalf("HeadAuthorIdentity = (%+v, %v)", headIdent, ok)
	}

	origAuthorDate := mustGit(t, env.repoDir, "log", "-1", "--format=%aI")

	// 2. Amend with only a new Implementer: stale Planner/Reviewer/Harness must be cleared,
	// Signed-off-by and AuthorDate preserved, and Author updated without duplicate "by".
	code, err = repo.Commit(CommitRequest{
		AuthorName:  "Ada Lovelace by gpt-5",
		AuthorEmail: "ada@analytical.engine",
		Trailers: []string{
			"Assisted-by: gpt-5",
			"X-Agent-Implementer: openai/gpt-5",
		},
		Args: []string{"--amend", "--no-edit"},
	})
	if err != nil || code != 0 {
		t.Fatalf("amend Commit failed: code=%d err=%v", code, err)
	}

	amendAuthor := mustGit(t, env.repoDir, "log", "-1", "--format=%an|%ae|%aI")
	wantAmendAuthor := "Ada Lovelace by gpt-5|ada@analytical.engine|" + origAuthorDate
	if amendAuthor != wantAmendAuthor {
		t.Errorf("amended author = %q, want %q", amendAuthor, wantAmendAuthor)
	}

	amendBody := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	for _, stale := range []string{"X-Agent-Planner:", "X-Agent-Reviewer:", "X-Agent-Harness:", "claude-sonnet-4"} {
		if strings.Contains(amendBody, stale) {
			t.Errorf("amended commit still has stale %q:\n%s", stale, amendBody)
		}
	}
	for _, want := range []string{
		"Signed-off-by: Ada Lovelace <ada@analytical.engine>",
		"Assisted-by: gpt-5",
		"X-Agent-Implementer: openai/gpt-5",
	} {
		if !strings.Contains(amendBody, want) {
			t.Errorf("amended commit missing %q:\n%s", want, amendBody)
		}
	}

	// 3. Amend with ClearManagedTrailers: all 7 managed AI trailers removed, Signed-off-by kept,
	// Author restored to pure human name.
	code, err = repo.Commit(CommitRequest{
		AuthorName:           "Ada Lovelace",
		AuthorEmail:          "ada@analytical.engine",
		ClearManagedTrailers: true,
		Args:                 []string{"--amend", "--no-edit"},
	})
	if err != nil || code != 0 {
		t.Fatalf("clear-trailers amend Commit failed: code=%d err=%v", code, err)
	}

	clearedAuthor := mustGit(t, env.repoDir, "log", "-1", "--format=%an|%ae")
	if clearedAuthor != "Ada Lovelace|ada@analytical.engine" {
		t.Errorf("cleared author = %q, want Ada Lovelace|ada@analytical.engine", clearedAuthor)
	}
	clearedBody := mustGit(t, env.repoDir, "log", "-1", "--format=%B")
	for _, key := range managedTrailerKeys {
		if strings.Contains(clearedBody, key+":") {
			t.Errorf("expected managed trailer %q cleared, got:\n%s", key, clearedBody)
		}
	}
	if !strings.Contains(clearedBody, "Signed-off-by: Ada Lovelace <ada@analytical.engine>") {
		t.Errorf("expected Signed-off-by preserved after ClearManagedTrailers, got:\n%s", clearedBody)
	}
}

func TestRepo_LookupEnvInjection(t *testing.T) {
	env := setupTestRepo(t)
	mustGit(t, env.repoDir, "config", "--unset", "user.name")
	mustGit(t, env.repoDir, "config", "--unset", "user.email")
	mustGit(t, env.repoDir, "config", "core.hooksPath", "~/injected-hooks")

	repo, err := Discover(env.repoDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	injectedHome := filepath.Join(env.root, "virtual-home")
	injectedEnv := map[string]string{
		"GIT_AUTHOR_NAME":  "Injected Author",
		"GIT_AUTHOR_EMAIL": "injected@example.com",
		"HOME":             injectedHome,
		"USERPROFILE":      injectedHome,
	}
	repo.LookupEnv = func(key string) (string, bool) {
		v, ok := injectedEnv[key]
		return v, ok
	}

	ident, err := repo.UserIdentity()
	if err != nil {
		t.Fatalf("UserIdentity: %v", err)
	}
	if ident.Name != "Injected Author" || ident.Email != "injected@example.com" {
		t.Errorf("UserIdentity = %+v, want Injected Author <injected@example.com>", ident)
	}

	hooksDir, err := repo.EffectiveHooksDir()
	if err != nil {
		t.Fatalf("EffectiveHooksDir: %v", err)
	}
	wantHooksDir := filepath.Join(injectedHome, "injected-hooks")
	if hooksDir != wantHooksDir {
		t.Errorf("EffectiveHooksDir = %q, want %q", hooksDir, wantHooksDir)
	}
}
