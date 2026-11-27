package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "0.1.0"

func main() {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-by: failed to determine working directory: %v\n", err)
		os.Exit(1)
	}
	app := &App{
		stdin:     os.Stdin,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		lookupEnv: os.LookupEnv,
		workDir:   wd,
	}
	os.Exit(app.Run(os.Args[1:]))
}

// App encapsulates CLI execution dependencies so subcommands can be tested
// both in-process and via compiled binary execution.
type App struct {
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	lookupEnv func(string) (string, bool)
	workDir   string
	isTTY     func() bool
	rawMode   func() (restore func(), err error)
}

func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.printRootHelp(a.stdout)
		return 0
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "version", "--version", "-v":
		fmt.Fprintf(a.stdout, "git-by version %s\n", version)
		return 0
	case "help", "--help", "-h":
		return a.cmdHelp(rest)
	case "set":
		return a.cmdSet(rest)
	case "status":
		return a.cmdStatus(rest)
	case "clear":
		return a.cmdClear(rest)
	case "commit":
		return a.cmdCommit(rest)
	case "setup":
		return a.cmdSetup(rest)
	case "hook":
		return a.cmdHook(rest)
	case "stats":
		return a.cmdStats(rest)
	case "init-skill", "prompt":
		return a.cmdInitSkill(rest)
	default:
		fmt.Fprintf(a.stderr, "git-by: unknown command %q\n\nRun 'git by help' for usage.\n", sub)
		return 1
	}
}

func (a *App) useColor(forceOff bool) bool {
	if forceOff {
		return false
	}
	if val, ok := a.lookupEnv("NO_COLOR"); ok && val != "" {
		return false
	}
	// Also respect explicit empty NO_COLOR if user exported NO_COLOR with empty value?
	// Per https://no-color.org/, NO_COLOR disables color when present and non-empty.
	if term, ok := a.lookupEnv("TERM"); ok && strings.EqualFold(term, "dumb") {
		return false
	}
	return true
}

type ansiPalette struct {
	enabled bool
}

func (p ansiPalette) wrap(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p ansiPalette) bold(s string) string      { return p.wrap("1", s) }
func (p ansiPalette) dim(s string) string       { return p.wrap("2", s) }
func (p ansiPalette) cyan(s string) string      { return p.wrap("36", s) }
func (p ansiPalette) boldCyan(s string) string  { return p.wrap("1;36", s) }
func (p ansiPalette) green(s string) string     { return p.wrap("32", s) }
func (p ansiPalette) boldGreen(s string) string { return p.wrap("1;32", s) }
func (p ansiPalette) yellow(s string) string    { return p.wrap("33", s) }
func (p ansiPalette) magenta(s string) string   { return p.wrap("35", s) }
func (p ansiPalette) blue(s string) string      { return p.wrap("34", s) }

func (a *App) cmdHelp(args []string) int {
	if len(args) == 0 {
		a.printRootHelp(a.stdout)
		return 0
	}
	switch args[0] {
	case "set":
		a.printSetHelp(a.stdout)
	case "status":
		a.printStatusHelp(a.stdout)
	case "clear":
		a.printClearHelp(a.stdout)
	case "commit":
		a.printCommitHelp(a.stdout)
	case "setup":
		a.printSetupHelp(a.stdout)
	case "hook":
		a.printHookHelp(a.stdout)
	case "stats":
		a.printStatsHelp(a.stdout)
	case "init-skill", "prompt":
		a.printInitSkillHelp(a.stdout)
	case "version":
		fmt.Fprintln(a.stdout, "Usage: git by version")
	default:
		fmt.Fprintf(a.stderr, "git-by: unknown help topic %q\n", args[0])
		return 1
	}
	return 0
}

