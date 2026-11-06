# Reglas de inscripción

## ¿Qué es una regla de inscripción?

Una regla de inscripción convierte una identidad en miembro de su organización al iniciar sesión, sin invitación ni acción de un administrador. Asocia una condición, sobre el dominio del correo o un claim OIDC, con un rol.

Una regla también se aplica a los tokens OIDC presentados directamente a la API: una aplicación de chat que transmite el token de cada persona no tiene nada más que hacer.

## Acceder a las reglas

1. Vaya a su organización: `/orgs/{slug}/`
2. Haga clic en **Reglas de inscripción** en el menú de administración

> **Nota**: consultar las reglas requiere `invites:read`, y gestionarlas `invites:write`, sobre toda la organización.

## Crear una regla

| Campo | Descripción |
|-------|-------------|
| **Nombre** | Para encontrarla en la lista |
| **Condición** | Dominio del correo o claim OIDC |
| **Nombre del claim** | Para una condición sobre un claim, por ejemplo `groups` |
| **Dominio o valor** | `example.org`, o el valor esperado del claim |
| **Rol** | Rol del nuevo miembro: cualquier rol de la organización salvo Propietario |
| **Activada** | Una regla desactivada deja de aplicarse |

### Dominio del correo

La regla solo se aplica a un correo que el proveedor de identidad afirma verificado (`email_verified`). El dominio se compara tal cual: `example.org` no cubre `sub.example.org`.

### Claim OIDC

El valor debe ser igual al del claim, o a uno de sus valores para una lista como `groups`. Un booleano se compara con `true` o `false`.

Los claims solo se leen para un proveedor que demuestra su emisor: en GitHub, el titular de la cuenta escribe su propio perfil. Las reglas solo los tienen en cuenta para los proveedores OIDC, Gitea con documento de descubrimiento y Google. En un inicio de sesión interactivo, los claims se guardan en la cookie de sesión: los que no caben (1 KiB en total, 32 valores por claim) se ignoran, y una advertencia en los registros del servidor los nombra. Un token de identidad presentado a la API no se guarda y conserva claims más grandes (16 KiB, 256 valores por claim); más allá, un claim que las reglas comparan se reduce a los valores que comparan, y una advertencia nombra el que sigue sin caber.

Para inscribir todas las cuentas de un proveedor de identidad, use el claim `iss` con la URL de su emisor.

## Qué hace una regla

Cuando una identidad coincide con una regla activada:

- se convierte en miembro de la organización, con el rol de la regla;
- la cuenta que crea su inicio de sesión se activa, incluso con `XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false`; una cuenta inactiva que ya existía sigue inactiva, pues un administrador pudo desactivarla;
- el evento `member.enrolled` se registra en el explorador de eventos.

## Una regla actúa una vez por cuenta

**Lo que un administrador cambie después prevalece sobre la regla.** Un miembro retirado de la organización no vuelve en su siguiente inicio de sesión, ni siquiera por otra regla de la organización, y una cuenta desactivada sigue desactivada.

Un miembro existente conserva su rol y su estado, incluso suspendido: la regla no los cambia ni activa la cuenta.

Una regla creada más tarde se aplica a las cuentas existentes en su siguiente inicio de sesión. Modificar una regla no la repite para las cuentas ya tratadas. Eliminar una regla conserva los miembros que inscribió, y la organización los recuerda: una regla creada de nuevo no hace volver a los que un administrador retiró.

## Límites

- Una regla modificada puede tardar hasta 30 segundos en aplicarse en las demás réplicas.
- Un rol personalizado no se puede eliminar mientras una regla lo asigne: modifique o elimine primero la regla.
- Cuando los miembros o las adhesiones pertenecen al sistema de control (`XOLO_OWNERSHIP`), las reglas no se aplican y sus páginas se ocultan.
- Con varios tenants, una regla solo afecta al tenant de su organización.
- Una regla que falla en el inicio de sesión que crea una cuenta se reintenta en las solicitudes siguientes. Pasada una hora ya no activa esa cuenta: un administrador debe entonces activarla.

Las reglas también se gestionan por la [API de provisioning](../../provisioning/provisioning.md#recursos-de-negocio).
