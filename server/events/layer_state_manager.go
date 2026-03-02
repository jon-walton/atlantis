// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"
	"sort"

	"github.com/runatlantis/atlantis/server/events/layered"
	"github.com/runatlantis/atlantis/server/events/models"
)

// LayerStateManager manages the state machine for layered planning.
// It is the single source of truth for layer progression, cascade
// evaluation, and skip handling.
type LayerStateManager interface {
	// InitializeLayerState builds the initial LayerState from a validated
	// dependency graph. Returns nil if no dependencies exist.
	InitializeLayerState(
		projects []models.ProjectStatus,
		graph *layered.DependencyGraph,
	) *models.LayerState

	// GetCurrentLayerProjects returns the project names in the current layer.
	GetCurrentLayerProjects(state *models.LayerState) []string

	// GetPendingCount returns the count of projects not yet in any processed layer.
	GetPendingCount(state *models.LayerState) int

	// IsLayerComplete returns true if all projects in the given layer have a
	// terminal status (applied, no-changes, or skipped). It also returns the
	// list of projects still waiting for action.
	IsLayerComplete(pullStatus *models.PullStatus, layer int) (complete bool, blocking []string)

	// CanAdvance returns true if the current layer is complete and there are
	// more layers to process or pending projects to evaluate.
	CanAdvance(pullStatus *models.PullStatus) bool

	// AdvanceLayer moves to the next layer, performing cascade evaluation.
	// Returns the updated LayerState and the list of project names to plan next.
	AdvanceLayer(pullStatus *models.PullStatus) (*models.LayerState, []string, error)

	// SkipProject marks a project as skipped.
	SkipProject(pullStatus *models.PullStatus, projectName string, skipEnabled bool) (*models.PullStatus, error)

	// HandleNewCommit determines the impact of new commits on layered state.
	HandleNewCommit(pullStatus *models.PullStatus, affectedProjects []string) ResetAction

	// ResetLayerState rolls back layer state to the specified layer, clearing
	// all project assignments and status for layers >= resetToLayer and moving
	// those projects back to PendingProjects.
	ResetLayerState(state *models.LayerState, resetToLayer int) error

	// IsAllComplete returns true if all layers have been processed.
	IsAllComplete(pullStatus *models.PullStatus) bool

	// GetLayerSummary returns a summary of each layer's status for dashboard rendering.
	GetLayerSummary(pullStatus *models.PullStatus) []LayerSummary

	// StampLayerAssignments sets the Layer field on each ProjectStatus based on
	// the LayerState's ProjectLayers mapping.
	StampLayerAssignments(pullStatus *models.PullStatus)
}

// ResetAction describes what should happen when new commits affect layered state.
type ResetAction struct {
	// ResetToLayer is the layer to reset back to (-1 = no reset needed).
	ResetToLayer int

	// ReplanProjects lists projects in the current layer that need re-planning.
	ReplanProjects []string

	// NoAction is true if no projects in processed layers were affected.
	NoAction bool
}

// LayerSummary describes the status of a single layer for dashboard rendering.
type LayerSummary struct {
	// Layer is the layer index.
	Layer int

	// IsCurrent is true if this is the active layer.
	IsCurrent bool

	// IsComplete is true if all projects in this layer are done.
	IsComplete bool

	// Projects is the list of project statuses in this layer.
	Projects []models.ProjectStatus
}

// projectStatusKey returns the lookup key for a ProjectStatus, matching the
// format used by projectContextKey() in layer_manager.go. If ProjectName is
// set, it is used; otherwise we fall back to "RepoRelDir::Workspace".
func projectStatusKey(p models.ProjectStatus) string {
	if p.ProjectName != "" {
		return p.ProjectName
	}
	return p.RepoRelDir + "::" + p.Workspace
}

// DefaultLayerStateManager implements LayerStateManager.
type DefaultLayerStateManager struct{}

// NewLayerStateManager creates a new DefaultLayerStateManager.
func NewLayerStateManager() *DefaultLayerStateManager {
	return &DefaultLayerStateManager{}
}

func (m *DefaultLayerStateManager) InitializeLayerState(
	projects []models.ProjectStatus,
	graph *layered.DependencyGraph,
) *models.LayerState {
	if graph == nil || !graph.HasDependencies() {
		return nil
	}

	// Calculate initial layers using the typed graph
	plan := graph.CalculateInitialLayers()

	// Build project layers map
	projectLayers := make(map[string]int)
	maxLayer := 0
	for _, layer := range plan.Layers {
		for _, projectID := range layer.Projects {
			projectLayers[string(projectID)] = layer.Index
			if layer.Index > maxLayer {
				maxLayer = layer.Index
			}
		}
	}

	// Out-of-scope projects become pending
	pending := make([]string, len(plan.OutOfScope))
	for i, id := range plan.OutOfScope {
		pending[i] = string(id)
	}

	return &models.LayerState{
		Graph:            graph,
		CurrentLayer:     0,
		TotalLayers:      maxLayer + 1,
		ProjectLayers:    projectLayers,
		PendingProjects:  pending,
		SkippedUpstreams: make(map[string]bool),
	}
}

