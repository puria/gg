package main

import (
	"errors"
	"fmt"
	"os"
)

var version = "dev"

var errSilent = errors.New("silent exit") //nolint:gochecknoglobals

// untestable: main() is the process entrypoint; os.Exit paths aren't exercised in tests.
func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errSilent) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "gg:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 0:
		return pathCommand(nil)
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		printUsage()
		return nil
	case args[0] == "version" || args[0] == "--version":
		fmt.Println(version)
		return nil
	case args[0] == "path":
		return pathCommand(args[1:])
	case args[0] == "config-path":
		path, err := configPath()
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	case args[0] == "alias":
		return aliasCommand(args[1:])
	case args[0] == "list" || args[0] == "ls":
		return listCommand(args[1:])
	case args[0] == "status":
		return statusCommand(args[1:])
	case args[0] == "starship":
		return starshipCommand(args[1:])
	case args[0] == "prune" || args[0] == "rm":
		return pruneCommand(args[1:])
	case args[0] == "init-config":
		return initConfigCommand()
	case args[0] == "shell-init":
		if len(args) != 2 {
			return errors.New("usage: gg shell-init fish|bash|zsh")
		}
		script, err := shellInit(args[1])
		if err != nil {
			return err
		}
		fmt.Print(script)
		return nil
	default:
		return pathCommand(args)
	}
}

func pathCommand(args []string) error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}

	request, err := resolveRequest(cfg, args)
	if err != nil {
		return err
	}

	path, err := ensureRequest(cfg, request)
	if err != nil {
		return err
	}

	fmt.Println(path)
	return nil
}

func printUsage() {
	fmt.Print(`gg resolves a GitHub repository to a local checkout path.

Usage:
  gg <owner>
  gg <owner/repo>
  gg <owner> <repo>
  gg <repo> <worktree>
  gg <repo> <pr-number>
  gg <repo> #<pr-number>
  gg <owner> <repo> <worktree>
  gg <owner> <repo> <pr-number>
  gg path <owner>
  gg path <owner/repo>
  gg path <owner> <repo>
  gg alias <target> <name>
  gg list <owner/repo>
  gg list <owner> <repo>
  gg ls <owner/repo>
  gg ls <owner> <repo>
  gg status <owner/repo>
  gg status <owner> <repo>
  gg status --files <owner/repo>
  gg starship [repo|kind|name|worktree|pr]
  gg prune <owner/repo>
  gg prune <owner> <repo>
  gg rm <owner/repo>
  gg rm <owner> <repo>
  gg config-path
  gg init-config
  gg shell-init fish|bash|zsh
  gg version

Behavior:
  - reads config from $XDG_CONFIG_HOME/gg/config or ~/.config/gg/config
  - expands aliases from config
  - can persist aliases with 'gg alias <target> <name>'
  - clones missing repositories into a bare repo container
  - can open an owner directory at <root>/<host>/<owner>
  - uses <repo>/main as the default checkout path
  - can create repo worktrees under <repo>/worktrees/<name>
  - can check out PRs under <repo>/PR/<number>
  - supports ls/list, status, and rm/prune for managed repo worktrees
  - status prints branch plus clean/dirty summary; use --files for changed paths
  - starship prints prompt-friendly metadata for the current gg checkout
  - prints the target local path
`)
}
