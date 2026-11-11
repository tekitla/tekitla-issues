# import-round

Importa a Tekitla issues escritos en markdown con `ImportIssue`. Lee el repo
de origen en solo lectura. Tiene dos formatos (`-formato`):

| Formato | Origen | Proyecto por defecto | Issue |
|---|---|---|---|
| `round` (default) | `documentacion/Issues/<n>-<slug>.md` de Round | `RND` | 025 |
| `template-docs` | `contenido/issues/<nnn>-<slug>/issue.md` con frontmatter YAML | `TK` (`tekitla/tekitla-docs`), `VR` (`vrojas1015/docs`) | 035 |

Los dos usan el mismo plan (qué archivo se queda con cada número), el mismo
reporte y la misma llamada. Se puede volver a correr: la clave de origen
reconoce cada issue (Round: el nombre del archivo, número + slug;
template-docs: el `id`, así renombrar el slug de la carpeta sigue
actualizando el mismo issue). Una corrida sin cambios no crea ni cambia
nada. El repo de docs manda: lo que se cambie en
el panel lo pisa la corrida siguiente, salvo las etiquetas puestas a mano.

## Cómo se corre

El servicio se alcanza por gRPC sin TLS: local (`make up`) o por un túnel
SSH al contenedor. Correrlo contra QA o prod lo decide una persona, y no hay
valores por defecto que apunten ahí. `-user` es el uid de Firebase de quien
importa.

```bash
# Round
go run ./tools/import-round -dry-run
go run ./tools/import-round -user <uid> -addr localhost:5000 -create-project

# template-docs: -dir es la raíz del repo de docs (la que tiene contenido/issues)
go run ./tools/import-round -formato template-docs -dir C:\dev\tekitla\docs -dry-run
go run ./tools/import-round -formato template-docs -dir C:\dev\tekitla\docs -user <uid> -create-project          # a TK
go run ./tools/import-round -formato template-docs -dir C:\dev\vrojas1015\docs -user <uid> -create-project       # a VR
go run ./tools/import-round -formato template-docs -dir <repo> -user <uid> -project DD -create-project           # otra clave
```

- `-project`: clave del proyecto destino. Con `template-docs` sin
  `-project`, la clave sugerida sale del remoto `origin` de `-dir`. Un repo
  sin clave sugerida exige `-project`.
- `-repo owner/repo`: el repo de GitHub de los enlaces y de la clave
  sugerida, si `-dir` no tiene un remoto `origin` de GitHub.
- `-create-project`: crea el proyecto si no existe. Sin el flag, un proyecto
  que falta es un error.
- `-dry-run`: solo lee y reporta, sin llamar al servicio.
- Antes de leer verifica que la copia esté en `main`, sin cambios y al día
  con `origin/main`. `-skip-git-check` lo saltea.

## Traducción de template-docs

| Frontmatter | Tekitla |
|---|---|
| `id` | Número del issue y clave de origen (`TK-30` = `id: 30`). Si no coincide con el número de la carpeta, manda el `id` y el reporte lo avisa. Con un `id` repetido gana la carpeta cuyo número coincide; las demás quedan afuera |
| `titulo` | Título (sin `titulo`: el `# ` del cuerpo; sin ninguno: el slug). Más de 200 caracteres: se recorta y se avisa |
| `estado`: `abierto` · `en-curso` · `bloqueado` · `resuelto` · `descartado` | Por hacer · En curso · En curso + etiqueta `bloqueado` · Hecho · Descartado |
| `prioridad`: `alta` · `media` · `baja` | `HIGH` · `MEDIUM` · `LOW` |
| `tipo`, `area`, `repos` | Etiquetas, en minúsculas y sin repetir (`bloqueado` va después de `tipo`). Más de 20: se quedan las primeras y se avisa |
| `creado` | Fecha de alta (solo al crear) |
| `resuelto` | Línea «Resuelto: AAAA-MM-DD» al pie de la descripción. El contrato no tiene esa fecha |
| Cuerpo | Descripción, sin el `# ` que repite el título, con enlace al `issue.md` en GitHub (rama `main`) y al `plan.md` si lo hay |

El frontmatter abre con `---` y cierra con `---` o `...`. Solo se leen las
carpetas `<número>-<slug>` de `contenido/issues`. ADRs,
runbooks, índices y planes no se importan.

## Reporte

