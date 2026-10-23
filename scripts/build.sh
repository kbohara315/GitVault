#!/bin/sh
set -eu

# Go/MyGo output inherits the caller's umask. A group-writable executable
# cannot serve as GitVault's credential helper. Apply this before compilation
# and packaging, not as a repair of users' backup folders at application startup.
umask 022
exec go tool mygo build "$@"
