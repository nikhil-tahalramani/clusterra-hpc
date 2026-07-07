FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
ARG TARGETOS TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o edge-agent .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/edge-agent /edge-agent
ENTRYPOINT ["/edge-agent"]
