# Goyim Arena — comandos canônicos de validação (P01-T03).
#
# Regra do plano: um gate ainda não implementado falha com mensagem
# explícita e nunca retorna sucesso falso. `make verify` passa apenas
# para as capacidades já existentes e lista claramente as pendentes.

GO ?= go
GOFMT ?= gofmt
NPM ?= npm
K6 ?= k6
GOVULNCHECK ?= govulncheck
SQLC ?= $(shell which sqlc 2>/dev/null || echo "$(shell $(GO) env GOPATH)/bin/sqlc")
ASSETGEN := $(GO) run ./cmd/assetgen

# Analisador estático fixado (P23-T02, ADR-016): `make lint` roda as análises do
# `go vet` e o staticcheck na versão abaixo, e o alvo recusa a árvore quando a
# versão instalada diverge do pino. A toolchain é lida do go.mod — um pino
# escrito duas vezes é um pino que deriva — porque o analisador lê os dados de
# exportação da biblioteca padrão que analisa: um staticcheck construído com Go
# mais antigo recusa um módulo que declara um Go mais novo.
STATICCHECK_MODULE := honnef.co/go/tools/cmd/staticcheck
STATICCHECK_VERSION := v0.8.1
STATICCHECK_TOOLCHAIN := go$(shell sed -n 's/^go //p' go.mod | head -1)

# Ferramenta de mutação (P24-T10): o gremlins mede a força dos testes dos
# pacotes domain/application Q0/Q1, e o portão `tools/mutationaudit` cobra os
# limiares do registro `quality/mutations.json`. Os pinos vivem aqui e no
# registro; o portão recusa divergência entre os dois. `go run módulo@versão`
# resolve e constrói exatamente a versão fixada (ou recusa rodar), então não
# há binário instalado para derivar — ao contrário do analisador, que precisa
# do GOTOOLCHAIN da árvore para ler a linguagem certa.
GREMLINS_MODULE := github.com/go-gremlins/gremlins/cmd/gremlins
GREMLINS_VERSION := v0.6.0
GREMLINS_TIMEOUT_COEFFICIENT := 100
GREMLINS_WORKERS := 8

# Gerador de contratos TypeScript (P18-T02): lê o subconjunto versionado do
# OpenAPI e emite web/src/contracts/generated.ts (nunca editado à mão).
CONTRACTGEN := $(GO) run ./tools/contractgen

# Ratchet de capacidade backend (P28-T08): rejulga thresholds k6, budgets Go,
# tetos de bytes e limites de produto contra quality/capacity-baseline.json —
# drift de capacidade falha o build.
CAPACITYRATCHET := $(GO) run ./tools/capacityratchet

# Imagem de produção (P19-T01): receita em Dockerfile, auditoria do artefato em
# tools/imageaudit. IMAGE é a tag que o build usa e que o scan examina.
IMAGE ?= goyim-arena:local
TRIVY ?= trivy

.PHONY: fmt fmt-check lint audit-complexity audit-deadcode audit-errors audit-provenance audit-tests audit-diff audit-deps audit-mutations audit-coverage test-unit test-integration test-race test-migration test-security dast flake-detect regression-pack test-web typecheck build-web audit-web audit-i18n i18n-audit audit-ci quality-catalog quality-waivers quality-taxonomy quality-inventory quality-inventory-write testenv-verify release-gate security-audit privacy-audit release-verify handoff-check handoff-walkthrough test-contract test-e2e test-load-smoke image-build image-verify image-scan caddy-verify compose-verify migration-audit backup-verify deploy-verify vuln generate generate-check verify quick-verify economy-decisions-check economy-certify release-matrix frontend-coverage

# Gerador i18n (P02-T07): fontes em locales/, artefatos versionados em
# web/src/i18n/generated.ts e internal/i18n/generated.go (nunca editados).
I18NGEN := $(GO) run ./cmd/i18ngen

# fmt formata o código Go (alvo mutante; use fmt-check para validar sem alterar).
fmt:
	$(GOFMT) -w .

# fmt-check valida a formatação do Go sem alterar nenhum arquivo.
fmt-check:
	@files="$$($(GOFMT) -l .)"; \
	if [ -n "$$files" ]; then \
		echo "fmt-check: FALHOU — arquivos fora do formato (execute 'make fmt'):" >&2; \
		echo "$$files" >&2; \
		exit 1; \
	fi; \
	echo "fmt-check: ok"

# Gate curto de integração. Testes de comportamento direcionados permanecem
# obrigatórios na microtarefa local; estes packages críticos dão um piso real
# ao PR sem banco, browser ou Docker. A suíte integral é gate de versão.
#
# Os oito gates baratos da fase 23 rodam aqui **e** em `verify`: `lint`
# (P23-T02), `audit-complexity` (P23-T03), `audit-deadcode` (P23-T04),
# `audit-errors` (P23-T05), `audit-provenance` (P23-T06), `audit-tests`
# (P23-T07), `audit-diff` (P23-T08) e `audit-deps` (P23-T09) são biblioteca
# padrão mais o analisador fixado — não precisam de banco, browser nem Docker —,
# e o critério de saída da fase pede que código estruturalmente ruim, duplicado,
# morto, sem tratamento de erro, sem procedência, provado por um teste que não
# prova nada, mudado sem a evidência da classe ou montado com dependência que
# ninguém aprovou seja recusado **antes** dos testes caros — o que só acontece no
# caminho que roda em cada PR.
quick-verify: fmt-check lint audit-complexity audit-deadcode audit-errors audit-provenance audit-tests audit-diff audit-deps
	$(GO) test -run '^$$' ./...
	$(GO) test ./internal/wallet/domain/... ./internal/identity/domain/... ./internal/arguments/domain/... ./internal/arenas/domain/... ./tools/ciaudit/...
	$(NPM) --prefix web run typecheck
	@echo "quick-verify: ok"

# Setor autônomo de qualidade (P30-T01): os cinco comandos canônicos,
# compostos somente por gates reais já existentes. O conteúdo e a cadência
# de cada camada vivem em quality/tiers.json, e o teste
# internal/contract/quality_tiers_test.go prova que os targets espelham o
# documento. Camadas não habilitadas só executam integralmente na P45; uma
# execução rápida nunca substitui a certificação. Nenhum target engole
# falha: só pré-requisitos e receitas que propagam exit não zero.
quality-fast: fmt-check
	$(GO) build ./...
	@echo "quality-fast: ok"
quality-main: quick-verify
	@echo "quality-main: ok"
quality-nightly: test-unit test-integration test-contract test-security
	@echo "quality-nightly: ok"
quality-weekly: quality-nightly test-race test-migration test-web audit-mutations audit-coverage
	@echo "quality-weekly: ok"
quality-certify: quality-weekly verify
	@echo "quality-certify: ok"

# test-unit executa os testes unitários das capacidades existentes (Go).
# O frontend ainda não possui runner de testes; será agregado quando existir.
#
# A segunda invocação é o build com a tag pseudolocale (P18-T10): é o catálogo
# derivado que as jornadas de navegador dirigem, e ele tem testes próprios
# (registro no allowlist, formatação de placeholders, catálogos reais
# intactos). Sem ela, a tag só seria compilada dentro do harness — e um erro no
# registro apareceria como jornada vermelha em vez de teste vermelho.
test-unit:
	$(GO) test ./...
	$(GO) test -tags pseudolocale ./internal/i18n/...
	@echo "test-unit: ok"

