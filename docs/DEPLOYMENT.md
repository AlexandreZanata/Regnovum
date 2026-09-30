# Deploy e operação

**Status:** plano inicial; nenhum ambiente foi provisionado por este documento

## 1. Sistema operacional e topologia

- Debian estável ou Ubuntu LTS com atualizações de segurança.
- Docker Engine e Compose plugin.
- Containers iniciais: `caddy`, `app`, `worker` e `postgres` (app e worker usam a mesma imagem, com comandos diferentes; P19-T01).
- Mesmo artefato imutável para app e worker, com comandos diferentes.
- Volumes persistentes apenas para PostgreSQL, Caddy e dados operacionais necessários.
- Upload público nunca fica no filesystem local da VPS.

Somente HTTP/HTTPS chegam publicamente à origem. Administração usa Tailscale preferencialmente; se SSH público for temporariamente necessário, somente chave, usuário não-root e limite de origem.

## 2. Orçamento inicial da VPS de 16 GB

Valores são limites de investigação, não promessa de capacidade:

- PostgreSQL: aproximadamente 4–6 GB entre buffers e processos.
- Aplicação e worker: limite combinado inicial abaixo de 2 GB.
- Caddy: abaixo de 256 MB em operação comum.
- Sistema, agentes e margem operacional: aproximadamente 2 GB.
- Restante preservado para page cache e picos.

Parâmetros como `shared_buffers`, `work_mem`, conexões e pool serão definidos por benchmark com CPU, NVMe e workload reais. Configuração copiada de outra VPS é proibida sem medição.

## 3. Rede

- PostgreSQL escuta apenas na rede privada do Compose.
- Caddy é o único container com portas públicas.
- Cloudflare usa modo TLS `Full (strict)` até a origem; `Flexible` é proibido.
- A origem recusa o tráfego que não vem do Cloudflare **no firewall**, não no servidor web (§3.1).
- Headers de proxy são confiados apenas aos CIDRs do Cloudflare, declarados em `deploy/caddy/Caddyfile` (§3.1).
- O certificado da origem vem de arquivo *secret*: Cloudflare Origin CA no que o repositório commita, ou uma build com ACME por DNS-01 (§3.2).
- IPv4 e IPv6 seguem a mesma política; não deixar origem exposta por uma família esquecida.
- Egress é permitido apenas conforme necessidade e monitorado para jobs sensíveis.

### 3.1 Quem é acreditado, e o que recusa o resto (P19-T03)

O `deploy/caddy/Caddyfile` declara `trusted_proxies static` com a lista publicada pelo Cloudflare — 15 faixas IPv4 e 7 IPv6, de https://www.cloudflare.com/ips-v4 e https://www.cloudflare.com/ips-v6, lidas em 2026-09-21 — e `client_ip_headers CF-Connecting-IP`. A consequência exata: `CF-Connecting-IP` só é evidência quando o peer está dentro daquelas faixas; para qualquer outro peer, o endereço do visitante **é** o endereço do peer e o header é ignorado. `X-Forwarded-For` não é consultado nem quando o peer é confiável: a lista foi estreitada a um header de valor único, e um header de cadeia é exatamente o que um cliente pode preencher.

O que o Caddyfile **não** faz é decidir quem pode conectar. A recusa do tráfego que não vem do Cloudflare pertence ao firewall em frente ao container (security group, `nftables`, o balanceador): ali a recusa não custa handshake TLS, nem requisição HTTP, nem linha de log — e uma allowlist declarada no servidor web recusaria também os gates deste repositório, que sobem a stack de verdade a partir do host. As faixas em que a regra deve ser escrita são as mesmas da lista acima, e a fonte é a mesma.

Nota de encaminhamento, registrada e não escondida: a aplicação ainda conta por endereço de peer (`clientip.New(nil)`; o critério está registrado desde a P16-T03), então atrás do Caddy todos os visitantes compartilham uma chave de rate limit. O Caddy passa o endereço que determinou em `X-Forwarded-For` e remove `CF-Connecting-IP` e `X-Real-IP` vindos do cliente, de modo que a informação está do lado certo da fronteira; fiar a aplicação para confiar no peer imediato é o critério registrado que falta.

Gate: `make caddy-verify` valida o arquivo com a **imagem que o próprio compose fixa**, exige que ele seja o que `caddy fmt` escreveria e roda um Caddy real atrás de um upstream stub para afirmar o que passa, o que sai e o que acontece quando a aplicação não responde.

O nome do software não sai do edge, e isso vale para os dois caminhos de resposta. Na rota proxiada um `header -Server` remove o valor; no bloco `handle_errors`, que escreve as respostas que a aplicação nunca produziu, um segundo `-Server` faz o mesmo — e o segundo existe porque o primeiro **não alcança** aquele caminho: medido na 2.10.0 fixada, sem a linha do bloco de erro a resposta de indisponibilidade sai com `Server: Caddy`. O gate afirma as duas coisas — o documento servido e a resposta de erro —, e o upstream stub do gate **declara um nome próprio** (`Server: stub/1.0`) porque, com um upstream silencioso, a asserção sobre vazamento de nome mediria nada: o Caddy encaminha o `Server` do upstream, e o `header -Server` da rota é exatamente o que o remove.

