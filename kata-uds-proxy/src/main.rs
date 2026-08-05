// Copyright (c) 2026 NVIDIA Corporation
//
// SPDX-License-Identifier: Apache-2.0

#![warn(clippy::all)]
#![deny(warnings)]

mod cgroup;
mod proxy;
mod qemu;

use std::path::PathBuf;

use anyhow::Context;
use clap::Parser;

const DEFAULT_LISTEN_CID: u32 = 2;
const DEFAULT_LISTEN_PORT: u32 = 1234;

#[derive(Parser, Debug)]
#[command(name = "kata-uds-proxy")]
struct Args {
    /// Vsock context ID to listen on (host is usually 2).
    #[arg(long, default_value_t = DEFAULT_LISTEN_CID)]
    listen_cid: u32,

    /// Vsock port to listen on.
    #[arg(long, default_value_t = DEFAULT_LISTEN_PORT)]
    listen_port: u32,

    /// Host unix socket to forward each connection to.
    #[arg(long)]
    uds: PathBuf,
}

fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    proxy::run_proxy(args.listen_cid, args.listen_port, &args.uds).context("proxy failed")?;
    Ok(())
}
