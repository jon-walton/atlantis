# Layered Planning: Layer State Manager - Implementation Plan

## Overview

This plan covers the state machine that tracks layered planning progress for a pull request. The Layer State Manager is responsible for:

1. Tracking which layer is current
2. Tracking project statuses within each layer
3. Determining when a layer is complete and the next can begin
4. Handling cascade evaluation (do downstream dependents need planning?)
5. Supporting skip functionality for failed projects
6. Persisting all of this durably across server restarts

## Prerequisite Reading

- Design document: `docs/plans/2026-02-13-layered-planning-design.md`
- Existing pull status model: `server/events/models/models.go` (lines 551-627)
- BoltDB persistence: `server/core/boltdb/boltdb.go` (lines 400-602)
- Redis persistence: `server/core/redis/redis.go` (lines 282-481)
- Database interface: `server/core/db/db.go`
- Project result flow: `server/events/command/project_result.go`

## 1. Data Structures

### 1.1 New Status Enum Value

**File: `server/events/models/models.go`**

Add a new `ProjectPlanStatus` value for skipped projects:

```go
const (
    ErroredPlanStatus ProjectPlanStatus = iota
    PlannedPlanStatus
    PlannedNoChangesPlanStatus
    ErroredApplyStatus
    AppliedPlanStatus
    DiscardedPlanStatus
    ErroredPolicyCheckStatus
    PassedPolicyCheckStatus
    SkippedPlanStatus  // NEW: project was skipped via `atlantis apply -skip`
)
```

Update the `String()` method to handle `SkippedPlanStatus`:

```go
case SkippedPlanStatus:
    return "skipped"
```

### 1.2 Extend ProjectStatus with Layer Info

**File: `server/events/models/models.go`**

Add a `Layer` field to `ProjectStatus`:

```go
type ProjectStatus struct {
    Workspace    string
    RepoRelDir   string
    ProjectName  string
    PolicyStatus []PolicySetStatus
    Status       ProjectPlanStatus
    Layer        int  // NEW: which layer this project belongs to (-1 = unassigned/pending)
}
```

### 1.3 LayerState Struct

**File: `server/events/models/models.go`** (add after PullStatus)

```go
// LayerState tracks the layered planning state for a pull request.
// It is only populated when layered planning is active (i.e., at least
// one in-scope project has depends_on configured).
type LayerState struct {
    // Enabled is true when layered planning is active for this PR.
    Enabled bool `json:"enabled"`

    // CurrentLayer is the layer index currently being planned/applied.
    // Starts at 0. A value of -1 means all layers are complete.
    CurrentLayer int `json:"current_layer"`

    // TotalLayers is the total number of known layers. This may grow as
    // cascade evaluation discovers new in-scope projects in later layers.
    TotalLayers int `json:"total_layers"`

    // DependencyGraph maps project name -> list of project names it depends on.
    // This is the full graph for all in-scope projects, used for cascade evaluation.
    DependencyGraph map[string][]string `json:"dependency_graph"`

    // ProjectLayers maps project name -> layer index for all in-scope projects.
    // Projects not yet assigned a layer (pending cascade evaluation) are absent.
    ProjectLayers map[string]int `json:"project_layers"`

    // PendingProjects lists project names that are known dependents but have not
    // yet been assigned to a layer (waiting for upstream plan results to determine
    // if they need to cascade).
    PendingProjects []string `json:"pending_projects,omitempty"`

    // SkippedUpstreams maps project name -> true for projects that were skipped.
    // Their downstream dependents will be excluded from future layers.
    SkippedUpstreams map[string]bool `json:"skipped_upstreams,omitempty"`
}
```

### 1.4 Extend PullStatus

**File: `server/events/models/models.go`**

Add `LayerState` to `PullStatus`:

```go
type PullStatus struct {
    Projects   []ProjectStatus
    Pull       PullRequest
    LayerState *LayerState `json:"layer_state,omitempty"` // NEW: nil when layered planning is not active
}
```

This is the key design decision: LayerState is embedded directly in PullStatus rather than stored separately. Rationale:

- PullStatus is already serialized as JSON to BoltDB/Redis
- Adding a new field with `omitempty` is backward-compatible (old records without it unmarshal to nil)
- All status updates already go through `UpdatePullWithResults` / `UpdateProjectStatus` which read-modify-write PullStatus atomically
- No new DB buckets, keys, or migration needed
- Both BoltDB and Redis backends automatically pick this up via JSON marshaling

## 2. Layer State Manager Interface

**New file: `server/events/layer_state_manager.go`**

```go
package events

import (
    "github.com/runatlantis/atlantis/server/events/command"
    "github.com/runatlantis/atlantis/server/events/models"
)

// LayerStateManager manages the state machine for layered planning.
// It is the single source of truth for layer progression, cascade
// evaluation, and skip handling.
type LayerStateManager interface {
    // InitializeLayerState builds the initial LayerState from the dependency
    // graph and the set of in-scope projects (those with git file changes).
    // Returns nil if layered planning is not needed (no depends_on relationships).
    InitializeLayerState(
        projects []models.ProjectStatus,
        dependencyGraph map[string][]string,
        changedProjects map[string]bool,
    ) (*models.LayerState, error)

    // GetCurrentLayerProjects returns the project names in the current layer.
    GetCurrentLayerProjects(state *models.LayerState) []string

    // GetPendingCount returns the count of projects not yet in any processed layer.
    GetPendingCount(state *models.LayerState) int

    // IsLayerComplete returns true if all projects in the given layer have a
    // terminal status (applied, no-changes, or skipped). It also returns the
    // list of projects still waiting for action.
    IsLayerComplete(pullStatus *models.PullStatus, layer int) (complete bool, blocking []string)

    // CanAdvance returns true if the current layer is complete and there are
    // more layers to process.
    CanAdvance(pullStatus *models.PullStatus) bool

    // AdvanceLayer moves to the next layer, performing cascade evaluation:
    // - Check which projects in the completed layer had actual changes
    // - For each dependent in the next layer, check if its upstream had changes
    // - If upstream had no changes, the dependent is excluded (cascade stops)
    // - If upstream was skipped, the dependent is excluded (cascade stops)
    // Returns the updated LayerState and the list of project names to plan next.
    AdvanceLayer(pullStatus *models.PullStatus) (*models.LayerState, []string, error)

    // SkipProject marks a project as skipped. Returns an error if:
    // - The project is not in the current layer
    // - The project has not failed an apply
    // - Skip functionality is not enabled
    SkipProject(pullStatus *models.PullStatus, projectName string, skipEnabled bool) (*models.PullStatus, error)

    // HandleNewCommit determines the impact of new commits on layered state.
    // Returns a ResetAction indicating what needs to happen.
    HandleNewCommit(
        pullStatus *models.PullStatus,
        affectedProjects []string,
    ) ResetAction

    // IsAllComplete returns true if all layers have been processed
    // (current layer is -1 or past the last layer).
    IsAllComplete(pullStatus *models.PullStatus) bool

    // GetLayerSummary returns a summary of each layer's status for dashboard rendering.
    GetLayerSummary(pullStatus *models.PullStatus) []LayerSummary
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
```

## 3. Layer State Manager Implementation

**New file: `server/events/layer_state_manager.go`** (implementation below the interface)

```go
// DefaultLayerStateManager implements LayerStateManager.
type DefaultLayerStateManager struct{}

func NewLayerStateManager() *DefaultLayerStateManager {
    return &DefaultLayerStateManager{}
}
```

### 3.1 InitializeLayerState

This is called during autoplan or manual plan when layered planning is enabled. The caller (plan command runner) provides:

- The list of projects detected from file changes
- The dependency graph built from `depends_on` config

Algorithm:

1. Build a full dependency graph from all projects' `depends_on` fields.
2. Identify "in-scope" projects: those with git file changes.
3. Topologically sort in-scope projects into layers:
   - Layer 0: in-scope projects with no in-scope upstream dependencies.
   - Layer N+1: in-scope projects whose latest upstream dependency is in layer N.
4. Identify "pending" projects: projects that are dependents of in-scope projects but don't have git changes themselves. These are pending cascade evaluation.
5. Store the graph, layer assignments, and pending list.

