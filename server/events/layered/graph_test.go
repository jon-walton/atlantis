// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package layered_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/runatlantis/atlantis/server/events/layered"
	. "github.com/runatlantis/atlantis/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Graph Construction Tests ---

func TestNewDependencyGraph_EmptyGraph(t *testing.T) {
	g, err := layered.NewDependencyGraph(nil)
	Ok(t, err)
	Assert(t, g != nil, "graph should not be nil")
	Assert(t, !g.HasDependencies(), "empty graph should have no dependencies")
	Equals(t, 0, len(g.AllNodes()))
}

func TestNewDependencyGraph_SingleNodeNoDeps(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)
	Assert(t, !g.HasDependencies(), "single node with no deps should have no dependencies")
	Equals(t, 1, len(g.AllNodes()))
	Assert(t, g.GetNode("a") != nil, "node a should exist")
}

func TestNewDependencyGraph_SimpleChain(t *testing.T) {
	// B depends on A
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil},
		{ID: "b", DependsOn: []string{"a"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)
	Assert(t, g.HasDependencies(), "should have dependencies")

	deps := g.GetDependencies("b")
	Equals(t, []string{"a"}, deps)

	dependents := g.GetDependents("a")
	Equals(t, []string{"b"}, dependents)
}

func TestNewDependencyGraph_Diamond(t *testing.T) {
	// A -> B, A -> C, B -> D, C -> D
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "d", DependsOn: []string{"b", "c"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)
	Assert(t, g.HasDependencies(), "should have dependencies")
	Equals(t, 4, len(g.AllNodes()))

	deps := g.GetDependencies("d")
	Equals(t, []string{"b", "c"}, deps)

	dependents := g.GetDependents("a")
	Equals(t, []string{"b", "c"}, dependents)
}

func TestNewDependencyGraph_UndefinedDependency(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: []string{"x"}},
	}
	_, err := layered.NewDependencyGraph(nodes)
	Assert(t, err != nil, "should error on undefined dependency")
	ErrContains(t, `project "a" depends on "x", but no project named "x" exists`, err)

	var undefinedErr *layered.UndefinedDependencyError
	Assert(t, fmt.Errorf("%w", err) != nil, "error should be unwrappable")
	// Check it's the right type
	errAs := false
	if ude, ok := err.(*layered.UndefinedDependencyError); ok {
		errAs = true
		Equals(t, "a", ude.ProjectID)
		Equals(t, "x", ude.DependencyID)
	}
	_ = undefinedErr
	Assert(t, errAs, "error should be *UndefinedDependencyError")
}

func TestNewDependencyGraph_SelfReference(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: []string{"a"}},
	}
	_, err := layered.NewDependencyGraph(nodes)
	Assert(t, err != nil, "should error on self-reference")
	ErrContains(t, "circular dependency detected", err)
	ErrContains(t, "a", err)
}

func TestNewDependencyGraph_DirectCycle(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}
	_, err := layered.NewDependencyGraph(nodes)
	Assert(t, err != nil, "should error on direct cycle")

	var cycleErr *layered.CycleError
	if ce, ok := err.(*layered.CycleError); ok {
		cycleErr = ce
	}
	Assert(t, cycleErr != nil, "error should be *CycleError")
	Assert(t, len(cycleErr.Cycle) == 2, "cycle should contain 2 nodes, got %d", len(cycleErr.Cycle))
}

func TestNewDependencyGraph_TransitiveCycle(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	_, err := layered.NewDependencyGraph(nodes)
	Assert(t, err != nil, "should error on transitive cycle")

	var cycleErr *layered.CycleError
	if ce, ok := err.(*layered.CycleError); ok {
		cycleErr = ce
	}
	Assert(t, cycleErr != nil, "error should be *CycleError")
	Assert(t, len(cycleErr.Cycle) == 3, "cycle should contain 3 nodes, got %d", len(cycleErr.Cycle))
}

func TestNewDependencyGraph_MixedCycleAndValid(t *testing.T) {
	// A, B->A are valid; C->D->C is a cycle
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"d"}},
		{ID: "d", DependsOn: []string{"c"}},
	}
	_, err := layered.NewDependencyGraph(nodes)
	Assert(t, err != nil, "should detect cycle even with valid nodes present")
	ErrContains(t, "circular dependency detected", err)
}

