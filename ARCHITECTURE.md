# Arquitetura

Decisões que sustentam o serviço, por que elas foram tomadas e o que ficou de fora.

## Visão geral

Uma operação de provedor entra por HTTP ou pela fila FIFO e segue o mesmo caso de uso, dentro de **uma única transação SQL**:

```
registra a operação (ON CONFLICT DO NOTHING)   arbitragem de idempotência, antes do lock
SELECT ... FROM wallets FOR NO KEY UPDATE      lock por carteira
resolve a referência, quando existe
o domínio decide: processar, rejeitar ou esperar
grava lançamento + saldo + status + eventos na outbox
acorda pendências que esperavam por esta operação
COMMIT
```

Só depois do commit a resposta sai e a mensagem é apagada da fila. Um worker separado publica os eventos da outbox, e outro retenta as reversões que ainda não acharam sua referência. Tudo isso roda no mesmo binário, composto com Uber Fx, e qualquer parte pode ser desligada por variável.

Não é event sourcing. É estado mais ledger append-only confirmados juntos, com reconciliação para provar que batem.

## Camadas

O domínio não importa nada de fora. Ele guarda as entidades, as regras e também os **contratos de repositório**, um por arquivo em `internal/domain/repositories`: quem decide o que precisa ser persistido é quem conhece a regra, não quem conhece o banco. `Registry` reúne esses contratos amarrados a uma transação e `UnitOfWork` é a porta que abre e fecha essa transação.

A aplicação tem três pastas com papéis distintos:

| Pasta | O que vive ali |
| --- | --- |
| `application/usecase` | um caso de uso por arquivo, cada um com sua interface e a implementação privada atrás do construtor |
| `application/dto` | entradas e saídas dos casos de uso, com as anotações de validação e o `Validate()` de cada entrada |
| `application/dto/message` | o contrato das mensagens da fila, que carrega a chave de idempotência dentro do corpo porque fila não tem cabeçalho |
| `application/dto/validation` | o motor de validação, as regras próprias (`opaque`, `wager_kind`) e o erro que lista todos os campos errados de uma vez |
| `application/mapper` | um mapper por arquivo, traduzindo DTO para objeto de domínio e de volta |
| `application/port` | o que a aplicação precisa do mundo e não é repositório: relógio, gerador de id, notificador de eventos, métricas e failpoints. Uma interface por arquivo |

O caso de uso valida a própria entrada. A borda HTTP e a borda da fila só decodificam o JSON e chamam o mapper; as regras de tamanho, formato e política por tipo de operação ficam num lugar só. Os dois contratos de entrada existem separados de propósito: o HTTP recebe a chave de idempotência num cabeçalho e a fila recebe dentro do corpo, e é a única diferença entre eles.

Na infraestrutura a mesma regra vale para baixo: `postgres` cuida de conexão e transação, `postgres/repositories` tem um arquivo por repositório SQL, `sqs` cuida de client e consumo, `sqs/listener` tem um listener por fila de entrada e `sqs/publisher` um publicador por destino de saída. Cada borda guarda o próprio formato de fio: o corpo HTTP vive em `infrastructure/http` e a mensagem da fila em `application/dto/message`.

Interface no caso de uso não é cerimônia: é o que permite trocar a implementação no teste da borda e o que o `mockgen` precisa. Os mocks de tudo isso ficam em `internal/mocks`, gerados por `go generate ./internal/mocks/`.

O relógio e o gerador de id são interfaces pelo mesmo motivo: sem eles, o teste do caso de uso não consegue afirmar que a próxima tentativa de uma pendência cai exatamente em `agora + backoff`. Em produção quem os implementa é `internal/infrastructure/system`.

## Dinheiro

`Money` é um value object imutável: `int64` em centavos mais a moeda. Nunca existe float em parsing, cálculo, serialização ou persistência.

