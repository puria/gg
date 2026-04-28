package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var execCommand = exec.Command //nolint:gochecknoglobals

var execLookPath = exec.LookPath //nolint:gochecknoglobals

var osStat = os.Stat //nolint:gochecknoglobals

var osMkdirAll = os.MkdirAll //nolint:gochecknoglobals

type Repo struct {
	Owner string
	Name  string
}

type RepoStore struct {
	ContainerPath string
	GitDir        string
	MainPath      string
	Managed       bool
	Repo          Repo
}

type Request struct {
	Owner  string
	Repo   Repo
	Target Target
}

type TargetKind int

const (
	TargetRepo TargetKind = iota
	TargetOwner
	TargetWorktree
	TargetPR
)

type Target struct {
	Kind         TargetKind
	WorktreeName string
	PRNumber     int
}

func (r Repo) String() string {
	return r.Owner + "/" + r.Name
}

func (r Repo) ContainerPath(cfg Config) string {
	return filepath.Join(cfg.Root, cfg.Host, r.Owner, r.Name)
}

func (r Repo) BarePath(cfg Config) string {
	return filepath.Join(r.ContainerPath(cfg), ".bare")
}

func (r Repo) MainPath(cfg Config) string {
	return filepath.Join(r.ContainerPath(cfg), "main")
}

func (r Repo) CloneURL(cfg Config) string {
	return "https://" + cfg.Host + "/" + r.Owner + "/" + r.Name + ".git"
}

func ensureRequest(cfg Config, request Request) (string, error) {
	if request.Target.Kind == TargetOwner {
		return ensureOwnerPath(cfg, request.Owner)
	}

	store, err := ensureRepoStore(cfg, request.Repo)
	if err != nil {
		return "", err
	}

	switch request.Target.Kind {
	case TargetRepo:
		return store.MainPath, nil
	case TargetWorktree:
		return ensureWorktree(store, request.Target.WorktreeName)
	case TargetPR:
		return ensurePRWorktree(store, request.Target.PRNumber)
	default:
		return "", fmt.Errorf("unsupported target kind %d", request.Target.Kind)
	}
}

func ensureRepoStore(cfg Config, repo Repo) (RepoStore, error) {
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
	if containerExists {
		classification, err := classifyExistingRepoPath(store)
		if err != nil {
			// untestable: passthrough — classifyExistingRepoPath error is wrapped at its source.
			return RepoStore{}, err
		}
		switch classification {
		case "managed":
			break
		case "local":
			store.MainPath = store.ContainerPath
			store.GitDir = ""
			store.Managed = false
			return store, nil
		}
	}

	bareExists, err := directoryExists(store.GitDir)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return RepoStore{}, err
	}
	freshClone := false
	if !bareExists {
		if err := osMkdirAll(store.ContainerPath, 0o755); err != nil {
			return RepoStore{}, fmt.Errorf("create repository container: %w", err)
		}

		if err := runCommand("", "git", "clone", "--bare", "--recursive", repo.CloneURL(cfg), store.GitDir); err != nil {
			return RepoStore{}, fmt.Errorf("clone %s: %w", repo.String(), err)
		}
		freshClone = true
	}

	mainExists, err := directoryExists(store.MainPath)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return RepoStore{}, err
	}
	hasRefs := false
	if !mainExists || gitDirInitialized(store.GitDir) {
		hasRefs, err = ensureRemoteTrackingRefs(store.GitDir, repo, freshClone)
		if err != nil {
			return RepoStore{}, err
		}
	}
	if !mainExists {
		if !hasRefs {
			args := []string{"--git-dir", store.GitDir, "worktree", "add", "--orphan", "-b", "main", store.MainPath}
			if err := runCommand("", "git", args...); err != nil {
				return RepoStore{}, fmt.Errorf("create main worktree for empty repository %s: %w", repo.String(), err)
			}
		} else {
			defaultBranch, baseRef, err := defaultBranchRef(store.GitDir)
			if err != nil {
				return RepoStore{}, err
			}

			branchExists, err := localBranchExists(store.GitDir, defaultBranch)
			if err != nil {
				return RepoStore{}, err
			}

			args := []string{"--git-dir", store.GitDir, "worktree", "add"}
			if branchExists {
				args = append(args, store.MainPath, defaultBranch)
			} else {
				args = append(args, "-b", defaultBranch, store.MainPath, baseRef)
			}

			if err := runCommand("", "git", args...); err != nil {
				return RepoStore{}, fmt.Errorf("create main worktree for %s: %w", repo.String(), err)
			}
		}

		if err := finalizeWorktreeSetup(store.MainPath, repo); err != nil {
			return RepoStore{}, err
		}
	}

	if err := ensureDefaultBranchUpstream(store, hasRefs); err != nil {
		return RepoStore{}, err
	}

	return store, nil
}

