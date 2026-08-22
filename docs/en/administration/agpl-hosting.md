# Hosting Xolo: what the AGPL implies

Xolo is licensed under the [AGPL-3.0](../../../LICENSE.md). This page explains
what that means when you host an instance for users other than yourself. It is
an explanation, not legal advice; the license text prevails.

## You host the official version, unmodified

No particular obligation. Point your users to the source code, for example by
linking to the [official repository](https://github.com/xolo-gateway/xolo).

## You host a modified version

Article 13 of the AGPL-3.0 applies: you must offer every user who interacts
with the instance over the network the possibility of obtaining the source
code corresponding to the version actually deployed.

This covers **every** modification of the core: a bug fix, a compiled-in
configuration change, a theme, a three-line integration patch.

### How to comply

1. Publish your fork in a public repository under AGPL-3.0.
2. Show a visible link in the interface (footer, "About" page).
3. Make sure the published code matches **exactly** the deployed version (a
   precise tag or commit).

### How to avoid the constraint

Put your specific behavior into **plugins** rather than modifications of the
core. Plugins run as separate processes and communicate with Xolo over the
gRPC interface defined in `pkg/pluginsdk/proto/plugin.proto`; article 13 does
not reach them.

It is also the better engineering choice: your adaptations survive upgrades.

Watch the boundary, though. A patch to Xolo itself that your plugin needs,
however small, is a modification of the core and stays under AGPL.