- Entrada e saída sempre como string: `{"amount":"25.00","currency":"BRL"}`. Número JSON é rejeitado no decode.
- A forma canônica é `^(0|[1-9][0-9]{0,16})\.[0-9]{2}$`. `"25"`, `"25.0"` e `"025.00"` são 400. Como não existe forma equivalente aceita, não há normalização de valor a documentar.
- Moedas suportadas: **BRL e USD**, as duas com duas casas. `JPY` e `brl` são inválidas nesta API. Não há câmbio.
- Limite: `92233720368547758.07`. Soma, subtração, negação e parsing tratam overflow.
- Persistência: `BIGINT` em centavos mais `CHAR(3)`, com `CHECK` da lista de moedas em toda coluna monetária.

Um crédito que passaria do limite não é um erro de infraestrutura: vira a rejeição de negócio `BALANCE_LIMIT_EXCEEDED`, com saldo e versão preservados. Valor de entrada acima do limite é 400, antes de persistir qualquer coisa.

## Banco e unidade de trabalho

`pgx/v5` com SQL explícito, sem ORM. O domínio não conhece `pgx`, SQL nem tags de persistência; os modelos vivem em `internal/infrastructure/postgres`.

A transação é explícita, não viaja escondida no `context`:

```go
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, registry Registry) error) error
	Read(ctx context.Context, fn func(ctx context.Context, registry Registry) error) error
}
```

`Do` abre a transação, aplica `SET LOCAL lock_timeout` e `statement_timeout`, monta os repositórios amarrados a ela e confirma ou desfaz. `fn` devolve `nil` para confirmar, inclusive quando a decisão foi rejeitar por regra de negócio, e `error` só quando a tentativa não pode persistir. `Read` abre `REPEATABLE READ READ ONLY` e é o que a reconciliação usa.

`Money` vira dois parâmetros na borda do repositório (`amount_minor`, `currency`) e volta por `money.FromMinor`. O saldo devolvido numa transação usa sempre a moeda da carteira, inclusive quando a operação rejeitada veio em outra moeda.

O dado mora num volume nomeado montado em `/var/lib/postgresql`, e o `PGDATA` aponta para `/var/lib/postgresql/18/docker`. A imagem 18 passou a usar esse caminho com a versão no meio, então um volume de uma versão anterior não é aproveitado por engano: o servidor sobe vazio em vez de tentar ler um diretório de formato diferente. Destruir e recriar o container mantém carteiras, ledger e histórico de migrations; quem quer começar do zero usa `docker compose down -v`.

### O pool é um orçamento

O pool de uma instância não é um número solto: ele é dividido entre quem pode segurar uma conexão ao mesmo tempo. O semáforo de escrita fica com `HTTP_WRITE_CONCURRENCY`, o consumidor com `SQS_CONSUMER_WORKERS`, cada job ligado com uma, e `DB_POOL_RESERVED` sobra para leituras, health e encerramento.

`config.ConnectionBudget()` calcula essa soma a partir das mesmas variáveis que dimensionam cada componente, então desligar o consumidor ou subir o limitador move a conta junto. Se a soma passar de `DB_POOL_MAX_CONNS`, o processo recusa subir e diz a conta. A alternativa seria descobrir isso em produção como latência esperando no pool, que é o pior lugar para descobrir.

## Concorrência

A coordenação é por carteira, com lock pessimista:

```sql
SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE
```

**Por que não `FOR UPDATE`.** O INSERT da operação acontece antes do lock e sua chave estrangeira para `wallets` pega `FOR KEY SHARE` na linha da carteira. Duas operações concorrentes ficam cada uma com KEY SHARE e, ao pedirem `FOR UPDATE`, uma espera a outra: deadlock. Isso foi reproduzido com duas sessões reais no cenário das duas apostas de 80,00. `FOR NO KEY UPDATE` não conflita com KEY SHARE, continua serializando os escritores e é o mesmo nível que o UPDATE de saldo e versão usa, já que nenhuma coluna-chave muda.

**Primeiro o lock, depois o saldo.** Saldo lido fora do lock é palpite. Quem chega depois espera na fila do Postgres e lê o saldo já atualizado.

Defesa em profundidade, toda no banco:

