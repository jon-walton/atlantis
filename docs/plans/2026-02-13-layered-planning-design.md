# Layered Planning and Applies

## Overview

Atlantis currently plans all changed projects simultaneously regardless of dependency order. This means downstream projects plan against stale/mock state, producing inaccurate plan output. Layered planning makes planning itself dependency-aware: downstream projects are only planned after their upstream dependencies have been applied.

## Core Model

### Layer Calculation

The dependency graph from `depends_on` is topologically sorted into layers. A layer is a set of projects whose dependencies are all in earlier layers.

**Inclusion rules:**

1. A project is **in scope** if it has git file changes OR a direct upstream dependency (that is in scope) planned with actual infrastructure changes (adds/removes/updates).
2. Scope is discovered progressively — rule 1's second condition can only be evaluated after planning.
3. Projects with no actual infrastructure changes do **not** cascade to their downstream dependents.

**Layer assignment for in-scope projects:**

- Layer 0: In-scope projects whose dependencies are either not in scope or have no in-scope dependencies ahead of them.
- Layer N+1: In-scope projects whose upstream dependency is in layer N.
- If both A and B have git changes but B depends on A, then A is layer 0 and B is layer 1 — git changes determine initial scope, but the dependency graph determines layer placement.

### Behavior

- Only the current layer is planned. Downstream projects are suppressed entirely.
- `atlantis apply` applies the current layer (all at once or per-project).
- After a layer is fully applied, the next layer is automatically planned.
- A layer doesn't advance until all projects in it are either applied or planned with no changes.
- A failed apply blocks the layer from advancing.

## Plan/Apply Lifecycle

### On PR Open or Push (Autoplan)

1. Detect changed files, match to projects.
2. If layered planning is enabled and any in-scope project has `depends_on`, build the dependency graph.
3. Calculate layer 0 — in-scope projects with no in-scope dependencies.
4. Plan layer 0 only.
5. Post the dashboard sticky comment showing layer 0 results and pending count.
6. Post individual plan output as separate comments (same as today).

### On `atlantis apply` (No Args)

1. Apply all planned-with-changes projects in the current layer.
2. If any apply fails, update dashboard, block layer advancement.
3. If all succeed, automatically plan the next layer based on cascade rules.
4. Update dashboard with new layer results.

### On `atlantis apply -d <project>`

1. Apply the single project within the current layer.
2. Update dashboard.
3. If this was the last project needing apply in the layer (all others are applied or no-changes), trigger next layer planning.

### On `atlantis plan` (Manual Re-plan)

1. Re-plans the current layer (useful after fixing a failed apply or pushing new commits).
2. Does not advance layers — just refreshes current layer plans.

## Dashboard Comment

A single sticky comment updated after every state change. Acts as the "at a glance" progress view.

### Format

Completed layers are collapsed:

```
**Layer 1** - 12 projects applied
<details><summary>Show projects</summary>

- ✅ Applied - vpc
- ✅ Applied - dns
- ...

</details>
```

Current layer is expanded:

```
**Layer 2** (current)
---
- ✅ Applied - iam
- 🔶 Planned (changes) - security-groups
- ⚪ Planned (no changes) - logging
- ❌ Apply failed - networking
```

Pending count at the bottom:

```
85 projects pending
```

### Status Lifecycle

```
PendingPlanStatus (⏳)  ──→  PlannedPlanStatus (🔶 changes)  ──→  ApplyingStatus (⏳)  ──→  AppliedStatus (✅)
                        ──→  ErroredPlanStatus (❌ plan failed)
                        ──→  PlannedNoChangesPlanStatus (⚪ no changes)  [terminal]
                                                                   ──→  ErroredApplyStatus (❌ apply failed)
                                                                   ──→  SkippedPlanStatus (⏭️ skipped)  [terminal]
```

Terminal states (`AppliedStatus`, `PlannedNoChangesPlanStatus`, `SkippedPlanStatus`) determine when a layer can advance. All projects in a layer must reach a terminal state before the next layer is planned.

The `statusDisplay` map in `layers/dashboard_types.go` is the single source of truth mapping each `models.ProjectPlanStatus` value to its display label and icon.

### Comment Splitting

