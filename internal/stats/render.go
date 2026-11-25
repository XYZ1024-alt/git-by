package stats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[90m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35m"
	ansiGreen   = "\x1b[32m"
	ansiRed     = "\x1b[31m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
)

// RenderJSON writes r as formatted JSON to w.
func RenderJSON(w io.Writer, r *Report) error {
	if w == nil {
		return fmt.Errorf("nil writer")
	}
	norm := normalizeReport(r)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(norm)
}

// RenderTerminal writes an aligned Unicode dashboard of r to w, optionally styled with ANSI colors.
func RenderTerminal(w io.Writer, r *Report, color bool) error {
	if w == nil {
		return fmt.Errorf("nil writer")
	}
	norm := normalizeReport(r)
	p := palette{enabled: color}

	var buf bytes.Buffer
	const innerWidth = 68

	// Header Box
	topBorder := "╭" + strings.Repeat("─", innerWidth) + "╮"
	midBorder := "├" + strings.Repeat("─", innerWidth) + "┤"
	botBorder := "╰" + strings.Repeat("─", innerWidth) + "╯"

	fmt.Fprintln(&buf, p.wrap(ansiDim, topBorder))
	titlePlain := "  git-by · AI Provenance Analytics"
	titlePad := maxInt(0, innerWidth-utf8.RuneCountInString(titlePlain))
	fmt.Fprintf(&buf, "%s%s%s%s\n",
		p.wrap(ansiDim, "│"),
		p.wrap(ansiBold+ansiCyan, titlePlain),
		strings.Repeat(" ", titlePad),
		p.wrap(ansiDim, "│"),
	)

	subPlain := fmt.Sprintf("  Repo: %s", truncateRunes(norm.RepoName, 30))
	if norm.Since != "" {
		subPlain += fmt.Sprintf("  ·  Since: %s", truncateRunes(norm.Since, 22))
	} else {
		subPlain += "  ·  Window: all non-merge commits"
	}
	subPlain = truncateRunes(subPlain, innerWidth-1)
	subPad := maxInt(0, innerWidth-utf8.RuneCountInString(subPlain))
	fmt.Fprintf(&buf, "%s%s%s%s\n",
		p.wrap(ansiDim, "│"),
		subPlain,
		strings.Repeat(" ", subPad),
		p.wrap(ansiDim, "│"),
	)
	fmt.Fprintln(&buf, p.wrap(ansiDim, midBorder))

	totalAdd := norm.Human.Additions + norm.AI.Additions
	totalDel := norm.Human.Deletions + norm.AI.Deletions
	summaryLeft := fmt.Sprintf("  Total Commits: %s", formatInt(norm.TotalCommits))
	summaryMid := fmt.Sprintf("  Changed LOC: %s", formatInt(norm.TotalLines))
	summaryRight := fmt.Sprintf("(+%s / -%s)", formatInt(totalAdd), formatInt(totalDel))
	summaryPlainLen := utf8.RuneCountInString(summaryLeft) + utf8.RuneCountInString(summaryMid) + 1 + utf8.RuneCountInString(summaryRight)
	summaryPad := maxInt(0, innerWidth-summaryPlainLen)

	coloredRight := fmt.Sprintf("(%s / %s)",
		p.wrap(ansiGreen, "+"+formatInt(totalAdd)),
		p.wrap(ansiRed, "-"+formatInt(totalDel)),
	)
	fmt.Fprintf(&buf, "%s%s%s %s%s%s\n",
		p.wrap(ansiDim, "│"),
		p.wrap(ansiBold, summaryLeft),
		summaryMid,
		coloredRight,
		strings.Repeat(" ", summaryPad),
		p.wrap(ansiDim, "│"),
	)
	fmt.Fprintln(&buf, p.wrap(ansiDim, botBorder))
	fmt.Fprintln(&buf)

	// Human vs AI Breakdown
	fmt.Fprintln(&buf, p.wrap(ansiBold, "▸ Human vs AI Breakdown"))
	const barWidth = 24

	fmt.Fprintf(&buf, "  %-14s %s  %6.1f%%  %s commits\n",
		"AI Commits",
		renderProgressBar(norm.AI.CommitPercent, barWidth, p, ansiMagenta),
		norm.AI.CommitPercent,
		padLeft(formatInt(norm.AI.Commits), 6),
	)
	fmt.Fprintf(&buf, "  %-14s %s  %6.1f%%  %s commits\n",
		"Human Commits",
		renderProgressBar(norm.Human.CommitPercent, barWidth, p, ansiGreen),
		norm.Human.CommitPercent,
		padLeft(formatInt(norm.Human.Commits), 6),
	)
	fmt.Fprintf(&buf, "  %-14s %s  %6.1f%%  %s  %s\n",
		"AI Lines",
		renderProgressBar(norm.AI.LinesPercent, barWidth, p, ansiCyan),
		norm.AI.LinesPercent,
		p.wrap(ansiGreen, padLeft("+"+formatInt(norm.AI.Additions), 8)),
		p.wrap(ansiRed, padLeft("-"+formatInt(norm.AI.Deletions), 8)),
	)
	fmt.Fprintf(&buf, "  %-14s %s  %6.1f%%  %s  %s\n",
		"Human Lines",
		renderProgressBar(norm.Human.LinesPercent, barWidth, p, ansiBlue),
		norm.Human.LinesPercent,
		p.wrap(ansiGreen, padLeft("+"+formatInt(norm.Human.Additions), 8)),
		p.wrap(ansiRed, padLeft("-"+formatInt(norm.Human.Deletions), 8)),
	)

	// Role Leaderboards
	renderRoleSection(&buf, "Top Implementer Models", norm.Implementers, true, p, ansiCyan)
	renderRoleSection(&buf, "Top Planner Models", norm.Planners, true, p, ansiMagenta)
	renderRoleSection(&buf, "Top Orchestrator Models", norm.Orchestrators, false, p, ansiYellow)
	renderRoleSection(&buf, "Top Reviewer Models", norm.Reviewers, false, p, ansiGreen)
	renderRoleSection(&buf, "Top Committer Models", norm.Committers, false, p, ansiBlue)
	renderRoleSection(&buf, "Top Agent Harnesses", norm.Harnesses, false, p, ansiCyan)

	_, err := w.Write(buf.Bytes())
	return err
}

