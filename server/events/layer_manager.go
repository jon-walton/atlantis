// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/layered"
	"github.com/runatlantis/atlantis/server/events/layers"
	"github.com/runatlantis/atlantis/server/events/models"
)

// LayerManager is the lifecycle coordinator for layered planning.
// It bridges the dependency graph (WS1), layer state manager (WS2),
// and dashboard updater (WS3) into the plan/apply command flow.
type LayerManager struct {
	stateManager     LayerStateManager
	dashboardUpdater *layers.DashboardUpdater
}

// NewLayerManager creates a new LayerManager.
func NewLayerManager(
	stateManager LayerStateManager,
	dashboardUpdater *layers.DashboardUpdater,
) *LayerManager {
	return &LayerManager{
		stateManager:     stateManager,
		dashboardUpdater: dashboardUpdater,
	}
}

// ShouldActivate returns true if layered planning should activate for this
// set of project commands. It checks whether any project has depends_on set.
func (lm *LayerManager) ShouldActivate(projectCmds []command.ProjectContext) bool {
	for _, cmd := range projectCmds {
		if len(cmd.DependsOn) > 0 {
			return true
		}
	}
	return false
}

// InitializeLayerState builds the dependency graph from the project commands,
// computes layers, and returns the initial LayerState. Returns nil if only
// one layer is needed (no cross-layer dependencies among in-scope projects).
func (lm *LayerManager) InitializeLayerState(
	projectCmds []command.ProjectContext,
) (*models.LayerState, error) {
	// Build project nodes for the typed graph
	var nodes []layered.ProjectNode
	for _, cmd := range projectCmds {
		key := projectContextKey(cmd)
		nodes = append(nodes, layered.ProjectNode{
			ID:             layered.ProjectID(key),
			DependsOn:      toProjectIDs(cmd.DependsOn),
			HasFileChanges: true,
		})
	}

	// Construct and validate the typed graph
	graph, err := layered.NewDependencyGraph(nodes)
	if err != nil {
		return nil, fmt.Errorf("building dependency graph: %w", err)
	}

	// Build project statuses
	var projectStatuses []models.ProjectStatus
	for _, cmd := range projectCmds {
		if cmd.CommandName != command.Plan {
			continue
		}
		projectStatuses = append(projectStatuses, models.ProjectStatus{
			Workspace:   cmd.Workspace,
			RepoRelDir:  cmd.RepoRelDir,
			ProjectName: cmd.ProjectName,
		})
	}

	return lm.stateManager.InitializeLayerState(projectStatuses, graph), nil
}

// toProjectIDs converts a slice of strings to a slice of ProjectIDs.
func toProjectIDs(deps []string) []layered.ProjectID {
	if deps == nil {
		return nil
	}
	result := make([]layered.ProjectID, len(deps))
	for i, d := range deps {
		result[i] = layered.ProjectID(d)
	}
	return result
}

// HasMultipleLayers returns true if the layer state has more than one layer.
func (lm *LayerManager) HasMultipleLayers(state *models.LayerState) bool {
	if !state.Enabled() {
		return false
	}
	return state.TotalLayers > 1
}

// FilterToCurrentLayer filters project commands to only those in the current layer.
func (lm *LayerManager) FilterToCurrentLayer(
	projectCmds []command.ProjectContext,
	state *models.LayerState,
) []command.ProjectContext {
	if state == nil {
		return projectCmds
	}

	currentLayerProjects := lm.stateManager.GetCurrentLayerProjects(state)
	currentSet := make(map[string]bool, len(currentLayerProjects))
	for _, p := range currentLayerProjects {
		currentSet[p] = true
	}

	var filtered []command.ProjectContext
	for _, cmd := range projectCmds {
		key := projectContextKey(cmd)
		if currentSet[key] {
			filtered = append(filtered, cmd)
		}
	}
	return filtered
}

// IsInCurrentLayer returns true if the given project context is in the current layer.
func (lm *LayerManager) IsInCurrentLayer(
	cmd command.ProjectContext,
	state *models.LayerState,
) bool {
	if state == nil {
		return true
	}
	key := projectContextKey(cmd)
	layer, ok := state.ProjectLayers[key]
	return ok && layer == state.CurrentLayer
}

