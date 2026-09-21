# Plano de implementação: desafio backend Jungle Gaming (Go)

Documento de handoff. Consolida tudo que foi planejado até 20/09/2026 para abrir uma nova sessão, terminar o planejamento que falta e começar o desenvolvimento.

Autor do projeto: Matheus (candidato à vaga de Tech Lead na Jungle Gaming).

---

## 0. Como usar este documento

1. **Fonte da verdade dos requisitos é o README do desafio.** Se este documento e o README divergirem, o README vence. Ler o README inteiro antes de escrever qualquer código.
2. **Marcação de status usada aqui:**
   - **[FECHADO]** decidido em conversa com o Matheus.
   - **[PROPOSTO]** sugestão já elaborada, mas que o Matheus ainda não revisou ou confirmou. Apresentar e validar antes de implementar.
   - **[PENDENTE]** ainda precisa ser planejado.
3. **Forma de trabalho.** O Matheus quer discutir e decidir cada tópico antes de gerar artefatos. Não escrever código, migration ou config de um tópico [PROPOSTO] ou [PENDENTE] sem alinhar primeiro. Respostas objetivas, com cenário concreto quando o assunto for regra de negócio ou concorrência.
4. **A migration da seção 6 é um rascunho.** Foi validada tecnicamente num Postgres 16 real, mas o Matheus ainda não revisou linha a linha. Tratar como proposta a revisar em conjunto.
5. **O repositório `desafio-b3-stone` é somente leitura.** Serve só de referência de organização. Nunca escrever nele.

---

## 1. Contexto

### 1.1 Links

| O quê | Link |
| --- | --- |
| Desafio (README com todos os requisitos) | https://github.com/junglegaming/backend-challenge-go |
| Referência de estrutura (desafio Stone/B3 do Matheus, Clean Arch em Go) | https://github.com/matheusj989/desafio-b3-stone |

### 1.2 O desafio em uma página

Serviço em Go, composto com Uber Fx, que processa operações financeiras de provedores de jogos sobre carteiras de jogadores, em ambiente distribuído.

- Duas portas de entrada com as mesmas garantias: API HTTP e consumidor SQS FIFO.
- Tipos de operação externos: `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`. Tipo interno: `OPENING` (crédito inicial na abertura de carteira).
- Entrega at-least-once. O sistema precisa aguentar: a mesma operação repetida (inclusive uma vez por HTTP e outra por SQS), reversão chegando antes da operação original, operações simultâneas na mesma carteira, processo morrendo antes ou depois do commit, evento de integração publicado mais de uma vez, Postgres ou SQS fora do ar por um tempo.
- Nada disso pode gerar movimentação duplicada, saldo negativo ou perda de evento cujo registro já foi confirmado no banco.
- Tudo tem que funcionar com pelo menos três instâncias independentes rodando ao mesmo tempo.

Endpoints exigidos:

```
POST /wallets                                   abertura (só serviço interno)
GET  /wallets/:walletId
GET  /wallets/:walletId/ledger?cursor=...&limit=50     cursor opaco, ordenação estável
POST /wallets/:walletId/reconciliation          compara saldo armazenado x ledger, não altera nada
POST /wagering/transactions                     header Idempotency-Key obrigatório
GET  /wagering/transactions/:transactionId
GET  /providers/:providerId/wagering/transactions/:externalTransactionId
GET  /health/live                               público
GET  /health/ready                              público, checa Postgres e SQS
```

Filas exigidas: `wager-transactions.fifo` e `wager-transactions-dlq.fifo` com redrive. Além delas, um destino para os eventos de saída, que a gente provisiona e documenta.

Eventos de saída exigidos: `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference`.

### 1.3 Stack obrigatória

| Responsabilidade | Tecnologia |
| --- | --- |
| Linguagem | Go, versão declarada no `go.mod` e no Dockerfile |
| Composição | Uber Fx (`go.uber.org/fx`), com `fx.Module`, `fx.Provide`, `fx.Invoke`, `fx.Lifecycle` |
| HTTP | `net/http` ou roteador à escolha |
| Autenticação | IdP externo OAuth 2.0/OIDC, Keycloak recomendado, `client_credentials` |
| Persistência | PostgreSQL. `pgx` com SQL explícito é o preferido, `sqlc` opcional |
| Mensageria | AWS SQS local via LocalStack ou MiniStack |
| Ambiente | Docker Compose |
| Banco | Migrations versionadas, com aplicação e reversão documentadas |
| Testes | `testing` e `go test`, incluindo `-race` |

O domínio tem que ficar independente de Fx, HTTP, SQS e bibliotecas de persistência.

### 1.4 Pesos da avaliação

| Critério | Pontos |
| --- | --- |
| Integridade financeira | 20 |
| Concorrência | 20 |
| Idempotência | 15 |
| Mensageria e recuperação | 15 |
| Modelagem e arquitetura | 10 |
| Testes | 10 |
| Observabilidade | 5 |
| Documentação | 5 |

Na prática os testes pesam mais que 10, porque são a evidência de todo o resto.

### 1.5 Itens eliminatórios (checklist final antes de entregar)

- [ ] Autenticação efetiva em todos os endpoints de negócio.
- [ ] Nenhum acesso não autorizado a operações ou transações de outro provedor.
- [ ] Nenhum `float32`/`float64` em dinheiro, em nenhuma etapa (parsing, cálculo, serialização, persistência).
- [ ] Saldo nunca fica negativo por concorrência.
- [ ] Nenhuma movimentação duplicada.
- [ ] Idempotência persistente, não em memória.
- [ ] Funciona com várias instâncias, sem depender de instância única.
- [ ] Nenhum evento publicado antes do commit.
- [ ] Ledger auditável existe.
- [ ] Testes de integração usam Postgres, SQS e IdP reais em container, não mocks.

### 1.6 Fora do escopo [PROPOSTO]

Opcionais que não vamos fazer: ledger de partidas dobradas, tracing com OpenTelemetry, dashboards, teste de carga.

### 1.7 Tamanho do trabalho

Estimativa: 50 a 70 horas de trabalho focado para entregar completo no nível da rubrica. IA ajuda no boilerplate, mas os testes multi-processo e o Keycloak consomem tempo de qualquer jeito. O prazo de entrega ainda não foi informado nesta conversa.

---

## 2. Visão geral da arquitetura [FECHADO]

O serviço tem duas mãos de mensageria. A entrada recebe operações dos provedores. A saída avisa outros sistemas do que aconteceu.

```
ENTRADA: provedor -> nós
  HTTP POST  ou  fila wager-transactions.fifo
        |
        v
  [ UMA transação SQL ]
    1. (só SQS) registra a mensagem na inbox
    2. registra a operação com chave única (idempotência)
    3. lock na linha da carteira (FOR NO KEY UPDATE)
    4. resolve referência, confere saldo, aplica a regra no domínio
    5. grava ledger + saldo novo + status final
    6. INSERT em outbox_events (só uma linha na tabela, nada é publicado aqui)
    COMMIT (solta o lock)
        |
        v
  responde o provedor / apaga a mensagem do SQS

SAÍDA: nós -> outros sistemas (em background, em todas as instâncias)
  worker da outbox:
    reivindica linhas não publicadas (SKIP LOCKED + lease)
    publica na fila de eventos (ex.: wallet-events.fifo)
    marca como publicada

RETENTATIVA: worker de referências pendentes
    reivindica transações PENDING_REFERENCE vencidas (SKIP LOCKED)
    tenta resolver de novo, com backoff, até processar ou expirar
```

Um único binário roda HTTP, consumidor SQS, publisher da outbox e worker de referências, cada um como `fx.Module`. Com três instâncias iguais, a disputa entre workers fica provada naturalmente.

---

## 3. Decisões de arquitetura

Cada item traz o seu status. A maioria está fechada; o que ainda é proposta está marcado.

### 3.1 Processamento síncrono numa única transação SQL [FECHADO]

**Decisão.** A operação é decidida e aplicada na hora, dentro de uma transação SQL. Não existe aceite assíncrono para operações sem dependência.

**Alternativa descartada.** O desenho original do Matheus era: salvar a transação com índice único, publicar mensagem no SQS, e um consumidor com janela de espera (ex.: 3 segundos sem novas transações) pegava lock no Redis e processava em lote, atualizando o saldo. Esse padrão é bom para medição de consumo (quando dá para contabilizar depois), mas aqui foi descartado por três motivos:

1. **Aposta é autorização, não medição.** O provedor precisa saber na hora se a BET passou ou foi recusada por saldo. O exemplo de resposta do README já devolve `PROCESSED` com o saldo. Com janela de espera, toda aposta vira PENDING por pelo menos alguns segundos, e uma carteira quente (que nunca fica em silêncio) nunca processa. O README permite aceite assíncrono, mas aí todo PENDING confirmado precisa de retomada durável por outra instância, com cenário de teste próprio. O próprio README diz que operação sem dependência pode ser concluída de forma síncrona, sem commit intermediário.
2. **Salvar no banco e depois publicar no SQS é dual write.** Se o processo morre entre o commit e o publish, a transação fica PENDING para sempre.
3. **Redis não está na stack e não pode garantir correção.** O README exige invariantes financeiras garantidas no banco, independente de locks locais e da deduplicação do FIFO. Lock no Redis tem TTL, sofre com pausa de GC e não tem fencing.

**Consequência boa.** Como o registro da transação, o ledger e o saldo entram no mesmo commit, nunca existe transação confirmada que ainda não bateu no saldo. Quem lê o saldo debaixo do lock lê a verdade. A única exceção é `PENDING_REFERENCE`, que por definição ainda não mexeu em dinheiro.

**Nomenclatura.** Isso não é event sourcing, e o modelo do desafio também não é: é estado + ledger append-only confirmados juntos, com reconciliação para provar que batem. Não usar o termo "event sourcing" no `ARCHITECTURE.md`.

