# Layered Planning: Plan/Apply Lifecycle Integration

## Overview

This document describes how layered planning integrates into the existing Atlantis plan/apply command flow. It covers autoplan modification, apply modification, manual plan, skip commands, feature gating, and dashboard triggering.

**Parent design:** `docs/plans/2026-02-13-layered-planning-design.md`

---

## 1. Architecture Summary

### Current Flow (Before Layered Planning)

```
PR Open/Push
  -> DefaultCommandRunner.RunAutoplanCommand()
     -> PlanCommandRunner.runAutoplan()
        -> prjCmdBuilder.BuildAutoplanCommands()  // finds ALL modified projects
        -> runProjectCmdsWithCancellationTracker() // runs ALL plans (grouped by ExecutionOrderGroup)
        -> pullUpdater.updatePull()                // posts plan output comment
        -> dbUpdater.updateDB()                    // persists PullStatus
        -> updateCommitStatus()                    // updates VCS commit status

Comment: "atlantis apply"
  -> DefaultCommandRunner.RunCommentCommand()
     -> ApplyCommandRunner.Run()
        -> prjCmdBuilder.BuildApplyCommands()      // finds all pending plans
        -> runProjectCmdsWithCancellationTracker()  // applies ALL pending plans
        -> pullUpdater.updatePull()                 // posts apply output comment
        -> dbUpdater.updateDB()                     // persists PullStatus
        -> updateCommitStatus()                     // updates VCS commit status
        -> autoMerger.automerge()                   // merges if enabled
```

### New Flow (With Layered Planning Enabled)

```
PR Open/Push
  -> DefaultCommandRunner.RunAutoplanCommand()
     -> PlanCommandRunner.runAutoplan()
        -> prjCmdBuilder.BuildAutoplanCommands()    // finds ALL modified projects
        -> [NEW] layerManager.FilterToCurrentLayer() // keeps only layer 0
        -> runProjectCmdsWithCancellationTracker()   // runs ONLY layer 0 plans
        -> pullUpdater.updatePull()                  // posts plan output comment
        -> dbUpdater.updateDB()                      // persists PullStatus
        -> [NEW] layerManager.UpdateLayerState()     // tracks layer progress
        -> [NEW] dashboardRenderer.UpdateDashboard() // creates/updates sticky comment
        -> updateCommitStatus()

Comment: "atlantis apply"
  -> DefaultCommandRunner.RunCommentCommand()
     -> ApplyCommandRunner.Run()
        -> prjCmdBuilder.BuildApplyCommands()        // finds pending plans
        -> [NEW] layerManager.FilterToCurrentLayer()  // keeps only current layer
        -> runProjectCmdsWithCancellationTracker()    // applies current layer only
        -> pullUpdater.updatePull()
        -> dbUpdater.updateDB()
        -> [NEW] layerManager.UpdateLayerState()
        -> [NEW] If layer complete: triggerNextLayerPlanning()
        -> [NEW] dashboardRenderer.UpdateDashboard()
        -> updateCommitStatus()
```

---

## 2. Feature Gating

### 2.1 Server Flags

Two new flags in `server/user_config.go`:

```go
// In UserConfig struct
EnableLayeredPlanning  bool `mapstructure:"enable-layered-planning"`
EnableLayeredApplySkip bool `mapstructure:"enable-layered-apply-skip"`
```

Corresponding CLI flag definitions in `cmd/server.go`:

```go
// Flag name constants
EnableLayeredPlanningFlag  = "enable-layered-planning"
EnableLayeredApplySkipFlag = "enable-layered-apply-skip"
```

### 2.2 Where Flags Are Checked

The flags should be checked at the **command runner level**, not deep in the builder or executor. This keeps the feature gate close to the entry point and avoids threading flags through many layers.

| Check Point | File | Purpose |
|---|---|---|
| Autoplan entry | `server/events/plan_command_runner.go` `runAutoplan()` | Gate layer filtering of autoplan projects |
| Manual plan entry | `server/events/plan_command_runner.go` `run()` | Gate layer filtering of manual plan |
| Apply entry | `server/events/apply_command_runner.go` `Run()` | Gate layer filtering and next-layer triggering |
| Skip command parsing | `server/events/comment_parser.go` `Parse()` | Gate `-skip` flag on apply |
| Dashboard rendering | `server/events/plan_command_runner.go`, `server/events/apply_command_runner.go` | Gate dashboard comment creation |

### 2.3 Flag Propagation

The flags must flow from `UserConfig` through `server.go` into the command runners. The approach:

1. Add `EnableLayeredPlanning bool` and `EnableLayeredApplySkip bool` fields to `PlanCommandRunner` and `ApplyCommandRunner`.
2. Pass them through `NewPlanCommandRunner()` and `NewApplyCommandRunner()`.
3. Also add `EnableLayeredApplySkip bool` to `CommentParser` for gating the `-skip` flag.

### 2.4 Activation Logic

Layered planning activates when ALL of the following are true:
- `enable-layered-planning` server flag is set
- The repo's `atlantis.yaml` contains at least one project with `depends_on`
- The in-scope projects (those with changes) include at least one with `depends_on`

When active but the calculated layers result in only a single layer (layer 0), behavior is identical to today -- no dashboard is shown, no layer filtering occurs. This is the "conditional dashboard" requirement.

---

## 3. New Components

### 3.1 Layer Manager

**New file:** `server/events/layer_manager.go`

The `LayerManager` is the central coordinator for layered planning state. It is stateless itself -- it computes layers from project configs and current `PullStatus`.

```go
type LayerManager struct {
    EnableLayeredPlanning  bool
    EnableLayeredApplySkip bool
}

// LayerState represents the computed layer state for a PR
type LayerState struct {
    Layers       []Layer    // ordered list of layers
    CurrentLayer int        // index of the current active layer
    TotalProjects int       // total in-scope projects
    HasMultipleLayers bool  // true if layers > 1 (controls dashboard visibility)
}

// Layer represents a single execution layer
type Layer struct {
    Index    int
    Projects []LayerProject
}

// LayerProject represents a project within a layer
type LayerProject struct {
    ProjectName string
    RepoRelDir  string
    Workspace   string
    Status      LayerProjectStatus
}

type LayerProjectStatus int
const (
    LayerStatusPending LayerProjectStatus = iota
    LayerStatusPlanning
    LayerStatusPlannedChanges
    LayerStatusPlannedNoChanges
    LayerStatusApplied
    LayerStatusApplyFailed
    LayerStatusSkipped
)
```

Key methods:

