package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/XYZ1024-alt/git-by/internal/git"
	"github.com/XYZ1024-alt/git-by/internal/provenance"
	"github.com/XYZ1024-alt/git-by/internal/session"
)

func (a *App) cmdCommit(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		a.printCommitHelp(a.stdout)
		return 0
	}

	cliMeta, clearTrailers, gitArgs, err := parseCommitArgs(args)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by commit: %v\n", err)
		return 1
	}

	repo, err := git.Discover(a.workDir)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by commit: %v\n", err)
		return 1
	}
	repo.LookupEnv = a.lookupEnv

	sessionPath, err := repo.SessionPath()
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by commit: %v\n", err)
		return 1
	}

	userIdent, err := repo.UserIdentity()
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by commit: failed to resolve git user identity: %v\n", err)
		return 1
	}

	ident := userIdent
	hasAmend, hasExplicitAuthor, hasResetAuthor := git.InspectCommitArgs(gitArgs)
	if hasAmend && !hasExplicitAuthor && !hasResetAuthor {
		if headIdent, ok := repo.HeadAuthorIdentity(); ok {
			ident = headIdent
			if ident.Name == "" {
				ident.Name = userIdent.Name
			}
			if ident.Email == "" {
				ident.Email = userIdent.Email
			}
		}
	}

	var (
		authorName string
		trailers   []string
	)
	if clearTrailers {
		authorName = provenance.StripAuthorAttribution(ident.Name)
		if authorName == "" && userIdent.Name != "" {
			authorName = provenance.StripAuthorAttribution(userIdent.Name)
		}
	} else {
		fileMeta, _, err := session.Load(sessionPath)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by commit: failed to load session: %v\n", err)
			return 1
		}

		envMeta := session.FromEnv(a.lookupEnv)
		meta := session.Merge(session.Merge(fileMeta, envMeta), cliMeta)

		authorName = meta.FormatAuthorName(ident.Name)
		rawTrailers := meta.Trailers()
		trailers = make([]string, 0, len(rawTrailers))
		for _, t := range rawTrailers {
			trailers = append(trailers, t.String())
		}
	}

	authorEmail := ident.Email

	exitCode, err := repo.Commit(git.CommitRequest{
		AuthorName:           authorName,
		AuthorEmail:          authorEmail,
		Trailers:             trailers,
		ClearManagedTrailers: clearTrailers,
		Args:                 gitArgs,
		Stdin:                a.stdin,
		Stdout:               a.stdout,
		Stderr:               a.stderr,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by commit: %v\n", err)
		return 1
	}
	if exitCode != 0 {
		// Preserve by-session.json untouched so user can fix hooks/staging and retry.
		return exitCode
	}

	if err := session.Clear(sessionPath); err != nil {
		fmt.Fprintf(a.stderr, "git-by commit: warning: failed to clear session: %v\n", err)
	}
	return 0
}

// parseCommitArgs strips git-by specific flags appearing before "--" while
// preserving all native git commit flags and their option arguments verbatim
// in exact order.
func parseCommitArgs(args []string) (provenance.Metadata, bool, []string, error) {
	var cliMeta provenance.Metadata
	var clearTrailers bool
	gitArgs := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		tok := args[i]

		// Everything from "--" onward belongs verbatim to git commit (pathspecs).
		if tok == "--" {
			gitArgs = append(gitArgs, args[i:]...)
			break
		}

		if tok == "--clear-trailers" {
			clearTrailers = true
			continue
		}
		if strings.HasPrefix(tok, "--clear-trailers=") {
			rawVal := strings.TrimPrefix(tok, "--clear-trailers=")
			b, err := strconv.ParseBool(rawVal)
			if err != nil {
				return provenance.Metadata{}, false, nil, fmt.Errorf("invalid boolean for --clear-trailers: %q", rawVal)
			}
			clearTrailers = b
			continue
		}

		if flagName, inlineVal, hasInline, isGitBy := matchGitByCommitFlag(tok); isGitBy {
			val := inlineVal
			if !hasInline {
				if i+1 >= len(args) {
					return provenance.Metadata{}, false, nil, fmt.Errorf("flag %s requires a value", flagName)
				}
				i++
				val = args[i]
			}
			if err := applyCLICommitFlag(&cliMeta, flagName, val); err != nil {
				return provenance.Metadata{}, false, nil, err
			}
			continue
		}

		// Preserve native git commit flags that consume the next token as their argument,
		// so values like `-m "--impl foo"` are never mistaken for git-by flags or "--".
		if nativeFlagConsumesNextArg(tok) && i+1 < len(args) {
			gitArgs = append(gitArgs, tok, args[i+1])
			i++
			continue
		}

		gitArgs = append(gitArgs, tok)
	}

	if clearTrailers && !cliMeta.IsEmpty() {
		return provenance.Metadata{}, false, nil, fmt.Errorf("--clear-trailers cannot be combined with provenance flags")
	}

	return cliMeta, clearTrailers, gitArgs, nil
}

