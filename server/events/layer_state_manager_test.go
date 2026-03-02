// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"sort"
	"testing"

	"github.com/runatlantis/atlantis/server/events/layered"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustBuildGraph creates a graph for testing, panics on error.
func mustBuildGraph(t *testing.T, nodes []layered.ProjectNode) *layered.DependencyGraph {
	t.Helper()
	g, err := layered.NewDependencyGraph(nodes)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}
	return g
}

// --- InitializeLayerState tests ---

func TestLayerStateInit_NoDependencies(t *testing.T) {
	t.Log("should return nil when no dependencies exist")
	m := NewLayerStateManager()

	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", HasFileChanges: true},
		{ID: "c", HasFileChanges: true},
	}
	graph, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	projects := []models.ProjectStatus{
		{ProjectName: "a"},
		{ProjectName: "b"},
		{ProjectName: "c"},
	}

	state := m.InitializeLayerState(projects, graph)
	Assert(t, state == nil, "expected nil state when no deps")
}

func TestInitializeLayerState_WithDependencies(t *testing.T) {
	manager := NewLayerStateManager()

	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil, HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
		{ID: "c", DependsOn: []layered.ProjectID{"b"}, HasFileChanges: true},
	}
	graph, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	projects := []models.ProjectStatus{
		{ProjectName: "a"},
		{ProjectName: "b"},
		{ProjectName: "c"},
	}

	state := manager.InitializeLayerState(projects, graph)
	require.NotNil(t, state)

	assert.Equal(t, 0, state.ProjectLayers["a"])
	assert.Equal(t, 1, state.ProjectLayers["b"])
	assert.Equal(t, 2, state.ProjectLayers["c"])
	assert.Equal(t, 3, state.TotalLayers)
	assert.Same(t, graph, state.Graph)
}

func TestLayerStateInit_SimpleChainOnlyRootChanged(t *testing.T) {
	t.Log("A -> B -> C, only A changed: layer 0 has A, B and C are pending")
	m := NewLayerStateManager()

	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: false},
		{ID: "c", DependsOn: []layered.ProjectID{"b"}, HasFileChanges: false},
	}
	graph, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	projects := []models.ProjectStatus{
		{ProjectName: "a"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)
	assert.Equal(t, 1, state.TotalLayers)
	assert.Equal(t, 0, state.ProjectLayers["a"])

	// B and C should be pending (not assigned a layer yet)
	sort.Strings(state.PendingProjects)
	assert.Equal(t, []string{"b", "c"}, state.PendingProjects)
}

