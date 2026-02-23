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

// GenerateLockID creates a consistent lock ID for a project context.
// This ensures the same format is used for both locking and unlocking operations.
func GenerateLockID(projCtx command.ProjectContext) string {
	// Use models.NewProject to ensure consistent path cleaning
	project := models.NewProject(projCtx.BaseRepo.FullName, projCtx.RepoRelDir, "")
	return models.GenerateLockKey(project, projCtx.Workspace)
}

func NewPlanCommandRunner(
	silenceVCSStatusNoPlans bool,
	silenceVCSStatusNoProjects bool,
	vcsClient vcs.Client,
	pendingPlanFinder PendingPlanFinder,
	workingDir WorkingDir,
	commitStatusUpdater CommitStatusUpdater,
	projectCommandBuilder ProjectPlanCommandBuilder,
	projectCommandRunner ProjectPlanCommandRunner,
	cancellationTracker CancellationTracker,
	dbUpdater *DBUpdater,
	pullUpdater *PullUpdater,
	policyCheckCommandRunner *PolicyCheckCommandRunner,
	autoMerger *AutoMerger,
	parallelPoolSize int,
	SilenceNoProjects bool,
	pullStatusFetcher PullStatusFetcher,
	lockingLocker locking.Locker,
	discardApprovalOnPlan bool,
	pullReqStatusFetcher vcs.PullReqStatusFetcher,
	PendingApplyStatus bool,
	layerManager *LayerManager,
	database db.Database,

) *PlanCommandRunner {
	return &PlanCommandRunner{
		silenceVCSStatusNoPlans:    silenceVCSStatusNoPlans,
		silenceVCSStatusNoProjects: silenceVCSStatusNoProjects,
		vcsClient:                  vcsClient,
		pendingPlanFinder:          pendingPlanFinder,
		workingDir:                 workingDir,
		commitStatusUpdater:        commitStatusUpdater,
		prjCmdBuilder:              projectCommandBuilder,
		prjCmdRunner:               projectCommandRunner,
		cancellationTracker:        cancellationTracker,
		dbUpdater:                  dbUpdater,
		pullUpdater:                pullUpdater,
		policyCheckCommandRunner:   policyCheckCommandRunner,
		autoMerger:                 autoMerger,
		parallelPoolSize:           parallelPoolSize,
		SilenceNoProjects:          SilenceNoProjects,
		pullStatusFetcher:          pullStatusFetcher,
		lockingLocker:              lockingLocker,
		DiscardApprovalOnPlan:      discardApprovalOnPlan,
		pullReqStatusFetcher:       pullReqStatusFetcher,
		PendingApplyStatus:         PendingApplyStatus,
		layerManager:               layerManager,
		database:                   database,
	}
}

type PlanCommandRunner struct {
	vcsClient vcs.Client
	// SilenceNoProjects is whether Atlantis should respond to PRs if no projects
	// are found
	SilenceNoProjects bool
	// SilenceVCSStatusNoPlans is whether autoplan should set commit status if no plans
	// are found
	silenceVCSStatusNoPlans bool
	// SilenceVCSStatusNoPlans is whether any plan should set commit status if no projects
	// are found
	silenceVCSStatusNoProjects bool
	commitStatusUpdater        CommitStatusUpdater
	pendingPlanFinder          PendingPlanFinder
	workingDir                 WorkingDir
	prjCmdBuilder              ProjectPlanCommandBuilder
	prjCmdRunner               ProjectPlanCommandRunner
	cancellationTracker        CancellationTracker
	dbUpdater                  *DBUpdater
	pullUpdater                *PullUpdater
	policyCheckCommandRunner   *PolicyCheckCommandRunner
	autoMerger                 *AutoMerger
	parallelPoolSize           int
	pullStatusFetcher          PullStatusFetcher
	lockingLocker              locking.Locker
	// DiscardApprovalOnPlan controls if all already existing approvals should be removed/dismissed before executing
	// a plan.
	DiscardApprovalOnPlan bool
	pullReqStatusFetcher  vcs.PullReqStatusFetcher
	SilencePRComments     []string
	PendingApplyStatus    bool
	// layerManager is the lifecycle coordinator for layered planning.
	// Nil when layered planning is not enabled.
	layerManager *LayerManager
	// database is used for layer state persistence.
	database db.Database
}

