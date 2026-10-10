## Qué cambia

<!-- Una o dos líneas. El título va con el issue: feat(issue-<n>): … (chore(deps) sin issue). -->

Issue: issue-N  <!-- carpeta contenido/issues/N-…/ del repo de docs; obligatorio salvo chore(deps) -->

## Cómo se probó

- [ ] `go build ./... && go vet ./...`
- [ ] `go test -count=1 ./...`
- [ ] Probado contra la base local (`make run` + grpcurl) en los RPC tocados

## Migraciones

- [ ] No toca `migrations/`
- [ ] Toca `migrations/`: tiene `down`, se probó up→down→up, y el deploy a prod requiere aplicar el SQL a mano

## Checklist

- [ ] Sin secretos ni datos personales en el diff (gitleaks corre en el CI igual)
- [ ] Migraciones: tienen `down` / no aplica
- [ ] Docs del repo actualizadas (README, `docs/`, CLAUDE.md) / no aplica
- [ ] Ficha o ADR en el repo de docs actualizados / no aplica

## Notas para el review

<!-- Decisiones, deuda que queda, PR relacionados en otros repos. -->
