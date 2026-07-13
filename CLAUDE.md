Este repo es un submódulo del repositorio coordinador [`CodeEditor`](https://github.com/luisli88/CodeEditor) — specs, arquitectura y constitución del proyecto viven ahí (`specs/001-core-development-flows/`, `.specify/memory/constitution.md`). Léelo antes de tocar este código, en particular Principio VII (por qué este componente es la única excepción al patrón Lambda-por-dominio del resto del backend) y Principio II (Terraform vetado).

## Cómo trabajar aquí

- Un paquete Go por responsabilidad bajo `internal/` (`ws`, `gitmanager`, `entitlements`, `devpod`, `bootstrap`, `debug`, `session`) — sin duplicación entre paquetes (Principio IV).
- Costura testeable en cada paquete que toca infraestructura externa: interfaz (`SecretStore`, `DynamoDBAPI`, `SSHCommandRunning`, etc.) + fake en tests, implementación real en producción. Preferir tests contra infraestructura real (`git`/`tmux`/`ssh-keygen` reales, `httptest.NewTLSServer`) sobre mocks livianos — ver los tests existentes en `tests/` y `internal/*/​*_test.go` como referencia del patrón.
- Cobertura objetivo ≥90% en `internal/{entitlements,gitmanager,session}` (extensión explícita del Principio V al Bridge Daemon).
- `claude` (el CLI) corre dentro de la misma sesión `tmux` de la Terminal — nunca en modo headless/autónomo (`-p` u otras flags equivalentes) por defecto (FR-049). Hay un test estructural (`tests/session_claude_test.go`) que falla si algún archivo invoca `claude` con `-p`.
- `cmd/bridged/main.go` ya cablea todo lo que tiene implementación real: los canales `lsp`/`shell`/`run`/`git`/`debug`, y las rutas HTTP de handshake/credenciales de git/aprovisionamiento — verificado sirviendo tráfico real de punta a punta contra self-hosted (sin AWS: `gitmanager.LocalFileStore` en vez de `SecretsManagerStore`, `entitlements.Gate` con `store` `nil` ya que `CheckQuota` nunca lo toca para `HostKind == "self-hosted"`). No registrado: `claude` (se empuja desde el `OutputWatcher` de `shell`, no necesita handler propio), `entitlements` (solo se llama internamente vía `Provisioner`, ningún caller manda un envelope WS directo), `fs` (sin implementación en ningún lado — nunca se construyó, ver `specs/001-core-development-flows/tasks.md` T103).
- **Gap real que sigue abierto**: `internal/ws/lsp_proxy.go`, `internal/debug`, `internal/devpod` (Run) e `internal/bootstrap.Executor` ejecutan sus subprocesos directamente en el host/contenedor del propio Bridge Daemon, no dentro del contenedor del Workspace vía `devpod ssh`. Correcto para un solo Workspace de prueba; incorrecto para varios simultáneos (el caso real de producción). Ver `README.md` "Despliegue".
- Para el tier gestionado (AWS real) hace falta reemplazar `LocalFileStore`/`Gate(nil)` por `SecretsManagerStore`/un `entitlements.DynamoDBStore` real — la costura ya existe (`gitmanager.SecretStore`, `entitlements.Store` son interfaces), solo falta la construcción con clientes AWS reales en `main.go`.

## Comandos

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key \
  --workspace-dir ./tmp/workspaces --secrets-dir ./tmp/secrets
go test ./... -cover
golangci-lint run ./...
docker build -t codeeditor-bridge-daemon .   # ver README.md "Despliegue" para docker run
```
