# WS3: Dashboard Renderer -- Plan vs Implementation Audit

## Summary

The WS3 Dashboard Renderer implementation is **substantially complete** and faithful to the plan. All core components exist: VCS interface extensions, dashboard types, renderer with splitting, updater with sticky comment lifecycle, the template, and comprehensive test suites. The main divergences are intentional design improvements (reusing `models.ProjectPlanStatus` instead of a standalone enum, template formatting differences, `normalizeMarkdown` helper). There are two notable gaps: missing VCS provider-level tests for `ListComments`/`EditComment`, and Azure DevOps `ListComments`/`EditComment` returning stub errors instead of real implementations.

---

## Audit Results

### 2. VCS Client Interface Changes

#### 2.1 New Methods on `vcs.Client`

| Plan Item | Status | Notes |
|---|---|---|
| `ListComments` method on `Client` interface | IMPLEMENTED | `server/events/vcs/client.go:38` -- signature matches plan exactly |
| `EditComment` method on `Client` interface | IMPLEMENTED | `server/events/vcs/client.go:41` -- signature matches plan exactly |
| `PullComment` type with ID/Body/Author | IMPLEMENTED | `server/events/vcs/client.go:24-28` -- matches plan |

#### 2.2 Implementation per VCS Provider

| Plan Item | Status | Notes |
|---|---|---|
| GitHub `ListComments` | IMPLEMENTED | `server/events/vcs/github/client.go:1200-1233` -- matches plan spec exactly (pagination, author handling) |
| GitHub `EditComment` | IMPLEMENTED | `server/events/vcs/github/client.go:1236-1245` -- matches plan |
| GitLab `ListComments` | IMPLEMENTED | `server/events/vcs/gitlab/client.go:791-824` -- matches plan (skips system notes, pagination) |
| GitLab `EditComment` | IMPLEMENTED | `server/events/vcs/gitlab/client.go:827-835` -- matches plan |
| Bitbucket Cloud `ListComments` | IMPLEMENTED | `server/events/vcs/bitbucketcloud/client.go:399-425` -- uses `GetPullRequestComments` internally |
| Bitbucket Cloud `EditComment` | IMPLEMENTED | `server/events/vcs/bitbucketcloud/client.go:428-438` -- PUT with JSON body |
| Bitbucket Server `ListComments` | IMPLEMENTED | `server/events/vcs/bitbucketserver/client.go:383-432` -- uses activities API, paginated |
| Bitbucket Server `EditComment` | IMPLEMENTED | `server/events/vcs/bitbucketserver/client.go:435-462` -- fetches version first (required by BBS), then PUTs |
| Azure DevOps `ListComments` | PARTIALLY IMPLEMENTED | `server/events/vcs/azuredevops/client.go:465-467` -- returns stub error "not yet implemented" |
| Azure DevOps `EditComment` | PARTIALLY IMPLEMENTED | `server/events/vcs/azuredevops/client.go:470-472` -- returns stub error "not yet implemented" |
| Gitea `ListComments` | IMPLEMENTED | `server/events/vcs/gitea/client.go:510-542` -- uses `ListIssueComments` with pagination |
| Gitea `EditComment` | IMPLEMENTED | `server/events/vcs/gitea/client.go:545-551` -- uses `EditIssueComment` |
| `NotConfiguredVCSClient` stubs | IMPLEMENTED | `server/events/vcs/not_configured_vcs_client.go:82-92` -- returns `a.err()` for ListComments/EditComment, 0 for MaxCommentLength |
| `ClientProxy` pass-through | IMPLEMENTED | `server/events/vcs/proxy.go:120-132` |
| `InstrumentedClient` wrappers | IMPLEMENTED | `server/events/vcs/common/instrumented_client.go:194-234` -- ListComments and EditComment instrumented with metrics; MaxCommentLength delegates via embedded interface |
| Mock client regenerated | IMPLEMENTED | `server/events/vcs/mocks/mock_client.go` includes ListComments, EditComment, MaxCommentLength |

