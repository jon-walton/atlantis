package layers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
)

func ds(status models.ProjectPlanStatus) DashboardStatus {
	return NewDashboardStatus(status)
}

func TestRender_SingleLayerCurrent(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number:    1,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "vpc", Status: ds(models.PendingPlanStatus)},
					{Name: "dns", Status: ds(models.PlannedPlanStatus)},
					{Name: "iam-roles", Status: ds(models.AppliedStatus)},
				},
			},
		},
	}

	result := r.Render(data)

	if !strings.Contains(result, DashboardSentinel) {
		t.Error("expected sentinel in output")
	}
	if !strings.Contains(result, "**Layer 1** _(current)_") {
		t.Errorf("expected current layer header, got:\n%s", result)
	}
	if !strings.Contains(result, "\u23f3 Pending") {
		t.Error("expected hourglass icon for Pending status")
	}
	if !strings.Contains(result, "\U0001f536 Planned (changes)") {
		t.Error("expected orange diamond icon for Planned status")
	}
	if !strings.Contains(result, "\u2705 Applied") {
		t.Error("expected check mark icon for Applied status")
	}
}

func TestRender_CompletedLayerCollapsed(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number: 1,
				Projects: []DashboardProject{
					{Name: "vpc", Status: ds(models.AppliedStatus)},
					{Name: "dns", Status: ds(models.AppliedStatus)},
					{Name: "iam-roles", Status: ds(models.PlannedNoChangesPlanStatus)},
				},
			},
		},
	}

	result := r.Render(data)

	if !strings.Contains(result, "<details><summary>") {
		t.Errorf("expected completed layer to be collapsed, got:\n%s", result)
	}
	if !strings.Contains(result, "2 applied, 1 no changes") {
		t.Errorf("expected summary in layer header, got:\n%s", result)
	}
}

func TestRender_MultipleLayersWithPending(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number: 1,
				Projects: []DashboardProject{
					{Name: "vpc", Status: ds(models.AppliedStatus)},
				},
			},
			{
				Number:    2,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "alb", Status: ds(models.PendingPlanStatus)},
				},
			},
		},
		PendingCount: 45,
	}

	result := r.Render(data)

	if !strings.Contains(result, "Layer 1</b> — 1 applied") {
		t.Errorf("expected completed layer 1 with summary, got:\n%s", result)
	}
	if !strings.Contains(result, "**Layer 2** _(current)_") {
		t.Errorf("expected current layer 2, got:\n%s", result)
	}
	if !strings.Contains(result, "45 additional projects pending") {
		t.Errorf("expected pending count, got:\n%s", result)
	}
}

func TestRender_AllStatusIcons(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number:    1,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "a", Status: ds(models.PendingPlanStatus)},
					{Name: "b", Status: ds(models.PlannedPlanStatus)},
					{Name: "c", Status: ds(models.PlannedNoChangesPlanStatus)},
					{Name: "d", Status: ds(models.AppliedStatus)},
					{Name: "e", Status: ds(models.ErroredApplyStatus)},
					{Name: "f", Status: ds(models.SkippedPlanStatus)},
				},
			},
		},
	}

	result := r.Render(data)

	expected := map[string]string{
		"\u23f3":       "Pending",
		"\U0001f536":   "Planned",
		"\u26aa":       "NoChanges",
		"\u2705":       "Applied",
		"\u274c":       "ApplyFailed",
		"\u23ed\ufe0f": "Skipped",
	}
	for icon, name := range expected {
		if !strings.Contains(result, icon) {
			t.Errorf("missing icon for %s: %s", name, icon)
		}
	}
}

func TestRender_PendingCountZero(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number:    1,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "vpc", Status: ds(models.PendingPlanStatus)},
				},
			},
		},
		PendingCount: 0,
	}

	result := r.Render(data)

	if strings.Contains(result, "pending") {
		t.Errorf("expected no pending line when count is 0, got:\n%s", result)
	}
}

func TestRender_SkippedProjectsInSummary(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number: 1,
				Projects: []DashboardProject{
					{Name: "a", Status: ds(models.AppliedStatus)},
					{Name: "b", Status: ds(models.AppliedStatus)},
					{Name: "c", Status: ds(models.SkippedPlanStatus)},
				},
			},
		},
	}

	result := r.Render(data)

	if !strings.Contains(result, "1 skipped") {
		t.Errorf("expected skipped count in summary, got:\n%s", result)
	}
	if !strings.Contains(result, "2 applied") {
		t.Errorf("expected applied count in summary, got:\n%s", result)
	}
}

func TestRender_EmptyLayers(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{},
	}

	result := r.Render(data)

	if !strings.Contains(result, DashboardSentinel) {
		t.Error("expected sentinel even with empty layers")
	}
	if !strings.Contains(result, "## Layered Planning Dashboard") {
		t.Error("expected dashboard header even with empty layers")
	}
}

