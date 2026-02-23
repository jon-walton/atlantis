# Layered Planning: Dependency Graph & Layer Calculator

## Workstream Overview

This plan covers the core algorithm and data structures for building a dependency DAG from `depends_on` relationships, topologically sorting it into layers, detecting circular dependencies, and dynamically discovering which downstream projects should be included after each layer is planned.

This is a pure computation module with no side effects -- it takes project configs and changed-file lists as input and returns layer assignments as output. Other workstreams (the plan/apply lifecycle orchestrator, the dashboard comment renderer, the state persistence layer) will consume this module.

---

## 1. Files to Create

### `server/events/layered/graph.go` -- DAG builder and layer calculator

This is the primary file. It contains the `DependencyGraph` struct, the DAG construction logic, the topological sort, the circular dependency detector, and the layer calculator.

### `server/events/layered/graph_test.go` -- Comprehensive test suite

### `server/events/layered/doc.go` -- Package doc comment

---

## 2. Files to Modify

### `server/core/config/raw/project.go` -- Validate `depends_on` entries

Currently the `DependsOn` validator is a no-op (line 95-97). It should be enhanced to:
- Validate that entries are non-empty strings
- Validate that entries look like valid project names (use the same `validProjectName` function already in that file)
- Disallow self-references (a project cannot depend on itself) -- this requires cross-field validation with the project name

### `server/core/config/parser_validator.go` -- Cross-project dependency validation

After all projects are parsed and names validated (in `parseRawRepoCfg`, around line 123), add a call to validate dependency references. This should:
- Check that every `depends_on` entry references an existing project name in the same config
- Report clear errors for undefined references: `project "foo" depends on "bar", but no project named "bar" exists`

No changes needed to `valid/repo_cfg.go` or `valid/global_cfg.go` -- the `DependsOn` field is already plumbed through correctly.

---

## 3. Data Structures

```go
package layered

import (
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

// DependencyGraph represents the full DAG of project dependencies.
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
```

---

## 4. Public API

```go
// NewDependencyGraph constructs a DependencyGraph from a list of project
// nodes. It validates that all dependency references exist and that there
// are no cycles. Returns an error (CycleError or UndefinedDependencyError)
// if validation fails.
func NewDependencyGraph(nodes []ProjectNode) (*DependencyGraph, error)

// HasDependencies returns true if any project in the graph has dependencies.
// This is used to short-circuit: if no project has depends_on, layered
// planning is not needed and the caller should use the standard plan path.
func (g *DependencyGraph) HasDependencies() bool

// CalculateInitialLayers computes the initial layer assignment for all
// in-scope projects. A project is initially in scope if HasFileChanges
// is true. Layer assignment respects dependency ordering: if B depends on
// A and both have file changes, A gets a lower layer number than B.
//
// Projects without file changes are excluded from the initial plan. They
// may be added later via ExpandLayer when their upstream dependencies
// are found to have actual infrastructure changes.
func (g *DependencyGraph) CalculateInitialLayers() *LayerPlan

// ExpandLayer takes the results of planning a layer and determines which
// downstream projects should be added to subsequent layers. It returns a
// new LayerPlan that includes any newly-in-scope projects.
//
// The expansion rules are:
// 1. For each project in the just-planned layer that has ChangeStatusHasChanges,
//    look at its direct dependents.
// 2. If a dependent is not already in scope and not already planned, add it
//    to the next layer.
// 3. If a dependent IS already in scope (because it had file changes), it
//    stays in whatever layer it was already assigned to.
//
// plannedLayerIndex is the 0-based index of the layer that was just planned.
// projectStatuses maps project IDs to their ChangeStatus after planning.
func (g *DependencyGraph) ExpandLayer(
    currentPlan *LayerPlan,
    plannedLayerIndex int,
    projectStatuses map[ProjectID]ChangeStatus,
) *LayerPlan

// GetDependencies returns the direct dependencies of a project.
func (g *DependencyGraph) GetDependencies(id ProjectID) []ProjectID

// GetDependents returns the projects that directly depend on the given project.
func (g *DependencyGraph) GetDependents(id ProjectID) []ProjectID

// GetNode returns the ProjectNode for the given ID, or nil if not found.
func (g *DependencyGraph) GetNode(id ProjectID) *ProjectNode

// AllNodes returns all project nodes in the graph.
func (g *DependencyGraph) AllNodes() []*ProjectNode
```

