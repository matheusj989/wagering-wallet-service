# Contratos de mensageria

Fechado em 21/09/2026 a partir do README (§10 e §11) e do plano (§3.4, §3.5, §9.3, §9.6). Normativo: specs `sqs-consumer` e `outbox-publishing`.

## 1. Filas

| Fila | Tipo | Papel | Atributos |
| --- | --- | --- | --- |
| `wager-transactions.fifo` | FIFO | Entrada de operações dos provedores | `VisibilityTimeout = 30`, `RedrivePolicy = {deadLetterTargetArn: <dlq>, maxReceiveCount: 5}`, `ContentBasedDeduplication = false` |
| `wager-transactions-dlq.fifo` | FIFO | Mensagens inválidas, conflitos e tentativas esgotadas | — |
| `wallet-events.fifo` | FIFO | Destino dos eventos de integração publicados pela outbox | `ContentBasedDeduplication = false` |

Criadas pelo script `deployments/docker/ministack/init-queues.sh` (hook `ready.d` do MiniStack) e, nos testes, pelo helper `testenv` com nomes únicos por teste (sufixo, mantendo `.fifo`). Em produção o mesmo script serve de documentação do provisionamento.

Política de acesso (documentada e aplicada com `SetQueueAttributes Policy`, mesmo que o emulador não a imponha): só o principal de ingestão dos provedores pode `sqs:SendMessage` em `wager-transactions.fifo`; só o principal do wallet-service pode `sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:ChangeMessageVisibility`, `sqs:GetQueueAttributes` nela e `sqs:SendMessage` na DLQ e em `wallet-events.fifo`. A fronteira de confiança é um serviço **interno de ingestão**: ele autentica o provedor a montante e define `providerId` com base nessa identidade; provedores não possuem credenciais diretas da fila. IAM autentica/autoriza o produtor, mas não valida o `providerId` dentro do JSON. O consumidor confia nessa responsabilidade da ingestão e executa as validações de domínio. Conforme revisão aprovada, esta premissa fica documentada; implementar ingestão, gateway ou assinatura de mensagem está fora deste change. Em MiniStack, verificar a presença da política não prova sua imposição.

Credenciais locais: `AWS_ACCESS_KEY_ID=test`, `AWS_SECRET_ACCESS_KEY=test`, `AWS_REGION=us-east-1`, `SQS_ENDPOINT=http://localhost:4566`.

## 2. Mensagem de entrada (`wager-transactions.fifo`)

Corpo válido conforme as regras por tipo (BET sem referência; o campo é opcional em WIN e obrigatório em REFUND/ROLLBACK):

```json
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}
```

Regras para o produtor:

| Item | Valor |
| --- | --- |
| `MessageGroupId` | `walletId`. Ordem por carteira e paralelismo entre carteiras; o FIFO entrega no máximo um grupo por consumidor de cada vez |
| `MessageDeduplicationId` | `hexLower(SHA256(UTF8(messageId)))`, 64 caracteres ASCII. A deduplicação do FIFO (janela de 5 min) é conveniência; a correção vem da inbox e da idempotência |
| Atributo `correlationId` (String) | Opcional. Ausente → gerado (UUID v7) |
| `messageId` | Identidade durável da mensagem no consumidor. Texto opaco, 1 a 255 caracteres, sem controles/espaços nas pontas; único na fila entre provedores, garantido pela ingestão |
| `type` | `WagerTransactionRequested`. Outro valor → inválida |
| `occurredAt` | RFC 3339. Metadado; não entra no hash nem na decisão |
| `data` | Mesmos campos e validações do `POST /wagering/transactions` (ver `http-contract.md` §1 e §3.5), mais `idempotencyKey` (obrigatório, 1 a 255 caracteres). Campos desconhecidos → inválida |

## 3. Política do consumidor

