---
status: accepted-for-planning
date: 2026-10-22
---
# Compose deep recovery and run modules

Under the user's delegated technical-planning request, keep shared artifact rules inside one `internal/backup` module and introduce durable run coordination only after offline restoration is proven. Splitting Git execution, manifest interpretation, and restoration into public modules immediately would spread one invariant across several interfaces before real callers exist.

Compose concrete adapters at startup. Introduce consumer-owned interfaces only for real variation or deterministic testing. Typed configuration owns normalization and immutable revisions; each module enforces the options it receives. Configuration cannot disable verification, no-replace publication, ownership, secret isolation, or TLS validation.

This refines the package sketch, not the accepted native-UI/full-bundle choices in [the foundation decision](../decisions/0001-foundation.md). It does not authorize production publication, real credential access, or deletion of user data.
