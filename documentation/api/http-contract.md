# Contrato HTTP

Referência de uso do contrato HTTP (exemplos e tabelas), derivada do README do desafio (§9). Alimenta a coleção do Postman.

## 1. Convenções

| Tema | Regra |
| --- | --- |
| Formato | JSON UTF-8. Requisições com corpo exigem `Content-Type: application/json`. Campos desconhecidos no corpo são rejeitados (400) |
| Dinheiro | `{"amount":"25.00","currency":"BRL"}`. `amount` é string na forma canônica `^(0\|[1-9][0-9]{0,16})\.[0-9]{2}$` (sem sinal, sem zeros à esquerda, exatamente duas casas). Número JSON, `"25"`, `"25.0"`, `"025.00"`, `"-1.00"`, `"1e2"`, `"NaN"` → 400. Limite: `92233720368547758.07` (int64 em centavos). `currency` somente `BRL` ou `USD` (duas casas); outras moedas, inclusive `ABC`/`JPY`, são inválidas |
| Identificadores nossos | UUID em minúsculas. Na entrada aceita-se qualquer UUID RFC 4122 (maiúsculas normalizadas para minúsculas). Os gerados são UUID v7 |
| Identificadores do provedor | `providerId`, `externalTransactionId`, `roundId`, `gameId`, `referenceExternalTransactionId` e o header `Idempotency-Key`: texto opaco de 1 a 255 caracteres, sem caracteres de controle, sem espaço no início ou no fim. Comparação exata (case-sensitive) |
| `kind` | Exatamente um de `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` (case-sensitive). `OPENING` é rejeitado com 400 |
| Timestamps | RFC 3339 em UTC com milissegundos: `2026-09-08T12:00:00.000Z` |
| Correlação | Header `X-Correlation-Id` opcional na requisição (1 a 128 caracteres ASCII imprimíveis). A resposta sempre devolve `X-Correlation-Id` (ecoado ou gerado como UUID v7). O mesmo valor vai para logs e para o `correlationId` dos eventos |
| Autenticação | `Authorization: Bearer <access_token>` emitido pelo Keycloak (`client_credentials`). Ver §2 |
| Tamanho | Corpo acima de 64 KiB → 413 |
| Erros de validação/transporte | `application/problem+json` (RFC 9457) com `code` estável. Ver §5; commit incerto não permite afirmar ausência de persistência |
| Resultados persistidos | Sempre JSON com `transactionId` e `status`, mesmo quando o código é 422 |

Distinção exigida pelo README ("entrada inválida, conflito, rejeição de negócio, processamento pendente e indisponibilidade transitória"): 400 / 409 / 422 / 202 / 503, nessa ordem.

## 2. Autorização

Papéis do realm `wallet` do Keycloak: `wagering-provider` (clients `provider-a`, `provider-b`, `provider-c-short-lived`) e `wallet-internal` (client `wallet-internal`). O `providerId` autorizado vem do claim `provider_id` do token, não do `client_id`.

| Endpoint | `wagering-provider` | `wallet-internal` | Anônimo |
| --- | --- | --- | --- |
| `POST /wallets` | 403 | ok | 401 |
| `GET /wallets/{walletId}` | 403 | ok | 401 |
| `GET /wallets/{walletId}/ledger` | 403 | ok | 401 |
| `POST /wallets/{walletId}/reconciliation` | 403 | ok | 401 |
| `POST /wagering/transactions` | ok se `providerId` do corpo = claim, senão 403 | 403 | 401 |
| `GET /wagering/transactions/{transactionId}` | ok se a transação é do provedor do token; de outro provedor ou interna (`OPENING`) → 404 | ok, qualquer transação | 401 |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | ok se `{providerId}` = claim, senão 403 | ok, qualquer provedor | 401 |
| `GET /health/live`, `GET /health/ready`, `GET /metrics` | público | público | público |

401 cobre: header ausente ou malformado, assinatura inválida, `iss` ou `aud` diferentes do configurado, token expirado. 403 cobre: token válido sem o papel exigido, ou, para papel de provedor, `providerId` (corpo ou path) diferente do claim. Acesso negado nunca tem efeito financeiro nem revela existência de dados (por isso 404 para transação de outro provedor).

