# issues-service — contexto para Claude

## Qué es este servicio

Proyectos e issues de Tekitla (issue 002): proyectos dentro de un espacio de
trabajo (hoy, el personal de cada usuario) y sus issues, con estados
configurables por proyecto (cada uno de una categoría fija: por hacer, en
curso, hecho, descartado), orden manual por `rank`, etiquetas, comentarios e
historial (rebanada 3a), descartar con motivo (issue 011), búsqueda, filtros
y orden en la lista (012) y color, límite WIP y columnas ocultas (013).

- **Contrato**: `issues.v1.IssueService` de `github.com/tekitla/tekitla-protos`
  (`gen/go/issues/v1`): proyectos, issues (`ListIssues`, `GetIssue`,
  `CreateIssue`, `MoveIssue`, `UpdateIssue`, `DiscardIssue`, `RestoreIssue`),
  estados (con `BatchUpdateStatuses`), etiquetas, `CreateComment` y
  `ListIssueEvents`. Los comentarios del `.proto` son la
  especificación.
- **Quién lo llama**: solo el `tekitla-gateway` (api-gateway), que autentica y
  propaga la identidad en la metadata gRPC `x-user-id` (además `x-roles` y
  `x-request-id`).
- **Identidad**: todo RPC exige `x-user-id` no vacío (si no, `UNAUTHENTICATED`).
  La lee el adapter (`handler/identity.go`) y la pasa como parámetro explícito
  (`ownerUID`) a la capa de aplicación; el dominio no lee el context.
- **Espacios de trabajo**: cada usuario tiene un espacio personal
  (`issues.workspaces`, uno por `owner_uid`) que se crea con su primer
  proyecto (`WorkspaceRepository.EnsurePersonal`, seguro con concurrencia); las
  lecturas no escriben (`GetPersonal`: sin espacio = sin proyectos). La capa de
  aplicación resuelve los espacios del usuario (`application/workspaces.go`) y
  los repos filtran por `workspace_id IN (...)`. Desde la rebanada 3 un espacio
  es un tenant de `accounts-service` (issue 005) y la lista llega del gateway.
- **Pertenencia**: todo se filtra por los espacios del usuario. Un proyecto
  ajeno responde `NOT_FOUND` (nunca `PERMISSION_DENIED`, para no revelar que
  existe). `IssueService` lo verifica con `ProjectRepository.GetByID` antes de
  tocar issues. `ListProjects` con un `workspace_id` ajeno devuelve la lista
  vacía; `CreateProject` en un espacio ajeno, `NOT_FOUND`. `workspace_id` no se
  valida como UUID (los ids de tenant pueden no serlo): uno desconocido es ajeno.

Reglas de negocio no obvias:

- Key del proyecto: 2–10 letras A-Z o dígitos, empieza con letra. Se recorta y
  se pasa a mayúsculas (`tk` → `TK`). Única por espacio: duplicada →
  `ALREADY_EXISTS` con reason `PROJECT_KEY_TAKEN`. Otro espacio puede usar la misma.
- Número de issue: correlativo por proyecto desde 1, no se reutiliza. Lo reserva
  `IssueRepository.Create` con `UPDATE projects SET next_issue_number = next_issue_number + 1
  ... RETURNING` en la misma transacción del `INSERT` (lock de fila: seguro con
  concurrencia).
- Issue nuevo: primer estado (por rank) de la categoría `TODO`; prioridad
  `UNSPECIFIED` se guarda como `MEDIUM`; `rank` al final del orden del
  proyecto (`model.RankAfter` del mayor rank, leído en la misma transacción con
  el lock de la fila del proyecto).
- Estados (`issues.statuses`): todo proyecto nace con cuatro, uno por categoría
  (`model.DefaultStatuses`); máximo 20; nombre único sin mayúsculas
  (`ALREADY_EXISTS`, reason `STATUS_NAME_TAKEN`); la categoría no cambia; no se
  borra el último de una categoría (`FAILED_PRECONDITION`, reason
  `LAST_STATUS_OF_CATEGORY`) ni uno con issues sin `move_to_status_id`
  (`STATUS_HAS_ISSUES`). `issues.state` es siempre la categoría de
  `issues.status_id`: lo asegura la FK compuesta `(status_id, project_id,
  state)` → `statuses (id, project_id, category)`.