func TestLayerStateInit_Diamond(t *testing.T) {
	t.Log("diamond: A -> B, A -> C, B -> D, C -> D, all changed")
	m := NewLayerStateManager()

	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
		{ID: "c", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
		{ID: "d", DependsOn: []layered.ProjectID{"b", "c"}, HasFileChanges: true},
	}
	graph, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	projects := []models.ProjectStatus{
		{ProjectName: "a"},
		{ProjectName: "b"},
		{ProjectName: "c"},
		{ProjectName: "d"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)
	assert.Equal(t, 3, state.TotalLayers)
	assert.Equal(t, 0, state.ProjectLayers["a"])
	assert.Equal(t, 1, state.ProjectLayers["b"])
	assert.Equal(t, 1, state.ProjectLayers["c"])
	assert.Equal(t, 2, state.ProjectLayers["d"])
}

func TestLayerStateInit_ParallelChains(t *testing.T) {
	t.Log("parallel independent chains: A -> B, C -> D")
	m := NewLayerStateManager()

	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
		{ID: "c", HasFileChanges: true},
		{ID: "d", DependsOn: []layered.ProjectID{"c"}, HasFileChanges: true},
	}
	graph, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	projects := []models.ProjectStatus{
		{ProjectName: "a"},
		{ProjectName: "b"},
		{ProjectName: "c"},
		{ProjectName: "d"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)
	assert.Equal(t, 2, state.TotalLayers)
	assert.Equal(t, 0, state.ProjectLayers["a"])
	assert.Equal(t, 1, state.ProjectLayers["b"])
	assert.Equal(t, 0, state.ProjectLayers["c"])
	assert.Equal(t, 1, state.ProjectLayers["d"])
}

func TestLayerStateInit_CircularDependency(t *testing.T) {
	t.Log("circular dependency should error at graph construction")

	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: []layered.ProjectID{"b"}, HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
	}
	_, err := layered.NewDependencyGraph(nodes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestLayerStateInit_MixedChangedAndPending(t *testing.T) {
	t.Log("A changed, B depends on A but not changed -> B is pending")
	m := NewLayerStateManager()

	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: false},
	}
	graph, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	projects := []models.ProjectStatus{
		{ProjectName: "a"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)
	assert.Equal(t, 0, state.ProjectLayers["a"])
	assert.Equal(t, []string{"b"}, state.PendingProjects)
}

// --- GetCurrentLayerProjects tests ---

func TestLayerStateGetCurrentLayerProjects(t *testing.T) {
	m := NewLayerStateManager()
	state := &models.LayerState{
		CurrentLayer: 0,
		TotalLayers:  2,
		ProjectLayers: map[string]int{
			"A": 0,
			"B": 0,
			"C": 1,
		},
	}

	projects := m.GetCurrentLayerProjects(state)
	sort.Strings(projects)
	Equals(t, []string{"A", "B"}, projects)
}

func TestLayerStateGetCurrentLayerProjects_Layer1(t *testing.T) {
	m := NewLayerStateManager()
	state := &models.LayerState{
		CurrentLayer: 1,
		TotalLayers:  2,
		ProjectLayers: map[string]int{
			"A": 0,
			"B": 0,
			"C": 1,
		},
	}

	projects := m.GetCurrentLayerProjects(state)
	Equals(t, []string{"C"}, projects)
}

// --- GetPendingCount tests ---

func TestLayerStateGetPendingCount(t *testing.T) {
	m := NewLayerStateManager()
	state := &models.LayerState{
		PendingProjects: []string{"X", "Y"},
	}
	Equals(t, 2, m.GetPendingCount(state))
}

func TestLayerStateGetPendingCount_NoPending(t *testing.T) {
	m := NewLayerStateManager()
	state := &models.LayerState{}
	Equals(t, 0, m.GetPendingCount(state))
}

// --- IsLayerComplete tests ---

func TestLayerStateIsLayerComplete_AllApplied(t *testing.T) {
	t.Log("all projects applied -> layer complete")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{CurrentLayer: 0},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)
	Equals(t, 0, len(blocking))
}

func TestLayerStateIsLayerComplete_AllNoChanges(t *testing.T) {
	t.Log("all no-changes -> layer complete")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedNoChangesPlanStatus},
			{ProjectName: "B", Layer: 0, Status: models.PlannedNoChangesPlanStatus},
		},
		LayerState: &models.LayerState{CurrentLayer: 0},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)
	Equals(t, 0, len(blocking))
}

func TestLayerStateIsLayerComplete_MixedTerminal(t *testing.T) {
	t.Log("mixed terminal states (applied, no-changes, skipped) -> complete")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 0, Status: models.PlannedNoChangesPlanStatus},
			{ProjectName: "C", Layer: 0, Status: models.SkippedPlanStatus},
		},
		LayerState: &models.LayerState{CurrentLayer: 0},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)
	Equals(t, 0, len(blocking))
}

func TestLayerStateIsLayerComplete_HasPlanned(t *testing.T) {
	t.Log("project still planned (needs apply) -> blocking")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 0, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{CurrentLayer: 0},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, false, complete)
	Equals(t, []string{"B"}, blocking)
}

func TestLayerStateIsLayerComplete_HasErroredApply(t *testing.T) {
	t.Log("errored apply -> blocking")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 0, Status: models.ErroredApplyStatus},
		},
		LayerState: &models.LayerState{CurrentLayer: 0},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, false, complete)
	Equals(t, []string{"B"}, blocking)
}

