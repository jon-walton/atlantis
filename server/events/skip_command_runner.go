// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
)

// NewSkipCommandRunner creates a new SkipCommandRunner.
func NewSkipCommandRunner(
	vcsClient vcs.Client,
	layerManager *LayerManager,
	commitStatusUpdater CommitStatusUpdater,
	enableLayeredPlanning bool,
	enableLayeredApplySkip bool,
	database db.Database,
) *SkipCommandRunner {
	return &SkipCommandRunner{
		vcsClient:              vcsClient,
		layerManager:           layerManager,
		commitStatusUpdater:    commitStatusUpdater,
		enableLayeredPlanning:  enableLayeredPlanning,
		enableLayeredApplySkip: enableLayeredApplySkip,
		db:                     database,
	}
}

// SkipCommandRunner handles the `atlantis skip` command for skipping
// projects in layered planning.
type SkipCommandRunner struct {
	vcsClient              vcs.Client
	layerManager           *LayerManager
	commitStatusUpdater    CommitStatusUpdater
	enableLayeredPlanning  bool
	enableLayeredApplySkip bool
	db                     db.Database
}

// Run executes the skip command to skip a project in layered planning.
func (s *SkipCommandRunner) Run(ctx *command.Context, cmd *CommentCommand) {
	pull := ctx.Pull

	// Validate that layered planning is enabled
	if !s.enableLayeredPlanning || s.layerManager == nil {
		if err := s.vcsClient.CreateComment(ctx.Log, pull.BaseRepo, pull.Num,
			"**Error:** `skip` requires layered planning to be enabled.",
			command.Skip.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}
		return
	}

	// Validate that skip is enabled
	if !s.enableLayeredApplySkip {
		if err := s.vcsClient.CreateComment(ctx.Log, pull.BaseRepo, pull.Num,
			"**Error:** `skip` command is not enabled. Set `--enable-layered-apply-skip` to enable.",
			command.Skip.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}
		return
	}

	// Get the project name from the command
	projectName := cmd.ProjectName
	if projectName == "" {
		if err := s.vcsClient.CreateComment(ctx.Log, pull.BaseRepo, pull.Num,
			"**Error:** `skip` requires a project name. Usage: `atlantis skip <project>` or `atlantis skip -p <project>`",
			command.Skip.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}
		return
	}

	// Get the current pull status with layer state
	pullStatus, err := s.db.GetPullStatus(pull)
	if err != nil || pullStatus == nil || pullStatus.LayerState == nil {
		if err := s.vcsClient.CreateComment(ctx.Log, pull.BaseRepo, pull.Num,
			"**Error:** No active layered planning state found for this pull request.",
			command.Skip.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}
		return
	}

	// Use the state manager to skip the project
	updatedStatus, err := s.layerManager.stateManager.SkipProject(pullStatus, projectName, s.enableLayeredApplySkip)
	if err != nil {
		if err := s.vcsClient.CreateComment(ctx.Log, pull.BaseRepo, pull.Num,
			fmt.Sprintf("**Error:** Could not skip project `%s`: %s", projectName, err),
			command.Skip.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}
		return
	}

	// Save updated layer state and persist the project's new SkippedPlanStatus
	// so the skip survives a server restart.
	if s.db != nil {
		if err := s.db.UpdateLayerState(pull, updatedStatus.LayerState); err != nil {
			ctx.Log.Err("saving layer state after skip: %s", err)
		}
		// Find the skipped project to get workspace/repoRelDir for persistence
		for _, proj := range updatedStatus.Projects {
			if proj.ProjectName == projectName && proj.Status == models.SkippedPlanStatus {
				if err := s.db.UpdateProjectStatus(pull, proj.Workspace, proj.RepoRelDir, models.SkippedPlanStatus); err != nil {
					ctx.Log.Err("persisting skip status: %s", err)
				}
				break
			}
		}
	}

	// Post confirmation comment
	if err := s.vcsClient.CreateComment(ctx.Log, pull.BaseRepo, pull.Num,
		fmt.Sprintf("Project `%s` has been skipped.", projectName),
		command.Skip.String()); err != nil {
		ctx.Log.Err("unable to comment on pull request: %s", err)
	}

	// Update commit status
	s.updateCommitStatus(ctx, updatedStatus)

	// Update dashboard
	s.layerManager.UpdateDashboard(ctx, updatedStatus)
}

// updateCommitStatus updates the VCS commit status based on the pull status.
func (s *SkipCommandRunner) updateCommitStatus(ctx *command.Context, pullStatus *models.PullStatus) {
	var numSuccess int
	var numErrored int
	status := models.SuccessCommitStatus

	numSuccess = pullStatus.StatusCount(models.AppliedStatus) + pullStatus.StatusCount(models.PlannedNoChangesPlanStatus) + pullStatus.StatusCount(models.SkippedPlanStatus)
	numErrored = pullStatus.StatusCount(models.ErroredApplyStatus)

	if numErrored > 0 {
		status = models.FailedCommitStatus
	} else if numSuccess < len(pullStatus.Projects) {
		// If there are plans that haven't been applied yet, we'll use a pending
		// status.
		status = models.PendingCommitStatus
	}

	if err := s.commitStatusUpdater.UpdateCombinedCount(
		ctx.Log,
		ctx.Pull.BaseRepo,
		ctx.Pull,
		status,
		command.Apply,
		numSuccess,
		len(pullStatus.Projects),
	); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}
}
