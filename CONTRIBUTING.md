# Contributing to Xolo

Thanks for taking the time. This document explains how to get a change into
Xolo. A French translation is available in
[`docs/fr/projet/contribuer.md`](docs/fr/projet/contribuer.md); the English
version is authoritative.

Issues and pull requests may be written in English or French.

## Before you start

- **Bug fixes, typos, doc improvements**: open a pull request directly.
- **New features or behavior changes**: open an issue first and describe what
  you want to do. This avoids writing code that cannot be merged because it
  conflicts with the roadmap or duplicates work in progress.
- **Structural changes** (architecture, compatibility breaks, central
  dependencies, plugin interface, governance): these require an RFC — see
  [GOVERNANCE.md](GOVERNANCE.md).

Ordinary proposals follow lazy consensus: published publicly, accepted if no
motivated objection is raised within three working days
([GOVERNANCE.md](GOVERNANCE.md) describes what a motivated objection is).

## Development setup

You need Go (the version pinned in [`go.mod`](go.mod)), GNU make, and Docker
for the integration tests.

```bash
make build          # builds bin/server
make generate       # templ + tailwind; required after editing .templ files
make watch          # hot-reload dev server (uses .env)

go test ./...       # unit tests, SQLite only, no Docker needed
make test-integration  # store suite on SQLite AND PostgreSQL (needs Docker)
```

Configuration is environment-based with the `XOLO_` prefix; see
[`.env.dist`](.env.dist).

## Conventions

- Write code that reads like the surrounding code.
- Never edit generated files (`*_templ.go`, `*.pb.go`); edit the source
  (`.templ`, `.proto`) and regenerate.
- Web UI work must use the templui components under
  `internal/http/handler/webui/templui/component/`, not raw HTML tags.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(scope): subject`, `fix: subject`, etc.
- New store behaviour must be asserted on both backends (the store tests go
  through `eachBackend()`).

## Developer Certificate of Origin (DCO)

This project uses the [Developer Certificate of Origin](DCO) rather than a
contributor license agreement (CLA).

By signing off your commits you certify that you have the right to submit the
code and that it can be distributed under the project license. You keep your
copyright in full: the project asks for no assignment and no exclusive license.

Xolo is and will remain distributed under AGPL-3.0. The project will not
publish a proprietary edition.

### How to sign off

```bash
git commit -s -m "fix: description of the change"
```

Git appends a line to the message:

```
Signed-off-by: Firstname Lastname <firstname.lastname@example.com>
```

Set your identity once:

```bash
git config --global user.name "Firstname Lastname"
git config --global user.email "firstname.lastname@example.com"
```

### If you forgot to sign off

Last commit:

```bash
git commit --amend -s --no-edit
```

Last N commits:

```bash
git rebase --signoff HEAD~N
git push --force-with-lease
```

CI rejects pull requests containing unsigned commits.

### Pseudonyms

A stable pseudonym is accepted if it identifies you consistently and a valid
contact address is associated with it. Anonymous contributions or throwaway
addresses cannot be accepted.

### Contributions made at work

In many jurisdictions your employer owns the code you write on the job. Make
sure you are allowed to contribute, and prefer your professional email
address.

## Pull request process

1. Fork, create a branch.
2. Keep the pull request focused; unrelated changes go in separate PRs.
3. Add or update tests for what you change.
4. Run `go test ./...` and, if you touched the stores, `make test-integration`.
5. Sign off every commit (`git commit -s`).
6. Open the PR and fill in the template.
7. Respond to review feedback.

Every change is reviewed before reaching a protected branch, and the author
of a contribution is never its only approver — this applies to maintainers
too.

First response within 5 business days, best effort. Merging is at the
committers' and maintainers' discretion; opening a PR creates no obligation
to accept it, and funding a development confers no automatic right to its
integration.

## Plugins

Plugins run as separate processes and talk to Xolo over the gRPC interface
defined in `pkg/pluginsdk/proto/plugin.proto`. Contributions of new bundled
plugins (under `plugins/`) are welcome; discuss them in an issue first.

Third-party plugins are not bound by the AGPL: the plugin SDK
(`pkg/pluginsdk`) is under Apache-2.0, and [LICENSE-EXCEPTION](LICENSE-EXCEPTION)
lets a plugin that talks to Xolo only through the gRPC interface ship under
any license. Bundled plugins contributed to this repository are under the
AGPL-3.0 like the rest of the core.
