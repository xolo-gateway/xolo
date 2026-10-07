# Configuración

Esta pagina aun no ha sido traducida. Consulte la [version en frances](https://xolo-gateway.org/latest/) para el contenido completo.

## XOLO_STORAGE_AUTO_MIGRATE

Aplica migraciones al arrancar (por defecto: `true`). Con `false`, comprueba el esquema sin migrarlo. Consulte el [procedimiento UUID](./uuid-migration.md); detenga todas las réplicas antiguas.

## Sesiones OIDC y cierre de sesión back-channel

Un inicio de sesión interactivo mediante un proveedor OAuth2/OIDC abre una sesión registrada en la base de datos; la cookie solo lleva su identificador. Una cookie copiada no sobrevive al cierre de sesión, y una sesión sigue siendo válida tras un reinicio y entre réplicas siempre que todas compartan `XOLO_HTTP_SESSION_KEYS`. Una sesión caduca tras `XOLO_HTTP_SESSION_COOKIE_MAX_AGE` (por defecto: `24h`), que debe ser positivo o el servidor no arranca. Cada réplica elimina las entradas caducadas cada 10 minutos.

Un inicio de sesión debe comenzar en Xolo (`/auth/oidc/providers/{proveedor}`) y volver al mismo navegador en un plazo de 15 minutos. Un inicio de sesión iniciado por el proveedor de identidad, o un retorno sin la cookie fijada al principio, falla con `sign-in start missing` en el registro: el usuario debe volver a iniciar sesión desde Xolo.

Un proveedor OIDC puede revocar sesiones mediante [OpenID Connect Back-Channel Logout](https://openid.net/specs/openid-connect-backchannel-1_0.html) en `https://{host}/auth/oidc/providers/{proveedor}/backchannel-logout`:

- solo lo aceptan los proveedores que prueban su emisor, con un identificador de cliente y un `jwks_uri` válido (proveedores OIDC con nombre, Gitea con `DISCOVERY_URL`, Google); los demás responden 404;
- el `logout_token` debe estar firmado (RS256, RS384 o RS512) por el emisor, para el identificador de cliente, y contener `sub` y `jti`. La revocación es **por sujeto**: se cierran todas las sesiones de ese emisor y sujeto, en todos los tenants, y se rechaza un inicio de sesión comenzado antes. Un token que solo lleva `sid` se rechaza (400);
- un token solo se acepta dentro de los 5 minutos siguientes a su emisión; un token ya procesado responde 200 sin efecto. Los relojes de las réplicas y del proveedor deben coincidir con un margen de 5 minutos;
- en modo multi-tenant sirve cualquier dominio activo. La ruta no está sujeta a la limitación por IP: cada solicitud se autentica con su token firmado.

El cierre de sesión duradero solo cubre estas sesiones interactivas. Un ID token OIDC presentado a la API (`oidctoken`) sigue siendo válido hasta su `exp` más `XOLO_HTTP_AUTHN_OIDCTOKEN_EXPIRY_LEEWAY`; un token de acceso opaco (`oauth2token`) hasta que caduca su entrada de caché (`XOLO_HTTP_AUTHN_OAUTH2TOKEN_CACHE_TTL`, 60 s); una sesión `/auth/token/login` durante la vida de su cookie. Un cierre de sesión local no cierra la sesión en el proveedor de identidad. Consulte la [versión en francés](https://xolo-gateway.org/latest/) para más detalles.
