// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package layered

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ProjectID is the unique identifier for a project. It corresponds to the
// project's `name` field in atlantis.yaml. Projects without names cannot
// participate in dependency graphs.
type ProjectID = string

// ChangeStatus describes what a project's plan revealed.
type ChangeStatus int

const (
	// ChangeStatusUnknown means the project hasn't been planned yet.
	ChangeStatusUnknown ChangeStatus = iota
	// ChangeStatusHasChanges means the plan showed infrastructure changes.
	ChangeStatusHasChanges
	// ChangeStatusNoChanges means the plan completed with no changes.
	ChangeStatusNoChanges
	// ChangeStatusError means the plan failed.
	ChangeStatusError
)

// ProjectNode represents a single project in the dependency graph.
type ProjectNode struct {
	// ID is the project name (required for dependency participation).
	ID ProjectID
	// DependsOn lists the project names this project depends on.
	DependsOn []ProjectID
	// HasFileChanges is true if this project has git file changes in the PR.
	HasFileChanges bool
	// ChangeStatus tracks the plan result (set after planning).
	ChangeStatus ChangeStatus
}

// DependencyGraph is the single source of truth for all graph operations:
//   - Construction and validation via NewDependencyGraph()
//   - Initial layer calculation via CalculateInitialLayers()
//   - Dynamic layer expansion via ExpandLayer()
//   - Dependency/dependent lookups via GetDependencies()/GetDependents()
type DependencyGraph struct {
	// nodes maps project ID to its node.
	nodes map[ProjectID]*ProjectNode
	// dependents maps a project ID to the set of projects that depend on it
	// (reverse edges). This is the "who depends on me?" lookup.
	dependents map[ProjectID]map[ProjectID]bool
	// dependencies maps a project ID to the set of projects it depends on
	// (forward edges). This is the "who do I depend on?" lookup.
	dependencies map[ProjectID]map[ProjectID]bool
}

// graphJSON is the JSON-serializable representation of DependencyGraph.
type graphJSON struct {
	Nodes []ProjectNode `json:"nodes"`
}

// MarshalJSON implements json.Marshaler for DependencyGraph.
func (g *DependencyGraph) MarshalJSON() ([]byte, error) {
	// Convert from pointers to values for JSON serialization
	nodes := make([]ProjectNode, len(g.nodes))
	allNodes := g.AllNodes()
	for i, n := range allNodes {
		nodes[i] = *n
	}
	return json.Marshal(graphJSON{Nodes: nodes})
}

// UnmarshalJSON implements json.Unmarshaler for DependencyGraph.
func (g *DependencyGraph) UnmarshalJSON(data []byte) error {
	var gj graphJSON
	if err := json.Unmarshal(data, &gj); err != nil {
		return err
	}
	rebuilt, err := NewDependencyGraph(gj.Nodes)
	if err != nil {
		return err
	}
	*g = *rebuilt
	return nil
}

// Layer represents a single execution layer.
type Layer struct {
	// Index is the 0-based layer number.
	Index int
	// Projects are the project IDs in this layer, sorted alphabetically
	// for deterministic output.
	Projects []ProjectID
}

// LayerPlan is the output of the layer calculator. It describes which
// projects are in which layers and which are not in scope.
type LayerPlan struct {
	// Layers is the ordered list of layers (layer 0 first).
	Layers []Layer
	// OutOfScope lists projects that exist in the graph but are not in scope
	// for this PR (no file changes and no upstream cascade triggered them).
	OutOfScope []ProjectID
	// TotalProjectCount is the total number of projects in the graph.
	TotalProjectCount int
}

// CycleError is returned when the dependency graph contains a cycle.
type CycleError struct {
	// Cycle contains the project IDs forming the cycle, in order.
	// The last element depends on the first, closing the cycle.
	Cycle []ProjectID
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("circular dependency detected: %s",
		strings.Join(append(e.Cycle, e.Cycle[0]), " -> "))
}

// UndefinedDependencyError is returned when a project references a
// dependency that doesn't exist.
type UndefinedDependencyError struct {
	ProjectID    ProjectID
	DependencyID ProjectID
}

func (e *UndefinedDependencyError) Error() string {
	return fmt.Sprintf("project %q depends on %q, but no project named %q exists",
		e.ProjectID, e.DependencyID, e.DependencyID)
}