func TestLayerStateIsLayerComplete_HasErroredPlan(t *testing.T) {
	t.Log("errored plan -> blocking")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.ErroredPlanStatus},
			{ProjectName: "B", Layer: 0, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{CurrentLayer: 0},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, false, complete)
	sort.Strings(blocking)
	Equals(t, []string{"A", "B"}, blocking)
}

func TestLayerStateIsLayerComplete_NilLayerState(t *testing.T) {
	t.Log("nil layer state -> always complete")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Status: models.PlannedPlanStatus},
		},
	}

	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)
	Equals(t, 0, len(blocking))
}

// --- CanAdvance tests ---

func TestLayerStateCanAdvance_LayerComplete(t *testing.T) {
	t.Log("current layer complete, more layers -> can advance")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			TotalLayers:  2,
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 1,
			},
		},
	}

	Equals(t, true, m.CanAdvance(pullStatus))
}

func TestLayerStateCanAdvance_LayerNotComplete(t *testing.T) {
	t.Log("current layer not complete -> cannot advance")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			TotalLayers:  2,
			ProjectLayers: map[string]int{
				"A": 0,
			},
		},
	}

	Equals(t, false, m.CanAdvance(pullStatus))
}

func TestLayerStateCanAdvance_LastLayer(t *testing.T) {
	t.Log("at last layer with no pending -> cannot advance")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			TotalLayers:  1,
			ProjectLayers: map[string]int{
				"A": 0,
			},
		},
	}

	Equals(t, false, m.CanAdvance(pullStatus))
}

func TestLayerStateCanAdvance_NilLayerState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{}
	Equals(t, false, m.CanAdvance(pullStatus))
}

func TestLayerStateCanAdvance_HasPendingProjects(t *testing.T) {
	t.Log("at last known layer but has pending projects -> can advance")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer:    0,
			TotalLayers:     1,
			ProjectLayers:   map[string]int{"A": 0},
			PendingProjects: []string{"B"},
		},
	}

	Equals(t, true, m.CanAdvance(pullStatus))
}

// --- AdvanceLayer tests ---

func TestLayerStateAdvance_UpstreamHadChanges(t *testing.T) {
	t.Log("upstream applied with changes -> pending dependent enters next layer")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			TotalLayers:      1,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  []string{"B"},
			SkippedUpstreams: map[string]bool{},
		},
	}

	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, []string{"B"}, nextProjects)
	Equals(t, 1, state.CurrentLayer)
	Equals(t, 2, state.TotalLayers)
	Equals(t, 1, state.ProjectLayers["B"])
	Equals(t, 0, len(state.PendingProjects))
}

func TestLayerStateAdvance_UpstreamNoChanges(t *testing.T) {
	t.Log("upstream had no changes -> cascade stops, dependent excluded")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedNoChangesPlanStatus},
		},
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			TotalLayers:      1,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  []string{"B"},
			SkippedUpstreams: map[string]bool{},
		},
	}

	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, 0, len(nextProjects))
	Equals(t, -1, state.CurrentLayer) // all done
}

func TestLayerStateAdvance_UpstreamSkipped(t *testing.T) {
	t.Log("upstream was skipped -> cascade stops, dependent excluded")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.SkippedPlanStatus},
		},
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			TotalLayers:      1,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  []string{"B"},
			SkippedUpstreams: map[string]bool{"A": true},
		},
	}

	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, 0, len(nextProjects))
	Equals(t, -1, state.CurrentLayer) // all done
}

func TestLayerStateAdvance_MultipleUpstreamsMixed(t *testing.T) {
	t.Log("B depends on A and C; A=applied, C=no-changes -> B still included")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "C", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A", "C"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "C", Layer: 0, Status: models.PlannedNoChangesPlanStatus},
		},
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			TotalLayers:      1,
			ProjectLayers:    map[string]int{"A": 0, "C": 0},
			PendingProjects:  []string{"B"},
			SkippedUpstreams: map[string]bool{},
		},
	}

	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, []string{"B"}, nextProjects)
	Equals(t, 1, state.CurrentLayer)
}

