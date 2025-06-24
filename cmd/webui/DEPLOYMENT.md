# interLink WebUI Deployment Guide

This guide covers how to build, containerize, and deploy the interLink WebUI as a Kubernetes service.

## Prerequisites

- Docker or compatible container runtime
- Kubernetes cluster with NGINX Ingress Controller
- `kubectl` configured to access your cluster
- OIDC provider (e.g., Keycloak, Auth0, Google, etc.)
- cert-manager (optional, for TLS certificates)

## Building the Docker Image

### Option 1: Build Locally

```bash
# From the repository root
docker build -f docker/Dockerfile.webui -t interlink-webui:local .
```

### Option 2: Using Make (if available)

```bash
# Add to Makefile if not present
make webui-image
```

### Option 3: Multi-platform Build

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  -f docker/Dockerfile.webui \
  -t ghcr.io/interlink-project/interlink-webui:latest \
  --push .
```

## Configuration

### 1. OIDC Provider Setup

Configure your OIDC provider with:

- **Client ID**: Your application's client identifier
- **Client Secret**: Your application's client secret
- **Redirect URL**: `https://your-domain.com/auth/callback`
- **Scopes**: `openid`, `profile`, `email`

### 2. Update ConfigMap

Edit `manifests/configmap.yaml`:

```yaml
data:
  webui-config.yaml: |
    server:
      port: 8080
      host: "0.0.0.0"
      
    oidc:
      client_id: "your-actual-client-id"
      client_secret: "your-actual-client-secret"
      redirect_url: "https://your-actual-domain.com/auth/callback"
      issuer: "https://your-oidc-provider.com"
      scopes:
        - "openid"
        - "profile"
        - "email"
        
    test_mode: false  # Set to true for development
```

### 3. Update Secret

Update the OIDC client secret in `manifests/secret.yaml`:

```bash
# Encode your actual client secret
echo -n "your-actual-client-secret" | base64
```

Then update the secret:

```yaml
data:
  oidc-client-secret: <base64-encoded-secret>
```

### 4. Update Ingress

Edit `manifests/ingress.yaml` to use your actual domain:

```yaml
spec:
  tls:
  - hosts:
    - your-actual-domain.com
    secretName: webui-tls
  rules:
  - host: your-actual-domain.com
```

## Deployment

### Using kubectl

```bash
# Deploy all resources
kubectl apply -f cmd/webui/manifests/

# Or using kustomize
kubectl apply -k cmd/webui/manifests/
```

### Using Kustomize with Overlays

Create environment-specific overlays:

```bash
# Development overlay
mkdir -p cmd/webui/overlays/dev
cat > cmd/webui/overlays/dev/kustomization.yaml << EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
- ../../manifests

patchesStrategicMerge:
- config-patch.yaml

images:
- name: ghcr.io/interlink-project/interlink-webui
  newTag: dev
EOF

# Create config patch for development
cat > cmd/webui/overlays/dev/config-patch.yaml << EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: webui-config
  namespace: interlink-webui
data:
  webui-config.yaml: |
    server:
      port: 8080
      host: "0.0.0.0"
    test_mode: true
EOF

# Deploy development version
kubectl apply -k cmd/webui/overlays/dev/
```

## Verification

### Check Pod Status

```bash
kubectl get pods -n interlink-webui
kubectl logs -n interlink-webui deployment/interlink-webui
```

### Check Service

```bash
kubectl get svc -n interlink-webui
kubectl port-forward -n interlink-webui svc/interlink-webui 8080:80
```

### Check Ingress

```bash
kubectl get ingress -n interlink-webui
curl -k https://your-domain.com
```

## Troubleshooting

### Common Issues

1. **Pod CrashLoopBackOff**
   ```bash
   kubectl logs -n interlink-webui deployment/interlink-webui
   kubectl describe pod -n interlink-webui -l app.kubernetes.io/name=interlink-webui
   ```

2. **OIDC Authentication Fails**
   - Verify OIDC provider configuration
   - Check redirect URL matches exactly
   - Ensure client secret is correct

3. **Static Files Not Found**
   - Verify static files are copied in Docker image
   - Check volume mounts in deployment

4. **Ingress Issues**
   - Verify NGINX Ingress Controller is running
   - Check ingress annotations
   - Verify DNS resolution

### Debug Mode

Enable test mode for debugging:

```bash
kubectl patch configmap webui-config -n interlink-webui --patch '
{
  "data": {
    "webui-config.yaml": "server:\n  port: 8080\n  host: \"0.0.0.0\"\ntest_mode: true\n"
  }
}'

kubectl rollout restart deployment/interlink-webui -n interlink-webui
```

## Security Considerations

1. **Use TLS/HTTPS** - Always deploy with TLS enabled
2. **Secure Secrets** - Store OIDC secrets securely
3. **Network Policies** - Implement network segmentation
4. **Resource Limits** - Set appropriate resource constraints
5. **RBAC** - Use least-privilege access patterns

## Monitoring

### Health Checks

The WebUI includes built-in health endpoints:
- Liveness probe: `GET /`
- Readiness probe: `GET /`

### Metrics

Monitor the following:
- Pod resource usage
- HTTP response times
- Authentication success/failure rates
- Error logs

## Scaling

### Horizontal Scaling

```bash
kubectl scale deployment interlink-webui -n interlink-webui --replicas=3
```

### Auto Scaling

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: interlink-webui-hpa
  namespace: interlink-webui
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: interlink-webui
  minReplicas: 1
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

## Cleanup

```bash
kubectl delete -k cmd/webui/manifests/
# or
kubectl delete namespace interlink-webui
```