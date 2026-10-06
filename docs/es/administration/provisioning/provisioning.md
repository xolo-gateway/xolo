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

## Contrato común

| Método | Ruta | Cuerpo |
|---|---|---|
| `GET` | `/v1/manifest` | — devuelve `{"name","version","contract_version"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, `"display_name"` opcional |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |

- **Los identificadores** son UUID canónicos en minúsculas elegidos por el
  cliente. Cualquier otro valor se rechaza con `400 invalid_parameter`. Un
  miembro es un usuario: `memberID` es el identificador del usuario.
- **Cada `PUT` responde `200`**, creación incluida, con la representación
  almacenada. Un `PUT` idéntico al estado almacenado no cambia nada ni escribe
  auditoría: un cliente puede repetir todo su estado deseado.
- **Un `PUT` reemplaza la representación.** Un `display_name` omitido queda
  vacío. Los campos fuera del contrato — descripciones, monedas, identidades,
  roles de plataforma, roles personalizados — nunca se modifican.
- **Valores:** `status` es `active` o `suspended`; `tenant_role` es `owner` o
  `member`; el `role` de una organización es `owner`, `admin` o `member`. Los
  slugs se pasan a minúsculas; los nombres tienen de 1 a 200 caracteres, sin
  caracteres de control.
- **Los cuerpos** son un objeto JSON de cadenas con
  `Content-Type: application/json` (si no, `415 unsupported_media_type`), de
  1 MiB como máximo. Un campo desconocido, un valor que no es cadena o Unicode
  inválido da `400 invalid_json`; un campo obligatorio ausente o `null` da
  `400 invalid_representation`. Las rutas comunes no aceptan parámetros de
  consulta (`400 invalid_parameter`).

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

`PUT …/members/{memberID}` actualiza un usuario del tenant: email, nombre
visible, rol de tenant y estado. `suspended` desactiva la cuenta. La identidad
de autenticación y los roles de plataforma nunca se modifican.

**Un miembro se aprovisiona una vez que ha iniciado sesión.** Un `PUT` sobre un
usuario desconocido responde `404`: una cuenta creada sin identidad bloquearía el
primer inicio de sesión de esa persona con su email. El plano de control
encuentra la cuenta con `GET /v1/xolo/tenants/{tenantID}/users?provider=&subject=`,
o deja que se cree inactiva al iniciar sesión
(`XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false`) y la recupera con
`GET /v1/xolo/tenants/{tenantID}/users?active=false`.

`tenant_role` declara los propietarios del tenant; no concede ningún privilegio
de plataforma. Un tenant conserva un propietario activo en cuanto tiene uno:
degradar o suspender al último se rechaza con `409 last_owner`.

**Los administradores de plataforma están protegidos.** Todo `PUT` que
modificaría una cuenta con el rol de plataforma `admin` — email, nombre visible,
estado o rol de tenant — se rechaza con `409 platform_admin_protected`; un `PUT`
idéntico sigue respondiendo `200`. La misma protección se aplica a
`PUT /v1/xolo/tenants/{tenantID}/users`. El provisioning nunca actúa sobre
privilegios de plataforma.

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
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` | Roles integrados y personalizados |
| `POST` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` | Rol personalizado |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | |
| `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Solo roles personalizados |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Solo roles personalizados |
| `GET` | `/v1/xolo/tenants/{tenantID}/users` | `?provider=&subject=` para una búsqueda exacta; si no, `?search=&active=&page=&limit=` |
| `PUT` | `/v1/xolo/tenants/{tenantID}/users` | Upsert idempotente sobre `(provider, subject)`: `201` al crear, `200` si no |
| `GET` | `/v1/xolo/tenants/{tenantID}/users/{userID}` | |

## Errores

Todos los errores comparten el formato `{"error":{"code":"…","message":"…"}}`.

| Código | HTTP | Causa |
|---|---|---|
| `invalid_parameter` | 400 | Identificador que no es un UUID canónico, o parámetro de consulta en una ruta común |
| `invalid_json` | 400 | Ruta común: JSON mal formado, campo desconocido, valor que no es cadena, Unicode inválido |
| `invalid_representation` | 400 | Ruta común: campo obligatorio ausente o `null` |
| `invalid_hostname` | 400 | Nombre de host no en minúsculas, con puerto, dirección IP o etiqueta inválida |
| `invalid_request` | 400 | `/v1/xolo`: cuerpo mal formado, campo desconocido, parámetro de consulta inválido |
| `client_certificate_rejected` | 403 | Certificado o URI de cliente no autorizado |
| `not_found` | 404 | Recurso o ruta desconocidos, o recurso de otro tenant u otra organización |
| `parent_not_found` | 404 | El tenant, la organización o el miembro del que depende el recurso no existe en ese ámbito |
| `method_not_allowed` | 405 | Recurso conocido, método incorrecto |
| `unsupported_media_type` | 415 | Ruta común sin `Content-Type: application/json` |
| `conflict` | 409 | Identificador o nombre de host de otro tenant, slug ya usado o invariante de negocio |
| `last_owner` | 409 | El cambio dejaría un tenant o una organización sin propietario activo |
| `platform_admin_protected` | 409 | El cambio afecta a un administrador de plataforma |
| `unprocessable` | 422 | Valor bien formado pero rechazado por el dominio |
| `rate_limited` | 429 | Presupuesto por URI superado; reintente tras los segundos de `Retry-After` |
| `internal_error` | 500 | Error inesperado |