#### 2.3 Exposing `MaxCommentLength`

| Plan Item | Status | Notes |
|---|---|---|
| `MaxCommentLength()` on `Client` interface | IMPLEMENTED | `server/events/vcs/client.go:45` |
| GitHub returns 65536 | IMPLEMENTED | `server/events/vcs/github/client.go:1248-1250` -- returns `maxCommentLength` constant |
| GitLab returns constant | IMPLEMENTED | `server/events/vcs/gitlab/client.go:838-840` |
| Bitbucket Cloud returns 0 (unlimited) | IMPLEMENTED | `server/events/vcs/bitbucketcloud/client.go:441-444` |
| Bitbucket Server returns constant | IMPLEMENTED | `server/events/vcs/bitbucketserver/client.go:465-467` |
| Azure DevOps returns 150000 | IMPLEMENTED | `server/events/vcs/azuredevops/client.go:475-477` |
| Gitea returns 65536 | IMPLEMENTED | `server/events/vcs/gitea/client.go:554-556` |

---

### 3. Dashboard Data Model

#### 3.1 Types (`server/events/layers/dashboard_types.go`)

| Plan Item | Status | Notes |
|---|---|---|
| `ProjectStatus` enum (iota) | DIVERGED | Reuses `models.ProjectPlanStatus` via `DashboardStatus` wrapper instead of a standalone enum. This is an improvement -- avoids maintaining duplicate status values. |
| `StatusPlanning` ("Planning...") | DIVERGED | Mapped to `PendingPlanStatus` -> "Pending" label. Plan had "Planning..." but implementation shows "Pending" which is more accurate for a pre-plan state. |
| `StatusPlanned` ("Planned (changes)") | IMPLEMENTED | `PlannedPlanStatus` -> "Planned (changes)" via `statusDisplay` map |
| `StatusNoChanges` ("Planned (no changes)") | IMPLEMENTED | `PlannedNoChangesPlanStatus` -> "Planned (no changes)" |
| `StatusApplied` ("Applied") | IMPLEMENTED | `AppliedStatus` -> "Applied" |
| `StatusApplyFailed` ("Apply failed") | IMPLEMENTED | `ErroredApplyStatus` -> "Apply failed" |
| `StatusSkipped` ("Skipped") | IMPLEMENTED | `SkippedPlanStatus` -> "Skipped" |
| Additional statuses not in plan | IMPLEMENTED | `ErroredPlanStatus` ("Plan failed"), `ApplyingStatus` ("Applying..."), `PassedPolicyCheckStatus` ("Planned (changes)") -- extra coverage for more model states |
| `Icon()` method with expected Unicode | IMPLEMENTED | All icons match plan: hourglass, orange diamond, white circle, check mark, cross mark, next track |
| `DashboardProject{Name, Status}` | IMPLEMENTED | `dashboard_types.go:63-66` -- uses `DashboardStatus` instead of `ProjectStatus` |
| `DashboardLayer{Number, Projects, IsCurrent}` | IMPLEMENTED | `dashboard_types.go:69-73` |
| `IsCompleted()` method | IMPLEMENTED | `dashboard_types.go:77-87` -- also handles empty slice (returns false), which the plan did not consider |
| `CompletedCount()` method | IMPLEMENTED | `dashboard_types.go:90-98` |
| `Summary()` method | IMPLEMENTED | `dashboard_types.go:101-126` -- uses `ProjectPlanStatus` switch instead of the plan's separate enum |
| `DashboardData{Layers, PendingCount, TotalCount, RepoName, PullNum}` | IMPLEMENTED | `dashboard_types.go:129-135` |

---

### 4. Dashboard Template

#### 4.1 Template File

