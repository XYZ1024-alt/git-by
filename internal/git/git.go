package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	managedHookMarker = "# git-by-managed-hook"
	sessionFileName   = "by-session.json"
	maxHookCopyDepth  = 32
)

var managedHookNames = []string{
	"prepare-commit-msg",
	"post-commit",
}

var managedTrailerKeys = []string{
	"Assisted-by",
	"X-Agent-Orchestrator",
	"X-Agent-Planner",
	"X-Agent-Implementer",
	"X-Agent-Reviewer",
	"X-Agent-Committer",
	"X-Agent-Harness",
}

// standardGitHookNames covers Git's standard hook filenames (per githooks(5))
// so Windows NTFS extensionless hooks (including Husky v9 hooks without a #! shebang)
// are recognized.
var standardGitHookNames = map[string]struct{}{
	"applypatch-msg": {}, "pre-applypatch": {}, "post-applypatch": {},
	"pre-commit": {}, "pre-merge-commit": {}, "prepare-commit-msg": {},
	"commit-msg": {}, "post-commit": {}, "pre-rebase": {},
	"post-checkout": {}, "post-merge": {}, "pre-push": {},
	"pre-receive": {}, "update": {}, "proc-receive": {},
	"post-receive": {}, "post-update": {}, "reference-transaction": {},
	"push-to-checkout": {}, "pre-auto-gc": {}, "post-rewrite": {},
	"sendemail-validate": {}, "fsmonitor-watchman": {},
	"p4-changelist": {}, "p4-prepare-changelist": {}, "p4-post-changelist": {},
	"p4-pre-submit": {}, "post-index-change": {},
}

// Identity represents a Git user's name and email address.
type Identity struct {
	Name  string
	Email string
}

// Repo holds resolved directory paths for a Git repository or linked worktree.
type Repo struct {
	WorkDir      string
	TopLevel     string
	GitDir       string
	GitCommonDir string
	LookupEnv    func(string) (string, bool)         // optional; nil falls back to os.LookupEnv
	symlinkFn    func(oldname, newname string) error // unexported test seam; nil falls back to os.Symlink
}

// CommitRequest describes a `git commit` invocation managed by git-by.
type CommitRequest struct {
	AuthorName           string
	AuthorEmail          string
	Trailers             []string // ordered "Key: Value" lines
	ClearManagedTrailers bool     // strip managed AI trailers even when Trailers is empty
	Args                 []string // raw passthrough args to `git commit`
	Stdin                io.Reader
	Stdout               io.Writer
	Stderr               io.Writer
}

// HookScope configures where git-by hooks are installed or uninstalled.
type HookScope struct {
	Global bool
	BinCmd string // defaults to "git-by" if empty
}

// Discover resolves Git repository and worktree paths starting at dir.
func Discover(dir string) (*Repo, error) {
	if dir == "" {
		dir = "."
	}
	absWorkDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir %q: %w", dir, err)
	}
	info, err := os.Stat(absWorkDir)
	if err != nil {
		return nil, fmt.Errorf("stat workdir %q: %w", absWorkDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workdir %q is not a directory", absWorkDir)
	}

	gitDirOut, err := runGit(absWorkDir, "rev-parse", "--git-dir")
	if err != nil {
		return nil, err
	}
	commonDirOut, err := runGit(absWorkDir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}

	var topLevel string
	if topOut, topErr := runGit(absWorkDir, "rev-parse", "--show-toplevel"); topErr == nil && topOut != "" {
		topLevel = resolveFromDir(absWorkDir, topOut)
	}

	return &Repo{
		WorkDir:      absWorkDir,
		TopLevel:     topLevel,
		GitDir:       resolveFromDir(absWorkDir, gitDirOut),
		GitCommonDir: resolveFromDir(absWorkDir, commonDirOut),
	}, nil
}

// GitPath resolves rel inside the repository's or worktree's Git directory via `git rev-parse --git-path`.
func (r *Repo) GitPath(rel string) (string, error) {
	if r == nil || r.WorkDir == "" {
		return "", errors.New("git: nil or uninitialized Repo")
	}
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("git: empty relative git path")
	}
	out, err := runGit(r.WorkDir, "rev-parse", "--git-path", rel)
	if err != nil {
		return "", err
	}
	return resolveFromDir(r.WorkDir, out), nil
}

