# Layered Planning: Dashboard Comment Renderer

## Implementation Plan

This plan covers the PR comment rendering for the layered planning dashboard: sticky comment creation/updating, the dashboard template, comment splitting at layer boundaries, and integration with the existing `MarkdownRenderer`.

---

## 1. Architecture Overview

The dashboard is a **single sticky comment** (or a small set of sticky comments if splitting is needed) that is updated in-place after every state change. It is separate from the per-project plan/apply output comments that Atlantis already posts.

```
+--------------------------+
|  DashboardRenderer       |  New struct, owns template + rendering logic
|  - RenderDashboard()     |  Renders LayerState -> markdown string(s)
|  - SplitAtLayerBoundary()|  Splits at layer boundaries if over VCS limit
+--------------------------+
          |
          v
+--------------------------+
|  DashboardUpdater        |  New struct, owns sticky comment lifecycle
|  - UpdateDashboard()     |  Find-or-create sticky comment, update body
+--------------------------+
          |
          v
+--------------------------+
|  vcs.Client              |  Extended with new methods
|  + ListComments()        |  List all comments on a PR
|  + EditComment()         |  Update an existing comment body by ID
+--------------------------+
```

The `DashboardUpdater` is called from the layered planning orchestrator (a different workstream) after each plan/apply/skip event.

---

## 2. VCS Client Interface Changes

### 2.1 New Methods on `vcs.Client`

The VCS `Client` interface needs two new methods. These are required because the existing interface only has `CreateComment` (no way to list or edit existing comments).

**File: `server/events/vcs/client.go`**

Add to the `Client` interface:

```go
// ListComments returns all comments on a pull request.
// Each comment includes its ID, body text, and the login of the author.
ListComments(logger logging.SimpleLogging, repo models.Repo, pullNum int) ([]PullComment, error)

// EditComment updates the body of an existing comment by its ID.
EditComment(logger logging.SimpleLogging, repo models.Repo, pullNum int, commentID int64, body string) error
```

**New type in `server/events/vcs/client.go`:**

```go
// PullComment represents a comment on a pull request.
type PullComment struct {
	ID     int64
	Body   string
	Author string
}
```

### 2.2 Implementation per VCS Provider

Each VCS client must implement the two new methods. The following table summarizes the API calls and comment size limits:

| Provider         | maxCommentLength | ListComments API                                    | EditComment API                                      |
|------------------|------------------|-----------------------------------------------------|------------------------------------------------------|
| GitHub           | 65,536           | `Issues.ListComments`                               | `Issues.EditComment`                                 |
| GitLab           | 999,900          | `Notes.ListMergeRequestNotes`                       | `Notes.UpdateMergeRequestNote`                       |
| Bitbucket Cloud  | ~200,000+        | `GET /2.0/repositories/{}/pullrequests/{}/comments`  | `PUT /2.0/repositories/{}/pullrequests/{}/comments/{}` |
| Bitbucket Server | 32,768           | Already lists in `HidePrevCommandComments` pattern  | `PUT /rest/api/1.0/.../comments/{}`                  |
| Azure DevOps     | 150,000          | `PullRequests.GetComments`                          | `PullRequests.UpdateComment`                         |
| Gitea            | 65,536 (assumed) | `ListIssueComments` (already used)                  | `EditIssueComment` (already used)                    |

**Files to modify:**

- `server/events/vcs/client.go` -- interface + `PullComment` type
- `server/events/vcs/github/client.go` -- implement `ListComments`, `EditComment`
- `server/events/vcs/gitlab/client.go` -- implement `ListComments`, `EditComment`
- `server/events/vcs/bitbucketcloud/client.go` -- implement `ListComments`, `EditComment`
- `server/events/vcs/bitbucketserver/client.go` -- implement `ListComments`, `EditComment`
- `server/events/vcs/azuredevops/client.go` -- implement `ListComments`, `EditComment`
- `server/events/vcs/gitea/client.go` -- implement `ListComments`, `EditComment`
- `server/events/vcs/not_configured_vcs_client.go` -- stub implementations
- `server/events/vcs/proxy.go` -- proxy implementations
- `server/events/vcs/common/instrumented_client.go` -- instrumented wrappers
- `server/events/vcs/mocks/mock_client.go` -- regenerate mock

#### 2.2.1 GitHub Implementation

**File: `server/events/vcs/github/client.go`**

