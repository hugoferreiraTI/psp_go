# PSP Go · H-BANK

**PT:** PSP de estudo e desenvolvimento do ecossistema **H-BANK**, responsável por autorizar pagamentos com cartão em conjunto com o projeto [Card Machine](https://github.com/hugoferreiraTI/go_card_machine). O fluxo completo depende da Card Machine: ela cria a venda, monta o TLV, protege o PIN, gera o criptograma e envia a autorização. A PSP valida o cadastro e a operação, movimenta os valores, registra o histórico e responde na mesma requisição HTTP.

**EN:** An educational PSP under development as a step toward the **H-BANK** ecosystem. It works together with [Card Machine](https://github.com/hugoferreiraTI/go_card_machine), which creates sales, builds TLV data, protects the PIN and generates the cryptogram. The PSP validates and authorizes payments, updates card balances or credit usage, persists transaction history and returns the decision in the original HTTP response.

**Projeto em andamento / Work in progress.** Os cartões, BINs e regras são simulados. Não há conexão com redes de cartões ou bancos reais. A PSP pode ser iniciada e testada isoladamente, mas **o fluxo de pagamento deste projeto não funciona sem a Card Machine ou um emissor compatível com o mesmo protocolo**. Esta integração é uma etapa do ecossistema H-BANK.

[Português](#português) · [English](#english) · [Referência de funções / Function reference](#function-reference)

## Português

### Responsabilidades e arquitetura

```text
Postman / operador
  → Card Machine: cria a venda e recebe os dados do cartão
  → Card Machine: TLV + AES-CMAC + PIN protegido por AES-GCM
  → POST PSP /authorize/card
  → evento Kafka PENDING
  → histórico PENDING no PostgreSQL
  → validações de estrutura, criptografia, cadastro e valor
  → APPROVED ou REPROVED no histórico
  → evento Kafka com a decisão
  → resposta JSON na mesma conexão HTTP
  → Card Machine recebe o resultado
```

A resposta não exige um segundo client da PSP para a Card Machine. Kafka observa as etapas; ele não transporta o comando de autorização nem substitui a resposta HTTP. Os eventos só são criados quando o JSON foi desserializado e entrou em `SaleCard`; JSON malformado é rejeitado pelo handler antes disso.

| Pasta | Responsabilidade |
|---|---|
| `cmd/main.go` | Carrega configuração, conecta dependências, registra rotas e gerencia encerramento. |
| `internal/handler` | Entrada e saída HTTP/JSON. |
| `internal/usecases` | Cadastro, autenticação, autorização e regras de débito/crédito. |
| `internal/parser` | Interpretação do TLV/BCD e conversão monetária. |
| `internal/securitysale` | Validações estruturais da transação decodificada. |
| `internal/repository` | Consultas e atualizações PostgreSQL. |
| `internal/model` | Contratos de entrada, dados decodificados, cartões, resposta e eventos. |
| `internal/kafka` | Publicação e observação de eventos da PSP. |
| `internal/db/migrations` | Evolução manual do schema SQL. |
| `test` | Três arquivos: HTTP, SQL e usecases. |

### Preparação e configuração

Pré-requisitos: Go compatível com `go.mod` (atualmente `1.26.8`), compilador C/CGO habilitado para o client Kafka, PostgreSQL, Kafka e o projeto Card Machine configurado com as mesmas chaves deste usuário do MVP.

1. Instale as dependências: `go mod download`.
2. Inicie o PostgreSQL: `docker compose up -d go_db`.
3. Disponibilize um broker Kafka em `localhost:9092`. O compose deste repositório contém apenas PostgreSQL. Se você já criou o container local chamado `kafka`, use `docker start kafka`.
4. Crie o tópico `psp_authorizations` no broker. Para o container local:

```bash
docker exec kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --create --if-not-exists \
  --topic psp_authorizations --partitions 1 --replication-factor 1
```

5. Prepare o schema conforme a seção abaixo.
6. Configure o `.env` na raiz da PSP, sem versionar chaves reais:

```dotenv
AES_CMAC_KEY=<chave hexadecimal de 128 bits compartilhada com esta Card Machine>
AES_GCM=<chave AES hexadecimal compartilhada com esta Card Machine>
KAFKA_BOOTSTRAP_SERVERS=localhost:9092
PORT=8080
```

`AES_CMAC_KEY` e `AES_GCM` são obrigatórias para iniciar o programa. O usecase recebe as chaves por `PaymentKeys`; elas não são aceitas no JSON. Neste MVP a instância atende o contexto configurado de um usuário; não existe chave universal de todos os usuários nem resolução multitenant automática. `KAFKA_BOOTSTRAP_SERVERS` e `PORT` têm os padrões mostrados acima.

A conexão atual do banco está em `internal/db/db.go`: host `localhost`, porta `5432`, banco/usuário `postgres`, senha local `1234`. Estes são valores do ambiente de desenvolvimento.

**Banco vazio:** execute os scripts de `000` a `008`, na ordem, com o mesmo banco de destino:

```bash
export PGPASSWORD=1234
for migration in internal/db/migrations/*.sql; do
  psql -h localhost -U postgres -d postgres -v ON_ERROR_STOP=1 -f "$migration" || break
done
```

**Banco existente:** aplique somente as migrações ainda não aplicadas. Não execute novamente o bootstrap `000` nem toda a sequência em um banco já preparado. Não há runner ou controle automático de versões. O script `000` cria o schema histórico mínimo; os demais chegam ao schema atual com `cards_debit`, `cards_credit` e `card_transactions`. O programa não executa migrações ao iniciar.

Inicie a PSP com `go run ./cmd`, depois inicie a Card Machine conforme as instruções daquele projeto. A PSP usa `8080` por padrão; nos exemplos a Card Machine usa `8081`. Após alterações, confirme que o processo da PSP na porta correta foi recompilado: um processo antigo pode continuar usando consultas de um schema anterior.

### Ordem correta de uso

1. Prepare PostgreSQL e Kafka, aplique as migrações e inicie a PSP.
2. **Cadastre o cartão antes de tentar autorizar uma venda.** Um PAN com BIN válido não significa um cartão cadastrado.
3. Para débito, adicione saldo antes da compra. Para crédito, provisione o cartão com limite e valor utilizado coerentes.
4. Inicialize a Card Machine pelo cadastro dela, se ainda necessário.
5. Crie uma venda na Card Machine, informando `CARD` e `DEBIT` ou `CREDIT`.
6. Envie os dados do cartão à Card Machine dentro do prazo da venda.
7. Confira a resposta, o histórico e os dois eventos Kafka da PSP.

Os endpoints de Pix pertencem à Card Machine; **esta PSP autoriza somente cartões de débito e crédito**.

### Endpoints da PSP

Todos recebem `Content-Type: application/json`.

| Método e rota | Entrada | Resultado |
|---|---|---|
| `POST /card/create` | `Card`: PAN, PIN e validade. | Cria apenas débito, retorna HTTP 201 e `card_id`. |
| `POST /card/add-value` | `CardAddAmount`: PAN, PIN, validade, tipo e centavos inteiros. | Adiciona saldo de débito e retorna o novo saldo em centavos. |
| `POST /authorize/card` | `CardPayment` preparado pela Card Machine, incluindo EMV/TLV e criptografia. | HTTP 200 com decisão de negócio e os sete campos de `AuthorizationResponse`. |

**Cadastro de débito — PSP `/card/create`:** exemplo sintético. Se o PAN já existir, escolha outro PAN válido dos ranges do MVP; não repita o cadastro para adicionar saldo.

```json
{
  "PAN": "5201000000000006",
  "PIN": "2468",
  "DateExpiry": "311231"
}
```

O usecase determina bandeira e emissor, valida Luhn e validade, exige PIN de 4 a 6 dígitos ASCII e gera bcrypt. O repository verifica o PAN completo em **ambas** as tabelas antes de inserir. PAN já cadastrado resulta em HTTP 409. O cartão nasce com tipo `DEBIT`, saldo `0` e status `ACTIVE`; ID, saldo, status, bandeira e emissor enviados pelo cliente não substituem esses padrões. O PIN em texto existe apenas na entrada e em memória, nunca na coluna do banco.

A consulta explícita de existência complementa `UNIQUE(pan)` em cada tabela. Ela não é uma constraint de unicidade global entre as tabelas sob inserções concorrentes; SQL manual também não passa por esse pré-teste da aplicação.

**Crédito:** o cadastro HTTP ainda não cria crédito. Use `UserRepository.InsertCreditCard` com `model.Card` contendo `PINHash` bcrypt gerado previamente, um limite em centavos e o valor já utilizado. A função também consulta a existência do PAN nas duas tabelas; não passe PIN em texto no campo de hash. O banco rejeita `amount > limit`. Não há endpoint público para provisionar o limite neste MVP.

**Adicionar saldo — PSP `/card/add-value`:**

```json
{
  "PAN": "5201000000000006",
  "PIN": "2468",
  "DateExpiry": "311231",
  "TypeCard": "DEBIT",
  "Amount": 1000000
}
```

`1000000` centavos = R$ 10.000,00. Aqui `Amount` é um **inteiro em centavos**, diferente do `Amount` decimal da venda. PAN, tipo, validade e PIN bcrypt precisam corresponder ao cadastro. O valor é somado ao saldo existente, não substitui o saldo.

### Exemplo de compra pela Card Machine

Estes requests vão para a **Card Machine**, não para `/authorize/card`. Crie primeiro a venda em `POST http://localhost:8081/Sale`:

```json
{
  "Amount": "1.33",
  "Type": "CARD",
  "payment_type": "DEBIT"
}
```

Depois envie `POST http://localhost:8081/Pay/card`:

```json
{
  "PAN": "5201000000000006",
  "dateExpirity": "31/12/31",
  "Country": "076",
  "Pin": {
    "PIN": "2468",
    "CVM": "ONLINE_PIN"
  }
}
```

Use a janela de pagamento definida pela Card Machine (atualmente 30 segundos). Para crédito, crie outra venda com `payment_type: "CREDIT"` e use um PAN cadastrado em `cards_credit`, por exemplo `5201500000000005` **se você o provisionou**. Os exemplos não criam cartões automaticamente. A PSP não mantém seeds permanentes desses PANs.

O nome `dateExpirity` pertence ao contrato de entrada da Card Machine. No cadastro da PSP o campo é `DateExpiry` em `YYMMDD`. A data `31/12/31` representa 31/12/2031 e vira `311231`.

### TLV, BIN e criptografia

A Card Machine envia um `CardPayment` com `SaleId`, `TimeNow`, `Amount`, `Status`, `PaymentType` e `Emv`. `Amount` é texto decimal com duas casas, como `"25.33"`; `Status` deve chegar `PENDING`. `TimeNow` é preservado na resposta.

Cada TLV tem `Tag []byte`, `Length int` e `Value []byte`. Embora o conteúdo seja representado em hexadecimal ao construir/interpretar as tags, **`encoding/json` transporta `[]byte` em Base64**. `Length` conta bytes decodificados, não caracteres da string Base64. Não envie a string de PAN da Card Machine diretamente como o campo TLV da PSP.

| Dado | Tag | Conteúdo e tamanho |
|---|---|---|
| PAN | `5A` | BCD, 6–10 bytes; um nibble `F` final é permitido para PAN ímpar. Depois são validados formato, tamanho e Luhn. |
| Validade | `5F24` | 3 bytes BCD, `YYMMDD`. |
| País | `9F1A` | 2 bytes BCD; Brasil é `0076`. |
| Número imprevisível | `9F37` | 4 bytes binários preservados sem conversão para texto. |
| Criptograma | `9F26` | 16 bytes de AES-CMAC completo no protocolo deste MVP. |
| PIN | `Emv.Pin.PIN` | Texto hexadecimal de `nonce || ciphertext || tag` AES-GCM; não é PAN/BCD nem bcrypt. |

Exemplo isolado da tag PAN `5201000000000006` no JSON da PSP:

```json
{
  "Tag": "Wg==",
  "Length": 8,
  "Value": "UgEAAAAAAAY="
}
```

Esse fragmento não é uma autorização completa: faltam as demais tags, o criptograma e o PIN protegido.

`VerifyPANRouting` usa **exatamente os seis primeiros dígitos** do PAN, sem BIN de 8 dígitos ou precedência variável. Os intervalos são inclusivos e simulados:

| BIN inicial | BIN final | Bandeira | Emissor |
|---|---|---|---|
| `520000` | `520099` | MASTERCARD | NUBANK |
| `520100` | `520199` | MASTERCARD | ITAU |
| `400000` | `400099` | VISA | NUBANK |
| `400100` | `400199` | VISA | BRADESCO |

A verificação de formato aceita PAN de 12–19 dígitos; Visa exige 13, 16 ou 19, e Mastercard exige 16. Um PAN com Luhn correto, porém fora dos ranges de emissor do MVP, continua sendo rejeitado no routing.

AES-CMAC usa a biblioteca [AES_CMAC_Go](https://github.com/hugoferreiraTI/AES_CMAC_GO). A PSP reconstrói a mesma mensagem da Card Machine:

```go
fmt.Sprintf("%s|%s|%s|%s", status, amountOriginal, timeNow, unpredictableNumber)
```

`unpredictableNumber` deve ser o `[4]byte` original; não substitua por uma string hexadecimal ou pelo Base64 do JSON. Alterar status, valor, horário ou número exige recalcular o CMAC. O protocolo inclui o valor decimal original, não os centavos convertidos.

AES-GCM usa `crypto/aes` e `crypto/cipher`, com AAD `nil`. `Open` autentica o payload antes de devolver o PIN em memória. `VerifyPIN` usa `bcrypt.CompareHashAndPassword` contra `pin_hash`; não gera outro hash para comparar strings. PAN, PIN, hash, chaves e payload EMV não fazem parte dos eventos Kafka nem da resposta de autorização.

### Autorização, valores e banco

Fluxo executado para débito e crédito:

1. Publica `PENDING` e tenta criar o histórico `PENDING`.
2. `DecodeTLV` interpreta tags, comprimentos, BCD, número imprevisível e valor monetário.
3. Determina bandeira/emissor; grava no histórico os dados já decodificados, ainda sem cartão identificado.
4. Valida estrutura da transação e criptograma; conclui a decisão de routing.
5. Busca o PAN na tabela correspondente ao tipo e vincula `card_id`.
6. Compara tipo, PAN, bandeira, emissor e validade com o cadastro; verifica expiração.
7. Autentica AES-GCM, recupera o PIN e compara com bcrypt.
8. Débito: exige saldo suficiente. Crédito: exige `amount utilizado + venda <= limit`.
9. O repository repete a condição financeira no `UPDATE` e finaliza a aprovação na mesma transação SQL da movimentação. Falha desfaz a movimentação.
10. Reprovações tentam finalizar o histórico como `REPROVED`. Depois publica o resultado e devolve a resposta HTTP.

Dinheiro é armazenado como `BIGINT` em centavos, usando `int64` na aplicação. `"25.33"` vira `2533` e a resposta volta como `"25.33"`, sem `float`. Uma ferramenta SQL pode exibir `2,533` como separador de milhares: o valor armazenado continua sendo o inteiro 2533 centavos. Crédito soma o gasto ao `amount` utilizado; débito subtrai do `balance`.

| Tabela | Finalidade |
|---|---|
| `cards_debit` | Cadastro, `pin_hash`, validade e saldo `balance`. |
| `cards_credit` | Cadastro, `pin_hash`, validade, `"limit"` total e `amount` utilizado. |
| `card_transactions` | `sale_id`, PAN, `card_id`, valor em centavos, tipo, status e horário de persistência. |

`card_id` é único como coluna de referência para ambos os tipos. `payment_type` determina se corresponde a `cards_debit.id` ou `cards_credit.id`; os mesmos números de ID podem existir nas duas tabelas. Uma FK nativa não referencia duas tabelas: triggers validam inserção/alteração do vínculo e impedem exclusão ou alteração de ID de cartões com histórico. Tentativas sem cartão identificado mantêm `card_id = NULL`.

`time_transaction` é gerado no PostgreSQL. `time_now` na resposta vem da Card Machine: são informações diferentes. O PAN completo permanece no cadastro/histórico deste MVP; ele é omitido de respostas e eventos.

### Respostas e tratamento de erros

Uma autorização processada retorna HTTP 200 tanto para aprovação quanto para reprovação. O cliente deve ler `status` e `log`, além do código HTTP:

```json
{
  "status": "REPROVED",
  "payment_type": "DEBIT",
  "card_network": "MASTERCARD",
  "issuer_bank": "ITAU",
  "amount": "1.33",
  "time_now": "2030-10-31T12:00:00-03:00",
  "log": "Saldo insuficiente."
}
```

Na aprovação, `status` é `APPROVED` e `log` é vazio. Campos ainda não determinados podem ficar vazios em uma reprovação antecipada. `authorizationLog` retorna mensagens fixas; não devolve erros crus do SQL/criptografia nem PAN/PIN.

| Situação | Tratamento |
|---|---|
| JSON inválido no handler | HTTP 400 com `error` e `message`; não entra no usecase. |
| Cadastro com PAN já presente em débito ou crédito | HTTP 409, `ErrCardAlreadyExists`. |
| Dados inválidos de cadastro/adição de saldo | HTTP 400; não insere/não soma. |
| PAN válido mas não cadastrado para o tipo | `REPROVED`: cartão inexistente para este tipo de pagamento. |
| PAN, bandeira, emissor ou tipo divergentes | `REPROVED`, motivo de divergência. |
| TLV, Luhn, BIN, país ou status inicial inválidos | `REPROVED`, motivo seguro correspondente. |
| Validade inválida, divergente ou expirada | `REPROVED`, motivo de validade. |
| CMAC divergente | `REPROVED`: criptograma inválido. |
| AES-GCM não autenticado | `REPROVED`: não foi possível autenticar o PIN recebido. |
| PIN divergente / hash inválido | `REPROVED`: PIN incorreto / cadastro de autenticação inválido. |
| Saldo ou limite insuficiente | `REPROVED`, sem movimentação financeira. |
| Transação já finalizada ou dados divergentes no UPDATE | Não sobrescreve aprovação/reprovação já concluída. |
| Falha interna não mapeada | `REPROVED`: não foi possível processar o pagamento. |
| Falha ao finalizar histórico de reprovação | Acrescenta aviso de histórico não salvo; não promete persistência. |

A criação e a adição de saldo usam o contrato próprio `error`/`message`, não `AuthorizationResponse`. Não existe autenticação de operador, prevenção completa de replay, expiração do horário da requisição ou idempotência por `sale_id` neste MVP: cada chamada cria uma tentativa de histórico. O CMAC não assina todos os campos do payload; ele segue exatamente a mensagem de quatro componentes acima. O fluxo representa o protocolo simulado entre estes projetos, não uma implementação certificada de EMV.

### Eventos Kafka

Tópico da PSP: **`psp_authorizations`**. Grupo do observador: **`psp-authorizations`**, separado do grupo da Card Machine. A chave Kafka é o `sale_id` decimal em texto; os dois eventos da mesma venda usam a mesma chave.

Chegada:

```json
{"sale_id": 42, "amount": "1.33", "payment_type": "DEBIT", "status": "PENDING"}
```

Conclusão, antes da resposta:

```json
{"sale_id": 42, "amount": "1.33", "payment_type": "DEBIT", "status": "APPROVED"}
```

Uma reprovação usa o mesmo formato com `status: "REPROVED"`; crédito usa `payment_type: "CREDIT"`. O primeiro evento preserva o valor recebido; o final usa o valor decimal formatado quando disponível, ou o original caso a decodificação falhe. Não inclua dados sensíveis nesses campos.

`ProducerGo` devolve erros ao enfileirar. As confirmações e falhas de entrega do broker são tratadas por um leitor único de eventos do produtor. O produtor usa `acks=all` e idempotência; no encerramento a aplicação aguarda até 5 segundos para entregas pendentes. O consumidor observa e imprime somente os quatro metadados, sem disparar novas vendas. Ele começa em `earliest` se seu grupo ainda não tiver offsets.

**Limite atual:** publicação é assíncrona e não é atômica com PostgreSQL. Uma falha Kafka é registrada sem mudar a decisão financeira. Sem broker disponível, confirmação de entrega ou em um encerramento abrupto, pode faltar evento; não há outbox nem garantia de exatamente dois eventos persistidos em toda condição de falha. Em funcionamento normal, cada entrada em `SaleCard` enfileira `PENDING` e uma decisão final, inclusive saídas antecipadas.

### Diagnóstico rápido

| Sintoma | O que conferir |
|---|---|
| `address already in use` | Já existe um processo na porta. Confirme o PID com `ss -ltnp 'sport = :8080'` antes de iniciar outra PSP. |
| Erro genérico e histórico sem dados decodificados | Confirme se o binário corresponde ao schema e às consultas atuais; depois confira conexão e migrações. |
| `Criptograma inválido.` | Chave CMAC compartilhada, valor original, status `PENDING`, horário e os quatro bytes imprevisíveis. Não edite um payload assinado sem recalcular. |
| `Não foi possível autenticar o PIN recebido.` | Chave GCM e payload hexadecimal completos, incluindo nonce e tag. |
| Cartão inexistente para o tipo | PAN cadastrado na tabela correta e `PaymentType` coerente com o cartão. |
| Sem evento Kafka | Broker e `KAFKA_BOOTSTRAP_SERVERS`, tópico `psp_authorizations` e erros de entrega; confirme que a PSP foi reiniciada com Kafka injetado. |

### Testes e consultas úteis

```bash
# Testes isolados: SQL mockado e broker Kafka de teste em memória.
go test ./... -skip '^TestSaleCard$' -count=1 -timeout=45s

# Eventos de entrada/saída, débito/crédito e falhas antecipadas.
go test ./test -v -run '^TestKafkaAuthorizationEvents$' -count=1

# PAN existente, novo e erro SQL; duplicata bloqueada nos dois cadastros.
go test ./test -v -run '^TestCardPANExists$' -count=1

go vet ./...
```

`test/http_test.go` cobre contratos HTTP; `test/sql_test.go` cobre consultas, bcrypt no INSERT e atomicidade; `test/usecase_test.go` cobre interpretação, criptografia, regras e eventos. `TestSaleCard` é um teste manual com JSON editável e PostgreSQL real: lê as chaves do `.env`, grava histórico e pode movimentar valores quando o payload é aprovado. O JSON fixado nele pode ter criptograma inválido após edição de campos; gere novamente pelo protocolo. Não o confunda com os testes isolados.

```sql
SELECT id, pan, card_network, issuer_bank, date_expiry, status,
       balance AS centavos, (balance::numeric / 100)::numeric(20,2) AS reais
FROM cards_debit;

SELECT id, pan, card_network, issuer_bank, date_expiry, status,
       "limit" AS limite_centavos, amount AS utilizado_centavos,
       (("limit" - amount)::numeric / 100)::numeric(20,2) AS disponivel_reais
FROM cards_credit;

SELECT id, sale_id, card_id, payment_type, status, amount AS centavos,
       (amount::numeric / 100)::numeric(20,2) AS reais, time_transaction
FROM card_transactions ORDER BY id DESC;
```

Essas consultas evitam expor `pin_hash`. Para observar eventos no container Kafka local, se ele fornece os scripts padrão:

```bash
docker exec -it kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 --topic psp_authorizations --from-beginning
```

## English

### Purpose and architecture

PSP Go is the authorization side of a simulated card-payment flow and a step toward **H-BANK**. It works with Card Machine, which collects card data, creates the sale, builds TLV fields, encrypts the PIN with AES-GCM and generates AES-CMAC. **The complete payment flow requires Card Machine or a protocol-compatible terminal.** Running the PSP process or isolated tests does not generate that terminal payload automatically.

Card Machine sends `POST /authorize/card`. The PSP queues a `PENDING` Kafka event, creates pending history, validates the transaction, authenticates the cardholder and applies debit or credit rules. It then finalizes history, queues the final event and returns JSON on the original HTTP connection. No reverse HTTP client or second callback is required. Kafka observes the authorization lifecycle; consuming an event does not authorize another payment. Malformed JSON fails before `SaleCard` and produces no authorization events.

Folders separate HTTP handlers, usecases, models, the EMV parser, structural validation, PostgreSQL access, Kafka and migrations. See the shared [function reference](#function-reference) for every function and its role. This is a work in progress with simulated BINs and cards, not a real bank/card-network connection or a certified EMV implementation.

### Setup and execution order

Use Go compatible with `go.mod` (`1.26.8`), CGO and a C compiler for the Kafka client, PostgreSQL and Kafka. Install modules with `go mod download`. Start PostgreSQL with `docker compose up -d go_db`; the compose only provisions PostgreSQL. Make a Kafka broker available on `localhost:9092`, or use `docker start kafka` if you already have that local container. Create topic `psp_authorizations` before starting the PSP, using the `kafka-topics.sh --create --if-not-exists` command in the Portuguese setup section; the application does not provision topics. The shown one-partition, replication-factor-one configuration is for this local development broker.

Create a root `.env` containing `AES_CMAC_KEY` (hex-encoded 128-bit CMAC key) and `AES_GCM` (hex-encoded AES key), matching the keys configured for this terminal/user. Do not commit actual keys. Optional `KAFKA_BOOTSTRAP_SERVERS` defaults to `localhost:9092`; `PORT` defaults to `8080`. Keys are injected through `PaymentKeys`, never accepted from payment JSON. One configured user context per instance is supported; automatic multitenant key selection is not implemented.

The development database connection is configured in `internal/db/db.go`: localhost, port 5432, database/user `postgres`, local password `1234`.

For a **new, empty database**, run all migration files from `000` through `008` in order using the loop in the Portuguese setup section. `000` creates the historical base tables; later scripts produce the current schema. For an existing database, run only unapplied migrations. There is no migration runner/version table, and application startup does not migrate automatically. Never replay the full sequence against an already migrated database.

Start the PSP with `go run ./cmd`. Then start/configure Card Machine according to its repository, typically on port 8081. Confirm the process listening on port 8080 uses the current binary after changes.

Required usage order:

1. Prepare PostgreSQL/Kafka, migrate and start the PSP.
2. **Create the card before attempting authorization.** A valid BIN and Luhn checksum do not establish registration.
3. Fund debit cards; provision credit cards with an appropriate total limit and current usage.
4. Initialize the terminal if required.
5. Create the sale in Card Machine with `Type: "CARD"` and `payment_type: "DEBIT"` or `"CREDIT"`.
6. Submit card data within the terminal's payment window, currently 30 seconds.
7. Inspect the HTTP response, transaction history and Kafka lifecycle events.

Pix endpoints belong to Card Machine. This PSP currently authorizes debit and credit cards only.

### HTTP contracts and examples

All endpoints accept `Content-Type: application/json`:

| PSP endpoint | Contract | Result |
|---|---|---|
| `POST /card/create` | `Card`, requiring PAN, PIN and expiry. | Creates debit only; HTTP 201 with `card_id`. Duplicate PAN returns 409. |
| `POST /card/add-value` | `CardAddAmount`: PAN, PIN, expiry, `TypeCard`, integer `Amount`. | Adds cents to the current debit balance and returns the new balance. |
| `POST /authorize/card` | Full `CardPayment` generated by the terminal. | HTTP 200 with an approved/reproved business decision. |

Create-card example:

```json
{"PAN":"5201000000000006","PIN":"2468","DateExpiry":"311231"}
```

The usecase validates routing/Luhn, expiry and a 4–6 ASCII-digit PIN, then generates bcrypt. The repository checks the full PAN in **both** debit and credit tables before insertion. Network/issuer are derived, and debit type, zero balance and `ACTIVE` status are enforced instead of trusting caller-supplied values. Existing-PAN checks complement each table's unique constraint; they are not a global cross-table uniqueness constraint under concurrent inserts, and direct SQL bypasses that application precheck.

Credit creation is currently a repository operation: call `UserRepository.InsertCreditCard` with a `Card` containing a previously generated bcrypt `PINHash`, a total limit and used amount in cents. It also checks both card tables for duplicates. There is no HTTP credit-provisioning or limit-management endpoint yet. PostgreSQL rejects credit usage above the total limit.

Add-funds example:

```json
{"PAN":"5201000000000006","PIN":"2468","DateExpiry":"311231","TypeCard":"DEBIT","Amount":1000000}
```

This adds R$ 10,000.00. PAN, debit type, expiry and bcrypt PIN must match. The value is added, not assigned. Here `Amount` is an integer number of cents, while sale `Amount` is a decimal string such as `"1.33"`.

The human-readable purchase request goes to **Card Machine**, not directly to the PSP. First send `/Sale`:

```json
{"Amount":"1.33","Type":"CARD","payment_type":"DEBIT"}
```

Then send `/Pay/card`:

```json
{
  "PAN":"5201000000000006",
  "dateExpirity":"31/12/31",
  "Country":"076",
  "Pin":{"PIN":"2468","CVM":"ONLINE_PIN"}
}
```

For credit, use a new sale with `payment_type: "CREDIT"` and a provisioned credit PAN, for example `5201500000000005` if you created it. Example PANs are not automatically seeded. Card Machine's `dateExpirity` uses `DD/MM/YY`; the PSP's registration field `DateExpiry` and EMV content use `YYMMDD`. Both dates above represent 31 December 2031.

### TLV, routing and cryptography

`CardPayment` contains `SaleId`, `TimeNow`, `Amount`, `Status`, `PaymentType` and `Emv`. Incoming status must be `PENDING`; the terminal's `TimeNow` is returned unchanged.

Each TLV contains binary `Tag`, decoded-byte `Length` and binary `Value`. Go marshals `[]byte` as **Base64 JSON strings**, not hexadecimal strings. The terminal builds the binary value from hexadecimal/BCD; the JSON transport then encodes those bytes. A PAN TLV example is `{"Tag":"Wg==","Length":8,"Value":"UgEAAAAAAAY="}`. That fragment alone is not a payment payload.

| EMV field | Tag | Validation |
|---|---|---|
| PAN | `5A` | 6–10 BCD bytes; one trailing `F` nibble allowed for odd digit count. |
| Expiry | `5F24` | Exactly 3 BCD bytes, `YYMMDD`. |
| Country | `9F1A` | Exactly 2 BCD bytes; supported country is `0076`. |
| Unpredictable number | `9F37` | Exactly 4 binary bytes, preserved as `[4]byte`. |
| Cryptogram | `9F26` | Full 16-byte AES-CMAC for this custom MVP protocol. |
| PIN payload | `Emv.Pin.PIN` | Hex string containing AES-GCM `nonce || ciphertext || tag`, with nil AAD. |

Routing validates ASCII digits and Luhn, then takes **exactly the first six PAN digits**. There is no 8-digit BIN support. Simulated inclusive ranges are `520000–520099 → MASTERCARD/NUBANK`, `520100–520199 → MASTERCARD/ITAU`, `400000–400099 → VISA/NUBANK`, and `400100–400199 → VISA/BRADESCO`. Visa requires 13, 16 or 19 digits; Mastercard requires 16. A valid checksum without an accepted issuer range still fails routing.

CMAC uses [AES_CMAC_Go](https://github.com/hugoferreiraTI/AES_CMAC_GO) and reconstructs `fmt.Sprintf("%s|%s|%s|%s", status, amountOriginal, timeNow, unpredictableNumber)`. The last argument is the original four-byte array, not its hex or Base64 text. Changing any signed component requires regenerating CMAC. The signed amount is the original decimal string, not converted cents. This custom four-component message does not sign every payment field.

AES-GCM authentication must succeed before the plaintext PIN is returned in memory. `bcrypt.CompareHashAndPassword` verifies that PIN against the stored salted hash; generating a fresh hash and comparing strings would be incorrect. Events and authorization responses exclude PAN, PIN, PIN hashes, keys and raw EMV payloads.

### Authorization and persistence

Both payment paths decode TLV/amount, derive routing metadata, save decoded history details, validate transaction structure and CMAC, fetch the card from the correct table, link its ID, compare identity/type/expiry, decrypt AES-GCM and verify bcrypt. Missing cards reject the transaction. Debit requires enough `balance`; credit requires `used amount + purchase <= limit`.

PostgreSQL updates repeat the financial conditions atomically. Debit subtracts cents from `balance`; credit adds cents to `amount` used. Approval and financial movement share a SQL transaction, and a failed approval rolls back the movement. Rejected attempts try to finalize history as `REPROVED`. The final Kafka event follows history finalization, before the HTTP result leaves the usecase.

Money uses signed `int64`/`BIGINT` cents with no float arithmetic: `"25.33" → 2533 → "25.33"`. A SQL UI rendering `2,533` as a thousands-separated integer does not mean the stored value is R$ 2.533. The sample SQL queries in the Portuguese section explicitly show both cents and decimal values without exposing `pin_hash`.

`cards_debit` stores debit registration and balance. `cards_credit` adds total `"limit"` and used `amount`. `card_transactions` stores the attempt, including sale ID, PAN, the shared `card_id`, amount, payment type, status and database timestamp. `payment_type` resolves `card_id` to the appropriate card table; numeric IDs may overlap. Native PostgreSQL FKs cannot reference two tables, so triggers validate links and prevent referenced-card deletion or ID changes. Unidentified cards remain NULL. Database `time_transaction` differs from terminal `time_now`. Full PANs remain in the database in this MVP, but not in authorization responses or Kafka events.

### Responses and failures

Read business `status` and `log`, not only HTTP status. Authorization returns HTTP 200 for both `APPROVED` and `REPROVED`:

```json
{
  "status":"REPROVED",
  "payment_type":"DEBIT",
  "card_network":"MASTERCARD",
  "issuer_bank":"ITAU",
  "amount":"1.33",
  "time_now":"2030-10-31T12:00:00-03:00",
  "log":"Saldo insuficiente."
}
```

Approved responses have an empty `log`. Early rejection can leave undetermined metadata empty. Rejection messages currently remain in Portuguese in both integrations; the README translation does not change the API.

Malformed JSON returns HTTP 400 before authorization. Duplicate card creation returns 409. Invalid card creation/add-funds data returns 400 with `error` and `message`. Authorization distinguishes invalid TLV, PAN/Luhn, unsupported network/issuer/country/type/status, missing card, mismatched identity/expiry, expired card, invalid CMAC/GCM, wrong PIN, invalid PIN hash and insufficient funds/limit. Unmapped failures return the safe generic message `Não foi possível processar o pagamento.` Raw database/crypto errors and sensitive values are not included in authorization responses. If rejected history cannot be finalized, the response adds a history-failure notice rather than promising persistence.

Completed transaction rows are not overwritten by the pending-only updates. However, there is currently no request-level idempotency by sale ID, complete replay prevention, request-age enforcement or operator authentication; each call creates another history attempt. Those are limits of this MVP, not guarantees supplied by CMAC or Kafka.

### Kafka lifecycle and testing

Topic: **`psp_authorizations`**. Observer group: **`psp-authorizations`**, independent from Card Machine's consumer group. Each message uses the decimal sale ID as its Kafka key and contains exactly these four fields:

```json
{"sale_id":42,"amount":"1.33","payment_type":"DEBIT","status":"PENDING"}
```

The final event changes status to `APPROVED` or `REPROVED`; credit uses `CREDIT`. The received event preserves the original amount, while the final event uses the formatted amount when decoding succeeded. The producer has one delivery-report reader, `acks=all`, idempotence and a five-second message timeout. Shutdown attempts a five-second flush. The consumer logs these metadata fields and does not execute payments; a new group starts at `earliest`.

Publishing is asynchronous and not atomic with PostgreSQL. Enqueue/delivery errors are logged without reversing the financial decision. There is no outbox or guarantee that two events survive every crash/broker outage. In normal operation each request entering `SaleCard` queues pending and final events, including early rejection and history-creation failure.

Run isolated checks with `go test ./... -skip '^TestSaleCard$' -count=1 -timeout=45s` and `go vet ./...`. `TestKafkaAuthorizationEvents` uses a local in-memory Kafka mock broker and SQL mocks to verify actual publication, ordering, approved/reproved debit and credit, early failures and sensitive-field exclusion. `TestCardPANExists` covers new/existing PANs, SQL failures and blocked duplicate inserts for both card types.

The three test files are `http_test.go`, `sql_test.go` and `usecase_test.go`. The separate `TestSaleCard` is manual: it uses an editable JSON fixture, real PostgreSQL and root `.env` keys, writes history and can move funds for an approved payload. Editing signed fields without regenerating CMAC will reject the fixture. Keep that test separate from routine isolated verification. A console-consumer command for the local Kafka container appears in the Portuguese section.

<a id="function-reference"></a>
## Referência de funções / Function reference

Os nomes abaixo correspondem ao código atual; mantêm-se `CreatCard` e `SaleId` para preservar os contratos existentes. Funções de teste e helpers de teste ficam nos três arquivos de `test`.

Names below match the current code, including existing spellings `CreatCard` and `SaleId`. The descriptions list the entry points and internal helpers by responsibility.

| Arquivo / File | Funções / Functions | Responsabilidade PT / EN |
|---|---|---|
| `cmd/main.go` | `main` | Configura dependências, rotas, sinais e encerramento HTTP/Kafka / wires dependencies, routes, signals and shutdown. |
| `db/db.go` | `ConnectDB` | Abre e testa PostgreSQL / opens and pings PostgreSQL. |
| `handler/card.go` | `NewCardHandler` | Injeta usecase e chaves confiáveis / injects usecase and trusted keys. |
| `handler/card.go` | `CreatCard`, `AddValueInCard`, `AuthorizeCard` | Adaptam JSON para operações e respostas HTTP / adapt JSON to operations and HTTP responses. |
| `usecases/card.go` | `CreatCard` | Valida cadastro, deriva routing, gera bcrypt e aplica padrões / validates registration, derives routing, generates bcrypt and forces defaults. |
| `usecases/card.go` | `AddValueInCard` | Valida cadastro/PIN e soma saldo / verifies card/PIN before adding funds. |
| `usecases/sale.go` | `NewSaleCaseDependency` | Injeta repository e produtor Kafka opcional em testes / injects repository and optional Kafka producer for tests. |
| `usecases/sale.go` | `SaleCard` | Orquestra eventos, histórico e despacho por tipo / orchestrates events, history and payment-type dispatch. |
| `usecases/sale.go` | `debit`, `credit` | Autorizam com validações comuns e movimentações próprias / authorize with shared checks and distinct financial updates. |
| `usecases/sale.go` | `VerifyPANRouting` | Formato, Luhn, bandeira e ranges de BIN de seis dígitos / format, Luhn, network and six-digit issuer ranges. |
| `usecases/sale.go` | `authorizationLog` | Converte erros em mensagens fixas de autorização / maps errors to fixed safe authorization messages. |
| `usecases/card_validation.go` | `VerifyCardIdentity`, `VerifyDateExpiry` | Comparam identidade/validade e expiração no Brasil / compare identity/expiry and Brazilian expiry day. |
| `usecases/card_validation.go` | `VerifyDebitBalance`, `VerifyCreditLimit` | Conferem saldo ou limite e valor positivo; crédito protege overflow / check balance or limit and positive price; credit guards overflow. |
| `usecases/payment_security.go` | `VerifyPIN`, `VerifyCryptogram`, `VerifyAESGCM` | Comparam bcrypt/CMAC e autenticam/descriptografam GCM / verify bcrypt/CMAC and authenticate/decrypt GCM. |
| `parser/emv.go` | `DecodeTLV` | Produz `DecodedCardPayment` a partir do contrato recebido / builds decoded payment data from the incoming contract. |
| `parser/emv.go` | `tlvValue`, `decodeBCD`, `amountToCents` | Validam tag/comprimento, interpretam BCD e convertem dinheiro / validate tag/length, decode BCD and convert money. |
| `securitysale/transaction.go` | `VerifyTransaction` | Coordena as validações estruturais / coordinates structural checks. |
| `securitysale/transaction.go` | `verifyPANFormat`, `VerifyDateExpiryFormat`, `verifyTransactionCountry`, `verifyStatus`, `verifyPaymentType`, `isASCIIDigits` | Validam campos decodificados, sem consultar banco / validate decoded fields without database access. |
| `repository/sql.go` | `NewUserRepository`, `CardPANExists` | Injeta conexão e consulta PAN nas duas tabelas / injects connection and checks PAN in both tables. |
| `repository/sql.go` | `InsertCard`, `CreatCard`, `InsertCreditCard` | Inserção com bcrypt no helper legado, persistência de débito e crédito / legacy bcrypt insertion helper, debit and credit persistence. |
| `repository/sql.go` | `AddValue` | Soma centavos no cartão validado / adds cents to the validated card. |
| `repository/sql.go` | `CreatePendingSale`, `UpdateSaleDetails`, `UpdateSaleStatus` | Criam e atualizam somente o histórico pendente / create and update pending history. |
| `repository/sql.go` | `ApproveSale`, `ApproveCreditSale` | Movimentam e aprovam atomicamente / apply movement and approval atomically. |
| `repository/sql.go` | `GetCardByPAN`, `GetCreditCardByPAN` | Buscam cadastro/PIN hash e dados financeiros por PAN e tipo / fetch card identity, PIN hash and financial data by PAN/type. |
| `kafka/producer.go` | `NewKafkaProducer`, `NewProducerCaseDependency`, `ProducerGo`, `bootstrapServers` | Configuram produtor, leitor único de confirmações e publicação / configure producer, one delivery-report reader and publication. |
| `kafka/consumer.go` | `NewKafkaConsumer`, `NewConsumerDependency`, `ConsumerGo` | Configuram observador e consumo cancelável pelo contexto / configure observer and context-cancellable consumption. |
| Migrações SQL / SQL migrations | `validate_transaction_card`, `restrict_transaction_card_change` | Validam referência polimórfica e protegem cartões com histórico / validate polymorphic references and protect cards referenced by history. |

Os caminhos da tabela, exceto `cmd` e `test`, são relativos a `internal/`. / Table paths other than `cmd` and `test` are relative to `internal/`.
