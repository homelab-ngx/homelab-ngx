# A new service: find a stack or write one

When the user asks for a service the host doesn't run yet, look for a
public stack first. Write a new one only when there is none, or the user
asks for it.

## Find a public stack

The project's stacks are the `stack-*` repos of the homelab-ngx
organisation on GitHub:

```sh
curl -s 'https://api.github.com/orgs/homelab-ngx/repos?per_page=100' |
  jq -r '.[] | select(.name | startswith("stack-")) | "\(.name)\t\(.description)"'
curl -s https://raw.githubusercontent.com/homelab-ngx/stack-<name>/HEAD/README.md
```

GitHub allows 60 such calls an hour without a token. A stack's README says
what it runs, its "Getting started" steps, and the grants and variables its
manifest asks for. Tell the user what the stack asks of the host before
you install it.

## Install a public stack

```sh
homelab stacks add github:homelab-ngx/stack-<name>   # nothing is approved, built or started
homelab stacks show <stack>                          # the fetched commit, its grants and variables
homelab stacks var set <stack> <NAME> --value-stdin  # each required variable
homelab stacks folder create <stack> <NAME>          # makes a path variable's folder, owned by the stack
homelab stacks approve <stack> <rev>                 # the Fetched commit from show
homelab stacks start <stack>                         # the first start builds it
```

Approving grants the stack its routes, folders, ports and other access, so
ask the user before you approve. A route answers at
`https://<label>.<the host's domain>` once the stack is healthy.

## Write a new stack

Read [stack-rules.md](stack-rules.md) first: the manifest's fields and the
rules for the NixOS module. The dashboard's draft assistant uses the same
rules.

1. Propose a name (1 to 12 characters: a-z, 0-9 and `-`, starting with a
   letter) and the folder `~/stacks/stack-<name>`. Ask before creating it.
2. Start from the template in `/usr/share/homelab/agent/template`. It
   serves a folder with nginx. Replace `NAME` in it with the name:

   ```sh
   mkdir -p ~/stacks/stack-<name> && cd ~/stacks/stack-<name>
   git init -q
   cp /usr/share/homelab/agent/template/* . && chmod u+w *
   sed -i 's/NAME/<name>/g' homelab-stack.json flake.nix
   ```

3. Write the service into `flake.nix` and what it asks of the host into
   `homelab-stack.json`. Look for the NixOS module in nixpkgs first
   (`services.<name>`). Say so when you are not sure an option exists.
4. Build it until it builds. This is the build the daemon runs; nothing is
   installed:

   ```sh
   homelab stacks build ~/stacks/stack-<name>
   ```

5. Commit, then install it from the repo. The daemon reads only committed
   files:

   ```sh
   git add -A && git commit -qm "First version"
   homelab stacks add --name <name> git+file:///root/stacks/stack-<name>
   ```

   Then set its variables, approve and start it as above, with the user's
   consent.

A stack in a public repo names nothing of this host: no host names,
addresses, user names or time zone. Values like these go in variables.

## Change it

```sh
homelab stacks build ~/stacks/stack-<name>
git commit -qam "<what changed>"
homelab stacks fetch <name>     # the new commit; one that asks for new grants waits for approval
homelab stacks update <name>    # an unhealthy result is rolled back
```

When a service fails after an update, read its journal (see
[logs.md](logs.md)) before you change the module again.

## Move it

- **To a remote repo:** push the repo, for example to GitHub, then
  `homelab stacks source <name> github:<owner>/stack-<name>`. What runs, the
  variables and the data stay. The commit found there is approved by itself
  when it asks for nothing new.
- **Into the dashboard:** Fork on the stack's page copies it into a draft
  and moves the stack to it. The user edits it there from then on, and the
  local repo is no longer the stack's source.