func (p *PlanCommandRunner) runAutoplan(ctx *command.Context) {
	baseRepo := ctx.Pull.BaseRepo
	pull := ctx.Pull

	projectCmds, err := p.prjCmdBuilder.BuildAutoplanCommands(ctx)
	if err != nil {
		if statusErr := p.commitStatusUpdater.UpdateCombined(ctx.Log, baseRepo, pull, models.FailedCommitStatus, command.Plan); statusErr != nil {
			ctx.Log.Warn("unable to update commit status: %s", statusErr)
		}
		p.pullUpdater.updatePull(ctx, AutoplanCommand{}, command.Result{Error: err})
		return
	}

	projectCmds, policyCheckCmds := p.partitionProjectCmds(ctx, projectCmds)

	if len(projectCmds) == 0 {
		ctx.Log.Info("determined there was no project to run plan in")
		if !p.silenceVCSStatusNoPlans && !p.silenceVCSStatusNoProjects {
			// If there were no projects modified, we set successful commit statuses
			// with 0/0 projects planned/policy_checked/applied successfully because some users require
			// the Atlantis status to be passing for all pull requests.
			ctx.Log.Debug("setting VCS status to success with no projects found")
			if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Plan, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
			if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.PolicyCheck, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
			if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Apply, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
		} else {
			// When silence is enabled and no projects are found, don't set any status
			ctx.Log.Debug("silence enabled and no projects found - not setting any VCS status")
		}
		return
	}

	// Layered planning gate
	layered := false
	var layerState *models.LayerState
	if p.layerManager != nil && p.layerManager.ShouldActivate(projectCmds) {
		// Check for existing layer state (re-plan on push scenario)
		if p.database != nil {
			existingStatus, fetchErr := p.database.GetPullStatus(pull)
			if fetchErr == nil && existingStatus != nil && existingStatus.LayerState != nil {
				// Existing layered state found -- determine re-plan scope
				ctx.Log.Info("existing layered state found, determining re-plan scope")
				p.handleLayeredReplan(ctx, existingStatus, projectCmds, policyCheckCmds)
				return
			}
		}

		// No existing state -- initialize fresh
		layerState, err = p.layerManager.InitializeLayerState(projectCmds)
		if err != nil {
			ctx.Log.Err("layered planning error: %s", err)
			p.pullUpdater.updatePull(ctx, AutoplanCommand{}, command.Result{
				Error: fmt.Errorf("layered planning error: %w", err),
			})
			return
		}
		if p.layerManager.HasMultipleLayers(layerState) {
			layered = true
			ctx.Log.Info("layered planning activated: %d layers", layerState.TotalLayers)
		}
	}

	if layered {
		// Post the initial dashboard before plan results so it's the first comment
		p.layerManager.PostInitialDashboard(ctx, layerState, projectCmds)

		projectCmds = p.layerManager.FilterToCurrentLayer(projectCmds, layerState)
		policyCheckCmds = p.layerManager.FilterToCurrentLayer(policyCheckCmds, layerState)
	}

	// discard previous plans that might not be relevant anymore
	ctx.Log.Debug("deleting previous plans and locks")
	p.deletePlans(ctx)
	_, err = p.lockingLocker.UnlockByPull(baseRepo.FullName, pull.Num)
	if err != nil {
		ctx.Log.Err("deleting locks: %s", err)
	}

	result := runProjectCmdsWithCancellationTracker(ctx, projectCmds, p.cancellationTracker, p.parallelPoolSize, p.isParallelEnabled(projectCmds), p.prjCmdRunner.Plan)

	if p.autoMerger.automergeEnabled(projectCmds) && result.HasErrors() {
		ctx.Log.Info("deleting plans because there were errors and automerge requires all plans succeed")
		p.deletePlans(ctx)
		_, err := p.lockingLocker.UnlockByPull(ctx.Pull.BaseRepo.FullName, ctx.Pull.Num)
		if err != nil {
			ctx.Log.Err("deleting locks: %s", err)
		}
		result.PlansDeleted = true
	}

	p.pullUpdater.updatePull(ctx, AutoplanCommand{}, result)

	pullStatus, err := p.dbUpdater.updateDB(ctx, ctx.Pull, result.ProjectResults)
	if err != nil {
		ctx.Log.Err("writing results: %s", err)
	}

	// Update layer state and dashboard
	if layered {
		p.layerManager.StampAndSaveLayerState(&pullStatus, layerState)
		if p.database != nil {
			if err := p.database.UpdateLayerState(pull, layerState); err != nil {
				ctx.Log.Err("saving layer state: %s", err)
			}
		}
		p.layerManager.UpdateDashboard(ctx, &pullStatus)
	}

	p.updateCommitStatus(ctx, pullStatus, command.Plan)
	p.updateCommitStatus(ctx, pullStatus, command.Apply)

	// Check if there are any planned projects and if there are any errors or if plans are being deleted
	if len(policyCheckCmds) > 0 &&
		(!result.HasErrors() && !result.PlansDeleted) {
		// Run policy_check command
		ctx.Log.Info("Running policy_checks for all plans")

		// refresh ctx's view of pull status since we just wrote to it.
		// realistically each command should refresh this at the start,
		// however, policy checking is weird since it's called within the plan command itself
		// we need to better structure how this command works.
		ctx.PullStatus = &pullStatus

		p.policyCheckCommandRunner.Run(ctx, policyCheckCmds)
	}
}

