# Two stages: build the binary, then copy it onto a minimal runtime image.
#
# The templates, stylesheet and migrations are embedded in the binary, so the
# runtime image holds one file plus CA certificates (needed to reach Google's
# OpenID configuration).

FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first: this layer is cached unless go.mod or go.sum changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
# CGO is off so the result runs on a distroless/alpine base with no libc
# surprises. -trimpath keeps build paths out of the binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/courtops ./cmd/courtops

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 courtops
USER courtops
COPY --from=build /out/courtops /usr/local/bin/courtops

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/courtops"]
