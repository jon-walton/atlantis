# Database Persistence and Output Handling Review - feature/layered-planning

## Summary

This review covers all database persistence, output handling, and SSE streaming code changes on the `feature/layered-planning` branch compared to `main`. The changes introduce a new project output persistence layer: a `ProjectOutput` model with JSON serialization, BoltDB and Redis implementations with secondary indexes (job-id-index, pull-index), an `OutputPersister` that captures command results, an `OutputPersistingProjectCommandRunner` decorator, revised SSE streaming with lock-ordering fixes, and cleanup on PR close.

**Scope**: 8 files reviewed, ~1,100 lines added, ~60 removed.

- `server/core/boltdb/boltdb.go` -- New methods for project output CRUD with index management
- `server/core/redis/redis.go` -- New methods for project output CRUD with Redis sets and pipeline transactions
- `server/core/db/db.go` -- Interface additions for project output operations
- `server/events/models/project_output.go` -- New `ProjectOutput` model and status enum
- `server/events/output_persister.go` -- Logic to convert command results into persisted records
- `server/events/output_persisting_project_command_runner.go` -- Decorator pattern for persistence
- `server/jobs/project_command_output_handler.go` -- SSE streaming, buffer management, lock ordering
- `server/events/pull_closed_executor.go` -- Cleanup on PR close

**Overall Assessment**: The persistence layer is well-structured with good patterns: transactional writes in BoltDB, pipeline transactions in Redis, proper index management, backwards-compatible fallback scans, and consistent lock ordering in the SSE handler. Several findings warrant attention, primarily around data consistency during upsert operations, a potential nil dereference, and the possibility of orphaned data under concurrent operations.

---

## Findings

### [MEDIUM] Stub and Result Use Different Keys, Causing Orphaned Running Records

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/output_persister.go:28-45` and `:48-133`
- **Description**: `PersistStub` and `PersistResult` both call `time.Now().UTC().UnixMilli()` independently to generate `RunTimestamp`, which is part of the composite key (via `output.Key()`). Since these are called at different times (before and after command execution), they produce different timestamps and therefore different keys. The upsert logic in `SaveProjectOutput` resolves this correctly when a `JobID` is present (it looks up the existing key via the job-id-index), but if `JobID` is ever empty, the stub record will be orphaned -- it will remain in the database forever with `RunningOutputStatus` and never be updated or cleaned up.
- **Impact**: Orphaned "running" records could accumulate in the database over time if any code path produces a `ProjectOutput` without a `JobID`. The `MarkInterruptedOutputs` method would transition these to "interrupted" on every restart, but they would never be deleted except when the entire pull is cleaned up.
- **Recommendation**: Either (1) generate the `RunTimestamp` once in `OutputPersistingProjectCommandRunner` and pass it to both `PersistStub` and `PersistResult`, or (2) add a defensive check that `JobID` is non-empty before persisting the stub. The current code likely always has a `JobID` set in `ProjectContext`, but the contract is not enforced.

### [MEDIUM] Nil Dereference in PullClosedExecutor When LogStreamResourceCleaner Is Nil

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/pull_closed_executor.go:89-101`
- **Description**: The existing code at line 99 calls `p.LogStreamResourceCleaner.CleanUp(jobContext)` unconditionally inside the `pullStatus != nil` block without checking if `LogStreamResourceCleaner` is nil. The newly added code at line 104 correctly checks `if p.LogStreamResourceCleaner != nil` before calling the hook cleanup variant. However, the pre-existing call at line 99 does not have this nil guard. If `pullStatus` is non-nil but `LogStreamResourceCleaner` is nil, this will panic.
- **Impact**: A nil pointer dereference would crash the pull cleanup goroutine, preventing lock cleanup, workspace deletion, and the new project output deletion from executing. This would leave stale locks and data behind.
- **Recommendation**: Add a nil check before the existing call at line 99: `if p.LogStreamResourceCleaner != nil { p.LogStreamResourceCleaner.CleanUp(jobContext) }`. Note: this is a pre-existing bug, but the new code path around it makes it more visible and should be fixed alongside these changes.