// SessionPath returns the worktree-local path to `by-session.json`.
func (r *Repo) SessionPath() (string, error) {
	return r.GitPath(sessionFileName)
}

func (r *Repo) lookupEnv(key string) (string, bool) {
	if r != nil && r.LookupEnv != nil {
		return r.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// UserIdentity reads user.name and user.email from Git config, falling back to
// GIT_AUTHOR_NAME and GIT_AUTHOR_EMAIL environment variables when unset.
func (r *Repo) UserIdentity() (Identity, error) {
	if r == nil || r.WorkDir == "" {
		return Identity{}, errors.New("git: nil or uninitialized Repo")
	}

	name, err := gitConfigGet(r.WorkDir, false, "user.name")
	if err != nil {
		return Identity{}, err
	}
	if name == "" {
		if v, ok := r.lookupEnv("GIT_AUTHOR_NAME"); ok {
			name = v
		}
	}

	email, err := gitConfigGet(r.WorkDir, false, "user.email")
	if err != nil {
		return Identity{}, err
	}
	if email == "" {
		if v, ok := r.lookupEnv("GIT_AUTHOR_EMAIL"); ok {
			email = v
		}
	}

	return Identity{
		Name:  name,
		Email: email,
	}, nil
}

// HeadAuthorIdentity returns the author name and email recorded on HEAD, or
// (Identity{}, false) on an unborn/empty repository or git log failure.
func (r *Repo) HeadAuthorIdentity() (Identity, bool) {
	if r == nil || r.WorkDir == "" {
		return Identity{}, false
	}
	out, err := runGit(r.WorkDir, "log", "-1", "--format=%an%x00%ae", "HEAD")
	if err != nil {
		return Identity{}, false
	}
	sep := strings.IndexByte(out, '\x00')
	if sep < 0 {
		return Identity{}, false
	}
	ident := Identity{
		Name:  out[:sep],
		Email: out[sep+1:],
	}
	if ident.Name == "" && ident.Email == "" {
		return Identity{}, false
	}
	return ident, true
}

// EffectiveHooksDir returns the active hooks directory for this repository/worktree,
// respecting core.hooksPath (including leading ~/ and relative paths).
func (r *Repo) EffectiveHooksDir() (string, error) {
	if r == nil || r.WorkDir == "" {
		return "", errors.New("git: nil or uninitialized Repo")
	}

	hooksPath, err := gitConfigGet(r.WorkDir, false, "core.hooksPath")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(hooksPath) == "" {
		return filepath.Join(r.GitCommonDir, "hooks"), nil
	}

	expanded, err := expandHomeWithLookup(hooksPath, r.LookupEnv)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}

	baseDir := r.TopLevel
	if baseDir == "" {
		baseDir = r.GitCommonDir
	}
	return filepath.Clean(filepath.Join(baseDir, expanded)), nil
}

// ApplyTrailersToFile strips stale managed AI trailers and injects ordered
// trailers into msgFile in place using two-pass `git interpret-trailers`.
// When trailers contains no non-empty entries, it is a no-op.
func ApplyTrailersToFile(workDir string, msgFile string, trailers []string) error {
	cleanTrailers := cleanTrailerLines(trailers)
	if len(cleanTrailers) == 0 {
		return nil
	}
	if strings.TrimSpace(msgFile) == "" {
		return errors.New("git: empty commit message file path")
	}

	resolvedMsgFile, err := resolveMsgFilePath(workDir, msgFile)
	if err != nil {
		return err
	}

	// Pass 1: strip all stale managed AI trailer keys while preserving human trailers and comments.
	stripArgs := append(stripManagedTrailerArgs(), resolvedMsgFile)
	if err := runInterpretTrailers(workDir, stripArgs); err != nil {
		return err
	}

	// Pass 2: inject the new trailers in canonical order.
	applyArgs := make([]string, 0, 4+2*len(cleanTrailers)+1)
	applyArgs = append(applyArgs, "interpret-trailers", "--in-place", "--if-exists", "replace")
	for _, t := range cleanTrailers {
		applyArgs = append(applyArgs, "--trailer", t)
	}
	applyArgs = append(applyArgs, resolvedMsgFile)
	return runInterpretTrailers(workDir, applyArgs)
}

// Commit runs `git commit` with ephemeral hook mirroring, trailer injection via prepare-commit-msg,
// and author identity overrides while leaving committer identity untouched.
func (r *Repo) Commit(req CommitRequest) (exitCode int, err error) {
	if r == nil || r.WorkDir == "" {
		return 0, errors.New("git: nil or uninitialized Repo")
	}

	origHooksDir, err := r.EffectiveHooksDir()
	if err != nil {
		return 0, err
	}

	tempHookDir, err := os.MkdirTemp("", "git-by-hooks-*")
	if err != nil {
		return 0, fmt.Errorf("create temp hooks dir: %w", err)
	}
	defer os.RemoveAll(tempHookDir)

	var origPrepareCommitMsg string
	if entries, readErr := os.ReadDir(origHooksDir); readErr == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasSuffix(name, ".sample") {
				continue
			}

			srcPath := filepath.Join(origHooksDir, name)
			absSrcPath, absErr := filepath.Abs(srcPath)
			if absErr != nil {
				return 0, fmt.Errorf("resolve hook path %q: %w", srcPath, absErr)
			}

			if name == "prepare-commit-msg" {
				if info, statErr := os.Stat(absSrcPath); statErr == nil && isHookRunnable(runtime.GOOS, absSrcPath, info) {
					origPrepareCommitMsg = absSrcPath
				}
				continue
			}

			dstPath := filepath.Join(tempHookDir, name)
			info, statErr := os.Stat(absSrcPath)
			if statErr != nil || !isHookRunnable(runtime.GOOS, absSrcPath, info) {
				if mirrorErr := mirrorHookEntry(absSrcPath, dstPath, r.symlinkFn); mirrorErr != nil {
					return 0, fmt.Errorf("mirror hook helper %q: %w", absSrcPath, mirrorErr)
				}
				continue
			}

			forwardScript := renderForwardHookScript(runtime.GOOS, absSrcPath)
			if writeErr := writeExecutableScript(dstPath, forwardScript); writeErr != nil {
				return 0, fmt.Errorf("write forwarding hook %q: %w", name, writeErr)
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return 0, fmt.Errorf("read hooks dir %q: %w", origHooksDir, readErr)
	}

	cleanTrailers := cleanTrailerLines(req.Trailers)

	if len(cleanTrailers) > 0 || req.ClearManagedTrailers || origPrepareCommitMsg != "" {
		pcmScript := renderTempPrepareCommitMsg(runtime.GOOS, origPrepareCommitMsg, cleanTrailers, req.ClearManagedTrailers)
		pcmPath := filepath.Join(tempHookDir, "prepare-commit-msg")
		if writeErr := writeExecutableScript(pcmPath, pcmScript); writeErr != nil {
			return 0, fmt.Errorf("write temp prepare-commit-msg hook: %w", writeErr)
		}
	}

	hasAmend, hasExplicitAuthor, hasResetAuthor := InspectCommitArgs(req.Args)

	gitArgs := make([]string, 0, 4+len(req.Args))
	gitArgs = append(gitArgs, "-c", "core.hooksPath="+tempHookDir, "commit")
	if hasAmend && !hasExplicitAuthor && !hasResetAuthor &&
		(len(cleanTrailers) > 0 || req.ClearManagedTrailers) &&
		req.AuthorName != "" && req.AuthorEmail != "" {
		gitArgs = append(gitArgs, "--author="+req.AuthorName+" <"+req.AuthorEmail+">")
	}
	gitArgs = append(gitArgs, req.Args...)

	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = r.WorkDir
	cmd.Stdin = req.Stdin
	cmd.Stdout = req.Stdout
	cmd.Stderr = req.Stderr

	env := append([]string(nil), os.Environ()...)
	env = upsertEnv(env, "GIT_BY_ACTIVE", "1")
	if req.AuthorName != "" {
		env = upsertEnv(env, "GIT_AUTHOR_NAME", req.AuthorName)
	}
	if req.AuthorEmail != "" {
		env = upsertEnv(env, "GIT_AUTHOR_EMAIL", req.AuthorEmail)
	}
	cmd.Env = env

	runErr := cmd.Run()
	if runErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, fmt.Errorf("run git commit: %w", runErr)
}

// InstallHooks installs chain-compatible prepare-commit-msg and post-commit hooks
// locally for the repository at dir or globally when opts.Global is true.
func InstallHooks(dir string, opts HookScope) (hooksDir string, err error) {
	binCmd := strings.TrimSpace(opts.BinCmd)
	if binCmd == "" {
		binCmd = "git-by"
	}

	hooksDir, err = resolveHookScopeDir(dir, opts.Global, true)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", fmt.Errorf("create hooks dir %q: %w", hooksDir, err)
	}

	formattedBin := formatBinCmd(binCmd)
	for _, hookName := range managedHookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		origPath := hookPath + ".git-by-orig"

		if _, lstatErr := os.Lstat(hookPath); lstatErr == nil {
			data, readErr := os.ReadFile(hookPath)
			isManaged := readErr == nil && strings.Contains(string(data), managedHookMarker)
			if !isManaged {
				if err := os.Rename(hookPath, origPath); err != nil {
					return "", fmt.Errorf("preserve existing hook %q: %w", hookPath, err)
				}
			} else {
				if err := os.Remove(hookPath); err != nil {
					return "", fmt.Errorf("replace managed hook %q: %w", hookPath, err)
				}
			}
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect hook %q: %w", hookPath, lstatErr)
		}

		absOrigPath, err := filepath.Abs(origPath)
		if err != nil {
			return "", fmt.Errorf("resolve orig hook path %q: %w", origPath, err)
		}

		script := renderManagedHookScript(runtime.GOOS, absOrigPath, formattedBin, hookName)
		if err := writeExecutableScript(hookPath, script); err != nil {
			return "", fmt.Errorf("install hook %q: %w", hookPath, err)
		}
	}

	return hooksDir, nil
}