func (m *DefaultLayerStateManager) GetCurrentLayerProjects(state *models.LayerState) []string {
	if state == nil {
		return nil
	}
	var projects []string
	for name, layer := range state.ProjectLayers {
		if layer == state.CurrentLayer {
			projects = append(projects, name)
		}
	}
	return projects
}

func (m *DefaultLayerStateManager) GetPendingCount(state *models.LayerState) int {
	if state == nil {
		return 0
	}
	return len(state.PendingProjects)
}

func (m *DefaultLayerStateManager) IsLayerComplete(
	pullStatus *models.PullStatus,
	layer int,
) (bool, []string) {
	if pullStatus.LayerState == nil {
		return true, nil
	}

	var blocking []string
	for _, proj := range pullStatus.Projects {
		if proj.Layer != layer {
			continue
		}
		switch proj.Status {
		case models.AppliedStatus,
			models.PlannedNoChangesPlanStatus,
			models.SkippedPlanStatus:
			// Terminal states - this project is done
			continue
		default:
			blocking = append(blocking, projectStatusKey(proj))
		}
	}
	return len(blocking) == 0, blocking
}

func (m *DefaultLayerStateManager) CanAdvance(pullStatus *models.PullStatus) bool {
	state := pullStatus.LayerState
	if state == nil {
		return false
	}

	// Check if current layer is complete
	complete, _ := m.IsLayerComplete(pullStatus, state.CurrentLayer)
	if !complete {
		return false
	}

	// Can advance if there are more known layers or pending projects
	if state.CurrentLayer+1 < state.TotalLayers {
		return true
	}
	if len(state.PendingProjects) > 0 {
		return true
	}

	return false
}

func (m *DefaultLayerStateManager) AdvanceLayer(
	pullStatus *models.PullStatus,
) (*models.LayerState, []string, error) {
	state := pullStatus.LayerState
	if state == nil {
		return nil, nil, fmt.Errorf("no layer state to advance")
	}

	graph := state.Graph
	completedLayer := state.CurrentLayer

	// Build current plan from state
	currentPlan := m.buildLayerPlanFromState(state)

	// Determine project statuses from pull status
	projectStatuses := make(map[layered.ProjectID]layered.ChangeStatus)
	for _, proj := range pullStatus.Projects {
		key := projectStatusKey(proj)
		if proj.Layer == completedLayer {
			switch proj.Status {
			case models.AppliedStatus:
				projectStatuses[layered.ProjectID(key)] = layered.ChangeStatusHasChanges
			case models.PlannedNoChangesPlanStatus:
				projectStatuses[layered.ProjectID(key)] = layered.ChangeStatusNoChanges
			case models.ErroredApplyStatus, models.ErroredPlanStatus:
				projectStatuses[layered.ProjectID(key)] = layered.ChangeStatusError
			}
		}
	}

	// Use typed graph to expand layers
	newPlan := graph.ExpandLayer(currentPlan, completedLayer, projectStatuses)

	// Extract next layer projects
	nextLayer := completedLayer + 1
	var nextLayerProjects []string
	if nextLayer < len(newPlan.Layers) {
		for _, id := range newPlan.Layers[nextLayer].Projects {
			nextLayerProjects = append(nextLayerProjects, string(id))
			state.ProjectLayers[string(id)] = nextLayer
		}
	}

	// Update pending projects from out-of-scope, but exclude those whose
	// upstreams had no changes, errors, or were skipped (they won't cascade).
	state.PendingProjects = nil
	for _, id := range newPlan.OutOfScope {
		// Check if project's upstream was skipped
		if state.SkippedUpstreams[string(id)] {
			continue
		}

		// Check if any upstream dependency has changes (cascade can continue)
		deps := graph.GetDependencies(id)
		hasPotentialUpstream := false
		for _, depID := range deps {
			// Check if this upstream was skipped
			if state.SkippedUpstreams[string(depID)] {
				continue
			}
			status := projectStatuses[depID]
			if status == layered.ChangeStatusHasChanges || status == 0 {
				// Upstream has changes or is not yet known (default) - could cascade
				hasPotentialUpstream = true
				break
			}
		}
		if hasPotentialUpstream {
			state.PendingProjects = append(state.PendingProjects, string(id))
		}
	}

	nextLayerProjects = deduplicate(nextLayerProjects)

	if len(nextLayerProjects) > 0 {
		state.CurrentLayer = nextLayer
		if nextLayer >= state.TotalLayers {
			state.TotalLayers = nextLayer + 1
		}
	} else if len(state.PendingProjects) == 0 {
		state.CurrentLayer = -1
	} else {
		state.CurrentLayer = nextLayer
	}

	return state, nextLayerProjects, nil
}

