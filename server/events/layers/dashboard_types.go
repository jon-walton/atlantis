package layers

import (
	"fmt"
	"strings"

	"github.com/runatlantis/atlantis/server/events/models"
)

// statusDisplay maps model statuses to their dashboard display info.
// This is the single source of truth for how statuses render on the dashboard.
var statusDisplay = map[models.ProjectPlanStatus]struct {
	Label string
	Icon  string
}{
	models.PendingPlanStatus:          {"Pending", "\u23f3"},             // hourglass
	models.ErroredPlanStatus:          {"Plan failed", "\u274c"},         // cross mark
	models.PlannedPlanStatus:          {"Planned (changes)", "\U0001f536"}, // large orange diamond
	models.PlannedNoChangesPlanStatus: {"Planned (no changes)", "\u26aa"}, // white circle
	models.ApplyingStatus:             {"Applying...", "\u23f3"},          // hourglass
	models.ErroredApplyStatus:         {"Apply failed", "\u274c"},         // cross mark
	models.AppliedStatus:              {"Applied", "\u2705"},              // white check mark
	models.SkippedPlanStatus:          {"Skipped", "\u23ed\ufe0f"},        // next track button
	models.PassedPolicyCheckStatus:    {"Planned (changes)", "\U0001f536"}, // policy passed = planned
}

// DashboardStatus wraps ProjectPlanStatus with display methods for templates.
type DashboardStatus struct {
	models.ProjectPlanStatus
}

// String returns the human-readable display label.
func (s DashboardStatus) String() string {
	if info, ok := statusDisplay[s.ProjectPlanStatus]; ok {
		return info.Label
	}
	return "Unknown"
}

// Icon returns the status icon emoji.
func (s DashboardStatus) Icon() string {
	if info, ok := statusDisplay[s.ProjectPlanStatus]; ok {
		return info.Icon
	}
	return "\u2753" // question mark
}

// IsTerminal returns true if this status represents a completed state.
func (s DashboardStatus) IsTerminal() bool {
	switch s.ProjectPlanStatus {
	case models.AppliedStatus, models.PlannedNoChangesPlanStatus, models.SkippedPlanStatus:
		return true
	}
	return false
}

// NewDashboardStatus creates a DashboardStatus from a model status.
func NewDashboardStatus(status models.ProjectPlanStatus) DashboardStatus {
	return DashboardStatus{status}
}

// DashboardProject represents a single project entry in the dashboard.
type DashboardProject struct {
	Name   string
	Status DashboardStatus
}

// DashboardLayer represents a single layer in the dashboard.
type DashboardLayer struct {
	Number    int
	Projects  []DashboardProject
	IsCurrent bool
}

// IsCompleted returns true if all projects in the layer are in a terminal state.
// A layer with no projects is not considered completed.
func (l DashboardLayer) IsCompleted() bool {
	if len(l.Projects) == 0 {
		return false
	}
	for _, p := range l.Projects {
		if !p.Status.IsTerminal() {
			return false
		}
	}
	return true
}

// CompletedCount returns the number of projects in terminal states.
func (l DashboardLayer) CompletedCount() int {
	count := 0
	for _, p := range l.Projects {
		if p.Status.IsTerminal() {
			count++
		}
	}
	return count
}

// Summary returns a short summary like "12 projects applied".
func (l DashboardLayer) Summary() string {
	applied := 0
	noChanges := 0
	skipped := 0
	for _, p := range l.Projects {
		switch p.Status.ProjectPlanStatus {
		case models.AppliedStatus:
			applied++
		case models.PlannedNoChangesPlanStatus:
			noChanges++
		case models.SkippedPlanStatus:
			skipped++
		}
	}
	var parts []string
	if applied > 0 {
		parts = append(parts, fmt.Sprintf("%d applied", applied))
	}
	if noChanges > 0 {
		parts = append(parts, fmt.Sprintf("%d no changes", noChanges))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	return strings.Join(parts, ", ")
}

// DashboardData is the top-level data structure passed to the dashboard template.
type DashboardData struct {
	Layers       []DashboardLayer
	PendingCount int
	TotalCount   int
	RepoName     string
	PullNum      int
}