func TestLayerStateAdvance_NoPendingProjects(t *testing.T) {
	t.Log("no pending projects -> all done")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
	})
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			TotalLayers:      1,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  nil,
			SkippedUpstreams: map[string]bool{},
		},
	}

	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, 0, len(nextProjects))
	Equals(t, -1, state.CurrentLayer)
}

func TestLayerStateAdvance_MultiLayerCascade(t *testing.T) {
	t.Log("A -> B -> C: advance from 0 adds B to 1, C stays pending")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
		{ID: "C", DependsOn: []layered.ProjectID{"B"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			TotalLayers:      1,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  []string{"B", "C"},
			SkippedUpstreams: map[string]bool{},
		},
	}

	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, []string{"B"}, nextProjects)
	Equals(t, 1, state.CurrentLayer)
	// C should still be pending (its dep B is in a future layer)
	Equals(t, []string{"C"}, state.PendingProjects)
}

func TestLayerStateAdvance_NilState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{}

	_, _, err := m.AdvanceLayer(pullStatus)
	Assert(t, err != nil, "expected error for nil layer state")
}

// --- SkipProject tests ---

func TestLayerStateSkip_Disabled(t *testing.T) {
	t.Log("skip disabled -> error")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			ProjectLayers: map[string]int{"A": 0},
		},
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.ErroredApplyStatus},
		},
	}

	_, err := m.SkipProject(pullStatus, "A", false)
	Assert(t, err != nil, "expected error when skip disabled")
	ErrContains(t, "skip functionality is not enabled", err)
}

func TestLayerStateSkip_NotInCurrentLayer(t *testing.T) {
	t.Log("project not in current layer -> error")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			ProjectLayers: map[string]int{"A": 1},
		},
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 1, Status: models.ErroredApplyStatus},
		},
	}

	_, err := m.SkipProject(pullStatus, "A", true)
	Assert(t, err != nil, "expected error for project not in current layer")
	ErrContains(t, "not in the current layer", err)
}

func TestLayerStateSkip_NotErrored(t *testing.T) {
	t.Log("project not errored -> error")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			ProjectLayers: map[string]int{"A": 0},
		},
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedPlanStatus},
		},
	}

	_, err := m.SkipProject(pullStatus, "A", true)
	Assert(t, err != nil, "expected error for non-errored project")
	ErrContains(t, "can only skip projects with failed applies", err)
}

func TestLayerStateSkip_Valid(t *testing.T) {
	t.Log("valid skip -> status updated, marked in skipped upstreams")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  []string{"B"},
			SkippedUpstreams: map[string]bool{},
		},
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.ErroredApplyStatus},
		},
	}

	result, err := m.SkipProject(pullStatus, "A", true)
	Ok(t, err)
	Equals(t, models.SkippedPlanStatus, result.Projects[0].Status)
	Equals(t, true, result.LayerState.SkippedUpstreams["A"])
}

func TestLayerStateSkip_TransitiveDependents(t *testing.T) {
	t.Log("skip A -> B (depends on A) excluded -> C (depends on B) excluded")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
		{ID: "C", DependsOn: []layered.ProjectID{"B"}, HasFileChanges: false},
	})
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			ProjectLayers:    map[string]int{"A": 0},
			PendingProjects:  []string{"B", "C"},
			SkippedUpstreams: map[string]bool{},
		},
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.ErroredApplyStatus},
		},
	}

	result, err := m.SkipProject(pullStatus, "A", true)
	Ok(t, err)
	// Both B and C should be in skipped upstreams
	Equals(t, true, result.LayerState.SkippedUpstreams["A"])
	Equals(t, true, result.LayerState.SkippedUpstreams["B"])
	Equals(t, true, result.LayerState.SkippedUpstreams["C"])
	// B and C should be removed from pending
	Equals(t, 0, len(result.LayerState.PendingProjects))
}

