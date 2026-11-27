# Presupuestos y cuotas

Los presupuestos limitan el gasto de una organización, un equipo o un miembro. Una vez alcanzado un límite, las nuevas solicitudes a la API se bloquean.

> Los presupuestos solo se aplican a los proveedores en modo **Pago por uso**. Los proveedores en modo suscripción tienen sus propios límites de plan.

## Jerarquía de presupuestos

Los presupuestos funcionan en varios niveles:

| Nivel | Descripción |
| --- | --- |
| Organización | Presupuesto global de la organización |
| Miembro | Presupuesto individual de un miembro |
| Equipo | Asignación de un [equipo](../organisation/equipes.md), todos sus miembros incluidos |
| Miembro en un equipo | Presupuesto de un miembro para las solicitudes que dirige a un equipo |

Una solicitud atribuida a un equipo se comprueba contra cada nivel de su camino: el presupuesto de la organización, el del miembro, el del equipo y el del miembro en el equipo. Se rechaza en cuanto se alcanza uno de ellos. Una solicitud atribuida solo a la organización no lee los niveles de equipo.

Cada nivel puede definir un presupuesto **diario**, **mensual** y/o **anual**. Un campo vacío significa "ilimitado".

## Presupuesto efectivo

Para cada periodo, Xolo toma el **mínimo** de los presupuestos definidos:

- Si la organización tiene un presupuesto mensual de 100 € y un miembro uno de 50 € → el miembro queda limitado a 50 €/mes.
- Si el miembro no tiene presupuesto personal → usa el presupuesto de la organización.

## Reparto equitativo del presupuesto

La opción **Partager le budget équitablement** (Repartir el presupuesto equitativamente) (en los [Ajustes](../organisation/parametres.md) de la organización) divide automáticamente el presupuesto de la organización entre todos sus miembros.

**Ejemplo**: una organización con 500 €/mes y 5 miembros → cada miembro dispone de 100 €/mes.

Esta regla solo se aplica **a los miembros sin cuota individual**:

1. si un miembro tiene una cuota personal → se usa su cuota;
2. si no → se usa la cuota repartida (presupuesto de la organización ÷ número de miembros).

> El reparto se recalcula automáticamente cada vez que cambia el número de miembros.

Un equipo tiene la misma opción, en su página: su asignación se divide entonces entre sus miembros sin presupuesto propio en el equipo.

La división es entera y se redondea hacia abajo, a la millonésima de la unidad monetaria: repartido entre 3 miembros, un presupuesto de 1 € da 0,333333 € a cada uno. El resto no se puede gastar.

Un equipo que reparte su asignación pero ya no tiene miembros no tiene nada que dividir: una solicitud que todavía se le atribuye, por ejemplo de una aplicación vinculada, queda limitada por la asignación completa.

## Reparto entre niveles

El presupuesto de una organización limita los de sus equipos, y el de un equipo los de sus miembros, según la regla de **reparto** elegida en el nivel superior. La regla se aplica al guardar un presupuesto, tanto de un hijo como del nivel superior:

| Regla | Efecto |
| --- | --- |
| **Libre** (por defecto) | Ninguna restricción: solo se aplican los límites comprobados en cada solicitud. |
| **Plafonnée** (Limitada) | Cada presupuesto hijo es como máximo igual al del nivel superior, periodo por periodo. Su suma puede superarlo. |
| **Partagée** (Repartida) | La suma de los presupuestos hijos no supera la del nivel superior, periodo por periodo: la asignación se reparte de verdad. |

Por lo tanto, se rechaza reducir el presupuesto de un nivel superior si los presupuestos hijos ya guardados dejan de caber en él: primero hay que reducirlos.

Un hijo sin presupuesto propio en un periodo no está limitado: sigue acotado por el nivel superior en cada solicitud.

## Consultar su propio presupuesto

Un usuario consulta su propio presupuesto en una organización con `GET /api/v1/quota?organization=<orgID>`. Esta ruta acepta las mismas autenticaciones que el proxy: token de usuario, ID token OIDC o sesión. Así, una aplicación puede mostrar a sus usuarios su consumo reenviando su ID token.

La respuesta solo cubre el nivel del usuario, es decir, el presupuesto que se compara con su propio contador:

- su cuota personal si la tiene (`"source": "personal"`);
- si no, su parte del presupuesto de la organización, si esta lo reparte equitativamente (`"shared"`);
- si no, ningún presupuesto (`"none"`).

Los presupuestos de la organización, de los equipos y de los miembros de equipo tienen sus propios contadores y no se devuelven. Por lo tanto, pueden bloquear una solicitud antes que el presupuesto del usuario.

Cuando el presupuesto de la organización es inferior al del usuario, las solicitudes se detienen en la cifra de la organización: `remaining` es entonces solo un límite superior, y el rechazo puede indicarse en el nivel del usuario (`User daily budget exceeded`, por ejemplo), ya que su contador se compara con el menor de los dos.

```json
{
  "organization_id": "00000000-0000-0000-0000-000000000102",
  "source": "personal",
  "currency": "EUR",
  "periods": {
    "daily":   {"budget": 1000000, "spent": 120000, "remaining": 880000, "resets_at": "2026-10-14T00:00:00+02:00"},
    "monthly": {"budget": null, "spent": 2400000, "remaining": null, "resets_at": "2026-11-01T00:00:00+01:00"},
    "yearly":  {"budget": null, "spent": 9800000, "remaining": null, "resets_at": "2027-01-01T00:00:00+01:00"}
  }
}
```

Los importes están en millonésimas de la unidad monetaria. `budget` y `remaining` valen `null` cuando el periodo es ilimitado, y `remaining` nunca baja de cero. `spent` cuenta el gasto del usuario en la organización desde el inicio del periodo, sin el uso cubierto por una suscripción. Los periodos siguen la zona horaria del servidor.

La organización se elige en este orden:

1. la del token, para un token vinculado a una organización;
2. si no, el parámetro `organization`;
3. si no, la única organización del usuario.

Sin parámetro, un usuario de varias organizaciones recibe `400`. Una organización de la que no es miembro activo, o distinta de la del token, da `404`. Un token de aplicación recibe `403`, ya que su presupuesto es el de la aplicación. No se requiere ningún permiso: cada uno solo consulta su propio presupuesto.

Al igual que `GET /api/v1/models`, esta ruta no está sujeta al límite de solicitudes por dirección IP. Las aplicaciones suelen llamarla desde una única dirección para todos sus usuarios.

## Permisos asociados

| Acción | Permiso requerido |
| --- | --- |
| Consultar los presupuestos | `quota:read` |
| Modificar los presupuestos | `quota:write` |

Consulte el tutorial [Presupuesto](../organisation/budget.md) para los pasos a seguir.
