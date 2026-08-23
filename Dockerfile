FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X swarmdash/internal/version.Version=${VERSION} -X swarmdash/internal/version.Commit=${COMMIT}" \
    -o /out/swarmdash ./cmd/swarmdash

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
COPY --from=build /out/swarmdash /usr/local/bin/swarmdash
ENTRYPOINT ["swarmdash"]