Limite medido, registrado e não escondido: `strict_sni_host on` é afirmado pelo audit sobre o arquivo (regra `sni_strict`, com mutação própria que reprova), mas a sonda de fio **não distingue** `on` de `insecure_off` nesta topologia — com um único certificado vindo de arquivo, um handshake para outro nome termina sem certificado nas duas configurações (curl 35). A asserção de fio continua sendo sobre o que se observa (um handshake para outro nome termina sem um), e a diretiva é responsabilidade do audit.

### 3.2 O certificado da origem

- **Cloudflare Origin CA** — o que o repositório commita: um certificado de longa duração emitido pelo Cloudflare para a origem, entregue como dois arquivos montados em `/run/secrets/origin_certificate` e `/run/secrets/origin_key`. Nenhum desafio precisa ser alcançável da internet e nenhuma renovação de 90 dias pode falhar em silêncio, que é o motivo da escolha: `Full (strict)` valida o certificado da origem, e uma renovação que falha é uma janela de indisponibilidade.
- **ACME por DNS-01** — renovação automática sem expor a origem, ao custo de uma build do Caddy com o plugin do provedor de DNS e de um token do provedor no ambiente do container: uma credencial de DNS dentro do processo que responde à internet. Vale quando o certificado precisa vir de uma CA pública.

O par de arquivos é a fonte, declarada como `tls <cert> <key>`; o Caddy responde só pelo nome do site (`strict_sni_host on`), com piso de TLS 1.2/1.3.

O Caddy roda como root dentro do container, e isso é uma decisão registrada, não um descuido: a chave é 0600 do usuário que faz o deploy, e lê-la como um root que largou capacidades exigiria devolver `CAP_DAC_READ_SEARCH` — "ler qualquer arquivo do sistema" — por um arquivo só. A alternativa correta (rodar o Caddy com o uid dono do par e `sysctls: net.ipv4.ip_unprivileged_port_start=0` para ainda poder abrir 80/443) depende de como a operação provisiona o par — dono, modo, grupo — e é uma decisão de deploy ainda não tomada.

## 4. Ambientes

- `local`: dados sintéticos, compose de desenvolvimento.
- `ci`: banco descartável por execução.
- `staging`: configuração semelhante, sem dados pessoais de produção.
- `production`: dados reais, secrets e volumes separados.

Nunca copiar banco de produção integral para desenvolvimento. Fixtures e dados anonimizados são a alternativa.

## 5. Pipeline

1. PR executa o check curto obrigatório; cada microtarefa prova localmente as regras e riscos alterados. Um merge em `main` **não** certifica release. No candidato de versão, executar a verificação completa: drift, testes unitários/integração/race, contratos, segurança, browser, vulnerabilidades, imagem/scan, ingress, topologia, backup/PITR e deploy/rollback ([CI.md](CI.md)). Falha impede tag estável, release e deploy.
2. Merge em `main` produz imagem OCI no GitHub Container Registry.
3. Release promove uma imagem por digest, não recompila na VPS.
4. Backup e verificações pré-deploy são executados.
5. Migrations compatíveis são aplicadas por papel separado.
6. Containers são atualizados e health checks confirmados.
7. Smoke tests exercitam leitura, autenticação e dependências críticas.
8. Falha faz rollback da aplicação; migration destrutiva nunca depende de `down` automático.

### A imagem da aplicação (P19-T01)

O `Dockerfile` na raiz compõe a imagem em três estágios, e cada um existe por um motivo que a sua ausência quebraria:

- **web** — compila TypeScript 7 em ESM nativo com o `tsc` oficial (`npm ci --ignore-scripts` sobre o lockfile e nada além: sem bundler, por política). Node vive aqui e não sai deste estágio;
- **build** — compila o binário Go e roda `assetgen`, que transforma o grafo emitido mais `web/src` nos endereços com hash e no manifest que o servidor relê no boot. Os dois sistemas de build se encontram uma única vez, em `web/generated`, que nenhum dos dois possui;
- **runtime** — o binário, o build de assets e a base. Nada de gerenciador de pacotes, compilador, shell ou Node.

As bases são **fixadas por digest do manifest list**, nunca por tag flutuante, com a tag mantida ao lado do digest (`node:24-bookworm-slim@sha256:...`, `golang:1.27.1-bookworm@sha256:...`, `gcr.io/distroless/static-debian12:nonroot@sha256:...`): a tag diz o que a imagem é, o digest decide qual imagem é. Atualizar uma base é um commit revisável, não um efeito colateral de um `docker build` de amanhã.

O container resultante tem propriedades que o smoke mede, não que a receita promete:

