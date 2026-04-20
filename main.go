package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// authMode is set via AUTH_MODE env var at startup.
var authMode string

// --------------------------------------------------------------------
// Auth mode: header (default)
// Allows requests with "x-ext-authz: allow" header.
// --------------------------------------------------------------------

func checkHeader(headers map[string]string, _ string) (bool, string, map[string]string) {
	if val, ok := headers["x-ext-authz"]; ok && val == "allow" {
		return true, "header x-ext-authz=allow present", nil
	}
	return false, "header `x-ext-authz: allow` not found in request", nil
}

// --------------------------------------------------------------------
// Auth mode: apikey
// Validates x-api-key against a known set of keys.
// Injects x-user-id on success.
//
// Configure keys via API_KEYS env var:
//   API_KEYS="sk-abc123=alice,sk-def456=bob"
// --------------------------------------------------------------------

var apiKeys map[string]string

func initAPIKeys() {
	apiKeys = make(map[string]string)
	raw := os.Getenv("API_KEYS")
	if raw == "" {
		// Defaults for quick demos
		apiKeys["sk-abc123"] = "alice"
		apiKeys["sk-def456"] = "bob"
		return
	}
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			apiKeys[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
}

func checkAPIKey(headers map[string]string, _ string) (bool, string, map[string]string) {
	key := headers["x-api-key"]
	if key == "" {
		return false, "missing x-api-key header", nil
	}
	user, ok := apiKeys[key]
	if !ok {
		return false, "invalid x-api-key", nil
	}
	return true, fmt.Sprintf("authenticated as %s", user), map[string]string{
		"x-user-id": user,
	}
}

// --------------------------------------------------------------------
// Auth mode: jwt
// Decodes a JWT from Authorization: Bearer <token> and validates
// claims. No signature verification — this is for demo/testing.
//
// Configure required claims via JWT_REQUIRED_CLAIMS env var:
//   JWT_REQUIRED_CLAIMS="team=engineering,tier=premium"
// --------------------------------------------------------------------

var jwtRequiredClaims map[string]string

func initJWTClaims() {
	jwtRequiredClaims = make(map[string]string)
	raw := os.Getenv("JWT_REQUIRED_CLAIMS")
	if raw == "" {
		return
	}
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			jwtRequiredClaims[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
}

func checkJWT(headers map[string]string, _ string) (bool, string, map[string]string) {
	auth := headers["authorization"]
	if auth == "" {
		return false, "missing Authorization header", nil
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return false, "Authorization header must be Bearer token", nil
	}
	token := strings.TrimPrefix(auth, "Bearer ")

	claims, err := decodeJWTPayload(token)
	if err != nil {
		return false, fmt.Sprintf("invalid JWT: %v", err), nil
	}

	// Check expiration
	if exp, ok := claims["exp"]; ok {
		if expFloat, ok := exp.(float64); ok {
			if time.Now().Unix() > int64(expFloat) {
				return false, "JWT expired", nil
			}
		}
	}

	// Check required claims
	for key, required := range jwtRequiredClaims {
		actual, ok := claims[key]
		if !ok {
			return false, fmt.Sprintf("missing required claim: %s", key), nil
		}
		if fmt.Sprintf("%v", actual) != required {
			return false, fmt.Sprintf("claim %s=%v does not match required value %s", key, actual, required), nil
		}
	}

	// Extract sub for header injection
	sub, _ := claims["sub"].(string)
	extraHeaders := map[string]string{}
	if sub != "" {
		extraHeaders["x-user-id"] = sub
	}

	return true, fmt.Sprintf("JWT valid (sub=%s)", sub), extraHeaders
}

func decodeJWTPayload(token string) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("token must have 3 parts, got %d", len(parts))
	}
	payload := parts[1]
	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode payload: %v", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse claims: %v", err)
	}
	return claims, nil
}

// --------------------------------------------------------------------
// Auth mode: model
// Validates which LLM model is being requested per user.
// Requires the request body to be forwarded (includeRequestBody).
//
// Configure allowed models via MODEL_POLICIES env var:
//   MODEL_POLICIES="alice=gpt-4o|gpt-4o-mini,bob=gpt-4o-mini"
//
// User is identified by x-api-key header (reuses apiKeys map).
// --------------------------------------------------------------------

var modelPolicies map[string][]string

