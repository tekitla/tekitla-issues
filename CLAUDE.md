# issues-service — contexto para Claude

## Qué es este servicio

Proyectos e issues de Tequio (issue 002, rebanada 1): proyectos de un solo
dueño y sus issues, con tres estados fijos (por hacer, en curso, hecho).

- **Contrato**: `issues.v1.IssueService` de `github.com/vrojas1015/tequio-protos`
  (`gen/go/issues/v1`): `ListProjects`, `CreateProject`, `ListIssues`,
  `GetIssue`, `CreateIssue`, `MoveIssue`. Los comentarios del `.proto` son la
  especificación.
- **Quién lo llama**: solo el `tequio-gateway` (api-gateway), que autentica y
  propaga la identidad en la metadata gRPC `x-user-id` (además `x-roles` y
  `x-request-id`).
- **Identidad**: todo RPC exige `x-user-id` no vacío (si no, `UNAUTHENTICATED`).
  La lee el adapter (`handler/identity.go`) y la pasa como parámetro explícito
  (`ownerUID`) a la capa de aplicación; el dominio no lee el context.
- **Pertenencia**: todo se filtra por `owner_uid`. Un proyecto ajeno responde
  `NOT_FOUND` (nunca `PERMISSION_DENIED`, para no revelar que existe).
  `IssueService` lo verifica con `ProjectRepository.GetByID` antes de tocar issues.

Reglas de negocio no obvias:

- Key del proyecto: 2–10 letras A-Z o dígitos, empieza con letra. Se recorta y
  se pasa a mayúsculas (`tq` → `TQ`). Única por dueño: duplicada →
  `ALREADY_EXISTS` con reason `PROJECT_KEY_TAKEN`. Otro dueño puede usar la misma.
- Número de issue: correlativo por proyecto desde 1, no se reutiliza. Lo reserva
  `IssueRepository.Create` con `UPDATE projects SET next_issue_number = next_issue_number + 1
  ... RETURNING` en la misma transacción del `INSERT` (lock de fila: seguro con
  concurrencia).
- Issue nuevo: estado `TODO`; prioridad `UNSPECIFIED` se guarda como `MEDIUM`.
- `MoveIssue` con estado `UNSPECIFIED` (o desconocido) → `INVALID_ARGUMENT`.
  Un valor de enum desconocido en el filtro de `ListIssues` también.
- Orden: proyectos por `name, id`; issues por `priority DESC, created_at DESC, id DESC`.

Microservicio Go gRPC, interno (Coolify, sin puerto publicado), Postgres en el
schema `issues`, puerto `5000`. Generado desde la plantilla
Copier `go-grpc-service` (`.copier-answers.yml`; actualizar con `copier update`).

## Arquitectura (hexagonal)

```
adapters → application → domain ← infra
                         (ports)
```

| Capa | Path | Regla |
|---|---|---|
| Modelos | `app/domain/model/` | Go puro: sin tags GORM ni tipos proto |
| Ports | `app/domain/port/` | Interfaces de repositorio/clientes |
| Errores | `app/domain/errors/` | `package errors`, importar como `domainerrors` |
| Use cases | `app/application/` | Solo dependen de ports |
| Handlers | `app/adapters/grpc/handler/` | Embeben `Unimplemented…Server`; finos |
| Mappers | `app/adapters/grpc/mapper/` | Única frontera proto ↔ dominio |
| Interceptor | `app/adapters/grpc/interceptor/` | Logging gRPC (con `request_id` y `user_id` de la metadata) |
| Repos | `app/infra/postgres/` | Rows unexported + `TableName()` con schema |
| Wiring | `app/infra/bootstrap/`, `app/infra/config/` | Config (env) + logger + DB |
| Telemetría | `app/infra/telemetry/` | Zap + banner |
| Entry point | `cli/main.go` | Solo wiring + ciclo de vida |

