package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultHost = "github.com"

type Config struct {
	Root    string            `json:"root"`
	Host    string            `json:"host"`
	Aliases map[string]string `json:"aliases"`
}

func loadConfig() (Config, string, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, "", err
	}

	cfg, err := defaultConfig()
	if err != nil {
		return Config{}, "", err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, path, nil
	}
	if err != nil {
		return Config{}, "", fmt.Errorf("read config %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, path, nil
	}

	var userCfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&userCfg); err != nil {
		return Config{}, "", fmt.Errorf("parse config %s: %w", path, err)
	}

	if userCfg.Root != "" {
		cfg.Root = userCfg.Root
	}
	if userCfg.Host != "" {
		cfg.Host = userCfg.Host
	}
	if userCfg.Aliases != nil {
		cfg.Aliases = userCfg.Aliases
	}

	cfg.Root, err = expandPath(cfg.Root)
	if err != nil {
		return Config{}, "", fmt.Errorf("expand root %q: %w", cfg.Root, err)
	}

	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Host == "" {
		return Config{}, "", errors.New("config host cannot be empty")
	}
	if strings.Contains(cfg.Host, "/") {
		return Config{}, "", fmt.Errorf("config host must not contain '/': %q", cfg.Host)
	}

	return cfg, path, nil
}

func defaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return Config{
		Root:    filepath.Join(home, "src"),
		Host:    defaultHost,
		Aliases: map[string]string{},
	}, nil
}

func configPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("GG_CONFIG")); path != "" {
		return path, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}

	return filepath.Join(dir, "gg", "config"), nil
}

func initConfigCommand() error {
	cfg, err := defaultConfig()
	if err != nil {
		return err
	}

	path, err := configPath()
	if err != nil {
		return err
	}

	cfgJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("render config template: %w", err)
	}
	cfgJSON = append(cfgJSON, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config %s: %w", path, err)
	}

	if err := os.WriteFile(path, cfgJSON, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}

	fmt.Println(path)
	return nil
}

func aliasCommand(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: gg alias <target> <name>")
	}

	target := strings.TrimSpace(args[0])
	name := strings.TrimSpace(args[1])
	if target == "" || name == "" {
		return errors.New("usage: gg alias <target> <name>")
	}

	cfg, path, err := loadConfig()
	if err != nil {
		return err
	}

	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	cfg.Aliases[name] = target

	if err := writeConfig(path, cfg); err != nil {
		return err
	}

	fmt.Printf("%s -> %s\n", name, target)
	return nil
}

func writeConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("render config %s: %w", path, err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}

	return nil
}

func expandPath(path string) (string, error) {
	expanded := os.ExpandEnv(strings.TrimSpace(path))
	if expanded == "" {
		return "", errors.New("path is empty")
	}

	if expanded == "~" {
		return os.UserHomeDir()
	}

	if strings.HasPrefix(expanded, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		expanded = filepath.Join(home, expanded[2:])
	}

	return filepath.Clean(expanded), nil
}
