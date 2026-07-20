# E2E performance investigation

Date: 2026-07-18

Baseline commit: `ba3ec13c58c532b4fa1eb91b84229310fdf27a48`

## Executive summary

The measured warm-cache E2E lifecycle takes 8m21.59s before validation and cleanup. A less-warm deployment took 12m08.12s by itself, making a full run roughly 14m30s. The dominant costs are deployment, especially KubeVirt and its network dependencies, followed by verification that restarts DSX Identity Keys four times and runs deterministic behavior against live infrastructure.

The topology is large, but most third-party components are required by explicit DSX Identity integration contracts. Removing KubeVirt, OVN-Kubernetes, SPIRE, the OAuth2 Event Bus path, or OpenBao would make the suite faster by removing required coverage. That is not recommended.

The first speedups should instead remove deployed-environment churn and duplicate assertions:

1. Move DSX Identity Keys restart reconstruction, rotation, and lease expiry out of E2E. Existing focused tests already exercise those behaviors, including deterministic virtual-time expiry and retained replay into a restarted client.
2. Stop creating, patching, and deleting extra DPU, host, and VM sources in E2E. Existing reconciler tests cover lifecycle behavior. Keep the deployed static-source and exact live SPIRE registration assertions.
3. Remove the repeated VM IMDS negative protocol matrix and most OpenBao generic JWT rejection cases. Keep the real positive VM path and the project-specific malformed DSX subject rejection.
4. Run the two remaining read-only verification scripts concurrently.
5. Reuse the deployed probe binary for aggregate verification instead of starting host `go test` processes.

These changes target at least the measured 44s restart and rotation block, both 9.4s lease-expiry waits, duplicated controller and protocol checks, and serial composition. They preserve every project-specific positive runtime path.

## Method

Measurements came from an isolated Git worktree at the baseline commit. Product changes in the main worktree did not affect the baseline. The host had 16 logical CPUs, 122 GiB RAM, and Linux 7.0.0-22-generic on x86-64.

Commands were timed with `/usr/bin/time -v`. Deployment substage timings came from Skaffold output and Kubernetes creation and condition timestamps. The persistent Zot pull-through cache was preserved because that is the repository's normal workflow. Two deployment samples show the cache sensitivity:

- A fresh-cluster run with a warm persistent upstream cache and locally available source images.
- An earlier fresh deployment with colder third-party image availability.

The clean baseline sequence was:

```bash
make clean
make -j2 k1-cluster k2-cluster
make -j install
make test
```

`make validate` was measured separately. No reported baseline timing includes the provisional performance changes.

## Lifecycle timings and concurrency

| Stage | Wall time | Concurrency | Notes |
| --- | ---: | --- | --- |
| Cleanup | 4.56s | k1 then k2 | Deletes both Kind clusters. Zot is preserved. |
| Cluster creation | 31.46s | k1 and k2 parallel | `make -j` overlaps both cluster targets. The shared registry target is made once. |
| Deployment, warm persistent cache | 359.50s | Skaffold dependency graph | Fresh clusters, all ten source images found locally. |
| Deployment, colder third-party availability | 728.12s | Skaffold dependency graph | k2 Kyverno stabilization alone rose from 33.46s to 212.46s while OVN images became available. |
| Verification | 110.63s | fully serial | k2 identity verification runs before cross-shard verification. |
| Validation | 9.98s | targets serial, Go packages parallel | `make validate` does not request make-level parallelism. `go test -race ./...` parallelizes packages. |
| Warm-cache lifecycle, excluding validation and cleanup | 501.59s | mixed | 8m21.59s total. |
| Colder-cache lifecycle estimate, excluding validation and cleanup | about 870s | mixed | About 14m30s using the measured 728.12s deployment and 110.63s verification. |

Cluster creation is already effectively parallel. Verification is not parallel at all. Deployment has substantial dependency-aware overlap, but critical provider hooks serialize consumers correctly.

## Deployment stage inventory

Skaffold builds source artifacts with unlimited configured concurrency. In the measured fresh-cluster warm-cache run it found all ten source images locally, so no source build was required. A prior run built nine artifacts in parallel, with individual cached build commands taking roughly 1.1s to 2.2s.

### Image loading

The graph visits the shared image config repeatedly through transitive requirements. The log contains 28 `Loading images into kind cluster nodes` passes.

