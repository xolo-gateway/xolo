# Equipos

## ¿Qué es un equipo?

Un equipo agrupa a miembros de la organización, los modelos y los middlewares que utilizan, y el presupuesto que consumen. Un miembro puede pertenecer a varios equipos.

Un rol indica lo que un miembro puede hacer. Un equipo indica con qué. El acceso a los modelos pasa por los equipos: un miembro utiliza un modelo si uno de sus equipos le da acceso. Los permisos `model:use:org` y `model:use:virtual` siguen siendo el único acceso que no pasa por un equipo. Abren todos los modelos de la organización, y el rol integrado **Membre** (Miembro) los incluye.

Los equipos no se anidan.

## Elegir el equipo al que se carga

Cada solicitud se atribuye a un solo equipo, o solo a la organización. El nombre del modelo lleva el equipo: `<organización>/<equipo>/<modelo>`, por ejemplo `acme/juridico/gpt-4o`. Un miembro de varios equipos elige el equipo al que se carga eligiendo el modelo, conversación por conversación, en cualquier cliente (OpenWebUI, los SDK de OpenAI y Anthropic, Claude Code).

- `/v1/models` lista un nombre por cada equipo que da acceso al modelo, en la forma calificada. Los miembros que tienen `model:use:org` también ven la forma corta `acme/gpt-4o`, que se carga solo a la organización.
- La forma corta `acme/gpt-4o` sigue aceptándose cuando solo existe un camino: el token está vinculado a un equipo, uno solo de los equipos del miembro da acceso al modelo, o el miembro tiene `model:use:org` (entonces solo se carga a la organización). El mismo miembro que nombra uno de sus equipos, por ejemplo `acme/legal/gpt-4o`, carga a ese equipo, aunque no dé acceso al modelo. Si no, la pasarela responde `400` con el código `model_ambiguous` y la lista de los nombres calificados que se pueden usar.
- Una clave de API o una aplicación vinculada a un equipo siempre carga a ese equipo, incluso cuando su titular tiene `model:use:org`, y no puede nombrar otro equipo.
- Un modelo virtual personal (`~/…`) carga al equipo elegido en su configuración. Si su autor deja ese equipo, si el equipo se elimina o si pertenece a otra organización que la de la solicitud, sus solicitudes se rechazan (`400 model_team_required`) hasta que el autor elija otro equipo.

El identificador de un equipo se puede modificar en su página, con las mismas reglas que en la creación. Los clientes guardan los nombres de modelos en su configuración: los que usan el identificador anterior dejan de funcionar y deben actualizarse. Los tokens, las aplicaciones, los modelos virtuales personales y los presupuestos vinculados al equipo lo siguen sin cambios.

> **Conflicto de nombres.** Un nombre de modelo puede contener una barra, como un modelo vLLM declarado con su identificador de Hugging Face (`meta/llama-3`). Xolo rechaza un equipo cuyo identificador sería el comienzo de un nombre de modelo existente (`meta`), y un nombre de modelo que empezaría por el identificador de un equipo seguido de `/`.

## Crear un equipo

1. Vaya a su organización y haga clic en **Équipes** (Equipos) en el menú **Gouvernance** (Gobernanza).
2. Haga clic en **Nouvelle équipe** (Nuevo equipo).
3. Indique el nombre, el identificador (minúsculas, cifras y guiones) y una descripción.
4. Haga clic en **Créer l'équipe** (Crear el equipo).

> **Nota**: Se necesita el permiso `teams:write` para crear un equipo o cambiar lo que utiliza.

## Dar acceso a modelos y middlewares

En la página del equipo, la sección **Modèles accessibles** (Modelos accesibles) lista los modelos reales y virtuales de la organización. Marque los que el equipo puede utilizar y haga clic en **Enregistrer** (Guardar).

Un modelo virtual delega el acceso a los modelos que su pipeline referencia en la configuración de sus nodos. Un equipo que tiene acceso al modelo virtual puede utilizar esos modelos a través de él, sin tener acceso directo a ellos. En cambio, un nombre de modelo calculado durante la solicitud (por un plugin o un nodo de valor) se comprueba contra el acceso del equipo. Consulte [Modelos virtuales](../virtual_model/virtual_model.md).

La sección **Middlewares de l'équipe** (Middlewares del equipo) vincula middlewares al equipo. Un middleware vinculado a al menos un equipo solo se aplica a las solicitudes de esos equipos. Se ejecuta después de los middlewares de la organización, que llevan las reglas de cumplimiento y se aplican a todas las solicitudes. Un middleware vinculado solo a este equipo no se puede desmarcar: sin equipo, se aplicaría a toda la organización. Vincúlelo primero a otro equipo, o elimínelo.

## Gestionar los miembros

La sección **Membres** (Miembros) lista los miembros del equipo. Elija un miembro de la organización y haga clic en **Ajouter à l'équipe** (Añadir al equipo). El icono de retirada retira a un miembro del equipo y elimina su presupuesto en el equipo. Sigue siendo miembro de la organización.