```go
func (g *Client) ListComments(logger logging.SimpleLogging, repo models.Repo, pullNum int) ([]vcs.PullComment, error) {
	logger.Debug("Listing comments on GitHub pull request %d", pullNum)
	var result []vcs.PullComment
	nextPage := 0
	for {
		comments, resp, err := g.client.Issues.ListComments(g.ctx, repo.Owner, repo.Name, pullNum, &github.IssueListCommentsOptions{
			Sort:        github.Ptr("created"),
			Direction:   github.Ptr("asc"),
			ListOptions: github.ListOptions{Page: nextPage},
		})
		if resp != nil {
			logger.Debug("GET /repos/%v/%v/issues/%d/comments returned: %v", repo.Owner, repo.Name, pullNum, resp.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("listing comments: %w", err)
		}
		for _, c := range comments {
			author := ""
			if c.User != nil {
				author = c.User.GetLogin()
			}
			result = append(result, vcs.PullComment{
				ID:     c.GetID(),
				Body:   c.GetBody(),
				Author: author,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		nextPage = resp.NextPage
	}
	return result, nil
}

func (g *Client) EditComment(logger logging.SimpleLogging, repo models.Repo, _ int, commentID int64, body string) error {
	logger.Debug("Editing comment %d on GitHub repo %s/%s", commentID, repo.Owner, repo.Name)
	_, resp, err := g.client.Issues.EditComment(g.ctx, repo.Owner, repo.Name, commentID, &github.IssueComment{
		Body: &body,
	})
	if resp != nil {
		logger.Debug("PATCH /repos/%v/%v/issues/comments/%d returned: %v", repo.Owner, repo.Name, commentID, resp.StatusCode)
	}
	return err
}
```

Note: `ListComments` already exists in `HidePrevCommandComments` on GitHub but as inline logic. We are extracting it to a standalone method. The `HidePrevCommandComments` function can later be refactored to use `ListComments` internally, but that refactor is out of scope.

#### 2.2.2 GitLab Implementation

**File: `server/events/vcs/gitlab/client.go`**

```go
func (g *Client) ListComments(logger logging.SimpleLogging, repo models.Repo, pullNum int) ([]vcs.PullComment, error) {
	logger.Debug("Listing comments on GitLab merge request %d", pullNum)
	var result []vcs.PullComment
	nextPage := 0
	for {
		notes, resp, err := g.Client.Notes.ListMergeRequestNotes(repo.FullName, pullNum,
			&gitlab.ListMergeRequestNotesOptions{
				Sort:        gitlab.Ptr("asc"),
				OrderBy:     gitlab.Ptr("created_at"),
				ListOptions: gitlab.ListOptions{Page: nextPage},
			})
		if resp != nil {
			logger.Debug("GET /projects/%s/merge_requests/%d/notes returned: %d", repo.FullName, pullNum, resp.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("listing comments: %w", err)
		}
		for _, n := range notes {
			if n.System {
				continue // Skip system notes
			}
			result = append(result, vcs.PullComment{
				ID:     int64(n.ID),
				Body:   n.Body,
				Author: n.Author.Username,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		nextPage = resp.NextPage
	}
	return result, nil
}

func (g *Client) EditComment(logger logging.SimpleLogging, repo models.Repo, pullNum int, commentID int64, body string) error {
	logger.Debug("Editing comment %d on GitLab merge request %d", commentID, pullNum)
	_, resp, err := g.Client.Notes.UpdateMergeRequestNote(repo.FullName, pullNum, int(commentID),
		&gitlab.UpdateMergeRequestNoteOptions{Body: &body})
	if resp != nil {
		logger.Debug("PUT /projects/%s/merge_requests/%d/notes/%d returned: %d", repo.FullName, pullNum, commentID, resp.StatusCode)
	}
	return err
}
```

#### 2.2.3 Other Providers

Bitbucket Cloud, Bitbucket Server, Azure DevOps, and Gitea follow the same pattern using their respective APIs. Gitea already has `EditIssueComment` in use (in `HidePrevCommandComments`), so it can be directly reused. For providers that don't support edit (none currently -- all major providers do), `EditComment` should fall back to: delete old + create new.

The `NotConfiguredVCSClient` stubs should return `a.err()` for both methods.

### 2.3 Exposing maxCommentLength

Each VCS client currently defines `maxCommentLength` as a package-level constant. The dashboard comment splitting needs access to this value per-provider. Add a new method to the interface:

```go
// MaxCommentLength returns the maximum number of characters allowed
// in a single comment for this VCS provider. Returns 0 if unlimited.
MaxCommentLength() int
```

Implement in each provider returning their respective constant. This avoids the dashboard renderer needing to know about VCS internals.