# test-integration executa os testes de integração contra PostgreSQL real descartável (P03-T05, P03-T06).
test-integration:
	$(GO) test -v -race ./internal/platform/dbpool/... ./internal/platform/dbtest/... ./internal/platform/postgres/...
	@echo "test-integration: ok"

# test-race é o gate de concorrência **selecionado** (P19-T08; docs/THREAT_MODEL.md
# §7): o detector de corridas nos caminhos onde mais de um processo disputa a
# mesma linha — o ledger e a carteira (THR-WAL-01, THR-WAL-02), a publicação e a
# retirada de argumento, e o agendador da fila, cuja semântica é lease/claim.
# "Selecionado" é deliberado: `-race` na árvore inteira custa muito e não
# pergunta nada novo a pacotes sem concorrência. Exige um PostgreSQL alcançável
# em `ARENA_DATABASE_URL` (o harness cria bancos descartáveis ao lado dele),
# como test-integration.
test-race:
	$(GO) test -race -count=1 ./internal/wallet/... ./internal/arguments/... ./internal/jobs/application/...
	@echo "test-race: ok"

# test-isolation é o gate de ciclo de vida dos testes (P22-T06): o guarda de
# cada teste recusa o que sobrevive a ele, uma fixture que vaza de propósito
# prova que o detector morde, a auditoria de resíduos mede o que uma execução
# deixa na máquina (banco descartável, conexão e diretório temporário) e a suíte
# roda com ordem embaralhada e paralelismo alto, imprimindo a seed para que uma
# execução vermelha possa ser repetida. Exige um PostgreSQL alcançável, como
# `test-unit` e `test-integration`, e roda a suíte inteira: por isso é alvo
# próprio, fora de `make verify` (docs/CI.md).
test-isolation:
	bash tools/isolationaudit/verify.sh
	@echo "test-isolation: ok"

# test-offline é o gate de execução offline e reprodutível (P22-T08): o
# manifesto de ferramentas, imagens e lockfiles é função da árvore (duas
# execuções respondem os mesmos bytes), o preload instala dos lockfiles
# aprovados e prova com egress negado que os caches respondem a eles, um
# processo que tenta alcançar a internet é recusado por regra nomeando o host e
# nada arquiva, e `make test-unit` e `make test-integration` rodam offline,
# verdes, arquivando manifesto e evidência parseável. Exige um PostgreSQL
# alcançável, como `test-unit`, e Node com os dois lockfiles: por isso é alvo
# próprio, fora de `make verify` (docs/CI.md).
test-offline:
	bash tools/offlineaudit/verify.sh
	@echo "test-offline: ok"

# test-migration é o nome que o CI dá às migrations (P19-T08): o runner (fontes
# ordenadas, tabela de versão, nenhum caminho de volta) e o harness que aplica
# as migrations embutidas a um banco virgem descartável. Ele não substitui a
# prova profunda — as 32 migrations aplicadas pela própria imagem, num cluster
# vazio, são `image-verify` e `compose-verify` —, e existe para que o gate tenha
# um nome no Makefile e no workflow em vez de ficar implícito dentro de outro.
test-migration:
	$(GO) test -count=1 ./internal/platform/dbmigrate/... ./internal/platform/dbtest/...
	@echo "test-migration: ok"

# test-web compila o frontend e seus testes com o tsc oficial (strict) e os
# executa no runner nativo do Node. Nenhuma dependência nova: o runtime
# entregue ao browser continua sem terceiros, e o harness vive fora de web/src.
test-web:
	$(NPM) ci --prefix web
	$(NPM) --prefix web run test
	@echo "test-web: ok"

# typecheck roda a checagem estrita de tipos do frontend (tsc --noEmit).
# npm ci garante instalação reprodutível a partir do package-lock.json.
typecheck:
	$(NPM) ci --prefix web
	$(NPM) --prefix web run typecheck
	@echo "typecheck: ok"

# build-web compila o frontend para ESM nativo em web/generated/ (ignorado).
build-web:
	$(NPM) ci --prefix web
	$(NPM) --prefix web run build
	@rm -rf web/dist
	$(ASSETGEN) -input web/generated -input web/src -output web/dist -manifest web/dist/manifest.json
	@echo "build-web: ok"

# audit-web mede o build entregue contra os orçamentos e as regras de
# dependência do frontend (P18-T08, docs/FRONTEND.md §11): custo comprimido por
# página pública e do CSS inicial, imports externos, bare specifiers, imports
# não publicados, construtos que a CSP servida recusa e primitivas de rede fora
# de web/src/core. Ele depende de build-web porque mede o build que as páginas
# referenciam, nunca as fontes: a árvore que compila e o build que é servido são
# dois artefatos diferentes.
audit-web: build-web
	$(GO) run ./tools/webaudit -build web/dist
	@echo "audit-web: ok"

# audit-i18n varre a árvore entregue em busca do que o catálogo não pode
# garantir sozinho (P18-T10): documento que escreve a própria linguagem (ou
# nenhuma), documento que traz prosa fora do catálogo e folha de estilo com
# propriedade física. Não depende de build porque lê o código-fonte entregue:
# é a árvore que vai para produção, e um layout é uma propriedade dela, não do
# artefato compilado. O scanner está fora do pacote entregue e nunca escreve
# nada; a lista de regras e o porquê de cada uma estão em tools/i18naudit.
audit-i18n:
	$(GO) run ./tools/i18naudit -root .
	@echo "audit-i18n: ok"

# i18n-audit é a auditoria final de internacionalização (P20-T09): executa as
# áreas que a fase nomeia — cobertura de catálogo, pseudo-locale, snapshots,
# emails, Problem Details, SEO, moeda, plural, timezone, cache e texto
# hardcoded — e depois julga o registro versionado (`docs/I18N_AUDIT.md`: prosa
# para o leitor, bloco de máquina para a ferramenta) contra a árvore medida
# agora. O registro não é reescrito por ele: as afirmações são do autor, e o que
# o portão faz é recusar cada uma que a árvore contradiz. As jornadas de
# navegador são a única área fora dele, porque exigem Chromium e PostgreSQL
# descartável: o CI as roda em `make test-e2e` e o registro guarda a execução.
i18n-audit:
	tools/i18nrelease/verify.sh
	@echo "i18n-audit: ok"

# audit-ci valida os workflows entregues (P19-T08): cada gate que a fase exige
# tem de estar ligado ao CI, toda ação de terceiro fixada por SHA, as permissões
# mínimas, nenhum passo pode mascarar a própria falha, todo alvo invocado tem de
# existir no Makefile, todo job que precisa de banco declara o serviço, e o CI
# completo pula PR em rascunho **sem** reduzir o gate final. Ele lê os arquivos
# como eles são, roda dentro de `make verify` e é o que impede o workflow de
# divergir do Makefile que ele invoca.
audit-ci:
	$(GO) run ./tools/ciaudit -root .
	@echo "audit-ci: ok"

# audit-req é o portão da rastreabilidade dos requisitos (P20-T02): lê
# docs/REQUIREMENTS.md e resolve cada referência que ela faz — a rota contra o
# contrato servido, o caso de uso e a migration contra os arquivos que existem,
# o teste contra a função que está de fato no arquivo — além das duas direções
# da tabela de cobertura, das dez invariantes de BR §11, dos itens de
# docs/MVP.md e da ausência de vocabulário de adiamento nas células. Ele entra em
# `verify` porque é gate de merge: matriz sem teste automatizado é requisito não
# rastreado, e o desenho da cobertura não pode envelhecer em silêncio. Ele nunca
# escreve: a correção pertence a quem mudou o código ou o documento.
audit-req:
	$(GO) run ./tools/reqaudit -root .
	@echo "audit-req: ok"

