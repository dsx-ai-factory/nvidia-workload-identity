#!/bin/bash
# Load a Docker image into a kind cluster.
# Usage: kind-load-image.sh <image> [cluster-name]

set -euo pipefail
set -m

IMG=${1:-}
CLUSTER=${2:-auth-callout}
pids=()
signal_status=0

cleanup() {
  local pid
  local -a owned_pids=("${pids[@]}")
  pids=()
  trap - INT TERM

  for pid in "${owned_pids[@]}"; do
    kill -- "-$pid" 2>/dev/null || true
  done
  for pid in "${owned_pids[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ -z "$IMG" ]; then
  echo "Usage: $0 <image> [cluster-name]" >&2
  exit 1
fi

# Check if containerd is ready on a node
containerd_ready() {
  local node=$1
  docker exec "$node" crictl version >/dev/null 2>&1
}

# Check whether the node has the same image layers as Docker.
image_current() {
  local node=$1
  local img=$2
  local local_image local_digest node_ref node_digest

  local_image=$(docker image inspect "$img")
  local_digest=$(jq -r '.[0].Descriptor.digest // empty' <<<"$local_image")
  if [[ -n "$local_digest" ]]; then
    node_ref=$(docker exec "$node" crictl inspecti "$img" 2>/dev/null | \
      jq -er '.status.repoTags[0]') || return 1
    node_digest=$(docker exec "$node" ctr --namespace=k8s.io images ls "name==$node_ref" | \
      awk 'NR == 2 { print $3 }')
  else
    local_digest=$(jq -er '.[0].Id' <<<"$local_image")
    node_digest=$(docker exec "$node" crictl inspecti "$img" 2>/dev/null | jq -er '.status.id')
  fi
  [[ -n "$node_digest" && "$local_digest" == "$node_digest" ]]
}

# Pull if not present locally
if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  echo "Pulling $IMG..."
  docker pull "$IMG"
fi

# Get all nodes in the cluster
NODES=$(kind get nodes --name "$CLUSTER")

# Function to load image into a single node
load_image_into_node() {
  local NODE=$1
  local IMG=$2
  
  if ! containerd_ready "$NODE"; then
    echo "ERROR: containerd not ready on $NODE" >&2
    return 1
  fi
  
  if ! image_current "$NODE" "$IMG"; then
    echo "Loading $IMG into node $NODE..."
    # Use direct ctr import to avoid kind's --all-platforms bug
    # See: https://github.com/kubernetes-sigs/kind/issues/3795
    docker save "$IMG" | docker exec -i "$NODE" ctr --namespace=k8s.io images import -
  else
    echo "$IMG already present on node $NODE"
  fi
  
  return 0
}

# Load into each node in parallel
trap 'signal_status=130' INT
trap 'signal_status=143' TERM
for NODE in $NODES; do
  ((signal_status == 0)) || exit "$signal_status"
  load_image_into_node "$NODE" "$IMG" &
  pids+=("$!")
  ((signal_status == 0)) || exit "$signal_status"
done
trap 'exit 130' INT
trap 'exit 143' TERM
((signal_status == 0)) || exit "$signal_status"

# Wait for all background jobs to complete
failed=0
while ((${#pids[@]})); do
  if ! wait "${pids[0]}"; then
    failed=1
  fi
  pids=("${pids[@]:1}")
done
if ((failed)); then
  echo "ERROR: Failed to load image into one or more nodes" >&2
  exit 1
fi