func (p *PlanCommandRunner) run(ctx *command.Context, cmd *CommentCommand) {
	var err error
	baseRepo := ctx.Pull.BaseRepo
	pull := ctx.Pull

	ctx.PullRequestStatus, err = p.pullReqStatusFetcher.FetchPullStatus(ctx.Log, pull)
	if err != nil {
		// On error we continue the request with mergeable assumed false.
		// We want to continue because not all apply's will need this status,
		// only if they rely on the mergeability requirement.
		// All PullRequestStatus fields are set to false by default when error.
		ctx.Log.Warn("unable to get pull request status: %s. Continuing with mergeable and approved assumed false", err)
	}

	if p.DiscardApprovalOnPlan {
		if err = p.pullUpdater.VCSClient.DiscardReviews(ctx.Log, baseRepo, pull); err != nil {
			ctx.Log.Err("failed to remove approvals: %s", err)
		}
	}

	projectCmds, err := p.prjCmdBuilder.BuildPlanCommands(ctx, cmd)
	if err != nil {
		if statusErr := p.commitStatusUpdater.UpdateCombined(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull, models.FailedCommitStatus, command.Plan); statusErr != nil {
			ctx.Log.Warn("unable to update commit status: %s", statusErr)
		}
		p.pullUpdater.updatePull(ctx, cmd, command.Result{Error: err})
		return
	}

	if len(projectCmds) == 0 && p.SilenceNoProjects {
		ctx.Log.Info("determined there was no project to run plan in")
		if !p.silenceVCSStatusNoProjects {
			if cmd.IsForSpecificProject() {
				// With a specific plan, just reset the status so it's not stuck in pending state
				pullStatus, err := p.pullStatusFetcher.GetPullStatus(pull)
				if err != nil {
					ctx.Log.Warn("unable to fetch pull status: %s", err)
					return
				}
				if pullStatus == nil {
					// default to 0/0
					ctx.Log.Debug("setting VCS status to 0/0 success as no previous state was found")
					if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Plan, 0, 0); err != nil {
						ctx.Log.Warn("unable to update commit status: %s", err)
					}
					return
				}
				ctx.Log.Debug("resetting VCS status")
				p.updateCommitStatus(ctx, *pullStatus, command.Plan)
			} else {
				// With a generic plan, we set successful commit statuses
				// with 0/0 projects planned successfully because some users require
				// the Atlantis status to be passing for all pull requests.
				// Does not apply to skipped runs for specific projects
				ctx.Log.Debug("setting VCS status to success with no projects found")
				if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Plan, 0, 0); err != nil {
					ctx.Log.Warn("unable to update commit status: %s", err)
				}
				if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.PolicyCheck, 0, 0); err != nil {
					ctx.Log.Warn("unable to update commit status: %s", err)
				}
				if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.Apply, 0, 0); err != nil {
					ctx.Log.Warn("unable to update commit status: %s", err)
				}
			}
		} else {
			// When silence is enabled and no projects are found, don't set any status
			ctx.Log.Debug("silence enabled and no projects found - not setting any VCS status")
		}
		return
	}

	projectCmds, policyCheckCmds := p.partitionProjectCmds(ctx, projectCmds)

	// Layered planning gate for manual plan (non-specific project)
	layered := false
	var layerState *models.LayerState
	layerStateIsNew := false
	if !cmd.IsForSpecificProject() && p.layerManager != nil && p.layerManager.ShouldActivate(projectCmds) {
		// Check if there's existing layer state (re-plan or cascade scenario)
		if p.database != nil {
			existingStatus, fetchErr := p.database.GetPullStatus(pull)
			if fetchErr == nil && existingStatus != nil && existingStatus.LayerState != nil {
				layerState = existingStatus.LayerState
			}
		}
		if layerState == nil {
			layerState, err = p.layerManager.InitializeLayerState(projectCmds)
			if err != nil {
				ctx.Log.Err("layered planning error: %s", err)
				p.pullUpdater.updatePull(ctx, cmd, command.Result{
					Error: fmt.Errorf("layered planning error: %w", err),
				})
				return
			}
			layerStateIsNew = true
		}
		if p.layerManager.HasMultipleLayers(layerState) {
			layered = true

			// Only post the initial dashboard when first creating layer state.
			// During cascade planning (existing state loaded from DB), the
			// dashboard was already updated by handleLayerCompletion with the
			// full project list. Calling PostInitialDashboard here would
			// overwrite it with a synthetic pullStatus containing only the
			// current layer's projects, hiding previous layers.
			if layerStateIsNew {
				p.layerManager.PostInitialDashboard(ctx, layerState, projectCmds)
			}

			projectCmds = p.layerManager.FilterToCurrentLayer(projectCmds, layerState)
			policyCheckCmds = p.layerManager.FilterToCurrentLayer(policyCheckCmds, layerState)
		}
	}

	// if the plan is generic, new plans will be generated based on changes
	// discard previous plans that might not be relevant anymore
	if !cmd.IsForSpecificProject() {
		ctx.Log.Debug("deleting previous plans and locks")
		p.deletePlans(ctx)
		_, err := p.lockingLocker.UnlockByPull(ctx.Pull.BaseRepo.FullName, ctx.Pull.Num)
		if err != nil {
			ctx.Log.Err("deleting locks: %s", err)
		}
	}

	result := runProjectCmdsWithCancellationTracker(ctx, projectCmds, p.cancellationTracker, p.parallelPoolSize, p.isParallelEnabled(projectCmds), p.prjCmdRunner.Plan)
	ctx.CommandHasErrors = result.HasErrors()

	if p.autoMerger.automergeEnabled(projectCmds) && result.HasErrors() {
		ctx.Log.Info("deleting plans because there were errors and automerge requires all plans succeed")
		p.deletePlans(ctx)
		_, err := p.lockingLocker.UnlockByPull(ctx.Pull.BaseRepo.FullName, ctx.Pull.Num)
		if err != nil {
			ctx.Log.Err("deleting locks: %s", err)
		}
		result.PlansDeleted = true
	}

	p.pullUpdater.updatePull(
		ctx,
		cmd,
		result)

	pullStatus, err := p.dbUpdater.updateDB(ctx, pull, result.ProjectResults)
	if err != nil {
		ctx.Log.Err("writing results: %s", err)
		return
	}

	// Update layer state and dashboard
	if layered {
		p.layerManager.StampAndSaveLayerState(&pullStatus, layerState)
		if p.database != nil {
			if err := p.database.UpdateLayerState(pull, layerState); err != nil {
				ctx.Log.Err("saving layer state: %s", err)
			}
		}
		p.layerManager.UpdateDashboard(ctx, &pullStatus)
	}

	p.updateCommitStatus(ctx, pullStatus, command.Plan)
	p.updateCommitStatus(ctx, pullStatus, command.Apply)

	// Runs policy checks step after all plans are successful.
	// This step does not approve any policies that require approval.
	if len(result.ProjectResults) > 0 &&
		(!result.HasErrors() && !result.PlansDeleted) {
		ctx.Log.Info("Running policy check for '%s'", cmd.CommandName())
		p.policyCheckCommandRunner.Run(ctx, policyCheckCmds)
	} else if len(projectCmds) == 0 && !cmd.IsForSpecificProject() {
		// If there were no projects modified, we set successful commit statuses
		// with 0/0 projects planned/policy_checked/applied successfully because some users require
		// the Atlantis status to be passing for all pull requests.
		ctx.Log.Debug("setting VCS status to success with no projects found")
		if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.PolicyCheck, 0, 0); err != nil {
			ctx.Log.Warn("unable to update commit status: %s", err)
		}
	}
}

