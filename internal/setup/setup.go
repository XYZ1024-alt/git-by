package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MarkerStart = "<!-- git-by:start -->"
	MarkerEnd   = "<!-- git-by:end -->"
)

type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

type Action string

const (
	ActionInstalled Action = "installed"
	ActionUpdated   Action = "updated"
	ActionRemoved   Action = "removed"
	ActionSkipped   Action = "skipped"
)

type Options struct {
	Scope             Scope    // ScopeGlobal (default if empty) or ScopeProject
	HomeDir           string   // required for ScopeGlobal
	DSHHome           string   // optional $DSH_HOME override for DeepSeek Harness (defaults to ~/.dsh)
	ProjectRoot       string   // required for ScopeProject
	Agents            []string // optional filter: agy, pi, claude, codex, opencode, dsh, cursor, windsurf
	SelectedTargets   []string // optional interactive picker target IDs
	CreateMissingDirs bool     // create parent directories for explicitly selected global targets
	Uninstall         bool
	DryRun            bool
}

type TargetResult struct {
	AgentID     string
	DisplayName string
	Path        string
	DisplayPath string
	Action      Action
	Reason      string
}

type Summary struct {
	Scope       Scope
	Uninstall   bool
	DryRun      bool
	Results     []TargetResult
	ActiveCount int // number of targets with ActionInstalled, ActionUpdated, or ActionRemoved
}

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[90m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

type targetSpec struct {
	agentID      string
	displayName  string
	path         string
	displayPath  string
	harness      string
	isStandalone bool
	skipReason   string
}

var defaultGlobalAgents = []string{
	"agy",
	"pi",
	"claude",
	"codex",
	"opencode",
	"dsh",
	"cursor",
	"windsurf",
}

// RenderManagedBlock renders the unified git-by provenance instruction block for the given harness.
// The returned string has no trailing newline; callers append a newline when writing to a file.
func RenderManagedBlock(harness string) string {
	h := strings.TrimSpace(harness)
	if h == "" {
		h = "<your-harness-name>"
	}
	lines := []string{
		MarkerStart,
		"## Git Commit Provenance (`git-by`)",
		"",
		"Whenever you participate in code changes or git operations in this workspace, record AI model provenance using `git by` instead of plain `git commit`:",
		"",
		"1. **Model ID Format**: Always identify yourself as `<provider>/<model-id>` (e.g., `anthropic/claude-sonnet-4-5`, `google/gemini-3-pro`, `openai/gpt-5`). Never invent unknown model names.",
		"2. **Multi-Agent Handoff (Non-committing roles)**:",
		"   - If you act as Orchestrator, Planner, Implementer, or Reviewer *without* making the final commit right now, record your contribution before finishing your turn:",
		"     - Orchestrator: `git by set orch <provider>/<model-id> --harness " + h + "`",
		"     - Planner / Architect: `git by set plan <provider>/<model-id> --harness " + h + "`",
		"     - Code Implementer: `git by set impl <provider>/<model-id> --harness " + h + "`",
		"     - Code Reviewer: `git by set rev <provider>/<model-id> --harness " + h + "`",
		"3. **Making Commits (Replaces `git commit`)**:",
		"   - **Single-model workflow** (you wrote the code AND are committing it now):",
		"     Stage files normally (`git add ...`), then run:",
		"     `git by commit --impl <provider>/<model-id> --harness " + h + " -m \"<commit message>\"`",
		"   - **Multi-model handoff workflow** (an earlier model already wrote the code or ran `git by set`, and you are only summarizing/committing):",
		"     `git by commit --committer <provider>/<model-id> --harness " + h + " -m \"<commit message>\"`",
		"   - All standard `git commit` flags (`-a`, `-m`, `--amend`, `-S`, etc.) are passed through natively.",
		"4. **Strict Prohibitions**:",
		"   - NEVER run plain `git commit` when `git by` is available in `$PATH`.",
		"   - NEVER modify `git config user.name` or `user.email`.",
		"   - NEVER append `Co-authored-by` or `Signed-off-by` trailers for AI models.",
		MarkerEnd,
	}
	return strings.Join(lines, "\n")
}

// ParseAgents parses comma-separated agent names/aliases, normalizes them to canonical IDs,
// and deduplicates them while preserving first-seen order.
func ParseAgents(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	const supportedList = "agy, pi, claude, codex, opencode, dsh, cursor, windsurf"

	var out []string
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		if strings.TrimSpace(entry) == "" {
			return nil, fmt.Errorf("agent name cannot be empty (supported: %s)", supportedList)
		}
		parts := strings.Split(entry, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				return nil, fmt.Errorf("agent name cannot be empty (supported: %s)", supportedList)
			}
			canonical, ok := canonicalAgentID(trimmed)
			if !ok {
				return nil, fmt.Errorf("unknown agent %q (supported: %s)", trimmed, supportedList)
			}
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
			out = append(out, canonical)
		}
	}
	return out, nil
}

