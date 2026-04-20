FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -o /ext-authz .

FROM gcr.io/distroless/static-debian12
COPY --from=builder /ext-authz /ext-authz
EXPOSE 9000
ENTRYPOINT ["/ext-authz"]