func matchGitByCommitFlag(tok string) (flagName string, val string, hasInline bool, ok bool) {
	name := tok
	if eq := strings.IndexByte(tok, '='); eq != -1 {
		name = tok[:eq]
		val = tok[eq+1:]
		hasInline = true
	}
	switch name {
	case "--orch", "--orchestrator",
		"--plan", "--planner",
		"--impl", "--implementer",
		"--rev", "--reviewer",
		"--committer",
		"--harness",
		"--tool":
		return name, val, hasInline, true
	default:
		return "", "", false, false
	}
}

func applyCLICommitFlag(meta *provenance.Metadata, flagName, rawVal string) error {
	switch flagName {
	case "--orch", "--orchestrator":
		return session.ApplyRole(meta, provenance.RoleOrchestrator, rawVal, "", nil, true)
	case "--plan", "--planner":
		return session.ApplyRole(meta, provenance.RolePlanner, rawVal, "", nil, true)
	case "--impl", "--implementer":
		// replace=false while accumulating multiple --impl flags on the same CLI invocation;
		// session.Merge(base, cliMeta) will replace base.Implementers with cliMeta.Implementers.
		return session.ApplyRole(meta, provenance.RoleImplementer, rawVal, "", nil, false)
	case "--rev", "--reviewer":
		return session.ApplyRole(meta, provenance.RoleReviewer, rawVal, "", nil, true)
	case "--committer":
		return session.ApplyRole(meta, provenance.RoleCommitter, rawVal, "", nil, true)
	case "--harness":
		h := strings.TrimSpace(rawVal)
		if h == "" {
			return fmt.Errorf("flag %s requires a non-empty value", flagName)
		}
		meta.Harness = h
		return nil
	case "--tool":
		parts := strings.Split(rawVal, ",")
		added := 0
		for _, p := range parts {
			t := strings.TrimSpace(p)
			if t == "" {
				continue
			}
			added++
			if !containsString(meta.Tools, t) {
				meta.Tools = append(meta.Tools, t)
			}
		}
		if added == 0 {
			return fmt.Errorf("flag %s requires a non-empty value", flagName)
		}
		return nil
	default:
		return fmt.Errorf("unsupported flag %s", flagName)
	}
}

func containsString(items []string, target string) bool {
	for _, s := range items {
		if s == target {
			return true
		}
	}
	return false
}

// nativeFlagConsumesNextArg reports whether a native `git commit` flag token
// unconditionally consumes the following argv element as its option value.
// Note: `-S` and `-u` take OPTIONAL stuck arguments in git commit (`-S[<keyid>]`,
// `-u[<mode>]`), so standalone `-S` or `-u` does NOT consume the next token.
func nativeFlagConsumesNextArg(tok string) bool {
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

	// Combined short flag clusters like `-am` or `-avm`:
	// If every preceding rune is a boolean git-commit short option and the final
	// rune requires a value (`m`, `F`, `c`, `C`, `t`), the next token is its argument.
	if len(tok) >= 3 && tok[0] == '-' && tok[1] != '-' {
		cluster := tok[1:]
		last := cluster[len(cluster)-1]
		if last == 'm' || last == 'F' || last == 'c' || last == 'C' || last == 't' {
			allBoolPrefix := true
			for i := 0; i < len(cluster)-1; i++ {
				switch cluster[i] {
				case 'a', 'p', 'i', 'o', 'n', 'v', 'q', 's', 'e', 'z':
				default:
					allBoolPrefix = false
				}
			}
			if allBoolPrefix {
				return true
			}
		}
	}

	return false
}
