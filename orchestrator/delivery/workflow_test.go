package delivery

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	sandbox "github.com/temporal-community/sandbox-orchestration-harness/sdk"
	"github.com/temporal-community/sandbox-orchestration-harness/sdk/compute"
	wfIface "github.com/temporal-community/sandbox-orchestration-harness/sdk/workflow"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

const testSandbox = "tsp-test0001"

// agentScript answers RunAgentSession calls by session role and attempt, and
// records every request so tests can inspect the prompts.
type agentScript struct {
	mu       sync.Mutex
	calls    []AgentSessionInput
	verdicts map[string]string // "qa-local-1" -> "PASS"
}

func (s *agentScript) run(_ context.Context, in AgentSessionInput) (AgentSessionResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, in)
	s.mu.Unlock()
	key := strings.TrimPrefix(in.Name, "default-test-workflow-id-")
	if strings.HasPrefix(key, "implement-") {
		return AgentSessionResult{Name: in.Name, Text: "Changed services/src/checkoutservice/main.go and restarted checkoutservice."}, nil
	}
	v, ok := s.verdicts[key]
	if !ok {
		v = "PASS"
	}
	text := "Ran place-test-order; shipping charged as required.\n\nVERDICT: " + v
	if v == "" {
		text = "I could not finish the check."
	}
	return AgentSessionResult{Name: in.Name, Text: text}, nil
}

func (s *agentScript) prompt(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.HasSuffix(c.Name, name) {
			return c.Prompt
		}
	}
	return ""
}

func newEnv(t *testing.T, script *agentScript) *testsuite.TestWorkflowEnvironment {
	return newEnvWithAgentDelay(t, script, 0)
}

// newEnvWithAgentDelay makes every agent session take delay of workflow time.
func newEnvWithAgentDelay(t *testing.T, script *agentScript, delay time.Duration) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(DeliverChange, workflow.RegisterOptions{Name: WorkflowName})
	env.RegisterActivity(&Activities{})
	sandbox.Register(env, nil)

	// The harness runs the sandbox lifecycle as a child workflow driven by
	// update-sending activities; stand in for both.
	env.OnWorkflow(wfIface.SandboxWorkflow, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity("SendSandboxInit", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in sandbox.SendSandboxInitInput) error {
			require.Equal(t, compute.ProviderTypeCrafting, in.ComputeProvider.Type)
			require.Equal(t, compute.NoIdleTimeout, in.IdleTimeout)
			return nil
		})
	env.OnActivity("SendSandboxExecuteCommand", mock.Anything, mock.Anything).
		Return(func(_ context.Context, in sandbox.SendSandboxExecuteCommandInput) (compute.CommandResult, error) {
			if strings.Contains(in.Command, "SANDBOX_NAME") {
				return compute.CommandResult{Stdout: testSandbox}, nil
			}
			return compute.CommandResult{Stdout: "## boutique\n M services/src/checkoutservice/main.go\n"}, nil
		})
	snaps := 0
	env.OnActivity("SendSandboxSnapshot", mock.Anything, mock.Anything).
		Return(func(context.Context, sandbox.SendSandboxSnapshotInput) (*compute.ProviderSnapshot, error) {
			snaps++
			return &compute.ProviderSnapshot{SnapshotID: `{"v":1,"ws":"app","home":"snap-` + string(rune('0'+snaps)) + `"}`}, nil
		})
	env.OnActivity("RunAgentSession", mock.Anything, mock.Anything).After(delay).Return(script.run)
	return env
}

func runDelivery(t *testing.T, env *testsuite.TestWorkflowEnvironment, in DeliveryInput) Report {
	t.Helper()
	env.ExecuteWorkflow(WorkflowName, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var r Report
	require.NoError(t, env.GetWorkflowResult(&r))
	return r
}

func roles(r Report) []string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Role+"-"+string(rune('0'+s.Attempt)))
	}
	return out
}

func TestFirstTryPass(t *testing.T) {
	script := &agentScript{}
	env := newEnv(t, script)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free for orders over $100."})

	require.Equal(t, OutcomePassed, r.Outcome)
	require.Equal(t, testSandbox, r.Sandbox)
	require.Equal(t, []string{"implement-1", "qa-local-1", "qa-cluster-1"}, roles(r))
	require.Equal(t, VerdictPass, r.Steps[1].Verdict)
	require.Equal(t, VerdictPass, r.Steps[2].Verdict)
	require.NotEmpty(t, r.BaselineSnapshot)
	require.NotEmpty(t, r.VerifiedSnapshot)
	require.NotEmpty(t, r.SandboxRef)
	require.Contains(t, r.ChangedFiles, "checkoutservice")

	require.Len(t, script.calls, 3)
	for _, c := range script.calls {
		require.Equal(t, testSandbox+"/app", c.Workspace)
	}
	require.Equal(t, CoderAgent, script.calls[0].Agent)
	require.Equal(t, QAAgent, script.calls[1].Agent)
	require.NotContains(t, script.prompt("implement-1"), "Previous attempt failed")
	require.Contains(t, script.prompt("qa-cluster-1"), "intercept plan `checkout`")
	require.Contains(t, script.prompt("qa-local-1"), "VERDICT: PASS")
	require.Contains(t, script.prompt("qa-local-1"), "report VERDICT: SKIPPED")
}