// UninstallHooks removes git-by managed hooks and restores any preserved .git-by-orig hooks.
func UninstallHooks(dir string, opts HookScope) (hooksDir string, err error) {
	hooksDir, err = resolveHookScopeDir(dir, opts.Global, false)
	if err != nil {
		return "", err
	}

	if _, statErr := os.Stat(hooksDir); errors.Is(statErr, os.ErrNotExist) {
		return hooksDir, nil
	} else if statErr != nil {
		return "", fmt.Errorf("stat hooks dir %q: %w", hooksDir, statErr)
	}

	for _, hookName := range managedHookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		origPath := hookPath + ".git-by-orig"

		if _, lstatErr := os.Lstat(hookPath); lstatErr == nil {
			data, readErr := os.ReadFile(hookPath)
			if readErr == nil && strings.Contains(string(data), managedHookMarker) {
				if err := os.Remove(hookPath); err != nil {
					return "", fmt.Errorf("remove managed hook %q: %w", hookPath, err)
				}
			} else {
				// Leave unmanaged user hook untouched.
				continue
			}
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect hook %q: %w", hookPath, lstatErr)
		}

		if _, origErr := os.Lstat(origPath); origErr == nil {
			if err := os.Rename(origPath, hookPath); err != nil {
				return "", fmt.Errorf("restore original hook %q: %w", hookPath, err)
			}
		} else if !errors.Is(origErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect backup hook %q: %w", origPath, origErr)
		}
	}

	return hooksDir, nil
}