- **não é root** — `USER 65532:65532`, declarado na receita e conferido no `Config` da imagem construída;
- **filesystem somente leitura** — `docker run --read-only` funciona porque o processo não escreve disco nenhum: o sink de email é recusado em produção, as migrations vivem dentro do binário e os logs saem em stdout. Um deploy que precise de rascunho monta um `tmpfs`, e isso é decisão do Compose, não da imagem;
- **sem árvore de fontes** — a imagem carrega o binário e `web/dist`, e o gate reprova qualquer arquivo de fonte (`*.ts`, `*.go`, `web/src/`), cache (`node_modules`, `GOPATH`, cache de apt) ou shell/compilador que apareça no runtime;
- **migrations dentro do binário** — `arena migrate up` roda a partir da própria imagem, contra um banco vazio, sem nada montado de fora; é o que o smoke executa antes de subir o servidor;
- **`ARENA_ADDR=0.0.0.0:8080` e `ARENA_ASSETS_DIR=/web/dist`** vêm definidos na imagem: o default `127.0.0.1` deixaria o processo inalcançável dentro do próprio namespace de rede, e só o Caddy publica porta. Nenhuma dessas duas é segredo — a configuração continua recusando variável `ARENA_*` desconhecida, e um `ARG`/`ENV` com nome de credencial reprova o gate, porque o que entra num build fica gravado numa camada;
- **metadata por build arg** — `VERSION`, `COMMIT` e `BUILD_DATE` chegam por `-ldflags` ao `buildinfo`; nenhum deles é credencial.

O contexto de build é reduzido pelo `.dockerignore`, que exclui `.git`, `.local`, `.env`/`.env.*`, `node_modules`, `web/dist`, `web/generated` e o tooling local — e a receita copia caminhos explícitos, nunca `COPY . .`. O gate exige as duas coisas: uma varredura que encontre o contexto inteiro oferecido ao build é uma varredura que vai encontrar um segredo.

O que verifica isso:

- `make image-build` constrói a imagem (tag em `IMAGE`, padrão `goyim-arena:local`);
- `make image-verify` é o gate: constrói, sobe um PostgreSQL descartável, aplica as migrations **de dentro da imagem**, sobe o container com filesystem somente leitura, prova `/health/live` 200, uma página (`/login` em `text/html`), um endereço com hash lido do manifest **de dentro da imagem** e um 404 para endereço que o manifest nunca declarou, e então entrega receita, contexto, `Config` e filesystem exportado a `tools/imageaudit`;
- `make image-scan` procura vulnerabilidades conhecidas na imagem construída, com `--severity CRITICAL,HIGH --ignore-unfixed --exit-code 1`;

Nenhum dos três entra em `make verify`, pelo mesmo motivo de `test-e2e`: exigem um daemon Docker (e o scan exige um scanner instalado fora do repositório, como `test-load-smoke` exige k6). Ausente do alvo, nunca ausente de gate: sem o scanner o alvo falha explicitamente e nunca retorna sucesso falso.

Limites registrados: a imagem é construída para a arquitetura do host (uma matriz multi-arquitetura é trabalho de deploy, não desta tarefa); `tools/imageaudit` varre o conteúdo em busca das formas conhecidas de credencial (DSN de desenvolvimento, chaves de provedor, chaves privadas) e **não pode** provar a ausência de um segredo desconhecido — é por isso que o `.dockerignore` e a cópia explícita continuam sendo a defesa principal; e o scan depende de um banco de vulnerabilidades atualizado no momento da execução.

### Requisitos de boot do `arena server` (P18-T07A, P18-T07B, P18-T07C)

O processo compõe as jornadas que serve a partir da configuração, e recusa o boot quando falta o que elas exigem:

- com `ARENA_DATABASE_URL` definida, a jornada de conta do browser é composta e montada no mux da plataforma (dentro das camadas de request id, locale e política de segurança); `ARENA_ASSETS_DIR` passa a ser obrigatória e aponta para um build do `make build-web` (padrão `web/dist`). O boot falha em vez de servir páginas cujos assets não existem;
- a jornada de participação da Arena (`/arenas/{slug}` e as quatro transições) entra junto, sobre o mesmo pool: é ela que cobra INK, e a publicação de um argumento debita a carteira na mesma transação em que grava o argumento. Ela exige `ARENA_CURSOR_SECRET` (mínimo de 32 bytes) porque assina os cursores das listas públicas; um cursor assinado com chave efêmera deixaria de resolver depois de um reinício, o que a pessoa vive como uma página que quebrou. Em desenvolvimento e teste, sem a variável a jornada **não** é montada e o log diz exatamente isso; em produção o boot é recusado, porque o Arena é o produto;
- sem a DSN o processo serve somente as rotas de health e registra isso no log: um processo que responde 404 em toda página enquanto se declara pronto é pior que uma sonda que diz o que é;
- em produção, a jornada de conta exige `ARENA_RESEND_API_KEY` (credencial do provedor, segredo redigido) e `ARENA_EMAIL_FROM` (endereço remetente validado, com nome de exibição opcional) e recusa o boot sem eles, porque um cadastro cujo link de confirmação não sai não é uma jornada. Com as duas variáveis, a composição instala a **entrega enfileirada** (P19-T02A): o módulo de identidade entrega a mensagem a uma bridge que a grava como trabalho durável (`app.jobs`, tipo `email_delivery`) na mesma transação do efeito de domínio, e é o `arena worker` que renderiza e envia pelo provedor;
- em desenvolvimento e teste a composição instala o sink local do módulo de identidade (em memória, ou em diretório com `ARENA_EMAIL_SINK_DIR`) e avisa no log que as mensagens são registradas e não entregues: ali nada é enfileirado, e é por isso que o worker não registra handler de email nesses ambientes — um handler instalado consumiria trabalho que nenhum fluxo produziu.

