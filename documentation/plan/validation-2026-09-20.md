# Validação do plano de handoff — 20/09/2026

Objetivo: conferir se o plano (`PLANO-jungle-gaming-backend-challenge.md`) está coerente com o README do desafio e se as afirmações técnicas marcadas como validadas se reproduzem. Feito em uma sessão só, sem subagentes, em Ubuntu 24.04 com Docker 29 e Compose v5, sem sudo.

Resultado: o plano está consistente com o README. Tudo que ele afirma ter validado se reproduziu. A validação levantou oito pontos que o plano não cobria e quatro notas de implementação; todos foram fechados no `design.md` do change `implement-wagering-wallet-service` (seção "Decisões fechadas na revisão").

## 1. Plano x README do desafio

Conferido item a item: endpoints (README §9), filas e consumidor (§10), os quatro eventos (§11), pesos e eliminatórios (§14), máquina de estados e `OPENING` (§6.3), política de zero (§7), semântica de `REFUND` e `ROLLBACK` (§7), `LOSS` sem `WalletBalanceChanged` (§7 e §11), abertura com `OPENING` no mesmo commit (§9), os oito cenários de concorrência e recuperação (§13). Sem divergência. Os opcionais que o plano descarta (§1.6) são os que o README chama de diferenciais.

## 2. Migration da seção 6 em PostgreSQL 16.15 real

Imagem `postgres:16-alpine`. Papel `wallet_app` criado antes (como o compose fará).

| Passo | Resultado |
| --- | --- |
| `up`, `down`, `up` de novo | Sem erro. Depois do `down`: 0 tabelas |
| Objetos após o `up` | 5 tabelas, 18 índices, 6 triggers (igual ao plano §6.4) |
| Grants de `wallet_app` | `wallets`, `wager_transactions`, `inbox_messages`, `outbox_events`: SELECT, INSERT, UPDATE. `wallet_ledger_entries`: SELECT, INSERT |

## 3. Cenários de concorrência (plano §3.2 e §3.3)

Executados com o fluxo central do plano (§6.5) reescrito em PL/pgSQL (`scripts/conc.sql`), duas sessões reais em paralelo, como `wallet_app`.

| Cenário | Resultado |
| --- | --- |
| Carteira com 100.00, duas BETs de 80.00 ao mesmo tempo (uma `HTTP`, outra `SQS`), `FOR NO KEY UPDATE`, 3 rodadas | Sempre uma `PROCESSED` e uma `REJECTED` (`INSUFFICIENT_FUNDS`), saldo final 20.00, versão 2, um débito no ledger, outbox com 2 `WagerTransactionProcessed` (abertura + aposta), 2 `WalletBalanceChanged`, 1 `WagerTransactionRejected`. Quem venceu variou entre as rodadas (HTTP, HTTP, SQS) |
| Mesmo cenário com `FOR UPDATE` | `ERROR 40P01: deadlock detected` numa das sessões, como o plano descreve. Saldo continuou correto, mas uma aposta se perdeu com erro |
| Mesma BET (mesma chave) 30 vezes em paralelo | 1 `PROCESSED`, 29 `REPLAY` com o saldo original (75.00), uma única linha em `wager_transactions`, um único débito |
| Reconciliação (`REPEATABLE READ READ ONLY`) | `stored = calculated = 20.00`, 2 lançamentos |
| `EXPLAIN` com `enable_seqscan = off` | Publisher usa `outbox_pending_idx`; worker de referências usa `wtx_pending_reference_due_idx` (index only scan); paginação do ledger usa `wle_wallet_version_uq` |

## 4. Proteções do schema

Cada linha era para falhar (ou devolver 0 linhas) e falhou com o erro esperado.

| # | Checagem | Resultado |
| --- | --- | --- |
| d1 | Saldo muda sem lançamento no ledger | Falha no COMMIT: `23000 ... reached version 3 without a matching ledger entry` |
| d2 | Saldo muda sem `version + 1` | `23514 balance change requires version = 3 (got 2)` |
| d3 | Versão muda sem saldo | `23514 version only changes together with balance` |
| d4 | Saldo negativo direto | `23514 wallets_balance_nonneg_ck` |
| d5 | UPDATE no ledger como `wallet_app` | `42501 permission denied` |
| d6, d7, d8 | UPDATE, DELETE, TRUNCATE no ledger como dono das tabelas | `23001 wallet_ledger_entries is append-only` |
| d9 | DELETE em `wallets` como dono | `23001 delete is not allowed` |
| d10 | Transição a partir de estado terminal | `23001 REJECTED is terminal` |
| d11 | Segundo `OPENING` na mesma carteira | `23505 wtx_opening_per_wallet_uq` |
| d12 | `OPENING` com origem HTTP | `23514 wtx_external_fields_ck` |
| d13 | Mesmo `(providerId, externalTransactionId)` com outra chave | INSERT devolve 0 linhas; busca pela chave acha 0; busca pelo ID externo acha 1. Ou seja, o conflito só é identificado consultando os dois `UNIQUE` |
| d14 | Primeira reversão `PROCESSED` da BET | Passa |
| d15 | Segunda reversão `PROCESSED` da mesma BET | `23505 wtx_single_successful_reversal_uq` |
| d16 | Lançamento com moeda diferente da carteira | `23503 wle_wallet_currency_fk` |
| d17 | Lançamento com aritmética errada | `23514 wle_arithmetic_ck` |
| d18 | Reescrever `payload` da outbox | `23001 event snapshot is immutable` |
| d19 | Claim da outbox com `SKIP LOCKED` + lease, depois `published_at` | 2 de 5 reivindicadas, 2 publicadas, 3 seguem reivindicáveis |
| d20 | Reescrever `published_at` | `23001 published_at cannot be rewritten` |
| d21 | `locked_by` sem `locked_until` | `23514 outbox_lease_ck` |
| d22 | Reentrega do mesmo `messageId` na inbox | Segundo INSERT devolve 0 linhas |
| d23 | Wake-up de pendentes com `FOR UPDATE SKIP LOCKED` | Executa sem cruzar lock (0 pendentes no momento) |

