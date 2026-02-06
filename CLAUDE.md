# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Kubernetes operator built with Kubebuilder that watches Secrets and automatically patches target resources when those Secrets change. The operator provides a declarative way to trigger resource updates (like Deployment rollouts) when configuration stored in Secrets is modified.

## Core Commands

### Development
- `make build` - Build the manager binary
- `make run` - Run the controller locally against your kubeconfig cluster
- `make test` - Run unit tests
- `make test-e2e` - Run end-to-end tests (creates a Kind cluster automatically)
- `make lint` - Run golangci-lint
- `make lint-fix` - Run golangci-lint and automatically fix issues

### Code Generation
- `make manifests` - Generate CRDs, RBAC, and webhook manifests
- `make generate` - Generate DeepCopy methods for API types

### Deployment
- `make install` - Install CRDs into the cluster
- `make uninstall` - Remove CRDs from the cluster
- `make deploy IMG=<registry>/<image>:<tag>` - Deploy controller to cluster
- `make undeploy` - Remove controller from cluster

### Docker
- `make docker-build IMG=<registry>/<image>:<tag>` - Build container image
- `make docker-push IMG=<registry>/<image>:<tag>` - Push container image

## Architecture

### API Structure (`api/v1alpha1/`)

The operator defines a single CRD: **PatchTracker**

A PatchTracker resource specifies:
- **Targets**: One or more Kubernetes resources to patch (identified by APIVersion, Kind, Name, Namespace)
- **SecretDeps**: Secrets to watch for changes that should trigger patches
- **PatchField**: The field path to update on the target (e.g., "spec.replicas", "metadata.annotations")
- **PatchStrategy**: How to apply the patch (none, jsonPatch, strategicMerge, serverSideApply)
- **Reconcile options**: Timing controls (requeueAfter, debounce)

### Controller Logic (`internal/controller/patchtracker_controller.go`)

The controller reconciliation loop:

1. **Secret Change Detection**: Uses a field indexer (`spec.secretRefs`) to efficiently map Secret changes to PatchTracker resources that reference them
2. **Version Tracking**: Tracks Secret `resourceVersion` in PatchTracker status to detect changes
3. **Target Identification**: Determines which targets need patching based on changed Secrets
4. **Patch Application**: Applies patches using the specified strategy:
   - **strategicMerge**: Default, with fallback to merge patch for CRDs
   - **jsonPatch**: RFC 6902 JSON Patch
   - **serverSideApply**: Server-side apply with field ownership
   - **none**: Skip patching (dry-run mode)

### Key Patterns

**Dynamic Client Usage**: The controller uses `unstructured.Unstructured` to handle any Kubernetes resource type dynamically without needing concrete types.

**Field Indexing**: The `SetupWithManager` function creates an index on `spec.secretRefs` that maps `namespace/secretname` strings to PatchTracker objects. This enables efficient reverse lookups when a Secret changes.

**Watch Pattern**: The controller watches both PatchTracker resources (primary) and Secret resources (secondary via `EnqueueRequestsFromMapFunc`), triggering reconciliation when either changes.

**Finalizer Management**: A finalizer (`patchtracker.resourcepatch.io/finalizer`) ensures cleanup logic runs before deletion.

**Path Notation**: Field paths use dot notation (e.g., `spec.template.metadata.annotations`) which gets converted to JSON Pointer format for JSON patches.

## Testing

### Unit Tests
Located in `internal/controller/patchtracker_controller_test.go`. Uses envtest to run against a real API server with test CRDs installed.

### E2E Tests
Located in `test/e2e/`. Uses Ginkgo/Gomega and requires a Kind cluster. The Makefile automatically creates/destroys the cluster.

Run a single test:
```bash
KUBEBUILDER_ASSETS="$(bin/setup-envtest-release-0.21 use 1.33 --bin-dir bin -p path)" go test ./internal/controller -run TestAPIs
```

## Development Workflow

1. Make changes to API types in `api/v1alpha1/`
2. Run `make manifests generate` to update generated code and CRDs
3. Run `make test` to verify unit tests pass
4. Run `make lint` to check code quality
5. Test locally with `make run` against a development cluster
6. For integration testing, use `make test-e2e`

## Important Files

- [api/v1alpha1/patchtracker_types.go](api/v1alpha1/patchtracker_types.go) - PatchTracker CRD definition
- [internal/controller/patchtracker_controller.go](internal/controller/patchtracker_controller.go) - Main reconciliation logic
- [config/samples/resourcepatch_v1alpha1_patchtracker.yaml](config/samples/resourcepatch_v1alpha1_patchtracker.yaml) - Example PatchTracker resource
- [Makefile](Makefile) - All build, test, and deployment targets

## Kubebuilder Markers

This project uses Kubebuilder markers for code generation:
- `+kubebuilder:validation:*` - API validation rules
- `+kubebuilder:rbac:*` - RBAC role generation
- `+kubebuilder:subresource:status` - Enable status subresource

After modifying markers, run `make manifests` to regenerate YAML files.