- `UPDATE wallets ... WHERE id = $1 AND version = $lida`: zero linhas significa conflito e desfaz
- `CHECK (balance_minor >= 0)` e trigger que acopla saldo e versão
- constraint triggers deferidos que, no COMMIT, exigem que cada mudança de saldo tenha o lançamento com os valores exatos de antes e depois, que a cadeia de versões não tenha buraco e que a operação só fique PROCESSED com a movimentação que o seu tipo implica
- `lock_timeout` por transação: quem espera demais recebe 503 com `Retry-After` em vez de prender conexão

Carteiras diferentes andam em paralelo. Não existe mutex global, advisory lock de chave fixa nem consumidor único.

Cada instância tem pool próprio (`DB_POOL_MAX_CONNS`, padrão 20) e um semáforo nas rotas de escrita (`HTTP_WRITE_CONCURRENCY`, padrão 12) que responde 503 quando a espera passa de `HTTP_WRITE_QUEUE_TIMEOUT`. Health e métricas ficam fora do semáforo: sob carga, o readiness não pode falhar e derrubar a instância.

## Idempotência

São duas coisas diferentes:

- **Chave de idempotência**: vem do cliente (`Idempotency-Key` no HTTP, `data.idempotencyKey` no SQS). O servidor nunca troca a chave recebida por outra calculada.
- **Hash do payload**: calculado por nós sobre os campos de negócio, para detectar a mesma chave chegando com conteúdo diferente.

O hash é SHA-256 em hex minúsculo do JSON canônico de `externalTransactionId`, `gameId`, `kind`, `money`, `playerId`, `providerId`, `referenceExternalTransactionId` (só quando presente), `roundId` e `walletId`. Chaves em ordem lexicográfica, sem espaços, sem escape de HTML, UTF-8. Ficam de fora a chave de idempotência, o `messageId`, o `type`, o `occurredAt` e qualquer header. A única normalização é UUID em minúsculas. HTTP e SQS passam pela mesma função, então o mesmo conteúdo produz o mesmo hash nas duas portas.

