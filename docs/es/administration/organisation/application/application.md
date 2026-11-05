# Aplicaciones

## ¿Qué es una aplicación?

Una aplicación es una configuración M2M (máquina a máquina) que permite a servicios externos conectarse a Xolo. Cada aplicación tiene sus propios tokens de API para la autenticación.

### Casos de uso

- **OpenWebUI**: integración con la interfaz web OpenWebUI
- **Scripts automatizados**: scripts o servicios que consultan la API de Xolo
- **CI/CD**: integración en pipelines de desarrollo
- **Aplicaciones a medida**: cualquier aplicación que necesite un acceso programático

## Acceder a las aplicaciones

1. Vaya a su organización: `/orgs/{slug}/`
2. Haga clic en **Applications** en el menú de administración

> **Nota**: Necesita el permiso `applications:write` para crear o modificar aplicaciones.

## Crear una aplicación

1. Haga clic en **Nouvelle application** (Nueva aplicación)
2. Complete la información:

| Campo                | Descripción                                             |
| -------------------- | ------------------------------------------------------- |
| **Nom** (Nombre)     | Nombre de la aplicación (p. ej. "OpenWebUI Production") |
| **Description**      | Descripción opcional                                    |
| **Équipe** (Equipo)  | Equipo al que está vinculada la aplicación (opcional)   |

3. Haga clic en **Enregistrer** (Guardar).

Una aplicación vinculada a un [equipo](../equipes/equipes.md) usa los modelos del equipo, y todas sus solicitudes se cargan al presupuesto del equipo. No puede nombrar a otro equipo. Sus roles siguen determinando las secciones de administración a las que accede y, con `model:use:org`, el acceso a todos los modelos de la organización.

## Gestionar los tokens de API

Cada aplicación puede tener varios tokens de API.

### Generar un token

1. Abra la aplicación en modo edición
2. En la sección **Jetons d'API** (Tokens de API), complete:
   - **Libellé** (Etiqueta): un nombre descriptivo (p. ej. "Token de producción")
   - **Expiration (jours)** (Caducidad en días): periodo de validez (p. ej. 365 para 1 año)
3. Haga clic en **Générer un jeton** (Generar un token)

### Revocar un token

Para revocar un token (invalidarlo de inmediato):

- Haga clic en el icono **Révoquer** (Revocar, papelera) en la fila del token y confirme

> **Atención**: Un token revocado no se puede recuperar. Hay que generar uno nuevo.

### Caducidad de los tokens

| Situación                     | Comportamiento                                          |
| ----------------------------- | ------------------------------------------------------- |
| **Fecha de caducidad fijada** | El token deja de ser válido en esa fecha                |
| **Sin caducidad**             | El token sigue siendo válido hasta su revocación manual |

## Modificar o desactivar una aplicación

1. Haga clic en **Modifier** (Modificar) en la fila de la aplicación
2. Cambie la información:
   - **Nom** (Nombre): nuevo nombre de la aplicación
   - **Description**: nueva descripción
   - **Active**: marque o desmarque para activarla o desactivarla

> **Nota**: Una aplicación desactivada ya no puede usar sus tokens de API.

## Eliminar una aplicación

1. Abra la aplicación en modo edición
2. En la **Zone de danger** (Zona de peligro), haga clic en **Supprimer l'application** (Eliminar la aplicación)

> **Atención**: Esta acción también elimina todos los tokens asociados. Los servicios que usan esos tokens pierden el acceso de inmediato.

## Tokens de aplicación frente a tokens de usuario

Xolo ofrece dos tipos de tokens de autenticación:

| Característica  | Tokens de aplicación                                   | Tokens de API de usuario                  |
| --------------- | ------------------------------------------------------ | ----------------------------------------- |
| **Creación**    | Por un administrador, en los ajustes de la aplicación  | Por cada usuario, en su perfil            |
| **Caducidad**   | Configurable (p. ej. 365 días)                         | Configurable, o ilimitada                 |
| **Caso de uso** | Servicios M2M (OpenWebUI, scripts)                     | Acceso personal a la API                  |
| **Identidad**   | Se identifica la aplicación, no el usuario             | Se identifica al usuario                  |

### Recomendaciones

- Use los **tokens de aplicación** para los servicios automatizados (CI/CD, scripts, OpenWebUI)
- Use los **tokens de API de usuario** para el acceso personal a la API

## Uso de los tokens de API

### Formato de autenticación

Envíe el token en la cabecera HTTP `Authorization`:

```bash
Authorization: Bearer <su_token_api>
```

### Ejemplo con curl