// NewDependencyGraph constructs a DependencyGraph from a list of project
// nodes. It validates that all dependency references exist and that there
// are no cycles. Returns an error (CycleError or UndefinedDependencyError)
// if validation fails.
func NewDependencyGraph(nodes []ProjectNode) (*DependencyGraph, error) {
	g := &DependencyGraph{
		nodes:        make(map[ProjectID]*ProjectNode),
		dependents:   make(map[ProjectID]map[ProjectID]bool),
		dependencies: make(map[ProjectID]map[ProjectID]bool),
	}

	// Phase 1: Register all nodes.
	for i := range nodes {
		node := &nodes[i]
		if _, exists := g.nodes[node.ID]; exists {
			return nil, fmt.Errorf("duplicate project ID %q", node.ID)
		}
		g.nodes[node.ID] = node
		g.dependents[node.ID] = make(map[ProjectID]bool)
		g.dependencies[node.ID] = make(map[ProjectID]bool)
	}

	// Phase 2: Build edges and validate references.
	for i := range nodes {
		node := &nodes[i]
		for _, dep := range node.DependsOn {
			if _, exists := g.nodes[dep]; !exists {
				return nil, &UndefinedDependencyError{
					ProjectID:    node.ID,
					DependencyID: dep,
				}
			}
			if dep == node.ID {
				return nil, &CycleError{Cycle: []ProjectID{node.ID}}
			}
			g.dependencies[node.ID][dep] = true
			g.dependents[dep][node.ID] = true
		}
	}

	// Phase 3: Detect cycles (Kahn's algorithm).
	if err := g.detectCycle(); err != nil {
		return nil, err
	}

	return g, nil
}

// HasDependencies returns true if any project in the graph has dependencies.
func (g *DependencyGraph) HasDependencies() bool {
	for _, deps := range g.dependencies {
		if len(deps) > 0 {
			return true
		}
	}
	return false
}

// CalculateInitialLayers computes the initial layer assignment for all
// in-scope projects. A project is initially in scope if HasFileChanges
// is true. Layer assignment respects dependency ordering.
func (g *DependencyGraph) CalculateInitialLayers() *LayerPlan {
	// Step 1: Determine initially in-scope projects.
	inScope := make(map[ProjectID]bool)
	for id, node := range g.nodes {
		if node.HasFileChanges {
			inScope[id] = true
		}
	}

	// If no in-scope projects, return empty plan.
	if len(inScope) == 0 {
		outOfScope := g.sortedNodeIDs()
		return &LayerPlan{
			OutOfScope:        outOfScope,
			TotalProjectCount: len(g.nodes),
		}
	}

	// Step 2: Calculate layer depth for each in-scope project.
	layerDepth := make(map[ProjectID]int)

	var calculateDepth func(id ProjectID) int
	calculateDepth = func(id ProjectID) int {
		if depth, ok := layerDepth[id]; ok {
			return depth
		}

		maxUpstreamDepth := -1
		for dep := range g.dependencies[id] {
			if inScope[dep] {
				depDepth := calculateDepth(dep)
				if depDepth > maxUpstreamDepth {
					maxUpstreamDepth = depDepth
				}
			}
		}

		if maxUpstreamDepth == -1 {
			layerDepth[id] = 0
		} else {
			layerDepth[id] = maxUpstreamDepth + 1
		}
		return layerDepth[id]
	}

	for id := range inScope {
		calculateDepth(id)
	}

	// Step 3: Group projects by layer depth.
	layerGroups := make(map[int][]ProjectID)
	maxLayer := 0
	for id, depth := range layerDepth {
		layerGroups[depth] = append(layerGroups[depth], id)
		if depth > maxLayer {
			maxLayer = depth
		}
	}

	// Step 4: Build ordered Layer slice.
	layers := make([]Layer, 0, maxLayer+1)
	for i := 0; i <= maxLayer; i++ {
		projects := layerGroups[i]
		sort.Strings(projects)
		layers = append(layers, Layer{Index: i, Projects: projects})
	}

	// Step 5: Collect out-of-scope projects.
	var outOfScope []ProjectID
	for id := range g.nodes {
		if !inScope[id] {
			outOfScope = append(outOfScope, id)
		}
	}
	sort.Strings(outOfScope)

	return &LayerPlan{
		Layers:            layers,
		OutOfScope:        outOfScope,
		TotalProjectCount: len(g.nodes),
	}
}