func TestLayerStateSkip_PreservesWorkspaceAndDir(t *testing.T) {
	t.Log("skip should preserve workspace and repoRelDir for persistence")
	m := NewLayerStateManager()
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "vpc", HasFileChanges: true},
	})
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
			Graph:            graph,
			CurrentLayer:     0,
			ProjectLayers:    map[string]int{"vpc": 0},
			SkippedUpstreams: map[string]bool{},
		},
		Projects: []models.ProjectStatus{
			{
				ProjectName: "vpc",
				Workspace:   "production",
				RepoRelDir:  "infra/vpc",
				Layer:       0,
				Status:      models.ErroredApplyStatus,
			},
		},
	}

	result, err := m.SkipProject(pullStatus, "vpc", true)
	Ok(t, err)
	Equals(t, models.SkippedPlanStatus, result.Projects[0].Status)
	// Verify workspace and repoRelDir are preserved so UpdateProjectStatus
	// can find the project in the database.
	Equals(t, "production", result.Projects[0].Workspace)
	Equals(t, "infra/vpc", result.Projects[0].RepoRelDir)
}

func TestLayerStateSkip_NilLayerState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{}

	_, err := m.SkipProject(pullStatus, "A", true)
	Assert(t, err != nil, "expected error for nil layer state")
	ErrContains(t, "layered planning is not active", err)
}

func TestLayerStateSkip_ProjectNotFound(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer:  0,
			ProjectLayers: map[string]int{"A": 0},
		},
		Projects: []models.ProjectStatus{},
	}

	_, err := m.SkipProject(pullStatus, "A", true)
	Assert(t, err != nil, "expected error for missing project")
	ErrContains(t, "not found in pull status", err)
}

// --- HandleNewCommit tests ---

func TestLayerStateHandleNewCommit_FutureLayer(t *testing.T) {
	t.Log("affected project in future layer -> no action")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 1, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 1,
			},
		},
	}

	action := m.HandleNewCommit(pullStatus, []string{"B"})
	Equals(t, true, action.NoAction)
}

func TestLayerStateHandleNewCommit_CurrentLayerNotApplied(t *testing.T) {
	t.Log("affected project in current layer, not applied -> replan")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer:  0,
			ProjectLayers: map[string]int{"A": 0},
		},
	}

	action := m.HandleNewCommit(pullStatus, []string{"A"})
	Equals(t, false, action.NoAction)
	Equals(t, -1, action.ResetToLayer)
	Equals(t, []string{"A"}, action.ReplanProjects)
}

func TestLayerStateHandleNewCommit_CurrentLayerAlreadyApplied(t *testing.T) {
	t.Log("affected project in current layer, already applied -> reset to current layer")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer:  0,
			TotalLayers:   2,
			ProjectLayers: map[string]int{"A": 0},
		},
	}

	action := m.HandleNewCommit(pullStatus, []string{"A"})
	Equals(t, false, action.NoAction)
	Equals(t, 0, action.ResetToLayer)
}

func TestLayerStateHandleNewCommit_CompletedLayer(t *testing.T) {
	t.Log("affected project in completed layer -> reset to that layer")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 1, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 1,
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 1,
			},
		},
	}

	action := m.HandleNewCommit(pullStatus, []string{"A"})
	Equals(t, false, action.NoAction)
	Equals(t, 0, action.ResetToLayer)
}

func TestLayerStateHandleNewCommit_MultipleLayers(t *testing.T) {
	t.Log("multiple affected layers -> reset to minimum")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 1, Status: models.AppliedStatus},
			{ProjectName: "C", Layer: 2, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 2,
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 1,
				"C": 2,
			},
		},
	}

	action := m.HandleNewCommit(pullStatus, []string{"A", "B"})
	Equals(t, 0, action.ResetToLayer)
}

