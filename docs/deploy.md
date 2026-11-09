# Deploy en Coolify v4 — issues-service

Setup **de una sola vez** para desplegar el servicio en un VPS con
[Coolify v4](https://coolify.io/docs), con build en la CI
(GitHub Actions → GHCR) y deploy disparado por tag de git.

```bash
git tag qa-v0.1.0   && git push origin qa-v0.1.0     # build + push + deploy a QA
git tag prod-v0.1.0 && git push origin prod-v0.1.0   # idem prod (auto salvo migraciones)
```

> Los nombres de menús y campos de Coolify cambian entre versiones de v4. Los
> pasos describen **qué** configurar; si un nombre no coincide, buscá el
> equivalente en tu versión. Lo que está entre `código` del lado del repo
> (compose, CI, scripts) sí está probado.

## Cómo funciona

```
tag qa-v1.2.3 ─▶ CI: test ─▶ docker build ─▶ push <registry>/<repo>:qa-v1.2.3 y :qa
                                          └▶ curl $COOLIFY_URL/api/v1/deploy?uuid=<recurso QA>
Coolify (VPS) ─▶ docker compose -f docker-compose.coolify.yml up (pull_policy: always)
                 app (IMAGE_TAG=qa) ──red coolify──▶ Postgres compartido del entorno
```

- **Un recurso de Coolify por entorno** (QA y prod), los dos con
  `docker-compose.coolify.yml`. El compose **no levanta Postgres**: usa el
  Postgres compartido de Coolify (proyecto `databases`, un Postgres 18 por
  entorno para todos los proyectos personales), con base y usuario propios de
  este servicio (paso 2).
- La imagen se construye **una vez en la CI**; el VPS solo la baja. Tags: el
  inmutable (`qa-v1.2.3`) y uno móvil por entorno (`qa` / `prod`).
- **Mismas variables de entorno que Cloud Run** (`APP_ENV`, `SERVICE_ENV`,
  `DEBUG`, `LOG_LEVEL`, `GRPC_PORT`, `DATABASE_URL`, `OTEL_*`). Mudar el
  servicio a Cloud Run es cambiar dónde corre, no el código
  (ver `docs/migrar-a-cloud-run.md`).
- Migraciones: **a mano, antes del deploy** que las necesita
  (`scripts/apply_migration.sh`). Prod tiene la misma guarda que en Cloud Run:
  si el release toca `migrations/`, el deploy automático no corre.

## 1. Acceso del VPS al registry

La imagen es privada: el Docker del VPS tiene que poder hacer `pull`. En el
servidor, como el usuario con el que corre Docker (normalmente `root`):

```bash
# Fine-grained o classic token con read:packages (de una cuenta de servicio, no personal)
docker login ghcr.io -u <usuario> --password-stdin
```

Coolify también permite cargar credenciales de registry desde el panel en
algunas versiones; el `docker login` en el servidor funciona en cualquier caso.

## 2. Base y usuario en el Postgres compartido (QA; repetir para prod)

Cada servicio tiene **su propia base y su propio usuario** dentro del Postgres
compartido del entorno; ningún servicio usa el superusuario ni la base de otro.
Lo corre una persona, conectada como superusuario al Postgres compartido
(terminal del recurso en Coolify, o `psql` por el túnel del paso 6). La
contraseña se pide con `\password`: no se escribe en el SQL.

```sql
CREATE ROLE tekitla_issues LOGIN;
\password tekitla_issues
CREATE DATABASE tekitla_issues OWNER tekitla_issues;
REVOKE ALL ON DATABASE tekitla_issues FROM PUBLIC;
```

Generá la contraseña con `openssl rand -hex 24` (hex: no hay que escaparla en
la URL). Las migraciones crean el schema `issues` con este usuario como dueño
(paso 6).

## 3. Crear el recurso (QA; repetir para prod)

1. Proyecto/entorno en Coolify (p. ej. proyecto `issues-service`, entornos
   `qa` y `production`).
2. **New resource** → desde el repositorio git (público, GitHub App o deploy
   key) → build pack **Docker Compose**.
3. Rama: la principal. **Docker Compose location**: `/docker-compose.coolify.yml`.
4. Desactivá el auto-deploy por push/webhook del recurso: el deploy lo dispara
   la CI cuando hay un tag, no cada commit a la rama.
5. Activá **Connect to Predefined Network** en la configuración del recurso:
   conecta los contenedores a la red `coolify`, donde el Postgres compartido
   se resuelve por el UUID de su recurso. Sin esto la app no llega a la base.
6. Anotá el **UUID** del recurso (aparece en la URL del panel y en la config):
   va a `COOLIFY_RESOURCE_UUID_QA` (o `_PROD`).

## 4. Variables de entorno (panel del recurso)

Coolify detecta las `${VAR}` del compose y las lista en *Environment
Variables*. Las marcadas con `:?` son obligatorias (el deploy falla si faltan).

| Variable | QA | Prod | Notas |
|---|---|---|---|
| `IMAGE_TAG` | `qa` | `prod` | Tag móvil. Rollback: poné un tag inmutable (`prod-v1.2.2`) y Redeploy |
| `IMAGE_REPO` | — | — | Opcional. Default `ghcr.io/tekitla/tekitla-issues` |
| `APP_ENV` | `development` | `production` | Igual que Cloud Run (QA usa `development`: reflection activo) |
| `SERVICE_ENV` | `qa` | `production` | |
| `DEBUG` | `true` | `false` | |
| `LOG_LEVEL` | `debug` | `info` | |
| `DATABASE_URL` | ver abajo | ver abajo | Secreto |

```
DATABASE_URL=postgres://tekitla_issues:<contraseña>@<UUID-del-postgres>:5432/tekitla_issues?sslmode=disable
```

El host es el **UUID del recurso de Postgres compartido** del entorno (en QA,
`postgres-qa`: `glisbusivbo0vdizn0bfk6m4`), que es su nombre en la red
`coolify`. `sslmode=disable` porque el tráfico no sale de la red de Docker del
VPS. `GRPC_PORT` y `OTEL_SERVICE_NAME` están fijos en el compose (`5000` /
`issues-service`).

> Cambiar la contraseña: `\password tekitla_issues` en el Postgres
> compartido, actualizar `DATABASE_URL` en el panel y Redeploy.

## 5. Variables de la CI

Settings > Secrets and variables > Actions:

| Tipo | Nombre | Valor |
|---|---|---|
| Variable | `COOLIFY_URL` | `https://coolify.ejemplo.com` (sin `/` final) |
| Secret | `COOLIFY_TOKEN` | token de API de Coolify con permiso de deploy |
| Variable | `COOLIFY_RESOURCE_UUID_QA` | UUID del recurso de QA |
| Variable | `COOLIFY_RESOURCE_UUID_PROD` | UUID del recurso de prod |

Settings > Environments: crear `qa` y `prod`; en `prod`, *Deployment branches
and tags* → solo `prod-v*` y *Required reviewers* (protege también el
override). Tag ruleset sobre `prod-v*` (Settings > Rules > Rulesets).

El push a GHCR usa `GITHUB_TOKEN` (`packages: write`). La primera vez, el
paquete queda privado y vinculado al repo; el VPS lo baja con el token del paso 1.

Probar la llamada a la API a mano (no despliega nada si el UUID es inválido):

```bash
curl -fsS "$COOLIFY_URL/api/v1/deploy?uuid=$COOLIFY_RESOURCE_UUID_QA&force=false" \
  -H "Authorization: Bearer $COOLIFY_TOKEN"
```

## 6. Migraciones y primer deploy

Las migraciones se aplican **antes** del deploy que las necesita, por túnel SSH
al Postgres compartido y **con el usuario del servicio** (así el schema
`issues` queda a su nombre).

1. Túnel. El Postgres compartido no publica puertos a internet: el túnel va a
   la IP de su contenedor en la red `coolify`.

```bash
# En el VPS: IP del contenedor del Postgres compartido (cambia si se recrea)
docker inspect -f '{{ (index .NetworkSettings.Networks "coolify").IPAddress }}' <UUID-del-postgres>
# En tu máquina:
ssh -N -L 15432:<IP-del-contenedor>:5432 usuario@vps &
```

2. Migraciones, en orden (`000001`, `000002`, `000003`, …):

```bash
read -rs DATABASE_URL && export DATABASE_URL   # postgres://tekitla_issues:<pass>@localhost:15432/tekitla_issues?sslmode=disable
./scripts/apply_migration.sh qa 000001_create_issues_schema
```

3. `git tag qa-v0.1.0 && git push origin qa-v0.1.0`. La CI construye, pushea y
   dispara el deploy; el healthcheck marca la app `healthy` (al arrancar solo
   hace ping a la base).

En prod: `./scripts/apply_migration.sh prod <migración>` (prueba en seco) y
después `--apply` (pide escribir el nombre de la base).

El **primer** `prod-v*` siempre va por el override
(Actions > ci > Run workflow): no hay tag anterior contra el que comparar.

## 7. Dominio y exposición de gRPC

El servicio es **interno**: el compose no publica el puerto de la app al host
(solo `expose`). Lo normal es que lo llame un gateway HTTP que corre en el
mismo servidor y es lo único con dominio público.

- **Gateway en otro recurso de Coolify**: cada recurso de Docker Compose tiene
  su propia red. Este recurso ya está en la red predefinida `coolify` (paso 3,
  por el Postgres); el gateway también tiene que estarlo, y usa como host el
  nombre del contenedor/servicio que muestra Coolify.
- **gRPC público detrás del proxy de Coolify (Traefik)**: gRPC necesita HTTP/2
  de punta a punta. Traefik termina TLS y por defecto habla con el backend por
  HTTP/1.1, lo que rompe gRPC. Hay que declarar el backend como **h2c**
  (en Traefik, `loadbalancer.server.scheme=h2c` en el servicio). Cómo agregar
  esa label sin que Coolify la pise depende de la versión: **no está probado en
  esta plantilla**. Si lo necesitás, probalo en QA con
  `grpcurl <dominio>:443 grpc.health.v1.Health/Check` antes de depender de eso.
  Recomendación: exponer un gateway HTTP/JSON y dejar este servicio interno.
- Si le asignás dominio al servicio `app` en el panel, Coolify genera las
  labels de Traefik para el puerto que indiques (`5000`).

## 8. Backups (y prueba de restore)

La base vive en el Postgres compartido: los backups programados a S3 se
configuran **una vez en el recurso de Postgres** (proyecto `databases`, pestaña
*Backups*) y no en este recurso. Verificá que incluyan `tekitla_issues`
**antes de tener datos reales en prod**. RPO = la frecuencia del backup (no hay
point-in-time recovery).

Para un dump puntual solo de este servicio (p. ej. antes de una migración en
prod), por el túnel del paso 6:

```bash
read -rs PGPASSWORD && export PGPASSWORD
docker run --rm -e PGPASSWORD postgres:18 pg_dump -h host.docker.internal -p 15432 \
  -U tekitla_issues -d tekitla_issues -n issues -Fc > "tekitla_issues-$(date -u +%Y%m%dT%H%M%SZ).dump"
```

### Prueba de restore (mensual, y siempre después de cambiar el backup)

Un backup que nunca se restauró no es un backup. En cualquier máquina con Docker:

```bash
# Un dump de la base (backup del Postgres compartido o el dump puntual de arriba)
cp <dump> ./restore-test.dump
docker run -d --name restore-test -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:18
sleep 5
docker exec -i restore-test pg_restore -U postgres -d postgres --no-owner --no-privileges --exit-on-error < restore-test.dump
docker exec restore-test psql -U postgres -c "SELECT count(*) FROM issues.issues"
# Opcional: levantar la imagen del servicio contra esta base y pegarle con grpcurl.
docker rm -f restore-test && rm restore-test.dump
```

Anotá fecha, tamaño del dump y tiempo de restore (es tu RTO real).

## 9. Release, guarda de prod y rollback

- `qa-v*` → deploy automático a QA.
- `prod-v*` → la guarda compara contra el `prod-v*` anterior; si el rango toca
  `migrations/`, el deploy no corre: aplicá el SQL
  (`scripts/apply_migration.sh prod <migración> --apply`, por túnel) y lanzá el
  override.
- **Rollback** (no reconstruye): en el panel del recurso, `IMAGE_TAG=prod-v1.2.2`
  (un tag inmutable que ya existe en el registry) → Redeploy. Para volver al
  flujo normal, `IMAGE_TAG=prod` y un tag nuevo. Si la versión mala trajo una
  migración, evaluá el `.down.sql` antes de volver a una imagen vieja.

## 10. Diagnóstico

| Síntoma | Causa probable |
|---|---|
| El deploy falla con `required variable ... is missing a value` | Falta una variable `:?` en el panel |
| `pull access denied` / `unauthorized` | Falta el `docker login` del paso 1 en el VPS |
| La app queda `unhealthy` | `DATABASE_URL` mal (mirá los logs: `failed to ping database`) o el puerto no es `5000` |
| `failed to ping database` con `no such host` | Falta **Connect to Predefined Network** (paso 3) o el host no es el UUID del Postgres compartido |
| `permission denied for schema issues` | Las migraciones se aplicaron con otro usuario: el schema tiene que ser de `tekitla_issues` |
| La CI hace deploy pero sigue la versión vieja | El recurso no usa `IMAGE_TAG=qa` (o `prod`) o se perdió `pull_policy: always` |
| `curl: (22) ... 401` en el job | `COOLIFY_TOKEN` inválido o sin permiso de deploy |
