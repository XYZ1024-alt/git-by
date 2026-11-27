package provenance_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/XYZ1024-alt/git-by/internal/provenance"
)

func TestParseRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  provenance.Role
		ok    bool
	}{
		{"orchestrator", provenance.RoleOrchestrator, true},
		{"orch", provenance.RoleOrchestrator, true},
		{"  ORCH  ", provenance.RoleOrchestrator, true},
		{"planner", provenance.RolePlanner, true},
		{"plan", provenance.RolePlanner, true},
		{"Plan", provenance.RolePlanner, true},
		{"implementer", provenance.RoleImplementer, true},
		{"impl", provenance.RoleImplementer, true},
		{"code", provenance.RoleImplementer, true},
		{"CODE", provenance.RoleImplementer, true},
		{"reviewer", provenance.RoleReviewer, true},
		{"rev", provenance.RoleReviewer, true},
		{"REV", provenance.RoleReviewer, true},
		{"committer", provenance.RoleCommitter, true},
		{"commit", provenance.RoleCommitter, true},
		{"Committer", provenance.RoleCommitter, true},
		{"", "", false},
		{"   ", "", false},
		{"author", "", false},
		{"tester", "", false},
	}

	for _, tc := range cases {
		got, err := provenance.ParseRole(tc.input)
		if tc.ok && err != nil {
			t.Errorf("ParseRole(%q) unexpected error: %v", tc.input, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseRole(%q) expected error, got role %q", tc.input, got)
		}
		if got != tc.want {
			t.Errorf("ParseRole(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestShortModelID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
	}{
		{"anthropic/claude-sonnet-4", "claude-sonnet-4"},
		{"openrouter/google/gemini-2.5-pro", "gemini-2.5-pro"},
		{"  openai/gpt-6-astra  ", "gpt-6-astra"},
		{"gpt-6-astra", "gpt-6-astra"},
		{"  claude-opus-4  ", "claude-opus-4"},
		{"", ""},
		{"   ", ""},
	}

	for _, tc := range cases {
		if got := provenance.ShortModelID(tc.input); got != tc.want {
			t.Errorf("ShortModelID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestIsEmpty(t *testing.T) {
	t.Parallel()

	if !(provenance.Metadata{}).IsEmpty() {
		t.Fatal("zero Metadata should be empty")
	}
	if !(provenance.Metadata{Implementers: []string{"", "  "}, Tools: []string{" "}}).IsEmpty() {
		t.Fatal("whitespace-only Metadata should be empty")
	}

	nonEmpty := []provenance.Metadata{
		{Orchestrator: "openai/o3"},
		{Planner: "openai/o3"},
		{Implementers: []string{"anthropic/claude-sonnet-4"}},
		{Reviewer: "google/gemini-2.5-pro"},
		{Committer: "anthropic/claude-haiku-4"},
		{Harness: "pi@1.0.0"},
		{Tools: []string{"go-test"}},
	}
	for i, m := range nonEmpty {
		if m.IsEmpty() {
			t.Errorf("case %d (%+v) expected IsEmpty() == false", i, m)
		}
	}
}

func TestPrimaryModelAndFormatAuthorName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		meta       provenance.Metadata
		baseAuthor string
		wantFull   string
		wantShort  string
		wantAuthor string
	}{
		{
			name:       "empty metadata",
			meta:       provenance.Metadata{},
			baseAuthor: "Alice",
			wantFull:   "",
			wantShort:  "",
			wantAuthor: "Alice",
		},
		{
			name: "reviewer only has no primary model",
			meta: provenance.Metadata{
				Reviewer: "openai/o3",
				Harness:  "pi@1.0.0",
			},
			baseAuthor: "Alice",
			wantFull:   "",
			wantShort:  "",
			wantAuthor: "Alice",
		},
		{
			name: "committer fallback",
			meta: provenance.Metadata{
				Committer: "anthropic/claude-haiku-4",
			},
			baseAuthor: "Alice",
			wantFull:   "anthropic/claude-haiku-4",
			wantShort:  "claude-haiku-4",
			wantAuthor: "Alice by claude-haiku-4",
		},
		{
			name: "orchestrator beats committer",
			meta: provenance.Metadata{
				Orchestrator: "anthropic/claude-opus-4",
				Committer:    "anthropic/claude-haiku-4",
			},
			baseAuthor: "Alice",
			wantFull:   "anthropic/claude-opus-4",
			wantShort:  "claude-opus-4",
			wantAuthor: "Alice by claude-opus-4",
		},
		{
			name: "planner beats orchestrator and committer",
			meta: provenance.Metadata{
				Orchestrator: "anthropic/claude-opus-4",
				Planner:      "openai/o3",
				Committer:    "anthropic/claude-haiku-4",
			},
			baseAuthor: "Alice",
			wantFull:   "openai/o3",
			wantShort:  "o3",
			wantAuthor: "Alice by o3",
		},
		{
			name: "1 implementer beats planner",
			meta: provenance.Metadata{
				Planner:      "openai/o3",
				Implementers: []string{"openai/gpt-6-astra"},
			},
			baseAuthor: "Alice",
			wantFull:   "openai/gpt-6-astra",
			wantShort:  "gpt-6-astra",
			wantAuthor: "Alice by gpt-6-astra",
		},
		{
			name: "1 implementer with empty base author",
			meta: provenance.Metadata{
				Implementers: []string{"anthropic/claude-sonnet-4"},
			},
			baseAuthor: "   ",
			wantFull:   "anthropic/claude-sonnet-4",
			wantShort:  "claude-sonnet-4",
			wantAuthor: "by claude-sonnet-4",
		},
		{
			name: "2 implementers format as & 1 more with clean PrimaryModel",
			meta: provenance.Metadata{
				Planner: "openai/o3",
				Implementers: []string{
					"openai/gpt-6-astra",
					"anthropic/claude-sonnet-4",
				},
			},
			baseAuthor: "Alice",
			wantFull:   "openai/gpt-6-astra",
			wantShort:  "gpt-6-astra",
			wantAuthor: "Alice by gpt-6-astra & 1 more",
		},
		{
			name: "3 implementers format as & 2 more with clean PrimaryModel",
			meta: provenance.Metadata{
				Planner: "openai/o3",
				Implementers: []string{
					"openai/gpt-6-astra",
					"anthropic/claude-sonnet-4",
					"google/gemini-2.5-pro",
				},
			},
			baseAuthor: "Alice",
			wantFull:   "openai/gpt-6-astra",
			wantShort:  "gpt-6-astra",
			wantAuthor: "Alice by gpt-6-astra & 2 more",
		},
		{
			name: "duplicate implementers are deduplicated before & N more",
			meta: provenance.Metadata{
				Implementers: []string{
					"openai/gpt-6-astra",
					"openai/gpt-6-astra",
					"anthropic/claude-sonnet-4",
				},
			},
			baseAuthor: "",
			wantFull:   "openai/gpt-6-astra",
			wantShort:  "gpt-6-astra",
			wantAuthor: "by gpt-6-astra & 1 more",
		},
		{
			name: "identical duplicate implementers collapse to single model without & N more",
			meta: provenance.Metadata{
				Implementers: []string{
					"openai/gpt-6-astra",
					"  openai/gpt-6-astra  ",
				},
			},
			baseAuthor: "Alice",
			wantFull:   "openai/gpt-6-astra",
			wantShort:  "gpt-6-astra",
			wantAuthor: "Alice by gpt-6-astra",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, short := tc.meta.PrimaryModel()
			if full != tc.wantFull || short != tc.wantShort {
				t.Errorf("PrimaryModel() = (%q, %q), want (%q, %q)", full, short, tc.wantFull, tc.wantShort)
			}
			gotAuthor := tc.meta.FormatAuthorName(tc.baseAuthor)
			if gotAuthor != tc.wantAuthor {
				t.Errorf("FormatAuthorName(%q) = %q, want %q", tc.baseAuthor, gotAuthor, tc.wantAuthor)
			}
		})
	}
}

func TestTrailersStrictOrderAndDenoising(t *testing.T) {
	t.Parallel()

	t.Run("full multi-agent relay with distinct committer", func(t *testing.T) {
		meta := provenance.Metadata{
			Orchestrator: "anthropic/claude-opus-4",
			Planner:      "openai/o3",
			Implementers: []string{"openai/gpt-6-astra", "anthropic/claude-sonnet-4", "openai/gpt-6-astra"},
			Reviewer:     "google/gemini-2.5-pro",
			Committer:    "anthropic/claude-haiku-4",
			Harness:      "pi@1.0.0",
			Tools:        []string{" golangci-lint ", "", "go-test", "golangci-lint"},
		}

		got := meta.Trailers()
		want := []string{
			"Assisted-by: pi:gpt-6-astra [golangci-lint] [go-test]",
			"X-Agent-Orchestrator: anthropic/claude-opus-4",
			"X-Agent-Planner: openai/o3",
			"X-Agent-Implementer: openai/gpt-6-astra, anthropic/claude-sonnet-4",
			"X-Agent-Reviewer: google/gemini-2.5-pro",
			"X-Agent-Committer: anthropic/claude-haiku-4",
			"X-Agent-Harness: pi@1.0.0",
		}

		assertTrailerLines(t, got, want)
	})

	t.Run("smart committer denoising omits identical sole implementer", func(t *testing.T) {
		meta := provenance.Metadata{
			Implementers: []string{"anthropic/claude-sonnet-4"},
			Committer:    "anthropic/claude-sonnet-4",
			Harness:      "claude-code",
		}

		got := meta.Trailers()
		want := []string{
			"Assisted-by: claude-code:claude-sonnet-4",
			"X-Agent-Implementer: anthropic/claude-sonnet-4",
			"X-Agent-Harness: claude-code",
		}

		assertTrailerLines(t, got, want)
	})

	t.Run("committer retained when multiple implementers exist", func(t *testing.T) {
		meta := provenance.Metadata{
			Implementers: []string{"anthropic/claude-sonnet-4", "openai/gpt-6-astra"},
			Committer:    "anthropic/claude-sonnet-4",
		}

		got := meta.Trailers()
		want := []string{
			"Assisted-by: claude-sonnet-4",
			"X-Agent-Implementer: anthropic/claude-sonnet-4, openai/gpt-6-astra",
			"X-Agent-Committer: anthropic/claude-sonnet-4",
		}

		assertTrailerLines(t, got, want)
	})

	t.Run("scoped npm harness strips version cleanly", func(t *testing.T) {
		meta := provenance.Metadata{
			Planner: "openai/o3",
			Harness: "@anthropic-ai/claude-code@1.2.3",
		}

		got := meta.Trailers()
		want := []string{
			"Assisted-by: @anthropic-ai/claude-code:o3",
			"X-Agent-Planner: openai/o3",
			"X-Agent-Harness: @anthropic-ai/claude-code@1.2.3",
		}

		assertTrailerLines(t, got, want)
	})

	t.Run("reviewer only omits Assisted-by", func(t *testing.T) {
		meta := provenance.Metadata{
			Reviewer: "openai/o3",
		}

		got := meta.Trailers()
		want := []string{
			"X-Agent-Reviewer: openai/o3",
		}

		assertTrailerLines(t, got, want)
	})

	t.Run("empty metadata emits no trailers", func(t *testing.T) {
		got := (provenance.Metadata{}).Trailers()
		if len(got) != 0 {
			t.Fatalf("expected 0 trailers, got %v", got)
		}
	})
}

func TestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	orig := provenance.Metadata{
		Orchestrator: "anthropic/claude-opus-4",
		Planner:      "openai/o3",
		Implementers: []string{"anthropic/claude-sonnet-4"},
		Reviewer:     "google/gemini-2.5-pro",
		Committer:    "anthropic/claude-haiku-4",
		Harness:      "pi@1.0.0",
		Tools:        []string{"go-test"},
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded provenance.Metadata
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if !reflect.DeepEqual(decoded, orig) {
		t.Fatalf("decoded = %+v, want %+v", decoded, orig)
	}
}

func TestStripAuthorAttribution(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		// Ordinary human names remain untouched.
		{"plain full name", "Ada Lovelace", "Ada Lovelace"},
		{"name containing by substring", "Shelby Miller", "Shelby Miller"},
		{"short name ending in by", "Bobby", "Bobby"},
		{"ampersand human name", "Alice & Bob", "Alice & Bob"},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},

		// Valid single, multi-model, nested, and no-base suffixes.
		{"single model", "Ada by claude-sonnet-4", "Ada"},
		{"multi-model suffix", "Ada by gpt-6-astra & 2 more", "Ada"},
		{"nested repeated single models", "Ada by old-model by new-model", "Ada"},
		{"nested repeated multi-models", "Ada by m1 & 1 more by m2 & 2 more", "Ada"},
		{"no base single model", "by old-model", ""},
		{"no base multi-model", "by old-model & 1 more", ""},
		{"no base nested models", "by m1 by m2", ""},
		{"legacy +N suffix", "Ada by gpt-6+1", "Ada"},
		{"model with punctuation", "Ada Lovelace by openai/gpt-5.2:beta@1", "Ada Lovelace"},

		// Malformed suffixes must not be erroneously stripped.
		{"invalid space in model", "Alice by bad model", "Alice by bad model"},
		{"zero more rejected", "Alice by gpt-6 & 0 more", "Alice by gpt-6 & 0 more"},
		{"leading zero more rejected", "Alice by gpt-6 & 01 more", "Alice by gpt-6 & 01 more"},
		{"double space before ampersand rejected", "Alice by gpt-6  & 1 more", "Alice by gpt-6  & 1 more"},
		{"trailing by without model", "Alice by ", "Alice by"},
		{"missing model before ampersand", "Alice by & 1 more", "Alice by & 1 more"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provenance.StripAuthorAttribution(tc.input); got != tc.want {
				t.Errorf("StripAuthorAttribution(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatAuthorName_DeduplicatesExistingSuffix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		meta       provenance.Metadata
		baseAuthor string
		want       string
	}{
		{
			name:       "replaces single existing suffix",
			meta:       provenance.Metadata{Implementers: []string{"anthropic/claude-sonnet-4"}},
			baseAuthor: "Ada Lovelace by old-model",
			want:       "Ada Lovelace by claude-sonnet-4",
		},
		{
			name:       "replaces multi-model existing suffix",
			meta:       provenance.Metadata{Implementers: []string{"openai/m1", "anthropic/m2"}},
			baseAuthor: "Ada Lovelace by old-model & 2 more",
			want:       "Ada Lovelace by m1 & 1 more",
		},
		{
			name:       "replaces bare by-model author without duplicating by",
			meta:       provenance.Metadata{Implementers: []string{"anthropic/claude-sonnet-4"}},
			baseAuthor: "by old-model",
			want:       "by claude-sonnet-4",
		},
		{
			name:       "preserves ordinary human name containing by",
			meta:       provenance.Metadata{Implementers: []string{"anthropic/claude-sonnet-4"}},
			baseAuthor: "Shelby Miller",
			want:       "Shelby Miller by claude-sonnet-4",
		},
		{
			name:       "empty metadata leaves base author unchanged",
			meta:       provenance.Metadata{},
			baseAuthor: "Ada Lovelace",
			want:       "Ada Lovelace",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.meta.FormatAuthorName(tc.baseAuthor); got != tc.want {
				t.Errorf("FormatAuthorName(%q) = %q, want %q", tc.baseAuthor, got, tc.want)
			}
		})
	}
}

func assertTrailerLines(t *testing.T, got []provenance.Trailer, want []string) {
	t.Helper()
	lines := make([]string, len(got))
	for i, tr := range got {
		lines[i] = tr.String()
		lower := strings.ToLower(tr.Key)
		if lower == "co-authored-by" || lower == "signed-off-by" {
			t.Fatalf("forbidden trailer emitted: %s", tr.String())
		}
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("trailers =\n  %v\nwant:\n  %v", lines, want)
	}
}