### 3.2 Concorrência: lock pessimista por carteira [FECHADO]

**Regra de ouro: primeiro o lock, depois a consulta de saldo.** Consultar o saldo antes do lock é o bug clássico de check-then-act: duas operações leem 100, as duas concluem que tem saldo. Saldo lido fora do lock é só palpite.

**Mecanismo.** `SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE` dentro da transação. Quem chega depois não falha: fica esperando, e o Postgres mantém a fila de espera daquela linha. Quando o primeiro dá commit, o segundo acorda e lê o saldo atualizado. Essa fila é por carteira, funciona entre instâncias, e se o processo morrer a conexão cai e o lock solta sozinho. Carteiras diferentes andam em paralelo, que é o que o README quer dizer com "locks globais são proibidos" (nada de mutex único, advisory lock com chave fixa, `LOCK TABLE`, consumidor único ou `MessageGroupId` único).

**Por que `FOR NO KEY UPDATE` e não `FOR UPDATE` (achado de teste, importante).** O INSERT em `wager_transactions` acontece antes do lock, e a FK dele para `wallets` pega `FOR KEY SHARE` na linha da carteira. Duas transações concorrentes ficam cada uma com KEY SHARE, e ao pedir `FOR UPDATE` uma espera a outra: deadlock. Foi reproduzido com duas sessões reais no cenário das duas BETs de 80. O Postgres detecta e mata uma, então o saldo ficava certo, mas uma aposta se perdia com erro e 1 segundo de atraso. `FOR NO KEY UPDATE` não conflita com KEY SHARE, continua serializando os escritores e é o mesmo nível que o UPDATE de `balance_minor`/`version` usa, já que nenhuma coluna-chave é alterada. Depois da troca, três execuções seguidas deram: uma PROCESSED, uma REJECTED por saldo, saldo final 20.00, um único débito no ledger.

**Cenário obrigatório do README, na linha do tempo:**

```
     A (API, BET 80)                  B (SQS, BET 80)
t1   BEGIN                            BEGIN
t2   lock na carteira: pegou
t3   lê saldo = 100                   lock na carteira: espera
t4   100 >= 80, debita                  ...
t5   grava ledger, saldo = 20           ...
t6   COMMIT                           acorda, lê saldo = 20
t7   responde PROCESSED, 20.00        20 < 80, grava REJECTED
t8                                    COMMIT, apaga a mensagem
```

**Defesa em profundidade:**

- `UPDATE wallets ... WHERE id = $1 AND version = $old` (checagem otimista por cima do lock).
- `CHECK (balance_minor >= 0)`.
- Trigger: saldo só muda junto com `version + 1`.
- Constraint trigger no COMMIT: saldo mudou sem lançamento correspondente no ledger, o commit falha.
- `lock_timeout` configurado: quem esperar demais recebe 503 em vez de prender conexão.
- Retry limitado para erros `40001` (serialization) e `40P01` (deadlock) como rede de segurança. `55P03` (lock timeout) vira 503 com `Retry-After`.

**Rejeição de negócio é COMMIT, não ROLLBACK.** Saldo insuficiente grava a transação como `REJECTED` com `failure_code` e o evento na outbox. No código, a função passada para a unidade de trabalho devolve `nil` em rejeição de negócio e `error` só em falha de infraestrutura.

**Prioridade entre API e mensageria: não.** O lock dura poucos milissegundos, quem chegou primeiro vai primeiro.

**Modelo mental de uma linha:** checar e debitar têm que ser a mesma operação atômica. A versão crua é `UPDATE wallets SET balance = balance - 80 WHERE id = $1 AND balance >= 80`. O lock explícito é a mesma ideia, só que deixa a regra dentro do agregado, como o desafio pede.

### 3.3 Idempotência [FECHADO]

São duas coisas diferentes:

- **Chave de idempotência**: vem do cliente (header `Idempotency-Key` no HTTP, `data.idempotencyKey` no SQS) e diz qual operação é. O servidor nunca substitui silenciosamente a chave recebida por outra calculada.
- **Hash do payload**: a gente calcula sobre os campos de negócio (JSON canônico, chaves ordenadas, sem a chave de idempotência e sem metadados de transporte). Serve para detectar a mesma chave chegando com conteúdo diferente.

Regras:

| Situação | Resultado |
| --- | --- |
| Mesma chave, mesmo hash | Devolve o resultado persistido com `idempotentReplay: true` e o **saldo observado no processamento original**, não o atual |
| Mesma chave, hash diferente | Conflito (409) |
| Mesmo `(providerId, externalTransactionId)` com outra chave | Conflito. A operação financeira não pode ser reaplicada com outra chave |

Mecanismo no banco: `UNIQUE (provider_id, idempotency_key)` e `UNIQUE (provider_id, external_transaction_id)`. A chave é única por provedor, para um provedor não colidir nem sondar chaves de outro.

Comportamento sob concorrência, sem lock extra: o registro da operação (`INSERT ... ON CONFLICT DO NOTHING RETURNING id`) vem **antes** do lock da carteira. Se a primeira ainda está em andamento, o Postgres faz o segundo INSERT esperar o commit dela, devolve 0 linhas, e aí é só ler o resultado guardado. Se a operação já terminou, um `SELECT` pela chave devolve na hora, sem lock nenhum. Validado: a mesma BET enviada 30 vezes em paralelo gerou uma única linha e nenhum lançamento a mais.

O hash tem que ser equivalente entre HTTP e SQS (mesmo algoritmo, mesmos campos, mesmas normalizações). A especificação exata está em [PENDENTE] na seção 9.4.

### 3.4 Inbox (entrada por SQS) [FECHADO]

- Identidade durável da mensagem: `messageId` do envelope. Unicidade por `(consumer_name, message_id)`.
- O registro da inbox entra na **mesma transação SQL** das alterações de domínio, ledger e outbox.
- A mensagem só é apagada da fila **depois do commit**.
- Se o processo morrer depois do commit e antes do `DeleteMessage`, a mensagem volta. A inbox e a idempotência tornam a reentrega inofensiva: o consumidor vê que já tratou, confere o hash e apaga.
- Rejeição de negócio confirmada é terminal e permite apagar a mensagem.
- Para referência pendente, a mensagem pode ser apagada assim que o `PENDING_REFERENCE` estiver persistido. O worker de referências assume dali.

### 3.5 Outbox (saída de eventos) [FECHADO]

**Para que serve.** Não tem nada a ver com atualizar saldo depois (o saldo já está resolvido na transação). É o mecanismo para avisar o mundo externo do que aconteceu, sem perder o aviso. Na vida real quem consome é a tela do jogador, cashback, antifraude, relatório regulatório. No desafio ninguém consome de verdade: os testes leem a fila para provar que o evento saiu.

**Por que tabela e não publicar direto depois do COMMIT.** Entre o COMMIT e o publish o processo pode morrer, e o débito existe mas o aviso sumiu. Publicar antes do COMMIT é pior e é eliminatório: anuncia uma aposta e o commit falha. Gravando a linha do evento na mesma transação do saldo, a intenção de publicar fica tão durável quanto o próprio débito.

**Quais eventos, em quais fluxos:**

| Situação | Eventos gravados na outbox |
| --- | --- |
| BET, WIN, REFUND, ROLLBACK processados | `WagerTransactionProcessed` + `WalletBalanceChanged` |
| LOSS processado | `WagerTransactionProcessed` (sem `WalletBalanceChanged`, não mexe em saldo nem em versão) |
| Rejeição definitiva de negócio | `WagerTransactionRejected` |
| Reversão aguardando referência | `WagerTransactionPendingReference` |
| Abertura de carteira com saldo positivo | `WagerTransactionProcessed` + `WalletBalanceChanged` (origem interna, sem metadados externos) |
| Abertura com saldo zero | nenhum |

Envelope exigido: `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` (opcional), `occurredAt`, `version`, `data` tipado. Tipo e versão definidos pelo construtor do evento. Timestamps UTC em RFC 3339, dinheiro em string decimal. Payload de `WalletBalanceChanged`: `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter`, `walletVersion`.

**Publisher:**

- Roda em todas as instâncias. Divisão do trabalho com `FOR UPDATE SKIP LOCKED`: cada worker pula as linhas que outro já segurou. (Contraste com a carteira: lá todo mundo precisa daquela linha específica e espera a vez; aqui qualquer linha serve, então quem está ocupado a gente pula.)
- Não segura transação aberta enquanto fala com o SQS. Faz um claim curto com lease (`locked_by`, `locked_until = now() + 30s`), dá commit, publica fora da transação, depois marca `published_at`. Se morrer no meio, o lease vence e outra instância assume.
- Falha ao publicar: incrementa `attempts`, agenda `next_attempt_at` com backoff exponencial, guarda `last_error`.
- Se morrer depois de publicar e antes de marcar, publica de novo com o **mesmo `eventId`**. Duplica, nunca perde. Quem consome descarta pelo `eventId`.
- Destino [PROPOSTO]: uma fila SQS FIFO `wallet-events.fifo`, `MessageGroupId = walletId`, `MessageDeduplicationId = eventId`.
- Ordem dos eventos da mesma carteira **não é garantida** com vários publishers. Quem consome ordena e detecta buraco pelo `walletVersion`. Documentar no `ARCHITECTURE.md`.

### 3.6 Reversões e referências

**Semântica [FECHADO]:**

- `REFUND`: evento de negócio. O jogo travou, a rodada foi anulada, devolve a aposta. Só vale para `BET`. Sempre crédito.
- `ROLLBACK`: desfazer técnico, "finge que aquilo não aconteceu". Vale para `BET` (crédito), `WIN` (débito) ou `REFUND` (débito).
- `referenceExternalTransactionId` é obrigatório nos dois e é resolvido por `(providerId, referenceExternalTransactionId)`.
- A operação e a referência têm que concordar em provedor, jogador, carteira, moeda e rodada, e o valor tem que ser igual (não existe reversão parcial). Motivo: se o provedor mandar ROLLBACK com 250.00 por bug numa BET de 25.00 e a gente confiar no payload, credita 250 para desfazer 25. O valor e os identificadores vêm da original, o payload só serve para conferir.