// buildLayerPlanFromState reconstructs a LayerPlan from LayerState
func (m *DefaultLayerStateManager) buildLayerPlanFromState(state *models.LayerState) *layered.LayerPlan {
	layers := make([]layered.Layer, state.TotalLayers)
	for i := range layers {
		layers[i] = layered.Layer{Index: i}
	}

	for proj, layer := range state.ProjectLayers {
		if layer < len(layers) {
			layers[layer].Projects = append(layers[layer].Projects, layered.ProjectID(proj))
		}
	}

	// Sort projects in each layer for determinism
	for i := range layers {
		sort.Slice(layers[i].Projects, func(a, b int) bool {
			return layers[i].Projects[a] < layers[i].Projects[b]
		})
	}

	outOfScope := make([]layered.ProjectID, len(state.PendingProjects))
	for i, p := range state.PendingProjects {
		outOfScope[i] = layered.ProjectID(p)
	}

	return &layered.LayerPlan{
		Layers:            layers,
		OutOfScope:        outOfScope,
		TotalProjectCount: len(state.ProjectLayers) + len(state.PendingProjects),
	}
}

func (m *DefaultLayerStateManager) SkipProject(
	pullStatus *models.PullStatus,
	projectName string,
	skipEnabled bool,
) (*models.PullStatus, error) {
	if !skipEnabled {
		return nil, fmt.Errorf("skip functionality is not enabled (enable-layered-apply-skip)")
	}

	state := pullStatus.LayerState
	if state == nil {
		return nil, fmt.Errorf("layered planning is not active for this pull request")
	}

	// Validate: project must be in current layer
	projLayer, ok := state.ProjectLayers[projectName]
	if !ok || projLayer != state.CurrentLayer {
		return nil, fmt.Errorf("project %q is not in the current layer (%d)", projectName, state.CurrentLayer)
	}

	// Validate: project must have failed apply
	var proj *models.ProjectStatus
	for i := range pullStatus.Projects {
		if projectStatusKey(pullStatus.Projects[i]) == projectName {
			proj = &pullStatus.Projects[i]
			break
		}
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found in pull status", projectName)
	}
	if proj.Status != models.ErroredApplyStatus {
		return nil, fmt.Errorf("can only skip projects with failed applies (current status: %s)", proj.Status)
	}

	// Update status
	proj.Status = models.SkippedPlanStatus

	// Mark in skipped upstreams so cascade evaluation excludes dependents
	if state.SkippedUpstreams == nil {
		state.SkippedUpstreams = make(map[string]bool)
	}
	state.SkippedUpstreams[projectName] = true

	// Also mark all transitive dependents as excluded
	m.markDependentsExcluded(state, projectName)

	return pullStatus, nil
}

// markDependentsExcluded recursively marks all downstream dependents of a
// skipped project as excluded.
func (m *DefaultLayerStateManager) markDependentsExcluded(
	state *models.LayerState,
	projectName string,
) {
	graph := state.Graph
	if graph == nil {
		return
	}

	var exclude func(name string)
	exclude = func(name string) {
		dependents := graph.GetDependents(layered.ProjectID(name))
		for _, dep := range dependents {
			depStr := string(dep)
			state.SkippedUpstreams[depStr] = true
			// Remove from pending if present
			for i, p := range state.PendingProjects {
				if p == depStr {
					state.PendingProjects = append(
						state.PendingProjects[:i],
						state.PendingProjects[i+1:]...,
					)
					break
				}
			}
			exclude(depStr)
		}
	}
	exclude(projectName)
}