**Alternative considered:** Pass the limit as a parameter to the renderer. This is simpler but couples the caller to knowing VCS details. The method approach is cleaner for the interface.

---

## 3. Dashboard Data Model

### 3.1 Types

**New file: `server/events/layers/dashboard_types.go`**

```go
package layers

// ProjectStatus represents the status of a single project within a layer.
type ProjectStatus int

const (
	StatusPlanning    ProjectStatus = iota // Currently being planned
	StatusPlanned                         // Plan completed, has changes
	StatusNoChanges                       // Plan completed, no changes
	StatusApplied                         // Successfully applied
	StatusApplyFailed                     // Apply failed
	StatusSkipped                         // User skipped (after failed apply)
)

// String returns the display string for a project status.
func (s ProjectStatus) String() string {
	switch s {
	case StatusPlanning:
		return "Planning..."
	case StatusPlanned:
		return "Planned (changes)"
	case StatusNoChanges:
		return "Planned (no changes)"
	case StatusApplied:
		return "Applied"
	case StatusApplyFailed:
		return "Apply failed"
	case StatusSkipped:
		return "Skipped"
	default:
		return "Unknown"
	}
}

// Icon returns the status icon emoji for a project status.
func (s ProjectStatus) Icon() string {
	switch s {
	case StatusPlanning:
		return "\u23f3"     // hourglass
	case StatusPlanned:
		return "\U0001f536" // large orange diamond
	case StatusNoChanges:
		return "\u26aa"     // white circle
	case StatusApplied:
		return "\u2705"     // white check mark
	case StatusApplyFailed:
		return "\u274c"     // cross mark
	case StatusSkipped:
		return "\u23ed\ufe0f" // next track button
	default:
		return "\u2753"     // question mark
	}
}

// DashboardProject represents a single project entry in the dashboard.
type DashboardProject struct {
	Name   string
	Status ProjectStatus
}

// DashboardLayer represents a single layer in the dashboard.
type DashboardLayer struct {
	Number   int
	Projects []DashboardProject
	IsCurrent bool
}

// IsCompleted returns true if all projects in the layer are in a terminal state.
func (l DashboardLayer) IsCompleted() bool {
	for _, p := range l.Projects {
		switch p.Status {
		case StatusApplied, StatusNoChanges, StatusSkipped:
			continue
		default:
			return false
		}
	}
	return true
}

// CompletedCount returns the number of projects in terminal states.
func (l DashboardLayer) CompletedCount() int {
	count := 0
	for _, p := range l.Projects {
		switch p.Status {
		case StatusApplied, StatusNoChanges, StatusSkipped:
			count++
		}
	}
	return count
}

// Summary returns a short summary like "12 projects applied".
func (l DashboardLayer) Summary() string {
	applied := 0
	noChanges := 0
	skipped := 0
	for _, p := range l.Projects {
		switch p.Status {
		case StatusApplied:
			applied++
		case StatusNoChanges:
			noChanges++
		case StatusSkipped:
			skipped++
		}
	}
	// Build a human-readable summary
	parts := []string{}
	if applied > 0 {
		parts = append(parts, fmt.Sprintf("%d applied", applied))
	}
	if noChanges > 0 {
		parts = append(parts, fmt.Sprintf("%d no changes", noChanges))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	return strings.Join(parts, ", ")
}

// DashboardData is the top-level data structure passed to the dashboard template.
type DashboardData struct {
	Layers       []DashboardLayer
	PendingCount int    // Number of projects not yet in any planned/active layer
	TotalCount   int    // Total number of projects across all layers + pending
	RepoName     string // For display context
	PullNum      int
}
```

---

## 4. Dashboard Template

### 4.1 Template File

**New file: `server/events/templates/layered_dashboard.tmpl`**

```gotemplate
{{ define "layeredDashboard" -}}
<!-- Atlantis Layered Planning Dashboard -->
## Layered Planning Dashboard

{{ range .Layers -}}
{{ if .IsCurrent -}}
**Layer {{ .Number }}** (current)
---
{{ range .Projects -}}
- {{ .Status.Icon }} {{ .Status.String }} - {{ .Name }}
{{ end -}}
{{ else if .IsCompleted -}}
**Layer {{ .Number }}** - {{ .Summary }}
<details><summary>Show projects</summary>

{{ range .Projects -}}
- {{ .Status.Icon }} {{ .Status.String }} - {{ .Name }}
{{ end -}}
</details>

{{ else -}}
**Layer {{ .Number }}**
---
{{ range .Projects -}}
- {{ .Status.Icon }} {{ .Status.String }} - {{ .Name }}
{{ end -}}
{{ end -}}
{{ end -}}
{{ if gt .PendingCount 0 -}}
---
*{{ .PendingCount }} projects pending*
{{ end -}}
{{ end -}}
```