## 3. Endpoints

### 3.1 `POST /wallets` — abertura de carteira (interno)

Requisição:

```json
{
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": { "amount": "1000.00", "currency": "BRL" }
}
```

`201 Created`:

```json
{
  "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1,
  "createdAt": "2026-09-08T12:00:00.000Z",
  "updatedAt": "2026-09-08T12:00:00.000Z"
}
```

Efeitos com saldo positivo, no mesmo commit: carteira (versão 1), transação `OPENING` em `PROCESSED`, lançamento `CREDIT` (`balanceBefore 0.00`, `walletVersion 1`), eventos `WagerTransactionProcessed` e `WalletBalanceChanged` na outbox. Saldo `0.00`: só a carteira.

| Código | `code` | Quando |
| --- | --- | --- |
| 409 | `WALLET_ALREADY_EXISTS` | Já existe carteira para `(playerId, currency)`. O problem inclui `walletId` da existente |
| 400 | `VALIDATION_FAILED` | Corpo inválido, `initialBalance` fora da forma canônica |

### 3.2 `GET /wallets/{walletId}` (interno)

`200 OK` com o mesmo corpo da abertura (estado atual). `404 WALLET_NOT_FOUND`. `400 VALIDATION_FAILED` se `walletId` não for UUID.

### 3.3 `GET /wallets/{walletId}/ledger?cursor=&limit=` (interno)

Ordenação estável e ascendente por `walletVersion` (do mais antigo para o mais novo). `limit` opcional, padrão 50, entre 1 e 100. `cursor` opaco (base64url de `v1:<walletVersion do último item>`); ausência = início.

`200 OK`:

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "entries": [
    {
      "id": "0192f2a0-1111-7000-8000-000000000001",
      "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
      "direction": "DEBIT",
      "money": { "amount": "25.00", "currency": "BRL" },
      "balanceBefore": { "amount": "1000.00", "currency": "BRL" },
      "balanceAfter": { "amount": "975.00", "currency": "BRL" },
      "walletVersion": 2,
      "createdAt": "2026-09-08T12:00:05.000Z"
    }
  ],
  "nextCursor": "djE6Mg"
}
```

`nextCursor` é `null` quando não há mais itens. `400 INVALID_CURSOR` para cursor que não decodifica; `400 VALIDATION_FAILED` para `limit` fora da faixa; `404 WALLET_NOT_FOUND`.

### 3.4 `POST /wallets/{walletId}/reconciliation` (interno)

Sem corpo. Lê carteira e ledger numa transação `REPEATABLE READ READ ONLY` e não altera nada. `difference = storedBalance - calculatedBalance`.

`200 OK`:

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "storedBalance": { "amount": "975.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "975.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

Divergência (`consistent: false`) vai para a resposta, para o log (nível error) e para a métrica `reconciliation_divergences_total`. `404 WALLET_NOT_FOUND`.

### 3.5 `POST /wagering/transactions` — envio de operação (provedor)

Headers: `Idempotency-Key` obrigatório (ausente ou inválido → 400). O servidor nunca substitui a chave recebida por outra calculada.

Requisição:

```json
{
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" }
}
```

`referenceExternalTransactionId`: obrigatório em `REFUND` e `ROLLBACK`; opcional em `WIN`; proibido em `BET` e `LOSS` (400).

Corpo de resultado (todos os códigos 201, 202 e 422):

| Campo | Presença |
| --- | --- |
| `transactionId` | sempre |
| `status` | sempre: `PROCESSED`, `PENDING_REFERENCE`, `REJECTED` ou `FAILED` |
| `balance` | em `PROCESSED` e `REJECTED`: saldo da carteira observado no processamento original, sempre na moeda da carteira, mesmo em `CURRENCY_MISMATCH` (para `LOSS` e rejeições, o saldo que estava lá). Ausente em `PENDING_REFERENCE` e `FAILED` |
| `failureCode` | em `REJECTED` e `FAILED` |
| `idempotentReplay` | `false` na primeira vez, `true` em replays |

| Código | Situação |
| --- | --- |
| 201 | Operação nova concluída com sucesso (`status: PROCESSED`) |
| 202 | Reversão registrada como `PENDING_REFERENCE` (a referência ainda não chegou ou ainda está pendente) |
| 422 | Resultado definitivo sem sucesso, persistido: `REJECTED` com `failureCode` do catálogo (§6). Também usado no replay de uma transação `FAILED` |
| 201 / 202 / 422 com `idempotentReplay: true` | Mesma chave e mesmo conteúdo: devolve o resultado persistido com o código do estado atual da transação. Uma reversão que estava pendente e já resolveu devolve o resultado final |
| 409 `IDEMPOTENCY_KEY_CONFLICT` | Mesma chave com conteúdo diferente (hash canônico diferente). Esta tentativa não cria operação; a chave/ID externo existentes continuam ocupados. Problem inclui `transactionId` da existente |
| 409 `EXTERNAL_TRANSACTION_ID_CONFLICT` | Mesmo `(providerId, externalTransactionId)` com outra chave. Esta tentativa não cria operação; a chave/ID externo existentes continuam ocupados. Problem inclui `transactionId` da existente |
| 400 `VALIDATION_FAILED` | JSON malformado, campo desconhecido, dinheiro fora da forma canônica, `kind` inválido ou `OPENING`, header ausente, referência obrigatória ausente ou proibida presente, valor zero fora de `LOSS`, `LOSS` com valor diferente de `0.00` |
| 404 `WALLET_NOT_FOUND` | `walletId` não existe. Nada persistido; o cliente pode corrigir e reenviar com a mesma chave |
| 401 / 403 | Ver §2 |
| 503 `SERVICE_UNAVAILABLE` | Limitador cheio, `lock_timeout` ou falha com abortamento conhecido. Esta tentativa não confirma alterações. Header `Retry-After: 1` |
| 503 `COMMIT_OUTCOME_UNKNOWN` | Conexão/prazo interrompido sem confirmação conclusiva do COMMIT. A operação pode ter sido persistida. Reenviar os mesmos identificadores e payload, ou consultar. Header `Retry-After: 1` |

Exemplos:

```json
{ "transactionId": "0192f298-345e-7e38-af88-e43f851a819d", "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" }, "idempotentReplay": false }
```

```json
{ "transactionId": "0192f2a5-0000-7000-8000-000000000002", "status": "REJECTED",
  "failureCode": "INSUFFICIENT_FUNDS",
  "balance": { "amount": "20.00", "currency": "BRL" }, "idempotentReplay": false }
