package main

import (
	"fmt"
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

func TestResolveRepoWithFullAliasTargetUsingSegmentAlias(t *testing.T) {
	cfg := Config{
		Root: "/tmp/src",
		Host: "github.com",
		Aliases: map[string]string{
			"f":       "ForkbombEu",
			"credimi": "f/credimi",
		},
	}

	request, err := resolveRequest(cfg, []string{"credimi"})
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}

	if request.Repo.String() != "ForkbombEu/credimi" {
		t.Fatalf("repo from nested alias = %q, want %q", request.Repo.String(), "ForkbombEu/credimi")
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

func TestShellInitPassesThroughManagementAliases(t *testing.T) {
	oldExecutablePath := executablePath
	executablePath = func() (string, error) {
		return "/tmp/bin/gg", nil
	}
	defer func() {
		executablePath = oldExecutablePath
	}()

	fishScript, err := shellInit("fish")
	if err != nil {
		t.Fatalf("shellInit(fish) error = %v", err)
	}
	if !strings.Contains(fishScript, "list ls status prune rm") {
		t.Fatalf("fish shell script missing ls/rm passthrough: %q", fishScript)
	}

	bashScript, err := shellInit("bash")
	if err != nil {
		t.Fatalf("shellInit(bash) error = %v", err)
	}
	if !strings.Contains(bashScript, "|list|ls|status|prune|rm)") {
		t.Fatalf("bash shell script missing ls/rm passthrough: %q", bashScript)
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

func TestFinalizeWorktreeSetupRunsMiseWhenConfigPresent(t *testing.T) {
	worktreePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktreePath, "mise.toml"), []byte("[tools]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	restore := stubExecCommand(t)
	defer restore()

	if err := finalizeWorktreeSetup(worktreePath); err != nil {
		t.Fatalf("finalizeWorktreeSetup() error = %v", err)
	}

	commands := readCommandLog(t)
	want := []string{
		"git\tsubmodule\tupdate\t--init\t--recursive",
		"mise\ttrust\t" + filepath.Join(worktreePath, "mise.toml"),
		"mise\tinstall",
	}
	if len(commands) != len(want) {
		t.Fatalf("len(commands) = %d, want %d (%v)", len(commands), len(want), commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestFinalizeWorktreeSetupSkipsMiseWithoutConfig(t *testing.T) {
	worktreePath := t.TempDir()

	restore := stubExecCommand(t)
	defer restore()

	if err := finalizeWorktreeSetup(worktreePath); err != nil {
		t.Fatalf("finalizeWorktreeSetup() error = %v", err)
	}

	commands := readCommandLog(t)
	want := []string{
		"git\tsubmodule\tupdate\t--init\t--recursive",
	}
	if len(commands) != len(want) {
		t.Fatalf("len(commands) = %d, want %d (%v)", len(commands), len(want), commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("command %d = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestParseStatusArgs(t *testing.T) {
	options, repoArgs, err := parseStatusArgs([]string{"--files", "ForkbombEu/credimi"})
	if err != nil {
		t.Fatalf("parseStatusArgs() error = %v", err)
	}
	if !options.ShowFiles {
		t.Fatal("options.ShowFiles = false, want true")
	}
	if len(repoArgs) != 1 || repoArgs[0] != "ForkbombEu/credimi" {
		t.Fatalf("repoArgs = %#v, want [ForkbombEu/credimi]", repoArgs)
	}
}

func TestParseStatusArgsRejectsUnknownFlag(t *testing.T) {
	if _, _, err := parseStatusArgs([]string{"--nope", "ForkbombEu/credimi"}); err == nil {
		t.Fatal("parseStatusArgs() error = nil, want error")
	}
}

func TestParseRepoStatus(t *testing.T) {
	status := parseRepoStatus("## main...origin/main [ahead 1]\n M repo.go\n?? new.txt\n")

	if status.Branch != "main...origin/main [ahead 1]" {
		t.Fatalf("status.Branch = %q, want %q", status.Branch, "main...origin/main [ahead 1]")
	}

	wantFiles := []string{"M repo.go", "?? new.txt"}
	if len(status.Files) != len(wantFiles) {
		t.Fatalf("len(status.Files) = %d, want %d (%v)", len(status.Files), len(wantFiles), status.Files)
	}
	for i := range wantFiles {
		if status.Files[i] != wantFiles[i] {
			t.Fatalf("status.Files[%d] = %q, want %q", i, status.Files[i], wantFiles[i])
		}
	}
}

func stubExecCommand(t *testing.T) func() {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("GG_TEST_COMMAND_LOG", logPath)

	oldExecCommand := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}

	return func() {
		execCommand = oldExecCommand
	}
}

func readCommandLog(t *testing.T) []string {
	t.Helper()

	logPath := os.Getenv("GG_TEST_COMMAND_LOG")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", logPath, err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}

	return strings.Split(content, "\n")
}

func TestHelperProcess(t *testing.T) { //nolint:unparam
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == -1 || separator+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "missing helper separator")
		os.Exit(2)
	}

	name := args[separator+1]
	cmdArgs := args[separator+2:]

	logPath := os.Getenv("GG_TEST_COMMAND_LOG")
	if logPath != "" {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer file.Close() //nolint:errcheck

		line := name
		if len(cmdArgs) > 0 {
			line += "\t" + strings.Join(cmdArgs, "\t")
		}
		if _, err := fmt.Fprintln(file, line); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	os.Exit(0)
}