```go
func (m *DefaultLayerStateManager) InitializeLayerState(
    projects []models.ProjectStatus,
    dependencyGraph map[string][]string,
    changedProjects map[string]bool,
) (*models.LayerState, error) {
    // If no dependencies exist, return nil (no layered planning needed)
    hasDeps := false
    for _, deps := range dependencyGraph {
        if len(deps) > 0 {
            hasDeps = true
            break
        }
    }
    if !hasDeps {
        return nil, nil
    }

    // Check for circular dependencies
    if err := detectCycles(dependencyGraph); err != nil {
        return nil, err
    }

    // Topologically sort changed projects into layers
    projectLayers := make(map[string]int)
    maxLayer := 0

    // Compute layers using depth in the dependency graph
    var computeLayer func(project string) int
    computing := make(map[string]bool) // cycle detection during computation
    computed := make(map[string]int)

    computeLayer = func(project string) int {
        if layer, ok := computed[project]; ok {
            return layer
        }
        computing[project] = true
        layer := 0
        for _, dep := range dependencyGraph[project] {
            if changedProjects[dep] {
                depLayer := computeLayer(dep) + 1
                if depLayer > layer {
                    layer = depLayer
                }
            }
        }
        computed[project] = layer
        delete(computing, project)
        return layer
    }

    for project := range changedProjects {
        layer := computeLayer(project)
        projectLayers[project] = layer
        if layer > maxLayer {
            maxLayer = layer
        }
    }

    // Identify pending projects (dependents of changed projects that
    // don't have changes themselves)
    var pending []string
    reverseDeps := buildReverseDependencyMap(dependencyGraph)
    for project := range changedProjects {
        for _, dependent := range reverseDeps[project] {
            if !changedProjects[dependent] {
                // This dependent doesn't have git changes but may need
                // to be planned if its upstream has actual infra changes
                pending = append(pending, dependent)
            }
        }
    }
    pending = deduplicate(pending)

    return &models.LayerState{
        Enabled:          true,
        CurrentLayer:     0,
        TotalLayers:      maxLayer + 1,
        DependencyGraph:  dependencyGraph,
        ProjectLayers:    projectLayers,
        PendingProjects:  pending,
        SkippedUpstreams: make(map[string]bool),
    }, nil
}
```

### 3.2 IsLayerComplete

```go
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
        case models.AppliedPlanStatus,
             models.PlannedNoChangesPlanStatus,
             models.SkippedPlanStatus:
            // Terminal states - this project is done
            continue
        default:
            blocking = append(blocking, proj.ProjectName)
        }
    }
    return len(blocking) == 0, blocking
}
```

### 3.3 AdvanceLayer (Cascade Evaluation)

This is the core cascade logic. When a layer completes:

1. For each project in the completed layer, determine if it had actual infrastructure changes (status == `AppliedPlanStatus`, meaning it was planned with changes and applied).
2. For each pending dependent whose upstream was in the completed layer:
   - If upstream had changes (applied) -> dependent enters the next layer
   - If upstream had no changes (planned-no-changes) -> dependent is excluded (cascade stops)
   - If upstream was skipped -> dependent is excluded (cascade stops)
3. Update `ProjectLayers`, `TotalLayers`, and `CurrentLayer`.

