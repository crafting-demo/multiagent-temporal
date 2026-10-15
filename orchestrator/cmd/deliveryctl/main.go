// Command deliveryctl starts and inspects DeliverChange workflows.
//
//	deliveryctl start [flags] [REQUEST|-]   start a delivery, print its workflow ID
//	deliveryctl status ID [--wait DUR]      show progress; --wait blocks until it changes
//	deliveryctl result ID                   wait for the final report
//	deliveryctl feedback ID TEXT            add feedback for the next coder attempt
//	deliveryctl cancel ID                   stop the delivery and its running agent
//	deliveryctl teardown ID                 delete the delivery's sandbox and snapshots
//	deliveryctl list                        list recent deliveries
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/crafting-demo/multiagent-temporal/orchestrator/delivery"
	"github.com/crafting-demo/multiagent-temporal/orchestrator/internal/config"
)

const sandboxStopSignal = "sandbox-stop"

var cfg = config.FromEnv()

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	c, err := client.Dial(client.Options{HostPort: cfg.Address, Namespace: cfg.Namespace})
	if err != nil {
		fatalf("connect to Temporal at %s: %v", cfg.Address, err)
	}
	defer c.Close()
	ctx := context.Background()

	switch cmd {
	case "start":
		err = start(ctx, c, args)
	case "status":
		err = status(ctx, c, args)
	case "result":
		err = result(ctx, c, args)
	case "feedback":
		err = feedback(ctx, c, args)
	case "cancel":
		err = cancel(ctx, c, args)
	case "teardown":
		err = teardown(ctx, c, args)
	case "list":
		err = list(ctx, c)
	default:
		usage()
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: deliveryctl start|status|result|feedback|cancel|teardown|list ...
  start [--template T] [--workspace W] [--intercept-plan P|none] [--max-attempts N]
        [--skip-local] [--skip-cluster] [--id ID] [--request-file F] [REQUEST|-]
  status ID [--wait DURATION] [--json]
  result ID [--json]
  feedback ID TEXT
  cancel ID
  teardown ID
  list`)
	os.Exit(2)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "deliveryctl: "+format+"\n", a...)
	os.Exit(1)
}

func start(ctx context.Context, c client.Client, args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	var in delivery.DeliveryInput
	fs.StringVar(&in.Template, "template", delivery.DefaultTemplate, "template to create the sandbox from")
	fs.StringVar(&in.Workspace, "workspace", delivery.DefaultWorkspace, "workspace the agents work in")
	fs.StringVar(&in.InterceptPlan, "intercept-plan", delivery.DefaultInterceptPlan, `Kubernetes intercept plan for the cluster pass ("none" to skip)`)
	fs.IntVar(&in.MaxAttempts, "max-attempts", delivery.DefaultMaxAttempts, "implement/verify attempts before giving up")
	fs.BoolVar(&in.SkipLocal, "skip-local", false, "skip the local QA pass")
	fs.BoolVar(&in.SkipCluster, "skip-cluster", false, "skip the cluster QA pass")
	id := fs.String("id", "", "workflow ID (default: deliver-<random>)")
	file := fs.String("request-file", "", "read the request from this file")
	_ = fs.Parse(reorder(args))

	switch {
	case *file != "":
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		in.Request = string(b)
	case fs.NArg() == 1 && fs.Arg(0) == "-", fs.NArg() == 0:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		in.Request = string(b)
	default:
		in.Request = strings.Join(fs.Args(), " ")
	}
	if strings.TrimSpace(in.Request) == "" {
		return errors.New("empty request")
	}
	if *id == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		*id = "deliver-" + hex.EncodeToString(b[:])
	}

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    *id,
		TaskQueue:             cfg.TaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, delivery.WorkflowName, in)
	if err != nil {
		return fmt.Errorf("start workflow: %w", err)
	}
	fmt.Printf("workflow: %s\n", run.GetID())
	if u := config.UIURL(cfg.Namespace, run.GetID()); u != "" {
		fmt.Printf("temporal-ui: %s\n", u)
	}
	return nil
}

// reorder moves flags ahead of positional arguments so "start TEXT --flag"
// parses like "start --flag TEXT".
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		if !strings.Contains(a, "=") && i+1 < len(args) && !isBoolFlag(a) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, pos...)
}

func isBoolFlag(a string) bool {
	a = strings.TrimLeft(a, "-")
	return a == "skip-local" || a == "skip-cluster" || a == "json"
}

type snapshot struct {
	status  enumspb.WorkflowExecutionStatus
	state   delivery.Status
	report  *delivery.Report
	failure string
}

func load(ctx context.Context, c client.Client, id string) (snapshot, error) {
	var s snapshot
	desc, err := c.DescribeWorkflowExecution(ctx, id, "")
	if err != nil {
		return s, err
	}
	s.status = desc.WorkflowExecutionInfo.Status
	switch s.status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
		v, err := c.QueryWorkflow(ctx, id, "", delivery.StatusQuery)
		if err != nil {
			return s, fmt.Errorf("query status: %w", err)
		}
		err = v.Get(&s.state)
		return s, err
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		var r delivery.Report
		if err := c.GetWorkflow(ctx, id, "").Get(ctx, &r); err != nil {
			return s, err
		}
		s.report, s.state = &r, r.Status
	default:
		if err := c.GetWorkflow(ctx, id, "").Get(ctx, nil); err != nil {
			s.failure = err.Error()
		}
		if v, err := c.QueryWorkflow(ctx, id, "", delivery.StatusQuery); err == nil {
			_ = v.Get(&s.state)
		}
	}
	return s, nil
}

func (s snapshot) progressKey() string {
	return fmt.Sprintf("%v|%s|%d|%d|%d", s.status, s.state.Phase, s.state.Attempt, len(s.state.Steps), countReports(s.state.Steps))
}

func countReports(steps []delivery.Step) int {
	n := 0
	for _, st := range steps {
		if st.Report != "" || st.Reason != "" {
			n++
		}
	}
	return n
}

func status(ctx context.Context, c client.Client, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	wait := fs.Duration("wait", 0, "block until progress changes or the delivery ends, up to this long")
	asJSON := fs.Bool("json", false, "print JSON")
	_ = fs.Parse(reorder(args))
	if fs.NArg() != 1 {
		usage()
	}
	id := fs.Arg(0)

	s, err := load(ctx, c, id)
	if err != nil {
		return err
	}
	if *wait > 0 {
		deadline := time.Now().Add(*wait)
		key := s.progressKey()
		for s.status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING && s.progressKey() == key && time.Now().Before(deadline) {
			time.Sleep(10 * time.Second)
			if s, err = load(ctx, c, id); err != nil {
				return err
			}
		}
	}
	return render(id, s, *asJSON)
}

func result(ctx context.Context, c client.Client, args []string) error {
	fs := flag.NewFlagSet("result", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	_ = fs.Parse(reorder(args))
	if fs.NArg() != 1 {
		usage()
	}
	id := fs.Arg(0)
	_ = c.GetWorkflow(ctx, id, "").Get(ctx, nil)
	s, err := load(ctx, c, id)
	if err != nil {
		return err
	}
	return render(id, s, *asJSON)
}

func render(id string, s snapshot, asJSON bool) error {
	if asJSON {
		out := map[string]any{"workflow_id": id, "execution_status": strings.TrimPrefix(s.status.String(), "WORKFLOW_EXECUTION_STATUS_")}
		if s.report != nil {
			out["report"] = s.report
		} else {
			out["status"] = s.state
		}
		if s.failure != "" {
			out["failure"] = s.failure
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	st := s.state
	fmt.Printf("Workflow:     %s (%s)\n", id, executionStatus(s.status))
	if u := config.UIURL(cfg.Namespace, id); u != "" {
		fmt.Printf("Temporal UI:  %s\n", u)
	}
	if s.report != nil {
		fmt.Printf("Outcome:      %s after %d of %d attempts\n", s.report.Outcome, st.Attempt, st.MaxAttempts)
	} else {
		fmt.Printf("Phase:        %s (attempt %d of %d)\n", st.Phase, st.Attempt, st.MaxAttempts)
	}
	if st.Sandbox != "" {
		fmt.Printf("Sandbox:      %s, workspace %s (template %s)\n", st.Sandbox, st.Workspace, st.Template)
	} else {
		fmt.Printf("Sandbox:      provisioning from template %s\n", st.Template)
	}
	if st.InterceptPlan != "" {
		fmt.Printf("Intercept:    plan %s\n", st.InterceptPlan)
	}
	if len(st.Feedback) > 0 {
		fmt.Printf("Feedback:     %s\n", strings.Join(st.Feedback, " | "))
	}
	if st.Cancelled {
		fmt.Println("Cancelled:    yes")
	}
	if st.Error != "" {
		fmt.Printf("Error:        %s\n", st.Error)
	}
	if s.failure != "" {
		fmt.Printf("Failure:      %s\n", s.failure)
	}
	if len(st.Steps) > 0 {
		fmt.Println("Steps:")
		for i, step := range st.Steps {
			state := "running"
			switch {
			case step.Verdict != "":
				state = string(step.Verdict)
				if step.Reason != "" {
					state += " (" + step.Reason + ")"
				}
			case step.Reason != "":
				state = "error: " + step.Reason
			case step.Report != "":
				state = "done"
			}
			fmt.Printf("  %d. %-10s attempt %d  session %-40s %s\n", i+1, step.Role, step.Attempt, step.Session, state)
		}
	}
	if s.report != nil {
		r := s.report
		if r.ChangedFiles != "" {
			fmt.Printf("Changed files:\n%s\n", indent(r.ChangedFiles))
		}
		if r.BaselineSnapshot != "" {
			fmt.Printf("Baseline snapshot: %s\n", r.BaselineSnapshot)
		}
		if r.VerifiedSnapshot != "" {
			fmt.Printf("Verified snapshot: %s\n", r.VerifiedSnapshot)
		}
		for i := len(st.Steps) - 1; i >= 0; i-- {
			if st.Steps[i].Role == "implement" && st.Steps[i].Report != "" {
				fmt.Printf("\nLast implementation report (%s):\n%s\n", st.Steps[i].Session, indent(st.Steps[i].Report))
				break
			}
		}
		for _, step := range st.Steps {
			if step.Role != "implement" && step.Attempt == st.Attempt && step.Report != "" {
				fmt.Printf("\n%s report (%s):\n%s\n", step.Role, step.Session, indent(step.Report))
			}
		}
		fmt.Printf("\nThe sandbox stays up for review. Remove it with: deliveryctl teardown %s\n", id)
	}
	return nil
}

func executionStatus(s enumspb.WorkflowExecutionStatus) string {
	name := strings.ToLower(strings.TrimPrefix(s.String(), "WORKFLOW_EXECUTION_STATUS_"))
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}

func feedback(ctx context.Context, c client.Client, args []string) error {
	if len(args) < 2 {
		usage()
	}
	if err := c.SignalWorkflow(ctx, args[0], "", delivery.FeedbackSignal, strings.Join(args[1:], " ")); err != nil {
		return err
	}
	fmt.Println("feedback recorded; it goes to the next coder attempt")
	return nil
}

func cancel(ctx context.Context, c client.Client, args []string) error {
	if len(args) != 1 {
		usage()
	}
	if err := c.SignalWorkflow(ctx, args[0], "", delivery.CancelSignal, nil); err != nil {
		return err
	}
	fmt.Println("cancel requested; the running agent session is being stopped")
	return nil
}

func teardown(ctx context.Context, c client.Client, args []string) error {
	if len(args) != 1 {
		usage()
	}
	s, err := load(ctx, c, args[0])
	if err != nil {
		return err
	}
	if s.status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return errors.New("delivery is still running; cancel it first")
	}
	if s.state.SandboxRef == "" {
		return errors.New("delivery has no sandbox")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s.state.SandboxRef)
	if err != nil {
		return fmt.Errorf("decode sandbox ref: %w", err)
	}
	var ref struct {
		SandboxID string `json:"sandbox_id"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil || ref.SandboxID == "" {
		return fmt.Errorf("decode sandbox ref: %v", err)
	}

	if err := c.SignalWorkflow(ctx, ref.SandboxID, "", sandboxStopSignal, nil); err != nil {
		fmt.Printf("sandbox workflow %s: %v (already stopped?)\n", ref.SandboxID, err)
	} else {
		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		_ = c.GetWorkflow(waitCtx, ref.SandboxID, "").Get(waitCtx, nil)
		fmt.Printf("deleted sandbox %s (harness workflow %s)\n", s.state.Sandbox, ref.SandboxID)
	}

	if s.report != nil {
		for _, id := range []string{s.report.BaselineSnapshot, s.report.VerifiedSnapshot} {
			for _, name := range snapshotNames(id) {
				out, err := exec.CommandContext(ctx, "cs", "snapshot", "remove", name, "-f").CombinedOutput()
				if err != nil {
					fmt.Printf("snapshot %s: %s\n", name, strings.TrimSpace(string(out)))
					continue
				}
				fmt.Printf("deleted snapshot %s\n", name)
			}
		}
	}
	return nil
}

