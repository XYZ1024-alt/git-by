package provenance

import (
	"fmt"
	"strings"
)

// Role identifies an AI agent's stage in a development workflow.
type Role string

const (
	RoleOrchestrator Role = "orchestrator"
	RolePlanner      Role = "planner"
	RoleImplementer  Role = "implementer"
	RoleReviewer     Role = "reviewer"
	RoleCommitter    Role = "committer"
)

// ParseRole maps case-insensitive aliases:
// orchestrator|orch, planner|plan, implementer|impl|code, reviewer|rev, committer|commit
func ParseRole(input string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "orchestrator", "orch":
		return RoleOrchestrator, nil
	case "planner", "plan":
		return RolePlanner, nil
	case "implementer", "impl", "code":
		return RoleImplementer, nil
	case "reviewer", "rev":
		return RoleReviewer, nil
	case "committer", "commit":
		return RoleCommitter, nil
	default:
		return "", fmt.Errorf(
			"unknown role %q (expected orchestrator|orch, planner|plan, implementer|impl|code, reviewer|rev, or committer|commit)",
			input,
		)
	}
}

// Trailer represents an ordered Git commit trailer line.
type Trailer struct {
	Key   string
	Value string
}

// String formats the trailer as "Key: Value".
func (t Trailer) String() string {
	return t.Key + ": " + t.Value
}

// Metadata stores multi-agent provenance information for a commit or session.
type Metadata struct {
	Orchestrator string   `json:"orchestrator,omitempty"`
	Planner      string   `json:"planner,omitempty"`
	Implementers []string `json:"implementers,omitempty"`
	Reviewer     string   `json:"reviewer,omitempty"`
	Committer    string   `json:"committer,omitempty"`
	Harness      string   `json:"harness,omitempty"`
	Tools        []string `json:"tools,omitempty"`
}

// IsEmpty reports whether no provenance metadata has been recorded.
func (m Metadata) IsEmpty() bool {
	return strings.TrimSpace(m.Orchestrator) == "" &&
		strings.TrimSpace(m.Planner) == "" &&
		len(cleanUnique(m.Implementers)) == 0 &&
		strings.TrimSpace(m.Reviewer) == "" &&
		strings.TrimSpace(m.Committer) == "" &&
		strings.TrimSpace(m.Harness) == "" &&
		len(cleanUnique(m.Tools)) == 0
}

// ShortModelID strips any provider prefix up to the last '/' and trims spaces.
func ShortModelID(model string) string {
	s := strings.TrimSpace(model)
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = strings.TrimSpace(s[idx+1:])
	}
	return s
}

// PrimaryModel returns the highest-priority model (Implementers[0] > Planner > Orchestrator > Committer)
// along with its clean short display identifier.
func (m Metadata) PrimaryModel() (full string, short string) {
	impls := cleanUnique(m.Implementers)
	if len(impls) > 0 {
		full = impls[0]
		return full, ShortModelID(full)
	}
	if p := strings.TrimSpace(m.Planner); p != "" {
		return p, ShortModelID(p)
	}
	if o := strings.TrimSpace(m.Orchestrator); o != "" {
		return o, ShortModelID(o)
	}
	if c := strings.TrimSpace(m.Committer); c != "" {
		return c, ShortModelID(c)
	}
	return "", ""
}

// FormatAuthorName formats the Git author display name with primary model attribution.
// When multiple distinct implementer models are present, it appends " & <M-1> more".
func (m Metadata) FormatAuthorName(baseUserName string) string {
	_, short := m.PrimaryModel()
	if short == "" {
		return baseUserName
	}
	attribution := short
	if impls := cleanUnique(m.Implementers); len(impls) > 1 {
		attribution = fmt.Sprintf("%s & %d more", short, len(impls)-1)
	}
	base := strings.TrimSpace(baseUserName)
	if base == "" {
		return "by " + attribution
	}
	return base + " by " + attribution
}

// Trailers returns the canonical ordered list of Git provenance trailers.
// It never emits Co-authored-by or Signed-off-by.
func (m Metadata) Trailers() []Trailer {
	var trailers []Trailer

	impls := cleanUnique(m.Implementers)
	orch := strings.TrimSpace(m.Orchestrator)
	plan := strings.TrimSpace(m.Planner)
	rev := strings.TrimSpace(m.Reviewer)
	comm := strings.TrimSpace(m.Committer)
	harness := strings.TrimSpace(m.Harness)
	tools := cleanUnique(m.Tools)

	if _, short := m.PrimaryModel(); short != "" {
		val := short
		if hb := harnessBase(harness); hb != "" {
			val = hb + ":" + short
		}
		if len(tools) > 0 {
			var b strings.Builder
			b.WriteString(val)
			for _, tool := range tools {
				b.WriteString(" [")
				b.WriteString(tool)
				b.WriteByte(']')
			}
			val = b.String()
		}
		trailers = append(trailers, Trailer{
			Key:   "Assisted-by",
			Value: val,
		})
	}

	if orch != "" {
		trailers = append(trailers, Trailer{
			Key:   "X-Agent-Orchestrator",
			Value: orch,
		})
	}
	if plan != "" {
		trailers = append(trailers, Trailer{
			Key:   "X-Agent-Planner",
			Value: plan,
		})
	}
	if len(impls) > 0 {
		trailers = append(trailers, Trailer{
			Key:   "X-Agent-Implementer",
			Value: strings.Join(impls, ", "),
		})
	}
	if rev != "" {
		trailers = append(trailers, Trailer{
			Key:   "X-Agent-Reviewer",
			Value: rev,
		})
	}
	if comm != "" && !(len(impls) == 1 && comm == impls[0]) {
		trailers = append(trailers, Trailer{
			Key:   "X-Agent-Committer",
			Value: comm,
		})
	}
	if harness != "" {
		trailers = append(trailers, Trailer{
			Key:   "X-Agent-Harness",
			Value: harness,
		})
	}

	return trailers
}

func harnessBase(harness string) string {
	h := strings.TrimSpace(harness)
	if h == "" || h == "@" {
		return ""
	}
	if idx := strings.LastIndex(h, "@"); idx > 0 {
		return strings.TrimSpace(h[:idx])
	}
	return h
}

func cleanUnique(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	var out []string
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
