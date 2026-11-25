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
