---
title: Authorize perch with mxRaven
weight: 15
description: Approve perch in the mxRaven dashboard with a device code.
---

`perch setup` creates a domain, listeners, API keys, and routes in your mxRaven
workspace. Before it can do that, mxRaven needs you to approve perch for that
workspace.

perch uses a **connected application** for this. It asks for a short code, you
approve the application in the dashboard, and the identity provider issues a
short-lived token. perch keeps that token in memory for the setup run only. It
never writes a control-plane credential to `perch.yaml`.

## Before you start

- You can sign in to the [mxRaven dashboard](https://app.mxraven.com).
- You are a **workspace owner**. Operators and auditors can see connected
  applications, but only an owner can approve one.
- You know your workspace slug, for example `my-workspace`. It is the
  `--workspace` value you pass to `perch setup`.

## 1. Run setup

```sh
perch setup \
  --workspace my-workspace \
  --domain example.com \
  --hostname localhost
```

perch prints an approval address and a code:

```text
Approve perch in your browser:
  https://app.mxraven.com/connect?user_code=ABCD-1234

Code: ABCD-1234

Waiting for approval...
```

On a terminal, perch opens the address automatically. Use `--no-browser` to
only print it.

## 2. Approve the application

1. Open the address. Sign in if the dashboard asks.
2. Choose the workspace perch may act on. It must be the same slug you passed
   to `--workspace`.
3. Review the permissions and choose **Approve**.
4. The dashboard hands you to the sign-in page to confirm the same code. The
   code is already filled in; confirm it.

The workspace approval and the code confirmation are separate steps. Creating
the connection records which workspace and which permissions you approved.
Confirming the code is what actually issues perch's token.

If perch is already connected to that workspace, approving again replaces the
live grant with the permissions shown on the screen. You do not need to revoke
the old connection first.

## 3. Let setup finish

After you confirm the code, perch continues provisioning and writes
`perch.yaml`. The access token is discarded when setup exits.

If the command says the code expired, run `perch setup` again. Codes are short
lived. If it says the workspace is not connected, approve the workspace you
actually passed to `--workspace`.

## Permissions perch requests

The approval screen shows the same list. Approval is all-or-nothing in the
current mxRaven dashboard.

| Permission | Used for |
| --- | --- |
| `tenant.read` | Read the workspace before provisioning. |
| `tenant.write` | Apply MTA rate-limit overrides. |
| `domains.read` | Find an existing sending domain and inbound route. |
| `domains.write` | Create the sending domain and inbound route. |
| `listeners.read` | Find existing MTA and submission listeners. |
| `listeners.write` | Create listeners, enable notifications, and configure Rspamd. |
| `api_keys.write` | Mint the listener API keys perch stores in its config. |
| `rules.read` | Find existing routing rules. |
| `rules.write` | Create S3 archival and auth reject rules. |
| `storage_integrations.read` | Find an existing S3 integration. |
| `storage_integrations.write` | Create the S3 integration used by `--storage s3`. |
| `credentials.write` | Store the S3 secret inside that integration. |

The grant belongs to the approving owner and is limited to that workspace. It
never exceeds the permissions you hold yourself. Revoking the connection in the
dashboard stops perch's access on its next API call.

## Revoke a connection

Open the workspace in the mxRaven dashboard and select **Connected
applications**. Revoking `perch` takes effect immediately. Running
`perch setup` again asks for a fresh approval.

## Automation

Device approval needs a human. For CI or unattended provisioning, pass a
workspace-scoped API token instead:

```sh
export MXRAVEN_API_TOKEN=...
perch setup --workspace my-workspace --domain example.com
```

See [Use an API token for automation](mxraven-api-key.md). The token path is
legacy, but it remains available for pipelines that cannot open a browser.