func TestNewDependencyGraph_DuplicateProjectID(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil},
		{ID: "a", DependsOn: nil},
	}
	_, err := layered.NewDependencyGraph(nodes)
	Assert(t, err != nil, "should error on duplicate project ID")
	ErrContains(t, "duplicate project", err)
}

func TestNewDependencyGraph_LargeFanOut(t *testing.T) {
	// A with 100 dependents
	nodes := []layered.ProjectNode{
		{ID: "root", DependsOn: nil},
	}
	for i := range 100 {
		nodes = append(nodes, layered.ProjectNode{
			ID:        fmt.Sprintf("dep-%03d", i),
			DependsOn: []string{"root"},
		})
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)
	Equals(t, 101, len(g.AllNodes()))
	dependents := g.GetDependents("root")
	Equals(t, 100, len(dependents))
}

func TestNewDependencyGraph_LargeFanIn(t *testing.T) {
	// 100 projects all depending on A
	nodes := make([]layered.ProjectNode, 0, 101)
	nodes = append(nodes, layered.ProjectNode{ID: "target", DependsOn: nil})
	for i := range 100 {
		nodes = append(nodes, layered.ProjectNode{
			ID:        fmt.Sprintf("src-%03d", i),
			DependsOn: []string{"target"},
		})
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)
	deps := g.GetDependents("target")
	Equals(t, 100, len(deps))
}

func TestNewDependencyGraph_LongChain(t *testing.T) {
	// A->B->C->...->Z (26 deep)
	letters := "abcdefghijklmnopqrstuvwxyz"
	nodes := make([]layered.ProjectNode, 26)
	for i := range 26 {
		var deps []string
		if i > 0 {
			deps = []string{string(letters[i-1])}
		}
		nodes[i] = layered.ProjectNode{
			ID:        string(letters[i]),
			DependsOn: deps,
		}
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)
	Equals(t, 26, len(g.AllNodes()))
}

// --- GetDependencies / GetDependents / GetNode for non-existent nodes ---

func TestGetDependencies_NonExistent(t *testing.T) {
	g, err := layered.NewDependencyGraph(nil)
	Ok(t, err)
	deps := g.GetDependencies("nonexistent")
	Assert(t, deps == nil, "deps of nonexistent node should be nil")
}

func TestGetDependents_NonExistent(t *testing.T) {
	g, err := layered.NewDependencyGraph(nil)
	Ok(t, err)
	deps := g.GetDependents("nonexistent")
	Assert(t, deps == nil, "dependents of nonexistent node should be nil")
}

func TestGetNode_NonExistent(t *testing.T) {
	g, err := layered.NewDependencyGraph(nil)
	Ok(t, err)
	Assert(t, g.GetNode("nonexistent") == nil, "should return nil for nonexistent node")
}

// --- Layer Calculation Tests ---

func TestCalculateInitialLayers_AllIndependentAllChanged(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", HasFileChanges: true},
		{ID: "c", HasFileChanges: true},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, 0, plan.Layers[0].Index)
	Equals(t, []string{"a", "b", "c"}, plan.Layers[0].Projects)
	Equals(t, 0, len(plan.OutOfScope))
	Equals(t, 3, plan.TotalProjectCount)
}

func TestCalculateInitialLayers_SimpleChainAllChanged(t *testing.T) {
	// C depends on B, B depends on A
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}, HasFileChanges: true},
		{ID: "c", DependsOn: []string{"b"}, HasFileChanges: true},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 3, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)
	Equals(t, []string{"b"}, plan.Layers[1].Projects)
	Equals(t, []string{"c"}, plan.Layers[2].Projects)
	Equals(t, 0, len(plan.OutOfScope))
}

func TestCalculateInitialLayers_SimpleChainRootChanged(t *testing.T) {
	// Only A changed; B,C should be out of scope
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)
	Equals(t, []string{"b", "c"}, plan.OutOfScope)
}

func TestCalculateInitialLayers_SimpleChainLeafChanged(t *testing.T) {
	// Only C changed; A,B out of scope; C goes to L0 because its deps are not in scope
	nodes := []layered.ProjectNode{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}, HasFileChanges: true},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"c"}, plan.Layers[0].Projects)
	Equals(t, []string{"a", "b"}, plan.OutOfScope)
}

func TestCalculateInitialLayers_DiamondAllChanged(t *testing.T) {
	// A -> B, A -> C, B -> D, C -> D, all changed
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}, HasFileChanges: true},
		{ID: "c", DependsOn: []string{"a"}, HasFileChanges: true},
		{ID: "d", DependsOn: []string{"b", "c"}, HasFileChanges: true},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 3, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)
	Equals(t, []string{"b", "c"}, plan.Layers[1].Projects)
	Equals(t, []string{"d"}, plan.Layers[2].Projects)
}