```go
func (m *DefaultLayerStateManager) AdvanceLayer(
    pullStatus *models.PullStatus,
) (*models.LayerState, []string, error) {
    state := pullStatus.LayerState
    if state == nil {
        return nil, nil, fmt.Errorf("no layer state to advance")
    }

    completedLayer := state.CurrentLayer

    // Determine which completed-layer projects had actual changes
    projectsWithChanges := make(map[string]bool)
    for _, proj := range pullStatus.Projects {
        if proj.Layer == completedLayer && proj.Status == models.AppliedPlanStatus {
            projectsWithChanges[proj.ProjectName] = true
        }
    }

    // Evaluate pending projects for the next layer
    nextLayer := completedLayer + 1
    var nextLayerProjects []string
    var stillPending []string

    for _, pendingProject := range state.PendingProjects {
        deps := state.DependencyGraph[pendingProject]
        shouldInclude := false
        allDepsResolved := true

        for _, dep := range deps {
            depLayer, assigned := state.ProjectLayers[dep]
            if !assigned {
                // This dependency hasn't been planned yet, can't evaluate
                allDepsResolved = false
                continue
            }
            if depLayer > completedLayer {
                // Dependency is in a future layer, can't evaluate yet
                allDepsResolved = false
                continue
            }
            // Check if this dependency had actual changes
            if projectsWithChanges[dep] {
                shouldInclude = true
            }
            // Check if this dependency was skipped
            if state.SkippedUpstreams[dep] {
                shouldInclude = false
                break // Skip takes precedence
            }
        }

        if state.SkippedUpstreams[pendingProject] {
            continue // Already marked as excluded
        }

        if !allDepsResolved {
            stillPending = append(stillPending, pendingProject)
        } else if shouldInclude {
            state.ProjectLayers[pendingProject] = nextLayer
            nextLayerProjects = append(nextLayerProjects, pendingProject)
        }
        // If allDepsResolved && !shouldInclude, the project is simply dropped
        // (cascade stopped because upstream had no changes)
    }

    state.PendingProjects = stillPending

    if len(nextLayerProjects) > 0 {
        state.CurrentLayer = nextLayer
        if nextLayer >= state.TotalLayers {
            state.TotalLayers = nextLayer + 1
        }
    } else if len(stillPending) == 0 {
        // No more projects to plan - all done
        state.CurrentLayer = -1
    } else {
        // There are still pending projects but none are ready yet
        // This shouldn't happen in a well-formed graph
        state.CurrentLayer = nextLayer
    }

    return state, nextLayerProjects, nil
}
```

### 3.4 SkipProject

```go
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
        if pullStatus.Projects[i].ProjectName == projectName {
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
    reverseDeps := buildReverseDependencyMap(state.DependencyGraph)
    var exclude func(name string)
    exclude = func(name string) {
        for _, dep := range reverseDeps[name] {
            state.SkippedUpstreams[dep] = true
            // Remove from pending if present
            for i, p := range state.PendingProjects {
                if p == dep {
                    state.PendingProjects = append(
                        state.PendingProjects[:i],
                        state.PendingProjects[i+1:]...,
                    )
                    break
                }
            }
            exclude(dep) // recursive
        }
    }
    exclude(projectName)
}
```

### 3.5 HandleNewCommit

```go
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
            // Current layer - needs re-plan
            // But only if not already applied
            for _, proj := range pullStatus.Projects {
                if proj.ProjectName == projName && proj.Status != models.AppliedPlanStatus {
                    replanProjects = append(replanProjects, projName)
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
```

## 4. State Transition Table

```
+---------------------------+----------------------------+-----------------------------+
| Current State             | Event                      | Next State                  |
+---------------------------+----------------------------+-----------------------------+
| Layer N active,           | Plan succeeds (changes)    | Project -> PlannedPlanStatus|
| project unplanned         |                            |                             |
+---------------------------+----------------------------+-----------------------------+
| Layer N active,           | Plan succeeds (no changes) | Project -> PlannedNoChanges |
| project unplanned         |                            | (no cascade to dependents)  |
+---------------------------+----------------------------+-----------------------------+
| Layer N active,           | Plan fails                 | Project -> ErroredPlanStatus|
| project unplanned         |                            | Layer blocked               |
+---------------------------+----------------------------+-----------------------------+
| Layer N active,           | Apply succeeds             | Project -> AppliedPlanStatus|
| project planned           |                            | Check layer completion      |
+---------------------------+----------------------------+-----------------------------+
| Layer N active,           | Apply fails                | Project -> ErroredApply     |
| project planned           |                            | Layer blocked               |
+---------------------------+----------------------------+-----------------------------+
| Layer N active,           | Skip command               | Project -> SkippedPlanStatus|
| project apply-errored     |                            | Dependents excluded         |
|                           |                            | Check layer completion      |
+---------------------------+----------------------------+-----------------------------+
| Layer N complete           | All applied/no-changes/    | Cascade evaluation          |
| (all projects terminal)   | skipped                    | -> Layer N+1 planned        |
+---------------------------+----------------------------+-----------------------------+
| Layer N complete,          | Cascade finds no           | CurrentLayer = -1           |
| no more pending           | dependents with changes    | All complete                |
+---------------------------+----------------------------+-----------------------------+
| Any layer active           | New commit affects         | Re-plan affected project    |
|                           | current layer project      | in current layer            |
+---------------------------+----------------------------+-----------------------------+
| Any layer active           | New commit affects         | Reset to that layer         |
|                           | already-applied layer      | Invalidate forward          |
+---------------------------+----------------------------+-----------------------------+
```

