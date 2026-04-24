# Project conventions for Claude

## Git workflow

- **Default communication language**: 中文.
- **Auto-commit & push**: when changes are ready, commit them and push to
  `origin/main` (and the current working branch, if different) without asking
  for confirmation first. The user has granted durable permission for this —
  do not prompt before `git push origin main`.
- If `main` and the working branch have diverged (e.g. a PR was merged on
  GitHub while a feature branch was in progress), resolve with a merge commit
  silently; do not ask which merge strategy to use.
- Still DO confirm before destructive operations (force-push, `reset --hard`,
  branch deletion, history rewrites). Auto-commit permission does not extend
  to those.

## Build / verify

- `go build -o /tmp/gw .` — must pass before any commit that touches Go.
- `go vet ./...` — must pass before any commit that touches Go.
- For security-related changes, run `scripts/abuse-scan.sh http://127.0.0.1:<port>`
  against a local instance as part of verification.
