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
| `--workspace-dir` | `/var/lib/codeeditor/workspaces` | Raíz donde se clonan los Workspaces (equivalente local al mount de EFS) |
| `--secrets-dir` | `/var/lib/codeeditor/secrets` | Raíz de `gitmanager.LocalFileStore` — credenciales de git self-hosted, sin AWS |
| `--mosh-udp-port-range` | `60000-61000` | Rango de puertos UDP que valida el checklist de `/handshake` |

## Cómo correr localmente

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key \
  --workspace-dir ./tmp/workspaces --secrets-dir ./tmp/secrets
```

`main.go` registra todos los canales de `internal/ws` con implementación real: `lsp`, `shell`, `run`, `git`, `debug` — verificado sirviendo tráfico real de punta a punta (clonado real de un repo público, generación real de una llave SSH ed25519, detección real de `devcontainer.json`). `claude` no tiene handler propio — se empuja desde el `OutputWatcher` del canal `shell` (contracts/auth-flows.md), no hace falta uno. `entitlements` tampoco — el único llamador de `Gate.CheckQuota` es el `Provisioner`, alcanzado por `POST /projects/provision`, no un envelope WS directo (nada en la app lo manda tampoco: `EntitlementsService.swift` consulta `Amplify.API` directo). `fs` sigue sin ninguna implementación — nunca se construyó el sync del árbol de archivos, ver `specs/001-core-development-flows/tasks.md` T103.

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
  -v codeeditor-workspaces:/var/lib/codeeditor/workspaces \
  -v codeeditor-secrets:/var/lib/codeeditor/secrets \
  -v /var/run/docker.sock:/var/run/docker.sock \
  codeeditor-bridge-daemon
```

Los dos volúmenes nombrados (`codeeditor-workspaces`, `codeeditor-secrets`) son opcionales pero recomendados — sin ellos, los Workspaces clonados y las credenciales de git generadas se pierden cada vez que el contenedor se recrea.

El `Dockerfile` es la imagen del daemon en sí (Go binario + `tmux`/`git`/`ssh`/CLI de `devpod`/CLI de `docker`) — **no** es donde viven los Workspaces de cada proyecto. Esos los crea `devpod up` bajo demanda a partir del `devcontainer.json` de cada repositorio (detectado o generado, `internal/gitmanager`) — no hay ni debe haber una imagen fija de "workspace de CodeEditor" en ningún registry, es justamente lo que resuelve usar DevPod en vez de mantener imágenes propias por lenguaje. El socket de Docker del host se monta (no Docker-in-Docker) para que `devpod` cree esos Workspaces como contenedores hermanos, no hijos, del contenedor del daemon.

**Verificado end-to-end en self-hosted** (fuera del contenedor, mismo binario): `/handshake` responde con el checklist real; `POST /git-credentials/ssh-key/generate` genera y almacena una llave ed25519 real; `POST /projects/provision` clona un repositorio público real, detecta/genera su `devcontainer.json`, y llega hasta el paso `devpod-up` — que solo falla porque `devpod` no estaba instalado en la máquina donde se corrió esta verificación puntual (si está instalado, como en la imagen de Docker, ese paso también corre real).

**Gap real que sigue abierto**: `internal/ws/lsp_proxy.go`, `internal/debug` (adaptadores DAP), `internal/devpod` (ejecución de Run) e `internal/bootstrap.Executor` invocan sus subprocesos (`pyright-langserver`, `debugpy`, el comando de Run, `pip`/`npm`/etc.) directamente en el host/contenedor del propio Bridge Daemon — no dentro del contenedor del Workspace vía `devpod ssh`. Con un solo Workspace de prueba activo esto no se nota (todo corre en el mismo filesystem clonado), pero es incorrecto para múltiples Workspaces simultáneos, que es el caso real de producción. Cerrar esto es un cambio más grande (enrutar cada subproceso a través de `devpod ssh <workspace> -- <comando>`) que no estaba en el alcance de esta pasada.

**Tier gestionado (AWS)**: el custom CDK stack (`backend/amplify/cdk/bridge-daemon-infra.ts`, en el submódulo `backend/` del coordinador) hoy solo provisiona la VPC y el cluster ECS compartidos — el Service/task definition del propio daemon y el push de esta imagen a ECR no están implementados. Ninguno de los dos estaba dentro del alcance de `specs/001-core-development-flows/tasks.md`.
