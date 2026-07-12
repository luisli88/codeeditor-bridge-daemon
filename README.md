# codeeditor-bridge-daemon

Control plane de larga duración de CodeEditor, escrito en **Go**. Multiplexa un único WebSocket seguro hacia el cliente en los canales `lsp`, `shell`, `claude`, `fs`, `run`, `git`, `debug` y `entitlements`, y sostiene vivas entre reconexiones las sesiones `tmux`, los procesos de Language Server y la sesión del Claude Agent SDK de cada proyecto.

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
└── session/           # tmux (Terminal) + Claude Agent SDK, una sesión por proyecto
tests/
```

Ver `specs/001-core-development-flows/contracts/websocket-protocol.md` del repositorio coordinador para el contrato completo del protocolo multiplexado.

## Cómo correr localmente

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key
```

## Tests

```bash
go test ./... -cover
```

Cobertura objetivo: ≥90% en `internal/entitlements`, `internal/gitmanager` e `internal/session` — ver Principio V de la constitución del repositorio coordinador.
