# Actualización a UUID y migraciones sin conexión

La migración `202610020001` convierte los identificadores de tenants,
organizaciones y usuarios a UUID. Conserva los UUID existentes, las relaciones y
los roles de plataforma. Los demás identificadores (tokens, modelos, nodos de
grafos, etc.) conservan su formato.
Los identificadores de aplicaciones siguen siendo xids, incluidos los valores
`scope_id` de cuotas con `scope = 'application'` y los atributos de eventos `application_id`.

**Detenga todos los servidores, réplicas, workers y procesos antiguos que escriban
en la base antes de actualizar. Las actualizaciones progresivas no son compatibles.**
Un binario antiguo puede seguir escribiendo IDs retirados en registros de uso y
cuotas, dejando filas huérfanas y gastos fuera de la contabilidad. El bloqueo de
migración coordina los nuevos procesos de migración, pero no detiene los binarios
antiguos. El arranque muestra una advertencia explícita.

Las esperas de bloqueo conservan su comportamiento actual. Con el tiempo de
espera de SQLite predeterminado de 5 segundos (`busy_timeout`), 11 intentos y diez
pausas de 100 ms dan una ventana de reintento de aproximadamente 56 segundos bajo
contención; otra configuración cambia esa duración. PostgreSQL no tiene un tiempo
máximo de espera para el bloqueo consultivo definido por la aplicación: la espera
termina al adquirir el bloqueo, cancelar el contexto o vencer un tiempo límite de
la base o la sesión.

## Procedimiento recomendado

1. Haga una copia coherente con las herramientas de su base. En SQLite incluya
   el estado del WAL; copiar solamente un archivo `.sqlite` activo no basta.
2. Ensaye los comandos siguientes sobre una copia restaurada.
3. Detenga todos los escritores de la base real y haga una copia final.
4. Genere y revise un plan contra esa base detenida, resuelva todos los
   diagnósticos y aplique exactamente el plan guardado.
5. Arranque únicamente los nuevos binarios y compruebe acceso, organizaciones,
   tokens API, alertas y totales de cuotas antes de reabrir el tráfico.

Los archivos de distribución y la imagen Docker incluyen `xolo-migrate`.
Desde el código fuente, `make build-migrate` genera `bin/migrate`; utilice esa ruta
en los ejemplos. Solo se requiere `XOLO_STORAGE_DATABASE_DSN`. El comando no carga
`.env` automáticamente. Indique el archivo SQLite existente o el DSN PostgreSQL
mediante esa variable.

```bash
export XOLO_STORAGE_DATABASE_DSN=/data/data.sqlite
xolo-migrate diagnose
xolo-migrate plan -out recovery.json
# Revise recovery.json y añada las correcciones explícitas necesarias.
xolo-migrate diagnose -plan recovery.json
xolo-migrate apply -plan recovery.json -writers-stopped
export XOLO_STORAGE_AUTO_MIGRATE=false
xolo-server
```

`diagnose` y `plan` utilizan una instantánea de solo lectura y nunca migran la
base. Un diagnóstico bloqueante devuelve un código distinto de cero y un informe
JSON. `plan` guarda el artefacto aunque queden problemas pendientes, para que
pueda corregirlos. El archivo se crea con permisos `0600` y nunca sobrescribe uno
existente. Guarde el mapping y reutilice el mismo artefacto al reintentar.

`apply` ejecuta toda la cadena de migraciones, incluidos sus marcadores, en una
transacción y revalida el plan bajo bloqueo. Un fallo revierte todo. Reaplicar el
mismo plan completado es idempotente; un artefacto distinto se rechaza después de
migrar. El artefacto también queda registrado en la base como punto de recuperación.

Para una instalación nueva o una actualización sin decisiones manuales, utilice
`xolo-migrate apply -writers-stopped` sin plan. Una base nueva no necesita `plan`,
que espera el esquema de tenants, usuarios y organizaciones de la versión anterior.

## Resolver diagnósticos