// handleLayeredReplan orchestrates the re-plan flow when a push arrives on a PR
// with existing layered planning state. It calls HandleNewCommit to classify
// affected projects and then either:
//   - Does nothing (all affected projects are in future layers)
//   - Re-plans only affected projects in the current layer
//   - Resets layer state and re-plans from the reset layer
func (p *PlanCommandRunner) handleLayeredReplan(
	ctx *command.Context,
	existingStatus *models.PullStatus,
	allProjectCmds []command.ProjectContext,
	allPolicyCheckCmds []command.ProjectContext,
) {
	pull := ctx.Pull
	layerState := existingStatus.LayerState

	// Build the list of affected project names from the autoplan commands
	affectedProjects := make([]string, 0, len(allProjectCmds))
	for _, cmd := range allProjectCmds {
		affectedProjects = append(affectedProjects, projectContextKey(cmd))
	}

	// Determine what action to take
	action := p.layerManager.stateManager.HandleNewCommit(existingStatus, affectedProjects)

	if action.NoAction {
		ctx.Log.Info("push affects only future-layer projects; no re-plan needed")
		return
	}

	var projectCmds []command.ProjectContext
	var policyCheckCmds []command.ProjectContext

	if action.ResetToLayer >= 0 {
		// Reset layer state and re-plan from the reset layer
		ctx.Log.Info("resetting layer state to layer %d", action.ResetToLayer)

		if err := p.layerManager.stateManager.ResetLayerState(layerState, action.ResetToLayer); err != nil {
			ctx.Log.Err("resetting layer state: %s", err)
			p.pullUpdater.updatePull(ctx, AutoplanCommand{}, command.Result{
				Error: fmt.Errorf("resetting layer state: %w", err),
			})
			return
		}

		// Re-initialize the layer state from the reset point.
		// This re-computes layer assignments for the pending projects.
		freshState, err := p.layerManager.InitializeLayerState(allProjectCmds)
		if err != nil {
			ctx.Log.Err("re-initializing layer state after reset: %s", err)
			p.pullUpdater.updatePull(ctx, AutoplanCommand{}, command.Result{
				Error: fmt.Errorf("re-initializing layer state after reset: %w", err),
			})
			return
		}
		layerState = freshState

		// Filter to current layer (the reset-to layer)
		projectCmds = p.layerManager.FilterToCurrentLayer(allProjectCmds, layerState)
		policyCheckCmds = p.layerManager.FilterToCurrentLayer(allPolicyCheckCmds, layerState)

		ctx.Log.Info("re-planning layer %d (%d projects)", layerState.CurrentLayer, len(projectCmds))
	} else {
		// Re-plan only affected projects in the current layer
		replanSet := make(map[string]bool, len(action.ReplanProjects))
		for _, name := range action.ReplanProjects {
			replanSet[name] = true
		}

		for _, cmd := range allProjectCmds {
			if replanSet[projectContextKey(cmd)] {
				projectCmds = append(projectCmds, cmd)
			}
		}
		for _, cmd := range allPolicyCheckCmds {
			if replanSet[projectContextKey(cmd)] {
				policyCheckCmds = append(policyCheckCmds, cmd)
			}
		}

		ctx.Log.Info("re-planning %d projects in current layer %d", len(projectCmds), layerState.CurrentLayer)
	}

	if len(projectCmds) == 0 {
		ctx.Log.Info("no projects to re-plan after filtering")
		return
	}

	// Delete previous plans and locks for this pull
	ctx.Log.Debug("deleting previous plans and locks for re-plan")
	p.deletePlans(ctx)
	_, err := p.lockingLocker.UnlockByPull(pull.BaseRepo.FullName, pull.Num)
	if err != nil {
		ctx.Log.Err("deleting locks: %s", err)
	}

	// Run the plans
	result := runProjectCmdsWithCancellationTracker(ctx, projectCmds, p.cancellationTracker, p.parallelPoolSize, p.isParallelEnabled(projectCmds), p.prjCmdRunner.Plan)

	if p.autoMerger.automergeEnabled(projectCmds) && result.HasErrors() {
		ctx.Log.Info("deleting plans because there were errors and automerge requires all plans succeed")
		p.deletePlans(ctx)
		_, err := p.lockingLocker.UnlockByPull(pull.BaseRepo.FullName, pull.Num)
		if err != nil {
			ctx.Log.Err("deleting locks: %s", err)
		}
		result.PlansDeleted = true
	}

	p.pullUpdater.updatePull(ctx, AutoplanCommand{}, result)

	pullStatus, err := p.dbUpdater.updateDB(ctx, pull, result.ProjectResults)
	if err != nil {
		ctx.Log.Err("writing results: %s", err)
	}

	// Update layer state and dashboard
	p.layerManager.StampAndSaveLayerState(&pullStatus, layerState)
	if p.database != nil {
		if err := p.database.UpdateLayerState(pull, layerState); err != nil {
			ctx.Log.Err("saving layer state: %s", err)
		}
	}
	p.layerManager.UpdateDashboard(ctx, &pullStatus)

	p.updateCommitStatus(ctx, pullStatus, command.Plan)
	p.updateCommitStatus(ctx, pullStatus, command.Apply)

	// Run policy checks if plans succeeded
	if len(policyCheckCmds) > 0 && !result.HasErrors() && !result.PlansDeleted {
		ctx.Log.Info("running policy_checks for re-planned projects")
		ctx.PullStatus = &pullStatus
		p.policyCheckCommandRunner.Run(ctx, policyCheckCmds)
	}
}