func TestLayerStateHandleNewCommit_NoAffected(t *testing.T) {
	t.Log("no affected projects in any layer -> no action")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer:  0,
			ProjectLayers: map[string]int{"A": 0},
		},
	}

	action := m.HandleNewCommit(pullStatus, []string{"X"})
	Equals(t, true, action.NoAction)
}

func TestLayerStateHandleNewCommit_NilState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{}

	action := m.HandleNewCommit(pullStatus, []string{"A"})
	Equals(t, true, action.NoAction)
}

// --- ResetLayerState tests ---

func TestResetLayerState_ClearsCorrectLayers(t *testing.T) {
	t.Log("reset to layer 1 should clear layers 1, 2, 3 and preserve layer 0")
	m := NewLayerStateManager()
	state := &models.LayerState{
		CurrentLayer: 3,
		TotalLayers:  4,
		ProjectLayers: map[string]int{
			"A": 0,
			"B": 1,
			"C": 2,
			"D": 3,
		},
		PendingProjects:  []string{},
		SkippedUpstreams: map[string]bool{"B": true},
	}

	err := m.ResetLayerState(state, 1)
	Ok(t, err)

	// Layer 0 projects should remain
	Equals(t, 0, state.ProjectLayers["A"])
	// Layers 1+ projects should be removed from ProjectLayers
	_, hasB := state.ProjectLayers["B"]
	_, hasC := state.ProjectLayers["C"]
	_, hasD := state.ProjectLayers["D"]
	Equals(t, false, hasB)
	Equals(t, false, hasC)
	Equals(t, false, hasD)

	// CurrentLayer should be set to resetToLayer
	Equals(t, 1, state.CurrentLayer)
	// TotalLayers should be decremented
	Equals(t, 1, state.TotalLayers)

	// Cleared projects should be in PendingProjects
	Equals(t, 3, len(state.PendingProjects))

	// Skipped upstreams for invalidated projects should be cleared
	_, skippedB := state.SkippedUpstreams["B"]
	Equals(t, false, skippedB)
}

func TestResetLayerState_ResetToLayerZero(t *testing.T) {
	t.Log("reset to layer 0 should clear all layers")
	m := NewLayerStateManager()
	state := &models.LayerState{
		CurrentLayer: 2,
		TotalLayers:  3,
		ProjectLayers: map[string]int{
			"A": 0,
			"B": 1,
			"C": 2,
		},
		PendingProjects:  []string{},
		SkippedUpstreams: map[string]bool{},
	}

	err := m.ResetLayerState(state, 0)
	Ok(t, err)

	Equals(t, 0, len(state.ProjectLayers))
	Equals(t, 0, state.CurrentLayer)
	Equals(t, 0, state.TotalLayers)
	Equals(t, 3, len(state.PendingProjects))
}

func TestResetLayerState_NilState(t *testing.T) {
	m := NewLayerStateManager()
	err := m.ResetLayerState(nil, 0)
	Assert(t, err != nil, "expected error for nil state")
}

func TestResetLayerState_InvalidLayer(t *testing.T) {
	m := NewLayerStateManager()
	state := &models.LayerState{
		CurrentLayer: 1,
		TotalLayers:  2,
	}
	err := m.ResetLayerState(state, 5)
	Assert(t, err != nil, "expected error for invalid layer")
}

// --- IsAllComplete tests ---

func TestLayerStateIsAllComplete_CurrentLayerNegative(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer: -1,
		},
	}
	Equals(t, true, m.IsAllComplete(pullStatus))
}

func TestLayerStateIsAllComplete_StillActive(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			TotalLayers:  2,
		},
	}
	Equals(t, false, m.IsAllComplete(pullStatus))
}

func TestLayerStateIsAllComplete_NilState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{}
	Equals(t, true, m.IsAllComplete(pullStatus))
}

// --- GetLayerSummary tests ---