| Parâmetro | Valor local | Variável |
| --- | --- | --- |
| Mensagens em processamento por instância | 4 | `SQS_CONSUMER_WORKERS`, teto do semáforo do consumidor |
| `MaxNumberOfMessages` por `ReceiveMessage` | 1 (fixo). Garante no máximo uma mensagem por carteira em processamento no cluster e preserva a ordem do grupo em retentativas | — |
| `WaitTimeSeconds` (long polling) | 20 | `SQS_WAIT_TIME` |
| Visibility timeout | 30 s | `SQS_VISIBILITY_TIMEOUT` |
| Prazo de processamento por mensagem (context) | 10 s | `SQS_MESSAGE_DEADLINE` |
| Backoff em falha transitória (`ChangeMessageVisibility`) | `min(5s × 2^(receiveCount-1), 5min)`: 5 s, 10 s, 20 s, 40 s | `SQS_RETRY_BACKOFF_BASE`, `SQS_RETRY_BACKOFF_MAX` |
| `maxReceiveCount` | 5 (depois o redrive move para a DLQ) | criação da fila |
| Nome do consumidor na inbox | `wager-transactions-consumer` | — |

Tratamento por desfecho:

| Desfecho | Ação |
| --- | --- |
| Uso de caso concluiu (`PROCESSED`, `REJECTED`, `PENDING_REFERENCE`) ou devolveu replay | Inbox gravada na mesma transação SQL; `DeleteMessage` **depois da confirmação durável** do COMMIT |
| Reentrega de `messageId` já concluído (inbox devolve 0 linhas) com o mesmo hash | Não reprocessa; `DeleteMessage`; métrica `sqs_redeliveries_total` |
| Reentrega com hash diferente | Inválida: DLQ com `reason = INBOX_HASH_MISMATCH` |
| Mensagem inválida (JSON quebrado, `type` desconhecido, schema, `kind = OPENING`, dinheiro fora da forma canônica) | `SendMessage` para a DLQ com o corpo original e atributos `reason`, `originalMessageId`, `receiveCount`; somente após confirmação do envio, `DeleteMessage` da original; métrica `sqs_dlq_total{reason}` |
| Carteira inexistente | DLQ com `reason = WALLET_NOT_FOUND` |
| Conflito de idempotência (mesma chave, hash diferente; ou ID externo reaplicado com outra chave) | Erro permanente: DLQ com `reason = IDEMPOTENCY_KEY_CONFLICT` ou `EXTERNAL_TRANSACTION_ID_CONFLICT`; nenhuma nova operação persistida, e os vínculos existentes são preservados |
| Falha transitória (conexão, `lock_timeout`, `40001`, `40P01`, throttling do SQS, prazo estourado) | Não apaga; `ChangeMessageVisibility` com o backoff acima; na 5ª entrega sem sucesso o SQS redireciona para a DLQ (métrica `sqs_dlq_total{reason=RETRIES_EXHAUSTED}` quando `receiveCount = maxReceiveCount`) |
| COMMIT enviado sem confirmação conclusiva | Não apaga nem afirma rollback; na reentrega, inbox/resultado persistido evitam duplicidade ou permitem processar se a tentativa abortou |
| Erro inesperado (não classificado) | Tratado como transitório: o `maxReceiveCount` é o teto |

Se o processo morrer entre o COMMIT e o `DeleteMessage`, a mensagem volta após o visibility timeout e cai no caso "reentrega com o mesmo hash". Duplicata na DLQ (morte entre envio e delete) é inofensiva. Na DLQ usar `hexLower(SHA256(JSONCanônico(["wallet-dlq-v1", sourceQueueArn, sqsMessageId, reason])))`, 64 caracteres ASCII; JSON UTF-8 sem espaços, escape HTML ou newline. O `sqsMessageId` é o `MessageId` do broker, não o `messageId` do envelope. `originalMessageId` carrega esse ID do broker; `MessageGroupId` vem do atributo de sistema recebido na mensagem original, solicitado no ReceiveMessage. Assim JSON quebrado, ID ausente, Unicode ou ID de negócio de 255 caracteres não impedem envio à DLQ. Mesmo broker ID/fila/reason produzem mesmo hash em retry; corpo bruto é preservado. Falha/timeout sem confirmação do envio à DLQ nunca autoriza apagar a original.

