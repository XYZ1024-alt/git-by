package stats

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Options configures repository provenance collection.
type Options struct {
	Since string // optional --since=<date>
	TopN  int    // default 5 when <= 0
}

// ModelCount represents commit participation metrics for a single model or harness.
type ModelCount struct {
	Model   string  `json:"model"`
	Commits int     `json:"commits"`
	Percent float64 `json:"percent"`
}

// Breakdown holds commit and line-change totals and percentages for Human or AI commits.
type Breakdown struct {
	Commits       int     `json:"commits"`
	CommitPercent float64 `json:"commit_percent"`
	Additions     int     `json:"additions"`
	Deletions     int     `json:"deletions"`
	LinesPercent  float64 `json:"lines_percent"`
}

// Report summarizes Human vs AI commit provenance and per-role leaderboards.
type Report struct {
	RepoName      string       `json:"repo_name"`
	Since         string       `json:"since,omitempty"`
	TotalCommits  int          `json:"total_commits"`
	TotalLines    int          `json:"total_lines"`
	Human         Breakdown    `json:"human"`
	AI            Breakdown    `json:"ai"`
	Implementers  []ModelCount `json:"implementers"`
	Planners      []ModelCount `json:"planners"`
	Orchestrators []ModelCount `json:"orchestrators,omitempty"`
	Reviewers     []ModelCount `json:"reviewers,omitempty"`
	Committers    []ModelCount `json:"committers,omitempty"`
	Harnesses     []ModelCount `json:"harnesses,omitempty"`
}

const (
	commitStartMarker = "\x00GITBY_COMMIT_START\x00"
	bodyEndMarker     = "\x00GITBY_BODY_END\x00"

	streamBufSize      = 64 * 1024
	maxRetainedBodyCap = 256 * 1024
)

var (
	commitStartMarkerBytes = []byte(commitStartMarker)
	bodyEndMarkerBytes     = []byte(bodyEndMarker)
	numstatDashBytes       = []byte("-")
)

