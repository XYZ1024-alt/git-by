package stats

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// RenderBadgeSVG generates a standalone GitHub-Stats dark rounded SVG card for r.
func RenderBadgeSVG(r *Report) ([]byte, error) {
	norm := normalizeReport(r)

	const (
		cardWidth = 720
		padX      = 24
		innerW    = cardWidth - 2*padX
	)

	implRows := norm.Implementers
	if len(implRows) > 5 {
		implRows = implRows[:5]
	}
	planRows := norm.Planners
	if len(planRows) > 5 {
		planRows = planRows[:5]
	}

	maxModelRows := len(implRows)
	if len(planRows) > maxModelRows {
		maxModelRows = len(planRows)
	}
	if maxModelRows == 0 {
		maxModelRows = 1
	}

	const (
		headerHeight  = 74
		stackedHeight = 112
		modelsTopY    = headerHeight + stackedHeight + 18
		rowStepY      = 34
		footerHeight  = 38
	)
	modelsHeight := 30 + maxModelRows*rowStepY
	cardHeight := modelsTopY + modelsHeight + footerHeight

	var b bytes.Buffer
	b.Grow(4096)

	titleEsc := xmlEscape(fmt.Sprintf("%s — AI Provenance Stats", norm.RepoName))
	descEsc := xmlEscape(fmt.Sprintf(
		"Total commits: %d (AI %.1f%%, Human %.1f%%), Total LOC: %d",
		norm.TotalCommits, norm.AI.CommitPercent, norm.Human.CommitPercent, norm.TotalLines,
	))

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="git-by-title git-by-desc">`+"\n",
		cardWidth, cardHeight, cardWidth, cardHeight)
	fmt.Fprintf(&b, `  <title id="git-by-title">%s</title>`+"\n", titleEsc)
	fmt.Fprintf(&b, `  <desc id="git-by-desc">%s</desc>`+"\n", descEsc)

	// Gradients & styles
	b.WriteString(`  <defs>
    <linearGradient id="bgGrad" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#0d1117"/>
      <stop offset="100%" stop-color="#161b22"/>
    </linearGradient>
    <linearGradient id="accentTop" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#58a6ff"/>
      <stop offset="50%" stop-color="#a371f7"/>
      <stop offset="100%" stop-color="#3fb950"/>
    </linearGradient>
    <linearGradient id="aiBarGrad" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#58a6ff"/>
      <stop offset="100%" stop-color="#a371f7"/>
    </linearGradient>
    <linearGradient id="planBarGrad" x1="0%" y1="0%" x2="100%" y2="0%">
      <stop offset="0%" stop-color="#a371f7"/>
      <stop offset="100%" stop-color="#db61a2"/>
    </linearGradient>
    <clipPath id="commitBarClip">
      <rect x="24" y="102" width="672" height="12" rx="6"/>
    </clipPath>
    <clipPath id="locBarClip">
      <rect x="24" y="154" width="672" height="12" rx="6"/>
    </clipPath>
  </defs>
  <style>
    .sans { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans', Helvetica, Arial, sans-serif; }
    .mono { font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace; }
    .repo-title { fill: #f0f6fc; font-size: 17px; font-weight: 700; }
    .subtitle { fill: #8b949e; font-size: 12px; font-weight: 400; }
    .pill-bg { fill: #21262d; stroke: #30363d; stroke-width: 1; }
    .pill-text { fill: #c9d1d9; font-size: 11px; font-weight: 600; }
    .section-label { fill: #c9d1d9; font-size: 12px; font-weight: 600; }
    .legend { fill: #8b949e; font-size: 11px; }
    .col-header { fill: #f0f6fc; font-size: 12px; font-weight: 600; letter-spacing: 0.2px; }
    .model-name { fill: #c9d1d9; font-size: 11px; }
    .model-stat { fill: #8b949e; font-size: 10.5px; }
    .empty-note { fill: #6e7681; font-size: 11px; font-style: italic; }
    .footer-text { fill: #6e7681; font-size: 10.5px; }
  </style>
`)

	// Outer dark card + top accent strip
	fmt.Fprintf(&b, `  <rect x="0.75" y="0.75" width="%.2f" height="%.2f" rx="12" fill="url(#bgGrad)" stroke="#30363d" stroke-width="1.5"/>`+"\n",
		float64(cardWidth)-1.5, float64(cardHeight)-1.5)
	fmt.Fprintf(&b, `  <rect x="24" y="1.5" width="%d" height="2.5" rx="1.25" fill="url(#accentTop)"/>`+"\n", innerW)

	// Header: repo icon + repo name + subtitle
	b.WriteString(`  <g transform="translate(24, 22)">
    <rect x="0" y="0" width="32" height="32" rx="8" fill="#21262d" stroke="#30363d" stroke-width="1"/>
    <circle cx="11" cy="11" r="2.5" fill="#58a6ff"/>
    <circle cx="21" cy="11" r="2.5" fill="#a371f7"/>
    <circle cx="16" cy="21" r="2.5" fill="#3fb950"/>
    <path d="M11 13.5 L16 18.5 M21 13.5 L16 18.5" stroke="#8b949e" stroke-width="1.4" stroke-linecap="round"/>
  </g>
`)
	repoDisplay := xmlEscape(truncateRunes(norm.RepoName, 34))
	subText := "git-by · Lossless Multi-Agent Provenance"
	if norm.Since != "" {
		subText = fmt.Sprintf("git-by · Since %s", truncateRunes(norm.Since, 24))
	}
	fmt.Fprintf(&b, `  <text x="68" y="37" class="sans repo-title">%s</text>`+"\n", repoDisplay)
	fmt.Fprintf(&b, `  <text x="68" y="53" class="sans subtitle">%s</text>`+"\n", xmlEscape(subText))

	// Right-aligned summary pill
	pillLabel := fmt.Sprintf("%s commits · %s LOC · %.1f%% AI",
		formatInt(norm.TotalCommits), formatInt(norm.TotalLines), norm.AI.CommitPercent)
	pillW := maxInt(180, len(pillLabel)*7+20)
	pillX := cardWidth - padX - pillW
	fmt.Fprintf(&b, `  <rect x="%d" y="25" width="%d" height="26" rx="13" class="pill-bg"/>`+"\n", pillX, pillW)
	fmt.Fprintf(&b, `  <text x="%d" y="42" text-anchor="middle" class="mono pill-text">%s</text>`+"\n",
		pillX+pillW/2, xmlEscape(pillLabel))

	// Header divider
	fmt.Fprintf(&b, `  <line x1="%d" y1="68" x2="%d" y2="68" stroke="#21262d" stroke-width="1"/>`+"\n", padX, cardWidth-padX)

	// Stacked Bar 1: Human vs AI Commits
	commitLegend := fmt.Sprintf("AI %s (%.1f%%)  ·  Human %s (%.1f%%)",
		formatInt(norm.AI.Commits), norm.AI.CommitPercent,
		formatInt(norm.Human.Commits), norm.Human.CommitPercent)
	fmt.Fprintf(&b, `  <text x="%d" y="93" class="sans section-label">Commits Breakdown</text>`+"\n", padX)
	fmt.Fprintf(&b, `  <text x="%d" y="93" text-anchor="end" class="mono legend">%s</text>`+"\n",
		cardWidth-padX, xmlEscape(commitLegend))
	writeStackedBar(&b, padX, 102, innerW, 12, "commitBarClip", norm.TotalCommits > 0, norm.AI.CommitPercent)

	// Stacked Bar 2: Human vs AI LOC
	locLegend := fmt.Sprintf("AI +%s/-%s (%.1f%%)  ·  Human +%s/-%s (%.1f%%)",
		formatInt(norm.AI.Additions), formatInt(norm.AI.Deletions), norm.AI.LinesPercent,
		formatInt(norm.Human.Additions), formatInt(norm.Human.Deletions), norm.Human.LinesPercent)
	fmt.Fprintf(&b, `  <text x="%d" y="145" class="sans section-label">Changed Lines (+/-)</text>`+"\n", padX)
	fmt.Fprintf(&b, `  <text x="%d" y="145" text-anchor="end" class="mono legend">%s</text>`+"\n",
		cardWidth-padX, xmlEscape(locLegend))
	writeStackedBar(&b, padX, 154, innerW, 12, "locBarClip", norm.TotalLines > 0, norm.AI.LinesPercent)

	// Section divider before model columns
	divY := modelsTopY - 12
	fmt.Fprintf(&b, `  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#21262d" stroke-width="1"/>`+"\n",
		padX, divY, cardWidth-padX, divY)

	// Two columns: Top Implementer Models | Top Planner Models
	const colGap = 28
	colW := (innerW - colGap) / 2
	leftX := padX
	rightX := padX + colW + colGap

	writeModelColumn(&b, leftX, modelsTopY, colW, "Top Implementer Models", implRows, "url(#aiBarGrad)")
	writeModelColumn(&b, rightX, modelsTopY, colW, "Top Planner Models", planRows, "url(#planBarGrad)")

	// Footer
	footerDivY := cardHeight - footerHeight + 6
	fmt.Fprintf(&b, `  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#21262d" stroke-width="1"/>`+"\n",
		padX, footerDivY, cardWidth-padX, footerDivY)
	footerY := cardHeight - 12
	fmt.Fprintf(&b, `  <circle cx="%d" cy="%d" r="4" fill="#a371f7"/>`+"\n", padX+6, footerY-3)
	fmt.Fprintf(&b, `  <text x="%d" y="%d" class="sans footer-text">AI Assisted</text>`+"\n", padX+16, footerY)
	fmt.Fprintf(&b, `  <circle cx="%d" cy="%d" r="4" fill="#3fb950"/>`+"\n", padX+96, footerY-3)
	fmt.Fprintf(&b, `  <text x="%d" y="%d" class="sans footer-text">Human Authored</text>`+"\n", padX+106, footerY)
	fmt.Fprintf(&b, `  <text x="%d" y="%d" text-anchor="end" class="mono footer-text">Generated by git-by</text>`+"\n",
		cardWidth-padX, footerY)

	b.WriteString("</svg>\n")
	return b.Bytes(), nil
}

func writeStackedBar(b *bytes.Buffer, x, y, width, height int, clipID string, hasData bool, aiPercent float64) {
	fmt.Fprintf(b, `  <rect x="%d" y="%d" width="%d" height="%d" rx="%d" fill="#21262d"/>`+"\n",
		x, y, width, height, height/2)
	if !hasData {
		return
	}
	if aiPercent < 0 {
		aiPercent = 0
	}
	if aiPercent > 100 {
		aiPercent = 100
	}
	aiW := float64(width) * (aiPercent / 100.0)
	humanW := float64(width) - aiW

	fmt.Fprintf(b, `  <g clip-path="url(#%s)">`+"\n", clipID)
	if aiW > 0 {
		fmt.Fprintf(b, `    <rect x="%.2f" y="%d" width="%.2f" height="%d" fill="url(#aiBarGrad)"/>`+"\n",
			float64(x), y, aiW, height)
	}
	if humanW > 0 {
		fmt.Fprintf(b, `    <rect x="%.2f" y="%d" width="%.2f" height="%d" fill="#3fb950"/>`+"\n",
			float64(x)+aiW, y, humanW, height)
	}
	b.WriteString("  </g>\n")
}

func writeModelColumn(b *bytes.Buffer, x, topY, width int, heading string, rows []ModelCount, fill string) {
	fmt.Fprintf(b, `  <text x="%d" y="%d" class="sans col-header">%s</text>`+"\n",
		x, topY+12, xmlEscape(heading))

	if len(rows) == 0 {
		fmt.Fprintf(b, `  <text x="%d" y="%d" class="sans empty-note">No models recorded yet</text>`+"\n",
			x, topY+38)
		return
	}

	for i, row := range rows {
		rowY := topY + 32 + i*34
		label := xmlEscape(truncateRunes(row.Model, 32))
		stat := xmlEscape(fmt.Sprintf("%s (%.1f%%)", formatInt(row.Commits), row.Percent))

		fmt.Fprintf(b, `  <text x="%d" y="%d" class="mono model-name">%s</text>`+"\n", x, rowY, label)
		fmt.Fprintf(b, `  <text x="%d" y="%d" text-anchor="end" class="mono model-stat">%s</text>`+"\n",
			x+width, rowY, stat)

		barY := rowY + 6
		fmt.Fprintf(b, `  <rect x="%d" y="%d" width="%d" height="6" rx="3" fill="#21262d"/>`+"\n",
			x, barY, width)

		pct := row.Percent
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		fillW := float64(width) * (pct / 100.0)
		if pct > 0 && fillW < 4 {
			fillW = 4
		}
		if fillW > float64(width) {
			fillW = float64(width)
		}
		if fillW > 0 {
			fillW = math.Round(fillW*100) / 100
			fmt.Fprintf(b, `  <rect x="%d" y="%d" width="%.2f" height="6" rx="3" fill="%s"/>`+"\n",
				x, barY, fillW, fill)
		}
	}
}

// WriteBadgeSVG renders r as an SVG card and writes it atomically to outputPath.
func WriteBadgeSVG(outputPath string, r *Report) error {
	trimmed := strings.TrimSpace(outputPath)
	if trimmed == "" {
		return fmt.Errorf("empty badge output path")
	}
	data, err := RenderBadgeSVG(r)
	if err != nil {
		return err
	}

	dir := filepath.Dir(trimmed)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create badge output directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".git-by-badge-*.svg")
	if err != nil {
		return fmt.Errorf("create temp badge file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp badge file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp badge file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp badge file: %w", err)
	}
	if err := os.Rename(tmpName, trimmed); err != nil {
		return fmt.Errorf("rename badge file to %q: %w", trimmed, err)
	}
	cleanup = false
	return nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	buf.Grow(len(s))
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