### Roles en el equipo

Un rol asignado a un miembro desde la página del equipo solo vale para ese equipo. Solo se aplican sus permisos marcados como «equipo» en el editor de roles:

| Permiso | Efecto en el equipo |
| --- | --- |
| `members:read`, `members:write` | Ver y gestionar los miembros del equipo y sus roles en el equipo |
| `quota:read`, `quota:write` | Ver el presupuesto del equipo y repartirlo entre sus miembros |
| `usage:read` | Ver el uso de las solicitudes del equipo |
| `events:read:all` | Ver los eventos de los miembros del equipo |
| `invites:read`, `invites:write` | Invitar a personas que se unen a la organización y al equipo, como miembros |

Así, un rol «Responsable de equipo» con `members:write`, `quota:write` y `usage:read`, asignado en el equipo jurídico, permite gestionar solo ese equipo.

La organización asigna, el equipo reparte. Un responsable de equipo nunca puede ampliar lo que el equipo utiliza ni su asignación. Solo puede asignar roles cuyos permisos tiene él mismo en el equipo.

## Presupuesto del equipo

El presupuesto de la organización, el del equipo y el de un miembro en el equipo se comprueban en cada solicitud. Una solicitud del equipo jurídico se rechaza en cuanto se alcanza uno de los niveles.

- **Asignación del equipo.** Se necesita `quota:write` en la organización para fijarla.
- **Reparto.** Se necesita `quota:write`, en la organización o en el equipo, para fijar el presupuesto de un miembro en el equipo (icono de lápiz en la fila del miembro), elegir la regla de reparto y el reparto a partes iguales.

La opción **Partager l'enveloppe à parts égales** (Repartir la asignación a partes iguales) divide el presupuesto del equipo entre sus miembros que no tienen presupuesto propio en el equipo.

El **reparto** restringe los presupuestos de los miembros al guardarlos:

| Regla | Efecto |
| --- | --- |
| **Libre** | Sin restricción: solo se aplican los límites comprobados en cada solicitud. |
| **Plafonnée** (Limitada) | El presupuesto de cada miembro es como máximo igual al del equipo. Su suma puede superarlo. |
| **Partagée** (Compartida) | La suma de los presupuestos de los miembros no supera el del equipo. |

La regla vale en ambos sentidos: bajar el presupuesto del equipo se rechaza si los presupuestos de los miembros ya no caben en él.

La misma regla se configura en el presupuesto de la organización, para las asignaciones de sus equipos. Consulte [Presupuestos y cuotas](../../../concepts/budgets-et-quotas.md).

## Claves de API y aplicaciones

- Una **clave de API** puede vincularse a uno de sus equipos al crearla, desde **Profil > Jetons API** (Perfil > Tokens de API). Si deja el equipo o si este se elimina, la clave se rechaza y la lista lo indica ("équipe quittée ou supprimée", equipo abandonado o eliminado): elimínela y cree otra. Una clave nunca cambia de equipo por sí sola.
- Una **aplicación** puede vincularse a un equipo desde su página. Entonces utiliza los modelos del equipo y todas sus solicitudes se cargan al equipo.
- Un **modelo virtual personal** elige en su configuración el equipo al que carga. No delega nada: cada modelo al que llama debe ser accesible para ese equipo.

Un equipo que tiene aplicaciones vinculadas no se puede eliminar. Vincúlelas primero a otro equipo. Lo mismo ocurre mientras un middleware esté vinculado solo a este equipo: sin equipo, se aplicaría a toda la organización. Vincúlelo a otro equipo, o elimínelo.

## Permisos

| Acción | Permiso requerido |
| --- | --- |
| Ver los equipos | `teams:read` |
| Crear, modificar, eliminar un equipo, elegir sus modelos y middlewares | `teams:write` |
| Gestionar los miembros de un equipo | `members:write`, en la organización o el equipo |
| Fijar la asignación de un equipo | `quota:write` en la organización |
| Repartir el presupuesto de un equipo | `quota:write`, en la organización o el equipo |

## Migración desde los accesos restringidos de los roles

Antes de los equipos, un rol personalizado podía autorizar modelos concretos. La actualización convierte cada rol personalizado que autorizaba modelos en un equipo, con los mismos modelos y con los titulares del rol como miembros. Una aplicación que tenía uno de esos roles se vincula a su equipo. Una aplicación que tenía varios recibe un equipo propio que reúne sus modelos. Nadie pierde acceso. Los roles conservan sus permisos.

Un miembro que tenía varios de esos roles pertenece ahora a varios equipos. Si dos de ellos dan acceso al mismo modelo, la forma corta del nombre se vuelve ambigua y el miembro debe nombrar el equipo.

> **Nota**: Detenga las demás instancias de Xolo durante la actualización. La migración elimina la tabla de accesos de los roles, que una instancia anterior todavía lee. No se puede deshacer: haga una copia de seguridad de la base de datos antes de la actualización para poder volver a la versión anterior.
