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
	PiAgentDir        string   // optional $PI_CODING_AGENT_DIR override for Pi Agent (defaults to ~/.pi/agent)
	OmpAgentDir       string   // optional $OMP_CODING_AGENT_DIR override for Oh My Pi (defaults to ~/.omp/agent)
	CodexHome         string   // optional $CODEX_HOME override for OpenAI Codex (defaults to ~/.codex)
	CopilotHome       string   // optional $COPILOT_HOME override for GitHub Copilot (defaults to ~/.copilot)
	ClineDir          string   // optional $CLINE_DIR override for Cline (defaults to ~/.cline)
	KimiHome          string   // optional $KIMI_CODE_HOME override for Kimi Code (defaults to ~/.kimi-code)
	HermesHome        string   // optional $HERMES_HOME override for Hermes Agent (defaults to ~/.hermes)
	ProjectRoot       string   // required for ScopeProject
	Agents            []string // optional filter across supported mainstream harnesses
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
	"omp",
	"gemini",
	"copilot",
	"cline",
	"roo",
	"kilo",
	"droid",
	"amp",
	"goose",
	"crush",
	"kiro",
	"continue",
	"trae",
	"augment",
	"qwen",
	"kimi",
	"codebuddy",
	"junie",
	"zed",
	"aider",
	"openhands",
	"hermes",
}

// SupportedAgentIDs returns a copy of all canonical agent harness IDs supported by setup.
func SupportedAgentIDs() []string {
	out := make([]string, len(defaultGlobalAgents))
	copy(out, defaultGlobalAgents)
	return out
}

type globalEnvDirs struct {
	dshHome     string
	piAgentDir  string
	ompAgentDir string
	codexHome   string
	copilotHome string
	clineDir    string
	kimiHome    string
	hermesHome  string
}

