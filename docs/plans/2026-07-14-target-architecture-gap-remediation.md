# DSX Identity Remediation Change Plan

This plan records the branch work and its verification. Architecture and the
accepted corrections to the source SDD live in `README.md`, component READMEs,
configuration comments, and `AGENTS.md`.

## Scope

- Align operator and dynamically tenant-supplied identity paths.
- Prove repository-owned identity components and associations in local E2E.
- Emulate external DPU and NICo boundaries without modeling their hardware,
  host operating systems, or physical link-local interception.

## Identity ownership and registration

- [x] Use generic DNS-label tenant identifiers without an `nca-` convention.
- [x] Configure one operator tenant for every DPU, Kubernetes Pod, and
  Kubernetes worker host identity.
- [x] Keep DSX Identity Keys tenant-agnostic and preserve the tenant already present in
  each validated local SPIFFE ID.
- [x] Reconcile only DPU and LaunchLayer sources. Do not configure a Kubernetes
  worker inventory or node-to-DPU mapping.
- [x] Derive standalone host and VM subjects from the LaunchLayer tenant.
- [x] Parent VM entries and Kubernetes Pod entries to the operator worker's
  `dsx_imds` agent.
- [x] Keep DPU token-fetcher selectors separate from DPU-hosted DSX IMDS
  selectors.

## Delivery paths

- [x] Use `dsx-token-fetcher` for Kubernetes Pod sidecars and the emulated
  external DPU process.
- [x] Use the real `metadata-service` for worker-host and KubeVirt VM identity
  delivery.
- [x] Use direct DSX Identity Keys HTTP for DPU-hosted metadata and DSX Identity
  Keys Proxy CSI for KubeVirt metadata.
- [x] Use `oauth2.Config.Exchange` with RFC 8693 parameters instead of a custom
  token-response client.
- [x] Let the configured URL and its owning client select transport. Remove
  plaintext override flags and shared URL-policy helpers.

## Chart and local deployment

- [x] Keep one DSX Identity Controller and one controller-manager per shard.
- [x] Install each identity chart once and use its normal DSX Identity Keys deployment.
- [x] Confine the local `k8s_psat` agent and DSX Identity Keys Pod to the Phase 0 seed
  node while worker Pods use the emulated `dsx_imds` host agent.
- [x] Keep the Event Bus, OAuth2 provider, Kyverno runtime, KubeVirt, OpenBao,
  and local clients outside the identity chart.
- [x] Pass SPIRE deployment and datastore choices through to the official chart.
- [x] Separate Helm deployments from raw fixture application and remove
  redundant Helm manifest renderers from Helm-deployed Skaffold configs.

## Runtime proof and documentation

- [x] Assert live SPIRE subjects, parents, selectors, hints, and tenant sources.
- [x] Prove real Pod, DPU token-file, worker-host IMDS, and KubeVirt VM delivery.
- [x] Prove local SPIRE issuer rejection, aggregate JWKS verification, OpenBao
  policy selection, retained-state reconstruction, key rotation, and lease
  expiry.
- [x] Keep the deployed architecture and the local emulation boundary separate
  in the README diagrams and prose.
- [x] Remove obsolete component names, tenant ownership claims, and deployment
  prescriptions from first-party documentation.

## Final verification

- [x] `make validate`
- [x] `make clean`
- [x] `make -j install`
- [x] `make test`
- [x] `make clean`

The required GitLab pipeline is tracked on the merge request. It must pass for
the final branch commit without a rerun-dependent result.