**Reversão chegando antes da original [FECHADO]:**

```
12:00:00.0  provedor envia BET tx-123 (25.00) via SQS  -> parada na fila
12:00:03.0  provedor dá timeout e cancela a rodada
12:00:03.1  provedor envia ROLLBACK ref=tx-123 via HTTP -> chega primeiro
            nós: tx-123 não existe. Grava PENDING_REFERENCE, responde 202
12:00:05.0  consumidor processa a BET tx-123 -> debita 25 (saldo 975)
12:00:06.0  worker tenta o ROLLBACK de novo, acha tx-123 -> credita 25 (saldo 1000)
```

Se a gente respondesse "não achei", o provedor daria o assunto por encerrado, a BET chegaria depois e debitaria, e o jogador pagaria por uma rodada cancelada. Por isso guarda e tenta de novo. Worker com backoff exponencial, sobrevive a reinício. Esgotou tentativas ou prazo: `REJECTED` com código de referência não encontrada + evento de rejeição.

**Regra de reversão única [PROPOSTO, validar]:** cada transação aceita no máximo **uma** reversão bem-sucedida, seja REFUND ou ROLLBACK, garantido por índice único parcial em `reference_transaction_id`. Cobre os dois furos: o retry do provedor mandando o mesmo cancelamento com IDs externos diferentes (rb-1 e rb-2, que a idempotência não pega), e a combinação cruzada (BET 25, REFUND devolve 25, ROLLBACK da mesma BET devolveria mais 25). A segunda vira `REJECTED` com "já revertida". Como as duas serializam no lock da mesma carteira, o domínio rejeita antes e o índice é só a rede de proteção.

Caso de borda a documentar como limitação: BET, REFUND, ROLLBACK do REFUND. A aposta volta a valer, mas não pode mais ser desfeita.

**Reversão que precisaria debitar mais que o saldo [FECHADO pelo README]:** rejeitada, auditável, com `failure_code` diferente do de aposta sem saldo. Exemplo: jogador com 0 recebe WIN de 100, aposta 80, fica com 20. ROLLBACK do WIN seria débito de 100, iria para -80.

**Comportamentos de resolução [PROPOSTO]:**

| Estado da referência | O que a reversão faz |
| --- | --- |
| Não existe ainda | `PENDING_REFERENCE`, retry com backoff até o prazo |
| Existe e está `PROCESSED` | Valida concordância e aplica |
| Existe e está `PENDING_REFERENCE` | Continua esperando |
| Existe e terminou `REJECTED` ou `FAILED` | `REJECTED`, não há nada para desfazer |
| Existe mas é de tipo não reversível (LOSS, ROLLBACK, OPENING; ou REFUND apontando para algo que não é BET) | `REJECTED` |

**Limitação conhecida a documentar:** o prazo do `PENDING_REFERENCE` tem que ser maior que a janela máxima de retry do SQS. Senão a BET pode chegar depois que o ROLLBACK expirou, debitar, e ninguém desfaz.

**Cuidado de implementação (lock cruzado):** quando uma transação é processada e quer "acordar" quem espera por ela (antecipar `next_attempt_at`), fazer o UPDATE só nas linhas obtidas com `FOR UPDATE SKIP LOCKED`. O worker de referências segura a linha da pendente e depois pede o lock da carteira; o fluxo principal segura a carteira e iria pedir a linha da pendente. Sem o SKIP LOCKED isso cruza.

### 3.7 Dinheiro [FECHADO]

- `Money` é value object imutável: `int64` em unidades mínimas (centavos) + moeda ISO 4217.
- Parser próprio a partir de string decimal. Rejeita vazio, `NaN`, `Infinity`, notação científica, mais de duas casas e negativo em entrada externa. Não arredonda nada silenciosamente.
- Overflow tratado no parsing, soma, subtração e negação.
- Negativo é permitido em diferenças e cálculos internos, nunca no saldo da carteira.
- Persistência: `BIGINT` + `CHAR(3)`.
- Contrato externo: `{"amount":"25.00","currency":"BRL"}`. `amount` é string. Se vier número JSON, o decode falha e vira 400.
- Pode operar só em BRL nos cenários principais, desde que o tipo carregue a moeda e existam testes de incompatibilidade de moeda.
- Política de zero: `LOSS` exige `"0.00"`, saldo inicial pode ser zero, `BET`/`WIN`/`REFUND`/`ROLLBACK` exigem maior que zero.

### 3.8 Identificadores [FECHADO]

UUID v7 em todo ID que a gente gera (carteira, transação, lançamento, evento). Os exemplos do README já são v7. IDs que vêm do provedor (`externalTransactionId`, chave de idempotência, `messageId`, `roundId`, `gameId`) são `TEXT` opaco, sem assumir formato.

### 3.9 Performance, pool e limitador [FECHADO]

- Consistência vem primeiro. O que decide a performance desse desenho é o tempo que o lock fica preso.
- **Dentro da transação só SQL, nunca chamada de rede.** Nada de SQS nem Keycloak com transação aberta. A outbox ajuda: no caminho crítico custa um ou dois INSERTs.
- Uma transação segura uma conexão do BEGIN ao COMMIT. A transação é uma conversa (BEGIN, SELECT, regra no Go, INSERTs, UPDATE, COMMIT), não um pacote enviado de uma vez. Transação esperando lock de carteira segura conexão parada, porque a fila do lock no Postgres é uma fila de conexões.
- Pool pequeno por instância. Em escrita pesada com lock, mais paralelismo vira contenção, não throughput.
- **Semáforo no HTTP** (middleware), só nas rotas de escrita. O pool do pgx já é um semáforo, então o valor do limitador explícito é decidir o que acontece quando lota: espera curta com prazo e, estourou, `503` com `Retry-After`. Se ele só faz esperar, a fila apenas mudou de lugar.
- O semáforo é **por instância**. A carga no banco é réplicas × limite. Limite global é o `max_connections` do Postgres (ou um PgBouncer). Para o desafio basta fazer a conta e documentar.
- Health check nunca atrás do limitador. Sob carga o readiness falharia, o orquestrador mataria a instância e a carga cairia nas outras, em cascata.
- O consumidor SQS já tem limitador natural (número de goroutines consumindo). Os workers precisam de conexão garantida. Exemplo de orçamento para um pool de 20: 12 HTTP, 4 consumidor, 2 workers, 2 de folga.
- **Carteira quente:** dez operações simultâneas na mesma carteira ocupam dez conexões, nove paradas. É caso de borda (tempestade de retry, bug, abuso) e drena em dezenas de milissegundos se as transações forem curtas. Do lado do SQS já está resolvido por `MessageGroupId = walletId` (o FIFO entrega uma mensagem por grupo de cada vez). Do lado HTTP, `lock_timeout`. Melhoria opcional se sobrar tempo: mutex local por `walletId` para a espera acontecer em goroutine em vez de conexão. Seria só economia de conexão; a correção continua sendo do lock no banco.
- Métricas que saem de graça: requisições em andamento e rejeitadas pelo limitador.

### 3.10 Índices [FECHADO]

Quase todos vêm das constraints que já são necessárias:

- Os dois `UNIQUE` da transação servem o replay e a resolução de referência.
- A carteira é travada pela PK.
- Workers usam **índices parciais**: na outbox só as linhas não publicadas, nas transações só as `PENDING_REFERENCE`. O índice fica minúsculo mesmo com a tabela enorme.
- Ledger paginado por `(wallet_id, wallet_version)`, sem OFFSET.

### 3.11 Emulador de SQS [PROPOSTO, validar no primeiro dia]

O LocalStack exige auth token para subir desde a versão 2026.03.0 (março de 2026). Existe plano gratuito só para uso não comercial e com cadastro; tags antigas ainda rodam sem token, sem atualizações. Como o avaliador precisa rodar de um checkout limpo, token está fora de questão.

Escolha: **MiniStack** (o README aceita explicitamente). Gratuito, MIT, mesma porta 4566, SQS com FIFO, deduplicação e DLQ. É projeto novo, então fazer um **spike no primeiro dia** validando o que a gente depende: ordenação por `MessageGroupId`, visibility timeout, `ChangeMessageVisibility`, redrive para a DLQ por `maxReceiveCount`, `ApproximateReceiveCount`. Plano B: fixar uma tag antiga do LocalStack. Manter o endpoint configurável.

### 3.12 Banco: papéis e tipos

- [PROPOSTO] Dois papéis: um dono das tabelas que roda as migrations, e `wallet_app` para runtime, sem DELETE em nada e só INSERT/SELECT no ledger. Os triggers seguram até o dono.
- [FECHADO] Enumerações como `TEXT + CHECK`, não `CREATE TYPE ... ENUM`. Mais fácil de evoluir e de reverter; ENUM no banco fica caro para editar depois.

---

## 4. Estrutura do projeto

### 4.1 Árvore [FECHADO]

```
cmd/api/main.go                     fx.New(...).Run()
deployments/
    app/
        Dockerfile
        app.yml                     compose só da aplicação
    docker/
        postgres.yml                um arquivo por serviço, todos com limites de recurso
        keycloak.yml                + keycloak/realm.json (realm e clients importados no boot)
        ministack.yml               + ministack/init.sh (filas FIFO, DLQ, redrive, fila de eventos)
        migrate.yml                 one-shot, roda as migrations e sai
        compose-main.yml            arquivo principal
migrations/
    000001_init.up.sql
    000001_init.down.sql
internal/
    domain/                         entidades, value objects, eventos, erros, portas de repositório
    application/                    casos de uso, portas (UnitOfWork, etc.)
    infrastructure/                 config, postgres, sqs, http, auth, workers, observabilidade
test/
    testenv/                        helper de suíte de integração
    integration/                    //go:build integration
```