# quality-catalog é o portão do catálogo de regras de negócio (P21-T02 e
# P21-T03): lê quality/catalog.json e julga cada afirmação que ele faz contra o
# checkout — o documento que a regra cita, os pacotes que ela nomeia, os testes
# que ela diz existirem, as declarações que a classe de risco exige — e então faz
# a pergunta que um catálogo completo também tem de responder: **este é o
# conjunto inteiro?** As entradas são comparadas com docs/REQUIREMENTS.md e
# docs/THREAT_MODEL.md como eles são, nos dois sentidos: requisito sem entrada e
# entrada sem regra são recusados. A severidade é lida pelo valor declarado e não
# pela posição da coluna, porque o modelo de ameaças tem uma linha sem a célula
# de ator. Ele entra em `verify` porque é gate de merge: uma regra sem evidência
# exigida é uma regra que ninguém aplica. Ele nunca escreve.
quality-catalog:
	$(GO) run ./tools/qualitycatalog -root .
	@echo "quality-catalog: ok"

# quality-waivers é o portão da política de waivers (P21-T04): lê
# quality/waivers.json e julga cada exceção contra os registros do checkout — o
# catálogo, que diz quão séria é a regra suspensa (a classe é lida de lá, não da
# declaração, para que o waiver não possa amaciá-la); as auditorias, que dizem
# que o finding aceito existe; e a árvore, onde o teste compensatório tem de
# estar. Proíbe waiver crítico nas oito áreas da fase, recusa janela vencida ou
# invertida, dono que seja uma pessoa e compensação que não resolva, e **não**
# recusa o registro vazio: nada suspendido é o estado saudável — o registro
# exato oposto ao do catálogo, e de propósito. Confere ainda
# quality/waivers.schema.json contra o vocabulário do próprio carregador, porque
# um schema que promete menos que a ferramenta é um segundo contrato. Ele entra
# em `verify` porque é gate de merge: exceção que ninguém consegue conferir é
# regra suspensa em silêncio. Imprime identificadores e códigos, nunca a
# justificativa, o dono ou a compensação — o log do CI não é lugar do texto que
# o waiver existe para conter.
quality-waivers:
	$(GO) run ./tools/qualitywaivers -root .
	@echo "quality-waivers: ok"

# quality-taxonomy é o portão da taxonomia e identidade das evidências de teste
# (P21-T05): lê quality/evidence.json e classifica cada identidade — que tipo de
# teste é, em que suíte roda, em que ambiente, quais regras do catálogo prova e
# em que classe de risco — **por dado declarado**, nunca pelo nome da função Go.
# A classe é lida do catálogo e a declaração não pode amaciá-la; o tipo e o
# ambiente têm de combinar, porque chamar de unitário um teste que só existe
# contra um PostgreSQL é exatamente o rótulo trocado que a taxonomia existe para
# recusar; e o dono só é exigido da suíte que prova regra crítica. Recusa ainda
# identidade duplicada, tipo, ambiente ou regra que ninguém declarou, referência
# de teste que não resolve na árvore, e confere quality/evidence.schema.json
# contra o vocabulário do próprio carregador. Ele entra em `verify` porque é
# gate de merge: evidência que ninguém encontra é evidência que não guarda nada.
# Ele nunca escreve e nunca executa teste — o que ele produz é o julgamento que
# o inventário da P21-T06 vai ler. A documentação em prosa dos onze tipos, dos
# cinco ambientes e das convenções de identificador é
# docs/quality/EVIDENCE_TAXONOMY.md.
quality-taxonomy:
	$(GO) run ./tools/qualitytaxonomy -root .
	@echo "quality-taxonomy: ok"

# quality-inventory é o inventário de cobertura semântica (P21-T06): cruza o
# catálogo, o registro de evidências e a matriz de requisitos com as seis
# famílias de artefatos que a fase nomeia — as rotas que o contrato serve, as
# migrations do schema, os casos de uso dos módulos, os tipos de job, os
# subcomandos do binário e as referências de teste citadas — e gera
# quality/coverage.json (máquina) e docs/quality/COVERAGE.md (pessoas).
# A cobertura é contada **por regra, nunca por linha**: uma regra está coberta
# quando um teste que a prova, uma rota, um caso de uso ou uma migration que a
# carrega, ou uma identidade de evidência que a declara existe de verdade. Ele
# entra em `verify` porque é gate de merge, e falha quando uma regra não tem
# âncora alguma (Q0 inclusive: uma regra crítica que nada prova é uma regra que
# ninguém aplica), quando uma citação dos três documentos aponta para algo que
# não existe mais, e quando o relatório versionado diverge da árvore — a
# remoção de uma evidência Q0 não passa em silêncio: ela aparece no diff que
# alguém tem de commitar. Órfão é listado e **não** reprova: a lista é o
# achado, e transformá-la em portão exigiria uma política que a fase não deu.
# Ele nunca escreve nesta forma.
quality-inventory:
	$(GO) run ./tools/qualityinventory -root .
	@echo "quality-inventory: ok"

# quality-inventory-write regenera os dois arquivos do inventário. É o alvo que
# se roda depois de mudar o catálogo, o registro ou a matriz, e o diff do par é
# a evidência da mudança de cobertura — não um efeito colateral dela.
quality-inventory-write:
	$(GO) run ./tools/qualityinventory -root . -write
	@echo "quality-inventory-write: ok"

# release-gate é o portão das decisões humanas do lançamento (P20-T01): lê o
# registro versionado (docs/GOVERNANCE.md) e **falha** enquanto qualquer uma das
# sete decisões da fase estiver em aberto, nomeando o que falta decidir, quem
# deve decidir e o que a decisão impede. Ele não entra em `verify` de propósito:
# `verify` é o gate de integração de cada merge, e um merge não é um release —
# incluir aqui faria toda tarefa da fase ficar vermelha por uma decisão que não é
# dela. Ele é o que se roda antes de publicar, e é isso que a P20 verifica de
# ponta a ponta (`docs/RELEASE_CHECKLIST.md`).
#
# O modo `-check` julga o documento (esquema, as sete decisões presentes, prosa
# de acordo com o bloco) sem tratar bloqueio aberto como falha: é o modo dos
# testes da ferramenta, e nunca um modo de release.
release-gate:
	$(GO) run ./tools/governanceaudit -root .
	@echo "release-gate: ok"

# security-audit é o portão da auditoria de segurança (P20-T04): lê o registro
# versionado (docs/SECURITY_AUDIT.md), resolve cada evidência que ele cita,
# confere o registro contra o modelo de ameaças (mesmo conjunto, mesma
# severidade, nenhuma ameaça Crítica respondida só com monitoramento), recusa
# achado Crítico ou Alto em aberto e aceite sem dono e data, varre a árvore em
# busca de segredo estrutural e roda as doze execuções que a fase nomeia.
#
# Ele não entra em `verify` de propósito, pelo mesmo motivo do release-gate: as
# execuções incluem `make vuln` e suítes com -race, e um gate de merge não é um
# release. O modo `-check` julga o registro sem executar nada — é o modo dos
# testes da ferramenta.
security-audit:
	$(GO) run ./tools/secaudit -root .
	@echo "security-audit: ok"