| Plan Item | Status | Notes |
|---|---|---|
| Template file at `server/events/layers/templates/layered_dashboard.tmpl` | IMPLEMENTED | File exists |
| Sentinel `<!-- Atlantis Layered Planning Dashboard -->` on line 2 | IMPLEMENTED | Line 2 of template |
| `{{ define "layeredDashboard" }}` | IMPLEMENTED | Line 1 |
| Current layer: `**Layer N** (current)` with expanded projects | DIVERGED | Uses `**Layer N** _(current)_` with italic formatting instead of parentheses. Minor cosmetic difference. |
| Completed layer: collapsed with `<details>/<summary>` | DIVERGED | Uses `<details><summary><b>Layer N</b> -- Summary</summary>` instead of `**Layer N** - Summary` + separate `<details>`. The collapsed layer header is inside the `<summary>` tag rather than outside it. This is actually better for rendering. |
| Project line format: `- Icon Status - Name` | DIVERGED | Uses `- Icon Status -- \`Name\`` with em-dash and backtick-quoted name. Minor cosmetic improvement for readability. |
| Pending count: `*N projects pending*` | DIVERGED | Uses `_N additional projects pending based on upstream results_` with more descriptive text and underscore italic. |
| Continued sentinel format | IMPLEMENTED | Constant defined in renderer: `DashboardContinuedSentinelFmt` matches plan |

---

### 5. DashboardRenderer

#### 5.1 Implementation (`server/events/layers/dashboard_renderer.go`)

| Plan Item | Status | Notes |
|---|---|---|
| `DashboardRenderer` struct | IMPLEMENTED | `dashboard_renderer.go:24-26` |
| `NewDashboardRenderer()` constructor | IMPLEMENTED | `dashboard_renderer.go:31-43` -- accepts `markdownTemplateOverridesDir` parameter as specified in section 7.2 |
| `Render(data DashboardData) string` | IMPLEMENTED | `dashboard_renderer.go:46-56` |
| `RenderSplit(data, maxLen) []string` | IMPLEMENTED | `dashboard_renderer.go:70-78` |
| `DashboardSentinel` constant | IMPLEMENTED | `dashboard_renderer.go:15` |
| `DashboardContinuedSentinelFmt` constant | IMPLEMENTED | `dashboard_renderer.go:17` |
| `embed.FS` for templates | IMPLEMENTED | `dashboard_renderer.go:20-21` |
| Sprig template functions | IMPLEMENTED | Uses `sprig.TxtFuncMap()` at line 32 |
| `normalizeMarkdown()` helper | IMPLEMENTED | `dashboard_renderer.go:60-65` -- not in plan; collapses excess newlines from template output. Good addition. |

#### 5.2 Comment Splitting Algorithm

| Plan Item | Status | Notes |
|---|---|---|
| `splitAtLayerBoundaries()` function | IMPLEMENTED | `dashboard_renderer.go:83-128` |
| Greedy packing of sections into comments | IMPLEMENTED | Same algorithm as plan |
| Continued sentinels on subsequent parts | IMPLEMENTED | Adds `DashboardContinuedSentinelFmt` |
| Single-section truncation fallback | IMPLEMENTED | `dashboard_renderer.go:87-90` and also at line 122 for final part overflow |
| `splitIntoLayerSections()` helper | IMPLEMENTED | `dashboard_renderer.go:132-153` |
| Layer boundary detection: `strings.HasPrefix(line, "**Layer ")` | DIVERGED | Also detects `<details><summary><b>Layer ` (line 139) to handle completed layers using `<details>` tags. This is necessary given the template format divergence. |
| Plan mentions `layerSplitMarker` constant | MISSING | The plan defined `layerSplitMarker = "<!-- layer-boundary -->"` but it is not used in the implementation. The implementation uses line-prefix detection instead, which is simpler and avoids template coupling. This is fine. |

---

### 6. DashboardUpdater (Sticky Comment Manager)

#### 6.1 Implementation (`server/events/layers/dashboard_updater.go`)