### Layer Completion Criteria

A layer is "complete" when every project in that layer has one of these terminal statuses:
- `AppliedPlanStatus` (applied successfully)
- `PlannedNoChangesPlanStatus` (planned with no changes)
- `SkippedPlanStatus` (user explicitly skipped)

A layer is "blocked" if any project has:
- `ErroredPlanStatus` (plan failed)
- `ErroredApplyStatus` (apply failed)
- `PlannedPlanStatus` (planned with changes but not yet applied)

## 5. Helper Functions

**File: `server/events/layer_state_manager.go`**

```go
// detectCycles checks for circular dependencies in the graph.
// Returns an error with the cycle path if found.
func detectCycles(graph map[string][]string) error {
    // Standard DFS cycle detection with path tracking
    // ...
}

// buildReverseDependencyMap inverts the dependency graph.
// Input: project -> [dependencies]
// Output: project -> [dependents]
func buildReverseDependencyMap(graph map[string][]string) map[string][]string {
    reverse := make(map[string][]string)
    for project, deps := range graph {
        for _, dep := range deps {
            reverse[dep] = append(reverse[dep], project)
        }
    }
    return reverse
}

// deduplicate returns a slice with unique elements.
func deduplicate(items []string) []string {
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
```

## 6. Persistence Approach

### 6.1 No Schema Changes Needed

The `LayerState` is embedded in `PullStatus` as a pointer field with `json:"layer_state,omitempty"`. Since both BoltDB and Redis backends serialize `PullStatus` as JSON:

- **BoltDB**: `writePullToBucket` calls `json.Marshal(pull)` and `getPullFromBucket` calls `json.Unmarshal`. The new field automatically serializes/deserializes.
- **Redis**: `writePull` and `getPull` use the same JSON approach.

### 6.2 Backward Compatibility

- Existing `PullStatus` records without `layer_state` will unmarshal with `LayerState == nil`, which is the correct "not active" state.
- No migration needed. Old Atlantis servers can read pull status written by new servers (they just ignore the unknown `layer_state` field). New servers reading old data see `nil` and behave normally.

### 6.3 No New Database Methods

All layer state mutations go through existing `UpdatePullWithResults` and `UpdateProjectStatus` methods. The only change is that `PullStatus` now carries more data. The read-modify-write pattern in both backends already handles this correctly.

However, we do need a new method for updating just the LayerState:

**File: `server/core/db/db.go`** - Add to interface:

```go
// UpdateLayerState updates only the LayerState portion of a pull's status.
UpdateLayerState(pull models.PullRequest, state *models.LayerState) error
```

**File: `server/core/boltdb/boltdb.go`** - Add implementation:

```go
func (b *BoltDB) UpdateLayerState(pull models.PullRequest, state *models.LayerState) error {
    key, err := b.pullKey(pull)
    if err != nil {
        return err
    }
    return b.db.Update(func(tx *bolt.Tx) error {
        bucket := tx.Bucket(b.pullsBucketName)
        currStatusPtr, err := b.getPullFromBucket(bucket, key)
        if err != nil {
            return err
        }
        if currStatusPtr == nil {
            return nil
        }
        currStatus := *currStatusPtr
        currStatus.LayerState = state
        return b.writePullToBucket(bucket, key, currStatus)
    })
}
```

**File: `server/core/redis/redis.go`** - Add matching implementation.

**File: `server/core/db/mocks/mock_database.go`** - Regenerate mock.

### 6.4 ProjectStatus Layer Assignment

When `UpdatePullWithResults` is called, the existing merge logic in BoltDB and Redis needs to preserve the `Layer` field on `ProjectStatus`. Since the merge works by matching on `Workspace + RepoRelDir + ProjectName` and updating the `Status` field, we need to ensure:

- When creating a new project status entry, the `Layer` value comes from the `LayerState.ProjectLayers` map.
- The `projectResultToProject` helper needs updating.

**Modification to `projectResultToProject` in both `server/core/boltdb/boltdb.go` and `server/core/redis/redis.go`:**