// PostInitialDashboard creates the dashboard comment with "Planning..." status
// for all current-layer projects. This should be called before plan results are
// posted so the dashboard is the first comment on the PR.
func (lm *LayerManager) PostInitialDashboard(
	ctx *command.Context,
	state *models.LayerState,
	projectCmds []command.ProjectContext,
) {
	if lm.dashboardUpdater == nil || state == nil || !lm.HasMultipleLayers(state) {
		return
	}

	// Build a synthetic pull status with current-layer projects at default status
	var projects []models.ProjectStatus
	for _, cmd := range projectCmds {
		key := projectContextKey(cmd)
		if layer, ok := state.ProjectLayers[key]; ok && layer == state.CurrentLayer {
			projects = append(projects, models.ProjectStatus{
				Workspace:   cmd.Workspace,
				RepoRelDir:  cmd.RepoRelDir,
				ProjectName: cmd.ProjectName,
				Layer:       layer,
			})
		}
	}

	pullStatus := &models.PullStatus{
		Projects:   projects,
		LayerState: state,
	}

	data := lm.buildDashboardData(ctx, pullStatus)
	if err := lm.dashboardUpdater.Update(
		ctx.Log,
		ctx.Pull.BaseRepo,
		ctx.Pull.Num,
		data,
	); err != nil {
		ctx.Log.Err("posting initial layered planning dashboard: %s", err)
	}
}

// UpdateDashboard creates or updates the dashboard comment on the PR.
func (lm *LayerManager) UpdateDashboard(
	ctx *command.Context,
	pullStatus *models.PullStatus,
) {
	if lm.dashboardUpdater == nil || pullStatus == nil || pullStatus.LayerState == nil {
		return
	}

	state := pullStatus.LayerState
	if !lm.HasMultipleLayers(state) {
		return
	}

	data := lm.buildDashboardData(ctx, pullStatus)
	if err := lm.dashboardUpdater.Update(
		ctx.Log,
		ctx.Pull.BaseRepo,
		ctx.Pull.Num,
		data,
	); err != nil {
		ctx.Log.Err("updating layered planning dashboard: %s", err)
	}
}

// buildDashboardData converts the PullStatus and LayerState into DashboardData
// for the dashboard renderer.
func (lm *LayerManager) buildDashboardData(
	ctx *command.Context,
	pullStatus *models.PullStatus,
) layers.DashboardData {
	summaries := lm.stateManager.GetLayerSummary(pullStatus)

	var dashLayers []layers.DashboardLayer
	for _, s := range summaries {
		// Skip layers with no projects — they haven't been populated yet
		if len(s.Projects) == 0 && !s.IsCurrent {
			continue
		}
		dl := layers.DashboardLayer{
			Number:    s.Layer + 1, // 1-indexed for display
			IsCurrent: s.IsCurrent,
		}
		for _, proj := range s.Projects {
			dl.Projects = append(dl.Projects, layers.DashboardProject{
				Name:   projectStatusDisplayName(proj),
				Status: layers.NewDashboardStatus(proj.Status),
			})
		}
		dashLayers = append(dashLayers, dl)
	}

	// Count projects not yet visible on the dashboard:
	// 1. PendingProjects: transitive dependents without direct file changes
	// 2. Future-layer projects: assigned to layers beyond what's shown
	pendingCount := lm.stateManager.GetPendingCount(pullStatus.LayerState)
	if pullStatus.LayerState != nil {
		// Count projects in future layers that aren't displayed yet
		shownProjects := 0
		for _, dl := range dashLayers {
			shownProjects += len(dl.Projects)
		}
		totalAssigned := len(pullStatus.LayerState.ProjectLayers)
		futureLayerCount := totalAssigned - shownProjects
		if futureLayerCount > 0 {
			pendingCount += futureLayerCount
		}
	}
	totalCount := len(pullStatus.Projects) + pendingCount

	return layers.DashboardData{
		Layers:       dashLayers,
		PendingCount: pendingCount,
		TotalCount:   totalCount,
		RepoName:     ctx.Pull.BaseRepo.FullName,
		PullNum:      ctx.Pull.Num,
	}
}

// StampAndSaveLayerState stamps layer assignments on the pull status and
// saves the layer state to the database.
func (lm *LayerManager) StampAndSaveLayerState(
	pullStatus *models.PullStatus,
	state *models.LayerState,
) {
	if pullStatus == nil || state == nil {
		return
	}
	pullStatus.LayerState = state
	lm.stateManager.StampLayerAssignments(pullStatus)
}

// projectContextKey returns a consistent key for a project context,
// matching the key format used in the LayerStateManager.
func projectContextKey(cmd command.ProjectContext) string {
	if cmd.ProjectName != "" {
		return cmd.ProjectName
	}
	return cmd.RepoRelDir + "::" + cmd.Workspace
}

// projectStatusDisplayName returns a display name for a project status.
func projectStatusDisplayName(proj models.ProjectStatus) string {
	if proj.ProjectName != "" {
		return proj.ProjectName
	}
	if proj.Workspace != "" && proj.Workspace != "default" {
		return proj.RepoRelDir + " (" + proj.Workspace + ")"
	}
	return proj.RepoRelDir
}