| Plan Item | Status | Notes |
|---|---|---|
| `DashboardUpdater` struct with `VCSClient` and `Renderer` | IMPLEMENTED | `dashboard_updater.go:13-16` |
| `NewDashboardUpdater(client)` constructor | DIVERGED | `dashboard_updater.go:19-24` -- accepts `markdownTemplateOverridesDir` as second param (supports section 7.2). Plan showed `NewDashboardUpdater(client vcs.Client)` only. |
| `Update(logger, repo, pullNum, data) error` | IMPLEMENTED | `dashboard_updater.go:30-71` -- full lifecycle: render, split, find, update/create, clear excess |
| `findDashboardComments()` | IMPLEMENTED | `dashboard_updater.go:74-92` |
| `isDashboardComment()` | IMPLEMENTED | `dashboard_updater.go:95-98` -- checks both sentinel and continued sentinel |
| Edit fallback to CreateComment on error | IMPLEMENTED | `dashboard_updater.go:49-52` |
| Clear excess comments with "no longer needed" | IMPLEMENTED | `dashboard_updater.go:62-68` |
| Uses `MaxCommentLength()` for splitting | IMPLEMENTED | `dashboard_updater.go:36` |

#### 6.2 Sticky Comment Flow

| Plan Item | Status | Notes |
|---|---|---|
| 1. Render dashboard | IMPLEMENTED | |
| 2. Split if needed | IMPLEMENTED | |
| 3. List all comments | IMPLEMENTED | |
| 4. Find by sentinel | IMPLEMENTED | |
| 5. Update or create | IMPLEMENTED | |
| 6. Clear excess | IMPLEMENTED | |
| Idempotent behavior | IMPLEMENTED | Same data produces same result |

---

### 7. Integration with Existing MarkdownRenderer

| Plan Item | Status | Notes |
|---|---|---|
| Separate from existing MarkdownRenderer | IMPLEMENTED | Own package, own templates |
| Template override support (`--markdown-template-overrides-dir`) | IMPLEMENTED | `NewDashboardRenderer` accepts overrides dir and calls `ParseGlob` |

---

### 8. Complete File Inventory

#### New Files

| Plan Item | Status | Notes |
|---|---|---|
| `server/events/layers/dashboard_types.go` | IMPLEMENTED | |
| `server/events/layers/dashboard_renderer.go` | IMPLEMENTED | |
| `server/events/layers/dashboard_updater.go` | IMPLEMENTED | |
| `server/events/layers/templates/layered_dashboard.tmpl` | IMPLEMENTED | |
| `server/events/layers/dashboard_renderer_test.go` | IMPLEMENTED | |
| `server/events/layers/dashboard_updater_test.go` | IMPLEMENTED | |

#### Modified Files

| Plan Item | Status | Notes |
|---|---|---|
| `server/events/vcs/client.go` | IMPLEMENTED | |
| `server/events/vcs/github/client.go` | IMPLEMENTED | |
| `server/events/vcs/gitlab/client.go` | IMPLEMENTED | |
| `server/events/vcs/bitbucketcloud/client.go` | IMPLEMENTED | |
| `server/events/vcs/bitbucketserver/client.go` | IMPLEMENTED | |
| `server/events/vcs/azuredevops/client.go` | PARTIALLY IMPLEMENTED | Methods exist but return stub errors |
| `server/events/vcs/gitea/client.go` | IMPLEMENTED | |
| `server/events/vcs/not_configured_vcs_client.go` | IMPLEMENTED | |
| `server/events/vcs/proxy.go` | IMPLEMENTED | |
| `server/events/vcs/common/instrumented_client.go` | IMPLEMENTED | ListComments and EditComment instrumented; MaxCommentLength delegates via embedded Client |
| `server/events/vcs/mocks/mock_client.go` | IMPLEMENTED | Mock regenerated with all three new methods |

---

### 9-10. Testing

#### 10.1 DashboardRenderer Tests

