# Member Join/Leave Status Tracking Control

## Overview

This document describes the command line flag `--disable-member-join-leave-status` that controls whether KubeBlocks tracks member join/leave status for component scaling operations.

## Command Line Flag

- **Name**: `--disable-member-join-leave-status`
- **Type**: Boolean
- **Default**: `false`
- **Location**: `pkg/constant/flag.go:DisableMemberJoinLeaveStatusFlag`

## Behavior

### When `--disable-member-join-leave-status=false` (Default)

KubeBlocks tracks member join/leave status in replica annotations:

1. **Scale-out**: New replicas get `MemberJoined: false` status initially
2. **Join Process**: After successful join, status changes to `MemberJoined: true` 
3. **Scale-in**: Only replicas with `MemberJoined: true` will execute leave operations
4. **Status Tracking**: Full status lifecycle is maintained for debugging and monitoring

### When `--disable-member-join-leave-status=true`

KubeBlocks enters **Hook-Based Mode** with pre-scaling hook execution:

1. **Scale-out Flow**:
   - Execute join hooks for all new replicas BEFORE creating pods
   - If any join hook fails, scale-out is prevented (error returned)
   - If all join hooks succeed, proceed with normal pod creation
   - No status tracking or annotation updates

2. **Scale-in Flow**:
   - Execute leave hooks for all to-be-deleted replicas BEFORE deleting pods
   - If any leave hook fails, scale-in is prevented (error returned)
   - If all leave hooks succeed, proceed with normal pod deletion
   - No status tracking or annotation updates

3. **Key Benefits**:
   - **Zero InstanceSet updates** for status tracking
   - **Pre-validation**: Hooks validate before any pod changes
   - **Fail-fast**: Prevents partial scaling on hook failures
   - **Simpler state management**: No annotations to manage

## Use Cases

### Enable Status Tracking (Default)
- Better debugging and monitoring capabilities
- Clear visibility into join/leave operation progress
- Automatic retry logic for failed operations
- Prevents duplicate join/leave operations

### Disable Status Tracking
- Custom hook implementations with built-in idempotency
- Simplified state management
- Reduced annotation overhead
- Custom orchestration requirements

## Configuration

Set the command line flag for the KubeBlocks controller:

### Method 1: Command Line
```bash
./manager --disable-member-join-leave-status=true
```

### Method 2: Deployment Args
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubeblocks
spec:
  template:
    spec:
      containers:
      - name: manager
        args:
        - --disable-member-join-leave-status=true
```

### Method 3: Helm Values
```yaml
# values.yaml
kubeblocks:
  manager:
    args:
    - --disable-member-join-leave-status=true
```

## Implementation Details

The feature is implemented in the following files:

- `pkg/constant/flag.go`: Command line flag constant definition
- `cmd/manager/main.go`: Flag registration and setup
- `pkg/controller/component/replicas.go`: Status tracking functions
- `controllers/apps/component/transformer_component_workload.go`: Scale operations

### Modified Functions

1. **`NewReplicasStatus`**: Conditionally sets `MemberJoined` status
2. **`StatusReplicasStatus`**: Conditionally updates join status
3. **`joinMember4ScaleOut`**: Skips status checks when disabled
4. **`scaleIn`**: Includes all provisioned replicas when status disabled

## Important Notes

⚠️ **When disabling status tracking, ensure your hooks are idempotent!**

- Join hooks must handle being called multiple times gracefully
- Leave hooks must handle non-existent or already-left members
- Error handling becomes the responsibility of the hook implementation

## Example Hook Implementation

```bash
#!/bin/bash
# Example idempotent join hook

MEMBER_EXISTS=$(check_if_member_exists "$POD_NAME")
if [ "$MEMBER_EXISTS" = "false" ]; then
    join_cluster "$POD_NAME"
    echo "Member $POD_NAME joined successfully"
else
    echo "Member $POD_NAME already exists, skipping join"
fi
```