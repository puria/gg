package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var osReadDir = os.ReadDir //nolint:gochecknoglobals

var osRemove = os.Remove //nolint:gochecknoglobals

var filepathWalkDir = filepath.WalkDir //nolint:gochecknoglobals

var stdinIsTerminal = func() bool { //nolint:gochecknoglobals
	info, err := os.Stdin.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

var stdinReader = bufio.NewReader(os.Stdin) //nolint:gochecknoglobals

type repoEntry struct {
	Kind string
	Name string
	Path string
}

type statusOptions struct {
	ShowFiles bool
}

type pruneOptions struct {
	Yes    bool
	DryRun bool
}

type repoStatus struct {
	Branch string
	Files  []string
}

type prunePlan struct {
	Store   RepoStore
	Entries []pruneEntry
}

type pruneEntry struct {
	repoEntry
	Clean     bool
	Removable bool
	Reason    string
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
		// untestable: passthrough — listRepoEntries error is wrapped at its source.
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
	options, repoArgs, err := parseStatusArgs(args)
	if err != nil {
		return err
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	repo, err := resolveRepoArgs(cfg, repoArgs)
	if err != nil {
		return err
	}

	store, err := findRepoStore(cfg, repo)
	if err != nil {
		return err
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		// untestable: passthrough — listRepoEntries error is wrapped at its source.
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

		status, err := readRepoStatus(entry.Path)
		if err != nil {
			return fmt.Errorf("status for %s: %w", entry.Path, err)
		}

		if status.Branch != "" {
			fmt.Printf("branch  %s\n", status.Branch)
		}
		if len(status.Files) == 0 {
			fmt.Println("status  clean")
			continue
		}

		changeLabel := "changes"
		if len(status.Files) == 1 {
			changeLabel = "change"
		}
		fmt.Printf("status  dirty (%d %s)\n", len(status.Files), changeLabel)
		if !options.ShowFiles {
			continue
		}

		for _, file := range status.Files {
			fmt.Printf("  %s\n", file)
		}
	}

	return nil
}

func pruneCommand(args []string) error {
	options, repoArgs, err := parsePruneArgs(args)
	if err != nil {
		return err
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	stores, err := resolvePruneStores(cfg, repoArgs)
	if err != nil {
		return err
	}

	plans := make([]prunePlan, 0, len(stores))
	for _, store := range stores {
		if !store.Managed {
			return fmt.Errorf("prune is only supported for managed repositories; %s is an existing local directory", store.ContainerPath)
		}
		plan, err := buildPrunePlan(store)
		if err != nil {
			return err
		}
		plans = append(plans, plan)
	}

	printPrunePlans(plans)
	if options.DryRun {
		return nil
	}

	if len(repoArgs) == 0 && !options.Yes {
		if !stdinIsTerminal() {
			fmt.Println("rerun with --yes to remove clean entries")
			return nil
		}
		confirmed, err := confirmPrune()
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("nothing removed")
			return nil
		}
	}

	removedAny := false
	for _, plan := range plans {
		removedMerged, removed, output, err := applyPrunePlan(plan)
		if err != nil {
			return err
		}
		if output != "" || len(removedMerged) > 0 || len(removed) > 0 {
			removedAny = true
		}
		printPruneResults(removedMerged, removed, output)
	}
	if !removedAny {
		fmt.Println("nothing to prune")
	}

	return nil
}

func applyPrunePlan(plan prunePlan) ([]repoEntry, []string, string, error) {
	output, err := captureCombinedCommand("", "git", "--git-dir", plan.Store.GitDir, "worktree", "prune", "--verbose")
	if err != nil {
		return nil, nil, "", fmt.Errorf("prune worktrees for %s: %w", plan.Store.ContainerPath, err)
	}

	var removedMerged []repoEntry
	for _, entry := range plan.Entries {
		if !entry.Removable {
			if !entry.Clean {
				fmt.Printf("skipped dirty %s %s %s\n", entry.Kind, entry.Name, entry.Path)
			}
			continue
		}
		if err := removeWorktree(plan.Store, entry.repoEntry); err != nil {
			return nil, nil, "", err
		}
		removedMerged = append(removedMerged, entry.repoEntry)
	}

	var removed []string
	for _, dir := range []string{
		filepath.Join(plan.Store.ContainerPath, "worktrees"),
		filepath.Join(plan.Store.ContainerPath, "PR"),
	} {
		pruned, err := removeEmptyChildren(dir)
		if err != nil {
			// untestable: passthrough — removeEmptyChildren error is wrapped at its source.
			return nil, nil, "", err
		}
		removed = append(removed, pruned...)
	}

	return removedMerged, removed, strings.TrimSpace(output), nil
}

func printPruneResults(removedMerged []repoEntry, removed []string, output string) {
	if output != "" {
		fmt.Println(output)
	}
	for _, entry := range removedMerged {
		fmt.Printf("removed clean %s %s %s\n", entry.Kind, entry.Name, entry.Path)
	}
	for _, path := range removed {
		fmt.Printf("removed empty directory %s\n", path)
	}
}

func buildPrunePlan(store RepoStore) (prunePlan, error) {
	entries, err := listRepoEntries(store)
	if err != nil {
		// untestable: passthrough — listRepoEntries error is wrapped at its source.
		return prunePlan{}, err
	}

	var candidates []repoEntry
	for _, entry := range entries {
		if entry.Kind == "worktree" || entry.Kind == "pr" {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) == 0 {
		return prunePlan{Store: store}, nil
	}

	if hasRemote, _ := repoHasOriginRemote(store.GitDir); hasRemote {
		if err := runCommand("", "git", "--git-dir", store.GitDir, "fetch", "--prune", "origin"); err != nil {
			return prunePlan{}, fmt.Errorf("fetch remote updates before prune: %w", err)
		}
	}

	baseRef, err := defaultBaseRef(store.GitDir)
	if err != nil {
		return prunePlan{}, err
	}

	var plan []pruneEntry
	for _, entry := range candidates {
		clean, err := worktreeClean(entry.Path)
		if err != nil {
			return prunePlan{}, fmt.Errorf("check %s %s status: %w", entry.Kind, entry.Name, err)
		}
		if !clean {
			plan = append(plan, pruneEntry{repoEntry: entry, Clean: false, Reason: "dirty"})
			continue
		}

		removable, reason, err := entryPrunable(entry, baseRef)
		if err != nil {
			return prunePlan{}, err
		}
		plan = append(plan, pruneEntry{repoEntry: entry, Clean: true, Removable: removable, Reason: reason})
	}

	return prunePlan{Store: store, Entries: plan}, nil
}

func worktreeClean(path string) (bool, error) {
	output, err := captureCommand(path, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(output) != "" {
		return false, nil
	}

	stashed, err := worktreeHasStash(path)
	if err != nil {
		return false, err
	}

	return !stashed, nil
}

func worktreeHasStash(path string) (bool, error) {
	stashes, err := captureCommand(path, "git", "stash", "list", "--format=%gs")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(stashes) == "" {
		return false, nil
	}

	branch, err := captureCommand(path, "git", "branch", "--show-current")
	if err != nil {
		return false, err
	}

	head, err := captureCommand(path, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return false, err
	}

	for _, stash := range strings.Split(stashes, "\n") {
		if stashMatchesWorktree(stash, branch, head) {
			return true, nil
		}
	}

	return false, nil
}

func stashMatchesWorktree(stash, branch, head string) bool {
	stash = strings.TrimSpace(stash)
	if stash == "" {
		return false
	}

	if branch != "" {
		if strings.HasPrefix(stash, "WIP on "+branch+":") || strings.HasPrefix(stash, "On "+branch+":") {
			return true
		}
	}

	if head == "" {
		return false
	}

	return strings.Contains(stash, ": "+head+" ") || strings.HasSuffix(stash, ": "+head)
}

func entryMerged(entry repoEntry, baseRef string) (bool, error) {
	if entry.Kind == "pr" {
		merged, ok := githubPRMerged(entry)
		if ok {
			return merged, nil
		}
	}

	merged, err := headMergedInto(entry.Path, baseRef)
	if err != nil {
		return false, fmt.Errorf("check whether %s %s is merged into %s: %w", entry.Kind, entry.Name, baseRef, err)
	}
	return merged, nil
}

func entryPrunable(entry repoEntry, baseRef string) (bool, string, error) {
	synced, err := worktreeHeadSynced(entry.Path)
	if err != nil {
		return false, "", fmt.Errorf("check %s %s unpushed commits: %w", entry.Kind, entry.Name, err)
	}
	if synced {
		return true, "clean and synced", nil
	}

	merged, err := entryMerged(entry, baseRef)
	if err != nil {
		return false, "", err
	}
	if merged {
		return true, "clean and merged", nil
	}

	return false, "has local commits", nil
}

func worktreeHeadSynced(path string) (bool, error) {
	upstream, err := captureCommand(path, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err == nil && strings.TrimSpace(upstream) != "" {
		output, err := captureCommand(path, "git", "rev-list", "--count", "@{upstream}..HEAD")
		if err != nil {
			return false, err
		}

		count, err := strconv.Atoi(strings.TrimSpace(output))
		if err != nil {
			return false, fmt.Errorf("parse unpushed commit count %q: %w", output, err)
		}

		if count == 0 {
			return true, nil
		}
	}

	output, err := captureCommand(path, "git", "for-each-ref", "--format=%(refname)", "--contains", "HEAD", "refs/remotes")
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(output) != "", nil
}

func githubPRMerged(entry repoEntry) (bool, bool) {
	if _, err := execLookPath("gh"); err != nil {
		return false, false
	}

	number, err := strconv.Atoi(entry.Name)
	if err != nil || number <= 0 {
		return false, false
	}

	output, err := captureCommand(entry.Path, "gh", "pr", "view", strconv.Itoa(number), "--json", "mergedAt", "--jq", ".mergedAt")
	if err != nil {
		return false, false
	}

	return strings.TrimSpace(output) != "", true
}

func headMergedInto(path, baseRef string) (bool, error) {
	err := runCommand(path, "git", "merge-base", "--is-ancestor", "HEAD", baseRef)
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, err
}

func removeWorktree(store RepoStore, entry repoEntry) error {
	if err := runCommand("", "git", "--git-dir", store.GitDir, "worktree", "remove", entry.Path); err != nil {
		return fmt.Errorf("remove %s %s: %w", entry.Kind, entry.Name, err)
	}

	if entry.Kind != "worktree" {
		return nil
	}

	branchName := branchNameFromWorktree(entry.Name)
	exists, err := localBranchExists(store.GitDir, branchName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	if err := runCommand("", "git", "--git-dir", store.GitDir, "branch", "-D", branchName); err != nil {
		return fmt.Errorf("delete merged branch %q: %w", branchName, err)
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

func parsePruneArgs(args []string) (pruneOptions, []string, error) {
	var options pruneOptions

	for i, arg := range args {
		switch arg {
		case "--yes", "-y":
			options.Yes = true
		case "--dry-run", "-n":
			options.DryRun = true
		case "--":
			return options, args[i+1:], nil
		default:
			if strings.HasPrefix(arg, "-") {
				return pruneOptions{}, nil, fmt.Errorf("unknown prune flag %q", arg)
			}
			return options, args[i:], nil
		}
	}

	return options, nil, nil
}

func resolvePruneStores(cfg Config, args []string) ([]RepoStore, error) {
	if len(args) > 0 {
		repo, err := resolveRepoArgs(cfg, args)
		if err != nil {
			return nil, err
		}
		store, err := findRepoStore(cfg, repo)
		if err != nil {
			return nil, err
		}
		return []RepoStore{store}, nil
	}

	cwd, err := osGetwd()
	if err != nil {
		return nil, fmt.Errorf("resolve current directory: %w", err)
	}

	return discoverPruneStoresFromPath(cfg, cwd)
}

func discoverPruneStoresFromPath(cfg Config, path string) ([]RepoStore, error) {
	hostRoot := filepath.Join(cfg.Root, cfg.Host)
	rel, err := filepath.Rel(hostRoot, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("current directory must be inside %s or pass a repository argument", hostRoot)
	}

	if rel == "." {
		return discoverManagedStoresUnder(cfg, hostRoot)
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 1 {
		return discoverManagedStoresUnder(cfg, filepath.Join(hostRoot, parts[0]))
	}

	repo := Repo{Owner: parts[0], Name: parts[1]}
	store, err := findRepoStore(cfg, repo)
	if err != nil {
		return nil, err
	}
	return []RepoStore{store}, nil
}

func discoverManagedStoresUnder(cfg Config, root string) ([]RepoStore, error) {
	exists, err := directoryExists(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("source scope does not exist: %s", root)
	}

	var stores []RepoStore
	err = filepathWalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// untestable: WalkDir only forwards OS errors already surfaced by the caller's seam.
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}

		if d.Name() == ".bare" {
			container := filepath.Dir(path)
			rel, err := filepath.Rel(filepath.Join(cfg.Root, cfg.Host), container)
			if err != nil {
				// untestable: container was discovered below the host root.
				return err
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) == 2 {
				stores = append(stores, RepoStore{
					ContainerPath: container,
					GitDir:        path,
					MainPath:      filepath.Join(container, "main"),
					Managed:       true,
					Repo:          Repo{Owner: parts[0], Name: parts[1]},
				})
			}
			return filepath.SkipDir
		}

		if d.Name() == "main" || d.Name() == "worktrees" || d.Name() == "PR" || strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan managed repositories under %s: %w", root, err)
	}

	sort.Slice(stores, func(i, j int) bool {
		return stores[i].Repo.String() < stores[j].Repo.String()
	})
	if len(stores) == 0 {
		return nil, fmt.Errorf("no managed repositories found under %s", root)
	}

	return stores, nil
}

func printPrunePlans(plans []prunePlan) {
	if len(plans) == 0 {
		return
	}

	for i, plan := range plans {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s %s\n", plan.Store.Repo.String(), plan.Store.ContainerPath)
		if len(plan.Entries) == 0 {
			fmt.Println("  no PR or worktree checkouts")
			continue
		}
		for _, entry := range plan.Entries {
			marker := "red"
			if entry.Removable {
				marker = "green"
			} else if entry.Clean {
				marker = "yellow"
			}
			fmt.Printf("  %-6s %-8s %-20s %s\n", marker, entry.Kind, entry.Name, entry.Reason)
		}
	}
}

func confirmPrune() (bool, error) {
	fmt.Print("remove green entries? [y/N] ")
	answer, err := stdinReader.ReadString('\n')
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func findRepoStore(cfg Config, repo Repo) (RepoStore, error) {
	store := RepoStore{
		ContainerPath: repo.ContainerPath(cfg),
		GitDir:        repo.BarePath(cfg),
		MainPath:      repo.MainPath(cfg),
		Managed:       true,
		Repo:          repo,
	}

	containerExists, err := directoryExists(store.ContainerPath)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return RepoStore{}, err
	}
	if !containerExists {
		return RepoStore{}, fmt.Errorf("repository is not available locally: %s", store.ContainerPath)
	}

	classification, err := classifyExistingRepoPath(store)
	if err != nil {
		// untestable: passthrough — classifyExistingRepoPath error is wrapped at its source.
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
		// untestable: passthrough — directoryExists error is wrapped at its source.
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
		// untestable: passthrough — discoverEntries error is wrapped at its source.
		return nil, err
	}
	prs, err := discoverEntries(filepath.Join(store.ContainerPath, "PR"), "pr")
	if err != nil {
		// untestable: passthrough — discoverEntries error is wrapped at its source.
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
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	var entries []repoEntry
	err = filepathWalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// untestable: passthrough — WalkDir only forwards OS errors already surfaced by the caller's seam.
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
			// untestable: passthrough — pathExists error is wrapped at its source.
			return err
		}
		if !gitExists {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			// untestable: WalkDir only invokes this callback with paths descended from root,
			// and both root and path are absolute — filepath.Rel cannot fail here.
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
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	var removed []string
	entries, err := osReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		paths, err := removeEmptyTree(filepath.Join(root, entry.Name()))
		if err != nil {
			// untestable: passthrough — removeEmptyTree error is wrapped at its source.
			return nil, err
		}
		removed = append(removed, paths...)
	}

	sort.Strings(removed)
	return removed, nil
}

func removeEmptyTree(path string) ([]string, error) {
	var removed []string

	entries, err := osReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", path, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childRemoved, err := removeEmptyTree(filepath.Join(path, entry.Name()))
		if err != nil {
			// untestable: passthrough — recursive removeEmptyTree error is wrapped at its source.
			return nil, err
		}
		removed = append(removed, childRemoved...)
	}

	entries, err = osReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", path, err)
	}
	if len(entries) != 0 {
		return removed, nil
	}

	if err := osRemove(path); err != nil {
		return nil, fmt.Errorf("remove directory %s: %w", path, err)
	}

	removed = append(removed, path)
	return removed, nil
}

func parseStatusArgs(args []string) (statusOptions, []string, error) {
	var options statusOptions

	for i, arg := range args {
		switch arg {
		case "--files", "-f":
			options.ShowFiles = true
		case "--":
			return options, args[i+1:], nil
		default:
			if strings.HasPrefix(arg, "-") {
				return statusOptions{}, nil, fmt.Errorf("unknown status flag %q", arg)
			}
			return options, args[i:], nil
		}
	}

	return options, nil, fmt.Errorf("usage: gg status [--files] <owner/repo> or gg status [--files] <owner> <repo>")
}

func readRepoStatus(path string) (repoStatus, error) {
	output, err := captureCombinedCommand(path, "git", "status", "--porcelain=v1", "--branch")
	if err != nil {
		return repoStatus{}, err
	}

	return parseRepoStatus(output), nil
}

func parseRepoStatus(output string) repoStatus {
	var status repoStatus

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			status.Branch = strings.TrimPrefix(line, "## ")
			continue
		}
		status.Files = append(status.Files, line)
	}

	return status
}