### [MEDIUM] Redis MarkInterruptedOutputs Is Not Atomic

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:522-547`
- **Description**: `MarkInterruptedOutputs` iterates over all pull index keys, reads each output individually, checks status, and writes back -- all as separate Redis operations with no transaction or pipeline. If the server crashes during this operation, some outputs may be marked as interrupted while others remain in the running state. Additionally, errors from `SMembers` and `Set` are silently swallowed (only `continue` on error).
- **Impact**: After a crash during startup, some jobs may still show as "running" when they should show as "interrupted". This is a startup-only operation and the inconsistency is cosmetic (the jobs are dead either way), but the silent error swallowing means failures would be invisible.
- **Recommendation**: (1) Use a Redis pipeline to batch the status transitions within each pull's outputs. (2) Log errors from `SMembers` and `Set` rather than silently continuing, so operators can diagnose startup issues. The BoltDB implementation handles this correctly by using a single `Update` transaction.

### [MEDIUM] Redis DeleteProjectOutputsByPull Uses Non-Transactional Pipeline

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:698-727`
- **Description**: `DeleteProjectOutputsByPull` uses `r.client.Pipeline()` (non-transactional) rather than `r.client.TxPipeline()` (transactional) for the delete operations. In contrast, `SaveProjectOutput` correctly uses `TxPipeline`. A non-transactional pipeline sends commands in a batch but does not wrap them in `MULTI/EXEC`, so if the Redis connection drops mid-execution, some keys may be deleted while others remain, leaving orphaned index entries or output keys.
- **Impact**: Partial deletion could leave the pull-index set referencing keys that no longer exist, or leave output keys without their corresponding job-id-index entries. The code handles stale index references gracefully in read paths, so the practical impact is limited, but using `TxPipeline` would be more correct.
- **Recommendation**: Change `r.client.Pipeline()` to `r.client.TxPipeline()` on line 717 for consistency with `SaveProjectOutput` and to guarantee atomic cleanup.

