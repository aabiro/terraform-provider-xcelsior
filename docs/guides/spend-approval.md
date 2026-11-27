---
page_title: "Spend approval under terraform apply"
subcategory: ""
description: |-
  How launches are approved when nobody is watching the apply.
---

# Spend approval under `terraform apply`

Xcelsior never starts billed compute without an approved plan. This guide
explains how that fits a pipeline that runs `terraform apply` with nobody at
the dashboard.

## The flow

For each new `xcelsior_instance` or `xcelsior_serverless_endpoint`:

1. **`terraform plan`** calls a quote that runs every check a real launch runs
   (validation, pricing, placement, approval) and persists nothing. A spec the
   API refuses fails the plan. A launch that will need a person shows a
   warning, so whoever reviews the plan sees "this apply will stop for
   approval" before it merges.
2. **`terraform apply`** creates the launch plan.
3. If the plan is inside the token's **spend envelope**, the provider approves
   it with the same token and executes it.
4. If not, the provider waits up to `approval_timeout` for a person to approve
   it in the dashboard (Launch plans). Approved: it executes. Revoked or
   expired: apply fails. Timed out: the provider **revokes the plan** and
   fails, naming the link and the reasons.
5. If the price moved beyond the approved tolerance before execution, the API
   answers with a replacement plan, and the provider starts again from step 3
   with it, at most three times.
6. If the answer to execute is lost, or the API is briefly unavailable (502,
   503, 504), the provider repeats the execute. The API executes a plan
   exactly once, so the repeat returns the instance the first call launched
   instead of launching a second. If execute fails for any other reason, the
   provider revokes the plan, so no approved spend is left standing after a
   failed apply.

A plan is valid for ten minutes, because its quote is. That is why
`terraform plan` never creates one: a plan reviewed in a pull request and
applied after merge would have expired.

## Spend envelopes

An envelope is a set of ceilings a person puts on one token:

| Ceiling | Meaning |
|---|---|
| Daily (required) | Total of launch estimates the token may approve itself per UTC day |
| Hourly | The same per UTC hour |
| Per launch | The most one launch may be estimated at |
| GPUs per launch | The most GPUs one launch may ask for |
| GPU models, regions | Allow-lists |

Amounts are integer micros of the account currency. A launch's estimate is one
hour of its rate (for an endpoint, one hour at `max_workers`). So a daily
ceiling bounds the launches the token starts on its own, not what those
launches go on to cost while they run: that remains the wallet's job
(per-second billing, holds, and a stop at zero balance).

Only a person signed in to Xcelsior can set or raise an envelope:
`PUT /api/v1/spend-envelopes/{client_id}` refuses every API token, so a token
can never raise its own. A page for this in the dashboard is not built yet;
until it is, set one from a signed-in session. A token can read its envelope
and what is left of the current hour and day (`GET /api/v1/spend-envelope`),
and can remove its own envelope.

## Recommendations

* An unattended pipeline: an envelope sized for the pipeline, and the default
  `approval_timeout = "0s"`, so a launch outside it fails fast instead of
  hanging the job.
* An apply run by a person: `approval_timeout = "15m"`, and approve in the
  dashboard when the apply waits.
* Protect volumes that hold data with `lifecycle { prevent_destroy = true }`:
  a destroy needs no approval.
