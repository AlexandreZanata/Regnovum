# Pacote de revisão financeira independente (P44-T05)

**Status: PACOTE DE REVISÃO, NÃO PARECER.** Este documento prepara o
trabalho de especialistas **independentes** da implementação em segurança
financeira/contábil e contratos por jurisdição. Nenhum parecer foi emitido,
nada aqui está certificado, e a própria IA implementadora **não assina**
aprovação alguma — ela apenas organiza o material revisável. O parecer
final é gate da P45. Ler este arquivo como aprovação é ler errado de
propósito.

**Produto desativado.** A economia segue desligada até o gate P44; as
regras Q19–Q31 estão `PENDENTE` em
[DECISOES_VIGENTES.md](../reino/DECISOES_VIGENTES.md), e proposta não
ratificada não vira regra por inferência. Os revisores avaliam controles
e provas, nunca uma operação ativa — porque ela não existe.

**Sem dados sigilosos.** O pacote contém só caminhos do repositório,
fixtures sintéticas e canários `.invalid`. Nenhum segredo, credencial,
dado real ou PII viaja com ele.

---

## 1. Objetivo e não-objetivos

O parecer independente deve responder, com evidência do repositório: os
controles financeiros provam oferta fixa por temporada, custódia exclusiva,
idempotência e negação fail-closed nas fronteiras monetárias? E quais
contratos por jurisdição faltam para qualquer ativação futura?

Não é objetivo deste pacote: emitir o parecer, aprovar parâmetros
pendentes (preço, Dízimo, Migalhas, jurisdições), autorizar ativação,
substituir a matriz DAST completa (P45) nem reexecutar a suíte integral
(P45).

## 2. Escopo da revisão

| # | Eixo | Regras | Estado no mapa |
|---|---|---|---|
| 1 | Oferta por temporada (S fixo, Σ=S) | Q19–Q20, TEMP-01/02 | coberto em amostra (T02/T03) |
| 2 | Custódia e partições exclusivas | Q23 | planejado |
| 3 | Idempotência e replay | Q21 | coberto em amostra (T02–T04) |
| 4 | Direitos legados | Q31 | bloqueado (aguarda P31) |
| 5 | Tempo, corte e fence | Q26–Q27 | planejado |
| 6 | Preço e cotação (oracle) | Q22 | planejado |
| 7 | Dízimo | Q24 | planejado (P37) |
| 8 | Migalhas e antifraude | Q25 | planejado (P38) |
| 9 | Decreto e autoridade sazonal | Q11/Q32 | coberto em amostra (T04 + P40/P47) |
| 10 | Privacidade e exportação | Q34–Q35 | coberto em amostra (T04 + P42) |
| 11 | Produtos desativados (Títulos, apostas) | — | diferido por decisão (P43) |

Fonte do mapa: [financial-coverage.json](../../quality/financial-coverage.json)
(11 eixos; 4 cobertos em amostra, 5 planejados, 1 bloqueado, 1 diferido).
Ameaças críticas/altas: [THREAT_MODEL.md](../THREAT_MODEL.md) §5
(THR-ECON-20/21/23/22/30/11/25/24/28/32/34/35). Temporadas:
[TEMPORADAS_SUCESSAO.md](../reino/TEMPORADAS_SUCESSAO.md) (TEMP-01–04
intenção aprovada; parâmetros operacionais pendentes).

## 3. Inventário de evidências

| Artefato | Caminho | O que prova |
|---|---|---|
| Catálogo Q0 de invariantes | [financial-coverage.json](../../quality/financial-coverage.json) + [financial_coverage_test.go](../../internal/contract/financial_coverage_test.go) | 100% de Q/TEMP/ameaças com teste, owner e runbook; nada certificado |
| Oracle diferencial + 16 mutantes | [seasonal_oracle_test.go](../../internal/economy/adapters/postgres/seasonal_oracle_test.go) | lockstep de 2 livros, zero mutante sobrevivente (mint/burn/double/cross/carry-over/dup-genesis/cutoff + 9 de regra) |
| Crash/concorrência, 8 casos | [settlement_crash_test.go](../../internal/economy/adapters/postgres/settlement_crash_test.go) | Σ=S após corrida, crash, lock-abort, restart, bounce, 2 closers, fora-de-ordem, ex-Rei recusado |
| Red team, RED-01–10 | [monetary_trust_audit_test.go](../../internal/contract/monetary_trust_audit_test.go) | amarração webhook→dona, forjado sem conceder, replay único, negação anônima, amostra DAST, deny-list sem mint/escrow |
| Ameaças e controles | [THREAT_MODEL.md](../THREAT_MODEL.md) §5 | controle preventivo, detector, teste de quebra e runbook por ameaça |
| Resposta a incidentes | [RUNBOOKS.md](../RUNBOOKS.md) R1–R9 | owner, alerta e ação por classe de desvio |
| Decisões pendentes | [DECISOES_VIGENTES.md](../reino/DECISOES_VIGENTES.md) + [QUESTOES_ABERTAS.md](../reino/QUESTOES_ABERTAS.md) | Q19–Q31 pendentes; nada ratificado por este pacote |
| Rastreabilidade | [season-trace.json](../../quality/season-trace.json) + [reino-trace.json](../../quality/reino-trace.json) | TEMP/Q ↔ requisitos ↔ ameaças |

