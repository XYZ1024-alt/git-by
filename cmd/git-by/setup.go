package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/XYZ1024-alt/git-by/internal/git"
	"github.com/XYZ1024-alt/git-by/internal/setup"
)

func (a *App) interactiveTTY() bool {
	if a.isTTY != nil {
		return a.isTTY()
	}
	return setup.IsTerminal(a.stdin) && setup.IsTerminal(a.stdout)
}

func (a *App) cmdSetup(args []string) int {
	var (
		hasGlobal  bool
		hasProject bool
		rawAgents  []string
		uninstall  bool
		dryRun     bool
		noColor    bool
		yes        bool
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
		case tok == "-y" || tok == "--yes":
			yes = true
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
		if a.lookupEnv != nil {
			envTrim := func(key string) string {
				if v, ok := a.lookupEnv(key); ok {
					return strings.TrimSpace(v)
				}
				return ""
			}
			opts.DSHHome = envTrim("DSH_HOME")
			opts.PiAgentDir = envTrim("PI_CODING_AGENT_DIR")
			opts.OmpAgentDir = envTrim("OMP_CODING_AGENT_DIR")
			opts.CodexHome = envTrim("CODEX_HOME")
			opts.CopilotHome = envTrim("COPILOT_HOME")
			opts.ClineDir = envTrim("CLINE_DIR")
			opts.KimiHome = envTrim("KIMI_CODE_HOME")
			opts.HermesHome = envTrim("HERMES_HOME")
		}
	}

	useColor := a.useColor(noColor)

	if !yes && len(rawAgents) == 0 && a.interactiveTTY() {
		items, err := setup.DiscoverPickerItems(opts)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by setup: %v\n", err)
			return 1
		}
		selected, err := setup.RunPicker(setup.PickerConfig{
			In:        a.stdin,
			Out:       a.stdout,
			Scope:     opts.Scope,
			Uninstall: opts.Uninstall,
			DryRun:    opts.DryRun,
			Color:     useColor,
			RawMode:   a.rawMode,
		}, items)
		if err != nil {
			if errors.Is(err, setup.ErrInterrupted) {
				fmt.Fprintln(a.stdout, "Setup cancelled; nothing changed.")
				return 130
			}
			if errors.Is(err, setup.ErrCancelled) {
				fmt.Fprintln(a.stdout, "Setup cancelled; nothing changed.")
				return 0
			}
			fmt.Fprintf(a.stderr, "git-by setup: %v\n", err)
			return 1
		}
		if len(selected) == 0 {
			fmt.Fprintln(a.stdout, "No agents selected; nothing changed.")
			return 0
		}
		opts.SelectedTargets = selected
		opts.CreateMissingDirs = !opts.Uninstall
	}

	summary, err := setup.Run(opts)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by setup: %v\n", err)
		return 1
	}

	if err := setup.RenderSummary(a.stdout, summary, useColor); err != nil {
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
  git by setup [--global | --project] [--agent <names>] [-y | --yes] [--uninstall | --remove] [--dry-run] [--no-color] [-h | --help]

Configures managed git-by provenance rules across AI coding agents. In an interactive
terminal, opens a multi-select agent picker (Space to toggle, Enter to confirm) with
detected agents pre-selected; in non-TTY pipelines or with --yes / --agent, runs
non-interactively without creating directories for uninstalled tools.

Flags:
  --global             Configure global user-level agent rules (default)
  --project            Configure repository-level rules (AGENTS.md + project rule files)
  --agent <names>      Target specific agent(s), repeatable or comma-separated:
                       agy, pi, claude, codex, opencode, dsh, cursor, windsurf,
                       omp, gemini, copilot, cline, roo, kilo, droid, amp, goose,
                       crush, kiro, continue, trae, augment, qwen, kimi,
                       codebuddy, junie, zed, aider, openhands, hermes
  -y, --yes            Skip interactive picker and auto-configure detected targets
  --uninstall          Remove managed git-by rule blocks and delete standalone rule files
  --remove             Alias for --uninstall
  --dry-run            Preview planned changes without writing to disk
  --no-color           Disable ANSI colors in output
  -h, --help           Show this help
`)
}
