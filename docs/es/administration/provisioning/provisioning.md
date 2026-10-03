# API de aprovisionamiento

Esta página aún no ha sido traducida. Consulte la [versión en francés](https://xolo-gateway.org/latest/) para el contenido completo.

## Extensiones de ciclo de vida y recursos de negocio

Ambas extensiones están desactivadas por defecto y se descubren mediante
`GET /v1/xolo/extensions`, fuera del manifiesto común. Activar
`XOLO_LIFECYCLE_ENABLED=true` solo cuando los consumidores acepten `deleted`,
la reconciliación por lectura y la reconstrucción tras pérdida de historial.
Detener las réplicas antiguas y coordinar la configuración de todos los escritores.

DELETE de un tenant, organización o miembro devuelve 202. El ámbito permanece
legible y congelado hasta recibir la confirmación del archivo y cumplir la
retención. Las cascadas inmediatas anteriores se han eliminado; la eliminación
local devuelve 409 si la extensión está desactivada.

1. Guardar el ETag eliminado y descargar `{recurso}/deletion/export` mediante
   el listener mTLS autorizado.
2. Verificar `xolo-deletion/1`, ámbito, versión, inventario, cantidad,
   `complete: true` y SHA-256 de los bytes exactos del payload JSON.
3. Guardar y releer el archivo desde almacenamiento duradero protegido. Solo
   entonces enviar `POST {recurso}/purge-confirmation` con el ETag eliminado
   en `If-Match` y `{"export_sha256":"…"}`. Generar un archivo no confirma nada.
4. Consultar `GET {recurso}/deletion`. Un fallo revierte toda la purga y registra
   `purge_failed`; corregir el almacenamiento y dejar que el worker reintente,
   incluso después de reiniciar.

`XOLO_LIFECYCLE_RETENTION` vale `720h` por defecto y queda fijado al programar
la eliminación; `XOLO_LIFECYCLE_POLL_INTERVAL` vale `1m`. Un tenant eliminado
tiene prioridad sobre sus descendientes. Los UUID purgados quedan reservados.
Las identidades compartidas conservan sus sesiones en otros tenants sin
restablecer la cuenta eliminada. Dominios y membresías usan DELETE inmediato 204,
con comprobaciones de padres, último propietario e `If-Match`; una recreación
recibe un ETag nuevo.

`XOLO_BUSINESS_RESOURCES_ENABLED=true` activa PUT completo/GET/list de roles
personalizados, aplicaciones, cuotas, alertas y proveedores bajo
`/v1/xolo/tenants/{tenantID}/organizations/{orgID}`, salvo las cuotas en
`/v1/xolo/tenants/{tenantID}/quotas`. Comparten transacciones,
`XOLO_OWNERSHIP`, ETags, precondiciones y cursores. Las credenciales del
proveedor son solo de escritura, cifradas y excluidas de los eventos.

El [perfil completo y el inventario de limpieza](https://github.com/xolo-gateway/xolo/blob/main/internal/provisionning/LIFECYCLE.md)
describen campos, permisos, secretos, datos personales, prioridades y límites
de restauración. Proteger las claves de cifrado por separado; no reproducir
sesiones, tokens ni entregas archivadas. Los huecos del flujo devuelven 410 y
`history_lost`. No se pueden recuperar solicitudes ya autorizadas ni
notificaciones enviadas. El operador define y verifica por separado la
caducidad de exportaciones, copias de seguridad y registros externos.