**O que isso exige de um deployment de produção:** o processo que serve páginas **enfileira** e o processo `arena worker` **entrega**. Subir apenas o servidor deixa a confirmação de cadastro na fila e nenhum email sai — a mensagem não se perde, ela fica esperando um consumidor —, então um ambiente que serve contas reais precisa dos dois processos, cada um com as mesmas variáveis de email (o worker também compõe a entrega, e também recusa o boot sem elas). O endereço do provedor não é configurável de propósito: com a URL fixa, a credencial só pode ser enviada ao próprio provedor.

- o build referenciado pelas páginas é servido pelo **mesmo processo**, a partir do manifest que ele já lê: a superfície publica exatamente os endereços que o build declarou e nada mais. É conteúdo, não operação: ela não entra no registro de rotas nem no contrato OpenAPI (um CSS não é um endpoint), e a política de cache é a da seção 9 — nome com hash `immutable` por um ano, caminho estável do grafo ESM com revalidação. Um endereço que o manifest não publicou responde 404, não há listagem de diretório e um caminho que tente sair do diretório do build não é filtrado, é irrepresentável: a lista de endereços é montada na composição a partir do manifest, e um manifest que descreva algo fora do build recusa o boot.

- em desenvolvimento e teste, `ARENA_EMAIL_SINK_DIR` faz o sink local de email escrever cada mensagem de identidade em um documento JSON no diretório nomeado, em vez de apenas registrar na memória: é assim que uma jornada dirigida **por outro processo** — o harness de browser de `tools/e2e`, ou uma pessoa completando um cadastro à mão — lê o código de confirmação. A variável é recusada em produção, no `Load` e na composição, porque um diretório de códigos de contas reais não é um mecanismo de entrega;

As rotas que o registro declara e nenhuma superfície monta continuam respondendo como placeholder: o processo declara o contrato inteiro e serve o que foi composto.

### A topologia de produção (P19-T02)

`compose.production.yaml` descreve os quatro papéis da §1 e **nada é construído onde a stack roda**: `app` e `worker` são o mesmo artefato, referenciado pelo digest do manifest list, com comandos diferentes — servir e executar trabalho enfileirado são duas perguntas feitas à mesma imagem —, `caddy` é o único serviço com porta publicada, e `db` só existe na rede interna.

As três redes são três concessões distintas, e não uma topologia decorativa: `edge` liga o ingress à aplicação (e a mais nada), `internal` é a rede do banco, declarada `internal: true` para que o processo que guarda os dados não tenha rota de saída, e `egress` é a única saída para os provedores (pagamento e email) — dela participam apenas os processos que fazem chamada de saída. O worker **não** está em `edge`: uma requisição de fora não tem caminho até um processo sem superfície de entrada. Como consequência verificada, o Caddy não compartilha rede com o PostgreSQL: um erro de rota não vira acesso ao banco.

Os volumes são três — dados do PostgreSQL, dados e configuração do Caddy. As aplicações não persistem nada: rodam com filesystem somente leitura e `cap_drop: ALL`, e é isso que torna substituir uma delas um restart e não uma migração.

**Nada de segredo no arquivo.** O Compose nomeia onde um valor vem, nunca o que ele é. A aplicação e o worker leem `COMPOSE_ENV_FILE` (padrão `.env.production`), o banco lê `COMPOSE_DB_ENV_FILE` (padrão `.env.production.db`), e o certificado e a chave da origem são arquivos do operador nomeados por caminho (`COMPOSE_TLS_CERT_FILE`, `COMPOSE_TLS_KEY_FILE`), montados como *secret* em `/run/secrets`. Dois arquivos de ambiente em vez de um é a metade de menor privilégio da mesma decisão: as credenciais de pagamento e de email nunca entram no container do banco, e a senha do banco nunca entra em um processo que a usa só dentro da DSN. Ambos são ignorados pelo Git (`.env.*`) e devem ser 0600; nenhum deles vai para a imagem.

**Limites iniciais** vêm da §2 (banco 4 GB, aplicação e worker 1 GB cada, Caddy 256 MB) e são limites a investigar, não promessa de capacidade: a P28 os revisita com medição. `make compose-verify` confere que o limite declarado **chegou ao container** (`HostConfig.Memory` e `NanoCpus` de cada serviço), porque um bloco `deploy` ignorado pelo runtime seria um número bonito e nenhuma proteção.

**Healthchecks, e o limite honesto:** o banco tem o seu (`pg_isready`) e o ingress tem o seu (o endpoint de administração em loopback, que só responde depois de a configuração carregar — a P19-T03 é dona do resto do Caddyfile e, se desligar o admin, muda esta sonda junto). A aplicação e o worker **não podem** ter uma sonda executada de dentro: a imagem é distroless e carrega um único binário, sem shell nem cliente HTTP, e um healthcheck que sempre sai 0 (`/arena version`, por exemplo) seria um verde falso, o que este repositório proíbe. Em vez disso a stack é provada de fora — o gate dirige o ingress até `/health/live` e `/health/ready` — e os serviços da imagem da aplicação declaram `stop_grace_period`, porque um processo que não pode ser sondado ao menos precisa poder parar sem abandonar trabalho. O gate de topologia exige exatamente isso: sonda para todo serviço capaz de executá-la, e prazo de parada para os que não são.