# privacy-audit é o portão da auditoria de privacidade e moderação (P20-T06):
# lê o registro versionado (docs/PRIVACY_AUDIT.md), recusa conta sintética fora
# de domínio reservado, caminho citado que não existe, área da fase sem
# auditoria e achado Crítico ou Alto em aberto, e confere o documento *contra o
# código que ele descreve*, nos dois sentidos: as chaves JSON que cada superfície
# de exportação pode emitir contra a allowlist declarada, a tabela de retenção
# contra o cronograma que o job aplica, o limiar de baixa contagem contra o que
# os agregados usam e o vocabulário de analytics contra a allowlist do
# despachante. Depois roda as sete execuções que a fase nomeia.
#
# Ele não entra em `verify`, como o release-gate e o security-audit: o julgamento
# é uma revisão de release, e o modo `-check` (que os testes da ferramenta usam)
# julga o registro sem executar nada.
privacy-audit:
	$(GO) run ./tools/privacyaudit -root .
	@echo "privacy-audit: ok"

# release-verify é a verificação final reproduzível do backend (P20-T07): cria
# um checkout limpo do commit atual, instala as dependências **somente** pelos
# lockfiles, sobe um PostgreSQL 18.4 descartável, roda `make verify`, constrói a
# imagem e a exercita com o smoke — cada comando **duas vezes** — e confere a
# árvore, o `git fsck` e o portão de release. O que ele mediu vira o registro
# versionado `docs/RELEASE_CHECKLIST.md`, renderizado pelo próprio run (a prosa
# sai das medições, nunca da mão de quem escreve), e um vermelho aborta antes de
# escrever qualquer coisa: uma verificação que falha nunca deixa para trás um
# documento que se lê como aprovado.
#
# Ele não entra em `verify`: exige daemon Docker, um checkout limpo e roda o
# próprio `verify` duas vezes. É o que se roda antes de publicar.
release-verify:
	tools/releaseverify/verify.sh
	@echo "release-verify: ok"

# handoff-check é o portão do handoff local (P20-T08): lê o README e recusa
# toda afirmação que a árvore contradiz — comando que não existe no Makefile,
# variável que não está no `.env.example`, caminho que não está lá, subcomando
# que o binário não tem — mais as declarações que a caminhada precisa (os
# marcadores do bloco, os passos que ele executa e as superfícies que ele
# sonda). Ele entra em `verify` porque é gate de merge: renomear um alvo sem
# atualizar o README é exatamente o que ele existe para recusar.
handoff-check:
	$(GO) run ./tools/handoffaudit check -root .
	@echo "handoff-check: ok"

# handoff-walkthrough é a outra metade: segue os blocos que o próprio README
# declara num checkout limpo do commit (a ferramenta e o documento entram por
# sobreposição declarada, com digest), roda a jornada de comandos que a página
# manda colar, sobe o servidor como a página manda e sonda as superfícies que
# ela nomeia. Nada aqui inventa um comando: a sequência vem do documento, e é
# por isso que uma página que não funciona fica vermelha em vez de virar um
# parágrafo em que alguém acredita. Ele não entra em `verify`: exige daemon
# Docker, um checkout limpo e a porta que o documento usa livre.
handoff-walkthrough:
	$(GO) run ./tools/handoffaudit walkthrough -root .
	@echo "handoff-walkthrough: ok"

# image-build constrói a imagem de produção a partir do Dockerfile. As bases
# estão fixadas por digest, então o mesmo commit gera a mesma árvore.
image-build:
	docker build --file Dockerfile --tag $(IMAGE) .
	@echo "image-build: ok"

# migration-audit é o gate do ciclo de vida das migrations (P20-T03): exercita
# um PostgreSQL 18.4 descartável com o próprio runner — banco vazio, um degrau
# que aplica cada migration com um leitor ativo segurando ACCESS SHARE,
# snapshot de cada versão copiado e rolado para o head, e uma migration que
# falha pela metade com a recuperação em seguida. Depois disso julga o catálogo
# que a história produziu: grants por classe (append-only, só leitura,
# apagável), a role de runtime, sequences, índices, chaves estrangeiras,
# cascatas e a correspondência entre as tabelas declaradas e as do catálogo.
# Ele não entra em `verify` porque exige um daemon Docker — o mesmo motivo de
# image-verify —, e a evidência versionada que ele produz é
# `docs/MIGRATION_AUDIT.md`.
migration-audit:
	ARENA_MIGRATION_AUDIT_REPORT=docs/MIGRATION_AUDIT.md tools/migrationaudit/verify.sh
	@echo "migration-audit: ok"

# image-verify é o gate da imagem (P19-T01): constrói, sobe o container com
# filesystem somente leitura contra um PostgreSQL descartável, prova que ele
# aplica as próprias migrations, serve uma página e o asset com hash que ela
# referencia, e entrega a receita e o artefato a tools/imageaudit. Ele não entra
# em `verify` porque exige um daemon Docker — o mesmo motivo de test-e2e.
image-verify:
	ARENA_IMAGE=$(IMAGE) tools/imageaudit/verify.sh
	@echo "image-verify: ok"

# compose-verify é o gate da topologia de produção (P19-T02): constrói a
# imagem, promove o artefato por digest num registry descartável, renderiza e
# audita o documento que o Compose cria, sobe a stack, aplica as migrations com
# a própria imagem, dirige um cadastro pelo ingress público e prova que os dados
# sobrevivem a um restart e a uma recriação completa. Ele não entra em `verify`
# porque exige um daemon Docker — o mesmo motivo de image-verify.
compose-verify: image-build
	ARENA_IMAGE=$(IMAGE) tools/composeaudit/verify.sh
	@echo "compose-verify: ok"

# caddy-verify é o gate da origem Caddy (P19-T03): valida o Caddyfile com a
# imagem que o próprio compose fixa, exige que o arquivo seja o que `caddy fmt`
# escreveria, e roda um Caddy de verdade atrás de um upstream stub para afirmar
# os cabeçalhos que passam, a compressão, quem é acreditado sobre o endereço do
# visitante, o que sai quando a aplicação não responde e o que uma sonda de
# admin alcança. Exige daemon Docker, como image-verify.
#
# ARENA_CADDY_IMAGE sobrescreve a imagem; sem ela, o gate lê o digest fixado em
# compose.production.yaml, que é o que o deploy roda.
caddy-verify:
	tools/caddyaudit/verify.sh
	@echo "caddy-verify: ok"

# backup-verify é o gate do backup e do PITR (P19-T04): julga o compose
# commitado, sobe um PostgreSQL descartável com os argumentos que o próprio
# arquivo declara, mede a arquivamento contínuo pelo pg_stat_archiver, criptografa
# e envia um base backup para um armazenamento compatível com S3, destrói o
# primário e restaura num cluster vazio até um instante escolhido — afirmando o
# que voltou, o que não voltou, as migrations, o checksum das linhas e o que a
# retenção remove. Exige daemon Docker, como image-verify, e constrói a imagem
# da aplicação porque as migrations rodam dentro dela — o mesmo pré-requisito
# de compose-verify e deploy-verify.
#
# ARENA_IMAGE alimenta o passo de migrations e ARENA_BACKUP_S3_IMAGE troca o
# armazenamento; sem elas, o gate usa a imagem local e o digest do MinIO que o
# repositório verificou.
backup-verify: image-build
	ARENA_IMAGE=$(IMAGE) deploy/backup/verify.sh
	@echo "backup-verify: ok"

