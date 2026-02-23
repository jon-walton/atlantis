package layers

import (
	"fmt"
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
func NewDashboardUpdater(client vcs.Client, markdownTemplateOverridesDir string) *DashboardUpdater {
	return &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(markdownTemplateOverridesDir),
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

	existing, err := u.findDashboardComments(logger, repo, pullNum)
	if err != nil {
		logger.Warn("failed to find existing dashboard comments, will create new: %s", err)
		existing = nil
	}

	for i, body := range bodies {
		if i < len(existing) {
			logger.Debug("Updating dashboard comment %d (ID: %d)", i+1, existing[i].ID)
			if err := u.VCSClient.EditComment(logger, repo, pullNum, existing[i].ID, body); err != nil {
				logger.Err("failed to update dashboard comment %d: %s", existing[i].ID, err)
				if err := u.VCSClient.CreateComment(logger, repo, pullNum, body, ""); err != nil {
					return err
				}
			}
		} else {
			logger.Debug("Creating new dashboard comment (part %d)", i+1)
			if err := u.VCSClient.CreateComment(logger, repo, pullNum, body, ""); err != nil {
				return err
			}
		}
	}

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
