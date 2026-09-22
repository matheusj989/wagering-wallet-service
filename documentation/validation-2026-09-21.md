# Validação final — 21/09/2026

Bateria executada sobre a versão entregue, partindo do README original do desafio, das decisões registradas em `ARCHITECTURE.md` e da execução real. A premissa de ingestão SQS confiável foi mantida e documentada; não foi criado um gateway adicional.

## Bateria executada

| Verificação | Resultado |
| --- | --- |
| `go test -count=1 -race ./...` | Passou |
| `go vet -tags 'integration failpoints compose' ./...` | Passou |
| `go test -count=1 -race -tags integration -timeout 35m -v ./test/...` | Passou; pacote de integração em 222,076 s |
| Regressões de HTTP transitório, startup e supervisor | Passaram; pacote de integração em 31,922 s |
| Instrumentação dos processos filhos | Binários compilados com `-race -tags failpoints`; `GORACE=halt_on_error=1 exitcode=66`; nenhuma corrida detectada |
| `go mod verify` | Todos os módulos verificados |
| `gofmt -l cmd internal test` | Nenhuma pendência |
| Compose com build e migrations, partindo de volume novo | PostgreSQL 18.6, Keycloak 26.7.4, MiniStack 1.5.14 e três APIs saudáveis |
| Postman/Newman após reiniciar as APIs | 58 requisições, 166 asserções, zero falhas |

## Ensaio externo do Compose

Código e reprodução: [test/compose](../test/compose/README.md). A stack usada nesta execução foi isolada sob o nome `wallet-final-validation`, com portas próprias e volume novo.

- 50 carteiras abertas, com até 10 fluxos HTTP concorrentes distribuídos pelas três réplicas.
- BET, WIN, LOSS, REFUND, ROLLBACK recebido antes da referência e rejeição por saldo insuficiente.
- 310 mensagens enviadas por `SendMessageBatch`, em lotes de 10; 100 mensagens reproduzem BETs já confirmadas via HTTP, usando identidades de transporte distintas.
- 400 transações de domínio, 300 entradas de ledger e 750 eventos confirmados na outbox e recebidos na fila de saída.
- 10 mensagens inválidas na DLQ, sem movimento financeiro.
- Cada carteira terminou em `1040.00`, reconciliada com suas seis entradas de ledger.
- Todas as APIs foram reiniciadas. A segunda fase releu as mesmas 50 carteiras e repetiu as BETs: saldos, ledgers e idempotência permaneceram corretos.
- Depois do Newman, a conferência adicional no banco encontrou zero carteiras inconsistentes/negativas e zero eventos aguardando publicação.

O ensaio de lotes levou 11,16 s e a verificação após reinício levou 0,22 s, excluindo inicialização do Compose. Esses tempos registram a execução local; não são benchmark de capacidade ou garantia de latência.

## Limites da evidência

O MiniStack não comprova enforcement real de IAM; essa limitação permanece explícita em `ARCHITECTURE.md`. Não foi realizado benchmark de produção nem campanha automatizada de mutation testing. As regressões verificam efeitos observáveis, invariantes e imutabilidade. Não há percentual de cobertura publicado: a instalação local de Go usada na validação não disponibilizou a ferramenta `covdata`; isso não impediu os testes com detector de corridas.
