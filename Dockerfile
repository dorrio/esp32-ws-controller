# syntax=docker/dockerfile:1

# --- Etapa 1: compilación ---------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache de dependencias: copiamos solo los manifiestos primero.
COPY go.mod go.sum ./
RUN go mod download

# Copiamos el resto del código y compilamos un binario estático.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/esp32ws .

# --- Etapa 2: imagen final mínima ------------------------------------------
# distroless: sin shell ni gestor de paquetes, superficie de ataque mínima.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/esp32ws /app/esp32ws

# Puerto en el que escucha el servidor (WebSocket + API HTTP comparten puerto).
ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/app/esp32ws"]
