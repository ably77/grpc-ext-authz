# gRPC ext-authz Server

A configurable gRPC external authorization server compatible with the [Envoy ext-authz API](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/auth/v3/external_auth.proto). Designed for use with [Solo Enterprise Agentgateway](https://docs.solo.io/agentgateway/) and any Envoy-based proxy.

## Quick Start

### Run locally
```bash
go run main.go
```

### Run with Docker
```bash
docker run -p 9000:9000 ably7/grpc-ext-authz:latest
```

### Run with a specific auth mode
```bash
docker run -p 9000:9000 -e AUTH_MODE=apikey ably7/grpc-ext-authz:latest
```

## Auth Modes

Set the `AUTH_MODE` environment variable to select which authorization logic to use. Defaults to `header`.

### `header` (default)

Allows requests that include the `x-ext-authz: allow` header. Useful for basic validation that the ext-authz integration is working.

| Env Var | Description | Default |
|---|---|---|
| — | No configuration needed | — |

```bash
# Allowed
curl -H "x-ext-authz: allow" ...

# Denied
curl ...
```

### `apikey`

Validates the `x-api-key` header against a map of known keys. Injects `x-user-id` header on success.

| Env Var | Description | Default |
|---|---|---|
| `API_KEYS` | Comma-separated `key=user` pairs | `sk-abc123=alice,sk-def456=bob` |

```bash
# Allowed — injects x-user-id: alice
curl -H "x-api-key: sk-abc123" ...

# Denied — invalid key
curl -H "x-api-key: sk-invalid" ...
```

**Kubernetes Deployment example:**
```yaml
env:
- name: AUTH_MODE
  value: "apikey"
- name: API_KEYS
  value: "sk-abc123=alice,sk-def456=bob,sk-ghi789=charlie"
```

### `jwt`

Decodes a JWT from the `Authorization: Bearer <token>` header and validates claims. Checks expiration (`exp`) and optionally enforces required claim values. Injects `x-user-id` from the `sub` claim.

> **Note:** This mode decodes the JWT payload but does **not** verify the signature. It is intended for demos and testing. For production JWT validation, use Agentgateway's built-in `jwtAuthentication` policy.

| Env Var | Description | Default |
|---|---|---|
| `JWT_REQUIRED_CLAIMS` | Comma-separated `claim=value` pairs that must be present | _(none — any valid JWT is accepted)_ |

```bash
# Allowed — valid JWT with matching claims
curl -H "Authorization: Bearer eyJhbG..." ...

# Denied — missing or expired token
curl ...
```

**Kubernetes Deployment example:**
```yaml
env:
- name: AUTH_MODE
  value: "jwt"
- name: JWT_REQUIRED_CLAIMS
  value: "team=engineering,tier=premium"
```

### `model`

Validates which LLM model is being requested on a per-user basis. Parses the `model` field from the JSON request body and checks it against user-specific allow lists. User is identified by the `x-api-key` header.

> **Note:** This mode requires the request body to be forwarded to the ext-authz server. Configure `includeRequestBody` in the policy.

| Env Var | Description | Default |
|---|---|---|
| `API_KEYS` | Key-to-user mapping (shared with `apikey` mode) | `sk-abc123=alice,sk-def456=bob` |
| `MODEL_POLICIES` | Comma-separated `user=model1\|model2` pairs | `alice=gpt-4o\|gpt-4o-mini,bob=gpt-4o-mini` |

```bash
# Allowed — alice can use gpt-4o
curl -H "x-api-key: sk-abc123" -d '{"model":"gpt-4o",...}' ...

# Denied — bob cannot use gpt-4o
curl -H "x-api-key: sk-def456" -d '{"model":"gpt-4o",...}' ...
```

**Kubernetes Deployment example:**
```yaml
env:
- name: AUTH_MODE
  value: "model"
- name: API_KEYS
  value: "sk-abc123=alice,sk-def456=bob"
- name: MODEL_POLICIES
  value: "alice=gpt-4o|gpt-4o-mini,bob=gpt-4o-mini"
```

### `time`

Allows requests only during configured hours (UTC). Useful for demonstrating time-based access controls.

| Env Var | Description | Default |
|---|---|---|
| `ALLOWED_HOURS` | `start-end` in 24h UTC format | `9-17` |

```bash
# Allowed — during business hours (9 AM - 5 PM UTC)
curl ...

# Denied — outside business hours
curl ...
```

**Kubernetes Deployment example:**
```yaml
env:
- name: AUTH_MODE
  value: "time"
- name: ALLOWED_HOURS
  value: "8-20"
```

### `budget`

Per-user request budget tracked in memory. Each user gets a fixed number of requests per time window, after which requests are denied until the window resets. User is identified by the `x-api-key` header.

| Env Var | Description | Default |
|---|---|---|
| `API_KEYS` | Key-to-user mapping (shared with `apikey` mode) | `sk-abc123=alice,sk-def456=bob` |
| `BUDGET_MAX` | Max requests per user per window | `10` |
| `BUDGET_WINDOW` | Window duration (Go duration string) | `60s` |

```bash
# First 10 requests — allowed
curl -H "x-api-key: sk-abc123" ...

# 11th request within 60s — denied
curl -H "x-api-key: sk-abc123" ...
```

**Kubernetes Deployment example:**
```yaml
env:
- name: AUTH_MODE
  value: "budget"
- name: API_KEYS
  value: "sk-abc123=alice,sk-def456=bob"
- name: BUDGET_MAX
  value: "5"
- name: BUDGET_WINDOW
  value: "30s"
```

## Server Configuration

| Env Var | Description | Default |
|---|---|---|
| `PORT` | gRPC listen port | `9000` |
| `AUTH_MODE` | Authorization mode to use | `header` |

## Deploying to Kubernetes

Deploy with any auth mode by setting environment variables:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grpc-ext-authz
  namespace: agentgateway-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: grpc-ext-authz
  template:
    metadata:
      labels:
        app: grpc-ext-authz
    spec:
      containers:
      - image: ably7/grpc-ext-authz:latest
        name: grpc-ext-authz
        ports:
        - containerPort: 9000
        env:
        - name: AUTH_MODE
          value: "apikey"
        - name: API_KEYS
          value: "sk-abc123=alice,sk-def456=bob"
---
apiVersion: v1
kind: Service
metadata:
  name: grpc-ext-authz
  namespace: agentgateway-system
spec:
  ports:
  - port: 4444
    targetPort: 9000
    protocol: TCP
    appProtocol: kubernetes.io/h2c
  selector:
    app: grpc-ext-authz
```

Then reference the service in an `EnterpriseAgentgatewayPolicy`:
```yaml
apiVersion: enterpriseagentgateway.solo.io/v1alpha1
kind: EnterpriseAgentgatewayPolicy
metadata:
  name: ext-auth-policy
  namespace: agentgateway-system
spec:
  targetRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway        # or HTTPRoute for route-level
    name: agentgateway-proxy
  traffic:
    extAuth:
      backendRef:
        name: grpc-ext-authz
        namespace: agentgateway-system
        port: 4444
      grpc: {}
```

## Customizing for Your Needs

This server is a starting point. Fork [github.com/ably77/grpc-ext-authz](https://github.com/ably77/grpc-ext-authz) and modify it to fit your authorization requirements.

### Adding a new auth mode

1. Write a check function with this signature:
   ```go
   func checkMyMode(headers map[string]string, body string) (bool, string, map[string]string) {
       // Return (allowed, reason, headersToInject)
   }
   ```

2. Register it in the `authModes` map in `main.go`:
   ```go
   var authModes = map[string]authFunc{
       // ... existing modes ...
       "mymode": checkMyMode,
   }
   ```

3. Add any init function if your mode needs startup config from env vars.

4. Build and push your image:
   ```bash
   ./build-and-push.sh 0.0.4
   ```

### Common customization patterns

| Use Case | What to Change |
|---|---|
| **Validate against a different header** | Modify `checkHeader()` or add a new mode |
| **Call an external API for entitlements** | Add an HTTP client call in your check function (see the [entitlement example](https://github.com/ably77/grpc-ext-authz/blob/main/README.md)) |
| **Route-based authorization** | Read `httpReq.GetPath()` in the `Check` method and dispatch to different logic per route |
| **Inject user identity headers** | Return headers in the third return value — they're automatically added to the upstream request via `OkResponse.Headers` |
| **Integrate with your IdP's JWT** | Modify `checkJWT()` to use your IdP's claim namespace (e.g., `https://your-org.com/role` instead of `https://example.com/role`) |

### Injected headers and MCP authorization

Headers injected by the gRPC ext-authz server are visible to downstream policies. Use them in MCP authorization CEL expressions:

```yaml
mcpAuthorization:
  rules:
  - 'request.headers["x-user-id"] == "admin" && mcp.tool.name == "delete"'
  - 'request.headers["x-authz-matched-product"] == "PRO" && mcp.tool.name in ["analyze", "report"]'
```

## References

- [Solo Enterprise Agentgateway: BYO ext-auth service](https://docs.solo.io/agentgateway/2.2.x/security/extauth/byo-ext-auth-service/) — official docs for gRPC ext-authz with Enterprise Agentgateway
- [Agentgateway OSS: BYO ext-auth service](https://agentgateway.dev/docs/kubernetes/main/security/extauth/byo-ext-auth-service/) — OSS docs with Envoy ext-authz proto details
- [Agentgateway Standalone: External authorization](https://agentgateway.dev/docs/standalone/main/configuration/security/external-authz/) — standalone config with gRPC and HTTP options
- [Envoy ext-authz proto](https://github.com/envoyproxy/envoy/blob/main/api/envoy/service/auth/v3/external_auth.proto) — the gRPC service definition this server implements
- [ably77/http-ext-authz](https://github.com/ably77/http-ext-authz) — HTTP version of this server (same auth modes, plain HTTP protocol)
- [Istio ext-authz sample](https://github.com/istio/istio/tree/master/samples/extauthz) — the Istio sample server used in the official docs

## Build and Push

```bash
./build-and-push.sh 0.0.3
```

Builds multi-arch (linux/amd64 + linux/arm64) and pushes to DockerHub.
