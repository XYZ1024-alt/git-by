package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	managedHookMarker = "# git-by-managed-hook"
	sessionFileName   = "by-session.json"
)

var managedHookNames = []string{
	"prepare-commit-msg",
	"post-commit",
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
}

// CommitRequest describes a `git commit` invocation managed by git-by.
type CommitRequest struct {
	AuthorName  string
	AuthorEmail string
	Trailers    []string // ordered "Key: Value" lines
	Args        []string // raw passthrough args to `git commit`
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
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
		name = os.Getenv("GIT_AUTHOR_NAME")
	}

	email, err := gitConfigGet(r.WorkDir, false, "user.email")
	if err != nil {
		return Identity{}, err
	}
	if email == "" {
		email = os.Getenv("GIT_AUTHOR_EMAIL")
	}

	return Identity{
		Name:  name,
		Email: email,
	}, nil
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

	expanded, err := expandHome(hooksPath)
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

// ApplyTrailersToFile injects ordered trailers into msgFile in place using `git interpret-trailers`.
func ApplyTrailersToFile(workDir string, msgFile string, trailers []string) error {
	if len(trailers) == 0 {
		return nil
	}
	if strings.TrimSpace(msgFile) == "" {
		return errors.New("git: empty commit message file path")
	}

	args := make([]string, 0, 4+2*len(trailers)+1)
	args = append(args, "interpret-trailers", "--in-place", "--if-exists", "replace")
	added := 0
	for _, t := range trailers {
		t = strings.TrimRight(t, "\r\n")
		if strings.TrimSpace(t) == "" {
			continue
		}
		args = append(args, "--trailer", t)
		added++
	}
	if added == 0 {
		return nil
	}

	resolvedMsgFile, err := resolveMsgFilePath(workDir, msgFile)
	if err != nil {
		return err
	}
	args = append(args, resolvedMsgFile)

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
				if info, statErr := os.Stat(absSrcPath); statErr == nil && !info.IsDir() && isExecutable(info.Mode()) {
					origPrepareCommitMsg = absSrcPath
				}
				continue
			}

			dstPath := filepath.Join(tempHookDir, name)
			info, statErr := os.Stat(absSrcPath)
			if statErr != nil || info.IsDir() || !isExecutable(info.Mode()) {
				if symErr := os.Symlink(absSrcPath, dstPath); symErr != nil {
					return 0, fmt.Errorf("symlink hook helper %q: %w", absSrcPath, symErr)
				}
				continue
			}

			forwardScript := "#!/bin/sh\nexec " + shellQuote(absSrcPath) + " \"$@\"\n"
			if writeErr := writeExecutableScript(dstPath, forwardScript); writeErr != nil {
				return 0, fmt.Errorf("write forwarding hook %q: %w", name, writeErr)
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return 0, fmt.Errorf("read hooks dir %q: %w", origHooksDir, readErr)
	}

	cleanTrailers := make([]string, 0, len(req.Trailers))
	for _, t := range req.Trailers {
		t = strings.TrimRight(t, "\r\n")
		if strings.TrimSpace(t) != "" {
			cleanTrailers = append(cleanTrailers, t)
		}
	}

	if len(cleanTrailers) > 0 || origPrepareCommitMsg != "" {
		var sb strings.Builder
		sb.WriteString("#!/bin/sh\n")
		if origPrepareCommitMsg != "" {
			sb.WriteString(shellQuote(origPrepareCommitMsg))
			sb.WriteString(" \"$@\" || exit $?\n")
		}
		if len(cleanTrailers) > 0 {
			sb.WriteString("exec git interpret-trailers --in-place --if-exists replace")
			for _, t := range cleanTrailers {
				sb.WriteString(" --trailer ")
				sb.WriteString(shellQuote(t))
			}
			sb.WriteString(" \"$1\"\n")
		}
		pcmPath := filepath.Join(tempHookDir, "prepare-commit-msg")
		if writeErr := writeExecutableScript(pcmPath, sb.String()); writeErr != nil {
			return 0, fmt.Errorf("write temp prepare-commit-msg hook: %w", writeErr)
		}
	}

	gitArgs := make([]string, 0, 3+len(req.Args))
	gitArgs = append(gitArgs, "-c", "core.hooksPath="+tempHookDir, "commit")
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

		var sb strings.Builder
		sb.WriteString("#!/bin/sh\n")
		sb.WriteString(managedHookMarker + "\n")
		sb.WriteString("if [ -x " + shellQuote(absOrigPath) + " ]; then\n")
		sb.WriteString("\t" + shellQuote(absOrigPath) + " \"$@\" || exit $?\n")
		sb.WriteString("fi\n")
		sb.WriteString("[ \"${GIT_BY_ACTIVE:-}\" = \"1\" ] && exit 0\n")
		sb.WriteString("exec " + formattedBin + " hook run " + hookName + " \"$@\"\n")

		if err := writeExecutableScript(hookPath, sb.String()); err != nil {
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

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
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
