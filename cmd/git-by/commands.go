package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/XYZ1024-alt/git-by/internal/git"
	"github.com/XYZ1024-alt/git-by/internal/provenance"
	"github.com/XYZ1024-alt/git-by/internal/session"
	"github.com/XYZ1024-alt/git-by/internal/setup"
	"github.com/XYZ1024-alt/git-by/internal/stats"
)

func (a *App) cmdSet(args []string) int {
	var (
		harness string
		tools   []string
		replace bool
		pos     []string
	)

	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-h" || tok == "--help":
			a.printSetHelp(a.stdout)
			return 0
		case tok == "--replace":
			replace = true
		case strings.HasPrefix(tok, "--replace="):
			val := strings.TrimPrefix(tok, "--replace=")
			b, err := strconv.ParseBool(val)
			if err != nil {
				fmt.Fprintf(a.stderr, "git-by set: invalid boolean for --replace: %q\n", val)
				return 1
			}
			replace = b
		case tok == "--harness":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by set: --harness requires a value")
				return 1
			}
			i++
			harness = strings.TrimSpace(args[i])
			if harness == "" {
				fmt.Fprintln(a.stderr, "git-by set: --harness requires a non-empty value")
				return 1
			}
		case strings.HasPrefix(tok, "--harness="):
			harness = strings.TrimSpace(strings.TrimPrefix(tok, "--harness="))
			if harness == "" {
				fmt.Fprintln(a.stderr, "git-by set: --harness requires a non-empty value")
				return 1
			}
		case tok == "--tool":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by set: --tool requires a value")
				return 1
			}
			i++
			t := strings.TrimSpace(args[i])
			if t == "" {
				fmt.Fprintln(a.stderr, "git-by set: --tool requires a non-empty value")
				return 1
			}
			tools = append(tools, t)
		case strings.HasPrefix(tok, "--tool="):
			t := strings.TrimSpace(strings.TrimPrefix(tok, "--tool="))
			if t == "" {
				fmt.Fprintln(a.stderr, "git-by set: --tool requires a non-empty value")
				return 1
			}
			tools = append(tools, t)
		case tok == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(tok, "-"):
			fmt.Fprintf(a.stderr, "git-by set: unknown flag %q\n", tok)
			return 1
		default:
			pos = append(pos, tok)
		}
	}

	if len(pos) != 2 {
		fmt.Fprintln(a.stderr, "git-by set: expected <role> <model>")
		a.printSetHelp(a.stderr)
		return 1
	}

	role, err := provenance.ParseRole(pos[0])
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by set: %v\n", err)
		return 1
	}
	modelArg := pos[1]

	repo, err := git.Discover(a.workDir)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by set: %v\n", err)
		return 1
	}

	sessionPath, err := repo.SessionPath()
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by set: %v\n", err)
		return 1
	}

	meta, _, err := session.Load(sessionPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by set: failed to load session: %v\n", err)
		return 1
	}

	if err := session.ApplyRole(&meta, role, modelArg, harness, tools, replace); err != nil {
		fmt.Fprintf(a.stderr, "git-by set: %v\n", err)
		return 1
	}

	if err := session.Save(sessionPath, meta); err != nil {
		fmt.Fprintf(a.stderr, "git-by set: failed to save session: %v\n", err)
		return 1
	}

	p := ansiPalette{enabled: a.useColor(false)}
	fmt.Fprintf(a.stdout, "%s Staged %s = %s\n",
		p.boldGreen("✓"),
		p.cyan(string(role)),
		p.bold(roleSummaryValue(meta, role)),
	)
	return 0
}

func roleSummaryValue(meta provenance.Metadata, role provenance.Role) string {
	switch role {
	case provenance.RoleOrchestrator:
		return meta.Orchestrator
	case provenance.RolePlanner:
		return meta.Planner
	case provenance.RoleImplementer:
		return strings.Join(meta.Implementers, ", ")
	case provenance.RoleReviewer:
		return meta.Reviewer
	case provenance.RoleCommitter:
		return meta.Committer
	default:
		return ""
	}
}

