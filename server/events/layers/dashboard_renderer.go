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
// If a single layer exceeds maxLen on its own, it falls back to truncation.
func splitAtLayerBoundaries(rendered string, maxLen int) []string {
	sections := splitIntoLayerSections(rendered)

	if len(sections) <= 1 {
		if len(rendered) > maxLen {
			return []string{rendered[:maxLen-50] + "\n\n... (dashboard truncated)"}
		}
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

		if current.Len() > 0 && proposedLen > maxLen {
			comments = append(comments, strings.TrimSpace(current.String()))
			current.Reset()
			partNum++
			sentinel = fmt.Sprintf(DashboardContinuedSentinelFmt, partNum) + "\n"
		}

		if current.Len() == 0 && i > 0 {
			current.WriteString(sentinel)
		}
		current.WriteString(section)
	}

	if current.Len() > 0 {
		result := strings.TrimSpace(current.String())
		// If this final part exceeds maxLen and we can't split further, truncate.
		if len(result) > maxLen {
			result = result[:maxLen-50] + "\n\n... (dashboard truncated)"
		}
		comments = append(comments, result)
	}

	return comments
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
