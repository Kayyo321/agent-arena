# Running Arena on a devbox

A devbox is a machine you own or rent that you reach over SSH. Running Arena there
inside `tmux` lets a long run continue while your laptop sleeps or disconnects, and you
reattach later to see where it got to.

## Boundaries

- Arena is not a hosted service. It runs only on the machine where you start it; there
  is no Arena server, account or remote worker.
- Nothing is synchronized between machines. The devbox has its own checkout, `.arena/`
  configuration, run history and worktrees. Moving work back to your laptop is ordinary
  Git (`git push` from the devbox, `git pull` locally).
- Provider sign-ins happen on the devbox, with each provider's own flow (for example
  run the provider CLI's login there, or set the API key in that shell). Never copy
  tokens, keychain items or credential files between machines. Arena does not read,
  move or share them, and neither should you.
- Supervision is attached: the process running the run owns it. Keep that process inside
  tmux, not in your SSH session, so a dropped connection does not interrupt the run.

## Steps

1. SSH to the devbox, clone the repository, build or install `arena`, and sign in to
   each provider you need on that machine.
2. Check the machine:

   ```sh
   arena doctor
   ```

   It reports Git, provider CLIs, bubblewrap or `sandbox-exec`, WSL2 and tmux. Only
   `FAIL` lines block; see [CLI](CLI.md#preflight).
3. Start the run in tmux. Either let Arena lay it out:

   ```sh
   arena tmux run WORKFLOW "your task"
   ```

   or start your own session and run `arena run WORKFLOW "your task"` in it. Pane 0 of
   the Arena layout is the supervisor; the other panes only observe
   ([details](CLI.md#running-a-run-in-tmux)).
4. Detach with the tmux prefix then `d`, or just disconnect. The run keeps going.
5. Later, SSH back in and reattach:

   ```sh
   tmux attach -t arena-RUN
   ```

   (`tmux ls` lists sessions.) If the devbox restarted, the run was interrupted:
   `arena resume RUN` or `arena tmux resume RUN`.

## Windows

Native Windows is not supported. On Windows use WSL2 and keep the project inside the
WSL filesystem (your Linux home directory), not under `/mnt/c` or another Windows drive
or network share, which are outside the supported scope. `arena doctor` detects WSL2
and warns when the project is on a Windows drive.
