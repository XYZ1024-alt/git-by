package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/XYZ1024-alt/git-by/internal/provenance"
)

const SessionFileName = "by-session.json"

const (
	EnvOrchestrator = "GIT_BY_ORCHESTRATOR"
	EnvPlanner      = "GIT_BY_PLANNER"
	EnvImplementer  = "GIT_BY_IMPLEMENTER"
	EnvReviewer     = "GIT_BY_REVIEWER"
	EnvCommitter    = "GIT_BY_COMMITTER"
	EnvHarness      = "GIT_BY_HARNESS"
	EnvTools        = "GIT_BY_TOOLS"
)

// Load reads provenance metadata from sessionFilePath.
// If sessionFilePath does not exist, it returns an empty Metadata, exists=false, and err=nil.
func Load(sessionFilePath string) (meta provenance.Metadata, exists bool, err error) {
	data, err := os.ReadFile(sessionFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return provenance.Metadata{}, false, nil
		}
		return provenance.Metadata{}, false, fmt.Errorf("read session file %s: %w", sessionFilePath, err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return provenance.Metadata{}, true, fmt.Errorf("parse session file %s: %w", sessionFilePath, err)
	}
	return meta, true, nil
}

// Save writes provenance metadata atomically to sessionFilePath via a temporary file in the same directory.
func Save(sessionFilePath string, meta provenance.Metadata) error {
	dir := filepath.Dir(sessionFilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session dir %s: %w", dir, err)
	}

	payload, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session metadata: %w", err)
	}
	payload = append(payload, '\n')

	tmpFile, err := os.CreateTemp(dir, ".by-session-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp session file: %w", err)
	}
	tmpName := tmpFile.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(payload); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp session file: %w", err)
	}
	if err := tmpFile.Chmod(0o644); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temp session file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp session file: %w", err)
	}
	if err := os.Rename(tmpName, sessionFilePath); err != nil {
		return fmt.Errorf("rename session file to %s: %w", sessionFilePath, err)
	}
	renamed = true
	return nil
}

// Clear removes sessionFilePath if present. Missing files are ignored.
func Clear(sessionFilePath string) error {
	if err := os.Remove(sessionFilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove session file %s: %w", sessionFilePath, err)
	}
	return nil
}

// ApplyRole updates meta with the specified role, model(s), optional harness, and optional tools.
func ApplyRole(meta *provenance.Metadata, role provenance.Role, modelArg string, harness string, tools []string, replace bool) error {
	if meta == nil {
		return errors.New("nil metadata pointer")
	}

	canonicalRole, err := provenance.ParseRole(string(role))
	if err != nil {
		return err
	}

	models := splitCommaUnique(modelArg)
	if len(models) == 0 {
		return errors.New("at least one non-empty model is required")
	}

	switch canonicalRole {
	case provenance.RoleOrchestrator:
		meta.Orchestrator = models[len(models)-1]
	case provenance.RolePlanner:
		meta.Planner = models[len(models)-1]
	case provenance.RoleImplementer:
		if replace {
			meta.Implementers = models
		} else {
			meta.Implementers = appendUnique(meta.Implementers, models...)
		}
	case provenance.RoleReviewer:
		meta.Reviewer = models[len(models)-1]
	case provenance.RoleCommitter:
		meta.Committer = models[len(models)-1]
	}

	if h := strings.TrimSpace(harness); h != "" {
		meta.Harness = h
	}

	parsedTools := splitSliceCommaUnique(tools)
	if replace && len(tools) > 0 {
		meta.Tools = parsedTools
	} else if len(parsedTools) > 0 {
		meta.Tools = appendUnique(meta.Tools, parsedTools...)
	}

	return nil
}

// FromEnv constructs provenance.Metadata from GIT_BY_* environment variables via lookup.
func FromEnv(lookup func(string) (string, bool)) provenance.Metadata {
	if lookup == nil {
		return provenance.Metadata{}
	}
	var meta provenance.Metadata
	if v, ok := lookup(EnvOrchestrator); ok {
		meta.Orchestrator = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvPlanner); ok {
		meta.Planner = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvImplementer); ok {
		meta.Implementers = splitCommaUnique(v)
	}
	if v, ok := lookup(EnvReviewer); ok {
		meta.Reviewer = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvCommitter); ok {
		meta.Committer = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvHarness); ok {
		meta.Harness = strings.TrimSpace(v)
	}
	if v, ok := lookup(EnvTools); ok {
		meta.Tools = splitCommaUnique(v)
	}
	return meta
}

// Merge combines base and override metadata.
// Non-empty scalar fields in override replace base; non-empty override.Implementers replaces base.Implementers
// (deduplicated); non-empty override.Tools appends deduplicated onto base.Tools.
func Merge(base, override provenance.Metadata) provenance.Metadata {
	out := provenance.Metadata{
		Orchestrator: strings.TrimSpace(base.Orchestrator),
		Planner:      strings.TrimSpace(base.Planner),
		Implementers: appendUnique(nil, base.Implementers...),
		Reviewer:     strings.TrimSpace(base.Reviewer),
		Committer:    strings.TrimSpace(base.Committer),
		Harness:      strings.TrimSpace(base.Harness),
		Tools:        appendUnique(nil, base.Tools...),
	}

	if v := strings.TrimSpace(override.Orchestrator); v != "" {
		out.Orchestrator = v
	}
	if v := strings.TrimSpace(override.Planner); v != "" {
		out.Planner = v
	}
	if impls := appendUnique(nil, override.Implementers...); len(impls) > 0 {
		out.Implementers = impls
	}
	if v := strings.TrimSpace(override.Reviewer); v != "" {
		out.Reviewer = v
	}
	if v := strings.TrimSpace(override.Committer); v != "" {
		out.Committer = v
	}
	if v := strings.TrimSpace(override.Harness); v != "" {
		out.Harness = v
	}
	if overrideTools := appendUnique(nil, override.Tools...); len(overrideTools) > 0 {
		out.Tools = appendUnique(out.Tools, overrideTools...)
	}

	return out
}

func splitCommaUnique(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	return appendUnique(nil, parts...)
}

func splitSliceCommaUnique(items []string) []string {
	var parts []string
	for _, item := range items {
		parts = append(parts, strings.Split(item, ",")...)
	}
	return appendUnique(nil, parts...)
}

func appendUnique(existing []string, additions ...string) []string {
	var out []string
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, item := range existing {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, item := range additions {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
