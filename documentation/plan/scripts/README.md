# Scripts da validação de 20/09/2026

Rodados contra `postgres:16-alpine` (PostgreSQL 16.15) e `ministackorg/ministack:1.5.14`, via Docker, sem sudo.

| Arquivo | Uso |
| --- | --- |
| `plan-section-6.up.sql` / `plan-section-6.down.sql` | Migration exatamente como está na seção 6 do plano (extraída do markdown). A migration definitiva em `migrations/` parte deste arquivo com os ajustes listados no design.md |
| `conc.sql` | Funções `setup_wallet` e `run_bet` que executam o fluxo central do plano (§6.5) dentro do Postgres, com `pg_sleep` para forçar a disputa pelo lock |
| `validate.sh` | Cenário 100.00 com duas BETs de 80.00 (HTTP x SQS) com `FOR NO KEY UPDATE` (3 rodadas) e com `FOR UPDATE` (deadlock), 30 duplicatas em paralelo, proteções do schema, reconciliação e planos de execução |
| `recheck.sh` | Checagens complementares: idempotência por ID externo, FK composta de moeda, aritmética do ledger, outbox (snapshot, lease, `published_at`), inbox e wake-up com `SKIP LOCKED` |
| `ministack_probe.py` | Smoke test do SQS do MiniStack: FIFO, RedrivePolicy, visibility timeout, `ApproximateReceiveCount`, `ChangeMessageVisibility`, ordem por grupo, dedup |

Os scripts assumem um container chamado `jg-pgcheck` (Postgres) e o papel `wallet_app` já criado. Eles são referência para reescrever os cenários como testes de integração em Go (ver tasks.md), não fazem parte da entrega.
