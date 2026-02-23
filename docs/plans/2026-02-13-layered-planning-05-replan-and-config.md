# Layered Planning: Smart Re-plan on Push & Server Configuration

Implementation plan for workstream 5 of [Layered Planning](./2026-02-13-layered-planning-design.md).

## Overview

Two pieces:

**Part A:** When new commits are pushed to a PR, intelligently determine what needs re-planning based on which layer the affected projects belong to, instead of resetting everything.

**Part B:** Two new server-side flags (`enable-layered-planning` and `enable-layered-apply-skip`) that gate the entire layered workflow.

---

## Prerequisites / Dependencies

This plan assumes that the following are already implemented by other workstreams:

1. **Layer state model** (`LayerState`): A data structure that tracks which layer each project belongs to, what the current layer is, and each project's status within its layer (planned, applied, no-changes, errored, skipped). This is persisted via the database.
2. **Dependency graph builder**: A function that takes a `valid.RepoCfg` (specifically the `DependsOn` fields) and produces a topologically sorted layer assignment for all in-scope projects.
3. **Layered plan command runner**: The plan command runner that respects layers (plans only the current layer, advances after apply).
4. **Dashboard comment renderer**: The sticky comment that shows layer progress.

This workstream builds on top of those primitives.

---

## Part A: Smart Re-plan on New Commits

### Current Flow (How Push Events Work Today)

1. A push event (new commits on a PR branch) arrives at `server/controllers/events/events_controller.go`.
2. The controller maps it to `models.UpdatedPullEvent` (line 588).
3. It calls `e.CommandRunner.RunAutoplanCommand(baseRepo, headRepo, pull, user)` (line 595/598).
4. `RunAutoplanCommand` in `server/events/command_runner.go` (line 139) creates a `CommentCommand{Name: command.Autoplan}` and invokes the plan command runner.
5. The plan command runner calls `projectCommandBuilder.BuildAutoplanCommands(ctx)` which:
   - Gets modified files via `VCSClient.GetModifiedFiles()` (line 469 of `project_command_builder.go`)
   - Clones the repo
   - Parses `atlantis.yaml`
   - Uses `ProjectFinder.DetermineProjectsViaConfig()` to match modified files to projects
   - Returns `[]command.ProjectContext` for all matching projects with autoplan enabled
6. All matched projects get planned simultaneously.

### Problem

With layered planning, a push should not blindly re-plan everything. The correct behavior depends on where each affected project sits relative to layer state:

| Scenario | Action |
|----------|--------|
| Affected project is in a **future layer** (not yet planned) | Do nothing -- it will be planned with new code when its turn comes |
| Affected project is in the **current layer** (planned but not applied) | Re-plan that project only within the current layer |
| Affected project is in an **already-applied layer** | Reset back to that layer; invalidate everything from that layer forward |

### Algorithm: Determining Re-plan Scope on Push

```
func DetermineReplanScope(
    modifiedProjects []string,    // project names/dirs affected by the push
    layerState *LayerState,       // current persistent layer state for this PR
) ReplanDecision

type ReplanDecision struct {
    // Projects in the current layer that need re-planning
    ReplanProjects []string

    // If non-nil, we need to reset to this layer number.
    // All layers >= ResetToLayer are invalidated.
    ResetToLayer *int

    // Projects in future layers -- no action needed (logged for debugging)
    SkippedFutureProjects []string
}
```

**Step-by-step algorithm:**

```
1. Look up the current layer state for this PR from the database.
   - If no layer state exists (layered planning not active for this PR),
     fall through to normal autoplan behavior.

2. For each project affected by the push:
   a. Find which layer this project belongs to.
   b. Compare against the current active layer:

      - If project.layer > currentLayer:
          -> Add to SkippedFutureProjects (no action)

      - If project.layer == currentLayer:
          -> If project status is NOT Applied:
              Add to ReplanProjects
          -> If project status IS Applied:
              This is problematic -- an already-applied project in the
              current layer has new code. Treat as a reset scenario:
              set ResetToLayer = currentLayer.

      - If project.layer < currentLayer:
          -> This project was in an already-completed layer.
             Set ResetToLayer = min(ResetToLayer, project.layer)

3. Return the ReplanDecision.
```