# disaster-drill é o exercício de desastre e carga da release (P20-T05):
# restaura um backup de verdade num ambiente isolado com os próprios scripts da
# operação (`deploy/backup/base-backup.sh` e `restore.sh`) sobre um PostgreSQL
# descartável, sobe a aplicação nos dados que voltaram, mede a integridade
# financeira contra a leitura de antes da perda, mede RPO e RTO, roda o baseline
# de carga versionado e exercita o provedor de email e o de pagamento
# indisponíveis. O que ele mediu vira `docs/DISASTER_DRILL.md`, julgado por
# `drillaudit check` — que recusa um número fora do teto declarado, um limiar não
# registrado ou um ledger que não voltou igual. Exige daemon Docker, k6,
# navegador e uma build do frontend, como test-e2e e test-load-smoke.
disaster-drill:
	tools/drillaudit/verify.sh
	@echo "disaster-drill: ok"

# deploy-verify é o exercício do pipeline de deploy e rollback (P19-T07):
# promove a imagem por digest através de um registry descartável, deixa o
# pipeline aplicar as migrations, exige que a prontidão e as páginas respondam,
# recusa uma tag, recusa um documento que roda outro artefato, recusa subir sem
# banco, e depois promove releases que *não* ficam prontas — uma que nunca
# responde à prontidão, outra que responde e perdeu a página — afirmando que
# nenhuma delas é promovida, que a versão anterior volta a servir e que o
# arquivo de estado não avança. Exige daemon Docker, como image-verify.
deploy-verify: image-build
	ARENA_IMAGE=$(IMAGE) tools/deployaudit/verify.sh
	@echo "deploy-verify: ok"

# vuln procura vulnerabilidades conhecidas nas dependências Go (P19-T08).
# Exige govulncheck instalado fora do repositório, como image-scan exige o
# scanner e test-load-smoke exige k6: sem ele o alvo falha explicitamente e nunca
# retorna sucesso falso. O workflow de supply chain instala a versão fixada e
# chama **este** alvo, para que o CI rode o mesmo comando que o operador roda.
vuln:
	@command -v "$(GOVULNCHECK)" >/dev/null 2>&1 || (echo "vuln: govulncheck is required; install it outside the repository (go install golang.org/x/vuln/cmd/govulncheck@v1.8.0)" >&2; exit 1)
	$(GOVULNCHECK) ./...
	@echo "vuln: ok"

# image-scan procura vulnerabilidades conhecidas na imagem construída. Ele exige
# um scanner instalado fora do repositório (o padrão é trivy), exatamente como
# test-load-smoke exige k6; sem ele o alvo falha explicitamente e nunca retorna
# sucesso falso. A base distroless não traz gerenciador de pacotes, então o que
# o scanner examina é sobretudo o binário Go e seus módulos.
image-scan: image-build
	@command -v "$(TRIVY)" >/dev/null 2>&1 || (echo "image-scan: trivy is required; install it outside the repository" >&2; exit 1)
	$(TRIVY) image --scanners vuln --severity CRITICAL,HIGH --ignore-unfixed --exit-code 1 $(IMAGE)
	@echo "image-scan: ok"

# test-contract valida o contrato OpenAPI versionado: o documento parseia,
# satisfaz as convenções estruturais do plano (Problem Details, security
# schemes, paginação/idempotência) e casa com as rotas registradas pelo
# binário — drift de rota falha o build (P02-T06).
test-contract:
	$(GO) test ./internal/contract/...
	@echo "test-contract: ok"

# capacity-ratchet rejulga a capacidade backend contra o baseline versionado
# (P28-T08): qualquer threshold, budget, teto ou limite fora do pin falha.
capacity-ratchet:
	$(CAPACITYRATCHET) -root .
	@echo "capacity-ratchet: ok"

# audit-runbooks verifica docs/RUNBOOKS.md contra a árvore (P29-T07):
# comandos destrutivos exigem a marca ⚠, hosts são loopback ou variável do
# operador, e links, âncoras, rotas, scripts e targets precisam existir.
# Alvo standalone: não entra em quick-verify para não mover a contagem de
# gates do ciaudit; os tabletops executáveis vivem nos testes do portão.
audit-runbooks:
	$(GO) run ./tools/runbookaudit -root .
	@echo "audit-runbooks: ok"

# economy-decisions-check é o portão da P31-T08: lê apenas documentos
# versionados e recusa decisão crítica pendente, preço sem aprovação,
# temporalidade ambígua, ameaça sem controle ou oferta jurídica proibida.
# Alvo standalone: não entra em quick-verify; pendência crítica bloqueia
# P32 e nunca se resolve por inferência, env ou flag.
economy-decisions-check:
	$(GO) run ./tools/economydecisions -root .
	@echo "economy-decisions-check: ok"

# economy-certify é o decisor fail-closed da P44-T12: julga um bundle
# JSON de certificação e emite PASS/FAIL com razões estáveis. P44
# exercita apenas fixtures, sem certificado real ou ativação: SHA,
# decisões, manifest de seasons, toolchains, cobertura, mutação,
# carga, threat review, jurisdição, reconciliação por livro, merges
# P46/P47, credencial e produto proibido, tudo dentro do bundle.
# Alvo standalone, fora do quick-verify como os demais portões de fase.
ECONOMY_BUNDLE ?= tools/economycertify/testdata/green.json
economy-certify:
	$(GO) run ./tools/economycertify -bundle $(ECONOMY_BUNDLE)
	@echo "economy-certify: ok"

# release-matrix é o portão da matriz de evidências (P45-T02): julga o
# manifesto versionado quality/release-matrix.json contra os alvos
# reais do Makefile e, com -results/-commit, um diretório de
# artefatos por portão contra o SHA congelado. Portão ausente,
# comando sem alvo, área descoberta, artefato sem SHA ou resultado
# ausente/SKIPPED/falso-verde reprova. Alvo standalone, fora do
# quick-verify; a matriz integral roda uma vez no SHA final de P45.
release-matrix:
	$(GO) run ./tools/releasematrix -root .
	@echo "release-matrix: ok"

# frontend-coverage é o portão de cobertura do frontend (P48-T02):
# confronta quality/frontend-routes.json com api/openapi.json, os
# quatro fragmentos staged e todos os routes.go. Rota nova/omitida,
# client sem página, evidência inexistente, test-only declarado como
# produção, staged tratado como ativo e exclusão injustificada
# reprovam. FRONTEND_COVERAGE_MODE=planning (default) aceita a
# pendência declarada sem certificar; =complete exige zero lacuna
# browser e zero falta de contrato obrigatória. Alvo standalone,
# fora do quick-verify; a cobertura completa só fecha na P58.
frontend-coverage:
	$(GO) run ./tools/frontendcoverage -root .
	@echo "frontend-coverage: ok"

# frontend-coverage-complete é a postura de release do mesmo portão
# (P59-T02): exige zero lacuna browser e zero falta de contrato
# obrigatória, sem aceitar pendência declarada. Alvo do manifesto de
# release (P45-G23); fora do quick-verify como o modo planning.
frontend-coverage-complete:
	FRONTEND_COVERAGE_MODE=complete $(GO) run ./tools/frontendcoverage -root .
	@echo "frontend-coverage-complete: ok"

# audit-toolchain julga os pinos de produção contra quality/toolchain.json
# (P29-T08): qualquer versão fora do pin falha. Alvo standalone, fora do
# quick-verify como os demais portões de fase.
audit-toolchain:
	$(GO) run ./tools/toolchainaudit -root .
	@echo "audit-toolchain: ok"

# toolchain-next emite o veredito informativo (P29-T08): quais pinos
# seguram, quais derivam e quais próximas patches estão aprovadas, sem
# tocar em lockfile. Sempre verde por desenho — informar, não barrar;
# promoção a obrigatória exige tarefa, ADR e as suites Q0.
toolchain-next:
	$(GO) run ./tools/toolchainaudit -root . -report
	@echo "toolchain-next: ok"

