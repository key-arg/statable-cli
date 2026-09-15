#!/bin/bash
# The script asciinema records. Each command is typed out character by
# character so the recording reads like somebody using the tool, then run.
set -u

type_out() {
  printf '\033[1;32m$\033[0m '
  local i
  for ((i = 0; i < ${#1}; i++)); do
    printf '%s' "${1:i:1}"
    sleep 0.035
  done
  printf '\n'
  sleep 0.35
}

run() {
  type_out "$1"
  eval "$1"
  sleep "${2:-2.5}"
  printf '\n'
}

run 'statable now' 2
run 'statable stats --range 30d --compare previous' 3
run 'statable series --by week --range 90d' 3.5
run 'statable top pages --range 30d --limit 5' 3
run 'statable check --metric visitors --range 7d --min 10; echo "exit=$?"' 3
run "statable top countries --range 30d --limit 3 --json | jq -c '.[]'" 3.5
