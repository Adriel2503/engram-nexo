# Nexo IA — imagen del servidor Engram Cloud del fork (engram cloud serve).
#
# Compila el binario desde el código de esta rama (multietapa) y produce una
# imagen equivalente a la de 03-servidor/piloto-local (alpine 3.21, usuario
# engram 10001, puerto 18080, ENTRYPOINT engram, CMD cloud serve), pero con
# los cambios del fork: trazabilidad (NX-01..NX-04), roles (NX-05), seguridad
# (NX-07) y espacios de conocimiento con su panel (NX-10); ver NEXO_CHANGES.md.
# Disponibilidad con la base: GET /nexo/health/ready (200 o 503).
#
# Construir (desde la raíz del fork):
#   docker build --platform linux/amd64 -f deploy/nexo/Dockerfile \
#     --build-arg VERSION=3.4.0-nexo.1 -t nexo-engram-cloud:3.4.0-nexo.1 .
# Verificar:
#   docker run --rm nexo-engram-cloud:3.4.0-nexo.1 version   -> engram 3.4.0-nexo.1

FROM --platform=$BUILDPLATFORM golang:1.25.10-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=3.4.0-nexo.1

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/engram ./cmd/engram

FROM alpine:3.21
ARG VERSION=3.4.0-nexo.1
LABEL org.opencontainers.image.title="nexo-engram-cloud" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.description="Engram Cloud (fork Nexo IA): trazabilidad NX-01..NX-04, roles NX-05, seguridad NX-07 y espacios de conocimiento NX-10"
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 engram
COPY --from=builder /out/engram /usr/local/bin/engram
USER engram
WORKDIR /home/engram
EXPOSE 18080
ENTRYPOINT ["engram"]
CMD ["cloud", "serve"]
