# Governance

This document describes how the Xolo project is run and how decisions are
made. A French translation is available in
[`docs/fr/projet/gouvernance.md`](docs/fr/projet/gouvernance.md); the English
version is authoritative.

Xolo is led by a lead maintainer, William Petit ([@Bornholm](https://github.com/Bornholm)),
who founded the project and holds final decision authority over it. Decisions
are prepared in public and taken by lazy consensus whenever possible; the
lead maintainer arbitrates when consensus does not emerge. This model is
deliberately simple for a project of Xolo's current size. It is expected to
evolve as the project grows, through the process described at the end of
this document.

## License commitment

Xolo is distributed under the GNU Affero General Public License version 3
([AGPL-3.0](LICENSE.md)) and will remain so. The project will not publish a
proprietary edition and will not adopt dual licensing.

The only license change the project allows itself is a switch to a later
version of the GNU AGPL, or to a free-software license guaranteeing
equivalent freedoms, and even that requires the authorization of every
rights holder. A proprietary edition and dual licensing are excluded
outright: that commitment is what the community can build on.

Plugins are a separate matter. The plugin SDK (`pkg/pluginsdk`) is
distributed under the Apache License 2.0, and [LICENSE-EXCEPTION](LICENSE-EXCEPTION)
lets plugins that talk to Xolo through its gRPC interface ship under any
license. The core stays copyleft; the plugin boundary is open.

The Xolo name and logo are trademarks, held by the lead maintainer until an
association is created to carry the project. Their use is governed by
[TRADEMARK.md](TRADEMARK.md).

Contributors keep the copyright on their contributions. The project uses the
[Developer Certificate of Origin](DCO), not a contributor license agreement:
nobody is asked to assign or exclusively license their rights to the project.

## Roles

**User.** Uses Xolo, reports bugs, proposes changes, takes part in
discussions.

**Contributor.** Has at least one merged contribution, of any kind: code,
documentation, translation, triage.

**Committer.** May merge pull requests. Named by the lead maintainer.

**Maintainer.** Holds decision responsibility over all or part of the
project: roadmap, arbitration, releases, security response. Named by the
lead maintainer.

**Lead maintainer.** Holds final authority over the project: architecture,
technical roadmap, development standards, appointment of committers and
maintainers, approval of RFCs, compatibility and security policy, releases.
Acts in the interest of the project and declares any conflict between that
interest and the interest of an employer or a client.

Access to the committer and maintainer roles is public and based on:

- the quality and regularity of contributions;
- the ability to review the work of others;
- knowledge of the project;
- compliance with the security rules and the code of conduct;
- the ability to act in the general interest of the project.

Working for the same employer as the lead maintainer, or funding the
project, confers no role by itself. Current role holders are listed in
[MAINTAINERS.md](MAINTAINERS.md).

## Decision making

Day-to-day decisions are made by lazy consensus, in public. A proposal
published in the project's discussion channels is considered accepted if it
has raised no motivated objection after **three working days**.

An objection must state:

- the risk identified;
- the technical elements it rests on;
- an alternative, or the conditions under which it would be withdrawn.

Structural decisions go through a public RFC, open for comments for at least
**seven working days**. Are considered structural, among others:

- a major evolution of the architecture;
- a compatibility break, including breaking changes to the plugin gRPC
  interface (`pkg/pluginsdk/proto/plugin.proto`);
- the addition of a central dependency;
- a change of data format;
- a significant change to the security model;
- the removal of a public feature;
- the creation or removal of a main component;
- changes to this document or to the licensing regime of the plugin
  interface and SDK (`pkg/pluginsdk`) — these stay open for at least
  **thirty days**.

When a technical disagreement persists, the positions are written down in
the RFC, a reversible or experimental path is looked for, and the lead
maintainer decides. Whatever the outcome, the decision and its reasons are
published.

## Development rules

- No change reaches a protected branch without review; the author of a
  contribution is never its only approver. When the lead maintainer is the
  only other maintainer, a committer's review counts.
- Mandatory automated tests must pass.
- Structural changes reference an RFC or an architecture decision.
- Commits identify their author and origin (see the DCO in
  [CONTRIBUTING.md](CONTRIBUTING.md)).
- Security fixes follow [SECURITY.md](SECURITY.md).

## Releases

A release is official when it is built from the official repository, follows
the release process, passes the automated checks, carries a version number
and release notes, is signed or attested, and is approved by the lead
maintainer. The lead maintainer may delegate the release manager role to
another maintainer.

A modified distribution published by anyone must make it possible to
identify its differences from the official release.

## Funded development

Anyone may fund the development of a feature. Funding confers no automatic
right to integration in the core, to inclusion in a given release, to a
change of technical standards, or to a maintainer role. Funded developments
follow the same contribution process as everything else.

## Security

Vulnerabilities are reported privately and handled under coordinated
disclosure by the maintainers. See [SECURITY.md](SECURITY.md).

## Changes to this document

Substantial changes go through the RFC process above, with the thirty-day
comment period, and are approved by the lead maintainer. The license
commitment above is not subject to this process: it can only be changed
with the authorization of every rights holder.

Moving to a shared governance (a maintainer council, a foundation, a
multi-company partnership) is one such substantial change: it is proposed
in the open, as an RFC, when the project has the contributors to justify it.