func renderRoleSection(buf *bytes.Buffer, title string, items []ModelCount, alwaysShow bool, p palette, barColor string) {
	if len(items) == 0 && !alwaysShow {
		return
	}
	fmt.Fprintln(buf)
	fmt.Fprintln(buf, p.wrap(ansiBold, "▸ "+title))
	if len(items) == 0 {
		fmt.Fprintln(buf, p.wrap(ansiDim, "  (none recorded)"))
		return
	}

	modelColWidth := 28
	for _, it := range items {
		l := utf8.RuneCountInString(it.Model)
		if l > modelColWidth {
			modelColWidth = l
		}
	}
	if modelColWidth > 38 {
		modelColWidth = 38
	}

	const roleBarWidth = 18
	for idx, it := range items {
		rankStr := fmt.Sprintf("%d.", idx+1)
		name := padRight(truncateRunes(it.Model, modelColWidth), modelColWidth)
		bar := renderProgressBar(it.Percent, roleBarWidth, p, barColor)
		unit := "commits"
		if it.Commits == 1 {
			unit = "commit "
		}
		fmt.Fprintf(buf, "  %s %s  %s  %6.1f%%  %s %s\n",
			p.wrap(ansiDim, padLeft(rankStr, 2)),
			name,
			bar,
			it.Percent,
			padLeft(formatInt(it.Commits), 5),
			unit,
		)
	}
}

func renderProgressBar(percent float64, width int, p palette, fillColor string) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(math.Round(percent / 100.0 * float64(width)))
	if percent > 0 && filled == 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	empty := width - filled

	filledStr := strings.Repeat("█", filled)
	emptyStr := strings.Repeat("░", empty)
	if !p.enabled {
		return filledStr + emptyStr
	}
	var out strings.Builder
	if filled > 0 {
		out.WriteString(p.wrap(fillColor, filledStr))
	}
	if empty > 0 {
		out.WriteString(p.wrap(ansiDim, emptyStr))
	}
	return out.String()
}

type palette struct {
	enabled bool
}

func (p palette) wrap(code, text string) string {
	if !p.enabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

func formatInt(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
		if len(s) > rem {
			b.WriteByte(',')
		}
	}
	for i := rem; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	runes := []rune(s)
	return string(runes[:maxRunes-1]) + "…"
}

func padRight(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func padLeft(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return strings.Repeat(" ", width-n) + s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