| Plan Item | Status | Notes |
|---|---|---|
| `TestRender_SingleLayerCurrent` | IMPLEMENTED | `dashboard_renderer_test.go:15-48` |
| `TestRender_CompletedLayerCollapsed` | IMPLEMENTED | `dashboard_renderer_test.go:50-73` |
| `TestRender_MultipleLayersWithPending` | IMPLEMENTED | `dashboard_renderer_test.go:75-107` |
| `TestRender_AllStatusIcons` | IMPLEMENTED | `dashboard_renderer_test.go:109-143` |
| `TestRender_PendingCountZero` | IMPLEMENTED | `dashboard_renderer_test.go:145-165` |
| `TestRender_SkippedProjectsInSummary` | IMPLEMENTED | `dashboard_renderer_test.go:167-190` |
| `TestRender_EmptyLayers` | IMPLEMENTED | `dashboard_renderer_test.go:192-206` |
| `TestRenderSplit_UnderLimit` | IMPLEMENTED | `dashboard_renderer_test.go:208-227` |
| `TestRenderSplit_ExceedsLimit` | IMPLEMENTED | `dashboard_renderer_test.go:250-294` |
| `TestRenderSplit_SingleHugeLayer` | IMPLEMENTED | `dashboard_renderer_test.go:296-317` |
| `TestSplitIntoLayerSections` | IMPLEMENTED | `dashboard_renderer_test.go:335-368` |
| Additional: `TestRenderSplit_ZeroMaxLen` | IMPLEMENTED | `dashboard_renderer_test.go:229-248` -- extra test not in plan |
| Additional: `TestSplitAtLayerBoundaries_UnsplittableSingleSection` | IMPLEMENTED | `dashboard_renderer_test.go:319-333` -- extra test |
| Additional: `TestDashboardStatus_String` | IMPLEMENTED | `dashboard_renderer_test.go:370-389` |
| Additional: `TestDashboardStatus_Icon` | IMPLEMENTED | `dashboard_renderer_test.go:391-409` |
| Additional: `TestDashboardLayer_IsCompleted` | IMPLEMENTED | `dashboard_renderer_test.go:411-469` |
| Additional: `TestDashboardLayer_CompletedCount` | IMPLEMENTED | `dashboard_renderer_test.go:471-484` |
| Additional: `TestDashboardLayer_Summary` | IMPLEMENTED | `dashboard_renderer_test.go:486-523` |

#### 9.2 DashboardUpdater Tests

| Plan Item | Status | Notes |
|---|---|---|
| `TestUpdate_CreateNew` | IMPLEMENTED | `dashboard_updater_test.go:81-99` |
| `TestUpdate_UpdateExisting` | IMPLEMENTED | `dashboard_updater_test.go:101-127` |
| `TestUpdate_SplitAndUpdate` | MISSING | No test exercises the split-then-update path (1 existing + split into 2) |
| `TestUpdate_ShrinkDashboard` | IMPLEMENTED | `dashboard_updater_test.go:129-163` |
| `TestUpdate_EditFails_FallsBackToCreate` | IMPLEMENTED | `dashboard_updater_test.go:165-185` |
| `TestUpdate_ListCommentsFails` | IMPLEMENTED | `dashboard_updater_test.go:187-204` |
| `TestIsDashboardComment_Sentinel` | IMPLEMENTED | `dashboard_updater_test.go:206-211` |
| `TestIsDashboardComment_ContinuedSentinel` | IMPLEMENTED | `dashboard_updater_test.go:213-218` |
| `TestIsDashboardComment_NormalComment` | IMPLEMENTED | `dashboard_updater_test.go:220-225` |
| Additional: `TestNewDashboardUpdater` | IMPLEMENTED | `dashboard_updater_test.go:227-237` |
| Uses fake VCS client instead of pegomock | DIVERGED | Uses hand-written `fakeVCSClient` test double. Simpler and avoids mock generation dependency. Acceptable. |

#### 9.3 VCS Client Tests (per provider)

