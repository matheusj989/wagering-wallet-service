# wagering-wallet-service

Serviço em Go que movimenta carteiras de jogadores a partir de operações de provedores de jogos, por HTTP e por SQS FIFO, com as mesmas garantias nas duas portas: idempotência persistente, ledger append-only, eventos publicados só depois do commit e saldo correto com várias instâncias ao mesmo tempo.

Decisões de arquitetura: [ARCHITECTURE.md](ARCHITECTURE.md). Contratos completos: [documentation/api/](documentation/api/).

## Pré-requisitos

- Docker e Docker Compose v2 ou superior
- Go 1.27 (só para rodar os testes fora do container)
- Para os testes de integração: toolchain C (`build-essential` ou equivalente), porque os processos do serviço são compilados com o detector de corridas

## Subir o ambiente

```bash
docker compose up --build
```

Sobe PostgreSQL 18.6, Keycloak 26.7, MiniStack (emulador de SQS), aplica as migrations num serviço one-shot e depois sobe três réplicas da aplicação em `8080`, `8081` e `8082`. As filas `wager-transactions.fifo`, `wager-transactions-dlq.fifo` (com redrive) e `wallet-events.fifo` são criadas no boot do MiniStack por `deployments/docker/ministack/init-queues.sh`.

Portas: API `8080-8082`, Keycloak `8180`, MiniStack `4566`, PostgreSQL `5432`. Todas vêm de variáveis com padrão, então `POSTGRES_PORT=0 docker compose up` usa portas efêmeras.

Para derrubar tudo, inclusive o volume do banco:

```bash
docker compose down -v
```

## Variáveis de ambiente

Estão todas em [.env.example](.env.example), com os valores locais. Os secrets ali são de desenvolvimento e não servem para mais nada.

Todas elas chegam ao container: [deployments/app/app.yml](deployments/app/app.yml) repassa cada uma com o mesmo padrão, então `HTTP_WRITE_CONCURRENCY=6 docker compose up` ajusta o serviço sem tocar em código. Só o endereço HTTP e os endpoints de rede ficam fixos ali, porque apontam para os nomes dos serviços do Compose. Um teste compara o que o serviço lê com o que esses dois arquivos declaram, então variável nova sem lugar para configurar quebra a suíte.

Vale destacar três:

| Variável | Para quê |
| --- | --- |
| `OIDC_ISSUER` | Issuer público e fixo do Keycloak. É o que vem dentro do token |
| `OIDC_DISCOVERY_URL` / `OIDC_JWKS_URL` | Onde a aplicação busca o discovery e as chaves. Dentro do Compose apontam para `keycloak:8080`, que é outra rede que o issuer |
| `DB_POOL_MAX_CONNS` / `DB_POOL_RESERVED` | Conexões por instância e a folga fora do orçamento dos componentes. Duas contas precisam fechar, e as duas saem destas variáveis |

### As duas contas do pool

Para fora, `réplicas × DB_POOL_MAX_CONNS ≤ max_connections` do servidor: 3 × 20 = 60, e o Postgres sobe com 200.

Para dentro, o que a instância pode segurar ao mesmo tempo não pode passar do próprio pool:

```
HTTP_WRITE_CONCURRENCY + SQS_CONSUMER_WORKERS + um por job ligado + DB_POOL_RESERVED ≤ DB_POOL_MAX_CONNS
        12             +          4           +            2            +      2      ≤       20
```

Componente desligado devolve a sua fatia. A conta é calculada das próprias variáveis e conferida no start: se não fechar, o processo recusa subir dizendo a soma, em vez de virar espera no pool depois. O que sobe registra a divisão na linha `database pool ready`.

## Migrations

Aplicadas automaticamente pelo serviço `migrate` antes das réplicas subirem. Para reverter a última:

```bash
docker compose run --rm migrate down 1
```

Os arquivos ficam em [migrations/](migrations/) e rodam inteiros num único `Exec`, com `BEGIN`/`COMMIT` no próprio arquivo.

## Fluxo autenticado de exemplo

Todo endpoint de negócio exige um token do Keycloak (`client_credentials`). Os clients de desenvolvimento são `provider-a`, `provider-b`, `provider-c-short-lived` e `wallet-internal`.

```bash
token() {
  curl -s -X POST http://localhost:8180/realms/wallet/protocol/openid-connect/token \
    -d grant_type=client_credentials -d "client_id=$1" -d "client_secret=$2" \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])'
}
INTERNAL=$(token wallet-internal wallet-internal-secret)
PROVIDER=$(token provider-a provider-a-secret)
PLAYER=$(uuidgen)
```

Abrir a carteira (só o serviço interno pode):

```bash
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
```

Guarde o `id` devolvido em `WALLET` e envie uma aposta:

```bash
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-1' \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"transaction-1\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
```

