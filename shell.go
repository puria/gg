package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

var executablePath = os.Executable //nolint:gochecknoglobals

func shellInit(shell string) (string, error) {
	bin, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}

	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}

	quoted := strconv.Quote(bin)

	switch shell {
	case "fish":
		return fmt.Sprintf(fishInitTemplate, quoted), nil
	case "bash":
		return fmt.Sprintf(posixInitTemplate, quoted, quoted), nil
	case "zsh":
		return fmt.Sprintf(posixInitTemplate, quoted, quoted), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

const fishInitTemplate = `function gg --description 'manage git repos'
    set -l gg_bin %s

    switch "$argv[1]"
    case help -h --help version --version shell-init config-path init-config path alias list ls status prune rm
        $gg_bin $argv
        return $status
    case new
        set -l dir ($gg_bin $argv)
        or return $status

        cd $dir
        return $status
    end

    set -l dir ($gg_bin path $argv)
    or return $status

    cd $dir
end
`

const posixInitTemplate = `gg() {
  local gg_bin=%s

  case "$1" in
    ""|help|-h|--help|version|--version|shell-init|config-path|init-config|path|alias|list|ls|status|prune|rm)
      "$gg_bin" "$@"
      return $?
      ;;
    new)
      local new_dir
      new_dir="$("$gg_bin" "$@")" || return $?
      cd "$new_dir" || return $?
      return $?
      ;;
  esac

  local dir
  dir="$("%s" path "$@")" || return $?
  cd "$dir" || return $?
}
`