The function currently does not set `Layer`. We leave `Layer` at its zero value (0) here. The caller (plan/apply command runner) is responsible for setting the correct layer by looking up `LayerState.ProjectLayers[projectName]` and setting it on the `ProjectStatus` before or after calling `UpdatePullWithResults`.

Better approach: Add a method to `LayerStateManager` that the command runner calls after `UpdatePullWithResults` to stamp the correct layer values:

```go
// StampLayerAssignments sets the Layer field on each ProjectStatus based on
// the LayerState's ProjectLayers mapping. Call this after UpdatePullWithResults.
func (m *DefaultLayerStateManager) StampLayerAssignments(pullStatus *models.PullStatus) {
    if pullStatus.LayerState == nil {
        return
    }
    for i := range pullStatus.Projects {
        proj := &pullStatus.Projects[i]
        if layer, ok := pullStatus.LayerState.ProjectLayers[proj.ProjectName]; ok {
            proj.Layer = layer
        }
    }
}
```

## 7. Integration Points

### 7.1 Where LayerStateManager Gets Called

The Layer State Manager is a pure logic component. It does not call the database or VCS directly. The command runners are responsible for calling it at the right points:

**`server/events/plan_command_runner.go`:**

1. **Autoplan / `atlantis plan`**: After building project commands, if layered planning is enabled:
   - Build the dependency graph from project configs.
   - Call `InitializeLayerState()` to create the layer state.
   - Filter `projectCmds` to only include layer 0 projects.
   - After plan execution, call `StampLayerAssignments()`.
   - Persist via `dbUpdater.updateDB()`.

2. **After apply completes a layer** (called from apply command runner):
   - Call `CanAdvance()`.
   - If yes, call `AdvanceLayer()` to get next layer projects.
   - Build project commands for those projects.
   - Run plans for the next layer.
   - Update DB and dashboard.

**`server/events/apply_command_runner.go`:**

1. **After apply**: Check if layer is now complete.
   - Call `IsLayerComplete()`.
   - If complete, trigger next-layer planning (delegating to plan command runner).

2. **Skip command**: Call `SkipProject()` and persist the updated status.

### 7.2 Filtering Projects by Layer

The plan command builder currently builds project commands for all changed projects. With layered planning, the command runner must filter:

```go
// filterProjectsByLayer returns only the project commands for the specified layer.
func filterProjectsByLayer(
    cmds []command.ProjectContext,
    layerState *models.LayerState,
    layer int,
) []command.ProjectContext {
    if layerState == nil {
        return cmds // No layering, return all
    }
    var filtered []command.ProjectContext
    for _, cmd := range cmds {
        projectLayer, ok := layerState.ProjectLayers[cmd.ProjectName]
        if ok && projectLayer == layer {
            filtered = append(filtered, cmd)
        }
    }
    return filtered
}
```

This should be added to `server/events/plan_command_runner.go` or a shared helper file.

## 8. Files to Create / Modify

### New Files

| File | Purpose |
|------|---------|
| `server/events/layer_state_manager.go` | Interface + DefaultLayerStateManager implementation |
| `server/events/layer_state_manager_test.go` | Comprehensive unit tests |
| `server/events/layer_graph.go` | Graph utilities: cycle detection, topological sort, reverse deps |
| `server/events/layer_graph_test.go` | Graph utility tests |

### Modified Files

| File | Change |
|------|--------|
| `server/events/models/models.go` | Add `SkippedPlanStatus`, `LayerState` struct, `Layer` field on `ProjectStatus`, `LayerState` field on `PullStatus` |
| `server/core/db/db.go` | Add `UpdateLayerState` to `Database` interface |
| `server/core/boltdb/boltdb.go` | Implement `UpdateLayerState` |
| `server/core/redis/redis.go` | Implement `UpdateLayerState` |
| `server/core/db/mocks/mock_database.go` | Regenerate (via `go generate`) |
| `server/events/command/project_result.go` | No change needed (PlanStatus() already returns correct status for existing commands) |

### Files Modified by Other Workstreams (Not This One)

These files will need changes but they belong to the command runner and dashboard workstreams:

| File | Change (done by other workstream) |
|------|------|
| `server/events/plan_command_runner.go` | Call LayerStateManager during plan |
| `server/events/apply_command_runner.go` | Call LayerStateManager after apply, handle skip |
| `server/events/event_parser.go` | Parse `-skip` flag on apply command |
| `server/events/project_command_context_builder.go` | Pass layer info into project context |