No banco, dois índices únicos: `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. A chave é única por provedor, então um provedor não colide nem sonda chaves de outro.

O registro da operação acontece **antes** do lock da carteira. Se o `INSERT ... ON CONFLICT DO NOTHING` devolve zero linhas, consultamos os dois índices para saber o que aconteceu:

| Situação | Resposta |
| --- | --- |
| Mesma chave, mesmo hash | Replay do resultado persistido, com o saldo observado no processamento original |
| Mesma chave, hash diferente | `409 IDEMPOTENCY_KEY_CONFLICT` |
| Mesmo ID externo com outra chave | `409 EXTERNAL_TRANSACTION_ID_CONFLICT` |

O 409 não grava nada novo, mas também não libera a chave nem o ID externo: eles continuam ligados à operação que já existe. Corrigir o payload não reabre uma chave consumida. Já 400 e 404 numa operação nova não reservam nada, então o cliente conserta e reenvia com a mesma chave.

## Máquina de estados

```
PENDING ──> PROCESSED | REJECTED | PENDING_REFERENCE
PENDING_REFERENCE ──> PROCESSED | REJECTED | FAILED   (ou continua, ao reagendar)
PROCESSED, REJECTED, FAILED: terminais
```

`OPENING` nasce `PROCESSED`. Voltar para `PENDING` é proibido, e `PENDING` não sobrevive ao COMMIT de uma operação nova: no desenho síncrono a linha nasce e termina na mesma transação. O domínio recusa a transição com erro classificável e o banco recusa de novo por trigger.

### Transitório, permanente e o commit sem resposta

- **Abortamento conhecido**: o servidor respondeu com erro, ou a falha aconteceu antes de o COMMIT ser enviado. Nada persistiu. `40001` e `40P01` são reexecutados até `DB_RETRY_ATTEMPTS`; `55P03` vira 503 sem retry.
- **Resultado incerto**: a conexão caiu enquanto esperávamos a resposta do COMMIT. Não dá para afirmar que não persistiu, e por isso a unidade de trabalho devolve `ErrCommitOutcomeUnknown`. O HTTP responde `503 COMMIT_OUTCOME_UNKNOWN` com `Retry-After`, sem prometer ausência de efeito; a recuperação é repetir a chamada com os mesmos identificadores, e a idempotência resolve tanto o caso em que o commit ocorreu quanto o caso em que não. Trocar os identificadores criaria outra operação. No SQS a mensagem não é apagada; na abertura de carteira, a unicidade `(playerId, currency)` devolve 409 com o `walletId` existente.
- **Permanente**: violação inesperada de invariante ou dado corrompido ao aplicar uma pendência. Só o worker de referências grava `FAILED`, numa transação separada, depois de confirmar o abortamento e reler a pendência ainda aberta sob lock. Panics dentro da unidade de trabalho são convertidos em erro permanente somente após rollback confirmado; falha no rollback permanece transitória e panic depois de iniciar COMMIT é resultado incerto. SQLSTATE `40001`/`40P01` permanece transitório mesmo depois de esgotar as tentativas locais. O caminho síncrono nunca grava `FAILED`.

## Reversões e referências pendentes

`REFUND` é evento de negócio (a rodada foi anulada, devolve a aposta) e só vale sobre `BET`. `ROLLBACK` é desfazer técnico e vale sobre `BET`, `WIN` ou `REFUND`. Nos dois, `referenceExternalTransactionId` é obrigatório e resolvido por `(providerId, referenceExternalTransactionId)`.

A operação e a referência precisam concordar em provedor, jogador, carteira, moeda e rodada, e o valor tem que ser igual: **não existe reversão parcial**. O valor aplicado vem sempre da original; o payload só serve para conferir. Se o provedor mandar um rollback de 250,00 por bug numa aposta de 25,00, a operação é rejeitada em vez de creditar 250.

Quando a referência ainda não chegou, a operação é persistida como `PENDING_REFERENCE` e o HTTP responde 202. Um worker em todas as instâncias reivindica as pendências vencidas com `FOR UPDATE SKIP LOCKED`, uma por transação, trava a carteira e tenta de novo, com backoff exponencial a partir de `REFERENCE_BACKOFF_BASE`. Ao passar de `REFERENCE_TTL` sem sucesso, a pendência vira `REJECTED` com `REFERENCE_NOT_FOUND`, ou `REFERENCE_NOT_PROCESSED` se a referência existe mas nunca ficou pronta.

Sem isso, responder "não achei" seria pior: o provedor daria o assunto por encerrado, a aposta chegaria depois e o jogador pagaria por uma rodada cancelada.

Toda operação que chega a estado terminal acorda as pendências que a referenciam, antecipando `next_attempt_at`. O wake-up usa `SKIP LOCKED` porque o fluxo principal segura a carteira e o worker segura a pendência: sem isso os dois se cruzariam.

Cada operação aceita **no máximo uma reversão bem-sucedida**, seja `REFUND` ou `ROLLBACK`, garantido por índice único parcial e rejeitado antes pelo domínio com `ALREADY_REVERSED`. O README do desafio só exige impedir duas do mesmo tipo; esta regra é mais estrita e cobre dois furos: o retry do provedor mandando o mesmo cancelamento com IDs externos diferentes, e a combinação cruzada de refund mais rollback sobre a mesma aposta.

## Inbox e outbox

**Inbox.** O consumidor grava `(consumer_name, message_id)` com o SHA-256 do corpo bruto na mesma transação do tratamento, e só apaga a mensagem da fila depois do commit. Se o processo morrer entre uma coisa e outra, a mensagem volta: a inbox reconhece o `messageId`, confere o hash e apaga sem reprocessar. Reentrega com hash diferente vai para a DLQ.

O hash da inbox é do corpo bruto e serve só para detectar reentrega adulterada. O hash canônico da operação é outro, fica em `wager_transactions` e serve à idempotência. São dois hashes com papéis diferentes.

**Outbox.** O evento nasce na mesma transação do saldo. Publicar antes do commit seria anunciar uma aposta que pode não existir; publicar depois, sem registro, perde o aviso se o processo morrer no meio. Gravando a linha junto, a intenção de publicar fica tão durável quanto o próprio débito.

O publisher roda em todas as instâncias e divide o trabalho com `FOR UPDATE SKIP LOCKED` mais um lease. Ele reivindica numa transação curta, confirma, publica fora de transação e só então marca. Cada claim incrementa `attempts`, e esse número é a geração do recibo: `MarkPublished` e `MarkFailed` só alteram a linha se id, dono, geração e lease ainda baterem. Se o lease venceu e outra instância reivindicou, a finalização atrasada da primeira não muda nada.

Morrer entre publicar e marcar republica o mesmo `eventId`. Duplica, nunca perde; quem consome descarta pelo `eventId`.

## Mensageria

| Item | Valor |
| --- | --- |
| `MessageGroupId` na entrada | `walletId`. Ordem por carteira, paralelismo entre carteiras, no máximo uma mensagem por carteira em processamento |
| `MessageDeduplicationId` na entrada | `hexLower(SHA256(messageId))`. O `messageId` de negócio é opaco e pode passar dos 128 caracteres que o transporte aceita; o hash cabe sempre e é estável |
| `MessageDeduplicationId` na DLQ | `hexLower(SHA256(["wallet-dlq-v1", arn da fila de origem, messageId do broker, reason]))`. Usa a identidade do broker, que existe mesmo quando o corpo está quebrado |
| `MessageGroupId` na DLQ | O atributo de sistema da mensagem original, pedido no `ReceiveMessage` |
| `MessageDeduplicationId` nos eventos | `eventId` |

O consumidor não sabe o que uma mensagem significa. Ele recebe, roteia e confirma. Quem entende o conteúdo é o listener: cada um declara em `Watching()` a fila que observa, e o consumidor monta um mapa de fila para listener na construção. Duas escutas na mesma fila derrubam o start em vez de virar uma disputa silenciosa.

A conversa entre os dois é o retorno do `Handle`. Sem erro, a mensagem é apagada. Erro comum, ela volta para a fila com a visibilidade esticada pelo backoff. `listener.Rejection`, vai direto para a DLQ com o motivo que o listener escolheu. `listener.ErrUnknownOutcome`, fica exatamente onde está, porque ninguém sabe se o trabalho aconteceu. Cada fila roda um recebedor com um semáforo de `SQS_CONSUMER_WORKERS` posições, então esse é o teto de mensagens em processamento por instância.

O consumidor usa `MaxNumberOfMessages = 1`. Com lote de 10, o SQS entrega várias mensagens do mesmo grupo de uma vez, o que quebraria a premissa de uma mensagem por carteira em processamento e exigiria liberar o resto do lote a cada falha. Com 1, ordem e exclusividade saem de graça.

Falha transitória estende a visibilidade com backoff calculado pelo `ApproximateReceiveCount`; `maxReceiveCount = 5` leva à DLQ por redrive. Erro permanente (envelope inválido, `OPENING` pela fila, carteira inexistente, conflito de idempotência, hash divergente) vai direto para a DLQ com `reason`, `originalMessageId` e `receiveCount`, e a original só é apagada depois da confirmação do envio. A deduplicação do FIFO é conveniência, nunca condição de correção.

Em `SIGTERM` o consumidor para de buscar, conclui o que está em andamento dentro de `SHUTDOWN_TIMEOUT` e, se não der, devolve a visibilidade para reentrega imediata.

### Contratos dos eventos

Quatro tipos, envelope comum com `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId`, `occurredAt`, `version` e `data` tipado. O construtor de cada evento fixa tipo e versão. `aggregateId` é a carteira em `WalletBalanceChanged` e a transação nos outros três; `causationId` é sempre o `transactionId` que originou o evento.

| Desfecho | Eventos |
| --- | --- |
| `BET`, `WIN`, `REFUND`, `ROLLBACK` processados | `WagerTransactionProcessed` + `WalletBalanceChanged` |
| `LOSS` processado | só `WagerTransactionProcessed` |
| Rejeição definitiva | `WagerTransactionRejected` |
| Reversão aguardando referência | `WagerTransactionPendingReference`, uma vez |
| Abertura com saldo positivo | `WagerTransactionProcessed` (kind `OPENING`) + `WalletBalanceChanged` |
| Abertura com zero, replay, `FAILED` | nenhum |

Detalhes de cada payload em [documentation/api/messaging-contracts.md](documentation/api/messaging-contracts.md).

## Contrato HTTP

| Situação | Código |
| --- | --- |
| Operação concluída | 201 |
| Reversão aguardando referência | 202 |
| Rejeição de negócio persistida | 422 com `failureCode` |
| Entrada inválida | 400 |
| Conflito de idempotência ou carteira duplicada | 409 |
| Recurso inexistente, ou de outro provedor | 404 |
| Indisponibilidade transitória | 503 com `Retry-After` |

Replay devolve o código do estado atual da operação com `idempotentReplay: true`. Erros de validação e transporte saem em `application/problem+json` com um `code` estável; resultados persistidos saem no corpo de resultado, com `transactionId` e `status`.

São onze `failureCode`, todos definitivos: `INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `BALANCE_LIMIT_EXCEEDED`, `REFERENCE_NOT_FOUND`, `REFERENCE_NOT_PROCESSED`, `REFERENCE_MISMATCH`, `REFERENCE_KIND_NOT_ALLOWED`, `ALREADY_REVERSED`, `CURRENCY_MISMATCH`, `WALLET_PLAYER_MISMATCH` e `PERMANENT_FAILURE`. Moeda e jogador errados são persistidos como rejeição, e não devolvidos como 400, porque são sinal de integração quebrada e merecem trilha. A tabela completa está em [documentation/api/http-contract.md](documentation/api/http-contract.md).