// ExpandLayer takes the results of planning a layer and determines which
// downstream projects should be added to subsequent layers.
func (g *DependencyGraph) ExpandLayer(
	currentPlan *LayerPlan,
	plannedLayerIndex int,
	projectStatuses map[ProjectID]ChangeStatus,
) *LayerPlan {
	// Step 1: Update change status on nodes.
	for id, status := range projectStatuses {
		if node, ok := g.nodes[id]; ok {
			node.ChangeStatus = status
		}
	}

	// Step 2: Build set of all currently in-scope project IDs.
	currentInScope := make(map[ProjectID]bool)
	for _, layer := range currentPlan.Layers {
		for _, id := range layer.Projects {
			currentInScope[id] = true
		}
	}

	// Step 3: Identify newly-in-scope projects.
	newlyInScope := make(map[ProjectID]bool)
	for id, status := range projectStatuses {
		if status != ChangeStatusHasChanges {
			continue
		}
		for dependent := range g.dependents[id] {
			if currentInScope[dependent] {
				continue
			}
			if newlyInScope[dependent] {
				continue
			}
			newlyInScope[dependent] = true
		}
	}

	// Step 4: If no new projects, return current plan unchanged.
	if len(newlyInScope) == 0 {
		return currentPlan
	}

	// Step 4: Calculate layers for newly-in-scope projects.
	newLayerAssignments := make(map[ProjectID]int)

	var assignLayer func(id ProjectID) int
	assignLayer = func(id ProjectID) int {
		if layer, ok := newLayerAssignments[id]; ok {
			return layer
		}

		maxUpstream := plannedLayerIndex
		for dep := range g.dependencies[id] {
			if currentInScope[dep] {
				depLayer := findLayerOf(dep, currentPlan)
				if depLayer > maxUpstream {
					maxUpstream = depLayer
				}
			}
			if newlyInScope[dep] {
				depLayer := assignLayer(dep)
				if depLayer > maxUpstream {
					maxUpstream = depLayer
				}
			}
		}

		newLayerAssignments[id] = maxUpstream + 1
		return newLayerAssignments[id]
	}

	for id := range newlyInScope {
		assignLayer(id)
	}

	// Step 5: Merge new assignments into existing plan.
	// Deep copy existing layers.
	newLayers := make([]Layer, len(currentPlan.Layers))
	for i, layer := range currentPlan.Layers {
		projects := make([]ProjectID, len(layer.Projects))
		copy(projects, layer.Projects)
		newLayers[i] = Layer{Index: layer.Index, Projects: projects}
	}

	// Add new projects to appropriate layers (extending if needed).
	for id, layerIdx := range newLayerAssignments {
		for len(newLayers) <= layerIdx {
			newLayers = append(newLayers, Layer{Index: len(newLayers)})
		}
		newLayers[layerIdx].Projects = append(newLayers[layerIdx].Projects, id)
	}

	// Re-sort projects within each layer.
	for i := range newLayers {
		sort.Strings(newLayers[i].Projects)
	}

	// Update out-of-scope.
	allInScope := make(map[ProjectID]bool)
	for _, layer := range newLayers {
		for _, id := range layer.Projects {
			allInScope[id] = true
		}
	}
	var newOutOfScope []ProjectID
	for id := range g.nodes {
		if !allInScope[id] {
			newOutOfScope = append(newOutOfScope, id)
		}
	}
	sort.Strings(newOutOfScope)

	return &LayerPlan{
		Layers:            newLayers,
		OutOfScope:        newOutOfScope,
		TotalProjectCount: currentPlan.TotalProjectCount,
	}
}

// GetDependencies returns the direct dependencies of a project, sorted
// alphabetically.
func (g *DependencyGraph) GetDependencies(id ProjectID) []ProjectID {
	deps, ok := g.dependencies[id]
	if !ok || len(deps) == 0 {
		return nil
	}
	result := make([]ProjectID, 0, len(deps))
	for dep := range deps {
		result = append(result, dep)
	}
	sort.Strings(result)
	return result
}

