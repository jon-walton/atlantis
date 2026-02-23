# Test Coverage and Quality Review - feature/layered-planning

## Summary

This review covers all test code on the `feature/layered-planning` branch compared to `main`. The branch adds 21 test files (new or modified) alongside 32 production files, covering layered planning state management, dashboard rendering/updating, output persistence, web UI controllers, database backends (BoltDB and Redis), project command output handling, CSRF protection, and template rendering.

**Scope**: 21 test files reviewed, ~3,800 lines of test code added. Cross-referenced against 32 modified production files.

**Overall Assessment**: Test coverage is good for the core features introduced in this branch. The layered planning state machine has extensive lifecycle and edge case tests. Controller tests cover happy paths, error handling, and template rendering. Database backends (BoltDB and Redis) have parallel test suites ensuring feature parity. However, there are notable coverage gaps in several production files that lack any corresponding tests, some test isolation concerns with time-dependent assertions, and a few areas where assertion quality could be strengthened.

---

## Findings

### [HIGH] Output Persister Tests Do Not Verify Field Values of Saved ProjectOutput

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/output_persister_test.go:19-188`
- **Description**: Four of the six `OutputPersister` tests (`PersistPlanSuccess`, `PersistFailure`, `PersistApplySuccess`, `PersistPolicyCheck`) use `AnyProjectOutput()` as a pegomock matcher and only verify that `SaveProjectOutput` was called once. They never inspect the `models.ProjectOutput` struct that was passed. This means the test would pass even if the persister set wrong values for `Status`, `Output`, `ResourceStats`, `Error`, `PolicyPassed`, or `CommandName`.
- **Impact**: Regressions in field mapping logic (e.g., setting `Status` to `SuccessOutputStatus` when there's an error) would not be caught. Only the last two tests (`DatabaseError` and `PersistsPullURLAndTitle`) actually capture and verify the saved output fields.
- **Recommendation**: Use `When(...).Then(...)` with captured arguments (as done in `TestOutputPersister_PersistsPullURLAndTitle`) in all persister tests. Assert on critical fields: `Status`, `CommandName`, `Output`, `Error`, `PolicyPassed`, and `ResourceStats` values.

### [HIGH] No Tests for `server/controllers/render.go` - Template Error Handling

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/controllers/render.go`
- **Description**: The `renderTemplate` function is a shared utility used by every controller for rendering HTML responses. It buffers template output before writing to the response (preventing partial HTML on errors) and sets `Content-Type` headers. There is no unit test for this function.
- **Impact**: If `renderTemplate` were changed to skip buffering, write wrong headers, or swallow errors, no test would catch it. All controllers depend on this function behaving correctly.
- **Recommendation**: Add tests for: (1) successful rendering sets `Content-Type` header, (2) template execution error returns 500 with "Internal server error", (3) nil logger does not panic, (4) buffer is not written to the response on error.

### [MEDIUM] Time-Dependent Tests Use `time.Sleep` for Synchronization

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/jobs/project_command_output_handler_test.go:108,202,236,315,409,493-500`
- **Description**: Multiple tests rely on `time.Sleep(10ms)` or `time.Sleep(50ms)` to wait for async processing. Examples: `TestProjectCommandOutputHandler/copies_buffer_to_new_channels` (line 108), `TestJobStartTimePreserved` (lines 493-500, 50ms sleep with a 50ms threshold assertion), and `TestRegisterLargeBufferNoDeadlock` (line 409). The `TestJobStartTimePreserved` test is particularly fragile: it asserts `duration >= 50*time.Millisecond` after sleeping exactly 50ms, which can fail on slow CI machines.
- **Impact**: These tests may be flaky under load or on slow CI runners. The 50ms boundary assertion in `TestJobStartTimePreserved` is especially susceptible to false failures.
- **Recommendation**: Where possible, use channel synchronization or polling with timeout instead of fixed sleeps. For `TestJobStartTimePreserved`, increase the sleep margin (e.g., sleep 100ms, assert >= 50ms) or use `synctest` for fake time as done in `server_internal_test.go`.

### [MEDIUM] No Tests for `server/events/instrumented_project_command_runner.go`

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/instrumented_project_command_runner.go`
- **Description**: The `InstrumentedProjectCommandRunner` wraps all project commands with metrics emission (counters for success/error/failure, execution timer). The file was modified to change return types from `ProjectResult` to `ProjectCommandOutput`, and the `RunAndEmitStats` function has branching logic for error vs failure vs success metrics. There is no test file for this code.
- **Impact**: Metrics emission regressions (e.g., not incrementing the error counter on errors, or not stopping the timer) would not be detected. The type change from `ProjectResult` to `ProjectCommandOutput` could mask a subtle field-access bug.
- **Recommendation**: Add tests verifying: (1) success increments `ExecutionSuccessMetric`, (2) error result increments `ExecutionErrorMetric`, (3) failure result increments `ExecutionFailureMetric`, (4) timer is started and stopped. Use a `tally/v4` test scope.

