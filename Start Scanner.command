#!/bin/zsh
set -eu
cd "${0:A:h}"
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
if [[ ! -x ./bin/xerox-airscan ]]; then
  if command -v asdf >/dev/null 2>&1 && asdf which go >/dev/null 2>&1; then
    asdf exec go build -trimpath -o bin/xerox-airscan ./cmd/xerox-airscan
  else
    go build -trimpath -o bin/xerox-airscan ./cmd/xerox-airscan
  fi
fi
exec ./bin/xerox-airscan serve "$@"
