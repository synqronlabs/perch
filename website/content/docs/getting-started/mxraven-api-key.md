---
title: Create an mxRaven API key
weight: 15
---

`perch setup` needs an mxRaven API key to provision your workspace. mxRaven
uses [ZITADEL](https://zitadel.com) for identity, so the key is a **Personal
Access Token (PAT)** on a ZITADEL service account.

This is a control-plane token, not a listener API key. perch uses it only during
setup to create the domain, listeners, routes, and listener credentials, and
never writes it to `perch.yaml`.

## Before you start

- You can sign in to the [mxRaven dashboard](https://app.mxraven.com).
- You know your workspace slug, for example `my-workspace`. It is the
  `--workspace` value you pass to `perch setup`.
- You have administrator access to that workspace.

## 1. Open the ZITADEL console

Sign in at <https://id.mxraven.com/ui/console> with the same account you use for
mxRaven. You can also open the dashboard user menu and choose **Manage
authentication**.

## 2. Select the workspace organization

Use the organization switcher in the top-left corner and choose the
organization named exactly after your workspace slug, for example
`my-workspace`. mxRaven creates this organization when it provisions the
workspace.

If you see more than one organization, the slug in the switcher identifies the
right one.

## 3. Create a service account

1. Open **Users** in the left navigation.
2. Select the **Service Accounts** tab.
3. Choose **New**.
4. Enter a username such as `perch` and a name such as `perch setup`.
5. Leave **Access Token Type** set to **Bearer** and choose **Create**.

## 4. Grant the mxRaven roles

1. Open the service account you just created.
2. Select the **Role Assignments** tab and choose **New**.
3. Choose the granted project **mxRaven**.
4. Select the roles to grant and choose **Save**.

The simplest option is to select every role in the `tenant` group. If you want
the smallest set that `perch setup` needs, select:

- `tenant.read` and `tenant.write`
- `domains.read` and `domains.write`
- `listeners.read` and `listeners.write`
- `api_keys.write`
- `rules.read` and `rules.write`

Add `storage_integrations.read`, `storage_integrations.write`, and
`credentials.write` if you use `--storage s3`.

## 5. Generate the API key

1. In the service account, select the **Personal Access Tokens** tab.
2. Choose **New** and optionally set an expiration date.
3. Copy the token from the dialog and store it somewhere safe. ZITADEL shows it
   only once.

## 6. Use the key with perch

Export the token for the setup command:

```sh
export MXRAVEN_API_TOKEN='paste-the-token-here'
```

Then run [the quickstart](quickstart.md). To check the token before running
setup, ask the control plane for your workspace:

```sh
curl -fsS "https://api.mxraven.com/v2/tenants/my-workspace" \
  -H "Authorization: Bearer $MXRAVEN_API_TOKEN"
```

A JSON workspace response means the token and roles are working. A `401` means
the token is missing, invalid, or expired. A `403` usually means the token
belongs to a different workspace or is missing a required role.

## Rotate or revoke

Create a replacement PAT on the same service account, update
`MXRAVEN_API_TOKEN`, then delete the old token. Deleting the service account
revokes all of its tokens.