`internal/` mantém a mesma divisão domain / application / infrastructure do repo da Stone.

### 4.2 Docker Compose [FECHADO]

- **Um YAML por serviço** e um `compose-main.yml` que referencia os outros, define a ordem de subida e inclui a própria aplicação. Subir tudo é um comando em cima do main.
- O main usa `include` para os arquivos de infra e `extends` para a aplicação (apontando para `deployments/app/app.yml`), acrescentando `depends_on` com `condition: service_healthy` (e `service_completed_successfully` para os one-shot de migration e de criação de filas). Assim cada arquivo continua válido sozinho e a ordem mora só no main.
- Limite de CPU e memória no próprio YAML com `deploy.resources.limits` (o Compose v2 respeita sem Swarm).
- Aplicação com 3 réplicas no ambiente local.
- Portas fixas só para dev local. Nos testes as portas são efêmeras, para pacotes paralelos não colidirem. [PROPOSTO: arquivo de override só com as portas.]
- Migrations como serviço one-shot antes da aplicação, não no boot de cada réplica. [PROPOSTO]

### 4.3 O que aproveitar e o que mudar do repo da Stone [FECHADO]

Aproveitar: a divisão em camadas, `chi`, `slog`, `testcontainers-go`, o padrão de caso de uso com input/output e interface.

Mudar:

1. O bootstrap manual (`Application`, `configureDependencies`, `signal.Notify`) sai. Entra `fx.Module` por área e `fx.Lifecycle`. Atenção: lá o erro de `configureDependencies()` é ignorado no `Start()`. Não carregar esse hábito.
2. A entidade de lá tem tag `bson` e `float64` no domínio. Aqui float em dinheiro é eliminatório e o domínio não pode conhecer persistência. Campos não exportados, construtores com validação, função de reidratação separada, e modelos de persistência na infraestrutura.
3. Falta uma porta de unidade de trabalho na camada de aplicação (seção 4.4).
4. Mocks: o `go.mod` de lá usa `github.com/golang/mock`, que foi arquivado. O fork mantido é `go.uber.org/mock`, mesma API. Aqui o uso vai ser pequeno (no máximo testes de handler), porque o README proíbe trocar a infra toda por mock e o caso de uso central é quase só orquestração de SQL.

### 4.4 Unidade de trabalho [PROPOSTO]

O README pede documentar a delimitação da transação SQL entre os repositórios, com transações "explícitas e verificáveis". Proposta: porta explícita, sem transação escondida no `context`.

```go
type Repositories interface {
    Wallets() WalletRepository
    Transactions() WagerTransactionRepository
    Ledger() LedgerRepository
    Inbox() InboxRepository
    Outbox() OutboxRepository
}

type UnitOfWork interface {
    // fn devolve nil para confirmar (inclusive rejeição de negócio) e error para desfazer.
    Do(ctx context.Context, fn func(ctx context.Context, r Repositories) error) error
}
```

Na infraestrutura, `Do` abre `pgx.Tx`, constrói os repositórios amarrados a essa transação, faz commit ou rollback, e classifica erros (`40001`, `40P01`, `55P03`, conexão).

### 4.5 Fx [PROPOSTO]

Módulos: `config`, `logging`, `postgres` (pool, unidade de trabalho, repositórios), `auth`, `sqs` (client), `http` (servidor, rotas, middlewares), `consumer`, `outbox`, `reference`, `observability` (métricas, health).

Ciclo de vida exigido pelo README:

- Start: valida configuração e dependências (ping no banco, filas existem, JWKS acessível).
- Workers com cancelamento, prazo de execução e término observável.
- Stop: para de aceitar entrada, conclui ou libera o trabalho em andamento, e só depois fecha as dependências. Como o Fx roda `OnStop` na ordem inversa do `OnStart`, registrar primeiro a infraestrutura (pool, client SQS) e depois servidor e workers já dá a ordem certa.
- Em `SIGTERM` o consumidor para de buscar mensagens e termina o que está em andamento dentro do prazo, ou libera a visibilidade (`ChangeMessageVisibility` para 0) para reentrega segura.

---

## 5. Modelo de domínio

Regras gerais do README [FECHADO]: estado encapsulado, construtores com validação, métodos explícitos de transição, **criação separada de reidratação** (reidratar não reaplica movimentação, transição nem emite evento), valores não inicializados ou inválidos são rejeitados, erros classificáveis por `errors.Is`/`errors.As`, `panic` nunca representa rejeição de negócio, toda operação de I/O recebe `context.Context`.

| Tipo | Papel | Pontos principais |
| --- | --- | --- |
| `Money` | Value object imutável | `int64` + moeda. `ParseMoney(string, currency)`, `Zero(currency)`, `Add`, `Sub`, `Neg`, `Cmp`, serialização. Overflow e moeda incompatível devolvem erro |
| `Wallet` | Raiz do agregado | id, playerId, currency, balance, version, createdAt, updatedAt. `NewWallet` e `RehydrateWallet`. `Debit`/`Credit` devolvem saldo antes e depois para alimentar o ledger. Versão inicial 1, só sobe quando o saldo muda |
| `WagerTransaction` | Operação + máquina de estados | ids interno e externo, provedor, chave, hash, carteira, jogador, rodada, jogo, tipo, `Money`, referência externa, referência interna resolvida, status, failureCode, saldo resultante, timestamps |
| `WalletLedgerEntry` | Lançamento imutável | id, walletId, transactionId, direção, valor, saldo antes, saldo depois, versão da carteira, criação. O construtor valida `depois = antes ± valor` |
| `InboxMessage` | Dedup do consumidor | consumerName, messageId, hash, recebimento, conclusão |
| `OutboxEvent` | Evento a publicar | eventId estável, agregado, tipo, versão, payload snapshot, tentativas, próximo envio, publicação |
| Eventos de domínio | Quatro tipos concretos | Envelope + `data` tipado. Tipo e versão definidos pelo construtor |
| `FailureCode` | Catálogo estável | Ver seção 9.2 |

Máquina de estados da transação [FECHADO pelo README]:

| Estado | Significado | Pode ir para |
| --- | --- | --- |
| `PENDING` | Aceita, ainda não concluída | `PROCESSED`, `REJECTED`, `PENDING_REFERENCE`, `FAILED` |
| `PENDING_REFERENCE` | Depende de referência que ainda não chegou | `PROCESSED`, `REJECTED`, `FAILED` |
| `PROCESSED` | Concluída com sucesso | terminal |
| `REJECTED` | Recusada por regra de negócio | terminal |
| `FAILED` | Falha permanente de infraestrutura, registrada para auditoria | terminal |

No desenho síncrono, `PENDING` nunca é confirmado sozinho: a linha nasce `PENDING` e transita na mesma transação. `OPENING` nasce direto em `PROCESSED`.

`OPENING` [FECHADO pelo README]: reservado à abertura interna. Rejeitar se vier por HTTP ou SQS. Não tem provedor, ID externo, chave, hash, rodada, jogo nem referência. O schema distingue interno de externo e impede crédito inicial duplicado.

---

## 6. Schema do banco [PROPOSTO: rascunho validado tecnicamente, falta a revisão do Matheus]

### 6.1 Resumo das tabelas

| Tabela | Papel | Proteções no banco |
| --- | --- | --- |
| `wallets` | Saldo e versão. Alvo do lock por carteira | `UNIQUE (player_id, currency)`, `CHECK` de saldo não negativo, trigger "saldo só muda com version + 1 e vice-versa", colunas de identidade imutáveis, sem DELETE, constraint trigger no COMMIT exigindo o lançamento do ledger |
| `wager_transactions` | Operação, idempotência, máquina de estados | Dois `UNIQUE` de idempotência, `CHECK`s de coerência de estado e de interno x externo, política de valor por tipo, único `OPENING` por carteira, única reversão bem-sucedida por referência, trigger contra transição de estado terminal e contra DELETE |
| `wallet_ledger_entries` | Ledger append-only | `UNIQUE (wallet_id, transaction_id)`, `UNIQUE (wallet_id, wallet_version)`, `CHECK` da aritmética, FK composta garantindo a moeda da carteira, triggers contra UPDATE, DELETE e TRUNCATE, papel da aplicação só com INSERT e SELECT |
| `inbox_messages` | Dedup do consumidor SQS | PK `(consumer_name, message_id)`, hash |
| `outbox_events` | Eventos a publicar | Snapshot imutável por trigger, `published_at` não se reescreve, lease coerente, índice parcial só do que não foi publicado |

Detalhes de desenho que valem destaque:

- **`wallet_version` no ledger** é a versão da carteira depois do lançamento. É única por carteira, então dá ordenação estritamente crescente (vira o cursor opaco da paginação) e impede dois lançamentos disputando a mesma versão.
- **Constraint trigger `DEFERRABLE INITIALLY DEFERRED` em `wallets`**: no COMMIT, se o saldo mudou (ou nasceu positivo) e não existe lançamento com aquela versão e aquele saldo final, o commit falha. É o "cada mudança financeira exige o lançamento correspondente, confirmado junto com o saldo" garantido pelo banco. Custa um lookup no índice único.
- **Moeda divergente não entra no ledger** (FK composta), mas pode ser gravada em `wager_transactions` como `REJECTED`, para ficar auditável.
- **Não há FK composta do ledger para a transação** (custaria mais um índice cheio na tabela mais quente). A coerência fica com o domínio e com a reconciliação.

### 6.2 `migrations/000001_init.up.sql` (completo)