`SIGTERM`: o consumidor para de chamar `ReceiveMessage`, conclui a mensagem em andamento dentro de `SHUTDOWN_TIMEOUT` (15 s) e, se não conseguir, faz `ChangeMessageVisibility(0)` para reentrega imediata por outra instância.

## 4. Eventos de saída (`wallet-events.fifo`)

| Item | Valor |
| --- | --- |
| `MessageGroupId` | `walletId` do evento (`partition_key` da outbox) |
| `MessageDeduplicationId` | `eventId` |
| Atributos da mensagem | `eventType` (String), `eventVersion` (Number), `aggregateType` (String: `wallet` ou `wager_transaction`), `correlationId` (String) |
| Corpo | O envelope abaixo, snapshot imutável gravado na outbox no mesmo commit do saldo |
| Ordem | Dentro de uma carteira os eventos são publicados na ordem de gravação por um mesmo publisher, mas com vários publishers a ordem não é garantida. O consumidor ordena por `walletVersion` (em `WalletBalanceChanged`) e por `occurredAt`; detecta buraco pela sequência de `walletVersion` |
| Duplicidade | Republicação após falha entre publicar e marcar `published_at` reenvia o mesmo `eventId`. Dentro de 5 min o FIFO deduplica; depois disso o consumidor descarta pelo `eventId` |

Envelope:

```json
{
  "eventId": "0192f2c0-0000-7000-8000-00000000000a",
  "eventType": "WalletBalanceChanged",
  "aggregateId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "correlationId": "0192f2b0-0000-7000-8000-000000000009",
  "causationId": "0192f298-345e-7e38-af88-e43f851a819d",
  "occurredAt": "2026-09-08T12:00:05.000Z",
  "version": 1,
  "data": { }
}
```

`eventId` é UUID v7 gerado ao construir o evento e estável entre republicações. `aggregateId` é a carteira em `WalletBalanceChanged` e a transação nos outros três. `causationId` é sempre o `transactionId` que originou o evento. `correlationId` é o da requisição ou mensagem de entrada. Tipo e versão são definidos pelo construtor de cada evento (versão inicial 1).

### 4.1 `WagerTransactionProcessed` (v1) — `aggregateId = transactionId`

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" },
  "balance": { "amount": "975.00", "currency": "BRL" },
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "referenceExternalTransactionId": null,
  "referenceTransactionId": null,
  "processedAt": "2026-09-08T12:00:05.000Z"
}
```

`balance` é o saldo depois da operação (em `LOSS`, o saldo inalterado). Para `OPENING` (origem interna) os campos `providerId`, `externalTransactionId`, `roundId`, `gameId` e as referências são omitidos.

### 4.2 `WagerTransactionRejected` (v1) — `aggregateId = transactionId`

```json
{
  "transactionId": "0192f2a5-0000-7000-8000-000000000002",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "kind": "BET",
  "money": { "amount": "80.00", "currency": "BRL" },
  "failureCode": "INSUFFICIENT_FUNDS",
  "balance": { "amount": "20.00", "currency": "BRL" },
  "providerId": "provider-a",
  "externalTransactionId": "transaction-124",
  "roundId": "round-988",
  "gameId": "fortune-chimp",
  "referenceExternalTransactionId": null,
  "rejectedAt": "2026-09-08T12:00:06.000Z"
}
```

Emitido em toda rejeição definitiva de negócio, inclusive `BALANCE_LIMIT_EXCEEDED` e as decididas pelo worker de referências. `balance` sempre usa a moeda da carteira, inclusive em `CURRENCY_MISMATCH`. `FAILED` não gera evento (fica em log, métrica e consulta).

### 4.3 `WalletBalanceChanged` (v1) — `aggregateId = walletId`

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "kind": "BET",
  "direction": "DEBIT",
  "money": { "amount": "25.00", "currency": "BRL" },
  "balanceBefore": { "amount": "1000.00", "currency": "BRL" },
  "balanceAfter": { "amount": "975.00", "currency": "BRL" },
  "walletVersion": 2
}
```