```go
// IsEnabled returns true if layered planning should activate for this set of projects.
// Checks the server flag AND whether any project has depends_on.
func (lm *LayerManager) IsEnabled(projectCfgs []valid.MergedProjectCfg) bool

// ComputeLayers takes in-scope project configs and returns the layer assignment.
// This is a pure function -- it computes layers from the dependency graph.
func (lm *LayerManager) ComputeLayers(projectCfgs []valid.MergedProjectCfg) (*LayerState, error)

// FilterToCurrentLayer filters a slice of ProjectContexts to only those
// in the current layer, given the current PullStatus.
func (lm *LayerManager) FilterToCurrentLayer(
    projectCmds []command.ProjectContext,
    pullStatus *models.PullStatus,
    layerState *LayerState,
) []command.ProjectContext

// IsLayerComplete returns true if all projects in the current layer are
// either applied, planned-no-changes, or skipped.
func (lm *LayerManager) IsLayerComplete(
    pullStatus models.PullStatus,
    layerState *LayerState,
) bool

// GetNextLayerProjects returns the project contexts for the next layer
// that should be planned, applying cascade rules (only plan downstreams
// of projects that had actual changes).
func (lm *LayerManager) GetNextLayerProjects(
    layerState *LayerState,
    pullStatus models.PullStatus,
    allProjectCfgs []valid.MergedProjectCfg,
) []valid.MergedProjectCfg
```

### 3.2 Layer State Persistence

Layer state needs to survive across requests. Options:

**Recommended approach:** Store layer metadata alongside the existing `PullStatus` in the BoltDB database.

Add a new database method and model:

```go
// In server/core/db/db.go Database interface:
SaveLayerState(pull models.PullRequest, state models.LayerState) error
GetLayerState(pull models.PullRequest) (*models.LayerState, error)
DeleteLayerState(pull models.PullRequest) error
```

Add to `server/events/models/models.go`:

```go
// LayerState persists the layer assignments for a PR
type LayerState struct {
    CurrentLayer int
    Layers       []LayerInfo
}

type LayerInfo struct {
    Index    int
    Projects []LayerProjectInfo
}

type LayerProjectInfo struct {
    ProjectName string
    RepoRelDir  string
    Workspace   string
    Status      LayerProjectStatus
    // UpstreamHadChanges tracks whether this project's upstream had changes,
    // used for cascade logic
    UpstreamHadChanges bool
}

type LayerProjectStatus int
const (
    LayerProjectPending LayerProjectStatus = iota
    LayerProjectPlanning
    LayerProjectPlannedChanges
    LayerProjectPlannedNoChanges
    LayerProjectApplied
    LayerProjectApplyFailed
    LayerProjectSkipped
)
```

### 3.3 Dashboard Renderer

**New file:** `server/events/layer_dashboard.go`

The dashboard renderer creates/updates a sticky comment on the PR.

```go
type LayerDashboardRenderer struct {
    VCSClient vcs.Client
}

// RenderDashboard generates the markdown for the dashboard comment
func (r *LayerDashboardRenderer) RenderDashboard(layerState *models.LayerState) string

// UpdateDashboard creates or updates the sticky dashboard comment on the PR
func (r *LayerDashboardRenderer) UpdateDashboard(
    logger logging.SimpleLogging,
    repo models.Repo,
    pullNum int,
    layerState *models.LayerState,
) error
```

The VCS `Client` interface currently does not have an `UpdateComment` method. The dashboard requires a sticky comment that is updated in place. Two approaches:

**Option A (Recommended):** Use `HidePrevCommandComments` to hide old dashboard comments and create a new one each time. The dashboard comment uses a unique command tag (e.g., `"layered-dashboard"`) so `HidePrevCommandComments` can target it specifically.

**Option B:** Add `UpdateComment(logger, repo, pullNum, commentID, body)` to the VCS `Client` interface. This requires storing the comment ID in the layer state. This is cleaner but requires VCS client changes across all providers.

Start with Option A; it works with the existing interface. Option B can be a follow-up.

---

## 4. Detailed Scenario Flows

### 4.1 Autoplan (PR Open or Push)

**Entry point:** `PlanCommandRunner.runAutoplan()` (line 100 of `plan_command_runner.go`)

**Current flow:**
1. `prjCmdBuilder.BuildAutoplanCommands(ctx)` returns ALL modified projects
2. Filter by `AutoplanEnabled`
3. Run all plans

**Modified flow:**

```
runAutoplan(ctx):
  projectCmds = prjCmdBuilder.BuildAutoplanCommands(ctx)
  projectCmds = filter by AutoplanEnabled  // existing

  IF NOT layerManager.IsEnabled(projectCmds):
    // existing flow unchanged
    run all plans, post results, update DB
    RETURN

  // Layered planning path
  layerState, err = layerManager.ComputeLayers(projectCmds)
  IF err (e.g., circular dependency):
    post error comment, RETURN

  IF NOT layerState.HasMultipleLayers:
    // Single layer -- identical to today, no dashboard
    run all plans, post results, update DB
    RETURN

  // Multi-layer path
  currentLayerCmds = layerManager.FilterToCurrentLayer(projectCmds, nil, layerState)

  // Delete previous plans and locks (existing behavior)
  deletePlans(ctx)
  lockingLocker.UnlockByPull(...)

  result = runProjectCmdsWithCancellationTracker(ctx, currentLayerCmds, ...)

  // Post individual plan comments (existing)
  pullUpdater.updatePull(ctx, AutoplanCommand{}, result)

  // Update DB with results (existing)
  pullStatus = dbUpdater.updateDB(ctx, pull, result.ProjectResults)

  // NEW: Update layer state with plan results
  layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
  database.SaveLayerState(pull, layerState)

  // NEW: Create/update dashboard comment
  dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)

  // Update commit status (existing)
  updateCommitStatus(ctx, pullStatus, command.Plan)
  updateCommitStatus(ctx, pullStatus, command.Apply)

  // Policy checks on current layer (existing pattern)
  if policyCheckCmds > 0 && !result.HasErrors():
    policyCheckCommandRunner.Run(ctx, policyCheckCmds filtered to current layer)
```

**Key changes in `plan_command_runner.go`:**
- Add `layerManager *LayerManager` field
- Add `dashboardRenderer *LayerDashboardRenderer` field
- Add `database db.Database` field (for layer state persistence)
- Insert layer computation and filtering between `BuildAutoplanCommands` and `runProjectCmds`

### 4.2 Manual Plan (`atlantis plan`)

**Entry point:** `PlanCommandRunner.run()` (line 184 of `plan_command_runner.go`)

**Current flow:**
1. `prjCmdBuilder.BuildPlanCommands(ctx, cmd)` returns project contexts
2. If no specific project, delete previous plans
3. Run all plans

**Modified flow:**

