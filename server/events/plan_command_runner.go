package events

import (
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
)

func NewPlanCommandRunner(
	silenceVCSStatusNoPlans bool,
	silenceVCSStatusNoProjects bool,
	vcsClient vcs.Client,
	pendingPlanFinder PendingPlanFinder,
	workingDir WorkingDir,
	commitStatusUpdater CommitStatusUpdater,
	projectCommandBuilder ProjectPlanCommandBuilder,
	projectCommandRunner ProjectPlanCommandRunner,
	dbUpdater *DBUpdater,
	pullUpdater *PullUpdater,
	policyCheckCommandRunner *PolicyCheckCommandRunner,
	autoMerger *AutoMerger,
	parallelPoolSize int,
	SilenceNoProjects bool,
	pullStatusFetcher PullStatusFetcher,
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
		dbUpdater:                  dbUpdater,
		pullUpdater:                pullUpdater,
		policyCheckCommandRunner:   policyCheckCommandRunner,
		autoMerger:                 autoMerger,
		parallelPoolSize:           parallelPoolSize,
		SilenceNoProjects:          SilenceNoProjects,
		pullStatusFetcher:          pullStatusFetcher,
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
	dbUpdater                  *DBUpdater
	pullUpdater                *PullUpdater
	policyCheckCommandRunner   *PolicyCheckCommandRunner
	autoMerger                 *AutoMerger
	parallelPoolSize           int
	pullStatusFetcher          PullStatusFetcher
}

