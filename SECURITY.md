# Security Policy

## Scope

Word Arena is a competitive multiplayer product. Security work covers authentication, session integrity, transport security, economy integrity, matchmaking abuse, cheating and service availability.

## Principles

- Never trust client score, ownership, rewards, timestamps or dictionary decisions.
- Session-resume credentials must be scoped, expiring and non-replayable.
- Sensitive operations require authenticated, authorized server-side checks.
- Economy mutations must be idempotent and auditable.
- Anti-cheat signals are evidence, not unquestionable truth.
- Administrative and moderation actions require audit logs.

## Secrets

Never commit API keys, signing keys, certificates, database credentials or production configuration containing secrets. Use the deployment secret manager and repository/environment secrets.

## Reporting

For a real vulnerability, use GitHub's private vulnerability reporting mechanism when enabled rather than publishing exploit details in a public issue.
