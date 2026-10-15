# Install (read this first)

You are installing the Temporal Engineering Manager onto the **current org**: the
`temporal-orch` template, the pinned `temporal-orch` sandbox, and the
`temporal-engineering-manager` agent. Follow this file until all three exist and you have
told the user what to do next.

You have no extra context beyond this repository and the tools available in this session
(`cs` CLI after you are in a workspace).

## 1. Get this repository into a workspace

If the files of `https://github.com/crafting-demo/multiagent-temporal` are **already** in
the current workspace, use them. Do not clone again. If that directory is a git checkout
of this repo, pull the latest default branch first so you install current files.

Otherwise:

1. Create a sandbox from this git repository (the URL the user pasted). Do **not** name it
   `temporal-orch`: the install creates a sandbox with that name.
2. Target a workspace in that sandbox **immediately**. Do not stop to ask whether to open it.
3. Hand off to the workspace agent so you can run `cs` and `bash`.

This is an install, not an exploration. After the sandbox exists, keep going until the
install finishes.

Work from the repository root (the directory that contains this `INSTALL.md`).

## 2. Install

Prefer an org-shared agent:

```sh
./install.sh --shared
```

If that fails because you are not allowed to manage shared agents, run it without the flag:

```sh
./install.sh
```

The first run creates the `temporal-orch` sandbox, which takes about five minutes. If your
command times out before the script finishes, run the same command again: it is safe to
re-run and picks up where it left off. Do not create the template, sandbox, or agent by
hand.

## 3. When you are done

Verify that `cs sandbox show temporal-orch` shows the sandbox ready and pinned, and that
`cs llm agent show temporal-engineering-manager` (add `--shared` if you installed it
org-shared) succeeds.

Tell the user:

- install is finished, and whether the agent is org-shared or personal;
- the Temporal UI link: the `temporal-ui` endpoint of `temporal-orch`;
- to start a **new** session, select `temporal-engineering-manager`, and paste the
  "Try it" prompt from the README;
- that the sandbox created for this install is no longer needed and can be deleted.
