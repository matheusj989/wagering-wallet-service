# Documentação

| Pasta / arquivo | O que é |
| --- | --- |
| `plan/README-desafio-junglegaming.md` | Cópia do README do desafio (fonte da verdade). Original: https://github.com/junglegaming/backend-challenge-go |
| `plan/PLANO-jungle-gaming-backend-challenge.md` | Plano de handoff, cópia idêntica de `/home/matheus/Downloads/PLANO-jungle-gaming-backend-challenge.md` |
| `plan/validation-2026-09-20.md` | Relatório da validação do plano: o que foi reproduzido em Postgres 16 e MiniStack reais, e o que foi encontrado |
| `plan/scripts/` | Scripts usados na validação (SQL do rascunho da migration, cenários de concorrência, smoke test do MiniStack) |
| `api/http-contract.md` | Contrato HTTP fechado: endpoints, códigos, corpos, erros e catálogo de failure codes |
| `api/messaging-contracts.md` | Mensagem de entrada (SQS), política do consumidor, eventos de saída e roteamento |
| `validation-2026-09-21.md` | Resultados da revisão final, regressões e ensaio do Compose com três réplicas |
| `postman/` | Coleção e ambiente do Postman com os fluxos autenticados e os cenários principais |

As decisões de arquitetura estão em `../ARCHITECTURE.md`.
`README.md` e `ARCHITECTURE.md` da entrega ficam na raiz do repositório, como o desafio pede, e apontam para esta pasta.

O plano, o relatório de validação e os scripts em `plan/` são registro histórico: foram escritos sobre PostgreSQL 16 e o serviço roda em PostgreSQL 18.6. Onde eles divergirem do design e das specs, valem o design e as specs.

A coleção do Postman em `postman/` cobre os fluxos do contrato HTTP e foi executada contra o serviço subido pelo `compose.yaml`. O passo a passo está no `README.md` daquela pasta.