func resolveHookScopeDir(dir string, global bool, configureIfUnset bool) (string, error) {
	if !global {
		repo, err := Discover(dir)
		if err != nil {
			return "", err
		}
		return repo.EffectiveHooksDir()
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine user home directory: %w", err)
	}

	var cmdDir string
	if dir != "" {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			cmdDir = dir
		}
	}

	configured, err := gitConfigGet(cmdDir, true, "core.hooksPath")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(configured) != "" {
		expanded, err := expandHomeWithBase(configured, home)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(expanded) {
			return filepath.Clean(expanded), nil
		}
		return filepath.Clean(filepath.Join(home, expanded)), nil
	}

	defaultGlobalHooks := filepath.Join(home, ".config", "git", "hooks")
	if configureIfUnset {
		if err := os.MkdirAll(defaultGlobalHooks, 0o755); err != nil {
			return "", fmt.Errorf("create global hooks dir %q: %w", defaultGlobalHooks, err)
		}
		if _, err := runGit(cmdDir, "config", "--global", "core.hooksPath", defaultGlobalHooks); err != nil {
			return "", fmt.Errorf("configure global core.hooksPath: %w", err)
		}
	}
	return defaultGlobalHooks, nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, errMsg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func gitConfigGet(dir string, global bool, key string) (string, error) {
	args := make([]string, 0, 4)
	args = append(args, "config")
	if global {
		args = append(args, "--global")
	}
	args = append(args, "--get", key)

	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return strings.TrimRight(stdout.String(), "\r\n"), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}
	errMsg := strings.TrimSpace(stderr.String())
	if errMsg != "" {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, errMsg)
	}
	return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func resolveFromDir(baseDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(baseDir, p))
}

