# Terraform provider for TatNet

[![CI](https://github.com/tatnet-ru/terraform-provider-tatnet/actions/workflows/ci.yml/badge.svg)](https://github.com/tatnet-ru/terraform-provider-tatnet/actions/workflows/ci.yml)

Экспериментальный провайдер на Terraform Plugin Framework и `tatnet-go v0.10.0`.
Опубликован в [Terraform Registry](https://registry.terraform.io/providers/tatnet-ru/tatnet/latest).
Версия 0.4.0 поддерживает `tatnet_image`, `tatnet_vm`, `tatnet_floating_ip`
и `tatnet_vpc` (data source и resource), а также `tatnet_nat_gateway`.

## Установка из Registry

```hcl
terraform {
  required_providers {
    tatnet = {
      source  = "tatnet-ru/tatnet"
      version = "0.4.0"
    }
  }
}

provider "tatnet" {}
```

Передайте API-ключ через `TATNET_API_KEY`, затем выполните `terraform init`.
Релиз подписан ключом `F1A4B97D33F29CC5`. Сохраните `.terraform.lock.hcl`
в репозитории своей инфраструктуры.

## Чтение образа

Обязательные поля: `project_id`, `cluster_id` (UUID региона), `family` (slug),
`version` (версия семейства). Результат `id` — разрешённая аккаунту сборка,
разложенная в указанном регионе. Используется региональный `by_cluster`,
с проверкой наличия ID в доступном каталоге; глобальный ID не подставляется.

Аккаунт определяется API-ключом. Нужны доступ к проекту и `vm:read`.
Для RED OS сначала выдайте аккаунту разрешение на конкретный image через админку.
Отсутствие разрешения, нужной версии или региональной сборки — ошибка чтения.
Служебные семейства не выбираются. Обновление доступной сборки может изменить
`id` при следующем plan; для будущей VM это потребует отдельной политики обновления.

## Чтение DNS — development build

Добавлены data sources `tatnet_dns_zone` и `tatnet_dns_record` по точным UUID.
Проверяются ID записи и её зоны; доступны имя, тип, content, TTL и `managed`.
В опубликованной 0.4.0 этих data sources ещё нет. Они читают метаданные API;
публичный DNS проверяется отдельно. Пример с postconditions для домена и IP:
[examples/dns-read](examples/dns-read/main.tf). Чтение через data sources не меняет DNS.

Добавлен resource `tatnet_dns_record` для create/read/update/delete/import.
Меняется только content; зона, имя и тип требуют замены. TTL общий для RRset,
поэтому этот ресурс его никогда не записывает. Платформенные записи защищены,
ошибки сохраняют state. Пример: [examples/dns-record](examples/dns-record/main.tf).
[Схема и ограничения](docs/resources/dns_record.md). Ресурс также пока доступен
только в development build.

## NAT-шлюз — с версии 0.4.0

`tatnet_nat_gateway` управляет исходящим NAT для VPC и выделяет новый платный
публичный IPv4. Создание ждёт `attached`; удаление — снятия NAT и фактического
освобождения IP. Таймауты/ошибки сохраняют ID; несовпадение UUID шлюза блокирует удаление. Импорт: `VPC_UUID/FLOATING_IP_UUID`.

Не управляйте его IP через `tatnet_floating_ip`: API автоматически освобождает
адрес после выключения шлюза. Пример: [examples/nat-gateway](examples/nat-gateway/main.tf).
[Схема, ограничения и ошибки](docs/resources/nat_gateway.md).
Ресурс доступен с версии 0.4.0. Платный live lifecycle, DNS/HTTPS egress без
индивидуального публичного IP VM, import и полная очистка прошли 9 октября 2026.
DELETE передаёт ожидаемый UUID IP: замена шлюза между GET и DELETE возвращает
409 и сохраняет state. Нужен API с поддержкой `expected_fip_id`.

## Управление VPC — с версии 0.3.0

`resource "tatnet_vpc"` поддерживает create/read/delete/import. Создание ждёт
`active` до десяти минут и сохраняет UUID при ошибках ожидания. Изменение имени,
региона или подсети требует замены. Default-сеть и сеть с NAT удалять нельзя;
отказ API сохраняет state. Серверная защита занятой non-default VPC установлена
9 октября 2026. Live create → plan → import → destroy прошёл через production API;
удаление с reserved IP вернуло 409 и сохранило state. Тестовые ресурсы удалены.

Пример: [examples/vpc-managed](examples/vpc-managed/main.tf).
[Схема, импорт и ограничения](docs/resources/vpc.md).
Ресурс доступен с версии 0.3.0.

## Чтение существующей VPC — с версии 0.3.0

Добавлен `data "tatnet_vpc"`: по UUID читает регион, IPv4-подсеть, имя и
опциональные статус/default-флаг. Требуется `vpc:read`. Пример `examples/vpc`
проверяет совпадение региона через postcondition; `examples/vm` использует
прочитанную сеть. Несовпадение останавливает план до создания VM.

Data source доступен с версии 0.3.0 и не создаёт сеть, NAT или адреса. Проверка метаданных
не подтверждает маршрутизацию или сетевую связность. [Схема](docs/data-sources/vpc.md).

## Локальный запуск

Go 1.25+. Соберите бинарник:

```sh
go build -o bin/terraform-provider-tatnet .
go test ./...
```

Создайте отдельный CLI config (например `/tmp/tatnet-terraform.rc`):

```hcl
provider_installation {
  dev_overrides {
    "tatnet-ru/tatnet" = "/absolute/path/to/terraform-provider-tatnet/bin"
  }
  direct {}
}
```

Укажите абсолютный путь к своему каталогу `bin`. Далее с установленным Terraform:

```sh
export TF_CLI_CONFIG_FILE=/tmp/tatnet-terraform.rc
export TATNET_API_KEY='ваш ключ'
cd examples/image
terraform plan -var='project_id=UUID_ПРОЕКТА' -var='cluster_id=UUID_РЕГИОНА'
```

При использовании dev_overrides запускайте `plan` напрямую: `terraform init`
устанавливает опубликованный релиз из Registry. Не сохраняйте ключ в HCL, tfvars или Git. `api_key` помечен Sensitive;
по умолчанию берётся из `TATNET_API_KEY`. `endpoint` необязателен и по умолчанию
равен `https://api.tatnet.ru/v1`; разрешён HTTPS, редиректы не выполняются,
таймаут запроса 30 секунд. Тело ошибочного ответа API не попадает в диагностику.

## Проверки

Тесты выполняют настоящий Read источника данных через Framework Config/State
и SDK с локальным HTTP-сервером: выбор регионального ID, отсутствие grant,
региона, версии и каталожного ID, служебное семейство, HTTP 401/403/404/500,
повреждённый ответ. Дополнительно проверяются регистрация схемы через Protocol 6
и конфигурация ключа/endpoint. Тесты не создают облачные ресурсы.

Основа: [официальная документация Framework](https://developer.hashicorp.com/terraform/plugin/framework/data-sources).

CI запускает Go build/vet/test с race detector, проверяет форматирование,
зависимости и `terraform validate` для всех примеров. CI не требует ключа TatNet
и не создаёт облачные ресурсы. Используется Terraform 1.14.7 и Go из go.mod.

## Минимальный ресурс tatnet_vm

Пример: `examples/vm/main.tf`. Создаёт предоплаченную VM по существующему тарифу
с одним приватным DHCP-интерфейсом в существующей VPC, без публичного IP.
CPU, память и диск берутся из тарифа. SSH-ключи, период оплаты и автопродление
задаются явно. Для работы нужны vm:create, vm:read, vm:delete; создание также
проверяет у владельца ключа право управления биллингом и списывает средства.
`vm_plan_id` пока нужно получить отдельно: каталога VM-тарифов в используемом
v1-контракте нет. Сам Terraform plan доступность тарифа и баланс не проверяет.

Create сохраняет ID из HTTP 201 до ожидания production-статуса `active`
(также принимает `running` для совместимости). Delete после HTTP 202
ждёт GET 404; таймаут операций 20 минут, интервал 5 секунд. Ошибка/отмена ожидания
сохраняет ресурс в state; Terraform может пометить неудачное создание для замены.
HTTP 403/500 при чтении не удаляют ресурс из state. Read обновляет имя, hostname,
статус и адреса; остальные параметры создания сохраняются из конфигурации,
так как GET не возвращает полный набор исходных параметров.

Любое изменение входных параметров требует замены ВМ (включая image_id).
Диски при замене не сохраняются. In-place update, import, resize, cloud-init,
и управление питанием в ресурс VM не включены. Публичный IP управляется
отдельным ресурсом `tatnet_floating_ip` начиная с версии 0.2.0.
Перед использованием на постоянных данных добавьте lifecycle.prevent_destroy.
В API нет идемпотентного токена создания: POST не повторяется автоматически;
при сетевой ошибке создания проверьте проект, прежде чем повторять apply.

## Проверенный жизненный цикл

Проверены тесты создания, чтения, удаления, ошибок API и таймаутов.
Terraform Protocol 6 проверяет, что смена образа требует замены VM.

8 октября 2026 выполнен live-тест на временной Debian 13 VM:
`apply` → `plan` без изменений → `destroy`. Обнаруженная ошибка ожидания статуса
`active` исправлена и покрыта тестом. Проверено сохранение ID при прерывании
создания и последующее удаление. Тестовые VM удалены. Проверка подтверждает
Terraform/API lifecycle; вход по SSH и приложения внутри гостя не проверялись.

## Разработка

```sh
gofmt -w .
go build ./...
go vet ./...
go test -race ./...
terraform fmt -check -recursive examples
```

Не коммитьте API-ключи, Terraform state/plan, tfvars и локальные логи.
Подготовка релизов описана в [RELEASING.md](RELEASING.md).
Документация для Registry находится в [docs](docs/index.md).

## Лицензия

Apache-2.0; см. [LICENSE](LICENSE).

## Floating IP (с версии 0.2.0)

`tatnet_floating_ip` выделяет адрес в регионе и опционально привязывает его
к `tatnet_vm.example.primary_interface_id`. Имя и интерфейс меняются без
перевыделения адреса; смена региона требует замены. При удалении ресурс
дожидается открепления, затем освобождает IP. Адрес оплачивается и без ВМ,
пока не освобождён. Нужны `floating_ip:read` и `floating_ip:write`.

Существующий адрес можно импортировать по UUID без переподключения. Укажите
его текущие имя, регион и интерфейс в конфигурации, затем проверьте plan.
VPC NAT и адреса с `auto_release=true` этим ресурсом не управляются.
Пример: [examples/floating-ip](examples/floating-ip/main.tf).
[Схема и обработка ошибок](docs/resources/floating_ip.md).

## Атомарный DNS RRset — development build

`tatnet_dns_rrset` управляет всем набором `records` и общим `ttl`.
Создание не перезаписывает существующий набор; update/delete требуют ревизию
из state, конфликт сохраняет state без автоматического повторения.
TTL=null наследует значение зоны. Отдельную запись и её RRset нельзя
управлять разными ресурсами/state. Пример: [examples/dns-rrset](examples/dns-rrset/main.tf).
[Схема, импорт и ограничения](docs/resources/dns_rrset.md). В Registry 0.4.0
ресурса ещё нет.