# quality-manifest monta o bundle determinístico de evidências (P30-T03):
# manifesto, checksum e cada artefato que a certificação lê. EVIDENCE é o
# diretório de resultados da execução; BUNDLE é o destino. A montagem
# recusa campo/artefato ausente, árvore suja e qualquer PII/segredo, e a
# mesma evidência produz bytes idênticos.
EVIDENCE ?= quality-evidence
BUNDLE ?= quality-bundle
quality-manifest:
	$(GO) run ./tools/qualitymanifest -root . -evidence $(EVIDENCE) -out $(BUNDLE)
	@echo "quality-manifest: ok"

# quality-decide julga um bundle de evidências e emite PASS/FAIL (P30-T05):
# somente conjunto completo, limpo e com waivers válidos retorna PASS. O
# bundle é a única entrada; não há override por env ou flag.
quality-decide:
	$(GO) run ./tools/qualitydecide -bundle $(BUNDLE)
	@echo "quality-decide: ok"

# legacy-inventory fotografa os livros legados em relatório sintético
# (P33-T01): agregados por contrato, origem e prazo, sem PII; órfãos e
# ambíguos bloqueiam. Alvo standalone, fora do quick-verify: o banco
# lido é informado por DSN e a leitura nunca escreve.
LEGACY_DSN ?= postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable
legacy-inventory:
	$(GO) run ./tools/legacyinventory -dsn $(LEGACY_DSN)
	@echo "legacy-inventory: ok"

# generate valida os catálogos i18n, reescreve os artefatos gerados, emite os
# contratos TypeScript do OpenAPI e executa a geração de código SQL tipado com
# sqlc para o adapter PostgreSQL.
generate:
	$(I18NGEN)
	$(CONTRACTGEN)
	$(SQLC) generate
	@echo "generate: ok"

# generate-check valida os catálogos i18n, a ausência de drift nos contratos
# TypeScript e no código SQL gerado pelo sqlc, falhando caso os artefatos
# gerados estejam desatualizados.
generate-check:
	$(I18NGEN) -check
	$(CONTRACTGEN) -check
	$(SQLC) diff
	@echo "generate-check: ok"

# test-security executa as regressões críticas do threat model: cache leak,
# IDOR/ownership, CSRF, replay de webhook, double spend e bypass administrativo.
# A matriz estrutural em internal/security também exige evidência para cada THR-*.
# A lista de pacotes espelha a coluna de evidência da matriz
# (docs/THREAT_MODEL_TEST_MATRIX.md): pacote citado lá roda aqui.
test-security:
	$(GO) test -count=1 ./internal/security/... ./internal/platform/security/... ./internal/identity/application/... ./internal/identity/adapters/http/... ./internal/platform/ratelimit/... ./internal/arenas/adapters/http/... ./internal/arguments/adapters/http/... ./internal/arguments/adapters/postgres/... ./internal/billing/adapters/stripe/... ./internal/billing/application/... ./internal/billing/adapters/http/... ./internal/moderation/adapters/http/... ./internal/moderation/application/... ./internal/moderation/adapters/postgres/... ./internal/audit/adapters/postgres/... ./internal/platform/postgres/... ./internal/wallet/application/... ./internal/positions/adapters/http/... ./internal/positions/application/... ./internal/persuasion/application/... ./internal/transparency/adapters/http/... ./internal/wallet/adapters/http/... ./internal/wallet/adapters/postgres/... ./internal/jobs/adapters/http/... ./internal/contract/...
	@echo "test-security: ok"

# flake-detect caça testes intermitentes (P27-T02): o motor com fixtures
# nas duas direções, waivers com dono e expiração e guarda que proíbe retry
# no CI. As caçadas longas (Q0 30x, pack 10x) são sob demanda com o binário
# (`go run ./tools/flakedetect -package ... -runs N`) e matriz na P45.
flake-detect:
	$(GO) test -count=1 ./tools/flakedetect/
	@echo "flake-detect: ok"

# regression-pack é o pack rápido por módulo (P27-T10): 86 regras Q0/Q1 com
# 142 testes em 39 pacotes, julgado pelo portão tools/regressionpack contra
# quality/regression-pack.json (cobertura total, teste existe em pacote
# rápido, sem skip ou retry de teste, defeitos históricos presos) e executado
# uma vez por pacote com -count=1, sem repetição. Medido em 28s; budget de
# 180s. É feedback de mudança local/PR e nunca substitui o completo
# (test-unit, test-integration, test-race, test-contract, test-security,
# test-e2e) do nightly/release — o portão recusa pack sem does_not_replace.
regression-pack:
	$(GO) test -count=1 ./tools/regressionpack/
	$(GO) run ./tools/regressionpack -root . -exec
	@echo "regression-pack: ok"

# dast é o scanner DAST reproduzível do backend (P26-T10, ADR-018): motor em
# stdlib com fixtures nas duas direções, waivers com dono e expiração, e
# guarda que recusa alvo fora de loopback. O alvo aqui são os testes do
# motor; a varredura integral da árvore com credenciais é sob demanda
# (`go run ./tools/dast -target <loopback> -openapi api/openapi.json`) e
# matriz na P45 — não entra em quick-verify nem em verify pelo custo de
# subir stacks e pelo volume de requests.
dast:
	$(GO) test -count=1 ./tools/dast/
	@echo "dast: ok"

# test-e2e roda as jornadas críticas em navegador real (P18-T07). O harness
# descartável de tools/e2e provisiona um PostgreSQL próprio, um sink de email em
# diretório, o binário `arena server` e o runner pinado — tudo fora do pacote
# entregue. Antes das jornadas, o gate de isolamento prova que nada do runner
# está em web/, no build que as páginas referenciam ou no binário entregue.
#
# O alvo depende de build-web porque são as páginas reais, servindo o build
# real, que as jornadas dirigem.
#
# Ele não está em `verify` porque exige um navegador instalado na máquina
# (o harness garante o build do Chromium que o runner fixa, mas não instala
# dependências de sistema). Ausente do alvo, nunca ausente de gate.
#
# Exige ARENA_DATABASE_URL (o workflow `verify` já a define em todo o job). O
# banco nomeado por ela é apenas a porta: o harness cria um banco descartável
# ao lado dele, migra, semeia e **remove** — o banco apontado nunca é tocado.
test-e2e: build-web
	@tools/e2e/isolation-check.sh
	tools/e2e/harness.sh
	@echo "test-e2e: ok"

# testenv-verify é a verificação ao vivo do ambiente descartável (P22-T01):
# sobe o orquestrador contra um daemon Docker de verdade e mede as quatro
# validações da tarefa — dois namespaces ao mesmo tempo não colidem, uma
# interrupção leva os recursos temporários embora, uma porta ocupada falha com
# diagnóstico, e nenhum serviço usa internet nem credencial real. A última é
# medida, não afirmada: os serviços do produto ficam só na rede interna (o
# `docker inspect` de cada contêiner é conferido), um contêiner de dentro dela
# não alcança um endereço público, e o mesmo probe *alcança* a aplicação ali
# dentro — sem esse controle positivo, "inalcançável" poderia ser um probe que
# nunca rodou. Também recusa um contêiner que tenha recebido a credencial que o
# shell exportou, e confirma que a aplicação recusa um `ARENA_*` desconhecido,
# que é o que mantém o ambiente publicando os próprios nomes fora daquele
# namespace.
#
# Exige daemon Docker e uma build do frontend (`make build-web`), como test-e2e
# e disaster-drill. Ele não está em `verify` porque não é uma verificação de
# código: é a propriedade da máquina em que o ambiente roda, e a suíte de
# `tools/testenv` já cobre as decisões do orquestrador sem daemon.
testenv-verify:
	tools/testenv/verify.sh
	@echo "testenv-verify: ok"