| Caso | Resolución |
| --- | --- |
| IDs antiguos, incluidos valores no xid como `org-acme` | Conversión automática. Conserve el mapping `ids`; los UUID válidos no se cambian. |
| Emails que solo difieren por mayúsculas o espacios dentro de un tenant | Se conservan exactamente, incluidas mayúsculas y espacios. Esta migración no normaliza emails ni fusiona cuentas. |
| Comparaciones EventQL exactas sobre `user`, `org`, `actor_id`, `user_id`, `org_id` y otros atributos de ID reconocidos | Se reescriben con la familia correcta. Los selectores indexados aceptan `user`/`org`; `user_id`/`actor_id` son filtros de atributos. También se actualizan los atributos históricos de eventos. |
| Referencias de grafos en campos de ID reconocidos o campos `value` exactos | Reescritura automática; se conservan los IDs de nodos y las aristas. |
| Expresión regular EventQL con un ID antiguo en un selector o atributo de ID reconocido, script/configuración opaca o ID ambiguo | El informe identifica tabla, fila, columna y ruta JSON. Añada una entrada `serialized_overrides` como se muestra abajo. |
| Consulta o JSON inválido | Proporcione una corrección serializada válida. Selectores desconocidos como `{user_id="..."}` deben convertirse a un selector reconocido o filtro de atributo. |
| Mapping incompleto, obsoleto, UUID inválido o duplicado | Regenere un plan todavía no aplicado contra la base detenida y revíselo. Nunca cambie un mapping ya aplicado. |
| ID primario vacío o relación huérfana | Repare los datos tras ensayar sobre una copia y vuelva a planificar. El diagnóstico nunca descarta filas. |

Los filtros de línea (`|=`, `|~`, `!=`, `!~`) buscan en `events.message`, que la
migración conserva sin cambios; por tanto, no se reescriben ni se notifican.

Los atributos de eventos solo se reescriben bajo las claves de ID reconocidas:
`user`, `user_id`, `actor_id`, `owner_id`, `member_user_id`, `created_by_user_id`,
`org`, `org_id`, `organization_id` y `tenant_id`. Las demás claves, incluidas las
definidas por plugins como `tenant_user`, conservan sus valores originales.
Antes de migrar, `diagnose` y `plan` indican posibles IDs antiguos en estos valores,
incluso dentro de un texto, en `notices`. Se agrupan por clave, con el total de
valores distintos y hasta cinco ejemplos ordenados. Son avisos informativos y no
bloquean `apply`; solo los elementos de `issues` lo bloquean. La migración automática
y `apply` también los registran en los logs con nivel INFO. Por ejemplo:

```text
unmapped event attribute: events.attributes [key "tenant_user"]: 1 distinct values; examples: "user-alice"
```

Revise los filtros de alertas que utilizan estos atributos personalizados. Si
procede, use una corrección serializada de `events.attributes` para modificar un
valor, o valores `before`/`after` idénticos para confirmar texto literal. Una
corrección explícita elimina el aviso de ese evento, pero sigue sujeta a las
comprobaciones habituales de JSON y de la coincidencia de `before`. Los planes
siguen en la versión 2.

La eliminación normal de un usuario conserva referencias históricas:
propietarios de alertas de organización, creadores de invitaciones, ámbitos
personales de secretos de plugins (`~:<userID>`) y claves OAuth de `mcp-bridge`
(`oauth:<userID>`). La migración reescribe estos valores si el usuario tiene un
mapping y los conserva sin cambios si ya no existe. Un ámbito antiguo de alerta
vacío o NULL significa organización. Los propietarios de alertas personales y
las demás relaciones obligatorias siguen bloqueando la migración si quedan
huérfanos. El ámbito de organización de un secreto de plugin debe seguir apuntando
a una organización. Los eventos, registros de uso y contadores de cuotas conservan
su política histórica. No se descartan filas ni valores cifrados; cualquier
referencia a un ID antiguo incluido en el mapping que permanezca en estas columnas
relacionales sigue provocando un fallo de verificación.