## 9. Testing Approach

**This workstream uses TDD.** Write tests first, then implement to pass. The state manager has clearly defined state transitions and query methods — ideal for test-driven development.

- **Framework:** Standard Go testing with custom assertions from `testing/` package
- **Mocks:** Pegomock for the `Database` interface (use existing `server/core/db/mocks/mock_database.go` pattern)
- **Test file:** `server/events/layer_state_manager_test.go`
- **Table-driven tests** for state transitions; individual tests for persistence and edge cases

## 10. Test Cases

### 10.1 Unit Tests for `InitializeLayerState`

**File: `server/events/layer_state_manager_test.go`**

| Test | Input | Expected |
|------|-------|----------|
| No dependencies | Projects A, B, C with no `depends_on` | Returns `nil` (no layered planning) |
| Simple chain A -> B -> C | A has changes, B depends on A, C depends on B | Layer 0: [A], Layer 1: [B], Layer 2: [C]; pending: [B, C] since B, C don't have git changes (only A does) |
| Simple chain, all changed | A, B, C all have git changes | Layer 0: [A], Layer 1: [B], Layer 2: [C]; no pending |
| Diamond: A -> B, A -> C, B -> D, C -> D | All changed | Layer 0: [A], Layer 1: [B, C], Layer 2: [D] |
| Parallel chains | A -> B, C -> D (independent) | Layer 0: [A, C], Layer 1: [B, D] |
| Circular dependency | A -> B -> A | Returns error |
| Mixed: some changed, some not | A changed, B depends on A but not changed | Layer 0: [A], pending: [B] |

### 9.2 Unit Tests for `IsLayerComplete`

| Test | State | Expected |
|------|-------|----------|
| All applied | Layer 0: [A=applied, B=applied] | complete=true, blocking=[] |
| All no-changes | Layer 0: [A=no-changes, B=no-changes] | complete=true |
| Mixed terminal | Layer 0: [A=applied, B=no-changes, C=skipped] | complete=true |
| Has planned (needs apply) | Layer 0: [A=applied, B=planned] | complete=false, blocking=[B] |
| Has errored apply | Layer 0: [A=applied, B=errored-apply] | complete=false, blocking=[B] |
| Has errored plan | Layer 0: [A=errored-plan, B=planned] | complete=false, blocking=[A, B] |

### 9.3 Unit Tests for `AdvanceLayer`

| Test | State | Expected |
|------|-------|----------|
| Upstream had changes, pending dependent exists | Layer 0 complete with A=applied; B pending, depends on A | Next layer has [B], CurrentLayer=1 |
| Upstream had no changes, cascade stops | Layer 0 complete with A=no-changes; B pending, depends on A | B excluded, no next layer, CurrentLayer=-1 |
| Upstream skipped, cascade stops | Layer 0 complete with A=skipped; B pending, depends on A | B excluded |
| Multiple upstreams, mixed | B depends on A and C; A=applied, C=no-changes | B still included (any upstream with changes triggers) |
| No pending projects | Layer 0 complete, no pending | CurrentLayer=-1 (all done) |
| Multi-layer cascade | Layer 0: [A=applied], pending: [B, C]; B depends on A, C depends on B | Layer 1: [B] added, C remains pending |

### 9.4 Unit Tests for `SkipProject`

| Test | Expected |
|------|----------|
| Skip disabled | Error: skip not enabled |
| Project not in current layer | Error: not in current layer |
| Project not errored | Error: can only skip failed applies |
| Valid skip | Status -> Skipped, dependents excluded |
| Skip with transitive dependents | A skipped -> B (depends on A) excluded -> C (depends on B) excluded |

### 9.5 Unit Tests for `HandleNewCommit`

