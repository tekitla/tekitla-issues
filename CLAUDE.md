# issues-service — contexto para Claude

## Qué es este servicio

Proyectos e issues de Tequio (issue 002).

<!-- TODO: completar. Qué dominio cubre, quién lo llama (gateway, otros micros),
     qué contrato gRPC implementa y reglas de negocio no obvias. -->

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
| Interceptor | `app/adapters/grpc/interceptor/` | Logging gRPC |
| Repos | `app/infra/postgres/` | Rows unexported + `TableName()` con schema |
| Wiring | `app/infra/bootstrap/`, `app/infra/config/` | Config (env) + logger + DB |
| Telemetría | `app/infra/telemetry/` | Zap + banner |
| Entry point | `cli/main.go` | Solo wiring + ciclo de vida |

Contrato gRPC: repo de protos `github.com/vrojas1015/tequio-protos` (ver
"Protos"). Mientras no se enchufe, el servicio expone el ejemplo local
`proto/example/v1/item.proto`. Además registra
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
- Handlers: un test de punta a punta con `bufconn` alcanza (ver `item_handler_test.go`).

## Datos

- PK **UUID** por default, generada en el service (`uuid.NewString()`). PK de
  texto solo si el proto la expone como identificador estable del cliente (y
  entonces upsert con `clause.OnConflict`).
- Paginación keyset `(created_at, id) DESC` con cursor opaco (`cursor.go`):
  pedir `limit+1` para `has_next`. Default/máximo de `limit` en el service.
- JSONB: DTOs unexported separados del dominio + `encoding/json`.
- Migraciones en `migrations/NNNNNN_desc.{up,down}.sql`, aplicadas a mano
  (`DATABASE_URL=... scripts/apply_migration.sh <qa|prod> <migración>`). Si nada
  está desplegado, reescribir `000001`. El `docker-compose.yml` local las aplica
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
make proto                    # regenera gen/go desde proto/ (buf)
go mod tidy                   # después de tocar imports
make workspace / workspace-off  # co-desarrollar con un checkout local de los protos
grpcurl -plaintext localhost:5000 list   # con reflection (no prod)
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

Repo de protos: `github.com/vrojas1015/tequio-protos` (versión inicial `v0.1.0`). Para enchufarlo:

1. `go get <módulo-de-protos>@<versión>` (ej. `github.com/vrojas1015/tequio-protos@v0.1.0`).
2. Handler nuevo que implemente `<paquete>.<X>ServiceServer`, mapper y registro en `cli/main.go`.
3. Borrar el ejemplo (abajo), `buf.yaml`, `buf.gen.yaml`, `proto/` y `gen/`.
4. Módulo privado: `GOPRIVATE`, token en CI y `make workspace`
   (`copier update` con `protos_module` los genera; ver `docs/ci-cd-setup.md`).

Repo de protos hecho con la plantilla `protos`: su ejemplo es este mismo
`ItemService`, en `<módulo-de-protos>/gen/go/<paquete>/v1`. Para pasarse basta
cambiar el import `github.com/vrojas1015/tequio-issues/gen/go/example/v1` por ese (handler, su
test, mapper y `cli/main.go`) y borrar `proto/`, `gen/` y `buf.*`: los dos no
pueden convivir (protobuf-go entra en pánico al registrar dos veces
`example/v1/item.proto`). Sin tag publicado todavía:
`go mod edit -require=<módulo>@v0.0.0 -replace=<módulo>=../<repo-protos>` y
`go mod tidy` (sacar el `replace` al publicar la primera versión).

## Borrar el ejemplo (entidad `Item`)

`app/domain/model/item.go`, `app/domain/port/item_repository.go`,
`app/application/item_service*.go` (+ mocks en `mocks_test.go`),
`app/infra/postgres/item_repo.go`, `app/adapters/grpc/{handler,mapper}/item_*`,
el wiring en `cli/main.go` y la tabla `items` en `migrations/000001_*`.
`go build ./... && go test ./...` tiene que quedar verde.

## Reglas duras

1. El dominio nunca importa proto ni GORM. La frontera la cruzan los mappers/repos.
2. Nada de librerías de mocking.
3. Proto-first: antes de implementar, leer el `.proto` y reportar el delta con el código.
4. Release con cambio de contrato: protos → clientes (gateway) → este micro.
5. **Secretos**: nunca al repo (`.env*`, claves, cuentas de servicio); gitleaks los busca en cada commit (`pre-commit install`) y en el CI, y uno filtrado se rota primero (`SECURITY.md`). Título del MR/PR: `feat(issue-<n>): …` (salvo `chore(deps)`).
