// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/layered"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustBuildGraphLM creates a DependencyGraph for testing, panicking on error.
func mustBuildGraphLM(t *testing.T, nodes []layered.ProjectNode) *layered.DependencyGraph {
	t.Helper()
	g, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)
	return g
}

func TestLayerManager_ShouldActivate(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	tests := []struct {
		name     string
		cmds     []command.ProjectContext
		expected bool
	}{
		{
			name:     "no projects",
			cmds:     nil,
			expected: false,
		},
		{
			name: "no depends_on",
			cmds: []command.ProjectContext{
				{ProjectName: "a", CommandName: command.Plan},
				{ProjectName: "b", CommandName: command.Plan},
			},
			expected: false,
		},
		{
			name: "has depends_on",
			cmds: []command.ProjectContext{
				{ProjectName: "a", CommandName: command.Plan},
				{ProjectName: "b", CommandName: command.Plan, DependsOn: []string{"a"}},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, lm.ShouldActivate(tt.cmds))
		})
	}
}

func TestLayerManager_InitializeLayerState(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	cmds := []command.ProjectContext{
		{ProjectName: "base", CommandName: command.Plan, Workspace: "default", RepoRelDir: "base"},
		{ProjectName: "child", CommandName: command.Plan, Workspace: "default", RepoRelDir: "child", DependsOn: []string{"base"}},
	}

	state, err := lm.InitializeLayerState(cmds)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Enabled())
	assert.Equal(t, 2, state.TotalLayers)
	assert.Equal(t, 0, state.CurrentLayer)
	assert.Equal(t, 0, state.ProjectLayers["base"])
	assert.Equal(t, 1, state.ProjectLayers["child"])
}

func TestLayerManager_InitializeLayerState_NoDeps(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	cmds := []command.ProjectContext{
		{ProjectName: "a", CommandName: command.Plan, Workspace: "default", RepoRelDir: "a"},
		{ProjectName: "b", CommandName: command.Plan, Workspace: "default", RepoRelDir: "b"},
	}

	state, err := lm.InitializeLayerState(cmds)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestLayerManager_HasMultipleLayers(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	// Build graphs for enabled states
	singleLayerGraph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "a", DependsOn: nil, HasFileChanges: true},
	})
	multiLayerGraph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "a", DependsOn: nil, HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
	})

	assert.False(t, lm.HasMultipleLayers(nil))
	assert.False(t, lm.HasMultipleLayers(&models.LayerState{Graph: singleLayerGraph, TotalLayers: 1}))
	assert.True(t, lm.HasMultipleLayers(&models.LayerState{Graph: multiLayerGraph, TotalLayers: 2}))
}

func TestLayerManager_FilterToCurrentLayer(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "child", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  2,
		ProjectLayers: map[string]int{
			"base":  0,
			"child": 1,
		},
	}

	cmds := []command.ProjectContext{
		{ProjectName: "base", CommandName: command.Plan},
		{ProjectName: "child", CommandName: command.Plan},
	}

	filtered := lm.FilterToCurrentLayer(cmds, state)
	require.Len(t, filtered, 1)
	assert.Equal(t, "base", filtered[0].ProjectName)
}

func TestLayerManager_FilterToCurrentLayer_NilState(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	cmds := []command.ProjectContext{
		{ProjectName: "a", CommandName: command.Plan},
		{ProjectName: "b", CommandName: command.Plan},
	}

	filtered := lm.FilterToCurrentLayer(cmds, nil)
	require.Len(t, filtered, 2)
}

func TestLayerManager_FilterToCurrentLayer_Layer1(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "mid", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "mid2", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "grandchild", DependsOn: []layered.ProjectID{"mid"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 1,
		TotalLayers:  3,
		ProjectLayers: map[string]int{
			"base":       0,
			"mid":        1,
			"mid2":       1,
			"grandchild": 2,
		},
	}

	cmds := []command.ProjectContext{
		{ProjectName: "base", CommandName: command.Plan},
		{ProjectName: "mid", CommandName: command.Plan},
		{ProjectName: "mid2", CommandName: command.Plan},
		{ProjectName: "grandchild", CommandName: command.Plan},
	}

	filtered := lm.FilterToCurrentLayer(cmds, state)
	require.Len(t, filtered, 2)
	names := make(map[string]bool)
	for _, f := range filtered {
		names[f.ProjectName] = true
	}
	assert.True(t, names["mid"])
	assert.True(t, names["mid2"])
}