```
run(ctx, cmd):
  projectCmds = prjCmdBuilder.BuildPlanCommands(ctx, cmd)

  IF cmd.IsForSpecificProject():
    // Specific project plan -- no layer filtering needed.
    // The user explicitly asked for this project.
    // Validate it's in the current layer (warn if not, but allow).
    run existing flow
    RETURN

  // Generic "atlantis plan" -- re-plan current layer
  IF NOT layerManager.IsEnabled(projectCmds):
    run existing flow
    RETURN

  layerState = database.GetLayerState(pull)
  IF layerState == nil:
    // First plan, compute fresh
    layerState = layerManager.ComputeLayers(projectCmds)

  IF NOT layerState.HasMultipleLayers:
    run existing flow
    RETURN

  // Re-plan current layer only
  currentLayerCmds = layerManager.FilterToCurrentLayer(projectCmds, pullStatus, layerState)

  // Delete previous plans for current layer only (not all plans)
  // This is a change from existing behavior
  deletePlansForProjects(ctx, currentLayerCmds)

  result = runProjectCmdsWithCancellationTracker(ctx, currentLayerCmds, ...)

  pullUpdater.updatePull(ctx, cmd, result)
  pullStatus = dbUpdater.updateDB(ctx, pull, result.ProjectResults)

  layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
  database.SaveLayerState(pull, layerState)
  dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)

  updateCommitStatus(ctx, pullStatus, command.Plan)
  updateCommitStatus(ctx, pullStatus, command.Apply)
```

**Important:** `atlantis plan` with no args should NOT advance layers. It re-plans the current layer. This is for recovering from failed applies or incorporating new commits.

### 4.3 Apply All (`atlantis apply` with no args)

**Entry point:** `ApplyCommandRunner.Run()` (line 73 of `apply_command_runner.go`)

**Current flow:**
1. Check global apply lock and `DisableApplyAll`
2. `prjCmdBuilder.BuildApplyCommands(ctx, cmd)` finds all pending plans
3. Run all applies
4. Update pull, DB, commit status, automerge

**Modified flow:**

```
Run(ctx, cmd):
  // Existing checks: global lock, DisableApplyAll
  // ... unchanged ...

  projectCmds = prjCmdBuilder.BuildApplyCommands(ctx, cmd)

  IF NOT layerManager.IsEnabled(projectCmds):
    run existing flow
    RETURN

  layerState = database.GetLayerState(pull)
  IF layerState == nil OR NOT layerState.HasMultipleLayers:
    run existing flow
    RETURN

  // Filter to current layer
  currentLayerCmds = layerManager.FilterToCurrentLayer(projectCmds, pullStatus, layerState)

  IF len(currentLayerCmds) == 0:
    post comment "No projects to apply in current layer"
    RETURN

  // Apply current layer
  result = runProjectCmdsWithCancellationTracker(ctx, currentLayerCmds, ...)
  ctx.CommandHasErrors = result.HasErrors()

  pullUpdater.updatePull(ctx, cmd, result)
  pullStatus = dbUpdater.updateDB(ctx, pull, result.ProjectResults)

  // Update layer state
  layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
  database.SaveLayerState(pull, layerState)

  IF result.HasErrors():
    // Failed apply -- block layer advancement
    dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)
    updateCommitStatus(ctx, pullStatus)
    RETURN

  IF layerManager.IsLayerComplete(pullStatus, layerState):
    // Advance to next layer
    nextLayerCfgs = layerManager.GetNextLayerProjects(layerState, pullStatus, allProjectCfgs)

    IF len(nextLayerCfgs) == 0:
      // All layers done!
      dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)
      updateCommitStatus(ctx, pullStatus)
      autoMerger.automerge(...)  // if enabled
      RETURN

    // Auto-trigger planning of next layer
    layerState.CurrentLayer++
    triggerNextLayerPlanning(ctx, nextLayerCfgs, layerState)
  ELSE:
    // Layer not complete yet -- some projects still need apply
    dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)
    updateCommitStatus(ctx, pullStatus)
```

### 4.4 Apply Single Project (`atlantis apply -d <project>`)

**Entry point:** `ApplyCommandRunner.Run()` -- same as above, but `cmd.IsForSpecificProject()` is true.

**Current flow:**
1. `prjCmdBuilder.BuildApplyCommands(ctx, cmd)` finds the specific project
2. Run single apply

**Modified flow:**

```
Run(ctx, cmd):
  // When cmd.IsForSpecificProject() is true:
  projectCmds = prjCmdBuilder.BuildApplyCommands(ctx, cmd)

  IF NOT layerManager.IsEnabled(projectCmds):
    run existing flow
    RETURN

  layerState = database.GetLayerState(pull)
  IF layerState == nil OR NOT layerState.HasMultipleLayers:
    run existing flow
    RETURN

  // Validate the project is in the current layer
  IF NOT layerManager.IsInCurrentLayer(projectCmds[0], layerState):
    post error comment "Project <name> is not in the current layer (layer N).
    You must apply all projects in layer N before this project becomes available."
    RETURN

  // Apply the single project
  result = runProjectCmdsWithCancellationTracker(ctx, projectCmds, ...)

  pullUpdater.updatePull(ctx, cmd, result)
  pullStatus = dbUpdater.updateDB(ctx, pull, result.ProjectResults)

  layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
  database.SaveLayerState(pull, layerState)

  // Check if this completed the layer
  IF NOT result.HasErrors() AND layerManager.IsLayerComplete(pullStatus, layerState):
    // Same next-layer logic as apply-all
    triggerNextLayerPlanning(ctx, nextLayerCfgs, layerState)
  ELSE:
    dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)
    updateCommitStatus(ctx, pullStatus)
```

### 4.5 Skip Command (`atlantis apply -skip <project>`)

**Entry point:** `CommentParser.Parse()` and `ApplyCommandRunner.Run()`

**Comment parsing changes:**

In `comment_parser.go`, when the `apply` command is being parsed and `EnableLayeredApplySkip` is true, add a new flag:

```go
case command.Apply.String():
    name = command.Apply
    flagSet = pflag.NewFlagSet(command.Apply.String(), pflag.ContinueOnError)
    // ... existing flags ...
    if e.EnableLayeredApplySkip {
        flagSet.StringVarP(&skipProject, "skip", "", "", "Skip a failed project in the current layer")
    }
```

Add to `CommentCommand`:

```go
type CommentCommand struct {
    // ... existing fields ...
    SkipProject string  // NEW: project name/dir to skip
}
```

**Apply runner changes:**