func (a *App) cmdStatus(args []string) int {
	noColorFlag := false
	for _, tok := range args {
		switch tok {
		case "-h", "--help":
			a.printStatusHelp(a.stdout)
			return 0
		case "--no-color":
			noColorFlag = true
		default:
			fmt.Fprintf(a.stderr, "git-by status: unexpected argument %q\n", tok)
			return 1
		}
	}

	repo, err := git.Discover(a.workDir)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by status: %v\n", err)
		return 1
	}
	repo.LookupEnv = a.lookupEnv

	sessionPath, err := repo.SessionPath()
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by status: %v\n", err)
		return 1
	}

	fileMeta, exists, err := session.Load(sessionPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by status: failed to load session: %v\n", err)
		return 1
	}

	envMeta := session.FromEnv(a.lookupEnv)
	meta := session.Merge(fileMeta, envMeta)

	ident, _ := repo.UserIdentity()
	committerName := ident.Name
	if cn, ok := a.lookupEnv("GIT_COMMITTER_NAME"); ok && strings.TrimSpace(cn) != "" {
		committerName = strings.TrimSpace(cn)
	}
	committerEmail := ident.Email
	if ce, ok := a.lookupEnv("GIT_COMMITTER_EMAIL"); ok && strings.TrimSpace(ce) != "" {
		committerEmail = strings.TrimSpace(ce)
	}

	authorName := meta.FormatAuthorName(ident.Name)
	authorPreview := formatIdentityPreview(authorName, ident.Email)
	committerPreview := formatIdentityPreview(committerName, committerEmail)

	displaySessionPath := sessionPath
	if rel, err := filepath.Rel(repo.WorkDir, sessionPath); err == nil && !strings.HasPrefix(rel, "..") {
		displaySessionPath = rel
	}

	p := ansiPalette{enabled: a.useColor(noColorFlag)}
	border := p.dim("──────────────────────────────────────────────────────────────────")

	fmt.Fprintf(a.stdout, "%s %s\n", p.boldCyan("╭─"), p.boldCyan("git-by Provenance Status"))
	fmt.Fprintf(a.stdout, "%s\n", border)

	var sourceDesc string
	switch {
	case exists && !envMeta.IsEmpty():
		sourceDesc = fmt.Sprintf("%s + GIT_BY_* env", displaySessionPath)
	case exists:
		sourceDesc = fmt.Sprintf("%s (staged)", displaySessionPath)
	case !envMeta.IsEmpty():
		sourceDesc = "GIT_BY_* environment (stateless)"
	default:
		sourceDesc = fmt.Sprintf("%s (clean)", displaySessionPath)
	}
	fmt.Fprintf(a.stdout, "  %-15s %s\n\n", p.dim("Session"), sourceDesc)

	fmt.Fprintf(a.stdout, "  %s\n", p.bold("Staged Roles"))
	if meta.IsEmpty() {
		fmt.Fprintf(a.stdout, "    %s\n", p.dim("(none staged — run 'git by set <role> <model>')"))
	} else {
		printRoleRow := func(label, val string) {
			if val == "" {
				return
			}
			fmt.Fprintf(a.stdout, "    %-14s %s\n", p.cyan(label), p.bold(val))
		}
		printRoleRow("Orchestrator", meta.Orchestrator)
		printRoleRow("Planner", meta.Planner)
		if len(meta.Implementers) > 0 {
			printRoleRow("Implementer", strings.Join(meta.Implementers, ", "))
		}
		printRoleRow("Reviewer", meta.Reviewer)
		printRoleRow("Committer", meta.Committer)
		printRoleRow("Harness", meta.Harness)
		if len(meta.Tools) > 0 {
			printRoleRow("Tools", strings.Join(meta.Tools, ", "))
		}
	}

	fmt.Fprintf(a.stdout, "\n  %s\n", p.bold("Git Identity Preview"))
	fmt.Fprintf(a.stdout, "    %-14s %s\n", p.cyan("Git Author"), p.boldGreen(authorPreview))
	fmt.Fprintf(a.stdout, "    %-14s %s %s\n", p.cyan("Git Committer"), committerPreview, p.dim("(untouched)"))

	trailers := meta.Trailers()
	fmt.Fprintf(a.stdout, "\n  %s\n", p.bold("Git Trailers Preview"))
	if len(trailers) == 0 {
		fmt.Fprintf(a.stdout, "    %s\n", p.dim("(none)"))
	} else {
		for _, t := range trailers {
			// Keep full "Key: Value" contiguous inside a single color span for clean grep/assertions.
			fmt.Fprintf(a.stdout, "    %s\n", p.yellow(t.String()))
		}
	}

	fmt.Fprintf(a.stdout, "%s\n", border)
	return 0
}

func formatIdentityPreview(name, email string) string {
	switch {
	case name != "" && email != "":
		return fmt.Sprintf("%s <%s>", name, email)
	case name != "":
		return name
	case email != "":
		return fmt.Sprintf("<%s>", email)
	default:
		return "<unconfigured>"
	}
}

