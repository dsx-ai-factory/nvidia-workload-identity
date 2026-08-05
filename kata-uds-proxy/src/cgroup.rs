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

    for line in data.lines() {
        let line = line.trim();
        if let Some(rest) = line.strip_prefix("0::") {
            let rest = rest.trim_start_matches('/');
            if rest.is_empty() {
                return Err(anyhow!("pid {pid} has empty cgroup v2 path"));
            }
            return Ok(PathBuf::from(rest));
        }
    }

    Err(anyhow!("pid {pid} has no cgroup v2 unified entry"))
}

pub fn join_cgroup(cgroup_dir: &Path, pid: i32) -> Result<()> {
    let procs = cgroup_dir.join("cgroup.procs");
    fs::write(&procs, format!("{pid}\n")).with_context(|| format!("write {}", procs.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::cgroup_v2_relative_path;

    fn is_cgroup_v2() -> bool {
        std::fs::read_to_string("/proc/self/cgroup")
            .map(|data| data.lines().any(|line| line.trim().starts_with("0::")))
            .unwrap_or(false)
    }

    #[test]
    fn parse_cgroup_v2_line() {
        if !is_cgroup_v2() {
            eprintln!("skipping parse_cgroup_v2_line: host is not using cgroup v2");
            return;
        }

        let pid = std::process::id() as i32;
        let rel = cgroup_v2_relative_path(pid).expect("current pid cgroup");
        assert!(!rel.as_os_str().is_empty());
    }
}