### [LOW] BoltDB SaveProjectOutput Silently Ignores Pull Index Write Failures

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/boltdb/boltdb.go:710-724`
- **Description**: In `SaveProjectOutput`, the pull index update uses `_ = pullIndexBucket.Put(...)` to discard the error. If the pull index write fails but the main output write succeeds, `GetActivePullRequests` will not find this pull until the next successful output save for the same pull. The `json.Marshal` error for `pullData` is also silently swallowed with `if err == nil`.
- **Impact**: In practice, BoltDB `Put` within a writable transaction is very unlikely to fail unless the disk is full. However, discarding errors makes debugging harder if it does happen.
- **Recommendation**: Propagate the error from `pullIndexBucket.Put()` via the transaction return value, or at minimum log a warning.

### [LOW] writeLogLine and completeJob Have Inverted Lock Ordering

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/jobs/project_command_output_handler.go:226-249` and `:286-309`
- **Description**: The code comments correctly document the intended lock ordering as `projectOutputBuffersLock -> receiverBuffersLock`. The `completeJob` method follows this ordering. However, `writeLogLine` acquires `projectOutputBuffersLock` first (line 287), releases it (line 296), then acquires `receiverBuffersLock` (line 298). This is technically not holding both locks simultaneously (it releases the first before acquiring the second), so it does not deadlock, but it creates a window where `completeJob` could execute between the two critical sections. This means a line could be written to the buffer but the channel could already be closed before the line is forwarded.
- **Impact**: A race between `writeLogLine` and `completeJob` could cause a line to be buffered but never forwarded to active SSE clients. The line would still be persisted to the database (since it is in the buffer), so it is only a live-streaming concern, not a data loss concern. The SSE client would see the line on reconnect or page refresh.
- **Recommendation**: This is an acceptable tradeoff for avoiding deadlocks. The current design prioritizes lock-free forwarding over strict ordering. If strict delivery is needed, both operations in `writeLogLine` should hold both locks simultaneously (matching `addChan`'s pattern).

### [LOW] GetProjectOutputBuffer Returns a Shallow Copy of Buffer Slice

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/jobs/project_command_output_handler.go:324-328`
- **Description**: `GetProjectOutputBuffer` returns the `OutputBuffer` struct by value, which copies the slice header but not the underlying array. The caller (`OutputPersister.PersistResult`) then reads from this slice via `strings.Join(buffer.Buffer, "\n")`. If `writeLogLine` appends to the buffer concurrently (which extends the underlying array), Go's append semantics generally allocate a new backing array, so the caller's slice is safe. However, if the buffer has excess capacity and append does not reallocate, the caller could observe partially-written data.
- **Impact**: Very low probability of data corruption in the persisted output. In practice, Go's append almost always reallocates for growing slices, and the timing window is extremely narrow.
- **Recommendation**: Either copy the slice in `GetProjectOutputBuffer` (like `addChan` already does) or document that the returned buffer is only safe to read after the job is complete. Since `PersistResult` is called after the command finishes but potentially before `completeJob` runs, a defensive copy would be safer.

### [LOW] Redis GetActivePullRequests Performs N+1 Queries

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:768-835`
- **Description**: `GetActivePullRequests` first scans for all pull index keys, then for each key calls `SMembers` to get output keys, then for each output key calls `GET` to find URL/Title metadata. For a deployment with many active PRs and outputs, this results in `O(P * K)` Redis round trips where P is pull count and K is outputs per pull.
- **Impact**: In small to medium deployments this is fine. For large deployments with hundreds of active PRs, this could cause noticeable latency on the PR list page and increased Redis load.
- **Recommendation**: Consider storing the pull metadata (URL, Title) in the pull index set value itself (similar to BoltDB's pull index which stores a serialized `PullRequest`), eliminating the need to query individual outputs for metadata. Alternatively, use `MGET` to batch the output key lookups.

### [LOW] Policy-Only Projects Are Silently Dropped in GetProjectOutputsByPull

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/boltdb/boltdb.go:829-835` and `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:680-685`
- **Description**: In `GetProjectOutputsByPull`, `policy_check` records are merged into the latest non-policy record for each project. However, if a project has only `policy_check` records and no `plan` or `apply` records, the `policyByProject` entry has no corresponding `latestByProject` entry to merge into, so the policy data is silently dropped.
- **Impact**: A project that has only had `policy_check` runs (and never a `plan` or `apply`) would not appear in the pull's project list. In practice this is unlikely since `policy_check` typically follows a `plan`, but it is a potential data loss edge case.
- **Recommendation**: Add a fallback: if a `policyByProject` entry has no matching `latestByProject` entry, include the policy record itself in the output list.

### [INFO] Consistent Lock Ordering Documented and Enforced

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/jobs/project_command_output_handler.go:253-255`
- **Description**: The refactored `addChan` method acquires both locks in the documented order (`projectOutputBuffersLock -> receiverBuffersLock`), takes a snapshot of the buffer, and returns it to the caller for backfill outside the lock scope. This eliminates the previous race condition where backfill lines could interleave with new lines from `writeLogLine`.
- **Impact**: Positive finding -- the previous implementation had a race between backfill and new messages that could cause duplicate or out-of-order lines. The new implementation correctly prevents this.

### [INFO] BoltDB Transactions Provide Strong Consistency

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/boltdb/boltdb.go:677-728`
- **Description**: All BoltDB write operations (`SaveProjectOutput`, `DeleteProjectOutputsByPull`, `MarkInterruptedOutputs`) use `db.Update()` transactions, ensuring atomicity of main record writes and index updates. If any write within the transaction fails, the entire transaction is rolled back.
- **Impact**: Positive finding -- data consistency is guaranteed for the BoltDB backend. The main record, job-id index, and pull index are always consistent.

### [INFO] Upsert-by-JobID Pattern Correctly Handles Re-runs

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/boltdb/boltdb.go:682-689` and `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:553-559`
- **Description**: Both `SaveProjectOutput` implementations check the job-id-index for an existing key before writing. When a stub is saved with a `JobID`, the subsequent result save with the same `JobID` finds and overwrites the stub rather than creating a duplicate record. This correctly handles the stub-then-result persistence flow.
- **Impact**: Positive finding -- ensures a single record per job execution, even though the stub and result have different timestamps.

### [INFO] Backwards-Compatible Fallback Scans

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/boltdb/boltdb.go:890-933` and `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/core/redis/redis.go:731-764`
- **Description**: Both `GetProjectOutputByJobID` and `GetActivePullRequests` attempt an indexed lookup first and fall back to a full scan if the index is empty or the entry is not found. This ensures data written before the indexes were introduced remains accessible.
- **Impact**: Positive finding -- smooth migration path for existing deployments upgrading to this version.

### [INFO] Decorator Pattern Follows Established Codebase Conventions

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/output_persisting_project_command_runner.go`
- **Description**: The `OutputPersistingProjectCommandRunner` follows the same decorator/wrapper pattern used elsewhere in the Atlantis codebase (e.g., instrumented command runners). It wraps `ProjectCommandRunner` and adds persistence behavior without modifying the inner runner's logic. Persistence failures are logged as warnings rather than failing the command.
- **Impact**: Positive finding -- persistence failures do not affect command execution. Commands complete successfully regardless of database issues.

### [INFO] Proper Cleanup on Pull Close

- **File**: `/Users/jon.walton@cloverhealth.com/source/counterparthealth/sre/atlantis/server/events/pull_closed_executor.go:130-133`
- **Description**: `DeleteProjectOutputsByPull` is called during pull cleanup, ensuring that project outputs are removed when a PR is closed. The operation deletes all outputs, their job-id-index entries, and the pull-index entry atomically (in BoltDB) or via pipeline (in Redis). Errors are logged as warnings rather than failing the entire cleanup.
- **Impact**: Positive finding -- prevents unbounded database growth from accumulated project outputs.

---

## Overall Assessment

The persistence layer on the `feature/layered-planning` branch is well-designed and follows good practices:

**Strengths:**
- Transactional writes in BoltDB ensure atomicity of multi-key operations
- Redis `TxPipeline` used for `SaveProjectOutput` to atomically update record + indexes
- Secondary indexes (job-id-index, pull-index) provide O(1) lookups with backwards-compatible full-scan fallback
- Decorator pattern keeps persistence concerns separate from command execution
- Persistence failures are non-fatal (logged as warnings, do not block commands)
- Consistent lock ordering documented and enforced in SSE handler refactoring
- `addChan` refactoring eliminates the previous race condition in SSE backfill
- `MarkInterruptedOutputs` correctly handles server crash recovery
- Proper cleanup on PR close prevents unbounded data growth
- `ProjectOutputStatus` uses string-based JSON serialization for readability and forward compatibility

**Areas for Improvement:**
- Stub and result records use independently generated timestamps, relying entirely on JobID index for upsert correctness
- Redis `MarkInterruptedOutputs` and `DeleteProjectOutputsByPull` lack transactional guarantees
- Pre-existing nil dereference risk in `PullClosedExecutor` for `LogStreamResourceCleaner`
- `GetProjectOutputBuffer` returns a shallow copy that could theoretically race with concurrent writes
- Policy-only projects are silently dropped from `GetProjectOutputsByPull` results

**Risk Rating**: Low-to-Medium. The most significant findings relate to data consistency under edge cases (orphaned stubs, non-atomic Redis operations) rather than correctness failures in the normal path. The core read/write/delete flow is correct and well-tested by the complementary index design. No critical or high-severity issues were identified.