// Collect analyzes non-merge commit history in repoDir and computes AI provenance statistics.
func Collect(repoDir string, opts Options) (*Report, error) {
	if strings.TrimSpace(repoDir) == "" {
		repoDir = "."
	}
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}
	info, err := os.Stat(absRepoDir)
	if err != nil {
		return nil, fmt.Errorf("stat repo path %q: %w", absRepoDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repo path %q is not a directory", absRepoDir)
	}

	// Ensure absRepoDir is inside a valid Git repository.
	if _, stderr, err := runGit(absRepoDir, "rev-parse", "--git-dir"); err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("not a git repository (%s): %s", absRepoDir, msg)
	}

	repoName := detectRepoName(absRepoDir)
	topN := opts.TopN
	if topN <= 0 {
		topN = 5
	}
	since := strings.TrimSpace(opts.Since)

	report := &Report{
		RepoName:     repoName,
		Since:        since,
		Implementers: []ModelCount{},
		Planners:     []ModelCount{},
	}

	logFormat := "%x00GITBY_COMMIT_START%x00%H%x00%an%x00%B%x00GITBY_BODY_END%x00"
	args := []string{
		"--no-pager",
		"log",
		"--no-merges",
		"--no-ext-diff",
		"--numstat",
		"--format=" + logFormat,
	}
	if since != "" {
		args = append(args, "--since="+since)
	}

	implCounts := make(map[string]int)
	planCounts := make(map[string]int)
	orchCounts := make(map[string]int)
	revCounts := make(map[string]int)
	committerCounts := make(map[string]int)
	harnessCounts := make(map[string]int)

	stderr, err := streamGitLog(absRepoDir, args, func(rec commitRecord) {
		report.TotalCommits++
		commitLines := rec.additions + rec.deletions
		report.TotalLines += commitLines

		parsed := parseProvenance(rec.authorName, rec.body)
		if parsed.isAI {
			report.AI.Commits++
			report.AI.Additions += rec.additions
			report.AI.Deletions += rec.deletions

			for _, m := range parsed.implementers {
				implCounts[m]++
			}
			for _, m := range parsed.planners {
				planCounts[m]++
			}
			for _, m := range parsed.orchestrators {
				orchCounts[m]++
			}
			for _, m := range parsed.reviewers {
				revCounts[m]++
			}
			for _, m := range parsed.committers {
				committerCounts[m]++
			}
			for _, h := range parsed.harnesses {
				harnessCounts[h]++
			}
		} else {
			report.Human.Commits++
			report.Human.Additions += rec.additions
			report.Human.Deletions += rec.deletions
		}
	})
	if err != nil {
		if isEmptyRepoStderr(stderr) {
			return report, nil
		}
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git log failed: %s", msg)
	}

	if report.TotalCommits == 0 {
		return report, nil
	}

	if report.TotalCommits > 0 {
		report.Human.CommitPercent = round2(float64(report.Human.Commits) / float64(report.TotalCommits) * 100.0)
		report.AI.CommitPercent = round2(float64(report.AI.Commits) / float64(report.TotalCommits) * 100.0)
	}
	if report.TotalLines > 0 {
		humanLines := report.Human.Additions + report.Human.Deletions
		aiLines := report.AI.Additions + report.AI.Deletions
		report.Human.LinesPercent = round2(float64(humanLines) / float64(report.TotalLines) * 100.0)
		report.AI.LinesPercent = round2(float64(aiLines) / float64(report.TotalLines) * 100.0)
	}

	report.Implementers = buildLeaderboard(implCounts, report.AI.Commits, topN, false)
	report.Planners = buildLeaderboard(planCounts, report.AI.Commits, topN, false)
	report.Orchestrators = buildLeaderboard(orchCounts, report.AI.Commits, topN, true)
	report.Reviewers = buildLeaderboard(revCounts, report.AI.Commits, topN, true)
	report.Committers = buildLeaderboard(committerCounts, report.AI.Commits, topN, true)
	report.Harnesses = buildLeaderboard(harnessCounts, report.AI.Commits, topN, true)

	return report, nil
}

type commitRecord struct {
	hash       string
	authorName string
	body       string
	additions  int
	deletions  int
}

func streamGitLog(dir string, args []string, onRecord func(commitRecord)) (stderr string, err error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	parseErr := streamCommitRecords(stdoutPipe, onRecord)
	if parseErr != nil {
		// Drain any unread output so git cannot block writing to the pipe before Wait.
		_, _ = io.Copy(io.Discard, stdoutPipe)
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		return stderrBuf.String(), waitErr
	}
	if parseErr != nil {
		return stderrBuf.String(), parseErr
	}
	return stderrBuf.String(), nil
}

func streamCommitRecords(r io.Reader, onRecord func(commitRecord)) error {
	br := bufio.NewReaderSize(r, streamBufSize)

	found, err := readUntilMarker(br, commitStartMarkerBytes, nil)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	var headerBodyBuf bytes.Buffer
	var lineBuf bytes.Buffer

	for {
		headerBodyBuf.Reset()
		found, err := readUntilMarker(br, bodyEndMarkerBytes, &headerBodyBuf)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}

		raw := headerBodyBuf.Bytes()
		valid := false
		var hash, authorName, body string
		if firstNul := bytes.IndexByte(raw, '\x00'); firstNul >= 0 {
			rest := raw[firstNul+1:]
			if secondNul := bytes.IndexByte(rest, '\x00'); secondNul >= 0 {
				hash = string(raw[:firstNul])
				authorName = string(rest[:secondNul])
				body = string(rest[secondNul+1:])
				valid = true
			}
		}

		// Release oversized backing buffers after processing an unusually large commit.
		if headerBodyBuf.Cap() > maxRetainedBodyCap {
			headerBodyBuf = bytes.Buffer{}
		}

		additions, deletions, hasNext, err := streamNumstatUntilNextCommit(br, &lineBuf)
		if lineBuf.Cap() > maxRetainedBodyCap {
			lineBuf = bytes.Buffer{}
		}
		if err != nil {
			return err
		}

		if valid && onRecord != nil {
			onRecord(commitRecord{
				hash:       hash,
				authorName: authorName,
				body:       body,
				additions:  additions,
				deletions:  deletions,
			})
		}

		if !hasNext {
			return nil
		}
	}
}

