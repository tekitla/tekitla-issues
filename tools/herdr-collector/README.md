# herdr-collector

Lee las sesiones de agentes de herdr (multiplexor de terminal para agentes) en esta máquina y
las reporta a Tekitla con `ReportSessions` (issue 026). Corre en la máquina
de la persona, al lado de herdr. Si herdr o Tekitla no responden, anota el
error y sigue: nunca bloquea a los agentes.

## Uso

```powershell
# Una vez: token de la persona con el permiso sessions:write (desde el panel).
# Variable de entorno de usuario, nunca en un archivo del repo.
[Environment]::SetEnvironmentVariable('TEKITLA_TOKEN', 'tka_…', 'User')

go run ./tools/herdr-collector -url https://api.tekitla.victorrb1015.com `
  -root C:\dev -machine PC-01 -projects round=RND,tekitla=TK

# Sin mandar nada: muestra el reporte de un ciclo.
go run ./tools/herdr-collector -dry-run -once -root C:\dev -projects round=RND,tekitla=TK
```

| Flag | Default | Qué es |
|---|---|---|
| `-url` | — | Gateway de Tekitla (REST) |
| `-root` | — | Raíz de los repos: `cwd_rel` es relativo a ella |
| `-machine` | — | Nombre corto de la máquina (`[A-Za-z0-9._-]`, hasta 40) |
| `-projects` | — | `nombre=CLAVE` separados por coma |
| `-interval` | `15s` | Cada cuánto lee herdr |
| `-heartbeat` | `2m` | Latido: manda aunque no cambie nada |
| `-herdr` | `herdr` | Binario de herdr |
| `-once`, `-dry-run` | — | Un solo ciclo / no mandar |

## Qué manda

Cada ciclo corre `herdr agent list` y `herdr workspace list` y arma la lista
completa de la máquina. La manda si cambió algo o si pasó el latido. El
servicio pasa a terminada toda sesión de la máquina que no venga.

- **Estado:** `working` → trabajando, `blocked` → esperando-ok, `idle` y
  `done` → en-reposo (en herdr, `done` es «terminó su turno» con el panel
  abierto; decisión de Victor, 2026-11-18), otro → desconocido.
  «Terminada» la pone el servicio cuando el panel deja de aparecer.
- **Qué espera** (tokens de `espera.ps1`): `espera_tipo` = `persona`,
  `sesion` (con `espera_a`) u `otro`. Sin `espera_tipo`, el respaldo mira el
  texto de `espera`: `🙋` → persona, `⏳ espera a <X>` → sesión `<X>`, otro →
  otra cosa. Una sesión que trabaja no espera nada.
- **Para qué espera** (issue 036, token `espera_motivo` de
  `espera.ps1 -Motivo`): `revision`, `pruebas`, `decision` o `deploy`. Se
  manda solo si la sesión espera algo (o está en esperando-ok sin `espera_tipo`). Un valor desconocido va sin motivo y
  no hace fallar el reporte. Define el perfil de la señal de contratación.
- **Issue** (la primera regla que reconoce, con el proyecto en `-projects`):
  nombre del agente `<proyecto>-issue-<n>`, workspace `<proyecto> #<n>`, o
  `cwd` con `worktrees/issue-<n>` o `<repo>-wt/issue-<n>` (el proyecto es la
  carpeta raíz).
- **Datos personales:** `cwd` relativo a `-root` (vacío fuera de ella). No
  viajan rutas absolutas ni el usuario del sistema.

Las sesiones que el servicio no pudo ligar o guardar (proyecto desconocido,
agente revocado) salen en el log con el motivo.
