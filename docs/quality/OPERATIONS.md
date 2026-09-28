# Operação contínua do setor de qualidade

**Status:** protocolo obrigatório do setor autônomo (P30-T07)

**Documentos de referência:** [QUALITY_CHARTER.md](QUALITY_CHARTER.md) · [EVIDENCE_TAXONOMY.md](EVIDENCE_TAXONOMY.md) · [RUNBOOKS.md](../RUNBOOKS.md) · [CI.md](../CI.md) · [catalog.json](../../quality/catalog.json) · [tiers.json](../../quality/tiers.json)

---

## 1. Intake de finding

Todo achado entra pelo mesmo funil, venha de humano, de IA, de gate ou de
incidente: uma issue com severidade, área do produto, evidência observável e
reprodução mínima. Achado sem evidência reproduzível volta ao intake, não
avança para correção. IA pode executar o intake inteiro — triar, reproduzir,
classificar — porque o funil julga o registro, nunca o autor; a autoria não
é evidência ([QUALITY_CHARTER.md](QUALITY_CHARTER.md) §1).

Campos obrigatórios do registro: severidade (§2), dono (§3), prazo de SLA
(§4), teste que falha antes e passa depois (§5) e, quando aplicável, waiver
com expiração (§6). Um campo ausente é o intake incompleto, não uma decisão.

## 2. Severidade

A severidade usa o mesmo vocabulário das regras e dos runbooks, sem segunda
tabela paralela:

| Severidade | Classe | Exemplos | Resposta |
|---|---|---|---|
| `critical` | Q0 | auth bypass, double spend, segredo vazado, dado perdido | P1 ([RUNBOOKS.md](../RUNBOOKS.md) §2): mitigar agora, regressão na mesma mudança |
| `high` | Q1 | flake em suite Q1, webhook com retry sem limite, regressão de payload | P2: mitigar no mesmo dia |
| `normal` | Q2 | projeção reconstruível divergente, ferramenta interna sem privilégio | P3: corrigir na semana |
| `flake` | transversal | teste que passa e falha sem mudança no código | quarentena com prazo; quarentena sem prazo é violação (§4) |
| `performance` | transversal | mediana acima do budget, teto de bytes estourado | reverter ou ADR na mesma janela; threshold nunca é afrouxado para ficar verde |

`flake` e `performance` cortam as classes: um flake em regra Q0 tem SLA de
Q0, e um budget estourado bloqueia como a classe da regra que ele mede.

## 3. Ownership

Cada achado tem um dono, e dono é papel, nunca pessoa: o mantenedor do
módulo ou da ferramenta que o gate avalia. O dono responde pelo prazo,
pela regressão e pelo waiver; o setor de qualidade responde pela regra,
pelo gate e pelo catálogo. Quando o dono do módulo e o dono da regra
discordarem sobre o que está certo, vale a precedência das fontes
([QUALITY_CHARTER.md](QUALITY_CHARTER.md) §2): a implementação perde para
a regra aprovada, e o conflito sobe para a autoridade de produto com as
evidências dos dois lados. Nomes e endereços pessoais nunca entram no
registro — o campo `owner` dos waivers carrega papel ou equipe.

## 4. SLA e expiração

| O que expira | Prazo máximo | Quando vence |
|---|---|---|
| Resposta critical / high | imediata / mesmo dia | §2 |
| Quarentena de flake | prazo registrado na entrada (`quality/flake-quarantine.json`) | a entrada sem prazo é recusada pelo portão |
| Waiver | data `expires` do registro (`quality/waivers.json`) | o dia passa e a exceção deixa de valer |
| Threshold inicial | hipótese com data ([RUNBOOKS.md](../RUNBOOKS.md) §5) | tráfego real ou incidente motiva a revisão |

Vencimento não é renovação automática: waiver vencido volta a bloquear, e
renovar é um novo registro com nova justificativa, nunca uma edição de
data. O decisor de certificação (`tools/qualitydecide`) recusa waiver
vencido sem ler a justificativa.

## 5. Regressão obrigatória

Toda correção exige o teste que falha antes e passa depois, na mesma
mudança: o diff que corrige sem o diff que prova é revisão incompleta. A
prova mora no pacote da regra violada, não em comentário nem em log de
execução. Para os gates, a regressão é a fixture que recusa
(`tools/*/testdata`, `internal/contract/gate_failclosed_test.go`): se um
defeito artificial passar, o metateste falha primeiro.