func TestLayerStateGetLayerSummary(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "B", Layer: 0, Status: models.AppliedStatus},
			{ProjectName: "C", Layer: 1, Status: models.PlannedPlanStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 1,
			TotalLayers:  2,
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 0,
				"C": 1,
			},
		},
	}

	summary := m.GetLayerSummary(pullStatus)
	Equals(t, 2, len(summary))

	// Layer 0
	Equals(t, 0, summary[0].Layer)
	Equals(t, false, summary[0].IsCurrent)
	Equals(t, true, summary[0].IsComplete)
	Equals(t, 2, len(summary[0].Projects))

	// Layer 1
	Equals(t, 1, summary[1].Layer)
	Equals(t, true, summary[1].IsCurrent)
	Equals(t, false, summary[1].IsComplete)
	Equals(t, 1, len(summary[1].Projects))
}

func TestLayerStateGetLayerSummary_CurrentLayerNoResults(t *testing.T) {
	t.Log("current layer projects appear as placeholders before plan results arrive")
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		// Only layer 0 projects have results — layer 1 hasn't been planned yet
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.AppliedStatus},
		},
		LayerState: &models.LayerState{
						CurrentLayer: 1,
			TotalLayers:  2,
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 1,
				"C": 1,
			},
		},
	}

	summary := m.GetLayerSummary(pullStatus)
	Equals(t, 2, len(summary))

	// Layer 0 — completed
	Equals(t, 0, summary[0].Layer)
	Equals(t, 1, len(summary[0].Projects))

	// Layer 1 — current, should have placeholder entries for B and C
	Equals(t, 1, summary[1].Layer)
	Equals(t, true, summary[1].IsCurrent)
	Equals(t, 2, len(summary[1].Projects))

	// Placeholder projects should have PendingPlanStatus (zero value, renders as "Planning...")
	names := map[string]bool{}
	for _, p := range summary[1].Projects {
		names[p.ProjectName] = true
		Equals(t, models.PendingPlanStatus, p.Status)
	}
	Assert(t, names["B"], "expected B in current layer")
	Assert(t, names["C"], "expected C in current layer")
}

func TestLayerStateGetLayerSummary_NilState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{}
	summary := m.GetLayerSummary(pullStatus)
	Equals(t, 0, len(summary))
}

// --- StampLayerAssignments tests ---

func TestLayerStateStampAssignments(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A"},
			{ProjectName: "B"},
			{ProjectName: "C"},
		},
		LayerState: &models.LayerState{
			ProjectLayers: map[string]int{
				"A": 0,
				"B": 1,
				"C": 2,
			},
		},
	}

	m.StampLayerAssignments(pullStatus)
	Equals(t, 0, pullStatus.Projects[0].Layer)
	Equals(t, 1, pullStatus.Projects[1].Layer)
	Equals(t, 2, pullStatus.Projects[2].Layer)
}

func TestLayerStateStampAssignments_NilState(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 5},
		},
	}

	// Should be a no-op
	m.StampLayerAssignments(pullStatus)
	Equals(t, 5, pullStatus.Projects[0].Layer)
}

// --- Integration / lifecycle tests ---

func TestLayerStateFullLifecycle(t *testing.T) {
	t.Log("full lifecycle: init -> plan L0 -> apply L0 -> advance -> plan L1 -> apply L1 -> complete")
	m := NewLayerStateManager()

	// Initialize with A -> B chain, both changed
	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: true},
	})
	projects := []models.ProjectStatus{
		{ProjectName: "A"},
		{ProjectName: "B"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)
	assert.Equal(t, 0, state.CurrentLayer)

	// Build PullStatus
	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedPlanStatus},
		},
		LayerState: state,
	}

	// Layer 0 not complete (A is planned, not applied)
	Equals(t, false, m.CanAdvance(pullStatus))
	Equals(t, false, m.IsAllComplete(pullStatus))

	// Apply A
	pullStatus.Projects[0].Status = models.AppliedStatus

	// Now layer 0 is complete
	complete, blocking := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)
	Equals(t, 0, len(blocking))
	Equals(t, true, m.CanAdvance(pullStatus))

	// Advance to layer 1
	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, []string{"B"}, nextProjects)
	Equals(t, 1, state.CurrentLayer)

	// Add B as planned
	pullStatus.Projects = append(pullStatus.Projects, models.ProjectStatus{
		ProjectName: "B", Layer: 1, Status: models.PlannedPlanStatus,
	})

	Equals(t, false, m.IsAllComplete(pullStatus))

	// Apply B
	pullStatus.Projects[1].Status = models.AppliedStatus
	complete, _ = m.IsLayerComplete(pullStatus, 1)
	Equals(t, true, complete)

	// No more pending, no more layers to advance
	Equals(t, false, m.CanAdvance(pullStatus))
}