Contrato gRPC: repo de protos `github.com/vrojas1015/tequio-protos` (ver
"Protos"); handler único `IssueHandler`. Además registra
`grpc.health.v1` (probes de Cloud Run y `<binario> healthcheck` de docker
compose, ver `cli/healthcheck.go`) y reflection fuera de prod.

## Errores

- Toda capa devuelve `*domainerrors.DomainError` vía los 9 constructores
  (`NotFoundError`, `ConflictError`, `ValidationError`, `UnauthorizedError`,
  `PermissionDeniedError`, `FailedPreconditionError`, `InternalError`,
  `ServiceUnavailableError`, `RateLimitExceededError`), firma `(msg, details)`.
- Error de Go inesperado: `domainerrors.InternalError("failed to X", map[string]any{"cause": err.Error()})`.
- Nada de `fmt.Errorf("…%w", sentinel)` para errores de negocio.
- Handlers: `return nil, domainerrors.MapToGRPC(err, h.logger)`. Adjunta
  `google.rpc.ErrorInfo` (Reason/Domain/Metadata); errores no-dominio salen como
  `Internal` sin filtrar detalles.
- Si el cliente necesita distinguir dos errores con el mismo código: `.WithReason("UPPER_SNAKE")`.
- Repos: `translateError` (`app/infra/postgres/errors.go`) mapea
  `gorm.ErrRecordNotFound` → NotFound, PG `23505` → Conflict, resto → Internal.
- Tests: `domainerrors.IsNotFound/IsConflict/IsValidation/IsFailedPrecondition/IsInternal`.

## Tests

- Solo stdlib `testing`. **Nada** de testify, gomock ni mockery.
- Mocks = structs con campos `func` opcionales (`app/application/mocks_test.go`);
  método sin setear → zero values.
- Un `<servicio>_test.go` por service: caso feliz + validaciones + propagación
  de errores del repo.
- Handlers: un test de punta a punta con `bufconn` y repos en memoria alcanza
  (ver `issue_handler_test.go`: identidad, pertenencia y mapeo de errores).
- No hay tests de integración contra Postgres: los repos se prueban levantando
  `make up` y pegándole con `grpcurl` (ver "Comandos").

## Datos

- PK **UUID** por default, generada en el service (`uuid.NewString()`). PK de
  texto solo si el proto la expone como identificador estable del cliente (y
  entonces upsert con `clause.OnConflict`).
- Paginación keyset con cursor opaco (`cursor.go`: JSON + base64 URL-safe, un
  struct por listado con las columnas de su `ORDER BY` + `id`): pedir `limit+1`
  para `has_next`. Default 50 / máximo 100 en `application/common.go`.
  Cursor mal formado → `INVALID_ARGUMENT`.
- Tablas: `issues.projects` (único `owner_uid, key`; `next_issue_number`) e
  `issues.issues` (FK al proyecto, único `project_id, number`; `state` texto
  `todo|in_progress|done` y `priority` 1..4, ambos con `CHECK`). Timestamps
  truncados a microsegundos en el repo (`dbNow`).
- JSONB: DTOs unexported separados del dominio + `encoding/json`.
- Migraciones en `migrations/NNNNNN_desc.{up,down}.sql`, aplicadas a mano
  (`DATABASE_URL=... scripts/apply_migration.sh <qa|prod> <migración>`). Hoy:
  `000001` crea el schema y `000002` las tablas; mientras nada esté
  desplegado, corregir esas dos en vez de sumar migraciones. El `docker-compose.yml` local las aplica
  solo al crear el volumen (`docker compose down -v` para empezar de cero).
- Copiar los datos del servicio entre dos Postgres (con verificación de filas):
  `scripts/migrate_db.sh SRC_URL DST_URL` (dry-run; `--apply` para ejecutar).

## Comandos

