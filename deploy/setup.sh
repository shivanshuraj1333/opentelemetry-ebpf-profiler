#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# setup.sh — Deploy opentelemetry-ebpf-profiler + Grafana Pyroscope
#
# Usage:
#   ./setup.sh                         # auto-detect cluster type and arch
#   ./setup.sh --kind <cluster-name>   # explicitly load image via kind
#   ./setup.sh --namespace profiling   # custom namespace for Pyroscope
#   ./setup.sh --version 0.147.0       # specific otelcol-ebpf-profiler version
# ─────────────────────────────────────────────────────────────────────────────

PROFILER_VERSION="0.147.0"
PYROSCOPE_NAMESPACE="pyroscope"
KIND_CLUSTER=""
IMAGE_NAME="otelcol-ebpf-profiler"
IMAGE_TAG="local"

# ── parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --kind)        KIND_CLUSTER="$2"; shift 2 ;;
    --namespace)   PYROSCOPE_NAMESPACE="$2"; shift 2 ;;
    --version)     PROFILER_VERSION="$2"; shift 2 ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

# ── helpers ───────────────────────────────────────────────────────────────────
info()    { echo "[INFO]  $*"; }
success() { echo "[OK]    $*"; }
die()     { echo "[ERROR] $*" >&2; exit 1; }

require() {
  for cmd in "$@"; do
    command -v "$cmd" &>/dev/null || die "'$cmd' is required but not installed"
  done
}

wait_for_pod() {
  local label="$1" ns="${2:-default}" timeout="${3:-120}"
  info "Waiting for pods with label '$label' in namespace '$ns'..."
  kubectl wait pod \
    --for=condition=Ready \
    --selector="$label" \
    --namespace="$ns" \
    --timeout="${timeout}s"
}

# ── preflight ─────────────────────────────────────────────────────────────────
require kubectl docker helm curl

kubectl cluster-info &>/dev/null || die "Cannot reach Kubernetes cluster. Check your kubeconfig."

CONTEXT=$(kubectl config current-context)
info "Target cluster context: $CONTEXT"

# ── detect node architecture ──────────────────────────────────────────────────
NODE_ARCH=$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
case "$NODE_ARCH" in
  amd64|x86_64) GOARCH="amd64" ;;
  arm64|aarch64) GOARCH="arm64" ;;
  *) die "Unsupported node architecture: $NODE_ARCH" ;;
esac
info "Node architecture: $GOARCH"

# ── auto-detect kind cluster ──────────────────────────────────────────────────
if [[ -z "$KIND_CLUSTER" ]] && echo "$CONTEXT" | grep -q "^kind-"; then
  KIND_CLUSTER="${CONTEXT#kind-}"
  info "Detected kind cluster: $KIND_CLUSTER"
fi

# ── download binary ───────────────────────────────────────────────────────────
WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

TARBALL="otelcol-ebpf-profiler_${PROFILER_VERSION}_linux_${GOARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${PROFILER_VERSION}/${TARBALL}"

info "Downloading otelcol-ebpf-profiler v${PROFILER_VERSION} (${GOARCH})..."
curl -fsSL "$DOWNLOAD_URL" -o "$WORKDIR/$TARBALL"
tar -xzf "$WORKDIR/$TARBALL" -C "$WORKDIR"
chmod +x "$WORKDIR/otelcol-ebpf-profiler"
success "Binary downloaded"

# ── build docker image ────────────────────────────────────────────────────────
info "Building Docker image ${IMAGE_NAME}:${IMAGE_TAG}..."
cat > "$WORKDIR/Dockerfile" <<'DOCKERFILE'
FROM debian:bookworm-slim
COPY otelcol-ebpf-profiler /otelcol-ebpf-profiler
ENTRYPOINT ["/otelcol-ebpf-profiler"]
DOCKERFILE

docker build \
  --platform "linux/${GOARCH}" \
  -t "${IMAGE_NAME}:${IMAGE_TAG}" \
  -f "$WORKDIR/Dockerfile" \
  "$WORKDIR" \
  --quiet
success "Docker image built: ${IMAGE_NAME}:${IMAGE_TAG}"