### [MEDIUM] No Tests for `server/core/db/db.go` Database Interface Changes

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/db/db.go`
- **Description**: The `Database` interface was extended with new methods (`SaveProjectOutput`, `GetProjectOutputByJobID`, `GetProjectOutputHistory`, `GetProjectOutputRun`, `GetProjectOutputsByPull`, `DeleteProjectOutputsByPull`, `GetActivePullRequests`, `MarkInterruptedOutputs`, `UpdateLayerState`, `Close`). While the implementations (BoltDB, Redis) are tested, the interface file itself defines the contract and has no corresponding test that verifies all implementations satisfy the full interface at compile time.
- **Impact**: The implementations are tested individually, so the practical risk is low. However, adding a new method to the interface without updating all implementations would only be caught at compile time, not at test time. A compile-time interface satisfaction check in a test file would catch this earlier in CI output.
- **Recommendation**: Add an `_ db.Database = (*boltdb.BoltDB)(nil)` and `_ db.Database = (*redis.RedisDB)(nil)` compile-time check in a test file to ensure all implementations satisfy the interface.

### [MEDIUM] WebSocket Mux Test File Does Not Exist

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/controllers/websocket/mux_test.go`
- **Description**: The file path is listed in the git diff as modified, but the actual `websocket/writer.go` file does not exist on disk (returns "File does not exist" error). The websocket directory may have been renamed or removed. This suggests either dead test code or a file path mismatch.
- **Impact**: If the websocket package was removed but tests still reference it, the tests may be stale. If the package exists under a different path, the test coverage may be miscounted.
- **Recommendation**: Verify whether the websocket package is still used. If it was replaced by SSE streaming, remove or update stale test files.

### [MEDIUM] `OutputPersistingProjectCommandRunner` Tests Do Not Verify Stub Persistence

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/output_persisting_project_command_runner_test.go`
- **Description**: The production code calls `p.persistStub(ctx, command.Plan)` before running the command, which writes a "Running" status record. None of the tests verify that `SaveProjectOutput` is called with `RunningOutputStatus` for the stub. The helper `lastSavedOutput` retrieves the last saved output (the final result), so the stub call is only verified implicitly through `AtLeast(1)`.
- **Impact**: If the stub persistence were removed or broken (e.g., no longer writing `RunningOutputStatus`), no test would fail. The web UI relies on stub records to show "running" state during command execution.
- **Recommendation**: Add assertions that `SaveProjectOutput` is called at least twice (once for stub, once for result), and verify the first call has `Status: RunningOutputStatus`.

### [LOW] BoltDB and Redis Tests Are Not Verified for Feature Parity

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/boltdb/boltdb_test.go` and `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis_test.go`
- **Description**: Both BoltDB and Redis have parallel test suites for the new project output methods. However, the test names and exact coverage differ slightly. For example, BoltDB has `TestBoltDB_GetActivePullRequests_PullIndexPreservesMetadata`, `TestBoltDB_GetActivePullRequests_PullIndexUpdatedOnSave`, and `TestBoltDB_GetActivePullRequests_PullIndexCleanedOnDelete`, while Redis has `TestRedisDB_SaveProjectOutput_Atomic` and `TestRedis_GetProjectOutputByJobID` tests that BoltDB lacks. Neither test suite has a `MarkInterruptedOutputs` test.
- **Impact**: Feature parity drift between storage backends could go undetected. A bug in one backend's implementation of a method might not be caught if only the other backend has a test for that scenario.
- **Recommendation**: Create a shared test suite (table-driven tests with a factory function) that runs the same scenarios against both BoltDB and Redis backends. Add tests for `MarkInterruptedOutputs` in both backends. Add `GetProjectOutputByJobID` tests for BoltDB.