---

## 5. Algorithm: Graph Construction and Validation

### 5.1 Build the Graph

```
function NewDependencyGraph(nodes):
    g = new DependencyGraph
    g.nodes = empty map
    g.dependents = empty map
    g.dependencies = empty map

    // Phase 1: Register all nodes
    for each node in nodes:
        if node.ID already in g.nodes:
            return error("duplicate project ID")
        g.nodes[node.ID] = node
        g.dependents[node.ID] = empty set
        g.dependencies[node.ID] = empty set

    // Phase 2: Build edges and validate references
    for each node in nodes:
        for each dep in node.DependsOn:
            if dep not in g.nodes:
                return UndefinedDependencyError{node.ID, dep}
            if dep == node.ID:
                return CycleError{[node.ID]}
            g.dependencies[node.ID].add(dep)
            g.dependents[dep].add(node.ID)

    // Phase 3: Detect cycles (Kahn's algorithm)
    error = g.detectCycle()
    if error != nil:
        return error

    return g
```

### 5.2 Cycle Detection (Kahn's Algorithm)

Kahn's algorithm performs a topological sort by repeatedly removing nodes with no incoming edges. If any nodes remain after the algorithm finishes, they form one or more cycles.

```
function detectCycle():
    // Calculate in-degree for each node
    inDegree = map of nodeID -> count of dependencies
    for each node in g.nodes:
        inDegree[node.ID] = len(g.dependencies[node.ID])

    // Seed queue with nodes that have no dependencies
    queue = []
    for each id, degree in inDegree:
        if degree == 0:
            queue.append(id)

    processed = 0
    while queue is not empty:
        current = queue.dequeue()
        processed++
        for each dependent in g.dependents[current]:
            inDegree[dependent]--
            if inDegree[dependent] == 0:
                queue.append(dependent)

    if processed < len(g.nodes):
        // There's a cycle. Find it using DFS for a clear error message.
        return findCycleDFS()

    return nil
```

### 5.3 Find Cycle for Error Reporting (DFS)

When Kahn's algorithm detects a cycle exists, we use DFS to find one specific cycle to report in the error message.

```
function findCycleDFS():
    // Only search among nodes that weren't processed by Kahn's
    // (they are the ones participating in cycles)
    white = set of all unprocessed nodes
    gray = empty set
    path = []

    function dfs(node):
        gray.add(node)
        white.remove(node)
        path.append(node)

        for each dep in g.dependencies[node]:
            if dep in gray:
                // Found a cycle! Extract it from path
                cycleStart = index of dep in path
                return CycleError{path[cycleStart:]}
            if dep in white:
                result = dfs(dep)
                if result != nil:
                    return result

        gray.remove(node)
        path.removeLast()
        return nil

    for each node in white:
        result = dfs(node)
        if result != nil:
            return result

    // Should never reach here since Kahn's confirmed a cycle exists
    panic("cycle detection inconsistency")
```

---

## 6. Algorithm: Layer Calculation

### 6.1 Initial Layer Calculation