```sql
-- 000001_init.up.sql
-- Carteira, transações de aposta, ledger, inbox e outbox.
--
-- Convenções
--   * Dinheiro: BIGINT em unidades mínimas (centavos) + currency CHAR(3). Nunca float.
--   * IDs nossos: UUID v7 gerado na aplicação. IDs do provedor: TEXT opaco.
--   * Enumerações: TEXT + CHECK (mais fácil de evoluir e reverter que CREATE TYPE).
--   * Timestamps: TIMESTAMPTZ vindos do domínio, sempre UTC.
--   * Papéis: o dono das tabelas roda as migrations; a aplicação usa wallet_app,
--     que não tem DELETE em nada e não tem UPDATE no ledger.

BEGIN;

-- ---------------------------------------------------------------------------
-- wallets: raiz do agregado. O lock por carteira é tomado nesta linha com
--
--     SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE
--
-- e NÃO com FOR UPDATE. Motivo (reproduzido em teste): o INSERT em
-- wager_transactions acontece antes do lock e a FK dele pega FOR KEY SHARE na
-- linha da carteira. Duas transações concorrentes ficam cada uma com KEY SHARE
-- e, ao pedir FOR UPDATE, uma espera a outra: deadlock. FOR NO KEY UPDATE não
-- conflita com KEY SHARE, continua serializando os escritores, e é o mesmo
-- nível que o UPDATE de balance/version usa (nenhuma coluna-chave é alterada).
-- ---------------------------------------------------------------------------
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL,
    balance_minor BIGINT      NOT NULL,
    version       BIGINT      NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_player_currency_uq UNIQUE (player_id, currency),
    -- alvo da FK composta do ledger: garante no banco que a moeda do lançamento é a da carteira
    CONSTRAINT wallets_id_currency_uq     UNIQUE (id, currency),
    CONSTRAINT wallets_currency_iso_ck    CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wallets_balance_nonneg_ck  CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_ck         CHECK (version >= 1)
);

-- Rede de proteção contra lost update e contra escrita fora do agregado:
-- saldo só muda junto com version + 1, e version só muda junto com saldo.
CREATE FUNCTION wallets_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wallets: delete is not allowed'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'wallets: initial version must be 1'
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;

    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallets: identity columns are immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF NEW.balance_minor <> OLD.balance_minor THEN
        IF NEW.version <> OLD.version + 1 THEN
            RAISE EXCEPTION 'wallets: balance change requires version = % (got %)',
                OLD.version + 1, NEW.version
                USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'wallets: version only changes together with balance'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER wallets_guard_trg
    BEFORE INSERT OR UPDATE OR DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallets_guard();

-- ---------------------------------------------------------------------------
-- wager_transactions: a operação em si + idempotência + máquina de estados.
-- origin = INTERNAL só para OPENING; HTTP/SQS para operações de provedor.
-- ---------------------------------------------------------------------------
CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL,
    kind                              TEXT        NOT NULL,
    status                            TEXT        NOT NULL,
    wallet_id                         UUID        NOT NULL REFERENCES wallets (id),
    player_id                         UUID        NOT NULL,
    amount_minor                      BIGINT      NOT NULL,
    currency                          CHAR(3)     NOT NULL,

    -- só para operações externas
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,       -- sha-256 hex do JSON canônico
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID        REFERENCES wager_transactions (id),

    -- resultado (é isso que o replay devolve)
    failure_code                      TEXT,
    result_balance_minor              BIGINT,

    -- retentativa de referência pendente
    reference_attempts                INT         NOT NULL DEFAULT 0,
    next_attempt_at                   TIMESTAMPTZ,
    reference_deadline_at             TIMESTAMPTZ,

    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    CONSTRAINT wtx_origin_ck CHECK (origin IN ('INTERNAL', 'HTTP', 'SQS')),
    CONSTRAINT wtx_kind_ck   CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wtx_status_ck CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wtx_currency_iso_ck CHECK (currency ~ '^[A-Z]{3}$'),

    -- interno x externo
    CONSTRAINT wtx_internal_is_opening_ck CHECK ((origin = 'INTERNAL') = (kind = 'OPENING')),
    CONSTRAINT wtx_internal_fields_ck CHECK (
        origin <> 'INTERNAL' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)),
    CONSTRAINT wtx_external_fields_ck CHECK (
        origin = 'INTERNAL' OR (
            provider_id IS NOT NULL AND external_transaction_id IS NOT NULL AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL AND round_id IS NOT NULL AND game_id IS NOT NULL)),
    CONSTRAINT wtx_payload_hash_ck CHECK (payload_hash IS NULL OR payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT wtx_opening_processed_ck CHECK (kind <> 'OPENING' OR status = 'PROCESSED'),

    -- política de valor: LOSS é zero, o resto é positivo
    CONSTRAINT wtx_amount_ck CHECK (CASE WHEN kind = 'LOSS' THEN amount_minor = 0 ELSE amount_minor > 0 END),

    -- referência
    CONSTRAINT wtx_reference_required_ck  CHECK (kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wtx_reference_forbidden_ck CHECK (kind <> 'BET' OR reference_external_transaction_id IS NULL),
    CONSTRAINT wtx_no_self_reference_ck   CHECK (reference_transaction_id IS NULL OR reference_transaction_id <> id),

    -- coerência da máquina de estados
    CONSTRAINT wtx_completed_ck CHECK ((completed_at IS NOT NULL) = (status IN ('PROCESSED', 'REJECTED', 'FAILED'))),
    CONSTRAINT wtx_failure_code_ck CHECK ((failure_code IS NOT NULL) = (status IN ('REJECTED', 'FAILED'))),
    CONSTRAINT wtx_processed_result_ck CHECK (status <> 'PROCESSED' OR result_balance_minor IS NOT NULL),
    CONSTRAINT wtx_processed_reversal_resolved_ck CHECK (
        status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL),
    CONSTRAINT wtx_pending_reference_ck CHECK (
        status <> 'PENDING_REFERENCE' OR (next_attempt_at IS NOT NULL AND reference_deadline_at IS NOT NULL)),
    CONSTRAINT wtx_result_balance_nonneg_ck CHECK (result_balance_minor IS NULL OR result_balance_minor >= 0),
    CONSTRAINT wtx_reference_attempts_ck CHECK (reference_attempts >= 0),

    -- idempotência: os dois índices também servem o replay e a resolução de referência
    CONSTRAINT wtx_provider_idempotency_key_uq UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wtx_provider_external_id_uq     UNIQUE (provider_id, external_transaction_id)
);

-- um único crédito inicial por carteira
CREATE UNIQUE INDEX wtx_opening_per_wallet_uq
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- no máximo uma reversão bem-sucedida (REFUND ou ROLLBACK) por transação referenciada
CREATE UNIQUE INDEX wtx_single_successful_reversal_uq
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

-- fila do worker de referências: só o que está pendente mora no índice
CREATE INDEX wtx_pending_reference_due_idx
    ON wager_transactions (next_attempt_at, id)
    WHERE status = 'PENDING_REFERENCE';

-- "acordar" quem espera por uma transação que acabou de ser processada.
-- Usar com FOR UPDATE SKIP LOCKED para não cruzar lock com o worker.
CREATE INDEX wtx_waiting_for_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

-- consultas por carteira (auditoria, reconciliação); id v7 já ordena por tempo
CREATE INDEX wtx_wallet_idx ON wager_transactions (wallet_id, id);

-- estado terminal não transita, identidade não muda, nada é apagado
CREATE FUNCTION wager_transactions_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager_transactions: delete is not allowed'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager_transactions: % is terminal (id %)', OLD.status, OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.amount_minor, NEW.currency,
        NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key, NEW.payload_hash,
        NEW.round_id, NEW.game_id, NEW.reference_external_transaction_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.amount_minor, OLD.currency,
        OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key, OLD.payload_hash,
        OLD.round_id, OLD.game_id, OLD.reference_external_transaction_id, OLD.created_at) THEN
        RAISE EXCEPTION 'wager_transactions: identity columns are immutable (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER wager_transactions_guard_trg
    BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();

-- ---------------------------------------------------------------------------
-- wallet_ledger_entries: append-only. Um lançamento por mudança de saldo.
-- wallet_version é a versão da carteira DEPOIS do lançamento: dá ordenação
-- estável por carteira (cursor da paginação) e impede dois lançamentos
-- disputando a mesma versão.
-- ---------------------------------------------------------------------------
CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL,
    transaction_id       UUID        NOT NULL REFERENCES wager_transactions (id),
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    wallet_version       BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT wle_wallet_currency_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    CONSTRAINT wle_wallet_transaction_uq UNIQUE (wallet_id, transaction_id),
    CONSTRAINT wle_wallet_version_uq     UNIQUE (wallet_id, wallet_version),
    CONSTRAINT wle_direction_ck CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wle_amount_ck    CHECK (amount_minor > 0),
    CONSTRAINT wle_balances_nonneg_ck CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),
    CONSTRAINT wle_wallet_version_ck  CHECK (wallet_version >= 1),
    CONSTRAINT wle_arithmetic_ck CHECK (
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor) OR
        (direction = 'DEBIT'  AND balance_after_minor = balance_before_minor - amount_minor))
);

CREATE FUNCTION wallet_ledger_entries_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only (% blocked)', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER wle_no_update_delete_trg
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_immutable();

CREATE TRIGGER wle_no_truncate_trg
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_ledger_entries_immutable();

-- Saldo e ledger confirmados juntos, garantido pelo banco: conferido no COMMIT.
-- Se o saldo mudou (ou nasceu positivo), tem que existir o lançamento daquela
-- versão com o mesmo saldo final. Custa um lookup no índice único acima.
CREATE FUNCTION wallets_require_ledger_entry() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.balance_minor = 0)
       OR (TG_OP = 'UPDATE' AND NEW.balance_minor = OLD.balance_minor) THEN
        RETURN NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM wallet_ledger_entries e
         WHERE e.wallet_id = NEW.id
           AND e.wallet_version = NEW.version
           AND e.balance_after_minor = NEW.balance_minor) THEN
        RAISE EXCEPTION 'wallet % reached version % without a matching ledger entry', NEW.id, NEW.version
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wallets_require_ledger_entry_trg
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallets_require_ledger_entry();

-- ---------------------------------------------------------------------------
-- inbox_messages: deduplicação durável do consumidor SQS.
-- Gravada na mesma transação SQL do tratamento da mensagem.
-- ---------------------------------------------------------------------------
CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   TEXT        NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions (id),
    outcome        TEXT,
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ,

    CONSTRAINT inbox_pk PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_payload_hash_ck CHECK (payload_hash ~ '^[0-9a-f]{64}$')
);

-- ---------------------------------------------------------------------------
-- outbox_events: o evento nasce na mesma transação do saldo; o publisher
-- publica depois. id = eventId estável entre republicações.
-- ---------------------------------------------------------------------------
CREATE TABLE outbox_events (
    id              UUID        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INT         NOT NULL,
    correlation_id  TEXT        NOT NULL,
    causation_id    TEXT,
    partition_key   TEXT        NOT NULL,       -- MessageGroupId (walletId)
    payload         JSONB       NOT NULL,       -- snapshot imutável do envelope
    occurred_at     TIMESTAMPTZ NOT NULL,

    -- estado de publicação (única parte mutável)
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,
    last_error      TEXT,
    published_at    TIMESTAMPTZ,

    CONSTRAINT outbox_aggregate_type_ck CHECK (aggregate_type IN ('wallet', 'wager_transaction')),
    CONSTRAINT outbox_event_type_ck CHECK (event_type IN (
        'WagerTransactionProcessed', 'WagerTransactionRejected',
        'WalletBalanceChanged', 'WagerTransactionPendingReference')),
    CONSTRAINT outbox_event_version_ck CHECK (event_version >= 1),
    CONSTRAINT outbox_attempts_ck      CHECK (attempts >= 0),
    CONSTRAINT outbox_lease_ck         CHECK ((locked_by IS NULL) = (locked_until IS NULL))
);

-- fila do publisher: só eventos não publicados
CREATE INDEX outbox_pending_idx
    ON outbox_events (next_attempt_at, id)
    WHERE published_at IS NULL;

-- métrica de atraso e diagnóstico por agregado
CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_id, id);

CREATE FUNCTION outbox_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.aggregate_type, NEW.aggregate_id, NEW.event_type, NEW.event_version,
        NEW.correlation_id, NEW.causation_id, NEW.partition_key, NEW.payload, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.id, OLD.aggregate_type, OLD.aggregate_id, OLD.event_type, OLD.event_version,
        OLD.correlation_id, OLD.causation_id, OLD.partition_key, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox_events: event snapshot is immutable (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'outbox_events: published_at cannot be rewritten (id %)', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER outbox_events_guard_trg
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_guard();

-- ---------------------------------------------------------------------------
-- Privilégios do papel de runtime. Sem DELETE/TRUNCATE em nada; ledger só INSERT.
-- O papel é criado fora da migration (init do Postgres no compose).
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions, inbox_messages, outbox_events TO wallet_app;
        GRANT SELECT, INSERT         ON wallet_ledger_entries TO wallet_app;
    END IF;
END;
$$;

COMMIT;
```

