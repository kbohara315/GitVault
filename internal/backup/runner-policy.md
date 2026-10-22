# Controlled Git runner (I02)

## Current qualification

The executable is resolved once from the desktop process's trusted `PATH`.
Only Linux is execution-enabled. Linux uses a new process group and `waitid`
with `WNOWAIT`: the leader's PID stays reserved until the final group kill,
then `Cmd.Wait` reaps that direct child. A per-process mutex prevents cancellation
from signalling a stale/reused process-group ID after reaping. The runner kills
remaining group members even when the leader exits successfully first.

Descendants must remain in Git's inherited process group. Executing adversarial
programs which deliberately escape with `setsid` is not supported; application
code execution as the current user is outside this boundary. No process-global
subreaper setting is changed. Dead orphan descendants are reaped by the host's
init, not by this process; Linux tests distinguish a non-running zombie from a
surviving writer. The direct child is always waited. Kernel-uninterruptible I/O
cannot be given a strict wall-clock kill guarantee by a userspace process group.

Darwin and Windows fail with `ErrGitUnsupported` before executing Git. Merely
cross-compiling is not native qualification. Windows needs creation suspended,
job-object assignment, then resume; assigning a running `exec.Cmd` to a job has
an escape race and is not an acceptable fallback.

## Pipe and output ownership

The runner owns both read pipe ends independently of `Cmd.Wait`. It kills the
group before draining, limits drain time, explicitly closes the pipes if needed,
and joins both reader goroutines. `WaitDelay` also bounds Go's stdin-copy path.
Machine output defaults to 32 MiB; excess stops the process and returns an error,
never apparently valid truncated data. Input is bounded by that same budget.
Diagnostics default to a 64 KiB accounting budget. Raw stderr is discarded:
arbitrary repository-controlled text cannot be safely redacted by a token regex.
The exposed diagnostic contains only byte counts and a truncation indication.
Errors expose a typed cause and exit status, never argv/environment/raw stderr.

## Environment and command policy

Options and the environment allowlist are captured once at construction. A
private scratch root is required; unique per-command HOME/XDG/template/hook/temp
directories are created below it and only those owned directories are removed.
PATH and native OS bootstrap variables are the only inherited entries. No
inherited `GIT_*`, dynamic-loader, credential, trace, proxy, editor or helper
variables survive. Git receives explicit system/global config isolation,
no-prompt/no-pager/no-replacement/no-lazy-fetch flags, fixed hook/template paths,
disabled fsmonitor/maintenance/submodule recursion, and protocol deny defaults.
Repository discovery cannot climb above the requested working directory.

Command execution is package-private and built with argv, never a shell command
string. Offline mode admits only fixed recovery/probe builtins; transport and
external-alias builtins are rejected before launching Git. Local-source mode
admits exactly `clone --mirror --no-local -- ABSOLUTE_SOURCE ABSOLUTE_NEW_TARGET`
after source inspection. No URLs, SCP syntax, helpers, arbitrary clone options,
existing target or network transport is admitted. There is no authenticated or
network-enabled runner policy in I02. Private engine arguments remain trusted
program code; future source/ref/path builders must validate untrusted operands.

## Source inspection and probes

Source inspection supports ordinary `.git` directories and bare repositories,
rejects symlinked critical paths, external/HTTP alternates, linked-worktree
indirection, shallow repositories, local includes, partial-clone/promisor state,
alternate-ref commands and unqualified repository extensions. Local config is
bounded and inspected using `config --file --no-includes` before repository
discovery. The discovered Git directory, bare state and object format must match
the inspected location. This check is not a source lease: I03 must recheck under
ownership and enforce its own private-copy, publication and destination safety.

Capability probing uses synthetic disposable repositories for both SHA-1 and
SHA-256. It performs actual init, object/commit/ref writes, local mirror transfer
of detached-only HEAD, full-bundle verification against an independent empty
repository, unbundle, explicit ref/HEAD reconstruction and `fsck --full`.
Evidence reports those operations, not a version-string guess. These temporary
probes are not snapshot production or the I03 recovery/publication engine.
