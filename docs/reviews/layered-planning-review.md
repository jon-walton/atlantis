# Layered Planning Code Review - feature/layered-planning

## Summary

This review covers the layered planning feature on the `feature/layered-planning` branch compared to `main`. The feature introduces dependency-aware plan/apply ordering: projects with `depends_on` are topologically sorted into layers, planned and applied sequentially layer-by-layer, with cascade evaluation determining whether downstream projects need planning based on upstream results.

**Scope**: Core state machine (`layer_state_manager.go`, `layer_graph.go`), lifecycle coordinator (`layer_manager.go`), dashboard rendering (`layers/` package), plan/apply runner integration (`plan_command_runner.go`, `apply_command_runner.go`), model types (`models.go`), database persistence (`boltdb.go`, `redis.go`), and tests.

**Overall Assessment**: The design is well-structured with clean separation of concerns. The topological sort, cascade evaluation, and skip logic are correct for the tested cases. The dashboard rendering and VCS comment management are thoughtfully implemented. However, there are several issues: a key consistency bug affecting projects without explicit names, a missing persistence call for skip status, a race condition in Redis layer state updates, an unwired `HandleNewCommit` feature, and the `AdvanceLayer` method mutating shared state in-place without making this ownership transfer explicit.

---

## Findings

### [HIGH] Project Key Inconsistency Between StampLayerAssignments and ProjectLayers Map

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layer_state_manager.go:533-543`
- **Description**: `StampLayerAssignments` looks up projects in `ProjectLayers` using only `proj.ProjectName` (line 539). However, `projectContextKey()` in `layer_manager.go:268-273` generates keys as `RepoRelDir + "::" + Workspace` when `ProjectName` is empty. If projects are configured without explicit names, the `ProjectLayers` map will contain keys like `"infra/vpc::default"` but `StampLayerAssignments` will look up the empty string `""`, failing to match and leaving `Layer` at its zero value. The same inconsistency exists in `IsLayerComplete` (line 224), `AdvanceLayer` (line 267), `HandleNewCommit` (line 450), and `SkipProject` (line 369), all of which match on `proj.ProjectName` only. `GetLayerSummary` (lines 504-507) is the only method that handles both key formats.
- **Impact**: For repos that use directory-based project identification without explicit `name` in `atlantis.yaml`, layer assignments will not be stamped correctly on `ProjectStatus` entries. This will cause the dashboard to show incorrect layer associations, `IsLayerComplete` to potentially miss projects (treating them as not in the target layer), and `SkipProject` to fail to find the project.
- **Recommendation**: Extract the key derivation logic into a shared helper function (e.g., `projectStatusKey(proj ProjectStatus) string`) that mirrors `projectContextKey()` and use it consistently across all methods that look up projects in the `ProjectLayers` map. Consider: `func projectStatusKey(p ProjectStatus) string { if p.ProjectName != "" { return p.ProjectName }; return p.RepoRelDir + "::" + p.Workspace }`.

### [HIGH] SkipProject Does Not Persist ProjectStatus Change to Database

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/apply_command_runner.go:297-349`
- **Description**: The `handleSkip` method calls `SkipProject()` which modifies the in-memory `pullStatus.Projects[i].Status` from `ErroredApplyStatus` to `SkippedPlanStatus` (layer_state_manager.go:382). It then saves the `LayerState` via `UpdateLayerState` (line 333) but never calls `UpdateProjectStatus` or `UpdatePullWithResults` to persist the project's new `SkippedPlanStatus` to the database. If Atlantis restarts after a skip, the project status will revert to `ErroredApplyStatus` from the DB while `SkippedUpstreams` in `LayerState` will correctly show it as skipped, creating an inconsistency.
- **Impact**: After restart, `IsLayerComplete` will see `ErroredApplyStatus` and report the layer as blocked, while cascade evaluation will correctly exclude dependents. The layer will be stuck, requiring manual intervention.
- **Recommendation**: Add a `Database.UpdateProjectStatus` call in `handleSkip` to persist the `SkippedPlanStatus`:
  ```go
  if a.Database != nil {
      if err := a.Database.UpdateProjectStatus(pull, /* workspace */, /* repoRelDir */, models.SkippedPlanStatus); err != nil {
          ctx.Log.Err("persisting skip status: %s", err)
      }
  }
  ```
  Note: the `SkipProject` method currently operates on `ProjectName` only and doesn't expose workspace/repoRelDir, which ties back to the key consistency issue above.