func TestLocalSkippedContinuesToCluster(t *testing.T) {
	script := &agentScript{verdicts: map[string]string{"qa-local-1": "SKIPPED"}}
	env := newEnv(t, script)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free for orders over $50."})

	require.Equal(t, OutcomePassed, r.Outcome)
	require.Equal(t, []string{"implement-1", "qa-local-1", "qa-cluster-1"}, roles(r))
	require.Equal(t, VerdictSkipped, r.Steps[1].Verdict)
}

func TestLocalFailThenPassWithFeedback(t *testing.T) {
	script := &agentScript{verdicts: map[string]string{"qa-local-1": "FAIL"}}
	env := newEnvWithAgentDelay(t, script, time.Minute)
	// Arrives while attempt 1 is running, so it lands in attempt 2's request.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(FeedbackSignal, "Keep the $100 threshold configurable.")
	}, 30*time.Second)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free for orders over $100."})

	require.Equal(t, OutcomePassed, r.Outcome)
	require.Equal(t, 2, r.Attempt)
	require.Equal(t, []string{"implement-1", "qa-local-1", "implement-2", "qa-local-2", "qa-cluster-2"}, roles(r))
	require.Equal(t, VerdictFail, r.Steps[1].Verdict)

	retry := script.prompt("implement-2")
	require.Contains(t, retry, "Previous attempt failed verification")
	require.Contains(t, retry, "default-test-workflow-id-qa-local-1")
	require.Contains(t, retry, "VERDICT: FAIL")
	require.Contains(t, retry, "Keep the $100 threshold configurable.")
	require.Equal(t, []string{"Keep the $100 threshold configurable."}, r.Feedback)
}

func TestClusterFailLoopsBack(t *testing.T) {
	script := &agentScript{verdicts: map[string]string{"qa-cluster-1": "FAIL"}}
	env := newEnv(t, script)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free for orders over $100."})

	require.Equal(t, OutcomePassed, r.Outcome)
	require.Equal(t, []string{"implement-1", "qa-local-1", "qa-cluster-1", "implement-2", "qa-local-2", "qa-cluster-2"}, roles(r))
	require.Contains(t, script.prompt("implement-2"), "cluster QA pass")
}

func TestAttemptsExhausted(t *testing.T) {
	script := &agentScript{verdicts: map[string]string{"qa-local-1": "FAIL", "qa-local-2": ""}}
	env := newEnv(t, script)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free for orders over $100.", MaxAttempts: 2})

	require.Equal(t, OutcomeFailed, r.Outcome)
	require.Equal(t, []string{"implement-1", "qa-local-1", "implement-2", "qa-local-2"}, roles(r))
	require.Equal(t, VerdictFail, r.Steps[3].Verdict)
	require.Equal(t, "no VERDICT line in the QA report", r.Steps[3].Reason)
	require.Empty(t, r.VerifiedSnapshot)
	require.NotEmpty(t, r.BaselineSnapshot)
}

func TestNoInterceptPlanSkipsCluster(t *testing.T) {
	script := &agentScript{}
	env := newEnv(t, script)
	r := runDelivery(t, env, DeliveryInput{Request: "Update the README.", InterceptPlan: "none"})

	require.Equal(t, OutcomePassed, r.Outcome)
	require.Equal(t, []string{"implement-1", "qa-local-1"}, roles(r))
	require.Empty(t, r.InterceptPlan)
	require.NotContains(t, script.prompt("qa-local-1"), "report VERDICT: SKIPPED")
}

func TestSkipLocal(t *testing.T) {
	script := &agentScript{}
	env := newEnv(t, script)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free.", SkipLocal: true})

	require.Equal(t, OutcomePassed, r.Outcome)
	require.Equal(t, []string{"implement-1", "qa-cluster-1"}, roles(r))
}

func TestCancelStopsRunningAgent(t *testing.T) {
	script := &agentScript{}
	env := newEnvWithAgentDelay(t, script, time.Hour)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelSignal, nil)
	}, time.Minute)
	r := runDelivery(t, env, DeliveryInput{Request: "Make shipping free."})

	require.Equal(t, OutcomeCancelled, r.Outcome)
	require.True(t, r.Cancelled)
	require.Len(t, r.Steps, 1)
	require.Equal(t, "implement", r.Steps[0].Role)
	require.Empty(t, r.VerifiedSnapshot)
}

func TestStatusQueryDuringRun(t *testing.T) {
	script := &agentScript{}
	env := newEnvWithAgentDelay(t, script, 10*time.Minute)
	var mid Status
	env.RegisterDelayedCallback(func() {
		v, err := env.QueryWorkflow(StatusQuery)
		require.NoError(t, err)
		require.NoError(t, v.Get(&mid))
	}, 15*time.Minute)
	runDelivery(t, env, DeliveryInput{Request: "Make shipping free."})

	require.Equal(t, PhaseLocalQA, mid.Phase)
	require.Equal(t, 1, mid.Attempt)
	require.Equal(t, testSandbox, mid.Sandbox)
	require.Len(t, mid.Steps, 2)
	require.NotEmpty(t, mid.Steps[0].Report)
	require.Empty(t, mid.Steps[1].Report)
}

func TestEmptyRequestRejected(t *testing.T) {
	env := newEnv(t, &agentScript{})
	env.ExecuteWorkflow(WorkflowName, DeliveryInput{Request: "  "})
	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "request is empty")
}