```
Run(ctx, cmd):
  IF cmd.SkipProject != "" AND layerManager is enabled:
    layerState = database.GetLayerState(pull)
    IF layerState == nil:
      post error "No active layered planning session"
      RETURN

    // Validate: can only skip projects in current layer that have failed
    project = layerManager.FindProject(layerState, cmd.SkipProject)
    IF project == nil:
      post error "Project <name> not found in current layers"
      RETURN
    IF project.Status != LayerProjectApplyFailed:
      post error "Can only skip projects that have failed an apply.
      Project <name> has status: <status>"
      RETURN

    // Mark as skipped
    layerManager.MarkSkipped(layerState, cmd.SkipProject)
    database.SaveLayerState(pull, layerState)

    // Check if layer is now complete
    IF layerManager.IsLayerComplete(pullStatus, layerState):
      triggerNextLayerPlanning(ctx, nextLayerCfgs, layerState)
    ELSE:
      dashboardRenderer.UpdateDashboard(log, repo, pullNum, layerState)

    RETURN  // skip doesn't produce normal apply output
```

### 4.6 Auto-Trigger Next Layer Planning

**New method:** `triggerNextLayerPlanning()` on `ApplyCommandRunner` (or a shared helper).

This is called after a layer is fully complete (all projects applied, no-changes, or skipped).

```go
func (a *ApplyCommandRunner) triggerNextLayerPlanning(
    ctx *command.Context,
    nextLayerCfgs []valid.MergedProjectCfg,
    layerState *models.LayerState,
) {
    // Build plan contexts for next layer projects
    // These are projects whose upstream dependencies have been applied
    // and had actual changes (cascade rule).

    var planCmds []command.ProjectContext
    for _, cfg := range nextLayerCfgs {
        projCtxs := a.projCmdContextBuilder.BuildProjectContext(
            ctx, command.Plan, "", cfg, nil, repoDir,
            automerge, parallelApply, parallelPlan, false, abortOnExecOrderFail,
            terraformClient,
        )
        planCmds = append(planCmds, projCtxs...)
    }

    if len(planCmds) == 0 {
        // No next layer projects to plan (cascade stopped).
        // Check if there are further layers or if we're done.
        // Update dashboard to show completion.
        return
    }

    // Run plans for next layer
    result := runProjectCmdsWithCancellationTracker(
        ctx, planCmds, a.cancellationTracker,
        a.parallelPoolSize, a.isParallelEnabled(planCmds),
        a.prjCmdRunner.Plan,
    )

    // Post plan results as comments
    a.pullUpdater.updatePull(ctx, &CommentCommand{Name: command.Plan}, result)

    // Update DB
    pullStatus, _ := a.dbUpdater.updateDB(ctx, ctx.Pull, result.ProjectResults)

    // Update layer state
    a.layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
    a.database.SaveLayerState(ctx.Pull, layerState)

    // Update dashboard
    a.dashboardRenderer.UpdateDashboard(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, layerState)

    // If ALL projects in this layer have no changes, auto-advance again
    if a.layerManager.IsLayerComplete(pullStatus, layerState) {
        nextNextCfgs := a.layerManager.GetNextLayerProjects(layerState, pullStatus, allCfgs)
        if len(nextNextCfgs) > 0 {
            layerState.CurrentLayer++
            a.triggerNextLayerPlanning(ctx, nextNextCfgs, layerState)
        }
        // Otherwise: all done, dashboard shows completion
    }
}
```

**Important:** This recursive call handles the case where a layer has all no-changes plans -- it auto-advances through multiple layers until it finds one with actual changes or reaches the end.

---

## 5. Files to Create

| File | Purpose |
|---|---|
| `server/events/layer_manager.go` | Core layer computation, filtering, and state management |
| `server/events/layer_manager_test.go` | Unit tests for layer manager |
| `server/events/layer_dashboard.go` | Dashboard markdown rendering and VCS comment management |
| `server/events/layer_dashboard_test.go` | Unit tests for dashboard renderer |
| `server/events/models/layer_state.go` | Layer state model types (or add to `models.go`) |

## 6. Files to Modify

| File | Change |
|---|---|
| `server/user_config.go` | Add `EnableLayeredPlanning` and `EnableLayeredApplySkip` fields |
| `cmd/server.go` | Add flag definitions for both new flags |
| `server/server.go` | Wire new flags into command runners and comment parser |
| `server/events/plan_command_runner.go` | Add layer filtering in `runAutoplan()` and `run()`, add dashboard updates |
| `server/events/apply_command_runner.go` | Add layer filtering, next-layer triggering, skip handling |
| `server/events/comment_parser.go` | Add `-skip` flag parsing for apply command (gated) |
| `server/events/event_parser.go` | Add `SkipProject` field to `CommentCommand` |
| `server/core/db/db.go` | Add `SaveLayerState`, `GetLayerState`, `DeleteLayerState` to `Database` interface |
| `server/core/db/boltdb.go` (or equivalent) | Implement layer state persistence |
| `server/events/vcs/client.go` | Possibly no changes if using Option A for dashboard |

---

## 7. Detailed Code Changes

### 7.1 `server/user_config.go`

```go
// Add to UserConfig struct after EnableRegExpCmd:
EnableLayeredPlanning  bool `mapstructure:"enable-layered-planning"`
EnableLayeredApplySkip bool `mapstructure:"enable-layered-apply-skip"`
```

### 7.2 `cmd/server.go`

Add flag definitions (follow existing patterns, adjacent to other `enable-*` flags):

```go
{
    Name:         "enable-layered-planning",
    Description:  "Enable the layered plan/apply workflow for repos with depends_on. When enabled, projects are planned and applied in dependency order across layers.",
    DefaultValue: false,
},
{
    Name:         "enable-layered-apply-skip",
    Description:  "Enable the 'atlantis apply -skip' command for skipping failed projects in layered planning. Requires enable-layered-planning.",
    DefaultValue: false,
},
```

### 7.3 `server/events/plan_command_runner.go`

Add fields to `PlanCommandRunner`:

```go
type PlanCommandRunner struct {
    // ... existing fields ...
    layerManager       *LayerManager
    dashboardRenderer  *LayerDashboardRenderer
    database           db.Database  // for layer state persistence
}
```

Modify `NewPlanCommandRunner()` to accept and set these fields.

Modify `runAutoplan()`:

```go
func (p *PlanCommandRunner) runAutoplan(ctx *command.Context) {
    baseRepo := ctx.Pull.BaseRepo
    pull := ctx.Pull

    projectCmds, err := p.prjCmdBuilder.BuildAutoplanCommands(ctx)
    if err != nil { /* existing error handling */ }

    projectCmds, policyCheckCmds := p.partitionProjectCmds(ctx, projectCmds)

    if len(projectCmds) == 0 { /* existing no-projects handling */ }

    // NEW: Layered planning gate
    layered := p.layerManager != nil && p.layerManager.IsEnabledForProjectCmds(projectCmds)
    var layerState *models.LayerState

    if layered {
        layerState, err = p.layerManager.ComputeLayersFromProjectCmds(projectCmds)
        if err != nil {
            // Circular dependency or other graph error
            p.pullUpdater.updatePull(ctx, AutoplanCommand{}, command.Result{
                Error: fmt.Errorf("layered planning error: %w", err),
            })
            return
        }

        if !layerState.HasMultipleLayers() {
            layered = false  // fall through to normal flow
        }
    }

    if layered {
        projectCmds = p.layerManager.FilterToCurrentLayer(projectCmds, layerState)
        policyCheckCmds = p.layerManager.FilterToCurrentLayer(policyCheckCmds, layerState)
    }

    // Existing: delete plans, run plans, update pull, update DB
    ctx.Log.Debug("deleting previous plans and locks")
    p.deletePlans(ctx)
    _, err = p.lockingLocker.UnlockByPull(baseRepo.FullName, pull.Num)
    if err != nil {
        ctx.Log.Err("deleting locks: %s", err)
    }

    result := runProjectCmdsWithCancellationTracker(
        ctx, projectCmds, p.cancellationTracker,
        p.parallelPoolSize, p.isParallelEnabled(projectCmds),
        p.prjCmdRunner.Plan,
    )

    // ... existing automerge error handling ...

    p.pullUpdater.updatePull(ctx, AutoplanCommand{}, result)
    pullStatus, err := p.dbUpdater.updateDB(ctx, ctx.Pull, result.ProjectResults)
    if err != nil {
        ctx.Log.Err("writing results: %s", err)
    }

    // NEW: Update layer state and dashboard
    if layered {
        p.layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
        if err := p.database.SaveLayerState(pull, *layerState); err != nil {
            ctx.Log.Err("saving layer state: %s", err)
        }
        p.dashboardRenderer.UpdateDashboard(ctx.Log, baseRepo, pull.Num, layerState)
    }

    p.updateCommitStatus(ctx, pullStatus, command.Plan)
    p.updateCommitStatus(ctx, pullStatus, command.Apply)

    // Policy checks (existing, but filtered to current layer)
    if len(policyCheckCmds) > 0 && !result.HasErrors() && !result.PlansDeleted {
        ctx.PullStatus = &pullStatus
        p.policyCheckCommandRunner.Run(ctx, policyCheckCmds)
    }
}
```

Similar changes to `run()` for manual plan -- filter to current layer, update dashboard.

### 7.4 `server/events/apply_command_runner.go`

Add fields to `ApplyCommandRunner`:

```go
type ApplyCommandRunner struct {
    // ... existing fields ...
    layerManager           *LayerManager
    dashboardRenderer      *LayerDashboardRenderer
    planCmdRunner          ProjectPlanCommandRunner  // needed for next-layer planning
    projCmdContextBuilder  ProjectCommandContextBuilder  // needed to build plan contexts
}
```

Modify `Run()`:

```go
func (a *ApplyCommandRunner) Run(ctx *command.Context, cmd *CommentCommand) {
    // ... existing lock checks, DisableApplyAll check ...

    // NEW: Handle skip command
    if cmd.SkipProject != "" {
        a.handleSkip(ctx, cmd)
        return
    }

    // ... existing mergeability check ...

    projectCmds, err = a.prjCmdBuilder.BuildApplyCommands(ctx, cmd)
    if err != nil { /* existing */ }

    if len(projectCmds) == 0 && a.SilenceNoProjects { /* existing */ }

    // NEW: Layered planning gate
    layered := a.layerManager != nil && a.layerManager.IsEnabledForProjectCmds(projectCmds)
    var layerState *models.LayerState

    if layered {
        layerState, err = a.Database.GetLayerState(ctx.Pull)
        if err != nil {
            ctx.Log.Err("getting layer state: %s", err)
            layered = false
        } else if layerState == nil || !layerState.HasMultipleLayers() {
            layered = false
        }
    }

    if layered && !cmd.IsForSpecificProject() {
        // Apply all in current layer
        projectCmds = a.layerManager.FilterToCurrentLayer(projectCmds, layerState)
    } else if layered && cmd.IsForSpecificProject() {
        // Validate project is in current layer
        if !a.layerManager.IsInCurrentLayer(projectCmds[0], layerState) {
            errMsg := fmt.Sprintf(
                "**Error:** Project `%s` is not in the current layer (layer %d). "+
                    "Apply all projects in the current layer first.",
                projectCmds[0].ProjectName, layerState.CurrentLayer,
            )
            a.vcsClient.CreateComment(ctx.Log, baseRepo, pull.Num, errMsg, command.Apply.String())
            return
        }
    }

    // Existing: run applies
    result := runProjectCmdsWithCancellationTracker(...)
    ctx.CommandHasErrors = result.HasErrors()

    a.pullUpdater.updatePull(ctx, cmd, result)
    pullStatus, err := a.dbUpdater.updateDB(ctx, pull, result.ProjectResults)
    if err != nil { /* existing */ }

    // NEW: Layer state update and next-layer triggering
    if layered {
        a.layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
        a.Database.SaveLayerState(ctx.Pull, *layerState)

        if !result.HasErrors() && a.layerManager.IsLayerComplete(pullStatus, layerState) {
            a.advanceToNextLayer(ctx, layerState, pullStatus)
        } else {
            a.dashboardRenderer.UpdateDashboard(ctx.Log, baseRepo, pull.Num, layerState)
        }
    }

    a.updateCommitStatus(ctx, pullStatus)

    if a.autoMerger.automergeEnabled(projectCmds) && !cmd.AutoMergeDisabled {
        // Only automerge if ALL layers are complete
        if !layered || a.layerManager.AllLayersComplete(layerState) {
            a.autoMerger.automerge(ctx, pullStatus, ...)
        }
    }
}

func (a *ApplyCommandRunner) advanceToNextLayer(
    ctx *command.Context,
    layerState *models.LayerState,
    pullStatus models.PullStatus,
) {
    // Get next layer projects (applying cascade rules)
    nextLayerCfgs := a.layerManager.GetNextLayerProjects(layerState, pullStatus)

    if len(nextLayerCfgs) == 0 {
        // All done or cascade stopped
        a.dashboardRenderer.UpdateDashboard(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, layerState)
        return
    }

    layerState.CurrentLayer++

    // Build plan commands for next layer
    // (implementation details depend on having access to the repo config and working dir)
    planCmds := a.buildPlanCommandsForLayer(ctx, nextLayerCfgs)

    // Run plans
    result := runProjectCmdsWithCancellationTracker(
        ctx, planCmds, a.cancellationTracker,
        a.parallelPoolSize, true, // parallel
        a.planCmdRunner.Plan,
    )

    // Post plan results
    a.pullUpdater.updatePull(ctx, &CommentCommand{Name: command.Plan}, result)
    pullStatus, _ = a.dbUpdater.updateDB(ctx, ctx.Pull, result.ProjectResults)

    // Update layer state
    a.layerManager.UpdateLayerStateFromResults(layerState, result.ProjectResults)
    a.Database.SaveLayerState(ctx.Pull, *layerState)
    a.dashboardRenderer.UpdateDashboard(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, layerState)

    // If all planned with no changes, auto-advance again (recursive)
    if a.layerManager.IsLayerComplete(pullStatus, layerState) {
        a.advanceToNextLayer(ctx, layerState, pullStatus)
    }
}

func (a *ApplyCommandRunner) handleSkip(ctx *command.Context, cmd *CommentCommand) {
    if !a.layerManager.EnableLayeredApplySkip {
        a.vcsClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num,
            "**Error:** The `--skip` flag is not enabled. Set `--enable-layered-apply-skip` to enable.",
            command.Apply.String())
        return
    }

    layerState, err := a.Database.GetLayerState(ctx.Pull)
    if err != nil || layerState == nil {
        a.vcsClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num,
            "**Error:** No active layered planning session for this PR.",
            command.Apply.String())
        return
    }

    project := a.layerManager.FindProjectInCurrentLayer(layerState, cmd.SkipProject)
    if project == nil {
        a.vcsClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num,
            fmt.Sprintf("**Error:** Project `%s` is not in the current layer.", cmd.SkipProject),
            command.Apply.String())
        return
    }

    if project.Status != models.LayerProjectApplyFailed {
        a.vcsClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num,
            fmt.Sprintf("**Error:** Can only skip projects with failed applies. Project `%s` status: %s",
                cmd.SkipProject, project.Status),
            command.Apply.String())
        return
    }

    // Mark as skipped
    project.Status = models.LayerProjectSkipped
    a.Database.SaveLayerState(ctx.Pull, *layerState)

    // Post confirmation comment
    a.vcsClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num,
        fmt.Sprintf("Project `%s` marked as skipped. Downstream dependents will be excluded.", cmd.SkipProject),
        command.Apply.String())

    // Check if layer is now complete
    pullStatus, _ := a.Database.GetPullStatus(ctx.Pull)
    if pullStatus != nil && a.layerManager.IsLayerComplete(*pullStatus, layerState) {
        a.advanceToNextLayer(ctx, layerState, *pullStatus)
    } else {
        a.dashboardRenderer.UpdateDashboard(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, layerState)
    }
}
```

