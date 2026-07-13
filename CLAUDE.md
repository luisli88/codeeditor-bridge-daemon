Este repo es un submódulo del repositorio coordinador [`CodeEditor`](https://github.com/luisli88/CodeEditor) — specs, arquitectura y constitución del proyecto viven ahí (`specs/001-core-development-flows/`, `.specify/memory/constitution.md`). Léelo antes de tocar este código, en particular Principio VII (por qué este componente es la única excepción al patrón Lambda-por-dominio del resto del backend) y Principio II (Terraform vetado).

## Cómo trabajar aquí

- Un paquete Go por responsabilidad bajo `internal/` (`ws`, `gitmanager`, `entitlements`, `devpod`, `bootstrap`, `debug`, `session`) — sin duplicación entre paquetes (Principio IV).
- Costura testeable en cada paquete que toca infraestructura externa: interfaz (`SecretStore`, `DynamoDBAPI`, `SSHCommandRunning`, etc.) + fake en tests, implementación real en producción. Preferir tests contra infraestructura real (`git`/`tmux`/`ssh-keygen` reales, `httptest.NewTLSServer`) sobre mocks livianos — ver los tests existentes en `tests/` y `internal/*/​*_test.go` como referencia del patrón.
- Cobertura objetivo ≥90% en `internal/{entitlements,gitmanager,session}` (extensión explícita del Principio V al Bridge Daemon).
- `claude` (el CLI) corre dentro de la misma sesión `tmux` de la Terminal — nunca en modo headless/autónomo (`-p` u otras flags equivalentes) por defecto (FR-049). Hay un test estructural (`tests/session_claude_test.go`) que falla si algún archivo invoca `claude` con `-p`.
- **Gap conocido**: `cmd/bridged/main.go` es hoy un listener TLS desnudo — no registra ningún handler de `internal/ws` ni construye los clientes AWS (DynamoDB/Secrets Manager) que `internal/entitlements`/`internal/gitmanager` necesitan en producción. Cada paquete está completo y probado individualmente, pero el composition root que los conecta no está escrito. No asumas que `go run ./cmd/bridged` sirve tráfico real de ningún canal.

## Comandos

```bash
go run ./cmd/bridged --port 8443 --tls-cert dev.crt --tls-key dev.key
go test ./... -cover
golangci-lint run ./...
```
