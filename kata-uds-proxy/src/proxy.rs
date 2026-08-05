// Copyright (c) 2026 NVIDIA Corporation
//
// SPDX-License-Identifier: Apache-2.0

use std::io::{Read, Write};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc;
use std::thread;

use anyhow::{Context, Result};
use nix::sys::socket::{
    AddressFamily, Backlog, SockFlag, SockType, VsockAddr, accept, bind, getpeername, listen,
    socket,
};
use nix::sys::wait::waitpid;
use nix::unistd::{ForkResult, Pid, dup, fork, getppid};

use crate::cgroup::{join_cgroup, workload_cgroup_for_qemu_pid};
use crate::qemu::find_qemu_by_guest_cid;

const MAX_LIVE_CHILDREN: usize = 1024;

static LIVE_CHILDREN: AtomicUsize = AtomicUsize::new(0);

pub fn run_proxy(listen_cid: u32, listen_port: u32, uds_path: &Path) -> Result<()> {
    let listener = socket(
        AddressFamily::Vsock,
        SockType::Stream,
        SockFlag::empty(),
        None,
    )
    .context("create vsock socket")?;

    let addr = VsockAddr::new(listen_cid, listen_port);
    bind(listener.as_raw_fd(), &addr).context("bind vsock")?;
    listen(&listener, Backlog::new(128).context("backlog")?).context("listen vsock")?;

    eprintln!(
        "kata-uds-proxy: listening cid={listen_cid} port={listen_port} uds={}",
        uds_path.display()
    );

    loop {
        let client_fd = match accept(listener.as_raw_fd()) {
            Ok(fd) => fd,
            Err(err) => {
                eprintln!("accept vsock: {err}");
                continue;
            }
        };
        let client = unsafe { OwnedFd::from_raw_fd(client_fd) };

        let guest_cid = match getpeername::<VsockAddr>(client.as_raw_fd()) {
            Ok(peer) => peer.cid(),
            Err(err) => {
                eprintln!("peer vsock cid: {err}");
                continue;
            }
        };

        if LIVE_CHILDREN.load(Ordering::Relaxed) >= MAX_LIVE_CHILDREN {
            eprintln!("guest cid={guest_cid}: at capacity, dropping connection");
            continue;
        }

        if let Err(err) = handle_accept(client, guest_cid, uds_path, &listener) {
            eprintln!("guest cid={guest_cid}: {err:#}");
        }
    }
}

fn handle_accept(
    client: OwnedFd,
    guest_cid: u32,
    uds_path: &Path,
    listener: &OwnedFd,
) -> Result<()> {
    let qemu_pid = find_qemu_by_guest_cid(guest_cid)
        .with_context(|| format!("find QEMU for guest-cid={guest_cid}"))?;

    let cgroup_dir = workload_cgroup_for_qemu_pid(qemu_pid)
        .with_context(|| format!("sandbox scope cgroup for qemu pid={qemu_pid}"))?;

    eprintln!(
        "guest cid={guest_cid} qemu_pid={qemu_pid} cgroup={}: forwarding to {}",
        cgroup_dir.display(),
        uds_path.display()
    );

    spawn_forward_child(client, uds_path, &cgroup_dir, listener)?;
    Ok(())
}

fn spawn_forward_child(
    client: OwnedFd,
    uds_path: &Path,
    cgroup_dir: &Path,
    listener: &OwnedFd,
) -> Result<()> {
    match unsafe { fork() } {
        Ok(ForkResult::Parent { child }) => {
            // Parent can drop the fd. Child has it now.
            drop(client);
            LIVE_CHILDREN.fetch_add(1, Ordering::Relaxed);
            eprintln!("forward child pid={}", child.as_raw());
            reap_forward_child(child);
            Ok(())
        }
        Ok(ForkResult::Child) => {
            // The child has a copy of the fd table which includes a reference
            // to the listener socket. We never use this, so close it immediately.
            // This would be closed anyway when the child is reaped, but close it
            // first to be safe.
            unsafe {
                libc::close(listener.as_raw_fd());
            }
            if let Err(err) = run_child(uds_path, cgroup_dir, client) {
                eprintln!("child: {err:#}");
                std::process::exit(1);
            }
            std::process::exit(0);
        }
        Err(err) => Err(err).context("fork"),
    }
}