func TestLayerStateLifecycle_NoChangesCascadeStop(t *testing.T) {
	t.Log("L0 plans with no changes -> L1 never created")
	m := NewLayerStateManager()

	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
	})
	projects := []models.ProjectStatus{
		{ProjectName: "A"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)

	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.PlannedNoChangesPlanStatus},
		},
		LayerState: state,
	}

	// Layer 0 complete (no-changes is terminal)
	complete, _ := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)
	Equals(t, true, m.CanAdvance(pullStatus)) // has pending projects

	// Advance: B should not enter next layer since A had no changes
	state, nextProjects, err := m.AdvanceLayer(pullStatus)
	Ok(t, err)
	Equals(t, 0, len(nextProjects))
	Equals(t, -1, state.CurrentLayer) // all done
}

func TestLayerStateLifecycle_SkipAndCascadeStop(t *testing.T) {
	t.Log("L0 apply fails -> skip -> advance -> dependents excluded")
	m := NewLayerStateManager()

	graph := mustBuildGraph(t, []layered.ProjectNode{
		{ID: "A", HasFileChanges: true},
		{ID: "B", DependsOn: []layered.ProjectID{"A"}, HasFileChanges: false},
	})
	projects := []models.ProjectStatus{
		{ProjectName: "A"},
	}

	state := m.InitializeLayerState(projects, graph)
	require.NotNil(t, state)

	pullStatus := &models.PullStatus{
		Projects: []models.ProjectStatus{
			{ProjectName: "A", Layer: 0, Status: models.ErroredApplyStatus},
		},
		LayerState: state,
	}

	// Skip A
	var err error
	pullStatus, err = m.SkipProject(pullStatus, "A", true)
	Ok(t, err)
	Equals(t, models.SkippedPlanStatus, pullStatus.Projects[0].Status)

	// Layer 0 now complete (skipped is terminal)
	complete, _ := m.IsLayerComplete(pullStatus, 0)
	Equals(t, true, complete)

	// B should have been removed from pending and marked as skipped dependent
	Equals(t, 0, len(pullStatus.LayerState.PendingProjects))

	// CanAdvance should return false since there are no pending projects left
	Equals(t, false, m.CanAdvance(pullStatus))
}

func TestGetLayerSummary_DeterministicOrdering(t *testing.T) {
	m := NewLayerStateManager()
	pullStatus := &models.PullStatus{
		LayerState: &models.LayerState{
						CurrentLayer: 0,
			TotalLayers:  1,
			ProjectLayers: map[string]int{
				"zebra":  0,
				"alpha":  0,
				"middle": 0,
			},
		},
		Projects: []models.ProjectStatus{},
	}

	// Run multiple times to catch non-deterministic behavior
	var firstOrder []string
	for i := 0; i < 10; i++ {
		summaries := m.GetLayerSummary(pullStatus)
		var names []string
		for _, p := range summaries[0].Projects {
			names = append(names, p.ProjectName)
		}
		if i == 0 {
			firstOrder = names
		} else {
			for j, name := range names {
				if name != firstOrder[j] {
					t.Fatalf("iteration %d: order changed from %v to %v", i, firstOrder, names)
				}
			}
		}
	}

	// Should be alphabetically sorted
	expected := []string{"alpha", "middle", "zebra"}
	for i, name := range firstOrder {
		if name != expected[i] {
			t.Errorf("expected %v, got %v", expected, firstOrder)
			break
		}
	}
}