```bash
cp .env.example .env.dev      # una vez; completar DATABASE_URL
make up / make down           # Postgres 16 + servicio con docker compose
docker compose up -d --wait postgres   # solo la base, para `make run`
make run                      # levanta el servicio (lee .env.dev)
make test                     # go test ./... -count=1
make lint                     # gofmt + go vet (+ golangci-lint si está)
make build                    # bin/issues-service
go mod tidy                   # después de tocar imports
make workspace / workspace-off  # co-desarrollar con un checkout local de los protos
grpcurl -plaintext localhost:5000 list   # con reflection (no prod)
grpcurl -plaintext -H 'x-user-id: u1' -d '{"key":"TQ","name":"Tequio"}'   localhost:5000 issues.v1.IssueService/CreateProject
# sin grpcurl local: docker run --rm --network issues-service_default #   fullstorydev/grpcurl:v1.9.3 -plaintext app:5000 list
```

## Deploy

Configuración 12-factor: todo sale de variables de entorno (`app/infra/config`)
y son **las mismas** en todos los destinos (`APP_ENV`, `SERVICE_ENV`, `DEBUG`,
`LOG_LEVEL`, `GRPC_PORT`, `DATABASE_URL`, `OTEL_*`). Una variable nueva se
agrega en `.env.example`, `docker-compose.yml` y `docker-compose.coolify.yml` (+ panel de Coolify).

Por tag de git vía GitHub Actions (`.github/workflows/`) → GHCR
→ API de Coolify v4, que redespliega `docker-compose.coolify.yml` en el VPS:

```bash
git tag qa-v0.1.0   && git push origin qa-v0.1.0     # QA, automático
git tag prod-v0.1.0 && git push origin prod-v0.1.0   # prod, automático salvo migraciones
```

- La imagen se publica con el tag inmutable (`qa-v0.1.0`) y uno móvil (`qa`/`prod`);
  el recurso de Coolify usa `IMAGE_TAG=qa|prod`.
- Release con cambios en `migrations/`: la guarda de prod bloquea. Aplicar el
  SQL por túnel SSH (`scripts/apply_migration.sh prod <migración> --apply`) y
  lanzar el override.
- Rollback: `IMAGE_TAG=<tag inmutable anterior>` en el panel de Coolify + Redeploy.
- Setup de una vez (recurso, variables, backups a S3, gRPC detrás de Traefik):
  `docs/deploy.md`. Mudar a Cloud Run: `docs/migrar-a-cloud-run.md`.
- Los comandos con credenciales o confirmación (`apply_migration.sh prod --apply`,
  `migrate_db.sh --apply`, cambios en el panel de Coolify) los corre una persona.

## Protos

Repo de protos: `github.com/vrojas1015/tequio-protos`, hoy en `v0.1.0`
(`go.mod`). Paquete Go `github.com/vrojas1015/tequio-protos/gen/go/issues/v1`
(alias `issuesv1`), usado solo en `handler/`, `mapper/` y `cli/main.go`.

- Versión nueva del contrato: `go get github.com/vrojas1015/tequio-protos@vX.Y.Z`
  + `go mod tidy`; implementar los RPCs nuevos en `IssueHandler` (el embed de
  `Unimplemented…` los deja en `UNIMPLEMENTED` mientras tanto).
- Co-desarrollo con un checkout local de los protos: `make workspace`
  (`go.work`, fuera del repo) / `make workspace-off`.

## Reglas duras

1. El dominio nunca importa proto ni GORM. La frontera la cruzan los mappers/repos.
2. Nada de librerías de mocking.
3. Proto-first: antes de implementar, leer el `.proto` y reportar el delta con el código.
4. Release con cambio de contrato: protos → clientes (gateway) → este micro.
5. **Secretos**: nunca al repo (`.env*`, claves, cuentas de servicio); gitleaks los busca en cada commit (`pre-commit install`) y en el CI, y uno filtrado se rota primero (`SECURITY.md`). Título del MR/PR: `feat(issue-<n>): …` (salvo `chore(deps)`).
