# Handoff — certificação full-product (P59-T04)

**Status:** preparação concluída; certificação **NÃO** concedida. Este documento
é imutável e vale para o SHA do merge de P59; qualquer commit posterior exige
novo handoff.

**Objetivo:** registrar o que a sequência P48–P59 entregou, o que foi provado
neste SHA e o que ainda bloqueia a certificação — para que o gate final
pós-merge (reutilização de P45 per `RELEASE_PLAYBOOK`) parta de fatos, não de
memória.

## O que P59 entregou (branch `phase-59-frontend-final-release-handoff`, PR #415)

| Tarefa | Commit | Entrega |
|--------|--------|---------|
| T01 | `41531e2` | `RELEASE_SCOPE.md` com P48–P58 (PR + merge SHA verificados), 62 migrations, 38 pacotes, toolchains, aceite do frontend |
| T02 | `b858819` | `frontend-coverage-complete` no Makefile, área `frontend` no decisor, P45-G23/G24 na matriz (24 portões), 4 recusas novas |
| T03 | `87e50a0` | `EXTERNAL_APPROVALS.md` (7 itens externos), R10 smoke com/sem JS, §6 gates de ativação futura |
| T04 | este commit | este handoff |

## Provas neste SHA (exit gate rápido)

- `make quick-verify` local: ok (inclui `tsc --noEmit` do frontend).
- `go test ./tools/releasematrix/` ok (12 casos executados); `./tools/economycertify/` ok (54 casos executados).
- `make release-matrix` ok; `make frontend-coverage-complete` ok.
- `make audit-runbooks` ok; `make handoff-check` ok; `git diff --check` limpo.
- Matriz integral, suíte completa, tag e release: **não executados** — são do gate pós-merge, não desta preparação.

## O que bloqueia a certificação

Os 7 itens de [EXTERNAL_APPROVALS.md](EXTERNAL_APPROVALS.md): nenhuma qualidade
Q01–Q36 ratificada, parâmetros TEMP em INTENÇÃO, jurídico por mercado ausente,
IREV ausente, revisão humana en-US pendente, credenciais de provider como
pré-requisito operacional. Sem essas aprovações, o gate final deve **parar
antes de matriz integral/tag** — e nenhuma inferência do agente conta como
aprovação.

## Próximo gate (não próxima microtarefa de código)

Após o merge de P59: reutilizar P45/`RELEASE_PLAYBOOK` no **novo SHA de main**,
não em SHA anterior. Certificados antigos seguem ausentes/invalidáveis por SHA;
nenhum deploy ou tag automático.