### 7.5 `server/events/comment_parser.go`

Add skip flag support:

```go
type CommentParser struct {
    // ... existing fields ...
    EnableLayeredApplySkip bool  // NEW
}
```

In `Parse()`, inside the `command.Apply.String()` case:

```go
case command.Apply.String():
    name = command.Apply
    flagSet = pflag.NewFlagSet(command.Apply.String(), pflag.ContinueOnError)
    flagSet.SetOutput(io.Discard)
    flagSet.StringVarP(&workspace, workspaceFlagLong, workspaceFlagShort, "", "...")
    flagSet.StringVarP(&dir, dirFlagLong, dirFlagShort, "", "...")
    flagSet.StringVarP(&project, projectFlagLong, projectFlagShort, "", "...")
    flagSet.BoolVarP(&autoMergeDisabled, autoMergeDisabledFlagLong, autoMergeDisabledFlagShort, false, "...")
    flagSet.StringVarP(&autoMergeMethod, autoMergeMethodFlagLong, autoMergeMethodFlagShort, "", "...")
    flagSet.BoolVarP(&verbose, verboseFlagLong, verboseFlagShort, false, "...")
    // NEW:
    if e.EnableLayeredApplySkip {
        flagSet.StringVar(&skipProject, "skip", "", "Skip a failed project in the current layer (layered planning only)")
    }
```

### 7.6 `server/events/event_parser.go`

Add `SkipProject` to `CommentCommand`:

```go
type CommentCommand struct {
    // ... existing fields ...
    // SkipProject is the name of a project to skip in layered planning.
    // Only valid for apply commands when layered apply skip is enabled.
    SkipProject string
}
```

Update `NewCommentCommand()` to accept and set `skipProject`.

### 7.7 `server/server.go`

Wire the new flags and components:

```go
// After creating the comment parser:
commentParser := &events.CommentParser{
    // ... existing fields ...
    EnableLayeredApplySkip: userConfig.EnableLayeredApplySkip,
}

// Create layer manager (nil if disabled):
var layerManager *events.LayerManager
var dashboardRenderer *events.LayerDashboardRenderer
if userConfig.EnableLayeredPlanning {
    layerManager = &events.LayerManager{
        EnableLayeredPlanning:  true,
        EnableLayeredApplySkip: userConfig.EnableLayeredApplySkip,
    }
    dashboardRenderer = &events.LayerDashboardRenderer{
        VCSClient: vcsClient,
    }
}

// Pass to command runners:
planCommandRunner := events.NewPlanCommandRunner(
    // ... existing args ...
    layerManager,       // NEW
    dashboardRenderer,  // NEW
    database,           // NEW (for layer state)
)

applyCommandRunner := events.NewApplyCommandRunner(
    // ... existing args ...
    layerManager,                // NEW
    dashboardRenderer,           // NEW
    persistingProjectCmdRunner,  // NEW (for next-layer planning)
)
```

---

## 8. Layer Computation Algorithm

Located in `server/events/layer_manager.go`:

```go
func (lm *LayerManager) ComputeLayersFromProjectCmds(cmds []command.ProjectContext) (*models.LayerState, error) {
    // 1. Build adjacency map from DependsOn
    //    key: project identifier (name or dir/workspace)
    //    value: list of upstream dependencies

    // 2. Build reverse map (downstream dependents)

    // 3. Detect cycles using DFS (return error if found)

    // 4. Topological sort into layers:
    //    - Layer 0: projects with no in-scope dependencies
    //    - Layer N+1: projects whose dependencies are all in layers <= N

    // 5. If only 1 layer, set HasMultipleLayers = false

    // Return LayerState with all projects assigned to layers,
    // CurrentLayer = 0
}
```

Project identity for dependency matching: The `depends_on` field in `atlantis.yaml` references project names (if named) or dir paths. The layer manager resolves these references against the project configs.

---

## 9. Dashboard Rendering

Located in `server/events/layer_dashboard.go`:

```go
func (r *LayerDashboardRenderer) RenderDashboard(layerState *models.LayerState) string {
    var sb strings.Builder
    sb.WriteString("## Layered Planning Dashboard\n\n")

    for _, layer := range layerState.Layers {
        if layer.Index < layerState.CurrentLayer {
            // Completed layer -- collapsed
            applied := countByStatus(layer.Projects, LayerProjectApplied)
            sb.WriteString(fmt.Sprintf(
                "**Layer %d** - %d projects applied\n"+
                "<details><summary>Show projects</summary>\n\n", layer.Index+1, applied))
            for _, p := range layer.Projects {
                sb.WriteString(fmt.Sprintf("- %s %s\n", statusEmoji(p.Status), p.ProjectName))
            }
            sb.WriteString("\n</details>\n\n")
        } else if layer.Index == layerState.CurrentLayer {
            // Current layer -- expanded
            sb.WriteString(fmt.Sprintf("**Layer %d** (current)\n---\n", layer.Index+1))
            for _, p := range layer.Projects {
                sb.WriteString(fmt.Sprintf("- %s %s\n", statusEmoji(p.Status), formatProjectStatus(p)))
            }
            sb.WriteString("\n")
        }
        // Future layers are not shown (just counted)
    }

    // Pending count
    pending := countPendingProjects(layerState)
    if pending > 0 {
        sb.WriteString(fmt.Sprintf("\n%d projects pending\n", pending))
    }

    return sb.String()
}
```

Dashboard comment management:

```go
func (r *LayerDashboardRenderer) UpdateDashboard(
    logger logging.SimpleLogging,
    repo models.Repo,
    pullNum int,
    layerState *models.LayerState,
) error {
    if !layerState.HasMultipleLayers() {
        return nil  // No dashboard for single-layer PRs
    }

    // Hide previous dashboard comments
    if err := r.VCSClient.HidePrevCommandComments(
        logger, repo, pullNum, "Layered Planning Dashboard", "",
    ); err != nil {
        logger.Warn("unable to hide previous dashboard comments: %s", err)
    }

    // Create new dashboard comment
    markdown := r.RenderDashboard(layerState)
    return r.VCSClient.CreateComment(logger, repo, pullNum, markdown, "layered-dashboard")
}
```

### Comment Splitting

For very large PRs, the dashboard may exceed VCS character limits:

```go
const maxCommentLength = 60000  // Leave margin below GitHub's 65536

func (r *LayerDashboardRenderer) UpdateDashboard(...) error {
    markdown := r.RenderDashboard(layerState)

    if len(markdown) <= maxCommentLength {
        // Single comment
        return r.VCSClient.CreateComment(...)
    }

    // Split at layer boundaries
    parts := r.splitAtLayerBoundary(markdown, maxCommentLength)
    for _, part := range parts {
        if err := r.VCSClient.CreateComment(logger, repo, pullNum, part, "layered-dashboard"); err != nil {
            return err
        }
    }
    return nil
}
```

---

## 10. Interaction with Existing Features

### 10.1 ExecutionOrderGroup

When layered planning is active, it **supersedes** `ExecutionOrderGroup`. The layer assignment from `depends_on` takes precedence. Projects within the same layer can still use `ExecutionOrderGroup` to control their relative execution order within that layer.

This means `splitByExecutionOrderGroup()` in `project_command_pool_executor.go` continues to work as-is within each layer. No changes needed there.

### 10.2 Policy Checks

Policy checks run after planning for each layer. The `partitionProjectCmds()` method in `PlanCommandRunner` already separates plan and policy check commands. When layered, policy check commands are also filtered to the current layer.

### 10.3 Automerge

Automerge should only trigger when ALL layers are complete. The `ApplyCommandRunner` checks `layerManager.AllLayersComplete(layerState)` before calling automerge.

### 10.4 HidePrevPlanComments

The existing `HidePrevPlanComments` functionality works per-command. Dashboard comments use a different command tag (`"layered-dashboard"`) so they won't conflict.

### 10.5 DisableApplyAll

When `disable-apply-all` is set AND layered planning is active, `atlantis apply` with no args should still be blocked. The check happens before layer filtering. Users must use `-d` or `-p` to apply specific projects within the current layer.

### 10.6 PR Close / Unlock

When a PR is closed or `atlantis unlock` is run, the layer state should be cleaned up. Add to the pull cleanup path:

```go
// In the pull cleanup handler:
database.DeleteLayerState(pull)
```

---

## 11. Testing Approach

**This workstream uses test-alongside.** Write implementation and tests together, following existing codebase patterns.

- **Framework:** Standard Go testing with custom assertions from `testing/` package
- **Mocks:** Pegomock for all dependencies — follows existing patterns in `plan_command_runner_test.go` and `apply_command_runner_test.go`
- **Unit tests:** Mock the graph calculator, state manager, dashboard updater, and VCS client to test lifecycle orchestration logic in isolation
- **Orchestrator integration test:** Wire real graph calculator, state manager, and dashboard renderer with mocked VCS and Terraform to test full layered lifecycle coordination (plan L0 → apply → auto-plan L1 → apply → done)
- **E2E test:** One happy-path test in `events_controller_e2e_test.go` with a new `testdata/test-repos/layered-planning/` fixture using real Terraform. Validates: autoplan triggers only layer 0 → apply → layer 1 auto-plans → apply → layer 2 auto-plans → apply → done. Dashboard comment appears and updates correctly.

## 12. Test Cases

### 12.1 Unit Tests for LayerManager

| Test | Description |
|---|---|
| `TestComputeLayers_NoDependsOn` | All projects land in layer 0, `HasMultipleLayers` is false |
| `TestComputeLayers_LinearChain` | A -> B -> C produces 3 layers |
| `TestComputeLayers_DiamondDependency` | A -> B, A -> C, B -> D, C -> D produces 3 layers |
| `TestComputeLayers_CircularDependency` | A -> B -> A returns error |
| `TestComputeLayers_MixedDepsAndNoDeps` | Projects with and without deps, correct layer assignment |
| `TestFilterToCurrentLayer` | Only projects in layer 0 are returned |
| `TestFilterToCurrentLayer_Layer1` | After layer 0 complete, only layer 1 projects returned |
| `TestIsLayerComplete_AllApplied` | Returns true when all projects applied |
| `TestIsLayerComplete_HasNoChanges` | No-changes projects count as complete |
| `TestIsLayerComplete_HasFailed` | Returns false when any project has failed apply |
| `TestIsLayerComplete_HasSkipped` | Skipped projects count as complete |
| `TestGetNextLayerProjects_CascadeRule` | Only plans downstream of projects with actual changes |
| `TestGetNextLayerProjects_NoCascade` | When upstream had no changes, downstream is excluded |
| `TestGetNextLayerProjects_SkippedUpstream` | Downstream of skipped projects is excluded |
| `TestIsEnabled_FlagOff` | Returns false when flag is disabled |
| `TestIsEnabled_NoDependsOn` | Returns false when no projects have depends_on |
| `TestIsEnabled_HasDependsOn` | Returns true when flag on and projects have depends_on |

### 11.2 Unit Tests for PlanCommandRunner (Layered)

| Test | Description |
|---|---|
| `TestAutoplan_LayeredEnabled_MultiLayer` | Only layer 0 projects are planned, dashboard created |
| `TestAutoplan_LayeredEnabled_SingleLayer` | All projects planned (no layer filtering), no dashboard |
| `TestAutoplan_LayeredDisabled` | Existing behavior unchanged |
| `TestManualPlan_LayeredEnabled` | Re-plans current layer only |
| `TestManualPlan_SpecificProject` | Plans specific project regardless of layer |
| `TestAutoplan_CircularDependency` | Error comment posted |

