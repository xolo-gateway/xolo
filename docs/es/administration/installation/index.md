# Instalación y despliegue

Antes de actualizar una base existente, siga el [procedimiento de migración UUID](./uuid-migration.md). Detenga todas las réplicas y workers antiguos.

Esta pagina aun no ha sido traducida. Consulte la [version en frances](https://xolo-gateway.org/latest/) para el contenido completo.

## Sondas de salud

Cada instancia responde sin autenticación en dos rutas, para un balanceador de
carga o un orquestador:

| Ruta       | Responde `200` cuando…                                       | Uso                      |
| ---------- | ------------------------------------------------------------ | ------------------------ |
| `/healthz` | el proceso atiende peticiones HTTP                           | sonda de vida (liveness) |
| `/readyz`  | la instancia también alcanza su base de datos (en 0,8 s)     | sonda de disponibilidad  |

`/readyz` responde `503` mientras la base de datos es inalcanzable: la
instancia sale del balanceador sin reiniciarse. Su resultado se comparte
durante 0,8 s, de modo que una avalancha de sondas solo cuesta una
comprobación a la base. Ambas rutas se atienden antes de la resolución del
inquilino, del registro, de las métricas HTTP y del limitador de tasa: una
sonda se dirige a la instancia por su propia dirección, que no es el dominio de
ningún inquilino.

En PostgreSQL, `/readyz` usa una conexión propia, fuera del pool de la
aplicación: un pool saturado por el tráfico no saca a todas las instancias del
balanceador a la vez. La sonda indica si la base responde, no si la instancia
está ocupada. Cuente una conexión más por instancia.

```yaml
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:3002/readyz"]
```