func (a *App) printRootHelp(w io.Writer) {
	fmt.Fprint(w, `git-by — Multi-agent AI provenance for Git commits

Usage:
  git by <command> [arguments]

Workflow Commands:
  set <role> <model> [flags]   Stage AI model provenance for the current worktree
  status                       Preview staged roles, Git Author/Committer, and Trailers
  clear                        Clear staged AI provenance for the current worktree
  commit [flags] [git-flags]   Commit with AI Author & Trailers (passes native git flags)

Automation & Insights:
  setup [flags]                Auto-detect & configure AI coding agent provenance rules
  hook <install|uninstall>     Manage sense-free prepare-commit-msg & post-commit hooks
  stats [flags]                Analyze Human vs AI provenance and generate SVG badges
  init-skill (alias: prompt)   Output copy-paste Markdown rules for AI coding agents

Other Commands:
  version                      Print git-by version
  help [command]               Show help for any command

Roles:
  orchestrator (orch)          High-level coordinator / router model
  planner      (plan)          Architecture & implementation planning model
  implementer  (impl, code)    Code authoring model(s) (accumulates unless --replace)
  reviewer     (rev)           Code review & verification model
  committer    (commit)        Final commit packaging model

Environment Overrides (merged over session file, overridden by CLI flags):
  GIT_BY_ORCHESTRATOR, GIT_BY_PLANNER, GIT_BY_IMPLEMENTER,
  GIT_BY_REVIEWER, GIT_BY_COMMITTER, GIT_BY_HARNESS, GIT_BY_TOOLS
`)
}

func (a *App) printSetHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by set <role> <model> [--harness <name>] [--tool <tool>...] [--replace]

Roles:
  orchestrator (orch), planner (plan), implementer (impl, code),
  reviewer (rev), committer (commit)

Flags:
  --harness <name>   Record agent harness (e.g. claude-code@1.0.0, pi@1.0.0)
  --tool <tool>      Record external tool(s) used (repeatable or comma-separated)
  --replace          Replace existing implementers/tools instead of appending
  -h, --help         Show this help
`)
}

func (a *App) printStatusHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by status [--no-color]

Merges worktree session (by-session.json) with GIT_BY_* environment variables
and previews staged roles, Git Author, untouched Git Committer, and Git Trailers.
Respects NO_COLOR.
`)
}

func (a *App) printClearHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by clear

Removes staged AI provenance (by-session.json) for the current worktree.
`)
}

func (a *App) printCommitHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by commit [git-by flags] [native git commit flags...]

git-by Flags (stripped before invoking git commit):
  --orch, --orchestrator <model>   Override orchestrator model
  --plan, --planner <model>        Override planner model
  --impl, --implementer <model>    Override implementer model(s) (repeatable)
  --rev, --reviewer <model>        Override reviewer model
  --committer <model>              Override committer model
  --harness <name>                 Override harness (e.g. pi@1.0.0)
  --tool <tool>                    Append tool(s) used (repeatable)
  --clear-trailers                 Strip managed AI trailers and author attribution (useful with --amend)

All other flags (-m, -am, --amend, -S, --no-verify, --author, -- <paths>, etc.)
are passed verbatim in order to 'git commit'. On success, clears by-session.json;
on non-zero git/hook exit, preserves by-session.json and exits with the same code.
`)
}

func (a *App) printHookHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by hook install [--global]
  git by hook uninstall [--global]

Installs or removes chain-safe prepare-commit-msg and post-commit hooks so native
'git commit' automatically injects staged AI trailers and clears session state.

Flags:
  --global     Configure hooks in global core.hooksPath (~/.config/git/hooks)
  -h, --help   Show this help
`)
}

func (a *App) printStatsHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by stats [--since <date>] [--json] [--badge <output.svg>] [--theme <auto|dark|light>] [--top <n>]

Flags:
  --since <date>         Analyze commits since date/ref (passed to git log --since)
  --json                 Output machine-readable JSON report
  --badge <output.svg>   Write standalone adaptive SVG provenance card
  --theme <mode>         SVG badge theme: auto (default), dark, or light
  --top <n>              Number of top models per role leaderboard (default: 5)
  --no-color             Disable ANSI colors in terminal dashboard
  -h, --help             Show this help
`)
}

func (a *App) printInitSkillHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  git by init-skill
  git by prompt

Outputs a copy-paste-ready Markdown rule snippet for CLAUDE.md, AGENTS.md,
.cursorrules, and Antigravity Rules.
`)
}
