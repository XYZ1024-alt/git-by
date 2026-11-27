package stats

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// BadgeTheme selects the color palette embedded in the generated SVG card.
type BadgeTheme string

const (
	ThemeAuto  BadgeTheme = "auto"
	ThemeDark  BadgeTheme = "dark"
	ThemeLight BadgeTheme = "light"
)

// ParseBadgeTheme parses "auto", "dark", or "light" (case-insensitive; empty defaults to ThemeAuto).
func ParseBadgeTheme(s string) (BadgeTheme, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ThemeAuto):
		return ThemeAuto, nil
	case string(ThemeDark):
		return ThemeDark, nil
	case string(ThemeLight):
		return ThemeLight, nil
	default:
		return "", fmt.Errorf("invalid badge theme %q (expected auto, dark, or light)", s)
	}
}

const (
	svgDarkVars = `      --bg-card: #0d1117;
      --bg-panel: #161b22;
      --bg-track: #21262d;
      --border: #30363d;
      --divider: #21262d;
      --text-primary: #f0f6fc;
      --text-body: #c9d1d9;
      --text-muted: #8b949e;
      --text-empty: #6e7681;
      --accent-blue: #58a6ff;
      --accent-blue-2: #79c0ff;
      --accent-blue-3: #a5d6ff;
      --accent-blue-4: #388bfd;
      --accent-purple: #a371f7;
      --accent-purple-2: #d2a8ff;
      --accent-purple-3: #bc8cff;
      --accent-purple-4: #8957e5;
      --accent-green: #3fb950;
      --accent-green-bar: #2ea043;
      --grad-commit-start: #388bfd;
      --grad-commit-end: #58a6ff;
      --grad-loc-start: #8957e5;
      --grad-loc-end: #a371f7;`

	svgLightVars = `      --bg-card: #ffffff;
      --bg-panel: #f6f8fa;
      --bg-track: #eaeef2;
      --border: #d0d7de;
      --divider: #d8dee4;
      --text-primary: #1f2328;
      --text-body: #24292f;
      --text-muted: #656d76;
      --text-empty: #6e7781;
      --accent-blue: #0969da;
      --accent-blue-2: #218bff;
      --accent-blue-3: #54aeff;
      --accent-blue-4: #0550ae;
      --accent-purple: #8250df;
      --accent-purple-2: #a475f9;
      --accent-purple-3: #c297ff;
      --accent-purple-4: #6639ba;
      --accent-green: #1a7f37;
      --accent-green-bar: #2da44e;
      --grad-commit-start: #0969da;
      --grad-commit-end: #218bff;
      --grad-loc-start: #8250df;
      --grad-loc-end: #a475f9;`
)

type svgGradientFallbacks struct {
	accentBlue   string
	accentPurple string
	accentGreen  string
	commitStart  string
	commitEnd    string
	locStart     string
	locEnd       string
}

func gradientFallbacksForTheme(theme BadgeTheme) svgGradientFallbacks {
	if theme == ThemeLight {
		return svgGradientFallbacks{
			accentBlue:   "#0969da",
			accentPurple: "#8250df",
			accentGreen:  "#1a7f37",
			commitStart:  "#0969da",
			commitEnd:    "#218bff",
			locStart:     "#8250df",
			locEnd:       "#a475f9",
		}
	}
	return svgGradientFallbacks{
		accentBlue:   "#58a6ff",
		accentPurple: "#a371f7",
		accentGreen:  "#3fb950",
		commitStart:  "#388bfd",
		commitEnd:    "#58a6ff",
		locStart:     "#8957e5",
		locEnd:       "#a371f7",
	}
}

// RenderBadgeSVG renders r as an 840px standalone SVG card using ThemeAuto.
func RenderBadgeSVG(r *Report) ([]byte, error) {
	return RenderBadgeSVGWithTheme(r, ThemeAuto)
}