## Autenticação e autorização

Keycloak, recomendado pelo desafio e suficiente aqui: realm importado de arquivo no boot, sem nenhuma chamada administrativa. Quatro clients confidenciais com service account: `provider-a`, `provider-b`, `provider-c-short-lived` (token de 5 s, só para o teste de expiração) e `wallet-internal`.

No start, a API exige um documento JWKS válido com chave RSA de assinatura compatível com RS256. Depois, `go-oidc` mantém o cache e busca chaves novamente ao encontrar uma assinatura desconhecida; o cliente de rede tem timeout de 10 s. A rotação é testada no Keycloak real, com tokens antigos e novos aceitos pelo mesmo processo. A API valida assinatura pelo JWKS, `iss`, `aud` e `exp`, sem tolerância. O `providerId` autorizado vem do claim `provider_id`, fixado por protocol mapper em cada client, e não do `client_id`: isso desacopla a identidade no IdP do identificador de negócio.

O discovery é buscado em `OIDC_DISCOVERY_URL` e o seu `issuer` é conferido contra `OIDC_ISSUER`; as chaves vêm de `OIDC_JWKS_URL`, configurado à parte. Isso importa porque o issuer é público e fixo (`http://localhost:8180/...`) enquanto o serviço, dentro do Compose, alcança o Keycloak por `keycloak:8080`, e nos testes por uma porta efêmera. Assumir que o `jwks_uri` anunciado é alcançável quebraria as duas topologias.