Importados, actualizados, sin cambios y con error, más cada caso con su
ruta: números repetidos, importados antes que ya no tienen archivo, estados
o prioridades desconocidos (van como Por hacer y `MEDIUM`), sin fecha, sin
etiquetas, `id` distinto de la carpeta, título, etiquetas o descripción
recortados y archivos
rotos (sin `issue.md`, sin frontmatter, YAML inválido, sin `id`). Un archivo
roto no frena a los demás.

## Histórico de Round (`-historico`, issue 036)

Reconstruye las transiciones de estado de cada issue de Round desde git, para
las métricas de flujo (lead time, cerrados por semana, WIP), y lo carga en
Tekitla con `ImportIssueEvents` (protos v0.6.0).

```bash
go run ./tools/import-round -historico -dry-run            # resumen y casos
go run ./tools/import-round -historico -dry-run -detalle   # además, cada evento
go run ./tools/import-round -historico -user <uid> -addr localhost:5000                 # carga
go run ./tools/import-round -historico -user <uid> -addr localhost:5000 -sobrescribir   # corrige lo ya cargado
```

**Carga.** Va sobre issues ya importados: primero se corre `import-round`
(el proyecto tiene que existir). Por cada issue:

1. Si su alta en Tekitla no es la del histórico, la corrige con
   `ImportIssue` y `override_created_at`. Esa llamada también refleja el
   archivo, como una corrida normal.
2. Manda sus transiciones con `ImportIssueEvents`. La `source_ref` es el
   sha del commit, o `inicial` para el estado con que nace el archivo. La
   fuente es `IMPORT_HISTORY` (commit) o `IMPORT_FILE` (`Fecha resuelto`).
   Los nombres son los valores del origen (`open`, `in-progress`), a lo
   sumo 40 caracteres.

Es idempotente: una segunda corrida no agrega nada. Con `-sobrescribir`,
reescribe los eventos ya cargados (fecha y cambio), para corregir una carga
equivocada. Un issue sin importar o un error van al reporte y no frenan a
los demás.

Antes, en un proyecto espejo con eventos de `ImportIssue` anteriores a
v0.6.0, conviene marcarlos `import_sync` con
`scripts/backfill_import_sync.sh` (ver el CLAUDE.md del repo).

- Un solo `git log --first-parent -M --name-status` de la carpeta, más
  `git cat-file --batch` para leer cada versión. Los renombres se siguen con
  la misma detección que `--follow`, y la clave es el nombre actual del
  archivo. Solo entra el archivo que se queda con el número, según el mismo
  plan de la importación. Con una copia superficial (`clone --depth`) no
  corre: el histórico saldría falso. Si `git cat-file` falla a mitad de la
  lectura, la corrida termina con error y sin reporte.
- **Solo la historia de `main`:** un cambio hecho en una rama cuenta cuando
  llega a `main` (en el merge, con la fecha del merge). Así dos ramas
  paralelas no se intercalan, porque eso inventaría transiciones de ida y
  vuelta.
- **Alta:** `Fecha detectado` si es de un día anterior al primer commit; si
  no, el primer commit.
- **Transición:** cada commit en que cambia el valor del `Estado:` (su primer
  token, como en la importación) es una transición con la fecha del commit
  (committer). Cambiar solo el texto alrededor no cuenta. Un commit sin línea
  `Estado:` se salta.
- **Cierre del archivo (CU-036-2):** si el archivo nace cerrado (en su primer
  commit con `Estado:`), ningún commit marca el cierre. Entonces su
  `Fecha resuelto` es la fecha del cierre, con fuente «archivo», siempre que
  caiga entre el alta y ese commit. Si después un commit cierra el issue,
  ese cierre usa el commit.
- **Idempotencia (CU-036-1):** cada evento tiene una clave estable:
  `<clave>#alta`, `<clave>#inicial` (el estado con que nace el archivo, si
  no es por hacer) o `<clave>#<sha>`. La clave no depende de la fuente:
  agregar después `Fecha resuelto` no la cambia. Una segunda corrida con lo
  ya cargado no deja nada pendiente. Reescribir la historia de git (rebase)
  sí cambia los sha.
- **Reporte:** totales (eventos, de commits o del archivo; al cargar,
  cargados, ya estaban, reescritos y altas corregidas), altas y cierres
  tomados del archivo, `Fecha detectado` posterior al primer commit,
  `Fecha resuelto` sin usar y por qué, estados desconocidos (una vez por
  valor), versiones ilegibles y números repetidos. Todo es «histórico
  aproximado»: los estados se cambiaban a mano y a veces tarde.