func globalEnvFromOptions(opts Options) globalEnvDirs {
	return globalEnvDirs{
		dshHome:     opts.DSHHome,
		piAgentDir:  opts.PiAgentDir,
		ompAgentDir: opts.OmpAgentDir,
		codexHome:   opts.CodexHome,
		copilotHome: opts.CopilotHome,
		clineDir:    opts.ClineDir,
		kimiHome:    opts.KimiHome,
		hermesHome:  opts.HermesHome,
	}
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
	supportedList := strings.Join(defaultGlobalAgents, ", ")

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
	case "pi", "pi-agent", "pi-coding-agent":
		return "pi", true
	case "claude", "claude-code":
		return "claude", true
	case "codex", "openai-codex", "codex-cli":
		return "codex", true
	case "opencode", "open-code":
		return "opencode", true
	case "dsh", "deepseek-harness":
		return "dsh", true
	case "cursor":
		return "cursor", true
	case "windsurf", "codeium-windsurf":
		return "windsurf", true
	case "omp", "oh-my-pi":
		return "omp", true
	case "gemini", "gemini-cli":
		return "gemini", true
	case "copilot", "github-copilot", "gh-copilot", "copilot-cli":
		return "copilot", true
	case "cline":
		return "cline", true
	case "roo", "roo-code", "roocode", "roo-cline":
		return "roo", true
	case "kilo", "kilocode", "kilo-code":
		return "kilo", true
	case "droid", "factory", "factory-droid":
		return "droid", true
	case "amp", "ampcode", "amp-code":
		return "amp", true
	case "goose", "block-goose":
		return "goose", true
	case "crush", "charm-crush":
		return "crush", true
	case "kiro", "kiro-cli", "aws-kiro":
		return "kiro", true
	case "continue", "continue-dev", "cn":
		return "continue", true
	case "trae", "trae-ide":
		return "trae", true
	case "augment", "auggie", "augment-code":
		return "augment", true
	case "qwen", "qwen-code":
		return "qwen", true
	case "kimi", "kimi-code", "kimi-cli":
		return "kimi", true
	case "codebuddy", "codebuddy-code", "tencent-codebuddy":
		return "codebuddy", true
	case "junie", "jetbrains-junie":
		return "junie", true
	case "zed", "zed-editor":
		return "zed", true
	case "aider":
		return "aider", true
	case "openhands", "open-hands":
		return "openhands", true
	case "hermes", "hermes-agent":
		return "hermes", true
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
	case "omp":
		return "Oh My Pi"
	case "gemini":
		return "Gemini CLI"
	case "copilot":
		return "GitHub Copilot"
	case "cline":
		return "Cline"
	case "roo":
		return "Roo Code"
	case "kilo":
		return "Kilo Code"
	case "droid":
		return "Factory Droid"
	case "amp":
		return "Amp"
	case "goose":
		return "Goose"
	case "crush":
		return "Crush"
	case "kiro":
		return "Kiro"
	case "continue":
		return "Continue"
	case "trae":
		return "Trae"
	case "augment":
		return "Augment"
	case "qwen":
		return "Qwen Code"
	case "kimi":
		return "Kimi Code"
	case "codebuddy":
		return "CodeBuddy"
	case "junie":
		return "Junie"
	case "zed":
		return "Zed"
	case "aider":
		return "Aider"
	case "openhands":
		return "OpenHands"
	case "hermes":
		return "Hermes Agent"
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
	case "omp":
		return "omp"
	case "gemini":
		return "gemini-cli"
	case "copilot":
		return "copilot"
	case "cline":
		return "cline"
	case "roo":
		return "roo-code"
	case "kilo":
		return "kilocode"
	case "droid":
		return "droid"
	case "amp":
		return "amp"
	case "goose":
		return "goose"
	case "crush":
		return "crush"
	case "kiro":
		return "kiro"
	case "continue":
		return "continue"
	case "trae":
		return "trae"
	case "augment":
		return "augment"
	case "qwen":
		return "qwen-code"
	case "kimi":
		return "kimi-code"
	case "codebuddy":
		return "codebuddy"
	case "junie":
		return "junie"
	case "zed":
		return "zed"
	case "aider":
		return "aider"
	case "openhands":
		return "openhands"
	case "hermes":
		return "hermes"
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
		env := globalEnvFromOptions(opts)
		if len(opts.SelectedTargets) > 0 {
			selectedAgents, err := ParseAgents(opts.SelectedTargets)
			if err != nil {
				return nil, err
			}
			specs = resolveGlobalTargetsWithEnv(homeDir, env, selectedAgents, createMissing)
		} else {
			specs = resolveGlobalTargetsWithEnv(homeDir, env, agents, createMissing)
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
	return resolveGlobalTargetsWithEnv(homeDir, globalEnvDirs{}, agents, false)
}

func resolveGlobalTargetsWithMode(homeDir string, dshHome string, agents []string, createMissing bool) []targetSpec {
	return resolveGlobalTargetsWithEnv(homeDir, globalEnvDirs{dshHome: dshHome}, agents, createMissing)
}

func resolveGlobalTargetsWithEnv(homeDir string, env globalEnvDirs, agents []string, createMissing bool) []targetSpec {
	explicit := len(agents) > 0
	order := agents
	if !explicit {
		order = defaultGlobalAgents
	}

	specs := make([]targetSpec, 0, len(order))
	for _, id := range order {
		spec, include := resolveOneGlobalTargetWithEnv(homeDir, env, id, explicit)
		if include {
			if createMissing && explicit {
				spec.skipReason = ""
			}
			specs = append(specs, spec)
		}
	}
	return specs
}

func resolveEnvHomeDir(homeDir, override, defaultRel string) (string, string) {
	trimmed := strings.TrimSpace(override)
	if trimmed == "" {
		def := filepath.Join(homeDir, defaultRel)
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

func resolveDshHomeDir(homeDir, dshHome string) (string, string) {
	return resolveEnvHomeDir(homeDir, dshHome, ".dsh")
}

func hasGeminiCLI(homeDir string) bool {
	geminiDir := filepath.Join(homeDir, ".gemini")
	for _, marker := range []string{
		"GEMINI.md",
		"settings.json",
		"oauth_creds.json",
		"google_accounts.json",
		"installation_id",
	} {
		if fileExists(filepath.Join(geminiDir, marker)) {
			return true
		}
	}
	return false
}

func resolveOneGlobalTarget(homeDir string, agentID string, explicit bool) (targetSpec, bool) {
	return resolveOneGlobalTargetWithEnv(homeDir, globalEnvDirs{}, agentID, explicit)
}

func resolveOneGlobalTargetWithDSH(homeDir string, dshHome string, agentID string, explicit bool) (targetSpec, bool) {
	return resolveOneGlobalTargetWithEnv(homeDir, globalEnvDirs{dshHome: dshHome}, agentID, explicit)
}

func resolveOneGlobalTargetWithEnv(homeDir string, env globalEnvDirs, agentID string, explicit bool) (targetSpec, bool) {
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
		if strings.TrimSpace(env.piAgentDir) != "" {
			piDir, piDisplayDir := resolveEnvHomeDir(homeDir, env.piAgentDir, filepath.Join(".pi", "agent"))
			target := filepath.Join(piDir, "APPEND_SYSTEM.md")
			spec := targetSpec{
				agentID:      "pi",
				displayName:  "Pi Agent",
				path:         target,
				displayPath:  formatHomeDisplayPath(homeDir, target),
				harness:      "pi",
				isStandalone: false,
			}
			if !dirExists(piDir) {
				spec.skipReason = fmt.Sprintf("(%s not found)", piDisplayDir)
			}
			return spec, true
		}
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
		codexDir, codexDisplayDir := resolveEnvHomeDir(homeDir, env.codexHome, ".codex")
		target := filepath.Join(codexDir, "AGENTS.md")
		spec := targetSpec{
			agentID:      "codex",
			displayName:  "Codex",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "codex",
			isStandalone: false,
		}
		if !dirExists(codexDir) {
			spec.skipReason = fmt.Sprintf("(%s not found)", codexDisplayDir)
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
		dshDir, dshDisplayDir := resolveDshHomeDir(homeDir, env.dshHome)
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

	case "omp":
		if strings.TrimSpace(env.ompAgentDir) != "" {
			ompDir, ompDisplayDir := resolveEnvHomeDir(homeDir, env.ompAgentDir, filepath.Join(".omp", "agent"))
			target := filepath.Join(ompDir, "APPEND_SYSTEM.md")
			spec := targetSpec{
				agentID:      "omp",
				displayName:  "Oh My Pi",
				path:         target,
				displayPath:  formatHomeDisplayPath(homeDir, target),
				harness:      "omp",
				isStandalone: false,
			}
			if !dirExists(ompDir) {
				spec.skipReason = fmt.Sprintf("(%s not found)", ompDisplayDir)
			}
			return spec, true
		}
		target := filepath.Join(homeDir, ".omp", "agent", "APPEND_SYSTEM.md")
		spec := targetSpec{
			agentID:      "omp",
			displayName:  "Oh My Pi",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "omp",
			isStandalone: false,
		}
		if !dirExists(filepath.Join(homeDir, ".omp")) {
			spec.skipReason = "(~/.omp not found)"
		}
		return spec, true

	case "gemini":
		geminiDir := filepath.Join(homeDir, ".gemini")
		target := filepath.Join(geminiDir, "GEMINI.md")
		spec := targetSpec{
			agentID:      "gemini",
			displayName:  "Gemini CLI",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "gemini-cli",
			isStandalone: false,
		}
		if !dirExists(geminiDir) {
			spec.skipReason = "(~/.gemini not found)"
		} else if !explicit && !hasGeminiCLI(homeDir) {
			spec.skipReason = "(~/.gemini/settings.json not found)"
		}
		return spec, true

	case "copilot":
		copilotDir, copilotDisplayDir := resolveEnvHomeDir(homeDir, env.copilotHome, ".copilot")
		target := filepath.Join(copilotDir, "copilot-instructions.md")
		spec := targetSpec{
			agentID:      "copilot",
			displayName:  "GitHub Copilot",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "copilot",
			isStandalone: false,
		}
		if !dirExists(copilotDir) {
			spec.skipReason = fmt.Sprintf("(%s not found)", copilotDisplayDir)
		}
		return spec, true

	case "cline":
		if strings.TrimSpace(env.clineDir) != "" {
			clineDir, clineDisplayDir := resolveEnvHomeDir(homeDir, env.clineDir, ".cline")
			target := filepath.Join(clineDir, "rules", "git-by.md")
			spec := targetSpec{
				agentID:      "cline",
				displayName:  "Cline",
				path:         target,
				displayPath:  formatHomeDisplayPath(homeDir, target),
				harness:      "cline",
				isStandalone: true,
			}
			if !dirExists(clineDir) {
				spec.skipReason = fmt.Sprintf("(%s not found)", clineDisplayDir)
			}
			return spec, true
		}
		dotCline := filepath.Join(homeDir, ".cline")
		docsCline := filepath.Join(homeDir, "Documents", "Cline")
		target := filepath.Join(dotCline, "rules", "git-by.md")
		skipReason := ""
		if dirExists(dotCline) {
			target = filepath.Join(dotCline, "rules", "git-by.md")
		} else if dirExists(docsCline) {
			target = filepath.Join(docsCline, "Rules", "git-by.md")
		} else {
			skipReason = "(~/.cline not found)"
		}
		return targetSpec{
			agentID:      "cline",
			displayName:  "Cline",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "cline",
			isStandalone: true,
			skipReason:   skipReason,
		}, true

	case "roo":
		rooDir := filepath.Join(homeDir, ".roo")
		target := filepath.Join(rooDir, "rules", "git-by.md")
		spec := targetSpec{
			agentID:      "roo",
			displayName:  "Roo Code",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "roo-code",
			isStandalone: true,
		}
		if !dirExists(rooDir) {
			spec.skipReason = "(~/.roo not found)"
		}
		return spec, true

	case "kilo":
		kiloConfigDir := filepath.Join(homeDir, ".config", "kilo")
		kiloDotDir := filepath.Join(homeDir, ".kilocode")
		configAgents := filepath.Join(kiloConfigDir, "AGENTS.md")
		standaloneRule := filepath.Join(kiloDotDir, "rules", "git-by.md")
		if fileExists(configAgents) || (dirExists(kiloConfigDir) && !fileExists(standaloneRule)) {
			return targetSpec{
				agentID:      "kilo",
				displayName:  "Kilo Code",
				path:         configAgents,
				displayPath:  formatHomeDisplayPath(homeDir, configAgents),
				harness:      "kilocode",
				isStandalone: false,
			}, true
		}
		spec := targetSpec{
			agentID:      "kilo",
			displayName:  "Kilo Code",
			path:         standaloneRule,
			displayPath:  formatHomeDisplayPath(homeDir, standaloneRule),
			harness:      "kilocode",
			isStandalone: true,
		}
		if !dirExists(kiloDotDir) {
			spec.skipReason = "(~/.kilocode not found)"
		}
		return spec, true

	case "droid":
		factoryDir := filepath.Join(homeDir, ".factory")
		target := filepath.Join(factoryDir, "AGENTS.md")
		spec := targetSpec{
			agentID:      "droid",
			displayName:  "Factory Droid",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "droid",
			isStandalone: false,
		}
		if !dirExists(factoryDir) {
			spec.skipReason = "(~/.factory not found)"
		}
		return spec, true

	case "amp":
		ampDir := filepath.Join(homeDir, ".config", "amp")
		target := filepath.Join(ampDir, "AGENTS.md")
		spec := targetSpec{
			agentID:      "amp",
			displayName:  "Amp",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "amp",
			isStandalone: false,
		}
		if !dirExists(ampDir) {
			spec.skipReason = "(~/.config/amp not found)"
		}
		return spec, true

	case "goose":
		gooseDir := filepath.Join(homeDir, ".config", "goose")
		target := filepath.Join(gooseDir, ".goosehints")
		spec := targetSpec{
			agentID:      "goose",
			displayName:  "Goose",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "goose",
			isStandalone: false,
		}
		if !dirExists(gooseDir) {
			spec.skipReason = "(~/.config/goose not found)"
		}
		return spec, true

	case "crush":
		crushDir := filepath.Join(homeDir, ".config", "crush")
		target := filepath.Join(crushDir, "CRUSH.md")
		spec := targetSpec{
			agentID:      "crush",
			displayName:  "Crush",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "crush",
			isStandalone: false,
		}
		if !dirExists(crushDir) {
			spec.skipReason = "(~/.config/crush not found)"
		}
		return spec, true

	case "kiro":
		kiroDir := filepath.Join(homeDir, ".kiro")
		target := filepath.Join(kiroDir, "steering", "git-by.md")
		spec := targetSpec{
			agentID:      "kiro",
			displayName:  "Kiro",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "kiro",
			isStandalone: true,
		}
		if !dirExists(kiroDir) {
			spec.skipReason = "(~/.kiro not found)"
		}
		return spec, true

	case "continue":
		continueDir := filepath.Join(homeDir, ".continue")
		target := filepath.Join(continueDir, "rules", "git-by.md")
		spec := targetSpec{
			agentID:      "continue",
			displayName:  "Continue",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "continue",
			isStandalone: true,
		}
		if !dirExists(continueDir) {
			spec.skipReason = "(~/.continue not found)"
		}
		return spec, true

	case "trae":
		traeDir := filepath.Join(homeDir, ".trae")
		userRulesPlain := filepath.Join(traeDir, "user_rules")
		target := filepath.Join(traeDir, "user_rules.md")
		if fileExists(userRulesPlain) {
			target = userRulesPlain
		} else if dirExists(userRulesPlain) {
			target = filepath.Join(userRulesPlain, "user_rules.md")
		}
		spec := targetSpec{
			agentID:      "trae",
			displayName:  "Trae",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "trae",
			isStandalone: false,
		}
		if !dirExists(traeDir) {
			spec.skipReason = "(~/.trae not found)"
		}
		return spec, true

	case "augment":
		augmentDir := filepath.Join(homeDir, ".augment")
		target := filepath.Join(augmentDir, "rules", "git-by.md")
		spec := targetSpec{
			agentID:      "augment",
			displayName:  "Augment",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "augment",
			isStandalone: true,
		}
		if !dirExists(augmentDir) {
			spec.skipReason = "(~/.augment not found)"
		}
		return spec, true

	case "qwen":
		qwenDir := filepath.Join(homeDir, ".qwen")
		target := filepath.Join(qwenDir, "QWEN.md")
		spec := targetSpec{
			agentID:      "qwen",
			displayName:  "Qwen Code",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "qwen-code",
			isStandalone: false,
		}
		if !dirExists(qwenDir) {
			spec.skipReason = "(~/.qwen not found)"
		}
		return spec, true

	case "kimi":
		if strings.TrimSpace(env.kimiHome) != "" {
			kimiDir, kimiDisplayDir := resolveEnvHomeDir(homeDir, env.kimiHome, ".kimi-code")
			target := filepath.Join(kimiDir, "AGENTS.md")
			spec := targetSpec{
				agentID:      "kimi",
				displayName:  "Kimi Code",
				path:         target,
				displayPath:  formatHomeDisplayPath(homeDir, target),
				harness:      "kimi-code",
				isStandalone: false,
			}
			if !dirExists(kimiDir) {
				spec.skipReason = fmt.Sprintf("(%s not found)", kimiDisplayDir)
			}
			return spec, true
		}
		kimiCodeDir := filepath.Join(homeDir, ".kimi-code")
		legacyKimiDir := filepath.Join(homeDir, ".kimi")
		target := filepath.Join(kimiCodeDir, "AGENTS.md")
		skipReason := ""
		if dirExists(kimiCodeDir) {
			target = filepath.Join(kimiCodeDir, "AGENTS.md")
		} else if dirExists(legacyKimiDir) {
			target = filepath.Join(legacyKimiDir, "AGENTS.md")
		} else {
			skipReason = "(~/.kimi-code not found)"
		}
		return targetSpec{
			agentID:      "kimi",
			displayName:  "Kimi Code",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "kimi-code",
			isStandalone: false,
			skipReason:   skipReason,
		}, true

	case "codebuddy":
		cbDir := filepath.Join(homeDir, ".codebuddy")
		target := filepath.Join(cbDir, "rules", "git-by.md")
		spec := targetSpec{
			agentID:      "codebuddy",
			displayName:  "CodeBuddy",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "codebuddy",
			isStandalone: true,
		}
		if !dirExists(cbDir) {
			spec.skipReason = "(~/.codebuddy not found)"
		}
		return spec, true

	case "junie":
		junieDir := filepath.Join(homeDir, ".junie")
		agentsFile := filepath.Join(junieDir, "AGENTS.md")
		guidelinesFile := filepath.Join(junieDir, "guidelines.md")
		target := agentsFile
		if fileExists(guidelinesFile) && !fileExists(agentsFile) {
			target = guidelinesFile
		}
		spec := targetSpec{
			agentID:      "junie",
			displayName:  "Junie",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "junie",
			isStandalone: false,
		}
		if !dirExists(junieDir) {
			spec.skipReason = "(~/.junie not found)"
		}
		return spec, true

	case "zed":
		zedDir := filepath.Join(homeDir, ".config", "zed")
		target := filepath.Join(zedDir, "AGENTS.md")
		spec := targetSpec{
			agentID:      "zed",
			displayName:  "Zed",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "zed",
			isStandalone: false,
		}
		if !dirExists(zedDir) {
			spec.skipReason = "(~/.config/zed not found)"
		}
		return spec, true

	case "aider":
		aiderDir := filepath.Join(homeDir, ".aider")
		target := filepath.Join(aiderDir, "CONVENTIONS.md")
		spec := targetSpec{
			agentID:      "aider",
			displayName:  "Aider",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "aider",
			isStandalone: false,
		}
		if !dirExists(aiderDir) {
			spec.skipReason = "(~/.aider not found)"
		}
		return spec, true

	case "openhands":
		ohDir := filepath.Join(homeDir, ".openhands")
		target := filepath.Join(ohDir, "microagents", "git-by.md")
		spec := targetSpec{
			agentID:      "openhands",
			displayName:  "OpenHands",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "openhands",
			isStandalone: true,
		}
		if !dirExists(ohDir) {
			spec.skipReason = "(~/.openhands not found)"
		}
		return spec, true

	case "hermes":
		hermesDir, hermesDisplayDir := resolveEnvHomeDir(homeDir, env.hermesHome, ".hermes")
		target := filepath.Join(hermesDir, "SOUL.md")
		spec := targetSpec{
			agentID:      "hermes",
			displayName:  "Hermes Agent",
			path:         target,
			displayPath:  formatHomeDisplayPath(homeDir, target),
			harness:      "hermes",
			isStandalone: false,
		}
		if !dirExists(hermesDir) {
			spec.skipReason = fmt.Sprintf("(%s not found)", hermesDisplayDir)
		}
		return spec, true
	}
	return targetSpec{}, false
}

var projectDedicatedOrder = []string{
	"claude",
	"cursor",
	"windsurf",
	"gemini",
	"copilot",
	"cline",
	"roo",
	"kilo",
	"goose",
	"crush",
	"kiro",
	"continue",
	"trae",
	"augment",
	"qwen",
	"codebuddy",
	"junie",
	"zed",
	"aider",
}

func isAgentsMDHarness(agentID string) bool {
	switch agentID {
	case "agy", "pi", "omp", "codex", "opencode", "dsh", "droid", "amp", "kimi", "openhands", "hermes":
		return true
	default:
		return false
	}
}

func resolveProjectDedicatedTarget(projectRoot, agentID string) (spec targetSpec, pickerLabel string, detected bool) {
	mk := func(id, rel string, standalone bool, detectDirRel string) (targetSpec, string, bool) {
		relSlash := filepath.ToSlash(rel)
		full := filepath.Join(projectRoot, filepath.FromSlash(relSlash))
		det := fileExists(full)
		if !det && detectDirRel != "" {
			det = dirHasEntries(filepath.Join(projectRoot, filepath.FromSlash(detectDirRel)))
		}
		s := targetSpec{
			agentID:      id,
			displayName:  agentDisplayName(id),
			path:         full,
			displayPath:  relSlash,
			harness:      agentHarness(id),
			isStandalone: standalone,
		}
		return s, fmt.Sprintf("%s (%s)", agentDisplayName(id), relSlash), det
	}

	switch agentID {
	case "claude":
		return mk("claude", "CLAUDE.md", false, "")
	case "cursor":
		return mk("cursor", ".cursorrules", false, "")
	case "windsurf":
		return mk("windsurf", ".windsurfrules", false, "")
	case "gemini":
		return mk("gemini", "GEMINI.md", false, "")
	case "copilot":
		return mk("copilot", ".github/copilot-instructions.md", false, "")
	case "cline":
		if fileExists(filepath.Join(projectRoot, ".clinerules")) {
			return mk("cline", ".clinerules", false, "")
		}
		return mk("cline", ".clinerules/git-by.md", true, ".clinerules")
	case "roo":
		if fileExists(filepath.Join(projectRoot, ".roorules")) {
			return mk("roo", ".roorules", false, "")
		}
		return mk("roo", ".roo/rules/git-by.md", true, ".roo/rules")
	case "kilo":
		if fileExists(filepath.Join(projectRoot, ".kilorules")) {
			return mk("kilo", ".kilorules", false, "")
		}
		return mk("kilo", ".kilocode/rules/git-by.md", true, ".kilocode/rules")
	case "goose":
		return mk("goose", ".goosehints", false, "")
	case "crush":
		return mk("crush", "CRUSH.md", false, "")
	case "kiro":
		return mk("kiro", ".kiro/steering/git-by.md", true, ".kiro/steering")
	case "continue":
		if fileExists(filepath.Join(projectRoot, ".continuerules")) {
			return mk("continue", ".continuerules", false, "")
		}
		return mk("continue", ".continue/rules/git-by.md", true, ".continue/rules")
	case "trae":
		if fileExists(filepath.Join(projectRoot, ".trae", "rules", "project_rules.md")) {
			return mk("trae", ".trae/rules/project_rules.md", false, ".trae/rules")
		}
		return mk("trae", ".trae/rules/git-by.md", true, ".trae/rules")
	case "augment":
		return mk("augment", ".augment/rules/git-by.md", true, ".augment/rules")
	case "qwen":
		return mk("qwen", "QWEN.md", false, "")
	case "codebuddy":
		return mk("codebuddy", "CODEBUDDY.md", false, "")
	case "junie":
		return mk("junie", ".junie/guidelines.md", false, ".junie")
	case "zed":
		return mk("zed", ".rules", false, "")
	case "aider":
		return mk("aider", "CONVENTIONS.md", false, "")
	default:
		return targetSpec{}, "", false
	}
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

		for _, id := range projectDedicatedOrder {
			spec, _, detected := resolveProjectDedicatedTarget(projectRoot, id)
			if uninstall {
				if fileExists(spec.path) {
					specs = append(specs, spec)
				}
			} else if detected {
				specs = append(specs, spec)
			}
		}
		return specs
	}

	// Count how many requested agents map to AGENTS.md so we know whether to use a specific harness or generic placeholder.
	var agentsMDMapped []string
	for _, id := range agents {
		if isAgentsMDHarness(id) {
			agentsMDMapped = append(agentsMDMapped, id)
		}
	}

	var specs []targetSpec
	addedAgentsMD := false
	for _, id := range agents {
		if isAgentsMDHarness(id) {
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
			continue
		}

		if spec, _, _ := resolveProjectDedicatedTarget(projectRoot, id); spec.path != "" {
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

		if id == "agents" || isAgentsMDHarness(id) {
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
			continue
		}

		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if spec, _, _ := resolveProjectDedicatedTarget(projectRoot, id); spec.path != "" {
			specs = append(specs, spec)
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

func dirHasEntries(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