# ── load image into cluster ───────────────────────────────────────────────────
if [[ -n "$KIND_CLUSTER" ]]; then
  info "Loading image into kind cluster '$KIND_CLUSTER'..."
  kind load docker-image "${IMAGE_NAME}:${IMAGE_TAG}" --name "$KIND_CLUSTER"
  success "Image loaded into kind cluster"
else
  info "Not a kind cluster — skipping 'kind load'. Push the image manually if needed:"
  info "  docker tag ${IMAGE_NAME}:${IMAGE_TAG} <your-registry>/${IMAGE_NAME}:${IMAGE_TAG}"
  info "  docker push <your-registry>/${IMAGE_NAME}:${IMAGE_TAG}"
fi

# ── deploy collector configmap ────────────────────────────────────────────────
info "Deploying otelcol-ebpf-profiler ConfigMap..."
kubectl apply -f - <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: otelcol-ebpf-profiler-config
  namespace: default
data:
  config.yaml: |
    receivers:
      profiling:

    exporters:
      otlphttp/pyroscope:
        endpoint: http://pyroscope.${PYROSCOPE_NAMESPACE}.svc.cluster.local:4040
        tls:
          insecure: true

    service:
      telemetry:
        logs:
          level: info
      pipelines:
        profiles:
          receivers: [profiling]
          exporters: [otlphttp/pyroscope]
YAML

# ── deploy daemonset ──────────────────────────────────────────────────────────
info "Deploying otelcol-ebpf-profiler DaemonSet..."
kubectl apply -f - <<YAML
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: otelcol-ebpf-profiler
  namespace: default
  labels:
    app: otelcol-ebpf-profiler
spec:
  selector:
    matchLabels:
      app: otelcol-ebpf-profiler
  template:
    metadata:
      labels:
        app: otelcol-ebpf-profiler
    spec:
      hostPID: true
      tolerations:
        - operator: Exists
      containers:
        - name: otelcol-ebpf-profiler
          image: ${IMAGE_NAME}:${IMAGE_TAG}
          imagePullPolicy: Never
          args:
            - "--config=/etc/otelcol/config.yaml"
            - "--feature-gates=+service.profilesSupport"
          securityContext:
            privileged: true
          resources:
            limits:
              cpu: 100m
              memory: 300Mi
            requests:
              cpu: 50m
              memory: 128Mi
          volumeMounts:
            - name: config
              mountPath: /etc/otelcol
            - name: proc
              mountPath: /proc
              readOnly: true
            - name: sys
              mountPath: /sys
              readOnly: true
            - name: debugfs
              mountPath: /sys/kernel/debug
      volumes:
        - name: config
          configMap:
            name: otelcol-ebpf-profiler-config
        - name: proc
          hostPath:
            path: /proc
        - name: sys
          hostPath:
            path: /sys
        - name: debugfs
          hostPath:
            path: /sys/kernel/debug
YAML

# ── install pyroscope ─────────────────────────────────────────────────────────
info "Adding Grafana Helm repo..."
helm repo add grafana https://grafana.github.io/helm-charts --force-update &>/dev/null
helm repo update grafana &>/dev/null

info "Installing Grafana Pyroscope in namespace '${PYROSCOPE_NAMESPACE}'..."
helm upgrade --install pyroscope grafana/pyroscope \
  --namespace "$PYROSCOPE_NAMESPACE" \
  --create-namespace \
  --set "pyroscope.extraArgs.log\.level=info" \
  --set "pyroscope.resources.limits.cpu=500m" \
  --set "pyroscope.resources.limits.memory=512Mi" \
  --set "pyroscope.resources.requests.cpu=100m" \
  --set "pyroscope.resources.requests.memory=256Mi" \
  --set "pyroscope.persistence.enabled=false" \
  --set "agent.enabled=false" \
  --wait \
  --timeout 120s

# ── wait for profiler pods ────────────────────────────────────────────────────
wait_for_pod "app=otelcol-ebpf-profiler" default 120

# ── done ──────────────────────────────────────────────────────────────────────
success "Setup complete!"
echo ""
echo "  Access Pyroscope UI:"
echo "    kubectl port-forward -n ${PYROSCOPE_NAMESPACE} svc/pyroscope 4040:4040"
echo "    open http://localhost:4040"
echo ""
echo "  Check profiler logs:"
echo "    kubectl logs -l app=otelcol-ebpf-profiler --tail=20"
echo ""
