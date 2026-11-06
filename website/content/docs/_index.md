---
title: Documentation
weight: 10
bookCollapseSection: false
---

perch is a small IMAP mailbox service backed by
[mxRaven](https://mxraven.com). These pages cover installation, mxRaven
provisioning, storage, TLS, operations, and the `perch` command-line client.

Start with [Installation](getting-started/installation.md) and the
[Quickstart](getting-started/quickstart.md). Before provisioning, follow
[Authorize perch with mxRaven](getting-started/authorize-perch.md). Then use the
[CLI reference](cli/_index.md) for command details. For agent and application
patterns, see [Agents and applications](mail/agents.md).

Coding-agent harnesses can install the bundled Agent Skill directly:

```sh
npx skills add https://github.com/synqronlabs/perch --skill perch
```

Add `-g` for a user-wide install, or `-a pi` / `-a hermes-agent` to target a
specific harness. The skill teaches an agent to send and receive mail through
the CLI and to diagnose connection, authentication, delivery, TLS, S3, and
configuration failures.
