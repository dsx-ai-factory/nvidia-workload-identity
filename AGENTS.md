# NVIDIA Workload Identity agent guide

The source repository is `https://github.com/dsx-ai-factory/nvidia-workload-identity`.
Use `gh` for pull requests, GitHub Actions, and release status. The GitLab
repository is a legacy mirror.

## Before starting

- Read the relevant root or component README and any linked local test guide.
- Inspect repository code, active CI workflows, and pinned shared CI templates
  before relying on prior branches, chat history, or generic defaults.
- Keep a FIFO task list during review loops. Finish the current item unless the
  user reprioritizes it.
- Preserve unrelated changes. Do not use destructive Git or filesystem commands
  without explicit authorization.
- Update this guide when the user gives a durable repository-wide instruction.

## Prose, comments, and documentation

- Use NVIDIA Workload Identity as the project name in prose.
- Write compact, factual sentences with plain words and active verbs. Match the
  surrounding comments and documentation. Remove words that add no information.
- State supported behavior directly. Avoid AI-speak, marketing language, vague
  qualifiers, invented compound labels, and unsolicited comparisons with
  unsupported behavior. Do not explain a feature by saying what it is not.
- Avoid duplicating dependency versions in documentation. Link to the pinned
  build configuration and keep version numbers only where behavior depends on them.
- Document current behavior. Omit internal history, revision narrative,
  meta-documentation, and speculative future designs.
- Use plain punctuation. Do not use em dashes, en dashes, curly quotes, or
  semicolons in prose or comments.
- Comments should explain non-obvious behavior, ordering, ownership, security
  constraints, or workarounds. Do not restate the code.
- In Helm templates and YAML, explain non-obvious templating, merge semantics,
  ordering, compatibility, and cross-resource behavior inline.
- Explain supported VM network modes beside the sidecar code and E2E fixtures
  that depend on them, and in the relevant READMEs.
- Root and component READMEs describe the chart and applications users run.
  Keep special test setup, fixtures, and E2E workflow instructions in
  `local-e2e-tests/README.md`. Link there when those instructions are needed.
- Ordinary tests run through standard validation. Do not add README test
  sections, per-package test commands, or test coverage lists for them.
  Adding or changing an ordinary test does not require a README update.
- Keep current documentation in these READMEs and in `docs/`, which holds the
  operator pages the root README links to. Fern publishes operator documentation
  only, through the navigation in `index.yml` and the config in `fern/`. Keep
  developer material such as releases, CI, and E2E out of the Fern navigation.
  Do not add standalone implementation plans unless the user explicitly requests one.
- Prevent documentation drift. When a change affects behavior, configuration, an
  API, a name, or a file location, survey every documentation touchpoint before
  finishing. Search for the old and new terms across the root README, `docs/`,
  component, chart, and example READMEs, `local-e2e-tests/README.md`, Helm values
  comments, diagrams, and `index.yml`. Update each place that describes the
  change. When a page is added, removed, or renamed, update `index.yml`, the
  Documentation list in the root README, and every link to it. Report the
  touchpoints you checked and any you left unchanged.
- Show user-facing API contracts with concrete request and response examples.
- Finish prose and comment updates before editing diagrams. Use simple, readable
  diagrams that retain the relevant components and connections. The network
  setup sidecar diagram must explain both VM bindings. Choose the diagram that
  best explains the setup instead of expanding it with test details.

## Implementation

- Search with `rg`. After deleting or renaming anything, check for stale references
  and run the relevant tests.
- Prefer existing code, standard libraries, native Kubernetes and Helm features,
  maintained dependencies, and upstream APIs. Do not add speculative abstractions,
  compatibility paths, wrappers, or configuration.
- Compose independent components around immutable state. Prefer atomic pointer
  swaps for shared snapshots.
- Keep runtime ownership separate from shared data. Use consumer-owned interfaces
  for dependency boundaries and test control. Cover acceptance and rejection.
- Keep supplied values exact. Do not trim, normalize, case-fold, or coerce input.
- Consume DPF source and dependency changes through upstream releases. Do not
  patch DPF source or override its dependencies in this repository.
- `charts/dsx-identity` installs each management-cluster shard.
- `charts/dsx-imds` and `charts/dsx-spiffe-csi` are DPUService payloads. Keep external
  systems and secret-source resources outside these charts.
- Put deployment configuration in Helm values. Use command-line overrides only
  for dynamic values.
- Prefer upstream defaults. Document every required nondefault setting next to it.
- Apply shared chart settings to every in-scope component. Do not call a value
  shared when a component bypasses it.
