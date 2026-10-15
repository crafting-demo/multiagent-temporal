package delivery

import (
	"fmt"
	"regexp"
	"strings"
)

const verdictInstruction = `End your report with exactly one final line, and nothing after it:
VERDICT: PASS
or
VERDICT: FAIL
or
VERDICT: SKIPPED`

func target(sandbox, workspace string) string {
	return fmt.Sprintf("Sandbox `%s`, workspace `%s`. Work in this existing sandbox only; do not create another sandbox.", sandbox, workspace)
}

// ImplementPrompt is the coder request. It does not ask for verification, so
// the coder does not delegate to its QA sub-agent: the workflow runs QA as
// separate, fresh sessions.
func ImplementPrompt(in DeliveryInput, sandbox string, attempt int, feedback []string, lastFailure *Step) string {
	var b strings.Builder
	b.WriteString(target(sandbox, in.Workspace))
	fmt.Fprintf(&b, " The sandbox was created from template `%s`.\n\n", in.Template)
	b.WriteString("## Request\n\n")
	b.WriteString(strings.TrimSpace(in.Request))
	b.WriteString("\n")
	if len(feedback) > 0 {
		b.WriteString("\n## Additional feedback from the requester\n\n")
		for _, f := range feedback {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(f))
		}
	}
	if lastFailure != nil {
		fmt.Fprintf(&b, "\n## Previous attempt failed verification\n\nThis is attempt %d. The %s pass (session `%s`) reported:\n\n", attempt, passName(lastFailure.Role), lastFailure.Session)
		b.WriteString(quote(lastFailure.Report))
		b.WriteString("\nFix the problems it found. The original request above still applies in full.\n")
	}
	b.WriteString(`
## How to work

- Implement the change only. An independent QA pass runs after you, so do not
  verify it yourself and do not hand off to a QA agent.
- When a template daemon runs the code you changed, restart it (` + "`cs restart NAME`" + `)
  once the change builds, and confirm with ` + "`cs ps`" + `.
- Do not open a pull request. Leave the sandbox running.

Report the sandbox and workspace, the files you changed, and how to exercise the change.
`)
	return b.String()
}

// LocalQAPrompt asks a fresh QA session for a local pass.
func LocalQAPrompt(in DeliveryInput, sandbox, implReport string) string {
	var b strings.Builder
	b.WriteString("Local QA pass. ")
	b.WriteString(target(sandbox, in.Workspace))
	b.WriteString("\n\nVerify the change against the services running in this sandbox. This request is the local pass only; do not run a cluster pass and do not start an intercept plan.\n\n")
	if !in.SkipCluster && in.InterceptPlan != "" {
		fmt.Fprintf(&b, `A cluster pass through intercept plan `+"`%s`"+` runs after this one. Some flows can only reach this
sandbox's services while that plan runs (for example, when the service under test
depends on cluster services or is only routed to through interception). If that is
the case here, check what you can locally (the code builds, the daemon runs the new
build, its log is clean, the diff matches the requirements), say what you could not
exercise and why, and report VERDICT: SKIPPED. Report VERDICT: FAIL only for a
concrete defect in the change.

`, in.InterceptPlan)
	}
	writeChange(&b, in, implReport)
	b.WriteString("\n")
	b.WriteString(verdictInstruction)
	b.WriteString("\n")
	return b.String()
}

// ClusterQAPrompt asks a fresh QA session for a cluster pass through the
// template's Kubernetes intercept plan.
func ClusterQAPrompt(in DeliveryInput, sandbox, implReport string) string {
	var b strings.Builder
	b.WriteString("Cluster QA pass. ")
	b.WriteString(target(sandbox, in.Workspace))
	fmt.Fprintf(&b, "\n\nVerify the change on the cluster through Crafting Kubernetes interception, using the template's intercept plan `%s`. ", in.InterceptPlan)
	b.WriteString("Prove the traffic reached this sandbox's service rather than the deployed workload. ")
	b.WriteString("If the template ships a helper script for driving the flow (for example `place-test-order`), prefer it.\n\n")
	writeChange(&b, in, implReport)
	b.WriteString("\n")
	b.WriteString(verdictInstruction)
	b.WriteString("\n")
	return b.String()
}

func writeChange(b *strings.Builder, in DeliveryInput, implReport string) {
	b.WriteString("## Requirements\n\n")
	b.WriteString(strings.TrimSpace(in.Request))
	b.WriteString("\n\n## What the implementer reported\n\n")
	b.WriteString(quote(implReport))
}

func passName(role string) string {
	switch role {
	case "qa-local":
		return "local QA"
	case "qa-cluster":
		return "cluster QA"
	}
	return role
}

func quote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "> (no report)\n"
	}
	return "> " + strings.ReplaceAll(s, "\n", "\n> ") + "\n"
}

var verdictLine = regexp.MustCompile(`(?i)^\W*verdict\W*:\W*(pass|fail|skipped)\b`)

// ParseVerdict returns the verdict on the last VERDICT line of a QA report. A
// report without one counts as a failure, so a QA session that stopped early
// sends the work back instead of letting it through.
func ParseVerdict(report string) (Verdict, string) {
	lines := strings.Split(report, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := verdictLine.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			return Verdict(strings.ToUpper(m[1])), ""
		}
	}
	return VerdictFail, "no VERDICT line in the QA report"
}