**Migrations não fazem parte da stack.** Uma release as aplica como passo próprio com a mesma imagem (`docker compose run --rm app migrate up`), porque um container que migra enquanto os vizinhos servem é uma mudança de schema correndo com uma requisição.

O que verifica isso:

- `make compose-verify` (P19-T02) constrói a imagem, promove o artefato **por digest** através de um registry descartável (o mesmo caminho da §5.2), renderiza o documento (`docker compose config --format json`), entrega-o a `tools/composeaudit` junto do arquivo commitado, sobe a stack em loopback com porta efêmera, aplica as migrations com a imagem, prova que o certificado servido é o que o operador forneceu, faz um **cadastro real pelo ingress** (o que prova ingress → aplicação → banco → fila), espera o worker consumir a entrega enfileirada, inspeciona as portas publicadas, confere os limites aplicados e então reinicia e **recria** a stack provando que a conta e o trabalho enfileirado sobrevivem. O que a espera do worker prova é o consumo da fila — um worker que não abriu socket também registra tentativa —, então o resultado gravado é **lido e impresso**: contra um provedor alcançável o detalhe é a recusa da credencial de exemplo e a rota de saída fica provada naquele run, sem rota o detalhe é a falha de transporte. Exigir o primeiro tornaria o gate dependente de um serviço de terceiro, e nenhuma asserção do gate afirma mais do que mede.
- `tools/composeaudit` responde as perguntas da topologia sobre o documento *criado*: imagem fixada por digest, nenhum serviço que constrói, um único ingress, porta publicada que não seja HTTP/HTTPS, porta do banco publicada, rede não declarada, rede do banco que não seja interna, ingress compartilhando rede com o banco, `restart`, limites e **log com `max-size`** declarados (um log sem limite cresce até encher o disco onde o banco escreve), sonda (ou prazo de parada), papéis da aplicação com comandos distintos e **cada papel com rota de saída** (um processo que só entrou em rede interna não entrega mensagem nem cobra cartão, e isso é propriedade do arquivo, não do host que roda o gate), variáveis que a produção exige, senha do banco fora dos processos que não a usam, e valor literal em chave com nome de credencial no arquivo commitado. Ele nunca imprime valor de ambiente: o documento renderizado já embutiu os arquivos do operador, e um gate que os ecoa é um vazamento com visto verde.
- Nenhum dos dois entra em `make verify`, pelo mesmo motivo de `image-verify`: exigem um daemon Docker. Ausentes do alvo, nunca ausentes de gate — as regras de `tools/composeaudit` rodam como teste unitário dentro de `make verify`, com um fixture por regra e um mutante por fixture.

A P19-T03 é dona do que falta no ingress: TLS `Full (strict)` com a origem Cloudflare, proxies confiáveis explícitos, security headers, compressão, comportamento de rota/cache e health do upstream. O que esta tarefa entrega é a **topologia** — caminho completo da internet à aplicação, e o que não deve ser alcançável.

O gate de browser (`make test-e2e`, P18-T07) dirige esse mesmo processo: `tools/e2e/harness.sh` provisiona um PostgreSQL descartável (removido em qualquer caminho de saída), um diretório de sink próprio, semeia duas contas confirmadas com INK e uma Arena publicada, sobe o binário e roda as jornadas com o Playwright pinado. `tools/e2e/isolation-check.sh` roda antes e recusa o gate se o runner aparecer no pacote do frontend, no build servido ou no binário. Ele não está em `make verify` porque exige um navegador instalado na máquina.

### O pipeline de deploy e rollback (P19-T07)

`deploy/deploy.sh` promove **um** artefato por digest, aplica as migrations **antes** de a release servir, prova a promoção e sabe voltar. As quatro regras que ele impõe:

1. **O artefato é um digest, nunca uma tag.** Uma tag é mutável, então é uma promessa que o registry pode quebrar depois que a máquina que promoveu parou de olhar. A referência precisa carregar `@sha256:` com 64 caracteres hexadecimais, a imagem precisa existir no lugar onde será buscada e a própria imagem precisa reportar aquele digest.
2. **Migrations só para a frente, e antes de o novo binário servir.** O runner não tem caminho de volta: `internal/platform/dbmigrate` expõe `Status`, `Up` e `CurrentVersion`, e o CLI só conhece `migrate status` e `migrate up` — `migrate down` responde "unknown migrate subcommand". O pipeline só chama `migrate up` com a própria imagem da release, então um *rollback* é do **processo**: o schema fica onde o passo *expand* o deixou, e é por isso que a expansão tem de ser compatível com a release que ela substitui (§6).
3. **Uma release que não se prova não é uma release.** Depois do *promote*, três coisas são medidas contra a superfície pública: a prontidão responde 200, **cada** caminho de smoke responde 200, e o contentor que está a correr reporta o digest que foi promovido. A terceira não é cerimônia: um *promote* que não recriou nada passaria por uma sonda que a release **antiga** respondeu, e o arquivo de estado passaria a registrar uma release que nenhum processo corre. Qualquer das três a falhar devolve a release anterior, prova essa release de novo e diz que não promoveu — e se a anterior também não se provar, o processo sai com o incidente na mão do operador em vez de fingir uma promoção.
4. **Nada é presumido do ambiente do operador.** O documento Compose, o arquivo de ambiente, o nome do projeto, o endereço público e o arquivo de estado são todos argumentos; `--smoke-path` e `--health-path` também. O estado (release corrente e anterior) fica ao lado do arquivo de ambiente, é do operador, e nunca entra no Git.

