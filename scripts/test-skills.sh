#!/usr/bin/env bash
# Deterministic skills regression suite. No installation or model generation.
# Usage: bash scripts/test-skills.sh [/absolute/path/to/PrAImate-FORGE-Codex-Kit]
set -euo pipefail

if (( $# > 1 )); then
  echo 'Usage: bash scripts/test-skills.sh [FORGE-kit-directory]' >&2
  exit 2
fi
cd "$(dirname "$0")/.."
if (( $# == 1 )); then
  if [[ ! -d "$1" || "$1" != /* ]]; then
    echo 'The kit must be an existing absolute directory.' >&2
    exit 2
  fi
  export PRAIMATE_FORGE_KIT="$1"
fi

gui_tags='desktop,production'
case "$(uname -s)" in
  Linux) gui_tags="$gui_tags,webkit2_41" ;;
  MINGW*|MSYS*|CYGWIN*) ;;
  *) echo 'GUI tests support Linux and Windows only.' >&2; exit 2 ;;
esac

go build ./...
go vet ./...
go test ./... -count=1
go test -race ./internal/agentic -count=1
go test -race ./internal/skills ./internal/core -run 'Test(Runtime|Managed.*Skill|ManagedWorkflowSequence|WorkflowSequence|.*Skill.*|Forge.*)' -count=1
(
  cd cmd/praimate-gui
  go vet -tags "$gui_tags" ./...
  go test -tags "$gui_tags" ./... -count=1
  cd frontend
  if [[ ! -d node_modules ]]; then
    echo 'Frontend dependencies are missing. This test script does not install them.' >&2
    exit 2
  fi
  node --test --test-reporter=spec src/lib/*.test.js src/pages/*.test.js
  npm run build
)
echo 'Deterministic checks passed. Real CLI/model and interactive GUI acceptance are separate; see docs/SKILLS_TEST_GUIDE.md.'
