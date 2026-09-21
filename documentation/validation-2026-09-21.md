# Validação final — 21/09/2026

Resultado: os nove pontos da auditoria foram corrigidos e os cenários abaixo passaram. Não ficou bloqueio técnico conhecido para iniciar o Git e enviar o desafio no escopo obrigatório. A inicialização, o commit e a publicação do repositório continuam sendo a próxima etapa da entrega.

A revisão partiu do README original do desafio, das decisões registradas em `ARCHITECTURE.md` e da execução real. A premissa aprovada de ingestão SQS confiável foi mantida e documentada; não foi criado um gateway adicional.

## Correções e evidências

| Ponto | Resultado verificado |
| --- | --- |
| SQLSTATE 40001/40P01 | Continuam transitórios após esgotar os retries. Trigger no PostgreSQL real força ambas as falhas: o worker mantém a pendência e depois resolve; HTTP responde 503 e a repetição após recuperação debita uma vez |
| JSON e envelope | Objeto seguido de `]`, `}`, outro objeto ou literal é recusado. Mensagens sem `occurredAt`, com timestamp inválido ou JSON quebrado vão à DLQ, sem escrita financeira |
| Contextos e encerramento | O backoff usa contexto independente do handler vencido. HTTP e SQS interrompem entradas juntos. SIGTERM com handler bloqueado no banco conclui durante o drain ou cancela e libera a mensagem antes da visibilidade original de 30 s |
| Panic e falha permanente | O UnitOfWork confirma rollback antes de classificar panic. O worker relê sob lock e grava FAILED em outra transação. Ledger, saldo e evento da tentativa abortada não sobrevivem; a próxima pendência avança |
| JWKS | Startup falha com endpoint inacessível ou documento sem chave de assinatura utilizável. Keycloak real troca o `kid`; o mesmo processo aceita tokens antigos e novos |
| Reidratação e invariantes | Dinheiro não inicializado, metadados ausentes e combinações inválidas de status/resultado/referência são recusados. Transições inválidas não alteram o objeto; eventos e bytes da outbox mantêm snapshots independentes |
| Métrica de pendências | `reference_pending` sobe a 1 com pendência durável e retorna a 0 quando resolvida. Cada réplica mede o mesmo banco: agregar com `max`, não com soma |
| Logs de reconciliação | Divergência continua na resposta autenticada e na métrica; o valor monetário foi retirado do log e há regressão que verifica isso |
| Cobertura de recuperação | COMMIT aplicado com resposta perdida foi exercitado em HTTP, consumidor e worker. A recuperação não duplica movimento. DLQ indisponível e timeout após publicação mantêm a origem até confirmação e produzem um único dead letter |

O código de aplicação da movimentação foi compartilhado entre o fluxo imediato e o worker, dentro da mesma fronteira transacional. Os helpers reutilizáveis agora incluem proxy PostgreSQL, proxy SQS, rotação real de chave e supervisão rigorosa de processos. Um failpoint só autoriza SIGKILL; outro código de saída ou diagnóstico de corrida reprova o teste. Inicializações negativas também reprovam saída 66 ou `DATA RACE`.

## Bateria executada

| Verificação | Resultado |
| --- | --- |
| `go test -count=1 -race ./...` | Passou |
| `go vet -tags 'integration failpoints compose' ./...` | Passou |
| `go test -count=1 -race -tags integration -timeout 35m -v ./test/...` | Passou; pacote de integração em 222,076 s |
| Regressões adicionais HTTP transitório, startup e supervisor após os últimos ajustes dos testes | Passaram; pacote de integração em 31,922 s |
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

O MiniStack não comprova enforcement real de IAM; essa limitação permanece explícita em `ARCHITECTURE.md`. Não foi realizado benchmark de produção nem campanha automatizada de mutation testing. As regressões verificam efeitos observáveis, invariantes e imutabilidade. Não há percentual de cobertura publicado: a instalação local de Go usada na auditoria não disponibilizou a ferramenta `covdata`; isso não impediu os testes com detector de corridas.

Não foram inicializados Git, commit, push ou publicação externa nesta etapa. Foram adicionados `.gitignore` e `.dockerignore` para preparar o versionamento e excluir configuração local e artefatos gerados.