```
function CalculateInitialLayers():
    // Step 1: Determine initially in-scope projects
    inScope = set of node IDs where HasFileChanges == true

    // If no in-scope projects, return empty plan
    if inScope is empty:
        return LayerPlan{Layers: [], OutOfScope: allNodeIDs}

    // Step 2: Calculate layer depth for each in-scope project using
    // topological ordering. The layer of a project is determined by
    // the longest path from any root (no in-scope dependencies) to it.
    layerDepth = map of nodeID -> int

    function calculateDepth(nodeID):
        if nodeID in layerDepth:
            return layerDepth[nodeID]

        maxUpstreamDepth = -1
        for each dep in g.dependencies[nodeID]:
            if dep in inScope:
                depDepth = calculateDepth(dep)
                if depDepth > maxUpstreamDepth:
                    maxUpstreamDepth = depDepth

        if maxUpstreamDepth == -1:
            // No in-scope dependencies -> layer 0
            layerDepth[nodeID] = 0
        else:
            layerDepth[nodeID] = maxUpstreamDepth + 1

        return layerDepth[nodeID]

    for each nodeID in inScope:
        calculateDepth(nodeID)

    // Step 3: Group projects by layer depth
    layerGroups = map of int -> []ProjectID
    maxLayer = 0
    for each nodeID, depth in layerDepth:
        layerGroups[depth] = append(layerGroups[depth], nodeID)
        if depth > maxLayer:
            maxLayer = depth

    // Step 4: Build ordered Layer slice
    layers = []
    for i from 0 to maxLayer:
        projects = layerGroups[i]
        sort(projects)  // alphabetical for determinism
        layers = append(layers, Layer{Index: i, Projects: projects})

    // Step 5: Collect out-of-scope projects
    outOfScope = []
    for each nodeID in g.nodes:
        if nodeID not in inScope:
            outOfScope = append(outOfScope, nodeID)
    sort(outOfScope)

    return LayerPlan{
        Layers:            layers,
        OutOfScope:        outOfScope,
        TotalProjectCount: len(g.nodes),
    }
```

### 6.2 Dynamic Layer Expansion (After Planning)

This is called after each layer is planned. It examines the plan results and determines if any previously-out-of-scope projects should now be included because an upstream dependency had actual infrastructure changes.

```
function ExpandLayer(currentPlan, plannedLayerIndex, projectStatuses):
    // Step 1: Update change status on nodes
    for each projectID, status in projectStatuses:
        g.nodes[projectID].ChangeStatus = status

    // Step 2: Identify newly-in-scope projects
    newlyInScope = empty set
    currentInScope = set of all project IDs in currentPlan.Layers

    for each projectID, status in projectStatuses:
        if status != ChangeStatusHasChanges:
            continue

        // Look at all direct dependents of this project
        for each dependent in g.dependents[projectID]:
            if dependent in currentInScope:
                // Already in scope, no action needed
                continue
            if dependent in newlyInScope:
                continue
            newlyInScope.add(dependent)

    // Step 3: If no new projects, return current plan unchanged
    if newlyInScope is empty:
        return currentPlan

    // Step 4: Calculate layers for newly-in-scope projects
    // They go into the next layer after their highest-layered dependency
    // that is already in scope.
    newLayerAssignments = map of nodeID -> int

    function assignLayer(nodeID):
        if nodeID in newLayerAssignments:
            return newLayerAssignments[nodeID]

        maxUpstream = plannedLayerIndex  // at minimum, after the just-planned layer
        for each dep in g.dependencies[nodeID]:
            if dep in currentInScope:
                depLayer = findLayerOf(dep, currentPlan)
                if depLayer > maxUpstream:
                    maxUpstream = depLayer
            if dep in newlyInScope:
                depLayer = assignLayer(dep)
                if depLayer > maxUpstream:
                    maxUpstream = depLayer

        newLayerAssignments[nodeID] = maxUpstream + 1
        return newLayerAssignments[nodeID]

    for each nodeID in newlyInScope:
        assignLayer(nodeID)

    // Step 5: Merge new assignments into existing plan
    // Copy existing layers
    newLayers = copy(currentPlan.Layers)

    // Add new projects to appropriate layers (extending if needed)
    for each nodeID, layerIdx in newLayerAssignments:
        // Ensure the layers slice is large enough
        while len(newLayers) <= layerIdx:
            newLayers = append(newLayers, Layer{Index: len(newLayers)})
        newLayers[layerIdx].Projects = append(newLayers[layerIdx].Projects, nodeID)

    // Re-sort projects within each modified layer
    for each layer in newLayers:
        sort(layer.Projects)

    // Update out-of-scope
    newOutOfScope = []
    allInScope = set of all project IDs across newLayers
    for each nodeID in g.nodes:
        if nodeID not in allInScope:
            newOutOfScope = append(newOutOfScope, nodeID)
    sort(newOutOfScope)

    return LayerPlan{
        Layers:            newLayers,
        OutOfScope:        newOutOfScope,
        TotalProjectCount: currentPlan.TotalProjectCount,
    }
```

---

## 7. Integration with Existing Config Parsing

### 7.1 Where to validate `depends_on` references