### 4.2 Sentinel Comment Marker

To identify dashboard comments for sticky-update behavior, each dashboard comment begins with an HTML comment sentinel that is invisible in rendered markdown:

```
<!-- Atlantis Layered Planning Dashboard -->
```

This sentinel is embedded in the template (line 2 above). When searching for an existing dashboard comment to update, we look for this exact string in the comment body.

If the dashboard is split across multiple comments, subsequent comments use:

```
<!-- Atlantis Layered Planning Dashboard (continued N) -->
```

where N is the part number (2, 3, ...).

---

## 5. DashboardRenderer

### 5.1 Implementation

**New file: `server/events/layers/dashboard_renderer.go`**

```go
package layers

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

const (
	// DashboardSentinel is the HTML comment used to identify dashboard comments.
	DashboardSentinel = "<!-- Atlantis Layered Planning Dashboard -->"
	// DashboardContinuedSentinelFmt is the format string for continued dashboard comments.
	DashboardContinuedSentinelFmt = "<!-- Atlantis Layered Planning Dashboard (continued %d) -->"

	// layerSplitMarker is used internally to identify split points between layers.
	layerSplitMarker = "<!-- layer-boundary -->"
)

//go:embed templates/layered_dashboard.tmpl
var dashboardTemplateFS embed.FS

// DashboardRenderer renders the layered planning dashboard markdown.
type DashboardRenderer struct {
	templates *template.Template
}

// NewDashboardRenderer creates a new DashboardRenderer.
func NewDashboardRenderer() *DashboardRenderer {
	templates, _ := template.New("").Funcs(sprig.TxtFuncMap()).ParseFS(dashboardTemplateFS, "templates/*.tmpl")
	return &DashboardRenderer{
		templates: templates,
	}
}

// Render produces the full dashboard markdown from the given data.
func (r *DashboardRenderer) Render(data DashboardData) string {
	buf := &bytes.Buffer{}
	tmpl := r.templates.Lookup("layeredDashboard")
	if tmpl == nil {
		return "Failed to find layered dashboard template, this is a bug"
	}
	if err := tmpl.Execute(buf, data); err != nil {
		return fmt.Sprintf("Failed to render layered dashboard template, this is a bug: %v", err)
	}
	return strings.TrimSpace(buf.String())
}

// RenderSplit renders the dashboard and splits it if it exceeds maxLen.
// Returns one or more comment bodies, each within maxLen.
// Splits occur at layer boundaries to keep layers intact.
func (r *DashboardRenderer) RenderSplit(data DashboardData, maxLen int) []string {
	full := r.Render(data)

	if maxLen <= 0 || len(full) <= maxLen {
		return []string{full}
	}

	return splitAtLayerBoundaries(full, maxLen)
}
```

### 5.2 Comment Splitting Algorithm

**Still in `server/events/layers/dashboard_renderer.go`:**

The splitting algorithm works differently from the existing `common.SplitComment` (which splits at arbitrary character boundaries). The dashboard renderer splits at **layer boundaries** to keep each layer's rendering intact.

```go
// splitAtLayerBoundaries splits a rendered dashboard at layer boundaries.
// Each resulting part is guaranteed to be <= maxLen.
// If a single layer exceeds maxLen on its own, it falls back to truncation.
func splitAtLayerBoundaries(rendered string, maxLen int) []string {
	// Split the rendered output into sections.
	// Each section starts with "**Layer N**" which we use as the boundary.
	// We also look for the pending count footer.
	sections := splitIntoLayerSections(rendered)

	if len(sections) <= 1 {
		// Can't split any further at layer boundaries.
		// Fall back to simple truncation with ellipsis.
		if len(rendered) > maxLen {
			return []string{rendered[:maxLen-50] + "\n\n... (dashboard truncated)"}
		}
		return []string{rendered}
	}

	// Greedily pack sections into comments.
	var comments []string
	var current strings.Builder
	partNum := 1

	// First comment gets the original sentinel (already in the rendered output).
	// Subsequent comments get the continued sentinel.
	for i, section := range sections {
		sentinel := ""
		if partNum > 1 && current.Len() == 0 {
			sentinel = fmt.Sprintf(DashboardContinuedSentinelFmt, partNum) + "\n"
		}

		proposedLen := current.Len() + len(sentinel) + len(section)

		if current.Len() > 0 && proposedLen > maxLen {
			// Current comment is full. Finalize it and start a new one.
			comments = append(comments, strings.TrimSpace(current.String()))
			current.Reset()
			partNum++
			sentinel = fmt.Sprintf(DashboardContinuedSentinelFmt, partNum) + "\n"
		}

		if current.Len() == 0 && i > 0 {
			current.WriteString(sentinel)
		}
		current.WriteString(section)
	}

	if current.Len() > 0 {
		comments = append(comments, strings.TrimSpace(current.String()))
	}

	return comments
}

// splitIntoLayerSections splits the rendered dashboard into sections,
// one per layer plus the header and footer.
func splitIntoLayerSections(rendered string) []string {
	lines := strings.Split(rendered, "\n")
	var sections []string
	var current strings.Builder

	for _, line := range lines {
		// A new layer section starts with "**Layer "
		if strings.HasPrefix(line, "**Layer ") && current.Len() > 0 {
			sections = append(sections, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}

	if current.Len() > 0 {
		sections = append(sections, current.String())
	}

	return sections
}
```

