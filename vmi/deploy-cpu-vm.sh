#!/bin/bash

# Script to deploy Ubuntu CPU-only VMs
# Usage: ./deploy-cpu-vm.sh <vm-name>

if [ -z "$1" ]; then
    echo "Usage: $0 <vm-name>"
    echo "Example: $0 ubuntu-cpu-worker-01"
    exit 1
fi

VM_NAME="$1"
TEMPLATE_FILE="ubuntu-cpu-vm-template.yaml"

if [ ! -f "$TEMPLATE_FILE" ]; then
    echo "Error: Template file $TEMPLATE_FILE not found!"
    exit 1
fi

echo "Deploying CPU-only VM: $VM_NAME"

# Create a temporary file with the VM name substituted
TEMP_FILE=$(mktemp)

# Replace the template placeholders with the actual VM name
sed -e "s/ubuntu-cpu-template/$VM_NAME/g" \
    -e "s/ubuntu-rootdisk/$VM_NAME-rootdisk/g" \
    "$TEMPLATE_FILE" > "$TEMP_FILE"

# Apply the configuration
kubectl apply -f "$TEMP_FILE"

# Clean up
rm "$TEMP_FILE"

echo "CPU-only VM $VM_NAME deployment initiated!"
echo "Check status with: kubectl get vm $VM_NAME"
echo "Connect to console with: kubectl virt console $VM_NAME"
echo "SSH to VM once running: ssh ubuntu@<vm-ip>"