Los diagnósticos relacionales y de mapping se agrupan por tabla/columna, ámbito
y categoría. Cada grupo indica el total de valores distintos, hasta cinco ejemplos
ordenados entre comillas y el número de ejemplos omitidos:

```text
orphan: quota.scope_id [references users; scope = 'user']: 7 distinct values; examples: "missing-0", "missing-1", "missing-2", "missing-3", "missing-4"; 2 omitted
missing mapping key: users.id: 1 distinct values; examples: "user-alice"
invalid UUID: users.id: 1 distinct values; examples: "user-alice" -> "bad-uuid"
```

También se identifican las claves inesperadas del mapping. Los diagnósticos de
destinos duplicados indican el UUID de destino y los IDs de origen, incluidos
ambos orígenes de una colisión entre dos IDs. Las referencias históricas de usuarios
eliminados descritas arriba no generan diagnósticos de relación huérfana.

Los planes utilizan la **versión 2** y contienen únicamente los mappings `ids`
y las correcciones opcionales `serialized_overrides`. Los planes de versión 1
se rechazan: regenérelos con `xolo-migrate plan -out recovery-v2.json` sobre la base
detenida, anterior a la migración, y revise cada corrección. No cambie el número
de versión a mano. Los campos desconocidos se rechazan.

Se conservan los emails, las identidades provider/subject y todas las asignaciones
de roles de plataforma y membresía, incluso varios roles integrados. Los dominios,
nuevos conceptos de roles/estados y contadores de publicación tendrán su propia
migración posterior.

Los mappings se cargan por lotes en una tabla temporal indexada. Cada referencia
relacional declarada se reescribe mediante una operación SQL; los campos
serializados se leen en páginas de 1 000 filas y se actualizan por lotes. Todos
los lotes permanecen en la misma transacción atómica. El análisis de grafos reutiliza
un buscador de subcadenas por bytes, construido una vez por recorrido serializado
a partir de los IDs modificados; las referencias opacas con puntuación o IDs
solapados siguen necesitando correcciones explícitas. El bloqueo solo se usa para
migraciones; las operaciones ordinarias mantienen sus transacciones y caché configurada.

Para una corrección serializada, añada esta lista al plan existente. Obtenga el
nuevo UUID de `ids.users` o de la familia correspondiente:

```json
"serialized_overrides": [{
  "table": "alerts",
  "id": "id-alerta-existente",
  "column": "query",
  "before": "{user=~\"id-antiguo-usuario\"}",
  "after": "{user=\"UUID-DEL-PLAN\"}"
}]
```

`before` debe coincidir exactamente con el campo almacenado. Una edición posterior
invalida el plan. `after` sustituye todo el campo; incluya todas las correcciones y
conserve la configuración restante. Valores idénticos de `before`/`after` confirman
un texto deliberadamente literal. Solo se permiten consultas de alertas, grafos
de modelos virtuales/personales o middlewares y atributos históricos de eventos.
Se rechazan destinos desconocidos y correcciones duplicadas.

## Arranque y recuperación

`XOLO_STORAGE_AUTO_MIGRATE` es `true` por defecto y conserva la migración automática
al arrancar. Esto **no** permite una actualización progresiva. Con `false`, el
arranque y los accesos posteriores al store solo comprueban el historial: una
migración pendiente o desconocida impide arrancar sin cambiar el esquema.

Si la migración automática se bloquea, mantenga los escritores detenidos y use el
comando para preparar, corregir, diagnosticar y aplicar un plan. Un fallo de la
transacción deja intacta la base anterior. Tras una conversión correcta, volver a
un binario antiguo requiere restaurar la copia previa; no existe migración inversa
de UUID. Los consumidores externos que almacenan IDs de Xolo deben actualizarlos
con el mapping. Las rutas de provisioning no cambian en esta etapa; su sustitución
corresponde a otra evolución.