func resolveRequest(cfg Config, args []string) (Request, error) {
	switch len(args) {
	case 1:
		repo, err := resolveOneArg(cfg, args[0])
		if err == nil {
			return Request{Repo: repo, Target: Target{Kind: TargetRepo}}, nil
		}

		owner, ownerErr := resolveOwner(cfg, args[0])
		if ownerErr == nil {
			return Request{Owner: owner, Target: Target{Kind: TargetOwner}}, nil
		}

		return Request{}, err
	case 2:
		if repo, ok, err := tryResolveStandaloneRepo(cfg, args[0]); err != nil {
			return Request{}, err
		} else if ok {
			target, err := parseTarget(args[1])
			if err != nil {
				return Request{}, err
			}
			return Request{Repo: repo, Target: target}, nil
		}

		repo, err := resolveTwoArgs(cfg, args[0], args[1])
		if err != nil {
			return Request{}, err
		}
		return Request{Repo: repo, Target: Target{Kind: TargetRepo}}, nil
	case 3:
		repo, err := resolveTwoArgs(cfg, args[0], args[1])
		if err != nil {
			return Request{}, err
		}
		target, err := parseTarget(args[2])
		if err != nil {
			return Request{}, err
		}
		return Request{Repo: repo, Target: target}, nil
	default:
		return Request{}, errors.New("usage: gg <owner/repo>, gg <owner> <repo>, gg <repo> <worktree>, or gg <repo> <pr-number>")
	}
}

func resolveOneArg(cfg Config, raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	spec, err := expandAlias(cfg.Aliases, raw, true)
	if err != nil {
		return Repo{}, err
	}

	owner, repo, err := splitRepoSpec(spec)
	if err != nil {
		return Repo{}, err
	}

	if spec != raw {
		owner = expandSegmentAlias(cfg.Aliases, owner)
		repo = expandSegmentAlias(cfg.Aliases, repo)
		return resolveCombinedAlias(cfg, owner, repo)
	}

	owner = expandSegmentAlias(cfg.Aliases, owner)
	repo = expandSegmentAlias(cfg.Aliases, repo)

	return resolveCombinedAlias(cfg, owner, repo)
}

func resolveTwoArgs(cfg Config, rawOwner, rawRepo string) (Repo, error) {
	owner, err := expandAlias(cfg.Aliases, strings.TrimSpace(rawOwner), false)
	if err != nil {
		return Repo{}, err
	}

	repo, err := expandAlias(cfg.Aliases, strings.TrimSpace(rawRepo), false)
	if err != nil {
		return Repo{}, err
	}

	return resolveCombinedAlias(cfg, owner, repo)
}

func resolveCombinedAlias(cfg Config, owner, repo string) (Repo, error) {
	spec, err := expandAlias(cfg.Aliases, owner+"/"+repo, true)
	if err != nil {
		return Repo{}, err
	}

	finalOwner, finalRepo, err := splitRepoSpec(spec)
	if err != nil {
		return Repo{}, err
	}

	return canonicalizeRepo(cfg, Repo{
		Owner: finalOwner,
		Name:  finalRepo,
	}), nil
}

