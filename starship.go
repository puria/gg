package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type starshipInfo struct {
	Repo Repo
	Kind string
	Name string
}

func starshipCommand(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: gg starship [repo|kind|name|worktree|pr]")
	}

	part := "summary"
	if len(args) == 1 {
		part = args[0]
	}
	switch part {
	case "summary", "repo", "kind", "name", "worktree", "pr":
	default:
		return fmt.Errorf("usage: gg starship [repo|kind|name|worktree|pr]")
	}

	info, ok, err := currentStarshipInfo()
	if err != nil {
		return err
	}
	if !ok {
		return errSilent
	}

	switch part {
	case "summary":
		fmt.Println(formatStarshipSummary(info))
	case "repo":
		fmt.Println(info.Repo.String())
	case "kind":
		fmt.Println(info.Kind)
	case "name":
		if info.Name != "" {
			fmt.Println(info.Name)
		} else {
			return errSilent
		}
	case "worktree":
		if info.Kind == "worktree" {
			fmt.Println(info.Name)
		} else {
			return errSilent
		}
	case "pr":
		if info.Kind == "pr" {
			fmt.Printf("#%s\n", info.Name)
		} else {
			return errSilent
		}
	}

	return nil
}

func currentStarshipInfo() (starshipInfo, bool, error) {
	cfg, err := loadConfigOnly()
	if err != nil {
		return starshipInfo{}, false, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return starshipInfo{}, false, fmt.Errorf("resolve current directory: %w", err)
	}

	hostRoot := filepath.Join(cfg.Root, cfg.Host)
	rel, ok, err := relativeWithin(hostRoot, cwd)
	if err != nil || !ok {
		return starshipInfo{}, false, err
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return starshipInfo{}, false, nil
	}

	info := starshipInfo{
		Repo: Repo{Owner: parts[0], Name: parts[1]},
		Kind: "local",
	}

	if len(parts) < 3 {
		return info, true, nil
	}

	switch parts[2] {
	case "main":
		info.Kind = "main"
		info.Name = "main"
	case "worktrees":
		if len(parts) < 4 {
			return starshipInfo{}, false, nil
		}
		info.Kind = "worktree"
		info.Name = parts[3]
	case "PR":
		if len(parts) < 4 {
			return starshipInfo{}, false, nil
		}
		info.Kind = "pr"
		info.Name = parts[3]
	}

	return info, true, nil
}

func relativeWithin(root, path string) (string, bool, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false, fmt.Errorf("resolve root %s: %w", root, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve path %s: %w", path, err)
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", false, fmt.Errorf("compare %s to %s: %w", absPath, absRoot, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, nil
	}

	return rel, true, nil
}

func formatStarshipSummary(info starshipInfo) string {
	switch info.Kind {
	case "worktree":
		return fmt.Sprintf("%s wt:%s", info.Repo.String(), info.Name)
	case "pr":
		return fmt.Sprintf("%s PR:#%s", info.Repo.String(), info.Name)
	default:
		return fmt.Sprintf("%s %s", info.Repo.String(), info.Kind)
	}
}