func readUntilMarker(br *bufio.Reader, marker []byte, dst *bytes.Buffer) (found bool, err error) {
	if len(marker) == 0 {
		return true, nil
	}
	first := marker[0]
	rest := marker[1:]

	for {
		chunk, err := br.ReadSlice(first)
		if errors.Is(err, bufio.ErrBufferFull) {
			if dst != nil {
				dst.Write(chunk)
			}
			continue
		}
		if err == nil {
			if dst != nil && len(chunk) > 1 {
				dst.Write(chunk[:len(chunk)-1])
			}
			if len(rest) == 0 {
				return true, nil
			}
			peeked, peekErr := br.Peek(len(rest))
			if len(peeked) == len(rest) && bytes.Equal(peeked, rest) {
				if _, discErr := br.Discard(len(rest)); discErr != nil {
					return false, discErr
				}
				return true, nil
			}
			if dst != nil {
				dst.WriteByte(first)
			}
			if peekErr != nil && !errors.Is(peekErr, io.EOF) {
				return false, peekErr
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			if dst != nil && len(chunk) > 0 {
				dst.Write(chunk)
			}
			return false, nil
		}
		return false, err
	}
}

func streamNumstatUntilNextCommit(br *bufio.Reader, lineBuf *bytes.Buffer) (additions int, deletions int, hasNext bool, err error) {
	lineBuf.Reset()

	for {
		if br.Buffered() == 0 {
			if _, peekErr := br.Peek(1); peekErr != nil {
				if errors.Is(peekErr, io.EOF) {
					if lineBuf.Len() > 0 {
						a, d := parseNumstatLine(lineBuf.Bytes())
						additions += a
						deletions += d
						lineBuf.Reset()
					}
					return additions, deletions, false, nil
				}
				return additions, deletions, false, peekErr
			}
		}

		avail, _ := br.Peek(br.Buffered())
		nlIdx := bytes.IndexByte(avail, '\n')
		nulIdx := bytes.IndexByte(avail, '\x00')

		if nulIdx >= 0 && (nlIdx < 0 || nulIdx < nlIdx) {
			if nulIdx > 0 {
				lineBuf.Write(avail[:nulIdx])
				if _, discErr := br.Discard(nulIdx); discErr != nil {
					return additions, deletions, false, discErr
				}
			}
			peeked, mErr := br.Peek(len(commitStartMarkerBytes))
			if len(peeked) == len(commitStartMarkerBytes) && bytes.Equal(peeked, commitStartMarkerBytes) {
				if lineBuf.Len() > 0 {
					a, d := parseNumstatLine(lineBuf.Bytes())
					additions += a
					deletions += d
					lineBuf.Reset()
				}
				if _, discErr := br.Discard(len(commitStartMarkerBytes)); discErr != nil {
					return additions, deletions, false, discErr
				}
				return additions, deletions, true, nil
			}
			if mErr != nil && !errors.Is(mErr, io.EOF) {
				return additions, deletions, false, mErr
			}
			lineBuf.WriteByte('\x00')
			if _, discErr := br.Discard(1); discErr != nil {
				return additions, deletions, false, discErr
			}
			continue
		}

		if nlIdx >= 0 {
			if lineBuf.Len() == 0 {
				a, d := parseNumstatLine(avail[:nlIdx])
				additions += a
				deletions += d
			} else {
				lineBuf.Write(avail[:nlIdx])
				a, d := parseNumstatLine(lineBuf.Bytes())
				additions += a
				deletions += d
				lineBuf.Reset()
			}
			if _, discErr := br.Discard(nlIdx + 1); discErr != nil {
				return additions, deletions, false, discErr
			}
			continue
		}

		lineBuf.Write(avail)
		if _, discErr := br.Discard(len(avail)); discErr != nil {
			return additions, deletions, false, discErr
		}
	}
}

func parseNumstatLine(line []byte) (additions int, deletions int) {
	line = bytes.TrimRight(line, "\r")
	if len(bytes.TrimSpace(line)) == 0 {
		return 0, 0
	}
	firstTab := bytes.IndexByte(line, '\t')
	if firstTab < 0 {
		return 0, 0
	}
	rest := line[firstTab+1:]
	secondTab := bytes.IndexByte(rest, '\t')
	if secondTab < 0 {
		return 0, 0
	}
	addBytes := bytes.TrimSpace(line[:firstTab])
	delBytes := bytes.TrimSpace(rest[:secondTab])
	if bytes.Equal(addBytes, numstatDashBytes) || bytes.Equal(delBytes, numstatDashBytes) {
		return 0, 0
	}
	add, errAdd := strconv.Atoi(string(addBytes))
	del, errDel := strconv.Atoi(string(delBytes))
	if errAdd != nil || errDel != nil {
		return 0, 0
	}
	return add, del
}

type commitProvenance struct {
	isAI          bool
	implementers  []string
	planners      []string
	orchestrators []string
	reviewers     []string
	committers    []string
	harnesses     []string
}

type rawTrailer struct {
	keyLower string
	value    string
}

func parseProvenance(authorName string, body string) commitProvenance {
	var out commitProvenance
	trailers := extractTrailers(body)

	var assistedByValues []string
	hasExplicitImpl := false

	for _, tr := range trailers {
		switch {
		case tr.keyLower == "assisted-by":
			out.isAI = true
			if tr.value != "" {
				assistedByValues = append(assistedByValues, tr.value)
			}
		case tr.keyLower == "x-agent-implementer":
			out.isAI = true
			models := splitModels(tr.value)
			if len(models) > 0 {
				hasExplicitImpl = true
				out.implementers = appendUnique(out.implementers, models...)
			}
		case tr.keyLower == "x-agent-planner":
			out.isAI = true
			out.planners = appendUnique(out.planners, splitModels(tr.value)...)
		case tr.keyLower == "x-agent-orchestrator":
			out.isAI = true
			out.orchestrators = appendUnique(out.orchestrators, splitModels(tr.value)...)
		case tr.keyLower == "x-agent-reviewer":
			out.isAI = true
			out.reviewers = appendUnique(out.reviewers, splitModels(tr.value)...)
		case tr.keyLower == "x-agent-committer":
			out.isAI = true
			out.committers = appendUnique(out.committers, splitModels(tr.value)...)
		case tr.keyLower == "x-agent-harness":
			out.isAI = true
			out.harnesses = appendUnique(out.harnesses, splitModels(tr.value)...)
		case strings.HasPrefix(tr.keyLower, "x-agent-"):
			out.isAI = true
		}
	}

	if !hasExplicitImpl && len(assistedByValues) > 0 {
		for _, rawVal := range assistedByValues {
			model := extractAssistedByModel(rawVal)
			if model == "" {
				continue
			}
			// Avoid misclassifying a non-implementer primary model (e.g. a Planner-only
			// commit where git-by emitted both X-Agent-Planner: openai/o3 and Assisted-by: o3)
			// while counting all Kernel-style Assisted-by-only commits and mixed commits.
			if matchesOtherRoleModel(model, out) {
				continue
			}
			out.implementers = appendUnique(out.implementers, model)
		}
	}

	if !out.isAI && hasProvenanceAuthor(authorName) {
		out.isAI = true
	}

	return out
}

func matchesOtherRoleModel(candidate string, p commitProvenance) bool {
	hasOtherRoles := len(p.planners) > 0 || len(p.orchestrators) > 0 || len(p.reviewers) > 0 || len(p.committers) > 0
	if !hasOtherRoles {
		return false
	}
	check := func(list []string) bool {
		for _, m := range list {
			if m == candidate || shortModelID(m) == candidate {
				return true
			}
		}
		return false
	}
	return check(p.planners) || check(p.orchestrators) || check(p.reviewers) || check(p.committers)
}

func shortModelID(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndexByte(model, '/'); idx >= 0 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}

// extractTrailers scans the trailing trailer block of a Git commit body.
func extractTrailers(body string) []rawTrailer {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n \t")
	if normalized == "" {
		return nil
	}

	lines := strings.Split(normalized, "\n")
	// Walk backwards over the trailing paragraph (never treating line 0, the commit subject, as a trailer).
	var rev []rawTrailer
	for i := len(lines) - 1; i >= 1; i-- {
		line := strings.TrimRight(lines[i], " \t")
		if strings.TrimSpace(line) == "" {
			break
		}
		if strings.HasPrefix(line, "(cherry picked from commit ") && strings.HasSuffix(line, ")") {
			continue
		}
		key, val, ok := parseTrailerLine(line)
		if !ok {
			break
		}
		rev = append(rev, rawTrailer{
			keyLower: strings.ToLower(key),
			value:    val,
		})
	}

	// Reverse to preserve top-to-bottom trailer order.
	for l, r := 0, len(rev)-1; l < r; l, r = l+1, r-1 {
		rev[l], rev[r] = rev[r], rev[l]
	}
	return rev
}

func parseTrailerLine(line string) (key string, value string, ok bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return "", "", false
	}
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx <= 0 {
		return "", "", false
	}
	k := line[:colonIdx]
	for i := 0; i < len(k); i++ {
		c := k[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '-' {
			return "", "", false
		}
	}
	return k, strings.TrimSpace(line[colonIdx+1:]), true
}

// extractAssistedByModel extracts <model> from "Assisted-by: [<harness>:]<model> [tools...]".
func extractAssistedByModel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Strip bracketed tool annotations like "[golangci-lint] [go-test]" and email "<...>".
	s = stripBracketedSegments(s, '[', ']')
	s = stripBracketedSegments(s, '<', '>')
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// In "[<harness>:]<model>", take the first whitespace-delimited token if followed by extras,
	// unless the value has no colons/slashes and is a multi-word display name.
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	token := fields[0]

	colonIdx := strings.IndexByte(token, ':')
	slashIdx := strings.IndexByte(token, '/')
	if colonIdx > 0 && (slashIdx == -1 || colonIdx < slashIdx) {
		model := strings.TrimSpace(token[colonIdx+1:])
		if model != "" {
			return model
		}
	}
	if len(fields) == 1 {
		return token
	}
	return strings.Join(fields, " ")
}