func initModelPolicies() {
	modelPolicies = make(map[string][]string)
	raw := os.Getenv("MODEL_POLICIES")
	if raw == "" {
		// Defaults for quick demos
		modelPolicies["alice"] = []string{"gpt-4o", "gpt-4o-mini"}
		modelPolicies["bob"] = []string{"gpt-4o-mini"}
		return
	}
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			user := strings.TrimSpace(parts[0])
			models := strings.Split(parts[1], "|")
			for i := range models {
				models[i] = strings.TrimSpace(models[i])
			}
			modelPolicies[user] = models
		}
	}
}

func checkModel(headers map[string]string, body string) (bool, string, map[string]string) {
	// Identify user via x-api-key
	key := headers["x-api-key"]
	if key == "" {
		return false, "missing x-api-key header", nil
	}
	user, ok := apiKeys[key]
	if !ok {
		return false, "invalid x-api-key", nil
	}

	// Parse model from body
	if body == "" {
		return false, "request body not available (enable includeRequestBody in policy)", nil
	}
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &reqBody); err != nil {
		return false, fmt.Sprintf("failed to parse request body: %v", err), nil
	}
	if reqBody.Model == "" {
		return false, "no model specified in request body", nil
	}

	// Check model policy
	allowed, exists := modelPolicies[user]
	if !exists {
		return false, fmt.Sprintf("no model policy defined for user %s", user), nil
	}
	for _, m := range allowed {
		if m == reqBody.Model {
			return true, fmt.Sprintf("user %s authorized for model %s", user, reqBody.Model), map[string]string{
				"x-user-id": user,
			}
		}
	}
	return false, fmt.Sprintf("user %s not authorized for model %s (allowed: %s)", user, reqBody.Model, strings.Join(allowed, ", ")), nil
}

// --------------------------------------------------------------------
// Auth mode: time
// Allows requests only during configured hours (UTC).
//
// Configure via ALLOWED_HOURS env var:
//   ALLOWED_HOURS="9-17"  (9 AM to 5 PM UTC)
// Default: 9-17
// --------------------------------------------------------------------

var allowedStartHour, allowedEndHour int

func initTimePolicy() {
	allowedStartHour = 9
	allowedEndHour = 17
	raw := os.Getenv("ALLOWED_HOURS")
	if raw != "" {
		fmt.Sscanf(raw, "%d-%d", &allowedStartHour, &allowedEndHour)
	}
}

func checkTime(headers map[string]string, _ string) (bool, string, map[string]string) {
	hour := time.Now().UTC().Hour()
	if hour >= allowedStartHour && hour < allowedEndHour {
		return true, fmt.Sprintf("within allowed hours (%d:00-%d:00 UTC, current: %d:00)", allowedStartHour, allowedEndHour, hour), nil
	}
	return false, fmt.Sprintf("outside allowed hours (%d:00-%d:00 UTC, current: %d:00 UTC)", allowedStartHour, allowedEndHour, hour), nil
}

// --------------------------------------------------------------------
// Auth mode: budget
// Per-user request budget tracked in memory.
// Resets every BUDGET_WINDOW (default: 60s).
//
// Configure via:
//   BUDGET_MAX="10"       (max requests per window per user)
//   BUDGET_WINDOW="60s"   (window duration)
//
// User is identified by x-api-key header (reuses apiKeys map).
// --------------------------------------------------------------------

var (
	budgetMax    int
	budgetWindow time.Duration
	budgetMu     sync.Mutex
	budgetCounts map[string]*budgetEntry
)

type budgetEntry struct {
	count     int
	resetAt   time.Time
}

func initBudgetPolicy() {
	budgetMax = 10
	budgetWindow = 60 * time.Second
	budgetCounts = make(map[string]*budgetEntry)

	if raw := os.Getenv("BUDGET_MAX"); raw != "" {
		fmt.Sscanf(raw, "%d", &budgetMax)
	}
	if raw := os.Getenv("BUDGET_WINDOW"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			budgetWindow = d
		}
	}
}