### 6.3 `migrations/000001_init.down.sql` (completo)

```sql
-- 000001_init.down.sql
-- Reverte a migration inicial. Ordem inversa das dependências.
-- DROP TABLE não dispara os triggers de proteção (eles cobrem UPDATE/DELETE/TRUNCATE).

BEGIN;

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;

DROP FUNCTION IF EXISTS outbox_events_guard();
DROP FUNCTION IF EXISTS wallets_require_ledger_entry();
DROP FUNCTION IF EXISTS wallet_ledger_entries_immutable();
DROP FUNCTION IF EXISTS wager_transactions_guard();
DROP FUNCTION IF EXISTS wallets_guard();

COMMIT;
```

### 6.4 O que já foi validado nesse rascunho

Rodado num PostgreSQL 16.15 real:

1. `up`, `down` e `up` de novo, sem erro. Resultado: 5 tabelas, 18 índices, 6 triggers.
2. **46 verificações** executadas com o papel `wallet_app` (o mesmo que a aplicação usaria), todas passando. Entre elas: carteira duplicada, carteira nascendo com saldo e sem ledger (falha no COMMIT), versão inicial diferente de 1, abertura completa no mesmo commit, segundo `OPENING`, saldo negativo, saldo sem subir versão, versão sem mudar saldo, saldo com versão certa mas sem ledger (falha no COMMIT), troca de moeda, DELETE sem permissão, BET completa, mesma chave, mesmo ID externo com outra chave, `ON CONFLICT DO NOTHING` devolvendo 0 linhas, transição de terminal, LOSS com valor, BET com zero, ROLLBACK sem referência, OPENING externo, REJECTED sem código, PENDING_REFERENCE sem agenda, ROLLBACK completo, segunda reversão da mesma BET, UPDATE/DELETE/TRUNCATE no ledger (como app e como dono das tabelas), aritmética errada, dois lançamentos por transação, dois lançamentos na mesma versão, moeda divergente, inbox com reentrega, claim da outbox com lease, payload imutável, `published_at` imutável, tipo de evento desconhecido, e a consulta de reconciliação batendo.
3. **Cenário obrigatório com duas sessões concorrentes reais**: carteira com 100.00, duas BETs de 80.00 ao mesmo tempo, uma com origem HTTP e outra SQS. Com `FOR UPDATE` deu deadlock (ver 3.2). Com `FOR NO KEY UPDATE`, três execuções seguidas: uma `PROCESSED`, uma `REJECTED` por saldo insuficiente, saldo final 20.00, versão 2, um débito no ledger.
4. **A mesma BET 30 vezes em paralelo**: uma linha só em `wager_transactions`, nenhum lançamento a mais.
5. **`EXPLAIN`** das consultas dos workers e da paginação usando `outbox_pending_idx`, `wtx_pending_reference_due_idx` e `wle_wallet_version_uq`.

Esses testes foram feitos em shell com `psql`. Eles precisam ser reescritos como testes de integração em Go.

### 6.5 Consultas de referência [PROPOSTO]

Fluxo central (o que o caso de uso executa dentro da unidade de trabalho):

```sql
-- 1. (só SQS) inbox
INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING message_id;
-- 0 linhas: reentrega. Confere o hash, confirma e apaga a mensagem.

-- 2. registra a operação (arbitragem de idempotência ANTES do lock da carteira)
INSERT INTO wager_transactions (...) VALUES (..., 'PENDING', ...)
ON CONFLICT DO NOTHING
RETURNING id;
-- 0 linhas: busca a existente por (provider_id, idempotency_key) e por
-- (provider_id, external_transaction_id); compara payload_hash.
--   igual     -> replay do resultado persistido
--   diferente -> conflito

-- 3. lock por carteira
SELECT id, player_id, currency, balance_minor, version, created_at, updated_at
  FROM wallets WHERE id = $1
   FOR NO KEY UPDATE;

-- 4. (REFUND/ROLLBACK) resolve a referência
SELECT ... FROM wager_transactions
 WHERE provider_id = $1 AND external_transaction_id = $2;

-- 5. domínio decide. Se houver movimentação:
INSERT INTO wallet_ledger_entries (...) VALUES (...);
UPDATE wallets
   SET balance_minor = $1, version = version + 1, updated_at = $2
 WHERE id = $3 AND version = $4;          -- 0 linhas afetadas = conflito, desfaz

-- 6. status final (PROCESSED, REJECTED ou PENDING_REFERENCE)
UPDATE wager_transactions SET status = ..., result_balance_minor = ..., completed_at = ... WHERE id = $1;

-- 7. eventos
INSERT INTO outbox_events (...) VALUES (...);
```

Claim do publisher da outbox (transação curta, publica fora dela):

```sql
UPDATE outbox_events
   SET locked_by = $1, locked_until = now() + interval '30 seconds', attempts = attempts + 1
 WHERE id IN (
        SELECT id FROM outbox_events
         WHERE published_at IS NULL
           AND next_attempt_at <= now()
           AND (locked_until IS NULL OR locked_until < now())
         ORDER BY next_attempt_at, id
           FOR UPDATE SKIP LOCKED
         LIMIT $2)
RETURNING *;

-- sucesso
UPDATE outbox_events SET published_at = now(), locked_by = NULL, locked_until = NULL WHERE id = $1;
-- falha: backoff
UPDATE outbox_events SET next_attempt_at = $2, last_error = $3, locked_by = NULL, locked_until = NULL WHERE id = $1;
```

Worker de referências (tudo SQL, então a própria trava da linha é o claim, sem lease):

```sql
SELECT ... FROM wager_transactions
 WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= now()
 ORDER BY next_attempt_at, id
   FOR UPDATE SKIP LOCKED
 LIMIT $1;
-- para cada uma, na mesma transação: lock da carteira (FOR NO KEY UPDATE),
-- tenta resolver, aplica ou reagenda (reference_attempts + 1, next_attempt_at com backoff),
-- ou rejeita se passou de reference_deadline_at.
```

Acordar quem espera por uma transação recém-processada (sempre com SKIP LOCKED, ver 3.6):

```sql
UPDATE wager_transactions SET next_attempt_at = now()
 WHERE id IN (
        SELECT id FROM wager_transactions
         WHERE status = 'PENDING_REFERENCE'
           AND provider_id = $1 AND reference_external_transaction_id = $2
           FOR UPDATE SKIP LOCKED);
```

Paginação do ledger (o cursor opaco é o `wallet_version` codificado):

```sql
SELECT ... FROM wallet_ledger_entries
 WHERE wallet_id = $1 AND wallet_version > $2
 ORDER BY wallet_version
 LIMIT $3;
```

Reconciliação (visão consistente, sem alterar nada):