func (m *DefaultLayerStateManager) HandleNewCommit(
	pullStatus *models.PullStatus,
	affectedProjects []string,
) ResetAction {
	state := pullStatus.LayerState
	if state == nil {
		return ResetAction{NoAction: true}
	}

	var replanProjects []string
	resetToLayer := -1

	for _, projName := range affectedProjects {
		layer, assigned := state.ProjectLayers[projName]
		if !assigned {
			// Project is pending (future layer) - no action needed
			continue
		}

		if layer > state.CurrentLayer {
			// Future layer - will be planned naturally
			continue
		}

		if layer == state.CurrentLayer {
			// Current layer - check project status
			for _, proj := range pullStatus.Projects {
				if projectStatusKey(proj) == projName {
					if proj.Status == models.AppliedStatus {
						// Already applied in current layer - need reset
						if resetToLayer == -1 || layer < resetToLayer {
							resetToLayer = layer
						}
					} else {
						replanProjects = append(replanProjects, projName)
					}
				}
			}
			continue
		}

		// Already-applied layer - need to reset back
		if resetToLayer == -1 || layer < resetToLayer {
			resetToLayer = layer
		}
	}

	if resetToLayer >= 0 {
		return ResetAction{ResetToLayer: resetToLayer}
	}

	if len(replanProjects) > 0 {
		return ResetAction{
			ResetToLayer:   -1,
			ReplanProjects: replanProjects,
		}
	}

	return ResetAction{NoAction: true}
}

// ResetLayerState rolls back the layer state to the specified layer.
// Projects in layers >= resetToLayer are removed from ProjectLayers and
// moved back to PendingProjects. CurrentLayer is set to resetToLayer
// and TotalLayers is decremented accordingly.
func (m *DefaultLayerStateManager) ResetLayerState(state *models.LayerState, resetToLayer int) error {
	if state == nil {
		return fmt.Errorf("no layer state to reset")
	}
	if resetToLayer < 0 || resetToLayer >= state.TotalLayers {
		return fmt.Errorf("invalid reset layer %d (total layers: %d)", resetToLayer, state.TotalLayers)
	}

	// Move projects in layers >= resetToLayer back to PendingProjects
	for projName, layer := range state.ProjectLayers {
		if layer >= resetToLayer {
			state.PendingProjects = append(state.PendingProjects, projName)
			delete(state.ProjectLayers, projName)
		}
	}
	state.PendingProjects = deduplicate(state.PendingProjects)

	// Reset current layer and total layers
	state.CurrentLayer = resetToLayer
	state.TotalLayers = resetToLayer

	// Clear skipped upstreams for invalidated projects (those no longer assigned)
	for projName := range state.SkippedUpstreams {
		if _, assigned := state.ProjectLayers[projName]; !assigned {
			delete(state.SkippedUpstreams, projName)
		}
	}

	return nil
}

func (m *DefaultLayerStateManager) IsAllComplete(pullStatus *models.PullStatus) bool {
	state := pullStatus.LayerState
	if state == nil {
		return true
	}
	return state.CurrentLayer == -1
}

func (m *DefaultLayerStateManager) GetLayerSummary(pullStatus *models.PullStatus) []LayerSummary {
	state := pullStatus.LayerState
	if state == nil {
		return nil
	}

	summaries := make([]LayerSummary, state.TotalLayers)
	for i := 0; i < state.TotalLayers; i++ {
		summaries[i] = LayerSummary{
			Layer:     i,
			IsCurrent: i == state.CurrentLayer,
		}
	}

	// Assign projects to their layers
	seenProjects := make(map[string]bool)
	for _, proj := range pullStatus.Projects {
		if proj.Layer >= 0 && proj.Layer < state.TotalLayers {
			summaries[proj.Layer].Projects = append(summaries[proj.Layer].Projects, proj)
			seenProjects[projectStatusKey(proj)] = true
		}
	}

	// For the current layer, add placeholder entries for projects that are
	// assigned to this layer but don't have results yet (planning in progress).
	// The zero-value PendingPlanStatus renders as "Planning..." on the dashboard.
	for projectKey, layer := range state.ProjectLayers {
		if layer == state.CurrentLayer && !seenProjects[projectKey] {
			summaries[layer].Projects = append(summaries[layer].Projects, models.ProjectStatus{
				ProjectName: projectKey,
				Layer:       layer,
			})
		}
	}

	// Sort projects within each layer for deterministic ordering
	for i := range summaries {
		sort.Slice(summaries[i].Projects, func(a, b int) bool {
			return projectStatusKey(summaries[i].Projects[a]) < projectStatusKey(summaries[i].Projects[b])
		})
	}

	// Determine completeness
	for i := range summaries {
		complete, _ := m.IsLayerComplete(pullStatus, i)
		summaries[i].IsComplete = complete
	}

	return summaries
}

func (m *DefaultLayerStateManager) StampLayerAssignments(pullStatus *models.PullStatus) {
	if pullStatus.LayerState == nil {
		return
	}
	for i := range pullStatus.Projects {
		proj := &pullStatus.Projects[i]
		if layer, ok := pullStatus.LayerState.ProjectLayers[projectStatusKey(*proj)]; ok {
			proj.Layer = layer
		}
	}
}

// deduplicate removes duplicate strings from a slice while preserving order.
func deduplicate(items []string) []string {
	if items == nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