- `MoveIssue`: `status_id` manda; con solo `state` va al primer estado de esa
  categoría, o no cambia si el issue ya está en ella. Reordenar
  (`after_number`/`before_number`): el rank sale entre el vecino y la clave
  contigua de **todo** el proyecto (`placeRank`), así nunca choca con un issue
  de otra columna; vecinos que no están en la columna destino o fuera de orden
  → `FAILED_PRECONDITION` (reason `STALE_ORDER`). Toma el lock del proyecto.
- Historial (`issues.issue_events`): toda escritura de un issue registra su
  evento en la misma transacción (creado, estado, título, descripción,
  prioridad, etiquetas, comentario); reordenar no. Orden por `seq` (identity).
  El JSONB `data` guarda los nombres de estados y etiquetas al momento del
  cambio (DTOs en `event_repo.go`: solo agregar campos).
- Etiquetas: máximo 100 por proyecto y 20 por issue; nombre único sin
  mayúsculas (`LABEL_NAME_TAKEN`); borrar una la quita de los issues sin evento.
- `rank`: *fractional indexing* (`model/rank.go`, port de `fractional-indexing`
  de rocicorp): base 62, se compara byte a byte (columna `COLLATE "C"`), entre
  dos claves siempre hay otra (`model.RankBetween`, para reordenar en la
  rebanada 3). Agregar al final da `a0`, `a1`, …, `az`, `b00`.
- Descartar (011): «descartado» = estar en un estado `CANCELED`, con o sin
  motivo. `DiscardIssue` lo pasa al estado `CANCELED` pedido (o al primero),
  conserva el rank y guarda `discard_reason`, `discarded_by` y `discarded_at`
  (las tres o ninguna, y solo en `canceled`: CHECKs de `000004`); ya
  descartado → `FAILED_PRECONDITION` `ISSUE_ALREADY_DISCARDED`.
  `RestoreIssue` lo pasa al primer estado `TODO`, al final de esa columna
  (`rankAtColumnEnd`); no descartado → `ISSUE_NOT_DISCARDED`. El motivo se
  mantiene entre estados `CANCELED` y se borra cuando el issue sale de
  `CANCELED` por cualquier vía (`MoveIssue`, `DeleteStatus`, el batch:
  `clearDiscard`). Eventos `discarded` (con motivo) y `restored`; descartar con
  `MoveIssue` deja un `status_changed`.
- Lista (012): filtros con Y entre sí y O dentro de cada lista; `state` y
  `states` juntos, `UNSPECIFIED` en una lista o un `status_id`/`label_id` de
  otro proyecto → `INVALID_ARGUMENT`. `query` (hasta 200 runas) busca en el
  título sin mayúsculas ni acentos con `issues.search_fold` (SQL de `000004`,
  `translate` + `lower`, sin extensiones) y LIKE escapado; si es un número
  (`12`) o una clave del proyecto (`TK-12`, la key sin mayúsculas) trae
  también ese issue (`model.ParseIssueSearch`). Orden `rank`, `updated_at
  DESC` o `priority DESC`, siempre con `rank` de desempate. El cursor lleva
  la huella de filtros y orden (`issueFilterFingerprint`): usarlo con otros →
  `INVALID_ARGUMENT`.
- Estados (013): `color` (mismo enum que las etiquetas; `UNSPECIFIED` = el
  de la categoría, `model.DefaultStatusColor`), `wip_limit` (0 = sin límite,
  negativo `INVALID_ARGUMENT`; pasarse no bloquea nada) y `hidden` (los
  `CANCELED` nacen ocultos). `BatchUpdateStatuses` es declarativo: lo valida
  `model.PlanStatusBatch` contra los estados actuales (un estado que no está
  ni en `statuses` ni en `deletions` → `FAILED_PRECONDITION`
  `STALE_STATUSES`) y el repo lo guarda con el lock del proyecto, con nombres
  y ranks provisorios para no chocar con los índices únicos a mitad del
  cambio. Ranks nuevos por posición (`a0`, `a1`, …). Sin control de versión:
  pisa lo guardado.
- `MoveIssue` sin `status_id` y con estado `UNSPECIFIED` (o desconocido) →
  `INVALID_ARGUMENT`; un `status_id` de otro proyecto también. Un valor de
  enum desconocido en el filtro de `ListIssues` también.
- `CANCELED` (`canceled` en la base) es un estado más: `MoveIssue` lo acepta y
  `ListIssues` sin filtro lo incluye (el panel lo oculta).
- Orden: proyectos por `name, id`; issues por `rank` (único por proyecto) por
  default. Un cursor de `v0.1` (por prioridad) o de `v0.3` (sin huella)
  responde `INVALID_ARGUMENT`.

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

