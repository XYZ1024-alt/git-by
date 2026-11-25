package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/XYZ1024-alt/git-by/internal/git"
	"github.com/XYZ1024-alt/git-by/internal/setup"
)

func (a *App) cmdSetup(args []string) int {
	var (
		hasGlobal  bool
		hasProject bool
		rawAgents  []string
		uninstall  bool
		dryRun     bool
		noColor    bool
	)

	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-h" || tok == "--help":
			a.printSetupHelp(a.stdout)
			return 0
		case tok == "--global":
			hasGlobal = true
		case tok == "--project":
			hasProject = true
		case tok == "--uninstall" || tok == "--remove":
			uninstall = true
		case tok == "--dry-run":
			dryRun = true
		case tok == "--no-color":
			noColor = true
		case tok == "--agent":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by setup: --agent requires a value")
				return 1
			}
			i++
			val := strings.TrimSpace(args[i])
			if val == "" {
				fmt.Fprintln(a.stderr, "git-by setup: --agent requires a non-empty value")
				return 1
			}
			rawAgents = append(rawAgents, val)
		case strings.HasPrefix(tok, "--agent="):
			val := strings.TrimSpace(strings.TrimPrefix(tok, "--agent="))
			if val == "" {
				fmt.Fprintln(a.stderr, "git-by setup: --agent requires a non-empty value")
				return 1
			}
			rawAgents = append(rawAgents, val)
		default:
			fmt.Fprintf(a.stderr, "git-by setup: unknown argument %q\n", tok)
			return 1
		}
	}

	if hasGlobal && hasProject {
		fmt.Fprintln(a.stderr, "git-by setup: cannot combine --global and --project")
		return 1
	}

	var agents []string
	if len(rawAgents) > 0 {
		var err error
		agents, err = setup.ParseAgents(rawAgents)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by setup: %v\n", err)
			return 1
		}
	}

	opts := setup.Options{
		Scope:     setup.ScopeGlobal,
		Agents:    agents,
		Uninstall: uninstall,
		DryRun:    dryRun,
	}

	if hasProject {
		opts.Scope = setup.ScopeProject
		repo, err := git.Discover(a.workDir)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by setup: not inside a git repository: %v\n", err)
			return 1
		}
		projectRoot := strings.TrimSpace(repo.TopLevel)
		if projectRoot == "" {
			projectRoot = repo.WorkDir
		}
		opts.ProjectRoot = projectRoot
	} else {
		homeDir, err := a.resolveHomeDir()
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by setup: failed to resolve home directory: %v\n", err)
			return 1
		}
		opts.HomeDir = homeDir
	}

	summary, err := setup.Run(opts)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by setup: %v\n", err)
		return 1
	}

	if err := setup.RenderSummary(a.stdout, summary, a.useColor(noColor)); err != nil {
		fmt.Fprintf(a.stderr, "git-by setup: %v\n", err)
		return 1
	}
	return 0
}

func (a *App) resolveHomeDir() (string, error) {
	if a.lookupEnv != nil {
		if h, ok := a.lookupEnv("HOME"); ok && strings.TrimSpace(h) != "" {
			return strings.TrimSpace(h), nil
		}
		if h, ok := a.lookupEnv("USERPROFILE"); ok && strings.TrimSpace(h) != "" {
			return strings.TrimSpace(h), nil
		}
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(h) == "" {
		return "", fmt.Errorf("home directory is empty")
	}
	return strings.TrimSpace(h), nil
}

func (a *App) printSetupHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by setup [--global | --project] [--agent <names>] [--uninstall | --remove] [--dry-run] [--no-color] [-h | --help]

Auto-detects installed AI coding agents and idempotently injects or updates managed
git-by provenance rules without creating directories for uninstalled tools.

Flags:
  --global             Configure global user-level agent rules (default)
  --project            Configure repository-level rules (AGENTS.md + existing rule files)
  --agent <names>      Target specific agent(s), repeatable or comma-separated:
                       agy, pi, claude, codex, opencode, cursor, windsurf
  --uninstall          Remove managed git-by rule blocks and delete standalone rule files
  --remove             Alias for --uninstall
  --dry-run            Preview planned changes without writing to disk
  --no-color           Disable ANSI colors in output
  -h, --help           Show this help
`)
}
