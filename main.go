package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type extAuthzServer struct{}

func (s *extAuthzServer) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	httpReq := req.GetAttributes().GetRequest().GetHttp()
	headers := httpReq.GetHeaders()
	path := httpReq.GetPath()
	method := httpReq.GetMethod()

	log.Printf("[ext-authz] %s %s | headers: %v", method, path, redactHeaders(headers))

	// --- Customize your authorization logic here ---
	allowed, reason := checkAuth(headers)

	if allowed {
		log.Printf("[ext-authz] ALLOWED: %s", reason)
		return &authv3.CheckResponse{
			Status: &status.Status{Code: int32(codes.OK)},
			HttpResponse: &authv3.CheckResponse_OkResponse{
				OkResponse: &authv3.OkHttpResponse{
					Headers: []*corev3.HeaderValueOption{
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
					},
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

// checkAuth contains the authorization logic. Modify this function to
// implement your own policy. Return (true, reason) to allow or
// (false, reason) to deny.
func checkAuth(headers map[string]string) (bool, string) {
	// Default behavior: allow requests with "x-ext-authz: allow" header.
	// This matches the Istio sample ext-authz so you can validate the
	// build works before customizing.
	if val, ok := headers["x-ext-authz"]; ok && val == "allow" {
		return true, "header x-ext-authz=allow present"
	}
	return false, "header `x-ext-authz: allow` not found in request"
}

// redactHeaders returns a sanitized copy of headers for logging.
func redactHeaders(headers map[string]string) map[string]string {
	redacted := make(map[string]string, len(headers))
	for k, v := range headers {
		lower := strings.ToLower(k)
		if lower == "authorization" || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey") {
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

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("Failed to listen on port %s: %v", port, err)
	}

	s := grpc.NewServer()
	authv3.RegisterAuthorizationServer(s, &extAuthzServer{})

	log.Printf("gRPC ext-authz server listening on :%s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
