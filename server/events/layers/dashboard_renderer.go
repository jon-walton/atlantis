package layers

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

const (
	// DashboardSentinel is the HTML comment used to identify dashboard comments.
	DashboardSentinel = "<!-- Atlantis Layered Planning Dashboard -->"
	// DashboardContinuedSentinelFmt is the format string for continued dashboard comments.
	DashboardContinuedSentinelFmt = "<!-- Atlantis Layered Planning Dashboard (continued %d) -->"
	// ContinuedOnNext is appended when a layer continues in the next comment.
	ContinuedOnNext = "\n\n_(continued on next comment)_"
	// ContinuedFromPrev is prepended when a layer continues from the previous comment.
	ContinuedFromPrev = "_(continued from previous comment)_\n\n"
)

//go:embed templates/layered_dashboard.tmpl
var dashboardTemplateFS embed.FS

// DashboardRenderer renders the layered planning dashboard markdown.
type DashboardRenderer struct {
	templates *template.Template
}

// NewDashboardRenderer creates a new DashboardRenderer.
// If markdownTemplateOverridesDir is non-empty, templates in that directory
// will override the embedded defaults.
func NewDashboardRenderer(markdownTemplateOverridesDir string) *DashboardRenderer {
	templates, _ := template.New("").Funcs(sprig.TxtFuncMap()).ParseFS(dashboardTemplateFS, "templates/*.tmpl")
	if markdownTemplateOverridesDir != "" {
		if overrides, err := templates.ParseGlob(
			fmt.Sprintf("%s/*.tmpl", markdownTemplateOverridesDir),
		); err == nil {
			templates = overrides
		}
	}
	return &DashboardRenderer{
		templates: templates,
	}
}

// Render produces the full dashboard markdown from the given data.
func (r *DashboardRenderer) Render(data DashboardData) string {
	buf := &bytes.Buffer{}
	tmpl := r.templates.Lookup("layeredDashboard")
	if tmpl == nil {
		return "Failed to find layered dashboard template, this is a bug"
	}
	if err := tmpl.Execute(buf, data); err != nil {
		return fmt.Sprintf("Failed to render layered dashboard template, this is a bug: %v", err)
	}
	return normalizeMarkdown(buf.String())
}

// normalizeMarkdown cleans up template output for consistent markdown rendering.
// It collapses runs of 3+ newlines into exactly 2 (one blank line) and trims.
func normalizeMarkdown(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// RenderSplit renders the dashboard and splits it if it exceeds maxLen.
// Returns one or more comment bodies, each within maxLen.
// Splits occur at layer boundaries to keep layers intact.
func (r *DashboardRenderer) RenderSplit(data DashboardData, maxLen int) []string {
	full := r.Render(data)

	if maxLen <= 0 || len(full) <= maxLen {
		return []string{full}
	}

	return splitAtLayerBoundaries(full, maxLen)
}

// splitAtLayerBoundaries splits a rendered dashboard at layer boundaries.
// Each resulting part is guaranteed to be <= maxLen.
// If a single layer exceeds maxLen, it splits mid-layer to preserve all content.
func splitAtLayerBoundaries(rendered string, maxLen int) []string {
	sections := splitIntoLayerSections(rendered)

	if len(sections) == 0 {
		return []string{rendered}
	}

	var comments []string
	var current strings.Builder
	partNum := 1

	for i, section := range sections {
		sentinel := ""
		if partNum > 1 && current.Len() == 0 {
			sentinel = fmt.Sprintf(DashboardContinuedSentinelFmt, partNum) + "\n"
		}

		proposedLen := current.Len() + len(sentinel) + len(section)

		// If adding this section would exceed limit and we have content, flush
		if current.Len() > 0 && proposedLen > maxLen {
			comments = append(comments, strings.TrimSpace(current.String()))
			current.Reset()
			partNum++
			sentinel = fmt.Sprintf(DashboardContinuedSentinelFmt, partNum) + "\n"
		}

		// Add sentinel if starting a new non-first part
		if current.Len() == 0 && i > 0 {
			current.WriteString(sentinel)
		}

		// Check if this single section exceeds maxLen
		if len(section)+current.Len() > maxLen {
			// Split the section itself across multiple comments
			sectionParts := splitLargeSection(section, maxLen-current.Len(), maxLen, partNum)
			for j, sp := range sectionParts {
				if j == 0 {
					current.WriteString(sp)
				} else {
					// Flush current and start new part
					comments = append(comments, strings.TrimSpace(current.String()))
					current.Reset()
					partNum++
					current.WriteString(sp)
				}
			}
		} else {
			current.WriteString(section)
		}
	}

	if current.Len() > 0 {
		comments = append(comments, strings.TrimSpace(current.String()))
	}

	return comments
}

// splitLargeSection splits a section that exceeds maxLen into multiple parts.
// firstPartLen is the available space in the current comment.
// Adds continuation indicators when splitting within a layer.
// Returns parts with appropriate continued sentinels.
func splitLargeSection(section string, firstPartLen int, maxLen int, startPartNum int) []string {
	if len(section) <= firstPartLen {
		return []string{section}
	}

	var parts []string
	remaining := section
	partNum := startPartNum
	isFirst := true

	for len(remaining) > 0 {
		availableLen := maxLen
		prefix := ""

		if isFirst {
			// Reserve space for "continued on next" indicator
			availableLen = firstPartLen - len(ContinuedOnNext)
			isFirst = false
		} else {
			// Add continued sentinel and "from previous" indicator
			sentinel := fmt.Sprintf(DashboardContinuedSentinelFmt, partNum) + "\n"
			prefix = sentinel + ContinuedFromPrev
			availableLen = maxLen - len(prefix) - len(ContinuedOnNext)
		}

		if availableLen <= 0 {
			availableLen = 50 // Minimum to make progress
		}

		// Find a good split point (prefer line boundary)
		splitAt := availableLen
		if splitAt >= len(remaining) {
			// This is the last part, no "continued on next" needed
			if prefix != "" {
				remaining = prefix + remaining
			}
			parts = append(parts, remaining)
			break
		}

		// Look for last newline before splitAt
		lastNewline := strings.LastIndex(remaining[:splitAt], "\n")
		if lastNewline > splitAt/2 { // Only use if it's not too far back
			splitAt = lastNewline + 1
		}

		part := remaining[:splitAt] + ContinuedOnNext
		if prefix != "" {
			part = prefix + part
		}
		parts = append(parts, part)
		remaining = remaining[splitAt:]
		partNum++
	}

	return parts
}

// splitIntoLayerSections splits the rendered dashboard into sections,
// one per layer plus the header and footer.
func splitIntoLayerSections(rendered string) []string {
	lines := strings.Split(rendered, "\n")
	var sections []string
	var current strings.Builder

	for _, line := range lines {
		isLayerBoundary := strings.HasPrefix(line, "**Layer ") ||
			strings.HasPrefix(line, "<details><summary><b>Layer ")
		if isLayerBoundary && current.Len() > 0 {
			sections = append(sections, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}

	if current.Len() > 0 {
		sections = append(sections, current.String())
	}

	return sections
}
