# Servidor WebSocket — Controlador remoto para ESP32 (EchoKit)

Servidor de WebSockets escrito en **Go** que actúa como controlador remoto de
una placa de desarrollo ESP32 (asistente de IA, ecosistema
[EchoKit](https://github.com/second-state/echokit_server)).

- La **placa ESP32 se conecta como cliente WebSocket** a `/ws`.
- Tú **inyectas comandos** desde una **API HTTP** (`POST /api/...`).
- Los comandos viajan como **JSON-RPC 2.0** siguiendo el protocolo MCP
  (`tools/call`), que es lo que el firmware del ESP32 sabe interpretar.

> **¿Por qué Go y no Node/Python?** El repositorio de referencia (echokit_server)
> está en Rust, pero la máquina no tiene la toolchain de Rust instalada y sí Go.
> Go es ideal para un servidor de red concurrente: una goroutine por conexión,
> compila a un único binario sin dependencias de runtime y arranca al instante.
> WebSocket y API HTTP se sirven en **el mismo puerto** (8080).

## Arquitectura

```
                  ┌─────────────────────────────────────┐
   curl / app     │            Servidor Go (:8080)       │      WebSocket
   ───POST───────►│  /api/command ──┐                    │◄──────────────── ESP32
   /api/...       │                 ▼                     │   (cliente /ws)
                  │   rpc.NewToolCall()  →  hub.Broadcast │ ───JSON-RPC 2.0──►
                  │                          │            │
                  │            registro de clientes (hub) │
                  └─────────────────────────────────────┘
```

| Paquete                 | Responsabilidad                                                |
|-------------------------|----------------------------------------------------------------|
| `main.go`               | Arranque, flags, apagado limpio (SIGINT/SIGTERM).              |
| `internal/server`       | Upgrade WebSocket, bombas de lectura/escritura, rutas HTTP.    |
| `internal/hub`          | Registro concurrente de placas conectadas; envío/difusión.    |
| `internal/rpc`          | Construcción de tramas JSON-RPC 2.0 (`tools/call`).           |
| `cmd/fake-esp32`        | Cliente de prueba que simula una placa (imprime lo que recibe).|

Cada conexión tiene **dos goroutines**: una lee del socket (respuestas de la
placa) y otra escribe (comandos en cola). Se usan **ping/pong** para detectar
conexiones muertas y un **canal de salida con buffer** por cliente para evitar
escrituras concurrentes sobre el mismo socket.

## Requisitos

- Go 1.21+ (probado con 1.26).

## Cómo ejecutarlo

```bash
# Arrancar el servidor (puerto 8080 por defecto)
go run .

# Otro puerto
go run . -addr :9000

# Compilar un binario
go build -o esp32ws . && ./esp32ws
```

En otra terminal, simula una placa ESP32:

```bash
go run ./cmd/fake-esp32
# o con clave de dispositivo:
#   go run ./cmd/fake-esp32 -url ws://localhost:8080/ws -key TU_DEVICE_KEY
```

Para que la API funcione necesitas arrancar con un token:

```bash
CONTROL_TOKEN=secreto DEVICE_KEY=clave go run .
```

## Seguridad

El servidor está pensado para exponerse a Internet (Dokploy), así que aplica:

| Variable de entorno | Protege | Efecto si NO se define |
|---------------------|---------|------------------------|
| `CONTROL_TOKEN`     | API `/api/*` (cabecera `Authorization: Bearer <token>`) | La API queda **deshabilitada** (responde 503) |
| `DEVICE_KEY`        | WebSocket `/ws` (la placa manda `?key=<clave>`)          | `/ws` acepta cualquier placa (solo red local) |
| `PUBLIC_URL` (opc.) | Solo informativa: se muestra en logs                    | Se detecta de `X-Forwarded-Host` en la 1ª petición |

Además, sin necesidad de configuración:

- **Anti-CSRF**: el token Bearer no lo adjunta un navegador cross-site, así que
  una web maliciosa no puede disparar comandos.
- **Anti-CSWSH**: `/ws` **rechaza** cualquier conexión con cabecera `Origin`
  (el firmware del ESP32 no la envía; un navegador sí).
- **Passthrough estricto**: las tramas JSON-RPC completas se validan y
  re-serializan (solo `method == "tools/call"`); no se reenvían bytes crudos.
- **Sin fuga de datos**: `/api/clients` no expone la IP de las placas; los logs
  escapan el contenido de los frames (`%q`).

Genera secretos robustos, por ejemplo:

```bash
export CONTROL_TOKEN=$(openssl rand -hex 32)
export DEVICE_KEY=$(openssl rand -hex 16)
```

> Comparación de tokens en **tiempo constante** (`crypto/subtle`) para no filtrar
> por temporización.

## API HTTP de control

**Todas las rutas `/api/*` exigen** la cabecera `Authorization: Bearer <CONTROL_TOKEN>`.
Cada endpoint acepta un `client_id` opcional: si se omite, el comando se
**difunde a todas las placas**; si se indica, va solo a esa placa (el `id` se ve
en `GET /api/clients`, p.ej. `esp32-1`).

### `POST /api/command` (alias `POST /api/send-command`)

Admite **dos modos** (todos los ejemplos usan `TOKEN` con tu `CONTROL_TOKEN`):

```bash
TOKEN=secreto   # = tu CONTROL_TOKEN
```

**1. Trama JSON-RPC completa (passthrough validado)** — se valida y re-serializa:

```bash
curl -X POST http://localhost:8080/api/send-command \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"self.reboot","arguments":{}}}'
```

**2. Forma simplificada** — el servidor la envuelve en `tools/call` y genera el `id`:

```bash
curl -X POST http://localhost:8080/api/command \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"self.camera.take_photo","arguments":{"question":"¿Qué ves?"}}'

# Dirigido a una placa concreta:
curl -X POST http://localhost:8080/api/command \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"client_id":"esp32-1","name":"self.reboot"}'
```

### Endpoints de conveniencia

Atajos que construyen la trama por ti:

```bash
curl -X POST http://localhost:8080/api/reboot                       -H "Authorization: Bearer $TOKEN"
curl -X POST "http://localhost:8080/api/photo?question=¿Qué%20ves?" -H "Authorization: Bearer $TOKEN"
curl -X POST "http://localhost:8080/api/volume?volume=80"           -H "Authorization: Bearer $TOKEN"
```

### `GET /api/clients`

Lista las placas conectadas (sin exponer su IP):

```bash
curl http://localhost:8080/api/clients -H "Authorization: Bearer $TOKEN"
# {"clients":["esp32-1"],"count":1}
```

### `GET /healthz`

Sonda de salud (devuelve `ok`).

## Protocolo JSON-RPC 2.0 (MCP)

Las tramas que el servidor envía a la placa siguen este formato:

| Acción          | Trama generada                                                                                              |
|-----------------|------------------------------------------------------------------------------------------------------------|
| Reiniciar       | `{"jsonrpc":"2.0","id":N,"method":"tools/call","params":{"name":"self.reboot","arguments":{}}}`            |
| Tomar foto      | `{"jsonrpc":"2.0","id":N,"method":"tools/call","params":{"name":"self.camera.take_photo","arguments":{"question":"¿Qué ves?"}}}` |
| Fijar volumen   | `{"jsonrpc":"2.0","id":N,"method":"tools/call","params":{"name":"self.audio_speaker.set_volume","arguments":{"volume":80}}}` |

El `id` se autogenera de forma monotónica salvo en modo passthrough, donde se
respeta el que envíes.

## Despliegue en Dokploy

Dokploy despliega contenedores Docker detrás de **Traefik**, que gestiona el
upgrade de WebSocket de forma transparente: **no hace falta configuración
especial** para que `/ws` funcione. WebSocket y API HTTP comparten el puerto
8080, así que con un único dominio te vale.

### Opción A — Application + Dockerfile (recomendada)

1. Sube el proyecto a un repositorio Git (GitHub/GitLab/Gitea):
   ```bash
   git init && git add . && git commit -m "Servidor WebSocket ESP32"
   git remote add origin <tu-repo> && git push -u origin main
   ```
2. En Dokploy: **Create Application** → conecta el proveedor Git y el repo.
3. **Build Type**: `Dockerfile` (Dokploy detecta el `Dockerfile` de la raíz).
4. En **Environment** añade los secretos (¡imprescindibles para que funcione!):
   ```
   CONTROL_TOKEN=<genera uno: openssl rand -hex 32>
   DEVICE_KEY=<genera otra: openssl rand -hex 16>
   ```
   (`PORT=8080` es opcional, ya es el valor por defecto.)
5. En **Domains** añade tu dominio y como **Container Port** pon `8080`.
   - Activa **HTTPS** (Let's Encrypt) si quieres `wss://` (recomendado).
6. **Deploy**. La placa se conectará a `wss://tu-dominio/ws?key=<DEVICE_KEY>` y
   los comandos se inyectan a `https://tu-dominio/api/...` con la cabecera
   `Authorization: Bearer <CONTROL_TOKEN>`.

### Opción B — Docker Compose

1. En Dokploy: **Create Compose** y apunta al repo (usa el `docker-compose.yml`).
2. En la pestaña **Domains** asocia el servicio `esp32ws`, puerto `8080`, y tu
   dominio. Dokploy genera las labels de Traefik automáticamente.
3. **Deploy**.

### Opción C — Imagen Docker ya publicada

Construye y publica la imagen en un registry y, en Dokploy, elige
**Docker Image** indicando `tu-registry/esp32ws:tag` y puerto `8080`:

```bash
docker build -t tu-registry/esp32ws:1.0 .
docker push tu-registry/esp32ws:1.0
```

### Notas importantes

- **Configuración del ESP32**: la API con token **no requiere cambios** en la
  placa (la API la usas tú, no el ESP32). Para la `DEVICE_KEY` solo cambia la URL
  del WebSocket en la config del firmware a `wss://tu-dominio/ws?key=<DEVICE_KEY>`.
- **HTTPS/WSS**: si sirves la web por `https`, el firmware debe usar `wss://`
  (no `ws://`), o el navegador/placa bloqueará la conexión por contenido mixto.
- **Una sola réplica**: el registro de clientes vive en memoria. Si escalas a
  varias réplicas, una placa solo está conectada a una de ellas; necesitarías
  **sticky sessions** en Traefik y/o un bus (Redis pub/sub) para difundir entre
  réplicas. Para un controlador de una placa, 1 réplica es lo correcto.
- **Healthcheck**: el contenedor se autocomprueba con `esp32ws -healthcheck`
  contra `/healthz` (la imagen distroless no trae `curl`).

## Tests

```bash
go test ./...
```

Cubre: difusión de comando simplificado, passthrough de trama completa,
endpoints de conveniencia, respuesta 503 sin placas conectadas, listado de
clientes y validación de cuerpo inválido.