# test-load-smoke executa os cenários k6 versionados contra uma instância
# local preparada exclusivamente com dados sintéticos. O gate falha se k6 não
# estiver instalado ou se o workload não atingir os thresholds declarados.
test-load-smoke:
	@test -n "$(K6_BASE_URL)" || (echo "test-load-smoke: K6_BASE_URL is required" >&2; exit 1)
	@command -v "$(K6)" >/dev/null 2>&1 || (echo "test-load-smoke: k6 is required; install it outside the repository" >&2; exit 1)
	@report="$$(mktemp)"; trap 'rm -f "$$report"' EXIT; \
	printf 'load-smoke report: commit=%s host=%s kernel=%s cpu=%s dataset=%s config=%s\n' \
		"$$(git rev-parse --short HEAD)" "$$(hostname)" "$$(uname -sr)" "$$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo unknown)" \
		"$${K6_DATASET_SEED:-unset}" "$${K6_BASE_URL}"; \
	"$(K6)" run --summary-export "$$report" tests/load/smoke.js; \
	printf 'load-smoke report: summary=%s\n' "$$report"

# lint é o gate da análise estática (P23-T02, ADR-016): as análises do `go vet`
# sobre a árvore entregue e o staticcheck na versão fixada em
# STATICCHECK_VERSION, mais o portão `tools/staticaudit`, que exige que cada
# família de regra continue sendo recusada por uma fixture, compara os achados
# com o baseline versionado (um trinco que só encolhe, com dono e motivo por
# entrada) e julga cada supressão: local, nomeando o check, com motivo, e na
# vocabulário que o analisador fixado realmente lê. Ele entra em `make verify`
# porque a análise estática pertence ao gate de merge; o módulo do analisador é
# resolvido do proxy do Go (ou do cache de módulos) na primeira execução.
lint:
	$(GO) run ./tools/staticaudit -root . -baseline quality/lint-baseline.json \
		-staticcheck-module "$(STATICCHECK_MODULE)" \
		-staticcheck-version "$(STATICCHECK_VERSION)" \
		-toolchain "$(STATICCHECK_TOOLCHAIN)"
	@echo "lint: ok"

# audit-complexity é o gate de complexidade, duplicação e tamanho (P23-T03): o
# portão `tools/complexityaudit` mede cada função da árvore — pontos de decisão,
# aninhamento, parâmetros, tamanho e blocos copiados — contra orçamentos
# declarados por escopo (produto, ferramentas e teste), com piso em cada
# orçamento para que a barra não desça sem revisão. Código gerado sai do corpus
# por proveniência: o marcador do Go é lido nos comentários antes da cláusula
# `package`, nunca nos bytes do arquivo. Ele entra em `make verify` porque um
# gate de merge é exatamente onde a função gerada sem ninguém olhando precisa
# ser recusada — `-print-findings` imprime o baseline para um humano atualizar,
# e o portão nunca reescreve o arquivo.
audit-complexity:
	$(GO) run ./tools/complexityaudit -root .
	@echo "audit-complexity: ok"

# audit-deadcode é o gate de código morto, placeholder e caminho impossível
# (P23-T04): o portão `tools/deadcodeaudit` julga a árvore inteira com seis
# regras — panic com vocabulário de placeholder, função que se anuncia como não
# pronta e devolve sucesso, adiamento sem dono (o marcador tem de nomear
# `Pnn-Tnn` ou `#nn`), sentença depois de um término incondicional, desvio sobre
# literal e configuração que mente nas cinco direções (aceita e não lida, não
# documentada em `.env.example`, lida e não aceita, documentada e recusada, e
# chave `ARENA_*` lida direto do ambiente fora do registro). Cada regra prova a
# própria fixture nas duas direções — a que ela recusa e a que ela aceita — e o
# código gerado sai do corpus por proveniência. Não há baseline: a árvore não
# tem achado, e um achado futuro é uma recusa e não uma linha nova.
audit-deadcode:
	$(GO) run ./tools/deadcodeaudit -root .
	@echo "audit-deadcode: ok"

# audit-errors é o gate de erros, contextos e recursos (P23-T05): o portão
# `tools/erroraudit` julga a árvore inteira com nove regras — recurso adquirido e
# não liberado, transação sem rollback no caminho de erro, cliente HTTP sem teto,
# contexto de parâmetro que o corpo ignora, `context.TODO`, `fmt.Errorf` sobre um
# erro sem `%w`, resultado de chamada do módulo descartado, mensagem pública que
# carrega o erro interno ou a credencial e goroutine sem dono. Cada regra prova a
# própria fixture nas duas direções — a que ela recusa e a limpa que ela aceita —,
# o código gerado sai do corpus por proveniência e os testes ficam fora dele por
# papel (P23-T07). O que o portão não consegue julgar sem type-checker — o erro
# descartado de um método de interface, de uma função da biblioteca padrão ou de
# uma variável — é medido e impresso: sem contagem a lacuna não é revisada. Não há
# baseline: a árvore não tem achado, e um achado futuro é uma recusa.
audit-errors:
	$(GO) run ./tools/erroraudit -root .
	@echo "audit-errors: ok"

# audit-provenance é o gate de procedência e drift dos artefatos gerados
# (P23-T06): o portão `tools/provenanceaudit` lê `quality/provenance.json`, o
# registro que nomeia para cada pipeline o gerador, o comando que o regenera, o
# pino de versão com a evidência na árvore, o que ele lê, o que ele escreve e como
# a sua reprodutibilidade é provada, e recusa o artefato cujos bytes não são os
# registrados (o que uma edição à mão parece de fora), o insumo ou o gerador que se
# moveu sem regenerar, o gerado que não se anuncia, o gerado que nenhuma família
# declara, o pino sem evidência, a família que não diz quem prova o seu
# determinismo e o produto de build que o `.gitignore` não cobre. Ele complementa
# `make generate-check`, que responde as mesmas perguntas de drift com sqlc, Node e
# uma regeneração completa; este responde as que não precisam de toolchain, e por
# isso cabe no caminho rápido de cada PR. Não há baseline: a árvore não tem achado.
# O portão **nunca** reescreve o registro — `$(GO) run ./tools/provenanceaudit
# -print-register` imprime o documento atualizado para um humano commitar, porque a
# revisão de uma regeneração é um diff de digests e não um diff de mil linhas
# geradas.
audit-provenance:
	$(GO) run ./tools/provenanceaudit -root .
	@echo "audit-provenance: ok"

# audit-tests é o gate da qualidade dos próprios testes (P23-T07): o portão
# `tools/testaudit` é o único do estágio que julga o corpus que os outros deixam
# de fora de propósito — um portão que julgasse um dublê recusaria o andaime que
# torna o caminho de erro alcançável. Ele lê os arquivos `_test.go` e as fixtures
# sob `testdata/` com nove regras: o teste que se desliga (`t.Skip`), o que não
# chama, não assere e não entra em pânico, a asserção que compara uma expressão com
# ela mesma, a fixture que nenhum arquivo nomeia, o arquivo cujos dublês são mais
# numerosos que as asserções, o `time.Sleep` que o corpo do teste executa, a
# entropia de um `math/rand` global não semeado, o erro observado e não cobrado e a
# condição que pede dois resultados incompatíveis ao mesmo sujeito. Cada regra
# prova a própria fixture nas duas direções, e a pausa que o teste **entrega** a um
# dublê e a que fica dentro de um laço são medidas e impressas em vez de recusadas,
# porque nenhuma das duas é o teste esperando pelo sujeito. A exceção é
# `quality/test-waivers.json`: um registro com lugar, regra, classe de risco não
# crítica, dono, razão, data de expiração e teste compensatório — e o portão recusa
# a exceção crítica, a expirada e a que sobrou, porque a lista só encolhe. Não há
# baseline. O portão **nunca** reescreve o registro, e uma execução que julga zero
# arquivo é recusada: corpus vazio é a forma de um portão que parou de funcionar.
audit-tests:
	$(GO) run ./tools/testaudit -root .
	@echo "audit-tests: ok"