```

```json
{ "transactionId": "0192f2a6-0000-7000-8000-000000000003", "status": "PENDING_REFERENCE",
  "idempotentReplay": false }
```

### 3.6 `GET /wagering/transactions/{transactionId}` e `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}`

`200 OK` com a representação completa:

```json
{
  "transactionId": "0192f2a6-0000-7000-8000-000000000003",
  "providerId": "provider-a",
  "externalTransactionId": "rollback-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "ROLLBACK",
  "money": { "amount": "25.00", "currency": "BRL" },
  "referenceExternalTransactionId": "transaction-123",
  "referenceTransactionId": null,
  "status": "PENDING_REFERENCE",
  "failureCode": null,
  "balance": null,
  "pendingReference": { "attempts": 3, "nextAttemptAt": "2026-09-08T12:00:11.000Z", "deadlineAt": "2026-09-08T12:15:03.100Z" },
  "createdAt": "2026-09-08T12:00:03.100Z",
  "updatedAt": "2026-09-08T12:00:07.000Z",
  "completedAt": null
}
```

`pendingReference` só existe em `PENDING_REFERENCE`. `referenceTransactionId` é preenchido quando a referência foi resolvida (reversões processadas e `WIN` com referência encontrada). Transação interna (`OPENING`) tem `providerId`, `externalTransactionId`, `roundId`, `gameId` e referências nulos e só é visível ao papel interno.

`404 TRANSACTION_NOT_FOUND`: inexistente, ou de outro provedor (não vaza existência). `403` quando um provedor usa `{providerId}` do path diferente do claim; `wallet-internal` pode consultar qualquer provedor. `400 VALIDATION_FAILED` para `transactionId` que não é UUID.

### 3.7 Health

`GET /health/live` → `200 {"status":"UP"}` sempre que o processo responde.

`GET /health/ready` → `200 {"status":"UP","checks":{"postgres":{"status":"UP"},"sqs":{"status":"UP"}}}` ou `503` com `"status":"DOWN"` e o check que falhou (`"status":"DOWN","error":"..."`). Checa `SELECT 1` no pool e `GetQueueAttributes` da fila de entrada, cada um com prazo de 2 s. Nunca passa pelo limitador de escrita.

`GET /metrics` → formato de exposição do Prometheus.

## 4. Fluxo do `POST /wagering/transactions`

```
validação sintática (DTO)                          -> 400 / 401 / 403 sem tocar o banco
limitador de escrita (semáforo por instância)      -> 503 se não entrar em 2 s
BEGIN (SET LOCAL lock_timeout = 3s, statement_timeout = 5s)
  INSERT wager_transactions ... ON CONFLICT DO NOTHING RETURNING id
     0 linhas -> busca por (providerId, Idempotency-Key): igual -> replay | diferente -> 409
                 não achou pela chave -> busca por (providerId, externalTransactionId) -> 409
     FK da carteira falha -> 404 WALLET_NOT_FOUND
  SELECT wallets ... FOR NO KEY UPDATE             (lock por carteira; quem chega depois espera)
  (REFUND/ROLLBACK/WIN com referência) SELECT da referência por (providerId, referenceExternalTransactionId)
  domínio decide: PROCESSED | REJECTED(failureCode) | PENDING_REFERENCE
  INSERT ledger + UPDATE wallets (WHERE version = versão lida) quando há movimentação
  UPDATE wager_transactions (status, failure_code, result_balance_minor, completed_at, agenda da pendência)
  INSERT outbox_events (eventos do desfecho)
  UPDATE ... SKIP LOCKED nas pendências que esperavam por esta transação (wake-up)