### 11.3 Unit Tests for ApplyCommandRunner (Layered)

| Test | Description |
|---|---|
| `TestApplyAll_LayeredEnabled` | Only current layer projects applied |
| `TestApplyAll_LayerComplete_TriggersNextPlan` | After all applied, next layer is planned |
| `TestApplyAll_LayerComplete_AllNoChanges_AutoAdvances` | Cascading through no-change layers |
| `TestApplyAll_HasErrors_BlocksAdvancement` | Failed apply prevents layer advancement |
| `TestApplySingle_InCurrentLayer` | Single project apply succeeds |
| `TestApplySingle_NotInCurrentLayer` | Error message posted |
| `TestApplySingle_CompletesLayer` | Single apply completes layer, triggers next |
| `TestApplySkip_Enabled_FailedProject` | Skip succeeds, layer advances |
| `TestApplySkip_Enabled_NotFailed` | Error: can only skip failed |
| `TestApplySkip_Disabled` | Error: feature not enabled |
| `TestApplySkip_CompletesLayer` | Skip completes layer, triggers next (excluding downstream) |
| `TestApply_AllLayersComplete_Automerge` | Automerge triggers only when all done |
| `TestApply_DisableApplyAll_WithLayered` | DisableApplyAll still blocks `atlantis apply` |

### 11.4 Unit Tests for Dashboard Renderer

| Test | Description |
|---|---|
| `TestRenderDashboard_SingleLayer` | No dashboard rendered |
| `TestRenderDashboard_TwoLayers` | Layer 1 expanded, pending count shown |
| `TestRenderDashboard_CompletedLayer` | Completed layers are collapsed |
| `TestRenderDashboard_AllStatuses` | All status emojis render correctly |
| `TestRenderDashboard_CommentSplitting` | Large dashboards split correctly |

### 11.5 Unit Tests for Comment Parser

| Test | Description |
|---|---|
| `TestParse_ApplySkip_Enabled` | `atlantis apply --skip project-name` parsed correctly |
| `TestParse_ApplySkip_Disabled` | `--skip` flag rejected when disabled |
| `TestParse_ApplySkip_WithOtherFlags` | Skip cannot be used with -d/-p/-w |

### 11.6 Integration / E2E Tests

| Test | Description |
|---|---|
| `TestE2E_LayeredPlanApply_TwoLayers` | Full flow: autoplan layer 0, apply, auto-plan layer 1, apply |
| `TestE2E_LayeredPlanApply_NoCascade` | Layer 0 has no changes, layer 1 excluded |
| `TestE2E_LayeredPlanApply_Skip` | Apply fail, skip, advance, downstream excluded |
| `TestE2E_LayeredPlanApply_NewPush` | New commits re-plan current layer |

---

## 12. Risks and Open Questions

### 12.1 Risks

| Risk | Mitigation |
|---|---|
| **Recursive auto-advance** could stack overflow for very deep dependency chains | Add a max recursion depth (e.g., 100 layers). In practice, dependency chains beyond 10 are extremely rare. |
| **Race condition** if multiple webhooks arrive simultaneously for the same PR | The existing `WorkingDirLocker` provides per-PR locking. Layer state reads/writes should happen within this lock scope. |
| **BoltDB layer state growth** for large PRs with many projects | Layer state is small (project names + statuses). Even 500 projects would be well under 1MB. Clean up on PR close. |
| **Dashboard comment flickering** (hide + create looks like delete/recreate) | This is acceptable for Option A. Option B (UpdateComment) eliminates flickering but requires more work. |
| **Next-layer planning needs access to working directory** which may not be checked out | The `advanceToNextLayer()` method runs in the same request as the apply. The working directory should still be available. If not, it needs to re-clone, which the existing `WorkingDir.Clone()` handles. |

### 12.2 Open Questions

1. **Should `atlantis plan -d <dir>` for a project in a future layer be allowed?**
   - Recommendation: Allow it but post a warning. The plan output may be inaccurate since upstream hasn't been applied yet.

2. **How should `atlantis apply` interact with `abort_on_execution_order_fail`?**
   - Recommendation: Within a layer, `abort_on_execution_order_fail` works as today (stops execution of remaining groups within the layer). Layer advancement is a separate concern and is blocked by any apply failure regardless of this flag.

3. **What happens when a new push arrives while a layer is partially applied?**
   - Per the design doc: Affected projects in the current layer are re-planned. Already-applied projects in the current layer are NOT re-planned. This requires checking which projects in the current layer have already been applied and excluding them from re-planning.
   - Implementation: In `runAutoplan()`, when layered planning is active and there's an existing `layerState`, compare changed files against layer assignments. If changes affect an already-applied layer, that's the "reset" case described in the design doc. This is a significant complexity that may warrant a separate implementation phase.

4. **Should the `-skip` flag use project name, dir, or either?**
   - Recommendation: Accept project name (via `-p` style resolution). If the project has no name, accept the dir path. The comment parser already handles this pattern.

5. **How does layered planning interact with `parallel_plan: false` / `parallel_apply: false`?**
   - Within a layer, the existing parallel/serial behavior applies. Layers are always sequential (layer N must complete before layer N+1 starts). No conflict.

6. **Dashboard: should it show future layer projects?**
   - Per the design doc: Show only a count ("85 projects pending"), not individual project names. Future layer membership may change as cascade rules are evaluated.

---

## 13. Implementation Order

Recommended implementation sequence:

1. **Phase 1: Layer computation** (`layer_manager.go`)
   - `ComputeLayers`, `FilterToCurrentLayer`, `IsEnabled`
   - Unit tests for all graph scenarios

2. **Phase 2: Server flags and wiring** (`user_config.go`, `cmd/server.go`, `server.go`)
   - Add flags, create and inject `LayerManager`

3. **Phase 3: Autoplan integration** (`plan_command_runner.go`)
   - Layer filtering in `runAutoplan()`
   - Layer state persistence

4. **Phase 4: Dashboard** (`layer_dashboard.go`)
   - Rendering and comment management

5. **Phase 5: Apply integration** (`apply_command_runner.go`)
   - Layer filtering, next-layer triggering
   - `advanceToNextLayer()` method

6. **Phase 6: Manual plan** (`plan_command_runner.go`)
   - Layer filtering in `run()`

7. **Phase 7: Skip command** (`comment_parser.go`, `apply_command_runner.go`)
   - Flag parsing, skip handling

8. **Phase 8: Edge cases**
   - Comment splitting, new-push handling, PR close cleanup

NOT FOUND