func resolveOwner(cfg Config, raw string) (string, error) {
	owner, err := expandAlias(cfg.Aliases, strings.TrimSpace(raw), false)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(owner) == "" {
		// untestable: expandAlias rejects empty input; this guard is defensive.
		return "", errors.New("owner cannot be empty")
	}

	return canonicalizeOwner(cfg, owner), nil
}

func tryResolveStandaloneRepo(cfg Config, raw string) (Repo, bool, error) {
	repo, err := resolveOneArg(cfg, raw)
	if err == nil {
		return repo, true, nil
	}

	if strings.Contains(strings.TrimSpace(raw), "/") {
		return Repo{}, false, err
	}

	return Repo{}, false, nil
}

func parseTarget(raw string) (Target, error) {
	prNumber, ok := parsePRNumber(raw)
	if ok {
		return Target{
			Kind:     TargetPR,
			PRNumber: prNumber,
		}, nil
	}

	worktreeName := strings.TrimSpace(raw)
	if worktreeName == "" {
		return Target{}, errors.New("worktree name cannot be empty")
	}

	return Target{
		Kind:         TargetWorktree,
		WorktreeName: worktreeName,
	}, nil
}

func canonicalizeRepo(cfg Config, repo Repo) Repo {
	repo.Owner = canonicalizeOwner(cfg, repo.Owner)
	repo.Name = canonicalizeSegment(filepath.Join(cfg.Root, cfg.Host, repo.Owner), repo.Name)
	return repo
}

func canonicalizeOwner(cfg Config, owner string) string {
	return canonicalizeSegment(filepath.Join(cfg.Root, cfg.Host), owner)
}

func canonicalizeSegment(parent, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		return name
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.EqualFold(entry.Name(), name) {
			return entry.Name()
		}
	}

	return name
}

func parsePRNumber(raw string) (int, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}

	if strings.HasPrefix(value, "#") {
		value = strings.TrimSpace(value[1:])
	}

	if value == "" {
		return 0, false
	}

	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
	}

	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, false
	}

	return number, true
}

func ensureWorktree(store RepoStore, worktreeName string) (string, error) {
	if !store.Managed {
		return "", fmt.Errorf("worktrees are only supported for managed repositories; %s is an existing local directory", store.ContainerPath)
	}

	path, err := resolveNestedRepoPath(store.ContainerPath, "worktrees", worktreeName)
	if err != nil {
		return "", err
	}

	exists, err := directoryExists(path)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return "", err
	}

	if hasRemote, _ := repoHasOriginRemote(store.GitDir); hasRemote {
		if err := runCommand("", "git", "--git-dir", store.GitDir, "fetch", "origin"); err != nil {
			return "", fmt.Errorf("fetch remote updates: %w", err)
		}
	}

	if exists {
		return path, nil
	}

	if err := osMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create worktree parent directory: %w", err)
	}

	branchName := branchNameFromWorktree(worktreeName)
	if err := validateBranchName(branchName); err != nil {
		return "", err
	}

	branchExists, err := localBranchExists(store.GitDir, branchName)
	if err != nil {
		return "", err
	}

	args := []string{"--git-dir", store.GitDir, "worktree", "add"}
	if branchExists {
		args = append(args, path, branchName)
	} else {
		hasRefs, err := repoHasRefs(store.GitDir)
		if err != nil {
			return "", err
		}
		if !hasRefs {
			args = append(args, "--orphan", "-b", branchName, path)
		} else {
			baseRef, err := defaultBaseRef(store.GitDir)
			if err != nil {
				return "", err
			}
			args = append(args, "-b", branchName, path, baseRef)
		}
	}

	if err := runCommand("", "git", args...); err != nil {
		return "", fmt.Errorf("create worktree %q: %w", worktreeName, err)
	}

	if err := finalizeWorktreeSetup(path, store.Repo); err != nil {
		return "", err
	}

	return path, nil
}