// snapshotNames lists the Crafting snapshots inside a composite snapshot ID,
// which is JSON carrying the workspace name plus one snapshot per component.
func snapshotNames(composite string) []string {
	if composite == "" {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(composite), &v); err != nil {
		return nil
	}
	var names []string
	var walk func(key string, x any)
	walk = func(key string, x any) {
		switch t := x.(type) {
		case string:
			if key != "ws" && key != "v" && t != "" {
				names = append(names, t)
			}
		case map[string]any:
			for k, y := range t {
				walk(k, y)
			}
		case []any:
			for _, y := range t {
				walk(key, y)
			}
		}
	}
	walk("", v)
	sort.Strings(names)
	return names
}

func list(ctx context.Context, c client.Client) error {
	resp, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: cfg.Namespace,
		Query:     fmt.Sprintf("WorkflowType = '%s'", delivery.WorkflowName),
		PageSize:  20,
	})
	if err != nil {
		return err
	}
	for _, e := range resp.Executions {
		fmt.Printf("%-24s %-10s %s\n", e.Execution.WorkflowId,
			strings.TrimPrefix(e.Status.String(), "WORKFLOW_EXECUTION_STATUS_"),
			e.StartTime.AsTime().Local().Format("2006-01-02 15:04"))
	}
	return nil
}