**Why not reuse `common.SplitComment`?** The existing function splits at arbitrary byte boundaries, which would break markdown rendering mid-table or mid-details-block. Layer-boundary splitting produces cleaner, readable results. We only fall back to truncation if a single layer is too large (extremely unlikely in practice since each layer entry is ~40 chars).

---

## 6. DashboardUpdater (Sticky Comment Manager)

### 6.1 Implementation

**New file: `server/events/layers/dashboard_updater.go`**

```go
package layers

import (
	"strings"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/logging"
)

// DashboardUpdater manages the lifecycle of sticky dashboard comments.
type DashboardUpdater struct {
	VCSClient vcs.Client
	Renderer  *DashboardRenderer
}

// NewDashboardUpdater creates a new DashboardUpdater.
func NewDashboardUpdater(client vcs.Client) *DashboardUpdater {
	return &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(),
	}
}

// Update renders the dashboard and creates or updates the sticky comment(s).
// It finds existing dashboard comments by sentinel, updates them in-place,
// and creates new ones if needed. Removes excess comments if the dashboard
// now fits in fewer comments than before.
func (u *DashboardUpdater) Update(
	logger logging.SimpleLogging,
	repo models.Repo,
	pullNum int,
	data DashboardData,
) error {
	maxLen := u.VCSClient.MaxCommentLength()
	bodies := u.Renderer.RenderSplit(data, maxLen)

	// Find existing dashboard comments.
	existing, err := u.findDashboardComments(logger, repo, pullNum)
	if err != nil {
		logger.Warn("failed to find existing dashboard comments, will create new: %s", err)
		existing = nil
	}

	// Update or create comments.
	for i, body := range bodies {
		if i < len(existing) {
			// Update existing comment.
			logger.Debug("Updating dashboard comment %d (ID: %d)", i+1, existing[i].ID)
			if err := u.VCSClient.EditComment(logger, repo, pullNum, existing[i].ID, body); err != nil {
				logger.Err("failed to update dashboard comment %d: %s", existing[i].ID, err)
				// Fall back to creating a new comment.
				if err := u.VCSClient.CreateComment(logger, repo, pullNum, body, ""); err != nil {
					return err
				}
			}
		} else {
			// Create new comment.
			logger.Debug("Creating new dashboard comment (part %d)", i+1)
			if err := u.VCSClient.CreateComment(logger, repo, pullNum, body, ""); err != nil {
				return err
			}
		}
	}

	// If we previously had more comments than we need now, hide/clear the extras.
	// This happens when the dashboard shrinks (e.g., layers complete and collapse).
	for i := len(bodies); i < len(existing); i++ {
		logger.Debug("Clearing excess dashboard comment %d (ID: %d)", i+1, existing[i].ID)
		clearedBody := fmt.Sprintf("%s\n\n*This dashboard section is no longer needed.*", DashboardSentinel)
		if err := u.VCSClient.EditComment(logger, repo, pullNum, existing[i].ID, clearedBody); err != nil {
			logger.Warn("failed to clear excess dashboard comment %d: %s", existing[i].ID, err)
		}
	}

	return nil
}

// findDashboardComments returns existing dashboard comments in order.
// It identifies them by the sentinel HTML comment at the start of the body.
func (u *DashboardUpdater) findDashboardComments(
	logger logging.SimpleLogging,
	repo models.Repo,
	pullNum int,
) ([]vcs.PullComment, error) {
	allComments, err := u.VCSClient.ListComments(logger, repo, pullNum)
	if err != nil {
		return nil, err
	}

	var dashboardComments []vcs.PullComment
	for _, c := range allComments {
		if isDashboardComment(c.Body) {
			dashboardComments = append(dashboardComments, c)
		}
	}

	return dashboardComments, nil
}

// isDashboardComment returns true if the comment body contains a dashboard sentinel.
func isDashboardComment(body string) bool {
	return strings.Contains(body, DashboardSentinel) ||
		strings.Contains(body, "<!-- Atlantis Layered Planning Dashboard (continued")
}
```