O que ele **nunca** faz: imprimir um segredo (nunca lê o arquivo de ambiente — entrega-o ao Compose), publicar porta, apagar volume, reiniciar o banco (`--no-deps`: um deploy substitui os processos que rodam a release, não a stack) ou tocar no banco além de `migrate up`. `status` não exige arquivo de ambiente e mostra os digests registrados ao lado do que o contentor reporta.

O que verifica isso — `make deploy-verify` (exige daemon Docker, como `image-verify`): registra as releases por digest num registry descartável, sobe **apenas** `db` e `caddy` (todo contentor que serve é criado pelo pipeline) e mede, em sequência: uma tag é recusada; um digest malformado é recusado; um deploy sem sonda de saúde é recusado; um documento que roda outro digest é recusado porque o artefato promovido não chegaria à aplicação; `migrate down` não existe; um deploy contra o banco parado é recusado **e o banco continua parado** (um pipeline que sobe um banco no caminho para produção é um pipeline que pode criar um banco vazio); o primeiro deploy aplica as 32 migrations, sobe a aplicação e serve `/login`; promover o mesmo digest de novo diz "nothing to promote" — e prova a release antes de o dizer; `rollback` sem release anterior é recusado; uma release mais nova é promovida e a rollback traz a anterior de volta, servindo de novo, com o schema **intacto**; um arquivo de estado que descreve uma release que ninguém corre é recusado antes do passo de migrate; e o contentor do banco nunca é substituído.

A release que *não* fica pronta é um **stand-in** (`tools/deployaudit/stub`), não uma cópia quebrada da aplicação: o artefato que se quer medido é a decisão do pipeline, e montar um binário quebrado para isso seria um artefato que ninguém revisa. O stand-in é promovido pelo mesmo caminho de uma release — registry, mesmo documento Compose, mesmo filesystem somente leitura, mesmas capacidades largadas, mesma sonda — e o modo que ele encarna vem **embutido na imagem**, não do arquivo de ambiente: uma release que não pode ficar pronta é uma release cuja própria configuração o diz, e a rollback tem de restaurar a anterior *saudável* enquanto essa configuração continua no arquivo do operador. São três modos, e os dois últimos são o que o gate usa como release que falha: `not-ready` (nunca responde 200 à prontidão), `page-missing` (responde à prontidão e perdeu a página) e `ready` (a release que é promovida e depois substituída pela rollback).

Medido: `make deploy-verify` exit 0; `go test ./tools/deployaudit/...` cobre o mapeamento modo/caminho do stand-in dentro de `make verify` (um modo desconhecido nunca alega prontidão); e **três falsificações pelo fio**, cada uma reprovando na asserção pretendida e restaurada por `diff` — remover a prova da promoção (“a release que nunca responde à prontidão foi promovida”), remover o passo de smoke (“um release cujas páginas não respondem foi promovida”) e registrar a release que falhou (“o arquivo de estado mudou depois de uma release que não foi promovida”).

### O primeiro administrador (P19-T09)

Uma instalação sobe sem administrador: nenhuma rota concede papel, e a superfície de moderação continua sem uma. O primeiro é promovido **da máquina**, por quem já detém o DSN, com o mesmo binário da release:

```bash
ARENA_DATABASE_URL=... arena admin bootstrap --email <address>
```

O comando exige uma conta existente, com endereço **verificado**, que consiga autenticar e que já tenha o segundo fator confirmado — o gate administrativo exige sessão que apresentou o fator, então promover uma conta sem matrícula criaria um administrador que não consegue agir. Ele recusa quando já existe atribuição ativa: é um *bootstrap*, não um caminho para conceder o papel a quem lê o DSN. Com terminal, o operador digita o endereço de volta para confirmar; num passo de automação sem terminal, `--yes` é a autorização explícita. A decisão e a escrita são um passo atômico (lock de tabela por transação), então dois comandos simultâneos produzem **um** primeiro administrador, e o evento de auditoria commita com a atribuição: `administration.role_granted` com motivo `administrative_bootstrap`.

`arena admin revoke --email <address>` é a volta: data a revogação e registra `administration.role_revoked` (`administrative_demotion`) na mesma trilha, devolvendo a instalação ao estado que o bootstrap exige — é o caminho de recuperação quando o único administrador perde o acesso. Nenhum dos dois subcomandos é alcançável por HTTP, e um teste reprova qualquer adaptador HTTP ou HTML que nomeie o caso de uso ([SECURITY.md](SECURITY.md) §3).