func TestCalculateInitialLayers_DiamondOnlyRoot(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "d", DependsOn: []string{"b", "c"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)
	Equals(t, []string{"b", "c", "d"}, plan.OutOfScope)
}

func TestCalculateInitialLayers_PartialOverlap(t *testing.T) {
	// A->B, C->D; only A and C changed
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", HasFileChanges: true},
		{ID: "d", DependsOn: []string{"c"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"a", "c"}, plan.Layers[0].Projects)
	Equals(t, []string{"b", "d"}, plan.OutOfScope)
}

func TestCalculateInitialLayers_BothChangedWithDep(t *testing.T) {
	// B depends on A, both changed. A should be L0, B should be L1.
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}, HasFileChanges: true},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)
	Equals(t, []string{"b"}, plan.Layers[1].Projects)
}

func TestCalculateInitialLayers_NoChanges(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 0, len(plan.Layers))
	Equals(t, []string{"a", "b", "c"}, plan.OutOfScope)
	Equals(t, 3, plan.TotalProjectCount)
}

func TestCalculateInitialLayers_IndependentAndChained(t *testing.T) {
	// A (independent), B->C (chained), D (independent), all changed
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"c"}, HasFileChanges: true},
		{ID: "c", HasFileChanges: true},
		{ID: "d", HasFileChanges: true},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"a", "c", "d"}, plan.Layers[0].Projects)
	Equals(t, []string{"b"}, plan.Layers[1].Projects)
}

// --- Dynamic Layer Expansion Tests ---

func TestExpandLayer_CascadeFromRoot(t *testing.T) {
	// A->B->C, only A changed initially
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)

	// A planned with changes
	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusHasChanges,
	}
	plan = g.ExpandLayer(plan, 0, statuses)

	// B should be added to L1
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"b"}, plan.Layers[1].Projects)
	// C should still be out of scope (only direct dependents added)
	Equals(t, []string{"c"}, plan.OutOfScope)
}

func TestExpandLayer_NoCascadeOnNoChanges(t *testing.T) {
	// A->B, A changed initially but planned with no changes
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusNoChanges,
	}
	plan = g.ExpandLayer(plan, 0, statuses)

	// B should NOT be added
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"b"}, plan.OutOfScope)
}

func TestExpandLayer_MultiLevelCascade(t *testing.T) {
	// A->B->C, A changed, plan A -> has changes -> B added
	// then plan B -> has changes -> C added
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()

	// Expand after planning layer 0 (A has changes)
	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusHasChanges,
	}
	plan = g.ExpandLayer(plan, 0, statuses)
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"b"}, plan.Layers[1].Projects)

	// Expand after planning layer 1 (B has changes)
	statuses = map[string]layered.ChangeStatus{
		"b": layered.ChangeStatusHasChanges,
	}
	plan = g.ExpandLayer(plan, 1, statuses)
	Equals(t, 3, len(plan.Layers))
	Equals(t, []string{"c"}, plan.Layers[2].Projects)
	Equals(t, 0, len(plan.OutOfScope))
}

func TestExpandLayer_FanOutCascade(t *testing.T) {
	// A->B, A->C, A->D; only A changed
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "d", DependsOn: []string{"a"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusHasChanges,
	}
	plan = g.ExpandLayer(plan, 0, statuses)

	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"b", "c", "d"}, plan.Layers[1].Projects)
	Equals(t, 0, len(plan.OutOfScope))
}

func TestExpandLayer_PartialCascade_AlreadyInScope(t *testing.T) {
	// A->B, A->C. Both A and B have file changes (B already in scope at L1).
	// After A has changes, C should be added but B stays where it is.
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}, HasFileChanges: true},
		{ID: "c", DependsOn: []string{"a"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	// B is already at L1 due to file changes
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"a"}, plan.Layers[0].Projects)
	Equals(t, []string{"b"}, plan.Layers[1].Projects)

	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusHasChanges,
	}
	plan = g.ExpandLayer(plan, 0, statuses)

	// C should be added to L1, B stays at L1
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"b", "c"}, plan.Layers[1].Projects)
	Equals(t, 0, len(plan.OutOfScope))
}