| Image-load activity | Measured time |
| --- | ---: |
| Initial k1 source image load | 9.35s |
| Overlapping duplicate k1 passes | 3.58s and 3.51s |
| Initial k2 source image loads | 9.59s, 4.80s, and 4.75s |
| Later cached checks | generally 0.04s to 0.05s each |

The repeated passes are noisy and create some initial contention, but most are cheap cache checks. The repository requires native Skaffold Kind loading, so this is an observation and a lower-priority optimization, not a reason to add custom image-loading orchestration.

### k1 components

| Component or stage | Measured readiness or apply time | Dependency and overlap notes |
| --- | ---: | --- |
| SPIRE CRDs | 0.40s | Independent provider branch. |
| Kyverno | 12.45s stabilization | Independent provider branch. Required before chart policies are admitted. |
| MetalLB | 0.41s | Runs in the gateway branch. |
| Envoy Gateway CRDs | 0.45s | Provider for Envoy Gateway. |
| Envoy Gateway runtime | 11.51s stabilization | Follows its CRDs. |
| Gateway resources | not separately emitted | Follows Envoy Gateway and MetalLB. |
| Keycloak | not separately emitted | Follows Event Bus secrets and gateway resources. Its wait is inside the provider chain. |
| Event Bus | installed at 01:46:38 | Follows Keycloak, image, and chart dependency configs. |
| Identity chart | 51.51s stabilization | Began at 01:46:39. Follows Event Bus, Kyverno, SPIRE CRDs, images, and secrets. |
| Worker-host boundary | not separately emitted | Follows the identity chart. |
| Worker-host SPIRE agent | not separately emitted | Follows the stable boundary config. |
| k1 fixture | 5.51s stabilization | Follows the worker-host agent. |
| OpenBao | 1.52s stabilization | Follows identity and gateway resources. It overlaps later k2 work. |

Some provider hooks do not emit standalone elapsed values. Their cost is included in the 359.50s deployment wall time and constrained by the dependency graph shown above.

### k2 components

| Component or stage | Measured readiness or apply time | Dependency and overlap notes |
| --- | ---: | --- |
| SPIRE CRDs | 0.40s | Independent provider branch. |
| Source fixture APIs | 0.40s | Follows SPIRE CRDs. |
| Network APIs | 0.41s | Provider for OVN-Kubernetes. |
| OVN-Kubernetes apply gate | 0.42s | Actual node-network readiness is visible in downstream waits. |
| Kyverno | 33.46s warm, 212.46s colder | Follows OVN. The large variance is dominated by third-party image availability and node networking. |
| KubeVirt operator apply gate | 0.42s | Follows OVN. |
| KubeVirt release apply gate | 0.42s | Its post-hook then waits for KubeVirt Available. |
| KubeVirt Available hook | about 101s | Resource created at 01:49:20 and became Available at 01:51:01. This is the warm-run deployment critical path. |
| Identity chart | 15.46s warm, 66.46s earlier | Follows source APIs, Kyverno, k1 gateway and Event Bus, images, and secrets. |
| Worker-host boundary | not separately emitted | Follows the identity chart. |
| Worker-host SPIRE agent | not separately emitted | Follows the stable boundary config. |
| k2 fixture | 3.50s stabilization | Follows the worker-host agent. |
| VM resources | applied after KubeVirt and worker host | VM and VMI became Ready about 12s after creation. Deployment returned before VM readiness. |
| External client Pod | Ready about 5s after creation | Includes two containers. The real verification waits later as needed. |

KubeVirt and OVN are expensive, but the real KubeVirt VM to metadata-service to DSX Identity Keys Proxy CSI path is an explicit project requirement. Replacing it with a fake would invalidate the main VM contract.

### Largest k2 and k1 runtime images

Sizes below are the images reported by the Kind node runtime. They are useful for explaining cold-cache variance, not exact registry transfer sizes.

| Image or component | Size |
| --- | ---: |
| Ubuntu VM containerDisk | 619.6 MB |
| KubeVirt virt-launcher | 526.0 MB |
| OVN-Kubernetes | 404.5 MB |
| KubeVirt virt-handler | 263.2 MB |
| `nicolaka/netshoot` external client | 218.7 MB |
| Keycloak | 234.8 MB |
| Multus | 182.5 MB |
| KubeVirt virt-operator | 125.3 MB |
| KubeVirt virt-api | 80.7 MB |
| KubeVirt virt-controller | 79.1 MB |
| OpenBao | 76.7 MB |
| Envoy Gateway | 74.2 MB |

