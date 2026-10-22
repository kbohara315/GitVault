---
id: gitvault-release-map
title: Find the route to the full GitVault release
labels: ["wayfinder:map"]
status: open
---

# Find the route to the full GitVault release

## Destination

An agent-executable full-release specification for native GitVault: recoverable full snapshots, explicit GitHub selection, durable jobs, configurable policy, optional cloud copies, retention, scheduling, and platform qualification. The destination of this effort is **the reviewed implementation plan**, not implementing or publishing the application.

## Notes

- Canonical plan: [Full-release implementation plan](../../docs/IMPLEMENTATION.md).
- Skills: improve-codebase-architecture, codebase-design, grill-with-docs, domain-modeling, wayfinder. These are agent workflows, not invented shell commands.
- Technical defaults/composition are delegated to the agents. HITL approval is not: never write a fictional user answer, and never equate planning with credential/deletion/release permission.
- No remote issue tracker or Git repository is configured. This directory is the local-markdown tracker; it has no native blocking/assignment mechanism, so frontmatter relationships provide the fallback.
- Tickets live in `tickets/*.md`. A ticket's `parent` is this map ID; `blocked_by` lists ticket IDs. `assignee: null` means unclaimed. Frontier = open, unblocked, unassigned child tickets. Claim by updating `assignee` and `status` before work. Only the actual assignee's work may resolve it. See [the local tracker convention](README.md).
- Answers live in ticket resolution records. The map only links and gists them. Research can resolve AFK; grilling tickets require a live human exchange. Do not close more than one non-research ticket in a session.
- Implementation task status belongs to `docs/implementation/tasks.json`, not decision-ticket status. Research approval is not task completion.

## Decisions so far

- [Verify the native Go integration choices](tickets/native-integration-evidence.md) — pinned source supports the documented native facilities and candidate dependency requirements; runtime/provider qualification remains separate.

## Not yet specified

No additional technical fog is blocking the first synthetic offline slice. Actual implementation may reveal a precise platform/filesystem capability question or a provider-specific incompatibility. Create a research ticket only when the question is sharp, and link the affected task; do not reopen every decided architecture choice.

## Out of scope

Implementation during this planning effort; production publication; incremental chains; LFS payload storage; GitHub discussion exports; client-side encryption; background OS services; hosted/team features. Runtime confirmation flows can be specified without exercising them against user data.

## Tracker operations

List child frontmatter under `tickets/` to find the frontier. Blocked tickets remain open; closed tickets have a dated `Resolution` entry naming evidence/actor. A restart inspects claims and evidence before changing ownership; it never marks work complete because a process vanished. Read-only independent research findings may be imported as evidence, but an agent must not fabricate a claim timestamp predating that work.