fn reap_forward_child(child: Pid) {
    thread::spawn(move || {
        if let Err(err) = waitpid(Some(child), None) {
            eprintln!("waitpid for forward child {}: {err}", child.as_raw());
        }
        LIVE_CHILDREN.fetch_sub(1, Ordering::Relaxed);
    });
}

fn run_child(uds_path: &Path, cgroup_dir: &Path, vsock_fd: OwnedFd) -> Result<()> {
    setup_parent_death_signal()?;

    join_cgroup(cgroup_dir, std::process::id() as i32)
        .with_context(|| format!("join cgroup {}", cgroup_dir.display()))?;

    let vsock = UnixStream::from(vsock_fd);
    let uds = UnixStream::connect(uds_path)
        .with_context(|| format!("dial unix {}", uds_path.display()))?;

    bridge_bidirectional(vsock, uds)?;
    Ok(())
}

/// If the proxy dies, terminate the forwarder instead of leaving it on the host.
fn setup_parent_death_signal() -> Result<()> {
    if unsafe { libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGTERM) } != 0 {
        return Err(std::io::Error::last_os_error()).context("PR_SET_PDEATHSIG")?;
    }
    // If child has parent pid 1, the parent has exited (before we registered the signal).
    if getppid() == Pid::from_raw(1) {
        return Err(std::io::Error::new(
            std::io::ErrorKind::NotConnected,
            "parent already exited",
        ))
        .context("PR_SET_PDEATHSIG race");
    }
    Ok(())
}

fn bridge_bidirectional(vsock: UnixStream, uds: UnixStream) -> Result<()> {
    // dup() so shutdown fds are not tied to the moved UnixStream handles.
    let vsock_shut = dup(&vsock).context("dup vsock fd")?;
    let uds_shut = dup(&uds).context("dup uds fd")?;

    let (mut vsock_read, mut vsock_write) = (vsock.try_clone().context("clone vsock")?, vsock);
    let (mut uds_read, mut uds_write) = (uds.try_clone().context("clone uds")?, uds);

    let (done_tx, done_rx) = mpsc::channel::<()>();

    let done_tx_guest = done_tx.clone();
    let guest_to_host = thread::spawn(move || {
        let result = copy_data(&mut vsock_read, &mut uds_write);
        let _ = done_tx_guest.send(());
        result
    });

    let host_to_guest = thread::spawn(move || {
        let result = copy_data(&mut uds_read, &mut vsock_write);
        let _ = done_tx.send(());
        result
    });

    // When either direction finishes (EOF or error), tear down both sockets so the
    // other copy thread unblocks. This is not suitable for all protocols (e.g.
    // TCP half-closed state), but with gRPC EOF from either side generally means
    // the connection is finished.
    let _ = done_rx.recv();
    shutdown_fd(vsock_shut.as_raw_fd());
    shutdown_fd(uds_shut.as_raw_fd());
    drop(vsock_shut);
    drop(uds_shut);

    guest_to_host
        .join()
        .unwrap_or_else(|_| Err(std::io::Error::other("guest-to-host copy")))
        .ok();
    host_to_guest
        .join()
        .unwrap_or_else(|_| Err(std::io::Error::other("host-to-guest copy")))
        .ok();

    Ok(())
}

fn shutdown_fd(fd: RawFd) {
    unsafe {
        libc::shutdown(fd, libc::SHUT_RDWR);
    }
}

fn copy_data<R: Read, W: Write>(reader: &mut R, writer: &mut W) -> std::io::Result<()> {
    let mut buf = [0u8; 8192];
    loop {
        let n = reader.read(&mut buf)?;
        if n == 0 {
            return Ok(());
        }
        writer.write_all(&buf[..n])?;
    }
}