| Plan Item | Status | Notes |
|---|---|---|
| GitHub `ListComments`/`EditComment` tests | MISSING | No `TestListComments`/`TestEditComment` in `server/events/vcs/github/client_test.go` |
| GitLab `ListComments`/`EditComment` tests | MISSING | No tests in `server/events/vcs/gitlab/client_test.go` |
| Bitbucket Cloud tests | MISSING | No tests |
| Bitbucket Server tests | MISSING | No tests |
| Azure DevOps tests | MISSING | No tests (methods are stubs anyway) |
| Gitea tests | MISSING | No tests |

---

## Gaps Requiring Action

1. **`TestUpdate_SplitAndUpdate` test missing** (`dashboard_updater_test.go`): The plan specifies a test case where the dashboard splits into 2 parts with 1 existing comment -- verifying the first is updated via `EditComment` and the second is created via `CreateComment`. This path exercises the mixed update/create logic and should be added.

2. **Azure DevOps `ListComments`/`EditComment` are stubs**: Both methods return `fmt.Errorf("not yet implemented")`. The plan states all providers should implement these methods. While Azure DevOps may not be a priority, the stub error means the dashboard will fail entirely for Azure DevOps users. At minimum, these should gracefully degrade (e.g., `ListComments` returning empty, `EditComment` falling back to create-only mode). The `DashboardUpdater` already handles `ListComments` failure gracefully by creating new comments, but `EditComment` failure triggers the create-fallback path, so the current behavior is actually acceptable -- but the stubs should ideally be real implementations.

3. **VCS provider-level tests missing for all providers**: The plan section 9.3 calls for httptest-based tests for `ListComments`/`EditComment` on each provider. None exist. This is a meaningful coverage gap, especially for the more complex implementations (Bitbucket Server with version-fetch, Bitbucket Cloud with JSON marshaling).

---

## Minor Divergences (Acceptable)

1. **Status type design**: Implementation uses `DashboardStatus` wrapper around `models.ProjectPlanStatus` instead of a standalone `ProjectStatus` enum. This is an improvement -- it avoids duplicate status definitions and ensures the dashboard stays in sync with the model layer automatically.

2. **Additional statuses**: Implementation handles `ErroredPlanStatus`, `ApplyingStatus`, and `PassedPolicyCheckStatus` which were not in the plan's `ProjectStatus` enum. This provides better coverage of real-world states.

3. **Template formatting**: Minor differences in the template (italic `_(current)_` vs `(current)`, em-dash vs hyphen, backtick-quoted project names, more descriptive pending text). These are cosmetic improvements.

4. **Completed layers use `<details><summary><b>Layer N</b>` format**: The layer header is inside the `<summary>` tag rather than using `**Layer N**` outside of `<details>`. This renders better in VCS providers and keeps the collapsed state cleaner.

5. **`normalizeMarkdown()` helper**: Added to clean up template whitespace. Not in plan but necessary for clean output from Go templates.

6. **`layerSplitMarker` constant not used**: Plan defined it but implementation uses line-prefix detection instead. Simpler approach that avoids template coupling.

7. **`splitIntoLayerSections` detects both `**Layer ` and `<details><summary><b>Layer `**: Necessary because completed layers use `<details>` tags. The plan only mentioned `**Layer ` detection.

8. **`NewDashboardUpdater` signature**: Takes `markdownTemplateOverridesDir` as second parameter, supporting section 7.2's template override requirement. The plan showed a simpler constructor.

9. **Fake VCS client instead of pegomock**: The updater tests use a hand-written fake rather than pegomock mocks. This is simpler, more readable, and avoids mock generation issues.

10. **`IsCompleted()` handles empty slice**: Returns `false` for layers with no projects, which the plan's version did not explicitly handle (would have returned `true` for empty, since the loop wouldn't find any non-terminal projects).

11. **`InstrumentedClient.MaxCommentLength()` not explicitly instrumented**: Delegates via embedded `vcs.Client` interface rather than having an explicit method with metrics. Acceptable since `MaxCommentLength` is a simple constant return with no I/O.

---

## Test Results

All 29 tests pass:

```
ok  github.com/runatlantis/atlantis/server/events/layers  0.800s
```
