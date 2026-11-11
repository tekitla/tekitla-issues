# import-round

Importa a Tekitla issues escritos en markdown con `ImportIssue`. Lee el repo
de origen en solo lectura. Tiene dos formatos (`-formato`):

| Formato | Origen | Proyecto por defecto | Issue |
|---|---|---|---|
| `round` (default) | `documentacion/Issues/<n>-<slug>.md` de Round | `RND` | 025 |
| `template-docs` | `contenido/issues/<nnn>-<slug>/issue.md` con frontmatter YAML | `TK` (`tekitla/tekitla-docs`), `VR` (`vrojas1015/docs`) | 035 |

Los dos usan el mismo plan (qué archivo se queda con cada número), el mismo
reporte y la misma llamada. Se puede volver a correr: la clave de origen
(carpeta o archivo, número + slug) reconoce cada issue. Una corrida sin
cambios no crea ni cambia nada. El repo de docs manda: lo que se cambie en
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
| `id` | Número del issue (`TK-30` = `id: 30`). Si no coincide con el número de la carpeta, manda el `id` y el reporte lo avisa |
| `titulo` | Título (sin `titulo`: el `# ` del cuerpo; sin ninguno: el slug) |
| `estado`: `abierto` · `en-curso` · `bloqueado` · `resuelto` · `descartado` | Por hacer · En curso · En curso + etiqueta `bloqueado` · Hecho · Descartado |
| `prioridad`: `alta` · `media` · `baja` | `HIGH` · `MEDIUM` · `LOW` |
| `tipo`, `area`, `repos` | Etiquetas, en minúsculas y sin repetir |
| `creado` | Fecha de alta (solo al crear) |
| `resuelto` | Línea «Resuelto: AAAA-MM-DD» al pie de la descripción. El contrato no tiene esa fecha |
| Cuerpo | Descripción, sin el `# ` del título, con enlace al `issue.md` en GitHub (rama `main`) y al `plan.md` si lo hay |

Solo se leen las carpetas `<número>-<slug>` de `contenido/issues`. ADRs,
runbooks, índices y planes no se importan.

## Reporte

Importados, actualizados, sin cambios y con error, más cada caso con su
ruta: números repetidos, importados antes que ya no tienen archivo, estados
o prioridades desconocidos (van como Por hacer y `MEDIUM`), sin fecha, sin
etiquetas, `id` distinto de la carpeta, descripción recortada y archivos
rotos (sin `issue.md`, sin frontmatter, YAML inválido, sin `id`). Un archivo
roto no frena a los demás.
