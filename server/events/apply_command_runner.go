// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/locking"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
)

func NewApplyCommandRunner(
	vcsClient vcs.Client,
	disableApplyAll bool,
	applyCommandLocker locking.ApplyLockChecker,
	commitStatusUpdater CommitStatusUpdater,
	prjCommandBuilder ProjectApplyCommandBuilder,
	prjCmdRunner ProjectApplyCommandRunner,
	cancellationTracker CancellationTracker,
	autoMerger *AutoMerger,
	pullUpdater *PullUpdater,
	dbUpdater *DBUpdater,
	database db.Database,
	parallelPoolSize int,
	SilenceNoProjects bool,
	silenceVCSStatusNoProjects bool,
	pullReqStatusFetcher vcs.PullReqStatusFetcher,
	layerManager *LayerManager,
	planCommandRunner *PlanCommandRunner,
) *ApplyCommandRunner {
	return &ApplyCommandRunner{
		vcsClient:                  vcsClient,
		DisableApplyAll:            disableApplyAll,
		locker:                     applyCommandLocker,
		commitStatusUpdater:        commitStatusUpdater,
		prjCmdBuilder:              prjCommandBuilder,
		prjCmdRunner:               prjCmdRunner,
		cancellationTracker:        cancellationTracker,
		autoMerger:                 autoMerger,
		pullUpdater:                pullUpdater,
		dbUpdater:                  dbUpdater,
		Database:                   database,
		parallelPoolSize:           parallelPoolSize,
		SilenceNoProjects:          SilenceNoProjects,
		silenceVCSStatusNoProjects: silenceVCSStatusNoProjects,
		pullReqStatusFetcher:       pullReqStatusFetcher,
		layerManager:               layerManager,
		planCommandRunner:          planCommandRunner,
	}
}

type ApplyCommandRunner struct {
	DisableApplyAll      bool
	Database             db.Database
	locker               locking.ApplyLockChecker
	vcsClient            vcs.Client
	commitStatusUpdater  CommitStatusUpdater
	prjCmdBuilder        ProjectApplyCommandBuilder
	prjCmdRunner         ProjectApplyCommandRunner
	cancellationTracker  CancellationTracker
	autoMerger           *AutoMerger
	pullUpdater          *PullUpdater
	dbUpdater            *DBUpdater
	parallelPoolSize     int
	pullReqStatusFetcher vcs.PullReqStatusFetcher
	// SilenceNoProjects is whether Atlantis should respond to PRs if no projects
	// are found
	SilenceNoProjects bool
	// SilenceVCSStatusNoPlans is whether any plan should set commit status if no projects
	// are found
	silenceVCSStatusNoProjects bool
	SilencePRComments          []string
	// layerManager is the lifecycle coordinator for layered planning.
	// Nil when layered planning is not enabled.
	layerManager *LayerManager
	// planCommandRunner is used to trigger next-layer planning after apply completes a layer.
	planCommandRunner *PlanCommandRunner
}

