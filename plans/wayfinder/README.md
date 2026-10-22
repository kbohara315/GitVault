# Local decision tracker

No hosted repository/issue tracker is configured. This folder is the local-Markdown tracker for the [GitVault release map](MAP.md); no GitHub issues or remote relationships are implied.

Each ticket has frontmatter: `id`, `title`, `parent`, `labels`, `status`, `assignee`, and `blocked_by`. Labels include `wayfinder:research` or `wayfinder:grilling`. The map has `wayfinder:map`. Native dependency support is absent, so `blocked_by` is the local convention. Link tickets by title in human-readable text.

Frontier: open, unassigned tickets whose `blocked_by` tickets are closed. A claim assigns the actual actor before work. Close a research ticket only after recording sourced findings in a `Resolution` section. A HITL grilling ticket closes only through an actual human exchange; agents cannot supply that answer. Keep release gates open while independent implementation work proceeds.

This tracker contains **decisions**, not the build task queue. [The implementation DAG](../../docs/implementation/tasks.json) is separate. Do not mark a build task done because a decision ticket closed.