### [LOW] Race Condition Tests Use `wg.Go()` Which May Not Be Available in All Go Versions

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/jobs/project_command_output_handler_test.go:290,298,319`
- **Description**: The `TestRaceConditionPrevention` and `TestHighConcurrencyStress` tests use `wg.Go()`, which was introduced in Go 1.24. If the project needs to maintain compatibility with earlier Go versions, these tests will fail to compile.
- **Impact**: Low if the project targets Go 1.24+. Could cause CI failures if the project is built with an older toolchain.
- **Recommendation**: Verify the project's minimum Go version requirement. If Go < 1.24 compatibility is needed, replace `wg.Go(func() { ... })` with the traditional `wg.Add(1); go func() { defer wg.Done(); ... }()` pattern.

### [LOW] No Tests for `server/events/models/models.go` Changes

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/models/models.go`
- **Description**: The models file was modified (likely to add `LayerState`, `ProjectOutput`, `ResourceStats` types and new `ProjectPlanStatus` constants like `SkippedPlanStatus`, `ApplyingStatus`). While these types are exercised through integration-style tests in other packages, there are no direct unit tests for methods or validation on the model types themselves.
- **Impact**: If the models file contains methods with logic (e.g., `PullInfo()`, `Stats()` on `PlanSuccess`), those are only tested indirectly.
- **Recommendation**: The `project_output_test.go` file covers `ProjectOutput` methods. Review whether `models.go` additions include any methods that should have direct unit tests.

### [LOW] Settings Controller Test File Listed but May Be Empty/Minimal

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/controllers/settings_controller_test.go`
- **Description**: The settings controller test file exists in the diff but could not be read (persisted output). The settings controller manages apply lock/unlock state. If the test is minimal or only covers happy paths, error handling for concurrent lock operations may be untested.
- **Impact**: Without verifying the test contents, this is speculative. The settings controller is a relatively simple CRUD controller.
- **Recommendation**: Verify the test covers: (1) lock acquisition, (2) lock release, (3) already-locked state, (4) database errors during lock operations.

### [LOW] `PullClosedExecutor` Test Uses `os.CreateTemp` Instead of `t.TempDir()`

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/pull_closed_executor_test.go:242-247`
- **Description**: The `TestCleanUpLogStreaming` test creates a temporary file with `os.CreateTemp` and manually opens BoltDB on it. Other tests in the same file use `t.TempDir()` with the `boltdb.New` helper. The `os.CreateTemp` approach does not get automatic cleanup if the test panics before the deferred close.
- **Impact**: Stale temp files could accumulate on CI runners if tests panic. Not a correctness issue.
- **Recommendation**: Use `t.TempDir()` with `boltdb.New()` for consistency with other tests in the file.

### [INFO] Comprehensive Layered Planning State Machine Tests

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layer_state_manager_test.go`
- **Description**: The `LayerStateManager` has 38 test functions covering initialization, layer progression, cascade evaluation, skip functionality, new commit handling, and full lifecycle scenarios. Tests cover: simple chains, diamond dependencies, parallel chains, circular dependency detection, mixed changed/pending projects, transitive skip cascading, and edge cases (nil state, project not found, skip disabled).
- **Impact**: This is the most critical piece of new business logic, and the test coverage is thorough.

### [INFO] Dashboard Renderer Tests Include Split/Truncation Edge Cases

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layers/dashboard_renderer_test.go`
- **Description**: Tests cover: single/multiple layers, all status icons, pending count, completed layer collapsing, comment splitting for large dashboards, truncation fallback, and unsplittable content. The `splitAtLayerBoundaries` and `splitIntoLayerSections` internal functions are tested directly.