func (a *ApplyCommandRunner) Run(ctx *command.Context, cmd *CommentCommand) {
	var err error
	baseRepo := ctx.Pull.BaseRepo
	pull := ctx.Pull

	locked, err := a.IsLocked()
	// CheckApplyLock falls back to AllowedCommand flag if fetching the lock
	// raises an error
	// We will log failure as warning
	if err != nil {
		ctx.Log.Warn("checking global apply lock: %s", err)
	}

	if locked {
		ctx.Log.Info("ignoring apply command since apply disabled globally")
		if err := a.vcsClient.CreateComment(ctx.Log, baseRepo, pull.Num, applyDisabledComment, command.Apply.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}

		return
	}

	if a.DisableApplyAll && !cmd.IsForSpecificProject() {
		ctx.Log.Info("ignoring apply command without flags since apply all is disabled")
		if err := a.vcsClient.CreateComment(ctx.Log, baseRepo, pull.Num, applyAllDisabledComment, command.Apply.String()); err != nil {
			ctx.Log.Err("unable to comment on pull request: %s", err)
		}

		return
	}

	// Get the mergeable status before we set any build statuses of our own.
	// We do this here because when we set a "Pending" status, if users have
	// required the Atlantis status checks to pass, then we've now changed
	// the mergeability status of the pull request.
	// This sets the approved, mergeable, and sqlocked status in the context.
	ctx.PullRequestStatus, err = a.pullReqStatusFetcher.FetchPullStatus(ctx.Log, pull)
	if err != nil {
		// On error we continue the request with mergeable assumed false.
		// We want to continue because not all apply's will need this status,
		// only if they rely on the mergeability requirement.
		// All PullRequestStatus fields are set to false by default when error.
		ctx.Log.Warn("unable to get pull request status: %s. Continuing with mergeable and approved assumed false", err)
	}

	var projectCmds []command.ProjectContext
	projectCmds, err = a.prjCmdBuilder.BuildApplyCommands(ctx, cmd)
	if err != nil {
		if statusErr := a.commitStatusUpdater.UpdateCombined(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull, models.FailedCommitStatus, cmd.CommandName()); statusErr != nil {
			ctx.Log.Warn("unable to update commit status: %s", statusErr)
		}
		a.pullUpdater.updatePull(ctx, cmd, command.Result{Error: err})
		return
	}

	// If there are no projects to apply, don't respond to the PR and ignore
	if len(projectCmds) == 0 && a.SilenceNoProjects {
		ctx.Log.Info("determined there was no project to run plan in")
		if !a.silenceVCSStatusNoProjects {
			if cmd.IsForSpecificProject() {
				// With a specific apply, just reset the status so it's not stuck in pending state
				pullStatus, err := a.Database.GetPullStatus(pull)
				if err != nil {
					ctx.Log.Warn("unable to fetch pull status: %s", err)
					return
				}
				if pullStatus == nil {
					// default to 0/0
					ctx.Log.Debug("setting VCS status to 0/0 success as no previous state was found")
					if err := a.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Apply, 0, 0); err != nil {
						ctx.Log.Warn("unable to update commit status: %s", err)
					}
					return
				}
				ctx.Log.Debug("resetting VCS status")
				a.updateCommitStatus(ctx, *pullStatus)
			} else {
				// With a generic apply, we set successful commit statuses
				// with 0/0 projects planned successfully because some users require
				// the Atlantis status to be passing for all pull requests.
				// Does not apply to skipped runs for specific projects
				ctx.Log.Debug("setting VCS status to success with no projects found")
				if err := a.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Apply, 0, 0); err != nil {
					ctx.Log.Warn("unable to update commit status: %s", err)
				}
			}
		}
		return
	}

	// Layered planning: load layer state and optionally filter to current layer
	layered := false
	var layerState *models.LayerState
	if a.layerManager != nil {
		existingStatus, fetchErr := a.Database.GetPullStatus(pull)
		if fetchErr == nil && existingStatus != nil && existingStatus.LayerState != nil {
			layerState = existingStatus.LayerState
			if a.layerManager.HasMultipleLayers(layerState) {
				layered = true
				// Only filter to current layer for apply-all; specific project
				// applies target the exact project the user requested but must
				// be validated against the current layer.
				if !cmd.IsForSpecificProject() {
					projectCmds = a.layerManager.FilterToCurrentLayer(projectCmds, layerState)
				} else if len(projectCmds) > 0 && !a.layerManager.IsInCurrentLayer(projectCmds[0], layerState) {
					ctx.Log.Info("rejecting apply for project not in current layer")
					a.vcsClient.CreateComment(
						ctx.Log, baseRepo, pull.Num,
						fmt.Sprintf(
							"**Error:** Project `%s` is in layer %d, but the current layer is %d. "+
								"Apply all projects in the current layer first before applying projects in later layers.",
							projectCmds[0].ProjectName, layerState.ProjectLayers[projectContextKey(projectCmds[0])], layerState.CurrentLayer,
						),
						command.Apply.String(),
					)
					return
				}
				ctx.Log.Info("layered apply: layer %d active (%d projects)", layerState.CurrentLayer, len(projectCmds))
			}
		}
	}

	// Layered planning: show "Applying..." status on the dashboard before
	// running applies so users see progress in real time.
	if layered {
		a.markProjectsApplying(ctx, pull, projectCmds, layerState)
	}

	result := runProjectCmdsWithCancellationTracker(ctx, projectCmds, a.cancellationTracker, a.parallelPoolSize, a.isParallelEnabled(projectCmds), a.prjCmdRunner.Apply)
	ctx.CommandHasErrors = result.HasErrors()

	a.pullUpdater.updatePull(
		ctx,
		cmd,
		result)

	pullStatus, err := a.dbUpdater.updateDB(ctx, pull, result.ProjectResults)
	if err != nil {
		ctx.Log.Err("writing results: %s", err)
		return
	}

	a.updateCommitStatus(ctx, pullStatus)

	// Layered planning: stamp layer assignments (they're not persisted on
	// individual project statuses, only in LayerState.ProjectLayers) and
	// then check if layer is complete and trigger next layer.
	if layered {
		a.layerManager.StampAndSaveLayerState(&pullStatus, layerState)
	}
	if layered && !result.HasErrors() {
		a.handleLayerCompletion(ctx, cmd, &pullStatus, layerState)
	} else if layered {
		// Update dashboard even on error
		a.layerManager.UpdateDashboard(ctx, &pullStatus)
	}

	// Only automerge if all layers are complete (or not layered)
	if a.autoMerger.automergeEnabled(projectCmds) && !cmd.AutoMergeDisabled {
		if layered {
			if a.layerManager.stateManager.IsAllComplete(&pullStatus) {
				a.autoMerger.automerge(ctx, pullStatus, a.autoMerger.deleteSourceBranchOnMergeEnabled(projectCmds), cmd.AutoMergeMethod)
			} else {
				ctx.Log.Info("skipping automerge: not all layers are complete")
			}
		} else {
			a.autoMerger.automerge(ctx, pullStatus, a.autoMerger.deleteSourceBranchOnMergeEnabled(projectCmds), cmd.AutoMergeMethod)
		}
	}
}

