# Ensaio de entrega com três réplicas

Usa as imagens e migrations do Compose, autenticação real e publicação SQS por `SendMessageBatch`. Execute em uma stack descartável: o teste cria 50 carteiras, envia 310 mensagens (incluindo 10 inválidas destinadas à DLQ) e consome os eventos publicados para conferir seus IDs. A fila de entrada, a DLQ e o banco devem começar vazios; rode o Newman depois deste ensaio.

```bash
export POSTGRES_PORT=15432 KEYCLOAK_PORT=18180
export KEYCLOAK_PUBLIC_URL=http://localhost:18180
export MINISTACK_PORT=14566 APP_PORT_RANGE=18080-18082

docker compose -p wallet-validation up --build -d --wait

export COMPOSE_DATABASE_URL='postgres://wallet_app:wallet_app@localhost:15432/wallet?sslmode=disable'
export COMPOSE_SQS_ENDPOINT=http://localhost:14566
export COMPOSE_KEYCLOAK_URL=http://localhost:18180
export COMPOSE_API_URLS=http://localhost:18080,http://localhost:18081,http://localhost:18082
export COMPOSE_STATE_FILE=/tmp/wallet-validation-state.json

go test -count=1 -race -tags compose -timeout 5m -run '^TestComposeBatch$' -v ./test/compose

docker compose -p wallet-validation restart api
docker compose -p wallet-validation up -d --wait
COMPOSE_VERIFY_RESTART=true go test -count=1 -race -tags compose -timeout 5m -run '^TestComposeRestart$' -v ./test/compose

docker compose -p wallet-validation down -v
```

São 400 transações de domínio, 300 entradas de ledger e 750 eventos da outbox. Cada carteira termina em `1040.00`, após BET, WIN, LOSS, REFUND, ROLLBACK antecipado e rejeição por saldo. Replays HTTP e SQS usam IDs distintos no transporte para realmente atravessar a deduplicação do FIFO e exercitar a idempotência persistida. Todas as carteiras são reconciliadas e os eventos confirmados no banco precisam chegar à fila.

A segunda fase lê o arquivo de estado da primeira e verifica os mesmos saldos, ledgers e replays após substituir todos os processos da API. O teste não executa comandos Docker sozinho. Os defaults são exclusivos do ambiente local de desenvolvimento; configure as URLs acima se usar outras portas.
