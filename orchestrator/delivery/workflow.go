package delivery

import (
	"fmt"
	"strings"
	"time"

	sandbox "github.com/temporal-community/sandbox-orchestration-harness/sdk"
	"github.com/temporal-community/sandbox-orchestration-harness/sdk/compute"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// changedFilesCmd lists uncommitted changes in every git checkout under the
// workspace home, which is where Crafting checkouts land.
const changedFilesCmd = `for d in "$HOME"/*/; do
  [ -d "$d/.git" ] || continue
  echo "## $(basename "$d")"
  git -C "$d" status --short
  git -C "$d" --no-pager diff --stat
done`

func withDefaults(in DeliveryInput) DeliveryInput {
	if in.Template == "" {
		in.Template = DefaultTemplate
	}
	if in.Workspace == "" {
		in.Workspace = DefaultWorkspace
	}
	if in.InterceptPlan == "" {
		in.InterceptPlan = DefaultInterceptPlan
	}
	if strings.EqualFold(in.InterceptPlan, "none") {
		in.InterceptPlan = ""
		in.SkipCluster = true
	}
	if in.MaxAttempts <= 0 {
		in.MaxAttempts = DefaultMaxAttempts
	}
	return in
}

// DeliverChange provisions a sandbox from the template through the
// sandbox-orchestration-harness, then runs implement, local QA, and cluster QA
// as separate agent sessions, sending each failure back to a fresh coder
// session until verification passes or MaxAttempts is reached.
//
// The sandbox outlives the workflow so the change can be reviewed; its
// lifecycle stays in Temporal as the harness SandboxWorkflow named by
// Report.SandboxRef, and stopping that workflow deletes the sandbox.
func DeliverChange(ctx workflow.Context, in DeliveryInput) (Report, error) {
	in = withDefaults(in)
	if strings.TrimSpace(in.Request) == "" {
		return Report{}, temporal.NewNonRetryableApplicationError("request is empty", "InvalidInput", nil)
	}
	logger := workflow.GetLogger(ctx)
	info := workflow.GetInfo(ctx)

	st := Status{
		WorkflowID:    info.WorkflowExecution.ID,
		Phase:         PhaseProvisioning,
		MaxAttempts:   in.MaxAttempts,
		Template:      in.Template,
		Workspace:     in.Workspace,
		InterceptPlan: in.InterceptPlan,
		Steps:         []Step{},
	}
	report := Report{}
	if err := workflow.SetQueryHandler(ctx, StatusQuery, func() (Status, error) { return st, nil }); err != nil {
		return report, err
	}

	// Agent sessions run under runCtx so a cancel signal stops the session in
	// flight. Sandbox lifecycle calls use ctx so they are never cut off midway.
	runCtx, cancelRun := workflow.WithCancel(ctx)
	workflow.Go(ctx, func(gctx workflow.Context) {
		workflow.GetSignalChannel(gctx, CancelSignal).Receive(gctx, nil)
		st.Cancelled = true
		cancelRun()
	})
	workflow.Go(ctx, func(gctx workflow.Context) {
		ch := workflow.GetSignalChannel(gctx, FeedbackSignal)
		for {
			var note string
			ch.Receive(gctx, &note)
			if note = strings.TrimSpace(note); note != "" {
				st.Feedback = append(st.Feedback, note)
			}
		}
	})

	finish := func(outcome Outcome) (Report, error) {
		st.Phase = PhaseDone
		report.Status = st
		report.Outcome = outcome
		return report, nil
	}

	sbx, err := sandbox.NewSandbox(ctx, sandbox.Provider{
		Type: compute.ProviderTypeCrafting,
		Config: map[string]string{
			"template":  in.Template,
			"workspace": in.Workspace,
		},
	},
		sandbox.WithCleanup(sandbox.CleanupDisabled),
		// The harness idle timer only sees its own commands, not the agents
		// working in the sandbox, so it must not suspend it mid-task.
		sandbox.WithIdleTimeout(sandbox.NoIdleTimeout),
	)
	if err != nil {
		return report, fmt.Errorf("provision sandbox from %s: %w", in.Template, err)
	}
	if st.SandboxRef, err = sbx.Ref(); err != nil {
		return report, err
	}
	name, err := sbx.ExecuteCommand(ctx, `printf %s "$SANDBOX_NAME"`)
	if err != nil {
		return report, fmt.Errorf("read sandbox name: %w", err)
	}
	st.Sandbox = strings.TrimSpace(name.Stdout)
	if st.Sandbox == "" {
		return report, temporal.NewNonRetryableApplicationError("sandbox did not report SANDBOX_NAME", "SandboxNameMissing", nil)
	}
	workspace := st.Sandbox + "/" + in.Workspace
	logger.Info("sandbox ready", "sandbox", st.Sandbox)

	if snap, err := sbx.Snapshot(ctx); err != nil {
		logger.Warn("baseline snapshot failed", "error", err)
	} else {
		report.BaselineSnapshot = snap.SnapshotID
	}

	agentCtx := workflow.WithActivityOptions(runCtx, workflow.ActivityOptions{
		StartToCloseTimeout: 90 * time.Minute,
		HeartbeatTimeout:    3 * time.Minute,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    3,
		},
	})
	var a *Activities
	runAgent := func(role string, attempt int, agent, prompt string) (Step, error) {
		step := Step{Role: role, Attempt: attempt, Session: fmt.Sprintf("%s-%s-%d", st.WorkflowID, role, attempt)}
		st.Steps = append(st.Steps, step)
		idx := len(st.Steps) - 1
		var res AgentSessionResult
		err := workflow.ExecuteActivity(agentCtx, a.RunAgentSession, AgentSessionInput{
			Name:      step.Session,
			Agent:     agent,
			Workspace: workspace,
			Prompt:    prompt,
		}).Get(agentCtx, &res)
		if err != nil {
			st.Steps[idx].Reason = err.Error()
			return st.Steps[idx], err
		}
		st.Steps[idx].Report = res.Text
		if role != "implement" {
			st.Steps[idx].Verdict, st.Steps[idx].Reason = ParseVerdict(res.Text)
		}
		return st.Steps[idx], nil
	}

	passed := false
	var lastFailure *Step
	for attempt := 1; attempt <= in.MaxAttempts && !st.Cancelled; attempt++ {
		st.Attempt = attempt

		st.Phase = PhaseImplementing
		impl, err := runAgent("implement", attempt, CoderAgent,
			ImplementPrompt(in, st.Sandbox, attempt, st.Feedback, lastFailure))
		if st.Cancelled {
			break
		}
		if err != nil {
			st.Error = err.Error()
			return finish(OutcomeFailed)
		}

		if !in.SkipLocal {
			st.Phase = PhaseLocalQA
			qa, err := runAgent("qa-local", attempt, QAAgent, LocalQAPrompt(in, st.Sandbox, impl.Report))
			if st.Cancelled {
				break
			}
			if err != nil {
				st.Error = err.Error()
				return finish(OutcomeFailed)
			}
			if qa.Verdict == VerdictFail {
				lastFailure = &qa
				continue
			}
		}

		if !in.SkipCluster && in.InterceptPlan != "" {
			st.Phase = PhaseClusterQA
			qa, err := runAgent("qa-cluster", attempt, QAAgent, ClusterQAPrompt(in, st.Sandbox, impl.Report))
			if st.Cancelled {
				break
			}
			if err != nil {
				st.Error = err.Error()
				return finish(OutcomeFailed)
			}
			if qa.Verdict == VerdictFail {
				lastFailure = &qa
				continue
			}
		}

		passed = true
		break
	}

	st.Phase = PhaseFinishing
	if changed, err := sbx.ExecuteCommand(ctx, changedFilesCmd); err != nil {
		logger.Warn("listing changed files failed", "error", err)
	} else {
		report.ChangedFiles = strings.TrimSpace(changed.Stdout)
	}

	switch {
	case st.Cancelled:
		return finish(OutcomeCancelled)
	case passed:
		if snap, err := sbx.Snapshot(ctx); err != nil {
			logger.Warn("verified snapshot failed", "error", err)
		} else {
			report.VerifiedSnapshot = snap.SnapshotID
		}
		return finish(OutcomePassed)
	default:
		return finish(OutcomeFailed)
	}
}