// handleLayerCompletion checks if the current layer is done and triggers
// planning for the next layer if so. If the newly planned layer has all
// no-changes projects (PlannedNoChangesPlanStatus or SkippedPlanStatus),
// it auto-advances through successive layers until a layer has actual
// changes or all layers are complete.
func (a *ApplyCommandRunner) handleLayerCompletion(
	ctx *command.Context,
	cmd *CommentCommand,
	pullStatus *models.PullStatus,
	layerState *models.LayerState,
) {
	// Safety bound: never loop more than TotalLayers times to prevent
	// infinite loops from unexpected state.
	maxIterations := layerState.TotalLayers
	if maxIterations < 1 {
		maxIterations = 1
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		canAdvance := a.layerManager.stateManager.CanAdvance(pullStatus)
		if !canAdvance {
			ctx.Log.Debug("current layer not yet complete, waiting for remaining projects")
			a.layerManager.UpdateDashboard(ctx, pullStatus)
			return
		}

		newState, nextProjects, err := a.layerManager.stateManager.AdvanceLayer(pullStatus)
		if err != nil {
			ctx.Log.Err("advancing layer: %s", err)
			a.layerManager.UpdateDashboard(ctx, pullStatus)
			return
		}

		if a.layerManager.stateManager.IsAllComplete(pullStatus) {
			ctx.Log.Info("all layers complete")
			a.layerManager.UpdateDashboard(ctx, pullStatus)
			return
		}

		// Save new layer state
		pullStatus.LayerState = newState
		if a.Database != nil {
			if err := a.Database.UpdateLayerState(ctx.Pull, newState); err != nil {
				ctx.Log.Err("saving layer state after advance: %s", err)
			}
		}

		a.layerManager.UpdateDashboard(ctx, pullStatus)

		// Trigger planning for next layer projects
		if len(nextProjects) == 0 || a.planCommandRunner == nil {
			return
		}

		ctx.Log.Info("triggering planning for next layer (%d projects): %v", len(nextProjects), nextProjects)
		nextCmd := &CommentCommand{
			Name: command.Plan,
		}
		a.planCommandRunner.run(ctx, nextCmd)

		// After cascade planning, re-fetch pull status to check if the
		// newly planned layer is already complete (all no-changes).
		updatedStatus, err := a.Database.GetPullStatus(ctx.Pull)
		if err != nil || updatedStatus == nil {
			ctx.Log.Err("fetching pull status after cascade plan: %s", err)
			return
		}

		// Ensure the layer state is current
		a.layerManager.StampAndSaveLayerState(updatedStatus, newState)
		pullStatus = updatedStatus

		// Check if the newly planned layer is already complete
		// (all projects have terminal status like PlannedNoChangesPlanStatus).
		// If not, we stop and wait for the user to apply.
		complete, _ := a.layerManager.stateManager.IsLayerComplete(pullStatus, newState.CurrentLayer)
		if !complete {
			// Layer has projects that need apply — stop here
			return
		}

		ctx.Log.Info("layer %d has all no-changes projects, auto-advancing", newState.CurrentLayer)
		// Loop continues: advance through this no-changes layer
	}

	ctx.Log.Warn("auto-advance safety bound reached after %d iterations", maxIterations)
	a.layerManager.UpdateDashboard(ctx, pullStatus)
}

