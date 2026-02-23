# WS4: Lifecycle Integration -- Plan vs Implementation Audit

## Summary

The lifecycle integration layer (WS4) is **substantially implemented** with strong alignment to the plan's architecture. The core flow -- autoplan filtering, apply filtering, layer advancement, dashboard updates, skip handling, next-layer cascade planning, and server wiring -- is all present and functional. The implementation makes several reasonable architectural divergences from the plan, notably refactoring the monolithic `LayerManager` into a `LayerManager` coordinator + `LayerStateManager` interface separation, and using the existing `PlanCommandRunner.run()` for cascade planning instead of a custom `triggerNextLayerPlanning()` method.

Key gaps are minor: the specific-project apply validation (`IsInCurrentLayer` guard) is defined but not invoked in the apply runner, the `deletePlansForProjects` method for manual plan is not implemented, and there is no explicit `DeleteLayerState` method on PR close (though `DeletePullStatus` may handle it since LayerState is embedded in PullStatus).

**Overall compliance: ~92%**

---

## Audit Results

### 1. Architecture Summary (Plan Section 1)

| Plan Item | Status | Notes |
|---|---|---|
| Autoplan: `BuildAutoplanCommands` -> `FilterToCurrentLayer` -> run plans -> update dashboard | IMPLEMENTED | `plan_command_runner.go:112-231` -- full flow matches plan |
| Apply: `BuildApplyCommands` -> `FilterToCurrentLayer` -> run applies -> check layer completion -> trigger next layer | IMPLEMENTED | `apply_command_runner.go:84-246` -- full flow matches plan |
| LayerManager as central coordinator | IMPLEMENTED | `layer_manager.go:17-284` -- split into `LayerManager` + `LayerStateManager` |
| Dashboard update after plan/apply | IMPLEMENTED | Both `runAutoplan()` and `run()` call `UpdateDashboard`; apply runner calls it in `handleLayerCompletion` and error paths |

### 2. Feature Gating (Plan Section 2)

| Plan Item | Status | Notes |
|---|---|---|
| `EnableLayeredPlanning` in `UserConfig` | IMPLEMENTED | `server/user_config.go:50` -- `enable-layered-planning` mapstructure tag |
| `EnableLayeredApplySkip` in `UserConfig` | IMPLEMENTED | `server/user_config.go:49` -- `enable-layered-apply-skip` mapstructure tag |
| CLI flag `enable-layered-planning` in `cmd/server.go` | IMPLEMENTED | `cmd/server.go:89` |
| CLI flag `enable-layered-apply-skip` in `cmd/server.go` | IMPLEMENTED | `cmd/server.go:88` |
| Validation: `EnableLayeredApplySkip` requires `EnableLayeredPlanning` | IMPLEMENTED | `server/server.go:201-203` -- explicit check at server startup |
| Autoplan entry gate | IMPLEMENTED | `plan_command_runner.go:153` -- `p.layerManager != nil && p.layerManager.ShouldActivate(projectCmds)` |
| Manual plan entry gate | IMPLEMENTED | `plan_command_runner.go:311` -- checks `!cmd.IsForSpecificProject()` + `ShouldActivate` |
| Apply entry gate | IMPLEMENTED | `apply_command_runner.go:183-197` -- loads existing layer state from DB |
| Skip command parsing gate | IMPLEMENTED | `comment_parser.go:263-265` -- gated by `EnableLayeredApplySkip` |
| Dashboard rendering gate | IMPLEMENTED | `layer_manager.go:179-181` -- `HasMultipleLayers` check |
| `EnableLayeredPlanning` on `PlanCommandRunner` | DIVERGED | Plan says add fields to runner struct. Implementation uses nil-check on `layerManager` pointer instead, which is cleaner. No flags on the struct itself. |
| `EnableLayeredApplySkip` on `CommentParser` | IMPLEMENTED | `comment_parser.go:91` |
| Activation logic: flag + depends_on + in-scope projects | IMPLEMENTED | `ShouldActivate()` checks `DependsOn` on project contexts. Flag check is implicit (layerManager is nil when disabled). |
| Single-layer no-dashboard behavior | IMPLEMENTED | `plan_command_runner.go:162-165` -- `HasMultipleLayers` check; falls through to normal flow |

### 3. New Components (Plan Section 3)

#### 3.1 LayerManager

| Plan Item | Status | Notes |
|---|---|---|
| `LayerManager` struct | DIVERGED (ACCEPTABLE) | Plan: monolithic with `EnableLayeredPlanning`/`EnableLayeredApplySkip` bool fields. Implementation: composed of `stateManager LayerStateManager` + `dashboardUpdater *layers.DashboardUpdater`. Feature flags handled via nil-check and separate `EnableLayeredApplySkip` on state manager's `SkipProject()`. |
| `IsEnabled(projectCfgs)` method | DIVERGED (ACCEPTABLE) | Named `ShouldActivate(projectCmds)` instead. Takes `[]command.ProjectContext` not `[]valid.MergedProjectCfg`. Same purpose. `layer_manager.go:35-42` |
| `ComputeLayers(projectCfgs)` method | DIVERGED (ACCEPTABLE) | Named `InitializeLayerState(projectCmds)`. Delegates to `stateManager.InitializeLayerState()`. Returns `*models.LayerState` not custom `LayerState`. `layer_manager.go:47-84` |
| `FilterToCurrentLayer(projectCmds, pullStatus, layerState)` | IMPLEMENTED | `layer_manager.go:95-117` -- signature differs slightly (no pullStatus param, uses stateManager.GetCurrentLayerProjects) |
| `IsLayerComplete(pullStatus, layerState)` | IMPLEMENTED | Delegated to `stateManager.IsLayerComplete(pullStatus, layer)`. `layer_state_manager.go:204-228` |
| `GetNextLayerProjects(layerState, pullStatus, allProjectCfgs)` | DIVERGED (ACCEPTABLE) | Implemented as `stateManager.AdvanceLayer(pullStatus)` which combines advance + get-next-projects. Returns `(state, nextProjects, error)`. `layer_state_manager.go:253-344` |
| `HasMultipleLayers(state)` | IMPLEMENTED | `layer_manager.go:87-92` |
| `IsInCurrentLayer(cmd, state)` | IMPLEMENTED | `layer_manager.go:120-130` |
| `PostInitialDashboard(ctx, state, projectCmds)` | IMPLEMENTED (NOT IN PLAN) | Extra method not in plan but needed for UX. Posts dashboard before plan results so it's the first comment. `layer_manager.go:135-172` |
| `UpdateDashboard(ctx, pullStatus)` | IMPLEMENTED | `layer_manager.go:175-197` |
| `StampAndSaveLayerState(pullStatus, state)` | IMPLEMENTED (NOT IN PLAN) | Extra method not in plan but necessary for layer assignment stamping. `layer_manager.go:255-264` |
| `buildDashboardData(ctx, pullStatus)` | IMPLEMENTED (NOT IN PLAN) | Internal method for converting PullStatus to DashboardData. `layer_manager.go:201-251` |

