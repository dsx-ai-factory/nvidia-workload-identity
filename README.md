# NVIDIA Workload Identity

NVIDIA Workload Identity turns shard-local SPIRE identities into DSX JWTs that
relying parties can validate across shards. An identity shard contains an
independent SPIRE deployment and a co-located NVIDIA Workload Identity Keys
deployment. NVIDIA Workload Identity Keys validates only its local SPIRE
JWT-SVIDs, issues DSX JWTs, and distributes its public signing keys through
the external DSX Event Bus.

There is no SPIRE federation between shards. Cross-shard trust starts after
token exchange, when another shard or relying party validates a DSX JWT with
the aggregate public keys served by any NVIDIA Workload Identity Keys
instance.


## Documentation

- [Overview](docs/overview.md) covers the user guide, component list, SPIRE upgrades, and deployment.
- [Architecture](docs/architecture.md) describes the identity hierarchy and identity delivery.
- [Identity contract](docs/identity-contract.md) defines DSX JWT subjects and the host and VM flows.
- [Observability](docs/observability.md) covers logs, metrics, and traces.

## Release candidates

1. Send feature PRs to `master`.
2. When ready for QA, cut protected `release/X.Y.Z` from a tested `master` commit.
3. Send release fixes to that branch, then cherry-pick them back to `master` through PRs.

Release-worthy merges create `vX.Y.Z-rc.N` on GitHub. With
`DSX_RC_ARTIFACT_PUBLISHER=external`, GitLab publishes the matching images and charts
to NGC. The [RC workflow](.github/workflows/release-rc.yml) verifies those artifacts
and records their digests for promotion. The default publisher is GitHub.
PRs only run validation. This workflow does not publish from `master` or promote RCs to stable.

[One-time setup](https://github.com/dsx-ai-factory/dsx-github-actions/blob/main/docs/release-candidate-publishing.md#prerequisites-and-scope):
configure the runner, protected release environment, NGC key, and release permissions.
External publishing also needs GitLab mirror pipelines, protected RC tags, and
the protected GitLab variable `GITLAB_PUBLISH_ARTIFACTS=true`.

### Promote an RC

Promotion requires the protected `stable-release` environment and repository variable
`DSX_RELEASE_MODE=rc-promotion`. Administrators must complete the
[release setup](https://github.com/dsx-ai-factory/dsx-github-actions/blob/main/docs/promote-rc.md)
before the first run.
Keep the promotion workflow and policy on `master`. The selected RC must use
an approved publisher revision recorded in `.github/rc-promotion.json`, with
`promotion-manifest: true`. Older RCs without that manifest cannot be promoted.

1. Have QA validate a published RC with a successful manifest capture.
2. Open **Actions > Promote RC > Run workflow**. Select `master` and enter `X.Y.Z-rc.N`.
3. Confirm the RC version and source commit with the configured release reviewers.
4. After approval, the workflow publishes `X.Y.Z` using the tested image digests.

The [promotion workflow](.github/workflows/promote-rc.yml) calls the shared publisher.
Images keep their tested bytes. Charts update release metadata and declared image
tags while preserving templates and vendored dependencies.

## CI

PRs run CI on `copy-pr-bot`'s `pull-request/<number>` branches. Pushes to `master`
and `release/**` also run `make validate`, the full [local E2E lifecycle](local-e2e-tests/README.md#workflow),
secret detection, and container scans. The bot must be installed for this repo,
and its [configuration](.github/copy-pr-bot.yaml) must be on `master` to take effect.
The shared build action scans both image platforms with Syft and Grype, uploads
SBOMs and reports, and fails on unsuppressed Critical vulnerabilities or scanner
errors. All image scans use the repository [Grype configuration](.github/grype.yaml).
These scans do not submit results to Pulse.

All product jobs use the event's commit and read-only repository permissions,
without release secrets. The RC workflow calls product CI once and waits on
`needs: dsx-component-ci`, without listing individual jobs. The [image inventory](.github/images.json) is shared by scanning
and publishing. See the [local E2E guide](local-e2e-tests/README.md#workflow) for runner
requirements.

### GitLab CI

The GitLab mirror runs SonarQube analysis against the existing
[project](https://sonar.nvidia.com/dashboard?id=NBUSW_DSX_DSX_nvidia-workload-identity).
Its analysis token comes from Vault. This job does not register releases in nSpect.
Pulse and SonarQube failures appear as warnings and do not block GitLab pipelines.

NGC publication is off by default. The artifact pipeline is loaded only when
`GITLAB_PUBLISH_ARTIFACTS=true` and the pipeline targets a protected
`vX.Y.Z-rc.N` tag. GitHub validation and E2E must pass before creating the tag.
GitLab builds and publishes the tagged source without repeating those tests.
GitLab does not create release tags. Switch the GitHub publisher to `external`
before enabling GitLab publication. Stable promotion still copies the verified
RC artifacts through GitHub.

Enable mirror-triggered pipelines only after this configuration is merged.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development and DCO sign-off,
[GOVERNANCE.md](GOVERNANCE.md) for maintainers and review, and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community standards. Report
security issues through [SECURITY.md](SECURITY.md).

## Licensing

NVIDIA Workload Identity uses the [Apache License 2.0](LICENSE). Third-party
code retains its upstream licenses and notices.
[THIRD-PARTY-NOTICES](THIRD-PARTY-NOTICES) contains dependency attribution and
license texts for Go, Rust, and the pinned submodules.

`make update-licenses` runs SkyWalking Eyes to add source headers and uses
`go-licenses` and `cargo licenses` to regenerate dependency notices.
SkyWalking Eyes checks Helm template headers but cannot add their comment syntax.
Add those headers when creating templates. `make validate` checks headers
and rejects stale notices through the required CI Validate job.

The repository pins these public submodules:

| Path | Source | License |
| --- | --- | --- |
| `third-party/doca-platform-foundation` | [NVIDIA/doca-platform](https://github.com/NVIDIA/doca-platform) | [Apache-2.0](third-party/doca-platform-foundation/LICENSE) |
| `third-party/ovn-kubernetes` | [ovn-org/ovn-kubernetes](https://github.com/ovn-org/ovn-kubernetes) | [Apache-2.0](third-party/ovn-kubernetes/LICENSE) |

## Local E2E

The [local E2E guide](local-e2e-tests/README.md) describes the Linux Kind setup, VM
bindings, identity checks, and install/test/clean workflow.

## Disclaimer

NOTICE AND DISCLAIMER: This software automatically retrieves, accesses or interacts with external materials.  Those retrieved materials are not distributed with this software and are governed solely by separate terms, conditions and licenses.  You are solely responsible for finding, reviewing and complying with all applicable terms, conditions, and licenses, and for verifying the security, integrity and suitability of any retrieved materials for your specific use case. This software is provided "AS IS", without warranty of any kind. The author makes no representations or warranties regarding any retrieved materials, and assumes no liability for any losses, damages, liabilities or legal consequences from your use or inability to use this software or any retrieved materials. Use this software and the retrieved materials at your own risk.
