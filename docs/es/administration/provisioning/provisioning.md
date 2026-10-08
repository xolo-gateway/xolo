# API de provisioning

La API administra tenants, sus dominios, organizaciones, miembros y roles en un
listener dedicado. Está desactivada por defecto y separada de la API pública.

Expone un **contrato común** — `PUT` idempotentes de tenants, dominios,
organizaciones, miembros y pertenencias, identificados por UUID elegidos por el
cliente — y, bajo `/v1/xolo`, las operaciones propias de Xolo. Este contrato
sustituye a las rutas anteriores: véase
[Migración desde las rutas anteriores](#migracion-desde-las-rutas-anteriores).

## Autenticación y actualización

Se exige **TLS 1.3**, un certificado cliente verificado por la CA configurada y
**exactamente un URI SAN** que coincida de forma exacta con una entrada de
`XOLO_PROVISIONNING_API_AUTHORIZED_URIS`. El Common Name nunca concede acceso.
La verificación se realiza durante el handshake y en el middleware de todas las
rutas, incluidas salud, permisos, rutas desconocidas y métodos rechazados.
Un rechazo HTTP devuelve `403` con `client_certificate_rejected`.

Cada URI autorizado administra toda la instancia, con comprobación de la cadena
tenant/organización/recurso. No existen permisos por certificado. Renovar el
certificado manteniendo el URI conserva la identidad y su presupuesto de solicitudes.

**Instalaciones que ya usan este listener:** antes de reiniciar, emita certificados
con un único URI SAN y configure la lista autorizada. Una lista vacía, un URI inválido
o duplicado impide el arranque. Los clientes deben admitir TLS 1.3.

## Configuración

| Variable | Valor por defecto | Uso |
|---|---|---|
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Activa el listener |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Dirección de escucha |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | — | Certificado del servidor, obligatorio |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | — | Clave privada del servidor, obligatoria |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | — | CA de clientes, obligatoria |
| `XOLO_PROVISIONNING_API_AUTHORIZED_URIS` | — | URI absolutos distintos, separados por comas; obligatorio |
| `XOLO_PROVISIONNING_API_RATE_LIMIT` | `10` | Solicitudes por segundo, por URI y proceso |
| `XOLO_PROVISIONNING_API_RATE_BURST` | `20` | Ráfaga permitida por URI |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Plazo de cierre |
| `XOLO_PROVISIONNING_API_EVENT_RETENTION` | `720h` | Historial conservado en el flujo de eventos, `0` para ilimitado; se aplica aunque el listener esté desactivado |

La tasa y la ráfaga se aplican a cada proceso: con N réplicas, un mismo URI dispone de N veces los valores configurados.

Los presupuestos son locales al proceso, se asignan únicamente a identidades
configuradas y se comparten entre certificados con el mismo URI. Si se supera el
límite, la respuesta es `429`, con `rate_limited` y `Retry-After` en segundos.

El multi-tenant se configura en la instancia, no en esta API:

| Variable | Valor por defecto | Uso |
|---|---|---|
| `XOLO_MULTITENANCY_ENABLED` | `false` | Permite más de un tenant y enruta las solicitudes por dominio |
| `XOLO_MULTITENANCY_HOST_PATTERN` | — | Solo para la actualización: se expande una única vez en un dominio por tenant existente, véase [Dominios y enrutamiento](#dominios-y-enrutamiento) |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | Tenant servido cuando el multi-tenant está desactivado |

Los webhooks tienen sus propias variables, véase [Webhooks](#webhooks).

## Contrato común

| Método | Ruta | Cuerpo |
|---|---|---|
| `GET` | `/v1/manifest` | — devuelve `{"name","version","contract_version","capabilities"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, `"display_name"` e `"identity"` opcionales |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |

Cada uno de estos recursos también puede leerse, listarse y seguirse mediante
el flujo de eventos: consulte [Lecturas, condiciones y sincronización](#lecturas-condiciones-y-sincronizacion).
`capabilities` enumera `adoption`, `business_resources`, `conditional_writes`,
`events`, `identity`, `ownership` y `reads`, más `webhooks` cuando están activados.
Trátela como un conjunto: su orden no es significativo.

- **Los identificadores** son UUID canónicos en minúsculas elegidos por el
  cliente. Cualquier otro valor se rechaza con `400 invalid_parameter`. Un
  miembro es un usuario: `memberID` es el identificador del usuario.
- **Cada `PUT` responde `200`**, creación incluida, con la representación
  almacenada y su `ETag`. Un `PUT` idéntico al estado almacenado no cambia nada,
  no escribe auditoría y conserva su `ETag`: un cliente puede repetir todo su
  estado deseado.
- **Un `PUT` reemplaza la representación.** Un `display_name` omitido queda
  vacío. Una `identity` omitida se conserva, consulte
  [Identidad declarada](#identidad-declarada). Los campos fuera del contrato —
  descripciones, monedas, vínculos de inicio de sesión, roles de plataforma,
  roles personalizados — nunca se modifican.
- **Valores:** `status` es `active` o `suspended`; `tenant_role` es `owner` o
  `member`; el `role` de una organización es `owner`, `admin` o `member`. Los
  slugs se pasan a minúsculas; los nombres tienen de 1 a 200 caracteres, sin
  caracteres de control.
- **Los cuerpos** son un objeto JSON de cadenas (salvo la `identity` de un miembro) con
  `Content-Type: application/json` (si no, `415 unsupported_media_type`), de
  1 MiB como máximo. Un campo desconocido, un valor que no es cadena o Unicode
  inválido da `400 invalid_json`; un campo obligatorio ausente o `null` da
  `400 invalid_representation`. Los `PUT` no aceptan parámetros de consulta
  (`400 invalid_parameter`).

### Tenants

`PUT /v1/tenants/{tenantID}` crea el tenant o lo renombra: slug y nombre cambian
libremente y los dominios siguen asociados. Crear un segundo tenant se rechaza
con `409` mientras `XOLO_MULTITENANCY_ENABLED` sea `false`. El tenant `default`
conserva su slug y sigue `active`: es el que resuelve toda instancia
mono-tenant. Su identificador se obtiene con `GET /v1/xolo/tenants?slug=default`.

Un tenant `suspended` responde `404` en todos sus dominios.

### Organizaciones

`PUT …/organizations/{orgID}` crea la organización con sus roles integrados, o
actualiza su slug, su nombre y su estado. Un identificador de organización ya
usado por otro tenant da `409`.

### Miembros

`PUT …/members/{memberID}` crea el miembro o lo actualiza: email, nombre visible,
rol de tenant, estado e identidad declarada. `suspended` desactiva la cuenta.
Los roles de plataforma nunca se modifican.

**Un miembro puede aprovisionarse antes de su primer inicio de sesión.** Un `PUT`
sobre un identificador desconocido crea la cuenta con el rol de plataforma
`user`, sin vínculo con ningún inicio de sesión: su primer inicio de sesión la
vincula, por su identidad declarada o por un email verificado (ver más abajo).
Un email que otra cuenta del tenant ya tiene, sea cual sea su capitalización, se
rechaza con `409 conflict`. Un token de API sigue designando a su propietario,
vinculado o no.

`tenant_role` declara los propietarios del tenant; no concede ningún privilegio
de plataforma. Un tenant conserva un propietario activo en cuanto tiene uno:
degradar o suspender al último se rechaza con `409 last_owner`.

**Los administradores de plataforma están protegidos.** Todo `PUT` que
modificaría una cuenta con el rol de plataforma `admin` — email, nombre visible,
estado, rol de tenant o identidad — se rechaza con `409 platform_admin_protected`;
un `PUT` idéntico sigue respondiendo `200`. La misma protección se aplica a
`PUT /v1/xolo/tenants/{tenantID}/users`. Un inicio de sesión tampoco vincula
nunca a un administrador de plataforma con una nueva identidad, ni por
declaración ni por email. El provisioning nunca actúa sobre privilegios de
plataforma.

### Identidad declarada

El campo opcional `identity` designa el inicio de sesión de un miembro:

```json
{"email":"jane@corp.tld","tenant_role":"member","status":"active",
 "identity":{"issuer":"https://id.corp.tld/realms/main","subject":"6f0c2a1e"}}
```

| `identity` | Efecto |
|---|---|
| ausente | La identidad declarada y el vínculo de inicio de sesión se conservan: un cliente que ignora el campo nunca desvincula a nadie. |
| `null` | La identidad declarada se retira y el vínculo de inicio de sesión se suelta. El miembro solo vuelve a iniciar sesión mediante una nueva declaración o un email verificado. |
| objeto | La identidad se declara. Un miembro ya vinculado a otro inicio de sesión se rechaza con `409 conflict`: para pasar una cuenta a otro proveedor u otra identidad, envíe `null` y luego la nueva identidad. |

- `issuer` es una URL HTTPS exacta de 2048 bytes como máximo, con host y sin
  userinfo, consulta, fragmento, espacios en los extremos ni caracteres de
  control. `subject` es una cadena UTF-8 exacta de 1 a 255 bytes sin caracteres
  de control. No se normaliza nada: mayúsculas, espacios y la barra final
  cuentan. Cualquier otro valor — `{}`, un campo ausente, vacío o de más, un
  valor que no es cadena — da `400 invalid_representation`. Xolo no llama a
  ningún emisor.
- Una identidad designa como máximo un miembro **por tenant**, declarada o ya
  vinculada: declarar una identidad que tiene otro miembro da `409 conflict`. La
  misma identidad puede tener un miembro distinto en cada tenant.
- `GET`, listas y `ETag` incluyen la identidad declarada. Los eventos solo llevan
  claves y ETags, nunca la identidad.

**Al iniciar sesión**, Xolo resuelve la cuenta en este orden:

1. la cuenta ya vinculada a ese inicio de sesión;
2. el miembro cuya identidad declarada es el emisor y el sujeto que probó el
   proveedor de identidad;
3. la única cuenta del tenant con ese email, comparado sin distinguir
   mayúsculas, cuando el proveedor de identidad lo afirma verificado
   (`email_verified`, o `verified_email` para Google) y la cuenta no está
   vinculada a ningún inicio de sesión ni declara identidad;
4. si no, una cuenta nueva, según `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS`, los
   administradores por defecto y las invitaciones pendientes, como antes.

Un inicio de sesión solo corresponde a una identidad declarada si su proveedor
prueba el emisor: proveedores OIDC con nombre y Gitea con documento de
descubrimiento (el `issuer` descubierto), Google (`https://accounts.google.com`),
y los autenticadores de tokens de esos proveedores. GitHub OAuth y un Gitea sin
descubrimiento no prueban ninguno: sus miembros inician sesión mediante un email
verificado.

Nunca se fusiona ni se reasigna nada. Varias cuentas cuyos emails solo difieren
en mayúsculas rechazan la vinculación y quedan como están; una identidad en
conflicto nunca recurre al email. Un inicio de sesión rechazado responde `409` y
registra un evento `auth.login.failed`. Una petición de una cuenta ya vinculada
solo la lee: sin transacción ni bloqueo. Vincular un inicio de sesión no cambia
ninguna proyección ni publica ningún evento.

Límites conocidos: los administradores por defecto
(`XOLO_HTTP_AUTHN_DEFAULT_ADMINS`) se reconocen por el email que devuelve el
proveedor de identidad, verificado o no; y cada inicio de sesión sigue copiando
en la cuenta el email y el nombre visible que devuelve el proveedor de
identidad, salvo cuando el plano de control gestiona los miembros (véase
*Miembros gestionados*).

### Pertenencias

`PUT …/organizations/{orgID}/members/{memberID}` añade el miembro a la
organización o actualiza su pertenencia. `role` fija el rol integrado de la
pertenencia; los roles personalizados asignados mediante `/v1/xolo` se
conservan. Una pertenencia `suspended` no da acceso a la organización y conserva
sus roles.

Una organización conserva un propietario activo en cuanto tiene uno: degradar o
suspender al último se rechaza con `409 last_owner`. Una organización o un
miembro de otro tenant da `404 parent_not_found`.

## Dominios y enrutamiento

`PUT /v1/tenants/{tenantID}/domains/{hostname}` declara un nombre de host del
tenant o cambia su estado. El nombre de host debe estar ya en minúsculas, sin
puerto ni dirección IP (si no, `400 invalid_hostname`), y pertenece a un solo
tenant (si no, `409`). Un tenant puede tener varios dominios.

Con `XOLO_MULTITENANCY_ENABLED=true`, el servidor público enruta cada solicitud
por estos dominios: el host de la solicitud debe ser un dominio `active` de un
tenant `active`; si no, la solicitud responde `404`. Enlaces, redirecciones y
callbacks OAuth conservan el esquema, el puerto y la ruta de `XOLO_HTTP_BASE_URL`
y toman el dominio como host. En una instancia mono-tenant los dominios se
guardan pero no se usan para enrutar.

En el primer arranque multi-tenant de una instancia actualizada,
`XOLO_MULTITENANCY_HOST_PATTERN`, si está definido, se expande una única vez en
un dominio `active` por tenant existente, de modo que cada tenant sigue
accesible en su nombre de host anterior. La expansión no vuelve a ejecutarse:
los tenants creados después deben declarar sus dominios mediante la API, y la
variable puede eliminarse. Un nombre de host ya declarado se conserva y se
registra en los logs, nunca se reasigna.

## Lecturas, condiciones y sincronización

Cada recurso del contrato común tiene una **proyección**: la representación que
devuelve su `PUT`, mantenida en la misma transacción que el recurso. Todo cambio
de una proyección, sea cual sea su origen — esta API, la interfaz web, un inicio
de sesión, una invitación aceptada, una eliminación — recibe la siguiente
posición de un único **flujo de eventos** y publica un evento.

| Método | Ruta | Respuesta |
|---|---|---|
| `GET` | `/v1/tenants/{tenantID}`, `…/domains/{hostname}`, `…/organizations/{orgID}`, `…/members/{memberID}`, `…/organizations/{orgID}/members/{memberID}` | La representación, con su cabecera `ETag` |
| `GET` | `/v1/tenants`, `…/domains`, `…/organizations`, `…/members`, `…/organizations/{orgID}/members` | `{"items":[{"key","representation","etag"}],"next_cursor"}` |
| `GET` | `/v1/events/cursor` | `{"cursor"}`: el final actual del flujo |
| `GET` | `/v1/events?cursor=` | `{"items":[…],"next_cursor","has_more"}` |

### ETags y escrituras condicionales

- **El ETag es una revisión**, `W/"<n>"`: la posición del flujo del último
  cambio de la representación. Las revisiones se persisten y crecen
  estrictamente en toda la instancia; no dependen de ningún reloj: ni un salto
  de reloj, ni un desfase entre réplicas, ni eliminar y volver a crear un
  recurso hacen reaparecer un ETag. Los recursos existentes recibieron su propia
  revisión al actualizar. Los ETags son validadores opacos: compárelos, no los
  calcule.
- Un `PUT` devuelve la proyección escrita por su propia transacción y su
  `ETag`. Un `PUT` que no cambia nada conserva la revisión.
- **`If-Match`** en un `PUT` se comprueba dentro de la transacción de mutación,
  antes de cualquier escritura: `*` o una lista de ETags separados por comas,
  comparados de forma débil. Una condición obsoleta responde
  `412 precondition_failed`, incluso con un cuerpo idéntico al estado
  almacenado; `*` sobre un recurso inexistente también da `412`. Sin
  `If-Match`, la escritura es incondicional. De dos escritores con el mismo
  ETag, solo uno tiene éxito. Una condición mal formada, o cualquier
  `If-None-Match`, da `400 invalid_precondition`.

### Listas y cursores

- Una colección es la ruta de su recurso unitario sin el último segmento. Las
  listas solo aceptan `limit` (de 1 a 1000, 100 por defecto) y `cursor`, una
  vez cada uno (si no, `400 invalid_parameter`); las lecturas unitarias,
  `/v1/manifest` y `/v1/events/cursor` no aceptan parámetros de consulta.
- Los elementos se ordenan según el orden binario de su clave. Una página no es
  una instantánea de la colección: los cambios confirmados durante una
  enumeración se recuperan con el flujo. No hay total.
- `next_cursor` es `null` en la última página. Los cursores se firman con un
  secreto de la instancia y están ligados a la colección, a sus padres y al
  límite: un cursor alterado, o usado para otra colección, otro límite o el
  flujo, responde `400 invalid_cursor`. Un cursor de lista caduca 24 horas
  después de la primera página, sin renovación (`410 cursor_expired`). Los
  cursores son opacos pero no están cifrados.
- Un padre desconocido responde `404 parent_not_found` en una lista y
  `404 not_found` en una lectura unitaria.

### Flujo de eventos

Los eventos siguen un perfil cerrado de [CloudEvents 1.0](https://cloudevents.io),
sin representación ni datos personales:

```json
{"specversion":"1.0","id":"…","source":"urn:uuid:…","type":"organization.updated.v1",
 "time":"…","datacontenttype":"application/json","sequence":"42","requestid":"…",
 "data":{"resource_type":"organization","key":{"tenant_id":"…","organization_id":"…"},"etag":"W/\"42\""}}
```

- `type` es `<resource_type>.created.v1`, `.updated.v1` o `.deleted.v1`, donde
  `resource_type` es `tenant`, `tenant_domain`, `organization`, `member`,
  `organization_membership` o una de las familias de negocio `provider`,
  `custom_role`, `application`, `quota` y `alert`. Una eliminación no lleva
  `etag`.
- **Un evento por recurso y por commit.** Una operación sin cambios o una
  transacción revertida no publica nada. Dentro de un commit, las eliminaciones
  van primero, de los hijos a los padres, y luego las creaciones y
  modificaciones, de los padres a los hijos.
- `sequence` ordena los eventos **en el orden de los commits**: una posición
  solo es visible cuando cada posición inferior es visible o ha sido revertida;
  un consumidor que reanuda tras la última posición leída nunca se salta un
  evento. Las posiciones tienen huecos. `time` es solo informativo.
  `requestid` es el `X-Request-ID` de la petición de provisioning, o una
  correlación generada para los demás cambios.
- `has_more=false` significa que la página alcanzó el final del flujo; su
  `next_cursor` reanuda desde ahí. Los cursores de eventos nunca caducan por sí
  mismos.
- Los usuarios técnicos de las aplicaciones no son miembros: no se proyectan ni
  pueden modificarse mediante `PUT …/members/{memberID}`.

**Algoritmo del consumidor.**

1. Capture un cursor con `GET /v1/events/cursor`, **antes** del inventario.
2. Enumere `/v1/tenants` y luego los dominios, organizaciones, miembros y
   pertenencias de cada tenant.
3. Consulte `GET /v1/events?cursor=`; para cada evento, vuelva a leer el
   recurso y aplique su representación actual (`404` significa que ha
   desaparecido). Deduplique los eventos por `(source, id)`; no permita nunca
   que una lectura anterior sobrescriba el resultado de una posterior.
4. Persista el estado aplicado y **después** el `next_cursor`. Una caída entre
   ambos pasos repite eventos, sin consecuencias.
5. Ante `410 cursor_expired`, reconstruya una nueva generación desde el paso 1
   y cambie a ella solo cuando esté completa.

### Retención

`XOLO_PROVISIONNING_API_EVENT_RETENTION` (`720h` por defecto, `0` conserva todos
los eventos) elimina cada hora los eventos más antiguos creados antes del
periodo de retención. La eliminación se detiene en el primer evento conservado:
un retroceso del reloj nunca elimina un evento que sigue a uno conservado. Un
cursor anterior a los eventos eliminados responde `410 cursor_expired`. La
retención se ejecuta aunque el listener esté desactivado, ya que los cambios se
publican en cualquier caso. Solo esta retención elimina eventos: eliminar
recursos nunca invalida los cursores de otros consumidores.

### Almacenamiento y rendimiento

- La fuente del flujo y el secreto que firma los cursores se guardan en la base
  de datos: inclúyalos en sus copias de seguridad. Restaurar otra base invalida
  los cursores (`400 invalid_cursor`) y cambia la `source`: los consumidores
  reconstruyen.
- Una transacción solo toma el **bloqueo del flujo** cuando modifica una
  proyección, al final, y lo mantiene hasta el commit; en PostgreSQL es un
  bloqueo consultivo junto a una secuencia. Las lecturas, las operaciones sin
  cambios, los inicios de sesión que no cambian nada y el proxy LLM nunca lo
  toman. Los cambios de proyecciones se serializan así durante el breve tiempo
  de su commit: menos rendimiento de escritura a cambio de un flujo sin huecos.

## Webhooks

Los webhooks envían los eventos del [flujo](#flujo-de-eventos) a receptores
HTTPS, a medida que se confirman. Son notificaciones, no una fuente de verdad:
un consumidor conserva su propio punto de reanudación en el flujo, consulta
`/v1/events` al arrancar, tras una reconexión y periódicamente, y nunca avanza
ese punto por un webhook. Un webhook perdido nunca hace perder un cambio.

Los webhooks están desactivados por defecto. El worker de entrega se ejecuta en
cada proceso con `XOLO_WEBHOOKS_ENABLED=true`, esté o no activado allí el
listener de aprovisionamiento; las suscripciones se gestionan a través del
listener.

| Variable | Valor por defecto | Uso |
|---|---|---|
| `XOLO_WEBHOOKS_ENABLED` | `false` | Ejecuta la preparación, la entrega y la limpieza de los webhooks, y anuncia `webhooks` en el manifiesto |
| `XOLO_WEBHOOKS_ALLOWED_ORIGINS` | — | Orígenes HTTPS separados por comas (`https://host[:puerto]`, sin ruta) a los que puede apuntar una suscripción; obligatorio |
| `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS` | `false` | Permite también las direcciones de loopback y privadas |
| `XOLO_WEBHOOKS_TLS_CA_FILE` | — | Autoridades de confianza adicionales (PEM); las del sistema siguen siendo de confianza |
| `XOLO_WEBHOOKS_WORKERS` | `2` | Entregas simultáneas por proceso, de 1 a 16 |
| `XOLO_WEBHOOKS_POLL_INTERVAL` | `1s` | Intervalo de preparación y de consulta, de 100 ms a 1 minuto |
| `XOLO_WEBHOOKS_QUEUE_CAPACITY` | `10000` | Entregas en espera o en curso en la instancia |
| `XOLO_WEBHOOKS_SUBSCRIPTION_CAPACITY` | `1000` | Entregas en espera o en curso de una suscripción, como máximo la capacidad de la cola |

### Suscripciones

| Método | Ruta | Respuesta |
|---|---|---|
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks` | `{"items":[…]}` |
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | La suscripción |
| `PUT` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | La crea o la sustituye: `200` y la suscripción |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | La elimina con sus entregas: `204` |
| `POST` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}/reset` | `{"acknowledgeLoss":true}`: descarta sus entregas y continúa al final del flujo, `204` |
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}/deliveries` | Las 100 últimas entregas, sin el evento ni la respuesta |

```json
{"destination":"https://hooks.example.com/xolo","events":["organization.updated.v1","organization.deleted.v1"],
 "enabled":true,"secrets":["whsec_…"]}
```

- Una suscripción entrega los eventos de **su tenant únicamente**. Su
  identificador es un UUID elegido por el cliente y único en la instancia:
  nunca pasa a otro tenant (`409 conflict`), y la suscripción de otro tenant
  responde `404 not_found`. Un tenant tiene como máximo 10 suscripciones
  (`409 webhook_capacity`).
- `events` es `["*"]` o una lista de hasta 20 tipos de eventos distintos del
  flujo. `destination` debe pertenecer a un origen permitido.
- `secrets` contiene un secreto, o dos distintos durante una rotación: base64,
  con el prefijo opcional `whsec_`, de 32 a 64 bytes aleatorios generados por
  el cliente. Son obligatorios al crearla; omitirlos en un `PUT` posterior
  conserva los guardados. Nunca se devuelven: la suscripción solo muestra
  `secretCount`. Se cifran con `XOLO_SECRET_KEY` y quedan ligados a su tenant y
  a su suscripción: guarde esa clave con la base de datos.
- Una suscripción nueva empieza al final del flujo: reconstruya primero el
  estado del consumidor y después apóyese en las notificaciones.
- `enabled: false` pausa la suscripción sin perder su posición. Los cambios de
  destino y de secretos se aplican a las entregas ya en cola.
- Cada escritura de una suscripción se audita con el llamante y el
  `X-Request-ID`, nunca con sus secretos.

### Entrega

Cada intento es un `POST` HTTPS del evento exacto del flujo, según
[Standard Webhooks](https://www.standardwebhooks.com):

| Cabecera | Valor |
|---|---|
| `Content-Type` | `application/cloudevents+json` |
| `webhook-id` | El `id` del evento, el mismo en cada intento |
| `webhook-timestamp` | Segundos Unix de este intento |
| `webhook-signature` | `v1,<HMAC-SHA256 en base64 de "<id>.<timestamp>.<cuerpo>">` por cada secreto, separadas por espacios |

Un receptor verifica una de las firmas sobre el cuerpo bruto antes de
decodificarlo, rechaza una marca de tiempo alejada más de cinco minutos,
deduplica por `(source, id)` y responde `2xx` una vez aceptado el evento de
forma duradera.

- **Al menos una vez, sin orden garantizado.** Un reintento, una caída tras la
  respuesta del receptor o dos réplicas pueden repetir un evento; use
  `sequence` para ordenar y el `GET` unitario para leer el estado actual.
- Un `2xx` confirma la entrega; su cuerpo se lee hasta 64 KiB y se descarta.
  Las redirecciones no se siguen. Todo lo demás se reintenta tras 5, 10, 20…
  segundos, con una hora como máximo entre intentos, durante 12 intentos o 24
  horas como máximo; la entrega pasa entonces a `failed`. Reconcilie mediante
  el flujo.
- Una entrega se reserva durante 30 segundos: un worker que se detiene a mitad
  de un intento, en cualquier réplica, la deja a otro cuando expira la reserva.
  Un resultado tardío nunca sobrescribe un intento más reciente.
- Las entregas son copias de su evento: la retención del flujo nunca elimina
  una entrega en cola. Las entregas terminadas se conservan siete días para el
  diagnóstico.
- Los workers nunca toman el bloqueo del flujo: solo leen el flujo de su
  tenant, y la ruta de las solicitudes nunca los espera.

### Destinos

Solo los orígenes permitidos son accesibles. Cada dirección a la que resuelve
el nombre se comprueba al conectar, y la dirección comprobada es la que se
marca, de modo que el nombre no puede redirigirse entretanto. Las direcciones
link-local —incluidos los puntos de metadatos de la nube—, multicast, no
especificadas, compartidas y de uso especial se rechazan siempre; el loopback y
las direcciones privadas solo con `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS=true`.
Los proxies del entorno se ignoran y los certificados se verifican siempre.
Restrinja también el tráfico saliente del proceso con un cortafuegos.

### Estados y recuperación

| `state` | Significado |
|---|---|
| `ready` | Al día, o poniéndose al día |
| `backpressure` | Se alcanzó la capacidad de la cola: la preparación se detiene en su posición y continúa cuando terminan entregas. Solo cuentan las entregas en espera y en curso, y una suscripción solo puede llenar su propia parte |
| `history_lost` | La [retención](#retencion) eliminó eventos que la suscripción aún no había preparado, típicamente tras una pausa larga. Nunca se saltan en silencio: reconstruya el consumidor y después reinicie la suscripción |

Un tenant suspendido no recibe nada: sus suscripciones se pausan y continúan al
reactivarse, suspensión incluida. Una pausa —suscripción desactivada o tenant
suspendido— más larga que la retención termina en `history_lost`, ya que los
eventos purgados ya no pueden distinguirse. Eliminar un tenant elimina sus suscripciones
y sus entregas. Solo la retención del flujo completo puede llevar una
suscripción a `history_lost`.

### Supervisión

| Métrica | Significado |
|---|---|
| `xolo_webhook_attempts_total` | Intentos de este proceso |
| `xolo_webhook_failures_total{reason}` | Fallos de este proceso, por diagnóstico |
| `xolo_webhook_queue{state}` | Entregas de la base de datos, por estado |
| `xolo_webhook_lag_seconds{stage}` | Antigüedad del evento más antiguo aún no preparado (`materialization`) o entregado (`delivery`) |
| `xolo_webhook_history_lost`, `xolo_webhook_backpressure` | Suscripciones en ese estado |

Los indicadores describen toda la base de datos y los muestrea cada proceso:
tome su máximo entre réplicas, no su suma. Ninguna etiqueta lleva un tenant, un
destino ni un evento. Alerte ante un retraso sostenido, ante los fallos y ante
cualquier `history_lost`.

## Extensiones Xolo

Las operaciones propias de Xolo están bajo `/v1/xolo`. Sus cuerpos son JSON en
camelCase, las marcas de tiempo RFC 3339, las colecciones tienen el formato
`{"items": […], "page": 1, "limit": 50, "total": 123}` y los campos desconocidos
se rechazan.

| Método | Ruta | Notas |
|---|---|---|
| `GET` | `/v1/xolo/healthz` | También detrás de mTLS |
| `GET` | `/v1/xolo/permissions` | El catálogo RBAC: única fuente de códigos de permiso válidos |
| `GET` | `/v1/xolo/tenants` | `?slug=` para una búsqueda exacta; si no, `?page=&limit=` |
| `GET` | `/v1/xolo/tenants/{tenantID}` | |
| `PATCH` | `/v1/xolo/tenants/{tenantID}` | `name`, `description`, `active` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations` | `?slug=` para una búsqueda exacta; si no, `?page=&limit=` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | |
| `PATCH` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | `name`, `description`, `active`, `currency`, `shareQuotaEqually` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members` | Paginado |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles` | Reemplazo completo de los roles |
| `GET`, `POST`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles[/{key}]` | Roles personalizados: véase *Recursos de negocio* |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/builtin` | Roles integrados |
| `GET` | `/v1/xolo/tenants/{tenantID}/users` | `?provider=&subject=` para una búsqueda exacta; si no, `?search=&active=&page=&limit=` |
| `PUT` | `/v1/xolo/tenants/{tenantID}/users` | Upsert idempotente sobre `(provider, subject)`: `201` al crear, `200` si no |
| `GET` | `/v1/xolo/tenants/{tenantID}/users/{userID}` | |
| `GET` | `/v1/xolo/ownership` | Autoridad de escritura efectiva de cada familia |
| `GET` | `/v1/xolo/adoption/export` | Exportación de adopción, flujo NDJSON: véase *Autoridad de escritura, adopción y desvinculación* |

## Recursos de negocio

Cinco familias propias de Xolo siguen el mismo contrato que las familias
comunes: lectura unitaria con `ETag`, listas paginadas, `PUT` condicionales y
eventos. El manifiesto anuncia `business_resources`.

| Método | Ruta |
|---|---|
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/applications[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/alerts[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/providers[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/quotas[/{key}]` |
| `POST` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` — crea un rol personalizado con una clave elegida por el servidor; responde `201` con `{key, representation, etag}` |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{key}` — elimina un rol personalizado (`204`) |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/builtin` — los roles integrados, `{"items":[{"id","builtin_kind","name"}]}`, para `PUT …/members/{membershipID}/roles` |

Las representaciones están en snake_case y son completas: cada campo es
obligatorio, `null` solo donde se indica.

| Familia | Representación |
|---|---|
| `custom_role` | `{"name","description","permissions":[…],"model_grants":[{"model_id","kind"}]}` |
| `application` | `{"name","description","active","role_ids":[…]}` |
| `quota` | `{"scope","scope_id","currency","daily_budget","monthly_budget","yearly_budget"}`, presupuestos en microcéntimos o `null` |
| `alert` | `{"name","description","scope","owner_id","query","aggregation","window_seconds","comparator","threshold","for_seconds","enabled"}` |
| `provider` | `{"name","type","base_url","active","currency","cloud_tier","billing_mode","subscription_plan","retry_config","rate_limit_config"}`, los tres últimos `null` u objetos, más `"api_key"` de solo escritura |

- **Claves.** Un recurso nuevo se crea con un UUID canónico elegido por el
  cliente. Un recurso creado desde la interfaz web conserva su identificador
  local, aceptado en lectura y actualización, nunca en creación. Los eventos y
  los elementos de lista llevan la clave en `key.resource_id`, con
  `key.organization_id` salvo para las cuotas.
- **Padres.** Cada referencia se comprueba en la transacción de la escritura.
  Un grant designa un modelo de la organización (y, para un modelo LLM, un
  proveedor de la organización); `role_ids` designa roles de la organización;
  una cuota limita una organización del tenant, un miembro del tenant o una
  aplicación de una de sus organizaciones; el propietario de una alerta es un
  miembro activo de la organización, obligatorio para una alerta `personal`.
  Un padre ausente o ajeno responde `404 parent_not_found`; un recurso de otra
  organización o de otro tenant responde `404 not_found`, como uno ausente.
- **Campos inmutables.** El `scope` y el `scope_id` de una cuota, el `scope` y
  el `owner_id` de una alerta nunca cambian (`409 conflict`). Un ámbito tiene
  una sola cuota: otra clave sobre el mismo ámbito responde `409 conflict`.
- **Normalización.** Permisos, grants e identificadores de roles se ordenan y
  deduplican: una repetición en otro orden no cambia nada.
- **El estado de ejecución queda fuera del contrato.** Un `PUT` de cuota
  conserva el consumo ya registrado. El estado de evaluación de una alerta no
  se devuelve ni se publica; un cambio de la alerta la reinicia desde `ok`,
  como una edición en la interfaz web. Los roles integrados no forman parte de
  la colección: escribir uno responde `409 conflict`.
- **Las claves de API de los proveedores son de solo escritura.** `api_key` es
  obligatoria al crear y se conserva cuando se omite. Se cifra con
  `XOLO_SECRET_KEY` y nunca aparece en una respuesta, una proyección, un evento
  ni en claro en la auditoría: una rotación no cambia ningún `ETag` ni publica
  ningún evento, pero la auditoría registra el cambio de una huella del
  cifrado.
- **Sin eliminación mediante el contrato**, salvo los roles personalizados: las
  eliminaciones llegarán con el ciclo de vida de los recursos. Los tokens de
  aplicación siguen gestionándose localmente.
- Las escrituras de la interfaz web también publican sus eventos. La migración
  `202610120001` proyecta los recursos de negocio existentes sin publicar
  eventos; como las anteriores, exige detener todos los servidores.

Límite conocido: la caché de cada proceso mantiene válidos los tokens de una
aplicación desactivada hasta que caducan sus entradas
(`XOLO_STORAGE_DATABASE_CACHE_USERS_TTL`): una escritura mediante este
listener solo vacía la caché de su propio proceso.

## Autoridad de escritura, adopción y desvinculación

Cada familia del contrato común, así como las suscripciones webhook, tiene una
**autoridad de escritura**: la instancia local (interfaz web, inicio de sesión,
invitaciones), el plano de control (este listener) o ambos.

| Variable | Por defecto | Descripción |
|---|---|---|
| `XOLO_OWNERSHIP` | — | Pares `familia=autoridad` separados por comas. Familias: `tenant`, `tenant_domain`, `organization`, `member`, `organization_membership`, `subscription`, y las familias de negocio `provider`, `custom_role`, `application`, `quota`, `alert`. Autoridades: `shared`, `local`, `control_plane`. Las familias omitidas valen `shared`; una familia o autoridad desconocida impide el arranque |

```dotenv
XOLO_OWNERSHIP=tenant=control_plane,tenant_domain=control_plane,organization=control_plane,member=control_plane,organization_membership=control_plane,subscription=control_plane
```

- `shared`, el valor por defecto, conserva el comportamiento anterior: ambos
  escriben.
- `local` rechaza las escrituras de este listener con `403 ownership_denied`.
- `control_plane` rechaza las escrituras locales: la interfaz web responde
  `403`, tanto en páginas completas como en fragmentos htmx. Las invitaciones
  producen pertenencias: crearlas, revocarlas o aceptarlas depende de
  `organization_membership`.

La política rige la **representación pública** de los recursos, la que
devuelve `GET`. Se comprueba en la transacción de la escritura, en cada
proyección que la escritura modifica, cascadas incluidas: eliminar un tenant
cuyos miembros dependen de otra autoridad se rechaza en bloque, sin eliminar
ni publicar nada. Los campos fuera del contrato siguen siendo locales sea cual
sea la política: roles de plataforma, moneda y reparto de cuota de una
organización, modelos, modelos virtuales, middlewares y tokens de
aplicación. Las cuentas de aplicación y los tokens de API
siguen siendo utilizables, y las lecturas conservan sus permisos.
`GET /v1/xolo/ownership` devuelve la política efectiva, y el manifiesto anuncia
`ownership`.

Una autoridad de escritura nunca concede un privilegio: los administradores de
plataforma siguen protegidos (`409 platform_admin_protected`) cuando el plano
de control gestiona los miembros.

La política se lee al arrancar; no se almacena en la base de datos. Detenga
**todos** los servidores y workers antes de cambiarla, y reinicie cada réplica
con el mismo valor: una réplica que aún ejecuta una política anterior sigue
aceptando las escrituras que esta permite. Cada proceso registra su política
efectiva al arrancar (`write authority policy`). Los comandos de operador fuera
de línea (`xolo-migrate`, `xolo-adoption`) tienen la autoridad de la base de
datos y no comprueban ninguna política.

### Miembros gestionados

Con `member=control_plane`, un inicio de sesión solo resuelve los miembros que
el plano de control declaró:

- vincula la identidad probada a un miembro existente — su identidad
  declarada y luego un email verificado — sin modificar su proyección;
- nunca copia el email ni el nombre visible del proveedor de identidad: se
  conserva el perfil declarado;
- no crea ninguna cuenta, digan lo que digan
  `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS` o una invitación pendiente, salvo para los
  administradores por defecto, que inicializan la instancia, y las cuentas de
  aplicación. Cualquier otra identidad se rechaza con `403`.

### Exportación de adopción

`GET /v1/xolo/adoption/export`, al igual que `xolo-adoption export`, transmite
en flujo el inventario de la instancia: cada proyección de las familias comunes y de
negocio de todos los tenants, recursos suspendidos incluidos, leídas sobre una misma
instantánea coherente sin tomar ningún bloqueo. El formato, `xolo-adoption/1`,
es NDJSON (`application/x-ndjson`):

```text
{"version":"xolo-adoption/1","contract":"0.1.0-draft.1","source":"urn:uuid:…","c0":"…","families":["tenant","tenant_domain","organization","member","organization_membership","provider","custom_role","application","quota","alert"]}
{"family":"tenant","key":{"tenant_id":"…"},"representation":{…},"etag":"W/\"42\""}
…
{"count":128,"complete":true,"sha256":"…"}
```

- Los registros siguen el orden de `families`, padres antes que hijos; `key`,
  `representation` y `etag` son los que devuelve `GET`.
- `c0` es un cursor de `/v1/events` tomado sobre la misma instantánea: los
  eventos posteriores son exactamente los cambios ausentes de la exportación.
- `sha256` es el SHA-256 hexadecimal en minúsculas de los bytes exactos de
  todas las líneas anteriores a la última, saltos de línea incluidos. Detecta
  la corrupción y el truncamiento, no un reemplazo deliberado: el transporte y
  los permisos del archivo establecen su procedencia.
- Una exportación interrumpida carece de su última línea, y la verificación la
  rechaza.
- La exportación contiene emails de contacto e identidades declaradas:
  protéjala como la base de datos. No contiene ningún vínculo de inicio de
  sesión no declarado, sesión, token, secreto ni entrada de auditoría.
  `verify` también acepta una exportación que solo enumera las cinco familias
  comunes, producida antes de las familias de negocio.

`xolo-adoption verify -in <archivo>` (`-in -` lee la entrada estándar)
comprueba la suma de verificación, el número de registros, la versión y el
contrato de la cabecera, la sintaxis de las claves, los estados, los
duplicados y que cada padre preceda a sus hijos, conservando solo las claves en
memoria. Muestra la fuente, `c0` y el número de registros de cada familia.

### Adoptar una instancia

1. Pruebe un inicio de sesión real de administrador de plataforma y haga una
   copia de seguridad de la base de datos. Deje todas las familias en
   `shared`.
2. Exporte (`xolo-adoption export -out /secure/inventory.ndjson` con
   `XOLO_STORAGE_DATABASE_DSN`, o la ruta anterior), verifique el archivo y
   prepárelo en el plano de control, conservando los UUID calificados por la
   `source` del flujo. Resuelva las colisiones de slug, dominio y email en el
   plano de control sin cambiar ningún identificador.
3. Reproduzca `/v1/events` desde `c0` hasta ponerse al día. Un
   `410 cursor_expired` obliga a empezar de nuevo desde una nueva exportación.
4. Detenga todos los servidores y workers, ponga al día el flujo una última
   vez, defina `XOLO_OWNERSHIP` y reinicie cada réplica. Compruebe
   `GET /v1/xolo/ownership`, el rechazo de una escritura local y un inicio de
   sesión de administrador. No se reescribe ningún recurso.

### Desvincular una instancia

La desvinculación devuelve la instancia a su administración local.

1. Pruebe un inicio de sesión de administrador de plataforma que siga siendo
   utilizable sin el plano de control y haga una copia de seguridad de la base
   de datos.
2. Detenga todos los servidores, workers y demás escritores de la base de
   datos.
3. Ejecute:

   ```sh
   XOLO_STORAGE_DATABASE_DSN=… xolo-adoption detach -writers-stopped -operator-access-verified
   ```

4. Retire los valores `control_plane` de `XOLO_OWNERSHIP` y reinicie cada
   réplica; después compruebe un inicio de sesión y una escritura local.

El comando se niega a ejecutarse si ningún administrador de plataforma activo
de un tenant activo tiene un vínculo de inicio de sesión. En una sola
transacción, elimina cada suscripción webhook, sus secretos cifrados y sus
entregas pendientes, registra una entrada de auditoría que nombra al operador
(`urn:xolo:operator:<uid>`) e indica cuántas suscripciones y entregas eliminó.
Recursos, UUID, identidades declaradas, vínculos de inicio de sesión,
auditoría, flujo de eventos y su fuente permanecen intactos. Un webhook ya
enviado no puede recuperarse. Las dos opciones dan fe de las comprobaciones
anteriores: el comando no puede detener otros procesos.

`xolo-adoption` se distribuye junto a `xolo-server` en las releases y las
imágenes de contenedor (`/usr/local/bin/xolo-adoption`). `export` y `verify`
solo leen, y ninguna acción migra el esquema: utilice el binario de la versión
que migró la base de datos.

## Errores

Todos los errores comparten el formato `{"error":{"code":"…","message":"…"}}`.

| Código | HTTP | Causa |
|---|---|---|
| `invalid_parameter` | 400 | Identificador que no es un UUID canónico, o parámetro de consulta que una ruta común no define |
| `invalid_cursor` | 400 | Cursor alterado, vacío o emitido para otra colección, otro límite o el flujo |
| `invalid_precondition` | 400 | `If-Match` mal formado, o `If-None-Match` |
| `invalid_json` | 400 | Ruta común: JSON mal formado, campo desconocido, valor que no es cadena, Unicode inválido |
| `invalid_representation` | 400 | Ruta común: campo obligatorio ausente, `null` o `identity` de miembro inválida |
| `invalid_hostname` | 400 | Nombre de host no en minúsculas, con puerto, dirección IP o etiqueta inválida |
| `invalid_request` | 400 | `/v1/xolo`: cuerpo mal formado, campo desconocido, parámetro de consulta inválido |
| `client_certificate_rejected` | 403 | Certificado o URI de cliente no autorizado |
| `ownership_denied` | 403 | La política de autoridad reserva la familia a la instancia local |
| `not_found` | 404 | Recurso o ruta desconocidos, o recurso de otro tenant u otra organización |
| `parent_not_found` | 404 | El tenant, la organización o el miembro del que depende el recurso no existe en ese ámbito |
| `method_not_allowed` | 405 | Recurso conocido, método incorrecto |
| `unsupported_media_type` | 415 | Ruta común sin `Content-Type: application/json` |
| `conflict` | 409 | Identificador o nombre de host de otro tenant, slug ya usado, identidad o email de otro miembro o invariante de negocio |
| `last_owner` | 409 | El cambio dejaría un tenant o una organización sin propietario activo |
| `platform_admin_protected` | 409 | El cambio afecta a un administrador de plataforma |
| `webhook_capacity` | 409 | El tenant ya tiene el número máximo de suscripciones webhook |
| `cursor_expired` | 410 | Cursor de lista de más de 24 horas, o cursor de eventos anterior a los eventos conservados: reconstruya |
| `precondition_failed` | 412 | `If-Match` no designa la revisión actual |
| `unprocessable` | 422 | Valor bien formado pero rechazado por el dominio |
| `rate_limited` | 429 | Presupuesto por URI superado; reintente tras los segundos de `Retry-After` |
| `internal_error` | 500 | Error inesperado |

Los mensajes siempre se construyen explícitamente: trazas, errores SQL, rutas de
archivos, detalles TLS y secretos nunca llegan al cliente.

## Invariantes

- Una autoridad de escritura nunca concede un privilegio: la política de
  autoridad solo decide quién puede escribir una familia, y todas las demás
  invariantes siguen aplicándose.
- El provisioning **nunca** concede ni modifica privilegios de plataforma. Un
  usuario creado mediante `PUT /v1/xolo/tenants/{tenantID}/users` o un `PUT` de
  miembro recibe exactamente el rol de plataforma `user`, los roles de
  plataforma nunca se modifican y un administrador de plataforma no se modifica
  en absoluto.
- Una identidad designa como máximo una cuenta por tenant. Un inicio de sesión
  nunca fusiona cuentas, nunca vuelve a vincular una cuenta ya vinculada y nunca
  vincula a un administrador de plataforma.
- Las direcciones de `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` están reservadas:
  escribirlas en un usuario se rechaza con `422`.
- Un tenant o una organización conserva al menos un propietario activo en cuanto
  tiene uno.
- Una pertenencia suspendida no concede nada; un dominio o un tenant suspendido
  no enruta nada.
- Un rol solo puede asignarse a una pertenencia de su organización; si no, `422`.
- Una pertenencia o un rol de otra organización da `404`, igual que una
  organización o un usuario de otro tenant.
- El tenant `default` conserva su slug y sigue activo.
- Los roles integrados no se pueden modificar ni eliminar.
- Solo se aceptan los códigos de permiso del catálogo RBAC.

## Migración desde las rutas anteriores

Todas las rutas anteriores se han movido o sustituido, sin alias. Los
identificadores no cambian: un recurso existente se direcciona con su UUID actual.

| Antes | Ahora |
|---|---|
| `GET /v1/healthz`, `GET /v1/permissions` | `GET /v1/xolo/healthz`, `GET /v1/xolo/permissions` |
| `GET /v1/tenants` | `GET /v1/xolo/tenants` |
| `POST /v1/tenants` `{slug, name, description, active}` | `PUT /v1/tenants/{tenantID}` `{slug, name, status}` con un UUID elegido por usted; `description` mediante `PATCH /v1/xolo/tenants/{tenantID}` |
| `GET /v1/tenants/{tenantID}` | `GET /v1/xolo/tenants/{tenantID}` |
| `PATCH /v1/tenants/{tenantID}` `{name, description, active}` | `PATCH /v1/xolo/tenants/{tenantID}` (mismo cuerpo), o `PUT /v1/tenants/{tenantID}` `{slug, name, status}` |
| `DELETE /v1/tenants/{tenantID}` | Eliminada: `PUT /v1/tenants/{tenantID}` con `"status": "suspended"` |
| `GET /v1/tenants/{tenantID}/organizations[/{orgID}]` | `GET /v1/xolo/tenants/{tenantID}/organizations[/{orgID}]` |
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}`; `description` y `currency` mediante `PATCH /v1/xolo/…/organizations/{orgID}`; el propietario mediante `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}`, una vez declarado con `PUT …/members/{userID}` |
| `PATCH /v1/tenants/{tenantID}/organizations/{orgID}` | `PATCH /v1/xolo/tenants/{tenantID}/organizations/{orgID}` (mismo cuerpo) |
| `DELETE /v1/tenants/{tenantID}/organizations/{orgID}` | Eliminada: `PUT …/organizations/{orgID}` con `"status": "suspended"` |
| `GET …/organizations/{orgID}/members[/{membershipID}]` | `GET /v1/xolo/…/organizations/{orgID}/members[/{membershipID}]` |
| `POST …/organizations/{orgID}/members` `{userId \| user, roleIds, builtinRoles}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}` `{role, status}`; roles personalizados mediante `PUT /v1/xolo/…/members/{membershipID}/roles` |
| `PUT …/members/{membershipID}/roles` | `PUT /v1/xolo/…/members/{membershipID}/roles` (mismo cuerpo) |
| `DELETE …/members/{membershipID}` | Eliminada: `PUT …/organizations/{orgID}/members/{userID}` con `"status": "suspended"` |
| `…/organizations/{orgID}/roles[/{roleID}]` (todos los métodos) | `/v1/xolo/…/organizations/{orgID}/roles[/{key}]`, con los cambios siguientes |
| `GET /v1/xolo/…/roles` (integrados y personalizados, `roleDTO` en camelCase) | `GET /v1/xolo/…/roles` enumera los roles personalizados como página de proyecciones; los roles integrados mediante `GET /v1/xolo/…/roles/builtin` |
| `GET /v1/xolo/…/roles/{roleID}` (`roleDTO` en camelCase) | Misma ruta: la representación snake_case y su `ETag` |
| `POST /v1/xolo/…/roles` `{name, description?, permissions?, modelGrants?}` | Misma ruta, la representación snake_case completa `{name, description, permissions, model_grants}`; `201` con `{key, representation, etag}` |
| `PUT /v1/xolo/…/roles/{roleID}` parcial `{name?, description?, permissions?, modelGrants?}` | Misma ruta, la representación completa, `If-Match` opcional; crea el rol con un UUID de su elección |
| `GET`, `PUT /v1/tenants/{tenantID}/users` | `GET`, `PUT /v1/xolo/tenants/{tenantID}/users` (mismos cuerpos) |
| `GET /v1/tenants/{tenantID}/users/{userID}` | `GET /v1/xolo/tenants/{tenantID}/users/{userID}` |
| `PATCH /v1/tenants/{tenantID}/users/{userID}` `{email, displayName, active}` | `PUT /v1/tenants/{tenantID}/members/{userID}` `{email, display_name, tenant_role, status}` |

**Detenga todos los servidores antes de actualizar.** La migración
`202610070001` añade los dominios, los roles de tenant y los estados de
pertenencia. Un servidor antiguo aún activo seguiría enrutando según el patrón
de host y daría acceso mediante pertenencias suspendidas. La migración no se
puede revertir. Con `XOLO_STORAGE_AUTO_MIGRATE=false`, detenga todos los
escritores, haga una copia de seguridad y ejecute
`bin/migrate apply -writers-stopped` antes de arrancar. El servidor realiza la
expansión de `XOLO_MULTITENANCY_HOST_PATTERN` en dominios al arrancar, en ambos
modos.

**La migración `202610080001` también exige detener todos los servidores.** Crea
las proyecciones y el flujo de eventos, y da a cada recurso existente su propia
revisión sin publicar eventos: los consumidores empiezan con un inventario. Un
servidor antiguo aún activo escribiría sin publicar, y las proyecciones, los
ETags y el flujo divergirían sin aviso. La migración no se puede revertir.

## Transacciones, auditoría y correlación

Cada mutación vuelve a comprobar los padres y realiza todas las escrituras en una
sola transacción. Cualquier error revierte también los cambios de usuarios existentes,
las asignaciones de roles y la auditoría. PostgreSQL usa `SERIALIZABLE`; los conflictos
de escritura SQLite y de serialización PostgreSQL repiten toda la operación con
esperas limitadas y cancelables. Solo una transacción que modifica una proyección
toma el bloqueo del flujo, al final (consulte [Almacenamiento y rendimiento](#almacenamiento-y-rendimiento)).

`mutation_audits` guarda un estado anterior y posterior por recurso realmente
modificado: tenant, dominio, organización, usuario, pertenencia o rol, incluidas asociaciones
de roles y eliminaciones en cascada. Los cambios sucesivos se agrupan; una operación
sin cambios no genera auditoría. Los estados excluyen secretos y marcas de tiempo
técnicas. El historial y su ámbito sobreviven a la eliminación del recurso. Los
UUID de auditoría no indican el orden de commit. Esta auditoría cubre las
mutaciones de provisioning y los inicios de sesión que crean, vinculan o
actualizan una cuenta, con esa cuenta como actor; no las lecturas ni las
operaciones habituales de la interfaz web. El estado de un usuario incluye su
identidad declarada: a diferencia de los eventos, la tabla de auditoría la
contiene.

`X-Request-ID` debe contener un único valor de 32 caracteres hexadecimales en
minúsculas. Si falta, se repite o es inválido, se genera otro. El valor seleccionado
se devuelve en la respuesta y se usa en logs, auditoría y eventos, sin cambiar en
los reintentos. El URI del actor procede exclusivamente del certificado autorizado.
Las llamadas internas sin actor HTTP usan `urn:xolo:operator:local` y una correlación
generada al inicio de la operación.

Los eventos locales de miembros y roles mantienen sus tipos y mensajes, incluidos
los eventos distintos de alta del miembro y asignación de roles. Se emiten solo
tras el commit mediante el mecanismo asíncrono existente; no son una outbox durable.
La auditoría, las proyecciones y el flujo de eventos se persisten de forma
atómica con el cambio. Las lecturas transaccionales acceden
a la base directamente. Tras el commit se invalidan los usuarios afectados, sus
claves secundarias y los tokens eliminados en cascada en la caché compartida.

La migración `202610060001` sigue a la migración UUID e incluye la tabla de auditoría
en instalaciones nuevas y actualizaciones. Su rollback rechaza borrar el historial.

## PKI de desarrollo y ejemplo

El certificado cliente debe incluir el URI que se configura en la lista autorizada:

```bash
mkdir -p dev-pki && cd dev-pki

# Certificate authority
openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
  -keyout ca.key -out ca.crt -subj "/CN=xolo-dev-ca"

# Server certificate
openssl req -newkey rsa:4096 -nodes -keyout server.key -out server.csr \
  -subj "/CN=localhost"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 365 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth")

# Client certificate
openssl req -newkey rsa:4096 -nodes -keyout client.key -out client.csr \
  -subj "/CN=control-plane"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 365 \
  -extfile <(printf "subjectAltName=URI:urn:xolo:client:control-plane\nextendedKeyUsage=clientAuth")
```

```bash
XOLO_SECRET_KEY=$(openssl rand -hex 32) \
XOLO_PROVISIONNING_API_ENABLED=true \
XOLO_PROVISIONNING_API_AUTHORIZED_URIS=urn:xolo:client:control-plane \
XOLO_PROVISIONNING_API_TLS_CERT_FILE=dev-pki/server.crt \
XOLO_PROVISIONNING_API_TLS_KEY_FILE=dev-pki/server.key \
XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=dev-pki/ca.crt \
bin/server

# Refused: no client certificate
curl -sk https://localhost:3003/v1/manifest

CURL="curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key -H Content-Type:application/json"

# Accepted: read the identifier of the default tenant
$CURL "https://localhost:3003/v1/xolo/tenants?slug=default"
TENANT=... # the id read above

# Create an organization with an identifier of your choice
ORG=$(uuidgen | tr A-Z a-z)
$CURL -X PUT "https://localhost:3003/v1/tenants/$TENANT/organizations/$ORG" \
  -d '{"slug":"acme","name":"Acme","status":"active"}'
```

En producción, utilice una autoridad de certificación gestionada (Vault, cert-manager, PKI interna) y rote los certificados de cliente.

## Fuera del alcance actual

- Los modelos LLM, modelos virtuales, middlewares, tokens de aplicación y parámetros de eventos: siguen gestionándose desde la interfaz web.
- Los alcances por certificado: cualquier URI autorizado administra la instancia completa.
- Eliminar tenants, dominios, organizaciones, pertenencias o recursos de negocio distintos de los roles personalizados: suspéndalos o desactívelos.
- Todavía no se genera ninguna especificación OpenAPI.
