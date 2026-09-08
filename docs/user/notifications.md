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

Each channel subscribes to any combination of the three events and covers
either **all applications** in the organization or **one application**.
Use **Send a test** on a channel to confirm it reaches its destination.

Delivery is retried a few times if the destination is unreachable; a
destination that keeps refusing is given up on for that event.