func TestExpandLayer_ErrorStopsCascade(t *testing.T) {
	// A->B, A changed and errored
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusError,
	}
	plan = g.ExpandLayer(plan, 0, statuses)

	// B should NOT be added (error is not HasChanges)
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"b"}, plan.OutOfScope)
}

func TestExpandLayer_MixedExpandAndExisting(t *testing.T) {
	// A->B, C->D. A and C changed. A has changes, C no changes.
	nodes := []layered.ProjectNode{
		{ID: "a", HasFileChanges: true},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", HasFileChanges: true},
		{ID: "d", DependsOn: []string{"c"}},
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 1, len(plan.Layers))
	Equals(t, []string{"a", "c"}, plan.Layers[0].Projects)

	statuses := map[string]layered.ChangeStatus{
		"a": layered.ChangeStatusHasChanges,
		"c": layered.ChangeStatusNoChanges,
	}
	plan = g.ExpandLayer(plan, 0, statuses)

	// B should be added (A had changes), D should NOT (C had no changes)
	Equals(t, 2, len(plan.Layers))
	Equals(t, []string{"b"}, plan.Layers[1].Projects)
	Equals(t, []string{"d"}, plan.OutOfScope)
}

// --- Edge Cases ---

func TestDeterministicOutput(t *testing.T) {
	// Run the same calculation multiple times and verify same output
	nodes := []layered.ProjectNode{
		{ID: "z", HasFileChanges: true},
		{ID: "m", HasFileChanges: true},
		{ID: "a", HasFileChanges: true},
		{ID: "f", DependsOn: []string{"z", "m"}, HasFileChanges: true},
	}

	for range 10 {
		g, err := layered.NewDependencyGraph(nodes)
		Ok(t, err)
		plan := g.CalculateInitialLayers()
		Equals(t, 2, len(plan.Layers))
		Equals(t, []string{"a", "m", "z"}, plan.Layers[0].Projects)
		Equals(t, []string{"f"}, plan.Layers[1].Projects)
	}
}

func TestLargeGraphPerformance(t *testing.T) {
	// 500 projects in a long chain
	nodes := make([]layered.ProjectNode, 500)
	for i := range 500 {
		var deps []string
		if i > 0 {
			deps = []string{fmt.Sprintf("project-%04d", i-1)}
		}
		nodes[i] = layered.ProjectNode{
			ID:             fmt.Sprintf("project-%04d", i),
			DependsOn:      deps,
			HasFileChanges: true,
		}
	}
	g, err := layered.NewDependencyGraph(nodes)
	Ok(t, err)

	plan := g.CalculateInitialLayers()
	Equals(t, 500, len(plan.Layers))
	Equals(t, 500, plan.TotalProjectCount)
}

// --- CycleError formatting ---

func TestCycleError_Format(t *testing.T) {
	err := &layered.CycleError{Cycle: []string{"a", "b", "c"}}
	Equals(t, "circular dependency detected: a -> b -> c -> a", err.Error())
}

func TestUndefinedDependencyError_Format(t *testing.T) {
	err := &layered.UndefinedDependencyError{ProjectID: "foo", DependencyID: "bar"}
	Equals(t, `project "foo" depends on "bar", but no project named "bar" exists`, err.Error())
}

// --- JSON Marshaling Tests ---

func TestDependencyGraph_JSONRoundTrip(t *testing.T) {
	nodes := []layered.ProjectNode{
		{ID: "a", DependsOn: nil, HasFileChanges: true},
		{ID: "b", DependsOn: []layered.ProjectID{"a"}, HasFileChanges: true},
		{ID: "c", DependsOn: []layered.ProjectID{"b"}, HasFileChanges: false},
	}
	original, err := layered.NewDependencyGraph(nodes)
	require.NoError(t, err)

	// Marshal
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal
	var restored layered.DependencyGraph
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	// Verify structure preserved
	assert.True(t, restored.HasDependencies())
	assert.Equal(t, []layered.ProjectID{"a"}, restored.GetDependencies("b"))
	assert.Equal(t, []layered.ProjectID{"b"}, restored.GetDependents("a"))
	assert.Equal(t, []layered.ProjectID{"b"}, restored.GetDependencies("c"))

	// Verify node properties preserved
	nodeA := restored.GetNode("a")
	require.NotNil(t, nodeA)
	assert.True(t, nodeA.HasFileChanges)

	nodeC := restored.GetNode("c")
	require.NotNil(t, nodeC)
	assert.False(t, nodeC.HasFileChanges)
}