Contrato gRPC: repo de protos `github.com/tekitla/tekitla-protos` (ver
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
- No hay tests de integración de los repos contra Postgres: se prueban
  levantando `make up` y pegándole con `grpcurl` (ver "Comandos").
- Migraciones: `make test-migrations` (`scripts/test_migrations.sh`) aplica
  todas contra un Postgres 16 descartable en docker, carga datos de la versión
  anterior antes de cada backfill, verifica el backfill y las restricciones, y
  prueba down → up de la última. Una migración nueva suma ahí sus
  verificaciones.

## Datos

- PK **UUID** por default, generada en el service (`uuid.NewString()`). PK de
  texto solo si el proto la expone como identificador estable del cliente (y
  entonces upsert con `clause.OnConflict`).
- Paginación keyset con cursor opaco (`cursor.go`: JSON + base64 URL-safe, un
  struct por listado con las columnas de su `ORDER BY` + `id`): pedir `limit+1`
  para `has_next`. Default 50 / máximo 100 en `application/common.go`.
  Cursor mal formado → `INVALID_ARGUMENT`.
- Tablas: `issues.workspaces` (único `owner_uid`), `issues.projects` (FK al
  espacio, único `workspace_id, key`; `next_issue_number`), `issues.issues` (FK
  al proyecto y a su estado, únicos `project_id, number` y `project_id, rank`;
  `state` texto `todo|in_progress|done|canceled` y `priority` 1..4, ambos con
  `CHECK`; `rank` `TEXT COLLATE "C"`; `discard_*` del descarte),
  `issues.statuses` (`color` 1..7, `wip_limit` ≥ 0, `hidden`), `issues.labels`,
  `issues.issue_labels`, `issues.comments` e `issues.issue_events`. Timestamps
  truncados a microsegundos en el repo (`dbNow`).
- JSONB: DTOs unexported separados del dominio + `encoding/json`.
- Migraciones en `migrations/NNNNNN_desc.{up,down}.sql`, aplicadas a mano
  (`DATABASE_URL=... scripts/apply_migration.sh <qa|prod> <migración>`).
  `000001` crea el schema, `000002` las tablas de la rebanada 1 y `000003` las
  de la 3a con backfill (estados de cada proyecto, `status_id` de cada issue y
  el evento «creado»), y `000004` las de 011 a 013 (descarte, `search_fold`,
  índices de los órdenes, color/WIP/visibilidad con backfill por categoría). El `docker-compose.yml` local las aplica
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
make test-migrations          # migraciones + backfill contra un Postgres descartable
make lint                     # gofmt + go vet (+ golangci-lint si está)
make build                    # bin/issues-service
go mod tidy                   # después de tocar imports
make workspace / workspace-off  # co-desarrollar con un checkout local de los protos
grpcurl -plaintext localhost:5000 list   # con reflection (no prod)
grpcurl -plaintext -H 'x-user-id: u1' -d '{"key":"TK","name":"Tekitla"}'   localhost:5000 issues.v1.IssueService/CreateProject
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
- `000003` no es compatible con el binario anterior (exige `status_id` y estados
  por proyecto) y `000004` tampoco (estados sin DEFAULT de color, WIP y
  visibilidad): se aplica y se despliega la imagen nueva en seguida, sin
  tráfico de por medio.
- Release con cambios en `migrations/`: la guarda de prod bloquea. Aplicar el
  SQL por túnel SSH (`scripts/apply_migration.sh prod <migración> --apply`) y
  lanzar el override.
- Rollback: `IMAGE_TAG=<tag inmutable anterior>` en el panel de Coolify + Redeploy.
- Setup de una vez (recurso, variables, backups a S3, gRPC detrás de Traefik):
  `docs/deploy.md`. Mudar a Cloud Run: `docs/migrar-a-cloud-run.md`.
- Los comandos con credenciales o confirmación (`apply_migration.sh prod --apply`,
  `migrate_db.sh --apply`, cambios en el panel de Coolify) los corre una persona.

## Protos

Repo de protos: `github.com/tekitla/tekitla-protos`, hoy en `v0.4.0`
(`go.mod`). Paquete Go `github.com/tekitla/tekitla-protos/gen/go/issues/v1`
(alias `issuesv1`), usado solo en `handler/`, `mapper/` y `cli/main.go`.

- Versión nueva del contrato: `go get github.com/tekitla/tekitla-protos@vX.Y.Z`
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