### 6.2 Sticky Comment Flow

The update flow is:

1. **Render** the dashboard markdown from current `DashboardData`.
2. **Split** if needed (layer-boundary splitting against `MaxCommentLength()`).
3. **List** all comments on the PR via `ListComments`.
4. **Find** existing dashboard comments by checking for the sentinel marker.
5. **Update** each existing comment with new body via `EditComment`, or **create** new ones.
6. **Clear** any excess old dashboard comments that are no longer needed.

This is idempotent: calling `Update` multiple times with the same data produces the same result.

---

## 7. Integration with Existing MarkdownRenderer

The `DashboardRenderer` is intentionally **separate** from the existing `MarkdownRenderer`. Reasons:

1. **Different lifecycle.** The `MarkdownRenderer` renders per-command results. The dashboard is a persistent, stateful comment updated across multiple commands.
2. **Different data model.** The `MarkdownRenderer` operates on `command.Result` / `command.ProjectResult`. The dashboard operates on `DashboardData` / `DashboardLayer`.
3. **Different templates.** Dashboard templates are in the `layers` package's embedded FS, not in `server/events/templates/`.

### 7.1 Coexistence

The layered planning orchestrator will:
1. Call `PullUpdater.updatePull()` as normal to post per-project plan/apply output (existing behavior, unchanged).
2. Call `DashboardUpdater.Update()` to update the sticky dashboard comment.

Both happen independently. The dashboard is additive context, not a replacement for individual project output.

### 7.2 Template Overrides

The dashboard template supports the same override mechanism as existing templates. If the user provides `--markdown-template-overrides-dir`, templates named `layered_dashboard.tmpl` in that directory will override the embedded default.

To support this, `NewDashboardRenderer` should accept an optional overrides dir parameter:

```go
func NewDashboardRenderer(markdownTemplateOverridesDir string) *DashboardRenderer {
	templates, _ := template.New("").Funcs(sprig.TxtFuncMap()).ParseFS(dashboardTemplateFS, "templates/*.tmpl")
	if markdownTemplateOverridesDir != "" {
		if overrides, err := templates.ParseGlob(
			fmt.Sprintf("%s/*.tmpl", markdownTemplateOverridesDir),
		); err == nil {
			templates = overrides
		}
	}
	return &DashboardRenderer{templates: templates}
}
```

---

## 8. Complete File Inventory

### New Files

| File | Purpose |
|------|---------|
| `server/events/layers/dashboard_types.go` | `ProjectStatus`, `DashboardProject`, `DashboardLayer`, `DashboardData` types |
| `server/events/layers/dashboard_renderer.go` | `DashboardRenderer` struct, `Render()`, `RenderSplit()`, `splitAtLayerBoundaries()` |
| `server/events/layers/dashboard_updater.go` | `DashboardUpdater` struct, `Update()`, `findDashboardComments()` |
| `server/events/layers/templates/layered_dashboard.tmpl` | Go template for dashboard markdown |
| `server/events/layers/dashboard_renderer_test.go` | Unit tests for rendering |
| `server/events/layers/dashboard_updater_test.go` | Unit tests for sticky comment logic |

### Modified Files

| File | Change |
|------|--------|
| `server/events/vcs/client.go` | Add `ListComments`, `EditComment`, `MaxCommentLength` to interface; add `PullComment` type |
| `server/events/vcs/github/client.go` | Implement `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/gitlab/client.go` | Implement `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/bitbucketcloud/client.go` | Implement `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/bitbucketserver/client.go` | Implement `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/azuredevops/client.go` | Implement `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/gitea/client.go` | Implement `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/not_configured_vcs_client.go` | Stub `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/proxy.go` | Add `ListComments`, `EditComment`, `MaxCommentLength` proxy methods |
| `server/events/vcs/common/instrumented_client.go` | Add instrumented `ListComments`, `EditComment`, `MaxCommentLength` |
| `server/events/vcs/mocks/mock_client.go` | Regenerate (run `go generate`) |