func checkBudget(headers map[string]string, _ string) (bool, string, map[string]string) {
	key := headers["x-api-key"]
	if key == "" {
		return false, "missing x-api-key header", nil
	}
	user, ok := apiKeys[key]
	if !ok {
		return false, "invalid x-api-key", nil
	}

	budgetMu.Lock()
	defer budgetMu.Unlock()

	entry, exists := budgetCounts[user]
	now := time.Now()
	if !exists || now.After(entry.resetAt) {
		entry = &budgetEntry{count: 0, resetAt: now.Add(budgetWindow)}
		budgetCounts[user] = entry
	}

	entry.count++
	if entry.count > budgetMax {
		remaining := time.Until(entry.resetAt).Round(time.Second)
		return false, fmt.Sprintf("user %s exceeded budget (%d/%d requests, resets in %s)", user, entry.count-1, budgetMax, remaining), nil
	}
	return true, fmt.Sprintf("user %s request %d/%d", user, entry.count, budgetMax), map[string]string{
		"x-user-id": user,
	}
}

// --------------------------------------------------------------------
// Auth dispatch
// --------------------------------------------------------------------

type authFunc func(headers map[string]string, body string) (bool, string, map[string]string)

var authModes = map[string]authFunc{
	"header": checkHeader,
	"apikey": checkAPIKey,
	"jwt":    checkJWT,
	"model":  checkModel,
	"time":   checkTime,
	"budget": checkBudget,
}

// --------------------------------------------------------------------
// gRPC server
// --------------------------------------------------------------------

type extAuthzServer struct{}

func (s *extAuthzServer) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	httpReq := req.GetAttributes().GetRequest().GetHttp()
	headers := httpReq.GetHeaders()
	path := httpReq.GetPath()
	method := httpReq.GetMethod()
	body := httpReq.GetBody()

	log.Printf("[ext-authz] mode=%s %s %s | headers: %v", authMode, method, path, redactHeaders(headers))

	checkFn := authModes[authMode]
	allowed, reason, extraHeaders := checkFn(headers, body)

	if allowed {
		log.Printf("[ext-authz] ALLOWED: %s", reason)
		okHeaders := []*corev3.HeaderValueOption{
			{
				Header: &corev3.HeaderValue{
					Key:   "x-ext-authz-check-result",
					Value: "allowed",
				},
			},
			{
				Header: &corev3.HeaderValue{
					Key:   "x-ext-authz-check-reason",
					Value: reason,
				},
			},
		}
		for k, v := range extraHeaders {
			okHeaders = append(okHeaders, &corev3.HeaderValueOption{
				Header: &corev3.HeaderValue{Key: k, Value: v},
			})
		}
		return &authv3.CheckResponse{
			Status: &status.Status{Code: int32(codes.OK)},
			HttpResponse: &authv3.CheckResponse_OkResponse{
				OkResponse: &authv3.OkHttpResponse{
					Headers: okHeaders,
				},
			},
		}, nil
	}

	log.Printf("[ext-authz] DENIED: %s", reason)
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{
			DeniedResponse: &authv3.DeniedHttpResponse{
				Status: &typev3.HttpStatus{
					Code: typev3.StatusCode_Forbidden,
				},
				Body: fmt.Sprintf("denied by ext_authz: %s", reason),
				Headers: []*corev3.HeaderValueOption{
					{
						Header: &corev3.HeaderValue{
							Key:   "x-ext-authz-check-result",
							Value: "denied",
						},
					},
				},
			},
		},
	}, nil
}

func redactHeaders(headers map[string]string) map[string]string {
	redacted := make(map[string]string, len(headers))
	for k, v := range headers {
		lower := strings.ToLower(k)
		if lower == "authorization" || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey") || lower == "x-api-key" {
			redacted[k] = "[REDACTED]"
		} else {
			redacted[k] = v
		}
	}
	return redacted
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	authMode = os.Getenv("AUTH_MODE")
	if authMode == "" {
		authMode = "header"
	}
	if _, ok := authModes[authMode]; !ok {
		log.Fatalf("Unknown AUTH_MODE=%q. Valid modes: header, apikey, jwt, model, time, budget", authMode)
	}

	// Initialize mode-specific config
	initAPIKeys()
	initJWTClaims()
	initModelPolicies()
	initTimePolicy()
	initBudgetPolicy()

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("Failed to listen on port %s: %v", port, err)
	}

	s := grpc.NewServer()
	authv3.RegisterAuthorizationServer(s, &extAuthzServer{})

	log.Printf("gRPC ext-authz server listening on :%s (mode=%s)", port, authMode)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