func ensurePRWorktree(store RepoStore, prNumber int) (string, error) {
	if !store.Managed {
		return "", fmt.Errorf("PR checkouts are only supported for managed repositories; %s is an existing local directory", store.ContainerPath)
	}

	path, err := resolveNestedRepoPath(store.ContainerPath, "PR", strconv.Itoa(prNumber))
	if err != nil {
		// untestable: strconv.Itoa of a positive PR number always produces a valid path segment.
		return "", err
	}

	exists, err := directoryExists(path)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return "", err
	}

	if hasRemote, _ := repoHasOriginRemote(store.GitDir); hasRemote {
		if err := runCommand("", "git", "--git-dir", store.GitDir, "fetch", "origin"); err != nil {
			return "", fmt.Errorf("fetch remote updates: %w", err)
		}
	}

	if exists {
		return path, nil
	}

	if err := osMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create PR parent directory: %w", err)
	}

	hasRefs, err := repoHasRefs(store.GitDir)
	if err != nil {
		return "", err
	}
	if !hasRefs {
		return "", fmt.Errorf("cannot checkout PR %d because %s has no commits yet", prNumber, store.ContainerPath)
	}

	baseRef, err := defaultBaseRef(store.GitDir)
	if err != nil {
		return "", err
	}

	if err := runCommand("", "git", "--git-dir", store.GitDir, "worktree", "add", "--detach", path, baseRef); err != nil {
		return "", fmt.Errorf("create PR worktree %d: %w", prNumber, err)
	}

	if err := runCommand(path, "gh", "pr", "checkout", strconv.Itoa(prNumber), "--detach"); err != nil {
		return "", fmt.Errorf("checkout PR %d: %w", prNumber, err)
	}

	if err := finalizeWorktreeSetup(path, store.Repo); err != nil {
		return "", err
	}

	return path, nil
}

func expandAlias(aliases map[string]string, key string, allowSlash bool) (string, error) {
	current := strings.TrimSpace(key)
	if current == "" {
		return "", errors.New("empty repository alias")
	}

	seen := map[string]struct{}{}
	for {
		if _, exists := seen[current]; exists {
			return "", fmt.Errorf("alias cycle detected at %q", current)
		}
		seen[current] = struct{}{}

		next, ok := aliases[current]
		if !ok {
			break
		}
		current = strings.TrimSpace(next)
		if current == "" {
			return "", fmt.Errorf("alias %q resolves to an empty value", key)
		}
	}

	if !allowSlash && strings.Contains(current, "/") {
		return "", fmt.Errorf("alias %q resolves to %q, but this position expects a single path segment", key, current)
	}

	return current, nil
}

func expandSegmentAlias(aliases map[string]string, key string) string {
	value, err := expandAlias(aliases, key, false)
	if err != nil {
		return strings.TrimSpace(key)
	}
	return value
}

func splitRepoSpec(spec string) (string, string, error) {
	spec = strings.Trim(strings.TrimSpace(spec), "/")
	if strings.Count(spec, "/") != 1 {
		return "", "", fmt.Errorf("repository must be in the form owner/repo, got %q", spec)
	}

	// SplitN produces two non-empty parts: Trim("/") removed leading/trailing
	// slashes above, and Count == 1 guarantees exactly one slash in the middle.
	parts := strings.SplitN(spec, "/", 2)
	return parts[0], parts[1], nil
}

func resolveNestedRepoPath(repoPath, group, name string) (string, error) {
	cleanName, err := cleanRelativePath(name)
	if err != nil {
		return "", err
	}

	return filepath.Join(repoPath, group, cleanName), nil
}

