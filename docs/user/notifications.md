---
title: Notifications
description: Be told when a run needs approval, fails, or finds drift — by email, in Slack, or through any webhook — by adding notification channels to your organization, choosing the events each receives, and limiting a channel to one application.
category: User
tags: [notifications, email, slack, webhook, approvals, drift, runs]
---

# Notifications

Spacefleet can tell you when something needs a person, without anyone
watching the run page. An organization admin adds **notification channels**
under **Admin → Notifications**; each channel is a destination and the
events it receives.

## Events

| Event | When it fires |
| --- | --- |
| **Awaiting approval** | A run parks at an approval gate — for example an OpenTofu apply waiting for someone to review the plan. |
| **Run failed** | A run settles failed or partially failed, including a run whose approval timed out or that was abandoned. |
| **Drift detected** | A drift check finishes and at least one resource changed outside of OpenTofu. |

Every notification names the application and the run, says which steps
failed, were skipped, or are waiting, lists the drifted resources for a
drift event, and links to the run page.

## Channels

- **Email** — an email address. This needs outbound email configured on
  your deployment (the same setting that sends invitations); ask your
  operator if messages don't arrive.
- **Slack** — a [Slack incoming webhook](https://api.slack.com/messaging/webhooks)
  URL. The message is posted to the channel the webhook was created for.
- **Webhook** — any `http(s)` URL. Spacefleet sends a `POST` with a JSON body
  and an `X-Spacefleet-Event` header naming the event, so an on-call tool or
  an automation of your own can act on it. The body carries a `headline` and
  an `event` object with the application, run id, action, status, message,
  who started the run, the notable steps, any drifted resources, and the
  run's URL.

A webhook URL is a secret: it is stored encrypted and never shown again —
the list shows only its host. Deleting and re-adding a channel is the way to
change a URL you no longer have.

### Verifying webhook deliveries

Give a webhook channel a **signing secret** and every delivery carries an
`X-Spacefleet-Signature-256` header: `sha256=` followed by the hex
HMAC-SHA256 of the request body under that secret — the same scheme GitHub
uses for its `X-Hub-Signature-256` header, so any verifier written for that
works unchanged. Compute the HMAC over the raw body bytes exactly as
received and compare it to the header in constant time; reject anything
that does not match. The secret is stored encrypted and never shown again;
a channel with one is marked **signed** in the list. Without a secret,
deliveries are unsigned. To rotate a secret, or to stop signing, use the
key icon on the channel's row: a new secret takes effect from the next
delivery, so update the receiver first if it rejects unsigned or
mis-signed requests.

Each channel subscribes to any combination of the three events and covers
either **all applications** in the organization or **one application**.
Use **Send a test** on a channel to confirm it reaches its destination.

Delivery is retried a few times if the destination is unreachable; a
destination that keeps refusing is given up on for that event.