Deploy automático em produção continua **desativado**: o que existe é um pipeline que um operador roda à mão, e que só promove depois de a release se provar. A verificação completa de release passou a existir na P19-T08 ([CI.md](CI.md)): dez jobs, os gates da fase todos ligados a algum job, `tools/ciaudit` recusando o workflow que perde um deles. O bootstrap administrativo auditado existe desde a P19-T09 (§ acima). Antes de ligar promoção automática continuam pendentes: a regra de firewall que recusa a origem fora do Cloudflare (§3.1) e um coletor que leia o listener administrativo de fora do host — hoje `/metrics` só existe em loopback dentro de um contêiner distroless, então o alerta de 5xx de [RUNBOOKS.md](RUNBOOKS.md) não pode depender dele. E a superfície HTTP operacional de jobs (`GET /api/v1/admin/jobs/health`, `/dead`, `POST …/retry`) existe e tem teste desde a P15-T06, mas não está composta em `arena server` nem no contrato: o alerta de fila depende dessa fiação.

## 6. Estratégia de migrations

Usar expand/contract:

1. adicionar estrutura compatível;
2. publicar código que entende os dois formatos quando necessário;
3. migrar dados em job observável;
4. mudar leituras;
5. remover estrutura antiga em release posterior.

Migrations destrutivas exigem backup, estimativa de lock, janela e ADR quando material.

A estimativa de lock não é estimada: `make migration-audit` (P20-T03) aplica a história uma migration por vez enquanto outra sessão segura `ACCESS SHARE` — o lock de um `SELECT` — em todas as tabelas do schema, e registra em que relação cada migration esperou. As **doze** versões que esperam estão nomeadas uma a uma em [MIGRATION_AUDIT.md](MIGRATION_AUDIT.md) com o motivo, e são elas que precisam de janela; uma migration que passe a esperar sem estar na lista faz o portão recusar.

O mesmo portão prova as duas metades do expand/contract: um snapshot copiado **depois de cada versão** e rolado para o head tem de ser exatamente o banco que uma instalação nova produz, sem perder linha nem coluna que a versão do snapshot já tinha; e uma migration que falha pela metade — numa base que ainda tinha migrations reais pendentes — tem de não deixar tabela, linha nem registro de versão, com o banco continuando rolável para o head, porque `migrate down` não existe no runner: a volta é o processo e o schema segue em frente (§7).

## 7. Backup

WAL contínuo e base backup para storage externo compatível com S3 (P19-T04). O exercício `deploy/backup/verify.sh` — `ARENA_IMAGE=<tag> make backup-verify`, exige daemon Docker — é o gate: julga o `compose.production.yaml` commitado, sobe o servidor com os argumentos que o próprio arquivo declara, mede o arquivamento pelo `pg_stat_archiver`, envia um base backup selado, **destrói o primário**, restaura num cluster vazio até um instante escolhido e compara o que voltou com o que existia.

- **Arquivamento.** O comando do serviço `db` carrega `archive_mode=on`, `wal_level=replica`, `archive_command=/opt/backup/archive-wal.sh %p %f`, `archive_timeout=300` e `wal_keep_size=512MB`. `tools/backupctl check-compose` reprova o arquivo quando qualquer um deles se perde, porque um servidor sem arquivamento recicla WAL não escrito e um armazenamento que para de receber segmentos se parece exatamente com um banco quieto.
- **Objetos selados.** `base-backup.sh` roda `pg_basebackup -Ft -z -X none` e sobe dois objetos: o `.tar.gz.enc` e, por último, o manifesto que o completa (com o checksum do selado e o LSN inicial). O WAL vem do arquivo, não do tarball — `-X none` de propósito, para que uma lacuna no arquivamento seja visível no exercício e não escondida dentro do backup. A ordem das subidas faz de uma execução interrompida um objeto que nenhuma restauração seleciona.
- **Criptografia.** Cada objeto é selado com a chave montada em `/run/secrets/backup_key`, sempre arquivo e nunca variável de ambiente (chave em ambiente é chave em `docker inspect`). A ferramenta recusa chave legível além do dono e o exercício mede o resultado: baixa o objeto como o armazenamento o guarda e prova que a linha marcada do exercício não está naqueles bytes, enquanto está no primário.
- **Restauração e retenção.** `restore.sh` recupera até `recovery_target_time`, espera a promoção (`pg_is_in_recovery()` respondido pelo servidor, não inferido do log) e devolve o tempo gasto. `retention.sh` aplica janela e piso; `prune` só apaga com `--apply`, nunca remove o primeiro segmento de que o backup mantido mais antigo precisa e nunca toca num nome que não seja segmento de WAL.

O operador fornece três coisas que não entram no Git: `secrets/backup.key` (0600, ignorado pelo Git), o binário `backupctl` montado em `/opt/backup-tool/backupctl` (caminho em `BACKUP_BIN`; não fica sob `/opt/backup` porque um bind mount aninhado dentro de um bind mount somente leitura não sobe) e o endpoint, bucket e credencial do armazenamento (`BACKUP_S3_*`). Como `archive_command` roda como o usuário `postgres` dentro do contêiner, a chave **dele** é um arquivo próprio, dono `postgres`, modo 0600 — alargar o modo do arquivo do operador para o servidor ler seria entregar a chave a todo processo do host.

Medido no exercício: **RTO de 2 s** (perda do primário até um servidor aceitando escrita no alvo) e RPO limitado por `archive_timeout=300`. A meta de 15 minutos continua meta: a verificação em ambiente isolado é manual (`make backup-verify`) e um alerta de atraso ou falha do arquivamento é a P19-T06.