func (p *PlanCommandRunner) runAutoplan(ctx *CommandContext) {
	baseRepo := ctx.Pull.BaseRepo
	pull := ctx.Pull

	projectCmds, err := p.prjCmdBuilder.BuildAutoplanCommands(ctx)
	if err != nil {
		if statusErr := p.commitStatusUpdater.UpdateCombined(baseRepo, pull, models.FailedCommitStatus, models.PlanCommand); statusErr != nil {
			ctx.Log.Warn("unable to update commit status: %s", statusErr)
		}
		p.pullUpdater.updatePull(ctx, AutoplanCommand{}, CommandResult{Error: err})
		return
	}

	projectCmds, policyCheckCmds := p.partitionProjectCmds(ctx, projectCmds)

	if len(projectCmds) == 0 {
		ctx.Log.Info("determined there was no project to run plan in")
		if !(p.silenceVCSStatusNoPlans || p.silenceVCSStatusNoProjects) {
			// If there were no projects modified, we set successful commit statuses
			// with 0/0 projects planned/policy_checked/applied successfully because some users require
			// the Atlantis status to be passing for all pull requests.
			ctx.Log.Debug("setting VCS status to success with no projects found")
			if err := p.commitStatusUpdater.UpdateCombinedCount(baseRepo, pull, models.SuccessCommitStatus, models.PlanCommand, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
			if err := p.commitStatusUpdater.UpdateCombinedCount(baseRepo, pull, models.SuccessCommitStatus, models.PolicyCheckCommand, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
			if err := p.commitStatusUpdater.UpdateCombinedCount(baseRepo, pull, models.SuccessCommitStatus, models.ApplyCommand, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
		}
		return
	}

	// At this point we are sure Atlantis has work to do, so set commit status to pending
	if err := p.commitStatusUpdater.UpdateCombined(ctx.Pull.BaseRepo, ctx.Pull, models.PendingCommitStatus, models.PlanCommand); err != nil {
		ctx.Log.Warn("unable to update plan commit status: %s", err)
	}
	if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Pull.BaseRepo, ctx.Pull, models.PendingCommitStatus, models.ApplyCommand, 0, len(projectCmds)); err != nil {
		ctx.Log.Warn("unable to update apply commit status: %s", err)
	}

	result := p.runProjectCmds(ctx, projectCmds)
	if p.autoMerger.automergeEnabled(projectCmds) && result.HasErrors() {
		ctx.Log.Info("deleting plans because there were errors and automerge requires all plans succeed")
		p.deletePlans(ctx)
		result.PlansDeleted = true
	}

	p.pullUpdater.updatePull(ctx, AutoplanCommand{}, result)

	pullStatus, err := p.dbUpdater.updateDB(ctx, ctx.Pull, result.ProjectResults)
	if err != nil {
		ctx.Log.Err("writing results: %s", err)
	}

	p.updateCommitStatus(ctx, pullStatus)

	// Check if there are any planned projects and if there are any errors or if plans are being deleted
	if len(policyCheckCmds) > 0 &&
		!(result.HasErrors() || result.PlansDeleted) {
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

func (p *PlanCommandRunner) run(ctx *CommandContext, cmd *CommentCommand) {
	var err error
	baseRepo := ctx.Pull.BaseRepo
	pull := ctx.Pull

	if err = p.commitStatusUpdater.UpdateCombined(baseRepo, pull, models.PendingCommitStatus, models.PlanCommand); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}

	projectCmds, err := p.prjCmdBuilder.BuildPlanCommands(ctx, cmd)
	if err != nil {
		if statusErr := p.commitStatusUpdater.UpdateCombined(ctx.Pull.BaseRepo, ctx.Pull, models.FailedCommitStatus, models.PlanCommand); statusErr != nil {
			ctx.Log.Warn("unable to update commit status: %s", statusErr)
		}
		p.pullUpdater.updatePull(ctx, cmd, CommandResult{Error: err})
		return
	}

	if len(projectCmds) == 0 && p.SilenceNoProjects {
		ctx.Log.Info("determined there was no project to run plan in")
		if !p.silenceVCSStatusNoProjects {
			// If there were no projects modified, we set successful commit statuses
			// with 0/0 projects planned successfully because some users require
			// the Atlantis status to be passing for all pull requests.
			ctx.Log.Debug("setting VCS status to success with no projects found")
			if err := p.commitStatusUpdater.UpdateCombinedCount(baseRepo, pull, models.SuccessCommitStatus, models.PlanCommand, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
		}
		return
	}

	projectCmds, policyCheckCmds := p.partitionProjectCmds(ctx, projectCmds)

	// At this point we are sure Atlantis has work to do, so set commit status to pending
	if err := p.commitStatusUpdater.UpdateCombined(ctx.Pull.BaseRepo, ctx.Pull, models.PendingCommitStatus, models.PlanCommand); err != nil {
		ctx.Log.Warn("unable to update plan commit status: %s", err)
	}
	if err := p.commitStatusUpdater.UpdateCombinedCount(ctx.Pull.BaseRepo, ctx.Pull, models.PendingCommitStatus, models.ApplyCommand, 0, len(projectCmds)); err != nil {
		ctx.Log.Warn("unable to update apply commit status: %s", err)
	}

	result := p.runProjectCmds(ctx, projectCmds)
	if p.autoMerger.automergeEnabled(projectCmds) && result.HasErrors() {
		ctx.Log.Info("deleting plans because there were errors and automerge requires all plans succeed")
		p.deletePlans(ctx)
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

	p.updateCommitStatus(ctx, pullStatus)

	// Runs policy checks step after all plans are successful.
	// This step does not approve any policies that require approval.
	if len(result.ProjectResults) > 0 &&
		!(result.HasErrors() || result.PlansDeleted) {
		ctx.Log.Info("Running policy check for %s", cmd.String())
		p.policyCheckCommandRunner.Run(ctx, policyCheckCmds)
	}
}

func (p *PlanCommandRunner) runProjectCmds(ctx *CommandContext, projectCmds []*models.ProjectCommandContext) CommandResult {
	var result CommandResult
	groups := p.prjCmdBuilder.GroupProjectCmdsByDependency(projectCmds)

	for i, groupCmds := range groups {
		// Always plan all projects in case a new commit is
		// made which changes a dependency
		var groupResult CommandResult
		ctx.Log.Info("Running plans for group %d", i)
		if p.isParallelEnabled(groupCmds) {
			ctx.Log.Debug("Running plans in parallel")
			groupResult = runProjectCmdsParallel(groupCmds, p.prjCmdRunner.Plan, p.parallelPoolSize)
		} else {
			ctx.Log.Debug("Running plans sequentially")
			groupResult = runProjectCmds(groupCmds, p.prjCmdRunner.Plan)
		}
		//for r, res := range groupResult.ProjectResults {
		//	groups[i][r].ProjectPlanStatus = res.PlanStatus()
		//}
		result = result.Merge(groupResult)
	}

	return result
}

func (p *PlanCommandRunner) Run(ctx *CommandContext, cmd *CommentCommand) {
	if ctx.Trigger == Auto {
		p.runAutoplan(ctx)
	} else {
		p.run(ctx, cmd)
	}
}

func (p *PlanCommandRunner) updateCommitStatus(ctx *CommandContext, pullStatus models.PullStatus) {
	var numSuccess int
	var numErrored int
	status := models.SuccessCommitStatus

	numErrored = pullStatus.StatusCount(models.ErroredPlanStatus)
	// We consider anything that isn't a plan error as a plan success.
	// For example, if there is an apply error, that means that at least a
	// plan was generated successfully.
	numSuccess = len(pullStatus.Projects) - numErrored

	if numErrored > 0 {
		status = models.FailedCommitStatus
	}

	if err := p.commitStatusUpdater.UpdateCombinedCount(
		ctx.Pull.BaseRepo,
		ctx.Pull,
		status,
		models.PlanCommand,
		numSuccess,
		len(pullStatus.Projects),
	); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}
}

// deletePlans deletes all plans generated in this ctx.
func (p *PlanCommandRunner) deletePlans(ctx *CommandContext) {
	pullDir, err := p.workingDir.GetPullDir(ctx.Pull.BaseRepo, ctx.Pull)
	if err != nil {
		ctx.Log.Err("getting pull dir: %s", err)
	}
	if err := p.pendingPlanFinder.DeletePlans(pullDir); err != nil {
		ctx.Log.Err("deleting pending plans: %s", err)
	}
}

func (p *PlanCommandRunner) partitionProjectCmds(
	ctx *CommandContext,
	cmds []*models.ProjectCommandContext,
) (
	projectCmds []*models.ProjectCommandContext,
	policyCheckCmds []*models.ProjectCommandContext,
) {
	for i, _ := range cmds {
		switch cmds[i].CommandName {
		case models.PlanCommand:
			projectCmds = append(projectCmds, cmds[i])
		case models.PolicyCheckCommand:
			policyCheckCmds = append(policyCheckCmds, cmds[i])
		default:
			ctx.Log.Err("%s is not supported", cmds[i].CommandName)
		}
	}
	return
}

func (p *PlanCommandRunner) isParallelEnabled(projectCmds []*models.ProjectCommandContext) bool {
	return len(projectCmds) > 0 && projectCmds[0].ParallelPlanEnabled
}
