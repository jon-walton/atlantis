// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/layered"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDashboardData_PendingCountIncludesFutureLayers(t *testing.T) {
	lm := NewLayerManager(NewLayerStateManager(), nil)
	logger := logging.NewNoopLogger(t)

	// Build a graph to enable layer state
	graph, err := layered.NewDependencyGraph([]layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "child-a", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "child-b", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "grandchild", DependsOn: []layered.ProjectID{"child-a"}, HasFileChanges: true},
		{ID: "leaf", DependsOn: []layered.ProjectID{"grandchild"}, HasFileChanges: true},
	})
	require.NoError(t, err)

	// Simulate a 4-layer cascade where only layer 0 is current.
	// Layer 0: "base" (current, shown on dashboard)
	// Layer 1: "child-a", "child-b" (future, not shown)
	// Layer 2: "grandchild" (future, not shown)
	// Layer 3: "leaf" (future, not shown)
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  4,
		ProjectLayers: map[string]int{
			"base":       0,
			"child-a":    1,
			"child-b":    1,
			"grandchild": 2,
			"leaf":       3,
		},
	}

	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "base", Status: models.PlannedPlanStatus, Layer: 0},
		},
		LayerState: state,
	}

	ctx := &command.Context{
		Log: logger,
		Pull: models.PullRequest{
			BaseRepo: models.Repo{FullName: "test/repo"},
			Num:      1,
		},
	}

	data := lm.buildDashboardData(ctx, pullStatus)

	// Layer 0 has 1 project shown on the dashboard.
	// Layers 1-3 have 4 projects not yet shown = pending.
	assert.Equal(t, 4, data.PendingCount, "should count future-layer projects as pending")
	assert.Equal(t, 5, data.TotalCount, "total = shown (1) + pending (4)")
}

func TestBuildDashboardData_PendingCountIncludesPendingProjects(t *testing.T) {
	lm := NewLayerManager(NewLayerStateManager(), nil)
	logger := logging.NewNoopLogger(t)

	// Build a graph to enable layer state
	graph, err := layered.NewDependencyGraph([]layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "child", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "transitive-dep", DependsOn: []layered.ProjectID{"child"}, HasFileChanges: false},
	})
	require.NoError(t, err)

	// Simulate a scenario with both future-layer projects AND pending projects
	// (transitive dependents without file changes).
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  2,
		ProjectLayers: map[string]int{
			"base":  0,
			"child": 1,
		},
		PendingProjects: []string{"transitive-dep"},
	}

	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "base", Status: models.PlannedPlanStatus, Layer: 0},
		},
		LayerState: state,
	}

	ctx := &command.Context{
		Log: logger,
		Pull: models.PullRequest{
			BaseRepo: models.Repo{FullName: "test/repo"},
			Num:      1,
		},
	}

	data := lm.buildDashboardData(ctx, pullStatus)

	// 1 pending project (transitive-dep) + 1 future-layer project (child) = 2 pending
	assert.Equal(t, 2, data.PendingCount, "should count both pending and future-layer projects")
	assert.Equal(t, 3, data.TotalCount, "total = shown (1) + pending (2)")
}

func TestBuildDashboardData_NoPendingWhenAllShown(t *testing.T) {
	lm := NewLayerManager(NewLayerStateManager(), nil)
	logger := logging.NewNoopLogger(t)

	// Build a graph to enable layer state (independent projects, no dependencies)
	graph, err := layered.NewDependencyGraph([]layered.ProjectNode{
		{ID: "a", DependsOn: nil, HasFileChanges: true},
		{ID: "b", DependsOn: nil, HasFileChanges: true},
	})
	require.NoError(t, err)

	// All projects are in the current layer — nothing pending.
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  1,
		ProjectLayers: map[string]int{
			"a": 0,
			"b": 0,
		},
	}

	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "a", Status: models.PlannedPlanStatus, Layer: 0},
			{ProjectName: "b", Status: models.PlannedPlanStatus, Layer: 0},
		},
		LayerState: state,
	}

	ctx := &command.Context{
		Log: logger,
		Pull: models.PullRequest{
			BaseRepo: models.Repo{FullName: "test/repo"},
			Num:      1,
		},
	}

	data := lm.buildDashboardData(ctx, pullStatus)

	assert.Equal(t, 0, data.PendingCount, "no pending when all projects are shown")
	assert.Equal(t, 2, data.TotalCount, "total = shown (2)")
}