Los mensajes siempre se construyen explícitamente: trazas, errores SQL, rutas de
archivos, detalles TLS y secretos nunca llegan al cliente.

## Invariantes

- El provisioning **nunca** concede ni modifica privilegios de plataforma. Un
  usuario creado mediante `PUT /v1/xolo/tenants/{tenantID}/users` recibe
  exactamente el rol de plataforma `user`, los roles de plataforma nunca se
  modifican y un administrador de plataforma no se modifica en absoluto.
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
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}`; `description` y `currency` mediante `PATCH /v1/xolo/…/organizations/{orgID}`; el propietario mediante `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}` una vez que haya iniciado sesión |
| `PATCH /v1/tenants/{tenantID}/organizations/{orgID}` | `PATCH /v1/xolo/tenants/{tenantID}/organizations/{orgID}` (mismo cuerpo) |
| `DELETE /v1/tenants/{tenantID}/organizations/{orgID}` | Eliminada: `PUT …/organizations/{orgID}` con `"status": "suspended"` |
| `GET …/organizations/{orgID}/members[/{membershipID}]` | `GET /v1/xolo/…/organizations/{orgID}/members[/{membershipID}]` |
| `POST …/organizations/{orgID}/members` `{userId \| user, roleIds, builtinRoles}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}` `{role, status}`; roles personalizados mediante `PUT /v1/xolo/…/members/{membershipID}/roles` |
| `PUT …/members/{membershipID}/roles` | `PUT /v1/xolo/…/members/{membershipID}/roles` (mismo cuerpo) |
| `DELETE …/members/{membershipID}` | Eliminada: `PUT …/organizations/{orgID}/members/{userID}` con `"status": "suspended"` |
| `…/organizations/{orgID}/roles[/{roleID}]` (todos los métodos) | `/v1/xolo/…/organizations/{orgID}/roles[/{roleID}]` (mismos cuerpos) |
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

## Transacciones, auditoría y correlación

Cada mutación vuelve a comprobar los padres y realiza todas las escrituras en una
sola transacción. Cualquier error revierte también los cambios de usuarios existentes,
las asignaciones de roles y la auditoría. PostgreSQL usa `SERIALIZABLE`; los conflictos
de escritura SQLite y de serialización PostgreSQL repiten toda la operación con
esperas limitadas y cancelables. No hay un bloqueo de publicación global.

`mutation_audits` guarda un estado anterior y posterior por recurso realmente
modificado: tenant, dominio, organización, usuario, pertenencia o rol, incluidas asociaciones
de roles y eliminaciones en cascada. Los cambios sucesivos se agrupan; una operación
sin cambios no genera auditoría. Los estados excluyen secretos y marcas de tiempo
técnicas. El historial y su ámbito sobreviven a la eliminación del recurso. Los
UUID de auditoría no indican el orden de commit. Esta auditoría cubre únicamente
mutaciones de provisioning, no lecturas ni operaciones habituales de la interfaz web.

`X-Request-ID` debe contener un único valor de 32 caracteres hexadecimales en
minúsculas. Si falta, se repite o es inválido, se genera otro. El valor seleccionado
se devuelve en la respuesta y se usa en logs, auditoría y eventos, sin cambiar en
los reintentos. El URI del actor procede exclusivamente del certificado autorizado.
Las llamadas internas sin actor HTTP usan `urn:xolo:operator:local` y una correlación
generada al inicio de la operación.

Los eventos locales de miembros y roles mantienen sus tipos y mensajes, incluidos
los eventos distintos de alta del miembro y asignación de roles. Se emiten solo
tras el commit mediante el mecanismo asíncrono existente; no son una outbox durable.
Solo la auditoría se persiste de forma atómica. Las lecturas transaccionales acceden
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

- Los proveedores, modelos LLM, modelos virtuales, middlewares, aplicaciones y sus tokens, cuotas, alertas y parámetros de eventos: siguen gestionándose desde la interfaz web.
- Los alcances por certificado: cualquier URI autorizado administra la instancia completa.
- Crear un miembro antes de su primer inicio de sesión: un miembro se aprovisiona una vez que ha iniciado sesión. El mecanismo de [invitación](../organisation/invitation/invitation.md) sigue siendo la vía por correo, desde la interfaz web.
- Eliminar tenants, dominios, organizaciones o pertenencias: suspéndalos.
- Las escrituras condicionales (`ETag`/`If-Match`), las lecturas comunes paginadas, el flujo de eventos y los webhooks.
- Todavía no se genera ninguna especificación OpenAPI.
