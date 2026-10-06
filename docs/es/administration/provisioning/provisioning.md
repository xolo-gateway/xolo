# API de provisioning

La API administra tenants, organizaciones, usuarios, miembros y roles en un
listener dedicado. Está desactivada por defecto y separada de la API pública.
Las rutas, los cuerpos de las solicitudes y las respuestas existentes se conservan.

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
Los errores conservan el formato `{"error":{"code":"…","message":"…"}}`.

## Transacciones, auditoría y correlación

Cada mutación vuelve a comprobar los padres y realiza todas las escrituras en una
sola transacción. Cualquier error revierte también los cambios de usuarios existentes,
las asignaciones de roles y la auditoría. PostgreSQL usa `SERIALIZABLE`; los conflictos
de escritura SQLite y de serialización PostgreSQL repiten toda la operación con
esperas limitadas y cancelables. No hay un bloqueo de publicación global.

`mutation_audits` guarda un estado anterior y posterior por recurso realmente
modificado: tenant, organización, usuario, pertenencia o rol, incluidas asociaciones
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
Con `XOLO_STORAGE_AUTO_MIGRATE=false`, detenga todos los escritores, haga una copia de
seguridad y ejecute `bin/migrate apply -writers-stopped` antes de arrancar. La migración
UUID y sus archivos de recuperación se conservan.

## Operaciones existentes

Descubra el tenant con `GET /v1/tenants?slug=default`. Los usuarios se gestionan en
`/v1/tenants/{tenantID}/users` y las organizaciones en
`/v1/tenants/{tenantID}/organizations`. Cada organización tiene subrutas `members`
y `roles`; `GET /v1/permissions` ofrece el catálogo RBAC y `GET /v1/healthz` comprueba
la salud. Todas requieren mTLS autorizado. Las colecciones usan `page` y `limit`.

Crear un administrador de organización nunca concede privilegios de plataforma.
El tenant `default` no se puede eliminar ni desactivar; una organización no puede
perder su último propietario. Las identidades y los padres se comprueban dentro
de la transacción. Crear otro tenant requiere `XOLO_MULTITENANCY_ENABLED=true`.

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
curl -sk https://localhost:3003/v1/permissions

# Accepted
curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key \
  "https://localhost:3003/v1/tenants?slug=default"

# Then, with the identifier it returned:
TENANT=... # the id read above

curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key \
  -X POST "https://localhost:3003/v1/tenants/$TENANT/organizations" \
  -d '{"slug":"acme","name":"Acme","owner":{"provider":"openid-connect","subject":"sub-123","email":"owner@acme.tld","displayName":"Owner"}}'
```

En producción, utilice una autoridad de certificación gestionada (Vault, cert-manager, PKI interna) y rote los certificados de cliente.

## Fuera del alcance actual

- Los proveedores, modelos LLM, modelos virtuales, middlewares, aplicaciones y sus tokens, cuotas, alertas y parámetros de eventos: siguen gestionándose desde la interfaz web.
- Los alcances por certificado: cualquier URI autorizado administra la instancia completa.
- El preaprovisionamiento por correo electrónico: el mecanismo de [invitación](../organisation/invitation/invitation.md) sigue siendo la vía por correo, desde la interfaz web.
- Todavía no se genera ninguna especificación OpenAPI.