### [MEDIUM] Redis UpdateLayerState Has Read-Modify-Write Race Condition

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:418-439`
- **Description**: `UpdateLayerState` performs a non-atomic read-modify-write: it reads the current pull status (GET), modifies the `LayerState` field in-memory, then writes the full status back (SET). If two concurrent operations (e.g., two webhook handlers for the same PR) both call `UpdateLayerState`, one update will be lost. Unlike BoltDB which uses `db.Update` with bolt's serialized transactions, Redis has no such protection.
- **Impact**: In multi-instance Atlantis deployments using Redis, concurrent plan/apply operations on the same PR could lose layer state updates. This could cause layer advancement to be missed or project statuses to be overwritten.
- **Recommendation**: Use a Redis transaction with WATCH/MULTI/EXEC or a Lua script to make the read-modify-write atomic. Alternatively, use Redis `WATCH` on the key and retry on conflict:
  ```go
  err := r.client.Watch(ctx, func(tx *redis.Tx) error {
      // read, modify, write within the transaction
  }, key)
  ```

### [MEDIUM] AdvanceLayer Mutates Shared LayerState In-Place

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layer_state_manager.go:253-344`
- **Description**: `AdvanceLayer` directly mutates the `LayerState` passed via `pullStatus.LayerState`: it modifies `ProjectLayers` (line 319), `PendingProjects` (line 326), `CurrentLayer` (lines 331, 337, 340), and `TotalLayers` (line 333). The caller (`handleLayerCompletion` in `apply_command_runner.go:263`) then also assigns the returned state pointer to `pullStatus.LayerState` (line 277), which is the same pointer. This creates unclear ownership semantics -- the method both mutates in-place AND returns the mutated pointer.
- **Impact**: Not currently a bug since there's a single caller operating on a single goroutine per PR, but this pattern is fragile. If the method is called concurrently or if the caller needs rollback on error, the in-place mutation makes that impossible. It also makes unit testing more subtle since assertions on the input object are affected.
- **Recommendation**: Either (a) make the mutation contract explicit in the docstring and remove the return value (since it's the same pointer), or (b) clone the `LayerState` at the top of `AdvanceLayer` and return the new copy, leaving the original unmodified until the caller explicitly adopts it.

### [MEDIUM] HandleNewCommit Is Defined But Not Wired Into Any Command Runner

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layer_state_manager.go:423-475`
- **Description**: `HandleNewCommit` is fully implemented and tested but never called from any command runner. The design doc specifies smart re-plan behavior when new commits are pushed affecting projects in the current, future, or completed layers. Without this wiring, a push to a PR with active layered planning will simply re-initialize the entire layer state from scratch (in `plan_command_runner.go:319-320`), losing all progress.
- **Impact**: Users will lose all layer progress (applied layers, cascade decisions) whenever new commits are pushed to a PR with active layered planning. This directly contradicts the design doc's "Smart Re-plan on New Commits" section which specifies targeted re-planning.
- **Recommendation**: Wire `HandleNewCommit` into the `runAutoplan` and `run` methods of `PlanCommandRunner`. When existing `LayerState` is found in the DB for a push event, call `HandleNewCommit` to determine the `ResetAction` before deciding whether to re-initialize or selectively re-plan.

### [MEDIUM] Plan Runner Deletes All Plans and Locks Before Layered Planning

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/plan_command_runner.go:177-182`
- **Description**: In `runAutoplan`, after filtering to the current layer's projects, the code unconditionally deletes ALL plans and locks (lines 177-182), not just those for the current layer. During cascade planning (when `handleLayerCompletion` triggers a new plan for the next layer), this would delete plans and locks from already-completed layers. The same issue exists in the manual `run` method (lines 350-357, though gated by `!cmd.IsForSpecificProject()`).
- **Impact**: When layer N completes and triggers planning for layer N+1, the plans and locks for layer N's applied projects are deleted. This may not cause immediate issues (since those projects are already applied), but it removes the lock records that would prevent conflicting operations and potentially causes issues if there's a need to reference prior plans.
- **Recommendation**: When layered planning is active, only delete plans and locks for projects in the current layer being re-planned, not all projects. This could be implemented by passing the filtered project list to a targeted unlock/delete method.

### [LOW] Hardcoded `true` for skipEnabled Bypasses Configuration Flag

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/apply_command_runner.go:321`
- **Description**: The `handleSkip` method calls `SkipProject(pullStatus, cmd.SkipProject, true)` with `skipEnabled` hardcoded to `true`. The `enable-layered-apply-skip` configuration flag is only enforced at the comment parser level (the `--skip` flag is not registered when the config is disabled), so this is defense-in-depth rather than a functional bypass. However, any future callers (e.g., API endpoints) would bypass the config check.
- **Impact**: Currently mitigated by the parser gate. Risk increases if skip is exposed through other entry points in the future.
- **Recommendation**: Store the `enableLayeredApplySkip` config on `ApplyCommandRunner` and pass it to `SkipProject` instead of hardcoding `true`. This ensures the check works regardless of entry point.

### [LOW] DashboardRenderer Silently Discards Template Parse Errors

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layers/dashboard_renderer.go:32`
- **Description**: `NewDashboardRenderer` discards the error from `template.ParseFS`: `templates, _ := template.New("").Funcs(sprig.TxtFuncMap()).ParseFS(...)`. If the embedded template has a syntax error (e.g., after a refactor), `templates` will be nil, and `Render` will return the "Failed to find layered dashboard template" fallback string.
- **Impact**: Template parse errors during initialization will surface as runtime rendering failures rather than startup failures, making them harder to diagnose.
- **Recommendation**: Return an error from `NewDashboardRenderer` or panic on template parse failure (since embedded templates should always be valid at compile time). Similar treatment to the override parse is acceptable (log and continue), but the embedded template failure should be fatal.

### [LOW] Cycle Detection Uses Append Without Capacity Pre-allocation

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layer_graph.go:43`
- **Description**: In `detectCycles`, line 43 uses `append(pathSlice[cycleStart:], dep)` which may mutate the original `pathSlice` backing array when there's sufficient capacity. The function does `pathSlice = pathSlice[:len(pathSlice)-1]` on line 53, which shares the same backing array. This is safe in practice because the function returns immediately after finding a cycle (the append result is only used to build the error message), but it's a subtle correctness dependency.
- **Impact**: No current bug, but future modifications to the DFS that continue traversal after finding the first cycle could produce incorrect cycle paths due to shared slice backing.
- **Recommendation**: Use an explicit copy: `cyclePath := make([]string, 0, len(pathSlice)-cycleStart+1); cyclePath = append(cyclePath, pathSlice[cycleStart:]...); cyclePath = append(cyclePath, dep)`.

### [LOW] AdvanceLayer Does Not Handle Pending Projects With No Dependencies In Graph

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layer_state_manager.go:283-324`
- **Description**: In `AdvanceLayer`, pending projects are evaluated by iterating their dependencies (line 284: `deps := state.DependencyGraph[pendingProject]`). If a pending project has no entry in the dependency graph (i.e., `deps` is nil), the `for` loop on line 288 iterates zero times, `shouldInclude` stays `false`, and `allDepsResolved` stays `true`, so the project is silently dropped. This could happen if the dependency graph was built from a different set of projects than the pending list.
- **Impact**: A data consistency issue between `PendingProjects` and `DependencyGraph` would cause projects to be silently excluded. In practice, `InitializeLayerState` builds both from the same data, so this is unlikely.
- **Recommendation**: Add a defensive check: if a pending project has no dependencies at all, it should not have been in the pending list. Log a warning if this case is encountered.

### [INFO] Layer Zero Value Ambiguity in ProjectStatus

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/models/models.go:615`
- **Description**: `ProjectStatus.Layer` is an `int` with zero value 0, and layer 0 is a valid layer. The `json:"layer,omitempty"` tag means projects genuinely in layer 0 will have their layer omitted during JSON serialization, making it indistinguishable from "no layer assigned" when deserialized. The `GetLayerSummary` method (line 502) checks `proj.Layer >= 0` which always passes for the zero value.
- **Impact**: When deserializing from the database, projects without layered planning and projects in layer 0 are indistinguishable. Currently mitigated by the `LayerState == nil` check that gates most layer logic, but could cause subtle issues if `LayerState` is present while some projects haven't been assigned layers yet.
- **Recommendation**: Consider using `*int` (pointer to int) for the `Layer` field, or change the `omitempty` tag to include layer 0 explicitly, or use -1 as the "no layer" sentinel value (set in the default ProjectStatus constructor).

---

## Strengths

- **Clean separation of concerns**: The `LayerStateManager` interface cleanly separates state machine logic from the lifecycle coordination in `LayerManager` and the rendering in `DashboardUpdater`. This makes each component independently testable.

- **Comprehensive test coverage for state machine**: The `layer_state_manager_test.go` file covers initialization, layer completion, advancement with cascade, skip with transitive exclusion, HandleNewCommit, and full lifecycle scenarios. Table-driven tests are used where appropriate.

- **Correct topological sort**: The recursive depth computation in `InitializeLayerState` correctly handles diamond dependencies, parallel chains, and mixed changed/pending projects. The cycle detection is a proper three-color DFS.

- **Robust dashboard comment management**: The `DashboardUpdater` handles create, update, shrink (clearing excess comments), and fallback-on-edit-failure patterns. Comment splitting at layer boundaries is a thoughtful approach to VCS character limits.

- **Progressive scope discovery**: The pending projects mechanism (tracking transitive dependents that haven't been assigned layers yet) correctly implements the design doc's progressive cascade evaluation, where downstream projects are only included after confirming upstream changes.

- **Defense-in-depth on skip**: Skip validation checks three conditions (enabled flag, current layer membership, errored-apply status) before allowing the operation, with clear error messages for each failure mode.

- **Dashboard template design**: Using Go's embedded FS for the template with an override directory for customization is a good pattern. The `normalizeMarkdown` cleanup and the sentinel-based comment identification are simple and effective.

- **Consistent error handling in persistence**: Both BoltDB and Redis implementations handle the nil-pull-status case gracefully (returning nil error) rather than treating it as a failure, which prevents false error propagation when layer state is updated before any plans have been saved.
