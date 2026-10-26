# Product

<!-- impeccable:product-schema 1 -->

Scope: the estate hub dashboard at `/hub` (`internal/api/hub_dashboard.html`). The rest of this
repository is upstream CLIProxyAPI and has no design surface of its own. Every fact below comes from
the coordinator's brief of 26 Oct 2026 and `docs/estate-hub-plan.md`; none came from an interview
with Waseem, so treat them as inferred until he confirms them.

## Platform

web

## Users

One person: Waseem, who owns every AI subscription on the estate. He opens the page many times a
day, mostly on his phone (390 px wide), sometimes on a desktop browser, in light and dark mode.

## Product Purpose

The hub is one proxy that holds every AI subscription login (Claude, Codex, Kimi, Grok, Antigravity,
Muse, Devin, and API-key providers such as DeepSeek, CommandCode, GLM and OpenCode Go) and routes
each new conversation to the account whose allowance resets soonest ("consume-first"). The page
answers two questions at a glance: what will be spent next, and what is nearly gone. Success is
that he can stop opening CodexBar and sysdash, the tools it replaces.

## Operating Context

- Read often, acted on rarely. Actions are: hold an account for a time, release a hold, disable or
  enable it, set its name and its groups.
- The figures come from the hub's own traffic and a usage poller (every ten minutes on the live hub);
  some providers report nothing.
- Vocabulary he already uses (sysdash, CodexBar): "consume-first", "held", "spent", "resets in",
  "+12 spare" / "-8 over" / "on pace", "banked resets", "lasts to reset" / "runs out in".

## Capabilities and Constraints

- One HTML file embedded in the Go binary. No build step, no external fonts or scripts, no network
  calls except the hub's own management API.
- The management key is typed once and kept in the browser's local storage.
- Every figure and every management action the first version shipped stays.

## Product Principles

1. The answer first: what to spend next and what is running out are readable without scrolling.
2. Less is more: a figure appears once, in the place it is needed.
3. Show the router's truth: the order on the page is the order the hub will use.
4. Name things the way Waseem does (account names, plan names, his sysdash labels).