func (a *App) cmdClear(args []string) int {
	for _, tok := range args {
		switch tok {
		case "-h", "--help":
			a.printClearHelp(a.stdout)
			return 0
		default:
			fmt.Fprintf(a.stderr, "git-by clear: unexpected argument %q\n", tok)
			return 1
		}
	}

	repo, err := git.Discover(a.workDir)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by clear: %v\n", err)
		return 1
	}

	sessionPath, err := repo.SessionPath()
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by clear: %v\n", err)
		return 1
	}

	if err := session.Clear(sessionPath); err != nil {
		fmt.Fprintf(a.stderr, "git-by clear: %v\n", err)
		return 1
	}

	p := ansiPalette{enabled: a.useColor(false)}
	fmt.Fprintf(a.stdout, "%s Cleared staged git-by session.\n", p.boldGreen("✓"))
	return 0
}

func (a *App) cmdHook(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		a.printHookHelp(a.stdout)
		return 0
	}

	action := args[0]
	rest := args[1:]

	switch action {
	case "install", "uninstall":
		global := false
		binCmd := ""
		for i := 0; i < len(rest); i++ {
			tok := rest[i]
			switch {
			case tok == "-h" || tok == "--help":
				a.printHookHelp(a.stdout)
				return 0
			case tok == "--global":
				global = true
			case tok == "--bin":
				if i+1 >= len(rest) {
					fmt.Fprintf(a.stderr, "git-by hook %s: --bin requires a value\n", action)
					return 1
				}
				i++
				binCmd = strings.TrimSpace(rest[i])
			case strings.HasPrefix(tok, "--bin="):
				binCmd = strings.TrimSpace(strings.TrimPrefix(tok, "--bin="))
			default:
				fmt.Fprintf(a.stderr, "git-by hook %s: unknown argument %q\n", action, tok)
				return 1
			}
		}

		scope := git.HookScope{
			Global: global,
			BinCmd: binCmd,
		}
		if action == "install" {
			hooksDir, err := git.InstallHooks(a.workDir, scope)
			if err != nil {
				fmt.Fprintf(a.stderr, "git-by hook install: %v\n", err)
				return 1
			}
			fmt.Fprintf(a.stdout, "Installed git-by hooks in %s\n", hooksDir)
			return 0
		}
		hooksDir, err := git.UninstallHooks(a.workDir, scope)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by hook uninstall: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.stdout, "Uninstalled git-by hooks from %s\n", hooksDir)
		return 0

	case "run":
		if len(rest) == 0 {
			fmt.Fprintln(a.stderr, "git-by hook run: missing hook name")
			return 1
		}
		return a.cmdHookRun(rest[0], rest[1:])

	default:
		fmt.Fprintf(a.stderr, "git-by hook: unknown subcommand %q\n", action)
		a.printHookHelp(a.stderr)
		return 1
	}
}

func (a *App) cmdHookRun(hookName string, hookArgs []string) int {
	if active, ok := a.lookupEnv("GIT_BY_ACTIVE"); ok && active == "1" {
		return 0
	}

	switch hookName {
	case "prepare-commit-msg":
		if len(hookArgs) < 1 || strings.TrimSpace(hookArgs[0]) == "" {
			return 0
		}
		msgFile := hookArgs[0]

		repo, err := git.Discover(a.workDir)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by hook prepare-commit-msg: %v\n", err)
			return 1
		}
		sessionPath, err := repo.SessionPath()
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by hook prepare-commit-msg: %v\n", err)
			return 1
		}
		fileMeta, _, err := session.Load(sessionPath)
		if err != nil {
			fmt.Fprintf(a.stderr, "git-by hook prepare-commit-msg: %v\n", err)
			return 1
		}
		envMeta := session.FromEnv(a.lookupEnv)
		meta := session.Merge(fileMeta, envMeta)

		rawTrailers := meta.Trailers()
		if len(rawTrailers) == 0 {
			return 0
		}
		trailers := make([]string, 0, len(rawTrailers))
		for _, t := range rawTrailers {
			trailers = append(trailers, t.String())
		}
		if err := git.ApplyTrailersToFile(repo.WorkDir, msgFile, trailers); err != nil {
			fmt.Fprintf(a.stderr, "git-by hook prepare-commit-msg: %v\n", err)
			return 1
		}
		return 0

	case "post-commit":
		repo, err := git.Discover(a.workDir)
		if err != nil {
			return 0
		}
		sessionPath, err := repo.SessionPath()
		if err != nil {
			return 0
		}
		if err := session.Clear(sessionPath); err != nil {
			fmt.Fprintf(a.stderr, "git-by hook post-commit: warning: %v\n", err)
		}
		return 0

	default:
		return 0
	}
}

