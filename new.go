package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	newTemplateRepoURL = "https://github.com/puria/md.git"
	initialCommitMsg   = "feat: Initial commit 🎉 by [gg](https://github.com/puria/gg)"
)

var osMkdirTemp = os.MkdirTemp //nolint:gochecknoglobals

var osRemoveAll = os.RemoveAll //nolint:gochecknoglobals

var osGetwd = os.Getwd //nolint:gochecknoglobals

func newCommand(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gg new <owner/repo>")
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	repo, err := resolveOneArg(cfg, args[0])
	if err != nil {
		return err
	}

	repoPath := repo.ContainerPath(cfg)
	exists, err := pathExists(repoPath)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("repository already exists: %s", repoPath)
	}

	if err := osMkdirAll(repoPath, 0o755); err != nil {
		return fmt.Errorf("create repository directory %s: %w", repoPath, err)
	}

	if err := copyMarkdownTemplates(repoPath); err != nil {
		return err
	}

	if err := runCommand(repoPath, "git", "init"); err != nil {
		return fmt.Errorf("initialize git repository: %w", err)
	}
	if err := runCommand(repoPath, "git", "add", "--all"); err != nil {
		return fmt.Errorf("stage initial files: %w", err)
	}
	if err := runCommand(repoPath, "git", "commit", "-m", initialCommitMsg); err != nil {
		return fmt.Errorf("create initial commit: %w", err)
	}

	fmt.Println(repoPath)
	return nil
}

func mdCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gg md init <owner/repo> | gg md up")
	}

	switch args[0] {
	case "init":
		if len(args) != 2 {
			return errors.New("usage: gg md init <owner/repo>")
		}
		return newCommand(args[1:])
	case "up":
		return mdUpCommand(args[1:])
	default:
		return errors.New("usage: gg md init <owner/repo> | gg md up")
	}
}

func mdUpCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: gg md up")
	}

	repoPath, err := osGetwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}

	topLevel, err := captureCommand(repoPath, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("current directory is not a git repository: %w", err)
	}
	if topLevel != "" {
		repoPath = topLevel
	}

	if err := copyMarkdownTemplates(repoPath); err != nil {
		return err
	}

	fmt.Println(repoPath)
	return nil
}

func copyMarkdownTemplates(dstRoot string) error {
	templatePath, err := osMkdirTemp("", "gg-md-*")
	if err != nil {
		return fmt.Errorf("create template download directory: %w", err)
	}
	defer func() {
		_ = osRemoveAll(templatePath)
	}()

	if err := runCommand("", "git", "clone", "--depth", "1", newTemplateRepoURL, templatePath); err != nil {
		return fmt.Errorf("download markdown templates: %w", err)
	}

	return copyMarkdownFiles(templatePath, dstRoot)
}

func copyMarkdownFiles(srcRoot, dstRoot string) error {
	copied := 0
	err := filepath.WalkDir(srcRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return fmt.Errorf("resolve template path %s: %w", path, err)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect template file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if isLegacyTaskfile(rel) {
			return nil
		}

		if err := copyFile(path, filepath.Join(dstRoot, rel), info.Mode().Perm()); err != nil {
			return err
		}
		copied++
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy markdown templates: %w", err)
	}
	if copied == 0 {
		return fmt.Errorf("copy markdown templates: no template files found in %s", newTemplateRepoURL)
	}

	return nil
}

func isLegacyTaskfile(path string) bool {
	return filepath.Base(path) == "Taskfile.yml"
}

func copyFile(src, dst string, mode fs.FileMode) error {
	input, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open template file %s: %w", src, err)
	}
	defer func() {
		_ = input.Close()
	}()

	if err := osMkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create template directory %s: %w", filepath.Dir(dst), err)
	}

	output, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create template file %s: %w", dst, err)
	}
	defer func() {
		_ = output.Close()
	}()

	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy template file %s: %w", src, err)
	}
	if err := output.Chmod(mode); err != nil {
		return fmt.Errorf("set template file mode %s: %w", dst, err)
	}

	return nil
}