func (p *PlanCommandRunner) Run(ctx *command.Context, cmd *CommentCommand) {
	if ctx.Trigger == command.AutoTrigger {
		p.runAutoplan(ctx)
	} else {
		p.run(ctx, cmd)
	}
}

func (p *PlanCommandRunner) updateCommitStatus(ctx *command.Context, pullStatus models.PullStatus, commandName command.Name) {
	var numSuccess int
	var numErrored int
	status := models.SuccessCommitStatus

	switch commandName {
	case command.Plan:
		numErrored = pullStatus.StatusCount(models.ErroredPlanStatus)
		// We consider anything that isn't a plan error as a plan success.
		// For example, if there is an apply error, that means that at least a
		// plan was generated successfully.
		numSuccess = len(pullStatus.Projects) - numErrored

		if numErrored > 0 {
			status = models.FailedCommitStatus
		}
	case command.Apply:
		numSuccess = pullStatus.StatusCount(models.AppliedStatus) + pullStatus.StatusCount(models.PlannedNoChangesPlanStatus)
		numErrored = pullStatus.StatusCount(models.ErroredApplyStatus)

		if numErrored > 0 {
			status = models.FailedCommitStatus
		} else if numSuccess < len(pullStatus.Projects) {
			// When there are planned changes that haven't been applied yet:
			// - GitLab: Set status to pending if PendingApplyStatus is enabled
			//           This prevents MR merging until all applies complete
			// - Other VCS: Leave status unchanged (existing behavior)
			if ctx.Pull.BaseRepo.VCSHost.Type == models.Gitlab && p.PendingApplyStatus {
				ctx.Log.Debug("Pending Apply Status is set. Pipeline status will be marked as pending since there are changes to apply")
				status = models.PendingCommitStatus
			} else {
				if p.PendingApplyStatus {
					// If a VCS uses this flag other than Gitlab, we log the warning to the user
					ctx.Log.Warn("Flag --pending-apply-status is not yet supported by your VCS. Pipeline status will not be marked as pending")
				}
				// Otherwise, status remains SuccessCommitStatus (no update needed)
				return
			}
		}
	}

	if err := p.commitStatusUpdater.UpdateCombinedCount(
		ctx.Log,
		ctx.Pull.BaseRepo,
		ctx.Pull,
		status,
		commandName,
		numSuccess,
		len(pullStatus.Projects),
	); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}
}

// deletePlans deletes all plans generated in this ctx.
func (p *PlanCommandRunner) deletePlans(ctx *command.Context) {
	pullDir, err := p.workingDir.GetPullDir(ctx.Pull.BaseRepo, ctx.Pull)
	if err != nil {
		ctx.Log.Err("getting pull dir: %s", err)
	}
	if err := p.pendingPlanFinder.DeletePlans(pullDir); err != nil {
		ctx.Log.Err("deleting pending plans: %s", err)
	}
}

func (p *PlanCommandRunner) partitionProjectCmds(
	ctx *command.Context,
	cmds []command.ProjectContext,
) (
	projectCmds []command.ProjectContext,
	policyCheckCmds []command.ProjectContext,
) {
	for _, cmd := range cmds {
		switch cmd.CommandName {
		case command.Plan:
			projectCmds = append(projectCmds, cmd)
		case command.PolicyCheck:
			policyCheckCmds = append(policyCheckCmds, cmd)
		default:
			ctx.Log.Err("%s is not supported", cmd.CommandName)
		}
	}
	return
}

func (p *PlanCommandRunner) isParallelEnabled(projectCmds []command.ProjectContext) bool {
	return len(projectCmds) > 0 && projectCmds[0].ParallelPlanEnabled
}
