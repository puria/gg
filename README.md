# gg

`gg` is a small Go CLI that resolves a GitHub repository to a local checkout path, clones it if needed, and is designed to be wrapped by a shell function so `gg f credim` can still land you inside the repo.

Repos are stored as a container directory with a bare Git repo plus worktrees:

```text
<root>/<host>/<owner>/<repo>/
  .bare/
  main/
  worktrees/<name>/
  PR/<number>/
```

## Install

```bash
go install github.com/puria/gg@latest
```

## Config

`gg` reads JSON config from:

```text
$XDG_CONFIG_HOME/gg/config
~/.config/gg/config
```

You can print the active path with:

```bash
command gg config-path
```

You can persist an alias with:

```bash
command gg alias ForkbombEu/credimi credim
```

You can create a starter config with:

```bash
command gg init-config
```

Example config:

```json
{
  "root": "/home/puria/src",
  "host": "github.com",
  "aliases": {
    "f": "ForkbombEu",
    "credim": "credimi",
    "fc": "ForkbombEu/credimi",
    "f/credimi": "ForkbombEu/credimi"
  }
}
```

Alias resolution works in this order:

- exact alias first, so `gg fc` can map straight to `ForkbombEu/credimi`
- per-segment alias expansion, so `gg f credim` becomes `ForkbombEu/credimi`
- final exact alias pass on `owner/repo`, so `f/credimi` can also be overridden directly

After adding an alias like this:

```bash
command gg alias ForkbombEu/credimi credim
```

you can just run:

```bash
gg credim
```

If the repo is missing, `gg` creates the repo container and checks out the default branch into `main/`. If it already exists, the shell wrapper `cd`s into `<repo>/main`.

If you pass just an owner like `gg ForkbombEu` or an owner alias like `gg f`, `gg` goes to `<root>/<host>/<owner>`.

## Usage

```bash
gg ForkbombEu
gg ForkbombEu/credimi
gg ForkbombEu credimi
gg f/credim
gg f credim
gg credimi newworktree
gg credimi 99
gg list credimi
gg status credimi
gg prune credimi
```

The binary prints the local checkout path and clones the repo first if it does not exist yet.

`list`, `status`, and `prune` are reserved command names. If you ever need an owner or repo alias with one of those names, use `gg path ...` as the escape hatch.

## Worktrees And PRs

If the first argument already resolves to a full repo by itself, the next argument is treated as a repo-local target:

```bash
gg credimi newworktree
gg credimi 99
gg credimi \#99
gg f credim feature-x
gg ForkbombEu credimi feature-x
```

Behavior:

- `gg credimi newworktree` creates or reuses `<repo>/worktrees/newworktree`
- it also creates a local branch named `newworktree` for that worktree
- `gg credimi 99` creates or reuses `<repo>/PR/99`
- it checks out PR `99` there with `gh pr checkout 99 --detach`
- plain `gg credimi` goes to `<repo>/main`
- plain `gg ForkbombEu` goes to `<root>/<host>/ForkbombEu`

The shell wrapper then `cd`s into the resulting path, so it feels like the old `gg` flow.

## Management Commands

These commands do not `cd`; they print information or perform maintenance:

```bash
gg list credimi
gg status credimi
gg prune credimi
```

Behavior:

- `gg list credimi` prints `main`, `worktrees/*`, and `PR/*` entries for the managed repo
- `gg status credimi` runs `git status --short --branch` for each known worktree
- `gg prune credimi` runs `git worktree prune --verbose` and removes empty directories left under `worktrees/` or `PR/`

## Shell Integration

A binary cannot `cd` the parent shell on its own, so the normal setup is a tiny wrapper function that delegates to the Go binary.

### Fish

```fish
command gg shell-init fish > ~/.config/fish/functions/gg.fish
```

Or inline:

```fish
function gg --description 'manage git repos'
    switch "$argv[1]"
    case help -h --help version --version shell-init config-path init-config path alias
        command gg $argv
        return $status
    end

    set -l dir (command gg path $argv)
    or return $status

    cd $dir
end
```

### Bash / Zsh

```bash
eval "$(command gg shell-init bash)"
```