---

## 9. Testing Approach

**This workstream uses test-alongside.** Write implementation and tests together, following existing codebase patterns.

- **Framework:** Standard Go testing with custom assertions from `testing/` package
- **Mocks:** Pegomock for `vcs.Client` (extend existing `server/events/vcs/mocks/mock_client.go`); httptest servers for VCS API tests
- **VCS client tests:** Follow existing patterns in `server/events/vcs/github/client_test.go` — use `httptest.NewServer()` to mock GitHub/GitLab APIs
- **Renderer tests:** Pure logic, no mocks needed — verify template output against expected markdown strings
- **Updater tests:** Use pegomock mock of `vcs.Client` to verify sticky comment lifecycle (find, create, update, clear)

## 10. Test Cases

### 10.1 DashboardRenderer Tests

**File: `server/events/layers/dashboard_renderer_test.go`**

```go
// Test case names and descriptions:

// TestRender_SingleLayerCurrent
// Single layer with mixed statuses, layer marked as current.
// Verify: expanded format, correct icons, correct status text.

// TestRender_CompletedLayerCollapsed
// A fully completed layer (all Applied/NoChanges).
// Verify: collapsed with <details>, summary shows count.

// TestRender_MultipleLayersWithPending
// Three layers: layer 1 completed, layer 2 current, layer 3 pending.
// Verify: layer 1 collapsed, layer 2 expanded, pending count shown.

// TestRender_AllStatusIcons
// One project per status type.
// Verify: each icon matches expected Unicode.

// TestRender_PendingCountZero
// All projects are in layers, none pending.
// Verify: no pending line in output.

// TestRender_SkippedProjectsInSummary
// Layer with mix of applied and skipped projects.
// Verify: summary includes "N skipped".

// TestRender_EmptyLayers
// Edge case: DashboardData with empty Layers slice.
// Verify: renders cleanly without errors, just the sentinel.

// TestRenderSplit_UnderLimit
// Dashboard fits in one comment.
// Verify: returns single-element slice.

// TestRenderSplit_ExceedsLimit
// Dashboard with many layers exceeding maxLen.
// Verify: splits at layer boundaries, each part <= maxLen.
// Verify: first part has DashboardSentinel, subsequent parts have continued sentinel.

// TestRenderSplit_SingleHugeLayer
// One layer with enough projects to exceed maxLen alone.
// Verify: falls back to truncation with ellipsis.

// TestSplitIntoLayerSections
// Unit test for the section-splitting helper.
// Verify: each section starts with "**Layer" except possibly the header.
```

### 9.2 DashboardUpdater Tests

**File: `server/events/layers/dashboard_updater_test.go`**

These tests use the mock VCS client.

```go
// TestUpdate_CreateNew
// No existing dashboard comments. Verify CreateComment called with sentinel body.

// TestUpdate_UpdateExisting
// One existing dashboard comment found. Verify EditComment called with same ID.

// TestUpdate_SplitAndUpdate
// Dashboard splits into 2 parts. One existing comment.
// Verify: first comment updated via EditComment, second created via CreateComment.

// TestUpdate_ShrinkDashboard
// Previously 3 dashboard comments, now only 1 needed.
// Verify: first updated, extras 2 and 3 cleared with "no longer needed" message.

// TestUpdate_EditFails_FallsBackToCreate
// EditComment returns error. Verify CreateComment called as fallback.

// TestUpdate_ListCommentsFails
// ListComments returns error. Verify new comment created anyway (graceful degradation).

// TestIsDashboardComment_Sentinel
// Body containing sentinel -> true.

// TestIsDashboardComment_ContinuedSentinel
// Body containing continued sentinel -> true.

// TestIsDashboardComment_NormalComment
// Body without sentinel -> false.
```

### 9.3 VCS Client Tests (per provider)

For each VCS provider, add tests for the new `ListComments` and `EditComment` methods, following the existing test patterns in each provider's test file. These are primarily API-call verification tests using the existing HTTP mock patterns.

---

## 10. Rendered Output Examples

### 10.1 Active Dashboard (Layer 2 Current)

```markdown
<!-- Atlantis Layered Planning Dashboard -->
## Layered Planning Dashboard

**Layer 1** - 3 applied
<details><summary>Show projects</summary>

- ✅ Applied - vpc
- ✅ Applied - dns
- ✅ Applied - iam-roles

</details>

**Layer 2** (current)
---
- ✅ Applied - security-groups
- 🔶 Planned (changes) - alb
- ⚪ Planned (no changes) - logging
- ❌ Apply failed - networking

---
*45 projects pending*
```