In `server/core/config/parser_validator.go`, function `parseRawRepoCfg`, after the call to `p.validateProjectNames(validConfig)` at line 123:

```go
// Validate depends_on references point to existing projects.
if err := p.validateProjectDependencies(validConfig); err != nil {
    return valid.RepoCfg{}, err
}
```

New method on `ParserValidator`:

```go
func (p *ParserValidator) validateProjectDependencies(config valid.RepoCfg) error {
    // Build a set of all named projects.
    namedProjects := make(map[string]bool)
    for _, project := range config.Projects {
        if project.Name != nil {
            namedProjects[*project.Name] = true
        }
    }

    for _, project := range config.Projects {
        for _, dep := range project.DependsOn {
            if !namedProjects[dep] {
                projName := "<unnamed>"
                if project.Name != nil {
                    projName = *project.Name
                }
                return fmt.Errorf(
                    "project %q depends_on %q, but no project named %q exists",
                    projName, dep, dep,
                )
            }
        }
        // A project using depends_on must itself be named so others can reference it.
        if len(project.DependsOn) > 0 && project.Name == nil {
            return fmt.Errorf(
                "project at dir %q uses depends_on but has no name; "+
                    "projects using depends_on must have a name",
                project.Dir,
            )
        }
    }
    return nil
}
```

### 7.2 Where to build the graph

The `DependencyGraph` should be constructed in the plan command orchestration layer (future workstream), not during config parsing. The config parser's job is just to validate that references are valid. The graph is built from `valid.Project` structs after the project finder determines which projects are in scope.

Helper function to bridge config to graph nodes:

```go
// ProjectsToNodes converts valid.Project configs and a set of changed
// project names into ProjectNode entries suitable for graph construction.
// It includes ALL projects that have depends_on relationships (or are
// depended upon), not just the ones with file changes.
func ProjectsToNodes(
    allProjects []valid.Project,
    changedProjectNames map[string]bool,
) []ProjectNode {
    // First pass: find all projects that participate in any dependency
    // relationship (either as a dependent or a dependency).
    participants := make(map[string]bool)
    for _, p := range allProjects {
        if p.Name == nil {
            continue
        }
        if len(p.DependsOn) > 0 {
            participants[*p.Name] = true
            for _, dep := range p.DependsOn {
                participants[dep] = true
            }
        }
    }

    // Second pass: build nodes for all participants.
    var nodes []ProjectNode
    for _, p := range allProjects {
        if p.Name == nil || !participants[*p.Name] {
            continue
        }
        nodes = append(nodes, ProjectNode{
            ID:             *p.Name,
            DependsOn:      p.DependsOn,
            HasFileChanges: changedProjectNames[*p.Name],
        })
    }
    return nodes
}
```

### 7.3 How the orchestrator will use this (reference for other workstreams)

```go
// In the plan command runner (future workstream):
nodes := layered.ProjectsToNodes(allProjects, changedProjectNames)
graph, err := layered.NewDependencyGraph(nodes)
if err != nil {
    // Post error to PR (CycleError or UndefinedDependencyError)
    return
}

if !graph.HasDependencies() {
    // No dependencies -- run standard plan path
    return
}

plan := graph.CalculateInitialLayers()

// Plan layer 0
layer0Results := planProjects(plan.Layers[0].Projects)

// Expand based on results
statuses := extractStatuses(layer0Results)
plan = graph.ExpandLayer(plan, 0, statuses)

// Continue with layer 1, etc.
```

---

## 8. Enhancing `depends_on` Validation in `raw/project.go`

Replace the no-op validator at line 95 of `server/core/config/raw/project.go`:

```go
DependsOn := func(value any) error {
    deps := value.([]string)
    for _, dep := range deps {
        if dep == "" {
            return errors.New("depends_on entries cannot be empty strings")
        }
        if !validProjectName(dep) {
            return fmt.Errorf("depends_on entry %q is not a valid project name", dep)
        }
    }
    // Self-reference check (requires name to be set)
    if p.Name != nil {
        for _, dep := range deps {
            if dep == *p.Name {
                return fmt.Errorf("project %q cannot depend on itself", *p.Name)
            }
        }
    }
    return nil
}
```

---

