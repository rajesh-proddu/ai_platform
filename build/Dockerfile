# One Dockerfile for both P0 services; pick with --build-arg SERVICE=stateservice|registry.
#   docker build -f build/Dockerfile --build-arg SERVICE=stateservice -t ai-platform/stateservice:dev .
ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS build
ARG SERVICE
WORKDIR /src

# No third-party dependencies in P0, so there is nothing to pre-download; go.mod is copied first
# anyway so the layer stays cached once dependencies do appear.
COPY go.mod ./
COPY . .

RUN test -n "$SERVICE" || (echo "SERVICE build-arg is required" && exit 1)
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/service /service
USER nonroot:nonroot
ENTRYPOINT ["/service"]
