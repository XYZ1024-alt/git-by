package session_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/XYZ1024-alt/git-by/internal/provenance"
	"github.com/XYZ1024-alt/git-by/internal/session"
)

func TestLoadSaveClearLifecycle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "nested", session.SessionFileName)

	meta, exists, err := session.Load(sessionPath)
	if err != nil {
		t.Fatalf("Load nonexistent returned error: %v", err)
	}
	if exists {
		t.Fatal("Load nonexistent reported exists=true")
	}
	if !meta.IsEmpty() {
		t.Fatalf("Load nonexistent returned non-empty meta: %+v", meta)
	}

	if err := session.Clear(sessionPath); err != nil {
		t.Fatalf("Clear nonexistent returned error: %v", err)
	}

	want := provenance.Metadata{
		Orchestrator: "anthropic/claude-opus-4",
		Planner:      "openai/o3",
		Implementers: []string{"openai/gpt-6-astra", "anthropic/claude-sonnet-4"},
		Reviewer:     "google/gemini-2.5-pro",
		Committer:    "anthropic/claude-haiku-4",
		Harness:      "pi@1.0.0",
		Tools:        []string{"golangci-lint", "go-test"},
	}

	if err := session.Save(sessionPath, want); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Ensure no leftover temporary files remain in the session directory.
	entries, err := os.ReadDir(filepath.Dir(sessionPath))
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != session.SessionFileName {
		t.Fatalf("unexpected files in session dir: %v", entries)
	}

	got, exists, err := session.Load(sessionPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !exists {
		t.Fatal("Load reported exists=false after Save")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}

	if err := session.Clear(sessionPath); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	_, exists, err = session.Load(sessionPath)
	if err != nil {
		t.Fatalf("Load after Clear returned error: %v", err)
	}
	if exists {
		t.Fatal("Load after Clear reported exists=true")
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	t.Parallel()

	sessionPath := filepath.Join(t.TempDir(), session.SessionFileName)
	if err := os.WriteFile(sessionPath, []byte("{corrupt"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	_, exists, err := session.Load(sessionPath)
	if err == nil {
		t.Fatal("expected error loading corrupt JSON")
	}
	if !exists {
		t.Fatal("expected exists=true when corrupt file exists")
	}
}

func TestConcurrentSaveAndLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionPath := filepath.Join(dir, session.SessionFileName)

	const writers = 24
	const iterations = 25

	var wg sync.WaitGroup
	errCh := make(chan error, writers*2)

	for w := 0; w < writers; w++ {
		wg.Add(2)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				meta := provenance.Metadata{
					Planner:      "openai/o3",
					Implementers: []string{fmt.Sprintf("provider/model-%d-%d", workerID, i)},
					Harness:      "pi@1.0.0",
					Tools:        []string{"go-test"},
				}
				if err := session.Save(sessionPath, meta); err != nil {
					errCh <- fmt.Errorf("Save worker %d iter %d: %w", workerID, i, err)
					return
				}
			}
		}(w)

		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				meta, exists, err := session.Load(sessionPath)
				if err != nil {
					errCh <- fmt.Errorf("Load reader %d iter %d: %w", workerID, i, err)
					return
				}
				if exists && (meta.Planner != "openai/o3" || len(meta.Implementers) != 1) {
					errCh <- fmt.Errorf("Load reader %d observed partial/invalid meta: %+v", workerID, meta)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != session.SessionFileName {
		t.Fatalf("expected only %s remaining, got %d entries", session.SessionFileName, len(entries))
	}
}

func TestApplyRole(t *testing.T) {
	t.Parallel()

	var meta provenance.Metadata

	if err := session.ApplyRole(nil, provenance.RolePlanner, "openai/o3", "", nil, false); err == nil {
		t.Fatal("expected error for nil meta")
	}
	if err := session.ApplyRole(&meta, "invalid", "openai/o3", "", nil, false); err == nil {
		t.Fatal("expected error for invalid role")
	}
	if err := session.ApplyRole(&meta, provenance.RolePlanner, " ,  , ", "", nil, false); err == nil {
		t.Fatal("expected error for empty modelArg")
	}

	if err := session.ApplyRole(&meta, provenance.RoleOrchestrator, "anthropic/claude-opus-4", "pi@1.0.0", []string{"golangci-lint, go-test"}, false); err != nil {
		t.Fatalf("ApplyRole orchestrator: %v", err)
	}
	if err := session.ApplyRole(&meta, provenance.RolePlanner, "openai/o3", "", []string{"go-test", "go-vet"}, false); err != nil {
		t.Fatalf("ApplyRole planner: %v", err)
	}
	if err := session.ApplyRole(&meta, provenance.RoleImplementer, "openai/gpt-6-astra, anthropic/claude-sonnet-4", "", nil, false); err != nil {
		t.Fatalf("ApplyRole implementer 1: %v", err)
	}
	if err := session.ApplyRole(&meta, provenance.RoleImplementer, "anthropic/claude-sonnet-4, google/gemini-2.5-pro", "", nil, false); err != nil {
		t.Fatalf("ApplyRole implementer 2: %v", err)
	}
	if err := session.ApplyRole(&meta, provenance.RoleReviewer, "openai/o4-mini", "", nil, false); err != nil {
		t.Fatalf("ApplyRole reviewer: %v", err)
	}
	if err := session.ApplyRole(&meta, provenance.RoleCommitter, "anthropic/claude-haiku-4", "", nil, false); err != nil {
		t.Fatalf("ApplyRole committer: %v", err)
	}

	want := provenance.Metadata{
		Orchestrator: "anthropic/claude-opus-4",
		Planner:      "openai/o3",
		Implementers: []string{"openai/gpt-6-astra", "anthropic/claude-sonnet-4", "google/gemini-2.5-pro"},
		Reviewer:     "openai/o4-mini",
		Committer:    "anthropic/claude-haiku-4",
		Harness:      "pi@1.0.0",
		Tools:        []string{"golangci-lint", "go-test", "go-vet"},
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("meta = %+v, want %+v", meta, want)
	}

	// Replace implementers without passing tools: Tools should stay intact.
	if err := session.ApplyRole(&meta, provenance.RoleImplementer, "xai/grok-4", "cursor@0.50.0", nil, true); err != nil {
		t.Fatalf("ApplyRole replace implementers: %v", err)
	}
	if !reflect.DeepEqual(meta.Implementers, []string{"xai/grok-4"}) {
		t.Fatalf("Implementers after replace = %v, want [xai/grok-4]", meta.Implementers)
	}
	if meta.Harness != "cursor@0.50.0" {
		t.Fatalf("Harness = %q, want cursor@0.50.0", meta.Harness)
	}
	if !reflect.DeepEqual(meta.Tools, []string{"golangci-lint", "go-test", "go-vet"}) {
		t.Fatalf("Tools unexpectedly modified: %v", meta.Tools)
	}

	// Replace with non-empty tools slice replaces Tools as well.
	if err := session.ApplyRole(&meta, provenance.RoleImplementer, "anthropic/claude-sonnet-4", "", []string{"govulncheck, go-test"}, true); err != nil {
		t.Fatalf("ApplyRole replace tools: %v", err)
	}
	if !reflect.DeepEqual(meta.Tools, []string{"govulncheck", "go-test"}) {
		t.Fatalf("Tools after replace = %v, want [govulncheck go-test]", meta.Tools)
	}
}

func TestFromEnvAndMerge(t *testing.T) {
	t.Parallel()

	if !(session.FromEnv(nil)).IsEmpty() {
		t.Fatal("FromEnv(nil) should be empty")
	}

	env := map[string]string{
		session.EnvOrchestrator: " anthropic/claude-opus-4 ",
		session.EnvPlanner:      " openai/o3 ",
		session.EnvImplementer:  "openai/gpt-6-astra, anthropic/claude-sonnet-4, openai/gpt-6-astra",
		session.EnvReviewer:     " google/gemini-2.5-pro ",
		session.EnvCommitter:    " anthropic/claude-haiku-4 ",
		session.EnvHarness:      " pi@1.0.0 ",
		session.EnvTools:        "golangci-lint, go-test, golangci-lint",
	}
	fromEnv := session.FromEnv(func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})

	wantEnv := provenance.Metadata{
		Orchestrator: "anthropic/claude-opus-4",
		Planner:      "openai/o3",
		Implementers: []string{"openai/gpt-6-astra", "anthropic/claude-sonnet-4"},
		Reviewer:     "google/gemini-2.5-pro",
		Committer:    "anthropic/claude-haiku-4",
		Harness:      "pi@1.0.0",
		Tools:        []string{"golangci-lint", "go-test"},
	}
	if !reflect.DeepEqual(fromEnv, wantEnv) {
		t.Fatalf("FromEnv = %+v, want %+v", fromEnv, wantEnv)
	}

	base := provenance.Metadata{
		Orchestrator: "base/orch",
		Planner:      "base/plan",
		Implementers: []string{"base/impl-1", "base/impl-2"},
		Reviewer:     "base/rev",
		Harness:      "base-harness@0.1.0",
		Tools:        []string{"tool-a", "tool-b"},
	}
	override := provenance.Metadata{
		Planner:      "override/plan",
		Implementers: []string{"override/impl", "override/impl"},
		Committer:    "override/commit",
		Tools:        []string{"tool-b", "tool-c"},
	}

	merged := session.Merge(base, override)
	wantMerged := provenance.Metadata{
		Orchestrator: "base/orch",
		Planner:      "override/plan",
		Implementers: []string{"override/impl"},
		Reviewer:     "base/rev",
		Committer:    "override/commit",
		Harness:      "base-harness@0.1.0",
		Tools:        []string{"tool-a", "tool-b", "tool-c"},
	}
	if !reflect.DeepEqual(merged, wantMerged) {
		t.Fatalf("Merge = %+v, want %+v", merged, wantMerged)
	}

	// Empty override should preserve base.Implementers and base.Tools without aliasing.
	mergedEmpty := session.Merge(base, provenance.Metadata{})
	if !reflect.DeepEqual(mergedEmpty, base) {
		t.Fatalf("Merge with empty override = %+v, want %+v", mergedEmpty, base)
	}
	mergedEmpty.Implementers[0] = "mutated"
	mergedEmpty.Tools[0] = "mutated"
	if base.Implementers[0] == "mutated" || base.Tools[0] == "mutated" {
		t.Fatal("Merge aliased slices from base")
	}
}