## 9. Testing Approach

**This workstream uses TDD.** Write tests first, then implement to pass. The dependency graph and layer calculator are pure logic with well-defined inputs/outputs — ideal for test-driven development.

- **Framework:** Standard Go testing with custom assertions from `testing/` package (`Ok`, `Equals`, `ErrContains`)
- **Mocks:** None needed — this is a pure computation module with no external dependencies
- **Test file:** `server/events/layered/graph_test.go`
- **Table-driven tests** for all test cases below

## 10. Test Cases

### 10.1 Graph Construction

| Test | Input | Expected |
|---|---|---|
| Empty graph | No nodes | Empty graph, `HasDependencies()` returns false |
| Single node, no deps | `[{A, deps:[]}]` | Valid graph, `HasDependencies()` returns false |
| Simple chain A->B | `[{A, deps:[]}, {B, deps:[A]}]` | Valid graph, A has dependent B, B has dependency A |
| Diamond A->B,C->D | `[A, B->A, C->A, D->B,C]` | Valid graph with 4 nodes |
| Undefined dependency | `[{A, deps:[X]}]` | `UndefinedDependencyError{A, X}` |
| Self-reference | `[{A, deps:[A]}]` | `CycleError{[A]}` |
| Direct cycle A<->B | `[{A, deps:[B]}, {B, deps:[A]}]` | `CycleError{[A, B]}` or `{[B, A]}` |
| Transitive cycle A->B->C->A | 3-node cycle | `CycleError` with all 3 nodes |
| Mixed: cycle + valid | `[A, B->A, C->D->C]` | `CycleError` for C/D |
| Large fan-out | A with 100 dependents | Valid graph |
| Large fan-in | 100 projects all depending on A | Valid graph |
| Long chain | A->B->C->...->Z (26 deep) | Valid graph |

### 9.2 Layer Calculation

| Test | Graph | Changed | Expected Layers |
|---|---|---|---|
| All independent, all changed | A,B,C (no deps) | A,B,C | Layer 0: [A,B,C] |
| Simple chain, all changed | A->B->C | A,B,C | L0:[A], L1:[B], L2:[C] |
| Simple chain, root changed | A->B->C | A | L0:[A] (B,C out of scope) |
| Simple chain, leaf changed | A->B->C | C | L0:[C] (A,B out of scope -- C goes to L0 because its deps are not in scope) |
| Diamond, all changed | A, B->A, C->A, D->B,C | All | L0:[A], L1:[B,C], L2:[D] |
| Diamond, only root | A, B->A, C->A, D->B,C | A | L0:[A] |
| Partial overlap | A->B, C->D | A,C | L0:[A,C] |
| Both A and B changed, B->A | A, B->A | A,B | L0:[A], L1:[B] |
| No changes at all | A->B->C | none | Empty plan |
| Independent + chained | A, B->C, D | A,B,C,D | L0:[A,C,D], L1:[B] |

### 9.3 Dynamic Layer Expansion

| Test | Initial State | Plan Result | Expected Expansion |
|---|---|---|---|
| Cascade from root | A->B->C, A changed, L0:[A] | A has changes | B added to L1 |
| No cascade on no-changes | A->B, A changed, L0:[A] | A no changes | B not added |
| Multi-level cascade | A->B->C, A changed | A has changes, then B has changes | B in L1, then C in L2 |
| Fan-out cascade | A->B, A->C, A->D, A changed | A has changes | B,C,D all added to L1 |
| Partial cascade | A->B, A->C, B already has changes at L1 | A has changes | C added to L1, B stays where it is |
| Error stops cascade | A->B, A changed | A errored | B not added (error is not HasChanges) |
| Mixed: expand + existing | A->B, C->D, A,C changed | A changes, C no changes | B added, D not added |

### 9.4 Edge Cases

| Test | Scenario | Expected |
|---|---|---|
| Project with deps but no name | Config has depends_on but no name | Validation error in parser_validator |
| Dep references unnamed project | Project "A" depends_on "B", B exists but has no name | Validation error |
| Duplicate project names | Two projects named "A" | Existing validation catches this |
| Glob-expanded projects with deps | Glob dir `envs/*` with depends_on | deps_on is copied to each expanded project |
| Large graph performance | 500 projects, deep chain | Should complete in <100ms |
| Deterministic output | Same input, multiple runs | Same layer assignment, same ordering |