func (a *App) cmdStats(args []string) int {
	var (
		since     string
		jsonOut   bool
		badgePath string
		theme     = stats.ThemeAuto
		topN      = 5
		noColor   bool
	)

	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-h" || tok == "--help":
			a.printStatsHelp(a.stdout)
			return 0
		case tok == "--json":
			jsonOut = true
		case tok == "--no-color":
			noColor = true
		case tok == "--since":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by stats: --since requires a value")
				return 1
			}
			i++
			since = strings.TrimSpace(args[i])
		case strings.HasPrefix(tok, "--since="):
			since = strings.TrimSpace(strings.TrimPrefix(tok, "--since="))
		case tok == "--badge":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by stats: --badge requires an output path")
				return 1
			}
			i++
			badgePath = strings.TrimSpace(args[i])
			if badgePath == "" {
				fmt.Fprintln(a.stderr, "git-by stats: --badge requires a non-empty output path")
				return 1
			}
		case strings.HasPrefix(tok, "--badge="):
			badgePath = strings.TrimSpace(strings.TrimPrefix(tok, "--badge="))
			if badgePath == "" {
				fmt.Fprintln(a.stderr, "git-by stats: --badge requires a non-empty output path")
				return 1
			}
		case tok == "--theme":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by stats: --theme requires a value (auto, dark, or light)")
				return 1
			}
			i++
			raw := strings.TrimSpace(args[i])
			if raw == "" {
				fmt.Fprintln(a.stderr, "git-by stats: --theme requires a non-empty value (auto, dark, or light)")
				return 1
			}
			parsed, err := stats.ParseBadgeTheme(raw)
			if err != nil {
				fmt.Fprintf(a.stderr, "git-by stats: %v\n", err)
				return 1
			}
			theme = parsed
		case strings.HasPrefix(tok, "--theme="):
			raw := strings.TrimSpace(strings.TrimPrefix(tok, "--theme="))
			if raw == "" {
				fmt.Fprintln(a.stderr, "git-by stats: --theme requires a non-empty value (auto, dark, or light)")
				return 1
			}
			parsed, err := stats.ParseBadgeTheme(raw)
			if err != nil {
				fmt.Fprintf(a.stderr, "git-by stats: %v\n", err)
				return 1
			}
			theme = parsed
		case tok == "--top":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "git-by stats: --top requires an integer value")
				return 1
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				fmt.Fprintf(a.stderr, "git-by stats: --top must be a positive integer, got %q\n", args[i])
				return 1
			}
			topN = n
		case strings.HasPrefix(tok, "--top="):
			raw := strings.TrimPrefix(tok, "--top=")
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				fmt.Fprintf(a.stderr, "git-by stats: --top must be a positive integer, got %q\n", raw)
				return 1
			}
			topN = n
		default:
			fmt.Fprintf(a.stderr, "git-by stats: unknown argument %q\n", tok)
			return 1
		}
	}

	repo, err := git.Discover(a.workDir)
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by stats: %v\n", err)
		return 1
	}

	report, err := stats.Collect(repo.WorkDir, stats.Options{
		Since: since,
		TopN:  topN,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "git-by stats: %v\n", err)
		return 1
	}

	if badgePath != "" {
		resolvedBadge := badgePath
		if !filepath.IsAbs(resolvedBadge) {
			resolvedBadge = filepath.Join(a.workDir, resolvedBadge)
		}
		if dir := filepath.Dir(resolvedBadge); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(a.stderr, "git-by stats: failed to create badge directory: %v\n", err)
				return 1
			}
		}
		if err := stats.WriteBadgeSVGWithTheme(resolvedBadge, report, theme); err != nil {
			fmt.Fprintf(a.stderr, "git-by stats: failed to write SVG badge: %v\n", err)
			return 1
		}
	}

	if jsonOut {
		if err := stats.RenderJSON(a.stdout, report); err != nil {
			fmt.Fprintf(a.stderr, "git-by stats: failed to render JSON: %v\n", err)
			return 1
		}
		return 0
	}

	if err := stats.RenderTerminal(a.stdout, report, a.useColor(noColor)); err != nil {
		fmt.Fprintf(a.stderr, "git-by stats: failed to render stats: %v\n", err)
		return 1
	}
	if badgePath != "" {
		fmt.Fprintf(a.stdout, "\nSaved SVG badge to %s\n", badgePath)
	}
	return 0
}

func (a *App) cmdInitSkill(args []string) int {
	for _, tok := range args {
		switch tok {
		case "-h", "--help":
			a.printInitSkillHelp(a.stdout)
			return 0
		default:
			fmt.Fprintf(a.stderr, "git-by init-skill: unexpected argument %q\n", tok)
			return 1
		}
	}

	fmt.Fprint(a.stdout, setup.RenderManagedBlock("")+"\n")
	return 0
}