func canonicalAgentID(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "agy", "antigravity":
		return "agy", true
	case "pi", "pi-agent":
		return "pi", true
	case "claude", "claude-code":
		return "claude", true
	case "codex", "openai-codex":
		return "codex", true
	case "opencode":
		return "opencode", true
	case "dsh", "deepseek-harness":
		return "dsh", true
	case "cursor":
		return "cursor", true
	case "windsurf":
		return "windsurf", true
	default:
		return "", false
	}
}

func agentDisplayName(agentID string) string {
	switch agentID {
	case "agy":
		return "Antigravity"
	case "pi":
		return "Pi Agent"
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "dsh":
		return "DeepSeek Harness"
	case "cursor":
		return "Cursor"
	case "windsurf":
		return "Windsurf"
	default:
		return agentID
	}
}

func agentHarness(agentID string) string {
	switch agentID {
	case "agy":
		return "antigravity"
	case "pi":
		return "pi"
	case "claude":
		return "claude-code"
	case "codex":
		return "codex"
	case "opencode":
		return "opencode"
	case "dsh":
		return "dsh"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	default:
		return ""
	}
}

// Run executes (or previews when DryRun is true) git-by rule installation or uninstallation.
func Run(opts Options) (*Summary, error) {
	scope := opts.Scope
	if scope == "" {
		scope = ScopeGlobal
	}
	if scope != ScopeGlobal && scope != ScopeProject {
		return nil, fmt.Errorf("invalid setup scope %q (expected %q or %q)", opts.Scope, ScopeGlobal, ScopeProject)
	}

	agents, err := ParseAgents(opts.Agents)
	if err != nil {
		return nil, err
	}

	var specs []targetSpec
	createMissing := opts.CreateMissingDirs && !opts.Uninstall
	switch scope {
	case ScopeGlobal:
		homeDir := strings.TrimSpace(opts.HomeDir)
		if homeDir == "" {
			return nil, errors.New("HomeDir is required for global setup")
		}
		homeDir = filepath.Clean(homeDir)
		if !dirExists(homeDir) {
			return nil, fmt.Errorf("home directory %q does not exist", homeDir)
		}
		if len(opts.SelectedTargets) > 0 {
			selectedAgents, err := ParseAgents(opts.SelectedTargets)
			if err != nil {
				return nil, err
			}
			specs = resolveGlobalTargetsWithMode(homeDir, opts.DSHHome, selectedAgents, createMissing)
		} else {
			specs = resolveGlobalTargetsWithMode(homeDir, opts.DSHHome, agents, createMissing)
		}

	case ScopeProject:
		projectRoot := strings.TrimSpace(opts.ProjectRoot)
		if projectRoot == "" {
			return nil, errors.New("ProjectRoot is required for project setup")
		}
		projectRoot = filepath.Clean(projectRoot)
		if !dirExists(projectRoot) {
			return nil, fmt.Errorf("project root %q does not exist", projectRoot)
		}
		if len(opts.SelectedTargets) > 0 {
			var err error
			specs, err = resolveSelectedProjectTargets(projectRoot, opts.SelectedTargets)
			if err != nil {
				return nil, err
			}
		} else {
			specs = resolveProjectTargets(projectRoot, agents, opts.Uninstall)
		}
	}

	summary := &Summary{
		Scope:     scope,
		Uninstall: opts.Uninstall,
		DryRun:    opts.DryRun,
		Results:   make([]TargetResult, 0, len(specs)),
	}

	for _, spec := range specs {
		res, err := applyTarget(spec, opts.Uninstall, opts.DryRun)
		if err != nil {
			return nil, err
		}
		summary.Results = append(summary.Results, res)
		if isActiveAction(res.Action) {
			summary.ActiveCount++
		}
	}

	return summary, nil
}

