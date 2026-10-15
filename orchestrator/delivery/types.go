// Package delivery implements DeliverChange, a Temporal workflow that takes a
// change request through the same flow the sample engineering manager runs in
// a single agent conversation: a coder agent implements the change in a
// boutique sandbox, then fresh QA agent sessions verify it locally and on the
// demo cluster through Kubernetes interception, looping back to the coder with
// the QA evidence until verification passes or attempts run out.
package delivery

const (
	WorkflowName = "DeliverChange"

	StatusQuery    = "status"
	FeedbackSignal = "feedback"
	CancelSignal   = "cancel"

	DefaultTemplate      = "sample-boutique"
	DefaultWorkspace     = "app"
	DefaultInterceptPlan = "checkout"
	DefaultMaxAttempts   = 3

	CoderAgent = "sample-software-engineer"
	QAAgent    = "sample-qa-engineer"
)

type Phase string

const (
	PhaseProvisioning Phase = "provisioning"
	PhaseImplementing Phase = "implementing"
	PhaseLocalQA      Phase = "local-qa"
	PhaseClusterQA    Phase = "cluster-qa"
	PhaseFinishing    Phase = "finishing"
	PhaseDone         Phase = "done"
)

type Verdict string

const (
	VerdictPass    Verdict = "PASS"
	VerdictFail    Verdict = "FAIL"
	VerdictSkipped Verdict = "SKIPPED"
)

type Outcome string

const (
	OutcomePassed    Outcome = "passed"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// DeliveryInput is the workflow input. Zero values take the defaults above;
// an InterceptPlan of "none" skips the cluster pass.
type DeliveryInput struct {
	Request       string `json:"request"`
	Template      string `json:"template,omitempty"`
	Workspace     string `json:"workspace,omitempty"`
	InterceptPlan string `json:"intercept_plan,omitempty"`
	MaxAttempts   int    `json:"max_attempts,omitempty"`
	SkipLocal     bool   `json:"skip_local,omitempty"`
	SkipCluster   bool   `json:"skip_cluster,omitempty"`
}

// Step is one agent session the workflow ran.
type Step struct {
	Role    string  `json:"role"` // implement, qa-local, qa-cluster
	Attempt int     `json:"attempt"`
	Session string  `json:"session"`
	Verdict Verdict `json:"verdict,omitempty"`
	Reason  string  `json:"reason,omitempty"`
	Report  string  `json:"report,omitempty"`
}

// Status is returned by the status query while the workflow runs and is
// embedded in the final report.
type Status struct {
	WorkflowID    string   `json:"workflow_id"`
	Phase         Phase    `json:"phase"`
	Attempt       int      `json:"attempt"`
	MaxAttempts   int      `json:"max_attempts"`
	Template      string   `json:"template"`
	Sandbox       string   `json:"sandbox,omitempty"`
	Workspace     string   `json:"workspace"`
	InterceptPlan string   `json:"intercept_plan,omitempty"`
	SandboxRef    string   `json:"sandbox_ref,omitempty"`
	Steps         []Step   `json:"steps"`
	Feedback      []string `json:"feedback,omitempty"`
	Cancelled     bool     `json:"cancelled,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// Report is the workflow result.
type Report struct {
	Status
	Outcome          Outcome `json:"outcome"`
	ChangedFiles     string  `json:"changed_files,omitempty"`
	BaselineSnapshot string  `json:"baseline_snapshot,omitempty"`
	VerifiedSnapshot string  `json:"verified_snapshot,omitempty"`
}

// AgentSessionInput starts (or rejoins) one Crafting agent session.
type AgentSessionInput struct {
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Workspace string `json:"workspace"` // SANDBOX/WORKSPACE
	Prompt    string `json:"prompt"`
}

type AgentSessionResult struct {
	Name  string `json:"name"`
	ID    string `json:"id,omitempty"`
	State string `json:"state,omitempty"`
	Text  string `json:"text"`
}
