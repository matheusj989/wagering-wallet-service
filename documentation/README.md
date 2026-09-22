# Documentação

| Pasta / arquivo | O que é |
| --- | --- |
| `CHALLENGE.md` | Cópia do README do desafio (fonte da verdade). Original: https://github.com/junglegaming/backend-challenge-go |
| `api/http-contract.md` | Contrato HTTP fechado: endpoints, códigos, corpos, erros e catálogo de failure codes |
| `api/messaging-contracts.md` | Mensagem de entrada (SQS), política do consumidor, eventos de saída e roteamento |
| `validation-2026-09-21.md` | Validação final: bateria de testes e ensaio do Compose com três réplicas |
| `postman/` | Coleção e ambiente do Postman com os fluxos autenticados e os cenários principais |

As decisões de arquitetura estão em `../ARCHITECTURE.md`.
`README.md` e `ARCHITECTURE.md` da entrega ficam na raiz do repositório, como o desafio pede, e apontam para esta pasta.

A coleção do Postman em `postman/` cobre os fluxos do contrato HTTP e foi executada contra o serviço subido pelo `compose.yaml`. O passo a passo está no `README.md` daquela pasta.
