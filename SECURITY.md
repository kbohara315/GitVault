# Security policy

GitVault is pre-release and is not ready for sensitive backups. There are no supported production releases yet.

## Report a vulnerability

No private reporting address or hosted repository has been configured. Do not post tokens, private repository contents, or sensitive exploit details in public issues. Before publication, the maintainer must configure a private reporting channel and replace this section with contact and response expectations.

## Development rules

- Never commit credentials or real repository backups.
- Never use a fixed application key as secret protection.
- Do not use `.gitignore` as a security boundary. Review staged content before committing.
- Git bundles are **not encrypted**. They can contain private source code and historical secrets.
- Checksums detect accidental changes; they do not authenticate an attacker-controlled snapshot and manifest.
- Use a separate destination for restoration. Do not execute restored project code or hooks.

See [the threat model and security design](docs/SECURITY.md).