- Use repository-pinned tools and dependencies. Pin container images and never
  use `latest`.
- Pin the Fern CLI to the same version in `fern/fern.config.json` and in every
  `fern-api@` install in `.github/workflows/`.
- Use Go 1.26+, standard formatting, structured `slog` logging, context cancellation,
  and the shared telemetry package.
- Never call `os.Exit` outside `main`. Keep cleanup in an error-returning function
  and let `main` translate its result to an exit code.
- Use `http.Header` methods and standard-library parsers for structured headers.
- Use two-space YAML indentation, least-privilege service accounts, ConfigMaps
  and Secrets for configuration, and resource requests, limits, and security
  contexts.
- Use `#!/bin/bash` and `set -euo pipefail` for Bash scripts. Reuse
  `local-e2e-tests/common.sh`. Clean up child processes and temporary resources
  on every exit and avoid arbitrary sleeps.
- Query live clusters manually for diagnosis and validation only. Encode required
  state changes in repeatable deployment code.

## Tests and validation

- Cover changed behavior with positive and negative Go tests where possible.
  Keep ordinary Go tests unprivileged. Do not require root, `sudo`, Linux
  capabilities, or privileged containers to run them.
- A request to test a change does not authorize changes to the test harness or CI
  workflows. Work within the existing setup unless the user asks to change it.
- Use E2E tests when Go tests cannot cover the deployed integration contract.
  Reuse the existing real VM E2E lifecycle for kernel and guest traffic behavior.
- Linux-only tests do not run on macOS. Use CI or the Orca-connected Colossus
  host over SSH. Verify connectivity before relying on remote execution.
- Tests must assert behavior. Do not inspect source, templates, rendered
  manifests, configuration, scripts, logs, readiness, or object counts as proxies
  for application behavior. Do not add meta-tests that inspect repository
  artifacts to assert behavior or wiring.
- Live resources created by a running controller may be asserted directly.
  Static linting, schema checks, and rendering prove syntax and deployability only.
- Keep Kubernetes test resources in manifest files. Deploy them through Skaffold,
  Helm, or Kustomize. Do not embed YAML or manually apply, create, or patch test
  resources with `kubectl` from test scripts.
- Keep tests deterministic. Do not commit tests that fail because of valid
  timing, scheduling, or event loss.
- Synchronize on observable conditions with bounded deadlines. Do not use sleeps
  or rerun-dependent acceptance.
- Use focused unit or integration tests for protocol matrices, lifecycle behavior,
  rotation, expiry, and reconciliation. Keep E2E focused on deployed integration
  contracts in the [local test guide](local-e2e-tests/README.md#local-e2e).
- Do not validate third-party behavior beyond the NVIDIA Workload Identity
  contract that consumes it.
- Use real positive-path components. Do not substitute fake metadata responders,
  test signers, directly signed tokens, or test-only authentication paths.
- Run `make validate` for code or configuration changes and complete required CI
  gates. CI-required validation and E2E jobs must not be manual or allowed to fail.
- Run the full E2E lifecycle after implementation and review work is complete
  when deployed behavior needs verification. Report exactly what ran and what
  remains unverified.

## Reviews

- When the user requests adversarial reviews, run correctness and simplicity
  review separately from prose and comment review. Run them in parallel when
  requested.
- Resolve findings within the authorized scope and have the correctness reviewer
  check the fixes.
- A broader review requested for information only is read-only. Verify suspected
  defects before reporting them and wait for authorization to fix them.
- Check documentation for clarity, unnecessary words, AI-speak, and misplaced
  local test details as well as technical accuracy.

## Git and pull requests

- Read active GitHub Actions and release workflows before creating or updating a
  pull request. Search for an equivalent open pull request or branch first.
- Use a `feat/` or `fix/` branch for release-producing changes.
- Use a Conventional Commit title. Use `feat` for a capability and `fix` for
  corrections, reliability, performance, or maintenance.
- Put a supplied Jira reference in the scope, for example
  `fix(VMAAS-1586): use BM identity resource type`. Use `NO-REF` when none was supplied.
- Wrap commit body lines at 72 characters and keep all lines within 100.
- Keep each pull request focused and its description current. Verify branch,
  title, description, complete commit range, and clean worktree before handoff.
- Open pull requests ready for review unless the user requests Draft status.
  Required CI gates merging and does not determine review readiness.

## Primary commands

```bash
make validate
make -j install
make test
make clean
```