### How Layer State Is Reset / Rolled Back

When `ResetToLayer` is set (say to layer N):

```
func ResetLayerState(layerState *LayerState, resetToLayer int) {
    1. For every project in layer >= resetToLayer:
       - Clear its plan status (set to "pending" / unplanned)
       - Delete any stored plan files from the working directory
       - Remove any plan locks held by these projects

    2. Set layerState.CurrentLayer = resetToLayer

    3. Mark all projects in layers < resetToLayer as unchanged
       (they remain Applied)

    4. Persist the updated layer state to the database

    5. The caller then triggers planning for the reset layer,
       which follows the normal layered plan flow.
}
```

**Deleting plan files:** Use the existing `WorkingDir` methods. Plan files live in the pull's working directory under paths like `{workspace}/{dir}/{planfile}`. The `PendingPlanFinder` already knows how to find them; we need the inverse operation.

**Removing locks:** Use `database.Unlock(project, workspace)` for each invalidated project. The existing `UnlockByPull` method could be used for a full reset, but for selective reset we need per-project unlock.

### Modified Files

The existing `project_command_builder.go` already does the heavy lifting of matching modified files to projects:

1. `VCSClient.GetModifiedFiles()` returns files changed in the PR (not just the latest push -- it's all files different from base).
2. `ProjectFinder.DetermineProjectsViaConfig()` matches those files against project `when_modified` patterns.

For the re-plan scope determination, we need the **incremental** modified files (just from the new push), not all PR files. However, `GetModifiedFiles` returns the full PR diff. Two approaches:

**Option A (Recommended): Use full PR modified files.** Since `DetermineProjectsViaConfig` already works with the full file list, we get all affected projects and then filter by layer state. This is simpler and correct -- even if a file was modified in a previous commit, re-planning with the latest code is the right thing to do.

**Option B: Compute incremental diff.** We would need the before/after SHAs from the push event and diff just those. This is more complex and not necessary for correctness.

**Decision: Option A.** The existing `GetModifiedFiles` output is sufficient. We determine all affected projects, then apply the layer-aware filtering.

### Files to Create / Modify

#### New Files

1. **`server/events/layered_replan.go`** -- Core re-plan logic

```go
package events

// ReplanDecision describes what should happen when new commits are pushed
// to a PR with active layered planning.
type ReplanDecision struct {
    // ReplanProjects are projects in the current layer that need re-planning.
    ReplanProjects []string
    // ResetToLayer, if non-nil, means we must reset to this layer number.
    // All state from this layer forward is invalidated.
    ResetToLayer *int
    // SkippedFutureProjects are in future layers; no action needed.
    SkippedFutureProjects []string
}

// DetermineReplanScope analyzes which projects are affected by a push
// and decides what action to take based on layer state.
func DetermineReplanScope(
    affectedProjects []AffectedProject,
    layerState *LayerState,
) ReplanDecision { ... }

// AffectedProject pairs a project identifier with its layer assignment.
type AffectedProject struct {
    ProjectName string
    RepoRelDir  string
    Workspace   string
    Layer       int
}

// ResetLayerState rolls back the layer state to the specified layer,
// clearing all plan state for that layer and all subsequent layers.
func ResetLayerState(
    layerState *LayerState,
    resetToLayer int,
    database db.Database,
    workingDir WorkingDir,
    pull models.PullRequest,
) error { ... }
```

2. **`server/events/layered_replan_test.go`** -- Unit tests for re-plan logic

#### Modified Files

3. **`server/events/plan_command_runner.go`** -- The `PlanCommandRunner.Run()` method (or a parallel method for autoplan) needs a new code path when layered planning is active.

Current flow in `run()` for autoplan:
```go
projectCmds, err := p.prjCmdBuilder.BuildAutoplanCommands(ctx)
// ... run all project commands
```

New flow when layered planning is enabled:
```go
func (p *PlanCommandRunner) run(ctx *command.Context, cmd *CommentCommand) {
    // ... existing setup ...

    if p.layeredPlanningEnabled && isAutoplan(cmd) {
        layerState, err := p.layerStateStore.GetLayerState(ctx.Pull)
        if err != nil { ... }

        if layerState != nil && layerState.IsActive() {
            // This is a push to an existing layered PR
            p.handleLayeredReplan(ctx, cmd, layerState)
            return
        }
    }

    // ... existing non-layered flow ...
}

func (p *PlanCommandRunner) handleLayeredReplan(
    ctx *command.Context,
    cmd *CommentCommand,
    layerState *LayerState,
) {
    // 1. Build the full set of affected projects (same as today)
    allProjectCmds, err := p.prjCmdBuilder.BuildAutoplanCommands(ctx)

    // 2. Determine re-plan scope
    affectedProjects := mapToAffectedProjects(allProjectCmds, layerState)
    decision := DetermineReplanScope(affectedProjects, layerState)

    // 3. Handle reset if needed
    if decision.ResetToLayer != nil {
        err := ResetLayerState(layerState, *decision.ResetToLayer,
            p.Database, p.workingDir, ctx.Pull)
        if err != nil { ... }

        // After reset, plan the reset-to layer (all projects in it)
        layerProjects := layerState.ProjectsInLayer(*decision.ResetToLayer)
        projectCmds := filterProjectCmds(allProjectCmds, layerProjects)
        // ... run plans for these projects ...
        return
    }

    // 4. Handle re-plan of current layer projects
    if len(decision.ReplanProjects) > 0 {
        projectCmds := filterProjectCmds(allProjectCmds, decision.ReplanProjects)
        // ... run plans for these projects only ...
        return
    }

    // 5. Nothing to do (all affected projects are in future layers)
    ctx.Log.Info("Push affects only future-layer projects; no re-plan needed")
}
```

Key fields to add to `PlanCommandRunner`:
```go
type PlanCommandRunner struct {
    // ... existing fields ...
    layeredPlanningEnabled bool
    layerStateStore        LayerStateStore  // interface for layer state persistence
}
```

4. **`server/events/command_runner.go`** -- `RunAutoplanCommand` needs awareness of layered mode. Currently it always sets up a standard plan. The change is minimal -- the layered logic is encapsulated in `PlanCommandRunner`.

However, `RunAutoplanCommand` does set the plan command to `command.Autoplan`. This distinction is important because we use it to know this was triggered by a push (not a manual `atlantis plan` comment). No structural change needed here; the `PlanCommandRunner` will check `cmd.Name == command.Autoplan` to distinguish push-triggered vs manual re-plans.

5. **`server/events/project_command_builder.go`** -- May need a new method or modification to `BuildAutoplanCommands` to support building commands for a subset of projects (only those in a specific layer).

New method:
```go
// BuildAutoplanCommandsForProjects builds plan commands for a specific
// subset of projects (identified by name or dir+workspace).
func (p *DefaultProjectCommandBuilder) BuildAutoplanCommandsForProjects(
    ctx *command.Context,
    projectFilter []ProjectIdentifier,
) ([]command.ProjectContext, error) {
    // Similar to BuildAutoplanCommands but filters results to only
    // include projects matching the filter.
    projCtxs, err := p.buildAllCommandsByCfg(ctx, command.Plan, "", nil, false)
    if err != nil {
        return nil, err
    }
    return filterByIdentifiers(projCtxs, projectFilter), nil
}

type ProjectIdentifier struct {
    ProjectName string
    RepoRelDir  string
    Workspace   string
}
```

Alternatively, the filtering can happen in `PlanCommandRunner.handleLayeredReplan` after calling `BuildAutoplanCommands`, which is simpler and does not require modifying the builder interface. **Recommendation: filter after building.** This avoids interface changes and the cost of building extra project contexts is minimal.

6. **`server/events/project_finder.go`** -- No changes needed. The existing `DetermineProjectsViaConfig` and `DetermineProjects` methods already handle file-to-project matching correctly.

### Integration with Layer State Store

The re-plan logic needs to read and write layer state. This requires a `LayerStateStore` interface (assumed to be defined by another workstream):

```go
type LayerStateStore interface {
    GetLayerState(pull models.PullRequest) (*LayerState, error)
    SaveLayerState(pull models.PullRequest, state *LayerState) error
}
```

The `LayerState` struct (defined elsewhere) must support:
- `IsActive() bool` -- whether layered planning is in progress
- `CurrentLayer() int` -- the current active layer number
- `ProjectLayer(projectName string) int` -- which layer a project belongs to
- `ProjectStatus(projectName string) ProjectLayerStatus` -- status within its layer
- `ProjectsInLayer(layer int) []string` -- all projects in a given layer
- `InvalidateFromLayer(layer int)` -- clear state for layer N and beyond

### Sequence Diagram: Push to PR with Active Layers

```
Push Event
    |
    v
EventsController.handlePullRequestEvent()
    |
    v
CommandRunner.RunAutoplanCommand()
    |
    v
PlanCommandRunner.run()
    |
    +-- Is layered planning enabled? No --> normal flow
    |
    +-- Yes: Get layer state from DB
    |
    +-- Layer state exists and active?
    |       |
    |       No --> normal layered plan (initial layer 0 planning)
    |       |
    |       Yes --> handleLayeredReplan()
    |                   |
    |                   +-- BuildAutoplanCommands() (get all affected projects)
    |                   +-- DetermineReplanScope() (classify by layer)
    |                   |
    |                   +-- ResetToLayer set?
    |                   |       |
    |                   |       Yes --> ResetLayerState()
    |                   |               Plan all projects in reset layer
    |                   |
    |                   |       No --> ReplanProjects non-empty?
    |                   |               |
    |                   |               Yes --> Plan only those projects
    |                   |               |
    |                   |               No --> Log "no re-plan needed"
    |                   |
    |                   +-- Update dashboard comment
    |                   +-- Update commit statuses
```

---

## Part B: Server Configuration

### Flag Definitions

Two new boolean flags, following the exact pattern of existing flags like `ParallelPlan` and `EnablePolicyChecks`.

#### 1. Flag constants in `cmd/server.go`

Add to the `const` block (around line 50-130):

```go
EnableLayeredPlanningFlag    = "enable-layered-planning"
EnableLayeredApplySkipFlag   = "enable-layered-apply-skip"
```

#### 2. Flag descriptions in `cmd/server.go`

Add to the `boolFlags` map (around line 500-650):

```go
EnableLayeredPlanningFlag: {
    description:  "Enable layered plan/apply workflow for repositories with depends_on configured. When enabled, projects are planned and applied in dependency order across layers.",
    defaultValue: false,
},
EnableLayeredApplySkipFlag: {
    description:  "Enable the 'atlantis apply -skip' command to skip failed applies in the current layer. Requires enable-layered-planning to also be set.",
    defaultValue: false,
},
```

#### 3. UserConfig struct in `server/user_config.go`

Add two fields (alphabetically near existing `Enable*` fields, around line 49-52):

```go
EnableLayeredPlanning    bool `mapstructure:"enable-layered-planning"`
EnableLayeredApplySkip   bool `mapstructure:"enable-layered-apply-skip"`
```

### Flag Propagation Path

Tracing how `EnableParallelPlan` flows through the system as the reference pattern:

```
cmd/server.go (flag definition)
    --> server/user_config.go (UserConfig.ParallelPlan)
        --> server/server.go (userConfig.ParallelPlan passed to constructors)
            --> server/events/project_command_builder.go (DefaultProjectCommandBuilder.EnableParallelPlan)
                --> command.ProjectContext.ParallelPlanEnabled
```

The new flags follow a similar but slightly different path because they affect the command runners, not the project context builder:

```
cmd/server.go (flag definition)
    --> server/user_config.go (UserConfig.EnableLayeredPlanning, UserConfig.EnableLayeredApplySkip)
        --> server/server.go (passed to PlanCommandRunner and ApplyCommandRunner constructors)
            --> PlanCommandRunner.layeredPlanningEnabled (controls plan behavior)
            --> ApplyCommandRunner.layeredApplySkipEnabled (controls skip command)
```

### Files to Modify

#### `cmd/server.go`

1. Add `EnableLayeredPlanningFlag` and `EnableLayeredApplySkipFlag` constants to the `const` block.
2. Add entries to the `boolFlags` map with descriptions and `defaultValue: false`.

#### `server/user_config.go`

Add two fields to the `UserConfig` struct:
```go
EnableLayeredPlanning    bool `mapstructure:"enable-layered-planning"`
EnableLayeredApplySkip   bool `mapstructure:"enable-layered-apply-skip"`
```

#### `server/server.go`

Pass the new config values to the command runners that need them. Specifically:

1. When constructing `PlanCommandRunner` (around line 786):
```go
planCommandRunner := events.NewPlanCommandRunner(
    // ... existing params ...
    userConfig.EnableLayeredPlanning,  // new param
)
```

2. When constructing `ApplyCommandRunner` (around line 809):
```go
applyCommandRunner := events.NewApplyCommandRunner(
    // ... existing params ...
    userConfig.EnableLayeredPlanning,    // new param
    userConfig.EnableLayeredApplySkip,   // new param
)
```

#### `server/events/plan_command_runner.go`

1. Add `layeredPlanningEnabled bool` field to `PlanCommandRunner` struct.
2. Add corresponding parameter to `NewPlanCommandRunner()`.
3. Add the layered re-plan check at the top of the `run()` method.

#### `server/events/apply_command_runner.go`

1. Add `layeredPlanningEnabled bool` field.
2. Add `layeredApplySkipEnabled bool` field.
3. Add corresponding parameters to `NewApplyCommandRunner()`.
4. In `Run()`, the skip functionality is gated on both flags:
```go
if cmd.Skip && !a.layeredApplySkipEnabled {
    // respond with error: skip not enabled
    return
}
```

### Where Flags Are Checked in the Lifecycle

| Flag | Where Checked | What It Gates |
|------|---------------|---------------|
| `enable-layered-planning` | `PlanCommandRunner.run()` | Whether to use layered plan logic on autoplan |
| `enable-layered-planning` | `PlanCommandRunner.run()` | Whether to build dependency graph and compute layers on initial plan |
| `enable-layered-planning` | `ApplyCommandRunner.Run()` | Whether to trigger next-layer planning after successful apply |
| `enable-layered-planning` | Dashboard comment renderer | Whether to render the layer-aware dashboard |
| `enable-layered-apply-skip` | `ApplyCommandRunner.Run()` | Whether the `-skip` flag is accepted |
| `enable-layered-apply-skip` | Comment parser | Whether `atlantis apply -skip` is a valid command |

### Environment Variable Support

Following Atlantis convention, flags automatically get corresponding environment variables via the `ATLANTIS_` prefix and uppercase/underscore transformation:

- `--enable-layered-planning` -> `ATLANTIS_ENABLE_LAYERED_PLANNING`
- `--enable-layered-apply-skip` -> `ATLANTIS_ENABLE_LAYERED_APPLY_SKIP`

This is handled automatically by the existing `viper` configuration in `cmd/server.go` -- no additional code needed.

### Config File Support

Similarly, via `mapstructure` tags, these are automatically supported in the YAML config file:

```yaml
enable-layered-planning: true
enable-layered-apply-skip: true
```

No additional code needed.

### Validation

Add validation in `server/server.go` (in the `NewServer` function) to warn/error if `enable-layered-apply-skip` is set without `enable-layered-planning`:

```go
if userConfig.EnableLayeredApplySkip && !userConfig.EnableLayeredPlanning {
    return nil, fmt.Errorf("--enable-layered-apply-skip requires --enable-layered-planning to also be set")
}
```

---

## Testing Approach

**This workstream uses test-alongside.** Write implementation and tests together, following existing codebase patterns.

- **Framework:** Standard Go testing with custom assertions from `testing/` package
- **Mocks:** Pegomock for dependencies (layer state manager, VCS client, working dir)
- **Re-plan logic tests:** Pure logic with mocked layer state — verify correct classification of affected projects into re-plan/reset/skip buckets
- **Config tests:** Follow existing patterns in `cmd/server_test.go` for flag parsing and validation
- **Integration tests:** Test re-plan logic wired into `PlanCommandRunner` with mocked dependencies

## Test Cases

### Part A: Re-plan Logic

#### Unit Tests (`server/events/layered_replan_test.go`)

1. **Test_DetermineReplanScope_AllFutureLayer**
   - Setup: 3 projects affected by push, all in layer 2. Current layer is 0.
   - Expected: `ReplanProjects` empty, `ResetToLayer` nil, all 3 in `SkippedFutureProjects`.

2. **Test_DetermineReplanScope_AllCurrentLayer**
   - Setup: 2 projects affected, both in layer 1 (current layer). Both have status `Planned`.
   - Expected: Both in `ReplanProjects`. No reset.

3. **Test_DetermineReplanScope_CurrentLayerAppliedProject**
   - Setup: 1 project in current layer with status `Applied`.
   - Expected: `ResetToLayer` set to current layer number.

4. **Test_DetermineReplanScope_PastLayer**
   - Setup: 1 project affected, in layer 0. Current layer is 2.
   - Expected: `ResetToLayer = 0`.

5. **Test_DetermineReplanScope_MixedLayers**
   - Setup: Project A in layer 0 (past), Project B in layer 1 (current), Project C in layer 3 (future). Current layer is 1.
   - Expected: `ResetToLayer = 0` (minimum past layer). Project C in `SkippedFutureProjects`.

6. **Test_DetermineReplanScope_ResetPicksMinimumLayer**
   - Setup: Project A in layer 0 (past), Project B in layer 1 (past). Current layer is 3.
   - Expected: `ResetToLayer = 0` (not 1).

7. **Test_DetermineReplanScope_NoLayerState**
   - Setup: No layer state exists for this PR.
   - Expected: Falls through to normal autoplan behavior (caller handles nil state).

8. **Test_ResetLayerState_ClearsCorrectLayers**
   - Setup: Layer state with layers 0-3, projects in each. Reset to layer 1.
   - Expected: Layer 0 projects unchanged (still Applied). Layers 1, 2, 3 projects cleared.
   - Verify: `CurrentLayer` set to 1.

9. **Test_ResetLayerState_DeletesPlanFiles**
   - Verify that plan files for invalidated projects are removed from working directory.

10. **Test_ResetLayerState_ReleasesLocks**
    - Verify that project locks for invalidated projects are released.

#### Integration Tests

11. **Test_Autoplan_LayeredReplan_CurrentLayer**
    - Push new commits affecting a project in the current layer.
    - Verify only that project is re-planned.
    - Verify other projects in the current layer retain their plan state.

12. **Test_Autoplan_LayeredReplan_Reset**
    - Push new commits affecting a project in an already-applied layer.
    - Verify layer state is reset.
    - Verify the reset layer is re-planned.
    - Verify downstream layers are invalidated.

13. **Test_Autoplan_LayeredReplan_FutureOnly**
    - Push new commits affecting only future-layer projects.
    - Verify no planning occurs.
    - Verify layer state is unchanged.

14. **Test_Autoplan_NonLayered_Unchanged**
    - With `enable-layered-planning=false`, push new commits.
    - Verify behavior is identical to existing (all affected projects planned).

### Part B: Configuration Tests

#### Unit Tests

15. **Test_EnableLayeredPlanningFlag_Default**
    - Verify default value is `false`.

16. **Test_EnableLayeredApplySkipFlag_Default**
    - Verify default value is `false`.

17. **Test_EnableLayeredApplySkip_RequiresLayeredPlanning**
    - Set `enable-layered-apply-skip=true` without `enable-layered-planning=true`.
    - Verify server startup returns error.

18. **Test_FlagPropagation_PlanCommandRunner**
    - Set `enable-layered-planning=true` via config.
    - Verify `PlanCommandRunner.layeredPlanningEnabled` is `true`.

19. **Test_FlagPropagation_ApplyCommandRunner**
    - Set both flags via config.
    - Verify both are propagated to `ApplyCommandRunner`.

20. **Test_EnvironmentVariables**
    - Set `ATLANTIS_ENABLE_LAYERED_PLANNING=true`.
    - Verify config picks it up correctly.

21. **Test_YamlConfig**
    - Parse a YAML config file with `enable-layered-planning: true`.
    - Verify it maps correctly to `UserConfig`.

---

## Summary of All Files to Create/Modify

### New Files

| File | Purpose |
|------|---------|
| `server/events/layered_replan.go` | Core re-plan scope determination and layer reset logic |
| `server/events/layered_replan_test.go` | Unit tests for re-plan and reset logic |

### Modified Files

| File | Changes |
|------|---------|
| `cmd/server.go` | Add `EnableLayeredPlanningFlag` and `EnableLayeredApplySkipFlag` constants; add entries to `boolFlags` map |
| `server/user_config.go` | Add `EnableLayeredPlanning` and `EnableLayeredApplySkip` fields to `UserConfig` struct |
| `server/server.go` | Pass new config values to `NewPlanCommandRunner` and `NewApplyCommandRunner`; add validation for flag dependency |
| `server/events/plan_command_runner.go` | Add `layeredPlanningEnabled` field; add `handleLayeredReplan` method; modify `run()` to check for layered mode on autoplan |
| `server/events/apply_command_runner.go` | Add `layeredPlanningEnabled` and `layeredApplySkipEnabled` fields; gate skip command on flag; trigger next-layer planning after apply when enabled |

---

## Risks and Open Questions

### Risks

1. **Race condition on layer state:** If two pushes arrive in rapid succession, the second push's re-plan scope calculation could be based on stale layer state. Mitigation: The existing `WorkingDirLocker` serializes operations per-repo-per-PR, which should prevent concurrent re-plan processing. Verify this is sufficient.

2. **Plan file cleanup on reset:** When resetting to a previous layer, we need to delete plan files for all invalidated projects. If a plan file is missing (already cleaned up or never created), the reset should not fail. Ensure `ResetLayerState` handles missing files gracefully.

3. **Lock cleanup on reset:** Similarly, `Unlock()` should be idempotent -- unlocking a project that has no lock should be a no-op, not an error. Verify the existing `database.Unlock()` behavior.

4. **Dashboard comment race:** If `handleLayeredReplan` runs while a manual `atlantis plan` is in progress, both could try to update the dashboard comment. The dashboard comment updater should be idempotent (last write wins is acceptable since state is re-derived from the database).

5. **GetModifiedFiles returns full PR diff, not push diff:** This means if project A's files were modified in commit 1 and project B's files in commit 2, a push of commit 2 will show both A and B as affected. This is actually correct behavior for our purposes -- we want to re-plan anything that has changed relative to base, not just the latest push.

### Open Questions

1. **Should `atlantis plan` (manual, not autoplan) also respect layer state?** The design doc says manual re-plan "re-plans the current layer" and "does not advance layers." This is handled by the layered plan command runner (another workstream). The re-plan-on-push logic should only trigger on `command.Autoplan`, not manual `command.Plan`. Confirm this interpretation.

2. **What happens if the `atlantis.yaml` changes in a push?** If `depends_on` configuration changes, the layer graph changes. The safest approach: if `atlantis.yaml` is in the modified files, fully reset layer state and recompute from scratch (treat as a reset to layer 0). This should be documented and implemented.

3. **Should we re-plan projects that have "no changes" status in the current layer?** If a project was planned with no changes, and new code is pushed that affects it, yes -- the new code might introduce changes. The algorithm above handles this correctly (it re-plans all non-applied projects in the current layer that are affected).

4. **What if a project is added or removed from `atlantis.yaml` in a push?** If a new project appears, it should be incorporated into the layer graph. If a project is removed, it should be dropped. In both cases, a full layer recomputation is needed. This is best handled by detecting `atlantis.yaml` changes and doing a full reset (see question 2).

5. **Integration with `execution_order_group`:** The design doc mentions that `execution_order_group` is a separate concept from layered planning. When layered planning is enabled, should `execution_order_group` still be respected within a layer? The simplest answer is yes -- within a single layer, projects can still be sorted by `execution_order_group` for the order they are planned/applied. This is already handled by the existing sorting in `project_command_builder.go` (line 574).

NOT FOUND
