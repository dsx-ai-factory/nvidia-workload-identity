// Copyright (c) 2026 NVIDIA Corporation
//
// SPDX-License-Identifier: Apache-2.0

use std::fs;

use anyhow::{Context, Result, anyhow};

const GUEST_CID_PREFIX: &str = "guest-cid=";
const SANDBOX_NAME_PREFIX: &str = "sandbox-";

pub fn find_qemu_by_guest_cid(guest_cid: u32) -> Result<i32> {
    let want = format!("{GUEST_CID_PREFIX}{guest_cid}");
    let mut matches = Vec::new();

    for entry in fs::read_dir("/proc").context("read /proc")? {
        let entry = entry?;
        let file_name = entry.file_name();
        let pid = match parse_pid(file_name.to_string_lossy().as_ref()) {
            Some(pid) => pid,
            None => continue,
        };

        let cmdline_path = entry.path().join("cmdline");
        let cmdline = match fs::read(cmdline_path) {
            Ok(data) if !data.is_empty() => data,
            _ => continue,
        };

        if !cmdline_matches_qemu_guest_cid(&cmdline, &want) {
            continue;
        }

        let args = split_cmdline(&cmdline);
        if !has_kata_sandbox_name(&args) {
            continue;
        }

        matches.push(pid);
    }

    match matches.len() {
        0 => Err(anyhow!("no QEMU process found for guest-cid={guest_cid}")),
        1 => Ok(matches.remove(0)),
        _ => Err(anyhow!(
            "multiple QEMU processes found for guest-cid={guest_cid}"
        )),
    }
}

fn cmdline_matches_qemu_guest_cid(cmdline: &[u8], want_guest_cid: &str) -> bool {
    let hay = String::from_utf8_lossy(cmdline);
    hay.contains("qemu") && hay.contains(want_guest_cid)
}

fn parse_pid(name: &str) -> Option<i32> {
    if name.is_empty() || !name.chars().all(|ch| ch.is_ascii_digit()) {
        return None;
    }
    name.parse().ok()
}

fn split_cmdline(cmdline: &[u8]) -> Vec<String> {
    cmdline
        .split(|b| *b == 0)
        .filter(|chunk| !chunk.is_empty())
        .map(|chunk| String::from_utf8_lossy(chunk).into_owned())
        .collect()
}

/// Kata QEMU uses `-name sandbox-<id>`; require that so we ignore unrelated QEMU VMs.
fn has_kata_sandbox_name(args: &[String]) -> bool {
    for (i, arg) in args.iter().enumerate() {
        if arg == "-name" {
            let Some(name) = args.get(i + 1) else {
                return false;
            };
            if let Some(rest) = name.strip_prefix(SANDBOX_NAME_PREFIX) {
                return !rest.is_empty();
            }
            return false;
        }
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn split_cmdline_parses_nulls() {
        let args = split_cmdline(b"qemu\x00-name\x00sandbox-abc\x00");
        assert_eq!(args, ["qemu", "-name", "sandbox-abc"]);
    }

    #[test]
    fn has_kata_sandbox_name_accepts_kata_qemu() {
        let args = vec![
            "/usr/bin/qemu-system-x86_64".to_string(),
            "-name".to_string(),
            "sandbox-deadbeef".to_string(),
        ];
        assert!(has_kata_sandbox_name(&args));
    }

    #[test]
    fn has_kata_sandbox_name_strips_qemu_name_suffix() {
        let args = vec![
            "qemu-system-x86_64".to_string(),
            "-name".to_string(),
            "sandbox-deadbeef,debug-threads=on".to_string(),
        ];
        assert!(has_kata_sandbox_name(&args));
    }

    #[test]
    fn has_kata_sandbox_name_rejects_non_kata_name() {
        let args = vec![
            "qemu-system-x86_64".to_string(),
            "-name".to_string(),
            "guest".to_string(),
        ];
        assert!(!has_kata_sandbox_name(&args));
    }

    #[test]
    fn cmdline_match_embedded_guest_cid() {
        let cmdline = b"/opt/kata/bin/qemu-system-x86_64\x00-name\x00sandbox-abc\x00-device\x00vhost-vsock-pci,guest-cid=2747252963,id=vsock\x00";
        assert!(cmdline_matches_qemu_guest_cid(
            cmdline,
            "guest-cid=2747252963"
        ));
    }

    #[test]
    fn parse_pid_skips_non_numeric() {
        assert_eq!(parse_pid("1234"), Some(1234));
        assert_eq!(parse_pid("self"), None);
        assert_eq!(parse_pid("thread-self"), None);
    }
}