### [INFO] Dashboard Updater Tests Use Faithful Fake VCS Client

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/layers/dashboard_updater_test.go`
- **Description**: The `fakeVCSClient` test double implements only the methods needed (`ListComments`, `EditComment`, `CreateComment`, `MaxCommentLength`) and records calls for verification. Tests cover: create new, update existing, shrink dashboard (clearing stale continued comments), edit failure fallback to create, and list comments failure handling.

### [INFO] Template Tests Verify ANSI Round-Trip and HTMX Compatibility

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/controllers/web_templates/web_templates_test.go`
- **Description**: Tests verify that ANSI escape codes survive the full rendering pipeline (Go string to JSON to template to rendered HTML) without double-escaping. A dedicated test verifies the data carrier uses `<div hidden>` instead of `<script>` because HTMX strips script tags on swap. OOB swap targets are verified for policy output, error output, and stats sections.

### [INFO] Concurrent Access Tests Use Race Detector

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/jobs/project_command_output_handler_test.go:252-344`
- **Description**: `TestRaceConditionPrevention` specifically targets the race conditions that were fixed by using `sync.Map` for `pullToJobMapping`. It exercises concurrent writers (Send) and readers (GetPullToJobMapping, GetJobIDMapForPull, GetProjectOutputBuffer) simultaneously with 50 goroutines each. When run with `-race`, this provides confidence that the synchronization is correct.

### [INFO] Server Internal Tests Use `synctest.Test` for Deterministic Timing

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/server_internal_test.go:69`
- **Description**: The `TestServer_CloseDatabase` test uses Go 1.24's `synctest.Test` to control fake time, making the timeout test deterministic. This is a strong pattern that avoids the flakiness issues seen in the output handler tests.

---

## Coverage Gap Summary

The following production files changed on this branch have **no corresponding test file**:

| File | Risk | Rationale |
|------|------|-----------|
| `server/controllers/render.go` | High | Shared utility used by all controllers |
| `server/events/instrumented_project_command_runner.go` | Medium | Metrics emission logic with branching |
| `server/core/db/db.go` | Low | Interface definition, tested via implementations |
| `server/events/models/models.go` | Low | Types tested indirectly through other packages |
| `server/events/event_parser.go` | Low | Changes likely minimal (dependency additions) |
| `server/controllers/jobs_controller_dev.go` | Low | Dev-only code behind build tag |
| `server/controllers/jobs_test_patterns.go` | Low | Test data generator, not production logic |
| `server/jobs/test_output_handler.go` | Low | Test helper, not production logic |
| `server/server_dev.go` | Low | Dev-only code behind build tag |
| `server/user_config.go` | Low | Configuration struct changes |
| `scripts/seed-db/main.go` | Low | Developer tooling, not production |

---

## Strengths

1. **Layered planning has near-complete coverage**: The `LayerStateManager` tests cover initialization, progression, cascade evaluation, skip handling, new commit impact, and full lifecycle flows. Edge cases like circular dependencies, nil state, and transitive skip propagation are all tested.

2. **Database tests ensure feature parity**: Both BoltDB and Redis have parallel test suites for the new project output persistence methods, including the critical `SkipsPolicyCheck` merge behavior.

3. **Template tests prevent ANSI rendering regressions**: The ANSI round-trip tests catch the double-escaping bug that would break terminal output rendering in the browser. The HTMX compatibility test ensures data carriers work with swapped content.

4. **Race condition tests are targeted and specific**: Rather than generic concurrency tests, `TestRaceConditionPrevention` specifically exercises the exact patterns that caused the original race conditions, providing high-value regression coverage.

5. **Controller tests capture and verify template data**: Several controller tests (e.g., `TestPRController_PRList_AggregatesResourceStats`, `TestPRController_PRList_SortsByLastActivity`) use `Then()` callbacks to capture the actual data passed to templates and verify field values, rather than just checking status codes.

6. **Fakes over mocks where appropriate**: The dashboard updater tests use a hand-written `fakeVCSClient` rather than a generated mock, resulting in tests that are easier to read and maintain.

7. **Good error path coverage**: Most controller tests include database error, template error, and invalid input scenarios alongside happy paths.

8. **Use of `synctest` for time-sensitive tests**: The `server_internal_test.go` demonstrates the correct approach for testing timeout behavior without flaky time-dependent assertions.