COMMIT
responde 201 / 202 / 422
```

Rejeição de negócio é COMMIT (fica auditável). Falha antes do envio do COMMIT, ou abortamento confirmado pelo banco, desfaz a tentativa. `40001` e `40P01` são retentados até 3 vezes dentro do servidor antes de virar 503. Se o COMMIT foi enviado e a confirmação se perdeu, responder `503 COMMIT_OUTCOME_UNKNOWN` quando possível: chamar rollback nesse momento não prova que nada foi persistido, e a função da unidade de trabalho não é reexecutada automaticamente nessa classe.

Recuperação: consultar a transação ou reenviar exatamente o mesmo provedor, `Idempotency-Key`, `externalTransactionId` e payload. Se houve commit, é replay; se houve abortamento, a operação é aplicada uma vez. Não trocar identificadores para resolver incerteza: trocar ambos significa uma operação nova. Na abertura de carteira, repetir os mesmos `playerId`/moeda/dados; eventual `409 WALLET_ALREADY_EXISTS` informa `walletId` para GET, sem reaplicar saldo inicial.

## 5. Formato de erro (`application/problem+json`)

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "detail": "Idempotency-Key 'provider-a:transaction-123' was already used with a different payload",
  "code": "IDEMPOTENCY_KEY_CONFLICT",
  "correlationId": "0192f2b0-0000-7000-8000-000000000009",
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d"
}
```

`errors` (só em 400) é uma lista `[{"field":"money.amount","message":"..."}]`. `transactionId` e `walletId` são extensões presentes só nos conflitos.

Códigos de erro de validação/transporte. Uma entrada nova rejeitada com 400/404 não reserva chave; isso não libera uma chave já usada. 409 mantém o registro existente e seus vínculos: corrigir o payload não permite sobrescrevê-lo. Commit incerto tem a recuperação descrita em §4.

| HTTP | `code` |
| --- | --- |
| 400 | `VALIDATION_FAILED`, `INVALID_CURSOR` |
| 401 | `UNAUTHENTICATED` |
| 403 | `FORBIDDEN` |
| 404 | `WALLET_NOT_FOUND`, `TRANSACTION_NOT_FOUND` |
| 409 | `WALLET_ALREADY_EXISTS`, `IDEMPOTENCY_KEY_CONFLICT`, `EXTERNAL_TRANSACTION_ID_CONFLICT` |
| 413 | `PAYLOAD_TOO_LARGE` |
| 503 | `SERVICE_UNAVAILABLE`, `COMMIT_OUTCOME_UNKNOWN` (com `Retry-After: 1`) |
| 500 | `INTERNAL_ERROR` (erro inesperado, sem detalhes internos; não implica ausência de efeito se o erro ocorrer depois de um commit já confirmado) |