func cleanRelativePath(name string) (string, error) {
	value := filepath.FromSlash(strings.TrimSpace(name))
	if value == "" {
		return "", errors.New("path name cannot be empty")
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("path name must be relative: %q", name)
	}

	clean := filepath.Clean(value)
	if clean == "." || clean == ".." {
		return "", fmt.Errorf("path name must not escape the repository: %q", name)
	}
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path name must not escape the repository: %q", name)
	}

	return clean, nil
}

func branchNameFromWorktree(name string) string {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(name)))
	return filepath.ToSlash(clean)
}

func directoryExists(path string) (bool, error) {
	info, err := osStat(path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return false, fmt.Errorf("%s exists but is not a directory", path)
		}
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
}

func pathExists(path string) (bool, error) {
	_, err := osStat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
}

func ensureOwnerPath(cfg Config, owner string) (string, error) {
	path := filepath.Join(cfg.Root, cfg.Host, owner)
	exists, err := directoryExists(path)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return "", err
	}
	if exists {
		return path, nil
	}

	if err := osMkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create owner directory %s: %w", path, err)
	}

	return path, nil
}

func validateBranchName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name cannot be empty")
	}

	if err := runCommand("", "git", "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("invalid branch name %q: %w", name, err)
	}

	return nil
}

func localBranchExists(gitDir, branchName string) (bool, error) {
	err := runCommand("", "git", "--git-dir", gitDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branchName)
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, fmt.Errorf("check branch %q: %w", branchName, err)
}

func defaultBaseRef(gitDir string) (string, error) {
	_, ref, err := defaultBranchRef(gitDir)
	return ref, err
}

func ensureRemoteTrackingRefs(gitDir string, repo Repo, assumeOriginRemote bool) (bool, error) {
	hasRefs, err := repoHasRefs(gitDir)
	if err != nil {
		return false, err
	}

	hasOriginRemote := assumeOriginRemote
	if !hasOriginRemote {
		hasOriginRemote, err = repoHasOriginRemote(gitDir)
		if err != nil {
			return false, err
		}
	}
	if !hasOriginRemote {
		return hasRefs, nil
	}

	// Bare clones keep remote heads as local branches, so configure a normal
	// remote-tracking refspec and backfill refs when they're still missing.
	if err := runCommand("", "git", "--git-dir", gitDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return false, fmt.Errorf("configure remote tracking for %s: %w", repo.String(), err)
	}

	if !hasRefs {
		return false, nil
	}

	_, ref, err := defaultBranchRef(gitDir)
	if err == nil && strings.HasPrefix(ref, "origin/") {
		return true, nil
	}

	if err := runCommand("", "git", "--git-dir", gitDir, "fetch", "origin"); err != nil {
		return false, fmt.Errorf("fetch remote tracking refs for %s: %w", repo.String(), err)
	}

	return true, nil
}

func repoHasRefs(gitDir string) (bool, error) {
	output, err := captureCommand("", "git", "--git-dir", gitDir, "for-each-ref", "--count=1", "--format=%(refname)")
	if err != nil {
		return false, fmt.Errorf("check refs for %s: %w", gitDir, err)
	}

	return strings.TrimSpace(output) != "", nil
}

func ensureDefaultBranchUpstream(store RepoStore, hasRefs bool) error {
	if !hasRefs {
		return nil
	}

	defaultBranch, baseRef, err := defaultBranchRef(store.GitDir)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(baseRef, "origin/") {
		return nil
	}

	exists, err := localBranchExists(store.GitDir, defaultBranch)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	if err := runCommand(store.MainPath, "git", "branch", "--set-upstream-to="+baseRef, defaultBranch); err != nil {
		return fmt.Errorf("set upstream for %s in %s: %w", defaultBranch, store.MainPath, err)
	}

	return nil
}

func repoHasOriginRemote(gitDir string) (bool, error) {
	_, err := captureCommand("", "git", "--git-dir", gitDir, "config", "--get", "remote.origin.url")
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, fmt.Errorf("check origin remote for %s: %w", gitDir, err)
}

func gitDirInitialized(gitDir string) bool {
	exists, err := pathExists(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return false
	}

	return exists
}