# audit-diff é o gate da mudança de produção com evidência (P23-T08): o portão
# `tools/diffaudit` julga a **faixa** que a branch traz sobre aquela a que ela
# aponta (`origin/main`, ou `main`), commit a commit, e não a árvore. Ele
# classifica cada arquivo que cada commit toca pela política versionada
# `quality/diff-policy.json` e exige a evidência que a classe declara, com oito
# regras: o arquivo que nenhuma classe cobre (recusa, e não silêncio); o arquivo
# **novo** de produção sem teste da mesma área na mesma mudança; a migration sem a
# prova de atualização; o conjunto de rotas que muda sem o contrato publicado; o
# artefato gerado sem o insumo que o produz — e o insumo sem o artefato
# regenerado, que é a mesma falha vista do outro lado; a referência que o catálogo
# declara e a árvore não tem; a regra Q0 que muda na tabela sem a regressão
# nominal e adversarial; e a mensagem de commit que anuncia a dispensa, que o
# programa proíbe por nome. Os registros que ele lê já são desta fase: o gerado e
# o par insumo/artefato vêm de `quality/provenance.json` (P23-T06) e as regras e a
# sua evidência vêm de `quality/catalog.json` (P21), de modo que este portão e o
# `make generate-check` nunca discordam sobre o que pertence a quê. Uma base que
# não resolve é recusa: um portão que não vê o diff não responde verde sobre ele.
# O portão **nunca** escreve: `-print-document` imprime o documento que ele julgou
# para uma recusa ser discutida com o diff na mão.
audit-diff:
	$(GO) run ./tools/diffaudit -root .
	@echo "audit-diff: ok"

# audit-deps é o gate da procedência das dependências (P23-T09): o portão
# `tools/dependencyaudit` julga o que a árvore é feita — os módulos que o
# `go.mod` exige (diretos e indiretos), os pacotes que os manifestos npm
# instalam, as imagens que os arquivos de contêiner nomeiam, as actions que os
# workflows rodam e as ferramentas que os alvos exigem — contra o registro
# versionado `quality/dependencies.json`, que aprova cada componente com
# **classe, dono, finalidade, alcance e licença**, e contra o catálogo de
# licenças `docs/DEPENDENCIES.md`. Treze regras: o componente que nenhuma
# entrada aprova (que é a dependência transitiva nova); a entrada que a árvore
# não declara mais, porque remover dependência é atualizar a evidência dela; a
# entrada sem dono, finalidade, alcance, classe, licença ou evidência de versão;
# a linha do catálogo que sobrevive à dependência que ela descreve; a licença
# fora do que a classe homologa (a reciprocidade forte que é aceitável num
# linter executado fora e proibida no binário); a faixa onde a classe exige pino
# exato; a imagem sem digest no arquivo que roda em produção; a action presa a
# uma tag em vez de a um commit; o módulo direto que ninguém importa; o
# componente fixado em duas versões; o nome que a política bane; o import do
# browser que sai da árvore; e a lista de materiais `quality/sbom.json`, que é
# **derivada** do mesmo censo e por isso recusa quando discorda do que se
# entrega.
#
# O portão nunca escreve: `-print-register` imprime o inventário que a árvore
# declara (com os campos vazios que o próximo run recusa) e `-print-sbom`
# imprime a lista de materiais, porque aprovar uma dependência é decisão humana e
# o documento impresso é o que um humano commita. Duas classes declaram a própria
# lacuna em vez de fingir um pino: `tooling-external` é a ferramenta que a árvore
# exige pelo nome — `k6` e `trivy` hoje —, e o portão mede e imprime essa
# população em toda execução.
audit-deps:
	$(GO) run ./tools/dependencyaudit -root .
	@echo "audit-deps: ok"

# audit-mutations é o gate de mutation testing das regras críticas (P24-T10):
# o portão `tools/mutationaudit` executa o gremlins fixado sobre os pacotes
# domain/application Q0/Q1 do registro `quality/mutations.json` e cobra os
# limiares por risco (Q0 ≥ 90%, Q1 ≥ 80%), zero sobrevivente não-manifestado
# nas áreas autorização, wallet, billing, webhook, idempotência, moderação e
# privacidade, e o manifesto de equivalentes com prova. É teste caro (~3 min):
# entra em `make verify`, não no caminho rápido de cada PR, cujo desenho
# recusa o barato antes do caro. A matriz completa (operadores opt-in e os
# pacotes diferidos) é gate de release na P45.
audit-mutations:
	$(GO) run ./tools/mutationaudit -root . \
		-tool-module "$(GREMLINS_MODULE)" \
		-tool-version "$(GREMLINS_VERSION)" \
		-timeout-coefficient "$(GREMLINS_TIMEOUT_COEFFICIENT)" \
		-workers "$(GREMLINS_WORKERS)"
	@echo "audit-mutations: ok"

# audit-coverage é o gate de pisos de cobertura e ratchet de diff (P24-T11):
# o portão `tools/coverageaudit` mede a cobertura de linhas por
# package/risco com o coverprofile padrão do Go (sem ferramenta de terceiro
# para fixar) e cobra 95% Q0, 90% Q1, 80% Q2, 85% global e 95% no diff, com
# pisos por pacote que só sobem e débito explícito com gate P45 para os 19
# pacotes abaixo do piso nominal, além da allowlist versionada de gerados.
# O relatório cita os limiares de mutação sem julgá-los, provando que
# cobertura e mutation score são gates independentes. É teste médio (~30s,
# só domain/application, sem banco): entra em `make verify`, não no caminho
# rápido, pelo mesmo desenho do gate de mutação. A matriz completa é gate
# de release na P45.
audit-coverage:
	$(GO) run ./tools/coverageaudit -root .
	@echo "audit-coverage: ok"

# verify agrega os gates existentes do estágio atual.
verify: fmt-check lint audit-complexity audit-deadcode audit-errors audit-provenance audit-tests audit-diff audit-deps audit-mutations audit-coverage generate-check test-unit test-integration test-race test-migration test-contract test-security test-web typecheck build-web audit-web audit-i18n i18n-audit audit-ci audit-req quality-catalog quality-waivers quality-taxonomy quality-inventory handoff-check
	@echo "verify: gates criados que exigem ambiente próprio e por isso não entram neste alvo:"
	@for gate in test-e2e test-load-smoke image-verify image-scan caddy-verify compose-verify migration-audit backup-verify deploy-verify disaster-drill vuln; do \
		echo "  - $$gate"; \
	done
	@echo "verify: portão de release, que não pertence a um merge (decisões humanas do lançamento, auditoria de segurança, revisão de privacidade e verificação reproduzível):"
	@for gate in release-gate security-audit privacy-audit release-verify; do \
		echo "  - $$gate"; \
	done
	@echo "verify: OK — todas as capacidades existentes do estágio atual passaram."
