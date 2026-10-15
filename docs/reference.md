# Reference

## Shell commands

The agent drives everything below; you can run the same commands yourself in the
orchestrator workspace.

```bash
cs ssh -W temporal-orchestrator/dev
cd ~/multiagent-temporal/orchestrator
bin/start-delivery "In checkoutservice, ..."         # prints the workflow ID and UI link
bin/delivery-status deliver-1a2b3c4d --wait 5m       # returns when progress changes
bin/deliveryctl result deliver-1a2b3c4d              # final report
bin/deliveryctl feedback deliver-1a2b3c4d "Keep the format configurable"
bin/deliveryctl cancel deliver-1a2b3c4d              # also stops the running agent session
bin/deliveryctl teardown deliver-1a2b3c4d            # deletes the delivery sandbox and snapshots
bin/deliveryctl list
```

`bin/start-delivery` options:

- `--template NAME`: the template for the delivery sandbox (default `sample-boutique`).
- `--intercept-plan NAME|none`: the plan for the cluster pass (default `checkout`); `none`
  skips it.
- `--skip-local`, `--skip-cluster`: skip a QA pass.
- `--max-attempts N`: implement-and-verify attempts (default 3).
- `--request-file FILE`: read the request from a file.

## Workflow

`DeliverChange` on task queue `deliver-change`
([workflow.go](../orchestrator/delivery/workflow.go)).

- **Input:** `request`, `template`, `workspace` (default `app`), `intercept_plan`,
  `max_attempts`, `skip_local`, `skip_cluster`.
- **Query `status`:** the phase, attempt, delivery sandbox, every step with its session
  name, verdict, and report, and any feedback received.
- **Signal `feedback`:** text added to the next coder attempt.
- **Signal `cancel`:** cancels the agent session in flight and ends with outcome
  `cancelled`.
- **Result:** the outcome (`passed`, `failed`, or `cancelled`), the changed files, the
  baseline and verified snapshots, and a reference to the sandbox.

Agent steps:

- Each step is the `RunAgentSession` activity
  ([activities.go](../orchestrator/delivery/activities.go)). It runs
  `cs llm session run --agent A --task -n NAME -W SANDBOX/app --wait=false`, then waits
  on the session in 45-second slices, heartbeating between them.
- Session names are fixed per step (`<workflow-id>-implement-1`,
  `<workflow-id>-qa-local-1`, `<workflow-id>-qa-cluster-1`, ...), so a retried activity
  rejoins its session.
- QA reports end with `VERDICT: PASS`, `FAIL`, or `SKIPPED`; a report without one counts
  as `FAIL`. Local QA reports `SKIPPED` for flows it can only reach through the intercept
  plan, which in `sample-boutique` is anything in checkout.
- Prompts are in [prompts.go](../orchestrator/delivery/prompts.go).

Delivery sandboxes are created and snapshotted through the Crafting provider of
[sandbox-orchestration-harness](https://github.com/temporal-community/sandbox-orchestration-harness).
They outlive the workflow, so you can review the change, until
`bin/deliveryctl teardown` deletes them.

## Develop

```bash
cd orchestrator && go test ./...    # Temporal test environment; agents and harness mocked
```

To deploy changes, push them to this repository and re-run `./install.sh`. It pulls the
latest code into `temporal-orchestrator`, rebuilds, and restarts the worker.
