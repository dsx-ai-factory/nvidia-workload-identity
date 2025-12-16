#!/bin/bash
# Load a Docker image into a kind cluster.
# Usage: kind-load-image.sh <image> [cluster-name]

set -euo pipefail

IMG=$1
CLUSTER=${2:-auth-callout}

if [ -z "$IMG" ]; then
  echo "Usage: $0 <image> [cluster-name]" >&2
  exit 1
fi

# Generic retry function
retry() {
  local retries=$1
  shift
  local count=0
  
  while [[ $count -lt $retries ]]; do
    if "$@"; then
      return 0
    fi
    sleep 1
    count=$((count + 1))
  done
  
  return 1
}

# Check if containerd is ready on a node
containerd_ready() {
  local node=$1
  docker exec "$node" crictl version >/dev/null 2>&1
}

# Check if image is present on a node
image_present() {
  local node=$1
  local img=$2
  docker exec "$node" crictl images -o json | jq -e ".images[] | select(.repoTags[]? | contains(\"$img\"))" >/dev/null 2>&1
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
  
  if ! retry 10 containerd_ready "$NODE"; then
    echo "ERROR: containerd not ready on $NODE" >&2
    return 1
  fi
  
  if ! image_present "$NODE" "$IMG"; then
    echo "Loading $IMG into node $NODE..."
    # Use direct ctr import to avoid kind's --all-platforms bug
    # See: https://github.com/kubernetes-sigs/kind/issues/3795
    docker save "$IMG" | docker exec -i "$NODE" ctr --namespace=k8s.io images import -
    # Verify import succeeded (with retries for async indexing)
    if ! retry 5 image_present "$NODE" "$IMG"; then
      echo "ERROR: Failed to load $IMG into node $NODE" >&2
      return 1
    fi
  else
    echo "$IMG already present on node $NODE"
  fi
  
  return 0
}

# Load into each node in parallel
for NODE in $NODES; do
  load_image_into_node "$NODE" "$IMG" &
done

# Wait for all background jobs to complete
# If any job failed, wait will return non-zero
if ! wait; then
  echo "ERROR: Failed to load image into one or more nodes" >&2
  exit 1
fi