// markProjectsApplying updates the DB and dashboard to show "Applying..."
// for projects that are about to be applied.
func (a *ApplyCommandRunner) markProjectsApplying(
	ctx *command.Context,
	pull models.PullRequest,
	projectCmds []command.ProjectContext,
	layerState *models.LayerState,
) {
	// Update each project's status in the DB to ApplyingStatus
	for _, cmd := range projectCmds {
		if err := a.Database.UpdateProjectStatus(pull, cmd.Workspace, cmd.RepoRelDir, models.ApplyingStatus); err != nil {
			ctx.Log.Err("updating project status to applying: %s", err)
		}
	}

	// Reload pull status and update dashboard
	pullStatus, err := a.Database.GetPullStatus(pull)
	if err != nil || pullStatus == nil {
		return
	}
	a.layerManager.StampAndSaveLayerState(pullStatus, layerState)
	a.layerManager.UpdateDashboard(ctx, pullStatus)
}

func (a *ApplyCommandRunner) IsLocked() (bool, error) {
	lock, err := a.locker.CheckApplyLock()

	return lock.Locked, err
}

func (a *ApplyCommandRunner) isParallelEnabled(projectCmds []command.ProjectContext) bool {
	return len(projectCmds) > 0 && projectCmds[0].ParallelApplyEnabled
}

func (a *ApplyCommandRunner) updateCommitStatus(ctx *command.Context, pullStatus models.PullStatus) {
	var numSuccess int
	var numErrored int
	status := models.SuccessCommitStatus

	numSuccess = pullStatus.StatusCount(models.AppliedStatus) + pullStatus.StatusCount(models.PlannedNoChangesPlanStatus)
	numErrored = pullStatus.StatusCount(models.ErroredApplyStatus)

	if numErrored > 0 {
		status = models.FailedCommitStatus
	} else if numSuccess < len(pullStatus.Projects) {
		// If there are plans that haven't been applied yet, we'll use a pending
		// status.
		status = models.PendingCommitStatus
	}

	if err := a.commitStatusUpdater.UpdateCombinedCount(
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

// applyAllDisabledComment is posted when apply all commands (i.e. "atlantis apply")
// are disabled and an apply all command is issued.
var applyAllDisabledComment = "**Error:** Running `atlantis apply` without flags is disabled." +
	" You must specify which project to apply via the `-d <dir>`, `-w <workspace>` or `-p <project name>` flags."

// applyDisabledComment is posted when apply commands are disabled globally and an apply command is issued.
var applyDisabledComment = "**Error:** Running `atlantis apply` is disabled."