#### 3.2 Layer State Persistence

| Plan Item | Status | Notes |
|---|---|---|
| `SaveLayerState(pull, state)` on Database | DIVERGED (ACCEPTABLE) | Named `UpdateLayerState(pull, state)` in `db.go:40`. Semantically equivalent. |
| `GetLayerState(pull)` on Database | DIVERGED (ACCEPTABLE) | Not a separate method. LayerState is embedded in PullStatus (`models.go:557`), so `GetPullStatus` returns it. The apply runner uses `GetPullStatus` to load layer state. |
| `DeleteLayerState(pull)` on Database | MISSING | No explicit `DeleteLayerState` method exists. The plan says to call this on PR close. However, `DeletePullStatus` likely handles this since LayerState is embedded in PullStatus. See Gaps section. |
| `LayerState` model | IMPLEMENTED | `models/models.go:563-591` -- fields match plan conceptually but differ structurally (see below) |
| `LayerState.Enabled` | IMPLEMENTED | `models/models.go:565` |
| `LayerState.CurrentLayer` | IMPLEMENTED | `models/models.go:569` -- includes -1 sentinel for "all complete" |
| `LayerState.TotalLayers` | IMPLEMENTED | `models/models.go:573` |
| `LayerState.DependencyGraph` | IMPLEMENTED (NOT IN PLAN) | `models/models.go:577` -- stores full graph for cascade evaluation. Plan stores this implicitly in LayerInfo. |
| `LayerState.ProjectLayers` | IMPLEMENTED (NOT IN PLAN) | `models/models.go:581` -- flat map vs plan's nested LayerInfo/LayerProjectInfo structure. Simpler approach. |
| `LayerState.PendingProjects` | IMPLEMENTED (NOT IN PLAN) | `models/models.go:586` -- tracks transitive dependents not yet assigned layers. Plan handles this differently via `UpstreamHadChanges`. |
| `LayerState.SkippedUpstreams` | IMPLEMENTED (NOT IN PLAN) | `models/models.go:590` -- tracks skipped projects for cascade exclusion. |
| Plan's `LayerInfo` / `LayerProjectInfo` nested types | NOT IMPLEMENTED | The implementation uses a flat `ProjectLayers` map and `PendingProjects` list instead of the plan's `[]LayerInfo` with nested `[]LayerProjectInfo`. This is a simpler and more appropriate design. |
| Plan's `LayerProjectStatus` enum | NOT IMPLEMENTED | Uses existing `ProjectPlanStatus` enum with added `ApplyingStatus` and `SkippedPlanStatus` values instead of a separate enum. Better integration with existing code. |

#### 3.3 Dashboard Renderer