Some VCS providers have character limits (GitHub ~65,536 chars). When the dashboard approaches the limit:

1. Render the full dashboard markdown.
2. If over limit, split at the nearest layer boundary that fits.
3. Second comment also gets sticky/update treatment.

### When to Show

The dashboard only appears when there are multiple layers. Single-layer PRs behave identically to today with no dashboard overhead.

## Smart Re-plan on New Commits

When new commits are pushed, determine which projects are affected by the changed files:

1. **Affected project is in a future layer (not yet planned):** Do nothing — it will be planned with the new code when its layer comes up naturally.
2. **Affected project is in the current layer:** Re-plan that project within the current layer. Don't touch already-applied projects in this layer.
3. **Affected project is in an already-applied layer:** Reset back to that layer. The project needs re-planning and re-applying. Everything from that layer forward is invalidated.

## Skip Functionality

### Configuration

Server-side config flag `enable-layered-apply-skip` — default off.

### Behavior

When enabled:

- User can run `atlantis apply -skip <project>` to mark a failed project as skipped.
- Skipped projects show as `⏭️ Skipped - <project>` in the dashboard.
- A skipped project's downstream dependents are excluded from subsequent layers (cascade stops).
- A layer can advance once all projects are either applied, no changes, or skipped.

### Constraints

- Can only skip projects in the current layer.
- Can only skip projects that have failed an apply.

## Edge Cases

- **PR with no `depends_on` configured:** Even with layered planning enabled, if no projects have `depends_on`, all projects land in layer 0 and behavior is identical to today.
- **Circular dependencies:** Detected during graph building, rejected with a clear error comment on the PR.
- **All projects in a layer show no changes:** Layer auto-advances to the next. Cascading stops for those branches. If there's no next layer to plan, we're done.

## Configuration

Two server-side flags, both default off:

- `enable-layered-planning` — Enables the layered plan/apply workflow for repos with `depends_on`.
- `enable-layered-apply-skip` — Enables the `atlantis apply -skip` command for failed applies.

## Testing Strategy

### Approach

- **TDD for pure logic** (workstreams 1, 2: dependency graph, state manager) — write tests first, implement to pass. These have well-defined inputs/outputs and the test cases are already specified in the plans.
- **Test-alongside for integration work** (workstreams 3, 4, 5: dashboard renderer, lifecycle integration, re-plan/config) — write implementation and tests together, following existing codebase patterns.

### Unit Tests

Each workstream plan includes detailed test case tables. Tests use:
- **Pegomock** for mock generation (existing codebase standard)
- **Table-driven tests** for comprehensive coverage
- **Custom assertions** from `testing/` package (`Ok`, `Equals`, `ErrContains`)
- **httptest** servers for VCS API testing (dashboard renderer VCS methods)

### Orchestrator Integration Tests

Wire the real graph calculator, state manager, and dashboard renderer together with a mocked VCS client and mocked Terraform execution. No real Terraform or VCS calls. Covers:

- Full layered lifecycle: plan layer 0 → apply → auto-plan layer 1 → apply → done
- Dynamic cascade: layer 0 has changes → dependents included; no changes → cascade stops
- Apply failure blocks layer advancement
- Skip functionality (when enabled) advances layer and stops cascade
- Smart re-plan: push affecting current layer, future layer, past layer
- Dashboard comment creation, updates, and splitting
- Edge cases: circular dependencies, no `depends_on`, single layer (no dashboard)

### E2E Test

One happy-path E2E test using the existing `events_controller_e2e_test.go` pattern with real Terraform and a new test repo fixture:

```
testdata/test-repos/layered-planning/
├── atlantis.yaml          # Projects A, B, C with depends_on
├── project-a/main.tf      # Creates a resource
├── project-b/main.tf      # depends_on A
└── project-c/main.tf      # depends_on B
```

Validates: autoplan triggers only layer 0 → apply → layer 1 auto-plans → apply → layer 2 auto-plans → apply → done. Dashboard comment appears and updates correctly throughout.

## Out of Scope (Future Work)

- Automatic layer calculation replacing `execution_order_group` (migration path).
- Any UI beyond PR comments.
- Cross-PR dependency awareness.
- Partial layer advancement (different branches of the graph progressing independently).