### 9.5 Config Validation Tests

Add tests to `server/core/config/raw/project_test.go`:
- `depends_on: [""]` should fail validation
- `depends_on: ["valid-name"]` should pass validation
- `depends_on: ["name with spaces"]` should fail validation
- Self-reference should fail when name is set

Add tests to `server/core/config/parser_validator_test.go`:
- Project references non-existent dependency -> error
- Project with depends_on but no name -> error
- Valid dependency chain -> no error

---

## 10. Implementation Details

### 10.1 Deterministic Ordering

All output slices (`Layer.Projects`, `LayerPlan.OutOfScope`) must be sorted alphabetically to ensure deterministic behavior. This matters for:
- Test stability
- Consistent PR comments
- Reproducible debugging

### 10.2 Thread Safety

The `DependencyGraph` is NOT thread-safe. It is designed to be used from a single goroutine (the plan orchestrator). If concurrent access is needed in the future, the caller should handle synchronization.

### 10.3 Performance Considerations

- Kahn's algorithm is O(V + E) where V is the number of projects and E is the number of dependency edges. For typical Atlantis configs (hundreds of projects, sparse dependencies), this is negligible.
- The `calculateDepth` function uses memoization to avoid recomputation. Each node is visited once, making it O(V + E).
- `ExpandLayer` is called once per layer and only examines dependents of projects that had changes. This is proportional to the fan-out of changed projects.

### 10.4 Memory

Each `DependencyGraph` holds three maps. For a repo with N projects and E dependency edges:
- `nodes`: N entries
- `dependents`: N entries, total values = E
- `dependencies`: N entries, total values = E

Total memory: O(N + E), which is trivial for any realistic config.

---

## 11. Risks and Open Questions

### 11.1 Unnamed Projects in `depends_on` Context

The current `depends_on` implementation uses project names as identifiers. Projects without names cannot participate in dependency graphs. This is already the case in the existing `ValidateProjectDependencies` in `command_requirement_handler.go` (line 30: `project.ProjectName == dependOnProject`). The validation added in this workstream makes this explicit.

**Risk:** Users may have unnamed projects with `depends_on` today. The new validation will reject configs that previously "worked" (silently doing nothing useful). This is acceptable -- it surfaces a real misconfiguration.

### 11.2 Glob-Expanded Projects with `depends_on`

When a project uses glob patterns in `dir` (e.g., `envs/*`), it gets expanded into multiple projects. If the original project had `depends_on`, each expanded project gets the same dependencies. However, expanded projects do NOT get names (see `copyProjectWithDir` in `parser_validator.go` line 352-353), which means they cannot participate in dependency graphs.

**Decision needed:** Should glob-expanded projects be allowed to have `depends_on`? If so, we'd need to auto-generate names (e.g., based on the expanded dir path). This is out of scope for this workstream but should be tracked.

### 11.3 `depends_on` vs `execution_order_group`

The existing `execution_order_group` field provides a coarser ordering mechanism (all projects in group N run before group N+1). Layered planning is strictly more expressive. The design doc notes that replacing `execution_order_group` with auto-calculated layers is future work.

**No conflict:** Both mechanisms can coexist. `execution_order_group` controls ordering WITHIN a layer (or when layered planning is disabled). Layered planning controls which layer a project is in.

### 11.4 When `depends_on` References Are Checked

Currently, `depends_on` validation is split:
- At config parse time: references are validated (this workstream adds this)
- At apply time: `ValidateProjectDependencies` in `command_requirement_handler.go` checks that dependencies are applied before allowing an apply

This workstream does NOT change the apply-time validation. That remains as a safety net even when layered planning is enabled.

### 11.5 Projects Both Changed and Downstream

If project B depends on A, and BOTH have file changes, B should be in a later layer than A even though B has its own file changes. The algorithm handles this correctly: B is in scope due to file changes, but its layer number is determined by the dependency graph (it must come after A).

After A is applied, B is re-planned in its layer, so B's plan will reflect both its own file changes AND A's applied state. This is the correct behavior.