The external client Pod contains both the 218.7 MB netshoot container and the repository-built `e2e-probe` container. Verification executes SSH only in netshoot. Repository search found no execution of the second container in this Pod. Consolidating these requires adding an SSH client to an existing repository image and measuring the resulting pull and build tradeoff. It is a valid follow-up experiment, but it is not part of the first low-risk change set.

## Verification timings and concurrency

| Verification component | Wall time | Mutates the environment | Current concurrency |
| --- | ---: | --- | --- |
| `scripts/k2/verify-identity.sh` | 17.66s in an isolated warm run | Yes | Runs first and alone. |
| `scripts/verify-e2e.sh` | 98.73s in an isolated warm run | Yes | Runs second and alone. |
| Complete clean-deploy `make test` | 110.63s | Yes | Fully serial. |
| Live MQTT authorization | 2.01s | No | Within the serial cross-shard script. |
| First live publication expiry | 9.41s | Publishes a temporary retained key | Within the serial cross-shard script. |
| Second live publication expiry | 9.52s | Publishes a temporary retained key | Within the serial cross-shard script. |
| DSX Identity Keys restart and rotation block | about 44s | Four rollouts and two signing-secret updates | Within the serial cross-shard script. |

The two separately measured scripts sum to 116.39s. The 5.76s difference from the clean-deploy 110.63s run is ordinary readiness and cache variation.

### Deployed-environment mutations

The cross-shard script:

- Restarts k1 DSX Identity Keys once to prove retained reconstruction.
- Restarts k2 DSX Identity Keys once to prove retained reconstruction.
- Replaces the k2 signing Secret and restarts k2 for rotation.
- Restores the original k2 signing Secret and restarts k2 again.
- Runs live publication lease expiry twice, each with a real 9.4s to 9.5s wait.

ReplicaSet timestamps place the whole restart, rotation, and repeated-assertion block between 01:52:47 and 01:53:31, about 44s. Individual replacement Pods became Ready in roughly 4s to 7s. These operations consume a large fraction of verification and introduce rollout and wall-clock sensitivity.

The k2 identity script also creates, patches, and deletes temporary DPU, host, and VM sources. It checks create, update, schema rejection, deletion, and recovery against the deployed controller. This is useful behavior, but it duplicates focused reconciler coverage and turns the shared test environment into a mutable fixture.

### Duplicate and misplaced assertions

| E2E behavior | Existing focused coverage | Recommendation |
| --- | --- | --- |
| Retained key reconstruction after client restart | `TestMQTTSynchronizesRetainedPublicKeys` starts an in-process broker and multiple shards, disconnects and reconnects, then starts a new client and verifies retained reconstruction. | Remove deployed DSX Identity Keys restarts. |
| Key rotation overlap and aggregate convergence | The same MQTT synchronization test exercises rotated keys across shards. | Remove deployed Secret replacement and rollouts. |
| Publication lease expiry | `TestKeyRegistryBackgroundLeaseExpiry` uses `testing/synctest`. `TestKeySyncRefreshesThreeTimesBeforeLeaseExpiry` also uses virtual time. | Remove both 9.4s live expiry waits. |
| Controller create, drift, update, invalid source, delete, and conflict recovery | `internal/identitycontroller/reconciler_test.go` covers DPU and LaunchLayer lifecycle cases. | Keep only static live registration shape and delivery assertions in E2E. |
| IMDS missing metadata header, forwarded headers, session endpoint, Accept handling, and both paths | `cmd/metadata-service/metadata_test.go` contains the protocol and validation matrix. | Keep the real VM positive JSON path. Move the negative matrix out of E2E. |
| OpenBao wrong issuer, expired, not-yet-valid, and unknown key rejection | These are normal OpenBao and JWT verifier behaviors. | Do not retest third-party generic behavior. |
| OpenBao malformed DSX subject rejection | This is the repository-owned CEL subject grammar and policy contract. | Keep one deployed negative assertion. |
| OpenBao positive policy and secret-access matrix for k1 and k2 Pod tokens | The real k2 VM flow already logs in and proves exact DSX policies plus allowed and denied access, which is the explicit E2E requirement. | Remove the duplicate Pod matrix and keep the real VM proof. |