```bash
curl -X POST https://xolo.example.com/v1/chat/completions \
  -H "Authorization: Bearer xlo_abc123def456" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mi-org/gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Alcance de un token y endpoint `GET /api/v1/models`

Un token (de aplicación o de usuario) está vinculado a **una sola organización**: aquella en la que se emitió. Este alcance se aplica igual a todas las solicitudes autenticadas con el token, incluida `GET /api/v1/models`, que solo devuelve los modelos activados de esa organización.

En la práctica:

- **Token de aplicación**: `GET /api/v1/models` devuelve los modelos activados de la organización de la aplicación.
- **Token de un usuario de varias organizaciones**: `GET /api/v1/models` solo devuelve los modelos activados de la organización del token (las demás pertenencias del usuario se ignoran, como en el proxy). Los modelos virtuales personales (`~/...`) siguen una regla distinta: se incluyen en cuanto el usuario tiene el permiso `personal-vm:create` (o el rol owner) en **al menos una** de las organizaciones del alcance anterior. Sin embargo, la lista no se filtra por organización: el modelo de datos (`internal/core/model/personal_virtual_model.go`) vincula cada modelo virtual personal a su `user_id` sin columna `OrgID`, por lo que se devuelven todos los `~/...` del usuario.
- **Sesión OIDC (usuario humano)**: `GET /api/v1/models` devuelve la unión de los modelos activados de todas las organizaciones de las que el usuario es miembro.
- **Equipos**: un modelo accesible a través de un equipo se lista como `<organización>/<equipo>/<modelo>`, una vez por equipo; un modelo accesible mediante `model:use:org` se lista como `<organización>/<modelo>`. Un token vinculado a un equipo solo ve los nombres de su equipo.
- **Sesión creada con `POST /auth/token/login`**: la sesión se crea a partir de un token de Xolo (de usuario o de aplicación) y `authn.User.OrgID` contiene la organización del token (`internal/http/middleware/authn/token/login.go`). `GET /api/v1/models` solo devuelve entonces los modelos activados de esa organización, como en el caso "Token" correspondiente (las demás pertenencias del usuario se ignoran; para una aplicación, es precisamente la organización de la aplicación). Este caso solo se distingue del anterior por el canal de autenticación (una cookie de sesión en lugar de la cabecera `Authorization`).

### Capacidades y precio de los modelos listados

Cada modelo de `GET /api/v1/models` sigue el formato de OpenRouter:

- `description`;
- `context_length`;
- `top_provider.context_length` y `top_provider.max_completion_tokens`, la ventana de contexto y el máximo de tokens que el modelo puede escribir en una respuesta, para dimensionar `max_tokens`. Un modelo virtual publica las ventanas más pequeñas de sus candidatos, o su sobrecarga `MaxOutputTokens`. Una ventana desconocida se omite, y solo los modelos de completado tienen este objeto. `per_request_limits` sigue siendo el límite de tokens fijado por el operador;
- `architecture.input_modalities`, que contiene `image` cuando el modelo lee imágenes;
- `supported_parameters`, que contiene `tools` y `reasoning` según sus capacidades;
- `pricing.prompt` y `pricing.completion`, en unidades monetarias por cada 1.000 tokens.

Xolo añade un objeto `xolo`:

```json
"xolo": {"capabilities_declared": true, "currency": "EUR"}
```

- `capabilities_declared` vale `false` para un modelo virtual cuyas capacidades no se pueden deducir de su pipeline y que no declara ninguna en su catálogo. Su ficha se lee entonces como un modelo gratuito de solo texto: esos valores nulos significan "desconocido", no "ausente".
- `currency` es la divisa del proveedor del modelo. Falta cuando el precio no se conoce, por ejemplo para un modelo virtual no deducido, para candidatos facturados en divisas diferentes, o cuando no se pudo leer la lista de proveedores.

> **Nota**: Antes de la corrección del error [#48](https://github.com/xolo-gateway/xolo/issues/48),
> un token de aplicación obtenía una lista vacía en `GET /api/v1/models`
> porque el usuario "fantasma" asociado a la aplicación no tiene ninguna
> pertenencia a una organización. Ya está corregido: la aplicación ve los
> modelos de su organización, como ya hacía el proxy.

## Integración con OpenWebUI

OpenWebUI se puede configurar para usar Xolo como backend LLM.

### Configuración

En el archivo de configuración de OpenWebUI (`openwebui.ini` o variables de entorno):

```ini
# URL base de Xolo
XOLO_BASE_URL=https://xolo.example.com

# Token de API de la aplicación Xolo
XOLO_API_TOKEN=xlo_su_token_de_aplicacion

# Modelo por defecto (opcional)
DEFAULT_MODEL=mi-org/gpt-4o
```

### Variables de entorno

También puede usar variables de entorno:

```bash
export XOLO_BASE_URL=https://xolo.example.com
export XOLO_API_TOKEN=xlo_su_token_de_aplicacion
```

### Comprobación

Una vez configurado, los modelos de Xolo deberían aparecer en la lista de modelos disponibles en OpenWebUI.

## Permisos

| Acción                         | Permiso requerido    |
| ------------------------------ | -------------------- |
| Consultar las aplicaciones     | `applications:read`  |
| Crear, modificar, eliminar     | `applications:write` |
