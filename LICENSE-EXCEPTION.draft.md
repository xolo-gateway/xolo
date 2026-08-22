# Plugin license exception — DRAFT, NOT IN EFFECT

> **This is a draft under review. It grants nothing.**
>
> No permission stated below applies to any released version of Xolo. The
> exception takes effect only when this file is renamed to
> `LICENSE-EXCEPTION`, this notice is removed, and the change is merged and
> released. Until then, Xolo is distributed under the [AGPL-3.0](LICENSE.md)
> without exception, and the plugin SDK carries no separate license.
>
> Two things must happen before that: written consent from every rights
> holder, and a review by a lawyer specializing in intellectual property and
> free software. Feedback on the text is welcome in the pull request that
> introduces it.

## Why this exception

Xolo plugins run as separate processes and talk to the server over gRPC. The
FSF's position on separate processes, and case law on interface protection
(*SAS Institute v. World Programming*, CJEU 2012; *Google v. Oracle*, US
Supreme Court 2021), both suggest a plugin is not a derivative work of the
server.

That is a comfortable position, but it still requires anyone writing a plugin
to reason about it — or to ask a lawyer. This exception removes the question
rather than answers it, so that a plugin author can rely on a written grant
instead of on an interpretation.

The intended outcome: plugins under any license, including proprietary ones,
while the core of Xolo stays copyleft.

## Proposed text

```
Additional permission for plugins — Xolo

In addition to the terms of the GNU Affero General Public License version 3
(AGPLv3) under which Xolo is distributed, and pursuant to its section 7, the
copyright holders grant the following additional permission.

SCOPE

Programs that communicate with Xolo exclusively through the gRPC interface
defined in the plugin SDK module (github.com/xolo-gateway/xolo/pkg/pluginsdk)
are not considered derivative works of Xolo within the meaning of the AGPLv3.

Such programs may be distributed under any license, including a proprietary
one, with no obligation to make their source code available.

CONDITIONS

This permission applies to programs that:

  a) run in a process separate from Xolo;
  b) communicate with Xolo only through the gRPC services defined in the
     .proto files of the plugin SDK module;
  c) incorporate no portion of the source code of Xolo, other than the
     plugin SDK module itself, which is distributed under the Apache
     License, Version 2.0.

LIMITS

This permission does not extend to:

  - modifications to Xolo itself, including patches required to integrate a
    plugin, however small;
  - programs using other forms of interaction, in particular dynamic library
    loading, direct database access, or undocumented internal interfaces;
  - forks or modified redistributions of Xolo.

This permission is irrevocable for the versions of Xolo to which it is
attached.

Xolo project — [DATE]
```

## Where the boundary sits

The limit that surprises people: **a patch to Xolo itself stays under the
AGPL**, even a three-line one, even when a plugin cannot work without it. The
exception covers what runs on the other side of the gRPC interface, nothing
more. Integrators who need core changes either upstream them or publish their
fork.

## Open questions

1. **The Apache-2.0 relicensing of the SDK has to land at the same time.**
   Condition (c) refers to a license the SDK does not yet carry; publishing
   one without the other leaves a dangling reference.
2. **"Are not considered derivative works" states a legal characterization**,
   while a section 7 additional permission grants rights. Wording that
   expressly authorizes distribution under other terms may be more robust
   than wording that asserts what a work is. To be settled with counsel.
3. **Irrevocability.** The grant is permanent for the versions it ships with.
   The project stays free to change it for future versions, subject to the
   agreement of the rights holders.

## Checklist before this takes effect

- [ ] Written consent from every rights holder
- [ ] Legal review
- [ ] `pkg/pluginsdk` effectively relicensed under Apache-2.0 (`LICENSE`
      file, headers in `plugin.proto`, generated code regenerated)
- [ ] This file renamed to `LICENSE-EXCEPTION`, draft notice removed, `[DATE]`
      filled in
- [ ] Cross-references added in `LICENSE.md`, `README.md` and
      `pkg/pluginsdk/README.md`
- [ ] Mentioned in `docs/*/administration/agpl-hosting.md`
