package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("GG_CONFIG", filepath.Join(t.TempDir(), "config"))
	t.Setenv("HOME", "/tmp/gg-home")

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if cfg.Root != "/tmp/gg-home/src" {
		t.Fatalf("cfg.Root = %q, want %q", cfg.Root, "/tmp/gg-home/src")
	}

	if cfg.Host != "github.com" {
		t.Fatalf("cfg.Host = %q, want github.com", cfg.Host)
	}
}

func TestLoadConfigFromXDGPath(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	t.Setenv("HOME", "/tmp/gg-home")

	path := filepath.Join(xdgHome, "gg", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data := `{
  "root": "~/work/src",
  "aliases": {
    "f": "ForkbombEu",
    "credim": "credimi",
    "fc": "ForkbombEu/credimi"
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, gotPath, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if gotPath != path {
		t.Fatalf("config path = %q, want %q", gotPath, path)
	}

	if cfg.Root != "/tmp/gg-home/work/src" {
		t.Fatalf("cfg.Root = %q, want %q", cfg.Root, "/tmp/gg-home/work/src")
	}

	if cfg.Aliases["f"] != "ForkbombEu" {
		t.Fatalf("owner alias missing after load")
	}
}

func TestResolveRepoWithSegmentAliases(t *testing.T) {
	cfg := Config{
		Root: "/tmp/src",
		Host: "github.com",
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f", "credim"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}
}

func TestResolveRepoWithSlashAndFullAlias(t *testing.T) {
	cfg := Config{
		Root: "/tmp/src",
		Host: "github.com",
		Aliases: map[string]string{
			"f":         "ForkbombEu",
			"credim":    "credimi",
			"fc":        "ForkbombEu/credimi",
			"f/credimi": "ForkbombEu/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f/credim"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	request, err = resolveRequest(cfg, []string{"fc"})
	if err != nil {
		t.Fatalf("resolveRequest() with full alias error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo from full alias = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}
}

func TestResolveRequestSupportsOwnerPath(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f": "ForkbombEu",
		},
	}

	request, err := resolveRequest(cfg, []string{"ForkbombEu"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Target.Kind != TargetOwner || request.Owner != "ForkbombEu" {
		t.Fatalf("request = %#v, want owner ForkbombEu", request)
	}

	request, err = resolveRequest(cfg, []string{"f"})
	if err != nil {
		t.Fatalf("resolveRequest() with alias error = %v", err)
	}

	if request.Target.Kind != TargetOwner || request.Owner != "ForkbombEu" {
		t.Fatalf("request = %#v, want owner alias ForkbombEu", request)
	}
}

func TestResolveRequestCanonicalizesExistingRepoCase(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}

	existing := filepath.Join(root, "github.com", "ForkbombEu", "credimi")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	request, err := resolveRequest(cfg, []string{"forkbombeu/credimi"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.Owner != "ForkbombEu" || request.Repo.Name != "credimi" {
		t.Fatalf("request.Repo = %#v, want ForkbombEu/credimi", request.Repo)
	}
}

func TestResolveRequestCanonicalizesExistingOwnerCase(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}

	if err := os.MkdirAll(filepath.Join(root, "github.com", "ForkbombEu"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	request, err := resolveRequest(cfg, []string{"forkbombeu"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Target.Kind != TargetOwner || request.Owner != "ForkbombEu" {
		t.Fatalf("request = %#v, want owner ForkbombEu", request)
	}
}

func TestResolveTwoArgsRejectsSlashAliasInSegmentPosition(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f": "ForkbombEu/credimi",
		},
	}

	if _, err := resolveTwoArgs(cfg, "f", "repo"); err == nil {
		t.Fatal("resolveTwoArgs() error = nil, want error")
	}
}

func TestAliasCommandWritesConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gg", "config")
	t.Setenv("GG_CONFIG", configPath)
	t.Setenv("HOME", "/tmp/gg-home")

	if err := aliasCommand([]string{"ForkbombEu/credimi", "credim"}); err != nil {
		t.Fatalf("aliasCommand() error = %v", err)
	}

	cfg, gotPath, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if gotPath != configPath {
		t.Fatalf("config path = %q, want %q", gotPath, configPath)
	}

	if cfg.Aliases["credim"] != "ForkbombEu/credimi" {
		t.Fatalf("cfg.Aliases[credim] = %q, want %q", cfg.Aliases["credim"], "ForkbombEu/credimi")
	}
}

func TestResolveRequestKeepsOwnerRepoTwoArgForm(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f", "credim"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("request.Repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	if request.Target.Kind != TargetRepo {
		t.Fatalf("request.Target.Kind = %v, want TargetRepo", request.Target.Kind)
	}
}

func TestResolveRequestTreatsRepoAliasPlusNameAsWorktree(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"credimi": "ForkbombEu/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"credimi", "newworktree"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("request.Repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	if request.Target.Kind != TargetWorktree || request.Target.WorktreeName != "newworktree" {
		t.Fatalf("request.Target = %#v, want worktree newworktree", request.Target)
	}
}

func TestResolveRequestTreatsRepoAliasPlusPRAsPRCheckout(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"credimi": "ForkbombEu/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"credimi", "99"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Target.Kind != TargetPR || request.Target.PRNumber != 99 {
		t.Fatalf("request.Target = %#v, want PR 99", request.Target)
	}

	request, err = resolveRequest(cfg, []string{"credimi", "#42"})
	if err != nil {
		t.Fatalf("resolveRequest() with #42 error = %v", err)
	}

	if request.Target.Kind != TargetPR || request.Target.PRNumber != 42 {
		t.Fatalf("request.Target = %#v, want PR 42", request.Target)
	}
}

func TestResolveRequestSupportsOwnerRepoPlusTarget(t *testing.T) {
	cfg := Config{
		Aliases: map[string]string{
			"f":      "ForkbombEu",
			"credim": "credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"f", "credim", "feature-x"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("request.Repo = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
	}

	if request.Target.Kind != TargetWorktree || request.Target.WorktreeName != "feature-x" {
		t.Fatalf("request.Target = %#v, want worktree feature-x", request.Target)
	}
}

func TestResolveNestedRepoPath(t *testing.T) {
	repoPath := "/tmp/src/github.com/ForkbombEu/credimi"

	path, err := resolveNestedRepoPath(repoPath, "worktrees", "feature/test")
	if err != nil {
		t.Fatalf("resolveNestedRepoPath() error = %v", err)
	}

	want := filepath.Join(repoPath, "worktrees", "feature", "test")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestResolveNestedRepoPathRejectsEscape(t *testing.T) {
	if _, err := resolveNestedRepoPath("/tmp/repo", "worktrees", "../oops"); err == nil {
		t.Fatal("resolveNestedRepoPath() error = nil, want error")
	}
}

func TestEnsureOwnerPath(t *testing.T) {
	cfg := Config{
		Root: t.TempDir(),
		Host: "github.com",
	}

	path, err := ensureOwnerPath(cfg, "ForkbombEu")
	if err != nil {
		t.Fatalf("ensureOwnerPath() error = %v", err)
	}

	want := filepath.Join(cfg.Root, "github.com", "ForkbombEu")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestEnsureRepoStoreUsesExistingLocalDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}
	repo := Repo{Owner: "puria", Name: "gg"}
	localPath := filepath.Join(root, "github.com", "puria", "gg")

	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, "README.md"), []byte("local only\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := ensureRepoStore(cfg, repo)
	if err != nil {
		t.Fatalf("ensureRepoStore() error = %v", err)
	}

	if store.Managed {
		t.Fatal("store.Managed = true, want false for existing local directory")
	}
	if store.MainPath != localPath {
		t.Fatalf("store.MainPath = %q, want %q", store.MainPath, localPath)
	}
}

func TestEnsureRequestReturnsExistingLocalDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root: root,
		Host: "github.com",
	}
	localPath := filepath.Join(root, "github.com", "puria", "gg")

	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPath, ".codex"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	path, err := ensureRequest(cfg, Request{
		Repo: Repo{Owner: "puria", Name: "gg"},
		Target: Target{
			Kind: TargetRepo,
		},
	})
	if err != nil {
		t.Fatalf("ensureRequest() error = %v", err)
	}

	if path != localPath {
		t.Fatalf("path = %q, want %q", path, localPath)
	}
}

func TestShellInitEmbedsExecutablePath(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return "/tmp/bin/gg", nil
	}
	defer func() {
		executablePath = oldExecutablePath
	}()

	script, err := shellInit("fish")
	if err != nil {
		t.Fatalf("shellInit() error = %v", err)
	}

	if !strings.Contains(script, `/tmp/bin/gg`) {
		t.Fatalf("shell script does not embed executable path: %q", script)
	}
	if strings.Contains(script, "command gg") {
		t.Fatalf("shell script still references command gg: %q", script)
	}
}

func TestRepoHasRefs(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "repo.git")

	if err := exec.Command("git", "init", "--bare", gitDir).Run(); err != nil {
		t.Fatalf("git init --bare error = %v", err)
	}

	hasRefs, err := repoHasRefs(gitDir)
	if err != nil {
		t.Fatalf("repoHasRefs() error = %v", err)
	}
	if hasRefs {
		t.Fatal("repoHasRefs() = true, want false for empty bare repo")
	}

	worktree := filepath.Join(root, "wt")
	if err := exec.Command("git", "--git-dir", gitDir, "worktree", "add", "--orphan", "-b", "main", worktree).Run(); err != nil {
		t.Fatalf("git worktree add error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "config", "user.name", "Test User").Run(); err != nil {
		t.Fatalf("git config user.name error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatalf("git config user.email error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "add", "README.md").Run(); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if err := exec.Command("git", "-C", worktree, "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("git commit error = %v", err)
	}

	hasRefs, err = repoHasRefs(gitDir)
	if err != nil {
		t.Fatalf("repoHasRefs() after commit error = %v", err)
	}
	if !hasRefs {
		t.Fatal("repoHasRefs() = false, want true after commit")
	}
}

func TestListRepoEntriesManaged(t *testing.T) {
	root := t.TempDir()
	store := RepoStore{
		ContainerPath: root,
		MainPath:      filepath.Join(root, "main"),
		Managed:       true,
	}

	for _, path := range []string{
		store.MainPath,
		filepath.Join(root, "worktrees", "feature-x"),
		filepath.Join(root, "PR", "99"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		t.Fatalf("listRepoEntries() error = %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}

	got := []string{
		entries[0].Kind + ":" + entries[0].Name,
		entries[1].Kind + ":" + entries[1].Name,
		entries[2].Kind + ":" + entries[2].Name,
	}
	want := []string{"main:main", "pr:99", "worktree:feature-x"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestListRepoEntriesLocal(t *testing.T) {
	store := RepoStore{
		MainPath: "/tmp/local-repo",
		Managed:  false,
	}

	entries, err := listRepoEntries(store)
	if err != nil {
		t.Fatalf("listRepoEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Kind != "local" || entries[0].Path != "/tmp/local-repo" {
		t.Fatalf("entry = %#v, want local /tmp/local-repo", entries[0])
	}
}
