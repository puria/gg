# AGENTS

## Purpose

`gg` is a small Go CLI plus shell wrapper that resolves GitHub repositories into local paths and supports three main workflows:

- open an owner directory
- open a repository checkout
- manage repo-local worktrees and PR checkouts

The Go binary prints or manages paths. The shell function is what performs `cd`.

## Build And Test

- Build: `go build -o bin/gg .`
- Tests: `go test ./...`
- Lint: `golangci-lint run ./...`
- Task aliases: `task build`, `task test`, `task lint`
- Before finishing work, always run the lint step relevant to CI and do not report completion until it passes

The user currently links `bin/gg` into `~/.local/bin/gg`.

## Shell Integration

The installed binary is expected to be on `PATH`.

The shell wrapper is generated with:

```fish
command gg shell-init fish > ~/.config/fish/functions/gg.fish
source ~/.config/fish/functions/gg.fish
```

Important behavior:

- `gg` should resolve to the shell function
- `command gg` should resolve to the binary
- management commands must be passed through by the shell wrapper
- after adding new top-level commands, regenerate the wrapper

## Config

Config is loaded from:

- `$GG_CONFIG` if set
- otherwise `$XDG_CONFIG_HOME/gg/config`
- otherwise `~/.config/gg/config`

The format is JSON with:

- `root`
- `host`
- `aliases`

Aliases can target:

- owner aliases like `f -> ForkbombEu`
- repo aliases like `credimi -> ForkbombEu/credimi`
- exact full aliases like `fc -> ForkbombEu/credimi`

The CLI can persist aliases with:

```bash
gg alias ForkbombEu/credimi credim
```

## Path Semantics

Single argument behavior:

- `gg ForkbombEu` opens the owner directory
- `gg credimi` opens the repo default checkout
- if a single argument resolves to a full repo alias, repo wins before owner fallback

Two and three argument behavior:

- `gg owner repo` opens the repo default checkout
- `gg repo worktree` opens or creates a named worktree when the first arg already resolves to a full repo
- `gg repo 99` or `gg repo #99` opens or creates a PR checkout
- `gg owner repo worktree` also works

Management commands:

- `gg list <repo>`
- `gg status <repo>`
- `gg prune <repo>`

Reserved top-level command names currently include:

- `help`
- `version`
- `path`
- `config-path`
- `alias`
- `init-config`
- `shell-init`
- `list`
- `status`
- `prune`

If a repo or owner collides with one of these, use `gg path ...` as the escape hatch.

## Managed Repo Layout

Managed repos use this layout:

```text
<root>/<host>/<owner>/<repo>/
  .bare/
  main/
  worktrees/<name>/
  PR/<number>/
```

Rules:

- plain `gg repo` goes to `<repo>/main`
- named worktrees live under `<repo>/worktrees/...`
- PR checkouts live under `<repo>/PR/...`

The `.bare` repository is the source of truth for managed repos.

## Existing Local Repositories

There are two supported local states:

1. Managed layout with `.bare/` and `main/`
2. Pre-existing plain local directory at `<root>/<host>/<owner>/<repo>`

If a plain local directory already exists, `gg` must prefer it instead of attempting a remote clone. In that case:

- `gg repo` returns the existing directory directly
- `gg list` reports it as `local`
- worktree and PR management commands are rejected for that repo

This behavior prevents failures for local-only projects like `puria/gg`.

## Case Canonicalization

Linux paths are case-sensitive, but GitHub owner/repo input is often typed with inconsistent casing.

Current rule:

- if an owner or repo directory already exists under the configured root, reuse the existing on-disk casing

This prevents duplicate local trees like:

- `ForkbombEu/credimi`
- `forkbombeu/credimi`

Important limitation:

- brand-new clones into an empty root still depend on the casing supplied by the user or aliases
- aliases remain the safest canonicalization mechanism for first use

## Clone And Worktree Rules

Managed clone behavior:

- bare clone uses `git clone --bare --recursive`
- after each worktree is created, run `git submodule update --init --recursive`

Default checkout behavior:

- for normal repos, create `main/` from the repo default branch
- for empty repos with no refs, create `main/` as an orphan branch

Named worktrees:

- path is `<repo>/worktrees/<name>`
- branch name matches the normalized worktree name
- if the branch already exists locally, reuse it
- if the repo has no refs yet, create the worktree as an orphan branch

PR checkouts:

- path is `<repo>/PR/<number>`
- create a detached worktree
- run `gh pr checkout <number> --detach` inside it
- PR checkout is rejected for repos with no commits yet

## Management Commands Behavior

`gg list`:

- lists `main`, discovered worktrees, and discovered PR directories for managed repos
- prints a single `local` entry for plain local repos

`gg status`:

- runs `git status --short --branch` in each listed entry

`gg prune`:

- runs `git worktree prune --verbose` against the managed `.bare` repo
- removes empty leftover directories under `worktrees/` and `PR/`
- is only valid for managed repos

## Files Of Interest

- [main.go](/home/puria/src/github.com/puria/gg/main.go): CLI entrypoint and command routing
- [config.go](/home/puria/src/github.com/puria/gg/config.go): config loading and alias persistence
- [repo.go](/home/puria/src/github.com/puria/gg/repo.go): repo resolution, canonicalization, clone/worktree/PR logic
- [manage.go](/home/puria/src/github.com/puria/gg/manage.go): `list`, `status`, `prune`
- [shell.go](/home/puria/src/github.com/puria/gg/shell.go): fish/bash/zsh wrapper generation
- [main_test.go](/home/puria/src/github.com/puria/gg/main_test.go): regression tests

## Things Not To Regress

- `gg puria gg` must use the existing local directory and must not try GitHub first
- `gg ForkbombEu` must open the owner directory
- `gg credimi` must open `<repo>/main`
- `gg credimi feature-x` must create or reuse `<repo>/worktrees/feature-x`
- `gg credimi 99` must create or reuse `<repo>/PR/99`
- empty repositories must still produce a usable `main/` worktree
- explicit `owner/repo` input must not be misinterpreted as a segment alias error
- after adding top-level commands, the generated shell wrapper must be updated to pass them through