func TestLayerManager_IsInCurrentLayer(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "child", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  2,
		ProjectLayers: map[string]int{
			"base":  0,
			"child": 1,
		},
	}

	assert.True(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "base"}, state))
	assert.False(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "child"}, state))
	assert.True(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "anything"}, nil))
}

func TestLayerManager_IsInCurrentLayer_FutureLayerRejected(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "mid", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "grandchild", DependsOn: []layered.ProjectID{"mid"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  3,
		ProjectLayers: map[string]int{
			"base":       0,
			"mid":        1,
			"grandchild": 2,
		},
	}

	// Current layer project should be allowed
	assert.True(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "base"}, state))

	// Future layer projects should be rejected
	assert.False(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "mid"}, state))
	assert.False(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "grandchild"}, state))

	// Unknown project (not in ProjectLayers) should be rejected
	assert.False(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "unknown"}, state))
}

func TestLayerManager_IsInCurrentLayer_AdvancedToLayer1(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "mid", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "grandchild", DependsOn: []layered.ProjectID{"mid"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 1,
		TotalLayers:  3,
		ProjectLayers: map[string]int{
			"base":       0,
			"mid":        1,
			"grandchild": 2,
		},
	}

	// Layer 0 (past layer) project - not in current layer
	assert.False(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "base"}, state))

	// Layer 1 (current layer) project - should be allowed
	assert.True(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "mid"}, state))

	// Layer 2 (future layer) project - should be rejected
	assert.False(t, lm.IsInCurrentLayer(command.ProjectContext{ProjectName: "grandchild"}, state))
}

func TestLayerManager_FilterToCurrentLayer_ExcludesFutureAndPast(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "mid-a", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "mid-b", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
		{ID: "grandchild", DependsOn: []layered.ProjectID{"mid-a"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 1,
		TotalLayers:  3,
		ProjectLayers: map[string]int{
			"base":       0,
			"mid-a":      1,
			"mid-b":      1,
			"grandchild": 2,
		},
	}

	cmds := []command.ProjectContext{
		{ProjectName: "base", CommandName: command.Apply},
		{ProjectName: "mid-a", CommandName: command.Apply},
		{ProjectName: "mid-b", CommandName: command.Apply},
		{ProjectName: "grandchild", CommandName: command.Apply},
	}

	filtered := lm.FilterToCurrentLayer(cmds, state)
	require.Len(t, filtered, 2)
	names := make(map[string]bool)
	for _, f := range filtered {
		names[f.ProjectName] = true
	}
	assert.True(t, names["mid-a"], "current-layer project mid-a should be included")
	assert.True(t, names["mid-b"], "current-layer project mid-b should be included")
	assert.False(t, names["base"], "past-layer project base should be excluded")
	assert.False(t, names["grandchild"], "future-layer project grandchild should be excluded")
}

func TestLayerManager_StampAndSaveLayerState(t *testing.T) {
	lm := events.NewLayerManager(events.NewLayerStateManager(), nil)

	graph := mustBuildGraphLM(t, []layered.ProjectNode{
		{ID: "base", DependsOn: nil, HasFileChanges: true},
		{ID: "child", DependsOn: []layered.ProjectID{"base"}, HasFileChanges: true},
	})
	state := &models.LayerState{
		Graph:        graph,
		CurrentLayer: 0,
		TotalLayers:  2,
		ProjectLayers: map[string]int{
			"base":  0,
			"child": 1,
		},
	}

	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "base", Status: models.PlannedPlanStatus},
			{ProjectName: "child", Status: models.PlannedPlanStatus},
		},
	}

	lm.StampAndSaveLayerState(pullStatus, state)

	assert.NotNil(t, pullStatus.LayerState)
	assert.Equal(t, 0, pullStatus.Projects[0].Layer)
	assert.Equal(t, 1, pullStatus.Projects[1].Layer)
}