Um por lançamento no ledger. `walletVersion` é a versão da carteira depois do lançamento (1 na abertura).

### 4.4 `WagerTransactionPendingReference` (v1) — `aggregateId = transactionId`

```json
{
  "transactionId": "0192f2a6-0000-7000-8000-000000000003",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "kind": "ROLLBACK",
  "money": { "amount": "25.00", "currency": "BRL" },
  "providerId": "provider-a",
  "externalTransactionId": "rollback-123",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "referenceExternalTransactionId": "transaction-123",
  "nextAttemptAt": "2026-09-08T12:00:04.100Z",
  "deadlineAt": "2026-09-08T12:15:03.100Z"
}
```

Emitido uma vez, ao registrar a pendência (não a cada retentativa).

### 4.5 Eventos por desfecho

| Desfecho | Eventos gravados na outbox (mesmo commit) |
| --- | --- |
| `BET`, `WIN`, `REFUND`, `ROLLBACK` processados | `WagerTransactionProcessed` + `WalletBalanceChanged` |
| `LOSS` processado | `WagerTransactionProcessed` |
| Rejeição definitiva (`REJECTED`) | `WagerTransactionRejected` |
| Reversão aguardando referência | `WagerTransactionPendingReference` |
| Abertura de carteira com saldo positivo | `WagerTransactionProcessed` (kind `OPENING`) + `WalletBalanceChanged` |
| Abertura com saldo zero, replay, `FAILED` | nenhum |

## 5. Publisher da outbox

| Parâmetro | Valor local | Variável |
| --- | --- | --- |
| Habilitado por instância | sim | `OUTBOX_PUBLISHER_ENABLED` (`false` só para o cenário de teste "morte entre commit e publicação") |
| Intervalo de varredura | 500 ms | `OUTBOX_POLL_INTERVAL` |
| Lote por claim | 50 | `OUTBOX_BATCH_SIZE` |
| Lease | 30 s | `OUTBOX_LEASE` |
| Backoff em falha de publicação | `min(1s × 2^attempts, 5min)` | `OUTBOX_BACKOFF_BASE`, `OUTBOX_BACKOFF_MAX` |
| Limite de tentativas | nenhum; a métrica `outbox_oldest_pending_age_seconds` denuncia atraso | — |

Ciclo: transação curta de claim (`UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED LIMIT n) RETURNING *`, com `locked_by = INSTANCE_ID`, `locked_until = now() + lease`, `attempts + 1`), COMMIT, `SendMessage` fora de transação para cada evento, depois `UPDATE` marcando `published_at` (sucesso) ou `next_attempt_at`/`last_error` (falha) e liberando o lease **somente se o recibo do claim continuar válido**. Recibo: `{id, lockedBy, generation}`, com generation igual ao `attempts` incrementado. Ambas as finalizações exigem id/owner/geração correspondentes, `published_at IS NULL` e `locked_until > clock_timestamp()`. Zero linhas significa claim obsoleto e não modifica nada. Lease vencido é reivindicável por outra geração, inclusive com o mesmo INSTANCE_ID; finalização tardia de A nunca sobrescreve B. Não iniciar envio após expiração; contexto da chamada SQS limitado ao lease restante. Publicação duplicada continua possível e usa o mesmo eventId.

Os IDs de transporte respeitam o [limite de SendMessage no SQS](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html); o hash adapta IDs opacos sem reduzir o contrato de negócio.
