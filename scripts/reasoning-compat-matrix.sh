#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: scripts/reasoning-compat-matrix.sh V0.8.1_WORKTREE [CURRENT_WORKTREE]" >&2
  exit 2
fi

old=$(cd "$1" && pwd)
current=$(cd "${2:-.}" && pwd)
probe="$current/scripts/reasoning-compat-probe/main.go"
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

run_probe() {
  target=$1
  legacy=$2
  output=$3
  mkdir -p "$scratch/$legacy"
  printf 'module reasoning-compat-probe\n\ngo 1.25\n\nrequire github.com/Judgment-Pack/judgment-pack-agent/go v0.0.0\nreplace github.com/Judgment-Pack/judgment-pack-agent/go => %s/go\n' "$target" > "$scratch/$legacy/go.mod"
  if [ "$legacy" = --legacy ]; then
    (cd "$scratch/$legacy" && GOFLAGS='-mod=mod -buildvcs=false' go run "$probe" --legacy) > "$output"
  else
    (cd "$scratch/$legacy" && GOFLAGS='-mod=mod -buildvcs=false' go run "$probe") > "$output"
  fi
}

run_probe "$old" --legacy "$scratch/old.tsv"
run_probe "$current" --current "$scratch/current.tsv"

printf '| Input | v0.8.1 | Current | Current author request |\n'
printf '| --- | --- | --- | --- |\n'
paste "$scratch/old.tsv" "$scratch/current.tsv" | while IFS=$'\t' read -r old_name old_admit old_request current_name current_admit current_request; do
  if [ "$old_name" != "$current_name" ]; then
    echo "matrix rows differ" >&2
    exit 1
  fi
  if [ "$old_admit" = true ] && [ "$current_admit" != true ]; then
    echo "current refused an input admitted by v0.8.1: $old_name" >&2
    exit 1
  fi
  case "$old_name" in
    *"author set")
      if [ "$old_request" != "$current_request" ]; then
        echo "set author request changed: $old_name" >&2
        exit 1
      fi
      ;;
    *"author omitted")
      if [ "$current_request" != '{}' ]; then
        echo "omitted author request gained a member: $old_name" >&2
        exit 1
      fi
      ;;
  esac
  printf '| %s | %s | %s | `%s` |\n' "$old_name" "$old_admit" "$current_admit" "$current_request"
done