func resolveGlobalTargets(homeDir string, agents []string) []targetSpec {
	return resolveGlobalTargetsWithMode(homeDir, "", agents, false)
}

func resolveGlobalTargetsWithMode(homeDir string, dshHome string, agents []string, createMissing bool) []targetSpec {
	explicit := len(agents) > 0
	order := agents
	if !explicit {
		order = defaultGlobalAgents
	}

	specs := make([]targetSpec, 0, len(order))
	for _, id := range order {
		spec, include := resolveOneGlobalTargetWithDSH(homeDir, dshHome, id, explicit)
		if include {
			if createMissing && explicit {
				spec.skipReason = ""
			}
			specs = append(specs, spec)
		}
	}
	return specs
}

func resolveDshHomeDir(homeDir, dshHome string) (string, string) {
	trimmed := strings.TrimSpace(dshHome)
	if trimmed == "" {
		def := filepath.Join(homeDir, ".dsh")
		return def, formatHomeDisplayPath(homeDir, def)
	}
	expanded := trimmed
	switch {
	case trimmed == "~":
		expanded = homeDir
	case strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`):
		expanded = filepath.Join(homeDir, trimmed[2:])
	}
	cleaned := filepath.Clean(expanded)
	return cleaned, formatHomeDisplayPath(homeDir, cleaned)
}

func resolveOneGlobalTarget(homeDir string, agentID string, explicit bool) (targetSpec, bool) {
	return resolveOneGlobalTargetWithDSH(homeDir, "", agentID, explicit)
}

func resolveOneGlobalTargetWithDSH(homeDir string, dshHome string, agentID string, explicit bool) (targetSpec, bool) {
	switch agentID {
	case "agy":
		target := filepath.Join(homeDir, ".gemini", "config", "rules", "git-by.md")
		spec := targetSpec{
			agentID:      "agy",
			displayName:  "Antigravity",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "antigravity",
			isStandalone: true,
		}
		if !dirExists(filepath.Join(homeDir, ".gemini")) {
			spec.skipReason = "(~/.gemini not found)"
		}
		return spec, true

	case "pi":
		target := filepath.Join(homeDir, ".pi", "agent", "APPEND_SYSTEM.md")
		spec := targetSpec{
			agentID:      "pi",
			displayName:  "Pi Agent",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "pi",
			isStandalone: false,
		}
		if !dirExists(filepath.Join(homeDir, ".pi")) {
			spec.skipReason = "(~/.pi not found)"
		}
		return spec, true

	case "claude":
		target := filepath.Join(homeDir, ".claude", "rules", "git-by.md")
		spec := targetSpec{
			agentID:      "claude",
			displayName:  "Claude Code",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "claude-code",
			isStandalone: true,
		}
		if !dirExists(filepath.Join(homeDir, ".claude")) {
			spec.skipReason = "(~/.claude not found)"
		}
		return spec, true

	case "codex":
		target := filepath.Join(homeDir, ".codex", "AGENTS.md")
		spec := targetSpec{
			agentID:      "codex",
			displayName:  "Codex",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "codex",
			isStandalone: false,
		}
		if !dirExists(filepath.Join(homeDir, ".codex")) {
			spec.skipReason = "(~/.codex not found)"
		}
		return spec, true

	case "opencode":
		target := filepath.Join(homeDir, ".config", "opencode", "AGENTS.md")
		spec := targetSpec{
			agentID:      "opencode",
			displayName:  "OpenCode",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "opencode",
			isStandalone: false,
		}
		if !dirExists(filepath.Join(homeDir, ".config", "opencode")) {
			spec.skipReason = "(~/.config/opencode not found)"
		}
		return spec, true

	case "dsh":
		dshDir, dshDisplayDir := resolveDshHomeDir(homeDir, dshHome)
		target := filepath.Join(dshDir, "AGENTS.md")
		spec := targetSpec{
			agentID:      "dsh",
			displayName:  "DeepSeek Harness",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "dsh",
			isStandalone: false,
		}
		if !dirExists(dshDir) {
			spec.skipReason = fmt.Sprintf("(%s not found)", dshDisplayDir)
		}
		return spec, true

	case "cursor":
		dotCursorRules := filepath.Join(homeDir, ".cursorrules")
		standaloneRule := filepath.Join(homeDir, ".cursor", "rules", "git-by.md")
		if !explicit {
			if fileExists(dotCursorRules) {
				return targetSpec{
					agentID:      "cursor",
					displayName:  "Cursor",
					path:         dotCursorRules,
					displayPath:  formatHomeDisplayPath(homeDir, dotCursorRules),
					harness:      "cursor",
					isStandalone: false,
				}, true
			}
			if fileExists(standaloneRule) {
				return targetSpec{
					agentID:      "cursor",
					displayName:  "Cursor",
					path:         standaloneRule,
					displayPath:  formatHomeDisplayPath(homeDir, standaloneRule),
					harness:      "cursor",
					isStandalone: true,
				}, true
			}
			return targetSpec{}, false
		}

		if fileExists(dotCursorRules) {
			return targetSpec{
				agentID:      "cursor",
				displayName:  "Cursor",
				path:         dotCursorRules,
				displayPath:  formatHomeDisplayPath(homeDir, dotCursorRules),
				harness:      "cursor",
				isStandalone: false,
			}, true
		}
		if dirExists(filepath.Join(homeDir, ".cursor")) {
			return targetSpec{
				agentID:      "cursor",
				displayName:  "Cursor",
				path:         standaloneRule,
				displayPath:  formatHomeDisplayPath(homeDir, standaloneRule),
				harness:      "cursor",
				isStandalone: true,
			}, true
		}
		return targetSpec{
			agentID:      "cursor",
			displayName:  "Cursor",
			path:         dotCursorRules,
			displayPath:  formatHomeDisplayPath(homeDir, dotCursorRules),
			harness:      "cursor",
			isStandalone: false,
		}, true

	case "windsurf":
		dotWindsurfRules := filepath.Join(homeDir, ".windsurfrules")
		memoriesRules := filepath.Join(homeDir, ".codeium", "windsurf", "memories", "global_rules.md")
		if !explicit {
			if fileExists(dotWindsurfRules) {
				return targetSpec{
					agentID:      "windsurf",
					displayName:  "Windsurf",
					path:         dotWindsurfRules,
					displayPath:  formatHomeDisplayPath(homeDir, dotWindsurfRules),
					harness:      "windsurf",
					isStandalone: false,
				}, true
			}
			if fileExists(memoriesRules) {
				return targetSpec{
					agentID:      "windsurf",
					displayName:  "Windsurf",
					path:         memoriesRules,
					displayPath:  formatHomeDisplayPath(homeDir, memoriesRules),
					harness:      "windsurf",
					isStandalone: false,
				}, true
			}
			return targetSpec{}, false
		}

		if fileExists(dotWindsurfRules) {
			return targetSpec{
				agentID:      "windsurf",
				displayName:  "Windsurf",
				path:         dotWindsurfRules,
				displayPath:  formatHomeDisplayPath(homeDir, dotWindsurfRules),
				harness:      "windsurf",
				isStandalone: false,
			}, true
		}
		if dirExists(filepath.Join(homeDir, ".codeium", "windsurf")) {
			return targetSpec{
				agentID:      "windsurf",
				displayName:  "Windsurf",
				path:         memoriesRules,
				displayPath:  formatHomeDisplayPath(homeDir, memoriesRules),
				harness:      "windsurf",
				isStandalone: false,
			}, true
		}
		return targetSpec{
			agentID:      "windsurf",
			displayName:  "Windsurf",
			path:         dotWindsurfRules,
			displayPath:  formatHomeDisplayPath(homeDir, dotWindsurfRules),
			harness:      "windsurf",
			isStandalone: false,
		}, true
	}
	return targetSpec{}, false
}

func resolveProjectTargets(projectRoot string, agents []string, uninstall bool) []targetSpec {
	if len(agents) == 0 {
		var specs []targetSpec
		agentsMD := filepath.Join(projectRoot, "AGENTS.md")
		if !uninstall || fileExists(agentsMD) {
			specs = append(specs, targetSpec{
				agentID:      "agents",
				displayName:  "AGENTS.md",
				path:         agentsMD,
				displayPath:  "AGENTS.md",
				harness:      "",
				isStandalone: false,
			})
		}

		optionalFiles := []struct {
			agentID     string
			displayName string
			relPath     string
			harness     string
		}{
			{agentID: "claude", displayName: "Claude Code", relPath: "CLAUDE.md", harness: "claude-code"},
			{agentID: "cursor", displayName: "Cursor", relPath: ".cursorrules", harness: "cursor"},
			{agentID: "windsurf", displayName: "Windsurf", relPath: ".windsurfrules", harness: "windsurf"},
		}
		for _, opt := range optionalFiles {
			full := filepath.Join(projectRoot, opt.relPath)
			if fileExists(full) {
				specs = append(specs, targetSpec{
					agentID:      opt.agentID,
					displayName:  opt.displayName,
					path:         full,
					displayPath:  opt.relPath,
					harness:      opt.harness,
					isStandalone: false,
				})
			}
		}
		return specs
	}

	// Count how many requested agents map to AGENTS.md so we know whether to use a specific harness or generic placeholder.
	var agentsMDMapped []string
	for _, id := range agents {
		switch id {
		case "agy", "pi", "codex", "opencode", "dsh":
			agentsMDMapped = append(agentsMDMapped, id)
		}
	}

	var specs []targetSpec
	addedAgentsMD := false
	for _, id := range agents {
		switch id {
		case "claude":
			specs = append(specs, targetSpec{
				agentID:      "claude",
				displayName:  "Claude Code",
				path:         filepath.Join(projectRoot, "CLAUDE.md"),
				displayPath:  "CLAUDE.md",
				harness:      "claude-code",
				isStandalone: false,
			})
		case "cursor":
			specs = append(specs, targetSpec{
				agentID:      "cursor",
				displayName:  "Cursor",
				path:         filepath.Join(projectRoot, ".cursorrules"),
				displayPath:  ".cursorrules",
				harness:      "cursor",
				isStandalone: false,
			})
		case "windsurf":
			specs = append(specs, targetSpec{
				agentID:      "windsurf",
				displayName:  "Windsurf",
				path:         filepath.Join(projectRoot, ".windsurfrules"),
				displayPath:  ".windsurfrules",
				harness:      "windsurf",
				isStandalone: false,
			})
		case "agy", "pi", "codex", "opencode", "dsh":
			if addedAgentsMD {
				continue
			}
			addedAgentsMD = true
			spec := targetSpec{
				agentID:      "agents",
				displayName:  "AGENTS.md",
				path:         filepath.Join(projectRoot, "AGENTS.md"),
				displayPath:  "AGENTS.md",
				harness:      "",
				isStandalone: false,
			}
			if len(agentsMDMapped) == 1 {
				only := agentsMDMapped[0]
				spec.agentID = only
				spec.displayName = agentDisplayName(only)
				spec.harness = agentHarness(only)
			}
			specs = append(specs, spec)
		}
	}
	return specs
}

func applyTarget(spec targetSpec, uninstall bool, dryRun bool) (TargetResult, error) {
	res := TargetResult{
		AgentID:     spec.agentID,
		DisplayName: spec.displayName,
		Path:        spec.path,
		DisplayPath: spec.displayPath,
	}

	if spec.skipReason != "" {
		res.Action = ActionSkipped
		res.Reason = spec.skipReason
		return res, nil
	}

	if uninstall {
		action, reason, err := uninstallTarget(spec, dryRun)
		if err != nil {
			return TargetResult{}, err
		}
		res.Action = action
		res.Reason = reason
		return res, nil
	}

	action, err := upsertTarget(spec, dryRun)
	if err != nil {
		return TargetResult{}, err
	}
	res.Action = action
	return res, nil
}

func upsertTarget(spec targetSpec, dryRun bool) (Action, error) {
	rawBlock := RenderManagedBlock(spec.harness)

	origBytes, err := os.ReadFile(spec.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read %s: %w", spec.path, err)
		}
		if !dryRun {
			if err := atomicWriteFile(spec.path, []byte(rawBlock+"\n")); err != nil {
				return "", err
			}
		}
		return ActionInstalled, nil
	}

	content := string(origBytes)
	startIdx, endEnd, hasMarkers := findManagedRange(content)

	var newContent string
	var action Action

	if hasMarkers {
		before := content[:startIdx]
		after := content[endEnd:]
		block := rawBlock
		if strings.Contains(before+after, "\r\n") {
			block = strings.ReplaceAll(rawBlock, "\n", "\r\n")
		}
		newContent = before + block + after
		action = ActionUpdated
	} else if len(origBytes) == 0 {
		newContent = rawBlock + "\n"
		if spec.isStandalone {
			action = ActionInstalled
		} else {
			action = ActionUpdated
		}
	} else {
		eol := "\n"
		block := rawBlock
		if strings.Contains(content, "\r\n") {
			eol = "\r\n"
			block = strings.ReplaceAll(rawBlock, "\n", "\r\n")
		}
		if strings.HasSuffix(content, "\n") {
			newContent = content + eol + block + eol
		} else {
			newContent = content + eol + eol + block + eol
		}
		action = ActionUpdated
	}

	if !dryRun && !bytes.Equal(origBytes, []byte(newContent)) {
		if err := atomicWriteFile(spec.path, []byte(newContent)); err != nil {
			return "", err
		}
	}

	return action, nil
}

func uninstallTarget(spec targetSpec, dryRun bool) (Action, string, error) {
	origBytes, err := os.ReadFile(spec.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ActionSkipped, "(not configured)", nil
		}
		return "", "", fmt.Errorf("read %s: %w", spec.path, err)
	}

	content := string(origBytes)
	startIdx, endEnd, hasMarkers := findManagedRange(content)
	if !hasMarkers {
		return ActionSkipped, "(not configured)", nil
	}

	before := content[:startIdx]
	after := content[endEnd:]

	if spec.isStandalone || strings.TrimSpace(before+after) == "" {
		if !dryRun {
			if err := os.Remove(spec.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", "", fmt.Errorf("remove %s: %w", spec.path, err)
			}
		}
		return ActionRemoved, "", nil
	}

	restored := stripManagedBlock(before, after)
	if !dryRun {
		if err := atomicWriteFile(spec.path, []byte(restored)); err != nil {
			return "", "", err
		}
	}
	return ActionRemoved, "", nil
}

func findManagedRange(content string) (startIdx int, endEnd int, ok bool) {
	startIdx = strings.Index(content, MarkerStart)
	if startIdx < 0 {
		return -1, -1, false
	}
	searchFrom := startIdx + len(MarkerStart)
	relEnd := strings.Index(content[searchFrom:], MarkerEnd)
	if relEnd < 0 {
		return -1, -1, false
	}
	endEnd = searchFrom + relEnd + len(MarkerEnd)
	return startIdx, endEnd, true
}

func stripManagedBlock(before, after string) string {
	// Case 1: block at end of file.
	if after == "" || after == "\n" || after == "\r\n" {
		if strings.HasSuffix(before, "\r\n\r\n") {
			return before[:len(before)-2]
		}
		if strings.HasSuffix(before, "\n\n") {
			return before[:len(before)-1]
		}
		return before
	}

	// Case 2: block at top of file.
	if before == "" {
		switch {
		case strings.HasPrefix(after, "\r\n\r\n"):
			return after[4:]
		case strings.HasPrefix(after, "\n\n"):
			return after[2:]
		case strings.HasPrefix(after, "\r\n"):
			return after[2:]
		case strings.HasPrefix(after, "\n"):
			return after[1:]
		default:
			return after
		}
	}

	// Case 3: block in middle of file.
	remAfter := after
	switch {
	case strings.HasPrefix(remAfter, "\r\n"):
		remAfter = remAfter[2:]
	case strings.HasPrefix(remAfter, "\n"):
		remAfter = remAfter[1:]
	}

	if strings.HasSuffix(before, "\r\n\r\n") && strings.HasPrefix(remAfter, "\r\n") {
		remAfter = remAfter[2:]
	} else if strings.HasSuffix(before, "\n\n") && strings.HasPrefix(remAfter, "\n") {
		remAfter = remAfter[1:]
	}

	return before + remAfter
}

func atomicWriteFile(path string, data []byte) error {
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".git-by-setup-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmpFile.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp file %s: %w", tmpName, err)
	}
	if err := tmpFile.Chmod(perm); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temp file %s: %w", tmpName, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	renamed = true
	return nil
}

// RenderSummary writes a human-readable setup summary table to w.
func RenderSummary(w io.Writer, s *Summary, color bool) error {
	if w == nil {
		return errors.New("nil writer")
	}
	norm := Summary{Scope: ScopeGlobal}
	if s != nil {
		norm = *s
		if norm.Scope == "" {
			norm.Scope = ScopeGlobal
		}
	}

	activeCount := norm.ActiveCount
	if activeCount == 0 && len(norm.Results) > 0 {
		for _, r := range norm.Results {
			if isActiveAction(r.Action) {
				activeCount++
			}
		}
	}

	var buf bytes.Buffer

	header := fmt.Sprintf("git-by setup (%s)", norm.Scope)
	if color {
		header = wrapANSI(ansiBold, header)
	}
	if norm.DryRun {
		tag := "[dry-run]"
		if color {
			tag = wrapANSI(ansiYellow, tag)
		}
		header += " " + tag
	}
	fmt.Fprintln(&buf, header)
	fmt.Fprintln(&buf)

	if len(norm.Results) == 0 {
		msg := "  (no configured targets found)"
		if color {
			msg = wrapANSI(ansiDim, msg)
		}
		fmt.Fprintln(&buf, msg)
	} else {
		nameWidth := 11
		actionWidth := 9
		for _, r := range norm.Results {
			if n := utf8.RuneCountInString(r.DisplayName); n > nameWidth {
				nameWidth = n
			}
			if n := utf8.RuneCountInString(string(r.Action)); n > actionWidth {
				actionWidth = n
			}
		}

		for _, r := range norm.Results {
			icon := "✔"
			iconColor := ansiGreen
			actionColor := ansiGreen
			detail := r.DisplayPath
			if detail == "" {
				detail = r.Path
			}
			detailColor := ""

			switch r.Action {
			case ActionInstalled:
				icon = "✔"
				iconColor = ansiGreen
				actionColor = ansiGreen
			case ActionUpdated:
				icon = "✔"
				iconColor = ansiGreen
				actionColor = ansiCyan
			case ActionRemoved:
				icon = "✔"
				iconColor = ansiGreen
				actionColor = ansiYellow
			case ActionSkipped:
				icon = "-"
				iconColor = ansiDim
				actionColor = ansiDim
				detailColor = ansiDim
				if r.Reason != "" {
					detail = r.Reason
				}
			}

			namePad := strings.Repeat(" ", max(0, nameWidth-utf8.RuneCountInString(r.DisplayName)))
			actionStr := string(r.Action)
			actionPad := strings.Repeat(" ", max(0, actionWidth-utf8.RuneCountInString(actionStr)))

			renderedIcon := icon
			renderedName := r.DisplayName
			renderedAction := actionStr
			renderedDetail := detail

			if color {
				renderedIcon = wrapANSI(iconColor, icon)
				if r.Action == ActionSkipped {
					renderedName = wrapANSI(ansiDim, r.DisplayName)
				}
				renderedAction = wrapANSI(actionColor, actionStr)
				if detailColor != "" {
					renderedDetail = wrapANSI(detailColor, detail)
				}
			}

			fmt.Fprintf(&buf, "  %s %s%s   %s%s   %s\n",
				renderedIcon,
				renderedName,
				namePad,
				renderedAction,
				actionPad,
				renderedDetail,
			)
		}
	}

	fmt.Fprintln(&buf)
	fmt.Fprintln(&buf, renderFooter(norm.Scope, norm.Uninstall, norm.DryRun, activeCount))

	_, err := w.Write(buf.Bytes())
	return err
}

func renderFooter(scope Scope, uninstall bool, dryRun bool, activeCount int) string {
	if dryRun {
		if uninstall {
			return fmt.Sprintf("Dry run complete: %d target(s) would be removed.", activeCount)
		}
		return fmt.Sprintf("Dry run complete: %d target(s) would be updated.", activeCount)
	}
	if uninstall {
		return fmt.Sprintf("Removed git-by rules from %d target(s).", activeCount)
	}
	unit := "agent(s)"
	if scope == ScopeProject {
		unit = "target(s)"
	}
	if activeCount == 0 {
		return fmt.Sprintf("Configured 0 %s.", unit)
	}
	return fmt.Sprintf("Configured %d %s. Your AI commits will now carry provenance automatically.", activeCount, unit)
}

func wrapANSI(code, text string) string {
	if code == "" || text == "" {
		return text
	}
	return code + text + ansiReset
}

func isActiveAction(a Action) bool {
	return a == ActionInstalled || a == ActionUpdated || a == ActionRemoved
}

func formatHomeDisplayPath(homeDir, targetPath string) string {
	rel, err := filepath.Rel(homeDir, targetPath)
	if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "~/" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(targetPath)
}

func resolveSelectedProjectTargets(projectRoot string, selected []string) ([]targetSpec, error) {
	seen := make(map[string]struct{}, len(selected))
	specs := make([]targetSpec, 0, len(selected))

	for _, raw := range selected {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "agents.md" {
			id = "agents"
		}
		if id != "agents" {
			canonical, ok := canonicalAgentID(id)
			if !ok {
				return nil, fmt.Errorf("unknown project target %q", raw)
			}
			id = canonical
		}

		switch id {
		case "agents":
			if _, dup := seen["agents"]; dup {
				continue
			}
			seen["agents"] = struct{}{}
			specs = append(specs, targetSpec{
				agentID:      "agents",
				displayName:  "AGENTS.md",
				path:         filepath.Join(projectRoot, "AGENTS.md"),
				displayPath:  "AGENTS.md",
				harness:      "",
				isStandalone: false,
			})
		case "claude":
			if _, dup := seen["claude"]; dup {
				continue
			}
			seen["claude"] = struct{}{}
			specs = append(specs, targetSpec{
				agentID:      "claude",
				displayName:  "Claude Code",
				path:         filepath.Join(projectRoot, "CLAUDE.md"),
				displayPath:  "CLAUDE.md",
				harness:      "claude-code",
				isStandalone: false,
			})
		case "cursor":
			if _, dup := seen["cursor"]; dup {
				continue
			}
			seen["cursor"] = struct{}{}
			specs = append(specs, targetSpec{
				agentID:      "cursor",
				displayName:  "Cursor",
				path:         filepath.Join(projectRoot, ".cursorrules"),
				displayPath:  ".cursorrules",
				harness:      "cursor",
				isStandalone: false,
			})
		case "windsurf":
			if _, dup := seen["windsurf"]; dup {
				continue
			}
			seen["windsurf"] = struct{}{}
			specs = append(specs, targetSpec{
				agentID:      "windsurf",
				displayName:  "Windsurf",
				path:         filepath.Join(projectRoot, ".windsurfrules"),
				displayPath:  ".windsurfrules",
				harness:      "windsurf",
				isStandalone: false,
			})
		case "agy", "pi", "codex", "opencode", "dsh":
			if _, dup := seen["agents"]; dup {
				continue
			}
			seen["agents"] = struct{}{}
			specs = append(specs, targetSpec{
				agentID:      "agents",
				displayName:  "AGENTS.md",
				path:         filepath.Join(projectRoot, "AGENTS.md"),
				displayPath:  "AGENTS.md",
				harness:      "",
				isStandalone: false,
			})
		}
	}
	return specs, nil
}

func fileHasMarkers(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, _, ok := findManagedRange(string(b))
	return ok
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
