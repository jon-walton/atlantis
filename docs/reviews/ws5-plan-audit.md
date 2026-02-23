# WS5: Re-plan & Config -- Plan vs Implementation Audit

## Summary

**Overall compliance: ~70%**

Part B (Server Configuration) is fully implemented and well-tested. Part A (Smart Re-plan on New Commits) has the core algorithm implemented (`HandleNewCommit` in the `LayerStateManager`) but is **not wired into the PlanCommandRunner** -- meaning the re-plan logic exists as dead code that is only exercised by unit tests. The planned `layered_replan.go` file, `handleLayeredReplan` method, `ResetLayerState` function, and full integration into the autoplan flow are all missing. Skip functionality is fully implemented end-to-end.

---

## Audit Results

### Part A: Smart Re-plan on New Commits

#### Core Re-plan Algorithm

| Plan Item | Status | Notes |
|---|---|---|
| `DetermineReplanScope()` function | DIVERGED | Implemented as `HandleNewCommit()` on `DefaultLayerStateManager` at `server/events/layer_state_manager.go:423`. Same algorithm, different name and signature. |
| `ReplanDecision` struct | DIVERGED | Implemented as `ResetAction` struct at `server/events/layer_state_manager.go:62`. Fields renamed: `ReplanProjects` kept, `ResetToLayer` is `int` with -1 sentinel instead of `*int`, `SkippedFutureProjects` dropped (not needed). |
| `AffectedProject` struct | NOT IMPLEMENTED | Not needed -- `HandleNewCommit` takes `[]string` (project names) directly instead of a struct with layer/workspace info. Acceptable simplification. |
| Future-layer projects: no action | IMPLEMENTED | `server/events/layer_state_manager.go:442-445` -- projects with `layer > currentLayer` are skipped. |
| Current-layer, not applied: replan | IMPLEMENTED | `server/events/layer_state_manager.go:447-455` -- projects in current layer with non-Applied status added to `ReplanProjects`. |
| Current-layer, already applied: treated as reset | DIVERGED | `server/events/layer_state_manager.go:447-455` -- **does NOT treat applied current-layer projects as a reset**. Instead, applied projects in the current layer result in `NoAction` (line 450: `proj.Status != models.AppliedStatus` check excludes them from replan but doesn't trigger reset). Plan says this is "problematic" and should set `ResetToLayer = currentLayer`. |
| Past-layer: reset to min layer | IMPLEMENTED | `server/events/layer_state_manager.go:457-460` -- picks minimum layer. |
| `ResetToLayer` picks minimum across all affected | IMPLEMENTED | `server/events/layer_state_manager.go:458` -- `if resetToLayer == -1 \|\| layer < resetToLayer`. |
| `ResetLayerState()` function | NOT IMPLEMENTED | No function exists to roll back layer state. No plan file deletion, no lock release, no state clearing for invalidated layers. |

#### Files Planned

| Plan Item | Status | Notes |
|---|---|---|
| `server/events/layered_replan.go` (new file) | NOT IMPLEMENTED | File does not exist. The re-plan algorithm was placed in `layer_state_manager.go` instead. |
| `server/events/layered_replan_test.go` (new file) | NOT IMPLEMENTED | Tests live in `layer_state_manager_test.go` instead (acceptable). |

#### Integration into PlanCommandRunner

| Plan Item | Status | Notes |
|---|---|---|
| `layeredPlanningEnabled` field on `PlanCommandRunner` | DIVERGED | Not a separate boolean field. Instead, `layerManager *LayerManager` is used; nil means disabled. Acceptable pattern. |
| `handleLayeredReplan()` method | NOT IMPLEMENTED | Method does not exist on `PlanCommandRunner`. |
| `runAutoplan()` check for existing layer state on push | NOT IMPLEMENTED | `runAutoplan()` at `server/events/plan_command_runner.go:112` always initializes fresh layer state via `InitializeLayerState()`. It never loads existing state from the DB to check if a push affects an in-progress layered PR. |
| `run()` (manual plan) loads existing layer state from DB | IMPLEMENTED | `server/events/plan_command_runner.go:313-318` -- the manual `run()` method does load existing layer state. |
| Filter project commands to subset for re-plan | NOT IMPLEMENTED | No filtering to re-plan only affected current-layer projects. `runAutoplan` always replans all projects in the current layer. |
| `BuildAutoplanCommandsForProjects()` method | NOT IMPLEMENTED | Plan recommended against this (filter after building). Implementation follows recommendation. N/A. |

#### Layer State Reset / Rollback

| Plan Item | Status | Notes |
|---|---|---|
| Clear plan status for invalidated layers | NOT IMPLEMENTED | No `ResetLayerState` or equivalent. |
| Delete plan files for invalidated projects | NOT IMPLEMENTED | No plan file cleanup on layer reset. |
| Release locks for invalidated projects | NOT IMPLEMENTED | No selective lock release. |
| Set `CurrentLayer` to reset target | NOT IMPLEMENTED | No reset mechanism. |
| Persist updated state to DB | NOT IMPLEMENTED | No reset-and-save flow. |

#### Modified Files (Plan Command Runner)

| Plan Item | Status | Notes |
|---|---|---|
| `layerStateStore` interface on `PlanCommandRunner` | DIVERGED | Uses `database db.Database` field instead (already present at `server/events/plan_command_runner.go:109`). Acceptable. |
| `layerStateStore.GetLayerState()` | DIVERGED | Uses `database.GetPullStatus()` to load layer state from `pullStatus.LayerState` (in `run()` method, line 314). |
| Check `cmd.Name == command.Autoplan` for push-triggered | NOT IMPLEMENTED | The `Run()` method at line 414-420 does dispatch differently (`ctx.Trigger == command.AutoTrigger` -> `runAutoplan()`), but `runAutoplan` doesn't have the re-plan-on-push logic. |

#### Integration with Existing Command Runner

| Plan Item | Status | Notes |
|---|---|---|
| `command_runner.go` changes for `RunAutoplanCommand` | NOT NEEDED | Plan notes "minimal change needed" and this is confirmed -- `RunAutoplanCommand` sets `command.AutoTrigger` and the dispatch happens in `PlanCommandRunner.Run()`. |

### Part A: Test Cases

| Test ID | Plan Description | Status | Notes |
|---|---|---|---|
| 1 | `Test_DetermineReplanScope_AllFutureLayer` | IMPLEMENTED | `TestLayerStateHandleNewCommit_FutureLayer` at `server/events/layer_state_manager_test.go:760` |
| 2 | `Test_DetermineReplanScope_AllCurrentLayer` | IMPLEMENTED | `TestLayerStateHandleNewCommit_CurrentLayerNotApplied` at `server/events/layer_state_manager_test.go:782` |
| 3 | `Test_DetermineReplanScope_CurrentLayerAppliedProject` | IMPLEMENTED | `TestLayerStateHandleNewCommit_CurrentLayerAlreadyApplied` at `server/events/layer_state_manager_test.go:802` (but behavior diverges from plan -- see gap below) |
| 4 | `Test_DetermineReplanScope_PastLayer` | IMPLEMENTED | `TestLayerStateHandleNewCommit_CompletedLayer` at `server/events/layer_state_manager_test.go:820` |
| 5 | `Test_DetermineReplanScope_MixedLayers` | IMPLEMENTED | `TestLayerStateHandleNewCommit_MultipleLayers` at `server/events/layer_state_manager_test.go:843` |
| 6 | `Test_DetermineReplanScope_ResetPicksMinimumLayer` | IMPLEMENTED | Covered in test 5 above (A in layer 0, B in layer 1 -> resets to 0) |
| 7 | `Test_DetermineReplanScope_NoLayerState` | IMPLEMENTED | `TestLayerStateHandleNewCommit_NilState` at `server/events/layer_state_manager_test.go:885` |
| 8 | `Test_ResetLayerState_ClearsCorrectLayers` | NOT IMPLEMENTED | `ResetLayerState` does not exist. |
| 9 | `Test_ResetLayerState_DeletesPlanFiles` | NOT IMPLEMENTED | `ResetLayerState` does not exist. |
| 10 | `Test_ResetLayerState_ReleasesLocks` | NOT IMPLEMENTED | `ResetLayerState` does not exist. |
| 11 | `Test_Autoplan_LayeredReplan_CurrentLayer` | NOT IMPLEMENTED | No integration test for re-plan on push. |
| 12 | `Test_Autoplan_LayeredReplan_Reset` | NOT IMPLEMENTED | No integration test for reset on push. |
| 13 | `Test_Autoplan_LayeredReplan_FutureOnly` | NOT IMPLEMENTED | No integration test for push affecting only future layers. |
| 14 | `Test_Autoplan_NonLayered_Unchanged` | NOT IMPLEMENTED | No integration test for non-layered autoplan. |

---

### Part B: Server Configuration

#### Flag Definitions (`cmd/server.go`)

| Plan Item | Status | Notes |
|---|---|---|
| `EnableLayeredPlanningFlag` constant | IMPLEMENTED | `cmd/server.go:89` |
| `EnableLayeredApplySkipFlag` constant | IMPLEMENTED | `cmd/server.go:88` |
| `boolFlags` entry for `EnableLayeredPlanningFlag` | IMPLEMENTED | `cmd/server.go:554-557` with correct description and `defaultValue: false` |
| `boolFlags` entry for `EnableLayeredApplySkipFlag` | IMPLEMENTED | `cmd/server.go:550-553` with correct description and `defaultValue: false` |

#### UserConfig (`server/user_config.go`)

| Plan Item | Status | Notes |
|---|---|---|
| `EnableLayeredPlanning` field | IMPLEMENTED | `server/user_config.go:50` with `mapstructure:"enable-layered-planning"` |
| `EnableLayeredApplySkip` field | IMPLEMENTED | `server/user_config.go:49` with `mapstructure:"enable-layered-apply-skip"` |

#### Flag Propagation (`server/server.go`)

| Plan Item | Status | Notes |
|---|---|---|
| Validation: `apply-skip` requires `planning` | IMPLEMENTED | `server/server.go:201-203` |
| Pass to `PlanCommandRunner` constructor | DIVERGED | Not passed as boolean. Instead, a `LayerManager` is constructed if `EnableLayeredPlanning` is true (`server/server.go:795-799`) and passed as `layerManager` parameter (line 822). Nil when disabled. Functionally equivalent. |
| Pass to `ApplyCommandRunner` constructor | DIVERGED | Same pattern -- `layerManager` passed at `server/server.go:842`. No separate `layeredApplySkipEnabled` bool passed. Skip enablement is checked at the `ApplyCommandRunner.handleSkip` level by checking `layerManager == nil`. |
| `layeredPlanningEnabled` field on `PlanCommandRunner` | DIVERGED | Uses `layerManager *LayerManager` nil-check pattern instead. |
| `layeredPlanningEnabled` field on `ApplyCommandRunner` | DIVERGED | Same nil-check pattern. |
| `layeredApplySkipEnabled` field on `ApplyCommandRunner` | NOT IMPLEMENTED | No separate field. Skip is always enabled when `layerManager` is non-nil. The `SkipProject` method on the state manager checks `skipEnabled` but it's always called with `true` at `server/events/apply_command_runner.go:321`. |

#### Comment Parser Integration

| Plan Item | Status | Notes |
|---|---|---|
| `--skip` flag gated on `EnableLayeredApplySkip` | IMPLEMENTED | `server/events/comment_parser.go:91,263-265` -- flag only registered when `EnableLayeredApplySkip` is true. |
| `--skip` validation (cannot combine with -d/-w/-p) | IMPLEMENTED | `server/events/comment_parser.go:351-354` |
| `SkipProject` field on `CommentCommand` | IMPLEMENTED | `server/events/event_parser.go:147-149` |
| `NewCommentCommand` accepts `skipProject` parameter | IMPLEMENTED | `server/events/event_parser.go:190,211` |
| `EnableLayeredApplySkip` passed to `CommentParser` constructor | IMPLEMENTED | `server/server.go:620` |

#### Where Flags Are Checked

| Plan Item | Status | Notes |
|---|---|---|
| `PlanCommandRunner.run()`: use layered plan logic on autoplan | IMPLEMENTED | Checked via `layerManager != nil` in both `runAutoplan()` (line 153) and `run()` (line 311). |
| `PlanCommandRunner.run()`: build dependency graph on initial plan | IMPLEMENTED | `layerManager.InitializeLayerState()` called in both autoplan and manual plan paths. |
| `ApplyCommandRunner.Run()`: trigger next-layer planning | IMPLEMENTED | `handleLayerCompletion()` at `server/events/apply_command_runner.go:248-295`. |
| Dashboard comment renderer: render layer-aware dashboard | IMPLEMENTED | Gated on `layerManager != nil` throughout. |
| `ApplyCommandRunner.Run()`: accept `--skip` flag | PARTIALLY IMPLEMENTED | `handleSkip()` checks `layerManager == nil` (line 301) but does NOT separately check `EnableLayeredApplySkip`. The gating happens at the comment parser level only. |
| Comment parser: `--skip` validity | IMPLEMENTED | `server/events/comment_parser.go:263-265`. |

#### Environment Variable / Config File Support

| Plan Item | Status | Notes |
|---|---|---|
| `ATLANTIS_ENABLE_LAYERED_PLANNING` env var | IMPLEMENTED | Automatic via viper/mapstructure. |
| `ATLANTIS_ENABLE_LAYERED_APPLY_SKIP` env var | IMPLEMENTED | Automatic via viper/mapstructure. |
| YAML config file support | IMPLEMENTED | Automatic via mapstructure tags. |

### Part B: Test Cases

| Test ID | Plan Description | Status | Notes |
|---|---|---|---|
| 15 | `Test_EnableLayeredPlanningFlag_Default` | IMPLEMENTED | `cmd/server_test.go:168` -- verified in `testFlags` defaults map. |
| 16 | `Test_EnableLayeredApplySkipFlag_Default` | IMPLEMENTED | `cmd/server_test.go:167` -- verified in `testFlags` defaults map. |
| 17 | `Test_EnableLayeredApplySkip_RequiresLayeredPlanning` | NOT EXPLICITLY TESTED | Validation exists at `server/server.go:201-203` but no dedicated test case for this validation. |
| 18 | `Test_FlagPropagation_PlanCommandRunner` | NOT EXPLICITLY TESTED | No test verifying `layerManager` is correctly set on `PlanCommandRunner`. |
| 19 | `Test_FlagPropagation_ApplyCommandRunner` | NOT EXPLICITLY TESTED | No test verifying `layerManager` is correctly set on `ApplyCommandRunner`. |
| 20 | `Test_EnvironmentVariables` | NOT EXPLICITLY TESTED | Relies on viper framework behavior (acceptable). |
| 21 | `Test_YamlConfig` | NOT EXPLICITLY TESTED | Relies on mapstructure framework behavior (acceptable). |
| -- | `TestParse_ApplySkipFlag` (not in plan) | IMPLEMENTED | `server/events/comment_parser_test.go:1172` -- comprehensive tests for `--skip` parsing. |
| -- | `TestParse_ApplySkipDisabled` (not in plan) | IMPLEMENTED | `server/events/comment_parser_test.go:1232` -- verifies skip fails when disabled. |

---

## Gaps Requiring Action

### Gap 1: `runAutoplan()` does not load existing layer state on push (CRITICAL)

**What the plan says:** When a push event arrives and there is already active layer state for the PR, the system should call `HandleNewCommit` / `DetermineReplanScope` to determine what to re-plan vs reset vs skip.

**What actually exists:** `runAutoplan()` at `server/events/plan_command_runner.go:112` always calls `InitializeLayerState()` which creates fresh layer state from scratch. It never checks the database for pre-existing layer state. This means:
- Every push to a PR with active layered planning reinitializes the entire layer graph
- Projects in already-applied layers lose their applied status
- Projects in the current layer lose their plan results
- The `HandleNewCommit` method is never called from the autoplan path

**Where the fix should go:** `server/events/plan_command_runner.go`, in the `runAutoplan()` method (starting at line 112). Before calling `InitializeLayerState()`, check the database for existing layer state:

```go
// In runAutoplan(), after building projectCmds, before the layered planning gate:
if p.layerManager != nil && p.database != nil {
    existingStatus, fetchErr := p.database.GetPullStatus(pull)
    if fetchErr == nil && existingStatus != nil && existingStatus.LayerState != nil {
        // Existing layered state -- determine re-plan scope
        action := p.layerManager.stateManager.HandleNewCommit(existingStatus, affectedProjectNames)
        // Handle reset or selective re-plan...
        return
    }
}
```

**Suggested fix:** Mirror the pattern already used in the `run()` method (lines 312-318) which checks `p.database.GetPullStatus()` before deciding whether to initialize or reuse state.

### Gap 2: `ResetLayerState()` function does not exist (HIGH)

**What the plan says:** A `ResetLayerState()` function should clear plan status, delete plan files, release locks, and set `CurrentLayer` for all layers >= the reset target.

**What actually exists:** Nothing. Even though `HandleNewCommit` can return a `ResetAction` with `ResetToLayer >= 0`, there is no code to act on that reset action. The `ResetAction` struct exists but is unused outside tests.

**Where the fix should go:** New function in `server/events/layer_state_manager.go` or a new method on `LayerManager` in `server/events/layer_manager.go`. It should:
1. Clear project status for all projects in layers >= resetToLayer
2. Remove plan files using `WorkingDir` / `PendingPlanFinder`
3. Release locks using `locking.Locker.Unlock()`
4. Update `state.CurrentLayer = resetToLayer`
5. Persist via `database.UpdateLayerState()`

### Gap 3: `handleLayeredReplan()` method missing on PlanCommandRunner (HIGH)

**What the plan says:** A `handleLayeredReplan()` method on `PlanCommandRunner` should orchestrate the re-plan flow: call `DetermineReplanScope`, handle reset if needed, or re-plan only affected current-layer projects.

**What actually exists:** Nothing. The method does not exist.

**Where the fix should go:** `server/events/plan_command_runner.go`. Add a new method:

```go
func (p *PlanCommandRunner) handleLayeredReplan(
    ctx *command.Context,
    layerState *models.LayerState,
    projectCmds []command.ProjectContext,
) { ... }
```

This should be called from `runAutoplan()` when existing layer state is detected.

### Gap 4: Applied current-layer project does not trigger reset (MEDIUM)

**What the plan says:** "If project status IS Applied [in the current layer]: This is problematic -- set `ResetToLayer = currentLayer`."

**What actually exists:** `HandleNewCommit` at `server/events/layer_state_manager.go:447-455` skips projects with `AppliedStatus` in the current layer (the `proj.Status != models.AppliedStatus` check excludes them). This means if a project in the current layer was already applied and new code is pushed affecting it, the system treats it as no-action.

**Where the fix should go:** `server/events/layer_state_manager.go:447-455`. The logic should be:
```go
if layer == state.CurrentLayer {
    for _, proj := range pullStatus.Projects {
        if proj.ProjectName == projName {
            if proj.Status == models.AppliedStatus {
                // Already applied in current layer -- need reset
                if resetToLayer == -1 || layer < resetToLayer {
                    resetToLayer = layer
                }
            } else {
                replanProjects = append(replanProjects, projName)
            }
        }
    }
    continue
}
```

The corresponding test `TestLayerStateHandleNewCommit_CurrentLayerAlreadyApplied` would need updating -- currently it asserts `NoAction: true`, but per the plan it should assert `ResetToLayer: 0`.

### Gap 5: `layeredApplySkipEnabled` not separately tracked in ApplyCommandRunner (LOW)

**What the plan says:** `ApplyCommandRunner` should have a `layeredApplySkipEnabled` field gating `--skip` acceptance in the runner.

**What actually exists:** The skip enablement is gated at two levels:
1. Comment parser: `--skip` flag only registered when `EnableLayeredApplySkip` is true (line 263)
2. State manager: `SkipProject()` takes `skipEnabled` bool, but it's hardcoded to `true` at the call site (line 321)

The comment parser gating is sufficient for the normal flow (invalid commands get rejected before reaching the runner). However, if someone calls `SkipProject` programmatically, the `skipEnabled` parameter is meaningless since it's always `true`.

**Suggested fix:** Either pass `userConfig.EnableLayeredApplySkip` through to `ApplyCommandRunner` and use it at line 321, or remove the `skipEnabled` parameter from `SkipProject()` since it's redundant with the comment parser gating.

### Gap 6: Missing validation and flag propagation tests (LOW)

**What the plan says:** Tests 17-21 for flag validation and propagation.

**What actually exists:** The validation logic exists (`server/server.go:201-203`) but has no dedicated unit test. Flag propagation relies on the existing `server.go` constructor wiring which is tested indirectly through integration tests but not explicitly.

**Suggested fix:** Add tests in `cmd/server_test.go` or `server/server_test.go`:
- Test that `NewServer` returns error when `EnableLayeredApplySkip=true` and `EnableLayeredPlanning=false`
- Test that `NewServer` succeeds when both are true

### Gap 7: Missing integration tests for autoplan re-plan scenarios (MEDIUM)

**What the plan says:** Tests 11-14 for integration testing of autoplan with layered re-plan.

**What actually exists:** No integration tests for the push-to-layered-PR scenario.

**Suggested fix:** Add integration tests in a new test file or extend existing `command_runner_test.go`. These tests should exercise the full flow from `RunAutoplanCommand` through to re-plan/reset behavior.

---

## Minor Divergences (Acceptable)

1. **Naming:** `DetermineReplanScope` -> `HandleNewCommit`, `ReplanDecision` -> `ResetAction`. The implementation names are more descriptive and consistent with the codebase style. No action needed.

2. **`SkippedFutureProjects` field omitted from `ResetAction`:** The plan included this for debugging, but the implementation simply doesn't track them. This is fine -- the information is logged at the caller level.

3. **`layeredPlanningEnabled` boolean replaced by nil-check pattern:** Instead of a separate boolean flag, the implementation uses `layerManager *LayerManager` where nil means disabled. This is a cleaner Go idiom and is used consistently throughout.

4. **No separate `layered_replan.go` file:** The re-plan logic was placed in `layer_state_manager.go` alongside the other state management methods. This is a reasonable organizational choice that reduces file count and keeps related logic together.

5. **`BuildAutoplanCommandsForProjects` not implemented:** Plan explicitly recommended against this ("filter after building") and the implementation follows that recommendation. No action needed.

6. **`ResetToLayer` uses `int` with -1 sentinel instead of `*int`:** Standard Go pattern trade-off. The sentinel approach avoids pointer indirection. Acceptable.

7. **Flag default tests use `testFlags` map:** Instead of individual test functions (tests 15-16), the defaults are verified in the `testFlags` map at `cmd/server_test.go:167-168`. This is the existing codebase pattern and is sufficient.

8. **`run()` (manual plan) handles existing layer state correctly:** The manual plan path properly checks the database for existing layer state before deciding to initialize or reuse. This is correct behavior even though the plan didn't explicitly describe it.