Como reproduzir (só leitura e teste, sem ativar nada): `go test`
do pacote citado com `-count=1` (e `-race` onde houver concorrência);
banco descartável via `dbtest`, seeds por `ARENA_TEST_SEED`.

## 4. Perguntas aos revisores

Segurança financeira/contábil:

1. A invariante Σ=S por `season_id` está completa contra carry-over
   silencioso entre temporadas? Falta algum caminho de escrita no ledger
   fora dos adapters auditados?
2. A exclusividade de custódia (Tesouro livre, reserva, escrow,
   obrigações) impede contagem dupla em todos os estados, inclusive
   erro parcial e replay fora de ordem?
3. A idempotência por intenção de negócio cobre replays cruzados
   (mesma chave, contas distintas) sem vazar existência de intent alheio?
4. O fence de reinado + backlog econômico bloqueia ato real de ex-Rei e
   de delegação stale em todos os relógios (tick antes/no/depois do
   corte)?
5. A amarração webhook→intent→conta resiste a evento forjado, reordenado
   e duplicado sem ajuste silencioso nem concessão a inocente?
6. A amostra DAST e o registro RED deixam algum vetor crítico/alto sem
   prova de bloqueio? Qual?

Contratos por jurisdição:

7. Quais jurisdições exigem aprovação prévia para INK circulante, P2P,
   Dízimo e Patentes, e que termos de consumidor cada uma requer?
8. Que tratamento tributário se aplica a cada fluxo, e onde o registro
   fail-closed deve travar sem parecer (gate P44-T10)?
9. Direitos legados (Q31, bloqueado): que inventário, equivalência e
   rollback o opt-in precisa demonstrar antes de qualquer migração?
10. Títulos e apostas seguem não ofertados: o bloqueio atual é suficiente
    como evidência negativa, ou falta prova adicional?

## 5. Formato de findings

Cada finding do parecer usa uma linha por achado, sem segredo ou PII:

| Campo | Conteúdo |
|---|---|
| ID | `IREV-NN` sequencial do parecer |
| Eixo | um dos 11 do §2 |
| Severidade | critical / high / medium / low (critério do THREAT_MODEL §1) |
| Achado | o que foi verificado e onde (caminho + teste ou ausência) |
| Recomendação | correção ou prova adicional, com owner sugerido por papel |
| Estado | open / accepted-risk (só medium ou abaixo, com aceite explícito) |

Piso inegociável: **nenhum finding critical/high aberto** na emissão do
parecer final (P45). Risco aceito exige owner e aceite explícito, nunca
silêncio.

## 6. Regras de independência

- Revisores sem autoria no código ou nos testes revisados; a IA
  implementadora organiza o pacote e **não aprova** o próprio trabalho.
- O parecer cita evidência reproduzível (comando + seed + commit), não
  opinião; ausência de parecer é pendência explícita, não aprovação.
- Todo material trafega sem segredo, credencial, dado real ou PII; se um
  anexo exigir sigilo (contrato de jurisdição), ele é referenciado pelo
  nome e guardado fora do repositório.

## 7. Pendências explícitas (não confundir com aprovação)

- Parecer final: **ausente** — gate da P45, com revisores independentes.
- Q19–Q31: **pendentes** de ratificação (P31); parâmetros de preço,
  Dízimo, Migalhas e jurisdições não existem como regra.
- Matriz DAST completa, carga integral, reconciliação contínua (T06),
  restore/PITR (T07) e decisor `economy-certify` (T12): **planejados**
  nesta fase, executados nas tarefas próprias.
- Este pacote prepara a revisão; a certificação financeira, se houver,
  acontece **depois da P45**, com gates técnicos, legais e operacionais
  cumpridos e autorização separada do titular.