| Test | Expected |
|------|----------|
| Affected project in future layer | NoAction |
| Affected project in current layer, not applied | ReplanProjects includes it |
| Affected project in current layer, already applied | No replan (don't touch applied) |
| Affected project in completed layer | ResetToLayer = that layer |
| Multiple affected projects, different layers | ResetToLayer = minimum affected completed layer |
| No affected projects in any layer | NoAction |

### 9.6 Unit Tests for Graph Utilities

**File: `server/events/layer_graph_test.go`**

| Test | Expected |
|------|----------|
| detectCycles: no cycle | nil |
| detectCycles: self-referencing | Error with cycle path |
| detectCycles: 2-node cycle | Error with cycle path |
| detectCycles: deep cycle (A->B->C->A) | Error with cycle path |
| buildReverseDependencyMap: simple | Correct inverse |
| buildReverseDependencyMap: empty | Empty map |

### 9.7 Integration Tests

| Test | Description |
|------|-------------|
| Full lifecycle | Initialize -> plan L0 -> apply L0 -> advance -> plan L1 -> apply L1 -> complete |
| No-changes cascade stop | L0 plans with no changes -> L1 never created |
| Skip and cascade stop | L0 apply fails -> skip -> advance -> dependents excluded |
| New commit mid-lifecycle | L0 applied, L1 planning, commit affects L0 -> reset |
| Persistence round-trip | Create LayerState, serialize to BoltDB, read back, verify identical |

## 10. Risks and Open Questions

### 10.1 Risks

1. **PullStatus size growth**: With many projects (100+), the `LayerState` adds non-trivial JSON to each PullStatus entry. The `DependencyGraph` and `ProjectLayers` maps could be large. Mitigation: the existing PullStatus already stores all project statuses, so the incremental cost is mainly the graph edges. For 100 projects with ~200 edges, this is roughly 5-10KB additional JSON -- acceptable.

2. **Concurrent modifications**: Both BoltDB and Redis use read-modify-write patterns. BoltDB wraps in a transaction, which is safe. Redis does not use transactions for this -- if two Atlantis workers process commands concurrently for the same PR, there is a race condition. This is a pre-existing issue (not introduced by this change) but layered state makes the consequences worse (corrupted layer tracking). Mitigation: document that layered planning assumes single-worker processing per PR, which matches current Atlantis behavior (project locks prevent concurrent applies).

3. **Server restart during layer transition**: If the server restarts after apply completes but before AdvanceLayer runs, the layer state will show a completed layer but CurrentLayer won't have advanced. On restart, the next command (manual plan or apply) should detect this and trigger advancement. Mitigation: Add a `CanAdvance` check at the start of plan/apply command runners.

4. **Backward compatibility of ProjectStatus.Layer**: Adding a `Layer int` field to `ProjectStatus` defaults to 0 for existing records. Since layer 0 is a valid layer, code must check `LayerState != nil` before interpreting the `Layer` field. When `LayerState` is nil, `Layer` should be ignored.

### 10.2 Open Questions

1. **Should `AdvanceLayer` be triggered automatically or require user action?** The design document says "automatically planned" after layer completion. This plan assumes automatic: after the last apply in a layer, the apply command runner checks `CanAdvance()` and immediately calls `AdvanceLayer()` + triggers planning. This means the apply command handler has a longer execution path (apply + plan next layer). Alternative: post a comment asking the user to run `atlantis plan` to advance. Recommendation: automatic, matching the design doc.

2. **Should the Layer field on ProjectStatus use a pointer (`*int`) to distinguish "not set" from "layer 0"?** Using a plain `int` means the zero value is ambiguous. Since we always check `LayerState != nil` before interpreting `Layer`, this is safe. Using a pointer adds complexity for marginal benefit. Recommendation: keep as `int`, gate on `LayerState != nil`.

3. **How does `atlantis plan` (manual re-plan) interact with layered state?** The design says "re-plans the current layer." If a user runs `atlantis plan` with no arguments during layered planning, we should re-plan all projects in the current layer, not all projects across all layers. If they specify `-p <project>`, we re-plan just that project (must be in current layer). This filtering logic belongs to the command runner workstream, not this one, but the state manager provides the data.

4. **What happens if `depends_on` references a project name that doesn't exist?** The graph builder should validate this and return a clear error. This validation should happen in `InitializeLayerState`.

5. **Thread safety**: `DefaultLayerStateManager` is stateless (all state is in `PullStatus`/`LayerState`). This is intentional -- it means the manager can be safely shared across goroutines without synchronization. All mutations happen on copies of `PullStatus` that are then persisted atomically.

NOT FOUND