## 6. Waiver

Waiver é exceção versionada, não perdão: campos `id`, `finding`, `risk`,
`categories`, `rules`, `owner`, `justification`, `compensation`,
`created`, `expires`, julgados por `tools/qualitywaivers` contra o
catálogo e contra a árvore. As proibições valem como escritas: waiver
crítico nas oito áreas (authorization, auditability, data-loss, ink,
money, privacy, security, webhook) não permite certificação, qualquer que
seja a compensação; waiver mais brando que a regra que cobre é recusado
porque a classe é lida do catálogo. Registro vazio é o estado saudável.

## 7. Atualização de catálogo

O catálogo (`quality/catalog.json`), o registro de evidências
(`quality/evidence.json`) e a matriz (`tiers.json` em
[tiers.json](../../quality/tiers.json)) mudam somente por tarefa própria,
com o diff do par gerado (`make quality-inventory-write`) como evidência
da mudança de cobertura. Acrescentar regra sem evidência declarada, ou
evidência sem regra que ela prove, é o portão que recusa, não uma
divergência a reconciliar depois.

## 8. Incidente escapado

Defeito que alcançou `main` ou produção vira incidente com o mesmo funil
do §1 mais duas exigências: a regressão entra na suite que deveria tê-lo
pegado (ou registra por que nenhuma suite poderia, com tarefa para
criá-la), e o postmortem responde o que o gate não viu. Incidente sem
regressão reabre; postmortem sem dono não fecha. Exercícios de restauração
e desastre seguem [DISASTER_DRILL.md](../DISASTER_DRILL.md).

## 9. Ratchet

Baselines e pisos só se movem em uma direção: o primeiro baseline não
reduz os mínimos do programa, e os seguintes permanecem ou apertam. Afrouxar
threshold, piso, budget ou cobertura exige ADR e bloqueia a certificação
automática até decisão — nunca é ajuste de texto na mesma mudança que
está vermelha. É por isso que os portões leem o pin do documento e não o
argumento de quem executa.

## 10. IA executa, política não se autoemenda

IA pode executar intake, correção, regressão, waiver redigido e evidência:
tudo que o funil julga pelo registro. O que IA — ou pessoa — não pode é
alterar a política na mesma mudança que tenta passar: regra, gate,
threshold, catálogo, waiver e este protocolo viajam em diff isolado, com
tarefa própria e evidência própria. Uma mudança de produto que toca
`quality/**`, `docs/quality/**` ou `tools/*audit*/` junto da funcionalidade
volta para separação antes de qualquer julgamento de mérito. É a
independência do setor, aplicada ao diff ([QUALITY_CHARTER.md](QUALITY_CHARTER.md) §1).

## 11. Workflows de exemplo

### Critical: bypass de autorização

Severidade `critical`, dono do módulo de identidade. Intake com prova de
conceito mínima → mitigação reversível agora → regressão na matriz de
autorização (célula que nega antes de permitir) → sem waiver possível
(`authorization` é área proibida) → `quality-main` verde → fecha com o
relato da causa. Se a mesma classe reaparecer, o postmortem pergunta o
que a matriz não cobria (§8).

### High: retry de webhook sem limite

Severidade `high`, dono do módulo de cobrança. Prova com o evento
resentido N vezes → teto de tentativas com backoff → regressão no teste
adversarial de webhook → `quality-nightly` do módulo → fecha. Waiver
somente com expiração e compensação monitorada; `money` e `webhook` nunca
em classe crítica (§6).

### Flake: suite Q1 intermitente

Severidade `flake`, dono do pacote. Reprodução com seed registrada
(`ARENA_TEST_SEED`) → quarentena com issue, dono e prazo em
`quality/flake-quarantine.json` → correção da causa-raiz (ordem do mecanismo, não
retry) → remoção da quarentena na mesma mudança que prova 30 execuções
limpas. Quarentena sem prazo ou com retry é recusada pelo portão.

### Performance: mediana acima do budget

Severidade `performance`, dono do módulo. Medição com o budget pinado
(`internal/performance`, SLOs em [SLO.md](../SLO.md)) → ou otimização com
a mesma mediana abaixo do teto, ou reversão da mudança que estourou, ou
ADR propondo novo teto com evidência — nunca edição do número para ficar
verde (§9). A regressão é o próprio budget mais o caso de recusa acima
do limite.
