# Pull Request

## Resumen
<!-- Una o dos oraciones sobre qué cambia este PR. -->


## Tipo de cambio
- [ ] feat — funcionalidad nueva
- [ ] fix — bug fix
- [ ] docs — solo documentación
- [ ] refactor — cambio interno sin afectar comportamiento
- [ ] adapter — adapter nuevo o modificación de uno existente
- [ ] ADR — decisión arquitectónica (incluir el ADR en este PR)
- [ ] otro: ___

## Modo afectado
- [ ] quick_deploy
- [ ] development
- [ ] production
- [ ] N/A (cambio en core / docs)

## Checklist
- [ ] El linter pasa: `python tools/compliance/paa-lint-compliance.py --ci`
- [ ] Sintaxis Python OK: `python -m py_compile $(git diff --name-only main HEAD | grep '\.py$')`
- [ ] Pruebas agregadas o actualizadas (si aplica)
- [ ] Documentación actualizada (`docs/`, READMEs, ADRs)
- [ ] Si es un adapter nuevo: registrado en `paa_core/composer.py`
- [ ] Si es un dominio nuevo: defaults agregados a `shared/modes/*_defaults.yaml`
- [ ] Sin secretos hardcoded (revisado)
- [ ] Sin valores `<<TODO>>` que bloqueen ejecución

## ADRs relacionados
<!-- Si este PR implementa o modifica una decisión registrada, link aquí. -->


## Cómo probar
<!-- Pasos concretos para que el reviewer reproduzca tu cambio. -->


## Riesgo / blast radius
<!-- ¿Qué se rompe si esto sale mal? ¿Qué tenants/agentes afecta? -->


## Owner del cambio
<!-- @nombre del autor / team -->