func TestRenderSplit_UnderLimit(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number:    1,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "vpc", Status: ds(models.PendingPlanStatus)},
				},
			},
		},
	}

	result := r.RenderSplit(data, 100000)

	if len(result) != 1 {
		t.Errorf("expected 1 comment, got %d", len(result))
	}
}

func TestRenderSplit_ZeroMaxLen(t *testing.T) {
	r := NewDashboardRenderer("")
	data := DashboardData{
		Layers: []DashboardLayer{
			{
				Number:    1,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "vpc", Status: ds(models.PendingPlanStatus)},
				},
			},
		},
	}

	result := r.RenderSplit(data, 0)

	if len(result) != 1 {
		t.Errorf("expected 1 comment for zero maxLen (unlimited), got %d", len(result))
	}
}

func TestRenderSplit_ExceedsLimit(t *testing.T) {
	r := NewDashboardRenderer("")

	// Create many layers to exceed a small limit
	var layers []DashboardLayer
	for i := 1; i <= 10; i++ {
		layers = append(layers, DashboardLayer{
			Number: i,
			Projects: []DashboardProject{
				{Name: fmt.Sprintf("project-%d-a", i), Status: ds(models.AppliedStatus)},
				{Name: fmt.Sprintf("project-%d-b", i), Status: ds(models.AppliedStatus)},
				{Name: fmt.Sprintf("project-%d-c", i), Status: ds(models.PlannedNoChangesPlanStatus)},
			},
		})
	}
	data := DashboardData{Layers: layers}

	// Use a small limit to force splitting
	maxLen := 300
	result := r.RenderSplit(data, maxLen)

	if len(result) < 2 {
		t.Errorf("expected at least 2 comments, got %d", len(result))
	}

	// First part should have the main sentinel
	if !strings.Contains(result[0], DashboardSentinel) {
		t.Error("first part should contain the main sentinel")
	}

	// Subsequent parts should have continued sentinel
	for i := 1; i < len(result); i++ {
		expectedSentinel := fmt.Sprintf(DashboardContinuedSentinelFmt, i+1)
		if !strings.Contains(result[i], expectedSentinel) {
			t.Errorf("part %d should contain continued sentinel %q", i+1, expectedSentinel)
		}
	}

	// All parts should be within limit
	for i, part := range result {
		if len(part) > maxLen {
			t.Errorf("part %d exceeds maxLen: %d > %d", i+1, len(part), maxLen)
		}
	}
}

func TestRenderSplit_SingleHugeLayer(t *testing.T) {
	// Test the truncation fallback by calling splitAtLayerBoundaries directly
	// with a single section that exceeds maxLen.
	hugeContent := DashboardSentinel + "\n## Layered Planning Dashboard\n\n**Layer 1** _(current)_\n"
	for i := 0; i < 100; i++ {
		hugeContent += fmt.Sprintf("- item %d with some long content to make it big\n", i)
	}

	result := splitAtLayerBoundaries(hugeContent, 200)

	// Because the header and the layer section are separate sections,
	// they should be split across multiple comments. Each part should
	// be within the limit.
	for i, part := range result {
		if len(part) > 200 {
			// If a single section exceeds the limit, it should be truncated
			if !strings.Contains(part, "... (dashboard truncated)") {
				t.Errorf("part %d exceeds limit but lacks truncation marker", i+1)
			}
		}
	}
}

func TestSplitAtLayerBoundaries_UnsplittableSingleSection(t *testing.T) {
	// A single long string with no layer boundaries should truncate.
	longContent := strings.Repeat("x", 500)
	result := splitAtLayerBoundaries(longContent, 200)

	if len(result) != 1 {
		t.Errorf("expected 1 truncated result, got %d", len(result))
	}
	if !strings.Contains(result[0], "... (dashboard truncated)") {
		t.Error("expected truncation marker")
	}
	if len(result[0]) > 200 {
		t.Errorf("result exceeds maxLen: %d > 200", len(result[0]))
	}
}

func TestSplitIntoLayerSections(t *testing.T) {
	rendered := `<!-- Atlantis Layered Planning Dashboard -->
## Layered Planning Dashboard

**Layer 1** — 2 applied
some content
**Layer 2** _(current)_
more content
**Layer 3**
even more content
`

	sections := splitIntoLayerSections(rendered)

	// Header + 3 layers = 4 sections
	if len(sections) != 4 {
		t.Errorf("expected 4 sections, got %d", len(sections))
		for i, s := range sections {
			t.Logf("section %d:\n%s", i, s)
		}
	}

	// First section should be the header
	if !strings.Contains(sections[0], "## Layered Planning Dashboard") {
		t.Error("first section should contain the header")
	}

	// Remaining sections should start with **Layer
	for i := 1; i < len(sections); i++ {
		if !strings.HasPrefix(sections[i], "**Layer ") {
			t.Errorf("section %d should start with '**Layer ', got: %q", i, sections[i][:20])
		}
	}
}

