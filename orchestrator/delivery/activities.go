package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
)

// Activities drive Crafting agents through the cs CLI, using whatever
// credentials cs already holds; inside a Crafting workspace that is the
// workspace owner.
type Activities struct {
	// CS is the cs binary. Empty uses $CS_BINARY, then "cs" on PATH.
	CS string
	// WaitSlice bounds each `cs llm session wait` call, so the activity can
	// heartbeat and notice cancellation between calls.
	WaitSlice time.Duration
}

// RunAgentSession starts a task-mode agent session and blocks until it stops,
// returning the agent's final reply.
//
// Session names are chosen by the workflow and are unique per workflow step,
// so a retried activity rejoins the session it already started instead of
// launching a second agent on the same work.
func (a *Activities) RunAgentSession(ctx context.Context, in AgentSessionInput) (AgentSessionResult, error) {
	logger := activity.GetLogger(ctx)
	res := AgentSessionResult{Name: in.Name}

	out, err := a.cs(ctx, in.Prompt, "llm", "session", "run", "-",
		"--agent", in.Agent,
		"--task",
		"-n", in.Name,
		"-W", in.Workspace,
		"--wait=false",
		"--print", "json")
	switch {
	case err == nil:
		logger.Info("started agent session", "session", in.Name, "agent", in.Agent)
	case isAlreadyExists(out):
		logger.Info("rejoining agent session", "session", in.Name)
	default:
		return res, fmt.Errorf("starting session %s: %w", in.Name, err)
	}

	slice := a.WaitSlice
	if slice <= 0 {
		slice = 45 * time.Second
	}
	for {
		activity.RecordHeartbeat(ctx, in.Name)
		if ctx.Err() != nil {
			a.cancelSession(in.Name)
			return res, ctx.Err()
		}
		out, err := a.cs(ctx, "", "llm", "session", "wait", in.Name, "--print", "json", "--timeout", slice.String())
		if ctx.Err() != nil {
			a.cancelSession(in.Name)
			return res, ctx.Err()
		}
		if err != nil && strings.Contains(strings.ToLower(out.stderr+out.stdout), "not found") {
			return res, fmt.Errorf("session %s disappeared: %w", in.Name, err)
		}
		msg, ok := lastMessage(out.stdout)
		if err == nil && ok && msg.stopped() {
			res.ID = msg.Session.ID
			res.State = msg.Status.State
			res.Text = msg.Reply.Content.Text
			if res.Text == "" {
				res.Text = fmt.Sprintf("(session %s stopped without a reply; stop details: %s)", in.Name, string(msg.Stop))
			}
			return res, nil
		}
	}
}

func (a *Activities) cancelSession(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = a.cs(ctx, "", "llm", "session", "cancel", name)
}

type csOutput struct {
	stdout, stderr string
}

func (a *Activities) cs(ctx context.Context, stdin string, args ...string) (csOutput, error) {
	bin := a.CS
	if bin == "" {
		bin = os.Getenv("CS_BINARY")
	}
	if bin == "" {
		bin = "cs"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := csOutput{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out, fmt.Errorf("cs %s exited %d: %s", args[0]+" "+args[1]+" "+args[2], exitErr.ExitCode(), firstLine(out.stderr))
		}
		return out, err
	}
	return out, nil
}

// sessionMessage is the JSON cs prints for `--print json`.
type sessionMessage struct {
	Reply struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"reply"`
	Session struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"session"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Stop json.RawMessage `json:"stop"`
}

func (m sessionMessage) stopped() bool {
	switch strings.ToUpper(m.Status.State) {
	case "", "RUNNING", "PENDING", "STARTING", "ACTIVE", "WAITING":
		return false
	}
	return true
}

// lastMessage returns the last JSON object cs printed. cs may print other
// lines (extension sync notices) around it.
func lastMessage(stdout string) (sessionMessage, bool) {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m sessionMessage
		if err := json.Unmarshal([]byte(line), &m); err == nil && m.Status.State != "" {
			return m, true
		}
	}
	return sessionMessage{}, false
}

func isAlreadyExists(out csOutput) bool {
	s := strings.ToLower(out.stderr + out.stdout)
	return strings.Contains(s, "alreadyexists") || strings.Contains(s, "already_exist") || strings.Contains(s, "already exists")
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Sync ") || strings.HasPrefix(line, "Try re-install") {
			continue
		}
		return line
	}
	return "no error output"
}
