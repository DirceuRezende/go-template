# Build multiestágio: compila num estágio com o toolchain e copia só o binário estático para uma imagem mínima.

FROM golang:1.27-alpine AS build
WORKDIR /src
# Dependências primeiro: esta camada só invalida quando go.mod/go.sum mudam.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Migrations e openapi.yaml vão embutidos no binário (go:embed); nada além dele precisa ir para a imagem final.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# distroless/static: sem shell, com CA certs e tzdata, roda como usuário não-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]