## 5. MiniStack (plano §3.11)

Imagem `ministackorg/ministack:1.5.14` (MIT, sem token, sem cadastro), porta 4566, 178 MB. Smoke test em `scripts/ministack_probe.py` com chamadas SQS diretas (JSON protocol).

| Verificação | Resultado |
| --- | --- |
| Criar fila FIFO e DLQ FIFO; `RedrivePolicy` com `maxReceiveCount` aceito e persistido | PASS |
| Visibility timeout respeitado; `ApproximateReceiveCount` 1 e 2 nas reentregas | PASS |
| Redrive para a DLQ ao exceder `maxReceiveCount` | PASS |
| `ChangeMessageVisibility` para 0 reentrega na hora; `DeleteMessage` remove | PASS |
| Ordem dentro do `MessageGroupId`; com uma mensagem do grupo em voo, só outros grupos são entregues | PASS |
| `MessageDeduplicationId` repetido entrega uma mensagem só | PASS |
| `ReceiveMessage` com `MaxNumberOfMessages = 10` devolve várias mensagens do mesmo grupo de uma vez | Confirmado (comportamento igual ao SQS real). Motivou a decisão de consumir com `MaxNumberOfMessages = 1` |

Detalhes úteis para o compose: a imagem não tem `curl` nem `wget`, mas tem `python3`, `aws` e `awslocal`; expõe `GET /_ministack/health` (200); aceita scripts de inicialização em `/etc/localstack/init/ready.d/` (mesmo mecanismo do LocalStack).

## 6. LocalStack (plano §3.11)

Confirmado: desde a versão 2026.03.0 a imagem `localstack/localstack` exige `LOCALSTACK_AUTH_TOKEN` para subir; o plano Hobby é gratuito só para uso não comercial e exige conta. Para um avaliador rodar de um checkout limpo, token está fora de questão. A escolha do MiniStack se mantém; o endpoint fica configurável como plano B.

## 7. Achados que viraram decisão

Pontos que o plano não cobria (todos fechados no `design.md`, "Decisões fechadas na revisão"):

1. `WIN` com `referenceExternalTransactionId` (README §7 diz que é opcional; plano não dizia o que fazer).
2. `LOSS` com referência (schema só proibia em `BET`).
3. Replay de operação ainda em `PENDING_REFERENCE`.
4. Saldo em rejeições (tabela HTTP prometia saldo no 422, mas o schema só o exigia em `PROCESSED`).
5. Conflito de idempotência chegando por SQS (erro permanente, não retry).
6. Hash guardado na inbox (o plano só especificava o hash canônico da transação).
7. Lote do consumidor SQS e a premissa "uma mensagem por carteira em processamento".
8. `aud` do token de `client_credentials` do Keycloak (por padrão vem `account`).

Notas de implementação confirmadas: consultar os dois `UNIQUE` quando o INSERT devolve 0 linhas (d13); golang-migrate no modo padrão executa o arquivo inteiro num único `Exec` (o `BEGIN`/`COMMIT` do arquivo funciona; o modo `x-multi-statement` quebraria os corpos `$$`); `docker compose up --build` literal precisa do arquivo principal na raiz; cada processo do `StartApp(n)` precisa de porta HTTP própria e o orçamento de conexões precisa caber no `max_connections`.

## 8. Ambiente da máquina

- Go não estava instalado. Instalado Go 1.27.1 (SHA-256 conferido contra go.dev) em `~/.local/go`, links em `~/.local/bin`, PATH em `~/.zshrc`. Sem sudo (pede senha).
- Docker 29.8 e Compose v5.5.1 funcionam sem sudo. Imagens já baixadas: `postgres:16-alpine`, `ministackorg/ministack:1.5.14`.