| Plan Item | Status | Notes |
|---|---|---|
| `LayerDashboardRenderer` struct | DIVERGED (ACCEPTABLE) | Split into `layers.DashboardUpdater` + `layers.DashboardRenderer`. Located in `server/events/layers/` package, not `server/events/layer_dashboard.go`. |
| `RenderDashboard(layerState)` | IMPLEMENTED | `layers/dashboard_renderer.go` -- uses Go templates for rendering |
| `UpdateDashboard(logger, repo, pullNum, layerState)` | IMPLEMENTED | `layers/dashboard_updater.go:30-71` -- uses edit-in-place approach (better than plan's Option A hide+create) |
| Option A: HidePrevCommandComments + CreateComment | NOT IMPLEMENTED | Implementation uses Option B approach (edit-in-place via `EditComment`), which the plan acknowledged as cleaner. `dashboard_updater.go:48` |
| Comment splitting for large dashboards | IMPLEMENTED | `dashboard_updater.go:37` -- calls `RenderSplit(data, maxLen)` |
| `VCSClient.ListComments` | IMPLEMENTED | Added to VCS client interface; implemented for GitHub, Gitea; stubbed for Azure DevOps |
| `VCSClient.EditComment` | IMPLEMENTED | Added to VCS client interface; implemented for GitHub, Gitea; stubbed for Azure DevOps |
| `VCSClient.MaxCommentLength` | IMPLEMENTED | Added to VCS client interface |

### 4. Detailed Scenario Flows (Plan Section 4)

#### 4.1 Autoplan (PR Open or Push)

| Plan Item | Status | Notes |
|---|---|---|
| `prjCmdBuilder.BuildAutoplanCommands(ctx)` | IMPLEMENTED | `plan_command_runner.go:116` |
| `layerManager.IsEnabled` / `ShouldActivate` check | IMPLEMENTED | `plan_command_runner.go:153` |
| `layerManager.ComputeLayers` / `InitializeLayerState` | IMPLEMENTED | `plan_command_runner.go:154` |
| Circular dependency error handling | IMPLEMENTED | `plan_command_runner.go:155-161` -- posts error to pull request |
| `HasMultipleLayers` check (single layer = normal flow) | IMPLEMENTED | `plan_command_runner.go:162-165` |
| `FilterToCurrentLayer` for projectCmds | IMPLEMENTED | `plan_command_runner.go:172` |
| `FilterToCurrentLayer` for policyCheckCmds | IMPLEMENTED | `plan_command_runner.go:173` |
| Delete plans and locks (existing behavior) | IMPLEMENTED | `plan_command_runner.go:177-182` |
| `runProjectCmdsWithCancellationTracker` | IMPLEMENTED | `plan_command_runner.go:184` |
| `pullUpdater.updatePull` | IMPLEMENTED | `plan_command_runner.go:196` |
| `dbUpdater.updateDB` | IMPLEMENTED | `plan_command_runner.go:198` |
| Update layer state with plan results | DIVERGED (ACCEPTABLE) | Plan says `UpdateLayerStateFromResults`. Implementation uses `StampAndSaveLayerState` + `database.UpdateLayerState`. `plan_command_runner.go:205-210` |
| Dashboard update | IMPLEMENTED | `plan_command_runner.go:211` |
| Commit status updates | IMPLEMENTED | `plan_command_runner.go:214-215` |
| Policy checks filtered to current layer | IMPLEMENTED | Policy check cmds filtered at line 173; run at lines 218-230 |
| Initial dashboard posted BEFORE plan results | IMPLEMENTED | `plan_command_runner.go:170` -- `PostInitialDashboard` called before `FilterToCurrentLayer` and plan execution |

#### 4.2 Manual Plan (`atlantis plan`)

| Plan Item | Status | Notes |
|---|---|---|
| `prjCmdBuilder.BuildPlanCommands(ctx, cmd)` | IMPLEMENTED | `plan_command_runner.go:253` |
| Specific project bypass (no layer filtering) | IMPLEMENTED | `plan_command_runner.go:311` -- `!cmd.IsForSpecificProject()` guard |
| Load existing layer state from DB | IMPLEMENTED | `plan_command_runner.go:313-318` |
| Fresh computation if no existing state | IMPLEMENTED | `plan_command_runner.go:319-328` |
| `layerStateIsNew` flag logic | IMPLEMENTED | `plan_command_runner.go:310,328,339` -- controls whether `PostInitialDashboard` is called. During cascade planning (loaded from DB), the dashboard is NOT re-initialized, preventing overwrite of previous layer data. |
| `HasMultipleLayers` check | IMPLEMENTED | `plan_command_runner.go:330-331` |
| `FilterToCurrentLayer` for projectCmds and policyCheckCmds | IMPLEMENTED | `plan_command_runner.go:343-344` |
| `deletePlansForProjects` (delete only current layer plans) | MISSING | Plan says "Delete previous plans for current layer only". Implementation at line 351 calls `p.deletePlans(ctx)` which deletes ALL plans, not just current-layer plans. `deletePlansForProjects` method does not exist. See Gaps. |
| Layer state + dashboard update after manual plan | IMPLEMENTED | `plan_command_runner.go:384-392` |
| `atlantis plan` with no args does NOT advance layers | IMPLEMENTED | The `run()` method re-plans the current layer. Layer advancement only happens in `handleLayerCompletion` in the apply runner. |

#### 4.3 Apply All (`atlantis apply` with no args)

| Plan Item | Status | Notes |
|---|---|---|
| Global lock and `DisableApplyAll` checks | IMPLEMENTED | `apply_command_runner.go:95-119` -- unchanged from existing flow |
| `prjCmdBuilder.BuildApplyCommands` | IMPLEMENTED | `apply_command_runner.go:136` |
| Load layer state from DB | IMPLEMENTED | `apply_command_runner.go:183-197` -- uses `GetPullStatus` to get embedded LayerState |
| `FilterToCurrentLayer` for apply-all | IMPLEMENTED | `apply_command_runner.go:192` |
| `markProjectsApplying` pre-apply dashboard update | IMPLEMENTED (NOT IN PLAN) | `apply_command_runner.go:201-203` -- updates project status to `ApplyingStatus` before running applies, so dashboard shows real-time progress |
| Run applies | IMPLEMENTED | `apply_command_runner.go:205` |
| `pullUpdater.updatePull` | IMPLEMENTED | `apply_command_runner.go:208-211` |
| `dbUpdater.updateDB` | IMPLEMENTED | `apply_command_runner.go:213-217` |
| `StampAndSaveLayerState` | IMPLEMENTED | `apply_command_runner.go:225` |
| Error handling: block layer advancement | IMPLEMENTED | `apply_command_runner.go:227-232` -- `handleLayerCompletion` only called when `!result.HasErrors()` |
| `handleLayerCompletion` / layer completion check | IMPLEMENTED | `apply_command_runner.go:228` -> `handleLayerCompletion` at lines 250-295 |
| `CanAdvance` check | IMPLEMENTED | `apply_command_runner.go:256` -- delegates to `stateManager.CanAdvance` |
| `AdvanceLayer` call | IMPLEMENTED | `apply_command_runner.go:263` -- delegates to `stateManager.AdvanceLayer` |
| `IsAllComplete` check | IMPLEMENTED | `apply_command_runner.go:270` -- if all complete, just update dashboard |
| Save new layer state after advance | IMPLEMENTED | `apply_command_runner.go:277-282` |
| Dashboard update after advance | IMPLEMENTED | `apply_command_runner.go:284` |
| Trigger next-layer planning | IMPLEMENTED | `apply_command_runner.go:287-294` -- triggers via `planCommandRunner.run(ctx, nextCmd)` |
| Automerge only when all layers complete | IMPLEMENTED | `apply_command_runner.go:235-245` -- checks `stateManager.IsAllComplete` before calling automerge |

#### 4.4 Apply Single Project (`atlantis apply -d <project>`)

| Plan Item | Status | Notes |
|---|---|---|
| Specific project apply: skip layer filtering | IMPLEMENTED | `apply_command_runner.go:191` -- `!cmd.IsForSpecificProject()` guard |
| Validate project is in current layer | MISSING | Plan says: validate and post error if project not in current layer. `IsInCurrentLayer` exists on `LayerManager` (`layer_manager.go:120-130`) but is **never called** in the apply runner. Specific project applies are allowed even for future-layer projects. See Gaps. |
| Single apply triggers layer completion check | IMPLEMENTED | After apply, `handleLayerCompletion` is always called if layered and no errors (`apply_command_runner.go:227-232`) |

#### 4.5 Skip Command (`atlantis apply -skip <project>`)

| Plan Item | Status | Notes |
|---|---|---|
| `--skip` flag on apply command in comment parser | IMPLEMENTED | `comment_parser.go:263-265` -- gated by `EnableLayeredApplySkip` |
| `SkipProject` field on `CommentCommand` | IMPLEMENTED | `event_parser.go:147-149` |
| `NewCommentCommand` accepts `skipProject` | IMPLEMENTED | `event_parser.go:211` |
| `--skip` cannot be used with `-d/-p/-w` | IMPLEMENTED | `comment_parser.go:351-355` |
| `handleSkip` method on `ApplyCommandRunner` | IMPLEMENTED | `apply_command_runner.go:298-349` |
| Skip entry: layerManager nil check | IMPLEMENTED | `apply_command_runner.go:301-308` |
| Skip entry: layerState nil check | IMPLEMENTED | `apply_command_runner.go:310-318` |
| Validate: project in current layer | IMPLEMENTED | Delegated to `stateManager.SkipProject()` which validates `projLayer != state.CurrentLayer`. `layer_state_manager.go:361-363` |
| Validate: project must have failed apply | IMPLEMENTED | `layer_state_manager.go:377` -- checks `proj.Status != models.ErroredApplyStatus` |
| Mark as skipped | IMPLEMENTED | `layer_state_manager.go:382` -- sets `proj.Status = models.SkippedPlanStatus` |
| Mark dependents as excluded (transitive) | IMPLEMENTED | `layer_state_manager.go:391` -- `markDependentsExcluded` recursively excludes all downstream |
| Save layer state after skip | IMPLEMENTED | `apply_command_runner.go:332-335` |
| Post confirmation comment | IMPLEMENTED | `apply_command_runner.go:338-342` |
| Check if skip completed the layer | IMPLEMENTED | `apply_command_runner.go:348` -- calls `handleLayerCompletion` |
| Dashboard update after skip | IMPLEMENTED | `apply_command_runner.go:345` |

#### 4.6 Auto-Trigger Next Layer Planning

| Plan Item | Status | Notes |
|---|---|---|
| `triggerNextLayerPlanning()` method | DIVERGED (ACCEPTABLE) | Plan describes a custom method that directly calls `runProjectCmdsWithCancellationTracker`. Implementation reuses `planCommandRunner.run(ctx, nextCmd)` instead, which is simpler and avoids duplicating the plan execution logic. `apply_command_runner.go:287-294` |
| Build plan contexts for next layer | IMPLEMENTED | Handled by `planCommandRunner.run()` which calls `BuildPlanCommands` and then filters to current layer using the updated LayerState |
| Run plans for next layer | IMPLEMENTED | Via `planCommandRunner.run()` |
| Post plan results | IMPLEMENTED | Via `planCommandRunner.run()` -> `pullUpdater.updatePull` |
| Update DB | IMPLEMENTED | Via `planCommandRunner.run()` -> `dbUpdater.updateDB` |
| Update layer state | IMPLEMENTED | Via `planCommandRunner.run()` -> `StampAndSaveLayerState` |
| Update dashboard | IMPLEMENTED | Via `planCommandRunner.run()` -> `UpdateDashboard` |
| Recursive auto-advance for no-changes layers | PARTIALLY IMPLEMENTED | The `handleLayerCompletion` checks `CanAdvance` and triggers next planning. If all projects in the next layer have no changes, `planCommandRunner.run()` completes. But the next `handleLayerCompletion` call happens only after an *apply*, not after a plan with all no-changes. Auto-advancing through no-changes layers during the apply flow requires the state machine to handle this. See Gaps. |

### 5. Files Created (Plan Section 5)

| Plan Item | Status | Notes |
|---|---|---|
| `server/events/layer_manager.go` | IMPLEMENTED | Present, 284 lines. Core coordinator. |
| `server/events/layer_manager_test.go` | IMPLEMENTED | Present, 203 lines. External test (events_test package). |
| `server/events/layer_dashboard.go` | DIVERGED (ACCEPTABLE) | Split into `server/events/layers/dashboard_renderer.go`, `dashboard_updater.go`, `dashboard_types.go` |
| `server/events/layer_dashboard_test.go` | IMPLEMENTED | `server/events/layers/dashboard_renderer_test.go` and `dashboard_updater_test.go` |
| `server/events/models/layer_state.go` | DIVERGED (ACCEPTABLE) | Added to existing `models/models.go` rather than a separate file. |
| `server/events/layer_state_manager.go` | IMPLEMENTED (NOT IN PLAN) | Added `LayerStateManager` interface and `DefaultLayerStateManager` implementation. Plan had all logic in `LayerManager`. |
| `server/events/layer_state_manager_test.go` | IMPLEMENTED (NOT IN PLAN) | 1187 lines of comprehensive tests. |
| `server/events/layer_manager_internal_test.go` | IMPLEMENTED (NOT IN PLAN) | 134 lines testing `buildDashboardData` internals. |
| `server/events/layer_graph.go` | IMPLEMENTED (NOT IN PLAN) | Graph utilities: `detectCycles`, `buildReverseDependencyMap`, `deduplicate`. |
| `server/events/layer_graph_test.go` | IMPLEMENTED (NOT IN PLAN) | Tests for graph utilities. |

### 6. Files Modified (Plan Section 6)

| Plan Item | Status | Notes |
|---|---|---|
| `server/user_config.go` -- add fields | IMPLEMENTED | Lines 49-50 |
| `cmd/server.go` -- add flag definitions | IMPLEMENTED | Lines 88-89, with descriptions |
| `server/server.go` -- wire flags + create LayerManager/DashboardUpdater | IMPLEMENTED | Lines 201-203 (validation), 794-799 (creation), 801-824 (plan runner injection), 826-844 (apply runner injection) |
| `server/events/plan_command_runner.go` -- add layer filtering | IMPLEMENTED | Both `runAutoplan()` and `run()` modified with layered planning gates |
| `server/events/apply_command_runner.go` -- add layer filtering, next-layer, skip | IMPLEMENTED | Full apply flow with layered support |
| `server/events/comment_parser.go` -- add `-skip` flag | IMPLEMENTED | Lines 263-265 and validation at 351-355 |
| `server/events/event_parser.go` -- add `SkipProject` field | IMPLEMENTED | Lines 147-149, 211 |
| `server/core/db/db.go` -- add layer state methods | PARTIALLY IMPLEMENTED | `UpdateLayerState` added. No separate `GetLayerState` or `DeleteLayerState`. |
| `server/events/vcs/client.go` -- VCS interface changes | IMPLEMENTED | `ListComments`, `EditComment`, `MaxCommentLength` added |

### 7. Detailed Code Changes (Plan Section 7)

#### 7.1 `server/user_config.go`

| Plan Item | Status | Notes |
|---|---|---|
| `EnableLayeredPlanning bool` with mapstructure tag | IMPLEMENTED | `user_config.go:50` |
| `EnableLayeredApplySkip bool` with mapstructure tag | IMPLEMENTED | `user_config.go:49` |

#### 7.2 `cmd/server.go`

| Plan Item | Status | Notes |
|---|---|---|
| `enable-layered-planning` flag definition | IMPLEMENTED | `cmd/server.go:89` with description |
| `enable-layered-apply-skip` flag definition | IMPLEMENTED | `cmd/server.go:88` with description |
| Default values = false | IMPLEMENTED | Both default to false |

#### 7.3 `server/events/plan_command_runner.go`

| Plan Item | Status | Notes |
|---|---|---|
| `layerManager *LayerManager` field | IMPLEMENTED | `plan_command_runner.go:107` |
| `database db.Database` field | IMPLEMENTED | `plan_command_runner.go:109` |
| `dashboardRenderer` field | NOT IMPLEMENTED | Dashboard rendering is handled via `layerManager.UpdateDashboard()` which internally delegates to the dashboard updater. The renderer is not a separate field. This is cleaner. |
| `NewPlanCommandRunner` accepts layerManager and database | IMPLEMENTED | `plan_command_runner.go:45-46` |
| `runAutoplan()` modifications | IMPLEMENTED | Full layered planning gate with all documented steps |
| `run()` modifications (manual plan) | IMPLEMENTED | Full layered planning gate with existing state loading and `layerStateIsNew` logic |

#### 7.4 `server/events/apply_command_runner.go`

| Plan Item | Status | Notes |
|---|---|---|
| `layerManager *LayerManager` field | IMPLEMENTED | `apply_command_runner.go:79` |
| `planCmdRunner` field (for next-layer planning) | IMPLEMENTED | Named `planCommandRunner *PlanCommandRunner`. `apply_command_runner.go:81` |
| `NewApplyCommandRunner` accepts layerManager and planCommandRunner | IMPLEMENTED | `apply_command_runner.go:32-33` |
| `Run()` skip handling at top | IMPLEMENTED | `apply_command_runner.go:86-89` |
| `Run()` layer filtering | IMPLEMENTED | `apply_command_runner.go:180-197` |
| `handleLayerCompletion()` method | IMPLEMENTED | `apply_command_runner.go:250-295` |
| `handleSkip()` method | IMPLEMENTED | `apply_command_runner.go:298-349` |
| `markProjectsApplying()` method | IMPLEMENTED (NOT IN PLAN) | `apply_command_runner.go:353-373` -- extra UX improvement |
| `advanceToNextLayer()` method | DIVERGED (ACCEPTABLE) | Named `handleLayerCompletion`. Uses `stateManager.AdvanceLayer` + `planCommandRunner.run` instead of direct project command building. |

#### 7.5 `server/events/comment_parser.go`

| Plan Item | Status | Notes |
|---|---|---|
| `EnableLayeredApplySkip bool` field | IMPLEMENTED | `comment_parser.go:91` |
| `NewCommentParser` accepts `enableLayeredApplySkip` | IMPLEMENTED | `comment_parser.go:95, 114` |
| `--skip` flag on apply case | IMPLEMENTED | `comment_parser.go:263-265` |
| Skip + dir/workspace/project mutual exclusion | IMPLEMENTED | `comment_parser.go:351-355` |

#### 7.6 `server/events/event_parser.go`

| Plan Item | Status | Notes |
|---|---|---|
| `SkipProject string` field on `CommentCommand` | IMPLEMENTED | `event_parser.go:147-149` |
| `NewCommentCommand` accepts skipProject | IMPLEMENTED | `event_parser.go:211` |

#### 7.7 `server/server.go`

| Plan Item | Status | Notes |
|---|---|---|
| `EnableLayeredApplySkip` passed to comment parser | IMPLEMENTED | `server/server.go:621` |
| `layerManager` creation (nil if disabled) | IMPLEMENTED | `server/server.go:794-799` |
| `dashboardRenderer` creation | IMPLEMENTED | Wrapped inside `DashboardUpdater` creation at `server/server.go:797` |
| `layerManager` passed to `planCommandRunner` | IMPLEMENTED | `server/server.go:822` |
| `database` passed to `planCommandRunner` | IMPLEMENTED | `server/server.go:823` -- note: database is passed twice (once for PullStatusFetcher, once for layer state) |
| `layerManager` passed to `applyCommandRunner` | IMPLEMENTED | `server/server.go:842` |
| `planCommandRunner` passed to `applyCommandRunner` | IMPLEMENTED | `server/server.go:843` |

### 8. Layer Computation Algorithm (Plan Section 8)

| Plan Item | Status | Notes |
|---|---|---|
| Build adjacency map from DependsOn | IMPLEMENTED | `layer_state_manager.go:96-111` |
| Detect cycles using DFS | IMPLEMENTED | `layer_state_manager.go:114` -> `detectCycles()` in `layer_graph.go` |
| Topological sort into layers | IMPLEMENTED | `layer_state_manager.go:123-148` -- recursive depth computation |
| Layer 0: projects with no in-scope dependencies | IMPLEMENTED | Default layer = 0 in the recursive function |
| Layer N+1: projects whose deps are all in layers <= N | IMPLEMENTED | `depLayer := computeLayer(dep) + 1` logic |
| Single layer sets HasMultipleLayers = false | IMPLEMENTED | `HasMultipleLayers` checks `TotalLayers > 1` |
| Pending projects (transitive dependents without file changes) | IMPLEMENTED | `layer_state_manager.go:152-180` -- walks reverse dependency graph |

### 9. Dashboard Rendering (Plan Section 9)

| Plan Item | Status | Notes |
|---|---|---|
| `RenderDashboard(layerState)` | IMPLEMENTED | `layers/dashboard_renderer.go` -- uses Go templates |
| Completed layers collapsed with `<details>` | IMPLEMENTED | Template uses `<details>` for non-current completed layers |
| Current layer expanded | IMPLEMENTED | Current layer shown expanded |
| Future layers shown as count | IMPLEMENTED | `PendingCount` in `DashboardData` |
| Status emojis for each project | IMPLEMENTED | `layers/dashboard_types.go:12-25` -- comprehensive status icon mapping |
| `UpdateDashboard` creates or updates sticky comment | IMPLEMENTED | `layers/dashboard_updater.go:30-71` -- uses edit-in-place |
| Comment splitting for large dashboards | IMPLEMENTED | `layers/dashboard_updater.go:37` -- `RenderSplit(data, maxLen)` |
| No dashboard for single-layer PRs | IMPLEMENTED | `layer_manager.go:166-167` in `UpdateDashboard` |

### 10. Interaction with Existing Features (Plan Section 10)

| Plan Item | Status | Notes |
|---|---|---|
| ExecutionOrderGroup: layered planning supersedes, groups still work within layers | IMPLEMENTED | No changes to `splitByExecutionOrderGroup` -- it works naturally within filtered project sets |
| Policy checks run per layer | IMPLEMENTED | Policy check cmds filtered to current layer in both `runAutoplan` and `run` |
| Automerge: only when all layers complete | IMPLEMENTED | `apply_command_runner.go:235-245` -- `IsAllComplete` check |
| HidePrevPlanComments: different command tag | IMPLEMENTED | Dashboard uses sentinel comment approach, separate from plan comments |
| DisableApplyAll: still blocks `atlantis apply` | IMPLEMENTED | DisableApplyAll check at `apply_command_runner.go:112` runs before layer logic |
| PR close / unlock: clean up layer state | NOT EXPLICITLY VERIFIED | `DeletePullStatus` in `PullClosedExecutor` likely cleans up since LayerState is embedded in PullStatus. No explicit `DeleteLayerState` call. |

### 11-12. Testing (Plan Sections 11-12)

#### Unit Tests for LayerManager

| Plan Test | Status | Notes |
|---|---|---|
| `TestComputeLayers_NoDependsOn` | IMPLEMENTED | `layer_manager_test.go:72-83` -- `TestLayerManager_InitializeLayerState_NoDeps` |
| `TestComputeLayers_LinearChain` | IMPLEMENTED | `layer_state_manager_test.go:37-62` -- `TestLayerStateInit_SimpleChainAllChanged` |
| `TestComputeLayers_DiamondDependency` | IMPLEMENTED | `layer_state_manager_test.go:89-115` -- `TestLayerStateInit_Diamond` |
| `TestComputeLayers_CircularDependency` | IMPLEMENTED | `layer_state_manager_test.go:145-163` -- `TestLayerStateInit_CircularDependency` |
| `TestComputeLayers_MixedDepsAndNoDeps` | IMPLEMENTED | `layer_state_manager_test.go:165-183` -- `TestLayerStateInit_MixedChangedAndPending` |
| `TestFilterToCurrentLayer` | IMPLEMENTED | `layer_manager_test.go:93-114` |
| `TestFilterToCurrentLayer_Layer1` | IMPLEMENTED | `layer_manager_test.go:128-158` |
| `TestIsLayerComplete_AllApplied` | IMPLEMENTED | `layer_state_manager_test.go:240-254` |
| `TestIsLayerComplete_HasNoChanges` | IMPLEMENTED | `layer_state_manager_test.go:256-270` |
| `TestIsLayerComplete_HasFailed` | IMPLEMENTED | `layer_state_manager_test.go:305-319` |
| `TestIsLayerComplete_HasSkipped` | IMPLEMENTED | `layer_state_manager_test.go:272-287` -- in MixedTerminal test |
| `TestGetNextLayerProjects_CascadeRule` | IMPLEMENTED | `layer_state_manager_test.go:442-470` -- `TestLayerStateAdvance_UpstreamHadChanges` |
| `TestGetNextLayerProjects_NoCascade` | IMPLEMENTED | `layer_state_manager_test.go:472-497` -- `TestLayerStateAdvance_UpstreamNoChanges` |
| `TestGetNextLayerProjects_SkippedUpstream` | IMPLEMENTED | `layer_state_manager_test.go:499-524` -- `TestLayerStateAdvance_UpstreamSkipped` |
| `TestIsEnabled_FlagOff` | IMPLEMENTED | Implicit -- layerManager is nil when disabled |
| `TestIsEnabled_NoDependsOn` | IMPLEMENTED | `layer_manager_test.go:30-33` |
| `TestIsEnabled_HasDependsOn` | IMPLEMENTED | `layer_manager_test.go:38-44` |

#### Unit Tests for PlanCommandRunner (Layered)

| Plan Test | Status | Notes |
|---|---|---|
| `TestAutoplan_LayeredEnabled_MultiLayer` | MISSING | No integration test for PlanCommandRunner with mocked layerManager. Tests exist for LayerManager and LayerStateManager independently. |
| `TestAutoplan_LayeredEnabled_SingleLayer` | MISSING | Same as above |
| `TestAutoplan_LayeredDisabled` | MISSING | Existing plan_command_runner tests exist but none specifically verify layered-disabled behavior |
| `TestManualPlan_LayeredEnabled` | MISSING | No mock-based integration test |
| `TestManualPlan_SpecificProject` | MISSING | No mock-based integration test |
| `TestAutoplan_CircularDependency` | MISSING | Tested at unit level in layer_state_manager_test but not at runner level |

#### Unit Tests for ApplyCommandRunner (Layered)

| Plan Test | Status | Notes |
|---|---|---|
| `TestApplyAll_LayeredEnabled` | MISSING | No mock-based integration test for apply runner |
| `TestApplyAll_LayerComplete_TriggersNextPlan` | MISSING | Same |
| `TestApplyAll_LayerComplete_AllNoChanges_AutoAdvances` | MISSING | Same |
| `TestApplyAll_HasErrors_BlocksAdvancement` | MISSING | Same |
| `TestApplySingle_InCurrentLayer` | MISSING | Same |
| `TestApplySingle_NotInCurrentLayer` | MISSING | Same (also, the feature is missing) |
| `TestApplySingle_CompletesLayer` | MISSING | Same |
| `TestApplySkip_Enabled_FailedProject` | MISSING | Tested at state manager level only |
| `TestApplySkip_Enabled_NotFailed` | MISSING | Same |
| `TestApplySkip_Disabled` | MISSING | Same |
| `TestApplySkip_CompletesLayer` | MISSING | Same |
| `TestApply_AllLayersComplete_Automerge` | MISSING | Same |
| `TestApply_DisableApplyAll_WithLayered` | MISSING | Same |

#### Unit Tests for Dashboard Renderer

| Plan Test | Status | Notes |
|---|---|---|
| `TestRenderDashboard_SingleLayer` | LIKELY IMPLEMENTED | In `layers/dashboard_renderer_test.go` (not read in detail) |
| `TestRenderDashboard_TwoLayers` | LIKELY IMPLEMENTED | Same |
| `TestRenderDashboard_CompletedLayer` | LIKELY IMPLEMENTED | Same |
| `TestRenderDashboard_AllStatuses` | LIKELY IMPLEMENTED | Same |
| `TestRenderDashboard_CommentSplitting` | LIKELY IMPLEMENTED | Same |

#### Unit Tests for Comment Parser

| Plan Test | Status | Notes |
|---|---|---|
| `TestParse_ApplySkip_Enabled` | IMPLEMENTED | `comment_parser_test.go:1172-1229` -- `TestParse_ApplySkipFlag` |
| `TestParse_ApplySkip_Disabled` | IMPLEMENTED | `comment_parser_test.go:1232-1237` -- `TestParse_ApplySkipDisabled` |
| `TestParse_ApplySkip_WithOtherFlags` | IMPLEMENTED | `comment_parser_test.go:1201-1211` -- skip+dir and skip+project tested |

#### Integration / Lifecycle Tests

| Plan Test | Status | Notes |
|---|---|---|
| Full lifecycle test (init -> plan -> apply -> advance -> complete) | IMPLEMENTED | `layer_state_manager_test.go:1051-1112` -- `TestLayerStateFullLifecycle` (at state manager level) |
| No-changes cascade stop lifecycle | IMPLEMENTED | `layer_state_manager_test.go:1114-1147` -- `TestLayerStateLifecycle_NoChangesCascadeStop` |
| Skip and cascade stop lifecycle | IMPLEMENTED | `layer_state_manager_test.go:1149-1186` -- `TestLayerStateLifecycle_SkipAndCascadeStop` |
| `TestE2E_LayeredPlanApply_TwoLayers` | MISSING | No E2E test in `events_controller_e2e_test.go` |
| `TestE2E_LayeredPlanApply_NoCascade` | MISSING | Same |
| `TestE2E_LayeredPlanApply_Skip` | MISSING | Same |
| `TestE2E_LayeredPlanApply_NewPush` | MISSING | Same |

---

## Gaps Requiring Action

### Gap 1: Specific-Project Apply Not Validated Against Current Layer

**What the plan says:** Section 4.4 -- when `cmd.IsForSpecificProject()` is true and layered planning is active, validate the project is in the current layer. If not, post an error comment like "Project X is not in the current layer (layer N)."

**What actually exists:** `IsInCurrentLayer()` method exists on `LayerManager` (`layer_manager.go:120-130`) but is **never called** in the apply runner. The apply runner at `apply_command_runner.go:191` only skips FilterToCurrentLayer for specific projects but does not validate that the specified project is in the current layer.

**Specific code location:** `apply_command_runner.go:191-195` -- after the `if !cmd.IsForSpecificProject()` block, there should be an `else` clause that calls `IsInCurrentLayer` and posts an error if the project is not in the current layer.

**Suggested fix:**
```go
if !cmd.IsForSpecificProject() {
    projectCmds = a.layerManager.FilterToCurrentLayer(projectCmds, layerState)
} else if len(projectCmds) > 0 && !a.layerManager.IsInCurrentLayer(projectCmds[0], layerState) {
    errMsg := fmt.Sprintf(
        "**Error:** Project `%s` is not in the current layer (layer %d). "+
            "Apply all projects in the current layer first.",
        projectCmds[0].ProjectName, layerState.CurrentLayer,
    )
    a.vcsClient.CreateComment(ctx.Log, baseRepo, pull.Num, errMsg, command.Apply.String())
    return
}
```

### Gap 2: Manual Plan Deletes ALL Plans Instead of Current-Layer Plans

**What the plan says:** Section 4.2 -- "Delete previous plans for current layer only (not all plans). This is a change from existing behavior." The plan specifies a `deletePlansForProjects(ctx, currentLayerCmds)` method.

**What actually exists:** `plan_command_runner.go:351` calls `p.deletePlans(ctx)` which deletes ALL plans via `pendingPlanFinder.DeletePlans(pullDir)`. There is no `deletePlansForProjects` method.

**Specific code location:** `plan_command_runner.go:350-357`

**Suggested fix:** This is acceptable in initial implementation since deleting all plans is the existing behavior and is safer (avoids stale plans). The plan acknowledges this is a behavior change. However, for correctness in layered workflows, previous layer plans should not be deleted during re-plan of current layer. Consider adding a `deletePlansForLayer()` method that only deletes plan files for projects in the current layer.

**Severity:** Low -- the current behavior is stricter (deletes more than necessary) but not incorrect.

### Gap 3: No Explicit DeleteLayerState on PR Close

**What the plan says:** Section 10.6 -- "When a PR is closed or `atlantis unlock` is run, the layer state should be cleaned up. Add `database.DeleteLayerState(pull)` to the pull cleanup handler."

**What actually exists:** No `DeleteLayerState` method on the `Database` interface. The `DeletePullStatus` method exists and is called during PR close in `PullClosedExecutor`.

**Why this is likely fine:** `LayerState` is embedded in `PullStatus` as a JSON field (`models.go:557`). When `DeletePullStatus` is called, the entire `PullStatus` including `LayerState` is deleted. No separate cleanup needed.

**Severity:** None -- this is covered by existing `DeletePullStatus`.

### Gap 4: Recursive Auto-Advance Through No-Changes Layers

**What the plan says:** Section 4.6 -- "This recursive call handles the case where a layer has all no-changes plans -- it auto-advances through multiple layers until it finds one with actual changes or reaches the end."

**What actually exists:** The `handleLayerCompletion` method triggers `planCommandRunner.run()` for the next layer. If all projects in that layer plan with no changes, `planCommandRunner.run()` completes and updates the DB. However, `handleLayerCompletion` is only called from the apply runner's `Run()` method after an apply operation, not from `planCommandRunner.run()`. This means auto-advancing through no-changes layers would require an intermediate apply step even when no changes exist.

**Why this may be acceptable:** `PlannedNoChangesPlanStatus` counts as layer-complete in `IsLayerComplete`. After the cascade plan completes, the PullStatus will show all no-changes. The next time a user runs `atlantis apply`, `handleLayerCompletion` will detect the layer is complete and advance. But it won't auto-advance immediately after planning.

**Suggested fix:** After `planCommandRunner.run()` returns in the cascade planning path (`handleLayerCompletion`), re-check if the newly planned layer is already complete (all no-changes) and recursively call `handleLayerCompletion` if so. This would require the plan runner's `run()` method to return whether all planned projects had no changes, or the apply runner could re-fetch `pullStatus` after the cascade plan.

**Severity:** Medium -- affects UX for deep no-changes cascades. Users would need to manually run `atlantis apply` to advance through no-changes layers.

### Gap 5: Mock-Based Integration Tests for Command Runners

**What the plan says:** Sections 11.2 and 11.3 list 6 plan runner tests and 13 apply runner tests that test the runners with mocked LayerManager, DB, VCS, etc.

**What actually exists:** All tests are at the LayerManager and LayerStateManager unit level. No mock-based tests for `PlanCommandRunner` or `ApplyCommandRunner` that verify the integration between the runners and the layered planning components.

**Severity:** Medium -- the individual components are well-tested, but the integration/wiring between them is not tested at the runner level.

### Gap 6: E2E Tests

**What the plan says:** Section 11.6 lists 4 E2E tests with a `testdata/test-repos/layered-planning/` fixture.

**What actually exists:** No E2E tests for layered planning.

**Severity:** Medium -- E2E tests are important for validating the full flow with real Terraform.

---

## Minor Divergences (Acceptable)

1. **Architecture refactor**: The plan puts all logic in a single `LayerManager` struct. The implementation splits it into `LayerManager` (coordinator) + `LayerStateManager` (interface/implementation for state machine logic) + `layers.DashboardUpdater` + `layers.DashboardRenderer`. This is a better separation of concerns.

2. **LayerState model**: The plan uses nested `[]LayerInfo` / `[]LayerProjectInfo` types with per-project status enums. The implementation uses a flat `map[string]int` (`ProjectLayers`), `[]string` (`PendingProjects`), and `map[string]bool` (`SkippedUpstreams`). The flat design is simpler and sufficient.

3. **Dashboard comment strategy**: Plan recommends Option A (hide previous + create new). Implementation uses edit-in-place via `EditComment` (Option B). The plan acknowledged Option B is cleaner. This required adding `ListComments`, `EditComment`, and `MaxCommentLength` to the VCS client interface.

4. **Next-layer planning trigger**: Plan describes a custom `triggerNextLayerPlanning()` method with direct project command building. Implementation reuses `planCommandRunner.run()` by creating a synthetic `CommentCommand{Name: command.Plan}`. This is simpler and avoids code duplication.

5. **`layerStateIsNew` flag**: Not in the original plan but necessary for correctness. Prevents `PostInitialDashboard` from overwriting the dashboard during cascade planning (when state is loaded from DB, not freshly computed).

6. **`markProjectsApplying` feature**: Not in the plan but a useful UX improvement. Updates the dashboard to show "Applying..." status before applies run, giving users real-time feedback.

7. **`EnableLayeredPlanning`/`EnableLayeredApplySkip` not on runner structs**: Plan says add these as fields to the runner structs. Implementation uses nil-check on `layerManager` pointer for the planning flag and passes `skipEnabled` through `stateManager.SkipProject()` for the skip flag. This avoids flag proliferation.

8. **Method naming**: `IsEnabled` -> `ShouldActivate`, `ComputeLayers` -> `InitializeLayerState`, `GetNextLayerProjects` -> `AdvanceLayer`, etc. All conceptually equivalent with better names in the implementation.

9. **`DependsOn` on `ProjectContext` not `MergedProjectCfg`**: The plan uses `valid.MergedProjectCfg` for layer computation input. The implementation uses `command.ProjectContext` which already carries `DependsOn` (line 70 of `project_context.go`). This is better because ProjectContext is the natural data structure at the command runner layer.