func resolveMsgFilePath(workDir, msgFile string) (string, error) {
	if filepath.IsAbs(msgFile) {
		return filepath.Clean(msgFile), nil
	}
	if workDir != "" {
		candidate := filepath.Clean(filepath.Join(workDir, msgFile))
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		if absCwd, err := filepath.Abs(msgFile); err == nil {
			if _, statErr := os.Stat(absCwd); statErr == nil {
				return absCwd, nil
			}
		}
		return candidate, nil
	}
	return filepath.Abs(msgFile)
}

func expandHomeWithLookup(p string, lookup func(string) (string, bool)) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	if lookup != nil {
		if runtime.GOOS == "windows" {
			if home, ok := lookup("USERPROFILE"); ok && home != "" {
				return expandHomeWithBase(p, home)
			}
		}
		if home, ok := lookup("HOME"); ok && home != "" {
			return expandHomeWithBase(p, home)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine user home directory: %w", err)
	}
	return expandHomeWithBase(p, home)
}

func expandHomeWithBase(p, home string) (string, error) {
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func isExecutable(mode os.FileMode) bool {
	return mode.Perm()&0o111 != 0
}

func isStandardGitHookName(name string) bool {
	_, ok := standardGitHookNames[name]
	return ok
}

func isWindowsExecutableExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".bat", ".cmd", ".com":
		return true
	default:
		return false
	}
}

func hasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [2]byte
	n, err := io.ReadFull(f, hdr[:])
	if err != nil || n < 2 {
		return false
	}
	return hdr[0] == '#' && hdr[1] == '!'
}

func isHookRunnable(goos string, path string, info os.FileInfo) bool {
	if info == nil || info.IsDir() {
		return false
	}
	if goos != "windows" {
		return isExecutable(info.Mode())
	}
	if !info.Mode().IsRegular() {
		return false
	}
	base := filepath.Base(path)
	if strings.HasSuffix(strings.ToLower(base), ".git-by-orig") {
		return false
	}
	if isExecutable(info.Mode()) || isWindowsExecutableExt(base) {
		return true
	}
	if isStandardGitHookName(base) && info.Size() > 0 {
		return true
	}
	return hasShebang(path)
}

