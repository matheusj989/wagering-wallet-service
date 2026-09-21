# Postman

Coleção `wagering-wallet-service.postman_collection.json` e ambiente `local.postman_environment.json`. Cobrem todos os endpoints de [../api/http-contract.md](../api/http-contract.md) e os fluxos que dá para exercitar por HTTP.

## Como usar

1. Suba o ambiente na raiz do repositório: `docker compose up --build`.
2. Importe a coleção e o ambiente e selecione **wagering-wallet-service (local)**.
3. Rode a pasta **0. Sessão** uma vez. Ela gera `playerId`, `roundId` e os IDs externos da rodada.
4. Rode as pastas na ordem, ou a coleção inteira no Collection Runner.

Sem abrir o Postman, a coleção inteira roda com o Newman:

```
npx newman@6 run documentation/postman/wagering-wallet-service.postman_collection.json \
  -e documentation/postman/local.postman_environment.json
```

Os tokens são obtidos sozinhos: cada pasta pede um token `client_credentials` ao Keycloak para o seu client e o guarda até expirar. A pasta **7. Tokens** só serve para inspecionar os claims.

## O que cada pasta prova

| Pasta | Token | Cenários |
| --- | --- | --- |
| 1. Health e métricas | nenhum | `live`, `ready` com os dois checks e `/metrics`, todos públicos |
| 2. Carteiras | `wallet-internal` | abertura, duplicidade 409 com o `walletId`, valores inválidos, ledger paginado, cursor inválido, reconciliação, provedor barrado |
| 3. Wagering | `provider-a` | saldo 1000 → 975 → 1075 → 1100 ao longo da pasta: aposta, replay, os dois conflitos 409, ganho com referência, perda, rejeição por saldo, reversão, reversão duplicada, pendência 202 e consultas |
| 4. Isolamento | `provider-b` | não lê nem escreve nada de `provider-a`, e a mesma string de chave é outra operação |
| 5. Autenticação | variados | sem token, token inválido e token de 5 s usado depois de expirar |
| 6. Moedas e limites | `provider-a` e interno | moeda fora da lista, moeda diferente da carteira e crédito acima do máximo representável |

## O que o Postman não cobre

Concorrência, morte de processo entre commit e publicação, reentrega de fila e recuperação com várias instâncias não cabem numa coleção HTTP. Esses cenários estão na suíte de integração (`go test -race -tags integration ./test/...`), que sobe processos reais e mata alguns deles de propósito.

A requisição "Consultar a pendência" aceita `PENDING_REFERENCE` ou, se o TTL já venceu, `REJECTED` com `REFERENCE_NOT_FOUND`.