Autorização por papel: carteira, ledger e reconciliação só para `wallet-internal`; envio de operação só para `wagering-provider`, com o `providerId` do corpo igual ao claim; consultas para o provedor dono ou para o papel interno. Transação de outro provedor devolve 404, não 403, para não revelar existência. Health e métricas são públicos.

**Fronteira de confiança da fila.** A fila de entrada é interna. Só um serviço de ingestão confiável publica nela; é ele que autentica o provedor a montante e define o `providerId` a partir dessa identidade autenticada. Provedores não recebem credenciais da fila. A política de acesso é provisionada com `SetQueueAttributes` e está documentada, mas IAM autoriza o produtor, não valida o campo `providerId` do JSON. O consumidor confia nesse contrato de ingestão e mantém todas as validações de domínio.

## Composição e encerramento

Uber Fx só no composition root. Os construtores continuam explícitos (`NewX(dependências...)`), o domínio e os casos de uso não recebem container nem procuram dependências, e os testes unitários chamam os construtores direto.

A ordem de registro é deliberada, porque `OnStop` roda ao contrário: banco, SQS, IdP, scheduler e um hook conjunto de entradas HTTP/SQS. O start valida as dependências (ping no banco, as três filas existem, discovery e JWKS respondem) e falha o processo se alguma não responder. Na parada, HTTP e consumidor interrompem novas entradas juntos e drenam em paralelo dentro de `SHUTDOWN_TIMEOUT`. O consumidor reserva parte desse prazo para cancelar handlers restantes e devolver suas mensagens com visibilidade zero. Chamadas de delete, DLQ e backoff usam um contexto próprio de até 5 s, independente do prazo já encerrado do handler. Depois os jobs são cancelados e aguardados; o pool fecha por último. O deadline global do Fx continua limitando o ciclo inteiro. Cada rodada de job tem prazo máximo de 30 s.