### Process and container overhead

The cross-shard script starts a new Docker container for each external curl or compiled probe invocation. Its aggregate checker also launches host `go test` twice per convergence check even though the compiled `identity-keys-probe` test binary is already present in the E2E image. This adds process startup and can trigger Go build-cache work. Reusing `identity-keys-probe` is simpler and keeps the verification environment consistent.

The k2 VM script prints the complete JSON response, including the bearer token. That output is unnecessary for successful runs and should be removed independently of speed.

## Third-party software assessment

| Third-party component | Why it is deployed | Keep in E2E? |
| --- | --- | --- |
| Official SPIRE charts | Core workload identity and node-attestation contract. | Yes. |
| OVN-Kubernetes and Multus | Required layer-3 UDN and KubeVirt metadata-network path. | Yes. |
| KubeVirt | Required real VM identity and metadata delivery path. | Yes. |
| Kyverno | DSX Identity owns chart policies for injection and registration protection. | Yes, for project-owned policy behavior. Do not test Kyverno internals. |
| NATS Event Bus | Required OAuth2-authenticated MQTT publication and retained key propagation path. | Yes. |
| Keycloak | Supplies the required OAuth2 client-credentials boundary for the Event Bus. | Yes. Do not test generic OAuth behavior beyond the DSX connection. |
| OpenBao | Required relying-party integration with DSX CEL subject and policy mapping. | Yes, for one exact real-token positive path and the DSX grammar boundary. |
| Envoy Gateway and MetalLB | Provide the external gateway route used by the cross-cluster OAuth, MQTT, and OpenBao contracts. | Yes while those external routes are the selected local contract. |
| netshoot | Supplies SSH from the tenant network fixture. It is not itself under test. | Replace if the existing repository probe image can provide SSH with less cold-cache cost. |

The current topology mostly tests required integrations, not arbitrary third-party software. The overtesting is concentrated in generic OpenBao rejection behavior and repeated protocol matrices, not in the presence of the infrastructure itself.

## Ranked findings

### 1. DSX Identity Keys lifecycle tests dominate verification

Confidence: high.

Measured impact: about 44s for restarts and rotation, plus 18.93s for two live expiry waits.

The same behaviors have stronger focused coverage that is faster and more deterministic. The deployed test adds rollout mechanics without proving an additional project contract.

### 2. E2E duplicates controller lifecycle coverage

Confidence: high.

Measured envelope: part of the 17.66s k2 script. The exact substage was not instrumented in the baseline.

Create, update, schema rejection, delete, and recovery belong in focused reconciler tests. E2E still needs exact live registration parents, selectors, hints, tenants, and real delivery paths for the statically deployed fixtures.

### 3. Verification is unnecessarily serial

Confidence: high after mutation removal.

Measured upper bound: the current scripts take 17.66s and 98.73s independently and 110.63s together. Once both become read-only, `make -j2` can overlap them. The final saving must be measured after the other reductions because the scripts share Docker, Kubernetes API, and gateway resources.

### 4. Protocol and relying-party matrices repeat focused or third-party behavior

Confidence: high.

The VM path makes four extra SSH round trips for cases already covered by metadata-service unit tests. The cross-shard script repeats positive OpenBao access checks for Pod tokens and asks OpenBao to prove generic JWT validation. Only the DSX-specific subject grammar negative adds repository-specific coverage.

### 5. The VM external client uses a large generic image and an unused second container

Confidence: medium.

Measured image size: 218.7 MB for netshoot. Warm readiness was about 5s, so this is mainly a cold-cache and CI transfer concern. Consolidation needs a before and after image-size, build-time, pull-time, SSH, and security-context validation before adoption.

### 6. Repeated Skaffold image-load graph visits add noise and some contention

Confidence: medium.

Measured activity: 28 passes. Initial duplicate loads take 3.5s to 4.8s while later checks take about 0.05s. The existing graph preserves correct provider ownership and native Kind loading. Do not redesign it unless profiling after the test fixes shows a material, safely removable critical-path cost.

### 7. Third-party image availability controls cold deployment time

Confidence: high.

Measured variance: deployment changed from 359.50s to 728.12s. k2 Kyverno stabilization changed from 33.46s to 212.46s while the networking stack became available. The persistent Zot cache is already the correct mitigation. CI cache population and registry diagnostics may improve predictability, but removing required integrations is not acceptable.

