# For a Workspace administrator

Somebody in your organization wants to use `spacebar`, a terminal client for
Google Chat, and has been told to hand you this page. It asks for one of three
things depending on what your organization already allows, and the first one
needs nothing from you at all.

Google Chat and Google are trademarks of Google LLC. `spacebar` is an
independent third-party client. It is not affiliated with, sponsored by, or
endorsed by Google, and Google does not support it.

> **Status.** This document is written against the Google Cloud and Admin
> consoles as they are documented. The exact click path in
> [Approving the application](#3-approving-the-application-third-party-access)
> is **marked as unverified** below and should be checked against your own
> console before you rely on it. A stale click path in an administrator's
> document is worse than no document, because the person reading it has no way
> to tell the difference. Everything else here is verified against the API or is
> a statement about `spacebar` itself.

## What it does, in one paragraph

It sends and reads messages in Chat spaces from a terminal, and exposes the same
capabilities to an AI agent over MCP. It stores credentials in the operating
system keyring, or in a mode `0600` file on a machine that has none, and it
warns every time it uses the file. It has no telemetry, no phone-home, and no
update check. The only hosts it contacts are the Chat API, Google's OAuth
endpoints, and an incoming webhook URL the user supplied. Its threat model and
the list of what is impossible by construction are in
[SECURITY.md](../SECURITY.md).

## The three paths, cheapest first

### 1. An incoming webhook, which needs nothing from you

If your organization allows webhooks in Chat spaces, a user can create one from
the space itself, under **Apps & integrations**, and needs no approval, no
OAuth client, and no Cloud project.

What they get is deliberately limited: **write-only, to one space, appearing as
a bot rather than as themselves.** It cannot read anything. If that is enough
for what they are doing, which for alerting and CI notifications it usually is,
you can stop reading here.

If the **Add webhooks** button is greyed out for them, your organization has
disabled webhooks and this path is closed.

### 2. Their own OAuth client, which needs a Cloud project

To read messages, the user has to authorize as themselves, which needs an OAuth
client. **The best outcome for both of you is a client they create in your own
Cloud project with an Internal user type**, because it avoids two separate
problems at once:

- An Internal client is not a third-party application to your organization, so
  third-party app access control does not apply to it.
- An External client that has not been through Google's verification is in
  testing mode, where **refresh tokens are revoked seven days after consent**
  and there is a hundred-test-user cap. The user would have to re-authorize
  every week, forever.

What you would need to allow is for them to create a project in your
organization, or to create one for them, and to enable the Google Chat API on
it. The steps they follow are printed by the tool itself:

```
spacebar auth setup
```

That prints the walkthrough with no network access and no browser, which matters
because the people who need this path are often on managed machines.

### 3. Approving the application (third-party access)

Only relevant if the user is running an **official release build**, which is
linked with an OAuth client belonging to the project's maintainer rather than
to your organization. A build from source has no client at all, by design: an
OAuth client committed to an open repository is a client every fork uses.

> **Unverified.** The path below is what Google's documentation describes, and
> it has **not** been checked against a live Admin console by anybody who wrote
> this. Verify it before following it, and please open an issue if it has moved.

> Admin console → **Security** → **Access and data control** → **API controls**
> → **Manage Third-Party App Access**

You would add the application by its OAuth client ID and mark it Trusted for the
scopes below.

> **The release client ID goes here.** No official release has been published,
> so there is no client ID to print yet. This section is a placeholder and the
> release that first ships an injected client owns filling it in.

## The permissions it asks for

`spacebar` never requests a blanket scope. It asks for the narrowest set that
does what the user is actually doing, because a narrower request is one you are
more likely to approve, and that is the difference between the tool working and
not.

| What the user is doing        | Scope requested                                            |
| ----------------------------- | ---------------------------------------------------------- |
| Posting only (`--send-only`)  | `https://www.googleapis.com/auth/chat.messages.create`      |
| Posting, editing, deleting    | `https://www.googleapis.com/auth/chat.messages`             |
| Finding a space by name       | `https://www.googleapis.com/auth/chat.spaces.readonly`      |
| Reading who is in a space     | `https://www.googleapis.com/auth/chat.memberships.readonly` |

Two scopes are deliberately **not** requested, and both of them are the writable
half of one that is:

- `chat.spaces` permits creating and deleting spaces. Nothing in the tool does
  that. Finding an existing direct message does **not** need it: that reads on
  `chat.spaces.readonly`, which is in the table above.
- `chat.memberships` permits adding and removing people from a space. Reading a
  membership list is the `.readonly` scope in the table above, and this tool
  never changes who is in a space.

## What a user can do that you may care about

Stated plainly, because a document that only lists reassurances is not a useful
one.

**They can post as themselves, from a script, with no human in the loop.** That
is the point of the tool. Anything their account may post, it may post.

**They can hand those capabilities to an AI agent.** The MCP server exists for
exactly that. It is gated more tightly than the terminal: write tools are not
registered at all unless `--allow-write` is passed, the spaces they apply to can
be restricted with `--allow-space`, and every tool call is logged. But the
capability is real and you should assume it will be used.

**They cannot exceed their own permissions.** It acts as the user, through
Google's own authorization, with the scopes above. It cannot see a space they
cannot see.

**A webhook posts as a bot, not as them.** Messages sent that way are attributed
to the webhook rather than to a person, which is worth knowing when you are
reading a space and wondering who said something.

## Revoking

For an OAuth authorization, the user can remove it from their own Google
account's security settings, and you can remove it for everybody by revoking the
application's access in the Admin console. `spacebar auth logout` forgets the
token locally and does **not** revoke the grant, which is the right thing to run
when retiring a machine and the wrong thing to rely on when one is lost.

For a webhook, delete it in the space it was created in. There is no other way
to revoke one, and anybody holding the URL can post until you do.

## Questions this document should answer and does not

Written down rather than left out, so that the gaps are visible:

- The exact Admin console click path is unverified. See above.
- There is no release client ID, because there is no release.
- Nothing here has been tested against a Workspace organization that actually
  enforces third-party app access control, so the failure a user sees when they
  hit one is described from Google's documentation rather than observed. The
  tool reports it as `admin_policy_enforced` and tells them this page exists.
