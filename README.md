# Terraform provider for TatNet

[![CI](https://github.com/tatnet-ru/terraform-provider-tatnet/actions/workflows/ci.yml/badge.svg)](https://github.com/tatnet-ru/terraform-provider-tatnet/actions/workflows/ci.yml)

Экспериментальный провайдер на Terraform Plugin Framework и `tatnet-go v0.9.0`.
В Terraform Registry не опубликована. Поддерживаются источник данных `tatnet_image` и минимальный ресурс `tatnet_vm`.

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

Для dev_overrides не запускайте `terraform init`: провайдер ещё не опубликован
в Registry. Не сохраняйте ключ в HCL, tfvars или Git. `api_key` помечен Sensitive;
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
зависимости и `terraform validate` для обоих примеров. CI не требует ключа TatNet
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
публичные IP и управление питанием в этот небольшой шаг не включены.
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