func TestDashboardStatus_String(t *testing.T) {
	tests := []struct {
		status   models.ProjectPlanStatus
		expected string
	}{
		{models.PendingPlanStatus, "Pending"},
		{models.ErroredPlanStatus, "Plan failed"},
		{models.PlannedPlanStatus, "Planned (changes)"},
		{models.PlannedNoChangesPlanStatus, "Planned (no changes)"},
		{models.ApplyingStatus, "Applying..."},
		{models.ErroredApplyStatus, "Apply failed"},
		{models.AppliedStatus, "Applied"},
		{models.SkippedPlanStatus, "Skipped"},
	}
	for _, tt := range tests {
		if got := ds(tt.status).String(); got != tt.expected {
			t.Errorf("DashboardStatus(%d).String() = %q, want %q", tt.status, got, tt.expected)
		}
	}
}

func TestDashboardStatus_Icon(t *testing.T) {
	tests := []struct {
		status       models.ProjectPlanStatus
		expectedIcon string
	}{
		{models.PendingPlanStatus, "\u23f3"},
		{models.PlannedPlanStatus, "\U0001f536"},
		{models.PlannedNoChangesPlanStatus, "\u26aa"},
		{models.AppliedStatus, "\u2705"},
		{models.ErroredApplyStatus, "\u274c"},
		{models.ErroredPlanStatus, "\u274c"},
		{models.SkippedPlanStatus, "\u23ed\ufe0f"},
	}
	for _, tt := range tests {
		if got := ds(tt.status).Icon(); got != tt.expectedIcon {
			t.Errorf("DashboardStatus(%d).Icon() = %q, want %q", tt.status, got, tt.expectedIcon)
		}
	}
}

func TestDashboardLayer_IsCompleted(t *testing.T) {
	tests := []struct {
		name     string
		projects []DashboardProject
		expected bool
	}{
		{
			"all applied",
			[]DashboardProject{
				{Status: ds(models.AppliedStatus)},
				{Status: ds(models.AppliedStatus)},
			},
			true,
		},
		{
			"mixed terminal",
			[]DashboardProject{
				{Status: ds(models.AppliedStatus)},
				{Status: ds(models.PlannedNoChangesPlanStatus)},
				{Status: ds(models.SkippedPlanStatus)},
			},
			true,
		},
		{
			"has pending",
			[]DashboardProject{
				{Status: ds(models.AppliedStatus)},
				{Status: ds(models.PendingPlanStatus)},
			},
			false,
		},
		{
			"has failed",
			[]DashboardProject{
				{Status: ds(models.AppliedStatus)},
				{Status: ds(models.ErroredApplyStatus)},
			},
			false,
		},
		{
			"empty projects",
			[]DashboardProject{},
			false,
		},
		{
			"nil projects",
			nil,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := DashboardLayer{Projects: tt.projects}
			if got := l.IsCompleted(); got != tt.expected {
				t.Errorf("IsCompleted() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDashboardLayer_CompletedCount(t *testing.T) {
	l := DashboardLayer{
		Projects: []DashboardProject{
			{Status: ds(models.AppliedStatus)},
			{Status: ds(models.PlannedNoChangesPlanStatus)},
			{Status: ds(models.SkippedPlanStatus)},
			{Status: ds(models.PendingPlanStatus)},
			{Status: ds(models.ErroredApplyStatus)},
		},
	}
	if got := l.CompletedCount(); got != 3 {
		t.Errorf("CompletedCount() = %d, want 3", got)
	}
}

func TestDashboardLayer_Summary(t *testing.T) {
	tests := []struct {
		name     string
		projects []DashboardProject
		expected string
	}{
		{
			"all applied",
			[]DashboardProject{
				{Status: ds(models.AppliedStatus)},
				{Status: ds(models.AppliedStatus)},
			},
			"2 applied",
		},
		{
			"mixed",
			[]DashboardProject{
				{Status: ds(models.AppliedStatus)},
				{Status: ds(models.PlannedNoChangesPlanStatus)},
				{Status: ds(models.SkippedPlanStatus)},
			},
			"1 applied, 1 no changes, 1 skipped",
		},
		{
			"empty",
			[]DashboardProject{},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := DashboardLayer{Projects: tt.projects}
			if got := l.Summary(); got != tt.expected {
				t.Errorf("Summary() = %q, want %q", got, tt.expected)
			}
		})
	}
}
