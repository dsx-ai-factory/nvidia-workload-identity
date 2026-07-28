# DSX Identity agent guide

## Start here

- Read the relevant [README](README.md) section before changing code or configuration.
  It is authoritative for architecture, topology, identity contracts, deployment,
  observability, and local E2E behavior.
- Use plans under `docs/plans/` as historical implementation context, not current
  requirements.
- Inspect repository code, CI configuration, and pinned shared CI templates before
  relying on prior branches, chat history, or generic defaults.
- Keep a FIFO task list during review loops. Finish the current item unless the user
  reprioritizes it.

## Scope and safety

- Preserve unrelated changes. Do not use destructive Git or filesystem commands
  without explicit authorization.
- Search with `rg`. After deleting or renaming anything, search the repository for
  stale references and run the relevant tests.
- Prefer existing code, standard libraries, native Kubernetes and Helm features,
  maintained dependencies, and upstream APIs. Do not add speculative abstractions,
  compatibility paths, wrappers, or configuration.
- Keep supplied values exact. Do not trim, normalize, case-fold, or coerce input.
- Keep comments concise and explain only non-obvious behavior, ordering, ownership,
  security constraints, or workarounds.
- Keep documentation factual and current. Do not add revision narrative,
  meta-documentation, marketing language, or future design speculation.
- Use plain punctuation. Do not use em dashes, en dashes, curly quotes, or semicolons
  in prose or comments.
- Update this guide when the user gives a durable repository-wide agent instruction.

## Git and merge requests

- Read active commitlint, semantic-release, and MR policy configuration before
  creating or updating an MR.
- Search for an equivalent open MR or branch before creating another.
- Use a `feat/` or `fix/` branch for release-producing changes.
- Use a Conventional Commit title recognized by semantic-release. Use `feat` for a
  capability and `fix` for corrections, reliability, performance, or maintenance.
- Include a Jira reference, `NO-REF`, or `NO_REF` as required by MR Cop.
- Validate every commit from the merge base with the repository's pinned commitlint
  policy. Wrap commit body lines at 72 characters and keep all lines within 100.
- Keep each MR focused and its description current. Verify branch, title,
  description, complete commit range, and clean worktree before handoff.
- Keep the MR Draft until required validation and exact-head CI pass.

## Implementation

- `charts/dsx-identity` is the single identity chart. Keep external systems and
  external secret-source resources outside it.
- Put deployment configuration in Helm values. Use command-line overrides only for
  genuinely dynamic values.
- Prefer upstream defaults. Document every required nondefault setting next to it.
- Apply shared chart settings to every in-scope component. Do not call a value shared
  when a component bypasses it.
- Use repository-pinned tools and dependencies. Pin container images and never use
  `latest`.
- Use Go 1.26+, standard formatting, structured `slog` logging, context cancellation,
  and the shared telemetry package.
- Never call `os.Exit` outside `main`. Keep cleanup in an error-returning function and
  let `main` translate its result to an exit code.
- Use `http.Header` methods and the relevant standard-library parser for structured
  headers.
- Use two-space YAML indentation, least-privilege service accounts, ConfigMaps and
  Secrets for configuration, and resource requests, limits, and security contexts.
- Use `#!/bin/bash` and `set -euo pipefail` for Bash scripts. Reuse
  `scripts/common.sh`, clean up child processes and temporary resources on every exit,
  and avoid arbitrary sleeps.
- Query live clusters manually for diagnosis and validation only. Encode required
  state changes in repeatable deployment code.

## Validation

- Tests must assert observed runtime behavior. Do not inspect source, templates,
  rendered manifests, YAML, configuration, scripts, logs, readiness, or object counts
  as proxies for behavior.
- Never add meta-tests that grep, parse, or inspect repository artifacts to assert
  application behavior or wiring.
- Static linting, schema checks, and rendering may validate syntax and deployability
  only.
- Keep tests deterministic. Synchronize on observable conditions with bounded
  deadlines. Do not use sleeps or rerun-dependent acceptance.
- Use focused unit or integration tests for protocol matrices, lifecycle behavior,
  rotation, expiry, and reconciliation. Keep E2E for unique deployed integration
  contracts described in the [README E2E section](README.md#local-e2e).
- Do not validate third-party behavior beyond the exact DSX Identity contract that
  consumes it.
- Use real positive-path components. Do not substitute fake metadata responders,
  test signers, directly signed tokens, or test-only authentication paths.
- CI-required validation and E2E jobs must not be manual or allowed to fail.
- Run `make validate` and the full E2E lifecycle after code or configuration changes.

## Primary commands

```bash
make validate
make -j install
make test
make clean
```
