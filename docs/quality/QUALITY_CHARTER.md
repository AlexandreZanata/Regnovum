# Quality Charter — Regnovum

**Status:** obrigatório para o programa de qualidade autônoma (P21–P30)
**Escopo:** qualidade e evidências; este charter não altera regras de produto.

## 1. Missão e independência

O setor de qualidade existe para transformar as regras aprovadas do produto em verificações reproduzíveis que possam bloquear uma mudança insegura ou incorreta antes da publicação. Ele verifica o comportamento e as evidências entregues; não presume que uma implementação está correta porque compila, tem cobertura, foi revisada ou foi produzida por uma pessoa ou por IA.

As regras e os gates de qualidade são independentes do código de produção que avaliam. Uma mudança de produto não pode alterar, desabilitar ou enfraquecer a regra que a avalia no mesmo passo sem revisão explícita do próprio escopo de qualidade e suas evidências. Ferramentas de qualidade devem falhar de forma observável diante de configuração ausente, entrada inválida, evidência incompleta ou resultado inconclusivo.

Código gerado por IA não recebe exceção, confiança implícita ou critério de aceitação diferente. Todo código é julgado pelas mesmas fontes normativas e pelos mesmos gates. A autoria não é evidência.

## 2. Fontes normativas e precedência

A qualidade verifica a implementação contra as fontes versionadas do produto:

1. [Constituição](../CONSTITUTION.md), decisões de negócio e [requisitos](../REQUIREMENTS.md);
2. invariantes de domínio, [modelo de ameaças](../THREAT_MODEL.md), requisitos de [segurança](../SECURITY.md), [privacidade](../PRIVACY.md) e [monetização](../MONETIZATION.md);
3. contratos públicos, eventos e políticas de apresentação;
4. catálogo executável de qualidade, quando entregue;
5. implementação.

A lista de fontes e a hierarquia correspondem ao [MVP](../MVP.md) e ao programa local de qualidade (`.local/QUALITY_PROGRAM.md`). Uma fonte de menor precedência não pode revogar silenciosamente uma de maior precedência.

Se a implementação divergir de uma regra aprovada, a implementação perde: o gate deve apontar a divergência e bloquear a mudança. Se fontes normativas de precedência igual ou a própria documentação normativa divergirem, a ferramenta e a pessoa executora param, registram o conflito com as evidências disponíveis e pedem decisão à autoridade de produto apropriada. Não se resolve conflito escolhendo a interpretação mais fácil, ajustando o teste ao código ou criando exceção implícita.

## 3. Classes de risco e evidência proporcional

Cada regra de qualidade declara uma classe de risco. As classes são as do programa local de qualidade:

- **Q0 — crítico:** autenticação e autorização; wallet e ledger; cobrança e webhooks; entitlements; privacidade; moderação administrativa; auditabilidade; migrations; backup e restore.
- **Q1 — alto:** Arenas; posições; argumentos; persuasão; jobs; email; cache privado; internacionalização de respostas públicas.
- **Q2 — normal:** leitura pública; projeções reconstruíveis; observabilidade não bloqueante; ferramentas internas sem privilégio.

A classificação dirige a evidência exigida, mas não dispensa verificações: Q0 requer cenários positivos, negativos, de limite, autorização, idempotência, concorrência e falha parcial quando aplicáveis; Q1 requer positivo, negativo, limite e integração; Q2 requer unidade ou contrato e regressão pertinente. Os floors quantitativos e as camadas adicionais permanecem os definidos em `.local/QUALITY_PROGRAM.md`; este charter não cria limites alternativos.

Uma evidência deve demonstrar o efeito observável da regra. Cobertura de linhas, nome de teste, snapshot isolado ou comentário não substituem cenário que falha quando a regra é violada.

## 4. Autoridade para bloquear

Os gates obrigatórios têm autoridade para bloquear merge, release ou certificação quando:

- regra aplicável é violada ou sua evidência está ausente, inválida ou inconclusiva;
- teste, scanner, compilador ou ferramenta requerida falha;
- uma ferramenta não pode verificar uma precondição de forma confiável;
- há conflito entre fontes normativas sem decisão registrada;
- uma proposta tenta ignorar, desligar, reduzir ou contornar um gate obrigatório.

Nenhum autor, revisor, operador ou ferramenta pode ignorar um gate obrigatório, transformar erro em sucesso, aceitar snapshot automaticamente, reduzir threshold para obter verde ou introduzir retry que esconda falha. A única exceção admissível é uma política de waiver explícita, versionada e sujeita ao gate próprio definido em tarefa posterior; um waiver nunca apaga a falha nem substitui sua evidência compensatória. Waivers em categorias proibidas pelo programa de qualidade continuam proibidos.

A autoridade de qualidade bloqueia a transição técnica; ela não toma decisões comerciais, jurídicas ou editoriais reservadas ao titular. Uma decisão humana ausente continua bloqueando a atividade correspondente e deve ser nomeada, nunca inventada.

## 5. O que é defeito

É defeito qualquer diferença entre comportamento entregue e fonte normativa aplicável, incluindo: regra de negócio ou invariante violada; autorização ou isolamento incorreto; perda, mistura ou exposição indevida de dados; efeito financeiro não atômico ou não idempotente; comportamento inseguro; contrato ou localização divergente; falha de confiabilidade, operação ou recuperação fora do budget aprovado; teste que não detecta a violação que afirma cobrir; gate que produz falso sucesso; e evidência ausente, desatualizada, não reproduzível ou sem vínculo verificável com o resultado.

Uma vulnerabilidade, resultado inconclusivo e flake também são falhas de gate, ainda que a causa não esteja identificada. A causa e o escopo podem ser investigados; não se altera a classificação ou o limiar para apagar o resultado.

## 6. Decisão, correção e trilha

Quando um gate falha, preserve a saída e o contexto suficientes para reproduzir a falha, identifique a regra e o artefato afetados e interrompa a publicação conforme o fluxo do repositório. Correção de produto, regra ou infraestrutura entra em escopo explícito e passa por novas evidências; um resultado anterior não é reescrito como sucesso. Mudanças de política seguem o processo de decisão versionado, e mudanças de gates exigem prova de que o próprio gate continua detectando a violação relevante.

Este charter é a política de entrada da fase 21. A implementação futura do catálogo, taxonomia, waivers e inventário deve manter os princípios acima e a hierarquia do programa de qualidade; ela não está autorizada a antecipar ou alterar o escopo desta microtarefa.