func hookExecPath(goos, absPath string) string {
	if goos == "windows" {
		return strings.ReplaceAll(filepath.ToSlash(absPath), `\`, "/")
	}
	return absPath
}

func renderForwardHookScript(goos, absSrcPath string) string {
	execPath := shellQuote(hookExecPath(goos, absSrcPath))
	if goos == "windows" {
		return "#!/bin/sh\n" +
			"if [ -x " + execPath + " ]; then\n" +
			"\texec " + execPath + " \"$@\"\n" +
			"fi\n" +
			"exec sh " + execPath + " \"$@\"\n"
	}
	return "#!/bin/sh\nexec " + execPath + " \"$@\"\n"
}

func renderManagedHookScript(goos, absOrigPath, formattedBin, hookName string) string {
	origPath := shellQuote(hookExecPath(goos, absOrigPath))
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString(managedHookMarker + "\n")
	if goos == "windows" {
		sb.WriteString("if [ -x " + origPath + " ]; then\n")
		sb.WriteString("\t" + origPath + " \"$@\" || exit $?\n")
		sb.WriteString("elif [ -s " + origPath + " ]; then\n")
		sb.WriteString("\tsh " + origPath + " \"$@\" || exit $?\n")
		sb.WriteString("fi\n")
	} else {
		sb.WriteString("if [ -x " + origPath + " ]; then\n")
		sb.WriteString("\t" + origPath + " \"$@\" || exit $?\n")
		sb.WriteString("fi\n")
	}
	sb.WriteString("[ \"${GIT_BY_ACTIVE:-}\" = \"1\" ] && exit 0\n")
	sb.WriteString("exec " + formattedBin + " hook run " + hookName + " \"$@\"\n")
	return sb.String()
}

func cleanTrailerLines(trailers []string) []string {
	if len(trailers) == 0 {
		return nil
	}
	clean := make([]string, 0, len(trailers))
	for _, t := range trailers {
		t = strings.TrimRight(t, "\r\n")
		if strings.TrimSpace(t) != "" {
			clean = append(clean, t)
		}
	}
	return clean
}

func stripManagedTrailerArgs() []string {
	args := make([]string, 0, 5+2*len(managedTrailerKeys))
	args = append(args, "interpret-trailers", "--in-place", "--trim-empty", "--if-exists", "replace")
	for _, key := range managedTrailerKeys {
		args = append(args, "--trailer", key+":")
	}
	return args
}

func runInterpretTrailers(workDir string, args []string) error {
	cmd := exec.Command("git", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("git interpret-trailers: %w: %s", err, msg)
		}
		return fmt.Errorf("git interpret-trailers: %w", err)
	}
	return nil
}

func renderTempPrepareCommitMsg(goos, origPrepareCommitMsg string, cleanTrailers []string, clearManaged bool) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	if origPrepareCommitMsg != "" {
		origExec := shellQuote(hookExecPath(goos, origPrepareCommitMsg))
		if goos == "windows" {
			sb.WriteString("if [ -x " + origExec + " ]; then\n")
			sb.WriteString("\t" + origExec + " \"$@\" || exit $?\n")
			sb.WriteString("else\n")
			sb.WriteString("\tsh " + origExec + " \"$@\" || exit $?\n")
			sb.WriteString("fi\n")
		} else {
			sb.WriteString(origExec + " \"$@\" || exit $?\n")
		}
	}
	if len(cleanTrailers) > 0 || clearManaged {
		var stripCmd strings.Builder
		stripCmd.WriteString("git")
		for _, arg := range stripManagedTrailerArgs() {
			stripCmd.WriteByte(' ')
			if strings.HasSuffix(arg, ":") {
				stripCmd.WriteString(shellQuote(arg))
			} else {
				stripCmd.WriteString(arg)
			}
		}
		if len(cleanTrailers) == 0 {
			sb.WriteString("exec " + stripCmd.String() + " \"$1\"\n")
		} else {
			sb.WriteString(stripCmd.String() + " \"$1\" || exit $?\n")
			sb.WriteString("exec git interpret-trailers --in-place --if-exists replace")
			for _, t := range cleanTrailers {
				sb.WriteString(" --trailer ")
				sb.WriteString(shellQuote(t))
			}
			sb.WriteString(" \"$1\"\n")
		}
	}
	return sb.String()
}

// InspectCommitArgs scans raw `git commit` arguments prior to "--" to detect
// --amend, explicit --author, and --reset-author while ignoring option values
// (such as `-m "--amend"`).
func InspectCommitArgs(args []string) (hasAmend, hasExplicitAuthor, hasResetAuthor bool) {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			break
		}
		switch {
		case tok == "--amend":
			hasAmend = true
			continue
		case tok == "--no-amend":
			hasAmend = false
			continue
		case tok == "--reset-author":
			hasResetAuthor = true
			continue
		case tok == "--no-reset-author":
			hasResetAuthor = false
			continue
		case tok == "--author":
			hasExplicitAuthor = true
			if i+1 < len(args) {
				i++
			}
			continue
		case strings.HasPrefix(tok, "--author="):
			hasExplicitAuthor = true
			continue
		}
		if commitFlagConsumesNextArg(tok) && i+1 < len(args) {
			i++
		}
	}
	return hasAmend, hasExplicitAuthor, hasResetAuthor
}

func commitFlagConsumesNextArg(tok string) bool {
	switch tok {
	case "-m", "--message",
		"-F", "--file",
		"-c", "--reedit-message",
		"-C", "--reuse-message",
		"-t", "--template",
		"--author",
		"--date",
		"--cleanup",
		"--fixup",
		"--squash",
		"--trailer",
		"--pathspec-from-file":
		return true
	}
	if len(tok) >= 3 && tok[0] == '-' && tok[1] != '-' {
		cluster := tok[1:]
		last := cluster[len(cluster)-1]
		if last == 'm' || last == 'F' || last == 'c' || last == 'C' || last == 't' {
			for i := 0; i < len(cluster)-1; i++ {
				switch cluster[i] {
				case 'a', 'p', 'i', 'o', 'n', 'v', 'q', 's', 'e', 'z':
				default:
					return false
				}
			}
			return true
		}
	}
	return false
}

func mirrorHookEntry(srcPath, dstPath string, symlinkFn func(oldname, newname string) error) error {
	if symlinkFn == nil {
		symlinkFn = os.Symlink
	}
	if err := symlinkFn(srcPath, dstPath); err == nil {
		return nil
	}
	if _, statErr := os.Stat(srcPath); errors.Is(statErr, os.ErrNotExist) {
		if linfo, lstatErr := os.Lstat(srcPath); lstatErr == nil && linfo.Mode()&os.ModeSymlink != 0 {
			return nil
		}
	}
	return copyFileOrDir(srcPath, dstPath)
}

func copyFileOrDir(srcPath, dstPath string) error {
	return copyFileOrDirDepth(srcPath, dstPath, 0)
}

func copyFileOrDirDepth(srcPath, dstPath string, depth int) error {
	if depth > maxHookCopyDepth {
		return fmt.Errorf("copy hook entry %q: exceeded maximum directory depth %d", srcPath, maxHookCopyDepth)
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat hook entry %q: %w", srcPath, err)
	}

	if info.IsDir() {
		dirPerm := info.Mode().Perm() | 0o700
		if err := os.MkdirAll(dstPath, dirPerm); err != nil {
			return fmt.Errorf("create hook dir %q: %w", dstPath, err)
		}
		entries, err := os.ReadDir(srcPath)
		if err != nil {
			return fmt.Errorf("read hook dir %q: %w", srcPath, err)
		}
		for _, entry := range entries {
			childSrc := filepath.Join(srcPath, entry.Name())
			childDst := filepath.Join(dstPath, entry.Name())
			if _, childStatErr := os.Stat(childSrc); errors.Is(childStatErr, os.ErrNotExist) {
				if linfo, lstatErr := os.Lstat(childSrc); lstatErr == nil && linfo.Mode()&os.ModeSymlink != 0 {
					continue
				}
			}
			if err := copyFileOrDirDepth(childSrc, childDst, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open hook file %q: %w", srcPath, err)
	}
	defer in.Close()

	filePerm := info.Mode().Perm() | 0o600
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return fmt.Errorf("create hook file %q: %w", dstPath, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy hook file %q to %q: %w", srcPath, dstPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close hook file %q: %w", dstPath, err)
	}
	return nil
}

func writeExecutableScript(path string, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func formatBinCmd(binCmd string) string {
	if !strings.ContainsAny(binCmd, " \t") {
		return shellQuote(binCmd)
	}
	if info, err := os.Stat(binCmd); err == nil && !info.IsDir() {
		return shellQuote(binCmd)
	}
	return binCmd
}

func upsertEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if !replaced {
				out = append(out, prefix+val)
				replaced = true
			}
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}