// GetDependents returns the projects that directly depend on the given
// project, sorted alphabetically.
func (g *DependencyGraph) GetDependents(id ProjectID) []ProjectID {
	deps, ok := g.dependents[id]
	if !ok || len(deps) == 0 {
		return nil
	}
	result := make([]ProjectID, 0, len(deps))
	for dep := range deps {
		result = append(result, dep)
	}
	sort.Strings(result)
	return result
}

// GetNode returns the ProjectNode for the given ID, or nil if not found.
func (g *DependencyGraph) GetNode(id ProjectID) *ProjectNode {
	return g.nodes[id]
}

// AllNodes returns all project nodes in the graph, sorted by ID.
func (g *DependencyGraph) AllNodes() []*ProjectNode {
	result := make([]*ProjectNode, 0, len(g.nodes))
	for _, node := range g.nodes {
		result = append(result, node)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

// detectCycle uses Kahn's algorithm to detect cycles. If a cycle is found,
// it uses DFS to find a specific cycle path for the error message.
func (g *DependencyGraph) detectCycle() error {
	// Calculate in-degree for each node.
	inDegree := make(map[ProjectID]int)
	for id := range g.nodes {
		inDegree[id] = len(g.dependencies[id])
	}

	// Seed queue with nodes that have no dependencies.
	var queue []ProjectID
	for id, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	// Sort for determinism.
	sort.Strings(queue)

	processed := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		processed++

		// Collect and sort dependents for deterministic processing.
		var deps []ProjectID
		for dep := range g.dependents[current] {
			deps = append(deps, dep)
		}
		sort.Strings(deps)

		for _, dependent := range deps {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if processed < len(g.nodes) {
		return g.findCycleDFS(inDegree)
	}
	return nil
}

// findCycleDFS finds a specific cycle among the unprocessed nodes for
// error reporting.
func (g *DependencyGraph) findCycleDFS(inDegree map[ProjectID]int) error {
	// Only search among nodes that weren't processed by Kahn's.
	white := make(map[ProjectID]bool)
	for id, degree := range inDegree {
		if degree > 0 {
			white[id] = true
		}
	}
	gray := make(map[ProjectID]bool)
	var path []ProjectID

	var dfs func(node ProjectID) *CycleError
	dfs = func(node ProjectID) *CycleError {
		gray[node] = true
		delete(white, node)
		path = append(path, node)

		// Sort dependencies for deterministic cycle detection.
		deps := make([]ProjectID, 0, len(g.dependencies[node]))
		for dep := range g.dependencies[node] {
			deps = append(deps, dep)
		}
		sort.Strings(deps)

		for _, dep := range deps {
			if gray[dep] {
				// Found a cycle. Extract from path.
				cycleStart := -1
				for i, p := range path {
					if p == dep {
						cycleStart = i
						break
					}
				}
				cycle := make([]ProjectID, len(path[cycleStart:]))
				copy(cycle, path[cycleStart:])
				return &CycleError{Cycle: cycle}
			}
			if white[dep] {
				result := dfs(dep)
				if result != nil {
					return result
				}
			}
		}

		delete(gray, node)
		path = path[:len(path)-1]
		return nil
	}

	// Sort white nodes for deterministic iteration.
	sortedWhite := make([]ProjectID, 0, len(white))
	for id := range white {
		sortedWhite = append(sortedWhite, id)
	}
	sort.Strings(sortedWhite)

	for _, node := range sortedWhite {
		if white[node] {
			result := dfs(node)
			if result != nil {
				return result
			}
		}
	}

	// Should never reach here since Kahn's confirmed a cycle exists.
	panic("cycle detection inconsistency")
}

// sortedNodeIDs returns all node IDs sorted alphabetically.
func (g *DependencyGraph) sortedNodeIDs() []ProjectID {
	ids := make([]ProjectID, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// findLayerOf returns the layer index of the given project in the plan,
// or -1 if not found.
func findLayerOf(id ProjectID, plan *LayerPlan) int {
	for _, layer := range plan.Layers {
		for _, pid := range layer.Projects {
			if pid == id {
				return layer.Index
			}
		}
	}
	return -1
}
