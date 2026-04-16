package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type repoEntry struct {
	Kind string
	Name string
	Path string
}

func listCommand(args []string) error {
	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	repo, err := resolveRepoArgs(cfg, args)
	if err != nil {
		return err
	}

	store, err := findRepoStore(cfg, repo)
	if err != nil {
		return err
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.Name == "" {
			fmt.Printf("%-8s %s\n", entry.Kind, entry.Path)
			continue
		}
		fmt.Printf("%-8s %-20s %s\n", entry.Kind, entry.Name, entry.Path)
	}

	return nil
}

func statusCommand(args []string) error {
	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	repo, err := resolveRepoArgs(cfg, args)
	if err != nil {
		return err
	}

	store, err := findRepoStore(cfg, repo)
	if err != nil {
		return err
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		return err
	}

	for i, entry := range entries {
		if i > 0 {
			fmt.Println()
		}

		label := entry.Kind
		if entry.Name != "" {
			label += " " + entry.Name
		}
		fmt.Printf("%s %s\n", label, entry.Path)

		output, err := captureCombinedCommand(entry.Path, "git", "status", "--short", "--branch")
		if err != nil {
			return fmt.Errorf("status for %s: %w", entry.Path, err)
		}

		output = strings.TrimSpace(output)
		if output == "" {
			fmt.Println("(clean)")
			continue
		}
		fmt.Println(output)
	}

	return nil
}

func pruneCommand(args []string) error {
	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	repo, err := resolveRepoArgs(cfg, args)
	if err != nil {
		return err
	}

	store, err := findRepoStore(cfg, repo)
	if err != nil {
		return err
	}
	if !store.Managed {
		return fmt.Errorf("prune is only supported for managed repositories; %s is an existing local directory", store.ContainerPath)
	}

	output, err := captureCombinedCommand("", "git", "--git-dir", store.GitDir, "worktree", "prune", "--verbose")
	if err != nil {
		return fmt.Errorf("prune worktrees for %s: %w", store.ContainerPath, err)
	}

	var removed []string
	for _, dir := range []string{
		filepath.Join(store.ContainerPath, "worktrees"),
		filepath.Join(store.ContainerPath, "PR"),
	} {
		pruned, err := removeEmptyChildren(dir)
		if err != nil {
			return err
		}
		removed = append(removed, pruned...)
	}

	output = strings.TrimSpace(output)
	if output != "" {
		fmt.Println(output)
	}
	for _, path := range removed {
		fmt.Printf("removed empty directory %s\n", path)
	}
	if output == "" && len(removed) == 0 {
		fmt.Println("nothing to prune")
	}

	return nil
}

func loadConfigOnly() (Config, error) {
	cfg, _, err := loadConfig()
	return cfg, err
}

func resolveRepoArgs(cfg Config, args []string) (Repo, error) {
	switch len(args) {
	case 1:
		return resolveOneArg(cfg, args[0])
	case 2:
		return resolveTwoArgs(cfg, args[0], args[1])
	default:
		return Repo{}, fmt.Errorf("usage: gg <command> <owner/repo> or gg <command> <owner> <repo>")
	}
}

func findRepoStore(cfg Config, repo Repo) (RepoStore, error) {
	store := RepoStore{
		ContainerPath: repo.ContainerPath(cfg),
		GitDir:        repo.BarePath(cfg),
		MainPath:      repo.MainPath(cfg),
		Managed:       true,
	}

	containerExists, err := directoryExists(store.ContainerPath)
	if err != nil {
		return RepoStore{}, err
	}
	if !containerExists {
		return RepoStore{}, fmt.Errorf("repository is not available locally: %s", store.ContainerPath)
	}

	classification, err := classifyExistingRepoPath(store)
	if err != nil {
		return RepoStore{}, err
	}
	if classification == "managed" {
		return store, nil
	}

	store.MainPath = store.ContainerPath
	store.GitDir = ""
	store.Managed = false
	return store, nil
}

func listRepoEntries(store RepoStore) ([]repoEntry, error) {
	if !store.Managed {
		return []repoEntry{{
			Kind: "local",
			Path: store.MainPath,
		}}, nil
	}

	var entries []repoEntry
	if exists, err := directoryExists(store.MainPath); err != nil {
		return nil, err
	} else if exists {
		entries = append(entries, repoEntry{
			Kind: "main",
			Name: "main",
			Path: store.MainPath,
		})
	}

	worktrees, err := discoverEntries(filepath.Join(store.ContainerPath, "worktrees"), "worktree")
	if err != nil {
		return nil, err
	}
	prs, err := discoverEntries(filepath.Join(store.ContainerPath, "PR"), "pr")
	if err != nil {
		return nil, err
	}

	entries = append(entries, worktrees...)
	entries = append(entries, prs...)

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

func discoverEntries(root, kind string) ([]repoEntry, error) {
	exists, err := directoryExists(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	var entries []repoEntry
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}

		gitPath := filepath.Join(path, ".git")
		gitExists, err := pathExists(gitPath)
		if err != nil {
			return err
		}
		if !gitExists {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, repoEntry{
			Kind: kind,
			Name: filepath.ToSlash(rel),
			Path: path,
		})
		return filepath.SkipDir
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}

	return entries, nil
}

func removeEmptyChildren(root string) ([]string, error) {
	exists, err := directoryExists(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	var removed []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		paths, err := removeEmptyTree(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		removed = append(removed, paths...)
	}

	sort.Strings(removed)
	return removed, nil
}

func removeEmptyTree(path string) ([]string, error) {
	var removed []string

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", path, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childRemoved, err := removeEmptyTree(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}
		removed = append(removed, childRemoved...)
	}

	entries, err = os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", path, err)
	}
	if len(entries) != 0 {
		return removed, nil
	}

	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove directory %s: %w", path, err)
	}

	removed = append(removed, path)
	return removed, nil
}