func stripBracketedSegments(s string, openByte, closeByte byte) string {
	var b strings.Builder
	b.Grow(len(s))
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == openByte {
			depth++
			continue
		}
		if c == closeByte && depth > 0 {
			depth--
			continue
		}
		if depth == 0 {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// hasProvenanceAuthor checks if AuthorName matches "<name> by <model>" (or "by <model>"),
// including multi-model "<name> by <model> & <N> more" and legacy "+N" suffixes,
// following git-by's provenance author convention.
func hasProvenanceAuthor(authorName string) bool {
	name := strings.TrimSpace(authorName)
	if name == "" {
		return false
	}
	var modelPart string
	if idx := strings.LastIndex(name, " by "); idx > 0 {
		base := strings.TrimSpace(name[:idx])
		if base == "" {
			return false
		}
		modelPart = strings.TrimSpace(name[idx+4:])
	} else if strings.HasPrefix(name, "by ") {
		modelPart = strings.TrimSpace(name[3:])
	} else {
		return false
	}

	modelPart = stripMultiModelSuffix(modelPart)
	return isValidProvenanceModelToken(modelPart)
}

// stripMultiModelSuffix strips a trailing " & <positive-int> more" suffix if present.
func stripMultiModelSuffix(s string) string {
	const tail = " more"
	if !strings.HasSuffix(s, tail) {
		return s
	}
	withoutMore := s[:len(s)-len(tail)]
	ampIdx := strings.LastIndex(withoutMore, " & ")
	if ampIdx <= 0 {
		return s
	}
	numStr := withoutMore[ampIdx+3:]
	if !isPositiveDecimalInt(numStr) {
		return s
	}
	return withoutMore[:ampIdx]
}

func isPositiveDecimalInt(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isValidProvenanceModelToken(tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		isLower := c >= 'a' && c <= 'z'
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		isPunct := c == '-' || c == '_' || c == '.' || c == ':' || c == '+' || c == '/' || c == '@'
		if i == 0 && !isLower && !isUpper && !isDigit {
			return false
		}
		if !isLower && !isUpper && !isDigit && !isPunct {
			return false
		}
	}
	return true
}

func splitModels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		m := strings.TrimSpace(p)
		if m != "" {
			out = appendUnique(out, m)
		}
	}
	return out
}