## 6. Catálogo de `failureCode` (resultados definitivos, persistidos em `REJECTED` ou `FAILED`)

São onze códigos. Todos são definitivos: a transação é terminal e a chave de idempotência ficou consumida com aquele conteúdo. Para tentar de novo o provedor precisa de uma operação nova (`externalTransactionId` e chave novos). Os códigos ficam também num `CHECK` do banco. Valor de entrada acima do limite é 400 antes de persistir; saldo que excederia o limite após somar um crédito válido é 422 com `BALANCE_LIMIT_EXCEEDED`.

| `failureCode` | Status | Quando |
| --- | --- | --- |
| `INSUFFICIENT_FUNDS` | REJECTED | `BET` com valor maior que o saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | REJECTED | `ROLLBACK` de `WIN` ou de `REFUND` cujo débito deixaria o saldo negativo |
| `BALANCE_LIMIT_EXCEEDED` | REJECTED | Crédito de WIN/REFUND/ROLLBACK de BET excederia `92233720368547758.07`; saldo e versão preservados, sem lançamento |
| `REFERENCE_NOT_FOUND` | REJECTED | Reversão cuja referência não chegou até o prazo (`REFERENCE_TTL`) |
| `REFERENCE_NOT_PROCESSED` | REJECTED | Referência existe mas terminou `REJECTED` ou `FAILED`; ou ainda estava `PENDING_REFERENCE` quando o prazo esgotou |
| `REFERENCE_MISMATCH` | REJECTED | Referência com jogador, carteira, moeda ou rodada diferentes; em `REFUND`/`ROLLBACK`, valor diferente do referenciado |
| `REFERENCE_KIND_NOT_ALLOWED` | REJECTED | `REFUND` de algo que não é `BET`; `ROLLBACK` de `LOSS`, `ROLLBACK` ou `OPENING`; `WIN` referenciando algo que não é `BET` |
| `ALREADY_REVERSED` | REJECTED | A referência já tem uma reversão bem-sucedida (`REFUND` ou `ROLLBACK`) |
| `CURRENCY_MISMATCH` | REJECTED | `money.currency` diferente da moeda da carteira |
| `WALLET_PLAYER_MISMATCH` | REJECTED | `playerId` não é o dono da carteira |
| `PERMANENT_FAILURE` | FAILED | O worker de referências bateu em erro não transitório que não é regra de negócio (violação inesperada de invariante, dado corrompido). Só via worker; o caminho síncrono nunca grava `FAILED` |

Ordem de avaliação no domínio: `WALLET_PLAYER_MISMATCH` → `CURRENCY_MISMATCH` → regras do tipo. Para reversões: referência ausente/pendente → `PENDING_REFERENCE`; `REFERENCE_NOT_PROCESSED` → `REFERENCE_KIND_NOT_ALLOWED` → `REFERENCE_MISMATCH` → `ALREADY_REVERSED` → limite financeiro (`REVERSAL_INSUFFICIENT_FUNDS` no débito; `BALANCE_LIMIT_EXCEEDED` no crédito).

## 7. Variáveis do ambiente local (para os exemplos)

| O quê | Valor |
| --- | --- |
| API | `http://localhost:8080` (réplicas em 8080, 8081 e 8082 quando o compose sobe 3; Keycloak em 8180 e MiniStack em 4566) |
| Token | `POST http://localhost:8180/realms/wallet/protocol/openid-connect/token`, `grant_type=client_credentials`, `client_id`/`client_secret` do `.env.example` |
| Clients de teste | `provider-a` / `provider-a-secret`, `provider-b` / `provider-b-secret`, `wallet-internal` / `wallet-internal-secret`, `provider-c-short-lived` / `provider-c-secret` (token de 5 s) |