```sql
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT balance_minor, currency FROM wallets WHERE id = $1;
SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0) AS calculated,
       COUNT(*) AS checked_entries
  FROM wallet_ledger_entries WHERE wallet_id = $1;
COMMIT;
-- difference = armazenado - reconstruído. Divergência vai para a resposta, para o log e para uma métrica.
-- Extra possível: conferir o encadeamento (balance_before de um = balance_after do anterior) com LAG().
```

---

## 7. Testes

### 7.1 Helper de suíte de integração `test/testenv` [FECHADO na ideia, PROPOSTO na API]

Ideia do Matheus: uma pasta de helpers que entrega uma suíte pronta. Os containers sobem via testcontainers **a partir de arquivos YAML**, para editar o YAML e não o código, inclusive os limites de recurso.

Como fazer: o `testcontainers-go` tem um módulo `compose` que sobe a stack a partir de arquivos compose, espera os serviços ficarem saudáveis e devolve host e porta de cada um. Custo: ele puxa o docker/compose como dependência e o `go.mod` engorda. Os arquivos usados são os mesmos do ambiente local (`deployments/docker/postgres.yml`, `keycloak.yml`, `ministack.yml`), passados direto para o helper, sem o `compose-main.yml` e sem o app, porque nos testes o app sobe como processo.

```go
func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) } // sobe a stack uma vez

func TestBet(t *testing.T) {
    env := testenv.New(t)        // banco novo, filas novas, cleanup automático
    env.DB                       // pgxpool
    env.SQS                      // client + URLs das filas
    env.Token("provider-a")      // token real do Keycloak
    apps := env.StartApp(3)      // 3 processos do binário neste ambiente
    apps[0].Kill()               // SIGKILL para os testes de falha
}
```

Decisões dentro do helper:

1. **Todos os testes de integração num pacote só**, com um `TestMain`. O Keycloak leva uns 20 a 30 segundos para subir e não dá para pagar isso por pacote.
2. **Isolamento por teste com template database.** A migration roda uma vez num banco modelo e cada teste faz `CREATE DATABASE ... TEMPLATE`, que leva milissegundos. Importa aqui porque o ledger está protegido contra DELETE e TRUNCATE, então limpar tabela entre testes brigaria com a nossa própria proteção. Filas: nomes únicos por teste (FIFO tem que terminar em `.fifo`).
3. **`StartApp(n)`** compila o binário uma vez e sobe N processos reais via `exec`, cada um com memória e conexões próprias. É o jeito mais simples de cumprir a exigência de três instâncias independentes e de matar processo em ponto específico.
4. **Failpoints** ligados por variável de ambiente, compilados só com build tag (seção 9.9).

### 7.2 Cenários obrigatórios do README

Unitários: parsing e operações de `Money`, escala, limites numéricos, entradas inválidas, moedas incompatíveis, invariantes da carteira, transições de estado, regras dos cinco tipos externos, conflito de payload para a mesma chave, política de zero por tipo, abertura interna com seus metadados e eventos.

Integração com containers reais: migrations, constraints, imutabilidade do ledger, atomicidade financeira, inbox, reentrega, outbox concorrente, retry, DLQ, recuperação após reinício. Mais a verificação da composição Fx com início e encerramento, incluindo liberação de recursos dos workers.

Autenticação: integração real com o IdP; rejeição de credencial ausente, inválida ou expirada; isolamento entre provedores, inclusive em consultas e replays; restrição das operações internas; nenhum efeito financeiro nem exposição de dados em acesso não autorizado.

Concorrência e recuperação:

| # | Cenário | Como provar |
| --- | --- | --- |
| 1 | Mesma aposta 50 vezes em paralelo | Um único débito, 49 replays |
| 2 | Duas apostas de 80.00 sobre 100.00 | Uma processada, uma rejeitada por saldo, saldo 20.00, um débito. Reenvios não mudam o resultado |
| 3 | Carteiras distintas ao mesmo tempo | Avançam em paralelo |
| 4 | Repetir os cenários relevantes com 3 instâncias | `env.StartApp(3)` |
| 5 | Matar o consumidor depois do commit e antes de apagar a mensagem | Failpoint + reentrega sem efeito duplicado |
| 6 | Dois publishers disputando a mesma outbox | Sem perda, `eventId` preservado na republicação |
| 7 | `REFUND` ou `ROLLBACK` antes da referência | Resolução posterior, ou rejeição por expiração |
| 8 | Reiniciar a aplicação | Idempotência, pendências e consistência preservadas |

Ao final de cada cenário financeiro: saldo armazenado igual à soma de créditos menos débitos do ledger. Incluir cenários que cruzem HTTP e SQS para a mesma operação. Os testes de duplicidade têm que exercitar a deduplicação **da aplicação**, com recebimentos repetidos comprovados (não vale depender da deduplicação do FIFO).

Comandos exigidos: `docker compose up --build`, `go test ./...`, `go test -race ./...`, `go vet ./...`. Documentar à parte como rodar integração, múltiplas instâncias e simulações de falha, e as build tags.

---

## 8. Bibliotecas [PROPOSTO]

| Uso | Biblioteca | Observação |
| --- | --- | --- |
| Composição | `go.uber.org/fx` | Obrigatória |
| HTTP | `github.com/go-chi/chi/v5` | Já usada pelo Matheus |
| Postgres | `github.com/jackc/pgx/v5` + `pgxpool` | SQL explícito. `sqlc` opcional |
| Migrations | `golang-migrate/migrate` | up e down |
| SQS | `aws-sdk-go-v2` (`service/sqs`) | Endpoint configurável |
| OIDC/JWT | `github.com/coreos/go-oidc/v3` | Validação por JWKS do Keycloak |
| Validação de DTO | `github.com/go-playground/validator/v10` | É a "de anotações" que o Matheus usa na Giro. **Só nos DTOs de borda** (HTTP e SQS). As regras ficam nos construtores do domínio, que não pode depender de lib |
| UUID | `github.com/google/uuid` | v7 |
| Logs | `log/slog` com handler JSON | |
| Métricas | `github.com/prometheus/client_golang` | |
| Testes | `testcontainers-go` + módulo `compose`, `testify` | |
| Mocks | `go.uber.org/mock` | Uso mínimo |
| Dinheiro | Implementação própria com `int64` | Sem lib decimal |

---

## 9. O que falta planejar

Os itens 9.1 a 9.7 têm decisão de verdade. Os itens 9.8 a 9.12 são mais mecânicos. Em cada um vai um ponto de partida já pensado, marcado como proposta.

### 9.1 Contrato HTTP: códigos e corpo de erro [PENDENTE]

O README pede que entrada inválida, conflito, rejeição de negócio, processamento pendente e indisponibilidade transitória sejam distinguíveis pelo contrato, com códigos e corpos documentados.

Ponto de partida [PROPOSTO]:

| Situação | HTTP |
| --- | --- |
| Operação nova processada | 201 |
| Replay de operação concluída | mesmo código do resultado original, com `idempotentReplay: true` |
| Reversão aguardando referência | 202, status `PENDING_REFERENCE` |
| Rejeição de negócio (persistida) | 422, com `transactionId`, status `REJECTED`, `failureCode` e saldo |
| Mesma chave com payload diferente, ou mesmo ID externo com outra chave | 409 |
| Carteira já existe para jogador e moeda | 409 |
| JSON malformado, dinheiro inválido, header ausente, `OPENING` externo | 400 |
| Token ausente, inválido ou expirado | 401 |
| Token válido, mas `providerId` do corpo diferente do token, ou sem permissão | 403 |
| Recurso não encontrado, inclusive transação de outro provedor (não vazar existência) | 404 |
| Limitador cheio, `lock_timeout`, banco ou SQS fora | 503 com `Retry-After` |

A decidir: formato do corpo de erro (JSON simples com `code` estável, ou `application/problem+json`), e se rejeição de negócio é 422 ou 200 com status `REJECTED`.

### 9.2 Catálogo de failure codes [PENDENTE]

O README exige `failureCode` estável e documentado, distinguindo entradas corrigíveis de resultados definitivos, e código de reversão sem saldo diferente do de aposta sem saldo.

Ponto de partida [PROPOSTO]:

| Código | Quando |
| --- | --- |
| `INSUFFICIENT_FUNDS` | BET sem saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | ROLLBACK de WIN ou de REFUND que levaria o saldo abaixo de zero |
| `REFERENCE_NOT_FOUND` | Referência não chegou dentro do prazo |
| `REFERENCE_NOT_PROCESSED` | Referência existe, mas terminou `REJECTED` ou `FAILED` |
| `REFERENCE_MISMATCH` | Provedor, jogador, carteira, moeda, rodada ou valor não batem com a original |
| `REFERENCE_KIND_NOT_ALLOWED` | REFUND de algo que não é BET; ROLLBACK de LOSS, ROLLBACK ou OPENING |
| `ALREADY_REVERSED` | A referência já tem uma reversão bem-sucedida |
| `CURRENCY_MISMATCH` | Moeda da operação diferente da carteira |
| `WALLET_PLAYER_MISMATCH` | `playerId` não é o dono da carteira |

A decidir: carteira inexistente não pode ser persistida (a FK impede), então vira erro de validação não persistido (404 ou 422). Definir quais erros são "corrigíveis" (não persistidos, o cliente conserta e reenvia com a mesma chave) e quais são "definitivos" (persistidos como `REJECTED`).

### 9.3 Payload dos eventos e destino [PENDENTE]

Definir os quatro tipos concretos em Go, o `data` de cada um, a versão inicial, quem é o `aggregateId` de cada evento (carteira para `WalletBalanceChanged`, transação para os outros), de onde vem `correlationId` (header de entrada ou gerado) e `causationId` (ID da transação ou `messageId`). Eventos de origem interna (`OPENING`) não carregam os metadados externos. Confirmar o destino (`wallet-events.fifo`) e documentar contrato de roteamento e consumo.

### 9.4 Especificação do hash canônico [PENDENTE]

Ponto de partida [PROPOSTO]:

- SHA-256, saída em hex minúsculo (o schema já valida 64 caracteres hex).
- JSON canônico: UTF-8, chaves ordenadas, sem espaços, sem escape de HTML (em Go, `json.Encoder` com `SetEscapeHTML(false)` sobre um mapa, porque o `encoding/json` ordena chaves de mapa).
- Campos: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency`, `referenceExternalTransactionId` (só quando presente).
- Fora do hash: chave de idempotência, `messageId`, `type`, `occurredAt`, headers.
- Normalização: aceitar dinheiro **só na forma canônica** (`^\d+\.\d{2}$`), rejeitando `25` e `25.0`. Assim não existe normalização para documentar. UUIDs em minúsculo.

A decidir: forma estrita ou aceitar equivalentes e normalizar antes do hash (o README permite, desde que documentado).

### 9.5 Autenticação e autorização no Keycloak [PENDENTE]

Requisitos do README: integração real com IdP OIDC, `client_credentials`, a identidade autenticada determina o `providerId` autorizado, provedor só acessa as próprias transações (inclusive replays), operações de carteira restritas ao serviço interno, provisionamento automático do IdP com identidades de teste, acesso à mensageria controlado por credenciais e políticas do broker. Cadastro de senha e emissão própria de token estão fora do escopo.

Ponto de partida [PROPOSTO]:

- Realm importado no boot por `realm.json` (`--import-realm`).
- Clients confidenciais com service account: `provider-a`, `provider-b` (identidades de teste) e `wallet-internal`.
- `providerId` vem de um claim `provider_id` (protocol mapper fixo em cada client de provedor), não do `client_id`, para desacoplar.
- Papéis: provedores com escrita e leitura de wagering; `wallet-internal` com administração de carteira.
- A API valida assinatura via JWKS (com cache e recarga em `kid` desconhecido), `iss`, `aud`, `exp`.
- Regras: `providerId` do corpo tem que ser igual ao do token (403); `GET /providers/:providerId/...` idem; `GET /wagering/transactions/:id` de outro provedor devolve 404; endpoints de carteira, ledger e reconciliação só para o serviço interno; health público.
- SQS: o `providerId` da mensagem é confiável porque só produtores autorizados conseguem escrever na fila (política da fila e credenciais). Documentar a política mesmo que o emulador não a imponha. As validações de domínio continuam rodando no consumidor.

A decidir: nomes de papéis ou escopos, quem pode ler carteira (só interno, ou o provedor também), e como testar token expirado (tempo de vida curto no realm de teste).

### 9.6 Política do consumidor SQS [PENDENTE]

O README pede documentar limites de tentativas, visibility timeout, tratamento de mensagem inválida, `MessageGroupId` e `MessageDeduplicationId`.

Ponto de partida [PROPOSTO]:

- Long polling, até 10 mensagens por chamada, N goroutines consumidoras.
- Visibility timeout de 30 s, com prazo de processamento menor que isso.
- Falha transitória: não apaga; `ChangeMessageVisibility` com backoff exponencial baseado em `ApproximateReceiveCount`.
- `maxReceiveCount = 5`, depois DLQ pelo redrive.
- Mensagem inválida (JSON quebrado, schema inválido, `OPENING`): retry não conserta. Enviar explicitamente para a DLQ e apagar a original, com métrica. Pior caso é duplicata na DLQ, que é inofensiva.
- Reentrega com mesmo `messageId` e hash diferente: tratar como inválida.
- `MessageGroupId = walletId` (ordem por carteira, paralelismo entre carteiras, e no máximo uma mensagem por carteira em processamento). `MessageDeduplicationId = messageId`. Não contar com a deduplicação do FIFO para correção.
- `SIGTERM`: para de buscar, conclui o que está em andamento dentro do prazo, e libera a visibilidade do que sobrar.

A decidir: os números, e se mensagem inválida fica registrada na inbox com desfecho `INVALID`.

### 9.7 Semântica do `FAILED` e falhas transitórias x permanentes [PENDENTE]

No desenho síncrono, falha transitória desfaz tudo e nada é persistido (HTTP devolve 503, a mensagem SQS volta para a fila). Então quando uma transação vira `FAILED`? Ponto de partida [PROPOSTO]: só no worker de referências, quando uma pendência bate num erro não recuperável que não é regra de negócio. Documentar a classificação de erros: transitório (conexão, timeout, `40001`, `40P01`, `55P03`, throttling do SQS) e permanente (violação inesperada de invariante, dado corrompido).

Junto com isso, fechar os números do worker de referências: backoff, máximo de tentativas ou prazo, e a relação com a janela de retry do SQS (ver 3.6).

### 9.8 Regra de reversão única [PROPOSTO, confirmar]

Confirmar a regra da seção 3.6 (no máximo uma reversão bem-sucedida por transação, REFUND ou ROLLBACK) e o texto da limitação do caso BET, REFUND, ROLLBACK do REFUND. Definir também se `WIN` com referência a uma BET que ainda não chegou fica `PENDING_REFERENCE` ou é processado sem resolver.

### 9.9 Failpoints para os testes de falha [PENDENTE]

Ponto de partida [PROPOSTO]: pacote na infraestrutura com `failpoint.Hit("nome")`, que lê uma variável de ambiente e encerra o processo na hora. Sem a build tag, compila como no-op. Pontos necessários:

- depois do commit e antes do `DeleteMessage` (cenário 5);
- depois de publicar o evento e antes de marcar `published_at` (cenário 6);
- depois de persistir `PENDING_REFERENCE` (cenários 7 e 8).

"Entre commit e publicação" não precisa de failpoint: basta matar a instância ou subir sem o publisher.

### 9.10 Tipos do domínio em Go [PENDENTE, mecânico]

Assinaturas de `Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry`, eventos e erros sentinela. Portas de repositório no domínio. Decidir organização dos pacotes (por camada como na Stone, com subpacotes por conceito).

### 9.11 Grafo do Fx [PENDENTE, mecânico]

Fechar a lista de módulos da seção 4.5, o que cada um provê, a ordem dos hooks e o teste de composição (`fx.ValidateApp` mais um teste de start e stop com a infraestrutura real).

### 9.12 Observabilidade [PENDENTE, mecânico]

Logs JSON com `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`. Sem credenciais, dados sensíveis ou payload financeiro completo.

Métricas pedidas pelo README, com nomes de partida [PROPOSTO]:

| Pedido | Métrica |
| --- | --- |
| Resultados por status | `wager_transactions_total{kind,status,origin}` |
| Duplicatas | `wager_idempotent_replays_total{origin}`, `wager_idempotency_conflicts_total` |
| Retries | `sqs_retries_total`, `reference_retries_total`, `outbox_publish_attempts_total{result}` |
| DLQ | `sqs_dlq_total{reason}` |
| Conflitos de concorrência | `wallet_concurrency_conflicts_total{reason}` (versão, deadlock, lock timeout) |
| Atraso da outbox | `outbox_pending`, `outbox_oldest_pending_age_seconds` |
| Latência | `wager_processing_duration_seconds{origin,kind}` |
| Divergência de reconciliação | `reconciliation_divergences_total` |
| Extras do limitador | `http_requests_in_flight`, `http_requests_shed_total` |

### 9.13 Documentação da entrega [PENDENTE]

- `README.md`: pré-requisitos, variáveis de ambiente, inicialização das filas, aplicação e reversão das migrations, execução, exemplos de chamadas autenticadas, comandos de teste.
- `.env.example` com valores locais, sem segredo real.
- `ARCHITECTURE.md` com as decisões sobre: dinheiro (representação e limites), biblioteca de banco e mapeamento de `Money`, delimitação da transação SQL entre repositórios, idempotência (algoritmo do hash, campos, normalizações), locks e estratégia de concorrência, máquina de estados, falhas transitórias x permanentes, referências pendentes, reversões (inclusive REFUND × ROLLBACK), inbox e outbox, `MessageGroupId` e `MessageDeduplicationId`, contratos dos eventos e roteamento, códigos HTTP e failure codes, autenticação e autorização, uso do Fx e shutdown, escolha do IdP. E uma seção explícita de limitações, interpretações adotadas e trabalho não concluído.
- Provisionamento automático do IdP, identidades de teste e instruções dos fluxos autenticados.
- Código formatado com `gofmt`, dependências reproduzíveis.

---

## 10. Ordem de implementação [FECHADO, exceto o spike do passo 1, que é PROPOSTO]

1. **Spike do MiniStack** (meio dia): validar FIFO, visibility, redrive.
2. **Esqueleto**: `go.mod`, Fx, config, compose por serviço + main, Dockerfile, migrations, health.
3. **`Money` + `Wallet` + abertura de carteira** (OPENING, ledger e outbox gravados no mesmo commit).
4. **BET, WIN e LOSS com idempotência**, e já os dois testes de concorrência (50 duplicatas e 80/80). Nesse ponto o núcleo que vale mais ponto está provado.
5. **Autenticação**, cedo porque é eliminatório.
6. **Consumidor SQS + inbox + DLQ**, com os testes que cruzam HTTP e SQS.
7. **Publisher da outbox** e os testes de recuperação.
8. **REFUND, ROLLBACK e o worker de referências.**
9. **Leituras**: consultas de transação, ledger paginado, reconciliação.
10. **Observabilidade.**
11. **Testes multi-instância e de falha, e a documentação.**

Antes do passo 2, fechar em conversa os itens 9.1 a 9.8 (pelo menos 9.1, 9.2, 9.4 e 9.5, que afetam contrato e schema) e revisar a migration da seção 6.

---

## 11. Próxima sessão: por onde começar

1. Ler o README do desafio e este documento.
2. Revisar com o Matheus a migration da seção 6 (ele ainda não revisou).
3. Discutir e fechar os itens [PENDENTE] e [PROPOSTO] da seção 9, um por vez, antes de gerar código.
4. Só então seguir a ordem da seção 10.
