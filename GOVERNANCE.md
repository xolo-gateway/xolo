# Governance

This document describes how the Xolo project is run and how decisions are
made. A French translation is available in
[`docs/fr/projet/gouvernance.md`](docs/fr/projet/gouvernance.md); the English
version is authoritative.

Xolo is governed at two levels. The companies that jointly steward the
project are bound by a partnership agreement (*convention de partenariat*)
that organizes their relationship: shared costs, trademark, infrastructure,
commercial loyalty, membership. This document is the public face of that
governance: it describes the bodies, roles, and decision processes that
apply to everyone who contributes, whether or not they work for a consortium
member.

## License commitment

Xolo is distributed under the GNU Affero General Public License version 3
([AGPL-3.0](LICENSE.md)) and will remain so. The project will not publish a
proprietary edition and will not adopt dual licensing.

The only license change the partnership agreement allows is a switch to a
later version of the GNU AGPL, or to a free-software license guaranteeing
equivalent freedoms, and even that requires both the unanimous
agreement of the consortium steering committee and the authorization of
every rights holder. A proprietary edition and dual licensing are excluded
outright: that commitment is what the community can build on.

Contributors keep the copyright on their contributions. The project uses the
[Developer Certificate of Origin](DCO), not a contributor license agreement:
nobody is asked to assign or exclusively license their rights to the project.

## Governance bodies

**Steering committee (COPIL).** Represents the consortium member companies,
one voice each. It handles what is not technical: budget and shared costs,
trademark policy, admission of new member companies, changes to the
partnership agreement, and commercial disputes between members. It appoints
the initial members of the technical committee.

**Technical committee (TSC).** The technical authority of the project. It
defines the architecture, administers the technical roadmap, adopts
development standards, names committers and maintainers, approves RFCs,
defines compatibility and security policy, and authorizes releases.

The TSC seats are:

- one maintainer presented by each consortium member company;
- one community seat, open to any maintainer who is not appointed by a
  member company (see below).

TSC members act in the interest of the project and declare any conflict
between that interest and the interest of their employer or a client. The
TSC publishes its non-confidential decisions in the project repository.

## Roles

**User.** Uses Xolo, reports bugs, proposes changes, takes part in
discussions.

**Contributor.** Has at least one merged contribution, of any kind: code,
documentation, translation, triage.

**Committer.** May merge pull requests. Named by the TSC.

**Maintainer.** Holds decision responsibility over all or part of the
project: roadmap, arbitration, releases, security response.

Access to the committer and maintainer roles is public and based on:

- the quality and regularity of contributions;
- the ability to review the work of others;
- knowledge of the project;
- compliance with the security rules and the code of conduct;
- the ability to act in the general interest of the project.

Belonging to a consortium member company does not, by itself, confer any
role. Current role holders are listed in [MAINTAINERS.md](MAINTAINERS.md).

## The community seat

The community seat on the TSC is reserved for a maintainer who is not
appointed by a consortium member company. It is vacant until a candidate
meets the following criteria:

- maintainer status, held for at least six months;
- sustained, substantial contributions over at least the preceding twelve
  months;
- regular participation in reviews and public technical discussions;
- no undeclared conflict of interest.

When at least one candidate qualifies, the seat is filled by election:
candidates declare themselves publicly, and the project's committers and
maintainers vote, one voice each, for a twelve-month renewable term. The TSC
organizes the vote and publishes the result.

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
  **thirty days** and require COPIL approval in addition to the TSC.

When a technical disagreement persists, the positions are written down in
the RFC, the TSC looks for a reversible or experimental path, and the status
quo holds — for sixty days at most, after which the steering committee
decides or appoints an independent expert. Whatever the outcome, the
decision and its reasons are published.

## Development rules

- No change reaches a protected branch without review; the author of a
  contribution is never its only approver.
- Mandatory automated tests must pass.
- Structural changes reference an RFC or an architecture decision.
- Commits identify their author and origin (see the DCO in
  [CONTRIBUTING.md](CONTRIBUTING.md)).
- Security fixes follow [SECURITY.md](SECURITY.md).

## Releases

A release is official when it is built from the official repository, follows
the release process, passes the automated checks, carries a version number
and release notes, is signed or attested, and is approved by the release
manager and at least one other maintainer. The release manager role rotates
between the consortium members on a calendar set by the TSC.

A modified distribution published by anyone must make it possible to
identify its differences from the official release.

## Funded development

Anyone may fund the development of a feature. Funding confers no automatic
right to integration in the core, to inclusion in a given release, to a
change of technical standards, or to a maintainer role. Funded developments
follow the same contribution process as everything else.

## Security

Vulnerabilities are reported privately and handled under coordinated
disclosure by a restricted security team that includes at least one
authorized representative of each consortium member. See
[SECURITY.md](SECURITY.md).

## Changes to this document

Substantial changes go through the RFC process above, with the thirty-day
comment period, and require the approval of the steering committee.