O scheduler em `internal/infrastructure/jobs` roda cada job num ticker próprio. Não é cron porque o trabalho não é um compromisso de relógio: o publisher da outbox drena a cada 500 ms, abaixo da menor granularidade que uma expressão cron alcança, e os dois jobs precisam parar no instante em que o contexto é cancelado. Um ticker da biblioteca padrão diz isso inteiro; uma biblioteca de cron traria expressões que o serviço não usa.

## Limitações e interpretações

- **Uma reversão por operação.** `BET` → `REFUND` → `ROLLBACK` do `REFUND` faz a aposta voltar a valer, mas ela não pode mais ser desfeita: já gastou sua reversão. Um `ROLLBACK` posterior da `BET` é rejeitado com `ALREADY_REVERSED`.
- **Ordem dos eventos não é garantida entre publishers.** Dentro de uma carteira, dois publishers podem entregar fora de ordem. Quem consome ordena por `walletVersion` em `WalletBalanceChanged` e detecta buraco pela sequência.
- **TTL da pendência contra a janela do SQS.** `REFERENCE_TTL` precisa ser maior que a janela máxima de retry da fila, senão a aposta pode chegar depois que a reversão expirou. Com os padrões, a janela do SQS fica abaixo de 3 minutos e o TTL é de 15.
- **`MaxNumberOfMessages = 1`** limita o throughput por goroutine. Foi escolha consciente: a ordem por carteira e a exclusividade saem de graça.
- **A política da fila não é imposta pelo emulador.** O MiniStack aceita e devolve a policy, mas não a aplica. Em produção ela seria o controle real; aqui serve como documentação executável do desenho.
- **`FAILED` não emite evento.** É estado de auditoria, visível por consulta, log e métrica.
- **Moedas.** Só BRL e USD, e sem conversão. Operação numa moeda suportada diferente da carteira é rejeição auditável, não erro de validação.
- **Fora do escopo.** Ledger de partidas dobradas, tracing com OpenTelemetry, dashboards e teste de carga são diferenciais opcionais do desafio e não foram feitos. O serviço de ingestão que publica na fila também não faz parte da entrega: a fronteira está documentada, não implementada.

## Evidências e manutenção dos testes

O relatório da validação final está em [documentation/validation-2026-09-21.md](documentation/validation-2026-09-21.md). Os helpers de `test/testenv` encapsulam processos, bancos, filas, tokens, rotação de chaves e proxies de falha. O supervisor distingue SIGKILL esperado de crash inesperado e sempre reprova diagnóstico de corrida ou saída 66, mesmo com failpoint armado.

O teste com tag `compose` usa o artefato Docker com três réplicas: publica lotes SQS, força replays HTTP/SQS com identidades de transporte distintas, reconcilia 50 carteiras e confere a entrega dos eventos da outbox. Uma segunda fase confere os mesmos dados depois de reiniciar todas as APIs. Isso demonstra os cenários exercitados; não é medição de capacidade ou benchmark de latência.

`reference_pending` conta as pendências duráveis no banco a cada rodada do worker. Como cada réplica observa o mesmo banco, agregue com `max(reference_pending)`, e não somando réplicas. Se o worker estiver desligado, essa métrica não é atualizada. Logs de divergência expõem identificadores e contagem de entradas; os valores monetários ficam na resposta autenticada de reconciliação.