// RenderBadgeSVGWithTheme renders r as an 840px standalone SVG card using theme.
func RenderBadgeSVGWithTheme(r *Report, theme BadgeTheme) ([]byte, error) {
	resolvedTheme, err := ParseBadgeTheme(string(theme))
	if err != nil {
		return nil, err
	}
	norm := normalizeReport(r)

	const (
		cardWidth    = 840
		maxSVGRows   = 10
		section2TopY = 198
		rowStepY     = 32
		harnessH     = 40
	)

	implRows := norm.Implementers
	if len(implRows) > maxSVGRows {
		implRows = implRows[:maxSVGRows]
	}
	planRows := norm.Planners
	if len(planRows) > maxSVGRows {
		planRows = planRows[:maxSVGRows]
	}

	maxRows := maxInt(1, maxInt(len(implRows), len(planRows)))
	modelCardH := 44 + maxRows*rowStepY + 10
	harnessY := section2TopY + modelCardH + 12
	footerY := harnessY + harnessH + 26
	cardHeight := footerY + 18
	outerInnerH := cardHeight - 2

	var b bytes.Buffer
	b.Grow(6144)

	titleEsc := xmlEscape(fmt.Sprintf("%s — AI Provenance Analytics", norm.RepoName))
	descEsc := xmlEscape(fmt.Sprintf(
		"AI provenance report for %s: %d commits (AI %.1f%%, Human %.1f%%), %d LOC changed.",
		norm.RepoName, norm.TotalCommits, norm.AI.CommitPercent, norm.Human.CommitPercent, norm.TotalLines,
	))

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="git-by-card-title git-by-card-desc">`+"\n",
		cardWidth, cardHeight, cardWidth, cardHeight)
	fmt.Fprintf(&b, `  <title id="git-by-card-title">%s</title>`+"\n", titleEsc)
	fmt.Fprintf(&b, `  <desc id="git-by-card-desc">%s</desc>`+"\n\n", descEsc)

	// Gradients & clip paths (CSS variable classes + static base-theme hex fallbacks on every <stop>).
	fb := gradientFallbacksForTheme(resolvedTheme)
	fmt.Fprintf(&b, `  <defs>
    <linearGradient id="header-accent" x1="0%%" y1="0%%" x2="100%%" y2="0%%">
      <stop offset="0%%" class="stop-accent-blue" stop-color="%s"/>
      <stop offset="50%%" class="stop-accent-purple" stop-color="%s"/>
      <stop offset="100%%" class="stop-accent-green" stop-color="%s"/>
    </linearGradient>
    <linearGradient id="ai-commit-grad" x1="0%%" y1="0%%" x2="100%%" y2="0%%">
      <stop offset="0%%" class="stop-commit-start" stop-color="%s"/>
      <stop offset="100%%" class="stop-commit-end" stop-color="%s"/>
    </linearGradient>
    <linearGradient id="ai-loc-grad" x1="0%%" y1="0%%" x2="100%%" y2="0%%">
      <stop offset="0%%" class="stop-loc-start" stop-color="%s"/>
      <stop offset="100%%" class="stop-loc-end" stop-color="%s"/>
    </linearGradient>
    <clipPath id="outer-clip">
      <rect x="1" y="1" width="838" height="%d" rx="12"/>
    </clipPath>
    <clipPath id="commit-bar-clip">
      <rect x="36" y="114" width="768" height="10" rx="5"/>
    </clipPath>
    <clipPath id="loc-bar-clip">
      <rect x="36" y="160" width="768" height="10" rx="5"/>
    </clipPath>
  </defs>
`,
		fb.accentBlue, fb.accentPurple, fb.accentGreen,
		fb.commitStart, fb.commitEnd,
		fb.locStart, fb.locEnd,
		outerInnerH,
	)

	writeSVGThemeStyle(&b, resolvedTheme)

	// Outer Card Background & Header
	fmt.Fprintf(&b, `
  <!-- Outer Card Background &amp; Header -->
  <g clip-path="url(#outer-clip)">
    <rect x="1" y="1" width="838" height="%d" class="card-bg"/>
    <rect x="1" y="1" width="838" height="64" class="panel-bg"/>
    <rect x="1" y="1" width="838" height="3" fill="url(#header-accent)"/>
    <line x1="1" y1="65" x2="839" y2="65" class="header-divider"/>
  </g>
  <rect x="1" y="1" width="838" height="%d" rx="12" class="card-border"/>
`, outerInnerH, outerInnerH)

	// Header Content: 3-node Git topology icon + repo name + dynamic AI PROVENANCE pill + right summary
	b.WriteString(`
  <!-- Header Content -->
  <g transform="translate(24, 22)">
    <circle cx="6" cy="6" r="3" class="icon-blue"/>
    <circle cx="18" cy="6" r="3" class="icon-purple"/>
    <circle cx="6" cy="18" r="3" class="icon-green"/>
    <path d="M6 9 L6 15 M18 9 C18 13, 6 11, 6 15" class="icon-edge"/>
  </g>
`)
	repoDisplay := truncateRunes(norm.RepoName, 22)
	pillX := 54 + utf8.RuneCountInString(repoDisplay)*11 + 14
	if pillX < 112 {
		pillX = 112
	}
	if pillX > 310 {
		pillX = 310
	}
	headerMeta := fmt.Sprintf("%s Commits · %s LOC · Kernel Assisted-by Spec",
		formatInt(norm.TotalCommits), formatInt(norm.TotalLines))
	if norm.Since != "" {
		headerMeta = fmt.Sprintf("%s Commits · %s LOC · Since %s",
			formatInt(norm.TotalCommits), formatInt(norm.TotalLines), truncateRunes(norm.Since, 18))
	}

	fmt.Fprintf(&b, `  <text x="54" y="40" class="mono title">%s</text>`+"\n", xmlEscape(repoDisplay))
	fmt.Fprintf(&b, `  <rect x="%d" y="23" width="124" height="22" rx="11" class="pill-bg"/>`+"\n", pillX)
	fmt.Fprintf(&b, `  <text x="%d" y="38" text-anchor="middle" class="sans badge-txt">AI PROVENANCE</text>`+"\n", pillX+62)
	fmt.Fprintf(&b, `  <text x="816" y="39" text-anchor="end" class="mono meta">%s</text>`+"\n", xmlEscape(headerMeta))

	// Section 1: Human vs AI Commit & LOC Ratio
	b.WriteString(`
  <!-- Section 1: Human vs AI Commit and LOC Ratio -->
  <rect x="20" y="80" width="800" height="106" rx="8" class="panel-card"/>
`)

	// Commit Ratio Row
	aiCommitLegend := fmt.Sprintf("AI-Assisted: %s (%.1f%%)", formatInt(norm.AI.Commits), norm.AI.CommitPercent)
	humanCommitLegend := fmt.Sprintf("Human Solo: %s (%.1f%%)", formatInt(norm.Human.Commits), norm.Human.CommitPercent)
	b.WriteString(`  <text x="36" y="105" class="sans sec-label">COMMITS</text>` + "\n")
	b.WriteString(`  <circle cx="110" cy="101" r="4" class="dot-blue"/>` + "\n")
	fmt.Fprintf(&b, `  <text x="120" y="105" class="mono model-name">%s</text>`+"\n", xmlEscape(aiCommitLegend))
	fmt.Fprintf(&b, `  <text x="792" y="105" text-anchor="end" class="mono model-name">%s</text>`+"\n", xmlEscape(humanCommitLegend))
	b.WriteString(`  <circle cx="800" cy="101" r="4" class="dot-green"/>` + "\n")
	writeRatioBar(&b, 36, 114, 768, 10, "commit-bar-clip", "ai-commit-grad", norm.TotalCommits > 0, norm.AI.Commits, norm.Human.Commits, norm.AI.CommitPercent)

	// LOC Ratio Row
	aiLOCLegend := fmt.Sprintf("AI: +%s / -%s (%.1f%%)", formatInt(norm.AI.Additions), formatInt(norm.AI.Deletions), norm.AI.LinesPercent)
	humanLOCLegend := fmt.Sprintf("Human: +%s / -%s (%.1f%%)", formatInt(norm.Human.Additions), formatInt(norm.Human.Deletions), norm.Human.LinesPercent)
	b.WriteString(`  <text x="36" y="151" class="sans sec-label">LOC CHANGED</text>` + "\n")
	b.WriteString(`  <circle cx="136" cy="147" r="4" class="dot-purple"/>` + "\n")
	fmt.Fprintf(&b, `  <text x="146" y="151" class="mono model-name">%s</text>`+"\n", xmlEscape(aiLOCLegend))
	fmt.Fprintf(&b, `  <text x="792" y="151" text-anchor="end" class="mono model-name">%s</text>`+"\n", xmlEscape(humanLOCLegend))
	b.WriteString(`  <circle cx="800" cy="147" r="4" class="dot-green"/>` + "\n")
	writeRatioBar(&b, 36, 160, 768, 10, "loc-bar-clip", "ai-loc-grad", norm.TotalLines > 0, norm.AI.Additions+norm.AI.Deletions, norm.Human.Additions+norm.Human.Deletions, norm.AI.LinesPercent)

	// Section 2: Equal-Height Model Leaderboard Cards
	writeModelCard(&b, 20, section2TopY, 392, modelCardH, "TOP IMPLEMENTERS", "X-Agent-Implementer", implRows, "impl-bar-")
	writeModelCard(&b, 428, section2TopY, 392, modelCardH, "TOP PLANNERS", "X-Agent-Planner", planRows, "plan-bar-")

	// Section 3: Dedicated Full-Width HARNESSES Bar
	harnessTextY := harnessY + 24
	fmt.Fprintf(&b, `
  <!-- Section 3: Harnesses Bar -->
  <rect x="20" y="%d" width="800" height="%d" rx="8" class="panel-card"/>
  <text x="36" y="%d" class="sans sec-label">HARNESSES</text>
`, harnessY, harnessH, harnessTextY)
	if summary := formatHarnessesSummary(norm.Harnesses); summary != "" {
		fmt.Fprintf(&b, `  <text x="128" y="%d" class="mono model-name">%s</text>`+"\n", harnessTextY, xmlEscape(summary))
	} else {
		fmt.Fprintf(&b, `  <text x="128" y="%d" class="sans empty-note">none recorded</text>`+"\n", harnessTextY)
	}
	fmt.Fprintf(&b, `  <text x="804" y="%d" text-anchor="end" class="mono sec-tag">X-Agent-Harness</text>`+"\n", harnessTextY)

	// Bottom Footer
	fmt.Fprintf(&b, `
  <!-- Bottom Footer -->
  <text x="24" y="%d" class="mono foot">$ git by stats --badge</text>
  <text x="804" y="%d" text-anchor="end" class="mono foot">GPG/SSH Intact · Worktree Safe · Zero Fake Co-Authors</text>
  <circle cx="812" cy="%d" r="3.5" class="dot-green"/>
</svg>
`, footerY, footerY, footerY-4)

	return b.Bytes(), nil
}

func writeSVGThemeStyle(b *bytes.Buffer, theme BadgeTheme) {
	b.WriteString("  <style>\n")
	switch theme {
	case ThemeDark:
		b.WriteString("    svg {\n")
		b.WriteString(svgDarkVars)
		b.WriteString("\n    }\n")
	case ThemeLight:
		b.WriteString("    svg {\n")
		b.WriteString(svgLightVars)
		b.WriteString("\n    }\n")
	default:
		b.WriteString("    svg {\n")
		b.WriteString(svgDarkVars)
		b.WriteString("\n    }\n")
		b.WriteString("    @media (prefers-color-scheme: light) {\n")
		b.WriteString("      svg {\n")
		b.WriteString(svgLightVars)
		b.WriteString("\n      }\n")
		b.WriteString("    }\n")
	}
	b.WriteString(`    .sans { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans", Helvetica, Arial, sans-serif; }
    .mono { font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace; }
    .card-bg { fill: var(--bg-card); }
    .panel-bg { fill: var(--bg-panel); }
    .panel-card { fill: var(--bg-panel); stroke: var(--border); stroke-width: 1; }
    .card-border { fill: none; stroke: var(--border); stroke-width: 1.5; }
    .header-divider { stroke: var(--border); stroke-width: 1; }
    .inner-divider { stroke: var(--divider); stroke-width: 1; }
    .pill-bg { fill: var(--bg-card); stroke: var(--border); stroke-width: 1; }
    .track { fill: var(--bg-track); }
    .bar-human { fill: var(--accent-green-bar); }
    .dot-blue { fill: var(--accent-blue); }
    .dot-purple { fill: var(--accent-purple); }
    .dot-green { fill: var(--accent-green); }
    .icon-blue { fill: none; stroke: var(--accent-blue); stroke-width: 1.8; }
    .icon-purple { fill: none; stroke: var(--accent-purple); stroke-width: 1.8; }
    .icon-green { fill: none; stroke: var(--accent-green); stroke-width: 1.8; }
    .icon-edge { fill: none; stroke: var(--text-muted); stroke-width: 1.6; stroke-linecap: round; }
    .impl-bar-0 { fill: var(--accent-blue); }
    .impl-bar-1 { fill: var(--accent-blue-2); }
    .impl-bar-2 { fill: var(--accent-blue-3); }
    .impl-bar-3 { fill: var(--accent-blue-4); }
    .plan-bar-0 { fill: var(--accent-purple); }
    .plan-bar-1 { fill: var(--accent-purple-2); }
    .plan-bar-2 { fill: var(--accent-purple-3); }
    .plan-bar-3 { fill: var(--accent-purple-4); }
    .stop-accent-blue { stop-color: var(--accent-blue); }
    .stop-accent-purple { stop-color: var(--accent-purple); }
    .stop-accent-green { stop-color: var(--accent-green); }
    .stop-commit-start { stop-color: var(--grad-commit-start); }
    .stop-commit-end { stop-color: var(--grad-commit-end); }
    .stop-loc-start { stop-color: var(--grad-loc-start); }
    .stop-loc-end { stop-color: var(--grad-loc-end); }
    .title { fill: var(--text-primary); font-size: 18px; font-weight: 700; }
    .badge-txt { fill: var(--accent-blue); font-size: 10.5px; font-weight: 600; letter-spacing: 0.6px; }
    .meta { fill: var(--text-muted); font-size: 12px; }
    .sec-label { fill: var(--text-muted); font-size: 11px; font-weight: 700; letter-spacing: 0.5px; }
    .sec-head { fill: var(--text-primary); font-size: 11.5px; font-weight: 700; letter-spacing: 0.5px; }
    .sec-tag { fill: var(--text-muted); font-size: 10.5px; }
    .model-name { fill: var(--text-body); font-size: 12px; }
    .model-stat { fill: var(--text-muted); font-size: 11.5px; }
    .empty-note { fill: var(--text-empty); font-size: 11.5px; font-style: italic; }
    .foot { fill: var(--text-muted); font-size: 11.5px; }
  </style>
`)
}

func writeRatioBar(b *bytes.Buffer, x, y, width, height int, clipID, aiGradID string, hasData bool, aiValue, humanValue int, aiPercent float64) {
	fmt.Fprintf(b, `  <g clip-path="url(#%s)">`+"\n", clipID)
	fmt.Fprintf(b, `    <rect x="%d" y="%d" width="%d" height="%d" class="track"/>`+"\n", x, y, width, height)
	if !hasData {
		b.WriteString("  </g>\n")
		return
	}
	if aiPercent < 0 {
		aiPercent = 0
	}
	if aiPercent > 100 {
		aiPercent = 100
	}

	hasAI := aiValue > 0 || (aiValue == 0 && humanValue == 0 && aiPercent > 0)
	hasHuman := humanValue > 0 || (aiValue == 0 && humanValue == 0 && aiPercent < 100)

	const (
		gap     = 2.0
		minSegW = 4.0
	)
	totalW := float64(width)
	switch {
	case hasAI && hasHuman:
		avail := totalW - gap
		aiW := avail * (aiPercent / 100.0)
		if aiW < minSegW {
			aiW = minSegW
		}
		if aiW > avail-minSegW {
			aiW = avail - minSegW
		}
		aiW = math.Round(aiW*100) / 100
		humanX := float64(x) + aiW + gap
		humanW := math.Round((avail-aiW)*100) / 100
		fmt.Fprintf(b, `    <rect x="%d" y="%d" width="%.2f" height="%d" fill="url(#%s)"/>`+"\n", x, y, aiW, height, aiGradID)
		fmt.Fprintf(b, `    <rect x="%.2f" y="%d" width="%.2f" height="%d" class="bar-human"/>`+"\n", humanX, y, humanW, height)
	case hasAI:
		fmt.Fprintf(b, `    <rect x="%d" y="%d" width="%d" height="%d" fill="url(#%s)"/>`+"\n", x, y, width, height, aiGradID)
	case hasHuman:
		fmt.Fprintf(b, `    <rect x="%d" y="%d" width="%d" height="%d" class="bar-human"/>`+"\n", x, y, width, height)
	}
	b.WriteString("  </g>\n")
}

func writeModelCard(b *bytes.Buffer, colX, cardTop, cardW, cardH int, heading, trailerTag string, rows []ModelCount, barClassPrefix string) {
	fmt.Fprintf(b, `
  <rect x="%d" y="%d" width="%d" height="%d" rx="8" class="panel-card"/>
  <text x="%d" y="%d" class="sans sec-head">%s</text>
  <text x="%d" y="%d" text-anchor="end" class="mono sec-tag">%s</text>
  <line x1="%d" y1="%d" x2="%d" y2="%d" class="inner-divider"/>
`,
		colX, cardTop, cardW, cardH,
		colX+16, cardTop+23, xmlEscape(heading),
		colX+376, cardTop+23, xmlEscape(trailerTag),
		colX, cardTop+34, colX+cardW, cardTop+34,
	)

	if len(rows) == 0 {
		fmt.Fprintf(b, `  <text x="%d" y="%d" class="sans empty-note">No models recorded yet</text>`+"\n",
			colX+16, cardTop+58)
		return
	}

	const barW = 118
	for i, row := range rows {
		rowY := cardTop + 58 + i*32
		barY := rowY - 9
		displayModel := shortModelID(row.Model)
		if displayModel == "" {
			displayModel = strings.TrimSpace(row.Model)
		}
		displayModel = truncateRunes(displayModel, 20)
		stat := fmt.Sprintf("%s (%.1f%%)", formatInt(row.Commits), row.Percent)

		fmt.Fprintf(b, `  <text x="%d" y="%d" class="mono model-name">%s</text>`+"\n", colX+16, rowY, xmlEscape(displayModel))
		fmt.Fprintf(b, `  <rect x="%d" y="%d" width="%d" height="8" rx="4" class="track"/>`+"\n", colX+168, barY, barW)

		pct := row.Percent
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		fillW := float64(barW) * (pct / 100.0)
		if pct > 0 && fillW < 4 {
			fillW = 4
		}
		if fillW > float64(barW) {
			fillW = float64(barW)
		}
		if fillW > 0 {
			fillW = math.Round(fillW*100) / 100
			fmt.Fprintf(b, `  <rect x="%d" y="%d" width="%.2f" height="8" rx="4" class="%s%d"/>`+"\n",
				colX+168, barY, fillW, barClassPrefix, i%4)
		}
		fmt.Fprintf(b, `  <text x="%d" y="%d" text-anchor="end" class="mono model-stat">%s</text>`+"\n",
			colX+376, rowY, xmlEscape(stat))
	}
}

func harnessBaseName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if idx := strings.LastIndexByte(s, '@'); idx > 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

func formatHarnessesSummary(harnesses []ModelCount) string {
	if len(harnesses) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(harnesses))
	names := make([]string, 0, len(harnesses))
	for _, h := range harnesses {
		name := harnessBaseName(h.Model)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	return truncateRunes(strings.Join(names, " · "), 66)
}

// WriteBadgeSVG renders r with ThemeAuto and writes it atomically to outputPath.
func WriteBadgeSVG(outputPath string, r *Report) error {
	return WriteBadgeSVGWithTheme(outputPath, r, ThemeAuto)
}

// WriteBadgeSVGWithTheme renders r with theme and writes it atomically to outputPath.
func WriteBadgeSVGWithTheme(outputPath string, r *Report, theme BadgeTheme) error {
	trimmed := strings.TrimSpace(outputPath)
	if trimmed == "" {
		return fmt.Errorf("empty badge output path")
	}
	data, err := RenderBadgeSVGWithTheme(r, theme)
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