func updateSubmodules(worktreePath string) error {
	if err := runCommand(worktreePath, "git", "submodule", "update", "--init", "--recursive"); err != nil {
		return fmt.Errorf("update submodules in %s: %w", worktreePath, err)
	}

	return nil
}

func configureGitHubDefaultRepo(worktreePath string, repo Repo) error {
	if repo == (Repo{}) {
		return nil
	}

	if _, err := execLookPath("gh"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("find gh CLI: %w", err)
	}

	if err := runCommand(worktreePath, "gh", "repo", "set-default", repo.String()); err != nil {
		return fmt.Errorf("set gh default repo for %s in %s: %w", repo.String(), worktreePath, err)
	}

	return nil
}

func finalizeWorktreeSetup(worktreePath string, repo Repo) error {
	if err := updateSubmodules(worktreePath); err != nil {
		return err
	}

	if err := configureGitHubDefaultRepo(worktreePath, repo); err != nil {
		return err
	}

	if err := setupMiseTooling(worktreePath); err != nil {
		return err
	}

	return nil
}

func setupMiseTooling(worktreePath string) error {
	configPaths, err := findMiseConfigPaths(worktreePath)
	if err != nil {
		// untestable: passthrough — findMiseConfigPaths error is wrapped at its source.
		return err
	}
	if len(configPaths) == 0 {
		return nil
	}

	for _, configPath := range configPaths {
		if err := runCommand(worktreePath, "mise", "trust", configPath); err != nil {
			return fmt.Errorf("trust mise config %s: %w", configPath, err)
		}
	}

	if err := runCommand(worktreePath, "mise", "install"); err != nil {
		return fmt.Errorf("install mise tools in %s: %w", worktreePath, err)
	}

	return nil
}

func findMiseConfigPaths(worktreePath string) ([]string, error) {
	var configPaths []string

	for _, name := range []string{"mise.toml", ".mise.toml"} {
		path := filepath.Join(worktreePath, name)
		exists, err := pathExists(path)
		if err != nil {
			// untestable: passthrough — pathExists error is wrapped at its source.
			return nil, err
		}
		if exists {
			configPaths = append(configPaths, path)
		}
	}

	return configPaths, nil
}

func defaultBranchRef(gitDir string) (string, string, error) {
	if ref, err := captureCommand("", "git", "--git-dir", gitDir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return strings.TrimPrefix(ref, "origin/"), ref, nil
	}

	candidates := []struct {
		branch string
		ref    string
	}{
		{branch: "main", ref: "origin/main"},
		{branch: "master", ref: "origin/master"},
		{branch: "main", ref: "main"},
		{branch: "master", ref: "master"},
	}

	for _, candidate := range candidates {
		if err := runCommand("", "git", "--git-dir", gitDir, "rev-parse", "--verify", "--quiet", candidate.ref); err == nil {
			return candidate.branch, candidate.ref, nil
		}
	}

	return "", "", fmt.Errorf("could not determine default branch for %s", gitDir)
}

func classifyExistingRepoPath(store RepoStore) (string, error) {
	bareExists, err := directoryExists(store.GitDir)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return "", err
	}
	mainExists, err := directoryExists(store.MainPath)
	if err != nil {
		// untestable: passthrough — directoryExists error is wrapped at its source.
		return "", err
	}
	if bareExists || mainExists {
		return "managed", nil
	}

	entries, err := osReadDir(store.ContainerPath)
	if err != nil {
		return "", fmt.Errorf("read repository directory %s: %w", store.ContainerPath, err)
	}
	if len(entries) > 0 {
		return "local", nil
	}

	return "empty", nil
}

func runCommand(dir, name string, args ...string) error {
	cmd := execCommand(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func captureCommand(dir, name string, args ...string) (string, error) {
	cmd := execCommand(name, args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func captureCombinedCommand(dir, name string, args ...string) (string, error) {
	cmd := execCommand(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}