## Improvement sequence

Changes should be applied and measured in this order:

1. Add stable stage timing output to both verification scripts so future results identify regressions without log archaeology.
2. Remove deployed DSX Identity Keys restarts, signing-key mutation, and wall-clock lease-expiry checks. Retain focused tests and static deployed aggregate convergence.
3. Remove temporary controller lifecycle sources and keep exact assertions against the deployed source fixtures.
4. Reduce IMDS and OpenBao checks to project-specific E2E contracts.
5. Replace host `go test` aggregate checks with the existing deployed probe binary.
6. Run the now read-only verification scripts concurrently.
7. Measure the full clean lifecycle again and compare coverage, wall time, and concurrency.
8. Separately prototype external-client container consolidation. Keep it only if it improves cold and warm results without weakening the tenant-network SSH boundary.

The first change set should not alter the deployment topology. It should not remove any required positive runtime path. It should not weaken exact live SPIRE registration checks.

## Required validation

After the first change set:

```bash
make validate
make clean
make -j install
make test
```

Record at least:

- Validation wall time.
- Cluster creation wall time.
- Deployment wall time.
- Each verification stage wall time.
- Maximum concurrent verification stages.
- Full lifecycle wall time.
- Exact E2E contract list before and after.

The target is not an arbitrary percentage. Success means a materially shorter and less variable verification phase with every required project integration still observed at runtime.

## Preserved E2E contract set

The improved E2E must still prove:

- A real k1 Pod obtains a k1 SPIRE JWT-SVID and exchanges it through k1 DSX Identity Keys.
- A real k2 Pod obtains a k2 SPIRE JWT-SVID and exchanges it through k2 DSX Identity Keys.
- Each DSX Identity Keys instance rejects the other shard's SPIRE JWT-SVID.
- OAuth2-authenticated MQTT connectivity works and retained publications from both DSX Identity Keys instances converge into both aggregate JWKS endpoints.
- Either aggregate verifies tokens from both shards.
- Static controller-managed DPU, worker-host, virt-launcher Pod, and VM entries have exact subjects, parents, selectors, hints, and tenant sources.
- The real DPU token fetcher produces a protected DSX token file without exposing the Workload API socket to its consumer.
- Operator worker-host IMDS returns the configured operator tenant and exact node ID.
- The real KubeVirt VM reaches metadata-service through DSX Identity Keys Proxy CSI and receives the exact tenant VM identity.
- The real k2 VM token logs in to OpenBao, receives exact DSX policies, can read allowed secrets, and cannot read forbidden secrets.
- OpenBao rejects a token whose subject violates the repository-owned DSX subject grammar.

Focused tests remain responsible for lifecycle state transitions, deterministic time behavior, protocol matrices, and generic dependency validation.

## Validated results

The report above was completed before implementation. The following results were appended after applying its high-confidence first change set.

| Measurement | Baseline | Improved | Change |
| --- | ---: | ---: | ---: |
| `make validate` | 9.98s | 9.80s | 0.18s faster, effectively unchanged |
| Fresh clusters plus deployment | 390.96s | 407.55s | 16.59s slower in this sample |
| First verification after clean deployment | 110.63s | 46.32s | 64.31s faster, 58.1% reduction |
| Immediate warm verification | 116.39s from separate warm script runs | 11.93s | 104.46s faster, 89.8% reduction |
| Measured clean lifecycle excluding validation and cleanup | 501.59s | 453.87s | 47.72s faster, 9.5% reduction |

The improved deployment rebuilt the two changed source images while the baseline deployment found every source image locally. Its 16.59s increase is within the observed readiness and cache variance and is unrelated to the verification changes. No deployment topology was removed.

The first improved verification was dominated by 45s of initial VM SSH availability. Cross-shard verification completed concurrently in 12s. On the immediate warm rerun, k2 identity verification took 11s and cross-shard verification took 12s, producing an 11.93s overall wall time. The maximum verification concurrency is now two scripts.

The improved verification passed all preserved E2E contracts. It performed no DSX Identity Keys rollout, signing Secret update, temporary source create, patch, or delete, or wall-clock publication-expiry wait. `make validate`, the full clean install, the first clean-deploy verification, and two immediate read-only reruns all passed. The final rerun took 12.31s.