### 10.2 Planning In Progress

```markdown
<!-- Atlantis Layered Planning Dashboard -->
## Layered Planning Dashboard

**Layer 1** (current)
---
- ⏳ Planning... - vpc
- ⏳ Planning... - dns
- ⏳ Planning... - iam-roles

---
*85 projects pending*
```

### 10.3 All Layers Complete

```markdown
<!-- Atlantis Layered Planning Dashboard -->
## Layered Planning Dashboard

**Layer 1** - 3 applied
<details><summary>Show projects</summary>

- ✅ Applied - vpc
- ✅ Applied - dns
- ✅ Applied - iam-roles

</details>

**Layer 2** - 2 applied, 1 no changes
<details><summary>Show projects</summary>

- ✅ Applied - security-groups
- ✅ Applied - alb
- ⚪ Planned (no changes) - logging

</details>

**Layer 3** - 1 applied
<details><summary>Show projects</summary>

- ✅ Applied - app-service

</details>

```

---

## 11. Implementation Order

1. **VCS Interface + PullComment type** -- Add `ListComments`, `EditComment`, `MaxCommentLength` to `vcs.Client` and `PullComment` type. Implement stubs in `NotConfiguredVCSClient`, `ClientProxy`, `InstrumentedClient`.
2. **GitHub implementation** -- Implement the two methods for GitHub (primary VCS for initial use).
3. **Dashboard types** -- Create `server/events/layers/dashboard_types.go`.
4. **Dashboard template** -- Create the `.tmpl` file.
5. **DashboardRenderer** -- Implement rendering and splitting.
6. **DashboardRenderer tests** -- Full test coverage.
7. **DashboardUpdater** -- Implement sticky comment lifecycle.
8. **DashboardUpdater tests** -- Full test coverage with mock VCS.
9. **Other VCS providers** -- Implement `ListComments` / `EditComment` for GitLab, Bitbucket, Azure DevOps, Gitea.
10. **Regenerate mocks** -- Run `go generate` for the VCS client mock.

---

## 12. Risks and Open Questions

### Risks

1. **API rate limits.** `ListComments` fetches all comments on every dashboard update. For PRs with hundreds of comments, this could hit rate limits. **Mitigation:** Cache the dashboard comment ID in the layer state (stored in the DB/lock backend by the orchestrator workstream) so we only need `ListComments` on the first update or when the cached ID fails. The `DashboardUpdater.Update` signature should accept an optional cached comment ID.

2. **Race conditions.** If two Atlantis workers try to update the dashboard simultaneously, one edit may overwrite the other. **Mitigation:** The layered planning orchestrator should serialize dashboard updates per-PR. This is the orchestrator workstream's responsibility.

3. **VCS provider compatibility.** The `<details>` / `<summary>` HTML tags used for collapsed layers are not supported by Bitbucket Cloud/Server. **Mitigation:** The existing `shouldUseWrappedTmpl` pattern already handles this. The dashboard template should have a variant without `<details>` for Bitbucket, or the renderer should accept a `disableMarkdownFolding` flag and render all layers expanded.

4. **Comment ID type mismatch.** GitHub uses `int64` for comment IDs, GitLab uses `int`, Bitbucket uses strings internally. The `PullComment.ID` field uses `int64` which works for GitHub and GitLab (with casting). For Bitbucket Cloud, comment IDs are numeric strings that fit in `int64`. If any provider uses non-numeric IDs, the type would need to change to `string`.

### Open Questions

1. **Should the dashboard comment be the first or last comment on the PR?** Creating it first makes it appear at the top of the conversation (most visible). But if created later, it goes to the bottom. GitHub does not support pinning comments. **Recommendation:** Create the dashboard comment before posting individual plan output, so it appears first chronologically.

2. **Should we delete excess dashboard comments or just clear them?** Deleting is cleaner but some VCS providers don't support comment deletion. Clearing (editing to a minimal body) is universally supported. **Recommendation:** Clear with a minimal body (current design).

3. **Should `MaxCommentLength` be a method on the interface or a standalone function per host type?** A method is cleaner and more extensible. **Recommendation:** Use a method (current design).

4. **Comment ID caching across restarts.** If Atlantis restarts mid-layered-planning, it needs to re-find the dashboard comment. The sentinel-based search handles this. The orchestrator workstream should persist the comment ID(s) in the layer state, but always fall back to sentinel search.

NOT FOUND