Repetir a mesma chamada devolve `201` com `idempotentReplay: true` e o saldo observado na primeira vez. Repetir a mesma chave com outro valor devolve `409`. Para desfazer, mande um `ROLLBACK` com `referenceExternalTransactionId: "transaction-1"`.

A coleção do Postman em [documentation/postman/](documentation/postman/) cobre esse fluxo e os casos de erro, com os tokens obtidos automaticamente.

## Testes

Suíte rápida, sem Docker. Cobre o domínio, os casos de uso com mocks e a borda HTTP:

```bash
go vet ./...
go test -race ./...
```

Os mocks vêm do `mockgen`, estão versionados e o `mockgen` é declarado como ferramenta do módulo, então não precisa instalar nada. Para regerar depois de mudar uma interface:

```bash
go generate ./internal/mocks/
```

Integração com PostgreSQL, Keycloak e MiniStack reais em container:

```bash
go test -race -tags integration -timeout 35m ./test/...
```

O helper `test/testenv` sobe a infraestrutura uma vez por execução a partir dos mesmos YAMLs do Compose, com portas efêmeras. Cada teste recebe um banco criado do template migrado, filas FIFO próprias e tokens reais. Cenários com várias instâncias chamam `env.StartApp(3)`, que sobe processos de verdade do binário.

Os processos filhos são compilados com `CGO_ENABLED=1 go build -race -tags failpoints` e executados com `GORACE="halt_on_error=1 exitcode=66"`. O helper acompanha a saída dos dois streams e falha o teste se aparecer `DATA RACE` ou saída 66, inclusive nos cenários que esperam morte por failpoint.

O ensaio externo com 50 carteiras, lotes e reinício das três réplicas está em [test/compose/README.md](test/compose/README.md). Os resultados da revisão final estão em [documentation/validation-2026-09-21.md](documentation/validation-2026-09-21.md).

Para rodar um cenário específico:

```bash
go test -race -tags integration -run TestConcurrency -v ./test/integration/
```

### Build tags

| Tag | Efeito |
| --- | --- |
| `compose` | Executa o ensaio de lotes em `test/compose` contra um Compose já iniciado; configuração em [test/compose/README.md](test/compose/README.md) |
| `integration` | Compila `test/testenv` e `test/integration`. Sem ela, `go test ./...` não toca em Docker |
| `failpoints` | Liga `failpoint.Hit`, que mata o processo no ponto indicado por `FAILPOINT`. Sem a tag vira no-op e o binário de entrega não carrega esse comportamento |

Três failpoints existem, cada um matando o processo no ponto em que uma falha dói mais. O que cada um prova está documentado em [internal/infrastructure/failpoint](internal/infrastructure/failpoint/failpoint.go):

| Failpoint | Onde |
| --- | --- |
| `consumer.after_commit_before_delete` | consumidor da fila, entre o commit e o delete da mensagem |
| `outbox.after_publish_before_mark` | publisher da outbox, entre o envio e a marcação |
| `wagering.after_pending_reference_commit` | handler HTTP, depois de gravar a pendência |

## Estrutura

```
cmd/api                            ponto de entrada, fx.New(...).Run()
internal/domain/                   entidades, regras e os contratos de repositório
  money wallet wagering event messaging repositories
internal/application/              o que o serviço faz, sem saber como
  usecase                          um caso de uso por arquivo, cada um com sua interface
  dto                              entradas e saídas dos casos de uso
  dto/message                      contrato das mensagens da fila
  dto/validation                   regras de validação e o erro por campo
  mapper                           DTO para objeto de domínio, um mapper por arquivo
  port                             relógio, gerador de id, notificador e métricas
  idempotency                      JSON canônico e SHA-256
internal/infrastructure/           como o serviço faz
  postgres                         client e unidade de trabalho
  postgres/repositories            um repositório SQL por arquivo
  sqs                              client e consumidor, que roteia por fila
  sqs/listener                     um listener por fila, cada um dizendo o que observa
  sqs/publisher                    um publicador por destino
  http auth jobs observability config logging failpoint system
internal/mocks                     mocks gerados a partir dos contratos
migrations                         schema versionado
deployments                        um YAML por serviço e o Dockerfile da aplicação
test/testenv test/integration      infraestrutura e cenários ponta a ponta
documentation                      contratos, plano histórico e Postman
```

## Observabilidade

- `GET /health/live` e `GET /health/ready` são públicos. O readiness checa PostgreSQL e SQS com prazo de 2 s cada
- `GET /metrics` expõe o formato do Prometheus na mesma porta
- Logs em JSON com `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`, `instanceId` e `component`. Token, secret, corpo de requisição e valores monetários nunca vão para o log
- `info` marca o início e o fim de cada processo de negócio, `debug` os passos intermediários (decisão tomada, saldo movido, referência ainda ausente) e `warn`/`error` as recusas e as falhas. Em `LOG_LEVEL=info` a linha de abertura e a de encerramento bastam para seguir uma operação pelo `correlationId`
