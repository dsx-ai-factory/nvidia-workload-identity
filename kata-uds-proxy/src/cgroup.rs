// Copyright (c) 2026 NVIDIA Corporation
//
// SPDX-License-Identifier: Apache-2.0

use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, anyhow};

const CGROUP_V2_ROOT: &str = "/sys/fs/cgroup";

/// Leaf cgroup used when joining the forwarder child for SPIRE attestation.
///
/// Kata QEMU (and the pod sandbox scope) live in `cri-containerd-<sandbox-id>.scope`.
/// Pod `.slice` parents are cgroup v2 domain nodes and reject processes (`EBUSY`).
///
/// SPIRE reads pod UID from this cgroup path. The sandbox container ID is not listed
/// in kubelet `ContainerStatuses`, so the agent must set
/// `disable_container_selectors = true` on the k8s workload attestor to emit pod-level
/// selectors only (`k8s:pod-name`, `k8s:pod-uid`, …) without matching a container ID.
pub fn workload_cgroup_for_qemu_pid(qemu_pid: i32) -> Result<PathBuf> {
    let rel = cgroup_v2_relative_path(qemu_pid)?;
    if !rel.as_os_str().to_string_lossy().ends_with(".scope") {
        return Err(anyhow!(
            "qemu pid {} cgroup {} is not a leaf .scope (cannot join)",
            qemu_pid,
            rel.display()
        ));
    }
    Ok(PathBuf::from(CGROUP_V2_ROOT).join(rel))
}

fn cgroup_v2_relative_path(pid: i32) -> Result<PathBuf> {
    let data = fs::read_to_string(format!("/proc/{pid}/cgroup"))
        .with_context(|| format!("read /proc/{pid}/cgroup"))?;

    parse_cgroup_v2_relative_path(&data).with_context(|| format!("parse /proc/{pid}/cgroup"))
}

fn parse_cgroup_v2_relative_path(data: &str) -> Result<PathBuf> {
    for line in data.lines() {
        let line = line.trim();
        if let Some(rest) = line.strip_prefix("0::") {
            let rest = rest.trim_start_matches('/');
            if rest.is_empty() {
                return Err(anyhow!("empty cgroup v2 path"));
            }
            return Ok(PathBuf::from(rest));
        }
    }

    Err(anyhow!("no cgroup v2 unified entry"))
}

pub fn join_cgroup(cgroup_dir: &Path, pid: i32) -> Result<()> {
    let procs = cgroup_dir.join("cgroup.procs");
    fs::write(&procs, format!("{pid}\n")).with_context(|| format!("write {}", procs.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::parse_cgroup_v2_relative_path;
    use std::path::Path;

    #[test]
    fn parse_cgroup_v2_line() {
        let data = "12:pids:/system.slice/kubelet.service\n\
                    0::/kubepods.slice/kubepods-pod0e1f.slice/cri-containerd-9ab3.scope\n";

        let rel = parse_cgroup_v2_relative_path(data).expect("unified entry");

        assert_eq!(
            rel,
            Path::new("kubepods.slice/kubepods-pod0e1f.slice/cri-containerd-9ab3.scope")
        );
    }
}