O exercício de desastre e carga do lançamento (`make disaster-drill`, P20-T05) mede a mesma recuperação com o dinheiro no meio: o dataset é criado pelos casos de uso do produto, a leitura financeira é tomada antes da perda e comparada depois da volta (carteira a carteira, digest a digest, agregado a agregado), o alvo não pode trazer as linhas escritas depois dele — inclusive uma conta inteira com INK —, e a aplicação sobe nos dados restaurados para responder as jornadas. Os números que ele mediu, e a prova de que nada foi criado ou perdido, estão em [DISASTER_DRILL.md](DISASTER_DRILL.md), que o próprio exercício escreve e `drillaudit check` julga: um RPO acima do teto do servidor ou da meta, um RTO acima da meta, um limiar de carga sem valor medido ou um provedor exercitado numa direção só reprovam o documento.

Antes do primeiro usuário pagante, continuam pendentes:

- exercício de restauração em agenda (hoje ele roda sob demanda);
- alerta para atraso ou falha do arquivamento;
- armazenamento durável fora da VPS;
- retenção publicada como política, com o expurgo em agenda.

Meta inicial: RPO de até 15 minutos e RTO de até 4 horas. A meta só pode ser publicada como garantia depois de exercícios reais. Backup na mesma VPS não conta como backup.

## 8. Observabilidade

- health endpoints distintos para liveness e readiness;
- logs JSON com rotação no host;
- Sentry para erros de aplicação;
- métricas de CPU, RAM, disco, inode, conexões e latência PostgreSQL;
- alertas para disco, falha de backup, erros 5xx, fila atrasada e webhook falhando;
- PostHog separado de logs operacionais e sem PII.

### Telemetria no processo (P19-T05)

Sentry e PostHog são **opcionais** e configurados por variável; sem elas o
processo serve o produto e não registra nada. Sentry e PostHog são alcançados
pelas suas APIs HTTP atrás de ports (`internal/platform/observability`), sem
SDK de fornecedor no processo.

| Variável | Papel |
|---|---|
| `ARENA_SENTRY_DSN` | credencial do repórter de erros (segredo; formato `https://<key>@<host>/<project>`) |
| `ARENA_POSTHOG_API_KEY` | chave de escrita do projeto de analytics (segredo; prefixo `phc_`) |
| `ARENA_POSTHOG_HOST` | origem da API de analytics (padrão: nuvem US; `https`, exceto loopback local) |
| `ARENA_ANALYTICS_SAMPLE_RATE` | percentual determinístico de amostragem de analytics, 0–100 (padrão 100) |

Regras que o adapter garante, por construção e não por lista de bloqueio: os
eventos são **allowlisted** em código (nome e propriedades admitidas), o que
trafega é apenas um locale validado e um inteiro limitado, e o relatório de
erro carrega só a mensagem e tags operacionais — **nunca** email, corpo de
mensagem, token ou payload de provedor (ex.: Stripe). Analytics **nunca
bloqueia** uma requisição: cada sink tem fila limitada e uma fila cheia
descarta e conta o descarte.

As métricas **nunca são enviadas a um provedor**: o registry é renderizado em
texto Prometheus e servido apenas no **listener administrativo**
(`ARENA_ADMIN_ADDR`), junto de `/debug/pprof/`:

- `GET /metrics` — RED dos requests (`http_requests_total`,
  `http_request_duration_seconds`, `http_requests_in_flight`) e USE do pool
  (`db_pool_*`) e da fila (`jobs_queue`, `jobs_lag_seconds`,
  `jobs_oldest_dead_seconds`);
- a leitura da fila é **uma query por scrape** (cacheada), limitada no tempo, e
  uma leitura recusada é contada em `jobs_health_scrape_errors_total`.

O listener administrativo vive só em loopback e não é montado no endereço
público; sem `ARENA_ADMIN_ADDR` não há `/metrics` exposto. Os alertas iniciais
para estes sinais, com threshold e ação, estão em [RUNBOOKS.md](RUNBOOKS.md).

Adicionar uma stack própria de métricas só quando a solução do provedor deixar lacuna mensurável.

## 9. Cache

Ponto de partida a validar:

- assets com hash: um ano, `immutable`;
- o grafo de módulos ESM também sob o caminho estável (`/assets/pages/auth.js` importa `./submission.js`): o hash não serve para imports relativos, então o caminho estável é servido com revalidação curta (`no-cache`) em vez de imutabilidade; a entrada referenciada pelo HTML é sempre a com hash;
- home e categorias: aproximadamente 10–30 segundos no edge;
- Arena e fragmentos públicos: aproximadamente 30 segundos no edge;
- exportações públicas: TTL curto com ETag;
- rotas privadas, wallet, auth, checkout e admin: `no-store`.

TTL e purge precisam de teste de conteúdo removido. Nenhum número é promessa antes de observar tráfego real.

## 10. Capacidade e escala

Não publicar estimativa de usuários suportados sem teste. k6 mede cenários separados:

- leitura pública com cache frio e quente;
- login e sessão;
- confirmação de posição;
- publicação de argumento com débito;
- webhook repetido;
- Arena viral com maioria de leitores.

Registrar p50, p95, p99, erro, CPU, memória, I/O, conexões, locks e cache hit. A próxima compra de infraestrutura deve responder a um gargalo observado.