func appendUnique(dst []string, items ...string) []string {
	for _, item := range items {
		if item == "" {
			continue
		}
		exists := false
		for _, cur := range dst {
			if cur == item {
				exists = true
				break
			}
		}
		if !exists {
			dst = append(dst, item)
		}
	}
	return dst
}

func buildLeaderboard(counts map[string]int, aiCommits int, topN int, nilIfEmpty bool) []ModelCount {
	if len(counts) == 0 {
		if nilIfEmpty {
			return nil
		}
		return []ModelCount{}
	}
	items := make([]ModelCount, 0, len(counts))
	for model, count := range counts {
		pct := 0.0
		if aiCommits > 0 {
			pct = round2(float64(count) / float64(aiCommits) * 100.0)
		}
		items = append(items, ModelCount{
			Model:   model,
			Commits: count,
			Percent: pct,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Commits != items[j].Commits {
			return items[i].Commits > items[j].Commits
		}
		return items[i].Model < items[j].Model
	})
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	return items
}

func detectRepoName(absRepoDir string) string {
	stdout, _, err := runGit(absRepoDir, "rev-parse", "--show-toplevel")
	if err == nil {
		top := strings.TrimSpace(string(stdout))
		if top != "" {
			if !filepath.IsAbs(top) {
				top = filepath.Join(absRepoDir, top)
			}
			base := filepath.Base(filepath.Clean(top))
			if base != "" && base != "." && base != string(filepath.Separator) {
				return base
			}
		}
	}
	return filepath.Base(filepath.Clean(absRepoDir))
}

func isEmptyRepoStderr(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "does not have any commits yet") ||
		strings.Contains(lower, "bad default revision 'head'") ||
		strings.Contains(lower, "ambiguous argument 'head'")
}

func runGit(dir string, args ...string) ([]byte, string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, stderr.String(), err
		}
		return nil, stderr.String(), err
	}
	return stdout.Bytes(), stderr.String(), nil
}

func round2(v float64) float64 {
	return math.Round(v*100.0) / 100.0
}

func normalizeReport(r *Report) *Report {
	if r == nil {
		return &Report{
			RepoName:     "repository",
			Implementers: []ModelCount{},
			Planners:     []ModelCount{},
		}
	}
	cp := *r
	if strings.TrimSpace(cp.RepoName) == "" {
		cp.RepoName = "repository"
	}
	if cp.Implementers == nil {
		cp.Implementers = []ModelCount{}
	}
	if cp.Planners == nil {
		cp.Planners = []ModelCount{}
	}
	return &cp
}
