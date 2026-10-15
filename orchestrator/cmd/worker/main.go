// Command worker runs the DeliverChange workflow, its agent activities, and
// the sandbox-orchestration-harness SandboxWorkflow on TEMPORAL_TASK_QUEUE.
package main

import (
	"log"

	sandbox "github.com/temporal-community/sandbox-orchestration-harness/sdk"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/crafting-demo/multiagent-temporal/orchestrator/delivery"
	"github.com/crafting-demo/multiagent-temporal/orchestrator/internal/config"
)

func main() {
	cfg := config.FromEnv()
	c, err := client.Dial(client.Options{HostPort: cfg.Address, Namespace: cfg.Namespace})
	if err != nil {
		log.Fatalf("connect to Temporal at %s: %v", cfg.Address, err)
	}
	defer c.Close()

	w := worker.New(c, cfg.TaskQueue, worker.Options{})
	sandbox.Register(w, c)
	w.RegisterWorkflowWithOptions(delivery.DeliverChange, workflow.RegisterOptions{Name: delivery.WorkflowName})
	w.RegisterActivity(&delivery.Activities{})

	log.Printf("worker polling %s on %s (namespace %s)", cfg.TaskQueue, cfg.Address, cfg.Namespace)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker: %v", err)
	}
}
