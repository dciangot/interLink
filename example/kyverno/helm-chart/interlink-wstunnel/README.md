# interLink wstunnel Helm Chart

This Helm chart deploys Kyverno policies that automatically create wstunnel infrastructure for interLink pods with exposed ports.

## Overview

The chart creates Kyverno ClusterPolicies that:

1. **Add wstunnel client sidecar** to pods scheduled on virtual nodes with exposed ports
2. **Create wstunnel server pods** on cluster nodes to handle tunneling
3. **Generate services** to expose wstunnel servers
4. **Create ingress resources** for external access (optional)
5. **Mutate user services** to point to wstunnel pods instead of virtual node pods

## Prerequisites

- Kubernetes cluster with Kyverno installed
- interLink virtual-kubelet deployed
- NGINX Ingress Controller (if ingress is enabled)

## Installation

```bash
# Install from local chart
helm install interlink-wstunnel ./example/kyverno/helm-chart/interlink-wstunnel

# Install with custom values
helm install interlink-wstunnel ./example/kyverno/helm-chart/interlink-wstunnel -f custom-values.yaml
```

## Configuration

### Key Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `virtualKubelet.nodeName` | Name of the virtual kubelet node | `virtual-kubelet` |
| `virtualKubelet.toleration.key` | Toleration key for virtual nodes | `virtual-node.interlink/no-schedule` |
| `wstunnel.image.repository` | wstunnel container image | `erebe/wstunnel` |
| `wstunnel.image.tag` | wstunnel image tag | `latest` |
| `wstunnel.server.port` | wstunnel server port | `8080` |
| `ingress.enabled` | Enable ingress creation | `true` |
| `ingress.className` | Ingress class name | `nginx` |
| `ingress.domain` | Base domain for ingress | `wstunnel.local` |
| `serviceMutation.enabled` | Enable service mutation | `true` |

### Resource Limits

The chart includes sensible defaults for resource limits:

- **Server pods**: 50m CPU / 64Mi RAM (requests), 200m CPU / 256Mi RAM (limits)
- **Client sidecars**: 25m CPU / 32Mi RAM (requests), 100m CPU / 128Mi RAM (limits)

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   User Pod      │    │  wstunnel       │    │   External      │
│  (Virtual Node) │◄──►│  Server Pod     │◄──►│   Access        │
│  + Client       │    │ (Cluster Node)  │    │  (Ingress)      │
│    Sidecar      │    │                 │    │                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                                │
                                ▼
                       ┌─────────────────┐
                       │    Service      │
                       │  (Redirected)   │
                       └─────────────────┘
```

## Usage Example

Deploy a pod with exposed ports:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: nginx-test
spec:
  nodeSelector:
    kubernetes.io/hostname: virtual-kubelet
  containers:
  - name: nginx
    image: nginx:alpine
    ports:
    - containerPort: 80
  tolerations:
  - key: virtual-node.interlink/no-schedule
    operator: Exists
```

The chart will automatically:
1. Add wstunnel client sidecar to the pod
2. Create wstunnel server pod on cluster node
3. Create service exposing the server
4. Create ingress for external access
5. Redirect any user-created services to the wstunnel server

## Uninstallation

```bash
helm uninstall interlink-wstunnel
```

Note: This will remove the Kyverno policies but may leave generated resources (pods, services, ingresses) that were created by the policies. Clean up manually if needed.

## Troubleshooting

### Check Policy Status
```bash
kubectl get clusterpolicies | grep interlink-wstunnel
```

### View Generated Resources
```bash
# Check for wstunnel server pods
kubectl get pods -l managed-by=kyverno-interlink

# Check for generated services
kubectl get services -l managed-by=kyverno-interlink

# Check for ingress resources
kubectl get ingress -l managed-by=kyverno-interlink
```

### Debug Service Mutation
Check service annotations for original selector information:
```bash
kubectl get service <service-name> -o yaml | grep -A5 annotations
```