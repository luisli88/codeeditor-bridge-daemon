# codeeditor-bridge-daemon

Control plane de larga duración de CodeEditor, escrito en **Go**. Multiplexa un único WebSocket seguro hacia el cliente en los canales `lsp`, `shell`, `claude`, `fs`, `run`, `git`, `debug` y `entitlements`, y sostiene vivas entre reconexiones las sesiones `tmux` de cada proyecto (incluida la sesión interactiva del CLI `claude` dentro de esa misma `tmux` — sin Claude Agent SDK, ver research.md §7) y los procesos de Language Server.

Este repositorio es un **submódulo** del repositorio coordinador [`CodeEditor`](https://github.com/luisli88/CodeEditor), que contiene las specs, la arquitectura y la constitución de las que este componente depende. Léelo primero si vas a tocar este código — en particular el Principio VII (por qué este componente es una excepción explícita al patrón "Lambda por dominio" del resto del backend) y el Principio II (Terraform vetado; toda infraestructura vía CDK/CloudFormation).

## Prerrequisitos

- Go 1.22+
- [DevPod CLI](https://devpod.sh/) (`devpod` en `$PATH`)
- Docker (o el motor de contenedores usado como target de DevPod)
- `tmux`

## Estructura

```text
cmd/bridged/          # entrypoint del binario
internal/
├── ws/                # servidor WebSocket multiplexado
├── gitmanager/        # clonado a EFS antes de crear el contenedor del proyecto
├── entitlements/      # cliente al Entitlements Gate (codeeditor-backend)
├── devpod/            # invocación de devpod CLI + devcontainer.json
├── bootstrap/         # instalación automática de Language Servers y toolchains de depuración
├── debug/             # adaptadores DAP por lenguaje (debugpy, vscode-js-debug, dlv dap, java-debug, lldb-dap, CodeLLDB)
└── session/           # tmux (Terminal), una sesión persistente por proyecto; detecta el estado de auth del CLI `claude` para el canal `claude` — sin Claude Agent SDK (research.md §7)
tests/
```

Ver `specs/001-core-development-flows/contracts/websocket-protocol.md` del repositorio coordinador para el contrato completo del protocolo multiplexado.

## Configuración

`cmd/bridged` se configura hoy solo por flags de línea de comandos (sin variables de entorno ni archivo de config):

| Flag | Default | Descripción |
|---|---|---|
| `--port` | `8443` | Puerto de escucha |
| `--tls-cert` | *(requerido)* | Ruta al certificado TLS |
| `--tls-key` | *(requerido)* | Ruta a la llave privada TLS |

## Cómo correr localmente

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key
```

> **Nota**: en su estado actual esto levanta un listener TLS que no sirve ningún canal — ver el gap documentado en `CLAUDE.md` y `specs/001-core-development-flows/tasks.md`. Útil para probar el binario compila y arranca, no para probar un flujo end-to-end todavía.

## Tests

```bash
go test ./... -cover
golangci-lint run ./...
```

Cobertura objetivo: ≥90% en `internal/entitlements`, `internal/gitmanager` e `internal/session` — ver Principio V de la constitución del repositorio coordinador.

## Despliegue

**Self-hosted (Mac mini, VPS, cuenta AWS propia)**: compilar el binario y correrlo como servicio persistente (systemd/launchd) en un host con `tmux`, Docker (o el motor de contenedores configurado como target de DevPod) y el DevPod CLI instalados.

```bash
go build -o bridged ./cmd/bridged
./bridged --port 8443 --tls-cert /etc/codeeditor/tls.crt --tls-key /etc/codeeditor/tls.key
```

**Local con Docker** (para probar rápido sin systemd/launchd):

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout dev.key -out dev.crt -subj "/CN=localhost"

docker build -t codeeditor-bridge-daemon .
docker run --rm -p 8443:8443 \
  -v "$(pwd)/dev.crt:/etc/bridged/tls.crt:ro" \
  -v "$(pwd)/dev.key:/etc/bridged/tls.key:ro" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  codeeditor-bridge-daemon
```

El `Dockerfile` es la imagen del daemon en sí (Go binario + `tmux`/`git`/`ssh`/CLI de `devpod`/CLI de `docker`) — **no** es donde viven los Workspaces de cada proyecto. Esos los crea `devpod up` bajo demanda a partir del `devcontainer.json` de cada repositorio (detectado o generado, `internal/gitmanager`) — no hay ni debe haber una imagen fija de "workspace de CodeEditor" en ningún registry, es justamente lo que resuelve usar DevPod en vez de mantener imágenes propias por lenguaje. El socket de Docker del host se monta (no Docker-in-Docker) para que `devpod` cree esos Workspaces como contenedores hermanos, no hijos, del contenedor del daemon.

**Gaps reales que bloquean una prueba end-to-end hoy**, incluso con la imagen: (1) `cmd/bridged/main.go` todavía no registra ningún handler de `internal/ws` (ver la nota arriba) — el contenedor levanta pero no sirve tráfico real de ningún canal; (2) `internal/gitmanager.SecretStore` solo tiene una implementación contra AWS Secrets Manager (`SecretsManagerStore`) — no hay un `SecretStore` local/en-archivo, así que generar/registrar credenciales de git no funciona sin una cuenta AWS real, incluso en self-hosted puro. El Entitlements Gate sí funciona sin AWS en self-hosted — `entitlements.Gate.CheckQuota` nunca consulta DynamoDB cuando `HostKind == "self-hosted"` (`02_arquitectura_solucion.md` §3.4 pt.4), así que ese canal no es un bloqueo.

**Tier gestionado (AWS)**: el custom CDK stack (`backend/amplify/cdk/bridge-daemon-infra.ts`, en el submódulo `backend/` del coordinador) hoy solo provisiona la VPC y el cluster ECS compartidos — el Service/task definition del propio daemon y el push de esta imagen a ECR no están implementados. Ninguno de los dos estaba dentro del alcance de `specs/001-core-development-flows/tasks.md`.
